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

echo "▸ windows/amd64"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-H=windowsgui -s -w" -o "$OUT/remote-agent-windows-amd64.exe" .

echo "▸ darwin universal"
TMP="$(mktemp -d)"
GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 CC="clang -arch x86_64" go build -trimpath -ldflags "-s -w" -o "$TMP/amd64" .
GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 CC="clang -arch arm64" go build -trimpath -ldflags "-s -w" -o "$TMP/arm64" .
lipo -create -output "$OUT/remote-agent-darwin" "$TMP/amd64" "$TMP/arm64"
codesign --force -s - -i vn.tuandv.remotedesk "$OUT/remote-agent-darwin"
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
