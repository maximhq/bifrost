package traceingest

import (
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
	assert.Equal(t, time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC), *trace.StartTime)

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
