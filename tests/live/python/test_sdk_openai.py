"""The OpenAI Python SDK's live client against Bifrost's OpenAI drop-in route."""

import asyncio
import fractions
import json

import openai
import pytest

from conftest import (
    BACKEND_MODEL,
    BACKEND_MODEL_2,
    FRAME_TIMEOUT,
    RESTRICTED_VIRTUAL_KEY,
    UPSTREAM,
    VOICE_MODEL,
    async_microphone,
    await_for,
    error_message,
    event_type,
    fake_only,
    find_content_row,
    find_live_row,
    make_client,
    microphone,
    session_config,
    settle_injection,
    wait_for,
)


def test_websocket_session_lifecycle(client, marker):
    """connect, start, update, steer, close: every command the SDK's session resource sends."""
    with client.live.connect() as connection, microphone(connection):
        connection.session.start(session=session_config(marker), event_id="evt_start")
        started = wait_for(connection, "session.started")
        provider_session_id = started.session.id
        assert provider_session_id, started

        connection.session.update(session={"delegation": {"type": "responses", "responses": {"model": BACKEND_MODEL_2}}}, event_id="switch")
        updated = wait_for(connection, "session.updated")
        assert updated.session.delegation.responses.model == BACKEND_MODEL_2

        connection.session.instructions.append(content="Answer in one short sentence.", delegation_id=None, event_id="steer_1")
        assert wait_for(connection, "session.instructions.appended").client_event_id == "steer_1"
        connection.session.thinking.append(content="The user is in a hurry.", delegation_id=None, event_id="think_1")
        # Closing while an injection is pending is an error on the real upstream; let it land first.
        assert wait_for(connection, "session.thinking.appended").client_event_id == "think_1"
        settle_injection()

        connection.session.close(event_id="bye")
        closed = wait_for(connection, "session.closed")
        assert closed.usage is not None

    row = find_live_row(provider_session_id)
    assert row["object"] == "live.session"
    assert row["status"] == "success"
    assert row["model"] == VOICE_MODEL
    assert row["live_session"]["transport"] == "websocket"


async def test_async_websocket_session(async_client, marker):
    async with async_client.live.connect() as connection, async_microphone(connection):
        await connection.session.start(session=session_config(marker))
        started = await await_for(connection, "session.started")
        await connection.session.close()
        closed = await await_for(connection, "session.closed")
        assert closed.session.id == started.session.id
    assert find_live_row(started.session.id)["live_session"]["transport"] == "websocket"


def test_client_delegation_commentary(client, marker):
    """Under client delegation the app speaks for the backend with session.commentary.append."""
    with client.live.connect() as connection, microphone(connection):
        connection.session.start(session=session_config(marker, backend=None, delegation={"type": "client"}))
        wait_for(connection, "session.started")
        connection.session.commentary.append(content="It is sunny in Paris.", delegation_id=None, event_id="say_1")
        assert wait_for(connection, "session.commentary.appended").client_event_id == "say_1"
        connection.session.close()
        wait_for(connection, "session.closed")


def test_download_recording(client, marker):
    with client.live.connect() as connection, microphone(connection):
        connection.session.start(session=session_config(marker, store=True))
        provider_session_id = wait_for(connection, "session.started").session.id
        connection.session.close()
        wait_for(connection, "session.closed")

    recording = client.live.sessions.download_recording(provider_session_id)
    body = recording.content
    assert body[:4] == b"RIFF", body[:16]
    if UPSTREAM == "fake":
        assert len(body) == 44 + 24000 * 2, "the fake's one second of 24 kHz silence, byte for byte"
    row = find_content_row(provider_session_id)
    assert row["status"] == "success", row
    assert row["provider"] == "openai", row
    assert not row.get("live_session"), "a download is logged on its own, not in the session's row"


def test_unstored_session_has_no_recording(client, marker):
    with client.live.connect() as connection, microphone(connection):
        connection.session.start(session=session_config(marker))
        provider_session_id = wait_for(connection, "session.started").session.id
        connection.session.close()
        wait_for(connection, "session.closed")
    with pytest.raises(openai.NotFoundError):
        client.live.sessions.download_recording(provider_session_id)


def test_create_without_transport_is_a_bad_request(client, marker):
    with pytest.raises(openai.BadRequestError) as refused:
        client.post("/live/sessions", body={"session": session_config(marker)}, cast_to=object)
    assert "transport" in str(refused.value)


