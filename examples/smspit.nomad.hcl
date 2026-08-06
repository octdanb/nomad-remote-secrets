# Example: running smspit with secrets pulled from 1Password at deploy time.
#
# Each `secret` block is resolved by the onepassword provider plugin on the
# Nomad client when the task starts. The fetched key/value pairs are then
# available anywhere in the task via ${secret.<block_name>.<key>} — here they
# are injected as environment variables into the Docker container.

job "smspit" {
  datacenters = ["dc1"]

  group "smspit" {
    network {
      port "http" {
        to = 8025
      }
    }

    task "smspit" {
      driver = "docker"

      # A single field: op://<vault>/<item>/<field>.
      # Exposed as ${secret.dashboard.value} (and ${secret.dashboard.<field>}).
      secret "dashboard" {
        provider = "onepassword"
        path     = "op://Infrastructure/smspit/dashboard_token"
      }

      # A whole item: op://<vault>/<item> — every non-empty field becomes a
      # key, e.g. ${secret.twilio.username} / ${secret.twilio.password}, plus
      # any custom fields by their (sanitized) label.
      secret "twilio" {
        provider = "onepassword"
        path     = "op://Infrastructure/twilio-prod"
      }

      config {
        image = "ghcr.io/octdanb/octavenz-smspit:latest"
        ports = ["http"]
      }

      env {
        SMSPIT_DASHBOARD_TOKEN = "${secret.dashboard.value}"
        TWILIO_ACCOUNT_SID     = "${secret.twilio.username}"
        TWILIO_AUTH_TOKEN      = "${secret.twilio.password}"
      }

      resources {
        cpu    = 200
        memory = 256
      }
    }
  }
}
