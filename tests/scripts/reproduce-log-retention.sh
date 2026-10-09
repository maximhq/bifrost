#!/usr/bin/env bash
set -euo pipefail

# Run the #8212 regression with a continuously running cleaner and an
# accelerated timer, without changing production scheduling.
task_repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
task_go_cmd="${GO_BIN:-go}"
task_overlay_dir=$(mktemp -d "${TMPDIR:-/tmp}/bifrost-8212.XXXXXX")
trap 'rm -rf -- "$task_overlay_dir"' EXIT

python3 - "$task_repo_root" "$task_overlay_dir" <<'PY'
import json
import re
import sys
from pathlib import Path

root = Path(sys.argv[1])
output = Path(sys.argv[2])
source = root / "framework/logstore/cleaner.go"
accelerated = source.read_text()
for name, value in (
    ("cleanupInterval", "time.Second"),
    ("minJitter", "time.Nanosecond"),
    ("maxJitter", "2 * time.Nanosecond"),
):
    accelerated, count = re.subn(
        r"(" + name + r"\s*=\s*)[^\n]+",
        lambda match: match[1] + value,
        accelerated,
        count=1,
    )
    if count != 1:
        raise SystemExit(f"Cannot find scheduler constant {name} in {source}")

overlay_source = output / "cleaner.go"
overlay_source.write_text(accelerated)
(output / "overlay.json").write_text(
    json.dumps({"Replace": {str(source): str(overlay_source)}})
)
PY

cd "$task_repo_root/transports"
env BIFROST_ISSUE_8212_REPRO=1 "$task_go_cmd" test \
  -overlay="$task_overlay_dir/overlay.json" \
  ./bifrost-http/server -run '^TestIssue8212RetentionChange$' \
  -count=1 -v -timeout=120s "$@"
