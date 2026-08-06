#!/usr/bin/env bash
# Make the image safe to clone: no host keys, machine IDs, logs, or
# leftover build files.
set -euo pipefail

apt-get -y autoremove --purge
apt-get -y clean

# Regenerated per instance by cloud-init / systemd.
rm -f /etc/ssh/ssh_host_*
truncate -s 0 /etc/machine-id
rm -f /var/lib/dbus/machine-id
ln -s /etc/machine-id /var/lib/dbus/machine-id

cloud-init clean --logs --seed

rm -rf /var/log/journal/* /var/lib/apt/lists/*
find /var/log -type f -exec truncate -s 0 {} +

rm -f /root/.bash_history /home/ubuntu/.bash_history
sync
