#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

# Setup Go workspace for CI (go.work is gitignored, must be regenerated) so the
# build below resolves local core/framework/plugins instead of the published
# versions pinned in transports/go.mod. Run with repo root as CWD because
# setup-go-workspace.sh's `go work use ./core` paths are repo-root-relative; the
# go.work file it writes at ${ROOT_DIR} is then auto-discovered by `go build`.
( cd "${ROOT_DIR}" && source "${ROOT_DIR}/.github/workflows/scripts/setup-go-workspace.sh" )

COMPOSE_FILE="${ROOT_DIR}/.github/workflows/configs/docker-compose.yml"
COMPOSE_PROJECT="${COMPOSE_PROJECT:-bifrost-cost-accuracy}"
BENCHMARK_DIR="${BENCHMARK_DIR:-${ROOT_DIR}/../bifrost-benchmarking}"
WORK_DIR="${ROOT_DIR}/tmp/cost-accuracy"
APP_DIR="${WORK_DIR}/app"
RESULTS_FILE="${WORK_DIR}/results.json"
BIFROST_BIN="${ROOT_DIR}/tmp/bifrost-http"
MOCKER_BIN="${ROOT_DIR}/tmp/mocker"
HITTER_BIN="${ROOT_DIR}/tmp/hitter"
POSTGRES_DB="${POSTGRES_DB:-bifrost_cost_accuracy}"
POSTGRES_USER="${POSTGRES_USER:-bifrost}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-bifrost_password}"
POSTGRES_HOST="${POSTGRES_HOST:-127.0.0.1}"
POSTGRES_PORT="${POSTGRES_PORT:-5432}"
MOCKER_PORT="${MOCKER_PORT:-8000}"
BIFROST_PORT="${BIFROST_PORT:-8080}"
RPS="${COST_ACCURACY_RPS:-10}"
DURATION="${COST_ACCURACY_DURATION:-10s}"
INPUT_COST_PER_TOKEN="${INPUT_COST_PER_TOKEN:-0.000001}"
OUTPUT_COST_PER_TOKEN="${OUTPUT_COST_PER_TOKEN:-0.000002}"
VIRTUAL_KEY_BUDGET_LIMIT="${VIRTUAL_KEY_BUDGET_LIMIT:-100}"

BIFROST_PID=""
MOCKER_PID=""
COMPOSE_CMD=()
VIRTUAL_KEY_ID=""
VIRTUAL_KEY_VALUE=""

log() {
  echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] $*"
}

cleanup() {
  set +e
  if [ -n "${BIFROST_PID}" ] && kill -0 "${BIFROST_PID}" 2>/dev/null; then
    kill "${BIFROST_PID}" 2>/dev/null || true
    wait "${BIFROST_PID}" 2>/dev/null || true
  fi
  if [ -n "${MOCKER_PID}" ] && kill -0 "${MOCKER_PID}" 2>/dev/null; then
    kill "${MOCKER_PID}" 2>/dev/null || true
    wait "${MOCKER_PID}" 2>/dev/null || true
  fi
  docker_compose -p "${COMPOSE_PROJECT}" -f "${COMPOSE_FILE}" down >/dev/null 2>&1 || true
}
trap cleanup EXIT

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

docker_compose() {
  if [ "${#COMPOSE_CMD[@]}" -gt 0 ]; then
    "${COMPOSE_CMD[@]}" "$@"
  elif docker compose version >/dev/null 2>&1; then
    docker compose "$@"
  elif command -v docker-compose >/dev/null 2>&1; then
    docker-compose "$@"
  else
    return 127
  fi
}

