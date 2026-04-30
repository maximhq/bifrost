# Bifrost Helm Charts

[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/bifrost)](https://artifacthub.io/packages/helm/bifrost/bifrost)

Official Helm charts for deploying [Bifrost](https://github.com/maximhq/bifrost) - a high-performance AI gateway with unified interface for multiple providers.

**Latest Version:** 2.1.10

## Changelog

### 2.1.10

- Added `bifrost.cluster.grpc` block for the cluster gRPC counter-sync transport (enterprise):
  - New values: `bifrost.cluster.grpc.port` (default `10102`) and `bifrost.cluster.grpc.dialTimeoutSeconds` (default `5`).
  - Rendered into `cluster_config.grpc` (`port`, `dial_timeout_seconds`) by `templates/_helpers.tpl`.
  - StatefulSet exposes the port as a named `grpc` container port; `service-headless` exposes it as a named service port so peers can dial each other.
  - Both port additions are guarded by `if .Values.bifrost.cluster.grpc` so values overrides that omit the block render cleanly.

### 2.1.9

- Added Kubernetes pod-discovery RBAC templates for cluster discovery:
  - Added `templates/rbac.yaml` to render a namespaced `Role`/`RoleBinding` for pod `get/list/watch`.
  - Added `rbac.podDiscovery.enabled` to `values.yaml` and `values.schema.json` for controlled enablement (defaults to `true`).
  - RBAC resources render only when `rbac.podDiscovery.enabled`, `bifrost.cluster.enabled`, and `bifrost.cluster.discovery.enabled` are true, with discovery `type: kubernetes`.

### 2.1.8

- Added provider key backward compatibility in Helm rendering:
  - If `bifrost.providers.<provider>.keys[].id` is omitted and `name` is present, Helm now auto-populates `id = name`.
  - This preserves legacy values files that only defined key names while still supporting `governance.virtualKeys[].provider_configs[].key_ids`.

### 2.1.7

- Added semantic cache Helm layers and examples:
  - Added Redis deployment template for semantic cache.
  - Extended Helm values/schema coverage for semantic cache and client-config examples.
- Added enterprise/governance Helm support:
  - Added governance `business_units` support in Helm schema/template rendering.
  - Added deferred virtual-key/provider-config budget ordering handling in Helm rendering.
- Added MCP tool-groups support in Helm:
  - Added `mcp.tool_groups` config support with governance bindings.
  - Added camelCase alias compatibility for related Helm config fields.

### 2.1.6

- Includes unreleased `2.1.5` changes 
- Built-in plugin versioning for DB-backed deployments:
  - Added `version` field support for built-in plugins.
  - Added default `version: 1` for built-in plugins in `values.yaml` (`telemetry`, `logging`, `governance`, `maxim`, `semanticCache`, `otel`, `datadog`).
  - Updated `_helpers.tpl` to include plugin `version` in rendered config when set (cast as integer).
- Updated StatefulSet PVC template labels to be immutable-safe:
  - `spec.volumeClaimTemplates.metadata.labels` now uses stable selector labels (without chart/app version labels).
- Governance schema and validation updates:
  - Added `governance.budgets[].virtual_key_id` support.
  - Removed stale `budget_id` references from virtual keys and provider configs in templates/tests.
  - `validate-helm-config-fields.sh` assertions were updated accordingly.
- Query/schema compatibility updates:
  - Tightened `query` validation in `values.schema.json` and `config.schema.json` to valid RuleGroupType shape (`null` or `{ combinator, rules }`).
- Config/input alias support updates:
  - Added support for `env.*` references in proxy/TLS fields (`ca_cert_pem`, `url`, `username`, `password`).
  - Added `provider_key_name` alias for routing targets and pricing overrides (resolved to `key_id` at config load time).
- MCP config improvements:
  - Added Go duration string support for `mcp.toolSyncInterval` (legacy numeric nanoseconds still supported).
  - Added hash-based MCP client config reconciliation for DB-backed config store updates.
- Upgrade impact:
  - Existing SQLite StatefulSets created from older chart templates may require a one-time StatefulSet recreation during upgrade because `spec.volumeClaimTemplates` is immutable in Kubernetes.
- Migration notes (only if upgrade fails with StatefulSet immutable-field error):
  1. Identify StatefulSet name and namespace for your Helm release.
  2. Delete only the StatefulSet while preserving dependents:
     - `kubectl delete statefulset <statefulset-name> -n <namespace> --cascade=orphan`
  3. Run Helm upgrade:
     - `helm upgrade <release-name> bifrost/bifrost -n <namespace> -f <values-file> --set image.tag=<tag>`
  4. If needed, re-apply/recreate the StatefulSet from the upgraded chart manifests.
  5. Verify PVCs are preserved and pods become healthy:
     - `kubectl get pvc -n <namespace>`
     - `kubectl get pods -n <namespace>`

### 2.1.5 (not released separately)

- Merged into `2.1.6` release notes above.

### 2.1.4

- Added stricter cluster discovery validation in Helm templates:
  - Require `bifrost.cluster.discovery.serviceName` when `bifrost.cluster.discovery.type` is `consul`, `etcd`, or `udp`.
  - For `udp` discovery, require both:
    - `bifrost.cluster.discovery.udpBroadcastPort`
    - `bifrost.cluster.discovery.allowedAddressSpace`
- Added/updated template fail-fast errors so invalid discovery config is rejected at render time instead of failing later at runtime.

### 2.1.3

- For `bifrost.cluster.discovery.type` set to `consul`, `etcd`, or `udp`, set `bifrost.cluster.discovery.serviceName` explicitly during upgrade.

### v2.1.2

- Removed `encryption_key` requirement — field is now optional; Bifrost will operate without encryption when omitted

### v2.1.1

- Made `bifrost.governance.virtualKeys[].value` optional — template no longer fails when the field is omitted, allowing the backend to auto-generate the virtual key value
- When `value` is absent, the rendered `config.json` omits the field entirely (consistent with other optional VK fields)

### v2.1.0-prerelease2 (prerelease)

- Synced helm `values.schema.json` with transport `config.schema.json` — fixed virtual key and budget drift:
  - Removed `required: [mcp_client_id]` constraint on `virtualKeys[].mcp_configs[]` items — canonical schema accepts either `mcp_client_id` (DB form) or `mcp_client_name` (config-file form, resolved to ID at startup)
  - Added `mcp_client_name` as an allowed property on `virtualKeys[].mcp_configs[]` items
  - Added `calendar_aligned` (boolean) on `virtualKeys[]` — field now lives on the virtual key, applies uniformly to all budgets under it
  - Removed stale `budget_id` from `virtualKeys[]` — `TableVirtualKey` has no `BudgetID`; budgets link via foreign key from the budget table
  - Removed stale `calendar_aligned` from `budgets[]` — moved to virtual key level

### v2.0.17

- Added object storage support (S3/GCS) for offloading log payloads from the database
- Added `storage.logsStore.objectStorage` configuration with S3 and GCS backend support
- Added object storage credential injection from Kubernetes secrets (`existingSecret`)
- Added `object_storage` schema to `config.schema.json` under `logs_store`
- Updated deployment and stateful templates with object storage secret env vars

### v2.0.16

- Fixed disabled custom plugins being completely removed from rendered config.json instead of being kept with `enabled: false`

### v2.0.15

- Synced helm schema with transport `config.schema.json` — added missing properties:
  - `client.mcpDisableAutoToolInject` — disable automatic MCP tool injection
  - `governance.budgets[].calendar_aligned` — snap budget resets to calendar boundaries
  - `governance.pricingOverrides` — scoped pricing overrides for the model catalog
  - `mcp.clientConfigs[].allowedExtraHeaders` — header allowlist per MCP client
  - `mcp.clientConfigs[].allowOnAllVirtualKeys` — make MCP server accessible to all virtual keys
  - `mcp.toolManagerConfig.disableAutoToolInject` — disable auto tool injection at manager level
  - `networkConfig.beta_header_overrides` — override Anthropic beta header support per provider
  - `websocket` — full WebSocket gateway tuning (connections, pool, transcript buffer)
- Fixed SSE `connectionString` not being rendered in `_helpers.tpl` for MCP clients
- Added template rendering for all new properties in `_helpers.tpl`

### v2.0.14

- Added `placement` and `order` fields to custom plugin schema and template rendering
- Added plugin property completeness check to `validate-helm-schema.sh`
- Added custom plugin placement/order rendering tests to `validate-helm-templates.sh`
- Added `PluginConfig` struct validation to `validate-go-config-fields.sh`

### v2.0.13

- Added missing client config properties: `asyncJobResultTTL`, `requiredHeaders`, `loggingHeaders`, `allowedHeaders`, `mcpAgentDepth`, `mcpToolExecutionTimeout`, `mcpCodeModeBindingLevel`, `mcpToolSyncInterval`, `hideDeletedVirtualKeysInFilters`
- Added MCP new fields: top-level `toolSyncInterval`, per-client `clientId`, `isCodeModeClient`, `toolSyncInterval`, `isPingAvailable`, `toolPricing`, and `codeModeBindingLevel` in tool manager config
- Added governance `modelConfigs` and `providers` top-level properties
- Added cluster `region` property
- Added guardrail provider `timeout` field (was missing from schema and template rendering)
- Fixed `isPingAvailable` rendering bug in `_helpers.tpl` (was using wrong key name)
- Added `is_ping_available` and `tool_pricing` to `config.schema.json` MCP client config
- Added new CI script `validate-go-config-fields.sh` for Go struct-to-schema drift detection
- Expanded all 3 existing CI validation scripts with Gap 1-8 property coverage

### v2.0.12

- Fixed health probe paths to use `/health` instead of `/metrics`

### v2.0.11

- Bumped appVersion to 1.4.11

### v2.0.10

- Added missing plugin config properties from Go implementations:
  - governance: `required_headers`, `is_enterprise`
  - logging: `disable_content_logging`, `logging_headers`
  - otel: `headers`, `tls_ca_cert`, `insecure`
  - telemetry: `custom_labels`

### v2.0.9

- Bumped appVersion to 1.4.8

### v2.0.8

- Added comprehensive config field coverage for all `config.schema.json` fields
- Added Pinecone vector store support (external only) with secret injection
- Added governance routing rules template support
- Added OTEL metrics fields (metrics_enabled, metrics_endpoint, metrics_push_interval)
- Added advanced Redis connection pool fields (pool_size, timeouts, idle conns, etc.)
- Added Weaviate timeout and className fields
- Expanded values.yaml with commented examples for all provider types (Azure, Vertex, Bedrock), network config, concurrency, proxy config, and governance entities
- Added helm config field validation CI test (246 assertions covering all config.schema.json fields)

### v2.0.7

- Previous release

### v2.0.6

- Fixes MCP client config template to convert camelCase Helm values to snake_case config format

### v2.0.5

- Fixes config field validation parity

### v2.0.2

- Added Qdrant vector store support with deployment, service, and PVC templates
- Added headless service template for StatefulSet DNS resolution
- Fixed gitignore pattern that was excluding template files from version control

### v2.0.1

- Added missing StatefulSet template for SQLite with persistence mode
- Added headless service for StatefulSet DNS resolution
- v2.0.0 documented StatefulSet support but the template was not included - this release fixes that

### v2.0.0 (Breaking Change)

#### StatefulSet for SQLite with Persistence

This release fixes the multi-attach volume error when running multiple replicas with SQLite storage mode.

#### What Changed

- When using `storage.mode: sqlite` with `storage.persistence.enabled: true`, Bifrost now deploys as a **StatefulSet** instead of a Deployment
- Each pod gets its own dedicated PersistentVolumeClaim (e.g., `data-bifrost-0`, `data-bifrost-1`, `data-bifrost-2`)
- A headless service is created for StatefulSet DNS resolution
- HorizontalPodAutoscaler now correctly references StatefulSet or Deployment based on storage configuration

#### Who Is Affected

- Users running SQLite mode with persistence enabled and multiple replicas
- Users upgrading existing SQLite deployments need to migrate (see below)

#### Who Is NOT Affected

- Users running PostgreSQL mode (`storage.mode: postgres`) - no changes, still uses Deployment
- Users running SQLite without persistence (`storage.persistence.enabled: false`)
- Users running SQLite with an existing PVC claim (`storage.persistence.existingClaim`)

#### Migration Guide for Existing SQLite Deployments

Since Kubernetes doesn't allow in-place conversion from Deployment to StatefulSet, you need to:

1. Back up your data (if needed)
2. Uninstall the existing release: `helm uninstall bifrost`
3. Delete the old PVC: `kubectl delete pvc bifrost-data`
4. Install with the new chart version: `helm install bifrost bifrost/bifrost --set image.tag=<latest-image>`

**Note:** For production high-availability setups, we recommend using PostgreSQL mode which scales horizontally without these concerns.

### v1.7.0

- Previous stable release with Deployment-based architecture for all storage modes

## Quick Start

```bash
# Add the Bifrost Helm repository
helm repo add bifrost https://maximhq.github.io/bifrost/helm-charts

# Update your local Helm chart repository cache
helm repo update

# Install Bifrost with default configuration (SQLite storage)
helm install bifrost bifrost/bifrost --set image.tag=v1.4.3
```

## Prerequisites

- Kubernetes 1.23+
- Helm 3.2.0+
- PV provisioner support in the underlying infrastructure (for persistent storage)

## Installation

### From Helm Repository (Recommended)

```bash
# Add repository
helm repo add bifrost https://maximhq.github.io/bifrost/helm-charts
helm repo update

# Install with default values
helm install bifrost bifrost/bifrost --set image.tag=v1.4.3

# Or install with custom values
helm install bifrost bifrost/bifrost -f my-values.yaml
```

### From Source

```bash
# Clone the repository
git clone https://github.com/maximhq/bifrost.git
cd bifrost/helm-charts

# Install from local chart
helm install bifrost ./bifrost --set image.tag=v1.5.2
```

### Interactive Installation

Use the included installation script for guided setup:

```bash
cd bifrost/helm-charts/bifrost
./scripts/install.sh
```

## Configuration

### Image Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `image.repository` | Container image repository | `docker.io/maximhq/bifrost` |
| `image.tag` | Container image tag (required) | `""` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |

> **Important:** You must specify the `image.tag`. See available tags at [Docker Hub](https://hub.docker.com/r/maximhq/bifrost/tags).

### Enterprise Private Registry

For enterprise customers with private container registries, simply override the `image.repository` with your full registry URL:

```yaml
# Google Artifact Registry
image:
  repository: us-west1-docker.pkg.dev/bifrost-enterprise/your-org/bifrost
  tag: v1.5.0

# AWS ECR
image:
  repository: 123456789.dkr.ecr.us-east-1.amazonaws.com/bifrost
  tag: v1.5.0

# Azure Container Registry
image:
  repository: yourregistry.azurecr.io/bifrost
  tag: v1.5.0

# Self-hosted registry
image:
  repository: registry.yourcompany.com/ai/bifrost
  tag: v1.5.0
```

If your private registry requires authentication, configure `imagePullSecrets`:

```yaml
image:
  repository: us-west1-docker.pkg.dev/bifrost-enterprise/your-org/bifrost
  tag: v1.5.0

imagePullSecrets:
  - name: my-registry-secret
```

Create the secret beforehand:
```bash
kubectl create secret docker-registry my-registry-secret \
  --docker-server=us-west1-docker.pkg.dev \
  --docker-username=_json_key \
  --docker-password="$(cat key.json)" \
  --docker-email=your-email@example.com
```

### Storage Configuration

Bifrost supports two storage backends (SQLite and PostgreSQL) that can be configured independently for config and logs stores.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `storage.mode` | Default storage backend (fallback when per-store type not set) | `sqlite` |
| `storage.persistence.enabled` | Enable persistent storage for SQLite | `true` |
| `storage.persistence.size` | Storage size | `10Gi` |
| `storage.configStore.enabled` | Enable configuration store | `true` |
| `storage.configStore.type` | Config store backend: `sqlite`, `postgres`, or `""` | `""` (uses `storage.mode`) |
| `storage.logsStore.enabled` | Enable logs store | `true` |
| `storage.logsStore.type` | Logs store backend: `sqlite`, `postgres`, or `""` | `""` (uses `storage.mode`) |

#### Mixed Backend Example

You can use different backends for config and logs stores:

```yaml
storage:
  mode: sqlite  # Default fallback
  configStore:
    enabled: true
    type: sqlite   # Config in SQLite (fast, local)
  logsStore:
    enabled: true
    type: postgres # Logs in PostgreSQL (scalable, queryable)

postgresql:
  enabled: true
  # ... PostgreSQL configuration for logs store
```

### PostgreSQL Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `postgresql.enabled` | Deploy PostgreSQL | `false` |
| `postgresql.auth.username` | Database username | `bifrost` |
| `postgresql.auth.password` | Database password | `bifrost_password` |
| `postgresql.auth.database` | Database name | `bifrost` |
| `postgresql.external.enabled` | Use external PostgreSQL | `false` |
| `postgresql.external.host` | External PostgreSQL host | `""` |

### Vector Store Configuration (Semantic Caching)

Bifrost supports multiple vector stores for semantic caching:

| Parameter | Description | Default |
|-----------|-------------|---------|
| `vectorStore.enabled` | Enable vector store | `false` |
| `vectorStore.type` | Vector store type: `none`, `weaviate`, `redis`, `qdrant` | `none` |

#### Weaviate

```yaml
vectorStore:
  enabled: true
  type: weaviate
  weaviate:
    enabled: true  # Deploy Weaviate
    # Or use external:
    # external:
    #   enabled: true
    #   host: "weaviate.example.com"
```

#### Redis

```yaml
vectorStore:
  enabled: true
  type: redis
  redis:
    enabled: true  # Deploy Redis
    # Or use external:
    # external:
    #   enabled: true
    #   host: "redis.example.com"
```

#### Qdrant

```yaml
vectorStore:
  enabled: true
  type: qdrant
  qdrant:
    enabled: true  # Deploy Qdrant
    # Or use external:
    # external:
    #   enabled: true
    #   host: "qdrant.example.com"
```

### Bifrost Application Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `bifrost.port` | Application port | `8080` |
| `bifrost.host` | Bind address | `0.0.0.0` |
| `bifrost.logLevel` | Log level | `info` |
| `bifrost.logStyle` | Log format: `json` or `text` | `json` |
| `bifrost.encryptionKey` | Encryption key for sensitive data | `""` |

### Provider Configuration

Configure AI provider API keys:

> **Note:** `keys[].weight` is optional in Helm values. If omitted, the chart renders it as `1`.

```yaml
bifrost:
  providers:
    openai:
      keys:
        - value: "sk-..."
          weight: 1
    anthropic:
      keys:
        - value: "sk-ant-..."
          weight: 1
```

### Plugins Configuration

| Plugin | Parameter | Description |
|--------|-----------|-------------|
| Telemetry | `bifrost.plugins.telemetry.enabled` | Enable metrics collection |
| Logging | `bifrost.plugins.logging.enabled` | Enable request logging |
| Governance | `bifrost.plugins.governance.enabled` | Enable budget management |
| Semantic Cache | `bifrost.plugins.semanticCache.enabled` | Enable semantic caching |
| OTEL | `bifrost.plugins.otel.enabled` | Enable OpenTelemetry integration |
| Maxim | `bifrost.plugins.maxim.enabled` | Enable Maxim observability |
| Datadog | `bifrost.plugins.datadog.enabled` | Enable Datadog APM integration |
| Custom | `bifrost.plugins.custom` | Array of custom/dynamic plugins |

#### Custom Plugins

You can add custom/dynamic plugins using the `bifrost.plugins.custom` array:

```yaml
bifrost:
  plugins:
    custom:
      - name: "my-custom-plugin"
        enabled: true
        path: "/plugins/my-plugin.so"
        version: 1
        placement: "pre_builtin"  # or "post_builtin" (default)
        order: 0                  # execution order within placement group
        config:
          key: value
```

### Client Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `bifrost.client.disableDbPingsInHealth` | Disable DB pings in health check | `false` |
| `bifrost.client.headerFilterConfig.allowlist` | Headers allowed to forward to LLM providers | `[]` |
| `bifrost.client.headerFilterConfig.denylist` | Headers blocked from forwarding | `[]` |

### MCP Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `bifrost.mcp.enabled` | Enable MCP (Model Context Protocol) | `false` |
| `bifrost.mcp.clientConfigs` | Array of MCP client configurations | `[]` |
| `bifrost.mcp.toolManagerConfig.toolExecutionTimeout` | Tool execution timeout in seconds | `30` |
| `bifrost.mcp.toolManagerConfig.maxAgentDepth` | Maximum agent depth | `10` |
| `bifrost.mcp.toolManagerConfig.codeModeBindingLevel` | Code mode binding level (`server` or `tool`) | `server` |
| `bifrost.mcp.toolManagerConfig.disableAutoToolInject` | Disable automatic MCP tool injection | `false` |
| `bifrost.mcp.toolSyncInterval` | Global MCP tool sync interval. Prefer a Go duration string (for example, `10m`); legacy numeric nanoseconds are still supported for backward compatibility, but string format is recommended. | `10m` |

#### MCP Migration Guide (`client.mcp*` -> `mcp.*`)

Prefer MCP settings under `bifrost.mcp` going forward. Older `bifrost.client.mcp*`
keys are retained for backward compatibility, but new configs should migrate to the
`mcp.toolManagerConfig` and `mcp.toolSyncInterval` fields.

| Old key | New key |
|---------|---------|
| `bifrost.client.mcpAgentDepth` | `bifrost.mcp.toolManagerConfig.maxAgentDepth` |
| `bifrost.client.mcpToolExecutionTimeout` | `bifrost.mcp.toolManagerConfig.toolExecutionTimeout` |
| `bifrost.client.mcpCodeModeBindingLevel` | `bifrost.mcp.toolManagerConfig.codeModeBindingLevel` |
| `bifrost.client.mcpDisableAutoToolInject` | `bifrost.mcp.toolManagerConfig.disableAutoToolInject` |
| `bifrost.client.mcpToolSyncInterval` | `bifrost.mcp.toolSyncInterval` |

### Ingress Configuration

```yaml
ingress:
  enabled: true
  className: "nginx"
  annotations:
    cert-manager.io/cluster-issuer: "letsencrypt-prod"
  hosts:
    - host: bifrost.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: bifrost-tls
      hosts:
        - bifrost.example.com
```

### Auto-scaling Configuration

```yaml
autoscaling:
  enabled: true
  minReplicas: 2
  maxReplicas: 10
  targetCPUUtilizationPercentage: 80
  targetMemoryUtilizationPercentage: 80
```

### Referencing Secrets in MCP Headers

`bifrost.mcp.clientConfigs[].headers` is a free-form `map<string, string>`
whose values can contain auth tokens. The chart does not wrap this map with
a bespoke `secretRef` — a per-header dict would explode the values surface.
Instead, use the standard pattern:

1. Write `env.MY_HEADER_VAR` as the header value in `values.yaml`:
   ```yaml
   bifrost:
     mcp:
       clientConfigs:
         - name: "my-mcp"
           connectionType: "http"
           headers:
             Authorization: "env.MY_MCP_AUTH"
   ```
2. Inject the env var into the pod via the chart's top-level `envFrom:` or
   `env:` pass-through — e.g., in `values.yaml`:
   ```yaml
   envFrom:
     - secretRef:
         name: my-mcp-auth-secret
   # OR:
   env:
     - name: MY_MCP_AUTH
       valueFrom:
         secretKeyRef:
           name: my-mcp-auth-secret
           key: authorization
   ```

For `bifrost.mcp.clientConfigs[].connectionString` itself, prefer the
chart-native `secretRef` (`name` + `connectionStringKey`) instead — the
chart will inject `BIFROST_MCP_<NAME>_CONNECTION_STRING` and rewrite the
config automatically.

## Example Configurations

The chart includes pre-configured examples in `values-examples/`:

| Configuration | Description |
|---------------|-------------|
| `sqlite-only.yaml` | Simple setup with SQLite (local development) |
| `postgres-only.yaml` | PostgreSQL for config and logs |
| `mixed-backend.yaml` | SQLite for config + PostgreSQL for logs (mixed backend) |
| `postgres-weaviate.yaml` | PostgreSQL + Weaviate for semantic caching |
| `postgres-redis.yaml` | PostgreSQL + Redis for semantic caching |
| `postgres-qdrant.yaml` | PostgreSQL + Qdrant for semantic caching |
| `sqlite-weaviate.yaml` | SQLite + Weaviate |
| `sqlite-redis.yaml` | SQLite + Redis |
| `sqlite-qdrant.yaml` | SQLite + Qdrant |
| `external-postgres.yaml` | Using external PostgreSQL |
| `production-ha.yaml` | Production high-availability setup |

### Using Example Configurations

```bash
# From Helm repository
helm install bifrost bifrost/bifrost \
  -f https://raw.githubusercontent.com/maximhq/bifrost/main/helm-charts/bifrost/values-examples/postgres-only.yaml \
  --set image.tag=v1.5.2

# From local source
helm install bifrost ./bifrost -f ./bifrost/values-examples/postgres-only.yaml
```

## Production Deployment

For production deployments, we recommend:

1. **Use PostgreSQL** for reliable data persistence
2. **Enable semantic caching** with Weaviate, Redis, or Qdrant
3. **Configure auto-scaling** for handling variable load
4. **Set up Ingress** with TLS termination
5. **Use external secrets** for sensitive data

### Example Production Setup

```yaml
# production-values.yaml
replicaCount: 3

autoscaling:
  enabled: true
  minReplicas: 3
  maxReplicas: 10

storage:
  mode: postgres

postgresql:
  enabled: true
  auth:
    password: "SECURE_PASSWORD_HERE"
  primary:
    persistence:
      size: 50Gi

vectorStore:
  enabled: true
  type: weaviate
  weaviate:
    enabled: true
    persistence:
      size: 50Gi

ingress:
  enabled: true
  className: "nginx"
  annotations:
    cert-manager.io/cluster-issuer: "letsencrypt-prod"
  hosts:
    - host: bifrost.yourdomain.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: bifrost-tls
      hosts:
        - bifrost.yourdomain.com

bifrost:
  client:
    initialPoolSize: 1000
    allowedOrigins:
      - "https://yourdomain.com"
  plugins:
    semanticCache:
      enabled: true
    telemetry:
      enabled: true
    logging:
      enabled: true
```

## Upgrading

```bash
# Update repository
helm repo update

# Upgrade release
helm upgrade bifrost bifrost/bifrost --set image.tag=v1.5.2

# Or with custom values
helm upgrade bifrost bifrost/bifrost -f my-values.yaml
```

## Uninstalling

```bash
# Uninstall release
helm uninstall bifrost

# If you want to delete persistent volumes
kubectl delete pvc -l app.kubernetes.io/name=bifrost
```

## Accessing Bifrost

After installation, access Bifrost using one of these methods:

### Port Forwarding (Development)

```bash
kubectl port-forward svc/bifrost 8080:8080
# Then visit http://localhost:8080
```

### LoadBalancer

```yaml
service:
  type: LoadBalancer
```

### Ingress

Configure the `ingress` section as shown above.

## Monitoring

Bifrost exposes Prometheus metrics at `/metrics`:

```bash
# Get metrics
curl http://localhost:8080/metrics
```

For OpenTelemetry integration:

```yaml
bifrost:
  plugins:
    otel:
      enabled: true
      config:
        service_name: "bifrost"
        collector_url: "http://otel-collector:4317"
        trace_type: "genai_extension"
        protocol: "grpc"
```

## Troubleshooting

### Check Pod Status

```bash
kubectl get pods -l app.kubernetes.io/name=bifrost
kubectl describe pod <pod-name>
```

### View Logs

```bash
kubectl logs -l app.kubernetes.io/name=bifrost -f
```

### Check Configuration

```bash
# View generated configmap
kubectl get configmap bifrost -o yaml

# View generated secrets
kubectl get secret bifrost -o yaml
```

### Common Issues

**Pod stuck in Pending state:**
- Check if PersistentVolume is available: `kubectl get pv`
- Check storage class: `kubectl get storageclass`

**Pod CrashLoopBackOff:**
- Check logs: `kubectl logs <pod-name>`
- Verify environment variables and secrets

**Cannot connect to PostgreSQL:**
- Ensure PostgreSQL pod is running
- Check connection string in configmap/secrets
- Verify network policies allow connectivity

## Resources

- [Bifrost Documentation](https://docs.getbifrost.ai)
- [GitHub Repository](https://github.com/maximhq/bifrost)
- [Docker Hub](https://hub.docker.com/r/maximhq/bifrost)
- [Discord Community](https://discord.gg/exN5KAydbU)

## License

This project is licensed under the Apache 2.0 License - see the [LICENSE](../LICENSE) file for details.

Built with ❤️ by [Maxim](https://github.com/maximhq)

