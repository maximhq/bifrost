package tracing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

type testRealtimeObservabilityPlugin struct {
	injected        chan *schemas.Trace
	injectedPayload chan string
}

func (p *testRealtimeObservabilityPlugin) GetName() string { return "test-observability" }
func (p *testRealtimeObservabilityPlugin) Cleanup() error  { return nil }
func (p *testRealtimeObservabilityPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}
func (p *testRealtimeObservabilityPlugin) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}
func (p *testRealtimeObservabilityPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}
func (p *testRealtimeObservabilityPlugin) Inject(_ context.Context, trace *schemas.Trace) error {
	if p.injectedPayload != nil {
		serialized, err := sonic.MarshalString(trace)
		if err != nil {
			return err
		}
		p.injectedPayload <- serialized
		return nil
	}
	if trace == nil {
		p.injected <- nil
		return nil
	}
	// SnapshotForExport, not `*trace`: Trace carries a sync.Mutex, so a struct
	// copy duplicates the lock (go vet: "assignment copies lock value") and
	// still shares the Spans slice and attribute maps with the original. The
	// helper takes the lock and deep-copies spans and maps, which is what a
	// snapshot handed across a channel actually needs.
	p.injected <- trace.SnapshotForExport()
	return nil
}

func TestTracer_CompleteAndFlushTraceInjectsObservabilityPlugins(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	traceID := tracer.CreateTrace("")
	plugin := &testRealtimeObservabilityPlugin{
		injected: make(chan *schemas.Trace, 1),
	}

	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{plugin}, nil)
	tracer.CompleteAndFlushTrace(traceID)

	select {
	case trace := <-plugin.injected:
		if trace == nil || trace.TraceID != traceID {
			t.Fatalf("injected trace = %+v, want trace %q", trace, traceID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for observability inject")
	}

	if got := tracer.store.GetTrace(traceID); got != nil {
		t.Fatalf("trace %q was not released after flush", traceID)
	}
}

// TestTracer_TransformPausedStreamBufferForwardsToAccumulator verifies the production tracer exposes the gate transformation capability.
func TestTracer_TransformPausedStreamBufferForwardsToAccumulator(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	const traceID = "trace-transform-paused"
	tracer.PauseStream(traceID)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTracer, tracer)
	ctx.SetValue(schemas.BifrostContextKeyTraceID, traceID)

	called := false
	err := ctx.TransformPausedStreamBuffer(func(chunks []*schemas.BifrostStreamChunk) (schemas.PausedStreamBufferTransformResult, error) {
		called = true
		return schemas.PausedStreamBufferTransformResult{Chunks: chunks}, nil
	})
	if err != nil {
		t.Fatalf("TransformPausedStreamBuffer() error = %v", err)
	}
	if !called {
		t.Fatal("TransformPausedStreamBuffer() did not reach the streaming accumulator")
	}
}

