#!/usr/bin/env bash
#
# create_organizations.sh - seed a running LiteLLM instance with organizations.
#
# Usage:
#   LITELLM_URL=http://localhost:4000 LITELLM_MASTER_KEY=sk-1234 ./create_organizations.sh

set -euo pipefail

LITELLM_URL="${LITELLM_URL:-http://localhost:4000}"
LITELLM_MASTER_KEY="${LITELLM_MASTER_KEY:-sk-1234}"
DRY_RUN="${DRY_RUN:-0}"

create_org() {
  local description="$1" body="$2"
  echo "==> ${description}"
  if [[ "${DRY_RUN}" == "1" ]]; then
    jq -c . <<<"${body}" 2>/dev/null || { echo "  INVALID JSON"; return; }
    echo ""
    return
  fi
  curl -sS --fail-with-body \
    --location "${LITELLM_URL}/organization/new" \
    --header "Authorization: Bearer ${LITELLM_MASTER_KEY}" \
    --header "Content-Type: application/json" \
    --data "${body}" \
    && echo "" \
    || echo "  (LiteLLM rejected this payload)"
  echo ""
}

# --- Happy path: every field populated -------------------------------------
# Expected Bifrost customer: name "seed-full", budget {500, "1M"},
# rate_limit {token 2000 / "1m", request 120 / "1m"}.
create_org "seed-full: budget + tpm + rpm + monthly reset" '{
  "organization_alias": "seed-full",
  "max_budget": 500,
  "budget_duration": "1mo",
  "tpm_limit": 2000,
  "rpm_limit": 120
}'

# --- Budget only ------------------------------------------------------------
# Expected: budget {100, "30d"}, no rate_limit.
create_org "seed-budget-30d: budget only, 30d reset" '{
  "organization_alias": "seed-budget-30d",
  "max_budget": 100,
  "budget_duration": "30d"
}'

# Expected: budget {250, "1M"} (mo -> M), no rate_limit.
create_org "seed-budget-monthly: budget only, 1mo reset" '{
  "organization_alias": "seed-budget-monthly",
  "max_budget": 250,
  "budget_duration": "1mo"
}'

# --- LiteLLM UI "Reset Budget" dropdown values -----------------------------
# The UI shows daily/weekly/monthly but stores 24h/7d/30d. These migrate
# straight through - Bifrost accepts the same units.
# Expected: budget {10, "24h"}.
create_org "seed-reset-daily: UI \"daily\" -> 24h" '{
  "organization_alias": "seed-reset-daily",
  "max_budget": 10,
  "budget_duration": "24h"
}'

# Expected: budget {70, "7d"}.
create_org "seed-reset-weekly: UI \"weekly\" -> 7d" '{
  "organization_alias": "seed-reset-weekly",
  "max_budget": 70,
  "budget_duration": "7d"
}'

# Expected: budget {300, "30d"}.
create_org "seed-reset-monthly: UI \"monthly\" -> 30d" '{
  "organization_alias": "seed-reset-monthly",
  "max_budget": 300,
  "budget_duration": "30d"
}'

# --- Pure customer (no budget, no limits) ----------------------------------
# Expected: name only, no budget, no rate_limit.
create_org "seed-pure: no budget, no limits" '{
  "organization_alias": "seed-pure"
}'

# --- Rate limits only -------------------------------------------------------
# Expected: rate_limit {token 1000 / "1m"} only.
create_org "seed-tpm-only: tpm only" '{
  "organization_alias": "seed-tpm-only",
  "tpm_limit": 1000
}'

# Expected: rate_limit {request 60 / "1m"} only.
create_org "seed-rpm-only: rpm only" '{
  "organization_alias": "seed-rpm-only",
  "rpm_limit": 60
}'

# Expected: rate_limit {token 1500 / "1m", request 90 / "1m"}, no budget.
create_org "seed-tpm-rpm: tpm + rpm, no budget" '{
  "organization_alias": "seed-tpm-rpm",
  "tpm_limit": 1500,
  "rpm_limit": 90
}'

# Expected: rate_limit nil.
create_org "seed-tpm-negative: negative tpm" '{
  "organization_alias": "seed-tpm-negative",
  "tpm_limit": -100
}'

# Expected: rate_limit nil.
create_org "seed-tpm-zero: zero tpm" '{
  "organization_alias": "seed-tpm-zero",
  "tpm_limit": 0
}'

# Expected: no budget, no rate_limit.
create_org "seed-budget-zero: zero max_budget" '{
  "organization_alias": "seed-budget-zero",
  "max_budget": 0,
  "budget_duration": "30d"
}'

# Expected: no budget.
create_org "seed-budget-negative: negative max_budget" '{
  "organization_alias": "seed-budget-negative",
  "max_budget": -50,
  "budget_duration": "30d"
}'

# Expected: max_budget set with no reset window.
create_org "seed-budget-noduration: max_budget without budget_duration" '{
  "organization_alias": "seed-budget-noduration",
  "max_budget": 100
}'

echo "Seeding complete."
