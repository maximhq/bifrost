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

	basePrompt := "What is the capital of France?"

	tests := []struct {
		name        string
		request1    *schemas.BifrostChatRequest
		request2    *schemas.BifrostChatRequest
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
			// Create a fresh context for each subtest to avoid context pollution
			ctx := CreateContextWithCacheKey("param-variations-test")

			// Clear cache for this subtest
			clearTestKeysWithStore(t, setup.Store)

			// Make first request
			_, err1 := setup.Client.ChatCompletionRequest(ctx, tt.request1)
			if err1 != nil {
				return // Test will be skipped by retry function
			}

			WaitForCache()

			// Make second request
			response2, err2 := setup.Client.ChatCompletionRequest(ctx, tt.request2)
			if err2 != nil {
				if err2.Error != nil {
					t.Fatalf("Second request failed: %v", err2.Error.Message)
				} else {
					t.Fatalf("Second request failed: %v", err2)
				}
			}

			// Check cache behavior
			if tt.shouldCache {
				AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, string(CacheTypeDirect))
			} else {
				AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2})
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
	baseRequest := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr("What's the weather like today?"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: bifrost.Ptr(100),
			Temperature:         bifrost.Ptr(0.5),
		},
	}

	// Request with tools
	requestWithTools := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr("What's the weather like today?"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: bifrost.Ptr(100),
			Temperature:         bifrost.Ptr(0.5),
			Tools: []schemas.ChatTool{
				{
					Type: schemas.ChatToolTypeFunction,
					Function: &schemas.ChatToolFunction{
						Name:        "get_weather",
						Description: bifrost.Ptr("Get the current weather"),
						Parameters: &schemas.ToolFunctionParameters{
							Type: "object",
							Properties: schemas.NewOrderedMapFromPairs(
								schemas.KV("location", map[string]interface{}{
									"type":        "string",
									"description": "The city and state",
								}),
							),
						},
						Strict: bifrost.Ptr(false),
					},
				},
			},
		},
	}

	// Request with different tools
	requestWithDifferentTools := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr("What's the weather like today?"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: bifrost.Ptr(100),
			Temperature:         bifrost.Ptr(0.5),
			Tools: []schemas.ChatTool{
				{
					Type: schemas.ChatToolTypeFunction,
					Function: &schemas.ChatToolFunction{
						Name:        "get_current_weather",
						Description: bifrost.Ptr("Get current weather information"),
						Parameters: &schemas.ToolFunctionParameters{
							Type: "object",
							Properties: schemas.NewOrderedMapFromPairs(
								schemas.KV("city", map[string]interface{}{ // Different parameter name
									"type":        "string",
									"description": "The city name",
								}),
							),
						},
						Strict: bifrost.Ptr(false),
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
		return // Test will be skipped by retry function
	}

	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2})

	WaitForCache()

	// Test 3: Same request with tools (should cache hit)
	t.Log("Making same request with tools again...")
	response3, err3 := setup.Client.ChatCompletionRequest(ctx, requestWithTools)
	if err3 != nil {
		t.Fatalf("Second request with tools failed: %v", err3)
	}

	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3}, "")

	// Test 4: Request with different tools (should NOT cache hit)
	t.Log("Making request with different tools...")
	response4, err4 := setup.Client.ChatCompletionRequest(ctx, requestWithDifferentTools)
	if err4 != nil {
		return // Test will be skipped by retry function
	}

	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response4})

	t.Log("✅ Tool variations test completed!")
}

// TestContentVariations tests caching behavior with different content types
func TestContentVariations(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	tests := []struct {
		name    string
		request *schemas.BifrostChatRequest
	}{
		{
			name: "Image URL Content",
			request: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentBlocks: []schemas.ChatContentBlock{
								{
									Type: schemas.ChatContentBlockTypeText,
									Text: bifrost.Ptr("Analyze this image"),
								},
								{
									Type: schemas.ChatContentBlockTypeImage,
									ImageURLStruct: &schemas.ChatInputImage{
										URL: "https://pub-cdead89c2f004d8f963fd34010c479d0.r2.dev/Gfp-wisconsin-madison-the-nature-boardwalk.jpg",
									},
								},
							},
						},
					},
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(200),
					Temperature:         bifrost.Ptr(0.3),
				},
			},
		},
		{
			name: "Multiple Images",
			request: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentBlocks: []schemas.ChatContentBlock{
								{
									Type: schemas.ChatContentBlockTypeText,
									Text: bifrost.Ptr("Compare these images"),
								},
								{
									Type: schemas.ChatContentBlockTypeImage,
									ImageURLStruct: &schemas.ChatInputImage{
										URL: "https://pub-cdead89c2f004d8f963fd34010c479d0.r2.dev/Gfp-wisconsin-madison-the-nature-boardwalk.jpg",
									},
								},
								{
									Type: schemas.ChatContentBlockTypeImage,
									ImageURLStruct: &schemas.ChatInputImage{
										URL: "https://upload.wikimedia.org/wikipedia/commons/b/b5/Scenery_.jpg",
									},
								},
							},
						},
					},
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(200),
					Temperature:         bifrost.Ptr(0.3),
				},
			},
		},
		{
			name: "Very Long Content",
			request: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: bifrost.Ptr(strings.Repeat("This is a very long prompt. ", 100)),
						},
					},
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(50),
					Temperature:         bifrost.Ptr(0.2),
				},
			},
		},
		{
			name: "Multi-turn Conversation",
			request: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: bifrost.Ptr("What is AI?"),
						},
					},
					{
						Role: schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{
							ContentStr: bifrost.Ptr("AI stands for Artificial Intelligence..."),
						},
					},
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: bifrost.Ptr("Can you give me examples?"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(150),
					Temperature:         bifrost.Ptr(0.5),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("Testing content variation: %s", tt.name)

			// Create a fresh context for each subtest to avoid context pollution
			ctx := CreateContextWithCacheKey("content-variations-test")

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
			AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, string(CacheTypeDirect))
			t.Logf("✅ %s content variation successful", tt.name)
		})
	}
}

