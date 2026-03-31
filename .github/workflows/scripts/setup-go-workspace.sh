#!/usr/bin/env bash
set -euo pipefail



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
