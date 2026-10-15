#!/usr/bin/env bash
set -euo pipefail

# Run the #8212 regression with production scheduling under Go's virtual clock.
task_repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
task_go_cmd="${GO_BIN:-go}"

cd "$task_repo_root/transports"
exec "$task_go_cmd" test ./bifrost-http/server \
  -run '^TestIssue8212RetentionChange$' -count=1 -v -timeout=120s "$@"
