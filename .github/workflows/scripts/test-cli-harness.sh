#!/usr/bin/env bash
set -euo pipefail

# CI runner for the Bifrost CLI harness (tests/e2e/clis).
#
# Unlike the provider harness (which drives newman against HTTP endpoints), this
# harness installs the real coding CLIs and runs them as subprocesses against a
# live Bifrost, asserting on their non-interactive stream-JSON output. The CLIs
# reach Bifrost through their own base-URL env vars, which the Go harness sets
# per cell (ANTHROPIC_BASE_URL -> <base>/anthropic, OPENAI_BASE_URL -> <base>/openai;
# see tests/e2e/clis/matrix_test.go).
#
# The harness already has a CI mode: QUIET=1 suppresses the live mirror (which
# cannot render in an append-only Actions log) while still writing
# tests/e2e/clis/reports/*.json. So no separate CI renderer is needed here.
#
# Scope is deliberately narrow. The unfiltered matrix is every CLI x provider x
# model x scenario - hours of runtime and meaningful provider quota per release
# (tests/e2e/clis/README.md:87). This job pins one model per CLI and a core
# scenario set; widen CLAUDE_CASES / CODEX_CASES below when you want more.

if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"
cd "$REPO_ROOT"

PORT="${PORT:-8080}"
BASE_URL="${BASE_URL:-http://localhost:$PORT}"
APP_DIR="$REPO_ROOT/tmp/cli-harness-app"
SERVER_LOG="$REPO_ROOT/tmp/bifrost-cli-harness.log"
REPORTS_DIR="$REPO_ROOT/tests/e2e/clis/reports"

# Pinned CLI versions. codex 0.145.0 is the version tests/e2e/clis/matrix_test.go
# documents its --image and model_reasoning_effort assertions against, so do not
# float this without re-checking those comments.
CLAUDE_CODE_VERSION="${CLAUDE_CODE_VERSION:-2.1.220}"
CODEX_VERSION="${CODEX_VERSION:-0.145.0}"

# Test-name regexes passed as TESTCASE. Path is
# TestCLIs/<cli>/<provider>/<model>/<scenario>; the Makefile anchors with ^...$.
CLAUDE_CASES="${CLAUDE_CASES:-TestCLIs/claude/anthropic/claude-sonnet-5/(simple-chat|conversation-memory|file-read)}"
CODEX_CASES="${CODEX_CASES:-TestCLIs/codex/openai/gpt-5\.5/(simple-chat|conversation-memory|file-read)}"

if ! command -v jq >/dev/null 2>&1; then
  echo "❌ jq is required" >&2
  exit 1
fi

# shellcheck source=./harness-gateway.sh
source "$SCRIPT_DIR/harness-gateway.sh"

trap harness_stop_gateway EXIT

source "$SCRIPT_DIR/setup-go-workspace.sh"

echo "📦 Installing coding CLIs (pinned)..."
npm install -g \
  "@anthropic-ai/claude-code@$CLAUDE_CODE_VERSION" \
  "@openai/codex@$CODEX_VERSION"
echo "  claude: $(claude --version 2>&1 | head -n1)"
echo "  codex:  $(codex --version 2>&1 | head -n1)"

harness_build_gateway
harness_seed_app_dir "$APP_DIR"
harness_start_gateway "$APP_DIR" "$PORT" "$SERVER_LOG"

# The harness probes /api/providers before running; config.json sets
# enforce_auth_on_inference=false, so the placeholder key is sufficient.
export BIFROST_API_KEY="${BIFROST_API_KEY:-dummy}"

