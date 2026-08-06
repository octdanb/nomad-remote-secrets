#!/usr/bin/env bash
# Traefik, installed but DISABLED. Instances that should run an edge proxy
# enable it from user data after writing /etc/traefik/traefik.env.
set -euo pipefail

case "$ARCH" in
  amd64) T_ARCH=amd64 ;;
  arm64) T_ARCH=arm64 ;;
  *) echo "unsupported arch $ARCH" >&2; exit 1 ;;
esac

curl -fsSL -o /tmp/traefik.tar.gz \
  "https://github.com/traefik/traefik/releases/download/v${TRAEFIK_VERSION}/traefik_v${TRAEFIK_VERSION}_linux_${T_ARCH}.tar.gz"
tar -xzf /tmp/traefik.tar.gz -C /tmp traefik
install -m 0755 /tmp/traefik /usr/local/bin/traefik
rm -f /tmp/traefik /tmp/traefik.tar.gz

install -d -m 0755 /etc/traefik
install -m 0644 /tmp/image-files/traefik.yml /etc/traefik/traefik.yml
install -m 0644 /tmp/image-files/traefik.service /etc/systemd/system/traefik.service

systemctl daemon-reload
systemctl disable traefik || true

/usr/local/bin/traefik version
