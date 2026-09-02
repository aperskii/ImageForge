# The dev environment.
#
# This file is only wiring: every decision that could differ between
# environments is a variable, and everything that could not is in a module.

locals {
  name_prefix = "${var.project}-${var.environment}"

  tags = merge(var.tags, {
    Project     = var.project
    Environment = var.environment
    ManagedBy   = "terraform"
  })

  # Bucket names have to be unique across all of AWS, so the account id goes on
  # the end. It is not a secret -- it appears in every ARN -- and it makes the
  # name deterministic rather than random.
  bucket_suffix = data.aws_caller_identity.current.account_id
}

data "aws_caller_identity" "current" {}

module "s3" {
  source = "../../modules/s3"

  name_prefix   = local.name_prefix
  bucket_suffix = local.bucket_suffix

  raw_expiration_days       = var.raw_expiration_days
  processed_expiration_days = var.processed_expiration_days

  # A dev environment that cannot be destroyed without emptying two buckets by
  # hand is a dev environment nobody destroys.
  force_destroy      = true
  versioning_enabled = false

  tags = local.tags
}

module "sqs" {
  source = "../../modules/sqs"

  name_prefix = local.name_prefix

  # The visibility timeout must exceed the worker's own job timeout, or a job
  # still running is handed to a second worker.
  visibility_timeout_seconds = var.queue_visibility_timeout_seconds
  max_receive_count          = var.queue_max_receive_count

  tags = local.tags
}

module "dynamodb" {
  source = "../../modules/dynamodb"

  name_prefix = local.name_prefix

  # Nothing in a dev table is worth a backup, and nothing in it should survive
  # a deliberate teardown.
  point_in_time_recovery_enabled = false
  deletion_protection_enabled    = false

  tags = local.tags
}

module "ecr" {
  source = "../../modules/ecr"

  name_prefix  = local.name_prefix
  repositories = ["api", "worker"]

  # Tags are pushed and re-pushed constantly while iterating, which immutable
  # tags forbid. Production should keep the default.
  image_tag_mutability = "MUTABLE"
  force_delete         = true

  tags = local.tags
}

module "network" {
  source = "../../modules/network"

  name_prefix = local.name_prefix
  vpc_cidr    = var.vpc_cidr

  # See the module's own documentation: a NAT gateway would roughly double the
  # monthly cost of this environment.
  enable_nat_gateway = var.enable_nat_gateway

  tags = local.tags
}

module "cloudfront" {
  source = "../../modules/cloudfront"

  name_prefix = local.name_prefix

  # Fronting the bucket the application actually writes results to. See the
  # "Known gap" section of the README: with one bucket setting in the
  # application, that is the raw bucket rather than the processed one.
  bucket_id                   = module.s3.raw_bucket_name
  bucket_arn                  = module.s3.raw_bucket_arn
  bucket_regional_domain_name = module.s3.raw_bucket_regional_domain_name

  # The origin bucket also holds uploaded originals, which are private. Without
  # this the distribution would happily serve them to anyone who knew a job id.
  allowed_key_prefix = "results/"

  price_class = var.cloudfront_price_class

  tags = local.tags
}

module "ecs" {
  source = "../../modules/ecs"

  name_prefix = local.name_prefix
  aws_region  = var.aws_region

  vpc_id            = module.network.vpc_id
  public_subnet_ids = module.network.public_subnet_ids
  task_subnet_ids   = module.network.task_subnet_ids
  assign_public_ip  = module.network.tasks_need_public_ip
  api_ingress_cidrs = var.api_ingress_cidrs

  api_image    = "${module.ecr.repository_urls["api"]}:${var.image_tag}"
  worker_image = "${module.ecr.repository_urls["worker"]}:${var.image_tag}"

  api_cpu              = var.api_cpu
  api_memory           = var.api_memory
  api_desired_count    = var.api_desired_count
  worker_cpu           = var.worker_cpu
  worker_memory        = var.worker_memory
  worker_desired_count = var.worker_desired_count

  # What the application is configured with, and what its IAM is scoped to.
  # Only this bucket is granted, so the ECS module never sees the other one.
  app_bucket_name = module.s3.raw_bucket_name
  app_bucket_arn  = module.s3.raw_bucket_arn

  queue_url  = module.sqs.queue_url
  queue_arn  = module.sqs.queue_arn
  table_name = module.dynamodb.table_name
  table_arn  = module.dynamodb.table_arn

  public_base_url = module.cloudfront.public_base_url
  cors_origins    = var.cors_origins

  log_retention_days = var.log_retention_days

  tags = local.tags
}
