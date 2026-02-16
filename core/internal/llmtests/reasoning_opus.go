package llmtests

import (
	"context"
	"os"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// OpusReasoningTestConfig holds configuration for Opus-specific reasoning tests
type OpusReasoningTestConfig struct {
	Provider   schemas.ModelProvider
	Opus45Model string // Opus 4.5 model identifier
	Opus46Model string // Opus 4.6 model identifier
	Fallbacks  []schemas.Fallback
	SkipOpus45 bool   // Skip Opus 4.5 tests
	SkipOpus46 bool   // Skip Opus 4.6 tests
	SkipReason string // Reason for skipping
}

// GetOpusReasoningTestConfigs returns test configurations for Opus reasoning across providers
func GetOpusReasoningTestConfigs() []OpusReasoningTestConfig {
	return []OpusReasoningTestConfig{
		{
			Provider:    schemas.Anthropic,
			Opus45Model: "claude-opus-4-5-20251101",
			Opus46Model: "claude-opus-4-6-20260210",
			Fallbacks:   []schemas.Fallback{},
		},
		{
			Provider:    schemas.Bedrock,
			Opus45Model: "global.anthropic.claude-opus-4-5-20251101-v1:0",
			Opus46Model: "global.anthropic.claude-opus-4-6-v1",
			Fallbacks:   []schemas.Fallback{},
		},
		{
			Provider:    schemas.Azure,
			Opus45Model: "claude-opus-4-5", // Uses deployment name
			Opus46Model: "claude-opus-4-6", // Uses deployment name
			Fallbacks:   []schemas.Fallback{},
		},
		{
			Provider:    schemas.Vertex,
			Opus45Model: "claude-opus-4-5", // Uses deployment name
			Opus46Model: "claude-opus-4-6", // Uses deployment name
			Fallbacks:   []schemas.Fallback{},
		},
	}
}

// RunOpus45ReasoningTest tests extended thinking with Opus 4.5 (budget_tokens mode)
func RunOpus45ReasoningTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, config OpusReasoningTestConfig) {
	if config.SkipOpus45 {
		t.Skipf("Skipping Opus 4.5 test: %s", config.SkipReason)
		return
	}

	if config.Opus45Model == "" {
		t.Skip("No Opus 4.5 model configured")
		return
	}

	t.Run("Opus45_ExtendedThinking", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Complex reasoning problem
		problemPrompt := "Solve this step by step: A train leaves station A at 9:00 AM traveling at 60 mph. Another train leaves station B (300 miles away) at 10:00 AM traveling towards station A at 80 mph. At what time will they meet, and how far from station A?"

		// Create a test config for retry framework
		testConfig := ComprehensiveTestConfig{
			Provider:       config.Provider,
			ReasoningModel: config.Opus45Model,
			Scenarios: TestScenarios{
				Reasoning: true,
			},
			Fallbacks: config.Fallbacks,
		}

		// Test via Responses API
		t.Run("ResponsesAPI", func(t *testing.T) {
			if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
				t.Parallel()
			}

			responsesMessages := []schemas.ResponsesMessage{
				CreateBasicResponsesMessage(problemPrompt),
			}

			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: config.Provider,
				Model:    config.Opus45Model,
				Input:    responsesMessages,
				Params: &schemas.ResponsesParameters{
					MaxOutputTokens: bifrost.Ptr(4000),
					Reasoning: &schemas.ResponsesParametersReasoning{
						Effort: bifrost.Ptr("high"),
					},
					Include: []string{"reasoning.encrypted_content"},
				},
				Fallbacks: config.Fallbacks,
			}

			// Use retry framework with enhanced validation for reasoning
			retryConfig := GetTestRetryConfigForScenario("Reasoning", testConfig)
			retryContext := TestRetryContext{
				ScenarioName: "Opus45_Reasoning_Responses",
				ExpectedBehavior: map[string]interface{}{
					"should_show_reasoning": true,
					"mathematical_problem":  true,
					"step_by_step":          true,
					"model_version":         "opus-4.5",
					"thinking_mode":         "budget_tokens",
				},
				TestMetadata: map[string]interface{}{
					"provider":          config.Provider,
					"model":             config.Opus45Model,
					"problem_type":      "mathematical",
					"complexity":        "high",
					"expects_reasoning": true,
				},
			}
			responsesRetryConfig := ResponsesRetryConfig{
				MaxAttempts: retryConfig.MaxAttempts,
				BaseDelay:   retryConfig.BaseDelay,
				MaxDelay:    retryConfig.MaxDelay,
				Conditions:  []ResponsesRetryCondition{},
				OnRetry:     retryConfig.OnRetry,
				OnFinalFail: retryConfig.OnFinalFail,
			}

			// Enhanced validation for reasoning scenarios
			expectations := GetExpectationsForScenario("Reasoning", testConfig, map[string]interface{}{
				"requires_reasoning": true,
			})
			expectations = ModifyExpectationsForProvider(expectations, config.Provider)

			response, responsesError := WithResponsesTestRetry(t, responsesRetryConfig, retryContext, expectations, "Opus45_Reasoning_Responses", func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
				bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
				return client.ResponsesRequest(bfCtx, responsesReq)
			})

			if responsesError != nil {
				t.Fatalf("❌ Opus 4.5 Responses API reasoning test failed after retries: %v", GetErrorMessage(responsesError))
			}
			defer response.Release()

			// Validate response has content
			content := GetResponsesContent(response)
			if content == "" {
				t.Error("Expected non-empty response content")
			} else {
				t.Logf("✅ Opus 4.5 reasoning response (first 200 chars): %s", truncateString(content, 200))
			}

			// Check for reasoning indicators
			reasoningDetected := validateResponsesAPIReasoning(t, response)
			if !reasoningDetected {
				t.Logf("⚠️ No explicit reasoning indicators found in response structure")
			} else {
				t.Logf("🧠 Reasoning structure detected in response")
			}

			t.Log("🎉 Opus 4.5 Responses API reasoning test passed!")
		})

		// Test via Chat Completions API
		t.Run("ChatCompletionsAPI", func(t *testing.T) {
			if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
				t.Parallel()
			}

			chatMessages := []schemas.ChatMessage{
				CreateBasicChatMessage(problemPrompt),
			}

			chatReq := &schemas.BifrostChatRequest{
				Provider: config.Provider,
				Model:    config.Opus45Model,
				Input:    chatMessages,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(4000),
					Reasoning: &schemas.ChatReasoning{
						Effort:    bifrost.Ptr("high"),
						MaxTokens: bifrost.Ptr(2000), // Budget tokens for Opus 4.5
					},
				},
				Fallbacks: config.Fallbacks,
			}

			// Use retry framework with enhanced validation for reasoning
			retryConfig := GetTestRetryConfigForScenario("Reasoning", testConfig)
			retryContext := TestRetryContext{
				ScenarioName: "Opus45_Reasoning_Chat",
				ExpectedBehavior: map[string]interface{}{
					"should_show_reasoning": true,
					"mathematical_problem":  true,
					"step_by_step":          true,
					"model_version":         "opus-4.5",
					"thinking_mode":         "budget_tokens",
				},
				TestMetadata: map[string]interface{}{
					"provider":          config.Provider,
					"model":             config.Opus45Model,
					"problem_type":      "mathematical",
					"complexity":        "high",
					"expects_reasoning": true,
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

			// Enhanced validation for reasoning scenarios
			expectations := GetExpectationsForScenario("Reasoning", testConfig, map[string]interface{}{
				"requires_reasoning": true,
			})
			expectations = ModifyExpectationsForProvider(expectations, config.Provider)

			response, chatError := WithChatTestRetry(t, chatRetryConfig, retryContext, expectations, "Opus45_Reasoning_Chat", func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
				bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
				return client.ChatCompletionRequest(bfCtx, chatReq)
			})

			if chatError != nil {
				t.Fatalf("❌ Opus 4.5 Chat Completions API reasoning test failed after retries: %v", GetErrorMessage(chatError))
			}
			defer response.Release()

			// Validate response has content
			content := GetChatContent(response)
			if content == "" {
				t.Error("Expected non-empty response content")
			} else {
				t.Logf("✅ Opus 4.5 reasoning response (first 200 chars): %s", truncateString(content, 200))
			}

			// Check for reasoning indicators
			reasoningDetected := validateChatCompletionReasoning(t, response)
			if !reasoningDetected {
				t.Logf("⚠️ No explicit reasoning indicators found in response structure")
			} else {
				t.Logf("🧠 Reasoning structure detected in response")
			}

			t.Log("🎉 Opus 4.5 Chat Completions API reasoning test passed!")
		})
	})
}

