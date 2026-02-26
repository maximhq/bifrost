package llmtests

import (
	"context"
	"os"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunResponsesReasoningTest executes the reasoning test scenario to test thinking capabilities via Responses API only
func RunResponsesReasoningTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.Reasoning {
		t.Logf("⏭️ Reasoning not supported for provider %s", testConfig.Provider)
		return
	}

	// Skip if no reasoning model is configured
	if testConfig.ReasoningModel == "" {
		t.Logf("⏭️ No reasoning model configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("ResponsesReasoning", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Create a complex problem that requires step-by-step reasoning
		problemPrompt := "A farmer has 100 chickens and 50 cows. Each chicken lays 5 eggs per week, and each cow produces 20 liters of milk per day. If the farmer sells eggs for $0.25 each and milk for $1.50 per liter, and it costs $2 per week to feed each chicken and $15 per week to feed each cow, what is the farmer's weekly profit? Please show your step-by-step reasoning."

		responsesMessages := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage(problemPrompt),
		}

		// Execute Responses API test with retries
		responsesReq := &schemas.BifrostResponsesRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ReasoningModel,
			Input:    responsesMessages,
			Params: &schemas.ResponsesParameters{
				// Reasoning models (o3, o4-mini) allocate tokens between reasoning and text output.
				// Note: Older o1 models may not return message output via Responses API - use o3/o4-mini.
				// OpenAI recommends reserving at least 25,000 tokens for reasoning and outputs.
				// See: https://platform.openai.com/docs/guides/reasoning#allocating-space-for-reasoning
				MaxOutputTokens: bifrost.Ptr(25000),
				// Configure reasoning-specific parameters
				Reasoning: &schemas.ResponsesParametersReasoning{
					Effort: bifrost.Ptr("high"), // High effort for complex reasoning
					// Summary: bifrost.Ptr("detailed"), // Detailed summary of reasoning process
				},
				// Include reasoning content in response
				Include: []string{"reasoning.encrypted_content"},
			},
			Fallbacks: testConfig.Fallbacks,
		}

		// Use retry framework with enhanced validation for reasoning
		retryConfig := GetTestRetryConfigForScenario("Reasoning", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "Reasoning",
			ExpectedBehavior: map[string]interface{}{
				"should_show_reasoning": true,
				"mathematical_problem":  true,
				"step_by_step":          true,
			},
			TestMetadata: map[string]interface{}{
				"provider":          testConfig.Provider,
				"model":             testConfig.ReasoningModel,
				"problem_type":      "mathematical",
				"complexity":        "high",
				"expects_reasoning": true,
			},
		}
		responsesRetryConfig := ResponsesRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []ResponsesRetryCondition{}, // Add specific responses retry conditions as needed
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		// Enhanced validation for reasoning scenarios
		expectations := GetExpectationsForScenario("Reasoning", testConfig, map[string]interface{}{
			"requires_reasoning": true,
		})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

		response, responsesError := WithResponsesTestRetry(t, responsesRetryConfig, retryContext, expectations, "Reasoning", func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.ResponsesRequest(bfCtx, responsesReq)
		})

		if responsesError != nil {
			t.Fatalf("❌ Reasoning test failed after retries: %v", GetErrorMessage(responsesError))
		}
		if response != nil {
			defer schemas.ReleaseBifrostResponsesResponse(response)
		}

		// Log the response content
		responsesContent := GetResponsesContent(response)
		if responsesContent == "" {
			t.Logf("✅ Responses API reasoning result: <no content>")
		} else {
			maxLen := 300
			if len(responsesContent) < maxLen {
				maxLen = len(responsesContent)
			}
			t.Logf("✅ Responses API reasoning result: %s", responsesContent[:maxLen])
		}

		// Additional reasoning-specific validation (complementary to the main validation)
		reasoningDetected := validateResponsesAPIReasoning(t, response)
		if !reasoningDetected {
			t.Logf("⚠️ No explicit reasoning indicators found in response structure - may still contain valid reasoning in content")
		} else {
			t.Logf("🧠 Reasoning structure detected in response")
		}

		t.Logf("🎉 Responses API passed Reasoning test!")
	})
}

