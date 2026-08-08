# Plan: generalize into a multi-provider Nomad secret plugin

Turn the 1Password-specific plugin into a provider-neutral one supporting
**1Password**, **AWS Parameter Store (SSM)**, and **AWS Secrets Manager**, plus
a pattern for **file-like secrets** (documents, attachments, binary secrets).

## Design decisions (locked)

- **One binary, `secrets`.** Jobs use `provider = "remote-secrets"` everywhere. The
  **reference scheme** selects the backend at fetch time, so a single secret
  block may mix providers.
- **JSON auto-expands to keys.** When a fetched value parses as a JSON object,
  each key is exposed as `${secret.block.<key>}` (sanitized) and the raw string
  stays at `${secret.block.value}`. Mirrors 1Password whole-item behavior.
- **Credentials only from host config / agent env, never the job.** The scheme
  in a job's path selects a backend, not credentials — no redirection risk.
- Back-compat: the `onepassword` binary name and `/etc/remote-secrets/`
  config path keep working via install aliases.

### Reference schemes

| Provider | Scheme | Example | Value shape |
|---|---|---|---|
| 1Password | `op://` | `op://Prod/database/password` | field / whole-item |
| AWS Parameter Store | `aws-ssm:` | `aws-ssm:/prod/db/password` or an ARN | string, or JSON→keys; SecureString decrypted |
| AWS Secrets Manager | `aws-sm:` | `aws-sm:prod/db/creds` or an ARN | SecretString (JSON→keys) / binary |

## Target package layout

```
internal/
  provider/
    provider.go        # Provider interface, Registry, scheme routing
    reference.go       # generic "name = <ref>" multi-entry splitter
    jsonexpand.go      # shared JSON-object → keys helper (AWS providers)
    onepassword/       # 1Password provider (moved from internal/{connect,serviceaccount,opitem,opref} + resolve logic)
    awsssm/            # AWS Parameter Store provider
    awssm/             # AWS Secrets Manager provider
  cache/               # unchanged (on-disk TTL + stale fallback)
  plugin/              # provider-neutral fingerprint/fetch/check + config
```

### Core interface

```go
type Provider interface {
    Scheme() string
    Resolve(ctx context.Context, ref string) (Result, error)
    Ping(ctx context.Context) error   // for `check`
    CacheID() string                  // account/region/host — namespaces cache keys
}

type Result struct {
    Values map[string]string // interpolation keys (value, value_base64, filename, or object keys)
    Object bool              // true for whole-item / JSON object (drives named-entry key prefixing)
}
```

The plugin's fetch loop stays provider-neutral: split `name = <ref>` entries,
route each ref to a provider by scheme, resolve, then merge:
`Name==""` → return values as-is; `Object` → prefix `name_`; else `merged[name]=values["value"]`.

## File-like secrets

Files are returned as ordinary map keys and materialized by the job with a
`template` block — the plugin never writes files itself (it's a short-lived
per-fetch process; side-effecting the FS would break Nomad's rendering/redaction).

Returned keys for a file reference:

| Key | Meaning |
|---|---|
| `value` | content as text (valid UTF-8 only) |
| `value_base64` | base64 of raw bytes — always safe, required for binary |
| `filename` | original filename metadata |

Job pattern (materialize into tmpfs `secrets/`, never `local/` or env for large blobs):

```hcl
secret "cert" { provider = "remote-secrets"  path = "aws-sm:prod/tls/bundle" }
template {
  data        = "{{ \"${secret.cert.value_base64}\" | base64Decode }}"
  destination = "secrets/bundle.p12"
  perms       = "0400"
}
```

Reference syntax:
- 1Password document item: `op://Vault/MyDocument` (category Document → content).
- 1Password file field: `op://Vault/Item/attachmentField` (field type FILE).
- Force semantics: `?attribute=file`, `?encoding=base64`; binary auto-detected.
- Secrets Manager: auto-detect `SecretBinary`; `?binary` to force.

Guardrail: `SECRET_MAX_FILE_BYTES` — error (not truncate) above the limit;
env-var interpolation has an OS size ceiling and base64 inflates ~33%.

## Phases (each its own PR)

1. **Core abstraction** — Provider/Registry/scheme router; move 1Password behind
   it; generalize plugin/cache/config; rename binary `onepassword`→`secrets`
   (keep `onepassword` alias). All existing tests stay green.
2. **AWS Parameter Store** provider + unit tests + localstack e2e.
3. **AWS Secrets Manager** provider + unit tests + localstack e2e (JSON cases).
4. **File-like secrets** — attribute/encoding syntax, per-provider content fetch
   (1P documents + file fields, SM binary), size guardrail, `template` docs, UI-redaction coverage.
5. **Build/release/docs** — Makefile + release.yml build `secrets`; ansible/packer
   install with `onepassword` alias; README rewrite; extend Playwright UI test
   to AWS-sourced secrets.

## Testing

- Table-driven scheme-router + generic reference splitter tests.
- Per-provider unit tests (1P existing; AWS via mocked SDK / `AWS_ENDPOINT_URL`).
- Hermetic AWS e2e via **localstack** (mirrors the `fakeconnect` pattern), in `ci.yml`.
- Playwright UI suite extended so AWS secrets are also asserted never-rendered.

## Open notes

- `aws-sdk-go-v2` adds real deps (stays CGO-free / static binary).
- Confirm the target Nomad version interpolates `${secret...}` into `template.data`
  (the file pattern relies on it); validate early in Phase 4.
</content>
