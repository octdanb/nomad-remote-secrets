# File-secret e2e (docker service). One running container exercises every
# secret *type* the plugin resolves, so e2e/files/run.sh can `nomad alloc
# exec` in and verify all three surfaces from inside the container:
#
#   * env values   — scalar field, sectioned field, whole-item expansion, and
#                    the file secrets' base64 / utf8 / filename keys
#   * file contents — each file-like secret materialized into secrets/ (exact
#                    bytes, verified by sha256)
#   * file modes    — each written with a distinct, restrictive permission
#
# File-like secrets are delivered as base64 env vars (the documented
# env+decode pattern, since ${secret...} interpolation is not available in
# template data) and decoded into the tmpfs secrets/ dir by the entrypoint.
job "e2e-files" {
  type = "service"

  group "g" {
    restart {
      attempts = 0
      mode     = "fail"
    }

    task "files" {
      driver = "docker"

      # --- scalar / structured (non-file) types ---------------------------

      # A single CONCEALED field.
      secret "db" {
        provider = "remote-secrets"
        path     = "op://Testing/database/password"
      }

      # A multi-entry block: a scalar, a sectioned field, and a whole item
      # (which expands to per-field keys prefixed with the entry name).
      secret "app" {
        provider = "remote-secrets"
        path     = <<-EOF
          pw  = op://Testing/database/password
          rep = op://Testing/database/replica/password
          db  = op://Testing/database
        EOF
      }

      # --- file-like types ------------------------------------------------

      # A whole-item Document → file content (UTF-8 text).
      secret "doc" {
        provider = "remote-secrets"
        path     = "op://Testing/welcome"
      }

      # A UTF-8 FILE-type field (PEM certificate).
      secret "cert" {
        provider = "remote-secrets"
        path     = "op://Testing/tls/certificate"
      }

      # A binary FILE-type field (keystore) — delivered as base64 only.
      secret "keystore" {
        provider = "remote-secrets"
        path     = "op://Testing/tls/keystore"
      }

      # A JSON Document → file content.
      secret "config" {
        provider = "remote-secrets"
        path     = "op://Testing/appconfig"
      }

      env {
        # scalar + structured values
        DB_PASSWORD = "${secret.db.value}"
        APP_PW      = "${secret.app.pw}"
        APP_REPLICA = "${secret.app.rep}"
        APP_USER    = "${secret.app.db_username}"
        APP_HOST    = "${secret.app.db_host_name}"

        # file keys: base64 (decoded into secrets/ by the entrypoint) + the
        # filename the plugin surfaced. The utf8 "value" key is intentionally
        # NOT injected as an env var — a PEM/JSON value contains newlines, and
        # the file content (decoded from base64) is verified directly instead.
        WELCOME_B64  = "${secret.doc.value_base64}"
        WELCOME_NAME = "${secret.doc.filename}"
        CERT_B64     = "${secret.cert.value_base64}"
        CERT_NAME    = "${secret.cert.filename}"
        STORE_B64    = "${secret.keystore.value_base64}"
        STORE_NAME   = "${secret.keystore.filename}"
        CONFIG_B64   = "${secret.config.value_base64}"
        CONFIG_NAME  = "${secret.config.filename}"
      }

      config {
        image        = "alpine:3.20"
        network_mode = "none"
        # Decode each file secret into secrets/ with a distinct, deliberately
        # restrictive mode, then idle so the harness can exec in and inspect.
        #
        # Brace-less shell vars ($VAR, not ${VAR}) on purpose: Nomad only
        # interpolates ${...} in task config, so bare $VAR passes through
        # untouched for the container's shell to expand. Using ${d} here would
        # make Nomad try (and fail) to resolve its own variable named "d".
        args = ["sh", "-c", <<-EOT
          set -e
          d="$NOMAD_SECRETS_DIR"
          printf %s "$WELCOME_B64" | base64 -d > "$d/welcome.txt";  chmod 0400 "$d/welcome.txt"
          printf %s "$CERT_B64"    | base64 -d > "$d/server.pem";   chmod 0444 "$d/server.pem"
          printf %s "$STORE_B64"   | base64 -d > "$d/keystore.p12"; chmod 0400 "$d/keystore.p12"
          printf %s "$CONFIG_B64"  | base64 -d > "$d/config.json";  chmod 0440 "$d/config.json"
          echo "materialized file secrets into $d"
          while true; do sleep 3600; done
        EOT
        ]
      }
    }
  }
}
