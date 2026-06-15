package semanticcache

import (
	"context"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// TestCacheTypeDirectOnly tests that CacheTypeKey set to "direct" only performs direct hash matching
func TestCacheTypeDirectOnly(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// First, cache a response using CacheTypeDirect so it is stored under the deterministic ID
	ctx1 := CreateContextWithCacheKeyAndType(t, "test-cache-type-direct", CacheTypeDirect)
	testRequest := CreateBasicChatRequest("What is Bifrost?", 0.7, 50)

	t.Log("Making first request to populate cache...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache(setup.Plugin)

	// Now test with CacheTypeKey set to direct only
	ctx2 := CreateContextWithCacheKeyAndType(t, "test-cache-type-direct", CacheTypeDirect)

	t.Log("Making second request with CacheTypeKey=direct...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err2 != nil {
		t.Fatalf("Second request failed: %v", err2.Error.Message)
	}

	// Should be a cache hit from direct search
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct")

	t.Log("✅ CacheTypeKey=direct correctly performs only direct hash matching")
}

// TestCacheTypeSemanticOnly tests that CacheTypeKey set to "semantic" only performs semantic search
func TestCacheTypeSemanticOnly(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// First, cache a response using normal behavior
	ctx1 := CreateContextWithCacheKey(t, "test-cache-type-semantic")
	testRequest := CreateBasicChatRequest("Explain machine learning concepts", 0.7, 50)

	t.Log("Making first request to populate cache...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache(setup.Plugin)

	// Test with slightly different wording that should match semantically but not directly
	similarRequest := CreateBasicChatRequest("Can you explain concepts in machine learning", 0.7, 50)

	// Try with semantic-only search
	ctx2 := CreateContextWithCacheKeyAndType(t, "test-cache-type-semantic", CacheTypeSemantic)

	t.Log("Making second request with similar content and CacheTypeKey=semantic...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, similarRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}

	// This might be a cache hit if semantic similarity is high enough.
	// Hit/miss is similarity-dependent, but CacheDebug must be stamped either
	// way — semantic search ran. This catches a regression where the stamping
	// stops without making the test flake on similarity scores.
	if response2.ExtraFields.CacheDebug == nil {
		t.Fatal("expected CacheDebug to be stamped on the response (semantic search should have run)")
	}
	if response2.ExtraFields.CacheDebug.CacheHit {
		AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "semantic")
		t.Log("✅ CacheTypeKey=semantic correctly found semantic match")
	} else {
		t.Log("ℹ️  No semantic match found (threshold may be too high for these similar phrases)")
		AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2})
	}

	t.Log("✅ CacheTypeKey=semantic correctly performs only semantic search")
}

// TestCacheTypeDirectWithSemanticFallback tests the default behavior (both direct and semantic)
func TestCacheTypeDirectWithSemanticFallback(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Cache a response first
	ctx1 := CreateContextWithCacheKey(t, "test-cache-type-fallback")
	testRequest := CreateBasicChatRequest("Define artificial intelligence", 0.7, 50)

	t.Log("Making first request to populate cache...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache(setup.Plugin)

	// Test exact match (should hit direct cache)
	ctx2 := CreateContextWithCacheKey(t, "test-cache-type-fallback")

	t.Log("Making second identical request (should hit direct cache)...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct")

	// Test similar request (should potentially hit semantic cache)
	similarRequest := CreateBasicChatRequest("What is artificial intelligence", 0.7, 50)

	t.Log("Making third similar request (should attempt semantic match)...")
	response3, err3 := setup.Client.ChatCompletionRequest(ctx2, similarRequest)
	if err3 != nil {
		t.Fatalf("Third request failed: %v", err3)
	}

	// May or may not be a cache hit depending on semantic similarity, but
	// CacheDebug must be stamped (regression guard).
	if response3.ExtraFields.CacheDebug == nil {
		t.Fatal("expected CacheDebug to be stamped on the response")
	}
	if response3.ExtraFields.CacheDebug.CacheHit {
		AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3}, "semantic")
		t.Log("✅ Default behavior correctly found semantic match")
	} else {
		t.Log("ℹ️  No semantic match found (normal for different wording)")
		AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3})
	}

	t.Log("✅ Default behavior correctly attempts both direct and semantic search")
}

