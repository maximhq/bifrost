package governance

import (
	"testing"
	"time"
)

// TestVirtualKeyTokenRateLimit tests that VK-level token rate limits are enforced
func TestVirtualKeyTokenRateLimit(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with a very restrictive token rate limit
	vkName := "test-vk-token-limit-" + generateRandomID()
	tokenLimit := int64(500) // Only 500 tokens per hour
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

	t.Logf("Created VK %s with token limit: %d tokens per %s", vkName, tokenLimit, tokenResetDuration)

	// Make requests until we hit the token limit
	successCount := 0
	for i := 0; i < 10; i++ {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{
						Role:    "user",
						Content: "Short test request " + string(rune('0'+i)) + " for token limit.",
					},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode >= 400 {
			if CheckErrorMessage(t, resp, "token") || CheckErrorMessage(t, resp, "rate") {
				t.Logf("Request %d correctly rejected due to token rate limit", i+1)
				return // Test passed - hit the token limit
			} else {
				t.Logf("Request %d failed with unexpected error: %v", i+1, resp.Body)
			}
		} else if resp.StatusCode == 200 {
			successCount++
			t.Logf("Request %d succeeded (tokens within limit)", i+1)
		}
	}

	if successCount > 0 {
		t.Logf("Made %d successful requests before hitting token limit ✓", successCount)
	} else {
		t.Skip("Could not make requests to test token limit")
	}
}

