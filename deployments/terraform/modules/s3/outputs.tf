output "raw_bucket_name" {
  description = "Name of the bucket holding uploaded originals."
  value       = aws_s3_bucket.raw.id
}

output "raw_bucket_arn" {
  description = "ARN of the bucket holding uploaded originals."
  value       = aws_s3_bucket.raw.arn
}

output "processed_bucket_name" {
  description = "Name of the bucket holding transformed results."
  value       = aws_s3_bucket.processed.id
}

output "processed_bucket_arn" {
  description = "ARN of the bucket holding transformed results."
  value       = aws_s3_bucket.processed.arn
}

output "processed_bucket_regional_domain_name" {
  description = "Regional domain name of the processed bucket, for a CloudFront origin."
  value       = aws_s3_bucket.processed.bucket_regional_domain_name
}

output "raw_bucket_regional_domain_name" {
  description = "Regional domain name of the raw bucket, for a CloudFront origin."
  value       = aws_s3_bucket.raw.bucket_regional_domain_name
}
