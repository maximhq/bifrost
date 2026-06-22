#!/usr/bin/env bash
#
# delete_entities.sh — delete ALL entities from a running LiteLLM instance.
# Covers, in dependency order: virtual keys, teams, organizations, budgets,
# (DB-backed) models and credentials. Use this to reset a LiteLLM instance before
# re-running the org->customer migration.
#
# Deletion order matters: keys and teams belong to organizations, so they are
# removed first; budgets orphaned by org deletion are swept up afterwards.
#
# Usage:
#   LITELLM_URL=http://localhost:4000 LITELLM_MASTER_KEY=sk-1234 ./delete_entities.sh
#
#   DRY_RUN=1 ./delete_entities.sh          # list what would be deleted, delete nothing
#   DELETE_KEYS=0 ./delete_entities.sh      # skip virtual keys
#   DELETE_TEAMS=0 ./delete_entities.sh     # skip teams
#   DELETE_ORGS=0 ./delete_entities.sh      # skip organizations
#   DELETE_BUDGETS=0 ./delete_entities.sh   # skip budgets
#   DELETE_MODELS=0 ./delete_entities.sh    # skip models
#   DELETE_CREDENTIALS=0 ./delete_entities.sh # skip credentials
#
# Endpoints used (LiteLLM management API):
#   GET  {url}/key/list            POST   {url}/key/delete           (batch)
#   GET  {url}/team/list           POST   {url}/team/delete          (batch)
#   GET  {url}/organization/list   DELETE {url}/organization/delete  (batch)
#   GET  {url}/budget/list         POST   {url}/budget/delete        (one at a time)
#   GET  {url}/model/info          POST   {url}/model/delete         (one at a time)
#   GET  {url}/credentials         DELETE {url}/credentials/{name}   (one at a time)
#
# Note: only DB-backed models (model_info.db_model == true) can be deleted via
# the API; models defined in config.yaml are skipped.
#
# Requires: curl, jq.

set -euo pipefail

LITELLM_URL="${LITELLM_URL:-http://localhost:4000}"
LITELLM_MASTER_KEY="${LITELLM_MASTER_KEY:-sk-1234}"
DRY_RUN="${DRY_RUN:-0}"
DELETE_KEYS="${DELETE_KEYS:-1}"
DELETE_TEAMS="${DELETE_TEAMS:-1}"
DELETE_ORGS="${DELETE_ORGS:-1}"
DELETE_BUDGETS="${DELETE_BUDGETS:-1}"
DELETE_MODELS="${DELETE_MODELS:-1}"
DELETE_CREDENTIALS="${DELETE_CREDENTIALS:-1}"

AUTH_HEADER="Authorization: Bearer ${LITELLM_MASTER_KEY}"

# get issues an authenticated GET and prints the response body.
get() {
  curl -sS --fail-with-body --location "${LITELLM_URL}$1" --header "${AUTH_HEADER}"
}

# delete_all_keys collects every virtual key across all pages of /key/list and
# deletes them in a single batch /key/delete request.
delete_all_keys() {
  echo "==> Listing virtual keys"
  local page1 total_pages keys count
  page1="$(get "/key/list?page=1")"
  total_pages="$(jq -r '.total_pages // 1' <<<"${page1}")"

  keys="$(jq -c '[.keys[]? | if type == "string" then . else (.token // .key // .key_name // empty) end]' <<<"${page1}")"
  local p
  for ((p = 2; p <= total_pages; p++)); do
    keys="$(jq -c --argjson more "$(get "/key/list?page=${p}" | jq -c '[.keys[]? | if type == "string" then . else (.token // .key // .key_name // empty) end]')" '. + $more' <<<"${keys}")"
  done

  count="$(jq 'length' <<<"${keys}")"
  echo "    found ${count} virtual key(s)"
  if [[ "${count}" -eq 0 ]]; then
    return 0
  fi

  if [[ "${DRY_RUN}" == "1" ]]; then
    echo "    DRY-RUN would delete ${count} virtual key(s)"
    return 0
  fi

  echo "==> Deleting ${count} virtual key(s)"
  curl -sS --fail-with-body --request POST --location "${LITELLM_URL}/key/delete" \
    --header "${AUTH_HEADER}" \
    --header "Content-Type: application/json" \
    --data "{\"keys\": ${keys}}" >/dev/null \
    && echo "    OK deleted ${count} virtual key(s)" \
    || echo "    FAIL delete ${count} virtual key(s)"
}

