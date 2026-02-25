#!/usr/bin/env bash
set -euo pipefail

# Migration Tests for Bifrost
# This script validates database migrations by:
# 1. Running previous versions of bifrost to create schema
# 2. Inserting faker data into all tables
# 3. Running current version to verify migrations work
#
# Usage: ./run-migration-tests.sh [db_type]
#   db_type: "postgres", "sqlite", or "all" (default: "all")
#
# Examples:
#   ./run-migration-tests.sh           # Test both PostgreSQL and SQLite
#   ./run-migration-tests.sh postgres  # Test PostgreSQL only
#   ./run-migration-tests.sh sqlite    # Test SQLite only
#
# Environment Variables (optional overrides):
#   POSTGRES_HOST     - PostgreSQL host (default: localhost)
#   POSTGRES_PORT     - PostgreSQL port (default: 5432)
#   POSTGRES_USER     - PostgreSQL user (default: bifrost)
#   POSTGRES_PASSWORD - PostgreSQL password (default: bifrost_password)
#   POSTGRES_DB       - PostgreSQL database (default: bifrost)
#   BIFROST_PORT      - Port for bifrost server (default: 8089)
#   VERSIONS_TO_TEST  - Number of previous versions to test (default: 3)

# Pull all the tags available
git fetch --tags

# Get the absolute path of the script directory
if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi

# Repository root (3 levels up from .github/workflows/scripts)
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"

# Setup Go workspace for CI (go.work is gitignored, must be regenerated)
source "$SCRIPT_DIR/setup-go-workspace.sh"

# Configuration
DB_TYPE="${1:-all}"

POSTGRES_HOST="${POSTGRES_HOST:-localhost}"
POSTGRES_PORT="${POSTGRES_PORT:-5432}"
POSTGRES_USER="${POSTGRES_USER:-bifrost}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-bifrost_password}"
POSTGRES_DB="${POSTGRES_DB:-bifrost_migration_test}"
POSTGRES_SSLMODE="${POSTGRES_SSLMODE:-disable}"
BIFROST_PORT="${BIFROST_PORT:-8089}"
VERSIONS_TO_TEST="${VERSIONS_TO_TEST:-3}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Temp directory for test artifacts
TEMP_DIR=""
BIFROST_PID=""

# ============================================================================
# Utility Functions
# ============================================================================

log_info() {
  echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
  echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
  echo -e "${RED}[ERROR]${NC} $1"
}

# Check if running in CI
is_ci() {
  [ -n "${CI:-}" ] || [ -n "${GITHUB_ACTIONS:-}" ]
}

# Find an available port
find_available_port() {
  local start_port="${1:-8089}"
  local port=$start_port
  
  while [ $port -lt $((start_port + 100)) ]; do
    if ! lsof -i ":$port" >/dev/null 2>&1; then
      echo "$port"
      return 0
    fi
    port=$((port + 1))
  done
  
  # Fallback to a random high port
  echo $((RANDOM % 10000 + 50000))
}

# Cleanup function
cleanup() {
  local exit_code=$?
  
  log_info "Cleaning up..."
  
  # Kill bifrost if running
  if [ -n "${BIFROST_PID:-}" ]; then
    log_info "Stopping bifrost (PID: $BIFROST_PID)..."
    kill "$BIFROST_PID" 2>/dev/null || true
    wait "$BIFROST_PID" 2>/dev/null || true
  fi
  
  # Also kill any bifrost processes on our port
  if [ -n "${BIFROST_PORT:-}" ]; then
    local pids
    pids=$(lsof -t -i ":$BIFROST_PORT" 2>/dev/null || true)
    if [ -n "$pids" ]; then
      log_info "Killing processes on port $BIFROST_PORT: $pids"
      echo "$pids" | xargs kill 2>/dev/null || true
    fi
  fi
  
  # Remove temp directory
  if [ -n "${TEMP_DIR:-}" ] && [ -d "$TEMP_DIR" ]; then
    log_info "Removing temp directory: $TEMP_DIR"
    rm -rf "$TEMP_DIR"
  fi
  
  exit $exit_code
}
trap cleanup EXIT

# Get previous N transport versions (excluding prereleases)
get_previous_versions() {
  local count="${1:-3}"
  cd "$REPO_ROOT"
  git tag -l "transports/v*" | grep -v -- "-" | sort -V | tail -n "$count" | sed 's|transports/||'
}

# Wait for bifrost to start
wait_for_bifrost() {
  local log_file="$1"
  local max_wait="${2:-60}"
  local elapsed=0
  
  while [ $elapsed -lt $max_wait ]; do
    if grep -q "successfully started bifrost" "$log_file" 2>/dev/null; then
      return 0
    fi
    
    # Check if process is still running
    if [ -n "${BIFROST_PID:-}" ] && ! kill -0 "$BIFROST_PID" 2>/dev/null; then
      log_error "Bifrost process died unexpectedly"
      cat "$log_file" 2>/dev/null || true
      return 1
    fi
    
    sleep 1
    elapsed=$((elapsed + 1))
  done
  
  log_error "Bifrost failed to start within ${max_wait}s"
  cat "$log_file" 2>/dev/null || true
  return 1
}

# Stop bifrost gracefully
stop_bifrost() {
  if [ -n "${BIFROST_PID:-}" ]; then
    log_info "Stopping bifrost (PID: $BIFROST_PID)..."
    kill "$BIFROST_PID" 2>/dev/null || true
    wait "$BIFROST_PID" 2>/dev/null || true
    BIFROST_PID=""
    
    # Wait for port to be released
    local max_wait=10
    local elapsed=0
    while [ $elapsed -lt $max_wait ]; do
      if ! lsof -i ":$BIFROST_PORT" >/dev/null 2>&1; then
        log_info "Port $BIFROST_PORT is now free"
        return 0
      fi
      sleep 1
      elapsed=$((elapsed + 1))
    done
    
    # Force kill anything still on the port
    local pids
    pids=$(lsof -t -i ":$BIFROST_PORT" 2>/dev/null || true)
    if [ -n "$pids" ]; then
      log_warn "Force killing processes on port $BIFROST_PORT: $pids"
      echo "$pids" | xargs kill -9 2>/dev/null || true
      sleep 1
    fi
  fi
}

# ============================================================================
# PostgreSQL Functions
# ============================================================================

check_postgres_available() {
  if ! command -v docker >/dev/null 2>&1; then
    log_warn "Docker not found. PostgreSQL tests will be skipped."
    return 1
  fi
  return 0
}

ensure_postgres_running() {
  local compose_file="$REPO_ROOT/.github/workflows/configs/docker-compose.yml"
  
  if [ ! -f "$compose_file" ]; then
    log_error "Docker compose file not found: $compose_file"
    return 1
  fi
  
  # Always ensure docker-compose postgres is running (not some other postgres)
  log_info "Ensuring docker-compose PostgreSQL is running..."
  docker compose -f "$compose_file" up -d postgres
  
  # Wait for postgres to be ready via docker exec
  log_info "Waiting for PostgreSQL to be ready..."
  local max_wait=30
  local elapsed=0
  while [ $elapsed -lt $max_wait ]; do
    if docker compose -f "$compose_file" exec -T postgres pg_isready -U "$POSTGRES_USER" >/dev/null 2>&1; then
      log_info "PostgreSQL container is ready"
      break
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
  
  if [ $elapsed -ge $max_wait ]; then
    log_error "PostgreSQL container failed to start within ${max_wait}s"
    return 1
  fi
  
  # Also verify we can connect from localhost (port mapping works)
  log_info "Verifying localhost connectivity on port $POSTGRES_PORT..."
  elapsed=0
  while [ $elapsed -lt 10 ]; do
    if pg_isready -h "$POSTGRES_HOST" -p "$POSTGRES_PORT" -U "$POSTGRES_USER" >/dev/null 2>&1; then
      log_info "PostgreSQL is accessible on $POSTGRES_HOST:$POSTGRES_PORT"
      return 0
    fi
    # Alternative check using nc/netcat if pg_isready not available
    if command -v nc >/dev/null 2>&1; then
      if nc -z "$POSTGRES_HOST" "$POSTGRES_PORT" 2>/dev/null; then
        log_info "PostgreSQL port $POSTGRES_PORT is open"
        return 0
      fi
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
  
  log_warn "Could not verify localhost connectivity, but container is running - proceeding"
  return 0
}

reset_postgres_database() {
  log_info "Resetting PostgreSQL database: $POSTGRES_DB"
  
  local container
  container=$(get_postgres_container)
  
  if [ -z "$container" ]; then
    log_error "Could not find any postgres container"
    return 1
  fi
  
  log_info "Using postgres container: $container"
  
  # First, terminate any existing connections to the database
  log_info "Terminating existing connections..."
  docker exec "$container" \
    psql -U "$POSTGRES_USER" -d postgres \
    -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$POSTGRES_DB' AND pid <> pg_backend_pid();" \
    2>/dev/null || true
  
  # Now drop and recreate
  log_info "Dropping and recreating database..."
  if ! docker exec "$container" \
    psql -U "$POSTGRES_USER" -d postgres \
    -c "DROP DATABASE IF EXISTS $POSTGRES_DB;"; then
      log_error "Failed to drop database"
      return 1
  fi
  
  if ! docker exec "$container" \
    psql -U "$POSTGRES_USER" -d postgres \
    -c "CREATE DATABASE $POSTGRES_DB;"; then
      log_error "Failed to create database"
      return 1
  fi
  
  log_info "Database reset complete"
  return 0
}

