output "ami" {
  value = {
    id      = data.aws_ami.nomad_client.id
    name    = data.aws_ami.nomad_client.name
    version = try(data.aws_ami.nomad_client.tags["Version"], "unknown")
  }
}

output "nomad_ui_url" {
  value = "https://nomad.${local.fqdn_base}"
}

output "traefik_dashboard_url" {
  # Only the presence of dashboard users is revealed, not their value.
  value = nonsensitive(var.traefik_dashboard_users != "") ? "https://traefik.${local.fqdn_base}" : "disabled"
}

output "ingress_nlb_dns" {
  value = aws_lb.ingress.dns_name
}

output "wildcard_dns" {
  value       = "*.${local.fqdn_base}"
  description = "Point-and-go hostname space for Nomad services routed by Traefik."
}

output "ecr_repository_urls" {
  value = { for name, repo in aws_ecr_repository.repo : name => repo.repository_url }
}

output "static_site_cloudfront_domain" {
  value = var.static_site_enabled ? aws_cloudfront_distribution.static[0].domain_name : null
}

output "op_vault" {
  value = local.op_vault
}

output "op_token_ssm_parameter" {
  value = local.op_ssm_arn
}

output "next_step" {
  value = <<-EOT
    Cluster infrastructure is up. Finish ACLs (one-time per cluster):
      cd ../ansible
      ansible-playbook cluster-acl.yml -e nomad_addr=https://nomad.${local.fqdn_base}
    then store the minted Traefik token in 1Password:
      op item create --category password --vault ${local.op_vault} \
        --title nomad-traefik-token password='<SecretID>'
    Ingress nodes install it automatically within ~1 minute.
  EOT
}
