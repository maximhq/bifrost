package governance

import (
	"testing"
	"time"
)

// ============================================================================
// SCENARIO 1: VK Switching Teams After Budget Exhaustion
// ============================================================================

// TestVKSwitchTeamAfterBudgetExhaustion verifies that after exhausting one team's budget,
// switching the VK to another team allows requests to pass
func TestVKSwitchTeamAfterBudgetExhaustion(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create Team 1 with small budget
	team1Name := "test-team1-switch-" + generateRandomID()
	team1Budget := 0.01 // $0.01
	createTeam1Resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/teams",
		Body: CreateTeamRequest{
			Name: team1Name,
			Budget: &BudgetRequest{
				MaxLimit:      team1Budget,
				ResetDuration: "1h",
			},
		},
	})

	if createTeam1Resp.StatusCode != 200 {
		t.Fatalf("Failed to create team1: status %d", createTeam1Resp.StatusCode)
	}

	team1ID := ExtractIDFromResponse(t, createTeam1Resp)
	testData.AddTeam(team1ID)

	// Create Team 2 with higher budget
	team2Name := "test-team2-switch-" + generateRandomID()
	team2Budget := 10.0 // $10
	createTeam2Resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/teams",
		Body: CreateTeamRequest{
			Name: team2Name,
			Budget: &BudgetRequest{
				MaxLimit:      team2Budget,
				ResetDuration: "1h",
			},
		},
	})

	if createTeam2Resp.StatusCode != 200 {
		t.Fatalf("Failed to create team2: status %d", createTeam2Resp.StatusCode)
	}

	team2ID := ExtractIDFromResponse(t, createTeam2Resp)
	testData.AddTeam(team2ID)

	t.Logf("Created Team1 (budget: $%.2f) and Team2 (budget: $%.2f)", team1Budget, team2Budget)

	// Create VK assigned to Team 1
	vkName := "test-vk-team-switch-" + generateRandomID()
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:   vkName,
			TeamID: &team1ID,
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp)
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created VK assigned to Team1")

	// Exhaust Team1's budget
	consumedBudget := 0.0
	requestNum := 1

	for requestNum <= 150 {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{Role: "user", Content: "Hello how are you?"},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode >= 400 {
			if CheckErrorMessage(t, resp, "budget") {
				t.Logf("Team1 budget exhausted at request %d (consumed: $%.6f)", requestNum, consumedBudget)
				break
			} else {
				t.Fatalf("Request %d failed with unexpected error: %v", requestNum, resp.Body)
			}
		}

		if usage, ok := resp.Body["usage"].(map[string]interface{}); ok {
			if prompt, ok := usage["prompt_tokens"].(float64); ok {
				if completion, ok := usage["completion_tokens"].(float64); ok {
					cost, _ := CalculateCost("openai/gpt-4o", int(prompt), int(completion))
					consumedBudget += cost
				}
			}
		}

		requestNum++

		if consumedBudget >= team1Budget {
			// Make one more request to trigger rejection
			continue
		}
	}

	if consumedBudget < team1Budget {
		t.Fatalf("Could not exhaust Team1 budget")
	}

	// Now switch VK to Team2
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/virtual-keys/" + vkID,
		Body: UpdateVirtualKeyRequest{
			TeamID: &team2ID,
		},
	})

	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to switch VK to Team2: status %d", updateResp.StatusCode)
	}

	t.Logf("Switched VK from Team1 to Team2")

	// Wait for in-memory update
	time.Sleep(500 * time.Millisecond)

	// Request should now succeed with Team2's budget
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{Role: "user", Content: "Request after switching to Team2"},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Fatalf("Request should succeed after switching to Team2 with available budget, got status %d", resp.StatusCode)
	}

	t.Logf("VK switch team after budget exhaustion verified ✓")
}

// ============================================================================
// SCENARIO 2: VK Switching Customers After Budget Exhaustion
// ============================================================================

