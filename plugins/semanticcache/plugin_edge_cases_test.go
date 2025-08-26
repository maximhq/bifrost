package semanticcache

import (
	"context"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestParameterVariations tests that different parameters don't cache hit inappropriately
func TestParameterVariations(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("param-variations-test")
	basePrompt := "What is the capital of France?"

	tests := []struct {
		name        string
		request1    *schemas.BifrostRequest
		request2    *schemas.BifrostRequest
		shouldCache bool
	}{
		{
			name:        "Same Parameters",
			request1:    CreateBasicChatRequest(basePrompt, 0.5, 50),
			request2:    CreateBasicChatRequest(basePrompt, 0.5, 50),
			shouldCache: true,
		},
		{
			name:        "Different Temperature",
			request1:    CreateBasicChatRequest(basePrompt, 0.1, 50),
			request2:    CreateBasicChatRequest(basePrompt, 0.9, 50),
			shouldCache: false,
		},
		{
			name:        "Different MaxTokens",
			request1:    CreateBasicChatRequest(basePrompt, 0.5, 50),
			request2:    CreateBasicChatRequest(basePrompt, 0.5, 200),
			shouldCache: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear cache for this subtest
			clearTestKeysWithStore(t, setup.Store)

			// Make first request
			_, err1 := setup.Client.ChatCompletionRequest(ctx, tt.request1)
			if err1 != nil {
				t.Fatalf("First request failed: %v", err1)
			}

			WaitForCache()

			// Make second request
			response2, err2 := setup.Client.ChatCompletionRequest(ctx, tt.request2)
			if err2 != nil {
				t.Fatalf("Second request failed: %v", err2)
			}

			// Check cache behavior
			if tt.shouldCache {
				AssertCacheHit(t, response2, string(CacheTypeDirect))
			} else {
				AssertNoCacheHit(t, response2)
			}
		})
	}
}

// TestToolVariations tests caching behavior with different tool configurations
func TestToolVariations(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("tool-variations-test")

	// Base request without tools
	baseRequest := &schemas.BifrostRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: schemas.RequestInput{
			ChatCompletionInput: &[]schemas.BifrostMessage{
				{
					Role: "user",
					Content: schemas.MessageContent{
						ContentStr: bifrost.Ptr("What's the weather like today?"),
					},
				},
			},
		},
		Params: &schemas.ModelParameters{
			Temperature: bifrost.Ptr(0.5),
			MaxTokens:   bifrost.Ptr(100),
		},
	}

	// Request with tools
	requestWithTools := &schemas.BifrostRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: schemas.RequestInput{
			ChatCompletionInput: &[]schemas.BifrostMessage{
				{
					Role: "user",
					Content: schemas.MessageContent{
						ContentStr: bifrost.Ptr("What's the weather like today?"),
					},
				},
			},
		},
		Params: &schemas.ModelParameters{
			Temperature: bifrost.Ptr(0.5),
			MaxTokens:   bifrost.Ptr(100),
			Tools: &[]schemas.Tool{
				{
					Type: "function",
					Function: schemas.Function{
						Name:        "get_weather",
						Description: "Get the current weather",
						Parameters: schemas.FunctionParameters{
							Type: "object",
							Properties: map[string]interface{}{
								"location": map[string]interface{}{
									"type":        "string",
									"description": "The city and state",
								},
							},
						},
					},
				},
			},
		},
	}

	// Request with different tools
	requestWithDifferentTools := &schemas.BifrostRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: schemas.RequestInput{
			ChatCompletionInput: &[]schemas.BifrostMessage{
				{
					Role: "user",
					Content: schemas.MessageContent{
						ContentStr: bifrost.Ptr("What's the weather like today?"),
					},
				},
			},
		},
		Params: &schemas.ModelParameters{
			Temperature: bifrost.Ptr(0.5),
			MaxTokens:   bifrost.Ptr(100),
			Tools: &[]schemas.Tool{
				{
					Type: "function",
					Function: schemas.Function{
						Name:        "get_current_weather", // Different name
						Description: "Get current weather information",
						Parameters: schemas.FunctionParameters{
							Type: "object",
							Properties: map[string]interface{}{
								"city": map[string]interface{}{ // Different parameter name
									"type":        "string",
									"description": "The city name",
								},
							},
						},
					},
				},
			},
		},
	}

	// Test 1: Request without tools
	t.Log("Making request without tools...")
	_, err1 := setup.Client.ChatCompletionRequest(ctx, baseRequest)
	if err1 != nil {
		t.Fatalf("Request without tools failed: %v", err1)
	}

	WaitForCache()

	// Test 2: Request with tools (should NOT cache hit)
	t.Log("Making request with tools...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx, requestWithTools)
	if err2 != nil {
		t.Fatalf("Request with tools failed: %v", err2)
	}

	AssertNoCacheHit(t, response2)

	WaitForCache()

	// Test 3: Same request with tools (should cache hit)
	t.Log("Making same request with tools again...")
	response3, err3 := setup.Client.ChatCompletionRequest(ctx, requestWithTools)
	if err3 != nil {
		t.Fatalf("Second request with tools failed: %v", err3)
	}

	AssertCacheHit(t, response3, string(CacheTypeDirect))

	// Test 4: Request with different tools (should NOT cache hit)
	t.Log("Making request with different tools...")
	response4, err4 := setup.Client.ChatCompletionRequest(ctx, requestWithDifferentTools)
	if err4 != nil {
		t.Fatalf("Request with different tools failed: %v", err4)
	}

	AssertNoCacheHit(t, response4)

	t.Log("✅ Tool variations test completed!")
}

