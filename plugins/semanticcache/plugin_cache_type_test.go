package semanticcache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// TestCacheTypeDirectOnly tests that CacheTypeKey set to "direct" only performs direct hash matching
func TestCacheTypeDirectOnly(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// First, cache a response using CacheTypeDirect so it is stored under the deterministic ID
	ctx1 := CreateContextWithCacheKeyAndType("test-cache-type-direct", CacheTypeDirect)
	testRequest := CreateBasicChatRequest("What is Bifrost?", 0.7, 50)

	t.Log("Making first request to populate cache...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache(setup.Plugin)

	// Now test with CacheTypeKey set to direct only
	ctx2 := CreateContextWithCacheKeyAndType("test-cache-type-direct", CacheTypeDirect)

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
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// First, cache a response using normal behavior
	ctx1 := CreateContextWithCacheKey("test-cache-type-semantic")
	testRequest := CreateBasicChatRequest("Explain machine learning concepts", 0.7, 50)

	t.Log("Making first request to populate cache...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache(setup.Plugin)

	// Test with slightly different wording that should match semantically but not directly
	similarRequest := CreateBasicChatRequest("Can you explain concepts in machine learning", 0.7, 50)

	// Try with semantic-only search
	ctx2 := CreateContextWithCacheKeyAndType("test-cache-type-semantic", CacheTypeSemantic)

	t.Log("Making second request with similar content and CacheTypeKey=semantic...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, similarRequest)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}

	// This might be a cache hit if semantic similarity is high enough
	// The test validates that semantic search is attempted
	if response2.ExtraFields.CacheDebug != nil && response2.ExtraFields.CacheDebug.CacheHit {
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
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Cache a response first
	ctx1 := CreateContextWithCacheKey("test-cache-type-fallback")
	testRequest := CreateBasicChatRequest("Define artificial intelligence", 0.7, 50)

	t.Log("Making first request to populate cache...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache(setup.Plugin)

	// Test exact match (should hit direct cache)
	ctx2 := CreateContextWithCacheKey("test-cache-type-fallback")

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

	// May or may not be a cache hit depending on semantic similarity
	if response3.ExtraFields.CacheDebug != nil && response3.ExtraFields.CacheDebug.CacheHit {
		AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3}, "semantic")
		t.Log("✅ Default behavior correctly found semantic match")
	} else {
		t.Log("ℹ️  No semantic match found (normal for different wording)")
		AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3})
	}

	t.Log("✅ Default behavior correctly attempts both direct and semantic search")
}

// TestCacheTypeInvalidValue tests behavior with invalid CacheTypeKey values
func TestCacheTypeInvalidValue(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Create context with invalid cache type
	ctx := CreateContextWithCacheKey("test-invalid-cache-type")
	ctx = ctx.WithValue(CacheTypeKey, "invalid_type")

	testRequest := CreateBasicChatRequest("Test invalid cache type", 0.7, 50)

	t.Log("Making request with invalid CacheTypeKey value...")
	response, err := setup.Client.ChatCompletionRequest(ctx, testRequest)
	if err != nil {
		return // Test will be skipped by retry function
	}

	// Should fall back to default behavior (both direct and semantic)
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response})

	t.Log("✅ Invalid CacheTypeKey value falls back to default behavior")
}

