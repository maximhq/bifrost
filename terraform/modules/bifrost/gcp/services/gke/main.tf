# ──────────────────────────────────────────────────────────────────────────────
# GKE service sub-module – Bifrost on Google Kubernetes Engine
# ──────────────────────────────────────────────────────────────────────────────

terraform {
  required_providers {
    google = {
      source = "hashicorp/google"
    }
    kubernetes = {
      source = "hashicorp/kubernetes"
    }
  }
}

locals {
  machine_type = coalesce(var.node_machine_type, "e2-standard-4")
}

# ──────────────────────────────────────────────────────────────────────────────
# GKE Cluster (optional – skip if create_cluster is false)
# ──────────────────────────────────────────────────────────────────────────────

resource "google_container_cluster" "bifrost" {
  count = var.create_cluster ? 1 : 0

  name     = "${var.name_prefix}-cluster"
  project  = var.project_id
  location = var.region

  network    = var.vpc_id
  subnetwork = var.subnet_id

  # We manage the node pool separately
  remove_default_node_pool = true
  initial_node_count       = 1

  ip_allocation_policy {
    cluster_secondary_range_name  = "${var.name_prefix}-pods"
    services_secondary_range_name = "${var.name_prefix}-services"
  }

  master_authorized_networks_config {
    cidr_blocks {
      cidr_block   = var.master_authorized_cidr
      display_name = "authorized-network"
    }
  }

  resource_labels = var.tags
}

resource "google_container_node_pool" "bifrost" {
  count = var.create_cluster ? 1 : 0

  name     = "${var.name_prefix}-node-pool"
  project  = var.project_id
  location = var.region
  cluster  = google_container_cluster.bifrost[0].name

  node_count = var.node_count

  node_config {
    machine_type    = local.machine_type
    service_account = var.service_account
    oauth_scopes = [
      "https://www.googleapis.com/auth/cloud-platform",
    ]

    labels = var.tags
    tags   = ["${var.name_prefix}-node"]

    disk_size_gb = 50
    disk_type    = "pd-standard"

    workload_metadata_config {
      mode = "GKE_METADATA"
    }
  }
}

# ──────────────────────────────────────────────────────────────────────────────
# Kubernetes Namespace
# ──────────────────────────────────────────────────────────────────────────────

resource "kubernetes_namespace" "bifrost" {
  metadata {
    name   = var.kubernetes_namespace
    labels = var.tags
  }

  depends_on = [google_container_node_pool.bifrost]
}

# ──────────────────────────────────────────────────────────────────────────────
# Kubernetes Secret – config.json
# ──────────────────────────────────────────────────────────────────────────────

resource "kubernetes_secret" "bifrost_config" {
  metadata {
    name      = "${var.name_prefix}-config"
    namespace = kubernetes_namespace.bifrost.metadata[0].name
  }

  data = {
    "config.json" = var.config_json
  }

  type = "Opaque"
}

# ──────────────────────────────────────────────────────────────────────────────
# Persistent Disk + PV + PVC for SQLite storage
# ──────────────────────────────────────────────────────────────────────────────

resource "google_compute_disk" "bifrost" {
  name    = "${var.name_prefix}-disk"
  project = var.project_id
  zone    = "${var.region}-a"
  size    = var.volume_size_gb
  type    = "pd-ssd"

  labels = var.tags

  lifecycle {
    ignore_changes = [labels]
  }
}

resource "kubernetes_persistent_volume" "bifrost" {
  metadata {
    name = "${var.name_prefix}-volume"
  }

  spec {
    capacity = {
      storage = "${var.volume_size_gb}Gi"
    }

    access_modes                     = ["ReadWriteOnce"]
    persistent_volume_reclaim_policy = "Retain"
    storage_class_name               = "premium-rwo"

    persistent_volume_source {
      gce_persistent_disk {
        pd_name = google_compute_disk.bifrost.name
      }
    }

    node_affinity {
      required {
        node_selector_term {
          match_expressions {
            key      = "topology.kubernetes.io/zone"
            operator = "In"
            values   = ["${var.region}-a"]
          }
        }
      }
    }
  }

  depends_on = [google_compute_disk.bifrost]

  lifecycle {
    prevent_destroy = false
  }
}

resource "kubernetes_persistent_volume_claim" "bifrost" {
  metadata {
    name      = "${var.name_prefix}-volume-claim"
    namespace = kubernetes_namespace.bifrost.metadata[0].name
  }

  spec {
    access_modes = ["ReadWriteOnce"]

    resources {
      requests = {
        storage = "${var.volume_size_gb}Gi"
      }
    }

    storage_class_name = "premium-rwo"
    volume_name        = kubernetes_persistent_volume.bifrost.metadata[0].name
  }

  depends_on = [kubernetes_persistent_volume.bifrost]
}

# ──────────────────────────────────────────────────────────────────────────────
# Kubernetes Deployment
# ──────────────────────────────────────────────────────────────────────────────

