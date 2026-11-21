"""Arize AX (arize-otel) -> /tracing/arize over HTTP, and the OTLP gRPC listener (its default transport).

Runs in its own environment (`uv run --extra arize`): arize-otel 0.14.1 only works with
OpenTelemetry releases before 1.36. Arize sends the key in the api_key header and a space_id it
requires, which Bifrost ignores.
"""

from __future__ import annotations

import pytest
from arize.otel import Transport, register

from conftest import Gateway, by_name, otel_trace_id


def arize_for(gateway: Gateway, project: str, transport: Transport):
    endpoint = gateway.url("/tracing/arize/v1/traces") if transport == Transport.HTTP else f"http://{gateway.grpc_endpoint}"
    provider = register(
        space_id="bifrost", api_key=gateway.api_key, project_name=project, endpoint=endpoint, transport=transport,
        set_global_tracer_provider=False, batch=False, verbose=False,
    )
    return provider, provider.get_tracer("bifrost-tracing-tests")


@pytest.mark.parametrize("transport", [Transport.HTTP, Transport.GRPC], ids=["http", "grpc"])
def test_agent_trace(gateway: Gateway, run_id: str, transport: Transport):
    project = f"arize-{transport.name.lower()}-{run_id}"
    provider, tracer = arize_for(gateway, project, transport)
    with tracer.start_as_current_span("classify-ticket", attributes={"openinference.span.kind": "AGENT", "input.value": "My card was charged twice",
                                                                      "session.id": f"ax-{run_id}"}) as agent:
        with tracer.start_as_current_span("lookup_customer", attributes={"openinference.span.kind": "TOOL", "tool.name": "lookup_customer",
                                                                         "input.value": '{"email": "a@b.co"}', "output.value": '{"tier": "gold"}'}):
            pass
        with tracer.start_as_current_span("llm-classify", attributes={"openinference.span.kind": "LLM", "llm.model_name": "gpt-4o-mini",
                                                                      "input.value": "Classify: My card was charged twice", "output.value": "billing",
                                                                      "llm.token_count.prompt": 12, "llm.token_count.completion": 1}):
            pass
        with tracer.start_as_current_span("policy-check", attributes={"openinference.span.kind": "GUARDRAIL"}):
            pass
        agent.set_attribute("output.value", "billing")
        trace_id = otel_trace_id(agent)
    provider.force_flush()

    body = gateway.wait_trace(gateway.project_id(project), trace_id, 4)
    spans = by_name(body["spans"])
    assert [spans[name]["kind"] for name in ("classify-ticket", "lookup_customer", "llm-classify", "policy-check")] == ["agent", "tool", "llm", "guardrail"]
    assert (spans["llm-classify"]["prompt_tokens"], spans["llm-classify"]["output"]) == (12, "billing")
    assert body["trace"]["session_id"] == f"ax-{run_id}"
    assert body["trace"]["output"] == "billing"
