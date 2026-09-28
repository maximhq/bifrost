package warp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/stretchr/testify/require"
)

type semanticLogReader struct {
	LogReaderStub
	logs       map[string]logstore.Log
	sawContext context.Context
	sawIDs     []string
	// allIDs accumulates across calls; sawIDs is the last call only.
	allIDs []string
}

func (r *semanticLogReader) GetLogsByIDs(ctx context.Context, ids []string) ([]logstore.Log, error) {
	r.sawContext = ctx
	r.sawIDs = append([]string(nil), ids...)
	r.allIDs = append(r.allIDs, ids...)
	result := make([]logstore.Log, 0, len(ids))
	for _, id := range ids {
		if entry, ok := r.logs[id]; ok {
			result = append(result, entry)
		}
	}
	return result, nil
}

func TestSemanticSearchHydratesScopedLogsAndPreservesVectorOrder(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	visibleContent, otherContent := "checkout card declined", "billing retry complete"
	userID := "user-1"
	visibleLatency, visibleCost := 420.5, 0.0025
	reader := &semanticLogReader{logs: map[string]logstore.Log{
		"visible": {
			ID: "visible", Timestamp: now.Add(-time.Hour), Object: string(schemas.ChatCompletionRequest), Status: "error", Provider: "openai", Model: "gpt-4o", UserID: &userID, ContentSummary: visibleContent, Latency: &visibleLatency, Cost: &visibleCost,
		},
		"wrong-status": {
			ID: "wrong-status", Timestamp: now.Add(-time.Hour), Object: string(schemas.ChatCompletionRequest), Status: "success", Provider: "openai", Model: "gpt-4o", UserID: &userID, ContentSummary: otherContent,
		},
		"hidden": {
			ID: "hidden", Timestamp: now.Add(-time.Hour), Object: string(schemas.ChatCompletionRequest), Status: "error", Provider: "openai", Model: "gpt-4o", UserID: &userID, ContentHidden: true, ContentSummary: "secret",
		},
	}}
	scoreVisible, scoreWrong, scoreMissing := 0.97, 0.96, 0.95
	vectors := newFakeWarpVectorStore()
	vectors.nearest = []vectorstore.SearchResult{
		{ID: "missing", Score: &scoreMissing},
		{ID: "visible", Score: &scoreVisible},
		{ID: "wrong-status", Score: &scoreWrong},
		{ID: "hidden", Score: &scoreMissing},
	}
	executor := func(*schemas.BifrostContext, *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		vector := make([]float64, 1536)
		return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: vector}}}}, nil
	}
	searcher := NewSemanticSearcher(&recordingStore{row: validWarpConfigRow()}, vectors, executor, reader)
	start, end := now.Add(-24*time.Hour), now
	// A typed key, not a bare string: a raw string key can collide with any
	// other package writing to the same context, and the repo bans them.
	type scopeKey struct{}
	ctx := context.WithValue(context.Background(), scopeKey{}, "kept")
	minLatency, maxLatency, minCost, maxCost := 400.25, 500.75, 0.002, 0.003
	result, err := searcher.Search(ctx, "customers whose card was declined", &logstore.SearchFilters{
		StartTime: &start, EndTime: &end, Providers: []string{"openai"}, Status: []string{"error"}, UserIDs: []string{userID},
		MinLatency: &minLatency, MaxLatency: &maxLatency, MinCost: &minCost, MaxCost: &maxCost,
	}, 10)
	require.NoError(t, err)
	require.Equal(t, ctx, reader.sawContext, "candidate hydration must retain the caller's scoped context")
	require.Equal(t, []string{"missing", "visible", "wrong-status", "hidden"}, reader.sawIDs)
	require.Equal(t, 1, result.Returned)
	require.Equal(t, "visible", result.Rows[0].ID)
	require.Equal(t, scoreVisible, result.Rows[0].Score)
	require.Contains(t, result.Rows[0].Content, visibleContent)
	require.Equal(t, schemas.WarpDefaultSemanticSearchThreshold, vectors.threshold)
	require.Equal(t, int64(50), vectors.limit)
	require.Contains(t, vectors.queries, vectorstore.Query{Field: "warp_log", Operator: vectorstore.QueryOperatorEqual, Value: true})
	require.Contains(t, vectors.queries, vectorstore.Query{Field: "user_id", Operator: vectorstore.QueryOperatorEqual, Value: userID})
	require.Contains(t, vectors.queries, vectorstore.Query{Field: "latency_ms", Operator: vectorstore.QueryOperatorGreaterThanOrEqual, Value: int64(400)})
	require.Contains(t, vectors.queries, vectorstore.Query{Field: "latency_ms", Operator: vectorstore.QueryOperatorLessThanOrEqual, Value: int64(501)})
	require.Contains(t, vectors.queries, vectorstore.Query{Field: "cost_micro_usd", Operator: vectorstore.QueryOperatorGreaterThanOrEqual, Value: int64(2000)})
	require.Contains(t, vectors.queries, vectorstore.Query{Field: "cost_micro_usd", Operator: vectorstore.QueryOperatorLessThanOrEqual, Value: int64(3000)})
}

