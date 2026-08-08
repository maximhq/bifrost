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

# Retry policy. test-core is a required release gate, so a genuinely flaky cell
# (a provider timeout, a 429 that outlived its backoff) must not sink a release
# on its own - but neither should a real regression be retried into a green.
#
# RERUN_FAILED=1 re-runs ONLY the items that failed in the previous attempt,
# read from tmp/newman-report.json (see filter-collection.mjs --rerun-failed).
# So a retry is cheap - it re-runs the handful that failed, not the full sweep -
# and a persistent failure still fails, just three times over.
RERUN_ATTEMPTS="${RERUN_ATTEMPTS:-2}"

ATTEMPT_DIR="$REPO_ROOT/tmp/harness-attempts"
# NOT tmp/newman-report-<n>.json: the Makefile's parallel path globs
# tmp/newman-report-*.json to merge the per-provider reports, so an archived
# attempt sitting at that name would be merged into the next attempt's results.
# A subdirectory keeps the archive clear of that glob entirely.
mkdir -p "$ATTEMPT_DIR"

# archive_attempt <n> snapshots the artifacts an attempt produced, since the
# next attempt overwrites them in place. Without this a run that passes on
# retry 2 leaves no record of what failed on attempts 0 and 1, which is exactly
# the evidence needed to tell a flake from a degradation.
archive_attempt() {
  # Two statements, not `local n=.. dest=..$n`: in a single `local`, the later
  # value is expanded before `local` binds the earlier name, so $n would resolve
  # against the enclosing scope and trip `set -u`.
  local n="$1"
  local dest="$ATTEMPT_DIR/attempt-$n"
  mkdir -p "$dest"
  for artifact in newman-report.json harness-failures.md newman-cli.log; do
    if [ -f "$REPO_ROOT/tmp/$artifact" ]; then
      cp "$REPO_ROOT/tmp/$artifact" "$dest/$artifact" 2>/dev/null || true
    fi
  done
  # Per-provider newman logs from the parallel path.
  cp "$REPO_ROOT"/tmp/newman-cli-*.log "$dest/" 2>/dev/null || true
}

# item_failure_count reports how many item-level failures the last run recorded.
#
# This is the guard that stops a retry from manufacturing a false green. A run
# can exit non-zero for reasons that are not item failures at all - the gateway
# dying, newman itself crashing - and in that case the report lists zero failed
# items. RERUN_FAILED would then filter the collection down to nothing, the
# Makefile would skip an empty run, and the retry would exit 0 having verified
# precisely nothing. Newman's own stats are used rather than re-deriving the
# failed set, because the question here is only "were there item failures at
# all", not "which ones" - that part stays filter-collection.mjs's job.
item_failure_count() {
  local report="$REPO_ROOT/tmp/newman-report.json"
  [ -f "$report" ] || { echo 0; return; }
  jq -r '((.run.stats.assertions.failed // 0) + (.run.stats.requests.failed // 0))' "$report" 2>/dev/null || echo 0
}

# USE_INFISICAL=0 makes the Makefile's EXPOSE_ENV fall through to ./.env, which
# does not exist on a runner - so it is a no-op and newman inherits the job's
# environment, i.e. the GitHub Actions secrets, directly.
run_harness() {
  local rerun="$1" rc=0
  make run-provider-harness-test \
    CI=1 \
    USE_INFISICAL=0 \
    PARALLEL=1 \
    BASE_URL="$BASE_URL" \
    APP_DIR="$APP_DIR_REL" \
    ${rerun:+RERUN_FAILED=1} || rc=$?
  return $rc
}

echo ""
echo "🧪 Running provider harness (full sweep, parallel per provider)..."
HARNESS_EXIT=0
run_harness "" || HARNESS_EXIT=$?
archive_attempt 0
FINAL_ATTEMPT=0

attempt=1
while [ "$HARNESS_EXIT" -ne 0 ] && [ "$attempt" -le "$RERUN_ATTEMPTS" ]; do
  failed_items=$(item_failure_count)
  if [ "$failed_items" -eq 0 ]; then
    echo ""
    echo "❌ Harness exited $HARNESS_EXIT but the report shows no item-level failures."
    echo "   That is an infrastructure failure (gateway down, newman crash), not a flaky cell,"
    echo "   so RERUN_FAILED would re-run an empty collection and pass without verifying anything."
    echo "   Failing now rather than retrying. See tmp/newman-cli*.log and tmp/bifrost-dev.log."
    break
  fi

  echo ""
  echo "🔁 Attempt $attempt of $RERUN_ATTEMPTS: re-running only the $failed_items failed item(s) (RERUN_FAILED=1)..."
  HARNESS_EXIT=0
  run_harness 1 || HARNESS_EXIT=$?
  archive_attempt "$attempt"
  FINAL_ATTEMPT="$attempt"
  attempt=$((attempt + 1))
