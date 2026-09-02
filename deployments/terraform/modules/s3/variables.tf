variable "name_prefix" {
  description = "Prefix for every resource name, e.g. \"imageforge-dev\"."
  type        = string
}

variable "bucket_suffix" {
  description = <<-EOT
    Suffix appended to each bucket name to make it globally unique. S3 bucket
    names are shared across every AWS account, so a fixed name will collide with
    someone else's sooner or later. The dev environment feeds the account id in.
  EOT
  type        = string
}

variable "raw_expiration_days" {
  description = <<-EOT
    Days after which an uploaded original is deleted. Originals are inputs that
    a result can always be re-derived from, so keeping them forever is paying to
    store something reproducible.
  EOT
  type        = number
  default     = 30

  validation {
    condition     = var.raw_expiration_days > 0
    error_message = "raw_expiration_days must be greater than zero."
  }
}

variable "processed_expiration_days" {
  description = <<-EOT
    Days after which a transformed result is deleted. Results are cache-like:
    losing one costs a re-run, not data.
  EOT
  type        = number
  default     = 90

  validation {
    condition     = var.processed_expiration_days > 0
    error_message = "processed_expiration_days must be greater than zero."
  }
}

variable "processed_ia_transition_days" {
  description = <<-EOT
    Days after which a result moves to Infrequent Access. Set to 0 to disable.
    Below 30 days S3 charges the 30-day minimum anyway, so anything less is
    worse than useless.
  EOT
  type        = number
  default     = 0

  validation {
    condition     = var.processed_ia_transition_days == 0 || var.processed_ia_transition_days >= 30
    error_message = "processed_ia_transition_days must be 0 or at least 30, the S3 minimum billing duration."
  }
}

variable "versioning_enabled" {
  description = <<-EOT
    Whether to keep object versions. Off by default: with lifecycle expiry on
    top, versioning quietly multiplies what is stored, and neither bucket holds
    anything that cannot be regenerated.
  EOT
  type        = bool
  default     = false
}

variable "force_destroy" {
  description = <<-EOT
    Whether `terraform destroy` may delete a bucket that still has objects in
    it. True is right for a dev environment and wrong everywhere else.
  EOT
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags applied to every resource in this module."
  type        = map(string)
  default     = {}
}
