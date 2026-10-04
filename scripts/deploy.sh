#!/usr/bin/env bash
# Build agents and deploy the relay to the VPS.
#
#   ./scripts/deploy.sh            # code + binaries + restart
#   ./scripts/deploy.sh --setup    # first time: also nginx site + TLS + token
#
# Needs SSH key access to $HOST. sudo steps read the password from $SSHPASS if set,
# otherwise sudo prompts in the terminal.
set -euo pipefail
cd "$(dirname "$0")/.."

HOST="${DEPLOY_HOST:-pc1@103.163.216.32}"
DIR="/home/pc1/remote-desk/relay"
# First domain is the default; certbot only gets the ones whose DNS already points here.
DOMAINS="${DOMAINS:-remote.tuandv.id.vn}"
DOMAIN="${DOMAINS%% *}"
SSH_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=15)

remote() { ssh "${SSH_OPTS[@]}" "$HOST" "$@"; }
remote_sudo() {
  if [ -n "${SSHPASS:-}" ]; then
    printf '%s\n' "$SSHPASS" | ssh "${SSH_OPTS[@]}" "$HOST" "sudo -S -p '' $*"
  else
    ssh "${SSH_OPTS[@]}" -t "$HOST" "sudo $*"
  fi
}

./scripts/build.sh

echo "▸ Đồng bộ relay tới $HOST:$DIR"
remote "mkdir -p '$DIR'"
rsync -az --delete --exclude node_modules/ --exclude .env -e "ssh ${SSH_OPTS[*]}" relay/ "$HOST:$DIR/"
remote "cd '$DIR' && npm ci --omit=dev --no-audit --no-fund --loglevel=error"

if [ "${1:-}" = "--setup" ]; then
  echo "▸ Token + .env (giữ nguyên nếu đã có)"
  URLS="$(for d in $DOMAINS; do printf 'https://%s,' "$d"; done)"; URLS="${URLS%,}"
  remote "test -f '$DIR/.env' || { umask 077; printf 'REMOTE_TOKEN=%s\nPORT=8790\n' \"\$(openssl rand -base64 33 | tr '+/' '-_' | tr -d '=')\" > '$DIR/.env'; }"
  remote "sed -i '/^PUBLIC_URL=/d' '$DIR/.env' && echo 'PUBLIC_URL=$URLS' >> '$DIR/.env'"
  echo "▸ nginx + TLS"
  remote_sudo "install -m 0644 '$DIR/deploy/nginx.conf' /etc/nginx/sites-available/$DOMAIN"
  remote_sudo "ln -sf /etc/nginx/sites-available/$DOMAIN /etc/nginx/sites-enabled/$DOMAIN"
  remote_sudo "nginx -t"
  remote_sudo "systemctl reload nginx"
  CERT_ARGS=""
  VPS_IP="$(dig +short "${HOST#*@}" | tail -1)"; VPS_IP="${VPS_IP:-${HOST#*@}}"
  for d in $DOMAINS; do
    if [ "$(dig +short "$d" | tail -1)" = "$VPS_IP" ]; then CERT_ARGS="$CERT_ARGS -d $d"; else echo "  (bỏ qua $d: DNS chưa trỏ về $VPS_IP)"; fi
  done
  remote_sudo "certbot --nginx $CERT_ARGS --cert-name remote-desk --expand --redirect --non-interactive --agree-tos --keep-until-expiring"
fi

echo "▸ systemd"
remote_sudo "install -m 0644 '$DIR/deploy/remote-desk.service' /etc/systemd/system/remote-desk.service"
remote_sudo "systemctl daemon-reload"
remote_sudo "systemctl enable remote-desk >/dev/null 2>&1 || true"
remote_sudo "systemctl restart remote-desk"

for i in $(seq 1 15); do
  if curl -fsS "https://$DOMAIN/healthz" >/dev/null 2>&1; then
    echo "✓ https://$DOMAIN đang chạy"
    exit 0
  fi
  sleep 1
done
echo "✗ relay chưa phản hồi — xem: ssh $HOST journalctl -u remote-desk -n 50" >&2
exit 1
