#!/usr/bin/env bash
# End-to-end UI test: boot a real Nomad dev agent with the onepassword plugin,
# deploy a long-running service job whose secrets resolve through a fake
# 1Password Connect, then drive the Nomad web UI with Playwright to prove the
# resolved secret values never render in the console.
#
#   NOMAD_BIN=<path>   nomad binary (default: nomad on PATH)
#
# Hermetic: e2e/fakeconnect stands in for 1Password Connect, so no creds.
set -euo pipefail
cd "$(dirname "$0")/../.."

NOMAD_BIN=${NOMAD_BIN:-nomad}
PLUGIN_DIR=/opt/nomad-e2e/plugins
SUDO=$(command -v sudo || true)
export NOMAD_ADDR=${NOMAD_ADDR:-http://127.0.0.1:4646}

CONNECT_PID=""
NOMAD_PID=""
cleanup() {
  [ -n "$NOMAD_PID" ] && $SUDO kill "$NOMAD_PID" 2>/dev/null || true
  [ -n "$CONNECT_PID" ] && kill "$CONNECT_PID" 2>/dev/null || true
}
trap cleanup EXIT

fail() {
  echo "UI E2E FAIL: $*" >&2
  echo "--- nomad agent log (tail) ---" >&2
  tail -40 /tmp/nomad-ui-agent.log >&2 || true
  exit 1
}

echo "==> building and installing plugin"
make build
$SUDO install -d "$PLUGIN_DIR/secrets"
$SUDO install -m 0755 bin/onepassword "$PLUGIN_DIR/secrets/onepassword"

echo "==> starting fake 1Password Connect"
go run ./e2e/fakeconnect >/tmp/nomad-ui-connect.log 2>&1 &
CONNECT_PID=$!
for _ in $(seq 1 30); do
  curl -sf -H "Authorization: Bearer e2e-test-token" http://127.0.0.1:8999/v1/vaults >/dev/null && break
  sleep 0.5
done

$SUDO install -d -m 0700 /etc/nomad-secret-onepassword
$SUDO tee /etc/nomad-secret-onepassword/config.env >/dev/null <<'EOF'
OP_CONNECT_HOST=http://127.0.0.1:8999
OP_CONNECT_TOKEN=e2e-test-token
OP_CACHE_TTL=0
OP_CACHE_MAX_STALE=0
EOF

echo "==> starting nomad dev agent ($($NOMAD_BIN version | head -1))"
$SUDO "$NOMAD_BIN" agent -dev -config e2e/agent.hcl >/tmp/nomad-ui-agent.log 2>&1 &
NOMAD_PID=$!
for _ in $(seq 1 60); do
  "$NOMAD_BIN" node status 2>/dev/null | grep -q ready && break
  sleep 1
done
"$NOMAD_BIN" node status | grep -q ready || fail "nomad client never became ready"

echo "==> deploying UI service job"
"$NOMAD_BIN" job run -detach e2e/ui/job-ui.nomad.hcl || fail "job submission rejected"

for _ in $(seq 1 60); do
  status=$("$NOMAD_BIN" job allocs -json ui-secrets 2>/dev/null | jq -r '.[0].ClientStatus // empty')
  [ "$status" = "running" ] && break
  sleep 1
done
[ "$status" = "running" ] || fail "allocation never reached running (last: ${status:-none})"

echo "==> installing Playwright and running UI tests"
cd e2e/ui
npm install
npx playwright install --with-deps chromium
NOMAD_ADDR="$NOMAD_ADDR" npx playwright test

echo "UI E2E PASS"
