package otel

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// contentAttrKeysWant is the set of content-bearing attribute keys the OTEL exporter must
// strip when content logging is disabled. The exporter delegates to schemas.IsContentAttribute
// so there is no local list to drift; this fixture asserts the classification the exporter
// depends on, including the keys a hand-maintained mirror previously missed
// (prompt, instructions, reasoning).
var contentAttrKeysWant = []string{
	schemas.AttrInputText,
	schemas.AttrInputMessages,
	schemas.AttrInputSpeech,
	schemas.AttrInputEmbedding,
	schemas.AttrOutputMessages,
	schemas.AttrPrompt,
	schemas.AttrInstructions,
	schemas.AttrRespReasoningText,
	schemas.AttrTools, schemas.AttrRespTools,
	schemas.AttrToolName, schemas.AttrToolCallID,
	schemas.AttrToolCallArguments, schemas.AttrToolCallResult, schemas.AttrToolType,
	schemas.AttrToolChoiceType, schemas.AttrToolChoiceName,
	schemas.AttrRespToolChoiceType, schemas.AttrRespToolChoiceName,
}

// TestIsContentAttributeCoversCanonicalSet is the A5 drift guard: every content key must be
// classified as content (and thus stripped) on export, and representative metadata keys must
// not be. The exporter shares core's classifier, so a new content attribute in core is stripped
// by OTEL automatically; this test pins the keys that must never regress to metadata.
func TestIsContentAttributeCoversCanonicalSet(t *testing.T) {
	for _, key := range contentAttrKeysWant {
		if !schemas.IsContentAttribute(key) {
			t.Errorf("IsContentAttribute(%q) = false, want true — content key not stripped on export", key)
		}
	}

	// Metadata must survive.
	for _, key := range []string{schemas.AttrRequestModel, schemas.AttrProviderName, schemas.AttrTotalTokens} {
		if schemas.IsContentAttribute(key) {
			t.Errorf("IsContentAttribute(%q) = true, want false — metadata must not be stripped as content", key)
		}
	}
}

// TestConvertAttributesStripsContentAllSpans verifies the A path: when disableContentLogging is
// true, convertAttributesToKeyValues drops every content-bearing attribute while retaining
// metadata. This is the all-spans strip (converter_test.go covers only the root-only variant).
func TestConvertAttributesStripsContentAllSpans(t *testing.T) {
	attrs := map[string]any{
		schemas.AttrInputMessages:     `[{"role":"user","content":"secret"}]`,
		schemas.AttrOutputMessages:    `[{"role":"assistant","content":"secret"}]`,
		schemas.AttrInputText:         "secret prompt",
		schemas.AttrToolCallArguments: `{"q":"secret"}`,
		schemas.AttrRequestModel:      "gpt-4o-mini",
		schemas.AttrProviderName:      "openai",
		schemas.AttrTotalTokens:       int64(42),
	}

	// Content logging enabled: everything is present.
	kept := kvMap(convertAttributesToKeyValues(attrs, false))
	for k := range attrs {
		if _, ok := kept[k]; !ok {
			t.Errorf("with content enabled, attribute %q was dropped", k)
		}
	}

	// Content logging disabled: content + tool content gone, metadata retained.
	stripped := kvMap(convertAttributesToKeyValues(attrs, true))
	for _, gone := range []string{schemas.AttrInputMessages, schemas.AttrOutputMessages, schemas.AttrInputText, schemas.AttrToolCallArguments} {
		if _, ok := stripped[gone]; ok {
			t.Errorf("content attribute %q survived disableContentLogging", gone)
		}
	}
	for _, keep := range []string{schemas.AttrRequestModel, schemas.AttrProviderName, schemas.AttrTotalTokens} {
		if _, ok := stripped[keep]; !ok {
			t.Errorf("metadata attribute %q was dropped by disableContentLogging", keep)
		}
	}
}

