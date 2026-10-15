#!/usr/bin/env bash
# Runs on the VPS as root (scripts/setup-turn.sh): coturn as the TURN server
# for the P2P path. Needs DOMAIN and ENV_FILE (the relay's .env).
set -euo pipefail
echo "▸ Cài coturn"
DEBIAN_FRONTEND=noninteractive apt-get install -y -q coturn >/dev/null

# Shared secret: reuse the relay's if it already has one.
SECRET="$(grep -E '^TURN_SECRET=' "$ENV_FILE" 2>/dev/null | cut -d= -f2- || true)"
if [ -z "$SECRET" ]; then
  SECRET="$(openssl rand -hex 32)"
  echo "TURN_SECRET=$SECRET" >> "$ENV_FILE"
fi
grep -q '^TURN_HOST=' "$ENV_FILE" || echo "TURN_HOST=$DOMAIN" >> "$ENV_FILE"
chown pc1:pc1 "$ENV_FILE"; chmod 600 "$ENV_FILE"

echo "▸ Chứng chỉ TLS cho cổng 5349 (copy từ Let's Encrypt, tự cập nhật khi gia hạn)"
HOOK=/etc/letsencrypt/renewal-hooks/deploy/coturn.sh
cat > "$HOOK" <<'EOF'
#!/bin/sh
install -o turnserver -g turnserver -m 0600 /etc/letsencrypt/live/remote-desk/fullchain.pem /etc/coturn/cert.pem
install -o turnserver -g turnserver -m 0600 /etc/letsencrypt/live/remote-desk/privkey.pem /etc/coturn/key.pem
systemctl try-restart coturn
EOF
chmod 755 "$HOOK"
mkdir -p /etc/coturn
"$HOOK"

echo "▸ /etc/turnserver.conf"
IP="$(ip -4 route get 1.1.1.1 | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')"
cat > /etc/turnserver.conf <<EOF
# RemoteDesk TURN (scripts/setup-turn.sh)
listening-port=3478
tls-listening-port=5349
listening-ip=$IP
external-ip=$IP
relay-ip=$IP
min-port=49160
max-port=49400
realm=$DOMAIN
use-auth-secret
static-auth-secret=$SECRET
fingerprint
cert=/etc/coturn/cert.pem
pkey=/etc/coturn/key.pem
no-cli
no-multicast-peers
no-loopback-peers
no-tlsv1
no-tlsv1_1
user-quota=12
total-quota=120
max-bps=3000000
stale-nonce=600
# Never relay into private / internal networks (incl. Tailscale).
denied-peer-ip=0.0.0.0-0.255.255.255
denied-peer-ip=10.0.0.0-10.255.255.255
denied-peer-ip=100.64.0.0-100.127.255.255
denied-peer-ip=127.0.0.0-127.255.255.255
denied-peer-ip=169.254.0.0-169.254.255.255
denied-peer-ip=172.16.0.0-172.31.255.255
denied-peer-ip=192.168.0.0-192.168.255.255
denied-peer-ip=::1
denied-peer-ip=fc00::-fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff
denied-peer-ip=fe80::-febf:ffff:ffff:ffff:ffff:ffff:ffff:ffff
syslog
EOF
chmod 640 /etc/turnserver.conf; chown root:turnserver /etc/turnserver.conf

systemctl enable coturn >/dev/null 2>&1
systemctl restart coturn
sleep 1
systemctl is-active coturn
ss -lnptu | grep -E ':(3478|5349) ' | awk '{print $1, $5}' | sort -u || true

echo "▸ Khởi động lại relay để đọc TURN_SECRET"
systemctl restart remote-desk
echo "✓ TURN sẵn sàng: turn:$DOMAIN:3478 (UDP/TCP), turns:$DOMAIN:5349"
echo "  Nếu nhà cung cấp VPS có tường lửa riêng: mở UDP+TCP 3478, TCP 5349, UDP 49160-49400."
