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

      # A file-like secret: a 1Password Document item resolved to its file
      # content. The plugin never writes files; the job materializes it into
      # tmpfs secrets/ via a template block.
      secret "doc" {
        provider = "remote-secrets"
        path     = "welcome = op://Testing/welcome"
      }

      template {
        data        = "{{ \"${secret.doc.welcome_value_base64}\" | base64Decode }}"
        destination = "secrets/welcome.txt"
        perms       = "0400"
      }

      env {
        DB_PASSWORD  = "${secret.db.value}"
        APP_PW       = "${secret.app.pw}"
        APP_REPLICA  = "${secret.app.rep}"
        APP_USER     = "${secret.app.db_username}"
        APP_DB_HOST  = "${secret.app.db_host_name}"
      }

      config {
        command = "/bin/sh"
        args    = ["-c", "echo \"PW=$${DB_PASSWORD} APP_PW=$${APP_PW} REP=$${APP_REPLICA} USER=$${APP_USER} HOST=$${APP_DB_HOST} DOC=$(cat $${NOMAD_SECRETS_DIR}/welcome.txt)\""]
      }
    }
  }
}