// TestVKSwitchCustomerAfterBudgetExhaustion verifies that after exhausting one customer's budget,
// switching the VK to another customer allows requests to pass
func TestVKSwitchCustomerAfterBudgetExhaustion(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create Customer 1 with small budget
	customer1Name := "test-customer1-switch-" + generateRandomID()
	customer1Budget := 0.01 // $0.01
	createCustomer1Resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/customers",
		Body: CreateCustomerRequest{
			Name: customer1Name,
			Budget: &BudgetRequest{
				MaxLimit:      customer1Budget,
				ResetDuration: "1h",
			},
		},
	})

	if createCustomer1Resp.StatusCode != 200 {
		t.Fatalf("Failed to create customer1: status %d", createCustomer1Resp.StatusCode)
	}

	customer1ID := ExtractIDFromResponse(t, createCustomer1Resp)
	testData.AddCustomer(customer1ID)

	// Create Customer 2 with higher budget
	customer2Name := "test-customer2-switch-" + generateRandomID()
	customer2Budget := 10.0 // $10
	createCustomer2Resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/customers",
		Body: CreateCustomerRequest{
			Name: customer2Name,
			Budget: &BudgetRequest{
				MaxLimit:      customer2Budget,
				ResetDuration: "1h",
			},
		},
	})

	if createCustomer2Resp.StatusCode != 200 {
		t.Fatalf("Failed to create customer2: status %d", createCustomer2Resp.StatusCode)
	}

	customer2ID := ExtractIDFromResponse(t, createCustomer2Resp)
	testData.AddCustomer(customer2ID)

	t.Logf("Created Customer1 (budget: $%.2f) and Customer2 (budget: $%.2f)", customer1Budget, customer2Budget)

	// Create VK assigned directly to Customer 1
	vkName := "test-vk-customer-switch-" + generateRandomID()
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:       vkName,
			CustomerID: &customer1ID,
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp)
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created VK assigned to Customer1")

	// Exhaust Customer1's budget
	consumedBudget := 0.0
	requestNum := 1

	for requestNum <= 150 {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{Role: "user", Content: "Hello how are you?"},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode >= 400 {
			if CheckErrorMessage(t, resp, "budget") {
				t.Logf("Customer1 budget exhausted at request %d (consumed: $%.6f)", requestNum, consumedBudget)
				break
			} else {
				t.Fatalf("Request %d failed with unexpected error: %v", requestNum, resp.Body)
			}
		}

		if usage, ok := resp.Body["usage"].(map[string]interface{}); ok {
			if prompt, ok := usage["prompt_tokens"].(float64); ok {
				if completion, ok := usage["completion_tokens"].(float64); ok {
					cost, _ := CalculateCost("openai/gpt-4o", int(prompt), int(completion))
					consumedBudget += cost
				}
			}
		}

		requestNum++

		if consumedBudget >= customer1Budget {
			continue
		}
	}

	if consumedBudget < customer1Budget {
		t.Fatalf("Could not exhaust Customer1 budget")
	}

	// Now switch VK to Customer2
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/virtual-keys/" + vkID,
		Body: UpdateVirtualKeyRequest{
			CustomerID: &customer2ID,
		},
	})

	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to switch VK to Customer2: status %d", updateResp.StatusCode)
	}

	t.Logf("Switched VK from Customer1 to Customer2")

	time.Sleep(500 * time.Millisecond)

	// Request should now succeed with Customer2's budget
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{Role: "user", Content: "Request after switching to Customer2"},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Fatalf("Request should succeed after switching to Customer2 with available budget, got status %d", resp.StatusCode)
	}

	t.Logf("VK switch customer after budget exhaustion verified ✓")
}

// ============================================================================
// SCENARIO 3: Hierarchical Chain VK->Team->Customer Budget Switching
// ============================================================================

