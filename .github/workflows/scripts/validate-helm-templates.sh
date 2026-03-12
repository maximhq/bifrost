#!/usr/bin/env bash
set -euo pipefail

# Helm template validation script for Bifrost
# Validates all storage and vector store combinations render correctly

echo "🔍 Validating Helm Chart Templates..."
echo "======================================"

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# Track test results
TESTS_PASSED=0
TESTS_FAILED=0

# Function to report test result
report_result() {
  local test_name=$1
  local result=$2
  
  if [ "$result" -eq 0 ]; then
    echo -e "${GREEN}✅ $test_name${NC}"
    TESTS_PASSED=$((TESTS_PASSED + 1))
  else
    echo -e "${RED}❌ $test_name${NC}"
    TESTS_FAILED=$((TESTS_FAILED + 1))
  fi
}

# Function to test a helm template combination
test_template() {
  local test_name=$1
  shift
  local helm_args=("$@")
  
  if helm template bifrost ./helm-charts/bifrost \
    --set image.tag=v1.0.0 \
    "${helm_args[@]}" \
    > /tmp/helm-template-output.yaml 2>&1; then
    report_result "$test_name" 0
    return 0
  else
    report_result "$test_name" 1
    echo -e "${YELLOW}  Error output:${NC}"
    head -10 /tmp/helm-template-output.yaml | sed 's/^/    /'
    return 1
  fi
}

# 1. Storage Combinations (9 tests)
echo ""
echo -e "${CYAN}📦 1/5 - Testing Storage Combinations (9 tests)...${NC}"
echo "---------------------------------------------------"

# config=no, logs=no
test_template "config=no, logs=no" \
  --set storage.configStore.enabled=false \
  --set storage.logsStore.enabled=false \
  --set postgresql.enabled=false

# config=no, logs=sqlite
test_template "config=no, logs=sqlite" \
  --set storage.configStore.enabled=false \
  --set storage.logsStore.enabled=true \
  --set storage.mode=sqlite \
  --set postgresql.enabled=false

# config=no, logs=postgres
test_template "config=no, logs=postgres" \
  --set storage.configStore.enabled=false \
  --set storage.logsStore.enabled=true \
  --set storage.mode=postgres \
  --set postgresql.enabled=true \
  --set postgresql.auth.password=testpass

# config=sqlite, logs=no
test_template "config=sqlite, logs=no" \
  --set storage.configStore.enabled=true \
  --set storage.logsStore.enabled=false \
  --set storage.mode=sqlite \
  --set postgresql.enabled=false

# config=sqlite, logs=sqlite
test_template "config=sqlite, logs=sqlite" \
  --set storage.configStore.enabled=true \
  --set storage.logsStore.enabled=true \
  --set storage.mode=sqlite \
  --set postgresql.enabled=false

# config=sqlite, logs=postgres
test_template "config=sqlite, logs=postgres" \
  --set storage.configStore.enabled=true \
  --set storage.logsStore.enabled=true \
  --set storage.mode=postgres \
  --set postgresql.enabled=true \
  --set postgresql.auth.password=testpass

# config=postgres, logs=no
test_template "config=postgres, logs=no" \
  --set storage.configStore.enabled=true \
  --set storage.logsStore.enabled=false \
  --set storage.mode=postgres \
  --set postgresql.enabled=true \
  --set postgresql.auth.password=testpass

# config=postgres, logs=sqlite
test_template "config=postgres, logs=sqlite" \
  --set storage.configStore.enabled=true \
  --set storage.logsStore.enabled=true \
  --set storage.mode=postgres \
  --set postgresql.enabled=true \
  --set postgresql.auth.password=testpass

# config=postgres, logs=postgres
test_template "config=postgres, logs=postgres" \
  --set storage.configStore.enabled=true \
  --set storage.logsStore.enabled=true \
  --set storage.mode=postgres \
  --set postgresql.enabled=true \
  --set postgresql.auth.password=testpass

