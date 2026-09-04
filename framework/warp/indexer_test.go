package warp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/stretchr/testify/require"
)

type fakeWarpVectorStore struct {
	mu         sync.Mutex
	namespace  string
	dimension  int
	adds       map[string]map[string]interface{}
	embeddings map[string][]float32
}

func newFakeWarpVectorStore() *fakeWarpVectorStore {
	return &fakeWarpVectorStore{adds: map[string]map[string]interface{}{}, embeddings: map[string][]float32{}}
}

func (f *fakeWarpVectorStore) Ping(context.Context) error { return nil }
func (f *fakeWarpVectorStore) CreateNamespace(_ context.Context, namespace string, dimension int, _ map[string]vectorstore.VectorStoreProperties) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.namespace = namespace
	f.dimension = dimension
	return nil
}
func (f *fakeWarpVectorStore) DeleteNamespace(context.Context, string) error { return nil }
func (f *fakeWarpVectorStore) ListNamespaces(context.Context, string) ([]string, error) {
	return nil, nil
}
func (f *fakeWarpVectorStore) GetChunk(context.Context, string, string) (vectorstore.SearchResult, error) {
	return vectorstore.SearchResult{}, nil
}
func (f *fakeWarpVectorStore) GetChunks(context.Context, string, []string) ([]vectorstore.SearchResult, error) {
	return nil, nil
}
func (f *fakeWarpVectorStore) GetAll(context.Context, string, []vectorstore.Query, []string, *string, int64) ([]vectorstore.SearchResult, *string, error) {
	return nil, nil, nil
}
func (f *fakeWarpVectorStore) GetNearest(context.Context, string, []float32, []vectorstore.Query, []string, float64, int64) ([]vectorstore.SearchResult, error) {
	return nil, nil
}
func (f *fakeWarpVectorStore) RequiresVectors() bool { return true }
func (f *fakeWarpVectorStore) Add(_ context.Context, _ string, id string, embedding []float32, metadata map[string]interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adds[id] = metadata
	f.embeddings[id] = embedding
	return nil
}
func (f *fakeWarpVectorStore) Delete(context.Context, string, string) error { return nil }
func (f *fakeWarpVectorStore) DeleteAll(context.Context, string, []vectorstore.Query) ([]vectorstore.DeleteResult, error) {
	return nil, nil
}
func (f *fakeWarpVectorStore) Close(context.Context, string) error { return nil }

func TestWarpIndexerEmbedsAndStoresVisibleConversation(t *testing.T) {
	row := validWarpConfigRow()
	row.EmbeddingAPIKeyID = "embedding-key"
	vectors := newFakeWarpVectorStore()
	var embedded string
	executor := func(ctx *schemas.BifrostContext, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		require.Equal(t, true, ctx.Value(schemas.BifrostContextKeySkipPluginPipeline))
		require.Equal(t, "embedding-key", ctx.Value(schemas.BifrostContextKeyAPIKeyID))
		embedded = *request.Input.Text
		vector := make([]float64, row.EmbeddingDimension)
		return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: vector}}}}, nil
	}
	indexer := NewLogIndexer(&recordingStore{row: row}, vectors, executor, nil)
	defer indexer.Close()
	user, assistant := "find payment failures", "The card was declined"
	latency, cost := 432.6, 0.001234
	entry := &logstore.Log{
		ID: "log-1", Timestamp: time.Unix(100, 0), Object: string(schemas.ChatCompletionRequest), Status: "success",
		Provider: "openai", Model: "gpt-4o", Latency: &latency, Cost: &cost, PromptTokens: 12, CompletionTokens: 8, TotalTokens: 20,
		InputHistoryParsed:  []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &user}}},
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &assistant}},
	}
	outcome, err := indexer.Index(context.Background(), entry)
	require.NoError(t, err)
	require.Equal(t, IndexOutcomeIndexed, outcome)
	require.Equal(t, "user: find payment failures\nassistant: The card was declined", embedded)
	require.Equal(t, schemas.WarpDefaultLogVectorStoreNamespace, vectors.namespace)
	require.Equal(t, 1536, vectors.dimension)
	require.Equal(t, "log-1", vectors.adds["log-1"]["log_id"])
	require.Equal(t, "success", vectors.adds["log-1"]["status"])
	require.Equal(t, int64(433), vectors.adds["log-1"]["latency_ms"])
	require.Equal(t, int64(1234), vectors.adds["log-1"]["cost_micro_usd"])
	require.Equal(t, 12, vectors.adds["log-1"]["prompt_tokens"])
	require.Equal(t, 8, vectors.adds["log-1"]["completion_tokens"])
	require.Equal(t, 20, vectors.adds["log-1"]["total_tokens"])
	require.NotContains(t, vectors.adds["log-1"], "content")
}

func TestWarpIndexerSkipsPrivateProcessingAndSelfTraffic(t *testing.T) {
	indexer := NewLogIndexer(&recordingStore{row: validWarpConfigRow()}, newFakeWarpVectorStore(), func(*schemas.BifrostContext, *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		t.Fatal("embedding should not run")
		return nil, nil
	}, nil)
	defer indexer.Close()
	text, warpApp := "secret", "Warp"
	for _, entry := range []*logstore.Log{
		{ID: "hidden", Object: string(schemas.ChatCompletionRequest), Status: "success", ContentHidden: true, ContentSummary: text},
		{ID: "processing", Object: string(schemas.ChatCompletionRequest), Status: "processing", ContentSummary: text},
		{ID: "warp", Object: string(schemas.ResponsesRequest), Status: "success", App: &warpApp, ContentSummary: text},
	} {
		outcome, err := indexer.Index(context.Background(), entry)
		require.NoError(t, err)
		require.Equal(t, IndexOutcomeSkipped, outcome)
	}
}

func TestWarpIndexerRejectsWrongEmbeddingDimension(t *testing.T) {
	indexer := NewLogIndexer(&recordingStore{row: validWarpConfigRow()}, newFakeWarpVectorStore(), func(*schemas.BifrostContext, *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 2}}}}}, nil
	}, nil)
	defer indexer.Close()
	_, err := indexer.Index(context.Background(), &logstore.Log{ID: "bad", Object: string(schemas.ChatCompletionRequest), Status: "success", ContentSummary: "hello"})
	require.ErrorContains(t, err, "dimension mismatch")
}
