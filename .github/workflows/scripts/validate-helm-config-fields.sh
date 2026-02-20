#!/usr/bin/env bash
set -euo pipefail

# Helm config.json field validation script for Bifrost
# Validates that all fields from config.schema.json are properly rendered
# in the helm template output config.json

echo "🔍 Validating Helm Config JSON Fields (config.schema.json coverage)..."
echo "======================================================================"

# Color codes
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

TESTS_PASSED=0
TESTS_FAILED=0
TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

CHART_DIR="./helm-charts/bifrost"

report_result() {
  local test_name=$1
  local result=$2

  if [ "$result" -eq 0 ]; then
    echo -e "${GREEN}  ✅ $test_name${NC}"
    TESTS_PASSED=$((TESTS_PASSED + 1))
  else
    echo -e "${RED}  ❌ $test_name${NC}"
    TESTS_FAILED=$((TESTS_FAILED + 1))
  fi
}

# Render template and extract config.json into $TMPDIR/config.json
# Usage: render_config <values-file>
render_config() {
  local values_file=$1
  helm template bifrost "$CHART_DIR" \
    --set image.tag=v1.0.0 \
    -f "$values_file" \
    > "$TMPDIR/rendered.yaml" 2>"$TMPDIR/render-err.txt"
  local rc=$?
  if [ "$rc" -ne 0 ]; then
    echo -e "${RED}    Render failed:${NC}"
    head -5 "$TMPDIR/render-err.txt" | sed 's/^/      /'
    return 1
  fi

  # Extract config.json value from ConfigMap
  python3 -c "
import yaml, json, sys
docs = list(yaml.safe_load_all(open('$TMPDIR/rendered.yaml')))
for doc in docs:
    if doc and doc.get('kind') == 'ConfigMap' and 'config.json' in doc.get('data', {}):
        cfg = json.loads(doc['data']['config.json'])
        json.dump(cfg, open('$TMPDIR/config.json', 'w'), indent=2)
        sys.exit(0)
print('ERROR: config.json not found in rendered ConfigMap', file=sys.stderr)
sys.exit(1)
" 2>"$TMPDIR/extract-err.txt"
}

