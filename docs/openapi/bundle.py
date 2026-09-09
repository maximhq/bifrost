#!/usr/bin/env python3
"""
OpenAPI Bundle Script

Bundles multiple OpenAPI YAML files with $ref references into a single
OpenAPI specification file using proper component references instead of
full inlining.

The bundler uses openapi.yaml#/components/* as a registry. All $refs that
resolve to a registered component are replaced with #/components/{type}/{Name}
pointers. Only genuinely unregistered sub-schemas are inlined.

This is fully generic - adding new component types (securitySchemes, headers,
requestBodies, links, callbacks, etc.) to openapi.yaml requires no changes here.

Usage:
    python bundle.py                    # Output to openapi.json
    python bundle.py --output spec.json # Output to custom file
    python bundle.py --format yaml      # Output as YAML

Requirements:
    pip install pyyaml
"""

import argparse
import copy
import json
import os
import sys
import warnings
from pathlib import Path
from typing import Any, Dict, Optional, Set, Tuple
from urllib.parse import urldefrag

try:
    import yaml
except ImportError:
    print("Error: PyYAML is required. Install with: pip install pyyaml")
    sys.exit(1)


class OpenAPIBundler:
    """
    Generic OpenAPI bundler that hoists all registered components into
    #/components/{type}/{name} refs rather than fully inlining $refs.

    Algorithm:
      Phase 1 - Build registry: scan ALL openapi.yaml components/* sections and
                map (abs_file, frag_key) -> (component_type, canonical_name).
      Phase 2 - Resolve components: for each registered component, resolve its
                content, substituting known refs with #/components/{type}/{name}.
      Phase 3 - Resolve paths: resolve all path items the same way.
      Phase 4 - Assemble output: emit the full bundled spec.

    Adding a new component type (e.g. securitySchemes, headers, requestBodies)
    only requires registering it in openapi.yaml components section - no changes
    needed in this file.

    Circular reference handling:
      If a $ref points back to something currently being resolved AND that
      something is registered, the registry lookup intercepts it first and emits
      a clean #/components/{type}/{name} pointer (breaking the cycle). If it is
      NOT registered, a warning is emitted with instructions to register it.
    """

    def __init__(self, base_path: Path):
        self.base_path = base_path
        self.file_cache: Dict[str, Any] = {}
        # Registry: (abs_file_str, frag_key) -> (component_type, canonical_name)
        # e.g. ('/path/chat.yaml', 'ChatMessage') -> ('schemas', 'ChatMessage')
        self.registry: Dict[Tuple[str, str], Tuple[str, str]] = {}
        # Resolved components: {component_type: {name: resolved_content}}
        self.resolved_components: Dict[str, Dict[str, Any]] = {}
        # Set of (abs_file_str, frag_key) currently being resolved (circular detection)
        self.resolving: Set[Tuple[str, str]] = set()

    # -------------------------------------------------------------------------
    # File loading
    # -------------------------------------------------------------------------

    def _load(self, path: Path) -> Any:
        key = str(path.resolve())
        if key not in self.file_cache:
            if not path.exists():
                raise FileNotFoundError(f"File not found: {path}")
            with open(path, "r", encoding="utf-8") as f:
                self.file_cache[key] = yaml.safe_load(f)
        return self.file_cache[key]

    # -------------------------------------------------------------------------
    # Ref parsing helpers
    # -------------------------------------------------------------------------

    def _split_ref(self, ref: str, current_file: Path) -> Tuple[Path, str]:
        """
        Split a $ref into (absolute_file_path, normalized_fragment_key).

        fragment_key is the JSON Pointer fragment with the leading '#/' stripped,
        e.g. '#/ChatMessage' -> 'ChatMessage', 'file.yaml#/foo/bar' -> 'foo/bar'.
        """
        url, fragment = urldefrag(ref)
        abs_path = (current_file.parent / url).resolve() if url else current_file.resolve()
        return abs_path, fragment.lstrip("/")

    def _navigate(self, content: Any, frag_key: str) -> Any:
        """Navigate into content using a normalized fragment key."""
        if not frag_key:
            return content
        for part in frag_key.split("/"):
            part = part.replace("~1", "/").replace("~0", "~")
            if isinstance(content, dict):
                if part not in content:
                    raise KeyError(
                        f"Key '{part}' not found. Available: {list(content.keys())}"
                    )
                content = content[part]
            elif isinstance(content, list):
                content = content[int(part)]
            else:
                raise KeyError(f"Cannot navigate into {type(content).__name__} at '{part}'")
        return content

    # -------------------------------------------------------------------------
    # Phase 1: Build registry (generic over all component types)
    # -------------------------------------------------------------------------

    def _build_registry(self, entry_path: Path) -> None:
        """
        Scan openapi.yaml components/* and register every $ref entry as
        (abs_file, frag_key) -> (component_type, canonical_name).

        Works for any component type: schemas, responses, parameters,
        securitySchemes, headers, requestBodies, links, callbacks, etc.
        No changes needed here when new types are added to openapi.yaml.
        """
        spec = self._load(entry_path)
        for comp_type, section in spec.get("components", {}).items():
            if not isinstance(section, dict):
                continue
            for name, comp_def in section.items():
                if isinstance(comp_def, dict) and "$ref" in comp_def:
                    abs_file, frag_key = self._split_ref(comp_def["$ref"], entry_path)
                    self.registry[(str(abs_file), frag_key)] = (comp_type, name)

    # -------------------------------------------------------------------------
    # Core resolver
    # -------------------------------------------------------------------------

    def _resolve_value(self, obj: Any, current_file: Path) -> Any:
        """
        Recursively resolve all $refs in obj.

        - If a $ref already points to #/components/..., keep it as-is.
        - If a $ref resolves to a registered component, replace with
          #/components/{type}/{name}.
        - Otherwise, inline the referenced content (resolved recursively).
        - Circular refs to unregistered content emit a warning with fix instructions.
        """
        if isinstance(obj, dict):
            if "$ref" in obj:
                ref = obj["$ref"]

                # Already an internal component ref - keep it as-is
                if ref.startswith("#/components/"):
                    if len(obj) > 1:
                        result = {"$ref": ref}
                        for k, v in obj.items():
                            if k != "$ref":
                                result[k] = self._resolve_value(v, current_file)
                        return result
                    return obj

                abs_file, frag_key = self._split_ref(ref, current_file)

                # Check if this resolves to a registered component
                match = self.registry.get((str(abs_file), frag_key))
                if match is not None:
                    comp_type, name = match
                    result: Dict[str, Any] = {"$ref": f"#/components/{comp_type}/{name}"}
                    if len(obj) > 1:
                        for k, v in obj.items():
                            if k != "$ref":
                                result[k] = self._resolve_value(v, current_file)
                    return result

                # Detect circular reference - the target is currently being resolved
                # and is NOT in the registry (so the registry can't break the cycle).
                #
                # This happens when a schema file has an internal self-ref (e.g.
                # `$ref: '#/MySchema'`) but MySchema was never added to openapi.yaml.
                #
                # FIX: register the schema in openapi.yaml components/schemas:
                #
                #   MySchema:
                #     $ref: './schemas/path/to/file.yaml#/MySchema'
                #
                # Once registered, the registry check above intercepts the ref and
                # emits a clean #/components/schemas/MySchema pointer instead of
                # attempting to inline it (which would recurse forever).
                resolve_key = (str(abs_file), frag_key)
                if resolve_key in self.resolving:
                    warnings.warn(
                        f"Circular $ref not in registry, left unresolved: '{ref}' "
                        f"(from {current_file}). Register it in openapi.yaml components/."
                    )
                    return obj

                # Inline the referenced content
                try:
                    content = self._load(abs_file)
                    value = self._navigate(content, frag_key)
                except (FileNotFoundError, KeyError) as e:
                    warnings.warn(f"Cannot resolve $ref '{ref}' from {current_file}: {e}")
                    return obj

                self.resolving.add(resolve_key)
                try:
                    resolved = self._resolve_value(copy.deepcopy(value), abs_file)
                finally:
                    self.resolving.discard(resolve_key)

                # Merge any sibling keys alongside $ref
                if len(obj) > 1 and isinstance(resolved, dict):
                    result = dict(resolved)
                    for k, v in obj.items():
                        if k != "$ref":
                            result[k] = self._resolve_value(v, current_file)
                    return result

                return resolved

            return {k: self._resolve_value(v, current_file) for k, v in obj.items()}

        elif isinstance(obj, list):
            return [self._resolve_value(item, current_file) for item in obj]

        return obj

    # -------------------------------------------------------------------------
    # Phase 2: Resolve all registered components (generic)
    # -------------------------------------------------------------------------

    def _ensure_component(
        self, comp_type: str, name: str, ref_str: str, entry_path: Path
    ) -> None:
        """
        Resolve a registered component and store it in resolved_components.
        Idempotent; handles circular refs via the resolving set.
        """
        if name in self.resolved_components.get(comp_type, {}):
            return

        abs_file, frag_key = self._split_ref(ref_str, entry_path)
        resolve_key = (str(abs_file), frag_key)

        if resolve_key in self.resolving:
            return  # Circular - the registry will emit a component ref to break the cycle

        self.resolving.add(resolve_key)
        try:
            content = self._load(abs_file)
            value = self._navigate(content, frag_key)
            resolved = self._resolve_value(copy.deepcopy(value), abs_file)
        except (FileNotFoundError, KeyError) as e:
            warnings.warn(f"Cannot resolve {comp_type} '{name}' ({ref_str}): {e}")
            resolved = {"description": f"[unresolvable: {e}]"}
        finally:
            self.resolving.discard(resolve_key)

        self.resolved_components.setdefault(comp_type, {})[name] = resolved

    # -------------------------------------------------------------------------
    # Main bundle entry point
    # -------------------------------------------------------------------------

    def bundle(self, entry_file: str = "openapi.yaml") -> Dict[str, Any]:
        """Bundle the OpenAPI spec starting from the entry file."""
        entry_path = (self.base_path / entry_file).resolve()
        if not entry_path.exists():
            raise FileNotFoundError(f"Entry file not found: {entry_path}")

        # Phase 1: Build registry from all components/* sections
        self._build_registry(entry_path)

        spec = self._load(entry_path)
        components = spec.get("components", {})

        # Phase 2: Resolve every registered component generically
        for comp_type, section in components.items():
            if not isinstance(section, dict):
                continue
            for name, comp_def in section.items():
                if isinstance(comp_def, dict) and "$ref" in comp_def:
                    self._ensure_component(comp_type, name, comp_def["$ref"], entry_path)
                else:
                    self.resolved_components.setdefault(comp_type, {})[name] = (
                        self._resolve_value(copy.deepcopy(comp_def), entry_path)
                    )

        # Phase 3 + 4: Build output spec
        output: Dict[str, Any] = {}
        for key, value in spec.items():
            if key == "paths":
                output["paths"] = self._resolve_paths(value, entry_path)
            elif key == "components":
                output["components"] = self.resolved_components
            else:
                # info, servers, tags, security, etc. - resolve defensively
                output[key] = (
                    self._resolve_value(copy.deepcopy(value), entry_path)
                    if isinstance(value, (dict, list))
                    else value
                )

        annotate_deprecated_operations(output)

        return output

    def _resolve_paths(self, paths: Dict[str, Any], entry_path: Path) -> Dict[str, Any]:
        """Resolve all path items."""
        resolved: Dict[str, Any] = {}
        for path_name, path_ref in paths.items():
            if isinstance(path_ref, dict) and "$ref" in path_ref:
                abs_file, frag_key = self._split_ref(path_ref["$ref"], entry_path)
                try:
                    content = self._load(abs_file)
                    value = self._navigate(content, frag_key)
                    resolved[path_name] = self._resolve_value(
                        copy.deepcopy(value), abs_file
                    )
                except (FileNotFoundError, KeyError) as e:
                    warnings.warn(f"Cannot resolve path '{path_name}': {e}")
                    resolved[path_name] = path_ref
            else:
                resolved[path_name] = self._resolve_value(path_ref, entry_path)
        return resolved


