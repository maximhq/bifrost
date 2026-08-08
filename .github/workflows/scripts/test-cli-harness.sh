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
# Console output is a progress table, not a log. Under QUIET=1 the harness
# prints a per-harness tally (total/done/pass/fail) to STDERR every 10s, while
# everything verbose - go test output, per-cell result lines, rate-limit
# notices - goes to STDOUT. So each suite's stdout is redirected into
# tmp/cli-harness-<label>.log (uploaded as an artifact, and tailed here on
# failure) and only the table reaches the Actions console.
#
# Scope is deliberately narrow. The unfiltered matrix is every CLI x provider x
# model x scenario - hours of runtime and meaningful provider quota per release
# (tests/e2e/clis/README.md:87). This job pins one model per CLI and a core
# scenario set; widen CLAUDE_CASES / CODEX_CASES / OPENCODE_CASES below when
# you want more.

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

# CLI versions. In CI these are ALWAYS supplied by the environment: the
# resolve-cli-versions job queries npm on every run and fans latest /
# latest-1 / latest-2 into three independent matrix legs, so nothing here is
# what actually gets installed.
#
# The literals below are therefore two things only: the default for a local
# invocation of this script, and the fallback resolve-cli-versions degrades to
# if npm is unreachable. Keep them in sync with the FALLBACK_* constants in
# resolve-cli-versions.sh.
#
# codex 0.145.0 is the version tests/e2e/clis/matrix_test.go documents its
# --image and model_reasoning_effort assertions against. Those assertions may
# well not hold on the newer codex releases the matrix now exercises - when a
# codex leg fails on them, that comment is the place to start.
CLAUDE_CODE_VERSION="${CLAUDE_CODE_VERSION:-2.1.220}"
CODEX_VERSION="${CODEX_VERSION:-0.145.0}"
OPENCODE_VERSION="${OPENCODE_VERSION:-1.18.10}"

# Test-name regexes passed as TESTCASE. Path is
# TestCLIs/<cli>/<provider>/<model>/<scenario>; the Makefile anchors with ^...$.
#
# opencode routes through Bifrost's /openai base path (see matrix_test.go), so
# it is pinned to the same OpenAI model as codex.
#
# The provider segment alternates so each CLI covers its native provider AND
# Azure in one invocation. Alternation is confined to a SINGLE path level on
# purpose - `go test -run` splits its pattern on "/" and matches each piece
# against the corresponding subtest level, so a pattern spanning levels matches
# nothing (same reasoning as build_rerun_filters below). This works only
# because azure exposes model IDs identical to the native providers':
# azureModels is the GPT lineup plus anthropicModels appended verbatim
# (tests/e2e/clis/matrix_test.go), so claude-sonnet-5 and gpt-5.5 resolve under
# both providers.
#
# codex is deliberately NOT extended to azure: supportsCLIProviderModel returns
# false for codex on any provider except openai (scenarios_test.go), so an
# azure alternative there would silently match zero cells.
# reasoning-replay runs here too, as the control for the Bedrock defect above:
# the report states the Anthropic Messages path is unaffected, and this is what
# turns that into a checked assertion rather than a footnote. If it ever fails
# here as well, the fault is not Responses-specific.
CLAUDE_CASES="${CLAUDE_CASES:-TestCLIs/claude/(anthropic|azure)/claude-sonnet-5/(simple-chat|conversation-memory|file-read|reasoning-replay)}"
CODEX_CASES="${CODEX_CASES:-TestCLIs/codex/openai/gpt-5\.5/(simple-chat|conversation-memory|file-read)}"
OPENCODE_CASES="${OPENCODE_CASES:-TestCLIs/opencode/(openai|azure)/gpt-5\.5/(simple-chat|conversation-memory|file-read)}"

