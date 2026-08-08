# Example: running smspit with secrets pulled from 1Password at deploy time.
#
# Each `secret` block is resolved by the secrets provider plugin on the
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

      # Several secrets in one block: one "name = reference" per line.
      # Single-field entries are exposed under their name; whole-item
      # entries (like twilio) expose every field prefixed with the name.
      secret "app" {
        provider = "secrets"
        path     = <<-EOF
          dashboard_token = op://Infrastructure/smspit/dashboard_token
          twilio          = op://Infrastructure/twilio-prod
        EOF
      }

      # The one-block-per-secret form works too:
      #
      #   secret "dashboard" {
      #     provider = "secrets"
      #     path     = "op://Infrastructure/smspit/dashboard_token"
      #   }
      #
      # exposing ${secret.dashboard.value}.

      config {
        image = "ghcr.io/octdanb/octavenz-smspit:latest"
        ports = ["http"]
      }

      env {
        SMSPIT_DASHBOARD_TOKEN = "${secret.app.dashboard_token}"
        TWILIO_ACCOUNT_SID     = "${secret.app.twilio_username}"
        TWILIO_AUTH_TOKEN      = "${secret.app.twilio_password}"
      }

      resources {
        cpu    = 200
        memory = 256
      }
    }
  }
}