// TestCacheTypeInvalidValue tests behavior with invalid CacheTypeKey values:
// the plugin must fall back to default behavior (try both direct + semantic)
// rather than disable caching entirely.
func TestCacheTypeInvalidValue(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	testRequest := CreateBasicChatRequest("Test invalid cache type", 0.7, 50)

	// First request with invalid CacheTypeKey — must be a miss but ALSO must
	// have caused the response to be cached (fallback to default behavior).
	ctx1 := CreateContextWithCacheKey(t, "test-invalid-cache-type")
	ctx1 = ctx1.WithValue(CacheTypeKey, "invalid_type")

	t.Log("Making first request with invalid CacheTypeKey value...")
	response1, err := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err != nil {
		t.Skipf("upstream request error, skipping test: %v", err)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache(setup.Plugin)

	// Second identical request — fallback should mean the entry was written
	// the first time, so this must hit (proves the invalid value didn't
	// disable caching as a side effect).
	ctx2 := CreateContextWithCacheKey(t, "test-invalid-cache-type")
	ctx2 = ctx2.WithValue(CacheTypeKey, "invalid_type")
	t.Log("Making second identical request — must hit cache, proving fallback to default cached the first call...")
	response2, err := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	if err != nil {
		t.Fatalf("Second request failed: %v", err)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, string(CacheTypeDirect))

	t.Log("✅ Invalid CacheTypeKey value falls back to default behavior (caching works)")
}

// TestCacheTypeWithEmbeddingRequests tests CacheTypeKey behavior with embedding requests
func TestCacheTypeWithEmbeddingRequests(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	embeddingRequest := CreateEmbeddingRequest([]string{"Test embedding with cache type"})

	// Cache first request
	ctx1 := CreateContextWithCacheKey(t, "test-embedding-cache-type")
	t.Log("Making first embedding request...")
	response1, err1 := setup.Client.EmbeddingRequest(ctx1, embeddingRequest)
	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response1})

	WaitForCache(setup.Plugin)

	// Test with direct-only cache type
	ctx2 := CreateContextWithCacheKeyAndType(t, "test-embedding-cache-type", CacheTypeDirect)
	t.Log("Making second embedding request with CacheTypeKey=direct...")
	response2, err2 := setup.Client.EmbeddingRequest(ctx2, embeddingRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	AssertCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response2}, "direct")

	// Test with semantic-only cache type (should not find semantic match for embeddings)
	ctx3 := CreateContextWithCacheKeyAndType(t, "test-embedding-cache-type", CacheTypeSemantic)
	t.Log("Making third embedding request with CacheTypeKey=semantic...")
	response3, err3 := setup.Client.EmbeddingRequest(ctx3, embeddingRequest)
	if err3 != nil {
		t.Fatalf("Third request failed: %v", err3)
	}
	// Semantic search should be skipped for embedding requests
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response3})

	t.Log("✅ CacheTypeKey works correctly with embedding requests")
}

