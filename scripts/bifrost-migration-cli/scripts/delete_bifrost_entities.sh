#!/usr/bin/env bash
#
# delete_bifrost_entities.sh — delete all migration entities from a running Bifrost
# instance: virtual keys, model configs, teams, customers, users, provider keys 
# and providers.
#
# Deletion order matters: virtual keys belong to teams/customers/users, model
# configs own budgets/rate limits, teams belong to customers, and provider keys
# belong to providers, so children are removed first. Deleting a user cascades
# its own governance settings, team memberships and access profiles.
#
# Usage:
#   BIFROST_URL=http://localhost:8080 BIFROST_API_KEY=<token> ./delete_bifrost_entities.sh
#
#   DRY_RUN=1 ./delete_bifrost_entities.sh              # list what would be deleted, delete nothing
#   DELETE_VKS=0 ./delete_bifrost_entities.sh           # skip virtual keys (default: 1)
#   DELETE_MODEL_CONFIGS=0 ./delete_bifrost_entities.sh # skip model configs
#   DELETE_TEAMS=0 ./delete_bifrost_entities.sh        # skip teams
#   DELETE_CUSTOMERS=0 ./delete_bifrost_entities.sh     # skip customers
#   DELETE_USERS=0 ./delete_bifrost_entities.sh         # skip users
#   DELETE_KEYS=0 ./delete_bifrost_entities.sh          # skip provider keys
#   DELETE_PROVIDERS=0 ./delete_bifrost_entities.sh     # skip providers
#
# Endpoints used (Bifrost management API, all require a management bearer token):
#   GET    {url}/api/governance/virtual-keys        DELETE {url}/api/governance/virtual-keys/{id}
#   GET    {url}/api/governance/model-configs       DELETE {url}/api/governance/model-configs/{id}
#   GET    {url}/api/governance/teams               DELETE {url}/api/governance/teams/{id}
#   GET    {url}/api/governance/customers           DELETE {url}/api/governance/customers/{id}
#   GET    {url}/api/users                          DELETE {url}/api/users/{id}
#   GET    {url}/api/providers                      DELETE {url}/api/providers/{provider}
#   GET    {url}/api/providers/{provider}/keys      DELETE {url}/api/providers/{provider}/keys/{key_id}
#
# Note: Bifrost exposes no batch delete for these entities, so each row is
# deleted individually. Deleting your own user is rejected by the API; such a
# failure is logged and counted, and does not abort the run.
#
# Requires: curl, jq.

set -euo pipefail

BIFROST_URL="${BIFROST_URL:-http://localhost:8080}"
BIFROST_API_KEY="${BIFROST_API_KEY:-}"
DRY_RUN="${DRY_RUN:-0}"
DELETE_VKS="${DELETE_VKS:-1}"
DELETE_MODEL_CONFIGS="${DELETE_MODEL_CONFIGS:-1}"
DELETE_TEAMS="${DELETE_TEAMS:-1}"
DELETE_CUSTOMERS="${DELETE_CUSTOMERS:-1}"
DELETE_USERS="${DELETE_USERS:-1}"
DELETE_KEYS="${DELETE_KEYS:-1}"
DELETE_PROVIDERS="${DELETE_PROVIDERS:-1}"

# curl_args adds the management bearer token only when one is configured, so the
# script also works against a local dev instance with auth disabled.
curl_args=(-sS --fail-with-body)
if [[ -n "${BIFROST_API_KEY}" ]]; then
  curl_args+=(--header "Authorization: Bearer ${BIFROST_API_KEY}")
fi

# get issues an authenticated GET against a Bifrost path and prints the body.
get() {
  curl "${curl_args[@]}" --location "${BIFROST_URL}$1"
}

# urlenc percent-encodes one URL path segment using jq's URI encoder.
urlenc() {
  jq -rn --arg v "$1" '$v|@uri'
}

# delete_one issues an authenticated DELETE for a single entity path, printing
# OK/FAIL and returning non-zero on failure.
delete_one() {
  local label="$1" path="$2"
  if curl "${curl_args[@]}" --request DELETE --location "${BIFROST_URL}${path}" >/dev/null; then
    echo "    OK deleted ${label}"
    return 0
  fi
  echo "    FAIL delete ${label}"
  return 1
}