done

# Render the browsable HTML report.
#
# newman's own htmlextra reporter only emits one in sequential mode, and this
# runs PARALLEL=1 (one fork per provider, no way to merge N HTML documents) - so
# without this, the provider harness ships no HTML at all while the CLI harness
# does. harness-viewer.mjs already renders exactly this JSON for local use, so
# --static reuses its markup rather than growing a second renderer that can
# drift from it.
#
# Best-effort: a rendering failure must not fail a passing test run.
echo ""
echo "📊 Rendering provider harness HTML report..."
if node "$REPO_ROOT/tests/e2e/api/runners/harness-viewer.mjs" \
     --report "$REPO_ROOT/tmp/newman-report.json" \
     --failures-md "$REPO_ROOT/tmp/harness-failures.md" \
     --token-parity-md "$REPO_ROOT/tmp/harness-token-parity.md" \
     --static "$REPO_ROOT/tmp/provider-harness-report.html"; then
  :
else
  echo "⚠️  HTML report rendering failed; the JSON and markdown artifacts are still intact"
  # Rendering is best effort, but the LINK to it is not: the release changelog
  # points at provider-harness/index.html unconditionally, and the upload step
  # publishes whatever is in this directory. Without a file here that link 404s
  # in public release notes. A stub that points at the markdown siblings keeps
  # the notes honest about what happened and still reaches the real data.
  cat > "$REPO_ROOT/tmp/provider-harness-report.html" <<'FALLBACK_HTML'
<!doctype html>
<meta charset="utf-8">
<title>Bifrost Provider Harness Report</title>
<style>body{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#0d1117;color:#e6edf3;margin:0;padding:48px;line-height:1.6}
a{color:#58a6ff}code{background:#161b22;padding:2px 6px;border-radius:4px}</style>
<h1>Provider harness report unavailable</h1>
<p>The harness ran and its results were collected, but rendering this HTML view
failed. The underlying data is unaffected and published alongside this page:</p>
<ul>
  <li><a href="harness-failures.md">harness-failures.md</a> - the failure breakdown and coverage matrices</li>
  <li><a href="harness-token-parity.md">harness-token-parity.md</a> - the direct-provider vs Bifrost token parity matrix</li>
</ul>
<p>The full <code>newman-report.json</code> is attached to the release workflow run as an artifact.</p>
FALLBACK_HTML
fi

if [ -n "${GITHUB_STEP_SUMMARY:-}" ] && [ -f "$REPO_ROOT/tmp/harness-failures.md" ]; then
  {
    echo "## Provider harness failure breakdown"
    echo ""
    if [ "$FINAL_ATTEMPT" -gt 0 ]; then
      echo "> Ran $((FINAL_ATTEMPT + 1)) attempt(s); attempts after the first re-ran only the previously failed items."
      echo "> Per-attempt artifacts are under \`tmp/harness-attempts/\`."
      echo ""
    fi
    cat "$REPO_ROOT/tmp/harness-failures.md"
    echo ""
  } >> "$GITHUB_STEP_SUMMARY"
fi

if [ "$HARNESS_EXIT" -ne 0 ]; then
  echo "❌ Provider harness failed after $((FINAL_ATTEMPT + 1)) attempt(s) (exit $HARNESS_EXIT)."
  echo "   See tmp/harness-failures.md, tmp/newman-cli-*.log, and tmp/harness-attempts/ for the per-attempt history."
  exit "$HARNESS_EXIT"
fi

if [ "$FINAL_ATTEMPT" -gt 0 ]; then
  # Loud on purpose. A pass that needed retries is not the same as a clean pass,
  # and burying that turns a slow degradation into an invisible one.
  echo "::warning::Provider harness passed only on retry $FINAL_ATTEMPT of $RERUN_ATTEMPTS - the first attempt had failures. See tmp/harness-attempts/attempt-0/harness-failures.md."
fi

echo "✅ Provider harness completed successfully (attempts: $((FINAL_ATTEMPT + 1)))"
