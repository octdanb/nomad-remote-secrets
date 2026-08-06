terraform {
  required_version = ">= 1.6"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.60"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = ">= 5.0"
    }
  }
}

provider "aws" {
  region = var.region
}

# Authenticates via the CLOUDFLARE_API_TOKEN environment variable — a token
# scoped to DNS:Edit on the zone is enough.
provider "cloudflare" {}

locals {
  name       = "${var.app_name}-${var.environment}"
  fqdn_base  = "${var.app_name}.${var.environment}.${var.dns_zone_name}" # nomad.<this>, traefik.<this>
  op_vault   = var.op_vault_name != "" ? var.op_vault_name : local.name
  op_ssm_arn = var.op_token_ssm_parameter != "" ? var.op_token_ssm_parameter : "/nomad/${var.app_name}/${var.environment}/op-service-account-token"

  # The 1Password item the ACL playbook stores the Traefik token in, and
  # that ingress nodes poll for (see templates/user-data-client.sh.tftpl).
  traefik_token_ref = "op://${local.op_vault}/nomad-traefik-token/password"

  # Cloudflare API token for Traefik's ACME DNS-01 challenge; seed this
  # item into the vault before terraform apply when traefik_acme_challenge
  # is "dns".
  cloudflare_token_ref = "op://${local.op_vault}/cloudflare-dns-token/password"

  server_join_tag = { key = "NomadRole", value = "server-${local.name}" }

  common_tags = {
    Application = var.app_name
    Environment = var.environment
    ManagedBy   = "terraform"
  }
}

data "aws_ami" "nomad_client" {
  owners      = [var.ami_owner_account_id]
  most_recent = true

  filter {
    name   = "name"
    values = [var.ami_name_filter]
  }
}

data "aws_ssm_parameter" "op_token_meta" {
  # Existence check only — the value is fetched by instances at boot via
  # their instance profile, never rendered into user data or state.
  name            = local.op_ssm_arn
  with_decryption = false
}
