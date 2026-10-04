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

# Wrap the agent in a real .app bundle: macOS privacy permissions (TCC) attach
# reliably to a bundle id, and the list shows "RemoteDesk" instead of a bare binary.
APP="$DIR/RemoteDesk.app"
BIN="$APP/Contents/MacOS/remote-agent"
echo "==> Tải agent..."
mkdir -p "$APP/Contents/MacOS"
curl -fsSL -H "Authorization: Bearer $TOKEN" "$SERVER/dl/remote-agent-darwin" -o "$DIR/agent.download"
chmod +x "$DIR/agent.download"
# Re-signing changes nothing for an identical binary, but a new binary makes
# macOS ask for Screen Recording/Accessibility again — so only touch it on change.
# (codesign rewrites the binary in place, so compare against the hash of what we downloaded last time)
NEW_SHA="$(shasum -a 256 "$DIR/agent.download" | cut -d' ' -f1)"
if [ -f "$DIR/agent.sha256" ] && [ "$(cat "$DIR/agent.sha256")" = "$NEW_SHA" ] && codesign -v "$APP" 2>/dev/null; then
  rm -f "$DIR/agent.download"
  echo "   agent không đổi — giữ nguyên (không phải cấp quyền lại)"
  AGENT_CHANGED=0
else
  mv -f "$DIR/agent.download" "$BIN"
  echo "$NEW_SHA" > "$DIR/agent.sha256"
  AGENT_CHANGED=1
fi
[ "$AGENT_CHANGED" = 1 ] && cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleIdentifier</key><string>$LABEL</string>
  <key>CFBundleName</key><string>RemoteDesk</string>
  <key>CFBundleDisplayName</key><string>RemoteDesk</string>
  <key>CFBundleExecutable</key><string>remote-agent</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>0.1</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>LSUIElement</key><true/>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
</dict>
</plist>
PLIST
[ "$AGENT_CHANGED" = 1 ] && codesign --force -s - -i "$LABEL" "$APP"
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
if [ "$AGENT_CHANGED" = 1 ]; then
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
