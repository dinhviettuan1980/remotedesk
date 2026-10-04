#!/bin/bash
# RemoteDesk agent installer for macOS (served by the relay with server/token filled in).
# Run in Terminal:   curl -fsSL "{{SERVER}}/install.sh?t=<token>" | bash
set -euo pipefail

SERVER='{{SERVER}}'
TOKEN='{{TOKEN}}'
WITH_APP='{{WITH_APP}}' # 1 = also install the RemoteDesk viewer app
DIR="$HOME/Library/Application Support/RemoteDesk"
LABEL="vn.tuandv.remotedesk"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"

echo "==> Cài RemoteDesk agent vào $DIR"
mkdir -p "$DIR" "$HOME/Library/LaunchAgents"

launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true

# The agent is a signed .app bundle built by scripts/build.sh. It is installed
# as-is (never re-signed here): macOS keeps Screen Recording / Accessibility
# for a new version as long as the bundle id and signing certificate match.
APP="$DIR/RemoteDesk.app"
BIN="$APP/Contents/MacOS/remote-agent"
# Grants carry over only from an agent signed with the same certificate; a first
# install or the old ad-hoc build means asking for them (once).
NEED_GRANT=1
if [ -d "$APP" ] && codesign -dv "$APP" 2>&1 | grep -q '^TeamIdentifier=[A-Z0-9]'; then NEED_GRANT=0; fi
echo "==> Tải agent..."
TMPA="$(mktemp -d)"
curl -fsSL -H "Authorization: Bearer $TOKEN" "$SERVER/dl/RemoteDesk-agent-mac.zip" -o "$TMPA/agent.zip"
NEW_SHA="$(shasum -a 256 "$TMPA/agent.zip" | cut -d' ' -f1)"
if [ -f "$DIR/agent.sha256" ] && [ "$(cat "$DIR/agent.sha256")" = "$NEW_SHA" ] && codesign -v "$APP" 2>/dev/null; then
  echo "   agent không đổi — giữ nguyên"
  AGENT_CHANGED=0
else
  ditto -x -k "$TMPA/agent.zip" "$TMPA"
  rm -rf "$APP"
  ditto "$TMPA/RemoteDesk.app" "$APP"
  echo "$NEW_SHA" > "$DIR/agent.sha256"
  AGENT_CHANGED=1
fi
rm -rf "$TMPA"
rm -f "$DIR/remote-agent" # pre-bundle layout

if [ "$WITH_APP" = 1 ]; then
  echo "==> Cài app RemoteDesk vào Applications (để từ máy này điều khiển máy khác)..."
  APPS=/Applications; [ -w "$APPS" ] || { APPS="$HOME/Applications"; mkdir -p "$APPS"; }
  TMPZ="$(mktemp -d)"
  curl -fsSL -H "Authorization: Bearer $TOKEN" "$SERVER/dl/RemoteDesk-mac.zip" -o "$TMPZ/app.zip"
  ditto -x -k "$TMPZ/app.zip" "$TMPZ"
  pkill -f "$APPS/RemoteDesk.app/Contents/MacOS/RemoteDesk" 2>/dev/null || true
  rm -rf "$APPS/RemoteDesk.app"
  ditto "$TMPZ/RemoteDesk.app" "$APPS/RemoteDesk.app"
  rm -rf "$TMPZ"
fi

FF="$(command -v ffmpeg || true)"
for p in /opt/homebrew/bin/ffmpeg /usr/local/bin/ffmpeg; do
  [ -z "$FF" ] && [ -x "$p" ] && FF="$p"
done
if [ -z "$FF" ]; then
  if command -v brew >/dev/null; then
    echo "==> Cài ffmpeg qua Homebrew..."
    brew install ffmpeg
    FF="$(brew --prefix)/bin/ffmpeg"
  else
    echo "!! Cần ffmpeg. Cài Homebrew (https://brew.sh) rồi chạy lại script này." >&2
    exit 1
  fi
fi

NAME="${RD_NAME:-$(scutil --get ComputerName 2>/dev/null || hostname -s)}"
ID="${RD_ID:-$(scutil --get LocalHostName 2>/dev/null | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9]+/-/g; s/^-|-$//g')}"
if [ -f "$DIR/config.json" ]; then
  OLD_ID="$(/usr/bin/python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("id",""))' "$DIR/config.json" 2>/dev/null || true)"
  [ -n "$OLD_ID" ] && [ -z "${RD_ID:-}" ] && ID="$OLD_ID"
  OLD_NAME="$(/usr/bin/python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("name",""))' "$DIR/config.json" 2>/dev/null || true)"
  [ -n "$OLD_NAME" ] && [ -z "${RD_NAME:-}" ] && NAME="$OLD_NAME"
fi
/usr/bin/python3 - "$DIR/config.json" "$SERVER" "$TOKEN" "$ID" "$NAME" "$FF" <<'PY'
import json, sys
path, server, token, id_, name, ff = sys.argv[1:]
json.dump({"server": server, "token": token, "id": id_, "name": name, "ffmpeg": ff}, open(path, "w"), ensure_ascii=False, indent=2)
PY
chmod 600 "$DIR/config.json"

cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key><array><string>$BIN</string></array>
  <key>AssociatedBundleIdentifiers</key><string>$LABEL</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>5</integer>
  <key>ProcessType</key><string>Interactive</string>
  <key>StandardErrorPath</key><string>$DIR/launchd.err.log</string>
</dict>
</plist>
EOF

launchctl bootstrap "gui/$(id -u)" "$PLIST"
echo "==> Agent đang chạy (launchd: $LABEL)"
echo
if [ "$AGENT_CHANGED" = 1 ] && [ "$NEED_GRANT" = 1 ]; then
  echo "(Nếu RemoteDesk đã có trong danh sách quyền từ bản cũ: xoá nó bằng nút − rồi thêm lại.)"
  echo "QUAN TRỌNG — cấp 2 quyền cho 'RemoteDesk' trong System Settings › Privacy & Security:"
  echo "   1. Screen & System Audio Recording"
  echo "   2. Accessibility"
  echo "Sau khi bật, chạy:  launchctl kickstart -k gui/$(id -u)/$LABEL"
fi
[ "$WITH_APP" = 1 ] && echo "App RemoteDesk: $APPS/RemoteDesk.app (mở từ Launchpad / Spotlight)"
echo
echo "XONG. Máy '$NAME' sẽ hiện trong $SERVER"
echo "Log: $DIR/agent.log"
if [ -z "$(defaults read /Library/Preferences/com.apple.loginwindow autoLoginUser 2>/dev/null)" ]; then
  echo
  echo "Agent tự chạy mỗi khi bạn đăng nhập. Muốn tự chạy cả sau khi khởi động lại (không ai đăng nhập):"
  echo "  System Settings › Users & Groups › Automatically log in as (không dùng được khi bật FileVault)."
fi
