"""In-process A2A round-trip through a fake Agent Gateway.

The fake gateway mounts the agent at /agents/a2a/<name>, checks the caller's
Bifrost credential, adds the agent's inbound key (what runtime_auth does in
Bifrost), and forwards the caller's credential when forward_accepted_credential
is on. This proves the wire contract without a running Bifrost.
"""

from __future__ import annotations

import httpx
import pytest
from a2a.types import a2a_pb2 as pb
from pydantic import SecretStr
from starlette.applications import Starlette
from starlette.responses import JSONResponse
from starlette.routing import Mount

from revops_copilot.common import a2aserver
from revops_copilot.common.a2aserver import FileBlob, TaskInput, TaskOutput, agent_card, build_app, call_agent
from revops_copilot.common.bifrost import BifrostSession, TaskScope
from revops_copilot.common.config import AgentName, AgentSettings, GlobalSettings
from revops_copilot.common.identity import VirtualKeyCredential
from revops_copilot.common.policy import Hop, PolicyDenied, Surface

BASE = "http://bifrost.test"
INBOUND = "bifrost-only-secret"
GRANTED_VK = "sk-bf-supervisor"
AGENT_DOWN_VK = "sk-bf-agent-down"  # the fake gateway answers 502, as when the agent is unreachable
SKILLS = [pb.AgentSkill(id="echo", name="echo", description="echo", tags=["test"])]


def _agent_settings() -> AgentSettings:
    return AgentSettings(
        public_url=f"{BASE}/agents/a2a/report-writer",
        inbound_key=SecretStr(INBOUND),  # no identity of its own: it uses the forwarded one
    )


seen: dict[str, object] = {}


async def fake_run(task: TaskInput) -> TaskOutput:
    seen["scope"] = task.session.scope
    seen["files"] = task.files
    seen["auth"] = await task.session.credential.auth_headers()
    await task.session.progress("tool", "Pylon-search_issues", detail="212 rows", surfaces="⑥")
    if "deny" in task.text:
        raise PolicyDenied(Surface.LLM_GOVERNANCE, Hop.LLM, 403, "model_blocked", "model_blocked")
    if "fail" in task.text:
        return TaskOutput(text="upstream said no", failed=True)
    return TaskOutput(
        text=f"echo: {task.text}",
        files=[FileBlob("q3.pdf", b"%PDF-1.7 fake", "application/pdf")],
        data={"pages": 3},
    )


def _gateway(*, forward: bool = True) -> Starlette:
    g = GlobalSettings(bifrost_url=BASE)
    a = _agent_settings()
    agent_app = build_app(AgentName.REPORT_WRITER, g, a, agent_card(AgentName.REPORT_WRITER, a, "test", SKILLS), fake_run)

    async def gateway(scope, receive, send):  # type: ignore[no-untyped-def]
        headers = dict((k.decode(), v.decode()) for k, v in scope["headers"])
        if headers.get("x-bf-vk") == AGENT_DOWN_VK:
            await JSONResponse({"error": {"message": "upstream connect error"}}, 502)(scope, receive, send)
            return
        if headers.get("x-bf-vk") != GRANTED_VK:
            await JSONResponse({"error": {"message": "agent_blocked: no grant for report-writer"}}, 403)(
                scope, receive, send
            )
            return
        # Bifrost strips the caller's credential, re-adds it only when forwarding
        # is on, and adds the agent's inbound key (runtime_auth).
        scope = dict(scope)
        kept = [(k, v) for k, v in scope["headers"] if k.decode().lower() != "x-bf-vk"]
        if forward:
            kept.append((b"x-bf-vk", GRANTED_VK.encode()))
        scope["headers"] = [*kept, (b"x-agent-key", INBOUND.encode())]
        await agent_app(scope, receive, send)

    return Starlette(routes=[Mount("/agents/a2a/report-writer", app=gateway)])


