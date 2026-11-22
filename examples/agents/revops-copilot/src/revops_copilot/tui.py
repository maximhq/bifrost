"""Terminal UI: type a request, watch every step the agents take through Bifrost.

    revops            (make chat)   interactive prompt
    revops ask "..."  (make ask)    one request, same live view

Each line is one action: an LLM call, an MCP tool call, opening a Virtual MCP,
or an A2A delegation. Indentation follows delegation (supervisor, then the
subagent it called), and the circled numbers are the Bifrost policy surfaces
that evaluated that hop.
"""

from __future__ import annotations

import asyncio

from rich.console import Console
from rich.text import Text

from revops_copilot.common.a2aserver import AgentReply, call_agent
from revops_copilot.common.config import AgentName, GlobalSettings
from revops_copilot.common.identity import IdentityError
from revops_copilot.common.policy import PolicyDenied
from revops_copilot.common.progress import Progress

console = Console(highlight=False)

LABELS = {"llm": "LLM", "tool": "tool", "mcp": "MCP", "step": "", "result": "", "denied": "denied"}
STYLES = {"ok": "green", "fail": "bold red", "start": "dim cyan"}

HELP = """[bold]Commands[/]
  <anything else>     send it to revops-copilot as a job
  /trace [session]    what Bifrost logged for a job (default: the last one)
  /preflight          check the Bifrost setup
  /help               this help
  /quit               leave (Ctrl-D works too)

[bold]Try[/]
  Prepare the Q3 2026 customer health report and upload the PDF to the shared drive
  Build last quarter's support health report for the leadership team"""


def format_line(p: Progress) -> str:
    """One progress event as one plain line: tree prefix, action, result, surfaces, time."""
    prefix = "● " if p.depth == 0 else "  " * p.depth + "├ "
    if p.kind == "delegate":
        mark = {"start": "→", "ok": "✓", "fail": "✗"}.get(p.status, "→")
        head = f"{mark} {p.title}"
    else:
        mark = {"fail": "✗ ", "start": "… "}.get(p.status, "")
        label = LABELS.get(p.kind, p.kind)
        head = f"{mark}{label + ' ' if label else ''}{p.title}"
    tail = []
    if p.detail:
        tail.append(p.detail)
    if p.surfaces:
        tail.append(p.surfaces)
    if p.elapsed_ms is not None:
        tail.append(f"{p.elapsed_ms / 1000:.1f}s")
    return prefix + head + ("  ·  " + "  ".join(tail) if tail else "")


async def show(p: Progress) -> None:
    console.print(Text(format_line(p), style=STYLES.get(p.status, "")))


async def run_job(text: str) -> tuple[int, str]:
    """Send one request to revops-copilot with the live view. Returns (exit code, session id)."""
    from revops_copilot.cli import caller_session

    try:
        session = caller_session()
    except IdentityError as e:
        console.print(f"[bold red]identity:[/] {e}")
        return 2, ""
    session.emit = show
    session_id = session.scope.context_id
    console.print(f"[dim]session {session_id} · {session.settings.bifrost_url}[/]")
    reply: AgentReply | None = None
    try:
        reply = await call_agent(session, AgentName.SUPERVISOR, text)
    except PolicyDenied as denied:
        console.print(f"[bold red]✗ refused by Bifrost[/] at surface {denied.surface} ({denied.status} {denied.code})")
        console.print(f"  {denied.message[:300]}")
    except Exception as e:  # noqa: BLE001 - the prompt must survive any failure
        console.print(f"[bold red]✗ {type(e).__name__}:[/] {str(e)[:400]}")
    finally:
        await session.aclose()
        await session.credential.close()

    if reply is None:
        code = 1
    elif reply.denied is not None:
        console.print(f"\n[bold red]✗ stopped by policy[/] at surface {reply.denied.surface} "
                      f"on the {reply.denied.hop} hop ({reply.denied.status} {reply.denied.code})")
        code = 1
    else:
        console.print()
        console.print(reply.text or reply.state)
        link = (reply.data.get("publish") or {}).get("link")
        if link:
            console.print(f"[bold]file:[/] {link}")
        # Only a completed task is success; a failed, canceled or unknown state is not.
        code = 0 if reply.state == "TASK_STATE_COMPLETED" else 1
        if code:
            console.print(f"[bold red]✗ task ended {reply.state}[/]")
    console.print(f"[dim]/trace {session_id}[/]")
    return code, session_id


async def chat() -> int:
    g = GlobalSettings()
    console.print(f"[bold]revops-copilot[/] · every LLM, MCP and A2A call goes through Bifrost at {g.bifrost_url}")
    console.print("[dim]Type a request, or /help. Ctrl-C stops the current job; Ctrl-D quits.[/]\n")
    last = ""
    while True:
        try:
            text = (await asyncio.to_thread(console.input, "[bold cyan]revops ❯[/] ")).strip()
        except (EOFError, KeyboardInterrupt):
            console.print()
            return 0
        if not text:
            continue
        if text in ("/quit", "/exit"):
            return 0
        if text == "/help":
            console.print(HELP)
            continue
        if text == "/preflight":
            from revops_copilot.preflight import preflight

            await preflight()
            continue
        if text.startswith("/trace"):
            from revops_copilot.trace import trace

            target = text.removeprefix("/trace").strip() or last
            if target:
                await trace(target)
            else:
                console.print("no job yet: /trace <session-id>")
            continue
        if text.startswith("/"):
            console.print(f"unknown command {text}; /help lists them")
            continue
        try:
            _, last = await run_job(text)
        except asyncio.CancelledError:
            # Ctrl-C cancels the running job only; clear the request and keep the prompt.
            task = asyncio.current_task()
            if task is not None:
                task.uncancel()
            console.print("[yellow]stopped[/]")
        console.print()