# 2. Vector Store Combinations (6 tests)
echo ""
echo -e "${CYAN}🗄️  2/5 - Testing Vector Store Combinations (6 tests)...${NC}"
echo "--------------------------------------------------------"

# Weaviate
test_template "vectorStore=weaviate" \
  --set vectorStore.enabled=true \
  --set vectorStore.type=weaviate \
  --set vectorStore.weaviate.enabled=true

# Redis
test_template "vectorStore=redis" \
  --set vectorStore.enabled=true \
  --set vectorStore.type=redis \
  --set vectorStore.redis.enabled=true

# Qdrant
test_template "vectorStore=qdrant" \
  --set vectorStore.enabled=true \
  --set vectorStore.type=qdrant \
  --set vectorStore.qdrant.enabled=true

# postgres + weaviate
test_template "postgres + weaviate" \
  --set storage.mode=postgres \
  --set postgresql.enabled=true \
  --set postgresql.auth.password=testpass \
  --set vectorStore.enabled=true \
  --set vectorStore.type=weaviate \
  --set vectorStore.weaviate.enabled=true

# postgres + qdrant
test_template "postgres + qdrant" \
  --set storage.mode=postgres \
  --set postgresql.enabled=true \
  --set postgresql.auth.password=testpass \
  --set vectorStore.enabled=true \
  --set vectorStore.type=qdrant \
  --set vectorStore.qdrant.enabled=true

# sqlite + qdrant
test_template "sqlite + qdrant" \
  --set storage.mode=sqlite \
  --set postgresql.enabled=false \
  --set vectorStore.enabled=true \
  --set vectorStore.type=qdrant \
  --set vectorStore.qdrant.enabled=true

# 3. Special Configurations (7 tests)
echo ""
echo -e "${CYAN}⚙️  3/5 - Testing Special Configurations (7 tests)...${NC}"
echo "-----------------------------------------------------"

# semantic cache: direct mode (dimension: 1, no provider/keys)
test_template "semanticCache: direct mode (dimension: 1)" \
  --set bifrost.plugins.semanticCache.enabled=true \
  --set bifrost.plugins.semanticCache.config.dimension=1 \
  --set bifrost.plugins.semanticCache.config.ttl=30m \
  --set vectorStore.enabled=true \
  --set vectorStore.type=redis \
  --set vectorStore.redis.enabled=true

# semantic cache: semantic mode (dimension > 1, requires provider/keys)
test_template "semanticCache: semantic mode (dimension: 1536)" \
  --set bifrost.plugins.semanticCache.enabled=true \
  --set bifrost.plugins.semanticCache.config.dimension=1536 \
  --set bifrost.plugins.semanticCache.config.provider=openai \
  --set 'bifrost.plugins.semanticCache.config.keys[0]=sk-test' \
  --set vectorStore.enabled=true \
  --set vectorStore.type=redis \
  --set vectorStore.redis.enabled=true

# semantic cache: direct mode with redis + postgres
test_template "semanticCache: direct mode + postgres" \
  --set bifrost.plugins.semanticCache.enabled=true \
  --set bifrost.plugins.semanticCache.config.dimension=1 \
  --set storage.mode=postgres \
  --set postgresql.enabled=true \
  --set postgresql.auth.password=testpass \
  --set vectorStore.enabled=true \
  --set vectorStore.type=redis \
  --set vectorStore.redis.enabled=true

# sqlite + persistence + autoscaling (StatefulSet HPA)
test_template "sqlite + persistence + autoscaling (StatefulSet)" \
  --set storage.mode=sqlite \
  --set storage.persistence.enabled=true \
  --set postgresql.enabled=false \
  --set autoscaling.enabled=true \
  --set autoscaling.minReplicas=2 \
  --set autoscaling.maxReplicas=5

# postgres + autoscaling (Deployment HPA)
test_template "postgres + autoscaling (Deployment)" \
  --set storage.mode=postgres \
  --set postgresql.enabled=true \
  --set postgresql.auth.password=testpass \
  --set autoscaling.enabled=true \
  --set autoscaling.minReplicas=2 \
  --set autoscaling.maxReplicas=5

