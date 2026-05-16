# =============================================================================
# AWS Shared Infrastructure Tests — VPC, SG, Secrets Manager conditionality
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

run "creates_vpc" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "ecs"
    region         = "us-east-1"
    config_json    = jsonencode({})
  }
}

run "skips_vpc_when_existing" {
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

run "skips_sg_when_existing" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider              = "aws"
    service                     = "ecs"
    region                      = "us-east-1"
    config_json                 = jsonencode({})
    existing_security_group_ids = ["sg-12345"]
  }
}

run "custom_cidr" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "ecs"
    region         = "us-east-1"
    config_json    = jsonencode({})
    allowed_cidr   = "10.0.0.0/8"
  }
}

run "custom_prefix" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "ecs"
    region         = "us-east-1"
    config_json    = jsonencode({})
    name_prefix    = "my-gateway"
  }
}

run "tags_applied" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "ecs"
    region         = "us-east-1"
    config_json    = jsonencode({})
    tags           = { Environment = "test", Team = "platform" }
  }
}

run "eks_no_ecs_resources" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "eks"
    region         = "us-east-1"
    config_json    = jsonencode({})
  }
}