# -----------------------------------------------------------------------------
# Deprecation annotations
# -----------------------------------------------------------------------------

HTTP_METHODS = ("get", "post", "put", "patch", "delete", "options", "head", "trace")

REMOVAL_PHRASES = {
    "following-major-release": "the following major release",
}

DEPRECATION_TAG = "Removing soon"
DEPRECATED_SUMMARY_SUFFIX = " (deprecated path)"


def annotate_deprecated_operations(spec: Dict[str, Any]) -> int:
    """
    Mark every operation carrying `x-bifrost-successor` as deprecating soon.

    Legacy aliases $ref their successor operation, so they inherit its summary
    verbatim. Mintlify derives a page route from tag + summary, meaning an alias
    and its successor would collide on the same route. Suffixing the summary both
    disambiguates the route and labels the endpoint in the navigation.

    Runs on the bundled output so the annotations stay in sync with the
    successor operations the aliases are defined against.
    """
    annotated = 0
    for path_item in spec.get("paths", {}).values():
        if not isinstance(path_item, dict):
            continue
        for method in HTTP_METHODS:
            operation = path_item.get(method)
            if not isinstance(operation, dict):
                continue
            successor = operation.get("x-bifrost-successor")
            if not successor:
                continue

            operation["deprecated"] = True

            summary = str(operation.get("summary") or "")
            if not summary.endswith(DEPRECATED_SUMMARY_SUFFIX):
                operation["summary"] = summary + DEPRECATED_SUMMARY_SUFFIX

            removal = REMOVAL_PHRASES.get(
                str(operation.get("x-bifrost-removal") or ""), "a future major release"
            )
            notice = (
                "<Warning>\n"
                f"  This path is deprecated and will be removed in {removal}.\n"
                f"  Use `{successor}` instead.\n"
                "</Warning>\n"
            )

            # x-mint is inherited from the successor - copy before mutating so the
            # successor's own metadata is left untouched.
            x_mint = copy.deepcopy(operation.get("x-mint") or {})
            metadata = dict(x_mint.get("metadata") or {})
            metadata["tag"] = DEPRECATION_TAG
            x_mint["metadata"] = metadata
            x_mint["content"] = notice + str(x_mint.get("content") or "")
            operation["x-mint"] = x_mint

            annotated += 1
    return annotated


