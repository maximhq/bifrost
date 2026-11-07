# =============================================================================
# Network input pairing: existing_vpc_id and existing_subnet_ids must be given
# together. These runs target the platform modules directly because
# expect_failures cannot name a resource inside a nested module.
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
mock_provider "random" {}
mock_provider "google" {}
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

variables {
  service                      = "cloud-run"
  config_json                  = "{}"
  image                        = "maximhq/bifrost:latest"
  container_port               = 8080
  health_check_path            = "/health"
  region                       = "us-central1"
  name_prefix                  = "test"
  tags                         = {}
  desired_count                = 1
  cpu                          = 1000
  memory                       = 2048
  create_load_balancer         = false
  enable_autoscaling           = false
  min_capacity                 = 1
  max_capacity                 = 3
  autoscaling_cpu_threshold    = 70
  autoscaling_memory_threshold = 80
  create_cluster               = true
  kubernetes_namespace         = "bifrost"
  node_count                   = 1
  volume_size_gb               = 10
}

run "gcp_rejects_vpc_without_subnets" {
  command = plan
  module { source = "./gcp" }
  variables {
    project_id          = "test-project"
    existing_vpc_id     = "projects/test-project/global/networks/shared"
    existing_subnet_ids = null
  }
  expect_failures = [terraform_data.validate_network_inputs]
}

run "gcp_rejects_subnets_without_vpc" {
  command = plan
  module { source = "./gcp" }
  variables {
    project_id          = "test-project"
    existing_vpc_id     = null
    existing_subnet_ids = ["projects/test-project/regions/us-central1/subnetworks/shared"]
  }
  expect_failures = [terraform_data.validate_network_inputs]
}

run "gcp_accepts_vpc_with_subnets" {
  command = plan
  module { source = "./gcp" }
  variables {
    project_id          = "test-project"
    existing_vpc_id     = "projects/test-project/global/networks/shared"
    existing_subnet_ids = ["projects/test-project/regions/us-central1/subnetworks/shared"]
  }
}

run "azure_rejects_vpc_without_subnets" {
  command = plan
  module { source = "./azure" }
  variables {
    service             = "aci"
    region              = "eastus"
    existing_vpc_id     = "/subscriptions/0/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/shared"
    existing_subnet_ids = null
  }
  expect_failures = [terraform_data.validate_network_inputs]
}

run "azure_rejects_subnets_without_vpc" {
  command = plan
  module { source = "./azure" }
  variables {
    service             = "aci"
    region              = "eastus"
    existing_vpc_id     = null
    existing_subnet_ids = ["/subscriptions/0/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/shared/subnets/one"]
  }
  expect_failures = [terraform_data.validate_network_inputs]
}

run "azure_aks_rejects_vpc_without_subnets" {
  command = plan
  module { source = "./azure" }
  variables {
    service             = "aks"
    region              = "eastus"
    existing_vpc_id     = "/subscriptions/0/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/shared"
    existing_subnet_ids = null
  }
  expect_failures = [terraform_data.validate_network_inputs]
}

run "azure_rejects_empty_subnet_list" {
  command = plan
  module { source = "./azure" }
  variables {
    service             = "aci"
    region              = "eastus"
    existing_vpc_id     = "/subscriptions/0/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/shared"
    existing_subnet_ids = []
  }
  expect_failures = [var.existing_subnet_ids]
}

run "aws_rejects_empty_subnet_list" {
  command = plan
  module { source = "./aws" }
  variables {
    service              = "ecs"
    region               = "us-east-1"
    create_load_balancer = false
    existing_vpc_id      = "vpc-12345"
    existing_subnet_ids  = []
  }
  expect_failures = [var.existing_subnet_ids]
}
