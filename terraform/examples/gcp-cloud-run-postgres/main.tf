# =============================================================================
# Bifrost on GCP: Cloud Run + Cloud SQL PostgreSQL 16
#
# The one-click GCP stack. Both Bifrost stores live in Cloud SQL, reached over
# Cloud Run's built-in Cloud SQL connector (a unix socket under /cloudsql), the
# encryption key and setup token are generated into Secret Manager, and the
# service is public. deploy/gcp/deploy.sh drives this example from Cloud Shell.
#
# This example calls the GCP platform module directly rather than the root
# bifrost module. Terraform configures every provider referenced in a module
# tree, so the root module's plan needs AWS and Azure credentials as well;
# the platform module needs only Google.
# =============================================================================

terraform {
  required_version = ">= 1.7"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 5.0"
    }
    random = {
      source  = "hashicorp/random"
      version = ">= 3.0"
    }
    # Pulled in by the (unused, count = 0) GKE sub-module. It needs no
    # configuration and no cluster.
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 2.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

locals {
  # The shared one-click config.json. Every store value is an env reference
  # that the platform module satisfies through PG_* and BIFROST_* variables.
  base_config = jsondecode(file("${path.module}/../../../deploy/config.postgres.json"))

  config_json = jsonencode(merge(
    local.base_config,
    {
      client = merge(try(local.base_config.client, {}), {
        enforce_auth_on_inference = var.enforce_auth_on_inference
      })
    },
    var.providers_config != null ? { providers = var.providers_config } : {},
  ))
}

module "bifrost" {
  source  = "../../modules/bifrost/gcp"
  service = "cloud-run"

  config_json       = local.config_json
  image             = "${var.image_repository}:${var.image_tag}"
  container_port    = 8080
  health_check_path = "/health"

  project_id  = var.project_id
  region      = var.region
  name_prefix = var.name_prefix
  tags        = var.tags

  # Managed PostgreSQL for both stores plus generated runtime secrets.
  create_database              = true
  database_instance_class      = var.database_instance_class
  database_storage_gb          = var.database_storage_gb
  database_deletion_protection = var.database_deletion_protection
  generate_secrets             = true

  # Compute and exposure. create_load_balancer grants allUsers the invoker
  # role, which is what makes the Cloud Run URL public.
  desired_count        = var.min_instances
  max_capacity         = var.max_instances
  cpu                  = var.cpu
  memory               = var.memory
  create_load_balancer = true

  # Not used by Cloud Run; required by the shared platform interface.
  enable_autoscaling           = false
  min_capacity                 = var.min_instances
  autoscaling_cpu_threshold    = 80
  autoscaling_memory_threshold = 80
  create_cluster               = false
  kubernetes_namespace         = "bifrost"
  node_count                   = 0
  volume_size_gb               = 10
}
