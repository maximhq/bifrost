"""Check the Bifrost setup this example expects. Read-only: it creates nothing.

Each check prints PASS, FAIL or SKIP with the fix. Identity and MCP checks run
as the example's one identity (REVOPS_COPILOT_*), so they prove its access
profile, not just that an object exists.
"""

from __future__ import annotations

import os
import sys
from collections.abc import Callable
from urllib.parse import urlparse

import httpx
from rich.console import Console
from rich.table import Table

from revops_copilot.cli import caller_session
from revops_copilot.common.bifrost import BifrostSession
from revops_copilot.common.config import AgentName, AgentSettings, GlobalSettings
from revops_copilot.common.identity import IdentityError
from revops_copilot.common.policy import PolicyDenied

console = Console()


# Plain http only for a gateway on this machine: preflight sends the admin token and the caller's credential.
LOOPBACK_HOSTS = {"localhost", "127.0.0.1", "::1"}


def gateway_url_problem(url: str) -> str | None:
    """Why this gateway URL cannot be used, or None when it can."""
    if not url.startswith(("http://", "https://")):
        return "enter a URL starting with http:// or https://"
    if url.startswith("http://") and urlparse(url).hostname not in LOOPBACK_HOSTS:
        return "use https:// for a gateway on another machine; http:// is only for localhost"
    return None


def ask_gateway_url(ask: Callable[[str], str] = input, *, tty: bool | None = None) -> str:
    """Ask which Bifrost gateway to check; BIFROST_URL (env or .env) is the default.

    The answer is written to os.environ so every settings object built later in
    this process uses it. Without a terminal, BIFROST_URL is required.
    """
    current = os.environ.get("BIFROST_URL", "").strip().rstrip("/")
    if tty is None:
        tty = sys.stdin.isatty()
    if not tty:
        if not current:
            raise SystemExit("set BIFROST_URL (no terminal to ask for the gateway URL)")
        if problem := gateway_url_problem(current):
            raise SystemExit(f"BIFROST_URL={current}: {problem}")
        os.environ["BIFROST_URL"] = current
        return current
    prompt = f"Bifrost gateway URL [{current}]: " if current else "Bifrost gateway URL (e.g. https://bifrost.example.com): "
    while True:
        url = (ask(prompt).strip() or current).rstrip("/")
        problem = gateway_url_problem(url)
        if problem is None:
            os.environ["BIFROST_URL"] = url
            return url
        console.print(f"[red]{problem}[/]")


class Report:
    def __init__(self) -> None:
        self.table = Table("check", "result", "detail", show_lines=False)
        self.failed = 0

    def add(self, check: str, ok: bool | None, detail: str = "") -> None:
        label = {True: "[green]PASS", False: "[red]FAIL", None: "[yellow]SKIP"}[ok]
        if ok is False:
            self.failed += 1
        self.table.add_row(check, label, detail)


UNGRANTED_SLUG = "revops-not-granted"


def _unreachable(e: httpx.HTTPError) -> str:
    # Gateway availability is often the thing being diagnosed, so a failed probe is a row, not a crash.
    return f"{type(e).__name__}: {str(e)[:160] or 'no response'}"


async def _identity(report: Report) -> BifrostSession | None:
    try:
        session = caller_session()
    except IdentityError as e:
        report.add("identity configured", False, str(e))
        return None
    try:
        headers = await session.headers()  # Entra mode: this fetches the token
    except Exception as e:  # noqa: BLE001 - e.g. AADSTS7000215 (wrong secret), AADSTS501051 (no app role)
        report.add("identity token", False, f"{type(e).__name__}: {str(e)[:200]}")
        await session.credential.close()
        return None
    try:
        async with httpx.AsyncClient(timeout=20) as http:
            r = await http.get(f"{session.settings.bifrost_url}/v1/models", headers=headers)
    except httpx.HTTPError as e:
        report.add("identity accepted (/v1/models)", False, _unreachable(e))
        await session.credential.close()
        return None
    ok = r.status_code == 200
    report.add("identity accepted (/v1/models)", ok, "" if ok else f"{r.status_code} {r.text[:200]}")
    if not ok:
        await session.credential.close()
    return session if ok else None


async def _mcp(report: Report, session: BifrostSession, slug: str, *, expect_access: bool) -> list[str]:
    label = f"{session.mcp_path(slug)} {'lists tools' if expect_access else 'is refused'}"
    try:
        async with session.mcp(slug) as mcp:
            names = [t.name for t in (await mcp.list_tools()).tools]
    except PolicyDenied as denied:
        report.add(label, not expect_access, f"{denied.status} {denied.message[:120]}")
        return []
    except Exception as e:  # noqa: BLE001
        report.add(label, False, f"{type(e).__name__}: {str(e)[:160]}")
        return []
    if expect_access:
        report.add(label, bool(names), ", ".join(names) or "no tools: check the Virtual MCP and the MCP client OAuth")
    else:
        # Only a refusal proves discovery was denied; an empty successful listing does not.
        report.add(label, False, f"listing succeeded, discovery was not refused; visible: {names}")
    return names


async def _admin(report: Report, g: GlobalSettings, token: str) -> None:
    if not token:
        report.add("admin: registrations and Virtual MCPs", None, "set BIFROST_ADMIN_TOKEN to check")
        return
    try:
        await _admin_checks(report, g, {"Authorization": f"Bearer {token}"})
    except httpx.HTTPError as e:
        report.add("admin: Bifrost reachable", False, _unreachable(e))


