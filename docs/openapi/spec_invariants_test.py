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


def test_vk_rotation_cooldown_bounds_match_config_schema():
    """The client-config contract lives in transports/config.schema.json; every copy of
    the vk_rotation_cooldown property in the OpenAPI sources and bundle must carry the
    same minimum/maximum, or generated clients silently drop the 30-day bound."""
    import json

    contract = json.loads(
        (HERE.parent.parent / "transports" / "config.schema.json").read_text(encoding="utf-8")
    )
    client_config = contract["properties"]["client"]["properties"]["vk_rotation_cooldown"]
    want = {"minimum": client_config["minimum"], "maximum": client_config["maximum"]}

    def collect(node, where, out):
        if isinstance(node, dict):
            prop = node.get("vk_rotation_cooldown")
            if isinstance(prop, dict) and "type" in prop:
                out.append((where, prop))
            for value in node.values():
                collect(value, where, out)
        elif isinstance(node, list):
            for value in node:
                collect(value, where, out)

    copies = []
    collect(load(HERE / "schemas" / "management" / "config.yaml"), "schemas/management/config.yaml", copies)
    collect(json.loads((HERE / "openapi.json").read_text(encoding="utf-8")), "openapi.json", copies)
    assert copies, "no vk_rotation_cooldown property found in the OpenAPI sources"
    drifted = [
        f"{where}: has minimum={prop.get('minimum')} maximum={prop.get('maximum')}, want {want}"
        for where, prop in copies
        if {"minimum": prop.get("minimum"), "maximum": prop.get("maximum")} != want
    ]
    assert not drifted, "vk_rotation_cooldown bounds drift from config.schema.json:\n    " + "\n    ".join(drifted)


def test_bulk_rotate_ids_requires_min_items():
    """The bulk rotate handler 400s on an empty ids array; the schema must say so via
    minItems in both the YAML source and the bundle, or generated clients allow []."""
    import json

    source = load(PATHS_DIR / "governance.yaml")
    source_ids = source["virtual-keys-rotate"]["post"]["requestBody"]["content"][
        "application/json"
    ]["schema"]["properties"]["ids"]
    bundle = json.loads((HERE / "openapi.json").read_text(encoding="utf-8"))
    bundle_ids = bundle["paths"]["/api/governance/virtual-keys/rotate"]["post"]["requestBody"][
        "content"
    ]["application/json"]["schema"]["properties"]["ids"]
    drifted = [
        f"{where}: ids minItems={ids.get('minItems')}, want 1"
        for where, ids in (("paths/management/governance.yaml", source_ids), ("openapi.json", bundle_ids))
        if ids.get("minItems") != 1
    ]
    assert not drifted, "bulk rotate ids schema permits []:\n    " + "\n    ".join(drifted)

def test_every_operation_declares_security():
    """Every mounted operation must declare its own `security`.

    The root `security` block is a fail-closed fallback, not a default to lean on: an
    operation that omits `security` silently advertises the root's inference-shaped
    credentials, which is how `/health`, `/metrics` and `/ws` drifted. `security: []`
    is a valid, meaningful declaration (genuinely public endpoints); absence is not.
    """
    methods = {"get", "post", "put", "delete", "patch", "head", "options", "trace"}
    missing: list[str] = []
    for template, item in sorted(paths.items()):
        if not isinstance(item, dict):
            continue
        ref = item.get("$ref")
        if not ref:
            continue
        file_part, _, pointer = ref.partition("#/")
        source = (HERE / file_part.lstrip("./")).resolve()
        if not source.exists():
            continue  # test_every_mounted_fragment_exists owns this failure
        resolved = resolve_pointer(load(source) or {}, pointer_tokens(pointer)) or {}
        for method, operation in resolved.items():
            if method not in methods or not isinstance(operation, dict):
                continue
            # A legacy alias is a $ref to a real operation and inherits its security.
            if "$ref" in operation:
                continue
            if "security" not in operation:
                missing.append(f"{method.upper()} {template} ({file_part}#/{pointer})")
    assert not missing, (
        "operation does not declare `security` and falls through to the root default:\n    "
        + "\n    ".join(missing)
    )


