#!/usr/bin/env bash
# End-to-end secret test driven entirely through `nomad alloc exec` into a
# running Docker container. A real Nomad agent runs a Docker *service* job that
# resolves every secret type through a hermetic fake 1Password Connect: a
# scalar field, a sectioned field, a whole-item expansion, and four file-like
# secrets (text document, PEM file field, binary keystore file field, JSON
# document). The harness execs in and asserts all three surfaces from inside
# the container:
#   * env values    — scalar/sectioned/whole-item, plus each file's base64,
#                     utf8 value, and filename keys
#   * file contents  — exact bytes (sha256) of each materialized file
#   * file modes     — permissions (stat) of each materialized file
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

# Base64 of the binary keystore — must match e2e/fakeconnect/main.go.
KEYSTORE_B64="3q2+7wD/AAG71g=="

# Per-run, invoker-owned log dir — avoids colliding with root-owned /tmp logs
# left by earlier sudo runs (which would fail the redirect with permission
# denied).
LOGDIR=$(mktemp -d "${TMPDIR:-/tmp}/nomad-files-XXXXXX")
AGENT_LOG="$LOGDIR/agent.log"
CONNECT_LOG="$LOGDIR/connect.log"

CONNECT_PID=""
NOMAD_PID=""
cleanup() {
  [ -n "$NOMAD_PID" ] && $SUDO kill "$NOMAD_PID" 2>/dev/null || true
  [ -n "$CONNECT_PID" ] && kill "$CONNECT_PID" 2>/dev/null || true
  rm -rf "$LOGDIR" 2>/dev/null || true
}
trap cleanup EXIT

fail() {
  echo "FILES E2E FAIL: $*" >&2
  echo "--- nomad agent log (tail: $AGENT_LOG) ---" >&2
  tail -40 "$AGENT_LOG" >&2 || true
  exit 1
}

echo "==> building and installing plugin"
make build
$SUDO install -d "$PLUGIN_DIR/secrets"
$SUDO install -m 0755 bin/remote-secrets "$PLUGIN_DIR/secrets/remote-secrets"

echo "==> starting fake 1Password Connect"
go run ./e2e/fakeconnect >"$CONNECT_LOG" 2>&1 &
CONNECT_PID=$!
for _ in $(seq 1 30); do
  curl -sf -H "Authorization: Bearer e2e-test-token" http://127.0.0.1:8999/v1/vaults >/dev/null && break
  sleep 0.5
done

$SUDO install -d -m 0700 /etc/remote-secrets
$SUDO tee /etc/remote-secrets/config.env >/dev/null <<'EOF'
OP_CONNECT_HOST=http://127.0.0.1:8999
OP_CONNECT_TOKEN=e2e-test-token
OP_CACHE_TTL=0
OP_CACHE_MAX_STALE=0
EOF

echo "==> starting nomad dev agent ($($NOMAD_BIN version | head -1))"
$SUDO "$NOMAD_BIN" agent -dev -config e2e/agent.hcl >"$AGENT_LOG" 2>&1 &
NOMAD_PID=$!
for _ in $(seq 1 60); do
  "$NOMAD_BIN" node status 2>/dev/null | grep -q ready && break
  sleep 1
done
"$NOMAD_BIN" node status | grep -q ready || fail "nomad client never became ready"

echo "==> deploying file-secret service job"
"$NOMAD_BIN" job run -detach e2e/files/job-files.nomad.hcl || fail "job submission rejected"

alloc=""
for _ in $(seq 1 60); do
  alloc=$("$NOMAD_BIN" job allocs -json e2e-files 2>/dev/null | jq -r '.[0].ID // empty')
  [ -n "$alloc" ] && break
  sleep 1
done
[ -n "$alloc" ] || fail "no allocation was placed"

# dump_alloc prints the task's own failure reason — status events plus stdout
# and stderr — so a failed run explains itself instead of just saying "failed".
dump_alloc() {
  echo "--- alloc status ---" >&2
  "$NOMAD_BIN" alloc status "$alloc" >&2 2>&1 || true
  echo "--- task stderr ---" >&2
  "$NOMAD_BIN" alloc logs -task files -stderr "$alloc" >&2 2>&1 || true
  echo "--- task stdout ---" >&2
  "$NOMAD_BIN" alloc logs -task files "$alloc" >&2 2>&1 || true
}

