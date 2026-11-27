"""A2A v1.0 plumbing shared by every agent.

Server side: `serve()` wraps an async `run(TaskInput) -> TaskOutput` function in
an a2a-sdk AgentExecutor and serves it with uvicorn. Each task gets its own
`BifrostSession`, so the agent's LLM and MCP calls carry that task's ids.

Client side: `call_agent()` sends a message to a peer agent through Bifrost's
Agent Gateway (never directly), so the hop is governed (surface 2) and logged.
"""

from __future__ import annotations

import asyncio
import hmac
import logging
import time
import uuid
from collections.abc import AsyncIterator, Awaitable, Callable
from contextlib import asynccontextmanager
from dataclasses import dataclass, field
from typing import Any

import httpx
import uvicorn
from a2a.client import A2AClientError, ClientConfig, ClientFactory
from a2a.helpers.proto_helpers import (
    get_data_parts,
    get_text_parts,
    new_data_part,
    new_raw_part,
    new_task,
    new_text_part,
)
from a2a.server.agent_execution import AgentExecutor, RequestContext
from a2a.server.events import EventQueue
from a2a.server.request_handlers import DefaultRequestHandler
from a2a.server.request_handlers.response_helpers import agent_card_to_dict
from a2a.server.routes import create_jsonrpc_routes
from a2a.server.tasks import InMemoryTaskStore, TaskUpdater
from a2a.types import a2a_pb2 as pb
from a2a.utils.constants import AGENT_CARD_WELL_KNOWN_PATH, PROTOCOL_VERSION_1_0, TransportProtocol
from google.protobuf.json_format import MessageToDict
from starlette.applications import Starlette
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from revops_copilot.common.bifrost import BifrostSession, TaskScope
from revops_copilot.common.config import AgentName, AgentSettings, GlobalSettings
from revops_copilot.common.identity import Credential, credential_for, forwarded_credential
from revops_copilot.common.policy import Hop, PolicyDenied, Surface, classify
from revops_copilot.common.progress import Progress, circled

log = logging.getLogger(__name__)

INBOUND_KEY_HEADER = "x-agent-key"
RPC_PATH = "/"


@dataclass
class FileBlob:
    name: str
    data: bytes
    media_type: str


@dataclass
class TaskInput:
    text: str
    files: list[FileBlob]
    data: list[dict[str, Any]]
    session: BifrostSession


@dataclass
class TaskOutput:
    text: str = ""
    files: list[FileBlob] = field(default_factory=list)
    data: dict[str, Any] = field(default_factory=dict)
    denied: PolicyDenied | None = None
    # The work did not finish (an upstream error, a failed delegation). The task ends FAILED, not COMPLETED,
    # so a caller never reads it as success.
    failed: bool = False


RunFn = Callable[[TaskInput], Awaitable[TaskOutput]]


# --------------------------------------------------------------------- parts


def _files_of(parts: Any) -> list[FileBlob]:
    return [
        FileBlob(name=p.filename or "file", data=p.raw, media_type=p.media_type or "application/octet-stream")
        for p in parts
        if p.HasField("raw")
    ]


def _output_parts(out: TaskOutput) -> list[pb.Part]:
    parts: list[pb.Part] = []
    if out.text:
        parts.append(new_text_part(out.text))
    data = dict(out.data)
    if out.denied is not None:
        data.update(out.denied.to_data())
    if data:
        parts.append(new_data_part(data))
    for f in out.files:
        parts.append(new_raw_part(f.data, media_type=f.media_type, filename=f.name))
    return parts


# --------------------------------------------------------------------- server


def _no_identity(agent: AgentName) -> PolicyDenied:
    return PolicyDenied(
        surface=Surface.IDENTITY,
        hop=Hop.A2A,
        status=401,
        code="no_forwarded_credential",
        message=f"{agent.value} has no Bifrost identity: turn on 'Forward Bifrost authentication' on its registration",
    )


