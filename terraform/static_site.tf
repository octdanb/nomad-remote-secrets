# Optional S3 + CloudFront static site (assets, SPA frontend). Private
# bucket, CloudFront Origin Access Control. For custom domains supply
# static_site_aliases plus a us-east-1 ACM certificate ARN.
resource "aws_s3_bucket" "static" {
  count  = var.static_site_enabled ? 1 : 0
  bucket = "${local.name}-static"
  tags   = local.common_tags
}

resource "aws_s3_bucket_public_access_block" "static" {
  count  = var.static_site_enabled ? 1 : 0
  bucket = aws_s3_bucket.static[0].id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_cloudfront_origin_access_control" "static" {
  count                             = var.static_site_enabled ? 1 : 0
  name                              = "${local.name}-static"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

resource "aws_cloudfront_distribution" "static" {
  count               = var.static_site_enabled ? 1 : 0
  enabled             = true
  default_root_object = "index.html"
  aliases             = var.static_site_aliases
  price_class         = "PriceClass_100"
  tags                = local.common_tags

  origin {
    domain_name              = aws_s3_bucket.static[0].bucket_regional_domain_name
    origin_id                = "s3-static"
    origin_access_control_id = aws_cloudfront_origin_access_control.static[0].id
  }

  default_cache_behavior {
    allowed_methods        = ["GET", "HEAD"]
    cached_methods         = ["GET", "HEAD"]
    target_origin_id       = "s3-static"
    viewer_protocol_policy = "redirect-to-https"
    cache_policy_id        = "658327ea-f89d-4fab-a63d-7e88639e58f6" # AWS managed: CachingOptimized
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    cloudfront_default_certificate = var.acm_certificate_arn == ""
    acm_certificate_arn            = var.acm_certificate_arn != "" ? var.acm_certificate_arn : null
    ssl_support_method             = var.acm_certificate_arn != "" ? "sni-only" : null
  }
}

data "aws_iam_policy_document" "static_bucket" {
  count = var.static_site_enabled ? 1 : 0

  statement {
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.static[0].arn}/*"]

    principals {
      type        = "Service"
      identifiers = ["cloudfront.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudfront_distribution.static[0].arn]
    }
  }
}

resource "aws_s3_bucket_policy" "static" {
  count  = var.static_site_enabled ? 1 : 0
  bucket = aws_s3_bucket.static[0].id
  policy = data.aws_iam_policy_document.static_bucket[0].json
}
