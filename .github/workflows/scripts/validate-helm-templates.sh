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
echo -e "${CYAN}📦 1/9 - Testing Storage Combinations (9 tests)...${NC}"
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
echo -e "${CYAN}🗄️  2/9 - Testing Vector Store Combinations (6 tests)...${NC}"
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
echo -e "${CYAN}⚙️  3/9 - Testing Special Configurations (7 tests)...${NC}"
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
echo -e "${CYAN}🆕 4/9 - Testing New Property Rendering (Gap 1-8)...${NC}"
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
  --set bifrost.plugins.otel.config.trace_type=genai_extension \
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

# Gap 9: Server config
test_template "server: readBufferSize (Gap 9)" \
  --set bifrost.server.readBufferSize=131072

# Gap 8: Combined production-like with all new fields
test_template "combined: all new Gap 1-9 fields" \
  --set bifrost.client.asyncJobResultTTL=300 \
  --set bifrost.client.mcpAgentDepth=5 \
  --set bifrost.client.hideDeletedVirtualKeysInFilters=true \
  --set bifrost.plugins.otel.enabled=true \
  --set bifrost.plugins.otel.config.collector_url=otel:4317 \
  --set bifrost.plugins.otel.config.trace_type=genai_extension \
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
  --set bifrost.cluster.region=us-west-2 \
  --set bifrost.server.readBufferSize=131072

# 5. Plugin Name Validation
echo ""
echo -e "${CYAN}🔌 5/9 - Validating Plugin Names Match Go Registry...${NC}"
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

# 6. Custom Plugin Placement and Order Rendering
echo ""
echo -e "${CYAN}🔧 6/9 - Validating Custom Plugin placement and order Rendering...${NC}"
echo "-------------------------------------------------------------------"

# Test custom plugin renders successfully with placement and order
test_template "custom plugin with placement and order" \
  --set 'bifrost.plugins.custom[0].name=my-plugin' \
  --set 'bifrost.plugins.custom[0].enabled=true' \
  --set 'bifrost.plugins.custom[0].path=/plugins/my-plugin.so' \
  --set 'bifrost.plugins.custom[0].placement=pre_builtin' \
  --set 'bifrost.plugins.custom[0].order=2'

# Verify placement appears in rendered output
test_name="custom plugin rendered JSON contains placement field"
if helm template bifrost ./helm-charts/bifrost \
  --set image.tag=v1.0.0 \
  --set 'bifrost.plugins.custom[0].name=my-plugin' \
  --set 'bifrost.plugins.custom[0].enabled=true' \
  --set 'bifrost.plugins.custom[0].path=/plugins/my-plugin.so' \
  --set 'bifrost.plugins.custom[0].placement=pre_builtin' \
  --set 'bifrost.plugins.custom[0].order=2' \
  > /tmp/helm-template-output.yaml 2>&1; then
  if grep -Eq '"placement"[[:space:]]*:[[:space:]]*"pre_builtin"' /tmp/helm-template-output.yaml; then
    report_result "$test_name" 0
  else
    report_result "$test_name" 1
    echo -e "${YELLOW}  placement field not found in rendered output${NC}"
  fi
else
  report_result "$test_name" 1
  echo -e "${YELLOW}  Error output:${NC}"
  head -10 /tmp/helm-template-output.yaml | sed 's/^/    /'
fi

# Verify order appears in rendered output
test_name="custom plugin rendered JSON contains order field"
if helm template bifrost ./helm-charts/bifrost \
  --set image.tag=v1.0.0 \
  --set 'bifrost.plugins.custom[0].name=my-plugin' \
  --set 'bifrost.plugins.custom[0].enabled=true' \
  --set 'bifrost.plugins.custom[0].path=/plugins/my-plugin.so' \
  --set 'bifrost.plugins.custom[0].placement=pre_builtin' \
  --set 'bifrost.plugins.custom[0].order=2' \
  > /tmp/helm-template-output.yaml 2>&1; then
  if grep -Eq '"order"[[:space:]]*:[[:space:]]*2' /tmp/helm-template-output.yaml; then
    report_result "$test_name" 0
  else
    report_result "$test_name" 1
    echo -e "${YELLOW}  order field not found in rendered output${NC}"
  fi