# delete_ids deletes every id read on stdin under the given path prefix,
# printing a dry-run line instead when DRY_RUN is set. Echoes the failure count.
delete_ids() {
  local label="$1" prefix="$2" failed=0 id enc
  while IFS= read -r id; do
    [[ -z "${id}" ]] && continue
    if [[ "${DRY_RUN}" == "1" ]]; then
      echo "    DRY-RUN would delete ${label} ${id}"
      continue
    fi
    enc="$(urlenc "${id}")"
    delete_one "${label} ${id}" "${prefix}${enc}" || failed=$((failed + 1))
  done
  return "${failed}"
}

# delete_all_virtual_keys pages through /api/governance/virtual-keys (limit /
# offset) and deletes every virtual key individually.
delete_all_virtual_keys() {
  echo "==> Listing virtual keys"
  local ids limit=100 offset=0 page batch
  ids=""
  while :; do
    page="$(get "/api/governance/virtual-keys?limit=${limit}&offset=${offset}")"
    batch="$(jq -r '.virtual_keys[]?.id' <<<"${page}")"
    [[ -z "${batch}" ]] && break
    ids+="${batch}"$'\n'
    offset=$((offset + limit))
  done

  local count
  count="$(grep -c . <<<"${ids}" || true)"
  echo "    found ${count} virtual key(s)"
  [[ "${count}" -eq 0 ]] && return 0

  local failed=0
  delete_ids "virtual key" "/api/governance/virtual-keys/" <<<"${ids}" || failed=$?
  [[ "${failed}" -gt 0 ]] && { echo "    ${failed} virtual key(s) failed"; return 1; }
  return 0
}

# delete_all_model_configs pages through /api/governance/model-configs and
# deletes every model config individually. Deleting a model config also deletes
# its owned budgets and rate limits in Bifrost.
delete_all_model_configs() {
  echo "==> Listing model configs"
  local ids limit=100 offset=0 page batch
  ids=""
  while :; do
    page="$(get "/api/governance/model-configs?limit=${limit}&offset=${offset}")"
    batch="$(jq -r '.model_configs[]?.id' <<<"${page}")"
    [[ -z "${batch}" ]] && break
    ids+="${batch}"$'\n'
    offset=$((offset + limit))
  done

  local count
  count="$(grep -c . <<<"${ids}" || true)"
  echo "    found ${count} model config(s)"
  [[ "${count}" -eq 0 ]] && return 0

  local failed=0
  delete_ids "model config" "/api/governance/model-configs/" <<<"${ids}" || failed=$?
  [[ "${failed}" -gt 0 ]] && { echo "    ${failed} model config(s) failed"; return 1; }
  return 0
}

# delete_all_teams deletes every team. /api/governance/teams returns the full
# list (no pagination).
delete_all_teams() {
  echo "==> Listing teams"
  local ids count
  ids="$(get "/api/governance/teams" | jq -r '.teams[]?.id')"
  count="$(grep -c . <<<"${ids}" || true)"
  echo "    found ${count} team(s)"
  [[ "${count}" -eq 0 ]] && return 0

  local failed=0
  delete_ids "team" "/api/governance/teams/" <<<"${ids}" || failed=$?
  [[ "${failed}" -gt 0 ]] && { echo "    ${failed} team(s) failed"; return 1; }
  return 0
}

# delete_all_customers deletes every customer. /api/governance/customers returns
# the full list (no pagination).
delete_all_customers() {
  echo "==> Listing customers"
  local ids count
  ids="$(get "/api/governance/customers" | jq -r '.customers[]?.id')"
  count="$(grep -c . <<<"${ids}" || true)"
  echo "    found ${count} customer(s)"
  [[ "${count}" -eq 0 ]] && return 0

  local failed=0
  delete_ids "customer" "/api/governance/customers/" <<<"${ids}" || failed=$?
  [[ "${failed}" -gt 0 ]] && { echo "    ${failed} customer(s) failed"; return 1; }
  return 0
}

