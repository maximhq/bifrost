package otel

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

func makeSpan(id, parentID, name string, kind schemas.SpanKind) *schemas.Span {
	return &schemas.Span{
		SpanID:    id,
		ParentID:  parentID,
		Name:      name,
		Kind:      kind,
		StartTime: time.Now(),
		EndTime:   time.Now(),
	}
}

// TestConvertTraceToResourceSpan_PluginSpanFilter exercises the OpenTelemetry export's end-to-end
// filtering behavior (the parts unique to this package; the filter/reparent logic itself is
// covered by core/schemas/span_filter_test.go). It asserts that filtered plugin spans are
// dropped from the exported ResourceSpan and that an exported child whose direct parent was
// filtered is re-parented to the nearest exported ancestor.
func TestConvertTraceToResourceSpan_PluginSpanFilter(t *testing.T) {
	p := &OtelPlugin{pluginSpanFilter: &PluginSpanFilter{
		Mode:    PluginSpanFilterModeExclude,
		Plugins: []string{"logging"},
	}}

	// Span tree: root (internal) -> logging.prehook (filtered) -> governance.prehook (kept).
	root := makeSpan("aaaa", "", "request", schemas.SpanKindInternal)
	trace := &schemas.Trace{
		TraceID:  "00000000000000000000000000000001",
		RootSpan: root,
		Spans: []*schemas.Span{
			root,
			makeSpan("bbbb", "aaaa", "plugin.logging.prehook", schemas.SpanKindPlugin),
			makeSpan("cccc", "bbbb", "plugin.governance.prehook", schemas.SpanKindPlugin),
		},
	}

	rs := p.convertTraceToResourceSpan("svc", trace, nil, TraceTypeGenAIExtension, false)
	spans := rs.ScopeSpans[0].Spans

	// The filtered logging span is dropped; root + governance remain.
	if len(spans) != 2 {
		t.Fatalf("expected 2 exported spans (logging dropped), got %d", len(spans))
	}
	byID := make(map[string]*Span, len(spans))
	for _, s := range spans {
		byID[string(s.SpanId)] = s
	}
	if _, ok := byID[string(hexToBytes("bbbb", 8))]; ok {
		t.Error("filtered logging span should not be exported")
	}
	gov, ok := byID[string(hexToBytes("cccc", 8))]
	if !ok {
		t.Fatal("governance span should be exported")
	}
	// governance's direct parent (logging) was filtered, so its parent must be rewritten to
	// the nearest exported ancestor (root), not left dangling at the dropped logging span.
	if !bytes.Equal(gov.ParentSpanId, hexToBytes("aaaa", 8)) {
		t.Errorf("governance ParentSpanId = %x, want %x (reparented to root)", gov.ParentSpanId, hexToBytes("aaaa", 8))
	}
}

func TestConvertTraceToResourceSpan_ContentLogging(t *testing.T) {
	const (
		input  = `[{"role":"user","content":"hello"}]`
		output = `[{"role":"assistant","content":"hi"}]`
	)

	span := makeSpan("aaaa", "", "chat test-model", schemas.SpanKindLLMCall)
	span.Attributes = map[string]any{
		schemas.AttrInputMessages:  input,
		schemas.AttrOutputMessages: output,
		schemas.AttrRequestModel:   "test-model",
	}
	trace := &schemas.Trace{
		TraceID:  "00000000000000000000000000000001",
		RootSpan: span,
		Spans:    []*schemas.Span{span},
	}
	p := &OtelPlugin{}

	t.Run("exports content by default", func(t *testing.T) {
		exported := p.convertTraceToResourceSpan("svc", trace, nil, TraceTypeGenAIExtension, false).ScopeSpans[0].Spans[0]
		attrs := otelStringAttributes(exported.Attributes)

		if got := attrs[schemas.AttrInputMessages]; got != input {
			t.Errorf("%s = %q, want %q", schemas.AttrInputMessages, got, input)
		}
		if got := attrs[schemas.AttrOutputMessages]; got != output {
			t.Errorf("%s = %q, want %q", schemas.AttrOutputMessages, got, output)
		}
	})

	t.Run("filters content when disabled", func(t *testing.T) {
		exported := p.convertTraceToResourceSpan("svc", trace, nil, TraceTypeGenAIExtension, true).ScopeSpans[0].Spans[0]
		attrs := otelStringAttributes(exported.Attributes)

		if _, ok := attrs[schemas.AttrInputMessages]; ok {
			t.Errorf("%s should not be exported", schemas.AttrInputMessages)
		}
		if _, ok := attrs[schemas.AttrOutputMessages]; ok {
			t.Errorf("%s should not be exported", schemas.AttrOutputMessages)
		}
		if got := attrs[schemas.AttrRequestModel]; got != "test-model" {
			t.Errorf("%s = %q, want test-model", schemas.AttrRequestModel, got)
		}
	})
}