def test_virtual_key_request_contract_is_current():
    """Virtual Key writes use multi-budget arrays and provider-scoped key IDs.
    Guard both the modular source and published bundle against pre-v1.5 request fields."""
    import json

    source = load(HERE / "schemas" / "management" / "governance.yaml")
    bundle = json.loads((HERE / "openapi.json").read_text(encoding="utf-8"))[
        "components"
    ]["schemas"]
    problems = []

    for schema_name in ("CreateVirtualKeyRequest", "UpdateVirtualKeyRequest"):
        for where, schema in (
            ("schemas/management/governance.yaml", source[schema_name]),
            ("openapi.json", bundle[schema_name]),
        ):
            properties = schema["properties"]
            provider_properties = properties["provider_configs"]["items"]["properties"]

            for legacy in ("budget", "budget_id", "allowed_keys", "key_ids"):
                if legacy in properties:
                    problems.append(f"{where} {schema_name}: unexpected top-level {legacy}")
            if "budgets" not in properties:
                problems.append(f"{where} {schema_name}: missing top-level budgets array")

            for legacy in ("budget", "budget_id", "allowed_keys"):
                if legacy in provider_properties:
                    problems.append(
                        f"{where} {schema_name}.provider_configs: unexpected {legacy}"
                    )
            for current in ("budgets", "key_ids"):
                if current not in provider_properties:
                    problems.append(
                        f"{where} {schema_name}.provider_configs: missing {current}"
                    )

            example = schema.get("example") or {}
            example_provider = (example.get("provider_configs") or [{}])[0]
            if not isinstance(example.get("budgets"), list):
                problems.append(f"{where} {schema_name} example: budgets is not an array")
            if example_provider.get("key_ids") != ["*"]:
                problems.append(
                    f'{where} {schema_name} example: key_ids must explicitly use ["*"]'
                )
            if not isinstance(example_provider.get("budgets"), list):
                problems.append(
                    f"{where} {schema_name} example: provider budgets is not an array"
                )

    assert not problems, "Virtual Key request contract drift:\n    " + "\n    ".join(problems)


def test_model_access_fields_are_documented_consistently():
    """The three Virtual Key provider-config schemas describe model access identically.

    A response schema and its create/update request twins are read side by side, and the
    allow/deny contract is asymmetric enough to be worth stating in full on each: an empty
    allowlist denies every model, an empty denylist blocks none. Wording that drifts
    between the three reads as three different rules.
    """
    source = load(HERE / "schemas" / "management" / "governance.yaml")
    fields = (
        "allowed_models",
        "blacklisted_models",
        "allowed_models_patterns",
        "blacklisted_models_patterns",
    )
    blocks = {
        "VirtualKeyProviderConfig": source["VirtualKeyProviderConfig"]["properties"],
        "CreateVirtualKeyRequest": source["CreateVirtualKeyRequest"]["properties"][
            "provider_configs"
        ]["items"]["properties"],
        "UpdateVirtualKeyRequest": source["UpdateVirtualKeyRequest"]["properties"][
            "provider_configs"
        ]["items"]["properties"],
    }

    def normalize(text: str) -> str:
        return " ".join(text.split())

    problems = []
    for field in fields:
        seen = {}
        for name, properties in blocks.items():
            if field not in properties:
                problems.append(f"{name}: {field} is not declared")
                continue
            seen.setdefault(normalize(properties[field].get("description", "")), []).append(name)
        if len(seen) > 1:
            wordings = "; ".join(f"{sorted(names)}: {text!r}" for text, names in seen.items())
            problems.append(f"{field} is described differently across the three schemas: {wordings}")

    # The allowlist already states what an empty list means and that `*` cannot be
    # mixed with names. The denylist is the half readers get wrong, and BlackList.Validate
    # enforces the same wildcard rule on it, so it has to say both too.
    for name, properties in blocks.items():
        denylist = normalize(properties.get("blacklisted_models", {}).get("description", ""))
        if "empty list blocks no models" not in denylist:
            problems.append(
                f"{name}: blacklisted_models does not state that an empty list blocks no models"
            )
        if "may not mix" not in denylist:
            problems.append(
                f"{name}: blacklisted_models does not state that `*` may not be mixed with named entries"
            )

    assert not problems, "model access documentation drift:\n    " + "\n    ".join(problems)


# Every model-pattern declaration across the management schemas, as (file, schema, field).
# The published API applies one pattern contract; a reader landing on any one of these
# should not have to find another to learn the rest of it.
PATTERN_DECLARATIONS = (
    ("governance.yaml", "VirtualKeyProviderConfig", "allowed_models_patterns"),
    ("governance.yaml", "VirtualKeyProviderConfig", "blacklisted_models_patterns"),
    ("governance.yaml", "CreateVirtualKeyRequest", "allowed_models_patterns"),
    ("governance.yaml", "CreateVirtualKeyRequest", "blacklisted_models_patterns"),
    ("governance.yaml", "UpdateVirtualKeyRequest", "allowed_models_patterns"),
    ("governance.yaml", "UpdateVirtualKeyRequest", "blacklisted_models_patterns"),
    ("projects.yaml", "ProjectProviderConfig", "allowed_models_patterns"),
    ("projects.yaml", "ProjectProviderConfig", "blacklisted_models_patterns"),
    ("providers.yaml", "Key", "models_patterns"),
    ("providers.yaml", "Key", "blacklisted_models_patterns"),
)


