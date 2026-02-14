# =============================================================================
# AWS EKS Service Module
# =============================================================================
#
# Deploys Bifrost on Amazon EKS with:
#   - IAM roles for EKS cluster and node group
#   - EKS cluster + managed node group (optional)
#   - Kubernetes namespace, secret, deployment, service
#   - EBS-backed persistent volume for SQLite data
#   - Horizontal Pod Autoscaler (optional)
#   - Ingress (optional, when domain_name is set)
# =============================================================================

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 2.20"
    }
  }
}

# --- Locals ---

locals {
  service_name      = "${var.name_prefix}-service"
  instance_type     = coalesce(var.node_machine_type, "t3.medium")
  availability_zone = "${var.region}a"

  common_labels = merge(var.tags, {
    app                          = local.service_name
    "app.kubernetes.io/name"     = local.service_name
    "app.kubernetes.io/instance" = var.name_prefix
  })
}

# =============================================================================
# IAM — EKS Cluster Role
# =============================================================================

data "aws_iam_policy_document" "eks_cluster_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["eks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "eks_cluster" {
  count = var.create_cluster ? 1 : 0

  name               = "${var.name_prefix}-eks-cluster-role"
  assume_role_policy = data.aws_iam_policy_document.eks_cluster_assume_role.json

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-eks-cluster-role"
  })
}

resource "aws_iam_role_policy_attachment" "eks_cluster_policy" {
  count = var.create_cluster ? 1 : 0

  role       = aws_iam_role.eks_cluster[0].name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSClusterPolicy"
}

# =============================================================================
# IAM — EKS Node Group Role
# =============================================================================

data "aws_iam_policy_document" "eks_node_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "eks_node" {
  count = var.create_cluster ? 1 : 0

  name               = "${var.name_prefix}-eks-node-role"
  assume_role_policy = data.aws_iam_policy_document.eks_node_assume_role.json

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-eks-node-role"
  })
}

resource "aws_iam_role_policy_attachment" "eks_worker_node_policy" {
  count = var.create_cluster ? 1 : 0

  role       = aws_iam_role.eks_node[0].name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy"
}

resource "aws_iam_role_policy_attachment" "eks_cni_policy" {
  count = var.create_cluster ? 1 : 0

  role       = aws_iam_role.eks_node[0].name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy"
}

resource "aws_iam_role_policy_attachment" "eks_ecr_read_only" {
  count = var.create_cluster ? 1 : 0

  role       = aws_iam_role.eks_node[0].name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly"
}

# =============================================================================
# EKS Cluster (optional — created only when create_cluster = true)
# =============================================================================

resource "aws_eks_cluster" "this" {
  count = var.create_cluster ? 1 : 0

  name     = "${var.name_prefix}-cluster"
  role_arn = aws_iam_role.eks_cluster[0].arn

  vpc_config {
    subnet_ids              = var.subnet_ids
    security_group_ids      = var.security_group_ids
    endpoint_public_access  = true
    endpoint_private_access = true
  }

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-cluster"
  })

  depends_on = [
    aws_iam_role_policy_attachment.eks_cluster_policy,
  ]
}

# =============================================================================
# EKS Managed Node Group
# =============================================================================

resource "aws_eks_node_group" "this" {
  count = var.create_cluster ? 1 : 0

  cluster_name    = aws_eks_cluster.this[0].name
  node_group_name = "${var.name_prefix}-node-group"
  node_role_arn   = aws_iam_role.eks_node[0].arn
  subnet_ids      = var.subnet_ids

  instance_types = [local.instance_type]
  disk_size      = 50

  scaling_config {
    desired_size = var.node_count
    min_size     = var.node_count
    max_size     = var.node_count
  }

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-node-group"
  })

  depends_on = [
    aws_iam_role_policy_attachment.eks_worker_node_policy,
    aws_iam_role_policy_attachment.eks_cni_policy,
    aws_iam_role_policy_attachment.eks_ecr_read_only,
  ]
}

# =============================================================================
# Kubernetes Namespace
# =============================================================================

resource "kubernetes_namespace" "bifrost" {
  metadata {
    name   = var.kubernetes_namespace
    labels = local.common_labels
  }

  depends_on = [
    aws_eks_cluster.this,
    aws_eks_node_group.this,
  ]
}

# =============================================================================
# Kubernetes Secret — config.json
# =============================================================================

resource "kubernetes_secret" "bifrost_config" {
  metadata {
    name      = "${var.name_prefix}-config"
    namespace = kubernetes_namespace.bifrost.metadata[0].name
  }

  data = {
    "config.json" = var.config_json
  }

  type = "Opaque"

  depends_on = [kubernetes_namespace.bifrost]
}

