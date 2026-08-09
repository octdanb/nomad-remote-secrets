# nomad-remote-secrets

[![CI](https://github.com/octdanb/nomad-remote-secrets/actions/workflows/ci.yml/badge.svg)](https://github.com/octdanb/nomad-remote-secrets/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/octdanb/nomad-remote-secrets?sort=semver)](https://github.com/octdanb/nomad-remote-secrets/releases)
[![Go](https://img.shields.io/github/go-mod/go-version/octdanb/nomad-remote-secrets)](go.mod)
![Nomad 1.11+](https://img.shields.io/badge/nomad-1.11%2B-00CA8E?logo=nomad)

A [Nomad secret provider plugin](https://developer.hashicorp.com/nomad/plugins/author/secret-provider)
that resolves secret references from various backend secret providers, so job specs can pull
secrets at deploy time.

## Supported providers
| Provider | Scheme | Example |
|---|---|---|
| [1Password](https://developer.1password.com/) | `op://` | `op://Production/database/password` |
| [AWS Parameter Store](https://docs.aws.amazon.com/systems-manager/latest/userguide/systems-manager-parameter-store.html) | `aws-ssm:` | `aws-ssm:/prod/db/password` or a parameter ARN |
| [AWS Secrets Manager](https://docs.aws.amazon.com/secretsmanager/) | `aws-sm:` | `aws-sm:prod/db/creds` or a secret ARN |


## Usage examples

These blocks live inside a Nomad `task` (a `secret` block may also sit at the
group or job level). At deploy time the client fetches each reference from its
backend, caches it locally, and interpolates the result into the task — secrets
never appear in the job spec, in Nomad server state, or in plugin logs.

### Single field

Fetch one field and expose it as an environment variable:

```hcl
task "app" {
  driver = "docker"

  secret "db" {
    provider = "remote-secrets"
    path     = "op://Production/database/password"
  }

  env {
    DB_PASSWORD = "${secret.db.value}"
  }
}
```

### Whole item (field expansion)

Reference an item with no field segment and every field expands into its own
interpolation key. (An AWS secret whose value is a JSON object expands the same
way.)

```hcl
secret "db" {
  provider = "remote-secrets"
  path     = "op://Production/database"   # whole item — no field
}

env {
  DB_USER     = "${secret.db.username}"
  DB_PASSWORD = "${secret.db.password}"
  DB_HOST     = "${secret.db.host_name}"  # label "host name" → host_name
}
```

### Multiple secrets in one block

Put one `name = <reference>` per line — entries may even mix backends. A
single-field entry is exposed under its name; a whole-item entry is prefixed
with its name (`twilio` → `twilio_username`, `twilio_password`, …):

```hcl
secret "app" {
  provider = "remote-secrets"
  path     = <<-EOF
    # one line per secret: <name> = <reference>
    db_password = op://Production/database/password
    api_key     = op://Production/api/credential
    twilio      = op://Production/twilio-prod
  EOF
}

env {
  DB_PASSWORD        = "${secret.app.db_password}"
  API_KEY            = "${secret.app.api_key}"
  TWILIO_ACCOUNT_SID = "${secret.app.twilio_username}"
  TWILIO_AUTH_TOKEN  = "${secret.app.twilio_password}"
}
```

The fetch **fails closed**: if any one reference can't be resolved, the whole
block errors and the task never starts with a partial secret set.

### Secret into a rendered config file (template)

`${secret...}` interpolates into `env {}` but **not** into a `template` block's
`data`. To render a secret into a config file, expose it as an env var and read
it back in the template with `{{ env }}`:

```hcl
secret "db" {
  provider = "remote-secrets"
  path     = "op://Production/database/password"
}

env {
  DB_PASSWORD = "${secret.db.value}"
}

template {
  destination = "secrets/app.conf"   # tmpfs, never shown in the Nomad UI
  data        = <<-EOT
    [database]
    password = {{ env "DB_PASSWORD" }}
  EOT
}
```

### File secret to a file on disk

A file-like secret — a 1Password document / file field, or a Secrets Manager
binary secret — is returned as interpolation keys; the plugin never writes the
file itself. Because `${secret...}` can't be referenced inside a `template`
block's `data` (only in `env {}`), you bridge through an env var. Whether you
need base64 depends on the content:

**Text (UTF-8) file** — e.g. a PEM bundle or config. Use the plain `value` key;
a `template` renders it and sets permissions declaratively with `perms`:

```hcl
secret "cert" {
  provider = "remote-secrets"
  path     = "op://Production/tls/bundle"   # a text document
}

env {
  BUNDLE = "${secret.cert.value}"
}

template {
  destination = "secrets/bundle.pem"        # tmpfs, never shown in the UI
  perms       = "0400"                       # owner read-only
  data        = "{{ env \"BUNDLE\" }}"
}
```

**Binary file** — e.g. a `.zip`, PKCS#12 keystore, or image. There is no UTF-8
`value`, so use `value_base64` and decode it. Decoding in the entrypoint keeps
the bytes exact and lets you `chmod`:

```hcl
secret "ks" {
  provider = "remote-secrets"
  path     = "op://Production/tls/keystore"  # a FILE-type field (binary)
}

env {
  KEYSTORE_B64 = "${secret.ks.value_base64}"
}

config {
  image = "app:latest"
  # entrypoint decodes and sets permissions:
  #   echo "$KEYSTORE_B64" | base64 -d > "$NOMAD_SECRETS_DIR/keystore.p12"
  #   chmod 0400 "$NOMAD_SECRETS_DIR/keystore.p12"
}
```

> A `template` can also decode with `{{ env "KEYSTORE_B64" | base64Decode }}`
> and set `perms`, but a heredoc's trailing newline can corrupt exact-byte
> binaries — prefer the entrypoint for binary files, the template for text.

See the [user guide](docs/user-guide.md) for the full reference syntax, AWS
setup, caching, and troubleshooting.

## Features

- **Multi-backend, one binary** — 1Password (service account or self-hosted
  Connect), AWS Parameter Store, and AWS Secrets Manager, selected per-reference
  by scheme. A single `secret` block can mix them.
- **No Vault required** — resolve secrets at deploy time with the tooling you
  already have.
- **Whole items & JSON** — fetch one field, a whole 1Password item, or a JSON
  secret; object values auto-expand into individual interpolation keys.
- **File-like secrets** — 1Password documents / file fields and Secrets Manager
  binary secrets are delivered as base64 for the task to materialize.
- **On-disk cache with stale fallback** — repeat fetches are served locally, and
  deploys keep working through short backend outages.
- **Fails closed & credential-safe** — a partial multi-secret block never
  starts the task; credentials come only from host config, never the job.
- **Operator diagnostics** — `remote-secrets check` verifies config, backend
  connectivity, and token scope without exposing values.

## Quickstart

Requires **Nomad 1.11.0+** (the release that introduced the `secret` block) and
a backend credential. Full details in the [user guide](docs/user-guide.md).

```sh
# 1. Install the plugin on each Nomad client node — either option lands it at
#    <common_plugin_dir>/secrets/remote-secrets:
#    • prebuilt:    download remote-secrets_linux_<arch> from the Releases page,
#                   verify against SHA256SUMS, and install it (see user guide)
#    • from source: make install PLUGIN_DIR=/opt/nomad/plugins

# 2. Point the client agent at the plugin directory (examples/client.hcl)
#    client { enabled = true; common_plugin_dir = "/opt/nomad/plugins" }

# 3. Configure a backend at /etc/remote-secrets/config.env
install -d -m 0700 /etc/remote-secrets
echo "ops_eyJ..." > /etc/remote-secrets/token         # a 1Password service-account token
cat > /etc/remote-secrets/config.env <<'EOF'
OP_SERVICE_ACCOUNT_TOKEN_FILE=/etc/remote-secrets/token
EOF
chmod 0600 /etc/remote-secrets/config.env /etc/remote-secrets/token

# 4. Verify before restarting Nomad, then reload the agent
/opt/nomad/plugins/secrets/remote-secrets check
systemctl reload nomad
```

Then reference a secret from a job as shown above. See the
[user guide](docs/user-guide.md) for AWS setup, multi-secret blocks, file
secrets, caching, and troubleshooting.

## Documentation

- **[User guide](docs/user-guide.md)** — requirements, installation,
  configuration, reference syntax (1Password + AWS), file-like secrets, caching,
  compatibility, troubleshooting, and security notes.
- **[Developer guide](docs/developer-guide.md)** — architecture, project layout,
  building, testing (unit + end-to-end), adding a backend, and the release
  process.
- **[e2e/README.md](e2e/README.md)** — the end-to-end test harness.
- **[examples/](examples/)** — a client config, a host `config.env`, and a
  complete sample job.

## Contributing

Issues and pull requests are welcome. Before opening a PR, run `make lint` and
`make test`; CI enforces `gofmt`, `go vet`, and the full unit + end-to-end
matrix. See the [developer guide](docs/developer-guide.md) for the workflow.

## License

No `LICENSE` file is present yet, so no usage rights are granted by default.
Add a license (e.g. MPL-2.0 or Apache-2.0) to define how others may use this
project.
