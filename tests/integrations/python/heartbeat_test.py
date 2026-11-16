"""
Heartbeat probe: stream gemini-3.1-flash-lite through Bifrost via LangChain for
roughly three minutes and count the SSE heartbeat frames on the wire.

LangChain's SSE decoder ignores comment lines (": heartbeat"), so it cannot see
them. Two streams of the same prompt therefore run side by side:

  raw       - httpx stream of the same request, logs every heartbeat frame with
              a timestamp and checks it uses Gemini's delimited framing
  langchain - ChatGoogleGenerativeAI.stream(), proves the SDK keeps decoding
              content while heartbeats are interleaved

Run from tests/integrations/python with the gateway on localhost:8080:

    uv run python heartbeat_test.py
"""

import os
import threading
import time

import httpx
from langchain_core.messages import HumanMessage
from langchain_google_genai import ChatGoogleGenerativeAI

BASE_URL = os.environ.get("BIFROST_BASE_URL", "http://localhost:8080") + "/langchain"
MODEL = os.environ.get("MODEL", "gemini-3.1-flash-lite")
TARGET_SECONDS = int(os.environ.get("TARGET_SECONDS", "180"))
HARD_TIMEOUT = TARGET_SECONDS + 120
DUMMY_KEY = "dummy-google-api-key-bifrost-handles-auth"

PROMPT = (
    "Write an extremely long, detailed technical book about the history of "
    "computing, one chapter per decade from 1940 to 2020. Every chapter must "
    "have at least ten long paragraphs. Do not stop early, do not summarise, "
    "keep writing until you run out of output budget."
)
MAX_TOKENS = 65536

started = time.monotonic()


def ts() -> str:
    return f"{time.monotonic() - started:7.2f}s"


def raw_stream(result: dict) -> None:
    url = f"{BASE_URL}/v1beta/models/{MODEL}:streamGenerateContent"
    body = {
        "contents": [{"role": "user", "parts": [{"text": PROMPT}]}],
        "generationConfig": {"maxOutputTokens": MAX_TOKENS},
    }
    headers = {"x-goog-api-key": DUMMY_KEY, "Content-Type": "application/json"}
    heartbeats = 0
    data_lines = 0
    bad_frames = 0
    prev_line = None
    last_heartbeat_at = None
    max_gap = 0.0
    try:
        with httpx.Client(timeout=httpx.Timeout(HARD_TIMEOUT, read=HARD_TIMEOUT)) as client:
            with client.stream("POST", url, params={"alt": "sse"}, json=body, headers=headers) as resp:
                result["status"] = resp.status_code
                if resp.status_code != 200:
                    result["error"] = resp.read().decode(errors="replace")[:500]
                    return
                for line in resp.iter_lines():
                    now = time.monotonic()
                    if line.startswith(":"):
                        heartbeats += 1
                        if last_heartbeat_at is not None:
                            max_gap = max(max_gap, now - last_heartbeat_at)
                        last_heartbeat_at = now
                        if heartbeats <= 5 or heartbeats % 20 == 0:
                            print(f"[raw {ts()}] heartbeat #{heartbeats}: {line!r}")
                    elif line.startswith("data:"):
                        data_lines += 1
                        if prev_line is not None and prev_line.startswith(":"):
                            # Gemini framing must be ": heartbeat\n\n" - a data line
                            # directly after the comment means the blank line is missing.
                            bad_frames += 1
                            print(f"[raw {ts()}] WARNING heartbeat not followed by blank line")
                    prev_line = line
                    if now - started > HARD_TIMEOUT:
                        print(f"[raw {ts()}] hard timeout, closing")
                        break
    except Exception as exc:  # noqa: BLE001
        result["error"] = repr(exc)
    result.update(
        heartbeats=heartbeats,
        data_lines=data_lines,
        bad_frames=bad_frames,
        max_gap_between_heartbeats=round(max_gap, 2),
        duration=round(time.monotonic() - started, 1),
    )


def langchain_stream(result: dict) -> None:
    chat = ChatGoogleGenerativeAI(
        model=MODEL,
        google_api_key=DUMMY_KEY,
        max_output_tokens=MAX_TOKENS,
        streaming=True,
        base_url=BASE_URL,
        timeout=HARD_TIMEOUT,
    )
    chunks = 0
    chars = 0
    try:
        for chunk in chat.stream([HumanMessage(content=PROMPT)]):
            chunks += 1
            text = chunk.content if isinstance(chunk.content, str) else str(chunk.content)
            chars += len(text)
            if chunks == 1 or chunks % 50 == 0:
                print(f"[langchain {ts()}] chunk #{chunks}, {chars} chars so far")
            if time.monotonic() - started > HARD_TIMEOUT:
                print(f"[langchain {ts()}] hard timeout, stopping")
                break
    except Exception as exc:  # noqa: BLE001
        result["error"] = repr(exc)
    result.update(chunks=chunks, chars=chars, duration=round(time.monotonic() - started, 1))


def main() -> None:
    print(f"gateway={BASE_URL} model={MODEL} target={TARGET_SECONDS}s")
    raw_result: dict = {}
    lc_result: dict = {}
    threads = [
        threading.Thread(target=raw_stream, args=(raw_result,), name="raw"),
        threading.Thread(target=langchain_stream, args=(lc_result,), name="langchain"),
    ]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    print("\n=== raw wire stream ===")
    for k, v in raw_result.items():
        print(f"  {k}: {v}")
    print("=== langchain stream ===")
    for k, v in lc_result.items():
        print(f"  {k}: {v}")

    ok = True
    if raw_result.get("error") or lc_result.get("error"):
        ok = False
    if raw_result.get("heartbeats", 0) == 0:
        print("FAIL: no heartbeat frames seen on the wire")
        ok = False
    if raw_result.get("bad_frames", 0):
        print("FAIL: heartbeat frames without the trailing blank line")
        ok = False
    if lc_result.get("chunks", 0) == 0:
        print("FAIL: langchain received no chunks")
        ok = False
    if raw_result.get("duration", 0) < TARGET_SECONDS:
        print(f"NOTE: stream ended after {raw_result.get('duration')}s, shorter than the {TARGET_SECONDS}s target; raise MAX_TOKENS or use a slower model for a longer run")
    print("RESULT:", "PASS" if ok else "FAIL")


if __name__ == "__main__":
    main()
