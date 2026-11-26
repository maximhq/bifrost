#!/usr/bin/env bash
set -euo pipefail

# Starts the compose stack's Weaviate for jobs that boot bifrost-http with
# tests/integrations/python/config.json, whose vector_store points at
# localhost:9000 (HTTP) and localhost:50051 (gRPC). Without it the gateway exits
# fatally at startup. Waits for HTTP readiness and the gRPC port, since the
# client does not check gRPC at startup.
#
# Usage: ./start-weaviate.sh

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
COMPOSE_FILE="$SCRIPT_DIR/../configs/docker-compose.yml"
TIMEOUT_SECONDS="${WEAVIATE_READY_TIMEOUT_SECONDS:-120}"

echo "🐳 Starting Weaviate..."
docker compose -f "$COMPOSE_FILE" up -d weaviate

echo "⏳ Waiting for Weaviate HTTP and gRPC..."
START=$SECONDS
until curl -fs --connect-timeout 2 --max-time 5 -o /dev/null "http://localhost:9000/v1/.well-known/ready" &&
  nc -z -w 2 127.0.0.1 50051 2>/dev/null; do
  if [ $((SECONDS - START)) -ge "$TIMEOUT_SECONDS" ]; then
    echo "❌ Weaviate was not ready within ${TIMEOUT_SECONDS}s"
    docker compose -f "$COMPOSE_FILE" logs weaviate | tail -50
    exit 1
  fi
  sleep 2
done
echo "✅ Weaviate ready ($((SECONDS - START))s)"