// TestCacheTypePerformanceCharacteristics tests that different cache types have expected performance
func TestCacheTypePerformanceCharacteristics(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	testRequest := CreateBasicChatRequest("Performance test for cache types", 0.7, 50)

	// Cache first request using CacheTypeDirect so it is stored under the deterministic ID
	ctx1 := CreateContextWithCacheKeyAndType(t, "test-cache-performance", CacheTypeDirect)
	t.Log("Making first request to populate cache...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache(setup.Plugin)

	// Test direct-only performance
	ctx2 := CreateContextWithCacheKeyAndType(t, "test-cache-performance", CacheTypeDirect)
	start2 := time.Now()
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	duration2 := time.Since(start2)
	if err2 != nil {
		t.Fatalf("Direct cache request failed: %v", err2)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct")

	t.Logf("Direct cache lookup took: %v", duration2)

	// Test default behavior (both direct and semantic) performance
	ctx3 := CreateContextWithCacheKey(t, "test-cache-performance")
	start3 := time.Now()
	response3, err3 := setup.Client.ChatCompletionRequest(ctx3, testRequest)
	duration3 := time.Since(start3)
	if err3 != nil {
		t.Fatalf("Default cache request failed: %v", err3)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3}, "direct")

	t.Logf("Default cache lookup took: %v", duration3)

	// Both lookups hit direct cache so both must be substantially faster than
	// a real upstream call. Compare against an upper bound rather than each
	// other (relative comparisons flake under CI load); 1s is generous and
	// still proves a cached lookup didn't silently hit the network.
	const upperBoundForCacheLookup = 1 * time.Second
	if duration2 > upperBoundForCacheLookup {
		t.Errorf("direct-only cache lookup took %v, expected < %v (provider likely called)", duration2, upperBoundForCacheLookup)
	}
	if duration3 > upperBoundForCacheLookup {
		t.Errorf("default-mode cache lookup took %v, expected < %v (provider likely called)", duration3, upperBoundForCacheLookup)
	}
	t.Log("✅ Cache type performance characteristics validated")
}

type directFastPathStore struct {
	chunks         map[string]vectorstore.SearchResult
	addIDs         []string
	getChunkCalls  int
	getAllCalls    int
	lastGetChunkID string
	lastGetAllCtx  context.Context
	getAllErr      error
}

func newDirectFastPathStore() *directFastPathStore {
	return &directFastPathStore{
		chunks: make(map[string]vectorstore.SearchResult),
	}
}

func (s *directFastPathStore) Ping(ctx context.Context) error { return nil }

func (s *directFastPathStore) CreateNamespace(ctx context.Context, namespace string, dimension int, properties map[string]vectorstore.VectorStoreProperties) error {
	return nil
}

func (s *directFastPathStore) DeleteNamespace(ctx context.Context, namespace string) error {
	return nil
}

func (s *directFastPathStore) GetChunk(ctx context.Context, namespace string, id string) (vectorstore.SearchResult, error) {
	s.getChunkCalls++
	s.lastGetChunkID = id
	result, ok := s.chunks[id]
	if !ok {
		return vectorstore.SearchResult{}, vectorstore.ErrNotFound
	}
	return result, nil
}

func (s *directFastPathStore) GetChunks(ctx context.Context, namespace string, ids []string) ([]vectorstore.SearchResult, error) {
	return nil, vectorstore.ErrNotSupported
}

func (s *directFastPathStore) GetAll(ctx context.Context, namespace string, queries []vectorstore.Query, selectFields []string, cursor *string, limit int64) ([]vectorstore.SearchResult, *string, error) {
	s.getAllCalls++
	s.lastGetAllCtx = ctx
	if s.getAllErr != nil {
		return nil, nil, s.getAllErr
	}
	return nil, nil, vectorstore.ErrNotSupported
}

func (s *directFastPathStore) GetNearest(ctx context.Context, namespace string, vector []float32, queries []vectorstore.Query, selectFields []string, threshold float64, limit int64) ([]vectorstore.SearchResult, error) {
	return nil, vectorstore.ErrNotSupported
}

func (s *directFastPathStore) RequiresVectors() bool { return false }

func (s *directFastPathStore) Add(ctx context.Context, namespace string, id string, embedding []float32, metadata map[string]interface{}) error {
	s.addIDs = append(s.addIDs, id)
	s.chunks[id] = vectorstore.SearchResult{
		ID:         id,
		Properties: metadata,
	}
	return nil
}

func (s *directFastPathStore) Delete(ctx context.Context, namespace string, id string) error {
	return nil
}

func (s *directFastPathStore) DeleteAll(ctx context.Context, namespace string, queries []vectorstore.Query) ([]vectorstore.DeleteResult, error) {
	return nil, vectorstore.ErrNotSupported
}

func (s *directFastPathStore) Close(ctx context.Context, namespace string) error { return nil }

func newCrossProviderChatRequest(provider schemas.ModelProvider, model string, requestType schemas.RequestType, prompt string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: requestType,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: provider,
			Model:    model,
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr(prompt),
					},
				},
			},
		},
	}
}

