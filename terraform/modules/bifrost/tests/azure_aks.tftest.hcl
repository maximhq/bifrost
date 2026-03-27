# =============================================================================
# Azure AKS Module Tests
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

run "aks_basic" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "azure"
    service        = "aks"
    region         = "eastus"
    config_json    = jsonencode({})
  }
}

run "aks_create_cluster" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "azure"
    service        = "aks"
    region         = "eastus"
    config_json    = jsonencode({})
    create_cluster = true
  }
}

run "aks_skip_cluster" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "azure"
    service        = "aks"
    region         = "eastus"
    config_json    = jsonencode({})
    create_cluster = false
  }
}

run "aks_custom_namespace" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider       = "azure"
    service              = "aks"
    region               = "eastus"
    config_json          = jsonencode({})
    kubernetes_namespace = "custom-ns"
  }
}

run "aks_with_hpa" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider     = "azure"
    service            = "aks"
    region             = "eastus"
    config_json        = jsonencode({})
    enable_autoscaling = true
    min_capacity       = 2
    max_capacity       = 10
  }
}

run "aks_with_ingress" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider       = "azure"
    service              = "aks"
    region               = "eastus"
    config_json          = jsonencode({})
    create_load_balancer = true
    domain_name          = "bifrost.example.com"
  }
}

run "aks_custom_nodes" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider    = "azure"
    service           = "aks"
    region            = "eastus"
    config_json       = jsonencode({})
    node_count        = 5
    node_machine_type = "Standard_D4s_v3"
  }
}

run "aks_custom_volume" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "azure"
    service        = "aks"
    region         = "eastus"
    config_json    = jsonencode({})
    volume_size_gb = 50
  }
}

run "aks_existing_resource_group" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider            = "azure"
    service                   = "aks"
    region                    = "eastus"
    config_json               = jsonencode({})
    azure_resource_group_name = "existing-rg"
  }
}

run "aks_existing_vnet" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider      = "azure"
    service             = "aks"
    region              = "eastus"
    config_json         = jsonencode({})
    existing_vpc_id     = "/subscriptions/00000000/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet"
    existing_subnet_ids = ["/subscriptions/00000000/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet/subnets/subnet1"]
  }
}
