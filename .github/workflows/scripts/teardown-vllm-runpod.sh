#!/usr/bin/env bash
set -uo pipefail

# Deletes the RunPod vLLM pod(s) created by setup-vllm-runpod.sh.
#
# Usage: ./teardown-vllm-runpod.sh
# Required env: RUNPOD_API_KEY
# Optional env: RUNPOD_POD_ID, RUNPOD_REASONING_POD_ID (set by
#   setup-vllm-runpod.sh via $GITHUB_ENV)
#
# Runs with `if: always()` in the workflow so it fires even if the test step
# failed; --terminate-after (set at pod creation) is the backstop in case this
# step itself never runs (e.g. the runner is killed).

if [ -z "${RUNPOD_POD_ID:-}" ] && [ -z "${RUNPOD_REASONING_POD_ID:-}" ]; then
  echo "No RUNPOD_POD_ID/RUNPOD_REASONING_POD_ID set, nothing to tear down."
  exit 0
fi

if [ -z "${RUNPOD_API_KEY:-}" ]; then
  echo "::error::RUNPOD_API_KEY is not set" >&2
  exit 1
fi

if ! command -v runpodctl >/dev/null 2>&1; then
  echo "::warning::runpodctl not found, cannot delete pods - check the RunPod console (auto-terminate-after is set as a backstop)"
  exit 0
fi

# runpodctl reads RUNPOD_API_KEY from the environment directly.

delete_pod() {
  local name_suffix="$1" pod_id="$2"
  if [ -z "$pod_id" ]; then
    return 0
  fi
  echo "🧹 Deleting ${name_suffix} vLLM pod ${pod_id}..."
  if runpodctl pod delete "$pod_id"; then
    echo "✅ ${name_suffix} pod ${pod_id} deleted"
  else
    echo "::warning::Failed to delete ${name_suffix} pod ${pod_id} - check the RunPod console (auto-terminate-after is set as a backstop)"
  fi
}

delete_pod "default" "${RUNPOD_POD_ID:-}"
delete_pod "reasoning" "${RUNPOD_REASONING_POD_ID:-}"