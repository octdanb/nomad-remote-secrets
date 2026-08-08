#!/usr/bin/env bash
# End-to-end test: a real Nomad agent fingerprints the onepassword plugin,
# runs a job whose secret blocks resolve through 1Password, and the secret
# values land in the task environment.
#
#   E2E_DRIVER=docker|raw_exec   task driver               (default docker)
#   E2E_MODE=fake|real           1Password backend         (default fake)
#   NOMAD_BIN=<path>             nomad binary              (default: nomad on PATH)
#
# fake mode is hermetic: e2e/fakeconnect stands in for 1Password Connect.
# real mode needs OP_SERVICE_ACCOUNT_TOKEN plus OP_E2E_SECRET_PATH (an
# op:// reference whose value equals OP_E2E_EXPECTED).
set -euo pipefail
cd "$(dirname "$0")/.."

DRIVER=${E2E_DRIVER:-docker}
MODE=${E2E_MODE:-fake}
NOMAD_BIN=${NOMAD_BIN:-nomad}
PLUGIN_DIR=/opt/nomad-e2e/plugins
SUDO=$(command -v sudo || true)

CONNECT_PID=""
NOMAD_PID=""
cleanup() {
  [ -n "$NOMAD_PID" ] && $SUDO kill "$NOMAD_PID" 2>/dev/null || true
  [ -n "$CONNECT_PID" ] && kill "$CONNECT_PID" 2>/dev/null || true
}
trap cleanup EXIT

fail() {
  echo "E2E FAIL: $*" >&2
  echo "--- nomad agent log (tail) ---" >&2
  tail -40 /tmp/nomad-e2e-agent.log >&2 || true
  exit 1
}

echo "==> building and installing plugin"
make build
$SUDO install -d "$PLUGIN_DIR/secrets"
$SUDO install -m 0755 bin/onepassword "$PLUGIN_DIR/secrets/onepassword"

echo "==> configuring 1Password backend ($MODE)"
$SUDO install -d -m 0700 /etc/nomad-secret-onepassword
if [ "$MODE" = "fake" ]; then
  go run ./e2e/fakeconnect >/tmp/nomad-e2e-connect.log 2>&1 &
  CONNECT_PID=$!
  for _ in $(seq 1 30); do
    curl -sf -H "Authorization: Bearer e2e-test-token" http://127.0.0.1:8999/v1/vaults >/dev/null && break
    sleep 0.5
  done
  # Caching off so every fetch exercises the live path.
  $SUDO tee /etc/nomad-secret-onepassword/config.env >/dev/null <<'EOF'
OP_CONNECT_HOST=http://127.0.0.1:8999
OP_CONNECT_TOKEN=e2e-test-token
OP_CACHE_TTL=0
OP_CACHE_MAX_STALE=0
EOF
  JOB_FILE="e2e/job-${DRIVER//_/}.nomad.hcl"
  [ "$DRIVER" = "raw_exec" ] && JOB_FILE="e2e/job-rawexec.nomad.hcl"
  JOB_ARGS=()
  EXPECTED=(
    "PW=hunter2-e2e"
    "APP_PW=hunter2-e2e"
    "REP=replica-pass-e2e"
    "USER=app-user"
    "HOST=db.internal.test"
    "DOC=e2e-document-content"
  )
else
  : "${OP_SERVICE_ACCOUNT_TOKEN:?real mode needs OP_SERVICE_ACCOUNT_TOKEN}"
  : "${OP_E2E_SECRET_PATH:?real mode needs OP_E2E_SECRET_PATH (op://... reference)}"
  printf '%s' "$OP_SERVICE_ACCOUNT_TOKEN" | $SUDO tee /etc/nomad-secret-onepassword/token >/dev/null
  $SUDO chmod 0600 /etc/nomad-secret-onepassword/token
  $SUDO tee /etc/nomad-secret-onepassword/config.env >/dev/null <<'EOF'
OP_SERVICE_ACCOUNT_TOKEN_FILE=/etc/nomad-secret-onepassword/token
OP_CACHE_TTL=0
OP_CACHE_MAX_STALE=0
EOF
  JOB_FILE="e2e/job-real.nomad.hcl"
  JOB_ARGS=(-var "secret_path=$OP_E2E_SECRET_PATH")
  EXPECTED=("PW=${OP_E2E_EXPECTED:-e2e-expected-value}")
fi

echo "==> plugin self-check"
$SUDO "$PLUGIN_DIR/secrets/onepassword" check || fail "onepassword check failed"

echo "==> starting nomad dev agent ($($NOMAD_BIN version | head -1))"
$SUDO "$NOMAD_BIN" agent -dev -config e2e/agent.hcl >/tmp/nomad-e2e-agent.log 2>&1 &
NOMAD_PID=$!
export NOMAD_ADDR=http://127.0.0.1:4646
for _ in $(seq 1 60); do
  "$NOMAD_BIN" node status 2>/dev/null | grep -q ready && break
  sleep 1
done
"$NOMAD_BIN" node status | grep -q ready || fail "nomad client never became ready"

echo "==> running e2e job ($JOB_FILE)"
"$NOMAD_BIN" job run -detach "${JOB_ARGS[@]}" "$JOB_FILE" || fail "job submission rejected"

alloc=""
for _ in $(seq 1 60); do
  alloc=$("$NOMAD_BIN" job allocs -json e2e-secrets 2>/dev/null | jq -r '.[0].ID // empty')
  [ -n "$alloc" ] && break
  sleep 1
done
[ -n "$alloc" ] || fail "no allocation was placed"

status=""
for _ in $(seq 1 120); do
  status=$("$NOMAD_BIN" alloc status -json "$alloc" | jq -r .ClientStatus)
  case "$status" in complete|failed) break ;; esac
  sleep 1
done

if [ "$status" != "complete" ]; then
  echo "--- alloc status ---" >&2
  "$NOMAD_BIN" alloc status "$alloc" >&2 || true
  fail "allocation finished as '$status' (expected complete)"
fi

logs=$("$NOMAD_BIN" alloc logs "$alloc" print)
echo "task output: $logs"
for want in "${EXPECTED[@]}"; do
  case "$logs" in
    *"$want"*) echo "OK   found $want" ;;
    *) fail "missing '$want' in task output" ;;
  esac
done

echo "E2E PASS ($MODE mode, $DRIVER driver)"
