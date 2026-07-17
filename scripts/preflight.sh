#!/usr/bin/env bash
set -u

env_file="${1:-.env.production}"
offline=false
if [ "${2:-}" = "--offline" ]; then
  offline=true
fi
failures=0
warnings=0

pass() { printf 'PASS  %s\n' "$1"; }
warn() { printf 'WARN  %s\n' "$1"; warnings=$((warnings + 1)); }
fail() { printf 'FAIL  %s\n' "$1"; failures=$((failures + 1)); }

read_env() {
  key="$1"
  value=""
  while IFS= read -r line || [ -n "$line" ]; do
    line=${line%$'\r'}
    case "$line" in
      "$key="*) value=${line#*=}; break ;;
    esac
  done < "$env_file"
  printf '%s' "$value"
}

printf 'PortFlow release preflight\n'
printf 'Environment: %s\n\n' "$env_file"

if [ ! -f "$env_file" ]; then
  fail "environment file does not exist"
  exit 1
fi

if command -v stat >/dev/null 2>&1; then
  mode=$(stat -c '%a' "$env_file" 2>/dev/null || true)
  if [ "$mode" = "600" ] || [ "$mode" = "400" ]; then
    pass "environment file permissions are restrictive ($mode)"
  else
    warn "environment file mode is ${mode:-unknown}; use chmod 600"
  fi
fi

postgres_password=$(read_env POSTGRES_PASSWORD)
if [ ${#postgres_password} -lt 32 ] || [ "$postgres_password" = "replace-with-a-long-random-hex-password" ]; then
  fail "POSTGRES_PASSWORD must be a replaced value with at least 32 characters"
elif ! printf '%s' "$postgres_password" | grep -Eq '^[A-Za-z0-9._~-]+$'; then
  fail "POSTGRES_PASSWORD should use URL-safe characters"
else
  pass "PostgreSQL password length and character set"
fi

mfa_key=$(read_env PORTFLOW_MFA_ENCRYPTION_KEY)
if printf '%s' "$mfa_key" | grep -Eq '^[A-Fa-f0-9]{64}$'; then
  pass "MFA secrets have a dedicated encryption key"
elif [ -z "$mfa_key" ] && printf '%s' "$postgres_password" | grep -Eq '^[A-Fa-f0-9]{64}$'; then
  warn "PORTFLOW_MFA_ENCRYPTION_KEY is absent; using the stable legacy PostgreSQL secret for this first v1.0.x upgrade"
elif ! printf '%s' "$mfa_key" | grep -Eq '^[A-Fa-f0-9]{64}$'; then
  fail "PORTFLOW_MFA_ENCRYPTION_KEY must contain exactly 64 hexadecimal characters"
fi

version=$(read_env PORTFLOW_VERSION)
if [ -z "$version" ] || [ "$version" = "dev" ]; then
  fail "PORTFLOW_VERSION must be a fixed release version"
else
  pass "release version is fixed ($version)"
fi

site=$(read_env PORTFLOW_SITE_ADDRESS)
case "$site" in
  ""|panel.example.com|*://*|*[[:space:]]*) fail "PORTFLOW_SITE_ADDRESS must be a real DNS hostname without a URL scheme" ;;
  *) pass "site hostname is configured ($site)" ;;
esac

email=$(read_env CADDY_EMAIL)
case "$email" in
  ""|admin@example.com|*@example.com|*@example.net|*@example.org) fail "CADDY_EMAIL must be a real notification address" ;;
  *@*) pass "Caddy notification email is configured" ;;
  *) fail "CADDY_EMAIL is not a valid email-shaped value" ;;
esac

secure_cookies=$(read_env PORTFLOW_SECURE_COOKIES)
if [ "$secure_cookies" = "true" ]; then
  pass "Secure Cookie enforcement is enabled"
else
  fail "PORTFLOW_SECURE_COOKIES must be true for production"
fi

for key in PORTFLOW_HTTP_BIND PORTFLOW_HTTPS_BIND; do
  value=$(read_env "$key")
  if [ -z "$value" ]; then
    fail "$key is empty"
  else
    pass "$key is configured ($value)"
  fi
done

if command -v docker >/dev/null 2>&1; then
  pass "Docker CLI is available"
  if [ "$offline" = true ]; then
    warn "offline validation selected; Docker daemon reachability check was skipped"
  elif docker info >/dev/null 2>&1; then
    pass "Docker daemon is reachable"
  else
    fail "Docker daemon is not reachable"
  fi
  if docker compose version >/dev/null 2>&1; then
    pass "Docker Compose plugin is available"
    if docker compose --env-file "$env_file" config --quiet >/dev/null 2>&1; then
      pass "Compose configuration renders successfully"
    else
      fail "Compose configuration does not render; run docker compose --env-file $env_file config"
    fi
  else
    fail "Docker Compose plugin is unavailable"
  fi
else
  fail "Docker CLI is unavailable"
fi

available_kib=$(df -Pk . 2>/dev/null | awk 'NR==2 {print $4}')
if [ -n "${available_kib:-}" ] && [ "$available_kib" -ge 1048576 ] 2>/dev/null; then
  pass "at least 1 GiB filesystem space is available"
else
  fail "less than 1 GiB filesystem space is available or could not be measured"
fi

if bash -n deploy/agent/install.sh && grep -q '^\[Service\]$' deploy/systemd/portflow-agent.service; then
  pass "Agent installer syntax and systemd unit structure"
else
  fail "Agent installer or systemd unit validation failed"
fi

if command -v ss >/dev/null 2>&1; then
  for key in PORTFLOW_HTTP_BIND PORTFLOW_HTTPS_BIND; do
    value=$(read_env "$key")
    port=${value##*:}
    if [ -n "$port" ] && ss -H -ltnu 2>/dev/null | grep -Eq "[:.]${port}[[:space:]]"; then
      warn "$key port $port already appears in the host listener table; confirm whether it belongs to an existing PortFlow deployment"
    fi
  done
else
  warn "ss is unavailable; host listener collision check was skipped"
fi

printf '\nResult: %d failure(s), %d warning(s)\n' "$failures" "$warnings"
if [ "$failures" -ne 0 ]; then
  exit 1
fi
printf 'PortFlow release preflight passed.\n'
