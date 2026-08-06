# One instance profile for all Nomad nodes: cloud auto-join, ECR pulls,
# SSM session access (no SSH keys), and read access to the one SSM parameter
# holding the 1Password service-account token.
data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "node" {
  name               = "${local.name}-nomad-node"
  assume_role_policy = data.aws_iam_policy_document.assume.json
  tags               = local.common_tags
}

data "aws_iam_policy_document" "node" {
  statement {
    sid       = "NomadAutoJoin"
    actions   = ["ec2:DescribeInstances"]
    resources = ["*"]
  }

  statement {
    sid = "PullFromECR"
    actions = [
      "ecr:GetAuthorizationToken",
      "ecr:BatchCheckLayerAvailability",
      "ecr:GetDownloadUrlForLayer",
      "ecr:BatchGetImage",
    ]
    resources = ["*"]
  }

  statement {
    sid       = "ReadOpToken"
    actions   = ["ssm:GetParameter"]
    resources = [data.aws_ssm_parameter.op_token_meta.arn]
  }
}

resource "aws_iam_role_policy" "node" {
  name   = "node-permissions"
  role   = aws_iam_role.node.id
  policy = data.aws_iam_policy_document.node.json
}

resource "aws_iam_role_policy_attachment" "ssm" {
  role       = aws_iam_role.node.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_instance_profile" "node" {
  name = "${local.name}-nomad-node"
  role = aws_iam_role.node.name
}
