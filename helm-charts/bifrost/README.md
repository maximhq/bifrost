# Bifrost Helm Chart

This Helm chart deploys [Bifrost](https://www.getbifrost.ai) - an AI Gateway with unified interface for multiple LLM providers.

## Features

- 🚀 Support for multiple storage backends (SQLite, PostgreSQL)
- 🔍 Optional vector store integration (Weaviate, Redis)
- 📊 Built-in observability and metrics
- 🔐 Encryption support for sensitive data
- 🎯 Semantic caching capabilities
- 📈 Horizontal Pod Autoscaling
- 🌐 Ingress support with TLS
- 🔄 Multiple deployment configurations

## Prerequisites

- Kubernetes 1.19+
- Helm 3.2.0+
- PV provisioner support in the underlying infrastructure (if using persistence)

## Installation

### Quick Start (SQLite)

```bash
helm install bifrost ./bifrost
```

This will deploy Bifrost with SQLite as the storage backend.

### PostgreSQL Backend

```bash
helm install bifrost ./bifrost -f values-examples/postgres-only.yaml
```

### PostgreSQL + Weaviate

```bash
helm install bifrost ./bifrost -f values-examples/postgres-weaviate.yaml
```

### PostgreSQL + Redis

```bash
helm install bifrost ./bifrost -f values-examples/postgres-redis.yaml
```

### SQLite + Weaviate

```bash
helm install bifrost ./bifrost -f values-examples/sqlite-weaviate.yaml
```

### SQLite + Redis

```bash
helm install bifrost ./bifrost -f values-examples/sqlite-redis.yaml
```

### External PostgreSQL

```bash
# Edit values-examples/external-postgres.yaml with your database details
helm install bifrost ./bifrost -f values-examples/external-postgres.yaml
```

### Production HA Setup

```bash
# Edit values-examples/production-ha.yaml with your configuration
helm install bifrost ./bifrost -f values-examples/production-ha.yaml
```

## Configuration

### Storage Modes

The chart supports two storage modes controlled by `storage.mode`:

- **sqlite** (default): Uses SQLite databases stored in persistent volumes
- **postgres**: Uses PostgreSQL for config and logs storage

### Vector Store Options

Configure semantic caching with vector stores:

- **none** (default): No vector store
- **weaviate**: Use Weaviate for vector storage
- **redis**: Use Redis for vector storage

### Key Configuration Parameters

| Parameter | Description | Default |
|-----------|-------------|---------|
| `storage.mode` | Storage backend (sqlite/postgres) | `sqlite` |
| `storage.persistence.enabled` | Enable persistent storage for SQLite | `true` |
| `storage.persistence.size` | Size of persistent volume | `10Gi` |
| `postgresql.enabled` | Deploy PostgreSQL | `false` |
| `postgresql.external.enabled` | Use external PostgreSQL | `false` |
| `vectorStore.enabled` | Enable vector store | `false` |
| `vectorStore.type` | Vector store type (none/weaviate/redis) | `none` |
| `bifrost.encryptionKey` | Encryption key for sensitive data | `""` |
| `bifrost.client.enableLogging` | Enable request/response logging | `true` |
| `bifrost.providers` | LLM provider configurations | `{}` |
| `ingress.enabled` | Enable ingress | `false` |
| `autoscaling.enabled` | Enable HPA | `false` |

### Adding Provider Keys

Edit your values file or use `--set`:

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

Or via command line:

```bash
helm install bifrost ./bifrost \
  --set bifrost.providers.openai.keys[0].value="sk-..." \
  --set bifrost.providers.openai.keys[0].weight=1
```

### Enabling Plugins

```yaml
bifrost:
  plugins:
    telemetry:
      enabled: true
      config: {}
    
    logging:
      enabled: true
      config: {}
    
    governance:
      enabled: true
      config:
        isVkMandatory: false
    
    semanticCache:
      enabled: true
      config:
        provider: "openai"
        keys:
          - "sk-..."
        embeddingModel: "text-embedding-3-small"
        dimension: 1536
        threshold: 0.8
        ttl: "5m"
```

## Architecture Patterns

### Pattern 1: Simple Development Setup
- **Storage**: SQLite
- **Scale**: Single replica
- **Use Case**: Local development, testing

```bash
helm install bifrost ./bifrost
```

### Pattern 2: Production with PostgreSQL
- **Storage**: PostgreSQL
- **Scale**: Multiple replicas with HPA
- **Features**: Logging, telemetry, governance
- **Use Case**: Production deployments

```bash
helm install bifrost ./bifrost -f values-examples/production-ha.yaml
```

### Pattern 3: ML/AI Workloads
- **Storage**: PostgreSQL
- **Vector Store**: Weaviate
- **Features**: Semantic caching, embeddings
- **Use Case**: High-volume AI inference with caching

```bash
helm install bifrost ./bifrost -f values-examples/postgres-weaviate.yaml
```

## Upgrade

```bash
helm upgrade bifrost ./bifrost -f your-values.yaml
```

## Uninstall

```bash
helm uninstall bifrost
```

To delete PVCs:

```bash
kubectl delete pvc -l app.kubernetes.io/instance=bifrost
```

## Accessing Bifrost

### Port Forward (ClusterIP)

```bash
export POD_NAME=$(kubectl get pods -l "app.kubernetes.io/name=bifrost,app.kubernetes.io/instance=bifrost" -o jsonpath="{.items[0].metadata.name}")
kubectl port-forward $POD_NAME 8080:8080
```

Then access at http://localhost:8080

### LoadBalancer

```bash
export SERVICE_IP=$(kubectl get svc bifrost --template "{{ range (index .status.loadBalancer.ingress 0) }}{{.}}{{ end }}")
echo http://$SERVICE_IP:8080
```

### Ingress

Configure `ingress.enabled=true` and access via your domain.

## Monitoring

Bifrost exposes Prometheus metrics at `/metrics` endpoint.

Enable telemetry plugin:

```yaml
bifrost:
  plugins:
    telemetry:
      enabled: true
```

## Security Considerations

1. **Encryption Key**: Always set a strong encryption key for production:
   ```yaml
   bifrost:
     encryptionKey: "your-secure-32-byte-key-here"
   ```

2. **Database Passwords**: Use strong passwords for PostgreSQL/Redis:
   ```yaml
   postgresql:
     auth:
       password: "use-a-strong-password"
   ```

3. **Secrets Management**: Consider using external secret management:
   ```yaml
   envFrom:
     - secretRef:
         name: bifrost-secrets
   ```

4. **Network Policies**: Implement Kubernetes network policies to restrict traffic.

5. **RBAC**: Use appropriate service account permissions.

## Troubleshooting

### Check Pod Status

```bash
kubectl get pods -l app.kubernetes.io/name=bifrost
kubectl logs -l app.kubernetes.io/name=bifrost
```

### Check Configuration

```bash
kubectl get configmap bifrost-config -o yaml
```

### Database Connection Issues

For PostgreSQL:
```bash
kubectl exec -it deployment/bifrost-postgresql -- psql -U bifrost -d bifrost
```

For SQLite:
```bash
kubectl exec -it deployment/bifrost -- ls -la /app/data/
```

### Vector Store Issues

Check Weaviate:
```bash
kubectl logs -l app.kubernetes.io/component=vectorstore
kubectl port-forward svc/bifrost-weaviate 8080:8080
```

Check Redis:
```bash
kubectl logs -l app.kubernetes.io/component=redis
kubectl exec -it deployment/bifrost-redis-master -- redis-cli ping
```

## Examples

### Example 1: Deploy with OpenAI Provider

```bash
cat <<EOF | helm install bifrost ./bifrost -f -
bifrost:
  providers:
    openai:
      keys:
        - value: "$OPENAI_API_KEY"
          weight: 1
EOF
```

### Example 2: Deploy with Multiple Providers

```yaml
# my-values.yaml
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
    gemini:
      keys:
        - value: "..."
          weight: 1
```

```bash
helm install bifrost ./bifrost -f my-values.yaml
```

### Example 3: Production Setup with Monitoring

```yaml
# production.yaml
replicaCount: 3

autoscaling:
  enabled: true
  minReplicas: 3
  maxReplicas: 10

storage:
  mode: postgres

postgresql:
  enabled: true
  primary:
    persistence:
      size: 50Gi

vectorStore:
  enabled: true
  type: weaviate

bifrost:
  encryptionKey: "production-key-32-bytes-long"
  plugins:
    telemetry:
      enabled: true
    logging:
      enabled: true
    semanticCache:
      enabled: true
```

## Support

- Documentation: [Bifrost Docs](https://www.getbifrost.ai/docs)
- GitHub: [maximhq/bifrost](https://github.com/maximhq/bifrost)
- Issues: [GitHub Issues](https://github.com/maximhq/bifrost/issues)

## License

This chart is provided under the same license as Bifrost.

