package governance

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLocalGovernanceStore_GetGovernanceUsageDataExcludesUnrelatedState checks that the usage snapshot holds budgets and rate limits and nothing else.
func TestLocalGovernanceStore_GetGovernanceUsageDataExcludesUnrelatedState(t *testing.T) {
	logger := NewMockLogger()
	// The live store loads only the limits something holds, so the key holds both.
	vk := buildVirtualKey("vk1", "sk-bf-test1", "Test VK 1", true)
	vk.Budgets = []configstoreTables.TableBudget{{ID: "budget1", ResetDuration: "1d"}}
	vk.RateLimit = &configstoreTables.TableRateLimit{ID: "rate-limit1"}
	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	data := store.GetGovernanceUsageData(context.Background())
	require.NotNil(t, data)
	assert.Contains(t, data.Budgets, "budget1")
	assert.Contains(t, data.RateLimits, "rate-limit1")
	assert.Nil(t, data.VirtualKeys)
	assert.Nil(t, data.Teams)
	assert.Nil(t, data.Customers)
	assert.Nil(t, data.Users)
	assert.Nil(t, data.ModelConfigs)
	assert.Nil(t, data.Providers)
}

// TestGovernanceStore_GetVirtualKey tests lock-free VK retrieval
func TestGovernanceStore_GetVirtualKey(t *testing.T) {
	logger := NewMockLogger()
	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{
			*buildVirtualKey("vk1", "sk-bf-test1", "Test VK 1", true),
			*buildVirtualKey("vk2", "sk-bf-test2", "Test VK 2", false),
		},
	}, nil, nil)
	require.NoError(t, err)

	tests := []struct {
		name    string
		vkValue string
		wantNil bool
		wantID  string
	}{
		{
			name:    "Found active VK",
			vkValue: "sk-bf-test1",
			wantNil: false,
			wantID:  "vk1",
		},
		{
			name:    "Found inactive VK",
			vkValue: "sk-bf-test2",
			wantNil: false,
			wantID:  "vk2",
		},
		{
			name:    "VK not found",
			vkValue: "sk-bf-nonexistent",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vk, exists := store.GetVirtualKey(context.Background(), tt.vkValue)
			if tt.wantNil {
				assert.False(t, exists)
				assert.Nil(t, vk)
			} else {
				assert.True(t, exists)
				assert.NotNil(t, vk)
				assert.Equal(t, tt.wantID, vk.ID)
			}
		})
	}
}

// TestGovernanceStore_ConcurrentReads tests lock-free concurrent reads
func TestGovernanceStore_ConcurrentReads(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-test", "Test VK", true)
	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	// Launch 100 concurrent readers
	var wg sync.WaitGroup
	readCount := atomic.Int64{}
	errorCount := atomic.Int64{}

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				vk, exists := store.GetVirtualKey(context.Background(), "sk-bf-test")
				if !exists || vk == nil {
					errorCount.Add(1)
					return
				}
				readCount.Add(1)
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, int64(10000), readCount.Load(), "Expected 10000 successful reads")
	assert.Equal(t, int64(0), errorCount.Load(), "Expected 0 errors")
}

// TestGovernanceStore_CheckBudget_SingleBudget tests budget validation with single budget
func TestGovernanceStore_CheckBudget_SingleBudget(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("budget1", 100.0, 50.0, "1d")
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", budget)

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	// Retrieve VK with budget
	_, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")

	tests := []struct {
		name      string
		usage     float64
		maxLimit  float64
		shouldErr bool
	}{
		{
			name:      "Usage below limit",
			usage:     50.0,
			maxLimit:  100.0,
			shouldErr: false,
		},
		{
			name:      "Usage at limit (should fail)",
			usage:     100.0,
			maxLimit:  100.0,
			shouldErr: true,
		},
		{
			name:      "Usage exceeds limit",
			usage:     150.0,
			maxLimit:  100.0,
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create new budget with test usage
			testBudget := buildBudgetWithUsage("budget1", tt.maxLimit, tt.usage, "1d")
			testVK := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", testBudget)
			testStore, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
				VirtualKeys: []configstoreTables.TableVirtualKey{*testVK},
				Budgets:     []configstoreTables.TableBudget{*testBudget},
			}, nil, nil)
			require.NoError(t, err)

			testRec, ok := testStore.GetVirtualKey(context.Background(), "sk-bf-test")
			require.True(t, ok, "fixture check")
			_, err = checkGrantBudgets(testStore, emptyCtx(), testRec, schemas.OpenAI, "", nil)
			if tt.shouldErr {
				assert.Error(t, err, "Expected error for usage check")
			} else {
				assert.NoError(t, err, "Expected no error for usage check")
			}
		})
	}
}

// TestGovernanceStoreCheckBudgetUsesOverride verifies enforcement reads the additive effective limit.
func TestGovernanceStoreCheckBudgetUsesOverride(t *testing.T) {
	store, err := newTestGovernanceStore(t, NewMockLogger(), &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)
	budget := buildBudgetWithUsage("override-budget", 100, 110, "1d")
	require.NoError(t, budget.SetOverride(25, configstoreTables.BudgetOverrideModeCycles, 2))

	decision, err := store.CheckBudget(context.Background(), EntityWiseBudgets{"VirtualKey": {budget}}, nil)
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision)

	decision, err = store.CheckBudget(context.Background(), EntityWiseBudgets{"VirtualKey": {budget}}, map[string]float64{budget.ID: 15})
	require.Error(t, err)
	assert.Equal(t, DecisionBudgetExceeded, decision)
	assert.Contains(t, err.Error(), "125.0000 dollars")
}

// TestGovernanceStore_CheckBudget_HierarchyValidation tests multi-level budget hierarchy
func TestGovernanceStore_CheckBudget_HierarchyValidation(t *testing.T) {
	logger := NewMockLogger()

	// Create budgets at different levels
	vkBudget := buildBudgetWithUsage("vk-budget", 100.0, 50.0, "1d")
	teamBudget := buildBudgetWithUsage("team-budget", 500.0, 200.0, "1d")
	customerBudget := buildBudgetWithUsage("customer-budget", 1000.0, 400.0, "1d")

	// Build hierarchy
	team := buildTeam("team1", "Team 1", teamBudget)
	customer := buildCustomer("customer1", "Customer 1", customerBudget)
	team.CustomerID = &customer.ID
	team.Customer = customer

	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", vkBudget)
	vk.TeamID = &team.ID
	vk.Team = team

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*vkBudget, *teamBudget, *customerBudget},
		Teams:       []configstoreTables.TableTeam{*team},
		Customers:   []configstoreTables.TableCustomer{*customer},
	}, nil, nil)
	require.NoError(t, err)

	rec, _ := store.GetVirtualKey(context.Background(), "sk-bf-test")

	// Test: All budgets under limit should pass
	_, err = checkGrantBudgets(store, emptyCtx(), rec, schemas.OpenAI, "", nil)
	assert.NoError(t, err, "Should pass when all budgets are under limit")

	// Test: If VK budget exceeds limit, should fail
	// Update the budget's usage directly (since a key refresh preserves usage)
	if len(rec.BudgetIDs) > 0 {
		budgetID := rec.BudgetIDs[0]
		if budget := store.LoadBudget(context.Background(), budgetID); budget != nil {
			budget.CurrentUsage = 100.0
			putBudget(t, store, budgetID, budget)
		}
	}
	_, err = checkGrantBudgets(store, emptyCtx(), rec, schemas.OpenAI, "", nil)
	require.Error(t, err, "Should fail when VK budget exceeds limit")
}

// TestGovernanceStore_MultiBudget_AllUnderLimit tests that requests pass when all budgets are under their limits
func TestGovernanceStore_MultiBudget_AllUnderLimit(t *testing.T) {
	logger := NewMockLogger()

	// Create VK with hourly ($10) and daily ($100) budgets
	hourlyBudget := buildBudgetWithUsage("hourly", 10.0, 5.0, "1h")
	dailyBudget := buildBudgetWithUsage("daily", 100.0, 40.0, "1d")

	vk := buildVirtualKeyWithMultiBudgets("vk1", "sk-bf-test", "Test VK",
		[]configstoreTables.TableBudget{*hourlyBudget, *dailyBudget})
	// Add provider config so the resolver allows the provider
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*hourlyBudget, *dailyBudget},
	}, nil, nil)
	require.NoError(t, err)

	rec, _ := store.GetVirtualKey(context.Background(), "sk-bf-test")
	_, err = checkGrantBudgets(store, emptyCtx(), rec, schemas.OpenAI, "", nil)
	assert.NoError(t, err, "Should pass when all budgets are under limit")
}

// TestGovernanceStore_MultiBudget_SmallBudgetExceeded tests that request is blocked when the smaller budget exceeds its limit
func TestGovernanceStore_MultiBudget_SmallBudgetExceeded(t *testing.T) {
	logger := NewMockLogger()

	// Hourly at limit, daily still has room
	hourlyBudget := buildBudgetWithUsage("hourly", 10.0, 10.0, "1h")
	dailyBudget := buildBudgetWithUsage("daily", 100.0, 40.0, "1d")

	vk := buildVirtualKeyWithMultiBudgets("vk1", "sk-bf-test", "Test VK",
		[]configstoreTables.TableBudget{*hourlyBudget, *dailyBudget})
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*hourlyBudget, *dailyBudget},
	}, nil, nil)
	require.NoError(t, err)

	rec, _ := store.GetVirtualKey(context.Background(), "sk-bf-test")
	_, err = checkGrantBudgets(store, emptyCtx(), rec, schemas.OpenAI, "", nil)
	require.Error(t, err, "Should fail when hourly budget is exceeded even though daily is fine")
	assert.Contains(t, err.Error(), "budget exceeded")
}

// TestGovernanceStore_MultiBudget_LargeBudgetExceeded tests that request is blocked when only the larger budget exceeds
func TestGovernanceStore_MultiBudget_LargeBudgetExceeded(t *testing.T) {
	logger := NewMockLogger()

	// Hourly has room, but daily is at limit
	hourlyBudget := buildBudgetWithUsage("hourly", 10.0, 3.0, "1h")
	dailyBudget := buildBudgetWithUsage("daily", 100.0, 100.0, "1d")

	vk := buildVirtualKeyWithMultiBudgets("vk1", "sk-bf-test", "Test VK",
		[]configstoreTables.TableBudget{*hourlyBudget, *dailyBudget})
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*hourlyBudget, *dailyBudget},
	}, nil, nil)
	require.NoError(t, err)

	rec, _ := store.GetVirtualKey(context.Background(), "sk-bf-test")
	_, err = checkGrantBudgets(store, emptyCtx(), rec, schemas.OpenAI, "", nil)
	require.Error(t, err, "Should fail when daily budget is exceeded even though hourly is fine")
	assert.Contains(t, err.Error(), "budget exceeded")
}

// TestGovernanceStore_MultiBudget_UsageUpdatesAllBudgets tests that usage updates are applied to every budget in the hierarchy
func TestGovernanceStore_MultiBudget_UsageUpdatesAllBudgets(t *testing.T) {
	logger := NewMockLogger()

	hourlyBudget := buildBudget("hourly", 10.0, "1h")
	dailyBudget := buildBudget("daily", 100.0, "1d")

	vk := buildVirtualKeyWithMultiBudgets("vk1", "sk-bf-test", "Test VK",
		[]configstoreTables.TableBudget{*hourlyBudget, *dailyBudget})
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*hourlyBudget, *dailyBudget},
	}, nil, nil)
	require.NoError(t, err)

	rec, _ := store.GetVirtualKey(context.Background(), "sk-bf-test")

	// Simulate a $3.50 request
	err = chargeGrantBudgets(store, context.Background(), rec, schemas.OpenAI, 3.50)
	require.NoError(t, err)

	// Both budgets should reflect the cost
	hourlyVal := store.LoadBudget(context.Background(), "hourly")
	require.NotNil(t, hourlyVal)
	assert.InDelta(t, 3.50, hourlyVal.CurrentUsage, 0.01, "Hourly budget should reflect usage")

	dailyVal := store.LoadBudget(context.Background(), "daily")
	require.NotNil(t, dailyVal)
	assert.InDelta(t, 3.50, dailyVal.CurrentUsage, 0.01, "Daily budget should reflect usage")

	// Second request: $7.00 — should push hourly over limit
	err = chargeGrantBudgets(store, context.Background(), rec, schemas.OpenAI, 7.00)
	require.NoError(t, err)

	hourlyVal = store.LoadBudget(context.Background(), "hourly")
	assert.InDelta(t, 10.50, hourlyVal.CurrentUsage, 0.01, "Hourly budget should accumulate")

	dailyVal = store.LoadBudget(context.Background(), "daily")
	assert.InDelta(t, 10.50, dailyVal.CurrentUsage, 0.01, "Daily budget should accumulate")

	// Now CheckBudget should fail (hourly exceeded)
	_, err = checkGrantBudgets(store, emptyCtx(), rec, schemas.OpenAI, "", nil)
	require.Error(t, err, "Should fail after usage exceeds hourly budget")
	assert.Contains(t, err.Error(), "budget exceeded")
}

// TestGovernanceStore_MultiBudget_ProviderConfigBudgets tests that provider-config-level multi-budgets are enforced
func TestGovernanceStore_MultiBudget_ProviderConfigBudgets(t *testing.T) {
	logger := NewMockLogger()

	// Provider-level budgets: hourly $5 (exceeded), daily $50 (ok)
	pcHourly := buildBudgetWithUsage("pc-hourly", 5.0, 5.0, "1h")
	pcDaily := buildBudgetWithUsage("pc-daily", 50.0, 10.0, "1d")

	pc := buildProviderConfigWithBudgets("openai", []string{"*"},
		[]configstoreTables.TableBudget{*pcHourly, *pcDaily})

	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK",
		[]configstoreTables.TableVirtualKeyProviderConfig{pc})

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*pcHourly, *pcDaily},
	}, nil, nil)
	require.NoError(t, err)

	rec, _ := store.GetVirtualKey(context.Background(), "sk-bf-test")
	_, err = checkGrantBudgets(store, emptyCtx(), rec, schemas.OpenAI, "", nil)
	require.Error(t, err, "Should fail when provider config hourly budget is exceeded")
	assert.Contains(t, err.Error(), "budget exceeded")
}

// TestGovernanceStore_MultiBudget_VKAndProviderConfigCombined tests budgets at both VK and provider config levels
func TestGovernanceStore_MultiBudget_VKAndProviderConfigCombined(t *testing.T) {
	logger := NewMockLogger()

	// VK-level budgets: all under limit
	vkMonthly := buildBudgetWithUsage("vk-monthly", 1000.0, 200.0, "1M")

	// Provider-config-level budgets: hourly at limit
	pcHourly := buildBudgetWithUsage("pc-hourly", 5.0, 5.0, "1h")

	pc := buildProviderConfigWithBudgets("openai", []string{"*"},
		[]configstoreTables.TableBudget{*pcHourly})

	vk := buildVirtualKeyWithMultiBudgets("vk1", "sk-bf-test", "Test VK",
		[]configstoreTables.TableBudget{*vkMonthly})
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{pc}

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*vkMonthly, *pcHourly},
	}, nil, nil)
	require.NoError(t, err)

	rec, _ := store.GetVirtualKey(context.Background(), "sk-bf-test")

	// Provider config budget exceeded → should block even though VK budget is fine
	_, err = checkGrantBudgets(store, emptyCtx(), rec, schemas.OpenAI, "", nil)
	require.Error(t, err, "Should fail: provider config budget exceeded even though VK budget is fine")
	assert.Contains(t, err.Error(), "budget exceeded")
}

// TestGovernanceStore_MultiBudget_ResolverBlocksOnBudgetExceeded tests that the full resolver flow blocks when any budget is exceeded
func TestGovernanceStore_MultiBudget_ResolverBlocksOnBudgetExceeded(t *testing.T) {
	logger := NewMockLogger()

	// Two VK-level budgets: hourly at limit, daily has room
	hourlyBudget := buildBudgetWithUsage("hourly", 10.0, 10.0, "1h")
	dailyBudget := buildBudgetWithUsage("daily", 100.0, 30.0, "1d")

	vk := buildVirtualKeyWithMultiBudgets("vk1", "sk-bf-test", "Test VK",
		[]configstoreTables.TableBudget{*hourlyBudget, *dailyBudget})
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*hourlyBudget, *dailyBudget},
	}, nil, nil)
	require.NoError(t, err)

	resolver := budgetResolverForTest(t, store, nil, logger)
	ctx := resolverCtx(store, "sk-bf-test")

	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)
	assertDecision(t, DecisionBudgetExceeded, result)
	assert.Contains(t, result.Reason, "budget exceeded")
}

// TestGovernanceStore_MultiBudget_ResolverAllowsUnderLimit tests that the full resolver flow allows requests when all budgets are under limit
func TestGovernanceStore_MultiBudget_ResolverAllowsUnderLimit(t *testing.T) {
	logger := NewMockLogger()

	hourlyBudget := buildBudgetWithUsage("hourly", 10.0, 5.0, "1h")
	dailyBudget := buildBudgetWithUsage("daily", 100.0, 30.0, "1d")

	vk := buildVirtualKeyWithMultiBudgets("vk1", "sk-bf-test", "Test VK",
		[]configstoreTables.TableBudget{*hourlyBudget, *dailyBudget})
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*hourlyBudget, *dailyBudget},
	}, nil, nil)
	require.NoError(t, err)

	resolver := budgetResolverForTest(t, store, nil, logger)
	ctx := resolverCtx(store, "sk-bf-test")

	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)
	assertDecision(t, DecisionAllow, result)
}

// TestGovernanceStore_MultiBudget_UsageDrivesBlockAfterRequests tests the full lifecycle:
// start under limit → accumulate usage → eventually hit a budget → get blocked
func TestGovernanceStore_MultiBudget_UsageDrivesBlockAfterRequests(t *testing.T) {
	logger := NewMockLogger()

	// Tight hourly ($2), generous daily ($100)
	hourlyBudget := buildBudget("hourly", 2.0, "1h")
	dailyBudget := buildBudget("daily", 100.0, "1d")

	vk := buildVirtualKeyWithMultiBudgets("vk1", "sk-bf-test", "Test VK",
		[]configstoreTables.TableBudget{*hourlyBudget, *dailyBudget})
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*hourlyBudget, *dailyBudget},
	}, nil, nil)
	require.NoError(t, err)

	resolver := budgetResolverForTest(t, store, nil, logger)

	// Request 1: $0.80 — both budgets fine
	rec, _ := store.GetVirtualKey(context.Background(), "sk-bf-test")
	err = chargeGrantBudgets(store, context.Background(), rec, schemas.OpenAI, 0.80)
	require.NoError(t, err)

	ctx := resolverCtx(store, "sk-bf-test")
	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)
	assertDecision(t, DecisionAllow, result)

	// Request 2: $0.80 — still fine ($1.60 total)
	rec, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	err = chargeGrantBudgets(store, context.Background(), rec, schemas.OpenAI, 0.80)
	require.NoError(t, err)

	ctx = resolverCtx(store, "sk-bf-test")
	result = evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)
	assertDecision(t, DecisionAllow, result)

	// Request 3: $0.80 — pushes hourly to $2.40 > $2.00 limit → blocked
	rec, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	err = chargeGrantBudgets(store, context.Background(), rec, schemas.OpenAI, 0.80)
	require.NoError(t, err)

	ctx = resolverCtx(store, "sk-bf-test")
	result = evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)
	assertDecision(t, DecisionBudgetExceeded, result)
	assert.Contains(t, result.Reason, "budget exceeded")

	// Verify daily budget is still under limit
	dailyVal := store.LoadBudget(context.Background(), "daily")
	require.NotNil(t, dailyVal)
	assert.InDelta(t, 2.40, dailyVal.CurrentUsage, 0.01,
		"Daily budget should be at $2.40, well under $100 limit")
}

// TestGovernanceStore_MultiBudget_CalendarAligned tests that calendar-aligned budgets are stored and retrievable
func TestGovernanceStore_MultiBudget_CalendarAligned(t *testing.T) {
	logger := NewMockLogger()

	// Calendar alignment is a VK-level setting — budgets don't have it
	dailyBudget := &configstoreTables.TableBudget{
		ID:            "daily-cal",
		MaxLimit:      50.0,
		CurrentUsage:  10.0,
		ResetDuration: "1d",
		LastReset:     time.Now(),
	}
	monthlyBudget := &configstoreTables.TableBudget{
		ID:            "monthly-cal",
		MaxLimit:      1000.0,
		CurrentUsage:  200.0,
		ResetDuration: "1M",
		LastReset:     time.Now(),
	}

	vk := buildVirtualKeyWithMultiBudgets("vk1", "sk-bf-test", "Test VK",
		[]configstoreTables.TableBudget{*dailyBudget, *monthlyBudget})
	vk.CalendarAligned = true // VK-level setting applies to all budgets
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*dailyBudget, *monthlyBudget},
	}, nil, nil)
	require.NoError(t, err)

	// Verify VK-level calendar_aligned is set
	rec, _ := store.GetVirtualKey(context.Background(), "sk-bf-test")
	assert.True(t, rec.CalendarAligned, "VK should have calendar_aligned=true")

	// Both under limit — should pass
	_, err = checkGrantBudgets(store, emptyCtx(), rec, schemas.OpenAI, "", nil)
	assert.NoError(t, err)
}

