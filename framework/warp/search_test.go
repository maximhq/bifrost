package warp

import (
	"context"
	"fmt"
	"strings"
	"sync"
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
	result, err := searcher.Search(ctx, []string{"customers whose card was declined"}, &logstore.SearchFilters{
		StartTime: &start, EndTime: &end, Providers: []string{"openai"}, Status: []string{"error"}, UserIDs: []string{userID},
		MinLatency: &minLatency, MaxLatency: &maxLatency, MinCost: &minCost, MaxCost: &maxCost,
	}, 10)
	require.NoError(t, err)
	require.Equal(t, ctx, reader.sawContext, "candidate hydration must retain the caller's scoped context")
	require.Equal(t, []string{"missing", "visible", "wrong-status", "hidden"}, reader.sawIDs)
	require.Equal(t, 1, result.Returned)
	require.Equal(t, "visible", result.Rows[0].ID)
	require.InDelta(t, (1+scoreVisible)/2, result.Rows[0].Score, 1e-9)
	require.Contains(t, result.Rows[0].Content, visibleContent)
	require.Equal(t, 2*schemas.WarpDefaultSemanticSearchThreshold-1, vectors.threshold)
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
		"queries": []any{"payment failures"}, "filters": map[string]any{},
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
	result, err := searcher.Search(context.Background(), []string{"card declined"}, nil, 1)
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

	_, err := searcher.Search(context.Background(), []string{strings.Repeat("é", MaxSemanticQueryChars+1)}, &logstore.SearchFilters{}, 5)
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
		"queries": []any{"refund requests"}, "filters": map[string]any{},
	})
	require.NoError(t, err)
	response := result.(map[string]any)
	require.Equal(t, 0, response["returned"])
	hint, _ := response["hint"].(string)
	require.Contains(t, hint, "threshold")
	require.Contains(t, hint, "do not fall back to count_logs or query_logs")
	// A survey question ("what kinds of tasks was Rohan doing") has no meaning
	// to embed, so an empty result is the expected outcome of the wrong first
	// step, not a finding. Forbidding the fallback outright here had Warp tell
	// a person with 42k requests in the window that nothing matched.
	require.Contains(t, hint, "survey question")
	require.Contains(t, hint, "query_logs using include_content and limit 25")
	require.Contains(t, hint, "not representative of the entire traffic")
}

// The threshold is a 0-1 similarity, (1 + cosine) / 2, on a cosine-scored index.
// It was handed to the store as is, and only Weaviate reads a threshold as that
// certainty - Chromem, Qdrant, Pinecone and Redis compare it to raw cosine - so
// the old 0.70 meant cosine 0.40 on one store and 0.70 on the others.
//
// Measured with text-embedding-3-small over Warp's own index text: conversations
// that really are about a topic score cosine 0.15 to 0.47 against a topic query,
// a similarity of 0.575 to 0.735, and unrelated ones score inside that range
// too, so no threshold separates them. 0.70 on a cosine store returned nothing
// for every topic question. The default has to admit every real match; the
// ranking, the limit and the model reading each row decide what is relevant.
func TestWarpDefaultSemanticThresholdAdmitsEveryMeasuredTopicMatch(t *testing.T) {
	const lowestMeasuredMatch = (1 + 0.15) / 2
	require.LessOrEqual(t, (&schemas.WarpConfig{}).EffectiveSemanticSearchThreshold(), lowestMeasuredMatch)
	require.Equal(t, 0.5, schemas.WarpDefaultSemanticSearchThreshold)
}

// On a store that scores raw cosine the similarity threshold goes down as
// cosine, and the scores come back as similarity, so the model and the merge
// read one scale whichever store is configured.
func TestWarpSemanticSearchConvertsACosineStoreToSimilarity(t *testing.T) {
	reader := &semanticLogReader{logs: map[string]logstore.Log{"trip": semanticConversation("trip")}}
	vectors := newFakeWarpVectorStore()
	vectors.nearest = scoredCandidates("trip", 0.4)
	searcher := NewSemanticSearcher(&recordingStore{row: validWarpConfigRow()}, vectors, zeroEmbeddingExecutor, reader)

	result, err := searcher.Search(context.Background(), []string{"users planning a trip"}, nil, 5)
	require.NoError(t, err)
	require.Equal(t, 0.0, vectors.threshold, "a 0.5 similarity is cosine 0 on a cosine store")
	require.Equal(t, schemas.WarpDefaultSemanticSearchThreshold, result.Threshold)
	require.Len(t, result.Rows, 1)
	require.InDelta(t, 0.7, result.Rows[0].Score, 1e-9, "cosine 0.4 is similarity 0.7")
}

