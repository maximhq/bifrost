package logstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTraceTestStore(t *testing.T) *RDBLogStore {
	t.Helper()
	store, err := newSqliteLogStore(context.Background(), &SQLiteConfig{Path: filepath.Join(t.TempDir(), "traces.db")}, testLogger{})
	require.NoError(t, err)
	return store
}

// TestTraceTablesMigrationIsIdempotent pins add_trace_tables: every trace table exists after boot,
// and a rerun over the existing tables and indexes succeeds.
func TestTraceTablesMigrationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	db := store.db
	for _, model := range []any{&TraceSpanPart{}, &TraceSpan{}, &TraceSpanLink{}, &Trace{}, &TraceScore{}, &TraceComment{}, &TraceUpload{}} {
		assert.True(t, db.Migrator().HasTable(model), "%T has a table", model)
	}
	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "add_trace_tables").Error)
	require.NoError(t, migrationAddTraceTables(ctx, db, testLogger{}), "a rerun over existing tables and indexes succeeds")
}

// TestTraceSpanPartsAreStoredOncePerContent pins the retry rule: an SDK that resends a batch stores
// no second copy of a part, while a different payload for the same span is kept as its own part.
func TestTraceSpanPartsAreStoredOncePerContent(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)

	base := &TraceSpanPart{ProjectID: "p1", TraceID: "t1", SpanID: "s1", RowID: "row-1", Source: TraceSourceBraintrust, ReceivedAt: now, Payload: `{"input":"hi"}`}
	merge := &TraceSpanPart{ProjectID: "p1", TraceID: "t1", SpanID: "s1", RowID: "row-1", Source: TraceSourceBraintrust, ReceivedAt: now.Add(time.Second), IsMerge: true, Payload: `{"output":"hello"}`}
	inserted, err := store.BatchCreateTraceSpanParts(ctx, []*TraceSpanPart{base, merge})
	require.NoError(t, err)
	assert.Equal(t, 2, inserted)

	retry := &TraceSpanPart{ProjectID: "p1", TraceID: "t1", SpanID: "s1", RowID: "row-1", Source: TraceSourceBraintrust, ReceivedAt: now.Add(time.Minute), Payload: `{"input":"hi"}`}
	inserted, err = store.BatchCreateTraceSpanParts(ctx, []*TraceSpanPart{retry})
	require.NoError(t, err)
	assert.Zero(t, inserted, "a resent part with the same content is not stored twice")

	parts, err := store.ListTraceSpanParts(ctx, "p1", "t1", "s1")
	require.NoError(t, err)
	require.Len(t, parts, 2)
	assert.False(t, parts[0].IsMerge, "parts come back in receive order")
	assert.True(t, parts[1].IsMerge)
	assert.Equal(t, now, parts[0].ReceivedAt.UTC(), "the first receive time is kept, not the retry's")

	other, err := store.ListTraceSpanParts(ctx, "p2", "t1", "s1")
	require.NoError(t, err)
	assert.Empty(t, other, "parts are scoped to their project")
}