ensure_docker_compose() {
  if docker compose version >/dev/null 2>&1; then
    COMPOSE_CMD=(docker compose)
    return
  fi
  if command -v docker-compose >/dev/null 2>&1; then
    COMPOSE_CMD=(docker-compose)
    return
  fi
  if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
    log "installing Docker Compose plugin"
    local docker_config="${DOCKER_CONFIG:-${HOME}/.docker}"
    mkdir -p "${docker_config}/cli-plugins"
    curl -fsSL "https://github.com/docker/compose/releases/latest/download/docker-compose-$(uname -s)-$(uname -m)" \
      -o "${docker_config}/cli-plugins/docker-compose"
    chmod +x "${docker_config}/cli-plugins/docker-compose"
    docker compose version >/dev/null
    COMPOSE_CMD=(docker compose)
    return
  fi
  echo "missing required command: docker compose or docker-compose" >&2
  exit 1
}

wait_http() {
  local url=$1
  local attempts=${2:-60}
  for _ in $(seq 1 "${attempts}"); do
    if curl -fsS "${url}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

start_postgres() {
  log "starting Postgres from ${COMPOSE_FILE}"
  docker_compose -p "${COMPOSE_PROJECT}" -f "${COMPOSE_FILE}" up -d postgres
  local container
  container="$(docker_compose -p "${COMPOSE_PROJECT}" -f "${COMPOSE_FILE}" ps -q postgres)"
  local pg_ready=0
  for _ in $(seq 1 60); do
    if docker exec "${container}" pg_isready -U "${POSTGRES_USER}" -d bifrost >/dev/null 2>&1; then
      log "Postgres is ready"
      pg_ready=1
      break
    fi
    sleep 1
  done
  if [ "${pg_ready}" -ne 1 ]; then
    log "Postgres did not become ready within 60s"
    docker logs --tail 100 "${container}" >&2 || true
    exit 1
  fi
  docker exec -e PGPASSWORD="${POSTGRES_PASSWORD}" "${container}" \
    psql -v ON_ERROR_STOP=1 -U "${POSTGRES_USER}" -d postgres \
      -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '${POSTGRES_DB}' AND pid <> pg_backend_pid();" \
      -c "DROP DATABASE IF EXISTS \"${POSTGRES_DB}\";" \
      -c "CREATE DATABASE \"${POSTGRES_DB}\";" >/dev/null
}

build_binaries() {
  if [ ! -d "${BENCHMARK_DIR}" ]; then
    log "cloning bifrost-benchmarking"
    git clone --depth 1 https://github.com/maximhq/bifrost-benchmarking.git "${BENCHMARK_DIR}"
  fi

  mkdir -p "${ROOT_DIR}/tmp" "${ROOT_DIR}/transports/bifrost-http/ui"
  touch "${ROOT_DIR}/transports/bifrost-http/ui/.gitkeep"

  log "building bifrost-http"
  (cd "${ROOT_DIR}/transports/bifrost-http" && go build -o "${BIFROST_BIN}" .)

  # GOWORK=off: bifrost-benchmarking is its own module and in CI is checked out
  # inside the repo root (${github.workspace}/bifrost-benchmarking), so `go build`
  # would auto-discover the repo's go.work and reject the mocker/hitter modules
  # ("not one of the workspace modules"). These binaries don't need local
  # core/framework, so build them outside the workspace.
  log "building benchmark mocker"
  (cd "${BENCHMARK_DIR}/mocker" && GOWORK=off go build -o "${MOCKER_BIN}" .)

  log "building benchmark hitter"
  (cd "${BENCHMARK_DIR}/hitter" && GOWORK=off go build -o "${HITTER_BIN}" .)
}

write_config() {
  rm -rf "${APP_DIR}"
  mkdir -p "${APP_DIR}"
  cat > "${APP_DIR}/config.json" <<EOF
{
  "\$schema": "https://www.getbifrost.ai/schema",
  "client": {
    "enable_logging": true,
    "drop_excess_requests": false,
    "allow_direct_keys": true,
    "enforce_auth_on_inference": false,
    "initial_pool_size": 1000
  },
  "config_store": {
    "enabled": true,
    "type": "postgres",
    "config": {
      "host": "${POSTGRES_HOST}",
      "port": "${POSTGRES_PORT}",
      "user": "${POSTGRES_USER}",
      "password": "${POSTGRES_PASSWORD}",
      "db_name": "${POSTGRES_DB}",
      "ssl_mode": "disable"
    }
  },
  "logs_store": {
    "enabled": true,
    "type": "postgres",
    "config": {
      "host": "${POSTGRES_HOST}",
      "port": "${POSTGRES_PORT}",
      "user": "${POSTGRES_USER}",
      "password": "${POSTGRES_PASSWORD}",
      "db_name": "${POSTGRES_DB}",
      "ssl_mode": "disable"
    }
  },
  "providers": {
    "openai": {
      "keys": [
        {
          "name": "mocker-key",
          "value": "Bearer mocker-key",
          "weight": 1,
          "models": ["*"]
        }
      ],
      "network_config": {
        "base_url": "http://127.0.0.1:${MOCKER_PORT}",
        "default_request_timeout_in_seconds": 30,
        "max_retries": 0
      },
      "concurrency_and_buffer_size": {
        "concurrency": 1000,
        "buffer_size": 2000
      }
    }
  }
}
EOF
}

start_services() {
  log "starting mocker on ${MOCKER_PORT}"
  "${MOCKER_BIN}" -host 127.0.0.1 -port "${MOCKER_PORT}" -latency 0 > "${WORK_DIR}/mocker.log" 2>&1 &
  MOCKER_PID=$!
  if ! wait_http "http://127.0.0.1:${MOCKER_PORT}/health" 60; then
    tail -n 100 "${WORK_DIR}/mocker.log" >&2 || true
    exit 1
  fi

  log "starting bifrost-http on ${BIFROST_PORT}"
  "${BIFROST_BIN}" -app-dir "${APP_DIR}" -host 127.0.0.1 -port "${BIFROST_PORT}" -log-level info > "${WORK_DIR}/bifrost.log" 2>&1 &
  BIFROST_PID=$!
  if ! wait_http "http://127.0.0.1:${BIFROST_PORT}/health" 90; then
    tail -n 100 "${WORK_DIR}/bifrost.log" >&2 || true
    exit 1
  fi
}

create_virtual_key() {
  log "creating virtual key with attached budget"
  curl -fsS -X POST "http://127.0.0.1:${BIFROST_PORT}/api/governance/virtual-keys" \
    -H "Content-Type: application/json" \
    -d "{
      \"name\": \"cost-accuracy-vk\",
      \"description\": \"Cost accuracy CI virtual key\",
      \"provider_configs\": [
        {
          \"provider\": \"openai\",
          \"allowed_models\": [\"*\"],
          \"key_ids\": [\"*\"]
        }
      ],
      \"budgets\": [
        {
          \"max_limit\": ${VIRTUAL_KEY_BUDGET_LIMIT},
          \"reset_duration\": \"1d\"
        }
      ],
      \"is_active\": true
    }" > "${WORK_DIR}/virtual-key.json"

  read -r VIRTUAL_KEY_ID VIRTUAL_KEY_VALUE < <(python3 - "${WORK_DIR}/virtual-key.json" <<'PY'
import json
import sys
from pathlib import Path

payload = json.loads(Path(sys.argv[1]).read_text())
vk = payload["virtual_key"]
print(vk["id"], vk["value"])
PY
)

  if [ -z "${VIRTUAL_KEY_ID}" ] || [ -z "${VIRTUAL_KEY_VALUE}" ]; then
    echo "failed to parse virtual key response" >&2
    exit 1
  fi
}

