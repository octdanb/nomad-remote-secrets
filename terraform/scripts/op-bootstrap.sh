#!/usr/bin/env bash
# One-time 1Password + SSM bootstrap for a cluster. Creates the per-cluster
# vault and a read-only service account scoped to it, then stores the
# service-account token in SSM SecureString where instances (via their
# instance profile) fetch it at boot.
#
# Requirements:
#   - op CLI signed in as a 1Password owner/admin (vault + service account
#     creation rights)
#   - aws CLI with credentials for the target account/region
#
# Usage: ./op-bootstrap.sh <app_name> <environment> [region]
set -euo pipefail

APP=${1:?usage: op-bootstrap.sh <app_name> <environment> [region]}
ENVIRONMENT=${2:?usage: op-bootstrap.sh <app_name> <environment> [region]}
REGION=${3:-ap-southeast-2}

VAULT="$APP-$ENVIRONMENT"
SA_NAME="nomad-$VAULT"
PARAM="/nomad/$APP/$ENVIRONMENT/op-service-account-token"

if op vault get "$VAULT" --format json >/dev/null 2>&1; then
  echo "vault $VAULT already exists"
else
  op vault create "$VAULT" >/dev/null
  echo "created vault $VAULT"
fi

# The token is printed exactly once at creation. Cluster nodes only ever
# read items, so scope the grant accordingly.
echo "creating service account $SA_NAME (scoped read-only to $VAULT)..."
TOKEN=$(op service-account create "$SA_NAME" \
  --vault "$VAULT:read_items" --format json | jq -r '.token // empty')

if [ -z "$TOKEN" ]; then
  cat >&2 <<EOF
Could not extract the token from 'op service-account create' output (the
account may already exist — service accounts cannot be re-issued a token;
rotate it in the 1Password web UI instead). Store the token manually:

  aws ssm put-parameter --region $REGION --name $PARAM \\
    --type SecureString --overwrite --value '<ops_token>'
EOF
  exit 1
fi

aws ssm put-parameter --region "$REGION" --name "$PARAM" \
  --type SecureString --overwrite --value "$TOKEN" >/dev/null

echo "stored token in SSM: $PARAM"
echo
echo "Next: terraform apply, then run ansible/cluster-acl.yml and store the"
echo "minted Traefik token in this vault as item 'nomad-traefik-token'"
echo "(password field) so ingress nodes pick it up automatically."
