package logstore

import (
	"context"
	"fmt"
	"gorm.io/gorm"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTraceTestStore(t *testing.T) *RDBLogStore {
	t.Helper()
	store, err := newSqliteLogStore(context.Background(), &SQLiteConfig{Path: filepath.Join(t.TempDir(), "traces.db")}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	return store
}

// newPostgresTraceTestStore returns a store on fresh trace tables in the package's Postgres test
// schema, and skips when Postgres is not reachable.
func newPostgresTraceTestStore(t *testing.T) *RDBLogStore {
	t.Helper()
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping")
	}
	for _, model := range traceModels() {
		require.NoError(t, db.Migrator().DropTable(model))
	}
	require.NoError(t, db.AutoMigrate(traceModels()...))
	return &RDBLogStore{db: db, logger: testLogger{}}
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

// TestCompleteTraceUploadDoesNotRecreateASweptUpload pins that a completion racing the retention
// sweep does not bring the expired upload back: the row is gone between the read and the write.
func TestCompleteTraceUploadDoesNotRecreateASweptUpload(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	require.NoError(t, store.CreateTraceUpload(ctx, &TraceUpload{ID: "u1", ProjectID: "p1", Source: TraceSourceBraintrust}))
	fired := false
	var sweepErr error
	require.NoError(t, store.db.Callback().Query().After("gorm:query").Register("sweep_after_upload_read", func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "trace_uploads" {
			return
		}
		fired = true
		sweepErr = tx.Session(&gorm.Session{NewDB: true}).Exec("DELETE FROM trace_uploads WHERE id = 'u1'").Error
	}))
	t.Cleanup(func() { _ = store.db.Callback().Query().Remove("sweep_after_upload_read") })
	err := store.CompleteTraceUpload(ctx, "u1", TraceUploadStatusDone, "", []byte("bytes"))
	require.True(t, fired)
	require.NoError(t, sweepErr)
	assert.ErrorIs(t, err, ErrNotFound, "the completion finds the upload gone")
	_, err = store.FindTraceUpload(ctx, "u1")
	assert.ErrorIs(t, err, ErrNotFound, "the swept upload is not recreated with its bytes")
}

// TestTraceRetentionDeletesOnlyExpiredRows pins per-row retention: a row past its own expires_at is
// deleted, a row with a later expiry or none is kept, and scores, comments and links go with their
// trace.
func TestTraceRetentionDeletesOnlyExpiredRows(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	// The recompute runs before "old" expires, as it does in production; the sweep runs after.
	soon, sweep, future := now.Add(time.Hour), now.Add(2*time.Hour), now.Add(3*time.Hour)
	for _, tc := range []struct {
		trace   string
		expires *time.Time
	}{{"old", &soon}, {"new", &future}, {"forever", nil}} {
		_, err := store.BatchCreateTraceSpanParts(ctx, []*TraceSpanPart{{ProjectID: "p1", TraceID: tc.trace, SpanID: "s", Source: TraceSourceOTel, Payload: tc.trace, ExpiresAt: tc.expires}})
		require.NoError(t, err)
		require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{{ProjectID: "p1", TraceID: tc.trace, SpanID: "s", Source: TraceSourceOTel, StartTime: now, ExpiresAt: tc.expires}}))
		require.NoError(t, store.CreateTraceSpanLinks(ctx, []*TraceSpanLink{{ProjectID: "p1", TraceID: tc.trace, SpanID: "s", LogKind: TraceLinkedLogKindLLM, LogID: "log-" + tc.trace, Method: "header"}}))
		require.NoError(t, store.UpsertTraceScores(ctx, []*TraceScore{{ID: "score-" + tc.trace, ProjectID: "p1", TraceID: tc.trace, Name: "n", Value: new(1.0), Source: "sdk"}}))
		require.NoError(t, store.CreateTraceComment(ctx, &TraceComment{ID: "comment-" + tc.trace, ProjectID: "p1", TraceID: tc.trace, Text: "x", Source: "human"}))
		_, err = store.RecomputeTraceRollup(ctx, "p1", tc.trace)
		require.NoError(t, err)
	}
	require.NoError(t, store.CreateTraceUpload(ctx, &TraceUpload{ID: "u-old", ProjectID: "p1", Source: TraceSourceOTel, Status: TraceUploadStatusDone, ExpiresAt: &soon}))
	require.NoError(t, store.CreateTraceUpload(ctx, &TraceUpload{ID: "u-new", ProjectID: "p1", Source: TraceSourceOTel, Status: TraceUploadStatusDone, ExpiresAt: &future}))

	deleted, err := store.DeleteExpiredTraceRowsBatch(ctx, sweep, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(7), deleted, "one part, span, trace, link, score, comment and upload")
	again, err := store.DeleteExpiredTraceRowsBatch(ctx, sweep, 100)
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

