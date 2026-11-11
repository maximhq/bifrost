"""The OpenAI Python SDK's live client against Bifrost's OpenAI drop-in route."""

import asyncio
import fractions
import json
import time

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


def test_download_recording_streams(client, marker):
    """The SDK's streaming download: chunks arrive through the gateway instead of one body."""
    with client.live.connect() as connection, microphone(connection):
        connection.session.start(session=session_config(marker, store=True))
        provider_session_id = wait_for(connection, "session.started").session.id
        if UPSTREAM == "real":
            # A session closed at once records an empty WAV; let the microphone's silence land first.
            time.sleep(4)
        connection.session.close()
        wait_for(connection, "session.closed")

    with client.live.sessions.with_streaming_response.download_recording(provider_session_id) as response:
        assert response.http_response.status_code == 200
        chunks = list(response.iter_bytes(chunk_size=4096))
    assert chunks and chunks[0][:4] == b"RIFF", chunks[:1]
    total = sum(len(chunk) for chunk in chunks)
    if UPSTREAM == "fake":
        assert total == 44 + 24000 * 2, "the fake's one second of 24 kHz silence, chunk by chunk"
    assert len(chunks) > 1, "the recording arrived in more than one chunk"


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


def silence_track():
    """A microphone that says nothing: 20 ms of silence at 48 kHz, as often as a microphone would.

    aiortc is imported here, not at module level, so a machine without it skips only the WebRTC
    scenario and still runs the WebSocket and auth tests."""
    aiortc = pytest.importorskip("aiortc", reason="the WebRTC scenario needs aiortc")
    av = pytest.importorskip("av")

    class Silence(aiortc.MediaStreamTrack):
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

    return aiortc, Silence()


async def next_event(events: asyncio.Queue, type_: str, timeout: float = FRAME_TIMEOUT, pc=None, answer_sdp: str = "") -> dict:
    deadline = asyncio.get_event_loop().time() + timeout
    seen = []
    while True:
        remaining = deadline - asyncio.get_event_loop().time()
        if remaining <= 0:
            break
        try:
            event = await asyncio.wait_for(events.get(), remaining)
        except TimeoutError:
            break
        seen.append(event.get("type"))
        if event.get("type") == type_:
            return event
        if event.get("type") == "error":
            pytest.fail(f"waiting for {type_}, got error: {error_message(event)}")
    # ICE to a remote gateway fails more often than the session does: name the connection state
    # and the gateway's candidates, as the Go client does.
    state = f"; peer connection {pc.connectionState}, ice {pc.iceConnectionState}" if pc is not None else ""
    candidates = " | ".join(sdp_candidates(answer_sdp))
    pytest.fail(f"no {type_} on the data channel within {timeout}s; saw {seen}{state}; gateway candidates: {candidates}")


def sdp_candidates(sdp: str) -> list[str]:
    """The ICE candidates an SDP carries, as "type ip:port" strings."""
    out = []
    for line in sdp.splitlines():
        fields = line.strip().split()
        if fields and fields[0].startswith("a=candidate:") and len(fields) >= 8:
            out.append(f"{fields[7]} {fields[4]}:{fields[5]}")
    return out


async def test_sideband_steers_a_webrtc_session(async_client, marker):
    """client.live.create with an SDP offer, then client.live.sideband.connect to steer it."""
    aiortc, microphone_track = silence_track()
    pc = aiortc.RTCPeerConnection()
    pc.addTrack(microphone_track)
    channel = pc.createDataChannel("oai-events")
    events: asyncio.Queue = asyncio.Queue()
    channel.on("message", lambda message: events.put_nowait(json.loads(message)))
    await pc.setLocalDescription(await pc.createOffer())  # aiortc gathers ICE before this returns
    try:
        created = await async_client.live.create(session=session_config(marker, transport="webrtc"), transport={"type": "webrtc", "sdp": pc.localDescription.sdp})
        assert created.transport.type == "webrtc"
        await pc.setRemoteDescription(aiortc.RTCSessionDescription(sdp=created.transport.sdp, type="answer"))
        started = await next_event(events, "session.started", pc=pc, answer_sdp=created.transport.sdp)
        assert started["session"]["id"] == created.session.id, "the create response names the session the channel joins"

        async with async_client.live.sideband.connect(session_id=created.session.id) as sideband:
            # OpenAI tells only the connections present at the start about session.started; a
            # sideband that joins moments later is not replayed it. The ack of its first command
            # proves it joined the right session, since the primary sees the same ack.
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
