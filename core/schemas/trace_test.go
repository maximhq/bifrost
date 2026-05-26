package schemas

import (
	"sync"
	"testing"
)

// TestTrace_MergeAttributes_LazyInit verifies that calling MergeAttributes on a
// trace with no pre-existing Attributes map allocates the map on demand.
func TestTrace_MergeAttributes_LazyInit(t *testing.T) {
	trace := &Trace{}
	if trace.Attributes != nil {
		t.Fatalf("expected Attributes to start nil, got %v", trace.Attributes)
	}

	trace.MergeAttributes(map[string]any{"customer_id": "acme"})

	if trace.Attributes == nil {
		t.Fatal("expected Attributes to be initialized after MergeAttributes")
	}
	if got := trace.Attributes["customer_id"]; got != "acme" {
		t.Errorf("Attributes[customer_id] = %v, want %q", got, "acme")
	}
}

// TestTrace_MergeAttributes_OverwritesOnConflict verifies that a later
// MergeAttributes call overwrites earlier values for the same key.
func TestTrace_MergeAttributes_OverwritesOnConflict(t *testing.T) {
	trace := &Trace{}
	trace.MergeAttributes(map[string]any{"environment": "staging"})
	trace.MergeAttributes(map[string]any{"environment": "prod"})

	if got := trace.Attributes["environment"]; got != "prod" {
		t.Errorf("Attributes[environment] = %v, want %q", got, "prod")
	}
}

// TestTrace_MergeAttributes_NoopOnEmpty verifies that nil/empty input is a true
// no-op: Attributes is not allocated.
func TestTrace_MergeAttributes_NoopOnEmpty(t *testing.T) {
	t.Run("nil map", func(t *testing.T) {
		trace := &Trace{}
		trace.MergeAttributes(nil)
		if trace.Attributes != nil {
			t.Errorf("expected Attributes to remain nil for nil input, got %v", trace.Attributes)
		}
	})

	t.Run("empty map", func(t *testing.T) {
		trace := &Trace{}
		trace.MergeAttributes(map[string]any{})
		if trace.Attributes != nil {
			t.Errorf("expected Attributes to remain nil for empty input, got %v", trace.Attributes)
		}
	})
}

// TestTrace_MergeAttributes_ConcurrentWithAddSpan exercises the trace mutex by
// firing MergeAttributes and AddSpan from many goroutines concurrently. Run
// under `go test -race` to catch any unsynchronized access.
func TestTrace_MergeAttributes_ConcurrentWithAddSpan(t *testing.T) {
	trace := &Trace{}
	const workers = 16
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(workers * 2)

	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				trace.MergeAttributes(map[string]any{
					"customer_id": "acme",
					"environment": "prod",
				})
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				trace.AddSpan(&Span{SpanID: "s"})
			}
		}()
	}

	wg.Wait()

	if got := trace.Attributes["customer_id"]; got != "acme" {
		t.Errorf("final Attributes[customer_id] = %v, want %q", got, "acme")
	}
	if got := trace.Attributes["environment"]; got != "prod" {
		t.Errorf("final Attributes[environment] = %v, want %q", got, "prod")
	}
	if len(trace.Spans) != workers*iterations {
		t.Errorf("Spans length = %d, want %d", len(trace.Spans), workers*iterations)
	}
}