func TestSemanticSearchToolAppliesDefaultCallerScope(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	oldNow := Now
	Now = func() time.Time { return now }
	defer func() { Now = oldNow }()
	userID := "asking-user"
	reader := &semanticLogReader{logs: map[string]logstore.Log{}}
	vectors := newFakeWarpVectorStore()
	executor := func(*schemas.BifrostContext, *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		vector := make([]float64, 1536)
		return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: vector}}}}, nil
	}
	searcher := NewSemanticSearcher(&recordingStore{row: validWarpConfigRow()}, vectors, executor, reader)
	result, err := runTool(t, "semantic_search_logs", &ToolDeps{logManager: reader, semantic: searcher, scope: Scope{HasIdentity: true, UserID: userID}}, map[string]any{
		"query": "payment failures", "filters": map[string]any{},
	})
	require.NoError(t, err)
	response := result.(map[string]any)
	require.Equal(t, "self", response["scope"])
	require.Contains(t, vectors.queries, vectorstore.Query{Field: "user_id", Operator: vectorstore.QueryOperatorEqual, Value: userID})
	// The provenance footer the prompt requires needs an absolute window on
	// every result, not just query_metrics's - otherwise the model has to
	// recompute one from the current-time reference, which is exactly the
	// arithmetic the prompt separately tells it not to do.
	require.NotEmpty(t, response["window"])
}

// A filter naming two providers was dropped entirely, because the scalar helper
// only emitted a query for exactly one value. The vector store then returned
// candidates from every provider, the 100-candidate cap was spent on rows that
// would be discarded, and the post-filter threw them away afterwards - so a
// perfectly ordinary "openai or anthropic" question came back thin or empty.
func TestWarpSemanticFiltersKeepMultipleScalarValues(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	queries := semanticVectorFilters(&logstore.SearchFilters{
		StartTime: &start, EndTime: &end,
		Providers: []string{"openai", "anthropic"},
		Status:    []string{"error"},
	})

	fields := map[string]vectorstore.Query{}
	for _, query := range queries {
		fields[query.Field] = query
	}

	provider, ok := fields["provider"]
	require.True(t, ok, "a two-provider filter must reach the vector store, not be dropped")
	require.Equal(t, vectorstore.QueryOperatorContainsAny, provider.Operator)
	require.ElementsMatch(t, []string{"openai", "anthropic"}, provider.Value)

	// One value still uses equality, which is the cheaper predicate.
	status, ok := fields["status"]
	require.True(t, ok)
	require.Equal(t, vectorstore.QueryOperatorEqual, status.Operator)
	require.Equal(t, "error", status.Value)
}

