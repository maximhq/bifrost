#!/usr/bin/env python3
"""Validate Bifrost's Render Blueprints and Railway template contracts."""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any

import yaml
from jsonschema import Draft201909Validator, Draft202012Validator, FormatChecker


REPO_ROOT = Path(__file__).resolve().parents[3]
BIFROST_SCHEMA = json.loads((REPO_ROOT / "transports/config.schema.json").read_text())
RAILWAY_SCHEMA = json.loads((REPO_ROOT / "deploy/railway/template-contract.schema.json").read_text())


def fail(message: str) -> None:
    raise AssertionError(message)


def validate_json_schema(instance: Any, schema: dict[str, Any], label: str, draft: type) -> None:
    validator = draft(schema, format_checker=FormatChecker())
    errors = sorted(validator.iter_errors(instance), key=lambda error: list(error.absolute_path))
    if errors:
        details = "\n".join(
            f"  - {'.'.join(str(part) for part in error.absolute_path) or '<root>'}: {error.message}"
            for error in errors
        )
        fail(f"{label} failed schema validation:\n{details}")


def assert_bootstrap_config(config: dict[str, Any], label: str, storage: str) -> None:
    validate_json_schema(config, BIFROST_SCHEMA, f"{label} BIFROST_CONFIG", Draft201909Validator)
    if config.get("source_of_truth") != "split":
        fail(f"{label} must preserve dashboard-managed configuration with source_of_truth=split")
    if config.get("encryption_key") != "env.BIFROST_ENCRYPTION_KEY":
        fail(f"{label} must reference the generated encryption key")
    if config.get("client", {}).get("enforce_auth_on_inference") is not True:
        fail(f"{label} must reject anonymous inference")

    auth = config.get("governance", {}).get("auth_config", {})
    expected_auth = {
        "admin_username": "admin",
        "admin_password": "env.BIFROST_ADMIN_PASSWORD",
        "is_enabled": True,
    }
    if auth != expected_auth:
        fail(f"{label} must provision operator-managed dashboard authentication")

    for store_name in ("config_store", "logs_store"):
        store = config.get(store_name)
        if storage == "postgres":
            if not store or store.get("type") != "postgres" or store.get("enabled") is not True:
                fail(f"{label} must enable PostgreSQL for {store_name}")
            store_config = store.get("config", {})
            if store_config.get("ssl_mode") != "require":
                fail(f"{label} must require TLS for {store_name}")
            if store_config.get("max_open_conns", 0) > 20:
                fail(f"{label} exceeds the one-click PostgreSQL connection budget")
        elif store is not None:
            fail(f"{label} SQLite configuration must not override {store_name}")


def env_vars(service: dict[str, Any]) -> dict[str, dict[str, Any]]:
    values = service.get("envVars", [])
    return {entry["key"]: entry for entry in values}


def validate_render(path: Path, storage: str) -> None:
    blueprint = yaml.safe_load(path.read_text())
    services = blueprint.get("services", [])
    if len(services) != 1:
        fail(f"{path} must define exactly one Bifrost service")
    service = services[0]
    label = str(path.relative_to(REPO_ROOT))

    if service.get("runtime") != "image" or service.get("image", {}).get("url") != "docker.io/maximhq/bifrost:latest":
        fail(f"{label} must use the public latest image")
    if service.get("numInstances") != 1:
        fail(f"{label} must run one OSS replica")
    if service.get("autoDeploy") is not False:
        fail(f"{label} image-tag updates must require an intentional manual deploy")
    if service.get("healthCheckPath") != "/health":
        fail(f"{label} must use /health")
    if service.get("maxShutdownDelaySeconds") != 60:
        fail(f"{label} must allow 60 seconds for shutdown")

    variables = env_vars(service)
    for name in ("BIFROST_ENCRYPTION_KEY", "BIFROST_ADMIN_PASSWORD"):
        if variables.get(name, {}).get("generateValue") is not True:
            fail(f"{label} must generate {name}")
    config = json.loads(variables["BIFROST_CONFIG"]["value"])
    assert_bootstrap_config(config, label, storage)

    if storage == "postgres":
        databases = blueprint.get("databases", [])
        if len(databases) != 1:
            fail(f"{label} must define one PostgreSQL database")
        database = databases[0]
        if service.get("plan") != "free" or database.get("plan") != "free":
            fail(f"{label} PostgreSQL evaluation must use free plans")
        if database.get("postgresMajorVersion") != "18":
            fail(f"{label} must provision PostgreSQL 18")
        if database.get("ipAllowList") != []:
            fail(f"{label} database must not allow public network ranges")
        if service.get("disk") is not None:
            fail(f"{label} PostgreSQL service must remain ephemeral")
    else:
        disk = service.get("disk", {})
        if service.get("plan") != "starter":
            fail(f"{label} SQLite service must use the minimum disk-capable plan")
        if disk != {"name": "bifrost-data", "mountPath": "/app/data", "sizeGB": 1}:
            fail(f"{label} must attach a 1 GB /app/data disk")
        if blueprint.get("databases"):
            fail(f"{label} SQLite Blueprint must not provision PostgreSQL")


def railway_service(contract: dict[str, Any], name: str) -> dict[str, Any]:
    for service in contract["services"]:
        if service["name"] == name:
            return service
    fail(f"Railway contract does not contain {name}")