# run_cases <label> <testcase-regex>
#
# Each label runs into its own reports subdirectory. That is what makes "this
# filter ran nothing" detectable: `go test -run` exits 0 when its regex matches
# no subtests, so a drifted CLAUDE_CASES / CODEX_CASES (a renamed model, a
# retired scenario) would otherwise sail through the release gate having
# executed zero cells. An empty subdirectory is the evidence; a shared one could
# not distinguish this suite's cells from the other's.
#
# `make cli-harness-report` is deliberately run later WITHOUT this variable, so
# it renders the aggregate index.html across every subdirectory.
run_cases() {
  local label="$1" cases="$2" rc=0
  local run_dir="$REPORTS_DIR/$label"

  echo ""
  echo "🧪 CLI harness: $label"
  echo "   filter: $cases"
  echo "   reports: $run_dir"

  rm -rf "$run_dir"
  mkdir -p "$run_dir"

  # USE_INFISICAL=0 makes EXPOSE_ENV a no-op on a runner, so the job's GitHub
  # Actions secrets are inherited directly. QUIET=1 is the harness's own CI mode.
  # The reports dir is absolute because the Makefile cds into tests/e2e/clis.
  BIFROST_E2E_CLIS_REPORTS_DIR="$run_dir" \
  make run-cli-harness-test \
    USE_INFISICAL=0 \
    QUIET=1 \
    PARALLEL=2 \
    TIMEOUT=25m \
    BASE_URL="$BASE_URL" \
    TESTCASE="$cases" || rc=$?

  # Count cells actually produced. Guard this even when go test exited 0: a
  # vacuous filter is exactly the case that exits 0 with nothing run.
  local cells
  cells=$(find "$run_dir" -maxdepth 1 -name '*.json' -type f 2>/dev/null | wc -l | tr -d ' ')
  echo "   cells produced: $cells"

  if [ "$cells" -eq 0 ]; then
    echo "❌ CLI harness produced no cells for $label"
    echo "   The filter matched no TestCLIs subtests, so nothing was verified."
    echo "   filter: $cases"
    echo "   Check the model/scenario names in the filter against tests/e2e/clis/matrix_test.go."
    return 1
  fi

  if [ "$rc" -ne 0 ]; then
    echo "❌ CLI harness failed for $label (exit $rc, $cells cells)"
  else
    echo "✅ CLI harness passed for $label ($cells cells)"
  fi
  return $rc
}

CLAUDE_RC=0
CODEX_RC=0
# Both run even if the first fails, so one broken CLI does not mask the other.
run_cases "claude" "$CLAUDE_CASES" || CLAUDE_RC=$?
run_cases "codex" "$CODEX_CASES" || CODEX_RC=$?

# Renders tests/e2e/clis/reports/index.html from the reports/*.json just written.
# Free and instant - no test re-execution.
echo ""
echo "📊 Rendering CLI harness report..."
make cli-harness-report || echo "⚠️  Report rendering failed; reports/*.json are still intact"

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "## CLI harness"
    echo ""
    echo "| CLI | Version | Filter | Result |"
    echo "| --- | --- | --- | --- |"
    echo "| claude | \`$CLAUDE_CODE_VERSION\` | \`$CLAUDE_CASES\` | $([ "$CLAUDE_RC" -eq 0 ] && echo "✅ pass" || echo "❌ fail") |"
    echo "| codex | \`$CODEX_VERSION\` | \`$CODEX_CASES\` | $([ "$CODEX_RC" -eq 0 ] && echo "✅ pass" || echo "❌ fail") |"
    echo ""
    echo "Full per-cell results are in the \`cli-harness-reports\` artifact (\`index.html\`)."
    echo ""
  } >> "$GITHUB_STEP_SUMMARY"
fi

if [ ! -d "$REPORTS_DIR" ]; then
  echo "⚠️  No reports directory at $REPORTS_DIR - the harness may not have run any cells"
fi

if [ "$CLAUDE_RC" -ne 0 ] || [ "$CODEX_RC" -ne 0 ]; then
  echo "❌ CLI harness failed (claude=$CLAUDE_RC, codex=$CODEX_RC)"
  exit 1
fi

echo "✅ CLI harness completed successfully"
