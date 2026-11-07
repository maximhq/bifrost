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

  # Resolve networking: use existing or created
  vpc_id    = var.existing_vpc_id != null ? var.existing_vpc_id : try(google_compute_network.bifrost[0].self_link, null)
  subnet_id = var.existing_subnet_ids != null ? var.existing_subnet_ids[0] : try(google_compute_subnetwork.bifrost[0].self_link, null)

  create_network = var.existing_vpc_id == null
}

# existing_vpc_id and existing_subnet_ids must arrive together. A VPC without
# subnets would hand GKE a null subnetwork; subnets without a VPC would create
# a new network the subnets do not belong to.
resource "terraform_data" "validate_network_inputs" {
  lifecycle {
    precondition {
      condition     = (var.existing_vpc_id == null) == (var.existing_subnet_ids == null)
      error_message = "existing_vpc_id and existing_subnet_ids must be set together: pass both to reuse a network, or neither to create one."
    }
  }
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
  ingress_path      = var.ingress_path

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

  # Managed database and generated secrets
  cloud_sql_connection_name = local.manage_database ? google_sql_database_instance.bifrost[0].connection_name : null
  extra_environment         = local.managed_environment
  extra_secret_environment  = local.managed_secret_environment

  # Optional features
  create_load_balancer = var.create_load_balancer
  domain_name          = var.domain_name

  depends_on = [
    google_sql_database.bifrost,
    google_sql_user.bifrost,
    google_project_iam_member.bifrost_cloudsql_client,
    google_secret_manager_secret_version.database_password,
    google_secret_manager_secret_version.encryption_key,
    google_secret_manager_secret_version.setup_token,
  ]
}

# ──────────────────────────────────────────────────────────────────────────────
# Managed PostgreSQL (Cloud SQL) and generated runtime secrets (Cloud Run only)
#
# Cloud Run reaches the instance through its built-in Cloud SQL connection:
# the instance is mounted as a unix socket under /cloudsql, so no VPC
# connector, private IP or authorized network is needed. The socket path is
# the PG_HOST and TLS is handled by the connector, hence PG_SSL_MODE=disable.
# ──────────────────────────────────────────────────────────────────────────────

locals {
  is_cloud_run     = var.service == "cloud-run"
  manage_database  = local.is_cloud_run && var.create_database
  generate_secrets = local.is_cloud_run && var.generate_secrets

  database_tier = coalesce(var.database_instance_class, "db-custom-1-3840")

  managed_environment = local.manage_database ? [
    { name = "PG_HOST", value = "/cloudsql/${google_sql_database_instance.bifrost[0].connection_name}" },
    { name = "PG_PORT", value = "5432" },
    { name = "PG_USER", value = var.database_user },
    { name = "PG_DATABASE", value = var.database_name },
    { name = "PG_SSL_MODE", value = "disable" },
  ] : []

  managed_secret_environment = concat(
    local.manage_database ? [
      { name = "PG_PASSWORD", secret = google_secret_manager_secret.database_password[0].secret_id },
    ] : [],
    local.generate_secrets ? [
      { name = "BIFROST_ENCRYPTION_KEY", secret = google_secret_manager_secret.encryption_key[0].secret_id },
      { name = "BIFROST_SETUP_TOKEN", secret = google_secret_manager_secret.setup_token[0].secret_id },
    ] : [],
  )
}

resource "random_password" "database" {
  count = local.manage_database ? 1 : 0

  length  = 32
  special = false
}

resource "random_password" "encryption_key" {
  count = local.generate_secrets ? 1 : 0

  length  = 48
  special = false
}

resource "random_password" "setup_token" {
  count = local.generate_secrets ? 1 : 0

  length  = 40
  special = false
}

# Cloud SQL instance names cannot be reused for about a week after deletion;
# the suffix lets a destroyed stack be recreated immediately.
resource "random_id" "database_suffix" {
  count = local.manage_database ? 1 : 0

  byte_length = 2
}

resource "google_sql_database_instance" "bifrost" {
  count = local.manage_database ? 1 : 0

  name             = "${var.name_prefix}-db-${random_id.database_suffix[0].hex}"
  project          = var.project_id
  region           = var.region
  database_version = "POSTGRES_16"

  deletion_protection = var.database_deletion_protection

  settings {
    tier              = local.database_tier
    availability_type = "ZONAL"
    disk_type         = "PD_SSD"
    disk_size         = var.database_storage_gb
    disk_autoresize   = true

    # Public IP with no authorized networks: only connector traffic
    # (Cloud Run's /cloudsql mount) and IAM-authorized tooling can reach it.
    ip_configuration {
      ipv4_enabled = true
    }

    backup_configuration {
      enabled                        = true
      point_in_time_recovery_enabled = true
    }

    user_labels = var.tags
  }
}

resource "google_sql_database" "bifrost" {
  count = local.manage_database ? 1 : 0

  name     = var.database_name
  project  = var.project_id
  instance = google_sql_database_instance.bifrost[0].name
}

resource "google_sql_user" "bifrost" {
  count = local.manage_database ? 1 : 0

  name     = var.database_user
  project  = var.project_id
  instance = google_sql_database_instance.bifrost[0].name
  password = random_password.database[0].result
}

resource "google_project_iam_member" "bifrost_cloudsql_client" {
  count = local.manage_database ? 1 : 0

  project = var.project_id
  role    = "roles/cloudsql.client"
  member  = "serviceAccount:${google_service_account.bifrost.email}"
}

resource "google_secret_manager_secret" "database_password" {
  count = local.manage_database ? 1 : 0

  secret_id = "${var.name_prefix}-db-password"
  project   = var.project_id
  labels    = var.tags

  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_version" "database_password" {
  count = local.manage_database ? 1 : 0

  secret      = google_secret_manager_secret.database_password[0].id
  secret_data = random_password.database[0].result
}

resource "google_secret_manager_secret" "encryption_key" {
  count = local.generate_secrets ? 1 : 0

  secret_id = "${var.name_prefix}-encryption-key"
  project   = var.project_id
  labels    = var.tags

  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_version" "encryption_key" {
  count = local.generate_secrets ? 1 : 0

  secret      = google_secret_manager_secret.encryption_key[0].id
  secret_data = random_password.encryption_key[0].result
}

resource "google_secret_manager_secret" "setup_token" {
  count = local.generate_secrets ? 1 : 0

  secret_id = "${var.name_prefix}-setup-token"
  project   = var.project_id
  labels    = var.tags

  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_version" "setup_token" {
  count = local.generate_secrets ? 1 : 0

  secret      = google_secret_manager_secret.setup_token[0].id
  secret_data = random_password.setup_token[0].result
}
