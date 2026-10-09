# Creates the bucket that holds the pipeline stack's remote state. This stack
# keeps local state on purpose: it is the one thing that can't store its state
# in a bucket that doesn't exist yet. Apply it once per account.

terraform {
  required_version = ">= 1.11"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project   = "media-pipeline"
      Stack     = "bootstrap"
      ManagedBy = "terraform"
    }
  }
}

# Versioned so a bad apply's state can be rolled back; locking is done with
# S3-native lock files (use_lockfile), so no DynamoDB table is needed.
module "state_bucket" {
  source = "../modules/private-bucket"

  name                    = var.state_bucket_name
  force_destroy           = var.force_destroy
  versioning              = true
  noncurrent_version_days = 90
}