func TestDirectCacheHitPreservesCachedProviderMetadataAcrossProviders(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	store := newDirectFastPathStore()
	config := getDefaultTestConfig()
	config.CacheByProvider = bifrost.Ptr(false)
	config.CacheByModel = bifrost.Ptr(false)
	config.ConversationHistoryThreshold = DefaultConversationHistoryThreshold
	plugin := &Plugin{
		store:  store,
		config: config,
		logger: logger,
	}

	const cacheKey = "cross-provider-direct-single"
	const prompt = "Explain green threading in Go in one short sentence."

	seedCtx := CreateContextWithCacheKeyAndType(t, cacheKey, CacheTypeDirect)
	seedReq := newCrossProviderChatRequest(schemas.OpenAI, "gpt-5.2", schemas.ChatCompletionRequest, prompt)

	_, shortCircuit, err := plugin.PreLLMHook(seedCtx, seedReq)
	if err != nil {
		t.Fatalf("seed PreLLMHook failed: %v", err)
	}
	if shortCircuit != nil {
		t.Fatal("expected seed request to miss cache")
	}

	seedResponse := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ID: "cross-provider-direct-single",
			Choices: []schemas.BifrostResponseChoice{
				{
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role: schemas.ChatMessageRoleAssistant,
							Content: &schemas.ChatMessageContent{
								ContentStr: bifrost.Ptr("Go schedules lightweight goroutines in user space onto a smaller pool of OS threads."),
							},
						},
					},
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-5.2",
				ResolvedModelUsed:      "gpt-5.2",
				RequestType:            schemas.ChatCompletionRequest,
			},
		},
	}

	if _, _, err = plugin.PostLLMHook(seedCtx, seedResponse, nil); err != nil {
		t.Fatalf("seed PostLLMHook failed: %v", err)
	}
	plugin.WaitForPendingOperations()

	hitCtx := CreateContextWithCacheKeyAndType(t, cacheKey, CacheTypeDirect)
	hitReq := newCrossProviderChatRequest(schemas.Anthropic, "claude-sonnet-4-6", schemas.ChatCompletionRequest, prompt)

	_, shortCircuit, err = plugin.PreLLMHook(hitCtx, hitReq)
	if err != nil {
		t.Fatalf("hit PreLLMHook failed: %v", err)
	}
	if shortCircuit == nil || shortCircuit.Response == nil || shortCircuit.Response.ChatResponse == nil {
		t.Fatal("expected cross-provider direct cache hit to return a response")
	}

	extraFields := shortCircuit.Response.ChatResponse.ExtraFields
	if extraFields.Provider != schemas.OpenAI {
		t.Fatalf("expected cached provider %q, got %q", schemas.OpenAI, extraFields.Provider)
	}
	if extraFields.OriginalModelRequested != "gpt-5.2" {
		t.Fatalf("expected OriginalModelRequested %q, got %q", "gpt-5.2", extraFields.OriginalModelRequested)
	}
	if extraFields.ResolvedModelUsed != "gpt-5.2" {
		t.Fatalf("expected ResolvedModelUsed %q, got %q", "gpt-5.2", extraFields.ResolvedModelUsed)
	}
	if extraFields.CacheDebug == nil {
		t.Fatal("expected cache_debug on cache hit")
	}
	if !extraFields.CacheDebug.CacheHit {
		t.Fatal("expected cache hit to be marked in cache_debug")
	}
	if extraFields.CacheDebug.HitType == nil || *extraFields.CacheDebug.HitType != string(CacheTypeDirect) {
		t.Fatalf("expected hit_type %q, got %v", CacheTypeDirect, extraFields.CacheDebug.HitType)
	}
	if extraFields.CacheDebug.RequestedProvider == nil || *extraFields.CacheDebug.RequestedProvider != string(schemas.Anthropic) {
		t.Fatalf("expected requested_provider %q, got %v", schemas.Anthropic, extraFields.CacheDebug.RequestedProvider)
	}
	if extraFields.CacheDebug.RequestedModel == nil || *extraFields.CacheDebug.RequestedModel != "claude-sonnet-4-6" {
		t.Fatalf("expected requested_model %q, got %v", "claude-sonnet-4-6", extraFields.CacheDebug.RequestedModel)
	}
}

