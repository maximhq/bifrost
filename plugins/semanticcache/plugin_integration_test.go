package semanticcache

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestSemanticCacheBasicFlow tests the complete semantic cache flow
func TestSemanticCacheBasicFlow(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := newBaseTestContext()
	ctx.SetValue(CacheKey, keyForTest(t, "test-cache-enabled"))

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
						ContentStr: bifrost.Ptr("Hello, world!"),
					},
				},
			},
			Params: &schemas.ChatParameters{
				Temperature:         bifrost.Ptr(0.7),
				MaxCompletionTokens: bifrost.Ptr(100),
			},
		},
	}

	t.Log("Testing first request (cache miss)...")

	// First request - should be a cache miss
	modifiedReq, shortCircuit, err := setup.Plugin.PreLLMHook(ctx, request)
	if err != nil {
		t.Fatalf("PreLLMHook failed: %v", err)
	}

	if shortCircuit != nil {
		t.Fatal("Expected cache miss, but got cache hit")
	}

	if modifiedReq == nil {
		t.Fatal("Modified request is nil")
	}

	t.Log("✅ Cache miss handled correctly")

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
								ContentStr: bifrost.Ptr("Hello! How can I help you today?"),
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

	// Capture original response content for comparison
	var originalContent string
	if len(response.ChatResponse.Choices) > 0 && response.ChatResponse.Choices[0].Message.Content.ContentStr != nil {
		originalContent = *response.ChatResponse.Choices[0].Message.Content.ContentStr
	}
	if originalContent == "" {
		t.Fatal("Original response content is empty")
	}
	t.Logf("Original response content: %s", originalContent)

	// Cache the response
	t.Log("Caching response...")
	_, _, err = setup.Plugin.PostLLMHook(ctx, response, nil)
	if err != nil {
		t.Fatalf("PostLLMHook failed: %v", err)
	}

	// Wait for async caching to complete
	WaitForCache(setup.Plugin)
	t.Log("✅ Response cached successfully")

	// Second request - should be a cache hit
	t.Log("Testing second identical request (expecting cache hit)...")

	// Reset context for second request
	ctx2 := newBaseTestContext()
	ctx2.SetValue(CacheKey, keyForTest(t, "test-cache-enabled"))

	modifiedReq2, shortCircuit2, err := setup.Plugin.PreLLMHook(ctx2, request)
	if err != nil {
		t.Fatalf("Second PreLLMHook failed: %v", err)
	}

	if shortCircuit2 == nil {
		t.Fatal("expected cache hit on identical request")
		return
	}

	if shortCircuit2.Response == nil {
		t.Fatal("Cache hit but response is nil")
	}

	if modifiedReq2 == nil {
		t.Fatal("Modified request is nil on cache hit")
	}

	t.Log("✅ Cache hit detected and response returned")

	// Verify the cached response
	if len(shortCircuit2.Response.ChatResponse.Choices) == 0 {
		t.Fatal("Cached response has no choices")
	}

	cachedContent := shortCircuit2.Response.ChatResponse.Choices[0].Message.Content.ContentStr
	if cachedContent == nil || *cachedContent == "" {
		t.Fatal("Cached response content is empty")
	}

	t.Logf("✅ Cached response content: %s", *cachedContent)

	// Compare original and cached content
	cachedContentStr := *cachedContent
	// Trim whitespace and newlines for comparison
	originalContentTrimmed := strings.TrimSpace(originalContent)
	cachedContentTrimmed := strings.TrimSpace(cachedContentStr)

	if originalContentTrimmed != cachedContentTrimmed {
		t.Fatalf("❌ Content mismatch: original='%s', cached='%s'", originalContentTrimmed, cachedContentTrimmed)
	}

	t.Log("✅ Content verification passed - original and cached responses match")
	t.Log("🎉 Basic semantic cache flow test passed!")
}

