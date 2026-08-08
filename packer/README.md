# Nomad client AMI (Packer)

Builds a versioned, org-shareable AMI in **ap-southeast-2** on **Ubuntu
24.04** containing:

| Component | State in image |
|---|---|
| Nomad (pinned, ≥ 1.11) | installed, **disabled** until configured |
| Docker Engine | installed, enabled |
| multi-provider `secrets` plugin | installed at `/opt/nomad/plugins/secrets/secrets` |
| Traefik (pinned) | installed, **disabled** — opt-in per instance |

The image contains **no credentials and no environment-specific settings**.
Node pool, datacenter, server join, the 1Password service account token, and
Traefik enablement are all injected at instance launch through cloud-init
user data ([examples/user-data.sh.tftpl](examples/user-data.sh.tftpl)). One
image serves every cluster, environment, and node pool.

Provisioning is **Ansible** ([../ansible/](../ansible/)) driven by Packer's
ansible provisioner — the same roles (`base`, `docker`, `nomad`,
`onepassword_plugin`, `traefik`) provision bare-metal or long-lived EC2
hosts:

```sh
ansible-playbook -i your-inventory ../ansible/image.yml \
  -e nomad_version=1.11.0 \
  -e onepassword_plugin_binary=/path/to/secrets_linux_amd64 \
  -e nomad_service_enabled=true -e traefik_service_enabled=true
```

(On bare metal you own `/etc/nomad.d/runtime.hcl` and `/etc/traefik/traefik.env`;
on AMI instances first-boot user data writes them.)

## Prerequisites

- Packer ≥ 1.10 and **ansible-playbook** on the build machine.
- AWS credentials for the account that owns your golden images (typically a
  dedicated images/shared-services account in the org, or the management
  account), with IAM permissions for Packer's EC2 build lifecycle plus
  `ec2:ModifyImageAttribute` (for org sharing).
- The plugin binary: run `make release` at the repository root first — the
  build installs `bin/secrets_linux_<arch>`.

## Build

```sh
make release          # repo root: builds bin/secrets_linux_{amd64,arm64}
cd packer
packer init .
packer build \
  -var image_version=1.0.0 \
  -var org_arn=arn:aws:organizations::<MGMT_ACCOUNT_ID>:organization/o-xxxxxxxxxx \
  .
```

Useful variables (see `nomad-client.pkr.hcl` for all):

| Variable | Default | Notes |
|---|---|---|
| `image_version` | — required | Semver; drives AMI name + `Version` tag |
| `arch` | `amd64` | `arm64` builds on t4g and installs the arm64 plugin/Traefik |
| `nomad_version` | `1.11.0` | apt-pinned; must be ≥ 1.11.0 |
| `traefik_version` | `3.5.2` | binary release baked in |
| `org_arn` | `""` | share with the whole AWS organization |
| `ou_arns` | `[]` | or share with specific OUs instead |

## Sharing across the organization

`ami_org_arns` grants **launch permission** to every account in the org (or
use `ou_arns` for a subset). Two caveats:

- **Encryption**: an AMI encrypted with the default AWS-managed EBS key
  cannot be shared. Either leave the boot snapshot unencrypted (the image
  holds no secrets by design) and let consuming accounts use
  *EBS encryption-by-default* to encrypt volumes at launch, or build with a
  customer-managed KMS key whose key policy grants the consuming accounts
  `kms:Decrypt`/`kms:CreateGrant`.
- Sharing grants *launch*, not *copy*; that's usually what you want for a
  fleet-wide golden image.

## Versioning and upgrades

Every build stamps the version into the AMI name
(`nomad-client-<version>-<arch>-<timestamp>`) and a `Version` tag. Consuming
accounts pin or float as they choose:

```hcl
data "aws_ami" "nomad_client" {
  owners      = ["<IMAGES_ACCOUNT_ID>"]
  most_recent = true
  filter {
    name   = "name"
    values = ["nomad-client-1.0.*-amd64-*"] # pin minor, float patch — or pin exactly
  }
}
```

