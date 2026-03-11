#!/usr/bin/env bash
set -euo pipefail

# Validate that config.schema.json stays in sync with Go struct JSON tags
# Extracts json:"..." tags from Go structs and compares against schema properties

echo "🔍 Validating Go struct fields vs config.schema.json..."
echo "========================================================"

# Get the repository root
if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

CONFIG_SCHEMA="$REPO_ROOT/transports/config.schema.json"

# Color codes
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

ERRORS=0
WARNINGS=0

# Check prerequisites
if [ ! -f "$CONFIG_SCHEMA" ]; then
  echo "❌ Config schema not found: $CONFIG_SCHEMA"
  exit 1
fi

if ! command -v jq &> /dev/null; then
  echo "❌ jq is required for Go-to-schema validation"
  exit 1
fi

# Extract JSON tags from a Go struct
# Usage: extract_go_json_tags <file> <struct_name>
# Returns sorted list of json tag names (excluding "-" and ",omitempty" suffixes)
extract_go_json_tags() {
  local file=$1
  local struct_name=$2
  awk "/^type ${struct_name} struct/,/^}/" "$file" \
    | grep -oE 'json:"([^"]+)"' \
    | sed 's/json:"//;s/"//' \
    | sed 's/,.*//' \
    | grep -v '^-$' \
    | sort
}

# Extract property keys from config.schema.json at a given jq path
# Usage: extract_schema_keys <jq_path>
extract_schema_keys() {
  local jq_path=$1
  jq -r "${jq_path} | keys[]" "$CONFIG_SCHEMA" 2>/dev/null | sort
}

# Compare Go struct tags against schema properties
# Usage: compare_struct_to_schema <label> <go_file> <struct_name> <jq_path> <exclusions...>
compare_struct_to_schema() {
  local label=$1
  local go_file=$2
  local struct_name=$3
  local jq_path=$4
  shift 4
  local exclusions=("$@")

  echo ""
  echo -e "${CYAN}  Checking: $label ($struct_name)${NC}"

  if [ ! -f "$go_file" ]; then
    echo -e "${RED}    ❌ Go file not found: $go_file${NC}"
    ERRORS=$((ERRORS + 1))
    return
  fi

  local go_tags
  go_tags=$(extract_go_json_tags "$go_file" "$struct_name")

  local schema_keys
  schema_keys=$(extract_schema_keys "$jq_path")

  if [ -z "$go_tags" ]; then
    echo -e "${RED}    ❌ No JSON tags found for struct $struct_name in $go_file${NC}"
    ERRORS=$((ERRORS + 1))
    return
  fi

  if [ -z "$schema_keys" ]; then
    echo -e "${RED}    ❌ No properties found at $jq_path in config.schema.json${NC}"
    ERRORS=$((ERRORS + 1))
    return
  fi

  local has_error=false

  # Check Go fields missing from schema
  while IFS= read -r tag; do
    [ -z "$tag" ] && continue
    # Check if excluded
    local excluded=false
    for exc in "${exclusions[@]+"${exclusions[@]}"}"; do
      if [ "$tag" = "$exc" ]; then
        excluded=true
        break
      fi
    done
    if [ "$excluded" = "true" ]; then
      continue
    fi

    if ! echo "$schema_keys" | grep -qx "$tag"; then
      echo -e "${RED}    ❌ Go field '$tag' missing from schema ($jq_path)${NC}"
      ERRORS=$((ERRORS + 1))
      has_error=true
    fi
  done <<< "$go_tags"

  # Check schema fields missing from Go (warnings only)
  while IFS= read -r key; do
    [ -z "$key" ] && continue
    if ! echo "$go_tags" | grep -qx "$key"; then
      echo -e "${YELLOW}    ⚠️  Schema property '$key' not found in Go struct $struct_name${NC}"
      WARNINGS=$((WARNINGS + 1))
    fi
  done <<< "$schema_keys"

  if [ "$has_error" = "false" ]; then
    echo -e "${GREEN}    ✅ All Go fields present in schema${NC}"
  fi
}

echo ""
echo "🔍 Comparing Go struct JSON tags against config.schema.json properties..."

# ClientConfig — framework/configstore/clientconfig.go → .properties.client.properties
compare_struct_to_schema \
  "Client Config" \
  "$REPO_ROOT/framework/configstore/clientconfig.go" \
  "ClientConfig" \
  '.properties.client.properties'

# GovernanceConfig — framework/configstore/clientconfig.go → .properties.governance.properties
compare_struct_to_schema \
  "Governance Config" \
  "$REPO_ROOT/framework/configstore/clientconfig.go" \
  "GovernanceConfig" \
  '.properties.governance.properties'

# MCPConfig — core/schemas/mcp.go → .properties.mcp.properties
compare_struct_to_schema \
  "MCP Config" \
  "$REPO_ROOT/core/schemas/mcp.go" \
  "MCPConfig" \
  '.properties.mcp.properties'

# MCPToolManagerConfig — core/schemas/mcp.go → .$defs.mcp_tool_manager_config.properties
compare_struct_to_schema \
  "MCP Tool Manager Config" \
  "$REPO_ROOT/core/schemas/mcp.go" \
  "MCPToolManagerConfig" \
  '."$defs".mcp_tool_manager_config.properties'

# MCPClientConfig — core/schemas/mcp.go → .$defs.mcp_client_config.properties
# Exclude: state (runtime-only), config_hash (internal)
compare_struct_to_schema \
  "MCP Client Config" \
  "$REPO_ROOT/core/schemas/mcp.go" \
  "MCPClientConfig" \
  '."$defs".mcp_client_config.properties' \
  "state" \
  "config_hash"

# Summary
echo ""
echo "========================================================"
echo "🏁 Go-to-Schema Validation Complete!"
echo "========================================================"
echo -e "${GREEN}Errors:   $ERRORS${NC}"
echo -e "${YELLOW}Warnings: $WARNINGS${NC}"
echo ""

if [ "$ERRORS" -gt 0 ]; then
  echo -e "${RED}❌ Some Go struct fields are missing from config.schema.json.${NC}"
  echo "   Add the missing properties to transports/config.schema.json"
  exit 1
else
  echo -e "${GREEN}✅ All Go struct fields are present in config.schema.json!${NC}"
  exit 0
fi
