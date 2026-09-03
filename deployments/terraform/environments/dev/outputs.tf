output "raw_bucket_name" {
  description = "Bucket holding uploaded originals."
  value       = module.s3.raw_bucket_name
}

output "processed_bucket_name" {
  description = "Bucket holding transformed results."
  value       = module.s3.processed_bucket_name
}

output "queue_url" {
  description = "URL of the job queue."
  value       = module.sqs.queue_url
}

output "dlq_url" {
  description = "URL of the dead-letter queue."
  value       = module.sqs.dlq_url
}

output "table_name" {
  description = "Name of the DynamoDB job table."
  value       = module.dynamodb.table_name
}

output "cloudfront_domain_name" {
  description = "Domain results are served from."
  value       = module.cloudfront.domain_name
}

output "cloudfront_distribution_id" {
  description = "Distribution id, for cache invalidations."
  value       = module.cloudfront.distribution_id
}

output "api_url" {
  description = "Base URL of the API."
  value       = module.ecs.api_url
}

output "ecr_repository_urls" {
  description = "Where to push the images, keyed by service."
  value       = module.ecr.repository_urls
}

output "ecs_cluster_name" {
  description = "Name of the ECS cluster."
  value       = module.ecs.cluster_name
}

output "ecs_service_names" {
  description = "Service names, for forcing a new deployment."
  value = {
    api    = module.ecs.api_service_name
    worker = module.ecs.worker_service_name
  }
}

output "deploy_command" {
  description = "What to run after pushing a new image, to roll the services onto it."
  value = join(" && ", [
    "aws ecs update-service --cluster ${module.ecs.cluster_name} --service ${module.ecs.api_service_name} --force-new-deployment",
    "aws ecs update-service --cluster ${module.ecs.cluster_name} --service ${module.ecs.worker_service_name} --force-new-deployment",
  ])
}

output "aws_region" {
  description = "Region everything was created in."
  value       = var.aws_region
}

output "github_actions_variables" {
  description = <<-EOT
    Repository variables to set under Settings, Secrets and variables, Actions.
    Empty when no GitHub repository is configured.
  EOT
  value = length(module.github_oidc) == 0 ? {} : merge(
    module.github_oidc[0].github_variables,
    { AWS_REGION = var.aws_region, ECR_NAMESPACE = local.name_prefix },
  )
}
