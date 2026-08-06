# Nomad servers: same golden AMI, server-mode runtime config from user data.
resource "aws_launch_template" "server" {
  name_prefix   = "${local.name}-nomad-server-"
  image_id      = data.aws_ami.nomad_client.id
  instance_type = var.server_instance_type

  iam_instance_profile {
    name = aws_iam_instance_profile.node.name
  }

  vpc_security_group_ids = [aws_security_group.nomad.id]

  metadata_options {
    http_tokens = "required"
  }

  user_data = base64encode(templatefile("${path.module}/templates/user-data-server.sh.tftpl", {
    datacenter            = var.nomad_datacenter
    nomad_region          = var.nomad_region
    server_count          = var.server_count
    server_join_tag_key   = local.server_join_tag.key
    server_join_tag_value = local.server_join_tag.value
  }))

  tag_specifications {
    resource_type = "instance"
    tags = merge(local.common_tags, {
      Name                        = "${local.name}-nomad-server"
      (local.server_join_tag.key) = local.server_join_tag.value
    })
  }
}

resource "aws_autoscaling_group" "server" {
  name                = "${local.name}-nomad-server"
  min_size            = var.server_count
  max_size            = var.server_count
  desired_capacity    = var.server_count
  vpc_zone_identifier = var.private_subnet_ids

  launch_template {
    id      = aws_launch_template.server.id
    version = "$Latest"
  }

  # Roll servers one at a time on AMI/user-data changes; quorum survives.
  instance_refresh {
    strategy = "Rolling"
    preferences {
      min_healthy_percentage = 66
    }
  }

  tag {
    key                 = "Name"
    value               = "${local.name}-nomad-server"
    propagate_at_launch = true
  }
}
