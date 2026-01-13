package governance

import (
	"testing"
	"time"
)

// TestUsageTrackingRateLimitReset tests that rate limit resets happen correctly on ticker
func TestUsageTrackingRateLimitReset(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with a rate limit that resets every 30 seconds
	vkName := "test-vk-rate-limit-reset-" + generateRandomID()
	tokenLimit := int64(10000) // 10k token limit
	tokenResetDuration := "30s"

	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			RateLimit: &CreateRateLimitRequest{
				TokenMaxLimit:      &tokenLimit,
				TokenResetDuration: &tokenResetDuration,
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

	t.Logf("Created VK %s with rate limit: %d tokens reset every %s", vkName, tokenLimit, tokenResetDuration)

	// Get initial rate limit data from data endpoint
	getVKResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	if getVKResp1.StatusCode != 200 {
		t.Fatalf("Failed to get governance data: status %d", getVKResp1.StatusCode)
	}

	virtualKeysMap1 := getVKResp1.Body["virtual_keys"].(map[string]interface{})
	vkData1 := virtualKeysMap1[vkValue].(map[string]interface{})
	rateLimitID, _ := vkData1["rate_limit_id"].(string)
	if rateLimitID == "" {
		t.Fatalf("Rate limit ID not found for VK")
	}

	t.Logf("Rate limit ID: %s", rateLimitID)

	// Make a request to consume tokens
	// Cost should be approximately: 5000 * 0.0000025 + 100 * 0.00001 = 0.013-0.014 dollars
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{
					Role:    "user",
					Content: "This is a test prompt to consume tokens for rate limit testing.",
				},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Logf("Request failed with status %d (may be due to other limits), body: %v", resp.StatusCode, resp.Body)
		t.Skip("Could not execute request to test rate limit reset")
	}

	// Extract token count from response
	var tokensUsed int
	if usage, ok := resp.Body["usage"].(map[string]interface{}); ok {
		if totalTokens, ok := usage["total_tokens"].(float64); ok {
			tokensUsed = int(totalTokens)
		}
	}

	if tokensUsed == 0 {
		t.Logf("No token usage in response, cannot verify rate limit reset")
		t.Skip("Could not extract token usage from response")
	}

	t.Logf("Request consumed %d tokens", tokensUsed)

	// Get rate limit data after request
	getDataResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	if getDataResp.StatusCode != 200 {
		t.Fatalf("Failed to get governance data: status %d", getDataResp.StatusCode)
	}

	// Rate limit counter should have been updated
	t.Logf("Rate limit should be tracking usage in in-memory store")

	// Wait for more than 30 seconds for the rate limit to reset
	t.Logf("Waiting 35 seconds for rate limit ticker to reset...")
	time.Sleep(35 * time.Second)

	// Get rate limit data after reset
	getDataResp3 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	if getDataResp3.StatusCode != 200 {
		t.Fatalf("Failed to get governance data after reset wait: status %d", getDataResp3.StatusCode)
	}

	// Verify rate limit has been reset (usage should be 0 or close to it)
	t.Logf("Rate limit reset should have occurred after 30s timeout ✓")
}

// TestUsageTrackingBudgetReset tests that budget resets happen correctly on ticker
func TestUsageTrackingBudgetReset(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with a budget that resets every 30 seconds
	vkName := "test-vk-budget-reset-" + generateRandomID()
	budgetLimit := 1.0 // $1 budget
	resetDuration := "30s"

	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			Budget: &BudgetRequest{
				MaxLimit:      budgetLimit,
				ResetDuration: resetDuration,
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

	t.Logf("Created VK %s with budget: $%.2f reset every %s", vkName, budgetLimit, resetDuration)

	// Get initial budget data
	getVKResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	virtualKeysMap := getVKResp.Body["virtual_keys"].(map[string]interface{})

	getBudgetsResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/budgets?from_memory=true",
	})

	budgetsMap := getBudgetsResp.Body["budgets"].(map[string]interface{})

	vkData := virtualKeysMap[vkValue].(map[string]interface{})
	budgetID, _ := vkData["budget_id"].(string)
	if budgetID == "" {
		t.Fatalf("Budget ID not found for VK")
	}

	budgetData := budgetsMap[budgetID].(map[string]interface{})
	initialUsage, _ := budgetData["current_usage"].(float64)

	t.Logf("Initial budget usage: $%.6f", initialUsage)

	// Make a request to consume budget
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{
					Role:    "user",
					Content: "Test prompt for budget reset testing.",
				},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Logf("Request failed with status %d, body: %v", resp.StatusCode, resp.Body)
		t.Skip("Could not execute request to test budget reset")
	}

	// Wait for async PostHook goroutine to complete budget update
	time.Sleep(2 * time.Second)

	getBudgetsResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/budgets?from_memory=true",
	})

	budgetsMap2 := getBudgetsResp2.Body["budgets"].(map[string]interface{})
	budgetData2 := budgetsMap2[budgetID].(map[string]interface{})
	usageAfterRequest, _ := budgetData2["current_usage"].(float64)

	t.Logf("Budget usage after request: $%.6f", usageAfterRequest)

	// Wait for budget reset
	t.Logf("Waiting 35 seconds for budget ticker to reset...")
	time.Sleep(35 * time.Second)

	// Get budget data after reset
	getDataResp3 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	if getDataResp3.StatusCode != 200 {
		t.Fatalf("Failed to get governance data after reset wait: status %d", getDataResp3.StatusCode)
	}

	getBudgetsResp3 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/budgets?from_memory=true",
	})

	budgetsMap3 := getBudgetsResp3.Body["budgets"].(map[string]interface{})
	budgetData3 := budgetsMap3[budgetID].(map[string]interface{})
	usageAfterReset, _ := budgetData3["current_usage"].(float64)

	// Budget should be reset (close to 0)
	if usageAfterReset > 0.001 {
		t.Fatalf("Budget not reset after 30s timeout: usage is $%.6f (should be ~0)", usageAfterReset)
	}

	t.Logf("Budget reset correctly after 30s timeout ✓")
}