# ingress enabled
test_template "ingress enabled" \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set 'ingress.hosts[0].host=bifrost.example.com' \
  --set 'ingress.hosts[0].paths[0].path=/' \
  --set 'ingress.hosts[0].paths[0].pathType=Prefix'

# full production-like config
test_template "production-like config" \
  --set storage.mode=postgres \
  --set postgresql.enabled=true \
  --set postgresql.auth.password=testpass \
  --set vectorStore.enabled=true \
  --set vectorStore.type=qdrant \
  --set vectorStore.qdrant.enabled=true \
  --set autoscaling.enabled=true \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set 'ingress.hosts[0].host=bifrost.example.com' \
  --set 'ingress.hosts[0].paths[0].path=/' \
  --set 'ingress.hosts[0].paths[0].pathType=Prefix'

# 4. New Property Rendering (Gap 1-8 tests)
echo ""
echo -e "${CYAN}🆕 4/5 - Testing New Property Rendering (Gap 1-8)...${NC}"
echo "-----------------------------------------------------"

# Gap 1+2: Client new properties
test_template "client: new properties (Gap 1+2)" \
  --set bifrost.client.asyncJobResultTTL=300 \
  --set 'bifrost.client.requiredHeaders[0]=X-Request-ID' \
  --set 'bifrost.client.loggingHeaders[0]=X-Trace-ID' \
  --set 'bifrost.client.allowedHeaders[0]=Authorization' \
  --set bifrost.client.mcpAgentDepth=5 \
  --set bifrost.client.mcpToolExecutionTimeout=30 \
  --set bifrost.client.mcpCodeModeBindingLevel=server \
  --set bifrost.client.mcpToolSyncInterval=60 \
  --set bifrost.client.hideDeletedVirtualKeysInFilters=true

# Gap 3: OTel plugin with new fields
test_template "otel: headers + tls_ca_cert + insecure (Gap 3)" \
  --set bifrost.plugins.otel.enabled=true \
  --set bifrost.plugins.otel.config.collector_url=otel:4317 \
  --set bifrost.plugins.otel.config.trace_type=otel \
  --set bifrost.plugins.otel.config.protocol=grpc \
  --set 'bifrost.plugins.otel.config.headers.Authorization=Bearer token' \
  --set bifrost.plugins.otel.config.tls_ca_cert=/certs/ca.pem \
  --set bifrost.plugins.otel.config.insecure=true

# Gap 4: Governance plugin with new fields
test_template "governance: required_headers + is_enterprise (Gap 4)" \
  --set bifrost.plugins.governance.enabled=true \
  --set 'bifrost.plugins.governance.config.required_headers[0]=X-Team-ID' \
  --set bifrost.plugins.governance.config.is_enterprise=true

# Gap 5: Governance modelConfigs + providers
test_template "governance: modelConfigs + providers (Gap 5)" \
  --set 'bifrost.governance.modelConfigs[0].id=mc-1' \
  --set 'bifrost.governance.modelConfigs[0].model_name=gpt-4o' \
  --set 'bifrost.governance.providers[0].name=openai'

# Gap 6: MCP new fields
test_template "mcp: toolSyncInterval + codeModeBindingLevel (Gap 6)" \
  --set bifrost.mcp.enabled=true \
  --set bifrost.mcp.toolSyncInterval=10m \
  --set bifrost.mcp.toolManagerConfig.codeModeBindingLevel=server \
  --set 'bifrost.mcp.clientConfigs[0].name=test' \
  --set 'bifrost.mcp.clientConfigs[0].connectionType=http' \
  --set 'bifrost.mcp.clientConfigs[0].httpConfig.url=http://localhost:3000' \
  --set 'bifrost.mcp.clientConfigs[0].clientId=client-1' \
  --set 'bifrost.mcp.clientConfigs[0].isCodeModeClient=true' \
  --set 'bifrost.mcp.clientConfigs[0].toolSyncInterval=5m'