// TestTraceSpanIdentityIsProjectTraceSpan pins the span key. OTLP span ids are unique only inside
// one trace, so the same span id in two traces, or in two projects, must stay two rows.
func TestTraceSpanIdentityIsProjectTraceSpan(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	start := time.Now().UTC().Truncate(time.Millisecond)

	spans := []*TraceSpan{
		{ProjectID: "p1", TraceID: "t1", SpanID: "s1", Name: "root", Kind: TraceSpanKindTask, Source: TraceSourceOTel, StartTime: start},
		{ProjectID: "p1", TraceID: "t2", SpanID: "s1", Name: "other trace", Kind: TraceSpanKindTask, Source: TraceSourceOTel, StartTime: start},
		{ProjectID: "p2", TraceID: "t1", SpanID: "s1", Name: "other project", Kind: TraceSpanKindTask, Source: TraceSourceOTel, StartTime: start},
	}
	require.NoError(t, store.UpsertTraceSpans(ctx, spans))

	got, err := store.ListTraceSpans(ctx, "p1", "t1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "root", got[0].Name)
}

// TestUpsertTraceSpanReplacesTheFoldedRow pins the refold write: a later fold of the same span
// replaces the stored row, and parsed fields round-trip through their stored columns.
func TestUpsertTraceSpanReplacesTheFoldedRow(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	start := time.Now().UTC().Truncate(time.Millisecond)
	end := start.Add(250 * time.Millisecond)

	require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{
		{ProjectID: "p1", TraceID: "t1", SpanID: "root", Name: "agent", Kind: TraceSpanKindTask, Source: TraceSourceBraintrust, StartTime: start},
		{ProjectID: "p1", TraceID: "t1", SpanID: "llm", ParentSpanIDsParsed: []string{"root"}, Name: "chat", Kind: TraceSpanKindLLM, Source: TraceSourceBraintrust, StartTime: start.Add(10 * time.Millisecond), Partial: true},
	}))
	require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{
		{ProjectID: "p1", TraceID: "t1", SpanID: "llm", ParentSpanIDsParsed: []string{"root", "extra"}, Name: "chat", Kind: TraceSpanKindLLM, Source: TraceSourceBraintrust,
			StartTime: start.Add(10 * time.Millisecond), EndTime: &end, Model: "gpt-4o", Provider: "openai", PromptTokens: 320, CompletionTokens: 72, TotalTokens: 392, CachedReadTokens: 64,
			TagsParsed: []string{"demo", "full-trace"}, MetadataParsed: map[string]any{"finish_reason": "tool_calls"}, Input: `[{"role":"user","content":"hi"}]`, Output: `{"content":"hello"}`},
	}))

	got, err := store.ListTraceSpans(ctx, "p1", "t1")
	require.NoError(t, err)
	require.Len(t, got, 2, "the refold replaced the span, it did not add a row")
	assert.Equal(t, "root", got[0].SpanID, "spans come back in start order")
	llm := got[1]
	assert.False(t, llm.Partial)
	require.NotNil(t, llm.EndTime)
	assert.Equal(t, end, llm.EndTime.UTC())
	assert.Equal(t, 392, llm.TotalTokens)
	assert.Equal(t, 64, llm.CachedReadTokens)
	assert.Equal(t, []string{"root", "extra"}, llm.ParentSpanIDsParsed, "every parent a Braintrust span names is kept")
	assert.Equal(t, "root", llm.ParentSpanID, "the tree parent is the first one")
	assert.Equal(t, []string{"demo", "full-trace"}, llm.TagsParsed)
	assert.Equal(t, "tool_calls", llm.MetadataParsed["finish_reason"])
	assert.Equal(t, 240.0, *llm.DurationMs, "duration is derived from start and end")
}

