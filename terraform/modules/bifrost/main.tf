terraform {
  required_version = ">= 1.7"
}

# Default provider configuration for azurerm.
# The azurerm provider requires a `features {}` block even when no Azure
# resources are being created (count=0). This default satisfies that
# requirement so AWS/GCP/K8s users don't need to configure azurerm.
#
# This block is a provider configuration of its own, not an inheritance point:
# Terraform does not pass a caller's azurerm settings (subscription_id, tenant,
# auxiliary tenants) into a module that declares its own provider block. Azure
# users select the subscription through the environment (ARM_SUBSCRIPTION_ID,
# `az account set`) or call ./azure directly with a configured provider.
#
# skip_provider_registration is deprecated in azurerm 4.x and removed in 5.0,
# which is why ./azure pins azurerm < 5.0. Without it azurerm registers
# resource providers at configure time, which needs valid Azure credentials
# even for an AWS or GCP plan and breaks the mocked tests.
#
# Terraform configures every provider in this tree during plan, including the
# clouds whose modules have count = 0. A plan therefore needs credentials for
# AWS, GCP and Azure at once. To deploy to one cloud with that cloud's
# credentials only, call its platform module directly (./aws, ./gcp, ./azure,
# ./kubernetes); examples/aws-ecs-postgres and examples/gcp-cloud-run-postgres
# show the pattern.
provider "azurerm" {
  features {}
  skip_provider_registration = true
}

locals {
  # Load base config from file or inline string (decoded to map)
  base_config = (
    var.config_json_file != null ? jsondecode(file(var.config_json_file)) :
    var.config_json != null ? jsondecode(var.config_json) :
    {}
  )

  # Schema precedence: explicit schema_url var > $schema already in base config > public default.
  # Only fall through to the public URL when the base config has no $schema, so a mirrored-schema
  # setup provided via config_json/config_json_file is never silently rewritten.
  schema_url = coalesce(var.schema_url, try(local.base_config["$schema"], null), "https://www.getbifrost.ai/schema")

  # Managed database and generated secrets are only wired for the container
  # services that read PG_* / BIFROST_* from their task environment.
  managed_runtime_supported = contains(["aws/ecs", "gcp/cloud-run", "azure/aci"], "${var.cloud_provider}/${var.service}")

  # Store definition injected when create_database is on. Every value is an
  # env reference resolved inside the container, so the same config.json works
  # for RDS (TLS required) and Cloud SQL over its unix socket (TLS disabled);
  # the platform module decides PG_SSL_MODE.
  managed_store = var.create_database ? {
    enabled = true
    type    = "postgres"
    config = {
      host     = "env.PG_HOST"
      port     = "env.PG_PORT"
      user     = "env.PG_USER"
      password = "env.PG_PASSWORD"
      db_name  = "env.PG_DATABASE"
      ssl_mode = "env.PG_SSL_MODE"
    }
  } : null

  # Managed values fill gaps only: an explicit variable wins, then whatever the
  # base config already sets, then the generated/managed value. Replacing a
  # base-config encryption_key would make existing encrypted data unreadable.
  base_has = { for k in ["encryption_key", "setup_token", "config_store", "logs_store"] : k => contains(keys(local.base_config), k) }

  effective_encryption_key = var.encryption_key != null ? var.encryption_key : (var.generate_secrets && !local.base_has.encryption_key ? "env.BIFROST_ENCRYPTION_KEY" : null)
  effective_setup_token    = var.setup_token != null ? var.setup_token : (var.generate_secrets && !local.base_has.setup_token ? "env.BIFROST_SETUP_TOKEN" : null)
  effective_config_store   = var.config_store != null ? var.config_store : (local.base_has.config_store ? null : local.managed_store)
  effective_logs_store     = var.logs_store != null ? var.logs_store : (local.base_has.logs_store ? null : local.managed_store)

  # Terraform variable overrides (non-null values only)
  overrides = {
    for k, v in {
      "$schema"            = local.schema_url
      encryption_key       = local.effective_encryption_key
      setup_token          = local.effective_setup_token
      auth_config          = var.auth_config
      client               = var.client
      framework            = var.framework
      providers            = var.providers_config
      governance           = var.governance
      mcp                  = var.mcp
      vector_store         = var.vector_store
      config_store         = local.effective_config_store
      logs_store           = local.effective_logs_store
      cluster_config       = var.cluster_config
      scim_config          = var.scim_config
      load_balancer_config = var.load_balancer_config
      guardrails_config    = var.guardrails_config
      plugins              = var.plugins
      audit_logs           = var.audit_logs
      websocket            = var.websocket
      server               = var.server
    } : k => v if v != null
  }

  # Merge: base config + overrides (overrides win at top-level key)
  config_json = jsonencode(merge(local.base_config, local.overrides))

  # Valid cloud_provider → service combinations
  valid_services = {
    aws        = ["ecs", "eks"]
    gcp        = ["gke", "cloud-run"]
    azure      = ["aks", "aci"]
    kubernetes = ["deployment"]
  }

  image             = "${var.image_repository}:${var.image_tag}"
  container_port    = 8080
  health_check_path = "/health"
}