else
  report_result "$test_name" 1
  echo -e "${YELLOW}  Error output:${NC}"
  head -10 /tmp/helm-template-output.yaml | sed 's/^/    /'
fi

# 7. Security Context Rendering
echo ""
echo -e "${CYAN}🔒 7/9 - Validating OpenShift-compatible Security Contexts...${NC}"
echo "----------------------------------------------------------------"

# Images before v1.6.4 use a non-numeric `USER appuser`, so kubelet can only
# verify runAsNonRoot when the chart pins runAsUser. The default render must
# carry runAsUser: 1000; OpenShift users unset it with explicit nulls (tested
# below).
test_name="default Bifrost pod sets runAsUser: 1000 (kubelet runAsNonRoot verification)"
if helm template bifrost ./helm-charts/bifrost \
  --set image.tag=v1.0.0 \
  -s templates/stateful.yaml \
  > /tmp/helm-template-output.yaml 2>&1; then
  if grep -Eq '^[[:space:]]*runAsUser:[[:space:]]*1000$' /tmp/helm-template-output.yaml; then
    report_result "$test_name" 0
  else
    report_result "$test_name" 1
    echo -e "${YELLOW}  runAsUser: 1000 missing from default render (pre-v1.6.4 images have non-numeric USER, so runAsNonRoot fails without it)${NC}"
  fi
else
  report_result "$test_name" 1
  echo -e "${YELLOW}  Error output:${NC}"
  head -10 /tmp/helm-template-output.yaml | sed 's/^/    /'
fi

# Postgres mode renders a Deployment (not the sqlite StatefulSet); assert the
# UID pin holds on that code path too.
test_name="postgres-mode Bifrost pod sets runAsUser: 1000 (kubelet runAsNonRoot verification)"
if helm template bifrost ./helm-charts/bifrost \
  --set image.tag=v1.0.0 \
  --set storage.mode=postgres \
  --set postgresql.enabled=true \
  --set postgresql.auth.password=testpass \
  -s templates/deployment.yaml \
  > /tmp/helm-template-output.yaml 2>&1; then
  if grep -Eq '^[[:space:]]*runAsUser:[[:space:]]*1000$' /tmp/helm-template-output.yaml; then
    report_result "$test_name" 0
  else
    report_result "$test_name" 1
    echo -e "${YELLOW}  runAsUser: 1000 missing from postgres render (pre-v1.6.4 images have non-numeric USER, so runAsNonRoot fails without it)${NC}"
  fi
else
  report_result "$test_name" 1
  echo -e "${YELLOW}  Error output:${NC}"
  head -10 /tmp/helm-template-output.yaml | sed 's/^/    /'
fi

# OpenShift path: explicit nulls must unset the UID pins so the SCC can
# assign an arbitrary UID.
test_name="OpenShift override (runAsUser/fsGroup null) removes UID pins"
if helm template bifrost ./helm-charts/bifrost \
  --set image.tag=v1.0.0 \
  --set podSecurityContext.runAsUser=null \
  --set podSecurityContext.fsGroup=null \
  --set securityContext.runAsUser=null \
  > /tmp/helm-template-output.yaml 2>&1; then
  if grep -Eq '^[[:space:]]*(runAsUser|fsGroup):' /tmp/helm-template-output.yaml; then
    report_result "$test_name" 1
    echo -e "${YELLOW}  runAsUser/fsGroup still rendered with null overrides (OpenShift SCC cannot assign a UID)${NC}"
  else
    report_result "$test_name" 0
  fi
else
  report_result "$test_name" 1
  echo -e "${YELLOW}  Error output:${NC}"
  head -10 /tmp/helm-template-output.yaml | sed 's/^/    /'
fi

# 8. Pod Scheduling: topologySpreadConstraints
echo ""
echo -e "${CYAN}🗺️  8/9 - Validating topologySpreadConstraints Rendering...${NC}"
echo "----------------------------------------------------------------"

