# E2E job for a REAL 1Password vault (service-account backend). Fetches a
# single caller-supplied reference; the runner asserts the expected value.
variable "secret_path" {
  type = string
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
        provider = "secrets"
        path     = var.secret_path
      }

      env {
        DB_PASSWORD = "${secret.db.value}"
      }

      config {
        image        = "alpine:3.20"
        network_mode = "none"
        args         = ["sh", "-c", "echo \"PW=$${DB_PASSWORD}\""]
      }
    }
  }
}
