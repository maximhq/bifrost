# Layered Helm Values Examples

These examples are split into composable value files so you can combine them with multiple `-f` flags.

## Files

- `values.yaml` - Base layer with shared image tag
- `values-storage-sqlite.yaml` - Storage layer for SQLite StatefulSet mode
- `values-storage-postgres.yaml` - Storage layer for Postgres mode (chart-managed Postgres)
- `values-providers.yaml` - Provider keys layer (`openai` + `anthropic`)
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

## Validate upgrade safety

1. Run:
   `helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost --namespace "${NAMESPACE}" -f examples/k8s/examples/values.yaml -f examples/k8s/examples/values-storage-sqlite.yaml -f examples/k8s/examples/values-providers.yaml -f examples/k8s/examples/values-with-pod-label.yaml --wait --timeout 5m`
2. Change `image.tag` in `values.yaml` (for example to a newer tag).
3. Run the same `helm upgrade --install ...` command again to perform an upgrade.

The second run should complete without StatefulSet immutable-field errors related to `volumeClaimTemplates`.
