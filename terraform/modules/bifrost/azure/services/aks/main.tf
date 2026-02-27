# =============================================================================
# AKS Service Module - Azure Kubernetes Service for Bifrost
# =============================================================================

locals {
  service_name    = "${var.name_prefix}-service"
  vm_size         = var.node_machine_type != null ? var.node_machine_type : "Standard_D2s_v3"
  cpu_request     = "${var.cpu}m"
  cpu_limit       = "${var.cpu * 2}m"
  memory_request  = "${var.memory}Mi"
  memory_limit    = "${var.memory * 2}Mi"
  cluster_name    = "${var.name_prefix}-aks"
  ingress_enabled = var.create_load_balancer && var.domain_name != null
}

# =============================================================================
# AKS Cluster (optional)
# =============================================================================

resource "azurerm_kubernetes_cluster" "this" {
  count               = var.create_cluster ? 1 : 0
  name                = local.cluster_name
  location            = var.region
  resource_group_name = var.resource_group_name
  dns_prefix          = var.name_prefix
  tags                = var.tags

  role_based_access_control_enabled = true

  api_server_access_profile {
    authorized_ip_ranges = var.api_server_authorized_ip_ranges
  }

  default_node_pool {
    name           = "default"
    node_count     = var.node_count
    vm_size        = local.vm_size
    vnet_subnet_id = var.subnet_ids[0]
  }

  identity {
    type         = "UserAssigned"
    identity_ids = [var.identity_id]
  }

  network_profile {
    network_plugin = "azure"
    service_cidr   = "10.1.0.0/16"
    dns_service_ip = "10.1.0.10"
  }
}

# =============================================================================
# Kubernetes Namespace
# =============================================================================

resource "kubernetes_namespace" "this" {
  metadata {
    name = var.kubernetes_namespace
    labels = {
      app = var.name_prefix
    }
  }

  depends_on = [azurerm_kubernetes_cluster.this]
}

# =============================================================================
# Configuration Secret
# =============================================================================

resource "kubernetes_secret" "bifrost_config" {
  metadata {
    name      = "${var.name_prefix}-config"
    namespace = kubernetes_namespace.this.metadata[0].name
  }

  data = {
    "config.json" = var.config_json
  }

  type = "Opaque"
}

# =============================================================================
# Managed Disk + Persistent Volume + PVC for SQLite storage
# =============================================================================

resource "azurerm_managed_disk" "bifrost_disk" {
  name                 = "${var.name_prefix}-disk"
  location             = var.region
  resource_group_name  = var.resource_group_name
  storage_account_type = "Premium_LRS"
  create_option        = "Empty"
  disk_size_gb         = var.volume_size_gb
  tags                 = var.tags

  lifecycle {
    ignore_changes = [tags]
  }
}

resource "kubernetes_persistent_volume" "bifrost_volume" {
  metadata {
    name = "${var.name_prefix}-volume"
  }

  spec {
    capacity = {
      storage = "${var.volume_size_gb}Gi"
    }
    access_modes                     = ["ReadWriteOnce"]
    persistent_volume_reclaim_policy = "Retain"
    storage_class_name               = "managed-premium"

    persistent_volume_source {
      azure_disk {
        disk_name     = azurerm_managed_disk.bifrost_disk.name
        data_disk_uri = azurerm_managed_disk.bifrost_disk.id
        kind          = "Managed"
        caching_mode  = "None"
      }
    }
  }

  depends_on = [azurerm_managed_disk.bifrost_disk]

  lifecycle {
    prevent_destroy = false
  }
}

resource "kubernetes_persistent_volume_claim" "bifrost_volume_claim" {
  metadata {
    name      = "${var.name_prefix}-volume-claim"
    namespace = kubernetes_namespace.this.metadata[0].name
  }

  spec {
    access_modes = ["ReadWriteOnce"]
    resources {
      requests = {
        storage = "${var.volume_size_gb}Gi"
      }
    }
    storage_class_name = "managed-premium"
    volume_name        = kubernetes_persistent_volume.bifrost_volume.metadata[0].name
  }
}

# =============================================================================
# Deployment
# =============================================================================

resource "kubernetes_deployment" "bifrost" {
  metadata {
    name      = local.service_name
    namespace = kubernetes_namespace.this.metadata[0].name
    labels = {
      app = local.service_name
    }
  }

  spec {
    replicas = var.desired_count

    selector {
      match_labels = {
        app = local.service_name
      }
    }

    template {
      metadata {
        labels = {
          app = local.service_name
        }
      }

      spec {
        security_context {
          fs_group               = 1000
          fs_group_change_policy = "OnRootMismatch"
        }

        # Init container to fix volume permissions
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
              cpu    = local.cpu_request
              memory = local.memory_request
            }
            limits = {
              cpu    = local.cpu_limit
              memory = local.memory_limit
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

        volume {
          name = "bifrost-volume"
          persistent_volume_claim {
            claim_name = kubernetes_persistent_volume_claim.bifrost_volume_claim.metadata[0].name
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
    kubernetes_persistent_volume_claim.bifrost_volume_claim,
  ]
}

# =============================================================================
# Service (ClusterIP)
# =============================================================================

resource "kubernetes_service" "bifrost" {
  metadata {
    name      = local.service_name
    namespace = kubernetes_namespace.this.metadata[0].name
    labels = {
      app = local.service_name
    }
  }

  spec {
    selector = {
      app = local.service_name
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

# =============================================================================
# Horizontal Pod Autoscaler (optional)
# =============================================================================

resource "kubernetes_horizontal_pod_autoscaler_v2" "bifrost" {
  count = var.enable_autoscaling ? 1 : 0

  metadata {
    name      = "${local.service_name}-hpa"
    namespace = kubernetes_namespace.this.metadata[0].name
  }

  spec {
    min_replicas = var.min_capacity
    max_replicas = var.max_capacity

    scale_target_ref {
      api_version = "apps/v1"
      kind        = "Deployment"
      name        = kubernetes_deployment.bifrost.metadata[0].name
    }

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

# =============================================================================
# Ingress (optional - when create_load_balancer = true)
# =============================================================================

resource "kubernetes_ingress_v1" "bifrost" {
  count = local.ingress_enabled ? 1 : 0

  metadata {
    name      = "${local.service_name}-ingress"
    namespace = kubernetes_namespace.this.metadata[0].name
    annotations = {
      "kubernetes.io/ingress.class" = "nginx"
    }
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
