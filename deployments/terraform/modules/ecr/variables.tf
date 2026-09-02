variable "name_prefix" {
  description = "Prefix for every repository name, e.g. \"imageforge-dev\"."
  type        = string
}

variable "repositories" {
  description = "Repository short names to create, one per image."
  type        = list(string)
  default     = ["api", "worker"]
}

variable "image_tag_mutability" {
  description = <<-EOT
    Whether a tag may be moved to a different image. IMMUTABLE is the safer
    default: a mutable tag means the image a task definition pins can change
    underneath it, and a rollback stops meaning anything.
  EOT
  type        = string
  default     = "IMMUTABLE"

  validation {
    condition     = contains(["MUTABLE", "IMMUTABLE"], var.image_tag_mutability)
    error_message = "image_tag_mutability must be MUTABLE or IMMUTABLE."
  }
}

variable "untagged_expiry_days" {
  description = <<-EOT
    Days after which an untagged image is deleted. Untagged images are usually
    layers orphaned by a re-push and nothing refers to them.
  EOT
  type        = number
  default     = 7
}

variable "tagged_image_count" {
  description = "How many tagged images to keep per repository, newest first."
  type        = number
  default     = 20
}

variable "force_delete" {
  description = "Whether `terraform destroy` may delete a repository that still holds images."
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags applied to every resource in this module."
  type        = map(string)
  default     = {}
}