// TestAnyToKeyValueFidelity is the B (value/type fidelity) golden: every Go type branch in
// anyToKeyValue must land in the correct OTEL AnyValue variant with the correct value. This is
// the connector's most bug-prone surface and was previously untested.
func TestAnyToKeyValueFidelity(t *testing.T) {
	t.Run("scalars", func(t *testing.T) {
		if got := anyToKeyValue("s", "hello").Value.GetStringValue(); got != "hello" {
			t.Errorf("string: got %q", got)
		}
		// Integer widths all collapse to IntValue.
		for _, tc := range []struct {
			name string
			in   any
		}{
			{"int", int(7)}, {"int32", int32(7)}, {"int64", int64(7)},
			{"uint", uint(7)}, {"uint32", uint32(7)}, {"uint64", uint64(7)},
		} {
			kv := anyToKeyValue(tc.name, tc.in)
			if _, ok := kv.Value.GetValue().(*IntValue); !ok {
				t.Errorf("%s: variant = %T, want *IntValue", tc.name, kv.Value.GetValue())
			}
			if got := kv.Value.GetIntValue(); got != 7 {
				t.Errorf("%s: got %d, want 7", tc.name, got)
			}
		}
		for _, tc := range []struct {
			name string
			in   any
		}{{"float32", float32(1.5)}, {"float64", float64(1.5)}} {
			kv := anyToKeyValue(tc.name, tc.in)
			if _, ok := kv.Value.GetValue().(*DoubleValue); !ok {
				t.Errorf("%s: variant = %T, want *DoubleValue", tc.name, kv.Value.GetValue())
			}
			if got := kv.Value.GetDoubleValue(); got != 1.5 {
				t.Errorf("%s: got %v, want 1.5", tc.name, got)
			}
		}
		if kv := anyToKeyValue("b", true); kv.Value.GetBoolValue() != true {
			t.Errorf("bool: got %v", kv.Value.GetBoolValue())
		}
	})

	t.Run("slices", func(t *testing.T) {
		kv := anyToKeyValue("ss", []string{"a", "b"})
		arr := kv.Value.GetArrayValue()
		if arr == nil || len(arr.Values) != 2 || arr.Values[0].GetStringValue() != "a" {
			t.Errorf("[]string: got %#v", arr)
		}

		kvi := anyToKeyValue("ii", []int{1, 2, 3})
		if arr := kvi.Value.GetArrayValue(); arr == nil || len(arr.Values) != 3 || arr.Values[2].GetIntValue() != 3 {
			t.Errorf("[]int: got %#v", arr)
		}

		kvi64 := anyToKeyValue("i64", []int64{9})
		if arr := kvi64.Value.GetArrayValue(); arr == nil || arr.Values[0].GetIntValue() != 9 {
			t.Errorf("[]int64: got %#v", kvi64.Value.GetArrayValue())
		}

		kvf := anyToKeyValue("ff", []float64{2.5})
		if arr := kvf.Value.GetArrayValue(); arr == nil || arr.Values[0].GetDoubleValue() != 2.5 {
			t.Errorf("[]float64: got %#v", kvf.Value.GetArrayValue())
		}

		// []any with mixed element types, each converted by the recursive path.
		kva := anyToKeyValue("mixed", []any{"x", int64(1), true})
		arr = kva.Value.GetArrayValue()
		if arr == nil || len(arr.Values) != 3 {
			t.Fatalf("[]any: got %#v", arr)
		}
		if arr.Values[0].GetStringValue() != "x" || arr.Values[1].GetIntValue() != 1 || arr.Values[2].GetBoolValue() != true {
			t.Errorf("[]any values mismatched: %#v", arr.Values)
		}
	})

	t.Run("map", func(t *testing.T) {
		kv := anyToKeyValue("m", map[string]any{"model": "gpt-4o", "n": int64(2)})
		list := kv.Value.GetKvlistValue()
		if list == nil || len(list.Values) != 2 {
			t.Fatalf("map: got %#v", list)
		}
		got := kvMap(list.Values)
		if got["model"].GetStringValue() != "gpt-4o" || got["n"].GetIntValue() != 2 {
			t.Errorf("map values mismatched: %#v", list.Values)
		}
	})

	t.Run("struct fallback", func(t *testing.T) {
		// An unrecognized type is marshalled then re-decoded into a generic value: a struct
		// becomes a kvlist keyed by its JSON field names.
		type payload struct {
			Model string `json:"model"`
			N     int    `json:"n"`
		}
		kv := anyToKeyValue("p", payload{Model: "claude", N: 3})
		list := kv.Value.GetKvlistValue()
		if list == nil {
			t.Fatalf("struct fallback: expected kvlist, got %#v", kv.Value.GetValue())
		}
		got := kvMap(list.Values)
		if got["model"].GetStringValue() != "claude" {
			t.Errorf("struct fallback model = %q, want claude", got["model"].GetStringValue())
		}
		// JSON numbers decode as float64, so N lands in DoubleValue.
		if got["n"].GetDoubleValue() != 3 {
			t.Errorf("struct fallback n = %v, want 3", got["n"].GetDoubleValue())
		}
	})
}

