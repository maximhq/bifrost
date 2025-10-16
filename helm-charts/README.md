# Bifrost Helm Charts

Official Helm charts for deploying [Bifrost](https://www.getbifrost.ai) on Kubernetes.

## Available Charts

- **bifrost**: Main application chart with support for multiple storage backends

## Quick Start

### Install with Default Configuration (SQLite)

```bash
helm install bifrost ./bifrost
```

### Install with PostgreSQL

```bash
helm install bifrost ./bifrost -f bifrost/values-examples/postgres-only.yaml
```

### Install with PostgreSQL + Weaviate

```bash
helm install bifrost ./bifrost -f bifrost/values-examples/postgres-weaviate.yaml
```

## Available Configurations

We provide several pre-configured examples in `bifrost/values-examples/`:

1. **postgres-only.yaml** - PostgreSQL for config and logs
2. **postgres-weaviate.yaml** - PostgreSQL + Weaviate vector store
3. **postgres-redis.yaml** - PostgreSQL + Redis vector store
4. **sqlite-only.yaml** - SQLite for config and logs
5. **sqlite-weaviate.yaml** - SQLite + Weaviate vector store
6. **sqlite-redis.yaml** - SQLite + Redis vector store
7. **external-postgres.yaml** - Use external PostgreSQL instance
8. **production-ha.yaml** - Production HA setup with auto-scaling

## Documentation

For detailed documentation, see the [Bifrost chart README](./bifrost/README.md).

## Repository Structure

```
helm-charts/
├── README.md                          # This file
└── bifrost/
    ├── Chart.yaml                     # Chart metadata
    ├── values.yaml                    # Default values
    ├── README.md                      # Detailed documentation
    ├── templates/                     # Kubernetes manifests
    │   ├── deployment.yaml
    │   ├── service.yaml
    │   ├── ingress.yaml
    │   ├── configmap.yaml
    │   ├── postgresql-*.yaml          # PostgreSQL resources
    │   ├── weaviate-*.yaml            # Weaviate resources
    │   └── redis-*.yaml               # Redis resources
    └── values-examples/               # Example configurations
        ├── postgres-only.yaml
        ├── postgres-weaviate.yaml
        ├── postgres-redis.yaml
        ├── sqlite-only.yaml
        ├── sqlite-weaviate.yaml
        ├── sqlite-redis.yaml
        ├── external-postgres.yaml
        └── production-ha.yaml
```

## Prerequisites

- Kubernetes 1.19+
- Helm 3.2.0+
- PV provisioner support (for persistent storage)

## Installation Examples

### Development Setup

```bash
# Simple SQLite setup for local development
helm install bifrost ./bifrost \
  --set bifrost.providers.openai.keys[0].value="sk-..." \
  --set bifrost.providers.openai.keys[0].weight=1
```

### Production Setup

```bash
# High-availability setup with PostgreSQL and monitoring
helm install bifrost ./bifrost \
  -f bifrost/values-examples/production-ha.yaml \
  --set bifrost.encryptionKey="your-secure-key" \
  --set postgresql.auth.password="secure-db-password" \
  --set ingress.hosts[0].host="bifrost.yourdomain.com"
```

### Semantic Caching Setup

```bash
# PostgreSQL with Weaviate for semantic caching
helm install bifrost ./bifrost \
  -f bifrost/values-examples/postgres-weaviate.yaml \
  --set bifrost.plugins.semanticCache.config.keys[0]="sk-your-embedding-key"
```

## Customization

Create your own values file:

```yaml
# my-values.yaml
storage:
  mode: postgres

postgresql:
  enabled: true

bifrost:
  encryptionKey: "my-encryption-key"
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

Then install:

```bash
helm install bifrost ./bifrost -f my-values.yaml
```

## Upgrade

```bash
helm upgrade bifrost ./bifrost -f your-values.yaml
```

## Uninstall

```bash
helm uninstall bifrost
```

## Support

- Documentation: https://www.getbifrost.ai/docs
- GitHub: https://github.com/maxim-ai/bifrost
- Issues: https://github.com/maxim-ai/bifrost/issues

## License

Apache 2.0 - See [LICENSE](../LICENSE) for more information.

