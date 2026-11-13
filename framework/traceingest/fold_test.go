package traceingest

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func permutations(parts []*SpanPart) [][]*SpanPart {
	if len(parts) <= 1 {
		return [][]*SpanPart{parts}
	}
	var out [][]*SpanPart
	for i := range parts {
		rest := make([]*SpanPart, 0, len(parts)-1)
		rest = append(rest, parts[:i]...)
		rest = append(rest, parts[i+1:]...)
		for _, perm := range permutations(rest) {
			out = append(out, append([]*SpanPart{parts[i]}, perm...))
		}
	}
	return out
}

// TestFoldIsOrderIndependent pins the fold rule the Braintrust wire needs: the SDK posts batches in
// parallel with no sequence number, so the same parts must fold to the same span in every arrival
// order, with the non-merge row as the base wherever it lands.
func TestFoldIsOrderIndependent(t *testing.T) {
	t0 := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	start, end := t0, t0.Add(3*time.Second)
	parts := []*SpanPart{
		{ID: "a", ReceivedAt: t0, TraceID: "t", SpanID: "s", Name: "chat", Kind: logstore.TraceSpanKindLLM, StartTime: &start,
			Input: map[string]any{"messages": []any{"hi"}}, Metadata: map[string]any{"model": "gpt-4o", "nested": map[string]any{"a": 1.0}}, Tags: []string{"prod"}},
		{ID: "b", ReceivedAt: t0.Add(time.Millisecond), IsMerge: true, TraceID: "t", SpanID: "s",
			Output: "hello", Metadata: map[string]any{"nested": map[string]any{"b": 2.0}}, PromptTokens: 12, CompletionTokens: 3, Tags: []string{"canary"}},
		{ID: "c", ReceivedAt: t0.Add(2 * time.Millisecond), IsMerge: true, TraceID: "t", SpanID: "s", EndTime: &end, Scores: map[string]float64{"accuracy": 1}},
	}
	want, err := schemas.MarshalSorted(Fold(nil, parts))
	require.NoError(t, err)
	for _, perm := range permutations(parts) {
		got, err := schemas.MarshalSorted(Fold(nil, perm))
		require.NoError(t, err)
		assert.JSONEq(t, string(want), string(got))
	}

	folded := Fold(nil, parts)
	assert.False(t, folded.IsMerge, "the base row arrived, so the span is whole")
	assert.Equal(t, map[string]any{"model": "gpt-4o", "nested": map[string]any{"a": 1.0, "b": 2.0}}, folded.Metadata, "maps merge key by key")
	assert.Equal(t, []string{"canary", "prod"}, folded.Tags, "tags are unioned")
	assert.Equal(t, "hello", folded.Output)
	assert.Equal(t, "chat", folded.Name, "a merge row that does not name the span keeps the base name")

	span := folded.Span("p1", logstore.TraceSourceBraintrust, nil)
	assert.Equal(t, 15, span.TotalTokens, "total is derived when the SDK sends only the two halves")
	assert.Equal(t, `{"messages":["hi"]}`, span.Input)
	assert.Equal(t, "hello", span.Output, "plain text is stored as text")
	assert.Equal(t, logstore.TraceSpanStatusOK, span.Status)
}

// TestFoldKeepsASpanWhoseBaseHasNotArrived pins the partial rule: merge rows ahead of their base
// still produce a span, marked partial, and the base completes it later.
func TestFoldKeepsASpanWhoseBaseHasNotArrived(t *testing.T) {
	t0 := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	merge := &SpanPart{ID: "m", ReceivedAt: t0, IsMerge: true, TraceID: "t", SpanID: "s", Output: "late", Error: "boom"}
	partial := Fold(nil, []*SpanPart{merge})
	assert.True(t, partial.IsMerge)
	span := partial.Span("p1", logstore.TraceSourceBraintrust, nil)
	assert.True(t, span.Partial)
	assert.Equal(t, logstore.TraceSpanKindUnknown, span.Kind)
	assert.Equal(t, logstore.TraceSpanStatusError, span.Status)
	assert.Equal(t, t0, span.StartTime, "with no start time yet, the span is placed at arrival")

	start := t0.Add(-time.Second)
	base := &SpanPart{ID: "b", ReceivedAt: t0.Add(time.Second), TraceID: "t", SpanID: "s", Name: "tool", Kind: logstore.TraceSpanKindTool, StartTime: &start, Output: "early"}
	whole := Fold(span, []*SpanPart{merge, base})
	assert.False(t, whole.IsMerge)
	assert.Equal(t, "late", whole.Output, "the merge row still wins over the base that arrived after it")
	assert.Equal(t, start, whole.Span("p1", logstore.TraceSourceBraintrust, span).StartTime)
}

