package semanticcache

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// requiresVectors returns true if the vector store requires vectors for storage.
// Some stores (like Qdrant, Pinecone, and Weaviate) require vectors for all entries,
// while others (like Redis) can store metadata without vectors.
func requiresVectors(storeType vectorstore.VectorStoreType) bool {
	switch storeType {
	case vectorstore.VectorStoreTypeQdrant, vectorstore.VectorStoreTypePinecone, vectorstore.VectorStoreTypeWeaviate:
		return true
	default:
		return false
	}
}

// skipIfNoAPIKey skips the test if OPENAI_API_KEY is not set and the store requires vectors.
func skipIfNoAPIKey(t *testing.T, storeType vectorstore.VectorStoreType) {
	if requiresVectors(storeType) && os.Getenv("OPENAI_API_KEY") == "" {
		t.Skipf("Skipping %s test: OPENAI_API_KEY not set (required for embedding generation)", storeType)
	}
}

// VectorStoreTestCase defines a test case for a specific vector store
type VectorStoreTestCase struct {
	Name      string
	StoreType vectorstore.VectorStoreType
}

// getVectorStoreTestCases returns all vector store test cases
func getVectorStoreTestCases() []VectorStoreTestCase {
	return []VectorStoreTestCase{
		{"Weaviate", vectorstore.VectorStoreTypeWeaviate},
		{"Redis", vectorstore.VectorStoreTypeRedis},
		{"Qdrant", vectorstore.VectorStoreTypeQdrant},
		{"Pinecone", vectorstore.VectorStoreTypePinecone},
	}
}

// getDefaultTestConfig returns the default test configuration
func getDefaultTestConfig() *Config {
	return &Config{
		Provider:          schemas.OpenAI,
		EmbeddingModel:    "text-embedding-3-small",
		Dimension:         1536,
		Threshold:         0.8,
		CleanUpOnShutdown: true,
		Keys: []schemas.Key{
			{
				Value:  *schemas.NewEnvVar("env.OPENAI_API_KEY"),
				Models: []string{},
				Weight: 1.0,
			},
		},
	}
}

