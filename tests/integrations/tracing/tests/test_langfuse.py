"""Langfuse SDK (v5, OTLP export) -> /tracing/langfuse.

The SDK sends observations over OTLP, scores as a JSON batch to /api/public/scores, and checks its
keys against /api/public/projects. The public key is the dashboard username, the secret key the
password. The key alone decides the project, so traces are found by trace id.
"""

from __future__ import annotations

import datetime as dt
import time

import pytest
import requests
from langfuse import Langfuse, propagate_attributes

from conftest import Gateway, by_name, wait_for

# Each observation type the SDK can open and the Bifrost kind it must land as.
OBSERVATION_KINDS = {
    "span": "task", "generation": "llm", "agent": "agent", "tool": "tool", "chain": "chain",
    "retriever": "retriever", "evaluator": "eval", "embedding": "embedding", "guardrail": "guardrail",
}


def client_for(gateway: Gateway, *, secret: str | None = None) -> Langfuse:
    return Langfuse(
        public_key=gateway.username if gateway.auth_on else "pk-unused",
        secret_key=secret if secret is not None else (gateway.password if gateway.auth_on else "sk-unused"),
        base_url=gateway.url("/tracing/langfuse"),
        flush_at=1,
    )


@pytest.fixture(scope="module")
def langfuse(gateway: Gateway) -> Langfuse:
    client = client_for(gateway)
    yield client
    client.shutdown()


def locate(gateway: Gateway, trace_id: str, span_count: int) -> tuple[str, dict]:
    """Finds the project the trace landed in, then waits for its spans."""
    rows = gateway.wait_traces(1, trace_id=trace_id)
    project_id = rows[0]["project_id"]
    return project_id, gateway.wait_trace(project_id, trace_id, span_count)


def test_every_observation_type_lands_with_its_kind(gateway: Gateway, langfuse: Langfuse, run_id: str):
    with langfuse.start_as_current_observation(as_type="agent", name=f"lf-root-{run_id}") as root:
        for as_type in OBSERVATION_KINDS:
            with langfuse.start_as_current_observation(as_type=as_type, name=f"{as_type} obs", input={"type": as_type}):
                pass
        langfuse.create_event(name="checkpoint", input={"at": "middle"})
        trace_id = root.trace_id
    langfuse.flush()

    _, body = locate(gateway, trace_id, len(OBSERVATION_KINDS) + 2)
    spans = by_name(body["spans"])
    for as_type, kind in OBSERVATION_KINDS.items():
        assert spans[f"{as_type} obs"]["kind"] == kind, as_type
    assert spans["checkpoint"]["kind"] == "event"
    assert spans[f"lf-root-{run_id}"]["kind"] == "agent"


def test_generation_usage_cost_and_trace_attributes(gateway: Gateway, langfuse: Langfuse, run_id: str):
    session = f"lf-session-{run_id}"
    with langfuse.start_as_current_observation(as_type="span", name="travel-planner", input={"request": "3 days in Lisbon"}) as root, \
            propagate_attributes(user_id=f"u-{run_id}", session_id=session, tags=["langfuse", run_id], metadata={"team": "growth"}):
        with langfuse.start_as_current_observation(as_type="retriever", name="find-places", input={"city": "Lisbon"}) as retriever:
            retriever.update(output=["Alfama", "Belem", "Sintra"])
        started = dt.datetime.now(dt.timezone.utc)
        with langfuse.start_as_current_observation(
            as_type="generation", name="draft-itinerary", model="claude-sonnet-5-5",
            input=[{"role": "user", "content": "Plan 3 days in Lisbon"}], model_parameters={"temperature": 0.2},
        ) as generation:
            time.sleep(0.05)
            generation.update(
                output="Day 1 Alfama, Day 2 Belem, Day 3 Sintra.",
                usage_details={"input": 41, "output": 12, "cache_read_input_tokens": 20},
                cost_details={"total": 0.0042},
                completion_start_time=started + dt.timedelta(milliseconds=30),
            )
        root.update(output={"itinerary": "done"})
        trace_id = root.trace_id
    langfuse.flush()

    _, body = locate(gateway, trace_id, 3)
    spans = by_name(body["spans"])
    generation = spans["draft-itinerary"]
    assert (generation["kind"], generation["model"]) == ("llm", "claude-sonnet-5-5")
    assert (generation["prompt_tokens"], generation["completion_tokens"], generation["cached_read_tokens"]) == (41, 12, 20)
    assert generation["cost"] == pytest.approx(0.0042)
    assert not generation.get("cost_estimated")
    assert generation["time_to_first_token_ms"] == pytest.approx(30, abs=15)
    assert "Alfama" in spans["find-places"]["output"]

    trace = body["trace"]
    assert trace["session_id"] == session
    assert trace["user_id"] == f"u-{run_id}"
    assert set(trace["tags"]) >= {"langfuse", run_id}
    assert trace["cost"] == pytest.approx(0.0042)
    assert spans["travel-planner"]["metadata"]["team"] == "growth"


