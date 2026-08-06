#!/usr/bin/env bash
# Nomad from the HashiCorp apt repository, pinned to $NOMAD_VERSION.
# The agent is installed but left DISABLED: instance user data writes
# /etc/nomad.d/runtime.hcl (node_pool, datacenter, join config) at first
# boot and then enables the service — so a mis-launched instance without
# configuration never joins a cluster half-configured.
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

curl -fsSL https://apt.releases.hashicorp.com/gpg | gpg --dearmor -o /usr/share/keyrings/hashicorp-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/hashicorp-archive-keyring.gpg] \
https://apt.releases.hashicorp.com $(. /etc/os-release && echo "$VERSION_CODENAME") main" \
  > /etc/apt/sources.list.d/hashicorp.list

apt-get update -y
apt-get install -y "nomad=${NOMAD_VERSION}-1"
apt-mark hold nomad # upgrades happen by replacing the AMI, not apt

install -d -m 0755 /opt/nomad/data
install -d -m 0755 /opt/nomad/plugins/secrets

install -m 0644 /tmp/image-files/nomad-base.hcl /etc/nomad.d/base.hcl
rm -f /etc/nomad.d/nomad.hcl # remove the package's example config

# The packaged unit runs Nomad as the nomad user; clients need root for the
# docker driver and for secret provider plugins.
install -d /etc/systemd/system/nomad.service.d
install -m 0644 /tmp/image-files/nomad-override.conf /etc/systemd/system/nomad.service.d/override.conf

systemctl daemon-reload
systemctl disable nomad

nomad version