class GraphExecutor(AgentExecutor):
    """Runs one agent function per A2A task."""

    def __init__(self, agent: AgentName, settings: GlobalSettings, credential: Credential | None, run: RunFn) -> None:
        self._agent = agent
        self._settings = settings
        self._credential = credential
        self._run = run

    async def execute(self, context: RequestContext, event_queue: EventQueue) -> None:
        assert context.task_id and context.context_id, "a2a-sdk assigns ids before execute"
        updater = TaskUpdater(event_queue, context.task_id, context.context_id)
        message = context.message
        if context.current_task is None:
            # A2A requires the Task itself before any status update for it.
            await event_queue.enqueue_event(
                new_task(
                    context.task_id,
                    context.context_id,
                    pb.TASK_STATE_SUBMITTED,
                    history=[message] if message is not None else None,
                )
            )
        await updater.start_work()

        parts = list(message.parts) if message is not None else []
        # The job's identity governs the job: use the credential Bifrost forwarded,
        # else this agent's own (only revops-copilot has one).
        inbound = context.call_context.state.get("headers", {}) if context.call_context else {}
        credential = forwarded_credential(inbound) or self._credential
        if credential is None:
            await self._finish(updater, TaskOutput(text="", denied=_no_identity(self._agent)))
            return
        async def publish(p: Progress) -> None:
            # Each action becomes a WORKING status update the caller streams.
            await updater.update_status(
                pb.TASK_STATE_WORKING,
                message=updater.new_agent_message([new_text_part(p.title), new_data_part(p.to_data())]),
            )

        session = BifrostSession(
            agent=self._agent,
            settings=self._settings,
            credential=credential,
            scope=TaskScope(context_id=context.context_id, task_id=context.task_id),
            emit=publish,
        )
        try:
            out = await self._run(
                TaskInput(
                    text="\n".join(get_text_parts(parts)),
                    files=_files_of(parts),
                    data=[d for d in get_data_parts(parts) if isinstance(d, dict)],
                    session=session,
                )
            )
        except PolicyDenied as denied:
            out = TaskOutput(text=str(denied), denied=denied)
        finally:
            await session.aclose()
        await self._finish(updater, out)

    async def _finish(self, updater: TaskUpdater, out: TaskOutput) -> None:
        await updater.add_artifact(_output_parts(out), name=f"{self._agent.value}-result")
        if out.denied is not None:
            # A refusal is a completed answer, not a crash: the caller decides
            # what to do with it, and Bifrost's logs already hold the decision.
            await updater.reject(updater.new_agent_message([new_text_part(str(out.denied))]))
        elif out.failed:
            await updater.failed(updater.new_agent_message([new_text_part(out.text)]))
        else:
            await updater.complete()

    async def cancel(self, context: RequestContext, event_queue: EventQueue) -> None:
        assert context.task_id and context.context_id
        await TaskUpdater(event_queue, context.task_id, context.context_id).cancel()


def agent_card(
    agent: AgentName,
    settings: AgentSettings,
    description: str,
    skills: list[pb.AgentSkill],
    *,
    streaming: bool = True,
) -> pb.AgentCard:
    card = pb.AgentCard(
        name=agent.value,
        description=description,
        version="0.1.0",
        default_input_modes=["text/plain", "application/json", "application/pdf"],
        default_output_modes=["text/plain", "application/json", "application/pdf"],
        # Streaming carries live progress and keeps proxies from cutting idle
        # requests. Off only behind tunnels that buffer SSE (Cloudflare quick tunnels).
        capabilities=pb.AgentCapabilities(streaming=streaming),
        skills=skills,
        supported_interfaces=[
            pb.AgentInterface(
                url=settings.public_url.rstrip("/") + RPC_PATH,
                protocol_binding=TransportProtocol.JSONRPC,
                protocol_version=PROTOCOL_VERSION_1_0,
            )
        ],
    )
    if settings.inbound_key.get_secret_value():
        card.security_schemes["bifrostGateway"].api_key_security_scheme.CopyFrom(
            pb.APIKeySecurityScheme(
                name=INBOUND_KEY_HEADER,
                location="header",
                description="Set by Bifrost Agent Gateway runtime_auth. Direct calls are refused.",
            )
        )
        card.security_requirements.add().schemes["bifrostGateway"].SetInParent()
    return card


def card_json(card: pb.AgentCard) -> dict[str, Any]:
    """The card as served: protobuf JSON, with security requirement scopes as lists.

    The A2A proto maps each scheme to a StringList message, which this SDK writes as
    {} (or {"list": [...]}). Bifrost's a2a-go types the scopes as []string and
    rejects the object; both SDKs accept a plain list.
    """
    out = agent_card_to_dict(card)
    for requirement in out.get("securityRequirements", []):
        schemes = requirement.get("schemes", {})
        for name, scopes in schemes.items():
            if isinstance(scopes, dict):
                schemes[name] = list(scopes.get("list", []))
    return out


def _card_route(card: pb.AgentCard) -> Route:
    async def serve_card(_: Request) -> Response:
        return JSONResponse(card_json(card))

    return Route(AGENT_CARD_WELL_KNOWN_PATH, serve_card, methods=["GET"])


