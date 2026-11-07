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

Unused cloud modules create nothing (`count = 0`), but Terraform still configures every provider referenced in the tree during `plan`. Through this root module a plan needs credentials for AWS, Google, and Azure at once: the `aws` provider wants a credential chain, `google` wants Application Default Credentials, and `azurerm` wants an `az login` or service principal. The `kubernetes` provider is lenient and needs nothing until a cluster resource is used.

If you only have one cloud's credentials, call that cloud's platform module directly (`./aws`, `./gcp`, `./azure`, `./kubernetes`) and pass it the resolved `config_json`; `examples/aws-ecs-postgres` and `examples/gcp-cloud-run-postgres` do exactly that.

The root module declares its own `azurerm` provider block (`features {}` plus `skip_provider_registration`) so that AWS, GCP and Kubernetes callers need no Azure configuration. Terraform treats that block as a separate provider configuration: an `azurerm` block in your own root module, including its `subscription_id`, does not configure it. Through the root module, select the Azure subscription with the environment (`ARM_SUBSCRIPTION_ID`, or `az account set`) and keep `azurerm` within `>= 3.0, < 5.0`. To pass a fully configured `azurerm` provider, call `./azure` directly.

The blocks below show the provider each cloud needs for its own resources.

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

## Implemented Deployments

| Cloud Provider | Service      | Description                        | Qualification |
|----------------|--------------|------------------------------------|---------------|
| `aws`          | `ecs`        | AWS Elastic Container Service      | Preview |
| `aws`          | `eks`        | AWS Elastic Kubernetes Service     | Preview |
| `gcp`          | `gke`        | Google Kubernetes Engine           | Preview |
| `gcp`          | `cloud-run`  | Google Cloud Run                   | Preview |
| `azure`        | `aks`        | Azure Kubernetes Service           | Preview |
| `azure`        | `aci`        | Azure Container Instances          | Preview |
| `kubernetes`   | `deployment` | Any existing Kubernetes cluster    | Preview |

Invalid combinations (e.g. `cloud_provider = "aws"` with `service = "gke"`) are rejected at plan time with a clear error message.

### Managed database and generated secrets

`aws/ecs` and `gcp/cloud-run` can provision their own PostgreSQL 16 and runtime secrets, which is what the one-click AWS and GCP buttons use:

| Variable | Default | Effect |
|----------|---------|--------|
| `create_database` | `false` | AWS: RDS PostgreSQL 16 in the service VPC, reachable only from the service security group. GCP: Cloud SQL PostgreSQL 16 mounted into Cloud Run over the Cloud SQL connector at `/cloudsql`. Both stores are pointed at it through `PG_HOST`, `PG_PORT`, `PG_USER`, `PG_PASSWORD`, `PG_DATABASE`, and `PG_SSL_MODE` env references unless `config_store` or `logs_store` is set explicitly or already present in the base config. |
| `database_name`, `database_user` | `bifrost` | Database and application user; the password is generated and stored in Secrets Manager or Secret Manager. |
| `database_instance_class` | `db.t4g.small` / `db-custom-1-3840` | RDS instance class or Cloud SQL tier. |
| `database_storage_gb` | `20` | Disk size. |
| `database_deletion_protection` | `false` | Protect the database from `terraform destroy`. Switch it back off and apply before destroying. |
| `database_skip_final_snapshot` | `true` | AWS only. Set `false` to keep a final RDS snapshot named `<name_prefix>-db-final` on destroy; independent of deletion protection. |
| `generate_secrets` | `false` | Generate `BIFROST_ENCRYPTION_KEY` and `BIFROST_SETUP_TOKEN`, store them in the cloud secret manager, inject them into the container, and reference them from `config.json`. Explicit `encryption_key` or `setup_token` values, and values already present in the base config, win. |
| `setup_token` | `null` | Bootstrap token for the first admin account, written to `config.json` as `setup_token`. |

Setting either flag on another target fails at plan time. The generated setup token is exposed through the `setup_token` output.

> **Preview:** these targets have maintained Terraform implementations and mocked plan tests, but are not continuously deployed to live cloud accounts. Validate the generated plan, networking, IAM, persistence, and rollback in a non-production account before adoption.

## Configuration Merging

The module supports three ways to provide Bifrost configuration, which are merged in order of precedence:

1. **Base config file** (`config_json_file`) -- path to a `config.json` file on disk.
2. **Base config string** (`config_json`) -- complete JSON config as a string (used if no file is provided).
3. **Individual variables** (`encryption_key`, `auth_config`, `providers_config`, etc.) -- override matching top-level keys from the base config.

Individual variables always take precedence over the base config. Sensitive values passed through Terraform variables can still be stored in Terraform state. Use an encrypted remote backend with tightly restricted access, audit state reads, and avoid exposing resolved configuration in CI logs.

Set `schema_url` to write a mirrored schema location into `$schema` for isolated deployments. It accepts an HTTP(S) URL, `file://` URL, or filesystem path, and defaults to `https://www.getbifrost.ai/schema`.

### Configurable Sections

The following 18 top-level sections of the [Bifrost config schema](../../../transports/config.schema.json) are exposed as Terraform variables (the schema defines more; pass any other section through `config_json_file` or `config_json`):

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
| `scim_config`        | `scim_config`        | SCIM/SSO (Okta, Entra)                         |
| `load_balancer_config` | `load_balancer_config` | Intelligent load balancer               |
| `guardrails_config`  | `guardrails_config`  | Guardrails (rules, providers)                  |
| `plugins`            | `plugins`            | Plugin configuration array                     |
| `audit_logs`         | `audit_logs`         | Audit logging (disabled, hmac_key)             |
| `websocket`          | `websocket`          | WebSocket gateway tuning                       |
| `server`             | `server`             | HTTP server (read_buffer_size, base_path)      |

For `scim_config` with `provider = "okta"`, set `config.issuerUrl`, `config.clientId`, `config.clientSecret`, and `config.apiToken`.

## Outputs

| Output             | Description                                     |
|--------------------|--------------------------------------------------|
| `service_url`      | URL to access the deployed Bifrost service       |
| `health_check_url` | URL to the `/health` endpoint                    |
| `config_json`      | Resolved configuration JSON (sensitive, for debugging) |
| `setup_token`      | Generated first-admin token when `generate_secrets` is true (sensitive) |
| `database_endpoint` | RDS `host:port` or Cloud SQL connection name when `create_database` is true |

## Testing

Tests use Terraform's native test framework (requires Terraform >= 1.7) with `mock_provider` — no cloud credentials needed. They validate configuration and planned resources; they do not prove that a live cloud deployment, upgrade, or rollback succeeds.

```bash
cd terraform/modules/bifrost
terraform init
terraform test
```

Test files are in `tests/` and cover all 7 deployment targets:

| File                        | Coverage                                         |
|-----------------------------|--------------------------------------------------|
| `root_validation.tftest.hcl`| Valid/invalid cloud_provider + service combos     |
| `config_merging.tftest.hcl` | Config precedence, schema location injection      |
| `aws_ecs.tftest.hcl`        | ECS: ALB, autoscaling, private subnets            |
| `aws_eks.tftest.hcl`        | EKS: cluster, HPA, ingress, HTTPS, nodes          |
| `aws_shared.tftest.hcl`     | VPC/SG creation vs existing, ECS isolation         |
| `gcp_gke.tftest.hcl`        | GKE: cluster, HPA, ingress, nodes, volumes         |
| `gcp_cloudrun.tftest.hcl`   | Cloud Run: public access, domain, scaling          |
| `azure_aks.tftest.hcl`      | AKS: cluster, HPA, ingress, resource groups        |
| `azure_aci.tftest.hcl`      | ACI: compute, resource groups, VNets               |
| `kubernetes.tftest.hcl`     | Generic K8s: storage class, ingress, annotations   |
| `network_inputs.tftest.hcl` | GCP/Azure: existing VPC and subnets must pair      |
| `aws_database.tftest.hcl`   | RDS: final snapshot independent of deletion protection |
| `aws_load_balancer.tftest.hcl` | ECS: ALB security group admits allowed_cidr, tasks admit only the ALB |

## Examples

See the [`examples/`](../../examples/) directory for complete deployment examples for each cloud provider and service combination. [`aws-ecs-postgres`](../../examples/aws-ecs-postgres/) and [`gcp-cloud-run-postgres`](../../examples/gcp-cloud-run-postgres/) are the one-click stacks driven by `deploy/aws/deploy.sh` and `deploy/gcp/deploy.sh`.
