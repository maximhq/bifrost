# Security Improvements: Kubernetes Secrets for API Keys

## Overview

We have replaced hardcoded API keys in the Helm chart values examples with Kubernetes Secret references for better security. This prevents sensitive API keys from being stored in version control or as plaintext in Helm values.

## Changes Made

### 1. Values Files Updated

The following values examples now use `secretRef` instead of hardcoded API keys:

- `production-ha.yaml` - Production high-availability setup
- `sqlite-redis.yaml` - SQLite + Redis configuration
- `sqlite-weaviate.yaml` - SQLite + Weaviate configuration
- `postgres-redis.yaml` - PostgreSQL + Redis configuration
- `postgres-weaviate.yaml` - PostgreSQL + Weaviate configuration

### 2. New Secret Reference Pattern

**Before (Insecure):**
```yaml
plugins:
  semanticCache:
    enabled: true
    config:
      provider: "openai"
      keys:
        - "sk-..."  # Hardcoded API key
      embeddingModel: "text-embedding-3-small"
```

**After (Secure):**
```yaml
plugins:
  semanticCache:
    enabled: true
    secretRef:
      name: "bifrost-semantic-cache"
      key: "openai-key"
    config:
      provider: "openai"
      # keys are injected from the secret via environment variable
      embeddingModel: "text-embedding-3-small"
```

### 3. Helm Template Updates

#### ConfigMap Template (`templates/configmap.yaml`)
- Added logic to detect `secretRef` in semantic cache configuration
- When `secretRef` is present, adds `keysFromEnv` field to plugin config
- Maintains backward compatibility with direct key configuration

#### Deployment Template (`templates/deployment.yaml`)
- Added conditional environment variable injection
- Injects `SEMANTIC_CACHE_API_KEY` from the referenced Kubernetes Secret
- Only activated when `secretRef` is configured

### 4. New Files Created

#### `values-examples/semantic-cache-secret-example.yaml`
Example Kubernetes Secret manifest with:
- Instructions for creating the secret
- Template for the secret structure
- Security warnings about not committing real keys

## Usage

### Creating the Secret

**Option 1: Using kubectl (Recommended)**
```bash
kubectl create secret generic bifrost-semantic-cache \
  --from-literal=openai-key=sk-YOUR_OPENAI_API_KEY \
  -n <namespace>
```

**Option 2: Using a manifest file**
```bash
# Edit semantic-cache-secret-example.yaml with your API key
kubectl apply -f values-examples/semantic-cache-secret-example.yaml -n <namespace>
```

### Deploying with Secrets

```bash
# 1. Create the secret first
kubectl create secret generic bifrost-semantic-cache \
  --from-literal=openai-key=sk-YOUR_OPENAI_API_KEY \
  -n default

# 2. Deploy Bifrost with the values file
helm install bifrost . \
  -f values-examples/production-ha.yaml \
  -n default
```

## Backward Compatibility

The changes maintain full backward compatibility:

- **With secretRef**: Keys are injected via environment variable
- **Without secretRef**: Keys can still be provided directly in `config.keys` (not recommended for production)
- **Existing deployments**: Continue to work without changes

## Security Best Practices

### ✅ DO:
- Use Kubernetes Secrets for all API keys
- Create secrets in the same namespace as the deployment
- Use RBAC to restrict secret access
- Rotate API keys regularly
- Use tools like Sealed Secrets or External Secrets Operator for GitOps

### ❌ DON'T:
- Hardcode API keys in values files
- Commit secrets to version control
- Share secrets across namespaces unnecessarily
- Use plaintext keys in production environments

## Migration Guide

If you have existing deployments with hardcoded keys:

### Step 1: Create the Secret
```bash
# Extract your current key from values
kubectl create secret generic bifrost-semantic-cache \
  --from-literal=openai-key=YOUR_CURRENT_KEY \
  -n <namespace>
```

### Step 2: Update Your Values File
```yaml
# Remove the keys array
plugins:
  semanticCache:
    enabled: true
    # Add secretRef
    secretRef:
      name: "bifrost-semantic-cache"
      key: "openai-key"
    config:
      provider: "openai"
      # Remove: keys: ["sk-..."]
      embeddingModel: "text-embedding-3-small"
```

### Step 3: Upgrade the Deployment
```bash
helm upgrade bifrost . \
  -f your-updated-values.yaml \
  -n <namespace>
```

## Environment Variable

The semantic cache plugin now supports reading keys from the environment variable:
- **Variable Name**: `SEMANTIC_CACHE_API_KEY`
- **Source**: Kubernetes Secret referenced by `secretRef`
- **Format**: Single API key or comma-separated keys for multiple keys

## Troubleshooting

### Secret Not Found
```
Error: secrets "bifrost-semantic-cache" not found
```
**Solution**: Create the secret before deploying:
```bash
kubectl create secret generic bifrost-semantic-cache \
  --from-literal=openai-key=sk-YOUR_KEY \
  -n <namespace>
```

### Invalid API Key
```
Error: API key authentication failed
```
**Solution**: Verify the secret contains the correct key:
```bash
kubectl get secret bifrost-semantic-cache -n <namespace> -o jsonpath='{.data.openai-key}' | base64 -d
```

### Plugin Not Using Secret
**Solution**: Verify the values file includes the `secretRef` section and the pod has the environment variable:
```bash
kubectl exec -n <namespace> <pod-name> -- env | grep SEMANTIC_CACHE_API_KEY
```

## References

- [Kubernetes Secrets Documentation](https://kubernetes.io/docs/concepts/configuration/secret/)
- [Helm Secrets Management](https://helm.sh/docs/chart_best_practices/secrets/)
- [Bifrost Documentation](https://www.getbifrost.ai/docs)