// assertFirstRollupGivesAnnotationsItsExpiry pins that a score or comment written before its trace's
// first rollup, and so with no expiry, takes the trace's expiry when that rollup is built. It runs on
// SQLite here and on ClickHouse, where a nil expiry would keep the row until 2100.
func assertFirstRollupGivesAnnotationsItsExpiry(t *testing.T, store *RDBLogStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	traceID := fmt.Sprintf("early-%d", now.UnixNano())
	require.NoError(t, store.UpsertTraceScores(ctx, []*TraceScore{{ID: traceID + "-score", ProjectID: "p1", TraceID: traceID, Name: "n", Value: new(1.0), Source: TraceAuthorSDK}}))
	require.NoError(t, store.CreateTraceComment(ctx, &TraceComment{ID: traceID + "-comment", ProjectID: "p1", TraceID: traceID, Text: "early", Source: TraceAuthorSDK}))
	expires := now.Add(time.Hour)
	require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{{ProjectID: "p1", TraceID: traceID, SpanID: "s", Source: TraceSourceOTel, StartTime: now, ExpiresAt: &expires}}))
	_, err := store.RecomputeTraceRollup(ctx, "p1", traceID)
	require.NoError(t, err)
	trace, err := store.FindTrace(ctx, "p1", traceID)
	require.NoError(t, err)
	require.NotNil(t, trace.ExpiresAt)
	scores, err := store.ListTraceScores(ctx, "p1", traceID)
	require.NoError(t, err)
	require.Len(t, scores, 1)
	require.NotNil(t, scores[0].ExpiresAt, "the early score takes the trace's expiry")
	assert.True(t, scores[0].ExpiresAt.Equal(*trace.ExpiresAt), "score expires %s, trace %s", scores[0].ExpiresAt, trace.ExpiresAt)
	comments, err := store.ListTraceComments(ctx, "p1", traceID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	require.NotNil(t, comments[0].ExpiresAt, "the early comment takes the trace's expiry")
	assert.True(t, comments[0].ExpiresAt.Equal(*trace.ExpiresAt), "comment expires %s, trace %s", comments[0].ExpiresAt, trace.ExpiresAt)
	assert.Equal(t, 1.0, *scores[0].Value, "the score keeps its value")
	assert.Equal(t, "early", comments[0].Text)
}

func TestFirstRollupGivesAnnotationsItsExpiry(t *testing.T) {
	assertFirstRollupGivesAnnotationsItsExpiry(t, newTraceTestStore(t))
}

// TestUpsertTraceScoresCollapsesDuplicateIDs pins that one call naming the same score twice stores
// the last one. Postgres rejects a multi-row ON CONFLICT that touches one row twice, and AddScores
// derives one id for two id-less scores with the same trace, span, name and source.
func TestUpsertTraceScoresCollapsesDuplicateIDs(t *testing.T) {
	stores := map[string]func(*testing.T) *RDBLogStore{"sqlite": newTraceTestStore, "postgres": newPostgresTraceTestStore}
	for name, open := range stores {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := open(t)
			require.NoError(t, store.UpsertTraceScores(ctx, []*TraceScore{
				{ID: "dup", ProjectID: "p1", TraceID: "t", Name: "n", Value: new(1.0), Source: TraceAuthorSDK},
				{ID: "other", ProjectID: "p1", TraceID: "t", Name: "m", Value: new(5.0), Source: TraceAuthorSDK},
				{ID: "dup", ProjectID: "p1", TraceID: "t", Name: "n", Value: new(2.0), Source: TraceAuthorSDK},
				{ID: "dup", ProjectID: "p2", TraceID: "t", Name: "n", Value: new(3.0), Source: TraceAuthorSDK},
			}))
			scores, err := store.ListTraceScores(ctx, "p1", "t")
			require.NoError(t, err)
			values := map[string]float64{}
			for _, score := range scores {
				values[score.ID] = *score.Value
			}
			assert.Equal(t, map[string]float64{"dup": 2.0, "other": 5.0}, values, "one row per id, the last one wins")
			other, err := store.ListTraceScores(ctx, "p2", "t")
			require.NoError(t, err)
			require.Len(t, other, 1, "the same id in another project is its own row")
			assert.Equal(t, 3.0, *other[0].Value)
		})
	}
}

// assertPastNextExpiryMarksPage pins that one retention pass queues every trace whose next expiry
// has passed, not only the first page: ClickHouse runs retention once a day, so a single page would
// build a backlog that never drains. Two traces share an expiry to cover the cursor's tie-break.
func assertPastNextExpiryMarksPage(t *testing.T, store *RDBLogStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	prefix := fmt.Sprintf("paged-%d-", now.UnixNano())
	expiries := []time.Duration{-5 * time.Hour, -4*time.Hour - 300*time.Millisecond, -4*time.Hour - 300*time.Millisecond, -3*time.Hour + 250*time.Millisecond, -time.Minute}
	want := map[string]bool{}
	for i, offset := range expiries {
		next := now.Add(offset)
		traceID := fmt.Sprintf("%s%d", prefix, i)
		want[traceID] = true
		require.NoError(t, store.db.Create(&Trace{ProjectID: "p1", TraceID: traceID, StartTime: now, NextExpiry: &next}).Error)
	}
	require.NoError(t, store.markTracesPastNextExpiry(ctx, now, 2))
	marks, err := store.ListDirtyTraces(ctx, 1000)
	require.NoError(t, err)
	got := map[string]bool{}
	for _, mark := range marks {
		if strings.HasPrefix(mark.TraceID, prefix) {
			got[mark.TraceID] = true
		}
	}
	assert.Equal(t, want, got, "every overdue trace is queued in one pass")
}

func TestMarkTracesPastNextExpiryPages(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { assertPastNextExpiryMarksPage(t, newTraceTestStore(t)) })
	t.Run("postgres", func(t *testing.T) { assertPastNextExpiryMarksPage(t, newPostgresTraceTestStore(t)) })
}

// seedTraceSpan stores one span (and its part) with the given expiry.
func seedTraceSpan(t *testing.T, store *RDBLogStore, trace, span string, at time.Time, expires *time.Time) {
	t.Helper()
	ctx := context.Background()
	_, err := store.BatchCreateTraceSpanParts(ctx, []*TraceSpanPart{{ProjectID: "p1", TraceID: trace, SpanID: span, Source: TraceSourceOTel, Payload: span, ExpiresAt: expires}})
	require.NoError(t, err)
	require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{{ProjectID: "p1", TraceID: trace, SpanID: span, Source: TraceSourceOTel, StartTime: at, ExpiresAt: expires}}))
}

