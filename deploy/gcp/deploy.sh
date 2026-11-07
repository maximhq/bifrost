#!/usr/bin/env bash
# =============================================================================
# Bifrost one-click GCP deployment: Cloud Run + Cloud SQL PostgreSQL 16.
#
# Runs from Google Cloud Shell or any shell with gcloud credentials. Drives
# terraform/examples/gcp-cloud-run-postgres and keeps the Terraform state in
# that directory, so keep the clone (Cloud Shell's home directory persists) or
# move the state to a GCS backend before relying on the deployment.
#
#   git clone --depth 1 https://github.com/maximhq/bifrost && bifrost/deploy/gcp/deploy.sh
#
# Environment overrides (all optional):
#   GOOGLE_CLOUD_PROJECT       Project to deploy into (defaults to gcloud's project, prompted when unset)
#   GCP_REGION                 Region to deploy into          (default: us-central1)
#   BIFROST_NAME_PREFIX        Resource name prefix           (default: bifrost)
#   BIFROST_IMAGE_REPOSITORY   Image repository               (default: maximhq/bifrost)
#   BIFROST_IMAGE_TAG          Image tag                      (default: latest)
#   BIFROST_AUTO_APPROVE=1     Skip the terraform apply confirmation
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$SCRIPT_DIR/../../terraform/examples/gcp-cloud-run-postgres"

log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
die() { printf '\033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }

command -v gcloud >/dev/null || die "gcloud not found. Run this from Google Cloud Shell or install the Google Cloud CLI."
command -v terraform >/dev/null || die "Terraform not found. It is preinstalled in Cloud Shell; elsewhere install it from https://developer.hashicorp.com/terraform/install."

log "Checking gcloud credentials"
ACCOUNT=$(gcloud config get-value account 2>/dev/null || true)
[ -n "$ACCOUNT" ] || die "No gcloud account. Run 'gcloud auth login' and rerun."
echo "Account: $ACCOUNT"

PROJECT_ID="${GOOGLE_CLOUD_PROJECT:-$(gcloud config get-value project 2>/dev/null || true)}"
if [ -z "$PROJECT_ID" ]; then
  read -r -p "GCP project ID: " PROJECT_ID
  [ -n "$PROJECT_ID" ] || die "A project ID is required."
fi
REGION="${GCP_REGION:-us-central1}"
NAME_PREFIX="${BIFROST_NAME_PREFIX:-bifrost}"
IMAGE_REPOSITORY="${BIFROST_IMAGE_REPOSITORY:-maximhq/bifrost}"
IMAGE_TAG="${BIFROST_IMAGE_TAG:-latest}"

log "Deployment settings"
cat <<SETTINGS
Project:          $PROJECT_ID
Region:           $REGION
Name prefix:      $NAME_PREFIX
Image:            $IMAGE_REPOSITORY:$IMAGE_TAG
Terraform dir:    $EXAMPLE_DIR
SETTINGS

log "Enabling required APIs"
gcloud services enable \
  run.googleapis.com \
  sqladmin.googleapis.com \
  secretmanager.googleapis.com \
  iam.googleapis.com \
  compute.googleapis.com \
  --project "$PROJECT_ID"

# Terraform's google provider uses Application Default Credentials. In Cloud
# Shell they are already set; elsewhere fall back to the user's login.
if ! gcloud auth application-default print-access-token >/dev/null 2>&1; then
  log "Setting up Application Default Credentials"
  gcloud auth application-default login
fi

cd "$EXAMPLE_DIR"
cat > terraform.tfvars <<TFVARS
# Written by deploy/gcp/deploy.sh. Edit and rerun 'terraform apply' to change the stack.
project_id       = "$PROJECT_ID"
region           = "$REGION"
name_prefix      = "$NAME_PREFIX"
image_repository = "$IMAGE_REPOSITORY"
image_tag        = "$IMAGE_TAG"
TFVARS

log "Initializing Terraform"
terraform init -input=false -upgrade >/dev/null

log "Creating the stack (Cloud SQL takes about 10 minutes)"
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

Next steps:
  1. Open the dashboard and create the first admin account with the setup token.
  2. Add a provider key, then create a virtual key for your clients.

State lives in $EXAMPLE_DIR. Keep it, or move it to a GCS backend.
Tear down with: cd $EXAMPLE_DIR && terraform destroy
NEXT
