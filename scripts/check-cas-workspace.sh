#!/usr/bin/env bash
# Fail closed if a release checkout resolves any Bifrost module remotely.
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT/transports"
command -v go >/dev/null
export CAS_RELEASE_ROOT="$ROOT"
go list -m -json all | python3 -c '
import json, os, pathlib, sys
root = pathlib.Path(os.environ["CAS_RELEASE_ROOT"]).resolve()
text = sys.stdin.read(); decoder = json.JSONDecoder(); modules = {}
while text.strip():
    module, end = decoder.raw_decode(text.lstrip()); text = text.lstrip()[end:]
    if module["Path"].startswith("github.com/maximhq/bifrost/"):
        modules[module["Path"]] = module
expected = [root / "core", root / "framework", root / "transports", *sorted((root / "plugins").glob("*/go.mod"))]
for item in expected:
    directory = item.parent if item.name == "go.mod" else item
    name = next(line.split()[1] for line in (directory / "go.mod").read_text().splitlines() if line.startswith("module "))
    module = modules.get(name)
    if not module or pathlib.Path(module.get("Dir", "")).resolve() != directory.resolve() or not module.get("Main"):
        raise SystemExit("release requires local workspace module: " + name)
for name, module in modules.items():
    if not module.get("Main") or not pathlib.Path(module.get("Dir", "")).resolve().is_relative_to(root):
        raise SystemExit("nonlocal Bifrost module: " + name)
print("All Bifrost modules resolve to the release checkout")
'
