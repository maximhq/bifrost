#!/usr/bin/env bash
set -uo pipefail

# Tests setup-vllm-runpod.sh and teardown-vllm-runpod.sh, which create and delete the
# test-vllm job's GPU pods through RunPod's GraphQL API. A fake `curl` on PATH answers
# GraphQL calls from a queue of canned responses and fails every readiness probe, so
# this runs offline and never creates a pod.
#
# Usage: ./setup-vllm-runpod.test.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
KEY="rp-test-key-do-not-leak"

fixture_failed() { echo "FAILED: could not set up the fake $1" >&2; exit 1; }
mkdir -p "$WORK/bin" || fixture_failed "bin directory"
# GraphQL calls pop the next response from $WORK/responses (one JSON document per
# line) and record argv, the -K config and the request body. Anything else is the
# readiness probe, which always fails.
cat > "$WORK/bin/curl" <<EOF || fixture_failed curl
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "$WORK/argv.log"
config="" url=""
while [ \$# -gt 0 ]; do
  case "\$1" in
    -K) config="\$2"; shift 2 ;;
    -H|-o|--max-time|--data-binary|-d) shift 2 ;;
    -*) shift ;;
    *) url="\$1"; shift ;;
  esac
done
case "\$url" in
  */graphql)
    [ -n "\$config" ] && cat "\$config" >> "$WORK/config.log"
    jq -c . >> "$WORK/requests.log"
    head -n 1 "$WORK/responses"
    tail -n +2 "$WORK/responses" > "$WORK/responses.next"; mv "$WORK/responses.next" "$WORK/responses"
    ;;
  *) exit 22 ;;
esac
EOF
# setup-vllm-runpod.sh computes the backstop with GNU date -d; pin it so this also runs on macOS.
cat > "$WORK/bin/date" <<'EOF' || fixture_failed date
#!/usr/bin/env bash
for arg in "$@"; do [ "$arg" = "-d" ] && { echo "2026-11-26T12:00:00Z"; exit 0; }; done
exec /bin/date "$@"
EOF
chmod +x "$WORK/bin/curl" "$WORK/bin/date" || fixture_failed "executables (chmod)"
for tool in curl date; do
  [ "$(PATH="$WORK/bin:$PATH" command -v "$tool")" = "$WORK/bin/$tool" ] || fixture_failed "$tool (not first on PATH)"
done

fail=0
check() {
  if eval "$2"; then echo "  ok - $1"; else echo "  FAIL - $1"; fail=1; fi
}

# Resets the fake API with the given responses and runs a script; sets $OUT and $RC.
# Inherited RUNPOD_* / VLLM_* settings are cleared first so only the case's own overrides apply.
run() {
  local script="$1"; shift
  : > "$WORK/argv.log"; : > "$WORK/config.log"; : > "$WORK/requests.log"; : > "$WORK/github_env"
  printf '%s\n' "${RESPONSES[@]}" > "$WORK/responses"
  local unset_args=() name
  for name in $(compgen -e | grep -E '^(RUNPOD_|VLLM_)'); do unset_args+=(-u "$name"); done
  OUT="$(env ${unset_args[@]+"${unset_args[@]}"} PATH="$WORK/bin:$PATH" RUNPOD_API_KEY="$KEY" GITHUB_ENV="$WORK/github_env" GITHUB_RUN_ID=42 \
    VLLM_READY_TIMEOUT_SECONDS=0 VLLM_CREATE_RETRY_SECONDS=0 "$@" bash "$SCRIPT_DIR/$script" 2>&1)"
  RC=$?
}
request() { sed -n "${1}p" "$WORK/requests.log"; }

echo "setup: both pods created, backstop and pod shape sent"
RESPONSES=('{"data":{"podFindAndDeployOnDemand":{"id":"pod-default","costPerHr":0.4}}}'
  '{"data":{"podFindAndDeployOnDemand":{"id":"pod-reasoning","costPerHr":0.4}}}')