// Retention goes by each row's own expiry, in bounded batches, and never removes a row
// that has not expired: not a span that arrived after the trace was picked, not a score
// that outlives its trace, and an expired span is removed even from a trace kept forever.
func TestTraceRetentionGoesByEachRowsOwnExpiry(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)

	t.Run("a span that arrives before the rollup catches up survives", func(t *testing.T) {
		store := newTraceTestStore(t)
		soon, sweep, later := now.Add(time.Hour), now.Add(2*time.Hour), now.Add(3*time.Hour)
		seedTraceSpan(t, store, "t", "old", now, &soon)
		_, err := store.RecomputeTraceRollup(ctx, "p1", "t")
		require.NoError(t, err)
		seedTraceSpan(t, store, "t", "late", now, &later) // the rollup still says "expired" at the sweep

		_, err = store.DeleteExpiredTraceRowsBatch(ctx, sweep, 100)
		require.NoError(t, err)
		spans, err := store.ListTraceSpans(ctx, "p1", "t")
		require.NoError(t, err)
		require.Len(t, spans, 1, "only the expired span goes")
		assert.Equal(t, "late", spans[0].SpanID)
		_, err = store.FindTrace(ctx, "p1", "t")
		assert.NoError(t, err, "the rollup stays while the trace has spans")
	})

	t.Run("a large trace drains in bounded batches", func(t *testing.T) {
		store := newTraceTestStore(t)
		const batch = 10
		soon, sweep := now.Add(time.Hour), now.Add(2*time.Hour)
		for i := range 3 * batch {
			seedTraceSpan(t, store, "big", fmt.Sprintf("s%02d", i), now, &soon)
		}
		_, err := store.RecomputeTraceRollup(ctx, "p1", "big")
		require.NoError(t, err)
		_, err = store.FindTrace(ctx, "p1", "big")
		require.NoError(t, err, "the rollup exists before the spans expire")

		for range 3 {
			before, err := store.ListTraceSpans(ctx, "p1", "big")
			require.NoError(t, err)
			deleted, err := store.DeleteExpiredTraceRowsBatch(ctx, sweep, batch)
			require.NoError(t, err)
			assert.LessOrEqual(t, deleted, int64(batch), "a batch reports at most its size, or the drain stops early")
			after, err := store.ListTraceSpans(ctx, "p1", "big")
			require.NoError(t, err)
			assert.GreaterOrEqual(t, len(after), len(before)-batch, "a batch deletes at most its size of spans")
		}
		for range 10 {
			if _, err := store.DeleteExpiredTraceRowsBatch(ctx, sweep, batch); err != nil {
				t.Fatal(err)
			}
		}
		spans, err := store.ListTraceSpans(ctx, "p1", "big")
		require.NoError(t, err)
		assert.Empty(t, spans)
		_, err = store.FindTrace(ctx, "p1", "big")
		assert.ErrorIs(t, err, ErrNotFound, "the rollup goes once its spans are gone")
	})

	t.Run("a score that outlives its trace stays", func(t *testing.T) {
		store := newTraceTestStore(t)
		seedTraceSpan(t, store, "t", "s", now, &past)
		require.NoError(t, store.UpsertTraceScores(ctx, []*TraceScore{{ID: "keep", ProjectID: "p1", TraceID: "t", Name: "n", Value: new(1.0), Source: "human", ExpiresAt: &future}}))
		_, err := store.RecomputeTraceRollup(ctx, "p1", "t")
		require.NoError(t, err)

		for range 3 {
			_, err = store.DeleteExpiredTraceRowsBatch(ctx, now, 100)
			require.NoError(t, err)
		}
		scores, err := store.ListTraceScores(ctx, "p1", "t")
		require.NoError(t, err)
		assert.Len(t, scores, 1, "the score has its own expiry")
	})

	t.Run("an expired span goes even from a trace kept forever", func(t *testing.T) {
		store := newTraceTestStore(t)
		seedTraceSpan(t, store, "t", "forever", now, nil)
		seedTraceSpan(t, store, "t", "expired", now, &past)
		_, err := store.RecomputeTraceRollup(ctx, "p1", "t")
		require.NoError(t, err)
		before, err := store.ListDirtyTraces(ctx, 10)
		require.NoError(t, err)
		require.Empty(t, before, "nothing is waiting to be recomputed yet")

		_, err = store.DeleteExpiredTraceRowsBatch(ctx, now, 100)
		require.NoError(t, err)
		spans, err := store.ListTraceSpans(ctx, "p1", "t")
		require.NoError(t, err)
		require.Len(t, spans, 1)
		assert.Equal(t, "forever", spans[0].SpanID)
		dirty, err := store.ListDirtyTraces(ctx, 10)
		require.NoError(t, err)
		require.Len(t, dirty, 1, "the trace that lost a span is recomputed")
		assert.Equal(t, "t", dirty[0].TraceID)
	})
}

