#!/bin/sh
# SBS fork of Bifrost: build the UI, the static binary (UI embedded) and the runtime image.
#
#   sbs/build.sh [tag]        default tag: sbs/bifrost:v2.6.0-sbs
#
# Everything runs in containers; Go modules, the Go build cache and node_modules live in the
# named volumes sbs_gomod, sbs_gocache and sbs_ui_node_modules so rebuilds are incremental.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TAG=${1:-sbs/bifrost:v2.6.0-sbs}
# Reported version: upstream release + build metadata, e.g. tag ...:v2.6.0-sbs.3 -> 2.6.0+sbs.3.
# Build metadata (not a "-" pre-release) so the UI does not see the upstream release as newer.
DEFAULT_VERSION=$(echo "${TAG##*:}" | sed -e 's/^v//' -e 's/-sbs/+sbs/')
VERSION=${VERSION:-$DEFAULT_VERSION}

echo "==> UI"
docker run --rm -v "$ROOT/ui":/src/ui -v sbs_ui_node_modules:/src/ui/node_modules -w /src/ui \
    -e NODE_OPTIONS=--max-old-space-size=4096 node:25-alpine3.23 \
    sh -c '[ -d node_modules/.bin ] || npm ci --no-audit --no-fund; npx vite build && npx tsc --noEmit'
rm -rf "$ROOT/transports/bifrost-http/ui"
cp -r "$ROOT/ui/out" "$ROOT/transports/bifrost-http/ui"

echo "==> binary"
mkdir -p "$ROOT/sbs/dist"
docker run --rm -v "$ROOT":/src -v sbs_gomod:/go/pkg/mod -v sbs_gocache:/root/.cache/go-build \
    -e GOWORK=/tmp/go.work -e CGO_ENABLED=1 -e GOOS=linux golang:1.27.4-alpine3.24 sh -c "
        set -e
        apk add -q gcc musl-dev sqlite-dev binutils binutils-gold >/dev/null
        cd /tmp && go work init /src/core /src/framework /src/transports
        for d in /src/plugins/*/; do [ -f \$d/go.mod ] && go work use \$d; done
        # Patched mcp-go (custom tool annotations), see sbs/mcp-go/SBS_PATCH.md.
        go work edit -replace github.com/mark3labs/mcp-go=/src/sbs/mcp-go
        cd /src/transports
        go build -ldflags=\"-w -s -X main.Version=v$VERSION -extldflags '-static'\" -trimpath \
            -tags sqlite_static -o /src/sbs/dist/bifrost ./bifrost-http
    "
cp "$ROOT/transports/docker-entrypoint.sh" "$ROOT/sbs/dist/docker-entrypoint.sh"

echo "==> image $TAG"
docker build -f "$ROOT/sbs/Dockerfile.runtime" -t "$TAG" "$ROOT/sbs"
echo "built $TAG"
