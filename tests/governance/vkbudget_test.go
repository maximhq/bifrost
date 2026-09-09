package governance

import (
	"strconv"
	"testing"
	"time"
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
			Name:            vkName,
			ProviderConfigs: defaultProviderConfigs(),
			Budgets: []BudgetRequest{{
				MaxLimit:      vkBudget,
				ResetDuration: "1h",
			}},
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

	var shouldStop = false

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

// expectedQuarterStart returns the UTC start of the fiscal quarter containing t
// for a fiscal year opening in quarterStartMonth.
//
// Deliberately re-derived here rather than read back from the API: a check that
// asks the server what it thinks and then agrees with it proves nothing. The
// arithmetic works in absolute months so the year wrap needs no special case -
// under an April fiscal year, January belongs to a quarter that opened in the
// previous calendar year.
func expectedQuarterStart(t time.Time, quarterStartMonth int) time.Time {
	t = t.UTC()
	absolute := t.Year()*12 + int(t.Month()) - 1
	offset := ((absolute-(quarterStartMonth-1))%3 + 3) % 3
	start := absolute - offset
	return time.Date(start/12, time.Month(start%12+1), 1, 0, 0, 0, 0, time.UTC)
}

// budgetFromVK reads one budget off a virtual key's in-memory governance state.
// from_memory is what actually enforces spend; the database only shows whatever
// the last dump tick happened to persist.
func budgetFromVK(t *testing.T, vkID string) map[string]interface{} {
	t.Helper()
	resp := MakeRequest(t, APIRequest{
		Method: "GET",
		Path:   "/api/governance/virtual-keys/" + vkID + "?from_memory=true",
	})
	if resp.StatusCode != 200 {
		t.Fatalf("Failed to read VK %s: status %d, body %v", vkID, resp.StatusCode, resp.Body)
	}
	vk, ok := resp.Body["virtual_key"].(map[string]interface{})
	if !ok {
		vk = resp.Body
	}
	budgets, ok := vk["budgets"].([]interface{})
	if !ok || len(budgets) == 0 {
		t.Fatalf("VK %s has no budgets in memory: %v", vkID, vk)
	}
	budget, ok := budgets[0].(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected budget shape for VK %s: %v", vkID, budgets[0])
	}
	return budget
}

// TestVKQuarterlyBudgetSnapsToFiscalQuarter verifies a calendar-aligned quarterly
// budget opens its window on the configured fiscal quarter boundary rather than
// on plain calendar quarters or on the creation timestamp.
//
// April is chosen for the fiscal start but February is what the assertion turns
// on: quarter boundaries repeat every three months, so January, April, July and
// October all produce identical reset dates. A test that only ever used April
// would pass against an implementation that ignored the setting entirely.
func TestVKQuarterlyBudgetSnapsToFiscalQuarter(t *testing.T) {
	t.Parallel()

	for _, quarterStartMonth := range []int{4, 2} {
		t.Run("quarter_start_month="+strconv.Itoa(quarterStartMonth), func(t *testing.T) {
			testData := NewGlobalTestData()
			defer testData.Cleanup(t)

			createResp := MakeRequest(t, APIRequest{
				Method: "POST",
				Path:   "/api/governance/virtual-keys",
				Body: CreateVirtualKeyRequest{
					Name:            "test-vk-quarterly-" + generateRandomID(),
					ProviderConfigs: defaultProviderConfigs(),
					CalendarAligned: true,
					Budgets: []BudgetRequest{{
						MaxLimit:      100,
						ResetDuration: "1Q",
						ResetConfig:   &BudgetResetConfigRequest{QuarterStartMonth: quarterStartMonth},
					}},
				},
			})
			if createResp.StatusCode != 200 {
				t.Fatalf("Failed to create quarterly VK: status %d, body %v", createResp.StatusCode, createResp.Body)
			}
			vkID := ExtractIDFromResponse(t, createResp)
			testData.AddVirtualKey(vkID)

			budget := budgetFromVK(t, vkID)

			resetConfig, ok := budget["reset_config"].(map[string]interface{})
			if !ok {
				t.Fatalf("quarter definition missing from the stored budget: %v", budget)
			}
			// quarter_start_month is omitempty, so a regression that zeroes it drops
			// the key entirely. Asserting through comma-ok reports that as the defect
			// it is rather than panicking and burying it in a stack trace.
			rawMonth, ok := resetConfig["quarter_start_month"].(float64)
			if !ok {
				t.Fatalf("quarter_start_month missing or not a number: %v", resetConfig)
			}
			if got := int(rawMonth); got != quarterStartMonth {
				t.Errorf("quarter_start_month = %d, want %d", got, quarterStartMonth)
			}

			rawReset, ok := budget["last_reset"].(string)
			if !ok {
				t.Fatalf("last_reset missing or not a string: %v", budget)
			}
			lastReset, err := time.Parse(time.RFC3339, rawReset)
			if err != nil {
				t.Fatalf("could not parse last_reset %q: %v", budget["last_reset"], err)
			}
			want := expectedQuarterStart(time.Now(), quarterStartMonth)
			if !lastReset.UTC().Equal(want) {
				t.Errorf("last_reset = %s, want the fiscal quarter boundary %s",
					lastReset.UTC().Format(time.RFC3339), want.Format(time.RFC3339))
			}
			if lastReset.UTC().Day() != 1 {
				t.Errorf("a quarter boundary must be the 1st, got %s", lastReset.UTC().Format(time.RFC3339))
			}
		})
	}
}

// TestVKQuarterlyBudgetConvergesWhenFiscalQuarterChanges verifies that editing
// the fiscal calendar actually takes effect on a live budget.
//
// The config write deliberately does not move last_reset: UpdateBudget carries
// usage and the reset boundary forward on every configuration write so a
// config.json force-sync cannot replay stale values over live accounting. The
// change lands anyway, because WindowStart reads the new quarter start
// immediately, so a budget whose boundary moved forward reads as due and the
// reset worker stamps the new boundary on its next tick.
//
// That worker runs on a ten second interval, hence the polling below. An
// assertion made immediately after the PUT would be reading the old boundary and
// would be testing the wrong thing.
func TestVKQuarterlyBudgetConvergesWhenFiscalQuarterChanges(t *testing.T) {
	// Deliberately not parallel. This test issues a PUT and then polls for
	// several seconds, and on the default SQLite store that write contends with
	// the other governance tests' creates and deletes hard enough to surface as
	// "database is locked". Running it serially costs a couple of seconds and
	// removes a flake that has nothing to do with what is being verified.
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Pick two fiscal starts that disagree today, so the edit is guaranteed to
	// move the boundary rather than coincidentally landing on the same date.
	// Quarter boundaries repeat every three months, so a pair one month apart
	// always differs.
	now := time.Now().UTC()
	fromMonth, toMonth := 1, 2
	if expectedQuarterStart(now, fromMonth).Equal(expectedQuarterStart(now, toMonth)) {
		t.Fatalf("test setup is degenerate: quarter starts %d and %d coincide on %s", fromMonth, toMonth, now.Format(time.RFC3339))
	}

	createResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:            "test-vk-quarterly-converge-" + generateRandomID(),
			ProviderConfigs: defaultProviderConfigs(),
			CalendarAligned: true,
			Budgets: []BudgetRequest{{
				MaxLimit:      100,
				ResetDuration: "1Q",
				ResetConfig:   &BudgetResetConfigRequest{QuarterStartMonth: fromMonth},
			}},
		},
	})
	if createResp.StatusCode != 200 {
		t.Fatalf("Failed to create quarterly VK: status %d, body %v", createResp.StatusCode, createResp.Body)
	}
	vkID := ExtractIDFromResponse(t, createResp)
	testData.AddVirtualKey(vkID)

	updateResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/virtual-keys/" + vkID,
		Body: UpdateVirtualKeyRequest{
			Budgets: []BudgetRequest{{
				MaxLimit:      100,
				ResetDuration: "1Q",
				ResetConfig:   &BudgetResetConfigRequest{QuarterStartMonth: toMonth},
			}},
		},
	})
	if updateResp.StatusCode != 200 {
		t.Fatalf("Failed to change the fiscal quarter: status %d, body %v", updateResp.StatusCode, updateResp.Body)
	}

	// The definition itself is config and lands synchronously.
	budget := budgetFromVK(t, vkID)
	resetConfig, ok := budget["reset_config"].(map[string]interface{})
	if !ok {
		t.Fatalf("quarter definition missing after the update: %v", budget)
	}
	rawMonth, ok := resetConfig["quarter_start_month"].(float64)
	if !ok {
		t.Fatalf("quarter_start_month missing or not a number after the update: %v", resetConfig)
	}
	if got := int(rawMonth); got != toMonth {
		t.Fatalf("quarter_start_month = %d, want %d immediately after the edit", got, toMonth)
	}

	// The boundary follows on the next reset tick.
	want := expectedQuarterStart(time.Now(), toMonth)
	deadline := time.Now().Add(45 * time.Second)
	var lastSeen time.Time
	for {
		budget = budgetFromVK(t, vkID)
		rawReset, ok := budget["last_reset"].(string)
		if !ok {
			t.Fatalf("last_reset missing or not a string: %v", budget)
		}
		parsed, err := time.Parse(time.RFC3339, rawReset)
		if err != nil {
			t.Fatalf("could not parse last_reset %q: %v", rawReset, err)
		}
		lastSeen = parsed.UTC()
		if lastSeen.Equal(want) {
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	t.Errorf("budget did not converge on the new fiscal boundary: last_reset = %s, want %s",
		lastSeen.Format(time.RFC3339), want.Format(time.RFC3339))
}

// TestVKQuarterlyBudgetRejectsInvalidResetConfig verifies the API refuses a
// quarter definition that cannot mean anything, rather than storing a setting
// that silently does nothing.
func TestVKQuarterlyBudgetRejectsInvalidResetConfig(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		resetDuration string
		quarterStart  int
	}{
		{name: "monthly budget with a quarter definition", resetDuration: "1M", quarterStart: 4},
		{name: "hourly budget with a quarter definition", resetDuration: "1h", quarterStart: 4},
		{name: "month above december", resetDuration: "1Q", quarterStart: 13},
		{name: "negative month", resetDuration: "1Q", quarterStart: -1},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resp := MakeRequest(t, APIRequest{
				Method: "POST",
				Path:   "/api/governance/virtual-keys",
				Body: CreateVirtualKeyRequest{
					Name:            "test-vk-quarterly-invalid-" + generateRandomID(),
					ProviderConfigs: defaultProviderConfigs(),
					Budgets: []BudgetRequest{{
						MaxLimit:      100,
						ResetDuration: testCase.resetDuration,
						ResetConfig:   &BudgetResetConfigRequest{QuarterStartMonth: testCase.quarterStart},
					}},
				},
			})
			if resp.StatusCode < 400 {
				// Clean up the key the server should not have created. The ID is read
				// straight from the body rather than through ExtractIDFromResponse,
				// which calls t.Fatalf when it finds nothing - that would abort here
				// with "could not extract ID" instead of the rejection message this
				// test exists to report, and leak the key it was about to delete.
				if vk, ok := resp.Body["virtual_key"].(map[string]interface{}); ok {
					if vkID, ok := vk["id"].(string); ok && vkID != "" {
						MakeRequest(t, APIRequest{Method: "DELETE", Path: "/api/governance/virtual-keys/" + vkID})
					}
				}
				t.Fatalf("expected the request to be rejected, got status %d", resp.StatusCode)
			}
		})
	}
}

