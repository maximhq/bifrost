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

	ctx := CreateContextWithCacheKey(t, "test-conversation-threshold-basic")

	// Test 1: Conversation with exactly 2 messages (should cache)
	conversation1 := BuildConversationHistory("",
		[]string{"Hello", "Hi there!"},
	)
	request1 := CreateConversationRequest(conversation1, 0.7, 50)

	t.Log("Testing conversation with exactly 2 messages (at threshold)...")
	response1, err1 := setup.Client.ChatCompletionRequest(ctx, request1)
	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1}) // Fresh request

	WaitForCache(setup.Plugin)

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
		t.Skipf("upstream request error, skipping test: %v", err3)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3}) // Should not cache

	WaitForCache(setup.Plugin)

	// Verify it was NOT cached. The first call already succeeded, so any error
	// here is a real regression rather than upstream flakiness — fail fast.
	t.Log("Verifying conversation exceeding threshold was not cached...")
	response4, err4 := setup.Client.ChatCompletionRequest(ctx, request2)
	if err4 != nil {
		t.Fatalf("verification request failed: %v", err4)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response4}) // Should still be fresh (not cached)

	t.Log("✅ Conversation history threshold works correctly")
}

// TestConversationHistoryThresholdWithSystemPrompt tests threshold with system messages
func TestConversationHistoryThresholdWithSystemPrompt(t *testing.T) {
	// Test with threshold of 3, ExcludeSystemPrompt = false
	setup := CreateTestSetupWithConversationThreshold(t, 3)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey(t, "test-threshold-system-prompt")

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
		t.Skipf("upstream request error, skipping test: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1}) // Should not cache (exceeds threshold)

	WaitForCache(setup.Plugin)

	// Verify not cached. First call already succeeded, so failures here
	// indicate a real regression rather than upstream flakiness.
	response2, err2 := setup.Client.ChatCompletionRequest(ctx, request)
	if err2 != nil {
		t.Fatalf("verification request failed: %v", err2)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response2}) // Should not be cached

	t.Log("✅ Conversation threshold correctly counts system messages")
}

// TestConversationHistoryThresholdWithExcludeSystemPrompt tests interaction between threshold and exclude system prompt
func TestConversationHistoryThresholdWithExcludeSystemPrompt(t *testing.T) {
	// Create setup with both threshold=3 and ExcludeSystemPrompt=true
	setup := CreateTestSetupWithThresholdAndExcludeSystem(t, 3, true)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey(t, "test-threshold-exclude-system")

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
		t.Skipf("upstream request error, skipping test: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1}) // Fresh request, should not hit cache

	WaitForCache(setup.Plugin)

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

			ctx := CreateContextWithCacheKey(t, "test-threshold-"+tc.name)

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
				t.Skipf("upstream request error, skipping test: %v", err1)
			}
			AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1}) // Always fresh first time

			WaitForCache(setup.Plugin)

			response2, err2 := setup.Client.ChatCompletionRequest(ctx, request)
			if err2 != nil {
				t.Fatalf("verification request failed: %v", err2)
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

	ctx := CreateContextWithCacheKey(t, "test-exclude-system-basic")

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
		t.Skipf("upstream request error, skipping test: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache(setup.Plugin)

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

	ctx1 := CreateContextWithCacheKey(t, "test-exclude-system-false")

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
		t.Skipf("upstream request error, skipping test: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache(setup1.Plugin)

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

	ctx2 := CreateContextWithCacheKey(t, "test-exclude-system-true")

	t.Log("Testing ExcludeSystemPrompt=true...")
	response3, err3 := setup2.Client.ChatCompletionRequest(ctx2, request1)
	if err3 != nil {
		t.Skipf("upstream request error, skipping test: %v", err3)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response3})

	WaitForCache(setup2.Plugin)

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

	ctx := CreateContextWithCacheKey(t, "test-multiple-system-messages")

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
		t.Skipf("upstream request error, skipping test: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache(setup.Plugin)

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

	ctx := CreateContextWithCacheKey(t, "test-no-system-messages")

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
		t.Skipf("upstream request error, skipping test: %v", err1)
	}
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: response1})

	WaitForCache(setup.Plugin)

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