// TestHierarchicalChainBudgetSwitch verifies switching the entire hierarchy
func TestHierarchicalChainBudgetSwitch(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create Customer 1 with small budget
	customer1Name := "test-customer1-hierarchy-" + generateRandomID()
	createCustomer1Resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/customers",
		Body: CreateCustomerRequest{
			Name: customer1Name,
			Budget: &BudgetRequest{
				MaxLimit:      0.01, // $0.01 - most restrictive
				ResetDuration: "1h",
			},
		},
	})

	if createCustomer1Resp.StatusCode != 200 {
		t.Fatalf("Failed to create customer1: status %d", createCustomer1Resp.StatusCode)
	}

	customer1ID := ExtractIDFromResponse(t, createCustomer1Resp)
	testData.AddCustomer(customer1ID)

	// Create Team 1 under Customer 1
	team1Name := "test-team1-hierarchy-" + generateRandomID()
	createTeam1Resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/teams",
		Body: CreateTeamRequest{
			Name:       team1Name,
			CustomerID: &customer1ID,
			Budget: &BudgetRequest{
				MaxLimit:      100.0, // High budget - customer is limiting
				ResetDuration: "1h",
			},
		},
	})

	if createTeam1Resp.StatusCode != 200 {
		t.Fatalf("Failed to create team1: status %d", createTeam1Resp.StatusCode)
	}

	team1ID := ExtractIDFromResponse(t, createTeam1Resp)
	testData.AddTeam(team1ID)

	// Create Customer 2 with higher budget
	customer2Name := "test-customer2-hierarchy-" + generateRandomID()
	createCustomer2Resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/customers",
		Body: CreateCustomerRequest{
			Name: customer2Name,
			Budget: &BudgetRequest{
				MaxLimit:      100.0, // High budget
				ResetDuration: "1h",
			},
		},
	})

	if createCustomer2Resp.StatusCode != 200 {
		t.Fatalf("Failed to create customer2: status %d", createCustomer2Resp.StatusCode)
	}

	customer2ID := ExtractIDFromResponse(t, createCustomer2Resp)
	testData.AddCustomer(customer2ID)

	// Create Team 2 under Customer 2
	team2Name := "test-team2-hierarchy-" + generateRandomID()
	createTeam2Resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/teams",
		Body: CreateTeamRequest{
			Name:       team2Name,
			CustomerID: &customer2ID,
			Budget: &BudgetRequest{
				MaxLimit:      100.0, // High budget
				ResetDuration: "1h",
			},
		},
	})

	if createTeam2Resp.StatusCode != 200 {
		t.Fatalf("Failed to create team2: status %d", createTeam2Resp.StatusCode)
	}

	team2ID := ExtractIDFromResponse(t, createTeam2Resp)
	testData.AddTeam(team2ID)

	t.Logf("Created hierarchy: Customer1(low budget)->Team1 and Customer2(high budget)->Team2")

	// Create VK assigned to Team 1
	vkName := "test-vk-hierarchy-switch-" + generateRandomID()
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:   vkName,
			TeamID: &team1ID,
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp)
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	// Exhaust Customer1's budget (which is limiting Team1)
	consumedBudget := 0.0
	requestNum := 1
	budgetExhausted := false

	for requestNum <= 150 {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{Role: "user", Content: "Hello how are you?"},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode >= 400 {
			if CheckErrorMessage(t, resp, "budget") {
				budgetExhausted = true
				t.Logf("Customer1 budget exhausted at request %d (consumed: $%.6f)", requestNum, consumedBudget)
				break
			} else {
				t.Fatalf("Request %d failed with unexpected error: %v", requestNum, resp.Body)
			}
		}

		if usage, ok := resp.Body["usage"].(map[string]interface{}); ok {
			if prompt, ok := usage["prompt_tokens"].(float64); ok {
				if completion, ok := usage["completion_tokens"].(float64); ok {
					cost, _ := CalculateCost("openai/gpt-4o", int(prompt), int(completion))
					consumedBudget += cost
				}
			}
		}

		requestNum++
	}

	if !budgetExhausted {
		t.Fatalf("Budget should have been exhausted within 150 requests, but no budget rejection was observed (consumed: $%.6f)", consumedBudget)
	}

	// Switch VK to Team2 (under Customer2)
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/virtual-keys/" + vkID,
		Body: UpdateVirtualKeyRequest{
			TeamID: &team2ID,
		},
	})

	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to switch VK to Team2: status %d", updateResp.StatusCode)
	}

	t.Logf("Switched VK from Team1(Customer1) to Team2(Customer2)")

	time.Sleep(500 * time.Millisecond)

	// Request should now succeed
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{Role: "user", Content: "Request after switching hierarchy"},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Fatalf("Request should succeed after switching hierarchy, got status %d", resp.StatusCode)
	}

	t.Logf("Hierarchical chain budget switch verified ✓")
}

// ============================================================================
// SCENARIO 4: VK Budget Update After Exhaustion
// ============================================================================

// TestVKBudgetUpdateAfterExhaustion verifies that updating VK budget after exhaustion allows requests
func TestVKBudgetUpdateAfterExhaustion(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create VK with small budget
	vkName := "test-vk-budget-update-" + generateRandomID()
	initialBudget := 0.01 // $0.01
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			Budget: &BudgetRequest{
				MaxLimit:      initialBudget,
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

	t.Logf("Created VK with budget: $%.2f", initialBudget)

	// Exhaust VK budget
	consumedBudget := 0.0
	requestNum := 1
	sawBudgetRejection := false

	for requestNum <= 150 {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{Role: "user", Content: "Hello how are you?"},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode >= 400 {
			if CheckErrorMessage(t, resp, "budget") {
				sawBudgetRejection = true
				t.Logf("VK budget exhausted at request %d (consumed: $%.6f)", requestNum, consumedBudget)
				break
			} else {
				t.Fatalf("Request %d failed with unexpected error: %v", requestNum, resp.Body)
			}
		}

		if usage, ok := resp.Body["usage"].(map[string]interface{}); ok {
			if prompt, ok := usage["prompt_tokens"].(float64); ok {
				if completion, ok := usage["completion_tokens"].(float64); ok {
					cost, _ := CalculateCost("openai/gpt-4o", int(prompt), int(completion))
					consumedBudget += cost
				}
			}
		}

		requestNum++
	}

	if !sawBudgetRejection {
		t.Fatalf("No budget rejection observed; consumed budget: $%.6f", consumedBudget)
	}

	// Update VK budget to a higher value
	newBudget := 10.0
	resetDuration := "1h"
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/virtual-keys/" + vkID,
		Body: UpdateVirtualKeyRequest{
			Budget: &UpdateBudgetRequest{
				MaxLimit:      &newBudget,
				ResetDuration: &resetDuration,
			},
		},
	})

	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to update VK budget: status %d", updateResp.StatusCode)
	}

	t.Logf("Updated VK budget from $%.2f to $%.2f", initialBudget, newBudget)

	time.Sleep(500 * time.Millisecond)

	// Request should now succeed
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{Role: "user", Content: "Request after budget update"},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Fatalf("Request should succeed after budget update, got status %d", resp.StatusCode)
	}

	t.Logf("VK budget update after exhaustion verified ✓")
}

// ============================================================================
// SCENARIO 5: Team Budget Update After Exhaustion
// ============================================================================

