package otel

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func decodeMessages(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := schemas.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("value %q is not a JSON array of messages: %v", raw, err)
	}
	return out
}

func partsOf(t *testing.T, msg map[string]any) []map[string]any {
	t.Helper()
	raw, ok := msg["parts"].([]any)
	if !ok {
		t.Fatalf("message %v has no parts array", msg)
	}
	parts := make([]map[string]any, 0, len(raw))
	for _, p := range raw {
		parts = append(parts, p.(map[string]any))
	}
	return parts
}

func kvsFrom(attrs map[string]any) []*KeyValue {
	return convertAttributesToKeyValues(attrs, false)
}

func find(kvs []*KeyValue, key string) string {
	for _, kv := range kvs {
		if kv.Key == key {
			return kv.Value.GetStringValue()
		}
	}
	return ""
}

// TestPartsFormat_ChatMessages asserts the flat chat shape becomes role+parts with text,
// reasoning, tool_call and tool_call_response parts in the model's order.
func TestPartsFormat_ChatMessages(t *testing.T) {
	kvs := kvsFrom(map[string]any{
		schemas.AttrInputMessages: `[
			{"role":"system","content":"You are terse."},
			{"role":"user","content":"Weather in Paris?"},
			{"role":"assistant","content":"","reasoning":"need a lookup","tool_calls":[{"id":"call_1","type":"function","name":"get_weather","args":"{\"city\":\"Paris\"}"}]},
			{"role":"tool","content":"18C, cloudy","tool_call_id":"call_1"}
		]`,
		schemas.AttrOutputMessages: `[{"role":"assistant","content":"18C and cloudy."}]`,
		schemas.AttrRequestModel:   "gpt-4o-mini",
	})
	kvs = applyPartsMessageFormat(kvs)

	in := decodeMessages(t, find(kvs, schemas.AttrInputMessages))
	if len(in) != 4 {
		t.Fatalf("input messages = %d, want 4", len(in))
	}
	if p := partsOf(t, in[1]); len(p) != 1 || p[0]["type"] != "text" || p[0]["content"] != "Weather in Paris?" {
		t.Errorf("user parts = %v, want one text part", p)
	}
	assistant := partsOf(t, in[2])
	if len(assistant) != 2 {
		t.Fatalf("assistant parts = %v, want reasoning then tool_call", assistant)
	}
	if assistant[0]["type"] != "reasoning" || assistant[0]["content"] != "need a lookup" {
		t.Errorf("assistant part 0 = %v, want reasoning", assistant[0])
	}
	call := assistant[1]
	if call["type"] != "tool_call" || call["id"] != "call_1" || call["name"] != "get_weather" {
		t.Errorf("assistant part 1 = %v, want tool_call call_1/get_weather", call)
	}
	if args, ok := call["arguments"].(map[string]any); !ok || args["city"] != "Paris" {
		t.Errorf("tool_call arguments = %v, want parsed object with city=Paris", call["arguments"])
	}
	tool := partsOf(t, in[3])
	if len(tool) != 1 || tool[0]["type"] != "tool_call_response" || tool[0]["id"] != "call_1" || tool[0]["response"] != "18C, cloudy" {
		t.Errorf("tool parts = %v, want one tool_call_response", tool)
	}

	out := decodeMessages(t, find(kvs, schemas.AttrOutputMessages))
	if len(out) != 1 || out[0]["role"] != "assistant" {
		t.Fatalf("output messages = %v, want one assistant message", out)
	}
	if p := partsOf(t, out[0]); len(p) != 1 || p[0]["type"] != "text" || p[0]["content"] != "18C and cloudy." {
		t.Errorf("output parts = %v, want one text part", p)
	}
	if got := find(kvs, schemas.AttrRequestModel); got != "gpt-4o-mini" {
		t.Errorf("non-message attribute rewritten: model = %q", got)
	}
}

// TestPartsFormat_BareStringAndEmptyContent asserts values that are not the flat shape
// (the root span's text excerpt) are wrapped rather than dropped, an empty tool result
// still yields a tool_call_response, and an empty message keeps an empty parts array.
func TestPartsFormat_BareStringAndEmptyContent(t *testing.T) {
	kvs := applyPartsMessageFormat(kvsFrom(map[string]any{
		schemas.AttrInputMessages:  "Weather in Paris?",
		schemas.AttrOutputMessages: `[{"role":"tool","content":"","tool_call_id":"call_9"},{"role":"assistant","content":""}]`,
	}))

	in := decodeMessages(t, find(kvs, schemas.AttrInputMessages))
	if len(in) != 1 || in[0]["role"] != "user" {
		t.Fatalf("bare input = %v, want one user message", in)
	}
	if p := partsOf(t, in[0]); len(p) != 1 || p[0]["content"] != "Weather in Paris?" {
		t.Errorf("bare input parts = %v, want the text wrapped", p)
	}

	out := decodeMessages(t, find(kvs, schemas.AttrOutputMessages))
	tool := partsOf(t, out[0])
	if len(tool) != 1 || tool[0]["type"] != "tool_call_response" || tool[0]["response"] != "" {
		t.Errorf("empty tool result parts = %v, want tool_call_response with empty response", tool)
	}
	if p := partsOf(t, out[1]); len(p) != 0 {
		t.Errorf("empty assistant parts = %v, want []", p)
	}
}

