# Cluster stack (Terraform)

Provisions a complete Nomad cluster environment in AWS from one tfvars file:
server + node-pool ASGs on the golden AMI, an ingress NLB with
`nomad.<app>.<env>.octave.nz` / `traefik.<app>.<env>.octave.nz` / wildcard
DNS in **Cloudflare**, ECR repositories, and an optional S3 + CloudFront
static site. Secrets flow through 1Password + SSM — nothing sensitive lands
in user data or Terraform state.

## The order of operations (and why there's no chicken-and-egg)

```
1. packer build              → golden AMI (once per image version)
2. scripts/op-bootstrap.sh   → 1Password vault + service account → token in SSM
   + op item create cloudflare-dns-token → ACME DNS-01 credential in the vault
3. terraform apply           → cluster up; Nomad UI + Traefik live immediately
   (CLOUDFLARE_API_TOKEN in the environment for the provider)
4. ansible cluster-acl.yml   → ACL bootstrap + policies + Traefik token
5. op item create            → token into the vault; ingress nodes
                               self-install it within ~1 minute
```

The apparent circularity — "Traefik hosts the Nomad UI, but Traefik's token
comes from a cluster that's already up" — dissolves because the two Traefik
concerns need different credentials:

- **Hosting the Nomad UI needs no Nomad token.** The route
  `nomad.<app>.<env>` → `http://127.0.0.1:4646` is plain reverse proxying,
  configured as a static file-provider route at boot. The *browser*
  authenticates to Nomad (ACL token sign-in / OIDC); Traefik just forwards.
  Same for the Traefik dashboard (basic auth). Both work from first boot,
  which is exactly what you need to run the ACL bootstrap against
  `https://nomad.<app>.<env>`.
- **Service discovery needs a token, and self-heals.** Ingress nodes run a
  systemd timer that polls 1Password (via the secrets plugin binary,
  using the same service-account token every node already has) for the
  `nomad-traefik-token` item. The moment step 5 stores it, nodes install it
  and restart Traefik — discovery on, timer disabled. Nodes launched later
  get it on first tick; no instance refresh, no re-apply.

## Walkthrough

```sh
# 2. One-time per cluster: vault, service account, token → SSM,
#    plus the ACME DNS-01 credential
./scripts/op-bootstrap.sh smspit prod
op item create --category password --vault smspit-prod \
  --title cloudflare-dns-token password='<cf token with Zone:DNS:Edit>'

# 3. Infrastructure
cp terraform.tfvars.example terraform.tfvars   # edit
export CLOUDFLARE_API_TOKEN='<token for the terraform provider>'
terraform init && terraform apply

# 4-5. ACLs (see the `next_step` output)
cd ../ansible
ansible-playbook cluster-acl.yml -e nomad_addr=https://nomad.smspit.prod.octave.nz
op item create --category password --vault smspit-prod \
  --title nomad-traefik-token password='<minted SecretID>'
```

Then deploy workloads: jobs in the `general` pool reference secrets with
`op://smspit-prod/...` blocks, register services tagged
`traefik.enable=true` + a Host rule on `*.smspit.prod.octave.nz`, and Traefik
routes them with automatic ACME certificates.

## What goes where (secrets)

| Secret | Home | How it moves |
|---|---|---|
| op service-account token | SSM SecureString | instance profile reads it at boot; never in user data/state |
| Nomad management token | 1Password (manual store at bootstrap) | operators only |
| Traefik's Nomad token | 1Password item `nomad-traefik-token` | ingress nodes poll + self-install |
| Cloudflare DNS token (ACME) | 1Password item `cloudflare-dns-token` | ingress nodes fetch at boot |
| App secrets | 1Password vault `<app>-<env>` | `secret {}` blocks via the plugin |

The service account is scoped **read-only to the one cluster vault**, so a
compromised node can read that vault and nothing else.

## Versioned upgrades

`ami_name_filter` pins the image line (e.g. `nomad-client-1.0.*-amd64-*`).
Build a new AMI version → `terraform apply` picks it up (data source) and
bumps launch templates → ASG `instance_refresh` rolls nodes: servers one at
a time (quorum-safe), clients at 50% minimum healthy. Roll back by pinning
the previous version in the filter.

## Notes

- **State backend**: configure your own (S3 + DynamoDB) — deliberately not
  hardcoded here.
- **Sub-org accounts**: run this stack in each workload account; the AMI is
  shared org-wide from the images account (`ami_owner_account_id`).
- **Cloudflare records are DNS-only (grey cloud) by default.** Universal
  SSL covers just one subdomain level, so proxying
  `nomad.<app>.<env>.octave.nz` through Cloudflare would serve an invalid
  edge certificate unless the zone has Advanced Certificate Manager /
  Total TLS. With DNS-only records Traefik terminates TLS itself via ACME.
  If you do enable `cloudflare_proxied` (with ACM), keep the ACME challenge
  on `dns` — HTTP-01 doesn't work behind the proxy.
- **ACME**: DNS-01 via Cloudflare is the default (`traefik_acme_challenge`)
  — it works proxied or internal, and needs no inbound port 80; it requires
  the `cloudflare-dns-token` vault item at instance boot. `http` challenge
  remains available for zones where a DNS credential is undesirable.
- **UI access**: `nomad.<app>.<env>` is public-by-DNS but deny-by-default —
  ACLs gate everything. Tighten `ingress_allowed_cidrs` to office/VPN
  ranges for network-level restriction too (with proxied records, use
  Cloudflare Access/WAF rules instead, since origin traffic then comes from
  Cloudflare IPs).
- **ECR**: repositories are created as `<app>-<env>/<name>`; Nomad's docker
  driver authenticates via the instance role (`ecr:GetAuthorizationToken`) —
  use `docker.config { auth { helper = "ecr-login" } }` or task-level
  `registry_credentials` if you prefer explicit auth.
