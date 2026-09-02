variable "name_prefix" {
  description = "Prefix for every resource name, e.g. \"imageforge-dev\"."
  type        = string
}

variable "vpc_cidr" {
  description = "CIDR block for the VPC."
  type        = string
  default     = "10.20.0.0/16"
}

variable "availability_zone_count" {
  description = <<-EOT
    How many availability zones to spread subnets across. Two is the minimum an
    Application Load Balancer will accept.
  EOT
  type        = number
  default     = 2

  validation {
    condition     = var.availability_zone_count >= 2
    error_message = "availability_zone_count must be at least 2, which is what an ALB requires."
  }
}

variable "enable_nat_gateway" {
  description = <<-EOT
    Whether to run private subnets behind a NAT gateway.

    False by default, and that is a real trade rather than an oversight. A NAT
    gateway is roughly 32 USD a month before data charges, which on a dev
    deployment is comparable to everything else put together. With it off, the
    tasks run in public subnets with public IPs and reach AWS APIs directly;
    they are still not reachable from the internet, because the security group
    accepts nothing except the load balancer. Turn it on for production, where
    tasks belong in private subnets.
  EOT
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags applied to every resource in this module."
  type        = map(string)
  default     = {}
}
