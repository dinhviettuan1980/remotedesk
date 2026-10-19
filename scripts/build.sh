#!/usr/bin/env bash
# Build agent binaries into relay/dist (served by the relay at /dl/...).
#   macOS universal (amd64 + arm64, needs Xcode CLT for cgo) and Windows amd64.
set -euo pipefail
cd "$(dirname "$0")/.."
OUT="$PWD/relay/dist"
mkdir -p "$OUT"
cd agent

echo "▸ test"
go test ./...

# The relay shows "có bản mới" next to machines running something older.
sed -n 's/^const version = "\(.*\)"/\1/p' main.go > "$OUT/agent-version.txt"

echo "▸ windows/amd64"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-H=windowsgui -s -w" -o "$OUT/remote-agent-windows-amd64.exe" .

echo "▸ darwin universal"
# Without this clang targets the build machine's macOS (26). 13.0 is what Go
# 1.27 itself needs; ScreenCaptureKit audio (also macOS 13) then always works.
export MACOSX_DEPLOYMENT_TARGET=13.0
# Also as a cgo flag: Go's build cache ignores the variable above.
export CGO_CFLAGS="-O2 -g -mmacosx-version-min=13.0" CGO_LDFLAGS="-O2 -g -mmacosx-version-min=13.0"
TMP="$(mktemp -d)"
GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 CC="clang -arch x86_64" go build -trimpath -ldflags "-s -w" -o "$TMP/amd64" .
GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 CC="clang -arch arm64" go build -trimpath -ldflags "-s -w" -o "$TMP/arm64" .
lipo -create -output "$OUT/remote-agent-darwin" "$TMP/amd64" "$TMP/arm64"
# Sign with a stable identity: macOS ties Screen Recording/Accessibility grants
# to the code's designated requirement. Ad-hoc → requirement = cdhash, so every
# build lost them; with a certificate it is "bundle id + certificate name" and
# grants survive updates. MAC_SIGN_ID: a codesigning identity (SHA-1 or name);
# default = the owner's Apple Development certificate (IMIP team), if present.
MAC_SIGN_ID="${MAC_SIGN_ID:-1D4539CE01A19A7B2E23A4D20C6FA23C454344CF}" # Apple Development: Tuan Dinh (3B8X54H7KQ), team IMIP
security find-identity -v -p codesigning | grep -q "$MAC_SIGN_ID" || { echo "  (signing identity $MAC_SIGN_ID not found: ad-hoc — macOS will ask for permissions again after updates)"; MAC_SIGN_ID=-; }
codesign --force -s "$MAC_SIGN_ID" -i vn.tuandv.remotedesk "$OUT/remote-agent-darwin"
# The agent as a signed .app bundle (what install.sh installs: TCC attaches to
# the bundle id, and the privacy lists show "RemoteDesk").
APPB="$TMP/RemoteDesk.app"
mkdir -p "$APPB/Contents/MacOS"
cp "$OUT/remote-agent-darwin" "$APPB/Contents/MacOS/remote-agent"
VERSION="$(sed -n 's/^const version = "\(.*\)"/\1/p' main.go)"
cat > "$APPB/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleIdentifier</key><string>vn.tuandv.remotedesk</string>
  <key>CFBundleName</key><string>RemoteDesk</string>
  <key>CFBundleDisplayName</key><string>RemoteDesk</string>
  <key>CFBundleExecutable</key><string>remote-agent</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>LSUIElement</key><true/>
  <key>LSMinimumSystemVersion</key><string>13.0</string>
</dict>
</plist>
PLIST
codesign --force -s "$MAC_SIGN_ID" -i vn.tuandv.remotedesk "$APPB"
codesign -d -r- "$APPB" 2>&1 | sed -n 's/^designated => /  requirement: /p'
rm -f "$OUT/RemoteDesk-agent-mac.zip"
(cd "$TMP" && ditto -c -k --keepParent RemoteDesk.app "$OUT/RemoteDesk-agent-mac.zip")
rm -rf "$TMP"

echo "▸ viewer app: windows"
(cd ../viewer-win && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-H=windowsgui -s -w" -o "$OUT/RemoteDesk.exe" .)
echo "▸ installer: windows (NSIS wizard, agent + ffmpeg bundled; relay appends config on download)"
VERSION="$(sed -n 's/^const version = "\(.*\)"/\1/p' main.go)"
# makensis: Homebrew's, or the one scripts/setup-nsis.sh builds into build/nsis.
MAKENSIS="$(command -v makensis || true)"
if [ -x ../build/nsis/nsis-3.13/makensis ]; then
  MAKENSIS=../build/nsis/nsis-3.13/makensis
  export NSISDIR="$(cd ../build/nsis/nsis-3.13 && pwd)"   # its Stubs/Include/Plugins
fi
if [ -n "$MAKENSIS" ]; then
  FFZIP=../build/win/ffmpeg.zip   # cached: ~115 MB download, only once
  if [ ! -f "$FFZIP" ]; then
    mkdir -p ../build/win
    curl -fsSL -o "$FFZIP" https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip
  fi
  PAY="$(mktemp -d)"
  cp "$OUT/remote-agent-windows-amd64.exe" "$PAY/remote-agent.exe"
  cp "$OUT/RemoteDesk.exe" ../setup-win/nsis/register.ps1 "$PAY/"
  unzip -p "$FFZIP" '*/bin/ffmpeg.exe' > "$PAY/ffmpeg.exe"
  # makensis needs a UTF-8 locale (bad_alloc otherwise)
  LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8 "$MAKENSIS" -V2 -DVERSION="$VERSION" -DOUTFILE="$OUT/RemoteDesk-Setup-base.exe" -DPAYLOAD="$PAY" ../setup-win/nsis/installer.nsi
  rm -rf "$PAY"
else
  # Fallback: the console Setup.exe that runs install.ps1 (downloads ffmpeg itself).
  echo "  (makensis not found — run scripts/setup-nsis.sh; building the console Setup.exe instead)"
  (cd ../setup-win && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$OUT/RemoteDesk-Setup-base.exe" .)
fi
echo "▸ installer: macOS (.pkg, token-free: read from the download link)"
../setup-mac/build.sh "$OUT" >/dev/null
echo "▸ viewer app: macOS"
../viewer-mac/build.sh "$OUT" >/dev/null

ls -la "$OUT"
