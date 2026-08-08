#!/usr/bin/env bash
# Mixed-backend end-to-end test: a real Nomad agent resolves 1Password,
# AWS Parameter Store, and AWS Secrets Manager references in a SINGLE secret
# block. Hermetic — e2e/fakeconnect stands in for 1Password Connect and
# localstack for AWS. Proves the scheme router dispatches per-reference with
# multiple backends configured on one node.
#
#   NOMAD_BIN=<path>   nomad binary (default: nomad on PATH)
set -euo pipefail
cd "$(dirname "$0")/../.."

NOMAD_BIN=${NOMAD_BIN:-nomad}
PLUGIN_DIR=/opt/nomad-e2e/plugins
LOCALSTACK_ENDPOINT=http://127.0.0.1:4566
LOCALSTACK_IMAGE=${LOCALSTACK_IMAGE:-localstack/localstack:3}
SUDO=$(command -v sudo || true)
export NOMAD_ADDR=http://127.0.0.1:4646

CONNECT_PID=""
NOMAD_PID=""
LOCALSTACK_ID=""
cleanup() {
  [ -n "$NOMAD_PID" ] && $SUDO kill "$NOMAD_PID" 2>/dev/null || true
  [ -n "$CONNECT_PID" ] && kill "$CONNECT_PID" 2>/dev/null || true
  [ -n "$LOCALSTACK_ID" ] && docker rm -f "$LOCALSTACK_ID" >/dev/null 2>&1 || true
}
trap cleanup EXIT

fail() {
  echo "MIXED E2E FAIL: $*" >&2
  echo "--- nomad agent log (tail) ---" >&2
  tail -40 /tmp/nomad-e2e-mixed-agent.log >&2 || true
  exit 1
}

aws_ssm() { AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_DEFAULT_REGION=us-east-1 aws --endpoint-url "$LOCALSTACK_ENDPOINT" ssm "$@"; }
aws_sm()  { AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_DEFAULT_REGION=us-east-1 aws --endpoint-url "$LOCALSTACK_ENDPOINT" secretsmanager "$@"; }

echo "==> building and installing plugin"
make build
$SUDO install -d "$PLUGIN_DIR/secrets"
$SUDO install -m 0755 bin/remote-secrets "$PLUGIN_DIR/secrets/remote-secrets"

echo "==> starting fake 1Password Connect"
go run ./e2e/fakeconnect >/tmp/nomad-e2e-mixed-connect.log 2>&1 &
CONNECT_PID=$!
for _ in $(seq 1 30); do
  curl -sf -H "Authorization: Bearer e2e-test-token" http://127.0.0.1:8999/v1/vaults >/dev/null && break
  sleep 0.5
done

echo "==> starting localstack (SSM + Secrets Manager)"
LOCALSTACK_ID=$(docker run -d -p 4566:4566 -e SERVICES=ssm,secretsmanager "$LOCALSTACK_IMAGE")
ready() { curl -sf "$LOCALSTACK_ENDPOINT/_localstack/health" | grep -q "\"$1\""; }
for _ in $(seq 1 60); do ready ssm && ready secretsmanager && break; sleep 1; done
ready ssm || fail "localstack SSM never became available"
ready secretsmanager || fail "localstack Secrets Manager never became available"

echo "==> seeding AWS secrets"
aws_ssm put-parameter --name /prod/db/password --type SecureString --value 'hunter2-aws' --overwrite
aws_sm create-secret --name prod/sm/plain --secret-string 'sm-plain-aws' \
  || aws_sm put-secret-value --secret-id prod/sm/plain --secret-string 'sm-plain-aws'
aws_sm create-secret --name prod/sm/creds --secret-string '{"username":"sm-user","password":"sm-json-pass"}' \
  || aws_sm put-secret-value --secret-id prod/sm/creds --secret-string '{"username":"sm-user","password":"sm-json-pass"}'

echo "==> configuring BOTH backends"
$SUDO install -d -m 0700 /etc/remote-secrets
$SUDO tee /etc/remote-secrets/config.env >/dev/null <<EOF
OP_CONNECT_HOST=http://127.0.0.1:8999
OP_CONNECT_TOKEN=e2e-test-token
AWS_REGION=us-east-1
AWS_ENDPOINT_URL=$LOCALSTACK_ENDPOINT
AWS_ACCESS_KEY_ID=test
AWS_SECRET_ACCESS_KEY=test
OP_CACHE_TTL=0
OP_CACHE_MAX_STALE=0
EOF

echo "==> plugin self-check (both backends)"
$SUDO "$PLUGIN_DIR/secrets/remote-secrets" check || fail "plugin check failed"

echo "==> starting nomad dev agent ($($NOMAD_BIN version | head -1))"
$SUDO "$NOMAD_BIN" agent -dev -config e2e/agent.hcl >/tmp/nomad-e2e-mixed-agent.log 2>&1 &
NOMAD_PID=$!
for _ in $(seq 1 60); do
  "$NOMAD_BIN" node status 2>/dev/null | grep -q ready && break
  sleep 1
done
"$NOMAD_BIN" node status | grep -q ready || fail "nomad client never became ready"

echo "==> running mixed job"
"$NOMAD_BIN" job run -detach e2e/mixed/job.nomad.hcl || fail "job submission rejected"

alloc=""
for _ in $(seq 1 60); do
  alloc=$("$NOMAD_BIN" job allocs -json e2e-mixed-secrets 2>/dev/null | jq -r '.[0].ID // empty')
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
  "$NOMAD_BIN" alloc status "$alloc" >&2 || true
  fail "allocation finished as '$status' (expected complete)"
fi

logs=$("$NOMAD_BIN" alloc logs "$alloc" print)
echo "task output: $logs"
for want in "OP=hunter2-e2e" "SSM=hunter2-aws" "SM=sm-plain-aws" "SMUSER=sm-user"; do
  case "$logs" in
    *"$want"*) echo "OK   found $want" ;;
    *) fail "missing '$want' in task output" ;;
  esac
done

echo "MIXED E2E PASS"
