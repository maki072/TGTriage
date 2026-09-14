#!/usr/bin/env bash
# Installs or upgrades tg-triage on Debian. Run as root from the project directory:
#   sudo ./deploy/install.sh bin/tg-triage-linux-amd64
set -euo pipefail

BIN_SRC="${1:-bin/tg-triage-linux-amd64}"
SERVICE=tg-triage
USER_NAME=tgtriage
ENV_DIR=/etc/tg-triage
ENV_FILE="$ENV_DIR/$SERVICE.env"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ $EUID -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi
if [[ ! -f "$BIN_SRC" ]]; then
  echo "binary not found: $BIN_SRC (build it with: make build-linux)" >&2
  exit 1
fi

if ! id "$USER_NAME" >/dev/null 2>&1; then
  useradd --system --no-create-home --home-dir /var/lib/$SERVICE --shell /usr/sbin/nologin "$USER_NAME"
  echo "created system user $USER_NAME"
fi

install -m 0755 "$BIN_SRC" /usr/local/bin/$SERVICE

install -d -m 0750 -o root -g "$USER_NAME" "$ENV_DIR"
if [[ ! -f "$ENV_FILE" ]]; then
  install -m 0640 -o root -g "$USER_NAME" "$SCRIPT_DIR/../.env.example" "$ENV_FILE"
  echo "created $ENV_FILE — fill in tokens and keys, then: systemctl restart $SERVICE"
fi

install -m 0644 "$SCRIPT_DIR/systemd/$SERVICE.service" /etc/systemd/system/$SERVICE.service
systemctl daemon-reload
systemctl enable $SERVICE >/dev/null

if grep -qE '^TELEGRAM_BOT_TOKEN=123456789:AAExample' "$ENV_FILE"; then
  echo "configure $ENV_FILE first; service is enabled but not started"
  exit 0
fi

systemctl restart $SERVICE
sleep 2
systemctl --no-pager --lines=20 status $SERVICE || true
