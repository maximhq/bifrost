# =============================================================================
# Generic Kubernetes Module Tests
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

run "k8s_basic" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "kubernetes"
    service        = "deployment"
    region         = "local"
    config_json    = jsonencode({})
  }
}

run "k8s_custom_namespace" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider       = "kubernetes"
    service              = "deployment"
    region               = "local"
    config_json          = jsonencode({})
    kubernetes_namespace = "custom-ns"
  }
}

run "k8s_custom_storage_class" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider     = "kubernetes"
    service            = "deployment"
    region             = "local"
    config_json        = jsonencode({})
    storage_class_name = "gp2"
  }
}

run "k8s_with_hpa" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider     = "kubernetes"
    service            = "deployment"
    region             = "local"
    config_json        = jsonencode({})
    enable_autoscaling = true
    min_capacity       = 2
    max_capacity       = 10
  }
}

run "k8s_with_ingress" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider       = "kubernetes"
    service              = "deployment"
    region               = "local"
    config_json          = jsonencode({})
    create_load_balancer = true
    domain_name          = "bifrost.example.com"
  }
}

run "k8s_custom_ingress_class" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider       = "kubernetes"
    service              = "deployment"
    region               = "local"
    config_json          = jsonencode({})
    create_load_balancer = true
    domain_name          = "bifrost.example.com"
    ingress_class_name   = "traefik"
  }
}

run "k8s_ingress_annotations" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider       = "kubernetes"
    service              = "deployment"
    region               = "local"
    config_json          = jsonencode({})
    create_load_balancer = true
    domain_name          = "bifrost.example.com"
    ingress_annotations  = { "cert-manager.io/cluster-issuer" = "letsencrypt" }
  }
}

run "k8s_custom_compute" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "kubernetes"
    service        = "deployment"
    region         = "local"
    config_json    = jsonencode({})
    cpu            = 1000
    memory         = 2048
    desired_count  = 3
  }
}

run "k8s_custom_volume" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "kubernetes"
    service        = "deployment"
    region         = "local"
    config_json    = jsonencode({})
    volume_size_gb = 50
  }
}

run "k8s_tags" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "kubernetes"
    service        = "deployment"
    region         = "local"
    config_json    = jsonencode({})
    tags           = { Environment = "staging", Team = "platform" }
  }
}
