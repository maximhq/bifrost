package governance

import (
	"testing"
	"time"
)

// ============================================================================
// VK-LEVEL RATE LIMIT UPDATE SYNC
// ============================================================================

// TestVKRateLimitUpdateSyncToMemory tests that VK rate limit updates sync to in-memory store
// and that usage resets to 0 when new max limit < current usage
func TestVKRateLimitUpdateSyncToMemory(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create VK with initial rate limit
	vkName := "test-vk-rate-update-" + generateRandomID()
	initialTokenLimit := int64(10000)
	tokenResetDuration := "1h"

	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			RateLimit: &CreateRateLimitRequest{
				TokenMaxLimit:      &initialTokenLimit,
				TokenResetDuration: &tokenResetDuration,
			},
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp, "id")
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created VK with initial token limit: %d", initialTokenLimit)

	// Get initial in-memory state
	getDataResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data1 := getDataResp1.Body["data"].(map[string]interface{})
	vkData1 := data1["virtual_keys"].(map[string]interface{})[vkValue].(map[string]interface{})
	rateLimitID1, _ := vkData1["rate_limit_id"].(string)

	rateLimitsMap1 := data1["rate_limits"].(map[string]interface{})
	rateLimit1 := rateLimitsMap1[rateLimitID1].(map[string]interface{})

	initialTokenMaxLimit, _ := rateLimit1["token_max_limit"].(float64)
	initialTokenUsage, _ := rateLimit1["token_current_usage"].(float64)

	if int64(initialTokenMaxLimit) != initialTokenLimit {
		t.Fatalf("Initial token max limit not correct: expected %d, got %d", initialTokenLimit, int64(initialTokenMaxLimit))
	}

	t.Logf("Initial state in memory: TokenMaxLimit=%d, TokenCurrentUsage=%d", int64(initialTokenMaxLimit), int64(initialTokenUsage))

	// Make a request to consume some tokens
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{
					Role:    "user",
					Content: "Test request to consume tokens.",
				},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Skip("Could not make request to consume tokens")
	}

	// Wait for async update
	time.Sleep(500 * time.Millisecond)

	// Get state with usage
	getDataResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data2 := getDataResp2.Body["data"].(map[string]interface{})
	vkData2 := data2["virtual_keys"].(map[string]interface{})[vkValue].(map[string]interface{})
	rateLimitID2, _ := vkData2["rate_limit_id"].(string)

	rateLimitsMap2 := data2["rate_limits"].(map[string]interface{})
	rateLimit2 := rateLimitsMap2[rateLimitID2].(map[string]interface{})

	tokenUsageBeforeUpdate, _ := rateLimit2["token_current_usage"].(float64)
	t.Logf("Token usage after request: %d", int64(tokenUsageBeforeUpdate))

	if tokenUsageBeforeUpdate <= 0 {
		t.Skip("No tokens consumed - cannot test usage reset")
	}

	// NOW UPDATE: set new limit LOWER than current usage
	// This should trigger usage reset to 0
	newLowerLimit := int64(100) // Much lower than current usage
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/virtual-keys/" + vkID,
		Body: UpdateVirtualKeyRequest{
			RateLimit: &CreateRateLimitRequest{
				TokenMaxLimit:      &newLowerLimit,
				TokenResetDuration: &tokenResetDuration,
			},
		},
	})

	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to update VK rate limit: status %d", updateResp.StatusCode)
	}

	t.Logf("Updated token limit from %d to %d (new < current usage)", initialTokenLimit, newLowerLimit)

	// Wait for update to sync
	time.Sleep(500 * time.Millisecond)

	// Verify update in in-memory store
	getDataResp3 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data3 := getDataResp3.Body["data"].(map[string]interface{})
	vkData3 := data3["virtual_keys"].(map[string]interface{})[vkValue].(map[string]interface{})
	rateLimitID3, _ := vkData3["rate_limit_id"].(string)

	rateLimitsMap3 := data3["rate_limits"].(map[string]interface{})
	rateLimit3 := rateLimitsMap3[rateLimitID3].(map[string]interface{})

	newTokenMaxLimit, _ := rateLimit3["token_max_limit"].(float64)
	tokenUsageAfterUpdate, _ := rateLimit3["token_current_usage"].(float64)

	// Verify new max limit is reflected
	if int64(newTokenMaxLimit) != newLowerLimit {
		t.Fatalf("Token max limit not updated in memory: expected %d, got %d", newLowerLimit, int64(newTokenMaxLimit))
	}

	t.Logf("✓ Token max limit updated in memory: %d", int64(newTokenMaxLimit))

	// Verify usage reset to 0 (since new max < old usage)
	if tokenUsageAfterUpdate > 0.001 {
		t.Fatalf("Token usage should reset to 0 when new limit < current usage, but got %d", int64(tokenUsageAfterUpdate))
	}

	t.Logf("✓ Token usage correctly reset to 0 (new limit: %d < old usage: %d)", int64(newTokenMaxLimit), int64(tokenUsageBeforeUpdate))

	// Test UPDATE with higher limit (usage should NOT reset)
	newerHigherLimit := int64(50000)
	updateResp2 := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/virtual-keys/" + vkID,
		Body: UpdateVirtualKeyRequest{
			RateLimit: &CreateRateLimitRequest{
				TokenMaxLimit:      &newerHigherLimit,
				TokenResetDuration: &tokenResetDuration,
			},
		},
	})

	if updateResp2.StatusCode != 200 {
		t.Fatalf("Failed to update VK rate limit second time: status %d", updateResp2.StatusCode)
	}

	time.Sleep(500 * time.Millisecond)

	getDataResp4 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data4 := getDataResp4.Body["data"].(map[string]interface{})
	vkData4 := data4["virtual_keys"].(map[string]interface{})[vkValue].(map[string]interface{})
	rateLimitID4, _ := vkData4["rate_limit_id"].(string)

	rateLimitsMap4 := data4["rate_limits"].(map[string]interface{})
	rateLimit4 := rateLimitsMap4[rateLimitID4].(map[string]interface{})

	newerTokenMaxLimit, _ := rateLimit4["token_max_limit"].(float64)
	tokenUsageAfterSecondUpdate, _ := rateLimit4["token_current_usage"].(float64)

	// Verify new higher limit is reflected
	if int64(newerTokenMaxLimit) != newerHigherLimit {
		t.Fatalf("Token max limit not updated to higher value: expected %d, got %d", newerHigherLimit, int64(newerTokenMaxLimit))
	}

	t.Logf("✓ Token max limit updated to higher value: %d", int64(newerTokenMaxLimit))

	// Since usage is 0 and new limit is higher, usage stays 0
	if tokenUsageAfterSecondUpdate != 0 {
		t.Logf("Note: Token usage is %d (expected 0 since it was reset)", int64(tokenUsageAfterSecondUpdate))
	}

	t.Logf("VK rate limit update sync to memory verified ✓")
}

