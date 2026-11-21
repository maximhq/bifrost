"""Plain OpenTelemetry SDK -> /tracing/otel (HTTP protobuf, HTTP JSON) and the OTLP gRPC listener.

Covers each LLM convention that rides on generic OTLP: OTel GenAI, OpenLLMetry (Traceloop), the
Vercel AI SDK and MLflow. Each one must land on the same Bifrost kinds and fields.
"""

from __future__ import annotations

import json
import time

import requests
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter as GRPCSpanExporter
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter as HTTPSpanExporter
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.trace import Status, StatusCode

from conftest import Gateway, by_name, otel_span_id, otel_trace_id, wait_for


def tracer_for(gateway: Gateway, service: str, *, grpc: bool = False, **resource: str):
    provider = TracerProvider(resource=Resource.create({"service.name": service, **resource}))
    if grpc:
        # gRPC metadata keys are lower case.
        headers = {k.lower(): v for k, v in gateway.basic_header.items()}
        exporter = GRPCSpanExporter(endpoint=gateway.grpc_endpoint, insecure=True, headers=headers)
    else:
        exporter = HTTPSpanExporter(endpoint=gateway.url("/tracing/otel/v1/traces"), headers=gateway.basic_header)
    # SimpleSpanProcessor exports each span as it ends, so children reach Bifrost before their root.
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    return provider, provider.get_tracer("bifrost-tracing-tests")


def test_genai_conventions_map_every_operation(gateway: Gateway, run_id: str):
    service = f"otel-genai-{run_id}"
    provider, tracer = tracer_for(gateway, service, **{"deployment.environment": "staging"})
    session = f"otel-session-{run_id}"
    with tracer.start_as_current_span("invoke_agent planner", attributes={"gen_ai.operation.name": "invoke_agent", "gen_ai.conversation.id": session}) as root:
        with tracer.start_as_current_span("create_agent planner", attributes={"gen_ai.operation.name": "create_agent"}):
            pass
        with tracer.start_as_current_span("chat gpt-4o", attributes={
            "gen_ai.operation.name": "chat", "gen_ai.provider.name": "openai", "gen_ai.request.model": "gpt-4o",
            "gen_ai.response.id": f"chatcmpl-{run_id}",
            "gen_ai.input.messages": json.dumps([{"role": "user", "parts": [{"type": "text", "content": "Weather in Oslo?"}]}]),
            "gen_ai.output.messages": json.dumps([{"role": "assistant", "parts": [{"type": "text", "content": "4C"}]}]),
            "gen_ai.usage.input_tokens": 30, "gen_ai.usage.output_tokens": 9, "gen_ai.usage.cache_read_input_tokens": 10,
        }):
            pass
        with tracer.start_as_current_span("text_completion davinci", attributes={"gen_ai.operation.name": "text_completion", "gen_ai.request.model": "davinci-002",
                                                                                  "gen_ai.usage.input_tokens": 5, "gen_ai.usage.output_tokens": 2}):
            pass
        with tracer.start_as_current_span("embeddings text-embedding-3-small", attributes={"gen_ai.operation.name": "embeddings", "gen_ai.request.model": "text-embedding-3-small",
                                                                                            "gen_ai.usage.input_tokens": 7}):
            pass
        with tracer.start_as_current_span("execute_tool", attributes={"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "get_weather",
                                                                      "gen_ai.tool.call.arguments": '{"city": "Oslo"}', "gen_ai.tool.call.result": '{"temp_c": 4}'}):
            pass
        # An older instrumentation: a model call with only gen_ai.system and messages as span events.
        with tracer.start_as_current_span("legacy completion", attributes={"gen_ai.system": "anthropic", "gen_ai.request.model": "claude-sonnet-5-5"}) as legacy:
            legacy.add_event("gen_ai.user.message", {"content": "hi"})
            legacy.add_event("gen_ai.choice", {"message": "hello"})
        trace_id = otel_trace_id(root)
    provider.force_flush()

    project_id = gateway.project_id(service)
    body = gateway.wait_trace(project_id, trace_id, 7)
    spans = by_name(body["spans"])
    assert spans["invoke_agent planner"]["kind"] == "agent"
    assert spans["create_agent planner"]["kind"] == "agent"
    chat = spans["chat gpt-4o"]
    assert chat["kind"] == "llm"
    assert (chat["model"], chat["provider"]) == ("gpt-4o", "openai")
    assert (chat["prompt_tokens"], chat["completion_tokens"], chat.get("cached_read_tokens")) == (30, 9, 10)
    assert chat["provider_response_id"] == f"chatcmpl-{run_id}"
    assert "Weather in Oslo?" in chat["input"] and "4C" in chat["output"]
    assert spans["text_completion davinci"]["kind"] == "llm"
    assert spans["embeddings text-embedding-3-small"]["kind"] == "embedding"
    # A tool span is renamed after its tool, so the tree reads "get_weather", not "execute_tool".
    assert "execute_tool" not in spans
    tool = spans["get_weather"]
    assert tool["kind"] == "tool" and "Oslo" in tool["input"] and "temp_c" in tool["output"]
    legacy = spans["legacy completion"]
    assert legacy["kind"] == "llm" and legacy["provider"] == "anthropic"
    assert "hi" in legacy["input"] and "hello" in legacy["output"]

    trace = body["trace"]
    assert trace["root_span_id"] == otel_span_id(root)
    assert trace["session_id"] == session
    assert trace["environment"] == "staging"
    assert trace["span_count"] == 7
    assert trace["llm_span_count"] >= 3
    assert trace["prompt_tokens"] == 30 + 5 + 7


