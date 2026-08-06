#!/usr/bin/env bash
# Base packages and hardening common to every node.
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

# cloud-init may still hold the apt lock right after boot.
until apt-get update -y; do sleep 5; done

apt-get install -y \
  ca-certificates \
  curl \
  gnupg \
  unzip \
  jq \
  chrony \
  unattended-upgrades

systemctl enable chrony
