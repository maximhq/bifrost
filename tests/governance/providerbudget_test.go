package governance

import (
	"strconv"
	"testing"
)

// TestProviderBudgetExceeded tests provider-specific budgets within a VK by making requests until budget is consumed
func TestProviderBudgetExceeded(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with different budgets for different providers
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: "test-vk-provider-budget-" + generateRandomID(),
			Budget: &BudgetRequest{
				MaxLimit:      1.0, // High overall budget
				ResetDuration: "1h",
			},
			ProviderConfigs: []ProviderConfigRequest{
				{
					Provider: "openai",
					Weight:   1.0,
					Budget: &BudgetRequest{
						MaxLimit:      0.01, // Specific OpenAI budget
						ResetDuration: "1h",
					},
				},
				{
					Provider: "anthropic",
					Weight:   1.0,
					Budget: &BudgetRequest{
						MaxLimit:      0.01, // Specific Anthropic budget
						ResetDuration: "1h",
					},
				},
			},
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp)
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created VK with OpenAI budget $0.01 and Anthropic budget $0.01")

	// Test OpenAI provider budget exceeded
	t.Run("OpenAIProviderBudgetExceeded", func(t *testing.T) {
		providerBudget := 0.01
		consumedBudget := 0.0
		requestNum := 1
		var lastSuccessfulCost float64
		shouldStop := false

		for requestNum <= 50 {
			longPrompt := "Please provide a comprehensive and detailed response to the following question. " +
				"I need extensive information covering all aspects of the topic. " +
				"Provide multiple paragraphs with detailed explanations. " +
				"Request number " + strconv.Itoa(requestNum) + ". " +
				"Here is a detailed prompt that will consume significant tokens: " +
				"Lorem ipsum dolor sit amet, consectetur adipiscing elit. " +
				"Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. " +
				"Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris. " +
				"Nisi ut aliquip ex ea commodo consequat. Duis aute irure dolor in reprehenderit. " +
				"In voluptate velit esse cillum dolore eu fugiat nulla pariatur. " +
				"Excepteur sint occaecat cupidatat non proident, sunt in culpa qui officia deserunt. " +
				"Mollit anim id est laborum. Lorem ipsum dolor sit amet, consectetur adipiscing elit. " +
				"Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. " +
				"Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris. " +
				"Nisi ut aliquip ex ea commodo consequat. Duis aute irure dolor in reprehenderit. " +
				"In voluptate velit esse cillum dolore eu fugiat nulla pariatur. " +
				"Excepteur sint occaecat cupidatat non proident, sunt in culpa qui officia deserunt. " +
				"Mollit anim id est laborum."

			resp := MakeRequest(t, APIRequest{
				Method: "POST",
				Path:   "/v1/chat/completions",
				Body: ChatCompletionRequest{
					Model: "openai/gpt-4o",
					Messages: []ChatMessage{
						{
							Role:    "user",
							Content: longPrompt,
						},
					},
				},
				VKHeader: &vkValue,
			})

			if resp.StatusCode >= 400 {
				if CheckErrorMessage(t, resp, "budget") || CheckErrorMessage(t, resp, "provider") {
					t.Logf("Request %d correctly rejected: OpenAI provider budget exceeded", requestNum)
					t.Logf("Consumed budget: $%.6f (limit: $%.2f)", consumedBudget, providerBudget)
					t.Logf("Last successful request cost: $%.6f", lastSuccessfulCost)

					if requestNum == 1 {
						t.Fatalf("First request should have succeeded but was rejected due to budget")
					}
					return // Test passed
				} else {
					t.Fatalf("Request %d failed with unexpected error (not budget): %v", requestNum, resp.Body)
				}
			}

			// Request succeeded - extract actual token usage from response
			if usage, ok := resp.Body["usage"].(map[string]interface{}); ok {
				if prompt, ok := usage["prompt_tokens"].(float64); ok {
					if completion, ok := usage["completion_tokens"].(float64); ok {
						actualInputTokens := int(prompt)
						actualOutputTokens := int(completion)
						actualCost, _ := CalculateCost("openai/gpt-4o", actualInputTokens, actualOutputTokens)

						consumedBudget += actualCost
						lastSuccessfulCost = actualCost

						t.Logf("Request %d succeeded: input_tokens=%d, output_tokens=%d, cost=$%.6f, consumed=$%.6f/$%.2f",
							requestNum, actualInputTokens, actualOutputTokens, actualCost, consumedBudget, providerBudget)
					}
				}
			}

			requestNum++

			if shouldStop {
				break
			}

			if consumedBudget >= providerBudget {
				shouldStop = true
			}
		}

		t.Fatalf("Made %d requests but never hit provider budget limit (consumed $%.6f / $%.2f) - budget not being enforced",
			requestNum-1, consumedBudget, providerBudget)
	})

	// Test Anthropic provider budget exceeded
	t.Run("AnthropicProviderBudgetExceeded", func(t *testing.T) {
		providerBudget := 0.01
		consumedBudget := 0.0
		requestNum := 1
		var lastSuccessfulCost float64
		shouldStop := false

		for requestNum <= 50 {
			longPrompt := "Please provide a comprehensive and detailed response to the following question. " +
				"I need extensive information covering all aspects of the topic. " +
				"Provide multiple paragraphs with detailed explanations. " +
				"Request number " + strconv.Itoa(requestNum) + ". " +
				"Here is a detailed prompt that will consume significant tokens: " +
				"Lorem ipsum dolor sit amet, consectetur adipiscing elit. " +
				"Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. " +
				"Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris. " +
				"Nisi ut aliquip ex ea commodo consequat. Duis aute irure dolor in reprehenderit. " +
				"In voluptate velit esse cillum dolore eu fugiat nulla pariatur. " +
				"Excepteur sint occaecat cupidatat non proident, sunt in culpa qui officia deserunt. " +
				"Mollit anim id est laborum. Lorem ipsum dolor sit amet, consectetur adipiscing elit. " +
				"Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. " +
				"Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris. " +
				"Nisi ut aliquip ex ea commodo consequat. Duis aute irure dolor in reprehenderit. " +
				"In voluptate velit esse cillum dolore eu fugiat nulla pariatur. " +
				"Excepteur sint occaecat cupidatat non proident, sunt in culpa qui officia deserunt. " +
				"Mollit anim id est laborum."

			resp := MakeRequest(t, APIRequest{
				Method: "POST",
				Path:   "/v1/chat/completions",
				Body: ChatCompletionRequest{
					Model: "anthropic/claude-3-7-sonnet-20250219",
					Messages: []ChatMessage{
						{
							Role:    "user",
							Content: longPrompt,
						},
					},
				},
				VKHeader: &vkValue,
			})

			if resp.StatusCode >= 400 {
				if CheckErrorMessage(t, resp, "budget") || CheckErrorMessage(t, resp, "provider") {
					t.Logf("Request %d correctly rejected: Anthropic provider budget exceeded", requestNum)
					t.Logf("Consumed budget: $%.6f (limit: $%.2f)", consumedBudget, providerBudget)
					t.Logf("Last successful request cost: $%.6f", lastSuccessfulCost)

					if requestNum == 1 {
						t.Fatalf("First request should have succeeded but was rejected due to budget")
					}
					return // Test passed
				} else {
					t.Fatalf("Request %d failed with unexpected error (not budget): %v", requestNum, resp.Body)
				}
			}

			// Request succeeded - extract actual token usage from response
			if usage, ok := resp.Body["usage"].(map[string]interface{}); ok {
				if prompt, ok := usage["prompt_tokens"].(float64); ok {
					if completion, ok := usage["completion_tokens"].(float64); ok {
						actualInputTokens := int(prompt)
						actualOutputTokens := int(completion)
						actualCost, _ := CalculateCost("anthropic/claude-3-7-sonnet-20250219", actualInputTokens, actualOutputTokens)

						consumedBudget += actualCost
						lastSuccessfulCost = actualCost

						t.Logf("Request %d succeeded: input_tokens=%d, output_tokens=%d, cost=$%.6f, consumed=$%.6f/$%.2f",
							requestNum, actualInputTokens, actualOutputTokens, actualCost, consumedBudget, providerBudget)
					}
				}
			}

			requestNum++

			if shouldStop {
				break
			}

			if consumedBudget >= providerBudget {
				shouldStop = true
			}
		}

		t.Fatalf("Made %d requests but never hit provider budget limit (consumed $%.6f / $%.2f) - budget not being enforced",
			requestNum-1, consumedBudget, providerBudget)
	})
}