// TestContentVariations tests caching behavior with different content types
func TestContentVariations(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("content-variations-test")

	tests := []struct {
		name    string
		request *schemas.BifrostRequest
	}{
		{
			name: "Unicode Content",
			request: &schemas.BifrostRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: schemas.RequestInput{
					ChatCompletionInput: &[]schemas.BifrostMessage{
						{
							Role: "user",
							Content: schemas.MessageContent{
								ContentStr: bifrost.Ptr("🌟 Unicode test: Hello, 世界! مرحبا 🌍"),
							},
						},
					},
				},
				Params: &schemas.ModelParameters{
					Temperature: bifrost.Ptr(0.1),
					MaxTokens:   bifrost.Ptr(50),
				},
			},
		},
		{
			name: "Image URL Content",
			request: &schemas.BifrostRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: schemas.RequestInput{
					ChatCompletionInput: &[]schemas.BifrostMessage{
						{
							Role: "user",
							Content: schemas.MessageContent{
								ContentBlocks: &[]schemas.ContentBlock{
									{
										Type: "text",
										Text: bifrost.Ptr("Analyze this image"),
									},
									{
										Type: "image_url",
										ImageURL: &schemas.ImageURLStruct{
											URL: "https://upload.wikimedia.org/wikipedia/commons/thumb/d/dd/Gfp-wisconsin-madison-the-nature-boardwalk.jpg/2560px-Gfp-wisconsin-madison-the-nature-boardwalk.jpg",
										},
									},
								},
							},
						},
					},
				},
				Params: &schemas.ModelParameters{
					Temperature: bifrost.Ptr(0.3),
					MaxTokens:   bifrost.Ptr(200),
				},
			},
		},
		{
			name: "Multiple Images",
			request: &schemas.BifrostRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: schemas.RequestInput{
					ChatCompletionInput: &[]schemas.BifrostMessage{
						{
							Role: "user",
							Content: schemas.MessageContent{
								ContentBlocks: &[]schemas.ContentBlock{
									{
										Type: "text",
										Text: bifrost.Ptr("Compare these images"),
									},
									{
										Type: "image_url",
										ImageURL: &schemas.ImageURLStruct{
											URL: "https://upload.wikimedia.org/wikipedia/commons/thumb/d/dd/Gfp-wisconsin-madison-the-nature-boardwalk.jpg/2560px-Gfp-wisconsin-madison-the-nature-boardwalk.jpg",
										},
									},
									{
										Type: "image_url",
										ImageURL: &schemas.ImageURLStruct{
											URL: "https://upload.wikimedia.org/wikipedia/commons/b/b5/Scenery_.jpg",
										},
									},
								},
							},
						},
					},
				},
				Params: &schemas.ModelParameters{
					Temperature: bifrost.Ptr(0.3),
					MaxTokens:   bifrost.Ptr(200),
				},
			},
		},
		{
			name: "Very Long Content",
			request: &schemas.BifrostRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: schemas.RequestInput{
					ChatCompletionInput: &[]schemas.BifrostMessage{
						{
							Role: "user",
							Content: schemas.MessageContent{
								ContentStr: bifrost.Ptr(strings.Repeat("This is a very long prompt. ", 100)),
							},
						},
					},
				},
				Params: &schemas.ModelParameters{
					Temperature: bifrost.Ptr(0.2),
					MaxTokens:   bifrost.Ptr(50),
				},
			},
		},
		{
			name: "Multi-turn Conversation",
			request: &schemas.BifrostRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: schemas.RequestInput{
					ChatCompletionInput: &[]schemas.BifrostMessage{
						{
							Role: "user",
							Content: schemas.MessageContent{
								ContentStr: bifrost.Ptr("What is AI?"),
							},
						},
						{
							Role: "assistant",
							Content: schemas.MessageContent{
								ContentStr: bifrost.Ptr("AI stands for Artificial Intelligence..."),
							},
						},
						{
							Role: "user",
							Content: schemas.MessageContent{
								ContentStr: bifrost.Ptr("Can you give me examples?"),
							},
						},
					},
				},
				Params: &schemas.ModelParameters{
					Temperature: bifrost.Ptr(0.5),
					MaxTokens:   bifrost.Ptr(150),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("Testing content variation: %s", tt.name)

			// Make first request
			_, err1 := setup.Client.ChatCompletionRequest(ctx, tt.request)
			if err1 != nil {
				t.Logf("⚠️  First %s request failed: %v", tt.name, err1)
				return // Skip this test case
			}

			WaitForCache()

			// Make second identical request
			response2, err2 := setup.Client.ChatCompletionRequest(ctx, tt.request)
			if err2 != nil {
				t.Fatalf("Second %s request failed: %v", tt.name, err2)
			}

			// Should be cached
			AssertCacheHit(t, response2, string(CacheTypeDirect))
			t.Logf("✅ %s content variation successful", tt.name)
		})
	}
}

