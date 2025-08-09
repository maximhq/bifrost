#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: $0 <package-name> <output-dir> <build-flags> <app-dir>"
  exit 1
}

# Input validation
if [[ $# -ne 4 ]]; then
  usage
fi

package_name="$1"
output_path=$(pwd)/"$2"
build_flags="$3"
app_dir="$4"

echo "Cleaning dist..."
rm -rf "$output_path"
mkdir -p "$output_path" || { echo "Failed to create output directory"; exit 1; }

platforms=("darwin/amd64" "darwin/arm64" "linux/amd64" "linux/arm64" "windows/amd64")

cd "$app_dir" || { echo "Error: Failed to change to directory $app_dir"; exit 1; }

for platform in "${platforms[@]}"; do
  IFS='/' read -r PLATFORM_DIR GOARCH <<< "$platform"

  case "$PLATFORM_DIR" in
    "windows") GOOS="windows" ;;
    "darwin")  GOOS="darwin" ;;
    "linux")   GOOS="linux" ;;
    *) echo "Unsupported platform: $PLATFORM_DIR"; exit 1 ;;
  esac

  output_name="$package_name"
  [[ "$GOOS" = "windows" ]] && output_name+='.exe'

  echo "Building $package_name for $PLATFORM_DIR/$GOARCH..."
  mkdir -p "$output_path/$PLATFORM_DIR/$GOARCH"

  if [[ "$GOOS" = "linux" ]]; then
    if [[ "$GOARCH" = "amd64" ]]; then
      CC_COMPILER="x86_64-linux-musl-gcc"
      CXX_COMPILER="x86_64-linux-musl-g++"
    elif [[ "$GOARCH" = "arm64" ]]; then
      CC_COMPILER="aarch64-linux-musl-gcc"
      CXX_COMPILER="aarch64-linux-musl-g++"
    else
      echo "Unsupported Linux architecture: $GOARCH"
      exit 1
    fi

    if ! command -v "$CC_COMPILER" >/dev/null; then
      echo "Compiler $CC_COMPILER not found"
      exit 1
    fi

    # Fully static linking flags
    build_args=(
      -tags "netgo,osusergo,static_build"
      -ldflags "-linkmode external -extldflags -static"
    )

    env GOWORK=off CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" CC="$CC_COMPILER" CXX="$CXX_COMPILER" \
      go build "${build_args[@]}" -o "$output_path/$PLATFORM_DIR/$GOARCH/$output_name" ${build_flags:+"$build_flags"}

  elif [[ "$GOOS" = "windows" ]]; then
    if [[ "$GOARCH" = "amd64" ]]; then
      CC_COMPILER="x86_64-w64-mingw32-gcc"
      CXX_COMPILER="x86_64-w64-mingw32-g++"
    else
      echo "Unsupported Windows architecture: $GOARCH"
      exit 1
    fi

    if ! command -v "$CC_COMPILER" >/dev/null; then
      echo "Compiler $CC_COMPILER not found"
      exit 1
    fi

    env GOWORK=off CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" CC="$CC_COMPILER" CXX="$CXX_COMPILER" \
      go build -o "$output_path/$PLATFORM_DIR/$GOARCH/$output_name" ${build_flags:+"$build_flags"}

  else # Darwin (macOS)
    env GOWORK=off CGO_ENABLED=1 GOOS="$GOOS" GOARCH="$GOARCH" \
      go build -o "$output_path/$PLATFORM_DIR/$GOARCH/$output_name" ${build_flags:+"$build_flags"}
  fi
done
