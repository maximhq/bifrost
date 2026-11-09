#!/usr/bin/env bash
# =============================================================================
# Bifrost one-click EKS deployment: EKS + RDS PostgreSQL 16 + public Service.
#
# Runs from AWS CloudShell or any shell with AWS credentials. Two phases:
#   1. create the EKS cluster and node group (terraform -target)
#   2. write the kubeconfig, then apply the database, secrets and workloads
# State stays in terraform/examples/aws-eks-postgres; keep the clone or move
# the state to an S3 backend before relying on the deployment.
#
#   git clone --depth 1 https://github.com/maximhq/bifrost && bifrost/deploy/eks/deploy.sh
#
# Environment overrides (all optional):
#   AWS_REGION                 Region (prompted when unset)
#   BIFROST_NAME_PREFIX        Resource name prefix        (default: bifrost)
#   BIFROST_IMAGE_REPOSITORY   Image repository            (default: maximhq/bifrost)
#   BIFROST_IMAGE_TAG          Image tag                   (default: latest)
#   BIFROST_AUTO_APPROVE=1     Skip the terraform apply confirmations
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXAMPLE_DIR="$SCRIPT_DIR/../../terraform/examples/aws-eks-postgres"

log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
die() { printf '\033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }

command -v aws >/dev/null || die "aws CLI not found. Run this from AWS CloudShell or install the AWS CLI."
command -v kubectl >/dev/null || die "kubectl not found. It is preinstalled in CloudShell."

log "Checking AWS credentials"
ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text 2>/dev/null) || die "No AWS credentials. Run 'aws configure' or open AWS CloudShell."
echo "Account: $ACCOUNT_ID"

if ! command -v terraform >/dev/null; then
  log "Installing Terraform"
  if command -v yum >/dev/null; then
    sudo yum install -y yum-utils >/dev/null
    sudo yum-config-manager --add-repo https://rpm.releases.hashicorp.com/AmazonLinux/hashicorp.repo >/dev/null
    sudo yum install -y terraform >/dev/null
  else
    die "Terraform not found. Install it from https://developer.hashicorp.com/terraform/install and rerun."
  fi
fi

REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-$(aws configure get region 2>/dev/null || true)}}"
if [ -z "$REGION" ]; then
  read -r -p "AWS region [us-east-1]: " REGION
  REGION="${REGION:-us-east-1}"
fi
NAME_PREFIX="${BIFROST_NAME_PREFIX:-bifrost}"
IMAGE_REPOSITORY="${BIFROST_IMAGE_REPOSITORY:-maximhq/bifrost}"
IMAGE_TAG="${BIFROST_IMAGE_TAG:-latest}"
APPROVE=""; [ "${BIFROST_AUTO_APPROVE:-0}" = "1" ] && APPROVE="-auto-approve"

log "Deployment settings"
cat <<SETTINGS
Region:           $REGION
Name prefix:      $NAME_PREFIX
Image:            $IMAGE_REPOSITORY:$IMAGE_TAG
Terraform dir:    $EXAMPLE_DIR
SETTINGS

cd "$EXAMPLE_DIR"
# The header invites edits, so a rerun must not throw them away.
if [ -f terraform.tfvars ]; then
  log "Keeping existing terraform.tfvars (edit it to change the stack)"
else
  cat > terraform.tfvars <<TFVARS
# Written by deploy/eks/deploy.sh. Edit and rerun 'terraform apply' to change the stack.
region           = "$REGION"
name_prefix      = "$NAME_PREFIX"
image_repository = "$IMAGE_REPOSITORY"
image_tag        = "$IMAGE_TAG"
TFVARS
fi

log "Initializing Terraform"
# No -upgrade: the lock file keeps provider versions stable across reruns.
terraform init -input=false >/dev/null

log "Phase 1: creating the EKS cluster and node group (about 15 minutes)"
terraform apply -input=false $APPROVE \
  -target='module.bifrost.module.eks[0].aws_eks_cluster.this' \
  -target='module.bifrost.module.eks[0].aws_eks_node_group.this'

log "Writing kubeconfig"
# A kept terraform.tfvars may name a different prefix or region than this
# shell, so take both from what Terraform actually created.
CLUSTER_NAME=$(terraform output -raw cluster_name)
CLUSTER_REGION=$(terraform output -raw cluster_region)
aws eks update-kubeconfig --region "$CLUSTER_REGION" --name "$CLUSTER_NAME"
kubectl get nodes

log "Phase 2: database, secrets and Bifrost (RDS takes about 10 minutes)"
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

The address answers once the pod has run its database migrations and the
load balancer targets are healthy (a few minutes).

Next steps:
  1. Open the dashboard and create the first admin account with the setup token.
  2. Add a provider key, then create a virtual key for your clients.
  3. The Service is plain HTTP; add TLS (ACM on an NLB/ALB, or an ingress controller)
     before production traffic.

State lives in $EXAMPLE_DIR. Keep it, or move it to an S3 backend.
Tear down with: cd $EXAMPLE_DIR && terraform destroy
NEXT