async def _admin_checks(report: Report, g: GlobalSettings, headers: dict[str, str]) -> None:
    async with httpx.AsyncClient(base_url=g.bifrost_url, headers=headers, timeout=20) as http:
        r = await http.get("/api/agents")
        if r.status_code != 200:
            report.add("admin: GET /api/agents", False, f"{r.status_code} {r.text[:160]}")
        else:
            body = r.json()
            items = body.get("agents", body) if isinstance(body, dict) else body
            names = {a.get("name"): a for a in items if isinstance(a, dict)}
            for agent in AgentName:
                reg = names.get(agent.value)
                if reg is None:
                    report.add(f"admin: agent '{agent.value}' registered", False, "register it in Agent Gateway")
                    continue
                problems = []
                if not reg.get("enabled", True):
                    problems.append("disabled")
                if reg.get("allow_by_default"):
                    problems.append("allow_by_default is on: grants are not enforced")
                forwards = bool(reg.get("forward_accepted_credential"))
                if agent is AgentName.SUPERVISOR and forwards:
                    problems.append("forwarding is on: it should use its own identity from .env")
                if agent is not AgentName.SUPERVISOR and not forwards:
                    problems.append("turn on Forward Bifrost authentication: this agent has no identity of its own")
                report.add(f"admin: agent '{agent.value}' registered", not problems, "; ".join(problems))
        r = await http.get("/api/mcp/virtual-mcps")
        if r.status_code == 200:
            body = r.json()
            items = body.get("virtual_mcps", body) if isinstance(body, dict) else body
            slugs = {v.get("endpoint_slug") for v in items if isinstance(v, dict)}
            if g.mcp_slug:  # narrowed to one Virtual MCP: it must exist
                ok = g.mcp_slug in slugs
                report.add(f"admin: Virtual MCP '{g.mcp_slug}'", ok, "" if ok else f"found {sorted(s for s in slugs if s)}")
        else:
            report.add("admin: GET /api/mcp/virtual-mcps", False, f"{r.status_code} {r.text[:160]}")


async def _local_agents(report: Report) -> None:
    # Liveness only: preflight runs beside the agents, so probe the listen port.
    # Reachability from Bifrost (ngrok, registration, grant) is the
    # "card through Agent Gateway" check.
    async with httpx.AsyncClient(timeout=5) as http:
        for agent in AgentName:
            port = AgentSettings.for_agent(agent).port
            check = f"{agent.value}: listening on :{port}"
            try:
                r = await http.get(f"http://127.0.0.1:{port}/.well-known/agent-card.json")
            except httpx.HTTPError:
                report.add(check, False, "not running: make agents-ngrok (remote gateway) or make agents")
                continue
            if r.status_code != 200:
                report.add(check, False, f"{r.status_code} on the agent card")
                continue
            interfaces = r.json().get("supportedInterfaces") or [{}]
            report.add(check, True, f"card advertises {interfaces[0].get('url', '?')}")


async def _gateway(report: Report, session: BifrostSession) -> None:
    async with httpx.AsyncClient(timeout=20) as http:
        for agent in AgentName:
            url = session.agent_url(agent) + "/.well-known/agent-card.json"
            try:
                r = await http.get(url, headers=await session.headers())
            except httpx.HTTPError as e:
                report.add(f"{agent.value}: card through Agent Gateway", False, _unreachable(e))
                continue
            detail = url if r.status_code == 200 else f"{r.status_code} {r.text[:160]}"
            report.add(f"{agent.value}: card through Agent Gateway", r.status_code == 200, detail)


async def preflight() -> int:
    ask_gateway_url()
    g = GlobalSettings()
    report = Report()
    console.print(f"Bifrost: {g.bifrost_url}   auth mode: {g.auth_mode}")

    await _admin(report, g, os.environ.get("BIFROST_ADMIN_TOKEN", ""))
    await _local_agents(report)

    session = await _identity(report)
    if session is not None:
        await _gateway(report, session)
        # Every agent opens Bifrost's MCP server; it must offer the Pylon and Drive tools.
        tools = await _mcp(report, session, g.mcp_slug, expect_access=True)
        for server in ("Pylon", "Drive"):
            count = sum(1 for t in tools if t.lower().startswith(server.lower() + "-"))
            report.add(f"{session.mcp_path(g.mcp_slug)} offers {server} tools", count > 0,
                       f"{count} tools" if count else f"none: grant a {server} Virtual MCP to the identity")
        await _mcp(report, session, UNGRANTED_SLUG, expect_access=False)
        if tools:
            from mcp.types import Tool

            from revops_copilot.agents.publisher.upload import UploadMappingError, pick_upload_tool

            try:
                picked = pick_upload_tool([Tool(name=t, inputSchema={}) for t in tools], g.drive_upload_tool)
                report.add("Drive upload tool", True, picked.name)
            except UploadMappingError as e:
                report.add("Drive upload tool", False, str(e))
        await session.aclose()
        await session.credential.close()
    if not g.drive_folder_id:
        report.add("DRIVE_FOLDER_ID", None, "empty: uploads land in My Drive root")

    console.print(report.table)
    return 1 if report.failed else 0