// TestFoldAppliesALateMergeOverTheStoredSpan pins the incremental rule: once a span's early parts
// have expired, a merge row is laid over the stored span instead of being folded on its own, and
// what the fold does not own (the gateway link, the estimate) survives.
func TestFoldAppliesALateMergeOverTheStoredSpan(t *testing.T) {
	start := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	stored := &logstore.TraceSpan{
		ProjectID: "p1", TraceID: "t", SpanID: "s", Name: "chat", Kind: logstore.TraceSpanKindLLM, StartTime: start,
		Input: `{"q":"hi"}`, MetadataParsed: map[string]any{"a": 1.0}, TagsParsed: []string{"prod"}, PromptTokens: 10,
		Cost: new(0.004), CostEstimated: true, LinkMethod: "header", LinkSnapshotParsed: &logstore.TraceLinkSnapshot{TotalTokens: 99},
		CreatedAt: start,
	}
	end := start.Add(time.Second)
	late := &SpanPart{ID: "m", ReceivedAt: start.Add(time.Hour), IsMerge: true, TraceID: "t", SpanID: "s", EndTime: &end, Metadata: map[string]any{"b": 2.0}, Output: map[string]any{"text": "done"}}

	folded := Fold(stored, []*SpanPart{late})
	span := folded.Span("p1", logstore.TraceSourceBraintrust, stored)
	assert.False(t, span.Partial)
	assert.Equal(t, "chat", span.Name)
	assert.Equal(t, `{"q":"hi"}`, span.Input)
	assert.Equal(t, `{"text":"done"}`, span.Output)
	assert.Equal(t, map[string]any{"a": 1.0, "b": 2.0}, span.MetadataParsed)
	assert.Equal(t, 10, span.PromptTokens)
	assert.Equal(t, start, span.StartTime)
	assert.Equal(t, &end, span.EndTime)
	require.NotNil(t, span.Cost)
	assert.True(t, span.CostEstimated, "Bifrost's own estimate is carried over, not treated as an SDK cost")
	assert.Equal(t, "header", span.LinkMethod)
	assert.Equal(t, 99, span.LinkSnapshotParsed.TotalTokens)
	assert.Equal(t, start, span.CreatedAt)
}

// BenchmarkFold measures the fold for the two shapes ingest sees: an OTLP span that arrives whole
// (one part), and a Braintrust span sent as a base row and two merge rows.
func BenchmarkFold(b *testing.B) {
	base := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	whole := &SpanPart{
		TraceID: "t1", SpanID: "s1", Name: "chat", Kind: logstore.TraceSpanKindLLM, StartTime: &base, EndTime: new(base.Add(time.Second)),
		Input:    []any{map[string]any{"role": "user", "content": "hi"}},
		Output:   "hello",
		Metadata: map[string]any{"tenant": "acme"}, Tags: []string{"a", "b"},
		Attributes: map[string]any{"llm.provider": "openai", "otel.scope.name": "bench", "resource": map[string]any{"service.name": "bench"}},
		Model:      "gpt-4o", PromptTokens: 18, CompletionTokens: 4, ReceivedAt: base, ID: "1",
	}
	merged := []*SpanPart{
		{TraceID: "t1", SpanID: "s1", Name: "task", StartTime: &base, Input: map[string]any{"q": "hi"}, Metadata: map[string]any{"phase": "start"}, Tags: []string{"first"}, ReceivedAt: base, ID: "1"},
		{TraceID: "t1", SpanID: "s1", IsMerge: true, Output: map[string]any{"a": "hello"}, Metadata: map[string]any{"phase": "end"}, Tags: []string{"second"}, ReceivedAt: base.Add(time.Millisecond), ID: "2"},
		{TraceID: "t1", SpanID: "s1", IsMerge: true, EndTime: new(base.Add(time.Second)), Scores: map[string]float64{"ok": 1}, ReceivedAt: base.Add(2 * time.Millisecond), ID: "3"},
	}
	b.Run("one_part", func(b *testing.B) {
		parts := []*SpanPart{whole}
		b.ReportAllocs()
		for b.Loop() {
			Fold(nil, parts).Span("p1", logstore.TraceSourceOTel, nil)
		}
	})
	b.Run("base_and_two_merges", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			Fold(nil, merged).Span("p1", logstore.TraceSourceBraintrust, nil)
		}
	})
}

