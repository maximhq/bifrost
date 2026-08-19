package otel

import (
	"bytes"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// logTestTarget builds a target with log export enabled and the given content switches.
func logTestTarget(disableSpanContent, disableLogContent bool) *otelTarget {
	return &otelTarget{
		serviceName:               "svc",
		logsURL:                   "http://collector:4318/v1/logs",
		logClient:                 &fakeLogClient{},
		disableContentLogging:     disableSpanContent,
		logsDisableContentLogging: disableLogContent,
	}
}

// chatTrace builds a root + one llm.call span carrying a realistic chat payload:
// a system/user/tool conversation in, one assistant message with a tool call out.
func chatTrace() *schemas.Trace {
	root := makeSpan("aaaa", "", "request", schemas.SpanKindInternal)
	root.Attributes = map[string]any{schemas.AttrRequestModel: "gpt-4o-mini"}
	child := makeSpan("bbbb", "aaaa", "chat", schemas.SpanKindLLMCall)
	child.EndTime = time.Unix(1700000000, 500)
	child.Attributes = map[string]any{
		schemas.AttrOperationName: "chat",
		schemas.AttrProviderName:  "openai",
		schemas.AttrRequestModel:  "gpt-4o-mini",
		schemas.AttrResponseModel: "gpt-4o-mini-2024-07-18",
		schemas.AttrInputTokens:   12,
		schemas.AttrOutputTokens:  7,
		schemas.AttrFinishReasons: []string{"tool_calls"},
		schemas.AttrInputMessages: `[
			{"role":"system","content":"be terse"},
			{"role":"user","content":"weather in paris?"},
			{"role":"tool","content":"{\"temp\":21}","tool_call_id":"call_1"}
		]`,
		schemas.AttrOutputMessages: `[{"role":"assistant","content":"looking it up","reasoning":"need the tool",` +
			`"tool_calls":[{"id":"call_2","type":"function","name":"get_weather","args":"{\"city\":\"paris\",\"days\":3}"}]}]`,
		schemas.AttrTools: `[{"name":"get_weather","description":"Look up weather"}]`,
	}
	return &schemas.Trace{
		TraceID:  "0123456789abcdef0123456789abcdef",
		RootSpan: root,
		Spans:    []*schemas.Span{root, child},
	}
}

// TestConvertTraceToResourceLogs_EventShape asserts the core record fields: one event per
// llm.call span, the semconv event name, correlation with the exported span's trace/span
// ids, INFO severity, an unset body, and the sampled flag.
func TestConvertTraceToResourceLogs_EventShape(t *testing.T) {
	p := &OtelPlugin{bifrostVersion: "1.0.0"}
	rl := p.convertTraceToResourceLogs("svc", chatTrace(), logTestTarget(false, false))
	if rl == nil {
		t.Fatal("expected a ResourceLog for a trace with an llm.call span")
	}
	if len(rl.ScopeLogs) != 1 || len(rl.ScopeLogs[0].LogRecords) != 1 {
		t.Fatalf("expected exactly 1 log record (one per llm.call span), got %d scope logs", len(rl.ScopeLogs))
	}
	rec := rl.ScopeLogs[0].LogRecords[0]

	if rec.EventName != EventNameInferenceDetails {
		t.Errorf("event_name = %q, want %q", rec.EventName, EventNameInferenceDetails)
	}
	// Correlation: same ids the span exporter writes for this span.
	if !bytes.Equal(rec.TraceId, hexToBytes("0123456789abcdef0123456789abcdef", 16)) {
		t.Errorf("trace_id = %x, want the trace's id", rec.TraceId)
	}
	if !bytes.Equal(rec.SpanId, hexToBytes("bbbb", 8)) {
		t.Errorf("span_id = %x, want the llm.call span id", rec.SpanId)
	}
	if rec.TimeUnixNano != uint64(time.Unix(1700000000, 500).UnixNano()) {
		t.Errorf("time_unix_nano = %d, want the span end time", rec.TimeUnixNano)
	}
	if rec.ObservedTimeUnixNano == 0 {
		t.Error("observed_time_unix_nano should be stamped at conversion time")
	}
	if rec.SeverityNumber != logspb.SeverityNumber_SEVERITY_NUMBER_INFO {
		t.Errorf("severity = %v, want INFO for a successful call", rec.SeverityNumber)
	}
	if rec.Body != nil {
		t.Error("body must stay unset; the convention puts everything in attributes")
	}
	if rec.Flags != logRecordFlagsSampled {
		t.Errorf("flags = %d, want the sampled bit %d", rec.Flags, logRecordFlagsSampled)
	}
	// Resource/scope are shared with the trace exporter.
	if kvMap(rl.Resource.Attributes)["service.name"].GetStringValue() != "svc" {
		t.Error("resource service.name should match the profile service name")
	}
	if rl.ScopeLogs[0].Scope.GetName() != "svc" {
		t.Error("instrumentation scope should match the trace exporter's")
	}
}

// TestConvertTraceToResourceLogs_StructuredMessages asserts the semconv requirement that
// content on events is recorded in structured form: nested AnyValue trees, not JSON
// strings — including tool_call parts with parsed arguments and a tool_call_response part
// carrying its tool_call_id.
func TestConvertTraceToResourceLogs_StructuredMessages(t *testing.T) {
	p := &OtelPlugin{}
	rl := p.convertTraceToResourceLogs("svc", chatTrace(), logTestTarget(false, false))
	attrs := kvMap(rl.ScopeLogs[0].LogRecords[0].Attributes)

	input := attrs[schemas.AttrInputMessages]
	if input.GetArrayValue() == nil {
		t.Fatalf("gen_ai.input.messages must be structured, got %v", input)
	}
	messages := input.GetArrayValue().Values
	if len(messages) != 3 {
		t.Fatalf("input messages = %d, want 3", len(messages))
	}

	// Message 0: a plain system message → one text part.
	sys := kvMap(messages[0].GetKvlistValue().Values)
	if got := sys["role"].GetStringValue(); got != "system" {
		t.Errorf("message 0 role = %q, want system", got)
	}
	sysParts := sys["parts"].GetArrayValue().Values
	if len(sysParts) != 1 {
		t.Fatalf("system message parts = %d, want 1", len(sysParts))
	}
	part := kvMap(sysParts[0].GetKvlistValue().Values)
	if part["type"].GetStringValue() != partTypeText || part["content"].GetStringValue() != "be terse" {
		t.Errorf("system part = %v, want a text part with the message content", part)
	}

	// Message 2: a tool result → tool_call_response part linked by tool_call_id.
	toolMsg := kvMap(messages[2].GetKvlistValue().Values)
	toolParts := toolMsg["parts"].GetArrayValue().Values
	if len(toolParts) != 1 {
		t.Fatalf("tool message parts = %d, want 1", len(toolParts))
	}
	toolPart := kvMap(toolParts[0].GetKvlistValue().Values)
	if got := toolPart["type"].GetStringValue(); got != partTypeToolCallResponse {
		t.Errorf("tool message part type = %q, want %q", got, partTypeToolCallResponse)
	}
	if got := toolPart["id"].GetStringValue(); got != "call_1" {
		t.Errorf("tool_call_response id = %q, want call_1 (from MessageSummary.ToolCallID)", got)
	}
	if got := toolPart["response"].GetStringValue(); got != `{"temp":21}` {
		t.Errorf("tool_call_response response = %q, want the tool output", got)
	}

	// Output: one assistant message with finish_reason, text, reasoning and a tool call
	// whose arguments are structured rather than a JSON string.
	output := attrs[schemas.AttrOutputMessages]
	outMessages := output.GetArrayValue().Values
	if len(outMessages) != 1 {
		t.Fatalf("output messages = %d, want 1", len(outMessages))
	}
	out := kvMap(outMessages[0].GetKvlistValue().Values)
	if got := out["finish_reason"].GetStringValue(); got != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", got)
	}
	outParts := out["parts"].GetArrayValue().Values
	if len(outParts) != 3 {
		t.Fatalf("output parts = %d, want text + reasoning + tool_call", len(outParts))
	}
	byType := make(map[string]map[string]*AnyValue, len(outParts))
	for _, p := range outParts {
		fields := kvMap(p.GetKvlistValue().Values)
		byType[fields["type"].GetStringValue()] = fields
	}
	if byType[partTypeReasoning]["content"].GetStringValue() != "need the tool" {
		t.Error("reasoning part missing or wrong")
	}
	call := byType[partTypeToolCall]
	if call == nil {
		t.Fatal("tool_call part missing from the output message")
	}
	if got := call["name"].GetStringValue(); got != "get_weather" {
		t.Errorf("tool_call name = %q, want get_weather", got)
	}
	args := call["arguments"].GetKvlistValue()
	if args == nil {
		t.Fatalf("tool_call arguments must be structured, got %v", call["arguments"])
	}
	argMap := kvMap(args.Values)
	if got := argMap["city"].GetStringValue(); got != "paris" {
		t.Errorf("arguments.city = %q, want paris", got)
	}
	if got := argMap["days"].GetIntValue(); got != 3 {
		t.Errorf("arguments.days = %d, want 3 (integral JSON numbers stay ints)", got)
	}

	// Tool definitions come from the span's request tools, renamed to the semconv key.
	tools := attrs[attrToolDefinitions].GetArrayValue()
	if tools == nil || len(tools.Values) != 1 {
		t.Fatalf("gen_ai.tool.definitions = %v, want one structured definition", attrs[attrToolDefinitions])
	}
	def := kvMap(tools.Values[0].GetKvlistValue().Values)
	if def["name"].GetStringValue() != "get_weather" || def["type"].GetStringValue() != "function" {
		t.Errorf("tool definition = %v, want {type:function, name:get_weather}", def)
	}
	if _, ok := attrs[schemas.AttrTools]; ok {
		t.Error("the raw gen_ai.request.tools string should not ride the event alongside the structured form")
	}
}