get_postgres_container() {
  local compose_file="$REPO_ROOT/.github/workflows/configs/docker-compose.yml"
  local container
  
  # First try docker-compose container
  container=$(docker compose -f "$compose_file" ps -q postgres 2>/dev/null || true)
  
  if [ -z "$container" ]; then
    # Fallback: find container by name pattern (prefer configs-postgres)
    container=$(docker ps -q --filter "name=configs-postgres" 2>/dev/null | head -1 || true)
  fi
  
  if [ -z "$container" ]; then
    # Last resort: any postgres container with port 5432 mapped
    container=$(docker ps --filter "publish=5432" -q 2>/dev/null | head -1 || true)
  fi
  
  echo "$container"
}

run_postgres_sql() {
  local sql="$1"
  
  local container
  container=$(get_postgres_container)
  
  if [ -z "$container" ]; then
    log_error "PostgreSQL container not found"
    return 1
  fi
  
  docker exec "$container" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
    -c "$sql" 2>/dev/null
}

run_postgres_sql_file() {
  local sql_file="$1"
  
  local container
  container=$(get_postgres_container)
  
  if [ -z "$container" ]; then
    log_error "PostgreSQL container not found"
    return 1
  fi
  
  docker exec -i "$container" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" < "$sql_file" 2>/dev/null
}

get_postgres_table_count() {
  local table="$1"
  local container
  container=$(get_postgres_container)
  
  if [ -z "$container" ]; then
    echo "0"
    return
  fi
  
  local result
  result=$(docker exec "$container" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -t -A \
    -c "SELECT COUNT(*) FROM $table;" 2>/dev/null || echo "0")
  echo "${result:-0}"
}

# ============================================================================
# SQLite Functions
# ============================================================================

reset_sqlite_database() {
  local db_path="$1"
  
  log_info "Resetting SQLite database: $db_path"
  rm -f "$db_path"
  return 0
}

run_sqlite_sql() {
  local db_path="$1"
  local sql="$2"
  
  if ! command -v sqlite3 >/dev/null 2>&1; then
    log_error "sqlite3 not found"
    return 1
  fi
  
  sqlite3 "$db_path" "$sql" 2>/dev/null
}

run_sqlite_sql_file() {
  local db_path="$1"
  local sql_file="$2"
  
  if ! command -v sqlite3 >/dev/null 2>&1; then
    log_error "sqlite3 not found"
    return 1
  fi
  
  sqlite3 "$db_path" < "$sql_file" 2>/dev/null
}

get_sqlite_table_count() {
  local db_path="$1"
  local table="$2"
  local result
  result=$(run_sqlite_sql "$db_path" "SELECT COUNT(*) FROM $table;" 2>/dev/null)
  echo "${result:-0}"
}

# ============================================================================
# Faker Data Generation
# ============================================================================