func TestTracer_CompleteAndFlushTraceRedactsContentBeforeInject(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	traceID := tracer.CreateTrace("")
	plugin := &testRealtimeObservabilityPlugin{
		injectedPayload: make(chan string, 1),
	}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{plugin}, nil)

	// Store replacements before output attributes are populated. This mirrors
	// streaming, where the final accumulated output lands near trace completion.
	tracer.SetTraceRedactionReplacements(traceID, schemas.RedactionPhaseInput, map[string]string{
		"alex@example.com": "[EMAIL-INPUT]",
	})
	tracer.SetTraceRedactionReplacements(traceID, schemas.RedactionPhaseOutput, map[string]string{
		"alex@example.com": "[EMAIL-OUTPUT]",
	})

	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	rootCtx, rootHandle := tracer.StartSpan(ctx, "http-request", schemas.SpanKindHTTPRequest)
	tracer.SetAttribute(rootHandle, schemas.AttrInputMessages, `{"content":"email alex@example.com"}`)
	_, childHandle := tracer.StartSpan(rootCtx, "llm-call", schemas.SpanKindLLMCall)
	tracer.SetAttribute(childHandle, schemas.AttrOutputMessages, `{"content":"reply alex@example.com"}`)
	tracer.SetAttribute(childHandle, schemas.AttrRequestModel, "gpt-4o-mini")

	tracer.CompleteAndFlushTrace(traceID)

	select {
	case payload := <-plugin.injectedPayload:
		if strings.Contains(payload, "alex@example.com") {
			t.Fatalf("injected trace leaked raw content: %s", payload)
		}
		if !strings.Contains(payload, "[EMAIL-INPUT]") {
			t.Fatalf("injected trace missing input redacted placeholder: %s", payload)
		}
		if !strings.Contains(payload, "[EMAIL-OUTPUT]") {
			t.Fatalf("injected trace missing output redacted placeholder: %s", payload)
		}
		if !strings.Contains(payload, "gpt-4o-mini") {
			t.Fatalf("injected trace should retain non-content attributes: %s", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for observability inject")
	}
}

func TestTracer_SetTraceRedactionReplacementsSurvivesLaterObservabilityPlugins(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	traceID := tracer.CreateTrace("")
	tracer.SetTraceRedactionReplacements(traceID, schemas.RedactionPhaseInput, map[string]string{
		"alex@example.com": "[EMAIL-1]",
	})

	plugin := &testRealtimeObservabilityPlugin{
		injectedPayload: make(chan string, 1),
	}
	tracer.SetObservabilityPlugins([]schemas.ObservabilityPlugin{plugin}, nil)

	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	_, rootHandle := tracer.StartSpan(ctx, "http-request", schemas.SpanKindHTTPRequest)
	tracer.SetAttribute(rootHandle, schemas.AttrInputMessages, `{"content":"email alex@example.com"}`)
	tracer.CompleteAndFlushTrace(traceID)

	select {
	case payload := <-plugin.injectedPayload:
		if strings.Contains(payload, "alex@example.com") {
			t.Fatalf("trace redaction replacements should survive later observability plugin registration: %s", payload)
		}
		if !strings.Contains(payload, "[EMAIL-1]") {
			t.Fatalf("injected trace missing redacted placeholder: %s", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for observability inject")
	}
}

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

	// Create trace with inherited trace ID. The returned value is a unique
	// per-request store key, not the inherited ID (issue #5256) — the W3C ID
	// lives on trace.TraceID for export.
	traceID := tracer.CreateTrace(inheritedTraceID)
	if traceID == inheritedTraceID {
		t.Errorf("CreateTrace() = %q, want a unique store key distinct from the inherited trace ID", traceID)
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

	// The exported W3C trace ID is preserved on both the trace and its spans;
	// only the store key returned by CreateTrace is the per-request handle.
	if trace.TraceID != inheritedTraceID {
		t.Errorf("trace.TraceID = %q, want inherited %q", trace.TraceID, inheritedTraceID)
	}
	if trace.RootSpan.TraceID != inheritedTraceID {
		t.Errorf("Root span TraceID = %q, want inherited %q", trace.RootSpan.TraceID, inheritedTraceID)
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

// A cancelled stream reaches PopulateLLMResponseAttributes with an accumulated
// response whose usage exists but reads zero (the final usage chunk never
// arrived) and an error carrying the authoritative BilledUsage. The response
// side's zero aggregates must not survive onto the span: PopulateErrorAttributes
// gates its emissions on > 0, so a details-only BilledUsage would otherwise
// leave a false zero in gen_ai.usage.*.
func TestTracer_PopulateLLMResponseAttributesDropsZeroAggregatesWhenBilled(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	traceID := tracer.CreateTrace("")
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	_, handle := tracer.StartSpan(ctx, "llm.call", schemas.SpanKindLLMCall)

	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 0, CompletionTokens: 0, TotalTokens: 0},
		},
	}
	bifrostErr := &schemas.BifrostError{Error: &schemas.ErrorField{Message: "client cancelled the stream"}}
	bifrostErr.ExtraFields.RequestType = schemas.ChatCompletionStreamRequest
	bifrostErr.ExtraFields.BilledUsage = &schemas.BifrostLLMUsage{
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{CachedWriteTokens5m: 120},
		},
	}

	bctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	tracer.PopulateLLMResponseAttributes(bctx, handle, resp, bifrostErr)

	span := store.GetTrace(traceID).RootSpan
	for _, key := range []string{schemas.AttrInputTokens, schemas.AttrOutputTokens, schemas.AttrTotalTokens} {
		if v, ok := span.Attributes[key]; ok {
			t.Errorf("zero-valued response aggregate %s = %v survived onto the billed failed span", key, v)
		}
	}
	if got := span.Attributes[schemas.AttrPromptTokenDetailsCachedWrite5m]; got != 120 {
		t.Errorf("attribute %s = %v, want 120", schemas.AttrPromptTokenDetailsCachedWrite5m, got)
	}
}

func TestTracer_PopulateLLMResponseAttributesRecordsComplexityScore(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	traceID := tracer.CreateTrace("")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyTraceID, traceID)
	ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityScore, 0.875)

	_, handle := tracer.StartSpan(ctx, "llm-call", schemas.SpanKindLLMCall)
	tracer.PopulateLLMResponseAttributes(ctx, handle, nil, nil)

	trace := store.GetTrace(traceID)
	require.NotNil(t, trace)
	require.Equal(t, 0.875, trace.RootSpan.Attributes[schemas.AttrBifrostComplexityScore])
}

