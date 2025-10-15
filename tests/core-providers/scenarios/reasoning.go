package scenarios

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/tests/core-providers/config"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunReasoningTest executes the reasoning test scenario to test thinking capabilities via Responses API only
func RunReasoningTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig config.ComprehensiveTestConfig) {
	if !testConfig.Scenarios.Reasoning {
		t.Logf("⏭️ Reasoning not supported for provider %s", testConfig.Provider)
		return
	}

	// Skip if no reasoning model is configured
	if testConfig.ReasoningModel == "" {
		t.Logf("⏭️ No reasoning model configured for provider %s", testConfig.Provider)
		return
	}

	t.Run("Reasoning", func(t *testing.T) {
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
				MaxOutputTokens: bifrost.Ptr(800),
				// Configure reasoning-specific parameters
				Reasoning: &schemas.ResponsesParametersReasoning{
					Effort:  bifrost.Ptr("high"),     // High effort for complex reasoning
					Summary: bifrost.Ptr("detailed"), // Detailed summary of reasoning process
				},
				// Include reasoning content in response
				Include: []string{"reasoning.encrypted_content"},
			},
		}

		// Use retry framework with enhanced validation for reasoning
		retryConfig := GetTestRetryConfigForScenario("Reasoning", testConfig)
		retryContext := TestRetryContext{
			ScenarioName: "Reasoning",
			ExpectedBehavior: map[string]interface{}{
				"should_show_reasoning": true,
				"should_calculate":      true,
				"mathematical_problem":  true,
				"step_by_step":         true,
			},
			TestMetadata: map[string]interface{}{
				"provider":        testConfig.Provider,
				"model":           testConfig.ReasoningModel,
				"problem_type":    "mathematical",
				"complexity":      "high",
				"expects_reasoning": true,
			},
		}

		// Enhanced validation for reasoning scenarios
		expectations := GetExpectationsForScenario("Reasoning", testConfig, map[string]interface{}{
			"requires_reasoning": true,
			"mathematical_problem": true,
		})
		expectations = ModifyExpectationsForProvider(expectations, testConfig.Provider)
		expectations.MinContentLength = 50  // Reasoning requires substantial content
		expectations.MaxContentLength = 2000 // Reasoning can be verbose
		expectations.ShouldContainKeywords = []string{"step", "calculate", "profit", "$"}
		expectations.ShouldNotContainWords = append(expectations.ShouldNotContainWords, []string{
			"cannot solve", "unable to calculate", "need more information",
		}...)

		response, bifrostErr := WithTestRetry(t, retryConfig, retryContext, expectations, "Reasoning", func() (*schemas.BifrostResponse, *schemas.BifrostError) {
			return client.ResponsesRequest(ctx, responsesReq)
		})

		if bifrostErr != nil {
			t.Fatalf("❌ Reasoning test failed after retries: %v", GetErrorMessage(bifrostErr))
		}

		// Log the response content
		responsesContent := GetResultContent(response)
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
func validateResponsesAPIReasoning(t *testing.T, response *schemas.BifrostResponse) bool {
	if response == nil || response.ResponsesResponse == nil {
		return false
	}

	reasoningFound := false
	summaryFound := false
	reasoningContentFound := false

	// Check if response contains reasoning messages or reasoning content
	for _, message := range response.ResponsesResponse.Output {
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

// min returns the smaller of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
