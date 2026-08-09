# Developer guide

How the plugin is built, tested, and released, for contributors and maintainers.

- [Architecture](#architecture)
- [Project layout](#project-layout)
- [Building](#building)
- [Testing](#testing)
  - [Unit tests](#unit-tests)
  - [End-to-end tests](#end-to-end-tests)
  - [Manual smoke test](#manual-smoke-test)
- [Adding a backend](#adding-a-backend)
- [Releasing](#releasing)
- [Contributing](#contributing)

For install and operational docs, see the [user guide](user-guide.md).

## Architecture

`remote-secrets` is a **single scheme-routed binary** that implements Nomad's
[secret provider plugin](https://developer.hashicorp.com/nomad/plugins/author/secret-provider)
contract. Nomad invokes it as a short-lived subprocess, once per operation:

```
job spec                Nomad client                       backend
─────────               ────────────                       ───────
secret "db" {     →     runs plugin binary:          →     op:// → 1Password
  provider =            remote-secrets fetch                       aws-ssm: → SSM
    "remote-secrets"        op://Production/database/…          aws-sm:  → Secrets Mgr
  path = "op://…"
}                       ← {"result": {"password": …}}
                          │
env {                     ├─ written to on-disk cache
  DB_PASSWORD =           ▼
  "${secret.db.…}"}     interpolated into the task env,
                        injected into the Docker container
```

- Nomad **discovers** the plugin at agent startup by executing every binary in
  `<common_plugin_dir>/secrets/` with the `fingerprint` argument.
- For every `secret` block naming `provider = "remote-secrets"`, Nomad executes
  the plugin with `fetch <path>` and expects a JSON key/value result, which it
  exposes as `${secret.<name>.<key>}` interpolation variables.
- The plugin **routes** each reference to a backend by its scheme, resolves it,
  returns the values, and caches them on disk. The `check` subcommand is an
  operator-facing diagnostic that reuses the same config/resolve paths.

Credentials come only from host config / the Nomad agent environment, never from
the job — the scheme in a path selects a backend, not credentials.

## Project layout

```
main.go                         # entry point: routes fingerprint / fetch / check / version
internal/
  plugin/                       # Nomad plugin operations + host config loading
    plugin.go                   #   Fingerprint, Fetch, ConfigPaths, Version constant
    config.go                   #   LoadConfig (host file wins over env)
    check.go                    #   the `check` diagnostic
  provider/                     # backend-neutral abstraction
    provider.go                 #   Provider interface + Registry (scheme → backend)
    reference.go                #   multi-entry path splitting
    fileresult.go               #   file-like Result (value / value_base64 / filename)
    jsonexpand.go               #   JSON object auto-expansion
    onepassword/                #   op:// backend
      opref/                    #     reference parsing (op://vault/item[/section]/field?…)
      opitem/                   #     backend-neutral 1Password object model
      connect/                  #     Connect REST client (stdlib only)
      serviceaccount/           #     service-account SDK backend
    awsssm/                     #   aws-ssm: (Parameter Store)
    awssm/                      #   aws-sm:  (Secrets Manager)
  cache/                        # file-backed cache with TTL + stale fallback
e2e/                            # real-agent end-to-end suites (see e2e/README.md)
examples/                       # client.hcl, config.env, sample jobs
packer/ ansible/ terraform/     # optional AWS cluster provisioning
```

The plugin is **CGO-free** and builds a static binary. The 1Password backends
use the standard library only; the AWS backends pull in `aws-sdk-go-v2`.

## Building

```sh
make build            # → bin/remote-secrets (current platform)
make release          # → bin/remote-secrets_linux_{amd64,arm64}
make install PLUGIN_DIR=/opt/nomad/plugins   # install to <dir>/secrets/remote-secrets
make lint             # gofmt + go vet
make clean
```

Requires Go 1.24+. Nomad clients are Linux, so ship a Linux build (`make
release` cross-compiles); `make build` targets your current OS for local work.

## Testing

### Unit tests

```sh
make test        # go test ./...  — no network; Connect is faked with httptest
```

Fast and hermetic. Coverage includes reference parsing, multi-entry splitting,
JSON expansion, the file-result builder, per-backend resolution, and the cache's
TTL / stale-fallback behavior — plus every file and password type and their
failure paths (size limits, missing/ambiguous fields, unattached files,
permission-denied fetches, binary vs UTF-8 payloads).

### End-to-end tests

The [`e2e/`](../e2e/) suites boot a **real Nomad dev agent** with the plugin
installed and drive it against a hermetic fake 1Password Connect (or localstack
for AWS), so no credentials are needed and they are fork-safe. They require
`nomad` (≥ 1.11) on `PATH`, Docker, Go, `jq`, and sudo (the dev agent's client
mode needs root). See [`e2e/README.md`](../e2e/README.md) for details.

| Command | What it proves |
|---|---|
| `./e2e/run.sh` | Fingerprint + `secret` block resolution into the task env (docker driver; `E2E_DRIVER=raw_exec` for no-docker) |
| `./e2e/files/run.sh` | File secrets materialized into a running container, verified via `nomad alloc exec`: env values, file contents (sha256), and permissions (`stat`) for text/PEM/binary/JSON types |
| `./e2e/ui/run.sh` | The Nomad web UI never renders resolved secret values (Playwright); `--watch` / `--ui` drive a visible browser |
| `./e2e/aws/run.sh` | `aws-ssm:` / `aws-sm:` resolution against localstack |
| `./e2e/mixed/run.sh` | One `secret` block mixing 1Password + both AWS backends (per-reference scheme routing) |
| `E2E_MODE=real ./e2e/run.sh` | The same round-trip against a real 1Password vault via a service account |

CI (`.github/workflows/ci.yml`) runs the hermetic suites on every push/PR,
including the Nomad version matrix; the real-1Password job is opt-in via the
`OP_E2E_ENABLED` repo variable.

### Manual smoke test

Point the binary at a backend and exercise the raw plugin protocol:

```sh
export OP_CONNECT_HOST=http://127.0.0.1:8080
export OP_CONNECT_TOKEN=eyJ...
./bin/remote-secrets fingerprint
./bin/remote-secrets fetch "op://Production/database/password"
./bin/remote-secrets check "op://Production/database"
```

## Adding a backend

1. Implement the `provider.Provider` interface (see
   [`internal/provider/provider.go`](../internal/provider/provider.go)):
   `CacheKey`, `Resolve`, `Ping`, and `Describe`.
2. Register the scheme in the registry so `Scheme(ref)` routes to it.
3. Reuse `provider.FileResult` for file-like content and `provider.ExpandJSON`
   for JSON auto-expansion, so behavior stays consistent across backends.
4. Add unit tests (fake the transport) and, ideally, an `e2e/` variant.

## Releasing

Releases are cut by pushing a semver tag that **matches `plugin.Version`**. The
[release workflow](../.github/workflows/release.yml) verifies the match, runs
tests, cross-builds the Linux binaries, and publishes them (plus `SHA256SUMS`)
as GitHub Release assets.

```sh
# 1. bump the version constant
#    internal/plugin/plugin.go →  const Version = "1.2.3"
#    (also update the sample output in docs/user-guide.md)

# 2. commit on main, then tag and push
git commit -am "Release v1.2.3"
git push origin main
git tag -a v1.2.3 -m "v1.2.3"
git push origin v1.2.3        # ← triggers the release workflow
```

If the tag and `plugin.Version` disagree, the workflow fails fast before
publishing anything.

## Contributing

- Run `make lint` and `make test` before opening a PR; CI enforces `gofmt`,
  `go vet`, and the full test + e2e matrix.
- Keep the plugin CGO-free and the 1Password backends stdlib-only.
- Match the surrounding code's style, comment density, and error-message
  conventions (self-contained messages that name the reference, the failure, and
  the active backend/config).
- Secret values must never reach logs, job specs, or Nomad server state — only
  the task's resolved environment.
