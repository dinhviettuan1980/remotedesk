#!/bin/bash
# RemoteDesk agent installer for Raspberry Pi / headless Linux (served by the
# relay with the server, and the access code when the link has one, filled in).
#   curl -fsSL "{{SERVER}}/install-pi.sh?t=<code>" | sudo bash
#   curl -fsSL "{{SERVER}}/install-pi.sh" | sudo bash      # asks for the code
# Re-running updates the agent and keeps the machine's id and settings.
set -euo pipefail

SERVER='{{SERVER}}'
TOKEN='{{TOKEN}}'
BIN=/usr/local/bin/remotedesk-pi
CONF_DIR=/etc/remotedesk-pi
CONF="$CONF_DIR/config.json"
UNIT=/etc/systemd/system/remotedesk-pi.service

[ "$(id -u)" = 0 ] || { echo "Cần chạy bằng root: ... | sudo bash" >&2; exit 1; }

case "$(uname -m)" in
  aarch64|arm64) ARCH=arm64 ;;
  armv6l|armv7l) ARCH=armv6 ;;
  x86_64|amd64) ARCH=amd64 ;;
  *) echo "Chưa hỗ trợ kiến trúc $(uname -m)" >&2; exit 1 ;;
esac

# Reinstall without a code in the link: keep the one already configured.
if [ -z "$TOKEN" ] && [ -f "$CONF" ]; then
  TOKEN="$(sed -n 's/.*"token": *"\([^"]*\)".*/\1/p' "$CONF")"
fi
while [ -z "$TOKEN" ]; do
  read -r -p "Mã truy cập RemoteDesk: " TOKEN </dev/tty
  TOKEN="$(echo "$TOKEN" | tr -d '[:space:]')"
  [ -z "$TOKEN" ] && continue
  code="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$SERVER/api/token-check" || true)"
  if [ "$code" != 200 ]; then echo "Mã không đúng (HTTP $code), nhập lại." >&2; TOKEN=""; fi
done

echo "==> Tải agent ($ARCH)…"
TMP="$(mktemp)"
curl -fsSL -H "Authorization: Bearer $TOKEN" "$SERVER/dl/pi-agent-linux-$ARCH" -o "$TMP"
chmod 755 "$TMP"
VER="$("$TMP" -version)"
install -m 755 "$TMP" "$BIN"
rm -f "$TMP"

mkdir -p "$CONF_DIR"
chmod 700 "$CONF_DIR"
# The id is fixed at first install (hostname + machine id): renaming the Pi
# later must not turn it into a new machine on the list.
ID=""
NAME="$(hostname)"
if [ -f "$CONF" ]; then
  ID="$(sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' "$CONF")"
  OLD_NAME="$(sed -n 's/.*"name": *"\([^"]*\)".*/\1/p' "$CONF")"
  [ -n "$OLD_NAME" ] && NAME="$OLD_NAME"
fi
if [ -z "$ID" ]; then
  ID="$(hostname | tr 'A-Z' 'a-z' | tr -c 'a-z0-9._\n-' '-')-$(head -c 6 /etc/machine-id)"
fi
# Keep optional keys (shellUser, watch) from an existing config.
EXTRA=""
if [ -f "$CONF" ] && command -v python3 >/dev/null; then
  EXTRA="$(python3 -c 'import json,sys; c=json.load(open(sys.argv[1])); x={k:c[k] for k in ("shellUser","watch") if k in c}; print(json.dumps(x)[1:-1])' "$CONF" 2>/dev/null || true)"
fi
umask 077
{
  printf '{\n  "server": "%s",\n  "token": "%s",\n  "id": "%s",\n  "name": "%s"' "$SERVER" "$TOKEN" "$ID" "$NAME"
  [ -n "$EXTRA" ] && printf ',\n  %s' "$EXTRA"
  printf '\n}\n'
} > "$CONF"

cat > "$UNIT" <<EOF
[Unit]
Description=RemoteDesk agent (quản lý Pi từ xa)
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=$BIN -config $CONF
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable remotedesk-pi >/dev/null 2>&1
systemctl restart remotedesk-pi

echo "==> Đã cài RemoteDesk agent $VER: \"$NAME\" (id $ID)"
echo "    Pi sẽ hiện trong tab Pi trên $SERVER sau vài giây."
echo "    Log: journalctl -u remotedesk-pi -f"