// TestGovernanceStore_TeamCalendarAlignmentSurvivesColdLoad checks that a team's calendar alignment reaches its budgets and rate limit when they are loaded from the database.
// TestGovernanceStore_KeyCalendarAlignmentSurvivesColdLoad checks that a calendar-aligned key's
// budgets and rate limit, and its provider configs', are calendar aligned straight after the store
// loads, so a weekly budget on the calendar boundary is not reset on its rolling window.
func TestGovernanceStore_KeyCalendarAlignmentSurvivesColdLoad(t *testing.T) {
	now := time.Now().UTC()
	weeklyReset := configstoreTables.GetCalendarPeriodStart("1w", now, configstoreTables.QuarterStartNotApplicable)
	keyID := "vk-calendar-aligned"
	keyBudget := configstoreTables.TableBudget{
		ID: "vk-weekly-calendar-aligned", VirtualKeyID: &keyID, MaxLimit: 100, CurrentUsage: 42,
		ResetDuration: "1w", LastReset: weeklyReset, CreatedAt: now.Add(-time.Minute),
	}
	configID := uint(1)
	configBudget := configstoreTables.TableBudget{
		ID: "pc-weekly-calendar-aligned", ProviderConfigID: &configID, MaxLimit: 50, CurrentUsage: 7,
		ResetDuration: "1w", LastReset: weeklyReset, CreatedAt: now.Add(-time.Minute),
	}
	keyRateLimit := buildRateLimit("vk-calendar-aligned-rate-limit", 100, 100)
	configRateLimit := buildRateLimit("pc-calendar-aligned-rate-limit", 100, 100)
	vk := buildVirtualKey(keyID, "sk-bf-calendar-key", "calendar key", true)
	vk.CalendarAligned = true
	vk.Budgets = []configstoreTables.TableBudget{keyBudget}
	vk.RateLimitID = &keyRateLimit.ID
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{{
		ID: configID, VirtualKeyID: keyID, Provider: "openai", AllowedModels: []string{"*"},
		Budgets: []configstoreTables.TableBudget{configBudget}, RateLimitID: &configRateLimit.ID,
	}}

	store, err := newTestGovernanceStore(t, NewMockLogger(), &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{keyBudget, configBudget},
		RateLimits:  []configstoreTables.TableRateLimit{*keyRateLimit, *configRateLimit},
	}, nil, nil)
	require.NoError(t, err)

	for _, id := range []string{keyBudget.ID, configBudget.ID} {
		budget := store.LoadBudget(context.Background(), id)
		require.NotNil(t, budget)
		assert.True(t, budget.IsCalendarAligned, "budget %s follows the key's calendar flag after a cold load", id)
	}
	for _, id := range []string{keyRateLimit.ID, configRateLimit.ID} {
		rateLimit := store.LoadRateLimit(context.Background(), id)
		require.NotNil(t, rateLimit)
		assert.True(t, rateLimit.IsCalendarAligned, "rate limit %s follows the key's calendar flag after a cold load", id)
	}
	assert.Empty(t, store.ResetExpiredBudgetsInMemory(context.Background(), keyBudget.ID, configBudget.ID),
		"a cold-loaded calendar budget must not reset on its creation-time rolling window")
	assert.Equal(t, 42.0, store.LoadBudget(context.Background(), keyBudget.ID).CurrentUsage)
}

// TestGovernanceStore_NestedLimitsLoadWithoutConfigStore checks that a gateway without a config store
// enforces limits written only under their holder in config.json: a key's and its provider config's
// budgets, the rate limits they name, a provider's budget and rate limit, and a customer's rate limit.
func TestGovernanceStore_NestedLimitsLoadWithoutConfigStore(t *testing.T) {
	keyID := "vk-nested"
	keyRateLimit := buildRateLimit("vk-nested-rate-limit", 100, 100)
	configRateLimit := buildRateLimit("pc-nested-rate-limit", 100, 100)
	providerRateLimit := buildRateLimit("provider-nested-rate-limit", 100, 100)
	providerBudget := buildBudget("provider-nested-budget", 30, "1d")
	vk := buildVirtualKey(keyID, "sk-bf-nested-key", "nested key", true)
	vk.CalendarAligned = true
	vk.Budgets = []configstoreTables.TableBudget{*buildBudget("vk-nested-budget", 10, "1d")}
	vk.RateLimitID, vk.RateLimit = &keyRateLimit.ID, keyRateLimit
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{{
		ID: 1, VirtualKeyID: keyID, Provider: "openai", AllowedModels: []string{"*"},
		Budgets:     []configstoreTables.TableBudget{*buildBudget("pc-nested-budget", 20, "1d")},
		RateLimitID: &configRateLimit.ID, RateLimit: configRateLimit,
	}}

	customerRateLimit := buildRateLimit("customer-nested-rate-limit", 100, 100)
	store, err := newTestGovernanceStore(t, NewMockLogger(), &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Customers: []configstoreTables.TableCustomer{{
			ID: "customer-nested", Name: "nested customer", CalendarAligned: true,
			RateLimitID: &customerRateLimit.ID, RateLimit: customerRateLimit,
		}},
		Providers: []configstoreTables.TableProvider{{
			Name: "openai", BudgetID: &providerBudget.ID, Budget: providerBudget,
			RateLimitID: &providerRateLimit.ID, RateLimit: providerRateLimit,
		}},
	}, nil, nil)
	require.NoError(t, err)

	for id, keyHeld := range map[string]bool{"vk-nested-budget": true, "pc-nested-budget": true, providerBudget.ID: false} {
		budget := store.LoadBudget(context.Background(), id)
		require.NotNil(t, budget, "budget %s written under its holder is loaded", id)
		assert.Equal(t, keyHeld, budget.IsCalendarAligned, "budget %s takes its holder's calendar flag", id)
	}
	for id, keyHeld := range map[string]bool{keyRateLimit.ID: true, configRateLimit.ID: true, providerRateLimit.ID: false, customerRateLimit.ID: true} {
		rateLimit := store.LoadRateLimit(context.Background(), id)
		require.NotNil(t, rateLimit, "rate limit %s written under its holder is loaded", id)
		assert.Equal(t, keyHeld, rateLimit.IsCalendarAligned, "rate limit %s takes its holder's calendar flag", id)
	}
}

func TestGovernanceStore_TeamCalendarAlignmentSurvivesColdLoad(t *testing.T) {
	logger := NewMockLogger()
	now := time.Now().UTC()
	lastReset := configstoreTables.GetCalendarPeriodStart("1w", now, configstoreTables.QuarterStartNotApplicable)
	createdAt := now.Add(-time.Minute)
	teamID := "team-calendar-aligned"
	weeklyBudget := configstoreTables.TableBudget{
		ID:            "team-weekly-calendar-aligned",
		TeamID:        &teamID,
		MaxLimit:      100,
		CurrentUsage:  42,
		ResetDuration: "1w",
		LastReset:     lastReset,
		CreatedAt:     createdAt,
	}
	dailyBudget := configstoreTables.TableBudget{
		ID:                "team-daily-calendar-aligned",
		TeamID:            &teamID,
		MaxLimit:          25,
		ResetDuration:     "1d",
		LastReset:         configstoreTables.GetCalendarPeriodStart("1d", now, configstoreTables.QuarterStartNotApplicable),
		CreatedAt:         createdAt,
		IsCalendarAligned: true,
	}
	rateLimit := buildRateLimit("team-calendar-aligned-rate-limit", 100, 100)
	staleRateLimit := *rateLimit
	staleRateLimit.TokenCurrentUsage = 1
	staleRateLimit.RequestCurrentUsage = 1
	staleRateLimit.TokenLastReset = rateLimit.TokenLastReset.Add(-24 * time.Hour)
	staleRateLimit.RequestLastReset = rateLimit.RequestLastReset.Add(-24 * time.Hour)
	staleDailyReset := dailyBudget.LastReset.Add(-24 * time.Hour)
	staleDailyBudget := dailyBudget
	staleDailyBudget.CurrentUsage = 1
	staleDailyBudget.LastReset = staleDailyReset
	team := configstoreTables.TableTeam{
		ID:              teamID,
		Name:            "Calendar aligned team",
		CalendarAligned: true,
		Budgets:         []configstoreTables.TableBudget{staleDailyBudget},
		RateLimitID:     &rateLimit.ID,
		RateLimit:       &staleRateLimit,
	}

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		Teams:      []configstoreTables.TableTeam{team},
		Budgets:    []configstoreTables.TableBudget{dailyBudget, weeklyBudget},
		RateLimits: []configstoreTables.TableRateLimit{*rateLimit},
	}, nil, nil)
	require.NoError(t, err)

	for _, budget := range []configstoreTables.TableBudget{dailyBudget, weeklyBudget} {
		loadedBudget := store.LoadBudget(context.Background(), budget.ID)
		require.NotNil(t, loadedBudget)
		assert.True(t, loadedBudget.IsCalendarAligned)
	}

	loadedDailyBudget := store.LoadBudget(context.Background(), dailyBudget.ID)
	require.NotNil(t, loadedDailyBudget)
	assert.Equal(t, dailyBudget.CurrentUsage, loadedDailyBudget.CurrentUsage)
	assert.WithinDuration(t, dailyBudget.LastReset, loadedDailyBudget.LastReset, 0)

	loadedWeeklyBudget := store.LoadBudget(context.Background(), weeklyBudget.ID)
	assert.Equal(t, 42.0, loadedWeeklyBudget.CurrentUsage)
	assert.WithinDuration(t, lastReset, loadedWeeklyBudget.LastReset, 0)
	assert.Empty(t, store.ResetExpiredBudgetsInMemory(context.Background(), weeklyBudget.ID),
		"cold-loaded calendar budget must not reset on its creation-time rolling window")

	collectedBudgets := store.CollectTeamBudgets(context.Background(), teamID)
	require.Len(t, collectedBudgets, 2)
	collectedIDs := []string{collectedBudgets[0].ID, collectedBudgets[1].ID}
	assert.ElementsMatch(t, []string{dailyBudget.ID, weeklyBudget.ID}, collectedIDs)

	loadedRateLimit := store.LoadRateLimit(context.Background(), rateLimit.ID)
	require.NotNil(t, loadedRateLimit)
	assert.True(t, loadedRateLimit.IsCalendarAligned)
	assert.Equal(t, rateLimit.TokenCurrentUsage, loadedRateLimit.TokenCurrentUsage)
	assert.Equal(t, rateLimit.RequestCurrentUsage, loadedRateLimit.RequestCurrentUsage)
	assert.WithinDuration(t, rateLimit.TokenLastReset, loadedRateLimit.TokenLastReset, 0)
	assert.WithinDuration(t, rateLimit.RequestLastReset, loadedRateLimit.RequestLastReset, 0)
}

// TestGovernanceStore_MultiBudget_InMemoryCreateAndDelete tests that writing a key and refreshing it,
// then deleting it and removing its record, properly store and clean up multi-budget entries
func TestGovernanceStore_MultiBudget_InMemoryCreateAndDelete(t *testing.T) {
	logger := NewMockLogger()

	h := newLiveHarness(t, &configstore.GovernanceConfig{}, nil)
	store := h.mustGovernanceStore(logger, nil)

	b1 := buildBudget("b1", 10.0, "1h")
	b2 := buildBudget("b2", 100.0, "1d")

	vk := buildVirtualKeyWithMultiBudgets("vk1", "sk-bf-test", "Test VK",
		[]configstoreTables.TableBudget{*b1, *b2})
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	// Create
	h.putVirtualKey(vk)

	exists := store.LoadBudget(context.Background(), "b1") != nil
	assert.True(t, exists, "Budget b1 should be in memory after create")
	exists = store.LoadBudget(context.Background(), "b2") != nil
	assert.True(t, exists, "Budget b2 should be in memory after create")

	retrieved, found := store.GetVirtualKey(context.Background(), "sk-bf-test")
	require.True(t, found)
	assert.Len(t, retrieved.BudgetIDs, 2, "VK should have 2 budgets")

	// Delete
	h.deleteVirtualKey("vk1")

	exists = store.LoadBudget(context.Background(), "b1") != nil
	assert.False(t, exists, "Budget b1 should be removed after delete")
	exists = store.LoadBudget(context.Background(), "b2") != nil
	assert.False(t, exists, "Budget b2 should be removed after delete")

	_, found = store.GetVirtualKey(context.Background(), "sk-bf-test")
	assert.False(t, found, "VK should not be found after delete")
}

// TestGovernanceStore_CreateVirtualKeyInMemory_DecouplesFromCallerPointer is a regression test
// for a double-counting bug on new VKs: the create handler keeps mutating the caller's
// TableVirtualKey after the store call (hydrateVKGovernance reassigns Budgets/RateLimit/RateLimitID
// from VK-scoped model configs for serialization), so if the store kept that pointer the
// model-config-owned IDs would leak onto the tracked VK's hierarchy fields and the usage tracker
// would count each request twice (VK-scoped-model + VK-hierarchy). The stored VK must be a
// decoupled clone.
func TestGovernanceStore_CreateVirtualKeyInMemory_DecouplesFromCallerPointer(t *testing.T) {
	logger := NewMockLogger()
	h := newLiveHarness(t, &configstore.GovernanceConfig{}, nil)
	store := h.mustGovernanceStore(logger, nil)

	// A freshly-created VK loaded from DB carries no legacy rate-limit/budget — its
	// governance lives in a VK-scoped model config, so RateLimitID is nil and Budgets empty.
	// It does carry a provider config (with no per-provider governance of its own yet).
	vk := buildVirtualKey("vk1", "sk-bf-test", "Test VK", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}
	h.putVirtualKey(vk)

	// Simulate hydrateVKGovernance mutating the caller's pointer after the store call: it
	// reassigns the top-level fields AND mutates the provider-config element IN PLACE
	// (pc := &vk.ProviderConfigs[i]; pc.Budgets = ...), injecting the model-config-owned
	// rate-limit/budget identity. The in-place element write is the case greptile flagged:
	// a shallow clone shares the ProviderConfigs backing array and would leak it.
	hydratedRL := buildRateLimit("mc-rl", 6, 6)
	vk.RateLimit = hydratedRL
	vk.RateLimitID = &hydratedRL.ID
	vk.Budgets = []configstoreTables.TableBudget{*buildBudget("mc-b", 200.0, "1h")}

	pcRL := buildRateLimit("mc-pc-rl", 10, 10)
	vk.ProviderConfigs[0].RateLimit = pcRL
	vk.ProviderConfigs[0].RateLimitID = &pcRL.ID
	vk.ProviderConfigs[0].Budgets = []configstoreTables.TableBudget{*buildBudget("mc-pc-b", 50.0, "1h")}

	// The tracked VK must NOT reflect those post-create mutations, otherwise the
	// VK-hierarchy usage path would double-count alongside the VK-scoped-model path.
	tracked, found := store.GetVirtualKey(context.Background(), "sk-bf-test")
	require.True(t, found)
	require.NotNil(t, tracked)
	assert.Nil(t, store.LoadRateLimit(context.Background(), hydratedRL.ID), "tracked VK rate limit must stay decoupled from caller mutation")
	assert.Empty(t, tracked.RateLimitID, "tracked VK rate limit ID must stay decoupled from caller mutation")
	assert.Empty(t, tracked.BudgetIDs, "tracked VK budgets must stay decoupled from caller mutation")

	// Per-provider entries must stay decoupled too — this is the ProviderConfigs slice
	// aliasing greptile flagged (in-place element mutation through the shared backing array).
	require.Len(t, tracked.ProviderLimits, 1)
	assert.Empty(t, tracked.ProviderLimits[0].RateLimitID, "tracked provider-config rate limit ID must stay decoupled")
	assert.Empty(t, tracked.ProviderLimits[0].BudgetIDs, "tracked provider-config budgets must stay decoupled")
}

// TestGovernanceStore_UpdateVirtualKeyInMemory_RotatedValueRemovesOldLookup checks that rewriting a key with a new value retires the old one and keeps the budget's usage.
func TestGovernanceStore_UpdateVirtualKeyInMemory_RotatedValueRemovesOldLookup(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("budget1", 100.0, 25.0, "1d")
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-old", "Test VK", budget)

	h := newLiveHarness(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil)
	store := h.mustGovernanceStore(logger, nil)
	// Usage in memory runs ahead of the row, which still says 25: the refresh must keep memory's 30.
	require.NoError(t, store.BumpBudgetUsageBy(context.Background(), "budget1", 5))

	updated := *vk
	updated.Value = *schemas.NewSecretVar("sk-bf-new")
	h.putVirtualKey(&updated)

	oldVK, oldFound := store.GetVirtualKey(context.Background(), "sk-bf-old")
	assert.False(t, oldFound)
	assert.Nil(t, oldVK)

	newVK, newFound := store.GetVirtualKey(context.Background(), "sk-bf-new")
	require.True(t, newFound)
	require.NotNil(t, newVK)
	assert.Equal(t, "vk1", newVK.ID)
	require.Len(t, newVK.BudgetIDs, 1)
	assert.Equal(t, 30.0, store.LoadBudget(context.Background(), newVK.BudgetIDs[0]).CurrentUsage)
}

// TestGovernanceStore_UpdateVirtualKeyInMemory_GracePeriodKeepsPreviousValue checks that a rotated key keeps accepting its previous value until the grace period ends.
func TestGovernanceStore_UpdateVirtualKeyInMemory_GracePeriodKeepsPreviousValue(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-old", "Test VK", true)
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	store := h.mustGovernanceStore(logger, nil)

	// Rotation with cooldown: previous value stays valid until expiry.
	now := time.Now().UTC()
	exp := now.Add(5 * time.Minute)
	rotated := *vk
	rotated.Value = *schemas.NewSecretVar("sk-bf-new")
	rotated.PreviousValue = *schemas.NewSecretVar("sk-bf-old")
	rotated.PreviousValueExpiresAt = &exp
	rotated.RotatedAt = &now
	h.putVirtualKey(&rotated)

	newVK, found := store.GetVirtualKey(context.Background(), "sk-bf-new")
	require.True(t, found)
	assert.Equal(t, "vk1", newVK.ID)

	oldVK, found := store.GetVirtualKey(context.Background(), "sk-bf-old")
	require.True(t, found, "previous value must authenticate during the grace period")
	assert.Equal(t, "vk1", oldVK.ID)

	byID, found := store.GetVirtualKeyByID(context.Background(), "vk1")
	require.True(t, found)
	assert.Equal(t, "sk-bf-new", byID.Value)
}

// TestGovernanceStore_StoreVirtualKey_GraceAliasDoesNotDisplaceCurrentOwner pins
// credential ownership when two keys claim the same value. PreviousValueHash is
// deliberately non-unique, so a rotated-out value can equal another virtual
// key's live current value. Registering the grace alias must never take that
// value away from the key that owns it as its current credential, otherwise a
// legitimate request authenticates as the wrong identity and is charged against
// the wrong budgets. Mirrors the database lookup order, which resolves the
// current value hash before the grace hash.
func TestGovernanceStore_StoreVirtualKey_GraceAliasDoesNotDisplaceCurrentOwner(t *testing.T) {
	logger := NewMockLogger()
	const shared = "sk-bf-shared"

	vkA := buildVirtualKey("vk-a", shared, "Rotating VK", true)
	vkB := buildVirtualKey("vk-b", "sk-bf-b-original", "Owner VK", true)
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vkA, *vkB},
	}, nil)
	store := h.mustGovernanceStore(logger, nil)

	// VK-A rotates away from the shared value into a live grace window.
	now := time.Now().UTC()
	exp := now.Add(5 * time.Minute)
	rotatedA := *vkA
	rotatedA.Value = *schemas.NewSecretVar("sk-bf-a-new")
	rotatedA.PreviousValue = *schemas.NewSecretVar(shared)
	rotatedA.PreviousValueExpiresAt = &exp
	rotatedA.RotatedAt = &now
	h.putVirtualKey(&rotatedA)

	graceOwner, found := store.GetVirtualKey(context.Background(), shared)
	require.True(t, found, "grace value authenticates while no one else claims it")
	require.Equal(t, "vk-a", graceOwner.ID)

	// VK-B now takes the same value as its CURRENT credential, which the schema
	// permits because the previous-value hash index is non-unique.
	claimedB := *vkB
	claimedB.Value = *schemas.NewSecretVar(shared)
	h.putVirtualKey(&claimedB)

	currentOwner, found := store.GetVirtualKey(context.Background(), shared)
	require.True(t, found)
	require.Equal(t, "vk-b", currentOwner.ID, "the key owning the value as its current credential must win")

	// Any later touch of VK-A re-registers its grace alias (a config reload, a
	// rename, an unrelated budget edit). That must not steal the value back.
	renamedA := rotatedA
	renamedA.Name = "Rotating VK renamed"
	h.putVirtualKey(&renamedA)

	afterTouch, found := store.GetVirtualKey(context.Background(), shared)
	require.True(t, found, "the current owner must still authenticate")
	assert.Equal(t, "vk-b", afterTouch.ID, "re-registering a grace alias must not displace the current owner")

	// VK-A itself keeps working under its own current value.
	stillA, found := store.GetVirtualKey(context.Background(), "sk-bf-a-new")
	require.True(t, found)
	assert.Equal(t, "vk-a", stillA.ID)
}

// sharedValueStore builds two keys where VK-A has rotated away from a value
// that VK-B then claimed as its own current credential - the collision the
// non-unique previous-value hash index allows. Returns the store with VK-B
// owning the shared value, and the harness its rows live in.
func sharedValueStore(t *testing.T, shared string) (*LocalGovernanceStore, *liveHarness) {
	t.Helper()
	logger := NewMockLogger()
	vkA := buildVirtualKey("vk-a", shared, "Rotating VK", true)
	vkB := buildVirtualKey("vk-b", "sk-bf-b-original", "Owner VK", true)
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vkA, *vkB},
	}, nil)
	store := h.mustGovernanceStore(logger, nil)

	now := time.Now().UTC()
	exp := now.Add(5 * time.Minute)
	rotatedA := *vkA
	rotatedA.Value = *schemas.NewSecretVar("sk-bf-a-new")
	rotatedA.PreviousValue = *schemas.NewSecretVar(shared)
	rotatedA.PreviousValueExpiresAt = &exp
	rotatedA.RotatedAt = &now
	h.putVirtualKey(&rotatedA)

	claimedB := *vkB
	claimedB.Value = *schemas.NewSecretVar(shared)
	h.putVirtualKey(&claimedB)

	owner, found := store.GetVirtualKey(context.Background(), shared)
	require.True(t, found)
	require.Equal(t, "vk-b", owner.ID, "VK-B must own the shared value before the cleanup under test")
	return store, h
}

// TestGovernanceStore_ReRotationDoesNotEvictAnotherKeysCurrentValue covers the
// delete side of credential ownership. When VK-A rotates again, its reconcile
// loop drops the aliases it used to own - but the retired value now belongs to
// VK-B as a current credential, so removing it would silently deauthenticate a
// live key.
func TestGovernanceStore_ReRotationDoesNotEvictAnotherKeysCurrentValue(t *testing.T) {
	const shared = "sk-bf-shared-rerotate"
	store, h := sharedValueStore(t, shared)

	// VK-A rotates again with the cooldown disabled, so every alias it held
	// falls out of the keep set.
	later := time.Now().UTC().Add(time.Minute)
	rerotatedA := *buildVirtualKey("vk-a", "sk-bf-a-newest", "Rotating VK", true)
	rerotatedA.RotatedAt = &later
	rerotatedA.ClearPreviousValue()
	h.putVirtualKey(&rerotatedA)

	afterRotate, found := store.GetVirtualKey(context.Background(), shared)
	require.True(t, found, "re-rotating VK-A must not evict the value VK-B owns")
	assert.Equal(t, "vk-b", afterRotate.ID)

	stillA, found := store.GetVirtualKey(context.Background(), "sk-bf-a-newest")
	require.True(t, found)
	assert.Equal(t, "vk-a", stillA.ID)
}

// TestGovernanceStore_DeletionDoesNotEvictAnotherKeysCurrentValue covers the
// same ownership rule on the deletion path, which clears the deleted key's
// current and grace-period aliases.
func TestGovernanceStore_DeletionDoesNotEvictAnotherKeysCurrentValue(t *testing.T) {
	const shared = "sk-bf-shared-delete"
	store, h := sharedValueStore(t, shared)

	// VK-A still carries the shared value as its grace-period previous value.
	h.deleteVirtualKey("vk-a")

	afterDelete, found := store.GetVirtualKey(context.Background(), shared)
	require.True(t, found, "deleting VK-A must not evict the value VK-B owns")
	assert.Equal(t, "vk-b", afterDelete.ID)

	_, found = store.GetVirtualKey(context.Background(), "sk-bf-a-new")
	assert.False(t, found, "the deleted key's own value must stop authenticating")
}