// Weaviate already filters and scores as certainty, which is the similarity
// scale, so it is passed through untouched.
func TestWarpSemanticSearchPassesWeaviateCertaintyThrough(t *testing.T) {
	require.True(t, scoresAsCertainty(&vectorstore.WeaviateStore{}))
	require.False(t, scoresAsCertainty(newFakeWarpVectorStore()))
	require.Equal(t, 0.5, storeThreshold(0.5, true))
	require.Equal(t, 0.73, similarityFromScore(0.73, true))
}

// Each phrasing is embedded and searched, and a log keeps the best score any
// phrasing gave it: a conversation only one phrasing reaches is still found,
// and one several reach is ranked by its closest.
func TestWarpSemanticSearchMergesEveryPhrasing(t *testing.T) {
	reader := &semanticLogReader{logs: map[string]logstore.Log{
		"itinerary": semanticConversation("itinerary"),
		"packing":   semanticConversation("packing"),
	}}
	vectors := newFakeWarpVectorStore()
	vectors.respondVector = func(vector []float32, _ []vectorstore.Query) []vectorstore.SearchResult {
		if vector[0] == 1 {
			return scoredCandidates("itinerary", 0.4, "packing", 0.1)
		}
		return scoredCandidates("packing", 0.3)
	}
	executor := phrasingExecutor(map[string]int{"travel itinerary for a city": 0, "packing for a trip": 1})
	searcher := NewSemanticSearcher(&recordingStore{row: validWarpConfigRow()}, vectors, executor, reader)

	result, err := searcher.Search(context.Background(), []string{"travel itinerary for a city", "packing for a trip"}, nil, 10)
	require.NoError(t, err)
	require.Len(t, result.Rows, 2)
	require.Equal(t, "itinerary", result.Rows[0].ID)
	require.InDelta(t, 0.7, result.Rows[0].Score, 1e-9)
	require.Equal(t, "packing", result.Rows[1].ID)
	require.InDelta(t, 0.65, result.Rows[1].Score, 1e-9, "packing keeps the closer of its two scores")
}

// The phrasings are embedded together, not one after another: each is a
// provider round trip, and a five-phrasing search should cost one of them in
// wall time.
func TestWarpSemanticSearchEmbedsPhrasingsInParallel(t *testing.T) {
	reader := &semanticLogReader{logs: map[string]logstore.Log{}}
	var arrived sync.WaitGroup
	arrived.Add(2)
	executor := func(ctx *schemas.BifrostContext, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		arrived.Done()
		together := make(chan struct{})
		go func() { arrived.Wait(); close(together) }()
		select {
		case <-together:
		case <-time.After(2 * time.Second):
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "the phrasings were embedded one at a time"}}
		}
		return zeroEmbeddingExecutor(ctx, request)
	}
	searcher := NewSemanticSearcher(&recordingStore{row: validWarpConfigRow()}, newFakeWarpVectorStore(), executor, reader)

	_, err := searcher.Search(context.Background(), []string{"users planning a trip", "travel itinerary"}, nil, 5)
	require.NoError(t, err)
}

func TestWarpSemanticSearchValidatesPhrasings(t *testing.T) {
	searcher := NewSemanticSearcher(&recordingStore{row: validWarpConfigRow()}, newFakeWarpVectorStore(), zeroEmbeddingExecutor, &semanticLogReader{})
	_, err := searcher.Search(context.Background(), []string{" ", ""}, nil, 5)
	require.ErrorContains(t, err, "at least one query")
	_, err = searcher.Search(context.Background(), []string{"a", "b", "c", "d", "e", "f"}, nil, 5)
	require.ErrorContains(t, err, "at most 5")
	// Repeats are dropped rather than refused, so they cost nothing and do not
	// count against the limit.
	_, err = searcher.Search(context.Background(), []string{"a", "b", "c", "d", "e", "a"}, nil, 5)
	require.NoError(t, err)
}

func semanticConversation(id string) logstore.Log {
	return logstore.Log{ID: id, Timestamp: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC), Object: string(schemas.ChatCompletionRequest), Status: "success", Provider: "openai", Model: "gpt-4o", ContentSummary: id}
}

func zeroEmbeddingExecutor(*schemas.BifrostContext, *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: make([]float64, 1536)}}}}, nil
}

