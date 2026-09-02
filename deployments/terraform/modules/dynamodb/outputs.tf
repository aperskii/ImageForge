output "table_name" {
  description = "Name of the job table, which is what the application is configured with."
  value       = aws_dynamodb_table.jobs.name
}

output "table_arn" {
  description = "ARN of the job table, for IAM policies."
  value       = aws_dynamodb_table.jobs.arn
}

output "hash_key" {
  description = "The partition key attribute name."
  value       = aws_dynamodb_table.jobs.hash_key
}