# Gap 7: Cluster with region
test_template "cluster: region (Gap 7)" \
  --set bifrost.cluster.enabled=true \
  --set 'bifrost.cluster.peers[0]=peer-0:7946' \
  --set bifrost.cluster.gossip.port=7946 \
  --set bifrost.cluster.gossip.config.timeoutSeconds=10 \
  --set bifrost.cluster.gossip.config.successThreshold=3 \
  --set bifrost.cluster.gossip.config.failureThreshold=3 \
  --set bifrost.cluster.region=us-east-1

# Gap 8: Combined production-like with all new fields
test_template "combined: all new Gap 1-8 fields" \
  --set bifrost.client.asyncJobResultTTL=300 \
  --set bifrost.client.mcpAgentDepth=5 \
  --set bifrost.client.hideDeletedVirtualKeysInFilters=true \
  --set bifrost.plugins.otel.enabled=true \
  --set bifrost.plugins.otel.config.collector_url=otel:4317 \
  --set bifrost.plugins.otel.config.trace_type=otel \
  --set bifrost.plugins.otel.config.protocol=grpc \
  --set bifrost.plugins.otel.config.insecure=true \
  --set bifrost.plugins.governance.enabled=true \
  --set bifrost.plugins.governance.config.is_enterprise=true \
  --set bifrost.cluster.enabled=true \
  --set 'bifrost.cluster.peers[0]=peer-0:7946' \
  --set bifrost.cluster.gossip.port=7946 \
  --set bifrost.cluster.gossip.config.timeoutSeconds=10 \
  --set bifrost.cluster.gossip.config.successThreshold=3 \
  --set bifrost.cluster.gossip.config.failureThreshold=3 \
  --set bifrost.cluster.region=us-west-2

# 5. Plugin Name Validation
echo ""
echo -e "${CYAN}🔌 5/5 - Validating Plugin Names Match Go Registry...${NC}"
echo "------------------------------------------------------"

# Verify semantic cache plugin renders with correct name ("semantic_cache", not "semantic_cache")
# Go registry: plugins/semantic_cache/main.go defines PluginName = "semantic_cache"
test_name="semanticCache plugin name matches Go registry (semantic_cache)"
if helm template bifrost ./helm-charts/bifrost \
  --set image.tag=v1.0.0 \
  --set bifrost.plugins.semanticCache.enabled=true \
  --set bifrost.plugins.semanticCache.config.dimension=1536 \
  --set bifrost.plugins.semanticCache.config.provider=openai \
  --set 'bifrost.plugins.semanticCache.config.keys[0]=sk-test' \
  --set vectorStore.enabled=true \
  --set vectorStore.type=redis \
  --set vectorStore.redis.enabled=true \
  > /tmp/helm-template-output.yaml 2>&1; then
  if grep -Eq '"name"[[:space:]]*:[[:space:]]*"semantic_cache"' /tmp/helm-template-output.yaml; then
    report_result "$test_name" 0
  else
    report_result "$test_name" 1
  fi
else
  report_result "$test_name" 1
  echo -e "${YELLOW}  Error output:${NC}"
  head -10 /tmp/helm-template-output.yaml | sed 's/^/    /'
fi

# Cleanup
rm -f /tmp/helm-template-output.yaml

# Final Summary
echo ""
echo "======================================"
echo "🏁 Helm Template Validation Complete!"
echo "======================================"
echo -e "${GREEN}Passed: $TESTS_PASSED${NC}"
echo -e "${RED}Failed: $TESTS_FAILED${NC}"
echo ""

if [ "$TESTS_FAILED" -gt 0 ]; then
  echo -e "${RED}❌ Some template validations failed. Please review the output above.${NC}"
  exit 1
else
  echo -e "${GREEN}✅ All template validations passed successfully!${NC}"
  exit 0
fi