func TestStreamingDirectCacheHitPreservesCachedProviderMetadataAcrossProviders(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	store := newDirectFastPathStore()
	config := getDefaultTestConfig()
	config.CacheByProvider = bifrost.Ptr(false)
	config.CacheByModel = bifrost.Ptr(false)
	config.ConversationHistoryThreshold = DefaultConversationHistoryThreshold
	plugin := &Plugin{
		store:  store,
		config: config,
		logger: logger,
	}

	const cacheKey = "cross-provider-direct-stream"
	const prompt = "Explain green threading in Go in one short sentence."

	seedCtx := CreateContextWithCacheKeyAndType(t, cacheKey, CacheTypeDirect)
	seedReq := newCrossProviderChatRequest(schemas.OpenAI, "gpt-5.2", schemas.ChatCompletionStreamRequest, prompt)

	_, shortCircuit, err := plugin.PreLLMHook(seedCtx, seedReq)
	if err != nil {
		t.Fatalf("seed PreLLMHook failed: %v", err)
	}
	if shortCircuit != nil {
		t.Fatal("expected seed request to miss cache")
	}

	chunks := []struct {
		content      string
		chunkIndex   int
		finishReason *string
		streamEnd    bool
	}{
		{content: "Go schedules lightweight goroutines", chunkIndex: 0, finishReason: nil, streamEnd: false},
		{content: " onto a smaller pool of OS threads.", chunkIndex: 1, finishReason: bifrost.Ptr("stop"), streamEnd: true},
	}

	for _, chunk := range chunks {
		seedCtx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, chunk.streamEnd)
		chunkResponse := &schemas.BifrostResponse{
			ChatResponse: &schemas.BifrostChatResponse{
				ID: "cross-provider-direct-stream",
				Choices: []schemas.BifrostResponseChoice{
					{
						Index:        chunk.chunkIndex,
						FinishReason: chunk.finishReason,
						ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
							Delta: &schemas.ChatStreamResponseChoiceDelta{
								Content: bifrost.Ptr(chunk.content),
							},
						},
					},
				},
				ExtraFields: schemas.BifrostResponseExtraFields{
					Provider:               schemas.OpenAI,
					OriginalModelRequested: "gpt-5.2",
					ResolvedModelUsed:      "gpt-5.2",
					RequestType:            schemas.ChatCompletionStreamRequest,
					ChunkIndex:             chunk.chunkIndex,
				},
			},
		}

		if _, _, err = plugin.PostLLMHook(seedCtx, chunkResponse, nil); err != nil {
			t.Fatalf("seed PostLLMHook failed for chunk %d: %v", chunk.chunkIndex, err)
		}
		plugin.WaitForPendingOperations()
	}

	hitCtx := CreateContextWithCacheKeyAndType(t, cacheKey, CacheTypeDirect)
	hitReq := newCrossProviderChatRequest(schemas.Anthropic, "claude-sonnet-4-6", schemas.ChatCompletionStreamRequest, prompt)

	_, shortCircuit, err = plugin.PreLLMHook(hitCtx, hitReq)
	if err != nil {
		t.Fatalf("hit PreLLMHook failed: %v", err)
	}
	if shortCircuit == nil || shortCircuit.Stream == nil {
		t.Fatal("expected cross-provider streaming direct cache hit to return a stream")
	}

	chunkCount := 0
	for chunk := range shortCircuit.Stream {
		if chunk.BifrostChatResponse == nil {
			t.Fatal("expected cached chat stream chunk")
		}

		extraFields := chunk.BifrostChatResponse.ExtraFields
		if extraFields.Provider != schemas.OpenAI {
			t.Fatalf("expected cached provider %q on chunk %d, got %q", schemas.OpenAI, chunkCount, extraFields.Provider)
		}
		if extraFields.OriginalModelRequested != "gpt-5.2" {
			t.Fatalf("expected OriginalModelRequested %q on chunk %d, got %q", "gpt-5.2", chunkCount, extraFields.OriginalModelRequested)
		}
		if extraFields.ResolvedModelUsed != "gpt-5.2" {
			t.Fatalf("expected ResolvedModelUsed %q on chunk %d, got %q", "gpt-5.2", chunkCount, extraFields.ResolvedModelUsed)
		}
		if chunkCount == len(chunks)-1 {
			if extraFields.CacheDebug == nil || !extraFields.CacheDebug.CacheHit {
				t.Fatal("expected final cached stream chunk to include cache_debug cache_hit=true")
			}
			if extraFields.CacheDebug.HitType == nil || *extraFields.CacheDebug.HitType != string(CacheTypeDirect) {
				t.Fatalf("expected final stream hit_type %q, got %v", CacheTypeDirect, extraFields.CacheDebug.HitType)
			}
			if extraFields.CacheDebug.RequestedProvider == nil || *extraFields.CacheDebug.RequestedProvider != string(schemas.Anthropic) {
				t.Fatalf("expected final stream requested_provider %q, got %v", schemas.Anthropic, extraFields.CacheDebug.RequestedProvider)
			}
			if extraFields.CacheDebug.RequestedModel == nil || *extraFields.CacheDebug.RequestedModel != "claude-sonnet-4-6" {
				t.Fatalf("expected final stream requested_model %q, got %v", "claude-sonnet-4-6", extraFields.CacheDebug.RequestedModel)
			}
		}

		chunkCount++
	}

	if chunkCount != len(chunks) {
		t.Fatalf("expected %d cached stream chunks, got %d", len(chunks), chunkCount)
	}
}

