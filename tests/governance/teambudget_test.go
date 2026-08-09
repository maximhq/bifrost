package governance

import (
	"strconv"
	"testing"
	"time"
)

// TestTeamBudgetExceededWithMultipleVKs tests that team level budgets are enforced across multiple VKs
// by making requests until budget is consumed
func TestTeamBudgetExceededWithMultipleVKs(t *testing.T) {
	t.Parallel()
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Create a team with a fixed budget
	teamBudget := 0.01
	teamName := "test-team-budget-exceeded-" + generateRandomID()
	createTeamResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/teams",
		Body: CreateTeamRequest{
			Name: teamName,
			Budgets: []BudgetRequest{{
				MaxLimit:      teamBudget,
				ResetDuration: "1h",
			}},
		},
	})

	if createTeamResp.StatusCode != 200 {
		t.Fatalf("Failed to create team: status %d", createTeamResp.StatusCode)
	}

	teamID := ExtractIDFromResponse(t, createTeamResp)
	testData.AddTeam(teamID)

	// Create 2 VKs under the team
	var vkValues []string
	for i := 1; i <= 2; i++ {
		createVKResp := MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/api/governance/virtual-keys",
			Body: CreateVirtualKeyRequest{
				Name:            "test-vk-" + generateRandomID(),
				ProviderConfigs: defaultProviderConfigs(),
				TeamID:          &teamID,
				Budgets: []BudgetRequest{{
					MaxLimit:      1.0, // High VK budget so team is the limiting factor
					ResetDuration: "1h",
				}},
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

	t.Logf("Created team %s with budget $%.2f and 2 VKs", teamName, teamBudget)

	// Keep making requests alternating between VKs, tracking actual token usage until team budget is exceeded
	consumedBudget := 0.0
	requestNum := 1
	var lastSuccessfulCost float64
	var shouldStop = false
	vkIndex := 0

	for requestNum <= 50 {
		// Alternate between VKs to test shared team budget
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
			if CheckErrorMessage(t, resp, "budget") || CheckErrorMessage(t, resp, "team") {
				t.Logf("Request %d correctly rejected: team budget exceeded", requestNum)
				t.Logf("Consumed budget: $%.6f (limit: $%.2f)", consumedBudget, teamBudget)
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
						requestNum, (vkIndex%2)+1, actualInputTokens, actualOutputTokens, actualCost, consumedBudget, teamBudget)
				}
			}
		}

		requestNum++
		vkIndex++

		if shouldStop {
			break
		}

		if consumedBudget >= teamBudget {
			shouldStop = true
		}
	}

	t.Fatalf("Made %d requests but never hit team budget limit (consumed $%.6f / $%.2f) - budget not being enforced",
		requestNum-1, consumedBudget, teamBudget)
}

// TestTeamBudgetCalendarAlignmentAppliesFromNextPeriod pins what enabling
// calendar alignment on an existing budget actually does.
//
// It leaves the current window alone. The docs and the handler used to promise
// that last_reset snapped back to the period start and usage was cleared, but
// neither ever happened: both values were written through UpdateBudget, which
// copies them back from the stored row on every config write so a config.json
// sync cannot replay stale values over live accounting.
//
// Deleting that dead code rather than making it work is deliberate. The intended
// snap moves the boundary backwards, and every persistence path guards last_reset
// forward-only so that cluster nodes agree on which window is current. Alignment
// still takes hold - from the next period boundary.
//
// Checked on a monthly budget with no usage, so it costs no inference: a budget
// created now is anchored at the creation instant, while the period start is
// midnight on the 1st.
func TestTeamBudgetCalendarAlignmentAppliesFromNextPeriod(t *testing.T) {
	// Not parallel: this issues a PUT that contends with the other governance
	// tests on the default SQLite store.
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	createResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/teams",
		Body: CreateTeamRequest{
			Name: "test-team-calendar-align-" + generateRandomID(),
			Budgets: []BudgetRequest{{
				MaxLimit:      100,
				ResetDuration: "1M",
			}},
		},
	})
	if createResp.StatusCode != 200 {
		t.Fatalf("Failed to create team: status %d, body %v", createResp.StatusCode, createResp.Body)
	}
	teamID := ExtractIDFromResponse(t, createResp)
	testData.AddTeam(teamID)

	// A non-aligned budget anchors on its creation instant, not the 1st, so the
	// two values below are distinguishable.
	before := teamBudgetLastReset(t, teamID)
	if before.Equal(monthStart) {
		t.Fatalf("precondition failed: a non-aligned budget already sits on the period start %s", monthStart.Format(time.RFC3339))
	}

	aligned := true
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/teams/" + teamID,
		Body:   UpdateTeamRequest{CalendarAligned: &aligned},
	})
	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to enable calendar alignment: status %d, body %v", updateResp.StatusCode, updateResp.Body)
	}

	after := teamBudgetLastReset(t, teamID)
	if !after.Equal(before) {
		t.Errorf("enabling calendar alignment moved last_reset from %s to %s; the current window must be left alone",
			before.Format(time.RFC3339), after.Format(time.RFC3339))
	}
}

