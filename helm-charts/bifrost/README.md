# Bifrost Helm Charts

[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/bifrost)](https://artifacthub.io/packages/helm/bifrost/bifrost)

Official Helm charts for deploying [Bifrost](https://github.com/maximhq/bifrost) - a high-performance AI gateway with unified interface for multiple providers.

**Latest Version:** 1.5.0

## Quick Start

```bash
# Add the Bifrost Helm repository
helm repo add bifrost https://maximhq.github.io/bifrost/helm-charts

# Update your local Helm chart repository cache
helm repo update

# Install Bifrost with default configuration (SQLite storage)
helm install bifrost bifrost/bifrost --set image.tag=v1.5.0
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
helm install bifrost bifrost/bifrost --set image.tag=v1.5.0

# Or install with custom values
helm install bifrost bifrost/bifrost -f my-values.yaml
```

### From Source

```bash
# Clone the repository
git clone https://github.com/maximhq/bifrost.git
cd bifrost/helm-charts

# Install from local chart
helm install bifrost ./bifrost --set image.tag=v1.3.37
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
  --set image.tag=v1.3.37

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
helm upgrade bifrost bifrost/bifrost --set image.tag=v1.3.37

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
        trace_type: "otel"
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

