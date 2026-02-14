# ──────────────────────────────────────────────────────────────────────────────
# Cloud Run service sub-module – Bifrost on Google Cloud Run
# ──────────────────────────────────────────────────────────────────────────────

terraform {
  required_providers {
    google = {
      source = "hashicorp/google"
    }
  }
}

locals {
  # Cloud Run expects CPU as string like "1" or "2" (whole vCPUs) or "1000m"
  cpu_string = "${var.cpu}m"
  # Cloud Run expects memory as string like "512Mi"
  memory_string = "${var.memory}Mi"
}

# ──────────────────────────────────────────────────────────────────────────────
# Cloud Run v2 Service
# ──────────────────────────────────────────────────────────────────────────────

resource "google_cloud_run_v2_service" "bifrost" {
  name     = var.service_name
  project  = var.project_id
  location = var.region

  labels = var.tags

  template {
    service_account = var.service_account

    scaling {
      min_instance_count = var.desired_count
      max_instance_count = var.max_capacity
    }

    volumes {
      name = "config-volume"
      secret {
        secret = var.secret_id
        items {
          version = var.secret_version
          path    = "config.json"
        }
      }
    }

    containers {
      image = var.image

      ports {
        container_port = var.container_port
      }

      resources {
        limits = {
          cpu    = local.cpu_string
          memory = local.memory_string
        }
      }

      # Mount config.json from Secret Manager
      volume_mounts {
        name       = "config-volume"
        mount_path = "/app/data"
      }

      # Startup probe – allows time for the container to initialize
      startup_probe {
        http_get {
          path = var.health_check_path
          port = var.container_port
        }
        initial_delay_seconds = 10
        period_seconds        = 5
        timeout_seconds       = 3
        failure_threshold     = 10
      }

      # Liveness probe – restarts the container if unhealthy
      liveness_probe {
        http_get {
          path = var.health_check_path
          port = var.container_port
        }
        initial_delay_seconds = 30
        period_seconds        = 10
        timeout_seconds       = 5
        failure_threshold     = 3
      }
    }
  }

  lifecycle {
    ignore_changes = [
      labels["run.googleapis.com/startupProbeType"],
    ]
  }
}

# ──────────────────────────────────────────────────────────────────────────────
# IAM – Allow unauthenticated access (optional, when create_load_balancer is true)
# ──────────────────────────────────────────────────────────────────────────────

resource "google_cloud_run_v2_service_iam_member" "public_access" {
  count = var.create_load_balancer ? 1 : 0

  project  = var.project_id
  location = var.region
  name     = google_cloud_run_v2_service.bifrost.name
  role     = "roles/run.invoker"
  member   = "allUsers"
}

# ──────────────────────────────────────────────────────────────────────────────
# Domain Mapping (optional – when domain_name is set)
# ──────────────────────────────────────────────────────────────────────────────

resource "google_cloud_run_domain_mapping" "bifrost" {
  count = var.domain_name != null ? 1 : 0

  name     = var.domain_name
  project  = var.project_id
  location = var.region

  metadata {
    namespace = var.project_id
    labels    = var.tags
  }

  spec {
    route_name = google_cloud_run_v2_service.bifrost.name
  }
}
