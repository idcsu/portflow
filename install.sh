#!/usr/bin/env bash
set -Eeuo pipefail

# PortFlow 中文交互式控制面管理器。
# 系统级安装和防火墙变更必须先说明原因、影响与恢复方式，再获得明确确认。

PROGRAM_VERSION="1.0.1"
INSTALL_ROOT="${PORTFLOW_INSTALL_ROOT:-/opt/portflow}"
RELEASES_DIR="$INSTALL_ROOT/releases"
SHARED_DIR="$INSTALL_ROOT/shared"
BACKUP_DIR="$INSTALL_ROOT/backups"
CURRENT_LINK="$INSTALL_ROOT/current"
ENV_FILE="$SHARED_DIR/.env.production"
MANAGER_CONFIG="$SHARED_DIR/manager.conf"
COMMAND_LINK="${PORTFLOW_COMMAND_LINK:-/usr/local/bin/portflow}"
FIREWALL_STATE="${PORTFLOW_FIREWALL_STATE:-/var/lib/portflow-firewall/rules}"

ACTION="menu"
REPOSITORY_ARG=""
RELEASE_TAG_ARG=""
SOURCE_DIR_ARG=""

if (: </dev/tty) 2>/dev/null && (: >/dev/tty) 2>/dev/null; then
  exec 3</dev/tty 4>/dev/tty
else
  exec 3<&0 4>&1
fi

if [ -t 4 ]; then
  C_BLUE=$'\033[1;34m'
  C_GREEN=$'\033[1;32m'
  C_YELLOW=$'\033[1;33m'
  C_RED=$'\033[1;31m'
  C_RESET=$'\033[0m'
else
  C_BLUE="" C_GREEN="" C_YELLOW="" C_RED="" C_RESET=""
fi

