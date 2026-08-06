# nomad-secret-plugin: 1Password for Nomad

A [Nomad secret provider plugin](https://developer.hashicorp.com/nomad/plugins/author/secret-provider)
that resolves 1Password secret references — `op://vault/item/field` — through a
[1Password Connect](https://developer.1password.com/docs/connect/) server, so
job specs can pull secrets straight from 1Password without running HashiCorp
Vault.

```hcl
task "app" {
  driver = "docker"

  secret "db" {
    provider = "onepassword"
    path     = "op://Production/database/password"
  }

  env {
    DB_PASSWORD = "${secret.db.value}"
  }
}
```

At deploy time the Nomad client fetches the secret from Connect, caches it
locally, and interpolates it into the task — here as an environment variable
inside the Docker container. Secrets never appear in the job spec itself.

Requires **Nomad 1.11.0+** (the release that introduced the `secret` block
and custom secret providers) and a running 1Password Connect server.

## How it works

```
job spec                Nomad client                       1Password
─────────               ────────────                       ─────────
secret "db" {     →     runs plugin binary:          →     Connect API
  provider =            onepassword fetch                  GET /v1/vaults/…
    "onepassword"         op://Production/database/…       GET /v1/…/items/…
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
- For every `secret` block naming `provider = "onepassword"`, Nomad executes
  the plugin with `fetch <path>` and expects a JSON key/value result, which
  it exposes as `${secret.<name>.<key>}` interpolation variables.
- The plugin resolves the `op://` reference against Connect, returns the
  values, and caches them on disk (see [Caching](#caching)).

## Installation

1. Build the binary (Go 1.24+):

   ```sh
   make build            # → bin/onepassword
   ```

2. Install it on **every Nomad client node** as
   `<common_plugin_dir>/secrets/onepassword`:

   ```sh
   make install PLUGIN_DIR=/opt/nomad/plugins
   ```

   The file name is the provider name — jobs say `provider = "onepassword"`
   because the binary is called `onepassword`.

3. Point the client agent at the plugin directory
   ([examples/client.hcl](examples/client.hcl)):

   ```hcl
   client {
     enabled           = true
     common_plugin_dir = "/opt/nomad/plugins"
   }
   ```

4. Configure the Connect connection on each node
   ([examples/onepassword.env](examples/onepassword.env)):

   ```sh
   install -d -m 0700 /etc/nomad-secret-onepassword
   cat > /etc/nomad-secret-onepassword/config.env <<'EOF'
   OP_CONNECT_HOST=http://127.0.0.1:8080
   OP_CONNECT_TOKEN_FILE=/etc/nomad-secret-onepassword/token
   EOF
   chmod 0600 /etc/nomad-secret-onepassword/config.env
   ```

5. Restart (or SIGHUP) the Nomad client and confirm registration:

   ```sh
   nomad node status -verbose $(nomad node status -quiet -self) | grep onepassword
   ```

## Secret references

The `path` parameter accepts standard 1Password secret reference syntax:

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

See [examples/smspit.nomad.hcl](examples/smspit.nomad.hcl) for a complete job.

`secret` blocks can sit at the job, group, or task level; task level keeps a
secret scoped to the one task that needs it.

## Configuration

Settings come from (highest precedence first):

1. `/etc/nomad-secret-onepassword/config.env`, or if absent
   `/etc/nomad.d/onepassword.env` — the host config file,
2. the plugin's process environment — the Nomad agent's environment plus any
   `env {}` block in the job's `secret` block.

| Setting | Default | Purpose |
|---|---|---|
| `OP_CONNECT_HOST` | — (required) | Base URL of the Connect server |
| `OP_CONNECT_TOKEN` | — | Connect access token |
| `OP_CONNECT_TOKEN_FILE` | — | File containing the token (preferred) |
| `OP_CACHE_TTL` | `5m` | Serve cached values this long without re-fetching; `0` disables |
| `OP_CACHE_MAX_STALE` | `24h` | On Connect outage, serve values up to this old; `0` disables |
| `OP_CACHE_DIR` | `/var/cache/nomad-secret-onepassword` | Cache location |
| `OP_REQUEST_TIMEOUT` | `30s` | Per-fetch Connect timeout (Nomad kills fetches at 60s) |

Durations accept Go syntax (`5m`, `90s`) or bare seconds (`300`).

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

## Security notes

- The plugin runs as the Nomad agent user (typically root). The Connect
  token and cache are readable only by that user.
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

The plugin is dependency-free Go (standard library only).

### Manual smoke test

```sh
export OP_CONNECT_HOST=http://127.0.0.1:8080
export OP_CONNECT_TOKEN=eyJ...
./bin/onepassword fingerprint
./bin/onepassword fetch "op://Production/database/password"
```

## Limitations

- Document/file attachments on items are not supported — only fields.
- `?attribute=otp` is the only supported query attribute.
- The `env {}` block in a `secret` stanza does not support Nomad variable
  interpolation (values arrive as literal strings — a
  [known Nomad limitation](https://github.com/hashicorp/nomad/issues/27569)).