// runDirectSearchForTest is a small helper for the unit tests that directly
// exercise performDirectSearch. It builds the metadata + paramsHash + state
// the way PreLLMHook would and then calls the search.
func runDirectSearchForTest(t *testing.T, plugin *Plugin, ctx *schemas.BifrostContext, req *schemas.BifrostRequest, cacheKey string) (*cacheState, *schemas.LLMPluginShortCircuit, error) {
	t.Helper()
	requestID, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if requestID == "" {
		t.Fatal("test context is missing request ID")
	}
	state := plugin.createCacheState(requestID)
	metadata, err := plugin.buildRequestMetadataForCaching(state, req)
	if err != nil {
		t.Fatalf("buildRequestMetadataForCaching failed: %v", err)
	}
	paramsHash, err := hashMap(metadata)
	if err != nil {
		t.Fatalf("hashMap failed: %v", err)
	}
	state.ParamsHash = paramsHash
	sc, err := plugin.performDirectSearch(ctx, state, req, cacheKey, metadata, paramsHash)
	return state, sc, err
}

func TestCacheTypeDirectUsesChunkLookup(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	store := newDirectFastPathStore()
	plugin := &Plugin{
		store:  store,
		config: getDefaultTestConfig(),
		logger: logger,
	}

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("What is Bifrost?", 0.7, 50),
	}

	// First pass: warm the deterministic cache ID and learn what it is.
	ctx := CreateContextWithCacheKeyAndType(t, "chunk-fast-path", CacheTypeDirect)
	state, _, err := runDirectSearchForTest(t, plugin, ctx, req, "chunk-fast-path")
	if err != nil {
		t.Fatalf("performDirectSearch failed: %v", err)
	}
	directID := state.DirectCacheID
	if directID == "" {
		t.Fatal("expected DirectCacheID to be populated")
	}

	cachedContent := "cached response"
	cachedResponse := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role: schemas.ChatMessageRoleAssistant,
							Content: &schemas.ChatMessageContent{
								ContentStr: &cachedContent,
							},
						},
					},
				},
			},
		},
	}
	responseJSON, err := schemas.MarshalDeeplySorted(cachedResponse)
	if err != nil {
		t.Fatalf("failed to marshal cached response: %v", err)
	}

	store.chunks[directID] = vectorstore.SearchResult{
		ID: directID,
		Properties: map[string]interface{}{
			"response":   string(responseJSON),
			"expires_at": time.Now().Add(time.Minute).Unix(),
		},
	}

	// Second pass: should hit the chunk we just stored, via point-fetch only.
	priorChunkCalls := store.getChunkCalls
	ctx2 := CreateContextWithCacheKeyAndType(t, "chunk-fast-path", CacheTypeDirect)
	_, shortCircuit, err := runDirectSearchForTest(t, plugin, ctx2, req, "chunk-fast-path")
	if err != nil {
		t.Fatalf("second performDirectSearch failed: %v", err)
	}
	if shortCircuit == nil || shortCircuit.Response == nil || shortCircuit.Response.ChatResponse == nil {
		t.Fatal("expected direct chunk lookup to return cached response")
	}
	if store.getChunkCalls != priorChunkCalls+1 {
		t.Fatalf("expected one additional GetChunk call, got %d total", store.getChunkCalls)
	}
	if store.getAllCalls != 0 {
		t.Fatalf("expected no GetAll calls, got %d", store.getAllCalls)
	}
	if store.lastGetChunkID != directID {
		t.Fatalf("expected GetChunk to use %q, got %q", directID, store.lastGetChunkID)
	}
}

