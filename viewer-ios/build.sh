#!/usr/bin/env bash
# Build the iOS app and install it on the iPhone/iPad connected by cable.
#
#   ./build.sh                      # production server, Release
#   ./build.sh --dev https://192.168.1.22:8443/   # a test relay on the LAN, Debug (Web Inspector on)
#
# Needs Xcode, xcodegen, and the device paired + Developer Mode on. DEVICE=<id>
# picks one when several are connected (xcrun devicectl list devices).
set -euo pipefail
cd "$(dirname "$0")"

CONFIG=Release
SERVER="https://remote.tuandv.id.vn/"
if [ "${1:-}" = "--dev" ]; then CONFIG=Debug; SERVER="${2:?--dev needs a URL}"; fi

# App icon: iOS wants one opaque 1024×1024 PNG.
ICON=RemoteDesk/Assets.xcassets/AppIcon.appiconset/icon-1024.png
if [ ! -f "$ICON" ]; then
  TMP="$(mktemp -d)"
  sips -s format jpeg -z 1024 1024 ../relay/public/icon-app.svg --out "$TMP/i.jpg" >/dev/null
  sips -s format png "$TMP/i.jpg" --out "$ICON" >/dev/null
fi

xcodegen generate --quiet
xcodebuild -project RemoteDesk.xcodeproj -scheme RemoteDesk -configuration "$CONFIG" \
  -destination 'generic/platform=iOS' -derivedDataPath build -allowProvisioningUpdates \
  RD_SERVER="$SERVER" -quiet build
APP="build/Build/Products/$CONFIG-iphoneos/RemoteDesk.app"

DEVICE="${DEVICE:-$(xcrun devicectl list devices 2>/dev/null | awk '/available \(paired\)/ { for (i = 1; i <= NF; i++) if ($i ~ /^[0-9A-F-]{36}$/) { print $i; exit } }')}"
if [ -z "$DEVICE" ]; then echo "Built $APP — no paired device connected, not installed."; exit 0; fi
echo "▸ Cài lên $DEVICE"
xcrun devicectl device install app --device "$DEVICE" "$APP" >/dev/null
xcrun devicectl device process launch --terminate-existing --device "$DEVICE" vn.tuandv.remotedesk.ios >/dev/null 2>&1 \
  && echo "▸ Đã mở RemoteDesk" || echo "▸ Đã cài. Mở khoá iPhone rồi mở app RemoteDesk."
