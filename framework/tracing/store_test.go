package tracing

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestCreateTrace_WithInheritedTraceID(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	// Use a trace ID from an incoming W3C traceparent header
	inheritedTraceID := "69538b980000000079943934f90c1d40"

	traceID := store.CreateTrace(inheritedTraceID)

	if traceID != inheritedTraceID {
		t.Errorf("CreateTrace() returned %q, want inherited trace ID %q", traceID, inheritedTraceID)
	}

	trace := store.GetTrace(traceID)
	if trace == nil {
		t.Fatal("GetTrace() returned nil")
	}

	if trace.TraceID != inheritedTraceID {
		t.Errorf("trace.TraceID = %q, want %q", trace.TraceID, inheritedTraceID)
	}

	// ParentID should be empty - we no longer set it incorrectly to the trace ID
	if trace.ParentID != "" {
		t.Errorf("trace.ParentID = %q, want empty string (parent span ID is set on spans, not traces)", trace.ParentID)
	}
}

func TestCreateTrace_GeneratesNewTraceID(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	traceID := store.CreateTrace("")

	if traceID == "" {
		t.Error("CreateTrace() returned empty trace ID")
	}

	// Generated trace ID should be 32 hex characters
	if len(traceID) != 32 {
		t.Errorf("Generated trace ID length = %d, want 32", len(traceID))
	}

	// Verify it's valid hex
	if !isHex(traceID) {
		t.Errorf("Generated trace ID %q is not valid hex", traceID)
	}

	trace := store.GetTrace(traceID)
	if trace == nil {
		t.Fatal("GetTrace() returned nil")
	}

	if trace.ParentID != "" {
		t.Errorf("trace.ParentID = %q, want empty string", trace.ParentID)
	}
}

func TestStartSpan_RootSpanHasNoParent(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	traceID := store.CreateTrace("")

	span := store.StartSpan(traceID, "root-operation", schemas.SpanKindHTTPRequest)
	if span == nil {
		t.Fatal("StartSpan() returned nil")
	}

	// Root span should have no parent when there's no incoming trace context
	if span.ParentID != "" {
		t.Errorf("root span.ParentID = %q, want empty string", span.ParentID)
	}

	if span.TraceID != traceID {
		t.Errorf("span.TraceID = %q, want %q", span.TraceID, traceID)
	}

	// Verify it's set as root span
	trace := store.GetTrace(traceID)
	if trace.RootSpan != span {
		t.Error("StartSpan() did not set trace.RootSpan")
	}
}

func TestStartSpan_SecondSpanHasRootAsParent(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	traceID := store.CreateTrace("")

	rootSpan := store.StartSpan(traceID, "root-operation", schemas.SpanKindHTTPRequest)
	if rootSpan == nil {
		t.Fatal("StartSpan() returned nil for root span")
	}

	// Second span created with StartSpan should have root as parent
	secondSpan := store.StartSpan(traceID, "second-operation", schemas.SpanKindLLMCall)
	if secondSpan == nil {
		t.Fatal("StartSpan() returned nil for second span")
	}

	if secondSpan.ParentID != rootSpan.SpanID {
		t.Errorf("second span.ParentID = %q, want root span ID %q", secondSpan.ParentID, rootSpan.SpanID)
	}
}

func TestStartChildSpan_HasCorrectParent(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	traceID := store.CreateTrace("")

	rootSpan := store.StartSpan(traceID, "root-operation", schemas.SpanKindHTTPRequest)
	if rootSpan == nil {
		t.Fatal("StartSpan() returned nil for root span")
	}

	// Create a child span with explicit parent
	childSpan := store.StartChildSpan(traceID, rootSpan.SpanID, "child-operation", schemas.SpanKindLLMCall)
	if childSpan == nil {
		t.Fatal("StartChildSpan() returned nil")
	}

	if childSpan.ParentID != rootSpan.SpanID {
		t.Errorf("child span.ParentID = %q, want %q", childSpan.ParentID, rootSpan.SpanID)
	}

	if childSpan.TraceID != traceID {
		t.Errorf("child span.TraceID = %q, want %q", childSpan.TraceID, traceID)
	}
}

func TestStartChildSpan_WithExternalParentSpanID(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	// Simulating an incoming request with W3C traceparent header
	inheritedTraceID := "69538b980000000079943934f90c1d40"
	externalParentSpanID := "aad09d1659b4c7e3" // Parent span ID from upstream service

	traceID := store.CreateTrace(inheritedTraceID)

	// Create root span as child of external parent span
	// This is what should happen when processing an incoming distributed trace
	rootSpan := store.StartChildSpan(traceID, externalParentSpanID, "bifrost-request", schemas.SpanKindHTTPRequest)
	if rootSpan == nil {
		t.Fatal("StartChildSpan() returned nil")
	}

	// Root span should have the external parent span ID
	if rootSpan.ParentID != externalParentSpanID {
		t.Errorf("root span.ParentID = %q, want external parent %q", rootSpan.ParentID, externalParentSpanID)
	}

	if rootSpan.TraceID != inheritedTraceID {
		t.Errorf("root span.TraceID = %q, want inherited trace ID %q", rootSpan.TraceID, inheritedTraceID)
	}
}

func TestGetTrace_NotFound(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	trace := store.GetTrace("nonexistent-trace-id")
	if trace != nil {
		t.Error("GetTrace() should return nil for nonexistent trace")
	}
}

func TestCompleteTrace_ReturnsAndRemoves(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	traceID := store.CreateTrace("")
	store.StartSpan(traceID, "operation", schemas.SpanKindHTTPRequest)

	trace := store.CompleteTrace(traceID)
	if trace == nil {
		t.Fatal("CompleteTrace() returned nil")
	}

	if trace.TraceID != traceID {
		t.Errorf("trace.TraceID = %q, want %q", trace.TraceID, traceID)
	}

	if trace.EndTime.IsZero() {
		t.Error("trace.EndTime should be set")
	}

	// Trace should be removed from store
	if store.GetTrace(traceID) != nil {
		t.Error("Trace should be removed from store after CompleteTrace()")
	}
}

func TestEndSpan_SetsStatusAndTime(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	traceID := store.CreateTrace("")
	span := store.StartSpan(traceID, "operation", schemas.SpanKindHTTPRequest)

	store.EndSpan(traceID, span.SpanID, schemas.SpanStatusOk, "success", map[string]any{
		"custom.attr": "value",
	})

	if span.Status != schemas.SpanStatusOk {
		t.Errorf("span.Status = %v, want SpanStatusOk", span.Status)
	}

	if span.EndTime.IsZero() {
		t.Error("span.EndTime should be set")
	}

	if span.Attributes["custom.attr"] != "value" {
		t.Error("EndSpan() should set custom attributes")
	}
}

func TestGenerateTraceID_Format(t *testing.T) {
	id := generateTraceID()

	if len(id) != 32 {
		t.Errorf("generateTraceID() length = %d, want 32", len(id))
	}

	if !isHex(id) {
		t.Errorf("generateTraceID() = %q, not valid hex", id)
	}
}

func TestGenerateSpanID_Format(t *testing.T) {
	id := generateSpanID()

	if len(id) != 16 {
		t.Errorf("generateSpanID() length = %d, want 16", len(id))
	}

	if !isHex(id) {
		t.Errorf("generateSpanID() = %q, not valid hex", id)
	}
}
