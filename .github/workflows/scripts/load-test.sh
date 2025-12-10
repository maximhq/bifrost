#!/bin/bash

# Load Test Script for Bifrost
# Runs a load test against bifrost-http with a mocker provider
# Usage: ./load-test.sh
#
# This script:
# 1. Builds bifrost-http and mocker locally
# 2. Creates a config.json with mocker provider (OpenAI-style)
# 3. Starts mocker with 0ms latency and bifrost-http
# 4. Runs a calibration (Vegeta -> Mocker direct) to measure Vegeta+network baseline
# 5. Runs the overhead test (Vegeta -> Bifrost -> Mocker) to measure total
# 6. Subtracts calibration from test to isolate Bifrost proxy overhead
#    (includes local network hop, JSON parsing/unparsing, plugins, and mocker jitter)
# 7. Restarts mocker with 10s latency for a sustained concurrency stress test
# 8. Asserts overhead < tiered thresholds (per percentile) and stress test has 100% success rate

set -e

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
BIFROST_HTTP_DIR="${REPO_ROOT}/transports/bifrost-http"
TRANSPORTS_DIR="${REPO_ROOT}/transports"
WORK_DIR="${SCRIPT_DIR}"
MOCKER_DIR="${REPO_ROOT}/../bifrost-benchmarking/mocker"

BIFROST_PORT=8080
MOCKER_PORT=8000
RATE=1000
MAX_WORKERS=12000
OVERHEAD_DURATION=30            # overhead measurement duration (seconds)
STRESS_DURATION=30              # stress test duration (seconds)
OVERHEAD_MOCKER_LATENCY_MS=1000  # 1 second latency for overhead measurement
STRESS_MOCKER_LATENCY_MS=1000    # 1 second latency for stress test
# Tiered overhead thresholds (µs) — these cover the full proxy cost:
# local network hop, JSON parsing/unparsing, plugins, and mocker jitter.
# At ${RATE} RPS × ${OVERHEAD_MOCKER_LATENCY_MS}ms latency ≈ 1000 concurrent requests.
MAX_OVERHEAD_MEAN_US=5000       # mean overhead threshold (5ms)
MAX_OVERHEAD_P50_US=5000        # p50 overhead threshold (5ms)
MAX_OVERHEAD_P90_US=10000       # p90 overhead threshold (10ms)
MAX_OVERHEAD_P95_US=20000       # p95 overhead threshold (20ms)
MAX_OVERHEAD_P99_US=100000      # p99 overhead threshold (100ms)

# Results storage for summary table
RESULTS_FILE="${WORK_DIR}/load-test-results.md"
RESULTS_JSON="${WORK_DIR}/load-test-results.json"

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

# Cleanup function to kill background processes
cleanup() {
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
  rm -f "${WORK_DIR}/config.json" "${WORK_DIR}/logs.db" "${WORK_DIR}/attack.bin" "${WORK_DIR}/calibration.bin" "${WORK_DIR}/stress.bin" "${WORK_DIR}/bifrost.log" "${WORK_DIR}/vegeta-target.json" "${WORK_DIR}/vegeta-target-calibration.json" "${WORK_DIR}/vegeta-target-stress.json" "${WORK_DIR}/vegeta-report.json" "${WORK_DIR}/bifrost-stats.csv" 2>/dev/null || true
  log_info "Cleanup complete"
}

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

  log_success "All dependencies found"
}

