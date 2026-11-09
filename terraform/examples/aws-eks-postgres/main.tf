# =============================================================================
# Bifrost on AWS: EKS + RDS PostgreSQL 16 + public LoadBalancer Service
#
# The one-click EKS stack. deploy/eks/deploy.sh drives it from AWS CloudShell
# in two phases: the cluster first, then (after `aws eks update-kubeconfig`)
# the database, secrets and Kubernetes resources. The kubernetes provider
# reads the kubeconfig that phase one writes.
#
# This example calls the AWS platform module directly rather than the root
# bifrost module, so a plan needs only AWS credentials.
# =============================================================================

terraform {
  required_version = ">= 1.7"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 2.20"
    }
    random = {
      source  = "hashicorp/random"
      version = ">= 3.0"
    }
  }
}

provider "aws" {
  region = var.region
}

provider "kubernetes" {
  config_path    = pathexpand(var.kubeconfig_path)
  config_context = var.kubeconfig_context
}

locals {
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
  service = "eks"

  config_json       = local.config_json
  image             = "${var.image_repository}:${var.image_tag}"
  container_port    = 8080
  health_check_path = "/health"

  region      = var.region
  name_prefix = var.name_prefix
  tags        = var.tags

  # Cluster
  create_cluster       = true
  kubernetes_namespace = "bifrost"
  node_count           = var.node_count
  node_machine_type    = var.node_machine_type
  volume_size_gb       = 10

  # Managed PostgreSQL for both stores plus generated runtime secrets.
  create_database              = true
  database_instance_class      = var.database_instance_class
  database_storage_gb          = var.database_storage_gb
  database_deletion_protection = var.database_deletion_protection
  database_skip_final_snapshot = var.database_skip_final_snapshot
  generate_secrets             = true

  # One pod behind a public Network Load Balancer.
  desired_count = 1
  cpu           = var.cpu
  memory        = var.memory
  service_type  = var.service_type
  allowed_cidr  = var.allowed_cidr

  # Ingress needs the AWS Load Balancer Controller; the Service covers exposure.
  create_load_balancer         = false
  enable_autoscaling           = false
  min_capacity                 = 1
  max_capacity                 = 1
  autoscaling_cpu_threshold    = 80
  autoscaling_memory_threshold = 80
}