// validateResponsesAPIReasoning performs additional validation specific to Responses API reasoning features
// Returns true if reasoning indicators are found
func validateResponsesAPIReasoning(t *testing.T, response *schemas.BifrostResponsesResponse) bool {
	if response == nil || response.Output == nil {
		return false
	}

	reasoningFound := false
	summaryFound := false
	reasoningContentFound := false

	// Check if response contains reasoning messages or reasoning content
	for _, message := range response.Output {
		// Check for ResponsesMessageTypeReasoning
		if message.Type != nil && *message.Type == schemas.ResponsesMessageTypeReasoning {
			reasoningFound = true
			t.Logf("🧠 Found ResponsesMessageTypeReasoning message in response")

			// Check for reasoning summary content
			if message.ResponsesReasoning != nil && len(message.ResponsesReasoning.Summary) > 0 {
				summaryFound = true
				t.Logf("📝 Found reasoning summary with %d content blocks", len(message.ResponsesReasoning.Summary))

				// Log first summary block for debugging
				if len(message.ResponsesReasoning.Summary) > 0 {
					firstSummary := message.ResponsesReasoning.Summary[0]
					if len(firstSummary.Text) > 0 {
						maxLen := 200
						if len(firstSummary.Text) < maxLen {
							maxLen = len(firstSummary.Text)
						}
						t.Logf("📋 First reasoning summary: %s", firstSummary.Text[:maxLen])
					} else {
						t.Logf("📋 First reasoning summary: (empty)")
					}
				}
			}

			// Check for encrypted reasoning content
			if message.ResponsesReasoning != nil && message.ResponsesReasoning.EncryptedContent != nil {
				t.Logf("🔐 Found encrypted reasoning content")
			}
		}

		// Check for content blocks with ResponsesOutputMessageContentTypeReasoning
		if message.Content != nil && message.Content.ContentBlocks != nil {
			for _, block := range message.Content.ContentBlocks {
				if block.Type == schemas.ResponsesOutputMessageContentTypeReasoning {
					reasoningContentFound = true
					t.Logf("🔍 Found ResponsesOutputMessageContentTypeReasoning content block")
				}
			}
		}
	}

	// Check if reasoning tokens were used
	if response.Usage != nil && response.Usage.OutputTokensDetails != nil &&
		response.Usage.OutputTokensDetails.ReasoningTokens > 0 {
		t.Logf("🔢 Reasoning tokens used: %d", response.Usage.OutputTokensDetails.ReasoningTokens)
		reasoningFound = true // Reasoning tokens indicate reasoning was performed
	}

	// Log findings
	detected := reasoningFound || reasoningContentFound
	if detected {
		t.Logf("✅ Responses API reasoning indicators detected")
		if reasoningFound {
			t.Logf("  - ResponsesMessageTypeReasoning or reasoning tokens found")
		}
		if reasoningContentFound {
			t.Logf("  - ResponsesOutputMessageContentTypeReasoning content blocks found")
		}
		if summaryFound {
			t.Logf("  - Reasoning summary content found")
		}
	} else {
		t.Logf("ℹ️ No explicit reasoning indicators found (may be provider-specific)")
	}

	return detected
}

