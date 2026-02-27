terraform {
  required_version = ">= 1.0"
  required_providers {
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.0"
    }
  }
}

provider "kubernetes" {
  config_path    = pathexpand(var.kubeconfig_path)
  config_context = var.kubeconfig_context
}

module "bifrost" {
  source         = "../../modules/bifrost"
  cloud_provider = "kubernetes"
  service        = "deployment"
  region         = "local"
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
  kubernetes_namespace = var.kubernetes_namespace
  volume_size_gb       = var.volume_size_gb
  storage_class_name   = var.storage_class_name

  # Ingress
  create_load_balancer = var.create_load_balancer
  ingress_class_name   = var.ingress_class_name
  ingress_annotations  = var.ingress_annotations
  domain_name          = var.domain_name

  # Autoscaling
  enable_autoscaling = var.enable_autoscaling
  min_capacity       = var.min_capacity
  max_capacity       = var.max_capacity
}
