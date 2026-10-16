#!/usr/bin/env bash
set -euo pipefail

# Test E2E UI with Playwright.
# Same path as `make run-e2e-ui` locally: tests/e2e/scripts/run-e2e.mjs builds the
# UI + bifrost-http, starts the dedicated bifrost-e2e Postgres (127.0.0.1:55432),
# boots WORKERS isolated Bifrost instances and schedules spec files across them.
# Usage: ./test-e2e-ui.sh [runner args, e.g. --features providers]

# Setup Go workspace for CI
source "$(dirname "$0")/setup-go-workspace.sh"

echo "🧪 Running E2E UI tests..."

trap 'node tests/e2e/scripts/run-e2e.mjs --down || true' EXIT

echo "📦 Installing Playwright dependencies..."
(cd tests/e2e && npm ci && npx playwright install --with-deps chromium)

CI=true MCP_SSE_HEADERS="${MCP_SSE_HEADERS:-}" node tests/e2e/scripts/run-e2e.mjs "$@"
