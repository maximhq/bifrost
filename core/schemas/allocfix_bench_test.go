package schemas

// Deterministic before/after microbenchmarks for the overhead-latency fix batch.
// Wall-clock p99 on the load harness is too noisy to see sub-microsecond,
// per-request changes; -benchmem allocs/op is deterministic and is the honest
// proof for these fixes.
//
// Run:
//
//	go test ./core/schemas/ -run '^$' -bench 'FixBatch' -benchmem
//
// Interpretation:
//   - Timer + SpanReset are ALLOCATION fixes: expect allocs/op to drop to ~0.
//   - ReservedKey is a CPU/latency fix (fewer comparisons): expect ns/op to
//     drop, allocs/op unchanged (0 in both).

import (
	"context"
	"slices"
	"testing"
	"time"
)

// ---- Fix 1: per-worker reusable delivery timer vs time.After per request ----

// benchTimerDur is large so a fresh timer never actually fires inside the loop;
// we measure only the cost of arming it, which is what the request path pays.
const benchTimerDur = time.Hour

// Old path: the worker armed time.After(5s) on every request. time.After is
// NewTimer(d).C, so each call heap-allocates a Timer and its channel. Stop() so
// the benchmark does not pile up b.N live timers (the leak we are fixing); the
// allocation measured is identical to time.After's.
func BenchmarkFixBatch_Timer_PerRequest(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		t := time.NewTimer(benchTimerDur)
		t.Stop()
	}
}

// New path: one timer per (long-lived) worker, reset per request.
func BenchmarkFixBatch_Timer_Reused(b *testing.B) {
	t := time.NewTimer(benchTimerDur)
	t.Stop()
	b.ReportAllocs()
	for b.Loop() {
		t.Reset(benchTimerDur)
		t.Stop()
	}
}

// ---- Fix 2: Span.Reset reuses the attribute map vs nil-ing it ----

// benchSpanAttrKeys/Vals model a typical span's attribute set. Values are
// pre-boxed into []any so interface boxing is constant across both benchmarks
// and the only allocation difference is the attribute map itself.
var (
	benchSpanAttrKeys = []string{
		"bifrost.provider", "bifrost.model", "bifrost.request.id",
		"bifrost.upstream.duration_ms", "http.status_code", "gen_ai.system",
		"span.kind", "bifrost.stream",
	}
	benchSpanAttrVals = []any{
		"openai", "gpt-4o", "req-abcdef", 1439.0, 200, "openai", "llm.call", true,
	}
)

func populateSpanAttrs(s *Span) {
	for i, k := range benchSpanAttrKeys {
		s.SetAttribute(k, benchSpanAttrVals[i])
	}
}

// New path: the real Span.Reset (clear + reuse). Steady state re-fills a cleared
// map with retained capacity, so no map allocation after the first cycle.
func BenchmarkFixBatch_SpanReset_Reuse(b *testing.B) {
	s := &Span{}
	populateSpanAttrs(s) // warm the map once so capacity is retained
	s.Reset()
	b.ReportAllocs()
	for b.Loop() {
		populateSpanAttrs(s)
		s.Reset()
	}
}

// Old path: replicate the pre-fix Reset (s.Attributes = nil). The next
// SetAttribute then make()s a fresh map every cycle: one map alloc per pooled
// span per request.
func oldSpanReset(s *Span) {
	s.SpanID = ""
	s.ParentID = ""
	s.TraceID = ""
	s.Name = ""
	s.Kind = SpanKindUnspecified
	s.StartTime = time.Time{}
	s.EndTime = time.Time{}
	s.Status = SpanStatusUnset
	s.StatusMsg = ""
	s.Attributes = nil
	s.Events = s.Events[:0]
}

func BenchmarkFixBatch_SpanReset_Nil(b *testing.B) {
	s := &Span{}
	populateSpanAttrs(s)
	oldSpanReset(s)
	b.ReportAllocs()
	for b.Loop() {
		populateSpanAttrs(s)
		oldSpanReset(s)
	}
}

// ---- Fix 3: reserved-key check, map lookup vs slices.Contains linear scan ----