create_pricing_override() {
  log "creating virtual-key scoped pricing override"
  curl -fsS -X POST "http://127.0.0.1:${BIFROST_PORT}/api/governance/pricing-overrides" \
    -H "Content-Type: application/json" \
    -d "{
      \"name\": \"cost accuracy gpt-4o-mini vk\",
      \"scope_kind\": \"virtual_key\",
      \"virtual_key_id\": \"${VIRTUAL_KEY_ID}\",
      \"match_type\": \"exact\",
      \"pattern\": \"gpt-4o-mini\",
      \"request_types\": [\"chat_completion\"],
      \"patch\": {
        \"input_cost_per_token\": ${INPUT_COST_PER_TOKEN},
        \"output_cost_per_token\": ${OUTPUT_COST_PER_TOKEN}
      }
    }" > "${WORK_DIR}/pricing-override.json"
}

run_hitter() {
  log "running hitter at ${RPS} RPS for ${DURATION}"
  date -u +"%Y-%m-%dT%H:%M:%SZ" > "${WORK_DIR}/run-start.txt"
  "${HITTER_BIN}" \
    -url "http://127.0.0.1:${BIFROST_PORT}/v1/chat/completions" \
    -providers openai \
    -models gpt-4o-mini \
    -virtual-key "${VIRTUAL_KEY_VALUE}" \
    -rps "${RPS}" \
    -duration "${DURATION}" \
    -max-tokens 150 > "${WORK_DIR}/hitter.log" 2>&1
}

