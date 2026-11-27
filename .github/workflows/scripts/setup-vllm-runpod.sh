#!/usr/bin/env bash
set -euo pipefail

# Allocates two vLLM pods on RunPod for the test-vllm CI job and waits for
# both to be healthy: a default pod (serves Qwen3.5) and a reasoning pod
# (serves GLM-4.7), both on a pinned stable vLLM image.
#
# Usage: ./setup-vllm-runpod.sh
# Required env: RUNPOD_API_KEY
# Optional env (defaults match the two terraform configs above):
#   RUNPOD_NETWORK_VOLUME_ID, RUNPOD_REASONING_NETWORK_VOLUME_ID - persist
#     each pod's HF cache across runs so models aren't re-downloaded every
#     release-pipeline run - create once in the RunPod console and store the
#     ids as these secrets,
#   VLLM_IMAGE, VLLM_MODEL_NAME, VLLM_TRUST_REMOTE_CODE, VLLM_TOOL_CALL_PARSER,
#     VLLM_REASONING_PARSER, VLLM_MAX_MODEL_LEN, VLLM_ENABLE_THINKING
#     (default pod)
#   VLLM_REASONING_IMAGE, VLLM_REASONING_MODEL_NAME,
#     VLLM_REASONING_TRUST_REMOTE_CODE, VLLM_REASONING_TOOL_CALL_PARSER,
#     VLLM_REASONING_REASONING_PARSER, VLLM_REASONING_MAX_MODEL_LEN,
#     VLLM_REASONING_ENABLE_THINKING (reasoning pod)
#   VLLM_READY_TIMEOUT_SECONDS, VLLM_MAX_POD_LIFETIME_MINUTES,
#     VLLM_GPU_TYPE_IDS (comma-separated, tried in order), VLLM_CREATE_ATTEMPTS,
#     VLLM_CREATE_RETRY_SECONDS (shared)
#
# Exports (via $GITHUB_ENV) for later steps in the same job:
#   RUNPOD_POD_ID, VLLM_BASE_URL, VLLM_API_KEY, VLLM_CHAT_MODEL, VLLM_TEXT_MODEL,
#   RUNPOD_REASONING_POD_ID, VLLM_REASONING_BASE_URL, VLLM_REASONING_MODEL

# Talks to RunPod's GraphQL API directly: runpodctl v2 deletes through the retired
# rest.runpod.io host, and v3 dropped --terminate-after, the backstop below.
# shellcheck source=/dev/null
source "$(dirname "$0")/runpod-api.sh"

API_PORT=8000
IFS=',' read -r -a GPU_TYPE_IDS <<< "${VLLM_GPU_TYPE_IDS:-NVIDIA A40,NVIDIA L40S,NVIDIA RTX A6000}"
CREATE_ATTEMPTS="${VLLM_CREATE_ATTEMPTS:-4}"
CREATE_RETRY_SECONDS="${VLLM_CREATE_RETRY_SECONDS:-30}"
READY_TIMEOUT_SECONDS="${VLLM_READY_TIMEOUT_SECONDS:-1200}"
MAX_POD_LIFETIME_MINUTES="${VLLM_MAX_POD_LIFETIME_MINUTES:-120}"

if [ -z "${RUNPOD_API_KEY:-}" ]; then
  echo "::error::RUNPOD_API_KEY is not set" >&2
  exit 1
fi

TERMINATE_AFTER=$(date -u -d "+${MAX_POD_LIFETIME_MINUTES} minutes" '+%Y-%m-%dT%H:%M:%SZ')

# Shared across both pods.
VLLM_API_KEY=$(openssl rand -hex 24)
echo "::add-mask::${VLLM_API_KEY}"

