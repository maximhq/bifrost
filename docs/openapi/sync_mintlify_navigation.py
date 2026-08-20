#!/usr/bin/env python3
"""Keep Mintlify API navigation complete, nested, and duplicate-free."""

from __future__ import annotations

import argparse
import copy
import json
import re
import sys
from collections import OrderedDict
from pathlib import Path
from typing import Any


HTTP_METHODS = ("get", "post", "put", "patch", "delete", "options", "head", "trace")
ENDPOINT_PAGE = re.compile(
    r"^(GET|POST|PUT|PATCH|DELETE|OPTIONS|HEAD|TRACE) (/.+)$"
)
OPENAPI_SOURCE = "openapi/openapi.json"

# Mintlify always expands top-level groups; only nested groups honour `expanded`.
# So every tag group is nested one level under a section to make it collapsible.
DEPRECATED_SECTION = "Deprecated"
GOVERNANCE_SECTION = "Governance"
SECTIONS: tuple[tuple[str, tuple[str, ...]], ...] = (
    (
        "Inference",
        (
            "Models",
            "Chat Completions",
            "Text Completions",
            "Responses",
            "OCR",
            "Rerank",
            "Embeddings",
            "Audio",
            "Images",
            "Videos",
            "Count Tokens",
            "Compaction",
            "Batch",
            "Files",
            "Containers",
            "Async Jobs",
            "Realtime",
        ),
    ),
    (
        "Integrations",
        (
            "OpenAI Integration",
            "Anthropic Integration",
            "GenAI Integration",
            "Bedrock Integration",
            "Cohere Integration",
            "Cursor Integration",
            "LiteLLM Integration",
            "LangChain Integration",
            "PydanticAI Integration",
        ),
    ),
    (
        "Platform",
        (
            "Health",
            "Configuration",
            "Session",
            "Providers",
            "Plugins",
            "MCP",
            "MCP Tool Groups",
            "OAuth",
            "Circuit Breaker",
            "Logging",
            "Prompt Repository",
            "Skills",
            "Cache",
            "Vault",
            "Audit Logs",
            "Infrastructure",
            "Webhooks",
        ),
    ),
)


def iter_page_entries(node: Any):
    if isinstance(node, str):
        if ENDPOINT_PAGE.match(node):
            yield node
        return
    if isinstance(node, list):
        for item in node:
            yield from iter_page_entries(item)
        return
    if isinstance(node, dict):
        yield from iter_page_entries(node.get("pages", []))


def strip_group_icons(node: Any) -> None:
    if not isinstance(node, dict):
        return
    node.pop("icon", None)
    for page in node.get("pages", []):
        strip_group_icons(page)


def find_api_tab(docs: dict[str, Any]) -> dict[str, Any]:
    tabs = docs.get("navigation", {}).get("tabs", [])
    matches = [tab for tab in tabs if tab.get("tab") == "API"]
    if len(matches) != 1:
        raise ValueError(f"expected exactly one API tab, found {len(matches)}")
    return matches[0]


def find_group(groups: list[dict[str, Any]], name: str) -> dict[str, Any]:
    matches = [group for group in groups if group.get("group") == name]
    if len(matches) != 1:
        raise ValueError(f"expected exactly one {name!r} group, found {len(matches)}")
    return matches[0]


def slugify(value: str) -> str:
    return re.sub(r"(^-|-$)", "", re.sub(r"[^a-z0-9]+", "-", value.lower()))


def openapi_operations(spec: dict[str, Any]) -> list[tuple[str, str, str, bool]]:
    operations: list[tuple[str, str, str, bool]] = []
    for path, path_item in spec.get("paths", {}).items():
        if not isinstance(path_item, dict):
            continue
        for method in HTTP_METHODS:
            operation = path_item.get(method)
            if not isinstance(operation, dict):
                continue
            tags = operation.get("tags") or ["Other"]
            summary = str(operation.get("summary") or f"{method.upper()} {path}")
            is_deprecated = bool(operation.get("x-bifrost-successor"))
            operations.append(
                (str(tags[0]), f"{method.upper()} {path}", summary, is_deprecated)
            )
    return operations


def collapsed(group: str, pages: list[Any]) -> dict[str, Any]:
    """A nested group that starts closed in the sidebar."""
    return {"group": group, "expanded": False, "pages": pages}