# opencode-responses is the ONLY suite on Bifrost's /v1/responses path (opencode
# is chat/completions, codex is gated to openai), and Bifrost converts requests
# separately per wire format - so without this suite that conversion code ships
# unexercised. A reasoning-replay defect on exactly this path shipped in a build
# this harness passed green.
#
# simple-chat is the control: it proves the Responses->Bedrock path works at all,
# so a reasoning-replay failure can be attributed to reasoning replay rather than
# to the path being broken outright. reasoning-replay is the regression test -
# its five turns accumulate reasoning blocks that get replayed on every request,
# which is the shape that failed.
#
# Pinned to one Anthropic-on-Bedrock model: the defect is specific to Anthropic
# models there (they mandate the reasoning signature), and this is the only
# suite that costs Bedrock quota.
OPENCODE_RESPONSES_CASES="${OPENCODE_RESPONSES_CASES:-TestCLIs/opencode-responses/bedrock/global\.anthropic\.claude-sonnet-5/(simple-chat|reasoning-replay)}"

# opencode-anthropic covers the THIRD wire format: /anthropic/v1/messages.
#
# It is the only client here that replays reasoning, and that is a protocol
# constraint rather than a client preference -- the Anthropic API requires an
# assistant turn's thinking blocks to be sent back verbatim when that turn
# contains tool_use. The Responses SDK is free to rebuild history as prose and
# does exactly that, which is why no amount of turns or tools makes the
# opencode-responses suite exercise reasoning replay.
#
# reasoning-tool-replay is therefore the case that matters: it is the only cell
# in the whole harness that reproduces the reported Bedrock 400
# ("messages.2 ... reasoningContent.reasoningText.text ... Member must not be
# null"). simple-chat is its control, and running both providers gives the
# Anthropic wire coverage on the native path as well as the Bedrock one.
OPENCODE_ANTHROPIC_CASES="${OPENCODE_ANTHROPIC_CASES:-TestCLIs/opencode-anthropic/(anthropic|bedrock)/(claude-sonnet-5|global\.anthropic\.claude-sonnet-5)/(simple-chat|reasoning-tool-replay)}"

# Retries per suite, re-running ONLY the cells that failed. Mirrors test-core's
# RERUN_FAILED policy (see test-provider-harness.sh): this suite is a required
# release gate, so a flaky cell must not sink a release on its own, while a real
# regression still fails after every attempt.
CLI_RERUN_ATTEMPTS="${CLI_RERUN_ATTEMPTS:-2}"

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
  "@openai/codex@$CODEX_VERSION" \
  "opencode-ai@$OPENCODE_VERSION"
echo "  claude:   $(claude --version 2>&1 | head -n1)"
echo "  codex:    $(codex --version 2>&1 | head -n1)"
echo "  opencode: $(opencode --version 2>&1 | head -n1)"

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
# run_suite <run-dir> <testcase-regex> <log-path> <append?>
#
# One `go test` invocation. USE_INFISICAL=0 makes EXPOSE_ENV a no-op on a
# runner, so the job's GitHub Actions secrets are inherited directly. QUIET=1 is
# the harness's own CI mode. The reports dir is absolute because the Makefile
# cds into tests/e2e/clis.
#
# Only stdout is redirected. The harness's progress table goes to stderr (see
# tests/e2e/clis/progress_test.go) so it stays on the console while every
# verbose line lands in the log, which is uploaded as an artifact and tailed on
# failure.
run_suite() {
  local run_dir="$1" cases="$2" run_log="$3" append="${4:-}" rc=0
  if [ -n "$append" ]; then
    BIFROST_E2E_CLIS_REPORTS_DIR="$run_dir" \
    make run-cli-harness-test \
      USE_INFISICAL=0 QUIET=1 PARALLEL=2 TIMEOUT=25m \
      BASE_URL="$BASE_URL" TESTCASE="$cases" >>"$run_log" || rc=$?
  else
    BIFROST_E2E_CLIS_REPORTS_DIR="$run_dir" \
    make run-cli-harness-test \
      USE_INFISICAL=0 QUIET=1 PARALLEL=2 TIMEOUT=25m \
      BASE_URL="$BASE_URL" TESTCASE="$cases" >"$run_log" || rc=$?
  fi
  return $rc
}

