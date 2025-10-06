package otel

import (
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// ResourceSpan is a trace in the OpenTelemetry format
type ResourceSpan = tracepb.ResourceSpans

// ScopeSpan is a group of spans in the OpenTelemetry format
type ScopeSpan = tracepb.ScopeSpans

// Span is a span in the OpenTelemetry format
type Span = tracepb.Span

// Event is an event in a span
type Event = tracepb.Span_Event

// KeyValue is a key-value pair in the OpenTelemetry format
type KeyValue = commonpb.KeyValue

// AnyValue is a value in the OpenTelemetry format
type AnyValue = commonpb.AnyValue

// StringValue is a string value in the OpenTelemetry format
type StringValue = commonpb.AnyValue_StringValue

// IntValue is an integer value in the OpenTelemetry format
type IntValue = commonpb.AnyValue_IntValue

// DoubleValue is a double value in the OpenTelemetry format
type DoubleValue = commonpb.AnyValue_DoubleValue

// BoolValue is a boolean value in the OpenTelemetry format
type BoolValue = commonpb.AnyValue_BoolValue

// ArrayValue is an array value in the OpenTelemetry format
type ArrayValue = commonpb.AnyValue_ArrayValue

// ArrayValueValue is an array value in the OpenTelemetry format
type ArrayValueValue = commonpb.ArrayValue

// ListValue is a list value in the OpenTelemetry format
type ListValue = commonpb.AnyValue_KvlistValue

// KeyValueList is a list value in the OpenTelemetry format
type KeyValueList = commonpb.KeyValueList
