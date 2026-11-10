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
mock_provider "random" {}

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

# --- Managed database and generated secrets (the one-click GCP stack) ---

run "cloudrun_with_managed_database" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider       = "gcp"
    service              = "cloud-run"
    region               = "us-central1"
    gcp_project_id       = "test-project"
    config_json          = jsonencode({})
    create_database      = true
    generate_secrets     = true
    create_load_balancer = true
  }

  assert {
    condition     = jsondecode(nonsensitive(output.config_json)).config_store.config.host == "env.PG_HOST"
    error_message = "create_database must inject an env-referenced postgres config_store"
  }

  assert {
    condition     = jsondecode(nonsensitive(output.config_json)).setup_token == "env.BIFROST_SETUP_TOKEN"
    error_message = "generate_secrets must reference BIFROST_SETUP_TOKEN"
  }
}

run "cloudrun_without_managed_database_keeps_config" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "gcp"
    service        = "cloud-run"
    region         = "us-central1"
    gcp_project_id = "test-project"
    config_json    = jsonencode({ config_store = { enabled = true, type = "sqlite", config = { path = "/app/data/bifrost.db" } } })
  }

  assert {
    condition     = jsondecode(nonsensitive(output.config_json)).config_store.type == "sqlite"
    error_message = "without create_database the base config must pass through untouched"
  }

  assert {
    condition     = !contains(keys(jsondecode(nonsensitive(output.config_json))), "setup_token")
    error_message = "without generate_secrets no setup_token must be injected"
  }
}