// phrasingExecutor embeds each known phrasing as a one-hot vector on its index,
// so a fake store can tell which phrasing it was asked with.
func phrasingExecutor(index map[string]int) EmbeddingExecutor {
	return func(_ *schemas.BifrostContext, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		vector := make([]float64, 1536)
		text := *request.Input[0].Content[0].Text
		vector[index[text]] = 1
		return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: vector}}}}, nil
	}
}

// Visibility is an OR across fields and vector filters only AND, so each
// dimension is its own filter set - the question's filters, plus one of the
// caller's sets.
func TestWarpSemanticPrefiltersFanOutByVisibilityDimension(t *testing.T) {
	base := vectorstore.Query{Field: "warp_log", Operator: vectorstore.QueryOperatorEqual, Value: true}
	provider := vectorstore.Query{Field: "provider", Operator: vectorstore.QueryOperatorEqual, Value: "openai"}
	filters := &logstore.SearchFilters{Providers: []string{"openai"}}

	t.Run("no visibility is the question's own filter", func(t *testing.T) {
		require.Equal(t, [][]vectorstore.Query{{base, provider}}, semanticPrefilters(filters, nil))
	})

	t.Run("one filter set per populated dimension", func(t *testing.T) {
		prefilters := semanticPrefilters(filters, &LogVisibility{
			// Mixed case: the index stores scalar ids lowered.
			UserIDs:       []string{"User-7"},
			VirtualKeyIDs: []string{"vk-1", "vk-2"},
			TeamIDs:       []string{"team-1"},
		})
		require.Equal(t, [][]vectorstore.Query{
			{base, provider, {Field: "user_id", Operator: vectorstore.QueryOperatorEqual, Value: "user-7"}},
			{base, provider, {Field: "virtual_key_id", Operator: vectorstore.QueryOperatorContainsAny, Value: []string{"vk-1", "vk-2"}}},
			{base, provider, {Field: "team_ids", Operator: vectorstore.QueryOperatorContainsAny, Value: []string{"team-1"}}},
		}, prefilters)
	})

	// Nothing to narrow by is not "match nothing": the store still decides, and
	// a prefilter that returned no candidates would be an access decision.
	t.Run("empty visibility searches unfiltered", func(t *testing.T) {
		require.Equal(t, [][]vectorstore.Query{{base, provider}}, semanticPrefilters(filters, &LogVisibility{}))
	})

	t.Run("a set too large to send searches unfiltered", func(t *testing.T) {
		members := make([]string, warpVisibilityPrefilterMaxIDs+1)
		for index := range members {
			members[index] = fmt.Sprintf("user-%d", index)
		}
		require.Equal(t, [][]vectorstore.Query{{base, provider}},
			semanticPrefilters(filters, &LogVisibility{UserIDs: members, TeamIDs: []string{"team-1"}}))
	})

	// The default scope names the caller, which is already inside their own
	// visibility: one query, not five returning the same rows.
	t.Run("a question inside a visibility set needs no fan-out", func(t *testing.T) {
		named := &logstore.SearchFilters{UserIDs: []string{"user-7"}}
		prefilters := semanticPrefilters(named, &LogVisibility{UserIDs: []string{"USER-7", "user-8"}, TeamIDs: []string{"team-1"}})
		require.Equal(t, [][]vectorstore.Query{{base, {Field: "user_id", Operator: vectorstore.QueryOperatorEqual, Value: "user-7"}}}, prefilters)

		// One named id outside the set and the question may reach rows the
		// caller cannot see, so the fan-out stays.
		outside := &logstore.SearchFilters{UserIDs: []string{"user-7", "user-9"}}
		require.Len(t, semanticPrefilters(outside, &LogVisibility{UserIDs: []string{"user-7"}, TeamIDs: []string{"team-1"}}), 2)
	})
}

func scoredCandidates(pairs ...any) []vectorstore.SearchResult {
	out := make([]vectorstore.SearchResult, 0, len(pairs)/2)
	for index := 0; index < len(pairs); index += 2 {
		score := pairs[index+1].(float64)
		out = append(out, vectorstore.SearchResult{ID: pairs[index].(string), Score: &score})
	}
	return out
}

