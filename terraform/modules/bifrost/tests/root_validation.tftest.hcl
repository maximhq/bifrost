# =============================================================================
# Root Module Validation Tests
#
# Tests variable validation rules and cross-validation of cloud_provider + service.
# Validation failures happen before provider calls, so no mock_provider needed.
# Valid-combination tests use the wrapper module with mock_provider.
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
}
mock_provider "kubernetes" {}

# --- Valid combinations (all 7 services) ---

run "valid_aws_ecs" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "ecs"
    region         = "us-east-1"
    config_json    = jsonencode({})
  }
}

run "valid_aws_eks" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "eks"
    region         = "us-east-1"
    config_json    = jsonencode({})
  }
}

run "valid_gcp_gke" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "gcp"
    service        = "gke"
    region         = "us-central1"
    gcp_project_id = "test-project"
    config_json    = jsonencode({})
  }
}

run "valid_gcp_cloud_run" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "gcp"
    service        = "cloud-run"
    region         = "us-central1"
    gcp_project_id = "test-project"
    config_json    = jsonencode({})
  }
}

run "valid_azure_aks" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "azure"
    service        = "aks"
    region         = "eastus"
    config_json    = jsonencode({})
  }
}

run "valid_azure_aci" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "azure"
    service        = "aci"
    region         = "eastus"
    config_json    = jsonencode({})
  }
}

run "valid_kubernetes_deployment" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "kubernetes"
    service        = "deployment"
    region         = "local"
    config_json    = jsonencode({})
  }
}

# --- Invalid inputs ---
# These run against the root module itself (no wrapper) so expect_failures
# names the root variables and the runs fail if the root's validation rules
# are removed. Validation fails before any provider is configured.

run "invalid_cloud_provider_rejected" {
  command = plan
  variables {
    cloud_provider = "digitalocean"
    service        = "ecs"
    region         = "us-east-1"
    config_json    = jsonencode({})
  }
  expect_failures = [var.cloud_provider]
}

run "invalid_service_rejected" {
  command = plan
  variables {
    cloud_provider = "aws"
    service        = "lambda"
    region         = "us-east-1"
    config_json    = jsonencode({})
  }
  expect_failures = [var.service]
}

# --- Cross-validation and feature gates ---
# Also run against the root module: expect_failures can name
# terraform_data.validate_service_combination there, which it cannot inside the
# wrapper's nested module. kubernetes/deployment has no cloud API to provision a
# database or a secret store with, so it stays unsupported for both flags.

run "cross_validation_rejects_aws_gke" {
  command = plan
  variables {
    cloud_provider = "aws"
    service        = "gke"
    region         = "us-east-1"
    config_json    = jsonencode({})
  }
  expect_failures = [terraform_data.validate_service_combination]
}

run "create_database_rejected_on_kubernetes" {
  command = plan
  variables {
    cloud_provider  = "kubernetes"
    service         = "deployment"
    region          = "local"
    config_json     = jsonencode({})
    create_database = true
  }
  expect_failures = [terraform_data.validate_service_combination]
}

run "generate_secrets_rejected_on_kubernetes" {
  command = plan
  variables {
    cloud_provider   = "kubernetes"
    service          = "deployment"
    region           = "local"
    config_json      = jsonencode({})
    generate_secrets = true
  }
  expect_failures = [terraform_data.validate_service_combination]
}