// TestSemanticCacheStrictFiltering tests that the cache respects parameter differences
func TestSemanticCacheStrictFiltering(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := newBaseTestContext()
	ctx.SetValue(CacheKey, keyForTest(t, "test-cache-enabled"))

	// Base request
	baseRequest := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("What is the weather like?"),
					},
				},
			},
			Params: &schemas.ChatParameters{
				Temperature:         bifrost.Ptr(0.7),
				MaxCompletionTokens: bifrost.Ptr(100),
			},
		},
	}

	t.Log("Testing first request with temperature=0.7...")

	// First request
	_, shortCircuit1, err := setup.Plugin.PreLLMHook(ctx, baseRequest)
	if err != nil {
		t.Fatalf("First PreLLMHook failed: %v", err)
	}

	if shortCircuit1 != nil {
		t.Fatal("Expected cache miss for first request")
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
								ContentStr: bifrost.Ptr("It's sunny today!"),
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
		t.Fatalf("PostLLMHook failed: %v", err)
	}

	WaitForCache(setup.Plugin)
	t.Log("✅ First response cached")

	// Second request with different temperature - should be cache miss
	t.Log("Testing second request with temperature=0.5 (expecting cache miss)...")

	ctx2 := newBaseTestContext()
	ctx2.SetValue(CacheKey, keyForTest(t, "test-cache-enabled"))

	modifiedRequest := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("What is the weather like?"),
					},
				},
			},
			Params: &schemas.ChatParameters{
				Temperature:         bifrost.Ptr(0.5), // Different temperature
				MaxCompletionTokens: bifrost.Ptr(100),
			},
		},
	}

	_, shortCircuit2, err := setup.Plugin.PreLLMHook(ctx2, modifiedRequest)
	if err != nil {
		t.Fatalf("Second PreLLMHook failed: %v", err)
	}

	if shortCircuit2 != nil {
		t.Fatal("Expected cache miss due to different temperature, but got cache hit")
	}

	t.Log("✅ Strict filtering working - different parameters result in cache miss")

	// Third request with different model - should be cache miss
	t.Log("Testing third request with different model (expecting cache miss)...")

	ctx3 := newBaseTestContext()
	ctx3.SetValue(CacheKey, keyForTest(t, "test-cache-enabled"))

	modifiedRequest2 := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-3.5-turbo", // Different model
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("What is the weather like?"),
					},
				},
			},
			Params: &schemas.ChatParameters{
				Temperature:         bifrost.Ptr(0.7),
				MaxCompletionTokens: bifrost.Ptr(100),
			},
		},
	}

	_, shortCircuit3, err := setup.Plugin.PreLLMHook(ctx3, modifiedRequest2)
	if err != nil {
		t.Fatalf("Third PreLLMHook failed: %v", err)
	}

	if shortCircuit3 != nil {
		t.Fatal("Expected cache miss due to different model, but got cache hit")
	}

	t.Log("✅ Strict filtering working - different model results in cache miss")
	t.Log("🎉 Strict filtering test passed!")
}

// TestSemanticCacheStreamingFlow tests streaming response caching
func TestSemanticCacheStreamingFlow(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := newBaseTestContext()
	ctx.SetValue(CacheKey, keyForTest(t, "test-cache-enabled"))

	request := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionStreamRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("Tell me a short story"),
					},
				},
			},
			Params: &schemas.ChatParameters{
				Temperature: bifrost.Ptr(0.8),
			},
		},
	}

	t.Log("Testing streaming request (cache miss)...")

	// First request - should be cache miss
	_, shortCircuit, err := setup.Plugin.PreLLMHook(ctx, request)
	if err != nil {
		t.Fatalf("PreLLMHook failed: %v", err)
	}

	if shortCircuit != nil {
		t.Fatal("Expected cache miss for streaming request")
	}

	t.Log("✅ Streaming cache miss handled correctly")

	// Simulate streaming response chunks
	t.Log("Caching streaming response chunks...")

	chunks := []string{
		"Once upon a time,",
		" there was a brave",
		" knight who saved the day.",
	}

	for i, chunk := range chunks {
		var finishReason *string
		isFinal := i == len(chunks)-1
		if isFinal {
			finishReason = bifrost.Ptr("stop")
		}

		// Bifrost's stream pipeline sets this on the final chunk before
		// invoking PostLLMHook (see core/bifrost.go where it stamps
		// BifrostContextKeyStreamEndIndicator=true). The cache plugin's
		// PostLLMHook flushes the accumulator only when IsFinalChunk(ctx)
		// returns true, so a hand-rolled stream simulation must mirror
		// that — otherwise the entry is never written and the second
		// request misses.
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, isFinal)

		chunkResponse := &schemas.BifrostResponse{
			ChatResponse: &schemas.BifrostChatResponse{
				ID: uuid.New().String(),
				Choices: []schemas.BifrostResponseChoice{
					{
						Index:        i,
						FinishReason: finishReason,
						ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
							Delta: &schemas.ChatStreamResponseChoiceDelta{
								Content: bifrost.Ptr(chunk),
							},
						},
					},
				},
				ExtraFields: schemas.BifrostResponseExtraFields{
					Provider:               schemas.OpenAI,
					OriginalModelRequested: "gpt-4o-mini",
					RequestType:            schemas.ChatCompletionStreamRequest,
					ChunkIndex:             i,
				},
			},
		}

		_, _, err = setup.Plugin.PostLLMHook(ctx, chunkResponse, nil)
		if err != nil {
			t.Fatalf("PostLLMHook failed for chunk %d: %v", i, err)
		}
	}

	WaitForCache(setup.Plugin)
	t.Log("✅ Streaming response chunks cached")

	// Test cache retrieval for streaming
	t.Log("Testing streaming cache retrieval...")

	ctx2 := newBaseTestContext()
	ctx2.SetValue(CacheKey, keyForTest(t, "test-cache-enabled"))

	_, shortCircuit2, err := setup.Plugin.PreLLMHook(ctx2, request)
	if err != nil {
		t.Fatalf("Second PreLLMHook failed: %v", err)
	}

	if shortCircuit2 == nil {
		t.Fatal("expected streaming cache hit on identical second request after the first stream was fully accumulated and stored")
	}
	if shortCircuit2.Stream == nil {
		t.Fatal("Cache hit but stream is nil")
	}

	t.Log("✅ Streaming cache hit detected")

	// Read from the cached stream
	chunkCount := 0
	for chunk := range shortCircuit2.Stream {
		if chunk.BifrostChatResponse == nil {
			continue
		}
		chunkCount++
		t.Logf("Received cached chunk %d", chunkCount)
	}

	if chunkCount == 0 {
		t.Fatal("No chunks received from cached stream")
	}

	t.Logf("✅ Received %d cached chunks", chunkCount)
	t.Log("🎉 Streaming cache test passed!")
}

