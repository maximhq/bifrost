"""Unit tests for identity, policy classification and header propagation."""

from __future__ import annotations

import asyncio
import json
from types import SimpleNamespace

import httpx2
import pytest
from pydantic import SecretStr
from langchain_core.messages import HumanMessage

from revops_copilot.common.bifrost import CORRELATION_HEADER, SESSION_HEADER, BifrostSession, TaskScope
from revops_copilot.common.config import AgentName, AgentSettings, AuthMode, GlobalSettings
from revops_copilot.common.identity import (
    EntraCredential,
    IdentityError,
    VirtualKeyCredential,
    credential_for,
)
from revops_copilot.common.policy import Hop, PolicyDenied, Surface, classify

# ------------------------------------------------------------------ identity


class FakeTokenCredential:
    def __init__(self) -> None:
        self.scopes: list[str] = []
        self.closed = False

    def get_token(self, *scopes: str, **_: object) -> SimpleNamespace:
        self.scopes.extend(scopes)
        return SimpleNamespace(token="eyJ.app-only.token", expires_on=0)

    def close(self) -> None:
        self.closed = True


async def test_vk_credential_sends_only_the_vk_header() -> None:
    cred = VirtualKeyCredential("sk-bf-agent")
    assert await cred.auth_headers() == {"x-bf-vk": "sk-bf-agent"}
    assert await cred.bearer() == "sk-bf-agent"


async def test_entra_credential_sends_only_a_bearer_for_the_sso_scope() -> None:
    token = FakeTokenCredential()
    cred = EntraCredential(token, "api://bifrost/.default")  # type: ignore[arg-type]
    headers = await cred.auth_headers()
    assert headers == {"Authorization": "Bearer eyJ.app-only.token"}
    assert "x-bf-vk" not in headers  # Bifrost rejects a token and a VK together
    assert token.scopes == ["api://bifrost/.default"]
    await cred.close()
    assert token.closed


def test_entra_credential_requires_a_scope() -> None:
    with pytest.raises(IdentityError, match="ENTRA_SCOPE"):
        EntraCredential(FakeTokenCredential(), "")  # type: ignore[arg-type]


