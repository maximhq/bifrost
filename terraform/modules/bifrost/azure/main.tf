# =============================================================================
# Azure Platform Module - Shared Infrastructure
# =============================================================================

terraform {
  required_providers {
    azurerm = {
      source = "hashicorp/azurerm"
      # 5.0 removed skip_provider_registration (used by the root module's
      # default provider block, deprecated but accepted through 4.x) and
      # azurerm_subnet.service_endpoints. A fresh init without this cap
      # resolves 5.x and fails validation.
      version = ">= 3.0, < 5.0"
    }
  }
}

data "azurerm_client_config" "current" {}

locals {
  # Sanitize name_prefix for resources that require alphanumeric-only names
  name_prefix_clean = replace(var.name_prefix, "-", "")

  # Resolve resource group: use existing or create new
  resource_group_name = var.resource_group_name != null ? var.resource_group_name : azurerm_resource_group.this[0].name
  resource_group_id   = var.resource_group_name != null ? data.azurerm_resource_group.existing[0].id : azurerm_resource_group.this[0].id

  # Resolve networking: use existing or create new
  vnet_id = var.existing_vpc_id != null ? var.existing_vpc_id : azurerm_virtual_network.this[0].id
  # Splat rather than [0]: with a mismatched pair the created subnet has count 0,
  # and the precondition below must be the error the plan reports.
  subnet_ids = var.existing_subnet_ids != null ? var.existing_subnet_ids : azurerm_subnet.this[*].id
}

# existing_vpc_id and existing_subnet_ids must arrive together; otherwise the
# plan either indexes a subnet that is never created or builds a VNet the given
# subnets do not belong to.
resource "terraform_data" "validate_network_inputs" {
  lifecycle {
    precondition {
      condition     = (var.existing_vpc_id == null) == (var.existing_subnet_ids == null)
      error_message = "existing_vpc_id and existing_subnet_ids must be set together: pass both to reuse a network, or neither to create one."
    }
  }
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
    default_action             = var.key_vault_network_default_action
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
  ingress_path                 = var.ingress_path
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

  # Managed database and generated secrets
  extra_environment        = local.managed_environment
  extra_secure_environment = local.managed_secure_environment

  # Private registry (Bifrost Enterprise)
  image_registry_credentials = var.image_registry_credentials

  depends_on = [
    azurerm_postgresql_flexible_server_database.bifrost,
    azurerm_postgresql_flexible_server_firewall_rule.azure_services,
  ]
}

# =============================================================================
# Managed PostgreSQL (Flexible Server) and generated runtime secrets (ACI only)
#
# The one-click Azure path provisions these so a fresh subscription gets a
# durable config store, a durable log store, a stable encryption key and a
# setup token. ACI has no secret-store reference mechanism, so the values are
# passed as secure environment variables (encrypted at rest, hidden from the
# portal and the API) and the setup token is also exposed as a sensitive
# output.
# =============================================================================

locals {
  is_aci           = var.service == "aci"
  manage_database  = local.is_aci && var.create_database
  generate_secrets = local.is_aci && var.generate_secrets

  database_sku = coalesce(var.database_instance_class, "B_Standard_B1ms")
  # Flexible Server accepts only these storage sizes (MB); round the request up
  # to the next tier. The variable validation caps the input at the last tier.
  database_storage_tiers_mb = [32768, 65536, 131072, 262144, 524288, 1048576, 2097152, 4193280, 4194304, 8388608, 16777216, 33553408]
  database_storage_mb       = [for tier in local.database_storage_tiers_mb : tier if tier >= var.database_storage_gb * 1024][0]

  managed_environment = local.manage_database ? {
    PG_HOST     = azurerm_postgresql_flexible_server.bifrost[0].fqdn
    PG_PORT     = "5432"
    PG_USER     = var.database_user
    PG_DATABASE = var.database_name
    PG_SSL_MODE = "require"
  } : {}

  managed_secure_environment = merge(
    local.manage_database ? { PG_PASSWORD = random_password.database[0].result } : {},
    local.generate_secrets ? {
      BIFROST_ENCRYPTION_KEY = random_password.encryption_key[0].result
      BIFROST_SETUP_TOKEN    = random_password.setup_token[0].result
    } : {},
  )
}

resource "random_password" "database" {
  count = local.manage_database ? 1 : 0

  length  = 32
  special = false
}

resource "random_password" "encryption_key" {
  count = local.generate_secrets ? 1 : 0

  length  = 48
  special = false
}

resource "random_password" "setup_token" {
  count = local.generate_secrets ? 1 : 0

  length  = 40
  special = false
}

# Flexible Server names are global and lowercase; the suffix keeps a destroyed
# stack recreatable without a name clash.
resource "random_id" "database_suffix" {
  count = local.manage_database ? 1 : 0

  byte_length = 2
}

resource "azurerm_postgresql_flexible_server" "bifrost" {
  count = local.manage_database ? 1 : 0

  name                = lower("${var.name_prefix}-db-${random_id.database_suffix[0].hex}")
  location            = var.region
  resource_group_name = local.resource_group_name
  version             = "16"

  sku_name   = local.database_sku
  storage_mb = local.database_storage_mb

  administrator_login    = var.database_user
  administrator_password = random_password.database[0].result

  # Public endpoint with TLS enforced by the server. The firewall rule below is
  # Azure's "allow Azure services" marker: it admits traffic from any
  # Azure-hosted source, not only this deployment, which is what lets the
  # container group (no static egress address) reach the server. Credentials
  # and TLS are the access control here; see the deployment guide.
  public_network_access_enabled = true
  backup_retention_days         = 7
  auto_grow_enabled             = true

  tags = var.tags

  lifecycle {
    # Azure may pin a zone after creation; do not fight it on later applies.
    ignore_changes = [zone]
  }
}

resource "azurerm_postgresql_flexible_server_database" "bifrost" {
  count = local.manage_database ? 1 : 0

  name      = var.database_name
  server_id = azurerm_postgresql_flexible_server.bifrost[0].id
  charset   = "UTF8"
  collation = "en_US.utf8"
}

# 0.0.0.0 to 0.0.0.0 is Azure's convention for "allow public access from any
# Azure service within Azure to this server". It is broader than this
# deployment's network (other subscriptions' Azure resources match too) and is
# what lets the container group, which has no fixed egress address, connect.
resource "azurerm_postgresql_flexible_server_firewall_rule" "azure_services" {
  count = local.manage_database ? 1 : 0

  name             = "allow-azure-services"
  server_id        = azurerm_postgresql_flexible_server.bifrost[0].id
  start_ip_address = "0.0.0.0"
  end_ip_address   = "0.0.0.0"
}

# terraform destroy refuses to remove the server while this lock exists.
resource "azurerm_management_lock" "database" {
  count = local.manage_database && var.database_deletion_protection ? 1 : 0

  name       = "${var.name_prefix}-db-delete-lock"
  scope      = azurerm_postgresql_flexible_server.bifrost[0].id
  lock_level = "CanNotDelete"
  notes      = "Bifrost database_deletion_protection"
}