// TestVKBudgetUpdateSyncToMemory tests that VK budget updates sync to in-memory store
// and that usage resets to 0 when new max budget < current usage
func TestVKBudgetUpdateSyncToMemory(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create VK with initial budget
	vkName := "test-vk-budget-update-" + generateRandomID()
	initialBudget := 10.0 // $10
	resetDuration := "1h"

	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			Budget: &BudgetRequest{
				MaxLimit:      initialBudget,
				ResetDuration: resetDuration,
			},
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp, "id")
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created VK with initial budget: $%.2f", initialBudget)

	// Get initial in-memory state
	getDataResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data1 := getDataResp1.Body["data"].(map[string]interface{})
	vkData1 := data1["virtual_keys"].(map[string]interface{})[vkValue].(map[string]interface{})
	budgetID, _ := vkData1["budget_id"].(string)
	budgetsMap1 := data1["budgets"].(map[string]interface{})
	budget1 := budgetsMap1[budgetID].(map[string]interface{})

	initialMaxLimit, _ := budget1["max_limit"].(float64)
	initialUsage, _ := budget1["current_usage"].(float64)

	if initialMaxLimit != initialBudget {
		t.Fatalf("Initial budget max limit not correct: expected %.2f, got %.2f", initialBudget, initialMaxLimit)
	}

	t.Logf("Initial state in memory: MaxLimit=$%.2f, CurrentUsage=$%.6f", initialMaxLimit, initialUsage)

	// Make a request to consume some budget
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{
					Role:    "user",
					Content: "Test request to consume budget.",
				},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Skip("Could not make request to consume budget")
	}

	// Wait for async update
	time.Sleep(500 * time.Millisecond)

	// Get state with usage
	getDataResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data2 := getDataResp2.Body["data"].(map[string]interface{})
	budgetsMap2 := data2["budgets"].(map[string]interface{})
	budget2 := budgetsMap2[budgetID].(map[string]interface{})

	usageBeforeUpdate, _ := budget2["current_usage"].(float64)
	t.Logf("Budget usage after request: $%.6f", usageBeforeUpdate)

	if usageBeforeUpdate <= 0 {
		t.Skip("No budget consumed - cannot test usage reset")
	}

	// UPDATE: set new limit LOWER than current usage
	newLowerBudget := 0.01 // Much lower than current usage
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/virtual-keys/" + vkID,
		Body: UpdateVirtualKeyRequest{
			Budget: &UpdateBudgetRequest{
				MaxLimit:      &newLowerBudget,
				ResetDuration: &resetDuration,
			},
		},
	})

	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to update VK budget: status %d", updateResp.StatusCode)
	}

	t.Logf("Updated budget from $%.2f to $%.2f (new < current usage)", initialBudget, newLowerBudget)

	// Wait for update to sync
	time.Sleep(500 * time.Millisecond)

	// Verify update in in-memory store
	getDataResp3 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data3 := getDataResp3.Body["data"].(map[string]interface{})
	budgetsMap3 := data3["budgets"].(map[string]interface{})
	budget3 := budgetsMap3[budgetID].(map[string]interface{})

	newMaxLimit, _ := budget3["max_limit"].(float64)
	usageAfterUpdate, _ := budget3["current_usage"].(float64)

	// Verify new max limit is reflected
	if newMaxLimit != newLowerBudget {
		t.Fatalf("Budget max limit not updated in memory: expected %.2f, got %.2f", newLowerBudget, newMaxLimit)
	}

	t.Logf("✓ Budget max limit updated in memory: $%.2f", newMaxLimit)

	// Verify usage reset to 0 (since new max < old usage)
	if usageAfterUpdate > 0.000001 {
		t.Fatalf("Budget usage should reset to 0 when new limit < current usage, but got $%.6f", usageAfterUpdate)
	}

	t.Logf("✓ Budget usage correctly reset to 0 (new limit: $%.2f < old usage: $%.6f)", newMaxLimit, usageBeforeUpdate)

	t.Logf("VK budget update sync to memory verified ✓")
}