# --- Validate cloud_provider + service combination ---
resource "terraform_data" "validate_service_combination" {
  lifecycle {
    precondition {
      condition     = contains(local.valid_services[var.cloud_provider], var.service)
      error_message = "Invalid service '${var.service}' for cloud_provider '${var.cloud_provider}'. Valid services: ${join(", ", local.valid_services[var.cloud_provider])}."
    }
    precondition {
      condition     = !var.create_database || local.managed_runtime_supported
      error_message = "create_database is supported for aws/ecs, gcp/cloud-run and azure/aci only. Point config_store and logs_store at your own PostgreSQL for '${var.cloud_provider}/${var.service}'."
    }
    precondition {
      condition     = !var.generate_secrets || local.managed_runtime_supported
      error_message = "generate_secrets is supported for aws/ecs, gcp/cloud-run and azure/aci only. Pass encryption_key and setup_token explicitly for '${var.cloud_provider}/${var.service}'."
    }
  }
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
  ingress_path                 = var.ingress_path
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
  assign_public_ip             = var.assign_public_ip
  enable_autoscaling           = var.enable_autoscaling
  min_capacity                 = var.min_capacity
  max_capacity                 = var.max_capacity
  autoscaling_cpu_threshold    = var.autoscaling_cpu_threshold
  autoscaling_memory_threshold = var.autoscaling_memory_threshold
  domain_name                  = var.domain_name
  certificate_arn              = var.certificate_arn
  create_cluster               = var.create_cluster
  kubernetes_namespace         = var.kubernetes_namespace
  node_count                   = var.node_count
  node_machine_type            = var.node_machine_type
  volume_size_gb               = var.volume_size_gb
  create_database              = var.create_database
  database_name                = var.database_name
  database_user                = var.database_user
  database_instance_class      = var.database_instance_class
  database_storage_gb          = var.database_storage_gb
  database_deletion_protection = var.database_deletion_protection
  database_skip_final_snapshot = var.database_skip_final_snapshot
  generate_secrets             = var.generate_secrets
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
  ingress_path                 = var.ingress_path
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
  create_database              = var.create_database
  database_name                = var.database_name
  database_user                = var.database_user
  database_instance_class      = var.database_instance_class
  database_storage_gb          = var.database_storage_gb
  database_deletion_protection = var.database_deletion_protection
  generate_secrets             = var.generate_secrets
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
  ingress_path                 = var.ingress_path
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
  create_database              = var.create_database
  database_name                = var.database_name
  database_user                = var.database_user
  database_instance_class      = var.database_instance_class
  database_storage_gb          = var.database_storage_gb
  database_deletion_protection = var.database_deletion_protection
  generate_secrets             = var.generate_secrets

  # Azure-only settings
  key_vault_network_default_action = var.key_vault_network_default_action
  image_registry_credentials       = var.image_registry_credentials
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
  ingress_path                 = var.ingress_path
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
