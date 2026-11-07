#!/usr/bin/env bash
# =============================================================================
# Bifrost one-click AWS deployment: ECS Fargate + RDS PostgreSQL 16 + ALB.
#
# Runs from AWS CloudShell or any shell with AWS credentials. Drives
# terraform/examples/aws-ecs-postgres and keeps the Terraform state in that
# directory, so keep the clone (CloudShell's home directory persists) or move
# the state to a remote backend before relying on the deployment.
#
#   git clone --depth 1 https://github.com/maximhq/bifrost && bifrost/deploy/aws/deploy.sh
#
# Environment overrides (all optional):
#   AWS_REGION                 Region to deploy into (prompted when unset)
#   BIFROST_NAME_PREFIX        Resource name prefix        (default: bifrost)
#   BIFROST_IMAGE_REPOSITORY   Image repository            (default: maximhq/bifrost)
#   BIFROST_IMAGE_TAG          Image tag                   (default: latest)
#   BIFROST_AUTO_APPROVE=1     Skip the terraform apply confirmation
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$SCRIPT_DIR/../../terraform/examples/aws-ecs-postgres"

log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
die() { printf '\033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }

command -v aws >/dev/null || die "aws CLI not found. Run this from AWS CloudShell or install the AWS CLI."

log "Checking AWS credentials"
ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text 2>/dev/null) || die "No AWS credentials. Run 'aws configure' or open AWS CloudShell."
echo "Account: $ACCOUNT_ID"

install_terraform() {
  if command -v terraform >/dev/null; then
    return
  fi
  log "Installing Terraform"
  if command -v yum >/dev/null; then
    # Amazon Linux (CloudShell): HashiCorp's RPM repository.
    sudo yum install -y yum-utils >/dev/null
    sudo yum-config-manager --add-repo https://rpm.releases.hashicorp.com/AmazonLinux/hashicorp.repo >/dev/null
    sudo yum install -y terraform >/dev/null
  elif command -v brew >/dev/null; then
    brew tap hashicorp/tap && brew install hashicorp/tap/terraform
  else
    die "Terraform not found. Install it from https://developer.hashicorp.com/terraform/install and rerun."
  fi
}
install_terraform
echo "Terraform: $(terraform version -json | sed -n 's/.*"terraform_version": *"\([^"]*\)".*/\1/p')"

REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-$(aws configure get region 2>/dev/null || true)}}"
if [ -z "$REGION" ]; then
  read -r -p "AWS region [us-east-1]: " REGION
  REGION="${REGION:-us-east-1}"
fi
NAME_PREFIX="${BIFROST_NAME_PREFIX:-bifrost}"
IMAGE_REPOSITORY="${BIFROST_IMAGE_REPOSITORY:-maximhq/bifrost}"
IMAGE_TAG="${BIFROST_IMAGE_TAG:-latest}"

log "Deployment settings"
cat <<SETTINGS
Region:           $REGION
Name prefix:      $NAME_PREFIX
Image:            $IMAGE_REPOSITORY:$IMAGE_TAG
Terraform dir:    $EXAMPLE_DIR
SETTINGS

cd "$EXAMPLE_DIR"
# The header invites edits, so a rerun must not throw them away. It also records
# the account the stack was created in: the local state belongs to that account,
# and applying it with other credentials would recreate every resource there
# while the originals stay behind unmanaged.
if [ -f terraform.tfvars ]; then
  log "Keeping existing terraform.tfvars (edit it to change the stack)"
  DEPLOYED_ACCOUNT=$(sed -n 's/^#[[:space:]]*deployed_to_account[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' terraform.tfvars | head -n 1)
  # Without the marker (a file from before it existed, or edited away) the
  # state itself still names the account: every AWS ARN in it carries one.
  if [ -z "$DEPLOYED_ACCOUNT" ] && [ -f terraform.tfstate ]; then
    DEPLOYED_ACCOUNT=$(grep -o 'arn:aws[a-z-]*:[a-z0-9-]*:[a-z0-9-]*:[0-9]\{12\}:' terraform.tfstate | head -n 1 | cut -d: -f5)
  fi
  if [ -n "$DEPLOYED_ACCOUNT" ] && [ "$DEPLOYED_ACCOUNT" != "$ACCOUNT_ID" ]; then
    die "$EXAMPLE_DIR/terraform.tfvars and its Terraform state were created for AWS account $DEPLOYED_ACCOUNT, but the current credentials belong to account $ACCOUNT_ID. Switch back to that account, or use a separate clone for a deployment in this one."
  fi
else
  cat > terraform.tfvars <<TFVARS
# Written by deploy/aws/deploy.sh. Edit and rerun 'terraform apply' to change the stack.
# deployed_to_account = "$ACCOUNT_ID"
region           = "$REGION"
name_prefix      = "$NAME_PREFIX"
image_repository = "$IMAGE_REPOSITORY"
image_tag        = "$IMAGE_TAG"
TFVARS
fi

log "Initializing Terraform"
# No -upgrade: the lock file keeps provider versions stable across reruns.
terraform init -input=false >/dev/null

log "Creating the stack (ECS, RDS and the ALB take about 10 minutes)"
if [ "${BIFROST_AUTO_APPROVE:-0}" = "1" ]; then
  terraform apply -input=false -auto-approve
else
  terraform apply -input=false
fi

SERVICE_URL=$(terraform output -raw service_url)
SETUP_TOKEN=$(terraform output -raw setup_token)

log "Bifrost is deploying"
cat <<NEXT
Dashboard:    $SERVICE_URL
Health:       $SERVICE_URL/health
Setup token:  $SETUP_TOKEN

The ALB reports healthy once the task has run its database migrations
(usually one to two minutes after apply finishes).

Next steps:
  1. Open the dashboard and create the first admin account with the setup token.
  2. Add a provider key, then create a virtual key for your clients.
  3. Put TLS in front of the ALB before production traffic (ACM certificate + HTTPS listener).

State lives in $EXAMPLE_DIR. Keep it, or move it to an S3 backend.
Tear down with: cd $EXAMPLE_DIR && terraform destroy
NEXT