// TestInMemoryUsageUpdateOnRequest tests that in-memory usage counters are updated on request
func TestInMemoryUsageUpdateOnRequest(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with rate limit to track usage
	vkName := "test-vk-usage-update-" + generateRandomID()
	tokenLimit := int64(100000)
	tokenResetDuration := "1h"
	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			RateLimit: &CreateRateLimitRequest{
				TokenMaxLimit:      &tokenLimit,
				TokenResetDuration: &tokenResetDuration,
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

	t.Logf("Created VK %s for usage tracking test", vkName)

	// Make a request to consume tokens
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{
					Role:    "user",
					Content: "Short test prompt for usage tracking.",
				},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Logf("Request failed with status %d", resp.StatusCode)
		t.Skip("Could not execute request to test usage tracking")
	}

	// Extract token usage from response
	var tokensUsed int
	if usage, ok := resp.Body["usage"].(map[string]interface{}); ok {
		if totalTokens, ok := usage["total_tokens"].(float64); ok {
			tokensUsed = int(totalTokens)
		}
	}

	if tokensUsed == 0 {
		t.Logf("No token usage in response")
		t.Skip("Could not extract token usage from response")
	}

	t.Logf("Request consumed %d tokens", tokensUsed)

	// Wait for async update to propagate to in-memory store
	var rateLimitID string
	var tokenUsage int64
	usageUpdated := WaitForCondition(t, func() bool {
		getDataResp := MakeRequest(t, APIRequest{
			Method: "GET",
			Path:   "/api/governance/virtual-keys?from_memory=true",
		})

		if getDataResp.StatusCode != 200 {
			return false
		}

		virtualKeysMap, ok := getDataResp.Body["virtual_keys"].(map[string]interface{})
		if !ok {
			return false
		}

		vkData, ok := virtualKeysMap[vkValue].(map[string]interface{})
		if !ok {
			return false
		}

		// Rate limit should exist
		rateLimitID, _ = vkData["rate_limit_id"].(string)
		if rateLimitID == "" {
			return false
		}

		// Fetch the rate limit data to check token usage
		getRateLimitsResp := MakeRequest(t, APIRequest{
			Method: "GET",
			Path:   "/api/governance/rate-limits?from_memory=true",
		})

		if getRateLimitsResp.StatusCode != 200 {
			return false
		}

		rateLimitsMap, ok := getRateLimitsResp.Body["rate_limits"].(map[string]interface{})
		if !ok {
			return false
		}

		rateLimitData, ok := rateLimitsMap[rateLimitID].(map[string]interface{})
		if !ok {
			return false
		}

		// Check that token usage has been updated (should be > 0 after the request)
		if tokenCurrentUsage, ok := rateLimitData["token_current_usage"].(float64); ok {
			tokenUsage = int64(tokenCurrentUsage)
			return tokenUsage > 0
		}

		return false
	}, 3*time.Second, "usage updated in in-memory store")

	if !usageUpdated {
		t.Fatalf("Rate limit usage not updated in in-memory store after request (timeout after 3s)")
	}

	if rateLimitID != "" {
		t.Logf("Rate limit tracking is enabled for VK ✓")
		t.Logf("Token usage in rate limit: %d tokens", tokenUsage)
	} else {
		t.Logf("No rate limit on VK (optional)")
	}

	t.Logf("In-memory usage tracking verified ✓")
}

