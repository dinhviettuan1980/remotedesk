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
echo "▸ installer: windows (Setup.exe base; relay appends config on download)"
(cd ../setup-win && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$OUT/RemoteDesk-Setup-base.exe" .)
echo "▸ installer: macOS (.pkg, embeds token from .env)"
../setup-mac/build.sh "$OUT" >/dev/null
echo "▸ viewer app: macOS"
../viewer-mac/build.sh "$OUT" >/dev/null

ls -la "$OUT"
