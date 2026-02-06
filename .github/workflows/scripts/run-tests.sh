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
      if cd "$mcp_dir" && npm install --silent && npm run build && cd - > /dev/null; then
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
if [ -d "tests/governance" ]; then
  cd tests/governance
  
  # Check if virtual environment exists, create if not
  if [ ! -d "venv" ]; then
    echo "Creating Python virtual environment..."
    python3 -m venv venv
  fi
  
  # Activate virtual environment
  source venv/bin/activate
  
  # Install dependencies
  echo "Installing Python dependencies..."
  pip install -q -r requirements.txt
  
  # Run tests
  if pytest -v; then
    report_result "Governance Tests" 0
  else
    report_result "Governance Tests" 1
  fi
  
  deactivate
  cd ../..
else
  echo -e "${YELLOW}⚠️  Governance tests directory not found, skipping...${NC}"
fi

# 5. Integration Tests
echo ""
echo "🔗 5/5 - Running Integration Tests..."
echo "-----------------------------------"
if [ -d "tests/integrations" ]; then
  cd tests/integrations
  
  # Check if virtual environment exists, create if not
  if [ ! -d "venv" ]; then
    echo "Creating Python virtual environment..."
    python3 -m venv venv
  fi
  
  # Activate virtual environment
  source venv/bin/activate
  
  # Install dependencies
  echo "Installing Python dependencies..."
  pip install -q -r requirements.txt
  
  # Run tests
  if python run_all_tests.py; then
    report_result "Integration Tests" 0
  else
    report_result "Integration Tests" 1
  fi
  
  deactivate
  cd ../..
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

