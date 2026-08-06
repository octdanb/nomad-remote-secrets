# Nomad client AMI (Packer)

Builds a versioned, org-shareable AMI in **ap-southeast-2** on **Ubuntu
24.04** containing:

| Component | State in image |
|---|---|
| Nomad (pinned, ≥ 1.11) | installed, **disabled** until configured |
| Docker Engine | installed, enabled |
| `onepassword` secret provider plugin | installed at `/opt/nomad/plugins/secrets/onepassword` |
| Traefik (pinned) | installed, **disabled** — opt-in per instance |

The image contains **no credentials and no environment-specific settings**.
Node pool, datacenter, server join, the 1Password service account token, and
Traefik enablement are all injected at instance launch through cloud-init
user data ([examples/user-data.sh.tftpl](examples/user-data.sh.tftpl)). One
image serves every cluster, environment, and node pool.

## Prerequisites

- Packer ≥ 1.10, AWS credentials for the account that owns your golden
  images (typically a dedicated images/shared-services account in the org,
  or the management account).
- IAM permissions for Packer's EC2 build lifecycle plus
  `ec2:ModifyImageAttribute` (for org sharing).
- The plugin binary: run `make release` at the repository root first — the
  template uploads `bin/onepassword_linux_<arch>`.

## Build

```sh
make release          # repo root: builds bin/onepassword_linux_{amd64,arm64}
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

## Injecting 1Password credentials (and other settings)

User data writes, at first boot:

- `/etc/nomad-secret-onepassword/token` + `config.env` — the vault-scoped
  service account token (or `OP_CONNECT_HOST`/token for a Connect backend).
  The host config file also locks backend settings against override from
  job-submitted `env {}` blocks.
- `/etc/nomad.d/runtime.hcl` — datacenter, region, node pool/class,
  `retry_join` (cloud auto-join by EC2 tag; needs `ec2:DescribeInstances`
  on the instance profile), plus anything passed in `nomad_extra_hcl`.
- optionally `/etc/traefik/traefik.env`.

It then runs `onepassword check` (result lands in `/var/log/user-data.log`
and the instance console log) and starts services. Nomad and Traefik stay
disabled unless user data configures them, so an instance launched without
user data joins nothing and leaks nothing.

Feed the token into Terraform from 1Password itself (Terraform `onepassword`
provider), SSM, or Secrets Manager — never from source control.

## Debugging a node

```sh
onepassword check                          # config, backend, connectivity, vault scope
cat /var/log/user-data.log                 # first-boot configuration output
journalctl -u nomad -u traefik --since -1h
```
