# The two values that name everything: hosts become
# nomad.<app_name>.<environment>.<zone> / traefik.<app_name>.<environment>.<zone>,
# resources are tagged and named <app_name>-<environment>-*.
variable "app_name" {
  type = string
}

variable "environment" {
  type = string
}

variable "region" {
  type    = string
  default = "ap-southeast-2"
}

variable "dns_zone_name" {
  type        = string
  description = "DNS zone the hostnames live under (managed in Cloudflare), e.g. octave.nz."
  default     = "octave.nz"
}

variable "cloudflare_zone_id" {
  type        = string
  description = "Cloudflare zone ID for dns_zone_name (Cloudflare dashboard → zone → API section). The provider itself authenticates via the CLOUDFLARE_API_TOKEN environment variable."
}

variable "cloudflare_proxied" {
  type        = bool
  default     = false
  description = <<-EOT
    Proxy the cluster hostnames through Cloudflare (orange cloud). Leave
    false unless the zone has Advanced Certificate Manager / Total TLS:
    Universal SSL only covers one subdomain level, so proxied
    nomad.<app>.<env>.<zone> would serve an invalid edge certificate.
    DNS-only records let Traefik terminate TLS with its own ACME certs.
  EOT
}

# ------------------------------------------------------------------- Network
variable "vpc_id" {
  type = string
}

variable "private_subnet_ids" {
  type        = list(string)
  description = "Subnets for Nomad servers and worker clients."
}

variable "public_subnet_ids" {
  type        = list(string)
  description = "Subnets for the NLB and ingress (Traefik) nodes."
}

variable "ingress_allowed_cidrs" {
  type        = list(string)
  default     = ["0.0.0.0/0"]
  description = "CIDRs allowed to reach the ingress nodes on 80/443 (NLB preserves client IPs)."
}

# ----------------------------------------------------------------------- AMI
variable "ami_name_filter" {
  type        = string
  default     = "nomad-client-*-amd64-*"
  description = "Name filter for the golden AMI built by packer/. Pin a version by narrowing, e.g. nomad-client-1.0.0-amd64-*."
}

variable "ami_owner_account_id" {
  type        = string
  description = "Account that owns the shared golden AMI (your images account)."
}

# ------------------------------------------------------------------- Cluster
variable "server_count" {
  type    = number
  default = 3
}

variable "server_instance_type" {
  type    = string
  default = "t3.medium"
}

# One ASG per Nomad node pool. The pool marked `ingress = true` runs Traefik
# and is placed in the public subnets behind the NLB.
variable "node_pools" {
  type = map(object({
    instance_type = string
    min_size      = number
    max_size      = number
    desired       = number
    ingress       = optional(bool, false)
    node_class    = optional(string, "default")
  }))
  default = {
    general = { instance_type = "t3.large", min_size = 1, max_size = 4, desired = 2 }
    ingress = { instance_type = "t3.small", min_size = 1, max_size = 2, desired = 1, ingress = true }
  }
}

variable "nomad_datacenter" {
  type    = string
  default = "dc1"
}

variable "nomad_region" {
  type    = string
  default = "apse2"
}

# --------------------------------------------------------------------- Secrets
variable "op_token_ssm_parameter" {
  type        = string
  default     = ""
  description = "SSM SecureString holding the 1Password service-account token (written by scripts/op-bootstrap.sh). Defaults to /nomad/<app>/<env>/op-service-account-token."
}

variable "op_vault_name" {
  type        = string
  default     = ""
  description = "1Password vault for this cluster's secrets. Defaults to <app_name>-<environment>."
}

# --------------------------------------------------------------------- Traefik
variable "traefik_acme_email" {
  type        = string
  description = "Email for Let's Encrypt registration (Traefik ACME)."
}

variable "traefik_acme_challenge" {
  type        = string
  default     = "dns"
  description = <<-EOT
    ACME challenge type. "dns" (recommended with Cloudflare) needs a
    Cloudflare API token with DNS-edit on the zone, stored in the cluster's
    1Password vault as item `cloudflare-dns-token` (password field) BEFORE
    terraform apply; it works with proxied records and internal-only
    clusters. "http" needs the hostnames publicly resolvable and port 80
    reachable, and the records DNS-only.
  EOT

  validation {
    condition     = contains(["dns", "http"], var.traefik_acme_challenge)
    error_message = "The traefik_acme_challenge value must be \"dns\" or \"http\"."
  }
}

variable "traefik_dashboard_users" {
  type        = string
  sensitive   = true
  default     = ""
  description = "htpasswd line(s) for the Traefik dashboard at traefik.<app>.<env>, e.g. from `htpasswd -nB admin`. Empty disables the dashboard route."
}

# ------------------------------------------------------------------------ ECR
variable "ecr_repositories" {
  type        = list(string)
  default     = []
  description = "ECR repository names to create, e.g. [\"app-api\", \"app-worker\"]."
}

# ----------------------------------------------------------------- Static site
variable "static_site_enabled" {
  type    = bool
  default = false
}

variable "static_site_aliases" {
  type        = list(string)
  default     = []
  description = "Optional CloudFront aliases; requires acm_certificate_arn (us-east-1 cert)."
}

variable "acm_certificate_arn" {
  type    = string
  default = ""
}