def _session(vk: str, monkeypatch: pytest.MonkeyPatch, *, forward: bool = True) -> BifrostSession:
    session = BifrostSession(
        agent=AgentName.SUPERVISOR,
        settings=GlobalSettings(bifrost_url=BASE),
        credential=VirtualKeyCredential(vk),
        scope=TaskScope(context_id="ctx-job", task_id="task-supervisor"),
    )
    transport = httpx.ASGITransport(app=_gateway(forward=forward))

    async def http() -> httpx.AsyncClient:
        if session._http is None:
            session._http = httpx.AsyncClient(transport=transport, headers=await session.headers(), base_url=BASE)
        return session._http

    monkeypatch.setattr(session, "http", http)
    return session


async def test_call_through_gateway_round_trips_text_data_and_pdf(monkeypatch: pytest.MonkeyPatch) -> None:
    session = _session(GRANTED_VK, monkeypatch)
    reply = await call_agent(
        session, AgentName.REPORT_WRITER, "write it", files=[FileBlob("in.csv", b"a,b", "text/csv")]
    )
    await session.aclose()

    assert reply.state == "TASK_STATE_COMPLETED"
    assert reply.text == "echo: write it"
    assert reply.data["pages"] == 3
    assert reply.files[0].data == b"%PDF-1.7 fake"
    assert reply.denied is None
    # The subagent's task joins the caller's Bifrost session.
    assert seen["scope"].context_id == "ctx-job"  # type: ignore[attr-defined]
    assert seen["files"][0].name == "in.csv"  # type: ignore[index]
    # The subagent called Bifrost as the caller's identity, forwarded by the gateway.
    assert seen["auth"] == {"x-bf-vk": GRANTED_VK}


async def test_subagent_without_a_forwarded_identity_rejects_at_surface_1(monkeypatch: pytest.MonkeyPatch) -> None:
    session = _session(GRANTED_VK, monkeypatch, forward=False)
    reply = await call_agent(session, AgentName.REPORT_WRITER, "write it")
    await session.aclose()

    assert reply.state == "TASK_STATE_REJECTED"
    assert reply.denied is not None
    assert reply.denied.surface is Surface.IDENTITY
    assert reply.denied.code == "no_forwarded_credential"


async def test_unfinished_work_ends_the_task_failed_not_completed(monkeypatch: pytest.MonkeyPatch) -> None:
    session = _session(GRANTED_VK, monkeypatch)
    reply = await call_agent(session, AgentName.REPORT_WRITER, "please fail")
    await session.aclose()

    assert reply.state == "TASK_STATE_FAILED"
    assert reply.denied is None
    assert "upstream said no" in reply.text


async def test_agent_refusal_comes_back_as_structured_denial(monkeypatch: pytest.MonkeyPatch) -> None:
    session = _session(GRANTED_VK, monkeypatch)
    reply = await call_agent(session, AgentName.REPORT_WRITER, "please deny")
    await session.aclose()

    assert reply.state == "TASK_STATE_REJECTED"
    assert reply.denied is not None
    assert reply.denied.surface is Surface.LLM_GOVERNANCE


async def test_gateway_grant_refusal_is_surface_2(monkeypatch: pytest.MonkeyPatch) -> None:
    session = _session("sk-bf-not-granted", monkeypatch)
    with pytest.raises(PolicyDenied) as info:
        await call_agent(session, AgentName.REPORT_WRITER, "hi")
    await session.aclose()
    assert info.value.surface is Surface.AGENT_GRANT


async def test_unreachable_agent_is_an_error_not_a_policy_denial(monkeypatch: pytest.MonkeyPatch) -> None:
    # A 502 with no policy code means the agent or network is down; reporting it as a denial sends
    # the operator hunting for a policy problem that does not exist.
    session = _session(AGENT_DOWN_VK, monkeypatch)
    with pytest.raises(Exception) as info:
        await call_agent(session, AgentName.REPORT_WRITER, "hi")
    await session.aclose()
    assert not isinstance(info.value, PolicyDenied)


