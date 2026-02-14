# Bifrost Terraform Module

Single entry point for deploying [Bifrost](https://github.com/maximhq/bifrost) on AWS, GCP, or Azure. This module handles configuration merging, image resolution, and routes to the appropriate cloud-provider sub-module based on your `cloud_provider` and `service` selections.

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

## Supported Deployments

| Cloud Provider | Service     | Description                        |
|----------------|-------------|------------------------------------|
| `aws`          | `ecs`       | AWS Elastic Container Service      |
| `aws`          | `eks`       | AWS Elastic Kubernetes Service     |
| `gcp`          | `cloud-run` | Google Cloud Run                   |
| `gcp`          | `gke`       | Google Kubernetes Engine           |
| `azure`        | `aci`       | Azure Container Instances          |
| `azure`        | `aks`       | Azure Kubernetes Service           |

## Configuration Merging

The module supports three ways to provide Bifrost configuration, which are merged in order of precedence:

1. **Base config file** (`config_json_file`) -- path to a `config.json` file on disk.
2. **Base config string** (`config_json`) -- complete JSON config as a string (used if no file is provided).
3. **Individual variables** (`encryption_key`, `auth_config`, `providers_config`, etc.) -- override matching top-level keys from the base config.

Individual variables always take precedence over the base config. This lets you keep secrets out of your config file and inject them via Terraform variables or a secrets manager.

## Outputs

| Output             | Description                                     |
|--------------------|--------------------------------------------------|
| `service_url`      | URL to access the deployed Bifrost service       |
| `health_check_url` | URL to the `/health` endpoint                    |
| `config_json`      | Resolved configuration JSON (sensitive, for debugging) |

## Examples

See the [`examples/`](../../examples/) directory for complete deployment examples for each cloud provider and service combination.