// ============================================================================
// PROVIDER CONFIG RATE LIMIT UPDATE SYNC
// ============================================================================

// TestProviderRateLimitUpdateSyncToMemory tests that provider config rate limit updates sync to memory
func TestProviderRateLimitUpdateSyncToMemory(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create VK with provider config and initial rate limit
	vkName := "test-vk-provider-rate-update-" + generateRandomID()
	initialTokenLimit := int64(5000)
	tokenResetDuration := "1h"

	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			ProviderConfigs: []ProviderConfigRequest{
				{
					Provider: "openai",
					Weight:   1.0,
					RateLimit: &CreateRateLimitRequest{
						TokenMaxLimit:      &initialTokenLimit,
						TokenResetDuration: &tokenResetDuration,
					},
				},
			},
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp, "id")
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created VK with provider config, initial token limit: %d", initialTokenLimit)

	// Get initial in-memory state
	getDataResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data1 := getDataResp1.Body["data"].(map[string]interface{})
	vkData1 := data1["virtual_keys"].(map[string]interface{})[vkValue].(map[string]interface{})
	providerConfigs1 := vkData1["provider_configs"].([]interface{})
	providerConfig1 := providerConfigs1[0].(map[string]interface{})
	providerConfigID := uint(providerConfig1["id"].(float64))
	rateLimitID1, _ := providerConfig1["rate_limit_id"].(string)

	rateLimitsMap1 := data1["rate_limits"].(map[string]interface{})
	rateLimit1 := rateLimitsMap1[rateLimitID1].(map[string]interface{})

	initialTokenMaxLimit, _ := rateLimit1["token_max_limit"].(float64)
	initialTokenUsage, _ := rateLimit1["token_current_usage"].(float64)

	if int64(initialTokenMaxLimit) != initialTokenLimit {
		t.Fatalf("Initial token max limit not correct: expected %d, got %d", initialTokenLimit, int64(initialTokenMaxLimit))
	}

	t.Logf("Initial provider rate limit in memory: TokenMaxLimit=%d, TokenCurrentUsage=%d", int64(initialTokenMaxLimit), int64(initialTokenUsage))

	// Make a request to consume some tokens
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{
					Role:    "user",
					Content: "Test request to consume provider tokens.",
				},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Skip("Could not make request to consume provider tokens")
	}

	time.Sleep(500 * time.Millisecond)

	// Get state with usage
	getDataResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data2 := getDataResp2.Body["data"].(map[string]interface{})
	vkData2 := data2["virtual_keys"].(map[string]interface{})[vkValue].(map[string]interface{})
	providerConfigs2 := vkData2["provider_configs"].([]interface{})
	providerConfig2 := providerConfigs2[0].(map[string]interface{})
	rateLimitID2, _ := providerConfig2["rate_limit_id"].(string)

	rateLimitsMap2 := data2["rate_limits"].(map[string]interface{})
	rateLimit2 := rateLimitsMap2[rateLimitID2].(map[string]interface{})

	tokenUsageBeforeUpdate, _ := rateLimit2["token_current_usage"].(float64)
	t.Logf("Provider token usage after request: %d", int64(tokenUsageBeforeUpdate))

	if tokenUsageBeforeUpdate <= 0 {
		t.Skip("No provider tokens consumed - cannot test usage reset")
	}

	// UPDATE: set new limit LOWER than current usage
	newLowerLimit := int64(50) // Much lower
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/virtual-keys/" + vkID,
		Body: UpdateVirtualKeyRequest{
			ProviderConfigs: []ProviderConfigRequest{
				{
					ID:       &providerConfigID,
					Provider: "openai",
					Weight:   1.0,
					RateLimit: &CreateRateLimitRequest{
						TokenMaxLimit:      &newLowerLimit,
						TokenResetDuration: &tokenResetDuration,
					},
				},
			},
		},
	})

	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to update provider rate limit: status %d", updateResp.StatusCode)
	}

	t.Logf("Updated provider token limit from %d to %d", initialTokenLimit, newLowerLimit)

	time.Sleep(500 * time.Millisecond)

	// Verify update in in-memory store
	getDataResp3 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data3 := getDataResp3.Body["data"].(map[string]interface{})
	vkData3 := data3["virtual_keys"].(map[string]interface{})[vkValue].(map[string]interface{})
	providerConfigs3 := vkData3["provider_configs"].([]interface{})
	providerConfig3 := providerConfigs3[0].(map[string]interface{})
	rateLimitID3, _ := providerConfig3["rate_limit_id"].(string)

	rateLimitsMap3 := data3["rate_limits"].(map[string]interface{})
	rateLimit3 := rateLimitsMap3[rateLimitID3].(map[string]interface{})

	newTokenMaxLimit, _ := rateLimit3["token_max_limit"].(float64)
	tokenUsageAfterUpdate, _ := rateLimit3["token_current_usage"].(float64)

	// Verify new limit is reflected
	if int64(newTokenMaxLimit) != newLowerLimit {
		t.Fatalf("Provider token max limit not updated: expected %d, got %d", newLowerLimit, int64(newTokenMaxLimit))
	}

	t.Logf("✓ Provider token max limit updated in memory: %d", int64(newTokenMaxLimit))

	// Verify usage reset to 0 (since new max < old usage)
	if tokenUsageAfterUpdate > 0.001 {
		t.Fatalf("Provider token usage should reset to 0 when new limit < current usage, but got %d", int64(tokenUsageAfterUpdate))
	}

	t.Logf("✓ Provider token usage reset to 0 (new limit: %d < old usage: %d)", int64(newTokenMaxLimit), int64(tokenUsageBeforeUpdate))

	t.Logf("Provider rate limit update sync to memory verified ✓")
}

