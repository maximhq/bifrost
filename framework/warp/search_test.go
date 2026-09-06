package warp

import (
	"context"
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
}

func (r *semanticLogReader) GetLogsByIDs(ctx context.Context, ids []string) ([]logstore.Log, error) {
	r.sawContext = ctx
	r.sawIDs = append([]string(nil), ids...)
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
	ctx := context.WithValue(context.Background(), "scope-marker", "kept")
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
	require.Equal(t, 0.8, vectors.threshold)
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
	require.Contains(t, response["scope"], "person asking")
	require.Contains(t, vectors.queries, vectorstore.Query{Field: "user_id", Operator: vectorstore.QueryOperatorEqual, Value: userID})
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
