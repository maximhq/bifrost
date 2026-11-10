# =============================================================================
# ACI container group wiring, asserted on the service module directly: the
# managed environment and secure environment reach the container, and
# registry credentials reach the container group.
# =============================================================================

mock_provider "azurerm" {}

variables {
  name_prefix         = "test"
  region              = "eastus"
  resource_group_name = "test-rg"
  tags                = {}
  config_json         = "{}"
  image               = "maximhq/bifrost:latest"
  container_port      = 8080
  health_check_path   = "/health"
  desired_count       = 1
  cpu                 = 1000
  memory              = 2048
  identity_id         = "/subscriptions/0/resourceGroups/test-rg/providers/Microsoft.ManagedIdentity/userAssignedIdentities/test"
}

run "managed_environment_reaches_the_container" {
  command = plan
  module { source = "./azure/services/aci" }
  variables {
    extra_environment = {
      PG_HOST = "test-db.postgres.database.azure.com"
      PG_PORT = "5432"
    }
    extra_secure_environment = {
      PG_PASSWORD            = "db-secret"
      BIFROST_ENCRYPTION_KEY = "enc-secret"
    }
  }

  assert {
    condition     = azurerm_container_group.bifrost.container[0].environment_variables["PG_HOST"] == "test-db.postgres.database.azure.com"
    error_message = "PG_HOST must be set on the planned container"
  }
  assert {
    condition     = contains(keys(azurerm_container_group.bifrost.container[0].secure_environment_variables), "PG_PASSWORD") && contains(keys(azurerm_container_group.bifrost.container[0].secure_environment_variables), "BIFROST_ENCRYPTION_KEY") && contains(keys(azurerm_container_group.bifrost.container[0].secure_environment_variables), "BIFROST_CONFIG")
    error_message = "PG_PASSWORD, BIFROST_ENCRYPTION_KEY and BIFROST_CONFIG must be secure environment variables on the planned container"
  }
  assert {
    condition     = length(azurerm_container_group.bifrost.image_registry_credential) == 0
    error_message = "no registry credential block without credentials"
  }
}

run "registry_credentials_reach_the_container_group" {
  command = plan
  module { source = "./azure/services/aci" }
  variables {
    image_registry_credentials = [{
      server   = "registry.example.com"
      username = "pull-user"
      password = "pull-secret"
    }]
  }

  assert {
    condition     = length(azurerm_container_group.bifrost.image_registry_credential) == 1 && azurerm_container_group.bifrost.image_registry_credential[0].server == "registry.example.com"
    error_message = "registry credentials must become an image_registry_credential block on the container group"
  }
}

# A registry server without a username or password would pass planning and fail
# only when the container group pulls, so the variable rejects partial entries.
run "registry_credentials_must_be_complete" {
  command = plan
  module { source = "./azure/services/aci" }
  variables {
    image_registry_credentials = [{ server = "myorg.azurecr.io", username = null, password = null }]
  }
  expect_failures = [var.image_registry_credentials]
}
