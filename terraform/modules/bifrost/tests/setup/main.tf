# =============================================================================
# Test Wrapper Module
#
# This module exists solely to declare all required_providers in one place
# for the Terraform test framework. The root bifrost module intentionally
# does NOT declare required_providers so users only need to configure the
# provider for their chosen cloud.
#
# Test files use: module { source = "./tests/setup" } in run blocks.
# =============================================================================

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
    google = {
      source  = "hashicorp/google"
      version = ">= 5.0"
    }
    azurerm = {
      source  = "hashicorp/azurerm"
      version = ">= 3.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 2.0"
    }
  }
}

module "bifrost" {
  source = "../../"

  # Deployment target
  cloud_provider = var.cloud_provider
  service        = var.service

  # Config
  config_json    = var.config_json
  config_json_file = var.config_json_file
  encryption_key = var.encryption_key
  auth_config    = var.auth_config
  client         = var.client
  framework      = var.framework
  providers_config = var.providers_config
  governance     = var.governance
  mcp            = var.mcp
  vector_store   = var.vector_store
  config_store   = var.config_store
  logs_store     = var.logs_store
  cluster_config = var.cluster_config
  scim_config    = var.scim_config
  load_balancer_config = var.load_balancer_config
  guardrails_config = var.guardrails_config
  plugins        = var.plugins
  audit_logs     = var.audit_logs
  websocket      = var.websocket

  # Image
  image_tag        = var.image_tag
  image_repository = var.image_repository

  # Infrastructure
  region      = var.region
  name_prefix = var.name_prefix
  tags        = var.tags

  # Compute
  desired_count = var.desired_count
  cpu           = var.cpu
  memory        = var.memory

  # Networking
  existing_vpc_id             = var.existing_vpc_id
  existing_subnet_ids         = var.existing_subnet_ids
  allowed_cidr                = var.allowed_cidr
  existing_security_group_ids = var.existing_security_group_ids

  # Features
  create_load_balancer         = var.create_load_balancer
  assign_public_ip             = var.assign_public_ip
  enable_autoscaling           = var.enable_autoscaling
  min_capacity                 = var.min_capacity
  max_capacity                 = var.max_capacity
  autoscaling_cpu_threshold    = var.autoscaling_cpu_threshold
  autoscaling_memory_threshold = var.autoscaling_memory_threshold
  domain_name                  = var.domain_name
  certificate_arn              = var.certificate_arn

  # K8s-specific
  create_cluster       = var.create_cluster
  kubernetes_namespace = var.kubernetes_namespace
  node_count           = var.node_count
  node_machine_type    = var.node_machine_type
  volume_size_gb       = var.volume_size_gb

  # Cloud-specific
  gcp_project_id           = var.gcp_project_id
  azure_resource_group_name = var.azure_resource_group_name

  # Generic K8s
  storage_class_name  = var.storage_class_name
  ingress_class_name  = var.ingress_class_name
  ingress_annotations = var.ingress_annotations
}
