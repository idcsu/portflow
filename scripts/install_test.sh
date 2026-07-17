#!/usr/bin/env bash
set -Eeuo pipefail

PROJECT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
# shellcheck source=../install.sh
source "$PROJECT_DIR/install.sh"

TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT

INSTALL_ROOT="$TEST_ROOT/portflow"
RELEASES_DIR="$INSTALL_ROOT/releases"
SHARED_DIR="$INSTALL_ROOT/shared"
BACKUP_DIR="$INSTALL_ROOT/backups"
CURRENT_LINK="$INSTALL_ROOT/current"
ENV_FILE="$SHARED_DIR/.env.production"
MANAGER_CONFIG="$SHARED_DIR/manager.conf"
COMMAND_LINK="$TEST_ROOT/bin/portflow"

mkdir -p "$TEST_ROOT/bin"
prepare_directories

[ "$(normalize_repository 'https://github.com/example/portflow.git')" = "example/portflow" ]
! normalize_repository 'example/portflow/invalid' >/dev/null
validate_tag 'v1.0.0'
! validate_tag '../v1' >/dev/null
validate_domain 'panel.internal.example'
! validate_domain 'https://panel.internal.example' >/dev/null
validate_email 'ops@internal.example'
validate_port '443'
! validate_port '70000' >/dev/null

MOCK_OS_RELEASE="$TEST_ROOT/os-release"
printf 'ID=debian\nVERSION_CODENAME=bookworm\n' > "$MOCK_OS_RELEASE"
PORTFLOW_OS_RELEASE_FILE="$MOCK_OS_RELEASE"
export PORTFLOW_OS_RELEASE_FILE
supported_docker_os
printf 'ID=ubuntu\nVERSION_CODENAME=noble\n' > "$MOCK_OS_RELEASE"
supported_docker_os
printf 'ID=fedora\nVERSION_CODENAME=forty-two\n' > "$MOCK_OS_RELEASE"
! supported_docker_os
unset PORTFLOW_OS_RELEASE_FILE

printf '%s\n' \
  'POSTGRES_PASSWORD=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef' \
  'PORTFLOW_VERSION=1.0.0' \
  'PORTFLOW_SECURE_COOKIES=true' \
  'PORTFLOW_SITE_ADDRESS=panel.internal.example' \
  'CADDY_EMAIL=ops@internal.example' \
  'PORTFLOW_HTTP_BIND=80' \
  'PORTFLOW_HTTPS_BIND=443' > "$ENV_FILE"
chmod 600 "$ENV_FILE"

set_env_value PORTFLOW_HTTPS_BIND 8443
[ "$(env_value PORTFLOW_HTTPS_BIND)" = "8443" ]
[ "$(stat -c '%a' "$ENV_FILE")" = "600" ]

write_manager_config example/portflow v1.0.0
[ "$(config_value PORTFLOW_REPOSITORY)" = "example/portflow" ]
[ "$(config_value PORTFLOW_RELEASE_TAG)" = "v1.0.0" ]

release_dir=$(acquire_release example/portflow v1.0.0 "$PROJECT_DIR")
[ -f "$release_dir/compose.yaml" ]
[ -x "$release_dir/install.sh" ]
switch_current "$release_dir"
[ "$(readlink -f "$CURRENT_LINK")" = "$release_dir" ]
[ "$(readlink -f "$COMMAND_LINK")" = "$release_dir/install.sh" ]

FLOW_ROOT="$TEST_ROOT/flow"
MOCK_BIN="$TEST_ROOT/mock-bin"
mkdir -p "$MOCK_BIN" "$FLOW_ROOT/bin"
printf '%s\n' \
  '#!/bin/sh' \
  'case "$*" in' \
  '  *pg_dump*) printf "fake-postgres-custom-backup\\n" ;;' \
  'esac' \
  'exit 0' > "$MOCK_BIN/docker"
chmod 0755 "$MOCK_BIN/docker"
PATH="$MOCK_BIN:$PATH"

INSTALL_ROOT="$FLOW_ROOT/app"
RELEASES_DIR="$INSTALL_ROOT/releases"
SHARED_DIR="$INSTALL_ROOT/shared"
BACKUP_DIR="$INSTALL_ROOT/backups"
CURRENT_LINK="$INSTALL_ROOT/current"
ENV_FILE="$SHARED_DIR/.env.production"
MANAGER_CONFIG="$SHARED_DIR/manager.conf"
COMMAND_LINK="$FLOW_ROOT/bin/portflow"
SOURCE_DIR_ARG="$PROJECT_DIR"
REPOSITORY_ARG="example/portflow"
RELEASE_TAG_ARG="v1.0.0"

