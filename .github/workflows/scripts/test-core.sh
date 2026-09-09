#!/usr/bin/env bash
set -euo pipefail

# Core release gate.
# Usage: ./test-core.sh
#
# The core module's own `go test` suite has been replaced here by the provider
# harness: it exercises the same provider code paths end-to-end through a live
# gateway instead of in-process. The core build is still validated first, both
# as a fast compile gate and because the harness needs a bifrost-http binary
# that links against this module.

if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi
cd "$(cd "$SCRIPT_DIR/../../.." && pwd -P)"

# Setup Go workspace for CI
source "$SCRIPT_DIR/setup-go-workspace.sh"

echo "🔨 Validating core build..."
pushd core > /dev/null
go mod download
go build ./...
popd > /dev/null
echo "✅ Core build validation successful"

echo "🧪 Running provider harness against core..."
"$SCRIPT_DIR/test-provider-harness.sh"

echo "✅ Core tests completed successfully"