// RunChatCompletionReasoningTest executes the reasoning test scenario to test thinking capabilities via Chat Completions API
func RunChatCompletionReasoningTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.Reasoning {
		t.Logf("⏭️ Reasoning not supported for provider %s", testConfig.Provider)
		return
	}

	// Skip if no reasoning model is configured
	if testConfig.ReasoningModel == "" {
		t.Logf("⏭️ No reasoning model configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("ChatCompletionReasoning", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		if testConfig.Provider == schemas.OpenAI {
			// OpenAI because reasoning for them in chat completions is extremely flaky
			t.Skip("Skipping ChatCompletionReasoning test for OpenAI")
			return
		}

		// Create a complex problem that requires step-by-step reasoning
		problemPrompt := "A farmer has 100 chickens and 50 cows. Each chicken lays 5 eggs per week, and each cow produces 20 liters of milk per day. If the farmer sells eggs for $0.25 each and milk for $1.50 per liter, and it costs $2 per week to feed each chicken and $15 per week to feed each cow, what is the farmer's weekly profit? Please show your step-by-step reasoning."

		chatMessages := []schemas.ChatMessage{
			CreateBasicChatMessage(problemPrompt),
		}

		// Execute Chat Completions API test with retries
		chatReq := &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ReasoningModel,
			Input:    chatMessages,
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(1800),
				// Configure reasoning-specific parameters
				Reasoning: &schemas.ChatReasoning{
					Effort:    bifrost.Ptr("high"), // High effort for complex reasoning
					MaxTokens: bifrost.Ptr(1500),   // Maximum tokens for reasoning output
				},
			},
			Fallbacks: testConfig.Fallbacks,
		}

		// Use retry framework with enhanced validation for reasoning
		retryConfig := GetTestRetryConfigForScenario("Reasoning", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "Reasoning",
			ExpectedBehavior: map[string]interface{}{
				"should_show_reasoning": true,
				"mathematical_problem":  true,
				"step_by_step":          true,
			},
			TestMetadata: map[string]interface{}{
				"provider":          testConfig.Provider,
				"model":             testConfig.ReasoningModel,
				"problem_type":      "mathematical",
				"complexity":        "high",
				"expects_reasoning": true,
			},
		}
		chatRetryConfig := ChatRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []ChatRetryCondition{}, // Add specific chat retry conditions as needed
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}

		// Enhanced validation for reasoning scenarios
		expectations := GetExpectationsForScenario("Reasoning", testConfig, map[string]interface{}{
			"requires_reasoning": true,
		})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

		response, chatError := WithChatTestRetry(t, chatRetryConfig, retryContext, expectations, "Reasoning", func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.ChatCompletionRequest(bfCtx, chatReq)
		})

		if chatError != nil {
			t.Fatalf("❌ Reasoning test failed after retries: %v", GetErrorMessage(chatError))
		}
		if response != nil {
			defer schemas.ReleaseBifrostChatResponse(response)
		}

		// Log the response content
		chatContent := GetChatContent(response)
		if chatContent == "" {
			t.Logf("✅ Chat Completions API reasoning result: <no content>")
		} else {
			maxLen := 300
			if len(chatContent) < maxLen {
				maxLen = len(chatContent)
			}
			t.Logf("✅ Chat Completions API reasoning result: %s", chatContent[:maxLen])
		}

		// Additional reasoning-specific validation (complementary to the main validation)
		reasoningDetected := validateChatCompletionReasoning(t, response)
		if !reasoningDetected {
			t.Logf("⚠️ No explicit reasoning indicators found in response structure - may still contain valid reasoning in content")
		} else {
			t.Logf("🧠 Reasoning structure detected in response")
		}

		t.Logf("🎉 Chat Completions API passed Reasoning test!")
	})
}

