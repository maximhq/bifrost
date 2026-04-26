# Enterprise Helm Overlays

Enterprise overlays in this folder are designed to be layered on top of your current base deployment command.

## Files

- `values-guardrails.yaml` - Guardrails rules + guardrail provider configurations (all supported guardrails fields populated)
- `values-governance-org-setup.yaml` - Enterprise governance org setup (virtual keys, teams, business units)
- `values-access-profiles-governance.yaml` - Enterprise access profile setup (global budget, rate limit, provider-level budgets/rate limits)
- `values-customers-budgets.yaml` - Enterprise customer budgets setup (customers with budget/rate-limit bindings)
- `values-users-teams.yaml` - Enterprise teams setup (multiple teams + business units + team budgets + virtual keys)
- `values-governance-multi-customer.yaml` - Consolidated governance example with 2 customers, customer/team budgets, team rate limits, business units, and VK-budget mapping
- `values-scim.yaml` - Enterprise SCIM/SSO setup (safe-by-default; enable only with real IdP credentials)

## Base command (as deployed)

```bash
NAMESPACE="bifrost-examples"
RELEASE_NAME="bifrost-statefulset-upgrade"

kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "${NAMESPACE}" apply -f examples/k8s/examples/secrets-providers-sample.yaml

helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-postgres.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/examples/values-mcp-routing.yaml \
  --set image.repository=bifrost \
  --set image.tag=latest
```

## Add enterprise guardrails overlay

```bash
NAMESPACE="bifrost-examples"
RELEASE_NAME="bifrost-statefulset-upgrade"

kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "${NAMESPACE}" apply -f examples/k8s/examples/secrets-providers-sample.yaml

helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-postgres.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/enterprise/values-guardrails.yaml \
  --set image.repository=bifrost \
  --set image.tag=latest \
  --set image.pullPolicy=IfNotPresent \
  --wait --timeout 5m
```

## Add enterprise governance org overlay

```bash
NAMESPACE="bifrost-examples"
RELEASE_NAME="bifrost-statefulset-upgrade"

kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "${NAMESPACE}" apply -f examples/k8s/examples/secrets-providers-sample.yaml

helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-postgres.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/enterprise/values-governance-org-setup.yaml \
  --set image.repository=bifrost \
  --set image.tag=latest \
  --set image.pullPolicy=IfNotPresent \
  --wait --timeout 5m
```

## Add enterprise access profile governance overlay

```bash
NAMESPACE="bifrost-examples"
RELEASE_NAME="bifrost-statefulset-upgrade"

kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "${NAMESPACE}" apply -f examples/k8s/examples/secrets-providers-sample.yaml

helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-postgres.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/enterprise/values-access-profiles-governance.yaml \
  --set image.repository=bifrost \
  --set image.tag=latest \
  --set image.pullPolicy=IfNotPresent \
  --wait --timeout 5m
```

## Add enterprise customers and budgets overlay

```bash
NAMESPACE="bifrost-examples"
RELEASE_NAME="bifrost-statefulset-upgrade"

kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "${NAMESPACE}" apply -f examples/k8s/examples/secrets-providers-sample.yaml

helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-postgres.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/enterprise/values-customers-budgets.yaml \
  --set image.repository=bifrost \
  --set image.tag=latest \
  --set image.pullPolicy=IfNotPresent \
  --wait --timeout 5m
```

## Add enterprise teams overlay

```bash
NAMESPACE="bifrost-examples"
RELEASE_NAME="bifrost-statefulset-upgrade"

kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "${NAMESPACE}" apply -f examples/k8s/examples/secrets-providers-sample.yaml

helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-postgres.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/enterprise/values-users-teams.yaml \
  --set image.repository=bifrost \
  --set image.tag=latest \
  --set image.pullPolicy=IfNotPresent \
  --wait --timeout 5m
```

## Add consolidated multi-customer governance overlay

```bash
NAMESPACE="bifrost-examples"
RELEASE_NAME="bifrost-statefulset-upgrade"

kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "${NAMESPACE}" apply -f examples/k8s/examples/secrets-providers-sample.yaml

helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-postgres.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/enterprise/values-governance-multi-customer.yaml \
  --set image.repository=bifrost \
  --set image.tag=latest \
  --set image.pullPolicy=IfNotPresent \
  --wait --timeout 5m
```

## Add enterprise SCIM overlay

```bash
NAMESPACE="bifrost-examples"
RELEASE_NAME="bifrost-statefulset-upgrade"

kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "${NAMESPACE}" apply -f examples/k8s/examples/secrets-providers-sample.yaml

helm upgrade --install "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-postgres.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/enterprise/values-scim.yaml \
  --set image.repository=bifrost \
  --set image.tag=latest \
  --set image.pullPolicy=IfNotPresent \
  --wait --timeout 5m
```

## Optional: quick render check

```bash
helm template "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-postgres.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/examples/values-mcp-routing.yaml \
  -f examples/k8s/enterprise/values-guardrails.yaml \
  | rg "guardrails_config|guardrail_rules|guardrail_providers"
```

```bash
helm template "${RELEASE_NAME}" ./helm-charts/bifrost \
  --namespace "${NAMESPACE}" \
  -f examples/k8s/examples/values.yaml \
  -f examples/k8s/examples/values-storage-postgres.yaml \
  -f examples/k8s/examples/values-providers.yaml \
  -f examples/k8s/enterprise/values-governance-org-setup.yaml \
  -f examples/k8s/enterprise/values-access-profiles-governance.yaml \
  -f examples/k8s/enterprise/values-customers-budgets.yaml \
  | rg "governance|access_profiles|virtual_keys|teams|customers"
```