func TestConvertTraceToResourceSpan_OpenInference(t *testing.T) {
	const input = `[{"role":"user","content":"weather in Paris?"}]`
	const output = `[{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","name":"weather","args":"{\"city\":\"Paris\"}"}]}]`

	root := makeSpan("aaaa", "", "request", schemas.SpanKindHTTPRequest)
	llm := makeSpan("bbbb", "aaaa", "chat test-model", schemas.SpanKindLLMCall)
	llm.Attributes = map[string]any{
		schemas.AttrBifrostProviderName:    "openai",
		schemas.AttrRequestModel:           "test-model",
		schemas.AttrInputMessages:          input,
		schemas.AttrOutputMessages:         output,
		schemas.AttrInputTokens:            12,
		schemas.AttrOutputTokens:           4,
		schemas.AttrTotalTokens:            16,
		schemas.AttrTemperature:            0.2,
		schemas.AttrTools:                  `[{"name":"weather","description":"compact"}]`,
		schemas.AttrBifrostToolDefinitions: `[{"type":"function","name":"weather","description":"Get weather","parameters":{"type":"object"}}]`,
	}
	trace := &schemas.Trace{
		TraceID:  "00000000000000000000000000000001",
		RootSpan: root,
		Spans:    []*schemas.Span{root, llm},
	}
	trace.SetAttribute(schemas.TraceAttrSessionID, "session-1")

	p := &OtelPlugin{}
	spans := p.convertTraceToResourceSpan("svc", trace, nil, TraceTypeOpenInference, false).ScopeSpans[0].Spans
	rootAttrs := otelAttributes(spans[0].Attributes)
	llmAttrs := otelAttributes(spans[1].Attributes)

	assertOTELStringAttribute(t, rootAttrs, openInferenceSpanKind, "CHAIN")
	assertOTELStringAttribute(t, rootAttrs, "session.id", "session-1")
	assertOTELStringAttribute(t, llmAttrs, openInferenceSpanKind, "LLM")
	assertOTELStringAttribute(t, llmAttrs, "session.id", "session-1")
	assertOTELStringAttribute(t, llmAttrs, "llm.provider", "openai")
	assertOTELStringAttribute(t, llmAttrs, "llm.system", "openai")
	assertOTELStringAttribute(t, llmAttrs, "llm.model_name", "test-model")
	assertOTELStringAttribute(t, llmAttrs, "llm.input_messages.0.message.role", "user")
	assertOTELStringAttribute(t, llmAttrs, "llm.input_messages.0.message.content", "weather in Paris?")
	assertOTELStringAttribute(t, llmAttrs, "llm.output_messages.0.message.tool_calls.0.tool_call.id", "call-1")
	assertOTELStringAttribute(t, llmAttrs, "llm.output_messages.0.message.tool_calls.0.tool_call.function.name", "weather")
	assertOTELStringAttribute(t, llmAttrs, "llm.tools.0.tool.json_schema", `{"description":"Get weather","name":"weather","parameters":{"type":"object"},"type":"function"}`)
	assertOTELStringAttribute(t, llmAttrs, oiInputMIMEType, "application/json")
	assertOTELStringAttribute(t, llmAttrs, oiOutputMIMEType, "application/json")

	if got := llmAttrs["llm.token_count.prompt"].GetIntValue(); got != 12 {
		t.Errorf("llm.token_count.prompt = %d, want 12", got)
	}
	if got := llmAttrs["llm.token_count.completion"].GetIntValue(); got != 4 {
		t.Errorf("llm.token_count.completion = %d, want 4", got)
	}
	if got := llmAttrs["llm.invocation_parameters"].GetStringValue(); got != `{"model":"test-model","temperature":0.2}` {
		t.Errorf("llm.invocation_parameters = %q, want model and temperature JSON", got)
	}
	if _, ok := rootAttrs["llm.model_name"]; ok {
		t.Error("CHAIN root span should not carry LLM-specific attributes")
	}
}