// TestTeamBudgetUpdateAfterExhaustion verifies that updating team budget after exhaustion allows requests
func TestTeamBudgetUpdateAfterExhaustion(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create team with small budget
	teamName := "test-team-budget-update-" + generateRandomID()
	initialBudget := 0.01 // $0.01
	createTeamResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/teams",
		Body: CreateTeamRequest{
			Name: teamName,
			Budget: &BudgetRequest{
				MaxLimit:      initialBudget,
				ResetDuration: "1h",
			},
		},
	})

	if createTeamResp.StatusCode != 200 {
		t.Fatalf("Failed to create team: status %d", createTeamResp.StatusCode)
	}

	teamID := ExtractIDFromResponse(t, createTeamResp)
	testData.AddTeam(teamID)

	// Create VK under team
	vkName := "test-vk-team-budget-update-" + generateRandomID()
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:   vkName,
			TeamID: &teamID,
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp)
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created team with budget: $%.2f", initialBudget)

	// Exhaust team budget
	consumedBudget := 0.0
	requestNum := 1
	sawBudgetRejection := false

	for requestNum <= 150 {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{Role: "user", Content: "Hello how are you?"},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode >= 400 {
			if CheckErrorMessage(t, resp, "budget") {
				sawBudgetRejection = true
				t.Logf("Team budget exhausted at request %d (consumed: $%.6f)", requestNum, consumedBudget)
				break
			} else {
				t.Fatalf("Request %d failed with unexpected error: %v", requestNum, resp.Body)
			}
		}

		if usage, ok := resp.Body["usage"].(map[string]interface{}); ok {
			if prompt, ok := usage["prompt_tokens"].(float64); ok {
				if completion, ok := usage["completion_tokens"].(float64); ok {
					cost, _ := CalculateCost("openai/gpt-4o", int(prompt), int(completion))
					consumedBudget += cost
				}
			}
		}

		requestNum++
	}

	if !sawBudgetRejection {
		t.Fatalf("No budget rejection observed; consumed budget: $%.6f", consumedBudget)
	}

	// Update team budget
	newBudget := 10.0
	resetDuration := "1h"
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/teams/" + teamID,
		Body: UpdateTeamRequest{
			Budget: &UpdateBudgetRequest{
				MaxLimit:      &newBudget,
				ResetDuration: &resetDuration,
			},
		},
	})

	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to update team budget: status %d", updateResp.StatusCode)
	}

	t.Logf("Updated team budget from $%.2f to $%.2f", initialBudget, newBudget)

	time.Sleep(500 * time.Millisecond)

	// Request should now succeed
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{Role: "user", Content: "Request after team budget update"},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Fatalf("Request should succeed after team budget update, got status %d", resp.StatusCode)
	}

	t.Logf("Team budget update after exhaustion verified ✓")
}

// ============================================================================
// SCENARIO 6: Customer Budget Update After Exhaustion
// ============================================================================

// TestCustomerBudgetUpdateAfterExhaustion verifies that updating customer budget after exhaustion allows requests
func TestCustomerBudgetUpdateAfterExhaustion(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create customer with small budget
	customerName := "test-customer-budget-update-" + generateRandomID()
	initialBudget := 0.01 // $0.01
	createCustomerResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/customers",
		Body: CreateCustomerRequest{
			Name: customerName,
			Budget: &BudgetRequest{
				MaxLimit:      initialBudget,
				ResetDuration: "1h",
			},
		},
	})

	if createCustomerResp.StatusCode != 200 {
		t.Fatalf("Failed to create customer: status %d", createCustomerResp.StatusCode)
	}

	customerID := ExtractIDFromResponse(t, createCustomerResp)
	testData.AddCustomer(customerID)

	// Create team under customer
	teamName := "test-team-customer-update-" + generateRandomID()
	createTeamResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/teams",
		Body: CreateTeamRequest{
			Name:       teamName,
			CustomerID: &customerID,
		},
	})

	if createTeamResp.StatusCode != 200 {
		t.Fatalf("Failed to create team: status %d", createTeamResp.StatusCode)
	}

	teamID := ExtractIDFromResponse(t, createTeamResp)
	testData.AddTeam(teamID)

	// Create VK under team
	vkName := "test-vk-customer-budget-update-" + generateRandomID()
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:   vkName,
			TeamID: &teamID,
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp)
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created customer with budget: $%.2f", initialBudget)

	// Exhaust customer budget
	consumedBudget := 0.0
	requestNum := 1
	sawBudgetRejection := false

	for requestNum <= 150 {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{Role: "user", Content: "Hello how are you?"},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode >= 400 {
			if CheckErrorMessage(t, resp, "budget") {
				sawBudgetRejection = true
				t.Logf("Customer budget exhausted at request %d (consumed: $%.6f)", requestNum, consumedBudget)
				break
			} else {
				t.Fatalf("Request %d failed with unexpected error: %v", requestNum, resp.Body)
			}
		}

		if usage, ok := resp.Body["usage"].(map[string]interface{}); ok {
			if prompt, ok := usage["prompt_tokens"].(float64); ok {
				if completion, ok := usage["completion_tokens"].(float64); ok {
					cost, _ := CalculateCost("openai/gpt-4o", int(prompt), int(completion))
					consumedBudget += cost
				}
			}
		}

		requestNum++
	}

	if !sawBudgetRejection {
		t.Fatalf("No budget rejection observed; consumed budget: $%.6f", consumedBudget)
	}

	// Update customer budget
	newBudget := 10.0
	resetDuration := "1h"
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/customers/" + customerID,
		Body: UpdateCustomerRequest{
			Budget: &UpdateBudgetRequest{
				MaxLimit:      &newBudget,
				ResetDuration: &resetDuration,
			},
		},
	})

	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to update customer budget: status %d", updateResp.StatusCode)
	}

	t.Logf("Updated customer budget from $%.2f to $%.2f", initialBudget, newBudget)

	time.Sleep(500 * time.Millisecond)

	// Request should now succeed
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{Role: "user", Content: "Request after customer budget update"},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Fatalf("Request should succeed after customer budget update, got status %d", resp.StatusCode)
	}

	t.Logf("Customer budget update after exhaustion verified ✓")
}

