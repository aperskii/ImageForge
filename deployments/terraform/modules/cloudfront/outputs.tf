output "domain_name" {
  description = "The distribution's domain name, which is where results are fetched from."
  value       = aws_cloudfront_distribution.this.domain_name
}

output "distribution_id" {
  description = "ID of the distribution, for cache invalidations."
  value       = aws_cloudfront_distribution.this.id
}

output "distribution_arn" {
  description = "ARN of the distribution."
  value       = aws_cloudfront_distribution.this.arn
}

output "public_base_url" {
  description = <<-EOT
    The base URL the API should be configured with, so a finished job reports a
    result URL that resolves.
  EOT
  value       = "https://${aws_cloudfront_distribution.this.domain_name}"
}
