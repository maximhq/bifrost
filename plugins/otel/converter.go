package otel

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// kvStr creates a key-value pair with a string value
func kvStr(k, v string) *KeyValue {
	return &KeyValue{Key: k, Value: &AnyValue{Value: &StringValue{StringValue: v}}}
}

// kvInt creates a key-value pair with an integer value
func kvInt(k string, v int64) *KeyValue {
	return &KeyValue{Key: k, Value: &AnyValue{Value: &IntValue{IntValue: v}}}
}

// kvDbl creates a key-value pair with a double value
func kvDbl(k string, v float64) *KeyValue {
	return &KeyValue{Key: k, Value: &AnyValue{Value: &DoubleValue{DoubleValue: v}}}
}

// kvBool creates a key-value pair with a boolean value
func kvBool(k string, v bool) *KeyValue {
	return &KeyValue{Key: k, Value: &AnyValue{Value: &BoolValue{BoolValue: v}}}
}

// kvAny creates a key-value pair with an any value
func kvAny(k string, v *AnyValue) *KeyValue {
	return &KeyValue{Key: k, Value: v}
}

// arrValue converts a list of any values to an OpenTelemetry array value
func arrValue(vals ...*AnyValue) *AnyValue {
	return &AnyValue{Value: &ArrayValue{ArrayValue: &ArrayValueValue{Values: vals}}}
}

// listValue converts a list of key-value pairs to an OpenTelemetry list value
func listValue(kvs ...*KeyValue) *AnyValue {
	return &AnyValue{Value: &ListValue{KvlistValue: &KeyValueList{Values: kvs}}}
}

// hexToBytes converts a hex string to bytes, padding/truncating as needed
func hexToBytes(hexStr string, length int) []byte {
	// Remove any non-hex characters
	cleaned := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			return r
		}
		return -1
	}, hexStr)
	// Ensure even length
	if len(cleaned)%2 != 0 {
		cleaned = "0" + cleaned
	}
	// Truncate or pad to desired length
	if len(cleaned) > length*2 {
		cleaned = cleaned[:length*2]
	} else if len(cleaned) < length*2 {
		cleaned = strings.Repeat("0", length*2-len(cleaned)) + cleaned
	}
	bytes, _ := hex.DecodeString(cleaned)
	return bytes
}

// convertTraceToResourceSpan converts a Bifrost trace to OTEL ResourceSpan
func (p *otelProfile) convertTraceToResourceSpan(trace *schemas.Trace) *ResourceSpan {
	otelSpans := make([]*Span, 0, len(trace.Spans))
	for _, span := range trace.Spans {
		otelSpans = append(otelSpans, p.convertSpanToOTELSpan(trace.TraceID, span))
	}

	return &ResourceSpan{
		Resource: &resourcepb.Resource{
			Attributes: p.getResourceAttributes(),
		},
		ScopeSpans: []*ScopeSpan{{
			Scope: p.getInstrumentationScope(),
			Spans: otelSpans,
		}},
	}
}

// convertSpanToOTELSpan converts a single Bifrost span to OTEL format
func (p *otelProfile) convertSpanToOTELSpan(traceID string, span *schemas.Span) *Span {
	otelSpan := &Span{
		TraceId:           hexToBytes(traceID, 16),
		SpanId:            hexToBytes(span.SpanID, 8),
		Name:              span.Name,
		Kind:              convertSpanKind(span.Kind),
		StartTimeUnixNano: uint64(span.StartTime.UnixNano()),
		EndTimeUnixNano:   uint64(span.EndTime.UnixNano()),
		Attributes:        convertAttributesToKeyValues(span.Attributes),
		Status:            convertSpanStatus(span.Status, span.StatusMsg),
		Events:            convertSpanEvents(span.Events),
	}

	// Set parent span ID if present
	if span.ParentID != "" {
		otelSpan.ParentSpanId = hexToBytes(span.ParentID, 8)
	}

	return otelSpan
}

// getResourceAttributes returns the resource attributes for the OTEL span
func (p *otelProfile) getResourceAttributes() []*KeyValue {
	attrs := []*KeyValue{
		kvStr("service.name", p.serviceName),
		kvStr("service.version", p.bifrostVersion),
		kvStr("telemetry.sdk.name", "bifrost"),
		kvStr("telemetry.sdk.language", "go"),
	}
	// Add environment attributes
	attrs = append(attrs, p.attributesFromEnvironment...)
	return attrs
}

// getInstrumentationScope returns the instrumentation scope for OTEL
func (p *otelProfile) getInstrumentationScope() *commonpb.InstrumentationScope {
	return &commonpb.InstrumentationScope{
		Name:    p.serviceName,
		Version: p.bifrostVersion,
	}
}