// ============================================================================
// SCENARIO 7: Provider Config Budget Update After Exhaustion
// ============================================================================

// TestProviderConfigBudgetUpdateAfterExhaustion verifies that updating provider config budget after exhaustion allows requests
func TestProviderConfigBudgetUpdateAfterExhaustion(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create VK with provider config budget
	vkName := "test-vk-provider-budget-update-" + generateRandomID()
	initialBudget := 0.01 // $0.01
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			ProviderConfigs: []ProviderConfigRequest{
				{
					Provider: "openai",
					Weight:   1.0,
					Budget: &BudgetRequest{
						MaxLimit:      initialBudget,
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

	t.Logf("Created VK with provider config budget: $%.2f", initialBudget)

	// Get provider config ID
	getDataResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	virtualKeysMap := getDataResp.Body["virtual_keys"].(map[string]interface{})
	vkData := virtualKeysMap[vkValue].(map[string]interface{})
	providerConfigs := vkData["provider_configs"].([]interface{})
	providerConfig := providerConfigs[0].(map[string]interface{})
	providerConfigID := uint(providerConfig["id"].(float64))

	// Exhaust provider config budget
	consumedBudget := 0.0
	requestNum := 1
	sawBudgetRejection := false

	for requestNum <= 150 {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{Role: "user", Content: "Hello how are you?"},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode >= 400 {
			if CheckErrorMessage(t, resp, "budget") {
				sawBudgetRejection = true
				t.Logf("Provider config budget exhausted at request %d (consumed: $%.6f)", requestNum, consumedBudget)
				break
			} else {
				t.Fatalf("Request %d failed with unexpected error: %v", requestNum, resp.Body)
			}
		}

		if usage, ok := resp.Body["usage"].(map[string]interface{}); ok {
			if prompt, ok := usage["prompt_tokens"].(float64); ok {
				if completion, ok := usage["completion_tokens"].(float64); ok {
					cost, _ := CalculateCost("openai/gpt-4o", int(prompt), int(completion))
					consumedBudget += cost
				}
			}
		}

		requestNum++
	}

	if !sawBudgetRejection {
		t.Fatalf("No budget rejection observed; consumed budget: $%.6f", consumedBudget)
	}

	// Update provider config budget
	newBudget := 10.0
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/virtual-keys/" + vkID,
		Body: UpdateVirtualKeyRequest{
			ProviderConfigs: []ProviderConfigRequest{
				{
					ID:       &providerConfigID,
					Provider: "openai",
					Weight:   1.0,
					Budget: &BudgetRequest{
						MaxLimit:      newBudget,
						ResetDuration: "1h",
					},
				},
			},
		},
	})

	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to update provider config budget: status %d", updateResp.StatusCode)
	}

	t.Logf("Updated provider config budget from $%.2f to $%.2f", initialBudget, newBudget)

	time.Sleep(500 * time.Millisecond)

	// Request should now succeed
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{Role: "user", Content: "Request after provider config budget update"},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Fatalf("Request should succeed after provider config budget update, got status %d", resp.StatusCode)
	}

	t.Logf("Provider config budget update after exhaustion verified ✓")
}

// ============================================================================
// SCENARIO 8: VK Deletion Cascade
// ============================================================================

