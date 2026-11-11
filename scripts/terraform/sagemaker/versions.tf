terraform {
  required_version = ">= 1.11"

  required_providers {
    # 6.x accepts InferenceAmiVersion al2023-ami-sagemaker-inference-gpu-4-1, which the CUDA 13 images need.
    aws = {
      source  = "hashicorp/aws"
      version = ">= 6.0, < 7.0"
    }
    # Reads JumpStart model specs from the public jumpstart-cache-prod-<region> bucket.
    http = {
      source  = "hashicorp/http"
      version = "~> 3.4"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

# Credentials come from the usual AWS chain (env, profile, SSO).
provider "aws" {
  region = var.region

  default_tags {
    tags = merge({ "bifrost-test" = "true" }, var.tags)
  }
}
