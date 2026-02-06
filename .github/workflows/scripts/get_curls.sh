#!/bin/bash
set -uo pipefail

# Bifrost HTTP Transport - GET API Endpoints
# This script tests all GET endpoints and reports their status

# Base URL (update as needed)
BASE_URL="${BASE_URL:-http://localhost:8080}"

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
NC='\033[0m' # No Color

# Track failures
FAILED_TESTS=0
TOTAL_TESTS=0

echo "Bifrost GET API Endpoints - Status Check"
echo "========================================"
echo "Base URL: $BASE_URL"
echo ""

# Function to test endpoint
test_endpoint() {
  local path=$1
  TOTAL_TESTS=$((TOTAL_TESTS + 1))
  local status=$(curl -s -o /dev/null -w "%{http_code}" -X GET "$BASE_URL$path" -H "Content-Type: application/json")
  
  if [ "$status" -ge 200 ] && [ "$status" -lt 300 ]; then
    echo -e "GET $path - ${GREEN}✓ SUCCESS${NC} ($status)"
  else
    echo -e "GET $path - ${RED}✗ FAILURE${NC} ($status)"
    FAILED_TESTS=$((FAILED_TESTS + 1))
  fi
}

# Test all endpoints
test_endpoint "/health"
test_endpoint "/api/session/is-auth-enabled"
test_endpoint "/api/plugins"
test_endpoint "/api/plugins/telemetry"
test_endpoint "/api/mcp/clients"
test_endpoint "/api/logs?limit=10&offset=0&sort_by=timestamp&order=desc"
test_endpoint "/api/logs/dropped"
test_endpoint "/api/logs/filterdata"
test_endpoint "/api/providers"
test_endpoint "/api/providers/openai"
test_endpoint "/api/keys"
test_endpoint "/api/governance/virtual-keys"
test_endpoint "/api/governance/virtual-keys/vk-123"
test_endpoint "/api/governance/teams"
test_endpoint "/api/governance/teams/team-123"
test_endpoint "/api/governance/customers"
test_endpoint "/api/governance/customers/cust-123"
test_endpoint "/api/config"
test_endpoint "/api/config?from_db=true"
test_endpoint "/api/version"
test_endpoint "/v1/models"

echo ""
echo -e "${YELLOW}Note: WebSocket endpoint (/ws) requires a WebSocket client${NC}"
echo ""
echo "========================================"
echo "Test Summary:"
echo "  Total tests: $TOTAL_TESTS"
echo "  Passed: $((TOTAL_TESTS - FAILED_TESTS))"
echo "  Failed: $FAILED_TESTS"
echo "========================================"

echo "The aim of the script is to make sure bifrost server is not crashing"