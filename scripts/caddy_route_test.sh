#!/usr/bin/env bash
set -Eeuo pipefail

CONFIG_FILE="${1:-deploy/web/Caddyfile}"

awk '
  /^[[:space:]]*handle[[:space:]]+\/api\/\*[[:space:]]*\{/ { api_line = NR; in_api = 1; next }
  in_api && /reverse_proxy[[:space:]]+control:8080/ { api_proxy = NR }
  in_api && /^[[:space:]]*\}/ { in_api = 0 }
  /^[[:space:]]*handle[[:space:]]*\{/ { fallback_line = NR }
  END {
    if (api_line == 0 || api_proxy == 0 || fallback_line == 0 || api_line >= fallback_line) {
      print "Caddy API route must be a handle /api/* block before the frontend fallback" > "/dev/stderr"
      exit 1
    }
  }
' "$CONFIG_FILE"

printf 'Caddy API route ordering test passed.\n'
