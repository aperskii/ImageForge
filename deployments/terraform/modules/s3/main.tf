# Two buckets, because the objects have different lifetimes and different
# audiences: originals are private inputs, results are what CloudFront serves.
# Splitting them means the lifecycle rules, the bucket policies and the IAM
# grants can each say something narrower than they could with one bucket.

locals {
  raw_bucket_name       = "${var.name_prefix}-raw-${var.bucket_suffix}"
  processed_bucket_name = "${var.name_prefix}-processed-${var.bucket_suffix}"
}

# ------------------------------------------------------------------- raw ----
resource "aws_s3_bucket" "raw" {
  bucket        = local.raw_bucket_name
  force_destroy = var.force_destroy

  tags = merge(var.tags, {
    Name    = local.raw_bucket_name
    Content = "uploaded-originals"
  })
}

resource "aws_s3_bucket_public_access_block" "raw" {
  bucket = aws_s3_bucket.raw.id

  # Originals are never served to anyone. Nothing should be able to make them
  # public by accident, including a future bucket policy.
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "raw" {
  bucket = aws_s3_bucket.raw.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
    # S3 encrypts with a bucket-level key rather than one per object, which
    # cuts KMS request costs if this is ever moved to a customer key.
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_versioning" "raw" {
  bucket = aws_s3_bucket.raw.id

  versioning_configuration {
    status = var.versioning_enabled ? "Enabled" : "Suspended"
  }
}

resource "aws_s3_bucket_ownership_controls" "raw" {
  bucket = aws_s3_bucket.raw.id

  rule {
    # ACLs are disabled: every object is owned by the bucket owner, and access
    # is decided by policy alone rather than by per-object ACLs nobody audits.
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "raw" {
  bucket = aws_s3_bucket.raw.id

  # The bucket must exist with its versioning setting before rules that mention
  # noncurrent versions make sense.
  depends_on = [aws_s3_bucket_versioning.raw]

  rule {
    id     = "expire-originals"
    status = "Enabled"

    filter {
      prefix = "originals/"
    }

    expiration {
      days = var.raw_expiration_days
    }

    dynamic "noncurrent_version_expiration" {
      for_each = var.versioning_enabled ? [1] : []
      content {
        noncurrent_days = 7
      }
    }
  }

  rule {
    id     = "abort-incomplete-uploads"
    status = "Enabled"

    filter {}

    # A multipart upload that is never completed or aborted is billed for
    # storage forever and is invisible in the object listing.
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}

# ------------------------------------------------------------- processed ----
resource "aws_s3_bucket" "processed" {
  bucket        = local.processed_bucket_name
  force_destroy = var.force_destroy

  tags = merge(var.tags, {
    Name    = local.processed_bucket_name
    Content = "transformed-results"
  })
}

resource "aws_s3_bucket_public_access_block" "processed" {
  bucket = aws_s3_bucket.processed.id

  # Public *access* stays blocked even though CloudFront serves from here: the
  # distribution reaches the bucket with a signed origin identity, not as an
  # anonymous caller, so the bucket itself never needs to be public.
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "processed" {
  bucket = aws_s3_bucket.processed.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_versioning" "processed" {
  bucket = aws_s3_bucket.processed.id

  versioning_configuration {
    status = var.versioning_enabled ? "Enabled" : "Suspended"
  }
}

resource "aws_s3_bucket_ownership_controls" "processed" {
  bucket = aws_s3_bucket.processed.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "processed" {
  bucket     = aws_s3_bucket.processed.id
  depends_on = [aws_s3_bucket_versioning.processed]

  rule {
    id     = "expire-results"
    status = "Enabled"

    filter {
      prefix = "results/"
    }

    dynamic "transition" {
      for_each = var.processed_ia_transition_days > 0 ? [1] : []
      content {
        days          = var.processed_ia_transition_days
        storage_class = "STANDARD_IA"
      }
    }

    expiration {
      days = var.processed_expiration_days
    }

    dynamic "noncurrent_version_expiration" {
      for_each = var.versioning_enabled ? [1] : []
      content {
        noncurrent_days = 7
      }
    }
  }

  rule {
    id     = "abort-incomplete-uploads"
    status = "Enabled"

    filter {}

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}