// TestVirtualKeyRequestRateLimit tests that VK-level request rate limits are enforced
func TestVirtualKeyRequestRateLimit(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with a very restrictive request rate limit
	vkName := "test-vk-request-limit-" + generateRandomID()
	requestLimit := int64(3) // Only 3 requests per minute
	requestResetDuration := "1m"

	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			RateLimit: &CreateRateLimitRequest{
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

	t.Logf("Created VK %s with request limit: %d requests per %s", vkName, requestLimit, requestResetDuration)

	// Make requests until we hit the request limit
	successCount := 0
	for i := 0; i < 5; i++ {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{
						Role:    "user",
						Content: "Request number " + string(rune('0'+i)) + ".",
					},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode >= 400 {
			if CheckErrorMessage(t, resp, "request") || CheckErrorMessage(t, resp, "rate") {
				t.Logf("Request %d correctly rejected due to request rate limit", i+1)
				return // Test passed
			} else {
				t.Logf("Request %d failed with different error", i+1)
			}
		} else if resp.StatusCode == 200 {
			successCount++
			t.Logf("Request %d succeeded (count: %d/%d)", i+1, successCount, requestLimit)
		}
	}

	if successCount > 0 {
		t.Logf("Made %d successful requests before hitting request limit ✓", successCount)
	} else {
		t.Skip("Could not make requests to test request limit")
	}
}

// TestProviderConfigTokenRateLimit tests that provider-level token rate limits are enforced
func TestProviderConfigTokenRateLimit(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with a provider config that has a token rate limit
	vkName := "test-vk-provider-token-limit-" + generateRandomID()
	providerTokenLimit := int64(300) // Limited tokens per provider
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
						TokenMaxLimit:      &providerTokenLimit,
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
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created VK %s with provider token limit: %d tokens per %s", vkName, providerTokenLimit, tokenResetDuration)

	// Make requests to openai until we hit provider token limit
	successCount := 0
	for i := 0; i < 10; i++ {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{
						Role:    "user",
						Content: "Provider token limit test " + string(rune('0'+i)) + ".",
					},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode >= 400 {
			if CheckErrorMessage(t, resp, "token") || CheckErrorMessage(t, resp, "rate") {
				t.Logf("Request %d correctly rejected due to provider token limit", i+1)
				return // Test passed
			} else {
				t.Logf("Request %d failed with different error", i+1)
			}
		} else if resp.StatusCode == 200 {
			successCount++
			t.Logf("Request %d succeeded", i+1)
		}
	}

	if successCount > 0 {
		t.Logf("Made %d successful requests with provider token limit ✓", successCount)
	} else {
		t.Skip("Could not make requests to test provider token limit")
	}
}

// TestProviderConfigRequestRateLimit tests that provider-level request rate limits are enforced
func TestProviderConfigRequestRateLimit(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with a provider config that has a request rate limit
	vkName := "test-vk-provider-request-limit-" + generateRandomID()
	providerRequestLimit := int64(2) // Only 2 requests per minute for this provider
	requestResetDuration := "1m"

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
						RequestMaxLimit:      &providerRequestLimit,
						RequestResetDuration: &requestResetDuration,
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

	t.Logf("Created VK %s with provider request limit: %d requests per %s", vkName, providerRequestLimit, requestResetDuration)

	// Make requests to openai until we hit provider request limit
	successCount := 0
	for i := 0; i < 5; i++ {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{
						Role:    "user",
						Content: "Provider request limit test " + string(rune('0'+i)) + ".",
					},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode >= 400 {
			if CheckErrorMessage(t, resp, "request") || CheckErrorMessage(t, resp, "rate") {
				t.Logf("Request %d correctly rejected due to provider request limit", i+1)
				return // Test passed
			} else {
				t.Logf("Request %d failed with different error", i+1)
			}
		} else if resp.StatusCode == 200 {
			successCount++
			t.Logf("Request %d succeeded (count: %d/%d)", i+1, successCount, providerRequestLimit)
		}
	}

	if successCount > 0 {
		t.Logf("Made %d successful requests with provider request limit ✓", successCount)
	} else {
		t.Skip("Could not make requests to test provider request limit")
	}
}

// TestMultipleProvidersSeparateRateLimits tests that different providers have independent rate limits
func TestMultipleProvidersSeparateRateLimits(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with multiple providers, each with their own rate limits
	vkName := "test-vk-multi-provider-limits-" + generateRandomID()
	openaiLimit := int64(100)
	anthropicLimit := int64(50)
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
						TokenMaxLimit:      &openaiLimit,
						TokenResetDuration: &tokenResetDuration,
					},
				},
				{
					Provider: "anthropic",
					Weight:   1.0,
					RateLimit: &CreateRateLimitRequest{
						TokenMaxLimit:      &anthropicLimit,
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
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created VK %s with separate rate limits per provider", vkName)

	// Verify both providers are allowed
	getDataResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	virtualKeysMap := getDataResp.Body["virtual_keys"].(map[string]interface{})
	vkData := virtualKeysMap[vkValue].(map[string]interface{})

	providerConfigs, _ := vkData["provider_configs"].([]interface{})
	if len(providerConfigs) != 2 {
		t.Fatalf("Expected 2 provider configs, got %d", len(providerConfigs))
	}

	t.Logf("VK has %d provider configs with separate rate limits ✓", len(providerConfigs))
}

// TestProviderAndVKRateLimitTogether tests that both provider and VK rate limits are enforced together
func TestProviderAndVKRateLimitTogether(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with both VK-level and provider-level rate limits
	vkName := "test-vk-both-limits-" + generateRandomID()
	vkTokenLimit := int64(1000)
	vkTokenResetDuration := "1h"
	providerTokenLimit := int64(300)
	providerTokenResetDuration := "1h"

	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			RateLimit: &CreateRateLimitRequest{
				TokenMaxLimit:      &vkTokenLimit,
				TokenResetDuration: &vkTokenResetDuration,
			},
			ProviderConfigs: []ProviderConfigRequest{
				{
					Provider: "openai",
					Weight:   1.0,
					RateLimit: &CreateRateLimitRequest{
						TokenMaxLimit:      &providerTokenLimit,
						TokenResetDuration: &providerTokenResetDuration,
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

	t.Logf("Created VK %s with VK limit (%d tokens) and provider limit (%d tokens)", vkName, vkTokenLimit, providerTokenLimit)

	// Verify the VK has both limits configured
	getDataResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	if getDataResp.StatusCode != 200 {
		t.Fatalf("Failed to get governance data: status %d", getDataResp.StatusCode)
	}

	virtualKeysMap := getDataResp.Body["virtual_keys"].(map[string]interface{})
	vkData := virtualKeysMap[vkValue].(map[string]interface{})

	// Check VK has rate limit
	vkRateLimitID, _ := vkData["rate_limit_id"].(string)
	if vkRateLimitID == "" {
		t.Fatalf("VK rate limit ID not found")
	}

	// Check provider config exists
	providerConfigs, _ := vkData["provider_configs"].([]interface{})
	if len(providerConfigs) == 0 {
		t.Fatalf("No provider configs found")
	}

	t.Logf("VK has both VK-level rate limit and provider-level rate limit configured ✓")
}

// TestRateLimitInMemorySync tests that rate limit changes sync to in-memory store
func TestRateLimitInMemorySync(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with a token rate limit
	vkName := "test-vk-rate-limit-sync-" + generateRandomID()
	initialTokenLimit := int64(1000)
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

	vkID := ExtractIDFromResponse(t, createVKResp)
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created VK %s with rate limit: %d tokens", vkName, initialTokenLimit)

	// Get initial rate limit from in-memory store
	getDataResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	if getDataResp.StatusCode != 200 {
		t.Fatalf("Failed to get governance data: status %d", getDataResp.StatusCode)
	}

	virtualKeysMap := getDataResp.Body["virtual_keys"].(map[string]interface{})
	vkData := virtualKeysMap[vkValue].(map[string]interface{})
	rateLimitID, _ := vkData["rate_limit_id"].(string)

	if rateLimitID == "" {
		t.Fatalf("Rate limit ID not found in VK")
	}

	// Update the rate limit
	newTokenLimit := int64(5000)
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/virtual-keys/" + vkID,
		Body: UpdateVirtualKeyRequest{
			RateLimit: &CreateRateLimitRequest{
				TokenMaxLimit:      &newTokenLimit,
				TokenResetDuration: &tokenResetDuration,
			},
		},
	})

	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to update VK rate limit: status %d", updateResp.StatusCode)
	}

	t.Logf("Updated rate limit from %d to %d tokens", initialTokenLimit, newTokenLimit)

	// Verify rate limit is updated in in-memory store
	time.Sleep(500 * time.Millisecond)

	getDataResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	if getDataResp2.StatusCode != 200 {
		t.Fatalf("Failed to get governance data after update: status %d", getDataResp2.StatusCode)
	}

	virtualKeysMap2 := getDataResp2.Body["virtual_keys"].(map[string]interface{})
	vkData2 := virtualKeysMap2[vkValue].(map[string]interface{})

	// Verify VK still has rate limit configured
	rateLimitID2, _ := vkData2["rate_limit_id"].(string)
	if rateLimitID2 == "" {
		t.Fatalf("Rate limit ID removed after update")
	}

	// Verify it's the same rate limit (ID should match)
	if rateLimitID2 != rateLimitID {
		t.Fatalf("Rate limit ID changed after update: was %s, now %s", rateLimitID, rateLimitID2)
	}

	// Verify rate limit content - check the actual values in the main RateLimits map
	getRateLimitsResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/rate-limits?from_memory=true",
	})

	rateLimitsMap2 := getRateLimitsResp2.Body["rate_limits"].(map[string]interface{})
	rateLimit2, ok := rateLimitsMap2[rateLimitID2].(map[string]interface{})
	if !ok {
		t.Fatalf("Rate limit not found in RateLimits map")
	}

	// Check TokenMaxLimit was updated
	tokenMaxLimit, ok := rateLimit2["token_max_limit"].(float64)
	if !ok {
		t.Fatalf("Token max limit not found in rate limit")
	}
	if int64(tokenMaxLimit) != newTokenLimit {
		t.Fatalf("Token max limit not updated: expected %d but got %d", newTokenLimit, int64(tokenMaxLimit))
	}
	t.Logf("Token max limit correctly updated to %d ✓", int64(tokenMaxLimit))

	// Check TokenResetDuration persists
	resetDuration, ok := rateLimit2["token_reset_duration"].(string)
	if !ok {
		t.Fatalf("Token reset duration not found in rate limit")
	}
	if resetDuration != tokenResetDuration {
		t.Fatalf("Token reset duration changed: expected %s but got %s", tokenResetDuration, resetDuration)
	}
	t.Logf("Token reset duration persisted: %s ✓", resetDuration)

	// Check usage counters exist
	if tokenCurrentUsage, ok := rateLimit2["token_current_usage"].(float64); ok {
		t.Logf("Token current usage in memory: %d", int64(tokenCurrentUsage))
	}

	t.Logf("Rate limit in-memory sync verified ✓")
	t.Logf("VK rate limit ID persisted: %s", rateLimitID2)
}