// TestGovernanceStore_GetVirtualKey_ExpiredPreviousValueRejectedAndCleaned checks that a previous value stops authenticating once its grace period has passed.
func TestGovernanceStore_GetVirtualKey_ExpiredPreviousValueRejectedAndCleaned(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-old", "Test VK", true)
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	store := h.mustGovernanceStore(logger, nil)

	// Rotation with an active grace window, aged deterministically below by
	// rewriting the row with a past expiry: expiry is evaluated at lookup time,
	// so no sleep is needed.
	now := time.Now().UTC()
	exp := now.Add(5 * time.Minute)
	rotated := *vk
	rotated.Value = *schemas.NewSecretVar("sk-bf-new")
	rotated.PreviousValue = *schemas.NewSecretVar("sk-bf-old")
	rotated.PreviousValueExpiresAt = &exp
	rotated.RotatedAt = &now
	h.putVirtualKey(&rotated)

	_, found := store.GetVirtualKey(context.Background(), "sk-bf-old")
	require.True(t, found, "grace value must work before expiry")

	_, found = store.GetVirtualKey(context.Background(), "sk-bf-new")
	require.True(t, found)
	past := now.Add(-time.Millisecond)
	expired := rotated
	expired.PreviousValueExpiresAt = &past
	h.putVirtualKey(&expired)

	_, found = store.GetVirtualKey(context.Background(), "sk-bf-old")
	assert.False(t, found, "grace value must stop working after expiry")

	// Expiry must not touch the current value or the ID index.
	_, found = store.GetVirtualKey(context.Background(), "sk-bf-new")
	assert.True(t, found)
	_, found = store.GetVirtualKeyByID(context.Background(), "vk1")
	assert.True(t, found)
}

// TestGovernanceStore_UpdateVirtualKeyInMemory_UnresolvableValueRemovesIDIndex checks that a key whose value stops resolving is unreachable by ID as well as by value.
func TestGovernanceStore_UpdateVirtualKeyInMemory_UnresolvableValueRemovesIDIndex(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-live", "Test VK", true)
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	store := h.mustGovernanceStore(logger, nil)

	_, found := store.GetVirtualKeyByID(context.Background(), "vk1")
	require.True(t, found)

	// Update to a value that cannot resolve (unset env ref). Fail closed: the
	// VK must become unavailable on BOTH lookup paths, not just the value map.
	storeTestWriteUnresolvableValue(h, "vk1", "env.BIFROST_TEST_UNSET_VK_VALUE")

	_, found = store.GetVirtualKey(context.Background(), "sk-bf-live")
	assert.False(t, found, "old value key must not authenticate after update")
	_, found = store.GetVirtualKeyByID(context.Background(), "vk1")
	assert.False(t, found, "ID index must fail closed when the new value cannot resolve")
}

// TestGovernanceStore_UpdateVirtualKeyInMemory_UnresolvableValueRemovesGraceKey checks that a rotating key whose current value stops resolving no longer authenticates with its previous value.
func TestGovernanceStore_UpdateVirtualKeyInMemory_UnresolvableValueRemovesGraceKey(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-old", "Test VK", true)
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	store := h.mustGovernanceStore(logger, nil)

	// Rotate with an active grace window so the previous value is registered.
	now := time.Now().UTC()
	exp := now.Add(5 * time.Minute)
	rotated := *vk
	rotated.Value = *schemas.NewSecretVar("sk-bf-new")
	rotated.PreviousValue = *schemas.NewSecretVar("sk-bf-old")
	rotated.PreviousValueExpiresAt = &exp
	rotated.RotatedAt = &now
	h.putVirtualKey(&rotated)

	_, found := store.GetVirtualKey(context.Background(), "sk-bf-old")
	require.True(t, found, "grace value must authenticate before the broken update")

	// Update to a current value that cannot resolve (unset env ref) while the
	// grace window is still active. Fail closed: the VK must become unavailable
	// on EVERY lookup path, including the grace-period previous value.
	storeTestWriteUnresolvableValue(h, "vk1", "env.BIFROST_TEST_UNSET_VK_VALUE")

	_, found = store.GetVirtualKey(context.Background(), "sk-bf-new")
	assert.False(t, found, "old current value must not authenticate after the broken update")
	_, found = store.GetVirtualKey(context.Background(), "sk-bf-old")
	assert.False(t, found, "grace value must fail closed when the new value cannot resolve")
	_, found = store.GetVirtualKeyByID(context.Background(), "vk1")
	assert.False(t, found, "ID index must fail closed when the new value cannot resolve")
	assert.Empty(t, store.live.Collections.VirtualKeys.List(registry.VirtualKeyByPreviousValue, "sk-bf-old"), "previous-value entry must be removed from the index")
}

// storeTestWriteUnresolvableValue stores ref as a key's value straight into its row and refreshes
// the key. The row hooks refuse to save a reference that does not resolve, so this stands in for a
// row whose environment or vault reference stopped resolving after it was written.
func storeTestWriteUnresolvableValue(h *liveHarness, id, ref string) {
	h.t.Helper()
	h.must(h.db.Model(&configstoreTables.TableVirtualKey{}).Where("id = ?", id).UpdateColumn("value", ref).Error, "write unresolvable value")
	h.refresh(livestore.Ref{Kind: livestore.KindVirtualKey, ID: id})
}

// TestGovernanceStore_UpdateVirtualKeyInMemory_RepeatedRotationDropsStalePrevious checks that a second rotation retires the value the first one kept.
func TestGovernanceStore_UpdateVirtualKeyInMemory_RepeatedRotationDropsStalePrevious(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-v1", "Test VK", true)
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	store := h.mustGovernanceStore(logger, nil)

	now := time.Now().UTC()
	exp := now.Add(5 * time.Minute)

	first := *vk
	first.Value = *schemas.NewSecretVar("sk-bf-v2")
	first.PreviousValue = *schemas.NewSecretVar("sk-bf-v1")
	first.PreviousValueExpiresAt = &exp
	first.RotatedAt = &now
	h.putVirtualKey(&first)

	// Second rotation inside the grace window: v2 becomes the grace value and v1
	// dies immediately (only one grace value at a time).
	second := first
	second.Value = *schemas.NewSecretVar("sk-bf-v3")
	second.PreviousValue = *schemas.NewSecretVar("sk-bf-v2")
	h.putVirtualKey(&second)

	_, found := store.GetVirtualKey(context.Background(), "sk-bf-v3")
	assert.True(t, found)
	_, found = store.GetVirtualKey(context.Background(), "sk-bf-v2")
	assert.True(t, found, "newest previous value must authenticate")
	_, found = store.GetVirtualKey(context.Background(), "sk-bf-v1")
	assert.False(t, found, "second-oldest value must die on repeated rotation")
	assert.Empty(t, store.live.Collections.VirtualKeys.List(registry.VirtualKeyByPreviousValue, "sk-bf-v1"), "stale previous key must be removed from the index")
}

// TestGovernanceStore_DeleteVirtualKeyInMemory_RemovesGraceKey checks that deleting a rotating key stops both its current and previous values authenticating.
func TestGovernanceStore_DeleteVirtualKeyInMemory_RemovesGraceKey(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-old", "Test VK", true)
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	store := h.mustGovernanceStore(logger, nil)

	now := time.Now().UTC()
	exp := now.Add(5 * time.Minute)
	rotated := *vk
	rotated.Value = *schemas.NewSecretVar("sk-bf-new")
	rotated.PreviousValue = *schemas.NewSecretVar("sk-bf-old")
	rotated.PreviousValueExpiresAt = &exp
	rotated.RotatedAt = &now
	h.putVirtualKey(&rotated)

	h.deleteVirtualKey("vk1")

	_, found := store.GetVirtualKey(context.Background(), "sk-bf-new")
	assert.False(t, found, "current value must not authenticate after delete")
	_, found = store.GetVirtualKey(context.Background(), "sk-bf-old")
	assert.False(t, found, "grace value must not authenticate after delete")
	_, found = store.GetVirtualKeyByID(context.Background(), "vk1")
	assert.False(t, found)
}

// TestGovernanceStore_UpdateRateLimitUsage_TokensAndRequests tests atomic rate limit usage updates
func TestGovernanceStore_UpdateRateLimitUsage_TokensAndRequests(t *testing.T) {
	logger := NewMockLogger()

	rateLimit := buildRateLimitWithUsage("rl1", 10000, 0, 1000, 0)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", rateLimit)

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil, nil)
	require.NoError(t, err)

	rec, _ := store.GetVirtualKeyByID(context.Background(), vk.ID)

	// Test updating tokens
	err = chargeGrantRateLimits(store, context.Background(), rec, schemas.OpenAI, 500, true, false)
	assert.NoError(t, err, "Rate limit update should succeed")

	// Retrieve the updated rate limit from the main RateLimits map
	governanceData := store.GetGovernanceData(context.Background())
	updatedRateLimit, exists := governanceData.RateLimits["rl1"]
	require.True(t, exists, "Rate limit should exist")
	require.NotNil(t, updatedRateLimit)

	assert.Equal(t, int64(500), updatedRateLimit.TokenCurrentUsage, "Token usage should be updated")
	assert.Equal(t, int64(0), updatedRateLimit.RequestCurrentUsage, "Request usage should not change")

	// Test updating requests
	err = chargeGrantRateLimits(store, context.Background(), rec, schemas.OpenAI, 0, false, true)
	assert.NoError(t, err, "Rate limit update should succeed")

	// Retrieve the updated rate limit again
	governanceData = store.GetGovernanceData(context.Background())
	updatedRateLimit, exists = governanceData.RateLimits["rl1"]
	require.True(t, exists, "Rate limit should exist")
	require.NotNil(t, updatedRateLimit)

	assert.Equal(t, int64(500), updatedRateLimit.TokenCurrentUsage, "Token usage should not change")
	assert.Equal(t, int64(1), updatedRateLimit.RequestCurrentUsage, "Request usage should be incremented")
}

// TestGovernanceStore_ResetExpiredRateLimits tests rate limit reset
func TestGovernanceStore_ResetExpiredRateLimits(t *testing.T) {
	logger := NewMockLogger()

	// Create rate limit that's already expired
	duration := "1m"
	rateLimit := &configstoreTables.TableRateLimit{
		ID:                   "rl1",
		TokenMaxLimit:        ptrInt64(10000),
		TokenCurrentUsage:    5000,
		TokenResetDuration:   &duration,
		TokenLastReset:       time.Now().Add(-2 * time.Minute), // Expired
		RequestMaxLimit:      ptrInt64(1000),
		RequestCurrentUsage:  500,
		RequestResetDuration: &duration,
		RequestLastReset:     time.Now().Add(-2 * time.Minute), // Expired
	}

	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", rateLimit)

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil, nil)
	require.NoError(t, err)

	// Reset expired rate limits
	expiredRateLimits := store.ResetExpiredRateLimitsInMemory(context.Background())
	err = store.ResetExpiredRateLimits(context.Background(), expiredRateLimits)
	assert.NoError(t, err, "Reset should succeed")

	// Retrieve the updated VK and the rate limit it names to check rate limit changes
	updatedVK, _ := store.GetVirtualKey(context.Background(), "sk-bf-test")
	require.NotNil(t, updatedVK)
	updatedRateLimit := store.LoadRateLimit(context.Background(), updatedVK.RateLimitID)
	require.NotNil(t, updatedRateLimit)

	assert.Equal(t, int64(0), updatedRateLimit.TokenCurrentUsage, "Token usage should be reset")
	assert.Equal(t, int64(0), updatedRateLimit.RequestCurrentUsage, "Request usage should be reset")
}

// TestGovernanceStore_ResetExpiredBudgets tests budget reset
func TestGovernanceStore_ResetExpiredBudgets(t *testing.T) {
	logger := NewMockLogger()

	// Create budget that's already expired
	budget := &configstoreTables.TableBudget{
		ID:            "budget1",
		MaxLimit:      100.0,
		CurrentUsage:  75.0,
		ResetDuration: "1d",
		LastReset:     time.Now().Add(-48 * time.Hour), // Expired
	}

	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", budget)

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	// Reset expired budgets
	expiredBudgets := store.ResetExpiredBudgetsInMemory(context.Background())
	err = store.ResetExpiredBudgets(context.Background(), expiredBudgets)
	assert.NoError(t, err, "Reset should succeed")

	// Retrieve the updated VK and the budgets it names to check budget changes
	updatedVK, _ := store.GetVirtualKey(context.Background(), "sk-bf-test")
	require.NotNil(t, updatedVK)
	require.True(t, len(updatedVK.BudgetIDs) > 0, "VK should have budgets")

	assert.Equal(t, 0.0, store.LoadBudget(context.Background(), updatedVK.BudgetIDs[0]).CurrentUsage, "Budget usage should be reset")
}

// TestGovernanceStoreResetBudgetAdvancesOverride verifies each existing reset consumes one finite cycle.
func TestGovernanceStoreResetBudgetAdvancesOverride(t *testing.T) {
	store := newStandaloneStore(t)
	finite := buildBudgetWithUsage("finite-override", 100, 75, "1d")
	require.NoError(t, finite.SetOverride(25, configstoreTables.BudgetOverrideModeCycles, 2))
	putBudget(t, store, finite.ID, finite)

	firstReset := finite.LastReset.Add(24 * time.Hour)
	reset, ok := store.ResetBudgetAt(context.Background(), finite.ID, firstReset)
	require.True(t, ok)
	assert.Zero(t, reset.CurrentUsage)
	assert.Equal(t, 1, reset.OverrideCyclesRemaining)
	assert.Equal(t, 125.0, reset.EffectiveMaxLimit())

	secondReset := firstReset.Add(24 * time.Hour)
	reset, ok = store.ResetBudgetAt(context.Background(), finite.ID, secondReset)
	require.True(t, ok)
	assert.False(t, reset.HasActiveOverride())
	assert.Equal(t, 100.0, reset.EffectiveMaxLimit())

	permanent := buildBudgetWithUsage("forever-override", 100, 75, "1d")
	require.NoError(t, permanent.SetOverride(50, configstoreTables.BudgetOverrideModeForever, 0))
	putBudget(t, store, permanent.ID, permanent)
	reset, ok = store.ResetBudgetAt(context.Background(), permanent.ID, permanent.LastReset.Add(24*time.Hour))
	require.True(t, ok)
	assert.True(t, reset.HasActiveOverride())
	assert.Equal(t, 150.0, reset.EffectiveMaxLimit())
}

// TestGovernanceStoreBudgetReloadRefreshesOverride verifies a reload of a budget's row refreshes its
// config and override without clobbering runtime counters.
func TestGovernanceStoreBudgetReloadRefreshesOverride(t *testing.T) {
	store := newStandaloneStore(t)
	lastReset := time.Now().Add(-30 * time.Minute)
	live := buildBudgetWithUsage("cache-override", 100, 40, "1h")
	live.LastReset = lastReset
	putBudget(t, store, live.ID, live)

	refreshed := buildBudgetWithUsage(live.ID, 120, 0, "2h")
	require.NoError(t, refreshed.SetOverride(30, configstoreTables.BudgetOverrideModeCycles, 3))
	reloadBudget(t, store, live.ID, refreshed)

	got := store.LoadBudget(context.Background(), live.ID)
	require.NotNil(t, got)
	assert.Equal(t, 40.0, got.CurrentUsage)
	assert.True(t, got.LastReset.Equal(lastReset))
	assert.Equal(t, 120.0, got.MaxLimit)
	assert.Equal(t, "2h", got.ResetDuration)
	assert.Equal(t, 30.0, got.OverrideAmount)
	assert.Equal(t, configstoreTables.BudgetOverrideModeCycles, got.OverrideMode)
	assert.Equal(t, 3, got.OverrideCyclesRemaining)
}

// TestGovernanceStoreResetPersistsOverrideLifecycle verifies the existing reset write stores the decremented cycle state.
func TestGovernanceStoreResetPersistsOverrideLifecycle(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	configStore, err := newTestConfigStore(t)
	require.NoError(t, err)

	// The live store loads only the budgets something holds, so a team holds this one.
	teamID := "persisted-override-team"
	require.NoError(t, configStore.CreateTeam(ctx, &configstoreTables.TableTeam{ID: teamID, Name: "Persisted override team"}))
	budget := buildBudgetWithUsage("persisted-override", 100, 75, "1d")
	budget.TeamID = &teamID
	require.NoError(t, budget.SetOverride(25, configstoreTables.BudgetOverrideModeCycles, 2))
	require.NoError(t, configStore.CreateBudget(ctx, budget))

	h, err := openLiveHarness(t, configStore, nil, nil)
	require.NoError(t, err)
	store, err := NewLocalGovernanceStore(ctx, logger, configStore, h.live, nil)
	require.NoError(t, err)
	t.Cleanup(store.Close)
	reset, ok := store.ResetBudgetAt(ctx, budget.ID, budget.LastReset.Add(24*time.Hour))
	require.True(t, ok)
	require.NoError(t, store.ResetExpiredBudgets(ctx, []*configstoreTables.TableBudget{reset}))

	persisted, err := configStore.GetBudget(ctx, budget.ID)
	require.NoError(t, err)
	assert.Zero(t, persisted.CurrentUsage)
	assert.Equal(t, 25.0, persisted.OverrideAmount)
	assert.Equal(t, configstoreTables.BudgetOverrideModeCycles, persisted.OverrideMode)
	assert.Equal(t, 1, persisted.OverrideCyclesRemaining)
}

// TestGovernanceStore_GetAllBudgets tests retrieving all budgets
func TestGovernanceStore_GetAllBudgets(t *testing.T) {
	logger := NewMockLogger()

	budgets := []configstoreTables.TableBudget{
		*buildBudget("budget1", 100.0, "1d"),
		*buildBudget("budget2", 500.0, "1d"),
		*buildBudget("budget3", 1000.0, "1d"),
	}

	// The live store loads only the budgets something holds, so a key holds all three.
	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*buildVirtualKeyWithMultiBudgets("vk1", "sk-bf-all-budgets", "All budgets VK", budgets)},
		Budgets:     budgets,
	}, nil, nil)
	require.NoError(t, err)

	allBudgets := store.GetGovernanceData(context.Background()).Budgets
	assert.Equal(t, 3, len(allBudgets), "Should have 3 budgets")
	assert.NotNil(t, allBudgets["budget1"])
	assert.NotNil(t, allBudgets["budget2"])
	assert.NotNil(t, allBudgets["budget3"])
}

// TestGovernanceStore_RateLimitStatus tests rate limit status calculation
func TestGovernanceStore_RateLimitStatus(t *testing.T) {
	logger := NewMockLogger()

	// Create a rate limit with 1000 token limit
	limit := int64(1000)
	rateLimitID := "provider:openai:ratelimit"
	rl := &configstoreTables.TableRateLimit{
		ID:                rateLimitID,
		TokenMaxLimit:     &limit,
		TokenCurrentUsage: 500,
	}

	// Create a provider config that references the rate limit
	providerConfig := &configstoreTables.TableProvider{
		Name:        "openai",
		RateLimitID: &rateLimitID,
	}
	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		Providers:  []configstoreTables.TableProvider{*providerConfig},
		RateLimits: []configstoreTables.TableRateLimit{storeTestRateLimitRow(rl)},
	}, nil, nil)
	require.NoError(t, err)

	putRateLimit(t, store, rateLimitID, rl)

	// Get status
	status := store.GetBudgetAndRateLimitStatus(emptyCtx(), schemas.ModelProvider("openai"), "", nil, nil, nil)

	assert.NotNil(t, status)
	assert.Equal(t, 50.0, status.RateLimitTokenPercentUsed)

	// Update usage to exhausted state
	rl.TokenCurrentUsage = 1000
	putRateLimit(t, store, rateLimitID, rl)
	status = store.GetBudgetAndRateLimitStatus(emptyCtx(), schemas.ModelProvider("openai"), "", nil, nil, nil)

	assert.Equal(t, 100.0, status.RateLimitTokenPercentUsed)
}

// Routing can ask for status on a request nothing has resolved access for yet. That is not an error
// and must not panic: the deployment's own limits still apply, and there is simply no holder to add.
func TestGovernanceStore_StatusWithNoResolvedAccess(t *testing.T) {
	logger := NewMockLogger()
	h := newLiveHarness(t, &configstore.GovernanceConfig{}, nil)
	store := h.mustGovernanceStore(logger, nil)

	limit := int64(1000)
	rateLimitID := "provider:openai:ratelimit"
	rl := &configstoreTables.TableRateLimit{
		ID:                rateLimitID,
		TokenMaxLimit:     &limit,
		TokenCurrentUsage: 500,
	}
	row := storeTestRateLimitRow(rl)
	h.putRateLimit(&row)

	h.putProvider(&configstoreTables.TableProvider{Name: "openai", RateLimitID: &rateLimitID})
	putRateLimit(t, store, rateLimitID, rl)

	ctx := emptyCtx()
	require.Nil(t, ctx.Grant().Access(), "nothing resolved on this request")

	var status *BudgetAndRateLimitStatus
	require.NotPanics(t, func() {
		status = store.GetBudgetAndRateLimitStatus(ctx, schemas.ModelProvider("openai"), "gpt-4o", nil, nil, nil)
	})
	require.NotNil(t, status)
	assert.Equal(t, 50.0, status.RateLimitTokenPercentUsed, "the deployment's provider limit applies whoever is asking")
}

// TestGovernanceStore_BudgetStatus tests budget status calculation
func TestGovernanceStore_BudgetStatus(t *testing.T) {
	logger := NewMockLogger()

	budgetID := "provider:openai:budget"
	budget := &configstoreTables.TableBudget{
		ID:           budgetID,
		MaxLimit:     100.0,
		CurrentUsage: 60.0,
	}

	// Create a provider config that references the budget
	providerConfig := &configstoreTables.TableProvider{
		Name:     "openai",
		BudgetID: &budgetID,
	}
	// The row needs a reset duration to be stored; the status reads usage alone.
	row := *budget
	row.ResetDuration = "1d"
	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		Providers: []configstoreTables.TableProvider{*providerConfig},
		Budgets:   []configstoreTables.TableBudget{row},
	}, nil, nil)
	require.NoError(t, err)

	putBudget(t, store, budgetID, budget)

	// Get status
	status := store.GetBudgetAndRateLimitStatus(emptyCtx(), schemas.ModelProvider("openai"), "", nil, nil, nil)

	assert.NotNil(t, status)
	assert.Equal(t, 60.0, status.BudgetPercentUsed)

	// Update usage to exhausted state
	budget.CurrentUsage = 100.0
	putBudget(t, store, budgetID, budget)
	status = store.GetBudgetAndRateLimitStatus(emptyCtx(), schemas.ModelProvider("openai"), "", nil, nil, nil)

	assert.Equal(t, 100.0, status.BudgetPercentUsed)
}