// TestSemanticCache_AllVectorStores_BasicFlow tests the basic cache flow across all vector stores
func TestSemanticCache_AllVectorStores_BasicFlow(t *testing.T) {
	for _, tc := range getVectorStoreTestCases() {
		t.Run(tc.Name, func(t *testing.T) {
			skipIfNoAPIKey(t, tc.StoreType)
			setup := NewTestSetupWithVectorStore(t, getDefaultTestConfig(), tc.StoreType)
			defer setup.Cleanup()

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(CacheKey, "test-"+strings.ToLower(tc.Name)+"-basic")

			// Test request
			request := &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{
					Provider: schemas.OpenAI,
					Model:    "gpt-4o-mini",
					Input: []schemas.ChatMessage{
						{
							Role: schemas.ChatMessageRoleUser,
							Content: &schemas.ChatMessageContent{
								ContentStr: bifrost.Ptr("Hello from " + tc.Name + " test!"),
							},
						},
					},
					Params: &schemas.ChatParameters{
						Temperature:         bifrost.Ptr(0.7),
						MaxCompletionTokens: bifrost.Ptr(100),
					},
				},
			}

			t.Logf("[%s] Testing first request (cache miss)...", tc.Name)

			// First request - should be a cache miss
			modifiedReq, shortCircuit, err := setup.Plugin.PreLLMHook(ctx, request)
			if err != nil {
				t.Fatalf("[%s] PreHook failed: %v", tc.Name, err)
			}

			if shortCircuit != nil {
				t.Fatalf("[%s] Expected cache miss, but got cache hit", tc.Name)
			}

			if modifiedReq == nil {
				t.Fatalf("[%s] Modified request is nil", tc.Name)
			}

			t.Logf("[%s] Cache miss handled correctly", tc.Name)

			// Simulate a response
			response := &schemas.BifrostResponse{
				ChatResponse: &schemas.BifrostChatResponse{
					ID: uuid.New().String(),
					Choices: []schemas.BifrostResponseChoice{
						{
							Index: 0,
							ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
								Message: &schemas.ChatMessage{
									Role: schemas.ChatMessageRoleAssistant,
									Content: &schemas.ChatMessageContent{
										ContentStr: bifrost.Ptr("Hello! Response from " + tc.Name + " test."),
									}},
							},
						},
					},
					ExtraFields: schemas.BifrostResponseExtraFields{
						Provider:       schemas.OpenAI,
						ModelRequested: "gpt-4o-mini",
						RequestType:    schemas.ChatCompletionRequest,
					},
				},
			}

			// Cache the response
			t.Logf("[%s] Caching response...", tc.Name)
			_, _, err = setup.Plugin.PostLLMHook(ctx, response, nil)
			if err != nil {
				t.Fatalf("[%s] PostHook failed: %v", tc.Name, err)
			}

			// Wait for async caching to complete
			WaitForCache()
			t.Logf("[%s] Response cached successfully", tc.Name)

			// Second request - should be a cache hit
			t.Logf("[%s] Testing second identical request (expecting cache hit)...", tc.Name)

			ctx2 := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx2.SetValue(CacheKey, "test-"+strings.ToLower(tc.Name)+"-basic")

			_, shortCircuit2, err := setup.Plugin.PreLLMHook(ctx2, request)
			if err != nil {
				t.Fatalf("[%s] Second PreHook failed: %v", tc.Name, err)
			}

			if shortCircuit2 == nil {
				t.Fatalf("[%s] Expected cache hit on identical request, but got cache miss", tc.Name)
			}

			if shortCircuit2.Response == nil {
				t.Fatalf("[%s] Cache hit but response is nil", tc.Name)
			}

			t.Logf("[%s] Cache hit detected and response returned", tc.Name)
			t.Logf("[%s] Basic flow test passed!", tc.Name)
		})
	}
}

// TestSemanticCache_AllVectorStores_DirectHashMatch tests direct hash matching across all vector stores
func TestSemanticCache_AllVectorStores_DirectHashMatch(t *testing.T) {
	for _, tc := range getVectorStoreTestCases() {
		t.Run(tc.Name, func(t *testing.T) {
			skipIfNoAPIKey(t, tc.StoreType)
			setup := NewTestSetupWithVectorStore(t, getDefaultTestConfig(), tc.StoreType)
			defer setup.Cleanup()

			// Use unique cache key per test run to avoid stale data from previous runs
			// (Pinecone Local doesn't support deletion by metadata filter)
			testRunID := uuid.New().String()[:8]
			cacheKey := "test-" + strings.ToLower(tc.Name) + "-direct-" + testRunID

			ctx := CreateContextWithCacheKeyAndType(cacheKey, CacheTypeDirect)

			testRequest := CreateBasicChatRequest("Direct hash test for "+tc.Name+" "+testRunID, 0.7, 50)

			t.Logf("[%s] Making first request to populate cache...", tc.Name)
			response1, err1 := setup.Client.ChatCompletionRequest(ctx, testRequest)
			if err1 != nil {
				t.Skipf("[%s] First request failed (likely no API key): %v", tc.Name, err1)
				return
			}
			AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

			WaitForCache()

			// Second request with direct-only cache type
			ctx2 := CreateContextWithCacheKeyAndType(cacheKey, CacheTypeDirect)

			t.Logf("[%s] Making second request with CacheTypeDirect...", tc.Name)
			response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
			if err2 != nil {
				t.Fatalf("[%s] Second request failed: %v", tc.Name, err2.Error.Message)
			}

			AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct")
			t.Logf("[%s] Direct hash match test passed!", tc.Name)
		})
	}
}

