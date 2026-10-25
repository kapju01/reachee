# The demo page lives in a private bucket and is served only through
# CloudFront with origin access control, which gives it HTTPS and keeps the
# bucket closed to the public.

resource "aws_s3_bucket" "demo" {
  bucket = local.demo_bucket
}

resource "aws_s3_bucket_public_access_block" "demo" {
  bucket                  = aws_s3_bucket.demo.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "demo" {
  bucket = aws_s3_bucket.demo.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_object" "index" {
  bucket       = aws_s3_bucket.demo.id
  key          = "index.html"
  source       = "${path.module}/../demo/index.html"
  source_hash  = filemd5("${path.module}/../demo/index.html")
  content_type = "text/html; charset=utf-8"
}

# The key the verifier fetches with --pubkey. It is served from the same
# origin as the page but is not read from our own database, which is the
# point: an auditor checks the chain against what was published.
resource "aws_s3_object" "pubkey" {
  bucket       = aws_s3_bucket.demo.id
  key          = "keys/reachee-ed25519.pub"
  source       = "${path.module}/../demo/keys/reachee-ed25519.pub"
  source_hash  = filemd5("${path.module}/../demo/keys/reachee-ed25519.pub")
  content_type = "text/plain; charset=utf-8"
}

# What the page needs to talk to this deployment and nothing more: the API
# it raises against, and the Heart-Map with every address and number taken
# out. It is derived from the same document attempt loads, so the policy the
# page shows cannot drift from the policy that runs. A judge's own deploy
# writes its own.
locals {
  heartmap_doc = yamldecode(local.heartmap).contact

  demo_config = {
    api = aws_apigatewayv2_api.http.api_endpoint
    policy = {
      contact_id   = local.heartmap_doc.id
      display_name = local.heartmap_doc.display_name
      channels     = sort(keys(local.heartmap_doc.channels))
      ladder       = local.heartmap_doc.failover_ladder
      quiet_hours  = local.heartmap_doc.quiet_hours
      rate_limit   = local.heartmap_doc.rate_limit
    }
    evidence = sort(tolist(fileset("${path.module}/../docs/evidence", "*.json")))
  }
}

resource "aws_s3_object" "config" {
  bucket       = aws_s3_bucket.demo.id
  key          = "config.json"
  content      = jsonencode(local.demo_config)
  content_type = "application/json"
}

# The committed evidence chains, served beside the page so a judge can walk
# them and download exactly what verify/ reads.
resource "aws_s3_object" "evidence" {
  for_each     = fileset("${path.module}/../docs/evidence", "*.json")
  bucket       = aws_s3_bucket.demo.id
  key          = "evidence/${each.value}"
  source       = "${path.module}/../docs/evidence/${each.value}"
  source_hash  = filemd5("${path.module}/../docs/evidence/${each.value}")
  content_type = "application/json"
}

resource "aws_cloudfront_origin_access_control" "demo" {
  name                              = "${local.name}-demo"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

# Caching disabled so a republished page is what the judge sees.
data "aws_cloudfront_cache_policy" "disabled" {
  name = "Managed-CachingDisabled"
}

resource "aws_cloudfront_distribution" "demo" {
  enabled             = true
  comment             = "Reachee demo"
  default_root_object = "index.html"
  price_class         = "PriceClass_100"

  origin {
    origin_id                = "demo"
    domain_name              = aws_s3_bucket.demo.bucket_regional_domain_name
    origin_access_control_id = aws_cloudfront_origin_access_control.demo.id
  }

  default_cache_behavior {
    target_origin_id       = "demo"
    viewer_protocol_policy = "redirect-to-https"
    allowed_methods        = ["GET", "HEAD"]
    cached_methods         = ["GET", "HEAD"]
    cache_policy_id        = data.aws_cloudfront_cache_policy.disabled.id
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    cloudfront_default_certificate = true
  }
}

data "aws_iam_policy_document" "demo_bucket" {
  statement {
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.demo.arn}/*"]
    principals {
      type        = "Service"
      identifiers = ["cloudfront.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudfront_distribution.demo.arn]
    }
  }
}

resource "aws_s3_bucket_policy" "demo" {
  bucket     = aws_s3_bucket.demo.id
  policy     = data.aws_iam_policy_document.demo_bucket.json
  depends_on = [aws_s3_bucket_public_access_block.demo]
}
