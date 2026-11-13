"""Arize Phoenix (phoenix.otel + OpenInference) -> /tracing/arize.

Phoenix sends the key as "authorization: Bearer <key>" and names the project in the resource
attribute openinference.project.name.
"""

from __future__ import annotations

import json

import requests
from openinference.instrumentation import using_metadata, using_session, using_tags, using_user
from openinference.semconv.trace import OpenInferenceSpanKindValues
from opentelemetry.trace import Status, StatusCode
from phoenix.otel import register

from conftest import Gateway, by_name, otel_span_id, otel_trace_id

# Every OpenInference span kind and the Bifrost kind it must land as.
EXPECTED_KINDS = {
    "TOOL": "tool", "CHAIN": "chain", "LLM": "llm", "RETRIEVER": "retriever", "EMBEDDING": "embedding",
    "AGENT": "agent", "RERANKER": "reranker", "GUARDRAIL": "guardrail", "EVALUATOR": "eval", "PROMPT": "task",
    "DECISION": "function", "UNKNOWN": "unknown",
}


def phoenix_for(gateway: Gateway, project: str, *, batch: bool = False):
    provider = register(
        endpoint=gateway.url("/tracing/arize/v1/traces"), project_name=project, batch=batch,
        set_global_tracer_provider=False, verbose=False, api_key=gateway.api_key if gateway.auth_on else None,
    )
    return provider, provider.get_tracer("bifrost-tracing-tests")


def test_every_span_kind_lands_with_its_kind(gateway: Gateway, run_id: str):
    sdk_kinds = {kind.value for kind in OpenInferenceSpanKindValues}
    assert sdk_kinds <= set(EXPECTED_KINDS), f"unmapped OpenInference kinds: {sdk_kinds - set(EXPECTED_KINDS)}"
    project = f"px-kinds-{run_id}"
    provider, tracer = phoenix_for(gateway, project)
    with tracer.start_as_current_span("root", attributes={"openinference.span.kind": "AGENT"}) as root:
        for kind in sorted(sdk_kinds):
            with tracer.start_as_current_span(f"{kind} span", attributes={"openinference.span.kind": kind}):
                pass
        trace_id = otel_trace_id(root)
    provider.force_flush()

    spans = by_name(gateway.wait_trace(gateway.project_id(project), trace_id, len(sdk_kinds) + 1)["spans"])
    for kind in sdk_kinds:
        assert spans[f"{kind} span"]["kind"] == EXPECTED_KINDS[kind], kind


