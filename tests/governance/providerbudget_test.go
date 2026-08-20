package governance

import (
	"strconv"
	"testing"
	"time"
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
			Budgets: []BudgetRequest{{
				MaxLimit:      1.0, // High overall budget
				ResetDuration: "1h",
			}},
			ProviderConfigs: []ProviderConfigRequest{
				{
					Provider:      "openai",
					Weight:        float64Ptr(1.0),
					AllowedModels: []string{"*"},
					KeyIDs:        []string{"*"},
					Budgets: []BudgetRequest{{
						MaxLimit:      0.01, // Specific OpenAI budget
						ResetDuration: "1h",
					}},
				},
				{
					Provider:      "anthropic",
					Weight:        float64Ptr(1.0),
					AllowedModels: []string{"*"},
					KeyIDs:        []string{"*"},
					Budgets: []BudgetRequest{{
						MaxLimit:      0.01, // Specific Anthropic budget
						ResetDuration: "1h",
					}},
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
		var shouldStop = false

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
		var shouldStop = false

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

// TestProviderGovernanceResetBudgetUsageClearsSpend covers provider-level
// governance, which is a distinct owner from a virtual key's per-provider budgets
// above: it applies to every request to that provider regardless of which key
// made it, and it is stored on a VK-agnostic model config.
//
// That storage detail is why it needs its own case. The reset is addressed to the
// model config that actually owns the budgets, not to the provider, and an
// implementation that addressed the provider would leave peers reloading an
// entity that owns nothing.
func TestProviderGovernanceResetBudgetUsageClearsSpend(t *testing.T) {
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Provider governance is global, so leave it clean regardless of outcome.
	defer func() {
		MakeRequest(t, APIRequest{Method: "DELETE", Path: "/api/governance/providers/openai"})
	}()

	setResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/providers/openai",
		Body:   UpdateProviderGovernanceRequest{Budgets: &[]BudgetRequest{{MaxLimit: 50, ResetDuration: "1M"}}},
	})
	if setResp.StatusCode != 200 {
		t.Fatalf("Failed to set provider governance: status %d, body %v", setResp.StatusCode, setResp.Body)
	}

	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:            "test-vk-provider-gov-reset-" + generateRandomID(),
			ProviderConfigs: defaultProviderConfigs(),
		},
	})
	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d, body %v", createVKResp.StatusCode, createVKResp.Body)
	}
	testData.AddVirtualKey(ExtractIDFromResponse(t, createVKResp))
	spendVia(t, createVKResp.Body["virtual_key"].(map[string]interface{})["value"].(string))

	deadline := time.Now().Add(30 * time.Second)
	var spent float64
	for {
		spent = providerGovernanceUsage(t, "openai")
		if spent > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("provider governance recorded no usage after a successful completion")
		}
		time.Sleep(time.Second)
	}
	t.Logf("provider governance recorded $%.8f of usage before the reset", spent)

	resetUsage := true
	resetResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/providers/openai",
		Body: UpdateProviderGovernanceRequest{
			Budgets:          &[]BudgetRequest{{MaxLimit: 60, ResetDuration: "1M"}},
			ResetBudgetUsage: &resetUsage,
		},
	})
	if resetResp.StatusCode != 200 {
		t.Fatalf("Failed to reset provider governance usage: status %d, body %v", resetResp.StatusCode, resetResp.Body)
	}

	if usage := providerGovernanceUsage(t, "openai"); usage != 0 {
		t.Errorf("reset_budget_usage did not clear provider governance spend: usage is still %v", usage)
	}
}

// TestProviderGovernanceCalendarAlignmentAppliesFromNextPeriod is the third snap
// site, alongside teams and customers. All three shared the same discarded write,
// and all three persist through different store methods, so each is pinned.
func TestProviderGovernanceCalendarAlignmentAppliesFromNextPeriod(t *testing.T) {
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)
	defer func() {
		MakeRequest(t, APIRequest{Method: "DELETE", Path: "/api/governance/providers/openai"})
	}()

	setResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/providers/openai",
		Body:   UpdateProviderGovernanceRequest{Budgets: &[]BudgetRequest{{MaxLimit: 50, ResetDuration: "1M"}}},
	})
	if setResp.StatusCode != 200 {
		t.Fatalf("Failed to set provider governance: status %d, body %v", setResp.StatusCode, setResp.Body)
	}

	before := providerGovernanceLastReset(t, "openai")

	aligned := true
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/providers/openai",
		Body:   UpdateProviderGovernanceRequest{CalendarAligned: &aligned},
	})
	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to enable calendar alignment: status %d, body %v", updateResp.StatusCode, updateResp.Body)
	}

	after := providerGovernanceLastReset(t, "openai")
	if !after.Equal(before) {
		t.Errorf("enabling calendar alignment moved last_reset from %s to %s; the current window must be left alone",
			before.Format(time.RFC3339), after.Format(time.RFC3339))
	}
}

// providerGovernanceBudget returns the first budget recorded against a provider.
func providerGovernanceBudget(t *testing.T, provider string) map[string]interface{} {
	t.Helper()
	resp := MakeRequest(t, APIRequest{Method: "GET", Path: "/api/governance/providers"})
	if resp.StatusCode != 200 {
		t.Fatalf("Failed to read provider governance: status %d, body %v", resp.StatusCode, resp.Body)
	}
	providers, ok := resp.Body["providers"].([]interface{})
	if !ok {
		t.Fatalf("unexpected provider governance response: %v", resp.Body)
	}
	for _, raw := range providers {
		entry, ok := raw.(map[string]interface{})
		if !ok || entry["provider"] != provider {
			continue
		}
		budgets, ok := entry["budgets"].([]interface{})
		if !ok || len(budgets) == 0 {
			t.Fatalf("provider %s has no budgets: %v", provider, entry)
		}
		budget, _ := budgets[0].(map[string]interface{})
		return budget
	}
	t.Fatalf("provider %s not found in governance response", provider)
	return nil
}