// TestFoldOfOnePartCopiesNothingItNeedNot pins the common case, an OTLP span that arrives whole: the
// fold returns a new part with sorted, deduplicated tags and leaves the received part as it was.
func TestFoldOfOnePartCopiesNothingItNeedNot(t *testing.T) {
	start := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	received := &SpanPart{TraceID: "t", SpanID: "s", Name: "chat", StartTime: &start, Tags: []string{"b", "a", "b"},
		Metadata: map[string]any{"k": "v"}, Input: map[string]any{"q": "hi"}, ReceivedAt: start}
	folded := Fold(nil, []*SpanPart{received})
	require.NotSame(t, received, folded)
	assert.False(t, folded.IsMerge)
	assert.Equal(t, []string{"a", "b"}, folded.Tags)
	assert.Equal(t, []string{"b", "a", "b"}, received.Tags, "the received part is not reordered")
	assert.Equal(t, received.Metadata, folded.Metadata)
	assert.Equal(t, received.Input, folded.Input)
}

// TestRowIDIsStable pins the id of a score or comment row. Scores already stored were written under
// it, and a score sent again must replace its first copy.
func TestRowIDIsStable(t *testing.T) {
	assert.Equal(t, "e63e5a069685a121151b76fc941a42883b05f864", rowID("score", "p1", "t1", "s1", "accuracy"))
}

// TestHotPathAllocationBudgets fails when a change makes the per-span ingest path allocate more.
// Paths with no JSON codec in them are pinned to what they need today. Paths that decode or encode
// JSON get about a quarter of headroom, because sonic allocates differently on amd64 and arm64.
// Raise a ceiling only with a reason.
func TestHotPathAllocationBudgets(t *testing.T) {
	spans := benchOTelSpans()
	base := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	whole := []*SpanPart{{TraceID: "t1", SpanID: "s1", Name: "chat", Kind: logstore.TraceSpanKindLLM, StartTime: &base, EndTime: new(base.Add(time.Second)),
		Input: []any{map[string]any{"role": "user", "content": "hi"}}, Output: "hello", Metadata: map[string]any{"tenant": "acme"}, Tags: []string{"a", "b"},
		Attributes: map[string]any{"llm.provider": "openai"}, Model: "gpt-4o", PromptTokens: 18, CompletionTokens: 4, ReceivedAt: base, ID: "1"}}
	budgets := []struct {
		name  string
		limit float64
		run   func()
	}{
		{"map a plain OTLP span", 12, func() { MapOTelSpan(spans["plain"]) }},
		{"map an OpenInference LLM span", 55, func() { MapOTelSpan(spans["openinference"]) }},
		{"map a GenAI LLM span", 54, func() { MapOTelSpan(spans["genai"]) }},
		{"map a Langfuse generation", 68, func() { MapOTelSpan(spans["langfuse"]) }},
		{"fold one whole span", 2, func() { Fold(nil, whole) }},
		{"fold and build the stored span", 12, func() { Fold(nil, whole).Span("p1", logstore.TraceSourceOTel, nil) }},
		{"derive a score row id", 2, func() { rowID("score", "p1", "t1", "s1", "accuracy") }},
	}
	for _, budget := range budgets {
		got := testing.AllocsPerRun(200, budget.run)
		assert.LessOrEqual(t, got, budget.limit, "%s allocates %.0f times, budget %.0f", budget.name, got, budget.limit)
	}
}
