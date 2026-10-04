#!/usr/bin/env bash
# Build RemoteDesk.app (universal, ad-hoc signed) zipped into $1 (default ../relay/dist).
set -euo pipefail
cd "$(dirname "$0")"
OUT="${1:-../relay/dist}"; mkdir -p "$OUT"
TMP="$(mktemp -d)"
APP="$TMP/RemoteDesk.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
for arch in x86_64 arm64; do
  swiftc -O -target "$arch-apple-macos13.3" -o "$TMP/rd-$arch" main.swift
done
lipo -create -output "$APP/Contents/MacOS/RemoteDesk" "$TMP/rd-x86_64" "$TMP/rd-arm64"
# App icon from the web icon.
ICONSET="$TMP/AppIcon.iconset"; mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
  sips -s format png -z $s $s ../relay/public/icon-app.svg --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
  d=$((s*2)); sips -s format png -z $d $d ../relay/public/icon-app.svg --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/AppIcon.icns"
cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleIdentifier</key><string>vn.tuandv.remotedesk.viewer</string>
  <key>CFBundleName</key><string>RemoteDesk</string>
  <key>CFBundleDisplayName</key><string>RemoteDesk</string>
  <key>CFBundleExecutable</key><string>RemoteDesk</string>
  <key>CFBundleIconFile</key><string>AppIcon</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>0.1</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>LSMinimumSystemVersion</key><string>13.3</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST
codesign --force --deep -s - "$APP"
# zip, not dmg: hdiutil hangs in some sandboxed shells; ditto keeps the bundle intact.
rm -f "$OUT/RemoteDesk-mac.zip"
ditto -c -k --keepParent "$APP" "$OUT/RemoteDesk-mac.zip"
rm -rf "$TMP"
ls -la "$OUT/RemoteDesk-mac.zip"