async def test_poll_mode_gives_up_on_a_task_that_never_ends() -> None:
    # Stream mode is bounded by the HTTP timeout; poll mode needs its own deadline or a stalled agent hangs the job.
    import asyncio

    session = BifrostSession(
        agent=AgentName.SUPERVISOR,
        settings=GlobalSettings(bifrost_url=BASE, request_timeout_s=0.2, a2a_poll_interval_s=0.01),
        credential=VirtualKeyCredential(GRANTED_VK),
        scope=TaskScope(context_id="ctx-job", task_id="task-supervisor"),
    )
    working = pb.Task(id="stuck-task", status=pb.TaskStatus(state=pb.TaskState.TASK_STATE_WORKING))

    class StalledAgent:
        async def get_task(self, _: pb.GetTaskRequest) -> pb.Task:
            return working

    with pytest.raises(TimeoutError, match="stuck-task"):
        await asyncio.wait_for(a2aserver._follow(session, StalledAgent(), working), timeout=2)


def test_build_app_refuses_an_empty_inbound_key() -> None:
    # Without the key there is no guard, so anyone who reaches the agent could run work under its identity.
    g = GlobalSettings(bifrost_url=BASE)
    a = AgentSettings(public_url=f"{BASE}/agents/a2a/report-writer", inbound_key=SecretStr(""))
    with pytest.raises(ValueError, match="REPORT_WRITER_INBOUND_KEY"):
        build_app(AgentName.REPORT_WRITER, g, a, agent_card(AgentName.REPORT_WRITER, a, "test", SKILLS), fake_run)


async def test_direct_call_without_inbound_key_is_refused() -> None:
    g = GlobalSettings(bifrost_url=BASE)
    a = _agent_settings()
    app = build_app(AgentName.REPORT_WRITER, g, a, agent_card(AgentName.REPORT_WRITER, a, "test", SKILLS), fake_run)
    async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://agent") as c:
        card = await c.get("/.well-known/agent-card.json")
        assert card.status_code == 200
        assert card.json()["securitySchemes"]["bifrostGateway"]["apiKeySecurityScheme"]["name"] == "x-agent-key"
        # The requirement is advertised (Bifrost's form and its auth/card check need it),
        # with scopes as a JSON array: a2a-go cannot read the Python SDK's {} encoding.
        assert card.json()["securityRequirements"] == [{"schemes": {"bifrostGateway": []}}]
        # Streaming by default: live progress, and no idle connection for a proxy to cut.
        assert card.json()["capabilities"]["streaming"] is True
        r = await c.post("/", json={"jsonrpc": "2.0", "id": 1, "method": "SendMessage", "params": {}})
        assert r.status_code == 401
        assert a2aserver.INBOUND_KEY_HEADER == "x-agent-key"
    # Quick tunnels buffer SSE, so that mode turns streaming off.
    assert agent_card(AgentName.REPORT_WRITER, a, "test", SKILLS, streaming=False).capabilities.streaming is False


@pytest.mark.parametrize("mode", ["stream", "poll"])
async def test_call_relays_subagent_progress_one_level_deeper(monkeypatch: pytest.MonkeyPatch, mode: str) -> None:
    from revops_copilot.common.progress import Progress

    session = _session(GRANTED_VK, monkeypatch)
    session.settings.a2a_progress = mode
    session.settings.a2a_poll_interval_s = 0.05
    events: list[Progress] = []

    async def collect(p: Progress) -> None:
        events.append(p)

    session.emit = collect
    reply = await call_agent(session, AgentName.REPORT_WRITER, "write it")
    await session.aclose()

    assert reply.text == "echo: write it" and reply.files[0].data == b"%PDF-1.7 fake"
    # The caller's own delegation line, then the subagent's step nested under it.
    assert events[0].kind == "delegate" and events[0].depth == 0 and events[0].title == "report-writer"
    tool = next(e for e in events if e.kind == "tool")
    assert (tool.agent, tool.title, tool.surfaces, tool.depth) == ("report-writer", "Pylon-search_issues", "⑥", 1)
    assert events[-1].kind == "delegate" and events[-1].status == "ok"
