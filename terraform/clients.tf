# One ASG per Nomad node pool. Pools flagged `ingress = true` run Traefik,
# live in the public subnets, and are registered with the NLB.
resource "aws_launch_template" "pool" {
  for_each = var.node_pools

  name_prefix   = "${local.name}-nomad-${each.key}-"
  image_id      = data.aws_ami.nomad_client.id
  instance_type = each.value.instance_type

  iam_instance_profile {
    name = aws_iam_instance_profile.node.name
  }

  vpc_security_group_ids = [aws_security_group.nomad.id]

  metadata_options {
    http_tokens = "required"
    # Docker workloads live one network hop from IMDS.
    http_put_response_hop_limit = 2
  }

  user_data = base64encode(templatefile("${path.module}/templates/user-data-client.sh.tftpl", {
    datacenter              = var.nomad_datacenter
    nomad_region            = var.nomad_region
    node_pool               = each.key
    node_class              = each.value.node_class
    server_join_tag_key     = local.server_join_tag.key
    server_join_tag_value   = local.server_join_tag.value
    aws_region              = var.region
    op_token_ssm_parameter  = local.op_ssm_arn
    enable_traefik          = each.value.ingress
    fqdn_base               = local.fqdn_base
    traefik_acme_email      = var.traefik_acme_email
    traefik_acme_challenge  = var.traefik_acme_challenge
    traefik_dashboard_users = var.traefik_dashboard_users
    traefik_token_ref       = local.traefik_token_ref
    cloudflare_token_ref    = local.cloudflare_token_ref
  }))

  tag_specifications {
    resource_type = "instance"
    tags = merge(local.common_tags, {
      Name          = "${local.name}-nomad-${each.key}"
      NomadNodePool = each.key
    })
  }
}

resource "aws_autoscaling_group" "pool" {
  for_each = var.node_pools

  name                = "${local.name}-nomad-${each.key}"
  min_size            = each.value.min_size
  max_size            = each.value.max_size
  desired_capacity    = each.value.desired
  vpc_zone_identifier = each.value.ingress ? var.public_subnet_ids : var.private_subnet_ids
  target_group_arns   = each.value.ingress ? [aws_lb_target_group.http.arn, aws_lb_target_group.https.arn] : []

  launch_template {
    id      = aws_launch_template.pool[each.key].id
    version = "$Latest"
  }

  instance_refresh {
    strategy = "Rolling"
    preferences {
      min_healthy_percentage = 50
    }
  }

  tag {
    key                 = "Name"
    value               = "${local.name}-nomad-${each.key}"
    propagate_at_launch = true
  }
}
