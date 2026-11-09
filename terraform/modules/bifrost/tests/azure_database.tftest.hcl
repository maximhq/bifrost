# =============================================================================
# Azure managed database and Key Vault settings, asserted on the platform
# module directly (through the root module they sit in a nested module).
# =============================================================================

mock_provider "azurerm" {
  mock_data "azurerm_client_config" {
    defaults = {
      tenant_id       = "00000000-0000-0000-0000-000000000000"
      subscription_id = "00000000-0000-0000-0000-000000000000"
      object_id       = "00000000-0000-0000-0000-000000000000"
    }
  }
}
mock_provider "kubernetes" {}
mock_provider "random" {}

variables {
  service                      = "aci"
  config_json                  = "{}"
  image                        = "maximhq/bifrost:latest"
  container_port               = 8080
  health_check_path            = "/health"
  region                       = "eastus"
  name_prefix                  = "test"
  tags                         = {}
  desired_count                = 1
  cpu                          = 1000
  memory                       = 2048
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
  create_database              = true
}

run "database_defaults" {
  command = plan
  module { source = "./azure" }

  assert {
    condition     = length(azurerm_postgresql_flexible_server.bifrost) == 1
    error_message = "create_database = true must plan exactly one Flexible Server"
  }
  assert {
    condition     = length(azurerm_postgresql_flexible_server_database.bifrost) == 1 && length(azurerm_postgresql_flexible_server_firewall_rule.azure_services) == 1
    error_message = "create_database = true must plan the database and the firewall rule on that server"
  }
  assert {
    condition     = azurerm_postgresql_flexible_server.bifrost[0].version == "16"
    error_message = "the managed server must be PostgreSQL 16"
  }
  assert {
    condition     = azurerm_postgresql_flexible_server.bifrost[0].storage_mb == 32768
    error_message = "the default 32 GB must map to the 32768 MB tier, got ${azurerm_postgresql_flexible_server.bifrost[0].storage_mb}"
  }
  assert {
    condition     = azurerm_postgresql_flexible_server_firewall_rule.azure_services[0].start_ip_address == "0.0.0.0" && azurerm_postgresql_flexible_server_firewall_rule.azure_services[0].end_ip_address == "0.0.0.0"
    error_message = "the firewall rule must be Azure's allow-Azure-services marker (0.0.0.0 to 0.0.0.0)"
  }
  assert {
    condition     = length(azurerm_management_lock.database) == 0
    error_message = "no delete lock without database_deletion_protection"
  }
  assert {
    condition     = one(azurerm_key_vault.this.network_acls).default_action == "Deny"
    error_message = "the Key Vault network ACL must default to Deny"
  }
}

run "database_absent_when_disabled" {
  command = plan
  module { source = "./azure" }
  variables {
    create_database              = false
    database_deletion_protection = true
  }

  assert {
    condition     = length(azurerm_postgresql_flexible_server.bifrost) == 0
    error_message = "no Flexible Server may be planned when create_database is false"
  }
  assert {
    condition     = length(azurerm_postgresql_flexible_server_database.bifrost) == 0
    error_message = "no database may be planned when create_database is false"
  }
  assert {
    condition     = length(azurerm_postgresql_flexible_server_firewall_rule.azure_services) == 0
    error_message = "no firewall rule may be planned when create_database is false"
  }
  assert {
    condition     = length(azurerm_management_lock.database) == 0
    error_message = "no delete lock may be planned when create_database is false, even with deletion protection on"
  }
  assert {
    condition     = !contains(module.aci[0].environment_keys, "PG_HOST") && !contains(module.aci[0].secure_environment_keys, "PG_PASSWORD")
    error_message = "the planned container must carry no managed database settings when create_database is false, got ${jsonencode(module.aci[0].environment_keys)} / ${jsonencode(module.aci[0].secure_environment_keys)}"
  }
}

# The managed values must reach the planned container through the platform
# module, not only when the service module is fed directly.
run "managed_values_reach_the_aci_container" {
  command = plan
  module { source = "./azure" }
  variables {
    generate_secrets = true
  }

  assert {
    condition     = alltrue([for k in ["PG_HOST", "PG_PORT", "PG_USER", "PG_DATABASE", "PG_SSL_MODE"] : contains(module.aci[0].environment_keys, k)])
    error_message = "the managed database settings must reach the planned container as environment variables, got ${jsonencode(module.aci[0].environment_keys)}"
  }
  assert {
    condition     = alltrue([for k in ["PG_PASSWORD", "BIFROST_ENCRYPTION_KEY", "BIFROST_SETUP_TOKEN"] : contains(module.aci[0].secure_environment_keys, k)])
    error_message = "the database password and generated secrets must reach the planned container as secure environment variables, got ${jsonencode(module.aci[0].secure_environment_keys)}"
  }
}

run "database_storage_rounds_up_to_a_tier" {
  command = plan
  module { source = "./azure" }
  variables {
    database_storage_gb = 50
  }

  assert {
    condition     = azurerm_postgresql_flexible_server.bifrost[0].storage_mb == 65536
    error_message = "50 GB is not a Flexible Server tier; it must round up to 65536 MB, got ${azurerm_postgresql_flexible_server.bifrost[0].storage_mb}"
  }
}

run "database_small_storage_raised_to_minimum" {
  command = plan
  module { source = "./azure" }
  variables {
    database_storage_gb = 10
  }

  assert {
    condition     = azurerm_postgresql_flexible_server.bifrost[0].storage_mb == 32768
    error_message = "values below 32 GB must be raised to the 32768 MB minimum"
  }
}

run "database_lock_with_deletion_protection" {
  command = plan
  module { source = "./azure" }
  variables {
    database_deletion_protection = true
  }

  assert {
    condition     = length(azurerm_management_lock.database) == 1 && azurerm_management_lock.database[0].lock_level == "CanNotDelete"
    error_message = "database_deletion_protection must add a CanNotDelete lock on the server"
  }
}

run "key_vault_allow" {
  command = plan
  module { source = "./azure" }
  variables {
    key_vault_network_default_action = "Allow"
  }

  assert {
    condition     = one(azurerm_key_vault.this.network_acls).default_action == "Allow"
    error_message = "key_vault_network_default_action = Allow must reach the Key Vault ACL"
  }
}

run "key_vault_rejects_other_values" {
  command = plan
  module { source = "./azure" }
  variables {
    key_vault_network_default_action = "Open"
  }
  expect_failures = [var.key_vault_network_default_action]
}
