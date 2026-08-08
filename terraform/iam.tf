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

  # remote-secrets plugin runtime access for aws-ssm: / aws-sm: references.
  # Each block appears only when its var list is non-empty.
  dynamic "statement" {
    for_each = length(var.remote_secrets_ssm_parameter_arns) > 0 ? [1] : []
    content {
      sid       = "RemoteSecretsSSM"
      actions   = ["ssm:GetParameter", "ssm:GetParameters"]
      resources = var.remote_secrets_ssm_parameter_arns
    }
  }

  dynamic "statement" {
    for_each = length(var.remote_secrets_sm_secret_arns) > 0 ? [1] : []
    content {
      sid       = "RemoteSecretsSecretsManager"
      actions   = ["secretsmanager:GetSecretValue"]
      resources = var.remote_secrets_sm_secret_arns
    }
  }

  dynamic "statement" {
    for_each = length(var.remote_secrets_kms_key_arns) > 0 ? [1] : []
    content {
      sid       = "RemoteSecretsKMSDecrypt"
      actions   = ["kms:Decrypt"]
      resources = var.remote_secrets_kms_key_arns
    }
  }

  # `remote-secrets check` lists parameters/secrets to verify connectivity;
  # these list actions can't be resource-scoped. Granted only when a backend
  # is in use.
  dynamic "statement" {
    for_each = (length(var.remote_secrets_ssm_parameter_arns) > 0 || length(var.remote_secrets_sm_secret_arns) > 0) ? [1] : []
    content {
      sid       = "RemoteSecretsCheckDiagnostic"
      actions   = ["ssm:DescribeParameters", "secretsmanager:ListSecrets"]
      resources = ["*"]
    }
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
