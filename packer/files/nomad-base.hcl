# Baked, environment-independent Nomad client configuration.
# Everything environment-specific (datacenter, node_pool, server join,
# node meta) is written to /etc/nomad.d/runtime.hcl by instance user data
# at first boot — see examples/user-data.sh.tftpl.

data_dir  = "/opt/nomad/data"
bind_addr = "0.0.0.0"

client {
  enabled = true

  # Secret provider plugins (onepassword) live in <dir>/secrets/.
  common_plugin_dir = "/opt/nomad/plugins"
}

plugin "docker" {
  config {
    allow_privileged = false
  }
}

telemetry {
  prometheus_metrics = true
  publish_allocation_metrics = true
  publish_node_metrics = true
}
