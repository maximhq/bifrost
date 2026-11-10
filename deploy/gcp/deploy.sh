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

# Cloud Shell stopped bundling Terraform in 2026 and leaves a placeholder on
# PATH that only prints install instructions, so test that the binary works
# rather than that it exists. The release is pinned with its published
# checksums and lands in ~/.local/bin, which Cloud Shell keeps across sessions.
TERRAFORM_VERSION="1.17.1"
TERRAFORM_SHA256_AMD64="4638628058e71a55212b1c563712eac03b902128f40740f3dc8174df12135061"
TERRAFORM_SHA256_ARM64="988e950a1c23058093b0e66ab2d5fb53fc18345ecfbd5871acf51dbac9325edd"
install_terraform() {
  if terraform version >/dev/null 2>&1; then
    return
  fi
  [ "$(uname -s)" = "Linux" ] || die "Terraform not found. Install it from https://developer.hashicorp.com/terraform/install and rerun."
  case "$(uname -m)" in
    x86_64) ARCH=amd64; EXPECTED_SHA256="$TERRAFORM_SHA256_AMD64" ;;
    aarch64|arm64) ARCH=arm64; EXPECTED_SHA256="$TERRAFORM_SHA256_ARM64" ;;
    *) die "Unsupported architecture $(uname -m). Install Terraform from https://developer.hashicorp.com/terraform/install and rerun." ;;
  esac
  log "Installing Terraform $TERRAFORM_VERSION into $HOME/.local/bin (persists across Cloud Shell sessions)"
  TMP_DIR="$(mktemp -d)"
  ZIP="$TMP_DIR/terraform.zip"
  curl -fsSL -o "$ZIP" "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_${ARCH}.zip"
  ACTUAL_SHA256="$(sha256sum "$ZIP" | awk '{print $1}')"
  [ "$ACTUAL_SHA256" = "$EXPECTED_SHA256" ] || die "Terraform download checksum mismatch (expected $EXPECTED_SHA256, got $ACTUAL_SHA256)."
  mkdir -p "$HOME/.local/bin"
  if command -v unzip >/dev/null; then
    unzip -o -q "$ZIP" terraform -d "$HOME/.local/bin"
  else
    python3 -m zipfile -e "$ZIP" "$HOME/.local/bin"
  fi
  chmod +x "$HOME/.local/bin/terraform"
  rm -rf "$TMP_DIR"
  export PATH="$HOME/.local/bin:$PATH"
  terraform version >/dev/null 2>&1 || die "Terraform was installed to $HOME/.local/bin but does not run."
}
install_terraform

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

# A kept terraform.tfvars decides the project Terraform deploys into, so the
# API preflight has to follow it when the shell's default project differs.
# An explicit GOOGLE_CLOUD_PROJECT that disagrees would be displayed but never
# applied; refuse that instead of misreporting. The capture stops at the
# closing quote so a trailing comment does not leak into the value.
# Terraform reads project_id from sources that outrank terraform.tfvars
# (TF_VAR_project_id, TF_CLI_ARGS*, *.auto.tfvars). This preflight cannot follow
# them, so it would enable the APIs in the wrong project; refuse them instead.
[ -z "${TF_VAR_project_id:-}" ] || die "TF_VAR_project_id is set. This script enables the APIs in the project from terraform.tfvars (or the gcloud project), so unset it and put project_id in $EXAMPLE_DIR/terraform.tfvars instead."
TF_CLI_ARGS_SET=$(env | sed -n 's/^\(TF_CLI_ARGS[A-Za-z_]*\)=.*/\1/p' | head -n 1)
[ -z "$TF_CLI_ARGS_SET" ] || die "$TF_CLI_ARGS_SET is set. This script cannot follow -var or -var-file overrides, so unset it and put the values in $EXAMPLE_DIR/terraform.tfvars instead."
AUTO_TFVARS=$(find "$EXAMPLE_DIR" -maxdepth 1 \( -name '*.auto.tfvars' -o -name '*.auto.tfvars.json' \) 2>/dev/null | head -n 1)
[ -z "$AUTO_TFVARS" ] || die "$AUTO_TFVARS would override terraform.tfvars, which this script reads to pick the project. Move its values into $EXAMPLE_DIR/terraform.tfvars and remove it."
TF_PROJECT_ID=""
if [ -f "$EXAMPLE_DIR/terraform.tfvars" ]; then
  TF_PROJECT_ID=$(sed -n 's/^[[:space:]]*project_id[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$EXAMPLE_DIR/terraform.tfvars" | head -n 1)
fi
if [ -n "$TF_PROJECT_ID" ] && [ "$TF_PROJECT_ID" != "$PROJECT_ID" ]; then
  if [ -n "${GOOGLE_CLOUD_PROJECT:-}" ]; then
    die "GOOGLE_CLOUD_PROJECT=$GOOGLE_CLOUD_PROJECT differs from project_id=\"$TF_PROJECT_ID\" in $EXAMPLE_DIR/terraform.tfvars, which this script keeps. Edit the file to change the project, or use a separate clone for another project."
  fi
  echo "terraform.tfvars targets project $TF_PROJECT_ID (gcloud's current project is $PROJECT_ID); deploying there."
  PROJECT_ID="$TF_PROJECT_ID"
fi

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
# The header invites edits, so a rerun must not throw them away.
if [ -f terraform.tfvars ]; then
  log "Keeping existing terraform.tfvars (edit it to change the stack)"
else
  cat > terraform.tfvars <<TFVARS
# Written by deploy/gcp/deploy.sh. Edit and rerun 'terraform apply' to change the stack.
project_id       = "$PROJECT_ID"
region           = "$REGION"
name_prefix      = "$NAME_PREFIX"
image_repository = "$IMAGE_REPOSITORY"
image_tag        = "$IMAGE_TAG"
TFVARS
fi

log "Initializing Terraform"
# No -upgrade: the lock file keeps provider versions stable across reruns.
terraform init -input=false >/dev/null

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