// TestRecomputeTraceRollupCountsEachCallOnce pins the no-double-count rule. A span linked to a
// gateway row is counted with the gateway's snapshot, never with the numbers the SDK reported for
// the same call, and an unlinked span is counted with its own.
func TestRecomputeTraceRollupCountsEachCallOnce(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	start := time.Now().UTC().Truncate(time.Millisecond)
	end := start.Add(800 * time.Millisecond)
	llmEnd := start.Add(300 * time.Millisecond)

	require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{
		{ProjectID: "p1", TraceID: "t1", SpanID: "root", Name: "agent", Kind: TraceSpanKindTask, Source: TraceSourceBraintrust, StartTime: start, EndTime: &end,
			Input: `"question"`, Output: `"answer"`, TagsParsed: []string{"demo"}, SessionID: "sess-1", UserID: "user-1"},
		// Went through the gateway: the SDK says 100 tokens, the gateway says 120 and priced it.
		{ProjectID: "p1", TraceID: "t1", SpanID: "linked", ParentSpanIDsParsed: []string{"root"}, Name: "chat", Kind: TraceSpanKindLLM, Source: TraceSourceBraintrust,
			StartTime: start.Add(10 * time.Millisecond), EndTime: &llmEnd, PromptTokens: 80, CompletionTokens: 20, TotalTokens: 100,
			LinkMethod: "header", LinkSnapshotParsed: &TraceLinkSnapshot{PromptTokens: 90, CompletionTokens: 30, TotalTokens: 120, CachedReadTokens: 10, Cost: new(0.004)}},
		// Called the provider directly, SDK sent no cost, Bifrost estimated one.
		{ProjectID: "p1", TraceID: "t1", SpanID: "direct", ParentSpanIDsParsed: []string{"root"}, Name: "chat", Kind: TraceSpanKindLLM, Source: TraceSourceBraintrust,
			StartTime: start.Add(400 * time.Millisecond), EndTime: &end, PromptTokens: 40, CompletionTokens: 10, TotalTokens: 50, Cost: new(0.001), CostEstimated: true},
		// Has tokens but no price could be found.
		{ProjectID: "p1", TraceID: "t1", SpanID: "unpriced", ParentSpanIDsParsed: []string{"root"}, Name: "chat", Kind: TraceSpanKindLLM, Source: TraceSourceBraintrust,
			StartTime: start.Add(500 * time.Millisecond), EndTime: &end, PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10, Status: TraceSpanStatusError},
	}))

	trace, err := store.RecomputeTraceRollup(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Equal(t, "root", trace.RootSpanID)
	assert.Equal(t, "agent", trace.Name)
	assert.False(t, trace.InProgress)
	assert.Equal(t, `"question"`, trace.Input)
	assert.Equal(t, `"answer"`, trace.Output)
	assert.Equal(t, "sess-1", trace.SessionID)
	assert.Equal(t, []string{"demo"}, trace.TagsParsed)
	assert.Equal(t, 800.0, *trace.DurationMs)
	assert.Equal(t, 4, trace.SpanCount)
	assert.Equal(t, 3, trace.LLMSpanCount)
	assert.Equal(t, 1, trace.LinkedSpanCount)
	assert.Equal(t, 1, trace.ErrorCount)
	assert.Equal(t, 180, trace.TotalTokens, "120 from the gateway, 50 and 10 from the unlinked spans; the linked span's own 100 is not added")
	assert.Equal(t, 135, trace.PromptTokens)
	assert.Equal(t, 45, trace.CompletionTokens)
	assert.Equal(t, 10, trace.CachedReadTokens)
	assert.InDelta(t, 0.005, *trace.Cost, 1e-9)
	assert.InDelta(t, 0.001, *trace.EstimatedCost, 1e-9, "only the Bifrost-priced share is marked estimated")
	assert.Equal(t, 1, trace.UnpricedSpanCount)

	stored, err := store.FindTrace(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Equal(t, 180, stored.TotalTokens, "the rollup is persisted")
	_, err = store.FindTrace(ctx, "p1", "missing")
	require.ErrorIs(t, err, ErrNotFound)
}

// TestRecomputeTraceRollupListsTraceBeforeItsRoot pins the rootless case. OTel exports only
// finished spans, so children land before the root: the trace is listed at once, marked in
// progress, and completes when the root arrives.
func TestRecomputeTraceRollupListsTraceBeforeItsRoot(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	start := time.Now().UTC().Truncate(time.Millisecond)
	childEnd := start.Add(100 * time.Millisecond)

	require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{
		{ProjectID: "p1", TraceID: "t1", SpanID: "child", ParentSpanIDsParsed: []string{"root"}, Name: "retrieve", Kind: TraceSpanKindRetriever, Source: TraceSourceOTel, StartTime: start.Add(5 * time.Millisecond), EndTime: &childEnd},
	}))
	trace, err := store.RecomputeTraceRollup(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.True(t, trace.InProgress, "no span without a missing parent has arrived yet")
	assert.Empty(t, trace.RootSpanID)
	assert.Equal(t, "retrieve", trace.Name, "the earliest span names the trace until the root lands")
	assert.Equal(t, 1, trace.SpanCount)
	created := trace.CreatedAt

	rootEnd := start.Add(time.Second)
	require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{
		{ProjectID: "p1", TraceID: "t1", SpanID: "root", Name: "agent", Kind: TraceSpanKindAgent, Source: TraceSourceOTel, StartTime: start, EndTime: &rootEnd},
	}))
	trace, err = store.RecomputeTraceRollup(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.False(t, trace.InProgress)
	assert.Equal(t, "root", trace.RootSpanID)
	assert.Equal(t, "agent", trace.Name)
	assert.Equal(t, 2, trace.SpanCount)
	assert.Equal(t, created.UTC(), trace.CreatedAt.UTC(), "the first time the trace was seen is kept")
}