// Retention and rollups keep a deleted or expired trace gone, write each trace's cleanup as one
// unit, and mark a trace for a recompute whenever something counted in it expires.
func TestTraceRetentionCleanupIsWhole(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	dirty := func(t *testing.T, store *RDBLogStore, traceID string) bool {
		t.Helper()
		marks, err := store.ListDirtyTraces(ctx, 100)
		require.NoError(t, err)
		for _, mark := range marks {
			if mark.TraceID == traceID {
				return true
			}
		}
		return false
	}

	t.Run("a recompute of a trace with no spans left does not bring its rollup back", func(t *testing.T) {
		store := newTraceTestStore(t)
		soon, sweep := now.Add(time.Hour), now.Add(2*time.Hour)
		seedTraceSpan(t, store, "gone", "s", now, &soon)
		_, err := store.RecomputeTraceRollup(ctx, "p1", "gone")
		require.NoError(t, err)
		_, err = store.DeleteExpiredTraceRowsBatch(ctx, sweep, 100)
		require.NoError(t, err)
		_, err = store.RecomputeTraceRollup(ctx, "p1", "gone") // the worker's pass for the dirty mark
		require.NoError(t, err)
		_, err = store.DeleteExpiredTraceRowsBatch(ctx, sweep, 100)
		require.NoError(t, err)
		_, err = store.FindTrace(ctx, "p1", "gone")
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("a score is not stored without an expiry when the trace cannot be read", func(t *testing.T) {
		store := newTraceTestStore(t)
		require.NoError(t, store.db.Migrator().DropTable(&Trace{}))
		err := store.UpsertTraceScores(ctx, []*TraceScore{{ID: "s", ProjectID: "p1", TraceID: "t", Name: "n", Value: new(1.0), Source: TraceAuthorSDK}})
		assert.Error(t, err, "a failed read of the trace's expiry is an error, not no expiry")
	})

	t.Run("a score or comment id is scoped to its project", func(t *testing.T) {
		store := newTraceTestStore(t)
		for _, project := range []string{"pa", "pb"} {
			require.NoError(t, store.UpsertTraceScores(ctx, []*TraceScore{{ID: "same", ProjectID: project, TraceID: "t", Name: "n", Value: new(1.0), Source: TraceAuthorSDK, ExpiresAt: &future}}))
			require.NoError(t, store.CreateTraceComment(ctx, &TraceComment{ID: "same", ProjectID: project, TraceID: "t", Text: project, Source: TraceAuthorSDK, ExpiresAt: &future}))
		}
		scores, err := store.ListTraceScores(ctx, "pa", "t")
		require.NoError(t, err)
		assert.Len(t, scores, 1, "project B's write does not move project A's score")
		comments, err := store.ListTraceComments(ctx, "pa", "t")
		require.NoError(t, err)
		assert.Len(t, comments, 1)
	})

	t.Run("an expired comment does not take another project's comment with the same id", func(t *testing.T) {
		store := newTraceTestStore(t)
		require.NoError(t, store.CreateTraceComment(ctx, &TraceComment{ID: "same", ProjectID: "pa", TraceID: "t", Text: "old", Source: TraceAuthorHuman, ExpiresAt: &past}))
		require.NoError(t, store.CreateTraceComment(ctx, &TraceComment{ID: "same", ProjectID: "pb", TraceID: "t", Text: "live", Source: TraceAuthorHuman, ExpiresAt: &future}))
		_, err := store.DeleteExpiredTraceRowsBatch(ctx, now, 1)
		require.NoError(t, err)
		gone, err := store.ListTraceComments(ctx, "pa", "t")
		require.NoError(t, err)
		assert.Empty(t, gone, "the expired comment goes")
		kept, err := store.ListTraceComments(ctx, "pb", "t")
		require.NoError(t, err)
		assert.Len(t, kept, 1, "project B's unexpired comment with the same id stays")
	})

	t.Run("an expired score marks its trace for a recompute", func(t *testing.T) {
		store := newTraceTestStore(t)
		seedTraceSpan(t, store, "scored", "s", now, &future)
		require.NoError(t, store.UpsertTraceScores(ctx, []*TraceScore{
			{ID: "low", ProjectID: "p1", TraceID: "scored", Name: "accuracy", Value: new(0.0), Source: TraceAuthorSDK, ExpiresAt: &past},
			{ID: "high", ProjectID: "p1", TraceID: "scored", Name: "accuracy", Value: new(1.0), Source: TraceAuthorSDK, ExpiresAt: &future},
		}))
		_, err := store.RecomputeTraceRollup(ctx, "p1", "scored")
		require.NoError(t, err)
		marks, err := store.ListDirtyTraces(ctx, 100)
		require.NoError(t, err)
		for _, mark := range marks {
			require.NoError(t, store.ClearTraceDirty(ctx, mark))
		}
		require.False(t, dirty(t, store, "scored"))
		deleted, err := store.deleteExpiredScoresBatch(ctx, now, 100)
		require.NoError(t, err)
		assert.Equal(t, int64(1), deleted)
		scores, err := store.ListTraceScores(ctx, "p1", "scored")
		require.NoError(t, err)
		require.Len(t, scores, 1)
		assert.Equal(t, "high", scores[0].ID)
		assert.True(t, dirty(t, store, "scored"), "the score sweep marks the trace whose rollup counted the score")
		_, err = store.RecomputeTraceRollup(ctx, "p1", "scored")
		require.NoError(t, err)
		stored, err := store.FindTrace(ctx, "p1", "scored")
		require.NoError(t, err)
		assert.Equal(t, 1.0, stored.ScoresParsed["accuracy"])
	})

	t.Run("a delete that lands between the recompute's read and its write leaves no rollup", func(t *testing.T) {
		store := newTraceTestStore(t)
		seedTraceSpan(t, store, "raced", "s", now, nil)
		fired := false
		var deleteErr error
		require.NoError(t, store.db.Callback().Query().After("gorm:query").Register("delete_after_span_read", func(tx *gorm.DB) {
			if fired || tx.Statement.Table != "trace_spans" {
				return
			}
			fired = true
			deleteErr = store.DeleteTraces(context.Background(), []TraceKey{{ProjectID: "p1", TraceID: "raced"}})
		}))
		t.Cleanup(func() { _ = store.db.Callback().Query().Remove("delete_after_span_read") })
		_, err := store.RecomputeTraceRollup(ctx, "p1", "raced")
		require.True(t, fired)
		require.NoError(t, deleteErr)
		assert.ErrorIs(t, err, ErrNotFound, "the recompute reports the trace gone")
		_, err = store.FindTrace(ctx, "p1", "raced")
		assert.ErrorIs(t, err, ErrNotFound, "a rollup with no spans would outlive the trace forever")
	})

	t.Run("a span past its expiry is not counted before the sweep removes it", func(t *testing.T) {
		store := newTraceTestStore(t)
		require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{
			{ProjectID: "p1", TraceID: "overdue", SpanID: "gone", Source: TraceSourceOTel, Kind: TraceSpanKindLLM, StartTime: now, TotalTokens: 100, ExpiresAt: &past},
			{ProjectID: "p1", TraceID: "overdue", SpanID: "kept", Source: TraceSourceOTel, Kind: TraceSpanKindLLM, StartTime: now, TotalTokens: 50},
		}))
		trace, err := store.RecomputeTraceRollup(ctx, "p1", "overdue")
		require.NoError(t, err)
		assert.Equal(t, 50, trace.TotalTokens, "the expired span is left out")
		assert.Nil(t, trace.NextExpiry)
	})

	t.Run("a span's cleanup is one write: a failed link delete keeps the span", func(t *testing.T) {
		store := newTraceTestStore(t)
		seedTraceSpan(t, store, "linked", "old", now, &past)
		seedTraceSpan(t, store, "linked", "live", now, &future)
		require.NoError(t, store.CreateTraceSpanLinks(ctx, []*TraceSpanLink{{ProjectID: "p1", TraceID: "linked", SpanID: "old", LogKind: TraceLinkedLogKindLLM, LogID: "log-1"}}))
		require.NoError(t, store.db.Exec("CREATE TRIGGER fail_link_delete BEFORE DELETE ON trace_span_links BEGIN SELECT RAISE(ABORT, 'boom'); END").Error)
		_, err := store.DeleteExpiredTraceRowsBatch(ctx, now, 100)
		require.Error(t, err)
		spans, err := store.ListTraceSpans(ctx, "p1", "linked")
		require.NoError(t, err)
		assert.Len(t, spans, 2, "the span delete rolled back with the failed link delete")
	})

	t.Run("a trace's cleanup is one write: a failed child delete keeps the rollup", func(t *testing.T) {
		store := newTraceTestStore(t)
		soon, sweep := now.Add(time.Hour), now.Add(2*time.Hour)
		seedTraceSpan(t, store, "empty", "s", now, &soon)
		_, err := store.RecomputeTraceRollup(ctx, "p1", "empty")
		require.NoError(t, err)
		require.NoError(t, store.db.Create(&TraceComment{ID: "note", ProjectID: "p1", TraceID: "empty", Text: "kept", Source: TraceAuthorHuman, CreatedAt: now}).Error)
		require.NoError(t, store.db.Exec("CREATE TRIGGER fail_comment_delete BEFORE DELETE ON trace_comments BEGIN SELECT RAISE(ABORT, 'boom'); END").Error)
		_, err = store.DeleteExpiredTraceRowsBatch(ctx, sweep, 100)
		require.Error(t, err)
		_, err = store.FindTrace(ctx, "p1", "empty")
		assert.NoError(t, err, "the rollup delete rolled back with the failed comment delete")
	})

	t.Run("a mark written after the empty-trace delete stays", func(t *testing.T) {
		store := newTraceTestStore(t)
		soon, sweep := now.Add(time.Hour), now.Add(2*time.Hour)
		reborn := sweep.Add(time.Hour) // ingest stamps its mark after the cleanup's now
		seedTraceSpan(t, store, "reborn", "s", now, &soon)
		_, err := store.RecomputeTraceRollup(ctx, "p1", "reborn")
		require.NoError(t, err)
		fired := false
		var markErr error
		require.NoError(t, store.db.Callback().Raw().After("gorm:raw").Register("mark_after_trace_delete", func(tx *gorm.DB) {
			if fired || !strings.HasPrefix(tx.Statement.SQL.String(), "DELETE FROM traces ") {
				return
			}
			fired = true
			// The mark goes through the cleanup's own transaction, so it lands before the child deletes.
			markErr = dirtyMarkUpsert(tx.Session(&gorm.Session{NewDB: true})).Create(&TraceDirtyMark{ProjectID: "p1", TraceID: "reborn", Dirty: true, MarkedAt: reborn}).Error
		}))
		t.Cleanup(func() { _ = store.db.Callback().Raw().Remove("mark_after_trace_delete") })
		_, err = store.DeleteExpiredTraceRowsBatch(ctx, sweep, 100)
		require.NoError(t, err)
		require.True(t, fired)
		require.NoError(t, markErr)
		var marks []TraceDirtyMark
		require.NoError(t, store.db.Where("project_id = ? AND trace_id = ?", "p1", "reborn").Find(&marks).Error)
		require.Len(t, marks, 1, "the new mark is not taken by the cleanup")
		assert.True(t, marks[0].Dirty)
		assert.True(t, marks[0].MarkedAt.Equal(reborn), "the stored mark is the one written after the delete: %s, want %s", marks[0].MarkedAt, reborn)
	})

	t.Run("a span kept alive between pick and delete keeps its links", func(t *testing.T) {
		store := newTraceTestStore(t)
		seedTraceSpan(t, store, "refold", "s", now, &past)
		require.NoError(t, store.CreateTraceSpanLinks(ctx, []*TraceSpanLink{{ProjectID: "p1", TraceID: "refold", SpanID: "s", LogKind: TraceLinkedLogKindLLM, LogID: "log-1"}}))
		fired := false
		// The refold lands after the pick and before the span delete.
		require.NoError(t, store.db.Callback().Raw().Before("gorm:raw").Register("refold_after_pick", func(tx *gorm.DB) {
			if fired || !strings.HasPrefix(tx.Statement.SQL.String(), "DELETE FROM trace_spans ") {
				return
			}
			fired = true
			_ = tx.Session(&gorm.Session{NewDB: true}).Exec("UPDATE trace_spans SET expires_at = ? WHERE span_id = 's'", future).Error
		}))
		t.Cleanup(func() { _ = store.db.Callback().Raw().Remove("refold_after_pick") })
		_, err := store.DeleteExpiredTraceRowsBatch(ctx, now, 100)
		require.NoError(t, err)
		require.True(t, fired)
		links, err := store.ListTraceSpanLinks(ctx, "p1", "refold")
		require.NoError(t, err)
		assert.Len(t, links, 1, "the span stayed, so its link stays")
	})
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

// An estimated cost is a fallback: it is written only where the span has no cost. A span whose SDK
// cost was committed after the enrichment read keeps that cost.
func TestEstimatedCostNeverReplacesAStoredCost(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{{ProjectID: "p1", TraceID: "t", SpanID: "sdk", Source: TraceSourceOTel, StartTime: time.Now(), Cost: new(0.5), Provider: "anthropic"}}))
	require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{{ProjectID: "p1", TraceID: "t", SpanID: "bare", Source: TraceSourceOTel, StartTime: time.Now()}}))

	estimate := TraceSpanEnrichment{Cost: new(9.0), CostEstimated: new(true), Provider: new("openai")}
	for _, span := range []string{"sdk", "bare"} {
		require.NoError(t, store.UpdateTraceSpanEnrichment(ctx, "p1", "t", span, estimate))
	}
	found, err := store.FindTraceSpans(ctx, "p1", []TraceSpanKey{{TraceID: "t", SpanID: "sdk"}, {TraceID: "t", SpanID: "bare"}})
	require.NoError(t, err)
	byID := map[string]*TraceSpan{}
	for _, span := range found {
		byID[span.SpanID] = span
	}
	require.NotNil(t, byID["sdk"].Cost)
	assert.Equal(t, 0.5, *byID["sdk"].Cost, "the SDK's cost stays")
	assert.False(t, byID["sdk"].CostEstimated)
	assert.Equal(t, "anthropic", byID["sdk"].Provider)
	require.NotNil(t, byID["bare"].Cost)
	assert.Equal(t, 9.0, *byID["bare"].Cost, "a span with no cost gets the estimate")
	assert.True(t, byID["bare"].CostEstimated)
}

