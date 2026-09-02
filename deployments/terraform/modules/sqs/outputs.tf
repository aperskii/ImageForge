output "queue_url" {
  description = "URL of the job queue, which is what the application is configured with."
  value       = aws_sqs_queue.jobs.url
}

output "queue_arn" {
  description = "ARN of the job queue, for IAM policies."
  value       = aws_sqs_queue.jobs.arn
}

output "queue_name" {
  description = "Name of the job queue."
  value       = aws_sqs_queue.jobs.name
}

output "dlq_url" {
  description = "URL of the dead-letter queue."
  value       = aws_sqs_queue.dlq.url
}

output "dlq_arn" {
  description = "ARN of the dead-letter queue."
  value       = aws_sqs_queue.dlq.arn
}

output "dlq_name" {
  description = "Name of the dead-letter queue."
  value       = aws_sqs_queue.dlq.name
}
