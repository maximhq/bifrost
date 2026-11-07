# =============================================================================
# AWS ECS Module Tests
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

run "ecs_basic" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "ecs"
    region         = "us-east-1"
    config_json    = jsonencode({})
  }
}

run "ecs_no_alb_by_default" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider       = "aws"
    service              = "ecs"
    region               = "us-east-1"
    config_json          = jsonencode({})
    create_load_balancer = false
  }
}

run "ecs_with_alb" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider       = "aws"
    service              = "ecs"
    region               = "us-east-1"
    config_json          = jsonencode({})
    create_load_balancer = true
  }
}

run "ecs_no_autoscaling_by_default" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "ecs"
    region         = "us-east-1"
    config_json    = jsonencode({})
  }
}

run "ecs_with_autoscaling" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider     = "aws"
    service            = "ecs"
    region             = "us-east-1"
    config_json        = jsonencode({})
    enable_autoscaling = true
    min_capacity       = 2
    max_capacity       = 8
  }
}

run "ecs_custom_compute" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "ecs"
    region         = "us-east-1"
    config_json    = jsonencode({})
    cpu            = 1024
    memory         = 2048
    desired_count  = 3
  }
}

run "ecs_existing_vpc" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider      = "aws"
    service             = "ecs"
    region              = "us-east-1"
    config_json         = jsonencode({})
    existing_vpc_id     = "vpc-12345"
    existing_subnet_ids = ["subnet-aaa", "subnet-bbb"]
  }
}

run "ecs_private_subnet" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider   = "aws"
    service          = "ecs"
    region           = "us-east-1"
    config_json      = jsonencode({})
    assign_public_ip = false
  }
}

# --- Managed database and generated secrets (the one-click AWS stack) ---

run "ecs_with_managed_database" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider       = "aws"
    service              = "ecs"
    region               = "us-east-1"
    config_json          = jsonencode({})
    create_database      = true
    generate_secrets     = true
    create_load_balancer = true
  }

  assert {
    condition     = jsondecode(nonsensitive(output.config_json)).config_store.type == "postgres"
    error_message = "create_database must inject a postgres config_store"
  }

  assert {
    condition     = jsondecode(nonsensitive(output.config_json)).logs_store.config.ssl_mode == "env.PG_SSL_MODE"
    error_message = "managed store must resolve ssl_mode from PG_SSL_MODE"
  }

  assert {
    condition     = jsondecode(nonsensitive(output.config_json)).encryption_key == "env.BIFROST_ENCRYPTION_KEY"
    error_message = "generate_secrets must reference BIFROST_ENCRYPTION_KEY"
  }

  assert {
    condition     = jsondecode(nonsensitive(output.config_json)).setup_token == "env.BIFROST_SETUP_TOKEN"
    error_message = "generate_secrets must reference BIFROST_SETUP_TOKEN"
  }
}

run "ecs_explicit_store_wins_over_managed_database" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider  = "aws"
    service         = "ecs"
    region          = "us-east-1"
    config_json     = jsonencode({})
    create_database = true
    config_store = {
      enabled = true
      type    = "sqlite"
      config  = { path = "/app/data/bifrost.db" }
    }
  }

  assert {
    condition     = jsondecode(nonsensitive(output.config_json)).config_store.type == "sqlite"
    error_message = "an explicit config_store must not be replaced by the managed database"
  }

  assert {
    condition     = jsondecode(nonsensitive(output.config_json)).logs_store.type == "postgres"
    error_message = "logs_store without an explicit value must still use the managed database"
  }
}

run "ecs_explicit_secrets_win_over_generated" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider   = "aws"
    service          = "ecs"
    region           = "us-east-1"
    config_json      = jsonencode({})
    generate_secrets = true
    encryption_key   = "env.MY_KEY"
    setup_token      = "env.MY_TOKEN"
  }

  assert {
    condition     = jsondecode(nonsensitive(output.config_json)).encryption_key == "env.MY_KEY"
    error_message = "an explicit encryption_key must win over the generated one"
  }

  assert {
    condition     = jsondecode(nonsensitive(output.config_json)).setup_token == "env.MY_TOKEN"
    error_message = "an explicit setup_token must win over the generated one"
  }
}