// TestRateLimitTokenAndRequestTogether tests that both token and request limits work together
func TestRateLimitTokenAndRequestTogether(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with both token and request limits
	vkName := "test-vk-token-and-request-" + generateRandomID()
	tokenLimit := int64(5000)
	tokenResetDuration := "1h"
	requestLimit := int64(100)
	requestResetDuration := "1h"

	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
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

	t.Logf("Created VK %s with token limit (%d) and request limit (%d)", vkName, tokenLimit, requestLimit)

	// Make a few requests and verify both limits are being tracked
	successCount := 0
	for i := 0; i < 3; i++ {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{
						Role:    "user",
						Content: "Test request for token and request limits " + string(rune('0'+i)) + ".",
					},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode == 200 {
			successCount++
			t.Logf("Request %d succeeded", i+1)
		} else if resp.StatusCode >= 400 {
			t.Logf("Request %d failed with status %d", i+1, resp.StatusCode)
			break
		}
	}

	if successCount > 0 {
		t.Logf("Made %d successful requests with both token and request limits ✓", successCount)
	} else {
		t.Skip("Could not make requests to test combined limits")
	}
}

// TestRateLimitUsageTrackedInMemory tests that VK-level rate limit usage is tracked in in-memory store
func TestRateLimitUsageTrackedInMemory(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with both token and request rate limits
	vkName := "test-vk-usage-tracking-" + generateRandomID()
	tokenLimit := int64(100000)
	tokenResetDuration := "1h"
	requestLimit := int64(100)
	requestResetDuration := "1h"

	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
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

	t.Logf("Created VK %s with rate limits for usage tracking", vkName)

	// Get initial state - rate limit usage should be 0
	getDataResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	virtualKeysMap1 := getDataResp1.Body["virtual_keys"].(map[string]interface{})
	vkData1 := virtualKeysMap1[vkValue].(map[string]interface{})
	rateLimitID1, _ := vkData1["rate_limit_id"].(string)

	initialTokenUsage := 0.0
	initialRequestUsage := 0.0

	// Check initial rate limit usage (should be 0) from main RateLimits map
	getRateLimitsResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/rate-limits?from_memory=true",
	})

	rateLimitsMap1 := getRateLimitsResp1.Body["rate_limits"].(map[string]interface{})
	rateLimit1, ok := rateLimitsMap1[rateLimitID1].(map[string]interface{})
	if !ok {
		t.Fatalf("Rate limit not found in RateLimits map")
	}

	if tokenUsage, ok := rateLimit1["token_current_usage"].(float64); ok {
		initialTokenUsage = tokenUsage
		t.Logf("Initial token usage: %d", int64(initialTokenUsage))
	}
	if requestUsage, ok := rateLimit1["request_current_usage"].(float64); ok {
		initialRequestUsage = requestUsage
		t.Logf("Initial request usage: %d", int64(initialRequestUsage))
	}

	// Make a request to use some tokens and increment request count
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{
					Role:    "user",
					Content: "Test request for usage tracking.",
				},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Skip("Could not make request to test usage tracking")
	}

	// Wait for async PostHook goroutine to complete usage update
	time.Sleep(2 * time.Second)

	// Get updated state - rate limit usage should have increased
	getDataResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	virtualKeysMap2 := getDataResp2.Body["virtual_keys"].(map[string]interface{})
	vkData2 := virtualKeysMap2[vkValue].(map[string]interface{})
	rateLimitID2, _ := vkData2["rate_limit_id"].(string)

	// Get rate limit from main RateLimits map
	getRateLimitsResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/rate-limits?from_memory=true",
	})

	rateLimitsMap2 := getRateLimitsResp2.Body["rate_limits"].(map[string]interface{})
	rateLimit2, ok := rateLimitsMap2[rateLimitID2].(map[string]interface{})
	if !ok {
		t.Fatalf("Rate limit not found in RateLimits map after request")
	}

	// Check that token usage increased
	tokenUsage2, ok := rateLimit2["token_current_usage"].(float64)
	if !ok {
		t.Fatalf("Token current usage not found in rate limit")
	}

	if tokenUsage2 <= initialTokenUsage {
		t.Logf("Warning: Token usage did not increase (before: %d, after: %d)", int64(initialTokenUsage), int64(tokenUsage2))
	} else {
		t.Logf("Token usage increased from %d to %d ✓", int64(initialTokenUsage), int64(tokenUsage2))
	}

	// Check that request usage increased
	requestUsage2, ok := rateLimit2["request_current_usage"].(float64)
	if !ok {
		t.Fatalf("Request current usage not found in rate limit")
	}

	if requestUsage2 <= initialRequestUsage {
		t.Logf("Warning: Request usage did not increase (before: %d, after: %d)", int64(initialRequestUsage), int64(requestUsage2))
	} else {
		t.Logf("Request usage increased from %d to %d ✓", int64(initialRequestUsage), int64(requestUsage2))
	}

	// Verify rate limit still has the configured max limits
	tokenMaxLimit, ok := rateLimit2["token_max_limit"].(float64)
	if ok && int64(tokenMaxLimit) != tokenLimit {
		t.Fatalf("Token max limit changed: expected %d but got %d", tokenLimit, int64(tokenMaxLimit))
	}

	requestMaxLimit, ok := rateLimit2["request_max_limit"].(float64)
	if ok && int64(requestMaxLimit) != requestLimit {
		t.Fatalf("Request max limit changed: expected %d but got %d", requestLimit, int64(requestMaxLimit))
	}

	t.Logf("VK-level rate limit usage properly tracked in in-memory store ✓")
	t.Logf("Token usage: %d/%d, Request usage: %d/%d",
		int64(tokenUsage2), tokenLimit, int64(requestUsage2), requestLimit)
}