func TestDefaultDirectSearchSetsStorageIDForDeterministicWrites(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	store := newDirectFastPathStore()
	plugin := &Plugin{
		store:  store,
		config: getDefaultTestConfig(),
		logger: logger,
	}

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("What is Bifrost?", 0.7, 50),
	}

	ctx := CreateContextWithCacheKey(t, "default-mode")
	state, _, err := runDirectSearchForTest(t, plugin, ctx, req, "default-mode")
	if err != nil {
		t.Fatalf("performDirectSearch failed: %v", err)
	}
	if state.DirectCacheID == "" {
		t.Fatal("expected default direct search to populate state.DirectCacheID")
	}
	if store.getChunkCalls != 1 {
		t.Fatalf("expected one GetChunk call, got %d", store.getChunkCalls)
	}
}

// TestPreLLMHookResetsStateOnReusedRequestID verifies that a second PreLLMHook
// call for the same request ID overwrites any prior state instead of inheriting it.
func TestPreLLMHookResetsStateOnReusedRequestID(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	store := newDirectFastPathStore()
	config := getDefaultTestConfig()
	config.ConversationHistoryThreshold = 3
	plugin := &Plugin{
		store:  store,
		config: config,
		logger: logger,
	}

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("What is Bifrost?", 0.7, 50),
	}

	ctx := CreateContextWithCacheKey(t, "reused-context")
	requestID, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	// Seed stale state under the same request ID.
	stale := plugin.createCacheState(requestID)
	stale.DirectCacheID = "stale-storage-id"
	stale.ParamsHash = "stale-params-hash"

	if _, _, err := plugin.PreLLMHook(ctx, req); err != nil {
		t.Fatalf("PreLLMHook failed: %v", err)
	}

	state := plugin.getCacheState(requestID)
	if state == nil {
		t.Fatal("expected cache state to be present after PreLLMHook")
	}
	if state == stale {
		t.Fatal("expected PreLLMHook to replace the stale state object")
	}
	if state.DirectCacheID == "" {
		t.Fatal("expected PreLLMHook to populate a deterministic DirectCacheID")
	}
	if state.DirectCacheID == "stale-storage-id" {
		t.Fatal("expected PreLLMHook to clear stale DirectCacheID before populating a new one")
	}
}

