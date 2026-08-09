#!/usr/bin/env bash
# End-to-end UI test: boot a real Nomad dev agent with the remote-secrets plugin,
# deploy a long-running service job whose secrets resolve through a fake
# 1Password Connect, then drive the Nomad web UI with Playwright to prove the
# resolved secret values never render in the console.
#
#   NOMAD_BIN=<path>   nomad binary (default: nomad on PATH)
#
# Watch it in a real browser (run WITHOUT sudo — the script sudo-prompts only
# for the Nomad agent; a root-owned browser has no window in your desktop
# session):
#   ./e2e/ui/run.sh --watch     # headed Chromium, slowed down so you can follow
#   ./e2e/ui/run.sh --ui        # Playwright's interactive UI mode (step/replay)
# Any other args are passed straight through to `playwright test`, e.g.
#   ./e2e/ui/run.sh --headed --debug
# Tune the watch pace with E2E_SLOWMO=<ms>. The Nomad UI URL is printed so you
# can also browse it yourself while the job runs. (If you must run the whole
# script under sudo, the browser steps are re-run as $SUDO_USER so the window
# can still appear.)
#
# Hermetic: e2e/fakeconnect stands in for 1Password Connect, so no creds.
set -euo pipefail

# Collect Playwright passthrough args. --watch is our shorthand for headed +
# slow-mo (a friendlier default than raw --headed).
PW_ARGS=()
WATCH=0
for arg in "$@"; do
  case "$arg" in
    --watch) WATCH=1 ;;
    *) PW_ARGS+=("$arg") ;;
  esac
done

cd "$(dirname "$0")/../.."

NOMAD_BIN=${NOMAD_BIN:-nomad}
PLUGIN_DIR=/opt/nomad-e2e/plugins
SUDO=$(command -v sudo || true)
export NOMAD_ADDR=${NOMAD_ADDR:-http://127.0.0.1:4646}

# Node/Playwright and the browser must run as the real desktop user, never
# root: a root-owned browser has no window in the user's session, and
# root-owned files in the browser cache or logs break the next non-sudo run
# (the permission errors you hit). When invoked under sudo we drop those steps
# back to $SUDO_USER; otherwise they run as-is.
if [ "$(id -u)" = 0 ] && [ -n "${SUDO_USER:-}" ]; then
  REAL_HOME=$(eval echo "~$SUDO_USER")
else
  REAL_HOME="$HOME"
fi
# A dedicated, user-owned browser cache so a previously root-polluted default
# cache (from an earlier `sudo` run) can't block the install with EACCES.
PW_BROWSERS="${PLAYWRIGHT_BROWSERS_PATH:-$REAL_HOME/.cache/ms-playwright}"
if [ "$(id -u)" = 0 ] && [ -n "${SUDO_USER:-}" ]; then
  as_user() { sudo -u "$SUDO_USER" env HOME="$REAL_HOME" PATH="$PATH" PLAYWRIGHT_BROWSERS_PATH="$PW_BROWSERS" "$@"; }
else
  as_user() { env PLAYWRIGHT_BROWSERS_PATH="$PW_BROWSERS" "$@"; }
fi

# Per-run, invoker-owned log dir — avoids colliding with root-owned /tmp logs
# left by earlier sudo runs, which made the redirect fail with permission denied.
LOGDIR=$(mktemp -d "${TMPDIR:-/tmp}/nomad-ui-XXXXXX")
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
  echo "UI E2E FAIL: $*" >&2
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

echo "==> deploying UI service job"
"$NOMAD_BIN" job run -detach e2e/ui/job-ui.nomad.hcl || fail "job submission rejected"

for _ in $(seq 1 60); do
  status=$("$NOMAD_BIN" job allocs -json ui-secrets 2>/dev/null | jq -r '.[0].ClientStatus // empty')
  [ "$status" = "running" ] && break
  sleep 1
done
[ "$status" = "running" ] || fail "allocation never reached running (last: ${status:-none})"

echo "==> installing Playwright and running UI tests"
echo "    Nomad web UI: ${NOMAD_ADDR}/ui/jobs/ui-secrets  (open it to watch the job)"
cd e2e/ui

as_user npm install

if [ "$WATCH" = 1 ]; then
  # Headed run: a real browser window drives the UI, slowed down so it's
  # watchable. Install without --with-deps (that needs root; a desktop already
  # has the libs) and run as the desktop user so the window actually appears.
  if [ "$(id -u)" = 0 ] && [ -z "${SUDO_USER:-}" ]; then
    fail "watch mode needs a desktop user: run './e2e/ui/run.sh --watch' (not as bare root) so the browser can open a window"
  fi
  as_user npx playwright install chromium
  echo "==> running UI tests in a visible browser (close the window or Ctrl-C to stop)"
  as_user E2E_HEADED=1 NOMAD_ADDR="$NOMAD_ADDR" npx playwright test --headed ${PW_ARGS[@]+"${PW_ARGS[@]}"}
else
  as_user npx playwright install --with-deps chromium
  as_user NOMAD_ADDR="$NOMAD_ADDR" npx playwright test ${PW_ARGS[@]+"${PW_ARGS[@]}"}
fi

echo "UI E2E PASS"
