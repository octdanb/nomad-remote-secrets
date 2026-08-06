# nomad.<app>.<env>.<zone>, traefik.<app>.<env>.<zone>, and a wildcard for
# application services — all CNAMEs to the ingress NLB in Cloudflare.
# Traefik routes by Host header from there.
#
# proxied defaults to false (grey cloud): Cloudflare Universal SSL covers
# only one subdomain level, so proxying these deep subdomains needs
# Advanced Certificate Manager — see var.cloudflare_proxied.
locals {
  dns_names = ["nomad.${local.fqdn_base}", "traefik.${local.fqdn_base}", "*.${local.fqdn_base}"]
}

resource "cloudflare_dns_record" "ingress" {
  for_each = toset(local.dns_names)

  zone_id = var.cloudflare_zone_id
  name    = each.value
  type    = "CNAME"
  content = aws_lb.ingress.dns_name
  ttl     = 1 # automatic
  proxied = var.cloudflare_proxied
  comment = "Nomad cluster ${local.name} ingress (terraform)"
}