// TestConvertAttributesEdgeCases is the E (nil/edge safety) path: nil map, empty string, and
// empty slices must all yield no attribute rather than a zero-value or a panic.
func TestConvertAttributesEdgeCases(t *testing.T) {
	if got := convertAttributesToKeyValues(nil, false); got != nil {
		t.Errorf("nil attrs: got %#v, want nil", got)
	}

	// nil interface value and empty string both drop.
	if kv := anyToKeyValue("nilval", nil); kv != nil {
		t.Errorf("nil value: got %#v, want nil", kv)
	}
	if kv := anyToKeyValue("empty", ""); kv != nil {
		t.Errorf("empty string: got %#v, want nil (empty strings are dropped)", kv)
	}

	// Empty slices contribute no attribute.
	for name, v := range map[string]any{
		"[]string":  []string{},
		"[]int":     []int{},
		"[]int64":   []int64{},
		"[]float64": []float64{},
		"[]any":     []any{},
	} {
		if kv := anyToKeyValue(name, v); kv != nil {
			t.Errorf("%s empty: got %#v, want nil", name, kv)
		}
	}

	// A map with only-droppable values still emits the outer key (an empty kvlist), but the
	// whole-map empty case drops.
	if kv := anyToKeyValue("emptymap", map[string]any{}); kv != nil {
		t.Errorf("empty map: got %#v, want nil", kv)
	}
}

// TestConvertTraceRequestHeaderFiltering is the C path: only allow-listed request headers are
// emitted, prefixed http.request.header.*, and only on the root span — never on children.
func TestConvertTraceRequestHeaderFiltering(t *testing.T) {
	p := &OtelPlugin{pluginSpanFilter: &PluginSpanFilter{}}

	root := makeSpan("aaaa", "", "request", schemas.SpanKindInternal)
	child := makeSpan("bbbb", "aaaa", "chat", schemas.SpanKindLLMCall)
	trace := &schemas.Trace{
		TraceID:  "00000000000000000000000000000042",
		RootSpan: root,
		Spans:    []*schemas.Span{root, child},
		RequestHeaders: map[string]string{
			"x-tenant-id":   "acme",
			"authorization": "Bearer secret",
		},
	}

	rs := p.convertTraceToResourceSpan("svc", trace, []string{"x-tenant-id"}, false, false, false)
	spans := rs.ScopeSpans[0].Spans

	rootOut := findRoot(spans)
	if got := attrString(rootOut, "http.request.header.x-tenant-id"); got != "acme" {
		t.Errorf("root allow-listed header = %q, want acme", got)
	}
	if got := attrString(rootOut, "http.request.header.authorization"); got != "" {
		t.Errorf("non-allow-listed header leaked to root: %q", got)
	}

	// Headers are a root-span-only concern; children must carry none.
	for _, s := range spans {
		if s == rootOut {
			continue
		}
		if got := attrString(s, "http.request.header.x-tenant-id"); got != "" {
			t.Errorf("child span %q carried a request header: %q", s.Name, got)
		}
	}
}

// TestConvertSpanKindExhaustive is the D drift guard: every schemas.SpanKind* value (except the
// deliberately-unspecified zero value) must map to a concrete, non-UNSPECIFIED OTEL kind. A new
// SpanKind added to core without a case in convertSpanKind falls through to UNSPECIFIED and is
// caught here (this is how SpanKindMCPClient's missing mapping was found).
func TestConvertSpanKindExhaustive(t *testing.T) {
	allKinds := []schemas.SpanKind{
		schemas.SpanKindLLMCall, schemas.SpanKindPlugin, schemas.SpanKindMCPTool,
		schemas.SpanKindMCPClient, schemas.SpanKindRetry, schemas.SpanKindFallback,
		schemas.SpanKindHTTPRequest, schemas.SpanKindEmbedding, schemas.SpanKindSpeech,
		schemas.SpanKindTranscription, schemas.SpanKindInternal,
	}
	for _, k := range allKinds {
		if got := convertSpanKind(k); got == tracepb.Span_SPAN_KIND_UNSPECIFIED {
			t.Errorf("convertSpanKind(%q) = UNSPECIFIED — add an explicit case in converter.go", k)
		}
	}
	// The zero value is intentionally unspecified.
	if got := convertSpanKind(schemas.SpanKindUnspecified); got != tracepb.Span_SPAN_KIND_UNSPECIFIED {
		t.Errorf("convertSpanKind(unspecified) = %v, want UNSPECIFIED", got)
	}
	// A few representative mappings pinned so a wrong reassignment is caught.
	if convertSpanKind(schemas.SpanKindLLMCall) != tracepb.Span_SPAN_KIND_CLIENT {
		t.Error("llm.call must map to CLIENT")
	}
	if convertSpanKind(schemas.SpanKindHTTPRequest) != tracepb.Span_SPAN_KIND_SERVER {
		t.Error("http.request must map to SERVER")
	}
	if convertSpanKind(schemas.SpanKindPlugin) != tracepb.Span_SPAN_KIND_INTERNAL {
		t.Error("plugin must map to INTERNAL")
	}
}

