package governance

import (
	"strconv"
	"testing"
)

// TestCustomerBudgetExceededWithMultipleVKs tests that customer level budgets are enforced across multiple VKs
// by making requests until budget is consumed
func TestCustomerBudgetExceededWithMultipleVKs(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a customer with a fixed budget
	customerBudget := 0.01
	customerName := "test-customer-budget-exceeded-" + generateRandomID()
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

	// Create 2 VKs under the customer (directly, without team)
	var vkValues []string
	for i := 1; i <= 2; i++ {
		createVKResp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/api/governance/virtual-keys",
			Body: CreateVirtualKeyRequest{
				Name:       "test-vk-" + generateRandomID(),
				CustomerID: &customerID,
				Budget: &BudgetRequest{
					MaxLimit:      1.0, // High VK budget so customer is the limiting factor
					ResetDuration: "1h",
				},
			},
		})

		if createVKResp.StatusCode != 200 {
			t.Fatalf("Failed to create VK %d: status %d", i, createVKResp.StatusCode)
		}

		vkID := ExtractIDFromResponse(t, createVKResp)
		testData.AddVirtualKey(vkID)

		vk := createVKResp.Body["virtual_key"].(map[string]interface{})
		vkValues = append(vkValues, vk["value"].(string))
	}

	t.Logf("Created customer %s with budget $%.2f and 2 VKs", customerName, customerBudget)

	// Keep making requests alternating between VKs, tracking actual token usage until customer budget is exceeded
	consumedBudget := 0.0
	requestNum := 1
	var lastSuccessfulCost float64
	shouldStop := false
	vkIndex := 0

	for requestNum <= 50 {
		// Alternate between VKs to test shared customer budget
		vkValue := vkValues[vkIndex%2]

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
			if CheckErrorMessage(t, resp, "budget") || CheckErrorMessage(t, resp, "customer") {
				t.Logf("Request %d correctly rejected: customer budget exceeded", requestNum)
				t.Logf("Consumed budget: $%.6f (limit: $%.2f)", consumedBudget, customerBudget)
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

					t.Logf("Request %d (VK%d) succeeded: input_tokens=%d, output_tokens=%d, cost=$%.6f, consumed=$%.6f/$%.2f",
						requestNum, (vkIndex%2)+1, actualInputTokens, actualOutputTokens, actualCost, consumedBudget, customerBudget)
				}
			}
		}

		requestNum++
		vkIndex++

		if shouldStop {
			break
		}

		if consumedBudget >= customerBudget {
			shouldStop = true
		}
	}

	t.Fatalf("Made %d requests but never hit customer budget limit (consumed $%.6f / $%.2f) - budget not being enforced",
		requestNum-1, consumedBudget, customerBudget)
}

// TestCustomerBudgetExceededWithMultipleTeams tests that customer level budgets are enforced across multiple teams
// by making requests until budget is consumed
func TestCustomerBudgetExceededWithMultipleTeams(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a customer with a fixed budget
	customerBudget := 0.01
	customerName := "test-customer-multi-team-" + generateRandomID()
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

	// Create 2 teams under the customer
	var vkValues []string
	for i := 1; i <= 2; i++ {
		createTeamResp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/api/governance/teams",
			Body: CreateTeamRequest{
				Name:       "test-team-" + generateRandomID(),
				CustomerID: &customerID,
				Budget: &BudgetRequest{
					MaxLimit:      1.0, // High team budget so customer is the limiting factor
					ResetDuration: "1h",
				},
			},
		})

		if createTeamResp.StatusCode != 200 {
			t.Fatalf("Failed to create team %d: status %d", i, createTeamResp.StatusCode)
		}

		teamID := ExtractIDFromResponse(t, createTeamResp)
		testData.AddTeam(teamID)

		// Create a VK under each team
		createVKResp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/api/governance/virtual-keys",
			Body: CreateVirtualKeyRequest{
				Name:   "test-vk-" + generateRandomID(),
				TeamID: &teamID,
				Budget: &BudgetRequest{
					MaxLimit:      1.0, // High VK budget so customer is the limiting factor
					ResetDuration: "1h",
				},
			},
		})

		if createVKResp.StatusCode != 200 {
			t.Fatalf("Failed to create VK %d: status %d", i, createVKResp.StatusCode)
		}

		vkID := ExtractIDFromResponse(t, createVKResp)
		testData.AddVirtualKey(vkID)

		vk := createVKResp.Body["virtual_key"].(map[string]interface{})
		vkValues = append(vkValues, vk["value"].(string))
	}

	t.Logf("Created customer %s with budget $%.2f and 2 teams with VKs", customerName, customerBudget)

	// Keep making requests alternating between VKs in different teams, tracking actual token usage until customer budget is exceeded
	consumedBudget := 0.0
	requestNum := 1
	var lastSuccessfulCost float64
	shouldStop := false
	vkIndex := 0

	for requestNum <= 50 {
		// Alternate between VKs in different teams to test shared customer budget
		vkValue := vkValues[vkIndex%2]

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
			if CheckErrorMessage(t, resp, "budget") || CheckErrorMessage(t, resp, "customer") {
				t.Logf("Request %d correctly rejected: customer budget exceeded", requestNum)
				t.Logf("Consumed budget: $%.6f (limit: $%.2f)", consumedBudget, customerBudget)
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

					t.Logf("Request %d (VK%d) succeeded: input_tokens=%d, output_tokens=%d, cost=$%.6f, consumed=$%.6f/$%.2f",
						requestNum, (vkIndex%2)+1, actualInputTokens, actualOutputTokens, actualCost, consumedBudget, customerBudget)
				}
			}
		}

		requestNum++
		vkIndex++

		if shouldStop {
			break
		}

		if consumedBudget >= customerBudget {
			shouldStop = true
		}
	}

	t.Fatalf("Made %d requests but never hit customer budget limit (consumed $%.6f / $%.2f) - budget not being enforced",
		requestNum-1, consumedBudget, customerBudget)
}
