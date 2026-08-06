#!/usr/bin/env bash
# The onepassword Nomad secret provider plugin, uploaded by Packer's file
# provisioner. Credentials are NOT baked — user data writes
# /etc/nomad-secret-onepassword/{config.env,token} at first boot.
set -euo pipefail

install -m 0755 /tmp/onepassword /opt/nomad/plugins/secrets/onepassword
install -d -m 0700 /etc/nomad-secret-onepassword

/opt/nomad/plugins/secrets/onepassword version
/opt/nomad/plugins/secrets/onepassword fingerprint
