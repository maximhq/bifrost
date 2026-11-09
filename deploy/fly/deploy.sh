#!/usr/bin/env bash
# =============================================================================
# Bifrost one-click Fly.io deployment: one Machine + Fly Postgres.
#
# Fly.io has no hosted shell, so this runs on your machine with flyctl
# installed and logged in (https://fly.io/docs/flyctl/install/).
#
#   git clone --depth 1 https://github.com/maximhq/bifrost && bifrost/deploy/fly/deploy.sh
#
# Environment overrides (all optional):
#   FLY_APP_NAME               App name (prompted when unset; must be globally unique)
#   FLY_REGION                 Primary region                 (default: iad)
#   FLY_ORG                    Organization slug              (default: personal)
#   FLY_PG_VM_SIZE             Postgres VM size               (default: shared-cpu-1x)
#   FLY_PG_VOLUME_GB           Postgres volume size           (default: 10)
#   BIFROST_IMAGE              Image reference                (default: maximhq/bifrost:latest)
#                              Enterprise: Fly pulls private registries only through its own
#                              registry. Pull and retag the image, `fly auth docker`, push it to
#                              registry.fly.io/<app>:<tag>, and pass that reference here.
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="$SCRIPT_DIR/fly.toml"
BIFROST_CONFIG_FILE="$SCRIPT_DIR/../config.postgres.json"

log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
die() { printf '\033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }

command -v fly >/dev/null || command -v flyctl >/dev/null || die "flyctl not found. Install it from https://fly.io/docs/flyctl/install/ and run 'fly auth login'."
FLY=$(command -v fly || command -v flyctl)

log "Checking Fly.io credentials"
$FLY auth whoami >/dev/null 2>&1 || die "Not logged in. Run 'fly auth login' and rerun."
echo "Account: $($FLY auth whoami)"

APP_NAME="${FLY_APP_NAME:-}"
if [ -z "$APP_NAME" ]; then
  read -r -p "App name (globally unique, e.g. acme-bifrost): " APP_NAME
  [ -n "$APP_NAME" ] || die "An app name is required."
fi
REGION="${FLY_REGION:-iad}"
ORG="${FLY_ORG:-personal}"
PG_APP="${APP_NAME}-db"
PG_VM_SIZE="${FLY_PG_VM_SIZE:-shared-cpu-1x}"
PG_VOLUME_GB="${FLY_PG_VOLUME_GB:-10}"
IMAGE="${BIFROST_IMAGE:-maximhq/bifrost:latest}"

random_secret() { od -An -N24 -tx1 /dev/urandom | tr -d ' \n'; }

log "Deployment settings"
cat <<SETTINGS
App:              $APP_NAME
Postgres app:     $PG_APP
Region:           $REGION
Organization:     $ORG
Image:            $IMAGE
SETTINGS

# List once and match in the shell: a `fly ... | grep -q` pipeline can fail
# under pipefail when grep exits before fly finishes writing.
EXISTING_APPS="$($FLY apps list --org "$ORG" --json 2>/dev/null || true)"
app_exists() {
  case "$EXISTING_APPS" in
    *"\"Name\": \"$1\""*|*"\"Name\":\"$1\""*) return 0 ;;
  esac
  return 1
}
EXISTING_SECRETS="$($FLY secrets list --app "$APP_NAME" 2>/dev/null || true)"
has_secret() {
  case "
$EXISTING_SECRETS" in
    *"
$1 "*|*"
$1	"*) return 0 ;;
  esac
  return 1
}

log "Creating the app"
if app_exists "$APP_NAME"; then
  echo "App $APP_NAME already exists; reusing it."
else
  $FLY apps create "$APP_NAME" --org "$ORG"
fi

log "Creating Fly Postgres ($PG_APP)"
if app_exists "$PG_APP"; then
  echo "Postgres app $PG_APP already exists; reusing it."
else
  $FLY postgres create \
    --name "$PG_APP" \
    --org "$ORG" \
    --region "$REGION" \
    --vm-size "$PG_VM_SIZE" \
    --volume-size "$PG_VOLUME_GB" \
    --initial-cluster-size 1 \
    --password "$(random_secret)"
fi

log "Attaching Postgres (sets DATABASE_URL on $APP_NAME)"
if has_secret DATABASE_URL; then
  echo "DATABASE_URL already set; skipping attach."
else
  $FLY postgres attach "$PG_APP" --app "$APP_NAME" --yes
fi

log "Setting configuration and generated secrets"
if has_secret BIFROST_ENCRYPTION_KEY; then
  echo "BIFROST_ENCRYPTION_KEY already set; keeping it (changing it would orphan encrypted data)."
  # Fly secrets are write-only, so a rerun cannot show the earlier token (for
  # example after a failed first deploy). Stage a fresh one: it is only used
  # to create the first admin account and is inert once that account exists.
  SETUP_TOKEN="$(random_secret)"
  $FLY secrets set --app "$APP_NAME" --stage \
    "BIFROST_CONFIG_JSON=$(cat "$BIFROST_CONFIG_FILE")" \
    "BIFROST_SETUP_TOKEN=$SETUP_TOKEN"
else
  SETUP_TOKEN="$(random_secret)"
  $FLY secrets set --app "$APP_NAME" --stage \
    "BIFROST_CONFIG_JSON=$(cat "$BIFROST_CONFIG_FILE")" \
    "BIFROST_ENCRYPTION_KEY=$(random_secret)" \
    "BIFROST_SETUP_TOKEN=$SETUP_TOKEN"
fi

log "Deploying $IMAGE"
$FLY deploy --config "$CONFIG_FILE" --app "$APP_NAME" --image "$IMAGE" --primary-region "$REGION" --ha=false

log "Bifrost is deployed"
cat <<NEXT
Dashboard:    https://$APP_NAME.fly.dev
Health:       https://$APP_NAME.fly.dev/health
NEXT
echo "Setup token:  $SETUP_TOKEN (regenerated on every run; only needed until the first admin account exists)"
cat <<NEXT

Next steps:
  1. Open the dashboard and create the first admin account with the setup token.
  2. Add a provider key, then create a virtual key for your clients.
  3. Pin the image: rerun with BIFROST_IMAGE=maximhq/bifrost:vX.Y.Z.

Tear down with: fly apps destroy $APP_NAME && fly apps destroy $PG_APP
NEXT
