#!/usr/bin/env bash
# Hermetic AWS end-to-end test: a real Nomad agent fingerprints the plugin,
# runs a batch job whose secret blocks resolve through AWS Parameter Store,
# and the values land in the task environment. No AWS account is needed —
# localstack stands in for SSM (mirrors the fake-1Password harness).
#
#   NOMAD_BIN=<path>   nomad binary (default: nomad on PATH)
set -euo pipefail
cd "$(dirname "$0")/../.."

NOMAD_BIN=${NOMAD_BIN:-nomad}
PLUGIN_DIR=/opt/nomad-e2e/plugins
LOCALSTACK_ENDPOINT=http://127.0.0.1:4566
SUDO=$(command -v sudo || true)

NOMAD_PID=""
LOCALSTACK_ID=""
cleanup() {
  [ -n "$NOMAD_PID" ] && $SUDO kill "$NOMAD_PID" 2>/dev/null || true
  [ -n "$LOCALSTACK_ID" ] && docker rm -f "$LOCALSTACK_ID" >/dev/null 2>&1 || true
}
trap cleanup EXIT

fail() {
  echo "AWS E2E FAIL: $*" >&2
  echo "--- nomad agent log (tail) ---" >&2
  tail -40 /tmp/nomad-e2e-aws-agent.log >&2 || true
  exit 1
}

# awscli-against-localstack with dummy creds; awslocal is used when present.
aws_ssm() {
  if command -v awslocal >/dev/null 2>&1; then
    awslocal ssm "$@"
  else
    AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_DEFAULT_REGION=us-east-1 \
      aws --endpoint-url "$LOCALSTACK_ENDPOINT" ssm "$@"
  fi
}

aws_sm() {
  if command -v awslocal >/dev/null 2>&1; then
    awslocal secretsmanager "$@"
  else
    AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_DEFAULT_REGION=us-east-1 \
      aws --endpoint-url "$LOCALSTACK_ENDPOINT" secretsmanager "$@"
  fi
}

echo "==> building and installing plugin"
make build
$SUDO install -d "$PLUGIN_DIR/secrets"
$SUDO install -m 0755 bin/remote-secrets "$PLUGIN_DIR/secrets/remote-secrets"

echo "==> starting localstack (SSM + Secrets Manager)"
# Pin to the v3 community image: SSM and Secrets Manager are free/community
# emulations, but the rolling `latest` tag is now Pro-gated and exits without a
# LOCALSTACK_AUTH_TOKEN. Override with LOCALSTACK_IMAGE if needed.
LOCALSTACK_IMAGE=${LOCALSTACK_IMAGE:-localstack/localstack:3}
LOCALSTACK_ID=$(docker run -d -p 4566:4566 -e SERVICES=ssm,secretsmanager "$LOCALSTACK_IMAGE")
ready() { curl -sf "$LOCALSTACK_ENDPOINT/_localstack/health" | grep -q "\"$1\""; }
for _ in $(seq 1 60); do
  ready ssm && ready secretsmanager && break
  sleep 1
done
ready ssm || fail "localstack SSM never became available"
ready secretsmanager || fail "localstack Secrets Manager never became available"

echo "==> seeding parameters"
aws_ssm put-parameter --name /prod/db/password --type SecureString --value 'hunter2-aws' --overwrite
aws_ssm put-parameter --name /prod/db/creds --type String \
  --value '{"username":"app-user","password":"json-pass-aws"}' --overwrite

echo "==> seeding secrets manager secrets"
aws_sm create-secret --name prod/sm/plain --secret-string 'sm-plain-aws' \
  || aws_sm put-secret-value --secret-id prod/sm/plain --secret-string 'sm-plain-aws'
aws_sm create-secret --name prod/sm/creds \
  --secret-string '{"username":"sm-user","password":"sm-json-pass"}' \
  || aws_sm put-secret-value --secret-id prod/sm/creds \
    --secret-string '{"username":"sm-user","password":"sm-json-pass"}'

echo "==> configuring AWS backend (localstack + dummy creds)"
$SUDO install -d -m 0700 /etc/remote-secrets
$SUDO tee /etc/remote-secrets/config.env >/dev/null <<EOF
AWS_REGION=us-east-1
AWS_ENDPOINT_URL=$LOCALSTACK_ENDPOINT
AWS_ACCESS_KEY_ID=test
AWS_SECRET_ACCESS_KEY=test
OP_CACHE_TTL=0
OP_CACHE_MAX_STALE=0
EOF

echo "==> plugin self-check"
$SUDO env AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test \
  "$PLUGIN_DIR/secrets/remote-secrets" check || fail "plugin check failed"

echo "==> starting nomad dev agent ($($NOMAD_BIN version | head -1))"
$SUDO env AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test \
  "$NOMAD_BIN" agent -dev -config e2e/agent.hcl >/tmp/nomad-e2e-aws-agent.log 2>&1 &
NOMAD_PID=$!
export NOMAD_ADDR=http://127.0.0.1:4646
for _ in $(seq 1 60); do
  "$NOMAD_BIN" node status 2>/dev/null | grep -q ready && break
  sleep 1
done
"$NOMAD_BIN" node status | grep -q ready || fail "nomad client never became ready"

echo "==> running aws e2e job"
"$NOMAD_BIN" job run -detach e2e/aws/job.nomad.hcl || fail "job submission rejected"

alloc=""
for _ in $(seq 1 60); do
  alloc=$("$NOMAD_BIN" job allocs -json e2e-aws-secrets 2>/dev/null | jq -r '.[0].ID // empty')
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
for want in "PW=hunter2-aws" "USER=app-user" "CPW=json-pass-aws" \
            "SMPW=sm-plain-aws" "SMUSER=sm-user" "SMCPW=sm-json-pass"; do
  case "$logs" in
    *"$want"*) echo "OK   found $want" ;;
    *) fail "missing '$want' in task output" ;;
  esac
done

echo "AWS E2E PASS"
