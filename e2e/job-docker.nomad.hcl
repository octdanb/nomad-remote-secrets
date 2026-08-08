# E2E batch job (docker variant, used in CI). Same assertions as the
# raw_exec variant: single field, sectioned field, multi-entry whole item.
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
      driver = "docker"

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

      env {
        DB_PASSWORD  = "${secret.db.value}"
        APP_PW       = "${secret.app.pw}"
        APP_REPLICA  = "${secret.app.rep}"
        APP_USER     = "${secret.app.db_username}"
        APP_DB_HOST  = "${secret.app.db_host_name}"
      }

      config {
        image        = "alpine:3.20"
        network_mode = "none"
        args         = ["sh", "-c", "echo \"PW=$${DB_PASSWORD} APP_PW=$${APP_PW} REP=$${APP_REPLICA} USER=$${APP_USER} HOST=$${APP_DB_HOST}\""]
      }
    }
  }
}