func TestTracer_GetSpanHandleByID_RootSpan(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	traceID := tracer.CreateTrace("")
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)

	rootCtx, rootHandle := tracer.StartSpan(ctx, "http-request", schemas.SpanKindHTTPRequest)
	_, childHandle := tracer.StartSpan(rootCtx, "llm-call", schemas.SpanKindLLMCall)

	tracer.SetAttribute(childHandle, "child.attr", "child-value")

	handle := tracer.GetSpanHandleByID(traceID, nil)
	if handle == nil {
		t.Fatal("GetSpanHandleByID(traceID, nil) returned nil")
	}
	tracer.SetAttribute(handle, "custom.root_attr", "root-value")

	trace := store.GetTrace(traceID)
	if trace.RootSpan.Attributes["custom.root_attr"] != "root-value" {
		t.Fatalf("root span custom attr = %v, want root-value", trace.RootSpan.Attributes["custom.root_attr"])
	}
	if trace.RootSpan.Attributes["child.attr"] != nil {
		t.Fatalf("child attr leaked to root span: %v", trace.RootSpan.Attributes["child.attr"])
	}

	childSpanID := childHandle.(*spanHandle).spanID
	childSpan := trace.GetSpan(childSpanID)
	if childSpan.Attributes["custom.root_attr"] != nil {
		t.Fatalf("root attr leaked to child span: %v", childSpan.Attributes["custom.root_attr"])
	}

	if rootHandle.(*spanHandle).spanID != trace.RootSpan.SpanID {
		t.Fatalf("root handle span ID = %q, want %q", rootHandle.(*spanHandle).spanID, trace.RootSpan.SpanID)
	}
}

func TestTracer_GetSpanHandleByID_ExplicitSpan(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	traceID := tracer.CreateTrace("")
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)

	rootCtx, _ := tracer.StartSpan(ctx, "http-request", schemas.SpanKindHTTPRequest)
	_, childHandle := tracer.StartSpan(rootCtx, "llm-call", schemas.SpanKindLLMCall)
	childSpanID := childHandle.(*spanHandle).spanID

	handle := tracer.GetSpanHandleByID(traceID, &childSpanID)
	if handle == nil {
		t.Fatal("GetSpanHandleByID(traceID, childSpanID) returned nil")
	}
	tracer.SetAttribute(handle, "custom.child_attr", "child-value")

	trace := store.GetTrace(traceID)
	childSpan := trace.GetSpan(childSpanID)
	if childSpan.Attributes["custom.child_attr"] != "child-value" {
		t.Fatalf("child span custom attr = %v, want child-value", childSpan.Attributes["custom.child_attr"])
	}
	if trace.RootSpan.Attributes["custom.child_attr"] != nil {
		t.Fatalf("child attr leaked to root span: %v", trace.RootSpan.Attributes["custom.child_attr"])
	}
}