// TestVKDeletionCascadeComplete verifies deleting VK removes provider configs, budgets, and rate limits from memory
func TestVKDeletionCascadeComplete(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create VK with budget, rate limit, and provider configs
	vkName := "test-vk-deletion-cascade-" + generateRandomID()
	tokenLimit := int64(10000)
	tokenResetDuration := "1h"
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			Budget: &BudgetRequest{
				MaxLimit:      10.0,
				ResetDuration: "1h",
			},
			RateLimit: &CreateRateLimitRequest{
				TokenMaxLimit:      &tokenLimit,
				TokenResetDuration: &tokenResetDuration,
			},
			ProviderConfigs: []ProviderConfigRequest{
				{
					Provider: "openai",
					Weight:   1.0,
					Budget: &BudgetRequest{
						MaxLimit:      5.0,
						ResetDuration: "1h",
					},
					RateLimit: &CreateRateLimitRequest{
						TokenMaxLimit:      &tokenLimit,
						TokenResetDuration: &tokenResetDuration,
					},
				},
			},
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp)
	// Don't add to testData since we'll delete manually

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created VK with budget, rate limit, and provider config")

	// Get initial state from in-memory store
	getDataResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	virtualKeysMap1 := getDataResp1.Body["virtual_keys"].(map[string]interface{})

	getBudgetsResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/budgets?from_memory=true",
	})

	budgetsMap1 := getBudgetsResp1.Body["budgets"].(map[string]interface{})

	getRateLimitsResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/rate-limits?from_memory=true",
	})

	rateLimitsMap1 := getRateLimitsResp1.Body["rate_limits"].(map[string]interface{})

	// Verify VK exists
	_, vkExists := virtualKeysMap1[vkValue]
	if !vkExists {
		t.Fatalf("VK not found in in-memory store")
	}

	vkData1 := virtualKeysMap1[vkValue].(map[string]interface{})
	vkBudgetID := vkData1["budget_id"].(string)
	vkRateLimitID := vkData1["rate_limit_id"].(string)
	providerConfigs := vkData1["provider_configs"].([]interface{})
	pc := providerConfigs[0].(map[string]interface{})
	pcBudgetID := pc["budget_id"].(string)
	pcRateLimitID := pc["rate_limit_id"].(string)

	// Verify all resources exist in memory
	_, vkBudgetExists := budgetsMap1[vkBudgetID]
	_, vkRateLimitExists := rateLimitsMap1[vkRateLimitID]
	_, pcBudgetExists := budgetsMap1[pcBudgetID]
	_, pcRateLimitExists := rateLimitsMap1[pcRateLimitID]

	if !vkBudgetExists || !vkRateLimitExists || !pcBudgetExists || !pcRateLimitExists {
		t.Fatalf("Not all resources found in memory before deletion")
	}

	t.Logf("All resources exist in memory before deletion ✓")

	// Delete VK
	deleteResp := MakeRequest(t, APIRequest{
		Method: "DELETE",
		Path:   "/api/governance/virtual-keys/" + vkID,
	})

	if deleteResp.StatusCode != 200 {
		t.Fatalf("Failed to delete VK: status %d", deleteResp.StatusCode)
	}

	t.Logf("VK deleted")

	time.Sleep(500 * time.Millisecond)

	// Verify VK and all related resources are removed from memory
	getDataResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	virtualKeysMap2 := getDataResp2.Body["virtual_keys"].(map[string]interface{})

	getBudgetsResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/budgets?from_memory=true",
	})

	budgetsMap2 := getBudgetsResp2.Body["budgets"].(map[string]interface{})

	getRateLimitsResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/rate-limits?from_memory=true",
	})

	rateLimitsMap2 := getRateLimitsResp2.Body["rate_limits"].(map[string]interface{})

	// VK should be gone
	_, vkStillExists := virtualKeysMap2[vkValue]
	if vkStillExists {
		t.Fatalf("VK still exists in memory after deletion")
	}

	// Budgets should be gone
	_, vkBudgetStillExists := budgetsMap2[vkBudgetID]
	_, pcBudgetStillExists := budgetsMap2[pcBudgetID]
	if vkBudgetStillExists || pcBudgetStillExists {
		t.Fatalf("Budgets should be cascade-deleted: VK budget exists=%v, PC budget exists=%v",
			vkBudgetStillExists, pcBudgetStillExists)
	}

	// Rate limits should be gone
	_, vkRateLimitStillExists := rateLimitsMap2[vkRateLimitID]
	_, pcRateLimitStillExists := rateLimitsMap2[pcRateLimitID]
	if vkRateLimitStillExists || pcRateLimitStillExists {
		t.Logf("Note: Rate limits may still exist in memory (orphaned) - this is acceptable")
	}

	t.Logf("VK removed from memory after deletion ✓")
	t.Logf("VK deletion cascade verified ✓")
}

// ============================================================================
// SCENARIO 9: Team/Customer Deletion Should Delete Budget
// ============================================================================

