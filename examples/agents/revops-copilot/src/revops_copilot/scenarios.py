"""Run one scenario from scenarios/*.yaml and check the expected surface fired.

Kinds:
  ask        the identity -> revops-copilot through the Agent Gateway (full job)
  a2a        any identity -> any agent through the Agent Gateway
  llm        any identity -> LLM Gateway
  mcp_list   any identity -> list tools on a Virtual MCP
  mcp_call   any identity -> call one tool on a Virtual MCP

Every scenario runs as the example's one identity (REVOPS_COPILOT_*) unless it
sets `caller_vk`: a literal or ${ENV} virtual key for a different principal, or
`caller`: another env prefix (e.g. UNMAPPED_ for an unmapped Entra app).
`requires` lists env vars the scenario needs; it is skipped when one is unset,
and also when a ${VAR} placeholder is left unset. `auth_mode` runs it in that
AUTH_MODE only.
"""

from __future__ import annotations

import asyncio
import os
import re
import time
from pathlib import Path
from collections.abc import Sequence
from typing import Any

import httpx
import openai
import yaml
from langchain_core.messages import HumanMessage
from rich.console import Console

from revops_copilot.cli import caller_session, print_reply
from revops_copilot.common.a2aserver import FileBlob, call_agent
from revops_copilot.common.bifrost import BifrostSession, TaskScope, llm_refusal, tool_refusal, tool_result_text
from revops_copilot.common.config import AgentName, GlobalSettings
from revops_copilot.common.identity import VirtualKeyCredential
from revops_copilot.common.policy import PolicyDenied, Surface
from revops_copilot.trace import _items

console = Console()
SCENARIO_DIR = Path(__file__).resolve().parents[2] / "scenarios"


def _load(name: str) -> dict[str, Any]:
    path = SCENARIO_DIR / f"{name}.yaml"
    if not path.exists():
        raise SystemExit(f"no scenario {name}; have: {', '.join(sorted(p.stem for p in SCENARIO_DIR.glob('*.yaml')))}")
    raw = os.path.expandvars(path.read_text())
    return yaml.safe_load(raw)


_PLACEHOLDER = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)\}")  # scenarios write ${VAR}; bare $ may be regex


def _unresolved(value: Any) -> list[str]:
    """Env placeholders that expandvars could not fill, in the order they appear."""
    if isinstance(value, str):
        return _PLACEHOLDER.findall(value)
    if isinstance(value, dict):
        return [v for x in value.values() for v in _unresolved(x)]
    if isinstance(value, list):
        return [v for x in value for v in _unresolved(x)]
    return []


async def _monitor_detections(
    bifrost_url: str, token: str, session_id: str, *, timeout_s: float = 30.0, interval_s: float = 2.0
) -> int:
    """Count output-monitor findings on the job's LLM logs.

    Monitor mode records each match as guardrail_debug.detections[] with
    enforced=false once the background evaluation finishes, a few seconds after
    the response, so this polls until one appears or the wait runs out.
    """
    deadline = time.monotonic() + timeout_s
    async with httpx.AsyncClient(base_url=bifrost_url, headers={"Authorization": f"Bearer {token}"}, timeout=30) as http:
        while True:
            r = await http.get("/api/logs", params={"session_id": session_id, "limit": 200})
            r.raise_for_status()
            found = sum(
                1
                for row in _items(r.json(), "logs", "data", "items")
                for d in ((row.get("guardrail_debug") or {}).get("detections") or [])
                if isinstance(d, dict) and d.get("enforced") is False
            )
            if found or time.monotonic() >= deadline:
                return found
            await asyncio.sleep(interval_s)


def _session(spec: dict[str, Any]) -> BifrostSession:
    if vk := spec.get("caller_vk"):
        return BifrostSession(
            agent=AgentName.SUPERVISOR,
            settings=GlobalSettings(),
            credential=VirtualKeyCredential(vk),
            scope=TaskScope.new(),
        )
    return caller_session(spec.get("caller", AgentName.SUPERVISOR.env_prefix))


