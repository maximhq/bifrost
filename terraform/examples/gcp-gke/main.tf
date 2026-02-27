terraform {
  required_version = ">= 1.0"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 5.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

module "bifrost" {
  source         = "../../modules/bifrost"
  cloud_provider = "gcp"
  service        = "gke"
  region         = var.region
  gcp_project_id = var.project_id
  image_tag      = var.image_tag
  name_prefix    = var.name_prefix

  # Config: use a file as base, override with variables
  config_json_file = var.config_json_file

  # Override specific config sections
  config_store     = var.config_store
  logs_store       = var.logs_store
  providers_config = var.providers_config

  # Compute
  desired_count        = var.desired_count
  cpu                  = var.cpu
  memory               = var.memory
  create_cluster       = var.create_cluster
  node_count           = var.node_count
  create_load_balancer = var.create_load_balancer

  # Autoscaling
  enable_autoscaling = var.enable_autoscaling
  min_capacity       = var.min_capacity
  max_capacity       = var.max_capacity
}
