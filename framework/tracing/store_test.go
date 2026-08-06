package tracing

import (
	"sync"
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

	// The returned handle is an opaque in-process storage key, NOT the
	// inherited W3C trace ID. Reusing the W3C ID as the storage key would
	// cause concurrent requests sharing the same upstream trace to overwrite
	// each other in the store; see TestCreateTrace_ConcurrentSameW3CTraceID.
	if traceID == inheritedTraceID {
		t.Errorf("CreateTrace() returned the W3C inherited trace ID %q as the storage handle; storage handle must be opaque", traceID)
	}
	if traceID == "" {
		t.Fatal("CreateTrace() returned empty storage handle")
	}

	trace := store.GetTrace(traceID)
	if trace == nil {
		t.Fatal("GetTrace() returned nil")
	}

	// The W3C trace ID is preserved on the trace for OTEL export.
	if trace.TraceID != inheritedTraceID {
		t.Errorf("trace.TraceID = %q, want %q", trace.TraceID, inheritedTraceID)
	}

	// The internal storage handle round-trips on the trace itself so that
	// plugins receiving the trace can correlate it back to per-request state.
	if trace.InternalID != traceID {
		t.Errorf("trace.InternalID = %q, want %q", trace.InternalID, traceID)
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

	// The opaque storage handle is generated using the same scheme as the
	// W3C trace ID generator: 32 hex characters.
	if len(traceID) != 32 {
		t.Errorf("Storage handle length = %d, want 32", len(traceID))
	}

	if !isHex(traceID) {
		t.Errorf("Storage handle %q is not valid hex", traceID)
	}

	trace := store.GetTrace(traceID)
	if trace == nil {
		t.Fatal("GetTrace() returned nil")
	}

	// When there's no inherited W3C trace ID, one is generated for export.
	// The storage handle and the W3C trace ID are independent values.
	if trace.TraceID == "" {
		t.Error("trace.TraceID should be generated when no inherited ID is provided")
	}
	if trace.TraceID == traceID {
		t.Error("trace.TraceID should be independent of the storage handle")
	}
	if trace.InternalID != traceID {
		t.Errorf("trace.InternalID = %q, want %q", trace.InternalID, traceID)
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

	trace := store.GetTrace(traceID)
	if trace == nil {
		t.Fatal("GetTrace() returned nil")
	}

	// span.TraceID should reflect the W3C trace ID (what gets exported),
	// not the in-process storage handle.
	if span.TraceID != trace.TraceID {
		t.Errorf("span.TraceID = %q, want %q (W3C trace ID, not storage handle %q)", span.TraceID, trace.TraceID, traceID)
	}

	// Verify it's set as root span
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

	// span.TraceID is the W3C trace ID, not the in-process storage handle.
	trace := store.GetTrace(traceID)
	if childSpan.TraceID != trace.TraceID {
		t.Errorf("child span.TraceID = %q, want %q (W3C trace ID)", childSpan.TraceID, trace.TraceID)
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

	// CompleteTrace returns the trace stored under the opaque storage handle;
	// the W3C trace.TraceID is independent of that handle.
	if trace.InternalID != traceID {
		t.Errorf("trace.InternalID = %q, want %q", trace.InternalID, traceID)
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

// TestCreateTrace_ConcurrentSameW3CTraceID is the regression test for the
// orphan-span / lost-parent-link bug observed when many concurrent Bifrost
// requests arrive under the same upstream W3C trace ID (e.g. a client doing
// parallel POSTs under one distributed trace).
//
// Before the fix, CreateTrace keyed s.traces by the W3C trace ID, so each
// concurrent CreateTrace(sameID) overwrote the previous *Trace. Every
// subsequent StartSpan/StartChildSpan with the same handle then wrote into
// whichever *Trace happened to be currently registered, producing spans whose
// ParentID referenced evicted (and now unreachable) root spans. CompleteTrace
// further LoadAndDeleted the only surviving entry, silently dropping all
// later plugin spans.
//
// After the fix, CreateTrace returns an opaque per-request storage handle so
// every concurrent request gets its own independent *Trace.
func TestCreateTrace_ConcurrentSameW3CTraceID(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	// All N requests arrive under the same W3C trace ID.
	sharedW3CTraceID := "69538b980000000079943934f90c1d40"
	externalParentSpanID := "aad09d1659b4c7e3"

	const N = 32
	type result struct {
		handle        string
		rootSpanID    string
		childSpanID   string
		completedRoot *schemas.Span
	}
	results := make([]result, N)

	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each request gets its own opaque storage handle even though
			// they all share sharedW3CTraceID.
			handle := store.CreateTrace(sharedW3CTraceID)
			// Mimic the HTTP middleware: root span is a child of the external
			// W3C parent span.
			root := store.StartChildSpan(handle, externalParentSpanID, "/v1/embeddings", schemas.SpanKindHTTPRequest)
			if root == nil {
				t.Errorf("goroutine %d: StartChildSpan(root) returned nil", i)
				return
			}
			// One nested plugin span beneath the root.
			child := store.StartChildSpan(handle, root.SpanID, "plugin.telemetry.prehook", schemas.SpanKindPlugin)
			if child == nil {
				t.Errorf("goroutine %d: StartChildSpan(child) returned nil", i)
				return
			}
			results[i] = result{handle: handle, rootSpanID: root.SpanID, childSpanID: child.SpanID}
		}(i)
	}
	wg.Wait()

	// All handles must be unique.
	seen := make(map[string]int, N)
	for i, r := range results {
		if r.handle == "" {
			t.Fatalf("goroutine %d: empty handle", i)
		}
		if prev, ok := seen[r.handle]; ok {
			t.Fatalf("handle collision: goroutines %d and %d both got handle %q", prev, i, r.handle)
		}
		seen[r.handle] = i
		// Reusing the W3C trace ID as the storage handle is the bug we're guarding against.
		if r.handle == sharedW3CTraceID {
			t.Fatalf("goroutine %d: handle equals shared W3C trace ID; storage handle must be opaque", i)
		}
	}

	// Each handle must resolve to its own independent *Trace with the
	// expected spans, and CompleteTrace must hand back that exact trace.
	for i, r := range results {
		live := store.GetTrace(r.handle)
		if live == nil {
			t.Fatalf("goroutine %d: GetTrace(%q) returned nil before completion", i, r.handle)
		}
		if live.TraceID != sharedW3CTraceID {
			t.Errorf("goroutine %d: live.TraceID = %q, want shared W3C %q", i, live.TraceID, sharedW3CTraceID)
		}
		if live.RootSpan == nil || live.RootSpan.SpanID != r.rootSpanID {
			t.Errorf("goroutine %d: live.RootSpan = %v, want span %q", i, live.RootSpan, r.rootSpanID)
		}
		if got := len(live.Spans); got != 2 {
			t.Errorf("goroutine %d: len(live.Spans) = %d, want 2", i, got)
		}
		// Child span's parent must be this trace's own root, not some other
		// goroutine's root — exactly the orphan-link scenario this test guards.
		if child := live.GetSpan(r.childSpanID); child == nil || child.ParentID != r.rootSpanID {
			t.Errorf("goroutine %d: child span parent = %v, want %q", i, child, r.rootSpanID)
		}
	}

	// CompleteTrace must return each trace independently.
	for i, r := range results {
		completed := store.CompleteTrace(r.handle)
		if completed == nil {
			t.Fatalf("goroutine %d: CompleteTrace(%q) returned nil", i, r.handle)
		}
		if completed.InternalID != r.handle {
			t.Errorf("goroutine %d: completed.InternalID = %q, want %q", i, completed.InternalID, r.handle)
		}
		if completed.TraceID != sharedW3CTraceID {
			t.Errorf("goroutine %d: completed.TraceID = %q, want shared W3C %q", i, completed.TraceID, sharedW3CTraceID)
		}
		if completed.RootSpan == nil || completed.RootSpan.SpanID != r.rootSpanID {
			t.Errorf("goroutine %d: completed.RootSpan = %v, want span %q", i, completed.RootSpan, r.rootSpanID)
		}
		if store.GetTrace(r.handle) != nil {
			t.Errorf("goroutine %d: trace not removed from store after CompleteTrace", i)
		}
		// Capture the root span ID *before* releasing — ReleaseTrace returns
		// pooled spans to the pool which zeroes their fields.
		if completed.RootSpan != nil {
			results[i].completedRoot = &schemas.Span{SpanID: completed.RootSpan.SpanID}
		}
		store.ReleaseTrace(completed)
	}

	// Every goroutine must have produced a distinct root span — there is no
	// scenario in which two requests "share" a root span across the wire.
	rootSeen := make(map[string]int, N)
	for i, r := range results {
		if r.completedRoot == nil {
			continue
		}
		if prev, ok := rootSeen[r.completedRoot.SpanID]; ok {
			t.Fatalf("root span collision: goroutines %d and %d both surfaced span %q as root", prev, i, r.completedRoot.SpanID)
		}
		rootSeen[r.completedRoot.SpanID] = i
	}
}

// TestStartSpan_ConcurrentRootElectionIsAtomic guards against a regression of
// the unsynchronized RootSpan election race. Two goroutines calling StartSpan
// on the same fresh trace must agree on a single root; the loser must become
// a child of the winner, not concurrently claim "I am also root".
func TestStartSpan_ConcurrentRootElectionIsAtomic(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	const iterations = 64
	for iter := 0; iter < iterations; iter++ {
		traceID := store.CreateTrace("")

		const N = 8
		spans := make([]*schemas.Span, N)
		var wg sync.WaitGroup
		for i := 0; i < N; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				spans[i] = store.StartSpan(traceID, "race", schemas.SpanKindLLMCall)
			}(i)
		}
		wg.Wait()

		trace := store.GetTrace(traceID)
		if trace == nil {
			t.Fatal("trace missing")
		}
		// Exactly one span must be RootSpan; every other span must be a child of it.
		if trace.RootSpan == nil {
			t.Fatalf("iter %d: no root elected", iter)
		}
		rootID := trace.RootSpan.SpanID
		for i, s := range spans {
			if s == nil {
				t.Fatalf("iter %d goroutine %d: nil span", iter, i)
			}
			if s.SpanID == rootID {
				if s.ParentID != "" {
					t.Errorf("iter %d: elected root span has ParentID %q, want empty", iter, s.ParentID)
				}
				continue
			}
			if s.ParentID != rootID {
				t.Errorf("iter %d goroutine %d: span ParentID = %q, want elected root %q", iter, i, s.ParentID, rootID)
			}
		}
		store.ReleaseTrace(store.CompleteTrace(traceID))
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

func TestSetTraceAttribute(t *testing.T) {
	store := NewTraceStore(5*time.Minute, nil)
	defer store.Stop()

	traceID := store.CreateTrace("")
	dims := map[string]string{"environment": "prod"}
	store.SetTraceAttribute(traceID, schemas.TraceAttrDimensions, dims)
	store.SetTraceAttribute(traceID, schemas.TraceAttrSessionID, "sess-1")

	trace := store.GetTrace(traceID)
	if trace == nil {
		t.Fatal("GetTrace() returned nil")
	}
	dimsAttr, _ := trace.GetAttribute(schemas.TraceAttrDimensions)
	got, ok := dimsAttr.(map[string]string)
	if !ok || got["environment"] != "prod" {
		t.Errorf("trace dimensions attribute = %v, want map with environment=prod", dimsAttr)
	}
	if sessionAttr, _ := trace.GetAttribute(schemas.TraceAttrSessionID); sessionAttr != "sess-1" {
		t.Errorf("trace session attribute = %v, want sess-1", sessionAttr)
	}
	if _, ok := trace.GetAttribute("missing"); ok {
		t.Error("GetAttribute(missing) reported present, want absent")
	}

	// Unknown trace ID must be a no-op, not a panic.
	store.SetTraceAttribute("does-not-exist", "k", "v")
}

func TestCleanupOldTraces_RemovesOrphanedDeferredSpans(t *testing.T) {
	store := NewTraceStore(10*time.Millisecond, nil)
	defer store.Stop()

	// Simulate a streaming request whose trace completer never ran: the trace
	// and its deferred span both exist, and nothing will call CompleteTrace or
	// ClearDeferredSpan for them.
	orphanTraceID := store.CreateTrace("")
	store.StoreDeferredSpan(orphanTraceID, "orphan-span")

	// Let the orphan exceed the TTL, then register a fresh deferred span that
	// must survive the sweep.
	time.Sleep(20 * time.Millisecond)
	freshTraceID := store.CreateTrace("")
	store.StoreDeferredSpan(freshTraceID, "fresh-span")

	store.cleanupOldTraces()

	if store.GetDeferredSpan(orphanTraceID) != nil {
		t.Error("orphaned deferred span should be removed by TTL cleanup")
	}
	if store.GetTrace(orphanTraceID) != nil {
		t.Error("orphaned trace should be removed by TTL cleanup")
	}
	if store.GetDeferredSpan(freshTraceID) == nil {
		t.Error("fresh deferred span should survive TTL cleanup")
	}
	if store.GetTrace(freshTraceID) == nil {
		t.Error("fresh trace should survive TTL cleanup")
	}
}
