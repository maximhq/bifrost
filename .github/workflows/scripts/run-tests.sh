#!/usr/bin/env bash
set -euo pipefail

# Comprehensive test runner for Bifrost PR validation
# This script runs all test suites to validate changes

echo "🧪 Starting Bifrost Test Suite..."
echo "=================================="

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Track test results
TESTS_PASSED=0
TESTS_FAILED=0

# Function to report test result
report_result() {
  local test_name=$1
  local result=$2
  
  if [ "$result" -eq 0 ]; then
    echo -e "${GREEN}✅ $test_name passed${NC}"
    ((TESTS_PASSED++))
  else
    echo -e "${RED}❌ $test_name failed${NC}"
    ((TESTS_FAILED++))
  fi
}

# 1. Core Build Validation
echo ""
echo "📦 1/5 - Validating Core Build..."
echo "-----------------------------------"
cd core
if go mod download && go build ./...; then
  report_result "Core Build" 0
else
  report_result "Core Build" 1
fi
cd ..

# 2. Build MCP Test Servers
echo ""
echo "🔌 2/5 - Building MCP Test Servers..."
echo "-----------------------------------"
MCP_BUILD_FAILED=0
for mcp_dir in examples/mcps/*/; do
  if [ -d "$mcp_dir" ]; then
    mcp_name=$(basename "$mcp_dir")
    if [ -f "$mcp_dir/go.mod" ]; then
      echo "  Building $mcp_name (Go)..."
      mkdir -p "$mcp_dir/bin"
      if cd "$mcp_dir" && GOWORK=off go build -o "bin/$mcp_name" . && cd - > /dev/null; then
        echo -e "  ${GREEN}✓ $mcp_name${NC}"
      else
        echo -e "  ${RED}✗ $mcp_name${NC}"
        MCP_BUILD_FAILED=1
        cd - > /dev/null 2>&1 || true
      fi
    elif [ -f "$mcp_dir/package.json" ]; then
      echo "  Building $mcp_name (TypeScript)..."
      # `npm ci`, not `npm install`: every examples/mcps/* TypeScript server commits a
      # package-lock.json, so ci installs that exact tree with integrity hashes enforced
      # instead of re-resolving transitive deps loosely at run time.
      if cd "$mcp_dir" && npm ci --silent && npm run build && cd - > /dev/null; then
        echo -e "  ${GREEN}✓ $mcp_name${NC}"
      else
        echo -e "  ${RED}✗ $mcp_name${NC}"
        MCP_BUILD_FAILED=1
        cd - > /dev/null 2>&1 || true
      fi
    fi
  fi
done
report_result "MCP Test Servers Build" $MCP_BUILD_FAILED

# 3. Core Provider Tests
echo ""
echo "🔧 3/5 - Running Core Provider Tests..."
echo "-----------------------------------"
cd core
if go test -v -run . ./...; then
  report_result "Core Provider Tests" 0
else
  report_result "Core Provider Tests" 1
fi
cd ..

# 4. Governance Tests
echo ""
echo "🛡️  4/5 - Running Governance Tests..."
echo "-----------------------------------"
# The governance suite is Go (tests/governance/go.mod), not Python. It was pytest once,
# and this block still built a venv and ran `pip install -r requirements.txt` against a
# file that no longer exists - which aborts the whole script under `set -e`.
if [ -d "tests/governance" ]; then
  cd tests/governance

  if go test ./... ; then
    report_result "Governance Tests" 0
  else
    report_result "Governance Tests" 1
  fi

  cd ../..
else
  echo -e "${YELLOW}⚠️  Governance tests directory not found, skipping...${NC}"
fi

# 5. Integration Tests
echo ""
echo "🔗 5/5 - Running Integration Tests..."
echo "-----------------------------------"
# The Python integration suite lives in tests/integrations/python and is uv-managed:
# `uv sync --frozen` installs exactly what uv.lock records, which both fixes the stale
# `pip install -r requirements.txt` (no such file) and pins the install by lockfile,
# matching how test-integrations.sh does it.
if [ -d "tests/integrations/python" ]; then
  cd tests/integrations/python

  echo "Installing Python dependencies from uv.lock..."
  uv sync --frozen

  # Run tests
  if uv run python run_all_tests.py; then
    report_result "Integration Tests" 0
  else
    report_result "Integration Tests" 1
  fi

  # No `deactivate` here: uv run manages its own env, and calling deactivate without an
  # activated venv is a command-not-found that would abort the script under `set -e`.
  cd ../../..
else
  echo -e "${YELLOW}⚠️  Integration tests directory not found, skipping...${NC}"
fi

# Final Summary
echo ""
echo "=================================="
echo "🏁 Test Suite Complete!"
echo "=================================="
echo -e "${GREEN}Passed: $TESTS_PASSED${NC}"
echo -e "${RED}Failed: $TESTS_FAILED${NC}"
echo ""

if [ "$TESTS_FAILED" -gt 0 ]; then
  echo -e "${RED}❌ Some tests failed. Please review the output above.${NC}"
  exit 1
else
  echo -e "${GREEN}✅ All tests passed successfully!${NC}"
  exit 0
fi

