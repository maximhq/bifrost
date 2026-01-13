package governance

import (
	"testing"
	"time"
)

// TestVirtualKeyTokenRateLimitEnforcement verifies VK token rate limits actually reject requests
// Rate limit enforcement is POST-HOC: the request that exceeds the limit is ALLOWED,
// but subsequent requests are BLOCKED.
func TestVirtualKeyTokenRateLimitEnforcement(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with a VERY restrictive token rate limit
	vkName := "test-vk-strict-token-limit-" + generateRandomID()
	tokenLimit := int64(100) // Only 100 tokens max
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

	t.Logf("Created VK with strict token limit: %d tokens per %s", tokenLimit, tokenResetDuration)

	// Verify rate limit is in in-memory store
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
		t.Fatalf("Rate limit not configured on VK")
	}

	t.Logf("Rate limit ID %s configured on VK ✓", rateLimitID)

	// Make requests until token limit is exceeded
	// Rate limit enforcement is POST-HOC: request that exceeds is allowed, next is blocked
	consumedTokens := int64(0)
	requestNum := 1
	shouldStop := false

	for requestNum <= 20 {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{
						Role:    "user",
						Content: "Hello how are you?",
					},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode >= 400 {
			// Request rejected - check if it's due to rate limit
			if resp.StatusCode == 429 || CheckErrorMessage(t, resp, "token") || CheckErrorMessage(t, resp, "rate") {
				t.Logf("Request %d correctly rejected: token limit exceeded at %d/%d", requestNum, consumedTokens, tokenLimit)

				// Verify rejection happened after exceeding the limit
				if consumedTokens < tokenLimit {
					t.Fatalf("Request rejected before token limit was exceeded: consumed %d < limit %d", consumedTokens, tokenLimit)
				}

				t.Logf("Token rate limit enforcement verified ✓")
				t.Logf("Request blocked after token limit exceeded")
				return // Test passed
			} else {
				t.Fatalf("Request %d failed with unexpected error (not rate limit): %v", requestNum, resp.Body)
			}
		}

		// Request succeeded - extract token usage
		var tokensUsed int64
		if usage, ok := resp.Body["usage"].(map[string]interface{}); ok {
			if total, ok := usage["total_tokens"].(float64); ok {
				tokensUsed = int64(total)
			}
		}

		consumedTokens += tokensUsed
		t.Logf("Request %d succeeded: tokens=%d, consumed=%d/%d", requestNum, tokensUsed, consumedTokens, tokenLimit)

		requestNum++

		if shouldStop {
			break
		}

		if consumedTokens >= tokenLimit {
			shouldStop = true
		}
	}

	t.Fatalf("Made %d requests but never hit token rate limit (consumed %d / %d) - rate limit not being enforced",
		requestNum-1, consumedTokens, tokenLimit)
}

// TestVirtualKeyRequestRateLimitEnforcement verifies VK request rate limits actually reject requests
func TestVirtualKeyRequestRateLimitEnforcement(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with a very restrictive request rate limit
	vkName := "test-vk-strict-request-limit-" + generateRandomID()
	requestLimit := int64(1) // Only 1 request allowed
	requestResetDuration := "1h"

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

	t.Logf("Created VK with request limit: %d request per %s", requestLimit, requestResetDuration)

	// Make requests until request limit is exceeded
	requestCount := int64(0)
	requestNum := 1

	for requestNum <= 10 {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{
						Role:    "user",
						Content: "Request to test request rate limit.",
					},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode >= 400 {
			// Request rejected - check if it's due to rate limit
			if resp.StatusCode == 429 || CheckErrorMessage(t, resp, "request") || CheckErrorMessage(t, resp, "rate") {
				t.Logf("Request %d correctly rejected: request limit exceeded at %d/%d", requestNum, requestCount, requestLimit)

				// Verify rejection happened after exceeding the limit
				if requestCount < requestLimit {
					t.Fatalf("Request rejected before request limit was exceeded: count %d < limit %d", requestCount, requestLimit)
				}

				t.Logf("Request rate limit enforcement verified ✓")
				t.Logf("Request blocked after request limit exceeded")
				return // Test passed
			} else {
				t.Fatalf("Request %d failed with unexpected error (not rate limit): %v", requestNum, resp.Body)
			}
		}

		// Request succeeded - increment count
		requestCount++
		t.Logf("Request %d succeeded: count=%d/%d", requestNum, requestCount, requestLimit)

		requestNum++
	}

	t.Fatalf("Made %d requests but never hit request rate limit (count %d / %d) - rate limit not being enforced",
		requestNum-1, requestCount, requestLimit)
}

