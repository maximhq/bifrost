#!/usr/bin/env bash
set -euo pipefail

# Allocates two vLLM pods on RunPod for the test-vllm CI job and waits for
# both to be healthy: a default vllm server pod (servers qwen3.5) and a vllm 
# nightly server pod (servers GLM-4.7).
#
# Usage: ./setup-vllm-runpod.sh
# Required env: RUNPOD_API_KEY
# Optional env (defaults match the two terraform configs above):
#   RUNPOD_NETWORK_VOLUME_ID, RUNPOD_REASONING_NETWORK_VOLUME_ID - persist
#     each pod's HF cache across runs so models aren't re-downloaded every
#     release-pipeline run - create once with `runpodctl network-volume
#     create` and store the ids as these secrets,
#   VLLM_IMAGE, VLLM_MODEL_NAME, VLLM_TRUST_REMOTE_CODE, VLLM_TOOL_CALL_PARSER,
#     VLLM_REASONING_PARSER, VLLM_MAX_MODEL_LEN, VLLM_ENABLE_THINKING
#     (default pod)
#   VLLM_REASONING_IMAGE, VLLM_REASONING_MODEL_NAME,
#     VLLM_REASONING_TRUST_REMOTE_CODE, VLLM_REASONING_TOOL_CALL_PARSER,
#     VLLM_REASONING_REASONING_PARSER, VLLM_REASONING_MAX_MODEL_LEN,
#     VLLM_REASONING_ENABLE_THINKING (reasoning pod)
#   VLLM_READY_TIMEOUT_SECONDS, VLLM_MAX_POD_LIFETIME_MINUTES (shared)
#
# Exports (via $GITHUB_ENV) for later steps in the same job:
#   RUNPOD_POD_ID, VLLM_BASE_URL, VLLM_API_KEY, VLLM_CHAT_MODEL, VLLM_TEXT_MODEL,
#   RUNPOD_REASONING_POD_ID, VLLM_REASONING_BASE_URL, VLLM_REASONING_MODEL

RUNPODCTL_VERSION="v2.7.2"
RUNPODCTL_LINUX_AMD64_SHA256="acf5c49a3192b522e95cae92539fa6fcd8be8c48802aa26c7f3f2ec980ab4f5c"

API_PORT=8000
GPU_TYPE_ID="NVIDIA A40"
READY_TIMEOUT_SECONDS="${VLLM_READY_TIMEOUT_SECONDS:-1200}"
MAX_POD_LIFETIME_MINUTES="${VLLM_MAX_POD_LIFETIME_MINUTES:-120}"

if [ -z "${RUNPOD_API_KEY:-}" ]; then
  echo "::error::RUNPOD_API_KEY is not set" >&2
  exit 1
fi

echo "🔧 Installing runpodctl ${RUNPODCTL_VERSION}..."
RUNPODCTL_TMP="$(mktemp)"
trap 'rm -f "$RUNPODCTL_TMP"' EXIT
curl -sSL -o "$RUNPODCTL_TMP" "https://github.com/runpod/runpodctl/releases/download/${RUNPODCTL_VERSION}/runpodctl-linux-amd64"
echo "${RUNPODCTL_LINUX_AMD64_SHA256}  ${RUNPODCTL_TMP}" | sha256sum -c -
chmod +x "$RUNPODCTL_TMP"
sudo mv "$RUNPODCTL_TMP" /usr/local/bin/runpodctl