// TestBoundaryParameterValues tests edge case parameter values
func TestBoundaryParameterValues(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("boundary-params-test")

	tests := []struct {
		name    string
		request *schemas.BifrostRequest
	}{
		{
			name: "Maximum Parameter Values",
			request: &schemas.BifrostRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: schemas.RequestInput{
					ChatCompletionInput: &[]schemas.BifrostMessage{
						{
							Role: "user",
							Content: schemas.MessageContent{
								ContentStr: bifrost.Ptr("Test max parameters"),
							},
						},
					},
				},
				Params: &schemas.ModelParameters{
					Temperature:      bifrost.Ptr(2.0),
					MaxTokens:        bifrost.Ptr(4096),
					TopP:             bifrost.Ptr(1.0),
					PresencePenalty:  bifrost.Ptr(2.0),
					FrequencyPenalty: bifrost.Ptr(2.0),
				},
			},
		},
		{
			name: "Minimum Parameter Values",
			request: &schemas.BifrostRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: schemas.RequestInput{
					ChatCompletionInput: &[]schemas.BifrostMessage{
						{
							Role: "user",
							Content: schemas.MessageContent{
								ContentStr: bifrost.Ptr("Test min parameters"),
							},
						},
					},
				},
				Params: &schemas.ModelParameters{
					Temperature:      bifrost.Ptr(0.0),
					MaxTokens:        bifrost.Ptr(1),
					TopP:             bifrost.Ptr(0.01),
					PresencePenalty:  bifrost.Ptr(-2.0),
					FrequencyPenalty: bifrost.Ptr(-2.0),
				},
			},
		},
		{
			name: "Edge Case Parameters",
			request: &schemas.BifrostRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: schemas.RequestInput{
					ChatCompletionInput: &[]schemas.BifrostMessage{
						{
							Role: "user",
							Content: schemas.MessageContent{
								ContentStr: bifrost.Ptr("Test edge case parameters"),
							},
						},
					},
				},
				Params: &schemas.ModelParameters{
					Temperature:   bifrost.Ptr(0.0),
					MaxTokens:     bifrost.Ptr(1),
					TopP:          bifrost.Ptr(0.1),
					StopSequences: &[]string{"STOP", "END", "HALT"},
					User:          bifrost.Ptr("test-user-id-12345"),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("Testing boundary parameters: %s", tt.name)

			_, err := setup.Client.ChatCompletionRequest(ctx, tt.request)
			if err != nil {
				t.Logf("⚠️  %s request failed (may be expected): %v", tt.name, err)
			} else {
				t.Logf("✅ %s handled gracefully", tt.name)
			}
		})
	}
}

