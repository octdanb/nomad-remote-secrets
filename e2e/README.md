# End-to-end tests

Proves the full chain with a **real Nomad agent**: plugin fingerprinting,
`secret` block resolution (single field, sectioned field, multi-entry with a
whole item), and interpolation into the task environment.

```
nomad agent -dev ── fingerprint ──> secrets plugin
      │                                   │
  job with secret{} blocks           1Password
      │                        (fake Connect, or real via
  env vars in the task          a service-account token)
      └── task prints env; runner asserts the values
```

## Run locally

```sh
# hermetic (fake 1Password Connect), docker driver:
./e2e/run.sh

# without docker:
E2E_DRIVER=raw_exec ./e2e/run.sh

# against a real vault:
E2E_MODE=real \
  OP_SERVICE_ACCOUNT_TOKEN=ops_... \
  OP_E2E_SECRET_PATH="op://nomad-plugin-e2e/e2e/password" \
  OP_E2E_EXPECTED="e2e-expected-value" \
  ./e2e/run.sh
```

Needs `nomad` (≥ 1.11) on PATH (or `NOMAD_BIN=...`), Go, jq, and root/sudo
(the dev agent's client mode requires it). The runner writes the plugin's
host config to `/etc/nomad-secret/` with caching disabled so
every fetch is live.

## CI

`ci.yml` runs the hermetic variant on every push/PR — the fake Connect
server means no credentials and fork-safe.

The `e2e-real-1password` job runs the same round-trip against an actual
1Password account. To enable it, set up once:

1. In 1Password: create a vault (e.g. `nomad-plugin-e2e`) containing an
   item `e2e` whose `password` field is `e2e-expected-value`, and a service
   account scoped **read-only to that vault**.
2. In the GitHub repo settings:
   - secret `OP_E2E_SERVICE_ACCOUNT_TOKEN` — the `ops_...` token
   - variable `OP_E2E_ENABLED` = `true`
   - variable `OP_E2E_SECRET_PATH` = `op://nomad-plugin-e2e/e2e/password`
   - variable `OP_E2E_EXPECTED` = `e2e-expected-value`

Repo secrets aren't exposed to fork PRs, so the job is skipped there.

## Files

| File | Purpose |
|---|---|
| `run.sh` | orchestrates: build → install plugin → backend config → dev agent → job → assert |
| `fakeconnect/` | in-memory 1Password Connect stand-in (vault `Testing`, item `database`) |
| `agent.hcl` | dev-agent overrides: `common_plugin_dir`, raw_exec enabled |
| `job-docker.nomad.hcl` / `job-rawexec.nomad.hcl` | fake-mode job, three secret shapes |
| `job-real.nomad.hcl` | real-mode job, caller-supplied reference |
