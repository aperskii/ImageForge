# CloudFront in front of the processed-images bucket.
#
# Results are immutable once written -- a job id appears in the key -- so they
# cache well, and serving them from an edge rather than from S3 is both faster
# and cheaper than paying S3 egress per request.

locals {
  origin_id = "${var.name_prefix}-processed-origin"
}

# Origin Access Control, the successor to Origin Access Identity. It signs
# CloudFront's requests to S3 with SigV4, which is what lets the bucket stay
# entirely private while still being served publicly.
resource "aws_cloudfront_origin_access_control" "this" {
  name                              = "${var.name_prefix}-processed-oac"
  description                       = "Signs CloudFront requests to the ImageForge processed bucket"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

resource "aws_cloudfront_distribution" "this" {
  enabled     = true
  comment     = "${var.name_prefix} processed images"
  price_class = var.price_class

  # No index document: this serves images by key, and a request for "/" should
  # be a 403 from S3 rather than something that looks like a site.
  origin {
    domain_name              = var.bucket_regional_domain_name
    origin_id                = local.origin_id
    origin_access_control_id = aws_cloudfront_origin_access_control.this.id
  }

  default_cache_behavior {
    target_origin_id = local.origin_id
    # Results are read-only artefacts; nothing else needs to reach the origin.
    allowed_methods = ["GET", "HEAD"]
    cached_methods  = ["GET", "HEAD"]
    compress        = true

    # Plain HTTP is answered with a redirect rather than the object, so a
    # result URL cannot be fetched in the clear.
    viewer_protocol_policy = "redirect-to-https"

    min_ttl     = 0
    default_ttl = var.default_ttl
    max_ttl     = var.max_ttl

    forwarded_values {
      query_string = false

      cookies {
        # Nothing here varies by cookie, and forwarding them would fragment the
        # cache by visitor for no benefit.
        forward = "none"
      }
    }
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    # The default *.cloudfront.net certificate. A custom domain needs an ACM
    # certificate in us-east-1 and an aliases block.
    cloudfront_default_certificate = true
    minimum_protocol_version       = "TLSv1.2_2021"
  }

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-processed"
  })
}

# The bucket policy lives here rather than in the S3 module because it has to
# name the distribution, and the distribution names the bucket. Putting it on
# this side of that pair is what keeps the dependency acyclic.
data "aws_iam_policy_document" "bucket" {
  statement {
    sid    = "AllowCloudFrontRead"
    effect = "Allow"

    principals {
      type        = "Service"
      identifiers = ["cloudfront.amazonaws.com"]
    }

    actions   = ["s3:GetObject"]
    resources = ["${var.bucket_arn}/${var.allowed_key_prefix}*"]

    # Only this distribution. Without the condition any CloudFront distribution
    # in any account could read the bucket.
    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudfront_distribution.this.arn]
    }
  }
}

resource "aws_s3_bucket_policy" "processed" {
  bucket = var.bucket_id
  policy = data.aws_iam_policy_document.bucket.json
}