def test_openllmetry_vercel_and_mlflow_conventions(gateway: Gateway, run_id: str):
    service = f"otel-conventions-{run_id}"
    provider, tracer = tracer_for(gateway, service)
    expected = {
        # OpenLLMetry
        "traceloop workflow": ({"traceloop.span.kind": "workflow", "traceloop.entity.input": '{"q": "x"}',
                                "traceloop.association.properties.session_id": f"tl-{run_id}"}, "chain"),
        "traceloop task": ({"traceloop.span.kind": "task"}, "task"),
        "traceloop agent": ({"traceloop.span.kind": "agent"}, "agent"),
        "traceloop tool": ({"traceloop.span.kind": "tool"}, "tool"),
        # Vercel AI SDK
        "ai.generateText": ({"ai.operationId": "ai.generateText"}, "function"),
        "ai.generateText.doGenerate": ({"ai.operationId": "ai.generateText.doGenerate", "ai.model.id": "gpt-4o-mini", "ai.model.provider": "openai.chat",
                                        "ai.usage.promptTokens": 11, "ai.usage.completionTokens": 3, "ai.response.text": "ok"}, "llm"),
        "ai.embed.doEmbed": ({"ai.operationId": "ai.embed.doEmbed"}, "embedding"),
        # MLflow
        "mlflow chat": ({"mlflow.spanType": '"CHAT_MODEL"', "mlflow.chat.tokenUsage": json.dumps({"input_tokens": 4, "output_tokens": 2, "total_tokens": 6})}, "llm"),
        "mlflow parser": ({"mlflow.spanType": '"PARSER"'}, "function"),
        "mlflow retriever": ({"mlflow.spanType": '"RETRIEVER"'}, "retriever"),
        # OpenInference on the generic endpoint
        "openinference reranker": ({"openinference.span.kind": "RERANKER"}, "reranker"),
        # HTTP client spans are recognised (and hidden by default in the tree)
        "POST /v1/chat/completions": ({"http.request.method": "POST", "url.full": "https://api.openai.com/v1/chat/completions"}, "http"),
    }
    with tracer.start_as_current_span("root", attributes={"traceloop.span.kind": "workflow"}) as root:
        for name, (attributes, _) in expected.items():
            with tracer.start_as_current_span(name, attributes=attributes):
                pass
        trace_id = otel_trace_id(root)
    # The Vercel tool span takes its tool name.
    with tracer.start_as_current_span("ai.toolCall", attributes={"ai.operationId": "ai.toolCall", "ai.toolCall.name": "lookup"}) as tool_root:
        tool_trace = otel_trace_id(tool_root)
    provider.force_flush()

    project_id = gateway.project_id(service)
    spans = by_name(gateway.wait_trace(project_id, trace_id, len(expected) + 1)["spans"])
    for name, (_, kind) in expected.items():
        assert spans[name]["kind"] == kind, f"{name}: {spans[name]['kind']} != {kind}"
    assert spans["traceloop workflow"]["session_id"] == f"tl-{run_id}"
    llm = spans["ai.generateText.doGenerate"]
    assert (llm["model"], llm["prompt_tokens"], llm["completion_tokens"], llm["output"]) == ("gpt-4o-mini", 11, 3, "ok")
    assert spans["mlflow chat"]["total_tokens"] == 6
    tool = gateway.wait_trace(project_id, tool_trace, 1)["spans"][0]
    assert (tool["name"], tool["kind"]) == ("lookup", "tool")


