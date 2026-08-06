# nomad.<app>.<env>.<zone>, traefik.<app>.<env>.<zone>, and a wildcard for
# application services — all pointing at the ingress NLB. Traefik routes by
# Host header from there.
locals {
  dns_names = ["nomad.${local.fqdn_base}", "traefik.${local.fqdn_base}", "*.${local.fqdn_base}"]
}

resource "aws_route53_record" "ingress" {
  for_each = toset(local.dns_names)

  zone_id = data.aws_route53_zone.main.zone_id
  name    = each.value
  type    = "A"

  alias {
    name                   = aws_lb.ingress.dns_name
    zone_id                = aws_lb.ingress.zone_id
    evaluate_target_health = true
  }
}