def test_rag_agent_with_context_attributes(gateway: Gateway, run_id: str):
    project = f"px-rag-{run_id}"
    session = f"px-session-{run_id}"
    provider, tracer = phoenix_for(gateway, project)
    with using_session(session), using_user(f"u-{run_id}"), using_tags(["phoenix", run_id]), using_metadata({"tenant": "acme"}):
        with tracer.start_as_current_span("rag-pipeline", attributes={"openinference.span.kind": "CHAIN", "input.value": "Who wrote Dune?"}) as chain:
            with tracer.start_as_current_span("embed-query", attributes={"openinference.span.kind": "EMBEDDING", "embedding.model_name": "text-embedding-3-small",
                                                                         "llm.token_count.prompt": 4}):
                pass
            with tracer.start_as_current_span("vector-search", attributes={"openinference.span.kind": "RETRIEVER", "input.value": "Who wrote Dune?",
                                                                           "retrieval.documents.0.document.content": "Dune (1965) by Frank Herbert",
                                                                           "retrieval.documents.0.document.score": 0.92}) as retriever:
                retriever.set_attribute("output.value", json.dumps(["Dune (1965) by Frank Herbert"]))
            with tracer.start_as_current_span("rerank", attributes={"openinference.span.kind": "RERANKER", "reranker.model_name": "rerank-v3"}):
                pass
            with tracer.start_as_current_span("ChatCompletion", attributes={
                "openinference.span.kind": "LLM", "llm.model_name": "gpt-4o", "llm.provider": "openai",
                "llm.input_messages.0.message.role": "system", "llm.input_messages.0.message.content": "Answer from context.",
                "llm.input_messages.1.message.role": "user", "llm.input_messages.1.message.content": "Who wrote Dune?",
                "llm.output_messages.0.message.role": "assistant", "llm.output_messages.0.message.content": "Frank Herbert.",
                "llm.token_count.prompt": 18, "llm.token_count.completion": 4, "llm.token_count.total": 22,
                "llm.token_count.prompt_details.cache_read": 6, "llm.cost.total": 0.00015,
            }):
                pass
            with tracer.start_as_current_span("cite", attributes={"openinference.span.kind": "TOOL", "tool.name": "cite", "input.value": '{"doc": 0}'}):
                pass
            chain.set_attribute("output.value", "Frank Herbert.")
            trace_id = otel_trace_id(chain)
            root_id = otel_span_id(chain)
    provider.force_flush()

    body = gateway.wait_trace(gateway.project_id(project), trace_id, 6)
    spans = by_name(body["spans"])
    llm = spans["ChatCompletion"]
    assert (llm["kind"], llm["model"], llm["provider"]) == ("llm", "gpt-4o", "openai")
    assert (llm["prompt_tokens"], llm["completion_tokens"], llm["total_tokens"], llm["cached_read_tokens"]) == (18, 4, 22, 6)
    assert llm["cost"] == 0.00015
    assert "Who wrote Dune?" in llm["input"] and "Frank Herbert." in llm["output"]
    assert spans["embed-query"]["model"] == "text-embedding-3-small"
    assert spans["rerank"]["model"] == "rerank-v3"
    assert "Frank Herbert" in spans["vector-search"]["output"]
    assert all(span["parent_span_id"] == root_id for name, span in spans.items() if name != "rag-pipeline")

    trace = body["trace"]
    assert (trace["session_id"], trace["user_id"]) == (session, f"u-{run_id}")
    assert set(trace["tags"]) >= {"phoenix", run_id}
    assert spans["rag-pipeline"]["metadata"]["tenant"] == "acme"
    assert trace["output"] == "Frank Herbert."
    assert trace["cost"] == 0.00015


def test_errors_and_exceptions(gateway: Gateway, run_id: str):
    project = f"px-errors-{run_id}"
    provider, tracer = phoenix_for(gateway, project)
    with tracer.start_as_current_span("agent", attributes={"openinference.span.kind": "AGENT"}) as root:
        with tracer.start_as_current_span("search", attributes={"openinference.span.kind": "TOOL", "tool.name": "search"}) as tool:
            tool.set_status(Status(StatusCode.ERROR, "rate limited"))
        with tracer.start_as_current_span("guard", attributes={"openinference.span.kind": "GUARDRAIL"}) as guard:
            guard.record_exception(RuntimeError("blocked: PII"))
            guard.set_status(Status(StatusCode.ERROR))
        trace_id = otel_trace_id(root)
    provider.force_flush()

    body = gateway.wait_trace(gateway.project_id(project), trace_id, 3)
    spans = by_name(body["spans"])
    assert spans["search"]["status"] == "error" and "rate limited" in spans["search"]["error"]
    assert spans["guard"]["status"] == "error" and "PII" in spans["guard"]["error"]
    assert body["trace"]["error_count"] == 2


def test_batch_processor_and_project_per_provider(gateway: Gateway, run_id: str):
    """batch=True is what Phoenix advises for production. Each provider names its own project."""
    traces = {}
    for name in ("search", "support"):
        provider, tracer = phoenix_for(gateway, f"px-{name}-{run_id}", batch=True)
        for index in range(3):
            with tracer.start_as_current_span(f"{name} {index}", attributes={"openinference.span.kind": "CHAIN"}) as span:
                traces.setdefault(name, set()).add(otel_trace_id(span))
        provider.force_flush()
    for name, ids in traces.items():
        project_id = gateway.project_id(f"px-{name}-{run_id}")
        assert {t["trace_id"] for t in gateway.wait_traces(3, project_ids=[project_id])} == ids


def test_credentials(gateway: Gateway):
    url = gateway.url("/tracing/arize/v1/traces")
    if gateway.auth_on:
        response = requests.post(url, json={"resourceSpans": []}, headers={"authorization": "Bearer wrong:wrong"}, timeout=10)
        assert response.status_code == 401
    response = requests.post(url, json={"resourceSpans": []}, headers={"authorization": f"Bearer {gateway.api_key}"}, timeout=10)
    assert response.status_code == 200
