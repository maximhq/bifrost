#!/bin/bash

# Load Test Script for Bifrost
# Runs a load test against bifrost-http with a mocker provider
# Usage: ./load-test.sh
#
# This script:
# 1. Builds bifrost-http and mocker locally
# 2. Creates a config.json with mocker provider (OpenAI-style)
# 3. Starts mocker with 0ms latency and bifrost-http
# 4. Runs calibration (Vegeta -> Mocker direct) for non-streaming and streaming
# 5. Runs overhead tests (Vegeta -> Bifrost -> Mocker) for non-streaming and streaming
# 6. Subtracts calibration from test to isolate Bifrost proxy overhead
#    (includes local network hop, JSON parsing/unparsing, plugins, and mocker jitter)
# 7. Restarts mocker with 10s latency for sustained concurrency stress tests
# 8. Asserts overhead < tiered thresholds (per percentile) and stress tests have 100% success rate

set -Ee

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

# Setup Go workspace for CI (go.work is gitignored, must be regenerated) so the
# build below resolves local core/framework/plugins instead of the published
# versions pinned in transports/go.mod. Run with repo root as CWD because
# setup-go-workspace.sh's `go work use ./core` paths are repo-root-relative; the
# go.work file it writes at ${REPO_ROOT} is then auto-discovered by `go build`.
( cd "${REPO_ROOT}" && source "${SCRIPT_DIR}/setup-go-workspace.sh" )

BIFROST_HTTP_DIR="${REPO_ROOT}/transports/bifrost-http"
TRANSPORTS_DIR="${REPO_ROOT}/transports"
WORK_DIR="${SCRIPT_DIR}"
BENCHMARK_DIR="${BENCHMARK_DIR:-${REPO_ROOT}/../bifrost-benchmarking}"
MOCKER_DIR="${BENCHMARK_DIR}/mocker"

BIFROST_PORT="${BIFROST_PORT:-8080}"
MOCKER_PORT="${MOCKER_PORT:-8000}"
BIFROST_LOG_LEVEL="${BIFROST_LOG_LEVEL:-warn}"
RATE="${RATE:-1000}"
STREAMING_RATE="${STREAMING_RATE:-${RATE}}"
MAX_WORKERS="${MAX_WORKERS:-12000}"
OVERHEAD_DURATION="${OVERHEAD_DURATION:-30}"                       # overhead measurement duration (seconds)
STRESS_DURATION="${STRESS_DURATION:-30}"                           # stress test duration per mode (seconds)
OVERHEAD_MOCKER_LATENCY_MS="${OVERHEAD_MOCKER_LATENCY_MS:-1000}"   # 1 second latency for overhead measurement
STRESS_MOCKER_LATENCY_MS="${STRESS_MOCKER_LATENCY_MS:-1000}"       # 1 second latency for stress test
# Tiered overhead thresholds (µs) — these cover the full proxy cost:
# local network hop, JSON parsing/unparsing, plugins, and mocker jitter.
# At RATE RPS × latency, concurrency is calculated per mode.
MAX_OVERHEAD_MEAN_US="${MAX_OVERHEAD_MEAN_US:-5000}"    # mean overhead threshold (5ms)
MAX_OVERHEAD_P50_US="${MAX_OVERHEAD_P50_US:-5000}"      # p50 overhead threshold (5ms)
MAX_OVERHEAD_P90_US="${MAX_OVERHEAD_P90_US:-10000}"     # p90 overhead threshold (10ms)
MAX_OVERHEAD_P95_US="${MAX_OVERHEAD_P95_US:-20000}"     # p95 overhead threshold (20ms)
MAX_OVERHEAD_P99_US="${MAX_OVERHEAD_P99_US:-100000}"    # p99 overhead threshold (100ms)

# Results storage for summary table
RESULTS_FILE="${WORK_DIR}/load-test-results.md"
RESULTS_JSON="${WORK_DIR}/load-test-results.json"
RESULTS_FILE_INITIALIZED=0

# Process stats monitoring
STATS_PID=""
STATS_FILE="${WORK_DIR}/bifrost-stats.csv"

# Overhead-phase process stats (saved before bifrost restart)
OVERHEAD_STATS_CPU_AVG=""
OVERHEAD_STATS_CPU_PEAK=""
OVERHEAD_STATS_RSS_AVG=""
OVERHEAD_STATS_RSS_PEAK=""

# Calibration results per bucket (Vegeta -> Mocker direct)
CAL_MIN_NS=0
CAL_MEAN_NS=0
CAL_50_NS=0
CAL_90_NS=0
CAL_95_NS=0
CAL_99_NS=0
CAL_MAX_NS=0

