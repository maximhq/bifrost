# =============================================================================
# Bifrost on AWS: ECS Fargate + RDS PostgreSQL 16 + ALB
#
# The one-click AWS stack. Both Bifrost stores live in RDS, the encryption key
# and setup token are generated into Secrets Manager, and an ALB exposes the
# service. deploy/aws/deploy.sh drives this example from AWS CloudShell.
#
# This example calls the AWS platform module directly rather than the root
# bifrost module. Terraform configures every provider referenced in a module
# tree, so the root module's plan needs Google and Azure credentials as well;
# the platform module needs only AWS.
# =============================================================================

terraform {
  required_version = ">= 1.7"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    random = {
      source  = "hashicorp/random"
      version = ">= 3.0"
    }
    # Pulled in by the (unused, count = 0) EKS sub-module. It needs no
    # configuration and no cluster.
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 2.0"
    }
  }
}

provider "aws" {
  region = var.region
}

locals {
  # The shared one-click config.json. Every store value is an env reference
  # that the platform module satisfies through PG_* and BIFROST_* variables.
  base_config = jsondecode(file("${path.module}/../../../deploy/config.postgres.json"))

  config_json = jsonencode(merge(
    local.base_config,
    {
      client = merge(try(local.base_config.client, {}), {
        enforce_auth_on_inference = var.enforce_auth_on_inference
      })
    },
    var.providers_config != null ? { providers = var.providers_config } : {},
  ))
}

module "bifrost" {
  source  = "../../modules/bifrost/aws"
  service = "ecs"

  config_json       = local.config_json
  image             = "${var.image_repository}:${var.image_tag}"
  container_port    = 8080
  health_check_path = "/health"

  region      = var.region
  name_prefix = var.name_prefix
  tags        = var.tags

  # Managed PostgreSQL for both stores plus generated runtime secrets.
  create_database              = true
  database_instance_class      = var.database_instance_class
  database_storage_gb          = var.database_storage_gb
  database_deletion_protection = var.database_deletion_protection
  generate_secrets             = true

  # Compute and exposure
  desired_count        = var.desired_count
  cpu                  = var.cpu
  memory               = var.memory
  create_load_balancer = true
  allowed_cidr         = var.allowed_cidr
  create_cluster       = true

  # Not used by ECS; required by the shared platform interface.
  enable_autoscaling           = false
  min_capacity                 = 1
  max_capacity                 = 1
  autoscaling_cpu_threshold    = 80
  autoscaling_memory_threshold = 80
  kubernetes_namespace         = "bifrost"
  node_count                   = 0
  volume_size_gb               = 10
}
