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

  # Option C: Let Terraform provision PostgreSQL and auto-wire config_store / logs_store
  create_postgresql = true
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
    aws/postgresql/             # AWS RDS PostgreSQL
    gcp/postgresql/             # GCP Cloud SQL PostgreSQL
    azure/postgresql/           # Azure Flexible Server PostgreSQL
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

### PostgreSQL Variables

When `create_postgresql = true`, Bifrost provisions a managed PostgreSQL instance (or K8s StatefulSet) and automatically configures `config_store` and `logs_store` to use it.

| Variable | Default | Description |
|----------|---------|-------------|
| `create_postgresql` | `false` | Create a managed PostgreSQL instance |
| `postgresql_engine_version` | `"16"` | PostgreSQL engine version |
| `postgresql_instance_class` | `null` | Instance class (uses cloud-specific defaults) |
| `postgresql_storage_gb` | `20` | Allocated storage in GB |
| `postgresql_database_name` | `"bifrost"` | Initial database name |
| `postgresql_username` | `"bifrost"` | Master username |
| `postgresql_password` | `null` | Master password (auto-generated if null) |
| `postgresql_backup_retention_days` | `7` | Backup retention in days |
| `postgresql_multi_az` | `false` | Enable multi-AZ / HA deployment |
| `postgresql_publicly_accessible` | `false` | Allow public access (AWS only) |

**Auto-wiring behavior**: When `create_postgresql = true`, the module automatically sets `config_store` and `logs_store` to use the provisioned PostgreSQL instance. If you explicitly set either variable, your value takes precedence.

**Cloud-specific implementations**:
- **AWS**: RDS PostgreSQL instance with encrypted storage, private subnets, security group restricted to Bifrost
- **GCP**: Cloud SQL PostgreSQL with private IP via VPC peering, automatic backups, SSL required
- **Azure**: Flexible Server with VNet integration via delegated subnet, private DNS zone
- **Kubernetes**: PostgreSQL StatefulSet with PVC, ClusterIP service, health probes

## Outputs

| Output | Description |
|--------|-------------|
| `service_url` | URL to access Bifrost |
| `health_check_url` | Health endpoint URL |
| `postgresql_endpoint` | PostgreSQL host:port (null when `create_postgresql = false`) |
| `postgresql_password` | PostgreSQL password (sensitive, null when `create_postgresql = false`) |
