#!/usr/bin/env bash
#
# create_virtual_keys.sh - seed a running LiteLLM instance with virtual keys
#
# Usage:
#   LITELLM_URL=http://localhost:4000 LITELLM_MASTER_KEY=sk-1234 ./create_virtual_keys.sh

set -euo pipefail

LITELLM_URL="${LITELLM_URL:-http://localhost:4000}"
LITELLM_MASTER_KEY="${LITELLM_MASTER_KEY:-sk-1234}"
DRY_RUN="${DRY_RUN:-0}"

# Stable ids reused across cases so the script is deterministic.
ORG_ID="seed-key-org"
USER_ID="seed-key-user"
TEAM_ID="seed-key-team"
BUDGET_ID="seed-key-budget"

# post sends one management-API call. Args: METHOD, PATH, DESCRIPTION, BODY.
# In DRY_RUN mode it prints the payload instead of sending it. Failures are
# logged and never abort the run (matches create_teams.sh).
post() {
  local method="$1" path="$2" description="$3" body="$4"
  echo "==> ${description}"
  if [[ "${DRY_RUN}" == "1" ]]; then
    jq -c . <<<"${body}" 2>/dev/null || { echo "  INVALID JSON"; return; }
    echo ""
    return
  fi
  curl -sS --fail-with-body \
    --location "${LITELLM_URL}${path}" \
    --header "Authorization: Bearer ${LITELLM_MASTER_KEY}" \
    --header "Content-Type: application/json" \
    --request "${method}" \
    --data "${body}" \
    && echo "" \
    || echo "  (LiteLLM rejected this payload — may be a duplicate id/key or an enterprise-only field)"
  echo ""
}

create_key() { post POST "/key/generate" "$1" "$2"; }

# ---------------------------------------------------------------------------
# Prerequisites: org, user, budget, and a permissive team for scoped keys.
# ---------------------------------------------------------------------------
echo "### Prerequisites ###"
echo

post POST "/organization/new" "prereq org: ${ORG_ID}" '{
  "organization_id": "'"${ORG_ID}"'",
  "organization_alias": "seed-key-org",
  "models": ["all-proxy-models"],
  "max_budget": 100000,
  "budget_duration": "30d"
}'

post POST "/user/new" "prereq user: ${USER_ID}" '{
  "user_id": "'"${USER_ID}"'",
  "user_email": "seed-key-user@example.com",
  "user_role": "internal_user",
  "auto_create_key": false
}'

post POST "/budget/new" "prereq budget: ${BUDGET_ID}" '{
  "budget_id": "'"${BUDGET_ID}"'",
  "max_budget": 250,
  "budget_duration": "30d",
  "tpm_limit": 5000,
  "rpm_limit": 300
}'

# Permissive team (empty models = all models allowed)
post POST "/team/new" "prereq team: ${TEAM_ID}" '{
  "team_id": "'"${TEAM_ID}"'",
  "team_alias": "seed-key-team",
  "organization_id": "'"${ORG_ID}"'"
}'

echo "### Standalone keys ###"
echo

# --- Case 1: minimal — auto-generated key, no params -----------------------
create_key "case01 minimal: auto key" '{}'

# --- Case 2: key_alias -----------------------------------------------------
create_key "case02 key_alias" '{
  "key_alias": "seed-key-alias"
}'

# --- Case 3: custom key value (NOT idempotent) -----------------------------
create_key "case03 custom key value" '{
  "key": "sk-seed-custom-0001",
  "key_alias": "seed-custom-value"
}'

# --- Case 4: expiry duration -----------------------------------------------
create_key "case04 expiry: duration 30d" '{
  "key_alias": "seed-expiry",
  "duration": "30d"
}'

# --- Case 5: model allowlist -----------------------------------------------
create_key "case05 models allowlist" '{
  "key_alias": "seed-models",
  "models": ["gpt-4o", "claude-3-5-sonnet", "text-embedding-3-small"]
}'

# --- Case 6: model aliases (upgrade/downgrade) -----------------------------
create_key "case06 model aliases" '{
  "key_alias": "seed-aliases",
  "models": ["gpt-4o"],
  "aliases": {"gpt-4": "gpt-4o", "fast": "gpt-4o"}
}'

# --- Case 7: budget — max + soft + duration --------------------------------
create_key "case07 budget: max + soft + duration" '{
  "key_alias": "seed-budget",
  "max_budget": 100,
  "soft_budget": 80,
  "budget_duration": "30d"
}'

# --- Case 8: rate limits — tpm + rpm + parallel ----------------------------
create_key "case08 rate limits: tpm + rpm + parallel" '{
  "key_alias": "seed-rate-limits",
  "tpm_limit": 2000,
  "rpm_limit": 120,
  "max_parallel_requests": 10
}'

# --- Case 9: rate-limit enforcement types ----------------------------------
create_key "case09 limit types" '{
  "key_alias": "seed-limit-types",
  "tpm_limit": 5000,
  "rpm_limit": 300,
  "tpm_limit_type": "guaranteed_throughput",
  "rpm_limit_type": "best_effort_throughput"
}'

# --- Case 10: per-model budgets --------------------------------------------
create_key "case10 model_max_budget" '{
  "key_alias": "seed-model-budgets",
  "model_max_budget": {"gpt-4o": {"budget_limit": 0.5, "time_period": "30d"}}
}'

