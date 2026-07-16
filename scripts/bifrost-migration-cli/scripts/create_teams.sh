#!/usr/bin/env bash
#
# create_teams.sh - seed a running LiteLLM instance with teams.
#
# Usage:
#   LITELLM_URL=http://localhost:4000 LITELLM_MASTER_KEY=sk-1234 ./create_teams.sh

set -euo pipefail

LITELLM_URL="${LITELLM_URL:-http://localhost:4000}"
LITELLM_MASTER_KEY="${LITELLM_MASTER_KEY:-sk-1234}"
DRY_RUN="${DRY_RUN:-0}"

# Stable ids reused across cases so the script is deterministic.
ORG_ID="seed-team-org"
ORG_ID_RESTRICTED="seed-team-org-restricted"
USER_ADMIN="seed-team-user-admin"
USER_MEMBER="seed-team-user-member"

# post sends one management-API call. Args: METHOD, PATH, DESCRIPTION, BODY.
# In DRY_RUN mode it prints the payload instead of sending it.
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
    || echo "  (LiteLLM rejected this payload — may be a duplicate id or an enterprise-only field)"
  echo ""
}

create_org() { post POST "/organization/new" "$1" "$2"; }
create_team() { post POST "/team/new" "$1" "$2"; }
create_user() { post POST "/user/new" "$1" "$2"; }

# ---------------------------------------------------------------------------
# Prerequisites: a permissive parent org and two users for membership cases.
# ---------------------------------------------------------------------------
echo "### Prerequisites ###"
echo

create_org "prereq org: ${ORG_ID}" '{
  "organization_id": "'"${ORG_ID}"'",
  "organization_alias": "seed-team-org",
  "models": ["all-proxy-models"],
  "max_budget": 100000,
  "budget_duration": "30d",
  "tpm_limit": 100000000,
  "rpm_limit": 1000000
}'

# A second org that exposes only a subset of proxy models (not all-proxy-models).
# Teams created inside it can only grant access to models within this allowlist.
create_org "prereq org (restricted models): ${ORG_ID_RESTRICTED}" '{
  "organization_id": "'"${ORG_ID_RESTRICTED}"'",
  "organization_alias": "seed-team-org-restricted",
  "models": ["openai-gpt-4o", "openai-gpt-4o-mini", "anthropic-claude-sonnet-4"],
  "max_budget": 100000,
  "budget_duration": "30d",
  "tpm_limit": 100000000,
  "rpm_limit": 1000000
}'

create_user "prereq user: ${USER_ADMIN}" '{
  "user_id": "'"${USER_ADMIN}"'",
  "user_email": "seed-team-admin@example.com",
  "user_role": "internal_user",
  "auto_create_key": false
}'

create_user "prereq user: ${USER_MEMBER}" '{
  "user_id": "'"${USER_MEMBER}"'",
  "user_email": "seed-team-member@example.com",
  "user_role": "internal_user",
  "auto_create_key": false
}'

echo "### Standalone teams (no organization) ###"
echo

# --- Case 1: minimal — alias only -----------------------------------------
create_team "case01 minimal: alias only" '{
  "team_alias": "seed-minimal"
}'

# --- Case 2: explicit team_id ----------------------------------------------
create_team "case02 explicit team_id" '{
  "team_id": "seed-team-explicit-id",
  "team_alias": "seed-explicit-id"
}'

# --- Case 3: members_with_roles (admin + user by user_id) ------------------
create_team "case03 members_with_roles (by user_id)" '{
  "team_alias": "seed-members-by-id",
  "members_with_roles": [
    {"role": "admin", "user_id": "'"${USER_ADMIN}"'"},
    {"role": "user", "user_id": "'"${USER_MEMBER}"'"}
  ]
}'

# --- Case 4: member by user_email (auto-creates the user if missing) --------
create_team "case04 member by user_email" '{
  "team_alias": "seed-members-by-email",
  "members_with_roles": [
    {"role": "admin", "user_email": "seed-team-admin@example.com"}
  ]
}'

# --- Case 5: budget — max + soft + duration --------------------------------
create_team "case05 budget: max + soft + duration" '{
  "team_alias": "seed-budget",
  "max_budget": 500,
  "soft_budget": 400,
  "budget_duration": "30d"
}'

