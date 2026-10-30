#!/usr/bin/env python3
"""Check management route coverage against local OSS and Enterprise sources.

Usage: python3 cli/scripts/auditmanagement.py --oss-root .. --enterprise-root ../bifrost-enterprise
The scanner handles direct router methods, Handle/register helpers, deprecated
aliases and the table-driven entity access-profile routes. It does not import
or build either server, and never contacts a running gateway.
"""
import argparse
import json
import re
from pathlib import Path

METHODS = {"get", "post", "put", "patch", "delete", "head", "options"}
EXCLUDED = {
    ("GET", "/api/dev/pprof"): "internal debug profiler",
    ("GET", "/api/dev/pprof/goroutines"): "internal debug profiler",
    ("GET", "/api/agent/auth/authorize"): "browser SSO workflow (bifrost auth login)",
    ("GET", "/api/agent/auth/status"): "SSO workflow (bifrost auth status)",
    ("POST", "/api/agent/auth/token"): "SSO workflow (bifrost auth login)",
    ("POST", "/api/agent/auth/refresh"): "SSO token refresh workflow",
    ("POST", "/api/agent/auth/logout"): "SSO workflow (bifrost auth logout)",
    ("POST", "/api/logs/mcp"): "agent telemetry ingestion, not management",
    ("GET", "/api/oauth2/consent/flows/{id}"): "browser consent workflow",
    ("PUT", "/api/oauth2/consent/flows/{id}"): "browser consent workflow",
    ("POST", "/api/scim/oauth/callback"): "browser OAuth callback",
    ("POST", "/api/scim/oauth/refresh"): "browser OAuth session refresh",
    ("POST", "/api/scim/oauth/logout"): "browser OAuth logout",
}


def normalize(path):
    return re.sub(r"\{[^}]+\}", "{}", path)


def catalog_routes(root):
    result = set()
    for path in [root / "docs/openapi/openapi.json", root / "cli/internal/operations/management.json"]:
        for route, item in json.loads(path.read_text())["paths"].items():
            result.update((m.upper(), normalize(route)) for m in item if m in METHODS)
    return result


def registered_routes(root):
    routes = {}
    # Only production registrations under transports are management endpoints.
    for source in sorted((root / "transports").rglob("*.go")):
        if source.name.endswith("_test.go"):
            continue
        text = source.read_text()
        # Ignore commented-out registrations.
        text = re.sub(r"(?m)^\s*//.*$", "", text)
        for method, path in re.findall(r'\.(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s*\(\s*"([^"]+)"', text):
            if path.startswith("/api/"):
                routes[method, path] = str(source.relative_to(root))
        for match in re.finditer(r'(?:\.Handle|\bregister|\bregisterDeprecatedRoute)\s*\(\s*(?:r,\s*)?fasthttp\.Method(Get|Post|Put|Patch|Delete|Head|Options)\s*,\s*"(/api/[^\"]+)"(?:\s*,\s*"(/api/[^\"]+)")?', text):
            for path in match.groups()[1:]:
                if path:
                    routes[match[1].upper(), path] = str(source.relative_to(root))
        if source.name == "entityaccessprofiles.go":
            # RegisterRoutes expands these exact suffixes for each entityRoutes row.
            for segment, param in re.findall(r'\{segment: "([^"]+)"[^\n]+param: "([^"]+)"', text):
                base = f"/api/governance/{segment}/{{{param}}}/access-profile"
                entries = [("GET", base), ("POST", base), ("DELETE", base), ("POST", base+"/migrate"), ("POST", base+"/virtual-keys/{vk_id}"), ("PUT", base+"/budgets/{budget_id}/override"), ("DELETE", base+"/budgets/{budget_id}/override"), ("GET", f"/api/governance/{segment}/access-profiles")]
                for entry in entries:
                    routes[entry] = str(source.relative_to(root))
    return routes


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--oss-root", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--enterprise-root", type=Path, required=True)
    parser.add_argument("--catalog-root", type=Path, default=Path(__file__).resolve().parents[2])
    args = parser.parse_args()
    catalog = catalog_routes(args.catalog_root)
    missing = []
    for label, root in [("OSS", args.oss_root), ("Enterprise", args.enterprise_root)]:
        if not (root / "transports").is_dir():
            parser.error(f"{root} has no transports directory")
        routes = registered_routes(root)
        excluded = sum(entry in EXCLUDED for entry in routes)
        gaps = [(m, p, source) for (m, p), source in routes.items() if (m, normalize(p)) not in catalog and (m, p) not in EXCLUDED]
        print(f"{label}: {len(routes)} registered API routes, {excluded} workflow/debug exclusions, {len(gaps)} missing")
        missing.extend((label, *gap) for gap in gaps)
    for label, method, path, source in sorted(missing):
        print(f"MISSING {label}: {method} {path} ({source})")
    return bool(missing)


if __name__ == "__main__":
    raise SystemExit(main())