// TestGetBudgetAndRateLimitStatus_VKScopedModelConfig tests that a VK-scoped model config
// budget (scope=virtual_key, model="*", provider="openai") is visible to GetBudgetAndRateLimitStatus.
// This is the regression introduced when the provider-governance migration relocated per-VK
// provider budgets from vk.ProviderConfigs into governance_model_configs; the status reader
// was not updated to look in the new location and always returned 0.0%.
func TestGetBudgetAndRateLimitStatus_VKScopedModelConfig(t *testing.T) {
	logger := NewMockLogger()
	vkID := "vk-test-id"
	vkValue := "vk-test-value"
	providerName := "openai"

	// Budget at 120% (exceeded) — mirrors the real bug scenario.
	budget := buildBudgetWithUsage("vk-model-budget", 0.001, 0.0012, "1h")

	// VK-scoped wildcard model config: scope=virtual_key, model="*", provider="openai".
	// This is exactly the shape the provider-governance migration writes.
	mc := buildVKScopedModelConfig("mc-vk-openai", configstoreTables.ModelConfigAllModels, &providerName, vkID, budget, nil)

	vk := buildVirtualKey(vkID, vkValue, "test-vk", true)

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	status := store.GetBudgetAndRateLimitStatus(resolverCtx(store, vkValue), schemas.ModelProvider(providerName), "gpt-5", nil, nil, nil)

	require.NotNil(t, status)
	assert.Greater(t, status.BudgetPercentUsed, 100.0, "VK-scoped model budget at 120%% must be visible to routing status")
}

// TestGetBudgetAndRateLimitStatus_VKScopedModelConfig_NoMatchOtherProvider tests that a
// VK-scoped model config for one provider does not bleed into status for another provider.
func TestGetBudgetAndRateLimitStatus_VKScopedModelConfig_NoMatchOtherProvider(t *testing.T) {
	logger := NewMockLogger()
	vkID := "vk-test-id"
	vkValue := "vk-test-value"
	providerName := "openai"

	budget := buildBudgetWithUsage("vk-model-budget", 0.001, 0.0012, "1h") // exceeded for openai
	mc := buildVKScopedModelConfig("mc-vk-openai", configstoreTables.ModelConfigAllModels, &providerName, vkID, budget, nil)
	vk := buildVirtualKey(vkID, vkValue, "test-vk", true)

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	// Query for anthropic — should not see the openai-scoped budget. resolverCtx resolves real
	// access for the VK so gatherLimits actually reaches the VK-scoped model config; emptyCtx
	// leaves access nil, which short-circuits gatherLimits before it ever consults VK-scoped
	// configs, so the assertion below would pass even if provider isolation were broken.
	status := store.GetBudgetAndRateLimitStatus(resolverCtx(store, vkValue), schemas.ModelProvider("anthropic"), "claude-3-5-sonnet", nil, nil, nil)

	require.NotNil(t, status)
	assert.Equal(t, 0.0, status.BudgetPercentUsed, "openai VK-scoped budget must not appear for anthropic requests")
}

// TestCollectApplicableGovernanceIDs_VKWildcardBudget_NoModel is a regression test for a
// batch accounting bug: a VK created with an unscoped "Budget configuration" (via the
// Create Virtual Key UI) stores the budget as a VK-scoped, all-providers, all-models
// wildcard model config (scope=virtual_key, model="*", provider=nil). Batch-create
// requests may not carry a top-level model, so CollectApplicableGovernanceIDs used to
// gate the entire VK-scoped model-config lookup on model != "" and silently miss this
// budget — the batch settled but the VK budget was never bumped.
func TestCollectApplicableGovernanceIDs_VKWildcardBudget_NoModel(t *testing.T) {
	logger := NewMockLogger()
	vkID := "vk-batches"
	vkValue := "vk-batches-value"

	budget := buildBudgetWithUsage("vk-wildcard-budget", 100.0, 0.0, "1M")
	mc := buildVKScopedModelConfig("mc-vk-wildcard", configstoreTables.ModelConfigAllModels, nil, vkID, budget, nil)
	vk := buildVirtualKey(vkID, vkValue, "batches", true)

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	// Settled the way the funnel settles it: the grant carries the attempt's limits, and the
	// accounted set is read from there.
	ctx := resolverCtx(store, vkValue)
	limits := settleAttemptLimits(ctx, store, schemas.ModelProvider("anthropic"), "")
	require.NotNil(t, limits)
	budgetIDs := limitIDsOf(limits.Budgets())

	assert.Contains(t, budgetIDs, budget.ID, "VK-scoped wildcard budget must be found even when the request carries no model (e.g. batch-create)")
}

// TestGetBudgetAndRateLimitStatus_GlobalModelConfig tests that a global model+provider
// config budget is visible to GetBudgetAndRateLimitStatus.
func TestGetBudgetAndRateLimitStatus_GlobalModelConfig(t *testing.T) {
	logger := NewMockLogger()
	providerName := "openai"

	budget := buildBudgetWithUsage("global-model-budget", 100.0, 75.0, "1h") // 75%
	mc := buildModelConfig("mc-global-openai", "gpt-5", &providerName, budget, nil)

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	status := store.GetBudgetAndRateLimitStatus(emptyCtx(), schemas.ModelProvider(providerName), "gpt-5", nil, nil, nil)

	require.NotNil(t, status)
	assert.Equal(t, 75.0, status.BudgetPercentUsed, "global model+provider budget must be visible to routing status")
}

// TestGetTeamNameAndGetCustomerName verifies the display-name accessors the
// enterprise layer uses as the log-stamping fallback when its edge-driven name
// caches miss: known entities return their name, unknown/empty ids return "".
func TestGetTeamNameAndGetCustomerName(t *testing.T) {
	logger := NewMockLogger()
	h := newLiveHarness(t, &configstore.GovernanceConfig{}, nil)
	store := h.mustGovernanceStore(logger, nil)

	h.putTeam(buildTeam("team-1", "Platform", nil))
	h.putCustomer(buildCustomer("cust-1", "ACME", nil))

	assert.Equal(t, "Platform", store.GetTeamName(context.Background(), "team-1"))
	assert.Equal(t, "ACME", store.GetCustomerName(context.Background(), "cust-1"))

	assert.Empty(t, store.GetTeamName(context.Background(), "unknown"))
	assert.Empty(t, store.GetCustomerName(context.Background(), "unknown"))
	assert.Empty(t, store.GetTeamName(context.Background(), ""))
	assert.Empty(t, store.GetCustomerName(context.Background(), ""))
}

// TestGovernanceStore_Customer_CalendarAligned_CreateInMemory verifies that
// writing a customer and refreshing it stamps IsCalendarAligned on the in-memory budget and
// rate limit so ResetExpiredBudgetsInMemory uses the calendar-aligned reset path.
func TestGovernanceStore_Customer_CalendarAligned_CreateInMemory(t *testing.T) {
	logger := NewMockLogger()
	h := newLiveHarness(t, &configstore.GovernanceConfig{}, nil)
	store := h.mustGovernanceStore(logger, nil)

	budgetID := "cust-bud-1"
	rlID := "cust-rl-1"
	budget := &configstoreTables.TableBudget{
		ID:            budgetID,
		MaxLimit:      100.0,
		ResetDuration: "1M",
		LastReset:     time.Now(),
	}
	rl := &configstoreTables.TableRateLimit{
		ID:                 rlID,
		TokenMaxLimit:      ptrInt64(1000),
		TokenResetDuration: storeTestDuration("1M"),
		TokenLastReset:     time.Now(),
		RequestLastReset:   time.Now(),
	}
	customer := buildCustomer("cust-1", "ACME", budget)
	customer.CalendarAligned = true
	customer.RateLimit = rl
	customer.RateLimitID = &rlID

	h.putCustomer(customer)

	storedBudget := store.LoadBudget(context.Background(), budgetID)
	require.NotNil(t, storedBudget, "budget should be in memory after create")
	assert.True(t, storedBudget.IsCalendarAligned, "budget.IsCalendarAligned should be true when customer.CalendarAligned=true")

	storedRL := store.LoadRateLimit(context.Background(), rlID)
	require.NotNil(t, storedRL, "rate limit should be in memory after create")
	assert.True(t, storedRL.IsCalendarAligned, "rate_limit.IsCalendarAligned should be true when customer.CalendarAligned=true")
}

// TestGovernanceStore_Customer_CalendarAligned_CreateInMemory_False verifies that
// IsCalendarAligned is false when the customer does not have calendar alignment enabled.
func TestGovernanceStore_Customer_CalendarAligned_CreateInMemory_False(t *testing.T) {
	logger := NewMockLogger()
	h := newLiveHarness(t, &configstore.GovernanceConfig{}, nil)
	store := h.mustGovernanceStore(logger, nil)

	budgetID := "cust-bud-2"
	budget := &configstoreTables.TableBudget{
		ID:            budgetID,
		MaxLimit:      50.0,
		ResetDuration: "1d",
		LastReset:     time.Now(),
	}
	customer := buildCustomer("cust-2", "Globex", budget)
	customer.CalendarAligned = false

	h.putCustomer(customer)

	storedBudget := store.LoadBudget(context.Background(), budgetID)
	require.NotNil(t, storedBudget)
	assert.False(t, storedBudget.IsCalendarAligned, "budget.IsCalendarAligned should be false when customer.CalendarAligned=false")
}

// TestGovernanceStore_Customer_CalendarAligned_UpdateInMemory verifies that
// writing a customer and refreshing it re-stamps IsCalendarAligned on the budget and rate limit
// so an in-flight toggle (false→true) takes effect immediately in memory.
func TestGovernanceStore_Customer_CalendarAligned_UpdateInMemory(t *testing.T) {
	logger := NewMockLogger()

	budgetID := "cust-bud-3"
	rlID := "cust-rl-3"
	budget := &configstoreTables.TableBudget{
		ID:            budgetID,
		MaxLimit:      200.0,
		ResetDuration: "1M",
		LastReset:     time.Now(),
	}
	rl := &configstoreTables.TableRateLimit{
		ID:                 rlID,
		TokenMaxLimit:      ptrInt64(500),
		TokenResetDuration: storeTestDuration("1M"),
		TokenLastReset:     time.Now(),
		RequestLastReset:   time.Now(),
	}
	customer := buildCustomer("cust-3", "Initech", budget)
	customer.CalendarAligned = false
	customer.RateLimit = rl
	customer.RateLimitID = &rlID

	h := newLiveHarness(t, &configstore.GovernanceConfig{
		Customers: []configstoreTables.TableCustomer{*customer},
		Budgets:   []configstoreTables.TableBudget{*budget},
	}, nil)
	store := h.mustGovernanceStore(logger, nil)

	// Budget and rate limit should start as non-calendar-aligned
	rawBudget := store.LoadBudget(context.Background(), budgetID)
	require.NotNil(t, rawBudget)
	assert.False(t, rawBudget.IsCalendarAligned)

	// Toggle calendar_aligned to true and update in memory
	customer.CalendarAligned = true
	h.putCustomer(customer)

	rawBudget = store.LoadBudget(context.Background(), budgetID)
	require.NotNil(t, rawBudget)
	assert.True(t, rawBudget.IsCalendarAligned, "budget.IsCalendarAligned should be true after update with CalendarAligned=true")

	rawRL := store.LoadRateLimit(context.Background(), rlID)
	require.NotNil(t, rawRL)
	assert.True(t, rawRL.IsCalendarAligned, "rate_limit.IsCalendarAligned should be true after update with CalendarAligned=true")
}

// Utility functions for tests

// ptrInt64 returns a pointer to i.
func ptrInt64(i int64) *int64 {
	return &i
}

// storeTestDuration returns a pointer to a reset duration.
func storeTestDuration(d string) *string {
	return &d
}

// storeTestRateLimitRow is rl as a storable row: a row with a token or request cap needs a reset
// duration, which these status tests leave out because the status reads usage alone.
func storeTestRateLimitRow(rl *configstoreTables.TableRateLimit) configstoreTables.TableRateLimit {
	row := *rl
	if row.TokenMaxLimit != nil && row.TokenResetDuration == nil {
		row.TokenResetDuration = storeTestDuration("1h")
	}
	if row.RequestMaxLimit != nil && row.RequestResetDuration == nil {
		row.RequestResetDuration = storeTestDuration("1h")
	}
	return row
}

// A downstream consumer's (e.g. an access profile's) per-model budgets materialise
// as virtual-key-scoped model configs naming one model and provider. A batch create
// carries no top-level model, so the in-flight collection matches only the wildcard
// tiers and those per-model budgets are invisible to it — which is why batch spend
// never reached them. Settlement knows the models and asks for them by name.
func TestCollectModelScopedGovernanceIDs_FindsExactModelConfigMissedAtCreateTime(t *testing.T) {
	logger := NewMockLogger()
	ctx := context.Background()
	providerName := "openai"
	vkID := "vk-alice"

	perModel := buildBudgetWithUsage("ap-model-budget", 100.0, 0.0, "1M")
	perModelMC := buildModelConfig("mc-vk-gpt5", "gpt-5", &providerName, perModel, nil)
	perModelMC.Scope = configstoreTables.ModelConfigScopeVirtualKey
	perModelMC.ScopeID = &vkID

	wildcard := buildBudgetWithUsage("ap-wildcard-budget", 100.0, 0.0, "1M")
	wildcardMC := buildModelConfig("mc-vk-all", configstoreTables.ModelConfigAllModels, &providerName, wildcard, nil)
	wildcardMC.Scope = configstoreTables.ModelConfigScopeVirtualKey
	wildcardMC.ScopeID = &vkID

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*perModelMC, *wildcardMC},
		Budgets:      []configstoreTables.TableBudget{*perModel, *wildcard},
	}, nil, nil)
	require.NoError(t, err)

	// What settlement sees: the model is known, so the per-model budget is reachable.
	atSettlement, _ := store.CollectModelScopedGovernanceIDs(ctx, vkID, "", schemas.ModelProvider(providerName), "gpt-5")
	assert.Contains(t, atSettlement, perModel.ID)
	// The wildcard is returned too and overlaps the in-flight set by design; callers
	// subtract what they already charged rather than relying on it being absent.
	assert.Contains(t, atSettlement, wildcard.ID)

	// A model with no config of its own still finds the wildcard and nothing else.
	otherModel, _ := store.CollectModelScopedGovernanceIDs(ctx, vkID, "", schemas.ModelProvider(providerName), "gpt-4o")
	assert.NotContains(t, otherModel, perModel.ID)
	assert.Contains(t, otherModel, wildcard.ID)

	// Scope isolation: another virtual key's id must not reach these configs.
	otherVK, _ := store.CollectModelScopedGovernanceIDs(ctx, "vk-bob", "", schemas.ModelProvider(providerName), "gpt-5")
	assert.Empty(t, otherVK)
}

// What a request is billed to and what is recorded on its log row are the limits settled on its
// grant, and nothing else. Both halves matter: a limit left out loses its usage when the node
// ghosts, and one that nothing debits invents usage on replay.
func TestSettledLimitsAreWhatIsAccounted(t *testing.T) {
	logger := NewMockLogger()

	vkBudget := buildBudget("b-vk", 1000, "1d")
	teamBudget := buildBudget("b-team", 1000, "1d")
	customerBudget := buildBudget("b-customer", 1000, "1d")
	providerBudget := buildBudget("b-provider", 1000, "1d")
	modelBudget := buildBudget("b-model", 1000, "1d")

	customer := buildCustomer("customer1", "Customer 1", customerBudget)
	team := buildTeam("team1", "Team 1", teamBudget)
	team.CustomerID = &customer.ID
	team.Customer = customer

	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-collect", "Collecting VK", vkBudget)
	vk.TeamID = &team.ID
	vk.Team = team
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		Teams:        []configstoreTables.TableTeam{*team},
		Customers:    []configstoreTables.TableCustomer{*customer},
		Providers:    []configstoreTables.TableProvider{*buildProviderWithGovernance("openai", providerBudget, nil)},
		ModelConfigs: []configstoreTables.TableModelConfig{*buildModelConfig("mc1", "gpt-4o", nil, modelBudget, nil)},
		Budgets: []configstoreTables.TableBudget{
			*vkBudget, *teamBudget, *customerBudget, *providerBudget, *modelBudget,
		},
	}, nil, nil)
	require.NoError(t, err)

	// Settling the limits on the grant is what the funnel does before any check runs; everything
	// afterwards reads them from there rather than working out a second answer.
	settle := func(t *testing.T, provider schemas.ModelProvider, model string) schemas.Limits {
		t.Helper()
		limits := settleAttemptLimits(resolverCtx(store, "sk-bf-collect"), store, provider, model)
		require.NotNil(t, limits)
		return limits
	}

	t.Run("every limit the access answers to, and the attempt's own", func(t *testing.T) {
		budgetIDs := limitIDsOf(settle(t, schemas.OpenAI, "gpt-4o").Budgets())

		assert.ElementsMatch(t,
			[]string{"b-provider", "b-model", "b-vk", "b-team", "b-customer"}, budgetIDs,
			"the holder chain, plus the provider and model config this attempt draws on")
	})

	t.Run("dropping what the holder funds keeps what the deployment does", func(t *testing.T) {
		// What a request whose holder is deliberately not being counted is billed. The provider and
		// the model config are owed whatever granted the request, so they stay.
		budgets := grant.LimitsFrom(settle(t, schemas.OpenAI, "gpt-4o").Budgets(), untrackedHolderKinds...)

		assert.ElementsMatch(t, []string{"b-provider", "b-model"}, limitIDsOf(budgets))
	})

	t.Run("a limit reached twice is accounted once", func(t *testing.T) {
		// The same budget row can cover a request by more than one route. Billing it twice would
		// charge one budget twice and have reconciliation replay its usage twice.
		budgetIDs := limitIDsOf(settle(t, schemas.OpenAI, "gpt-4o").Budgets())

		seen := map[string]int{}
		for _, id := range budgetIDs {
			seen[id]++
		}
		for id, count := range seen {
			assert.Equal(t, 1, count, "budget %s accounted more than once", id)
		}
	})

	t.Run("an attempt with no model still answers to its provider", func(t *testing.T) {
		// Tool execution and other modelless attempts still spend against the provider.
		budgetIDs := limitIDsOf(settle(t, schemas.OpenAI, "").Budgets())

		assert.Contains(t, budgetIDs, "b-provider")
		assert.NotContains(t, budgetIDs, "b-model", "no model means no model config applies")
	})
}

// Narrowing is what turns "every limit this request might answer to" into the one set it does, and it
// happens once the provider and model are settled. Everything after it (the checks, the co-payers
// named for the log, the usage charged) reads that set, so what lands here is what gets enforced.
func TestResolveLimits(t *testing.T) {
	logger := NewMockLogger()

	keyBudget := buildBudget("b-key", 1000, "1d")
	openaiBudget := buildBudget("b-key-openai", 1000, "1d")
	bedrockBudget := buildBudget("b-key-bedrock", 1000, "1d")
	teamBudget := buildBudget("b-team", 1000, "1d")
	providerBudget := buildBudget("b-provider", 1000, "1d")
	modelBudget := buildBudget("b-model", 1000, "1d")

	team := buildTeam("team1", "Team 1", teamBudget)
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-resolve", "Resolving VK", keyBudget)
	vk.TeamID = &team.ID
	vk.Team = team
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfigWithBudgets("openai", []string{"*"}, []configstoreTables.TableBudget{*openaiBudget}),
		buildProviderConfigWithBudgets("bedrock", []string{"*"}, []configstoreTables.TableBudget{*bedrockBudget}),
	}

	store, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
		VirtualKeys:  []configstoreTables.TableVirtualKey{*vk},
		Teams:        []configstoreTables.TableTeam{*team},
		Providers:    []configstoreTables.TableProvider{*buildProviderWithGovernance("openai", providerBudget, nil)},
		ModelConfigs: []configstoreTables.TableModelConfig{*buildModelConfig("mc1", "gpt-4o", nil, modelBudget, nil)},
		Budgets: []configstoreTables.TableBudget{
			*keyBudget, *openaiBudget, *bedrockBudget, *teamBudget, *providerBudget, *modelBudget,
		},
	}, nil, nil)
	require.NoError(t, err)

	t.Run("before it runs, only what tells one provider from another is known", func(t *testing.T) {
		// What load balancing needs and all it needs: each config's own limits, per provider, read
		// from the store by the permit's identity. Nothing that answers the same for every provider,
		// because that cannot help it choose. And nothing is settled on the grant yet, which is not
		// the same as nothing applying.
		ctx := resolverCtx(store, "sk-bf-resolve")
		access := ctx.Grant().Access()
		require.NotNil(t, access)
		require.Len(t, access.Bases(), 1)

		openaiBudgets, _ := store.PermitProviderLimits(ctx, access.Bases()[0], schemas.OpenAI)
		bedrockBudgets, _ := store.PermitProviderLimits(ctx, access.Bases()[0], schemas.Bedrock)
		assert.Equal(t, []string{"b-key-openai"}, limitIDsOf(openaiBudgets))
		assert.Equal(t, []string{"b-key-bedrock"}, limitIDsOf(bedrockBudgets))
		assert.Nil(t, ctx.Grant().Limits(), "nothing is settled yet, which is not the same as nothing applying")
	})

	t.Run("after it runs, the grant carries exactly what this pair answers to", func(t *testing.T) {
		ctx := resolverCtx(store, "sk-bf-resolve")

		settled := settleAttemptLimits(ctx, store, schemas.OpenAI, "gpt-4o")

		require.NotNil(t, settled)
		assert.ElementsMatch(t,
			[]string{"b-provider", "b-model", "b-key", "b-key-openai", "b-team"},
			limitIDsOf(settled.Budgets()))
		assert.NotContains(t, limitIDsOf(settled.Budgets()), "b-key-bedrock",
			"a provider this request is not using is not funding it")
	})

	t.Run("the settled limits are what everything downstream reads", func(t *testing.T) {
		ctx := resolverCtx(store, "sk-bf-resolve")

		settled := settleAttemptLimits(ctx, store, schemas.OpenAI, "gpt-4o")

		assert.Same(t, settled, ctx.Grant().Limits(),
			"recorded, or the check and the charge would each resolve their own")
	})

	t.Run("the next attempt replaces them whole", func(t *testing.T) {
		// A request that fails over changes its provider, not its caller: the access stays, and the
		// limits are settled again for the pair the new attempt uses.
		ctx := resolverCtx(store, "sk-bf-resolve")
		access := ctx.Grant().Access()

		first := settleAttemptLimits(ctx, store, schemas.OpenAI, "gpt-4o")
		second := settleAttemptLimits(ctx, store, schemas.Bedrock, "claude-sonnet-4")

		assert.Same(t, access, ctx.Grant().Access(), "what the request may reach is unchanged")
		assert.NotSame(t, first, second)
		assert.Same(t, second, ctx.Grant().Limits())
		assert.Contains(t, limitIDsOf(second.Budgets()), "b-key-bedrock")
		assert.NotContains(t, limitIDsOf(second.Budgets()), "b-key-openai")
	})

	t.Run("a request carrying no access still answers to the deployment", func(t *testing.T) {
		// Nothing granted it anything, so no holder pays for it; the deployment's own limits bind it
		// all the same, and they are settled on its grant like anyone else's.
		ctx := emptyCtx()

		settled := settleAttemptLimits(ctx, store, schemas.OpenAI, "gpt-4o")

		require.NotNil(t, settled)
		assert.ElementsMatch(t, []string{"b-provider", "b-model"}, limitIDsOf(settled.Budgets()))
		assert.Nil(t, ctx.Grant().Access(), "nothing resolved is not access permitting nothing")
	})

	t.Run("a context carrying no grant has nowhere to settle them", func(t *testing.T) {
		// The transport installs a grant on every request; a context without one is a wiring fault,
		// and nothing here papers over it.
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		assert.Nil(t, settleAttemptLimits(ctx, store, schemas.OpenAI, "gpt-4o"))
		assert.Nil(t, ctx.Grant())
	})

	t.Run("a modelless attempt still answers to its provider", func(t *testing.T) {
		ctx := resolverCtx(store, "sk-bf-resolve")

		settled := settleAttemptLimits(ctx, store, schemas.OpenAI, "")

		ids := limitIDsOf(settled.Budgets())
		assert.Contains(t, ids, "b-provider")
		assert.NotContains(t, ids, "b-model", "no model means no model config applies")
	})
}