def test_error_level_marks_the_span(gateway: Gateway, langfuse: Langfuse, run_id: str):
    with langfuse.start_as_current_observation(as_type="chain", name=f"lf-error-{run_id}") as root:
        with langfuse.start_as_current_observation(as_type="tool", name="flaky-tool") as tool:
            tool.update(level="ERROR", status_message="upstream 503")
        with langfuse.start_as_current_observation(as_type="guardrail", name="pii-check") as guard:
            guard.update(level="WARNING", output={"redacted": 0})
        trace_id = root.trace_id
    langfuse.flush()

    _, body = locate(gateway, trace_id, 3)
    spans = by_name(body["spans"])
    assert spans["flaky-tool"]["status"] == "error"
    assert spans["flaky-tool"]["error"] == "upstream 503"
    assert spans["pii-check"].get("status") != "error"
    assert body["trace"]["error_count"] == 1


def test_scores_of_every_data_type(gateway: Gateway, langfuse: Langfuse, run_id: str):
    with langfuse.start_as_current_observation(as_type="agent", name=f"lf-scores-{run_id}") as root:
        with langfuse.start_as_current_observation(as_type="generation", name="answer") as generation:
            observation_id = generation.id
        langfuse.score_current_trace(name="quality", value=0.9, comment="good")
        trace_id = root.trace_id
    langfuse.create_score(name="tone", value="friendly", data_type="CATEGORICAL", trace_id=trace_id, observation_id=observation_id)
    langfuse.create_score(name="grounded", value=1, data_type="BOOLEAN", trace_id=trace_id)
    langfuse.create_score(name="notes", value="cites two sources", data_type="TEXT", trace_id=trace_id)
    langfuse.flush()

    project_id, _ = locate(gateway, trace_id, 2)

    def all_scores():
        scores = gateway.detail(project_id, trace_id)["scores"]
        return scores if len(scores) >= 4 else None

    scores = {score["name"]: score for score in wait_for(all_scores, "four scores")}
    assert scores["quality"]["value"] == pytest.approx(0.9) and scores["quality"]["comment"] == "good"
    assert scores["tone"]["string_value"] == "friendly" and scores["tone"]["span_id"] == observation_id
    assert scores["grounded"]["value"] == 1
    assert scores["notes"]["string_value"] == "cites two sources"
    assert all(score["source"] == "sdk" for score in scores.values())


def test_sessions_group_traces(gateway: Gateway, langfuse: Langfuse, run_id: str):
    session = f"lf-chat-{run_id}"
    trace_ids = []
    for turn in range(2):
        with langfuse.start_as_current_observation(as_type="agent", name=f"turn {turn}") as root, propagate_attributes(session_id=session):
            with langfuse.start_as_current_observation(as_type="generation", name="reply") as generation:
                generation.update(usage_details={"input": 5, "output": 5})
            trace_ids.append(root.trace_id)
    langfuse.flush()

    traces = gateway.wait_traces(2, session_id=session)
    assert sorted(t["trace_id"] for t in traces) == sorted(trace_ids)
    project_id = traces[0]["project_id"]

    def session_row():
        rows = gateway.get("/api/traces/sessions", project_ids=project_id, session_id=session)["sessions"]
        return next((row for row in rows if row["session_id"] == session and row["trace_count"] == 2), None)

    assert wait_for(session_row, f"session {session}")["total_tokens"] == 20


def test_trace_url_redirects_to_bifrost(gateway: Gateway, langfuse: Langfuse, run_id: str):
    with langfuse.start_as_current_observation(as_type="span", name=f"lf-link-{run_id}") as root:
        trace_id = root.trace_id
    langfuse.flush()
    url = langfuse.get_trace_url(trace_id=trace_id)
    assert url and url.startswith(gateway.url("/tracing/langfuse/project/"))
    response = requests.get(url, allow_redirects=False, timeout=10)
    assert response.status_code in (301, 302, 303, 307, 308)
    assert trace_id in response.headers["Location"]
    assert "/workspace/tracing" in response.headers["Location"]


def test_credentials(gateway: Gateway, langfuse: Langfuse):
    assert langfuse.auth_check() is True
    if gateway.auth_on:
        wrong = client_for(gateway, secret="wrong-password")
        try:
            with pytest.raises(Exception):
                wrong.auth_check()
        finally:
            wrong.shutdown()