def test_errors_exceptions_and_unknown_spans(gateway: Gateway, run_id: str):
    service = f"otel-errors-{run_id}"
    provider, tracer = tracer_for(gateway, service)
    with tracer.start_as_current_span("pipeline", attributes={"gen_ai.operation.name": "invoke_agent"}) as root:
        with tracer.start_as_current_span("failing tool", attributes={"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "explode"}) as failing:
            failing.set_status(Status(StatusCode.ERROR, "tool timed out"))
        with tracer.start_as_current_span("raising step", attributes={"traceloop.span.kind": "task"}) as raising:
            raising.record_exception(ValueError("bad input"))
        # A span no mapper understands is stored, never dropped.
        with tracer.start_as_current_span("mystery", attributes={"custom.thing": 1}):
            pass
        trace_id = otel_trace_id(root)
    provider.force_flush()

    project_id = gateway.project_id(service)
    body = gateway.wait_trace(project_id, trace_id, 4)
    spans = by_name(body["spans"])
    assert spans["failing tool"]["status"] == "error"
    assert "tool timed out" in spans["failing tool"]["error"]
    assert spans["raising step"]["status"] == "error"
    assert "bad input" in spans["raising step"]["error"]
    assert spans["mystery"]["kind"] == "unknown"
    assert spans["mystery"]["attributes"]["custom.thing"] == 1
    assert body["trace"]["error_count"] == 2

    errors = gateway.traces(project_ids=[project_id], status="error")
    assert [t["trace_id"] for t in errors] == [trace_id]
    stats = gateway.get("/api/traces/stats", project_ids=project_id)
    assert stats["stats"]["error_trace_count"] == 1
    assert stats["stats"]["trace_count"] == 1


def test_children_before_root_shows_in_progress_then_completes(gateway: Gateway, run_id: str):
    service = f"otel-rootless-{run_id}"
    provider, tracer = tracer_for(gateway, service)
    root = tracer.start_span("slow agent", attributes={"gen_ai.operation.name": "invoke_agent"})
    from opentelemetry.trace import set_span_in_context

    context = set_span_in_context(root)
    for index in range(3):
        child = tracer.start_span(f"step {index}", context=context, attributes={"traceloop.span.kind": "task"})
        child.end()
    provider.force_flush()
    trace_id = otel_trace_id(root)
    project_id = gateway.project_id(service)

    # The children are stored, the root is not: the trace is listed and marked in progress.
    partial = gateway.wait_trace(project_id, trace_id, 3, complete=False)
    assert partial["trace"]["in_progress"] is True
    assert all(span["parent_span_id"] == otel_span_id(root) for span in partial["spans"])

    time.sleep(0.2)
    root.end()
    provider.force_flush()
    done = gateway.wait_trace(project_id, trace_id, 4)
    assert done["trace"]["in_progress"] is False
    assert done["trace"]["root_span_id"] == otel_span_id(root)
    assert done["trace"]["name"] == "slow agent"
    assert done["trace"]["duration_ms"] >= 200


def test_grpc_transport(gateway: Gateway, run_id: str):
    service = f"otel-grpc-{run_id}"
    provider, tracer = tracer_for(gateway, service, grpc=True)
    with tracer.start_as_current_span("grpc agent", attributes={"gen_ai.operation.name": "invoke_agent"}) as root:
        with tracer.start_as_current_span("chat", attributes={"gen_ai.operation.name": "chat", "gen_ai.request.model": "gpt-4o", "gen_ai.usage.input_tokens": 3}):
            pass
        trace_id = otel_trace_id(root)
    provider.force_flush()

    project_id = gateway.project_id(service)
    spans = by_name(gateway.wait_trace(project_id, trace_id, 2)["spans"])
    assert spans["grpc agent"]["kind"] == "agent"
    assert spans["chat"]["prompt_tokens"] == 3


def test_otlp_json_body(gateway: Gateway, run_id: str):
    """OTLP over HTTP JSON is what the browser and some JS exporters send."""
    trace_id = run_id.ljust(32, "0")
    now = time.time_ns()
    body = {"resourceSpans": [{
        "resource": {"attributes": [{"key": "service.name", "value": {"stringValue": f"otel-json-{run_id}"}}]},
        "scopeSpans": [{"spans": [
            {"traceId": trace_id, "spanId": "a" * 16, "name": "json root", "startTimeUnixNano": str(now), "endTimeUnixNano": str(now + 5_000_000),
             "attributes": [{"key": "gen_ai.operation.name", "value": {"stringValue": "invoke_agent"}}]},
            {"traceId": trace_id, "spanId": "b" * 16, "parentSpanId": "a" * 16, "name": "json chat",
             "startTimeUnixNano": str(now + 1_000_000), "endTimeUnixNano": str(now + 4_000_000),
             "attributes": [{"key": "gen_ai.operation.name", "value": {"stringValue": "chat"}},
                            {"key": "gen_ai.usage.input_tokens", "value": {"intValue": "12"}}]},
        ]}],
    }]}
    response = requests.post(gateway.url("/tracing/otel/v1/traces"), json=body, headers=gateway.basic_header, timeout=15)
    assert response.status_code == 200, response.text
    assert response.headers["Content-Type"].startswith("application/json")

    project_id = gateway.project_id(f"otel-json-{run_id}")
    spans = by_name(gateway.wait_trace(project_id, trace_id, 2)["spans"])
    assert spans["json chat"]["parent_span_id"] == "a" * 16
    assert spans["json chat"]["prompt_tokens"] == 12


def test_each_service_is_its_own_project(gateway: Gateway, run_id: str):
    ids = {}
    for name in ("alpha", "beta"):
        service = f"otel-{name}-{run_id}"
        provider, tracer = tracer_for(gateway, service)
        with tracer.start_as_current_span(f"{name} root") as root:
            ids[name] = otel_trace_id(root)
        provider.force_flush()
    alpha = gateway.project_id(f"otel-alpha-{run_id}")
    beta = gateway.project_id(f"otel-beta-{run_id}")
    assert alpha != beta
    assert [t["trace_id"] for t in gateway.wait_traces(1, project_ids=[alpha])] == [ids["alpha"]]
    assert [t["trace_id"] for t in gateway.wait_traces(1, project_ids=[beta])] == [ids["beta"]]


def test_sessions_group_traces(gateway: Gateway, run_id: str):
    service = f"otel-sessions-{run_id}"
    session = f"conv-{run_id}"
    provider, tracer = tracer_for(gateway, service)
    for turn in range(3):
        with tracer.start_as_current_span(f"turn {turn}", attributes={"gen_ai.operation.name": "invoke_agent", "gen_ai.conversation.id": session,
                                                                       "user.id": f"user-{run_id}"}):
            with tracer.start_as_current_span("chat", attributes={"gen_ai.operation.name": "chat", "gen_ai.usage.input_tokens": 10, "gen_ai.usage.output_tokens": 1}):
                pass
    provider.force_flush()
    project_id = gateway.project_id(service)
    assert len(gateway.wait_traces(3, project_ids=[project_id], session_id=session)) == 3

    def session_row():
        rows = gateway.get("/api/traces/sessions", project_ids=project_id)["sessions"]
        return next((row for row in rows if row["session_id"] == session and row["trace_count"] == 3), None)

    row = wait_for(session_row, f"session {session}")
    assert row["total_tokens"] == 33
    assert row["user_id"] == f"user-{run_id}"
    assert len(gateway.traces(project_ids=[project_id], user_id=f"user-{run_id}")) == 3


def test_credentials(gateway: Gateway, run_id: str):
    url = gateway.url("/tracing/otel/v1/traces")
    empty = {"resourceSpans": []}
    if gateway.auth_on:
        assert requests.post(url, json=empty, timeout=10).status_code == 401
        wrong = {"Authorization": "Basic " + "d3Jvbmc6d3Jvbmc="}  # wrong:wrong
        assert requests.post(url, json=empty, headers=wrong, timeout=10).status_code == 401
        # Every form a user may paste as an SDK key is accepted.
        for headers in (gateway.basic_header, {"Authorization": f"Bearer {gateway.api_key}"}, {"x-api-key": gateway.api_key}):
            assert requests.post(url, json=empty, headers=headers, timeout=10).status_code == 200, headers.keys()
    else:
        # With dashboard auth off, SDKs need no key at all.
        assert requests.post(url, json=empty, timeout=10).status_code == 200
