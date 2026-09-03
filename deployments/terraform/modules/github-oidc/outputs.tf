output "push_role_arn" {
  description = "Set this as the AWS_ROLE_ARN repository variable."
  value       = aws_iam_role.push.arn
}

output "plan_role_arn" {
  description = "Set this as the AWS_PLAN_ROLE_ARN repository variable."
  value       = aws_iam_role.plan.arn
}

output "apply_role_arn" {
  description = "Set this as the AWS_APPLY_ROLE_ARN repository variable."
  value       = aws_iam_role.apply.arn
}

output "oidc_provider_arn" {
  description = "ARN of the GitHub OIDC provider in use."
  value       = local.provider_arn
}

output "github_variables" {
  description = "Everything to set under Settings, Secrets and variables, Actions, Variables."
  value = {
    AWS_ROLE_ARN       = aws_iam_role.push.arn
    AWS_PLAN_ROLE_ARN  = aws_iam_role.plan.arn
    AWS_APPLY_ROLE_ARN = aws_iam_role.apply.arn
  }
}
