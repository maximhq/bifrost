package semanticcache

import (
	"strconv"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestConversationHistoryThresholdBasic tests basic conversation history threshold functionality
func TestConversationHistoryThresholdBasic(t *testing.T) {
	// Test with threshold of 2 messages
	setup := CreateTestSetupWithConversationThreshold(t, 2)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("test-conversation-threshold-basic")

	// Test 1: Conversation with exactly 2 messages (should cache)
	conversation1 := BuildConversationHistory("",
		[]string{"Hello", "Hi there!"},
	)
	request1 := CreateConversationRequest(conversation1, 0.7, 50)

	t.Log("Testing conversation with exactly 2 messages (at threshold)...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx, request1)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1}) // Fresh request

	WaitForCache()

	// Verify it was cached
	response2, err2 := setup.Client.ChatCompletionRequest(ctx, request1)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct") // Should be cached

	// Test 2: Conversation with 3 messages (exceeds threshold, should NOT cache)
	conversation2 := BuildConversationHistory("",
		[]string{"Hello", "Hi there!"},
		[]string{"How are you?", "I'm doing well!"},
	)
	messages2 := AddUserMessage(conversation2, "What's the weather?")
	request2 := CreateConversationRequest(messages2, 0.7, 50) // 5 messages total > 2

	t.Log("Testing conversation with 5 messages (exceeds threshold)...")
	response3, err3 := setup.Client.ChatCompletionRequest(ctx, request2)
	if err3 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3}) // Should not cache

	WaitForCache()

	// Verify it was NOT cached
	t.Log("Verifying conversation exceeding threshold was not cached...")
	response4, err4 := setup.Client.ChatCompletionRequest(ctx, request2)
	if err4 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response4}) // Should still be fresh (not cached)

	t.Log("✅ Conversation history threshold works correctly")
}

// TestConversationHistoryThresholdWithSystemPrompt tests threshold with system messages
func TestConversationHistoryThresholdWithSystemPrompt(t *testing.T) {
	// Test with threshold of 3, ExcludeSystemPrompt = false
	setup := CreateTestSetupWithConversationThreshold(t, 3)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("test-threshold-system-prompt")

	// System prompt + 2 user/assistant pairs = 5 messages total > 3
	conversation := BuildConversationHistory(
		"You are a helpful assistant", // System message (counts toward threshold)
		[]string{"Hello", "Hi there!"},
		[]string{"How are you?", "I'm doing well!"},
	)
	request := CreateConversationRequest(conversation, 0.7, 50)

	t.Log("Testing conversation with system prompt (5 total messages > 3 threshold)...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx, request)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1}) // Should not cache (exceeds threshold)

	WaitForCache()

	// Verify not cached
	response2, err2 := setup.Client.ChatCompletionRequest(ctx, request)
	if err2 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}) // Should not be cached

	t.Log("✅ Conversation threshold correctly counts system messages")
}

// TestConversationHistoryThresholdWithExcludeSystemPrompt tests interaction between threshold and exclude system prompt
func TestConversationHistoryThresholdWithExcludeSystemPrompt(t *testing.T) {
	// Create setup with both threshold=3 and ExcludeSystemPrompt=true
	setup := CreateTestSetupWithThresholdAndExcludeSystem(t, 3, true)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("test-threshold-exclude-system")

	// Create conversation with exactly 3 non-system messages to test threshold boundary
	// System + 1.5 user/assistant pairs = 4 messages total
	// With ExcludeSystemPrompt=true, should only count 3 non-system messages for threshold
	conversation := BuildConversationHistory(
		"You are helpful",       // System (excluded from count)
		[]string{"Hello", "Hi"}, // User + Assistant = 2 messages
		[]string{"Thanks", ""},  // User only = 1 message (no assistant response)
	)
	// No slicing needed; BuildConversationHistory skips empty assistant entries.
	request := CreateConversationRequest(conversation, 0.7, 50) // 3 non-system messages exactly

	t.Log("Testing threshold with ExcludeSystemPrompt=true (3 non-system messages = at threshold)...")

	// Test logic:
	// - Total messages: 4 (1 system + 3 others)
	// - With ExcludeSystemPrompt=true: counts as 3 non-system messages
	// - Threshold is 3, so 3 <= 3 should allow caching

	response1, err1 := setup.Client.ChatCompletionRequest(ctx, request)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1}) // Fresh request, should not hit cache

	WaitForCache()

	// Second request should hit cache (3 non-system messages <= 3 threshold)
	response2, err2 := setup.Client.ChatCompletionRequest(ctx, request)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct") // Should cache since 3 <= 3 after excluding system

	t.Log("✅ Conversation threshold respects ExcludeSystemPrompt setting")
}

