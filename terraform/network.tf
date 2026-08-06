# Cluster security group: Nomad ports between members, 80/443 into ingress
# nodes (the NLB is a pass-through TCP balancer, so client CIDRs apply here).
resource "aws_security_group" "nomad" {
  name_prefix = "${local.name}-nomad-"
  vpc_id      = var.vpc_id
  tags        = local.common_tags

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_vpc_security_group_ingress_rule" "nomad_tcp_self" {
  security_group_id            = aws_security_group.nomad.id
  referenced_security_group_id = aws_security_group.nomad.id
  from_port                    = 4646
  to_port                      = 4648
  ip_protocol                  = "tcp"
  description                  = "Nomad HTTP/RPC/Serf within the cluster"
}

resource "aws_vpc_security_group_ingress_rule" "nomad_serf_udp" {
  security_group_id            = aws_security_group.nomad.id
  referenced_security_group_id = aws_security_group.nomad.id
  from_port                    = 4648
  to_port                      = 4648
  ip_protocol                  = "udp"
  description                  = "Nomad Serf gossip (UDP)"
}

# Dynamic ports for service allocations (Nomad default range) between nodes,
# so Traefik can reach workloads on any client.
resource "aws_vpc_security_group_ingress_rule" "alloc_ports" {
  security_group_id            = aws_security_group.nomad.id
  referenced_security_group_id = aws_security_group.nomad.id
  from_port                    = 20000
  to_port                      = 32000
  ip_protocol                  = "tcp"
  description                  = "Nomad dynamic allocation ports"
}

resource "aws_vpc_security_group_ingress_rule" "ingress_http" {
  for_each          = toset(var.ingress_allowed_cidrs)
  security_group_id = aws_security_group.nomad.id
  cidr_ipv4         = each.value
  from_port         = 80
  to_port           = 80
  ip_protocol       = "tcp"
  description       = "Traefik HTTP (via NLB)"
}

resource "aws_vpc_security_group_ingress_rule" "ingress_https" {
  for_each          = toset(var.ingress_allowed_cidrs)
  security_group_id = aws_security_group.nomad.id
  cidr_ipv4         = each.value
  from_port         = 443
  to_port           = 443
  ip_protocol       = "tcp"
  description       = "Traefik HTTPS (via NLB)"
}

resource "aws_vpc_security_group_egress_rule" "all_out" {
  security_group_id = aws_security_group.nomad.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}