// ============================================================================
// TEAM BUDGET UPDATE SYNC
// ============================================================================

// TestTeamBudgetUpdateSyncToMemory tests that team budget updates sync to in-memory store
func TestTeamBudgetUpdateSyncToMemory(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create team with initial budget
	teamName := "test-team-budget-update-" + generateRandomID()
	initialBudget := 5.0
	resetDuration := "1h"

	createTeamResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/teams",
		Body: CreateTeamRequest{
			Name: teamName,
			Budget: &BudgetRequest{
				MaxLimit:      initialBudget,
				ResetDuration: resetDuration,
			},
		},
	})

	if createTeamResp.StatusCode != 200 {
		t.Fatalf("Failed to create team: status %d", createTeamResp.StatusCode)
	}

	teamID := ExtractIDFromResponse(t, createTeamResp, "id")
	testData.AddTeam(teamID)

	// Create VK under team to consume budget
	vkName := "test-vk-under-team-" + generateRandomID()
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

	vkID := ExtractIDFromResponse(t, createVKResp, "id")
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created team with initial budget: $%.2f", initialBudget)

	// Get initial in-memory state
	getDataResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data1 := getDataResp1.Body["data"].(map[string]interface{})
	teamsMap1 := data1["teams"].(map[string]interface{})
	teamData1 := teamsMap1[teamID].(map[string]interface{})
	budgetID, _ := teamData1["budget_id"].(string)
	budgetsMap1 := data1["budgets"].(map[string]interface{})
	budget1 := budgetsMap1[budgetID].(map[string]interface{})

	initialMaxLimit, _ := budget1["max_limit"].(float64)
	initialUsage, _ := budget1["current_usage"].(float64)

	if initialMaxLimit != initialBudget {
		t.Fatalf("Initial budget not correct: expected %.2f, got %.2f", initialBudget, initialMaxLimit)
	}

	t.Logf("Initial team budget in memory: MaxLimit=$%.2f, CurrentUsage=$%.6f", initialMaxLimit, initialUsage)

	// Make request to consume team budget
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{
					Role:    "user",
					Content: "Test request to consume team budget.",
				},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Skip("Could not make request to consume team budget")
	}

	time.Sleep(500 * time.Millisecond)

	// Get state with usage
	getDataResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data2 := getDataResp2.Body["data"].(map[string]interface{})
	budgetsMap2 := data2["budgets"].(map[string]interface{})
	budget2 := budgetsMap2[budgetID].(map[string]interface{})

	usageBeforeUpdate, _ := budget2["current_usage"].(float64)
	t.Logf("Team budget usage after request: $%.6f", usageBeforeUpdate)

	if usageBeforeUpdate <= 0 {
		t.Skip("No team budget consumed")
	}

	// UPDATE: set new limit LOWER than current usage
	newLowerBudget := 0.001
	resetDurationPtr := resetDuration
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/teams/" + teamID,
		Body: UpdateTeamRequest{
			Budget: &UpdateBudgetRequest{
				MaxLimit:      &newLowerBudget,
				ResetDuration: &resetDurationPtr,
			},
		},
	})

	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to update team budget: status %d", updateResp.StatusCode)
	}

	t.Logf("Updated team budget from $%.2f to $%.2f", initialBudget, newLowerBudget)

	time.Sleep(500 * time.Millisecond)

	// Verify update in in-memory store
	getDataResp3 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data3 := getDataResp3.Body["data"].(map[string]interface{})
	budgetsMap3 := data3["budgets"].(map[string]interface{})
	budget3 := budgetsMap3[budgetID].(map[string]interface{})

	newMaxLimit, _ := budget3["max_limit"].(float64)
	usageAfterUpdate, _ := budget3["current_usage"].(float64)

	// Verify new limit is reflected
	if newMaxLimit != newLowerBudget {
		t.Fatalf("Team budget max limit not updated: expected %.2f, got %.2f", newLowerBudget, newMaxLimit)
	}

	t.Logf("✓ Team budget max limit updated in memory: $%.2f", newMaxLimit)

	// Verify usage reset to 0 (since new max < old usage)
	if usageAfterUpdate > 0.000001 {
		t.Fatalf("Team budget usage should reset to 0 when new limit < current usage, but got $%.6f", usageAfterUpdate)
	}

	t.Logf("✓ Team budget usage correctly reset to 0 (new limit: $%.2f < old usage: $%.6f)", newMaxLimit, usageBeforeUpdate)

	t.Logf("Team budget update sync to memory verified ✓")
}

