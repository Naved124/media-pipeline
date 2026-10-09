terraform {
  required_version = ">= 1.11"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  # Partial configuration: bucket and region come from a gitignored
  # backend.tfbackend (see backend.tfbackend.example), so no account-specific
  # names live in the repository.
  #   terraform init -backend-config=backend.tfbackend
  backend "s3" {
    key          = "pipeline/terraform.tfstate"
    encrypt      = true
    use_lockfile = true
  }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project   = var.project
      Stack     = "pipeline"
      ManagedBy = "terraform"
    }
  }
}

data "aws_caller_identity" "current" {}
