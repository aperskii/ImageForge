# The job state table.
#
# On-demand billing rather than provisioned capacity: the access pattern is one
# small write per upload and a handful of reads while a client polls, which is
# both spiky and tiny. Provisioned capacity would mean guessing a number and
# paying for it around the clock.

locals {
  table_name = "${var.name_prefix}-jobs"
}

resource "aws_dynamodb_table" "jobs" {
  name         = local.table_name
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = var.hash_key

  # Only the key is declared. DynamoDB is schemaless beyond its keys, and the
  # rest of a job's attributes are the application's business.
  attribute {
    name = var.hash_key
    type = "S"
  }

  point_in_time_recovery {
    enabled = var.point_in_time_recovery_enabled
  }

  server_side_encryption {
    # AWS-owned key: encrypted at rest at no cost. A customer-managed key would
    # add per-request KMS charges for no benefit this table needs.
    enabled = false
  }

  dynamic "ttl" {
    for_each = var.ttl_attribute != "" ? [1] : []
    content {
      attribute_name = var.ttl_attribute
      enabled        = true
    }
  }

  deletion_protection_enabled = var.deletion_protection_enabled

  tags = merge(var.tags, {
    Name = local.table_name
  })
}