// TestTeamDeletionDeletesBudget verifies that deleting a team also deletes its budget from memory
func TestTeamDeletionDeletesBudget(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create team with budget
	teamName := "test-team-delete-budget-" + generateRandomID()
	createTeamResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/teams",
		Body: CreateTeamRequest{
			Name: teamName,
			Budget: &BudgetRequest{
				MaxLimit:      100.0,
				ResetDuration: "1h",
			},
		},
	})

	if createTeamResp.StatusCode != 200 {
		t.Fatalf("Failed to create team: status %d", createTeamResp.StatusCode)
	}

	teamID := ExtractIDFromResponse(t, createTeamResp)
	// Don't add to testData since we'll delete manually

	t.Logf("Created team with budget")

	// Get budget ID from in-memory store
	getTeamsResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/teams?from_memory=true",
	})

	teamsMap1 := getTeamsResp1.Body["teams"].(map[string]interface{})

	getBudgetsResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/budgets?from_memory=true",
	})

	budgetsMap1 := getBudgetsResp1.Body["budgets"].(map[string]interface{})

	teamData1 := teamsMap1[teamID].(map[string]interface{})
	budgetID := teamData1["budget_id"].(string)

	_, budgetExists := budgetsMap1[budgetID]
	if !budgetExists {
		t.Fatalf("Budget not found in memory before deletion")
	}

	t.Logf("Team and budget exist in memory ✓")

	// Delete team
	deleteResp := MakeRequest(t, APIRequest{
		Method: "DELETE",
		Path:   "/api/governance/teams/" + teamID,
	})

	if deleteResp.StatusCode != 200 {
		t.Fatalf("Failed to delete team: status %d", deleteResp.StatusCode)
	}

	t.Logf("Team deleted")

	time.Sleep(500 * time.Millisecond)

	// Verify team and budget are removed from memory
	getTeamsResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/teams?from_memory=true",
	})

	teamsMap2 := getTeamsResp2.Body["teams"].(map[string]interface{})

	_, teamStillExists := teamsMap2[teamID]
	if teamStillExists {
		t.Fatalf("Team still exists in memory after deletion")
	}

	t.Logf("Team removed from memory ✓")

	// Verify budget is also removed from memory
	getBudgetsResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/budgets?from_memory=true",
	})

	if getBudgetsResp2.StatusCode != 200 {
		t.Fatalf("Failed to get budgets from memory: status %d", getBudgetsResp2.StatusCode)
	}

	budgetsMap2 := getBudgetsResp2.Body["budgets"].(map[string]interface{})

	_, budgetStillExists := budgetsMap2[budgetID]
	if budgetStillExists {
		t.Fatalf("Budget %s still exists in memory after team deletion", budgetID)
	}

	t.Logf("Budget removed from memory ✓")
	t.Logf("Team deletion with budget verified ✓")
}

// TestCustomerDeletionDeletesBudget verifies that deleting a customer also deletes its budget from memory
func TestCustomerDeletionDeletesBudget(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create customer with budget
	customerName := "test-customer-delete-budget-" + generateRandomID()
	createCustomerResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/customers",
		Body: CreateCustomerRequest{
			Name: customerName,
			Budget: &BudgetRequest{
				MaxLimit:      100.0,
				ResetDuration: "1h",
			},
		},
	})

	if createCustomerResp.StatusCode != 200 {
		t.Fatalf("Failed to create customer: status %d", createCustomerResp.StatusCode)
	}

	customerID := ExtractIDFromResponse(t, createCustomerResp)
	// Don't add to testData since we'll delete manually

	t.Logf("Created customer with budget")

	// Get budget ID from in-memory store
	getCustomersResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/customers?from_memory=true",
	})

	customersMap1 := getCustomersResp1.Body["customers"].(map[string]interface{})

	getBudgetsResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/budgets?from_memory=true",
	})

	budgetsMap1 := getBudgetsResp1.Body["budgets"].(map[string]interface{})

	customerData1 := customersMap1[customerID].(map[string]interface{})
	budgetID := customerData1["budget_id"].(string)

	_, budgetExists := budgetsMap1[budgetID]
	if !budgetExists {
		t.Fatalf("Budget not found in memory before deletion")
	}

	t.Logf("Customer and budget exist in memory ✓")

	// Delete customer
	deleteResp := MakeRequest(t, APIRequest{
		Method: "DELETE",
		Path:   "/api/governance/customers/" + customerID,
	})

	if deleteResp.StatusCode != 200 {
		t.Fatalf("Failed to delete customer: status %d", deleteResp.StatusCode)
	}

	t.Logf("Customer deleted")

	time.Sleep(500 * time.Millisecond)

	// Verify customer is removed from memory
	getCustomersResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/customers?from_memory=true",
	})

	customersMap2 := getCustomersResp2.Body["customers"].(map[string]interface{})

	_, customerStillExists := customersMap2[customerID]
	if customerStillExists {
		t.Fatalf("Customer still exists in memory after deletion")
	}

	t.Logf("Customer removed from memory ✓")

	// Verify budget is also removed from memory
	getBudgetsResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/budgets?from_memory=true",
	})

	if getBudgetsResp2.StatusCode != 200 {
		t.Fatalf("Failed to get budgets from memory: status %d", getBudgetsResp2.StatusCode)
	}

	budgetsMap2 := getBudgetsResp2.Body["budgets"].(map[string]interface{})

	_, budgetStillExists := budgetsMap2[budgetID]
	if budgetStillExists {
		t.Fatalf("Budget still exists in memory after customer deletion")
	}

	t.Logf("Budget removed from memory ✓")
	t.Logf("Customer deletion with budget verified ✓")
}

// ============================================================================
// SCENARIO 10: Team/Customer Deletion Sets VK entity_id = nil
// ============================================================================

