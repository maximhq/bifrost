package logging

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// span builds a finished span at the given millisecond offsets from base.
func span(base time.Time, id, parent, name string, kind schemas.SpanKind, startMs, endMs int) *schemas.Span {
	return &schemas.Span{
		SpanID:    id,
		ParentID:  parent,
		Name:      name,
		Kind:      kind,
		StartTime: base.Add(time.Duration(startMs) * time.Millisecond),
		EndTime:   base.Add(time.Duration(endMs) * time.Millisecond),
	}
}

func bucketMap(t *testing.T, trace *schemas.Trace, overheadMs float64, ovOK bool) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	for _, b := range computeOverheadBreakdown(trace, overheadMs, ovOK) {
		out[b.Name] = b.DurationUs
	}
	return out
}

// A typical chat trace: root wraps key.selection, one plugin's pre/post hooks, and
// the llm.call. Overhead decomposes into key.selection and one collapsed plugin
// bucket. The llm.call is upstream, and the root http.request self-time is
// deliberately not bucketed (it can hide streaming socket reads).
func TestComputeOverheadBreakdown_ChatPath(t *testing.T) {
	base := time.Now()
	trace := &schemas.Trace{Spans: []*schemas.Span{
		span(base, "root", "", "/v1/chat/completions", schemas.SpanKindHTTPRequest, 0, 100),
		span(base, "keysel", "root", "key.selection", schemas.SpanKindInternal, 1, 3),
		span(base, "pre", "root", "plugin.governance.prehook", schemas.SpanKindPlugin, 3, 8),
		span(base, "llm", "root", "chat gpt-4o", schemas.SpanKindLLMCall, 8, 95),
		span(base, "post", "root", "plugin.governance.posthook", schemas.SpanKindPlugin, 95, 96),
	}}

	// Total overhead 12ms; measured plugin/internal spans are 8ms, so 4ms lands in core.
	got := bucketMap(t, trace, 12, true)

	if len(got) != 3 {
		t.Fatalf("expected 3 buckets (key.selection, plugin.governance, core), got %v", got)
	}
	if got["key.selection"] != 2000 {
		t.Errorf("key.selection = %v us, want 2000", got["key.selection"])
	}
	// prehook 5ms + posthook 1ms, collapsed to one plugin bucket
	if got["plugin.governance"] != 6000 {
		t.Errorf("plugin.governance = %v us, want 6000", got["plugin.governance"])
	}
	// 12ms overhead - 8ms measured = 4ms attributed to core
	if got["core"] != 4000 {
		t.Errorf("core = %v us, want 4000", got["core"])
	}
	// llm.call (upstream) and the root http.request span are not overhead buckets
	if _, ok := got["chat gpt-4o"]; ok {
		t.Error("llm.call span must not appear as an overhead bucket")
	}
	if _, ok := got["/v1/chat/completions"]; ok {
		t.Error("root http.request span must not appear as an overhead bucket")
	}
}

// When overhead exceeds the measured plugin spans but no plugin/internal spans were
// captured at all, the whole overhead is attributed to core.
func TestComputeOverheadBreakdown_CoreOnly(t *testing.T) {
	base := time.Now()
	trace := &schemas.Trace{Spans: []*schemas.Span{
		span(base, "root", "", "/v1/chat/completions", schemas.SpanKindHTTPRequest, 0, 100),
		span(base, "llm", "root", "chat gpt-4o", schemas.SpanKindLLMCall, 5, 95),
	}}

	got := bucketMap(t, trace, 3, true)
	if len(got) != 1 || got["core"] != 3000 {
		t.Errorf("expected a single core bucket of 3000 us, got %v", got)
	}
}

// When measured spans exceed the computed overhead (upstream over-counting), no
// negative core bucket is emitted; only the measured spans remain.
func TestComputeOverheadBreakdown_NoNegativeCore(t *testing.T) {
	base := time.Now()
	trace := &schemas.Trace{Spans: []*schemas.Span{
		span(base, "root", "", "/v1/chat/completions", schemas.SpanKindHTTPRequest, 0, 100),
		span(base, "pre", "root", "plugin.governance.prehook", schemas.SpanKindPlugin, 0, 10),
	}}

	// Overhead 5ms < measured 10ms: core would be negative, so it is omitted.
	got := bucketMap(t, trace, 5, true)
	if len(got) != 1 || got["plugin.governance"] != 10000 {
		t.Errorf("expected only plugin.governance=10000, got %v", got)
	}
	if _, ok := got["core"]; ok {
		t.Error("core bucket must not be emitted when it would be negative")
	}
}