# Assert a JSON path exists (is not null/missing) in config.json
# Usage: assert_field "test name" ".path.to.field"
assert_field() {
  local test_name=$1
  local jq_path=$2
  local result
  result=$(python3 -c "
import json, sys
cfg = json.load(open('$TMPDIR/config.json'))
parts = '$jq_path'.strip('.').split('.')
obj = cfg
for p in parts:
    if p.startswith('[') and p.endswith(']'):
        idx = int(p[1:-1])
        if not isinstance(obj, list) or idx >= len(obj):
            print('MISSING')
            sys.exit(0)
        obj = obj[idx]
    elif isinstance(obj, dict) and p in obj:
        obj = obj[p]
    else:
        print('MISSING')
        sys.exit(0)
print('FOUND')
" 2>/dev/null)
  if [ "$result" = "FOUND" ]; then
    report_result "$test_name" 0
  else
    report_result "$test_name" 1
    echo -e "${YELLOW}    Expected field '$jq_path' in config.json${NC}"
  fi
}

# Assert a JSON path equals a specific value
# Usage: assert_field_value "test name" ".path.to.field" "expected_value"
assert_field_value() {
  local test_name=$1
  local jq_path=$2
  local expected=$3
  local result
  result=$(python3 -c "
import json, sys
cfg = json.load(open('$TMPDIR/config.json'))
parts = '$jq_path'.strip('.').split('.')
obj = cfg
for p in parts:
    if p.startswith('[') and p.endswith(']'):
        idx = int(p[1:-1])
        if not isinstance(obj, list) or idx >= len(obj):
            print('MISSING')
            sys.exit(0)
        obj = obj[idx]
    elif isinstance(obj, dict) and p in obj:
        obj = obj[p]
    else:
        print('MISSING')
        sys.exit(0)
print(json.dumps(obj))
" 2>/dev/null)
  local expected_json
  expected_json=$(python3 -c "
import json
raw = '''$expected'''
if raw == 'true': print('true')
elif raw == 'false': print('false')
elif raw == 'null': print('null')
else: print(json.dumps($expected))
" 2>/dev/null || echo "\"$expected\"")
  if [ "$result" = "$expected_json" ]; then
    report_result "$test_name" 0
  else
    report_result "$test_name" 1
    echo -e "${YELLOW}    Expected '$jq_path' = $expected_json, got: $result${NC}"
  fi
}

###############################################################################
# 1. Schema + Encryption Key + Client Config
###############################################################################
echo ""
echo -e "${CYAN}📋 1/10 - Schema, Encryption Key & Client Config${NC}"
echo "---------------------------------------------------"

cat > "$TMPDIR/values-client.yaml" << 'VALS'
image:
  tag: v1.0.0
bifrost:
  encryptionKey: "my-secret-passphrase"
  client:
    dropExcessRequests: true
    initialPoolSize: 500
    allowedOrigins:
      - "https://example.com"
    enableLogging: true
    disableContentLogging: true
    disableDbPingsInHealth: true
    logRetentionDays: 30
    enableGovernance: false
    enforceGovernanceHeader: true
    allowDirectKeys: true
    maxRequestBodySizeMb: 50
    enableLitellmFallbacks: true
    prometheusLabels:
      - "team"
      - "env"
    headerFilterConfig:
      allowlist:
        - "x-custom-header"
      denylist:
        - "x-blocked"
VALS

render_config "$TMPDIR/values-client.yaml"
assert_field_value 'schema field' '.$schema' '"https://www.getbifrost.ai/schema"'
assert_field_value 'encryption_key' '.encryption_key' '"my-secret-passphrase"'
assert_field_value 'client.drop_excess_requests' '.client.drop_excess_requests' 'true'
assert_field_value 'client.initial_pool_size' '.client.initial_pool_size' '500'
assert_field 'client.allowed_origins' '.client.allowed_origins'
assert_field_value 'client.enable_logging' '.client.enable_logging' 'true'
assert_field_value 'client.disable_content_logging' '.client.disable_content_logging' 'true'
assert_field_value 'client.disable_db_pings_in_health' '.client.disable_db_pings_in_health' 'true'
assert_field_value 'client.log_retention_days' '.client.log_retention_days' '30'
assert_field_value 'client.enable_governance' '.client.enable_governance' 'false'
assert_field_value 'client.enforce_governance_header' '.client.enforce_governance_header' 'true'
assert_field_value 'client.allow_direct_keys' '.client.allow_direct_keys' 'true'
assert_field_value 'client.max_request_body_size_mb' '.client.max_request_body_size_mb' '50'
assert_field_value 'client.enable_litellm_fallbacks' '.client.enable_litellm_fallbacks' 'true'
assert_field 'client.prometheus_labels' '.client.prometheus_labels'
assert_field 'client.header_filter_config.allowlist' '.client.header_filter_config.allowlist'
assert_field 'client.header_filter_config.denylist' '.client.header_filter_config.denylist'

###############################################################################
# 2. Framework (Pricing)
###############################################################################
echo ""
echo -e "${CYAN}💰 2/10 - Framework Config (Pricing)${NC}"
echo "--------------------------------------"

cat > "$TMPDIR/values-framework.yaml" << 'VALS'
image:
  tag: v1.0.0
bifrost:
  framework:
    pricing:
      pricingUrl: "https://custom-pricing.example.com/data.json"
      pricingSyncInterval: 7200
VALS

render_config "$TMPDIR/values-framework.yaml"
assert_field_value 'framework.pricing.pricing_url' '.framework.pricing.pricing_url' '"https://custom-pricing.example.com/data.json"'
assert_field_value 'framework.pricing.pricing_sync_interval' '.framework.pricing.pricing_sync_interval' '7200'

###############################################################################
# 3. Providers (standard, azure, vertex, bedrock, network/concurrency/proxy)
###############################################################################
echo ""
echo -e "${CYAN}🔑 3/10 - Provider Configurations${NC}"
echo "-----------------------------------"

cat > "$TMPDIR/values-providers.yaml" << 'VALS'
image:
  tag: v1.0.0
bifrost:
  providers:
    openai:
      keys:
        - name: "primary-key"
          value: "sk-test"
          weight: 1
          models:
            - "gpt-4o"
            - "gpt-4o-mini"
          use_for_batch_api: true
      network_config:
        base_url: "https://custom.openai.com"
        extra_headers:
          X-Custom: "value"
        default_request_timeout_in_seconds: 120
        max_retries: 5
        retry_backoff_initial_ms: 200
        retry_backoff_max_ms: 10000
      concurrency_and_buffer_size:
        concurrency: 50
        buffer_size: 100
      proxy_config:
        type: "http"
        url: "http://proxy:3128"
        username: "user"
        password: "pass"
        ca_cert_pem: "PEM_DATA"
      send_back_raw_response: true
    azure:
      keys:
        - name: "azure-key"
          value: "az-test"
          weight: 1
          azure_key_config:
            endpoint: "https://myresource.openai.azure.com"
            api_version: "2024-02-15-preview"
            deployments:
              gpt-4o: "my-deployment"
    vertex:
      keys:
        - name: "vertex-key"
          value: ""
          weight: 1
          vertex_key_config:
            project_id: "my-project"
            region: "us-central1"
            auth_credentials: "env.GOOGLE_CREDS"
    bedrock:
      keys:
        - name: "bedrock-key"
          value: ""
          weight: 1
          bedrock_key_config:
            region: "us-east-1"
            access_key: "env.AWS_KEY"
            secret_key: "env.AWS_SECRET"
            session_token: "env.AWS_TOKEN"
            arn: "arn:aws:iam::role/test"
VALS

render_config "$TMPDIR/values-providers.yaml"

# Standard provider keys
assert_field_value 'providers.openai.keys[0].name' '.providers.openai.keys.[0].name' '"primary-key"'
assert_field_value 'providers.openai.keys[0].value' '.providers.openai.keys.[0].value' '"sk-test"'
assert_field_value 'providers.openai.keys[0].weight' '.providers.openai.keys.[0].weight' '1'
assert_field 'providers.openai.keys[0].models' '.providers.openai.keys.[0].models'
assert_field_value 'providers.openai.keys[0].use_for_batch_api' '.providers.openai.keys.[0].use_for_batch_api' 'true'

# Network config
assert_field_value 'providers.openai.network_config.base_url' '.providers.openai.network_config.base_url' '"https://custom.openai.com"'
assert_field 'providers.openai.network_config.extra_headers' '.providers.openai.network_config.extra_headers'
assert_field_value 'providers.openai.network_config.default_request_timeout_in_seconds' '.providers.openai.network_config.default_request_timeout_in_seconds' '120'
assert_field_value 'providers.openai.network_config.max_retries' '.providers.openai.network_config.max_retries' '5'
assert_field_value 'providers.openai.network_config.retry_backoff_initial_ms' '.providers.openai.network_config.retry_backoff_initial_ms' '200'
assert_field_value 'providers.openai.network_config.retry_backoff_max_ms' '.providers.openai.network_config.retry_backoff_max_ms' '10000'

# Concurrency config
assert_field_value 'providers.openai.concurrency_and_buffer_size.concurrency' '.providers.openai.concurrency_and_buffer_size.concurrency' '50'
assert_field_value 'providers.openai.concurrency_and_buffer_size.buffer_size' '.providers.openai.concurrency_and_buffer_size.buffer_size' '100'

# Proxy config
assert_field_value 'providers.openai.proxy_config.type' '.providers.openai.proxy_config.type' '"http"'
assert_field_value 'providers.openai.proxy_config.url' '.providers.openai.proxy_config.url' '"http://proxy:3128"'
assert_field_value 'providers.openai.proxy_config.username' '.providers.openai.proxy_config.username' '"user"'
assert_field_value 'providers.openai.proxy_config.password' '.providers.openai.proxy_config.password' '"pass"'
assert_field_value 'providers.openai.proxy_config.ca_cert_pem' '.providers.openai.proxy_config.ca_cert_pem' '"PEM_DATA"'

# send_back_raw_response
assert_field_value 'providers.openai.send_back_raw_response' '.providers.openai.send_back_raw_response' 'true'

# Azure key config
assert_field_value 'providers.azure.keys[0].azure_key_config.endpoint' '.providers.azure.keys.[0].azure_key_config.endpoint' '"https://myresource.openai.azure.com"'
assert_field_value 'providers.azure.keys[0].azure_key_config.api_version' '.providers.azure.keys.[0].azure_key_config.api_version' '"2024-02-15-preview"'
assert_field 'providers.azure.keys[0].azure_key_config.deployments' '.providers.azure.keys.[0].azure_key_config.deployments'

# Vertex key config
assert_field_value 'providers.vertex.keys[0].vertex_key_config.project_id' '.providers.vertex.keys.[0].vertex_key_config.project_id' '"my-project"'
assert_field_value 'providers.vertex.keys[0].vertex_key_config.region' '.providers.vertex.keys.[0].vertex_key_config.region' '"us-central1"'
assert_field_value 'providers.vertex.keys[0].vertex_key_config.auth_credentials' '.providers.vertex.keys.[0].vertex_key_config.auth_credentials' '"env.GOOGLE_CREDS"'

# Bedrock key config
assert_field_value 'providers.bedrock.keys[0].bedrock_key_config.region' '.providers.bedrock.keys.[0].bedrock_key_config.region' '"us-east-1"'
assert_field_value 'providers.bedrock.keys[0].bedrock_key_config.access_key' '.providers.bedrock.keys.[0].bedrock_key_config.access_key' '"env.AWS_KEY"'
assert_field_value 'providers.bedrock.keys[0].bedrock_key_config.secret_key' '.providers.bedrock.keys.[0].bedrock_key_config.secret_key' '"env.AWS_SECRET"'
assert_field_value 'providers.bedrock.keys[0].bedrock_key_config.session_token' '.providers.bedrock.keys.[0].bedrock_key_config.session_token' '"env.AWS_TOKEN"'
assert_field_value 'providers.bedrock.keys[0].bedrock_key_config.arn' '.providers.bedrock.keys.[0].bedrock_key_config.arn' '"arn:aws:iam::role/test"'

###############################################################################
# 4. Governance (budgets, rate_limits, customers, teams, virtual_keys, routing_rules, auth_config)
###############################################################################
echo ""
echo -e "${CYAN}🏛️  4/10 - Governance Configuration${NC}"
echo "-------------------------------------"

cat > "$TMPDIR/values-governance.yaml" << 'VALS'
image:
  tag: v1.0.0
bifrost:
  governance:
    budgets:
      - id: "budget-1"
        max_limit: 100
        reset_duration: "1M"
    rateLimits:
      - id: "rl-1"
        token_max_limit: 50000
        token_reset_duration: "1d"
        request_max_limit: 1000
        request_reset_duration: "1h"
    customers:
      - id: "cust-1"
        name: "Acme Corp"
        budget_id: "budget-1"
        rate_limit_id: "rl-1"
    teams:
      - id: "team-1"
        name: "Engineering"
        customer_id: "cust-1"
        budget_id: "budget-1"
        rate_limit_id: "rl-1"
        profile:
          department: "eng"
        config:
          max_tokens: 4096
        claims:
          role: "admin"
    virtualKeys:
      - id: "vk-1"
        name: "Test VK"
        value: "vk-test-value"
        description: "A test virtual key"
        is_active: true
        team_id: "team-1"
        customer_id: "cust-1"
        budget_id: "budget-1"
        rate_limit_id: "rl-1"
        provider_configs:
          - provider: "openai"
            weight: 1.0
            allowed_models:
              - "gpt-4o"
            budget_id: "budget-1"
            rate_limit_id: "rl-1"
            keys:
              - key_id: "key-uuid-1"
                name: "vk-provider-key"
                value: "sk-test"
        mcp_configs:
          - mcp_client_id: 1
            tools_to_execute:
              - "search"
              - "compute"
    routingRules:
      - id: "route-1"
        name: "Route GPT to Azure"
        description: "Redirect GPT models to Azure"
        enabled: true
        cel_expression: "request.model.startsWith('gpt-')"
        provider: "azure"
        model: "gpt-4o"
        fallbacks:
          - "openai"
        scope: "global"
        priority: 10
      - id: "route-2"
        name: "Team-scoped route"
        enabled: true
        cel_expression: "true"
        scope: "team"
        scope_id: "team-1"
        priority: 0
    authConfig:
      adminUsername: "admin"
      adminPassword: "secret"
      isEnabled: true
      disableAuthOnInference: true
VALS

render_config "$TMPDIR/values-governance.yaml"

# Budgets
assert_field_value 'governance.budgets[0].id' '.governance.budgets.[0].id' '"budget-1"'
assert_field_value 'governance.budgets[0].max_limit' '.governance.budgets.[0].max_limit' '100'
assert_field_value 'governance.budgets[0].reset_duration' '.governance.budgets.[0].reset_duration' '"1M"'

# Rate limits
assert_field_value 'governance.rate_limits[0].id' '.governance.rate_limits.[0].id' '"rl-1"'
assert_field_value 'governance.rate_limits[0].token_max_limit' '.governance.rate_limits.[0].token_max_limit' '50000'
assert_field_value 'governance.rate_limits[0].token_reset_duration' '.governance.rate_limits.[0].token_reset_duration' '"1d"'
assert_field_value 'governance.rate_limits[0].request_max_limit' '.governance.rate_limits.[0].request_max_limit' '1000'
assert_field_value 'governance.rate_limits[0].request_reset_duration' '.governance.rate_limits.[0].request_reset_duration' '"1h"'

# Customers
assert_field_value 'governance.customers[0].id' '.governance.customers.[0].id' '"cust-1"'
assert_field_value 'governance.customers[0].name' '.governance.customers.[0].name' '"Acme Corp"'
assert_field_value 'governance.customers[0].budget_id' '.governance.customers.[0].budget_id' '"budget-1"'
assert_field_value 'governance.customers[0].rate_limit_id' '.governance.customers.[0].rate_limit_id' '"rl-1"'

# Teams
assert_field_value 'governance.teams[0].id' '.governance.teams.[0].id' '"team-1"'
assert_field_value 'governance.teams[0].name' '.governance.teams.[0].name' '"Engineering"'
assert_field_value 'governance.teams[0].customer_id' '.governance.teams.[0].customer_id' '"cust-1"'
assert_field_value 'governance.teams[0].budget_id' '.governance.teams.[0].budget_id' '"budget-1"'
assert_field_value 'governance.teams[0].rate_limit_id' '.governance.teams.[0].rate_limit_id' '"rl-1"'
assert_field 'governance.teams[0].profile' '.governance.teams.[0].profile'
assert_field 'governance.teams[0].config' '.governance.teams.[0].config'
assert_field 'governance.teams[0].claims' '.governance.teams.[0].claims'

# Virtual keys
assert_field_value 'governance.virtual_keys[0].id' '.governance.virtual_keys.[0].id' '"vk-1"'
assert_field_value 'governance.virtual_keys[0].name' '.governance.virtual_keys.[0].name' '"Test VK"'
assert_field_value 'governance.virtual_keys[0].value' '.governance.virtual_keys.[0].value' '"vk-test-value"'
assert_field_value 'governance.virtual_keys[0].description' '.governance.virtual_keys.[0].description' '"A test virtual key"'
assert_field_value 'governance.virtual_keys[0].is_active' '.governance.virtual_keys.[0].is_active' 'true'
assert_field_value 'governance.virtual_keys[0].team_id' '.governance.virtual_keys.[0].team_id' '"team-1"'
assert_field_value 'governance.virtual_keys[0].customer_id' '.governance.virtual_keys.[0].customer_id' '"cust-1"'
assert_field_value 'governance.virtual_keys[0].budget_id' '.governance.virtual_keys.[0].budget_id' '"budget-1"'
assert_field_value 'governance.virtual_keys[0].rate_limit_id' '.governance.virtual_keys.[0].rate_limit_id' '"rl-1"'
assert_field 'governance.virtual_keys[0].provider_configs' '.governance.virtual_keys.[0].provider_configs'
assert_field_value 'governance.virtual_keys[0].provider_configs[0].provider' '.governance.virtual_keys.[0].provider_configs.[0].provider' '"openai"'
assert_field 'governance.virtual_keys[0].provider_configs[0].allowed_models' '.governance.virtual_keys.[0].provider_configs.[0].allowed_models'
assert_field 'governance.virtual_keys[0].provider_configs[0].keys' '.governance.virtual_keys.[0].provider_configs.[0].keys'
assert_field 'governance.virtual_keys[0].mcp_configs' '.governance.virtual_keys.[0].mcp_configs'
assert_field_value 'governance.virtual_keys[0].mcp_configs[0].mcp_client_id' '.governance.virtual_keys.[0].mcp_configs.[0].mcp_client_id' '1'
assert_field 'governance.virtual_keys[0].mcp_configs[0].tools_to_execute' '.governance.virtual_keys.[0].mcp_configs.[0].tools_to_execute'

# Routing rules
assert_field_value 'governance.routing_rules[0].id' '.governance.routing_rules.[0].id' '"route-1"'
assert_field_value 'governance.routing_rules[0].name' '.governance.routing_rules.[0].name' '"Route GPT to Azure"'
assert_field_value 'governance.routing_rules[0].description' '.governance.routing_rules.[0].description' '"Redirect GPT models to Azure"'
assert_field_value 'governance.routing_rules[0].enabled' '.governance.routing_rules.[0].enabled' 'true'
assert_field_value 'governance.routing_rules[0].cel_expression' '.governance.routing_rules.[0].cel_expression' '"request.model.startsWith('\''gpt-'\'')"'
assert_field_value 'governance.routing_rules[0].provider' '.governance.routing_rules.[0].provider' '"azure"'
assert_field_value 'governance.routing_rules[0].model' '.governance.routing_rules.[0].model' '"gpt-4o"'
assert_field 'governance.routing_rules[0].fallbacks' '.governance.routing_rules.[0].fallbacks'
assert_field_value 'governance.routing_rules[0].scope' '.governance.routing_rules.[0].scope' '"global"'
assert_field_value 'governance.routing_rules[0].priority' '.governance.routing_rules.[0].priority' '10'
assert_field_value 'governance.routing_rules[1].scope' '.governance.routing_rules.[1].scope' '"team"'
assert_field_value 'governance.routing_rules[1].scope_id' '.governance.routing_rules.[1].scope_id' '"team-1"'

# Auth config
assert_field_value 'governance.auth_config.admin_username' '.governance.auth_config.admin_username' '"admin"'
assert_field_value 'governance.auth_config.admin_password' '.governance.auth_config.admin_password' '"secret"'
assert_field_value 'governance.auth_config.is_enabled' '.governance.auth_config.is_enabled' 'true'
assert_field_value 'governance.auth_config.disable_auth_on_inference' '.governance.auth_config.disable_auth_on_inference' 'true'

###############################################################################
# 5. Top-level Auth Config
###############################################################################
echo ""
echo -e "${CYAN}🔐 5/10 - Top-level Auth Config${NC}"
echo "--------------------------------"

cat > "$TMPDIR/values-auth.yaml" << 'VALS'
image:
  tag: v1.0.0
bifrost:
  authConfig:
    adminUsername: "root"
    adminPassword: "rootpass"
    isEnabled: true
    disableAuthOnInference: false
VALS

render_config "$TMPDIR/values-auth.yaml"
assert_field_value 'auth_config.admin_username' '.auth_config.admin_username' '"root"'
assert_field_value 'auth_config.admin_password' '.auth_config.admin_password' '"rootpass"'
assert_field_value 'auth_config.is_enabled' '.auth_config.is_enabled' 'true'
assert_field_value 'auth_config.disable_auth_on_inference' '.auth_config.disable_auth_on_inference' 'false'

###############################################################################
# 6. Plugins (telemetry, logging, governance, maxim, semanticcache, otel, datadog, custom)
###############################################################################
echo ""
echo -e "${CYAN}🔌 6/10 - Plugins Configuration${NC}"
echo "--------------------------------"

cat > "$TMPDIR/values-plugins.yaml" << 'VALS'
image:
  tag: v1.0.0
bifrost:
  plugins:
    telemetry:
      enabled: true
      config:
        push_gateway:
          enabled: true
          push_gateway_url: "http://pushgateway:9091"
          job_name: "bifrost-test"
          instance_id: "node-1"
          push_interval: 30
          basic_auth:
            username: "prom"
            password: "prompass"
    logging:
      enabled: true
      config: {}
    governance:
      enabled: true
      config:
        is_vk_mandatory: true
    maxim:
      enabled: true
      config:
        api_key: "maxim-key-123"
        log_repo_id: "repo-456"
      secretRef:
        name: ""
        key: "api-key"
    semanticCache:
      enabled: true
      config:
        provider: "openai"
        keys:
          - "sk-embed-key"
        embedding_model: "text-embedding-3-small"
        dimension: 1536
        threshold: 0.85
        ttl: "10m"
        conversation_history_threshold: 5
        cache_by_model: true
        cache_by_provider: false
        exclude_system_prompt: true
        cleanup_on_shutdown: true
        vector_store_namespace: "bifrost-cache"
    otel:
      enabled: true
      config:
        service_name: "bifrost-test"
        collector_url: "otel-collector:4317"
        trace_type: "otel"
        protocol: "grpc"
        metrics_enabled: true
        metrics_endpoint: "otel-collector:4317"
        metrics_push_interval: 30
    datadog:
      enabled: true
      config:
        service_name: "bifrost-dd"
        agent_addr: "dd-agent:8126"
        env: "staging"
        version: "1.0.0"
        custom_tags:
          team: "platform"
        enable_traces: true
    custom:
      - name: "my-plugin"
        enabled: true
        path: "/plugins/my-plugin.so"
        version: 2
        config:
          key1: "val1"
vectorStore:
  enabled: true
  type: weaviate
  weaviate:
    enabled: true
VALS

render_config "$TMPDIR/values-plugins.yaml"

# Telemetry plugin
assert_field_value 'plugins: telemetry name' '.plugins.[0].name' '"telemetry"'
assert_field_value 'plugins: telemetry enabled' '.plugins.[0].enabled' 'true'
assert_field 'plugins: telemetry push_gateway' '.plugins.[0].config.push_gateway'

# Logging plugin
assert_field_value 'plugins: logging name' '.plugins.[1].name' '"logging"'

# Governance plugin
assert_field_value 'plugins: governance name' '.plugins.[2].name' '"governance"'
assert_field_value 'plugins: governance is_vk_mandatory' '.plugins.[2].config.is_vk_mandatory' 'true'

# Maxim plugin
assert_field_value 'plugins: maxim name' '.plugins.[3].name' '"maxim"'
assert_field_value 'plugins: maxim api_key' '.plugins.[3].config.api_key' '"maxim-key-123"'
assert_field_value 'plugins: maxim log_repo_id' '.plugins.[3].config.log_repo_id' '"repo-456"'

# Semantic cache plugin
assert_field_value 'plugins: semanticcache name' '.plugins.[4].name' '"semanticcache"'
assert_field_value 'plugins: semanticcache provider' '.plugins.[4].config.provider' '"openai"'
assert_field 'plugins: semanticcache keys' '.plugins.[4].config.keys'
assert_field_value 'plugins: semanticcache embedding_model' '.plugins.[4].config.embedding_model' '"text-embedding-3-small"'
assert_field_value 'plugins: semanticcache dimension' '.plugins.[4].config.dimension' '1536'
assert_field_value 'plugins: semanticcache threshold' '.plugins.[4].config.threshold' '0.85'
assert_field_value 'plugins: semanticcache ttl' '.plugins.[4].config.ttl' '"10m"'
assert_field_value 'plugins: semanticcache conversation_history_threshold' '.plugins.[4].config.conversation_history_threshold' '5'
assert_field_value 'plugins: semanticcache cache_by_model' '.plugins.[4].config.cache_by_model' 'true'
assert_field_value 'plugins: semanticcache cache_by_provider' '.plugins.[4].config.cache_by_provider' 'false'
assert_field_value 'plugins: semanticcache exclude_system_prompt' '.plugins.[4].config.exclude_system_prompt' 'true'
assert_field_value 'plugins: semanticcache cleanup_on_shutdown' '.plugins.[4].config.cleanup_on_shutdown' 'true'
assert_field_value 'plugins: semanticcache vector_store_namespace' '.plugins.[4].config.vector_store_namespace' '"bifrost-cache"'

# OTEL plugin
assert_field_value 'plugins: otel name' '.plugins.[5].name' '"otel"'
assert_field_value 'plugins: otel service_name' '.plugins.[5].config.service_name' '"bifrost-test"'
assert_field_value 'plugins: otel collector_url' '.plugins.[5].config.collector_url' '"otel-collector:4317"'
assert_field_value 'plugins: otel trace_type' '.plugins.[5].config.trace_type' '"otel"'
assert_field_value 'plugins: otel protocol' '.plugins.[5].config.protocol' '"grpc"'
assert_field_value 'plugins: otel metrics_enabled' '.plugins.[5].config.metrics_enabled' 'true'
assert_field_value 'plugins: otel metrics_endpoint' '.plugins.[5].config.metrics_endpoint' '"otel-collector:4317"'
assert_field_value 'plugins: otel metrics_push_interval' '.plugins.[5].config.metrics_push_interval' '30'

# Datadog plugin
assert_field_value 'plugins: datadog name' '.plugins.[6].name' '"datadog"'
assert_field_value 'plugins: datadog service_name' '.plugins.[6].config.service_name' '"bifrost-dd"'
assert_field_value 'plugins: datadog agent_addr' '.plugins.[6].config.agent_addr' '"dd-agent:8126"'
assert_field_value 'plugins: datadog env' '.plugins.[6].config.env' '"staging"'
assert_field_value 'plugins: datadog version' '.plugins.[6].config.version' '"1.0.0"'
assert_field 'plugins: datadog custom_tags' '.plugins.[6].config.custom_tags'
assert_field_value 'plugins: datadog enable_traces' '.plugins.[6].config.enable_traces' 'true'

# Custom plugin
assert_field_value 'plugins: custom name' '.plugins.[7].name' '"my-plugin"'
assert_field_value 'plugins: custom path' '.plugins.[7].path' '"/plugins/my-plugin.so"'
assert_field_value 'plugins: custom version' '.plugins.[7].version' '2'
assert_field 'plugins: custom config' '.plugins.[7].config'

###############################################################################
# 7. MCP Configuration
###############################################################################
echo ""
echo -e "${CYAN}🔧 7/10 - MCP Configuration${NC}"
echo "-----------------------------"

cat > "$TMPDIR/values-mcp.yaml" << 'VALS'
image:
  tag: v1.0.0
bifrost:
  mcp:
    enabled: true
    clientConfigs:
      - name: "stdio-server"
        connectionType: "stdio"
        stdioConfig:
          command: "/usr/bin/mcp-server"
          args:
            - "--port"
            - "3000"
          envs:
            - "MCP_TOKEN=abc"
      - name: "http-server"
        connectionType: "http"
        httpConfig:
          url: "https://mcp.example.com/v1"
      - name: "ws-server"
        connectionType: "websocket"
        websocketConfig:
          url: "wss://mcp.example.com/ws"
    toolManagerConfig:
      toolExecutionTimeout: 60
      maxAgentDepth: 5
VALS

render_config "$TMPDIR/values-mcp.yaml"
assert_field 'mcp.client_configs' '.mcp.client_configs'

# stdio client
assert_field_value 'mcp client[0] name' '.mcp.client_configs.[0].name' '"stdio-server"'
assert_field_value 'mcp client[0] connection_type' '.mcp.client_configs.[0].connection_type' '"stdio"'
assert_field_value 'mcp client[0] stdio_config.command' '.mcp.client_configs.[0].stdio_config.command' '"/usr/bin/mcp-server"'
assert_field 'mcp client[0] stdio_config.args' '.mcp.client_configs.[0].stdio_config.args'
assert_field 'mcp client[0] stdio_config.envs' '.mcp.client_configs.[0].stdio_config.envs'

# http client (mapped to connection_string)
assert_field_value 'mcp client[1] name' '.mcp.client_configs.[1].name' '"http-server"'
assert_field_value 'mcp client[1] connection_type' '.mcp.client_configs.[1].connection_type' '"http"'
assert_field_value 'mcp client[1] connection_string' '.mcp.client_configs.[1].connection_string' '"https://mcp.example.com/v1"'

# websocket client (mapped to sse + connection_string)
assert_field_value 'mcp client[2] name' '.mcp.client_configs.[2].name' '"ws-server"'
assert_field_value 'mcp client[2] connection_type (ws->sse)' '.mcp.client_configs.[2].connection_type' '"sse"'
assert_field_value 'mcp client[2] connection_string' '.mcp.client_configs.[2].connection_string' '"wss://mcp.example.com/ws"'

# Tool manager config
assert_field_value 'mcp tool_manager_config.tool_execution_timeout' '.mcp.tool_manager_config.tool_execution_timeout' '60'
assert_field_value 'mcp tool_manager_config.max_agent_depth' '.mcp.tool_manager_config.max_agent_depth' '5'

###############################################################################
# 8. Cluster, SAML, Load Balancer, Guardrails, Audit Logs
###############################################################################
echo ""
echo -e "${CYAN}🌐 8/10 - Cluster, SAML, LB, Guardrails, Audit Logs${NC}"
echo "-----------------------------------------------------"

cat > "$TMPDIR/values-cluster.yaml" << 'VALS'
image:
  tag: v1.0.0
bifrost:
  cluster:
    enabled: true
    peers:
      - "bifrost-0.bifrost-headless:7946"
      - "bifrost-1.bifrost-headless:7946"
    gossip:
      port: 7946
      config:
        timeoutSeconds: 10
        successThreshold: 3
        failureThreshold: 3
    discovery:
      enabled: true
      type: "kubernetes"
      allowedAddressSpace:
        - "10.0.0.0/8"
      k8sNamespace: "bifrost"
      k8sLabelSelector: "app=bifrost"
VALS

render_config "$TMPDIR/values-cluster.yaml"
assert_field_value 'cluster_config.enabled' '.cluster_config.enabled' 'true'
assert_field 'cluster_config.peers' '.cluster_config.peers'
assert_field_value 'cluster_config.gossip.port' '.cluster_config.gossip.port' '7946'
assert_field_value 'cluster_config.gossip.config.timeout_seconds' '.cluster_config.gossip.config.timeout_seconds' '10'
assert_field_value 'cluster_config.gossip.config.success_threshold' '.cluster_config.gossip.config.success_threshold' '3'
assert_field_value 'cluster_config.gossip.config.failure_threshold' '.cluster_config.gossip.config.failure_threshold' '3'
assert_field_value 'cluster_config.discovery.enabled' '.cluster_config.discovery.enabled' 'true'
assert_field_value 'cluster_config.discovery.type' '.cluster_config.discovery.type' '"kubernetes"'
assert_field 'cluster_config.discovery.allowed_address_space' '.cluster_config.discovery.allowed_address_space'
assert_field_value 'cluster_config.discovery.k8s_namespace' '.cluster_config.discovery.k8s_namespace' '"bifrost"'
assert_field_value 'cluster_config.discovery.k8s_label_selector' '.cluster_config.discovery.k8s_label_selector' '"app=bifrost"'

# SAML - Okta
cat > "$TMPDIR/values-saml-okta.yaml" << 'VALS'
image:
  tag: v1.0.0
bifrost:
  saml:
    enabled: true
    provider: "okta"
    config:
      issuerUrl: "https://dev-123.okta.com/oauth2/default"
      clientId: "okta-client-id"
      clientSecret: "okta-client-secret"
      audience: "api://default"
      userIdField: "sub"
      teamIdsField: "groups"
      rolesField: "roles"
VALS

render_config "$TMPDIR/values-saml-okta.yaml"
assert_field_value 'saml_config.enabled' '.saml_config.enabled' 'true'
assert_field_value 'saml_config.provider' '.saml_config.provider' '"okta"'
assert_field 'saml_config.config' '.saml_config.config'

# SAML - Entra
cat > "$TMPDIR/values-saml-entra.yaml" << 'VALS'
image:
  tag: v1.0.0
bifrost:
  saml:
    enabled: true
    provider: "entra"
    config:
      tenantId: "tenant-uuid"
      clientId: "entra-client-id"
      clientSecret: "entra-secret"
      audience: "api://entra"
      appIdUri: "api://entra-client-id"
      userIdField: "oid"
      teamIdsField: "groups"
      rolesField: "roles"
VALS

render_config "$TMPDIR/values-saml-entra.yaml"
assert_field_value 'saml_config (entra) provider' '.saml_config.provider' '"entra"'
assert_field 'saml_config (entra) config' '.saml_config.config'

# Load Balancer
cat > "$TMPDIR/values-lb.yaml" << 'VALS'
image:
  tag: v1.0.0
bifrost:
  loadBalancer:
    enabled: true
    trackerConfig:
      window_size: 100
    bootstrap:
      route_metrics: {}
      direction_metrics: {}
      routes: {}
VALS

render_config "$TMPDIR/values-lb.yaml"
assert_field_value 'load_balancer_config.enabled' '.load_balancer_config.enabled' 'true'
assert_field 'load_balancer_config.tracker_config' '.load_balancer_config.tracker_config'
assert_field 'load_balancer_config.bootstrap' '.load_balancer_config.bootstrap'

# Guardrails
cat > "$TMPDIR/values-guardrails.yaml" << 'VALS'
image:
  tag: v1.0.0
bifrost:
  guardrails:
    rules:
      - id: 1
        name: "Block PII"
        description: "Block PII in requests"
        enabled: true
        cel_expression: "!contains(request.body, 'SSN')"
        apply_to: "input"
        sampling_rate: 100
        timeout: 1000
        provider_config_ids:
          - 1
    providers:
      - id: 1
        provider_name: "bedrock"
        policy_name: "content-filter"
        enabled: true
        config:
          guardrailId: "abc"
VALS

render_config "$TMPDIR/values-guardrails.yaml"
assert_field 'guardrails_config.guardrail_rules' '.guardrails_config.guardrail_rules'
assert_field_value 'guardrails rule[0].id' '.guardrails_config.guardrail_rules.[0].id' '1'
assert_field_value 'guardrails rule[0].name' '.guardrails_config.guardrail_rules.[0].name' '"Block PII"'
assert_field_value 'guardrails rule[0].description' '.guardrails_config.guardrail_rules.[0].description' '"Block PII in requests"'
assert_field_value 'guardrails rule[0].enabled' '.guardrails_config.guardrail_rules.[0].enabled' 'true'
assert_field_value 'guardrails rule[0].apply_to' '.guardrails_config.guardrail_rules.[0].apply_to' '"input"'
assert_field_value 'guardrails rule[0].sampling_rate' '.guardrails_config.guardrail_rules.[0].sampling_rate' '100'
assert_field_value 'guardrails rule[0].timeout' '.guardrails_config.guardrail_rules.[0].timeout' '1000'
assert_field 'guardrails rule[0].provider_config_ids' '.guardrails_config.guardrail_rules.[0].provider_config_ids'

assert_field 'guardrails_config.guardrail_providers' '.guardrails_config.guardrail_providers'
assert_field_value 'guardrails provider[0].id' '.guardrails_config.guardrail_providers.[0].id' '1'
assert_field_value 'guardrails provider[0].provider_name' '.guardrails_config.guardrail_providers.[0].provider_name' '"bedrock"'
assert_field_value 'guardrails provider[0].policy_name' '.guardrails_config.guardrail_providers.[0].policy_name' '"content-filter"'
assert_field_value 'guardrails provider[0].enabled' '.guardrails_config.guardrail_providers.[0].enabled' 'true'
assert_field 'guardrails provider[0].config' '.guardrails_config.guardrail_providers.[0].config'

# Audit Logs
cat > "$TMPDIR/values-audit.yaml" << 'VALS'
image:
  tag: v1.0.0
bifrost:
  auditLogs:
    disabled: false
    hmacKey: "my-hmac-secret-key-32-bytes-long!"
VALS

render_config "$TMPDIR/values-audit.yaml"
assert_field_value 'audit_logs.disabled' '.audit_logs.disabled' 'false'
assert_field_value 'audit_logs.hmac_key' '.audit_logs.hmac_key' '"my-hmac-secret-key-32-bytes-long!"'

###############################################################################
# 9. Vector Store Types (weaviate, redis, qdrant, pinecone) with all fields
###############################################################################
echo ""
echo -e "${CYAN}🗄️  9/10 - Vector Store Config Fields${NC}"
echo "--------------------------------------"

# Weaviate with all fields
cat > "$TMPDIR/values-vs-weaviate.yaml" << 'VALS'
image:
  tag: v1.0.0
vectorStore:
  enabled: true
  type: weaviate
  weaviate:
    external:
      enabled: true
      scheme: https
      host: "weaviate.example.com:443"
      apiKey: "wv-api-key"
      grpcHost: "weaviate-grpc.example.com:443"
      grpcSecured: true
      timeout: "10s"
      className: "BifrostCache"
VALS

render_config "$TMPDIR/values-vs-weaviate.yaml"
assert_field_value 'vector_store.type (weaviate)' '.vector_store.type' '"weaviate"'
assert_field_value 'vector_store.enabled (weaviate)' '.vector_store.enabled' 'true'
assert_field_value 'weaviate config.scheme' '.vector_store.config.scheme' '"https"'
assert_field_value 'weaviate config.host' '.vector_store.config.host' '"weaviate.example.com:443"'
assert_field_value 'weaviate config.api_key' '.vector_store.config.api_key' '"wv-api-key"'
assert_field_value 'weaviate config.grpc_config.host' '.vector_store.config.grpc_config.host' '"weaviate-grpc.example.com:443"'
assert_field_value 'weaviate config.grpc_config.secured' '.vector_store.config.grpc_config.secured' 'true'
assert_field_value 'weaviate config.timeout' '.vector_store.config.timeout' '"10s"'
assert_field_value 'weaviate config.class_name' '.vector_store.config.class_name' '"BifrostCache"'

# Redis with all fields
cat > "$TMPDIR/values-vs-redis.yaml" << 'VALS'
image:
  tag: v1.0.0
vectorStore:
  enabled: true
  type: redis
  redis:
    external:
      enabled: true
      host: "redis.example.com"
      port: 6380
      username: "redisuser"
      password: "redispass"
      database: 3
      poolSize: 50
      maxActiveConns: 100
      minIdleConns: 5
      maxIdleConns: 20
      connMaxLifetime: "30m"
      connMaxIdleTime: "5m"
      dialTimeout: "5s"
      readTimeout: "3s"
      writeTimeout: "3s"
      contextTimeout: "10s"
VALS

render_config "$TMPDIR/values-vs-redis.yaml"
assert_field_value 'vector_store.type (redis)' '.vector_store.type' '"redis"'
assert_field_value 'redis config.addr' '.vector_store.config.addr' '"redis.example.com:6380"'
assert_field_value 'redis config.username' '.vector_store.config.username' '"redisuser"'
assert_field_value 'redis config.password' '.vector_store.config.password' '"redispass"'
assert_field_value 'redis config.db' '.vector_store.config.db' '3'
assert_field_value 'redis config.pool_size' '.vector_store.config.pool_size' '50'
assert_field_value 'redis config.max_active_conns' '.vector_store.config.max_active_conns' '100'
assert_field_value 'redis config.min_idle_conns' '.vector_store.config.min_idle_conns' '5'
assert_field_value 'redis config.max_idle_conns' '.vector_store.config.max_idle_conns' '20'
assert_field_value 'redis config.conn_max_lifetime' '.vector_store.config.conn_max_lifetime' '"30m"'
assert_field_value 'redis config.conn_max_idle_time' '.vector_store.config.conn_max_idle_time' '"5m"'
assert_field_value 'redis config.dial_timeout' '.vector_store.config.dial_timeout' '"5s"'
assert_field_value 'redis config.read_timeout' '.vector_store.config.read_timeout' '"3s"'
assert_field_value 'redis config.write_timeout' '.vector_store.config.write_timeout' '"3s"'
assert_field_value 'redis config.context_timeout' '.vector_store.config.context_timeout' '"10s"'

# Qdrant with all fields
cat > "$TMPDIR/values-vs-qdrant.yaml" << 'VALS'
image:
  tag: v1.0.0
vectorStore:
  enabled: true
  type: qdrant
  qdrant:
    external:
      enabled: true
      host: "qdrant.example.com"
      port: 6334
      apiKey: "qdrant-api-key"
      useTls: true
VALS

render_config "$TMPDIR/values-vs-qdrant.yaml"
assert_field_value 'vector_store.type (qdrant)' '.vector_store.type' '"qdrant"'
assert_field_value 'qdrant config.host' '.vector_store.config.host' '"qdrant.example.com"'
assert_field_value 'qdrant config.port' '.vector_store.config.port' '6334'
assert_field_value 'qdrant config.api_key' '.vector_store.config.api_key' '"qdrant-api-key"'
assert_field_value 'qdrant config.use_tls' '.vector_store.config.use_tls' 'true'

# Pinecone with all fields
cat > "$TMPDIR/values-vs-pinecone.yaml" << 'VALS'
image:
  tag: v1.0.0
vectorStore:
  enabled: true
  type: pinecone
  pinecone:
    external:
      enabled: true
      apiKey: "pinecone-api-key"
      indexHost: "my-index.svc.us-east1.pinecone.io"
VALS

render_config "$TMPDIR/values-vs-pinecone.yaml"
assert_field_value 'vector_store.type (pinecone)' '.vector_store.type' '"pinecone"'
assert_field_value 'pinecone config.api_key' '.vector_store.config.api_key' '"pinecone-api-key"'
assert_field_value 'pinecone config.index_host' '.vector_store.config.index_host' '"my-index.svc.us-east1.pinecone.io"'

###############################################################################
# 10. Config Store & Logs Store (sqlite + postgres)
###############################################################################
echo ""
echo -e "${CYAN}💾 10/10 - Config Store & Logs Store${NC}"
echo "--------------------------------------"

# SQLite stores
cat > "$TMPDIR/values-stores-sqlite.yaml" << 'VALS'
image:
  tag: v1.0.0
storage:
  mode: sqlite
  configStore:
    enabled: true
  logsStore:
    enabled: true
VALS

render_config "$TMPDIR/values-stores-sqlite.yaml"
assert_field_value 'config_store.type (sqlite)' '.config_store.type' '"sqlite"'
assert_field_value 'config_store.enabled' '.config_store.enabled' 'true'
assert_field 'config_store.config.path' '.config_store.config.path'
assert_field_value 'logs_store.type (sqlite)' '.logs_store.type' '"sqlite"'
assert_field_value 'logs_store.enabled' '.logs_store.enabled' 'true'
assert_field 'logs_store.config.path' '.logs_store.config.path'

# Postgres stores
cat > "$TMPDIR/values-stores-pg.yaml" << 'VALS'
image:
  tag: v1.0.0
storage:
  mode: postgres
  configStore:
    enabled: true
    maxIdleConns: 10
    maxOpenConns: 100
  logsStore:
    enabled: true
    maxIdleConns: 5
    maxOpenConns: 50
postgresql:
  enabled: true
  auth:
    username: bifrost
    password: testpass
    database: bifrost
VALS

render_config "$TMPDIR/values-stores-pg.yaml"
assert_field_value 'config_store.type (postgres)' '.config_store.type' '"postgres"'
assert_field 'config_store.config.host' '.config_store.config.host'
assert_field 'config_store.config.port' '.config_store.config.port'
assert_field 'config_store.config.user' '.config_store.config.user'
assert_field 'config_store.config.password' '.config_store.config.password'
assert_field 'config_store.config.db_name' '.config_store.config.db_name'
assert_field 'config_store.config.ssl_mode' '.config_store.config.ssl_mode'
assert_field_value 'config_store.config.max_idle_conns' '.config_store.config.max_idle_conns' '10'
assert_field_value 'config_store.config.max_open_conns' '.config_store.config.max_open_conns' '100'
assert_field_value 'logs_store.type (postgres)' '.logs_store.type' '"postgres"'
assert_field_value 'logs_store.config.max_idle_conns' '.logs_store.config.max_idle_conns' '5'
assert_field_value 'logs_store.config.max_open_conns' '.logs_store.config.max_open_conns' '50'

###############################################################################
# Summary
###############################################################################
echo ""
echo "======================================================================"
echo "🏁 Config JSON Field Validation Complete!"
echo "======================================================================"
echo -e "${GREEN}Passed: $TESTS_PASSED${NC}"
echo -e "${RED}Failed: $TESTS_FAILED${NC}"
echo ""

if [ "$TESTS_FAILED" -gt 0 ]; then
  echo -e "${RED}❌ Some field validations failed. Please review the output above.${NC}"
  exit 1
else
  echo -e "${GREEN}✅ All config.json field validations passed!${NC}"
  exit 0
fi
