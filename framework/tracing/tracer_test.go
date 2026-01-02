package tracing

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestTracer_StartSpan_RootSpanWithW3CParent(t *testing.T) {
	// This is the key test: verifies that when an incoming request has a W3C traceparent header,
	// the root span in Bifrost correctly links to the upstream service's span.
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	// Simulate incoming W3C traceparent: 00-{traceID}-{parentSpanID}-01
	inheritedTraceID := "69538b980000000079943934f90c1d40"
	externalParentSpanID := "aad09d1659b4c7e3"

	// Create trace with inherited trace ID
	traceID := tracer.CreateTrace(inheritedTraceID)
	if traceID != inheritedTraceID {
		t.Errorf("CreateTrace() = %q, want inherited trace ID %q", traceID, inheritedTraceID)
	}

	// Set up context with trace ID and parent span ID (as middleware would do)
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	ctx = context.WithValue(ctx, schemas.BifrostContextKeyParentSpanID, externalParentSpanID)

	// Create root span - this should link to the external parent
	newCtx, handle := tracer.StartSpan(ctx, "bifrost-http-request", schemas.SpanKindHTTPRequest)
	if handle == nil {
		t.Fatal("StartSpan() returned nil handle")
	}

	// Verify the span was created with correct parent
	trace := store.GetTrace(traceID)
	if trace == nil {
		t.Fatal("Trace not found in store")
	}

	if trace.RootSpan == nil {
		t.Fatal("Root span not set on trace")
	}

	// THE CRITICAL CHECK: Root span should have the external parent span ID
	if trace.RootSpan.ParentID != externalParentSpanID {
		t.Errorf("Root span ParentID = %q, want external parent span ID %q", trace.RootSpan.ParentID, externalParentSpanID)
	}

	// Verify trace ID is preserved
	if trace.RootSpan.TraceID != inheritedTraceID {
		t.Errorf("Root span TraceID = %q, want %q", trace.RootSpan.TraceID, inheritedTraceID)
	}

	// Verify context has span ID for child span creation
	spanID, ok := newCtx.Value(schemas.BifrostContextKeySpanID).(string)
	if !ok || spanID == "" {
		t.Error("Context should have span ID after StartSpan()")
	}

	if spanID != trace.RootSpan.SpanID {
		t.Errorf("Context span ID = %q, want %q", spanID, trace.RootSpan.SpanID)
	}
}

func TestTracer_StartSpan_RootSpanWithoutW3CParent(t *testing.T) {
	// When there's no incoming W3C context, root span should have no parent
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	// Create new trace (no inherited trace ID)
	traceID := tracer.CreateTrace("")

	// Set up context with only trace ID (no parent span ID)
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)

	// Create root span
	_, handle := tracer.StartSpan(ctx, "local-request", schemas.SpanKindHTTPRequest)
	if handle == nil {
		t.Fatal("StartSpan() returned nil handle")
	}

	trace := store.GetTrace(traceID)
	if trace == nil {
		t.Fatal("Trace not found in store")
	}

	// Root span should have no parent
	if trace.RootSpan.ParentID != "" {
		t.Errorf("Root span ParentID = %q, want empty string (no W3C parent)", trace.RootSpan.ParentID)
	}
}

func TestTracer_StartSpan_ChildSpanLinking(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	inheritedTraceID := "69538b980000000079943934f90c1d40"
	externalParentSpanID := "aad09d1659b4c7e3"

	traceID := tracer.CreateTrace(inheritedTraceID)

	// Set up context with W3C parent span ID
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	ctx = context.WithValue(ctx, schemas.BifrostContextKeyParentSpanID, externalParentSpanID)

	// Create root span
	rootCtx, rootHandle := tracer.StartSpan(ctx, "http-request", schemas.SpanKindHTTPRequest)
	if rootHandle == nil {
		t.Fatal("StartSpan() returned nil handle for root span")
	}

	// Create child span using the context from root span
	childCtx, childHandle := tracer.StartSpan(rootCtx, "llm-call", schemas.SpanKindLLMCall)
	if childHandle == nil {
		t.Fatal("StartSpan() returned nil handle for child span")
	}

	trace := store.GetTrace(traceID)

	// Find the child span
	var childSpan *schemas.Span
	for _, span := range trace.Spans {
		if span.Name == "llm-call" {
			childSpan = span
			break
		}
	}

	if childSpan == nil {
		t.Fatal("Child span not found in trace")
	}

	// Child span should have root span as parent (not the external parent)
	if childSpan.ParentID != trace.RootSpan.SpanID {
		t.Errorf("Child span ParentID = %q, want root span ID %q", childSpan.ParentID, trace.RootSpan.SpanID)
	}

	// Create grandchild span
	_, grandchildHandle := tracer.StartSpan(childCtx, "plugin-call", schemas.SpanKindPlugin)
	if grandchildHandle == nil {
		t.Fatal("StartSpan() returned nil handle for grandchild span")
	}

	// Find the grandchild span
	var grandchildSpan *schemas.Span
	for _, span := range trace.Spans {
		if span.Name == "plugin-call" {
			grandchildSpan = span
			break
		}
	}

	if grandchildSpan == nil {
		t.Fatal("Grandchild span not found in trace")
	}

	// Grandchild should have child as parent
	if grandchildSpan.ParentID != childSpan.SpanID {
		t.Errorf("Grandchild span ParentID = %q, want child span ID %q", grandchildSpan.ParentID, childSpan.SpanID)
	}
}

