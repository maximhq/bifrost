# =============================================================================
# Azure Platform Module - Shared Infrastructure
# =============================================================================

terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = ">= 3.0"
    }
  }
}

data "azurerm_client_config" "current" {}

locals {
  # Sanitize name_prefix for resources that require alphanumeric-only names
  name_prefix_clean = replace(var.name_prefix, "-", "")

  # Validate: existing VPC and subnets must be provided together
  _validate_vpc_subnet = (
    (var.existing_vpc_id == null) == (var.existing_subnet_ids == null)
  )

  # Resolve resource group: use existing or create new
  resource_group_name = var.resource_group_name != null ? var.resource_group_name : azurerm_resource_group.this[0].name
  resource_group_id   = var.resource_group_name != null ? data.azurerm_resource_group.existing[0].id : azurerm_resource_group.this[0].id

  # Resolve networking: use existing or create new
  vnet_id    = var.existing_vpc_id != null ? var.existing_vpc_id : azurerm_virtual_network.this[0].id
  subnet_ids = var.existing_subnet_ids != null ? var.existing_subnet_ids : [azurerm_subnet.this[0].id]
}

# =============================================================================
# Resource Group
# =============================================================================

data "azurerm_resource_group" "existing" {
  count = var.resource_group_name != null ? 1 : 0
  name  = var.resource_group_name
}

resource "azurerm_resource_group" "this" {
  count    = var.resource_group_name == null ? 1 : 0
  name     = "${var.name_prefix}-rg"
  location = var.region
  tags     = var.tags
}

# =============================================================================
# Virtual Network (optional - skip if existing_vpc_id provided)
# =============================================================================

resource "azurerm_virtual_network" "this" {
  count               = var.existing_vpc_id == null ? 1 : 0
  name                = "${var.name_prefix}-vnet"
  location            = var.region
  resource_group_name = local.resource_group_name
  address_space       = ["10.0.0.0/16"]
  tags                = var.tags
}

resource "azurerm_subnet" "this" {
  count                = var.existing_vpc_id == null ? 1 : 0
  name                 = "${var.name_prefix}-subnet"
  resource_group_name  = local.resource_group_name
  virtual_network_name = azurerm_virtual_network.this[0].name
  address_prefixes     = ["10.0.1.0/24"]
  service_endpoints    = ["Microsoft.KeyVault"]
}

# =============================================================================
# Network Security Group
# =============================================================================

resource "azurerm_network_security_group" "this" {
  name                = "${var.name_prefix}-nsg"
  location            = var.region
  resource_group_name = local.resource_group_name
  tags                = var.tags

  security_rule {
    name                       = "allow-bifrost-inbound"
    priority                   = 100
    direction                  = "Inbound"
    access                     = "Allow"
    protocol                   = "Tcp"
    source_port_range          = "*"
    destination_port_range     = tostring(var.container_port)
    source_address_prefix      = var.allowed_cidr
    destination_address_prefix = "*"
  }
}

resource "azurerm_subnet_network_security_group_association" "this" {
  count                     = var.existing_vpc_id == null ? 1 : 0
  subnet_id                 = azurerm_subnet.this[0].id
  network_security_group_id = azurerm_network_security_group.this.id
}

# =============================================================================
# Key Vault for config storage
# =============================================================================

resource "azurerm_key_vault" "this" {
  name                       = "${local.name_prefix_clean}kv"
  location                   = var.region
  resource_group_name        = local.resource_group_name
  tenant_id                  = data.azurerm_client_config.current.tenant_id
  sku_name                   = "standard"
  soft_delete_retention_days = 7
  purge_protection_enabled   = false
  tags                       = var.tags

  network_acls {
    default_action             = "Deny"
    bypass                     = "AzureServices"
    virtual_network_subnet_ids = local.subnet_ids
  }

  # Access policy for the current Terraform principal
  access_policy {
    tenant_id = data.azurerm_client_config.current.tenant_id
    object_id = data.azurerm_client_config.current.object_id

    secret_permissions = [
      "Get",
      "List",
      "Set",
      "Delete",
      "Purge",
    ]
  }

  # Access policy for the managed identity
  access_policy {
    tenant_id = data.azurerm_client_config.current.tenant_id
    object_id = azurerm_user_assigned_identity.this.principal_id

    secret_permissions = [
      "Get",
      "List",
    ]
  }
}

resource "azurerm_key_vault_secret" "config" {
  name         = "${var.name_prefix}-config"
  value        = var.config_json
  key_vault_id = azurerm_key_vault.this.id
}

# =============================================================================
# User Assigned Identity
# =============================================================================

resource "azurerm_user_assigned_identity" "this" {
  name                = "${var.name_prefix}-identity"
  location            = var.region
  resource_group_name = local.resource_group_name
  tags                = var.tags
}

# =============================================================================
# Service Modules
# =============================================================================

# --- AKS (Azure Kubernetes Service) ---
module "aks" {
  source = "./services/aks"
  count  = var.service == "aks" ? 1 : 0

  name_prefix                  = var.name_prefix
  region                       = var.region
  resource_group_name          = local.resource_group_name
  tags                         = var.tags
  config_json                  = var.config_json
  image                        = var.image
  container_port               = var.container_port
  health_check_path            = var.health_check_path
  desired_count                = var.desired_count
  cpu                          = var.cpu
  memory                       = var.memory
  subnet_ids                   = local.subnet_ids
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
  identity_id                  = azurerm_user_assigned_identity.this.id
}

# --- ACI (Azure Container Instances) ---
module "aci" {
  source = "./services/aci"
  count  = var.service == "aci" ? 1 : 0

  name_prefix         = var.name_prefix
  region              = var.region
  resource_group_name = local.resource_group_name
  tags                = var.tags
  config_json         = var.config_json
  image               = var.image
  container_port      = var.container_port
  health_check_path   = var.health_check_path
  desired_count       = var.desired_count
  cpu                 = var.cpu
  memory              = var.memory
  subnet_ids          = local.subnet_ids
  identity_id         = azurerm_user_assigned_identity.this.id
}
