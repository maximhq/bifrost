# =============================================================================
# AWS EKS Module Tests
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

run "eks_basic" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "eks"
    region         = "us-east-1"
    config_json    = jsonencode({})
  }
}

run "eks_create_cluster" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "eks"
    region         = "us-east-1"
    config_json    = jsonencode({})
    create_cluster = true
  }
}

run "eks_skip_cluster" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "eks"
    region         = "us-east-1"
    config_json    = jsonencode({})
    create_cluster = false
  }
}

run "eks_custom_namespace" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider       = "aws"
    service              = "eks"
    region               = "us-east-1"
    config_json          = jsonencode({})
    kubernetes_namespace = "custom-ns"
  }
}

run "eks_with_hpa" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider     = "aws"
    service            = "eks"
    region             = "us-east-1"
    config_json        = jsonencode({})
    enable_autoscaling = true
    min_capacity       = 2
    max_capacity       = 10
  }
}

run "eks_with_ingress" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider       = "aws"
    service              = "eks"
    region               = "us-east-1"
    config_json          = jsonencode({})
    create_load_balancer = true
    domain_name          = "bifrost.example.com"
  }
}

run "eks_with_https" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider       = "aws"
    service              = "eks"
    region               = "us-east-1"
    config_json          = jsonencode({})
    create_load_balancer = true
    domain_name          = "bifrost.example.com"
    certificate_arn      = "arn:aws:acm:us-east-1:123456789012:certificate/abc-123"
  }
}

run "eks_custom_nodes" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider    = "aws"
    service           = "eks"
    region            = "us-east-1"
    config_json       = jsonencode({})
    node_count        = 5
    node_machine_type = "t3.large"
  }
}

run "eks_custom_volume" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "aws"
    service        = "eks"
    region         = "us-east-1"
    config_json    = jsonencode({})
    volume_size_gb = 50
  }
}
