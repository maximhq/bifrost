# =============================================================================
# GCP Cloud SQL PostgreSQL
# =============================================================================

terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 5.0"
    }
    random = {
      source  = "hashicorp/random"
      version = ">= 3.0"
    }
  }
}

locals {
  tier     = coalesce(var.tier, "db-custom-2-7680")
  password = var.password != null ? var.password : random_password.master[0].result
}

# --- Random password ---

resource "random_password" "master" {
  count = var.password == null ? 1 : 0

  length           = 32
  special          = true
  override_special = "!#$%&*()-_=+[]<>:?"
}

# --- Private IP allocation for Cloud SQL ---

resource "google_compute_global_address" "postgresql" {
  name          = "${var.name_prefix}-pg-ip"
  project       = var.project_id
  purpose       = "VPC_PEERING"
  address_type  = "INTERNAL"
  prefix_length = 16
  network       = var.vpc_id
}

resource "google_service_networking_connection" "postgresql" {
  network                 = var.vpc_id
  service                 = "servicenetworking.googleapis.com"
  reserved_peering_ranges = [google_compute_global_address.postgresql.name]
}

# --- Cloud SQL Instance ---

resource "google_sql_database_instance" "this" {
  name             = "${var.name_prefix}-pg"
  project          = var.project_id
  region           = var.region
  database_version = "POSTGRES_${var.engine_version}"

  depends_on = [google_service_networking_connection.postgresql]

  settings {
    tier              = local.tier
    disk_size         = var.disk_size_gb
    disk_type         = "PD_SSD"
    disk_autoresize   = true
    availability_type = var.high_availability ? "REGIONAL" : "ZONAL"

    ip_configuration {
      ipv4_enabled                                  = false
      private_network                               = var.vpc_id
      enable_private_path_for_google_cloud_services = true
    }

    backup_configuration {
      enabled                        = true
      point_in_time_recovery_enabled = true
      transaction_log_retention_days = var.backup_retention_days
    }

    user_labels = var.tags
  }

  deletion_protection = false
}

# --- Database ---

resource "google_sql_database" "this" {
  name     = var.database_name
  project  = var.project_id
  instance = google_sql_database_instance.this.name
}

# --- User ---

resource "google_sql_user" "this" {
  name     = var.username
  project  = var.project_id
  instance = google_sql_database_instance.this.name
  password = local.password
}
