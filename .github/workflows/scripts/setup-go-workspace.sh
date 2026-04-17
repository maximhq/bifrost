#!/usr/bin/env bash
set -euo pipefail

export GOTOOLCHAIN=auto

# If go.work exists, skip
if [ -f "go.work" ]; then
  echo "🔍 Go workspace already exists, skipping initialization"
  return
fi


# Setup Go workspace for CI
# Usage: source setup-go-workspace.sh
echo "🔧 Setting up Go workspace..."
if [ -f "go.work" ]; then
  echo "✅ Go workspace already exists, skipping init"
  return 0 2>/dev/null || exit 0
fi
go work init
# Bump workspace go directive so GOTOOLCHAIN=auto switches to >= 1.26.2
# (required by published core@v1.4.19 which is referenced from transports/go.mod).
go work edit -go=1.26.2 -toolchain=go1.26.2
go work use ./core
go work use ./framework
go work use ./plugins/governance
go work use ./plugins/jsonparser
go work use ./plugins/litellmcompat
go work use ./plugins/logging
go work use ./plugins/maxim
go work use ./plugins/mocker
go work use ./plugins/otel
go work use ./plugins/semanticcache
go work use ./plugins/telemetry
go work use ./transports
echo "✅ Go workspace initialized"
