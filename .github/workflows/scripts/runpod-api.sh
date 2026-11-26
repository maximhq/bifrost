#!/usr/bin/env bash

RUNPOD_GRAPHQL_URL="${RUNPOD_GRAPHQL_URL:-https://api.runpod.io/graphql}"

runpod_graphql() {
  local query="$1" variables="$2"
  jq -n --arg query "$query" --argjson variables "$variables" '{query: $query, variables: $variables}' |
    curl -sS --max-time 60 \
      -K <(printf 'header = "Authorization: Bearer %s"\n' "$RUNPOD_API_KEY") \
      -H "Content-Type: application/json" \
      --data-binary @- \
      "$RUNPOD_GRAPHQL_URL"
}