# count_failed_cells <run-dir> - cells whose summary records status "fail".
#
# Only "fail" is retried. soft_pass and interrupted are deliberately excluded:
# neither fails the suite, so re-running them would spend provider quota to
# change nothing.
count_failed_cells() {
  local run_dir="$1" f n=0
  for f in "$run_dir"/*.json; do
    [ -f "$f" ] || continue
    if [ "$(jq -r '.status // ""' "$f" 2>/dev/null)" = "fail" ]; then
      n=$((n + 1))
    fi
  done
  echo "$n"
}

# build_rerun_filters <run-dir>
#
# Emits one `-run` regex per line covering exactly the failed cells.
#
# Why not one regex for all of them: `go test -run` splits its pattern on "/"
# and matches each piece against the corresponding subtest level, so an
# alternation that spans levels - (claude/openai/x|claude/openai/y) - is torn
# apart at the slashes and matches nothing like what was intended. Alternating
# only within a single level is the one form that survives that split.
#
# So cells are grouped by their parent path and alternated on the final segment
# alone. That is exact rather than a cross-product, and it handles both depths
# uniformly: a plain cell's leaf is its scenario, and an effort cell's leaf is
# its effort level (.../image-qa/low), because the summary's "scenario@effort"
# label maps onto that extra path segment.
build_rerun_filters() {
  local run_dir="$1" f stem cli provider model scenario rest path
  for f in "$run_dir"/*.json; do
    [ -f "$f" ] || continue
    [ "$(jq -r '.status // ""' "$f" 2>/dev/null)" = "fail" ] || continue
    stem=$(basename "$f" .json)
    cli=${stem%%__*}; rest=${stem#*__}
    provider=${rest%%__*}; rest=${rest#*__}
    model=${rest%%__*}; scenario=${rest#*__}
    # Model IDs carry dots (gpt-5.5), which are regex wildcards; escape them so
    # the filter cannot match a neighbouring model.
    path="TestCLIs/${cli}/${provider}/${model//./\\.}"
    if [ "$scenario" != "${scenario%@*}" ]; then
      path="$path/${scenario%@*}/${scenario#*@}"
    else
      path="$path/$scenario"
    fi
    echo "$path"
  done | sort -u | while IFS= read -r path; do
    printf '%s\t%s\n' "${path%/*}" "${path##*/}"
  done | sort | awk -F'\t' '
    { if ($1 != prev) { if (prev != "") print prev "/(" acc ")"; prev = $1; acc = $2 }
      else acc = acc "|" $2 }
    END { if (prev != "") print prev "/(" acc ")" }'
}

