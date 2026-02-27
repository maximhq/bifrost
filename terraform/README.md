# Bifrost Terraform Modules

Deploy Bifrost on AWS, GCP, Azure, or any Kubernetes cluster using a single Terraform module.

## Quick Start

Reference the module directly from GitHub. Pin to a specific release tag using `?ref=`:

```hcl
module "bifrost" {
  source         = "github.com/maximhq/bifrost//terraform/modules/bifrost?ref=terraform/v0.1.0"
  cloud_provider = "aws"       # "aws" | "gcp" | "azure" | "kubernetes"
  service        = "ecs"       # AWS: "ecs" | "eks", GCP: "gke" | "cloud-run", Azure: "aks" | "aci", K8s: "deployment"
  region         = "us-east-1"
  image_tag      = "v1.4.6"

  # Option A: Provide a config.json file
  config_json_file = "./config.json"

  # Option B: Build config from Terraform variables (overrides matching keys from file)
  providers_config = {
    openai = { keys = [{ value = var.openai_key, weight = 1 }] }
  }
  config_store = {
    enabled = true
    type    = "postgres"
    config  = { host = var.db_host, port = "5432", user = "bifrost", password = var.db_password, db_name = "bifrost" }
  }
}
```

## Supported Deployments

| Cloud | Service | Description |
|-------|---------|-------------|
| AWS | `ecs` | ECS Fargate with ALB, Secrets Manager, auto-scaling |
| AWS | `eks` | EKS with K8s Deployment, PVC for SQLite, HPA |
| GCP | `gke` | GKE with K8s Deployment, persistent disk, HPA |
| GCP | `cloud-run` | Cloud Run v2 with Secret Manager, auto-scaling |
| Azure | `aks` | AKS with K8s Deployment, managed disk, HPA |
| Azure | `aci` | Azure Container Instances (single instance, dev/test) |
| Kubernetes | `deployment` | Any K8s cluster with Deployment, PVC, HPA, Ingress |

## Configuration

Bifrost config can come from two sources simultaneously. Terraform variables override matching keys from the base file.

1. **File-based**: Set `config_json_file` to a path or `config_json` to a raw JSON string.
2. **Variable-based**: Set individual variables (`config_store`, `logs_store`, `providers_config`, `auth_config`, etc.) corresponding to top-level keys in [config.schema.json](../transports/config.schema.json).

All 16 top-level config properties from the schema are supported as variables:
`encryption_key`, `auth_config`, `client`, `framework`, `providers_config`, `governance`, `mcp`, `vector_store`, `config_store`, `logs_store`, `cluster_config`, `saml_config`, `load_balancer_config`, `guardrails_config`, `plugins`, `audit_logs`.

## Directory Structure

```text
terraform/
  modules/bifrost/              # Top-level module (the only thing you call)
    aws/                        # AWS platform (VPC, SG, IAM, Secrets Manager)
      services/ecs/             # ECS Fargate
      services/eks/             # EKS + K8s resources
    gcp/                        # GCP platform (VPC, firewall, Secret Manager, SA)
      services/gke/             # GKE + K8s resources
      services/cloud-run/       # Cloud Run v2
    azure/                      # Azure platform (VNet, NSG, Key Vault, identity)
      services/aks/             # AKS + K8s resources
      services/aci/             # Azure Container Instances
    kubernetes/                 # Generic K8s (any cluster, no cloud APIs)
  examples/
    aws-ecs/                    # Deploy on ECS Fargate
    gcp-gke/                    # Deploy on GKE
    azure-aks/                  # Deploy on AKS
    kubernetes/                 # Deploy on any K8s cluster
```

## Examples

Each example directory contains `main.tf`, `variables.tf`, `outputs.tf`, `terraform.tfvars.example`, and a `README.md`.

```bash
cd examples/aws-ecs
cp terraform.tfvars.example terraform.tfvars
# Edit terraform.tfvars with your values
terraform init
terraform plan
terraform apply
```

## Key Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `cloud_provider` | (required) | `"aws"`, `"gcp"`, `"azure"`, or `"kubernetes"` |
| `service` | (required) | Service type (see table above) |
| `region` | (required) | Cloud region |
| `image_tag` | `"latest"` | Bifrost Docker image tag |
| `desired_count` | `1` | Number of replicas |
| `cpu` | `512` | CPU units (ECS) or millicores (K8s) |
| `memory` | `1024` | Memory in MB |
| `create_load_balancer` | `false` | Create a load balancer |
| `enable_autoscaling` | `false` | Enable auto-scaling |
| `create_cluster` | `true` | Create new cluster (set `false` to use existing) |
| `storage_class_name` | `"standard"` | K8s StorageClass for PVC (generic K8s only) |
| `ingress_class_name` | `"nginx"` | Ingress controller class (generic K8s only) |
| `ingress_annotations` | `{}` | Ingress annotations (generic K8s only) |

## Outputs

| Output | Description |
|--------|-------------|
| `service_url` | URL to access Bifrost |
| `health_check_url` | Health endpoint URL |