// vkBudgetUsage reads the first budget's current usage off a virtual key's live
// in-memory governance state, which is what actually enforces spend.
func vkBudgetUsage(t *testing.T, vkID string) float64 {
	t.Helper()
	budget := budgetFromVK(t, vkID)
	usage, ok := budget["current_usage"].(float64)
	if !ok {
		t.Fatalf("budget for VK %s has no current_usage: %v", vkID, budget)
	}
	return usage
}

// spendOnVirtualKey makes one small real completion so the budget has usage to
// clear, and returns the usage the gateway recorded.
//
// One short request is enough: the assertions below care that usage is non-zero
// and then zero, not about the amount, so there is no reason to spend more.
func spendOnVirtualKey(t *testing.T, vkID, vkValue string) float64 {
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
		t.Fatalf("failed to spend against VK %s: status %d, body %v", vkID, resp.StatusCode, resp.Body)
	}

	// Usage is recorded asynchronously relative to the response, so poll briefly.
	deadline := time.Now().Add(20 * time.Second)
	for {
		if usage := vkBudgetUsage(t, vkID); usage > 0 {
			return usage
		}
		if time.Now().After(deadline) {
			t.Fatalf("VK %s recorded no budget usage after a successful completion", vkID)
		}
		time.Sleep(time.Second)
	}
}