# --- Case 6: rate limits — tpm + rpm ---------------------------------------
create_team "case06 rate limits: tpm + rpm" '{
  "team_alias": "seed-rate-limits",
  "tpm_limit": 2000,
  "rpm_limit": 120
}'

# --- Case 7: rate-limit enforcement types ----------------------------------
create_team "case07 limit types: guaranteed_throughput" '{
  "team_alias": "seed-limit-types",
  "tpm_limit": 5000,
  "rpm_limit": 300,
  "tpm_limit_type": "guaranteed_throughput",
  "rpm_limit_type": "best_effort_throughput"
}'

# --- Case 8: per-model tpm/rpm limits --------------------------------------
create_team "case08 per-model tpm/rpm limits" '{
  "team_alias": "seed-per-model-limits",
  "model_tpm_limit": {"gpt-4o": 1000, "claude-3-5-sonnet": 2000},
  "model_rpm_limit": {"gpt-4o": 60, "claude-3-5-sonnet": 120}
}'

# --- Case 9: model access restriction --------------------------------------
create_team "case09 models restriction list" '{
  "team_alias": "seed-models-allowlist",
  "models": ["gpt-4o", "claude-3-5-sonnet", "text-embedding-3-small"]
}'

# --- Case 10: model aliases (team-based routing) ---------------------------
create_team "case10 model_aliases" '{
  "team_alias": "seed-model-aliases",
  "models": ["gpt-4o"],
  "model_aliases": {"gpt-4": "gpt-4o", "fast": "gpt-4o"}
}'

# --- Case 11: metadata ------------------------------------------------------
create_team "case11 metadata" '{
  "team_alias": "seed-metadata",
  "metadata": {"department": "platform", "cost_center": "CC-1234", "extra_info": "seed"}
}'

# --- Case 12: tags (spend tracking / tag routing) --------------------------
create_team "case12 tags" '{
  "team_alias": "seed-tags",
  "tags": ["production", "team-platform", "tier-1"]
}'

# --- Case 13: blocked team --------------------------------------------------
create_team "case13 blocked team" '{
  "team_alias": "seed-blocked",
  "blocked": true
}'

# --- Case 14: per-team-member controls -------------------------------------
create_team "case14 per-member budget/limits/key-duration" '{
  "team_alias": "seed-member-controls",
  "team_member_budget": 50,
  "team_member_budget_duration": "30d",
  "team_member_rpm_limit": 30,
  "team_member_tpm_limit": 10000,
  "team_member_key_duration": "7d"
}'

# --- Case 15: concurrent budget windows (enterprise) -----------------------
create_team "case15 budget_limits: multiple windows (enterprise)" '{
  "team_alias": "seed-budget-windows",
  "budget_limits": [
    {"budget_limit": 10.0, "time_period": "1d"},
    {"budget_limit": 50.0, "time_period": "7d"}
  ]
}'

# --- Case 16: team_member_permissions (enterprise) -------------------------
create_team "case16 team_member_permissions (enterprise)" '{
  "team_alias": "seed-member-permissions",
  "team_member_permissions": ["/key/generate", "/key/update", "/key/delete"]
}'

# --- Case 17: object_permission — vector stores (enterprise) ---------------
create_team "case17 object_permission: vector_stores (enterprise)" '{
  "team_alias": "seed-object-permission",
  "object_permission": {"vector_stores": ["vector_store_1", "vector_store_2"]}
}'

# --- Case 18: guardrails (enterprise) --------------------------------------
create_team "case18 guardrails (enterprise)" '{
  "team_alias": "seed-guardrails",
  "guardrails": ["my-pii-guard", "my-prompt-injection-guard"]
}'

# --- Case 19: default models for new members -------------------------------
create_team "case19 default_team_member_models" '{
  "team_alias": "seed-default-member-models",
  "models": ["gpt-4o", "claude-3-5-sonnet"],
  "default_team_member_models": ["gpt-4o"]
}'

