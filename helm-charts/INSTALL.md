# Bifrost Helm Chart Installation Guide

Complete guide for installing Bifrost using Helm.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Installation Methods](#installation-methods)
- [Quick Start Examples](#quick-start-examples)
- [Configuration](#configuration)
- [Post-Installation](#post-installation)
- [Troubleshooting](#troubleshooting)

## Prerequisites

### Required

- Kubernetes cluster (v1.27+)
- `kubectl` configured to communicate with your cluster
- Helm 3.2.0+ installed

### Optional

- Persistent Volume provisioner (for data persistence)
- Ingress controller (for external access)
- Prometheus (for metrics)

### Verify Prerequisites

```bash
# Check Kubernetes connection
kubectl cluster-info

# Check Helm version
helm version

# Check available storage classes
kubectl get storageclass
```

## Installation Methods

### Method 1: Helm Repository (Recommended)

**Step 1:** Add the Bifrost Helm repository

```bash
helm repo add bifrost https://maximhq.github.io/bifrost/helm-charts
helm repo update
```

**Step 2:** Install Bifrost

```bash
helm install bifrost bifrost/bifrost
```

**Step 3:** Verify installation

```bash
kubectl get pods -l app.kubernetes.io/name=bifrost
```

### Method 2: Direct Install from GitHub Releases

Install a specific version directly:

```bash
helm install bifrost \
  https://github.com/maximhq/bifrost/releases/download/helm-chart-v1.3.5/bifrost-1.3.5.tgz
```

### Method 3: Install from Source

**Step 1:** Clone the repository

```bash
git clone https://github.com/maximhq/bifrost.git
cd bifrost/helm-charts
```

**Step 2:** Install Bifrost

```bash
helm install bifrost ./bifrost
```

### Method 4: Install from Package

**Step 1:** Download the chart package

```bash
wget https://maximhq.github.io/bifrost/helm-charts/bifrost-1.3.5.tgz
```

**Step 2:** Install from the package

```bash
helm install bifrost bifrost-1.3.5.tgz
```

## Quick Start Examples

### Example 1: Development (SQLite)

Perfect for local testing and development.

```bash
helm install bifrost bifrost/bifrost \
  --set bifrost.providers.openai.keys[0].value="sk-your-key" \
  --set bifrost.providers.openai.keys[0].weight=1
```

**What you get:**
- Single replica
- SQLite storage (10Gi PVC)
- ClusterIP service
- No auto-scaling

**Access:**
```bash
kubectl port-forward svc/bifrost 8080:8080
curl http://localhost:8080/metrics
```

### Example 2: Production (PostgreSQL)

Production-ready with high availability.

```bash
# Create a values file
cat <<EOF > production.yaml
replicaCount: 3

storage:
  mode: postgres

postgresql:
  enabled: true
  auth:
    password: "$(openssl rand -base64 32)"
  primary:
    persistence:
      size: 50Gi

autoscaling:
  enabled: true
  minReplicas: 3
  maxReplicas: 10

ingress:
  enabled: true
  className: nginx
  hosts:
    - host: bifrost.yourdomain.com
      paths:
        - path: /
          pathType: Prefix

bifrost:
  encryptionKey: "$(openssl rand -base64 32)"
  providers:
    openai:
      keys:
        - value: "$OPENAI_API_KEY"
          weight: 1
  plugins:
    telemetry:
      enabled: true
    logging:
      enabled: true
EOF

# Install
helm install bifrost bifrost/bifrost -f production.yaml
```

**What you get:**
- 3 initial replicas
- Auto-scaling (3-10 pods)
- PostgreSQL database
- Ingress with custom domain
- Telemetry and logging enabled

### Example 3: AI Workloads with Semantic Caching

Optimized for high-volume AI inference.

```bash
# Create secret for API keys
kubectl create secret generic bifrost-secrets \
  --from-literal=openai-key="$OPENAI_API_KEY" \
  --from-literal=semantic-cache-key="$OPENAI_API_KEY"

# Create values file
cat <<EOF > ai-workload.yaml
storage:
  mode: postgres

postgresql:
  enabled: true
  auth:
    password: "strong-password"

vectorStore:
  enabled: true
  type: weaviate

vectorStore.weaviate:
  enabled: true
  persistence:
    size: 50Gi

bifrost:
  providers:
    openai:
      keys:
        - value: "$OPENAI_API_KEY"
          weight: 1
  plugins:
    semanticCache:
      enabled: true
      config:
        provider: "openai"
        embeddingModel: "text-embedding-3-small"
        threshold: 0.8
        ttl: "5m"
EOF

# Install
helm install bifrost bifrost/bifrost -f ai-workload.yaml
```

**What you get:**
- PostgreSQL for config/logs
- Weaviate for vector storage
- Semantic caching enabled
- Optimized for AI workloads

### Example 4: Multi-Provider Setup

Support for multiple LLM providers.

```bash
cat <<EOF > multi-provider.yaml
bifrost:
  providers:
    openai:
      keys:
        - value: "$OPENAI_API_KEY"
          weight: 1
    anthropic:
      keys:
        - value: "$ANTHROPIC_API_KEY"
          weight: 1
    gemini:
      keys:
        - value: "$GEMINI_API_KEY"
          weight: 1
    cohere:
      keys:
        - value: "$COHERE_API_KEY"
          weight: 1
EOF

helm install bifrost bifrost/bifrost -f multi-provider.yaml
```

### Example 5: External Database

Use existing PostgreSQL instance.

```bash
cat <<EOF > external-db.yaml
storage:
  mode: postgres

postgresql:
  enabled: false
  external:
    enabled: true
    host: "postgres.example.com"
    port: 5432
    user: "bifrost"
    password: "your-password"
    database: "bifrost"
    sslMode: "require"

bifrost:
  providers:
    openai:
      keys:
        - value: "$OPENAI_API_KEY"
          weight: 1
EOF

helm install bifrost bifrost/bifrost -f external-db.yaml
```

## Configuration

### Minimal Configuration

```yaml
# minimal.yaml
bifrost:
  providers:
    openai:
      keys:
        - value: "sk-..."
          weight: 1
```

```bash
helm install bifrost bifrost/bifrost -f minimal.yaml
```

### Recommended Production Configuration

```yaml
# production.yaml
replicaCount: 3

image:
  pullPolicy: IfNotPresent

storage:
  mode: postgres
  persistence:
    enabled: true
    size: 50Gi

postgresql:
  enabled: true
  auth:
    username: bifrost
    password: "CHANGE_ME"
    database: bifrost
  primary:
    persistence:
      enabled: true
      size: 50Gi
    resources:
      requests:
        cpu: 500m
        memory: 1Gi
      limits:
        cpu: 2000m
        memory: 2Gi

autoscaling:
  enabled: true
  minReplicas: 3
  maxReplicas: 10
  targetCPUUtilizationPercentage: 70
  targetMemoryUtilizationPercentage: 80

ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  hosts:
    - host: bifrost.yourdomain.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: bifrost-tls
      hosts:
        - bifrost.yourdomain.com

resources:
  requests:
    cpu: 500m
    memory: 1Gi
  limits:
    cpu: 2000m
    memory: 2Gi

bifrost:
  # Use 32-byte random value (generate with: openssl rand -base64 32)
  encryptionKey: "CHANGE_ME_32_BYTE_KEY"
  logLevel: info
  logStyle: json
  
  client:
    dropExcessRequests: true
    maxRequestBodySizeMb: 100
    enableLogging: true
    enableGovernance: true
  
  providers:
    openai:
      keys:
        - value: "CHANGE_ME"
          weight: 1
  
  plugins:
    telemetry:
      enabled: true
    logging:
      enabled: true
    governance:
      enabled: true
```

## Post-Installation

### 1. Verify Deployment

```bash
# Check pod status
kubectl get pods -l app.kubernetes.io/name=bifrost

# Check logs
kubectl logs -l app.kubernetes.io/name=bifrost --tail=50

# Check service
kubectl get svc bifrost
```

### 2. Test API Endpoint

```bash
# Port forward
kubectl port-forward svc/bifrost 8080:8080 &

# Test metrics endpoint
curl http://localhost:8080/metrics

# Test chat completion (if provider configured)
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-3.5-turbo",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

### 3. Configure DNS (If using Ingress)

Point your domain to the ingress controller's load balancer:

```bash
# Get ingress IP
kubectl get ingress bifrost

# Add DNS A record
# bifrost.yourdomain.com -> <INGRESS_IP>
```

### 4. Enable Monitoring (Optional)

```bash
# Port forward to metrics
kubectl port-forward svc/bifrost 8080:8080 &

# Scrape metrics
curl http://localhost:8080/metrics
```

### 5. Set Up Backup (Production)

For PostgreSQL:
```bash
# Create backup CronJob
kubectl create cronjob bifrost-backup \
  --image=postgres:16-alpine \
  --schedule="0 2 * * *" \
  -- /bin/bash -c "pg_dump -h bifrost-postgresql -U bifrost bifrost > /backup/bifrost-$(date +%Y%m%d).sql"
```

## Troubleshooting

### Pod Not Starting

```bash
# Check events
kubectl describe pod -l app.kubernetes.io/name=bifrost

# Check logs
kubectl logs -l app.kubernetes.io/name=bifrost --previous
```

Common issues:
- Missing PVC: Check storage class exists
- Image pull errors: Check image repository access
- Config errors: Check ConfigMap values

### Database Connection Failed

```bash
# For embedded PostgreSQL
kubectl exec -it deployment/bifrost-postgresql -- psql -U bifrost

# Check connection from Bifrost pod
kubectl exec -it deployment/bifrost -- nc -zv bifrost-postgresql 5432
```

### High Memory Usage

```bash
# Check resource usage
kubectl top pods -l app.kubernetes.io/name=bifrost

# Increase resources
helm upgrade bifrost bifrost/bifrost \
  --set resources.limits.memory=4Gi \
  --reuse-values
```

### Ingress Not Working

```bash
# Check ingress status
kubectl describe ingress bifrost

# Check ingress controller logs
kubectl logs -n ingress-nginx -l app.kubernetes.io/name=ingress-nginx
```

### Vector Store Connection Issues

```bash
# For Weaviate
kubectl port-forward svc/bifrost-weaviate 8080:8080
curl http://localhost:8080/v1/.well-known/ready

# For Redis
kubectl exec -it deployment/bifrost-redis-master -- redis-cli ping
```

## Upgrade

```bash
# Update repo
helm repo update

# Upgrade with same values
helm upgrade bifrost bifrost/bifrost --reuse-values

# Upgrade with new values
helm upgrade bifrost bifrost/bifrost -f your-values.yaml

# Rollback if needed
helm rollback bifrost
```

## Uninstall

```bash
# Uninstall release
helm uninstall bifrost

# Delete PVCs (if you want to remove data)
kubectl delete pvc -l app.kubernetes.io/instance=bifrost

# Delete secrets (if created manually)
kubectl delete secret bifrost-secrets
```

## Getting Help

- **Documentation**: <https://www.getbifrost.ai/docs>
- **GitHub Issues**: <https://github.com/maximhq/bifrost/issues>
- **Community**: Join our Discord/Slack (contact us for invite)
- **Email**: <mailto:support@getbifrost.ai>

## Next Steps

1. Review the [Configuration Reference](./bifrost/README.md)
2. Explore [Example Configurations](./bifrost/values-examples/)
3. Set up [Monitoring and Observability](<https://www.getbifrost.ai/docs/monitoring>)
4. Configure [Provider Keys](<https://www.getbifrost.ai/docs/providers>)
5. Enable [Plugins](<https://www.getbifrost.ai/docs/plugins>)
