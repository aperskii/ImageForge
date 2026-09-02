terraform {
  required_version = ">= 1.9"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.60"
    }

    random = {
      source  = "hashicorp/random"
      version = ">= 3.6"
    }
  }

  # State is local by default, which is fine for one person trying this out and
  # wrong for anything shared: local state cannot be locked, so two applies at
  # once corrupt it. Fill in a bucket and uncomment before a second person
  # touches this environment.
  #
  # backend "s3" {
  #   bucket       = "imageforge-tfstate"
  #   key          = "dev/terraform.tfstate"
  #   region       = "eu-west-1"
  #   encrypt      = true
  #   use_lockfile = true
  # }
}

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = {
      Project     = var.project
      Environment = var.environment
      ManagedBy   = "terraform"
    }
  }
}
