#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "run this installer as root" >&2
  exit 1
fi

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
BINARY=${1:-"$SCRIPT_DIR/../../bin/portflow-agent"}
UNIT=${2:-"$SCRIPT_DIR/../systemd/portflow-agent.service"}

if [ ! -f "$BINARY" ] || [ ! -x "$BINARY" ]; then
  echo "agent binary is missing or not executable: $BINARY" >&2
  exit 1
fi
if [ ! -f "$UNIT" ]; then
  echo "systemd unit is missing: $UNIT" >&2
  exit 1
fi

"$BINARY" -version

if systemctl is-active --quiet portflow-agent.service 2>/dev/null; then
  echo "portflow-agent is running; stop it in a maintenance window before upgrading" >&2
  exit 1
fi

if ! getent group portflow-agent >/dev/null 2>&1; then
  groupadd --system portflow-agent
fi
if ! id portflow-agent >/dev/null 2>&1; then
  NOLOGIN=$(command -v nologin || printf '%s' /usr/sbin/nologin)
  useradd --system --gid portflow-agent --home-dir /var/lib/portflow-agent --shell "$NOLOGIN" portflow-agent
fi

install -d -m 0700 -o portflow-agent -g portflow-agent /var/lib/portflow-agent
install -m 0755 -o root -g root "$BINARY" /usr/local/bin/portflow-agent
install -m 0644 -o root -g root "$UNIT" /etc/systemd/system/portflow-agent.service
systemctl daemon-reload

echo "PortFlow Agent installed but not started. Enroll the node before enabling the service."