// A row with no recorded latency or cost must not pass a bound on it.
//
// derefFloat turns a nil metric into 0, so a max-only bound admitted every row
// that never recorded one - "requests under $0.01" quietly included requests
// whose cost was never measured. The logstore's own range filters exclude NULL,
// so semantic search was answering a different question from the same filter
// expressed through query_logs.
func TestWarpSemanticFiltersRejectMissingMetrics(t *testing.T) {
	bound := func(v float64) *float64 { return &v }
	missing := &logstore.Log{Timestamp: time.Now(), Provider: "openai", Status: "success"}
	present := &logstore.Log{
		Timestamp: time.Now(), Provider: "openai", Status: "success",
		Latency: bound(12), Cost: bound(0.002),
	}

	for name, filters := range map[string]*logstore.SearchFilters{
		"max cost":    {MaxCost: bound(0.01)},
		"min cost":    {MinCost: bound(0)},
		"max latency": {MaxLatency: bound(100)},
		"min latency": {MinLatency: bound(0)},
	} {
		require.False(t, matchesSemanticFilters(missing, filters),
			"%s: a row that never recorded the metric must not satisfy a bound on it", name)
		require.True(t, matchesSemanticFilters(present, filters), name)
	}

	// With no bound on a metric, a row missing it is still perfectly valid.
	require.True(t, matchesSemanticFilters(missing, &logstore.SearchFilters{Providers: []string{"openai"}}))
}

func TestWarpSemanticRefillsCandidatesPastFilteredRows(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	userID := "user-1"
	reader := &semanticLogReader{logs: map[string]logstore.Log{}}
	vectors := newFakeWarpVectorStore()
	// Twenty-five hidden rows outrank the one readable match. The first
	// candidate page is entirely spent on rows the post-filter discards.
	score := 0.99
	for index := range 25 {
		id := fmt.Sprintf("hidden-%02d", index)
		reader.logs[id] = logstore.Log{
			ID: id, Timestamp: now.Add(-time.Hour), Object: string(schemas.ChatCompletionRequest),
			Status: "success", Provider: "openai", Model: "gpt-4o", UserID: &userID,
			ContentHidden: true, ContentSummary: "secret",
		}
		vectors.nearest = append(vectors.nearest, vectorstore.SearchResult{ID: id, Score: &score})
	}
	reader.logs["keep"] = logstore.Log{
		ID: "keep", Timestamp: now.Add(-time.Hour), Object: string(schemas.ChatCompletionRequest),
		Status: "success", Provider: "openai", Model: "gpt-4o", UserID: &userID,
		ContentSummary: "checkout card declined",
	}
	lowest := 0.90
	vectors.nearest = append(vectors.nearest, vectorstore.SearchResult{ID: "keep", Score: &lowest})

	searcher := NewSemanticSearcher(&recordingStore{row: validWarpConfigRow()}, vectors, func(*schemas.BifrostContext, *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: make([]float64, 1536)}}}}, nil
	}, reader)
	result, err := searcher.Search(context.Background(), "card declined", nil, 1)
	require.NoError(t, err)
	require.Equal(t, 1, result.Returned,
		"a readable match below the first candidate page must still be found: the cap is a vector-store page size, not the answer")
	require.Equal(t, "keep", result.Rows[0].ID)
	require.Equal(t, []int64{5, 10, 20, 40}, vectors.limits,
		"each refill must widen the candidate page instead of re-asking for the same one")
	require.Len(t, reader.allIDs, 26,
		"rows already hydrated must not be fetched again on a refill")
	seen := make(map[string]bool, len(reader.allIDs))
	for _, id := range reader.allIDs {
		require.False(t, seen[id], "log %s was hydrated twice", id)
		seen[id] = true
	}
}

