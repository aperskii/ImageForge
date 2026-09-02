variable "name_prefix" {
  description = "Prefix for every resource name, e.g. \"imageforge-dev\"."
  type        = string
}

variable "visibility_timeout_seconds" {
  description = <<-EOT
    How long a received message stays hidden from other consumers. It must
    exceed the time a job takes to process, or the same job is handed to a
    second worker while the first is still on it.
  EOT
  type        = number
  default     = 120

  validation {
    condition     = var.visibility_timeout_seconds >= 0 && var.visibility_timeout_seconds <= 43200
    error_message = "visibility_timeout_seconds must be between 0 and 43200 (12 hours)."
  }
}

variable "message_retention_seconds" {
  description = "How long an unconsumed message is kept. Four days by default."
  type        = number
  default     = 345600

  validation {
    condition     = var.message_retention_seconds >= 60 && var.message_retention_seconds <= 1209600
    error_message = "message_retention_seconds must be between 60 and 1209600 (14 days)."
  }
}

variable "dlq_retention_seconds" {
  description = <<-EOT
    How long a dead-lettered message is kept. Longer than the main queue on
    purpose: a message only reaches the DLQ because something needs looking at,
    and that should survive a weekend.
  EOT
  type        = number
  default     = 1209600
}

variable "max_receive_count" {
  description = <<-EOT
    How many times a message may be received before it is moved to the DLQ. A
    poison message that kills the worker mid-job reappears each time its
    visibility timeout expires, so this bounds the damage one can do.
  EOT
  type        = number
  default     = 5

  validation {
    condition     = var.max_receive_count >= 1 && var.max_receive_count <= 1000
    error_message = "max_receive_count must be between 1 and 1000."
  }
}

variable "receive_wait_time_seconds" {
  description = <<-EOT
    Long-polling wait on the queue itself, so a consumer that forgets to ask for
    it still gets it. Twenty seconds is the SQS maximum.
  EOT
  type        = number
  default     = 20

  validation {
    condition     = var.receive_wait_time_seconds >= 0 && var.receive_wait_time_seconds <= 20
    error_message = "receive_wait_time_seconds must be between 0 and 20."
  }
}

variable "alarm_actions" {
  description = <<-EOT
    SNS topic ARNs notified when the dead-letter queue is not empty. Empty by
    default, which leaves the alarm defined but silent.
  EOT
  type        = list(string)
  default     = []
}

variable "tags" {
  description = "Tags applied to every resource in this module."
  type        = map(string)
  default     = {}
}
