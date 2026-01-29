package governance

import (
	"strconv"
	"testing"
)

// TestVKBudgetExceeded tests that VK level budgets are enforced by making requests until budget is consumed
func TestVKBudgetExceeded(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with a fixed budget
	vkBudget := 0.01
	vkName := "test-vk-budget-exceeded-" + generateRandomID()
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			Budget: &BudgetRequest{
				MaxLimit:      vkBudget,
				ResetDuration: "1h",
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

	t.Logf("Created VK %s with budget $%.2f", vkName, vkBudget)

	// Keep making requests, tracking actual token usage from responses, until budget is exceeded
	consumedBudget := 0.0
	requestNum := 1
	var lastSuccessfulCost float64

	shouldStop := false

	for requestNum <= 50 {
		// Create a longer prompt to consume more tokens and budget faster
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
			// Request failed - check if it's due to budget
			if CheckErrorMessage(t, resp, "budget") {
				t.Logf("Request %d correctly rejected: budget exceeded", requestNum)
				t.Logf("Consumed budget: $%.6f (limit: $%.2f)", consumedBudget, vkBudget)
				t.Logf("Last successful request cost: $%.6f", lastSuccessfulCost)

				// Verify that we made at least one successful request before hitting budget
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
						requestNum, actualInputTokens, actualOutputTokens, actualCost, consumedBudget, vkBudget)
				}
			}
		}

		requestNum++

		if shouldStop {
			break
		}

		if consumedBudget >= vkBudget {
			shouldStop = true
		}
	}

	t.Fatalf("Made %d requests but never hit budget limit (consumed $%.6f / $%.2f) - budget not being enforced",
		requestNum-1, consumedBudget, vkBudget)
}
