# =============================================================================
# GCP GKE Module Tests
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

run "gke_basic" {
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

run "gke_create_cluster" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "gcp"
    service        = "gke"
    region         = "us-central1"
    gcp_project_id = "test-project"
    config_json    = jsonencode({})
    create_cluster = true
  }
}

run "gke_skip_cluster" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "gcp"
    service        = "gke"
    region         = "us-central1"
    gcp_project_id = "test-project"
    config_json    = jsonencode({})
    create_cluster = false
  }
}

run "gke_custom_namespace" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider       = "gcp"
    service              = "gke"
    region               = "us-central1"
    gcp_project_id       = "test-project"
    config_json          = jsonencode({})
    kubernetes_namespace = "custom-ns"
  }
}

run "gke_with_hpa" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider     = "gcp"
    service            = "gke"
    region             = "us-central1"
    gcp_project_id     = "test-project"
    config_json        = jsonencode({})
    enable_autoscaling = true
    min_capacity       = 2
    max_capacity       = 10
  }
}

run "gke_with_ingress" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider       = "gcp"
    service              = "gke"
    region               = "us-central1"
    gcp_project_id       = "test-project"
    config_json          = jsonencode({})
    create_load_balancer = true
    domain_name          = "bifrost.example.com"
  }
}

run "gke_custom_nodes" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider    = "gcp"
    service           = "gke"
    region            = "us-central1"
    gcp_project_id    = "test-project"
    config_json       = jsonencode({})
    node_count        = 5
    node_machine_type = "e2-standard-8"
  }
}

run "gke_custom_volume" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider = "gcp"
    service        = "gke"
    region         = "us-central1"
    gcp_project_id = "test-project"
    config_json    = jsonencode({})
    volume_size_gb = 50
  }
}

run "gke_existing_vpc" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider      = "gcp"
    service             = "gke"
    region              = "us-central1"
    gcp_project_id      = "test-project"
    config_json         = jsonencode({})
    existing_vpc_id     = "projects/test/global/networks/existing-vpc"
    existing_subnet_ids = ["projects/test/regions/us-central1/subnetworks/existing-subnet"]
  }
}

# --- Managed database, generated secrets and a public Service (the one-click GKE stack) ---

run "gke_with_managed_database" {
  command = plan
  module { source = "./tests/setup" }
  variables {
    cloud_provider   = "gcp"
    service          = "gke"
    region           = "us-central1"
    gcp_project_id   = "test-project"
    config_json      = jsonencode({})
    create_database  = true
    generate_secrets = true
    service_type     = "LoadBalancer"
  }

  assert {
    condition     = jsondecode(nonsensitive(output.config_json)).config_store.config.password == "env.PG_PASSWORD"
    error_message = "create_database must inject an env-referenced postgres config_store"
  }

  assert {
    condition     = jsondecode(nonsensitive(output.config_json)).setup_token == "env.BIFROST_SETUP_TOKEN"
    error_message = "generate_secrets must reference BIFROST_SETUP_TOKEN"
  }
}
