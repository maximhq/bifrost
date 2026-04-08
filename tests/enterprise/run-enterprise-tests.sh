#!/usr/bin/env bash
# =============================================================================
# run-enterprise-tests.sh
# Runner for Bifrost Enterprise integration test suite (TC-001 → TC-013+).
#
# Usage:
#   ./tests/enterprise/run-enterprise-tests.sh [SUITE] [FLAGS...]
#
# Examples:
#   ./tests/enterprise/run-enterprise-tests.sh            # all suites
#   ./tests/enterprise/run-enterprise-tests.sh license    # TC-013 only
#   ./tests/enterprise/run-enterprise-tests.sh rbac -v    # TC-004, verbose
#   ./tests/enterprise/run-enterprise-tests.sh inference  # TC-001
#   ./tests/enterprise/run-enterprise-tests.sh audit      # TC-005
#   ./tests/enterprise/run-enterprise-tests.sh guardrails # TC-006
#   ./tests/enterprise/run-enterprise-tests.sh vault      # TC-012
#   ./tests/enterprise/run-enterprise-tests.sh cluster    # TC-010 cluster
#   ./tests/enterprise/run-enterprise-tests.sh payload    # TC-010 payload
#
# Required env vars (see README):
#   BIFROST_BASE_URL       - default: http://localhost:8080
#   SUPER_ADMIN_TOKEN      - seeded super_admin session token
#   ADMIN_TOKEN            - seeded admin session token
#   OPERATOR_TOKEN         - seeded operator session token
#   VIEWER_TOKEN           - seeded viewer session token
#   API_USER_TOKEN         - seeded api_user session token
#
# Optional:
#   ENTERPRISE_LICENSE_JWT - valid enterprise JWT (skip-proof community tests if unset)
#   VK_MODEL_RESTRICTED    - virtual key that only allows gpt-4o-mini
#   VK_TIGHT_BUDGET        - virtual key with exhausted budget
#   TIMEOUT                - go test timeout (default: 5m)
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
TEST_DIR="${REPO_ROOT}/tests/enterprise"

TIMEOUT="${TIMEOUT:-5m}"
SUITE="${1:-all}"; shift 2>/dev/null || true
EXTRA_FLAGS="$*"

# ─── Map suite name → test name pattern ──────────────────────────────────────
case "$SUITE" in
  all)         PATTERN="." ;;
  license)     PATTERN="TestLicense" ;;
  rbac)        PATTERN="TestRBAC" ;;
  audit)       PATTERN="TestAudit" ;;
  guardrails)  PATTERN="TestGuardrails" ;;
  vault)       PATTERN="TestVault" ;;
  cluster)     PATTERN="TestCluster" ;;
  payload)     PATTERN="TestPayload" ;;
  inference)   PATTERN="TestInference" ;;
  *)           PATTERN="$SUITE" ;;  # allow raw pattern
esac

echo "=============================================="
echo "  Bifrost Enterprise Integration Tests"
echo "  Target: ${BIFROST_BASE_URL:-http://localhost:8080}"
echo "  Suite:  ${SUITE} (pattern: ${PATTERN})"
echo "  Timeout: ${TIMEOUT}"
echo "=============================================="
echo ""

# ─── Check Bifrost is reachable ──────────────────────────────────────────────
TARGET="${BIFROST_BASE_URL:-http://localhost:8080}"
if ! curl -sf "${TARGET}/health" > /dev/null 2>&1; then
  echo "⚠️  WARNING: Bifrost unreachable at ${TARGET}"
  echo "   Some tests will be skipped automatically."
  echo ""
fi

# ─── Run tests ───────────────────────────────────────────────────────────────
cd "${TEST_DIR}"

GOWORK=off go test \
  -v \
  -timeout "${TIMEOUT}" \
  -run "${PATTERN}" \
  ${EXTRA_FLAGS:-} \
  ./...

echo ""
echo "✅ Enterprise test run complete."