def test_agent_settings_read_the_agent_prefix(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("SUPPORT_ANALYST_VK", "sk-bf-analyst")
    monkeypatch.setenv("PUBLISHER_VK", "sk-bf-publisher")
    s = AgentSettings.for_agent(AgentName.SUPPORT_ANALYST)
    assert s.vk.get_secret_value() == "sk-bf-analyst"
    assert s.port == 9101
    assert s.public_url == "http://127.0.0.1:9101"


def test_credential_for_vk_mode_requires_the_agent_vk(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("PUBLISHER_VK", raising=False)
    g = GlobalSettings(auth_mode=AuthMode.VK)
    with pytest.raises(IdentityError, match="PUBLISHER_VK"):
        credential_for("PUBLISHER_", g, AgentSettings.for_agent(AgentName.PUBLISHER))


def test_credential_for_entra_mode_requires_a_client_id(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("PUBLISHER_ENTRA_CLIENT_ID", raising=False)
    g = GlobalSettings(auth_mode=AuthMode.ENTRA, entra_scope="api://bifrost/.default")
    with pytest.raises(IdentityError, match="ENTRA_CLIENT_ID"):
        credential_for("PUBLISHER_", g, AgentSettings.for_agent(AgentName.PUBLISHER))


# ------------------------------------------------------------------ policy


@pytest.mark.parametrize(
    ("hop", "status", "body", "phase", "surface"),
    [
        (Hop.A2A, 403, '{"error":{"message":"agent_blocked: no grant"}}', "input", Surface.AGENT_GRANT),
        (Hop.LLM, 403, '{"error":{"message":"model_blocked for this key"}}', "input", Surface.LLM_GOVERNANCE),
        (Hop.LLM, 429, '{"error":{"message":"budget_exceeded"}}', "input", Surface.LLM_GOVERNANCE),
        (Hop.LLM, 400, '{"error":{"type":"guardrail_intervention","message":"blocked"}}', "input", Surface.LLM_INPUT_GUARDRAIL),
        (Hop.LLM, 400, '{"error":{"type":"guardrail_intervention","message":"blocked"}}', "output", Surface.LLM_OUTPUT_GUARDRAIL),
        (Hop.MCP, 0, "guardrail_intervention: PII", "input", Surface.MCP_GUARDRAIL),
        (Hop.MCP, 0, "mcp_tool_blocked: delete_file", "input", Surface.MCP_TOOL_AUTHZ),
        (Hop.MCP, 403, "forbidden", "input", Surface.MCP_DISCOVERY),
        (Hop.LLM, 401, '{"error":{"message":"invalid token"}}', "input", Surface.IDENTITY),
        (Hop.LLM, 403, "workload identity not authorized: abc is not connected", "input", Surface.IDENTITY),
    ],
)
def test_classify_names_the_surface(hop: Hop, status: int, body: str, phase: str, surface: Surface) -> None:
    assert classify(hop, status, body, phase=phase).surface is surface


def test_policy_denied_round_trips_through_task_data() -> None:
    denied = classify(Hop.A2A, 403, '{"error":{"message":"agent_blocked"}}')
    back = PolicyDenied.from_data(json.loads(json.dumps(denied.to_data())))
    assert back == denied


# ------------------------------------------------------------------ LLM wire


def _mock_openai(status: int, body: dict) -> tuple[httpx2.AsyncClient, list[httpx2.Request]]:
    """The OpenAI SDK (3.x) runs on httpx2, so respx cannot see it."""
    calls: list[httpx2.Request] = []

    def handler(request: httpx2.Request) -> httpx2.Response:
        calls.append(request)
        return httpx2.Response(status, json=body)

    return httpx2.AsyncClient(transport=httpx2.MockTransport(handler)), calls


COMPLETION = {
    "id": "x",
    "object": "chat.completion",
    "created": 0,
    "model": "openai/gpt-4o-mini",
    "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
}


class RotatingToken:
    """An Entra-like credential whose token changes on every fetch, as after an expiry."""

    def __init__(self) -> None:
        self.n = 0

    async def bearer(self) -> str:
        self.n += 1
        return f"token-{self.n}"

    async def auth_headers(self) -> dict[str, str]:
        return {"Authorization": f"Bearer {await self.bearer()}"}

    async def close(self) -> None:
        return None


async def test_chat_model_fetches_the_bearer_for_each_request() -> None:
    client, calls = _mock_openai(200, COMPLETION)
    session = BifrostSession(
        agent=AgentName.SUPERVISOR,
        settings=GlobalSettings(bifrost_url="http://bifrost.test"),
        credential=RotatingToken(),  # type: ignore[arg-type]
        scope=TaskScope(context_id="ctx-1", task_id="task-1"),
    )
    llm = await session.chat_model(http_async_client=client)
    await llm.ainvoke([HumanMessage("round 1")])
    await llm.ainvoke([HumanMessage("round 2")])
    # The supervisor reuses one model across rounds; a token fetched once would go stale.
    assert calls[0].headers["authorization"] != calls[1].headers["authorization"]


async def test_a2a_client_refreshes_the_credential_for_each_request(monkeypatch: pytest.MonkeyPatch) -> None:
    import functools

    import httpx

    from revops_copilot.common import bifrost

    seen: list[str] = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen.append(request.headers["authorization"])
        return httpx.Response(200)

    monkeypatch.setattr(bifrost.httpx, "AsyncClient", functools.partial(httpx.AsyncClient, transport=httpx.MockTransport(handler)))
    session = BifrostSession(
        agent=AgentName.SUPERVISOR,
        settings=GlobalSettings(bifrost_url="http://bifrost.test"),
        credential=RotatingToken(),  # type: ignore[arg-type]
        scope=TaskScope(context_id="ctx-1", task_id="task-1"),
    )
    http = await session.http()
    await http.get("http://bifrost.test/agents/a2a/support-analyst")
    await http.get("http://bifrost.test/agents/a2a/report-writer")
    await session.aclose()
    assert seen[0] != seen[1]


async def test_chat_model_sends_identity_and_correlation_headers() -> None:
    client, calls = _mock_openai(200, COMPLETION)
    session = BifrostSession(
        agent=AgentName.REPORT_WRITER,
        settings=GlobalSettings(bifrost_url="http://bifrost.test"),
        credential=VirtualKeyCredential("sk-bf-writer"),
        scope=TaskScope(context_id="ctx-1", task_id="task-1"),
    )
    llm = await session.chat_model(http_async_client=client)
    await llm.ainvoke([HumanMessage("hi")])

    assert str(calls[-1].url) == "http://bifrost.test/v1/chat/completions"
    sent = calls[-1].headers
    assert sent[SESSION_HEADER] == "ctx-1"
    assert sent[CORRELATION_HEADER] == "task-1"
    assert sent["x-bf-vk"] == "sk-bf-writer"
    assert sent["authorization"] == "Bearer sk-bf-writer"


async def test_chat_model_refusal_is_classified() -> None:
    from openai import APIStatusError

    from revops_copilot.common.bifrost import llm_refusal

    client, _ = _mock_openai(403, {"error": {"message": "model_blocked: gpt-5 not allowed"}})
    session = BifrostSession(
        agent=AgentName.REPORT_WRITER,
        settings=GlobalSettings(bifrost_url="http://bifrost.test"),
        credential=VirtualKeyCredential("sk-bf-writer"),
        scope=TaskScope.new(),
    )
    llm = await session.chat_model(http_async_client=client)
    with pytest.raises(APIStatusError) as info:
        await llm.ainvoke([HumanMessage("hi")])
    assert llm_refusal(info.value).surface is Surface.LLM_GOVERNANCE


def test_forwarded_credential_reuses_the_header_bifrost_forwarded() -> None:
    from revops_copilot.common.identity import forwarded_credential

    vk = forwarded_credential({"x-bf-vk": "sk-bf-root", "x-agent-key": "inbound"})
    assert vk is not None
    assert asyncio.run(vk.auth_headers()) == {"x-bf-vk": "sk-bf-root"}
    jwt = forwarded_credential({"authorization": "Bearer eyJ.jwt"})
    assert jwt is not None
    assert asyncio.run(jwt.auth_headers()) == {"authorization": "Bearer eyJ.jwt"}
    assert asyncio.run(jwt.bearer()) == "eyJ.jwt"
    # The inbound key is Bifrost's proof to the agent, never an identity.
    assert forwarded_credential({"x-agent-key": "inbound"}) is None


def test_mcp_refusal_on_an_unread_streamed_response_is_surface_5() -> None:
    import httpx

    from revops_copilot.common.bifrost import _mcp_refusal

    # The MCP client streams: a 403 arrives with its body never read.
    request = httpx.Request("POST", "https://bifrost.test/mcp/not-granted")
    response = httpx.Response(403, request=request, stream=httpx.ByteStream(b"forbidden"))
    error = httpx.HTTPStatusError("403", request=request, response=response)
    denied = _mcp_refusal(BaseExceptionGroup("mcp", [error]))
    assert isinstance(denied, PolicyDenied)
    assert denied.surface is Surface.MCP_DISCOVERY
    assert denied.status == 403


def test_an_upstream_tool_error_with_a_type_is_not_a_policy_refusal() -> None:
    from revops_copilot.common.bifrost import tool_refusal

    def result(text: str) -> SimpleNamespace:
        return SimpleNamespace(content=[SimpleNamespace(type="text", text=text)], isError=True)

    # A typed upstream error goes back to the model as a tool error, not a "Stopped by Bifrost policy".
    assert tool_refusal("drive-get_file", result('{"error": {"type": "not_found", "message": "no such file"}}')) is None
    guardrail = tool_refusal("drive-create_file", result('{"error": {"type": "guardrail_intervention", "message": "x"}}'))
    assert guardrail is not None and guardrail.surface is Surface.MCP_GUARDRAIL


def test_progress_round_trips_through_an_a2a_data_part() -> None:
    from revops_copilot.common.progress import Progress

    p = Progress(agent="support-analyst", kind="tool", title="Pylon-search_issues", surfaces="⑥", elapsed_ms=812)
    assert Progress.from_data(p.to_data()) == p
    assert Progress.from_data({"support_metrics": {}}) is None
    assert p.nested().depth == 1


async def test_step_reports_when_it_starts_and_when_it_ends() -> None:
    from revops_copilot.common.progress import Progress

    events: list[Progress] = []

    async def collect(p: Progress) -> None:
        events.append(p)

    session = BifrostSession(
        agent=AgentName.SUPPORT_ANALYST,
        settings=GlobalSettings(bifrost_url="http://bifrost.test"),
        credential=VirtualKeyCredential("sk-bf-x"),
        scope=TaskScope(context_id="c", task_id="t"),
        emit=collect,
    )
    async with session.step("llm", "decide next step (2/12)", surfaces="③④⑧") as note:
        note["detail"] = "Pylon-search_issues"
    assert [(e.status, e.title) for e in events] == [("start", "decide next step (2/12)"), ("ok", "decide next step (2/12)")]
    assert events[0].elapsed_ms is None and events[1].elapsed_ms is not None
    assert events[1].detail == "Pylon-search_issues"


async def test_a2a_client_asks_for_uncompressed_replies() -> None:
    # Bifrost forwards the caller's Accept-Encoding upstream; with zstd allowed,
    # Cloudflare compresses the agent's reply and Bifrost's Go client cannot decode it.
    session = BifrostSession(
        agent=AgentName.SUPERVISOR,
        settings=GlobalSettings(bifrost_url="http://bifrost.test"),
        credential=VirtualKeyCredential("sk-bf-x"),
        scope=TaskScope(context_id="c", task_id="t"),
    )
    http = await session.http()
    assert http.headers["accept-encoding"] == "identity"
    await session.aclose()


def test_progress_survives_the_protobuf_struct_round_trip() -> None:
    # A2A data parts are protobuf Structs: every number comes back as a float.
    from google.protobuf.json_format import MessageToDict, ParseDict
    from google.protobuf.struct_pb2 import Struct

    from revops_copilot.common.progress import Progress

    p = Progress(agent="a", kind="llm", title="t", depth=2, elapsed_ms=812)
    back = Progress.from_data(MessageToDict(ParseDict(p.to_data(), Struct())))
    assert back == p and isinstance(back.depth, int) and isinstance(back.elapsed_ms, int)


def test_mcp_url_is_bifrosts_main_server_unless_narrowed() -> None:
    session = BifrostSession(
        agent=AgentName.PUBLISHER,
        settings=GlobalSettings(bifrost_url="https://bifrost.test/"),
        credential=VirtualKeyCredential("sk-bf-x"),
        scope=TaskScope(context_id="c", task_id="t"),
    )
    # No trailing slash: Bifrost answers /mcp/ with a 308 redirect.
    assert session.mcp_url() == "https://bifrost.test/mcp"
    assert session.mcp_url("pylon-readonly") == "https://bifrost.test/mcp/pylon-readonly"
    assert GlobalSettings().mcp_slug == ""  # every agent uses /mcp by default


def test_entra_client_secret_credential_builds_without_extra_packages() -> None:
    # Regression: azure-identity's async credentials need aiohttp, which is not a dependency.
    g = GlobalSettings(auth_mode=AuthMode.ENTRA, entra_tenant_id="tenant-1", entra_scope="api://bifrost/.default")
    a = AgentSettings(entra_client_id="client-1", entra_client_secret=SecretStr("not-a-real-secret"))
    cred = credential_for("REVOPS_COPILOT_", g, a)
    assert isinstance(cred, EntraCredential)
