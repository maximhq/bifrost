"""Clients for Bifrost's three gateways, scoped to one A2A task.

Every hop an agent makes goes through Bifrost:

* LLM Gateway   POST {BIFROST_URL}/v1/chat/completions
* MCP Gateway   {BIFROST_URL}/mcp/<virtual-mcp-slug>  (streamable HTTP)
* Agent Gateway {BIFROST_URL}/agents/a2a/<agent>/...   (A2A v1.0)

Each request carries the job's identity (one auth header: revops-copilot's own,
or the one Bifrost forwarded to a subagent) plus the
correlation headers Bifrost uses to link Agent Logs to LLM and MCP logs:

* x-bf-session-id           = A2A context id
* x-bf-agent-correlation-id = A2A task id

A `BifrostSession` is built per task and never shared across tasks, so one
task's ids are never attributed to another.
"""

from __future__ import annotations

import time
import uuid
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from dataclasses import dataclass, field
from typing import Any

import httpx
import openai
from langchain_openai import ChatOpenAI
from mcp import ClientSession
from mcp.client.streamable_http import streamablehttp_client

from revops_copilot.common.config import AgentName, GlobalSettings
from revops_copilot.common.identity import Credential
from revops_copilot.common.policy import Hop, PolicyDenied, Surface, classify
from revops_copilot.common.progress import Emit, Progress, circled

SESSION_HEADER = "x-bf-session-id"
CORRELATION_HEADER = "x-bf-agent-correlation-id"


@dataclass(frozen=True)
class TaskScope:
    """The A2A ids of the task an agent is working on."""

    context_id: str
    task_id: str

    @classmethod
    def new(cls) -> TaskScope:
        """A scope for work that does not start from an A2A task (the CLI)."""
        return cls(context_id=str(uuid.uuid4()), task_id=str(uuid.uuid4()))

    def headers(self) -> dict[str, str]:
        return {SESSION_HEADER: self.context_id, CORRELATION_HEADER: self.task_id}


@dataclass
class BifrostSession:
    """Gateway clients for one agent working on one task."""

    agent: AgentName
    settings: GlobalSettings
    credential: Credential
    scope: TaskScope
    # Where this agent's progress goes: an A2A status update inside a task, the
    # terminal in the CLI, nowhere when unset.
    emit: Emit | None = field(default=None, repr=False)
    _http: httpx.AsyncClient | None = field(default=None, init=False, repr=False)

    async def progress(
        self,
        kind: str,
        title: str,
        *,
        detail: str = "",
        surfaces: str = "",
        status: str = "ok",
        elapsed_ms: int | None = None,
    ) -> None:
        """Report one action this agent took."""
        if self.emit is not None:
            await self.emit(
                Progress(self.agent.value, kind, title, detail, surfaces, status, 0, elapsed_ms)
            )

    @asynccontextmanager
    async def step(self, kind: str, title: str, *, surfaces: str = "") -> AsyncIterator[dict[str, str]]:
        """Time an action and report it when it ends; set note["detail"] to add a result.

        A PolicyDenied or any other error is reported as a failed step and re-raised.
        """
        note: dict[str, str] = {"detail": ""}
        # Report the start too: a slow LLM call then shows as running, not as a hang.
        await self.progress(kind, title, surfaces=surfaces, status="start")
        started = time.monotonic()
        try:
            yield note
        except PolicyDenied as denied:
            await self.progress(kind, title, detail=f"refused: {denied.code or denied.status}",
                                surfaces=circled(denied.surface), status="fail",
                                elapsed_ms=int((time.monotonic() - started) * 1000))
            raise
        except Exception as e:
            await self.progress(kind, title, detail=f"{type(e).__name__}: {str(e)[:120]}", surfaces=surfaces,
                                status="fail", elapsed_ms=int((time.monotonic() - started) * 1000))
            raise
        await self.progress(kind, title, detail=note["detail"], surfaces=surfaces,
                            elapsed_ms=int((time.monotonic() - started) * 1000))

    async def headers(self) -> dict[str, str]:
        """Auth header + correlation headers for any Bifrost request."""
        return {**(await self.credential.auth_headers()), **self.scope.headers()}

    # ---------------------------------------------------------------- LLM

    async def chat_model(
        self, *, model: str | None = None, temperature: float = 0.0, **overrides: Any
    ) -> ChatOpenAI:
        """A LangChain chat model pointed at Bifrost's OpenAI-compatible route.

        The OpenAI SDK always sends `Authorization: Bearer <api_key>`, so the
        credential's bearer value is the api key. In VK mode that is the VK
        (Bifrost accepts a prefixed VK as a bearer), in Entra mode the token.
        """
        headers = await self.headers()
        headers.pop("Authorization", None)
        return ChatOpenAI(
            base_url=f"{self.settings.bifrost_url.rstrip('/')}/v1",
            # Called per request: the supervisor reuses this model across rounds, and an
            # Entra token fetched once would expire. VK and forwarded values never change.
            api_key=self.credential.bearer,
            model=model or self.settings.model,
            temperature=temperature,
            default_headers=headers,
            timeout=self.settings.request_timeout_s,
            max_retries=0,  # a refusal is an answer, not a transient failure
            **overrides,
        )

    # ---------------------------------------------------------------- MCP

    def mcp_url(self, slug: str = "") -> str:
        """Bifrost's MCP server: /mcp serves every tool the identity is granted;
        /mcp/<slug> serves one Virtual MCP."""
        base = f"{self.settings.bifrost_url.rstrip('/')}/mcp"
        return f"{base}/{slug}" if slug else base

    def mcp_path(self, slug: str | None = None) -> str:
        """For progress lines: "/mcp" or "/mcp/<slug>"."""
        slug = self.settings.mcp_slug if slug is None else slug
        return f"/mcp/{slug}" if slug else "/mcp"

    @asynccontextmanager
    async def mcp(self, slug: str | None = None) -> AsyncIterator[ClientSession]:
        """An initialized MCP session on one Virtual MCP endpoint.

        Discovery (surface 5) happens here: Bifrost answers 403 for a slug the
        identity has no grant for, and lists only the granted tools.
        """
        slug = self.settings.mcp_slug if slug is None else slug
        headers = await self.headers()
        try:
            async with streamablehttp_client(
                self.mcp_url(slug), headers=headers, timeout=self.settings.request_timeout_s
            ) as (read, write, _), ClientSession(read, write) as session:
                await session.initialize()
                yield session
        except BaseExceptionGroup as group:
            raise _mcp_refusal(group) from group
        except httpx.HTTPStatusError as e:
            raise classify(Hop.MCP, e.response.status_code, _body(e.response)) from e

    # ---------------------------------------------------------------- A2A

    def agent_url(self, target: AgentName) -> str:
        return f"{self.settings.bifrost_url.rstrip('/')}/agents/a2a/{target.value}"

    async def http(self) -> httpx.AsyncClient:
        """An httpx client carrying this task's headers, for A2A calls."""
        if self._http is None:

            async def refresh_auth(request: httpx.Request) -> None:
                # The client outlives one token: an Entra token can expire mid-job.
                request.headers.update(await self.credential.auth_headers())

            # identity: Bifrost forwards the caller's Accept-Encoding to the agent. With
            # zstd or br allowed, a proxy in front of the agent (Cloudflare) compresses
            # the reply, and Bifrost's Go client fails to decode it ("invalid character '('").
            self._http = httpx.AsyncClient(
                headers={**(await self.headers()), "Accept-Encoding": "identity"},
                timeout=self.settings.request_timeout_s,
                event_hooks={"request": [refresh_auth]},
            )
        return self._http

    async def aclose(self) -> None:
        if self._http is not None:
            await self._http.aclose()
            self._http = None


