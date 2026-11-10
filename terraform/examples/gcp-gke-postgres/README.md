# Bifrost on GKE with managed PostgreSQL

The one-click GKE stack: a new cluster, one Bifrost pod exposed through a public `LoadBalancer` Service, Cloud SQL PostgreSQL 16 (db-custom-1-3840, private IP via service networking) for both Bifrost stores, and generated runtime secrets delivered through a Kubernetes Secret.

The fastest path is the deploy script from Cloud Shell:

```bash
git clone --depth 1 https://github.com/maximhq/bifrost && bifrost/deploy/gke/deploy.sh
```

## Why two phases

The kubernetes provider needs a kubeconfig for a cluster that does not exist yet. The script applies the cluster first with `terraform apply -target`, writes the kubeconfig with the cloud CLI, then applies the rest. Manual users follow the same order:

```bash
cp terraform.tfvars.example terraform.tfvars   # set project_id (and region) before applying
terraform init
# Phase 1: the GKE cluster and node pool only.
terraform apply \
  -target='module.bifrost.module.gke[0].google_container_cluster.bifrost' \
  -target='module.bifrost.module.gke[0].google_container_node_pool.bifrost'
# Write the kubeconfig the kubernetes provider needs for phase 2.
gcloud container clusters get-credentials <name_prefix>-cluster --region <region> --project <project_id>
# Phase 2: database, Secrets and Bifrost.
terraform apply
terraform output -raw setup_token; echo
```

## What gets created

| Resource | Purpose |
|---|---|
| Network scaffold | Shared with the other targets of the platform module |
| GKE cluster and node pool | Runs the Bifrost pod |
| Cloud SQL PostgreSQL 16 (db-custom-1-3840, private IP via service networking) | Config store and log store, reachable only from inside the deployment's network |
| Kubernetes Secrets `bifrost-config` and `bifrost-runtime` (`<name_prefix>-config` and `<name_prefix>-runtime`; default prefix shown) | `config.json`; database password, encryption key, setup token |
| `LoadBalancer` Service | Public TCP load balancer; the URL is the `service_url` output |

The Service is plain HTTP. Add TLS through an ingress controller or the cloud's managed certificates before production traffic. Terraform state stays in this directory; move it to GCS for anything long-lived.

## Enterprise

Set `image_repository` to the registry path Maxim issued for your organization and add an image pull secret as the enterprise guide describes. Enterprise requires PostgreSQL for both stores, which this example provisions.

## Cleanup

```bash
terraform destroy
```
