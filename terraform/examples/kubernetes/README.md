# Bifrost on Kubernetes

Deploys Bifrost on any existing Kubernetes cluster using a Deployment, PVC, and optional Ingress + HPA.

## Prerequisites

- A running Kubernetes cluster with `kubectl` access
- A kubeconfig file (default: `~/.kube/config`)
- A StorageClass that supports dynamic provisioning (e.g. `standard`, `gp2`, `premium-rwo`)
- Terraform >= 1.0

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

## Ingress

To expose Bifrost externally, set `create_load_balancer = true` and configure:

- `ingress_class_name` -- Your ingress controller class (e.g. `nginx`, `traefik`, `haproxy`)
- `domain_name` -- The hostname for the Ingress rule
- `ingress_annotations` -- Any annotations your ingress controller needs (e.g. TLS, rate limiting)

## Cleanup

```bash
terraform destroy
```