// TestMarkTracesDirtyAndListDirty pins the recompute queue: a fold, link or score marks its trace,
// and the delayed pass finds exactly the marked traces.
func TestMarkTracesDirtyAndListDirty(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	start := time.Now().UTC()
	for _, traceID := range []string{"t1", "t2"} {
		require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{{ProjectID: "p1", TraceID: traceID, SpanID: "root", Name: "agent", Kind: TraceSpanKindTask, Source: TraceSourceOTel, StartTime: start}}))
		_, err := store.RecomputeTraceRollup(ctx, "p1", traceID)
		require.NoError(t, err)
	}
	dirty, err := store.ListDirtyTraces(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, dirty, "nothing is queued until a trace is marked")

	keys := []TraceKey{{ProjectID: "p1", TraceID: "t2"}, {ProjectID: "p1", TraceID: "never-seen"}}
	require.NoError(t, store.MarkTracesDirty(ctx, keys))
	dirty, err = store.ListDirtyTraces(ctx, 10)
	require.NoError(t, err)
	require.Len(t, dirty, 2, "a trace marked before its first rollup is queued too")
	assert.ElementsMatch(t, keys, []TraceKey{dirty[0].TraceKey, dirty[1].TraceKey})

	// A trace marked again after it was listed stays queued: the clear names the mark it saw.
	stale := dirty[0]
	stale.MarkedAt = stale.MarkedAt.Add(-time.Minute)
	require.NoError(t, store.ClearTraceDirty(ctx, stale))
	remaining, err := store.ListDirtyTraces(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, remaining, 2, "a clear for an older mark leaves the newer mark in place")

	require.NoError(t, store.ClearTraceDirty(ctx, dirty[0]))
	remaining, err = store.ListDirtyTraces(ctx, 10)
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Equal(t, dirty[1].TraceKey, remaining[0].TraceKey)
}

// TestTraceSpanLinksAreStoredOncePerGatewayRow pins the link rule: a span holds one link per
// gateway row, a repeat link is a no-op, and links are found again from the gateway side.
func TestTraceSpanLinksAreStoredOncePerGatewayRow(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	link := func(logID string) *TraceSpanLink {
		return &TraceSpanLink{ProjectID: "p1", TraceID: "t1", SpanID: "s1", LogKind: TraceLinkedLogKindLLM, LogID: logID, Method: "header"}
	}
	require.NoError(t, store.CreateTraceSpanLinks(ctx, []*TraceSpanLink{link("log-1"), link("log-2")}))
	require.NoError(t, store.CreateTraceSpanLinks(ctx, []*TraceSpanLink{link("log-1")}), "a repeat link is a no-op")

	links, err := store.ListTraceSpanLinks(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Len(t, links, 2, "the fallback chain keeps one link per gateway row")

	byLog, err := store.FindTraceSpanLinksByLog(ctx, TraceLinkedLogKindLLM, []string{"log-2", "log-9"})
	require.NoError(t, err)
	require.Len(t, byLog, 1)
	assert.Equal(t, "s1", byLog[0].SpanID)
}

// TestTraceScoresFeedTheRollup pins scores: a score written again under the same id replaces the
// first, and the trace rollup carries the mean of each numeric score name.
func TestTraceScoresFeedTheRollup(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	end := now.Add(time.Second)
	require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{{ProjectID: "p1", TraceID: "t1", SpanID: "root", Source: TraceSourceBraintrust, Name: "root", Kind: TraceSpanKindTask, StartTime: now, EndTime: &end}}))

	require.NoError(t, store.UpsertTraceScores(ctx, []*TraceScore{
		{ID: "sc1", ProjectID: "p1", TraceID: "t1", SpanID: "root", Name: "accuracy", Value: new(0.5), Source: "sdk"},
		{ID: "sc2", ProjectID: "p1", TraceID: "t1", Name: "accuracy", Value: new(1.0), Source: "human", Author: "reviewer"},
		{ID: "sc3", ProjectID: "p1", TraceID: "t1", Name: "label", StringValue: "good", Source: "human"},
	}))
	require.NoError(t, store.UpsertTraceScores(ctx, []*TraceScore{{ID: "sc1", ProjectID: "p1", TraceID: "t1", SpanID: "root", Name: "accuracy", Value: new(0.0), Source: "sdk"}}))

	scores, err := store.ListTraceScores(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Len(t, scores, 3, "the rewritten score replaced its first version")

	trace, err := store.RecomputeTraceRollup(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"accuracy": 0.5}, trace.ScoresParsed, "numeric scores are averaged by name; text scores are left out")

	require.NoError(t, store.DeleteTraceScore(ctx, "p1", "sc2"))
	trace, err = store.RecomputeTraceRollup(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"accuracy": 0}, trace.ScoresParsed)
	stored, err := store.FindTrace(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"accuracy": 0}, stored.ScoresParsed, "a score that drops to zero is still stored")
}

