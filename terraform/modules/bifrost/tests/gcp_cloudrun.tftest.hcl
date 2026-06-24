# =============================================================================
# GCP Cloud Run Module Tests
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

run "cloudrun_basic" {
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

run "cloudrun_with_public_access" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider       = "gcp"
    service              = "cloud-run"
    region               = "us-central1"
    gcp_project_id       = "test-project"
    config_json          = jsonencode({})
    create_load_balancer = true
  }
}

run "cloudrun_with_domain" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "gcp"
    service        = "cloud-run"
    region         = "us-central1"
    gcp_project_id = "test-project"
    config_json    = jsonencode({})
    domain_name    = "bifrost.example.com"
  }
}

run "cloudrun_scaling" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "gcp"
    service        = "cloud-run"
    region         = "us-central1"
    gcp_project_id = "test-project"
    config_json    = jsonencode({})
    desired_count  = 2
    max_capacity   = 20
  }
}

run "cloudrun_custom_compute" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "gcp"
    service        = "cloud-run"
    region         = "us-central1"
    gcp_project_id = "test-project"
    config_json    = jsonencode({})
    cpu            = 1000
    memory         = 2048
  }
}
