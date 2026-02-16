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

  # PostgreSQL auto-wiring: when create_postgresql = true and user hasn't
  # manually specified config_store / logs_store, auto-configure them to use
  # the provisioned PostgreSQL instance.
  pg_host = var.create_postgresql ? coalesce(
    try(module.aws[0].postgresql_endpoint, null),
    try(module.gcp[0].postgresql_endpoint, null),
    try(module.azure[0].postgresql_endpoint, null),
    try(module.kubernetes[0].postgresql_endpoint, null),
  ) : null

  pg_password = var.create_postgresql ? coalesce(
    try(module.aws[0].postgresql_password, null),
    try(module.gcp[0].postgresql_password, null),
    try(module.azure[0].postgresql_password, null),
    try(module.kubernetes[0].postgresql_password, null),
  ) : null

  pg_store_config = var.create_postgresql ? {
    enabled = true
    type    = "postgres"
    config = {
      host           = local.pg_host
      port           = "5432"
      user           = var.postgresql_username
      password       = local.pg_password
      db_name        = var.postgresql_database_name
      ssl_mode       = var.cloud_provider == "kubernetes" ? "disable" : "require"
      max_idle_conns = 5
      max_open_conns = 50
    }
  } : null

  pg_overrides = var.create_postgresql ? merge(
    var.config_store == null ? { config_store = local.pg_store_config } : {},
    var.logs_store == null ? { logs_store = local.pg_store_config } : {},
  ) : {}

  # Merge: base config + user overrides + postgresql auto-config
  config_json = jsonencode(merge(local.base_config, local.overrides, local.pg_overrides))

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

  # PostgreSQL
  create_postgresql                = var.create_postgresql
  postgresql_engine_version        = var.postgresql_engine_version
  postgresql_instance_class        = var.postgresql_instance_class
  postgresql_storage_gb            = var.postgresql_storage_gb
  postgresql_database_name         = var.postgresql_database_name
  postgresql_username              = var.postgresql_username
  postgresql_password              = var.postgresql_password
  postgresql_backup_retention_days = var.postgresql_backup_retention_days
  postgresql_multi_az              = var.postgresql_multi_az
  postgresql_publicly_accessible   = var.postgresql_publicly_accessible
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

  # PostgreSQL
  create_postgresql                = var.create_postgresql
  postgresql_engine_version        = var.postgresql_engine_version
  postgresql_instance_class        = var.postgresql_instance_class
  postgresql_storage_gb            = var.postgresql_storage_gb
  postgresql_database_name         = var.postgresql_database_name
  postgresql_username              = var.postgresql_username
  postgresql_password              = var.postgresql_password
  postgresql_backup_retention_days = var.postgresql_backup_retention_days
  postgresql_multi_az              = var.postgresql_multi_az
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

  # PostgreSQL
  create_postgresql                = var.create_postgresql
  postgresql_engine_version        = var.postgresql_engine_version
  postgresql_instance_class        = var.postgresql_instance_class
  postgresql_storage_gb            = var.postgresql_storage_gb
  postgresql_database_name         = var.postgresql_database_name
  postgresql_username              = var.postgresql_username
  postgresql_password              = var.postgresql_password
  postgresql_backup_retention_days = var.postgresql_backup_retention_days
  postgresql_multi_az              = var.postgresql_multi_az
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

  # PostgreSQL
  create_postgresql         = var.create_postgresql
  postgresql_engine_version = var.postgresql_engine_version
  postgresql_storage_gb     = var.postgresql_storage_gb
  postgresql_database_name  = var.postgresql_database_name
  postgresql_username       = var.postgresql_username
  postgresql_password       = var.postgresql_password
}