# Values files for the sections below live in a private directory (not fixed /tmp paths,
# which another user on a shared host could pre-create as symlinks).
VALUES_DIR=$(mktemp -d)
trap 'rm -rf "$VALUES_DIR"' EXIT

# check_workload renders the chart and runs a python assertion against the Bifrost
# server workload (Deployment or StatefulSet). The assertion sees `w` (the workload
# document); it prints nothing on success and a reason on failure.
check_workload() {
  local test_name=$1
  local assertion=$2
  shift 2
  if ! helm template bifrost ./helm-charts/bifrost --set image.tag=v1.0.0 "$@" \
    > /tmp/helm-template-output.yaml 2>&1; then
    report_result "$test_name" 1
    echo -e "${YELLOW}  Error output:${NC}"
    head -10 /tmp/helm-template-output.yaml | sed 's/^/    /'
    return
  fi
  local reason
  reason=$(ASSERTION="$assertion" python3 -c '
import os, yaml
docs = [d for d in yaml.safe_load_all(open("/tmp/helm-template-output.yaml")) if d]
ws = [d for d in docs if d.get("kind") in ("Deployment", "StatefulSet") and d["metadata"]["name"] == "bifrost"]
if len(ws) != 1:
    print("expected exactly one bifrost Deployment/StatefulSet, got %d" % len(ws))
else:
    w = ws[0]
    try:
        exec(os.environ["ASSERTION"])
    except AssertionError as e:
        print(e or "assertion failed")
    except Exception as e:
        print("%s: %s" % (type(e).__name__, e))
' 2>&1 || true)
  if [ -z "$reason" ]; then
    report_result "$test_name" 0
  else
    report_result "$test_name" 1
    echo -e "${YELLOW}  $reason${NC}"
  fi
}

# expect_render_failure asserts the chart refuses the values (schema or template guard).
expect_render_failure() {
  local test_name=$1
  local expected=$2
  shift 2
  if helm template bifrost ./helm-charts/bifrost --set image.tag=v1.0.0 "$@" \
    > /tmp/helm-template-output.yaml 2>&1; then
    report_result "$test_name" 1
    echo -e "${YELLOW}  render succeeded; expected it to fail with: $expected${NC}"
  elif grep -q -- "$expected" /tmp/helm-template-output.yaml; then
    report_result "$test_name" 0
  else
    report_result "$test_name" 1
    echo -e "${YELLOW}  render failed for another reason:${NC}"
    head -10 /tmp/helm-template-output.yaml | sed 's/^/    /'
  fi
}

cat > "$VALUES_DIR/tsc-values.yaml" << 'VALS'
topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: topology.kubernetes.io/zone
    whenUnsatisfiable: ScheduleAnyway
  - maxSkew: 2
    topologyKey: kubernetes.io/hostname
    whenUnsatisfiable: DoNotSchedule
    minDomains: 2
    matchLabelKeys: ["pod-template-hash"]
    nodeAffinityPolicy: Honor
    nodeTaintsPolicy: Ignore
    labelSelector:
      matchLabels:
        team: platform
VALS

check_workload "topologySpreadConstraints: omitted by default" \
  'assert "topologySpreadConstraints" not in w["spec"]["template"]["spec"], "rendered without being configured"'

check_workload "topologySpreadConstraints: Deployment fills labelSelector with the pod selector" \
  'tsc = w["spec"]["template"]["spec"]["topologySpreadConstraints"]
assert w["kind"] == "Deployment", "expected a Deployment in postgres mode, got %s" % w["kind"]
assert tsc[0]["labelSelector"] == {"matchLabels": w["spec"]["selector"]["matchLabels"]}, "default labelSelector %r does not match the workload selector %r" % (tsc[0]["labelSelector"], w["spec"]["selector"]["matchLabels"])
assert (tsc[0]["maxSkew"], tsc[0]["topologyKey"], tsc[0]["whenUnsatisfiable"]) == (1, "topology.kubernetes.io/zone", "ScheduleAnyway"), "constraint fields changed: %r" % tsc[0]' \
  -f "$VALUES_DIR/tsc-values.yaml" \
  --set storage.mode=postgres \
  --set postgresql.enabled=true \
  --set postgresql.auth.password=testpass

check_workload "topologySpreadConstraints: StatefulSet fills labelSelector with the pod selector" \
  'tsc = w["spec"]["template"]["spec"]["topologySpreadConstraints"]
assert w["kind"] == "StatefulSet", "expected a StatefulSet in sqlite+persistence mode, got %s" % w["kind"]
assert tsc[0]["labelSelector"] == {"matchLabels": w["spec"]["selector"]["matchLabels"]}, "default labelSelector %r does not match the workload selector %r" % (tsc[0]["labelSelector"], w["spec"]["selector"]["matchLabels"])' \
  -f "$VALUES_DIR/tsc-values.yaml"

check_workload "topologySpreadConstraints: explicit labelSelector and optional fields pass through" \
  'tsc = w["spec"]["template"]["spec"]["topologySpreadConstraints"]
assert len(tsc) == 2, "expected 2 constraints, got %d" % len(tsc)
assert tsc[1] == {"maxSkew": 2, "topologyKey": "kubernetes.io/hostname", "whenUnsatisfiable": "DoNotSchedule", "minDomains": 2, "matchLabelKeys": ["pod-template-hash"], "nodeAffinityPolicy": "Honor", "nodeTaintsPolicy": "Ignore", "labelSelector": {"matchLabels": {"team": "platform"}}}, "second constraint was altered: %r" % tsc[1]' \
  -f "$VALUES_DIR/tsc-values.yaml"

expect_render_failure "topologySpreadConstraints: schema rejects an unknown whenUnsatisfiable" \
  "whenUnsatisfiable" \
  --set 'topologySpreadConstraints[0].maxSkew=1' \
  --set 'topologySpreadConstraints[0].topologyKey=kubernetes.io/hostname' \
  --set 'topologySpreadConstraints[0].whenUnsatisfiable=Sometimes'

expect_render_failure "topologySpreadConstraints: schema requires topologyKey" \
  "topologyKey" \
  --set 'topologySpreadConstraints[0].maxSkew=1' \
  --set 'topologySpreadConstraints[0].whenUnsatisfiable=DoNotSchedule'

# Kubernetes rejects minDomains unless whenUnsatisfiable is DoNotSchedule.
expect_render_failure "topologySpreadConstraints: schema rejects minDomains with ScheduleAnyway" \
  "DoNotSchedule" \
  --set 'topologySpreadConstraints[0].maxSkew=1' \
  --set 'topologySpreadConstraints[0].topologyKey=topology.kubernetes.io/zone' \
  --set 'topologySpreadConstraints[0].whenUnsatisfiable=ScheduleAnyway' \
  --set 'topologySpreadConstraints[0].minDomains=2'

# An explicit empty selector matches every pod in the namespace; only an absent one is defaulted.
cat > "$VALUES_DIR/tsc-empty-selector.yaml" << 'VALS'
topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: kubernetes.io/hostname
    whenUnsatisfiable: ScheduleAnyway
    labelSelector: {}
VALS
check_workload "topologySpreadConstraints: explicit empty labelSelector is kept" \
  'tsc = w["spec"]["template"]["spec"]["topologySpreadConstraints"]
assert tsc[0]["labelSelector"] == {}, "explicit {} selector was replaced with %r" % tsc[0]["labelSelector"]' \
  -f "$VALUES_DIR/tsc-empty-selector.yaml"
rm -f "$VALUES_DIR/tsc-empty-selector.yaml"

rm -f "$VALUES_DIR/tsc-values.yaml"

# 9. Sidecars: native (initContainers entry with restartPolicy: Always) and regular (extraContainers)
echo ""
echo -e "${CYAN}🛵 9/9 - Validating Sidecar Container Rendering...${NC}"
echo "----------------------------------------------------------------"

# Native sidecars need Kubernetes 1.29+, and Helm's default .Capabilities.KubeVersion varies by
# Helm release (v4.0.0, used in CI, reports v1.20.0), so every render of these values pins it.
cat > "$VALUES_DIR/sidecar-values.yaml" << 'VALS'
initContainers:
  - name: native-proxy
    image: busybox:1.36
    restartPolicy: Always
    command: ["sh", "-c", "sleep infinity"]
  - name: setup
    image: busybox:1.36
    command: ["true"]
extraContainers:
  - name: classic-shipper
    image: busybox:1.36
    command: ["sh", "-c", "sleep infinity"]
VALS

check_workload "sidecars: no extra or init containers by default" \
  's = w["spec"]["template"]["spec"]
assert "initContainers" not in s, "initContainers rendered without being configured: %r" % s.get("initContainers")
assert [c["name"] for c in s["containers"]] == ["bifrost"], "unexpected containers: %r" % [c["name"] for c in s["containers"]]'

check_workload "sidecars: Deployment keeps the native sidecar init container as given and appends extraContainers" \
  's = w["spec"]["template"]["spec"]
assert w["kind"] == "Deployment", "expected a Deployment in postgres mode, got %s" % w["kind"]
assert [(c["name"], c.get("restartPolicy")) for c in s["initContainers"]] == [("native-proxy", "Always"), ("setup", None)], "init containers: %r" % [(c["name"], c.get("restartPolicy")) for c in s["initContainers"]]
assert [c["name"] for c in s["containers"]] == ["bifrost", "classic-shipper"], "containers: %r" % [c["name"] for c in s["containers"]]
assert s["containers"][1]["command"] == ["sh", "-c", "sleep infinity"], "extraContainers entry was altered: %r" % s["containers"][1]' \
  --kube-version 1.29.0 \
  -f "$VALUES_DIR/sidecar-values.yaml" \
  --set storage.mode=postgres \
  --set postgresql.enabled=true \
  --set postgresql.auth.password=testpass

check_workload "sidecars: StatefulSet keeps the native sidecar init container as given and appends extraContainers" \
  's = w["spec"]["template"]["spec"]
assert w["kind"] == "StatefulSet", "expected a StatefulSet in sqlite+persistence mode, got %s" % w["kind"]
assert [c["name"] for c in s["containers"]] == ["bifrost", "classic-shipper"], "containers: %r" % [c["name"] for c in s["containers"]]
assert s["containers"][1]["command"] == ["sh", "-c", "sleep infinity"], "extraContainers entry was altered: %r" % s["containers"][1]
assert [(c["name"], c.get("restartPolicy")) for c in s["initContainers"]] == [("native-proxy", "Always"), ("setup", None)], "init containers: %r" % [(c["name"], c.get("restartPolicy")) for c in s["initContainers"]]' \
  --kube-version 1.29.0 \
  -f "$VALUES_DIR/sidecar-values.yaml"

check_workload "sidecars: native sidecar renders on Kubernetes 1.29" \
  's = w["spec"]["template"]["spec"]
assert [(c["name"], c.get("restartPolicy")) for c in s["initContainers"]] == [("native-proxy", "Always"), ("setup", None)], "init containers: %r" % [(c["name"], c.get("restartPolicy")) for c in s["initContainers"]]' \
  --kube-version 1.29.0 \
  -f "$VALUES_DIR/sidecar-values.yaml"

check_workload "sidecars: regular initContainers still render on Kubernetes 1.28" \
  's = w["spec"]["template"]["spec"]
assert [c["name"] for c in s["initContainers"]] == ["setup"], "init containers: %r" % [c["name"] for c in s["initContainers"]]' \
  --kube-version 1.28.0 \
  --set 'initContainers[0].name=setup' \
  --set 'initContainers[0].image=busybox:1.36'

expect_render_failure "sidecars: native sidecar refused on Kubernetes 1.28" \
  "needs Kubernetes 1.29 or newer" \
  --kube-version 1.28.0 \
  -f "$VALUES_DIR/sidecar-values.yaml"

expect_render_failure "sidecars: schema requires an image on extraContainers" \
  "image" \
  --set 'extraContainers[0].name=no-image'

rm -f "$VALUES_DIR/sidecar-values.yaml"

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
