package traceingest

import (
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMapLangfuseEvent pins the mapping of Langfuse's legacy batch events: the trace becomes the
// root span, an observation with no parent hangs under it, an update is a merge row, and a score
// is returned on its own.
func TestMapLangfuseEvent(t *testing.T) {
	trace, _, err := MapLangfuseEvent(&LangfuseEvent{Type: "trace-create", Timestamp: "2026-11-12T10:00:00.000Z", Body: map[string]any{
		"id": "tr-1", "name": "chat-app", "userId": "u1", "sessionId": "s1", "tags": []any{"prod"}, "input": map[string]any{"q": "hi"}, "metadata": map[string]any{"k": "v"}, "release": "1.2.3",
	}})
	require.NoError(t, err)
	assert.Equal(t, "tr-1", trace.SpanID, "the trace is the root span, with the trace id as its span id")
	assert.Empty(t, trace.ParentSpanIDs)
	assert.Equal(t, "s1", trace.SessionID)
	assert.Equal(t, []string{"prod"}, trace.Tags)
	assert.Equal(t, time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC), *trace.DefaultStartTime, "the event time is the fallback start of a trace with no timestamp")

	generation, _, err := MapLangfuseEvent(&LangfuseEvent{Type: "generation-create", Timestamp: "2026-11-12T10:00:01Z", Body: map[string]any{
		"id": "gen-1", "traceId": "tr-1", "name": "llm", "startTime": "2026-11-12T10:00:00.100Z", "completionStartTime": "2026-11-12T10:00:00.400Z",
		"model": "gpt-4o", "input": `[{"role":"user","content":"hi"}]`, "modelParameters": map[string]any{"temperature": 0.2},
	}})
	require.NoError(t, err)
	assert.Equal(t, logstore.TraceSpanKindLLM, generation.Kind)
	assert.Equal(t, []string{"tr-1"}, generation.ParentSpanIDs, "no parent observation: it hangs under the trace root")
	assert.False(t, generation.IsMerge)
	assert.IsType(t, []any{}, generation.Input, "content serialised by the SDK is decoded")
	assert.InDelta(t, 300, *generation.TimeToFirstToken, 1e-6)

	update, _, err := MapLangfuseEvent(&LangfuseEvent{Type: "generation-update", Timestamp: "2026-11-12T10:00:02Z", Body: map[string]any{
		"id": "gen-1", "traceId": "tr-1", "endTime": "2026-11-12T10:00:01.500Z", "output": "hello",
		"usage": map[string]any{"promptTokens": 9.0, "completionTokens": 3.0, "totalTokens": 12.0}, "costDetails": map[string]any{"total": 0.0004},
		"level": "ERROR", "statusMessage": "cut off",
	}})
	require.NoError(t, err)
	assert.True(t, update.IsMerge)
	assert.Empty(t, update.ParentSpanIDs, "an update does not move the span")
	assert.Nil(t, update.StartTime, "an update that sends no start time does not reset it")
	assert.Equal(t, 12, update.TotalTokens)
	assert.InDelta(t, 0.0004, *update.Cost, 1e-12)
	assert.Equal(t, logstore.TraceSpanStatusError, update.Status)

	folded := Fold(nil, []*SpanPart{update, generation}).Span("p1", logstore.TraceSourceLangfuse, nil)
	assert.Equal(t, "hello", folded.Output)
	assert.Equal(t, "tr-1", folded.ParentSpanID)
	assert.InDelta(t, 1400, *folded.DurationMs, 1e-6)

	child, _, err := MapLangfuseEvent(&LangfuseEvent{Type: "observation-create", Body: map[string]any{"id": "o-2", "traceId": "tr-1", "type": "SPAN", "parentObservationId": "gen-1", "startTime": "2026-11-12T10:00:00Z"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"gen-1"}, child.ParentSpanIDs)
	assert.Equal(t, logstore.TraceSpanKindTask, child.Kind)

	_, score, err := MapLangfuseEvent(&LangfuseEvent{ID: "evt-9", Type: "score-create", Body: map[string]any{"traceId": "tr-1", "observationId": "gen-1", "name": "helpful", "value": true, "comment": "yes"}})
	require.NoError(t, err)
	stored := score.TraceScore(logstore.TraceAuthorSDK)
	assert.Equal(t, "evt-9", stored.ID, "a score with no id of its own takes the event's, so a resend replaces it")
	assert.Equal(t, 1.0, *stored.Value)
	assert.Equal(t, "gen-1", stored.SpanID)
	_, label, err := MapLangfuseEvent(&LangfuseEvent{ID: "evt-10", Type: "score-create", Body: map[string]any{"traceId": "tr-1", "name": "tone", "value": "formal"}})
	require.NoError(t, err)
	assert.Equal(t, "categorical", label.TraceScore(logstore.TraceAuthorSDK).DataType)

	for name, event := range map[string]*LangfuseEvent{
		"trace without id":       {Type: "trace-create", Body: map[string]any{"name": "x"}},
		"span without trace":     {Type: "span-create", Body: map[string]any{"id": "s"}},
		"score without a value":  {Type: "score-create", Body: map[string]any{"traceId": "t", "name": "n"}},
		"score without the name": {Type: "score-create", Body: map[string]any{"traceId": "t", "value": 1.0}},
	} {
		_, _, err := MapLangfuseEvent(event)
		assert.Error(t, err, name)
	}
	part, score, err := MapLangfuseEvent(&LangfuseEvent{Type: "sdk-log", Body: map[string]any{"log": "x"}})
	assert.NoError(t, err)
	assert.Nil(t, part)
	assert.Nil(t, score)
}

// foldEvents maps Langfuse events and folds them, as ingest does for one span.
func foldEvents(t *testing.T, stored *logstore.TraceSpan, events ...*LangfuseEvent) *SpanPart {
	t.Helper()
	parts := make([]*SpanPart, 0, len(events))
	for i, event := range events {
		part, _, err := MapLangfuseEvent(event)
		require.NoError(t, err)
		part.ReceivedAt = time.Date(2026, 11, 12, 10, 0, i, 0, time.UTC)
		part.ID = fmt.Sprintf("p%d", i)
		parts = append(parts, part)
	}
	return Fold(stored, parts)
}

// A Langfuse trace-create is an upsert: an update that arrives after the first event's part
// expired lays its fields over the stored root instead of rebuilding it from the update alone,
// and an update with no timestamp keeps the trace's start.
func TestLangfuseTraceUpdatesAreUpserts(t *testing.T) {
	start := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	stored := &logstore.TraceSpan{ProjectID: "p1", TraceID: "tr", SpanID: "tr", Name: "agent", Kind: logstore.TraceSpanKindTask, SessionID: "s1", Input: `"question"`, StartTime: start}
	update := &LangfuseEvent{Type: "trace-create", Timestamp: "2026-11-12T10:30:00Z", Body: map[string]any{"id": "tr", "output": "answer"}}
	folded := foldEvents(t, stored, update)
	assert.Equal(t, "agent", folded.Name, "the stored name survives an output-only update")
	assert.Equal(t, "s1", folded.SessionID)
	assert.Equal(t, decodeValue(stored.Input), folded.Input, "the stored input survives")
	assert.Equal(t, "answer", folded.Output)
	require.NotNil(t, folded.StartTime)
	assert.True(t, folded.StartTime.Equal(start), "an update with no timestamp keeps the start: %s", folded.StartTime)

	create := &LangfuseEvent{Type: "trace-create", Timestamp: "2026-11-12T09:00:00Z", Body: map[string]any{"id": "tr", "name": "agent", "timestamp": "2026-11-12T09:00:00Z"}}
	both := foldEvents(t, nil, create, update)
	require.NotNil(t, both.StartTime)
	assert.True(t, both.StartTime.Equal(start), "the create's timestamp wins over the update's event time")
	assert.False(t, both.IsMerge, "a trace upsert is a complete row, not a partial one")
}

// Streaming integrations send completionStartTime in a later update than the start. Time to first
// token is computed after the parts fold, so it is not lost.
func TestLangfuseTimeToFirstTokenAcrossUpdates(t *testing.T) {
	create := &LangfuseEvent{Type: "generation-create", Body: map[string]any{"id": "g1", "traceId": "tr", "startTime": "2026-11-12T10:00:00Z"}}
	update := &LangfuseEvent{Type: "generation-update", Body: map[string]any{"id": "g1", "traceId": "tr", "completionStartTime": "2026-11-12T10:00:00.300Z"}}
	folded := foldEvents(t, nil, create, update)
	require.NotNil(t, folded.TimeToFirstToken)
	assert.InDelta(t, 300, *folded.TimeToFirstToken, 1e-6)
}