// teamBudgetLastReset reads the first budget's last_reset off a team.
func teamBudgetLastReset(t *testing.T, teamID string) time.Time {
	t.Helper()
	resp := MakeRequest(t, APIRequest{Method: "GET", Path: "/api/governance/teams/" + teamID})
	if resp.StatusCode != 200 {
		t.Fatalf("Failed to read team %s: status %d, body %v", teamID, resp.StatusCode, resp.Body)
	}
	team, ok := resp.Body["team"].(map[string]interface{})
	if !ok {
		team = resp.Body
	}
	budgets, ok := team["budgets"].([]interface{})
	if !ok || len(budgets) == 0 {
		t.Fatalf("team %s has no budgets: %v", teamID, team)
	}
	budget, ok := budgets[0].(map[string]interface{})
	if !ok {
		t.Fatalf("team %s first budget is not an object: %v", teamID, budgets[0])
	}
	raw, ok := budget["last_reset"].(string)
	if !ok {
		t.Fatalf("team %s budget has no last_reset string: %v", teamID, budget)
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("could not parse last_reset %q: %v", raw, err)
	}
	return parsed.UTC()
}

// TestTeamRateLimitCalendarAlignmentAppliesFromNextPeriod is the rate-limit half
// of the same contract.
//
// Each calendar-align block snapped its owner's rate limit as well as its
// budgets, through UpdateRateLimit, which carries both usage counters and both
// reset stamps forward exactly as UpdateBudget does. That half was discarded too,
// and is worth pinning separately: budgets and rate limits are persisted by
// different store methods, so one could regress without the other.
func TestTeamRateLimitCalendarAlignmentAppliesFromNextPeriod(t *testing.T) {
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	monthly := "1M"
	tokenLimit := int64(100000)
	createResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/teams",
		Body: CreateTeamRequest{
			Name:      "test-team-rl-calendar-align-" + generateRandomID(),
			RateLimit: &CreateRateLimitRequest{TokenMaxLimit: &tokenLimit, TokenResetDuration: &monthly},
		},
	})
	if createResp.StatusCode != 200 {
		t.Fatalf("Failed to create team: status %d, body %v", createResp.StatusCode, createResp.Body)
	}
	teamID := ExtractIDFromResponse(t, createResp)
	testData.AddTeam(teamID)

	before := teamRateLimitTokenLastReset(t, teamID)

	aligned := true
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/teams/" + teamID,
		Body:   UpdateTeamRequest{CalendarAligned: &aligned},
	})
	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to enable calendar alignment: status %d, body %v", updateResp.StatusCode, updateResp.Body)
	}

	after := teamRateLimitTokenLastReset(t, teamID)
	if !after.Equal(before) {
		t.Errorf("enabling calendar alignment moved token_last_reset from %s to %s; the current window must be left alone",
			before.Format(time.RFC3339), after.Format(time.RFC3339))
	}
}

// teamRateLimitTokenLastReset reads a team's rate-limit token reset stamp.
func teamRateLimitTokenLastReset(t *testing.T, teamID string) time.Time {
	t.Helper()
	resp := MakeRequest(t, APIRequest{Method: "GET", Path: "/api/governance/teams/" + teamID})
	if resp.StatusCode != 200 {
		t.Fatalf("Failed to read team %s: status %d, body %v", teamID, resp.StatusCode, resp.Body)
	}
	team, ok := resp.Body["team"].(map[string]interface{})
	if !ok {
		team = resp.Body
	}
	rateLimit, ok := team["rate_limit"].(map[string]interface{})
	if !ok {
		t.Fatalf("team %s has no rate limit: %v", teamID, team)
	}
	raw, ok := rateLimit["token_last_reset"].(string)
	if !ok {
		t.Fatalf("team %s rate limit has no token_last_reset: %v", teamID, rateLimit)
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("could not parse token_last_reset %q: %v", raw, err)
	}
	return parsed.UTC()
}

