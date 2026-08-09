# nomad-remote-secrets

[![CI](https://github.com/octdanb/nomad-remote-secrets/actions/workflows/ci.yml/badge.svg)](https://github.com/octdanb/nomad-remote-secrets/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/octdanb/nomad-remote-secrets?sort=semver)](https://github.com/octdanb/nomad-remote-secrets/releases)
[![Go](https://img.shields.io/github/go-mod/go-version/octdanb/nomad-remote-secrets)](go.mod)
![Nomad 1.11+](https://img.shields.io/badge/nomad-1.11%2B-00CA8E?logo=nomad)

A [Nomad secret provider plugin](https://developer.hashicorp.com/nomad/plugins/author/secret-provider)
that resolves secret references from multiple backends, so job specs can pull
secrets at deploy time **without running HashiCorp Vault**.

It is a single scheme-routed binary named `remote-secrets`: jobs always say
`provider = "remote-secrets"`, and the **reference scheme** selects the backend
at fetch time — so one `secret` block may mix providers.

| Provider | Scheme | Example |
|---|---|---|
| [1Password](https://developer.1password.com/) | `op://` | `op://Production/database/password` |
| [AWS Parameter Store](https://docs.aws.amazon.com/systems-manager/latest/userguide/systems-manager-parameter-store.html) | `aws-ssm:` | `aws-ssm:/prod/db/password` or a parameter ARN |
| [AWS Secrets Manager](https://docs.aws.amazon.com/secretsmanager/) | `aws-sm:` | `aws-sm:prod/db/creds` or a secret ARN |

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

At deploy time the Nomad client fetches the secret from the backend, caches it
locally, and interpolates it into the task — here as an environment variable
inside the container. Secrets never appear in the job spec, in Nomad server
state, or in plugin logs.

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
</content>