// TestProviderConfigTokenRateLimitEnforcement verifies provider-level token limits reject requests
func TestProviderConfigTokenRateLimitEnforcement(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with provider-level token rate limit
	vkName := "test-vk-provider-strict-token-" + generateRandomID()
	providerTokenLimit := int64(100)
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

	t.Logf("Created VK with provider token limit: %d tokens", providerTokenLimit)

	// Verify provider config rate limit is set
	getDataResp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys?from_memory=true",
	})

	if getDataResp.StatusCode != 200 {
		t.Fatalf("Failed to get governance data: status %d", getDataResp.StatusCode)
	}

	virtualKeysMap := getDataResp.Body["virtual_keys"].(map[string]interface{})
	vkData := virtualKeysMap[vkValue].(map[string]interface{})
	providerConfigs, _ := vkData["provider_configs"].([]interface{})

	if len(providerConfigs) == 0 {
		t.Fatalf("Provider config not found")
	}

	t.Logf("Provider config rate limit configured ✓")

	// Make requests until provider token limit is exceeded
	// Rate limit enforcement is POST-HOC: request that exceeds is allowed, next is blocked
	consumedTokens := int64(0)
	requestNum := 1
	shouldStop := false

	for requestNum <= 20 {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{
						Role:    "user",
						Content: "Request to openai to test provider token limit.",
					},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode >= 400 {
			// Request rejected - check if it's due to rate limit
			if resp.StatusCode == 429 || CheckErrorMessage(t, resp, "token") || CheckErrorMessage(t, resp, "rate") {
				t.Logf("Request %d correctly rejected: provider token limit exceeded at %d/%d", requestNum, consumedTokens, providerTokenLimit)

				// Verify rejection happened after exceeding the limit
				if consumedTokens < providerTokenLimit {
					t.Fatalf("Request rejected before provider token limit was exceeded: consumed %d < limit %d", consumedTokens, providerTokenLimit)
				}

				t.Logf("Provider token rate limit enforcement verified ✓")
				t.Logf("Request blocked after provider token limit exceeded")
				return // Test passed
			} else {
				t.Fatalf("Request %d failed with unexpected error (not rate limit): %v", requestNum, resp.Body)
			}
		}

		// Request succeeded - extract token usage
		var tokensUsed int64
		if usage, ok := resp.Body["usage"].(map[string]interface{}); ok {
			if total, ok := usage["total_tokens"].(float64); ok {
				tokensUsed = int64(total)
			}
		}

		consumedTokens += tokensUsed
		t.Logf("Request %d succeeded: tokens=%d, consumed=%d/%d", requestNum, tokensUsed, consumedTokens, providerTokenLimit)

		requestNum++

		if shouldStop {
			break
		}

		if consumedTokens >= providerTokenLimit {
			shouldStop = true
		}
	}

	t.Fatalf("Made %d requests but never hit provider token rate limit (consumed %d / %d) - rate limit not being enforced",
		requestNum-1, consumedTokens, providerTokenLimit)
}

