# Nomad UI secret-rendering test

Playwright tests that drive the Nomad **web UI** and assert the resolved
plugin secret values are never rendered — the console may show the secret
*reference* (`${secret.db.value}`) but must never expose the plaintext.

## Run

```bash
./e2e/ui/run.sh
```

Hermetic — reuses `e2e/fakeconnect` as the 1Password backend, boots a Nomad
dev agent with the `secrets` plugin, deploys `job-ui.nomad.hcl` (a
long-running service so the alloc stays visible in the UI), then runs the
Playwright suite. Needs `nomad` (≥ 1.11) on PATH (or `NOMAD_BIN=...`), Go, jq,
Node, and root/sudo.

## What it checks

- **Job definition page** references the secret plumbing but shows no value.
- **Allocation & task pages** (where env vars surface in the UI) redact every
  resolved secret value from both the rendered text and the raw DOM.
- **HTTP API payloads** the UI consumes (`/v1/job/...`, `/v1/allocation/...`)
  carry the env-var *keys* but never the resolved secret values.

## Config

| Env | Default | Purpose |
|---|---|---|
| `NOMAD_ADDR` | `http://127.0.0.1:4646` | UI/API base URL |
| `SECRET_VALUES` | fakeconnect values | comma-separated plaintext that must be absent |
| `UI_JOB_ID` | `ui-secrets` | job to inspect |
