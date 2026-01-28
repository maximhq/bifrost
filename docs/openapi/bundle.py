#!/usr/bin/env python3
"""
OpenAPI Bundle Script

This script bundles multiple OpenAPI YAML files with $ref references into a single
resolved OpenAPI specification file (JSON or YAML).

Usage:
    python bundle.py                    # Output to openapi.json
    python bundle.py --output spec.json # Output to custom file
    python bundle.py --format yaml      # Output as YAML
    python bundle.py --validate         # Validate the bundled spec

Requirements:
    pip install pyyaml

Optional (for validation):
    pip install openapi-spec-validator
"""

import argparse
import json
import os
import re
import sys
import warnings
from pathlib import Path
from typing import Any, Dict, Set, Tuple, Optional
from urllib.parse import urldefrag
import copy

try:
    import yaml
except ImportError:
    print("Error: PyYAML is required. Install with: pip install pyyaml")
    sys.exit(1)


class OpenAPIBundler:
    """Bundles OpenAPI specs with $ref resolution."""

    def __init__(self, base_path: Path):
        self.base_path = base_path
        self.cache: Dict[str, Any] = {}
        self.resolved_refs: Dict[str, Any] = {}  # Cache resolved content
        self.circular_schemas: Dict[str, Any] = {}  # Schemas with circular refs
        self.in_progress: Set[str] = set()  # Track refs currently being resolved

    def load_yaml(self, file_path: Path) -> Dict[str, Any]:
        """Load a YAML file and cache it."""
        abs_path = str(file_path.resolve())
        if abs_path in self.cache:
            return self.cache[abs_path]

        with open(file_path, "r", encoding="utf-8") as f:
            content = yaml.safe_load(f)
            self.cache[abs_path] = content
            return content

    def resolve_ref(self, ref: str, current_file: Path) -> Tuple[Any, Path, str]:
        """Resolve a $ref pointer to its actual content.

        Returns: (content, target_file, schema_name)
        """
        # Split the reference into file path and JSON pointer
        file_part, fragment = urldefrag(ref)

        # Determine the target file
        if file_part:
            # Relative path reference
            target_file = (current_file.parent / file_part).resolve()
        else:
            # Same file reference
            target_file = current_file

        # Check if target file exists
        if not target_file.exists():
            raise FileNotFoundError(
                f"Referenced file not found: {target_file}\n"
                f"  Referenced from: {current_file}\n"
                f"  $ref: {ref}"
            )

        # Load the target file
        target_content = self.load_yaml(target_file)

        # Extract schema name from fragment
        schema_name = ""

        # Navigate to the fragment if present
        if fragment:
            # Remove leading '#/' or '#' and split by '/'
            pointer = fragment.lstrip("#/")
            if pointer:
                path_parts = []
                for part in pointer.split("/"):
                    # Handle URL-encoded characters
                    part = part.replace("~1", "/").replace("~0", "~")
                    path_parts.append(part)
                    if isinstance(target_content, dict):
                        if part not in target_content:
                            raise KeyError(
                                f"Invalid $ref key '{part}' not found in object\n"
                                f"  Full pointer: {fragment}\n"
                                f"  Available keys: {list(target_content.keys())}\n"
                                f"  Target file: {target_file}\n"
                                f"  Referenced from: {current_file}\n"
                                f"  $ref: {ref}"
                            )
                        target_content = target_content[part]
                    elif isinstance(target_content, list):
                        try:
                            target_content = target_content[int(part)]
                        except (ValueError, IndexError) as e:
                            raise KeyError(
                                f"Invalid array index '{part}' in $ref\n"
                                f"  Full pointer: {fragment}\n"
                                f"  Target file: {target_file}\n"
                                f"  Referenced from: {current_file}\n"
                                f"  $ref: {ref}\n"
                                f"  Error: {e}"
                            )
                    else:
                        raise KeyError(
                            f"Cannot navigate into non-object/non-array at '{part}'\n"
                            f"  Full pointer: {fragment}\n"
                            f"  Target file: {target_file}\n"
                            f"  Referenced from: {current_file}\n"
                            f"  $ref: {ref}"
                        )
                # Use the last part as schema name
                schema_name = path_parts[-1] if path_parts else ""

        return target_content, target_file, schema_name

    def _make_unique_schema_name(self, base_name: str, target_file: Path) -> str:
        """Generate a unique schema name based on file path context."""
        if base_name in self.circular_schemas:
            # Add file context to make it unique
            file_stem = target_file.stem
            parent_name = target_file.parent.name
            unique_name = f"{parent_name}_{file_stem}_{base_name}"
            return unique_name
        return base_name

    def resolve_refs_recursive(
        self, obj: Any, current_file: Path, depth: int = 0
    ) -> Any:
        """Recursively resolve all $ref pointers in an object."""
        if depth > 100:
            raise RecursionError("Maximum recursion depth exceeded while resolving refs")

        if isinstance(obj, dict):
            # Check if this object contains a $ref
            if "$ref" in obj:
                ref = obj["$ref"]
                ref_key = f"{current_file}:{ref}"

                # Check for circular reference
                if ref_key in self.in_progress:
                    # Circular reference detected
                    # Resolve to get schema name, but don't recurse
                    try:
                        resolved, target_file, schema_name = self.resolve_ref(ref, current_file)
                        if schema_name and isinstance(resolved, dict):
                            # Store the schema for later addition to components
                            if schema_name not in self.circular_schemas:
                                self.circular_schemas[schema_name] = {
                                    'content': resolved,
                                    'target_file': target_file,
                                    'original_ref': ref
                                }
                            # Return a reference to the component schema
                            component_ref = {"$ref": f"#/components/schemas/{schema_name}"}
                            # Merge with siblings if any
                            if len(obj) > 1:
                                siblings = {k: v for k, v in obj.items() if k != "$ref"}
                                resolved_siblings = {
                                    k: self.resolve_refs_recursive(v, current_file, depth + 1)
                                    for k, v in siblings.items()
                                }
                                component_ref.update(resolved_siblings)
                            return component_ref
                    except (FileNotFoundError, KeyError) as e:
                        warnings.warn(
                            f"Failed to resolve circular reference '{ref}' from {current_file}: {e}"
                        )
                    return obj

                # Return cached resolved content if already processed
                if ref_key in self.resolved_refs:
                    cached = self.resolved_refs[ref_key]
                    if cached is not None:
                        # If there are siblings, merge them
                        if len(obj) > 1:
                            siblings = {k: v for k, v in obj.items() if k != "$ref"}
                            resolved_siblings = {
                                k: self.resolve_refs_recursive(v, current_file, depth + 1)
                                for k, v in siblings.items()
                            }
                            if isinstance(cached, dict):
                                result = dict(cached)
                                result.update(resolved_siblings)
                                return result
                        return cached

                # Mark as in progress
                self.in_progress.add(ref_key)

                try:
                    # Resolve the reference (will raise on missing refs)
                    resolved, target_file, schema_name = self.resolve_ref(ref, current_file)

                    # Recursively resolve any nested refs
                    resolved_content = self.resolve_refs_recursive(resolved, target_file, depth + 1)

                    # Cache the resolved content
                    self.resolved_refs[ref_key] = resolved_content

                    # If there are sibling properties alongside $ref, merge them
                    if len(obj) > 1:
                        # Get sibling properties (everything except $ref)
                        siblings = {k: v for k, v in obj.items() if k != "$ref"}
                        # Recursively resolve siblings
                        resolved_siblings = {
                            k: self.resolve_refs_recursive(v, current_file, depth + 1)
                            for k, v in siblings.items()
                        }
                        # Merge resolved content with siblings (siblings override)
                        if isinstance(resolved_content, dict):
                            result = dict(resolved_content)
                            result.update(resolved_siblings)
                            return result
                        else:
                            # If resolved content is not a dict, can't merge
                            return resolved_content

                    return resolved_content
                finally:
                    # Remove from in progress
                    self.in_progress.discard(ref_key)

            # Process all keys in the dict
            return {
                k: self.resolve_refs_recursive(v, current_file, depth + 1)
                for k, v in obj.items()
            }

        elif isinstance(obj, list):
            return [
                self.resolve_refs_recursive(item, current_file, depth + 1)
                for item in obj
            ]

        return obj

    def _resolve_circular_schemas(self, spec: Dict[str, Any], entry_path: Path) -> None:
        """Resolve circular schemas and add them to components/schemas."""
        if not self.circular_schemas:
            return

        # Ensure components/schemas exists
        if "components" not in spec:
            spec["components"] = {}
        if "schemas" not in spec["components"]:
            spec["components"]["schemas"] = {}

        # Process circular schemas
        for schema_name, schema_info in self.circular_schemas.items():
            content = schema_info['content']
            target_file = schema_info['target_file']

            # Deep copy to avoid mutation issues
            schema_content = copy.deepcopy(content)

            # Resolve refs within the schema (with circular ref handling)
            resolved_schema = self._resolve_schema_refs(schema_content, target_file, schema_name)

            spec["components"]["schemas"][schema_name] = resolved_schema

    def _resolve_schema_refs(self, obj: Any, current_file: Path, parent_schema: str, depth: int = 0) -> Any:
        """Resolve refs within a schema, converting local refs to component refs."""
        if depth > 50:
            return obj

        if isinstance(obj, dict):
            if "$ref" in obj:
                ref = obj["$ref"]

                # Check if this is a local ref that should point to a component
                if ref.startswith("#/"):
                    schema_name = ref.lstrip("#/")
                    if schema_name in self.circular_schemas or schema_name == parent_schema:
                        # Convert to component ref
                        result = {"$ref": f"#/components/schemas/{schema_name}"}
                        if len(obj) > 1:
                            siblings = {k: v for k, v in obj.items() if k != "$ref"}
                            result.update(siblings)
                        return result

                # Try to resolve normally
                try:
                    resolved, target_file, schema_name = self.resolve_ref(ref, current_file)

                    # Check if this schema is circular
                    if schema_name and schema_name in self.circular_schemas:
                        result = {"$ref": f"#/components/schemas/{schema_name}"}
                        if len(obj) > 1:
                            siblings = {k: v for k, v in obj.items() if k != "$ref"}
                            result.update(siblings)
                        return result

                    # Resolve recursively
                    resolved_content = self._resolve_schema_refs(resolved, target_file, parent_schema, depth + 1)

                    if len(obj) > 1:
                        siblings = {k: v for k, v in obj.items() if k != "$ref"}
                        if isinstance(resolved_content, dict):
                            result = dict(resolved_content)
                            result.update(siblings)
                            return result

                    return resolved_content
                except (FileNotFoundError, KeyError) as e:
                    warnings.warn(
                        f"Failed to resolve schema reference '{ref}' from {current_file}: {e}"
                    )
                    return obj

            return {
                k: self._resolve_schema_refs(v, current_file, parent_schema, depth + 1)
                for k, v in obj.items()
            }

        elif isinstance(obj, list):
            return [
                self._resolve_schema_refs(item, current_file, parent_schema, depth + 1)
                for item in obj
            ]

        return obj

    def bundle(self, entry_file: str = "openapi.yaml") -> Dict[str, Any]:
        """Bundle the OpenAPI spec starting from the entry file."""
        entry_path = self.base_path / entry_file
        if not entry_path.exists():
            raise FileNotFoundError(f"Entry file not found: {entry_path}")

        spec = self.load_yaml(entry_path)
        resolved_spec = self.resolve_refs_recursive(spec, entry_path)

        # Add circular schemas to components
        self._resolve_circular_schemas(resolved_spec, entry_path)

        return resolved_spec


