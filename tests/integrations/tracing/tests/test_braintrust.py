"""Braintrust SDK -> /tracing/braintrust (login, project and experiment register, logs3).

Braintrust posts rows, not spans: a base row, then merge rows for later span.log() calls, then
feedback rows. Bifrost must fold them into one span whatever order they land in.
"""

from __future__ import annotations

import time

import braintrust
import pytest
from braintrust.span_types import SpanTypeAttribute

from conftest import Gateway, by_name, wait_for

# Every Braintrust span type and the Bifrost kind it must land as.
EXPECTED_KINDS = {
    "llm": "llm", "score": "score", "function": "function", "eval": "eval", "task": "task", "tool": "tool",
    "automation": "task", "facet": "task", "preprocessor": "function", "classifier": "function", "review": "score",
    "decision": "function", "question": "task", "log": "event",
}


def logger_for(gateway: Gateway, project: str):
    app_url = gateway.url("/tracing/braintrust")
    braintrust.login(app_url=app_url, api_key=gateway.api_key, force_login=True)
    return braintrust.init_logger(project=project, app_url=app_url, api_key=gateway.api_key, async_flush=False)


@pytest.fixture(autouse=True)
def no_org_name(monkeypatch):
    # A user's own BRAINTRUST_ORG_NAME would make the SDK look for an org Bifrost does not name.
    monkeypatch.delenv("BRAINTRUST_ORG_NAME", raising=False)


def test_every_span_type_lands_with_its_kind(gateway: Gateway, run_id: str):
    sdk_types = {t.value for t in SpanTypeAttribute}
    # The table above must cover what this SDK release can send; a new type needs a mapping first.
    assert sdk_types <= set(EXPECTED_KINDS), f"unmapped Braintrust span types: {sdk_types - set(EXPECTED_KINDS)}"

    project = f"bt-kinds-{run_id}"
    logger = logger_for(gateway, project)
    with logger.start_span(name="root", type="task") as root:
        for span_type in sorted(sdk_types):
            with root.start_span(name=f"{span_type} span", type=span_type) as child:
                child.log(input={"type": span_type})
        trace_id = root.root_span_id
    logger.flush()

    project_id = gateway.project_id(project)
    spans = by_name(gateway.wait_trace(project_id, trace_id, len(sdk_types) + 1)["spans"])
    for span_type in sdk_types:
        assert spans[f"{span_type} span"]["kind"] == EXPECTED_KINDS[span_type], span_type


def test_agent_tree_with_usage_scores_session_and_tags(gateway: Gateway, run_id: str):
    project = f"bt-agent-{run_id}"
    session = f"bt-session-{run_id}"
    logger = logger_for(gateway, project)
    with logger.start_span(name="answer_question", type="task") as root:
        root.log(input={"question": "Capital of France?"}, metadata={"session_id": session, "user_id": f"u-{run_id}", "environment": "prod"},
                 tags=["braintrust", "agent"])
        with root.start_span(name="plan", type="function") as plan:
            with plan.start_span(name="search_kb", type="tool") as tool:
                time.sleep(0.02)
                tool.log(input={"query": "France capital"}, output={"hits": 3})
        with root.start_span(name="Chat Completion", type="llm") as llm:
            llm.log(
                input=[{"role": "user", "content": "Capital of France?"}],
                output=[{"message": {"role": "assistant", "content": "Paris."}}],
                metadata={"model": "gpt-4o-mini", "provider": "openai"},
                metrics={"prompt_tokens": 24, "completion_tokens": 6, "tokens": 30, "prompt_cached_tokens": 8},
            )
        root.log(output={"answer": "Paris."}, scores={"helpful": 1.0, "concise": 0.5})
        trace_id = root.root_span_id
        root_span_id = root.span_id
    logger.flush()

    project_id = gateway.project_id(project)
    body = gateway.wait_trace(project_id, trace_id, 4)
    spans = by_name(body["spans"])
    assert spans["plan"]["parent_span_id"] == root_span_id
    assert spans["search_kb"]["parent_span_id"] == spans["plan"]["span_id"]
    llm = spans["Chat Completion"]
    assert (llm["kind"], llm["model"], llm["prompt_tokens"], llm["completion_tokens"]) == ("llm", "gpt-4o-mini", 24, 6)
    assert "Paris." in llm["output"]

    trace = body["trace"]
    assert trace["session_id"] == session
    assert trace["user_id"] == f"u-{run_id}"
    assert trace["environment"] == "prod"
    assert set(trace["tags"]) >= {"braintrust", "agent"}
    assert trace["prompt_tokens"] == 24
    scores = {score["name"]: score["value"] for score in body["scores"]}
    assert scores == {"helpful": 1.0, "concise": 0.5}

    assert [t["trace_id"] for t in gateway.traces(project_ids=[project_id], session_id=session)] == [trace_id]
    assert [t["trace_id"] for t in gateway.traces(project_ids=[project_id], tags=["agent"])] == [trace_id]