# delete_all_teams enumerates every team and deletes them in a single batch
# /team/delete request. Null team ids (the implicit default team) are skipped.
delete_all_teams() {
  echo "==> Listing teams"
  local teams ids count
  teams="$(get "/team/list")"
  ids="$(jq -c '[.[].team_id | select(. != null)]' <<<"${teams}")"
  count="$(jq 'length' <<<"${ids}")"
  echo "    found ${count} team(s)"
  if [[ "${count}" -eq 0 ]]; then
    return 0
  fi

  jq -r '.[] | "    team \(.)"' <<<"${ids}"

  if [[ "${DRY_RUN}" == "1" ]]; then
    echo "    DRY-RUN would delete ${count} team(s)"
    return 0
  fi

  echo "==> Deleting ${count} team(s)"
  curl -sS --fail-with-body --request POST --location "${LITELLM_URL}/team/delete" \
    --header "${AUTH_HEADER}" \
    --header "Content-Type: application/json" \
    --data "{\"team_ids\": ${ids}}" >/dev/null \
    && echo "    OK deleted ${count} team(s)" \
    || echo "    FAIL delete ${count} team(s)"
}

# delete_all_organizations enumerates every organization and deletes them in a
# single batch /organization/delete request.
delete_all_organizations() {
  echo "==> Listing organizations"
  local orgs ids count
  orgs="$(get "/organization/list")"
  ids="$(jq -c '[.[].organization_id | select(. != null)]' <<<"${orgs}")"
  count="$(jq 'length' <<<"${ids}")"
  echo "    found ${count} organization(s)"
  if [[ "${count}" -eq 0 ]]; then
    return 0
  fi

  jq -r '.[] | "    org \(.organization_id) (\(.organization_alias // "-"))"' <<<"${orgs}"

  if [[ "${DRY_RUN}" == "1" ]]; then
    echo "    DRY-RUN would delete ${count} organization(s)"
    return 0
  fi

  echo "==> Deleting ${count} organization(s)"
  curl -sS --fail-with-body --request DELETE --location "${LITELLM_URL}/organization/delete" \
    --header "${AUTH_HEADER}" \
    --header "Content-Type: application/json" \
    --data "{\"organization_ids\": ${ids}}" >/dev/null \
    && echo "    OK deleted ${count} organization(s)" \
    || echo "    FAIL delete ${count} organization(s)"
}

# delete_all_budgets enumerates every budget and deletes them one at a time;
# LiteLLM exposes no batch delete for budgets.
delete_all_budgets() {
  echo "==> Listing budgets"
  local budgets count failed=0
  budgets="$(get "/budget/list")"
  count="$(jq 'length' <<<"${budgets}")"
  echo "    found ${count} budget(s)"
  if [[ "${count}" -eq 0 ]]; then
    return 0
  fi

  while IFS= read -r budget_id; do
    [[ -z "${budget_id}" ]] && continue
    if [[ "${DRY_RUN}" == "1" ]]; then
      echo "    DRY-RUN would delete budget ${budget_id}"
      continue
    fi
    if curl -sS --fail-with-body --request POST --location "${LITELLM_URL}/budget/delete" \
      --header "${AUTH_HEADER}" \
      --header "Content-Type: application/json" \
      --data "$(jq -n --arg id "${budget_id}" '{"id": $id}')" >/dev/null; then
      echo "    OK deleted budget ${budget_id}"
    else
      echo "    FAIL delete budget ${budget_id}"
      failed=$((failed + 1))
    fi
  done < <(jq -r '.[] | select(.budget_id != null) | .budget_id' <<<"${budgets}")

  if [[ "${failed}" -gt 0 ]]; then
    echo "    ${failed} budget(s) failed to delete"
    return 1
  fi
}

