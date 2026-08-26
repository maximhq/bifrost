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
	buckets, _, _ := computeOverheadBreakdown(trace, overheadMs, ovOK, 0, false)
	for _, b := range buckets {
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
		t.Fatalf("expected 3 buckets (key.selection, plugin.governance, scheduling), got %v", got)
	}
	if got["key.selection"] != 2000 {
		t.Errorf("key.selection = %v us, want 2000", got["key.selection"])
	}
	// prehook 5ms + posthook 1ms, collapsed to one plugin bucket
	if got["plugin.governance"] != 6000 {
		t.Errorf("plugin.governance = %v us, want 6000", got["plugin.governance"])
	}
	// 12ms overhead - 8ms measured = 4ms attributed to scheduling
	if got["scheduling"] != 4000 {
		t.Errorf("scheduling = %v us, want 4000", got["scheduling"])
	}
	// llm.call (upstream) and the root http.request span are not overhead buckets
	if _, ok := got["chat gpt-4o"]; ok {
		t.Error("llm.call span must not appear as an overhead bucket")
	}
	if _, ok := got["/v1/chat/completions"]; ok {
		t.Error("root http.request span must not appear as an overhead bucket")
	}
}

// Regression: a nested phase span (e.g. "credentials-fetch" opened while "request-sign"
// is the active parent, or a plugin hook opened while "handle-setup" is active) must be
// counted ONCE. Because self-time subtracts only DIRECT children, the parent phase must
// truly be the child's parent — if the two were siblings (the bug that motivated
// startCoreSpan/StartScopedPhaseSpan installing themselves as the active parent), the
// overlapping window would be counted in both buckets. Here credentials-fetch (15-25) is
// a child of request-sign (10-30): request-sign self-time = 20ms - 10ms = 10ms, and the
// two buckets sum to 20ms, not 30ms.
func TestComputeOverheadBreakdown_NestedPhaseSpansCountedOnce(t *testing.T) {
	base := time.Now()
	trace := &schemas.Trace{Spans: []*schemas.Span{
		span(base, "root", "", "/v1/chat/completions", schemas.SpanKindHTTPRequest, 0, 100),
		span(base, "sign", "root", "request-sign", schemas.SpanKindInternal, 10, 30),
		span(base, "creds", "sign", "credentials-fetch", schemas.SpanKindInternal, 15, 25),
	}}

	// Overhead 20ms == the two spans' combined self-time, so core is empty.
	got := bucketMap(t, trace, 20, true)

	if got["request-sign"] != 10000 {
		t.Errorf("request-sign = %v us, want 10000 (20ms wall - 10ms nested child)", got["request-sign"])
	}
	if got["credentials-fetch"] != 10000 {
		t.Errorf("credentials-fetch = %v us, want 10000", got["credentials-fetch"])
	}
	// The overlapping 10ms window is counted once, not in both buckets.
	if sum := got["request-sign"] + got["credentials-fetch"]; sum != 20000 {
		t.Errorf("request-sign + credentials-fetch = %v us, want 20000 (no double-count)", sum)
	}
	if _, ok := got["scheduling"]; ok {
		t.Errorf("scheduling should be empty when spans account for all overhead, got %v", got["scheduling"])
	}
}

