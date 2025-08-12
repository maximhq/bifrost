#!/bin/bash
set -euo pipefail

# Cross-compile Go binaries for multiple platforms
# Usage: ./build-executables.sh

echo "🔨 Building Go executables..."

# Clean and create dist directory
rm -rf ../dist
mkdir -p ../dist

# Define platforms
platforms=(
  "darwin/amd64"
  "darwin/arm64" 
  "linux/amd64"
  "linux/arm64"
  "windows/amd64"
)

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
  mkdir -p "../dist/$PLATFORM_DIR/$GOARCH"
  
  if [[ "$GOOS" = "linux" ]]; then
    if [[ "$GOARCH" = "amd64" ]]; then
      CC_COMPILER="x86_64-linux-musl-gcc"
      CXX_COMPILER="x86_64-linux-musl-g++"
    elif [[ "$GOARCH" = "arm64" ]]; then
      CC_COMPILER="aarch64-linux-musl-gcc"
      CXX_COMPILER="aarch64-linux-musl-g++"
    fi
    
    env GOWORK=off CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" CC="$CC_COMPILER" CXX="$CXX_COMPILER" \
      go build -tags "netgo,osusergo,static_build" -ldflags "-linkmode external -extldflags -static" \
      -o "../dist/$PLATFORM_DIR/$GOARCH/$output_name" ./bifrost-http
      
  elif [[ "$GOOS" = "windows" ]]; then
    if [[ "$GOARCH" = "amd64" ]]; then
      CC_COMPILER="x86_64-w64-mingw32-gcc"
      CXX_COMPILER="x86_64-w64-mingw32-g++"
    fi
    
    env GOWORK=off CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" CC="$CC_COMPILER" CXX="$CXX_COMPILER" \
      go build -o "../dist/$PLATFORM_DIR/$GOARCH/$output_name" ./bifrost-http
      
  else # Darwin (macOS)
    env GOWORK=off CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" \
      go build -o "../dist/$PLATFORM_DIR/$GOARCH/$output_name" ./bifrost-http
  fi
done

echo "✅ All binaries built successfully"
