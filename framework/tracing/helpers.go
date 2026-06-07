// Package tracing provides distributed tracing infrastructure for Bifrost
package tracing

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
)

// GetTraceID retrieves the trace ID from the context
func GetTraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	traceID, ok := ctx.Value(schemas.BifrostContextKeyTraceID).(string)
	if !ok {
		return ""
	}
	return traceID
}

// GetTrace retrieves the current trace from context using the store
func GetTrace(ctx context.Context, store *TraceStore) *schemas.Trace {
	traceID := GetTraceID(ctx)
	if traceID == "" {
		return nil
	}
	return store.GetTrace(traceID)
}

// AddSpan adds a new span to the current trace
func AddSpan(ctx context.Context, store *TraceStore, name string, kind schemas.SpanKind) *schemas.Span {
	traceID := GetTraceID(ctx)
	if traceID == "" {
		return nil
	}
	return store.StartSpan(traceID, name, kind)
}

// AddChildSpan adds a new child span to the current trace under a specific parent
func AddChildSpan(ctx context.Context, store *TraceStore, parentSpanID, name string, kind schemas.SpanKind) *schemas.Span {
	traceID := GetTraceID(ctx)
	if traceID == "" {
		return nil
	}
	return store.StartChildSpan(traceID, parentSpanID, name, kind)
}

// EndSpan completes a span with the given status
func EndSpan(ctx context.Context, store *TraceStore, spanID string, status schemas.SpanStatus, statusMsg string, attrs map[string]any) {
	traceID := GetTraceID(ctx)
	if traceID == "" {
		return
	}
	store.EndSpan(traceID, spanID, status, statusMsg, attrs)
}

// SetSpanAttribute sets an attribute on a span
func SetSpanAttribute(ctx context.Context, store *TraceStore, spanID, key string, value any) {
	trace := GetTrace(ctx, store)
	if trace == nil {
		return
	}
	span := trace.GetSpan(spanID)
	if span == nil {
		return
	}
	span.SetAttribute(key, value)
}

// AddSpanEvent adds an event to a span
func AddSpanEvent(ctx context.Context, store *TraceStore, spanID string, event schemas.SpanEvent) {
	trace := GetTrace(ctx, store)
	if trace == nil {
		return
	}
	span := trace.GetSpan(spanID)
	if span == nil {
		return
	}
	span.AddEvent(event)
}