// oldReservedSlice mirrors the pre-fix []any, built from the same key set so
// membership semantics match exactly. Miss case = full scan (the worst case,
// and the common case since most context writes are not reserved keys).
func oldReservedSlice() []any {
	s := make([]any, 0, len(reservedKeys))
	for k := range reservedKeys {
		s = append(s, k)
	}
	return s
}

var benchReservedMissKey any = BifrostContextKey("x-not-a-reserved-key")

func BenchmarkFixBatch_ReservedKey_SliceScan(b *testing.B) {
	old := oldReservedSlice()
	b.ReportAllocs()
	var ok bool
	for b.Loop() {
		ok = slices.Contains(old, benchReservedMissKey)
	}
	_ = ok
}

func BenchmarkFixBatch_ReservedKey_MapLookup(b *testing.B) {
	b.ReportAllocs()
	var ok bool
	for b.Loop() {
		ok = isReservedKey(benchReservedMissKey)
	}
	_ = ok
}

// ---- Fix 4: logging plugin skips the identity/governance context reads on
// non-final stream chunks. This is a CPU/latency fix, not an allocation fix:
// each read is one RLock + map lookup (BifrostContext.Value), returning an
// already-boxed value, so allocs/op is ~0. The number below is the per-chunk
// cost the gate now avoids; multiply by (chunks - 1) for a stream's saving. ----

// loggingIdentityKeys mirrors the block moved past the non-final-chunk gate in
// plugins/logging/main.go (the ~19 identity/governance lookups feeding
// applyOutputFieldsToEntry).
var loggingIdentityKeys = []any{
	BifrostContextKeySelectedKeyID, BifrostContextKeySelectedKeyName,
	BifrostContextKeyGovernanceVirtualKeyID, BifrostContextKeyGovernanceVirtualKeyName,
	BifrostContextKeyGovernanceRoutingRuleID, BifrostContextKeyGovernanceRoutingRuleName,
	BifrostContextKeySelectedPromptName, BifrostContextKeySelectedPromptVersion,
	BifrostContextKeySelectedPromptID, BifrostContextKeyGovernanceTeamID,
	BifrostContextKeyGovernanceTeamName, BifrostContextKeyGovernanceCustomerID,
	BifrostContextKeyGovernanceCustomerName, BifrostContextKeyUserID,
	BifrostContextKeyUserName, BifrostContextKeyGovernanceBusinessUnitID,
	BifrostContextKeyGovernanceBusinessUnitName, BifrostContextKeyGovernanceProjectID,
	BifrostContextKeyGovernanceProjectName, BifrostContextKeyNumberOfRetries,
	BifrostContextKeyAttemptTrail,
}

func newBenchIdentityContext() *BifrostContext {
	bc := NewBifrostContext(context.Background(), time.Now().Add(time.Hour))
	for _, k := range loggingIdentityKeys {
		bc.SetValue(k, "v")
	}
	return bc
}

// The per-non-final-chunk work the gate now skips: read the full identity block.
func BenchmarkFixBatch_LoggingPerChunkReads(b *testing.B) {
	bc := newBenchIdentityContext()
	b.ReportAllocs()
	var sink any
	for b.Loop() {
		for _, k := range loggingIdentityKeys {
			sink = bc.Value(k)
		}
	}
	_ = sink
}

// ---- Fix C5: presize the per-request userValues map (~25 SetValue writes) ----
var benchC5Keys = func() []any {
	k := make([]any, 25)
	for i := range k {
		k[i] = BifrostContextKey("k" + string(rune('a'+i)))
	}
	return k
}()

func BenchmarkFixC5_MapSize0(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		m := make(map[any]any)
		for _, k := range benchC5Keys {
			m[k] = "v"
		}
	}
}

func BenchmarkFixC5_MapPresized(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		m := make(map[any]any, 32)
		for _, k := range benchC5Keys {
			m[k] = "v"
		}
	}
}

func BenchmarkFixC5_MapHint16(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		m := make(map[any]any, 16)
		for _, k := range benchC5Keys {
			m[k] = "v"
		}
	}
}
