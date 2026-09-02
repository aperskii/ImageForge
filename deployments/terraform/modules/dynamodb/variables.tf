variable "name_prefix" {
  description = "Prefix for every resource name, e.g. \"imageforge-dev\"."
  type        = string
}

variable "hash_key" {
  description = <<-EOT
    The partition key attribute. It must match dynamorepo.KeyAttribute in the
    application; changing one without the other means every read misses.
  EOT
  type        = string
  default     = "id"
}

variable "point_in_time_recovery_enabled" {
  description = <<-EOT
    Whether to keep continuous backups. Off for dev, where the table holds
    nothing that cannot be recreated by re-running a job.
  EOT
  type        = bool
  default     = false
}

variable "ttl_attribute" {
  description = <<-EOT
    Attribute holding a Unix expiry time, in seconds. Set to "" to disable TTL.
    Note that the application stores timestamps in nanoseconds, so this is not
    one of its existing attributes: it needs a dedicated one.
  EOT
  type        = string
  default     = ""
}

variable "deletion_protection_enabled" {
  description = "Whether the table refuses to be deleted. Off for dev, on for prod."
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags applied to every resource in this module."
  type        = map(string)
  default     = {}
}