# --- Case 20: enforced file/batch expiry -----------------------------------
create_team "case20 enforced file/batch expiry" '{
  "team_alias": "seed-file-expiry",
  "enforced_file_expires_after": {"anchor": "created_at", "days": 30},
  "enforced_batch_output_expires_after": {"anchor": "created_at", "days": 7}
}'

echo "### Teams inside the organization (${ORG_ID}) ###"
echo

# --- Case 21: minimal team in org ------------------------------------------
create_team "case21 in-org: minimal" '{
  "team_alias": "seed-org-minimal",
  "organization_id": "'"${ORG_ID}"'"
}'

# --- Case 22: in-org team with members (auto-added to the org) -------------
create_team "case22 in-org: with members (auto-added to org)" '{
  "team_alias": "seed-org-members",
  "organization_id": "'"${ORG_ID}"'",
  "members_with_roles": [
    {"role": "admin", "user_id": "'"${USER_ADMIN}"'"},
    {"role": "user", "user_id": "'"${USER_MEMBER}"'"}
  ]
}'

# --- Case 23: in-org team with budget within the org's budget --------------
create_team "case23 in-org: budget within org limits" '{
  "team_alias": "seed-org-budget",
  "organization_id": "'"${ORG_ID}"'",
  "max_budget": 1000,
  "budget_duration": "30d",
  "tpm_limit": 50000,
  "rpm_limit": 500
}'

# --- Case 24: in-org kitchen sink ------------------------------------------
create_team "case24 in-org: kitchen sink" '{
  "team_id": "seed-org-kitchen-sink",
  "team_alias": "seed-org-kitchen-sink",
  "organization_id": "'"${ORG_ID}"'",
  "members_with_roles": [
    {"role": "admin", "user_id": "'"${USER_ADMIN}"'"},
    {"role": "user", "user_id": "'"${USER_MEMBER}"'"}
  ],
  "models": ["gpt-4o", "claude-3-5-sonnet"],
  "model_aliases": {"gpt-4": "gpt-4o"},
  "max_budget": 2000,
  "soft_budget": 1500,
  "budget_duration": "30d",
  "tpm_limit": 40000,
  "rpm_limit": 400,
  "team_member_budget": 100,
  "team_member_budget_duration": "30d",
  "tags": ["production", "org-team"],
  "metadata": {"department": "research", "tier": "premium"}
}'

echo "### Teams inside the restricted-models organization (${ORG_ID_RESTRICTED}) ###"
echo

# --- Case 25: in-restricted-org team, no models (inherits org's subset) -----
# The team grants no explicit models, so members are bounded by the org's
# allowlist (openai-gpt-4o, openai-gpt-4o-mini, anthropic-claude-sonnet-4).
create_team "case25 in-restricted-org: inherits org model subset" '{
  "team_alias": "seed-restricted-org-inherit",
  "organization_id": "'"${ORG_ID_RESTRICTED}"'"
}'

# --- Case 26: in-restricted-org team with an allowed-models subset ----------
# The team narrows access further to a subset of the org's allowlist.
create_team "case26 in-restricted-org: team model subset" '{
  "team_alias": "seed-restricted-org-subset",
  "organization_id": "'"${ORG_ID_RESTRICTED}"'",
  "models": ["openai-gpt-4o", "anthropic-claude-sonnet-4"]
}'

# --- Case 27: in-restricted-org kitchen sink with model subset -------------
create_team "case27 in-restricted-org: subset + aliases + members" '{
  "team_id": "seed-restricted-org-kitchen-sink",
  "team_alias": "seed-restricted-org-kitchen-sink",
  "organization_id": "'"${ORG_ID_RESTRICTED}"'",
  "members_with_roles": [
    {"role": "admin", "user_id": "'"${USER_ADMIN}"'"},
    {"role": "user", "user_id": "'"${USER_MEMBER}"'"}
  ],
  "models": ["openai-gpt-4o", "openai-gpt-4o-mini"],
  "model_aliases": {"gpt-4": "openai-gpt-4o", "fast": "openai-gpt-4o-mini"},
  "default_team_member_models": ["openai-gpt-4o-mini"],
  "max_budget": 1000,
  "budget_duration": "30d",
  "tags": ["production", "restricted-org-team"],
  "metadata": {"department": "platform", "tier": "restricted"}
}'

echo "Seeding complete."