// validateChatCompletionReasoning performs additional validation specific to Chat Completions API reasoning features
// Returns true if reasoning indicators are found
func validateChatCompletionReasoning(t *testing.T, response *schemas.BifrostChatResponse) bool {
	if response == nil || len(response.Choices) == 0 {
		return false
	}

	reasoningFound := false
	reasoningDetailsFound := false
	reasoningTokensFound := false

	// Check each choice for reasoning indicators
	for _, choice := range response.Choices {
		// Check for reasoning details in ChatNonStreamResponseChoice
		if choice.ChatNonStreamResponseChoice != nil && choice.ChatNonStreamResponseChoice.Message != nil {
			message := choice.ChatNonStreamResponseChoice.Message

			if message == nil {
				continue
			}

			// Check for reasoning content in message (for backward compatibility)
			if message.ChatAssistantMessage != nil && message.ChatAssistantMessage.Reasoning != nil && *message.ChatAssistantMessage.Reasoning != "" {
				reasoningFound = true
				t.Logf("🧠 Found reasoning content in message (length: %d)", len(*message.ChatAssistantMessage.Reasoning))

				// Log first 200 chars for debugging
				reasoningText := *message.ChatAssistantMessage.Reasoning
				maxLen := 200
				if len(reasoningText) < maxLen {
					maxLen = len(reasoningText)
				}
				t.Logf("📋 First reasoning content: %s", reasoningText[:maxLen])
			}

			// Check for reasoning details array
			if message.ChatAssistantMessage != nil && len(message.ChatAssistantMessage.ReasoningDetails) > 0 {
				reasoningDetailsFound = true
				t.Logf("📝 Found %d reasoning details entries", len(message.ChatAssistantMessage.ReasoningDetails))

				// Log details about each reasoning entry
				for i, detail := range message.ChatAssistantMessage.ReasoningDetails {
					t.Logf("  - Entry %d: Type=%s, Index=%d", i, detail.Type, detail.Index)

					switch detail.Type {
					case schemas.BifrostReasoningDetailsTypeSummary:
						if detail.Summary != nil {
							t.Logf("    Summary length: %d", len(*detail.Summary))
						}
					case schemas.BifrostReasoningDetailsTypeText:
						if detail.Text != nil {
							textLen := len(*detail.Text)
							t.Logf("    Text length: %d", textLen)
							if textLen > 0 {
								maxLen := 150
								if textLen < maxLen {
									maxLen = textLen
								}
								t.Logf("    Text preview: %s", (*detail.Text)[:maxLen])
							}
						}
					case schemas.BifrostReasoningDetailsTypeEncrypted:
						if detail.Data != nil {
							t.Logf("    Encrypted data length: %d", len(*detail.Data))
						}
						if detail.Signature != nil {
							t.Logf("    Signature present: %d bytes", len(*detail.Signature))
						}
					}
				}
			}
		}
	}

	// Check if reasoning tokens were used
	if response.Usage != nil && response.Usage.CompletionTokensDetails != nil &&
		response.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
		reasoningTokensFound = true
		t.Logf("🔢 Reasoning tokens used: %d", response.Usage.CompletionTokensDetails.ReasoningTokens)
	}

	// Log findings
	detected := reasoningFound || reasoningDetailsFound || reasoningTokensFound
	if detected {
		t.Logf("✅ Chat Completions API reasoning indicators detected")
		if reasoningFound {
			t.Logf("  - Reasoning content found in message")
		}
		if reasoningDetailsFound {
			t.Logf("  - Reasoning details array found")
		}
		if reasoningTokensFound {
			t.Logf("  - Reasoning tokens usage reported")
		}
	} else {
		t.Logf("ℹ️ No explicit reasoning indicators found (may be provider-specific)")
	}

	return detected
}