func TestTracer_GetSpanHandleByID_MissingInputs(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	if handle := tracer.GetSpanHandleByID("", nil); handle != nil {
		t.Fatalf("empty trace ID handle = %v, want nil", handle)
	}
	if handle := tracer.GetSpanHandleByID("missing-trace", nil); handle != nil {
		t.Fatalf("missing trace handle = %v, want nil", handle)
	}

	traceID := tracer.CreateTrace("")
	if handle := tracer.GetSpanHandleByID(traceID, nil); handle != nil {
		t.Fatalf("trace with no root span handle = %v, want nil", handle)
	}

	missingSpanID := "missing-span"
	if handle := tracer.GetSpanHandleByID(traceID, &missingSpanID); handle != nil {
		t.Fatalf("missing span handle = %v, want nil", handle)
	}

	emptySpanID := ""
	if handle := tracer.GetSpanHandleByID(traceID, &emptySpanID); handle != nil {
		t.Fatalf("empty span ID handle = %v, want nil", handle)
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

	// All spans carry the inherited W3C trace identity; the store key returned
	// by CreateTrace is a separate per-request handle.
	if httpSpan.TraceID != inheritedTraceID || llmSpan.TraceID != inheritedTraceID || pluginSpan.TraceID != inheritedTraceID {
		t.Error("All spans should have the inherited trace ID")
	}
	if trace.TraceID != inheritedTraceID {
		t.Errorf("trace.TraceID = %q, want inherited %q", trace.TraceID, inheritedTraceID)
	}

	t.Logf("Trace structure (for Datadog):")
	t.Logf("  Trace ID: %s", trace.TraceID)
	t.Logf("  External Parent Span: %s (from Datadog)", externalParentSpanID)
	t.Logf("    -> HTTP Span: %s (ParentID: %s)", httpSpan.SpanID, httpSpan.ParentID)
	t.Logf("      -> LLM Span: %s (ParentID: %s)", llmSpan.SpanID, llmSpan.ParentID)
	t.Logf("        -> Plugin Span: %s (ParentID: %s)", pluginSpan.SpanID, pluginSpan.ParentID)
}

// Span-derived connectors read the classification off the span, so it must land there.
func TestTracer_PopulateLLMResponseAttributesStampsErrorType(t *testing.T) {
	newSpan := func(t *testing.T, requestType schemas.RequestType) (*Tracer, *TraceStore, string, schemas.SpanHandle, *schemas.BifrostContext) {
		t.Helper()
		store := NewTraceStore(5*time.Minute, nil)
		t.Cleanup(store.Stop)
		tracer := NewTracer(store, nil, nil)
		t.Cleanup(tracer.Stop)

		traceID := tracer.CreateTrace("")
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyTraceID, traceID)
		_, handle := tracer.StartSpan(ctx, "llm-call", schemas.SpanKindLLMCall)
		// Mirrors core/bifrost.go, which stamps request.type at span creation.
		tracer.SpanFromHandle(handle).SetAttribute(schemas.AttrLegacyRequestType, string(requestType))
		return tracer, store, traceID, handle, ctx
	}

	t.Run("provider 404 on an embedding", func(t *testing.T) {
		tracer, store, traceID, handle, ctx := newSpan(t, schemas.EmbeddingRequest)
		status := 404
		tracer.PopulateLLMResponseAttributes(ctx, handle, nil, &schemas.BifrostError{
			StatusCode: &status,
			// The raw provider type must not win over the classification.
			Error: &schemas.ErrorField{Type: schemas.Ptr("invalid_request_error")},
		})

		got := store.GetTrace(traceID).RootSpan.Attributes[schemas.AttrBifrostErrorType]
		if got != string(schemas.ErrorTypeCallerModelUnknown) {
			t.Errorf("attribute %s = %v, want %q", schemas.AttrBifrostErrorType, got, schemas.ErrorTypeCallerModelUnknown)
		}
	})

	t.Run("a declaration wins over the status", func(t *testing.T) {
		tracer, store, traceID, handle, ctx := newSpan(t, schemas.ChatCompletionRequest)
		status := 403
		bifrostErr := &schemas.BifrostError{StatusCode: &status}
		bifrostErr.ExtraFields.ErrorType = schemas.ErrorTypePolicyModelBlocked
		tracer.PopulateLLMResponseAttributes(ctx, handle, nil, bifrostErr)

		got := store.GetTrace(traceID).RootSpan.Attributes[schemas.AttrBifrostErrorType]
		if got != string(schemas.ErrorTypePolicyModelBlocked) {
			t.Errorf("attribute %s = %v, want %q", schemas.AttrBifrostErrorType, got, schemas.ErrorTypePolicyModelBlocked)
		}
	})

	t.Run("absent on success", func(t *testing.T) {
		tracer, store, traceID, handle, ctx := newSpan(t, schemas.ChatCompletionRequest)
		tracer.PopulateLLMResponseAttributes(ctx, handle, nil, nil)

		if _, ok := store.GetTrace(traceID).RootSpan.Attributes[schemas.AttrBifrostErrorType]; ok {
			t.Errorf("attribute %s present on a successful span", schemas.AttrBifrostErrorType)
		}
	})
}