// seedTraceRollups stores three rolled-up traces across two projects for the read-side tests.
func seedTraceRollups(t *testing.T, store *RDBLogStore) time.Time {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	seed := func(project, trace string, offset time.Duration, mutate func(root, llm *TraceSpan)) {
		start := base.Add(offset)
		end := start.Add(2 * time.Second)
		root := &TraceSpan{ProjectID: project, TraceID: trace, SpanID: "root", Source: TraceSourceBraintrust, Name: "agent " + trace, Kind: TraceSpanKindTask, StartTime: start, EndTime: &end, Input: "hello " + trace, SessionID: "sess-1", UserID: "u1", Environment: "prod", TagsParsed: []string{"nightly"}}
		llm := &TraceSpan{ProjectID: project, TraceID: trace, SpanID: "llm", ParentSpanIDsParsed: []string{"root"}, Source: TraceSourceBraintrust, Name: "chat", Kind: TraceSpanKindLLM, Model: "gpt-4o", StartTime: start, EndTime: &end, PromptTokens: 80, CompletionTokens: 20, TotalTokens: 100, Cost: new(0.01), SessionID: "sess-1"}
		if mutate != nil {
			mutate(root, llm)
		}
		require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{root, llm}))
		_, err := store.RecomputeTraceRollup(ctx, project, trace)
		require.NoError(t, err)
	}
	seed("p1", "t1", 0, nil)
	seed("p1", "t2", time.Hour, func(root, llm *TraceSpan) {
		llm.Status, llm.Error = TraceSpanStatusError, "rate limited"
		llm.LinkSnapshotParsed = &TraceLinkSnapshot{TotalTokens: 300, PromptTokens: 250, CompletionTokens: 50, Cost: new(0.05)}
		root.SessionID, llm.SessionID, root.TagsParsed = "sess-2", "sess-2", nil
	})
	seed("p2", "t3", 2*time.Hour, func(root, llm *TraceSpan) {
		root.Source, llm.Source, root.SessionID, llm.SessionID = TraceSourceLangfuse, TraceSourceLangfuse, "", ""
		llm.Cost, llm.Model = nil, "house-model"
	})
	return base
}