// TestConvertSpanStatus asserts the status-code mapping and that the error message is carried
// only on the error status.
func TestConvertSpanStatus(t *testing.T) {
	if s := convertSpanStatus(schemas.SpanStatusOk, "ignored"); s.Code != tracepb.Status_STATUS_CODE_OK {
		t.Errorf("ok: code = %v", s.Code)
	}
	errStatus := convertSpanStatus(schemas.SpanStatusError, "boom")
	if errStatus.Code != tracepb.Status_STATUS_CODE_ERROR {
		t.Errorf("error: code = %v", errStatus.Code)
	}
	if errStatus.Message != "boom" {
		t.Errorf("error: message = %q, want boom", errStatus.Message)
	}
	if s := convertSpanStatus(schemas.SpanStatus(""), "x"); s.Code != tracepb.Status_STATUS_CODE_UNSET {
		t.Errorf("unset: code = %v", s.Code)
	}
}

// TestConvertSpanEventsStripContent asserts events convert with their attributes and that the
// content filter applies inside event attributes too, not just span attributes.
func TestConvertSpanEventsStripContent(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	events := []schemas.SpanEvent{{
		Name:      "gen_ai.content.completion",
		Timestamp: ts,
		Attributes: map[string]any{
			schemas.AttrOutputMessages: "secret completion",
			schemas.AttrRequestModel:   "gpt-4o-mini",
		},
	}}

	if got := convertSpanEvents(nil, false); got != nil {
		t.Errorf("nil events: got %#v, want nil", got)
	}

	kept := convertSpanEvents(events, false)
	if len(kept) != 1 || kept[0].Name != "gen_ai.content.completion" {
		t.Fatalf("events not converted: %#v", kept)
	}
	if uint64(ts.UnixNano()) != kept[0].TimeUnixNano {
		t.Errorf("event timestamp = %d, want %d", kept[0].TimeUnixNano, uint64(ts.UnixNano()))
	}
	keptAttrs := kvMap(kept[0].Attributes)
	if _, ok := keptAttrs[schemas.AttrOutputMessages]; !ok {
		t.Error("with content enabled, event output content should be present")
	}

	stripped := convertSpanEvents(events, true)
	strippedAttrs := kvMap(stripped[0].Attributes)
	if _, ok := strippedAttrs[schemas.AttrOutputMessages]; ok {
		t.Error("event content attribute survived disableContentLogging")
	}
	if _, ok := strippedAttrs[schemas.AttrRequestModel]; !ok {
		t.Error("event metadata attribute was dropped by disableContentLogging")
	}
}