// TestSemanticCache_NoCacheWhenKeyMissing verifies cache is disabled when cache key is missing from context
func TestSemanticCache_NoCacheWhenKeyMissing(t *testing.T) {
	t.Parallel()
	t.Log("Testing cache behavior when cache key is missing...")

	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := newBaseTestContext()
	// Don't set the cache key - cache should be disabled

	request := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("Test message"),
					},
				},
			},
		},
	}

	_, shortCircuit, err := setup.Plugin.PreLLMHook(ctx, request)
	if err != nil {
		t.Fatalf("PreLLMHook failed: %v", err)
	}

	if shortCircuit != nil {
		t.Fatal("Expected no caching when cache key is not set, but got cache hit")
	}

	t.Log("✅ Cache properly disabled when no cache key is set")
	t.Log("🎉 No cache key test passed!")
}

// TestSemanticCache_CustomTTLHandling verifies cache respects custom TTL values from context
func TestSemanticCache_CustomTTLHandling(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Configure plugin with custom TTL key
	ctx := newBaseTestContext()
	ctx.SetValue(CacheKey, keyForTest(t, "test-cache-enabled"))
	ctx.SetValue(CacheTTLKey, 1*time.Minute) // Custom TTL

	request := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("TTL test message"),
					},
				},
			},
		},
	}

	// First request - cache miss
	_, shortCircuit, err := setup.Plugin.PreLLMHook(ctx, request)
	if err != nil {
		t.Fatalf("PreLLMHook failed: %v", err)
	}

	if shortCircuit != nil {
		t.Fatal("Expected cache miss, but got cache hit")
	}

	// Simulate response and cache it
	response := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ID: "ttl-test-response",
			Choices: []schemas.BifrostResponseChoice{
				{
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role: "assistant",
							Content: &schemas.ChatMessageContent{
								ContentStr: bifrost.Ptr("TTL test response"),
							},
						},
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
		t.Fatalf("PostLLMHook failed: %v", err)
	}

	WaitForCache(setup.Plugin)

	// Read back: a second identical request must hit AND the entry's TTL
	// must reflect the per-request override (1 minute), not the plugin
	// default (5 minutes). expires_at is exposed via cache_debug isn't
	// directly readable, but we can confirm the entry is present.
	ctx2 := newBaseTestContext()
	ctx2.SetValue(CacheKey, keyForTest(t, "test-cache-enabled"))
	ctx2.SetValue(CacheTTLKey, 1*time.Minute)
	_, sc2, err := setup.Plugin.PreLLMHook(ctx2, request)
	if err != nil {
		t.Fatalf("Second PreLLMHook failed: %v", err)
	}
	if sc2 == nil || sc2.Response == nil {
		t.Fatal("expected cache hit on second identical request with custom TTL")
	}
	if cd := sc2.Response.GetExtraFields().CacheDebug; cd == nil || !cd.CacheHit {
		t.Fatal("expected CacheDebug.CacheHit=true on hit")
	}
	t.Log("✅ Custom TTL configuration test passed (entry written and retrievable)")
}