// TestSearchTracesFiltersSortsAndPages pins the trace list.
func TestSearchTracesFiltersSortsAndPages(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	base := seedTraceRollups(t, store)

	all, err := store.SearchTraces(ctx, TraceFilters{}, PaginationOptions{Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, int64(3), all.Pagination.TotalCount)
	require.Len(t, all.Traces, 2)
	assert.Equal(t, "t3", all.Traces[0].TraceID, "newest first")

	for name, tc := range map[string]struct {
		filters TraceFilters
		want    []string
	}{
		"project":     {TraceFilters{ProjectIDs: []string{"p1"}}, []string{"t2", "t1"}},
		"source":      {TraceFilters{Sources: []string{TraceSourceLangfuse}}, []string{"t3"}},
		"errors":      {TraceFilters{Status: TraceSpanStatusError}, []string{"t2"}},
		"no errors":   {TraceFilters{Status: TraceSpanStatusOK}, []string{"t3", "t1"}},
		"session":     {TraceFilters{SessionID: "sess-2"}, []string{"t2"}},
		"tag":         {TraceFilters{Tags: []string{"nightly"}}, []string{"t3", "t1"}},
		"search":      {TraceFilters{Search: "HELLO T1"}, []string{"t1"}},
		"like escape": {TraceFilters{Search: "%"}, nil},
		"time range":  {TraceFilters{StartTime: new(base.Add(30 * time.Minute)), EndTime: new(base.Add(90 * time.Minute))}, []string{"t2"}},
		"min tokens":  {TraceFilters{MinTokens: new(200)}, []string{"t2"}},
		"span model":  {TraceFilters{Models: []string{"house-model"}}, []string{"t3"}},
		"span kind":   {TraceFilters{Kinds: []string{TraceSpanKindLLM}, ProjectIDs: []string{"p2"}}, []string{"t3"}},
	} {
		result, err := store.SearchTraces(ctx, tc.filters, PaginationOptions{})
		require.NoError(t, err, name)
		var got []string
		for _, trace := range result.Traces {
			got = append(got, trace.TraceID)
		}
		assert.Equal(t, tc.want, got, name)
	}

	byCost, err := store.SearchTraces(ctx, TraceFilters{ProjectIDs: []string{"p1"}}, PaginationOptions{SortBy: "cost", Order: "asc"})
	require.NoError(t, err)
	assert.Equal(t, "t1", byCost.Traces[0].TraceID)

	spans, err := store.SearchTraceSpans(ctx, TraceFilters{Kinds: []string{TraceSpanKindLLM}, Models: []string{"gpt-4o"}}, PaginationOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(2), spans.Pagination.TotalCount)
}

// TestTraceStatsCountEachCallOnce pins the overview: a linked call is summed with the gateway's
// numbers, and the link share and unpriced count are reported.
func TestTraceStatsCountEachCallOnce(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	base := seedTraceRollups(t, store)

	stats, err := store.GetTraceStats(ctx, TraceFilters{})
	require.NoError(t, err)
	assert.Equal(t, int64(3), stats.TraceCount)
	assert.Equal(t, int64(6), stats.SpanCount)
	assert.Equal(t, int64(3), stats.LLMSpanCount)
	assert.Equal(t, int64(1), stats.LinkedSpanCount)
	assert.Equal(t, int64(1), stats.ErrorTraceCount)
	assert.Equal(t, int64(500), stats.TotalTokens, "100 + 300 from the gateway snapshot + 100")
	assert.InDelta(t, 0.06, stats.TotalCost, 1e-9)
	assert.Equal(t, int64(1), stats.UnpricedSpanCount)
	assert.InDelta(t, 2000, stats.AvgDurationMs, 1e-6)

	histogram, err := store.GetTraceHistogram(ctx, TraceFilters{StartTime: &base, EndTime: new(base.Add(3 * time.Hour))}, 3600)
	require.NoError(t, err)
	require.Len(t, histogram.Buckets, 4, "empty buckets inside the range are zero-filled")
	assert.Equal(t, int64(1), histogram.Buckets[0].Count)
	assert.Equal(t, int64(1), histogram.Buckets[1].Errors)
	assert.Zero(t, histogram.Buckets[3].Count)

	bySource, err := store.GetTraceBreakdown(ctx, TraceFilters{}, "source")
	require.NoError(t, err)
	require.Len(t, bySource, 2)
	assert.Equal(t, TraceSourceBraintrust, bySource[0].Key)
	assert.Equal(t, int64(2), bySource[0].TraceCount)
	_, err = store.GetTraceBreakdown(ctx, TraceFilters{}, "name; drop table traces")
	assert.Error(t, err)

	sessions, err := store.SearchTraceSessions(ctx, TraceFilters{}, PaginationOptions{})
	require.NoError(t, err)
	require.Len(t, sessions.Sessions, 2, "a trace with no session id is not a session")
	assert.Equal(t, "sess-2", sessions.Sessions[0].SessionID, "most recently active first")
	assert.Equal(t, int64(2), sessions.Pagination.TotalCount)

	filterData, err := store.GetTraceFilterData(ctx, []string{"p1"})
	require.NoError(t, err)
	assert.Equal(t, []string{TraceSourceBraintrust}, filterData.Sources)
	assert.Equal(t, []string{"gpt-4o"}, filterData.Models)
	assert.Equal(t, []string{TraceSpanKindLLM, TraceSpanKindTask}, filterData.Kinds)

	keys, err := store.ListProjectTraceKeys(ctx, "p1", 10)
	require.NoError(t, err)
	assert.Len(t, keys, 2)
}

// Search edge cases: a tag with a quote or a backslash matches its stored JSON form, rows that
// tie on the sort column page by their full identity, and the session count has no cap.
func TestTraceSearchEdgeCases(t *testing.T) {
	ctx := context.Background()

	t.Run("tags with quotes and backslashes", func(t *testing.T) {
		store := newTraceTestStore(t)
		root := &TraceSpan{ProjectID: "p1", TraceID: "t", SpanID: "root", Source: TraceSourceOTel, StartTime: time.Now(), TagsParsed: []string{`say "hi"`, `a\b`}}
		require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{root}))
		_, err := store.RecomputeTraceRollup(ctx, "p1", "t")
		require.NoError(t, err)
		for _, tag := range []string{`say "hi"`, `a\b`} {
			result, err := store.SearchTraces(ctx, TraceFilters{Tags: []string{tag}}, PaginationOptions{})
			require.NoError(t, err)
			assert.Len(t, result.Traces, 1, "tag %q", tag)
		}
	})

	t.Run("ties page by project too", func(t *testing.T) {
		store := newTraceTestStore(t)
		at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
		for _, project := range []string{"pa", "pb"} {
			require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{{ProjectID: project, TraceID: "same", SpanID: "root", Source: TraceSourceOTel, StartTime: at}}))
			_, err := store.RecomputeTraceRollup(ctx, project, "same")
			require.NoError(t, err)
		}
		first := func(order string) string {
			result, err := store.SearchTraces(ctx, TraceFilters{}, PaginationOptions{Limit: 1, Order: order})
			require.NoError(t, err)
			require.Len(t, result.Traces, 1)
			return result.Traces[0].ProjectID
		}
		assert.Equal(t, "pa", first("asc"))
		assert.Equal(t, "pb", first("desc"), "the tiebreak includes the project, in the sort's direction")
	})

	t.Run("the session count is exact past ten thousand", func(t *testing.T) {
		store := newTraceTestStore(t)
		traces := make([]Trace, 10_001)
		for i := range traces {
			traces[i] = Trace{ProjectID: "p1", TraceID: fmt.Sprintf("t%05d", i), SessionID: fmt.Sprintf("s%05d", i), StartTime: time.Now()}
		}
		require.NoError(t, store.db.CreateInBatches(&traces, 500).Error)
		result, err := store.SearchTraceSessions(ctx, TraceFilters{}, PaginationOptions{Limit: 1})
		require.NoError(t, err)
		assert.Equal(t, int64(10_001), result.Pagination.TotalCount)
	})
}