// TestConvertTraceContentFidelity is the OTEL analog of the Kafka/PubSub harness
// assertPayloadComplete: it builds a realistic root + llm.call trace carrying the same
// gen_ai.* attributes the framework's llmspan producer emits (provider/model, JSON-string
// input/output messages, []string finish reasons, int token counts) and asserts the *actual
// values* survive conversion onto the correct (llm.call) exported span — not just that the keys
// exist. This is the content-present counterpart to TestConvertAttributesStripsContentAllSpans.
func TestConvertTraceContentFidelity(t *testing.T) {
	p := &OtelPlugin{pluginSpanFilter: &PluginSpanFilter{}}

	root := makeSpan("aaaa", "", "request", schemas.SpanKindInternal)
	child := makeSpan("bbbb", "aaaa", "chat", schemas.SpanKindLLMCall)
	// Mirror the shapes framework/tracing/llmspan.go actually stores: messages are JSON
	// strings (MarshalString), finish reasons are []string, token counts are ints.
	child.Attributes = map[string]any{
		schemas.AttrProviderName:   "openai",
		schemas.AttrRequestModel:   "gpt-4o-mini",
		schemas.AttrResponseModel:  "gpt-4o-mini-2024-07-18",
		schemas.AttrInputMessages:  `[{"role":"user","content":"hello world"}]`,
		schemas.AttrOutputMessages: `[{"role":"assistant","content":"hello world"}]`,
		schemas.AttrFinishReasons:  []string{"stop"},
		schemas.AttrInputTokens:    2,
		schemas.AttrOutputTokens:   2,
		schemas.AttrTotalTokens:    4,
	}
	trace := &schemas.Trace{
		TraceID:  "00000000000000000000000000000077",
		RootSpan: root,
		Spans:    []*schemas.Span{root, child},
	}

	// Content logging enabled (disableContentLogging=false, disableRootSpanContent=false).
	rs := p.convertTraceToResourceSpan("svc", trace, nil, false, false, false)

	// Find the fixture's llm.call span by its span ID, not by kind/position — other span
	// kinds (MCP tool/client, embedding, speech, transcription) also map to CLIENT, so a
	// kind-based scan would drift onto the wrong span if the fixture ever grew one.
	var llm *Span
	for _, s := range rs.ScopeSpans[0].Spans {
		if bytes.Equal(s.SpanId, hexToBytes("bbbb", 8)) {
			llm = s
			break
		}
	}
	if llm == nil {
		t.Fatal("no llm.call span found in exported trace")
	}
	attrs := kvMap(llm.Attributes)

	// Scalar metadata: exact string values.
	if got := attrs[schemas.AttrProviderName].GetStringValue(); got != "openai" {
		t.Errorf("provider = %q, want openai", got)
	}
	if got := attrs[schemas.AttrRequestModel].GetStringValue(); got != "gpt-4o-mini" {
		t.Errorf("request model = %q, want gpt-4o-mini", got)
	}
	if got := attrs[schemas.AttrResponseModel].GetStringValue(); got != "gpt-4o-mini-2024-07-18" {
		t.Errorf("response model = %q, want gpt-4o-mini-2024-07-18", got)
	}

	// Message content: the actual "hello world" text must be present in the JSON string.
	if in := attrs[schemas.AttrInputMessages].GetStringValue(); !strings.Contains(in, "hello world") {
		t.Errorf("input messages = %q, want to contain %q", in, "hello world")
	}
	if out := attrs[schemas.AttrOutputMessages].GetStringValue(); !strings.Contains(out, "hello world") {
		t.Errorf("output messages = %q, want to contain %q", out, "hello world")
	}

	// Finish reasons: []string -> ArrayValue of strings.
	if fr := attrs[schemas.AttrFinishReasons].GetArrayValue(); fr == nil || len(fr.Values) != 1 || fr.Values[0].GetStringValue() != "stop" {
		t.Errorf("finish reasons = %#v, want [\"stop\"]", attrs[schemas.AttrFinishReasons].GetArrayValue())
	}

	// Token counts: int -> IntValue, exact values.
	if got := attrs[schemas.AttrInputTokens].GetIntValue(); got != 2 {
		t.Errorf("input tokens = %d, want 2", got)
	}
	if got := attrs[schemas.AttrOutputTokens].GetIntValue(); got != 2 {
		t.Errorf("output tokens = %d, want 2", got)
	}
	if got := attrs[schemas.AttrTotalTokens].GetIntValue(); got != 4 {
		t.Errorf("total tokens = %d, want 4", got)
	}
}

// kvMap indexes a KeyValue slice by key for convenient assertions.
func kvMap(kvs []*KeyValue) map[string]*AnyValue {
	m := make(map[string]*AnyValue, len(kvs))
	for _, kv := range kvs {
		m[kv.Key] = kv.Value
	}
	return m
}

// TestKVMapKeysUnique is a sanity check that convertAttributesToKeyValues does not emit duplicate
// keys for a plain metadata map (kvMap would otherwise silently collapse them in the assertions
// above), keeping the map-based assertions honest.
func TestKVMapKeysUnique(t *testing.T) {
	attrs := map[string]any{schemas.AttrRequestModel: "m", schemas.AttrProviderName: "p"}
	kvs := convertAttributesToKeyValues(attrs, false)
	if len(kvs) != len(kvMap(kvs)) {
		t.Errorf("duplicate keys emitted: %d kvs collapsed to %d unique", len(kvs), len(kvMap(kvs)))
	}
}