// TestPartsFormat_InstructionsMirroredAsSystemInstructions asserts the Responses/speech
// instructions string is mirrored to the spec attribute as a text part while the original
// attribute is kept.
func TestPartsFormat_InstructionsMirroredAsSystemInstructions(t *testing.T) {
	kvs := applyPartsMessageFormat(kvsFrom(map[string]any{
		schemas.AttrInstructions: "Code every brand mention.",
	}))
	if got := find(kvs, schemas.AttrInstructions); got != "Code every brand mention." {
		t.Errorf("original instructions attribute = %q, want kept", got)
	}
	var parts []map[string]any
	if err := schemas.Unmarshal([]byte(find(kvs, AttrSystemInstructions)), &parts); err != nil || len(parts) != 1 {
		t.Fatalf("%s = %q, want one-part JSON array (%v)", AttrSystemInstructions, find(kvs, AttrSystemInstructions), err)
	}
	if parts[0]["type"] != "text" || parts[0]["content"] != "Code every brand mention." {
		t.Errorf("system instructions part = %v", parts[0])
	}

	// No instructions: no spec attribute either.
	if got := find(applyPartsMessageFormat(kvsFrom(map[string]any{schemas.AttrRequestModel: "m"})), AttrSystemInstructions); got != "" {
		t.Errorf("%s emitted without instructions: %q", AttrSystemInstructions, got)
	}
}

// TestConvertTraceToResourceSpan_MessageFormat asserts the profile option is applied per
// span, defaults to the flat shape, and never resurrects content dropped by
// disableContentLogging.
func TestConvertTraceToResourceSpan_MessageFormat(t *testing.T) {
	p := &OtelPlugin{pluginSpanFilter: &PluginSpanFilter{}}
	makeTrace := func() *schemas.Trace {
		root := makeSpan("aaaa", "", "request", schemas.SpanKindInternal)
		root.Attributes = map[string]any{schemas.AttrInputMessages: "hello"}
		child := makeSpan("bbbb", "aaaa", "chat", schemas.SpanKindLLMCall)
		child.Attributes = map[string]any{
			schemas.AttrInputMessages: `[{"role":"user","content":"hello"}]`,
			schemas.AttrInstructions:  "Be brief.",
		}
		return &schemas.Trace{TraceID: "00000000000000000000000000000003", RootSpan: root, Spans: []*schemas.Span{root, child}}
	}
	child := func(rs *ResourceSpan) *Span {
		for _, s := range rs.ScopeSpans[0].Spans {
			if string(s.SpanId) == string(hexToBytes("bbbb", 8)) {
				return s
			}
		}
		return nil
	}

	flat := p.convertTraceToResourceSpan("svc", makeTrace(), nil, false, false, false, MessageFormatFlat)
	if got := attrString(child(flat), schemas.AttrInputMessages); got != `[{"role":"user","content":"hello"}]` {
		t.Errorf("flat format rewrote input: %q", got)
	}
	if attrString(child(flat), AttrSystemInstructions) != "" {
		t.Error("flat format emitted gen_ai.system_instructions")
	}

	parts := p.convertTraceToResourceSpan("svc", makeTrace(), nil, false, false, false, MessageFormatParts)
	msgs := decodeMessages(t, attrString(child(parts), schemas.AttrInputMessages))
	if len(msgs) != 1 || len(partsOf(t, msgs[0])) != 1 {
		t.Errorf("parts format child input = %v", msgs)
	}
	if attrString(child(parts), AttrSystemInstructions) == "" {
		t.Error("parts format did not emit gen_ai.system_instructions")
	}
	rootMsgs := decodeMessages(t, attrString(findRoot(parts.ScopeSpans[0].Spans), schemas.AttrInputMessages))
	if len(rootMsgs) != 1 || rootMsgs[0]["role"] != "user" {
		t.Errorf("parts format root input = %v, want wrapped bare text", rootMsgs)
	}

	dropped := p.convertTraceToResourceSpan("svc", makeTrace(), nil, true, false, false, MessageFormatParts)
	if attrString(child(dropped), schemas.AttrInputMessages) != "" || attrString(child(dropped), AttrSystemInstructions) != "" {
		t.Error("disableContentLogging content reappeared under parts format")
	}
}

// TestBuildTargetRejectsUnknownMessageFormat asserts a typo in message_format fails
// configuration instead of silently exporting the flat shape.
func TestBuildTargetRejectsUnknownMessageFormat(t *testing.T) {
	for _, f := range []MessageFormat{"", MessageFormatFlat, MessageFormatParts} {
		if !validMessageFormat(f) {
			t.Errorf("validMessageFormat(%q) = false, want true", f)
		}
	}
	if validMessageFormat("semconv") {
		t.Error(`validMessageFormat("semconv") = true, want false`)
	}
}