// Routing asks for this before a request's limits have been settled onto its grant, so the status cannot
// read the settled set: gatherLimits assembles from the store and the access each time. Every source a
// request can answer to has to be reachable that way, or routing prefers a provider that is out of money.
//
// Each case loads exactly one source at 40% and asserts the status reports 40: a source that is not
// reached reports 0, so this fails for whichever one is missed rather than only for the total.
func TestGetBudgetAndRateLimitStatusReachesEverySource(t *testing.T) {
	const vkValue = "sk-bf-status"

	newStore := func(t *testing.T, build func(vk *configstoreTables.TableVirtualKey, cfg *configstore.GovernanceConfig)) *LocalGovernanceStore {
		t.Helper()
		vk := buildVirtualKey("vk1", vkValue, "Status VK", true)
		vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
			buildProviderConfig("openai", []string{"*"}),
		}
		cfg := &configstore.GovernanceConfig{}
		build(vk, cfg)
		cfg.VirtualKeys = []configstoreTables.TableVirtualKey{*vk}
		store, err := newTestGovernanceStore(t, NewMockLogger(), cfg, nil, nil)
		require.NoError(t, err)
		return store
	}

	// 40 used of 100, distinct from 0 so a source that is never reached is visible.
	fortyPercent := func(id string) *configstoreTables.TableBudget {
		return buildBudgetWithUsage(id, 100.0, 40.0, "1d")
	}

	cases := []struct {
		name  string
		build func(vk *configstoreTables.TableVirtualKey, cfg *configstore.GovernanceConfig)
	}{
		{
			name: "the deployment's own provider budget",
			build: func(_ *configstoreTables.TableVirtualKey, cfg *configstore.GovernanceConfig) {
				budget := fortyPercent("b-provider")
				cfg.Providers = []configstoreTables.TableProvider{*buildProviderWithGovernance("openai", budget, nil)}
				cfg.Budgets = append(cfg.Budgets, *budget)
			},
		},
		{
			name: "a global model config",
			build: func(_ *configstoreTables.TableVirtualKey, cfg *configstore.GovernanceConfig) {
				budget := fortyPercent("b-model")
				cfg.ModelConfigs = []configstoreTables.TableModelConfig{*buildModelConfig("mc1", "gpt-4o", nil, budget, nil)}
				cfg.Budgets = append(cfg.Budgets, *budget)
			},
		},
		{
			name: "a model config the key scoped to itself",
			build: func(vk *configstoreTables.TableVirtualKey, cfg *configstore.GovernanceConfig) {
				budget := fortyPercent("b-vk-model")
				cfg.ModelConfigs = []configstoreTables.TableModelConfig{
					*buildVKScopedModelConfig("mc-vk", "gpt-4o", nil, vk.ID, budget, nil),
				}
				cfg.Budgets = append(cfg.Budgets, *budget)
			},
		},
		{
			name: "the key's own budget",
			build: func(vk *configstoreTables.TableVirtualKey, cfg *configstore.GovernanceConfig) {
				budget := fortyPercent("b-key")
				vk.Budgets = []configstoreTables.TableBudget{*budget}
				cfg.Budgets = append(cfg.Budgets, *budget)
			},
		},
		{
			name: "the key's provider config",
			build: func(vk *configstoreTables.TableVirtualKey, cfg *configstore.GovernanceConfig) {
				budget := fortyPercent("b-key-openai")
				vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
					buildProviderConfigWithBudgets("openai", []string{"*"}, []configstoreTables.TableBudget{*budget}),
				}
				cfg.Budgets = append(cfg.Budgets, *budget)
			},
		},
		{
			name: "the team the key belongs to",
			build: func(vk *configstoreTables.TableVirtualKey, cfg *configstore.GovernanceConfig) {
				budget := fortyPercent("b-team")
				team := buildTeam("team1", "Team 1", budget)
				vk.TeamID = &team.ID
				vk.Team = team
				cfg.Teams = []configstoreTables.TableTeam{*team}
				cfg.Budgets = append(cfg.Budgets, *budget)
			},
		},
		{
			name: "the customer above that team",
			build: func(vk *configstoreTables.TableVirtualKey, cfg *configstore.GovernanceConfig) {
				budget := fortyPercent("b-customer")
				customer := buildCustomer("cust1", "Customer 1", budget)
				team := buildTeam("team1", "Team 1", nil)
				team.CustomerID = &customer.ID
				team.Customer = customer
				vk.TeamID = &team.ID
				vk.Team = team
				cfg.Teams = []configstoreTables.TableTeam{*team}
				cfg.Customers = []configstoreTables.TableCustomer{*customer}
				cfg.Budgets = append(cfg.Budgets, *budget)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newStore(t, tc.build)

			status := store.GetBudgetAndRateLimitStatus(resolverCtx(store, vkValue), schemas.OpenAI, "gpt-4o", nil, nil, nil)

			require.NotNil(t, status)
			assert.Equal(t, 40.0, status.BudgetPercentUsed,
				"this source is not reached, so routing would prefer a provider that is out of money")
		})
	}

	t.Run("the tightest constraint is what routing sees", func(t *testing.T) {
		// Not a sum and not the last one read: a request is refused by whichever limit is closest to its
		// cap, so that is the number a rule preferring a provider with room has to be given.
		store := newStore(t, func(vk *configstoreTables.TableVirtualKey, cfg *configstore.GovernanceConfig) {
			loose := buildBudgetWithUsage("b-provider", 100.0, 10.0, "1d")
			tight := buildBudgetWithUsage("b-key", 100.0, 90.0, "1d")
			cfg.Providers = []configstoreTables.TableProvider{*buildProviderWithGovernance("openai", loose, nil)}
			vk.Budgets = []configstoreTables.TableBudget{*tight}
			cfg.Budgets = append(cfg.Budgets, *loose, *tight)
		})

		status := store.GetBudgetAndRateLimitStatus(resolverCtx(store, vkValue), schemas.OpenAI, "gpt-4o", nil, nil, nil)

		assert.Equal(t, 90.0, status.BudgetPercentUsed)
	})
}

// TestModelConfigScopesForIgnoresExtraScopedIDsResolvers pins #6724's contract: one permit selects
// exactly one scope, and registered resolvers are not folded in here. Callers wanting an extra
// scope enforced ask for it by name via ScopedModelLimits / ProviderScopedModelLimitsInScope.
func TestModelConfigScopesForIgnoresExtraScopedIDsResolvers(t *testing.T) {
	extraScopedIDsResolversMu.Lock()
	saved := extraScopedIDsResolvers
	extraScopedIDsResolvers = nil
	extraScopedIDsResolversMu.Unlock()
	t.Cleanup(func() {
		extraScopedIDsResolversMu.Lock()
		extraScopedIDsResolvers = saved
		extraScopedIDsResolversMu.Unlock()
	})

	RegisterExtraScopedIDsResolver(func(_ context.Context, _, _ string) []ScopedID {
		return []ScopedID{
			{Scope: "batch_only", ScopeID: "b-1"}, // Kind deliberately empty
			{Scope: "with_kind", ScopeID: "w-1", Kind: grant.LimitHolderModelConfig},
		}
	})

	scopes := modelConfigScopesFor(nil)

	require.Len(t, scopes, 1, "a nil permit selects the deployment's global scope and nothing else")
	assert.Equal(t, configstoreTables.ModelConfigScopeGlobal, scopes[0].name)
	assert.Equal(t, grant.LimitHolderModelConfig, scopes[0].kind)
	for _, s := range scopes {
		assert.NotEqual(t, "batch_only", s.name, "a resolver-supplied scope must not reach request-time enforcement here")
		assert.NotEqual(t, "with_kind", s.name, "a resolver-supplied scope must not reach request-time enforcement here")
	}
}

// With no guard registered - every OSS build - nothing is governed and nothing is refused, whatever
// is asked about.
func TestLegacyLimitsGovernedByAnswersNothingWhenUnregistered(t *testing.T) {
	governedBy, err := LegacyLimitsGovernedBy(context.Background(), LegacyLimitHolderTeam, "team-1")
	require.NoError(t, err)
	assert.Empty(t, governedBy)
}

// A registered guard is asked about the entity in hand, and its answer - a name, nothing, or a
// failure - is what callers act on.
func TestLegacyLimitsGovernedByUsesTheRegisteredGuard(t *testing.T) {
	t.Cleanup(func() { RegisterLegacyLimitGuard(nil) })

	var askedKind, askedID string
	RegisterLegacyLimitGuard(func(_ context.Context, holderKind, holderID string) (string, error) {
		askedKind, askedID = holderKind, holderID
		if holderID == "customer-governed" {
			return "platform-access", nil
		}
		if holderID == "customer-unknowable" {
			return "", errors.New("database unavailable")
		}
		return "", nil
	})

	governedBy, err := LegacyLimitsGovernedBy(context.Background(), LegacyLimitHolderCustomer, "customer-governed")
	require.NoError(t, err)
	assert.Equal(t, "platform-access", governedBy)
	assert.Equal(t, LegacyLimitHolderCustomer, askedKind)
	assert.Equal(t, "customer-governed", askedID)

	governedBy, err = LegacyLimitsGovernedBy(context.Background(), LegacyLimitHolderTeam, "team-free")
	require.NoError(t, err)
	assert.Empty(t, governedBy, "an entity nothing governs keeps its own budgets")

	_, err = LegacyLimitsGovernedBy(context.Background(), LegacyLimitHolderCustomer, "customer-unknowable")
	require.Error(t, err, "an unanswerable question is an error, not a silent yes")

	// An empty id is nobody: the guard is not asked, so a caller with no owner in hand writes as usual.
	askedID = ""
	governedBy, err = LegacyLimitsGovernedBy(context.Background(), LegacyLimitHolderTeam, "")
	require.NoError(t, err)
	assert.Empty(t, governedBy)
	assert.Empty(t, askedID, "the guard must not be asked about an entity with no id")
}

// modelKeyStore builds a store over the given model configs with the offline pricing catalog, so
// base-model aliasing ("gpt-4o-2024-08-06" is a "gpt-4o") is live the way it is in a deployment. It
// returns the harness the rows live in too, for tests that change them.
func modelKeyStore(t *testing.T, mcs ...*configstoreTables.TableModelConfig) (*LocalGovernanceStore, *liveHarness) {
	t.Helper()
	cfg := &configstore.GovernanceConfig{}
	for _, mc := range mcs {
		cfg.ModelConfigs = append(cfg.ModelConfigs, *mc)
		cfg.Budgets = append(cfg.Budgets, mc.Budgets...)
	}
	h := newLiveHarness(t, cfg, nil)
	return h.mustGovernanceStore(NewMockLogger(), newOfflinePricingCatalog(t)), h
}

// Every spelling of a model the gateway accepts lands on the same provider-scoped (model, provider)
// limit: a budget on (gpt-4o, openai) governs the dated alias, the prefixed form, the re-cased form
// and the padded form alike. A sibling model and another provider stay out of it.
func TestGovernanceStore_ProviderScopedModelLimitMatchesEverySpelling(t *testing.T) {
	ctx := context.Background()
	providerName := "openai"
	exhausted := buildBudgetWithUsage("b-gpt4o-openai", 100.0, 100.0, "1d")
	store, _ := modelKeyStore(t, buildModelConfig("mc-gpt4o-openai", "gpt-4o", &providerName, exhausted, nil))

	for _, model := range []string{
		"gpt-4o",
		"openai/gpt-4o",
		"GPT-4O",
		" gpt-4o ",
		"gpt-4o-2024-08-06",
		"openai/GPT-4o-2024-08-06 ",
	} {
		t.Run(model, func(t *testing.T) {
			_, err := checkDeploymentBudgets(store, ctx, schemas.OpenAI, model, nil)
			require.Error(t, err, "request for %q must be governed by the (gpt-4o, openai) budget", model)
			assert.Contains(t, err.Error(), "budget exceeded")
		})
	}

	for _, model := range []string{"gpt-4o-mini", "gpt-4o-mini-2024-07-18", "gpt-4"} {
		_, err := checkDeploymentBudgets(store, ctx, schemas.OpenAI, model, nil)
		assert.NoError(t, err, "sibling %q must not be charged to the gpt-4o budget", model)
	}
	_, err := checkDeploymentBudgets(store, ctx, schemas.Anthropic, "gpt-4o-2024-08-06", nil)
	assert.NoError(t, err, "another provider must not be governed by the openai-scoped budget")
}

// ModelConfigGoverns answers what a request-time limit lookup would: a gpt-4o config governs the
// dated model on its provider (and on every provider when it names none), not a sibling model or
// another provider, and an alias reaches the limit of the model the caller's usable keys map it to.
func TestGovernanceStore_ModelConfigGovernsMatchesRequestLookup(t *testing.T) {
	store, h := modelKeyStore(t)
	openai := "openai"
	all := schemas.WhiteList{"*"}
	assert.True(t, store.ModelConfigGoverns("gpt-4o", &openai, schemas.OpenAI, "gpt-4o-2024-08-06", all))
	assert.True(t, store.ModelConfigGoverns("gpt-4o", nil, schemas.Azure, "gpt-4o-2024-08-06", all))
	assert.False(t, store.ModelConfigGoverns("gpt-4o", &openai, schemas.OpenAI, "gpt-4o-mini", all))
	assert.False(t, store.ModelConfigGoverns("gpt-4o", &openai, schemas.Anthropic, "gpt-4o", all))

	// Two keys map the alias to different models; only the keys the caller may use count.
	bedrock := "bedrock"
	const sonnet, opus = "us.anthropic.claude-sonnet-4-6", "us.anthropic.claude-opus-4-6"
	h.setProviders(map[schemas.ModelProvider]configstore.ProviderConfig{schemas.Bedrock: {}}, map[schemas.ModelProvider][]schemas.Key{
		schemas.Bedrock: {
			{ID: "key-sonnet", Models: all, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: sonnet}}},
			{ID: "key-opus", Models: all, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: opus}}},
		},
	})
	assert.True(t, store.ModelConfigGoverns(sonnet, &bedrock, schemas.Bedrock, "claude-sonnet", all))
	assert.True(t, store.ModelConfigGoverns(opus, &bedrock, schemas.Bedrock, "claude-sonnet", all))
	assert.True(t, store.ModelConfigGoverns(sonnet, &bedrock, schemas.Bedrock, "claude-sonnet", schemas.WhiteList{"key-sonnet"}))
	assert.False(t, store.ModelConfigGoverns(opus, &bedrock, schemas.Bedrock, "claude-sonnet", schemas.WhiteList{"key-sonnet"}))
	assert.False(t, store.ModelConfigGoverns(opus, &bedrock, schemas.Bedrock, "claude-sonnet", nil))
}

// ModelAccessAllows answers what inference's model check does for a provider config: a key alias
// the usable keys map to an allowed model is allowed though the list does not name it, one only an
// unusable key maps is not, and an alias whose target the config blacklists is refused.
func TestGovernanceStore_ModelAccessAllowsKeyAliases(t *testing.T) {
	store, h := modelKeyStore(t)
	all := schemas.WhiteList{"*"}
	h.setProviders(map[schemas.ModelProvider]configstore.ProviderConfig{schemas.OpenAI: {}}, map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {{ID: "key-1", Models: all, Aliases: schemas.KeyAliases{"friendly": {ModelID: "gpt-4o"}}}},
	})
	assert.True(t, store.ModelAccessAllows(schemas.OpenAI, "gpt-4o", schemas.WhiteList{"gpt-4o"}, nil, all))
	assert.True(t, store.ModelAccessAllows(schemas.OpenAI, "friendly", schemas.WhiteList{"gpt-4o"}, nil, all))
	assert.True(t, store.ModelAccessAllows(schemas.OpenAI, "friendly", schemas.WhiteList{"gpt-4o"}, nil, schemas.WhiteList{"key-1"}))
	assert.False(t, store.ModelAccessAllows(schemas.OpenAI, "friendly", schemas.WhiteList{"gpt-4o"}, nil, schemas.WhiteList{"key-2"}))
	assert.False(t, store.ModelAccessAllows(schemas.OpenAI, "friendly", schemas.WhiteList{"gpt-4o-mini"}, nil, all))
	assert.False(t, store.ModelAccessAllows(schemas.OpenAI, "friendly", schemas.WhiteList{"gpt-4o", "friendly"}, schemas.BlackList{"gpt-4o"}, all))
	assert.True(t, store.ModelAccessAllows(schemas.OpenAI, "friendly", schemas.WhiteList{"gpt-4o", "friendly"}, schemas.BlackList{"gpt-4o"}, schemas.WhiteList{"key-2"}))
	assert.False(t, store.ModelAccessAllows(schemas.OpenAI, "gpt-4o", schemas.WhiteList{"gpt-4o"}, schemas.BlackList{"gpt-4o"}, all))
}

// The spelling an operator stores a provider-scoped config under is canonicalised the same way a
// request is, on both the load path and the refresh path that follows a write, so existing rows keep
// matching without a migration.
func TestGovernanceStore_ProviderScopedModelConfigStoredUnderCanonicalKey(t *testing.T) {
	ctx := context.Background()
	providerName := "openai"
	for _, stored := range []string{"gpt-4o", "openai/gpt-4o", "GPT-4O", " gpt-4o "} {
		t.Run("loaded:"+stored, func(t *testing.T) {
			exhausted := buildBudgetWithUsage("b-"+stored, 100.0, 100.0, "1d")
			store, _ := modelKeyStore(t, buildModelConfig("mc-"+stored, stored, &providerName, exhausted, nil))
			_, err := checkDeploymentBudgets(store, ctx, schemas.OpenAI, "gpt-4o-2024-08-06", nil)
			require.Error(t, err, "config stored as %q must govern gpt-4o-2024-08-06", stored)
		})
		t.Run("created:"+stored, func(t *testing.T) {
			store, h := modelKeyStore(t)
			exhausted := buildBudgetWithUsage("b-"+stored, 100.0, 100.0, "1d")
			h.putModelConfig(buildModelConfig("mc-"+stored, stored, &providerName, exhausted, nil))
			_, err := checkDeploymentBudgets(store, ctx, schemas.OpenAI, "gpt-4o-2024-08-06", nil)
			require.Error(t, err, "config created as %q must govern gpt-4o-2024-08-06", stored)
		})
	}
}

// Renaming or re-scoping a config must leave no entry behind under its previous key: a request for
// the old model would otherwise keep consuming the moved config's budget.
func TestGovernanceStore_UpdateModelConfigInMemoryEvictsPreviousKey(t *testing.T) {
	ctx := context.Background()
	openai := "openai"
	exhausted := buildBudgetWithUsage("b-moved", 100.0, 100.0, "1d")
	store, h := modelKeyStore(t, buildModelConfig("mc-moved", "gpt-4o", &openai, exhausted, nil))
	_, err := checkDeploymentBudgets(store, ctx, schemas.OpenAI, "gpt-4o", nil)
	require.Error(t, err, "precondition: the config governs gpt-4o before the rename")

	moved := buildModelConfig("mc-moved", "gpt-4o-mini", &openai, exhausted, nil)
	h.putModelConfig(moved)

	_, err = checkDeploymentBudgets(store, ctx, schemas.OpenAI, "gpt-4o-mini", nil)
	require.Error(t, err, "the renamed config must govern gpt-4o-mini")
	_, err = checkDeploymentBudgets(store, ctx, schemas.OpenAI, "gpt-4o", nil)
	require.NoError(t, err, "the previous key must no longer resolve to the moved config")

	azure := "azure"
	h.putModelConfig(buildModelConfig("mc-moved", "gpt-4o-mini", &azure, exhausted, nil))
	_, err = checkDeploymentBudgets(store, ctx, schemas.OpenAI, "gpt-4o-mini", nil)
	require.NoError(t, err, "re-scoping to another provider must evict the openai entry")
	_, err = checkDeploymentBudgets(store, ctx, schemas.Azure, "gpt-4o-mini", nil)
	require.Error(t, err, "the re-scoped config must govern azure")
}

// A provider-less config keeps matching the same set of spellings it did before, now with the
// prefixed and re-cased forms included.
func TestGovernanceStore_ProviderlessModelLimitMatchesEverySpelling(t *testing.T) {
	ctx := context.Background()
	exhausted := buildBudgetWithUsage("b-gpt4o-any", 100.0, 100.0, "1d")
	store, _ := modelKeyStore(t, buildModelConfig("mc-gpt4o-any", "gpt-4o", nil, exhausted, nil))

	for _, model := range []string{"gpt-4o", "openai/gpt-4o", "GPT-4O", "gpt-4o-2024-08-06"} {
		for _, provider := range []schemas.ModelProvider{schemas.OpenAI, schemas.Azure} {
			_, err := checkDeploymentBudgets(store, ctx, provider, model, nil)
			require.Error(t, err, "%q on %s must be governed by the provider-less gpt-4o budget", model, provider)
		}
	}
	_, err := checkDeploymentBudgets(store, ctx, schemas.OpenAI, "gpt-4o-mini", nil)
	assert.NoError(t, err)
}