// TestTraceCommentsRoundTrip pins comments: listed oldest first, removed by id.
func TestTraceCommentsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, store.CreateTraceComment(ctx, &TraceComment{ID: "c2", ProjectID: "p1", TraceID: "t1", Text: "second", Source: "human", CreatedAt: now.Add(time.Second)}))
	require.NoError(t, store.CreateTraceComment(ctx, &TraceComment{ID: "c1", ProjectID: "p1", TraceID: "t1", SpanID: "s1", Text: "first", Source: "sdk", CreatedAt: now}))
	comments, err := store.ListTraceComments(ctx, "p1", "t1")
	require.NoError(t, err)
	require.Len(t, comments, 2)
	assert.Equal(t, "first", comments[0].Text)
	require.NoError(t, store.DeleteTraceComment(ctx, "p1", "c1"))
	comments, err = store.ListTraceComments(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Len(t, comments, 1)
}

// TestTraceUploadLifecycle pins uploads: created pending, completed with bytes, found by id.
func TestTraceUploadLifecycle(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	require.NoError(t, store.CreateTraceUpload(ctx, &TraceUpload{ID: "u1", ProjectID: "p1", Source: TraceSourceBraintrust, Status: TraceUploadStatusPending, ContentType: "image/png"}))
	_, err := store.FindTraceUpload(ctx, "missing")
	assert.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, store.CompleteTraceUpload(ctx, "u1", TraceUploadStatusDone, "", []byte("png-bytes")))
	upload, err := store.FindTraceUpload(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, TraceUploadStatusDone, upload.Status)
	assert.Equal(t, []byte("png-bytes"), upload.Data)
	assert.Equal(t, int64(9), upload.SizeBytes)
	assert.ErrorIs(t, store.CompleteTraceUpload(ctx, "missing", TraceUploadStatusDone, "", nil), ErrNotFound)
}

// TestTraceRetentionDeletesOnlyExpiredRows pins per-row retention: a row past its own expires_at is
// deleted, a row with a later expiry or none is kept, and scores, comments and links go with their
// trace.
func TestTraceRetentionDeletesOnlyExpiredRows(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	for _, tc := range []struct {
		trace   string
		expires *time.Time
	}{{"old", &past}, {"new", &future}, {"forever", nil}} {
		_, err := store.BatchCreateTraceSpanParts(ctx, []*TraceSpanPart{{ProjectID: "p1", TraceID: tc.trace, SpanID: "s", Source: TraceSourceOTel, Payload: tc.trace, ExpiresAt: tc.expires}})
		require.NoError(t, err)
		require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{{ProjectID: "p1", TraceID: tc.trace, SpanID: "s", Source: TraceSourceOTel, StartTime: now, ExpiresAt: tc.expires}}))
		require.NoError(t, store.CreateTraceSpanLinks(ctx, []*TraceSpanLink{{ProjectID: "p1", TraceID: tc.trace, SpanID: "s", LogKind: TraceLinkedLogKindLLM, LogID: "log-" + tc.trace, Method: "header"}}))
		require.NoError(t, store.UpsertTraceScores(ctx, []*TraceScore{{ID: "score-" + tc.trace, ProjectID: "p1", TraceID: tc.trace, Name: "n", Value: new(1.0), Source: "sdk"}}))
		require.NoError(t, store.CreateTraceComment(ctx, &TraceComment{ID: "comment-" + tc.trace, ProjectID: "p1", TraceID: tc.trace, Text: "x", Source: "human"}))
		_, err = store.RecomputeTraceRollup(ctx, "p1", tc.trace)
		require.NoError(t, err)
	}
	require.NoError(t, store.CreateTraceUpload(ctx, &TraceUpload{ID: "u-old", ProjectID: "p1", Source: TraceSourceOTel, Status: TraceUploadStatusDone, ExpiresAt: &past}))
	require.NoError(t, store.CreateTraceUpload(ctx, &TraceUpload{ID: "u-new", ProjectID: "p1", Source: TraceSourceOTel, Status: TraceUploadStatusDone, ExpiresAt: &future}))

	deleted, err := store.DeleteExpiredTraceRowsBatch(ctx, now, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(7), deleted, "one part, span, trace, link, score, comment and upload")
	again, err := store.DeleteExpiredTraceRowsBatch(ctx, now, 100)
	require.NoError(t, err)
	assert.Zero(t, again)

	for _, kept := range []string{"new", "forever"} {
		spans, err := store.ListTraceSpans(ctx, "p1", kept)
		require.NoError(t, err)
		assert.Len(t, spans, 1, kept)
		scores, err := store.ListTraceScores(ctx, "p1", kept)
		require.NoError(t, err)
		assert.Len(t, scores, 1, kept)
	}
	_, err = store.FindTrace(ctx, "p1", "old")
	assert.ErrorIs(t, err, ErrNotFound)
	links, err := store.ListTraceSpanLinks(ctx, "p1", "old")
	require.NoError(t, err)
	assert.Empty(t, links)
	_, err = store.FindTraceUpload(ctx, "u-old")
	assert.ErrorIs(t, err, ErrNotFound)
}