// TestSemanticSimilarityEdgeCases tests edge cases in semantic similarity matching
func TestSemanticSimilarityEdgeCases(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	setup.Config.Threshold = 0.9

	ctx := CreateContextWithCacheKey("semantic-edge-test")

	// Test case: Similar questions with different wording
	similarTests := []struct {
		prompt1     string
		prompt2     string
		shouldMatch bool
		description string
	}{
		{
			prompt1:     "What is machine learning?",
			prompt2:     "Can you explain machine learning?",
			shouldMatch: true,
			description: "Similar questions about ML",
		},
		{
			prompt1:     "How does AI work?",
			prompt2:     "Explain artificial intelligence",
			shouldMatch: true,
			description: "AI-related questions",
		},
		{
			prompt1:     "What is the weather today?",
			prompt2:     "What do you know about bifrost?",
			shouldMatch: false,
			description: "Completely different topics",
		},
		{
			prompt1:     "Hello, how are you?",
			prompt2:     "Hi, how are you doing?",
			shouldMatch: true,
			description: "Similar greetings",
		},
	}

	for i, test := range similarTests {
		t.Run(test.description, func(t *testing.T) {
			// Clear cache for this subtest
			clearTestKeysWithStore(t, setup.Store)

			// Make first request
			request1 := CreateBasicChatRequest(test.prompt1, 0.1, 50)
			_, err1 := setup.Client.ChatCompletionRequest(ctx, request1)
			if err1 != nil {
				t.Fatalf("First request failed: %v", err1)
			}

			// Wait for cache to be written
			WaitForCache()

			// Make second request with similar content
			request2 := CreateBasicChatRequest(test.prompt2, 0.1, 50) // Same parameters
			response2, err2 := setup.Client.ChatCompletionRequest(ctx, request2)
			if err2 != nil {
				t.Fatalf("Second request failed: %v", err2)
			}

			var cacheThresholdFloat float64
			var cacheSimilarityFloat float64

			// Check if semantic matching occurred
			semanticMatch := false
			if response2.ExtraFields.CacheDebug != nil {
				if response2.ExtraFields.CacheDebug.CacheHit {
					if response2.ExtraFields.CacheDebug.CacheHitType == string(CacheTypeSemantic) {
						semanticMatch = true

						if response2.ExtraFields.CacheDebug.CacheThreshold != nil {
							cacheThresholdFloat = *response2.ExtraFields.CacheDebug.CacheThreshold
						}
						if response2.ExtraFields.CacheDebug.CacheSimilarity != nil {
							cacheSimilarityFloat = *response2.ExtraFields.CacheDebug.CacheSimilarity
						}
					}
				}
			}

			if test.shouldMatch {
				if semanticMatch {
					t.Logf("✅ Test %d: Semantic match found as expected for '%s'", i+1, test.description)
				} else {
					t.Logf("ℹ️  Test %d: No semantic match found for '%s', check with threshold: %f and found similarity: %f", i+1, test.description, cacheThresholdFloat, cacheSimilarityFloat)
				}
			} else {
				if semanticMatch {
					t.Errorf("❌ Test %d: Unexpected semantic match for different topics: '%s', check with threshold: %f and found similarity: %f", i+1, test.description, cacheThresholdFloat, cacheSimilarityFloat)
				} else {
					t.Logf("✅ Test %d: Correctly no semantic match for different topics: '%s'", i+1, test.description)
				}
			}
		})
	}
}

// TestErrorHandlingEdgeCases tests various error scenarios
func TestErrorHandlingEdgeCases(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	testRequest := CreateBasicChatRequest("Test error handling scenarios", 0.5, 50)

	// Test without cache key (should not crash and bypass cache)
	t.Run("Request without cache key", func(t *testing.T) {
		ctxNoKey := context.Background() // No cache key

		response, err := setup.Client.ChatCompletionRequest(ctxNoKey, testRequest)
		if err != nil {
			t.Errorf("Request without cache key failed: %v", err)
			return
		}

		// Should bypass cache since there's no cache key
		AssertNoCacheHit(t, response)
		t.Log("✅ Request without cache key correctly bypassed cache")
	})

	// Test with invalid cache key type
	t.Run("Request with invalid cache key type", func(t *testing.T) {
		// First establish a cached response with valid context
		validCtx := CreateContextWithCacheKey("error-handling-test")
		_, err := setup.Client.ChatCompletionRequest(validCtx, testRequest)
		if err != nil {
			t.Fatalf("First request with valid cache key failed: %v", err)
		}

		WaitForCache()

		// Now test with invalid key type - should bypass cache
		ctxInvalidKey := context.WithValue(context.Background(), ContextKey(TestCacheKey), 12345) // Wrong type (int instead of string)

		response, err := setup.Client.ChatCompletionRequest(ctxInvalidKey, testRequest)
		if err != nil {
			t.Errorf("Request with invalid cache key type failed: %v", err)
			return
		}

		// Should bypass cache due to invalid key type
		AssertNoCacheHit(t, response)
		t.Log("✅ Request with invalid cache key type correctly bypassed cache")
	})

	t.Log("✅ Error handling edge cases completed!")
}
