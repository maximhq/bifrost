package traceingest

import (
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func btRow(t testing.TB, raw string) map[string]any {
	t.Helper()
	var row map[string]any
	require.NoError(t, sonic.Unmarshal([]byte(raw), &row))
	return row
}

// TestMapBraintrustRow pins the mapping of Braintrust logs3 rows: a span row, the merge row that
// ends it, feedback addressed by row id alone, and a comment.
func TestMapBraintrustRow(t *testing.T) {
	start, err := MapBraintrustRow(btRow(t, `{
		"id":"row-1","span_id":"span-1","root_span_id":"root-1","span_parents":["root-1"],"project_id":"p1","log_id":"g",
		"span_attributes":{"name":"Chat Completion","type":"llm","exec_counter":3},
		"input":[{"role":"user","content":"hi"}],
		"metadata":{"model":"gpt-4o","provider":"openai","temperature":0.2,"session_id":"sess-1"},
		"metrics":{"start":1700000000.25},"tags":["prod"],"created":"2023-11-14T22:13:20.250Z"}`))
	require.NoError(t, err)
	part := start.Part
	assert.Equal(t, "p1", start.ProjectID)
	assert.Equal(t, "root-1", part.TraceID, "root_span_id is the trace")
	assert.Equal(t, "row-1", part.RowID)
	assert.Equal(t, []string{"root-1"}, part.ParentSpanIDs)
	assert.Equal(t, logstore.TraceSpanKindLLM, part.Kind)
	assert.Equal(t, "Chat Completion", part.Name)
	assert.Equal(t, "gpt-4o", part.Model)
	assert.Equal(t, "sess-1", part.SessionID)
	assert.Equal(t, time.Unix(1700000000, 250000000).UTC(), *part.StartTime, "metrics times are epoch seconds")
	assert.Equal(t, map[string]any{"exec_counter": 3.0}, part.Attributes["span_attributes"], "span attributes with no field of their own are kept")
	assert.False(t, part.IsMerge)

	end, err := MapBraintrustRow(btRow(t, `{
		"id":"row-1","span_id":"span-1","root_span_id":"root-1","project_id":"p1","_is_merge":true,
		"output":[{"message":{"role":"assistant","content":"hello"}}],
		"metrics":{"end":1700000001.75,"prompt_tokens":12,"completion_tokens":5,"tokens":17,"prompt_cached_tokens":4,"time_to_first_token":0.31},
		"scores":{"accuracy":1,"skipped":null}}`))
	require.NoError(t, err)
	assert.True(t, end.Part.IsMerge)
	assert.Empty(t, end.Part.Kind, "a merge row with no type does not reset the span's kind")
	assert.Equal(t, 17, end.Part.TotalTokens)
	assert.Equal(t, 4, end.Part.CachedReadTokens)
	assert.InDelta(t, 310, *end.Part.TimeToFirstToken, 1e-9)
	assert.Equal(t, map[string]float64{"accuracy": 1}, end.Part.Scores, "a null score is not a score")

	span := Fold(nil, []*SpanPart{end.Part, part}).Span("p1", logstore.TraceSourceBraintrust, nil)
	assert.InDelta(t, 1500, *span.DurationMs, 1e-6)
	assert.Equal(t, "root-1", span.ParentSpanID)

	plain, err := MapBraintrustRow(btRow(t, `{"id":"r","span_id":"s","root_span_id":"s","project_id":"p1","span_attributes":{"name":"answer"},"error":{"message":"boom"},"metrics":{"start":1700000000}}`))
	require.NoError(t, err)
	assert.Equal(t, logstore.TraceSpanKindTask, plain.Part.Kind, "a traced() span with no type is a unit of work")
	assert.Equal(t, logstore.TraceSpanStatusError, plain.Part.Status)
	assert.Equal(t, `{"message":"boom"}`, plain.Part.Error)

	feedback, err := MapBraintrustRow(btRow(t, `{"id":"row-1","project_id":"p1","_is_merge":true,"scores":{"correctness":0.5},"expected":"hello","tags":["reviewed"]}`))
	require.NoError(t, err)
	assert.Equal(t, "row-1", feedback.TargetRowID, "feedback names its span by row id only")
	assert.Empty(t, feedback.Part.SpanID)
	assert.Equal(t, map[string]float64{"correctness": 0.5}, feedback.Part.Scores)

	comment, err := MapBraintrustRow(btRow(t, `{"id":"c-1","project_id":"p1","origin":{"id":"row-1"},"comment":{"text":"looks wrong"}}`))
	require.NoError(t, err)
	assert.Equal(t, "row-1", comment.TargetRowID)
	assert.Equal(t, "looks wrong", comment.Comment)
	assert.Nil(t, comment.Part)

	_, err = MapBraintrustRow(btRow(t, `{"project_id":"p1","scores":{"a":1}}`))
	assert.Error(t, err, "a row that names no span and no row cannot be stored")
}

// TestBraintrustObjectIDCarriesItsProject pins the ids issued for experiments and datasets.
func TestBraintrustObjectIDCarriesItsProject(t *testing.T) {
	id := BraintrustObjectID("exp", "3f0c1c7e-1111-2222-3333-444455556666", "baseline")
	assert.Equal(t, id, BraintrustObjectID("exp", "3f0c1c7e-1111-2222-3333-444455556666", "baseline"), "the same name gets the same id")
	assert.NotEqual(t, id, BraintrustObjectID("exp", "3f0c1c7e-1111-2222-3333-444455556666", "candidate"))
	project, ok := BraintrustObjectProject("exp", id)
	require.True(t, ok)
	assert.Equal(t, "3f0c1c7e-1111-2222-3333-444455556666", project)
	_, ok = BraintrustObjectProject("exp", "3f0c1c7e-1111-2222-3333-444455556666")
	assert.False(t, ok)
	_, ok = BraintrustObjectProject("dataset", id)
	assert.False(t, ok)
}

// BenchmarkMapBraintrustRow measures mapping one logs3 row: an LLM span row with messages and usage.
func BenchmarkMapBraintrustRow(b *testing.B) {
	row := btRow(b, `{
		"id":"row-1","span_id":"s1","root_span_id":"t1","span_parents":["s0"],"project_id":"p1",
		"span_attributes":{"name":"Chat Completion","type":"llm"},
		"input":[{"role":"system","content":"You are a support agent."},{"role":"user","content":"My card was charged twice"}],
		"output":[{"message":{"role":"assistant","content":"I have refunded the duplicate charge."}}],
		"metadata":{"model":"gpt-4o-mini","provider":"openai","temperature":0.2,"session_id":"s-1","user_id":"u-1"},
		"metrics":{"start":1794477600.0,"end":1794477601.5,"prompt_tokens":42,"completion_tokens":11,"tokens":53,"prompt_cached_tokens":16},
		"scores":{"correct":1.0},"tags":["support"],"created":"2026-11-12T10:00:00Z"
	}`)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := MapBraintrustRow(row); err != nil {
			b.Fatal(err)
		}
	}
}