def pattern_property(source, schema_name, field, components=None):
    """Return one pattern declaration, reaching through a provider_configs array when present.

    `components` resolves a `$ref`: the bundler replaces an inline provider-config object
    with a pointer at its registered component, so the published spec needs one more hop
    where the modular source has none.
    """

    def deref(node):
        ref = node.get("$ref") if isinstance(node, dict) else None
        if not ref or components is None:
            return node
        return components.get(ref.rsplit("/", 1)[-1], {})

    schema = deref(source.get(schema_name) or {}).get("properties") or {}
    if field not in schema and "provider_configs" in schema:
        items = deref((schema["provider_configs"] or {}).get("items") or {})
        schema = items.get("properties") or {}
    return schema.get(field)


def test_model_patterns_declare_the_whole_contract():
    """Every model-pattern field documents the same rules and constrains its own items.

    ModelPatternList.Validate rejects blank, duplicate, `*`, and un-compilable patterns at
    save time, and matching is a case-insensitive full match against both the bare model
    name and `provider/model`. A declaration that states only part of that leaves a client
    free to send values the server refuses, or to misread how far a block pattern reaches.
    """
    sources = {}
    problems = []

    required_phrases = (
        ("case-insensitively as a full match", "how patterns are matched"),
        ("`provider/model`", "that the qualified name is matched too"),
        ("`*` is not a valid pattern", "that `*` is not a pattern"),
        ("refused", "that invalid entries are refused at save time"),
    )

    for filename, schema_name, field in PATTERN_DECLARATIONS:
        where = f"{filename} {schema_name}.{field}"
        source = sources.setdefault(
            filename, load(HERE / "schemas" / "management" / filename)
        )
        declaration = pattern_property(source, schema_name, field)
        if declaration is None:
            problems.append(f"{where}: not declared")
            continue

        description = " ".join((declaration.get("description") or "").split())
        for phrase, what in required_phrases:
            if phrase not in description:
                problems.append(f"{where}: description does not say {what}")
        if field.startswith("blacklisted") and "win over" not in description:
            problems.append(f"{where}: description does not say block patterns win")

        # config.schema.json rejects these values outright. The published spec describes the
        # same API, so it carries the same constraints rather than only prose about them.
        if declaration.get("uniqueItems") is not True:
            problems.append(f"{where}: array does not set uniqueItems")
        items = declaration.get("items") or {}
        if items.get("minLength") != 1:
            problems.append(f"{where}: items do not set minLength 1")
        if items.get("not") != {"const": "*"}:
            problems.append(f"{where}: items do not exclude the `*` wildcard")

    assert not problems, "model pattern contract drift:\n    " + "\n    ".join(problems)


def test_published_bundle_is_current():
    """openapi.json is what bundle.py produces from the sources as they stand.

    The bundle is a tracked artifact, and every other invariant here reads the modular
    sources, which is exactly how a stale bundle survives review: a schema edit lands, the
    rebuild does not, and the published spec keeps describing the previous contract.
    bundle.py is deterministic, so regenerating into a temp file and comparing is an exact
    check rather than a sampled one.
    """
    import subprocess
    import tempfile

    with tempfile.TemporaryDirectory() as tmp:
        rebuilt = Path(tmp) / "openapi.json"
        result = subprocess.run(
            [sys.executable, "bundle.py", "--output", str(rebuilt)],
            cwd=HERE,
            capture_output=True,
            text=True,
        )
        assert result.returncode == 0, f"bundle.py failed:\n{result.stderr}"
        want = rebuilt.read_text(encoding="utf-8")

    got = (HERE / "openapi.json").read_text(encoding="utf-8")
    assert want == got, (
        "openapi.json does not match the modular sources, run `python3 bundle.py`"
    )


check("no path key has a null Path Item", test_no_null_path_items)
check("no two paths collide after parameter normalization", test_no_duplicate_path_templates)
check("every fragment openapi.yaml mounts exists", test_every_mounted_fragment_exists)
check("no fragment is defined but never used", test_no_orphaned_fragments)
check("no operationId is claimed by two mounted operations", test_duplicate_operation_ids)
check("legacy aliases are mounted and their successors documented", test_legacy_aliases_mount_legacy_fragments)
check("vk_rotation_cooldown bounds match config.schema.json", test_vk_rotation_cooldown_bounds_match_config_schema)
check("bulk rotate ids schema rejects empty arrays", test_bulk_rotate_ids_requires_min_items)
check("virtual key request contract uses budgets and provider-scoped key_ids", test_virtual_key_request_contract_is_current)
check("every operation declares its own security", test_every_operation_declares_security)

check("model access fields are documented consistently across virtual key schemas", test_model_access_fields_are_documented_consistently)

check("model pattern declarations state the whole contract", test_model_patterns_declare_the_whole_contract)
check("published bundle is regenerated from the current sources", test_published_bundle_is_current)

print(f"\n{passed} passed, {failed} failed")
sys.exit(0 if failed == 0 else 1)
