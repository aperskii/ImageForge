output "cluster_name" {
  description = "Name of the ECS cluster."
  value       = aws_ecs_cluster.this.name
}

output "api_url" {
  description = "Base URL of the API, through its load balancer."
  value       = "http://${aws_lb.api.dns_name}"
}

output "api_dns_name" {
  description = "DNS name of the load balancer."
  value       = aws_lb.api.dns_name
}

output "api_service_name" {
  description = "Name of the API service, for `aws ecs update-service`."
  value       = aws_ecs_service.api.name
}

output "worker_service_name" {
  description = "Name of the worker service."
  value       = aws_ecs_service.worker.name
}

output "api_task_role_arn" {
  description = "ARN of the API task role."
  value       = aws_iam_role.api.arn
}

output "worker_task_role_arn" {
  description = "ARN of the worker task role."
  value       = aws_iam_role.worker.arn
}

output "jwt_parameter_name" {
  description = "Parameter Store name holding the token signing key."
  value       = aws_ssm_parameter.jwt_signing_key.name
}