// A project's traces are listed from every table that holds their rows, not only from rollups:
// spans are stored before the first rollup runs, and a delete right after ingest must reach them.
func TestListProjectTraceKeysFindsTracesWithoutARollup(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	require.NoError(t, store.UpsertTraceSpans(ctx, []*TraceSpan{{ProjectID: "p1", TraceID: "fresh", SpanID: "s", Source: TraceSourceOTel, StartTime: time.Now()}}))
	_, err := store.BatchCreateTraceSpanParts(ctx, []*TraceSpanPart{{ProjectID: "p1", TraceID: "parts-only", SpanID: "s", Source: TraceSourceOTel, Payload: "x"}})
	require.NoError(t, err)
	keys, err := store.ListProjectTraceKeys(ctx, "p1", 10)
	require.NoError(t, err)
	var got []string
	for _, key := range keys {
		got = append(got, key.TraceID)
	}
	assert.ElementsMatch(t, []string{"fresh", "parts-only"}, got)
}

// A pending upload that is issued again gets a fresh window and token, so the new upload URL works.
// A finished upload is never reopened.
func TestCreateTraceUploadRefreshesAPendingUpload(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	old := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	require.NoError(t, store.CreateTraceUpload(ctx, &TraceUpload{ID: "bt-key", ProjectID: "p1", Source: TraceSourceBraintrust, Token: "first", CreatedAt: old}))
	require.NoError(t, store.CreateTraceUpload(ctx, &TraceUpload{ID: "bt-key", ProjectID: "p1", Source: TraceSourceBraintrust, Token: "second"}))
	upload, err := store.FindTraceUpload(ctx, "bt-key")
	require.NoError(t, err)
	assert.Equal(t, "second", upload.Token)
	assert.True(t, upload.CreatedAt.After(old), "the upload window starts again")

	require.NoError(t, store.SaveTraceUploadBytes(ctx, "bt-key", []byte("png")))
	require.NoError(t, store.CreateTraceUpload(ctx, &TraceUpload{ID: "bt-key", ProjectID: "p1", Source: TraceSourceBraintrust, Token: "third"}))
	done, err := store.FindTraceUpload(ctx, "bt-key")
	require.NoError(t, err)
	assert.Equal(t, TraceUploadStatusDone, done.Status, "a finished upload stays finished")
	assert.Equal(t, "second", done.Token)
}