// pluginSpanDemandStub declares its plugin-span and overhead demand explicitly.
type pluginSpanDemandStub struct {
	name           string
	wantsPlugin    bool
	statesPlugin   bool
	wantsOverhead  bool
	statesOverhead bool
}

func (p *pluginSpanDemandStub) GetName() string                                  { return p.name }
func (p *pluginSpanDemandStub) Inject(_ context.Context, _ *schemas.Trace) error { return nil }
func (p *pluginSpanDemandStub) Cleanup() error                                   { return nil }

// ConsumesPluginSpans is only consulted when statesPlugin is set; a stub that
// leaves it unset stands in for a connector predating the interface.
func (p *pluginSpanDemandStub) ConsumesPluginSpans() bool {
	if !p.statesPlugin {
		return true
	}
	return p.wantsPlugin
}

func (p *pluginSpanDemandStub) ConsumesOverheadSpans() bool {
	if !p.statesOverhead {
		return false
	}
	return p.wantsOverhead
}

// TestPluginSpanDemandGate pins when plugin hook spans are created. Skipping is
// irreversible, so the default in every ambiguous case must be to create them.
func TestPluginSpanDemandGate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		plugins []schemas.ObservabilityPlugin
		want    bool
	}{
		{
			name:    "no connector attached",
			plugins: []schemas.ObservabilityPlugin{},
			want:    false,
		},
		{
			name:    "connector silent on plugin spans",
			plugins: []schemas.ObservabilityPlugin{&pluginSpanDemandStub{name: "silent"}},
			want:    true,
		},
		{
			name: "connector declines plugin spans",
			plugins: []schemas.ObservabilityPlugin{
				&pluginSpanDemandStub{name: "declines", statesPlugin: true, wantsPlugin: false},
			},
			want: false,
		},
		{
			name: "connector declines spans but consumes the overhead breakdown",
			plugins: []schemas.ObservabilityPlugin{
				&pluginSpanDemandStub{name: "overhead", statesPlugin: true, wantsPlugin: false,
					statesOverhead: true, wantsOverhead: true},
			},
			want: true,
		},
		{
			name: "one connector declines, another does not",
			plugins: []schemas.ObservabilityPlugin{
				&pluginSpanDemandStub{name: "declines", statesPlugin: true, wantsPlugin: false},
				&pluginSpanDemandStub{name: "silent"},
			},
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewTraceStore(5*time.Minute, nil)
			tracer := NewTracer(store, nil, nil)
			defer tracer.Stop()
			tracer.SetObservabilityPlugins(tc.plugins, nil)

			traceID := tracer.CreateTrace("")
			ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
			ctx, root := tracer.StartSpan(ctx, "http-request", schemas.SpanKindHTTPRequest)
			if root == nil {
				t.Fatal("root span was skipped; only plugin spans are gated")
			}

			_, pluginHandle := tracer.StartSpanID(ctx, "plugin.governance.prehook", schemas.SpanKindPlugin)
			if got := pluginHandle != nil; got != tc.want {
				t.Errorf("plugin span created = %v, want %v", got, tc.want)
			}

			// An LLM span is never gated, whatever the demand.
			if _, llm := tracer.StartSpanID(ctx, "chat gpt-4o", schemas.SpanKindLLMCall); llm == nil {
				t.Error("LLM span was skipped; only plugin spans are gated")
			}
		})
	}
}

// TestPluginSpanDemandUnknownCreatesSpans covers the boot window: a tracer whose
// plugins have not been registered yet must create plugin spans, since unknown
// demand is not the same as no demand.
func TestPluginSpanDemandUnknownCreatesSpans(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	traceID := tracer.CreateTrace("")
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	ctx, _ = tracer.StartSpan(ctx, "http-request", schemas.SpanKindHTTPRequest)

	if _, handle := tracer.StartSpanID(ctx, "plugin.governance.prehook", schemas.SpanKindPlugin); handle == nil {
		t.Error("plugin span skipped before SetObservabilityPlugins ran; unknown demand must not drop spans")
	}
}