# runpodctl reads RUNPOD_API_KEY from the environment directly.

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

  local create_args=(
    pod create
    --name "bifrost-ci-vllm-${name_suffix}-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}"
    --image "$image"
    --gpu-id "$GPU_TYPE_ID"
    --gpu-count 1
    --cloud-type SECURE
    --container-disk-in-gb 30
    --ports "${API_PORT}/http"
    --docker-args "$docker_args"
    --terminate-after "$TERMINATE_AFTER"
    -o json
  )
  if [ -n "$network_volume_id" ]; then
    echo "📦 Using persistent network volume ${network_volume_id} for the ${name_suffix} pod's HF cache" >&2
    create_args+=(--network-volume-id "$network_volume_id" --volume-mount-path /root/.cache/huggingface)
  else
    create_args+=(--volume-in-gb 60 --volume-mount-path /root/.cache/huggingface)
  fi

  echo "🚀 Creating ${name_suffix} vLLM pod ($model on $GPU_TYPE_ID, auto-terminates at ${TERMINATE_AFTER} as a backstop)..." >&2
  local create_output
  create_output=$(runpodctl "${create_args[@]}")
  echo "$create_output" >&2

  local pod_id
  pod_id=$(echo "$create_output" | jq -r '.id // empty')
  if [ -z "$pod_id" ]; then
    echo "::error::Failed to parse pod id from runpodctl output for the ${name_suffix} pod" >&2
    exit 1
  fi

  echo "$pod_id"
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
  "${VLLM_IMAGE:-vllm/vllm-openai:latest}" \
  "$DEFAULT_MODEL_NAME" \
  "${VLLM_TRUST_REMOTE_CODE:-false}" \
  "${VLLM_TOOL_CALL_PARSER:-qwen3_coder}" \
  "${VLLM_REASONING_PARSER:-qwen3}" \
  "${VLLM_ENABLE_THINKING:-false}" \
  "${VLLM_MAX_MODEL_LEN:-32768}" \
  "${RUNPOD_NETWORK_VOLUME_ID:-}")

# --- Reasoning pod: Reasoning scenario only (thinking on) ---
REASONING_MODEL_NAME="${VLLM_REASONING_MODEL_NAME:-QuantTrio/GLM-4.7-Flash-AWQ}"
REASONING_POD_ID=$(create_vllm_pod \
  "reasoning" \
  "${VLLM_REASONING_IMAGE:-vllm/vllm-openai:nightly}" \
  "$REASONING_MODEL_NAME" \
  "${VLLM_REASONING_TRUST_REMOTE_CODE:-true}" \
  "${VLLM_REASONING_TOOL_CALL_PARSER:-glm47}" \
  "${VLLM_REASONING_REASONING_PARSER:-glm45}" \
  "${VLLM_REASONING_ENABLE_THINKING:-true}" \
  "${VLLM_REASONING_MAX_MODEL_LEN:-131072}" \
  "${RUNPOD_REASONING_NETWORK_VOLUME_ID:-}")

VLLM_BASE_URL="https://${POD_ID}-${API_PORT}.proxy.runpod.net"
VLLM_REASONING_BASE_URL="https://${REASONING_POD_ID}-${API_PORT}.proxy.runpod.net"
echo "Default pod: ${POD_ID} -> ${VLLM_BASE_URL}"
echo "Reasoning pod: ${REASONING_POD_ID} -> ${VLLM_REASONING_BASE_URL}"

# Persist for later steps in this job (test run + teardown).
{
  echo "RUNPOD_POD_ID=${POD_ID}"
  echo "VLLM_BASE_URL=${VLLM_BASE_URL}"
  echo "VLLM_API_KEY=${VLLM_API_KEY}"
  echo "VLLM_CHAT_MODEL=${DEFAULT_MODEL_NAME}"
  echo "VLLM_TEXT_MODEL=${DEFAULT_MODEL_NAME}"
  echo "RUNPOD_REASONING_POD_ID=${REASONING_POD_ID}"
  echo "VLLM_REASONING_BASE_URL=${VLLM_REASONING_BASE_URL}"
  echo "VLLM_REASONING_MODEL=${REASONING_MODEL_NAME}"
} >> "$GITHUB_ENV"

wait_for_vllm_ready "default" "$VLLM_BASE_URL" "$VLLM_API_KEY"
wait_for_vllm_ready "reasoning" "$VLLM_REASONING_BASE_URL" "$VLLM_API_KEY"