# Creates one vLLM pod, echoes the pod id on success.
# Args: name-suffix image model trust-remote-code tool-call-parser reasoning-parser
# enable-thinking max-model-len network-volume-id-env.
create_vllm_pod() {
  local name_suffix="$1" image="$2" model="$3" trust_remote_code="$4" \
    tool_call_parser="$5" reasoning_parser="$6" enable_thinking="$7" \
    max_model_len="$8" network_volume_id="$9"

  local docker_args="--model ${model} --host 0.0.0.0 --port ${API_PORT} --max-model-len ${max_model_len} --api-key ${VLLM_API_KEY} --enable-auto-tool-choice --tool-call-parser ${tool_call_parser} --reasoning-parser ${reasoning_parser} --default-chat-template-kwargs {\\\"enable_thinking\\\":${enable_thinking}}"
  if [ "$trust_remote_code" = "true" ]; then
    docker_args="${docker_args} --trust-remote-code"
  fi

  local input
  input=$(jq -n \
    --arg name "bifrost-ci-vllm-${name_suffix}-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}" \
    --arg image "$image" \
    --arg ports "${API_PORT}/http" \
    --arg docker_args "$docker_args" \
    --arg terminate_after "$TERMINATE_AFTER" \
    --arg network_volume_id "$network_volume_id" \
    '{
      cloudType: "SECURE",
      containerDiskInGb: 30,
      gpuCount: 1,
      imageName: $image,
      name: $name,
      ports: $ports,
      dockerArgs: $docker_args,
      startSsh: true,
      supportPublicIp: false,
      terminateAfter: $terminate_after,
      volumeMountPath: "/root/.cache/huggingface"
    } + (if $network_volume_id != "" then {networkVolumeId: $network_volume_id} else {volumeInGb: 60} end)')
  if [ -n "$network_volume_id" ]; then
    echo "📦 Using persistent network volume ${network_volume_id} for the ${name_suffix} pod's HF cache" >&2
  fi

  # Only capacity errors are retried, across GPU_TYPE_IDS then again after a pause.
  # Anything else (bad input, auth, balance) fails on the first try.
  local query='mutation createPod($input: PodFindAndDeployOnDemandInput!) { podFindAndDeployOnDemand(input: $input) { id costPerHr machine { gpuDisplayName location } } }'
  local response="" error="" pod_id="" attempt gpu
  for attempt in $(seq 1 "$CREATE_ATTEMPTS"); do
    for gpu in "${GPU_TYPE_IDS[@]}"; do
      echo "🚀 Creating ${name_suffix} vLLM pod ($model on $gpu, attempt ${attempt}/${CREATE_ATTEMPTS}, auto-terminates at ${TERMINATE_AFTER} as a backstop)..." >&2
      response=$(runpod_graphql "$query" "$(jq -c --arg gpu "$gpu" '{input: (. + {gpuTypeId: $gpu})}' <<<"$input")") || response=""
      pod_id=$(jq -r '.data.podFindAndDeployOnDemand.id // empty' <<<"$response" 2>/dev/null || true)
      if [ -n "$pod_id" ]; then
        jq -c '.data.podFindAndDeployOnDemand' <<<"$response" >&2
        echo "$pod_id"
        return 0
      fi
      error=$(jq -r '[.errors[]?.message] | join("; ")' <<<"$response" 2>/dev/null || true)
      echo "${error:-${response:-no response from ${RUNPOD_GRAPHQL_URL}}}" >&2
      if ! grep -qiE "no (longer any )?instances" <<<"$error"; then
        echo "::error::Failed to create the ${name_suffix} vLLM pod; see RunPod error above" >&2
        return 1
      fi
    done
    if [ "$attempt" -lt "$CREATE_ATTEMPTS" ]; then
      echo "⏳ No capacity for the ${name_suffix} pod on any of: ${GPU_TYPE_IDS[*]}; retrying in ${CREATE_RETRY_SECONDS}s" >&2
      sleep "$CREATE_RETRY_SECONDS"
    fi
  done
  echo "::error::No RunPod capacity for the ${name_suffix} vLLM pod on ${GPU_TYPE_IDS[*]} after ${CREATE_ATTEMPTS} attempts" >&2
  return 1
}

