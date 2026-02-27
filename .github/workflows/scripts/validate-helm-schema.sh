#!/usr/bin/env bash
set -euo pipefail

# Validate that the Helm chart values.schema.json is in sync with config.schema.json
# This script extracts required fields from both schemas and compares them

# Get the repository root
if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

CONFIG_SCHEMA="$REPO_ROOT/transports/config.schema.json"
HELM_SCHEMA="$REPO_ROOT/helm-charts/bifrost/values.schema.json"

echo "📋 Comparing schemas:"
echo "   Config schema: $CONFIG_SCHEMA"
echo "   Helm schema:   $HELM_SCHEMA"

# Check if files exist
if [ ! -f "$CONFIG_SCHEMA" ]; then
  echo "❌ Config schema not found: $CONFIG_SCHEMA"
  exit 1
fi

if [ ! -f "$HELM_SCHEMA" ]; then
  echo "❌ Helm schema not found: $HELM_SCHEMA"
  exit 1
fi

# Check if jq is available
if ! command -v jq &> /dev/null; then
  echo "⚠️  jq not found, skipping detailed schema comparison"
  echo "   Install jq for full schema validation"
  exit 0
fi

ERRORS=0

# Function to extract required fields from a schema definition
extract_required_fields() {
  local schema_file="$1"
  local def_path="$2"
  jq -r "$def_path.required // [] | .[]" "$schema_file" 2>/dev/null | sort
}

# Function to check if a definition exists in schema
def_exists() {
  local schema_file="$1"
  local def_path="$2"
  jq -e "$def_path" "$schema_file" > /dev/null 2>&1
}

echo ""
echo "🔍 Checking required fields in governance entities..."