// collectModelConfigsFor returns every applicable config most specific first: the exact pair, the
// base-model pair, the exact model on any provider, the base model on any provider, then the two
// wildcards. A request never satisfies a looser bucket while a tighter one exists for it.
func TestGovernanceStore_CollectModelConfigsForPrecedence(t *testing.T) {
	ctx := context.Background()
	providerName := "openai"
	mk := func(id, model string, provider *string) *configstoreTables.TableModelConfig {
		return buildModelConfig(id, model, provider, buildBudget("b-"+id, 100.0, "1d"), nil)
	}
	store, _ := modelKeyStore(t,
		mk("exact-pair", "gpt-4o-2024-08-06", &providerName),
		mk("base-pair", "gpt-4o", &providerName),
		mk("exact-any", "gpt-4o-2024-08-06", nil),
		mk("base-any", "gpt-4o", nil),
		mk("all-openai", configstoreTables.ModelConfigAllModels, &providerName),
		mk("all-any", configstoreTables.ModelConfigAllModels, nil),
	)
	ids := func(mcs []*registry.ModelConfig) []string {
		out := make([]string, 0, len(mcs))
		for _, mc := range mcs {
			out = append(out, mc.ID)
		}
		return out
	}
	global := configstoreTables.ModelConfigScopeGlobal

	// The dated alias on openai reaches every tier. "exact-any" shares its key with "base-any"
	// (provider-less configs collapse to the base model), so whichever was indexed last answers.
	got := ids(store.collectModelConfigsFor(ctx, global, "", "openai/GPT-4o-2024-08-06", &providerName, nil))
	require.Len(t, got, 5)
	assert.Equal(t, []string{"exact-pair", "base-pair"}, got[:2])
	assert.Contains(t, []string{"exact-any", "base-any"}, got[2])
	assert.Equal(t, []string{"all-openai", "all-any"}, got[3:])

	// The base name skips the dated pair but still finds its own.
	got = ids(store.collectModelConfigsFor(ctx, global, "", "gpt-4o", &providerName, nil))
	require.Len(t, got, 4)
	assert.Equal(t, "base-pair", got[0])
	assert.Equal(t, []string{"all-openai", "all-any"}, got[2:])

	// A sibling model only reaches the wildcards.
	assert.Equal(t, []string{"all-openai", "all-any"}, ids(store.collectModelConfigsFor(ctx, global, "", "gpt-4o-mini", &providerName, nil)))

	// Another provider skips the openai-scoped tiers entirely.
	anthropic := "anthropic"
	got = ids(store.collectModelConfigsFor(ctx, global, "", "gpt-4o-2024-08-06", &anthropic, nil))
	assert.NotContains(t, got, "exact-pair")
	assert.NotContains(t, got, "base-pair")
	assert.NotContains(t, got, "all-openai")
	assert.Contains(t, got, "all-any")
}

// A key alias names a model the keys resolve only after governance has run, so a limit set on the
// model the alias stands for has to reach a request that names the alias. Targets the keys
// disagree on all apply; a key that key selection never picks for the name does not count.
func TestGovernanceStore_CollectModelConfigsForKeyAliases(t *testing.T) {
	ctx := context.Background()
	provider := "bedrock"
	const sonnet, opus = "us.anthropic.claude-sonnet-4-6", "us.anthropic.claude-opus-4-6"
	anyModel := schemas.WhiteList{"*"}
	mk := func(id, model string) *configstoreTables.TableModelConfig {
		return buildModelConfig(id, model, &provider, buildBudget("b-"+id, 100.0, "1d"), nil)
	}
	ids := func(mcs []*registry.ModelConfig) []string {
		out := make([]string, 0, len(mcs))
		for _, mc := range mcs {
			out = append(out, mc.ID)
		}
		return out
	}
	global := configstoreTables.ModelConfigScopeGlobal
	// withKeys takes the subtest's t, so a fixture failure is reported by the subtest that hit it.
	withKeys := func(t *testing.T, keys ...schemas.Key) *LocalGovernanceStore {
		store, h := modelKeyStore(t, mk("sonnet-limit", sonnet), mk("opus-limit", opus))
		h.setProviders(map[schemas.ModelProvider]configstore.ProviderConfig{schemas.Bedrock: {}}, map[schemas.ModelProvider][]schemas.Key{schemas.Bedrock: keys})
		return store
	}
	off := false
	// collect resolves the targets the way every entry point does, then walks the global scope.
	collect := func(store *LocalGovernanceStore, model string, prov *string) []*registry.ModelConfig {
		return store.collectModelConfigsFor(ctx, global, "", model, prov, store.aliasTargets(ctx, prov, model))
	}

	t.Run("the alias reaches the limit of the model it stands for", func(t *testing.T) {
		store := withKeys(t, schemas.Key{Models: anyModel, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: sonnet}}})
		assert.Equal(t, []string{"sonnet-limit"}, ids(collect(store, "claude-sonnet", &provider)))
		assert.Equal(t, []string{"sonnet-limit"}, ids(collect(store, sonnet, &provider)))
	})
	t.Run("keys that disagree apply every target", func(t *testing.T) {
		store := withKeys(t,
			schemas.Key{Models: anyModel, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: sonnet}}},
			schemas.Key{Models: anyModel, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: opus}}},
		)
		assert.ElementsMatch(t, []string{"sonnet-limit", "opus-limit"}, ids(collect(store, "claude-sonnet", &provider)))
	})
	t.Run("a key that cannot serve the name contributes nothing", func(t *testing.T) {
		store := withKeys(t,
			schemas.Key{Models: schemas.WhiteList{"titan-*"}, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: sonnet}}},
			schemas.Key{Models: anyModel, Enabled: &off, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: opus}}},
		)
		assert.Empty(t, collect(store, "claude-sonnet", &provider))
	})
	t.Run("no aliases leaves the lookup as it was", func(t *testing.T) {
		store := withKeys(t, schemas.Key{Models: anyModel})
		assert.Empty(t, collect(store, "claude-sonnet", &provider))
	})
	t.Run("a lookup without a provider cannot resolve an alias", func(t *testing.T) {
		store := withKeys(t, schemas.Key{Models: anyModel, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: sonnet}}})
		assert.Empty(t, collect(store, "claude-sonnet", nil))
	})

	t.Run("the real entry point finds the limit through the alias", func(t *testing.T) {
		store := withKeys(t, schemas.Key{Models: anyModel, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: sonnet}}})
		budgets, _ := store.GlobalModelLimits(ctx, schemas.Bedrock, "claude-sonnet")
		require.Len(t, budgets, 1)
		assert.Equal(t, "b-sonnet-limit", budgets[0].ID)
	})
	t.Run("two keys that agree yield each limit once, and the alias name's own limit stays", func(t *testing.T) {
		store := withKeys(t,
			schemas.Key{Models: anyModel, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: sonnet}}},
			schemas.Key{Models: anyModel, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: sonnet}}},
		)
		assert.Equal(t, []string{"sonnet-limit"}, ids(collect(store, "claude-sonnet", &provider)))
		assert.Equal(t, []string{sonnet}, store.aliasTargets(ctx, &provider, "claude-sonnet"))

		store, h := modelKeyStore(t, mk("sonnet-limit", sonnet), mk("alias-limit", "claude-sonnet"))
		h.setProviders(map[schemas.ModelProvider]configstore.ProviderConfig{schemas.Bedrock: {}}, map[schemas.ModelProvider][]schemas.Key{
			schemas.Bedrock: {{Models: anyModel, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: sonnet}}}},
		})
		assert.Equal(t, []string{"alias-limit", "sonnet-limit"}, ids(collect(store, "claude-sonnet", &provider)))
	})
	t.Run("a provider-less config on the target is found", func(t *testing.T) {
		store, h := modelKeyStore(t, buildModelConfig("any-provider", sonnet, nil, buildBudget("b-any", 100.0, "1d"), nil))
		h.setProviders(map[schemas.ModelProvider]configstore.ProviderConfig{schemas.Bedrock: {}}, map[schemas.ModelProvider][]schemas.Key{
			schemas.Bedrock: {{Models: anyModel, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: sonnet}}}},
		})
		assert.Equal(t, []string{"any-provider"}, ids(collect(store, "claude-sonnet", &provider)))
	})
	t.Run("a virtual-key-scoped config on the target is found when charging", func(t *testing.T) {
		scoped := mk("vk-sonnet", sonnet)
		scoped.Scope = configstoreTables.ModelConfigScopeVirtualKey
		vkID := "vk-1"
		scoped.ScopeID = &vkID
		store, h := modelKeyStore(t, scoped)
		h.setProviders(map[schemas.ModelProvider]configstore.ProviderConfig{schemas.Bedrock: {}}, map[schemas.ModelProvider][]schemas.Key{
			schemas.Bedrock: {{Models: anyModel, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: sonnet}}}},
		})
		budgetIDs, _ := store.CollectModelScopedGovernanceIDs(ctx, vkID, "", schemas.Bedrock, "claude-sonnet")
		assert.Equal(t, []string{"b-vk-sonnet"}, budgetIDs)
	})
	// The live store always holds the configured providers; a provider it holds no record of is the
	// store that cannot see that provider's config.
	t.Run("a provider with no record leaves the lookup as it was", func(t *testing.T) {
		store, h := modelKeyStore(t, mk("sonnet-limit", sonnet), mk("opus-limit", opus))
		h.setProviders(map[schemas.ModelProvider]configstore.ProviderConfig{schemas.Bedrock: {}}, map[schemas.ModelProvider][]schemas.Key{schemas.Bedrock: {{Models: anyModel, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: sonnet}}}}})
		h.deleteProvider(schemas.Bedrock)
		assert.Empty(t, collect(store, "claude-sonnet", &provider))
		assert.Nil(t, store.aliasTargets(ctx, &provider, "claude-sonnet"))
	})
	t.Run("a provider missing from the live store adds nothing", func(t *testing.T) {
		store := withKeys(t, schemas.Key{Models: anyModel, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: sonnet}}})
		other := "openai"
		assert.Empty(t, collect(store, "claude-sonnet", &other))
	})
	// Core serves a pinned request from the pinned key alone (a routing-rule pin first, then the
	// caller's id, then the caller's name), so only that key's alias decides what the name becomes.
	t.Run("a pinned key contributes only its own target", func(t *testing.T) {
		store := withKeys(t,
			schemas.Key{ID: "k-alias", Name: "alias-key", Models: anyModel, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: sonnet}}},
			schemas.Key{ID: "k-plain", Name: "plain-key", Models: anyModel},
		)
		pinned := func(key schemas.BifrostContextKey, value string) context.Context {
			return context.WithValue(ctx, key, value)
		}
		assert.Equal(t, []string{sonnet}, store.aliasTargets(ctx, &provider, "claude-sonnet"), "unpinned: every eligible key's target")
		assert.Nil(t, store.aliasTargets(pinned(schemas.BifrostContextKeyAPIKeyID, "k-plain"), &provider, "claude-sonnet"))
		assert.Nil(t, store.aliasTargets(pinned(schemas.BifrostContextKeyAPIKeyName, "plain-key"), &provider, "claude-sonnet"))
		assert.Equal(t, []string{sonnet}, store.aliasTargets(pinned(schemas.BifrostContextKeyAPIKeyID, "k-alias"), &provider, "claude-sonnet"))
		assert.Nil(t, store.aliasTargets(pinned(schemas.BifrostContextKeyAPIKeyID, "k-missing"), &provider, "claude-sonnet"), "core refuses an unknown pin, so nothing is added")

		routed := context.WithValue(pinned(schemas.BifrostContextKeyAPIKeyID, "k-alias"), schemas.BifrostContextKeyRoutingPinnedAPIKeyID, "k-plain")
		assert.Nil(t, store.aliasTargets(routed, &provider, "claude-sonnet"), "a routing-rule pin overrides the caller's pin")
		assert.Empty(t, ids(store.collectModelConfigsFor(routed, global, "", "claude-sonnet", &provider, store.aliasTargets(routed, &provider, "claude-sonnet"))))
	})

	// A model that runs at the provider exactly as written is never resolved through a key alias,
	// so the limits of the alias's target must not be added to it.
	t.Run("a model that runs as written takes no alias target's limits", func(t *testing.T) {
		store := withKeys(t, schemas.Key{Models: anyModel, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: sonnet}}})
		bifrostCtx := func(key, value any) *schemas.BifrostContext {
			c := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			c.SetValue(key, value)
			return c
		}
		assert.Equal(t, []string{sonnet}, store.aliasTargets(ctx, &provider, "claude-sonnet"), "an ordinary request is resolved through the alias")
		for name, marked := range map[string]context.Context{
			"literal marker":               context.WithValue(ctx, literalLimitsContextKey, true),
			"literal on a bifrost context": bifrostCtx(literalLimitsContextKey, true),
			"direct key":                   bifrostCtx(schemas.BifrostContextKeyDirectKey, schemas.Key{ID: "direct"}),
			"skip key selection":           bifrostCtx(schemas.BifrostContextKeySkipKeySelection, true),
		} {
			assert.Nil(t, store.aliasTargets(marked, &provider, "claude-sonnet"), name)
			assert.Empty(t, ids(store.collectModelConfigsFor(marked, global, "", "claude-sonnet", &provider, store.aliasTargets(marked, &provider, "claude-sonnet"))), name)
		}
		assert.Equal(t, []string{sonnet}, store.aliasTargets(bifrostCtx(literalLimitsContextKey, false), &provider, "claude-sonnet"), "an unmarked literal flag leaves the alias in force")
	})
	t.Run("an exhausted limit on the alias target does not reach the aliased name when it runs as written", func(t *testing.T) {
		sonnetCfg := buildModelConfig("sonnet-cfg", "claude-sonnet-4-6", &provider, buildBudget("b-sonnet-cfg", 100.0, "1d"), nil)
		store, h := modelKeyStore(t, sonnetCfg)
		h.setProviders(map[schemas.ModelProvider]configstore.ProviderConfig{schemas.Bedrock: {}}, map[schemas.ModelProvider][]schemas.Key{
			schemas.Bedrock: {{Models: anyModel, Aliases: schemas.KeyAliases{"claude-opus-4-6": {ModelID: "claude-sonnet-4-6"}}}},
		})
		normal := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		literal := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		literal.SetValue(literalLimitsContextKey, true)

		budgets, _, err := store.GatherLimits(normal, nil, schemas.Bedrock, "claude-opus-4-6")
		require.NoError(t, err)
		require.Len(t, budgets, 1, "the alias is resolved, so the target's limit applies")
		budgets, _, err = store.GatherLimits(literal, nil, schemas.Bedrock, "claude-opus-4-6")
		require.NoError(t, err)
		assert.Empty(t, budgets, "the name runs as written, so the target's limit does not apply")

		sonnetBudgets, _ := store.CollectModelScopedGovernanceIDs(normal, "", "", schemas.Bedrock, "claude-opus-4-6")
		assert.Equal(t, []string{"b-sonnet-cfg"}, sonnetBudgets)
		sonnetBudgets, _ = store.CollectModelScopedGovernanceIDs(literal, "", "", schemas.Bedrock, "claude-opus-4-6")
		assert.Empty(t, sonnetBudgets)
	})
}

// Resolving what an alias stands for is done once per attempt and once per charge however many
// scopes and permits the walks cover, and every walk finds the target's limit in its own scope. The
// provider read is a lock-free live-store lookup, so how many reads a walk makes is not observable
// here; the limits each walk finds are.
func TestGovernanceStore_KeyAliasTargetsResolvedOncePerAttempt(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	provider := "bedrock"
	const sonnet = "us.anthropic.claude-sonnet-4-6"
	mk := func(id, model, scope, scopeID string) *configstoreTables.TableModelConfig {
		mc := buildModelConfig(id, model, &provider, buildBudget("b-"+id, 100.0, "1d"), nil)
		mc.Scope = scope
		if scopeID != "" {
			mc.ScopeID = &scopeID
		}
		return mc
	}
	store, h := modelKeyStore(t,
		mk("global-sonnet", sonnet, configstoreTables.ModelConfigScopeGlobal, ""),
		mk("vk-a-sonnet", sonnet, configstoreTables.ModelConfigScopeVirtualKey, "vk-a"),
		mk("vk-b-sonnet", sonnet, configstoreTables.ModelConfigScopeVirtualKey, "vk-b"),
	)
	h.setProviders(map[schemas.ModelProvider]configstore.ProviderConfig{schemas.Bedrock: {}}, map[schemas.ModelProvider][]schemas.Key{
		schemas.Bedrock: {{Models: schemas.WhiteList{"*"}, Aliases: schemas.KeyAliases{"claude-sonnet": {ModelID: sonnet}}}},
	})

	t.Run("GatherLimits with no access", func(t *testing.T) {
		budgets, _, err := store.GatherLimits(ctx, nil, schemas.Bedrock, "claude-sonnet")
		require.NoError(t, err)
		require.Len(t, budgets, 1)
	})
	t.Run("GatherLimits with two permits", func(t *testing.T) {
		access := grant.NewAccess([]schemas.Permit{
			permitWithProviders(grant.PermitVirtualKey, "vk-a", "a", "bedrock"),
			permitWithProviders(grant.PermitVirtualKey, "vk-b", "b", "bedrock"),
		}, "", nil, "", nil)
		budgets, _, err := store.GatherLimits(ctx, access, schemas.Bedrock, "claude-sonnet")
		require.NoError(t, err)
		var got []string
		for _, b := range budgets {
			got = append(got, b.ID)
		}
		assert.ElementsMatch(t, []string{"b-global-sonnet", "b-vk-a-sonnet", "b-vk-b-sonnet"}, got)
	})
	t.Run("CollectModelScopedGovernanceIDs", func(t *testing.T) {
		budgetIDs, _ := store.CollectModelScopedGovernanceIDs(ctx, "vk-a", "", schemas.Bedrock, "claude-sonnet")
		assert.ElementsMatch(t, []string{"b-global-sonnet", "b-vk-a-sonnet"}, budgetIDs)
	})
}

// BenchmarkGatherLimits_KeyAliases measures GatherLimits for a name that is, and is not, a key
// alias, with the provider read from the live store as in production.
func BenchmarkGatherLimits_KeyAliases(b *testing.B) {
	provider := "bedrock"
	// newStore builds a harness for one sub-benchmark on its own b, so a fixture failure is reported
	// by that sub-benchmark and its store is closed when it ends.
	newStore := func(b *testing.B) (*liveHarness, *LocalGovernanceStore) {
		mc := buildModelConfig("sonnet-limit", "us.anthropic.claude-sonnet-4-6", &provider, buildBudget("b-sonnet-limit", 100.0, "1d"), nil)
		cfg := &configstore.GovernanceConfig{ModelConfigs: []configstoreTables.TableModelConfig{*mc}, Budgets: mc.Budgets}
		h := newLiveHarness(b, cfg, nil)
		return h, h.mustGovernanceStore(NewMockLogger(), nil)
	}
	aliased := schemas.KeyAliases{}
	for a := 0; a < 20; a++ {
		aliased[fmt.Sprintf("alias-%d", a)] = schemas.AliasConfig{ModelID: fmt.Sprintf("us.vendor.model-%d", a)}
	}
	aliased["claude-sonnet"] = schemas.AliasConfig{ModelID: "us.anthropic.claude-sonnet-4-6"}
	plain := []schemas.Key{{Models: schemas.WhiteList{"*"}}, {Models: schemas.WhiteList{"*"}}}
	withAliases := []schemas.Key{{Models: schemas.WhiteList{"*"}, Aliases: aliased}, {Models: schemas.WhiteList{"*"}, Aliases: aliased}}
	withAliases[0].ID = "k-aliased"
	for _, tc := range []struct {
		name  string
		keys  []schemas.Key
		noMem bool
		model string
		want  int
		set   func(*schemas.BifrostContext)
	}{
		// baseline and no_aliases name the canonical model too, so every row does the same limit work and the
		// allocation difference between rows is the alias resolution alone.
		{"baseline_no_store", nil, true, "us.anthropic.claude-sonnet-4-6", 1, nil},
		{"no_aliases", plain, false, "us.anthropic.claude-sonnet-4-6", 1, nil},
		{"aliases_unused", withAliases, false, "us.anthropic.claude-sonnet-4-6", 1, nil},
		{"alias_used", withAliases, false, "claude-sonnet", 1, nil},
		// A pinned key resolves through that key alone; a direct key runs the name as written.
		{"alias_used_pinned_key", withAliases, false, "claude-sonnet", 1, func(ctx *schemas.BifrostContext) {
			ctx.SetValue(schemas.BifrostContextKeyAPIKeyID, "k-aliased")
		}},
		{"alias_direct_key", withAliases, false, "claude-sonnet", 0, func(ctx *schemas.BifrostContext) {
			ctx.SetValue(schemas.BifrostContextKeyDirectKey, schemas.Key{ID: "direct"})
		}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			h, store := newStore(b)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if tc.set != nil {
				tc.set(ctx)
			}
			if tc.noMem {
				h.setProviders(map[schemas.ModelProvider]configstore.ProviderConfig{schemas.Bedrock: {}}, nil)
				h.deleteProvider(schemas.Bedrock)
			} else {
				h.setProviders(map[schemas.ModelProvider]configstore.ProviderConfig{schemas.Bedrock: {}}, map[schemas.ModelProvider][]schemas.Key{schemas.Bedrock: tc.keys})
			}
			budgets, _, err := store.GatherLimits(ctx, nil, schemas.Bedrock, tc.model)
			if err != nil || len(budgets) != tc.want {
				b.Fatalf("%s: %d budgets (err %v), want %d", tc.name, len(budgets), err, tc.want)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _, _ = store.GatherLimits(ctx, nil, schemas.Bedrock, tc.model)
			}
		})
	}
}

// TestResolveAccessStampsVirtualKeyMetadata checks that resolving access publishes the key's
// metadata for the logging plugin, as a copy: a request that changes its copy must not reach the key
// every other request shares. A key without metadata stamps nothing.
func TestResolveAccessStampsVirtualKeyMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata map[string]string
	}{
		{name: "no metadata stamps nothing", metadata: nil},
		{name: "metadata is stamped", metadata: map[string]string{"cost_center": "cc-42", "owner": "a@example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vk := buildVKForMCPStamping(nil)
			vk.Metadata = tc.metadata

			logger := NewMockLogger()
			local, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
				VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
			}, nil, &liveFixture{})
			require.NoError(t, err)
			plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, local, nil, nil, nil, local.live)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })

			ctx := emptyCtx()
			ctx.Grant().SetIdentity(grant.NewIdentity(grant.NewCredential(grant.CredentialVirtualKey, mcpTestVKValue), nil, nil, nil, nil, nil, nil))
			_, err = plugin.ResolveAccess(ctx)
			require.NoError(t, err)
			require.Equal(t, vk.ID, ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID), "the key resolved")

			stamped := ctx.Value(VirtualKeyMetadataContextKey)
			if tc.metadata == nil {
				assert.Nil(t, stamped, "a key without metadata must leave the context key absent")
				return
			}
			got, ok := stamped.(map[string]string)
			require.True(t, ok, "stamped metadata must be a map[string]string, got %T", stamped)
			assert.Equal(t, tc.metadata, got)

			got["cost_center"] = "tampered"
			stored, ok := local.GetVirtualKey(ctx, mcpTestVKValue)
			require.True(t, ok)
			assert.Equal(t, "cc-42", stored.Metadata["cost_center"], "the stamped map must be a copy, not the stored key's map")
		})
	}
}