run setup-vllm-runpod.sh VLLM_GPU_TYPE_IDS="NVIDIA A40" RUNPOD_REASONING_NETWORK_VOLUME_ID=vol-r
check "both pod ids reach GITHUB_ENV for teardown" 'grep -qx "RUNPOD_POD_ID=pod-default" "$WORK/github_env" && grep -qx "RUNPOD_REASONING_POD_ID=pod-reasoning" "$WORK/github_env"'
check "fails at readiness, after recording the ids" '[ "$RC" -ne 0 ] && grep -q "Timed out waiting for the default" <<<"$OUT"'
check "create sends terminateAfter as the backstop" '[ "$(request 1 | jq -r .variables.input.terminateAfter)" = "2026-11-26T12:00:00Z" ]'
check "create keeps the runpodctl v2 pod shape" '[ "$(request 1 | jq -c "[.variables.input | .cloudType, .gpuCount, .gpuTypeId, .containerDiskInGb, .ports, .startSsh, .supportPublicIp, .volumeInGb, .volumeMountPath]")" = "[\"SECURE\",1,\"NVIDIA A40\",30,\"8000/http\",true,false,60,\"/root/.cache/huggingface\"]" ]'
check "a network volume replaces the per-pod volume" '[ "$(request 2 | jq -c "[.variables.input.networkVolumeId, .variables.input.volumeInGb]")" = "[\"vol-r\",null]" ]'
check "create calls podFindAndDeployOnDemand" 'request 1 | jq -e ".query | contains(\"podFindAndDeployOnDemand\")" >/dev/null'
check "the API key goes through -K, never argv" '! grep -q "$KEY" "$WORK/argv.log" && grep -q "Authorization: Bearer $KEY" "$WORK/config.log"'
check "the API key is not printed" '! grep -q "$KEY" <<<"$OUT"'

echo "setup: a capacity error moves to the next GPU"
RESPONSES=('{"errors":[{"message":"There are no longer any instances available with the requested specifications."}]}'
  '{"data":{"podFindAndDeployOnDemand":{"id":"pod-default"}}}'
  '{"data":{"podFindAndDeployOnDemand":{"id":"pod-reasoning"}}}')
run setup-vllm-runpod.sh VLLM_GPU_TYPE_IDS="NVIDIA A40,NVIDIA L40S"
check "retries the default pod on the second GPU" '[ "$(request 1 | jq -r .variables.input.gpuTypeId)" = "NVIDIA A40" ] && [ "$(request 2 | jq -r .variables.input.gpuTypeId)" = "NVIDIA L40S" ]'
check "the retried pod is recorded" 'grep -qx "RUNPOD_POD_ID=pod-default" "$WORK/github_env"'

echo "setup: any other error fails on the first try"
RESPONSES=('{"errors":[{"message":"Your account balance is too low to rent a pod. Please add funds to your account."}]}')
run setup-vllm-runpod.sh VLLM_GPU_TYPE_IDS="NVIDIA A40,NVIDIA L40S"
check "exits non-zero after one request" '[ "$RC" -ne 0 ] && [ "$(wc -l < "$WORK/requests.log" | tr -d " ")" = 1 ]'
check "surfaces RunPod's message" 'grep -q "balance is too low" <<<"$OUT"'
check "records no pod id" '! grep -q RUNPOD_POD_ID "$WORK/github_env"'

echo "setup: capacity exhausted on every attempt"
RESPONSES=('{"errors":[{"message":"no instances available"}]}' '{"errors":[{"message":"no instances available"}]}')
run setup-vllm-runpod.sh VLLM_GPU_TYPE_IDS="NVIDIA A40" VLLM_CREATE_ATTEMPTS=2
check "gives up after CREATE_ATTEMPTS" '[ "$RC" -ne 0 ] && [ "$(wc -l < "$WORK/requests.log" | tr -d " ")" = 2 ] && grep -q "after 2 attempts" <<<"$OUT"'

echo "teardown: both pods terminated"
RESPONSES=('{"data":{"podTerminate":null}}' '{"data":{"podTerminate":null}}')
run teardown-vllm-runpod.sh RUNPOD_POD_ID=pod-default RUNPOD_REASONING_POD_ID=pod-reasoning
check "terminates each pod by id" '[ "$(request 1 | jq -r .variables.podId)" = pod-default ] && [ "$(request 2 | jq -r .variables.podId)" = pod-reasoning ]'
check "calls podTerminate" 'request 1 | jq -e ".query | contains(\"podTerminate\")" >/dev/null'
check "reports success" '[ "$RC" -eq 0 ] && [ "$(grep -c "deleted" <<<"$OUT")" = 2 ]'

echo "teardown: a failed terminate warns and does not fail the job"
RESPONSES=('{"errors":[{"message":"pod not found"}]}')
run teardown-vllm-runpod.sh RUNPOD_POD_ID=pod-default
check "warns with RunPod's message" '[ "$RC" -eq 0 ] && grep -q "pod not found" <<<"$OUT" && grep -q "::warning::Failed to delete default pod pod-default" <<<"$OUT"'

echo "teardown: nothing to delete"
RESPONSES=()
run teardown-vllm-runpod.sh
check "makes no API call" '[ "$RC" -eq 0 ] && [ ! -s "$WORK/requests.log" ]'

if [ "$fail" -ne 0 ]; then
  echo "FAILED"
  exit 1
fi
echo "all passed"