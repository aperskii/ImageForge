# The job queue and its dead-letter queue.
#
# The DLQ has to exist before the main queue can name it in a redrive policy,
# which is why it is declared first; Terraform works the order out from the
# reference either way.

locals {
  queue_name = "${var.name_prefix}-jobs"
  dlq_name   = "${var.name_prefix}-jobs-dlq"
}

resource "aws_sqs_queue" "dlq" {
  name                      = local.dlq_name
  message_retention_seconds = var.dlq_retention_seconds
  sqs_managed_sse_enabled   = true

  tags = merge(var.tags, {
    Name = local.dlq_name
    Role = "dead-letter"
  })
}

resource "aws_sqs_queue" "jobs" {
  name                       = local.queue_name
  visibility_timeout_seconds = var.visibility_timeout_seconds
  message_retention_seconds  = var.message_retention_seconds
  receive_wait_time_seconds  = var.receive_wait_time_seconds
  sqs_managed_sse_enabled    = true

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq.arn
    maxReceiveCount     = var.max_receive_count
  })

  tags = merge(var.tags, {
    Name = local.queue_name
    Role = "jobs"
  })
}

# The other half of the redrive relationship: without this the DLQ would accept
# dead letters from any queue in the account that named it.
resource "aws_sqs_queue_redrive_allow_policy" "dlq" {
  queue_url = aws_sqs_queue.dlq.id

  redrive_allow_policy = jsonencode({
    redrivePermission = "byQueue"
    sourceQueueArns   = [aws_sqs_queue.jobs.arn]
  })
}

# A message on the dead-letter queue means a job failed enough times to be given
# up on, which is never routine and always worth a look.
resource "aws_cloudwatch_metric_alarm" "dlq_not_empty" {
  alarm_name          = "${local.dlq_name}-not-empty"
  alarm_description   = "Messages are sitting on the ImageForge dead-letter queue."
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateNumberOfMessagesVisible"
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 0
  comparison_operator = "GreaterThanThreshold"
  # Missing data means SQS published nothing, which for this metric means the
  # queue is empty. Treating it as breaching would alarm on a healthy system.
  treat_missing_data = "notBreaching"

  dimensions = {
    QueueName = aws_sqs_queue.dlq.name
  }

  alarm_actions = var.alarm_actions
  ok_actions    = var.alarm_actions

  tags = var.tags
}