// responsesTurn builds a Responses request from alternating user and assistant texts.
func responsesTurn(texts ...string) *schemas.BifrostRequest {
	input := make([]schemas.ResponsesMessage, 0, len(texts))
	for i, text := range texts {
		role := schemas.ResponsesInputMessageRoleUser
		if i%2 == 1 {
			role = schemas.ResponsesInputMessageRoleAssistant
		}
		input = append(input, schemas.ResponsesMessage{
			Role:    bifrost.Ptr(role),
			Type:    bifrost.Ptr(schemas.ResponsesMessageTypeMessage),
			Content: &schemas.ResponsesMessageContent{ContentStr: bifrost.Ptr(text)},
		})
	}
	return &schemas.BifrostRequest{
		RequestType:      schemas.ResponsesStreamRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "gpt-4o-mini", Input: input},
	}
}

// chatTurn builds a Chat request from alternating user and assistant texts.
func chatTurn(texts ...string) *schemas.BifrostRequest {
	input := make([]schemas.ChatMessage, 0, len(texts))
	for i, text := range texts {
		role := schemas.ChatMessageRoleUser
		if i%2 == 1 {
			role = schemas.ChatMessageRoleAssistant
		}
		input = append(input, schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: bifrost.Ptr(text)}})
	}
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o-mini", Input: input},
	}
}

// paramsHashFor returns the params hash PreLLMHook would use as the semantic search filter.
func paramsHashFor(t *testing.T, plugin *Plugin, req *schemas.BifrostRequest) string {
	t.Helper()
	metadata, err := plugin.buildRequestMetadataForCaching(plugin.createCacheState("req"), req)
	if err != nil {
		t.Fatalf("buildRequestMetadataForCaching failed: %v", err)
	}
	hash, err := hashMap(metadata)
	if err != nil {
		t.Fatalf("hashMap failed: %v", err)
	}
	return hash
}

// TestFollowUpTurnNeverMatchesEarlierTurn reproduces a multi-turn client getting its previous answer again:
// turn 2 ("user1, reply1, user2") was embedded as one text, scored above the threshold against turn 1
// ("user1") and was served turn 1's stored response.
func TestFollowUpTurnNeverMatchesEarlierTurn(t *testing.T) {
	plugin := &Plugin{config: &Config{ExcludeSystemPrompt: bifrost.Ptr(true)}}
	first := "case-1. Remember these two values. Reply with these strings joined: aaaa and bbbb"
	reply := "aaaabbbb"
	follow := "case-1. Join the same two remembered strings in the opposite order."

	for name, build := range map[string]func(...string) *schemas.BifrostRequest{"responses": responsesTurn, "chat": chatTurn} {
		t.Run(name, func(t *testing.T) {
			turn1 := build(first)
			turn2 := build(first, reply, follow)
			if paramsHashFor(t, plugin, turn1) == paramsHashFor(t, plugin, turn2) {
				t.Fatal("a follow-up turn shares the semantic search filter with the first turn")
			}

			// Another follow-up after the same history may still match by meaning.
			reworded := build(first, reply, "case-1. Join both remembered strings, reversed.")
			if paramsHashFor(t, plugin, turn2) != paramsHashFor(t, plugin, reworded) {
				t.Fatal("follow-ups after an identical history must share the semantic search filter")
			}

			// A different earlier reply is a different conversation.
			if paramsHashFor(t, plugin, turn2) == paramsHashFor(t, plugin, build(first, "zzzz", follow)) {
				t.Fatal("follow-ups after a different history share the semantic search filter")
			}

			// Only the newest message is compared by meaning; the history is matched exactly instead.
			text, err := plugin.extractTextForEmbedding(nil, turn2)
			if err != nil {
				t.Fatalf("extractTextForEmbedding failed: %v", err)
			}
			if text != "user: "+normalizeTextForTest(follow) && text != "user: message: "+normalizeTextForTest(follow) {
				t.Fatalf("embedding text should be the newest message only, got %q", text)
			}
		})
	}
}

