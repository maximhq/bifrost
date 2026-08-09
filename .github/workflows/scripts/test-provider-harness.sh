#!/usr/bin/env bash
set -euo pipefail

# CI runner for the Bifrost provider harness (tests/e2e/api/collections/provider-harness.json).
#
# Local developers run `make run-provider-harness-test`, which auto-starts Bifrost
# via `make dev`, sources secrets from Infisical/.env, and renders an in-place
# terminal dashboard. None of that works on a GitHub runner, so this script:
#
#   1. builds the UI + the bifrost-http binary (the harness needs a live gateway),
#   2. seeds a throwaway app dir with sqlite stores (no Postgres/Docker needed),
#   3. authenticates gcloud from VERTEX_CREDENTIALS so the Makefile can mint the
#      Vertex access token used by the direct-provider token-parity cells,
#   4. boots the binary and waits for /health (the make target detects a healthy
#      gateway and skips its own auto-start),
#   5. delegates to `make run-provider-harness-test CI=1 USE_INFISICAL=0`, which
#      reads secrets straight from the environment (GitHub Actions secrets) and
#      runs harness-monitor.mjs in append-only --ci mode.
#
# Exit code is the harness exit code, so a provider failure fails the job.

if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"
cd "$REPO_ROOT"

PORT="${PORT:-8080}"
BASE_URL="${BASE_URL:-http://localhost:$PORT}"
# Kept inside the repo (not mktemp) so the relative paths the Makefile builds for
# APP_DIR - the dbverify config path and the sqlite logs URL - resolve correctly.
APP_DIR_REL="tmp/harness-app"
APP_DIR="$REPO_ROOT/$APP_DIR_REL"
SERVER_LOG="$REPO_ROOT/tmp/bifrost-dev.log"
GCLOUD_KEY_FILE="$APP_DIR/vertex-sa.json"

if ! command -v jq >/dev/null 2>&1; then
  echo "❌ jq is required" >&2
  exit 1
fi
if ! command -v newman >/dev/null 2>&1; then
  echo "❌ newman is required (npm install -g newman@6.2.1 newman-reporter-htmlextra@1.23.1)" >&2
  exit 1
fi

# shellcheck source=./harness-gateway.sh
source "$SCRIPT_DIR/harness-gateway.sh"

cleanup() {
  local exit_code=$?
  harness_stop_gateway
  # The service-account key is the only secret written to disk; never leave it
  # behind for the artifact-upload step to pick up.
  rm -f "$GCLOUD_KEY_FILE"
  exit $exit_code
}
trap cleanup EXIT

source "$SCRIPT_DIR/setup-go-workspace.sh"

harness_build_gateway
harness_seed_app_dir "$APP_DIR"

# dbverify (on by default) asserts logged cost against the getbifrost.ai
# datasheet. Point it at the app dir's sqlite logs DB explicitly - the Makefile's
# default assumes a repo-relative APP_DIR.
export BIFROST_LOGS_DB_URL="sqlite://$APP_DIR/logs.db"

if [ -n "${VERTEX_CREDENTIALS:-}" ] && command -v gcloud >/dev/null 2>&1; then
  echo "🔑 Activating gcloud service account for Vertex token parity..."
  umask 077
  printf '%s' "$VERTEX_CREDENTIALS" > "$GCLOUD_KEY_FILE"
  if gcloud auth activate-service-account --key-file="$GCLOUD_KEY_FILE" --quiet; then
    echo "✅ gcloud authenticated"
  else
    echo "⚠️  gcloud auth failed - Vertex direct-provider parity cells will 401"
  fi
else
  echo "⚠️  VERTEX_CREDENTIALS unset or gcloud missing - Vertex direct-provider parity cells will 401"
fi

harness_start_gateway "$APP_DIR" "$PORT" "$SERVER_LOG"

echo ""
echo "🧪 Running provider harness (full sweep, parallel per provider)..."
# USE_INFISICAL=0 makes the Makefile's EXPOSE_ENV fall through to ./.env, which
# does not exist on a runner - so it is a no-op and newman inherits the job's
# environment, i.e. the GitHub Actions secrets, directly.
HARNESS_EXIT=0
make run-provider-harness-test \
  CI=1 \
  USE_INFISICAL=0 \
  PARALLEL=1 \
  BASE_URL="$BASE_URL" \
  APP_DIR="$APP_DIR_REL" || HARNESS_EXIT=$?

if [ -n "${GITHUB_STEP_SUMMARY:-}" ] && [ -f "$REPO_ROOT/tmp/harness-failures.md" ]; then
  {
    echo "## Provider harness failure breakdown"
    echo ""
    cat "$REPO_ROOT/tmp/harness-failures.md"
    echo ""
  } >> "$GITHUB_STEP_SUMMARY"
fi

if [ "$HARNESS_EXIT" -ne 0 ]; then
  echo "❌ Provider harness failed (exit $HARNESS_EXIT). See tmp/harness-failures.md and tmp/newman-cli-*.log"
  exit "$HARNESS_EXIT"
fi

echo "✅ Provider harness completed successfully"
