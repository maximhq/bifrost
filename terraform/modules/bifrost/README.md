# Bifrost Terraform Module

Single entry point for deploying [Bifrost](https://github.com/maximhq/bifrost) on AWS, GCP, Azure, or any Kubernetes cluster. This module handles configuration merging, image resolution, and routes to the appropriate cloud-provider sub-module based on your `cloud_provider` and `service` selections.

## Usage

```hcl
module "bifrost" {
  source = "github.com/maximhq/bifrost//terraform/modules/bifrost"

  cloud_provider  = "aws"
  service         = "ecs"
  region          = "us-east-1"
  config_json_file = "${path.module}/config.json"

  # Override specific config sections via variables
  encryption_key = var.encryption_key
  auth_config = {
    admin_username = "admin"
    admin_password = var.admin_password
    is_enabled     = true
  }
  providers_config = {
    openai = {
      api_key = var.openai_api_key
    }
  }

  # Infrastructure
  cpu                  = 1024
  memory               = 2048
  desired_count        = 2
  create_load_balancer = true
  enable_autoscaling   = true
  max_capacity         = 5

  tags = {
    Environment = "production"
  }
}
```

## Provider Configuration

You only need to configure the Terraform providers for the cloud you are deploying to. Unused cloud modules are skipped automatically (`count = 0`).

**AWS (ECS / EKS):**

```hcl
provider "aws" {
  region = "us-east-1"
}

# EKS also requires the kubernetes provider
provider "kubernetes" {
  host                   = module.bifrost.cluster_endpoint
  cluster_ca_certificate = base64decode(module.bifrost.cluster_ca)
  token                  = data.aws_eks_cluster_auth.this.token
}
```

**GCP (GKE / Cloud Run):**

```hcl
provider "google" {
  project = "my-project-id"
  region  = "us-central1"
}
```

**Azure (AKS / ACI):**

```hcl
provider "azurerm" {
  features {}
}
```

**Generic Kubernetes:**

```hcl
provider "kubernetes" {
  config_path = "~/.kube/config"
}
```

## Supported Deployments

| Cloud Provider | Service      | Description                        |
|----------------|--------------|------------------------------------|
| `aws`          | `ecs`        | AWS Elastic Container Service      |
| `aws`          | `eks`        | AWS Elastic Kubernetes Service     |
| `gcp`          | `gke`        | Google Kubernetes Engine           |
| `gcp`          | `cloud-run`  | Google Cloud Run                   |
| `azure`        | `aks`        | Azure Kubernetes Service           |
| `azure`        | `aci`        | Azure Container Instances          |
| `kubernetes`   | `deployment` | Any existing Kubernetes cluster    |

Invalid combinations (e.g. `cloud_provider = "aws"` with `service = "gke"`) are rejected at plan time with a clear error message.

## Configuration Merging

The module supports three ways to provide Bifrost configuration, which are merged in order of precedence:

1. **Base config file** (`config_json_file`) -- path to a `config.json` file on disk.
2. **Base config string** (`config_json`) -- complete JSON config as a string (used if no file is provided).
3. **Individual variables** (`encryption_key`, `auth_config`, `providers_config`, etc.) -- override matching top-level keys from the base config.

Individual variables always take precedence over the base config. This lets you keep secrets out of your config file and inject them via Terraform variables or a secrets manager.

### Configurable Sections

All 18 top-level properties from the [Bifrost config schema](../../../transports/config.schema.json) are exposed as Terraform variables:

| Variable             | Schema Key           | Description                                    |
|----------------------|----------------------|------------------------------------------------|
| `encryption_key`     | `encryption_key`     | Encryption key for sensitive data (Argon2id)   |
| `auth_config`        | `auth_config`        | Authentication (admin credentials, SSO)        |
| `client`             | `client`             | Client settings (CORS, logging, body size)     |
| `framework`          | `framework`          | Framework settings (pricing)                   |
| `providers_config`   | `providers`          | LLM provider configurations                   |
| `governance`         | `governance`         | Budgets, rate limits, virtual keys, teams      |
| `mcp`                | `mcp`                | Model Context Protocol settings                |
| `vector_store`       | `vector_store`       | Vector database configuration                  |
| `config_store`       | `config_store`       | Config storage (SQLite/Postgres)               |
| `logs_store`         | `logs_store`         | Logging storage (SQLite/Postgres)              |
| `cluster_config`     | `cluster_config`     | Cluster mode (peers, gossip, discovery)        |
| `saml_config`        | `saml_config`        | SAML/SSO (Okta, Entra)                         |
| `load_balancer_config` | `load_balancer_config` | Intelligent load balancer               |
| `guardrails_config`  | `guardrails_config`  | Guardrails (rules, providers)                  |
| `plugins`            | `plugins`            | Plugin configuration array                     |
| `audit_logs`         | `audit_logs`         | Audit logging (disabled, hmac_key)             |
| `websocket`          | `websocket`          | WebSocket gateway tuning                       |

## Outputs

| Output             | Description                                     |
|--------------------|--------------------------------------------------|
| `service_url`      | URL to access the deployed Bifrost service       |
| `health_check_url` | URL to the `/health` endpoint                    |
| `config_json`      | Resolved configuration JSON (sensitive, for debugging) |

## Testing

Tests use Terraform's native test framework (requires Terraform >= 1.7) with `mock_provider` — no cloud credentials needed.

```bash
cd terraform/modules/bifrost
terraform init
terraform test
```

Test files are in `tests/` and cover all 7 deployment targets:

| File                        | Coverage                                         |
|-----------------------------|--------------------------------------------------|
| `root_validation.tftest.hcl`| Valid/invalid cloud_provider + service combos     |
| `config_merging.tftest.hcl` | Config precedence, schema URL injection           |
| `aws_ecs.tftest.hcl`        | ECS: ALB, autoscaling, private subnets            |
| `aws_eks.tftest.hcl`        | EKS: cluster, HPA, ingress, HTTPS, nodes          |
| `aws_shared.tftest.hcl`     | VPC/SG creation vs existing, ECS isolation         |
| `gcp_gke.tftest.hcl`        | GKE: cluster, HPA, ingress, nodes, volumes         |
| `gcp_cloudrun.tftest.hcl`   | Cloud Run: public access, domain, scaling          |
| `azure_aks.tftest.hcl`      | AKS: cluster, HPA, ingress, resource groups        |
| `azure_aci.tftest.hcl`      | ACI: compute, resource groups, VNets               |
| `kubernetes.tftest.hcl`     | Generic K8s: storage class, ingress, annotations   |

## Examples

See the [`examples/`](../../examples/) directory for complete deployment examples for each cloud provider and service combination.
