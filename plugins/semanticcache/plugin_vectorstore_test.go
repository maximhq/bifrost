package semanticcache

import (
	"context"
	"maps"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/qdrant/go-client/qdrant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// getDefaultTestConfig returns the default test configuration. Mirrors the
// defaults Init applies, which matters for unit tests that construct Plugin
// directly without going through Init.
func getDefaultTestConfig() *Config {
	return &Config{
		Provider:                     schemas.OpenAI,
		EmbeddingModel:               "text-embedding-3-small",
		Dimension:                    1536,
		Threshold:                    0.8,
		ConversationHistoryThreshold: DefaultConversationHistoryThreshold,
	}
}

// TestSemanticCache_AllVectorStores_BasicFlow tests the basic cache flow across all vector stores
func TestSemanticCache_AllVectorStores_BasicFlow(t *testing.T) {
	t.Parallel()
	for _, tc := range getVectorStoreTestCases() {
		t.Run(tc.Name, func(t *testing.T) {
			skipIfNoAPIKey(t, tc.StoreType)
			setup := NewTestSetupWithVectorStore(t, getDefaultTestConfig(), tc.StoreType)
			defer setup.Cleanup()

			ctx := newBaseTestContext()
			ctx.SetValue(CacheKey, keyForTest(t, "test-"+strings.ToLower(tc.Name)+"-basic"))

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
						Provider:               schemas.OpenAI,
						OriginalModelRequested: "gpt-4o-mini",
						RequestType:            schemas.ChatCompletionRequest,
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
			WaitForCache(setup.Plugin)
			t.Logf("[%s] Response cached successfully", tc.Name)

			// Second request - should be a cache hit
			t.Logf("[%s] Testing second identical request (expecting cache hit)...", tc.Name)

			ctx2 := newBaseTestContext()
			ctx2.SetValue(CacheKey, keyForTest(t, "test-"+strings.ToLower(tc.Name)+"-basic"))

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
	t.Parallel()
	for _, tc := range getVectorStoreTestCases() {
		t.Run(tc.Name, func(t *testing.T) {
			skipIfNoAPIKey(t, tc.StoreType)
			setup := NewTestSetupWithVectorStore(t, getDefaultTestConfig(), tc.StoreType)
			defer setup.Cleanup()

			// Use unique cache key per test run to avoid stale data from previous runs
			// (Pinecone Local doesn't support deletion by metadata filter)
			testRunID := uuid.New().String()[:8]
			cacheKey := "test-" + strings.ToLower(tc.Name) + "-direct-" + testRunID

			ctx := CreateContextWithCacheKeyAndType(t, cacheKey, CacheTypeDirect)

			testRequest := CreateBasicChatRequest("Direct hash test for "+tc.Name+" "+testRunID, 0.7, 50)

			t.Logf("[%s] Making first request to populate cache...", tc.Name)
			response1, err1 := setup.Client.ChatCompletionRequest(ctx, testRequest)
			if err1 != nil {
				t.Skipf("[%s] First request failed (likely no API key): %v", tc.Name, err1)
				return
			}
			AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

			WaitForCache(setup.Plugin)

			// Second request with direct-only cache type
			ctx2 := CreateContextWithCacheKeyAndType(t, cacheKey, CacheTypeDirect)

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
	t.Parallel()
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
			ctx1 := CreateContextWithCacheKey(t, cacheKey1)
			testRequest := CreateBasicChatRequest("Namespace isolation test for "+tc.Name+" "+testRunID, 0.7, 50)

			t.Logf("[%s] Making request with cache key 1...", tc.Name)
			response1, err1 := setup.Client.ChatCompletionRequest(ctx1, testRequest)
			if err1 != nil {
				t.Skipf("[%s] First request failed (likely no API key): %v", tc.Name, err1)
				return
			}
			AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

			WaitForCache(setup.Plugin)

			// Try with different cache key - should miss
			ctx2 := CreateContextWithCacheKey(t, cacheKey2)

			t.Logf("[%s] Making same request with different cache key (expecting miss)...", tc.Name)
			response2, err2 := setup.Client.ChatCompletionRequest(ctx2, testRequest)
			if err2 != nil {
				t.Fatalf("[%s] Second request failed: %v", tc.Name, err2.Error.Message)
			}

			// Should be a cache miss because different namespace
			AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2})

			// Try with original key - should hit
			ctx3 := CreateContextWithCacheKey(t, cacheKey1)

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
	t.Parallel()
	for _, tc := range getVectorStoreTestCases() {
		t.Run(tc.Name, func(t *testing.T) {
			skipIfNoAPIKey(t, tc.StoreType)
			setup := NewTestSetupWithVectorStore(t, getDefaultTestConfig(), tc.StoreType)
			defer setup.Cleanup()

			ctx := newBaseTestContext()
			ctx.SetValue(CacheKey, keyForTest(t, "test-"+strings.ToLower(tc.Name)+"-params"))

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
						Provider:               schemas.OpenAI,
						OriginalModelRequested: "gpt-4o-mini",
						RequestType:            schemas.ChatCompletionRequest,
					},
				},
			}

			_, _, err = setup.Plugin.PostLLMHook(ctx, response, nil)
			if err != nil {
				t.Fatalf("[%s] PostHook failed: %v", tc.Name, err)
			}

			WaitForCache(setup.Plugin)
			t.Logf("[%s] First response cached", tc.Name)

			// Second request with different temperature - should be cache miss
			t.Logf("[%s] Testing second request with temperature=0.5 (expecting cache miss)...", tc.Name)

			ctx2 := newBaseTestContext()
			ctx2.SetValue(CacheKey, keyForTest(t, "test-"+strings.ToLower(tc.Name)+"-params"))

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
	t.Parallel()
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
			ctx1 := CreateContextWithCacheKey(t, cacheKey)
			t.Logf("[%s] Making first embedding request...", tc.Name)
			response1, err1 := setup.Client.EmbeddingRequest(ctx1, embeddingRequest)
			if err1 != nil {
				t.Skipf("[%s] First embedding request failed (likely no API key): %v", tc.Name, err1)
				return
			}
			AssertNoCacheHit(t, &schemas.BifrostResponse{EmbeddingResponse: response1})

			WaitForCache(setup.Plugin)

			// Second request - should be cache hit
			ctx2 := CreateContextWithCacheKey(t, cacheKey)
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

// Run real cache hooks and Qdrant RPCs without paid embedding/provider calls.
// Payload-only fields must survive storage, replay and index cleanup, while
// strict-mode queries still have every index they need.
func TestSemanticCache_QdrantPayloadOnlyFields(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping Qdrant integration test in short mode")
	}
	for _, stream := range []bool{false, true} {
		name := "response"
		if stream {
			name = "stream_chunks"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			config := getQdrantConfigFromEnv()
			logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
			store, err := vectorstore.NewVectorStore(ctx, &vectorstore.Config{Enabled: true, Type: vectorstore.VectorStoreTypeQdrant, Config: config}, logger)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, store.Close(context.Background(), "")) })
			admin, err := qdrant.NewClient(&qdrant.Config{
				Host: config.Host.GetValue(), Port: config.Port.CoerceInt(6334), APIKey: config.APIKey.GetValue(),
				UseTLS: config.UseTLS.CoerceBool(false), SkipCompatibilityCheck: true,
			})
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, admin.Close()) })
			namespace := "cache-payload-policy-" + uuid.NewString()
			require.NoError(t, admin.CreateCollection(ctx, &qdrant.CreateCollection{
				CollectionName: namespace, OnDiskPayload: qdrant.PtrOf(true),
				VectorsConfig: qdrant.NewVectorsConfig(&qdrant.VectorParams{Size: 3, Distance: qdrant.Distance_Cosine}),
			}))
			t.Cleanup(func() { assert.NoError(t, store.DeleteNamespace(context.Background(), namespace)) })
			initPlugin := func() *Plugin {
				llmPlugin, err := Init(ctx, &Config{Provider: schemas.OpenAI, EmbeddingModel: "test-embedding", Dimension: 3, VectorStoreNamespace: namespace}, logger, store)
				require.NoError(t, err)
				plugin := llmPlugin.(*Plugin)
				plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
					return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 0, 0}}}}}, nil
				})
				t.Cleanup(func() { assert.NoError(t, plugin.Cleanup()) })
				return plugin
			}
			plugin := initPlugin()
			newContext := func(key string, cacheType CacheType) *schemas.BifrostContext {
				requestCtx := withTestRequestID(schemas.NewBifrostContext(ctx, schemas.NoDeadline))
				requestCtx.SetValue(CacheKey, key)
				if cacheType != "" {
					requestCtx.SetValue(CacheTypeKey, cacheType)
				}
				t.Cleanup(requestCtx.Cancel)
				return requestCtx
			}
			requestType := schemas.ChatCompletionRequest
			if stream {
				requestType = schemas.ChatCompletionStreamRequest
			}
			newRequest := func(text string) *schemas.BifrostRequest {
				return &schemas.BifrostRequest{RequestType: requestType, ChatRequest: &schemas.BifrostChatRequest{
					Provider: schemas.OpenAI, Model: "test-model", Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &text}}},
				}}
			}
			request := newRequest("original request")
			writeCtx := newContext("scope-a", "")
			_, hit, err := plugin.PreLLMHook(writeCtx, request)
			require.NoError(t, err)
			require.Nil(t, hit)
			contents := []string{strings.Repeat("cached response ", 256)}
			if stream {
				contents = []string{"first chunk", "second chunk"}
			}
			for i, content := range contents {
				choice := schemas.BifrostResponseChoice{Index: 0}
				if stream {
					choice.ChatStreamResponseChoice = &schemas.ChatStreamResponseChoice{Delta: &schemas.ChatStreamResponseChoiceDelta{Content: &content}}
					writeCtx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, i == len(contents)-1)
				} else {
					choice.ChatNonStreamResponseChoice = &schemas.ChatNonStreamResponseChoice{Message: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &content}}}
				}
				response := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{ID: "provider-response", Choices: []schemas.BifrostResponseChoice{choice}, ExtraFields: schemas.BifrostResponseExtraFields{
					Provider: schemas.OpenAI, OriginalModelRequested: "test-model", RequestType: requestType, ChunkIndex: i,
				}}}
				_, bifrostErr, err := plugin.PostLLMHook(writeCtx, response, nil)
				require.NoError(t, err)
				require.Nil(t, bifrostErr)
			}
			plugin.WaitForPendingOperations()
			assertSchema := func(extraFields ...string) {
				info, err := admin.GetCollectionInfo(ctx, namespace)
				require.NoError(t, err)
				fields := make([]string, 0, len(info.PayloadSchema))
				for field := range info.PayloadSchema {
					fields = append(fields, field)
				}
				assert.ElementsMatch(t, append([]string{"cache_key", "provider", "model", "params_hash", "from_bifrost_semantic_cache_plugin"}, extraFields...), fields)
				assert.True(t, info.Config.Params.OnDiskPayload)
			}
			assertSchema()
			entries, _, err := store.GetAll(ctx, namespace, nil, nil, nil, 10)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			entry := entries[0]
			stored, err := store.GetChunk(ctx, namespace, entry.ID)
			require.NoError(t, err)
			assert.Equal(t, entry.Properties, stored.Properties)
			assertRetainedEntry := func() {
				t.Helper()
				// Flush queued index operations without recreating a missing
				// point or restoring its response, chunks or vector.
				result, err := admin.SetPayload(ctx, &qdrant.SetPayloadPoints{
					CollectionName: namespace, Wait: qdrant.PtrOf(true),
					PointsSelector: qdrant.NewPointsSelector(qdrant.NewID(entry.ID)),
					Payload:        map[string]*qdrant.Value{},
				})
				require.NoError(t, err)
				require.Equal(t, qdrant.UpdateStatus_Completed, result.GetStatus())
				stored, err := store.GetChunk(ctx, namespace, entry.ID)
				require.NoError(t, err)
				assert.Equal(t, entry.Properties, stored.Properties)
			}

			// Required indexes are asynchronous. The completed upsert above
			// follows them in the collection's update queue before strict mode.
			require.NoError(t, admin.UpdateCollection(ctx, &qdrant.UpdateCollection{CollectionName: namespace, StrictModeConfig: &qdrant.StrictModeConfig{
				Enabled: qdrant.PtrOf(true), UnindexedFilteringRetrieve: qdrant.PtrOf(false), UnindexedFilteringUpdate: qdrant.PtrOf(false),
			}}))
			assertHit := func(cacheType CacheType, req *schemas.BifrostRequest) {
				readCtx := newContext("scope-a", cacheType)
				_, hit, err := plugin.PreLLMHook(readCtx, req)
				require.NoError(t, err)
				require.NotNil(t, hit)
				var replayed []string
				if stream {
					require.NotNil(t, hit.Stream)
					var last *schemas.BifrostChatResponse
					for chunk := range hit.Stream {
						require.Nil(t, chunk.BifrostError)
						require.NotNil(t, chunk.BifrostChatResponse)
						last = chunk.BifrostChatResponse
						for _, choice := range last.Choices {
							require.NotNil(t, choice.ChatStreamResponseChoice)
							require.NotNil(t, choice.Delta)
							require.NotNil(t, choice.Delta.Content)
							replayed = append(replayed, *choice.Delta.Content)
						}
						_, bifrostErr, err := plugin.PostLLMHook(readCtx, &schemas.BifrostResponse{ChatResponse: last}, nil)
						require.NoError(t, err)
						require.Nil(t, bifrostErr)
					}
					require.NotNil(t, last)
					require.NotNil(t, last.ExtraFields.CacheDebug)
					assert.True(t, last.ExtraFields.CacheDebug.CacheHit)
					require.NotNil(t, last.ExtraFields.CacheDebug.HitType)
					assert.Equal(t, string(cacheType), *last.ExtraFields.CacheDebug.HitType)
				} else {
					require.NotNil(t, hit.Response)
					require.NotNil(t, hit.Response.ChatResponse)
					require.Len(t, hit.Response.ChatResponse.Choices, 1)
					require.NotNil(t, hit.Response.ChatResponse.Choices[0].ChatNonStreamResponseChoice)
					require.NotNil(t, hit.Response.ChatResponse.Choices[0].Message)
					require.NotNil(t, hit.Response.ChatResponse.Choices[0].Message.Content)
					require.NotNil(t, hit.Response.ChatResponse.Choices[0].Message.Content.ContentStr)
					replayed = []string{*hit.Response.ChatResponse.Choices[0].Message.Content.ContentStr}
					AssertCacheHit(t, hit.Response, string(cacheType))
				}
				assert.Equal(t, contents, replayed)
			}
			assertHit(CacheTypeDirect, request)
			assertHit(CacheTypeSemantic, newRequest("different phrasing"))
			_, hit, err = plugin.PreLLMHook(newContext("scope-b", CacheTypeSemantic), request)
			require.NoError(t, err)
			require.Nil(t, hit, "semantic searches must still isolate cache keys")

			// Upgrades do not delete externally managed or legacy indexes.
			for field, fieldType := range map[string]qdrant.FieldType{
				"response":      qdrant.FieldType_FieldTypeKeyword,
				"stream_chunks": qdrant.FieldType_FieldTypeKeyword,
				"expires_at":    qdrant.FieldType_FieldTypeInteger,
				"operator_note": qdrant.FieldType_FieldTypeKeyword,
			} {
				_, err := admin.CreateFieldIndex(ctx, &qdrant.CreateFieldIndexCollection{CollectionName: namespace, FieldName: field, FieldType: &fieldType, Wait: qdrant.PtrOf(true)})
				require.NoError(t, err)
			}
			require.NoError(t, plugin.Cleanup())
			plugin = initPlugin()
			assertRetainedEntry()
			assertSchema("response", "stream_chunks", "expires_at", "operator_note")
			for _, field := range []string{"response", "stream_chunks", "expires_at"} {
				_, err := admin.DeleteFieldIndex(ctx, &qdrant.DeleteFieldIndexCollection{CollectionName: namespace, FieldName: field, Wait: qdrant.PtrOf(true)})
				require.NoError(t, err)
			}
			require.NoError(t, plugin.Cleanup())
			plugin = initPlugin()
			assertRetainedEntry()
			assertSchema("operator_note")
			assertHit(CacheTypeDirect, request)
			assertHit(CacheTypeSemantic, newRequest("different phrasing"))

			// Expiry and clear-by-key need payloads and the filter indexes, not
			// indexes on the response, chunks or expiry timestamp.
			expired := maps.Clone(entry.Properties)
			expired["expires_at"] = time.Now().Add(-time.Minute).Unix()
			require.NoError(t, store.Add(ctx, namespace, entry.ID, []float32{1, 0, 0}, expired))
			_, hit, err = plugin.PreLLMHook(newContext("scope-a", CacheTypeDirect), request)
			require.NoError(t, err)
			require.Nil(t, hit)
			plugin.WaitForPendingOperations()
			require.Eventually(t, func() bool {
				points, err := admin.Get(ctx, &qdrant.GetPoints{CollectionName: namespace, Ids: []*qdrant.PointId{qdrant.NewID(entry.ID)}})
				return err == nil && len(points) == 0
			}, 5*time.Second, 10*time.Millisecond)
			require.NoError(t, store.Add(ctx, namespace, uuid.NewString(), []float32{1, 0, 0}, entry.Properties))
			other := maps.Clone(entry.Properties)
			other["cache_key"] = "scope-b"
			otherID := uuid.NewString()
			require.NoError(t, store.Add(ctx, namespace, otherID, []float32{1, 0, 0}, other))
			require.NoError(t, plugin.ClearCacheForKey("scope-a"))
			require.Eventually(t, func() bool {
				remaining, _, err := store.GetAll(ctx, namespace, nil, nil, nil, 10)
				return err == nil && len(remaining) == 1 && remaining[0].ID == otherID
			}, 5*time.Second, 10*time.Millisecond)
			assertSchema("operator_note")
		})
	}
}