# Mode currently being measured.
CURRENT_MODE_LABEL="Non-streaming"
CURRENT_MODE_KEY="non_streaming"
CURRENT_STREAM=false
CURRENT_PHASE="startup"
FAILURE_CONTEXT_PRINTED=0

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() {
  echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
  echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warn() {
  echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
  echo -e "${RED}[ERROR]${NC} $1"
}

print_result_locations() {
  if [ -f "${RESULTS_FILE}" ]; then
    log_info "Partial Markdown results: ${RESULTS_FILE}"
  fi
  if [ -f "${RESULTS_JSON}" ]; then
    log_info "Partial JSON results: ${RESULTS_JSON}"
  fi
}

print_failure_context() {
  local exit_code="$1"
  local line_no="$2"
  local command="$3"

  FAILURE_CONTEXT_PRINTED=1
  echo ""
  log_error "Load test failed"
  log_error "  Exit code: ${exit_code}"
  log_error "  Phase: ${CURRENT_PHASE}"
  log_error "  Mode: ${CURRENT_MODE_LABEL}"
  log_error "  Line: ${line_no}"
  log_error "  Command: ${command}"
  print_result_locations

  if [ -f "${WORK_DIR}/bifrost.log" ]; then
    echo ""
    log_error "Last 80 lines from bifrost.log:"
    tail -n 80 "${WORK_DIR}/bifrost.log" || true
  fi
  if [ -f "${WORK_DIR}/mocker.log" ]; then
    echo ""
    log_error "Last 40 lines from mocker.log:"
    tail -n 40 "${WORK_DIR}/mocker.log" || true
  fi
}

# Cleanup function to kill background processes
cleanup() {
  local exit_code=$?
  set +e
  if [ "${exit_code}" -ne 0 ] && [ "${FAILURE_CONTEXT_PRINTED}" -eq 0 ]; then
    print_failure_context "${exit_code}" "unknown" "script exited before completion"
  fi

  log_info "Cleaning up..."
  if [ -n "$STATS_PID" ] && kill -0 "$STATS_PID" 2>/dev/null; then
    kill "$STATS_PID" 2>/dev/null || true
    wait "$STATS_PID" 2>/dev/null || true
  fi
  if [ -n "$BIFROST_PID" ] && kill -0 "$BIFROST_PID" 2>/dev/null; then
    kill "$BIFROST_PID" 2>/dev/null || true
    wait "$BIFROST_PID" 2>/dev/null || true
  fi
  if [ -n "$MOCKER_PID" ] && kill -0 "$MOCKER_PID" 2>/dev/null; then
    kill "$MOCKER_PID" 2>/dev/null || true
    wait "$MOCKER_PID" 2>/dev/null || true
  fi
  # Clean up temporary files (keep results files for artifact upload)
  rm -f "${WORK_DIR}/config.json" "${WORK_DIR}/logs.db" "${WORK_DIR}"/attack-*.bin "${WORK_DIR}"/calibration-*.bin "${WORK_DIR}"/stress-*.bin "${WORK_DIR}/bifrost.log" "${WORK_DIR}"/vegeta-target*.json "${WORK_DIR}/vegeta-report.json" "${WORK_DIR}/bifrost-stats.csv" 2>/dev/null || true
  log_info "Cleanup complete"
  exit "${exit_code}"
}

on_error() {
  local exit_code="$1"
  local line_no="$2"
  local command="$3"
  trap - ERR
  print_failure_context "${exit_code}" "${line_no}" "${command}"
}

trap 'on_error "$?" "$LINENO" "$BASH_COMMAND"' ERR
trap cleanup EXIT

# Check for required tools
check_dependencies() {
  log_info "Checking dependencies..."

  if ! command -v go &> /dev/null; then
    log_error "Go is not installed. Please install Go 1.24.3 or later."
    exit 1
  fi

  if ! command -v git &> /dev/null; then
    log_error "Git is not installed. Please install Git."
    exit 1
  fi

  if ! command -v jq &> /dev/null; then
    log_error "jq is not installed. Please install jq."
    exit 1
  fi

  if ! command -v perl &> /dev/null; then
    log_error "perl is not installed. It is required to patch the benchmark mocker checkout."
    exit 1
  fi

  log_success "All dependencies found"
}

# Kill any process listening on a specific port (not processes with connections to it)
kill_port() {
  local port=$1
  local pids
  pids=$(lsof -ti "TCP:${port}" -sTCP:LISTEN 2>/dev/null || true)
  if [ -n "$pids" ]; then
    log_warn "Killing existing process(es) listening on port ${port}: ${pids}"
    echo "$pids" | xargs kill -9 2>/dev/null || true
    sleep 1
  fi
}

# Kill processes on required ports before starting
cleanup_ports() {
  log_info "Checking for processes on required ports..."
  kill_port ${MOCKER_PORT}
  kill_port ${BIFROST_PORT}
}

# Install Vegeta if not present
install_vegeta() {
  if ! command -v vegeta &> /dev/null; then
    log_info "Installing Vegeta load testing tool..."
    go install github.com/tsenart/vegeta/v12@latest
    export PATH="$PATH:$(go env GOPATH)/bin"
    if ! command -v vegeta &> /dev/null; then
      log_error "Failed to install Vegeta"
      exit 1
    fi
    log_success "Vegeta installed"
  else
    log_success "Vegeta already installed"
  fi
}

# Build bifrost-http if binary doesn't exist
build_bifrost_http() {
  if [ -f "${REPO_ROOT}/tmp/bifrost-http" ]; then
    log_success "bifrost-http binary already exists at ${REPO_ROOT}/tmp/bifrost-http"
    return 0
  fi

  log_info "Building bifrost-http..."
  cd "${BIFROST_HTTP_DIR}"

  # Ensure ui directory exists for //go:embed all:ui (load test does not need the real UI assets)
  mkdir -p "${BIFROST_HTTP_DIR}/ui"
  if [ ! -f "${BIFROST_HTTP_DIR}/ui/.gitkeep" ]; then
    echo "placeholder" > "${BIFROST_HTTP_DIR}/ui/.gitkeep"
  fi

  if go build -o ${REPO_ROOT}/tmp/bifrost-http .; then
    log_success "bifrost-http built successfully"
  else
    log_error "Failed to build bifrost-http"
    exit 1
  fi

  cd "${WORK_DIR}"
}

# Clone and setup mocker from bifrost-benchmarking
setup_mocker() {
  if [ -d "${BENCHMARK_DIR}" ]; then
    log_info "Updating bifrost-benchmarking repository..."
    cd "${BENCHMARK_DIR}"
    git pull --quiet || true
    cd "${WORK_DIR}"
  else
    log_info "Cloning bifrost-benchmarking repository..."
    mkdir -p "$(dirname "${BENCHMARK_DIR}")"
    git clone --depth 1 https://github.com/maximhq/bifrost-benchmarking.git "${BENCHMARK_DIR}"
    cd "${WORK_DIR}"
  fi

  log_success "Mocker setup complete"
}

patch_mocker_for_load_test() {
  local mocker_main="${MOCKER_DIR}/main.go"
  if [ ! -f "${mocker_main}" ]; then
    log_error "Mocker main.go not found at ${mocker_main}"
    exit 1
  fi

  log_info "Patching mocker for high-RPS streaming load tests..."

  LC_ALL=C perl -0pi -e 's/\n\tctx\.Response\.Header\.Set\("Connection", "close"\)//g; s/\n\tctx\.Response\.Header\.Set\("Transfer-Encoding", "chunked"\)//g; s/\n\tctx\.SetConnectionClose\(\)//g' "${mocker_main}"

  LC_ALL=C perl -0pi -e 's/\n\tif provider != "" \{\n\t\tlog\.Printf\("\[chat\/completions\] provider=%s model=%s stream=%v", provider, model, stream\)\n\t\} else \{\n\t\tlog\.Printf\("\[chat\/completions\] model=%s stream=%v", model, stream\)\n\t\}\n/\n\tif logRaw {\n\t\tif provider != "" {\n\t\t\tlog.Printf("[chat\/completions] provider=%s model=%s stream=%v", provider, model, stream)\n\t\t} else {\n\t\t\tlog.Printf("[chat\/completions] model=%s stream=%v", model, stream)\n\t\t}\n\t}\n/s' "${mocker_main}"

  if grep -q 'ctx.SetConnectionClose()' "${mocker_main}" || grep -q 'Header.Set("Connection", "close")' "${mocker_main}"; then
    log_error "Failed to remove forced SSE connection close from mocker"
    exit 1
  fi

  rm -f "${REPO_ROOT}/tmp/mocker"
  log_success "Mocker patched for high-RPS streaming"
}

# Build mocker binary (avoids go run overhead)
build_mocker() {
  if [ -f "${REPO_ROOT}/tmp/mocker" ]; then
    log_success "mocker binary already exists at ${REPO_ROOT}/tmp/mocker"
    return 0
  fi

  log_info "Building mocker..."
  cd "${MOCKER_DIR}"

  if go build -o "${REPO_ROOT}/tmp/mocker" .; then
    log_success "mocker built successfully"
  else
    log_error "Failed to build mocker"
    exit 1
  fi

  cd "${WORK_DIR}"
}

initialize_results() {
  rm -f "${RESULTS_FILE}" "${RESULTS_JSON}"
  RESULTS_FILE_INITIALIZED=0
}

# Create config.json for bifrost with mocker provider
create_config() {
  log_info "Creating config.json..."

  cat > "${WORK_DIR}/config.json" << 'EOF'
{
  "$schema": "https://www.getbifrost.ai/schema",
  "client": {
    "enable_logging": false,
    "disable_content_logging": true,
    "initial_pool_size": 5000,
    "drop_excess_requests": false,
    "allow_direct_keys": false
  },
  "config_store": {
    "enabled": false
  },
  "logs_store": {
    "enabled": false
  },
  "plugins": [],
  "providers": {
    "anthropic": {
      "keys": [{ "name": "mocker-anthropic-key", "value": "Bearer mocker-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "azure": {
      "keys": [
        {
          "name": "mocker-azure-key",
          "value": "mocker-key",
          "weight": 1,
          "models": ["*"],
          "azure_key_config": { "endpoint": "http://127.0.0.1:8000" }
        }
      ],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "bedrock": {
      "keys": [
        {
          "name": "mocker-bedrock-key",
          "weight": 1,
          "models": ["*"],
          "bedrock_key_config": {
            "access_key": "mocker-access-key",
            "secret_key": "mocker-secret-key",
            "region": "us-east-1"
          }
        }
      ],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "cerebras": {
      "keys": [{ "name": "mocker-cerebras-key", "value": "Bearer mocker-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "cohere": {
      "keys": [{ "name": "mocker-cohere-key", "value": "Bearer mocker-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "elevenlabs": {
      "keys": [{ "name": "mocker-elevenlabs-key", "value": "Bearer mocker-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "fireworks": {
      "keys": [{ "name": "mocker-fireworks-key", "value": "Bearer mocker-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "gemini": {
      "keys": [{ "name": "mocker-gemini-key", "value": "Bearer mocker-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "groq": {
      "keys": [{ "name": "mocker-groq-key", "value": "Bearer mocker-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "huggingface": {
      "keys": [{ "name": "mocker-huggingface-key", "value": "Bearer mocker-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "mistral": {
      "keys": [{ "name": "mocker-mistral-key", "value": "Bearer mocker-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "nebius": {
      "keys": [{ "name": "mocker-nebius-key", "value": "Bearer mocker-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "ollama": {
      "keys": [
        {
          "name": "mocker-ollama-key",
          "value": "Bearer mocker-key",
          "weight": 1,
          "models": ["*"],
          "ollama_key_config": { "url": "http://127.0.0.1:8000" }
        }
      ],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "opencode-go": {
      "keys": [{ "name": "mocker-opencode-go-key", "value": "Bearer mocker-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "opencode-zen": {
      "keys": [{ "name": "mocker-opencode-zen-key", "value": "Bearer mocker-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
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
        "base_url": "http://127.0.0.1:8000",
        "default_request_timeout_in_seconds": 30
      },
      "concurrency_and_buffer_size": {
        "concurrency": 5000,
        "buffer_size": 10000
      }
    },
    "openrouter": {
      "keys": [{ "name": "mocker-openrouter-key", "value": "Bearer mocker-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "parasail": {
      "keys": [{ "name": "mocker-parasail-key", "value": "Bearer mocker-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "perplexity": {
      "keys": [{ "name": "mocker-perplexity-key", "value": "Bearer mocker-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "replicate": {
      "keys": [
        {
          "name": "mocker-replicate-key",
          "value": "Bearer mocker-key",
          "weight": 1,
          "models": ["*"],
          "replicate_key_config": { "use_deployments_endpoint": false }
        }
      ],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "runway": {
      "keys": [{ "name": "mocker-runway-key", "value": "Bearer mocker-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "sgl": {
      "keys": [
        {
          "name": "mocker-sgl-key",
          "value": "Bearer mocker-key",
          "weight": 1,
          "models": ["*"],
          "sgl_key_config": { "url": "http://127.0.0.1:8000" }
        }
      ],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "vertex": {
      "keys": [
        {
          "name": "mocker-vertex-key",
          "weight": 1,
          "models": ["*"],
          "vertex_key_config": {
            "project_id": "mocker-project",
            "region": "us-central1"
          }
        }
      ],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "vllm": {
      "keys": [
        {
          "name": "mocker-vllm-key",
          "value": "Bearer mocker-key",
          "weight": 1,
          "models": ["*"],
          "vllm_key_config": {
            "url": "http://127.0.0.1:8000",
            "model_name": "gpt-4o-mini"
          }
        }
      ],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    },
    "xai": {
      "keys": [{ "name": "mocker-xai-key", "value": "Bearer mocker-key", "weight": 1, "models": ["*"] }],
      "network_config": { "base_url": "http://127.0.0.1:8000", "default_request_timeout_in_seconds": 30 },
      "concurrency_and_buffer_size": { "concurrency": 1, "buffer_size": 1 }
    }
  }
}
EOF

  log_success "config.json created"
}

# Start mocker with specified latency
# Arguments: $1 = latency in ms
start_mocker() {
  local latency_ms=${1:-0}
  CURRENT_PHASE="start mocker (${latency_ms}ms)"
  log_info "Starting mocker server on port ${MOCKER_PORT} with ${latency_ms}ms latency..."

  "${REPO_ROOT}/tmp/mocker" -port ${MOCKER_PORT} -host 0.0.0.0 -latency ${latency_ms} > "${WORK_DIR}/mocker.log" 2>&1 &
  MOCKER_PID=$!

  # Wait for mocker to be ready
  local max_attempts=30
  local attempt=0
  while ! curl -s "http://127.0.0.1:${MOCKER_PORT}/v1/chat/completions" -X POST \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer mocker-key" \
    -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"test"}]}' > /dev/null 2>&1; do
    sleep 1
    attempt=$((attempt + 1))
    if [ $attempt -ge $max_attempts ]; then
      log_error "Mocker failed to start within ${max_attempts} seconds"
      exit 1
    fi
  done

  log_success "Mocker server started (PID: ${MOCKER_PID})"
}

# Stop mocker
stop_mocker() {
  if [ -n "$MOCKER_PID" ] && kill -0 "$MOCKER_PID" 2>/dev/null; then
    log_info "Stopping mocker (PID: ${MOCKER_PID})..."
    kill "$MOCKER_PID" 2>/dev/null || true
    wait "$MOCKER_PID" 2>/dev/null || true
    MOCKER_PID=""
    sleep 1
  fi
}

# Stop bifrost-http server
stop_bifrost() {
  if [ -n "$BIFROST_PID" ] && kill -0 "$BIFROST_PID" 2>/dev/null; then
    log_info "Stopping bifrost (PID: ${BIFROST_PID})..."
    kill "$BIFROST_PID" 2>/dev/null || true
    wait "$BIFROST_PID" 2>/dev/null || true
    BIFROST_PID=""
    sleep 1
  fi
}

# Start background process stats collection for bifrost
# Samples CPU% and RSS every second, writes to CSV
start_stats_monitor() {
  if [ -z "$BIFROST_PID" ] || ! kill -0 "$BIFROST_PID" 2>/dev/null; then
    log_warn "Cannot start stats monitor: bifrost not running"
    return
  fi

  echo "timestamp,cpu_pct,rss_mb" > "${STATS_FILE}"

  (
    while kill -0 "$BIFROST_PID" 2>/dev/null; do
      # ps -o %cpu= -o rss= works on both macOS and Linux
      stats=$(ps -p "$BIFROST_PID" -o %cpu=,rss= 2>/dev/null)
      if [ -n "$stats" ]; then
        cpu=$(echo "$stats" | awk '{print $1}')
        rss_kb=$(echo "$stats" | awk '{print $2}')
        rss_mb=$(echo "scale=1; ${rss_kb} / 1024" | bc)
        echo "$(date +%s),${cpu},${rss_mb}" >> "${STATS_FILE}"
      fi
      sleep 1
    done
  ) &
  STATS_PID=$!
  log_info "Stats monitor started (PID: ${STATS_PID})"
}

# Stop stats monitor and print summary
stop_stats_monitor() {
  if [ -n "$STATS_PID" ] && kill -0 "$STATS_PID" 2>/dev/null; then
    kill "$STATS_PID" 2>/dev/null || true
    wait "$STATS_PID" 2>/dev/null || true
    STATS_PID=""
  fi

  if [ ! -f "${STATS_FILE}" ] || [ $(wc -l < "${STATS_FILE}") -le 1 ]; then
    log_warn "No process stats collected"
    return
  fi

  # Compute peak and average CPU/RSS from CSV (skip header)
  if command -v awk &> /dev/null; then
    local stats_summary=$(awk -F',' 'NR>1 {
      cpu_sum+=$2; rss_sum+=$3; n++;
      if($2>cpu_max) cpu_max=$2;
      if($3>rss_max) rss_max=$3;
    } END {
      if(n>0) printf "%.1f,%.1f,%.1f,%.1f,%d", cpu_sum/n, cpu_max, rss_sum/n, rss_max, n
    }' "${STATS_FILE}")

    STATS_CPU_AVG=$(echo "$stats_summary" | cut -d',' -f1)
    STATS_CPU_PEAK=$(echo "$stats_summary" | cut -d',' -f2)
    STATS_RSS_AVG=$(echo "$stats_summary" | cut -d',' -f3)
    STATS_RSS_PEAK=$(echo "$stats_summary" | cut -d',' -f4)
    local samples=$(echo "$stats_summary" | cut -d',' -f5)

    echo ""
    log_success "Bifrost process stats (single instance, ${samples} samples):"
    log_info "  CPU:  avg=${STATS_CPU_AVG}%, peak=${STATS_CPU_PEAK}%"
    log_info "  RSS:  avg=${STATS_RSS_AVG}MB, peak=${STATS_RSS_PEAK}MB"
  fi
}

# Start bifrost-http server
start_bifrost() {
  CURRENT_PHASE="start bifrost"
  log_info "Starting bifrost-http on port ${BIFROST_PORT} with log level ${BIFROST_LOG_LEVEL}..."

  cd "${WORK_DIR}"
  local bifrost_log="${WORK_DIR}/bifrost.log"
  "${REPO_ROOT}/tmp/bifrost-http" -app-dir "${WORK_DIR}" -port "${BIFROST_PORT}" -host "0.0.0.0" -log-level "${BIFROST_LOG_LEVEL}" > "${bifrost_log}" 2>&1 &
  BIFROST_PID=$!

  # Wait for bifrost to be ready. /health is skipped by the access log middleware,
  # so this works even when Bifrost runs at warn level to suppress per-request logs.
  local max_attempts=60
  local attempt=0
  while ! curl -fsS "http://127.0.0.1:${BIFROST_PORT}/health" > /dev/null 2>&1; do
    sleep 1
    attempt=$((attempt + 1))
    if [ $attempt -ge $max_attempts ]; then
      log_error "Bifrost failed to start within ${max_attempts} seconds"
      log_error "Bifrost log output:"
      cat "${bifrost_log}" 2>/dev/null || true
      exit 1
    fi
    # Check if process is still running
    if ! kill -0 "$BIFROST_PID" 2>/dev/null; then
      log_error "Bifrost process died unexpectedly"
      log_error "Bifrost log output:"
      cat "${bifrost_log}" 2>/dev/null || true
      exit 1
    fi
  done

  log_success "Bifrost-http started (PID: ${BIFROST_PID})"
}

# Extract latencies from a vegeta binary results file
# Arguments: $1 = path to .bin file
# Sets: EXTRACTED_MIN_NS, EXTRACTED_MEAN_NS, EXTRACTED_50_NS, etc.
extract_latencies() {
  local bin_file=$1
  local json_report_file="${WORK_DIR}/vegeta-report.json"
  vegeta report -type=json < "${bin_file}" > "${json_report_file}"

  EXTRACTED_MIN_NS=$(jq '.latencies.min // 0' "${json_report_file}")
  EXTRACTED_MEAN_NS=$(jq '.latencies.mean // 0' "${json_report_file}")
  EXTRACTED_50_NS=$(jq '.latencies["50th"] // 0' "${json_report_file}")
  EXTRACTED_90_NS=$(jq '.latencies["90th"] // 0' "${json_report_file}")
  EXTRACTED_95_NS=$(jq '.latencies["95th"] // 0' "${json_report_file}")
  EXTRACTED_99_NS=$(jq '.latencies["99th"] // 0' "${json_report_file}")
  EXTRACTED_MAX_NS=$(jq '.latencies.max // 0' "${json_report_file}")
  EXTRACTED_SUCCESS=$(jq '.success // 0' "${json_report_file}")
  EXTRACTED_RATE=$(jq '.rate // 0' "${json_report_file}")
  EXTRACTED_THROUGHPUT=$(jq '.throughput // 0' "${json_report_file}")

  rm -f "${json_report_file}"
}

set_test_mode() {
  CURRENT_MODE_LABEL="$1"
  CURRENT_MODE_KEY="$2"
  CURRENT_STREAM="$3"
}

current_rate() {
  if [ "${CURRENT_STREAM}" = "true" ]; then
    echo "${STREAMING_RATE}"
  else
    echo "${RATE}"
  fi
}

current_concurrency() {
  local rate="$1"
  local latency_ms="$2"
  local concurrency=$((rate * latency_ms / 1000))
  if [ "${concurrency}" -lt 1 ]; then
    concurrency=1
  fi
  echo "${concurrency}"
}

chat_payload() {
  local model="$1"
  local stream="$2"
  if [ "${stream}" = "true" ]; then
    printf '{"model":"%s","messages":[{"role":"user","content":"Hello, how are you?"}],"stream":true}' "${model}"
  else
    printf '{"model":"%s","messages":[{"role":"user","content":"Hello, how are you?"}]}' "${model}"
  fi
}

base64_one_line() {
  base64 | tr -d '\n'
}

append_overhead_json() {
  local mode_key="$1"
  local configured_rate="$2"
  local concurrent="$3"
  local actual_rps="$4"
  local success_pct="$5"
  local us_mean="$6"
  local us_50="$7"
  local us_90="$8"
  local us_95="$9"
  local us_99="${10}"

  local tmp_json
  tmp_json=$(mktemp)
  if [ ! -f "${RESULTS_JSON}" ]; then
    echo '{}' > "${RESULTS_JSON}"
  fi

  jq \
    --arg mode_key "${mode_key}" \
    --arg timestamp "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
    --arg configured_rate "${configured_rate}" \
    --arg actual_rate "${actual_rps}" \
    --arg duration "${OVERHEAD_DURATION}" \
    --arg concurrent "${concurrent}" \
    --arg success_rate "${success_pct}" \
    --arg us_mean "${us_mean}" \
    --arg us_50 "${us_50}" \
    --arg us_90 "${us_90}" \
    --arg us_95 "${us_95}" \
    --arg us_99 "${us_99}" \
    '.overhead[$mode_key] = {
      configured_rate: ($configured_rate | tonumber),
      actual_rate: ($actual_rate | tonumber),
      duration: ($duration | tonumber),
      concurrent: ($concurrent | tonumber),
      success_rate: ($success_rate | tonumber),
      latency_us: {
        mean: ($us_mean | tonumber),
        p50: ($us_50 | tonumber),
        p90: ($us_90 | tonumber),
        p95: ($us_95 | tonumber),
        p99: ($us_99 | tonumber)
      }
    } | .timestamp = (.timestamp // $timestamp)' \
    "${RESULTS_JSON}" > "${tmp_json}"
  mv "${tmp_json}" "${RESULTS_JSON}"
}

append_stress_json() {
  local mode_key="$1"
  local label="$2"
  local configured_rate="$3"
  local success_pct="$4"

  local tmp_json
  tmp_json=$(mktemp)
  if [ ! -f "${RESULTS_JSON}" ]; then
    echo '{}' > "${RESULTS_JSON}"
  fi

  jq \
    --arg mode_key "${mode_key}" \
    --arg label "${label}" \
    --arg timestamp "$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
    --arg rate "${configured_rate}" \
    --arg duration "${STRESS_DURATION}" \
    --arg mocker_latency_ms "${STRESS_MOCKER_LATENCY_MS}" \
    --arg success_rate "${success_pct}" \
    '.stress[$mode_key][$label] = {
      rate: ($rate | tonumber),
      duration: ($duration | tonumber),
      mocker_latency_ms: ($mocker_latency_ms | tonumber),
      success_rate: ($success_rate | tonumber)
    } | .timestamp = (.timestamp // $timestamp)' \
    "${RESULTS_JSON}" > "${tmp_json}"
  mv "${tmp_json}" "${RESULTS_JSON}"
}

# ============================================================
# Phase 1: Overhead measurement (mocker at ${OVERHEAD_MOCKER_LATENCY_MS}ms)
# ============================================================

# Calibration: Vegeta -> Mocker direct (with latency)
# Measures: Vegeta HTTP client + localhost network round-trip + mocker response generation
run_calibration() {
  CURRENT_PHASE="calibration (${CURRENT_MODE_LABEL})"
  local test_rate
  local test_concurrency
  test_rate=$(current_rate)
  test_concurrency=$(current_concurrency "${test_rate}" "${OVERHEAD_MOCKER_LATENCY_MS}")

  echo ""
  echo "╔═══════════════════════════════════════════════════════════╗"
  echo "║    Calibration (${CURRENT_MODE_LABEL}): Vegeta -> Mocker (${OVERHEAD_MOCKER_LATENCY_MS}ms)    ║"
  echo "╚═══════════════════════════════════════════════════════════╝"
  echo ""
  log_info "Measuring ${CURRENT_MODE_LABEL} Vegeta + network baseline (mocker at ${OVERHEAD_MOCKER_LATENCY_MS}ms latency)"
  log_info "Duration: ${OVERHEAD_DURATION}s at ${test_rate} RPS, ~${test_concurrency} concurrent"
  echo ""

  local target_file="${WORK_DIR}/vegeta-target-calibration-${CURRENT_MODE_KEY}.json"
  local payload
  local encoded_payload
  payload=$(chat_payload "gpt-4o-mini" "${CURRENT_STREAM}")
  encoded_payload=$(printf "%s" "${payload}" | base64_one_line)

  cat > "${target_file}" << EOF
{"method": "POST", "url": "http://127.0.0.1:${MOCKER_PORT}/v1/chat/completions", "header": {"Content-Type": ["application/json"], "Authorization": ["Bearer mocker-key"]}, "body": "${encoded_payload}"}
EOF

  vegeta attack \
    -format=json \
    -targets="${target_file}" \
    -rate="${test_rate}" \
    -duration="${OVERHEAD_DURATION}s" \
    -timeout="$((OVERHEAD_MOCKER_LATENCY_MS / 1000 + 15))s" \
    -workers="${test_concurrency}" \
    -max-workers="${MAX_WORKERS}" > "${WORK_DIR}/calibration-${CURRENT_MODE_KEY}.bin"

  echo ""
  log_info "Calibration complete. Results:"
  vegeta report < "${WORK_DIR}/calibration-${CURRENT_MODE_KEY}.bin"

  extract_latencies "${WORK_DIR}/calibration-${CURRENT_MODE_KEY}.bin"

  log_info "Actual RPS: $(printf "%.0f" $EXTRACTED_RATE) (configured: ${test_rate})"

  CAL_MIN_NS=$EXTRACTED_MIN_NS
  CAL_MEAN_NS=$EXTRACTED_MEAN_NS
  CAL_50_NS=$EXTRACTED_50_NS
  CAL_90_NS=$EXTRACTED_90_NS
  CAL_95_NS=$EXTRACTED_95_NS
  CAL_99_NS=$EXTRACTED_99_NS
  CAL_MAX_NS=$EXTRACTED_MAX_NS

  echo ""
  log_success "Calibration baseline (per bucket):"
  log_info "  Min:  $(echo "scale=2; $CAL_MIN_NS / 1000" | bc)µs"
  log_info "  Mean: $(echo "scale=2; $CAL_MEAN_NS / 1000" | bc)µs"
  log_info "  P50:  $(echo "scale=2; $CAL_50_NS / 1000" | bc)µs"
  log_info "  P90:  $(echo "scale=2; $CAL_90_NS / 1000" | bc)µs"
  log_info "  P95:  $(echo "scale=2; $CAL_95_NS / 1000" | bc)µs"
  log_info "  P99:  $(echo "scale=2; $CAL_99_NS / 1000" | bc)µs"
  log_info "  Max:  $(echo "scale=2; $CAL_MAX_NS / 1000" | bc)µs"
}

# Overhead test: Vegeta -> Bifrost -> Mocker (with latency)
# Same duration/rate as calibration so percentile distributions are comparable
run_overhead_test() {
  CURRENT_PHASE="overhead (${CURRENT_MODE_LABEL})"
  local test_rate
  local test_concurrency
  test_rate=$(current_rate)
  test_concurrency=$(current_concurrency "${test_rate}" "${OVERHEAD_MOCKER_LATENCY_MS}")

  echo ""
  echo "╔═══════════════════════════════════════════════════════════╗"
  echo "║  Overhead Test (${CURRENT_MODE_LABEL}): Vegeta -> Bifrost -> Mocker     ║"
  echo "╚═══════════════════════════════════════════════════════════╝"
  echo ""
  log_info "Measuring ${CURRENT_MODE_LABEL} Bifrost overhead (single instance, mocker at ${OVERHEAD_MOCKER_LATENCY_MS}ms latency)"
  log_info "Duration: ${OVERHEAD_DURATION}s at ${test_rate} RPS, ~${test_concurrency} concurrent requests through Bifrost"
  log_info "Overhead consists of: vegetta overhead and mocker timeout jitter"
  echo ""

  local target_file="${WORK_DIR}/vegeta-target-${CURRENT_MODE_KEY}.json"
  local payload
  local encoded_payload
  payload=$(chat_payload "openai/gpt-4o-mini" "${CURRENT_STREAM}")
  encoded_payload=$(printf "%s" "${payload}" | base64_one_line)

  cat > "${target_file}" << EOF
{"method": "POST", "url": "http://127.0.0.1:${BIFROST_PORT}/v1/chat/completions", "header": {"Content-Type": ["application/json"]}, "body": "${encoded_payload}"}
EOF

  vegeta attack \
    -format=json \
    -targets="${target_file}" \
    -rate="${test_rate}" \
    -duration="${OVERHEAD_DURATION}s" \
    -timeout="$((OVERHEAD_MOCKER_LATENCY_MS / 1000 + 15))s" \
    -workers="${test_concurrency}" \
    -max-workers="${MAX_WORKERS}" > "${WORK_DIR}/attack-${CURRENT_MODE_KEY}.bin"

  echo ""
  log_info "Overhead test complete. Results:"
  vegeta report < "${WORK_DIR}/attack-${CURRENT_MODE_KEY}.bin"

  echo ""
  log_info "Latency histogram:"
  vegeta report -type=hist[0,100us,500us,1ms,5ms,10ms,50ms,100ms] < "${WORK_DIR}/attack-${CURRENT_MODE_KEY}.bin" || log_warn "Histogram generation failed"

  # Extract and compute overhead
  extract_latencies "${WORK_DIR}/attack-${CURRENT_MODE_KEY}.bin"

  log_info "  Raw latencies (ns): min=$EXTRACTED_MIN_NS, mean=$EXTRACTED_MEAN_NS, p50=$EXTRACTED_50_NS, p99=$EXTRACTED_99_NS, max=$EXTRACTED_MAX_NS"
  log_info "  Success rate: $EXTRACTED_SUCCESS"
  log_info "  Actual RPS: $(printf "%.0f" $EXTRACTED_RATE) (configured: ${test_rate})"

  if [ -z "$EXTRACTED_MIN_NS" ] || [ "$EXTRACTED_MIN_NS" = "0" ] || [ "$EXTRACTED_MIN_NS" = "null" ]; then
    log_error "Failed to extract latency values from vegeta report"
    exit 1
  fi

  # Subtract calibration per bucket: overhead = through_bifrost - direct_to_mocker
  local us_mean=$(printf "%.2f" $(echo "scale=4; ($EXTRACTED_MEAN_NS - $CAL_MEAN_NS) / 1000" | bc))
  local us_50=$(printf "%.2f" $(echo "scale=4; ($EXTRACTED_50_NS - $CAL_50_NS) / 1000" | bc))
  local us_90=$(printf "%.2f" $(echo "scale=4; ($EXTRACTED_90_NS - $CAL_90_NS) / 1000" | bc))
  local us_95=$(printf "%.2f" $(echo "scale=4; ($EXTRACTED_95_NS - $CAL_95_NS) / 1000" | bc))
  local us_99=$(printf "%.2f" $(echo "scale=4; ($EXTRACTED_99_NS - $CAL_99_NS) / 1000" | bc))

  local success_pct=$(printf "%.2f" $(echo "scale=4; $EXTRACTED_SUCCESS * 100" | bc))

  echo ""
  log_success "Bifrost overhead (calibration-subtracted buckets):"
  log_info "  Mean: ${us_mean}µs"
  log_info "  P50:  ${us_50}µs"
  log_info "  P90:  ${us_90}µs"
  log_info "  P95:  ${us_95}µs"
  log_info "  P99:  ${us_99}µs"

  local actual_rps=$(printf "%.0f" $EXTRACTED_RATE)

  # Write results
  if [ "$RESULTS_FILE_INITIALIZED" -eq 0 ]; then
    cat > "${RESULTS_FILE}" << EOF
# Bifrost Load Test Results (single instance)

## Bifrost Processing Overhead

| Metric | Actual RPS | Duration | Concurrent | Success Rate | Mean | P50 | P90 | P95 | P99 |
|--------|-----------|----------|------------|--------------|------|-----|-----|-----|-----|
EOF
    RESULTS_FILE_INITIALIZED=1
  fi
  echo "| Overhead (${CURRENT_MODE_LABEL}) | ${actual_rps} | ${OVERHEAD_DURATION}s | ~${test_concurrency} | ${success_pct}% | ${us_mean}µs | ${us_50}µs | ${us_90}µs | ${us_95}µs | ${us_99}µs |" >> "${RESULTS_FILE}"

  append_overhead_json "${CURRENT_MODE_KEY}" "${test_rate}" "${test_concurrency}" "${actual_rps}" "${success_pct}" "${us_mean}" "${us_50}" "${us_90}" "${us_95}" "${us_99}"

  # Check tiered thresholds (skip Min/Max — single-point extremes are too noisy)
  local failed=0
  local labels=("Mean" "P50" "P90" "P95" "P99")
  local real_values=($EXTRACTED_MEAN_NS $EXTRACTED_50_NS $EXTRACTED_90_NS $EXTRACTED_95_NS $EXTRACTED_99_NS)
  local cal_values=($CAL_MEAN_NS $CAL_50_NS $CAL_90_NS $CAL_95_NS $CAL_99_NS)
  local thresholds=($MAX_OVERHEAD_MEAN_US $MAX_OVERHEAD_P50_US $MAX_OVERHEAD_P90_US $MAX_OVERHEAD_P95_US $MAX_OVERHEAD_P99_US)
  local extras=()

  for i in "${!real_values[@]}"; do
    local overhead_us=$(( (real_values[i] - cal_values[i]) / 1000 ))
    if [ "$overhead_us" -gt "${thresholds[i]}" ]; then
      extras+=("${labels[i]}:${overhead_us}:${thresholds[i]}")
      failed=1
    fi
  done

  if [ "$failed" -eq 1 ]; then
    echo ""
    log_error "FAILED: Bifrost overhead exceeded tiered thresholds"
    log_error "Overhead  consists of: vegetta overhead and mocker timeout jitter. In real-world the P99 overhead will be approximately 100 microseconds."
    echo ""
    echo -e "${RED}| Bucket | Overhead (µs) | Threshold (µs) |${NC}"
    echo -e "${RED}|--------|---------------|----------------|${NC}"
    for entry in "${extras[@]}"; do
      IFS=: read -r bucket overhead threshold <<< "$entry"
      echo -e "${RED}| ${bucket} | ${overhead}µs | ${threshold}µs |${NC}"
    done
    echo ""
    stop_stats_monitor
    exit 1
  fi

  log_success "All overhead buckets within tiered thresholds (mean<${MAX_OVERHEAD_MEAN_US}µs, p50<${MAX_OVERHEAD_P50_US}µs, p90<${MAX_OVERHEAD_P90_US}µs, p95<${MAX_OVERHEAD_P95_US}µs, p99<${MAX_OVERHEAD_P99_US}µs)"
}

# ============================================================
# Phase 2: Stress test (mocker at 10s latency)
# ============================================================

# Arguments: $1 = label
run_stress_test() {
  local label="${1:-Stress}"
  CURRENT_PHASE="${label} (${CURRENT_MODE_LABEL})"
  local test_rate
  local test_concurrency
  test_rate=$(current_rate)
  test_concurrency=$(current_concurrency "${test_rate}" "${STRESS_MOCKER_LATENCY_MS}")
  local safe_label
  safe_label=$(echo "${label}" | tr '[:upper:]' '[:lower:]' | tr ' #' '--' | tr -cd '[:alnum:]_-')
  local bin_file="${WORK_DIR}/stress-${CURRENT_MODE_KEY}-${safe_label}.bin"

  echo ""
  echo "╔═══════════════════════════════════════════════════════════╗"
  echo "║    ${label} (${CURRENT_MODE_LABEL}): ${test_rate} RPS with ${STRESS_MOCKER_LATENCY_MS}ms latency   ║"
  echo "╚═══════════════════════════════════════════════════════════╝"
  echo ""
  log_info "Testing ${CURRENT_MODE_LABEL} single Bifrost instance under sustained concurrency"
  log_info "Duration: ${STRESS_DURATION}s at ${test_rate} RPS (${STRESS_MOCKER_LATENCY_MS}ms mocker latency)"
  log_info "Expected concurrent requests: ~${test_concurrency} (provider concurrency: 5,000, buffer: 10,000)"
  echo ""

  local target_file="${WORK_DIR}/vegeta-target-stress-${CURRENT_MODE_KEY}-${safe_label}.json"
  local payload
  local encoded_payload
  payload=$(chat_payload "openai/gpt-4o-mini" "${CURRENT_STREAM}")
  encoded_payload=$(printf "%s" "${payload}" | base64_one_line)

  cat > "${target_file}" << EOF
{"method": "POST", "url": "http://127.0.0.1:${BIFROST_PORT}/v1/chat/completions", "header": {"Content-Type": ["application/json"]}, "body": "${encoded_payload}"}
EOF

  vegeta attack \
    -format=json \
    -targets="${target_file}" \
    -rate="${test_rate}" \
    -duration="${STRESS_DURATION}s" \
    -timeout="30s" \
    -workers="${test_concurrency}" \
    -max-workers="${MAX_WORKERS}" > "${bin_file}"

  echo ""
  log_info "${label} complete. Results:"
  vegeta report < "${bin_file}"

  echo ""
  log_info "Latency histogram:"
  vegeta report -type=hist[0,1ms,5ms,10ms,50ms,100ms,500ms,1s,5s,10s,15s] < "${bin_file}" || log_warn "Histogram generation failed"

  # Check success rate
  extract_latencies "${bin_file}"

  local success_pct=$(printf "%.2f" $(echo "scale=4; $EXTRACTED_SUCCESS * 100" | bc))

  log_info "Actual RPS: $(printf "%.0f" $EXTRACTED_RATE) (configured: ${test_rate})"

  local stress_actual_rps=$(printf "%.0f" $EXTRACTED_RATE)

  # Append stress test results to results file
  cat >> "${RESULTS_FILE}" << EOF

## ${label} - ${CURRENT_MODE_LABEL} (${STRESS_MOCKER_LATENCY_MS}ms mocker latency)

| Metric | Actual RPS | Duration | Concurrent | Success Rate | Min | Mean | P50 | P90 | P95 | P99 | Max |
|--------|-----------|----------|------------|--------------|-----|------|-----|-----|-----|-----|-----|
| ${label} (${CURRENT_MODE_LABEL}) | ${stress_actual_rps} | ${STRESS_DURATION}s | ~${test_concurrency} | ${success_pct}% | $(echo "scale=2; $EXTRACTED_MIN_NS / 1000000" | bc)ms | $(echo "scale=2; $EXTRACTED_MEAN_NS / 1000000" | bc)ms | $(echo "scale=2; $EXTRACTED_50_NS / 1000000" | bc)ms | $(echo "scale=2; $EXTRACTED_90_NS / 1000000" | bc)ms | $(echo "scale=2; $EXTRACTED_95_NS / 1000000" | bc)ms | $(echo "scale=2; $EXTRACTED_99_NS / 1000000" | bc)ms | $(echo "scale=2; $EXTRACTED_MAX_NS / 1000000" | bc)ms |
EOF

  append_stress_json "${CURRENT_MODE_KEY}" "${label}" "${test_rate}" "${success_pct}"

  if [ "$success_pct" != "100.00" ]; then
    echo ""
    log_error "FAILED: ${label} success rate is ${success_pct}% (expected 100%)"
    exit 1
  fi

  log_success "${label} passed: ${success_pct}% success rate"
}

# ============================================================
# Finalize
# ============================================================

finalize_results() {
  CURRENT_PHASE="finalize results"
  # Append process stats if available
  local has_overhead_stats=false
  local has_stress_stats=false

  if [ -n "$OVERHEAD_STATS_CPU_PEAK" ]; then
    has_overhead_stats=true
  fi
  if [ -n "$STATS_CPU_PEAK" ]; then
    has_stress_stats=true
  fi

  if [ "$has_overhead_stats" = true ] || [ "$has_stress_stats" = true ]; then
    cat >> "${RESULTS_FILE}" << 'EOF'

## Bifrost Process Stats (single instance)

| Phase | CPU Avg | CPU Peak | RSS Avg | RSS Peak |
|-------|---------|----------|---------|----------|
EOF

    if [ "$has_overhead_stats" = true ]; then
      echo "| Overhead | ${OVERHEAD_STATS_CPU_AVG}% | ${OVERHEAD_STATS_CPU_PEAK}% | ${OVERHEAD_STATS_RSS_AVG}MB | ${OVERHEAD_STATS_RSS_PEAK}MB |" >> "${RESULTS_FILE}"
    fi
    if [ "$has_stress_stats" = true ]; then
      echo "| Stress | ${STATS_CPU_AVG}% | ${STATS_CPU_PEAK}% | ${STATS_RSS_AVG}MB | ${STATS_RSS_PEAK}MB |" >> "${RESULTS_FILE}"
    fi
  fi

  cat >> "${RESULTS_FILE}" << EOF

## Method

- **Single instance**: All tests run against one bifrost-http process. Non-streaming uses ${RATE} RPS; streaming uses ${STREAMING_RATE} RPS.
- **Overhead measurement**: Non-streaming and streaming chat completions. Mocker at ${OVERHEAD_MOCKER_LATENCY_MS}ms latency, calibration (Vegeta->Mocker) subtracted from test (Vegeta->Bifrost->Mocker)
- **Stress test**: Non-streaming and streaming chat completions. Mocker at ${STRESS_MOCKER_LATENCY_MS}ms latency, verifies 100% success under sustained concurrency

## Notes

- Overhead values are in microseconds (µs), stress test values in milliseconds (ms)
- Overhead is computed by subtracting matching calibration buckets from test buckets; Min/Max are intentionally omitted because extrema from separate runs are not comparable.
- Overhead ignores the mocker jitter, local network request queuing. In real-world the P99 overhead will be approximately 100 microseconds.
- Tiered overhead thresholds: mean<${MAX_OVERHEAD_MEAN_US}µs, p50<${MAX_OVERHEAD_P50_US}µs, p90<${MAX_OVERHEAD_P90_US}µs, p95<${MAX_OVERHEAD_P95_US}µs, p99<${MAX_OVERHEAD_P99_US}µs
- P50/P90/P95/P99 represent percentile latencies

---
*Generated by Bifrost Load Test Script*
EOF

  # Update JSON with process stats
  local tmp_json=$(mktemp)
  if command -v jq &> /dev/null; then
    jq --arg cpu_avg "${STATS_CPU_AVG:-0}" --arg cpu_peak "${STATS_CPU_PEAK:-0}" \
       --arg rss_avg "${STATS_RSS_AVG:-0}" --arg rss_peak "${STATS_RSS_PEAK:-0}" \
       --arg oh_cpu_avg "${OVERHEAD_STATS_CPU_AVG:-0}" --arg oh_cpu_peak "${OVERHEAD_STATS_CPU_PEAK:-0}" \
       --arg oh_rss_avg "${OVERHEAD_STATS_RSS_AVG:-0}" --arg oh_rss_peak "${OVERHEAD_STATS_RSS_PEAK:-0}" \
       '.process_stats = {"overhead": {"cpu_avg_pct": ($oh_cpu_avg | tonumber), "cpu_peak_pct": ($oh_cpu_peak | tonumber), "rss_avg_mb": ($oh_rss_avg | tonumber), "rss_peak_mb": ($oh_rss_peak | tonumber)}, "stress": {"cpu_avg_pct": ($cpu_avg | tonumber), "cpu_peak_pct": ($cpu_peak | tonumber), "rss_avg_mb": ($rss_avg | tonumber), "rss_peak_mb": ($rss_peak | tonumber)}}' \
       "${RESULTS_JSON}" > "${tmp_json}"
    mv "${tmp_json}" "${RESULTS_JSON}"
  fi

  log_success "Results saved to:"
  log_info "  - Markdown: ${RESULTS_FILE}"
  log_info "  - JSON: ${RESULTS_JSON}"

  echo ""
  log_success "Load test final result: PASS"
  if [ -f "${RESULTS_JSON}" ]; then
    echo "Summary:"
    jq -r '
      (.overhead // {}) | to_entries[] |
      "  overhead \(.key): success=\(.value.success_rate)%, mean=\(.value.latency_us.mean)us, p99=\(.value.latency_us.p99)us"
    ' "${RESULTS_JSON}"
    jq -r '
      (.stress // {}) | to_entries[] as $mode |
      ($mode.value // {}) | to_entries[] |
      "  stress \($mode.key) \(.key): success=\(.value.success_rate)%"
    ' "${RESULTS_JSON}"
  fi
}

# Main execution
main() {
  echo ""
  echo "╔═══════════════════════════════════════════════════════════╗"
  echo "║       Bifrost Load Test (single instance)                     ║"
  echo "╚═══════════════════════════════════════════════════════════╝"
  echo ""

  log_info "Configuration: single bifrost-http instance, non-streaming ${RATE} RPS, streaming ${STREAMING_RATE} RPS"
  log_info "Provider concurrency: 5,000 (buffer: 10,000)"
  log_info "Overhead thresholds: mean<${MAX_OVERHEAD_MEAN_US}µs, p50<${MAX_OVERHEAD_P50_US}µs, p90<${MAX_OVERHEAD_P90_US}µs, p95<${MAX_OVERHEAD_P95_US}µs, p99<${MAX_OVERHEAD_P99_US}µs"
  log_info "Phase 1: Overhead measurement — non-streaming + streaming, ${OVERHEAD_MOCKER_LATENCY_MS}ms mocker, ${OVERHEAD_DURATION}s each"
  log_info "Phase 2: Stress test — non-streaming + streaming, ${STRESS_MOCKER_LATENCY_MS}ms mocker, ${STRESS_DURATION}s each"

  check_dependencies
  install_vegeta
  build_bifrost_http
  setup_mocker
  patch_mocker_for_load_test
  build_mocker
  initialize_results
  create_config
  cleanup_ports

  # ── Phase 1: Overhead measurement with ${OVERHEAD_MOCKER_LATENCY_MS}ms mocker ──
  start_mocker ${OVERHEAD_MOCKER_LATENCY_MS}
  start_bifrost
  start_stats_monitor

  set_test_mode "Non-streaming" "non_streaming" "false"
  run_calibration
  run_overhead_test

  set_test_mode "Streaming" "streaming" "true"
  run_calibration
  run_overhead_test

  # ── Collect process stats from overhead phase ──
  stop_stats_monitor
  OVERHEAD_STATS_CPU_AVG="${STATS_CPU_AVG}"
  OVERHEAD_STATS_CPU_PEAK="${STATS_CPU_PEAK}"
  OVERHEAD_STATS_RSS_AVG="${STATS_RSS_AVG}"
  OVERHEAD_STATS_RSS_PEAK="${STATS_RSS_PEAK}"

  # ── Phase 2: Stress test with high-latency mocker ──
  # Restart both mocker and bifrost to ensure a clean fasthttp connection pool.
  # Without restarting bifrost, stale TCP connections from the overhead phase
  # (which used a different mocker process) cause immediate 400s on POST requests
  # because fasthttp does not retry non-idempotent methods on broken connections.
  stop_mocker
  stop_bifrost
  start_mocker ${STRESS_MOCKER_LATENCY_MS}
  start_bifrost
  start_stats_monitor

  set_test_mode "Non-streaming" "non_streaming" "false"
  run_stress_test "Stress"
  set_test_mode "Streaming" "streaming" "true"
  run_stress_test "Stress"

  # ── Collect process stats from stress phase ──
  stop_stats_monitor

  # ── Finalize ──
  finalize_results

  cleanup_ports
  echo ""

  # Print final summary
  echo "╔══════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════╗"
  echo "║                                                         FINAL RESULTS SUMMARY                                                                                    ║"
  echo "╚══════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════════╝"
  echo ""
  cat "${RESULTS_FILE}"
  echo ""
  log_success "All tests passed!"
}

main "$@"