class InboundKeyMissing(ValueError):
    """The agent has no inbound key, so it cannot tell Bifrost's calls from anyone else's."""


def _inbound_key_guard(expected: str, app: Starlette) -> Callable[..., Awaitable[None]]:
    """ASGI middleware: only Bifrost (which holds the key) may send work."""

    async def middleware(scope: dict[str, Any], receive: Any, send: Any) -> None:
        if scope["type"] == "http" and scope.get("method") == "POST":
            request = Request(scope)
            got = request.headers.get(INBOUND_KEY_HEADER, "")
            if not hmac.compare_digest(got.encode(), expected.encode()):
                response: Response = JSONResponse(
                    {"error": "this agent only accepts work through the Bifrost Agent Gateway"},
                    status_code=401,
                )
                await response(scope, receive, send)
                return
        await app(scope, receive, send)

    return middleware


def build_app(
    agent: AgentName,
    global_settings: GlobalSettings,
    agent_settings: AgentSettings,
    card: pb.AgentCard,
    run: RunFn,
) -> Any:
    # Only the entry agent holds an identity; subagents use the one Bifrost forwards.
    credential = (
        credential_for(agent.env_prefix, global_settings, agent_settings) if agent is AgentName.SUPERVISOR else None
    )
    handler = DefaultRequestHandler(
        agent_executor=GraphExecutor(agent, global_settings, credential, run),
        task_store=InMemoryTaskStore(),
        agent_card=card,
    )
    @asynccontextmanager
    async def lifespan(_: Starlette) -> AsyncIterator[None]:
        yield
        if credential is not None:
            await credential.close()

    app = Starlette(
        routes=[
            _card_route(card),
            *create_jsonrpc_routes(handler, rpc_url=RPC_PATH),
        ],
        lifespan=lifespan,
    )
    key = agent_settings.inbound_key.get_secret_value()
    if not key:
        # No key means no guard: anyone who reaches the agent could run work under its identity.
        raise InboundKeyMissing(f"{agent.value}: set {agent.env_prefix}INBOUND_KEY; the agent refuses to start without it")
    return _inbound_key_guard(key, app)


def serve(agent: AgentName, description: str, skills: list[pb.AgentSkill], run: RunFn) -> None:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s")
    g = GlobalSettings()
    a = AgentSettings.for_agent(agent)
    card = agent_card(agent, a, description, skills, streaming=g.a2a_streaming)
    log.info("%s listening on %s (card %s%s)", agent.value, a.public_url, a.public_url, AGENT_CARD_WELL_KNOWN_PATH)
    uvicorn.run(build_app(agent, g, a, card, run), host=a.host, port=a.port, log_level="info")


# --------------------------------------------------------------------- client


@dataclass
class AgentReply:
    text: str
    files: list[FileBlob]
    data: dict[str, Any]
    denied: PolicyDenied | None
    state: str


async def call_agent(
    session: BifrostSession,
    target: AgentName,
    text: str,
    *,
    files: list[FileBlob] | None = None,
    data: dict[str, Any] | None = None,
) -> AgentReply:
    """Send one message to `target` through the Agent Gateway and wait for the
    final task. The caller's context id is reused so the whole job is one
    Bifrost session."""
    parts: list[pb.Part] = [new_text_part(text)]
    if data:
        parts.append(new_data_part(data))
    for f in files or []:
        parts.append(new_raw_part(f.data, media_type=f.media_type, filename=f.name))
    request = pb.SendMessageRequest(
        message=pb.Message(
            message_id=str(uuid.uuid4()),
            context_id=session.scope.context_id,
            role=pb.ROLE_USER,
            parts=parts,
        )
    )

    http = await session.http()
    poll = session.settings.a2a_progress == "poll"
    streaming = session.settings.a2a_streaming and not poll
    factory = ClientFactory(ClientConfig(streaming=streaming, httpx_client=http))
    await session.progress("delegate", target.value, detail="A2A via Bifrost Agent Gateway", status="start")
    started = time.monotonic()
    task = pb.Task()
    reply_message: pb.Message | None = None
    try:
        client = await factory.create_from_url(session.agent_url(target))
        if poll:
            request.configuration.return_immediately = True
        async for event in client.send_message(request):
            if event.HasField("task"):
                task.CopyFrom(event.task)
            elif event.HasField("message"):
                reply_message = event.message
            elif event.HasField("status_update"):
                task.status.CopyFrom(event.status_update.status)
                relayed = _progress_of(event.status_update.status.message)
                if relayed is not None and session.emit is not None:
                    await session.emit(relayed.nested())
            elif event.HasField("artifact_update"):
                _merge_artifact(task, event.artifact_update.artifact)
        if poll and task.id:
            task = await _follow(session, client, task)
    except A2AClientError as e:
        denied = _a2a_refusal(e)
        if denied.surface is Surface.UNKNOWN:
            # No policy code and no policy status: the agent or the network failed, not Bifrost policy.
            await session.progress("delegate", target.value, detail=f"error: {denied.status or e}",
                                   status="fail", elapsed_ms=_ms(started))
            raise
        await session.progress("delegate", target.value, detail=f"refused: {denied.code or denied.status}",
                               surfaces=circled(denied.surface), status="fail", elapsed_ms=_ms(started))
        raise denied from e

    reply = _reply_from(task if task.ByteSize() else None, reply_message)
    await session.progress(
        "delegate", target.value,
        detail=reply.text.splitlines()[0][:160] if reply.text else reply.state,
        surfaces=circled(reply.denied.surface) if reply.denied else "",
        status="fail" if reply.denied else "ok",
        elapsed_ms=_ms(started),
    )
    return reply


