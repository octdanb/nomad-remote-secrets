# nomad-secret-plugin: multi-provider secrets for Nomad

A [Nomad secret provider plugin](https://developer.hashicorp.com/nomad/plugins/author/secret-provider)
that resolves secret references from multiple backends so job specs can pull
secrets at deploy time without running HashiCorp Vault. It is a **single
scheme-routed binary** named `secrets`: jobs always say `provider = "secrets"`,
and the **reference scheme** selects the backend at fetch time — so one
`secret` block may mix providers.

| Provider | Scheme | Example |
|---|---|---|
| [1Password](https://developer.1password.com/) | `op://` | `op://Production/database/password` |
| [AWS Parameter Store](https://docs.aws.amazon.com/systems-manager/latest/userguide/systems-manager-parameter-store.html) | `aws-ssm:` | `aws-ssm:/prod/db/password` or a parameter ARN |
| [AWS Secrets Manager](https://docs.aws.amazon.com/secretsmanager/) | `aws-sm:` | `aws-sm:prod/db/creds` or a secret ARN |

Credentials come only from host config / the Nomad agent environment, never
from the job — the scheme in a path selects a backend, not credentials.

```hcl
task "app" {
  driver = "docker"

  secret "db" {
    provider = "secrets"
    path     = "op://Production/database/password"
  }

  env {
    DB_PASSWORD = "${secret.db.value}"
  }
}
```

At deploy time the Nomad client fetches the secret from the backend, caches it
locally, and interpolates it into the task — here as an environment variable
inside the Docker container. Secrets never appear in the job spec itself.

> **Back-compat: `provider = "onepassword"` still works.** This plugin was
> previously the 1Password-only `onepassword` plugin. The `install` targets lay
> the same binary down under both `secrets` and `onepassword` in
> `<plugin_dir>/secrets/` (the binary dispatches on its operation argument, not
> its filename), and the host config path `/etc/nomad-secret-onepassword/`
> keeps working. Existing jobs and clusters need no change.

Requires **Nomad 1.11.0+** (the release that introduced the `secret` block and
custom secret providers). 1Password needs a service account or Connect server;
AWS needs credentials via the SDK default chain (see below).

## How it works

```
job spec                Nomad client                       backend
─────────               ────────────                       ───────
secret "db" {     →     runs plugin binary:          →     op:// → 1Password
  provider =            secrets fetch                       aws-ssm: → SSM
    "secrets"            op://Production/database/…          aws-sm:  → Secrets Mgr
  path = "op://…"
}                       ← {"result": {"password": …}}
                          │
env {                     ├─ written to on-disk cache
  DB_PASSWORD =           ▼
  "${secret.db.…}"}     interpolated into the task env,
                        injected into the Docker container
```

- Nomad discovers the plugin at agent startup by executing every binary in
  `<common_plugin_dir>/secrets/` with the `fingerprint` argument.
- For every `secret` block naming `provider = "secrets"` (or the back-compat
  `provider = "onepassword"`), Nomad executes the plugin with `fetch <path>`
  and expects a JSON key/value result, which it exposes as
  `${secret.<name>.<key>}` interpolation variables.
- The plugin routes each reference to a backend by its scheme, resolves it,
  returns the values, and caches them on disk (see [Caching](#caching)).

## Installation

> Building AWS infrastructure? [packer/](packer/) ships a versioned,
> org-shareable Ubuntu AMI (ap-southeast-2, provisioned by the reusable
> [ansible/](ansible/) roles), and [terraform/](terraform/) turns one tfvars
> file into a full cluster: server/pool ASGs, ingress NLB with
> `nomad.<app>.<env>` / `traefik.<app>.<env>` DNS, ECR, optional
> S3+CloudFront, and 1Password/SSM secret wiring. The steps below cover
> manual installation.

1. Build the binary (Go 1.24+):

   ```sh
   make build            # → bin/secrets
   ```

2. Install it on **every Nomad client node** as
   `<common_plugin_dir>/secrets/secrets`:

   ```sh
   make install PLUGIN_DIR=/opt/nomad/plugins
   ```

   The file name is the provider name — jobs say `provider = "secrets"`
   because the binary is called `secrets`. `make install` also creates an
   `onepassword` symlink to the same binary in that directory, so
   `provider = "onepassword"` jobs keep resolving.

3. Point the client agent at the plugin directory
   ([examples/client.hcl](examples/client.hcl)):

   ```hcl
   client {
     enabled           = true
     common_plugin_dir = "/opt/nomad/plugins"
   }
   ```

4. Configure a backend on each node
   ([examples/onepassword.env](examples/onepassword.env)). The config file
   path is `/etc/nomad-secret-onepassword/config.env` (kept for back-compat;
   `/etc/nomad.d/onepassword.env` is also consulted).

   **1Password** with a service account (create one in 1Password, scoped
   read-only to the vaults your jobs need):

   ```sh
   install -d -m 0700 /etc/nomad-secret-onepassword
   echo "ops_eyJ..." > /etc/nomad-secret-onepassword/token
   cat > /etc/nomad-secret-onepassword/config.env <<'EOF'
   OP_SERVICE_ACCOUNT_TOKEN_FILE=/etc/nomad-secret-onepassword/token
   EOF
   chmod 0600 /etc/nomad-secret-onepassword/config.env /etc/nomad-secret-onepassword/token
   ```

   Or with a self-hosted Connect server:

   ```sh
   cat > /etc/nomad-secret-onepassword/config.env <<'EOF'
   OP_CONNECT_HOST=http://127.0.0.1:8080
   OP_CONNECT_TOKEN_FILE=/etc/nomad-secret-onepassword/token
   EOF
   ```

   **AWS** (Parameter Store / Secrets Manager) — set the region. On EC2/ECS the
   [AWS SDK default credential chain](https://docs.aws.amazon.com/sdkref/latest/guide/standardized-credentials.html)
   picks up the instance profile / IRSA automatically. Off an instance role
   (or under Nomad's controlled plugin environment), pin static credentials in
   the host config so the SDK doesn't stall probing instance metadata:

   ```sh
   cat > /etc/nomad-secret-onepassword/config.env <<'EOF'
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

5. Restart (or SIGHUP) the Nomad client and confirm registration:

   ```sh
   nomad node status -verbose $(nomad node status -quiet -self) | grep secrets
   ```

## Secret references

The `path` parameter accepts a reference whose scheme selects the backend.
The 1Password (`op://`) syntax is documented in detail below; the AWS schemes
are covered in [AWS references](#aws-references).

### 1Password references

The `op://` scheme accepts standard 1Password secret reference syntax:

| Reference                                | Resolves to |
|------------------------------------------|-------------|
| `op://vault/item/field`                  | one field — exposed as `${secret.<name>.value}` |
| `op://vault/item/section/field`          | one field inside a section |
| `op://vault/item`                        | the whole item — every non-empty field by its label |
| `op://vault/item/field?attribute=otp`    | the current TOTP code of an OTP field (never cached) |

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

Nomad's `secret` block takes a single `path`, but the plugin accepts a list
of named references in it — one `name = op://...` per line (an HCL heredoc
keeps it readable):

```hcl
secret "app" {
  provider = "onepassword"
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

- Single-field entries are exposed under their name; whole-item entries
  expose every field prefixed with the name (`twilio` → `twilio_username`,
  `twilio_password`, ...).
- Names may contain letters, digits, and underscores; `#` lines are
  comments. Entries may also be comma-separated on one line.
- The fetch fails closed: if any one reference can't be resolved, the whole
  block errors and the task doesn't start with a partial secret set.
- Each reference is cached individually, so entries shared between blocks
  or jobs hit the same cache.

See [examples/smspit.nomad.hcl](examples/smspit.nomad.hcl) for a complete job.

`secret` blocks can sit at the job, group, or task level; task level keeps a
secret scoped to the one task that needs it.

## AWS references

Both AWS backends need only `AWS_REGION` (plus optional `AWS_ENDPOINT_URL`)
in the host config; credentials come from the AWS SDK default chain (see
[Installation](#installation), step 4). The scheme selects the service:

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
  provider = "secrets"
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

## File-like secrets

Some secrets are files rather than scalar strings: a 1Password **document**, a
**file-field attachment**, or an AWS Secrets Manager **binary** secret. The
plugin never writes files itself (it's a short-lived per-fetch process, and
side-effecting the filesystem would break Nomad's rendering and redaction).
Instead it returns the content as ordinary interpolation keys that the job
materializes into tmpfs `secrets/` with a `template` block.

A file reference returns:

| Key | Meaning |
|---|---|
| `value` | content as text — only when it is valid UTF-8 |
| `value_base64` | base64 of the raw bytes — always present, safe for binary |
| `filename` | original filename metadata, when the backend knows it |

Named file entries are prefixed like whole items, so a `cert` entry exposes
`cert_value`, `cert_value_base64`, and `cert_filename`.

**Text file** (UTF-8, e.g. a PEM bundle):

```hcl
secret "cert" {
  provider = "secrets"
  path     = "bundle = op://Prod/tls-bundle" # a Document item
}

template {
  data        = "${secret.cert.bundle_value}"
  destination = "secrets/bundle.pem"
  perms       = "0400"
}
```

**Binary file** (e.g. a PKCS#12 keystore) — decode the base64:

```hcl
secret "ks" {
  provider = "secrets"
  path     = "keystore = aws-sm:prod/tls/keystore" # a SecretBinary secret
}

template {
  data        = "{{ \"${secret.ks.keystore_value_base64}\" | base64Decode }}"
  destination = "secrets/keystore.p12"
  perms       = "0400"
}
```

Reference syntax per backend:

- **1Password** — a Document item (`op://Vault/MyDocument`) resolves to its
  content automatically. A FILE-type field (`op://Vault/Item/attachment`)
  resolves to that attachment. Force file semantics on any item or field with
  `?attribute=file`. `?encoding=base64` drops the utf8 `value`, leaving only
  `value_base64`. `?attribute=otp` is unchanged.
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

1. `/etc/nomad-secret-onepassword/config.env`, or if absent
   `/etc/nomad.d/onepassword.env` — the host config file,
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
| `OP_CACHE_MAX_STALE` | `24h` | On Connect outage, serve values up to this old; `0` disables |
| `OP_CACHE_DIR` | `/var/cache/nomad-secret-onepassword` | Cache location |
| `OP_REQUEST_TIMEOUT` | `30s` | Per-fetch Connect timeout (Nomad kills fetches at 60s) |
| `SECRET_MAX_FILE_BYTES` | `1048576` | Max raw byte length of a file-like secret; larger is rejected. `0` disables |

Durations accept Go syntax (`5m`, `90s`) or bare seconds (`300`).

Backend selection: if a service account token is configured, it is used and
any Connect settings are ignored; otherwise Connect requires both
`OP_CONNECT_HOST` and a token. Exactly one backend serves each fetch.

### Choosing a backend

**Service accounts** need zero infrastructure — but every client node needs
outbound HTTPS to 1password.com, and requests count against 1Password's
[service account rate limits](https://developer.1password.com/docs/service-accounts/rate-limits/)
(hourly per-account read/write caps plus daily caps by plan, e.g. 10,000/day
per service account on Business). The plugin's on-disk cache keeps
deploy-time fetch volume far below these caps for typical clusters. You can
create up to 100 service accounts per 1Password account, so one token per
cluster or environment is a reasonable pattern.

**Connect** self-hosts a cache of your vaults, so reads are local, fast, and
uncounted — the right choice for very high fetch volumes or client nodes
without internet egress. It costs you two containers to run plus a
`1password-credentials.json` deployment credential to protect.

> **Why the config file beats the environment.** Nomad passes a job's
> `env {}` block into the plugin's process environment. If the environment
> took precedence, any job author could set `OP_CONNECT_HOST` to a server
> they control and the plugin would send it the operator's Connect token.
> With the host file in place, jobs can only fill in settings the operator
> left unset — so always ship the config file in production. The `env {}`
> block remains useful on dev clusters that have no host file.

## Caching

Nomad executes the plugin once per secret fetch, so caching lives on disk:

- Fetched values are stored under `OP_CACHE_DIR` (directory `0700`, files
  `0600`, readable only by the Nomad agent user). File names are SHA-256
  hashes, so `op://` paths don't leak into directory listings.
- Within `OP_CACHE_TTL` (default 5 minutes), repeat fetches of the same
  reference — job restarts, several tasks sharing one secret — are answered
  from cache without touching Connect.
- If Connect is unreachable, the plugin serves the last known value for up
  to `OP_CACHE_MAX_STALE` (default 24 hours) and logs a warning to stderr,
  so deploys and restarts keep working through short outages.
- Cache entries are keyed by Connect host + token digest + reference, so
  values are never shared across different servers or tokens.
- OTP codes (`?attribute=otp`) are never cached.

Consequence of caching: after rotating a secret in 1Password, clients may
serve the old value for up to the TTL. Set `OP_CACHE_TTL=0` in the host
config if you always want a live read (stale fallback still applies).

Note that changed secrets reach a task only when it is restarted or
redeployed — Nomad resolves `secret` blocks at task start, not continuously.

## Debugging

When a secret can't be fetched, the task fails to start and Nomad surfaces
the plugin's error as a **task event on the allocation** — in the UI under
*Job → allocation → task → Events*, and on the CLI via `nomad alloc status
<alloc-id>` or `nomad job status <job>` (recent events). Nomad then retries
per the job's `restart`/`reschedule` policy, so a misconfigured secret shows
up as a task cycling through restarts with the same event message.

Error messages are written to be self-contained: they name the exact
reference, what failed, and which backend and config file were active, e.g.

```
entry "db_password": resolving op://Production/database/password: no vault
named "Production" is visible to this service account [backend: 1Password
service account; config: /etc/nomad-secret-onepassword/config.env; try
`secrets check` on this node]
```

Distinct failures produce distinct messages: an invalid/expired token, a
vault that doesn't exist *or isn't granted to the credential* (1Password
can't distinguish these — both read "not visible"), a missing item or field,
an ambiguous name, and network timeouts all say so explicitly.

To dig deeper, run the diagnostic on the client node:

```sh
# verify config, backend, cache, connectivity, and token scope
$ secrets check
secrets provider v0.4.0 — diagnostic

OK   config loaded from: /etc/nomad-secret-onepassword/config.env
OK   backend: 1Password service account
     request timeout 30s, cache TTL 5m0s, max stale 24h0m0s
OK   cache: /var/cache/nomad-secret-onepassword
OK   connectivity: 2 vault(s) visible: Infrastructure, Production

# dry-run any reference (or a full multi-entry path) — prints the
# interpolation keys that would be exposed, never the values
$ secrets check "op://Production/database"
OK   op://Production/database → keys: host_name, password, username
```

On failure, `check` prints a `FAIL` line with a hint for the likely fix
(wrong token, missing vault grant, exact-title mismatch, network) and exits
non-zero, so it also works as a provisioning smoke test.

Warnings that don't fail a fetch (stale cache served during an outage,
unwritable cache directory) go to the plugin's stderr, which lands in the
**Nomad client agent logs** on that node.

## Security notes

- The plugin runs as the Nomad agent user (typically root). Tokens and the
  cache are readable only by that user.
- Service account tokens can be created with an expiry (`op
  service-account create --expires-in ...`); expiring tokens plus a rotation
  step in your image pipeline beats long-lived credentials.
- Secret values never appear in job specs, in Nomad server state, or in
  plugin logs — only in the task's resolved environment.
- Prefer `OP_CONNECT_TOKEN_FILE` over inline `OP_CONNECT_TOKEN`, and scope
  the Connect token to only the vaults your jobs need.
- Anyone who can submit jobs to a client can read any secret the node's
  Connect token can see. Use separate Connect tokens (or servers) per
  cluster/environment if you need stronger isolation, and Nomad namespaces
  with ACLs to control who can submit jobs where.

## Development

```sh
make test      # unit tests (no network; Connect is faked with httptest)
make lint      # gofmt + go vet
make build     # static binary for the current platform
make release   # linux/amd64 + linux/arm64
```

The plugin is CGO-free and builds a static binary. The 1Password backends use
the standard library only; the AWS backends pull in `aws-sdk-go-v2`.

### Manual smoke test

```sh
export OP_CONNECT_HOST=http://127.0.0.1:8080
export OP_CONNECT_TOKEN=eyJ...
./bin/secrets fingerprint
./bin/secrets fetch "op://Production/database/password"
```

## Limitations

- Supported query attributes are `?attribute=otp`, `?attribute=file`,
  `?encoding=base64` (1Password) and `?binary`, `?encoding=base64`
  (Secrets Manager); see [File-like secrets](#file-like-secrets).
- The `env {}` block in a `secret` stanza does not support Nomad variable
  interpolation (values arrive as literal strings — a
  [known Nomad limitation](https://github.com/hashicorp/nomad/issues/27569)).