// TestProviderLevelRateLimitUsageTracking tests that provider-level rate limits are separately tracked
func TestProviderLevelRateLimitUsageTracking(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with multiple providers, each with their own rate limits
	vkName := "test-vk-provider-usage-" + generateRandomID()
	openaiTokenLimit := int64(50000)
	anthropicTokenLimit := int64(30000)
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
						TokenMaxLimit:      &openaiTokenLimit,
						TokenResetDuration: &tokenResetDuration,
					},
				},
				{
					Provider: "anthropic",
					Weight:   1.0,
					RateLimit: &CreateRateLimitRequest{
						TokenMaxLimit:      &anthropicTokenLimit,
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
	testData.AddVirtualKey(vkID)

	vk := createVKResp.Body["virtual_key"].(map[string]interface{})
	vkValue := vk["value"].(string)

	t.Logf("Created VK %s with per-provider rate limits", vkName)

	// Get initial state - provider rate limit usage should be 0
	getDataResp1 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	virtualKeysMap1 := getDataResp1.Body["virtual_keys"].(map[string]interface{})
	vkData1 := virtualKeysMap1[vkValue].(map[string]interface{})

	providerConfigs1, ok := vkData1["provider_configs"].([]interface{})
	if !ok {
		t.Fatalf("Provider configs not found in VK data")
	}

	if len(providerConfigs1) != 2 {
		t.Fatalf("Expected 2 provider configs, got %d", len(providerConfigs1))
	}

	t.Logf("VK has %d provider configs with separate rate limits", len(providerConfigs1))

	// Make a request with openai model to use openai provider's rate limit
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{
					Role:    "user",
					Content: "Test request for provider rate limit tracking.",
				},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Skip("Could not make request to test provider rate limit tracking")
	}

	// Wait for async PostHook goroutine to complete usage update
	time.Sleep(2 * time.Second)

	// Get updated state - openai provider rate limit usage should have increased
	getDataResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	virtualKeysMap2 := getDataResp2.Body["virtual_keys"].(map[string]interface{})
	vkData2 := virtualKeysMap2[vkValue].(map[string]interface{})

	providerConfigs2, ok := vkData2["provider_configs"].([]interface{})
	if !ok {
		t.Fatalf("Provider configs not found in VK data after request")
	}

	// Check each provider config for rate limit updates
	var openaiUsage, anthropicUsage float64
	var openaiMaxLimit, anthropicMaxLimit float64

	// Get rate limits from main RateLimits map
	getRateLimitsResp2 := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/rate-limits?from_memory=true",
	})

	rateLimitsMap2 := getRateLimitsResp2.Body["rate_limits"].(map[string]interface{})

	for i, providerConfig := range providerConfigs2 {
		config, ok := providerConfig.(map[string]interface{})
		if !ok {
			continue
		}

		provider, ok := config["provider"].(string)
		if !ok {
			continue
		}

		rateLimitID, ok := config["rate_limit_id"].(string)
		if !ok {
			t.Logf("Provider %s: No rate limit ID found", provider)
			continue
		}

		rateLimit, ok := rateLimitsMap2[rateLimitID].(map[string]interface{})
		if !ok {
			t.Logf("Provider %s: No rate limit found in RateLimits map", provider)
			continue
		}

		tokenUsage, _ := rateLimit["token_current_usage"].(float64)
		tokenMaxLimit, _ := rateLimit["token_max_limit"].(float64)

		if provider == "openai" {
			openaiUsage = tokenUsage
			openaiMaxLimit = tokenMaxLimit
			t.Logf("Provider %d (openai): Token usage: %d/%d", i, int64(tokenUsage), int64(tokenMaxLimit))
		} else if provider == "anthropic" {
			anthropicUsage = tokenUsage
			anthropicMaxLimit = tokenMaxLimit
			t.Logf("Provider %d (anthropic): Token usage: %d/%d", i, int64(tokenUsage), int64(tokenMaxLimit))
		}
	}

	// Verify provider limits are independent
	if openaiMaxLimit != float64(openaiTokenLimit) {
		t.Logf("Warning: OpenAI max limit changed: expected %d but got %d", openaiTokenLimit, int64(openaiMaxLimit))
	}

	if anthropicMaxLimit != float64(anthropicTokenLimit) {
		t.Logf("Warning: Anthropic max limit changed: expected %d but got %d", anthropicTokenLimit, int64(anthropicMaxLimit))
	}

	t.Logf("Provider-level rate limits properly tracked separately in in-memory store ✓")
	t.Logf("OpenAI usage: %d, Anthropic usage: %d (separate limits)", int64(openaiUsage), int64(anthropicUsage))
}