func TestConvertTraceToResourceSpan_OpenInferenceContentLoggingDisabled(t *testing.T) {
	span := makeSpan("aaaa", "", "chat test-model", schemas.SpanKindLLMCall)
	span.Attributes = map[string]any{
		schemas.AttrRequestModel:   "test-model",
		schemas.AttrInputMessages:  `[{"role":"user","content":"secret"}]`,
		schemas.AttrOutputMessages: `[{"role":"assistant","content":"secret response"}]`,
	}
	trace := &schemas.Trace{
		TraceID:  "00000000000000000000000000000001",
		RootSpan: span,
		Spans:    []*schemas.Span{span},
	}

	p := &OtelPlugin{}
	attrs := otelAttributes(p.convertTraceToResourceSpan("svc", trace, nil, TraceTypeOpenInference, true).ScopeSpans[0].Spans[0].Attributes)

	assertOTELStringAttribute(t, attrs, openInferenceSpanKind, "LLM")
	assertOTELStringAttribute(t, attrs, "llm.model_name", "test-model")
	for key := range attrs {
		if strings.HasPrefix(key, "llm.input_messages") || strings.HasPrefix(key, "llm.output_messages") || key == oiInputValue || key == oiOutputValue {
			t.Errorf("content attribute %s should not be exported", key)
		}
	}
}

func TestConvertTraceToResourceSpan_OpenInferenceExceptionAttributes(t *testing.T) {
	t.Run("uses explicit error attributes", func(t *testing.T) {
		span := makeSpan("aaaa", "", "chat test-model", schemas.SpanKindLLMCall)
		span.Status = schemas.SpanStatusError
		span.StatusMsg = "status fallback"
		span.Attributes = map[string]any{
			schemas.AttrError:         "provider rejected request",
			schemas.AttrErrorTypeSpec: "rate_limit",
		}
		trace := &schemas.Trace{TraceID: "00000000000000000000000000000001", RootSpan: span, Spans: []*schemas.Span{span}}

		attrs := otelAttributes((&OtelPlugin{}).convertTraceToResourceSpan("svc", trace, nil, TraceTypeOpenInference, true).ScopeSpans[0].Spans[0].Attributes)
		assertOTELStringAttribute(t, attrs, "exception.message", "provider rejected request")
		assertOTELStringAttribute(t, attrs, "exception.type", "rate_limit")
	})

	t.Run("falls back to status message", func(t *testing.T) {
		span := makeSpan("aaaa", "", "chat test-model", schemas.SpanKindLLMCall)
		span.Status = schemas.SpanStatusError
		span.StatusMsg = "request failed"
		trace := &schemas.Trace{TraceID: "00000000000000000000000000000001", RootSpan: span, Spans: []*schemas.Span{span}}

		attrs := otelAttributes((&OtelPlugin{}).convertTraceToResourceSpan("svc", trace, nil, TraceTypeOpenInference, true).ScopeSpans[0].Spans[0].Attributes)
		assertOTELStringAttribute(t, attrs, "exception.message", "request failed")
		if _, ok := attrs["exception.type"]; ok {
			t.Error("exception.type should not be inferred when no error type is available")
		}
	})
}

func TestConvertTraceToResourceSpan_GenAIProfileDoesNotAddOpenInferenceAttributes(t *testing.T) {
	span := makeSpan("aaaa", "", "chat test-model", schemas.SpanKindLLMCall)
	span.Attributes = map[string]any{
		schemas.AttrRequestModel: "test-model",
		"http.method":            "POST",
	}
	trace := &schemas.Trace{
		TraceID:  "00000000000000000000000000000001",
		RootSpan: span,
		Spans:    []*schemas.Span{span},
	}

	p := &OtelPlugin{}
	attrs := otelAttributes(p.convertTraceToResourceSpan("svc", trace, nil, TraceTypeGenAIExtension, false).ScopeSpans[0].Spans[0].Attributes)
	if _, ok := attrs[openInferenceSpanKind]; ok {
		t.Errorf("%s should only be exported for open_inference profiles", openInferenceSpanKind)
	}
	assertOTELStringAttribute(t, attrs, schemas.AttrRequestModel, "test-model")
	assertOTELStringAttribute(t, attrs, "http.method", "POST")
}