async def _execute(spec: dict[str, Any], session: BifrostSession) -> tuple[PolicyDenied | None, str, dict[str, Any]]:
    """Run the scenario: (refusal, result text, what else the check needs: task state, files, data, tool error)."""
    kind = spec["kind"]
    if kind in ("ask", "a2a"):
        target = AgentName(spec.get("target", AgentName.SUPERVISOR.value))
        reply = await call_agent(session, target, spec["prompt"], data=spec.get("data"))
        print_reply(reply, session)
        return reply.denied, reply.text, {"state": reply.state, "files": reply.files, "data": reply.data}
    if kind == "llm":
        llm = await session.chat_model(model=spec.get("model"))
        try:
            out = await llm.ainvoke([HumanMessage(spec["prompt"])])
        except openai.APIStatusError as e:
            return llm_refusal(e, phase=spec.get("phase", "input")), "", {}
        extra: dict[str, Any] = {}
        if spec.get("expect", {}).get("output_detection"):
            extra["detections"] = await _monitor_detections(
                session.settings.bifrost_url, os.environ["BIFROST_ADMIN_TOKEN"], session.scope.context_id
            )
        return None, str(out.content), extra
    slug = spec.get("slug")  # none: Bifrost's MCP server (/mcp), like the agents use
    async with session.mcp(slug) as mcp:
        if kind == "mcp_list":
            names = [t.name for t in (await mcp.list_tools()).tools]
            return None, "\n".join(names), {}
        if kind == "mcp_call":
            result = await mcp.call_tool(spec["tool"], spec.get("args", {}))
            denied = tool_refusal(spec["tool"], result)
            return denied, tool_result_text(result), {"error": bool(getattr(result, "isError", False))}
    raise SystemExit(f"unknown kind {kind}")


def _check(
    spec: dict[str, Any],
    denied: PolicyDenied | None,
    text: str,
    *,
    state: str = "",
    files: Sequence[FileBlob] = (),
    data: dict[str, Any] | None = None,
    error: bool = False,
    detections: int | None = None,
) -> bool:
    expect = spec.get("expect", {})
    ok = True
    if expect.get("output_detection") and not detections:
        # A successful call does not show the output rule ran; only its recorded finding does.
        console.print("[red]no output-monitor finding on the job's LLM log: is the output rule on, in monitor mode?")
        ok = False
    if expect.get("completed") and state != "TASK_STATE_COMPLETED":
        console.print(f"[red]expected a completed task, got {state or 'no task state'}")
        ok = False
    if expect.get("pdf") and not any(f.media_type == "application/pdf" for f in files):
        console.print("[red]expected a PDF in the reply, got none")
        ok = False
    if expect.get("published") and not ((data or {}).get("publish") or {}).get("link"):
        console.print("[red]expected a published file link, got none")
        ok = False
    if expect.get("absent_patterns") and (denied is not None or error or not text.strip()):
        # Redaction is only shown when real content came back and the pattern is missing from it.
        console.print("[red]no content came back, so the result does not show that redaction worked")
        ok = False
    if "surface" in expect:
        want = Surface(expect["surface"])
        got = denied.surface if denied else None
        if got is not want:
            console.print(f"[red]expected surface {want}, got {got}")
            ok = False
    if "code" in expect:
        got_code = denied.code if denied else None
        if got_code != expect["code"]:
            console.print(f"[red]expected refusal code {expect['code']}, got {got_code}")
            ok = False
    if expect.get("allowed") and denied is not None:
        console.print(f"[red]expected success, got {denied}")
        ok = False
    for pattern in expect.get("absent_patterns", []):
        if re.search(pattern, text):
            console.print(f"[red]pattern {pattern!r} reached the caller: the guardrail did not redact it")
            ok = False
    for pattern in expect.get("present_patterns", []):
        if not re.search(pattern, text):
            console.print(f"[red]pattern {pattern!r} missing from the result")
            ok = False
    return ok


async def run_scenario(name: str) -> int:
    if not name:
        for p in sorted(SCENARIO_DIR.glob("*.yaml")):
            spec = yaml.safe_load(p.read_text())
            console.print(f"[bold]{p.stem:<20}[/] {spec.get('surface', ''):<28} {spec.get('description', '')}")
        return 0
    spec = _load(name)
    console.rule(f"[bold]{name}[/]  {spec.get('surface', '')}")
    console.print(spec.get("description", ""))
    if (mode := spec.get("auth_mode")) and GlobalSettings().auth_mode != mode:
        console.print(f"[yellow]SKIP[/] runs with AUTH_MODE={mode} only")
        return 0
    missing = [var for var in spec.get("requires", []) if not os.environ.get(var)]
    # expandvars leaves an unset ${VAR} in place, which would be sent as a literal model or tool name.
    missing += [var for var in _unresolved(spec) if var not in missing]
    if missing:
        console.print(f"[yellow]SKIP[/] set {', '.join(missing)} to run this scenario")
        return 0
    try:
        session = _session(spec)
    except Exception as e:  # noqa: BLE001
        console.print(f"[red]{e}")
        return 2
    extra: dict[str, Any] = {}
    try:
        denied, text, extra = await _execute(spec, session)
    except PolicyDenied as e:
        denied, text = e, ""
    finally:
        await session.aclose()
        await session.credential.close()

    if denied is not None:
        console.print(f"[yellow]refused:[/] {denied}")
    elif text and spec["kind"] not in ("ask", "a2a"):
        console.print(text[:2000])
    ok = _check(spec, denied, text, **extra)
    console.print(f"[dim]revops trace {session.scope.context_id}[/]")
    console.print("[green]PASS" if ok else "[red]FAIL")
    return 0 if ok else 1