status=""
for _ in $(seq 1 120); do
  status=$("$NOMAD_BIN" alloc status -json "$alloc" | jq -r .ClientStatus)
  case "$status" in
    running) break ;;
    failed|complete) dump_alloc; fail "alloc finished as '$status' before it could be inspected" ;;
  esac
  sleep 1
done
[ "$status" = "running" ] || { dump_alloc; fail "allocation never reached running (last: ${status:-none})"; }

# Give the container's entrypoint a moment to materialize the files.
for _ in $(seq 1 30); do
  "$NOMAD_BIN" alloc exec -task files "$alloc" sh -c 'test -f "$NOMAD_SECRETS_DIR/config.json"' 2>/dev/null && break
  sleep 1
done

# exec_task runs a command inside the running container and returns its stdout.
exec_task() {
  "$NOMAD_BIN" alloc exec -task files "$alloc" sh -c "$1"
}

# check_env asserts an environment variable inside the container holds exactly
# the expected value (for single-line scalar/structured secrets and filenames).
#   $1 env var name   $2 expected value
check_env() {
  local name=$1 want=$2 got
  got=$(exec_task "printf %s \"\$$name\"")
  [ "$got" = "$want" ] || fail "env $name = '$got', want '$want'"
  echo "OK   env $name = '$got' ✓"
}

# check_file asserts an in-container file is accessible, has the expected
# bytes (sha256), and the expected mode.
#   $1 basename  $2 want_sha256  $3 want_mode
check_file() {
  local base=$1 want_sha=$2 want_mode=$3
  local path="\$NOMAD_SECRETS_DIR/$base"

  exec_task "test -r $path" || fail "$base is not readable in the container"

  local got_sha got_mode
  got_sha=$(exec_task "sha256sum $path" | awk '{print $1}')
  [ "$got_sha" = "$want_sha" ] || fail "$base content mismatch: sha256 $got_sha, want $want_sha"

  got_mode=$(exec_task "stat -c '%a' $path")
  [ "$got_mode" = "$want_mode" ] || fail "$base permissions are $got_mode, want $want_mode"

  echo "OK   file $base — content ✓, mode $got_mode ✓"
}

# Expected sha256s are computed from the same source of truth as fakeconnect,
# so a drift in either side fails loudly.
sha() { sha256sum | awk '{print $1}'; }
welcome_sha=$(printf 'e2e-document-content\n' | sha)
cert_sha=$(printf -- '-----BEGIN CERTIFICATE-----\nZTJlLXRscy1jZXJ0Cg==\n-----END CERTIFICATE-----\n' | sha)
config_sha=$(printf '{"db":{"host":"db.internal.test","port":5432}}\n' | sha)
keystore_sha=$(printf %s "$KEYSTORE_B64" | base64 -d | sha)

echo "==> asserting injected env values (scalar, sectioned, whole-item expansion)"
check_env "DB_PASSWORD" "hunter2-e2e"       # single CONCEALED field
check_env "APP_PW"      "hunter2-e2e"       # multi-entry scalar
check_env "APP_REPLICA" "replica-pass-e2e"  # sectioned field
check_env "APP_USER"    "app-user"          # whole-item expansion (username)
check_env "APP_HOST"    "db.internal.test"  # whole-item expansion (host name)

echo "==> asserting file-secret env keys (base64 + filename)"
check_env "WELCOME_NAME" "welcome.txt"
check_env "CERT_NAME"    "server.pem"
check_env "STORE_NAME"   "keystore.p12"
check_env "CONFIG_NAME"  "config.json"
check_env "STORE_B64"    "$KEYSTORE_B64"   # binary: base64 delivered verbatim

echo "==> asserting materialized files (contents + permissions)"
check_file "welcome.txt"   "$welcome_sha"  "400"
check_file "server.pem"    "$cert_sha"     "444"
check_file "keystore.p12"  "$keystore_sha" "400"
check_file "config.json"   "$config_sha"   "440"

echo "FILES E2E PASS"