// ============================================================================
// CUSTOMER BUDGET UPDATE SYNC
// ============================================================================

// TestCustomerBudgetUpdateSyncToMemory tests that customer budget updates sync to in-memory store
func TestCustomerBudgetUpdateSyncToMemory(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create customer with initial budget
	customerName := "test-customer-budget-update-" + generateRandomID()
	initialBudget := 20.0
	resetDuration := "1h"

	createCustomerResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/customers",
		Body: CreateCustomerRequest{
			Name: customerName,
			Budget: &BudgetRequest{
				MaxLimit:      initialBudget,
				ResetDuration: resetDuration,
			},
		},
	})

	if createCustomerResp.StatusCode != 200 {
		t.Fatalf("Failed to create customer: status %d", createCustomerResp.StatusCode)
	}

	customerID := ExtractIDFromResponse(t, createCustomerResp, "id")
	testData.AddCustomer(customerID)

	// Create team and VK under customer
	teamName := "test-team-under-customer-" + generateRandomID()
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

	teamID := ExtractIDFromResponse(t, createTeamResp, "id")
	testData.AddTeam(teamID)

	vkName := "test-vk-under-customer-" + generateRandomID()
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

	vkID := ExtractIDFromResponse(t, createVKResp, "id")
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created customer with initial budget: $%.2f", initialBudget)

	// Get initial in-memory state
	getDataResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data1 := getDataResp1.Body["data"].(map[string]interface{})
	customersMap1 := data1["customers"].(map[string]interface{})
	customerData1 := customersMap1[customerID].(map[string]interface{})
	budgetID, _ := customerData1["budget_id"].(string)
	budgetsMap1 := data1["budgets"].(map[string]interface{})
	budget1 := budgetsMap1[budgetID].(map[string]interface{})

	initialMaxLimit, _ := budget1["max_limit"].(float64)
	initialUsage, _ := budget1["current_usage"].(float64)

	if initialMaxLimit != initialBudget {
		t.Fatalf("Initial customer budget not correct: expected %.2f, got %.2f", initialBudget, initialMaxLimit)
	}

	t.Logf("Initial customer budget in memory: MaxLimit=$%.2f, CurrentUsage=$%.6f", initialMaxLimit, initialUsage)

	// Make request to consume customer budget
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{
					Role:    "user",
					Content: "Test request to consume customer budget.",
				},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Skip("Could not make request to consume customer budget")
	}

	time.Sleep(500 * time.Millisecond)

	// Get state with usage
	getDataResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data2 := getDataResp2.Body["data"].(map[string]interface{})
	budgetsMap2 := data2["budgets"].(map[string]interface{})
	budget2 := budgetsMap2[budgetID].(map[string]interface{})

	usageBeforeUpdate, _ := budget2["current_usage"].(float64)
	t.Logf("Customer budget usage after request: $%.6f", usageBeforeUpdate)

	if usageBeforeUpdate <= 0 {
		t.Skip("No customer budget consumed")
	}

	// UPDATE: set new limit LOWER than current usage
	newLowerBudget := 0.001
	resetDurationPtr := resetDuration
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/customers/" + customerID,
		Body: UpdateCustomerRequest{
			Budget: &UpdateBudgetRequest{
				MaxLimit:      &newLowerBudget,
				ResetDuration: &resetDurationPtr,
			},
		},
	})

	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to update customer budget: status %d", updateResp.StatusCode)
	}

	t.Logf("Updated customer budget from $%.2f to $%.2f", initialBudget, newLowerBudget)

	time.Sleep(500 * time.Millisecond)

	// Verify update in in-memory store
	getDataResp3 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data3 := getDataResp3.Body["data"].(map[string]interface{})
	budgetsMap3 := data3["budgets"].(map[string]interface{})
	budget3 := budgetsMap3[budgetID].(map[string]interface{})

	newMaxLimit, _ := budget3["max_limit"].(float64)
	usageAfterUpdate, _ := budget3["current_usage"].(float64)

	// Verify new limit is reflected
	if newMaxLimit != newLowerBudget {
		t.Fatalf("Customer budget max limit not updated: expected %.2f, got %.2f", newLowerBudget, newMaxLimit)
	}

	t.Logf("✓ Customer budget max limit updated in memory: $%.2f", newMaxLimit)

	// Verify usage reset to 0 (since new max < old usage)
	if usageAfterUpdate > 0.000001 {
		t.Fatalf("Customer budget usage should reset to 0 when new limit < current usage, but got $%.6f", usageAfterUpdate)
	}

	t.Logf("✓ Customer budget usage correctly reset to 0 (new limit: $%.2f < old usage: $%.6f)", newMaxLimit, usageBeforeUpdate)

	t.Logf("Customer budget update sync to memory verified ✓")
}