func TestCacheTypeDirectStoresDeterministicID(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	store := newDirectFastPathStore()
	config := getDefaultTestConfig()
	plugin := &Plugin{
		store:  store,
		config: config,
		logger: logger,
	}

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("What is Bifrost?", 0.7, 50),
	}
	ctx := CreateContextWithCacheKeyAndType(t, "deterministic-store", CacheTypeDirect)

	if _, _, err := plugin.PreLLMHook(ctx, req); err != nil {
		t.Fatalf("PreLLMHook failed: %v", err)
	}
	requestID, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	state := plugin.getCacheState(requestID)
	if state == nil || state.DirectCacheID == "" {
		t.Fatal("expected PreLLMHook to populate state.DirectCacheID")
	}
	directID := state.DirectCacheID

	content := "stored response"
	response := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role: schemas.ChatMessageRoleAssistant,
							Content: &schemas.ChatMessageContent{
								ContentStr: &content,
							},
						},
					},
				},
			},
		},
	}
	response.ChatResponse.ExtraFields.RequestType = schemas.ChatCompletionRequest

	if _, _, err := plugin.PostLLMHook(ctx, response, nil); err != nil {
		t.Fatalf("PostLLMHook failed: %v", err)
	}

	plugin.WaitForPendingOperations()

	if len(store.addIDs) != 1 {
		t.Fatalf("expected one store.Add call, got %d", len(store.addIDs))
	}
	if store.addIDs[0] != directID {
		t.Fatalf("expected deterministic storage id %q, got %q", directID, store.addIDs[0])
	}
	if store.addIDs[0] == requestID {
		t.Fatal("expected storage id to differ from request ID")
	}
}

func TestPostLLMHookUsesDeterministicStorageIDOutsideDirectMode(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	store := newDirectFastPathStore()
	plugin := &Plugin{
		store:  store,
		config: getDefaultTestConfig(),
		logger: logger,
	}

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("What is Bifrost?", 0.7, 50),
	}

	// Default mode (no CacheTypeKey) should still produce a deterministic
	// storage ID via the direct-search path that PreLLMHook always runs.
	ctx := CreateContextWithCacheKey(t, "default-mode-store")
	if _, _, err := plugin.PreLLMHook(ctx, req); err != nil {
		t.Fatalf("PreLLMHook failed: %v", err)
	}
	requestID, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	state := plugin.getCacheState(requestID)
	if state == nil || state.DirectCacheID == "" {
		t.Fatal("expected default-mode PreLLMHook to populate state.DirectCacheID")
	}
	directID := state.DirectCacheID

	content := "stored response"
	response := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role: schemas.ChatMessageRoleAssistant,
							Content: &schemas.ChatMessageContent{
								ContentStr: &content,
							},
						},
					},
				},
			},
		},
	}
	response.ChatResponse.ExtraFields.RequestType = schemas.ChatCompletionRequest

	if _, _, err := plugin.PostLLMHook(ctx, response, nil); err != nil {
		t.Fatalf("PostLLMHook failed: %v", err)
	}

	plugin.WaitForPendingOperations()

	if len(store.addIDs) != 1 {
		t.Fatalf("expected one store.Add call, got %d", len(store.addIDs))
	}
	if store.addIDs[0] != directID {
		t.Fatalf("expected PostLLMHook to use deterministic storage id outside direct mode, got %q", store.addIDs[0])
	}
}

func TestGetOrCreateStreamAccumulatorUsesSingleAccumulatorPerRequest(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	plugin := &Plugin{
		logger: logger,
	}

	requestID := "stream-request"
	storageID := "stream-storage"
	embedding := []float32{1, 2, 3}
	metadata := map[string]interface{}{"cache_key": "stream-cache"}
	ttl := time.Minute

	const workers = 8
	results := make(chan *StreamAccumulator, workers)

	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			results <- plugin.getOrCreateStreamAccumulator(requestID, storageID, embedding, metadata, ttl)
		}()
	}

	wg.Wait()
	close(results)

	var first *StreamAccumulator
	for accumulator := range results {
		if accumulator == nil {
			t.Fatal("expected accumulator")
		}
		if first == nil {
			first = accumulator
			continue
		}
		if accumulator != first {
			t.Fatal("expected all callers to receive the same accumulator instance")
		}
	}

	stored, ok := plugin.streamAccumulators.Load(requestID)
	if !ok {
		t.Fatal("expected accumulator to be stored")
	}
	if stored.(*StreamAccumulator) != first {
		t.Fatal("expected stored accumulator to match returned accumulator")
	}
	if first.StorageID != storageID {
		t.Fatalf("expected storage id %q, got %q", storageID, first.StorageID)
	}
	if first.TTL != ttl {
		t.Fatalf("expected ttl %v, got %v", ttl, first.TTL)
	}
}
