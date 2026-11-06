"""GPT Live through Bifrost, as the OpenAI Python SDK's live client drives it.

The gateway is at BIFROST_BASE_URL and the SDK talks to its OpenAI drop-in route, /openai/v1,
exactly as a customer's app would. The Go suite in the parent folder starts the fake upstream
and mints the virtual keys, then runs this folder with pytest; see sdk_test.go.
"""

import asyncio
import base64
import contextlib
import os
import threading
import time
from datetime import datetime, timedelta, timezone

import httpx
import pytest
from openai import AsyncOpenAI, OpenAI

GATEWAY = os.environ.get("BIFROST_BASE_URL", "http://localhost:8080").rstrip("/")
UPSTREAM = os.environ.get("LIVE_UPSTREAM", "fake")
VIRTUAL_KEY = os.environ.get("BIFROST_VK", "")
# OSS setup lock: /api needs the setup token while dashboard auth is not active; a deployed
# gateway with dashboard auth needs the admin's Authorization header instead.
ADMIN_HEADERS = {"X-Bifrost-Setup-Token": os.environ.get("BIFROST_SETUP_TOKEN", "bifrost-live-setup-token")}
if os.environ.get("BIFROST_ADMIN_AUTH"):
    ADMIN_HEADERS["Authorization"] = os.environ["BIFROST_ADMIN_AUTH"]
RESTRICTED_VIRTUAL_KEY = os.environ.get("BIFROST_VK_RESTRICTED", "")
VOICE_MODEL = os.environ.get("LIVE_VOICE_MODEL", "gpt-live-1")
BACKEND_MODEL = os.environ.get("LIVE_BACKEND_MODEL", "gpt-5.6-luna")
BACKEND_MODEL_2 = os.environ.get("LIVE_BACKEND_MODEL_2", "gpt-5.6-terra")
# The fake answers at once; OpenAI acknowledges an appended instruction only once the model has
# taken it in, which can take most of the real suite's 30 s turn wait.
FRAME_TIMEOUT = 30.0 if UPSTREAM == "real" else 10.0
ROW_TIMEOUT = 15.0


def fake_only(reason: str = "needs the fake upstream (LIVE_UPSTREAM=fake)") -> None:
    if UPSTREAM != "fake":
        pytest.skip(reason)


def credential_headers(virtual_key: str = VIRTUAL_KEY) -> dict[str, str]:
    return {"x-bf-vk": virtual_key} if virtual_key else {}


def make_client(virtual_key: str = VIRTUAL_KEY) -> OpenAI:
    # The SDK's own key never reaches OpenAI: Bifrost swaps in the provider key it selects.
    return OpenAI(base_url=GATEWAY + "/openai/v1", api_key="sk-bifrost-sdk-tests", default_headers=credential_headers(virtual_key))


def make_async_client(virtual_key: str = VIRTUAL_KEY) -> AsyncOpenAI:
    return AsyncOpenAI(base_url=GATEWAY + "/openai/v1", api_key="sk-bifrost-sdk-tests", default_headers=credential_headers(virtual_key))


@pytest.fixture
def client() -> OpenAI:
    return make_client()


@pytest.fixture
def async_client() -> AsyncOpenAI:
    return make_async_client()


@pytest.fixture
def marker(request: pytest.FixtureRequest) -> str:
    # Names the session on the wire, in its instructions, so its log row can be found.
    return f"live-e2e sdk {request.node.name} {time.time_ns()}"


def session_config(marker: str, backend: str | None = BACKEND_MODEL, transport: str = "websocket", **extra):
    audio = {"output": {"voice": "marin"}}
    if transport == "websocket":
        # A WebSocket session declares the PCM its microphone sends; WebRTC negotiates its media.
        audio["format"] = {"type": "audio/pcm", "rate": 24000}
    session = {"model": VOICE_MODEL, "instructions": marker, "audio": audio}
    if backend:
        session["delegation"] = {"type": "responses", "responses": {"model": backend, "tools": [{"type": "web_search"}]}}
    session.update(extra)
    return session