// TestBoundaryParameterValues tests edge case parameter values
func TestBoundaryParameterValues(t *testing.T) {
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	tests := []struct {
		name    string
		request *schemas.BifrostChatRequest
	}{
		{
			name: "Maximum Parameter Values",
			request: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: bifrost.Ptr("Test max parameters"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(4096),
					PresencePenalty:     bifrost.Ptr(2.0),
					FrequencyPenalty:    bifrost.Ptr(2.0),
					Temperature:         bifrost.Ptr(2.0),
					TopP:                bifrost.Ptr(1.0),
				},
			},
		},
		{
			name: "Minimum Parameter Values",
			request: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: bifrost.Ptr("Test min parameters"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(1),
					PresencePenalty:     bifrost.Ptr(-2.0),
					FrequencyPenalty:    bifrost.Ptr(-2.0),
					Temperature:         bifrost.Ptr(0.0),
					TopP:                bifrost.Ptr(0.01),
				},
			},
		},
		{
			name: "Edge Case Parameters",
			request: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: []schemas.ChatMessage{
					{
						Role: schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{
							ContentStr: bifrost.Ptr("Test edge case parameters"),
						},
					},
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(1),
					User:                bifrost.Ptr("test-user-id-12345"),
					Temperature:         bifrost.Ptr(0.0),
					TopP:                bifrost.Ptr(0.1),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("Testing boundary parameters: %s", tt.name)

			// Create a fresh context for each subtest to avoid context pollution
			ctx := CreateContextWithCacheKey("boundary-params-test")

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
			// Create a fresh context for each subtest to avoid context pollution
			ctx := CreateContextWithCacheKey("semantic-edge-test")

			// Clear cache for this subtest
			clearTestKeysWithStore(t, setup.Store)

			// Make first request
			request1 := CreateBasicChatRequest(test.prompt1, 0.1, 50)
			_, err1 := setup.Client.ChatCompletionRequest(ctx, request1)
			if err1 != nil {
				return // Test will be skipped by retry function
			}

			// Wait for cache to be written
			WaitForCache()

			// Make second request with similar content
			request2 := CreateBasicChatRequest(test.prompt2, 0.1, 50) // Same parameters
			response2, err2 := setup.Client.ChatCompletionRequest(ctx, request2)
			if err2 != nil {
				if err2.Error != nil {
					t.Fatalf("Second request failed: %v", err2.Error.Message)
				} else {
					t.Fatalf("Second request failed: %v", err2)
				}
			}

			var cacheThresholdFloat float64
			var cacheSimilarityFloat float64

			// Check if semantic matching occurred
			semanticMatch := false
			if response2.ExtraFields.CacheDebug != nil && response2.ExtraFields.CacheDebug.CacheHit {
				if response2.ExtraFields.CacheDebug.HitType != nil && *response2.ExtraFields.CacheDebug.HitType == string(CacheTypeSemantic) {
					semanticMatch = true

					if response2.ExtraFields.CacheDebug.Threshold != nil {
						cacheThresholdFloat = *response2.ExtraFields.CacheDebug.Threshold
					}
					if response2.ExtraFields.CacheDebug.Similarity != nil {
						cacheSimilarityFloat = *response2.ExtraFields.CacheDebug.Similarity
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
		ctxNoKey := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		response, err := setup.Client.ChatCompletionRequest(ctxNoKey, testRequest)
		if err != nil {
			t.Errorf("Request without cache key failed: %v", err)
			return
		}

		// Should bypass cache since there's no cache key
		AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response})
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
		ctxInvalidKey := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline).WithValue(CacheKey, 12345)

		response, err := setup.Client.ChatCompletionRequest(ctxInvalidKey, testRequest)
		if err != nil {
			t.Errorf("Request with invalid cache key type failed: %v", err)
			return
		}

		// Should bypass cache due to invalid key type
		AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response})
		t.Log("✅ Request with invalid cache key type correctly bypassed cache")
	})

	t.Log("✅ Error handling edge cases completed!")
}