def validate_railway(path: Path, storage: str) -> None:
    contract = json.loads(path.read_text())
    label = str(path.relative_to(REPO_ROOT))
    validate_json_schema(contract, RAILWAY_SCHEMA, label, Draft202012Validator)
    bifrost = railway_service(contract, "Bifrost")

    if contract["template"]["owner"] != "Maxim AI's Projects":
        fail(f"{label} must remain Maxim-owned")

    if bifrost["image"] != "docker.io/maximhq/bifrost:latest" or bifrost.get("image_auto_update") != "daily":
        fail(f"{label} must track latest with daily image checks")
    deploy = bifrost["deploy"]
    expected = {"replicas": 1, "healthcheck_path": "/health", "draining_seconds": 60, "public_port": 8080}
    for key, value in expected.items():
        if deploy.get(key) != value:
            fail(f"{label} has invalid Bifrost deploy setting {key}")

    variables = bifrost["variables"]
    if variables.get("BIFROST_CONFIG", {}).get("sensitive") is not False:
        fail(f"{label} BIFROST_CONFIG must contain references, not secret values")
    for name in ("BIFROST_ENCRYPTION_KEY", "BIFROST_ADMIN_PASSWORD"):
        variable = variables.get(name, {})
        if variable.get("value") != "${{secret()}}" or variable.get("sensitive") is not True:
            fail(f"{label} must generate and hide {name}")
    config = json.loads(variables["BIFROST_CONFIG"]["value"])
    assert_bootstrap_config(config, label, storage)

    if storage == "postgres":
        postgres = railway_service(contract, "Postgres")
        if contract["template"]["slug"] != "blue-dark":
            fail(f"{label} must describe the existing blue-dark template")
        if postgres["image"] != "ghcr.io/railwayapp-templates/postgres-ssl:18":
            fail(f"{label} must provision the TLS-enabled PostgreSQL 18 image")
        expected_postgres_variables = {
            "POSTGRES_PASSWORD": "${{secret()}}",
            "PGHOST": "${{RAILWAY_PRIVATE_DOMAIN}}",
            "PGPORT": "5432",
            "PGUSER": "${{POSTGRES_USER}}",
            "PGPASSWORD": "${{POSTGRES_PASSWORD}}",
            "PGDATABASE": "${{POSTGRES_DB}}",
        }
        for name, value in expected_postgres_variables.items():
            if postgres["variables"].get(name, {}).get("value") != value:
                fail(f"{label} has invalid PostgreSQL variable {name}")
        if postgres["volumes"] != [{"mount_path": "/var/lib/postgresql/data"}]:
            fail(f"{label} must persist PostgreSQL data")
        if bifrost["volumes"]:
            fail(f"{label} PostgreSQL Bifrost service must not attach a volume")
        for variable in ("RAILWAY_RUN_UID", "BIFROST_RUN_AS_UID", "BIFROST_RUN_AS_GID"):
            if variable in variables:
                fail(f"{label} PostgreSQL Bifrost service must not set {variable}")
    else:
        expected_variables = {"RAILWAY_RUN_UID": "0", "BIFROST_RUN_AS_UID": "1000", "BIFROST_RUN_AS_GID": "0"}
        for name, value in expected_variables.items():
            if variables.get(name, {}).get("value") != value:
                fail(f"{label} must set {name}={value}")
        if bifrost["volumes"] != [{"mount_path": "/app/data"}]:
            fail(f"{label} must attach only /app/data")
        if deploy.get("overlap_seconds") != 0:
            fail(f"{label} must disable overlap for the single-writer SQLite volume")


def validate_documentation_links() -> None:
    render_docs = (REPO_ROOT / "docs/deployment-guides/platforms/render.mdx").read_text()
    railway_docs = (REPO_ROOT / "docs/deployment-guides/platforms/railway.mdx").read_text()
    expected_render_links = (
        "https://render.com/deploy?repo=https://github.com/maximhq/bifrost/tree/main",
        "https://render.com/deploy?repo=https://github.com/maximhq/bifrost/tree/render-sqlite",
    )
    for link in expected_render_links:
        if link not in render_docs:
            fail(f"Render documentation is missing branch-bound button {link}")
    railway_postgres = "https://railway.com/new/template/blue-dark"
    if railway_postgres not in railway_docs:
        fail("Railway documentation is missing the blue-dark PostgreSQL button")

    sqlite_contract = json.loads((REPO_ROOT / "deploy/railway/sqlite.template-contract.json").read_text())
    sqlite_slug = sqlite_contract["template"]["slug"]
    if sqlite_slug and f"https://railway.com/new/template/{sqlite_slug}" not in railway_docs:
        fail("Railway SQLite template slug is not linked from its documentation")


def main() -> int:
    validate_render(REPO_ROOT / "render.yaml", "postgres")
    validate_render(REPO_ROOT / "deploy/render/render-sqlite.yaml", "sqlite")
    validate_railway(REPO_ROOT / "deploy/railway/postgres.template-contract.json", "postgres")
    validate_railway(REPO_ROOT / "deploy/railway/sqlite.template-contract.json", "sqlite")
    validate_documentation_links()
    print("deployment template validation passed")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (AssertionError, KeyError, TypeError, ValueError, json.JSONDecodeError) as error:
        print(f"deployment template validation failed: {error}", file=sys.stderr)
        sys.exit(1)
