#!/usr/bin/env bash
set -euo pipefail

# Cross-compile CLI binaries for multiple platforms
# Usage: ./build-cli-executables.sh <version>

if [[ -z "${1:-}" ]]; then
  echo "Usage: $0 <version>" >&2
  exit 1
fi
VERSION="$1"

echo "🔨 Building CLI executables with version: $VERSION"

# Get the script directory and project root
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

# Clean and create dist directory
rm -rf "$PROJECT_ROOT/dist"
mkdir -p "$PROJECT_ROOT/dist"

# Define platforms
platforms=(
  "darwin/amd64"
  "darwin/arm64"
  "linux/amd64"
  "linux/arm64"
  "windows/amd64"
)

MODULE_PATH="$PROJECT_ROOT/cli"
COMMIT="${GITHUB_SHA:-$(git rev-parse HEAD 2>/dev/null || echo 'unknown')}"

for platform in "${platforms[@]}"; do
  IFS='/' read -r GOOS GOARCH <<< "$platform"

  output_name="bifrost"
  [[ "$GOOS" = "windows" ]] && output_name+='.exe'

  echo "Building bifrost CLI for $GOOS/$GOARCH..."
  mkdir -p "$PROJECT_ROOT/dist/$GOOS/$GOARCH"

  cd "$MODULE_PATH"

  # CLI has no CGO dependencies, so we can cross-compile without cross-compilers
  env GOWORK=off CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath \
    -ldflags "-s -w -buildid= -X main.version=v${VERSION} -X main.commit=${COMMIT}" \
    -o "$PROJECT_ROOT/dist/$GOOS/$GOARCH/$output_name" .

  # Generate SHA-256 checksum for the binary
  (cd "$PROJECT_ROOT/dist/$GOOS/$GOARCH" && shasum -a 256 "$output_name" > "$output_name.sha256")
  echo "  → checksum: $(cat "$PROJECT_ROOT/dist/$GOOS/$GOARCH/$output_name.sha256")"

  cd "$PROJECT_ROOT"
done

echo "✅ All CLI binaries built successfully"