// TestSemanticCache_CustomThresholdHandling verifies cache respects custom similarity threshold from context
func TestSemanticCache_CustomThresholdHandling(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Seed an entry with the DEFAULT threshold (0.8) so a follow-up
	// request can attempt semantic search against it.
	seedCtx := newBaseTestContext()
	seedCtx.SetValue(CacheKey, keyForTest(t, "threshold-seed"))
	seedReq := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("Threshold test message"),
					},
				},
			},
		},
	}

	_, sc1, err := setup.Plugin.PreLLMHook(seedCtx, seedReq)
	if err != nil {
		t.Fatalf("seed PreLLMHook failed: %v", err)
	}
	if sc1 != nil {
		t.Fatal("Expected initial cache miss")
	}
	seedRes := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ID: "threshold-test",
			Choices: []schemas.BifrostResponseChoice{{
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role:    "assistant",
						Content: &schemas.ChatMessageContent{ContentStr: bifrost.Ptr("seed response")},
					},
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider: schemas.OpenAI, OriginalModelRequested: "gpt-4o-mini", RequestType: schemas.ChatCompletionRequest,
			},
		},
	}
	if _, _, err := setup.Plugin.PostLLMHook(seedCtx, seedRes, nil); err != nil {
		t.Fatalf("seed PostLLMHook failed: %v", err)
	}
	WaitForCache(setup.Plugin)

	// Identical-content request with a HIGH threshold (0.95) MUST still hit
	// via the direct path (direct hashing ignores threshold). Threshold only
	// gates semantic search; a same-input request matches the deterministic
	// directCacheID regardless. This proves the override doesn't break direct.
	hitCtx := newBaseTestContext()
	hitCtx.SetValue(CacheKey, keyForTest(t, "threshold-seed"))
	hitCtx.SetValue(CacheThresholdKey, 0.95)
	_, sc2, err := setup.Plugin.PreLLMHook(hitCtx, seedReq)
	if err != nil {
		t.Fatalf("hit PreLLMHook failed: %v", err)
	}
	if sc2 == nil || sc2.Response == nil {
		t.Fatal("expected direct cache hit even with high threshold (direct ignores threshold)")
	}
	if cd := sc2.Response.GetExtraFields().CacheDebug; cd == nil || cd.HitType == nil || *cd.HitType != string(CacheTypeDirect) {
		t.Fatalf("expected hit_type=direct, got cache_debug=%+v", cd)
	}
	t.Log("✅ Custom threshold override tracked through PreLLMHook without breaking direct path")
}

// TestSemanticCache_ProviderModelCachingFlags verifies cache behavior with provider/model caching flags
func TestSemanticCache_ProviderModelCachingFlags(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Test with provider/model caching disabled
	setup.Config.CacheByProvider = bifrost.Ptr(false)
	setup.Config.CacheByModel = bifrost.Ptr(false)

	ctx := newBaseTestContext()
	ctx.SetValue(CacheKey, keyForTest(t, "test-cache-enabled"))

	request1 := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("Provider model flags test"),
					},
				},
			},
		},
	}

	// First request with OpenAI
	_, shortCircuit1, err := setup.Plugin.PreLLMHook(ctx, request1)
	if err != nil {
		t.Fatalf("PreLLMHook failed: %v", err)
	}

	if shortCircuit1 != nil {
		t.Fatal("Expected cache miss, but got cache hit")
	}

	// Cache the response
	response := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ID: "provider-model-test",
			Choices: []schemas.BifrostResponseChoice{
				{
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role: "assistant",
							Content: &schemas.ChatMessageContent{
								ContentStr: bifrost.Ptr("Provider model test response"),
							},
						},
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
		t.Fatalf("PostLLMHook failed: %v", err)
	}

	WaitForCache(setup.Plugin)

	// Second request with different provider - should potentially hit cache since provider is not considered
	request2 := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.Anthropic, // Different provider
			Model:    "claude-3-haiku",  // Different model
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("Provider model flags test"), // Same content
					},
				},
			},
		},
	}

	ctx2 := newBaseTestContext()
	ctx2.SetValue(CacheKey, keyForTest(t, "test-cache-enabled"))

	_, shortCircuit2, err := setup.Plugin.PreLLMHook(ctx2, request2)
	if err != nil {
		t.Fatalf("Second PreLLMHook failed: %v", err)
	}

	// CacheByProvider=false + CacheByModel=false means provider and model are
	// stripped from the directCacheID input. Same content + same cache_key
	// must produce the SAME directCacheID, so the second request MUST hit
	// even though it specifies a completely different provider/model.
	if shortCircuit2 == nil || shortCircuit2.Response == nil {
		t.Fatal("expected cache hit across providers/models when CacheByProvider+CacheByModel=false")
	}
	if cd := shortCircuit2.Response.GetExtraFields().CacheDebug; cd == nil || !cd.CacheHit {
		t.Fatalf("expected CacheDebug.CacheHit=true, got %+v", cd)
	}
	t.Log("✅ CacheByProvider=false + CacheByModel=false correctly shares entries across providers/models")
}

