#!/usr/bin/env bash
set -euo pipefail

# Cross-compile Go binaries for multiple platforms
# Usage: ./build-executables.sh <version>

# Require version argument (matches usage)
if [[ -z "${1:-}" ]]; then
  echo "Usage: $0 <version>" >&2
  exit 1
fi
VERSION="$1"

echo "🔨 Building Go executables with version: $VERSION"

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

MODULE_PATH="$PROJECT_ROOT/transports/bifrost-http"


for platform in "${platforms[@]}"; do
  IFS='/' read -r PLATFORM_DIR GOARCH <<< "$platform"

  case "$PLATFORM_DIR" in
    "windows") GOOS="windows" ;;
    "darwin")  GOOS="darwin" ;;
    "linux")   GOOS="linux" ;;
    *) echo "Unsupported platform: $PLATFORM_DIR"; exit 1 ;;
  esac

  output_name="bifrost-http"
  [[ "$GOOS" = "windows" ]] && output_name+='.exe'

  echo "Building bifrost-http for $PLATFORM_DIR/$GOARCH..."
  mkdir -p "$PROJECT_ROOT/dist/$PLATFORM_DIR/$GOARCH"

  # Change to the module directory for building
  cd "$MODULE_PATH"

  if [[ "$GOOS" = "linux" ]]; then
    if [[ "$GOARCH" = "amd64" ]]; then
      CC_COMPILER="x86_64-linux-musl-gcc"
      CXX_COMPILER="x86_64-linux-musl-g++"
    elif [[ "$GOARCH" = "arm64" ]]; then
      CC_COMPILER="aarch64-linux-musl-gcc"
      CXX_COMPILER="aarch64-linux-musl-g++"
    fi

    env GOWORK=off CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" CC="$CC_COMPILER" CXX="$CXX_COMPILER" \
      go build -trimpath -tags "netgo,osusergo,sqlite_static" \
      -ldflags "-s -w -buildid= -extldflags '-static' -X main.Version=v${VERSION}" \
      -o "$PROJECT_ROOT/dist/$PLATFORM_DIR/$GOARCH/$output_name" .

  elif [[ "$GOOS" = "windows" ]]; then
    if [[ "$GOARCH" = "amd64" ]]; then
      CC_COMPILER="x86_64-w64-mingw32-gcc"
      CXX_COMPILER="x86_64-w64-mingw32-g++"
    fi

    env GOWORK=off CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" CC="$CC_COMPILER" CXX="$CXX_COMPILER" \
      go build -trimpath -ldflags "-s -w -buildid= -X main.Version=v${VERSION}" \
      -o "$PROJECT_ROOT/dist/$PLATFORM_DIR/$GOARCH/$output_name" .

   else # Darwin (macOS)
    if [[ "$GOARCH" = "amd64" ]]; then
      CC_COMPILER="o64-clang"
      CXX_COMPILER="o64-clang++"
    elif [[ "$GOARCH" = "arm64" ]]; then
      CC_COMPILER="oa64-clang"
      CXX_COMPILER="oa64-clang++"
    fi

    env GOWORK=off CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" CC="$CC_COMPILER" CXX="$CXX_COMPILER" \
      go build -trimpath -ldflags "-s -w -buildid= -X main.Version=v${VERSION}" \
      -o "$PROJECT_ROOT/dist/$PLATFORM_DIR/$GOARCH/$output_name" .
  fi

  # Change back to project root
  cd "$PROJECT_ROOT"
done

echo "✅ All binaries built successfully"
