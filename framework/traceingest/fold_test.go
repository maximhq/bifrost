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
		CreatedAt: start, TracingKeyID: "k1",
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
	assert.Equal(t, "k1", span.TracingKeyID)
}
