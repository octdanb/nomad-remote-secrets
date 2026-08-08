# AWS e2e batch job: resolves aws-ssm: references (one plain SecureString,
# one JSON object that auto-expands) into the task environment.
job "e2e-aws-secrets" {
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
        provider = "secrets"
        path     = "aws-ssm:/prod/db/password"
      }

      secret "creds" {
        provider = "secrets"
        path     = "aws-ssm:/prod/db/creds"
      }

      env {
        DB_PASSWORD = "${secret.db.value}"
        CREDS_USER  = "${secret.creds.username}"
        CREDS_PW    = "${secret.creds.password}"
      }

      config {
        image        = "alpine:3.20"
        network_mode = "none"
        args         = ["sh", "-c", "echo \"PW=$${DB_PASSWORD} USER=$${CREDS_USER} CPW=$${CREDS_PW}\""]
      }
    }
  }
}