func TestConvertTraceToResourceSpan_OpenInferenceExportsCleanAttributes(t *testing.T) {
	span := makeSpan("aaaa", "", "chat test-model", schemas.SpanKindLLMCall)
	span.Attributes = map[string]any{
		schemas.AttrBifrostProviderName: "openai",
		schemas.AttrRequestModel:        "test-model",
		schemas.AttrInputMessages:       `[{"role":"user","content":"hello"}]`,
		schemas.AttrBifrostRequestID:    "request-1",
		"http.method":                   "POST",
		"custom.attribute":              "value",
	}
	trace := &schemas.Trace{
		TraceID:        "00000000000000000000000000000001",
		RequestHeaders: map[string]string{"x-request-tag": "tag"},
		RootSpan:       span,
		Spans:          []*schemas.Span{span},
	}

	p := &OtelPlugin{instanceAttrs: []*KeyValue{kvStr("service.instance.id", "instance-1")}}
	attrs := otelAttributes(p.convertTraceToResourceSpan("svc", trace, []string{"x-request-tag"}, TraceTypeOpenInference, false).ScopeSpans[0].Spans[0].Attributes)

	assertOTELStringAttribute(t, attrs, openInferenceSpanKind, "LLM")
	assertOTELStringAttribute(t, attrs, "llm.model_name", "test-model")
	assertOTELStringAttribute(t, attrs, "llm.input_messages.0.message.content", "hello")

	for _, key := range []string{
		schemas.AttrBifrostProviderName,
		schemas.AttrRequestModel,
		schemas.AttrInputMessages,
		schemas.AttrBifrostRequestID,
		"http.method",
		"http.request.header.x-request-tag",
		"service.instance.id",
		"custom.attribute",
	} {
		if _, ok := attrs[key]; ok {
			t.Errorf("non-OpenInference attribute %s should not be exported", key)
		}
	}
}

func TestConvertTraceToResourceSpan_OpenInferenceOmitsImplementationSpans(t *testing.T) {
	root := makeSpan("aaaa", "", "request", schemas.SpanKindHTTPRequest)
	plugin := makeSpan("bbbb", "aaaa", "plugin.governance.prehook", schemas.SpanKindPlugin)
	internal := makeSpan("cccc", "bbbb", "key.selection", schemas.SpanKindInternal)
	llm := makeSpan("dddd", "cccc", "chat test-model", schemas.SpanKindLLMCall)
	llm.Attributes = map[string]any{
		schemas.AttrRequestModel:   "test-model",
		schemas.AttrInputMessages:  `[{"role":"user","content":"hello"}]`,
		schemas.AttrOutputMessages: `[{"role":"assistant","content":"hi"}]`,
	}
	trace := &schemas.Trace{
		TraceID:  "00000000000000000000000000000001",
		RootSpan: root,
		Spans:    []*schemas.Span{root, plugin, internal, llm},
	}

	p := &OtelPlugin{}
	spans := p.convertTraceToResourceSpan("svc", trace, nil, TraceTypeOpenInference, false).ScopeSpans[0].Spans
	if len(spans) != 2 {
		t.Fatalf("OpenInference spans = %d, want root and LLM only", len(spans))
	}
	if spans[0].Name != "request" || spans[1].Name != "chat test-model" {
		t.Fatalf("OpenInference span names = [%q, %q], want [request, chat test-model]", spans[0].Name, spans[1].Name)
	}
	if got := spans[1].GetParentSpanId(); string(got) != string(hexToBytes(root.SpanID, 8)) {
		t.Errorf("LLM parent span ID was not reparented to root")
	}

	llmAttrs := otelAttributes(spans[1].Attributes)
	assertOTELStringAttribute(t, llmAttrs, "llm.input_messages.0.message.content", "hello")
	assertOTELStringAttribute(t, llmAttrs, "llm.output_messages.0.message.content", "hi")
	assertOTELStringAttribute(t, llmAttrs, oiInputValue, `[{"role":"user","content":"hello"}]`)
	assertOTELStringAttribute(t, llmAttrs, oiOutputValue, `[{"role":"assistant","content":"hi"}]`)
}