resource "kubernetes_deployment" "bifrost" {
  metadata {
    name      = var.service_name
    namespace = kubernetes_namespace.bifrost.metadata[0].name

    labels = merge(var.tags, {
      app = var.service_name
    })
  }

  spec {
    replicas = var.desired_count

    selector {
      match_labels = {
        app = var.service_name
      }
    }

    template {
      metadata {
        labels = merge(var.tags, {
          app = var.service_name
        })
      }

      spec {
        security_context {
          fs_group               = 1000
          fs_group_change_policy = "OnRootMismatch"
        }

        # Init container: fix permissions on the data volume
        init_container {
          name    = "fix-permissions"
          image   = "busybox:latest"
          command = ["sh", "-c", "chown -R 1000:1000 /app/data && chmod -R 755 /app/data"]

          security_context {
            run_as_user = 0
          }

          volume_mount {
            name       = "bifrost-volume"
            mount_path = "/app/data"
          }
        }

        # Main Bifrost container
        container {
          name  = "bifrost"
          image = var.image

          port {
            container_port = var.container_port
            name           = "http"
          }

          security_context {
            run_as_user                = 1000
            run_as_group               = 1000
            run_as_non_root            = true
            allow_privilege_escalation = false
          }

          resources {
            requests = {
              cpu    = "${var.cpu}m"
              memory = "${var.memory}Mi"
            }
            limits = {
              cpu    = "${var.cpu * 2}m"
              memory = "${var.memory * 2}Mi"
            }
          }

          # Data volume
          volume_mount {
            name       = "bifrost-volume"
            mount_path = "/app/data"
          }

          # Config file mounted via subPath
          volume_mount {
            name       = "config-volume"
            mount_path = "/app/data/config.json"
            sub_path   = "config.json"
          }

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

          readiness_probe {
            http_get {
              path = var.health_check_path
              port = var.container_port
            }
            initial_delay_seconds = 10
            period_seconds        = 5
            timeout_seconds       = 3
            failure_threshold     = 3
          }
        }

        # Volumes
        volume {
          name = "bifrost-volume"
          persistent_volume_claim {
            claim_name = kubernetes_persistent_volume_claim.bifrost.metadata[0].name
          }
        }

        volume {
          name = "config-volume"
          secret {
            secret_name = kubernetes_secret.bifrost_config.metadata[0].name
          }
        }
      }
    }
  }

  depends_on = [
    kubernetes_secret.bifrost_config,
    kubernetes_persistent_volume_claim.bifrost,
  ]
}

# ──────────────────────────────────────────────────────────────────────────────
# Kubernetes Service (ClusterIP, port 80 -> 8080)
# ──────────────────────────────────────────────────────────────────────────────

resource "kubernetes_service" "bifrost" {
  metadata {
    name      = var.service_name
    namespace = kubernetes_namespace.bifrost.metadata[0].name

    labels = merge(var.tags, {
      app = var.service_name
    })
  }

  spec {
    selector = {
      app = var.service_name
    }

    port {
      name        = "http"
      port        = 80
      target_port = var.container_port
      protocol    = "TCP"
    }

    type = "ClusterIP"
  }
}

# ──────────────────────────────────────────────────────────────────────────────
# Horizontal Pod Autoscaler (optional)
# ──────────────────────────────────────────────────────────────────────────────

resource "kubernetes_horizontal_pod_autoscaler_v2" "bifrost" {
  count = var.enable_autoscaling ? 1 : 0

  metadata {
    name      = "${var.service_name}-hpa"
    namespace = kubernetes_namespace.bifrost.metadata[0].name
  }

  spec {
    scale_target_ref {
      api_version = "apps/v1"
      kind        = "Deployment"
      name        = kubernetes_deployment.bifrost.metadata[0].name
    }

    min_replicas = var.min_capacity
    max_replicas = var.max_capacity

    metric {
      type = "Resource"
      resource {
        name = "cpu"
        target {
          type                = "Utilization"
          average_utilization = var.autoscaling_cpu_threshold
        }
      }
    }

    metric {
      type = "Resource"
      resource {
        name = "memory"
        target {
          type                = "Utilization"
          average_utilization = var.autoscaling_memory_threshold
        }
      }
    }
  }
}

# ──────────────────────────────────────────────────────────────────────────────
# Kubernetes Ingress (optional – created when create_load_balancer is true)
# ──────────────────────────────────────────────────────────────────────────────

resource "kubernetes_ingress_v1" "bifrost" {
  count = var.create_load_balancer ? 1 : 0

  metadata {
    name      = "${var.service_name}-ingress"
    namespace = kubernetes_namespace.bifrost.metadata[0].name

    annotations = {
      "kubernetes.io/ingress.class"                 = "gce"
      "kubernetes.io/ingress.global-static-ip-name" = "${var.name_prefix}-ip"
    }

    labels = var.tags
  }

  spec {
    rule {
      host = var.domain_name

      http {
        path {
          path      = "/"
          path_type = "Prefix"

          backend {
            service {
              name = kubernetes_service.bifrost.metadata[0].name
              port {
                number = 80
              }
            }
          }
        }
      }
    }
  }
}

# Reserve a global static IP for the Ingress
resource "google_compute_global_address" "bifrost" {
  count = var.create_load_balancer ? 1 : 0

  name    = "${var.name_prefix}-ip"
  project = var.project_id
}
