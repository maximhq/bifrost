# ──────────────────────────────────────────────────────────────────────────────
# Generic Kubernetes module – Bifrost on any K8s cluster
# ──────────────────────────────────────────────────────────────────────────────

terraform {
  required_providers {
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 2.0"
    }
  }
}

# ──────────────────────────────────────────────────────────────────────────────
# Kubernetes Namespace
# ──────────────────────────────────────────────────────────────────────────────

resource "kubernetes_namespace_v1" "bifrost" {
  metadata {
    name   = var.kubernetes_namespace
    labels = var.tags
  }
}

# ──────────────────────────────────────────────────────────────────────────────
# Kubernetes Secret – config.json
# ──────────────────────────────────────────────────────────────────────────────

resource "kubernetes_secret_v1" "bifrost_config" {
  metadata {
    name      = "${var.name_prefix}-config"
    namespace = kubernetes_namespace_v1.bifrost.metadata[0].name
  }

  data = {
    "config.json" = var.config_json
  }

  type = "Opaque"
}

# ──────────────────────────────────────────────────────────────────────────────
# Persistent Volume Claim (dynamic provisioning via StorageClass)
# ──────────────────────────────────────────────────────────────────────────────

resource "kubernetes_persistent_volume_claim_v1" "bifrost" {
  metadata {
    name      = "${var.name_prefix}-volume-claim"
    namespace = kubernetes_namespace_v1.bifrost.metadata[0].name
  }

  spec {
    access_modes = ["ReadWriteOnce"]

    resources {
      requests = {
        storage = "${var.volume_size_gb}Gi"
      }
    }

    storage_class_name = var.storage_class_name
  }
}

# ──────────────────────────────────────────────────────────────────────────────
# Kubernetes Deployment
# ──────────────────────────────────────────────────────────────────────────────

resource "kubernetes_deployment_v1" "bifrost" {
  metadata {
    name      = var.service_name
    namespace = kubernetes_namespace_v1.bifrost.metadata[0].name

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
            claim_name = kubernetes_persistent_volume_claim_v1.bifrost.metadata[0].name
          }
        }

        volume {
          name = "config-volume"
          secret {
            secret_name = kubernetes_secret_v1.bifrost_config.metadata[0].name
          }
        }
      }
    }
  }

  depends_on = [
    kubernetes_secret_v1.bifrost_config,
    kubernetes_persistent_volume_claim_v1.bifrost,
  ]
}

# ──────────────────────────────────────────────────────────────────────────────
# Kubernetes Service (ClusterIP, port 80 -> 8080)
# ──────────────────────────────────────────────────────────────────────────────

resource "kubernetes_service_v1" "bifrost" {
  metadata {
    name      = var.service_name
    namespace = kubernetes_namespace_v1.bifrost.metadata[0].name

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
    namespace = kubernetes_namespace_v1.bifrost.metadata[0].name
  }

  spec {
    scale_target_ref {
      api_version = "apps/v1"
      kind        = "Deployment"
      name        = kubernetes_deployment_v1.bifrost.metadata[0].name
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
# Kubernetes Ingress (optional – when create_load_balancer is true)
# ──────────────────────────────────────────────────────────────────────────────

resource "kubernetes_ingress_v1" "bifrost" {
  count = var.create_load_balancer && var.domain_name != null ? 1 : 0

  metadata {
    name      = "${var.service_name}-ingress"
    namespace = kubernetes_namespace_v1.bifrost.metadata[0].name

    annotations = var.ingress_annotations

    labels = var.tags
  }

  spec {
    ingress_class_name = var.ingress_class_name

    rule {
      host = var.domain_name

      http {
        path {
          path      = "/"
          path_type = "Prefix"

          backend {
            service {
              name = kubernetes_service_v1.bifrost.metadata[0].name
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