info() { printf '%s[信息]%s %s\n' "$C_BLUE" "$C_RESET" "$*"; }
success() { printf '%s[完成]%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
warning() { printf '%s[提醒]%s %s\n' "$C_YELLOW" "$C_RESET" "$*"; }
error() { printf '%s[错误]%s %s\n' "$C_RED" "$C_RESET" "$*" >&2; }
die() { error "$*"; exit 1; }

usage() {
  cat <<'EOF'
PortFlow 中文部署管理器

用法：
  install.sh                         打开交互菜单
  install.sh install [选项]         安装控制面
  install.sh update [选项]          更新控制面
  install.sh settings               修改域名、邮箱和端口
  install.sh status                 查看状态
  install.sh logs                   查看日志
  install.sh backup                 备份 PostgreSQL
  install.sh restart                重启控制面
  install.sh rollback               回滚到以前安装的版本
  install.sh firewall               管理 Agent 转发端口防火墙规则
  install.sh uninstall              卸载
  install.sh check                  只读环境检查

选项：
  --repo OWNER/REPO                 GitHub 仓库，例如 acme/portflow
  --version VERSION                 发布版本，例如 1.0.1（对应标签 v1.0.1）
  --tag TAG                         直接指定 Git 标签
  --source DIRECTORY                从本地源码目录安装，用于开发或离线部署
  --root DIRECTORY                  安装根目录，默认 /opt/portflow
  --help                            显示帮助

环境变量：
  PORTFLOW_INSTALL_ROOT             安装根目录
  PORTFLOW_COMMAND_LINK             管理命令软链接位置

在 Debian/Ubuntu 上，脚本可在明确确认后从 Docker 官方仓库安装 Engine 和 Compose。
防火墙只在明确确认后添加带记录的 UFW/firewalld 端口规则，并提供恢复功能。
EOF
}

parse_args() {
  if [ $# -gt 0 ] && [[ "$1" != --* ]]; then
    ACTION="$1"
    shift
  fi
  while [ $# -gt 0 ]; do
    case "$1" in
      --repo) [ $# -ge 2 ] || die "--repo 缺少参数"; REPOSITORY_ARG="$2"; shift 2 ;;
      --version) [ $# -ge 2 ] || die "--version 缺少参数"; RELEASE_TAG_ARG="v$2"; shift 2 ;;
      --tag) [ $# -ge 2 ] || die "--tag 缺少参数"; RELEASE_TAG_ARG="$2"; shift 2 ;;
      --source) [ $# -ge 2 ] || die "--source 缺少参数"; SOURCE_DIR_ARG="$2"; shift 2 ;;
      --root)
        [ $# -ge 2 ] || die "--root 缺少参数"
        INSTALL_ROOT="$2"
        RELEASES_DIR="$INSTALL_ROOT/releases"
        SHARED_DIR="$INSTALL_ROOT/shared"
        BACKUP_DIR="$INSTALL_ROOT/backups"
        CURRENT_LINK="$INSTALL_ROOT/current"
        ENV_FILE="$SHARED_DIR/.env.production"
        MANAGER_CONFIG="$SHARED_DIR/manager.conf"
        shift 2
        ;;
      -h|--help) usage; exit 0 ;;
      *) die "未知参数：$1" ;;
    esac
  done
}

prompt() {
  local message="$1" default_value="${2:-}" answer
  if [ -n "$default_value" ]; then
    printf '%s [%s]：' "$message" "$default_value" >&4
  else
    printf '%s：' "$message" >&4
  fi
  IFS= read -r answer <&3 || answer=""
  printf '%s' "${answer:-$default_value}"
}

confirm() {
  local message="$1" default_answer="${2:-N}" answer suffix
  if [ "$default_answer" = "Y" ]; then suffix="Y/n"; else suffix="y/N"; fi
  answer=$(prompt "$message ($suffix)" "$default_answer")
  [[ "$answer" =~ ^[Yy]$ ]]
}

require_root() {
  [ "$(id -u)" -eq 0 ] || die "此操作需要 root 权限，请使用 sudo 重新运行"
  [[ "$INSTALL_ROOT" == /* ]] && [[ "$INSTALL_ROOT" != *'/../'* ]] && [[ "$INSTALL_ROOT" != *'/..' ]] \
    && [[ "$INSTALL_ROOT" != *'/./'* ]] && [[ "$INSTALL_ROOT" != *'/.' ]] && [[ "$INSTALL_ROOT" != *'//'* ]] \
    || die "安装根目录包含不安全的路径片段：$INSTALL_ROOT"
  case "$INSTALL_ROOT" in
    /|/bin|/boot|/dev|/etc|/home|/lib|/lib64|/proc|/root|/run|/sbin|/sys|/usr|/var)
      die "拒绝使用危险的安装根目录：$INSTALL_ROOT" ;;
    /*) ;;
    *) die "安装根目录必须是绝对路径" ;;
  esac
  [ ! -L "$INSTALL_ROOT" ] || die "安装根目录不能是软链接：$INSTALL_ROOT"
  [[ "$COMMAND_LINK" == /* ]] && [[ "$COMMAND_LINK" != *'/../'* ]] && [ "${COMMAND_LINK##*/}" = "portflow" ] \
    || die "管理命令路径必须是以 portflow 结尾的安全绝对路径"
}

has_command() { command -v "$1" >/dev/null 2>&1; }

compose() {
  docker compose --project-directory "$CURRENT_LINK" --env-file "$ENV_FILE" "$@"
}

config_value() {
  local key="$1"
  [ -f "$MANAGER_CONFIG" ] || return 0
  awk -F= -v wanted="$key" '$1 == wanted {sub(/^[^=]*=/, ""); print; exit}' "$MANAGER_CONFIG"
}

env_value() {
  local key="$1"
  [ -f "$ENV_FILE" ] || return 0
  awk -F= -v wanted="$key" '$1 == wanted {sub(/^[^=]*=/, ""); print; exit}' "$ENV_FILE"
}

set_env_value() {
  local key="$1" value="$2" temporary
  temporary=$(mktemp "$SHARED_DIR/.env.XXXXXX")
  awk -F= -v wanted="$key" -v replacement="$value" '
    BEGIN { found=0 }
    $1 == wanted { print wanted "=" replacement; found=1; next }
    { print }
    END { if (!found) print wanted "=" replacement }
  ' "$ENV_FILE" > "$temporary"
  chmod 600 "$temporary"
  mv -f "$temporary" "$ENV_FILE"
}

normalize_repository() {
  local repository="$1"
  repository="${repository#https://github.com/}"
  repository="${repository#http://github.com/}"
  repository="${repository%.git}"
  repository="${repository%/}"
  [[ "$repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || return 1
  printf '%s' "$repository"
}

validate_tag() { [[ "$1" =~ ^[A-Za-z0-9._-]+$ ]]; }
validate_domain() { [[ "$1" =~ ^[A-Za-z0-9.-]+$ ]] && [[ "$1" == *.* ]] && [ "$1" != "panel.example.com" ]; }
validate_email() { [[ "$1" =~ ^[^[:space:]@]+@[^[:space:]@]+\.[^[:space:]@]+$ ]] && [[ "$1" != *@example.com ]]; }
validate_port() { [[ "$1" =~ ^[0-9]+$ ]] && [ "$1" -ge 1 ] && [ "$1" -le 65535 ]; }

os_release_value() {
  local key="$1" release_file="${PORTFLOW_OS_RELEASE_FILE:-/etc/os-release}"
  [ -f "$release_file" ] || return 0
  awk -F= -v wanted="$key" '
    $1 == wanted {
      value=substr($0, index($0, "=") + 1)
      gsub(/^"|"$/, "", value)
      print value
      exit
    }
  ' "$release_file"
}

supported_docker_os() {
  local os_id
  os_id=$(os_release_value ID)
  [ "$os_id" = "debian" ] || [ "$os_id" = "ubuntu" ]
}

install_docker_official() {
  require_root
  local os_id codename architecture repository_url conflicts package repository_backup
  os_id=$(os_release_value ID)
  codename=$(os_release_value VERSION_CODENAME)
  [ "$os_id" = "debian" ] || [ "$os_id" = "ubuntu" ] || die "自动安装仅支持 Debian 和 Ubuntu"
  [[ "$codename" =~ ^[A-Za-z0-9._-]+$ ]] || die "无法识别系统版本代号，请按 Docker 官方文档手工安装"
  has_command apt-get || die "系统缺少 apt-get，无法自动安装"
  has_command dpkg || die "系统缺少 dpkg，无法自动安装"
  architecture=$(dpkg --print-architecture)
  case "$architecture" in amd64|arm64|armhf|ppc64el) ;; *) die "Docker 官方仓库不支持当前架构：$architecture" ;; esac

  printf '\n即将执行以下系统变更：\n'
  printf '  - 添加 Docker 官方 APT 签名密钥和 %s 软件源；\n' "$os_id"
  printf '  - 安装 docker-ce、containerd、Buildx 和 Compose 插件；\n'
  printf '  - 启用并启动 docker.service；Docker 会创建网桥和自己的主机防火墙规则。\n'
  printf '原因：PortFlow 控制面使用 Docker Compose 运行。\n'
  printf '网络影响：Docker 发布的容器端口可能绕过 UFW/firewalld 普通入站规则。\n'
  printf '恢复方式：先卸载 PortFlow，再按文档移除 Docker 软件包和官方软件源；不会由 PortFlow 卸载流程自动删除 Docker。\n\n'
  confirm "允许安装 Docker Engine 和 Compose？" "N" || die "用户取消 Docker 安装"

  conflicts=""
  for package in docker.io docker-compose docker-doc podman-docker containerd runc; do
    if dpkg-query -W -f='${Status}' "$package" 2>/dev/null | grep -q 'install ok installed'; then
      conflicts="$conflicts $package"
    fi
  done
  if [ -n "$conflicts" ]; then
    warning "Docker 官方软件包与以下已安装软件冲突：$conflicts"
    confirm "允许先卸载这些冲突软件包？现有容器数据不会自动删除" "N" || die "用户拒绝移除冲突软件包"
    # shellcheck disable=SC2086
    apt-get remove -y $conflicts
  fi

  apt-get update
  apt-get install -y ca-certificates curl
  install -m 0755 -d /etc/apt/keyrings
  if [ -e /etc/apt/keyrings/docker.asc ] || [ -e /etc/apt/sources.list.d/docker.sources ]; then
    repository_backup="/var/backups/portflow-docker-repository-$(date -u +%Y%m%dT%H%M%SZ)"
    install -d -m 0700 "$repository_backup"
    [ ! -e /etc/apt/keyrings/docker.asc ] || cp -a /etc/apt/keyrings/docker.asc "$repository_backup/docker.asc"
    [ ! -e /etc/apt/sources.list.d/docker.sources ] || cp -a /etc/apt/sources.list.d/docker.sources "$repository_backup/docker.sources"
    warning "原有 Docker 软件源文件已备份到 $repository_backup"
  fi
  curl -fsSL "https://download.docker.com/linux/$os_id/gpg" -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  repository_url="https://download.docker.com/linux/$os_id"
  {
    printf 'Types: deb\n'
    printf 'URIs: %s\n' "$repository_url"
    printf 'Suites: %s\n' "$codename"
    printf 'Components: stable\n'
    printf 'Architectures: %s\n' "$architecture"
    printf 'Signed-By: /etc/apt/keyrings/docker.asc\n'
  } > /etc/apt/sources.list.d/docker.sources
  apt-get update
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable --now docker
  docker info >/dev/null 2>&1 || die "Docker 已安装但服务不可访问，请检查 systemctl status docker"
  docker compose version >/dev/null 2>&1 || die "Docker Compose 插件安装后仍不可用"
  success "Docker Engine 和 Compose 已从官方仓库安装"
}

check_environment() {
  local failed=0
  printf '\nPortFlow 环境检查\n\n'
  if [ "$(uname -s)" = "Linux" ]; then success "操作系统：Linux"; else error "仅支持 Linux"; failed=1; fi
  if has_command bash; then success "Bash 可用"; else error "缺少 Bash"; failed=1; fi
  if has_command docker; then
    success "Docker CLI 可用"
    if docker info >/dev/null 2>&1; then success "Docker 服务可访问"; else warning "当前用户无法访问 Docker 服务"; fi
    if docker compose version >/dev/null 2>&1; then success "Docker Compose 插件可用"; else error "缺少 Docker Compose 插件"; failed=1; fi
  else
    if supported_docker_os; then
      warning "缺少 Docker；正式安装时可选择自动安装官方 Docker Engine 和 Compose"
    else
      error "缺少 Docker，且当前系统不支持自动安装；请按 Docker 官方文档手工安装"
      failed=1
    fi
  fi
  if has_command curl; then success "curl 可用"; else warning "远程安装需要 curl"; fi
  if has_command tar; then success "tar 可用"; else warning "远程安装需要 tar"; fi
  if has_command ss; then success "ss 可用于端口检查"; else warning "缺少 ss，将无法自动提示端口占用"; fi
  info "主机防火墙检测结果：$(detect_firewall)"
  printf '\n'
  if [ "$failed" -ne 0 ]; then return 1; fi
}

require_runtime() {
  if ! has_command docker || ! docker compose version >/dev/null 2>&1; then
    if supported_docker_os; then
      warning "没有检测到完整的 Docker Engine 和 Compose 插件"
      install_docker_official
    else
      die "当前系统不支持自动安装 Docker，请按 Docker 官方文档手工安装 Engine 和 Compose 插件"
    fi
  fi
  if ! docker info >/dev/null 2>&1; then
    if has_command systemctl && systemctl list-unit-files docker.service >/dev/null 2>&1; then
      warning "Docker 已安装但服务没有运行或当前无法访问"
      if confirm "允许启用并启动 docker.service？" "N"; then
        systemctl enable --now docker
      fi
    fi
  fi
  docker info >/dev/null 2>&1 || die "Docker 服务不可访问，请检查 systemctl status docker"
  docker compose version >/dev/null 2>&1 || die "Docker Compose 插件不可用"
}

detect_firewall() {
  if has_command ufw && LC_ALL=C ufw status 2>/dev/null | grep -q '^Status: active'; then
    printf 'ufw'
  elif has_command firewall-cmd && firewall-cmd --state >/dev/null 2>&1; then
    printf 'firewalld'
  elif has_command ufw; then
    printf 'ufw-inactive'
  elif has_command firewall-cmd; then
    printf 'firewalld-inactive'
  elif has_command nft && nft list ruleset 2>/dev/null | grep -q '[^[:space:]]'; then
    printf 'nftables'
  elif has_command iptables && iptables -S 2>/dev/null | grep -q '^-'; then
    printf 'iptables'
  else
    printf 'none'
  fi
}

validate_port_spec() {
  local value="$1" first last
  if [[ "$value" =~ ^[0-9]+$ ]]; then
    validate_port "$value"
    return
  fi
  [[ "$value" =~ ^([0-9]+)-([0-9]+)$ ]] || return 1
  first="${BASH_REMATCH[1]}"
  last="${BASH_REMATCH[2]}"
  validate_port "$first" && validate_port "$last" && [ "$first" -le "$last" ]
}

firewall_state_add() {
  local backend="$1" port_spec="$2" protocol="$3" record state_dir
  record="$backend|$port_spec|$protocol|agent"
  state_dir=${FIREWALL_STATE%/*}
  [ "$state_dir" != "$FIREWALL_STATE" ] || state_dir="."
  install -d -m 0700 "$state_dir"
  [ ! -L "$FIREWALL_STATE" ] || die "防火墙规则记录文件不能是软链接：$FIREWALL_STATE"
  touch "$FIREWALL_STATE"
  chmod 600 "$FIREWALL_STATE"
  grep -Fqx "$record" "$FIREWALL_STATE" 2>/dev/null || printf '%s\n' "$record" >> "$FIREWALL_STATE"
}

ufw_add_port() {
  local port_spec="$1" protocol="$2" ufw_spec
  ufw_spec="${port_spec/-/:}"
  ufw allow "$ufw_spec/$protocol" comment 'PortFlow managed' >/dev/null
  firewall_state_add ufw "$port_spec" "$protocol"
}

firewalld_service_exists() {
  firewall-cmd --permanent --get-services 2>/dev/null | tr ' ' '\n' | grep -qx 'portflow-managed'
}

firewalld_add_port() {
  local port_spec="$1" protocol="$2"
  if ! firewalld_service_exists; then
    firewall-cmd --permanent --new-service=portflow-managed >/dev/null
    firewall-cmd --permanent --service=portflow-managed --set-short='PortFlow managed ports' >/dev/null
    firewall-cmd --permanent --service=portflow-managed --set-description='Ingress ports explicitly approved for PortFlow Agent forwarding' >/dev/null
  fi
  firewall-cmd --permanent --query-service=portflow-managed >/dev/null 2>&1 \
    || firewall-cmd --permanent --add-service=portflow-managed >/dev/null
  firewall-cmd --permanent --service=portflow-managed --add-port="$port_spec/$protocol" >/dev/null
  firewall-cmd --reload >/dev/null
  firewall_state_add firewalld "$port_spec" "$protocol"
}

open_firewall_ports() {
  require_root
  local backend port_spec protocol protocols=()
  backend=$(detect_firewall)
  case "$backend" in
    ufw|firewalld) ;;
    ufw-inactive)
      die "检测到 UFW 但它未启用。脚本不会自动启用整个防火墙，以免锁断 SSH；请先人工规划并启用 UFW"
      ;;
    firewalld-inactive)
      die "检测到 firewalld 但服务未运行。请先人工确认区域和 SSH 规则，再启动 firewalld"
      ;;
    nftables|iptables)
      die "检测到 $backend 自定义规则集。脚本无法安全判断现有链和持久化方式，请按提示手工添加规则"
      ;;
    none)
      die "没有检测到启用的 UFW 或 firewalld；若端口仍不通，请检查云安全组、上级路由器或自定义防火墙"
      ;;
  esac

  while true; do
    port_spec=$(prompt "Agent 转发监听端口或范围（如 8080 或 20000-20100）" "")
    validate_port_spec "$port_spec" && break
    warning "端口必须是 1-65535，范围使用起始-结束格式"
  done
  protocol=$(prompt "协议（tcp/udp/both）" "tcp")
  case "$protocol" in
    tcp) protocols=(tcp) ;;
    udp) protocols=(udp) ;;
    both) protocols=(tcp udp) ;;
    *) die "协议只能是 tcp、udp 或 both" ;;
  esac

  printf '\n即将修改 %s：\n' "$backend"
  printf '  放行 Agent 入站端口 %s，协议 %s。\n' "$port_spec" "$protocol"
  printf '原因：PortFlow Agent 会在该端口监听转发流量，主机防火墙必须允许客户端入站。\n'
  printf '恢复方式：重新运行本脚本选择“防火墙管理 → 恢复全部 PortFlow 规则”。\n'
  printf '注意：云服务器安全组不受本脚本控制，仍需在云平台单独放行。\n\n'
  confirm "允许添加以上防火墙规则？" "N" || { warning "已取消，防火墙没有变化"; return 0; }
  for protocol in "${protocols[@]}"; do
    if [ "$backend" = "ufw" ]; then
      ufw_add_port "$port_spec" "$protocol"
    else
      firewalld_add_port "$port_spec" "$protocol"
    fi
  done
  success "防火墙规则已添加并记录到 $FIREWALL_STATE"
}

list_firewall_rules() {
  local backend
  backend=$(detect_firewall)
  printf '\n检测结果：%s\n' "$backend"
  if [ -s "$FIREWALL_STATE" ]; then
    printf 'PortFlow 记录的规则：\n'
    awk -F'|' '{printf "  - 后端=%s 端口=%s 协议=%s 用途=%s\n", $1, $2, $3, $4}' "$FIREWALL_STATE"
  else
    printf '没有找到 PortFlow 添加的防火墙规则记录。\n'
  fi
  printf '\n'
}

restore_firewall_rules() {
  require_root
  [ -s "$FIREWALL_STATE" ] || { warning "没有需要恢复的 PortFlow 防火墙规则"; return 0; }
  local backend port_spec protocol purpose ufw_spec failures=0
  printf '将只删除记录在 %s 中、由 PortFlow 明确添加的规则。\n' "$FIREWALL_STATE"
  confirm "确认恢复全部 PortFlow 防火墙修改？" "N" || return 0
  while IFS='|' read -r backend port_spec protocol purpose; do
    [ -n "$backend" ] && [ -n "$port_spec" ] && [ -n "$protocol" ] || continue
    case "$backend" in
      ufw)
        if has_command ufw; then
          ufw_spec="${port_spec/-/:}"
          ufw --force delete allow "$ufw_spec/$protocol" comment 'PortFlow managed' >/dev/null 2>&1 || failures=$((failures + 1))
        else
          failures=$((failures + 1))
        fi
        ;;
      firewalld)
        if has_command firewall-cmd && firewalld_service_exists; then
          firewall-cmd --permanent --service=portflow-managed --remove-port="$port_spec/$protocol" >/dev/null 2>&1 || failures=$((failures + 1))
        else
          failures=$((failures + 1))
        fi
        ;;
      *) failures=$((failures + 1)) ;;
    esac
  done < "$FIREWALL_STATE"
  if has_command firewall-cmd && firewalld_service_exists; then
    if [ -z "$(firewall-cmd --permanent --service=portflow-managed --get-ports 2>/dev/null)" ]; then
      firewall-cmd --permanent --remove-service=portflow-managed >/dev/null 2>&1 || true
      firewall-cmd --permanent --delete-service=portflow-managed >/dev/null 2>&1 || true
    fi
    firewall-cmd --reload >/dev/null 2>&1 || true
  fi
  if [ "$failures" -eq 0 ]; then
    rm -f "$FIREWALL_STATE"
    success "PortFlow 添加的防火墙规则已恢复"
  else
    die "有 $failures 条规则未能自动恢复，记录文件已保留，请人工核对后重试"
  fi
}

firewall_menu() {
  require_root
  local choice
  while true; do
    cat <<'EOF'

防火墙管理（用于 Agent 转发监听端口）
  1. 检测并查看记录
  2. 放行一个端口或端口范围
  3. 恢复全部 PortFlow 防火墙规则
  0. 返回
EOF
    choice=$(prompt "请选择" "0")
    case "$choice" in
      1) list_firewall_rules ;;
      2) open_firewall_ports ;;
      3) restore_firewall_rules ;;
      0) return 0 ;;
      *) warning "无效选择" ;;
    esac
  done
}

show_control_network_notice() {
  local backend
  backend=$(detect_firewall)
  printf '\n网络与防火墙提示：\n'
  printf '  - 当前主机防火墙检测结果：%s。\n' "$backend"
  printf '  - 控制面 80/443 由 Docker 发布；Docker 可能绕过 UFW/firewalld 的普通入站规则。\n'
  printf '  - 请把 Compose 发布地址、云安全组和上游路由器作为控制面真正的访问边界。\n'
  printf '  - Agent 转发端口请在对应节点运行“防火墙管理”，经确认后放行。\n\n'
}

generate_password() {
  if [ -r /dev/urandom ] && has_command od; then
    od -An -N32 -tx1 /dev/urandom | tr -d ' \n'
  elif has_command openssl; then
    openssl rand -hex 32
  else
    die "无法生成安全随机密码：需要 /dev/urandom 和 od，或 openssl"
  fi
}

write_manager_config() {
  local repository="$1" tag="$2" temporary
  temporary=$(mktemp "$SHARED_DIR/.manager.XXXXXX")
  {
    printf 'PORTFLOW_REPOSITORY=%s\n' "$repository"
    printf 'PORTFLOW_RELEASE_TAG=%s\n' "$tag"
  } > "$temporary"
  chmod 600 "$temporary"
  mv -f "$temporary" "$MANAGER_CONFIG"
}

write_initial_env() {
  local version="$1" domain email http_port https_port password
  while true; do
    domain=$(prompt "控制面域名（DNS 需已指向本机）" "")
    validate_domain "$domain" && break
    warning "请输入真实域名，例如 panel.company.com，不要包含 https://"
  done
  while true; do
    email=$(prompt "HTTPS 证书通知邮箱" "")
    validate_email "$email" && break
    warning "邮箱格式不正确"
  done
  while true; do
    http_port=$(prompt "HTTP 主机端口" "80")
    validate_port "$http_port" && break
    warning "端口必须在 1 到 65535 之间"
  done
  while true; do
    https_port=$(prompt "HTTPS 主机端口" "443")
    validate_port "$https_port" && [ "$https_port" != "$http_port" ] && break
    warning "端口必须有效且不能与 HTTP 端口相同"
  done
  password=$(generate_password)
  umask 077
  {
    printf 'POSTGRES_PASSWORD=%s\n' "$password"
    printf 'PORTFLOW_VERSION=%s\n' "$version"
    printf 'PORTFLOW_SECURE_COOKIES=true\n'
    printf 'PORTFLOW_SITE_ADDRESS=%s\n' "$domain"
    printf 'CADDY_EMAIL=%s\n' "$email"
    printf 'PORTFLOW_HTTP_BIND=%s\n' "$http_port"
    printf 'PORTFLOW_HTTPS_BIND=%s\n' "$https_port"
  } > "$ENV_FILE"
  chmod 600 "$ENV_FILE"
  unset password
}

prepare_directories() {
  install -d -m 0755 "$INSTALL_ROOT" "$RELEASES_DIR"
  install -d -m 0700 "$SHARED_DIR" "$BACKUP_DIR"
}

acquire_release() {
  local repository="$1" tag="$2" source_dir="$3" stage archive release_id release_dir
  stage=$(mktemp -d)
  archive="$stage/source.tar.gz"
  mkdir -p "$stage/source"
  if [ -n "$source_dir" ]; then
    [ -f "$source_dir/compose.yaml" ] && [ -f "$source_dir/scripts/preflight.sh" ] || {
      rm -rf "$stage"
      die "本地源码目录不完整：$source_dir"
    }
    has_command tar || { rm -rf "$stage"; die "本地安装需要 tar"; }
    info "复制本地源码：$source_dir" >&2
    tar -C "$source_dir" \
      --exclude='./.git' --exclude='./.env.production' --exclude='./backups' \
      --exclude='./bin' --exclude='./web/dist' --exclude='./web/node_modules' \
      -cf - . | tar -C "$stage/source" -xf -
  else
    has_command curl || { rm -rf "$stage"; die "远程安装需要 curl"; }
    has_command tar || { rm -rf "$stage"; die "远程安装需要 tar"; }
    info "从 GitHub 下载 $repository 的标签 $tag" >&2
    curl --fail --location --silent --show-error \
      "https://github.com/$repository/archive/refs/tags/$tag.tar.gz" -o "$archive" || {
        rm -rf "$stage"
        die "下载失败，请确认仓库和标签存在且网络可访问"
      }
    tar -xzf "$archive" --strip-components=1 -C "$stage/source" || {
      rm -rf "$stage"
      die "源码包解压失败"
    }
  fi
  [ -f "$stage/source/compose.yaml" ] && [ -x "$stage/source/scripts/preflight.sh" ] && [ -f "$stage/source/install.sh" ] || {
    rm -rf "$stage"
    die "源码包缺少 PortFlow 部署文件"
  }
  release_id="${tag}-$(date -u +%Y%m%dT%H%M%SZ)"
  release_dir="$RELEASES_DIR/$release_id"
  mkdir -p "$release_dir"
  cp -a "$stage/source/." "$release_dir/"
  chmod 0755 "$release_dir/install.sh" "$release_dir/scripts/preflight.sh"
  rm -rf "$stage"
  printf '%s' "$release_dir"
}

switch_current() {
  local release_dir="$1" temporary_link="$INSTALL_ROOT/.current.new"
  ln -sfn "$release_dir" "$temporary_link"
  mv -Tf "$temporary_link" "$CURRENT_LINK"
  ln -sfn "$CURRENT_LINK/install.sh" "$COMMAND_LINK"
}

run_preflight() {
  (cd "$CURRENT_LINK" && ./scripts/preflight.sh "$ENV_FILE")
}

wait_for_control() {
  local attempt
  for attempt in $(seq 1 30); do
    if compose exec -T control /portflow-control -healthcheck http://127.0.0.1:8080/api/v1/health >/dev/null 2>&1; then
      success "控制面健康检查通过"
      return 0
    fi
    sleep 2
  done
  error "控制面在 60 秒内没有通过健康检查"
  compose ps || true
  return 1
}

deploy_current() {
  run_preflight || return 1
  info "构建 PortFlow 镜像，这一步首次运行可能需要几分钟"
  compose build || return 1
  info "启动 PortFlow"
  compose up -d --remove-orphans || return 1
  wait_for_control
}

install_control() {
  require_root
  require_runtime
  [ ! -e "$CURRENT_LINK" ] || die "PortFlow 已安装；请使用更新功能"
  local repository tag version release_dir
  repository="$REPOSITORY_ARG"
  if [ -z "$SOURCE_DIR_ARG" ]; then
    repository="${repository:-$(prompt "GitHub 仓库（OWNER/REPO）" "")}" 
    repository=$(normalize_repository "$repository") || die "GitHub 仓库格式不正确"
  else
    repository="${repository:-local/source}"
  fi
  tag="${RELEASE_TAG_ARG:-v$PROGRAM_VERSION}"
  validate_tag "$tag" || die "发布标签包含不安全字符"
  version="${tag#v}"
  prepare_directories
  if [ ! -f "$ENV_FILE" ]; then write_initial_env "$version"; fi
  show_control_network_notice
  release_dir=$(acquire_release "$repository" "$tag" "$SOURCE_DIR_ARG")
  switch_current "$release_dir"
  write_manager_config "$repository" "$tag"
  if ! deploy_current; then
    error "安装启动失败，保留配置和日志以便排查"
    exit 1
  fi
  printf '\n'
  success "PortFlow $version 安装完成"
  printf '访问地址：https://%s\n' "$(env_value PORTFLOW_SITE_ADDRESS)"
  printf '管理命令：sudo portflow\n'
  warning "本次控制面安装没有修改防火墙；如无法访问，请核对 Docker 端口发布、云安全组和上游网络策略"
}

ensure_installed() {
  [ -L "$CURRENT_LINK" ] && [ -f "$ENV_FILE" ] && [ -f "$CURRENT_LINK/compose.yaml" ] || die "尚未发现完整的 PortFlow 安装"
}

backup_database() {
  require_root
  require_runtime
  ensure_installed
  local backup_file
  install -d -m 0700 "$BACKUP_DIR"
  backup_file="$BACKUP_DIR/portflow-$(date -u +%Y%m%dT%H%M%SZ).dump"
  info "正在备份 PostgreSQL"
  if compose exec -T postgres pg_dump -U portflow -d portflow --format=custom > "$backup_file" && [ -s "$backup_file" ]; then
    chmod 600 "$backup_file"
    success "数据库已备份到 $backup_file"
    printf '%s\n' "$backup_file"
  else
    rm -f "$backup_file"
    die "数据库备份失败或生成了空文件"
  fi
}

update_control() {
  require_root
  require_runtime
  ensure_installed
  local repository old_tag new_tag version new_release old_release
  repository="${REPOSITORY_ARG:-$(config_value PORTFLOW_REPOSITORY)}"
  repository=$(normalize_repository "$repository") || die "已保存的 GitHub 仓库格式不正确"
  old_tag="$(config_value PORTFLOW_RELEASE_TAG)"
  new_tag="${RELEASE_TAG_ARG:-$(prompt "目标版本标签" "$old_tag")}" 
  validate_tag "$new_tag" || die "发布标签包含不安全字符"
  version="${new_tag#v}"
  old_release=$(readlink -f "$CURRENT_LINK")
  backup_database >/dev/null
  new_release=$(acquire_release "$repository" "$new_tag" "$SOURCE_DIR_ARG")
  switch_current "$new_release"
  set_env_value PORTFLOW_VERSION "$version"
  write_manager_config "$repository" "$new_tag"
  if deploy_current; then
    success "PortFlow 已更新到 $version"
    info "旧版本仍保留在 $old_release，可从回滚菜单恢复"
    return 0
  fi
  error "新版本启动失败，正在自动回滚"
  switch_current "$old_release"
  set_env_value PORTFLOW_VERSION "${old_tag#v}"
  write_manager_config "$repository" "$old_tag"
  compose build || true
  compose up -d --remove-orphans || true
  wait_for_control || true
  die "更新失败，已切回 $old_tag；请查看日志确认状态"
}

show_status() {
  require_runtime
  ensure_installed
  printf '\nPortFlow 状态\n'
  printf '版本：%s\n' "$(env_value PORTFLOW_VERSION)"
  printf '地址：https://%s\n\n' "$(env_value PORTFLOW_SITE_ADDRESS)"
  compose ps
}

show_logs() {
  require_runtime
  ensure_installed
  local service lines
  service=$(prompt "服务（control/web/postgres/all）" "all")
  lines=$(prompt "显示最近多少行" "200")
  [[ "$lines" =~ ^[0-9]+$ ]] || die "日志行数必须是数字"
  case "$service" in
    control|web|postgres) compose logs --tail "$lines" "$service" ;;
    all) compose logs --tail "$lines" ;;
    *) die "服务名称不正确" ;;
  esac
}

restart_control() {
  require_root
  require_runtime
  ensure_installed
  compose restart
  wait_for_control
}

settings_menu() {
  require_root
  require_runtime
  ensure_installed
  local choice value
  while true; do
    cat <<EOF

当前设置
  1. 域名：$(env_value PORTFLOW_SITE_ADDRESS)
  2. 证书邮箱：$(env_value CADDY_EMAIL)
  3. HTTP 端口：$(env_value PORTFLOW_HTTP_BIND)
  4. HTTPS 端口：$(env_value PORTFLOW_HTTPS_BIND)
  5. 运行发布预检
  6. 应用设置并重建容器
  0. 返回
EOF
    choice=$(prompt "请选择" "0")
    case "$choice" in
      1)
        value=$(prompt "新域名" "$(env_value PORTFLOW_SITE_ADDRESS)")
        validate_domain "$value" || { warning "域名格式不正确"; continue; }
        set_env_value PORTFLOW_SITE_ADDRESS "$value"
        ;;
      2)
        value=$(prompt "新邮箱" "$(env_value CADDY_EMAIL)")
        validate_email "$value" || { warning "邮箱格式不正确"; continue; }
        set_env_value CADDY_EMAIL "$value"
        ;;
      3)
        value=$(prompt "新 HTTP 端口" "$(env_value PORTFLOW_HTTP_BIND)")
        validate_port "$value" || { warning "端口无效"; continue; }
        [ "$value" != "$(env_value PORTFLOW_HTTPS_BIND)" ] || { warning "不能与 HTTPS 端口相同"; continue; }
        set_env_value PORTFLOW_HTTP_BIND "$value"
        ;;
      4)
        value=$(prompt "新 HTTPS 端口" "$(env_value PORTFLOW_HTTPS_BIND)")
        validate_port "$value" || { warning "端口无效"; continue; }
        [ "$value" != "$(env_value PORTFLOW_HTTP_BIND)" ] || { warning "不能与 HTTP 端口相同"; continue; }
        set_env_value PORTFLOW_HTTPS_BIND "$value"
        ;;
      5) run_preflight || true ;;
      6)
        run_preflight || { warning "预检失败，设置没有应用"; continue; }
        compose up -d --remove-orphans
        wait_for_control
        success "设置已经应用"
        ;;
      0) return 0 ;;
      *) warning "无效选择" ;;
    esac
  done
}

rollback_control() {
  require_root
  require_runtime
  ensure_installed
  local current releases=() item choice selected old_version selected_tag
  current=$(readlink -f "$CURRENT_LINK")
  while IFS= read -r item; do
    [ "$item" = "$current" ] || releases+=("$item")
  done < <(find "$RELEASES_DIR" -mindepth 1 -maxdepth 1 -type d -print | sort -r)
  [ "${#releases[@]}" -gt 0 ] || die "没有可回滚的旧版本"
  printf '\n可回滚版本：\n'
  for item in "${!releases[@]}"; do printf '  %d. %s\n' "$((item + 1))" "$(basename "${releases[$item]}")"; done
  choice=$(prompt "选择版本编号" "")
  [[ "$choice" =~ ^[0-9]+$ ]] && [ "$choice" -ge 1 ] && [ "$choice" -le "${#releases[@]}" ] || die "选择无效"
  selected="${releases[$((choice - 1))]}"
  confirm "确认回滚到 $(basename "$selected")？将先备份数据库" "N" || return 0
  backup_database >/dev/null
  old_version=$(env_value PORTFLOW_VERSION)
  selected_tag=$(basename "$selected")
  selected_tag="${selected_tag%%-20*}"
  switch_current "$selected"
  set_env_value PORTFLOW_VERSION "${selected_tag#v}"
  if deploy_current; then
    write_manager_config "$(config_value PORTFLOW_REPOSITORY)" "$selected_tag"
    success "已回滚到 $selected_tag"
  else
    error "回滚版本启动失败，恢复原版本"
    switch_current "$current"
    set_env_value PORTFLOW_VERSION "$old_version"
    compose up -d --remove-orphans || true
    die "回滚失败"
  fi
}

uninstall_control() {
  require_root
  require_runtime
  ensure_installed
  local confirmation preserved_backups
  warning "默认卸载只移除容器和程序文件，保留数据库卷、配置与备份"
  confirm "确认卸载 PortFlow？" "N" || return 0
  if confirm "卸载前创建数据库备份？" "Y"; then backup_database >/dev/null; fi
  if [ -s "$FIREWALL_STATE" ] && confirm "同时恢复由 PortFlow 记录的 Agent 防火墙规则？" "N"; then
    restore_firewall_rules
  fi
  if confirm "同时永久删除 PostgreSQL 和 Caddy 数据卷？" "N"; then
    printf '此操作不可恢复。如确定删除全部数据，请输入 DELETE：' >&4
    IFS= read -r confirmation <&3 || confirmation=""
    [ "$confirmation" = "DELETE" ] || die "确认词不匹配，已取消卸载"
    compose down -v --remove-orphans
    if [ -d "$BACKUP_DIR" ] && find "$BACKUP_DIR" -mindepth 1 -print -quit | grep -q .; then
      preserved_backups="${INSTALL_ROOT}-backups-$(date -u +%Y%m%dT%H%M%SZ)"
      mv "$BACKUP_DIR" "$preserved_backups"
      warning "数据库备份没有随数据卷删除，已保留在 $preserved_backups"
    fi
    rm -f "$COMMAND_LINK" "$CURRENT_LINK"
    rm -rf "$INSTALL_ROOT"
    success "PortFlow 程序、配置和数据卷已删除"
  else
    compose down --remove-orphans
    rm -f "$COMMAND_LINK" "$CURRENT_LINK"
    rm -rf "$RELEASES_DIR"
    success "PortFlow 程序已卸载；Docker 数据卷、配置和备份仍然保留在 $INSTALL_ROOT"
  fi
  warning "卸载不会重置防火墙；只会在你另行确认后恢复 PortFlow 记录的规则"
}

show_menu() {
  local choice
  while true; do
    cat <<'EOF'

========================================
       PortFlow 中文部署管理器
========================================
  1. 安装控制面
  2. 更新控制面
  3. 修改设置
  4. 查看状态
  5. 查看日志
  6. 备份数据库
  7. 重启控制面
  8. 回滚版本
  9. 环境检查
 10. 防火墙管理
 11. 卸载
  0. 退出
EOF
    choice=$(prompt "请选择操作" "0")
    case "$choice" in
      1) install_control ;;
      2) update_control ;;
      3) settings_menu ;;
      4) show_status ;;
      5) show_logs ;;
      6) backup_database ;;
      7) restart_control ;;
      8) rollback_control ;;
      9) check_environment || true ;;
      10) firewall_menu ;;
      11) uninstall_control; return 0 ;;
      0) return 0 ;;
      *) warning "无效选择" ;;
    esac
  done
}

main() {
  parse_args "$@"
  case "$ACTION" in
    menu) show_menu ;;
    install) install_control ;;
    update) update_control ;;
    settings|config) settings_menu ;;
    status) show_status ;;
    logs) show_logs ;;
    backup) backup_database ;;
    restart) restart_control ;;
    rollback) rollback_control ;;
    firewall) firewall_menu ;;
    uninstall) uninstall_control ;;
    check) check_environment ;;
    help) usage ;;
    *) usage; die "未知操作：$ACTION" ;;
  esac
}

if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then
  main "$@"
fi