// RunOpus46ReasoningTest tests adaptive thinking with Opus 4.6 (adaptive mode + effort)
func RunOpus46ReasoningTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, config OpusReasoningTestConfig) {
	if config.SkipOpus46 {
		t.Skipf("Skipping Opus 4.6 test: %s", config.SkipReason)
		return
	}

	if config.Opus46Model == "" {
		t.Skip("No Opus 4.6 model configured")
		return
	}

	t.Run("Opus46_AdaptiveThinking", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Complex reasoning problem that benefits from adaptive thinking
		problemPrompt := "Analyze this logic puzzle: Five people (A, B, C, D, E) are sitting in a row. A is not at either end. B is somewhere to the left of C. D is not next to E. E is at one of the ends. In how many different valid arrangements can they sit? Show your reasoning."

		// Create a test config for retry framework
		testConfig := ComprehensiveTestConfig{
			Provider:       config.Provider,
			ReasoningModel: config.Opus46Model,
			Scenarios: TestScenarios{
				Reasoning: true,
			},
			Fallbacks: config.Fallbacks,
		}

		// Test via Responses API with different effort levels
		effortLevels := []string{"low", "medium", "high"}

		for _, effort := range effortLevels {
			effort := effort // capture range variable
			t.Run("ResponsesAPI_Effort_"+effort, func(t *testing.T) {
				if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
					t.Parallel()
				}

				responsesMessages := []schemas.ResponsesMessage{
					CreateBasicResponsesMessage(problemPrompt),
				}

				responsesReq := &schemas.BifrostResponsesRequest{
					Provider: config.Provider,
					Model:    config.Opus46Model,
					Input:    responsesMessages,
					Params: &schemas.ResponsesParameters{
						MaxOutputTokens: bifrost.Ptr(4000),
						Reasoning: &schemas.ResponsesParametersReasoning{
							Effort: bifrost.Ptr(effort), // Adaptive thinking uses effort parameter
						},
						Include: []string{"reasoning.encrypted_content"},
					},
					Fallbacks: config.Fallbacks,
				}

				// Use retry framework with enhanced validation for reasoning
				retryConfig := GetTestRetryConfigForScenario("Reasoning", testConfig)
				retryContext := TestRetryContext{
					ScenarioName: "Opus46_Reasoning_Responses_" + effort,
					ExpectedBehavior: map[string]interface{}{
						"should_show_reasoning": true,
						"logic_puzzle":          true,
						"step_by_step":          true,
						"model_version":         "opus-4.6",
						"thinking_mode":         "adaptive",
						"effort_level":          effort,
					},
					TestMetadata: map[string]interface{}{
						"provider":          config.Provider,
						"model":             config.Opus46Model,
						"problem_type":      "logic_puzzle",
						"complexity":        "high",
						"expects_reasoning": true,
						"effort":            effort,
					},
				}
				responsesRetryConfig := ResponsesRetryConfig{
					MaxAttempts: retryConfig.MaxAttempts,
					BaseDelay:   retryConfig.BaseDelay,
					MaxDelay:    retryConfig.MaxDelay,
					Conditions:  []ResponsesRetryCondition{},
					OnRetry:     retryConfig.OnRetry,
					OnFinalFail: retryConfig.OnFinalFail,
				}

				// Enhanced validation for reasoning scenarios
				expectations := GetExpectationsForScenario("Reasoning", testConfig, map[string]interface{}{
					"requires_reasoning": true,
				})
				expectations = ModifyExpectationsForProvider(expectations, config.Provider)

				response, responsesError := WithResponsesTestRetry(t, responsesRetryConfig, retryContext, expectations, "Opus46_Reasoning_Responses_"+effort, func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
					bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
					return client.ResponsesRequest(bfCtx, responsesReq)
				})

				if responsesError != nil {
					t.Fatalf("❌ Opus 4.6 Responses API (effort=%s) reasoning test failed after retries: %v", effort, GetErrorMessage(responsesError))
				}
				defer response.Release()

				// Validate response has content
				content := GetResponsesContent(response)
				if content == "" {
					t.Errorf("Expected non-empty response content for effort=%s", effort)
				} else {
					t.Logf("✅ Opus 4.6 (effort=%s) response (first 200 chars): %s", effort, truncateString(content, 200))
				}

				// Check for reasoning indicators
				reasoningDetected := validateResponsesAPIReasoning(t, response)
				if !reasoningDetected {
					t.Logf("⚠️ No explicit reasoning indicators found in response structure")
				} else {
					t.Logf("🧠 Reasoning structure detected in response")
				}

				t.Logf("🎉 Opus 4.6 Responses API (effort=%s) reasoning test passed!", effort)
			})
		}

		// Test via Chat Completions API
		t.Run("ChatCompletionsAPI", func(t *testing.T) {
			if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
				t.Parallel()
			}

			chatMessages := []schemas.ChatMessage{
				CreateBasicChatMessage(problemPrompt),
			}

			chatReq := &schemas.BifrostChatRequest{
				Provider: config.Provider,
				Model:    config.Opus46Model,
				Input:    chatMessages,
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: bifrost.Ptr(4000),
					Reasoning: &schemas.ChatReasoning{
						Effort: bifrost.Ptr("high"), // Opus 4.6 uses adaptive thinking with effort
						// Note: MaxTokens (budget_tokens) is NOT used for Opus 4.6
					},
				},
				Fallbacks: config.Fallbacks,
			}

			// Use retry framework with enhanced validation for reasoning
			retryConfig := GetTestRetryConfigForScenario("Reasoning", testConfig)
			retryContext := TestRetryContext{
				ScenarioName: "Opus46_Reasoning_Chat",
				ExpectedBehavior: map[string]interface{}{
					"should_show_reasoning": true,
					"logic_puzzle":          true,
					"step_by_step":          true,
					"model_version":         "opus-4.6",
					"thinking_mode":         "adaptive",
				},
				TestMetadata: map[string]interface{}{
					"provider":          config.Provider,
					"model":             config.Opus46Model,
					"problem_type":      "logic_puzzle",
					"complexity":        "high",
					"expects_reasoning": true,
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

			// Enhanced validation for reasoning scenarios
			expectations := GetExpectationsForScenario("Reasoning", testConfig, map[string]interface{}{
				"requires_reasoning": true,
			})
			expectations = ModifyExpectationsForProvider(expectations, config.Provider)

			response, chatError := WithChatTestRetry(t, chatRetryConfig, retryContext, expectations, "Opus46_Reasoning_Chat", func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
				bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
				return client.ChatCompletionRequest(bfCtx, chatReq)
			})

			if chatError != nil {
				t.Fatalf("❌ Opus 4.6 Chat Completions API reasoning test failed after retries: %v", GetErrorMessage(chatError))
			}
			defer response.Release()

			// Validate response has content
			content := GetChatContent(response)
			if content == "" {
				t.Error("Expected non-empty response content")
			} else {
				t.Logf("✅ Opus 4.6 reasoning response (first 200 chars): %s", truncateString(content, 200))
			}

			// Check for reasoning indicators
			reasoningDetected := validateChatCompletionReasoning(t, response)
			if !reasoningDetected {
				t.Logf("⚠️ No explicit reasoning indicators found in response structure")
			} else {
				t.Logf("🧠 Reasoning structure detected in response")
			}

			t.Log("🎉 Opus 4.6 Chat Completions API reasoning test passed!")
		})
	})
}