func TestTracer_StartSpan_NoTraceID(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	// Context without trace ID
	ctx := context.Background()

	newCtx, handle := tracer.StartSpan(ctx, "operation", schemas.SpanKindHTTPRequest)
	if handle != nil {
		t.Error("StartSpan() should return nil handle when no trace ID in context")
	}

	// Context should be unchanged
	if newCtx != ctx {
		t.Error("Context should be unchanged when StartSpan() fails")
	}
}

func TestTracer_EndTrace_ReturnsTraceData(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	inheritedTraceID := "69538b980000000079943934f90c1d40"
	externalParentSpanID := "aad09d1659b4c7e3"

	traceID := tracer.CreateTrace(inheritedTraceID)

	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	ctx = context.WithValue(ctx, schemas.BifrostContextKeyParentSpanID, externalParentSpanID)

	_, rootHandle := tracer.StartSpan(ctx, "http-request", schemas.SpanKindHTTPRequest)
	tracer.EndSpan(rootHandle, schemas.SpanStatusOk, "")

	trace := tracer.EndTrace(traceID)
	if trace == nil {
		t.Fatal("EndTrace() returned nil")
	}

	if trace.TraceID != inheritedTraceID {
		t.Errorf("trace.TraceID = %q, want %q", trace.TraceID, inheritedTraceID)
	}

	if len(trace.Spans) != 1 {
		t.Errorf("len(trace.Spans) = %d, want 1", len(trace.Spans))
	}

	// Root span should still have external parent
	if trace.RootSpan.ParentID != externalParentSpanID {
		t.Errorf("Root span ParentID = %q, want %q", trace.RootSpan.ParentID, externalParentSpanID)
	}
}

func TestTracer_SetAttribute(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	traceID := tracer.CreateTrace("")
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)

	_, handle := tracer.StartSpan(ctx, "operation", schemas.SpanKindHTTPRequest)

	tracer.SetAttribute(handle, "http.method", "POST")
	tracer.SetAttribute(handle, "http.status_code", 200)

	trace := store.GetTrace(traceID)
	span := trace.RootSpan

	if span.Attributes["http.method"] != "POST" {
		t.Errorf("span attribute http.method = %v, want POST", span.Attributes["http.method"])
	}

	if span.Attributes["http.status_code"] != 200 {
		t.Errorf("span attribute http.status_code = %v, want 200", span.Attributes["http.status_code"])
	}
}

func TestTracer_AddEvent(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	traceID := tracer.CreateTrace("")
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)

	_, handle := tracer.StartSpan(ctx, "operation", schemas.SpanKindHTTPRequest)

	tracer.AddEvent(handle, "request.received", map[string]any{
		"size": 1024,
	})

	trace := store.GetTrace(traceID)
	span := trace.RootSpan

	if len(span.Events) != 1 {
		t.Fatalf("len(span.Events) = %d, want 1", len(span.Events))
	}

	if span.Events[0].Name != "request.received" {
		t.Errorf("event name = %q, want request.received", span.Events[0].Name)
	}

	if span.Events[0].Attributes["size"] != 1024 {
		t.Errorf("event attribute size = %v, want 1024", span.Events[0].Attributes["size"])
	}
}

