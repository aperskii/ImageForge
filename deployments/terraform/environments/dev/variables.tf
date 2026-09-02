# ------------------------------------------------------------- identity -----
variable "project" {
  description = "Project name, the first part of every resource name."
  type        = string
  default     = "imageforge"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,20}$", var.project))
    error_message = "project must be lowercase letters, digits and hyphens, 2 to 21 characters, starting with a letter."
  }
}

variable "environment" {
  description = <<-EOT
    Which environment this is. It appears in every resource name, so two
    environments in one account never collide.
  EOT
  type        = string
  default     = "dev"

  validation {
    condition     = contains(["dev", "staging", "prod"], var.environment)
    error_message = "environment must be dev, staging or prod."
  }
}

variable "aws_region" {
  description = "Region everything is created in."
  type        = string
  default     = "eu-west-1"
}

variable "tags" {
  description = "Extra tags merged into the ones every resource already carries."
  type        = map(string)
  default     = {}
}

# ------------------------------------------------------------ networking ----
variable "vpc_cidr" {
  description = "CIDR block for the VPC."
  type        = string
  default     = "10.20.0.0/16"
}

variable "enable_nat_gateway" {
  description = <<-EOT
    Whether to put tasks in private subnets behind a NAT gateway. False for dev:
    a NAT gateway costs roughly 32 USD a month before data charges, which is
    comparable to everything else in this environment put together.
  EOT
  type        = bool
  default     = false
}

variable "api_ingress_cidrs" {
  description = "Who may reach the API load balancer."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "cors_origins" {
  description = "Comma-separated origins the API accepts browser requests from."
  type        = string
  default     = "*"
}

# ---------------------------------------------------------------- images ----
variable "image_tag" {
  description = <<-EOT
    Tag of the images the services run.

    "latest" is convenient and wrong for anything but dev: it makes a deploy
    unrepeatable and a rollback meaningless. Pin a digest or a commit sha in
    staging and production.
  EOT
  type        = string
  default     = "latest"
}

# ---------------------------------------------------------------- sizing ----
variable "api_cpu" {
  description = "CPU units for the API task, where 1024 is one vCPU."
  type        = number
  default     = 256
}

variable "api_memory" {
  description = "Memory in MiB for the API task."
  type        = number
  default     = 512
}

variable "api_desired_count" {
  description = "How many API tasks to run. One is a dev choice; production wants at least two."
  type        = number
  default     = 1
}

variable "worker_cpu" {
  description = "CPU units for the worker task. Image work is CPU-bound, so this decides throughput."
  type        = number
  default     = 512
}

variable "worker_memory" {
  description = "Memory in MiB for the worker task."
  type        = number
  default     = 1024
}

variable "worker_desired_count" {
  description = "How many worker tasks to run."
  type        = number
  default     = 1
}

# ------------------------------------------------------------- lifecycle ----
variable "raw_expiration_days" {
  description = "Days an uploaded original is kept."
  type        = number
  default     = 7
}

variable "processed_expiration_days" {
  description = "Days a transformed result is kept."
  type        = number
  default     = 30
}

variable "log_retention_days" {
  description = "Days container logs are kept."
  type        = number
  default     = 7
}

# ----------------------------------------------------------------- queue ----
variable "queue_visibility_timeout_seconds" {
  description = <<-EOT
    How long a received message stays hidden. It must exceed the worker's job
    timeout, which defaults to two minutes, or a job still running is handed to
    a second worker.
  EOT
  type        = number
  default     = 180
}

variable "queue_max_receive_count" {
  description = "Receives before a message is moved to the dead-letter queue."
  type        = number
  default     = 5
}

# ------------------------------------------------------------ cloudfront ----
variable "cloudfront_price_class" {
  description = "Which edge locations to serve from. PriceClass_100 is the cheapest."
  type        = string
  default     = "PriceClass_100"
}
