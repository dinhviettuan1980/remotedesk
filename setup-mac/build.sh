#!/usr/bin/env bash
# Build the macOS installers (.pkg) into $1 (default ../relay/dist):
#   RemoteDesk-Installer.pkg        agent + app
#   RemoteDesk-Agent-Installer.pkg  agent only
# The .pkg carries no token (one file for every user): its postinstall finds
# the token in the download link macOS records, then fetches install.sh.
set -euo pipefail
cd "$(dirname "$0")"
OUT="${1:-../relay/dist}"; mkdir -p "$OUT"
SERVER="${PUBLIC_URL_PRIMARY:-https://remote.tuandv.id.vn}"
for variant in full agent; do
  TMP="$(mktemp -d)"
  mkdir -p "$TMP/scripts"
  app=1; name=RemoteDesk-Installer.pkg
  [ "$variant" = agent ] && { app=0; name=RemoteDesk-Agent-Installer.pkg; }
  sed -e "s|{{SERVER}}|$SERVER|g" -e "s|{{WITH_APP}}|$app|g" postinstall.tmpl > "$TMP/scripts/postinstall"
  chmod 755 "$TMP/scripts/"*
  pkgbuild --nopayload --scripts "$TMP/scripts" --identifier "vn.tuandv.remotedesk.installer.$variant" \
    --version 0.2 "$OUT/$name" >/dev/null
  rm -rf "$TMP"
done
ls -la "$OUT"/*.pkg