// TestVKResetBudgetUsageClearsSpend is the full round trip for the operator's
// Reset choice: HTTP request, through the database, into the in-memory store that
// enforcement reads.
//
// This is the only layer that can catch the bug it guards. reset_budget_usage was
// accepted by the API, documented, and sent by the UI for a long time while no
// handler read it, and the value would have been discarded by UpdateBudget even if
// one had. Both failures are invisible to a unit test with a mocked store.
//
// Not parallel: it issues writes and polls, which contends with the rest of the
// suite on the default SQLite store.
func TestVKResetBudgetUsageClearsSpend(t *testing.T) {
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	createResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:            "test-vk-reset-usage-" + generateRandomID(),
			ProviderConfigs: defaultProviderConfigs(),
			Budgets:         []BudgetRequest{{MaxLimit: 50, ResetDuration: "1M"}},
		},
	})
	if createResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d, body %v", createResp.StatusCode, createResp.Body)
	}
	vkID := ExtractIDFromResponse(t, createResp)
	testData.AddVirtualKey(vkID)
	vkValue := createResp.Body["virtual_key"].(map[string]interface{})["value"].(string)

	spent := spendOnVirtualKey(t, vkID, vkValue)
	t.Logf("recorded $%.6f of usage before the reset", spent)

	before := budgetFromVK(t, vkID)
	lastResetBefore, err := time.Parse(time.RFC3339, before["last_reset"].(string))
	if err != nil {
		t.Fatalf("could not parse last_reset %q: %v", before["last_reset"], err)
	}

	// A budget edit WITHOUT the flag must leave the spend alone. Asserting this
	// first matters: without it, a handler that always reset would still pass the
	// positive case below and silently wipe usage on every unrelated edit.
	preserveResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/virtual-keys/" + vkID,
		Body: UpdateVirtualKeyRequest{
			Budgets: []BudgetRequest{{MaxLimit: 60, ResetDuration: "1M"}},
		},
	})
	if preserveResp.StatusCode != 200 {
		t.Fatalf("Failed to update VK: status %d, body %v", preserveResp.StatusCode, preserveResp.Body)
	}
	if usage := vkBudgetUsage(t, vkID); usage != spent {
		t.Errorf("an edit without reset_budget_usage changed usage from %v to %v; spend must be preserved", spent, usage)
	}

	// Now with the flag.
	resetUsage := true
	resetResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/virtual-keys/" + vkID,
		Body: UpdateVirtualKeyRequest{
			Budgets:          []BudgetRequest{{MaxLimit: 70, ResetDuration: "1M"}},
			ResetBudgetUsage: &resetUsage,
		},
	})
	if resetResp.StatusCode != 200 {
		t.Fatalf("Failed to reset budget usage: status %d, body %v", resetResp.StatusCode, resetResp.Body)
	}

	if usage := vkBudgetUsage(t, vkID); usage != 0 {
		t.Errorf("reset_budget_usage did not clear spend: usage is still %v", usage)
	}

	// The reset clears usage only. Moving the boundary is ruled out by the
	// forward-only invariant that keeps cluster nodes agreeing on the open window.
	after := budgetFromVK(t, vkID)
	lastResetAfter, err := time.Parse(time.RFC3339, after["last_reset"].(string))
	if err != nil {
		t.Fatalf("could not parse last_reset %q: %v", after["last_reset"], err)
	}
	if !lastResetAfter.Equal(lastResetBefore) {
		t.Errorf("resetting usage moved last_reset from %s to %s; the window must be left alone",
			lastResetBefore.Format(time.RFC3339), lastResetAfter.Format(time.RFC3339))
	}
}