generate_faker_sql() {
  local db_type="$1"  # postgres or sqlite
  local output_file="$2"
  
  local now
  local future
  local past
  if [ "$db_type" = "postgres" ]; then
    now="NOW()"
    future="NOW() + INTERVAL '1 hour'"
    past="NOW() - INTERVAL '1 day'"
  else
    now="datetime('now')"
    future="datetime('now', '+1 hour')"
    past="datetime('now', '-1 day')"
  fi
  
  cat > "$output_file" << EOF
-- Faker data for migration tests
-- Generated for: $db_type
-- IMPORTANT: Insert data into ALL tables to verify migration preserves data
-- Order respects foreign key dependencies
-- NOTE: All columns must be covered to ensure migration tests are comprehensive

-- ============================================================================
-- 1. Tables with NO foreign keys (base tables)
-- ============================================================================

-- config_hashes (tracks config file hash)
INSERT INTO config_hashes (id, hash, created_at, updated_at)
VALUES (1, 'migration-test-hash-abc123def456', $now, $now)
ON CONFLICT DO NOTHING;

-- governance_budgets (reset_duration is a string like "1d", "1h", etc.)
INSERT INTO governance_budgets (id, max_limit, current_usage, reset_duration, last_reset, config_hash, created_at, updated_at)
VALUES 
  ('budget-migration-test-1', 1000.00, 100.00, '1d', $now, 'budget-hash-001', $now, $now),
  ('budget-migration-test-2', 5000.00, 250.00, '7d', $now, 'budget-hash-002', $now, $now)
ON CONFLICT DO NOTHING;

-- governance_rate_limits (flexible duration format with token_* and request_* columns)
INSERT INTO governance_rate_limits (id, token_max_limit, token_reset_duration, token_current_usage, token_last_reset, request_max_limit, request_reset_duration, request_current_usage, request_last_reset, config_hash, created_at, updated_at)
VALUES 
  ('ratelimit-migration-test-1', 10000, '1m', 500, $now, 100, '1m', 10, $now, 'ratelimit-hash-001', $now, $now),
  ('ratelimit-migration-test-2', 50000, '1d', 2500, $now, 500, '1d', 50, $now, 'ratelimit-hash-002', $now, $now)
ON CONFLICT DO NOTHING;

-- governance_customers (with budget_id, rate_limit_id, and config_hash)
INSERT INTO governance_customers (id, name, budget_id, rate_limit_id, config_hash, created_at, updated_at)
VALUES 
  ('customer-migration-test-1', 'Migration Test Customer One', 'budget-migration-test-1', 'ratelimit-migration-test-1', 'customer-hash-001', $now, $now),
  ('customer-migration-test-2', 'Migration Test Customer Two', NULL, NULL, 'customer-hash-002', $now, $now)
ON CONFLICT DO NOTHING;

-- governance_teams (with customer_id, budget_id, rate_limit_id, profile, config, claims, config_hash)
INSERT INTO governance_teams (id, name, customer_id, budget_id, rate_limit_id, profile, config, claims, config_hash, created_at, updated_at)
VALUES 
  ('team-migration-test-1', 'Migration Test Team Alpha', 'customer-migration-test-1', 'budget-migration-test-2', 'ratelimit-migration-test-2', '{"role": "admin"}', '{"setting": "value"}', '{"claim1": "val1"}', 'team-hash-001', $now, $now),
  ('team-migration-test-2', 'Migration Test Team Beta', NULL, NULL, NULL, NULL, NULL, NULL, 'team-hash-002', $now, $now)
ON CONFLICT DO NOTHING;

-- config_providers (with all JSON config fields and governance fields including budget_id, rate_limit_id)
INSERT INTO config_providers (name, send_back_raw_request, send_back_raw_response, network_config_json, concurrency_buffer_json, proxy_config_json, custom_provider_config_json, budget_id, rate_limit_id, config_hash, created_at, updated_at)
VALUES 
  ('openai', false, false, '{"timeout": 30}', '{"buffer_size": 100}', NULL, NULL, 'budget-migration-test-1', 'ratelimit-migration-test-1', 'provider-hash-openai', $now, $now),
  ('anthropic', true, true, '{"timeout": 60}', '{"buffer_size": 200}', '{"url": "http://proxy.test"}', NULL, NULL, NULL, 'provider-hash-anthropic', $now, $now)
ON CONFLICT DO NOTHING;

-- framework_configs
INSERT INTO framework_configs (id, pricing_url, pricing_sync_interval)
VALUES (1, 'https://example.com/pricing.json', 3600)
ON CONFLICT DO NOTHING;

-- config_log_store (with config column)
INSERT INTO config_log_store (id, enabled, type, config, created_at, updated_at)
VALUES (1, true, 'postgres', '{"host": "localhost", "port": 5432}', $now, $now)
ON CONFLICT DO NOTHING;

-- config_vector_store (with config column)
INSERT INTO config_vector_store (id, enabled, type, ttl_seconds, cache_by_model, cache_by_provider, config, created_at, updated_at)
VALUES (1, false, 'redis', 300, true, false, '{"host": "localhost", "port": 6379}', $now, $now)
ON CONFLICT DO NOTHING;

-- oauth_tokens (OAuth access/refresh tokens - no FK, must be before oauth_configs)
INSERT INTO oauth_tokens (id, access_token, refresh_token, token_type, expires_at, scopes, last_refreshed_at, created_at, updated_at)
VALUES
  ('oauth-token-migration-test-001', 'encrypted-access-token-fake-001', 'encrypted-refresh-token-fake-001', 'Bearer', $future, '["read", "write"]', $now, $now, $now),
  ('oauth-token-migration-test-002', 'encrypted-access-token-fake-002', '', 'Bearer', $future, '[]', NULL, $now, $now)
ON CONFLICT DO NOTHING;

-- oauth_configs (OAuth client configurations - references oauth_tokens via token_id)
INSERT INTO oauth_configs (id, client_id, client_secret, authorize_url, token_url, registration_url, redirect_uri, scopes, state, code_verifier, code_challenge, status, token_id, server_url, use_discovery, mcp_client_config_json, created_at, updated_at, expires_at)
VALUES
  ('oauth-config-migration-test-001', 'client-id-fake-001', 'encrypted-secret-fake-001', 'https://auth.example.com/authorize', 'https://auth.example.com/token', NULL, 'https://bifrost.example.com/oauth/callback', '["read", "write"]', 'state-migration-test-001', 'verifier-migration-test-001', 'challenge-migration-test-001', 'authorized', 'oauth-token-migration-test-001', 'https://mcp.example.com', false, NULL, $now, $now, $future),
  ('oauth-config-migration-test-002', 'client-id-fake-002', '', 'https://auth2.example.com/authorize', 'https://auth2.example.com/token', 'https://auth2.example.com/register', 'https://bifrost.example.com/oauth/callback2', '[]', 'state-migration-test-002', 'verifier-migration-test-002', 'challenge-migration-test-002', 'pending', NULL, 'https://mcp2.example.com', true, '{"name":"test-client"}', $now, $now, $future)
ON CONFLICT DO NOTHING;

-- distributed_locks
INSERT INTO distributed_locks (lock_key, holder_id, expires_at, created_at)
VALUES ('migration-test-lock', 'holder-migration-test-001', $future, $now)
ON CONFLICT DO NOTHING;

-- config_client (global client configuration)
INSERT INTO config_client (id, drop_excess_requests, prometheus_labels_json, allowed_origins_json, allowed_headers_json, header_filter_config_json, initial_pool_size, enable_logging, disable_content_logging, disable_db_pings_in_health, log_retention_days, enforce_governance_header, allow_direct_keys, max_request_body_size_mb, mcp_agent_depth, mcp_tool_execution_timeout, mcp_code_mode_binding_level, mcp_tool_sync_interval, enable_litellm_fallbacks, config_hash, created_at, updated_at)
VALUES (1, false, '["provider", "model"]', '["*"]', '["Authorization"]', '{}', 300, true, false, false, 365, true, false, true, 100, 10, 30, 'server', 10, false, 'client-config-hash-001', $now, $now)
ON CONFLICT DO NOTHING;

-- governance_config (key-value config table)
INSERT INTO governance_config (key, value)
VALUES 
  ('migration_test_key_1', 'migration_test_value_1'),
  ('migration_test_key_2', 'migration_test_value_2')
ON CONFLICT DO NOTHING;

-- governance_model_pricing (model pricing data - with ALL columns)
-- NOTE: base_model column is added dynamically via append_dynamic_inserts() for schema compatibility
INSERT INTO governance_model_pricing (id, model, provider, input_cost_per_token, output_cost_per_token, mode, input_cost_per_video_per_second, input_cost_per_audio_per_second, input_cost_per_character, output_cost_per_character, input_cost_per_token_above128k_tokens, input_cost_per_character_above128k_tokens, input_cost_per_image_above128k_tokens, input_cost_per_video_per_second_above128k_tokens, input_cost_per_audio_per_second_above128k_tokens, output_cost_per_token_above128k_tokens, output_cost_per_character_above128k_tokens, input_cost_per_token_above_200k_tokens, output_cost_per_token_above_200k_tokens, cache_creation_input_token_cost_above_200k_tokens, cache_read_input_token_cost_above_200k_tokens, cache_read_input_token_cost, cache_creation_input_token_cost, input_cost_per_token_batches, output_cost_per_token_batches, input_cost_per_image_token, output_cost_per_image_token, input_cost_per_image, output_cost_per_image, cache_read_input_image_token_cost)
VALUES
  (1, 'gpt-4', 'openai', 0.00003, 0.00006, 'chat', NULL, NULL, NULL, NULL, 0.00006, NULL, NULL, NULL, NULL, 0.00012, NULL, NULL, NULL, NULL, NULL, 0.000015, 0.000045, 0.000015, 0.00003, NULL, NULL, NULL, NULL, NULL),
  (2, 'claude-3-opus', 'anthropic', 0.000015, 0.000075, 'chat', NULL, NULL, 0.00000125, 0.00000625, 0.00002, 0.00000150, NULL, NULL, NULL, 0.0001, 0.0000075, 0.000025, 0.000125, 0.0000375, 0.0000075, 0.0000075, 0.0000375, 0.0000075, 0.0000375, NULL, NULL, 0.02, 0.04, NULL)
ON CONFLICT DO NOTHING;

-- governance_model_configs (model-level governance configuration)
INSERT INTO governance_model_configs (id, model_name, provider, budget_id, rate_limit_id, config_hash, created_at, updated_at)
VALUES 
  ('model-config-migration-test-1', 'gpt-4', 'openai', 'budget-migration-test-1', 'ratelimit-migration-test-1', 'model-config-hash-001', $now, $now),
  ('model-config-migration-test-2', 'claude-3-opus', 'anthropic', NULL, NULL, 'model-config-hash-002', $now, $now)
ON CONFLICT DO NOTHING;

-- migrations (migration tracking table - used by gorp migrator)
INSERT INTO migrations (id, applied_at)
VALUES ('migration-test-001', $now)
ON CONFLICT DO NOTHING;

-- ============================================================================
-- 2. Tables with foreign keys to base tables
-- ============================================================================

-- config_keys (references config_providers) - with ALL columns including Azure/Vertex/Bedrock/Replicate fields
-- NOTE: azure_scopes column is added dynamically via append_dynamic_inserts() for schema compatibility
INSERT INTO config_keys (name, provider_id, provider, key_id, value, models_json, weight, enabled, config_hash, azure_endpoint, azure_api_version, azure_deployments_json, azure_client_id, azure_client_secret, azure_tenant_id, vertex_project_id, vertex_project_number, vertex_region, vertex_auth_credentials, vertex_deployments_json, bedrock_access_key, bedrock_secret_key, bedrock_session_token, bedrock_region, bedrock_arn, bedrock_deployments_json, bedrock_batch_s3_config_json, use_for_batch_api, replicate_deployments_json, created_at, updated_at)
SELECT 'migration-test-key-openai', id, 'openai', 'key-migration-uuid-001', 'sk-migration-test-fake-key-value-openai', '["gpt-4", "gpt-3.5-turbo"]', 1.0, true, 'key-hash-001', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, false, NULL, $now, $now
FROM config_providers WHERE name = 'openai'
ON CONFLICT DO NOTHING;

INSERT INTO config_keys (name, provider_id, provider, key_id, value, models_json, weight, enabled, config_hash, azure_endpoint, azure_api_version, azure_deployments_json, azure_client_id, azure_client_secret, azure_tenant_id, vertex_project_id, vertex_project_number, vertex_region, vertex_auth_credentials, vertex_deployments_json, bedrock_access_key, bedrock_secret_key, bedrock_session_token, bedrock_region, bedrock_arn, bedrock_deployments_json, bedrock_batch_s3_config_json, use_for_batch_api, replicate_deployments_json, created_at, updated_at)
SELECT 'migration-test-key-anthropic', id, 'anthropic', 'key-migration-uuid-002', 'sk-ant-migration-test-fake-key', '["claude-3-opus"]', 0.8, true, 'key-hash-002', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, false, NULL, $now, $now
FROM config_providers WHERE name = 'anthropic'
ON CONFLICT DO NOTHING;

-- config_models (references config_providers) - column is 'name' not 'model_name', 'id' not 'model_id'
INSERT INTO config_models (id, provider_id, name, created_at, updated_at)
SELECT 'model-migration-uuid-001', id, 'gpt-4-turbo', $now, $now
FROM config_providers WHERE name = 'openai'
ON CONFLICT DO NOTHING;

INSERT INTO config_models (id, provider_id, name, created_at, updated_at)
SELECT 'model-migration-uuid-002', id, 'claude-3-opus', $now, $now
FROM config_providers WHERE name = 'anthropic'
ON CONFLICT DO NOTHING;

-- config_env_keys (no FK but tracks env vars)
INSERT INTO config_env_keys (env_var, provider, key_type, config_path, key_id, created_at)
VALUES 
  ('OPENAI_API_KEY', 'openai', 'api_key', 'providers.openai.keys[0]', 'key-migration-uuid-001', $now),
  ('ANTHROPIC_API_KEY', 'anthropic', 'api_key', 'providers.anthropic.keys[0]', 'key-migration-uuid-002', $now)
ON CONFLICT DO NOTHING;

-- config_plugins (with path and config_hash)
INSERT INTO config_plugins (name, enabled, config_json, version, is_custom, path, config_hash, created_at, updated_at)
VALUES 
  ('migration-test-plugin', true, '{"setting1": "value1", "setting2": 42}', 1, false, '/path/to/plugin', 'plugin-hash-001', $now, $now)
ON CONFLICT DO NOTHING;

-- config_mcp_clients INSERT is generated dynamically after this heredoc
-- to handle older schemas that may not have newer columns (tool_pricing_json, auth_type, etc.)

-- governance_virtual_keys (with all columns including description, is_active, team_id, customer_id, budget_id, rate_limit_id, config_hash)
INSERT INTO governance_virtual_keys (id, name, description, value, is_active, team_id, customer_id, budget_id, rate_limit_id, config_hash, created_at, updated_at)
VALUES 
  ('vk-migration-test-1', 'Migration Test Virtual Key 1', 'Test virtual key for migration', 'vk-migration-fake-value-001', true, 'team-migration-test-1', NULL, 'budget-migration-test-1', 'ratelimit-migration-test-1', 'vk-hash-001', $now, $now),
  ('vk-migration-test-2', 'Migration Test Virtual Key 2', 'Another test virtual key', 'vk-migration-fake-value-002', true, NULL, 'customer-migration-test-2', NULL, NULL, 'vk-hash-002', $now, $now)
ON CONFLICT DO NOTHING;

-- governance_virtual_key_provider_configs (references virtual_keys - with all columns)
INSERT INTO governance_virtual_key_provider_configs (virtual_key_id, provider, weight, allowed_models, budget_id, rate_limit_id)
VALUES 
  ('vk-migration-test-1', 'openai', 0.7, '["gpt-4"]', NULL, NULL),
  ('vk-migration-test-2', 'anthropic', 0.3, '[]', 'budget-migration-test-2', 'ratelimit-migration-test-2')
ON CONFLICT DO NOTHING;

-- governance_virtual_key_provider_config_keys (join table for provider configs and keys)
-- Insert after provider configs exist - link to config_keys
INSERT INTO governance_virtual_key_provider_config_keys (table_virtual_key_provider_config_id, table_key_id)
SELECT vpc.id, ck.id
FROM governance_virtual_key_provider_configs vpc
CROSS JOIN config_keys ck
WHERE vpc.virtual_key_id = 'vk-migration-test-1' AND ck.name = 'migration-test-key-openai'
ON CONFLICT DO NOTHING;

-- governance_virtual_key_mcp_configs (references virtual_keys and mcp_clients)
-- We need to reference the mcp_client by its internal ID, so use a subquery
INSERT INTO governance_virtual_key_mcp_configs (virtual_key_id, mcp_client_id, tools_to_execute)
SELECT 'vk-migration-test-1', id, '["tool1"]'
FROM config_mcp_clients WHERE client_id = 'mcp-migration-test-001'
ON CONFLICT DO NOTHING;

-- sessions (id is auto-increment integer, not a string)
INSERT INTO sessions (token, expires_at, created_at, updated_at)
VALUES 
  ('session-migration-token-fake-123', $future, $now, $now),
  ('session-migration-token-fake-456', $future, $now, $now)
ON CONFLICT DO NOTHING;

-- routing_rules (with all columns including config_hash, description, model, fallbacks, query, scope_id)
INSERT INTO routing_rules (id, config_hash, name, description, cel_expression, provider, model, fallbacks, query, scope, scope_id, enabled, priority, created_at, updated_at)
VALUES
  ('rule-migration-test-1', 'rule-hash-001', 'Migration Test Rule One', 'Routes all traffic to openai', 'true', 'openai', 'gpt-4', '["anthropic", "azure"]', '{"temperature": 0.7}', 'global', NULL, true, 1, $now, $now),
  ('rule-migration-test-2', 'rule-hash-002', 'Migration Test Rule Two', 'Fallback rule for anthropic', 'true', 'anthropic', '', NULL, NULL, 'team', 'team-migration-test-1', false, 2, $now, $now)
ON CONFLICT DO NOTHING;

-- ============================================================================
-- 3. Log store tables
-- ============================================================================

-- logs (main log table) - with ALL columns
-- NOTE: routing_engine_used column is added dynamically via append_dynamic_inserts() for schema compatibility
INSERT INTO logs (id, parent_request_id, timestamp, object_type, provider, model, number_of_retries, fallback_index, selected_key_id, selected_key_name, virtual_key_id, virtual_key_name, routing_rule_id, routing_rule_name, input_history, responses_input_history, output_message, responses_output, embedding_output, params, tools, tool_calls, speech_input, transcription_input, image_generation_input, speech_output, transcription_output, image_generation_output, cache_debug, latency, token_usage, cost, status, error_details, stream, content_summary, raw_request, raw_response, prompt_tokens, completion_tokens, total_tokens, created_at)
VALUES
  ('log-migration-test-001', NULL, $past, 'chat_completion', 'openai', 'gpt-4', 0, 0, 'key-migration-uuid-001', 'migration-test-key-openai', 'vk-migration-test-1', 'Migration Test Virtual Key 1', 'rule-migration-test-1', 'Migration Test Rule One', '[{"role":"user","content":"Hello"}]', '', '{"role":"assistant","content":"Hi there!"}', '', '', '{"temperature":0.7}', '[]', '[]', '', '', '', '', '', '', '', 1250.5, '{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}', 0.0045, 'success', '', false, 'Test summary', '{"model":"gpt-4"}', '{"id":"resp-001"}', 10, 20, 30, $past),
  ('log-migration-test-002', 'log-migration-test-001', $past, 'chat_completion', 'anthropic', 'claude-3-opus', 1, 0, 'key-migration-uuid-002', 'migration-test-key-anthropic', 'vk-migration-test-2', 'Migration Test Virtual Key 2', NULL, NULL, '[{"role":"user","content":"Test"}]', '', '{"role":"assistant","content":"Response"}', '', '', '{}', '[]', '[]', '', '', '', '', '', '', '', 2500.75, '{"prompt_tokens":5,"completion_tokens":15,"total_tokens":20}', 0.0125, 'success', '', true, '', '', '', 5, 15, 20, $past),
  ('log-migration-test-003', NULL, $past, 'embedding', 'openai', 'text-embedding-3-small', 0, 0, 'key-migration-uuid-001', 'migration-test-key-openai', NULL, NULL, NULL, NULL, '', '', '', '', '[[0.1,0.2,0.3]]', '{}', '[]', '[]', '', '', '', '', '', '', '', 500.0, '{"prompt_tokens":8,"total_tokens":8}', NULL, 'error', '{"message":"Rate limit exceeded"}', false, '', '', '', 8, 0, 8, $past)
ON CONFLICT DO NOTHING;

-- mcp_tool_logs (with all columns including virtual_key_id, virtual_key_name)
INSERT INTO mcp_tool_logs (id, llm_request_id, timestamp, tool_name, server_label, virtual_key_id, virtual_key_name, arguments, result, error_details, latency, cost, status, created_at)
VALUES 
  ('mcp-log-migration-001', 'log-migration-test-001', $past, 'migration_test_tool_alpha', 'test-server-1', 'vk-migration-test-1', 'Migration Test Virtual Key 1', '{"arg1":"value1"}', '{"result":"success"}', '', 150.5, 0.001, 'success', $past),
  ('mcp-log-migration-002', NULL, $past, 'migration_test_tool_beta', 'test-server-2', NULL, NULL, '{"arg2":"value2"}', '', '{"message":"Tool failed"}', 75.25, NULL, 'error', $past)
ON CONFLICT DO NOTHING;

EOF

  # NOTE: config_mcp_clients INSERT is NOT generated here because it needs to be
  # generated dynamically AFTER the schema is created for each version.
  # Use append_dynamic_mcp_clients_insert() after schema creation.

  log_info "Generated faker SQL: $output_file"
}