Cluster upgrade flow: build a new version → update the ASG launch template
to the new AMI → `aws autoscaling start-instance-refresh` (or Terraform's
`instance_refresh` block). Nomad clients drain gracefully if you hook
[`nomad node drain`](https://developer.hashicorp.com/nomad/docs/commands/node/drain)
into the instance-refresh lifecycle hook; new nodes join with the same user
data and pick up work. Because workload configuration lives in user data,
rolling back is just pointing the launch template at the previous AMI.

Optionally publish each build to an SSM parameter so sub-accounts can
resolve "current" without name filters:

```sh
aws ssm put-parameter --name /images/nomad-client/current \
  --type String --overwrite \
  --value "$(jq -r '.builds[-1].artifact_id' manifest.json | cut -d: -f2)"
```

(SSM parameters can be shared org-wide via RAM, or duplicated per account in
your image pipeline.)

## Node pools

The user-data template sets `client { node_pool = "..." }`. Node pools are
created automatically when a client registers with a new pool name, so an
ASG per pool (`general`, `ingress`, `batch`, ...) with different user data —
same AMI — is all that's needed. Jobs then target pools with
`node_pool = "ingress"` in the job spec.

## Traefik

Traefik is baked but disabled. An instance becomes an edge node when its
user data sets `enable_traefik = true` — typically a small dedicated ASG in
the `ingress` node pool. The baked static config uses Traefik's **Nomad
provider** against the local agent, so any Nomad service tagged
`traefik.enable=true` is routed automatically; per-instance settings
(ACME resolver, dashboard) go in `/etc/traefik/traefik.env`.

If you'd rather run Traefik as a Nomad system job on the ingress pool
instead of a host service, ignore `enable_traefik` — the binary being
present costs nothing.

## Nomad ACLs

The image enables ACL enforcement on every agent (`acl { enabled = true }`,
baked by the `nomad` role). Everything else about ACLs — the bootstrap,
policies, and tokens — is **cluster state stored by the Nomad servers**, so
it is provisioned against a *live* cluster, not baked into the image:

```sh
cd ansible
# first run on a fresh cluster: bootstraps ACLs, applies policies, mints tokens
ansible-playbook cluster-acl.yml -e nomad_addr=http://nomad.internal:4646

# later runs (idempotent — re-apply policies, create missing tokens)
NOMAD_TOKEN=<management-token> ansible-playbook cluster-acl.yml \
  -e nomad_addr=http://nomad.internal:4646
```

The playbook prints the bootstrap management token and each newly created
token **exactly once** — store them in 1Password immediately (the output
includes a ready `op item create` command). With ACLs enabled, anonymous
requests are denied, so run the bootstrap soon after the first servers come
up.

Out of the box it provisions:

| Object | Purpose |
|---|---|
| policy `traefik` | read-only namespace access — service discovery only |
| policy `ui-readonly` | read-only jobs/allocations/nodes for humans |
| token `traefik-nomad-provider` (client, policy `traefik`) | Traefik's Nomad provider |

Add policies/tokens by extending `nomad_acl_policies` / `nomad_acl_tokens`
in `ansible/roles/nomad_acl/defaults/main.yml`.

### Traefik ↔ Nomad with ACLs

Traefik's Nomad provider authenticates with the `traefik-nomad-provider`
token. Store the minted token in 1Password, then inject it at instance
launch via the user-data `traefik_env` input:

```
TRAEFIK_PROVIDERS_NOMAD_ENDPOINT_TOKEN=<token from 1Password>
```

Traefik's static configuration is environment-variable based
(`/etc/traefik/static.env` baked, `/etc/traefik/traefik.env` injected)
precisely so this token can be supplied at boot — Traefik does not merge
static config from multiple sources, so a baked YAML file couldn't take a
runtime token.

### Nomad UI restricted access

The UI is served by the same HTTP API on port 4646, so restricting it has
two independent layers:

1. **AuthN/AuthZ (ACLs)** — with ACLs enabled, an unauthenticated browser
   sees nothing and the UI shows a token sign-in. Hand humans tokens bound
   to `ui-readonly` (or richer policies per team). For team-scale access,
   configure **SSO instead of shared tokens**: Nomad supports OIDC login in
   the UI —

   ```sh
   nomad acl auth-method create -type=OIDC -name=sso -default-token-ttl=8h \
     -token-locality=global -max-token-ttl=8h -config @oidc-config.json
   nomad acl binding-rule create -auth-method=sso \
     -bind-type=policy -bind-name=ui-readonly -selector='true'
   ```

   with your IdP (Google Workspace, Okta, Entra) in `oidc-config.json`; map
   IdP groups to Nomad policies with more selective binding rules.

2. **Network** — don't expose 4646 publicly. Reach the UI over VPN or an
   internal ALB; security groups should admit 4646 only from the VPC and
   admin sources. If you want the UI on a friendly internal name, route it
   through Traefik as a normal service and keep ACLs as the auth layer.

Client nodes themselves need no token for normal operation (client↔server
RPC is internal); tokens are for the HTTP API — humans, CI, and Traefik.

## Injecting 1Password credentials (and other settings)

User data writes, at first boot:

- `/etc/nomad-secret/token` + `config.env` — the vault-scoped
  service account token (or `OP_CONNECT_HOST`/token for a Connect backend).
  The host config file also locks backend settings against override from
  job-submitted `env {}` blocks.
- `/etc/nomad.d/runtime.hcl` — datacenter, region, node pool/class,
  `retry_join` (cloud auto-join by EC2 tag; needs `ec2:DescribeInstances`
  on the instance profile), plus anything passed in `nomad_extra_hcl`.
- optionally `/etc/traefik/traefik.env`.

It then runs `secrets check` (result lands in `/var/log/user-data.log`
and the instance console log) and starts services. Nomad and Traefik stay
disabled unless user data configures them, so an instance launched without
user data joins nothing and leaks nothing.

Feed the token into Terraform from 1Password itself (Terraform `onepassword`
provider), SSM, or Secrets Manager — never from source control.

## Debugging a node

```sh
secrets check                              # config, backend, connectivity, vault scope
cat /var/log/user-data.log                 # first-boot configuration output
journalctl -u nomad -u traefik --since -1h
```
