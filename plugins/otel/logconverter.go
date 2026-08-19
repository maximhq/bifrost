package otel

import (
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/tracing"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// GenAI events semantic conventions.
// Source: https://github.com/open-telemetry/semantic-conventions-genai (docs/gen-ai/gen-ai-events.md).
// Everything below is Development stability — attribute names may change with semconv releases.
const (
	// EventNameInferenceDetails is the single event emitted per inference operation. It
	// replaces the removed per-message events (gen_ai.system.message, gen_ai.choice, ...).
	EventNameInferenceDetails = "gen_ai.client.inference.operation.details"

	// Event-only attribute names. Everything else is copied from the span verbatim.
	attrSystemInstructions = "gen_ai.system_instructions"
	attrToolDefinitions    = "gen_ai.tool.definitions"
	attrConversationID     = "gen_ai.conversation.id"

	// Semconv message part types.
	partTypeText             = "text"
	partTypeToolCall         = "tool_call"
	partTypeToolCallResponse = "tool_call_response"
	partTypeReasoning        = "reasoning"
	partTypeRefusal          = "refusal"
)

// logRecordFlagsSampled is the W3C "sampled" bit within LOG_RECORD_FLAGS_TRACE_FLAGS_MASK.
// Bifrost hands completed traces to connectors only for requests it already decided to
// record, so every emitted event is sampled.
const logRecordFlagsSampled uint32 = 0x01

// contentAttrsHandledStructurally are the content attributes the event carries in
// structured form under a semconv name, so the generic attribute copy must skip them.
var contentAttrsHandledStructurally = map[string]struct{}{
	schemas.AttrInputMessages:  {},
	schemas.AttrOutputMessages: {},
	schemas.AttrInstructions:   {},
	schemas.AttrTools:          {},
	schemas.AttrRespTools:      {},
}

// convertTraceToResourceLogs builds the GenAI events for one completed trace: one
// gen_ai.client.inference.operation.details log record per exported llm.call span,
// correlated to that span via trace_id/span_id. Returns nil when the trace holds no
// exportable LLM span, so the caller can skip the export entirely.
func (p *OtelPlugin) convertTraceToResourceLogs(serviceName string, trace *schemas.Trace, target *otelTarget) *ResourceLog {
	if trace == nil {
		return nil
	}

	sessionID := getStringAttr(trace.Attributes, schemas.TraceAttrSessionID)

	// Use the same trace ID the span exporter would use, so events and spans agree even
	// when requests are grouped into a session-derived trace.
	traceID := trace.TraceID
	if target.groupTracesBySession && sessionID != "" && trace.RootSpan != nil && trace.RootSpan.ParentID == "" {
		traceID = sessionTraceID(sessionID)
	}

	// Attributes that only ever live on the root span (or the trace) are copied onto every
	// event, so each log record is self-describing for backends that only ingest logs.
	sharedAttrs := p.buildSharedLogAttrs(trace, target, sessionID)

	observedTime := uint64(time.Now().UnixNano())
	records := make([]*LogRecord, 0, len(trace.Spans))
	for _, span := range trace.Spans {
		if span == nil || span.Kind != schemas.SpanKindLLMCall {
			continue
		}
		if !p.pluginSpanFilter.ShouldExportSpan(span) {
			continue
		}
		records = append(records, buildInferenceEvent(traceID, span, target, sharedAttrs, observedTime))
	}
	if len(records) == 0 {
		return nil
	}

	return &ResourceLog{
		Resource: &resourcepb.Resource{
			Attributes: p.getResourceAttributes(serviceName),
		},
		ScopeLogs: []*ScopeLog{{
			Scope:      p.getInstrumentationScope(serviceName),
			LogRecords: records,
		}},
	}
}

// buildSharedLogAttrs collects the trace/root-level enrichment attached to every event:
// session and request identity, instance attributes, and captured request headers.
func (p *OtelPlugin) buildSharedLogAttrs(trace *schemas.Trace, target *otelTarget, sessionID string) []*KeyValue {
	attrs := make([]*KeyValue, 0, 8)
	if sessionID != "" {
		attrs = append(attrs, kvStr("session.id", sessionID))
		// Only a caller-supplied x-bf-session-id counts as a conversation; Bifrost never
		// synthesizes one.
		attrs = append(attrs, kvStr(attrConversationID, sessionID))
	}
	if requestID := trace.GetRequestID(); requestID != "" {
		attrs = append(attrs, kvStr(schemas.AttrBifrostRequestID, requestID))
	}
	attrs = append(attrs, p.instanceAttrs...)
	for k, v := range schemas.FilterHeaders(trace.RequestHeaders, target.requestHeaders) {
		attrs = append(attrs, kvStr("http.request.header."+k, v))
	}
	return attrs
}

// buildInferenceEvent converts one llm.call span into a GenAI inference event.
func buildInferenceEvent(traceID string, span *schemas.Span, target *otelTarget, sharedAttrs []*KeyValue, observedTime uint64) *LogRecord {
	timestamp := observedTime
	if !span.EndTime.IsZero() {
		timestamp = uint64(span.EndTime.UnixNano())
	}

	record := &LogRecord{
		TimeUnixNano:         timestamp,
		ObservedTimeUnixNano: observedTime,
		EventName:            EventNameInferenceDetails,
		TraceId:              hexToBytes(traceID, 16),
		SpanId:               hexToBytes(span.SpanID, 8),
		Flags:                logRecordFlagsSampled,
		Attributes:           buildEventAttributes(span, target, sharedAttrs),
		// Body stays unset: the convention carries everything in attributes.
	}

	// A failed call is an ERROR-severity event so log backends can alert on it without
	// parsing attributes.
	if getStringAttr(span.Attributes, schemas.AttrErrorTypeSpec) != "" || getStringAttr(span.Attributes, schemas.AttrErrorType) != "" {
		record.SeverityNumber = logspb.SeverityNumber_SEVERITY_NUMBER_ERROR
		record.SeverityText = "ERROR"
	} else {
		record.SeverityNumber = logspb.SeverityNumber_SEVERITY_NUMBER_INFO
		record.SeverityText = "INFO"
	}

	return record
}

// buildEventAttributes assembles the event's attributes: every span attribute (metadata
// plus Bifrost extras) verbatim, with the content attributes re-encoded into the
// structured semconv shapes. Content is dropped entirely when the profile sets
// logs_disable_content_logging — note this is the logs-specific switch; the trace-level
// disable_content_logging continues to gate spans only.
func buildEventAttributes(span *schemas.Span, target *otelTarget, sharedAttrs []*KeyValue) []*KeyValue {
	attrs := span.Attributes
	kvs := make([]*KeyValue, 0, len(attrs)+len(sharedAttrs))

	for k, v := range attrs {
		// Overhead is computed for the logs DB only; it never rides an export.
		if k == schemas.AttrBifrostOverheadDurationMs {
			continue
		}
		if _, structured := contentAttrsHandledStructurally[k]; structured {
			continue
		}
		if target.logsDisableContentLogging && schemas.IsContentAttribute(k) {
			continue
		}
		if kv := anyToKeyValue(k, v); kv != nil {
			kvs = append(kvs, kv)
		}
	}

	// The OTel general semconv key, in case only the legacy gen_ai.* one was populated.
	if getStringAttr(attrs, schemas.AttrErrorTypeSpec) == "" {
		if errType := getStringAttr(attrs, schemas.AttrErrorType); errType != "" {
			kvs = append(kvs, kvStr(schemas.AttrErrorTypeSpec, errType))
		}
	}

	if !target.logsDisableContentLogging {
		kvs = append(kvs, buildContentAttributes(attrs)...)
	}

	return append(kvs, sharedAttrs...)
}

// buildContentAttributes re-encodes the span's JSON-string content attributes into the
// structured (nested AnyValue) form events require. Anything that fails to parse falls
// back to the raw value as a plain attribute — an event with degraded content beats no
// event at all.
func buildContentAttributes(attrs map[string]any) []*KeyValue {
	out := make([]*KeyValue, 0, 4)

	if raw, ok := attrs[schemas.AttrInputMessages]; ok {
		out = appendContentAttr(out, schemas.AttrInputMessages, raw, messagesToAnyValue(raw, nil))
	}
	if raw, ok := attrs[schemas.AttrOutputMessages]; ok {
		out = appendContentAttr(out, schemas.AttrOutputMessages, raw, messagesToAnyValue(raw, finishReasonsFromAttrs(attrs)))
	}
	if instructions := getStringAttr(attrs, schemas.AttrInstructions); instructions != "" {
		// System instructions are a list of message parts, not a bare string.
		out = append(out, kvAny(attrSystemInstructions, arrValue(
			listValue(kvStr("type", partTypeText), kvStr("content", instructions)),
		)))
	}
	// The Responses API echoes the tool set back on the response; either source describes
	// the same definitions, so whichever is present wins (request first).
	toolsRaw, ok := attrs[schemas.AttrTools]
	if !ok {
		toolsRaw, ok = attrs[schemas.AttrRespTools]
	}
	if ok {
		out = appendContentAttr(out, attrToolDefinitions, toolsRaw, toolDefinitionsToAnyValue(toolsRaw))
	}

	return out
}

// appendContentAttr appends the structured encoding under key, falling back to the raw
// span value when structured encoding was not possible.
func appendContentAttr(out []*KeyValue, key string, raw any, structured *AnyValue) []*KeyValue {
	if structured != nil {
		return append(out, kvAny(key, structured))
	}
	if kv := anyToKeyValue(key, raw); kv != nil {
		return append(out, kv)
	}
	return out
}

// finishReasonsFromAttrs reads the response finish reasons, preferring the array form and
// falling back to the singular attribute.
func finishReasonsFromAttrs(attrs map[string]any) []string {
	if reasons := getStringSliceAttr(attrs, schemas.AttrFinishReasons); len(reasons) > 0 {
		return reasons
	}
	if reason := getStringAttr(attrs, schemas.AttrFinishReason); reason != "" {
		return []string{reason}
	}
	return nil
}

// messagesToAnyValue converts a span's JSON-string message summary into the semconv
// message schema: a list of {role, parts[, finish_reason]}. finishReasons is non-nil for
// output messages and is applied positionally. Returns nil when the value is not a
// parseable message list, letting the caller fall back to the raw attribute.
func messagesToAnyValue(raw any, finishReasons []string) *AnyValue {
	jsonStr, ok := raw.(string)
	if !ok || jsonStr == "" {
		return nil
	}
	var messages []tracing.MessageSummary
	if err := sonic.UnmarshalString(jsonStr, &messages); err != nil || len(messages) == 0 {
		return nil
	}

	values := make([]*AnyValue, 0, len(messages))
	for i, msg := range messages {
		fields := []*KeyValue{kvStr("role", messageRole(msg, finishReasons != nil))}
		fields = append(fields, kvAny("parts", arrValue(messageParts(msg)...)))
		if i < len(finishReasons) && finishReasons[i] != "" {
			fields = append(fields, kvStr("finish_reason", finishReasons[i]))
		}
		values = append(values, listValue(fields...))
	}
	return arrValue(values...)
}

// messageRole returns the message's role, defaulting to "assistant" for output messages
// and "user" for input messages when the summary carries none.
func messageRole(msg tracing.MessageSummary, isOutput bool) string {
	if msg.Role != "" {
		return msg.Role
	}
	if isOutput {
		return string(schemas.ChatMessageRoleAssistant)
	}
	return string(schemas.ChatMessageRoleUser)
}

// messageParts maps one MessageSummary onto the semconv message-part list.
func messageParts(msg tracing.MessageSummary) []*AnyValue {
	parts := make([]*AnyValue, 0, 2+len(msg.ToolCalls)+len(msg.ReasoningDetails))

	if msg.Content != "" {
		if msg.Role == string(schemas.ChatMessageRoleTool) {
			// A tool message's content is the result of an earlier tool call, not free text.
			fields := []*KeyValue{kvStr("type", partTypeToolCallResponse), kvStr("response", msg.Content)}
			if msg.ToolCallID != "" {
				fields = append(fields, kvStr("id", msg.ToolCallID))
			}
			parts = append(parts, listValue(fields...))
		} else {
			parts = append(parts, textPart(msg.Content))
		}
	}

	if msg.Reasoning != "" {
		parts = append(parts, listValue(kvStr("type", partTypeReasoning), kvStr("content", msg.Reasoning)))
	}
	for _, detail := range msg.ReasoningDetails {
		if detail.Text == "" {
			continue
		}
		parts = append(parts, listValue(kvStr("type", partTypeReasoning), kvStr("content", detail.Text)))
	}

	for _, tc := range msg.ToolCalls {
		fields := []*KeyValue{kvStr("type", partTypeToolCall)}
		if tc.ID != "" {
			fields = append(fields, kvStr("id", tc.ID))
		}
		if tc.Name != "" {
			fields = append(fields, kvStr("name", tc.Name))
		}
		if tc.Args != "" {
			// Arguments are a provider-supplied JSON string; keep them structured when they
			// parse, and as the raw string when they do not (models emit invalid JSON).
			if parsed := jsonToAnyValue(tc.Args); parsed != nil {
				fields = append(fields, kvAny("arguments", parsed))
			} else {
				fields = append(fields, kvStr("arguments", tc.Args))
			}
		}
		parts = append(parts, listValue(fields...))
	}

	// Refusal has no dedicated part type in the schema; the schema permits arbitrary types.
	if msg.Refusal != "" {
		parts = append(parts, listValue(kvStr("type", partTypeRefusal), kvStr("content", msg.Refusal)))
	}
	if msg.Audio != nil && msg.Audio.Transcript != "" {
		parts = append(parts, textPart(msg.Audio.Transcript))
	}

	return parts
}

// textPart builds a {type: "text", content: ...} message part.
func textPart(content string) *AnyValue {
	return listValue(kvStr("type", partTypeText), kvStr("content", content))
}

// toolDefinitionsToAnyValue converts the span's JSON-string tool list into the semconv
// gen_ai.tool.definitions shape. Bifrost records name/description only; per semconv it is
// NOT RECOMMENDED to expand parameter schemas, so nothing more is added here. Returns nil
// when the value cannot be parsed.
func toolDefinitionsToAnyValue(raw any) *AnyValue {
	jsonStr, ok := raw.(string)
	if !ok || jsonStr == "" {
		return nil
	}
	var tools []struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}
	if err := sonic.UnmarshalString(jsonStr, &tools); err != nil || len(tools) == 0 {
		return nil
	}
	values := make([]*AnyValue, 0, len(tools))
	for _, tool := range tools {
		fields := []*KeyValue{kvStr("type", "function"), kvStr("name", tool.Name)}
		if tool.Description != "" {
			fields = append(fields, kvStr("description", tool.Description))
		}
		values = append(values, listValue(fields...))
	}
	return arrValue(values...)
}

