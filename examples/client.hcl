# Nomad client agent configuration for the onepassword secret provider.
#
# common_plugin_dir is where Nomad discovers secret provider plugins: any
# executable in the secrets/ subdirectory is fingerprinted at agent startup
# (and on SIGHUP). Install the plugin binary as:
#
#   /opt/nomad/plugins/secrets/onepassword
#
# Requires Nomad 1.11.0 or newer.

client {
  enabled           = true
  common_plugin_dir = "/opt/nomad/plugins"
}
