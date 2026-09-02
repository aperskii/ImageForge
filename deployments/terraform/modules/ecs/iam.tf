# IAM for the two tasks.
#
# The point of this file is that neither task can do anything the code does not
# actually do. The API never reads an object, so it has no s3:GetObject. The
# worker never creates a job, so it has no dynamodb:PutItem. Where a permission
# only makes sense under a key prefix, the resource ARN says so.

data "aws_caller_identity" "current" {}

# --------------------------------------------------- shared execution role --
# The execution role belongs to the ECS agent, not to the application: it pulls
# the image and writes the log stream before the container starts. Keeping it
# separate from the task role is what stops the application inheriting the
# ability to read every repository in the account.
data "aws_iam_policy_document" "task_execution_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }

    # Only this account's ECS may assume it, which closes the confused-deputy
    # path where another account's task assumes a role by ARN.
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}

resource "aws_iam_role" "task_execution" {
  name               = "${var.name_prefix}-task-execution"
  assume_role_policy = data.aws_iam_policy_document.task_execution_assume.json
  tags               = var.tags
}

resource "aws_iam_role_policy_attachment" "task_execution" {
  role = aws_iam_role.task_execution.name
  # The AWS-managed policy for exactly this job: pull from ECR, write to logs.
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_iam_role_policy" "task_execution_secrets" {
  name = "${var.name_prefix}-task-execution-secrets"
  role = aws_iam_role.task_execution.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid    = "ReadTaskSecrets"
      Effect = "Allow"
      Action = ["ssm:GetParameters"]
      # Only the parameters this deployment owns, not the account's tree.
      Resource = [aws_ssm_parameter.jwt_signing_key.arn]
    }]
  })
}

# ------------------------------------------------------------- task roles ---
data "aws_iam_policy_document" "task_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}

# ---------------------------------------------------------------- api role --
resource "aws_iam_role" "api" {
  name               = "${var.name_prefix}-api-task"
  assume_role_policy = data.aws_iam_policy_document.task_assume.json
  tags               = merge(var.tags, { Service = "api" })
}

data "aws_iam_policy_document" "api" {
  # CreateJob stores the upload. It never reads one back: GET /jobs/{id} answers
  # from DynamoDB, and the result is fetched from CloudFront by the browser. So
  # this is a write-only grant, under the originals prefix only.
  statement {
    sid       = "PutOriginals"
    effect    = "Allow"
    actions   = ["s3:PutObject"]
    resources = ["${var.app_bucket_arn}/originals/*"]
  }

  # Enqueue, plus the lookup the adapter does once at startup to turn a queue
  # name into a URL.
  statement {
    sid    = "EnqueueJobs"
    effect = "Allow"
    actions = [
      "sqs:SendMessage",
      "sqs:GetQueueUrl",
    ]
    resources = [var.queue_arn]
  }

  # Save, Get and UpdateStatus. No Delete, no Scan, no Query: the application
  # only ever addresses a job by its key.
  statement {
    sid    = "ManageJobs"
    effect = "Allow"
    actions = [
      "dynamodb:PutItem",
      "dynamodb:GetItem",
      "dynamodb:UpdateItem",
    ]
    resources = [var.table_arn]
  }
}

resource "aws_iam_role_policy" "api" {
  name   = "${var.name_prefix}-api-task"
  role   = aws_iam_role.api.id
  policy = data.aws_iam_policy_document.api.json
}

# ------------------------------------------------------------- worker role --
resource "aws_iam_role" "worker" {
  name               = "${var.name_prefix}-worker-task"
  assume_role_policy = data.aws_iam_policy_document.task_assume.json
  tags               = merge(var.tags, { Service = "worker" })
}

data "aws_iam_policy_document" "worker" {
  # Reads the original it was asked to transform.
  statement {
    sid       = "ReadOriginals"
    effect    = "Allow"
    actions   = ["s3:GetObject"]
    resources = ["${var.app_bucket_arn}/originals/*"]
  }

  # Writes the result. Separate statement from the read because the prefixes
  # differ: the worker cannot overwrite an original, and cannot read a result.
  statement {
    sid       = "WriteResults"
    effect    = "Allow"
    actions   = ["s3:PutObject"]
    resources = ["${var.app_bucket_arn}/results/*"]
  }

  # Consume and settle. ChangeMessageVisibility is what Nack uses to return a
  # message immediately rather than waiting out its visibility timeout.
  statement {
    sid    = "ConsumeJobs"
    effect = "Allow"
    actions = [
      "sqs:ReceiveMessage",
      "sqs:DeleteMessage",
      "sqs:ChangeMessageVisibility",
      "sqs:GetQueueUrl",
      "sqs:GetQueueAttributes",
    ]
    resources = [var.queue_arn]
  }

  # ProcessJob loads a job and records its outcome. It never creates one, so
  # there is no PutItem here even though the API has it.
  statement {
    sid    = "UpdateJobs"
    effect = "Allow"
    actions = [
      "dynamodb:GetItem",
      "dynamodb:UpdateItem",
    ]
    resources = [var.table_arn]
  }
}

resource "aws_iam_role_policy" "worker" {
  name   = "${var.name_prefix}-worker-task"
  role   = aws_iam_role.worker.id
  policy = data.aws_iam_policy_document.worker.json
}

# ---------------------------------------------------------------- secrets ---
# The token signing key. It is generated here rather than committed, and reaches
# the container through the task definition's secrets block, which means it
# never appears in the definition itself where anyone with
# ecs:DescribeTaskDefinition could read it.
# 32 random bytes, rendered as hex, which is exactly what the application
# expects: it decodes IMAGEFORGE_JWT_KEY as hex and refuses anything shorter
# than 32 bytes.
resource "random_id" "jwt_signing_key" {
  byte_length = 32
}

resource "aws_ssm_parameter" "jwt_signing_key" {
  name        = "/${var.name_prefix}/jwt-signing-key"
  description = "HS256 key ImageForge signs its bearer tokens with"
  type        = "SecureString"
  value       = random_id.jwt_signing_key.hex

  tags = var.tags

  lifecycle {
    # Rotating the key invalidates every outstanding token, so it should be a
    # deliberate act rather than a side effect of a plan.
    ignore_changes = [value]
  }
}