run_cases() {
  local label="$1" cases="$2" rc=0
  local run_dir="$REPORTS_DIR/$label"
  local run_log="$REPO_ROOT/tmp/cli-harness-$label.log"
  local attempt_dir="$REPO_ROOT/tmp/cli-harness-attempts/$label"

  echo ""
  echo "🧪 CLI harness: $label"
  echo "   filter: $cases"
  echo "   reports: $run_dir"
  echo "   log:     $run_log"

  rm -rf "$run_dir"
  mkdir -p "$run_dir" "$(dirname "$run_log")" "$attempt_dir"

  run_suite "$run_dir" "$cases" "$run_log" || rc=$?

  # Count cells actually produced. Guard this even when go test exited 0: a
  # vacuous filter is exactly the case that exits 0 with nothing run.
  local cells
  cells=$(find "$run_dir" -maxdepth 1 -name '*.json' -type f 2>/dev/null | wc -l | tr -d ' ')
  echo "   cells produced: $cells"

  # Retry only the cells that actually failed. A CLI cell drives a real CLI
  # against a live provider, so a timeout or a 429 that outlived its backoff is
  # a genuine flake - but the suite is a required release gate, so a real
  # regression must still fail, just three times over.
  local attempt=1 final_attempt=0 failed
  while [ "$rc" -ne 0 ] && [ "$attempt" -le "$CLI_RERUN_ATTEMPTS" ] && [ "$cells" -ne 0 ]; do
    failed=$(count_failed_cells "$run_dir")
    if [ "$failed" -eq 0 ]; then
      echo "   ⚠️  suite failed but no cell is marked 'fail' - not a flaky cell, so not retrying."
      echo "      (build failure, gateway loss, or a panic outside a cell; see $run_log)"
      break
    fi

    # Snapshot before the rerun overwrites the failed cells' summaries in place.
    # Without this a suite that passes on retry leaves no record of what failed
    # first, which is the evidence for telling a flake from a degradation.
    cp -R "$run_dir" "$attempt_dir/attempt-$((attempt - 1))" 2>/dev/null || true
    cp "$run_log" "$attempt_dir/attempt-$((attempt - 1)).log" 2>/dev/null || true

    echo ""
    echo "🔁 $label attempt $attempt of $CLI_RERUN_ATTEMPTS: re-running $failed failed cell(s)..."
    rc=0
    while IFS= read -r filter; do
      [ -n "$filter" ] || continue
      echo "   rerun filter: $filter"
      run_suite "$run_dir" "$filter" "$run_log" append || rc=$?
    done < <(build_rerun_filters "$run_dir")

    # The reports are the source of truth, not go test's exit code. A rerun
    # filter that matched nothing exits 0 while the cell's summary still reads
    # "fail" - trusting the exit code there would turn a drifted filter into a
    # green suite.
    if [ "$(count_failed_cells "$run_dir")" -gt 0 ]; then
      rc=1
    fi
    final_attempt="$attempt"
    attempt=$((attempt + 1))
  done

  # Recorded on disk rather than in a shell global so the summary below can read
  # it back per label without indirect variable naming.
  echo "$((final_attempt + 1))" > "$attempt_dir/count"

  if [ "$rc" -eq 0 ] && [ "$final_attempt" -gt 0 ]; then
    # Loud on purpose: a pass that needed retries is not a clean pass, and
    # burying that turns a slow degradation into an invisible one.
    echo "::warning::CLI harness ($label) passed only on retry $final_attempt of $CLI_RERUN_ATTEMPTS - see tmp/cli-harness-attempts/$label/ for what failed first."
  fi

  if [ "$cells" -eq 0 ]; then
    echo "❌ CLI harness produced no cells for $label"
    echo "   The filter matched no TestCLIs subtests, so nothing was verified."
    echo "   filter: $cases"
    echo "   Check the model/scenario names in the filter against tests/e2e/clis/matrix_test.go."
    dump_log_tail "$label" "$run_log"
    return 1
  fi

  if [ "$rc" -ne 0 ]; then
    echo "❌ CLI harness failed for $label after $((final_attempt + 1)) attempt(s) ($cells cells, $(count_failed_cells "$run_dir") still failing)"
    dump_log_tail "$label" "$run_log"
  else
    echo "✅ CLI harness passed for $label ($cells cells, $((final_attempt + 1)) attempt(s))"
  fi
  return $rc
}

# dump_log_tail surfaces the end of a failed suite's redirected stdout. Without
# it a failure would show only the progress table's fail count, with the actual
# assertion text sitting in an artifact nobody has downloaded yet.
dump_log_tail() {
  local label="$1" run_log="$2"
  if [ ! -s "$run_log" ]; then
    echo "   (no output captured in $run_log)"
    return 0
  fi
  echo ""
  echo "::group::Last 200 lines of $label output ($run_log)"
  tail -n 200 "$run_log"
  echo "::endgroup::"
}

# attempts_for <label> - how many attempts that suite took, for the summary.
# Defaults to 1 if the suite never wrote a count (it failed before run_cases
# reached that point).
attempts_for() {
  local f="$REPO_ROOT/tmp/cli-harness-attempts/$1/count"
  if [ -f "$f" ]; then cat "$f"; else echo 1; fi
}

