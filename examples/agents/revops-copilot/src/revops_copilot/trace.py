"""Print what Bifrost logged for one job, ordered by time, labelled by surface.

One job is one Bifrost session (x-bf-session-id = the A2A context id), so three
read-only queries cover it:

* GET /api/agents/history?context_id=<id>  A2A hops (Agent Gateway)
* GET /api/logs?session_id=<id>            LLM calls
* GET /api/mcp-logs?session_id=<id>        MCP tool calls

Field names differ slightly across Bifrost versions, so rows are read
defensively and unknown fields are skipped.
"""

from __future__ import annotations

import json
import os
from typing import Any

import httpx
from rich.console import Console
from rich.table import Table

from revops_copilot.common.config import GlobalSettings
from revops_copilot.common.policy import Hop, Surface, classify
from revops_copilot.common.progress import circled

console = Console()


def _items(body: Any, *keys: str) -> list[dict[str, Any]]:
    if isinstance(body, list):
        return [x for x in body if isinstance(x, dict)]
    if isinstance(body, dict):
        for k in keys:
            if isinstance(body.get(k), list):
                return [x for x in body[k] if isinstance(x, dict)]
    return []


def _get(row: dict[str, Any], *keys: str) -> str:
    for k in keys:
        cur: Any = row
        for part in k.split("."):
            cur = cur.get(part) if isinstance(cur, dict) else None
        if cur not in (None, "", [], {}):
            return str(cur)
    return ""


def _who(row: dict[str, Any]) -> str:
    return _get(row, "user_name", "user_email", "user_id", "virtual_key_name", "virtual_key.name", "virtual_key_id")


_HOPS = {"a2a": Hop.A2A, "llm": Hop.LLM, "mcp": Hop.MCP}


def _checks(kind: str) -> str:
    """The surfaces that can act on this hop: candidates, not a record of what ran."""
    return {"a2a": "1,2", "llm": "1,3,4,8", "mcp": "1,5,6,7"}[kind]


def _fired(kind: str, row: dict[str, Any]) -> str:
    """What the log records as having acted: the refusing surface, or an output-monitor finding."""
    debug = row.get("guardrail_debug") if isinstance(row.get("guardrail_debug"), dict) else {}
    parts: list[str] = []
    if _get(row, "status").lower() in ("error", "failed", "rejected"):
        details = row.get("error_details")
        body = json.dumps(details) if isinstance(details, dict) else _get(row, "error", "error_message")
        status = _get(row, "error_details.status_code", "status_code")
        interventions = [i for i in (debug.get("interventions") or []) if isinstance(i, dict)]
        phase = str(interventions[0].get("phase") or "input") if interventions else "input"
        denied = classify(_HOPS[kind], int(status) if status.isdigit() else 0, body, phase=phase)
        if denied.surface is Surface.UNKNOWN and not interventions:
            parts.append("error, not a policy refusal")
        else:
            parts.append(f"[red]{circled(denied.surface)} refused ({denied.code or denied.status})")
    for d in debug.get("detections") or []:
        if isinstance(d, dict):
            surface = Surface.MCP_GUARDRAIL if kind == "mcp" else (
                Surface.LLM_OUTPUT_GUARDRAIL if d.get("phase") == "output" else Surface.LLM_INPUT_GUARDRAIL
            )
            mode = "monitor" if d.get("enforced") is False else "enforced"
            parts.append(f"{circled(surface)} {d.get('action') or 'detect'} ({mode})")
    return "; ".join(parts)


async def trace(session_id: str) -> int:
    g = GlobalSettings()
    token = os.environ.get("BIFROST_ADMIN_TOKEN", "")
    headers = {"Authorization": f"Bearer {token}"} if token else {}
    rows: list[tuple[str, str, str, str, str, str, str]] = []
    failed: list[str] = []
    async with httpx.AsyncClient(base_url=g.bifrost_url, headers=headers, timeout=30) as http:
        queries = (
            ("a2a", "/api/agents/history", {"context_id": session_id, "limit": 200}, ("data", "logs", "items", "history")),
            ("llm", "/api/logs", {"session_id": session_id, "limit": 200}, ("logs", "data", "items")),
            ("mcp", "/api/mcp-logs", {"session_id": session_id, "limit": 200}, ("logs", "data", "items")),
        )
        for kind, path, params, keys in queries:
            r = await http.get(path, params=params)
            if r.status_code != 200:
                console.print(f"[yellow]{path}: {r.status_code} {r.text[:200]}")
                failed.append(path)
                continue
            for row in _items(r.json(), *keys):
                what = {
                    "a2a": _get(row, "agent_name", "agent", "method"),
                    "llm": _get(row, "model", "provider"),
                    "mcp": _get(row, "tool_name", "tool", "mcp_client_name"),
                }[kind]
                rows.append(
                    (
                        _get(row, "timestamp", "created_at", "started_at"),
                        kind,
                        what,
                        _who(row),
                        _get(row, "status"),
                        _checks(kind),
                        _fired(kind, row),
                    )
                )
    rows.sort(key=lambda r: r[0])
    table = Table("time", "hop", "target", "identity", "status", "checks (candidates)", "fired", title=f"session {session_id}")
    for row in rows:
        table.add_row(*row)
    console.print(table)
    if failed:
        # A partial or failed read must not look like a complete trace to make or a script.
        console.print(f"[red]Could not read {', '.join(failed)}: the trace is incomplete.")
        return 1
    if not rows:
        console.print("No rows. Check BIFROST_ADMIN_TOKEN, log retention, and that the agents sent x-bf-session-id.")
    return 0