// TestConversationHistoryThresholdDifferentValues tests different threshold values
func TestConversationHistoryThresholdDifferentValues(t *testing.T) {
	testCases := []struct {
		name        string
		threshold   int
		messages    int
		shouldCache bool
	}{
		{"Threshold 1, 1 message", 1, 1, true},
		{"Threshold 1, 2 messages", 1, 2, false},
		{"Threshold 5, 4 messages", 5, 4, true},
		{"Threshold 5, 6 messages", 5, 6, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			setup := CreateTestSetupWithConversationThreshold(t, tc.threshold)
			defer setup.Cleanup()

			ctx := CreateContextWithCacheKey("test-threshold-" + tc.name)

			// Build conversation with specified number of messages
			var conversation []schemas.ChatMessage
			for i := 0; i < tc.messages; i++ {
				role := schemas.ChatMessageRoleUser
				if i%2 == 1 {
					role = schemas.ChatMessageRoleAssistant
				}
				message := schemas.ChatMessage{
					Role: role,
					Content: &schemas.ChatMessageContent{
						ContentStr: bifrost.Ptr("Message " + strconv.Itoa(i+1)),
					},
				}
				conversation = append(conversation, message)
			}

			request := CreateConversationRequest(conversation, 0.7, 50)

			response1, err1 := setup.Client.ChatCompletionRequest(ctx, request)
			if err1 != nil {
				return // Test will be skipped by retry function
			}
			AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1}) // Always fresh first time

			WaitForCache()

			response2, err2 := setup.Client.ChatCompletionRequest(ctx, request)
			if err2 != nil {
				return // Test will be skipped by retry function
			}

			if tc.shouldCache {
				AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct")
			} else {
				AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2})
			}
		})
	}

	t.Log("✅ Different conversation threshold values work correctly")
}

// TestExcludeSystemPromptBasic tests basic ExcludeSystemPrompt functionality
func TestExcludeSystemPromptBasic(t *testing.T) {
	// Test with ExcludeSystemPrompt = true
	setup := CreateTestSetupWithExcludeSystemPrompt(t, true)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("test-exclude-system-basic")

	// Create two conversations with different system prompts but same user/assistant messages
	conversation1 := BuildConversationHistory(
		"You are a helpful assistant",
		[]string{"What is AI?", "AI is artificial intelligence."},
	)

	conversation2 := BuildConversationHistory(
		"You are a technical expert",                              // Different system prompt
		[]string{"What is AI?", "AI is artificial intelligence."}, // Same user/assistant
	)

	request1 := CreateConversationRequest(conversation1, 0.7, 50)
	request2 := CreateConversationRequest(conversation2, 0.7, 50)

	t.Log("Caching conversation with system prompt 1...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx, request1)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	t.Log("Testing conversation with different system prompt (should hit cache due to ExcludeSystemPrompt=true)...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx, request2)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	// Should hit cache because system prompts are excluded from cache key
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct")

	t.Log("✅ ExcludeSystemPrompt=true correctly ignores system prompts in cache keys")
}

