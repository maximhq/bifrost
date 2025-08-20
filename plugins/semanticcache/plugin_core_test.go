package semanticcache

import (
	"context"
	"os"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// TestSemanticCacheBasicFunctionality tests the core caching functionality
func TestSemanticCacheBasicFunctionality(t *testing.T) {
	setup := NewTestSetup(t, TestPrefix+"basic_")
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("test-basic-value")

	// Create test request
	testRequest := CreateBasicChatRequest(
		"What is Bifrost? Answer in one short sentence.",
		0.7,
		50,
	)

	t.Log("Making first request (should go to OpenAI and be cached)...")

	// Make first request (will go to OpenAI and be cached)
	start1 := time.Now()
	response1, err1 := setup.Client.ChatCompletionRequest(ctx, testRequest)
	duration1 := time.Since(start1)

	if err1 != nil {
		t.Fatalf("First request failed: %v", err1)
	}

	if response1 == nil || len(response1.Choices) == 0 || response1.Choices[0].Message.Content.ContentStr == nil {
		t.Fatal("First response is invalid")
	}

	t.Logf("First request completed in %v", duration1)
	t.Logf("Response: %s", *response1.Choices[0].Message.Content.ContentStr)

	// Wait for cache to be written
	WaitForCache()

	t.Log("Making second identical request (should be served from cache)...")

	// Make second identical request (should be cached)
	start2 := time.Now()
	response2, err2 := setup.Client.ChatCompletionRequest(ctx, testRequest)
	duration2 := time.Since(start2)

	if err2 != nil {
		t.Fatalf("Second request failed: %v", err2)
	}

	if response2 == nil || len(response2.Choices) == 0 || response2.Choices[0].Message.Content.ContentStr == nil {
		t.Fatal("Second response is invalid")
	}

	t.Logf("Second request completed in %v", duration2)
	t.Logf("Response: %s", *response2.Choices[0].Message.Content.ContentStr)

	// Verify cache hit
	AssertCacheHit(t, response2, string(CacheTypeDirect))

	// Performance comparison
	t.Logf("Performance Summary:")
	t.Logf("First request (OpenAI):  %v", duration1)
	t.Logf("Second request (Cache):  %v", duration2)

	if duration2 >= duration1 {
		t.Errorf("Cache request took longer than original request: cache=%v, original=%v", duration2, duration1)
	} else {
		speedup := float64(duration1) / float64(duration2)
		t.Logf("Cache speedup: %.2fx faster", speedup)

		// Assert that cache is at least 2x faster (reasonable expectation)
		if speedup < 2.0 {
			t.Errorf("Cache speedup is less than 2x: got %.2fx", speedup)
		}
	}

	// Verify responses are identical (content should be the same)
	content1 := *response1.Choices[0].Message.Content.ContentStr
	content2 := *response2.Choices[0].Message.Content.ContentStr

	if content1 != content2 {
		t.Errorf("Response content differs between cached and original:\nOriginal: %s\nCached:   %s", content1, content2)
	}

	// Verify provider information is maintained in cached response
	if response2.ExtraFields.Provider != testRequest.Provider {
		t.Errorf("Provider mismatch in cached response: expected %s, got %s",
			testRequest.Provider, response2.ExtraFields.Provider)
	}

	t.Log("✅ Basic semantic caching test completed successfully!")
}

// TestSemanticSearch tests the semantic similarity search functionality
func TestSemanticSearch(t *testing.T) {
	setup := NewTestSetup(t, TestPrefix+"semantic_")
	defer setup.Cleanup()

	// Lower threshold for more flexible matching
	setup.Config.Threshold = 0.5

	ctx := CreateContextWithCacheKey("semantic-test-value")

	// First request - this will be cached
	firstRequest := CreateBasicChatRequest(
		"What is machine learning? Explain briefly.",
		0.0, // Use 0 temperature for consistent results
		50,
	)

	t.Log("Making first request (should go to OpenAI and be cached)...")
	start1 := time.Now()
	response1, err1 := setup.Client.ChatCompletionRequest(ctx, firstRequest)
	duration1 := time.Since(start1)

	if err1 != nil {
		t.Fatalf("First request failed: %v", err1)
	}

	if response1 == nil || len(response1.Choices) == 0 || response1.Choices[0].Message.Content.ContentStr == nil {
		t.Fatal("First response is invalid")
	}

	t.Logf("First request completed in %v", duration1)
	t.Logf("Response: %s", *response1.Choices[0].Message.Content.ContentStr)

	// Wait for cache to be written (async PostHook needs time to complete)
	WaitForCache()

	// Second request - very similar text to test semantic matching
	secondRequest := CreateBasicChatRequest(
		"What is machine learning? Explain it briefly.",
		0.0, // Use 0 temperature for consistent results
		50,
	)

	t.Log("Making semantically similar request (should be served from semantic cache)...")
	start2 := time.Now()
	response2, err2 := setup.Client.ChatCompletionRequest(ctx, secondRequest)
	duration2 := time.Since(start2)

	if err2 != nil {
		t.Fatalf("Second request failed: %v", err2)
	}

	if response2 == nil || len(response2.Choices) == 0 || response2.Choices[0].Message.Content.ContentStr == nil {
		t.Fatal("Second response is invalid")
	}

	t.Logf("Second request completed in %v", duration2)
	t.Logf("Response: %s", *response2.Choices[0].Message.Content.ContentStr)

	// Check if second request was served from semantic cache
	semanticMatch := false
	if response2.ExtraFields.RawResponse != nil {
		if rawMap, ok := response2.ExtraFields.RawResponse.(map[string]interface{}); ok {
			if cachedFlag, exists := rawMap["bifrost_cached"]; exists {
				if cachedBool, ok := cachedFlag.(bool); ok && cachedBool {
					// Check if it was a semantic match
					if cacheType, exists := rawMap["bifrost_cache_type"]; exists {
						if cacheTypeStr, ok := cacheType.(string); ok {
							if cacheTypeStr == string(CacheTypeSemantic) {
								semanticMatch = true
								t.Log("Second request was served from semantic cache!")
							}
						}
					}
				}
			}
		}
	}

	if !semanticMatch {
		t.Error("Semantic match expected but not found")
		return
	}

	// Performance comparison
	t.Logf("Semantic Cache Performance:")
	t.Logf("First request (OpenAI):     %v", duration1)
	t.Logf("Second request (Semantic):  %v", duration2)

	if duration2 < duration1 {
		speedup := float64(duration1) / float64(duration2)
		t.Logf("Semantic cache speedup: %.2fx faster", speedup)
	}

	t.Log("✅ Semantic search test completed successfully!")
}

// TestDirectVsSemanticSearch tests the difference between direct hash matching and semantic search
func TestDirectVsSemanticSearch(t *testing.T) {
	setup := NewTestSetup(t, TestPrefix+"direct_vs_semantic_")
	defer setup.Cleanup()

	// Lower threshold for more flexible semantic matching
	setup.Config.Threshold = 0.2

	ctx := CreateContextWithCacheKey("direct-vs-semantic-test")

	// Test Case 1: Exact same request (should use direct hash matching)
	t.Log("=== Test Case 1: Exact Same Request (Direct Hash Match) ===")

	exactRequest := CreateBasicChatRequest(
		"What is artificial intelligence?",
		0.1,
		100,
	)

	t.Log("Making first request...")
	_, err1 := setup.Client.ChatCompletionRequest(ctx, exactRequest)
	if err1 != nil {
		t.Fatalf("First request failed: %v", err1)
	}

	WaitForCache()

	t.Log("Making exact same request (should hit direct cache)...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx, exactRequest)
	if err2 != nil {
		t.Fatalf("Second request failed: %v", err2)
	}

	// Should be a direct cache hit
	AssertCacheHit(t, response2, string(CacheTypeDirect))

	// Test Case 2: Similar but different request (should use semantic search)
	t.Log("\n=== Test Case 2: Semantically Similar Request ===")

	semanticRequest := CreateBasicChatRequest(
		"Can you explain what AI is?", // Similar but different wording
		0.1,                           // Same parameters
		100,
	)

	t.Log("Making semantically similar request...")
	response3, err3 := setup.Client.ChatCompletionRequest(ctx, semanticRequest)
	if err3 != nil {
		t.Fatalf("Third request failed: %v", err3)
	}

	// Check if it was served from cache and what type
	if response3.ExtraFields.RawResponse != nil {
		if rawMap, ok := response3.ExtraFields.RawResponse.(map[string]interface{}); ok {
			if cachedFlag, exists := rawMap["bifrost_cached"]; exists {
				if cachedBool, ok := cachedFlag.(bool); ok && cachedBool {
					if cacheType, exists := rawMap["bifrost_cache_type"]; exists {
						if cacheTypeStr, ok := cacheType.(string); ok {
							t.Logf("Cache hit type: %s", cacheTypeStr)
							if cacheTypeStr == string(CacheTypeSemantic) {
								t.Log("✅ Semantic search successfully found similar request")
							} else {
								t.Log("ℹ️  Direct hash match occurred (requests were more similar than expected)")
							}
						}
					}
				}
			}
		}
	} else {
		t.Log("ℹ️  No cache hit - embeddings may not be similar enough")
	}

	t.Log("✅ Direct vs semantic search test completed!")
}

// TestNoCacheScenarios tests scenarios where caching should NOT occur
func TestNoCacheScenarios(t *testing.T) {
	setup := NewTestSetup(t, TestPrefix+"no_cache_")
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("no-cache-test")

	// Test Case 1: Different parameters should NOT cache hit
	t.Log("=== Test Case 1: Different Parameters ===")

	basePrompt := "What is the capital of France?"

	// First request
	request1 := CreateBasicChatRequest(basePrompt, 0.1, 50)
	_, err1 := setup.Client.ChatCompletionRequest(ctx, request1)
	if err1 != nil {
		t.Fatalf("First request failed: %v", err1)
	}

	WaitForCache()

	// Second request with different temperature
	request2 := CreateBasicChatRequest(basePrompt, 0.9, 50) // Different temperature
	response2, err2 := setup.Client.ChatCompletionRequest(ctx, request2)
	if err2 != nil {
		t.Fatalf("Second request failed: %v", err2)
	}

	// Should NOT be cached
	AssertNoCacheHit(t, response2)

	// Test Case 2: Different max_tokens should NOT cache hit
	t.Log("\n=== Test Case 2: Different MaxTokens ===")

	request3 := CreateBasicChatRequest(basePrompt, 0.1, 200) // Different max_tokens
	response3, err3 := setup.Client.ChatCompletionRequest(ctx, request3)
	if err3 != nil {
		t.Fatalf("Third request failed: %v", err3)
	}

	// Should NOT be cached
	AssertNoCacheHit(t, response3)

	t.Log("✅ No cache scenarios test completed!")
}

// TestCacheConfiguration tests different cache configuration options
func TestCacheConfiguration(t *testing.T) {
	tests := []struct {
		name             string
		config           Config
		expectedBehavior string
	}{
		{
			name: "High Threshold",
			config: Config{
				CacheKey:       TestCacheKey,
				Prefix:         TestPrefix + "high_threshold_",
				Provider:       schemas.OpenAI,
				EmbeddingModel: "text-embedding-3-small",
				Threshold:      0.95, // Very high threshold
				Keys: []schemas.Key{
					{Value: os.Getenv("OPENAI_API_KEY"), Models: []string{}, Weight: 1.0},
				},
			},
			expectedBehavior: "strict_matching",
		},
		{
			name: "Low Threshold",
			config: Config{
				CacheKey:       TestCacheKey,
				Prefix:         TestPrefix + "low_threshold_",
				Provider:       schemas.OpenAI,
				EmbeddingModel: "text-embedding-3-small",
				Threshold:      0.1, // Very low threshold
				Keys: []schemas.Key{
					{Value: os.Getenv("OPENAI_API_KEY"), Models: []string{}, Weight: 1.0},
				},
			},
			expectedBehavior: "loose_matching",
		},
		{
			name: "Custom TTL",
			config: Config{
				CacheKey:       TestCacheKey,
				Prefix:         TestPrefix + "custom_ttl_",
				Provider:       schemas.OpenAI,
				EmbeddingModel: "text-embedding-3-small",
				Threshold:      0.8,
				TTL:            1 * time.Hour, // Custom TTL
				Keys: []schemas.Key{
					{Value: os.Getenv("OPENAI_API_KEY"), Models: []string{}, Weight: 1.0},
				},
			},
			expectedBehavior: "custom_ttl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setup := NewTestSetupWithConfig(t, tt.config)
			defer setup.Cleanup()

			ctx := CreateContextWithCacheKey("config-test-" + tt.name)

			// Basic functionality test with the configuration
			testRequest := CreateBasicChatRequest("Test configuration: "+tt.name, 0.5, 50)

			_, err1 := setup.Client.ChatCompletionRequest(ctx, testRequest)
			if err1 != nil {
				t.Fatalf("First request failed: %v", err1)
			}

			WaitForCache()

			_, err2 := setup.Client.ChatCompletionRequest(ctx, testRequest)
			if err2 != nil {
				t.Fatalf("Second request failed: %v", err2)
			}

			t.Logf("✅ Configuration test '%s' completed", tt.name)
		})
	}
}

// MockUnsupportedStore is a mock store that returns ErrNotSupported for semantic operations
type MockUnsupportedStore struct {
	vectorstore.VectorStore // Embed interface to implement all methods
}

func (m *MockUnsupportedStore) SearchSemanticCache(ctx context.Context, indexName string, queryEmbedding []float32, metadata map[string]interface{}, threshold float64, limit int64) ([]vectorstore.SearchResult, error) {
	return nil, vectorstore.ErrNotSupported
}

func (m *MockUnsupportedStore) AddSemanticCache(ctx context.Context, key string, embedding []float32, metadata map[string]interface{}, ttl time.Duration) error {
	return vectorstore.ErrNotSupported
}

func (m *MockUnsupportedStore) EnsureSemanticIndex(ctx context.Context, indexName string, keyPrefix string, embeddingDim int, metadataFields []string) error {
	return vectorstore.ErrNotSupported
}

func (m *MockUnsupportedStore) Close(ctx context.Context) error {
	return nil
}

// TestSemanticCacheErrNotSupportedHandling tests that ErrNotSupported is handled as a soft miss
func TestSemanticCacheErrNotSupportedHandling(t *testing.T) {
	// Create mock store that returns ErrNotSupported
	mockStore := &MockUnsupportedStore{}

	// Create plugin config
	config := Config{
		CacheKey:       TestCacheKey,
		Prefix:         TestPrefix + "unsupported_",
		Provider:       schemas.OpenAI,
		EmbeddingModel: "text-embedding-3-small",
		Threshold:      0.8,
		TTL:            5 * time.Minute,
		Keys: []schemas.Key{
			{
				Value:  "test-key",
				Models: []string{},
				Weight: 1.0,
			},
		},
	}

	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)

	// Create plugin with mock store
	plugin := &Plugin{
		store:  mockStore,
		config: config,
		logger: logger,
	}

	// Create test context and request
	ctx := CreateContextWithCacheKey("test-unsupported-value")
	testRequest := CreateBasicChatRequest(
		"Test semantic search with unsupported store",
		0.7,
		50,
	)

	t.Log("Testing semantic search with ErrNotSupported store...")

	// Call performSemanticSearch which should handle ErrNotSupported gracefully
	result, err := plugin.performSemanticSearch(&ctx, testRequest, bifrost.ChatCompletionRequest)

	// Should return (nil, nil) for soft miss, not an error
	if err != nil {
		t.Errorf("Expected no error for ErrNotSupported (soft miss), but got: %v", err)
	}

	if result != nil {
		t.Errorf("Expected nil result for ErrNotSupported (soft miss), but got: %v", result)
	}

	t.Log("✅ ErrNotSupported handled correctly as soft miss (nil, nil)")
}