# delete_all_users pages through /api/users (page / has_more) and deletes every
# user individually. Deleting the caller's own user is rejected by the API and
# counted as a failure.
delete_all_users() {
  echo "==> Listing users"
  local ids="" limit=100 page=1 resp batch has_more
  while :; do
    resp="$(get "/api/users?page=${page}&limit=${limit}")"
    batch="$(jq -r '.users[]?.id' <<<"${resp}")"
    [[ -n "${batch}" ]] && ids+="${batch}"$'\n'
    has_more="$(jq -r '.has_more // false' <<<"${resp}")"
    [[ "${has_more}" != "true" ]] && break
    page=$((page + 1))
  done

  local count
  count="$(grep -c . <<<"${ids}" || true)"
  echo "    found ${count} user(s)"
  [[ "${count}" -eq 0 ]] && return 0

  local failed=0
  delete_ids "user" "/api/users/" <<<"${ids}" || failed=$?
  [[ "${failed}" -gt 0 ]] && { echo "    ${failed} user(s) failed (a self-delete is expected to fail)"; return 1; }
  return 0
}

# delete_all_provider_keys deletes every key under every configured provider.
# Provider deletion usually cascades keys, but deleting keys first gives clearer
# reset output and works when provider deletion is disabled.
delete_all_provider_keys() {
  echo "==> Listing provider keys"
  local providers provider provider_path ids count total=0 failed=0 key_id key_path
  providers="$(get "/api/providers" | jq -r '.providers[]?.name')"
  count="$(grep -c . <<<"${providers}" || true)"
  echo "    found ${count} provider(s) to inspect"
  [[ "${count}" -eq 0 ]] && return 0

  while IFS= read -r provider; do
    [[ -z "${provider}" ]] && continue
    provider_path="$(urlenc "${provider}")"
    ids="$(get "/api/providers/${provider_path}/keys" | jq -r '.keys[]?.id')"
    count="$(grep -c . <<<"${ids}" || true)"
    echo "    provider ${provider}: found ${count} key(s)"
    total=$((total + count))
    while IFS= read -r key_id; do
      [[ -z "${key_id}" ]] && continue
      key_path="$(urlenc "${key_id}")"
      if [[ "${DRY_RUN}" == "1" ]]; then
        echo "    DRY-RUN would delete provider key ${provider}/${key_id}"
        continue
      fi
      delete_one "provider key ${provider}/${key_id}" "/api/providers/${provider_path}/keys/${key_path}" || failed=$((failed + 1))
    done <<<"${ids}"
  done <<<"${providers}"

  echo "    found ${total} provider key(s)"
  [[ "${failed}" -gt 0 ]] && { echo "    ${failed} provider key(s) failed"; return 1; }
  return 0
}

# delete_all_providers deletes every configured provider after keys are removed.
delete_all_providers() {
  echo "==> Listing providers"
  local providers count failed=0 provider provider_path
  providers="$(get "/api/providers" | jq -r '.providers[]?.name')"
  count="$(grep -c . <<<"${providers}" || true)"
  echo "    found ${count} provider(s)"
  [[ "${count}" -eq 0 ]] && return 0

  while IFS= read -r provider; do
    [[ -z "${provider}" ]] && continue
    provider_path="$(urlenc "${provider}")"
    if [[ "${DRY_RUN}" == "1" ]]; then
      echo "    DRY-RUN would delete provider ${provider}"
      continue
    fi
    delete_one "provider ${provider}" "/api/providers/${provider_path}" || failed=$((failed + 1))
  done <<<"${providers}"

  [[ "${failed}" -gt 0 ]] && { echo "    ${failed} provider(s) failed"; return 1; }
  return 0
}

main() {
  if [[ "${DRY_RUN}" == "1" ]]; then
    echo "DRY-RUN mode: nothing will be deleted."
    echo
  fi

  [[ "${DELETE_VKS}" == "1" ]] && { delete_all_virtual_keys || true; echo; }
  [[ "${DELETE_MODEL_CONFIGS}" == "1" ]] && { delete_all_model_configs || true; echo; }
  [[ "${DELETE_TEAMS}" == "1" ]] && { delete_all_teams || true; echo; }
  [[ "${DELETE_CUSTOMERS}" == "1" ]] && { delete_all_customers || true; echo; }
  [[ "${DELETE_USERS}" == "1" ]] && { delete_all_users || true; echo; }
  [[ "${DELETE_KEYS}" == "1" ]] && { delete_all_provider_keys || true; echo; }
  [[ "${DELETE_PROVIDERS}" == "1" ]] && { delete_all_providers || true; echo; }

  echo "Cleanup complete."
}

main "$@"