// The store-side prefilter must match the way the post-filter matches.
//
// matchesString compares with EqualFold, but the vector store's equality is
// exact - so "OpenAI" as a filter excluded an indexed "openai" before
// hydration ever ran, and the same question answered through query_logs found
// the rows this one silently dropped. Both ends normalize to lower case:
// buildLogIndexItem stores the scalar fields lowered, and the filter values
// are lowered to meet them.
func TestWarpSemanticScalarFiltersAreCaseInsensitive(t *testing.T) {
	queries := semanticVectorFilters(&logstore.SearchFilters{
		Providers: []string{"OpenAI"},
		Models:    []string{"GPT-5.5", "Claude-Sonnet-5"},
	})
	fields := map[string]vectorstore.Query{}
	for _, query := range queries {
		fields[query.Field] = query
	}
	require.Equal(t, "openai", fields["provider"].Value)
	require.ElementsMatch(t, []string{"gpt-5.5", "claude-sonnet-5"}, fields["model"].Value)

	// Status stays canonical lowercase - terminalWarpLogStatus matches it
	// exactly - but provider and model casing comes from provider config and
	// request bodies, so those are the fields that need lowering on the way in.
	item, ok := buildLogIndexItem(&logstore.Log{
		ID: "log-case", Object: string(schemas.ChatCompletionRequest), Status: "success",
		Provider: "OpenAI", Model: "GPT-5.5", ContentSummary: "hello",
	})
	require.True(t, ok)
	require.Equal(t, "openai", item.metadata["provider"])
	require.Equal(t, "gpt-5.5", item.metadata["model"])
}

// The query becomes an embedding request, so it needs a ceiling: a tool
// argument has no natural bound, and an unbounded one is provider capacity and
// usage budget spent on a single call. Runes, not bytes, to match the schema's
// advertised maxLength.
func TestWarpSemanticSearchBoundsQueryLength(t *testing.T) {
	reader := &semanticLogReader{logs: map[string]logstore.Log{}}
	searcher := NewSemanticSearcher(&recordingStore{row: validWarpConfigRow()}, newFakeWarpVectorStore(), func(*schemas.BifrostContext, *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		t.Fatal("an oversized query must be refused before the embedding request is paid for")
		return nil, nil
	}, reader)

	_, err := searcher.Search(context.Background(), strings.Repeat("é", MaxSemanticQueryChars+1), &logstore.SearchFilters{}, 5)
	require.ErrorContains(t, err, fmt.Sprintf("%d characters", MaxSemanticQueryChars+1))
}

// An empty semantic result used to be four bare fields, and the model read it as
// "search is useless here" and went off counting and listing logs instead. The
// hint says what happened (nothing scored above the threshold) and what the
// legitimate next moves are, so a meaning question stays a meaning question.
func TestSemanticSearchToolHintsWhenNothingMatches(t *testing.T) {
	reader := &semanticLogReader{logs: map[string]logstore.Log{}}
	executor := func(*schemas.BifrostContext, *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: make([]float64, 1536)}}}}, nil
	}
	searcher := NewSemanticSearcher(&recordingStore{row: validWarpConfigRow()}, newFakeWarpVectorStore(), executor, reader)
	result, err := runTool(t, "semantic_search_logs", &ToolDeps{logManager: reader, semantic: searcher, scope: Scope{}}, map[string]any{
		"query": "refund requests", "filters": map[string]any{},
	})
	require.NoError(t, err)
	response := result.(map[string]any)
	require.Equal(t, 0, response["returned"])
	hint, _ := response["hint"].(string)
	require.Contains(t, hint, "threshold")
	require.Contains(t, hint, "Do not fall back to count_logs or query_logs")
}

// The default threshold is a Weaviate certainty, (1 + cosine) / 2. At 0.80 it
// cut off real matches: with text-embedding-3-small, "Has anyone had trouble
// resetting their account password recently?" scored 0.738 against a logged
// "I can't reset my account password, the reset email never arrives", so the
// search came back empty and Warp reported that nobody had asked. Question-to-
// conversation matches sit well below near-duplicate scores, and the default
// has to admit them.
func TestWarpDefaultSemanticThresholdAdmitsAQuestionToConversationMatch(t *testing.T) {
	const measuredMatch = 0.738
	require.LessOrEqual(t, (&schemas.WarpConfig{}).EffectiveSemanticSearchThreshold(), measuredMatch)
	require.Equal(t, 0.70, schemas.WarpDefaultSemanticSearchThreshold)
}