// The named core phases (transport in/out, request-marshal, response-parse) are
// internal spans that bucket by name; the two transport spans aggregate into one.
func TestComputeOverheadBreakdown_CorePhases(t *testing.T) {
	base := time.Now()
	trace := &schemas.Trace{Spans: []*schemas.Span{
		span(base, "root", "", "/v1/chat/completions", schemas.SpanKindHTTPRequest, 0, 100),
		span(base, "tin", "root", "transport", schemas.SpanKindInternal, 0, 2),
		span(base, "llm", "root", "chat gpt-4o", schemas.SpanKindLLMCall, 2, 90),
		span(base, "marshal", "llm", "request-marshal", schemas.SpanKindInternal, 2, 5),
		span(base, "parse", "llm", "response-parse", schemas.SpanKindInternal, 80, 89),
		span(base, "tout", "root", "transport", schemas.SpanKindInternal, 95, 98),
	}}

	got := bucketMap(t, trace, 0, false)
	if got["transport"] != 5000 { // 2ms in + 3ms out, aggregated
		t.Errorf("transport = %v us, want 5000 (aggregated)", got["transport"])
	}
	if got["request-marshal"] != 3000 {
		t.Errorf("request-marshal = %v us, want 3000", got["request-marshal"])
	}
	if got["response-parse"] != 9000 {
		t.Errorf("response-parse = %v us, want 9000", got["response-parse"])
	}
}

// A plugin span that itself contains a nested child (e.g. an MCP round-trip) counts
// only its self-time: the plugin's own wall duration minus the child's.
func TestComputeOverheadBreakdown_SelfTimeExcludesChildren(t *testing.T) {
	base := time.Now()
	trace := &schemas.Trace{Spans: []*schemas.Span{
		span(base, "root", "", "/v1/chat/completions", schemas.SpanKindHTTPRequest, 0, 100),
		span(base, "pre", "root", "plugin.mcp.prehook", schemas.SpanKindPlugin, 0, 40),
		// MCP round-trip nested under the plugin: subtracted from the plugin's self-time.
		span(base, "tool", "pre", "mcp.execute", schemas.SpanKindMCPTool, 5, 35),
	}}

	// No overhead total supplied: only measured self-time, no core bucket.
	got := bucketMap(t, trace, 0, false)
	// plugin wall 40ms minus 30ms child = 10ms self-time.
	if got["plugin.mcp"] != 10000 {
		t.Errorf("plugin.mcp = %v us, want 10000", got["plugin.mcp"])
	}
	if _, ok := got["core"]; ok {
		t.Error("no core bucket without an overhead total")
	}
}

// key.selection is re-parented as llm.call's parent for trace-hierarchy reasons
// (core/bifrost.go), yet llm.call starts only after key.selection ends. Its wall must
// NOT be subtracted from key.selection's self-time, so key.selection still surfaces
// as its own bucket instead of collapsing into core.
func TestComputeOverheadBreakdown_KeySelectionReparentedLLMCall(t *testing.T) {
	base := time.Now()
	trace := &schemas.Trace{Spans: []*schemas.Span{
		span(base, "root", "", "/v1/chat/completions", schemas.SpanKindHTTPRequest, 0, 100),
		span(base, "keysel", "root", "key.selection", schemas.SpanKindInternal, 1, 3),
		// llm.call is linked UNDER key.selection but runs 3..95 (after keysel ends).
		span(base, "llm", "keysel", "chat gpt-4o", schemas.SpanKindLLMCall, 3, 95),
	}}

	got := bucketMap(t, trace, 5, true)
	// key.selection self-time is its own 2ms, not 2ms - 92ms.
	if got["key.selection"] != 2000 {
		t.Errorf("key.selection = %v us, want 2000", got["key.selection"])
	}
	// 5ms overhead - 2ms key.selection = 3ms core.
	if got["core"] != 3000 {
		t.Errorf("core = %v us, want 3000", got["core"])
	}
}

// When socket time dominates (upstream ~= root), overhead spans are near-zero and
// produce no buckets: the exact "~1 microsecond" symptom this feature diagnoses.
func TestComputeOverheadBreakdown_NegligibleOverhead(t *testing.T) {
	base := time.Now()
	trace := &schemas.Trace{Spans: []*schemas.Span{
		span(base, "root", "", "/v1/chat/completions", schemas.SpanKindHTTPRequest, 0, 100),
		// llm.call covers essentially the whole root window; root self-time ~= 0.
		span(base, "llm", "root", "chat gpt-4o", schemas.SpanKindLLMCall, 0, 100),
	}}

	if got := computeOverheadBreakdown(trace, 0, false); len(got) != 0 {
		t.Errorf("expected no overhead buckets when overhead is negligible, got %v", got)
	}
}

func TestComputeOverheadBreakdown_Empty(t *testing.T) {
	if computeOverheadBreakdown(nil, 0, false) != nil {
		t.Error("nil trace must yield nil")
	}
	if computeOverheadBreakdown(&schemas.Trace{}, 5, true) != nil {
		t.Error("trace with no spans must yield nil")
	}
}