// TestConvertTraceToResourceLogs_MalformedContentFallsBack asserts that unparseable
// content degrades to a plain string attribute rather than dropping the event.
func TestConvertTraceToResourceLogs_MalformedContentFallsBack(t *testing.T) {
	p := &OtelPlugin{}
	trace := chatTrace()
	llm := trace.Spans[1]
	llm.Attributes[schemas.AttrInputMessages] = "not json at all"
	llm.Attributes[schemas.AttrOutputMessages] = `[{"role":"assistant","content":"hi","tool_calls":[{"id":"c","name":"f","args":"{oops"}]}]`

	rl := p.convertTraceToResourceLogs("svc", trace, logTestTarget(false, false))
	if rl == nil {
		t.Fatal("a malformed content attribute must not drop the event")
	}
	attrs := kvMap(rl.ScopeLogs[0].LogRecords[0].Attributes)
	if got := attrs[schemas.AttrInputMessages].GetStringValue(); got != "not json at all" {
		t.Errorf("unparseable input messages = %v, want the raw string preserved", attrs[schemas.AttrInputMessages])
	}
	// The message list still parses, so only the tool-call arguments fall back.
	outParts := kvMap(attrs[schemas.AttrOutputMessages].GetArrayValue().Values[0].GetKvlistValue().Values)["parts"].GetArrayValue().Values
	for _, part := range outParts {
		fields := kvMap(part.GetKvlistValue().Values)
		if fields["type"].GetStringValue() != partTypeToolCall {
			continue
		}
		if got := fields["arguments"].GetStringValue(); got != "{oops" {
			t.Errorf("invalid tool-call arguments = %v, want the raw string preserved", fields["arguments"])
		}
	}
}