// ============================================================================
// PROVIDER CONFIG BUDGET UPDATE SYNC
// ============================================================================

// TestProviderBudgetUpdateSyncToMemory tests that provider config budget updates sync to memory
func TestProviderBudgetUpdateSyncToMemory(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create VK with provider config and initial budget
	vkName := "test-vk-provider-budget-update-" + generateRandomID()
	initialBudget := 5.0
	resetDuration := "1h"

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
						ResetDuration: resetDuration,
					},
				},
			},
		},
	})

	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d", createVKResp.StatusCode)
	}

	vkID := ExtractIDFromResponse(t, createVKResp, "id")
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created VK with provider budget: $%.2f", initialBudget)

	// Get initial in-memory state
	getDataResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data1 := getDataResp1.Body["data"].(map[string]interface{})
	vkData1 := data1["virtual_keys"].(map[string]interface{})[vkValue].(map[string]interface{})
	providerConfigs1 := vkData1["provider_configs"].([]interface{})
	providerConfig1 := providerConfigs1[0].(map[string]interface{})
	providerConfigID := uint(providerConfig1["id"].(float64))
	budgetID, _ := providerConfig1["budget_id"].(string)
	budgetsMap1 := data1["budgets"].(map[string]interface{})
	budget1 := budgetsMap1[budgetID].(map[string]interface{})

	initialMaxLimit, _ := budget1["max_limit"].(float64)
	initialUsage, _ := budget1["current_usage"].(float64)

	if initialMaxLimit != initialBudget {
		t.Fatalf("Initial provider budget not correct: expected %.2f, got %.2f", initialBudget, initialMaxLimit)
	}

	t.Logf("Initial provider budget in memory: MaxLimit=$%.2f, CurrentUsage=$%.6f", initialMaxLimit, initialUsage)

	// Make request to consume provider budget
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{
					Role:    "user",
					Content: "Test request to consume provider budget.",
				},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Skip("Could not make request to consume provider budget")
	}

	time.Sleep(500 * time.Millisecond)

	// Get state with usage
	getDataResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data2 := getDataResp2.Body["data"].(map[string]interface{})
	budgetsMap2 := data2["budgets"].(map[string]interface{})
	budget2 := budgetsMap2[budgetID].(map[string]interface{})

	usageBeforeUpdate, _ := budget2["current_usage"].(float64)
	t.Logf("Provider budget usage after request: $%.6f", usageBeforeUpdate)

	if usageBeforeUpdate <= 0 {
		t.Skip("No provider budget consumed")
	}

	// UPDATE: set new limit LOWER than current usage
	newLowerBudget := 0.001
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
						MaxLimit:      newLowerBudget,
						ResetDuration: resetDuration,
					},
				},
			},
		},
	})

	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to update provider budget: status %d", updateResp.StatusCode)
	}

	t.Logf("Updated provider budget from $%.2f to $%.2f", initialBudget, newLowerBudget)

	time.Sleep(500 * time.Millisecond)

	// Verify update in in-memory store
	getDataResp3 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/data",
	})

	data3 := getDataResp3.Body["data"].(map[string]interface{})
	budgetsMap3 := data3["budgets"].(map[string]interface{})
	budget3 := budgetsMap3[budgetID].(map[string]interface{})

	newMaxLimit, _ := budget3["max_limit"].(float64)
	usageAfterUpdate, _ := budget3["current_usage"].(float64)

	// Verify new limit is reflected
	if newMaxLimit != newLowerBudget {
		t.Fatalf("Provider budget max limit not updated: expected %.2f, got %.2f", newLowerBudget, newMaxLimit)
	}

	t.Logf("✓ Provider budget max limit updated in memory: $%.2f", newMaxLimit)

	// Verify usage reset to 0 (since new max < old usage)
	if usageAfterUpdate > 0.000001 {
		t.Fatalf("Provider budget usage should reset to 0 when new limit < current usage, but got $%.6f", usageAfterUpdate)
	}

	t.Logf("✓ Provider budget usage correctly reset to 0 (new limit: $%.2f < old usage: $%.6f)", newMaxLimit, usageBeforeUpdate)

	t.Logf("Provider budget update sync to memory verified ✓")
}
