variable "name_prefix" {
  description = "Prefix for every resource name, e.g. \"imageforge-dev\"."
  type        = string
}

variable "aws_region" {
  description = "Region the tasks run in, passed to the containers and the log driver."
  type        = string
}

# ------------------------------------------------------------- networking ---
variable "vpc_id" {
  description = "VPC the service and load balancer live in."
  type        = string
}

variable "public_subnet_ids" {
  description = "Subnets for the load balancer. At least two, in different zones."
  type        = list(string)
}

variable "task_subnet_ids" {
  description = "Subnets the tasks run in."
  type        = list(string)
}

variable "assign_public_ip" {
  description = <<-EOT
    Whether tasks get a public IP. Required when they run in public subnets,
    because that route table is then their only way to reach the AWS APIs.
  EOT
  type        = bool
  default     = true
}

variable "api_ingress_cidrs" {
  description = <<-EOT
    Who may reach the load balancer. Open by default because the API is meant to
    be public; narrow it to an office range for a dev deployment that should not
    be.
  EOT
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

# ----------------------------------------------------------------- images ---
variable "api_image" {
  description = "Fully qualified image for the API, including its tag."
  type        = string
}

variable "worker_image" {
  description = "Fully qualified image for the worker, including its tag."
  type        = string
}

# ------------------------------------------------------------------ sizing --
variable "api_cpu" {
  description = "CPU units for the API task. 256 is a quarter of a vCPU."
  type        = number
  default     = 256
}

variable "api_memory" {
  description = "Memory in MiB for the API task."
  type        = number
  default     = 512
}

variable "api_desired_count" {
  description = "How many API tasks to run."
  type        = number
  default     = 1
}

variable "worker_cpu" {
  description = <<-EOT
    CPU units for the worker task. Image work is CPU-bound, so this is the knob
    that decides throughput; 512 is half a vCPU.
  EOT
  type        = number
  default     = 512
}

variable "worker_memory" {
  description = <<-EOT
    Memory in MiB for the worker task. libvips holds whole decoded images, so
    this needs headroom over the file size, not over the compressed bytes.
  EOT
  type        = number
  default     = 1024
}

variable "worker_desired_count" {
  description = "How many worker tasks to run."
  type        = number
  default     = 1
}

variable "worker_pool_size" {
  description = "Goroutines per worker task, passed as IMAGEFORGE_WORKERS."
  type        = number
  default     = 4
}

# ------------------------------------------------------------- resources ----
variable "app_bucket_name" {
  description = <<-EOT
    The bucket the application is actually configured with.

    The application takes one bucket setting today and writes both prefixes to
    it, so this decides where originals and results both land. See the "Known
    gap" section of the README: splitting them across the two buckets above
    needs a second setting in the application.
  EOT
  type        = string
}

variable "app_bucket_arn" {
  description = "ARN of the bucket named by app_bucket_name."
  type        = string
}

variable "queue_url" {
  description = "URL of the job queue."
  type        = string
}

variable "queue_arn" {
  description = "ARN of the job queue, for IAM."
  type        = string
}

variable "table_name" {
  description = "Name of the job table."
  type        = string
}

variable "table_arn" {
  description = "ARN of the job table, for IAM."
  type        = string
}

variable "public_base_url" {
  description = "Base URL a finished job's result URL is built from, normally the CloudFront domain."
  type        = string
}

variable "cors_origins" {
  description = "Comma-separated origins the API accepts browser requests from."
  type        = string
  default     = "*"
}

# -------------------------------------------------------------- operations --
variable "log_retention_days" {
  description = "How long to keep container logs. CloudWatch keeps them forever by default, which bills forever."
  type        = number
  default     = 14
}

variable "enable_container_insights" {
  description = "Whether to collect ECS Container Insights, which costs extra per metric."
  type        = bool
  default     = false
}

variable "log_level" {
  description = "IMAGEFORGE_LOG_LEVEL for both services."
  type        = string
  default     = "INFO"
}

variable "tags" {
  description = "Tags applied to every resource in this module."
  type        = map(string)
  default     = {}
}