def validate_spec(spec: Dict[str, Any]) -> bool:
    """Validate the OpenAPI spec (requires openapi-spec-validator)."""
    try:
        from openapi_spec_validator import validate
        from openapi_spec_validator.readers import read_from_filename

        validate(spec)
        print("✓ OpenAPI specification is valid!")
        return True
    except ImportError:
        print(
            "Warning: openapi-spec-validator not installed. "
            "Install with: pip install openapi-spec-validator"
        )
        return True
    except Exception as e:
        print(f"✗ Validation error: {e}")
        return False


def main():
    parser = argparse.ArgumentParser(
        description="Bundle OpenAPI YAML files into a single specification"
    )
    parser.add_argument(
        "--input",
        "-i",
        default="openapi.yaml",
        help="Entry point YAML file (default: openapi.yaml)",
    )
    parser.add_argument(
        "--output",
        "-o",
        default="openapi.json",
        help="Output file path (default: openapi.json)",
    )
    parser.add_argument(
        "--format",
        "-f",
        choices=["json", "yaml"],
        default="json",
        help="Output format (default: json)",
    )
    parser.add_argument(
        "--validate",
        "-v",
        action="store_true",
        help="Validate the bundled specification",
    )
    parser.add_argument(
        "--indent",
        type=int,
        default=2,
        help="Indentation level for output (default: 2)",
    )
    parser.add_argument(
        "--inline",
        action="store_true",
        help="Replace the input file with resolved specification (for Mintlify compatibility)",
    )

    args = parser.parse_args()

    # Determine the base path (directory of this script)
    base_path = Path(__file__).parent.resolve()

    print(f"Bundling OpenAPI spec from: {base_path / args.input}")

    try:
        bundler = OpenAPIBundler(base_path)
        spec = bundler.bundle(args.input)

        # Validate if requested
        if args.validate:
            if not validate_spec(spec):
                sys.exit(1)

        # Handle inline replacement for Mintlify compatibility
        if args.inline:
            input_path = base_path / args.input
            # When inlining, update the original YAML file
            with open(input_path, "w", encoding="utf-8") as f:
                yaml.dump(
                    spec,
                    f,
                    default_flow_style=False,
                    allow_unicode=True,
                    sort_keys=False,
                )
            print(f"✓ Updated original file with resolved references: {input_path}")

            # Also create the JSON output for reference
            output_path = base_path / args.output
            with open(output_path, "w", encoding="utf-8") as f:
                json.dump(spec, f, indent=args.indent, ensure_ascii=False)
            print(f"✓ JSON bundled specification written to: {output_path}")
        else:
            # Write output
            output_path = base_path / args.output
            with open(output_path, "w", encoding="utf-8") as f:
                if args.format == "json":
                    json.dump(spec, f, indent=args.indent, ensure_ascii=False)
                else:
                    yaml.dump(
                        spec,
                        f,
                        default_flow_style=False,
                        allow_unicode=True,
                        sort_keys=False,
                    )

            print(f"✓ Bundled specification written to: {output_path}")

        # Print some stats
        paths_count = len(spec.get("paths", {}))
        schemas_count = len(spec.get("components", {}).get("schemas", {}))
        print(f"  - Paths: {paths_count}")
        print(f"  - Schemas: {schemas_count}")

        if bundler.circular_schemas:
            print(f"  - Circular schemas resolved: {len(bundler.circular_schemas)}")
            for name in bundler.circular_schemas:
                print(f"      • {name}")

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
