# =============================================================================
# Azure ACI Module Tests
# =============================================================================

mock_provider "aws" {
  mock_data "aws_availability_zones" {
    defaults = { names = ["us-east-1a", "us-east-1b", "us-east-1c"] }
  }
  mock_data "aws_caller_identity" {
    defaults = { account_id = "123456789012" }
  }
  mock_data "aws_region" {
    defaults = { name = "us-east-1" }
  }
  mock_data "aws_iam_policy_document" {
    defaults = { json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}" }
  }
}
mock_provider "google" {}
mock_provider "azurerm" {
  mock_data "azurerm_client_config" {
    defaults = {
      tenant_id       = "00000000-0000-0000-0000-000000000000"
      subscription_id = "00000000-0000-0000-0000-000000000000"
      object_id       = "00000000-0000-0000-0000-000000000000"
    }
  }
  mock_data "azurerm_resource_group" {
    defaults = {
      name     = "existing-rg"
      location = "eastus"
    }
  }
}
mock_provider "kubernetes" {}

run "aci_basic" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "azure"
    service        = "aci"
    region         = "eastus"
    config_json    = jsonencode({})
  }
}

run "aci_custom_compute" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "azure"
    service        = "aci"
    region         = "eastus"
    config_json    = jsonencode({})
    cpu            = 1000
    memory         = 2048
  }
}

run "aci_existing_resource_group" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider            = "azure"
    service                   = "aci"
    region                    = "eastus"
    config_json               = jsonencode({})
    azure_resource_group_name = "existing-rg"
  }
}

run "aci_existing_vnet" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider      = "azure"
    service             = "aci"
    region              = "eastus"
    config_json         = jsonencode({})
    existing_vpc_id     = "/subscriptions/00000000/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet"
    existing_subnet_ids = ["/subscriptions/00000000/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet/subnets/subnet1"]
  }
}

run "aci_custom_prefix" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "azure"
    service        = "aci"
    region         = "eastus"
    config_json    = jsonencode({})
    name_prefix    = "my-bifrost"
  }
}
