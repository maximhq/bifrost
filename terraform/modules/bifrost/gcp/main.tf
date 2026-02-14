# ──────────────────────────────────────────────────────────────────────────────
# GCP platform module – shared infrastructure for GKE and Cloud Run
# ──────────────────────────────────────────────────────────────────────────────

terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 5.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 2.0"
    }
  }
}

locals {
  service_name = "${var.name_prefix}-service"

  # Validate: VPC and subnet must be provided together
  _validate_vpc_subnet = (
    (var.existing_vpc_id == null) == (var.existing_subnet_ids == null)
  )

  # Resolve networking: use existing or created
  vpc_id    = var.existing_vpc_id != null ? var.existing_vpc_id : try(google_compute_network.bifrost[0].self_link, null)
  subnet_id = var.existing_subnet_ids != null ? var.existing_subnet_ids[0] : try(google_compute_subnetwork.bifrost[0].self_link, null)

  create_network = var.existing_vpc_id == null
}

# ──────────────────────────────────────────────────────────────────────────────
# VPC Network (optional – skip if existing_vpc_id is provided)
# ──────────────────────────────────────────────────────────────────────────────

resource "google_compute_network" "bifrost" {
  count = local.create_network ? 1 : 0

  name                    = "${var.name_prefix}-vpc"
  project                 = var.project_id
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "bifrost" {
  count = local.create_network ? 1 : 0

  name          = "${var.name_prefix}-subnet"
  project       = var.project_id
  region        = var.region
  network       = google_compute_network.bifrost[0].self_link
  ip_cidr_range = "10.0.0.0/16"

  secondary_ip_range {
    range_name    = "${var.name_prefix}-pods"
    ip_cidr_range = "10.1.0.0/16"
  }

  secondary_ip_range {
    range_name    = "${var.name_prefix}-services"
    ip_cidr_range = "10.2.0.0/16"
  }
}

# ──────────────────────────────────────────────────────────────────────────────
# Firewall – allow ingress on the container port
# ──────────────────────────────────────────────────────────────────────────────

resource "google_compute_firewall" "bifrost_allow_ingress" {
  count = local.create_network ? 1 : 0

  name    = "${var.name_prefix}-allow-ingress"
  project = var.project_id
  network = local.vpc_id

  direction = "INGRESS"

  allow {
    protocol = "tcp"
    ports    = [tostring(var.container_port)]
  }

  source_ranges = [var.allowed_cidr]
  target_tags   = ["${var.name_prefix}-node"]
}

# ──────────────────────────────────────────────────────────────────────────────
# Secret Manager – store config.json
# ──────────────────────────────────────────────────────────────────────────────

resource "google_secret_manager_secret" "bifrost_config" {
  secret_id = "${var.name_prefix}-config"
  project   = var.project_id

  labels = var.tags

  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_version" "bifrost_config" {
  secret      = google_secret_manager_secret.bifrost_config.id
  secret_data = var.config_json
}

# ──────────────────────────────────────────────────────────────────────────────
# Service Account + IAM bindings
# ──────────────────────────────────────────────────────────────────────────────

resource "google_service_account" "bifrost" {
  account_id   = "${var.name_prefix}-sa"
  display_name = "Bifrost Service Account"
  project      = var.project_id
}

resource "google_project_iam_member" "bifrost_secret_accessor" {
  project = var.project_id
  role    = "roles/secretmanager.secretAccessor"
  member  = "serviceAccount:${google_service_account.bifrost.email}"
}

resource "google_project_iam_member" "bifrost_log_writer" {
  project = var.project_id
  role    = "roles/logging.logWriter"
  member  = "serviceAccount:${google_service_account.bifrost.email}"
}

# ──────────────────────────────────────────────────────────────────────────────
# Route to GKE or Cloud Run
# ──────────────────────────────────────────────────────────────────────────────

module "gke" {
  source = "./services/gke"
  count  = var.service == "gke" ? 1 : 0

  # Bifrost
  service_name      = local.service_name
  config_json       = var.config_json
  image             = var.image
  container_port    = var.container_port
  health_check_path = var.health_check_path

  # GCP
  project_id      = var.project_id
  region          = var.region
  name_prefix     = var.name_prefix
  tags            = var.tags
  service_account = google_service_account.bifrost.email

  # Networking
  vpc_id    = local.vpc_id
  subnet_id = local.subnet_id

  # Compute
  desired_count = var.desired_count
  cpu           = var.cpu
  memory        = var.memory

  # Cluster
  create_cluster       = var.create_cluster
  kubernetes_namespace = var.kubernetes_namespace
  node_count           = var.node_count
  node_machine_type    = var.node_machine_type
  volume_size_gb       = var.volume_size_gb

  # Optional features
  create_load_balancer         = var.create_load_balancer
  enable_autoscaling           = var.enable_autoscaling
  min_capacity                 = var.min_capacity
  max_capacity                 = var.max_capacity
  autoscaling_cpu_threshold    = var.autoscaling_cpu_threshold
  autoscaling_memory_threshold = var.autoscaling_memory_threshold
  domain_name                  = var.domain_name
}

module "cloud_run" {
  source = "./services/cloud-run"
  count  = var.service == "cloud-run" ? 1 : 0

  # Bifrost
  service_name      = local.service_name
  config_json       = var.config_json
  image             = var.image
  container_port    = var.container_port
  health_check_path = var.health_check_path

  # GCP
  project_id      = var.project_id
  region          = var.region
  name_prefix     = var.name_prefix
  tags            = var.tags
  service_account = google_service_account.bifrost.email
  secret_id       = google_secret_manager_secret.bifrost_config.secret_id
  secret_version  = google_secret_manager_secret_version.bifrost_config.version

  # Networking
  vpc_id = local.vpc_id

  # Compute
  desired_count = var.desired_count
  cpu           = var.cpu
  memory        = var.memory

  # Scaling
  max_capacity = var.max_capacity

  # Optional features
  create_load_balancer = var.create_load_balancer
  domain_name          = var.domain_name
}