# -----------------------------------------------------------------------------
# CLI
# -----------------------------------------------------------------------------


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Bundle OpenAPI YAML files into a single specification"
    )
    parser.add_argument(
        "--input", "-i", default="openapi.yaml",
        help="Entry point YAML file (default: openapi.yaml)",
    )
    parser.add_argument(
        "--output", "-o", default="openapi.json",
        help="Output file path (default: openapi.json)",
    )
    parser.add_argument(
        "--format", "-f", choices=["json", "yaml"], default="json",
        help="Output format (default: json)",
    )
    parser.add_argument(
        "--indent", type=int, default=2,
        help="Indentation level for output (default: 2)",
    )

    args = parser.parse_args()

    base_path = Path(__file__).parent.resolve()
    print(f"Bundling OpenAPI spec from: {base_path / args.input}")

    try:
        bundler = OpenAPIBundler(base_path)
        spec = bundler.bundle(args.input)

        output_path = base_path / args.output
        with open(output_path, "w", encoding="utf-8") as f:
            if args.format == "json":
                json.dump(spec, f, indent=args.indent, ensure_ascii=False)
            else:
                yaml.dump(spec, f, default_flow_style=False, allow_unicode=True, sort_keys=False)

        print(f"✓ Bundled specification written to: {output_path}")

        paths_count = len(spec.get("paths", {}))
        print(f"  - Paths: {paths_count}")
        for comp_type, section in spec.get("components", {}).items():
            print(f"  - {comp_type.capitalize()}: {len(section)}")
        size_kb = os.path.getsize(output_path) / 1024
        print(f"  - File size: {size_kb:.1f} KB")

    except FileNotFoundError as e:
        print(f"Error: {e}")
        sys.exit(1)
    except Exception as e:
        print(f"Error bundling spec: {e}")
        import traceback
        traceback.print_exc()
        sys.exit(1)


if __name__ == "__main__":
    main()
