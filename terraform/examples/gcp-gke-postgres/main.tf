# =============================================================================
# Bifrost on GCP: GKE + Cloud SQL PostgreSQL 16 (private IP) + public Service
#
# The one-click GKE stack. deploy/gke/deploy.sh drives it from Cloud Shell in
# two phases: the cluster first, then (after `gcloud container clusters
# get-credentials`) the database, secrets and Kubernetes resources. Cloud SQL
# gets a private IP on the cluster VPC through service networking peering.
#
# This example calls the GCP platform module directly rather than the root
# bifrost module, so a plan needs only Google credentials.
# =============================================================================

terraform {
  required_version = ">= 1.7"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 5.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 2.20"
    }
    random = {
      source  = "hashicorp/random"
      version = ">= 3.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

provider "kubernetes" {
  config_path    = pathexpand(var.kubeconfig_path)
  config_context = var.kubeconfig_context
}

locals {
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
  service = "gke"

  config_json       = local.config_json
  image             = "${var.image_repository}:${var.image_tag}"
  container_port    = 8080
  health_check_path = "/health"

  project_id  = var.project_id
  region      = var.region
  name_prefix = var.name_prefix
  tags        = var.tags

  # Cluster
  create_cluster       = true
  kubernetes_namespace = "bifrost"
  node_count           = var.node_count
  node_machine_type    = var.node_machine_type
  volume_size_gb       = 10

  # Managed PostgreSQL for both stores plus generated runtime secrets.
  create_database              = true
  database_instance_class      = var.database_instance_class
  database_storage_gb          = var.database_storage_gb
  database_deletion_protection = var.database_deletion_protection
  generate_secrets             = true

  # One pod behind a public TCP load balancer.
  desired_count = 1
  cpu           = var.cpu
  memory        = var.memory
  service_type  = var.service_type
  allowed_cidr  = var.allowed_cidr

  # The GCE Ingress path is optional; the Service covers exposure.
  create_load_balancer         = false
  enable_autoscaling           = false
  min_capacity                 = 1
  max_capacity                 = 1
  autoscaling_cpu_threshold    = 80
  autoscaling_memory_threshold = 80
}