// RunOpus46MultiTurnReasoningTest tests multi-turn conversations with reasoning content passthrough.
// This verifies that reasoning details (text + signature) from assistant messages are correctly
// passed back to the model in follow-up turns.
func RunOpus46MultiTurnReasoningTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, config OpusReasoningTestConfig) {
	if config.SkipOpus46 {
		t.Skipf("Skipping Opus 4.6 multi-turn test: %s", config.SkipReason)
		return
	}

	if config.Opus46Model == "" {
		t.Skip("No Opus 4.6 model configured")
		return
	}

	t.Run("Opus46_MultiTurnReasoning", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		testConfig := ComprehensiveTestConfig{
			Provider:       config.Provider,
			ReasoningModel: config.Opus46Model,
			Scenarios:      TestScenarios{Reasoning: true},
			Fallbacks:      config.Fallbacks,
		}

		// Step 1: Send initial reasoning request
		initialPrompt := "What is 15 * 17? Think step by step."
		chatMessages := []schemas.ChatMessage{
			CreateBasicChatMessage(initialPrompt),
		}

		chatReq := &schemas.BifrostChatRequest{
			Provider: config.Provider,
			Model:    config.Opus46Model,
			Input:    chatMessages,
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(4000),
				Reasoning: &schemas.ChatReasoning{
					Effort: bifrost.Ptr("low"),
				},
			},
			Fallbacks: config.Fallbacks,
		}

		retryConfig := GetTestRetryConfigForScenario("Reasoning", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "Opus46_MultiTurn_Step1",
			ExpectedBehavior: map[string]interface{}{
				"should_show_reasoning": true,
				"model_version":         "opus-4.6",
				"thinking_mode":         "adaptive",
			},
			TestMetadata: map[string]interface{}{
				"provider": config.Provider,
				"model":    config.Opus46Model,
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
		expectations = ModifyExpectationsForProvider(expectations, config.Provider)

		firstResponse, chatError := WithChatTestRetry(t, chatRetryConfig, retryContext, expectations, "Opus46_MultiTurn_Step1", func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.ChatCompletionRequest(bfCtx, chatReq)
		})

		if chatError != nil {
			t.Fatalf("Step 1 failed: %v", GetErrorMessage(chatError))
		}
		defer firstResponse.Release()

		firstContent := GetChatContent(firstResponse)
		if firstContent == "" {
			t.Fatal("Step 1: Expected non-empty response content")
		}
		t.Logf("Step 1 response (first 200 chars): %s", truncateString(firstContent, 200))

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
			Provider: config.Provider,
			Model:    config.Opus46Model,
			Input:    multiTurnMessages,
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(4000),
				Reasoning: &schemas.ChatReasoning{
					Effort: bifrost.Ptr("low"),
				},
			},
			Fallbacks: config.Fallbacks,
		}

		retryContext2 := TestRetryContext{
			ScenarioName: "Opus46_MultiTurn_Step2",
			ExpectedBehavior: map[string]interface{}{
				"multi_turn":    true,
				"model_version": "opus-4.6",
				"thinking_mode": "adaptive",
			},
			TestMetadata: map[string]interface{}{
				"provider": config.Provider,
				"model":    config.Opus46Model,
				"step":     "follow_up",
			},
		}

		secondResponse, chatError2 := WithChatTestRetry(t, chatRetryConfig, retryContext2, expectations, "Opus46_MultiTurn_Step2", func() (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.ChatCompletionRequest(bfCtx, multiTurnReq)
		})

		if chatError2 != nil {
			t.Fatalf("Step 2 (multi-turn with reasoning passthrough) failed: %v", GetErrorMessage(chatError2))
		}
		defer secondResponse.Release()

		secondContent := GetChatContent(secondResponse)
		if secondContent == "" {
			t.Error("Step 2: Expected non-empty response content")
		} else {
			t.Logf("Step 2 response (first 200 chars): %s", truncateString(secondContent, 200))
		}

		t.Log("Multi-turn reasoning passthrough test passed!")
	})
}

// RunAllOpusReasoningTests runs Opus 4.5 and 4.6 reasoning tests for a given provider
func RunAllOpusReasoningTests(t *testing.T, client *bifrost.Bifrost, ctx context.Context, config OpusReasoningTestConfig) {
	t.Run(string(config.Provider)+"_OpusReasoning", func(t *testing.T) {
		t.Run("Opus45", func(t *testing.T) {
			RunOpus45ReasoningTest(t, client, ctx, config)
		})
		t.Run("Opus46", func(t *testing.T) {
			RunOpus46ReasoningTest(t, client, ctx, config)
		})
		t.Run("Opus46_MultiTurn", func(t *testing.T) {
			RunOpus46MultiTurnReasoningTest(t, client, ctx, config)
		})
	})
}