# Waits for a pod's OpenAI-compatible endpoint to answer. Args: name-suffix base-url api-key.
wait_for_vllm_ready() {
  local name_suffix="$1" base_url="$2" api_key="$3"
  echo "⏳ Waiting for the ${name_suffix} vLLM endpoint to become ready (timeout: ${READY_TIMEOUT_SECONDS}s)..."
  local seconds_waited=0
  until curl -sf --max-time 10 -o /dev/null -H "Authorization: Bearer ${api_key}" "${base_url}/v1/models"; do
    if [ "$seconds_waited" -ge "$READY_TIMEOUT_SECONDS" ]; then
      echo "::error::Timed out waiting for the ${name_suffix} vLLM endpoint to become ready" >&2
      exit 1
    fi
    sleep 15
    seconds_waited=$((seconds_waited + 15))
    echo "  ...still waiting on ${name_suffix} pod (${seconds_waited}s elapsed)"
  done
  echo "✅ ${name_suffix} vLLM endpoint is ready"
}

# --- Default pod: chat/text/tool-calling scenarios (thinking off) ---
DEFAULT_MODEL_NAME="${VLLM_MODEL_NAME:-Qwen/Qwen3.5-35B-A3B-GPTQ-Int4}"
POD_ID=$(create_vllm_pod \
  "default" \
  "${VLLM_IMAGE:-vllm/vllm-openai:v0.31.0}" \
  "$DEFAULT_MODEL_NAME" \
  "${VLLM_TRUST_REMOTE_CODE:-false}" \
  "${VLLM_TOOL_CALL_PARSER:-qwen3_coder}" \
  "${VLLM_REASONING_PARSER:-qwen3}" \
  "${VLLM_ENABLE_THINKING:-false}" \
  "${VLLM_MAX_MODEL_LEN:-32768}" \
  "${RUNPOD_NETWORK_VOLUME_ID:-}")
# Make the first pod available to teardown even if the second creation fails.
echo "RUNPOD_POD_ID=${POD_ID}" >> "$GITHUB_ENV"

# --- Reasoning pod: Reasoning scenario only (thinking on) ---
REASONING_MODEL_NAME="${VLLM_REASONING_MODEL_NAME:-QuantTrio/GLM-4.7-Flash-AWQ}"
REASONING_POD_ID=$(create_vllm_pod \
  "reasoning" \
  "${VLLM_REASONING_IMAGE:-vllm/vllm-openai:v0.31.0}" \
  "$REASONING_MODEL_NAME" \
  "${VLLM_REASONING_TRUST_REMOTE_CODE:-true}" \
  "${VLLM_REASONING_TOOL_CALL_PARSER:-glm47}" \
  "${VLLM_REASONING_REASONING_PARSER:-glm45}" \
  "${VLLM_REASONING_ENABLE_THINKING:-true}" \
  "${VLLM_REASONING_MAX_MODEL_LEN:-131072}" \
  "${RUNPOD_REASONING_NETWORK_VOLUME_ID:-}")
echo "RUNPOD_REASONING_POD_ID=${REASONING_POD_ID}" >> "$GITHUB_ENV"

VLLM_BASE_URL="https://${POD_ID}-${API_PORT}.proxy.runpod.net"
VLLM_REASONING_BASE_URL="https://${REASONING_POD_ID}-${API_PORT}.proxy.runpod.net"
echo "Default pod: ${POD_ID} -> ${VLLM_BASE_URL}"
echo "Reasoning pod: ${REASONING_POD_ID} -> ${VLLM_REASONING_BASE_URL}"

# Persist for later steps in this job (test run + teardown).
{
  echo "VLLM_BASE_URL=${VLLM_BASE_URL}"
  echo "VLLM_API_KEY=${VLLM_API_KEY}"
  echo "VLLM_CHAT_MODEL=${DEFAULT_MODEL_NAME}"
  echo "VLLM_TEXT_MODEL=${DEFAULT_MODEL_NAME}"
  echo "VLLM_REASONING_BASE_URL=${VLLM_REASONING_BASE_URL}"
  echo "VLLM_REASONING_MODEL=${REASONING_MODEL_NAME}"
} >> "$GITHUB_ENV"

wait_for_vllm_ready "default" "$VLLM_BASE_URL" "$VLLM_API_KEY"
wait_for_vllm_ready "reasoning" "$VLLM_REASONING_BASE_URL" "$VLLM_API_KEY"