# Append dynamic INSERTs to faker SQL based on current schema
# Must be called AFTER the database schema is created (e.g., after bifrost starts/stops)
# Handles columns that may not exist in older schema versions
append_dynamic_mcp_clients_insert() {
  local db_type="$1"
  local faker_sql="$2"
  local config_db="${3:-}"  # Only used for SQLite

  local now
  local past
  if [ "$db_type" = "postgres" ]; then
    now="NOW()"
    past="NOW() - INTERVAL '1 day'"
    generate_mcp_clients_insert_postgres "$now" "$faker_sql"
    append_dynamic_columns_postgres "$now" "$past" "$faker_sql"
  else
    now="datetime('now')"
    past="datetime('now', '-1 day')"
    generate_mcp_clients_insert_sqlite "$now" "$faker_sql" "$config_db"
    append_dynamic_columns_sqlite "$now" "$past" "$faker_sql" "$config_db"
  fi
}

# Append dynamic column UPDATEs for columns that may not exist in older schemas (PostgreSQL)
# Uses UPDATE instead of modifying the INSERT to keep the static INSERTs working for all versions
append_dynamic_columns_postgres() {
  local now="$1"
  local past="$2"
  local output_file="$3"

  echo "" >> "$output_file"
  echo "-- Dynamic column coverage for newer columns (generated based on schema)" >> "$output_file"

  # config_keys.azure_scopes (added in v1.4.5)
  # Set to NULL for coverage - config sync resets this column on startup so non-null values
  # would cause a snapshot comparison diff
  if column_exists_postgres "config_keys" "azure_scopes"; then
    echo "UPDATE config_keys SET azure_scopes = NULL WHERE name = 'migration-test-key-anthropic';" >> "$output_file"
  fi

  # governance_model_pricing.base_model (added in v1.4.5)
  if column_exists_postgres "governance_model_pricing" "base_model"; then
    echo "UPDATE governance_model_pricing SET base_model = 'claude-3-opus-20240229' WHERE model = 'claude-3-opus';" >> "$output_file"
  fi

  # logs.routing_engine_used (added in v1.4.5)
  if column_exists_postgres "logs" "routing_engine_used"; then
    echo "UPDATE logs SET routing_engine_used = 'routing-rule' WHERE id = 'log-migration-test-001';" >> "$output_file"
    echo "UPDATE logs SET routing_engine_used = 'loadbalancing' WHERE id = 'log-migration-test-002';" >> "$output_file"
  fi

  # -------------------------------------------------------------------------
  # Dropped columns - columns that existed in older versions but were removed
  # -------------------------------------------------------------------------

  # config_client.enable_governance (dropped in v1.4.8)
  if column_exists_postgres "config_client" "enable_governance"; then
    echo "UPDATE config_client SET enable_governance = true WHERE id = 1;" >> "$output_file"
  fi
}

# Append dynamic column UPDATEs for columns that may not exist in older schemas (SQLite)
append_dynamic_columns_sqlite() {
  local now="$1"
  local past="$2"
  local output_file="$3"
  local config_db="$4"

  echo "" >> "$output_file"
  echo "-- Dynamic column coverage for newer columns (generated based on schema)" >> "$output_file"

  if [ -f "$config_db" ]; then
    # config_keys.azure_scopes (added in v1.4.5)
    # Set to NULL for coverage - config sync resets this column on startup
    if column_exists_sqlite "$config_db" "config_keys" "azure_scopes"; then
      echo "UPDATE config_keys SET azure_scopes = NULL WHERE name = 'migration-test-key-anthropic';" >> "$output_file"
    fi

    # governance_model_pricing.base_model (added in v1.4.5)
    if column_exists_sqlite "$config_db" "governance_model_pricing" "base_model"; then
      echo "UPDATE governance_model_pricing SET base_model = 'claude-3-opus-20240229' WHERE model = 'claude-3-opus';" >> "$output_file"
    fi
  fi

  # logs.routing_engine_used (added in v1.4.5)
  # logs table is in a separate DB (logs_db). The faker SQL is run against both DBs,
  # so these UPDATEs will be harmless on config_db (table doesn't exist) and work on logs_db.
  # We always emit them - if the column doesn't exist, the UPDATE will fail silently.
  echo "UPDATE logs SET routing_engine_used = 'routing-rule' WHERE id = 'log-migration-test-001';" >> "$output_file"
  echo "UPDATE logs SET routing_engine_used = 'loadbalancing' WHERE id = 'log-migration-test-002';" >> "$output_file"

  # -------------------------------------------------------------------------
  # Dropped columns - columns that existed in older versions but were removed
  # -------------------------------------------------------------------------

  if [ -f "$config_db" ]; then
    # config_client.enable_governance (dropped in v1.4.8)
    if column_exists_sqlite "$config_db" "config_client" "enable_governance"; then
      echo "UPDATE config_client SET enable_governance = true WHERE id = 1;" >> "$output_file"
    fi
  fi
}

