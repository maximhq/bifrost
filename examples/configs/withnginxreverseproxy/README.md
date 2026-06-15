# Bifrost behind NGINX (Docker Compose)

This example runs 3 Bifrost containers behind an NGINX reverse proxy.

## Files

- `docker-compose.yml` - Starts NGINX and 3 Bifrost nodes
- `nginx.conf` - Reverse proxy and load balancing config
- `config.json` - Shared Bifrost config for all nodes
- `.env.example` - Required environment variables
- `helm-values.yaml` - Helm values for Kubernetes + NGINX Ingress
- `k8s-ingress.yaml` - Standalone ingress manifest (non-Helm or override)

## Run

```bash
cd examples/configs/withnginxreverseproxy
cp .env.example .env
# Edit .env and set real values

docker compose config
docker compose up -d
docker compose ps
```

NGINX exposes Bifrost on `http://localhost:8080`.

## Verify

```bash
# Health through NGINX
curl -i http://localhost:8080/health

# Chat completion through NGINX
curl -sS http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "Say hello"}]
  }'
```

Streaming check:

```bash
curl -N http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "stream": true,
    "messages": [{"role": "user", "content": "stream test"}]
  }'
```

## Stop

```bash
docker compose down
```

## Kubernetes / Helm

```bash
# Render manifests and verify ingress is present
helm template bifrost ./helm-charts/bifrost \
  -f examples/configs/withnginxreverseproxy/helm-values.yaml

# Install (or upgrade) with this example
helm upgrade --install bifrost ./helm-charts/bifrost \
  -f examples/configs/withnginxreverseproxy/helm-values.yaml
```

Validate ingress manifest only:

```bash
kubectl apply --dry-run=client -f examples/configs/withnginxreverseproxy/k8s-ingress.yaml
```