// dimensionRecorder records the trace dimensions governance writes and backs the root span with a
// real span, starting from the attributes the transport would have set.
type dimensionRecorder struct {
	schemas.NoOpTracer
	traceID   string
	traceDims map[string]string
	root      *schemas.Span
}

type rootHandle struct{}

func newDimensionRecorder(rootAttrs map[string]any) *dimensionRecorder {
	return &dimensionRecorder{root: &schemas.Span{SpanID: "root", Attributes: maps.Clone(rootAttrs)}}
}

func (r *dimensionRecorder) MergeTraceDimensions(traceID string, dims map[string]string) {
	r.traceID = traceID
	r.traceDims = dims
}

func (r *dimensionRecorder) GetSpanHandleByID(_ string, spanID *string) schemas.SpanHandle {
	if spanID != nil {
		return nil
	}
	return rootHandle{}
}

func (r *dimensionRecorder) SpanFromHandle(handle schemas.SpanHandle) *schemas.Span {
	if _, ok := handle.(rootHandle); ok {
		return r.root
	}
	return nil
}

func (r *dimensionRecorder) SetAttribute(handle schemas.SpanHandle, key string, value any) {
	r.SpanFromHandle(handle).SetAttribute(key, value)
}

// TestResolveAccessStampsVirtualKeyMetadataIntoDimensions checks that the key's metadata reaches
// the dimensions on the context, the trace and the root span, overriding a header dimension of the
// same name, without touching the header map. "path" and "method" stay out, as for headers. On the
// root span a key never replaces an attribute Bifrost set: metadata named after the content-logging
// flag must not turn content back on for connectors.
func TestResolveAccessStampsVirtualKeyMetadataIntoDimensions(t *testing.T) {
	headerRoot := map[string]any{"team": "header", "region": "eu", "http.request.method": "POST"}
	for _, tc := range []struct {
		name           string
		metadata       map[string]string
		disableContent bool
		wantCtx        map[string]string
		wantAdded      map[string]string
		wantRoot       map[string]any
	}{
		{
			name:     "no metadata leaves the dimensions alone",
			metadata: nil,
			wantCtx:  map[string]string{"team": "header", "region": "eu"},
			wantRoot: headerRoot,
		},
		{
			name:      "metadata is added and wins over a header",
			metadata:  map[string]string{"team": "vk", "cost_center": "cc-42", "path": "/vk", "method": "PUT"},
			wantCtx:   map[string]string{"team": "vk", "region": "eu", "cost_center": "cc-42"},
			wantAdded: map[string]string{"team": "vk", "cost_center": "cc-42"},
			wantRoot:  map[string]any{"team": "vk", "region": "eu", "http.request.method": "POST", "cost_center": "cc-42"},
		},
		{
			name:           "metadata never replaces an attribute bifrost set on the root span",
			metadata:       map[string]string{schemas.AttrBifrostContentLoggingDisabled: "false", "http.request.method": "GET", "cost_center": "cc-42"},
			disableContent: true,
			wantCtx:        map[string]string{"team": "header", "region": "eu", schemas.AttrBifrostContentLoggingDisabled: "false", "http.request.method": "GET", "cost_center": "cc-42"},
			wantAdded:      map[string]string{schemas.AttrBifrostContentLoggingDisabled: "false", "http.request.method": "GET", "cost_center": "cc-42"},
			wantRoot:       map[string]any{"team": "header", "region": "eu", "http.request.method": "POST", schemas.AttrBifrostContentLoggingDisabled: true, "cost_center": "cc-42"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vk := buildVKForMCPStamping(nil)
			vk.Metadata = tc.metadata
			if tc.disableContent {
				vk.DisableContentLogging = boolPtr(true)
			}

			logger := NewMockLogger()
			local, err := newTestGovernanceStore(t, logger, &configstore.GovernanceConfig{
				VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
			}, nil, &liveFixture{})
			require.NoError(t, err)
			plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, local, nil, nil, nil, local.live)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })

			recorder := newDimensionRecorder(headerRoot)
			headerDims := map[string]string{"team": "header", "region": "eu"}
			ctx := emptyCtx()
			ctx.SetValue(schemas.BifrostContextKeyTracer, recorder)
			ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-1")
			ctx.SetValue(schemas.BifrostContextKeyDimensions, headerDims)
			ctx.Grant().SetIdentity(grant.NewIdentity(grant.NewCredential(grant.CredentialVirtualKey, mcpTestVKValue), nil, nil, nil, nil, nil, nil))
			_, err = plugin.ResolveAccess(ctx)
			require.NoError(t, err)

			assert.Equal(t, tc.wantCtx, ctx.Value(schemas.BifrostContextKeyDimensions))
			assert.Equal(t, map[string]string{"team": "header", "region": "eu"}, headerDims, "the header map must not be changed in place")
			assert.Equal(t, tc.wantRoot, recorder.root.Attributes)
			assert.Equal(t, tc.disableContent, schemas.ContentLoggingDisabledForTrace(&schemas.Trace{RootSpan: recorder.root}), "content logging decision connectors read")
			if tc.wantAdded == nil {
				assert.Nil(t, recorder.traceDims, "nothing merged into the trace")
				return
			}
			assert.Equal(t, "trace-1", recorder.traceID)
			assert.Equal(t, tc.wantAdded, recorder.traceDims)
		})
	}
}

// TestVirtualKeyMetadataContextKeyValue pins the context key's wire string. The logging plugin and
// the HTTP transport declare their own copy of it, so changing it here alone would silently stop
// virtual key metadata from reaching the logs.
func TestVirtualKeyMetadataContextKeyValue(t *testing.T) {
	assert.Equal(t, schemas.BifrostContextKey("bifrost-governance-virtual-key-metadata"), VirtualKeyMetadataContextKey)
}

// BenchmarkStampVirtualKeyMetadata measures stamping a key's metadata on a traced request, with
// and without metadata (the common case, which must stay free).
func BenchmarkStampVirtualKeyMetadata(b *testing.B) {
	for _, bc := range []struct {
		name     string
		metadata map[string]string
	}{
		{name: "no_metadata", metadata: nil},
		{name: "two_entries", metadata: map[string]string{"cost_center": "cc-42", "owner": "platform"}},
	} {
		b.Run(bc.name, func(b *testing.B) {
			vk := &registry.VirtualKey{Metadata: bc.metadata}

			recorder := newDimensionRecorder(nil)
			headerDims := map[string]string{"team": "header"}
			b.ReportAllocs()
			for b.Loop() {
				ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				ctx.SetValue(schemas.BifrostContextKeyTracer, recorder)
				ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-1")
				ctx.SetValue(schemas.BifrostContextKeyDimensions, headerDims)
				stampVirtualKeyMetadata(ctx, vk)
			}
		})
	}
}

// TestGovernanceStore_TeamUpdateMovesKeysToNewCustomer checks a team's keys charge and log its new
// customer as soon as the team changes, and none once it is cleared.
func TestGovernanceStore_TeamUpdateMovesKeysToNewCustomer(t *testing.T) {
	ctx := context.Background()
	budgetA := buildBudgetWithUsage("budget-a", 1000.0, 0.0, "1d")
	budgetB := buildBudgetWithUsage("budget-b", 1000.0, 0.0, "1d")
	customerA := buildCustomer("customer-a", "Customer A", budgetA)
	customerB := buildCustomer("customer-b", "Customer B", budgetB)

	team := buildTeam("team", "Team", nil)
	team.CustomerID = &customerA.ID
	team.Customer = customerA
	vk := buildVirtualKey("vk", "sk-bf-team", "Team key", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{buildProviderConfig("openai", []string{"*"})}
	vk.TeamID = &team.ID
	vk.Team = team

	h := newLiveHarness(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budgetA, *budgetB},
		Teams:       []configstoreTables.TableTeam{*team},
		Customers:   []configstoreTables.TableCustomer{*customerA, *customerB},
	}, nil)
	store := h.mustGovernanceStore(NewMockLogger(), nil)

	// chargedAndStamped reports the customer budgets charged and the customer logged.
	chargedAndStamped := func() (charged []string, stamped string) {
		key, ok := store.GetVirtualKey(ctx, "sk-bf-team")
		require.True(t, ok)
		byID, ok := store.GetVirtualKeyByID(ctx, "vk")
		require.True(t, ok)
		require.Same(t, key, byID, "both lookups serve the same refreshed key")
		reqCtx := emptyCtx()
		budgets, _ := store.HolderLimits(reqCtx, store.permitForVirtualKey(reqCtx, key))
		for _, b := range budgets {
			if b.ID == "budget-a" || b.ID == "budget-b" {
				charged = append(charged, b.ID)
			}
		}
		reqCtx.Grant().SetIdentity(grant.NewIdentity(grant.NewCredential(grant.CredentialVirtualKey, "sk-bf-team"), nil, nil, nil, nil, nil, nil))
		store.StampVirtualKeyScope(reqCtx, key)
		if identity := reqCtx.Grant().Identity(); identity != nil && len(identity.Customers()) > 0 {
			stamped = identity.Customers()[0].ID
		}
		return charged, stamped
	}

	charged, stamped := chargedAndStamped()
	require.Equal(t, []string{"budget-a"}, charged)
	require.Equal(t, "customer-a", stamped)

	moved := *team
	moved.CustomerID = &customerB.ID
	moved.Customer = customerB
	h.putTeam(&moved)
	charged, stamped = chargedAndStamped()
	require.Equal(t, []string{"budget-b"}, charged, "the new customer is charged, the old one is not")
	require.Equal(t, "customer-b", stamped)

	cleared := *team
	cleared.CustomerID = nil
	cleared.Customer = nil
	h.putTeam(&cleared)
	charged, stamped = chargedAndStamped()
	require.Empty(t, charged, "a team with no customer charges none")
	require.Empty(t, stamped)
}

// TestGovernanceStore_TeamChangesReachGracePeriodAlias checks a team update and delete reach a
// key's rotated-out value as well as its current one.
func TestGovernanceStore_TeamChangesReachGracePeriodAlias(t *testing.T) {
	ctx := context.Background()
	customer := buildCustomer("customer-b", "Customer B", nil)
	team := buildTeam("team", "Team", nil)
	vk := buildVirtualKey("vk1", "sk-bf-old", "Test VK", true)
	vk.TeamID = &team.ID
	vk.Team = team
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Teams:       []configstoreTables.TableTeam{*team},
		Customers:   []configstoreTables.TableCustomer{*customer},
	}, nil)
	store := h.mustGovernanceStore(NewMockLogger(), nil)

	exp := time.Now().UTC().Add(5 * time.Minute)
	rotated := *vk
	rotated.Value = *schemas.NewSecretVar("sk-bf-new")
	rotated.PreviousValue = *schemas.NewSecretVar("sk-bf-old")
	rotated.PreviousValueExpiresAt = &exp
	h.putVirtualKey(&rotated)

	moved := *team
	moved.CustomerID = &customer.ID
	moved.Customer = customer
	h.putTeam(&moved)
	for _, value := range []string{"sk-bf-new", "sk-bf-old"} {
		key, ok := store.GetVirtualKey(ctx, value)
		require.True(t, ok, value)
		org := keyOrganizationOf(store.live, key)
		require.NotNil(t, org.team, value)
		assert.Equal(t, "customer-b", org.team.CustomerID, value)
	}

	h.deleteTeam("team")
	for _, value := range []string{"sk-bf-new", "sk-bf-old"} {
		key, ok := store.GetVirtualKey(ctx, value)
		require.True(t, ok, value)
		assert.Nil(t, keyOrganizationOf(store.live, key).team, value)
		assert.Empty(t, key.TeamID, value)
	}
}

// TestGovernanceStore_DeleteBeatsConcurrentTeamRefresh races key deletes against team refreshes and
// checks a deleted key never survives in either lookup.
func TestGovernanceStore_DeleteBeatsConcurrentTeamRefresh(t *testing.T) {
	ctx := context.Background()
	team := buildTeam("team", "Team", nil)
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		Teams: []configstoreTables.TableTeam{*team},
	}, nil)
	store := h.mustGovernanceStore(NewMockLogger(), nil)

	for i := 0; i < 200; i++ {
		vk := buildVirtualKey("vk1", "sk-bf-key", "Test VK", true)
		vk.TeamID = &team.ID
		vk.Team = team
		h.putVirtualKey(vk)

		var wg sync.WaitGroup
		var teamErr, deleteErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			renamed := *team
			if teamErr = h.writeTeam(&renamed, nil, nil); teamErr == nil {
				teamErr = h.live.Refresh(h.ctx, []livestore.Ref{{Kind: livestore.KindTeam, ID: team.ID}})
			}
		}()
		go func() {
			defer wg.Done()
			if deleteErr = h.configStore.DeleteVirtualKey(h.ctx, "vk1"); deleteErr == nil {
				deleteErr = h.live.Refresh(h.ctx, []livestore.Ref{{Kind: livestore.KindVirtualKey, ID: "vk1", Remove: true}})
			}
		}()
		wg.Wait()
		require.NoError(t, teamErr, "iteration %d: team refresh", i)
		require.NoError(t, deleteErr, "iteration %d: key delete", i)

		_, found := store.GetVirtualKey(ctx, "sk-bf-key")
		require.False(t, found, "iteration %d: deleted key still authenticates", i)
		_, found = store.GetVirtualKeyByID(ctx, "vk1")
		require.False(t, found, "iteration %d: deleted key still in the ID index", i)
	}
}

// TestGovernanceStore_ConcurrentTeamAndKeyWritesStayConsistent races team updates, key edits and key
// rotations, then checks each rotation was published, a key's value, ID and grace-period entries
// agree, and the key follows the team row last committed.
func TestGovernanceStore_ConcurrentTeamAndKeyWritesStayConsistent(t *testing.T) {
	ctx := context.Background()
	customerA := buildCustomer("customer-a", "Customer A", nil)
	customerB := buildCustomer("customer-b", "Customer B", nil)
	team := buildTeam("team", "Team", nil)
	team.CustomerID = &customerA.ID
	team.Customer = customerA
	vk := buildVirtualKey("vk1", "sk-bf-v0", "Test VK", true)
	vk.TeamID = &team.ID
	vk.Team = team
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Teams:       []configstoreTables.TableTeam{*team},
		Customers:   []configstoreTables.TableCustomer{*customerA, *customerB},
	}, nil)
	store := h.mustGovernanceStore(NewMockLogger(), nil)
	putTeam := func(moved configstoreTables.TableTeam) error {
		if err := h.writeTeam(&moved, nil, nil); err != nil {
			return err
		}
		return h.live.Refresh(h.ctx, []livestore.Ref{{Kind: livestore.KindTeam, ID: moved.ID}})
	}

	exp := time.Now().UTC().Add(5 * time.Minute)
	for round := 0; round < 50; round++ {
		var wg sync.WaitGroup
		errs := make([]error, 3)
		var rotatedFrom string
		wg.Add(3)
		go func() {
			defer wg.Done()
			moved := *team
			moved.CustomerID = &customerB.ID
			moved.Customer = customerB
			errs[0] = putTeam(moved)
		}()
		go func() {
			defer wg.Done()
			errs[1] = putTeam(*team)
		}()
		go func(round int) {
			defer wg.Done()
			current, ok := store.GetVirtualKeyByID(ctx, "vk1")
			if !ok {
				errs[2] = fmt.Errorf("key vk1 is not held before its rotation")
				return
			}
			rotatedFrom = current.Value
			rotated := *vk
			rotated.PreviousValue = *schemas.NewSecretVar(current.Value)
			rotated.PreviousValueExpiresAt = &exp
			rotated.Value = *schemas.NewSecretVar(fmt.Sprintf("sk-bf-v%d", round+1))
			if errs[2] = h.writeVirtualKey(&rotated, nil, nil); errs[2] == nil {
				errs[2] = h.live.Refresh(h.ctx, []livestore.Ref{{Kind: livestore.KindVirtualKey, ID: rotated.ID}})
			}
		}(round)
		wg.Wait()
		for _, err := range errs {
			require.NoError(t, err, "round %d", round)
		}

		byID, ok := store.GetVirtualKeyByID(ctx, "vk1")
		require.True(t, ok)
		require.Equal(t, fmt.Sprintf("sk-bf-v%d", round+1), byID.Value, "round %d: the rotation is published", round)
		require.Equal(t, rotatedFrom, byID.PreviousValue, "round %d: the rotated-from value is in its grace period", round)
		byValue, ok := store.GetVirtualKey(ctx, byID.Value)
		require.True(t, ok, "round %d: current value resolves", round)
		require.Same(t, byID, byValue, "round %d: ID and value entries agree", round)
		if prev := byID.PreviousValue; prev != "" {
			alias, ok := store.GetVirtualKey(ctx, prev)
			require.True(t, ok, "round %d: grace alias resolves", round)
			require.Same(t, byID, alias, "round %d: grace alias agrees", round)
		}
		cachedTeam, ok := store.live.Collections.Teams.Get("team")
		require.True(t, ok)
		require.Same(t, cachedTeam, keyOrganizationOf(store.live, byID).team, "round %d: the key follows the cached team", round)
		committed, err := h.configStore.GetTeam(ctx, "team")
		require.NoError(t, err)
		require.NotNil(t, committed.CustomerID)
		require.Equal(t, *committed.CustomerID, keyOrganizationOf(store.live, byID).team.CustomerID, "round %d: the key follows the committed team", round)
	}
}

// TestGovernanceStore_CreateTeamRefreshesKeysThatArrivedFirst checks a key held before its team
// picks up the team's customer once the team lands.
func TestGovernanceStore_CreateTeamRefreshesKeysThatArrivedFirst(t *testing.T) {
	ctx := context.Background()
	customer := buildCustomer("customer-a", "Customer A", nil)
	stale := buildTeam("team", "Team", nil)
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		Customers: []configstoreTables.TableCustomer{*customer},
	}, nil)
	store := h.mustGovernanceStore(NewMockLogger(), nil)
	// The team's row exists, as the key's reference needs, but its record has not reached memory.
	h.must(h.writeTeam(stale, nil, nil), "write team")
	vk := buildVirtualKey("vk1", "sk-bf-key", "Test VK", true)
	vk.TeamID = &stale.ID
	h.putVirtualKey(vk)

	team := buildTeam("team", "Team", nil)
	team.CustomerID = &customer.ID
	team.Customer = customer
	h.putTeam(team)

	key, ok := store.GetVirtualKey(ctx, "sk-bf-key")
	require.True(t, ok)
	org := keyOrganizationOf(store.live, key)
	require.NotNil(t, org.team)
	require.NotEmpty(t, org.team.CustomerID)
	assert.Equal(t, "customer-a", org.team.CustomerID)
}

// TestGovernanceStore_ResetDoesNotRevertAConcurrentTeamEdit checks a team edit keeps the usage
// memory holds ahead of its budget's row, and a budget reset after the edit keeps the edit and still
// applies the reset: the reset touches the budget's usage only.
func TestGovernanceStore_ResetDoesNotRevertAConcurrentTeamEdit(t *testing.T) {
	ctx := context.Background()
	budget := buildBudgetWithUsage("team-budget", 100.0, 40.0, "1d")
	customerA := buildCustomer("customer-a", "Customer A", nil)
	customerB := buildCustomer("customer-b", "Customer B", nil)
	team := buildTeam("team", "Team", budget)
	team.CustomerID = &customerA.ID
	team.Customer = customerA
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		Budgets:   []configstoreTables.TableBudget{*budget},
		Teams:     []configstoreTables.TableTeam{*team},
		Customers: []configstoreTables.TableCustomer{*customerA, *customerB},
	}, nil)
	store := h.mustGovernanceStore(NewMockLogger(), nil)
	require.NoError(t, store.BumpBudgetUsageBy(ctx, "team-budget", 5))

	edited := *team
	edited.CustomerID = &customerB.ID
	edited.Customer = customerB
	h.putTeam(&edited)
	afterEdit := store.LoadBudget(ctx, "team-budget")
	require.NotNil(t, afterEdit)
	require.Equal(t, 45.0, afterEdit.CurrentUsage, "the team edit keeps the usage memory holds ahead of the row")
	_, ok := store.ResetBudgetUsageInMemory(ctx, "team-budget")
	require.True(t, ok)

	final, ok := store.live.Collections.Teams.Get("team")
	require.True(t, ok)
	assert.Equal(t, "customer-b", final.CustomerID, "the edit is kept")
	reset := store.LoadBudget(ctx, "team-budget")
	require.NotNil(t, reset)
	assert.Zero(t, reset.CurrentUsage, "and the reset still applies")
}

// TestModelConfigRefreshPreservesRateLimitResetEvidence checks that a stale
// config refresh preserves both live accounting tuples and applies new limits. The
// model config is written again with new caps on its rate limit and refreshed: the
// rate limit arrives as the model config's child, and the store keeps the usage it
// holds while it applies the caps.
func TestModelConfigRefreshPreservesRateLimitResetEvidence(t *testing.T) {
	ctx := context.Background()
	h := newLiveHarness(t, nil, nil)
	store := h.mustGovernanceStore(NewMockLogger(), nil)
	h.putModelConfig(buildModelConfig("mc-1", "gpt-4o", nil, nil, buildRateLimit("rate", 1000, 1000)))
	rate := buildRateLimit("rate", 1000, 1000)
	rate.TokenLastReset = time.Now().UTC().Truncate(time.Hour)
	rate.RequestLastReset = rate.TokenLastReset.Add(-time.Hour)
	rate.TokenUsageResetEpoch = rate.TokenLastReset.UnixNano()
	rate.RequestUsageResetEpoch = rate.RequestLastReset.UnixNano()
	rate.TokenCurrentUsage, rate.RequestCurrentUsage = 17, 19
	setUsage(store.rateLimitUsage, rate.ID, func(s *LimitState) {
		s.Usage[TokenSlot] = WholeCount(rate.TokenCurrentUsage)
		s.Usage[RequestSlot] = WholeCount(rate.RequestCurrentUsage)
		s.setLastReset(TokenSlot, rate.TokenLastReset)
		s.setLastReset(RequestSlot, rate.RequestLastReset)
		s.ResetEpoch[TokenSlot] = rate.TokenUsageResetEpoch
		s.ResetEpoch[RequestSlot] = rate.RequestUsageResetEpoch
	})

	incoming := buildRateLimit(rate.ID, 2000, 3000)
	h.putModelConfig(buildModelConfig("mc-1", "gpt-4o", nil, nil, incoming))
	live := store.LoadRateLimit(ctx, rate.ID)
	require.NotNil(t, live, "the model config refresh delivers its rate limit")
	require.Equal(t, incoming.TokenMaxLimit, live.TokenMaxLimit)
	require.Equal(t, incoming.RequestMaxLimit, live.RequestMaxLimit)
	require.Equal(t, rate.TokenCurrentUsage, live.TokenCurrentUsage)
	require.Equal(t, rate.RequestCurrentUsage, live.RequestCurrentUsage)
	require.Equal(t, rate.TokenLastReset, live.TokenLastReset)
	require.Equal(t, rate.RequestLastReset, live.RequestLastReset)
	require.Equal(t, rate.TokenUsageResetEpoch, live.TokenUsageResetEpoch)
	require.Equal(t, rate.RequestUsageResetEpoch, live.RequestUsageResetEpoch)
}