TERMINAL = {
    pb.TASK_STATE_COMPLETED, pb.TASK_STATE_FAILED, pb.TASK_STATE_CANCELED,
    pb.TASK_STATE_REJECTED, pb.TASK_STATE_INPUT_REQUIRED, pb.TASK_STATE_AUTH_REQUIRED,
}


async def _follow(session: BifrostSession, client: Any, task: pb.Task) -> pb.Task:
    """Poll a non-blocking task until it ends, relaying each new progress message.

    The agent keeps every earlier status message in the task history, so nothing
    is lost between polls; message ids keep each event to one relay.
    """
    seen: set[str] = set()
    # Stream mode is bounded by the HTTP timeout; polling needs the same bound or a stalled agent hangs the job.
    deadline = time.monotonic() + session.settings.request_timeout_s
    while True:
        for message in [*task.history, task.status.message]:
            if not message.message_id or message.message_id in seen:
                continue
            seen.add(message.message_id)
            relayed = _progress_of(message)
            if relayed is not None and session.emit is not None:
                await session.emit(relayed.nested())
        if task.status.state in TERMINAL:
            return task
        if time.monotonic() >= deadline:
            raise TimeoutError(
                f"task {task.id} still {pb.TaskState.Name(task.status.state)} after "
                f"{session.settings.request_timeout_s:g}s (REQUEST_TIMEOUT_S)"
            )
        await asyncio.sleep(session.settings.a2a_poll_interval_s)
        task = await client.get_task(pb.GetTaskRequest(id=task.id))


def _ms(started: float) -> int:
    return int((time.monotonic() - started) * 1000)


def _progress_of(message: pb.Message) -> Progress | None:
    for d in get_data_parts(list(message.parts)):
        if isinstance(d, dict) and (p := Progress.from_data(d)) is not None:
            return p
    return None


def _merge_artifact(task: pb.Task, artifact: pb.Artifact) -> None:
    """Streaming delivers artifacts as updates; fold them into the task."""
    for existing in task.artifacts:
        if existing.artifact_id and existing.artifact_id == artifact.artifact_id:
            existing.parts.extend(artifact.parts)
            return
    task.artifacts.append(artifact)


def _a2a_refusal(e: A2AClientError) -> PolicyDenied:
    """The SDK wraps the gateway's HTTP error; the body is on the cause."""
    cause: BaseException | None = e
    while cause is not None:
        if isinstance(cause, httpx.HTTPStatusError):
            return classify(Hop.A2A, cause.response.status_code, cause.response.text)
        cause = cause.__cause__
    status = getattr(e, "status_code", 0) or 0
    body = str(e) if e.data is None else f"{e} {e.data}"
    return classify(Hop.A2A, int(status), body)


def _reply_from(task: pb.Task | None, message: pb.Message | None) -> AgentReply:
    parts: list[pb.Part] = []
    state = "unknown"
    if task is not None:
        state = pb.TaskState.Name(task.status.state)
        for artifact in task.artifacts:
            parts.extend(artifact.parts)
    if message is not None:
        parts.extend(message.parts)
    data: dict[str, Any] = {}
    for d in get_data_parts(parts):
        if isinstance(d, dict):
            data.update(d)
    return AgentReply(
        text="\n".join(get_text_parts(parts)),
        files=_files_of(parts),
        data=data,
        denied=PolicyDenied.from_data(data),
        state=state,
    )


def task_json(task: pb.Task) -> dict[str, Any]:
    """For debugging: a task as JSON."""
    return MessageToDict(task)
