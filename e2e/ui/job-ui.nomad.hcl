# Long-running service job for the UI test. Unlike the batch e2e jobs it does
# NOT echo secret values anywhere — the whole point of the UI test is to prove
# the resolved secret never surfaces in the web UI. The task just sleeps while
# the secret is injected into its (non-exported) environment.
job "ui-secrets" {
  type = "service"

  group "g" {
    task "sleep" {
      driver = "raw_exec"

      secret "db" {
        provider = "remote-secrets"
        path     = "op://Testing/database/password"
      }

      secret "app" {
        provider = "remote-secrets"
        path     = <<-EOF
          pw  = op://Testing/database/password
          rep = op://Testing/database/replica/password
          db  = op://Testing/database
        EOF
      }

      # File-like secrets are injected too — as base64 env vars — so the UI
      # leak test also proves file content (text, binary, JSON) never renders
      # in the Nomad console, in either its raw or base64 form.
      secret "doc" {
        provider = "remote-secrets"
        path     = "op://Testing/welcome"
      }

      secret "cert" {
        provider = "remote-secrets"
        path     = "op://Testing/tls/certificate"
      }

      secret "keystore" {
        provider = "remote-secrets"
        path     = "op://Testing/tls/keystore"
      }

      secret "config" {
        provider = "remote-secrets"
        path     = "op://Testing/appconfig"
      }

      env {
        DB_PASSWORD = "${secret.db.value}"
        APP_PW      = "${secret.app.pw}"
        APP_REPLICA = "${secret.app.rep}"
        APP_USER    = "${secret.app.db_username}"
        APP_DB_HOST = "${secret.app.db_host_name}"
        WELCOME_B64 = "${secret.doc.value_base64}"
        CERT_B64    = "${secret.cert.value_base64}"
        STORE_B64   = "${secret.keystore.value_base64}"
        CONFIG_B64  = "${secret.config.value_base64}"
      }

      config {
        command = "/bin/sh"
        args    = ["-c", "while true; do sleep 3600; done"]
      }
    }
  }
}