// TestConvertTraceToResourceLogs_ContentGatingIsPerSignal covers all four capture modes:
// span content and event content are gated by independent switches, so operators can put
// content on spans only, events only, both, or neither.
func TestConvertTraceToResourceLogs_ContentGatingIsPerSignal(t *testing.T) {
	p := &OtelPlugin{}
	contentKeys := []string{schemas.AttrInputMessages, schemas.AttrOutputMessages, attrToolDefinitions}

	cases := []struct {
		name             string
		disableSpan      bool
		disableLog       bool
		wantSpanContent  bool
		wantEventContent bool
	}{
		{name: "SPAN_AND_EVENT", wantSpanContent: true, wantEventContent: true},
		{name: "EVENT_ONLY", disableSpan: true, wantEventContent: true},
		{name: "SPAN_ONLY", disableLog: true, wantSpanContent: true},
		{name: "NO_CONTENT", disableSpan: true, disableLog: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := logTestTarget(tc.disableSpan, tc.disableLog)
			rl := p.convertTraceToResourceLogs("svc", chatTrace(), target)
			if rl == nil {
				t.Fatal("events are emitted even without content, for correlation and audit")
			}
			eventAttrs := kvMap(rl.ScopeLogs[0].LogRecords[0].Attributes)
			for _, key := range contentKeys {
				if _, ok := eventAttrs[key]; ok != tc.wantEventContent {
					t.Errorf("event attribute %q present = %v, want %v", key, ok, tc.wantEventContent)
				}
			}
			// Metadata always survives, so the event stays useful with content off.
			if got := eventAttrs[schemas.AttrRequestModel].GetStringValue(); got != "gpt-4o-mini" {
				t.Errorf("event gen_ai.request.model = %q, want it kept regardless of content gating", got)
			}
			if got := eventAttrs[schemas.AttrInputTokens].GetIntValue(); got != 12 {
				t.Errorf("event input tokens = %d, want 12", got)
			}

			// The span export is gated by the other switch, from the same trace.
			rs := p.convertTraceToResourceSpan("svc", chatTrace(), nil, target.disableContentLogging, false, false)
			var llmSpan *Span
			for _, s := range rs.ScopeSpans[0].Spans {
				if bytes.Equal(s.SpanId, hexToBytes("bbbb", 8)) {
					llmSpan = s
				}
			}
			if got := attrString(llmSpan, schemas.AttrInputMessages) != ""; got != tc.wantSpanContent {
				t.Errorf("span content present = %v, want %v", got, tc.wantSpanContent)
			}
		})
	}
}