// TestTraceExperimentsAreRegisteredOnce pins experiments: registering one again keeps the first
// row, and experiments are listed newest first for the projects asked for.
func TestTraceExperimentsAreRegisteredOnce(t *testing.T) {
	ctx := context.Background()
	store := newTraceTestStore(t)
	first := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	require.NoError(t, store.RegisterTraceExperiment(ctx, &TraceExperiment{ProjectID: "p1", ID: "exp-1", Name: "baseline", Source: TraceSourceBraintrust, CreatedAt: first}))
	require.NoError(t, store.RegisterTraceExperiment(ctx, &TraceExperiment{ProjectID: "p1", ID: "exp-1", Name: "renamed", Source: TraceSourceBraintrust}))
	require.NoError(t, store.RegisterTraceExperiment(ctx, &TraceExperiment{ProjectID: "p1", ID: "exp-2", Name: "candidate", Source: TraceSourceLangfuse, DatasetName: "golden"}))
	require.NoError(t, store.RegisterTraceExperiment(ctx, &TraceExperiment{ProjectID: "p2", ID: "exp-1", Name: "other project", Source: TraceSourceBraintrust}))

	found, err := store.FindTraceExperiment(ctx, "p1", "exp-1")
	require.NoError(t, err)
	assert.Equal(t, "baseline", found.Name, "a repeat registration keeps the stored row")
	assert.Equal(t, first, found.CreatedAt.UTC())
	_, err = store.FindTraceExperiment(ctx, "p1", "absent")
	assert.ErrorIs(t, err, ErrNotFound)
	listed, err := store.ListTraceExperiments(ctx, []string{"p1"}, 10)
	require.NoError(t, err)
	require.Len(t, listed, 2)
	assert.Equal(t, "candidate", listed[0].Name, "newest first")
	everywhere, err := store.ListTraceExperiments(ctx, nil, 10)
	require.NoError(t, err)
	assert.Len(t, everywhere, 3)
}