func TestWarpSemanticMergePages(t *testing.T) {
	t.Run("one page keeps the store's order", func(t *testing.T) {
		scores := map[string]float64{}
		ids, exhausted := mergeSemanticPages([][]vectorstore.SearchResult{scoredCandidates("b", 0.8, "a", 0.9)}, 5, scores, false)
		require.Equal(t, []string{"b", "a"}, ids)
		require.True(t, exhausted)
		require.Equal(t, 0.9, scores["a"])
	})

	t.Run("pages merge by score without duplicates", func(t *testing.T) {
		scores := map[string]float64{}
		ids, exhausted := mergeSemanticPages([][]vectorstore.SearchResult{
			scoredCandidates("a", 0.9, "c", 0.7),
			scoredCandidates("b", 0.8, "a", 0.9),
		}, 5, scores, false)
		require.Equal(t, []string{"a", "b", "c"}, ids)
		require.True(t, exhausted, "no page was full")
	})

	// A full page stopped at 0.80; the row it would have returned next may be
	// 0.79, which outranks the other page's 0.60. Returning the 0.60 now would
	// put it ahead of a better match the wider round is about to find.
	t.Run("holds back candidates a full page could still outrank", func(t *testing.T) {
		pages := [][]vectorstore.SearchResult{
			scoredCandidates("a1", 0.95, "a2", 0.80),
			scoredCandidates("b1", 0.90),
			scoredCandidates("c1", 0.60),
		}
		ids, exhausted := mergeSemanticPages(pages, 2, map[string]float64{}, false)
		require.Equal(t, []string{"a1", "b1", "a2"}, ids)
		require.False(t, exhausted)

		// With no wider round to come, everything found is used.
		ids, _ = mergeSemanticPages(pages, 2, map[string]float64{}, true)
		require.Equal(t, []string{"a1", "b1", "a2", "c1"}, ids)
	})

	// Hydration accepts at most the candidate limit, and silently drops the
	// rest - which would read as "not visible".
	t.Run("capped at the candidate limit, best first", func(t *testing.T) {
		var first, second []vectorstore.SearchResult
		for index := range warpSemanticCandidateLimit {
			low, high := 0.5-float64(index)/1000, 0.9-float64(index)/1000
			first = append(first, vectorstore.SearchResult{ID: fmt.Sprintf("low-%d", index), Score: &low})
			second = append(second, vectorstore.SearchResult{ID: fmt.Sprintf("high-%d", index), Score: &high})
		}
		ids, _ := mergeSemanticPages([][]vectorstore.SearchResult{first, second}, warpSemanticCandidateLimit, map[string]float64{}, true)
		require.Len(t, ids, warpSemanticCandidateLimit)
		require.Equal(t, "high-0", ids[0])
		require.Equal(t, fmt.Sprintf("high-%d", warpSemanticCandidateLimit-1), ids[len(ids)-1])
	})
}