// TestConvertTraceToResourceLogs_ErrorSeverity asserts a failed call is recorded at ERROR
// severity and carries the unprefixed error.type even when only the legacy gen_ai.error.type
// was populated.
func TestConvertTraceToResourceLogs_ErrorSeverity(t *testing.T) {
	p := &OtelPlugin{}
	trace := chatTrace()
	trace.Spans[1].Status = schemas.SpanStatusError
	trace.Spans[1].Attributes[schemas.AttrErrorType] = "rate_limit_error"

	rl := p.convertTraceToResourceLogs("svc", trace, logTestTarget(false, false))
	rec := rl.ScopeLogs[0].LogRecords[0]
	if rec.SeverityNumber != logspb.SeverityNumber_SEVERITY_NUMBER_ERROR {
		t.Errorf("severity = %v, want ERROR for a failed call", rec.SeverityNumber)
	}
	if rec.SeverityText != "ERROR" {
		t.Errorf("severity_text = %q, want ERROR", rec.SeverityText)
	}
	if got := kvMap(rec.Attributes)[schemas.AttrErrorTypeSpec].GetStringValue(); got != "rate_limit_error" {
		t.Errorf("error.type = %q, want it derived from gen_ai.error.type", got)
	}
}

// TestConvertTraceToResourceLogs_SharedAttrsOnEveryEvent asserts that root-only enrichment
// (session, request id, instance attrs, captured headers) is copied onto each event, so a
// log-only backend can filter by tenant without joining to the trace.
func TestConvertTraceToResourceLogs_SharedAttrsOnEveryEvent(t *testing.T) {
	p := &OtelPlugin{instanceAttrs: []*KeyValue{kvStr("service.instance.id", "pod-7")}}
	trace := chatTrace()
	trace.Attributes = map[string]any{schemas.TraceAttrSessionID: "sess-9"}
	trace.RequestHeaders = map[string]string{"x-tenant": "acme"}
	// A second attempt (retry/fallback) gets its own event.
	second := makeSpan("cccc", "aaaa", "chat", schemas.SpanKindLLMCall)
	second.Attributes = map[string]any{schemas.AttrRequestModel: "gpt-4o"}
	trace.Spans = append(trace.Spans, second)

	target := logTestTarget(false, false)
	target.requestHeaders = []string{"x-tenant"}
	rl := p.convertTraceToResourceLogs("svc", trace, target)
	records := rl.ScopeLogs[0].LogRecords
	if len(records) != 2 {
		t.Fatalf("log records = %d, want one per llm.call span", len(records))
	}
	for i, rec := range records {
		attrs := kvMap(rec.Attributes)
		if got := attrs["session.id"].GetStringValue(); got != "sess-9" {
			t.Errorf("record %d session.id = %q, want sess-9", i, got)
		}
		if got := attrs[attrConversationID].GetStringValue(); got != "sess-9" {
			t.Errorf("record %d gen_ai.conversation.id = %q, want the caller-supplied session id", i, got)
		}
		if got := attrs["service.instance.id"].GetStringValue(); got != "pod-7" {
			t.Errorf("record %d service.instance.id = %q, want pod-7", i, got)
		}
		if got := attrs["http.request.header.x-tenant"].GetStringValue(); got != "acme" {
			t.Errorf("record %d captured header = %q, want acme", i, got)
		}
	}
}

