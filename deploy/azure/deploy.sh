#!/usr/bin/env bash
# =============================================================================
# Bifrost one-click Azure deployment: Container Instances + PostgreSQL Flexible Server.
#
# Runs from Azure Cloud Shell or any shell with an `az login`. Drives
# terraform/examples/azure-aci-postgres and keeps the Terraform state in that
# directory, so keep the clone (Cloud Shell's home directory persists) or move
# the state to an Azure Storage backend before relying on the deployment.
#
#   git clone --depth 1 https://github.com/maximhq/bifrost && bifrost/deploy/azure/deploy.sh
#
# Environment overrides (all optional):
#   AZURE_LOCATION             Region to deploy into          (default: eastus)
#   AZURE_SUBSCRIPTION_ID      Subscription (defaults to the az CLI's current one)
#   BIFROST_NAME_PREFIX        Resource name prefix and DNS label (default: bifrost-<4 random hex>)
#   BIFROST_IMAGE_REPOSITORY   Image repository               (default: maximhq/bifrost)
#   BIFROST_IMAGE_TAG          Image tag                      (default: latest)
#   BIFROST_AUTO_APPROVE=1     Skip the terraform apply confirmation
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$SCRIPT_DIR/../../terraform/examples/azure-aci-postgres"

log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
die() { printf '\033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }

command -v az >/dev/null || die "az CLI not found. Run this from Azure Cloud Shell or install the Azure CLI."
command -v terraform >/dev/null || die "Terraform not found. It is preinstalled in Cloud Shell; elsewhere install it from https://developer.hashicorp.com/terraform/install."

log "Checking Azure credentials"
SUBSCRIPTION_ID="${AZURE_SUBSCRIPTION_ID:-$(az account show --query id -o tsv 2>/dev/null || true)}"
[ -n "$SUBSCRIPTION_ID" ] || die "No Azure login. Run 'az login' and rerun."
az account set --subscription "$SUBSCRIPTION_ID"
echo "Subscription: $(az account show --query name -o tsv) ($SUBSCRIPTION_ID)"
# The azurerm provider reads the subscription from the CLI context or this variable.
export ARM_SUBSCRIPTION_ID="$SUBSCRIPTION_ID"

LOCATION="${AZURE_LOCATION:-eastus}"
# The prefix doubles as the public DNS label, which must be unique per region.
# An existing terraform.tfvars is kept as is below, so show the prefix it holds.
EXISTING_PREFIX=""
if [ -f "$EXAMPLE_DIR/terraform.tfvars" ]; then
  # The capture stops at the closing quote so a trailing comment does not leak into the value.
  EXISTING_PREFIX=$(sed -n 's/^name_prefix[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$EXAMPLE_DIR/terraform.tfvars")
fi
# The kept file wins, so an explicit prefix that disagrees with it would be
# displayed but never applied; refuse that instead of misreporting.
if [ -n "${BIFROST_NAME_PREFIX:-}" ] && [ -n "$EXISTING_PREFIX" ] && [ "$BIFROST_NAME_PREFIX" != "$EXISTING_PREFIX" ]; then
  die "BIFROST_NAME_PREFIX=$BIFROST_NAME_PREFIX differs from name_prefix=\"$EXISTING_PREFIX\" in $EXAMPLE_DIR/terraform.tfvars, which this script keeps. Edit the file to change the prefix, or remove it to start a new stack."
fi
NAME_PREFIX="${BIFROST_NAME_PREFIX:-${EXISTING_PREFIX:-bifrost-$(od -An -N2 -tx1 /dev/urandom | tr -d ' \n')}}"
IMAGE_REPOSITORY="${BIFROST_IMAGE_REPOSITORY:-maximhq/bifrost}"
IMAGE_TAG="${BIFROST_IMAGE_TAG:-latest}"

log "Deployment settings"
cat <<SETTINGS
Location:         $LOCATION
Name prefix:      $NAME_PREFIX
Image:            $IMAGE_REPOSITORY:$IMAGE_TAG
Terraform dir:    $EXAMPLE_DIR
SETTINGS

log "Registering resource providers (no-op when already registered)"
for ns in Microsoft.ContainerInstance Microsoft.DBforPostgreSQL Microsoft.KeyVault Microsoft.ManagedIdentity Microsoft.Network; do
  az provider register --namespace "$ns" --wait >/dev/null 2>&1 || echo "warning: could not register $ns; continuing"
done

cd "$EXAMPLE_DIR"
# The header invites edits, so a rerun must not throw them away; it also keeps
# the random prefix from the first run, so the same stack is updated.
if [ -f terraform.tfvars ]; then
  log "Keeping existing terraform.tfvars (edit it to change the stack)"
  # A pre-created file (the documented way to supply Enterprise registry values
  # before the first run) may omit the settings this script chooses. Add them,
  # or Terraform would silently use the defaults, the shared "bifrost" prefix
  # included, while the settings above claim otherwise.
  if [ -z "$EXISTING_PREFIX" ]; then
    printf 'name_prefix      = "%s"\n' "$NAME_PREFIX" >> terraform.tfvars
    echo "Added name_prefix = \"$NAME_PREFIX\" to terraform.tfvars (it had none)."
  fi
  if ! grep -q '^[[:space:]]*region[[:space:]]*=' terraform.tfvars; then
    printf 'region           = "%s"\n' "$LOCATION" >> terraform.tfvars
    echo "Added region = \"$LOCATION\" to terraform.tfvars (it had none)."
  fi
  # The file records the subscription the stack was created in: the local state
  # belongs to it, and applying with another subscription would recreate the
  # resource group there while the originals stay behind unmanaged.
  DEPLOYED_SUBSCRIPTION=$(sed -n 's/^#[[:space:]]*deployed_to_subscription[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' terraform.tfvars | head -n 1)
  # Without the marker (a file from before it existed, or edited away) the
  # state itself still names the subscription in every azurerm resource ID.
  if [ -z "$DEPLOYED_SUBSCRIPTION" ] && [ -f terraform.tfstate ]; then
    DEPLOYED_SUBSCRIPTION=$(grep -o '/subscriptions/[0-9a-fA-F-]\{36\}/' terraform.tfstate | head -n 1 | cut -d/ -f3)
  fi
  if [ -n "$DEPLOYED_SUBSCRIPTION" ] && [ "$DEPLOYED_SUBSCRIPTION" != "$SUBSCRIPTION_ID" ]; then
    die "$EXAMPLE_DIR/terraform.tfvars and its Terraform state were created for subscription $DEPLOYED_SUBSCRIPTION, but the current subscription is $SUBSCRIPTION_ID. Switch back to it, or use a separate clone for a deployment in this one."
  fi
else
  cat > terraform.tfvars <<TFVARS
# Written by deploy/azure/deploy.sh. Edit and rerun 'terraform apply' to change the stack.
# deployed_to_subscription = "$SUBSCRIPTION_ID"
region           = "$LOCATION"
name_prefix      = "$NAME_PREFIX"
image_repository = "$IMAGE_REPOSITORY"
image_tag        = "$IMAGE_TAG"
TFVARS
fi

log "Initializing Terraform"
# No -upgrade: the lock file keeps provider versions stable across reruns.
terraform init -input=false >/dev/null

log "Creating the stack (the Flexible Server takes about 10 minutes)"
if [ "${BIFROST_AUTO_APPROVE:-0}" = "1" ]; then
  terraform apply -input=false -auto-approve
else
  terraform apply -input=false
fi

SERVICE_URL=$(terraform output -raw service_url)
SETUP_TOKEN=$(terraform output -raw setup_token)

log "Bifrost is deployed"
cat <<NEXT
Dashboard:    $SERVICE_URL
Health:       $SERVICE_URL/health
Setup token:  $SETUP_TOKEN

The container group answers once the database migrations finish
(usually one to two minutes after apply).

Next steps:
  1. Open the dashboard and create the first admin account with the setup token.
  2. Add a provider key, then create a virtual key for your clients.
  3. The endpoint is plain HTTP; put Front Door, Application Gateway or your own
     proxy with TLS in front before production traffic.

State lives in $EXAMPLE_DIR. Keep it, or move it to an Azure Storage backend.
Tear down with: cd $EXAMPLE_DIR && terraform destroy
NEXT
