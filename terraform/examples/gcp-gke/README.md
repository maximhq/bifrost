# Bifrost on GCP GKE

Deploys Bifrost as a Kubernetes workload on Google Kubernetes Engine.

## Prerequisites

- GCP project with billing enabled
- `gcloud` CLI authenticated (`gcloud auth application-default login`)
- Terraform >= 1.0
- GKE API enabled (`gcloud services enable container.googleapis.com`)

## Usage

```bash
# Copy and edit the example variables file
cp terraform.tfvars.example terraform.tfvars

# Deploy
terraform init
terraform plan
terraform apply
```

## Configuration

Two approaches can be combined:

1. **File-based** -- Set `config_json_file` to point to an existing `config.json`.
2. **Variable-based** -- Set individual variables (`config_store`, `logs_store`, `providers_config`). These override matching keys from the file.

See `terraform.tfvars.example` for examples of both.

## Cleanup

```bash
terraform destroy
```
