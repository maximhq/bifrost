#!/bin/bash

# Bifrost All Integration Tests Runner
# This script runs all integration test suites sequentially and aggregates results

set -e
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Print banner
echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}Bifrost All Integration Tests Runner${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""


# Parse command line arguments
ARGS=()
while [[ $# -gt 0 ]]; do
    case $1 in
        --help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --verbose           Show detailed output"
            echo "  --html              Generate HTML reports"
            echo "  --json              Generate JSON reports"
            echo "  --all-reports       Generate all report types"
            echo "  --env <provider>    Run tests with specific provider only"
            echo "  --help              Show this help message"
            echo ""
            echo "This script runs all integration test collections:"
            echo "  1. OpenAI Integration"
            echo "  2. Anthropic Integration"
            echo "  3. Bedrock Integration"
            echo "  4. Composite Integrations (GenAI, Cohere, LiteLLM, LangChain, PydanticAI)"
            echo ""
            echo "Examples:"
            echo "  $0                                    # Run all tests for all providers"
            echo "  $0 --env openai                       # Run all tests with OpenAI provider only"
            echo "  $0 --html --verbose                   # Verbose with HTML reports"
            exit 0
            ;;
        *)
            ARGS+=("$1")
            shift
            ;;
    esac
done

# Test scripts
TEST_SCRIPTS=(
    "run-newman-openai-integration.sh"
    "run-newman-anthropic-integration.sh"
    "run-newman-bedrock-integration.sh"
    "run-newman-composite-integration.sh"
)

# Test names for display
TEST_NAMES=(
    "OpenAI Integration"
    "Anthropic Integration"
    "Bedrock Integration"
    "Composite Integrations"
)

# Track results
FAILED_TESTS=()
PASSED_COUNT=0
FAILED_COUNT=0

echo -e "${GREEN}Running ${#TEST_SCRIPTS[@]} integration test suites...${NC}"
echo ""

# Run each test suite
for i in "${!TEST_SCRIPTS[@]}"; do
    script="${TEST_SCRIPTS[$i]}"
    name="${TEST_NAMES[$i]}"

    echo -e "${BLUE}========================================${NC}"
    echo -e "${BLUE}[$((i+1))/${#TEST_SCRIPTS[@]}] Running ${name}${NC}"
    echo -e "${BLUE}========================================${NC}"
    echo ""

    script_path="$SCRIPT_DIR/$script"
    if [ -f "$script_path" ]; then
        if (cd "$SCRIPT_DIR" && "./$script" "${ARGS[@]}"); then
            echo ""
            echo -e "${GREEN}✓ ${name} PASSED${NC}"
            PASSED_COUNT=$((PASSED_COUNT + 1))
        else
            echo ""
            echo -e "${RED}✗ ${name} FAILED${NC}"
            FAILED_TESTS+=("$name")
            FAILED_COUNT=$((FAILED_COUNT + 1))
        fi
    else
        echo -e "${RED}Error: Test script not found: $script_path${NC}"
        FAILED_TESTS+=("$name (script not found)")
        FAILED_COUNT=$((FAILED_COUNT + 1))
    fi

    echo ""
done

# Print summary
echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}Test Summary${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""
echo -e "Total test suites: ${#TEST_SCRIPTS[@]}"
echo -e "${GREEN}Passed: ${PASSED_COUNT}${NC}"
echo -e "${RED}Failed: ${FAILED_COUNT}${NC}"
echo ""

if [ ${FAILED_COUNT} -eq 0 ]; then
    echo -e "${GREEN}✓ All integration test suites passed!${NC}"
    exit 0
else
    echo -e "${RED}✗ The following test suites failed:${NC}"
    for test in "${FAILED_TESTS[@]}"; do
        echo -e "  ${RED}- ${test}${NC}"
    done
    echo ""
    echo -e "${YELLOW}Check individual test reports in newman-reports/ directories${NC}"
    exit 1
fi
