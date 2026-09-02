output "vpc_id" {
  description = "ID of the VPC."
  value       = aws_vpc.this.id
}

output "public_subnet_ids" {
  description = "IDs of the public subnets."
  value       = aws_subnet.public[*].id
}

output "private_subnet_ids" {
  description = "IDs of the private subnets, empty when no NAT gateway is enabled."
  value       = aws_subnet.private[*].id
}

output "task_subnet_ids" {
  description = <<-EOT
    Subnets the ECS tasks should run in: private when a NAT gateway pays for
    their egress, public otherwise.
  EOT
  value       = var.enable_nat_gateway ? aws_subnet.private[*].id : aws_subnet.public[*].id
}

output "tasks_need_public_ip" {
  description = <<-EOT
    Whether tasks need a public IP to reach AWS APIs. True in the no-NAT layout,
    where the public subnet route table is their only way out.
  EOT
  value       = !var.enable_nat_gateway
}
