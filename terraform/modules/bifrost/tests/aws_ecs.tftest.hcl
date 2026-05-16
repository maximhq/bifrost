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