func providerGovernanceUsage(t *testing.T, provider string) float64 {
	t.Helper()
	budget := providerGovernanceBudget(t, provider)
	// Checked rather than defaulted to 0: every reset assertion is "usage is now
	// zero", so a helper that silently returns 0 for a missing field would make
	// those assertions pass without the reset having done anything.
	usage, ok := budget["current_usage"].(float64)
	if !ok {
		t.Fatalf("provider %s governance budget has no current_usage: %v", provider, budget)
	}
	return usage
}

func providerGovernanceLastReset(t *testing.T, provider string) time.Time {
	t.Helper()
	raw, _ := providerGovernanceBudget(t, provider)["last_reset"].(string)
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("could not parse last_reset %q: %v", raw, err)
	}
	return parsed.UTC()
}

// TestModelConfigResetBudgetUsageClearsSpend covers model-scoped limits, the last
// budget owner. Its budgets are reconciled by reconcileModelConfigBudgets, the
// same function the virtual key path uses, but reached through a different
// handler, so the wiring is verified independently.
func TestModelConfigResetBudgetUsageClearsSpend(t *testing.T) {
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	provider := "openai"
	createResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/model-configs",
		Body: CreateModelConfigRequest{
			ModelName: "gpt-4o",
			Provider:  &provider,
			Scope:     "global",
			Budgets:   []BudgetRequest{{MaxLimit: 50, ResetDuration: "1M"}},
		},
	})
	if createResp.StatusCode != 200 {
		t.Fatalf("Failed to create model config: status %d, body %v", createResp.StatusCode, createResp.Body)
	}
	mcID := modelConfigIDFromResponse(t, createResp)
	defer func() {
		MakeRequest(t, APIRequest{Method: "DELETE", Path: "/api/governance/model-configs/" + mcID})
	}()

	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:            "test-vk-model-config-reset-" + generateRandomID(),
			ProviderConfigs: defaultProviderConfigs(),
		},
	})
	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d, body %v", createVKResp.StatusCode, createVKResp.Body)
	}
	testData.AddVirtualKey(ExtractIDFromResponse(t, createVKResp))
	spendVia(t, createVKResp.Body["virtual_key"].(map[string]interface{})["value"].(string))

	deadline := time.Now().Add(30 * time.Second)
	var spent float64
	for {
		spent = modelConfigUsage(t, mcID)
		if spent > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("model config %s recorded no usage after a successful completion on its model", mcID)
		}
		time.Sleep(time.Second)
	}
	t.Logf("model config recorded $%.8f of usage before the reset", spent)

	resetUsage := true
	resetResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/model-configs/" + mcID,
		Body: UpdateModelConfigRequest{
			Budgets:          []BudgetRequest{{MaxLimit: 60, ResetDuration: "1M"}},
			ResetBudgetUsage: &resetUsage,
		},
	})
	if resetResp.StatusCode != 200 {
		t.Fatalf("Failed to reset model config usage: status %d, body %v", resetResp.StatusCode, resetResp.Body)
	}

	if usage := modelConfigUsage(t, mcID); usage != 0 {
		t.Errorf("reset_budget_usage did not clear model config spend: usage is still %v", usage)
	}
}

// modelConfigIDFromResponse pulls the id out of a model-config create response,
// which nests it under a key the shared extractor does not know about.
func modelConfigIDFromResponse(t *testing.T, resp *APIResponse) string {
	t.Helper()
	if mc, ok := resp.Body["model_config"].(map[string]interface{}); ok {
		if id, ok := mc["id"].(string); ok {
			return id
		}
	}
	if id, ok := resp.Body["id"].(string); ok {
		return id
	}
	t.Fatalf("could not find model config id in response: %v", resp.Body)
	return ""
}

func modelConfigUsage(t *testing.T, mcID string) float64 {
	t.Helper()
	resp := MakeRequest(t, APIRequest{Method: "GET", Path: "/api/governance/model-configs/" + mcID})
	if resp.StatusCode != 200 {
		t.Fatalf("Failed to read model config %s: status %d, body %v", mcID, resp.StatusCode, resp.Body)
	}
	mc, ok := resp.Body["model_config"].(map[string]interface{})
	if !ok {
		mc = resp.Body
	}
	budgets, ok := mc["budgets"].([]interface{})
	if !ok || len(budgets) == 0 {
		t.Fatalf("model config %s has no budgets: %v", mcID, mc)
	}
	budget, ok := budgets[0].(map[string]interface{})
	if !ok {
		t.Fatalf("model config %s first budget is not an object: %v", mcID, budgets[0])
	}
	// Checked rather than defaulted to 0: every reset assertion is "usage is now
	// zero", so a helper that silently returns 0 for a missing field would make
	// those assertions pass without the reset having done anything.
	usage, ok := budget["current_usage"].(float64)
	if !ok {
		t.Fatalf("model config %s budget has no current_usage: %v", mcID, budget)
	}
	return usage
}
