# Nomad dev-agent overrides for the e2e run: point the client at the
# plugin directory and allow raw_exec (used when Docker is unavailable).
client {
  common_plugin_dir = "/opt/nomad-e2e/plugins"
}

plugin "raw_exec" {
  config {
    enabled = true
  }
}