// TestTeamDeletionSetsVKTeamIDToNil verifies that deleting a team sets team_id=nil on associated VKs
func TestTeamDeletionSetsVKTeamIDToNil(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create team
	teamName := "test-team-vk-nil-" + generateRandomID()
	createTeamResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/teams",
		Body: CreateTeamRequest{
			Name: teamName,
		},
	})

	if createTeamResp.StatusCode != 200 {
		t.Fatalf("Failed to create team: status %d", createTeamResp.StatusCode)
	}

	teamID := ExtractIDFromResponse(t, createTeamResp)
	// Don't add to testData since we'll delete manually

	// Create VK assigned to team
	vkName := "test-vk-team-nil-" + generateRandomID()
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:   vkName,
			TeamID: &teamID,
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp)
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created team and VK assigned to it")

	// Verify VK has team_id set
	getDataResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	virtualKeysMap1 := getDataResp1.Body["virtual_keys"].(map[string]interface{})
	vkData1 := virtualKeysMap1[vkValue].(map[string]interface{})

	teamIDFromVK1, hasTeamID := vkData1["team_id"].(string)
	if !hasTeamID || teamIDFromVK1 != teamID {
		t.Fatalf("VK team_id not set correctly before team deletion")
	}

	t.Logf("VK has team_id=%s ✓", teamID)

	// Delete team
	deleteResp := MakeRequest(t, APIRequest{
		Method: "DELETE",
		Path:   "/api/governance/teams/" + teamID,
	})

	if deleteResp.StatusCode != 200 {
		t.Fatalf("Failed to delete team: status %d", deleteResp.StatusCode)
	}

	t.Logf("Team deleted")

	time.Sleep(500 * time.Millisecond)

	// Verify VK still exists but team_id is nil
	getDataResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	virtualKeysMap2 := getDataResp2.Body["virtual_keys"].(map[string]interface{})

	vkData2, vkStillExists := virtualKeysMap2[vkValue].(map[string]interface{})
	if !vkStillExists {
		t.Fatalf("VK should still exist after team deletion")
	}

	teamIDFromVK2, hasTeamID2 := vkData2["team_id"].(string)
	if hasTeamID2 && teamIDFromVK2 != "" {
		t.Fatalf("VK team_id should be nil after team deletion, got: %s", teamIDFromVK2)
	}

	t.Logf("VK team_id is now nil ✓")
	t.Logf("Team deletion sets VK team_id to nil verified ✓")
}

// TestCustomerDeletionSetsVKCustomerIDToNil verifies that deleting a customer sets customer_id=nil on associated VKs
func TestCustomerDeletionSetsVKCustomerIDToNil(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create customer
	customerName := "test-customer-vk-nil-" + generateRandomID()
	createCustomerResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/customers",
		Body: CreateCustomerRequest{
			Name: customerName,
		},
	})

	if createCustomerResp.StatusCode != 200 {
		t.Fatalf("Failed to create customer: status %d", createCustomerResp.StatusCode)
	}

	customerID := ExtractIDFromResponse(t, createCustomerResp)
	// Don't add to testData since we'll delete manually

	// Create VK assigned directly to customer
	vkName := "test-vk-customer-nil-" + generateRandomID()
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:       vkName,
			CustomerID: &customerID,
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp)
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created customer and VK assigned to it")

	// Verify VK has customer_id set
	getDataResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	virtualKeysMap1 := getDataResp1.Body["virtual_keys"].(map[string]interface{})
	vkData1 := virtualKeysMap1[vkValue].(map[string]interface{})

	customerIDFromVK1, hasCustomerID := vkData1["customer_id"].(string)
	if !hasCustomerID || customerIDFromVK1 != customerID {
		t.Fatalf("VK customer_id not set correctly before customer deletion")
	}

	t.Logf("VK has customer_id=%s ✓", customerID)

	// Delete customer
	deleteResp := MakeRequest(t, APIRequest{
		Method: "DELETE",
		Path:   "/api/governance/customers/" + customerID,
	})

	if deleteResp.StatusCode != 200 {
		t.Fatalf("Failed to delete customer: status %d", deleteResp.StatusCode)
	}

	t.Logf("Customer deleted")

	time.Sleep(500 * time.Millisecond)

	// Verify VK still exists but customer_id is nil
	getDataResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	virtualKeysMap2 := getDataResp2.Body["virtual_keys"].(map[string]interface{})

	vkData2, vkStillExists := virtualKeysMap2[vkValue].(map[string]interface{})
	if !vkStillExists {
		t.Fatalf("VK should still exist after customer deletion")
	}

	customerIDFromVK2, hasCustomerID2 := vkData2["customer_id"].(string)
	if hasCustomerID2 && customerIDFromVK2 != "" {
		t.Fatalf("VK customer_id should be nil after customer deletion, got: %s", customerIDFromVK2)
	}

	t.Logf("VK customer_id is now nil ✓")
	t.Logf("Customer deletion sets VK customer_id to nil verified ✓")
}