# delete_all_models enumerates DB-backed models and deletes them one at a time;
# models defined in config.yaml (db_model == false) cannot be deleted via the
# API and are skipped.
delete_all_models() {
  echo "==> Listing models"
  local models db_ids count skipped failed=0
  models="$(get "/model/info")"
  db_ids="$(jq -c '[.data[] | select(.model_info.db_model == true and .model_info.id != null) | .model_info.id]' <<<"${models}")"
  count="$(jq 'length' <<<"${db_ids}")"
  skipped="$(jq '[.data[] | select(.model_info.db_model != true)] | length' <<<"${models}")"
  echo "    found ${count} DB-backed model(s) (${skipped} config model(s) skipped)"
  if [[ "${count}" -eq 0 ]]; then
    return 0
  fi

  while IFS= read -r model_id; do
    [[ -z "${model_id}" ]] && continue
    if [[ "${DRY_RUN}" == "1" ]]; then
      echo "    DRY-RUN would delete model ${model_id}"
      continue
    fi
    if curl -sS --fail-with-body --request POST --location "${LITELLM_URL}/model/delete" \
      --header "${AUTH_HEADER}" \
      --header "Content-Type: application/json" \
      --data "$(jq -n --arg id "${model_id}" '{"id": $id}')" >/dev/null; then
      echo "    OK deleted model ${model_id}"
    else
      echo "    FAIL delete model ${model_id}"
      failed=$((failed + 1))
    fi
  done < <(jq -r '.[]' <<<"${db_ids}")

  if [[ "${failed}" -gt 0 ]]; then
    echo "    ${failed} model(s) failed to delete"
    return 1
  fi
}

# delete_all_credentials enumerates every stored credential and deletes them one
# at a time via DELETE /credentials/{credential_name}.
delete_all_credentials() {
  echo "==> Listing credentials"
  local credentials count failed=0
  credentials="$(get "/credentials")"
  count="$(jq '.credentials // [] | length' <<<"${credentials}")"
  echo "    found ${count} credential(s)"
  if [[ "${count}" -eq 0 ]]; then
    return 0
  fi

  while IFS=$'\t' read -r credential_name credential_path; do
    [[ -z "${credential_name}" ]] && continue
    if [[ "${DRY_RUN}" == "1" ]]; then
      echo "    DRY-RUN would delete credential ${credential_name}"
      continue
    fi
    if curl -sS --fail-with-body --request DELETE --location "${LITELLM_URL}/credentials/${credential_path}" \
      --header "${AUTH_HEADER}" >/dev/null; then
      echo "    OK deleted credential ${credential_name}"
    else
      echo "    FAIL delete credential ${credential_name}"
      failed=$((failed + 1))
    fi
  done < <(jq -r '.credentials // [] | .[] | select(.credential_name != null) | [.credential_name, (.credential_name | @uri)] | @tsv' <<<"${credentials}")

  if [[ "${failed}" -gt 0 ]]; then
    echo "    ${failed} credential(s) failed to delete"
    return 1
  fi
}

main() {
  if [[ "${DRY_RUN}" == "1" ]]; then
    echo "DRY-RUN mode: nothing will be deleted."
    echo
  fi

  [[ "${DELETE_KEYS}" == "1" ]] && { delete_all_keys || true; echo; }
  [[ "${DELETE_TEAMS}" == "1" ]] && { delete_all_teams || true; echo; }
  [[ "${DELETE_ORGS}" == "1" ]] && { delete_all_organizations || true; echo; }
  [[ "${DELETE_BUDGETS}" == "1" ]] && { delete_all_budgets || true; echo; }
  [[ "${DELETE_MODELS}" == "1" ]] && { delete_all_models || true; echo; }
  [[ "${DELETE_CREDENTIALS}" == "1" ]] && { delete_all_credentials || true; echo; }

  echo "Cleanup complete."
}

main "$@"