# Check governance.budgets required fields
CONFIG_BUDGET_REQUIRED=$(jq -r '.properties.governance.properties.budgets.items.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_BUDGET_REQUIRED=$(jq -r '.properties.bifrost.properties.governance.properties.budgets.items.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_BUDGET_REQUIRED" != "$HELM_BUDGET_REQUIRED" ]; then
  echo "❌ Budget required fields mismatch:"
  echo "   Config: [$CONFIG_BUDGET_REQUIRED]"
  echo "   Helm:   [$HELM_BUDGET_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Budget required fields match: [$CONFIG_BUDGET_REQUIRED]"
fi

# Check governance.rate_limits required fields
CONFIG_RATELIMIT_REQUIRED=$(jq -r '.properties.governance.properties.rate_limits.items.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_RATELIMIT_REQUIRED=$(jq -r '.properties.bifrost.properties.governance.properties.rateLimits.items.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_RATELIMIT_REQUIRED" != "$HELM_RATELIMIT_REQUIRED" ]; then
  echo "❌ Rate limits required fields mismatch:"
  echo "   Config: [$CONFIG_RATELIMIT_REQUIRED]"
  echo "   Helm:   [$HELM_RATELIMIT_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Rate limits required fields match: [$CONFIG_RATELIMIT_REQUIRED]"
fi

# Check governance.customers required fields
CONFIG_CUSTOMER_REQUIRED=$(jq -r '.properties.governance.properties.customers.items.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_CUSTOMER_REQUIRED=$(jq -r '.properties.bifrost.properties.governance.properties.customers.items.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_CUSTOMER_REQUIRED" != "$HELM_CUSTOMER_REQUIRED" ]; then
  echo "❌ Customer required fields mismatch:"
  echo "   Config: [$CONFIG_CUSTOMER_REQUIRED]"
  echo "   Helm:   [$HELM_CUSTOMER_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Customer required fields match: [$CONFIG_CUSTOMER_REQUIRED]"
fi

# Check governance.teams required fields
CONFIG_TEAM_REQUIRED=$(jq -r '.properties.governance.properties.teams.items.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_TEAM_REQUIRED=$(jq -r '.properties.bifrost.properties.governance.properties.teams.items.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_TEAM_REQUIRED" != "$HELM_TEAM_REQUIRED" ]; then
  echo "❌ Team required fields mismatch:"
  echo "   Config: [$CONFIG_TEAM_REQUIRED]"
  echo "   Helm:   [$HELM_TEAM_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Team required fields match: [$CONFIG_TEAM_REQUIRED]"
fi

# Check governance.virtual_keys required fields
CONFIG_VK_REQUIRED=$(jq -r '.properties.governance.properties.virtual_keys.items.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_VK_REQUIRED=$(jq -r '.properties.bifrost.properties.governance.properties.virtualKeys.items.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_VK_REQUIRED" != "$HELM_VK_REQUIRED" ]; then
  echo "❌ Virtual key required fields mismatch:"
  echo "   Config: [$CONFIG_VK_REQUIRED]"
  echo "   Helm:   [$HELM_VK_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Virtual key required fields match: [$CONFIG_VK_REQUIRED]"
fi

echo ""
echo '🔍 Checking required fields in $defs...'

# Check base_key required fields
CONFIG_BASEKEY_REQUIRED=$(jq -r '."$defs".base_key.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_BASEKEY_REQUIRED=$(jq -r '."$defs".providerKey.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_BASEKEY_REQUIRED" != "$HELM_BASEKEY_REQUIRED" ]; then
  echo "❌ Provider key (base_key) required fields mismatch:"
  echo "   Config: [$CONFIG_BASEKEY_REQUIRED]"
  echo "   Helm:   [$HELM_BASEKEY_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Provider key required fields match: [$CONFIG_BASEKEY_REQUIRED]"
fi

# Check azure_key_config required fields
CONFIG_AZURE_REQUIRED=$(jq -r '."$defs".azure_key.allOf[1].properties.azure_key_config.properties | keys | map(select(. as $k | ["endpoint", "api_version"] | index($k))) | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "endpoint,api_version")
HELM_AZURE_REQUIRED=$(jq -r '."$defs".providerKey.properties.azure_key_config.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

# Normalize the comparison (config schema uses allOf pattern)
CONFIG_AZURE_REQ_NORM=$(jq -r '."$defs".azure_key.allOf[1].properties.azure_key_config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
if [ -z "$CONFIG_AZURE_REQ_NORM" ]; then
  # Try the direct path in $defs
  CONFIG_AZURE_REQ_NORM=$(jq -r '."$defs".azure_key_config.required // ["endpoint", "api_version"] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "api_version,endpoint")
fi

if [ "$CONFIG_AZURE_REQ_NORM" != "$HELM_AZURE_REQUIRED" ]; then
  echo "❌ Azure key config required fields mismatch:"
  echo "   Config: [$CONFIG_AZURE_REQ_NORM]"
  echo "   Helm:   [$HELM_AZURE_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Azure key config required fields match: [$HELM_AZURE_REQUIRED]"
fi

# Check vertex_key_config required fields
CONFIG_VERTEX_REQUIRED=$(jq -r '."$defs".vertex_key.allOf[1].properties.vertex_key_config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_VERTEX_REQUIRED=$(jq -r '."$defs".providerKey.properties.vertex_key_config.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_VERTEX_REQUIRED" != "$HELM_VERTEX_REQUIRED" ]; then
  echo "❌ Vertex key config required fields mismatch:"
  echo "   Config: [$CONFIG_VERTEX_REQUIRED]"
  echo "   Helm:   [$HELM_VERTEX_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Vertex key config required fields match: [$CONFIG_VERTEX_REQUIRED]"
fi

# Check bedrock_key_config required fields
CONFIG_BEDROCK_REQUIRED=$(jq -r '."$defs".bedrock_key.allOf[1].properties.bedrock_key_config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_BEDROCK_REQUIRED=$(jq -r '."$defs".providerKey.properties.bedrock_key_config.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_BEDROCK_REQUIRED" != "$HELM_BEDROCK_REQUIRED" ]; then
  echo "❌ Bedrock key config required fields mismatch:"
  echo "   Config: [$CONFIG_BEDROCK_REQUIRED]"
  echo "   Helm:   [$HELM_BEDROCK_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Bedrock key config required fields match: [$CONFIG_BEDROCK_REQUIRED]"
fi

# Check vllm_key_config required fields
CONFIG_VLLM_REQUIRED=$(jq -r '."$defs".vllm_key.allOf[1].properties.vllm_key_config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_VLLM_REQUIRED=$(jq -r '."$defs".providerKey.properties.vllm_key_config.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_VLLM_REQUIRED" != "$HELM_VLLM_REQUIRED" ]; then
  echo "❌ VLLM key config required fields mismatch:"
  echo "   Config: [$CONFIG_VLLM_REQUIRED]"
  echo "   Helm:   [$HELM_VLLM_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ VLLM key config required fields match: [$HELM_VLLM_REQUIRED]"
fi

# Check concurrency_config required fields
CONFIG_CONCURRENCY_REQUIRED=$(jq -r '."$defs".concurrency_config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_CONCURRENCY_REQUIRED=$(jq -r '."$defs".concurrencyConfig.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_CONCURRENCY_REQUIRED" != "$HELM_CONCURRENCY_REQUIRED" ]; then
  echo "❌ Concurrency config required fields mismatch:"
  echo "   Config: [$CONFIG_CONCURRENCY_REQUIRED]"
  echo "   Helm:   [$HELM_CONCURRENCY_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Concurrency config required fields match: [$CONFIG_CONCURRENCY_REQUIRED]"
fi

# Check proxy_config required fields
CONFIG_PROXY_REQUIRED=$(jq -r '."$defs".proxy_config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_PROXY_REQUIRED=$(jq -r '."$defs".proxyConfig.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_PROXY_REQUIRED" != "$HELM_PROXY_REQUIRED" ]; then
  echo "❌ Proxy config required fields mismatch:"
  echo "   Config: [$CONFIG_PROXY_REQUIRED]"
  echo "   Helm:   [$HELM_PROXY_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Proxy config required fields match: [$CONFIG_PROXY_REQUIRED]"
fi

# Check mcp_client_config required fields
# Note: Config uses snake_case (connection_type), Helm uses camelCase (connectionType)
CONFIG_MCP_REQUIRED=$(jq -r '."$defs".mcp_client_config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_MCP_REQUIRED=$(jq -r '."$defs".mcpClientConfig.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

# Normalize config snake_case to camelCase for comparison
CONFIG_MCP_NORM=$(echo "$CONFIG_MCP_REQUIRED" | tr ',' '\n' | sed 's/connection_type/connectionType/' | sort | tr '\n' ',' | sed 's/,$//')

if [ "$CONFIG_MCP_NORM" != "$HELM_MCP_REQUIRED" ]; then
  echo "❌ MCP client config required fields mismatch:"
  echo "   Config: [$CONFIG_MCP_REQUIRED] (normalized: [$CONFIG_MCP_NORM])"
  echo "   Helm:   [$HELM_MCP_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ MCP client config required fields match: [$HELM_MCP_REQUIRED]"
fi

# Check provider $def required fields
CONFIG_PROVIDER_REQUIRED=$(jq -r '."$defs".provider.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_PROVIDER_REQUIRED=$(jq -r '."$defs".provider.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_PROVIDER_REQUIRED" != "$HELM_PROVIDER_REQUIRED" ]; then
  echo "❌ Provider def required fields mismatch:"
  echo "   Config: [$CONFIG_PROVIDER_REQUIRED]"
  echo "   Helm:   [$HELM_PROVIDER_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Provider def required fields match: [$CONFIG_PROVIDER_REQUIRED]"
fi

# Check routing_rule required fields
CONFIG_ROUTING_REQUIRED=$(jq -r '."$defs".routing_rule.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_ROUTING_REQUIRED=$(jq -r '.properties.bifrost.properties.governance.properties.routingRules.items.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_ROUTING_REQUIRED" != "$HELM_ROUTING_REQUIRED" ]; then
  echo "❌ Routing rule required fields mismatch:"
  echo "   Config: [$CONFIG_ROUTING_REQUIRED]"
  echo "   Helm:   [$HELM_ROUTING_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Routing rule required fields match: [$CONFIG_ROUTING_REQUIRED]"
fi

echo ""
echo "🔍 Checking required fields in guardrails..."

# Check guardrail_rules required fields
CONFIG_GUARDRAIL_RULE_REQUIRED=$(jq -r '.properties.guardrails_config.properties.guardrail_rules.items.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
# Also check in $defs
if [ -z "$CONFIG_GUARDRAIL_RULE_REQUIRED" ]; then
  CONFIG_GUARDRAIL_RULE_REQUIRED=$(jq -r '."$defs".guardrails_config.properties.guardrail_rules.items.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
fi
HELM_GUARDRAIL_RULE_REQUIRED=$(jq -r '.properties.bifrost.properties.guardrails.properties.rules.items.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_GUARDRAIL_RULE_REQUIRED" != "$HELM_GUARDRAIL_RULE_REQUIRED" ]; then
  echo "❌ Guardrail rules required fields mismatch:"
  echo "   Config: [$CONFIG_GUARDRAIL_RULE_REQUIRED]"
  echo "   Helm:   [$HELM_GUARDRAIL_RULE_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Guardrail rules required fields match: [$CONFIG_GUARDRAIL_RULE_REQUIRED]"
fi

# Check guardrail_providers required fields
CONFIG_GUARDRAIL_PROV_REQUIRED=$(jq -r '.properties.guardrails_config.properties.guardrail_providers.items.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
if [ -z "$CONFIG_GUARDRAIL_PROV_REQUIRED" ]; then
  CONFIG_GUARDRAIL_PROV_REQUIRED=$(jq -r '."$defs".guardrails_config.properties.guardrail_providers.items.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
fi
HELM_GUARDRAIL_PROV_REQUIRED=$(jq -r '.properties.bifrost.properties.guardrails.properties.providers.items.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_GUARDRAIL_PROV_REQUIRED" != "$HELM_GUARDRAIL_PROV_REQUIRED" ]; then
  echo "❌ Guardrail providers required fields mismatch:"
  echo "   Config: [$CONFIG_GUARDRAIL_PROV_REQUIRED]"
  echo "   Helm:   [$HELM_GUARDRAIL_PROV_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Guardrail providers required fields match: [$CONFIG_GUARDRAIL_PROV_REQUIRED]"
fi

echo ""
echo "🔍 Checking required fields in cluster config..."

# Check cluster gossip required fields (port, config)
CONFIG_GOSSIP_TOP_REQUIRED=$(jq -r '."$defs".cluster_config.properties.gossip.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_GOSSIP_TOP_REQUIRED=$(jq -r '.properties.bifrost.properties.cluster.properties.gossip.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_GOSSIP_TOP_REQUIRED" != "$HELM_GOSSIP_TOP_REQUIRED" ]; then
  echo "❌ Cluster gossip required fields mismatch:"
  echo "   Config: [$CONFIG_GOSSIP_TOP_REQUIRED]"
  echo "   Helm:   [$HELM_GOSSIP_TOP_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Cluster gossip required fields match: [$CONFIG_GOSSIP_TOP_REQUIRED]"
fi

# Check cluster gossip config required fields (timeout_seconds, success_threshold, failure_threshold)
CONFIG_GOSSIP_REQUIRED=$(jq -r '."$defs".cluster_config.properties.gossip.properties.config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_GOSSIP_REQUIRED=$(jq -r '.properties.bifrost.properties.cluster.properties.gossip.properties.config.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

# Normalize field names (config uses snake_case, helm uses camelCase)
CONFIG_GOSSIP_NORM=$(echo "$CONFIG_GOSSIP_REQUIRED" | tr ',' '\n' | sed 's/failure_threshold/failureThreshold/;s/success_threshold/successThreshold/;s/timeout_seconds/timeoutSeconds/' | sort | tr '\n' ',' | sed 's/,$//')

if [ "$CONFIG_GOSSIP_NORM" != "$HELM_GOSSIP_REQUIRED" ]; then
  echo "❌ Cluster gossip config required fields mismatch:"
  echo "   Config: [$CONFIG_GOSSIP_REQUIRED] (normalized: [$CONFIG_GOSSIP_NORM])"
  echo "   Helm:   [$HELM_GOSSIP_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Cluster gossip config required fields match: [$HELM_GOSSIP_REQUIRED]"
fi

echo ""
echo "🔍 Checking required fields in virtual_key_provider_config..."

# Check virtual_key_provider_config required fields
CONFIG_VKPC_REQUIRED=$(jq -r '."$defs".virtual_key_provider_config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_VKPC_REQUIRED=$(jq -r '."$defs".virtualKeyProviderConfig.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_VKPC_REQUIRED" != "$HELM_VKPC_REQUIRED" ]; then
  echo "❌ Virtual key provider config required fields mismatch:"
  echo "   Config: [$CONFIG_VKPC_REQUIRED]"
  echo "   Helm:   [$HELM_VKPC_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Virtual key provider config required fields match: [$CONFIG_VKPC_REQUIRED]"
fi

# Check virtual_key_provider_config keys items required fields (key_id, name, value)
CONFIG_VKPC_KEY_REQUIRED=$(jq -r '."$defs".virtual_key_provider_config.properties.keys.items.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_VKPC_KEY_REQUIRED=$(jq -r '."$defs".virtualKeyProviderConfig.properties.keys.items.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_VKPC_KEY_REQUIRED" != "$HELM_VKPC_KEY_REQUIRED" ]; then
  echo "❌ VK provider config key items required fields mismatch:"
  echo "   Config: [$CONFIG_VKPC_KEY_REQUIRED]"
  echo "   Helm:   [$HELM_VKPC_KEY_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ VK provider config key items required fields match: [$CONFIG_VKPC_KEY_REQUIRED]"
fi

# Check VK provider config key azure_key_config required fields
CONFIG_VKPC_AZURE_REQUIRED=$(jq -r '."$defs".virtual_key_provider_config.properties.keys.items.properties.azure_key_config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_VKPC_AZURE_REQUIRED=$(jq -r '."$defs".virtualKeyProviderConfig.properties.keys.items.properties.azure_key_config.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_VKPC_AZURE_REQUIRED" != "$HELM_VKPC_AZURE_REQUIRED" ]; then
  echo "❌ VK provider config key azure_key_config required fields mismatch:"
  echo "   Config: [$CONFIG_VKPC_AZURE_REQUIRED]"
  echo "   Helm:   [$HELM_VKPC_AZURE_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ VK provider config key azure_key_config required fields match: [$CONFIG_VKPC_AZURE_REQUIRED]"
fi

# Check VK provider config key vertex_key_config required fields
CONFIG_VKPC_VERTEX_REQUIRED=$(jq -r '."$defs".virtual_key_provider_config.properties.keys.items.properties.vertex_key_config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_VKPC_VERTEX_REQUIRED=$(jq -r '."$defs".virtualKeyProviderConfig.properties.keys.items.properties.vertex_key_config.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_VKPC_VERTEX_REQUIRED" != "$HELM_VKPC_VERTEX_REQUIRED" ]; then
  echo "❌ VK provider config key vertex_key_config required fields mismatch:"
  echo "   Config: [$CONFIG_VKPC_VERTEX_REQUIRED]"
  echo "   Helm:   [$HELM_VKPC_VERTEX_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ VK provider config key vertex_key_config required fields match: [$CONFIG_VKPC_VERTEX_REQUIRED]"
fi

# Check VK provider config key vllm_key_config required fields
CONFIG_VKPC_VLLM_REQUIRED=$(jq -r '."$defs".virtual_key_provider_config.properties.keys.items.properties.vllm_key_config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_VKPC_VLLM_REQUIRED=$(jq -r '."$defs".virtualKeyProviderConfig.properties.keys.items.properties.vllm_key_config.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_VKPC_VLLM_REQUIRED" != "$HELM_VKPC_VLLM_REQUIRED" ]; then
  echo "❌ VK provider config key vllm_key_config required fields mismatch:"
  echo "   Config: [$CONFIG_VKPC_VLLM_REQUIRED]"
  echo "   Helm:   [$HELM_VKPC_VLLM_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ VK provider config key vllm_key_config required fields match: [$CONFIG_VKPC_VLLM_REQUIRED]"
fi

echo ""
echo "🔍 Checking required fields in virtual key MCP config..."

# Check virtual_key_mcp_config required fields
CONFIG_VK_MCP_REQUIRED=$(jq -r '."$defs".virtual_key_mcp_config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_VK_MCP_REQUIRED=$(jq -r '.properties.bifrost.properties.governance.properties.virtualKeys.items.properties.mcp_configs.items.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_VK_MCP_REQUIRED" != "$HELM_VK_MCP_REQUIRED" ]; then
  echo "❌ Virtual key MCP config required fields mismatch:"
  echo "   Config: [$CONFIG_VK_MCP_REQUIRED]"
  echo "   Helm:   [$HELM_VK_MCP_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Virtual key MCP config required fields match: [$CONFIG_VK_MCP_REQUIRED]"
fi

echo ""
echo "🔍 Checking required fields in MCP sub-configs..."

# Check MCP stdio_config required fields
CONFIG_MCP_STDIO_REQUIRED=$(jq -r '."$defs".mcp_client_config.properties.stdio_config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_MCP_STDIO_REQUIRED=$(jq -r '."$defs".mcpClientConfig.properties.stdioConfig.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_MCP_STDIO_REQUIRED" != "$HELM_MCP_STDIO_REQUIRED" ]; then
  echo "❌ MCP stdio config required fields mismatch:"
  echo "   Config: [$CONFIG_MCP_STDIO_REQUIRED]"
  echo "   Helm:   [$HELM_MCP_STDIO_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ MCP stdio config required fields match: [$CONFIG_MCP_STDIO_REQUIRED]"
fi

# Check MCP websocket_config required fields
CONFIG_MCP_WS_REQUIRED=$(jq -r '."$defs".mcp_client_config.properties.websocket_config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_MCP_WS_REQUIRED=$(jq -r '."$defs".mcpClientConfig.properties.websocketConfig.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_MCP_WS_REQUIRED" != "$HELM_MCP_WS_REQUIRED" ]; then
  echo "❌ MCP websocket config required fields mismatch:"
  echo "   Config: [$CONFIG_MCP_WS_REQUIRED]"
  echo "   Helm:   [$HELM_MCP_WS_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ MCP websocket config required fields match: [$CONFIG_MCP_WS_REQUIRED]"
fi

# Check MCP http_config required fields
CONFIG_MCP_HTTP_REQUIRED=$(jq -r '."$defs".mcp_client_config.properties.http_config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_MCP_HTTP_REQUIRED=$(jq -r '."$defs".mcpClientConfig.properties.httpConfig.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_MCP_HTTP_REQUIRED" != "$HELM_MCP_HTTP_REQUIRED" ]; then
  echo "❌ MCP http config required fields mismatch:"
  echo "   Config: [$CONFIG_MCP_HTTP_REQUIRED]"
  echo "   Helm:   [$HELM_MCP_HTTP_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ MCP http config required fields match: [$CONFIG_MCP_HTTP_REQUIRED]"
fi

echo ""
echo "🔍 Checking required fields in SAML/SCIM config..."

# Check okta_config required fields
CONFIG_OKTA_REQUIRED=$(jq -r '."$defs".okta_config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_OKTA_REQUIRED=$(jq -r '.properties.bifrost.properties.saml.allOf[0].then.properties.config.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_OKTA_REQUIRED" != "$HELM_OKTA_REQUIRED" ]; then
  echo "❌ Okta config required fields mismatch:"
  echo "   Config: [$CONFIG_OKTA_REQUIRED]"
  echo "   Helm:   [$HELM_OKTA_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Okta config required fields match: [$CONFIG_OKTA_REQUIRED]"
fi

# Check entra_config required fields
CONFIG_ENTRA_REQUIRED=$(jq -r '."$defs".entra_config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_ENTRA_REQUIRED=$(jq -r '.properties.bifrost.properties.saml.allOf[1].then.properties.config.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_ENTRA_REQUIRED" != "$HELM_ENTRA_REQUIRED" ]; then
  echo "❌ Entra config required fields mismatch:"
  echo "   Config: [$CONFIG_ENTRA_REQUIRED]"
  echo "   Helm:   [$HELM_ENTRA_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Entra config required fields match: [$CONFIG_ENTRA_REQUIRED]"
fi

echo ""
echo "🔍 Checking required fields in plugin configs..."

# Check semantic cache plugin required fields (dimension)
# Config uses an allOf pattern on plugins array items; Helm uses conditional on semanticCache.enabled
CONFIG_SEMCACHE_REQUIRED=$(jq -r '.properties.plugins.items.allOf[] | select(.if.properties.name.const == "semanticcache") | .then.properties.config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_SEMCACHE_REQUIRED=$(jq -r '.properties.bifrost.properties.plugins.properties.semanticCache.then.properties.config.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_SEMCACHE_REQUIRED" != "$HELM_SEMCACHE_REQUIRED" ]; then
  echo "❌ Semantic cache plugin config required fields mismatch:"
  echo "   Config: [$CONFIG_SEMCACHE_REQUIRED]"
  echo "   Helm:   [$HELM_SEMCACHE_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Semantic cache plugin config required fields match: [$CONFIG_SEMCACHE_REQUIRED]"
fi

# Check OTEL plugin required fields (collector_url, trace_type, protocol)
CONFIG_OTEL_REQUIRED=$(jq -r '.properties.plugins.items.allOf[] | select(.if.properties.name.const == "otel") | .then.properties.config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_OTEL_REQUIRED=$(jq -r '.properties.bifrost.properties.plugins.properties.otel.then.properties.config.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_OTEL_REQUIRED" != "$HELM_OTEL_REQUIRED" ]; then
  echo "❌ OTEL plugin config required fields mismatch:"
  echo "   Config: [$CONFIG_OTEL_REQUIRED]"
  echo "   Helm:   [$HELM_OTEL_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ OTEL plugin config required fields match: [$CONFIG_OTEL_REQUIRED]"
fi

# Check telemetry push_gateway required fields
CONFIG_PUSHGW_REQUIRED=$(jq -r '.properties.plugins.items.allOf[] | select(.if.properties.name.const == "telemetry") | .then.properties.config.properties.push_gateway.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_PUSHGW_REQUIRED=$(jq -r '.properties.bifrost.properties.plugins.properties.telemetry.properties.config.properties.push_gateway.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_PUSHGW_REQUIRED" != "$HELM_PUSHGW_REQUIRED" ]; then
  echo "❌ Telemetry push_gateway required fields mismatch:"
  echo "   Config: [$CONFIG_PUSHGW_REQUIRED]"
  echo "   Helm:   [$HELM_PUSHGW_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Telemetry push_gateway required fields match: [$CONFIG_PUSHGW_REQUIRED]"
fi

# Check telemetry push_gateway basic_auth required fields
CONFIG_PUSHGW_AUTH_REQUIRED=$(jq -r '.properties.plugins.items.allOf[] | select(.if.properties.name.const == "telemetry") | .then.properties.config.properties.push_gateway.properties.basic_auth.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_PUSHGW_AUTH_REQUIRED=$(jq -r '.properties.bifrost.properties.plugins.properties.telemetry.properties.config.properties.push_gateway.properties.basic_auth.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_PUSHGW_AUTH_REQUIRED" != "$HELM_PUSHGW_AUTH_REQUIRED" ]; then
  echo "❌ Telemetry push_gateway basic_auth required fields mismatch:"
  echo "   Config: [$CONFIG_PUSHGW_AUTH_REQUIRED]"
  echo "   Helm:   [$HELM_PUSHGW_AUTH_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Telemetry push_gateway basic_auth required fields match: [$CONFIG_PUSHGW_AUTH_REQUIRED]"
fi

# Check plugin array items required fields (enabled, name)
# Config defines plugins as an array; Helm splits into named plugins + a "custom" array
CONFIG_PLUGIN_ITEMS_REQUIRED=$(jq -r '.properties.plugins.items.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_PLUGIN_ITEMS_REQUIRED=$(jq -r '.properties.bifrost.properties.plugins.properties.custom.items.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_PLUGIN_ITEMS_REQUIRED" != "$HELM_PLUGIN_ITEMS_REQUIRED" ]; then
  echo "❌ Plugin items required fields mismatch:"
  echo "   Config (plugins.items): [$CONFIG_PLUGIN_ITEMS_REQUIRED]"
  echo "   Helm (plugins.custom.items): [$HELM_PLUGIN_ITEMS_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Plugin items required fields match: [$CONFIG_PLUGIN_ITEMS_REQUIRED]"
fi

# Check maxim plugin config required fields (api_key)
# Note: Helm allows either config.api_key OR secretRef.name via anyOf
CONFIG_MAXIM_REQUIRED=$(jq -r '.properties.plugins.items.allOf[] | select(.if.properties.name.const == "maxim") | .then.properties.config.required // [] | sort | join(",")' "$CONFIG_SCHEMA" 2>/dev/null || echo "")
HELM_MAXIM_REQUIRED=$(jq -r '.properties.bifrost.properties.plugins.properties.maxim.then.anyOf[0].properties.config.required // [] | sort | join(",")' "$HELM_SCHEMA" 2>/dev/null || echo "")

if [ "$CONFIG_MAXIM_REQUIRED" != "$HELM_MAXIM_REQUIRED" ]; then
  echo "❌ Maxim plugin config required fields mismatch:"
  echo "   Config: [$CONFIG_MAXIM_REQUIRED]"
  echo "   Helm (anyOf[0]): [$HELM_MAXIM_REQUIRED]"
  ERRORS=$((ERRORS + 1))
else
  echo "✅ Maxim plugin config required fields match: [$CONFIG_MAXIM_REQUIRED]"
fi

echo ""
if [ $ERRORS -gt 0 ]; then
  echo "❌ Schema validation failed with $ERRORS error(s)"
  echo ""
  echo "To fix these errors, update helm-charts/bifrost/values.schema.json to match"
  echo "the required fields in transports/config.schema.json"
  exit 1
fi

echo "✅ All schema validations passed!"
exit 0
