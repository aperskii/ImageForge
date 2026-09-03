# The Fargate cluster, the load balancer in front of the API, and the two
# services.
#
# The worker has no load balancer and no inbound rule at all: it reaches SQS and
# is never reached.

locals {
  api_container    = "api"
  worker_container = "worker"

  # Settings both services share, so the two task definitions cannot drift on
  # which bucket or which table they are pointed at.
  common_environment = [
    { name = "IMAGEFORGE_BACKEND", value = "aws" },
    { name = "AWS_REGION", value = var.aws_region },
    { name = "IMAGEFORGE_S3_BUCKET", value = var.app_bucket_name },
    { name = "IMAGEFORGE_SQS_QUEUE", value = var.queue_url },
    { name = "IMAGEFORGE_DYNAMODB_TABLE", value = var.table_name },
    { name = "IMAGEFORGE_LOG_LEVEL", value = var.log_level },
  ]
}

resource "aws_ecs_cluster" "this" {
  name = "${var.name_prefix}-cluster"

  setting {
    name  = "containerInsights"
    value = var.enable_container_insights ? "enabled" : "disabled"
  }

  tags = var.tags
}

resource "aws_ecs_cluster_capacity_providers" "this" {
  cluster_name       = aws_ecs_cluster.this.name
  capacity_providers = ["FARGATE", "FARGATE_SPOT"]

  default_capacity_provider_strategy {
    capacity_provider = "FARGATE"
    weight            = 1
  }
}

# ------------------------------------------------------------------- logs ----
resource "aws_cloudwatch_log_group" "api" {
  name              = "/ecs/${var.name_prefix}/api"
  retention_in_days = var.log_retention_days
  tags              = merge(var.tags, { Service = "api" })
}

resource "aws_cloudwatch_log_group" "worker" {
  name              = "/ecs/${var.name_prefix}/worker"
  retention_in_days = var.log_retention_days
  tags              = merge(var.tags, { Service = "worker" })
}

# -------------------------------------------------------- security groups ----
resource "aws_security_group" "alb" {
  name        = "${var.name_prefix}-alb"
  description = "Ingress to the ImageForge load balancer"
  vpc_id      = var.vpc_id

  tags = merge(var.tags, { Name = "${var.name_prefix}-alb" })
}

resource "aws_vpc_security_group_ingress_rule" "alb_http" {
  for_each = toset(var.api_ingress_cidrs)

  security_group_id = aws_security_group.alb.id
  description       = "HTTP from ${each.value}"
  cidr_ipv4         = each.value
  from_port         = 80
  to_port           = 80
  ip_protocol       = "tcp"
}

resource "aws_vpc_security_group_egress_rule" "alb_to_tasks" {
  security_group_id            = aws_security_group.alb.id
  description                  = "Forward to the API tasks"
  referenced_security_group_id = aws_security_group.api.id
  from_port                    = 8080
  to_port                      = 8080
  ip_protocol                  = "tcp"
}

resource "aws_security_group" "api" {
  name        = "${var.name_prefix}-api-task"
  description = "ImageForge API tasks"
  vpc_id      = var.vpc_id

  tags = merge(var.tags, { Name = "${var.name_prefix}-api-task" })
}

# The only way in is the load balancer. Even with a public IP, nothing on the
# internet can reach the task directly.
resource "aws_vpc_security_group_ingress_rule" "api_from_alb" {
  security_group_id            = aws_security_group.api.id
  description                  = "From the load balancer only"
  referenced_security_group_id = aws_security_group.alb.id
  from_port                    = 8080
  to_port                      = 8080
  ip_protocol                  = "tcp"
}