// TestPluginSpanDemandRecomputedOnReload covers adding a connector after boot:
// the transport calls SetObservabilityPlugins again (server.reloadObservabilityPlugins),
// which recomputes demand, so requests starting after that create plugin spans.
// Requests already in flight keep whatever the demand was when their spans were
// created, which is why an empty plugin set is only ever declared deliberately.
func TestPluginSpanDemandRecomputedOnReload(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	startPluginSpan := func() bool {
		traceID := tracer.CreateTrace("")
		ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
		ctx, _ = tracer.StartSpan(ctx, "http-request", schemas.SpanKindHTTPRequest)
		_, handle := tracer.StartSpanID(ctx, "plugin.governance.prehook", schemas.SpanKindPlugin)
		return handle != nil
	}

	// Boot with no connectors: plugin spans are skipped.
	tracer.SetObservabilityPlugins(nil, nil)
	if startPluginSpan() {
		t.Error("plugin span created with no connector attached")
	}

	// A connector is configured at runtime.
	tracer.SetObservabilityPlugins(
		[]schemas.ObservabilityPlugin{&pluginSpanDemandStub{name: "late"}}, nil)
	if !startPluginSpan() {
		t.Error("plugin span skipped after a connector was added; demand must be recomputed on reload")
	}

	// And removed again.
	tracer.SetObservabilityPlugins(nil, nil)
	if startPluginSpan() {
		t.Error("plugin span created after the last connector was removed")
	}
}

// A Responses API refusal must reach the llm.call span as both the spec'd
// gen_ai.response.finish_reasons list and the legacy singular
// gen_ai.response.finish_reason, exactly like a chat completion does. Before
// the fix the Responses populator never emitted the key, so the tracer had
// nothing to derive the singular from and both attributes were absent.
func TestTracer_PopulateLLMResponseAttributesEmitsResponsesFinishReason(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	traceID := tracer.CreateTrace("")
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyTraceID, traceID)
	_, handle := tracer.StartSpan(ctx, "llm.call", schemas.SpanKindLLMCall)

	resp := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			ID:         schemas.Ptr("resp_refusal"),
			Model:      "gpt-4o-mini",
			StopReason: schemas.Ptr("refusal"),
		},
	}

	bctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	tracer.PopulateLLMResponseAttributes(bctx, handle, resp, nil)

	span := store.GetTrace(traceID).RootSpan
	require.Equal(t, []string{"refusal"}, span.Attributes[schemas.AttrFinishReasons])
	require.Equal(t, "refusal", span.Attributes[schemas.AttrFinishReason])
}

// Before SetObservabilityPlugins runs, demand is unknown rather than absent. A
// request in flight during boot must still get its attributes; declaring an
// empty plugin set is what expresses "nothing is listening".
func TestDemandUnknownBeforeRegistrationIsFull(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()
	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	if d := tracer.Demand(); !d.Any || !d.Content {
		t.Errorf("unregistered tracer demand = %+v, want full", d)
	}

	// An explicit empty set is the opposite: nothing is listening.
	tracer.SetObservabilityPlugins(nil, nil)
	if d := tracer.Demand(); d.Any || d.Content {
		t.Errorf("empty plugin set demand = %+v, want zero", d)
	}
}

// Raw demand must be recomputed on reload, like plugin-span demand: a connector
// added after boot gets raw, and removing the last one stops building it.
func TestRawPayloadDemandRecomputedOnReload(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()
	tracer := NewTracer(store, nil, nil)
	defer tracer.Stop()

	// Before registration, demand is unknown — raw must stay off.
	if tracer.Demand().RawPayloads {
		t.Error("raw demanded before any connector registered")
	}

	tracer.SetObservabilityPlugins(
		[]schemas.ObservabilityPlugin{&rawDemandStub{name: "quiet", wants: false}}, nil)
	if tracer.Demand().RawPayloads {
		t.Error("raw demanded when the only connector declined")
	}

	tracer.SetObservabilityPlugins(
		[]schemas.ObservabilityPlugin{
			&rawDemandStub{name: "quiet", wants: false},
			&rawDemandStub{name: "loud", wants: true},
		}, nil)
	if !tracer.Demand().RawPayloads {
		t.Error("raw not demanded after a consuming connector was added")
	}

	tracer.SetObservabilityPlugins(nil, nil)
	if tracer.Demand().RawPayloads {
		t.Error("raw still demanded after the last connector was removed")
	}
}

type rawDemandStub struct {
	name  string
	wants bool
}

func (p *rawDemandStub) GetName() string                                  { return p.name }
func (p *rawDemandStub) Inject(_ context.Context, _ *schemas.Trace) error { return nil }
func (p *rawDemandStub) Cleanup() error                                   { return nil }
func (p *rawDemandStub) ConsumesRawPayloads() bool                        { return p.wants }