// RunMultiTurnReasoningTest tests multi-turn conversations with reasoning content passthrough.
// It verifies that reasoning details (text + signature) from assistant messages are correctly
// passed back to the model in follow-up turns via the Chat Completions API.
func RunMultiTurnReasoningTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.Reasoning {
		t.Logf("⏭️ Reasoning not supported for provider %s", testConfig.Provider)
		return
	}

	if testConfig.ReasoningModel == "" {
		t.Logf("⏭️ No reasoning model configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("MultiTurnReasoning", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		if testConfig.Provider == schemas.OpenAI {
			t.Skip("Skipping MultiTurnReasoning test for OpenAI")
			return
		}

		// Step 1: Send initial reasoning request
		initialPrompt := "What is 15 * 17? Think step by step."
		chatMessages := []schemas.ChatMessage{
			CreateBasicChatMessage(initialPrompt),
		}

		chatReq := &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ReasoningModel,
			Input:    chatMessages,
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(4000),
				Reasoning: &schemas.ChatReasoning{
					Effort: bifrost.Ptr("low"),
				},
			},
			Fallbacks: testConfig.Fallbacks,
		}

		retryConfig := GetTestRetryConfigForScenario("Reasoning", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "MultiTurnReasoning_Step1",
			ExpectedBehavior: map[string]interface{}{
				"should_show_reasoning": true,
				"multi_turn":            true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ReasoningModel,
				"step":     "initial",
			},
		}
		chatRetryConfig := ChatRetryConfig{
			MaxAttempts: retryConfig.MaxAttempts,
			BaseDelay:   retryConfig.BaseDelay,
			MaxDelay:    retryConfig.MaxDelay,
			Conditions:  []ChatRetryCondition{},
			OnRetry:     retryConfig.OnRetry,
			OnFinalFail: retryConfig.OnFinalFail,
		}
		expectations := GetExpectationsForScenario("Reasoning", testConfig, map[string]interface{}{
			"requires_reasoning": true,
		})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)

		firstResponse, chatError := WithChatTestRetry(t, chatRetryConfig, retryContext, expectations, "MultiTurnReasoning_Step1", func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.ChatCompletionRequest(bfCtx, chatReq)
		})

		if chatError != nil {
			t.Fatalf("Step 1 failed: %v", GetErrorMessage(chatError))
		}
		defer schemas.ReleaseBifrostChatResponse(firstResponse)

		firstContent := GetChatContent(firstResponse)
		if firstContent == "" {
			t.Fatal("Step 1: Expected non-empty response content")
		}
		t.Logf("Step 1 response: %s", truncateString(firstContent, 200))

		// Extract reasoning details from first response
		var reasoningDetails []schemas.ChatReasoningDetails
		if len(firstResponse.Choices) > 0 {
			choice := firstResponse.Choices[0]
			if choice.ChatNonStreamResponseChoice != nil &&
				choice.ChatNonStreamResponseChoice.Message != nil &&
				choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage != nil {
				reasoningDetails = choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ReasoningDetails
			}
		}

		t.Logf("Step 1: Found %d reasoning detail entries", len(reasoningDetails))

		// Step 2: Build multi-turn conversation with reasoning details passed back
		multiTurnMessages := []schemas.ChatMessage{
			CreateBasicChatMessage(initialPrompt),
			{
				Role: schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{
					ContentStr: &firstContent,
				},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ReasoningDetails: reasoningDetails,
				},
			},
			CreateBasicChatMessage("Now multiply that result by 2."),
		}

		multiTurnReq := &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ReasoningModel,
			Input:    multiTurnMessages,
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(4000),
				Reasoning: &schemas.ChatReasoning{
					Effort: bifrost.Ptr("low"),
				},
			},
			Fallbacks: testConfig.Fallbacks,
		}

		retryContext2 := TestRetryContext{
			ScenarioName: "MultiTurnReasoning_Step2",
			ExpectedBehavior: map[string]interface{}{
				"multi_turn":            true,
				"reasoning_passthrough": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ReasoningModel,
				"step":     "follow_up",
			},
		}

		secondResponse, chatError2 := WithChatTestRetry(t, chatRetryConfig, retryContext2, expectations, "MultiTurnReasoning_Step2", func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.ChatCompletionRequest(bfCtx, multiTurnReq)
		})

		if chatError2 != nil {
			t.Fatalf("Step 2 (multi-turn with reasoning passthrough) failed: %v", GetErrorMessage(chatError2))
		}
		defer schemas.ReleaseBifrostChatResponse(secondResponse)

		secondContent := GetChatContent(secondResponse)
		if secondContent == "" {
			t.Error("Step 2: Expected non-empty response content")
		} else {
			t.Logf("Step 2 response: %s", truncateString(secondContent, 200))
		}

		t.Log("Multi-turn reasoning passthrough test passed!")
	})
}

// min returns the smaller of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
