# Bifrost on Google Cloud Run with Cloud SQL

The one-click GCP stack: a public Cloud Run service, a Cloud SQL PostgreSQL 16 instance for both Bifrost stores, and generated runtime secrets in Secret Manager.

The fastest path is the deploy script from Cloud Shell:

```bash
git clone --depth 1 https://github.com/maximhq/bifrost && bifrost/deploy/gcp/deploy.sh
```

## Prerequisites

- A GCP project with billing enabled and permission to manage Cloud Run, Cloud SQL, Secret Manager, IAM and Compute
- These APIs enabled (the deploy script enables them): `run`, `sqladmin`, `secretmanager`, `iam`, `compute`
- Terraform >= 1.7 (preinstalled in Cloud Shell)

## Manual usage

```bash
cp terraform.tfvars.example terraform.tfvars   # set project_id
terraform init
terraform apply
terraform output -raw setup_token; echo
```

Open the `service_url` output, create the first admin account with the setup token, then add providers and a virtual key.

## What gets created

| Resource | Purpose |
|---|---|
| VPC and subnet | Network scaffold shared with the other GCP targets |
| Service account with Secret Manager, logging and Cloud SQL client roles | Identity for the Cloud Run service |
| Cloud SQL PostgreSQL 16 (`db-custom-1-3840`, 20 GB SSD, backups with PITR) | Config store and log store |
| Secret Manager `<prefix>-config`, `<prefix>-db-password`, `<prefix>-encryption-key`, `<prefix>-setup-token` | `config.json` and runtime secrets |
| Cloud Run v2 service with the Cloud SQL connector mounted at `/cloudsql` | Runs the Bifrost image behind Google-managed TLS |

Bifrost connects to Cloud SQL over the connector's unix socket, so the instance has no authorized networks and no private IP wiring is needed.

## Enterprise

Set `image_repository` to the Artifact Registry path Maxim issued for your organization. Grant the created service account `roles/artifactregistry.reader` on that repository, or follow the enterprise deployment guide for Workload Identity Federation.

## Cleanup

```bash
terraform destroy
```

Cloud SQL instance names are reserved for about a week after deletion; this example adds a random suffix so a fresh apply works immediately.