# Kill any process listening on a specific port (not processes with connections to it)
kill_port() {
  local port=$1
  local pids=$(lsof -ti "TCP:${port}" -sTCP:LISTEN 2>/dev/null)
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
  cd "${TRANSPORTS_DIR}"

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
  if [ -d "${REPO_ROOT}/../bifrost-benchmarking" ]; then
    log_info "Updating bifrost-benchmarking repository..."
    cd "${REPO_ROOT}/../bifrost-benchmarking"
    git pull --quiet || true
    cd "${WORK_DIR}"
  else
    log_info "Cloning bifrost-benchmarking repository..."
    cd "${WORK_DIR}"
    git clone --depth 1 https://github.com/maximhq/bifrost-benchmarking.git
  fi

  log_success "Mocker setup complete"
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

# Create config.json for bifrost with mocker provider
create_config() {
  log_info "Creating config.json..."

  cat > "${WORK_DIR}/config.json" << 'EOF'
{
  "$schema": "https://www.getbifrost.ai/schema",
  "client": {
    "enable_logging": false,
    "initial_pool_size": 20000,
    "drop_excess_requests": false,
    "enable_governance": false,
    "allow_direct_keys": false
  },
  "config_store": {
    "enabled": false
  },
  "logs_store": {
    "enabled": false
  },
  "providers": {
    "openai": {
      "keys": [
        {
          "name": "mocker-key",
          "value": "Bearer mocker-key",
          "weight": 1
        }
      ],
      "network_config": {
        "base_url": "http://localhost:8000",
        "default_request_timeout_in_seconds": 30
      },
      "concurrency_and_buffer_size": {
        "concurrency": 20000,
        "buffer_size": 40000
      },
      "custom_provider_config": {
        "base_provider_type": "openai",
        "allowed_requests": {
          "list_models": false,
          "chat_completion": true,
          "chat_completion_stream": true
        }
      }
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
  log_info "Starting mocker server on port ${MOCKER_PORT} with ${latency_ms}ms latency..."

  "${REPO_ROOT}/tmp/mocker" -port ${MOCKER_PORT} -host 0.0.0.0 -latency ${latency_ms} &
  MOCKER_PID=$!

  # Wait for mocker to be ready
  local max_attempts=30
  local attempt=0
  while ! curl -s "http://localhost:${MOCKER_PORT}/v1/chat/completions" -X POST \
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
  log_info "Starting bifrost-http on port ${BIFROST_PORT}..."

  cd "${WORK_DIR}"
  local bifrost_log="${WORK_DIR}/bifrost.log"
  "${REPO_ROOT}/tmp/bifrost-http" -app-dir "${WORK_DIR}" -port "${BIFROST_PORT}" -host "0.0.0.0" -log-level "info" > "${bifrost_log}" 2>&1 &
  BIFROST_PID=$!

  # Wait for bifrost to be fully ready (look for "successfully started bifrost" message)
  local max_attempts=60
  local attempt=0
  while ! grep -q "successfully started bifrost" "${bifrost_log}" 2>/dev/null; do
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

  if command -v jq &> /dev/null; then
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
  elif command -v python3 &> /dev/null; then
    EXTRACTED_MIN_NS=$(python3 -c "import json; d=json.load(open('${json_report_file}')); print(d.get('latencies', {}).get('min', 0))")
    EXTRACTED_MEAN_NS=$(python3 -c "import json; d=json.load(open('${json_report_file}')); print(d.get('latencies', {}).get('mean', 0))")
    EXTRACTED_50_NS=$(python3 -c "import json; d=json.load(open('${json_report_file}')); print(d.get('latencies', {}).get('50th', 0))")
    EXTRACTED_90_NS=$(python3 -c "import json; d=json.load(open('${json_report_file}')); print(d.get('latencies', {}).get('90th', 0))")
    EXTRACTED_95_NS=$(python3 -c "import json; d=json.load(open('${json_report_file}')); print(d.get('latencies', {}).get('95th', 0))")
    EXTRACTED_99_NS=$(python3 -c "import json; d=json.load(open('${json_report_file}')); print(d.get('latencies', {}).get('99th', 0))")
    EXTRACTED_MAX_NS=$(python3 -c "import json; d=json.load(open('${json_report_file}')); print(d.get('latencies', {}).get('max', 0))")
    EXTRACTED_SUCCESS=$(python3 -c "import json; d=json.load(open('${json_report_file}')); print(d.get('success', 0))")
    EXTRACTED_RATE=$(python3 -c "import json; d=json.load(open('${json_report_file}')); print(d.get('rate', 0))")
    EXTRACTED_THROUGHPUT=$(python3 -c "import json; d=json.load(open('${json_report_file}')); print(d.get('throughput', 0))")
  else
    log_error "Neither jq nor python3 found. Cannot parse JSON results."
    return 1
  fi

  rm -f "${json_report_file}"
}

# ============================================================
# Phase 1: Overhead measurement (mocker at ${OVERHEAD_MOCKER_LATENCY_MS}ms)
# ============================================================

# Calibration: Vegeta -> Mocker direct (with latency)
# Measures: Vegeta HTTP client + localhost network round-trip + mocker response generation
run_calibration() {
  echo ""
  echo "╔═══════════════════════════════════════════════════════════╗"
  echo "║    Calibration: Vegeta -> Mocker (${OVERHEAD_MOCKER_LATENCY_MS}ms, direct)        ║"
  echo "╚═══════════════════════════════════════════════════════════╝"
  echo ""
  log_info "Measuring Vegeta + network baseline (mocker at ${OVERHEAD_MOCKER_LATENCY_MS}ms latency)"
  log_info "Duration: ${OVERHEAD_DURATION}s at ${RATE} RPS, ~$(( RATE * OVERHEAD_MOCKER_LATENCY_MS / 1000 )) concurrent"
  echo ""

  local target_file="${WORK_DIR}/vegeta-target-calibration.json"
  local payload='{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hello, how are you?"}]}'

  cat > "${target_file}" << EOF
{"method": "POST", "url": "http://localhost:${MOCKER_PORT}/v1/chat/completions", "header": {"Content-Type": ["application/json"], "Authorization": ["Bearer mocker-key"]}, "body": "$(echo -n "${payload}" | base64)"}
EOF

  vegeta attack \
    -format=json \
    -targets="${target_file}" \
    -rate="${RATE}" \
    -duration="${OVERHEAD_DURATION}s" \
    -timeout="$((OVERHEAD_MOCKER_LATENCY_MS / 1000 + 5))s" \
    -workers=$((RATE * OVERHEAD_MOCKER_LATENCY_MS / 1000)) \
    -max-workers="${MAX_WORKERS}" > "${WORK_DIR}/calibration.bin"

  echo ""
  log_info "Calibration complete. Results:"
  vegeta report < "${WORK_DIR}/calibration.bin"

  extract_latencies "${WORK_DIR}/calibration.bin"

  log_info "Actual RPS: $(printf "%.0f" $EXTRACTED_RATE) (configured: ${RATE})"

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
  echo ""
  echo "╔═══════════════════════════════════════════════════════════╗"
  echo "║  Overhead Test: Vegeta -> Bifrost -> Mocker (${OVERHEAD_MOCKER_LATENCY_MS}ms)     ║"
  echo "╚═══════════════════════════════════════════════════════════╝"
  echo ""
  log_info "Measuring Bifrost overhead (single instance, mocker at ${OVERHEAD_MOCKER_LATENCY_MS}ms latency)"
  log_info "Duration: ${OVERHEAD_DURATION}s at ${RATE} RPS, ~$(( RATE * OVERHEAD_MOCKER_LATENCY_MS / 1000 )) concurrent requests through Bifrost"
  log_info "Overhead consists of: local network hop, JSON parsing/unparsing, plugins, and mocker jitter"
  echo ""

  local target_file="${WORK_DIR}/vegeta-target.json"
  local payload='{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"Hello, how are you?"}]}'

  cat > "${target_file}" << EOF
{"method": "POST", "url": "http://localhost:${BIFROST_PORT}/v1/chat/completions", "header": {"Content-Type": ["application/json"]}, "body": "$(echo -n "${payload}" | base64)"}
EOF

  vegeta attack \
    -format=json \
    -targets="${target_file}" \
    -rate="${RATE}" \
    -duration="${OVERHEAD_DURATION}s" \
    -timeout="$((OVERHEAD_MOCKER_LATENCY_MS / 1000 + 5))s" \
    -workers=$((RATE * OVERHEAD_MOCKER_LATENCY_MS / 1000)) \
    -max-workers="${MAX_WORKERS}" > "${WORK_DIR}/attack.bin"

  echo ""
  log_info "Overhead test complete. Results:"
  vegeta report < "${WORK_DIR}/attack.bin"

  echo ""
  log_info "Latency histogram:"
  vegeta report -type=hist[0,100us,500us,1ms,5ms,10ms,50ms,100ms] < "${WORK_DIR}/attack.bin" || log_warn "Histogram generation failed"

  # Extract and compute overhead
  extract_latencies "${WORK_DIR}/attack.bin"

  log_info "  Raw latencies (ns): min=$EXTRACTED_MIN_NS, mean=$EXTRACTED_MEAN_NS, p50=$EXTRACTED_50_NS, p99=$EXTRACTED_99_NS, max=$EXTRACTED_MAX_NS"
  log_info "  Success rate: $EXTRACTED_SUCCESS"
  log_info "  Actual RPS: $(printf "%.0f" $EXTRACTED_RATE) (configured: ${RATE})"

  if [ -z "$EXTRACTED_MIN_NS" ] || [ "$EXTRACTED_MIN_NS" = "0" ] || [ "$EXTRACTED_MIN_NS" = "null" ]; then
    log_error "Failed to extract latency values from vegeta report"
    exit 1
  fi

  # Subtract calibration per bucket: overhead = through_bifrost - direct_to_mocker
  local us_min=$(printf "%.2f" $(echo "scale=4; ($EXTRACTED_MIN_NS - $CAL_MIN_NS) / 1000" | bc))
  local us_mean=$(printf "%.2f" $(echo "scale=4; ($EXTRACTED_MEAN_NS - $CAL_MEAN_NS) / 1000" | bc))
  local us_50=$(printf "%.2f" $(echo "scale=4; ($EXTRACTED_50_NS - $CAL_50_NS) / 1000" | bc))
  local us_90=$(printf "%.2f" $(echo "scale=4; ($EXTRACTED_90_NS - $CAL_90_NS) / 1000" | bc))
  local us_95=$(printf "%.2f" $(echo "scale=4; ($EXTRACTED_95_NS - $CAL_95_NS) / 1000" | bc))
  local us_99=$(printf "%.2f" $(echo "scale=4; ($EXTRACTED_99_NS - $CAL_99_NS) / 1000" | bc))
  local us_max=$(printf "%.2f" $(echo "scale=4; ($EXTRACTED_MAX_NS - $CAL_MAX_NS) / 1000" | bc))

  local success_pct=$(printf "%.2f" $(echo "scale=4; $EXTRACTED_SUCCESS * 100" | bc))

  echo ""
  log_success "Bifrost overhead (per bucket):"
  log_info "  Min:  ${us_min}µs"
  log_info "  Mean: ${us_mean}µs"
  log_info "  P50:  ${us_50}µs"
  log_info "  P90:  ${us_90}µs"
  log_info "  P95:  ${us_95}µs"
  log_info "  P99:  ${us_99}µs"
  log_info "  Max:  ${us_max}µs"

  local actual_rps=$(printf "%.0f" $EXTRACTED_RATE)

  # Write results
  cat > "${RESULTS_FILE}" << EOF
# Bifrost Load Test Results (single instance, ${actual_rps} RPS)

## Bifrost Processing Overhead

| Metric | Actual RPS | Duration | Concurrent | Success Rate | Min | Mean | P50 | P90 | P95 | P99 | Max |
|--------|-----------|----------|------------|--------------|-----|------|-----|-----|-----|-----|-----|
| Overhead | ${actual_rps} | ${OVERHEAD_DURATION}s | ~$((RATE * OVERHEAD_MOCKER_LATENCY_MS / 1000)) | ${success_pct}% | ${us_min}µs | ${us_mean}µs | ${us_50}µs | ${us_90}µs | ${us_95}µs | ${us_99}µs | ${us_max}µs |
EOF

  echo '{"overhead": {"configured_rate": '"${RATE}"', "actual_rate": '"${actual_rps}"', "duration": '"${OVERHEAD_DURATION}"', "concurrent": '$((RATE * OVERHEAD_MOCKER_LATENCY_MS / 1000))', "success_rate": '"${success_pct}"', "latency_us": {"min": '"${us_min}"', "mean": '"${us_mean}"', "p50": '"${us_50}"', "p90": '"${us_90}"', "p95": '"${us_95}"', "p99": '"${us_99}"', "max": '"${us_max}"'}}, "timestamp": "'"$(date -u +"%Y-%m-%dT%H:%M:%SZ")"'"}' > "${RESULTS_JSON}"

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
    log_error "Overhead ignores the mocker jitter, local network request queuing. In real-world the P99 overhead will be approximately 100 microseconds."
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

# Arguments: $1 = label (e.g. "Stress #1", "Stress #2")
run_stress_test() {
  local label="${1:-Stress}"
  local bin_file="${WORK_DIR}/stress.bin"

  echo ""
  echo "╔═══════════════════════════════════════════════════════════╗"
  echo "║    ${label}: ${RATE} RPS with ${STRESS_MOCKER_LATENCY_MS}ms mocker latency          ║"
  echo "╚═══════════════════════════════════════════════════════════╝"
  echo ""
  log_info "Testing single Bifrost instance under sustained concurrency"
  log_info "Duration: ${STRESS_DURATION}s at ${RATE} RPS (${STRESS_MOCKER_LATENCY_MS}ms mocker latency)"
  log_info "Expected concurrent requests: ~$(( RATE * STRESS_MOCKER_LATENCY_MS / 1000 )) (provider concurrency: 15,000, buffer: 20,000)"
  echo ""

  local target_file="${WORK_DIR}/vegeta-target-stress.json"
  local payload='{"model":"openai/gpt-4o-mini","messages":[{"role":"user","content":"Hello, how are you?"}]}'

  cat > "${target_file}" << EOF
{"method": "POST", "url": "http://localhost:${BIFROST_PORT}/v1/chat/completions", "header": {"Content-Type": ["application/json"]}, "body": "$(echo -n "${payload}" | base64)"}
EOF

  vegeta attack \
    -format=json \
    -targets="${target_file}" \
    -rate="${RATE}" \
    -duration="${STRESS_DURATION}s" \
    -timeout="30s" \
    -workers=$((RATE * STRESS_MOCKER_LATENCY_MS / 1000)) \
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

  log_info "Actual RPS: $(printf "%.0f" $EXTRACTED_RATE) (configured: ${RATE})"

  local stress_actual_rps=$(printf "%.0f" $EXTRACTED_RATE)

  # Append stress test results to results file
  cat >> "${RESULTS_FILE}" << EOF

## ${label} (${STRESS_MOCKER_LATENCY_MS}ms mocker latency)

| Metric | Actual RPS | Duration | Concurrent | Success Rate | Min | Mean | P50 | P90 | P95 | P99 | Max |
|--------|-----------|----------|------------|--------------|-----|------|-----|-----|-----|-----|-----|
| ${label} | ${stress_actual_rps} | ${STRESS_DURATION}s | ~$((RATE * STRESS_MOCKER_LATENCY_MS / 1000)) | ${success_pct}% | $(echo "scale=2; $EXTRACTED_MIN_NS / 1000000" | bc)ms | $(echo "scale=2; $EXTRACTED_MEAN_NS / 1000000" | bc)ms | $(echo "scale=2; $EXTRACTED_50_NS / 1000000" | bc)ms | $(echo "scale=2; $EXTRACTED_90_NS / 1000000" | bc)ms | $(echo "scale=2; $EXTRACTED_95_NS / 1000000" | bc)ms | $(echo "scale=2; $EXTRACTED_99_NS / 1000000" | bc)ms | $(echo "scale=2; $EXTRACTED_MAX_NS / 1000000" | bc)ms |
EOF

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

- **Single instance**: All tests run against one bifrost-http process at ${RATE} RPS
- **Overhead measurement**: Mocker at ${OVERHEAD_MOCKER_LATENCY_MS}ms latency, calibration (Vegeta->Mocker) subtracted from test (Vegeta->Bifrost->Mocker)
- **Stress test**: Mocker at ${STRESS_MOCKER_LATENCY_MS}ms latency, verifies 100% success under sustained concurrency

## Notes

- Overhead values are in microseconds (µs), stress test values in milliseconds (ms)
- Overhead ignores the mocker jitter, local network request queuing. In real-world the P99 overhead will be approximately 100 microseconds.
- Tiered overhead thresholds: mean<${MAX_OVERHEAD_MEAN_US}µs, p50<${MAX_OVERHEAD_P50_US}µs, p90<${MAX_OVERHEAD_P90_US}µs, p95<${MAX_OVERHEAD_P95_US}µs, p99<${MAX_OVERHEAD_P99_US}µs
- P50/P90/P95/P99 represent percentile latencies

---
*Generated by Bifrost Load Test Script*
EOF

  # Update JSON with stress results and process stats
  local tmp_json=$(mktemp)
  if command -v jq &> /dev/null; then
    jq --arg sr "$(printf "%.2f" $(echo "scale=4; $EXTRACTED_SUCCESS * 100" | bc))" \
       --arg cpu_avg "${STATS_CPU_AVG:-0}" --arg cpu_peak "${STATS_CPU_PEAK:-0}" \
       --arg rss_avg "${STATS_RSS_AVG:-0}" --arg rss_peak "${STATS_RSS_PEAK:-0}" \
       --arg oh_cpu_avg "${OVERHEAD_STATS_CPU_AVG:-0}" --arg oh_cpu_peak "${OVERHEAD_STATS_CPU_PEAK:-0}" \
       --arg oh_rss_avg "${OVERHEAD_STATS_RSS_AVG:-0}" --arg oh_rss_peak "${OVERHEAD_STATS_RSS_PEAK:-0}" \
       '.stress = {"rate": '"${RATE}"', "duration": '"${STRESS_DURATION}"', "mocker_latency_ms": '"${STRESS_MOCKER_LATENCY_MS}"', "success_rate": ($sr | tonumber)} | .process_stats = {"overhead": {"cpu_avg_pct": ($oh_cpu_avg | tonumber), "cpu_peak_pct": ($oh_cpu_peak | tonumber), "rss_avg_mb": ($oh_rss_avg | tonumber), "rss_peak_mb": ($oh_rss_peak | tonumber)}, "stress": {"cpu_avg_pct": ($cpu_avg | tonumber), "cpu_peak_pct": ($cpu_peak | tonumber), "rss_avg_mb": ($rss_avg | tonumber), "rss_peak_mb": ($rss_peak | tonumber)}}' \
       "${RESULTS_JSON}" > "${tmp_json}"
    mv "${tmp_json}" "${RESULTS_JSON}"
  fi

  log_success "Results saved to:"
  log_info "  - Markdown: ${RESULTS_FILE}"
  log_info "  - JSON: ${RESULTS_JSON}"
}

# Main execution
main() {
  echo ""
  echo "╔═══════════════════════════════════════════════════════════╗"
  echo "║       Bifrost Load Test (single instance, ${RATE} RPS)        ║"
  echo "╚═══════════════════════════════════════════════════════════╝"
  echo ""

  log_info "Configuration: single bifrost-http instance, ${RATE} RPS"
  log_info "Provider concurrency: 15,000 (buffer: 20,000)"
  log_info "Overhead thresholds: mean<${MAX_OVERHEAD_MEAN_US}µs, p50<${MAX_OVERHEAD_P50_US}µs, p90<${MAX_OVERHEAD_P90_US}µs, p95<${MAX_OVERHEAD_P95_US}µs, p99<${MAX_OVERHEAD_P99_US}µs"
  log_info "Phase 1: Overhead measurement — ${OVERHEAD_MOCKER_LATENCY_MS}ms mocker, ${OVERHEAD_DURATION}s, ~$(( RATE * OVERHEAD_MOCKER_LATENCY_MS / 1000 )) concurrent requests"
  log_info "Phase 2: Stress test — ${STRESS_MOCKER_LATENCY_MS}ms mocker, ${STRESS_DURATION}s, ~$(( RATE * STRESS_MOCKER_LATENCY_MS / 1000 )) concurrent requests"

  check_dependencies
  install_vegeta
  build_bifrost_http
  setup_mocker
  build_mocker
  create_config
  cleanup_ports

  # ── Phase 1: Overhead measurement with ${OVERHEAD_MOCKER_LATENCY_MS}ms mocker ──
  start_mocker ${OVERHEAD_MOCKER_LATENCY_MS}
  start_bifrost
  start_stats_monitor

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

  run_stress_test "Stress #1"

  echo ""
  log_info "Waiting 30s before second stress test (idle period)..."
  sleep 30

  run_stress_test "Stress #2"

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