func TestOpenInferenceKind(t *testing.T) {
	trace := &schemas.Trace{}
	tests := map[schemas.SpanKind]string{
		schemas.SpanKindLLMCall:       "LLM",
		schemas.SpanKindEmbedding:     "EMBEDDING",
		schemas.SpanKindMCPTool:       "TOOL",
		schemas.SpanKindPlugin:        "CHAIN",
		schemas.SpanKindHTTPRequest:   "CHAIN",
		schemas.SpanKindInternal:      "CHAIN",
		schemas.SpanKindRetry:         "CHAIN",
		schemas.SpanKindFallback:      "CHAIN",
		schemas.SpanKindSpeech:        "LLM",
		schemas.SpanKindTranscription: "LLM",
	}

	for kind, want := range tests {
		if got := openInferenceKind(trace, &schemas.Span{Kind: kind}); got != want {
			t.Errorf("openInferenceKind(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestOpenInferenceKindExecuteTool(t *testing.T) {
	span := &schemas.Span{
		Kind: schemas.SpanKindMCPClient,
		Attributes: map[string]any{
			schemas.AttrOperationName: schemas.OTelOperationNameExecuteTool,
		},
	}
	if got := openInferenceKind(&schemas.Trace{}, span); got != "TOOL" {
		t.Errorf("openInferenceKind(execute tool) = %q, want TOOL", got)
	}
}

func TestOpenInferenceKindAgent(t *testing.T) {
	root := &schemas.Span{
		SpanID: "aaaa",
		Kind:   schemas.SpanKindHTTPRequest,
		Attributes: map[string]any{
			schemas.AttrBifrostAgentMode: true,
		},
	}
	trace := &schemas.Trace{
		TraceID:  "00000000000000000000000000000001",
		RootSpan: root,
		Spans:    []*schemas.Span{root},
	}

	if got := openInferenceKind(trace, root); got != "AGENT" {
		t.Errorf("openInferenceKind(agent root) = %q, want AGENT", got)
	}
	p := &OtelPlugin{}
	attrs := otelAttributes(p.convertTraceToResourceSpan("svc", trace, nil, TraceTypeOpenInference, false).ScopeSpans[0].Spans[0].Attributes)
	assertOTELStringAttribute(t, attrs, openInferenceSpanKind, "AGENT")
	attrs = otelAttributes(p.convertTraceToResourceSpan("svc", trace, nil, TraceTypeGenAIExtension, false).ScopeSpans[0].Spans[0].Attributes)
	if _, ok := attrs[schemas.AttrBifrostAgentMode]; ok {
		t.Error("internal agent marker should not be exported by the OTEL profile")
	}

	llmRoot := &schemas.Span{
		Kind: schemas.SpanKindLLMCall,
		Attributes: map[string]any{
			schemas.AttrBifrostAgentMode: true,
		},
	}
	trace.RootSpan = llmRoot
	if got := openInferenceKind(trace, llmRoot); got != "LLM" {
		t.Errorf("openInferenceKind(agent-marked LLM root) = %q, want LLM", got)
	}
}

func TestOpenInferenceProviderAndSystem(t *testing.T) {
	tests := map[string][2]string{
		"openai":        {"openai", "openai"},
		"bedrock":       {"bedrock", "amazon"},
		"gcp.vertex_ai": {"gcp.vertex_ai", "vertexai"},
		"azure":         {"azure", "openai"},
		"mistral":       {"mistral", "mistralai"},
	}

	for raw, want := range tests {
		provider, system := openInferenceProviderAndSystem(map[string]any{schemas.AttrBifrostProviderName: raw})
		if provider != want[0] || system != want[1] {
			t.Errorf("openInferenceProviderAndSystem(%q) = (%q, %q), want (%q, %q)", raw, provider, system, want[0], want[1])
		}
	}

	provider, system := openInferenceProviderAndSystem(map[string]any{
		schemas.AttrProviderName:        "aws.bedrock",
		schemas.AttrBifrostProviderName: "bedrock",
	})
	if provider != "aws.bedrock" || system != "amazon" {
		t.Errorf("canonical provider preferred = (%q, %q), want (%q, %q)", provider, system, "aws.bedrock", "amazon")
	}
}

func TestConvertTraceToResourceSpan_OpenInferenceEmbedding(t *testing.T) {
	span := makeSpan("aaaa", "", "embedding test-model", schemas.SpanKindEmbedding)
	span.Attributes = map[string]any{
		schemas.AttrBifrostProviderName: "openai",
		schemas.AttrRequestModel:        "test-model",
		schemas.AttrInputEmbedding:      []string{"hello", "world"},
		schemas.AttrInputTokens:         2,
		schemas.AttrTotalTokens:         2,
		schemas.AttrDimensions:          1536,
	}
	trace := &schemas.Trace{
		TraceID:  "00000000000000000000000000000001",
		RootSpan: span,
		Spans:    []*schemas.Span{span},
	}

	p := &OtelPlugin{}
	exported := p.convertTraceToResourceSpan("svc", trace, nil, TraceTypeOpenInference, false).ScopeSpans[0].Spans[0]
	attrs := otelAttributes(exported.Attributes)

	assertOTELStringAttribute(t, attrs, openInferenceSpanKind, "EMBEDDING")
	assertOTELStringAttribute(t, attrs, "embedding.model_name", "test-model")
	assertOTELStringAttribute(t, attrs, "embedding.invocation_parameters", `{"dimensions":1536,"model":"test-model"}`)
	assertOTELStringAttribute(t, attrs, oiInputValue, `["hello","world"]`)
	assertOTELStringAttribute(t, attrs, oiInputMIMEType, "application/json")
	if _, ok := attrs["llm.prompts.0.prompt.text"]; ok {
		t.Error("EMBEDDING span should not carry llm.prompts.0.prompt.text")
	}
	if _, ok := attrs["llm.provider"]; ok {
		t.Error("EMBEDDING span should not carry llm.provider")
	}
	if _, ok := attrs["llm.system"]; ok {
		t.Error("EMBEDDING span should not carry llm.system")
	}
	if exported.Name != "CreateEmbeddings" {
		t.Errorf("embedding span name = %q, want CreateEmbeddings", exported.Name)
	}
}

func TestConvertTraceToResourceSpan_OpenInferenceEmbeddingTextContentLogging(t *testing.T) {
	span := makeSpan("aaaa", "", "embedding test-model", schemas.SpanKindEmbedding)
	span.Attributes = map[string]any{
		schemas.AttrRequestModel: "test-model",
		schemas.AttrInputText:    "hello world",
	}
	trace := &schemas.Trace{
		TraceID:  "00000000000000000000000000000001",
		RootSpan: span,
		Spans:    []*schemas.Span{span},
	}

	p := &OtelPlugin{}
	attrs := otelAttributes(p.convertTraceToResourceSpan("svc", trace, nil, TraceTypeOpenInference, false).ScopeSpans[0].Spans[0].Attributes)
	assertOTELStringAttribute(t, attrs, "embedding.text", "hello world")
	if _, ok := attrs["llm.prompts.0.prompt.text"]; ok {
		t.Error("EMBEDDING span should not carry llm.prompts.0.prompt.text")
	}

	attrs = otelAttributes(p.convertTraceToResourceSpan("svc", trace, nil, TraceTypeOpenInference, true).ScopeSpans[0].Spans[0].Attributes)
	if _, ok := attrs["embedding.text"]; ok {
		t.Error("embedding.text should not be exported when content logging is disabled")
	}
}

func TestConvertTraceToResourceSpan_OpenInferenceTool(t *testing.T) {
	span := makeSpan("aaaa", "", "weather", schemas.SpanKindMCPTool)
	span.Attributes = map[string]any{
		schemas.AttrToolName:               "weather",
		schemas.AttrToolCallID:             "call-1",
		schemas.AttrToolCallArguments:      `{"city":"Paris"}`,
		schemas.AttrToolCallResult:         `{"temperature":21}`,
		schemas.AttrBifrostToolDescription: "Get weather",
		schemas.AttrBifrostToolJSONSchema:  `{"type":"function","function":{"name":"weather"}}`,
		schemas.AttrBifrostToolParameters:  `{"type":"object","properties":{"city":{"type":"string"}}}`,
	}
	trace := &schemas.Trace{
		TraceID:  "00000000000000000000000000000001",
		RootSpan: span,
		Spans:    []*schemas.Span{span},
	}

	p := &OtelPlugin{}
	attrs := otelAttributes(p.convertTraceToResourceSpan("svc", trace, nil, TraceTypeOpenInference, false).ScopeSpans[0].Spans[0].Attributes)

	assertOTELStringAttribute(t, attrs, openInferenceSpanKind, "TOOL")
	assertOTELStringAttribute(t, attrs, "tool.name", "weather")
	assertOTELStringAttribute(t, attrs, "tool.id", "call-1")
	assertOTELStringAttribute(t, attrs, "tool_call.function.name", "weather")
	assertOTELStringAttribute(t, attrs, "tool_call.function.arguments", `{"city":"Paris"}`)
	assertOTELStringAttribute(t, attrs, "tool.description", "Get weather")
	assertOTELStringAttribute(t, attrs, "tool.json_schema", `{"type":"function","function":{"name":"weather"}}`)
	assertOTELStringAttribute(t, attrs, "tool.parameters", `{"type":"object","properties":{"city":{"type":"string"}}}`)
	assertOTELStringAttribute(t, attrs, oiInputValue, `{"city":"Paris"}`)
	assertOTELStringAttribute(t, attrs, oiOutputValue, `{"temperature":21}`)

	withoutContent := otelAttributes(p.convertTraceToResourceSpan("svc", trace, nil, TraceTypeOpenInference, true).ScopeSpans[0].Spans[0].Attributes)
	assertOTELStringAttribute(t, withoutContent, openInferenceSpanKind, "TOOL")
	assertOTELStringAttribute(t, withoutContent, "tool.name", "weather")
	assertOTELStringAttribute(t, withoutContent, "tool.id", "call-1")
	assertOTELStringAttribute(t, withoutContent, "tool_call.function.name", "weather")
	assertOTELStringAttribute(t, withoutContent, "tool_call.id", "call-1")
	for _, key := range []string{"tool.description", "tool.json_schema", "tool.parameters", "tool_call.function.arguments", oiInputValue, oiOutputValue} {
		if _, ok := withoutContent[key]; ok {
			t.Errorf("content attribute %s should not be exported", key)
		}
	}

	genAIAttrs := otelAttributes(p.convertTraceToResourceSpan("svc", trace, nil, TraceTypeGenAIExtension, false).ScopeSpans[0].Spans[0].Attributes)
	for _, key := range []string{schemas.AttrBifrostToolDescription, schemas.AttrBifrostToolDefinitions, schemas.AttrBifrostToolJSONSchema, schemas.AttrBifrostToolParameters} {
		if _, ok := genAIAttrs[key]; ok {
			t.Errorf("OpenInference carrier attribute %s should not be exported by the GenAI profile", key)
		}
	}
}

func TestAppendOpenInferenceMessageStructuredContent(t *testing.T) {
	attrs := otelAttributes(appendOpenInferenceMessage(nil, "llm.input_messages.0", map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "text", "text": "describe this image"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/image.png"}},
		},
	}))

	assertOTELStringAttribute(t, attrs, "llm.input_messages.0.message.contents.0.message_content.type", "text")
	assertOTELStringAttribute(t, attrs, "llm.input_messages.0.message.contents.0.message_content.text", "describe this image")
	assertOTELStringAttribute(t, attrs, "llm.input_messages.0.message.contents.1.message_content.type", "image_url")
	assertOTELStringAttribute(t, attrs, "llm.input_messages.0.message.contents.1.message_content.image.image.url", "https://example.com/image.png")
	if _, ok := attrs["llm.input_messages.0.message.content"]; ok {
		t.Error("structured message content should not emit message.content")
	}
}

func otelStringAttributes(attrs []*commonpb.KeyValue) map[string]string {
	result := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		if value := attr.GetValue().GetStringValue(); value != "" {
			result[attr.Key] = value
		}
	}
	return result
}

func otelAttributes(attrs []*commonpb.KeyValue) map[string]*commonpb.AnyValue {
	result := make(map[string]*commonpb.AnyValue, len(attrs))
	for _, attr := range attrs {
		result[attr.Key] = attr.Value
	}
	return result
}

func assertOTELStringAttribute(t *testing.T, attrs map[string]*commonpb.AnyValue, key, want string) {
	t.Helper()
	value, ok := attrs[key]
	if !ok {
		t.Fatalf("missing OTEL attribute %s", key)
	}
	if got := value.GetStringValue(); got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}