def build_navigation(
    docs: dict[str, Any], spec: dict[str, Any]
) -> tuple[dict[str, Any], int]:
    updated = copy.deepcopy(docs)
    api_tab = find_api_tab(updated)
    existing_groups = api_tab.get("groups", [])

    authentication = find_group(existing_groups, "Authentication")
    governance = find_group(existing_groups, "Governance")
    governance.pop("openapi", None)
    strip_group_icons(governance)
    governance["pages"] = [
        page
        for page in governance.get("pages", [])
        if not (
            isinstance(page, dict)
            and page.get("group") in ("Deprecated APIs", "Deprecated paths")
        )
    ]

    governance_pages = list(iter_page_entries(governance))
    governance_set = set(governance_pages)
    if len(governance_pages) != len(governance_set):
        raise ValueError("Governance navigation contains duplicate endpoint entries")

    operations = openapi_operations(spec)
    operation_pages = [page for _, page, _, _ in operations]
    operation_set = set(operation_pages)
    if len(operation_pages) != len(operation_set):
        raise ValueError("OpenAPI contains duplicate method/path operations")

    unknown_governance_pages = sorted(governance_set - operation_set)
    if unknown_governance_pages:
        raise ValueError(
            "Governance navigation references missing OpenAPI operations: "
            + ", ".join(unknown_governance_pages)
        )

    governed_openapi_pages = {
        page
        for _, page, _, _ in operations
        if page.split(" ", 1)[1].startswith("/api/governance/")
    }
    missing_governance_pages = sorted(governed_openapi_pages - governance_set)
    if missing_governance_pages:
        raise ValueError(
            "Governance navigation is missing governed OpenAPI operations: "
            + ", ".join(missing_governance_pages)
        )

    generated_page_routes: dict[str, str] = {}
    for tag, page, summary, _ in operations:
        generated_route = f"{slugify(tag)}/{slugify(summary)}"
        colliding_page = generated_page_routes.get(generated_route)
        if colliding_page is not None:
            raise ValueError(
                "Visible OpenAPI operations generate the same Mintlify page route: "
                f"{colliding_page}, {page}"
            )
        generated_page_routes[generated_route] = page

    # Deprecated aliases are grouped on their own rather than sitting under the tag
    # they inherit from their successor, where they would shadow the curated
    # Governance subgroup names at the top level.
    pages_by_tag: OrderedDict[str, list[str]] = OrderedDict()
    deprecated_by_tag: OrderedDict[str, list[str]] = OrderedDict()
    for tag, page, _, is_deprecated in operations:
        if page in governance_set:
            continue
        target = deprecated_by_tag if is_deprecated else pages_by_tag
        target.setdefault(tag, []).append(page)

    placed_tags = {tag for _, tags in SECTIONS for tag in tags}
    unplaced_tags = sorted(set(pages_by_tag) - placed_tags)
    if unplaced_tags:
        raise ValueError(
            "OpenAPI tags are not assigned to a navigation section: "
            + ", ".join(unplaced_tags)
            + " (add them to SECTIONS)"
        )

    sections = [
        {
            "group": section,
            "pages": [
                collapsed(tag, pages_by_tag[tag]) for tag in tags if tag in pages_by_tag
            ],
        }
        for section, tags in SECTIONS
    ]

    # The curated Governance hierarchy becomes the section's own children.
    sections.append(
        {
            "group": GOVERNANCE_SECTION,
            "pages": [
                collapsed(page["group"], page["pages"])
                if isinstance(page, dict) and "group" in page
                else page
                for page in governance.get("pages", [])
            ],
        }
    )

    if deprecated_by_tag:
        sections.append(
            {
                "group": DEPRECATED_SECTION,
                "pages": [
                    collapsed(tag, pages) for tag, pages in deprecated_by_tag.items()
                ],
            }
        )

    api_tab["openapi"] = OPENAPI_SOURCE
    api_tab["groups"] = [authentication, *sections]

    final_pages = list(iter_page_entries(api_tab.get("groups", [])))
    final_set = set(final_pages)
    visible_operation_set = {page for _, page, _, _ in operations}
    if len(final_pages) != len(final_set):
        raise ValueError("API navigation contains duplicate endpoint entries")
    if final_set != visible_operation_set:
        missing = sorted(visible_operation_set - final_set)
        extra = sorted(final_set - visible_operation_set)
        raise ValueError(
            f"API navigation does not match OpenAPI; missing={missing}, extra={extra}"
        )

    return updated, len(visible_operation_set)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--check",
        action="store_true",
        help="fail if docs.json is not synchronized instead of updating it",
    )
    args = parser.parse_args()

    script_dir = Path(__file__).resolve().parent
    docs_path = script_dir.parent / "docs.json"
    spec_path = script_dir / "openapi.json"

    docs = json.loads(docs_path.read_text(encoding="utf-8"))
    spec = json.loads(spec_path.read_text(encoding="utf-8"))
    updated, visible_operation_count = build_navigation(docs, spec)

    rendered = json.dumps(updated, indent=2) + "\n"
    current = docs_path.read_text(encoding="utf-8")
    if args.check:
        if current != rendered:
            print(
                "docs.json API navigation is stale; run "
                "python3 docs/openapi/sync_mintlify_navigation.py",
                file=sys.stderr,
            )
            return 1
        print(
            f"Mintlify API navigation is synchronized: "
            f"{visible_operation_count} visible operations, no duplicates"
        )
        return 0

    docs_path.write_text(rendered, encoding="utf-8")
    print(
        f"Updated Mintlify API navigation: "
        f"{visible_operation_count} visible operations, no duplicates"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
