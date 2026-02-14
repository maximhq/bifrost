terraform {
  required_version = ">= 1.3"
}

locals {
  # Load base config from file or inline string (decoded to map)
  base_config = (
    var.config_json_file != null ? jsondecode(file(var.config_json_file)) :
    var.config_json != null ? jsondecode(var.config_json) :
    {}
  )

  # Terraform variable overrides (non-null values only)
  overrides = {
    for k, v in {
      "$schema"            = "https://www.getbifrost.ai/schema"
      encryption_key       = var.encryption_key
      auth_config          = var.auth_config
      client               = var.client
      framework            = var.framework
      providers            = var.providers_config
      governance           = var.governance
      mcp                  = var.mcp
      vector_store         = var.vector_store
      config_store         = var.config_store
      logs_store           = var.logs_store
      cluster_config       = var.cluster_config
      saml_config          = var.saml_config
      load_balancer_config = var.load_balancer_config
      guardrails_config    = var.guardrails_config
      plugins              = var.plugins
      audit_logs           = var.audit_logs
    } : k => v if v != null
  }

  # Merge: base config + overrides (overrides win at top-level key)
  config_json = jsonencode(merge(local.base_config, local.overrides))

  image             = "${var.image_repository}:${var.image_tag}"
  container_port    = 8080
  health_check_path = "/health"
}

# --- AWS ---
module "aws" {
  source = "./aws"
  count  = var.cloud_provider == "aws" ? 1 : 0

  service                      = var.service
  config_json                  = local.config_json
  image                        = local.image
  container_port               = local.container_port
  health_check_path            = local.health_check_path
  region                       = var.region
  name_prefix                  = var.name_prefix
  tags                         = var.tags
  desired_count                = var.desired_count
  cpu                          = var.cpu
  memory                       = var.memory
  existing_vpc_id              = var.existing_vpc_id
  existing_subnet_ids          = var.existing_subnet_ids
  allowed_cidr                 = var.allowed_cidr
  existing_security_group_ids  = var.existing_security_group_ids
  create_load_balancer         = var.create_load_balancer
  enable_autoscaling           = var.enable_autoscaling
  min_capacity                 = var.min_capacity
  max_capacity                 = var.max_capacity
  autoscaling_cpu_threshold    = var.autoscaling_cpu_threshold
  autoscaling_memory_threshold = var.autoscaling_memory_threshold
  domain_name                  = var.domain_name
  create_cluster               = var.create_cluster
  kubernetes_namespace         = var.kubernetes_namespace
  node_count                   = var.node_count
  node_machine_type            = var.node_machine_type
  volume_size_gb               = var.volume_size_gb
}

# --- GCP ---
module "gcp" {
  source = "./gcp"
  count  = var.cloud_provider == "gcp" ? 1 : 0

  service                      = var.service
  config_json                  = local.config_json
  image                        = local.image
  container_port               = local.container_port
  health_check_path            = local.health_check_path
  project_id                   = var.gcp_project_id
  region                       = var.region
  name_prefix                  = var.name_prefix
  tags                         = var.tags
  desired_count                = var.desired_count
  cpu                          = var.cpu
  memory                       = var.memory
  allowed_cidr                 = var.allowed_cidr
  existing_vpc_id              = var.existing_vpc_id
  existing_subnet_ids          = var.existing_subnet_ids
  create_load_balancer         = var.create_load_balancer
  enable_autoscaling           = var.enable_autoscaling
  min_capacity                 = var.min_capacity
  max_capacity                 = var.max_capacity
  autoscaling_cpu_threshold    = var.autoscaling_cpu_threshold
  autoscaling_memory_threshold = var.autoscaling_memory_threshold
  domain_name                  = var.domain_name
  create_cluster               = var.create_cluster
  kubernetes_namespace         = var.kubernetes_namespace
  node_count                   = var.node_count
  node_machine_type            = var.node_machine_type
  volume_size_gb               = var.volume_size_gb
}

# --- Azure ---
module "azure" {
  source = "./azure"
  count  = var.cloud_provider == "azure" ? 1 : 0

  service                      = var.service
  config_json                  = local.config_json
  image                        = local.image
  container_port               = local.container_port
  health_check_path            = local.health_check_path
  region                       = var.region
  name_prefix                  = var.name_prefix
  tags                         = var.tags
  desired_count                = var.desired_count
  cpu                          = var.cpu
  memory                       = var.memory
  allowed_cidr                 = var.allowed_cidr
  existing_vpc_id              = var.existing_vpc_id
  existing_subnet_ids          = var.existing_subnet_ids
  create_load_balancer         = var.create_load_balancer
  enable_autoscaling           = var.enable_autoscaling
  min_capacity                 = var.min_capacity
  max_capacity                 = var.max_capacity
  autoscaling_cpu_threshold    = var.autoscaling_cpu_threshold
  autoscaling_memory_threshold = var.autoscaling_memory_threshold
  domain_name                  = var.domain_name
  create_cluster               = var.create_cluster
  kubernetes_namespace         = var.kubernetes_namespace
  node_count                   = var.node_count
  node_machine_type            = var.node_machine_type
  volume_size_gb               = var.volume_size_gb
  resource_group_name          = var.azure_resource_group_name
}

# --- Generic Kubernetes ---
module "kubernetes" {
  source = "./kubernetes"
  count  = var.cloud_provider == "kubernetes" ? 1 : 0

  service_name                 = var.name_prefix
  config_json                  = local.config_json
  image                        = local.image
  container_port               = local.container_port
  health_check_path            = local.health_check_path
  name_prefix                  = var.name_prefix
  tags                         = var.tags
  desired_count                = var.desired_count
  cpu                          = var.cpu
  memory                       = var.memory
  kubernetes_namespace         = var.kubernetes_namespace
  volume_size_gb               = var.volume_size_gb
  create_load_balancer         = var.create_load_balancer
  enable_autoscaling           = var.enable_autoscaling
  min_capacity                 = var.min_capacity
  max_capacity                 = var.max_capacity
  autoscaling_cpu_threshold    = var.autoscaling_cpu_threshold
  autoscaling_memory_threshold = var.autoscaling_memory_threshold
  domain_name                  = var.domain_name
  storage_class_name           = var.storage_class_name
  ingress_class_name           = var.ingress_class_name
  ingress_annotations          = var.ingress_annotations
}