// TestConfigRefreshPreservesBudgetAndProviderAccounting checks that stale
// persisted settings can change limits without changing the live accounting tuple.
// A model config's and a provider's budgets and rate limit are re-read with them
// and handed to the store as limit rows, which take the row's settings and the
// holder's calendar flag and keep the usage counted in memory.
func TestConfigRefreshPreservesBudgetAndProviderAccounting(t *testing.T) {
	ctx := context.Background()
	h := newLiveHarness(t, &configstore.GovernanceConfig{}, nil)
	store := h.mustGovernanceStore(NewMockLogger(), nil)
	boundary := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	modelBudget := buildBudgetWithUsage("model-budget", 100, 7, "1M")
	modelBudget.LastReset, modelBudget.UsageResetEpoch = boundary, boundary.UnixNano()
	putBudget(t, store, modelBudget.ID, modelBudget)
	staleModelBudget := buildBudgetWithUsage(modelBudget.ID, 200, 42, "1M")
	h.putModelConfig(&configstoreTables.TableModelConfig{
		ID: "model", ModelName: "gpt-4o", CalendarAligned: true, Budgets: []configstoreTables.TableBudget{*staleModelBudget},
	})
	expectedModelBudget := *modelBudget
	expectedModelBudget.MaxLimit = staleModelBudget.MaxLimit
	liveModelBudget := store.LoadBudget(ctx, modelBudget.ID)
	requireLimitAccounting(t, &expectedModelBudget, nil, liveModelBudget, nil)
	require.True(t, liveModelBudget.IsCalendarAligned, "the budget follows its model config's calendar flag")

	providerBudget := buildBudgetWithUsage("provider-budget", 100, 9, "1M")
	providerBudget.LastReset, providerBudget.UsageResetEpoch = boundary, boundary.UnixNano()
	putBudget(t, store, providerBudget.ID, providerBudget)
	rate := buildRateLimit("provider-rate", 1000, 1000)
	rate.TokenLastReset = boundary
	rate.RequestLastReset = boundary.Add(-time.Hour)
	rate.TokenUsageResetEpoch, rate.RequestUsageResetEpoch = rate.TokenLastReset.UnixNano(), rate.RequestLastReset.UnixNano()
	rate.TokenCurrentUsage, rate.RequestCurrentUsage = 17, 19
	putRateLimit(t, store, rate.ID, rate)

	staleBudget := buildBudgetWithUsage(providerBudget.ID, 200, 42, "1M")
	staleRate := buildRateLimit(rate.ID, 2000, 3000)
	h.putProvider(&configstoreTables.TableProvider{Name: "provider", Budget: staleBudget, RateLimit: staleRate})
	expectedProviderBudget := *providerBudget
	expectedProviderBudget.MaxLimit = staleBudget.MaxLimit
	expectedRate := *rate
	expectedRate.TokenMaxLimit, expectedRate.RequestMaxLimit = staleRate.TokenMaxLimit, staleRate.RequestMaxLimit
	requireLimitAccounting(t, &expectedProviderBudget, &expectedRate, store.LoadBudget(ctx, providerBudget.ID), store.LoadRateLimit(ctx, rate.ID))
}

// TestUsageResetEpochsIgnoreAdoption verifies retained usage never produces reset
// evidence, while actual resets publish evidence before hooks run.
func TestUsageResetEpochsIgnoreAdoption(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStore(t)
	now := time.Date(2026, 2, 5, 12, 0, 0, 0, time.UTC)
	old := time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)
	budget := buildBudgetWithUsage("budget", 100, 42, "1M")
	budget.LastReset, budget.CreatedAt = old, old
	putBudget(t, store, budget.ID, budget)
	rate := buildRateLimit("rate", 1000, 1000)
	month := "1M"
	rate.TokenResetDuration, rate.RequestResetDuration = &month, &month
	rate.TokenLastReset, rate.RequestLastReset, rate.CreatedAt = old, old, old
	rate.TokenCurrentUsage, rate.RequestCurrentUsage = 17, 19
	putRateLimit(t, store, rate.ID, rate)
	if !store.AdoptCalendarAlignmentInMemory(ctx, budget.ID, now) || !store.AdoptRateLimitCalendarAlignmentInMemory(ctx, rate.ID, now) {
		t.Fatal("expected calendar adoption")
	}
	adoptedBudget := store.LoadBudget(ctx, budget.ID)
	if adoptedBudget.CurrentUsage != 42 || adoptedBudget.UsageResetEpoch != 0 {
		t.Fatalf("after adoption, budget usage = %v and reset epoch = %v", adoptedBudget.CurrentUsage, adoptedBudget.UsageResetEpoch)
	}
	adoptedRate := store.LoadRateLimit(ctx, rate.ID)
	if adoptedRate.TokenCurrentUsage != 17 || adoptedRate.RequestCurrentUsage != 19 {
		t.Fatalf("after adoption, token usage = %v and request usage = %v", adoptedRate.TokenCurrentUsage, adoptedRate.RequestCurrentUsage)
	}
	if adoptedRate.TokenUsageResetEpoch != 0 || adoptedRate.RequestUsageResetEpoch != 0 {
		t.Fatalf("after adoption, token reset epoch = %v and request reset epoch = %v", adoptedRate.TokenUsageResetEpoch, adoptedRate.RequestUsageResetEpoch)
	}
	target := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	reset, ok := store.ResetRateLimitAt(ctx, rate.ID, &target, nil)
	if !ok || reset.TokenCurrentUsage != 0 || reset.RequestCurrentUsage != 19 {
		t.Fatal("token reset changed retained requests")
	}
	// Each state is immutable, so a read never waits on a writer.
	tokens, requests := store.LoadRateLimit(ctx, rate.ID).TokenUsageResetEpoch, store.LoadRateLimit(ctx, rate.ID).RequestUsageResetEpoch
	if tokens != target.UnixNano() || requests != 0 {
		t.Fatalf("independent evidence = %v, %v", tokens, requests)
	}
	if _, ok := store.ResetBudgetAt(ctx, budget.ID, target); !ok {
		t.Fatal("expected budget reset")
	}
	epoch := store.LoadBudget(ctx, budget.ID).UsageResetEpoch
	if epoch != target.UnixNano() {
		t.Fatalf("budget evidence = %v", epoch)
	}
	reloadBudget(t, store, budget.ID, budget)
	reloadRateLimit(t, store, rate.ID, rate)
	if store.LoadBudget(ctx, budget.ID).UsageResetEpoch != target.UnixNano() {
		t.Fatal("config replacement lost evidence")
	}
	tokens, requests = store.LoadRateLimit(ctx, rate.ID).TokenUsageResetEpoch, store.LoadRateLimit(ctx, rate.ID).RequestUsageResetEpoch
	if tokens != target.UnixNano() || requests != 0 {
		t.Fatal("config replacement changed rate evidence")
	}
	dropLimit(t, store, livestore.KindBudget, budget.ID)
	dropLimit(t, store, livestore.KindRateLimit, rate.ID)
	if store.LoadBudget(ctx, budget.ID) != nil {
		t.Fatal("deleted budget retained evidence")
	}
	if store.LoadRateLimit(ctx, rate.ID) != nil {
		t.Fatal("deleted rate retained evidence")
	}
}

// TestUsageResetEpochsPublishedBeforeHooks covers normal expiry commits and
// verifies a rate hook cannot mistake an untouched request boundary for a reset.
func TestUsageResetEpochsPublishedBeforeHooks(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStore(t)
	old := time.Now().Add(-2 * time.Hour)
	budget := buildBudgetWithUsage("budget", 100, 42, "1h")
	budget.LastReset = old
	putBudget(t, store, budget.ID, budget)
	rate := buildRateLimit("rate", 1000, 1000)
	rate.TokenLastReset = old
	rate.RequestLastReset = time.Now()
	putRateLimit(t, store, rate.ID, rate)
	budgetHook, rateHook := false, false
	store.SetResetHooks(func(rows []*configstoreTables.TableBudget) {
		budgetHook = true
		if store.LoadBudget(ctx, rows[0].ID).UsageResetEpoch != rows[0].LastReset.UnixNano() {
			t.Fatal("hook preceded evidence")
		}
	}, func(rows []*configstoreTables.TableRateLimit) {
		rateHook = true
		tokens, requests := store.LoadRateLimit(ctx, rows[0].ID).TokenUsageResetEpoch, store.LoadRateLimit(ctx, rows[0].ID).RequestUsageResetEpoch
		if tokens != rows[0].TokenLastReset.UnixNano() || requests != 0 {
			t.Fatal("hook has incorrect dimension evidence")
		}
	})
	store.ResetExpiredBudgetsInMemory(ctx, budget.ID)
	store.ResetExpiredRateLimitsInMemory(ctx, rate.ID)
	if !budgetHook || !rateHook {
		t.Fatal("expected reset hooks")
	}
}

// TestUsageResetEpochsReloadAndOperatorReset checks evidence follows resource
// lifetime through full reloads. Operator clears preserve window reset evidence.
func TestUsageResetEpochsReloadAndOperatorReset(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStore(t)
	old := time.Now().Add(-time.Hour)
	target := old.Add(time.Minute)
	budget := buildBudgetWithUsage("budget", 100, 42, "1h")
	budget.LastReset = old
	rate := buildRateLimit("rate", 100, 100)
	rate.TokenLastReset, rate.RequestLastReset = old, old
	putBudget(t, store, budget.ID, budget)
	putRateLimit(t, store, rate.ID, rate)
	// The rows a reload re-reads, captured before the resets: they carry no reset evidence, so a
	// seed that took the incoming row's epochs would erase what the resets record below.
	budgetRow, rateRow := *budget, *rate
	reset, ok := store.ResetBudgetAt(ctx, budget.ID, target)
	if !ok || store.LoadBudget(ctx, budget.ID).UsageResetEpoch != target.UnixNano() {
		t.Fatal("window reset must publish evidence")
	}
	if err := store.BumpBudgetUsageBy(ctx, budget.ID, 9); err != nil {
		t.Fatal(err)
	}
	reset, ok = store.ResetBudgetUsageInMemory(ctx, budget.ID)
	if !ok || reset.CurrentUsage != 0 || !reset.LastReset.Equal(target) || store.LoadBudget(ctx, budget.ID).UsageResetEpoch != target.UnixNano() {
		t.Fatal("operator clear must preserve the window and its reset evidence")
	}
	if _, ok := store.ResetRateLimitAt(ctx, rate.ID, nil, &target); !ok {
		t.Fatal("expected request reset")
	}
	tokens, requests := store.LoadRateLimit(ctx, rate.ID).TokenUsageResetEpoch, store.LoadRateLimit(ctx, rate.ID).RequestUsageResetEpoch
	if tokens != 0 || requests != target.UnixNano() {
		t.Fatal("request-only reset changed token evidence")
	}
	if _, ok := store.ResetRateLimitAt(ctx, rate.ID, nil, &old); ok {
		t.Fatal("stale reset succeeded")
	}
	// A reload re-reads each limit's row: the live store hands the governance store every row it
	// still holds, and a row it no longer holds arrives as a removal. The rows reseeded here were
	// read before the resets, so the evidence survives only if seeding keeps the store's own epochs.
	heldBudget, heldRate := *store.LoadBudget(ctx, budget.ID), *store.LoadRateLimit(ctx, rate.ID)
	reloadBudget(t, store, budget.ID, &budgetRow)
	reloadRateLimit(t, store, rate.ID, &rateRow)
	reloadedBudget := store.LoadBudget(ctx, budget.ID)
	if reloadedBudget.UsageResetEpoch != target.UnixNano() {
		t.Fatal("reload lost budget evidence")
	}
	// The rows carry the usage and reset times from before the resets, so the held values must win
	// for those too, not only for the epochs.
	if reloadedBudget.CurrentUsage != heldBudget.CurrentUsage || !reloadedBudget.LastReset.Equal(heldBudget.LastReset) {
		t.Fatalf("reload restored the row's budget usage %v and reset %v, want the held %v and %v",
			reloadedBudget.CurrentUsage, reloadedBudget.LastReset, heldBudget.CurrentUsage, heldBudget.LastReset)
	}
	reloadedRate := store.LoadRateLimit(ctx, rate.ID)
	tokens, requests = reloadedRate.TokenUsageResetEpoch, reloadedRate.RequestUsageResetEpoch
	if tokens != 0 || requests != target.UnixNano() {
		t.Fatal("reload changed rate evidence")
	}
	if reloadedRate.TokenCurrentUsage != heldRate.TokenCurrentUsage || reloadedRate.RequestCurrentUsage != heldRate.RequestCurrentUsage ||
		!reloadedRate.TokenLastReset.Equal(heldRate.TokenLastReset) || !reloadedRate.RequestLastReset.Equal(heldRate.RequestLastReset) {
		t.Fatal("reload restored the row's rate-limit usage or reset times instead of the held ones")
	}
	dropLimit(t, store, livestore.KindBudget, budget.ID)
	dropLimit(t, store, livestore.KindRateLimit, rate.ID)
	if store.LoadBudget(ctx, budget.ID) != nil {
		t.Fatal("reload retained removed budget evidence")
	}
	if store.LoadRateLimit(ctx, rate.ID) != nil {
		t.Fatal("reload retained removed rate evidence")
	}
}

// calendarOwner writes one kind of owner holding the given limits and reloads it, as the owner's
// write does.
type calendarOwner func(h *liveHarness, aligned bool, budget configstoreTables.TableBudget, rate configstoreTables.TableRateLimit)

// calendarOwners lists every OSS owner whose reload stamps calendar alignment.
func calendarOwners() map[string]calendarOwner {
	return map[string]calendarOwner{
		"team": func(h *liveHarness, aligned bool, budget configstoreTables.TableBudget, rate configstoreTables.TableRateLimit) {
			h.putTeam(&configstoreTables.TableTeam{ID: "owner", Name: "owner", CalendarAligned: aligned, Budgets: []configstoreTables.TableBudget{budget}, RateLimitID: &rate.ID, RateLimit: &rate})
		},
		"customer": func(h *liveHarness, aligned bool, budget configstoreTables.TableBudget, rate configstoreTables.TableRateLimit) {
			h.putCustomer(&configstoreTables.TableCustomer{ID: "owner", Name: "owner", CalendarAligned: aligned, Budgets: []configstoreTables.TableBudget{budget}, RateLimitID: &rate.ID, RateLimit: &rate})
		},
		"virtual key": func(h *liveHarness, aligned bool, budget configstoreTables.TableBudget, rate configstoreTables.TableRateLimit) {
			h.putVirtualKey(&configstoreTables.TableVirtualKey{ID: "owner", Name: "owner", Value: *schemas.NewSecretVar("sk-bf-owner"), CalendarAligned: aligned, Budgets: []configstoreTables.TableBudget{budget}, RateLimitID: &rate.ID, RateLimit: &rate})
		},
		"provider config": func(h *liveHarness, aligned bool, budget configstoreTables.TableBudget, rate configstoreTables.TableRateLimit) {
			h.putVirtualKey(&configstoreTables.TableVirtualKey{ID: "owner", Name: "owner", Value: *schemas.NewSecretVar("sk-bf-owner"), CalendarAligned: aligned, ProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{{ID: 1, Provider: "openai", Budgets: []configstoreTables.TableBudget{budget}, RateLimitID: &rate.ID, RateLimit: &rate}}})
		},
		"model config": func(h *liveHarness, aligned bool, budget configstoreTables.TableBudget, rate configstoreTables.TableRateLimit) {
			h.putModelConfig(&configstoreTables.TableModelConfig{ID: "owner", ModelName: "gpt-4o", CalendarAligned: aligned, Budgets: []configstoreTables.TableBudget{budget}, RateLimitID: &rate.ID, RateLimit: &rate})
		},
	}
}

// openWindow returns limits with usage in a window opened before this month's boundary.
func openWindow() (time.Time, configstoreTables.TableBudget, configstoreTables.TableRateLimit) {
	month, day := "1M", "1d"
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	opened := monthStart.Add(-10 * 24 * time.Hour)
	budget := configstoreTables.TableBudget{ID: "budget", MaxLimit: 100, CurrentUsage: 7, ResetDuration: month, CreatedAt: opened, LastReset: opened}
	rate := *buildRateLimit("rate", 100, 100)
	rate.TokenResetDuration, rate.RequestResetDuration = &month, &day
	rate.TokenCurrentUsage, rate.RequestCurrentUsage = 17, 19
	rate.TokenLastReset, rate.RequestLastReset = opened, opened
	return monthStart, budget, rate
}

// calendarHarness returns an empty live harness and a governance store following it.
func calendarHarness(t *testing.T) (*liveHarness, *LocalGovernanceStore) {
	t.Helper()
	h := newLiveHarness(t, &configstore.GovernanceConfig{}, nil)
	return h, h.mustGovernanceStore(NewMockLogger(), nil)
}

// TestReloadAdoptsCalendarBeforeSweep checks that a sweep running right after the
// reload that switches alignment on finds every window current and keeps its usage.
func TestReloadAdoptsCalendarBeforeSweep(t *testing.T) {
	for name, reload := range calendarOwners() {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			h, store := calendarHarness(t)
			monthStart, budget, rate := openWindow()
			reload(h, false, budget, rate)
			reload(h, true, budget, rate)

			require.Empty(t, store.ResetExpiredBudgetsInMemory(ctx, budget.ID))
			require.Empty(t, store.ResetExpiredRateLimitsInMemory(ctx, rate.ID))
			liveBudget, liveRate := store.LoadBudget(ctx, budget.ID), store.LoadRateLimit(ctx, rate.ID)
			require.True(t, liveBudget.IsCalendarAligned)
			require.Equal(t, 7.0, liveBudget.CurrentUsage)
			require.True(t, liveBudget.LastReset.Equal(monthStart))
			require.Zero(t, liveBudget.UsageResetEpoch)
			require.Equal(t, int64(17), liveRate.TokenCurrentUsage)
			require.Equal(t, int64(19), liveRate.RequestCurrentUsage)
			require.True(t, liveRate.TokenLastReset.Equal(monthStart))
			require.Zero(t, liveRate.TokenUsageResetEpoch)
			require.Zero(t, liveRate.RequestUsageResetEpoch)
		})
	}
}

// TestReloadAdoptsCalendarWithNewDuration checks that adoption uses the duration
// installed by the same edit, not the one it replaces.
func TestReloadAdoptsCalendarWithNewDuration(t *testing.T) {
	for name, reload := range calendarOwners() {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			h, store := calendarHarness(t)
			monthStart, budget, rate := openWindow()
			hourly := budget
			hourly.ResetDuration = "1h"
			reload(h, false, hourly, rate)
			reload(h, true, budget, rate)

			require.Empty(t, store.ResetExpiredBudgetsInMemory(ctx, budget.ID))
			live := store.LoadBudget(ctx, budget.ID)
			require.Equal(t, 7.0, live.CurrentUsage)
			require.True(t, live.LastReset.Equal(monthStart))
		})
	}
}

// TestReloadKeepsDueCalendarReset checks that neither the first load of an owner that
// is already aligned nor a later reload of it adopts. The overdue window must still
// reset.
func TestReloadKeepsDueCalendarReset(t *testing.T) {
	for name, reload := range calendarOwners() {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			h, store := calendarHarness(t)
			monthStart, budget, rate := openWindow()
			reload(h, true, budget, rate)
			// A changed setting is what makes the reload bring the limits in again.
			raised := int64(101)
			budget.MaxLimit, rate.TokenMaxLimit = 101, &raised
			reload(h, true, budget, rate)

			require.True(t, store.LoadBudget(ctx, budget.ID).LastReset.Before(monthStart))
			require.Len(t, store.ResetExpiredBudgetsInMemory(ctx, budget.ID), 1)
			require.Len(t, store.ResetExpiredRateLimitsInMemory(ctx, rate.ID), 1)
			require.Zero(t, store.LoadBudget(ctx, budget.ID).CurrentUsage)
			require.Zero(t, store.LoadRateLimit(ctx, rate.ID).TokenCurrentUsage)
		})
	}
}

// TestLaterBoundaryKeepsUnfoldedUsageWithoutReset checks that a baseline from a later
// boundary keeps local charges when its known reset epoch is not newer than the local
// one. A newer reset, or unknown evidence, replaces the old window's usage.
func TestLaterBoundaryKeepsUnfoldedUsageWithoutReset(t *testing.T) {
	ctx := context.Background()
	for name, test := range map[string]struct {
		local, incoming int64
		keeps           bool
	}{
		"same known epoch":      {10, 10, true},
		"sender reset":          {10, 20, false},
		"unknown sender":        {10, 0, false},
		"unknown on both sides": {0, 0, false},
		"local reset is newer":  {20, 10, true},
	} {
		t.Run(name, func(t *testing.T) {
			store := newStandaloneStore(t)
			opened := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
			boundary := opened.Add(24 * time.Hour)
			budget := buildBudgetWithUsage("budget", 1000, 100, "1M")
			budget.LastReset, budget.UsageResetEpoch = opened, test.local
			rate := buildRateLimit("rate", 1000, 1000)
			rate.TokenCurrentUsage, rate.RequestCurrentUsage = 100, 100
			rate.TokenLastReset, rate.RequestLastReset = opened, opened
			rate.TokenUsageResetEpoch, rate.RequestUsageResetEpoch = test.local, test.local
			putBudget(t, store, budget.ID, budget)
			putRateLimit(t, store, rate.ID, rate)
			setBudgetBaseline(t, store, budget.ID, 100)
			setRateLimitBaselines(t, store, rate.ID, 100, 100)
			require.NoError(t, store.BumpBudgetUsageBy(ctx, budget.ID, 7))
			require.NoError(t, store.BumpRateLimitUsageBy(ctx, rate.ID, 7, 7))

			require.True(t, store.ApplyBudgetBaselineWithResetEpoch(ctx, budget.ID, 100, boundary, test.incoming))
			tokens, requests := store.ApplyRateLimitBaselinesWithResetEpochs(ctx, rate.ID, 100, 100, boundary, boundary, test.incoming, test.incoming)
			require.True(t, tokens && requests)
			want := int64(100)
			if test.keeps {
				want = 107
			}
			liveBudget, liveRate := store.LoadBudget(ctx, budget.ID), store.LoadRateLimit(ctx, rate.ID)
			require.Equal(t, float64(want), liveBudget.CurrentUsage)
			require.True(t, liveBudget.LastReset.Equal(boundary))
			require.Equal(t, want, liveRate.TokenCurrentUsage)
			require.Equal(t, want, liveRate.RequestCurrentUsage)
			require.True(t, liveRate.TokenLastReset.Equal(boundary))
			require.True(t, liveRate.RequestLastReset.Equal(boundary))
		})
	}
}