def test_anonymous_create_is_unauthorized(marker):
    fake_only("the fake-mode gateway enforces auth; the real profile may not")
    anonymous = make_client(virtual_key="")
    with pytest.raises(openai.AuthenticationError):
        anonymous.live.create(session=session_config(marker, transport="webrtc"), transport={"type": "webrtc", "sdp": "v=0"})


def test_voice_model_refused_by_key_arrives_as_an_error_event(marker):
    fake_only()
    if not RESTRICTED_VIRTUAL_KEY:
        pytest.skip("BIFROST_VK_RESTRICTED is not set")
    restricted = make_client(virtual_key=RESTRICTED_VIRTUAL_KEY)
    with restricted.live.connect() as connection:
        connection.session.start(session=session_config(marker))
        event = connection.recv()
        assert event_type(event) == "error", event
        assert "model" in error_message(event).lower()


# ---- WebRTC: the SDK creates the session, aiortc carries the media and the events ----

aiortc = pytest.importorskip("aiortc", reason="the WebRTC scenario needs aiortc")
av = pytest.importorskip("av")


class Silence(aiortc.MediaStreamTrack):
    """A microphone that says nothing: 20 ms of silence at 48 kHz, as often as a microphone would."""

    kind = "audio"

    def __init__(self):
        super().__init__()
        self._pts = 0

    async def recv(self):
        await asyncio.sleep(0.02)
        frame = av.AudioFrame(format="s16", layout="mono", samples=960)
        for plane in frame.planes:
            plane.update(bytes(plane.buffer_size))
        frame.sample_rate = 48000
        frame.pts = self._pts
        frame.time_base = fractions.Fraction(1, 48000)
        self._pts += 960
        return frame


async def next_event(events: asyncio.Queue, type_: str, timeout: float = FRAME_TIMEOUT) -> dict:
    deadline = asyncio.get_event_loop().time() + timeout
    seen = []
    while True:
        remaining = deadline - asyncio.get_event_loop().time()
        if remaining <= 0:
            pytest.fail(f"no {type_} on the data channel within {timeout}s; saw {seen}")
        event = await asyncio.wait_for(events.get(), remaining)
        seen.append(event.get("type"))
        if event.get("type") == type_:
            return event
        if event.get("type") == "error":
            pytest.fail(f"waiting for {type_}, got error: {error_message(event)}")


async def test_sideband_steers_a_webrtc_session(async_client, marker):
    """client.live.create with an SDP offer, then client.live.sideband.connect to steer it."""
    pc = aiortc.RTCPeerConnection()
    pc.addTrack(Silence())
    channel = pc.createDataChannel("oai-events")
    events: asyncio.Queue = asyncio.Queue()
    channel.on("message", lambda message: events.put_nowait(json.loads(message)))
    await pc.setLocalDescription(await pc.createOffer())  # aiortc gathers ICE before this returns
    try:
        created = await async_client.live.create(session=session_config(marker, transport="webrtc"), transport={"type": "webrtc", "sdp": pc.localDescription.sdp})
        assert created.transport.type == "webrtc"
        await pc.setRemoteDescription(aiortc.RTCSessionDescription(sdp=created.transport.sdp, type="answer"))
        started = await next_event(events, "session.started")
        assert started["session"]["id"] == created.session.id, "the create response names the session the channel joins"

        async with async_client.live.sideband.connect(session_id=created.session.id) as sideband:
            joined = await await_for(sideband, "session.started")
            assert joined.session.id == created.session.id, "a sideband is told which session it joined"

            # An instruction from the sideband is acknowledged on both connections.
            await sideband.send({"type": "session.instructions.append", "event_id": "steer_1", "delegation_id": None, "content": "be brief"})
            assert (await await_for(sideband, "session.instructions.appended")).client_event_id == "steer_1"
            assert (await next_event(events, "session.instructions.appended"))["client_event_id"] == "steer_1"

            # A backend switch from the sideband takes effect on the primary.
            await sideband.send({"type": "session.update", "event_id": "switch", "session": {"delegation": {"type": "responses", "responses": {"model": BACKEND_MODEL_2}}}})
            updated = await next_event(events, "session.updated")
            assert updated["session"]["delegation"]["responses"]["model"] == BACKEND_MODEL_2

        channel.send(json.dumps({"type": "session.close"}))
        await next_event(events, "session.closed")
    finally:
        await pc.close()

    row = find_live_row(created.session.id)
    assert row["live_session"]["transport"] == "webrtc"
    assert row["status"] == "success"