// TestSingleTurnCacheKeysUnchanged keeps entries written before the fix reachable for one-message requests.
func TestSingleTurnCacheKeysUnchanged(t *testing.T) {
	plugin := &Plugin{config: &Config{ExcludeSystemPrompt: bifrost.Ptr(true)}}
	req := responsesTurn("Explain quantum physics")
	metadata, err := plugin.buildRequestMetadataForCaching(plugin.createCacheState("req"), req)
	if err != nil {
		t.Fatalf("buildRequestMetadataForCaching failed: %v", err)
	}
	if _, ok := metadata["conversation_history"]; ok {
		t.Fatal("a single-message request must not carry a conversation history key")
	}
	text, err := plugin.extractTextForEmbedding(nil, req)
	if err != nil {
		t.Fatalf("extractTextForEmbedding failed: %v", err)
	}
	if text != "user: message: explain quantum physics" {
		t.Fatalf("single-message embedding text changed: %q", text)
	}
}

// normalizeTextForTest mirrors normalizeText for expected values.
func normalizeTextForTest(s string) string { return normalizeText(s) }

// TestToolOutputAsNewestItemIsEmbedded keeps semantic lookup working for agent loops, whose requests
// end in a tool output. A Responses tool output carries its text in Output, not Content, so reading
// only Content left the newest item with nothing to embed.
func TestToolOutputAsNewestItemIsEmbedded(t *testing.T) {
	plugin := &Plugin{config: &Config{ExcludeSystemPrompt: bifrost.Ptr(true)}}
	history := func() []schemas.ResponsesMessage {
		return []schemas.ResponsesMessage{
			{
				Role:    bifrost.Ptr(schemas.ResponsesInputMessageRoleUser),
				Type:    bifrost.Ptr(schemas.ResponsesMessageTypeMessage),
				Content: &schemas.ResponsesMessageContent{ContentStr: bifrost.Ptr("What is the weather in Paris?")},
			},
			{
				Type: bifrost.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    bifrost.Ptr("call_1"),
					Name:      bifrost.Ptr("get_weather"),
					Arguments: bifrost.Ptr(`{"city":"Paris"}`),
				},
			},
		}
	}
	outputs := map[string]struct {
		itemType schemas.ResponsesMessageType
		output   *schemas.ResponsesToolMessageOutputStruct
		want     string
	}{
		"function output string": {
			itemType: schemas.ResponsesMessageTypeFunctionCallOutput,
			output:   &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: bifrost.Ptr("Sunny, 21 degrees")},
			want:     ": function_call_output: sunny, 21 degrees",
		},
		"function output blocks": {
			itemType: schemas.ResponsesMessageTypeFunctionCallOutput,
			output: &schemas.ResponsesToolMessageOutputStruct{ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
				{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: bifrost.Ptr("Sunny,")},
				{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: bifrost.Ptr("21 degrees")},
			}},
			want: ": function_call_output: sunny, 21 degrees",
		},
		"custom tool output string": {
			itemType: schemas.ResponsesMessageTypeCustomToolCallOutput,
			output:   &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: bifrost.Ptr("Sunny, 21 degrees")},
			want:     ": custom_tool_call_output: sunny, 21 degrees",
		},
		"shell output": {
			itemType: schemas.ResponsesMessageTypeShellCallOutput,
			output: &schemas.ResponsesToolMessageOutputStruct{ResponsesShellCallOutput: []schemas.ResponsesShellCallOutputContent{
				{Stdout: "Sunny, 21 degrees"},
			}},
			want: ": shell_call_output: sunny, 21 degrees",
		},
	}
	for name, tc := range outputs {
		t.Run(name, func(t *testing.T) {
			input := append(history(), schemas.ResponsesMessage{
				Type:                 bifrost.Ptr(tc.itemType),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: bifrost.Ptr("call_1"), Output: tc.output},
			})
			req := &schemas.BifrostRequest{
				RequestType:      schemas.ResponsesRequest,
				ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "gpt-4o-mini", Input: input},
			}
			text, err := plugin.extractTextForEmbedding(nil, req)
			if err != nil {
				t.Fatalf("a request ending in a tool output must still produce embedding text: %v", err)
			}
			if text != tc.want {
				t.Fatalf("embedding text should be the newest tool output, got %q, want %q", text, tc.want)
			}
		})
	}
}

