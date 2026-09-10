# =============================================================================
# Azure Database for PostgreSQL Flexible Server
# =============================================================================

terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = ">= 3.0"
    }
    random = {
      source  = "hashicorp/random"
      version = ">= 3.0"
    }
  }
}

locals {
  name_prefix_clean = replace(var.name_prefix, "-", "")
  sku_name          = coalesce(var.sku_name, "B_Standard_B1ms")
  password          = var.password != null ? var.password : random_password.master[0].result
  create_subnet     = var.subnet_id == null
}

# --- Random password ---

resource "random_password" "master" {
  count = var.password == null ? 1 : 0

  length           = 32
  special          = true
  override_special = "!#$%&*()-_=+[]<>:?"
}

# --- Delegated Subnet for PostgreSQL (if not provided) ---

data "azurerm_virtual_network" "this" {
  name                = split("/", var.vnet_id)[8]
  resource_group_name = var.resource_group_name
}

resource "azurerm_subnet" "postgresql" {
  count = local.create_subnet ? 1 : 0

  name                 = "${var.name_prefix}-pg-subnet"
  resource_group_name  = var.resource_group_name
  virtual_network_name = data.azurerm_virtual_network.this.name
  address_prefixes     = ["10.0.2.0/24"]

  delegation {
    name = "postgresql-delegation"

    service_delegation {
      name = "Microsoft.DBforPostgreSQL/flexibleServers"
      actions = [
        "Microsoft.Network/virtualNetworks/subnets/join/action",
      ]
    }
  }
}

# --- Private DNS Zone ---

resource "azurerm_private_dns_zone" "postgresql" {
  name                = "${local.name_prefix_clean}.postgres.database.azure.com"
  resource_group_name = var.resource_group_name
  tags                = var.tags
}

resource "azurerm_private_dns_zone_virtual_network_link" "postgresql" {
  name                  = "${var.name_prefix}-pg-dns-link"
  resource_group_name   = var.resource_group_name
  private_dns_zone_name = azurerm_private_dns_zone.postgresql.name
  virtual_network_id    = var.vnet_id
  tags                  = var.tags
}

# --- Flexible Server ---

resource "azurerm_postgresql_flexible_server" "this" {
  name                          = "${var.name_prefix}-pg"
  resource_group_name           = var.resource_group_name
  location                      = var.region
  version                       = var.engine_version
  sku_name                      = local.sku_name
  storage_mb                    = var.storage_mb
  administrator_login           = var.username
  administrator_password        = local.password
  backup_retention_days         = var.backup_retention_days
  delegated_subnet_id           = local.create_subnet ? azurerm_subnet.postgresql[0].id : var.subnet_id
  private_dns_zone_id           = azurerm_private_dns_zone.postgresql.id
  public_network_access_enabled = false

  dynamic "high_availability" {
    for_each = var.high_availability ? [1] : []
    content {
      mode = "ZoneRedundant"
    }
  }

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-pg"
  })

  depends_on = [azurerm_private_dns_zone_virtual_network_link.postgresql]
}

# --- Database ---

resource "azurerm_postgresql_flexible_server_database" "this" {
  name      = var.database_name
  server_id = azurerm_postgresql_flexible_server.this.id
  charset   = "UTF8"
  collation = "en_US.utf8"
}