// TestSemanticCache_AllVectorStores_NamespaceIsolation tests that different cache keys are isolated
func TestSemanticCache_AllVectorStores_NamespaceIsolation(t *testing.T) {
	for _, tc := range getVectorStoreTestCases() {
		t.Run(tc.Name, func(t *testing.T) {
			skipIfNoAPIKey(t, tc.StoreType)
			setup := NewTestSetupWithVectorStore(t, getDefaultTestConfig(), tc.StoreType)
			defer setup.Cleanup()

			// Use unique cache keys per test run to avoid stale data from previous runs
			// (Pinecone Local doesn't support deletion by metadata filter)
			testRunID := uuid.New().String()[:8]
			cacheKey1 := "test-" + strings.ToLower(tc.Name) + "-namespace-1-" + testRunID
			cacheKey2 := "test-" + strings.ToLower(tc.Name) + "-namespace-2-" + testRunID

			// Cache with first key
			ctx1 := CreateContextWithCacheKey(cacheKey1)
			testRequest := CreateBasicChatRequest("Namespace isolation test for "+tc.Name+" "+testRunID, 0.7, 50)

			t.Logf("[%s] Making request with cache key 1...", tc.Name)
			response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
			if err1 != nil {
				t.Skipf("[%s] First request failed (likely no API key): %v", tc.Name, err1)
				return
			}
			AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

			WaitForCache()

			// Try with different cache key - should miss
			ctx2 := CreateContextWithCacheKey(cacheKey2)

			t.Logf("[%s] Making same request with different cache key (expecting miss)...", tc.Name)
			response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
			if err2 != nil {
				t.Fatalf("[%s] Second request failed: %v", tc.Name, err2.Error.Message)
			}

			// Should be a cache miss because different namespace
			AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2})

			// Try with original key - should hit
			ctx3 := CreateContextWithCacheKey(cacheKey1)

			t.Logf("[%s] Making same request with original cache key (expecting hit)...", tc.Name)
			response3, err3 := setup.Client.ChatCompletionRequest(ctx3, testRequest)
			if err3 != nil {
				t.Fatalf("[%s] Third request failed: %v", tc.Name, err3.Error.Message)
			}

			AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3}, "direct")
			t.Logf("[%s] Namespace isolation test passed!", tc.Name)
		})
	}
}