# One 20 ms frame of 24 kHz PCM16 silence: what a microphone sends when nobody speaks.
SILENCE_FRAME = base64.b64encode(bytes(24000 * 2 // 50)).decode()


@contextlib.contextmanager
def microphone(connection):
    """Keeps audio flowing on a WebSocket session: OpenAI ends a session whose microphone stops,
    so every real session streams silence, as the Go client does. The fake needs none."""
    if UPSTREAM != "real":
        yield
        return
    stop = threading.Event()

    def pump():
        while not stop.wait(0.02):
            try:
                connection.session.input_audio.append(audio=SILENCE_FRAME)
            except Exception:
                return

    thread = threading.Thread(target=pump, daemon=True)
    thread.start()
    try:
        yield
    finally:
        stop.set()
        thread.join(timeout=1)


@contextlib.asynccontextmanager
async def async_microphone(connection):
    if UPSTREAM != "real":
        yield
        return

    async def pump():
        while True:
            await asyncio.sleep(0.02)
            try:
                await connection.session.input_audio.append(audio=SILENCE_FRAME)
            except Exception:
                return

    task = asyncio.create_task(pump())
    try:
        yield
    finally:
        task.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await task


def event_type(event) -> str:
    return getattr(event, "type", None) or (event.get("type") if isinstance(event, dict) else "")


def wait_for(connection, type_: str, timeout: float = FRAME_TIMEOUT):
    """Receives until an event of a type arrives; an error event in between fails the test."""
    deadline = time.monotonic() + timeout
    seen = []
    while time.monotonic() < deadline:
        event = connection.recv()
        kind = event_type(event)
        seen.append(kind)
        if kind == type_:
            return event
        if kind == "error":
            pytest.fail(f"waiting for {type_}, got error: {error_message(event)}; saw {seen}")
    pytest.fail(f"no {type_} within {timeout}s; saw {seen}")


def settle_injection() -> None:
    """OpenAI drops a session closed while appended context is still being injected; an
    acknowledgement is not the end of that. The fake has nothing to settle."""
    if UPSTREAM == "real":
        time.sleep(3)


async def await_for(connection, type_: str, timeout: float = FRAME_TIMEOUT):
    deadline = time.monotonic() + timeout
    seen = []
    while time.monotonic() < deadline:
        try:
            event = await asyncio.wait_for(connection.recv(), deadline - time.monotonic())
        except asyncio.TimeoutError:
            break
        kind = event_type(event)
        seen.append(kind)
        if kind == type_:
            return event
        if kind == "error":
            pytest.fail(f"waiting for {type_}, got error: {error_message(event)}; saw {seen}")
    pytest.fail(f"no {type_} within {timeout}s; saw {seen}")


def error_message(event) -> str:
    error = getattr(event, "error", None) or (event.get("error") if isinstance(event, dict) else None)
    if error is None:
        return ""
    return getattr(error, "message", None) or (error.get("message") if isinstance(error, dict) else str(error))


def fetch_log(log_id: str) -> dict:
    row = httpx.get(f"{GATEWAY}/api/logs/{log_id}", headers=ADMIN_HEADERS, timeout=10).raise_for_status().json()
    return row.get("log", row)


# Only rows logged since the suite began are looked at, and each row's payload is read once:
# the list endpoint returns summaries without it, and a remote gateway makes every read count.
SUITE_START = datetime.now(timezone.utc) - timedelta(seconds=5)
_row_owner: dict[str, tuple[str, str]] = {}


def _row_owner_of(log_id: str) -> tuple[str, str]:
    """The provider session a row belongs to: as a session row, and as a download row."""
    if log_id not in _row_owner:
        row = fetch_log(log_id)
        _row_owner[log_id] = (
            (row.get("live_session") or {}).get("provider_session_id") or "",
            (row.get("metadata") or {}).get("provider_session_id") or "",
        )
    return _row_owner[log_id]


def _find_row(objects: str, provider_session_id: str, owner_index: int, timeout: float) -> dict:
    deadline = time.monotonic() + timeout
    while True:
        params = {"objects": objects, "limit": 500, "start_time": SUITE_START.isoformat()}
        listing = httpx.get(f"{GATEWAY}/api/logs", params=params, headers=ADMIN_HEADERS, timeout=10).raise_for_status().json()
        for summary in listing.get("logs", []):
            if _row_owner_of(summary["id"])[owner_index] == provider_session_id:
                return fetch_log(summary["id"])
        if time.monotonic() > deadline:
            pytest.fail(f"no {objects} row for provider session {provider_session_id} within {timeout}s")
        time.sleep(0.3)


def find_live_row(provider_session_id: str, timeout: float = ROW_TIMEOUT) -> dict:
    """The one row a session leaves, found by the provider's session id."""
    return _find_row("live.session", provider_session_id, 0, timeout)


def find_content_row(provider_session_id: str, timeout: float = ROW_TIMEOUT) -> dict:
    """The row a recording download leaves, found by the provider's session id in its metadata."""
    return _find_row("live_content", provider_session_id, 1, timeout)
