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
        provider = "secrets"
        path     = "op://Testing/database/password"
      }

      secret "app" {
        provider = "secrets"
        path     = <<-EOF
          pw  = op://Testing/database/password
          rep = op://Testing/database/replica/password
          db  = op://Testing/database
        EOF
      }

      env {
        DB_PASSWORD = "${secret.db.value}"
        APP_PW      = "${secret.app.pw}"
        APP_REPLICA = "${secret.app.rep}"
        APP_USER    = "${secret.app.db_username}"
        APP_DB_HOST = "${secret.app.db_host_name}"
      }

      config {
        command = "/bin/sh"
        args    = ["-c", "while true; do sleep 3600; done"]
      }
    }
  }
}
