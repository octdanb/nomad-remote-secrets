# TCP pass-through NLB in front of the ingress (Traefik) pool. TLS
# terminates at Traefik (ACME), which also preserves client IPs for it.
resource "aws_lb" "ingress" {
  name               = "${local.name}-ingress"
  load_balancer_type = "network"
  subnets            = var.public_subnet_ids
  tags               = local.common_tags
}

resource "aws_lb_target_group" "http" {
  name     = "${local.name}-ingress-80"
  port     = 80
  protocol = "TCP"
  vpc_id   = var.vpc_id

  health_check {
    protocol = "TCP"
    port     = "80"
  }
}

resource "aws_lb_target_group" "https" {
  name     = "${local.name}-ingress-443"
  port     = 443
  protocol = "TCP"
  vpc_id   = var.vpc_id

  health_check {
    protocol = "TCP"
    port     = "443"
  }
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.ingress.arn
  port              = 80
  protocol          = "TCP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.http.arn
  }
}

resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.ingress.arn
  port              = 443
  protocol          = "TCP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.https.arn
  }
}
