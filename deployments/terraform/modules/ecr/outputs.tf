output "repository_urls" {
  description = "Push and pull URLs, keyed by image short name."
  value       = { for name, repo in aws_ecr_repository.this : name => repo.repository_url }
}

output "repository_arns" {
  description = "Repository ARNs, keyed by image short name, for IAM policies."
  value       = { for name, repo in aws_ecr_repository.this : name => repo.arn }
}

output "repository_names" {
  description = "Repository names, keyed by image short name."
  value       = { for name, repo in aws_ecr_repository.this : name => repo.name }
}
