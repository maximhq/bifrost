package governance

import (
	"strconv"
	"testing"
	"time"
)

// TestCrissCrossComplexBudgetHierarchy tests complex scenarios involving provider, VK, team, and customer level budgets
// Tests that the most restrictive budget at each level is enforced
func TestCrissCrossComplexBudgetHierarchy(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a customer with a moderate budget
	customerBudget := 0.15
	customerName := "test-customer-criss-cross-" + generateRandomID()
	createCustomerResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/customers",
		Body: CreateCustomerRequest{
			Name: customerName,
			Budget: &BudgetRequest{
				MaxLimit:      customerBudget,
				ResetDuration: "1h",
			},
		},
	})

	if createCustomerResp.StatusCode != 200 {
		t.Fatalf("Failed to create customer: status %d", createCustomerResp.StatusCode)
	}

	customerID := ExtractIDFromResponse(t, createCustomerResp)
	testData.AddCustomer(customerID)

	// Create a team under customer with a tighter budget
	teamBudget := 0.12
	createTeamResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/teams",
		Body: CreateTeamRequest{
			Name:       "test-team-criss-cross-" + generateRandomID(),
			CustomerID: &customerID,
			Budget: &BudgetRequest{
				MaxLimit:      teamBudget,
				ResetDuration: "1h",
			},
		},
	})

	if createTeamResp.StatusCode != 200 {
		t.Fatalf("Failed to create team: status %d", createTeamResp.StatusCode)
	}

	teamID := ExtractIDFromResponse(t, createTeamResp)
	testData.AddTeam(teamID)

	// Create a VK with even tighter budget and provider-specific budgets
	vkBudget := 0.01
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:   "test-vk-criss-cross-" + generateRandomID(),
			TeamID: &teamID,
			Budget: &BudgetRequest{
				MaxLimit:      vkBudget,
				ResetDuration: "1h",
			},
			ProviderConfigs: []ProviderConfigRequest{
				{
					Provider: "openai",
					Weight:   1.0,
					Budget: &BudgetRequest{
						MaxLimit:      0.08, // Even tighter provider budget
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

	t.Logf("Created hierarchy: Customer ($%.2f) -> Team ($%.2f) -> VK ($%.2f) with Provider Budget ($0.08)",
		customerBudget, teamBudget, vkBudget)

	// Wait for VK and provider config budgets to be synced to in-memory store
	time.Sleep(1000 * time.Millisecond)

	// Test: Provider budget should be the limiting factor (most restrictive)
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
			// Request failed - check if it's due to budget
			if CheckErrorMessage(t, resp, "budget") || CheckErrorMessage(t, resp, "provider") {
				t.Logf("Request %d correctly rejected: budget exceeded in criss-cross hierarchy", requestNum)
				t.Logf("Consumed budget: $%.6f (provider budget limit: $0.08)", consumedBudget)
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

					t.Logf("Request %d succeeded: input_tokens=%d, output_tokens=%d, cost=$%.6f, consumed=$%.6f",
						requestNum, actualInputTokens, actualOutputTokens, actualCost, consumedBudget)
				}
			}
		}

		requestNum++

		if shouldStop {
			break
		}

		if consumedBudget >= 0.08 { // Provider budget
			shouldStop = true
		}
	}

	t.Fatalf("Made %d requests but never hit provider budget limit - budget not being enforced",
		requestNum-1)
}
