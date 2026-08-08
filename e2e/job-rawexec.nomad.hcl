# E2E batch job (raw_exec variant, for environments without Docker).
# Exercises a single-field secret, a sectioned field, and a multi-entry
# secret with a whole-item reference, all injected as task env vars.
variable "secret_path" {
  type    = string
  default = "op://Testing/database/password"
}

job "e2e-secrets" {
  type = "batch"

  group "g" {
    reschedule {
      attempts  = 0
      unlimited = false
    }
    restart {
      attempts = 0
      mode     = "fail"
    }

    task "print" {
      driver = "raw_exec"

      secret "db" {
        provider = "remote-secrets"
        path     = var.secret_path
      }

      secret "app" {
        provider = "remote-secrets"
        path     = <<-EOF
          pw      = op://Testing/database/password
          rep     = op://Testing/database/replica/password
          db      = op://Testing/database
        EOF
      }

      # A file-like secret: a 1Password Document item. Its base64 is exposed as
      # an env var and the task decodes it into tmpfs secrets/ — env
      # interpolation of ${secret...} works; template.data interpolation does not.
      secret "doc" {
        provider = "remote-secrets"
        path     = "welcome = op://Testing/welcome"
      }

      env {
        DB_PASSWORD  = "${secret.db.value}"
        APP_PW       = "${secret.app.pw}"
        APP_REPLICA  = "${secret.app.rep}"
        APP_USER     = "${secret.app.db_username}"
        APP_DB_HOST  = "${secret.app.db_host_name}"
        DOC_B64      = "${secret.doc.welcome_value_base64}"
      }

      config {
        command = "/bin/sh"
        args    = ["-c", "echo \"$${DOC_B64}\" | base64 -d > $${NOMAD_SECRETS_DIR}/welcome.txt; echo \"PW=$${DB_PASSWORD} APP_PW=$${APP_PW} REP=$${APP_REPLICA} USER=$${APP_USER} HOST=$${APP_DB_HOST} DOC=$(cat $${NOMAD_SECRETS_DIR}/welcome.txt)\""]
      }
    }
  }
}