// jsonToAnyValue parses a JSON document into a nested AnyValue tree. Returns nil when the
// input is not valid JSON, so callers can fall back to the raw string.
func jsonToAnyValue(raw string) *AnyValue {
	if raw == "" {
		return nil
	}
	var generic any
	if err := sonic.UnmarshalString(raw, &generic); err != nil {
		return nil
	}
	return anyToAnyValue(generic)
}

// anyToAnyValue recursively converts a decoded JSON value into an OTEL AnyValue. Unlike
// anyToKeyValue it preserves empty strings and empty containers, since dropping them from
// a structured payload would silently change the message shape.
func anyToAnyValue(value any) *AnyValue {
	switch v := value.(type) {
	case nil:
		return &AnyValue{}
	case string:
		return &AnyValue{Value: &StringValue{StringValue: v}}
	case bool:
		return &AnyValue{Value: &BoolValue{BoolValue: v}}
	case float64:
		// JSON has one number type; keep integral values as integers so backends do not
		// render token counts and ids as floats.
		if v == float64(int64(v)) {
			return &AnyValue{Value: &IntValue{IntValue: int64(v)}}
		}
		return &AnyValue{Value: &DoubleValue{DoubleValue: v}}
	case int64:
		return &AnyValue{Value: &IntValue{IntValue: v}}
	case int:
		return &AnyValue{Value: &IntValue{IntValue: int64(v)}}
	case []any:
		vals := make([]*AnyValue, 0, len(v))
		for _, item := range v {
			vals = append(vals, anyToAnyValue(item))
		}
		return arrValue(vals...)
	case map[string]any:
		kvs := make([]*KeyValue, 0, len(v))
		for k, item := range v {
			kvs = append(kvs, kvAny(k, anyToAnyValue(item)))
		}
		return listValue(kvs...)
	default:
		// Anything else came from a Go value rather than JSON; reuse the span encoder.
		if kv := anyToKeyValue("_", v); kv != nil {
			return kv.Value
		}
		return &AnyValue{}
	}
}