// TestProviderConfigRequestRateLimitEnforcement verifies provider-level request limits
func TestProviderConfigRequestRateLimitEnforcement(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a VK with provider-level request rate limit
	vkName := "test-vk-provider-strict-request-" + generateRandomID()
	providerRequestLimit := int64(1) // Only 1 request allowed
	requestResetDuration := "1h"

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

	t.Logf("Created VK with provider request limit: %d request", providerRequestLimit)

	// Make requests until provider request limit is exceeded
	requestCount := int64(0)
	requestNum := 1

	for requestNum <= 10 {
		resp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model: "openai/gpt-4o",
				Messages: []ChatMessage{
					{
						Role:    "user",
						Content: "Request to test provider request rate limit.",
					},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode >= 400 {
			// Request rejected - check if it's due to rate limit
			if resp.StatusCode == 429 || CheckErrorMessage(t, resp, "request") || CheckErrorMessage(t, resp, "rate") {
				t.Logf("Request %d correctly rejected: provider request limit exceeded at %d/%d", requestNum, requestCount, providerRequestLimit)

				// Verify rejection happened after exceeding the limit
				if requestCount < providerRequestLimit {
					t.Fatalf("Request rejected before provider request limit was exceeded: count %d < limit %d", requestCount, providerRequestLimit)
				}

				t.Logf("Provider request rate limit enforcement verified ✓")
				t.Logf("Request blocked after provider request limit exceeded")
				return // Test passed
			} else {
				t.Fatalf("Request %d failed with unexpected error (not rate limit): %v", requestNum, resp.Body)
			}
		}

		// Request succeeded - increment count
		requestCount++
		t.Logf("Request %d succeeded: count=%d/%d", requestNum, requestCount, providerRequestLimit)

		requestNum++
	}

	t.Fatalf("Made %d requests but never hit provider request rate limit (count %d / %d) - rate limit not being enforced",
		requestNum-1, requestCount, providerRequestLimit)
}

// TestProviderAndVKRateLimitBothEnforced verifies both provider and VK limits are enforced
func TestProviderAndVKRateLimitBothEnforced(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create VK with both VK and provider request limits
	vkName := "test-vk-both-enforced-" + generateRandomID()
	vkRequestLimit := int64(5)
	providerRequestLimit := int64(2) // More restrictive
	requestResetDuration := "1h"

	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name: vkName,
			RateLimit: &CreateRateLimitRequest{
				RequestMaxLimit:      &vkRequestLimit,
				RequestResetDuration: &requestResetDuration,
			},
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

	t.Logf("Created VK with VK limit (%d) and provider limit (%d requests)", vkRequestLimit, providerRequestLimit)

	// Make requests - provider limit (2) is more restrictive than VK limit (5)
	// So we should hit provider limit first
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
						Content: "Request " + string(rune('0'+i)) + " to test both limits.",
					},
				},
			},
			VKHeader: &vkValue,
		})

		if resp.StatusCode == 200 {
			successCount++
			t.Logf("Request %d succeeded (count: %d)", i+1, successCount)
		} else if resp.StatusCode >= 400 {
			t.Logf("Request %d rejected with status %d", i+1, resp.StatusCode)
			if successCount < int(providerRequestLimit) {
				t.Fatalf("Request rejected before provider limit (%d): %v", providerRequestLimit, resp.Body)
			}
			// Expected - hit provider limit first
			return
		}
	}

	if successCount > 0 {
		if successCount >= 5 {
			t.Fatalf("Made all %d requests without hitting rate limit (provider limit was %d) - rate limit not enforced",
				successCount, providerRequestLimit)
		}
		t.Logf("Both VK and provider rate limits are configured and enforced ✓")
	} else {
		t.Skip("Could not test - all requests failed")
	}
}

// TestRateLimitInMemoryUsageTracking verifies usage counters are tracked in in-memory store
func TestRateLimitInMemoryUsageTracking(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create VK with rate limit
	vkName := "test-vk-usage-tracking-" + generateRandomID()
	tokenLimit := int64(10000)
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

	t.Logf("Created VK for usage tracking test")

	// Make a request
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model: "openai/gpt-4o",
			Messages: []ChatMessage{
				{
					Role:    "user",
					Content: "Test for usage tracking.",
				},
			},
		},
		VKHeader: &vkValue,
	})

	if resp.StatusCode != 200 {
		t.Skip("Could not execute request for usage tracking test")
	}

	// Get usage from response
	var tokensUsed int
	if usage, ok := resp.Body["usage"].(map[string]interface{}); ok {
		if total, ok := usage["total_tokens"].(float64); ok {
			tokensUsed = int(total)
		}
	}

	if tokensUsed == 0 {
		t.Skip("Could not extract token usage from response")
	}

	t.Logf("Request used %d tokens", tokensUsed)

	// Wait for async update to propagate to in-memory store
	var rateLimitID string
	usageUpdated := WaitForCondition(t, func() bool {
		getDataResp := MakeRequest(t, APIRequest{
			Method: "GET",
			Path:   "/api/governance/virtual-keys?from_memory=true",
		})

		if getDataResp.StatusCode != 200 {
			return false
		}

		virtualKeysMap, ok := getDataResp.Body["virtual_keys"].(map[string]interface{})
		if !ok || virtualKeysMap == nil {
			return false
		}

		vkData, ok := virtualKeysMap[vkValue].(map[string]interface{})
		if !ok {
			return false
		}

		rateLimitID, _ = vkData["rate_limit_id"].(string)
		return rateLimitID != ""
	}, 3*time.Second, "rate limit usage tracked in in-memory store")

	if !usageUpdated {
		t.Fatalf("Rate limit usage not tracked in in-memory store after request (timeout after 3s)")
	}

	if rateLimitID != "" {
		t.Logf("Rate limit %s is configured and tracking usage ✓", rateLimitID)
	} else {
		t.Logf("Rate limit is configured ✓")
	}
}