# Egress is open because the task has to reach S3, SQS, DynamoDB, ECR and
# CloudWatch, whose address ranges change. VPC endpoints would let this be
# closed, at a per-endpoint hourly cost this deployment does not justify.
resource "aws_vpc_security_group_egress_rule" "api_all" {
  security_group_id = aws_security_group.api.id
  description       = "AWS APIs"
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

resource "aws_security_group" "worker" {
  name        = "${var.name_prefix}-worker-task"
  description = "ImageForge worker tasks"
  vpc_id      = var.vpc_id

  tags = merge(var.tags, { Name = "${var.name_prefix}-worker-task" })
}

# No ingress rule at all: the worker pulls from SQS and is never called.
resource "aws_vpc_security_group_egress_rule" "worker_all" {
  security_group_id = aws_security_group.worker.id
  description       = "AWS APIs"
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

# ---------------------------------------------------------- load balancer ----
resource "aws_lb" "api" {
  name               = "${var.name_prefix}-api"
  load_balancer_type = "application"
  internal           = false
  security_groups    = [aws_security_group.alb.id]
  subnets            = var.public_subnet_ids

  drop_invalid_header_fields = true

  tags = merge(var.tags, { Service = "api" })
}

resource "aws_lb_target_group" "api" {
  name        = "${var.name_prefix}-api"
  port        = 8080
  protocol    = "HTTP"
  vpc_id      = var.vpc_id
  target_type = "ip"

  health_check {
    enabled = true
    path    = "/healthz"
    matcher = "200"
    # /healthz performs no dependency checks, so it is cheap enough to ask
    # often and answers only about this task.
    interval            = 15
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }

  # Long enough for an in-flight upload to finish, short enough that a deploy
  # does not crawl.
  deregistration_delay = 30

  tags = merge(var.tags, { Service = "api" })
}

resource "aws_lb_listener" "api" {
  load_balancer_arn = aws_lb.api.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.api.arn
  }

  tags = var.tags
}

# ------------------------------------------------------- task definitions ----
resource "aws_ecs_task_definition" "api" {
  family                   = "${var.name_prefix}-api"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.api_cpu
  memory                   = var.api_memory
  execution_role_arn       = aws_iam_role.task_execution.arn
  task_role_arn            = aws_iam_role.api.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([{
    name      = local.api_container
    image     = var.api_image
    essential = true

    portMappings = [{
      containerPort = 8080
      protocol      = "tcp"
    }]

    environment = concat(local.common_environment, [
      { name = "IMAGEFORGE_ADDR", value = ":8080" },
      { name = "IMAGEFORGE_PUBLIC_BASE_URL", value = var.public_base_url },
      { name = "IMAGEFORGE_CORS_ORIGINS", value = var.cors_origins },
    ])

    # Injected by the agent from Parameter Store, so the value is not part of
    # the task definition and cannot be read back from the ECS API.
    secrets = [{
      name      = "IMAGEFORGE_JWT_KEY"
      valueFrom = aws_ssm_parameter.jwt_signing_key.arn
    }]

    # The image is distroless with no shell, so the check is the binary itself.
    healthCheck = {
      command     = ["CMD", "/usr/local/bin/api", "healthcheck"]
      interval    = 15
      timeout     = 5
      retries     = 3
      startPeriod = 10
    }

    readonlyRootFilesystem = true

    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = aws_cloudwatch_log_group.api.name
        "awslogs-region"        = var.aws_region
        "awslogs-stream-prefix" = "ecs"
      }
    }
  }])

  tags = merge(var.tags, { Service = "api" })
}

resource "aws_ecs_task_definition" "worker" {
  family                   = "${var.name_prefix}-worker"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.worker_cpu
  memory                   = var.worker_memory
  execution_role_arn       = aws_iam_role.task_execution.arn
  task_role_arn            = aws_iam_role.worker.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([{
    name      = local.worker_container
    image     = var.worker_image
    essential = true

    portMappings = [{
      containerPort = 9090
      protocol      = "tcp"
    }]

    environment = concat(local.common_environment, [
      { name = "IMAGEFORGE_METRICS_ADDR", value = ":9090" },
      { name = "IMAGEFORGE_WORKERS", value = tostring(var.worker_pool_size) },
    ])

    healthCheck = {
      command     = ["CMD", "/usr/local/bin/worker", "healthcheck"]
      interval    = 15
      timeout     = 5
      retries     = 3
      startPeriod = 15
    }

    # libvips spills a large image to a temporary file rather than holding all
    # of it, so this one needs a writable filesystem.
    readonlyRootFilesystem = false

    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = aws_cloudwatch_log_group.worker.name
        "awslogs-region"        = var.aws_region
        "awslogs-stream-prefix" = "ecs"
      }
    }
  }])

  tags = merge(var.tags, { Service = "worker" })
}

# --------------------------------------------------------------- services ----
resource "aws_ecs_service" "api" {
  name            = "${var.name_prefix}-api"
  cluster         = aws_ecs_cluster.this.id
  task_definition = aws_ecs_task_definition.api.arn
  desired_count   = var.api_desired_count
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = var.task_subnet_ids
    security_groups  = [aws_security_group.api.id]
    assign_public_ip = var.assign_public_ip
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.api.arn
    container_name   = local.api_container
    container_port   = 8080
  }

  # Give the task time to come up before the load balancer decides it is
  # unhealthy and replaces it, which otherwise loops.
  health_check_grace_period_seconds = 30

  deployment_circuit_breaker {
    enable = true
    # A deploy whose tasks will not stabilize rolls back rather than leaving
    # the service half-replaced.
    rollback = true
  }

  # The listener has to exist before the service registers targets against it.
  depends_on = [aws_lb_listener.api]

  tags = merge(var.tags, { Service = "api" })

  lifecycle {
    # A deploy pipeline updates the image and therefore the task definition;
    # Terraform should not fight it back to the revision in state.
    ignore_changes = [task_definition, desired_count]
  }
}

resource "aws_ecs_service" "worker" {
  name            = "${var.name_prefix}-worker"
  cluster         = aws_ecs_cluster.this.id
  task_definition = aws_ecs_task_definition.worker.arn
  desired_count   = var.worker_desired_count
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = var.task_subnet_ids
    security_groups  = [aws_security_group.worker.id]
    assign_public_ip = var.assign_public_ip
  }

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  tags = merge(var.tags, { Service = "worker" })

  lifecycle {
    ignore_changes = [task_definition, desired_count]
  }
}
