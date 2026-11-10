# Bifrost on AKS with managed PostgreSQL

The one-click AKS stack: a new cluster, one Bifrost pod exposed through a public `LoadBalancer` Service, PostgreSQL Flexible Server 16 (B_Standard_B1ms, 32 GB, TLS) for both Bifrost stores, and generated runtime secrets delivered through a Kubernetes Secret.

The fastest path is the deploy script from Azure Cloud Shell:

```bash
git clone --depth 1 https://github.com/maximhq/bifrost && bifrost/deploy/aks/deploy.sh
```

## Why two phases

The kubernetes provider needs a kubeconfig for a cluster that does not exist yet. The script applies the cluster first with `terraform apply -target`, writes the kubeconfig with the cloud CLI, then applies the rest. Manual users follow the same order:

```bash
cp terraform.tfvars.example terraform.tfvars
terraform init
# Phase 1: the AKS cluster only.
terraform apply -target='module.bifrost.module.aks[0].azurerm_kubernetes_cluster.this'
# Write the kubeconfig the kubernetes provider needs for phase 2. These are the
# default names; substitute yours if you changed name_prefix or resource_group_name.
az aks get-credentials --resource-group bifrost-rg --name bifrost-aks
# Phase 2: database, Secrets and Bifrost.
terraform apply
terraform output -raw setup_token; echo
```

## What gets created

| Resource | Purpose |
|---|---|
| Network scaffold | Shared with the other targets of the platform module |
| AKS cluster and node pool | Runs the Bifrost pod |
| PostgreSQL Flexible Server 16 (B_Standard_B1ms, 32 GB, TLS enforced) | Config store and log store. Public endpoint behind Azure's "allow Azure services" firewall rule, which admits any Azure-hosted address, not only this deployment; the generated credentials are the access control |
| Kubernetes Secrets `bifrost-config` and `bifrost-runtime` (`<name_prefix>-config` and `<name_prefix>-runtime`; default prefix shown) | `config.json`; database password, encryption key, setup token |
| `LoadBalancer` Service | Public Azure load balancer restricted to `allowed_cidr` (Service source ranges plus an NSG rule for port 80); the URL is the `service_url` output |

The Service is plain HTTP. Add TLS through an ingress controller or the cloud's managed certificates before production traffic. Terraform state stays in this directory; move it to Azure Storage for anything long-lived.

## Enterprise

Set `image_repository` to the registry path Maxim issued for your organization and add an image pull secret as the enterprise guide describes. Enterprise requires PostgreSQL for both stores, which this example provisions.

## Cleanup

```bash
terraform destroy
```
