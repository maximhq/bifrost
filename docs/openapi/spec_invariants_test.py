#!/usr/bin/env python3
"""Structural invariants for the split OpenAPI sources. Run directly: `python3 spec_invariants_test.py`.

No test framework needed (docs/openapi has no test runner configured). These guard the
kinds of drift that survive a successful bundle: the bundler resolves whatever `$ref`s it
is handed and never checks that the path catalogue is coherent, so a duplicate template, a
null Path Item, an orphaned fragment, or a legacy URL mounted on the current spec all
produce a clean `openapi.json` that documents the wrong surface.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

import yaml


HERE = Path(__file__).resolve().parent
ENTRY = HERE / "openapi.yaml"
PATHS_DIR = HERE / "paths" / "management"

# Fragment files whose definitions are mounted by openapi.yaml. Anything defined in one of
# these and never referenced is dead weight that silently drifts out of sync with the code.
FRAGMENT_FILES = sorted(PATHS_DIR.glob("*.yaml"))

REF_RE = re.compile(r"\./paths/management/([\w.-]+)\.yaml#/([\w~/{}.-]+)")

# Fragments that are genuinely unmounted and predate this work. Listed rather than skipped
# so they stay visible; removing one is a separate change from keeping the catalogue honest.
KNOWN_ORPHANS = {
    "infrastructure.yaml": {"websocket-responses"},
}

passed = 0
failed = 0


def check(name, fn):
    global passed, failed
    try:
        fn()
        passed += 1
        print(f"  ok - {name}")
    except AssertionError as exc:
        failed += 1
        print(f"  FAIL - {name}\n    {exc}")


def load(path: Path):
    return yaml.safe_load(path.read_text(encoding="utf-8"))


spec = load(ENTRY)
paths = spec.get("paths") or {}
entry_text = ENTRY.read_text(encoding="utf-8")


def pointer_tokens(pointer: str) -> tuple[str, ...]:
    """Split on `/` first, then unescape - `~1` is a literal `/` inside one token, so
    `~1plugins~1{name}` is the single key `/plugins/{name}`, not three steps."""
    return tuple(
        token.replace("~1", "/").replace("~0", "~") for token in pointer.split("/")
    )


mounted_refs = {
    (filename, pointer_tokens(pointer))
    for filename, pointer in REF_RE.findall(entry_text)
}


def normalize(template: str) -> str:
    """Collapse `{anything}` so two paths differing only in parameter name collide."""
    return re.sub(r"\{[^}]+\}", "{}", template)


def test_no_null_path_items():
    empty = sorted(key for key, value in paths.items() if value is None)
    assert not empty, f"path keys with no Path Item: {empty}"


def test_no_duplicate_path_templates():
    seen: dict[str, list[str]] = {}
    for key in paths:
        seen.setdefault(normalize(key), []).append(key)
    dupes = {norm: keys for norm, keys in seen.items() if len(keys) > 1}
    assert not dupes, f"paths that collide after parameter normalization: {dupes}"


def resolve_pointer(document, tokens):
    """Walk already-unescaped pointer tokens, returning None when any step is absent."""
    node = document
    for token in tokens:
        if not isinstance(node, dict) or token not in node:
            return None
        node = node[token]
    return node


def test_every_mounted_fragment_exists():
    missing = []
    for filename, tokens in sorted(mounted_refs):
        source = PATHS_DIR / f"{filename}.yaml"
        if not source.exists():
            missing.append(f"{filename}.yaml (file)")
            continue
        if resolve_pointer(load(source) or {}, tokens) is None:
            missing.append(f"{filename}.yaml#/{'/'.join(tokens)}")
    assert not missing, f"openapi.yaml references fragments that do not exist: {missing}"


def test_no_orphaned_fragments():
    orphans = []
    for source in FRAGMENT_FILES:
        stem = source.stem
        defined = set(load(source) or {})
        referenced = {tokens[0] for name, tokens in mounted_refs if name == stem}
        # A fragment also counts as used when another fragment composes it by $ref -
        # cross-file (the legacy aliases pull in `./governanceextensions.yaml#/x/put`)
        # or same-file (logging.yaml's shared `_*-parameters` blocks).
        composed = set()
        for other in FRAGMENT_FILES:
            text = other.read_text(encoding="utf-8")
            for frag in defined:
                cross_file = f"{stem}.yaml#/{frag}/" in text
                same_file = other == source and f"'#/{frag}/" in text
                if cross_file or same_file:
                    composed.add(frag)
        unused = sorted(defined - referenced - composed - KNOWN_ORPHANS.get(source.name, set()))
        if unused:
            orphans.append(f"{source.name}: {unused}")
    assert not orphans, "fragments defined but never mounted or composed:\n    " + "\n    ".join(orphans)


def test_duplicate_operation_ids():
    """Two mounted operations must never share an operationId."""
    seen: dict[str, list[str]] = {}
    for filename, tokens in sorted(mounted_refs):
        source = PATHS_DIR / f"{filename}.yaml"
        if not source.exists():
            continue
        item = resolve_pointer(load(source) or {}, tokens) or {}
        for method, operation in item.items():
            if not isinstance(operation, dict):
                continue
            op_id = operation.get("operationId")
            if op_id:
                seen.setdefault(op_id, []).append(f"{filename}.yaml#/{'/'.join(tokens)}.{method}")
    dupes = {op: where for op, where in seen.items() if len(where) > 1}
    assert not dupes, f"operationId declared by more than one mounted operation: {dupes}"


def test_legacy_aliases_mount_legacy_fragments():
    """Every legacy alias defined in governancelegacy.yaml must be mounted, and each of its
    declared successors must itself be a documented path."""
    legacy_file = PATHS_DIR / "governancelegacy.yaml"
    legacy = load(legacy_file) or {}
    referenced = {tokens[0] for name, tokens in mounted_refs if name == "governancelegacy"}
    unmounted = sorted(set(legacy) - referenced)
    assert not unmounted, f"legacy aliases defined but never mounted: {unmounted}"

    missing_successors = set()
    for fragment, item in legacy.items():
        for operation in (item or {}).values():
            if not isinstance(operation, dict):
                continue
            successor = operation.get("x-bifrost-successor")
            if successor and successor not in paths:
                missing_successors.add(f"{fragment} -> {successor}")
    assert not missing_successors, (
        "legacy aliases point at successor paths that are not documented: "
        f"{sorted(missing_successors)}"
    )


check("no path key has a null Path Item", test_no_null_path_items)
check("no two paths collide after parameter normalization", test_no_duplicate_path_templates)
check("every fragment openapi.yaml mounts exists", test_every_mounted_fragment_exists)
check("no fragment is defined but never used", test_no_orphaned_fragments)
check("no operationId is claimed by two mounted operations", test_duplicate_operation_ids)
check("legacy aliases are mounted and their successors documented", test_legacy_aliases_mount_legacy_fragments)

print(f"\n{passed} passed, {failed} failed")
sys.exit(0 if failed == 0 else 1)