# --- Case 11: per-model rpm/tpm limits -------------------------------------
create_key "case11 per-model rpm/tpm" '{
  "key_alias": "seed-per-model-limits",
  "model_rpm_limit": {"gpt-4o": 60, "claude-3-5-sonnet": 120},
  "model_tpm_limit": {"gpt-4o": 1000, "claude-3-5-sonnet": 2000}
}'

# --- Case 12: metadata -----------------------------------------------------
create_key "case12 metadata" '{
  "key_alias": "seed-metadata",
  "metadata": {"team": "core-infra", "app": "app2", "email": "owner@example.com"}
}'

# --- Case 13: tags (spend tracking / tag routing) --------------------------
create_key "case13 tags" '{
  "key_alias": "seed-tags",
  "tags": ["production", "team-platform", "tier-1"]
}'

# --- Case 14: permissions (pii controls) -----------------------------------
create_key "case14 permissions" '{
  "key_alias": "seed-permissions",
  "permissions": {"allow_pii_controls": true}
}'

# --- Case 15: blocked key --------------------------------------------------
create_key "case15 blocked" '{
  "key_alias": "seed-blocked",
  "blocked": true
}'

# --- Case 16: allowed_routes -----------------------------------------------
create_key "case16 allowed_routes" '{
  "key_alias": "seed-allowed-routes",
  "allowed_routes": ["/chat/completions", "/embeddings"]
}'

# --- Case 17: allowed_cache_controls ---------------------------------------
create_key "case17 allowed_cache_controls" '{
  "key_alias": "seed-cache-controls",
  "allowed_cache_controls": ["no-cache", "no-store"]
}'

# --- Case 18: key_type — read_only -----------------------------------------
create_key "case18 key_type: read_only" '{
  "key_alias": "seed-read-only",
  "key_type": "read_only"
}'

# --- Case 19: key_type — management ----------------------------------------
create_key "case19 key_type: management" '{
  "key_alias": "seed-management",
  "key_type": "management"
}'

# --- Case 20: config override ----------------------------------------------
create_key "case20 config override" '{
  "key_alias": "seed-config",
  "config": {"num_retries": 3}
}'

echo "### Scoped keys (team / user / org / budget) ###"
echo

# --- Case 21: scoped to a team ---------------------------------------------
create_key "case21 team-scoped" '{
  "key_alias": "seed-team-scoped",
  "team_id": "'"${TEAM_ID}"'"
}'

# --- Case 22: scoped to a user ---------------------------------------------
create_key "case22 user-scoped" '{
  "key_alias": "seed-user-scoped",
  "user_id": "'"${USER_ID}"'"
}'

# --- Case 23: scoped to an organization ------------------------------------
create_key "case23 org-scoped" '{
  "key_alias": "seed-org-scoped",
  "organization_id": "'"${ORG_ID}"'"
}'

# --- Case 24: linked to a shared budget ------------------------------------
create_key "case24 budget_id-linked" '{
  "key_alias": "seed-budget-linked",
  "budget_id": "'"${BUDGET_ID}"'"
}'

# --- Case 25: team + user together -----------------------------------------
create_key "case25 team + user" '{
  "key_alias": "seed-team-user",
  "team_id": "'"${TEAM_ID}"'",
  "user_id": "'"${USER_ID}"'"
}'

echo "### Enterprise-only cases (may be rejected on OSS builds) ###"
echo

# --- Case 26: guardrails ---------------------------------------------------
create_key "case26 guardrails (enterprise)" '{
  "key_alias": "seed-guardrails",
  "guardrails": ["my-pii-guard", "my-prompt-injection-guard"]
}'

# --- Case 27: object_permission — vector stores ----------------------------
create_key "case27 object_permission (enterprise)" '{
  "key_alias": "seed-object-permission",
  "object_permission": {"vector_stores": ["vector_store_1", "vector_store_2"]}
}'

# --- Case 28: enforced_params ----------------------------------------------
create_key "case28 enforced_params (enterprise)" '{
  "key_alias": "seed-enforced-params",
  "enforced_params": ["user", "metadata.generation_name"]
}'

# --- Case 29: concurrent budget windows ------------------------------------
create_key "case29 budget_limits windows (enterprise)" '{
  "key_alias": "seed-budget-windows",
  "budget_limits": [
    {"budget_limit": 10.0, "time_period": "1d"},
    {"budget_limit": 50.0, "time_period": "7d"}
  ]
}'

# --- Case 30: auto-rotation ------------------------------------------------
create_key "case30 auto_rotate (enterprise)" '{
  "key_alias": "seed-auto-rotate",
  "auto_rotate": true,
  "rotation_interval": "30d"
}'

echo "### Kitchen sink ###"
echo

# --- Case 31: everything together ------------------------------------------
create_key "case31 kitchen sink" '{
  "key_alias": "seed-kitchen-sink",
  "team_id": "'"${TEAM_ID}"'",
  "user_id": "'"${USER_ID}"'",
  "models": ["gpt-4o", "claude-3-5-sonnet"],
  "aliases": {"gpt-4": "gpt-4o"},
  "max_budget": 200,
  "soft_budget": 150,
  "budget_duration": "30d",
  "duration": "90d",
  "tpm_limit": 4000,
  "rpm_limit": 200,
  "max_parallel_requests": 20,
  "model_rpm_limit": {"gpt-4o": 100},
  "tags": ["production", "kitchen-sink"],
  "metadata": {"owner": "platform", "tier": "premium"},
  "permissions": {"allow_pii_controls": true}
}'

echo "Seeding complete."