# =============================================================================
# EBS Volume + Persistent Volume + PVC — SQLite persistence
# =============================================================================

resource "aws_ebs_volume" "bifrost_data" {
  availability_zone = local.availability_zone
  size              = var.volume_size_gb
  type              = "gp3"
  encrypted         = true

  tags = merge(var.tags, {
    Name = "${var.name_prefix}-data"
  })

  lifecycle {
    ignore_changes = [tags]
  }
}

resource "kubernetes_persistent_volume" "bifrost_data" {
  metadata {
    name = "${var.name_prefix}-data-pv"
  }

  spec {
    capacity = {
      storage = "${var.volume_size_gb}Gi"
    }

    access_modes                     = ["ReadWriteOnce"]
    persistent_volume_reclaim_policy = "Retain"
    storage_class_name               = "gp3"

    persistent_volume_source {
      aws_elastic_block_store {
        volume_id = aws_ebs_volume.bifrost_data.id
        fs_type   = "ext4"
      }
    }

    node_affinity {
      required {
        node_selector_term {
          match_expressions {
            key      = "topology.kubernetes.io/zone"
            operator = "In"
            values   = [local.availability_zone]
          }
        }
      }
    }
  }

  depends_on = [aws_ebs_volume.bifrost_data]

  lifecycle {
    prevent_destroy = false
  }
}

resource "kubernetes_persistent_volume_claim" "bifrost_data" {
  metadata {
    name      = "${var.name_prefix}-data-pvc"
    namespace = kubernetes_namespace.bifrost.metadata[0].name
  }

  spec {
    access_modes = ["ReadWriteOnce"]

    resources {
      requests = {
        storage = "${var.volume_size_gb}Gi"
      }
    }

    storage_class_name = "gp3"
    volume_name        = kubernetes_persistent_volume.bifrost_data.metadata[0].name
  }

  depends_on = [kubernetes_persistent_volume.bifrost_data]
}

# =============================================================================
# Kubernetes Deployment
# =============================================================================

resource "kubernetes_deployment" "bifrost" {
  metadata {
    name      = local.service_name
    namespace = kubernetes_namespace.bifrost.metadata[0].name

    labels = local.common_labels
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
        labels = merge(local.common_labels, {
          app = local.service_name
        })
      }

      spec {
        # --- Pod-level security context ---
        security_context {
          fs_group               = 1000
          fs_group_change_policy = "OnRootMismatch"
        }

        # --- Init container: fix volume permissions ---
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

        # --- Main container ---
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

          # Config secret (mounted as a single file via sub_path)
          volume_mount {
            name       = "config-volume"
            mount_path = "/app/data/config.json"
            sub_path   = "config.json"
          }

          # Liveness probe
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

          # Readiness probe
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

        # --- Volumes ---
        volume {
          name = "bifrost-volume"
          persistent_volume_claim {
            claim_name = kubernetes_persistent_volume_claim.bifrost_data.metadata[0].name
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
    kubernetes_persistent_volume_claim.bifrost_data,
  ]
}

# =============================================================================
# Kubernetes Service (ClusterIP)
# =============================================================================

resource "kubernetes_service" "bifrost" {
  metadata {
    name      = local.service_name
    namespace = kubernetes_namespace.bifrost.metadata[0].name

    labels = local.common_labels
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

  depends_on = [kubernetes_deployment.bifrost]
}

# =============================================================================
# Horizontal Pod Autoscaler (optional)
# =============================================================================

resource "kubernetes_horizontal_pod_autoscaler_v2" "bifrost" {
  count = var.enable_autoscaling ? 1 : 0

  metadata {
    name      = "${local.service_name}-hpa"
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

# =============================================================================
# Kubernetes Ingress (optional — created when create_load_balancer is true)
# =============================================================================

resource "kubernetes_ingress_v1" "bifrost" {
  count = var.create_load_balancer ? 1 : 0

  metadata {
    name      = "${local.service_name}-ingress"
    namespace = kubernetes_namespace.bifrost.metadata[0].name

    labels = local.common_labels

    annotations = merge({
      "kubernetes.io/ingress.class"                = "alb"
      "alb.ingress.kubernetes.io/scheme"           = "internet-facing"
      "alb.ingress.kubernetes.io/target-type"      = "ip"
      "alb.ingress.kubernetes.io/listen-ports"     = var.certificate_arn != null ? "[{\"HTTPS\":443}]" : "[{\"HTTP\":80}]"
      "alb.ingress.kubernetes.io/healthcheck-path" = var.health_check_path
      }, var.certificate_arn != null ? {
      "alb.ingress.kubernetes.io/certificate-arn" = var.certificate_arn
    } : {})
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

  depends_on = [kubernetes_service.bifrost]
}