// convertAttributesToKeyValues converts map[string]any to OTEL KeyValue slice
func convertAttributesToKeyValues(attrs map[string]any) []*KeyValue {
	if attrs == nil {
		return nil
	}
	kvs := make([]*KeyValue, 0, len(attrs))
	for k, v := range attrs {
		kv := anyToKeyValue(k, v)
		if kv != nil {
			kvs = append(kvs, kv)
		}
	}
	return kvs
}

// anyToKeyValue converts any Go value to OTEL KeyValue
func anyToKeyValue(key string, value any) *KeyValue {
	if value == nil {
		return nil
	}
	switch v := value.(type) {
	case string:
		if v == "" {
			return nil
		}
		return kvStr(key, v)
	case int:
		return kvInt(key, int64(v))
	case int32:
		return kvInt(key, int64(v))
	case int64:
		return kvInt(key, v)
	case uint:
		return kvInt(key, int64(v))
	case uint32:
		return kvInt(key, int64(v))
	case uint64:
		return kvInt(key, int64(v))
	case float32:
		return kvDbl(key, float64(v))
	case float64:
		return kvDbl(key, v)
	case bool:
		return kvBool(key, v)
	case []string:
		if len(v) == 0 {
			return nil
		}
		vals := make([]*AnyValue, len(v))
		for i, s := range v {
			vals[i] = &AnyValue{Value: &StringValue{StringValue: s}}
		}
		return kvAny(key, arrValue(vals...))
	case []int:
		if len(v) == 0 {
			return nil
		}
		vals := make([]*AnyValue, len(v))
		for i, n := range v {
			vals[i] = &AnyValue{Value: &IntValue{IntValue: int64(n)}}
		}
		return kvAny(key, arrValue(vals...))
	case []int64:
		if len(v) == 0 {
			return nil
		}
		vals := make([]*AnyValue, len(v))
		for i, n := range v {
			vals[i] = &AnyValue{Value: &IntValue{IntValue: n}}
		}
		return kvAny(key, arrValue(vals...))
	case []float64:
		if len(v) == 0 {
			return nil
		}
		vals := make([]*AnyValue, len(v))
		for i, n := range v {
			vals[i] = &AnyValue{Value: &DoubleValue{DoubleValue: n}}
		}
		return kvAny(key, arrValue(vals...))
	case map[string]any:
		if len(v) == 0 {
			return nil
		}
		kvList := make([]*KeyValue, 0, len(v))
		for k, val := range v {
			kv := anyToKeyValue(k, val)
			if kv != nil {
				kvList = append(kvList, kv)
			}
		}
		return kvAny(key, listValue(kvList...))
	default:
		// For any other type, convert to string
		return kvStr(key, fmt.Sprintf("%v", v))
	}
}

// convertSpanKind maps Bifrost SpanKind to OTEL SpanKind
func convertSpanKind(kind schemas.SpanKind) tracepb.Span_SpanKind {
	switch kind {
	case schemas.SpanKindLLMCall:
		return tracepb.Span_SPAN_KIND_CLIENT
	case schemas.SpanKindHTTPRequest:
		return tracepb.Span_SPAN_KIND_SERVER
	case schemas.SpanKindPlugin:
		return tracepb.Span_SPAN_KIND_INTERNAL
	case schemas.SpanKindInternal:
		return tracepb.Span_SPAN_KIND_INTERNAL
	case schemas.SpanKindRetry:
		return tracepb.Span_SPAN_KIND_INTERNAL
	case schemas.SpanKindFallback:
		return tracepb.Span_SPAN_KIND_INTERNAL
	case schemas.SpanKindMCPTool:
		return tracepb.Span_SPAN_KIND_CLIENT
	case schemas.SpanKindEmbedding:
		return tracepb.Span_SPAN_KIND_CLIENT
	case schemas.SpanKindSpeech:
		return tracepb.Span_SPAN_KIND_CLIENT
	case schemas.SpanKindTranscription:
		return tracepb.Span_SPAN_KIND_CLIENT
	default:
		return tracepb.Span_SPAN_KIND_UNSPECIFIED
	}
}

// convertSpanStatus maps Bifrost SpanStatus to OTEL Status
func convertSpanStatus(status schemas.SpanStatus, msg string) *tracepb.Status {
	switch status {
	case schemas.SpanStatusOk:
		return &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK}
	case schemas.SpanStatusError:
		return &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: msg}
	default:
		return &tracepb.Status{Code: tracepb.Status_STATUS_CODE_UNSET}
	}
}

// convertSpanEvents converts Bifrost span events to OTEL events
func convertSpanEvents(events []schemas.SpanEvent) []*Event {
	if len(events) == 0 {
		return nil
	}
	otelEvents := make([]*Event, len(events))
	for i, event := range events {
		otelEvents[i] = &Event{
			TimeUnixNano: uint64(event.Timestamp.UnixNano()),
			Name:         event.Name,
			Attributes:   convertAttributesToKeyValues(event.Attributes),
		}
	}
	return otelEvents
}
