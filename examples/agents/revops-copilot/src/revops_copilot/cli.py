"""revops: run agents, send a job, check Bifrost setup, run scenarios, trace a session.

    revops                        interactive prompt with a live view of every step (make chat)
    revops serve <agent>          start one agent's A2A server
    revops ngrok                  start all four agents behind public ngrok URLs
    revops cloudflared            start all four agents behind Cloudflare quick tunnels
    revops ask "<request>"        one job through the Agent Gateway, same live view
    revops preflight              check the Bifrost setup this example expects
    revops demo <scenario>        run one scenario from scenarios/
    revops trace <session-id>     print the per-surface timeline Bifrost logged
"""

from __future__ import annotations

import argparse
import asyncio
import importlib
import json
import sys

from dotenv import load_dotenv
from rich.console import Console

from revops_copilot.common.a2aserver import AgentReply
from revops_copilot.common.bifrost import BifrostSession, TaskScope
from revops_copilot.common.config import AgentName, AgentSettings, GlobalSettings
from revops_copilot.common.identity import IdentityError, credential_for

console = Console()

AGENT_MODULES = {
    AgentName.SUPERVISOR: "revops_copilot.supervisor.agent",
    AgentName.SUPPORT_ANALYST: "revops_copilot.agents.supportanalyst.agent",
    AgentName.REPORT_WRITER: "revops_copilot.agents.reportwriter.agent",
    AgentName.PUBLISHER: "revops_copilot.agents.publisher.agent",
}

# The one identity of this example lives on the entry agent. The CLI starts jobs
# as that identity too; Bifrost forwards it to every subagent.
IDENTITY_PREFIX = AgentName.SUPERVISOR.env_prefix


def caller_session(env_prefix: str = IDENTITY_PREFIX, *, as_agent: AgentName = AgentName.SUPERVISOR) -> BifrostSession:
    """A session for a caller outside any A2A task (the CLI, preflight, scenarios)."""
    g = GlobalSettings()
    a = AgentSettings.with_prefix(env_prefix)
    return BifrostSession(agent=as_agent, settings=g, credential=credential_for(env_prefix, g, a), scope=TaskScope.new())


def print_reply(reply: AgentReply, session: BifrostSession) -> None:
    style = "red" if reply.denied else "green"
    console.rule(f"[{style}]{reply.state}")
    console.print(reply.text)
    if reply.denied:
        console.print(f"[red]policy surface:[/] {reply.denied.surface}  hop={reply.denied.hop}  code={reply.denied.code}")
    steps = reply.data.get("steps")
    if steps:
        console.print_json(json.dumps(steps))
    console.print(f"[dim]x-bf-session-id={session.scope.context_id}  (revops trace {session.scope.context_id})[/]")


async def ask(text: str) -> int:
    from revops_copilot.tui import run_job

    code, _ = await run_job(text)
    return code


def main(argv: list[str] | None = None) -> int:
    # Scenario files and the admin token read os.environ; settings read .env too.
    load_dotenv(override=False)
    parser = argparse.ArgumentParser(prog="revops")
    sub = parser.add_subparsers(dest="cmd")
    sub.add_parser("chat", help="interactive prompt with a live view of every step (the default)")
    p_serve = sub.add_parser("serve", help="start one agent")
    p_serve.add_argument("agent", choices=[a.value for a in AgentName])
    p_ask = sub.add_parser("ask", help="send a job to revops-copilot")
    p_ask.add_argument("text")
    sub.add_parser("ngrok", help="start all four agents behind public ngrok URLs")
    sub.add_parser("cloudflared", help="start all four agents behind Cloudflare quick tunnels")
    sub.add_parser("preflight", help="check the Bifrost setup")
    p_demo = sub.add_parser("demo", help="run a scenario")
    p_demo.add_argument("scenario", nargs="?", default="")
    p_trace = sub.add_parser("trace", help="per-surface timeline for a session id")
    p_trace.add_argument("session_id")
    args = parser.parse_args(argv)

    if args.cmd in (None, "chat"):
        from revops_copilot.tui import chat

        return asyncio.run(chat())
    if args.cmd == "serve":
        try:
            importlib.import_module(AGENT_MODULES[AgentName(args.agent)]).main()
        except IdentityError as e:
            # Fail at boot, not at the first task: an agent without an identity
            # would pass registration and then fail every call to Bifrost.
            raise SystemExit(f"{args.agent}: {e}. Set it in .env (BIFROST_SETUP.md, step 4).") from None
        return 0
    if args.cmd in ("ngrok", "cloudflared"):
        from revops_copilot.tunnel import run_agents

        return run_agents(args.cmd)
    if args.cmd == "ask":
        return asyncio.run(ask(args.text))
    if args.cmd == "preflight":
        from revops_copilot.preflight import preflight

        return asyncio.run(preflight())
    if args.cmd == "demo":
        from revops_copilot.scenarios import run_scenario

        return asyncio.run(run_scenario(args.scenario))
    if args.cmd == "trace":
        from revops_copilot.trace import trace

        return asyncio.run(trace(args.session_id))
    return 2


if __name__ == "__main__":
    sys.exit(main())