CLAUDE_RC=0
CODEX_RC=0
OPENCODE_RC=0
OPENCODE_RESPONSES_RC=0
OPENCODE_ANTHROPIC_RC=0
# All five run even if an earlier one fails, so one broken CLI does not mask
# the others.
run_cases "claude" "$CLAUDE_CASES" || CLAUDE_RC=$?
run_cases "codex" "$CODEX_CASES" || CODEX_RC=$?
run_cases "opencode" "$OPENCODE_CASES" || OPENCODE_RC=$?
run_cases "opencode-responses" "$OPENCODE_RESPONSES_CASES" || OPENCODE_RESPONSES_RC=$?
run_cases "opencode-anthropic" "$OPENCODE_ANTHROPIC_CASES" || OPENCODE_ANTHROPIC_RC=$?

# Renders tests/e2e/clis/reports/index.html from the reports/*.json just written.
# Free and instant - no test re-execution.
echo ""
echo "📊 Rendering CLI harness report..."
make cli-harness-report || echo "⚠️  Report rendering failed; reports/*.json are still intact"

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    # All matrix legs write into one job summary, so the heading has to say
    # which version generation this section is reporting on.
    if [ -n "${CLI_VERSION_LABEL:-}" ]; then
      echo "## CLI harness ($CLI_VERSION_LABEL)"
    else
      echo "## CLI harness"
    fi
    echo ""
    echo "| CLI | Version | Filter | Attempts | Result |"
    echo "| --- | --- | --- | --- | --- |"
    echo "| claude | \`$CLAUDE_CODE_VERSION\` | \`$CLAUDE_CASES\` | $(attempts_for claude) | $([ "$CLAUDE_RC" -eq 0 ] && echo "✅ pass" || echo "❌ fail") |"
    echo "| codex | \`$CODEX_VERSION\` | \`$CODEX_CASES\` | $(attempts_for codex) | $([ "$CODEX_RC" -eq 0 ] && echo "✅ pass" || echo "❌ fail") |"
    echo "| opencode | \`$OPENCODE_VERSION\` | \`$OPENCODE_CASES\` | $(attempts_for opencode) | $([ "$OPENCODE_RC" -eq 0 ] && echo "✅ pass" || echo "❌ fail") |"
    echo "| opencode-responses | \`$OPENCODE_VERSION\` | \`$OPENCODE_RESPONSES_CASES\` | $(attempts_for opencode-responses) | $([ "$OPENCODE_RESPONSES_RC" -eq 0 ] && echo "✅ pass" || echo "❌ fail") |"
    echo "| opencode-anthropic | \`$OPENCODE_VERSION\` | \`$OPENCODE_ANTHROPIC_CASES\` | $(attempts_for opencode-anthropic) | $([ "$OPENCODE_ANTHROPIC_RC" -eq 0 ] && echo "✅ pass" || echo "❌ fail") |"
    echo ""
    echo "Attempts > 1 means failed cells were re-run; only cells with status \`fail\` are retried."
    echo "Full per-cell results are in the \`cli-harness-reports${CLI_VERSION_LABEL:+-$CLI_VERSION_LABEL}\` artifact (\`index.html\`),"
    echo "with the pre-retry state under \`tmp/cli-harness-attempts/\`."
    echo ""
  } >> "$GITHUB_STEP_SUMMARY"
fi

if [ ! -d "$REPORTS_DIR" ]; then
  echo "⚠️  No reports directory at $REPORTS_DIR - the harness may not have run any cells"
fi

if [ "$CLAUDE_RC" -ne 0 ] || [ "$CODEX_RC" -ne 0 ] || [ "$OPENCODE_RC" -ne 0 ] || [ "$OPENCODE_RESPONSES_RC" -ne 0 ] || [ "$OPENCODE_ANTHROPIC_RC" -ne 0 ]; then
  echo "❌ CLI harness failed (claude=$CLAUDE_RC, codex=$CODEX_RC, opencode=$OPENCODE_RC, opencode-responses=$OPENCODE_RESPONSES_RC, opencode-anthropic=$OPENCODE_ANTHROPIC_RC)"
  exit 1
fi

echo "✅ CLI harness completed successfully"
