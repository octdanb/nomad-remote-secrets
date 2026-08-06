resource "aws_ecr_repository" "repo" {
  for_each = toset(var.ecr_repositories)

  name                 = "${local.name}/${each.value}"
  image_tag_mutability = "IMMUTABLE"
  force_delete         = false
  tags                 = local.common_tags

  image_scanning_configuration {
    scan_on_push = true
  }
}

# Keep the last 30 images per repo.
resource "aws_ecr_lifecycle_policy" "repo" {
  for_each   = aws_ecr_repository.repo
  repository = each.value.name

  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Expire untagged and old images"
      selection = {
        tagStatus   = "any"
        countType   = "imageCountMoreThan"
        countNumber = 30
      }
      action = { type = "expire" }
    }]
  })
}
