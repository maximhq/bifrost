# =============================================================================
# Bifrost on Azure: Container Instances + Azure Database for PostgreSQL 16
#
# The one-click Azure stack. Both Bifrost stores live in a Flexible Server,
# the encryption key and setup token are generated into the container group's
# secure environment, and the container group gets a public DNS label.
# deploy/azure/deploy.sh drives this example from Azure Cloud Shell.
#
# This example calls the Azure platform module directly rather than the root
# bifrost module. Terraform configures every provider referenced in a module
# tree, so the root module's plan needs AWS and Google credentials as well;
# the platform module needs only Azure.
# =============================================================================

terraform {
  required_version = ">= 1.7"
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = ">= 3.0, < 5.0"
    }
    random = {
      source  = "hashicorp/random"
      version = ">= 3.0"
    }
    # Pulled in by the (unused, count = 0) AKS sub-module. It needs no
    # configuration and no cluster.
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 2.0"
    }
  }
}

provider "azurerm" {
  features {}
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
  source  = "../../modules/bifrost/azure"
  service = "aci"

  config_json       = local.config_json
  image             = "${var.image_repository}:${var.image_tag}"
  container_port    = 8080
  health_check_path = "/health"

  region              = var.region
  name_prefix         = var.name_prefix
  tags                = var.tags
  resource_group_name = var.resource_group_name

  # Managed PostgreSQL for both stores plus generated runtime secrets.
  create_database              = true
  database_instance_class      = var.database_instance_class
  database_storage_gb          = var.database_storage_gb
  database_deletion_protection = var.database_deletion_protection
  generate_secrets             = true

  # Cloud Shell writes the config secret from outside the service subnet.
  key_vault_network_default_action = "Allow"

  # Private registry for the Enterprise image; empty for the public image.
  image_registry_credentials = var.registry_server == null ? [] : [{
    server   = var.registry_server
    username = var.registry_username
    password = var.registry_password
  }]

  # Compute and exposure
  desired_count = 1
  cpu           = var.cpu
  memory        = var.memory
  allowed_cidr  = var.allowed_cidr

  # Not used by ACI; required by the shared platform interface.
  create_load_balancer         = false
  enable_autoscaling           = false
  min_capacity                 = 1
  max_capacity                 = 1
  autoscaling_cpu_threshold    = 80
  autoscaling_memory_threshold = 80
  create_cluster               = false
  kubernetes_namespace         = "bifrost"
  node_count                   = 0
  volume_size_gb               = 10
}