// TestTeamResetBudgetUsageClearsSpend covers the team owner's reset path.
//
// Each owner reconciles budgets through different code - teams use an inline
// loop, customers and model configs use their own reconcilers - so they are
// covered separately rather than by analogy from the virtual key case.
//
// Spend is accrued through a virtual key attached to the team, which is how team
// budgets are charged in the first place.
func TestTeamResetBudgetUsageClearsSpend(t *testing.T) {
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	createTeamResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/teams",
		Body: CreateTeamRequest{
			Name:    "test-team-reset-usage-" + generateRandomID(),
			Budgets: []BudgetRequest{{MaxLimit: 50, ResetDuration: "1M"}},
		},
	})
	if createTeamResp.StatusCode != 200 {
		t.Fatalf("Failed to create team: status %d, body %v", createTeamResp.StatusCode, createTeamResp.Body)
	}
	teamID := ExtractIDFromResponse(t, createTeamResp)
	testData.AddTeam(teamID)

	createVKResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:            "test-vk-team-reset-" + generateRandomID(),
			TeamID:          &teamID,
			ProviderConfigs: defaultProviderConfigs(),
		},
	})
	if createVKResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d, body %v", createVKResp.StatusCode, createVKResp.Body)
	}
	vkID := ExtractIDFromResponse(t, createVKResp)
	testData.AddVirtualKey(vkID)
	vkValue := createVKResp.Body["virtual_key"].(map[string]interface{})["value"].(string)

	spendVia(t, vkValue)
	spent := waitForTeamBudgetUsage(t, teamID)
	t.Logf("team recorded $%.8f of usage before the reset", spent)

	resetUsage := true
	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/teams/" + teamID,
		Body: UpdateTeamRequest{
			Budgets:          &[]BudgetRequest{{MaxLimit: 60, ResetDuration: "1M"}},
			ResetBudgetUsage: &resetUsage,
		},
	})
	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to reset team budget usage: status %d, body %v", updateResp.StatusCode, updateResp.Body)
	}

	if usage := teamBudgetUsage(t, teamID); usage != 0 {
		t.Errorf("reset_budget_usage did not clear team spend: usage is still %v", usage)
	}
}

// spendVia makes one small completion against a virtual key so its owners accrue usage.
func spendVia(t *testing.T, vkValue string) {
	t.Helper()
	resp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/v1/chat/completions",
		Body: ChatCompletionRequest{
			Model:    "openai/gpt-4o",
			Messages: []ChatMessage{{Role: "user", Content: "Reply with the single word: ok"}},
		},
		VKHeader: &vkValue,
	})
	if resp.StatusCode >= 400 {
		t.Fatalf("failed to spend: status %d, body %v", resp.StatusCode, resp.Body)
	}
}

// teamBudgetUsage reads the first budget's current usage off a team.
func teamBudgetUsage(t *testing.T, teamID string) float64 {
	t.Helper()
	resp := MakeRequest(t, APIRequest{Method: "GET", Path: "/api/governance/teams/" + teamID})
	if resp.StatusCode != 200 {
		t.Fatalf("Failed to read team %s: status %d", teamID, resp.StatusCode)
	}
	team, ok := resp.Body["team"].(map[string]interface{})
	if !ok {
		team = resp.Body
	}
	budgets, ok := team["budgets"].([]interface{})
	if !ok || len(budgets) == 0 {
		t.Fatalf("team %s has no budgets: %v", teamID, team)
	}
	budget, ok := budgets[0].(map[string]interface{})
	if !ok {
		t.Fatalf("team %s first budget is not an object: %v", teamID, budgets[0])
	}
	// Checked rather than defaulted to 0: every reset assertion is "usage is now
	// zero", so a helper that silently returns 0 for a missing field would make
	// those assertions pass without the reset having done anything.
	usage, ok := budget["current_usage"].(float64)
	if !ok {
		t.Fatalf("team %s budget has no current_usage: %v", teamID, budget)
	}
	return usage
}

// waitForTeamBudgetUsage polls until spend is recorded, which happens
// asynchronously relative to the completion response.
func waitForTeamBudgetUsage(t *testing.T, teamID string) float64 {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if usage := teamBudgetUsage(t, teamID); usage > 0 {
			return usage
		}
		if time.Now().After(deadline) {
			t.Fatalf("team %s recorded no budget usage after a successful completion", teamID)
		}
		time.Sleep(time.Second)
	}
}