// TestDeleteTraceRemovesEveryRowOfTheTrace pins deletion: parts, spans, links, scores, comments and
// the rollup row all go, and another trace of the same project is untouched.
func TestDeleteTraceRemovesEveryRowOfTheTrace(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, trace := range []string{"gone", "kept"} {
		_, err := store.BatchCreateTraceSpanParts(ctx, []*TraceSpanPart{{ProjectID: "p1", TraceID: trace, SpanID: "s", Source: TraceSourceOTel, Payload: trace}})
		require.NoError(t, err)
		require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{{ProjectID: "p1", TraceID: trace, SpanID: "s", Source: TraceSourceOTel, StartTime: now}}))
		require.NoError(t, store.CreateTraceSpanLinks(ctx, []*TraceSpanLink{{ProjectID: "p1", TraceID: trace, SpanID: "s", LogKind: TraceLinkedLogKindLLM, LogID: "l-" + trace, Method: "header"}}))
		require.NoError(t, store.UpsertTraceScores(ctx, []*TraceScore{{ID: "sc-" + trace, ProjectID: "p1", TraceID: trace, Name: "n", Value: new(1.0), Source: "sdk"}}))
		require.NoError(t, store.CreateTraceComment(ctx, &TraceComment{ID: "c-" + trace, ProjectID: "p1", TraceID: trace, Text: "x", Source: "human"}))
		_, err = store.RecomputeTraceRollup(ctx, "p1", trace)
		require.NoError(t, err)
	}
	require.NoError(t, store.DeleteTraces(ctx, []TraceKey{{ProjectID: "p1", TraceID: "gone"}}))
	_, err := store.FindTrace(ctx, "p1", "gone")
	assert.ErrorIs(t, err, ErrNotFound)
	for _, model := range []any{&TraceSpanPart{}, &TraceSpan{}, &TraceSpanLink{}, &Trace{}, &TraceScore{}, &TraceComment{}} {
		var count int64
		require.NoError(t, store.db.Model(model).Where("trace_id = ?", "gone").Count(&count).Error)
		assert.Zero(t, count, "%T", model)
		require.NoError(t, store.db.Model(model).Where("trace_id = ?", "kept").Count(&count).Error)
		assert.Equal(t, int64(1), count, "%T", model)
	}
}

// TestTraceUploadBytesLiveOnTheRowWithoutAnObjectStore pins upload bytes on a SQL store: saved on
// the row, read back whole.
func TestTraceUploadBytesLiveOnTheRowWithoutAnObjectStore(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	assert.True(t, store.TraceUploadsAvailable())
	require.NoError(t, store.CreateTraceUpload(ctx, &TraceUpload{ID: "u1", ProjectID: "p1", Source: TraceSourceLangfuse, ContentType: "image/png"}))
	require.NoError(t, store.SaveTraceUploadBytes(ctx, "u1", []byte("png")))
	upload, data, err := store.ReadTraceUploadBytes(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, []byte("png"), data)
	assert.Equal(t, TraceUploadStatusDone, upload.Status)
	assert.ErrorIs(t, store.SaveTraceUploadBytes(ctx, "missing", []byte("x")), ErrNotFound)
}