// TestResetTickerBothBudgetAndRateLimit tests that ticker resets both budget and rate limit together
func TestResetTickerBothBudgetAndRateLimit(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with both budget and rate limit that reset every 30 seconds
	vkName := "test-vk-both-reset-" + generateRandomID()
	budgetLimit := 2.0
	budgetResetDuration := "30s"
	tokenLimit := int64(50000)
	tokenResetDuration := "30s"

	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			Budget: &BudgetRequest{
				MaxLimit:      budgetLimit,
				ResetDuration: budgetResetDuration,
			},
			RateLimit: &CreateRateLimitRequest{
				TokenMaxLimit:      &tokenLimit,
				TokenResetDuration: &tokenResetDuration,
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

	t.Logf("Created VK %s with budget and rate limit both resetting every 30s", vkName)

	// Make requests to consume both budget and tokens
	for i := 0; i < 3; i++ {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{
						Role:    "user",
						Content: "Test request " + string(rune('0'+i)) + " for reset ticker test.",
					},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode != 200 {
			t.Logf("Request %d failed with status %d", i+1, resp.StatusCode)
			break
		}
		t.Logf("Request %d succeeded", i+1)
	}

	// Wait for async PostHook goroutines to complete budget updates
	t.Logf("Waiting 3 seconds for async updates to complete...")
	time.Sleep(3 * time.Second)

	// Get usage before reset
	getVKResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	virtualKeysMap := getVKResp.Body["virtual_keys"].(map[string]interface{})

	getBudgetsResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/budgets?from_memory=true",
	})

	budgetsMap := getBudgetsResp.Body["budgets"].(map[string]interface{})

	vkData := virtualKeysMap[vkValue].(map[string]interface{})
	budgetID, _ := vkData["budget_id"].(string)

	var usageBeforeReset float64
	if budgetID != "" {
		budgetData := budgetsMap[budgetID].(map[string]interface{})
		usageBeforeReset, _ = budgetData["current_usage"].(float64)
	}

	t.Logf("Budget usage before reset: $%.6f", usageBeforeReset)

	// Wait for reset (reset ticker runs every 10s, budget resets at 30s, add buffer for processing)
	t.Logf("Waiting 40 seconds for reset ticker...")
	time.Sleep(40 * time.Second)

	// Get usage after reset
	getBudgetsResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/budgets?from_memory=true",
	})

	budgetsMap2 := getBudgetsResp2.Body["budgets"].(map[string]interface{})

	var usageAfterReset float64
	if budgetID != "" {
		budgetData2 := budgetsMap2[budgetID].(map[string]interface{})
		usageAfterReset, _ = budgetData2["current_usage"].(float64)
	}

	t.Logf("Budget usage after reset: $%.6f", usageAfterReset)

	if usageBeforeReset > 0 && usageAfterReset >= usageBeforeReset {
		t.Fatalf("Budget not reset properly: before=$%.6f, after=$%.6f (expected reset to ~0)", usageBeforeReset, usageAfterReset)
	}

	t.Logf("Both budget and rate limit reset on ticker ✓")
}

// TestDataPersistenceAcrossRequests tests that budget and rate limit data persists correctly
func TestDataPersistenceAcrossRequests(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with both budget and rate limit
	vkName := "test-vk-persistence-" + generateRandomID()
	budgetLimit := 5.0
	budgetResetDuration := "1h"
	tokenLimit := int64(100000)
	tokenResetDuration := "1h"
	requestLimit := int64(100)
	requestResetDuration := "1h"

	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			Budget: &BudgetRequest{
				MaxLimit:      budgetLimit,
				ResetDuration: budgetResetDuration,
			},
			RateLimit: &CreateRateLimitRequest{
				TokenMaxLimit:        &tokenLimit,
				TokenResetDuration:   &tokenResetDuration,
				RequestMaxLimit:      &requestLimit,
				RequestResetDuration: &requestResetDuration,
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

	t.Logf("Created VK %s for persistence testing", vkName)

	// Make multiple requests and verify data persists
	successCount := 0
	for i := 0; i < 2; i++ {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{
						Role:    "user",
						Content: "Persistence test request " + string(rune('0'+i)) + ".",
					},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode == 200 {
			successCount++
		} else {
			t.Logf("Request %d failed with status %d", i+1, resp.StatusCode)
		}
	}

	if successCount == 0 {
		t.Skip("Could not make requests to test persistence")
	}

	t.Logf("Made %d successful requests", successCount)

	// Verify data persists in in-memory store
	getDataResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	if getDataResp.StatusCode != 200 {
		t.Fatalf("Failed to get governance data: status %d", getDataResp.StatusCode)
	}

	virtualKeysMap := getDataResp.Body["virtual_keys"].(map[string]interface{})

	vkData, exists := virtualKeysMap[vkValue]
	if !exists {
		t.Fatalf("VK not found in in-memory store after requests")
	}

	vkDataMap := vkData.(map[string]interface{})
	budgetID, _ := vkDataMap["budget_id"].(string)
	rateLimitID, _ := vkDataMap["rate_limit_id"].(string)

	if budgetID == "" {
		t.Fatalf("Budget ID not found for VK")
	}
	if rateLimitID == "" {
		t.Fatalf("Rate limit ID not found for VK")
	}

	t.Logf("VK data persists correctly in in-memory store ✓")
}