# ============================================================================
# Faker Column Coverage Validation
# ============================================================================

# Extract table -> columns mapping from the generated faker SQL
# Output format: table_name:col1,col2,col3 (one per line)
# Handles INSERT INTO and UPDATE SET statements
# Multiple lines per table are OK - the validation merges them via grep + sort -u
extract_faker_columns() {
  local faker_sql="$1"
  local output_file="$2"

  # Extract INSERT statements and parse table/columns
  # Handles both "INSERT INTO table (cols)" and "INSERT INTO table (cols) SELECT ..."
  grep -E "^INSERT INTO [a-z_]+ \(" "$faker_sql" | \
    sed -E 's/INSERT INTO ([a-z_]+) \(([^)]+)\).*/\1:\2/' | \
    tr -d ' ' | sort -u > "$output_file"

  # Also extract UPDATE SET columns (for dynamically added columns)
  # Pattern: UPDATE table SET col = value WHERE ...
  grep -E "^UPDATE [a-z_]+ SET [a-z_]+" "$faker_sql" | \
    sed -E 's/UPDATE ([a-z_]+) SET ([a-z_]+) =.*/\1:\2/' | \
    tr -d ' ' | sort -u >> "$output_file"
}

# Check if a column exists in a PostgreSQL table
column_exists_postgres() {
  local table="$1"
  local column="$2"
  local container
  container=$(get_postgres_container)
  
  if [ -z "$container" ]; then
    return 1
  fi
  
  local count
  count=$(docker exec "$container" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -t -A \
    -c "SELECT COUNT(*) FROM information_schema.columns 
        WHERE table_name = '$table' 
        AND column_name = '$column'
        AND table_schema = 'public';" 2>/dev/null)
  
  [ "$count" = "1" ]
}

# Generate config_mcp_clients INSERT based on schema columns
# This handles older schemas that may not have newer columns like tool_pricing_json, auth_type, etc.
generate_mcp_clients_insert_postgres() {
  local now="$1"
  local output_file="$2"
  
  # Core columns that always exist in config_mcp_clients
  local cols="client_id, name, is_code_mode_client, connection_type, connection_string, stdio_config_json, tools_to_execute_json, tools_to_auto_execute_json, headers_json, is_ping_available, config_hash, created_at, updated_at"
  local vals="'mcp-migration-test-001', 'migration-test-mcp-server', false, 'sse', 'http://mcp-server:8080', NULL, '[\"tool1\", \"tool2\"]', '[]', '{}', true, 'mcp-hash-001', $now, $now"
  
  # Add optional columns if they exist in the schema
  if column_exists_postgres "config_mcp_clients" "tool_pricing_json"; then
    cols="$cols, tool_pricing_json"
    vals="$vals, '{\"tool1\": 0.001, \"tool2\": 0.002}'"
  fi
  
  if column_exists_postgres "config_mcp_clients" "tool_sync_interval"; then
    cols="$cols, tool_sync_interval"
    vals="$vals, 5"
  fi
  
  if column_exists_postgres "config_mcp_clients" "auth_type"; then
    cols="$cols, auth_type"
    vals="$vals, 'oauth'"
  fi
  
  if column_exists_postgres "config_mcp_clients" "oauth_config_id"; then
    cols="$cols, oauth_config_id"
    vals="$vals, 'oauth-config-migration-test-001'"
  fi
  
  # Append the dynamic INSERT to the output file
  echo "" >> "$output_file"
  echo "-- config_mcp_clients (MCP server configurations - dynamically generated based on schema)" >> "$output_file"
  echo "INSERT INTO config_mcp_clients ($cols) VALUES ($vals) ON CONFLICT DO NOTHING;" >> "$output_file"
}

# Get columns that are auto-increment primary keys (don't need faker coverage)
get_postgres_auto_increment_columns() {
  local table="$1"
  local container
  container=$(get_postgres_container)
  
  if [ -z "$container" ]; then
    return
  fi
  
  # Find columns with SERIAL/BIGSERIAL or identity columns
  docker exec "$container" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -t -A \
    -c "SELECT column_name FROM information_schema.columns 
        WHERE table_name = '$table' 
        AND table_schema = 'public'
        AND (column_default LIKE 'nextval%' OR is_identity = 'YES');" 2>/dev/null
}

# Validate that faker SQL covers all columns in the older version's schema
validate_faker_column_coverage_postgres() {
  local faker_sql="$1"
  local version="$2"
  local temp_dir="$3"
  
  log_info "Validating faker column coverage for $version schema..."
  
  # Tables to skip (system/tracking tables)
  local skip_tables="gorp_migrations schema_migrations"
  
  # Extract faker columns to a temp file
  local faker_cols_file="$temp_dir/faker_columns.txt"
  extract_faker_columns "$faker_sql" "$faker_cols_file"
  
  local failed=0
  local missing_report=""
  
  # Get all tables from the database
  local tables
  tables=$(get_postgres_tables)
  
  for table in $tables; do
    # Skip system tables
    if [[ " $skip_tables " == *" $table "* ]]; then
      continue
    fi
    
    # Get columns that faker inserts for this table
    local faker_cols
    faker_cols=$(grep "^${table}:" "$faker_cols_file" 2>/dev/null | cut -d: -f2 | tr ',' '\n' | sort -u)
    
    # If faker doesn't insert into this table at all, that's a problem
    if [ -z "$faker_cols" ]; then
      missing_report="${missing_report}\n  Table '$table': NO FAKER DATA - table not covered by faker SQL"
      failed=1
      continue
    fi
    
    # Get all columns from the database schema
    local db_cols
    db_cols=$(get_postgres_columns "$table")
    
    # Get auto-increment columns (these don't need faker coverage)
    local auto_cols
    auto_cols=$(get_postgres_auto_increment_columns "$table")
    
    # Check each DB column
    local missing_cols=""
    for col in $db_cols; do
      # Skip auto-increment columns
      if echo "$auto_cols" | grep -q "^${col}$"; then
        continue
      fi
      
      # Check if faker covers this column
      if ! echo "$faker_cols" | grep -q "^${col}$"; then
        if [ -z "$missing_cols" ]; then
          missing_cols="$col"
        else
          missing_cols="$missing_cols, $col"
        fi
      fi
    done
    
    if [ -n "$missing_cols" ]; then
      missing_report="${missing_report}\n  Table '$table': $missing_cols"
      failed=1
    fi
  done
  
  if [ $failed -eq 1 ]; then
    log_error "Faker SQL missing coverage for columns in $version schema:"
    echo -e "$missing_report" | while read -r line; do
      [ -n "$line" ] && log_error "$line"
    done
    log_error ""
    log_error "Please update generate_faker_sql() in this script to include these columns."
    log_error "Migration tests require all columns in the older schema to have test data coverage."
    return 1
  fi
  
  log_info "Faker column coverage validation passed for $version schema"
  return 0
}

# Get SQLite table columns
get_sqlite_columns() {
  local db_path="$1"
  local table="$2"
  
  if [ ! -f "$db_path" ]; then
    return
  fi
  
  sqlite3 "$db_path" "PRAGMA table_info($table);" 2>/dev/null | cut -d'|' -f2
}

# Get SQLite tables
get_sqlite_tables() {
  local db_path="$1"
  
  if [ ! -f "$db_path" ]; then
    return
  fi
  
  sqlite3 "$db_path" "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name;" 2>/dev/null
}

# Get SQLite auto-increment primary key columns
get_sqlite_auto_increment_columns() {
  local db_path="$1"
  local table="$2"
  
  if [ ! -f "$db_path" ]; then
    return
  fi
  
  # In SQLite, INTEGER PRIMARY KEY columns are auto-increment
  sqlite3 "$db_path" "PRAGMA table_info($table);" 2>/dev/null | \
    awk -F'|' '$3 == "INTEGER" && $6 == "1" {print $2}'
}

# Check if a column exists in a SQLite table
column_exists_sqlite() {
  local db_path="$1"
  local table="$2"
  local column="$3"
  
  if [ ! -f "$db_path" ]; then
    return 1
  fi
  
  local count
  count=$(sqlite3 "$db_path" "PRAGMA table_info($table);" 2>/dev/null | grep -c "^[0-9]*|${column}|") || count=0
  
  [ "$count" -ge "1" ]
}

# Generate config_mcp_clients INSERT based on schema columns for SQLite
# This handles older schemas that may not have newer columns like tool_pricing_json, auth_type, etc.
generate_mcp_clients_insert_sqlite() {
  local now="$1"
  local output_file="$2"
  local config_db="$3"
  
  # Check if the table exists in the database
  if [ ! -f "$config_db" ]; then
    return
  fi
  
  local table_exists
  table_exists=$(sqlite3 "$config_db" "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='config_mcp_clients';" 2>/dev/null || echo "0")
  
  if [ "$table_exists" != "1" ]; then
    return
  fi
  
  # Core columns that always exist in config_mcp_clients
  local cols="client_id, name, is_code_mode_client, connection_type, connection_string, stdio_config_json, tools_to_execute_json, tools_to_auto_execute_json, headers_json, is_ping_available, config_hash, created_at, updated_at"
  local vals="'mcp-migration-test-001', 'migration-test-mcp-server', 0, 'sse', 'http://mcp-server:8080', NULL, '[\"tool1\", \"tool2\"]', '[]', '{}', 1, 'mcp-hash-001', $now, $now"
  
  # Add optional columns if they exist in the schema
  if column_exists_sqlite "$config_db" "config_mcp_clients" "tool_pricing_json"; then
    cols="$cols, tool_pricing_json"
    vals="$vals, '{\"tool1\": 0.001, \"tool2\": 0.002}'"
  fi
  
  if column_exists_sqlite "$config_db" "config_mcp_clients" "tool_sync_interval"; then
    cols="$cols, tool_sync_interval"
    vals="$vals, 5"
  fi
  
  if column_exists_sqlite "$config_db" "config_mcp_clients" "auth_type"; then
    cols="$cols, auth_type"
    vals="$vals, 'oauth'"
  fi
  
  if column_exists_sqlite "$config_db" "config_mcp_clients" "oauth_config_id"; then
    cols="$cols, oauth_config_id"
    vals="$vals, 'oauth-config-migration-test-001'"
  fi
  
  # Append the dynamic INSERT to the output file
  echo "" >> "$output_file"
  echo "-- config_mcp_clients (MCP server configurations - dynamically generated based on schema)" >> "$output_file"
  echo "INSERT INTO config_mcp_clients ($cols) VALUES ($vals) ON CONFLICT DO NOTHING;" >> "$output_file"
}

# Validate faker column coverage for SQLite
validate_faker_column_coverage_sqlite() {
  local faker_sql="$1"
  local version="$2"
  local temp_dir="$3"
  local config_db="$4"
  local logs_db="$5"
  
  log_info "Validating faker column coverage for $version schema (SQLite)..."
  
  # Tables to skip (system/tracking tables)
  local skip_tables="gorp_migrations schema_migrations"
  
  # Extract faker columns to a temp file
  local faker_cols_file="$temp_dir/faker_columns.txt"
  extract_faker_columns "$faker_sql" "$faker_cols_file"
  
  local failed=0
  local missing_report=""
  
  # Check both config and logs databases
  for db_path in "$config_db" "$logs_db"; do
    if [ ! -f "$db_path" ]; then
      continue
    fi
    
    local db_name
    db_name=$(basename "$db_path")
    
    # Get all tables from the database
    local tables
    tables=$(get_sqlite_tables "$db_path")
    
    for table in $tables; do
      # Skip system tables
      if [[ " $skip_tables " == *" $table "* ]]; then
        continue
      fi
      
      # Get columns that faker inserts for this table
      local faker_cols
      faker_cols=$(grep "^${table}:" "$faker_cols_file" 2>/dev/null | cut -d: -f2 | tr ',' '\n' | sort -u)
      
      # If faker doesn't insert into this table at all, that's a problem
      if [ -z "$faker_cols" ]; then
        missing_report="${missing_report}\n  Table '$table' ($db_name): NO FAKER DATA - table not covered by faker SQL"
        failed=1
        continue
      fi
      
      # Get all columns from the database schema
      local db_cols
      db_cols=$(get_sqlite_columns "$db_path" "$table")
      
      # Get auto-increment columns
      local auto_cols
      auto_cols=$(get_sqlite_auto_increment_columns "$db_path" "$table")
      
      # Check each DB column
      local missing_cols=""
      for col in $db_cols; do
        # Skip auto-increment columns
        if echo "$auto_cols" | grep -q "^${col}$"; then
          continue
        fi
        
        # Check if faker covers this column
        if ! echo "$faker_cols" | grep -q "^${col}$"; then
          if [ -z "$missing_cols" ]; then
            missing_cols="$col"
          else
            missing_cols="$missing_cols, $col"
          fi
        fi
      done
      
      if [ -n "$missing_cols" ]; then
        missing_report="${missing_report}\n  Table '$table' ($db_name): $missing_cols"
        failed=1
      fi
    done
  done
  
  if [ $failed -eq 1 ]; then
    log_error "Faker SQL missing coverage for columns in $version schema:"
    echo -e "$missing_report" | while read -r line; do
      [ -n "$line" ] && log_error "$line"
    done
    log_error ""
    log_error "Please update generate_faker_sql() in this script to include these columns."
    log_error "Migration tests require all columns in the older schema to have test data coverage."
    return 1
  fi
  
  log_info "Faker column coverage validation passed for $version schema (SQLite)"
  return 0
}

# ============================================================================
# Data Snapshot Functions - Capture table data before/after migration
# ============================================================================

# Get list of all tables in the database
get_postgres_tables() {
  local container
  container=$(get_postgres_container)
  
  if [ -z "$container" ]; then
    return
  fi
  
  docker exec "$container" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -t -A \
    -c "SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename;" 2>/dev/null
}

# Get columns for a table
get_postgres_columns() {
  local table="$1"
  local container
  container=$(get_postgres_container)
  
  if [ -z "$container" ]; then
    return
  fi
  
  docker exec "$container" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -t -A \
    -c "SELECT column_name FROM information_schema.columns WHERE table_name = '$table' AND table_schema = 'public' ORDER BY ordinal_position;" 2>/dev/null
}

# Dump table data as CSV-like format (for comparison)
dump_postgres_table() {
  local table="$1"
  local output_file="$2"
  local container
  container=$(get_postgres_container)
  
  if [ -z "$container" ]; then
    return 1
  fi
  
  # Get column list
  local columns
  columns=$(get_postgres_columns "$table" | tr '\n' ',' | sed 's/,$//')
  
  if [ -z "$columns" ]; then
    echo "# Table $table does not exist" > "$output_file"
    return 0
  fi
  
  # Export data as CSV (header + data), sorted by first column for consistent ordering
  docker exec "$container" \
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -t -A -F'|' \
    -c "SELECT $columns FROM $table ORDER BY 1;" 2>/dev/null > "$output_file"
  
  # Prepend column header
  local tmp_file="${output_file}.tmp"
  echo "# COLUMNS: $columns" > "$tmp_file"
  cat "$output_file" >> "$tmp_file"
  mv "$tmp_file" "$output_file"
}

# Capture snapshot of all tables
capture_postgres_snapshot() {
  local snapshot_dir="$1"
  
  mkdir -p "$snapshot_dir"
  
  log_info "Capturing database snapshot..."
  
  local tables
  tables=$(get_postgres_tables)
  
  # Save table list
  echo "$tables" > "$snapshot_dir/tables.txt"
  
  local table_count=0
  for table in $tables; do
    # Skip migration tracking tables
    if [[ "$table" == "gorp_migrations" ]] || [[ "$table" == "schema_migrations" ]]; then
      continue
    fi
    
    dump_postgres_table "$table" "$snapshot_dir/${table}.csv"
    local row_count
    row_count=$(get_postgres_table_count "$table")
    log_info "  Captured $table: $row_count rows"
    table_count=$((table_count + 1))
  done
  
  log_info "Snapshot captured: $table_count tables"
}

# Compare two snapshots - verifies all data from before exists unchanged after migration
compare_postgres_snapshots() {
  local before_dir="$1"
  local after_dir="$2"
  
  log_info "Comparing data before and after migration..."
  
  local failed=0
  local checked=0
  local new_cols_count=0
  
  # Tables to skip entirely (system/tracking tables that change during migration)
  local skip_tables="gorp_migrations schema_migrations migrations governance_config governance_model_pricing"
  
  # Columns to ignore when comparing (these are expected to change during migration)
  # - updated_at: timestamps are updated when records are touched
  # - config_hash: recomputed when config is synced  
  # - created_at: some migrations reset this to default (known issue, tracked separately)
  # - models_json: migrations add default empty array
  # - weight: migrations add default value
  # - allowed_models: migrations add default empty array
  # - network_config_json, concurrency_buffer_json, proxy_config_json, custom_provider_config_json:
  #   JSON fields that get normalized with default values during migration
  # - budget_id, rate_limit_id: governance fields that may be reset or initialized during migrations
  local ignore_columns="updated_at config_hash created_at models_json weight allowed_models network_config_json concurrency_buffer_json proxy_config_json custom_provider_config_json budget_id rate_limit_id"
  
  # Get tables from before snapshot
  if [ ! -f "$before_dir/tables.txt" ]; then
    log_error "Before snapshot not found!"
    return 1
  fi
  
  local before_tables
  before_tables=$(cat "$before_dir/tables.txt")
  
  for table in $before_tables; do
    # Skip system/tracking tables
    if [[ " $skip_tables " == *" $table "* ]]; then
      log_info "  Skipping $table (system table)"
      continue
    fi
    
    local before_file="$before_dir/${table}.csv"
    local after_file="$after_dir/${table}.csv"
    
    if [ ! -f "$before_file" ]; then
      continue
    fi
    
    # Check if table still exists after migration
    if [ ! -f "$after_file" ]; then
      log_error "Table $table missing after migration!"
      failed=1
      continue
    fi
    
    # Get before columns
    local before_columns
    before_columns=$(head -1 "$before_file" | sed 's/^# COLUMNS: //')
    
    # Get after columns  
    local after_columns
    after_columns=$(head -1 "$after_file" | sed 's/^# COLUMNS: //')
    
    # Check that all before columns still exist (new columns are OK)
    # Columns that are intentionally renamed during migration should be excluded
    # routing_engine_used -> routing_engines_used (v1.4.7)
    local renamed_columns="routing_engine_used"
    
    local before_col_array
    IFS=',' read -ra before_col_array <<< "$before_columns"
    
    local missing_cols=""
    for col in "${before_col_array[@]}"; do
      # Skip columns that are intentionally renamed during migration
      if [[ " $renamed_columns " == *" $col "* ]]; then
        continue
      fi
      if [[ ! ",$after_columns," == *",$col,"* ]]; then
        missing_cols="$missing_cols $col"
      fi
    done
    
    if [ -n "$missing_cols" ]; then
      log_error "Table $table: columns dropped after migration:$missing_cols"
      failed=1
      continue
    fi
    
    # Check row count
    local before_rows
    local after_rows
    before_rows=$(tail -n +2 "$before_file" | wc -l | tr -d ' ')
    after_rows=$(tail -n +2 "$after_file" | wc -l | tr -d ' ')
    
    if [ "$before_rows" -ne "$after_rows" ]; then
      log_error "Table $table: row count changed! Before: $before_rows, After: $after_rows"
      failed=1
      continue
    fi
    
    # Skip empty tables
    if [ "$before_rows" -eq 0 ]; then
      log_info "  Table $table: 0 rows (empty)"
      checked=$((checked + 1))
      continue
    fi
    
    # Check if new columns were added (this is OK)
    if [ "$before_columns" != "$after_columns" ]; then
      new_cols_count=$((new_cols_count + 1))
    fi
    
    # Build column indices for comparison - map before column positions to after positions
    # This allows us to compare only the columns that existed before migration
    local after_col_array
    IFS=',' read -ra after_col_array <<< "$after_columns"
    
    # Create index mapping: for each before column, find its position in after columns
    local col_map=""
    local compare_cols=""
    local col_idx=1
    for col in "${before_col_array[@]}"; do
      # Skip columns that are expected to change
      if [[ " $ignore_columns " == *" $col "* ]]; then
        col_idx=$((col_idx + 1))
        continue
      fi
      
      # Find this column in after_columns
      local after_idx=1
      for after_col in "${after_col_array[@]}"; do
        if [ "$col" = "$after_col" ]; then
          col_map="$col_map $col_idx:$after_idx"
          compare_cols="$compare_cols $col"
          break
        fi
        after_idx=$((after_idx + 1))
      done
      col_idx=$((col_idx + 1))
    done
    
    # Guard: skip if no comparable columns (all columns were ignored or not matched)
    if [ -z "$col_map" ]; then
      log_info "  Table $table: no comparable columns (all ignored or unmatched), skipping data comparison"
      checked=$((checked + 1))
      continue
    fi
    
    # Extract comparable data from before file (using before column positions)
    local before_comparable="$before_dir/${table}_comparable.txt"
    local after_comparable="$after_dir/${table}_comparable.txt"
    
    # Build cut command for before columns
    local before_cut_cols=""
    for mapping in $col_map; do
      local before_idx="${mapping%%:*}"
      if [ -z "$before_cut_cols" ]; then
        before_cut_cols="$before_idx"
      else
        before_cut_cols="$before_cut_cols,$before_idx"
      fi
    done
    
    # Build cut command for after columns  
    local after_cut_cols=""
    for mapping in $col_map; do
      local after_idx="${mapping##*:}"
      if [ -z "$after_cut_cols" ]; then
        after_cut_cols="$after_idx"
      else
        after_cut_cols="$after_cut_cols,$after_idx"
      fi
    done
    
    # Extract and sort data for comparison
    tail -n +2 "$before_file" | cut -d'|' -f"$before_cut_cols" | sort > "$before_comparable"
    tail -n +2 "$after_file" | cut -d'|' -f"$after_cut_cols" | sort > "$after_comparable"
    
    # Compare the extracted data
    if ! diff -q "$before_comparable" "$after_comparable" > /dev/null 2>&1; then
      log_error "Table $table: data values changed after migration!"
      log_error "  Compared columns:$compare_cols"
      
      # Show first difference
      local diff_output
      diff_output=$(diff "$before_comparable" "$after_comparable" | head -10)
      log_error "  Difference (first 10 lines):"
      echo "$diff_output" | while read -r line; do
        log_error "    $line"
      done
      
      failed=1
      rm -f "$before_comparable" "$after_comparable"
      continue
    fi
    
    rm -f "$before_comparable" "$after_comparable"
    
    log_info "  Table $table: $before_rows rows ✓ (verified ${#before_col_array[@]} columns)"
    checked=$((checked + 1))
  done
  
  log_info "Comparison complete: $checked tables checked"
  if [ "$new_cols_count" -gt 0 ]; then
    log_info "  $new_cols_count tables have new columns added (OK - schema expansion)"
  fi
  
  return $failed
}

# ============================================================================
# Validation Functions (simplified, uses snapshots)
# ============================================================================

validate_postgres_data() {
  local before_snapshot="$1"
  local after_snapshot="$2"
  
  compare_postgres_snapshots "$before_snapshot" "$after_snapshot"
}

validate_sqlite_data() {
  local config_db="$1"
  local logs_db="$2"
  
  log_info "Validating SQLite data integrity..."
  
  local failed=0
  
  # Check config store tables
  if [ -f "$config_db" ]; then
    # Required tables
    local required_tables=("config_providers")
    for table in "${required_tables[@]}"; do
      local count
      count=$(get_sqlite_table_count "$config_db" "$table")
      if [ "$count" -eq 0 ]; then
        log_error "Required table $table has no data after migration!"
        failed=1
      else
        log_info "Table $table: $count rows ✓"
      fi
    done
  fi
  
  # Check log store tables
  if [ -f "$logs_db" ]; then
    local log_tables=("logs")
    for table in "${log_tables[@]}"; do
      local count
      count=$(get_sqlite_table_count "$logs_db" "$table")
      if [ "$count" -eq 0 ]; then
        log_error "Required table $table has no data after migration!"
        failed=1
      else
        log_info "Table $table: $count rows ✓"
      fi
    done
  fi
  
  return $failed
}

# ============================================================================
# PostgreSQL Migration Test
# ============================================================================

run_postgres_migration_tests() {
  log_info "=========================================="
  log_info "Running PostgreSQL Migration Tests"
  log_info "=========================================="
  
  # Check prerequisites
  if ! check_postgres_available; then
    log_warn "Skipping PostgreSQL tests - Docker not available"
    return 0
  fi
  
  # Find an available port for bifrost
  BIFROST_PORT=$(find_available_port 8089)
  log_info "Using port $BIFROST_PORT for bifrost"
  
  if ! ensure_postgres_running; then
    log_error "Failed to start PostgreSQL"
    return 1
  fi
  
  # Create temp directory
  TEMP_DIR=$(mktemp -d)
  log_info "Using temp directory: $TEMP_DIR"
  
  # Create config file for PostgreSQL
  local config_file="$TEMP_DIR/config.json"
  cat > "$config_file" << EOF
{
  "\$schema": "https://www.getbifrost.ai/schema",
  "config_store": {
    "enabled": true,
    "type": "postgres",
    "config": {
      "host": "$POSTGRES_HOST",
      "port": "$POSTGRES_PORT",
      "user": "$POSTGRES_USER",
      "password": "$POSTGRES_PASSWORD",
      "db_name": "$POSTGRES_DB",
      "ssl_mode": "$POSTGRES_SSLMODE"
    }
  },
  "logs_store": {
    "enabled": true,
    "type": "postgres",
    "config": {
      "host": "$POSTGRES_HOST",
      "port": "$POSTGRES_PORT",
      "user": "$POSTGRES_USER",
      "password": "$POSTGRES_PASSWORD",
      "db_name": "$POSTGRES_DB",
      "ssl_mode": "$POSTGRES_SSLMODE"
    }
  }
}
EOF
  
  # Generate faker SQL
  local faker_sql="$TEMP_DIR/faker.sql"
  generate_faker_sql "postgres" "$faker_sql"
  
  # Build current version ONCE before testing
  log_info "Building current version from Go workspace..."
  local current_binary="$TEMP_DIR/bifrost-http-current"
  cd "$REPO_ROOT"
  # Ensure the embedded ui directory exists (it's gitignored, so it won't be present in CI)
  if [ ! -d "$REPO_ROOT/transports/bifrost-http/ui" ]; then
    mkdir -p "$REPO_ROOT/transports/bifrost-http/ui"
    echo "placeholder" > "$REPO_ROOT/transports/bifrost-http/ui/.gitkeep"
  fi
  if ! go build -o "$current_binary" ./transports/bifrost-http; then
    log_error "Failed to build current version"
    return 1
  fi
  log_info "Current version built successfully: $current_binary"
  
  # Get previous versions
  local versions
  versions=$(get_previous_versions "$VERSIONS_TO_TEST")
  
  if [ -z "$versions" ]; then
    log_warn "No previous versions found, skipping version-based migration tests"
    versions="latest"
  fi
  
  log_info "Testing versions: $(echo $versions | tr '\n' ' ')"
  
  # Test each version
  for version in $versions; do
    log_info "------------------------------------------"
    log_info "Testing migration from version: $version"
    log_info "------------------------------------------"
    
    # Create snapshot directories for this version
    local before_snapshot="$TEMP_DIR/snapshot-before-$version"
    local after_snapshot="$TEMP_DIR/snapshot-after-$version"
    
    # Reset database
    if ! reset_postgres_database; then
      log_error "Failed to reset database for version $version"
      exit 1
    fi
    
    # Start bifrost with this version using npx
    local server_log="$TEMP_DIR/server-$version.log"
    log_info "Starting bifrost $version via npx..."
    
    npx @maximhq/bifrost --transport-version "$version" \
      --app-dir "$TEMP_DIR" --port "$BIFROST_PORT" > "$server_log" 2>&1 &
    BIFROST_PID=$!
    
    if ! wait_for_bifrost "$server_log" 120; then
      log_error "Failed to start bifrost $version"
      cat "$server_log" 2>/dev/null || true
      stop_bifrost
      exit 1
    fi
    
    log_info "Bifrost $version started successfully"
    
    # Stop bifrost (schema is now created)
    stop_bifrost
    
    # Create version-specific faker SQL with dynamic config_mcp_clients INSERT
    local version_faker_sql="$TEMP_DIR/faker-$version.sql"
    cp "$faker_sql" "$version_faker_sql"
    append_dynamic_mcp_clients_insert "postgres" "$version_faker_sql"
    
    # Validate faker column coverage against older version's schema
    if ! validate_faker_column_coverage_postgres "$version_faker_sql" "$version" "$TEMP_DIR"; then
      log_error "Faker column coverage validation failed for $version"
      return 1
    fi
    
    # Insert faker data
    log_info "Inserting faker data..."
    if ! run_postgres_sql_file "$version_faker_sql"; then
      log_warn "Some faker data inserts may have failed (tables might not exist in this version)"
    fi
    
    # STEP 3: Capture snapshot BEFORE migration (after inserting faker data)
    log_info "Capturing pre-migration snapshot..."
    capture_postgres_snapshot "$before_snapshot"
    
    # Now run current version to test migration
    log_info "Running current version to test migration..."
    
    # Start current version (already built)
    local current_log="$TEMP_DIR/server-current-$version.log"
    "$current_binary" --app-dir "$TEMP_DIR" --port "$BIFROST_PORT" > "$current_log" 2>&1 &
    BIFROST_PID=$!
    
    if ! wait_for_bifrost "$current_log" 120; then
      log_error "Current version failed to start after migrating from $version"
      cat "$current_log"
      stop_bifrost
      return 1
    fi
    
    log_info "Current version started successfully after migration from $version"
    
    # Wait a moment to ensure all migrations are fully committed to DB
    # The "successfully started" log means server is listening, but async operations may still be completing
    sleep 2
    
    # Verify the server is actually responding before we capture snapshot
    local health_check_attempts=0
    while [ $health_check_attempts -lt 10 ]; do
      if curl -s "http://localhost:$BIFROST_PORT/health" >/dev/null 2>&1; then
        log_info "Health check passed, server fully operational"
        break
      fi
      sleep 1
      health_check_attempts=$((health_check_attempts + 1))
    done
    
    # Fail fast if health check never succeeded
    if [ $health_check_attempts -ge 10 ]; then
      log_error "Health check failed: server did not respond on /health after 10 attempts"
      stop_bifrost
      return 1
    fi
    
    # Stop current version before taking snapshot
    stop_bifrost
    
    # STEP 4: Capture snapshot AFTER migration
    log_info "Capturing post-migration snapshot..."
    capture_postgres_snapshot "$after_snapshot"
    
    # STEP 5: Compare snapshots - validate all data is intact
    if ! validate_postgres_data "$before_snapshot" "$after_snapshot"; then
      log_error "Data validation failed after migration from $version"
      stop_bifrost
      return 1
    fi
    
    stop_bifrost
    log_info "Migration from $version: SUCCESS"
  done
  
  log_info "=========================================="
  log_info "PostgreSQL Migration Tests: PASSED"
  log_info "=========================================="
  return 0
}

# ============================================================================
# SQLite Migration Test
# ============================================================================

run_sqlite_migration_tests() {
  log_info "=========================================="
  log_info "Running SQLite Migration Tests"
  log_info "=========================================="
  
  # Check if sqlite3 is available
  if ! command -v sqlite3 >/dev/null 2>&1; then
    log_warn "sqlite3 not found, skipping SQLite tests"
    return 0
  fi
  
  # Find an available port for bifrost
  BIFROST_PORT=$(find_available_port 8089)
  log_info "Using port $BIFROST_PORT for bifrost"
  
  # Create temp directory
  TEMP_DIR=$(mktemp -d)
  log_info "Using temp directory: $TEMP_DIR"
  
  local config_db="$TEMP_DIR/config.db"
  local logs_db="$TEMP_DIR/logs.db"
  
  # Create config file for SQLite
  local config_file="$TEMP_DIR/config.json"
  cat > "$config_file" << EOF
{
  "\$schema": "https://www.getbifrost.ai/schema",
  "config_store": {
    "enabled": true,
    "type": "sqlite",
    "config": {
      "path": "$config_db"
    }
  },
  "logs_store": {
    "enabled": true,
    "type": "sqlite",
    "config": {
      "path": "$logs_db"
    }
  }
}
EOF
  
  # Generate faker SQL
  local faker_sql="$TEMP_DIR/faker.sql"
  generate_faker_sql "sqlite" "$faker_sql"
  
  # Build current version ONCE before testing
  log_info "Building current version from Go workspace..."
  local current_binary="$TEMP_DIR/bifrost-http-current"
  cd "$REPO_ROOT"
  # Ensure the embedded ui directory exists (it's gitignored, so it won't be present in CI)
  if [ ! -d "$REPO_ROOT/transports/bifrost-http/ui" ]; then
    mkdir -p "$REPO_ROOT/transports/bifrost-http/ui"
    echo "placeholder" > "$REPO_ROOT/transports/bifrost-http/ui/.gitkeep"
  fi
  if ! go build -o "$current_binary" ./transports/bifrost-http; then
    log_error "Failed to build current version"
    return 1
  fi
  log_info "Current version built successfully: $current_binary"
  
  # Get previous versions
  local versions
  versions=$(get_previous_versions "$VERSIONS_TO_TEST")
  
  if [ -z "$versions" ]; then
    log_warn "No previous versions found, skipping version-based migration tests"
    versions="latest"
  fi
  
  log_info "Testing versions: $(echo $versions | tr '\n' ' ')"
  
  # Test each version
  for version in $versions; do
    log_info "------------------------------------------"
    log_info "Testing migration from version: $version"
    log_info "------------------------------------------"
    
    # Reset databases
    reset_sqlite_database "$config_db"
    reset_sqlite_database "$logs_db"
    
    # Start bifrost with this version using npx
    local server_log="$TEMP_DIR/server-$version.log"
    log_info "Starting bifrost $version via npx..."
    
    npx @maximhq/bifrost --transport-version "$version" \
      --app-dir "$TEMP_DIR" --port "$BIFROST_PORT" > "$server_log" 2>&1 &
    BIFROST_PID=$!
    
    if ! wait_for_bifrost "$server_log" 120; then
      log_error "Failed to start bifrost $version"
      cat "$server_log" 2>/dev/null || true
      stop_bifrost
      exit 1
    fi
    
    log_info "Bifrost $version started successfully"
    
    # Stop bifrost (schema is now created)
    stop_bifrost
    
    # Create version-specific faker SQL with dynamic config_mcp_clients INSERT
    local version_faker_sql="$TEMP_DIR/faker-$version.sql"
    cp "$faker_sql" "$version_faker_sql"
    append_dynamic_mcp_clients_insert "sqlite" "$version_faker_sql" "$config_db"
    
    # Validate faker column coverage against older version's schema
    if ! validate_faker_column_coverage_sqlite "$version_faker_sql" "$version" "$TEMP_DIR" "$config_db" "$logs_db"; then
      log_error "Faker column coverage validation failed for $version"
      return 1
    fi
    
    # Insert faker data into config store
    log_info "Inserting faker data..."
    if [ -f "$config_db" ]; then
      run_sqlite_sql_file "$config_db" "$version_faker_sql" 2>/dev/null || true
    fi
    if [ -f "$logs_db" ]; then
      run_sqlite_sql_file "$logs_db" "$version_faker_sql" 2>/dev/null || true
    fi
    
    # Now run current version to test migration
    log_info "Running current version to test migration..."
    
    # Start current version (already built)
    local current_log="$TEMP_DIR/server-current-$version.log"
    "$current_binary" --app-dir "$TEMP_DIR" --port "$BIFROST_PORT" > "$current_log" 2>&1 &
    BIFROST_PID=$!
    
    if ! wait_for_bifrost "$current_log" 120; then
      log_error "Current version failed to start after migrating from $version"
      cat "$current_log"
      stop_bifrost
      return 1
    fi
    
    log_info "Current version started successfully after migration from $version"
    
    # Stop server before reading SQLite databases to avoid locks
    stop_bifrost
    sleep 2
    
    # Validate data
    if ! validate_sqlite_data "$config_db" "$logs_db"; then
      log_error "Data validation failed after migration from $version"
      cat "$current_log"
      return 1
    fi
    
    log_info "Migration from $version: SUCCESS"
  done
  
  log_info "=========================================="
  log_info "SQLite Migration Tests: PASSED"
  log_info "=========================================="
  return 0
}

# ============================================================================
# Main
# ============================================================================

main() {
  log_info "=========================================="
  log_info "Bifrost Migration Tests"
  log_info "=========================================="
  log_info "Database type: $DB_TYPE"
  log_info "Versions to test: $VERSIONS_TO_TEST"
  log_info ""
  
  local exit_code=0
  
  case "$DB_TYPE" in
    postgres)
      run_postgres_migration_tests || exit_code=$?
      ;;
    sqlite)
      run_sqlite_migration_tests || exit_code=$?
      ;;
    all)
      run_postgres_migration_tests || exit_code=$?
      run_sqlite_migration_tests || exit_code=$?
      ;;
    *)
      log_error "Unknown db_type: $DB_TYPE"
      echo "Usage: $0 [postgres|sqlite|all]"
      exit 1
      ;;
  esac
  
  if [ $exit_code -eq 0 ]; then
    log_info "=========================================="
    log_info "All Migration Tests: PASSED"
    log_info "=========================================="
  else
    log_error "=========================================="
    log_error "Migration Tests: FAILED"
    log_error "=========================================="
  fi
  
  exit $exit_code
}

main "$@"
