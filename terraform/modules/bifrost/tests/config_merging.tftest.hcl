# =============================================================================
# Config Merging Tests — precedence: base JSON → variable overrides → $schema
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

run "base_config_preserved" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "ecs"
    region         = "us-east-1"
    config_json    = jsonencode({ encryption_key = "base-key" })
  }
  assert {
    condition     = jsondecode(output.config_json)["encryption_key"] == "base-key"
    error_message = "Base config key should be preserved"
  }
}

run "override_wins" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "ecs"
    region         = "us-east-1"
    config_json    = jsonencode({ encryption_key = "base-key" })
    encryption_key = "override-key"
  }
  assert {
    condition     = jsondecode(output.config_json)["encryption_key"] == "override-key"
    error_message = "Variable override should win over base config"
  }
}

run "schema_url_injected" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "ecs"
    region         = "us-east-1"
    config_json    = jsonencode({})
  }
  assert {
    condition     = jsondecode(output.config_json)["$schema"] == "https://www.getbifrost.ai/schema"
    error_message = "Schema URL should always be injected"
  }
}

run "no_base_config" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "ecs"
    region         = "us-east-1"
    encryption_key = "standalone-key"
  }
  assert {
    condition     = jsondecode(output.config_json)["encryption_key"] == "standalone-key"
    error_message = "Config should work with no base, only overrides"
  }
}

run "multiple_overrides" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "ecs"
    region         = "us-east-1"
    config_json    = jsonencode({ encryption_key = "base-key", framework = "test" })
    encryption_key = "new-key"
    governance     = { budgets = [] }
  }
  assert {
    condition     = jsondecode(output.config_json)["encryption_key"] == "new-key"
    error_message = "encryption_key override should apply"
  }
  assert {
    condition     = jsondecode(output.config_json)["governance"] != null
    error_message = "governance override should be present"
  }
  assert {
    condition     = jsondecode(output.config_json)["framework"] == "test"
    error_message = "Non-overridden base keys should be preserved"
  }
}