// TestIntegration_FullDistributedTraceFlow tests the complete flow of receiving
// a distributed trace from an upstream service and properly linking spans.
func TestIntegration_FullDistributedTraceFlow(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	// Simulating headers from user's actual Datadog request:
	// traceparent: 00-69538b980000000079943934f90c1d40-aad09d1659b4c7e3-01
	inheritedTraceID := "69538b980000000079943934f90c1d40"
	externalParentSpanID := "aad09d1659b4c7e3"

	// Step 1: Middleware extracts trace context and creates trace
	traceID := tracer.CreateTrace(inheritedTraceID)

	// Step 2: Middleware sets up context (simulating what TracingMiddleware does)
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	ctx = context.WithValue(ctx, schemas.BifrostContextKeyParentSpanID, externalParentSpanID)

	// Step 3: Middleware creates root span
	httpCtx, httpHandle := tracer.StartSpan(ctx, "/v1/chat/completions", schemas.SpanKindHTTPRequest)
	tracer.SetAttribute(httpHandle, "http.method", "POST")

	// Step 4: Bifrost creates LLM call span
	llmCtx, llmHandle := tracer.StartSpan(httpCtx, "openai.chat.completions", schemas.SpanKindLLMCall)
	tracer.SetAttribute(llmHandle, "llm.model", "gpt-4")
	tracer.SetAttribute(llmHandle, "llm.provider", "openai")

	// Step 5: Plugin creates its own span
	_, pluginHandle := tracer.StartSpan(llmCtx, "governance-plugin", schemas.SpanKindPlugin)
	tracer.SetAttribute(pluginHandle, "plugin.name", "governance")

	// Step 6: Complete spans (in reverse order)
	tracer.EndSpan(pluginHandle, schemas.SpanStatusOk, "")
	tracer.EndSpan(llmHandle, schemas.SpanStatusOk, "")
	tracer.EndSpan(httpHandle, schemas.SpanStatusOk, "")

	// Step 7: Complete trace
	trace := tracer.EndTrace(traceID)

	// Verify the trace structure for Datadog
	if trace.TraceID != inheritedTraceID {
		t.Errorf("Trace ID should match inherited ID from Datadog: got %q, want %q", trace.TraceID, inheritedTraceID)
	}

	// Find spans by name
	var httpSpan, llmSpan, pluginSpan *schemas.Span
	for _, span := range trace.Spans {
		switch span.Name {
		case "/v1/chat/completions":
			httpSpan = span
		case "openai.chat.completions":
			llmSpan = span
		case "governance-plugin":
			pluginSpan = span
		}
	}

	if httpSpan == nil || llmSpan == nil || pluginSpan == nil {
		t.Fatal("Not all spans found in trace")
	}

	// Verify span hierarchy for Datadog linking:
	// External Parent (aad09d1659b4c7e3) -> HTTP Span -> LLM Span -> Plugin Span

	// HTTP span should link to Datadog's parent span
	if httpSpan.ParentID != externalParentSpanID {
		t.Errorf("HTTP span should link to Datadog parent: got ParentID %q, want %q",
			httpSpan.ParentID, externalParentSpanID)
	}

	// LLM span should be child of HTTP span
	if llmSpan.ParentID != httpSpan.SpanID {
		t.Errorf("LLM span should be child of HTTP span: got ParentID %q, want %q",
			llmSpan.ParentID, httpSpan.SpanID)
	}

	// Plugin span should be child of LLM span
	if pluginSpan.ParentID != llmSpan.SpanID {
		t.Errorf("Plugin span should be child of LLM span: got ParentID %q, want %q",
			pluginSpan.ParentID, llmSpan.SpanID)
	}

	// All spans should have the same trace ID
	if httpSpan.TraceID != inheritedTraceID || llmSpan.TraceID != inheritedTraceID || pluginSpan.TraceID != inheritedTraceID {
		t.Error("All spans should have the inherited trace ID")
	}

	t.Logf("Trace structure (for Datadog):")
	t.Logf("  Trace ID: %s", trace.TraceID)
	t.Logf("  External Parent Span: %s (from Datadog)", externalParentSpanID)
	t.Logf("    -> HTTP Span: %s (ParentID: %s)", httpSpan.SpanID, httpSpan.ParentID)
	t.Logf("      -> LLM Span: %s (ParentID: %s)", llmSpan.SpanID, llmSpan.ParentID)
	t.Logf("        -> Plugin Span: %s (ParentID: %s)", pluginSpan.SpanID, pluginSpan.ParentID)
}
