# Layered Helm Values Examples

These examples are split into composable value files so you can combine them with multiple `-f` flags.

## Files

- `values.yaml` - Base layer with shared image tag
- `values-storage-sqlite.yaml` - Storage layer for SQLite StatefulSet mode
- `values-storage-postgres.yaml` - Storage layer for Postgres mode (chart-managed Postgres)
- `values-providers.yaml` - Provider keys layer (`openai` + `anthropic`)
- `values-client-configs.yaml` - Client settings layer (non-MCP, non-model client config options)
- `values-semantic-search-redis.yaml` - Semantic cache + Redis vector store layer
- `values-semantic-search-weaviate.yaml` - Semantic cache + Weaviate vector store layer
- `values-mcp-routing.yaml` - MCP + routing layer (latest `mcp.*` globals, MCP client config, chain rule/fallback examples)
- `values-governance-teams.yaml` - Governance base layer (budgets, rate limits, customers, teams)
- `values-with-routing-rules-pricing.yaml` - Advanced governance layer (virtual keys, routing rules, pricing overrides, access profile)
- `values-with-pod-label.yaml` - Pod label overlay for StatefulSet template change testing

## Prerequisites

- A reachable Kubernetes cluster
- `kubectl` configured for that cluster
- `helm` installed

## TLS options for providers

`values-providers.yaml` includes provider `network_config` fields for TLS:

- `insecure_skip_verify` (optional, default false)
- `ca_cert_pem` (optional; inline PEM or `env.VAR_NAME`)

Helm values example:

```yaml
bifrost:
  providers:
    openai:
      network_config:
        insecure_skip_verify: false
        ca_cert_pem: "env.OPENAI_CA_CERT_PEM"
```

Equivalent `config.json` example:

```json
{
  "providers": {
    "openai": {
      "network_config": {
        "insecure_skip_verify": false,
        "ca_cert_pem": "env.OPENAI_CA_CERT_PEM"
      }
    }
  }
}
```

## Deploy with layered `-f`

```bash
NAMESPACE="bifrost-examples"
RELEASE_NAME="bifrost-statefulset-upgrade"

kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

# SQLite base stack
helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-sqlite.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  --wait \
  --timeout 5m

# Postgres base stack
helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-postgres.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  --wait \
  --timeout 5m

# Full governance stack (SQLite + providers + teams + routing/pricing)
helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-sqlite.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/examples/values-governance-teams.yaml \
  -f examples/k8s/examples/values-with-routing-rules-pricing.yaml \
  --wait \
  --timeout 5m

# Client settings stack (SQLite + providers + client-config overlay)
helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-sqlite.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/examples/values-client-configs.yaml \
  --wait \
  --timeout 5m

# Semantic search stack (Redis vector store)
helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-sqlite.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/examples/values-semantic-search-redis.yaml \
  --wait \
  --timeout 5m

# Note: this Redis semantic layer uses Redis Stack (search-enabled) because
# semantic cache requires FT.* commands (RediSearch module). Redis Stack
# auto-loads search modules at startup.

# Semantic search stack (Weaviate vector store)
helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-sqlite.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/examples/values-semantic-search-weaviate.yaml \
  --wait \
  --timeout 5m

# MCP + routing stack (SQLite + providers + MCP/routing overlay)
helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-sqlite.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/examples/values-mcp-routing.yaml \
  --wait \
  --timeout 5m

# Add pod-label overlay on top of the same stack
helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-sqlite.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/examples/values-governance-teams.yaml \
  -f examples/k8s/examples/values-with-routing-rules-pricing.yaml \
  -f examples/k8s/examples/values-with-pod-label.yaml \
  --wait \
  --timeout 5m
  
```

## Dashboard auth env vars

`values-client-configs.yaml` enables dashboard auth and uses:

- `env.BIFROST_ADMIN_USERNAME`
- `env.BIFROST_ADMIN_PASSWORD`

Create a secret and inject it via `envFrom`:

```bash
kubectl -n "${NAMESPACE}" create secret generic bifrost-admin-auth \
  --from-literal=BIFROST_ADMIN_USERNAME=admin \
  --from-literal=BIFROST_ADMIN_PASSWORD='change-me' \
  --dry-run=client -o yaml | kubectl apply -f -
```

```yaml
# Add this in your layered values (or pass with --set/--set-file)
envFrom:
  - secretRef:
      name: bifrost-admin-auth
```

Then include `examples/k8s/examples/values-client-configs.yaml` in your Helm command.

## Local image deploy commands

If you built a local image (for example `bifrost-local:v1.5.0-prerelease21`) and want
to run with `image.pullPolicy=Never`, use commands like:

```bash
# Semantic search + Redis + client config (local image)
helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-sqlite.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/examples/values-client-configs.yaml \
  -f examples/k8s/examples/values-semantic-search-redis.yaml \
  --set image.repository=bifrost-local \
  --set image.tag=v1.5.0-prerelease21 \
  --set image.pullPolicy=Never \
  --wait \
  --timeout 5m

# Semantic search + Weaviate + client config (local image)
helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-sqlite.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/examples/values-client-configs.yaml \
  -f examples/k8s/examples/values-semantic-search-weaviate.yaml \
  --set image.repository=bifrost-local \
  --set image.tag=v1.5.0-prerelease21 \
  --set image.pullPolicy=Never \
  --wait \
  --timeout 5m
```

## Validate upgrade safety

1. Run:
   `helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost --namespace "${NAMESPACE}" -f examples/k8s/examples/values.yaml -f examples/k8s/examples/values-storage-sqlite.yaml -f examples/k8s/examples/values-providers.yaml -f examples/k8s/examples/values-with-pod-label.yaml --wait --timeout 5m`
2. Change `image.tag` in `values.yaml` (for example to a newer tag).
3. Run the same `helm upgrade --install ...` command again to perform an upgrade.

The second run should complete without StatefulSet immutable-field errors related to `volumeClaimTemplates`.
