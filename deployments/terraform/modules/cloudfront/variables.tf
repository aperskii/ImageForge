variable "name_prefix" {
  description = "Prefix for every resource name, e.g. \"imageforge-dev\"."
  type        = string
}

variable "bucket_id" {
  description = "Name of the S3 bucket to serve, used to attach its policy."
  type        = string
}

variable "bucket_arn" {
  description = "ARN of the S3 bucket to serve."
  type        = string
}

variable "bucket_regional_domain_name" {
  description = "Regional domain name of the bucket, used as the origin."
  type        = string
}

variable "price_class" {
  description = <<-EOT
    Which edge locations to serve from. PriceClass_100 is North America and
    Europe only and is the cheapest; the others add regions and cost.
  EOT
  type        = string
  default     = "PriceClass_100"

  validation {
    condition     = contains(["PriceClass_100", "PriceClass_200", "PriceClass_All"], var.price_class)
    error_message = "price_class must be PriceClass_100, PriceClass_200 or PriceClass_All."
  }
}

variable "default_ttl" {
  description = "Seconds to cache an object the origin gave no cache header for."
  type        = number
  default     = 86400
}

variable "max_ttl" {
  description = "Longest an object may be cached, whatever the origin asks for."
  type        = number
  default     = 31536000
}

variable "tags" {
  description = "Tags applied to every resource in this module."
  type        = map(string)
  default     = {}
}

variable "allowed_key_prefix" {
  description = <<-EOT
    Restricts what the distribution may read from the bucket. Empty means the
    whole bucket.

    This matters when the origin bucket holds more than results. The
    application writes originals and results to one bucket today, and an
    original is a private upload: without this, anyone who learned a job id
    could fetch the image somebody uploaded straight from the CDN.
  EOT
  type        = string
  default     = ""
}