// The failure this exists for: a caller who may see a sliver of a large
// deployment. The index's nearest hundred rows are all other people's, the
// store discards every one after hydration, and the caller's own match - a
// good one, just not in the deployment's top hundred - is never reached.
func TestWarpSemanticSearchReachesARestrictedCallersRows(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	mine, teamKey := "user-7", "vk-team"
	// The reader stands in for the scoped store: it only holds what the caller
	// may read, so every other candidate hydrates as absent.
	reader := &semanticLogReader{logs: map[string]logstore.Log{
		"mine": {
			ID: "mine", Timestamp: now.Add(-time.Hour), Object: string(schemas.ChatCompletionRequest),
			Status: "success", Provider: "openai", Model: "gpt-4o", UserID: &mine, ContentSummary: "card declined at checkout",
		},
		"team-key": {
			ID: "team-key", Timestamp: now.Add(-time.Hour), Object: string(schemas.ChatCompletionRequest),
			Status: "success", Provider: "openai", Model: "gpt-4o", VirtualKeyID: &teamKey, ContentSummary: "declined card on renewal",
		},
	}}
	var others []vectorstore.SearchResult
	for index := range 300 {
		score := 0.99 - float64(index)/10000
		others = append(others, vectorstore.SearchResult{ID: fmt.Sprintf("other-%03d", index), Score: &score})
	}
	hasQuery := func(queries []vectorstore.Query, field string) bool {
		for _, query := range queries {
			if query.Field == field {
				return true
			}
		}
		return false
	}
	newVectors := func() *fakeWarpVectorStore {
		vectors := newFakeWarpVectorStore()
		vectors.respond = func(queries []vectorstore.Query) []vectorstore.SearchResult {
			switch {
			case hasQuery(queries, "user_id"):
				return scoredCandidates("mine", 0.71)
			case hasQuery(queries, "virtual_key_id"):
				return scoredCandidates("team-key", 0.74)
			case hasQuery(queries, "team_ids"):
				return nil
			}
			return others
		}
		return vectors
	}
	executor := func(*schemas.BifrostContext, *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: make([]float64, 1536)}}}}, nil
	}

	t.Run("unfiltered, the cap is spent on rows the caller cannot read", func(t *testing.T) {
		searcher := NewSemanticSearcher(&recordingStore{row: validWarpConfigRow()}, newVectors(), executor, reader)
		result, err := searcher.Search(context.Background(), []string{"card declined"}, nil, 5)
		require.NoError(t, err)
		require.Zero(t, result.Returned)
	})

	t.Run("with the caller's visibility, their rows are found in score order", func(t *testing.T) {
		vectors := newVectors()
		searcher := NewSemanticSearcher(&recordingStore{row: validWarpConfigRow()}, vectors, executor, reader)
		visible := &LogVisibility{UserIDs: []string{mine}, VirtualKeyIDs: []string{teamKey}, TeamIDs: []string{"team-1"}}
		result, err := searcher.SearchVisible(context.Background(), []string{"card declined"}, nil, visible, 5)
		require.NoError(t, err)
		require.Equal(t, 2, result.Returned)
		require.Equal(t, "team-key", result.Rows[0].ID)
		require.Equal(t, "mine", result.Rows[1].ID)
		require.Len(t, vectors.calls, 3, "one query per populated dimension, and no wider round once all are exhausted")
		for _, queries := range vectors.calls {
			require.Contains(t, queries, vectorstore.Query{Field: "warp_log", Operator: vectorstore.QueryOperatorEqual, Value: true})
		}
	})

	// The prefilter chooses candidates; it is not the access check. A row the
	// index offers under the caller's filter is still dropped when the store
	// will not hand it over.
	t.Run("hydration still decides", func(t *testing.T) {
		vectors := newFakeWarpVectorStore()
		vectors.respond = func([]vectorstore.Query) []vectorstore.SearchResult {
			return scoredCandidates("not-readable", 0.95, "mine", 0.71)
		}
		searcher := NewSemanticSearcher(&recordingStore{row: validWarpConfigRow()}, vectors, executor, reader)
		result, err := searcher.SearchVisible(context.Background(), []string{"card declined"}, nil, &LogVisibility{UserIDs: []string{mine}}, 5)
		require.NoError(t, err)
		require.Equal(t, 1, result.Returned)
		require.Equal(t, "mine", result.Rows[0].ID)
	})
}

// The tool hands the searcher the visibility Warp resolved for the caller.
func TestSemanticSearchToolPrefiltersByCallerVisibility(t *testing.T) {
	reader := &semanticLogReader{logs: map[string]logstore.Log{}}
	vectors := newFakeWarpVectorStore()
	executor := func(*schemas.BifrostContext, *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: make([]float64, 1536)}}}}, nil
	}
	searcher := NewSemanticSearcher(&recordingStore{row: validWarpConfigRow()}, vectors, executor, reader)
	scope := Scope{HasIdentity: true, UserID: "user-7", Visible: &LogVisibility{UserIDs: []string{"user-7"}, TeamIDs: []string{"team-1"}}}
	// scope "all": the one question a restricted caller's default does not
	// already narrow to their own user id.
	_, err := runTool(t, "semantic_search_logs", &ToolDeps{logManager: reader, semantic: searcher, scope: scope}, map[string]any{
		"queries": []any{"payment failures"}, "filters": map[string]any{"scope": "all"},
	})
	require.NoError(t, err)
	require.Len(t, vectors.calls, 2)
	fields := map[string]bool{}
	for _, queries := range vectors.calls {
		for _, query := range queries {
			fields[query.Field] = true
		}
	}
	require.True(t, fields["user_id"] && fields["team_ids"])
}

// Scores cannot tell a topic match from an unrelated row, so the model is told
// to judge each row by its content. Without it Warp counted every returned row
// as a match, or - with nothing above the old threshold - said a topic with
// nineteen conversations had none.
func TestWarpSemanticSearchToldToJudgeRowsByContent(t *testing.T) {
	tool := semanticSearchLogsTool()
	require.Contains(t, tool.description, "Read each row's content and keep only the rows actually about what was asked")
	require.Contains(t, tool.description, "several phrasings")
	require.Contains(t, tool.schemaJSON, `"queries"`)
	require.Contains(t, SemanticSearchGuidance, "drop every row whose content is not about the topic")
}
