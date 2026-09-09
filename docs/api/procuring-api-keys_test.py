#!/usr/bin/env python3
"""Checks the endpoint table in procuring-api-keys.mdx against the OpenAPI spec.

Run directly: `python3 procuring-api-keys_test.py`. No test framework needed.

The table is hand-maintained prose that enumerates the management surface, so it drifts
silently whenever a route moves. Two things must hold: every path it lists is a real
documented path, and for each governance area it shows BOTH the canonical
`/api/governance/...` route and the legacy alias that still answers, so a reader on either
generation of the API finds what they are calling.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

import yaml

HERE = Path(__file__).resolve().parent
DOC = HERE / "procuring-api-keys.mdx"
SPEC = HERE.parent / "openapi" / "openapi.yaml"

spec_paths = set((yaml.safe_load(SPEC.read_text(encoding="utf-8")).get("paths") or {}))

# Every `/api/...` in a backtick span inside the table, keyed by the Area cell.
rows: dict[str, set[str]] = {}
for line in DOC.read_text(encoding="utf-8").splitlines():
    if not line.startswith("|") or line.startswith("| ---"):
        continue
    cells = [cell.strip() for cell in line.strip("|").split("|")]
    if len(cells) < 2 or cells[0] in ("Area",):
        continue
    found = set(re.findall(r"`(/api/[^`]+)`", cells[1]))
    if found:
        rows.setdefault(cells[0], set()).update(found)

listed = {path for paths in rows.values() for path in paths}

# Governance areas that were renamed under /api/governance. Each legacy alias must appear
# in the table alongside its canonical successor.
LEGACY_TO_CANONICAL = {
    "/api/roles": "/api/governance/rbac/roles",
    "/api/roles/{role_id}": "/api/governance/rbac/roles/{role_id}",
    "/api/roles/{role_id}/permissions": "/api/governance/rbac/roles/{role_id}/permissions",
    "/api/resources": "/api/governance/rbac/resources",
    "/api/operations": "/api/governance/rbac/operations",
    "/api/permissions": "/api/governance/rbac/permissions",
    "/api/users": "/api/governance/users",
    "/api/users/{user_id}": "/api/governance/users/{user_id}",
    "/api/users/me/permissions": "/api/governance/users/me/permissions",
    "/api/users/{user_id}/role": "/api/governance/users/{user_id}/role",
    "/api/users/{user_id}/teams": "/api/governance/users/{user_id}/teams",
    "/api/users/email/{email}/virtual-keys": "/api/governance/users/email/{email}/virtual-keys",
    "/api/teams": "/api/governance/teams",
    "/api/teams/{team_id}": "/api/governance/teams/{team_id}",
    "/api/teams/{team_id}/members": "/api/governance/teams/{team_id}/members",
    "/api/teams/{team_id}/members/{user_id}": "/api/governance/teams/{team_id}/members/{user_id}",
    "/api/access-profiles": "/api/governance/access-profiles",
    "/api/access-profiles/{profile_id}": "/api/governance/access-profiles/{profile_id}",
    "/api/users/{user_id}/access-profiles": "/api/governance/users/{user_id}/access-profiles",
    "/api/audit-logs": "/api/governance/audit-logs",
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


def test_table_is_not_empty():
    assert len(listed) > 100, f"only parsed {len(listed)} endpoints from the table"


def test_every_listed_path_is_documented():
    unknown = sorted(path for path in listed if path not in spec_paths)
    assert not unknown, f"table lists paths absent from openapi.yaml: {unknown}"


def test_canonical_governance_routes_are_listed():
    missing = sorted(
        canonical
        for canonical in set(LEGACY_TO_CANONICAL.values())
        if canonical not in listed
    )
    assert not missing, f"canonical governance routes missing from the table: {missing}"


def test_legacy_aliases_are_still_listed():
    missing = sorted(legacy for legacy in LEGACY_TO_CANONICAL if legacy not in listed)
    assert not missing, f"legacy aliases dropped from the table: {missing}"


def test_legacy_rows_are_labelled_as_deprecated():
    """A reader must be able to tell which of the two generations they are looking at."""
    unlabelled = set()
    for area, paths in rows.items():
        legacy_here = paths & set(LEGACY_TO_CANONICAL)
        if legacy_here and "deprecated" not in area.lower():
            unlabelled.add(area)
    assert not unlabelled, (
        "areas list legacy aliases without saying so in the Area cell: "
        f"{sorted(unlabelled)}"
    )


check("the endpoint table parses", test_table_is_not_empty)
check("every path in the table exists in openapi.yaml", test_every_listed_path_is_documented)
check("canonical /api/governance routes are listed", test_canonical_governance_routes_are_listed)
check("legacy aliases are still listed", test_legacy_aliases_are_still_listed)
check("legacy aliases are labelled as deprecated", test_legacy_rows_are_labelled_as_deprecated)

print(f"\n{passed} passed, {failed} failed")
sys.exit(0 if failed == 0 else 1)
