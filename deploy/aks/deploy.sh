#!/usr/bin/env bash
# =============================================================================
# Bifrost one-click AKS deployment: AKS + PostgreSQL Flexible Server 16 + public Service.
#
# Runs from Azure Cloud Shell or any shell with an `az login`. Two phases:
#   1. create the AKS cluster (terraform -target)
#   2. fetch cluster credentials, then apply the database, secrets and workloads
# State stays in terraform/examples/azure-aks-postgres; keep the clone or move
# the state to an Azure Storage backend before relying on the deployment.
#
#   git clone --depth 1 https://github.com/maximhq/bifrost && bifrost/deploy/aks/deploy.sh
#
# Environment overrides (all optional):
#   AZURE_LOCATION             Region                         (default: eastus)
#   AZURE_SUBSCRIPTION_ID      Subscription (defaults to the az CLI's current one)
#   BIFROST_NAME_PREFIX        Resource name prefix           (default: bifrost)
#   BIFROST_IMAGE_REPOSITORY   Image repository               (default: maximhq/bifrost)
#   BIFROST_IMAGE_TAG          Image tag                      (default: latest)
#   BIFROST_AUTO_APPROVE=1     Skip the terraform apply confirmations
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$SCRIPT_DIR/../../terraform/examples/azure-aks-postgres"

log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
die() { printf '\033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }

command -v az >/dev/null || die "az CLI not found. Run this from Azure Cloud Shell or install the Azure CLI."
command -v terraform >/dev/null || die "Terraform not found. It is preinstalled in Cloud Shell."
command -v kubectl >/dev/null || die "kubectl not found. It is preinstalled in Cloud Shell."

log "Checking Azure credentials"
SUBSCRIPTION_ID="${AZURE_SUBSCRIPTION_ID:-$(az account show --query id -o tsv 2>/dev/null || true)}"
[ -n "$SUBSCRIPTION_ID" ] || die "No Azure login. Run 'az login' and rerun."
az account set --subscription "$SUBSCRIPTION_ID"
echo "Subscription: $(az account show --query name -o tsv) ($SUBSCRIPTION_ID)"
export ARM_SUBSCRIPTION_ID="$SUBSCRIPTION_ID"

LOCATION="${AZURE_LOCATION:-eastus}"
NAME_PREFIX="${BIFROST_NAME_PREFIX:-bifrost}"
IMAGE_REPOSITORY="${BIFROST_IMAGE_REPOSITORY:-maximhq/bifrost}"
IMAGE_TAG="${BIFROST_IMAGE_TAG:-latest}"
APPROVE=""; [ "${BIFROST_AUTO_APPROVE:-0}" = "1" ] && APPROVE="-auto-approve"

log "Deployment settings"
cat <<SETTINGS
Location:         $LOCATION
Name prefix:      $NAME_PREFIX
Image:            $IMAGE_REPOSITORY:$IMAGE_TAG
Terraform dir:    $EXAMPLE_DIR
SETTINGS

log "Registering resource providers (no-op when already registered)"
for ns in Microsoft.ContainerService Microsoft.DBforPostgreSQL Microsoft.KeyVault Microsoft.ManagedIdentity Microsoft.Network Microsoft.Compute; do
  az provider register --namespace "$ns" --wait >/dev/null 2>&1 || echo "warning: could not register $ns; continuing"
done

cd "$EXAMPLE_DIR"
# The header invites edits, so a rerun must not throw them away.
if [ -f terraform.tfvars ]; then
  log "Keeping existing terraform.tfvars (edit it to change the stack)"
else
  cat > terraform.tfvars <<TFVARS
# Written by deploy/aks/deploy.sh. Edit and rerun 'terraform apply' to change the stack.
region           = "$LOCATION"
name_prefix      = "$NAME_PREFIX"
image_repository = "$IMAGE_REPOSITORY"
image_tag        = "$IMAGE_TAG"
TFVARS
fi

log "Initializing Terraform"
# No -upgrade: the lock file keeps provider versions stable across reruns.
terraform init -input=false >/dev/null

log "Phase 1: creating the AKS cluster (about 10 minutes)"
terraform apply -input=false $APPROVE \
  -target='module.bifrost.module.aks[0].azurerm_kubernetes_cluster.this'

log "Fetching cluster credentials"
# Taken from what Terraform created, since a kept terraform.tfvars may name a
# different prefix or an existing resource group.
CLUSTER_NAME=$(terraform output -raw cluster_name)
CLUSTER_RG=$(terraform output -raw cluster_resource_group)
az aks get-credentials --resource-group "$CLUSTER_RG" --name "$CLUSTER_NAME" --overwrite-existing
kubectl get nodes

log "Phase 2: database, secrets and Bifrost (the Flexible Server takes about 10 minutes)"
terraform apply -input=false $APPROVE

log "Waiting for the public load balancer address"
for _ in $(seq 1 30); do
  SERVICE_URL=$(terraform output -raw service_url 2>/dev/null || true)
  if [ -n "$SERVICE_URL" ] && [ "$SERVICE_URL" != "null" ]; then break; fi
  terraform refresh -input=false >/dev/null 2>&1 || true
  sleep 10
done
SETUP_TOKEN=$(terraform output -raw setup_token)

log "Bifrost is deploying"
cat <<NEXT
Dashboard:    ${SERVICE_URL:-<pending: terraform output service_url>}
Health:       ${SERVICE_URL:-<pending>}/health
Setup token:  $SETUP_TOKEN

Next steps:
  1. Open the dashboard and create the first admin account with the setup token.
  2. Add a provider key, then create a virtual key for your clients.
  3. The Service is plain HTTP; add TLS (Application Gateway ingress or an ingress
     controller) before production traffic.

State lives in $EXAMPLE_DIR. Keep it, or move it to an Azure Storage backend.
Tear down with: cd $EXAMPLE_DIR && terraform destroy
NEXT