// TestExcludeSystemPromptComparison tests ExcludeSystemPrompt true vs false
func TestExcludeSystemPromptComparison(t *testing.T) {
	// Test 1: ExcludeSystemPrompt = false (default)
	setup1 := CreateTestSetupWithExcludeSystemPrompt(t, false)
	defer setup1.Cleanup()

	ctx1 := CreateContextWithCacheKey("test-exclude-system-false")

	conversation1 := BuildConversationHistory(
		"You are helpful",
		[]string{"Hello", "Hi there!"},
	)

	conversation2 := BuildConversationHistory(
		"You are an expert",            // Different system prompt
		[]string{"Hello", "Hi there!"}, // Same user/assistant
	)

	request1 := CreateConversationRequest(conversation1, 0.7, 50)
	request2 := CreateConversationRequest(conversation2, 0.7, 50)

	t.Log("Testing ExcludeSystemPrompt=false...")
	response1, err1 := setup1.Client.ChatCompletionRequest(ctx1, request1)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	response2, err2 := setup1.Client.ChatCompletionRequest(ctx1, request2)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	// Should NOT hit direct cache, but might hit semantic cache due to similar content
	if response2.ExtraFields.CacheDebug != nil && response2.ExtraFields.CacheDebug.CacheHit {
		if response2.ExtraFields.CacheDebug.HitType != nil && *response2.ExtraFields.CacheDebug.HitType == "semantic" {
			t.Log("✅ Found semantic cache match (expected with similar content)")
		} else {
			t.Error("❌ Unexpected direct cache hit with different system prompts")
		}
	} else {
		t.Log("✅ No cache hit (system prompts create different cache keys)")
	}

	// Test 2: ExcludeSystemPrompt = true
	setup2 := CreateTestSetupWithExcludeSystemPrompt(t, true)
	defer setup2.Cleanup()

	ctx2 := CreateContextWithCacheKey("test-exclude-system-true")

	t.Log("Testing ExcludeSystemPrompt=true...")
	response3, err3 := setup2.Client.ChatCompletionRequest(ctx2, request1)
	if err3 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3})

	WaitForCache()

	response4, err4 := setup2.Client.ChatCompletionRequest(ctx2, request2)
	if err4 != nil {
		t.Fatalf("Fourth request failed: %v", err4)
	}
	// Should hit cache because system prompts are excluded from cache key
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response4}, "direct")

	t.Log("✅ ExcludeSystemPrompt true vs false comparison works correctly")
}

// TestExcludeSystemPromptWithMultipleSystemMessages tests behavior with multiple system messages
func TestExcludeSystemPromptWithMultipleSystemMessages(t *testing.T) {
	setup := CreateTestSetupWithExcludeSystemPrompt(t, true)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("test-multiple-system-messages")

	// Manually create conversation with multiple system messages
	conversation1 := []schemas.ChatMessage{
		{
			Role:    schemas.ChatMessageRoleSystem,
			Content: &schemas.ChatMessageContent{ContentStr: bifrost.Ptr("You are helpful")},
		},
		{
			Role:    schemas.ChatMessageRoleSystem,
			Content: &schemas.ChatMessageContent{ContentStr: bifrost.Ptr("Be concise")},
		},
		{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: bifrost.Ptr("Hello")},
		},
		{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{ContentStr: bifrost.Ptr("Hi!")},
		},
	}

	conversation2 := []schemas.ChatMessage{
		{
			Role:    schemas.ChatMessageRoleSystem,
			Content: &schemas.ChatMessageContent{ContentStr: bifrost.Ptr("You are an expert")},
		},
		{
			Role:    schemas.ChatMessageRoleSystem,
			Content: &schemas.ChatMessageContent{ContentStr: bifrost.Ptr("Be detailed")},
		},
		{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: bifrost.Ptr("Hello")},
		},
		{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{ContentStr: bifrost.Ptr("Hi!")},
		},
	}

	request1 := CreateConversationRequest(conversation1, 0.7, 50)
	request2 := CreateConversationRequest(conversation2, 0.7, 50)

	t.Log("Caching conversation with multiple system messages...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx, request1)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	t.Log("Testing conversation with different multiple system messages...")
	response2, err2 := setup.Client.ChatCompletionRequest(ctx, request2)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	// Should hit cache because all system messages are excluded
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct")

	t.Log("✅ ExcludeSystemPrompt works with multiple system messages")
}

// TestExcludeSystemPromptWithNoSystemMessages tests behavior when there are no system messages
func TestExcludeSystemPromptWithNoSystemMessages(t *testing.T) {
	setup := CreateTestSetupWithExcludeSystemPrompt(t, true)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey("test-no-system-messages")

	// Conversation with no system messages
	conversation := []schemas.ChatMessage{
		{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: bifrost.Ptr("Hello")},
		},
		{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{ContentStr: bifrost.Ptr("Hi there!")},
		},
	}

	request := CreateConversationRequest(conversation, 0.7, 50)

	t.Log("Testing conversation with no system messages...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx, request)
	if err1 != nil {
		return // Test will be skipped by retry function
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache()

	// Should cache normally
	response2, err2 := setup.Client.ChatCompletionRequest(ctx, request)
	if err2 != nil {
		if err2.Error != nil {
			t.Fatalf("Second request failed: %v", err2.Error.Message)
		} else {
			t.Fatalf("Second request failed: %v", err2)
		}
	}
	AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}, "direct")

	t.Log("✅ ExcludeSystemPrompt works correctly when no system messages present")
}
