#!/bin/bash
set -euo pipefail

# Install cross-compilation toolchains
# Usage: ./install-cross-compilers.sh

echo "📦 Installing cross-compilation toolchains..."

sudo apt-get update
sudo apt-get install -y \
  gcc-x86-64-linux-gnu \
  gcc-aarch64-linux-gnu \
  gcc-mingw-w64-x86-64 \
  musl-tools

# Create symbolic links for musl compilers
sudo ln -sf /usr/bin/x86_64-linux-gnu-gcc /usr/local/bin/x86_64-linux-musl-gcc
sudo ln -sf /usr/bin/x86_64-linux-gnu-g++ /usr/local/bin/x86_64-linux-musl-g++
sudo ln -sf /usr/bin/aarch64-linux-gnu-gcc /usr/local/bin/aarch64-linux-musl-gcc
sudo ln -sf /usr/bin/aarch64-linux-gnu-g++ /usr/local/bin/aarch64-linux-musl-g++

echo "✅ Cross-compilation toolchains installed"