// TestConvertTraceToResourceLogs_NoConversationIDWithoutSession asserts Bifrost never
// synthesizes a conversation id: without an inbound x-bf-session-id there is none.
func TestConvertTraceToResourceLogs_NoConversationIDWithoutSession(t *testing.T) {
	p := &OtelPlugin{}
	rl := p.convertTraceToResourceLogs("svc", chatTrace(), logTestTarget(false, false))
	attrs := kvMap(rl.ScopeLogs[0].LogRecords[0].Attributes)
	if _, ok := attrs[attrConversationID]; ok {
		t.Error("gen_ai.conversation.id must be absent when the caller supplied no session id")
	}
}

// TestConvertTraceToResourceLogs_SessionGroupingParity asserts events adopt the same
// session-derived trace ID the span exporter uses, so logs and spans stay joinable.
func TestConvertTraceToResourceLogs_SessionGroupingParity(t *testing.T) {
	p := &OtelPlugin{}
	trace := chatTrace()
	trace.Attributes = map[string]any{schemas.TraceAttrSessionID: "user-42"}

	target := logTestTarget(false, false)
	target.groupTracesBySession = true
	rl := p.convertTraceToResourceLogs("svc", trace, target)
	want := hexToBytes(sessionTraceID("user-42"), 16)
	if got := rl.ScopeLogs[0].LogRecords[0].TraceId; !bytes.Equal(got, want) {
		t.Errorf("event trace_id = %x, want the session-derived trace id %x", got, want)
	}

	// Grouping off: the original trace ID, matching the span exporter.
	target.groupTracesBySession = false
	rl = p.convertTraceToResourceLogs("svc", trace, target)
	want = hexToBytes(trace.TraceID, 16)
	if got := rl.ScopeLogs[0].LogRecords[0].TraceId; !bytes.Equal(got, want) {
		t.Errorf("event trace_id = %x, want the original trace id %x", got, want)
	}
}

// TestConvertTraceToResourceLogs_NoLLMSpans asserts traces without an exportable llm.call
// span produce nothing, so no empty export request is made.
func TestConvertTraceToResourceLogs_NoLLMSpans(t *testing.T) {
	p := &OtelPlugin{}
	root := makeSpan("aaaa", "", "request", schemas.SpanKindInternal)
	trace := &schemas.Trace{
		TraceID:  "0000000000000000000000000000000b",
		RootSpan: root,
		Spans:    []*schemas.Span{root, makeSpan("bbbb", "aaaa", "plugin.logging.prehook", schemas.SpanKindPlugin)},
	}
	if rl := p.convertTraceToResourceLogs("svc", trace, logTestTarget(false, false)); rl != nil {
		t.Error("a trace with no llm.call spans should produce no log export")
	}
}

// TestConvertTraceToResourceLogs_HonoursSpanFilter asserts log export respects the same
// plugin span filter as trace export.
func TestConvertTraceToResourceLogs_HonoursSpanFilter(t *testing.T) {
	p := &OtelPlugin{pluginSpanFilter: &PluginSpanFilter{
		Mode:    PluginSpanFilterModeExclude,
		Plugins: []string{"logging"},
	}}
	if rl := p.convertTraceToResourceLogs("svc", chatTrace(), logTestTarget(false, false)); rl == nil {
		t.Fatal("an llm.call span is not filtered by a plugin exclusion")
	}
}