// TestSemanticCache_AllVectorStores_ParameterFiltering tests that different parameters don't share cache
func TestSemanticCache_AllVectorStores_ParameterFiltering(t *testing.T) {
	for _, tc := range getVectorStoreTestCases() {
		t.Run(tc.Name, func(t *testing.T) {
			skipIfNoAPIKey(t, tc.StoreType)
			setup := NewTestSetupWithVectorStore(t, getDefaultTestConfig(), tc.StoreType)
			defer setup.Cleanup()

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(CacheKey, "test-"+strings.ToLower(tc.Name)+"-params")

			// First request with temperature=0.7
			request1 := &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{
					Provider: schemas.OpenAI,
					Model:    "gpt-4o-mini",
					Input: []schemas.ChatMessage{
						{
							Role: schemas.ChatMessageRoleUser,
							Content: &schemas.ChatMessageContent{
								ContentStr: bifrost.Ptr("Parameter test for " + tc.Name),
							},
						},
					},
					Params: &schemas.ChatParameters{
						Temperature:         bifrost.Ptr(0.7),
						MaxCompletionTokens: bifrost.Ptr(100),
					},
				},
			}

			t.Logf("[%s] Testing first request with temperature=0.7...", tc.Name)

			_, shortCircuit1, err := setup.Plugin.PreLLMHook(ctx, request1)
			if err != nil {
				t.Fatalf("[%s] First PreHook failed: %v", tc.Name, err)
			}

			if shortCircuit1 != nil {
				t.Fatalf("[%s] Expected cache miss for first request", tc.Name)
			}

			// Cache a response
			response := &schemas.BifrostResponse{
				ChatResponse: &schemas.BifrostChatResponse{
					ID: uuid.New().String(),
					Choices: []schemas.BifrostResponseChoice{
						{
							ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
								Message: &schemas.ChatMessage{
									Role: schemas.ChatMessageRoleAssistant,
									Content: &schemas.ChatMessageContent{
										ContentStr: bifrost.Ptr("Response for " + tc.Name),
									}},
							},
						},
					},
					ExtraFields: schemas.BifrostResponseExtraFields{
						Provider:       schemas.OpenAI,
						ModelRequested: "gpt-4o-mini",
						RequestType:    schemas.ChatCompletionRequest,
					},
				},
			}

			_, _, err = setup.Plugin.PostLLMHook(ctx, response, nil)
			if err != nil {
				t.Fatalf("[%s] PostHook failed: %v", tc.Name, err)
			}

			WaitForCache()
			t.Logf("[%s] First response cached", tc.Name)

			// Second request with different temperature - should be cache miss
			t.Logf("[%s] Testing second request with temperature=0.5 (expecting cache miss)...", tc.Name)

			ctx2 := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx2.SetValue(CacheKey, "test-"+strings.ToLower(tc.Name)+"-params")

			request2 := &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{
					Provider: schemas.OpenAI,
					Model:    "gpt-4o-mini",
					Input: []schemas.ChatMessage{
						{
							Role: schemas.ChatMessageRoleUser,
							Content: &schemas.ChatMessageContent{
								ContentStr: bifrost.Ptr("Parameter test for " + tc.Name),
							},
						},
					},
					Params: &schemas.ChatParameters{
						Temperature:         bifrost.Ptr(0.5), // Different temperature
						MaxCompletionTokens: bifrost.Ptr(100),
					},
				},
			}

			_, shortCircuit2, err := setup.Plugin.PreLLMHook(ctx2, request2)
			if err != nil {
				t.Fatalf("[%s] Second PreHook failed: %v", tc.Name, err)
			}

			if shortCircuit2 != nil {
				t.Fatalf("[%s] Expected cache miss due to different temperature, but got cache hit", tc.Name)
			}

			t.Logf("[%s] Parameter filtering test passed!", tc.Name)
		})
	}
}

// TestSemanticCache_AllVectorStores_EmbeddingRequest tests embedding request caching across all vector stores
func TestSemanticCache_AllVectorStores_EmbeddingRequest(t *testing.T) {
	for _, tc := range getVectorStoreTestCases() {
		t.Run(tc.Name, func(t *testing.T) {
			skipIfNoAPIKey(t, tc.StoreType)
			setup := NewTestSetupWithVectorStore(t, getDefaultTestConfig(), tc.StoreType)
			defer setup.Cleanup()

			// Use unique cache key per test run to avoid stale data from previous runs
			// (Pinecone Local doesn't support deletion by metadata filter)
			testRunID := uuid.New().String()[:8]
			cacheKey := "test-" + strings.ToLower(tc.Name) + "-embedding-" + testRunID

			embeddingRequest := CreateEmbeddingRequest([]string{"Test embedding with " + tc.Name + " " + testRunID})

			// Cache first request
			ctx1 := CreateContextWithCacheKey(cacheKey)
			t.Logf("[%s] Making first embedding request...", tc.Name)
			response1, err1 := setup.Client.EmbeddingRequest(ctx1, embeddingRequest)
			if err1 != nil {
				t.Skipf("[%s] First embedding request failed (likely no API key): %v", tc.Name, err1)
				return
			}
			AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response1})

			WaitForCache()

			// Second request - should be cache hit
			ctx2 := CreateContextWithCacheKey(cacheKey)
			t.Logf("[%s] Making second embedding request (expecting cache hit)...", tc.Name)
			response2, err2 := setup.Client.EmbeddingRequest(ctx2, embeddingRequest)
			if err2 != nil {
				t.Fatalf("[%s] Second embedding request failed: %v", tc.Name, err2.Error.Message)
			}
			AssertCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response2}, "direct")

			t.Logf("[%s] Embedding request caching test passed!", tc.Name)
		})
	}
}