validate_costs() {
  log "validating logged costs"
  python3 - "$BIFROST_PORT" "$INPUT_COST_PER_TOKEN" "$OUTPUT_COST_PER_TOKEN" "$RESULTS_FILE" "${WORK_DIR}/hitter.log" "${WORK_DIR}/run-start.txt" "$VIRTUAL_KEY_ID" "$VIRTUAL_KEY_VALUE" <<'PY'
import json
import math
import re
import sys
import time
import urllib.parse
import urllib.request
from pathlib import Path

port = sys.argv[1]
input_rate = float(sys.argv[2])
output_rate = float(sys.argv[3])
results_file = Path(sys.argv[4])
hitter_log = Path(sys.argv[5]).read_text(errors="replace")
start_time = Path(sys.argv[6]).read_text().strip()
virtual_key_id = sys.argv[7]
virtual_key_value = sys.argv[8]
match = re.search(r"Successful:\s+(\d+)", hitter_log)
if not match:
    raise SystemExit("could not parse successful request count from hitter log")
expected_count = int(match.group(1))
if expected_count <= 0:
    raise SystemExit("hitter reported zero successful requests")

base = f"http://127.0.0.1:{port}"

def get_json(path, params):
    url = base + path + "?" + urllib.parse.urlencode(params)
    with urllib.request.urlopen(url, timeout=10) as resp:
        return json.loads(resp.read().decode("utf-8"))

params = {
    "providers": "openai",
    "models": "gpt-4o-mini",
    "status": "success",
    "virtual_key_ids": virtual_key_id,
    "start_time": start_time,
    "limit": "1000",
    "sort_by": "timestamp",
    "order": "asc",
}

def logs_complete(logs):
    # Log writes are fully async (single batched insert in PostLLMHook), so a
    # row can be visible before its usage/cost are readable. Poll on the
    # predicate we assert (every row has usage and cost), not just row count.
    return all(
        (item.get("token_usage") or {}).get("prompt_tokens") is not None
        and (item.get("token_usage") or {}).get("completion_tokens") is not None
        and item.get("cost") is not None
        for item in logs
    )

logs = []
for _ in range(60):
    payload = get_json("/api/logs", params)
    logs = payload.get("logs", [])
    if len(logs) >= expected_count and logs_complete(logs):
        break
    time.sleep(1)

if len(logs) != expected_count:
    raise SystemExit(f"log count mismatch: got {len(logs)}, want {expected_count}")

mismatches = []
expected_total = 0.0
actual_total = 0.0
for item in logs:
    if item.get("virtual_key_id") != virtual_key_id:
        mismatches.append({
            "id": item.get("id"),
            "reason": "virtual_key_id mismatch",
            "expected_virtual_key_id": virtual_key_id,
            "actual_virtual_key_id": item.get("virtual_key_id"),
        })
        continue
    usage = item.get("token_usage") or {}
    prompt = usage.get("prompt_tokens")
    completion = usage.get("completion_tokens")
    actual = item.get("cost")
    if prompt is None or completion is None or actual is None:
        mismatches.append({"id": item.get("id"), "reason": "missing token_usage or cost"})
        continue
    expected = prompt * input_rate + completion * output_rate
    expected_total += expected
    actual_total += actual
    if math.fabs(actual - expected) > 1e-12:
        mismatches.append({
            "id": item.get("id"),
            "prompt_tokens": prompt,
            "completion_tokens": completion,
            "expected": expected,
            "actual": actual,
            "delta": actual - expected,
        })

stats = get_json("/api/logs/stats", {
    "providers": "openai",
    "models": "gpt-4o-mini",
    "status": "success",
    "virtual_key_ids": virtual_key_id,
    "start_time": start_time,
})
stats_total = float(stats.get("total_cost", 0))

quota = None
for _ in range(60):
    req = urllib.request.Request(base + "/api/governance/virtual-keys/quota", headers={"x-bf-vk": virtual_key_value})
    with urllib.request.urlopen(req, timeout=10) as resp:
        quota = json.loads(resp.read().decode("utf-8"))
    budget_total = sum(float(b.get("current_usage") or 0) for b in quota.get("budgets", []))
    if math.fabs(budget_total - expected_total) <= 1e-12:
        break
    time.sleep(1)

budget_current_usage_total = sum(float(b.get("current_usage") or 0) for b in quota.get("budgets", []))
quota_model_total = 0.0
for budget in quota.get("budgets", []):
    for model_usage in budget.get("per_model_usage", []):
        if model_usage.get("model") == "gpt-4o-mini" and model_usage.get("provider") == "openai":
            quota_model_total += float(model_usage.get("total_cost") or 0)

summary = {
    "expected_count": expected_count,
    "log_count": len(logs),
    "virtual_key_id": virtual_key_id,
    "expected_total": expected_total,
    "actual_total_from_logs": actual_total,
    "actual_total_from_stats": stats_total,
    "budget_current_usage_total": budget_current_usage_total,
    "quota_model_total": quota_model_total,
    "log_delta": actual_total - expected_total,
    "stats_delta": stats_total - expected_total,
    "budget_delta": budget_current_usage_total - expected_total,
    "quota_model_delta": quota_model_total - expected_total,
    "mismatches": mismatches[:10],
}
results_file.parent.mkdir(parents=True, exist_ok=True)
results_file.write_text(json.dumps(summary, indent=2, sort_keys=True))

if mismatches:
    raise SystemExit(f"per-log cost mismatches: {json.dumps(summary, indent=2)}")
if math.fabs(actual_total - expected_total) > 1e-12:
    raise SystemExit(f"log total mismatch: {json.dumps(summary, indent=2)}")
if math.fabs(stats_total - expected_total) > 1e-12:
    raise SystemExit(f"stats total mismatch: {json.dumps(summary, indent=2)}")
if math.fabs(budget_current_usage_total - expected_total) > 1e-12:
    raise SystemExit(f"budget usage mismatch: {json.dumps(summary, indent=2)}")
if math.fabs(quota_model_total - expected_total) > 1e-12:
    raise SystemExit(f"quota model usage mismatch: {json.dumps(summary, indent=2)}")
print(json.dumps(summary, indent=2, sort_keys=True))
PY
}

main() {
  require_cmd curl
  require_cmd docker
  require_cmd git
  require_cmd go
  require_cmd python3
  ensure_docker_compose
  mkdir -p "${WORK_DIR}"
  start_postgres
  build_binaries
  write_config
  start_services
  create_virtual_key
  create_pricing_override
  run_hitter
  validate_costs
  log "cost accuracy test passed"
}

main "$@"