// TestVKResetBudgetUsageRestoresCapacity answers the question a usage counter
// alone cannot: does clearing spend actually let requests through again.
//
// Zeroing current_usage is only meaningful if enforcement follows it. The
// enforcement path reads the in-memory governance store, not the database, so a
// reset that reached the database but not memory would show usage at zero on
// every read while requests kept getting rejected. This drives the budget to
// exhaustion, confirms rejection, resets, and confirms the next request succeeds.
//
// Not parallel: it writes and polls, contending with the rest of the suite on
// the default SQLite store.
func TestVKResetBudgetUsageRestoresCapacity(t *testing.T) {
	testData := NewGlobalTestData()
	defer testData.Cleanup(t)

	// Small enough that a single completion exhausts it, so the test costs one
	// extra request rather than a loop.
	createResp := MakeRequest(t, APIRequest{
		Method: "POST",
		Path:   "/api/governance/virtual-keys",
		Body: CreateVirtualKeyRequest{
			Name:            "test-vk-reset-capacity-" + generateRandomID(),
			ProviderConfigs: defaultProviderConfigs(),
			Budgets:         []BudgetRequest{{MaxLimit: 0.00001, ResetDuration: "1M"}},
		},
	})
	if createResp.StatusCode != 200 {
		t.Fatalf("Failed to create VK: status %d, body %v", createResp.StatusCode, createResp.Body)
	}
	vkID := ExtractIDFromResponse(t, createResp)
	testData.AddVirtualKey(vkID)
	vkValue := createResp.Body["virtual_key"].(map[string]interface{})["value"].(string)

	completion := func() *APIResponse {
		return MakeRequest(t, APIRequest{
			Method: "POST",
			Path:   "/v1/chat/completions",
			Body: ChatCompletionRequest{
				Model:    "openai/gpt-4o",
				Messages: []ChatMessage{{Role: "user", Content: "Reply with the single word: ok"}},
			},
			VKHeader: &vkValue,
		})
	}

	// The first request is admitted because usage starts at zero, and its cost
	// pushes usage past the cap.
	if resp := completion(); resp.StatusCode >= 400 {
		t.Fatalf("first request should be admitted on an unused budget: status %d, body %v", resp.StatusCode, resp.Body)
	}

	// Usage is recorded asynchronously, so wait for enforcement to start rejecting.
	deadline := time.Now().Add(30 * time.Second)
	var blocked *APIResponse
	for {
		resp := completion()
		if resp.StatusCode >= 400 {
			blocked = resp
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("budget of $0.00001 never blocked a request; usage is %v", vkBudgetUsage(t, vkID))
		}
		time.Sleep(time.Second)
	}
	if !CheckErrorMessage(t, blocked, "budget") {
		t.Fatalf("request was rejected for a reason other than budget: %v", blocked.Body)
	}
	t.Logf("budget exhausted as expected at $%.8f of usage", vkBudgetUsage(t, vkID))

	// Reset usage and raise the cap, so the next request is admitted only if the
	// reset actually reached the store that enforces spend.
	resetUsage := true
	resetResp := MakeRequest(t, APIRequest{
		Method: "PUT",
		Path:   "/api/governance/virtual-keys/" + vkID,
		Body: UpdateVirtualKeyRequest{
			Budgets:          []BudgetRequest{{MaxLimit: 5, ResetDuration: "1M"}},
			ResetBudgetUsage: &resetUsage,
		},
	})
	if resetResp.StatusCode != 200 {
		t.Fatalf("Failed to reset budget usage: status %d, body %v", resetResp.StatusCode, resetResp.Body)
	}
	if usage := vkBudgetUsage(t, vkID); usage != 0 {
		t.Fatalf("usage was not cleared: %v", usage)
	}

	if resp := completion(); resp.StatusCode >= 400 {
		t.Errorf("request still rejected after the usage reset, so enforcement did not see it: status %d, body %v",
			resp.StatusCode, resp.Body)
	}
}
