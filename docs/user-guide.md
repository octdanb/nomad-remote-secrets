# User guide

Everything an operator needs to install, configure, and run
`nomad-remote-secrets` on a Nomad cluster.

- [Requirements](#requirements)
- [Installation](#installation)
- [Secret references](#secret-references)
  - [1Password references](#1password-references)
  - [Multiple secrets in one block](#multiple-secrets-in-one-block)
  - [AWS references](#aws-references)
  - [File-like secrets](#file-like-secrets)
- [Configuration](#configuration)
- [Caching](#caching)
- [Compatibility](#compatibility)
- [Troubleshooting](#troubleshooting)
- [Security notes](#security-notes)
- [Limitations](#limitations)

For architecture, building from source, and contributing, see the
[developer guide](developer-guide.md).

## Requirements

**Nomad 1.11.0+** — the release that introduced the `secret` block and custom
secret providers. Nomad 1.10 and earlier lack the feature and are unsupported
(see [Compatibility](#compatibility)).

**1Password** (for `op://` references) — one of:

- A **service account** (any paid plan): create and scope it read-only to the
  vaults your jobs use, then copy the `ops_...` token to the node:
  ```sh
  op service-account create nomad-<cluster> --vault "Production:read_items"
  ```
  Each client node needs outbound HTTPS to `1password.com`. Simplest to start;
  subject to [service-account rate limits](https://developer.1password.com/docs/service-accounts/rate-limits/)
  (the on-disk cache keeps fetch volume well under them).
- **or** a self-hosted **Connect** server: deploy Connect (two containers) with
  its `1password-credentials.json`, mint an access token
  (`op connect token create <name> --vault Production`), and set
  `OP_CONNECT_HOST` / `OP_CONNECT_TOKEN_FILE`. Reads are local and uncounted —
  for high fetch volume or nodes without internet egress.

You reference secrets by `op://vault/item[/section]/field`; vault and item must
be unique names or you must use their IDs.

**AWS** (for `aws-ssm:` / `aws-sm:` references):

- An AWS account and a region (`AWS_REGION`).
- Credentials via the **EC2/ECS instance role** (recommended) or **static keys**
  in the host config. Ambient IRSA/container-credential env vars are not
  honoured — see the [credential note](#aws-references).
- The IAM permissions in [AWS IAM requirements](#aws-iam-requirements).

## Installation

> Building AWS infrastructure? [`packer/`](../packer/) ships a versioned,
> org-shareable Ubuntu AMI (provisioned by the reusable [`ansible/`](../ansible/)
> roles), and [`terraform/`](../terraform/) turns one tfvars file into a full
> cluster with 1Password/SSM secret wiring. The steps below cover manual
> installation.

1. **Install the plugin binary on every Nomad client node** at
   `<common_plugin_dir>/secrets/remote-secrets`. Choose one of:

   **Option A — prebuilt binary from GitHub releases** (recommended). Each
   [release](https://github.com/octdanb/nomad-remote-secrets/releases) attaches
   `remote-secrets_linux_amd64`, `remote-secrets_linux_arm64`, and a
   `SHA256SUMS` file. Download, verify the checksum, and install:

   ```sh
   VERSION=v1.0.1                       # pick a release tag
   ARCH=amd64                           # or arm64
   base="https://github.com/octdanb/nomad-remote-secrets/releases/download/$VERSION"
   curl -fsSLO "$base/remote-secrets_linux_$ARCH"
   curl -fsSLO "$base/SHA256SUMS"
   sha256sum --check --ignore-missing SHA256SUMS   # verify integrity
   sudo install -D -m 0755 "remote-secrets_linux_$ARCH" \
     /opt/nomad/plugins/secrets/remote-secrets
   ```

   **Option B — build from source** (Go 1.24+). Nomad clients are Linux, so
   build for Linux — `make build` targets your current OS, `make release`
   cross-builds Linux amd64/arm64, and `make install` copies it into place:

   ```sh
   make release                                 # → bin/remote-secrets_linux_{amd64,arm64}
   make install PLUGIN_DIR=/opt/nomad/plugins   # → <dir>/secrets/remote-secrets
   ```

   Either way the file name **must** be `remote-secrets` — that is the provider
   name, so jobs say `provider = "remote-secrets"` — under a `secrets/`
   subdirectory of `common_plugin_dir`.

2. **Point the client agent at the plugin directory**
   ([`examples/client.hcl`](../examples/client.hcl)):

   ```hcl
   client {
     enabled           = true
     common_plugin_dir = "/opt/nomad/plugins"
   }
   ```

3. **Configure a backend on each node**
   ([`examples/config.env`](../examples/config.env)). The config file path is
   `/etc/remote-secrets/config.env`; `/etc/nomad.d/remote-secrets.env` is also
   consulted.

   **1Password** with a service account (scoped read-only to the vaults your
   jobs need):

   ```sh
   install -d -m 0700 /etc/remote-secrets
   echo "ops_eyJ..." > /etc/remote-secrets/token
   cat > /etc/remote-secrets/config.env <<'EOF'
   OP_SERVICE_ACCOUNT_TOKEN_FILE=/etc/remote-secrets/token
   EOF
   chmod 0600 /etc/remote-secrets/config.env /etc/remote-secrets/token
   ```

   Or with a self-hosted Connect server:

   ```sh
   cat > /etc/remote-secrets/config.env <<'EOF'
   OP_CONNECT_HOST=http://127.0.0.1:8080
   OP_CONNECT_TOKEN_FILE=/etc/remote-secrets/token
   EOF
   ```

   **AWS** (Parameter Store / Secrets Manager) — set the region. On EC2/ECS the
   [AWS SDK default credential chain](https://docs.aws.amazon.com/sdkref/latest/guide/standardized-credentials.html)
   picks up the instance profile / IRSA automatically. Off an instance role
   (or under Nomad's controlled plugin environment), pin static credentials in
   the host config so the SDK doesn't stall probing instance metadata:

   ```sh
   cat > /etc/remote-secrets/config.env <<'EOF'
   AWS_REGION=ap-southeast-2
   # AWS_ENDPOINT_URL=http://127.0.0.1:4566   # optional: localstack / VPC endpoint
   # Optional static credentials (otherwise the SDK default chain is used):
   # AWS_ACCESS_KEY_ID=AKIA...
   # AWS_SECRET_ACCESS_KEY=...
   # AWS_SESSION_TOKEN=...                     # optional, for temporary creds
   EOF
   ```

   Backends coexist: configure any subset. Each reference's scheme picks the
   backend, so a single node can serve `op://`, `aws-ssm:`, and `aws-sm:`.

4. **Verify** the node's config, backend connectivity, and credential scope
   *before* restarting Nomad — this catches a bad token or region without a
   failed deploy (see [Troubleshooting](#troubleshooting)):

   ```sh
   /opt/nomad/plugins/secrets/remote-secrets check
   ```

5. **Restart** (or SIGHUP) the Nomad client so it discovers the plugin, then
   confirm it registered:

   ```sh
   nomad node status -verbose $(nomad node status -quiet -self) | grep remote-secrets
   ```

## Secret references

The `path` parameter accepts a reference whose scheme selects the backend.

### 1Password references

The `op://` scheme accepts standard 1Password secret reference syntax:

| Reference | Resolves to |
|---|---|
| `op://vault/item/field` | one field — exposed as `${secret.<name>.value}` |
| `op://vault/item/section/field` | one field inside a section |
| `op://vault/item` | the whole item — every non-empty field by its label |

Details:

- Vault and item segments accept either names/titles or 1Password IDs. Names
  must match exactly (one result); if two vaults or items share a name, the
  error tells you to use the ID instead.
- Field segments match the field's label (case-insensitive), ID, or purpose
  (`username`, `password`, `notes`).
- For whole-item fetches, field labels are sanitized into interpolation-safe
  keys (`host name` → `host_name`); sectioned fields are prefixed with the
  section label (`replica` section's `password` → `replica_password`). Login
  items always expose `username`, `password`, and `notes` keys.
- Percent-encoded segments (`My%20Vault`) are decoded; literal spaces also
  work.

### Multiple secrets in one block

Nomad's `secret` block takes a single `path`, but the plugin accepts a list of
named references in it — one `name = <reference>` per line (an HCL heredoc
keeps it readable):

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

- Single-field entries are exposed under their name; whole-item entries expose
  every field prefixed with the name (`twilio` → `twilio_username`,
  `twilio_password`, ...).
- Names may contain letters, digits, and underscores; `#` lines are comments.
  Entries may also be comma-separated on one line.
- The fetch **fails closed**: if any one reference can't be resolved, the whole
  block errors and the task doesn't start with a partial secret set.
- Each reference is cached individually, so entries shared between blocks or
  jobs hit the same cache.

See [`examples/smspit.nomad.hcl`](../examples/smspit.nomad.hcl) for a complete
job. `secret` blocks can sit at the job, group, or task level; task level keeps
a secret scoped to the one task that needs it.

### AWS references

Both AWS backends need `AWS_REGION` (plus optional `AWS_ENDPOINT_URL`) in the
host config; credentials come from the EC2/ECS instance role, static keys, or a
profile in the host config (see [Installation](#installation) step 3 and the
credential note below). The scheme selects the service:

| Reference | Resolves to |
|---|---|
| `aws-ssm:/prod/db/password` | a Parameter Store parameter; `SecureString` is decrypted |
| `aws-ssm:arn:aws:ssm:…:parameter/prod/db/password` | the same, by ARN |
| `aws-sm:prod/db/creds` | a Secrets Manager secret's `SecretString` |
| `aws-sm:arn:aws:secretsmanager:…:secret:prod/db/creds-AbCdEf` | the same, by ARN |

Value shapes:

- A plain string is exposed as `${secret.<name>.value}`.
- A value that parses as a **JSON object** auto-expands: each key becomes
  `${secret.<name>.<key>}` (sanitized), and the raw string stays at
  `${secret.<name>.value}`. This mirrors the whole-item behavior of 1Password.
- Secrets Manager **binary** secrets (`SecretBinary`) are detected and exposed
  as `value_base64` (see [File-like secrets](#file-like-secrets)).

```hcl
secret "db" {
  provider = "remote-secrets"
  path     = <<-EOF
    password = aws-ssm:/prod/db/password
    creds    = aws-sm:prod/db/creds        # JSON → creds_username, creds_password
  EOF
}

env {
  DB_PASSWORD = "${secret.db.password}"
  DB_USER     = "${secret.db.creds_username}"
  DB_PASS     = "${secret.db.creds_password}"
}
```

The multi-entry syntax, JSON auto-expansion, fail-closed behavior, and
per-reference caching described under
[Multiple secrets in one block](#multiple-secrets-in-one-block) apply to every
scheme, and a single block may mix `op://`, `aws-ssm:`, and `aws-sm:` entries.

Per-reference options (append `?k=v`):

| Option | Scheme | Effect |
|---|---|---|
| `?raw=true` | `aws-ssm:`, `aws-sm:` | Do not auto-expand JSON; expose only `value` |
| `?decrypt=false` | `aws-ssm:` | Return the raw `SecureString` ciphertext (skip `WithDecryption`) |
| `?version=<id>` | `aws-sm:` | Fetch a specific `VersionId` |
| `?stage=<label>` | `aws-sm:` | Fetch a `VersionStage` (e.g. `AWSPREVIOUS`) |
| `?binary=true` | `aws-sm:` | Treat as binary even if `SecretString` is set (→ `value_base64`) |

#### AWS IAM requirements

The identity the plugin runs as (EC2 instance role, or the static keys in the
host config) needs, scoped to the parameters/secrets your jobs reference:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Sid": "RemoteSecretsSSM",
      "Effect": "Allow",
      "Action": ["ssm:GetParameter"],
      "Resource": "arn:aws:ssm:REGION:ACCOUNT:parameter/prod/*" },
    { "Sid": "RemoteSecretsSecretsManager",
      "Effect": "Allow",
      "Action": ["secretsmanager:GetSecretValue"],
      "Resource": "arn:aws:secretsmanager:REGION:ACCOUNT:secret:prod/*" },
    { "Sid": "RemoteSecretsKMSDecrypt",
      "Effect": "Allow",
      "Action": ["kms:Decrypt"],
      "Resource": "arn:aws:kms:REGION:ACCOUNT:key/KMS_KEY_ID" },
    { "Sid": "RemoteSecretsCheckDiagnostic",
      "Effect": "Allow",
      "Action": ["ssm:DescribeParameters", "secretsmanager:ListSecrets"],
      "Resource": "*" }
  ]
}
```

- Grant only the services you use — drop the SSM or Secrets Manager statement
  if unused.
- `kms:Decrypt` is required only for SSM `SecureString` values and for Secrets
  Manager secrets encrypted with a **customer-managed** KMS key. Secrets under
  the default AWS-managed key are covered by `secretsmanager:GetSecretValue`.
- The `RemoteSecretsCheckDiagnostic` list actions can't be resource-scoped;
  they back `remote-secrets check` connectivity verification. Omit them if you
  don't rely on `check` (fetches don't need them).
- The Terraform stack wires these via `remote_secrets_ssm_parameter_arns`,
  `remote_secrets_sm_secret_arns`, and `remote_secrets_kms_key_arns`.

> **Credential sources.** The plugin uses static keys from the host config, an
> explicit `AWS_PROFILE`, or the **EC2/ECS instance role via IMDS**. For
> security it does **not** honour ambient environment-based credentials
> (IRSA `AWS_WEB_IDENTITY_TOKEN_FILE`, ECS `AWS_CONTAINER_CREDENTIALS_*`): the
> plugin scrubs `AWS_*` from its environment so a job's `secret env{}` block
> can't redirect the SDK. Use an instance role or static keys in the host file.

### File-like secrets

Some secrets are files rather than scalar strings: a 1Password **document**, a
**file-field attachment**, or an AWS Secrets Manager **binary** secret. The
plugin never writes files itself (it's a short-lived per-fetch process, and
side-effecting the filesystem would break Nomad's rendering and redaction).
Instead it returns the content as ordinary interpolation keys.

A file reference returns:

| Key | Meaning |
|---|---|
| `value` | content as text — only when it is valid UTF-8 |
| `value_base64` | base64 of the raw bytes — always present, safe for binary |
| `filename` | original filename metadata, when the backend knows it |

Named file entries are prefixed like whole items, so a `cert` entry exposes
`cert_value`, `cert_value_base64`, and `cert_filename`.

**Materialize into a file** by exposing the value as an env var, then writing it
into the task's tmpfs secrets dir (`$NOMAD_SECRETS_DIR`, i.e. `secrets/`). The
env var is the required bridge: Nomad interpolates `${secret...}` into `env {}`
but **not** into a `template` block's `data`, so a template reads the value back
with `{{ env "..." }}` — you can't reference the secret in the template
directly.

Text file (UTF-8, e.g. a PEM bundle) — use the plain `value`. A `template`
renders it and sets permissions declaratively with `perms`:

```hcl
secret "cert" {
  provider = "remote-secrets"
  path     = "op://Prod/tls-bundle"          # a Document item (text)
}
env { BUNDLE = "${secret.cert.value}" }
template {
  destination = "secrets/bundle.pem"
  perms       = "0400"                        # owner read-only
  data        = "{{ env \"BUNDLE\" }}"
}
```

Binary file (e.g. a PKCS#12 keystore) — there is no UTF-8 `value`, so decode
`value_base64`. Decode in the entrypoint to keep the bytes exact and `chmod`:

```hcl
secret "ks" {
  provider = "remote-secrets"
  path     = "aws-sm:prod/tls/keystore"       # a SecretBinary secret
}
env { KEYSTORE_B64 = "${secret.ks.value_base64}" }
# in the task's command / entrypoint:
#   echo "$KEYSTORE_B64" | base64 -d > "$NOMAD_SECRETS_DIR/keystore.p12"
#   chmod 0400 "$NOMAD_SECRETS_DIR/keystore.p12"
```

A `template` can also decode with `{{ env "KEYSTORE_B64" | base64Decode }}` and
set `perms`, but a heredoc's trailing newline can corrupt exact-byte binaries —
prefer the entrypoint for binary files, the template for text.

Files in `$NOMAD_SECRETS_DIR` live on tmpfs, aren't rendered in the Nomad UI,
and are removed when the alloc stops.

#### Keeping the secret out of the task environment

The bridge env var (`BUNDLE`, `KEYSTORE_B64`, …) exists in the task's runtime
environment. Nomad **redacts secret-sourced values from its UI and API**, so it
never appears in the job definition or console — but the running container's
process environment still holds it (readable by the app, and by a host operator
via `docker inspect` / `/proc/<pid>/environ`). Two ways to keep it out of the
application:

**Unset it in a wrapper** — templates render *before* the task command runs, so
the file already exists; drop the var, then `exec` the app:

```hcl
config {
  command = "/bin/sh"
  args    = ["-c", "unset BUNDLE; exec /app --tls-cert secrets/bundle.pem"]
}
```

This removes it from the app and its children (it is still visible in
`docker inspect`, since it was passed to the container).

**Use a `prestart` lifecycle task** for full isolation — a short-lived task
holds the secret and writes the file to the shared alloc dir; the application
task declares no `secret` block and carries no secret env var at all:

```hcl
task "materialize" {
  lifecycle { hook = "prestart" }
  driver = "docker"

  secret "cert" {
    provider = "remote-secrets"
    path     = "op://Production/tls/bundle"
  }
  env { BUNDLE = "${secret.cert.value}" }

  config {
    image = "busybox:1.36"
    args  = ["sh", "-c",
      "printf %s \"$BUNDLE\" > ${NOMAD_ALLOC_DIR}/data/bundle.pem && chmod 0400 ${NOMAD_ALLOC_DIR}/data/bundle.pem"]
  }
}

task "app" {
  driver = "docker"
  config { image = "app:latest" }   # reads ${NOMAD_ALLOC_DIR}/data/bundle.pem
}
```

The shared alloc dir (`$NOMAD_ALLOC_DIR/data`) is readable by every task in the
group but not by other allocations, and is removed when the alloc stops.

Reference syntax per backend:

- **1Password** — a Document item (`op://Vault/MyDocument`) resolves to its
  content automatically. A FILE-type field (`op://Vault/Item/attachment`)
  resolves to that attachment. Force file semantics on any item or field with
  `?attribute=file`. `?encoding=base64` drops the utf8 `value`, leaving only
  `value_base64`.
- **AWS Secrets Manager** — a `SecretBinary` secret is detected automatically
  and base64-encoded; `?binary` forces binary handling and `?encoding=base64`
  drops the back-compat `value` (which otherwise carries the base64 string).
- **AWS Parameter Store** — strings only; no file semantics.

**Size guardrail** — `SECRET_MAX_FILE_BYTES` (default `1048576` = 1 MiB, `0`
disables) caps a file's raw byte length. Larger content is **rejected**, not
truncated: env-var interpolation has an OS size ceiling and base64 inflates
content ~33%. The error names the reference and the limit.

## Configuration

Settings come from (highest precedence first):

1. `/etc/remote-secrets/config.env`, or if absent
   `/etc/nomad.d/remote-secrets.env` — the host config file,
2. the plugin's process environment — the Nomad agent's environment plus any
   `env {}` block in the job's `secret` block.

| Setting | Default | Purpose |
|---|---|---|
| `OP_SERVICE_ACCOUNT_TOKEN` | — | Service account token (`ops_...`) — enables the direct backend |
| `OP_SERVICE_ACCOUNT_TOKEN_FILE` | — | File containing the service account token (preferred) |
| `OP_CONNECT_HOST` | — | Base URL of the Connect server (Connect backend) |
| `OP_CONNECT_TOKEN` | — | Connect access token |
| `OP_CONNECT_TOKEN_FILE` | — | File containing the Connect token (preferred) |
| `OP_CACHE_TTL` | `5m` | Serve cached values this long without re-fetching; `0` disables |
| `OP_CACHE_MAX_STALE` | `24h` | On backend outage, serve values up to this old; `0` disables |
| `OP_CACHE_DIR` | `/var/cache/remote-secrets` | Cache location |
| `OP_REQUEST_TIMEOUT` | `30s` | Per-fetch backend timeout (Nomad kills fetches at 60s) |
| `AWS_REGION` | — | AWS region; setting it (or `AWS_ENDPOINT_URL`) enables the AWS backends |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | — | Static AWS credentials (otherwise the SDK default chain / instance role is used) |
| `AWS_SESSION_TOKEN` | — | Session token for temporary AWS credentials |
| `AWS_PROFILE` | — | Shared-config profile to select instead of static keys |
| `AWS_ENDPOINT_URL` | — | Override the AWS endpoint (localstack, VPC/PrivateLink) |
| `AWS_SSM_DECRYPT` | `true` | Decrypt SSM `SecureString` values (`WithDecryption`) |
| `SECRET_MAX_FILE_BYTES` | `1048576` | Max raw byte length of a file-like secret; larger is rejected. `0` disables |

Durations accept Go syntax (`5m`, `90s`) or bare seconds (`300`). The `OP_*`
cache/timeout keys apply to every backend (the prefix is historical).

Backend selection is per-reference by scheme, and backends coexist. A backend
is *enabled* when configured: 1Password by a service account token (which wins)
or a Connect host+token; AWS by `AWS_REGION` or `AWS_ENDPOINT_URL`. At least one
must be configured or the plugin errors at startup.

### Choosing a 1Password backend

**Service accounts** need zero infrastructure — but every client node needs
outbound HTTPS to 1password.com, and requests count against 1Password's
[service account rate limits](https://developer.1password.com/docs/service-accounts/rate-limits/)
(hourly per-account read/write caps plus daily caps by plan, e.g. 10,000/day per
service account on Business). The plugin's on-disk cache keeps deploy-time fetch
volume far below these caps for typical clusters. You can create up to 100
service accounts per 1Password account, so one token per cluster or environment
is a reasonable pattern.

**Connect** self-hosts a cache of your vaults, so reads are local, fast, and
uncounted — the right choice for very high fetch volumes or client nodes without
internet egress. It costs you two containers to run plus a
`1password-credentials.json` deployment credential to protect.

> **Why the config file beats the environment.** Nomad passes a job's `env {}`
> block into the plugin's process environment. If the environment took
> precedence, any job author could set `OP_CONNECT_HOST` to a server they
> control and the plugin would send it the operator's Connect token. With the
> host file in place, jobs can only fill in settings the operator left unset —
> so always ship the config file in production. The `env {}` block remains
> useful on dev clusters that have no host file.

## Caching

Nomad executes the plugin once per secret fetch, so caching lives on disk:

- Fetched values are stored under `OP_CACHE_DIR` (directory `0700`, files
  `0600`, readable only by the Nomad agent user). File names are SHA-256 hashes,
  so reference paths don't leak into directory listings.
- Within `OP_CACHE_TTL` (default 5 minutes), repeat fetches of the same
  reference — job restarts, several tasks sharing one secret — are answered from
  cache without touching the backend.
- If the backend is unreachable, the plugin serves the last known value for up
  to `OP_CACHE_MAX_STALE` (default 24 hours) and logs a warning to stderr, so
  deploys and restarts keep working through short outages.
- Cache entries are keyed by backend + token digest + reference, so values are
  never shared across different servers, accounts, or tokens.

Consequence of caching: after rotating a secret, clients may serve the old value
for up to the TTL. Set `OP_CACHE_TTL=0` in the host config if you always want a
live read (stale fallback still applies).

Note that changed secrets reach a task only when it is restarted or redeployed —
Nomad resolves `secret` blocks at task start, not continuously.

## Compatibility

CI runs the real-agent end-to-end suite against a matrix of Nomad versions on
every push, so the supported range is continuously verified:

| Nomad | Status |
|---|---|
| ≤ 1.10.x | **Unsupported** — no `secret` block / secret-provider API (CI runs it allowed-to-fail to document the boundary) |
| 1.11.0 | Supported — minimum version |
| 1.11.x (latest) | Supported |
| 2.0.x (latest) | Supported |

The plugin speaks the stable fingerprint/fetch contract, so newer Nomad releases
are expected to work; open an issue if a version regresses.

## Troubleshooting

When a secret can't be fetched, the task fails to start and Nomad surfaces the
plugin's error as a **task event on the allocation** — in the UI under *Job →
allocation → task → Events*, and on the CLI via `nomad alloc status <alloc-id>`
or `nomad job status <job>` (recent events). Nomad then retries per the job's
`restart`/`reschedule` policy, so a misconfigured secret shows up as a task
cycling through restarts with the same event message.

Error messages are written to be self-contained: they name the exact reference,
what failed, and which backend and config file were active, e.g.

```
entry "db_password": resolving op://Production/database/password: no vault
named "Production" is visible to this service account [backend: 1Password
service account; config: /etc/remote-secrets/config.env; try
`remote-secrets check` on this node]
```

Distinct failures produce distinct messages: an invalid/expired token, a vault
that doesn't exist *or isn't granted to the credential* (1Password can't
distinguish these — both read "not visible"), a missing item or field, an
ambiguous name, and network timeouts all say so explicitly.

To dig deeper, run the diagnostic on the client node:

```sh
# verify config, backend, cache, connectivity, and token scope
$ remote-secrets check
remote-secrets provider v1.0.1 — diagnostic

OK   config loaded from: /etc/remote-secrets/config.env
OK   backend: 1Password service account
     request timeout 30s, cache TTL 5m0s, max stale 24h0m0s
OK   cache: /var/cache/remote-secrets
OK   connectivity: 2 vault(s) visible: Infrastructure, Production

# dry-run any reference (or a full multi-entry path) — prints the
# interpolation keys that would be exposed, never the values
$ remote-secrets check "op://Production/database"
OK   op://Production/database → keys: host_name, password, username
```

On failure, `check` prints a `FAIL` line with a hint for the likely fix (wrong
token, missing vault grant, exact-title mismatch, network) and exits non-zero,
so it also works as a provisioning smoke test.

Warnings that don't fail a fetch (stale cache served during an outage,
unwritable cache directory) go to the plugin's stderr, which lands in the
**Nomad client agent logs** on that node.

### Nomad can't find the plugin

If `nomad node status -verbose <node> | grep remote-secrets` returns nothing:

- **Wrong path** — the binary must be at
  `<common_plugin_dir>/secrets/remote-secrets`. The `secrets/` subdirectory is
  required (it's Nomad's secrets-plugin type dir).
- **Not executable** — `chmod 0755` the binary.
- **`common_plugin_dir` unset** — set it in the client stanza (step 2) and make
  sure the agent actually loaded that config.
- **Not rescanned** — Nomad only scans the plugin dir at agent **start or
  SIGHUP**. After installing/updating the binary, `systemctl reload nomad` (or
  restart). A change to `/etc/remote-secrets/config.env` needs **no** restart —
  the plugin is exec'd per fetch and reads config fresh.
- **Nomad too old** — the feature requires 1.11.0+ (see
  [Compatibility](#compatibility)).
- **SELinux/AppArmor** — on enforcing hosts, ensure the plugin binary and
  `/var/cache/remote-secrets` carry the right contexts (e.g. `restorecon`).

## Security notes

- The plugin runs as the Nomad agent user (typically root). Tokens and the cache
  are readable only by that user.
- Service account tokens can be created with an expiry
  (`op service-account create --expires-in ...`); expiring tokens plus a
  rotation step in your image pipeline beats long-lived credentials.
- Secret values never appear in job specs, in Nomad server state, or in plugin
  logs — only in the task's resolved environment.
- Credentials and endpoints come only from the host config file (or the agent
  environment for keys it leaves unset) — never from the job. Nomad merges a
  job's `secret { env {} }` into the plugin's environment, so the plugin reads
  the host file with precedence and **scrubs all `AWS_*` variables** before the
  AWS SDK runs, preventing a job from redirecting the SDK (e.g. via
  `AWS_ENDPOINT_URL`) to capture instance-role-signed requests. Ship the host
  config file on production nodes.
- Prefer `OP_CONNECT_TOKEN_FILE` over inline `OP_CONNECT_TOKEN`, and scope the
  Connect token to only the vaults your jobs need.
- Anyone who can submit jobs to a client can read any secret the node's
  credential can see. Use separate tokens (or servers) per cluster/environment
  if you need stronger isolation, and Nomad namespaces with ACLs to control who
  can submit jobs where.

## Limitations

- Supported query attributes are `?attribute=file`, `?encoding=base64`
  (1Password); `?raw`, `?decrypt` (Parameter Store); and `?raw`, `?binary`,
  `?encoding=base64`, `?version`, `?stage` (Secrets Manager). See
  [AWS references](#aws-references) and [File-like secrets](#file-like-secrets).
- The `env {}` block in a `secret` stanza does not support Nomad variable
  interpolation (values arrive as literal strings — a
  [known Nomad limitation](https://github.com/hashicorp/nomad/issues/27569)).
