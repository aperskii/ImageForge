variable "name_prefix" {
  description = "Prefix for every role name, e.g. \"imageforge-dev\"."
  type        = string
}

variable "github_owner" {
  description = "GitHub user or organization that owns the repository."
  type        = string
}

variable "github_repository" {
  description = "Repository name, without the owner."
  type        = string
}

variable "push_branches" {
  description = <<-EOT
    Branches whose runs may push images. Only the default branch belongs here:
    a run from any other ref must not be able to write to the registry the
    services actually run from.
  EOT
  type        = list(string)
  default     = ["main", "master"]
}

variable "apply_environments" {
  description = <<-EOT
    GitHub environments whose runs may apply. The apply role trusts the
    environment rather than a branch, which is what makes an approval rule on
    that environment load-bearing: without the approval GitHub never mints a
    token with this subject.
  EOT
  type        = list(string)
  default     = ["dev"]
}

variable "ecr_repository_arns" {
  description = "Repositories the push role may write to."
  type        = list(string)
}

variable "create_oidc_provider" {
  description = <<-EOT
    Whether to create the GitHub OIDC provider. It is account-wide, so an
    account that already has one should set this false and pass its ARN below
    instead of failing on a duplicate.
  EOT
  type        = bool
  default     = true
}

variable "oidc_provider_arn" {
  description = "ARN of an existing GitHub OIDC provider, used when create_oidc_provider is false."
  type        = string
  default     = ""
}

variable "state_bucket_arn" {
  description = <<-EOT
    ARN of the Terraform state bucket, if remote state is in use. Grants the
    plan role the writes a state lock needs; empty means local state.
  EOT
  type        = string
  default     = ""
}

variable "apply_policy_arn" {
  description = <<-EOT
    Policy attached to the apply role.

    Defaults to PowerUserAccess plus nothing else, which cannot create the IAM
    roles this configuration needs; set it to AdministratorAccess for an
    account dedicated to this project, or to a narrower customer-managed policy
    once the set of resources has settled.
  EOT
  type        = string
  default     = "arn:aws:iam::aws:policy/PowerUserAccess"
}

variable "tags" {
  description = "Tags applied to every resource in this module."
  type        = map(string)
  default     = {}
}
