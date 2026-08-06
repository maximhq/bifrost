package otel

import (
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
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

// ResourceLog is a set of log records for one resource in the OpenTelemetry format
type ResourceLog = logspb.ResourceLogs

// ScopeLog is a group of log records emitted by one instrumentation scope
type ScopeLog = logspb.ScopeLogs

// LogRecord is a single log record (a GenAI event, for this plugin) in the OpenTelemetry format
type LogRecord = logspb.LogRecord

// SeverityNumber is the numeric severity of a log record
type SeverityNumber = logspb.SeverityNumber

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
