"""Shared helpers for the tracing SDK suite.

Each test points one vendor SDK at a running Bifrost, sends traces, and reads them back through the
dashboard's Traces API. Nothing here talks to a vendor: every SDK URL is a Bifrost URL.

Environment:
    BIFROST_BASE_URL               the gateway (default http://localhost:8080)
    BIFROST_ADMIN_USERNAME/PASSWORD dashboard credentials, when dashboard auth is on. SDKs send them as
                                   their API key, exactly as a user would.
    BIFROST_SETUP_TOKEN            sent on dashboard reads while dashboard auth is off (the OSS setup lock)
    BIFROST_TRACING_GRPC_ENDPOINT  host:port of the OTLP gRPC listener (default localhost:4317)
"""

from __future__ import annotations

import base64
import os
import time
import uuid
from dataclasses import dataclass
from typing import Any, Callable

import pytest
import requests

POLL_TIMEOUT_S = 30.0
POLL_INTERVAL_S = 0.25


@dataclass(frozen=True)
class Gateway:
    base_url: str
    username: str
    password: str
    setup_token: str
    grpc_endpoint: str

    @property
    def auth_on(self) -> bool:
        return bool(self.username)

    # The forms each SDK sends its key in. All of them carry the dashboard credentials.
    @property
    def api_key(self) -> str:
        """A bare "username:password": what Braintrust and Arize send as their API key."""
        return f"{self.username}:{self.password}" if self.auth_on else "unused"

    @property
    def basic_header(self) -> dict[str, str]:
        if not self.auth_on:
            return {}
        token = base64.b64encode(f"{self.username}:{self.password}".encode()).decode()
        return {"Authorization": f"Basic {token}"}

    def url(self, path: str) -> str:
        return self.base_url.rstrip("/") + path

    # ----- Traces API -----

    @property
    def dashboard_headers(self) -> dict[str, str]:
        """Headers for /api reads: the credentials, or the setup token while dashboard auth is off."""
        if self.auth_on:
            return self.basic_header
        return {"X-Bifrost-Setup-Token": self.setup_token} if self.setup_token else {}

    def get(self, path: str, **params: Any) -> Any:
        clean = {k: (",".join(v) if isinstance(v, (list, tuple)) else v) for k, v in params.items() if v is not None}
        response = requests.get(self.url(path), params=clean, headers=self.dashboard_headers, timeout=15)
        assert response.status_code == 200, f"GET {path} -> {response.status_code}: {response.text[:300]}"
        return response.json()

    def project_id(self, name: str) -> str:
        def find() -> str | None:
            for project in self.get("/api/traces/projects")["projects"]:
                if project["name"] == name:
                    return project["id"]
            return None

        return wait_for(find, f"tracing project {name!r}")

    def traces(self, **filters: Any) -> list[dict]:
        return self.get("/api/traces", limit=200, **filters)["traces"]

    def spans(self, **filters: Any) -> list[dict]:
        return self.get("/api/traces/spans", limit=200, **filters)["spans"]

    def detail(self, project_id: str, trace_id: str) -> dict:
        return self.get(f"/api/traces/trace/{project_id}/{trace_id}")

    def wait_trace(self, project_id: str, trace_id: str, span_count: int, *, complete: bool = True) -> dict:
        """Waits until the trace holds span_count spans (and, by default, its root has landed)."""

        def ready() -> dict | None:
            response = requests.get(
                self.url(f"/api/traces/trace/{project_id}/{trace_id}"), headers=self.dashboard_headers, timeout=15
            )
            if response.status_code == 404:
                return None
            assert response.status_code == 200, response.text[:300]
            body = response.json()
            if len(body["spans"]) < span_count:
                return None
            if complete and body["trace"].get("in_progress"):
                return None
            return body

        return wait_for(ready, f"trace {trace_id} with {span_count} spans")

    def wait_traces(self, count: int, **filters: Any) -> list[dict]:
        def ready() -> list[dict] | None:
            found = self.traces(**filters)
            return found if len(found) >= count else None

        return wait_for(ready, f"{count} traces matching {filters}")


def wait_for(probe: Callable[[], Any], what: str, timeout: float = POLL_TIMEOUT_S) -> Any:
    """Polls probe until it returns something truthy. Ingest acks after its write commits, but the
    trace rollup is recomputed shortly after, so reads poll instead of sleeping a fixed time."""
    deadline = time.monotonic() + timeout
    last: Any = None
    while time.monotonic() < deadline:
        last = probe()
        if last:
            return last
        time.sleep(POLL_INTERVAL_S)
    raise AssertionError(f"timed out after {timeout:.0f}s waiting for {what} (last: {last!r})")


def by_name(spans: list[dict]) -> dict[str, dict]:
    return {span["name"]: span for span in spans}


def otel_trace_id(span: Any) -> str:
    return format(span.get_span_context().trace_id, "032x")


def otel_span_id(span: Any) -> str:
    return format(span.get_span_context().span_id, "016x")


@pytest.fixture(scope="session")
def gateway() -> Gateway:
    base = os.getenv("BIFROST_BASE_URL", "http://localhost:8080")
    response = requests.get(base.rstrip("/") + "/health", timeout=10)
    assert response.status_code == 200, f"no Bifrost at {base}: {response.status_code}"
    return Gateway(
        base_url=base,
        username=os.getenv("BIFROST_ADMIN_USERNAME", ""),
        password=os.getenv("BIFROST_ADMIN_PASSWORD", ""),
        setup_token=os.getenv("BIFROST_SETUP_TOKEN", ""),
        grpc_endpoint=os.getenv("BIFROST_TRACING_GRPC_ENDPOINT", "localhost:4317"),
    )


@pytest.fixture
def run_id() -> str:
    """A short id that keeps every name, session and project of one test apart from other runs."""
    return uuid.uuid4().hex[:10]