def llm_refusal(exc: openai.APIStatusError, *, phase: str = "input") -> PolicyDenied:
    """Turn an OpenAI SDK error from Bifrost into a PolicyDenied."""
    body = exc.response.text if exc.response is not None else str(exc)
    return classify(Hop.LLM, exc.status_code, body, phase=phase)


def _body(response: httpx.Response) -> str:
    """The error body, or "" when the MCP client streamed it and never read it.

    The status code alone still names the surface (a 403 on a slug is surface 5).
    """
    try:
        return response.text
    except httpx.ResponseNotRead:
        return ""


def _mcp_refusal(group: BaseExceptionGroup) -> Exception:
    """The MCP client wraps HTTP errors in exception groups; find the status."""
    for exc in _walk(group):
        if isinstance(exc, httpx.HTTPStatusError):
            return classify(Hop.MCP, exc.response.status_code, _body(exc.response))
        if isinstance(exc, PolicyDenied):
            return exc
    first = next(iter(_walk(group)), group)
    return first if isinstance(first, Exception) else RuntimeError(str(group))


def _walk(exc: BaseException) -> list[BaseException]:
    if isinstance(exc, BaseExceptionGroup):
        out: list[BaseException] = []
        for inner in exc.exceptions:
            out.extend(_walk(inner))
        return out
    return [exc]


def tool_result_text(result: Any) -> str:
    """Join the text blocks of an MCP CallToolResult."""
    parts = []
    for block in getattr(result, "content", []) or []:
        text = getattr(block, "text", None)
        if text:
            parts.append(text)
    return "\n".join(parts)


def tool_refusal(tool: str, result: Any) -> PolicyDenied | None:
    """A tool result with isError=true is how the MCP gateway reports a
    governance or guardrail refusal of one call (surfaces 6 and 7)."""
    if not getattr(result, "isError", False):
        return None
    denied = classify(Hop.MCP, 0, tool_result_text(result))
    # classify copies any JSON error type into code, so an upstream "not_found" has a code
    # too; only a named surface is a policy refusal. Everything else goes back to the model.
    if denied.surface is not Surface.UNKNOWN:
        return denied
    return None