// TestSemanticCache_ConfigurationEdgeCases verifies edge cases in configuration handling
func TestSemanticCache_ConfigurationEdgeCases(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	// Test with invalid TTL type in context
	ctx := newBaseTestContext()
	ctx.SetValue(CacheKey, keyForTest(t, "test-cache-enabled"))
	ctx.SetValue(CacheTTLKey, "not-a-duration") // Invalid TTL type

	request := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o-mini",
			Input: []schemas.ChatMessage{
				{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("Edge case test"),
					},
				},
			},
		},
	}

	// Should handle invalid TTL gracefully
	_, shortCircuit, err := setup.Plugin.PreLLMHook(ctx, request)
	if err != nil {
		t.Fatalf("PreLLMHook failed with invalid TTL: %v", err)
	}
	if shortCircuit != nil {
		t.Fatal("Unexpected cache hit with invalid TTL")
	}

	// Plugin must FALL BACK to its default TTL — verify by writing then
	// reading the entry. If the invalid TTL caused caching to silently
	// disable, the second request would miss.
	res := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ID: "edge-ttl",
			Choices: []schemas.BifrostResponseChoice{{
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{Role: "assistant", Content: &schemas.ChatMessageContent{ContentStr: bifrost.Ptr("ok")}},
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{Provider: schemas.OpenAI, OriginalModelRequested: "gpt-4o-mini", RequestType: schemas.ChatCompletionRequest},
		},
	}
	if _, _, err := setup.Plugin.PostLLMHook(ctx, res, nil); err != nil {
		t.Fatalf("PostLLMHook failed: %v", err)
	}
	WaitForCache(setup.Plugin)

	ctxRead := newBaseTestContext()
	ctxRead.SetValue(CacheKey, keyForTest(t, "test-cache-enabled"))
	ctxRead.SetValue(CacheTTLKey, "not-a-duration")
	if _, sc, err := setup.Plugin.PreLLMHook(ctxRead, request); err != nil {
		t.Fatalf("read PreLLMHook failed: %v", err)
	} else if sc == nil {
		t.Fatal("expected cache hit — invalid TTL should have fallen back to default and entry should be retrievable")
	}

	// Test with invalid threshold type — same expectation: fallback works.
	ctx2 := newBaseTestContext()
	ctx2.SetValue(CacheKey, keyForTest(t, "test-cache-threshold-edge"))
	ctx2.SetValue(CacheThresholdKey, "not-a-float")

	_, sc2, err := setup.Plugin.PreLLMHook(ctx2, request)
	if err != nil {
		t.Fatalf("PreLLMHook failed with invalid threshold: %v", err)
	}
	if sc2 != nil {
		t.Fatal("Unexpected cache hit on first call with invalid threshold")
	}
	if _, _, err := setup.Plugin.PostLLMHook(ctx2, res, nil); err != nil {
		t.Fatalf("PostLLMHook failed: %v", err)
	}
	WaitForCache(setup.Plugin)

	ctx2Read := newBaseTestContext()
	ctx2Read.SetValue(CacheKey, keyForTest(t, "test-cache-threshold-edge"))
	ctx2Read.SetValue(CacheThresholdKey, "still-not-a-float")
	if _, sc, err := setup.Plugin.PreLLMHook(ctx2Read, request); err != nil {
		t.Fatalf("threshold read PreLLMHook failed: %v", err)
	} else if sc == nil {
		t.Fatal("expected cache hit — invalid threshold should have fallen back to default")
	}

	t.Log("✅ Configuration edge cases test passed (invalid TTL/threshold fall back gracefully)")
}