def test_merge_rows_across_flushes_fold_into_one_span(gateway: Gateway, run_id: str):
    """span.log() after a flush is sent as a separate merge row. The span must hold both halves."""
    project = f"bt-merge-{run_id}"
    logger = logger_for(gateway, project)
    root = logger.start_span(name="long task", type="task")
    root.log(input={"step": 1}, metadata={"phase": "start"}, tags=["first"])
    logger.flush()
    trace_id = root.root_span_id
    project_id = gateway.project_id(project)
    first = gateway.wait_trace(project_id, trace_id, 1, complete=False)["spans"][0]
    assert first["input"] and not first.get("output")

    root.log(output={"done": True}, metadata={"phase": "end", "extra": "x"}, tags=["second"])
    root.end()
    logger.flush()

    def merged():
        span = gateway.detail(project_id, trace_id)["spans"][0]
        return span if span.get("output") and span.get("end_time") else None

    span = wait_for(merged, "the merge row to fold")
    assert '"step"' in span["input"] and '"done"' in span["output"]
    assert span["metadata"]["phase"] == "end" and span["metadata"]["extra"] == "x"
    assert set(span["tags"]) == {"first", "second"}
    assert len(gateway.detail(project_id, trace_id)["spans"]) == 1


def test_errors_feedback_and_comments(gateway: Gateway, run_id: str):
    project = f"bt-feedback-{run_id}"
    logger = logger_for(gateway, project)
    with logger.start_span(name="flaky agent", type="task") as root:
        with root.start_span(name="broken tool", type="tool") as tool:
            tool.log(error="connection reset by peer")
        trace_id = root.root_span_id
        row_id = root.id
    logger.flush()
    # Feedback arrives later and names the row, not the span.
    logger.log_feedback(id=row_id, scores={"correct": 0.0}, comment="answered the wrong city")
    logger.flush()

    project_id = gateway.project_id(project)
    body = gateway.wait_trace(project_id, trace_id, 2)
    tool = by_name(body["spans"])["broken tool"]
    assert tool["status"] == "error" and "connection reset" in tool["error"]
    assert body["trace"]["error_count"] == 1

    def feedback():
        detail = gateway.detail(project_id, trace_id)
        return detail if detail["scores"] and detail["comments"] else None

    detail = wait_for(feedback, "feedback score and comment")
    assert [(s["name"], s["value"]) for s in detail["scores"]] == [("correct", 0.0)]
    assert "wrong city" in detail["comments"][0]["text"]


def test_projects_are_isolated(gateway: Gateway, run_id: str):
    traces = {}
    for name in ("one", "two"):
        logger = logger_for(gateway, f"bt-{name}-{run_id}")
        with logger.start_span(name=f"{name} root", type="task") as root:
            traces[name] = root.root_span_id
        logger.flush()
    for name, trace_id in traces.items():
        project_id = gateway.project_id(f"bt-{name}-{run_id}")
        assert [t["trace_id"] for t in gateway.wait_traces(1, project_ids=[project_id])] == [trace_id]


def test_experiment_rows_and_summary(gateway: Gateway, run_id: str):
    project = f"bt-exp-{run_id}"
    app_url = gateway.url("/tracing/braintrust")
    braintrust.login(app_url=app_url, api_key=gateway.api_key, force_login=True)
    experiment = braintrust.init(project=project, experiment=f"exp-{run_id}", app_url=app_url, api_key=gateway.api_key)
    for question, answer, expected in [("2+2", "4", "4"), ("3+3", "7", "6")]:
        with experiment.start_span(name="eval case", type="eval") as case:
            case.log(input=question, output=answer, expected=expected, scores={"exact": float(answer == expected)})
    summary = experiment.summarize()
    assert summary.experiment_name == f"exp-{run_id}"
    project_id = gateway.project_id(project)
    traces = gateway.wait_traces(2, project_ids=[project_id], experiment_id=experiment.id)
    assert len(traces) == 2
    assert all(t["experiment_id"] == experiment.id for t in traces)


def test_credentials(gateway: Gateway, run_id: str):
    app_url = gateway.url("/tracing/braintrust")
    if gateway.auth_on:
        with pytest.raises(Exception):
            braintrust.login(app_url=app_url, api_key="wrong:wrong", force_login=True)
    else:
        # With dashboard auth off, any key logs in.
        braintrust.login(app_url=app_url, api_key="anything", force_login=True)
    braintrust.login(app_url=app_url, api_key=gateway.api_key, force_login=True)