prompt() {
  case "$1" in
    '控制面域名'* ) printf 'panel.internal.example' ;;
    'HTTPS 证书通知邮箱'* ) printf 'ops@internal.example' ;;
    'HTTP 主机端口'* ) printf '18080' ;;
    'HTTPS 主机端口'* ) printf '18443' ;;
    * ) printf '%s' "${2:-}" ;;
  esac
}

install_control
[ "$(env_value PORTFLOW_VERSION)" = "1.0.0" ]
[ "$(config_value PORTFLOW_RELEASE_TAG)" = "v1.0.0" ]
[ -L "$COMMAND_LINK" ]

RELEASE_TAG_ARG="v1.1.0"
update_control
[ "$(env_value PORTFLOW_VERSION)" = "1.1.0" ]
[ "$(config_value PORTFLOW_RELEASE_TAG)" = "v1.1.0" ]
find "$BACKUP_DIR" -type f -size +0c -print -quit | grep -q .

stable_release=$(readlink -f "$CURRENT_LINK")
if (
  RELEASE_TAG_ARG="v1.2.0"
  deploy_current() { return 1; }
  update_control
); then
  printf 'expected failed update to return a non-zero status\n' >&2
  exit 1
fi
[ "$(readlink -f "$CURRENT_LINK")" = "$stable_release" ]
[ "$(env_value PORTFLOW_VERSION)" = "1.1.0" ]
[ "$(config_value PORTFLOW_RELEASE_TAG)" = "v1.1.0" ]

MOCK_UFW_LOG="$TEST_ROOT/ufw.log"
export MOCK_UFW_LOG
printf '%s\n' \
  '#!/bin/sh' \
  'if [ "${1:-}" = "status" ]; then printf "Status: active\\n"; exit 0; fi' \
  'printf "%s\\n" "$*" >> "$MOCK_UFW_LOG"' \
  'exit 0' > "$MOCK_BIN/ufw"
chmod 0755 "$MOCK_BIN/ufw"
FIREWALL_STATE="$TEST_ROOT/firewall/rules"
prompt() {
  case "$1" in
    'Agent 转发监听端口'* ) printf '20000-20010' ;;
    '协议'* ) printf 'both' ;;
    * ) printf '%s' "${2:-}" ;;
  esac
}
confirm() { return 0; }
open_firewall_ports
[ "$(wc -l < "$FIREWALL_STATE")" -eq 2 ]
grep -Fq 'allow 20000:20010/tcp comment PortFlow managed' "$MOCK_UFW_LOG"
grep -Fq 'allow 20000:20010/udp comment PortFlow managed' "$MOCK_UFW_LOG"
restore_firewall_rules
[ ! -e "$FIREWALL_STATE" ]
grep -Fq -- '--force delete allow 20000:20010/tcp comment PortFlow managed' "$MOCK_UFW_LOG"

rm -f "$MOCK_BIN/ufw"
MOCK_FIREWALLD_LOG="$TEST_ROOT/firewalld.log"
MOCK_FIREWALLD_SERVICE="$TEST_ROOT/firewalld-service"
export MOCK_FIREWALLD_LOG MOCK_FIREWALLD_SERVICE
printf '%s\n' \
  '#!/bin/sh' \
  'case "$*" in' \
  '  "--state") exit 0 ;;' \
  '  *"--get-services"*) [ ! -f "$MOCK_FIREWALLD_SERVICE" ] || printf "portflow-managed\\n"; exit 0 ;;' \
  '  *"--new-service=portflow-managed"*) touch "$MOCK_FIREWALLD_SERVICE" ;;' \
  '  *"--delete-service=portflow-managed"*) rm -f "$MOCK_FIREWALLD_SERVICE" ;;' \
  '  *"--get-ports"*) exit 0 ;;' \
  'esac' \
  'printf "%s\\n" "$*" >> "$MOCK_FIREWALLD_LOG"' \
  'exit 0' > "$MOCK_BIN/firewall-cmd"
chmod 0755 "$MOCK_BIN/firewall-cmd"
prompt() {
  case "$1" in
    'Agent 转发监听端口'* ) printf '8443' ;;
    '协议'* ) printf 'tcp' ;;
    * ) printf '%s' "${2:-}" ;;
  esac
}
open_firewall_ports
grep -Fqx 'firewalld|8443|tcp|agent' "$FIREWALL_STATE"
grep -Fq -- '--service=portflow-managed --add-port=8443/tcp' "$MOCK_FIREWALLD_LOG"
restore_firewall_rules
[ ! -e "$FIREWALL_STATE" ]
grep -Fq -- '--service=portflow-managed --remove-port=8443/tcp' "$MOCK_FIREWALLD_LOG"

printf 'PortFlow installer tests passed.\n'
