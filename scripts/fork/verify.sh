#!/usr/bin/env bash
# Checks that the fork's features still build and pass their tests, e.g. after an upstream sync.
#
# Usage: scripts/fork/verify.sh [--ui]
#   --ui   also build the UI and run its unit tests (needs node/npm)
#
# Runs the fork's own tests (rotation engine, antigravity/kiro providers, key_selection
# plumbing, sign-in endpoints, credential write-back, fork migrations), the core package tests
# (which cover the retry/rotation hooks) and the API collection fixture check. Failures in
# other upstream tests are upstream's to fix and are out of scope here.
set -euo pipefail

with_ui=false
while [ $# -gt 0 ]; do
  case "$1" in
    --ui) with_ui=true ;;
    -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

root="$(git rev-parse --show-toplevel)"
cd "$root"

step() { printf '\n==> %s\n' "$*"; }

step "go workspace"
scripts/fork/workspace.sh

step "core: build, vet, tests"
(
  cd core
  go build . ./keyselectors/ ./schemas/ ./providers/...
  go vet . ./keyselectors/ ./providers/antigravity/ ./providers/kiro/
  go test -count=1 . ./schemas/ ./keyselectors/ ./providers/antigravity/ ./providers/kiro/
)

step "framework: build, vet, configstore tests"
(
  cd framework
  go build ./...
  go vet ./configstore/...
  go test -count=1 ./configstore/ -run 'TestForkMigrations|KeySelection|PromptCache'
)

step "transports: build, vet, fork tests"
(
  cd transports
  go build ./...
  go vet ./bifrost-http/handlers/ ./bifrost-http/lib/ ./bifrost-http/server/
  go test -count=1 ./bifrost-http/handlers/ \
    -run 'OAuthSession|ParseAntigravityCallback|AntigravityComplete|KiroStart|KiroPoll|SubscriptionCredentials|KeySelection|ApplyProviderConfigUpdates'
  go test -count=1 ./bifrost-http/lib/ -run 'UpdateProviderKeyCredential|KeySelection'
)

if command -v node >/dev/null 2>&1; then
  step "API collection fixtures"
  node tests/e2e/api/collections/collection-scripts.test.mjs
fi

if [ "$with_ui" = true ]; then
  step "ui: build and unit tests"
  (cd ui && npm ci --no-audit --no-fund && npm run build && npx vitest run)
fi

step "fork verification passed"