// Regression: a phase span (e.g. "pipeline-post") that encloses MULTIPLE plugin hook
// spans must subtract ALL of them, not just the first. This is why the plugin loops
// restore the active span after each plugin (making the plugins siblings/direct children
// of the phase span) instead of chaining them. Here pipeline-post (50-90) has three
// direct plugin children (52-60, 60-70, 70-78): its self-time is 40ms - 26ms = 14ms, and
// the phase + three plugin buckets sum to exactly the 40ms wall (no double-count). Were
// the plugins chained (each parented to the previous), pipeline-post would subtract only
// the first and the later two would be counted twice.
func TestComputeOverheadBreakdown_SiblingPluginsUnderPhaseCountedOnce(t *testing.T) {
	base := time.Now()
	trace := &schemas.Trace{Spans: []*schemas.Span{
		span(base, "root", "", "/v1/chat/completions", schemas.SpanKindHTTPRequest, 0, 100),
		span(base, "pp", "root", "pipeline-post", schemas.SpanKindInternal, 50, 90),
		span(base, "a", "pp", "plugin.logging.posthook", schemas.SpanKindPlugin, 52, 60),
		span(base, "b", "pp", "plugin.telemetry.posthook", schemas.SpanKindPlugin, 60, 70),
		span(base, "c", "pp", "plugin.governance.posthook", schemas.SpanKindPlugin, 70, 78),
	}}

	got := bucketMap(t, trace, 40, true)

	if got["pipeline-post"] != 14000 {
		t.Errorf("pipeline-post = %v us, want 14000 (40ms wall - 26ms of nested plugins)", got["pipeline-post"])
	}
	if got["plugin.logging"] != 8000 || got["plugin.telemetry"] != 10000 || got["plugin.governance"] != 8000 {
		t.Errorf("plugin buckets = logging %v / telemetry %v / governance %v us, want 8000/10000/8000",
			got["plugin.logging"], got["plugin.telemetry"], got["plugin.governance"])
	}
	sum := got["pipeline-post"] + got["plugin.logging"] + got["plugin.telemetry"] + got["plugin.governance"]
	if sum != 40000 {
		t.Errorf("phase + plugins = %v us, want 40000 (== pipeline-post wall, no double-count)", sum)
	}
	if _, ok := got["scheduling"]; ok {
		t.Errorf("scheduling should be empty, got %v", got["scheduling"])
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
	if len(got) != 1 || got["scheduling"] != 3000 {
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
	if _, ok := got["scheduling"]; ok {
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
	if _, ok := got["scheduling"]; ok {
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
	if got["scheduling"] != 3000 {
		t.Errorf("core = %v us, want 3000", got["scheduling"])
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

	if got, _, _ := computeOverheadBreakdown(trace, 0, false, 0, false); len(got) != 0 {
		t.Errorf("expected no overhead buckets when overhead is negligible, got %v", got)
	}
}

func TestComputeOverheadBreakdown_Empty(t *testing.T) {
	if got, _, _ := computeOverheadBreakdown(nil, 0, false, 0, false); got != nil {
		t.Error("nil trace must yield nil")
	}
	if got, _, _ := computeOverheadBreakdown(&schemas.Trace{}, 5, true, 0, false); got != nil {
		t.Error("trace with no spans must yield nil")
	}
}

// Streaming: overhead is the measured Bifrost CPU (the buckets), never total-upstream.
// A stream stamps stream-phase attributes on the root span; the breakdown must fold those
// into buckets, emit NO "scheduling" residual (that leftover is off-CPU relay/scheduler
// wait between chunks, not Bifrost work), and return measuredMs = the bucket sum with
// isStreaming=true so Inject can use it as the overhead and fold the remainder into upstream.
func TestComputeOverheadBreakdown_StreamingNoSchedulingResidual(t *testing.T) {
	base := time.Now()
	root := span(base, "root", "", "/v1/responses", schemas.SpanKindHTTPRequest, 0, 100)
	root.Attributes = map[string]any{
		schemas.AttrBifrostStreamParseMs: 1.0, // 1ms SSE framing+decode -> response-parse bucket
	}
	trace := &schemas.Trace{
		RootSpan: root,
		Spans: []*schemas.Span{
			root,
			span(base, "keysel", "root", "key.selection", schemas.SpanKindInternal, 1, 3), // 2ms
		},
	}

	// total-upstream overhead is a huge 40ms (dominated by off-CPU relay wait), but the
	// measured Bifrost CPU is only key.selection (2ms) + stream-parse (1ms) = 3ms.
	buckets, measuredMs, isStreaming := computeOverheadBreakdown(trace, 40, true, 5, true)
	if !isStreaming {
		t.Fatal("expected isStreaming=true when a stream attr is present on the root span")
	}
	got := map[string]float64{}
	for _, b := range buckets {
		got[b.Name] = b.DurationUs
	}
	if _, ok := got["scheduling"]; ok {
		t.Errorf("streaming must not emit a scheduling residual bucket, got %v", got)
	}
	if got["key.selection"] != 2000 {
		t.Errorf("key.selection = %v us, want 2000", got["key.selection"])
	}
	if got["response-parse"] != 1000 {
		t.Errorf("response-parse (stream framing) = %v us, want 1000", got["response-parse"])
	}
	if measuredMs < 2.99 || measuredMs > 3.01 {
		t.Errorf("measuredMs = %v, want ~3 (2ms key.selection + 1ms stream-parse), not the 40ms total-upstream", measuredMs)
	}
}
