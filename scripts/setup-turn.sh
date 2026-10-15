#!/usr/bin/env bash
# One-time: install coturn on the VPS as the TURN server for the P2P path.
# Run from the repo on your Mac (asks for the VPS sudo password once):
#
#   ./scripts/setup-turn.sh             (sudo asks for the password)
#   SSHPASS=… ./scripts/setup-turn.sh   (non-interactive)
#
# TURN relays WebRTC media over UDP through the VPS when the two ends cannot
# reach each other directly (e.g. symmetric NAT at the office). Credentials are
# short-lived HMACs the relay hands out (use-auth-secret), so only logged-in
# viewers and their agents can use it. Relaying into private networks is denied.
set -euo pipefail
cd "$(dirname "$0")/.."

HOST="${DEPLOY_HOST:-pc1@103.163.216.32}"
DOMAIN="${DOMAIN:-remote.tuandv.id.vn}"
ENV_FILE="/home/pc1/remote-desk/relay/.env"

REMOTE_SCRIPT=/tmp/rd-setup-turn.sh
scp -q -o ConnectTimeout=15 relay/deploy/setup-turn-remote.sh "$HOST:$REMOTE_SCRIPT"
if [ -n "${SSHPASS:-}" ]; then
  printf '%s\n' "$SSHPASS" | ssh -o ConnectTimeout=15 "$HOST" "sudo -S -p '' env DOMAIN='$DOMAIN' ENV_FILE='$ENV_FILE' bash $REMOTE_SCRIPT; rm -f $REMOTE_SCRIPT"
else
  ssh -t -o ConnectTimeout=15 "$HOST" "sudo env DOMAIN='$DOMAIN' ENV_FILE='$ENV_FILE' bash $REMOTE_SCRIPT; rm -f $REMOTE_SCRIPT"
fi