// TestConversationHistoryHashCostsNothingWithoutHistory pins the common case: a request with no
// earlier messages (one message, or a type with no conversation such as embeddings) must not
// normalize or copy its input to learn that it has no history.
func TestConversationHistoryHashCostsNothingWithoutHistory(t *testing.T) {
	plugin := &Plugin{config: &Config{ExcludeSystemPrompt: bifrost.Ptr(true)}}
	text := "Explain quantum physics"
	requests := map[string]*schemas.BifrostRequest{
		"chat one message":      chatTurn(text),
		"responses one message": responsesTurn(text),
		"embedding batch": {
			RequestType: schemas.EmbeddingRequest,
			EmbeddingRequest: &schemas.BifrostEmbeddingRequest{Provider: schemas.OpenAI, Model: "text-embedding-3-small", Input: []schemas.EmbeddingInputItem{
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}},
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}},
			}},
		},
		"speech": {
			RequestType:   schemas.SpeechRequest,
			SpeechRequest: &schemas.BifrostSpeechRequest{Provider: schemas.OpenAI, Model: "tts-1", Input: &schemas.SpeechInput{Input: text}},
		},
	}
	for name, req := range requests {
		t.Run(name, func(t *testing.T) {
			hash, err := plugin.conversationHistoryHash(req)
			if err != nil || hash != "" {
				t.Fatalf("a request with no history must return an empty hash, got %q, err %v", hash, err)
			}
			allocs := testing.AllocsPerRun(100, func() { _, _ = plugin.conversationHistoryHash(req) })
			if allocs != 0 {
				t.Fatalf("a request with no history must not allocate, got %v allocs per call", allocs)
			}
		})
	}
}

// BenchmarkBuildRequestMetadataForCaching measures the metadata build that runs on every cached
// request, including the conversation history hash for multi-message requests.
func BenchmarkBuildRequestMetadataForCaching(b *testing.B) {
	plugin := &Plugin{config: &Config{ExcludeSystemPrompt: bifrost.Ptr(true)}}
	turns := make([]string, 0, 21)
	for i := range 21 {
		turns = append(turns, "Turn "+strconv.Itoa(i)+": summarize the previous answer and add one more detail about the topic.")
	}
	// Three messages is the default conversation_history_threshold: longer requests are not cached
	// and never reach this code unless the threshold is raised.
	// An embedding request has no conversation, so it must cost what it did before the history hash.
	embeddingInput := make([]schemas.EmbeddingInputItem, 0, 8)
	for i := range 8 {
		embeddingInput = append(embeddingInput, schemas.EmbeddingInputItem{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &turns[i]}}})
	}
	cases := map[string]*schemas.BifrostRequest{
		"embedding/8_inputs": {
			RequestType:      schemas.EmbeddingRequest,
			EmbeddingRequest: &schemas.BifrostEmbeddingRequest{Provider: schemas.OpenAI, Model: "text-embedding-3-small", Input: embeddingInput},
		},
		"chat/one_message":      chatTurn(turns[0]),
		"chat/3_messages":       chatTurn(turns[:3]...),
		"chat/21_messages":      chatTurn(turns...),
		"responses/one_message": responsesTurn(turns[0]),
		"responses/3_messages":  responsesTurn(turns[:3]...),
		"responses/21_messages": responsesTurn(turns...),
	}
	for _, name := range []string{"embedding/8_inputs", "chat/one_message", "chat/3_messages", "chat/21_messages", "responses/one_message", "responses/3_messages", "responses/21_messages"} {
		req := cases[name]
		b.Run(name, func(b *testing.B) {
			state := plugin.createCacheState("req")
			b.ReportAllocs()
			for b.Loop() {
				if _, err := plugin.buildRequestMetadataForCaching(state, req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
