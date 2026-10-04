#!/usr/bin/env bash
# Build the macOS installers (.pkg) into $1 (default ../relay/dist):
#   RemoteDesk-Installer.pkg        agent + app
#   RemoteDesk-Agent-Installer.pkg  agent only
# The relay's install.sh is embedded with server + token filled in (from ../.env),
# so the .pkg works offline from the web page and needs no Terminal.
set -euo pipefail
cd "$(dirname "$0")"
OUT="${1:-../relay/dist}"; mkdir -p "$OUT"
source ../.env
SERVER="${PUBLIC_URL_PRIMARY:-https://remote.tuandv.id.vn}"
for variant in full agent; do
  TMP="$(mktemp -d)"
  mkdir -p "$TMP/scripts"
  app=1; name=RemoteDesk-Installer.pkg
  [ "$variant" = agent ] && { app=0; name=RemoteDesk-Agent-Installer.pkg; }
  sed -e "s|{{SERVER}}|$SERVER|g" -e "s|{{TOKEN}}|$REMOTE_TOKEN|g" -e "s|{{WITH_APP}}|$app|g" \
    ../relay/install/install.sh > "$TMP/scripts/rd-install.sh"
  cp postinstall.tmpl "$TMP/scripts/postinstall"
  chmod 755 "$TMP/scripts/"*
  pkgbuild --nopayload --scripts "$TMP/scripts" --identifier "vn.tuandv.remotedesk.installer.$variant" \
    --version 0.1 "$OUT/$name" >/dev/null
  rm -rf "$TMP"
done
ls -la "$OUT"/*.pkg
