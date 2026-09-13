#!/usr/bin/env bash
# Run only in an isolated release checkout; does not build or deploy a server.
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT/ui"
node --input-type=module -e 'const [major, minor] = process.versions.node.split(".").map(Number); if (major < 22 || (major === 22 && minor < 12)) throw new Error("Node >=22.12 required")'
npm ci --no-audit --no-fund
npx --no-install vitest run app/workspace/logs/sheets/logDetailsSheet.test.tsx
npm run build
# npm run build includes typecheck and copies out/ to the Go embed directory.
test -s out/index.html
test -s ../transports/bifrost-http/ui/index.html
test -n "$(find out/assets -type f -name '*.js' -print -quit)"
diff -qr out ../transports/bifrost-http/ui
printf 'Complete UI: %s/ui/out -> %s/transports/bifrost-http/ui\n' "$ROOT" "$ROOT"