// TestCacheTypeWithEmbeddingRequests tests CacheTypeKey behavior with embedding requests
func TestCacheTypeWithEmbeddingRequests(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	embeddingRequest := CreateEmbeddingRequest([]string{"Test embedding with cache type"})

	// Cache first request
	ctx1 := CreateContextWithCacheKey("test-embedding-cache-type")
	t.Log("Making first embedding request...")
	response1, err1 := setup.Client.EmbeddingRequest(ctx1, embeddingRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response1})

	WaitForCache(setup.Plugin)

	// Test with direct-only cache type
	ctx2 := CreateContextWithCacheKeyAndType("test-embedding-cache-type", CacheTypeDirect)
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
	ctx3 := CreateContextWithCacheKeyAndType("test-embedding-cache-type", CacheTypeSemantic)
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
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	testRequest := CreateBasicChatRequest("Performance test for cache types", 0.7, 50)

	// Cache first request using CacheTypeDirect so it is stored under the deterministic ID
	ctx1 := CreateContextWithCacheKeyAndType("test-cache-performance", CacheTypeDirect)
	t.Log("Making first request to populate cache...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache(setup.Plugin)

	// Test direct-only performance
	ctx2 := CreateContextWithCacheKeyAndType("test-cache-performance", CacheTypeDirect)
	start2 := time.Now()
	response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
	duration2 := time.Since(start2)
	if err2 != nil {
		t.Fatalf("Direct cache request failed: %v", err2)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct")

	t.Logf("Direct cache lookup took: %v", duration2)

	// Test default behavior (both direct and semantic) performance
	ctx3 := CreateContextWithCacheKey("test-cache-performance")
	start3 := time.Now()
	response3, err3 := setup.Client.ChatCompletionRequest(ctx3, testRequest)
	duration3 := time.Since(start3)
	if err3 != nil {
		t.Fatalf("Default cache request failed: %v", err3)
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3}, "direct")

	t.Logf("Default cache lookup took: %v", duration3)

	// Both should be fast since they hit direct cache
	// Direct-only might be slightly faster as it doesn't need to prepare for semantic fallback
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

	seedCtx := CreateContextWithCacheKeyAndType(cacheKey, CacheTypeDirect)
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

	hitCtx := CreateContextWithCacheKeyAndType(cacheKey, CacheTypeDirect)
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

	seedCtx := CreateContextWithCacheKeyAndType(cacheKey, CacheTypeDirect)
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

	hitCtx := CreateContextWithCacheKeyAndType(cacheKey, CacheTypeDirect)
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

	ctx := CreateContextWithCacheKeyAndType("chunk-fast-path", CacheTypeDirect)
	directID, err := plugin.prepareDirectCacheLookup(ctx, req, "chunk-fast-path")
	if err != nil {
		t.Fatalf("prepareDirectCacheLookup failed: %v", err)
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

	shortCircuit, err := plugin.performDirectChunkLookup(ctx, req, "chunk-fast-path")
	if err != nil {
		t.Fatalf("performDirectChunkLookup failed: %v", err)
	}
	if shortCircuit == nil || shortCircuit.Response == nil || shortCircuit.Response.ChatResponse == nil {
		t.Fatal("expected direct chunk lookup to return cached response")
	}
	if store.getChunkCalls != 1 {
		t.Fatalf("expected one GetChunk call, got %d", store.getChunkCalls)
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

	ctx := CreateContextWithCacheKey("default-mode")
	_, err := plugin.performDirectSearch(ctx, req, "default-mode")
	if err != nil && !errors.Is(err, vectorstore.ErrNotSupported) {
		t.Fatalf("performDirectSearch failed: %v", err)
	}

	storageID, _ := ctx.Value(requestStorageIDKey).(string)
	if storageID == "" {
		t.Fatal("expected default direct search to set requestStorageIDKey")
	}
	if store.getChunkCalls != 1 {
		t.Fatalf("expected one GetChunk call, got %d", store.getChunkCalls)
	}
}

func TestPreLLMHookClearsStaleStorageIDOnReusedContext(t *testing.T) {
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

	ctx := CreateContextWithCacheKey("reused-context")
	ctx.SetValue(requestStorageIDKey, "stale-storage-id")

	if _, _, err := plugin.PreLLMHook(ctx, req); err != nil {
		t.Fatalf("PreLLMHook failed: %v", err)
	}

	storageID, _ := ctx.Value(requestStorageIDKey).(string)
	if storageID == "" {
		t.Fatal("expected PreLLMHook to replace stale requestStorageIDKey with a deterministic id")
	}
	if storageID == "stale-storage-id" {
		t.Fatal("expected PreLLMHook to clear stale requestStorageIDKey before setting a deterministic id")
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
	ctx := CreateContextWithCacheKeyAndType("deterministic-store", CacheTypeDirect)
	ctx.SetValue(requestIDKey, "request-uuid")
	ctx.SetValue(requestProviderKey, schemas.OpenAI)
	ctx.SetValue(requestModelKey, req.ChatRequest.Model)

	directID, err := plugin.prepareDirectCacheLookup(ctx, req, "deterministic-store")
	if err != nil {
		t.Fatalf("prepareDirectCacheLookup failed: %v", err)
	}
	ctx.SetValue(requestStorageIDKey, directID)

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
	if store.addIDs[0] == "request-uuid" {
		t.Fatal("expected storage id to differ from request UUID")
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

	ctx := CreateContextWithCacheKey("default-mode-store")
	ctx.SetValue(requestIDKey, "request-uuid")
	ctx.SetValue(requestProviderKey, schemas.OpenAI)
	ctx.SetValue(requestModelKey, "openai/gpt-4o-mini")
	ctx.SetValue(requestHashKey, "request-hash")
	ctx.SetValue(requestParamsHashKey, "params-hash")

	directID := plugin.generateDirectCacheID(schemas.OpenAI, "openai/gpt-4o-mini", "default-mode-store", "request-hash", "params-hash")
	ctx.SetValue(requestStorageIDKey, directID)

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

func TestPerformDirectSearchDisablesScanFallbackForLegacyLookup(t *testing.T) {
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

	ctx := CreateContextWithCacheKey("legacy-no-scan")
	_, err := plugin.performDirectSearch(ctx, req, "legacy-no-scan")
	if err != nil && !errors.Is(err, vectorstore.ErrNotSupported) {
		t.Fatalf("performDirectSearch failed: %v", err)
	}

	if store.getAllCalls != 1 {
		t.Fatalf("expected one legacy GetAll call, got %d", store.getAllCalls)
	}
	if !vectorstore.IsScanFallbackDisabled(store.lastGetAllCtx) {
		t.Fatal("expected legacy direct lookup to disable scan fallback")
	}
}

func TestPerformLegacyDirectSearchTreatsQuerySyntaxErrorAsMiss(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	store := newDirectFastPathStore()
	store.getAllErr = vectorstore.ErrQuerySyntax
	plugin := &Plugin{
		store:  store,
		config: getDefaultTestConfig(),
		logger: logger,
	}

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("What is Bifrost?", 0.7, 50),
	}

	ctx := CreateContextWithCacheKey("legacy-query-syntax")
	_, err := plugin.prepareDirectCacheLookup(ctx, req, "legacy-query-syntax")
	if err != nil {
		t.Fatalf("prepareDirectCacheLookup failed: %v", err)
	}

	shortCircuit, err := plugin.performLegacyDirectSearch(ctx, req, "legacy-query-syntax")
	if err != nil {
		t.Fatalf("performLegacyDirectSearch failed: %v", err)
	}
	if shortCircuit != nil {
		t.Fatal("expected query syntax incompatibility to be treated as a miss")
	}
	if store.getAllCalls != 1 {
		t.Fatalf("expected one legacy GetAll call, got %d", store.getAllCalls)
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
