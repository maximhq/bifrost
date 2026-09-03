package governance

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalGovernanceStore_GetGovernanceUsageDataExcludesUnrelatedState(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{
			*buildVirtualKey("vk1", "sk-bf-test1", "Test VK 1", true),
		},
		Budgets:    []configstoreTables.TableBudget{{ID: "budget1"}},
		RateLimits: []configstoreTables.TableRateLimit{{ID: "rate-limit1"}},
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
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
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
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	// Retrieve VK with budget
	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")

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
			testStore, _ := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
				VirtualKeys: []configstoreTables.TableVirtualKey{*testVK},
				Budgets:     []configstoreTables.TableBudget{*testBudget},
			}, nil, nil)

			testVK, _ = testStore.GetVirtualKey(context.Background(), "sk-bf-test")
			_, err := checkGrantBudgets(testStore, emptyCtx(), testVK, schemas.OpenAI, "", nil)
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
	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil, nil)
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*vkBudget, *teamBudget, *customerBudget},
		Teams:       []configstoreTables.TableTeam{*team},
		Customers:   []configstoreTables.TableCustomer{*customer},
	}, nil, nil)
	require.NoError(t, err)

	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")

	// Test: All budgets under limit should pass
	_, err = checkGrantBudgets(store, emptyCtx(), vk, schemas.OpenAI, "", nil)
	assert.NoError(t, err, "Should pass when all budgets are under limit")

	// Test: If VK budget exceeds limit, should fail
	// Update the budget directly in the budgets map (since UpdateVirtualKeyInMemory preserves usage)
	if len(vk.Budgets) > 0 {
		budgetID := vk.Budgets[0].ID
		if budgetValue, exists := store.budgets.Load(budgetID); exists && budgetValue != nil {
			if budget, ok := budgetValue.(*configstoreTables.TableBudget); ok && budget != nil {
				budget.CurrentUsage = 100.0
				store.budgets.Store(budgetID, budget)
			}
		}
	}
	_, err = checkGrantBudgets(store, emptyCtx(), vk, schemas.OpenAI, "", nil)
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*hourlyBudget, *dailyBudget},
	}, nil, nil)
	require.NoError(t, err)

	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	_, err = checkGrantBudgets(store, emptyCtx(), vk, schemas.OpenAI, "", nil)
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*hourlyBudget, *dailyBudget},
	}, nil, nil)
	require.NoError(t, err)

	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	_, err = checkGrantBudgets(store, emptyCtx(), vk, schemas.OpenAI, "", nil)
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*hourlyBudget, *dailyBudget},
	}, nil, nil)
	require.NoError(t, err)

	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	_, err = checkGrantBudgets(store, emptyCtx(), vk, schemas.OpenAI, "", nil)
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*hourlyBudget, *dailyBudget},
	}, nil, nil)
	require.NoError(t, err)

	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")

	// Simulate a $3.50 request
	err = chargeGrantBudgets(store, context.Background(), vk, schemas.OpenAI, 3.50)
	require.NoError(t, err)

	// Both budgets should reflect the cost
	hourlyVal, exists := store.budgets.Load("hourly")
	require.True(t, exists)
	assert.InDelta(t, 3.50, hourlyVal.(*configstoreTables.TableBudget).CurrentUsage, 0.01, "Hourly budget should reflect usage")

	dailyVal, exists := store.budgets.Load("daily")
	require.True(t, exists)
	assert.InDelta(t, 3.50, dailyVal.(*configstoreTables.TableBudget).CurrentUsage, 0.01, "Daily budget should reflect usage")

	// Second request: $7.00 — should push hourly over limit
	err = chargeGrantBudgets(store, context.Background(), vk, schemas.OpenAI, 7.00)
	require.NoError(t, err)

	hourlyVal, _ = store.budgets.Load("hourly")
	assert.InDelta(t, 10.50, hourlyVal.(*configstoreTables.TableBudget).CurrentUsage, 0.01, "Hourly budget should accumulate")

	dailyVal, _ = store.budgets.Load("daily")
	assert.InDelta(t, 10.50, dailyVal.(*configstoreTables.TableBudget).CurrentUsage, 0.01, "Daily budget should accumulate")

	// Now CheckBudget should fail (hourly exceeded)
	_, err = checkGrantBudgets(store, emptyCtx(), vk, schemas.OpenAI, "", nil)
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*pcHourly, *pcDaily},
	}, nil, nil)
	require.NoError(t, err)

	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	_, err = checkGrantBudgets(store, emptyCtx(), vk, schemas.OpenAI, "", nil)
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*vkMonthly, *pcHourly},
	}, nil, nil)
	require.NoError(t, err)

	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")

	// Provider config budget exceeded → should block even though VK budget is fine
	_, err = checkGrantBudgets(store, emptyCtx(), vk, schemas.OpenAI, "", nil)
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*hourlyBudget, *dailyBudget},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*hourlyBudget, *dailyBudget},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*hourlyBudget, *dailyBudget},
	}, nil, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)

	// Request 1: $0.80 — both budgets fine
	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	err = chargeGrantBudgets(store, context.Background(), vk, schemas.OpenAI, 0.80)
	require.NoError(t, err)

	ctx := resolverCtx(store, "sk-bf-test")
	result := evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)
	assertDecision(t, DecisionAllow, result)

	// Request 2: $0.80 — still fine ($1.60 total)
	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	err = chargeGrantBudgets(store, context.Background(), vk, schemas.OpenAI, 0.80)
	require.NoError(t, err)

	ctx = resolverCtx(store, "sk-bf-test")
	result = evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)
	assertDecision(t, DecisionAllow, result)

	// Request 3: $0.80 — pushes hourly to $2.40 > $2.00 limit → blocked
	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	err = chargeGrantBudgets(store, context.Background(), vk, schemas.OpenAI, 0.80)
	require.NoError(t, err)

	ctx = resolverCtx(store, "sk-bf-test")
	result = evaluateVirtualKey(resolver, ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false, false)
	assertDecision(t, DecisionBudgetExceeded, result)
	assert.Contains(t, result.Reason, "budget exceeded")

	// Verify daily budget is still under limit
	dailyVal, exists := store.budgets.Load("daily")
	require.True(t, exists)
	assert.InDelta(t, 2.40, dailyVal.(*configstoreTables.TableBudget).CurrentUsage, 0.01,
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*dailyBudget, *monthlyBudget},
	}, nil, nil)
	require.NoError(t, err)

	// Verify VK-level calendar_aligned is set
	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	assert.True(t, vk.CalendarAligned, "VK should have calendar_aligned=true")

	// Both under limit — should pass
	_, err = checkGrantBudgets(store, emptyCtx(), vk, schemas.OpenAI, "", nil)
	assert.NoError(t, err)
}

// TestGovernanceStore_MultiBudget_InMemoryCreateAndDelete tests CreateVirtualKeyInMemory and DeleteVirtualKeyInMemory
// properly store and clean up multi-budget entries
func TestGovernanceStore_MultiBudget_InMemoryCreateAndDelete(t *testing.T) {
	logger := NewMockLogger()

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)

	b1 := buildBudget("b1", 10.0, "1h")
	b2 := buildBudget("b2", 100.0, "1d")

	vk := buildVirtualKeyWithMultiBudgets("vk1", "sk-bf-test", "Test VK",
		[]configstoreTables.TableBudget{*b1, *b2})
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}

	// Create
	store.CreateVirtualKeyInMemory(context.Background(), vk)

	_, exists := store.budgets.Load("b1")
	assert.True(t, exists, "Budget b1 should be in memory after create")
	_, exists = store.budgets.Load("b2")
	assert.True(t, exists, "Budget b2 should be in memory after create")

	retrieved, found := store.GetVirtualKey(context.Background(), "sk-bf-test")
	require.True(t, found)
	assert.Len(t, retrieved.Budgets, 2, "VK should have 2 budgets")

	// Delete
	store.DeleteVirtualKeyInMemory(context.Background(), "vk1")

	_, exists = store.budgets.Load("b1")
	assert.False(t, exists, "Budget b1 should be removed after delete")
	_, exists = store.budgets.Load("b2")
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
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)

	// A freshly-created VK loaded from DB carries no legacy rate-limit/budget — its
	// governance lives in a VK-scoped model config, so RateLimitID is nil and Budgets empty.
	// It does carry a provider config (with no per-provider governance of its own yet).
	vk := buildVirtualKey("vk1", "sk-bf-test", "Test VK", true)
	vk.ProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	}
	store.CreateVirtualKeyInMemory(context.Background(), vk)

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
	assert.Nil(t, tracked.RateLimit, "tracked VK rate limit must stay decoupled from caller mutation")
	assert.Nil(t, tracked.RateLimitID, "tracked VK rate limit ID must stay decoupled from caller mutation")
	assert.Empty(t, tracked.Budgets, "tracked VK budgets must stay decoupled from caller mutation")

	// Per-provider entries must stay decoupled too — this is the ProviderConfigs slice
	// aliasing greptile flagged (in-place element mutation through the shared backing array).
	require.Len(t, tracked.ProviderConfigs, 1)
	assert.Nil(t, tracked.ProviderConfigs[0].RateLimit, "tracked provider-config rate limit must stay decoupled")
	assert.Nil(t, tracked.ProviderConfigs[0].RateLimitID, "tracked provider-config rate limit ID must stay decoupled")
	assert.Empty(t, tracked.ProviderConfigs[0].Budgets, "tracked provider-config budgets must stay decoupled")
}

func TestGovernanceStore_UpdateVirtualKeyInMemory_RotatedValueRemovesOldLookup(t *testing.T) {
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("budget1", 100.0, 25.0, "1d")
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-old", "Test VK", budget)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	updated := *vk
	updated.Value = *schemas.NewSecretVar("sk-bf-new")
	store.UpdateVirtualKeyInMemory(context.Background(), &updated, nil, nil, nil)

	oldVK, oldFound := store.GetVirtualKey(context.Background(), "sk-bf-old")
	assert.False(t, oldFound)
	assert.Nil(t, oldVK)

	newVK, newFound := store.GetVirtualKey(context.Background(), "sk-bf-new")
	require.True(t, newFound)
	require.NotNil(t, newVK)
	assert.Equal(t, "vk1", newVK.ID)
	require.Len(t, newVK.Budgets, 1)
	assert.Equal(t, 25.0, newVK.Budgets[0].CurrentUsage)
}

func TestGovernanceStore_UpdateVirtualKeyInMemory_GracePeriodKeepsPreviousValue(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-old", "Test VK", true)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	// Rotation with cooldown: previous value stays valid until expiry.
	now := time.Now().UTC()
	exp := now.Add(5 * time.Minute)
	rotated := *vk
	rotated.Value = *schemas.NewSecretVar("sk-bf-new")
	rotated.PreviousValue = *schemas.NewSecretVar("sk-bf-old")
	rotated.PreviousValueExpiresAt = &exp
	rotated.RotatedAt = &now
	store.UpdateVirtualKeyInMemory(context.Background(), &rotated, nil, nil, nil)

	newVK, found := store.GetVirtualKey(context.Background(), "sk-bf-new")
	require.True(t, found)
	assert.Equal(t, "vk1", newVK.ID)

	oldVK, found := store.GetVirtualKey(context.Background(), "sk-bf-old")
	require.True(t, found, "previous value must authenticate during the grace period")
	assert.Equal(t, "vk1", oldVK.ID)

	byID, found := store.GetVirtualKeyByID(context.Background(), "vk1")
	require.True(t, found)
	assert.Equal(t, "sk-bf-new", byID.Value.GetValue())
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
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vkA, *vkB},
	}, nil, nil)
	require.NoError(t, err)

	// VK-A rotates away from the shared value into a live grace window.
	now := time.Now().UTC()
	exp := now.Add(5 * time.Minute)
	rotatedA := *vkA
	rotatedA.Value = *schemas.NewSecretVar("sk-bf-a-new")
	rotatedA.PreviousValue = *schemas.NewSecretVar(shared)
	rotatedA.PreviousValueExpiresAt = &exp
	rotatedA.RotatedAt = &now
	store.UpdateVirtualKeyInMemory(context.Background(), &rotatedA, nil, nil, nil)

	graceOwner, found := store.GetVirtualKey(context.Background(), shared)
	require.True(t, found, "grace value authenticates while no one else claims it")
	require.Equal(t, "vk-a", graceOwner.ID)

	// VK-B now takes the same value as its CURRENT credential, which the schema
	// permits because the previous-value hash index is non-unique.
	claimedB := *vkB
	claimedB.Value = *schemas.NewSecretVar(shared)
	store.UpdateVirtualKeyInMemory(context.Background(), &claimedB, nil, nil, nil)

	currentOwner, found := store.GetVirtualKey(context.Background(), shared)
	require.True(t, found)
	require.Equal(t, "vk-b", currentOwner.ID, "the key owning the value as its current credential must win")

	// Any later touch of VK-A re-registers its grace alias (a config reload, a
	// rename, an unrelated budget edit). That must not steal the value back.
	renamedA := rotatedA
	renamedA.Name = "Rotating VK renamed"
	store.UpdateVirtualKeyInMemory(context.Background(), &renamedA, nil, nil, nil)

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
// owning the shared value.
func sharedValueStore(t *testing.T, shared string) *LocalGovernanceStore {
	t.Helper()
	logger := NewMockLogger()
	vkA := buildVirtualKey("vk-a", shared, "Rotating VK", true)
	vkB := buildVirtualKey("vk-b", "sk-bf-b-original", "Owner VK", true)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vkA, *vkB},
	}, nil, nil)
	require.NoError(t, err)

	now := time.Now().UTC()
	exp := now.Add(5 * time.Minute)
	rotatedA := *vkA
	rotatedA.Value = *schemas.NewSecretVar("sk-bf-a-new")
	rotatedA.PreviousValue = *schemas.NewSecretVar(shared)
	rotatedA.PreviousValueExpiresAt = &exp
	rotatedA.RotatedAt = &now
	store.UpdateVirtualKeyInMemory(context.Background(), &rotatedA, nil, nil, nil)

	claimedB := *vkB
	claimedB.Value = *schemas.NewSecretVar(shared)
	store.UpdateVirtualKeyInMemory(context.Background(), &claimedB, nil, nil, nil)

	owner, found := store.GetVirtualKey(context.Background(), shared)
	require.True(t, found)
	require.Equal(t, "vk-b", owner.ID, "VK-B must own the shared value before the cleanup under test")
	return store
}

// TestGovernanceStore_ReRotationDoesNotEvictAnotherKeysCurrentValue covers the
// delete side of credential ownership. When VK-A rotates again, its reconcile
// loop drops the aliases it used to own - but the retired value now belongs to
// VK-B as a current credential, so removing it would silently deauthenticate a
// live key.
func TestGovernanceStore_ReRotationDoesNotEvictAnotherKeysCurrentValue(t *testing.T) {
	const shared = "sk-bf-shared-rerotate"
	store := sharedValueStore(t, shared)

	// VK-A rotates again with the cooldown disabled, so every alias it held
	// falls out of the keep set.
	later := time.Now().UTC().Add(time.Minute)
	rerotatedA := *buildVirtualKey("vk-a", "sk-bf-a-newest", "Rotating VK", true)
	rerotatedA.RotatedAt = &later
	rerotatedA.ClearPreviousValue()
	store.UpdateVirtualKeyInMemory(context.Background(), &rerotatedA, nil, nil, nil)

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
	store := sharedValueStore(t, shared)

	// VK-A still carries the shared value as its grace-period previous value.
	store.DeleteVirtualKeyInMemory(context.Background(), "vk-a")

	afterDelete, found := store.GetVirtualKey(context.Background(), shared)
	require.True(t, found, "deleting VK-A must not evict the value VK-B owns")
	assert.Equal(t, "vk-b", afterDelete.ID)

	_, found = store.GetVirtualKey(context.Background(), "sk-bf-a-new")
	assert.False(t, found, "the deleted key's own value must stop authenticating")
}

func TestGovernanceStore_GetVirtualKey_ExpiredPreviousValueRejectedAndCleaned(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-old", "Test VK", true)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	// Rotation with an active grace window (storeVirtualKey registers only
	// ACTIVE previous values), aged deterministically below via the stored
	// object: expiry is evaluated lazily at lookup time, so no sleep is needed.
	now := time.Now().UTC()
	exp := now.Add(5 * time.Minute)
	rotated := *vk
	rotated.Value = *schemas.NewSecretVar("sk-bf-new")
	rotated.PreviousValue = *schemas.NewSecretVar("sk-bf-old")
	rotated.PreviousValueExpiresAt = &exp
	rotated.RotatedAt = &now
	store.UpdateVirtualKeyInMemory(context.Background(), &rotated, nil, nil, nil)

	_, found := store.GetVirtualKey(context.Background(), "sk-bf-old")
	require.True(t, found, "grace value must work before expiry")

	storedVK, found := store.GetVirtualKey(context.Background(), "sk-bf-new")
	require.True(t, found)
	past := now.Add(-time.Millisecond)
	storedVK.PreviousValueExpiresAt = &past

	_, found = store.GetVirtualKey(context.Background(), "sk-bf-old")
	assert.False(t, found, "grace value must stop working after expiry")

	// Lazy cleanup must not touch the current value or the ID index.
	_, found = store.GetVirtualKey(context.Background(), "sk-bf-new")
	assert.True(t, found)
	_, found = store.GetVirtualKeyByID(context.Background(), "vk1")
	assert.True(t, found)

	// The expired entry itself is gone from the map.
	_, stale := store.virtualKeys.Load("sk-bf-old")
	assert.False(t, stale, "expired previous-value entry must be lazily deleted")
}

func TestGovernanceStore_UpdateVirtualKeyInMemory_UnresolvableValueRemovesIDIndex(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-live", "Test VK", true)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	_, found := store.GetVirtualKeyByID(context.Background(), "vk1")
	require.True(t, found)

	// Update to a value that cannot resolve (unset env ref). Fail closed: the
	// VK must become unavailable on BOTH lookup paths, not just the value map.
	updated := *vk
	updated.Value = *schemas.NewSecretVar("env.BIFROST_TEST_UNSET_VK_VALUE")
	store.UpdateVirtualKeyInMemory(context.Background(), &updated, nil, nil, nil)

	_, found = store.GetVirtualKey(context.Background(), "sk-bf-live")
	assert.False(t, found, "old value key must not authenticate after update")
	_, found = store.GetVirtualKeyByID(context.Background(), "vk1")
	assert.False(t, found, "ID index must fail closed when the new value cannot resolve")
}

func TestGovernanceStore_UpdateVirtualKeyInMemory_UnresolvableValueRemovesGraceKey(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-old", "Test VK", true)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	// Rotate with an active grace window so the previous value is registered.
	now := time.Now().UTC()
	exp := now.Add(5 * time.Minute)
	rotated := *vk
	rotated.Value = *schemas.NewSecretVar("sk-bf-new")
	rotated.PreviousValue = *schemas.NewSecretVar("sk-bf-old")
	rotated.PreviousValueExpiresAt = &exp
	rotated.RotatedAt = &now
	store.UpdateVirtualKeyInMemory(context.Background(), &rotated, nil, nil, nil)

	_, found := store.GetVirtualKey(context.Background(), "sk-bf-old")
	require.True(t, found, "grace value must authenticate before the broken update")

	// Update to a current value that cannot resolve (unset env ref) while the
	// grace window is still active. Fail closed: the VK must become unavailable
	// on EVERY lookup path, including the grace-period previous value.
	broken := rotated
	broken.Value = *schemas.NewSecretVar("env.BIFROST_TEST_UNSET_VK_VALUE")
	store.UpdateVirtualKeyInMemory(context.Background(), &broken, nil, nil, nil)

	_, found = store.GetVirtualKey(context.Background(), "sk-bf-new")
	assert.False(t, found, "old current value must not authenticate after the broken update")
	_, found = store.GetVirtualKey(context.Background(), "sk-bf-old")
	assert.False(t, found, "grace value must fail closed when the new value cannot resolve")
	_, found = store.GetVirtualKeyByID(context.Background(), "vk1")
	assert.False(t, found, "ID index must fail closed when the new value cannot resolve")
	_, stale := store.virtualKeys.Load("sk-bf-old")
	assert.False(t, stale, "previous-value entry must be removed from the map")
}

func TestGovernanceStore_UpdateVirtualKeyInMemory_RepeatedRotationDropsStalePrevious(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-v1", "Test VK", true)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	now := time.Now().UTC()
	exp := now.Add(5 * time.Minute)

	first := *vk
	first.Value = *schemas.NewSecretVar("sk-bf-v2")
	first.PreviousValue = *schemas.NewSecretVar("sk-bf-v1")
	first.PreviousValueExpiresAt = &exp
	first.RotatedAt = &now
	store.UpdateVirtualKeyInMemory(context.Background(), &first, nil, nil, nil)

	// Second rotation inside the grace window: v2 becomes the grace value and v1
	// dies immediately (only one grace value at a time).
	second := first
	second.Value = *schemas.NewSecretVar("sk-bf-v3")
	second.PreviousValue = *schemas.NewSecretVar("sk-bf-v2")
	store.UpdateVirtualKeyInMemory(context.Background(), &second, nil, nil, nil)

	_, found := store.GetVirtualKey(context.Background(), "sk-bf-v3")
	assert.True(t, found)
	_, found = store.GetVirtualKey(context.Background(), "sk-bf-v2")
	assert.True(t, found, "newest previous value must authenticate")
	_, found = store.GetVirtualKey(context.Background(), "sk-bf-v1")
	assert.False(t, found, "second-oldest value must die on repeated rotation")
	_, stale := store.virtualKeys.Load("sk-bf-v1")
	assert.False(t, stale, "stale previous key must be removed from the map")
}

func TestGovernanceStore_DeleteVirtualKeyInMemory_RemovesGraceKey(t *testing.T) {
	logger := NewMockLogger()
	vk := buildVirtualKey("vk1", "sk-bf-old", "Test VK", true)
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	now := time.Now().UTC()
	exp := now.Add(5 * time.Minute)
	rotated := *vk
	rotated.Value = *schemas.NewSecretVar("sk-bf-new")
	rotated.PreviousValue = *schemas.NewSecretVar("sk-bf-old")
	rotated.PreviousValueExpiresAt = &exp
	rotated.RotatedAt = &now
	store.UpdateVirtualKeyInMemory(context.Background(), &rotated, nil, nil, nil)

	store.DeleteVirtualKeyInMemory(context.Background(), "vk1")

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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil, nil)
	require.NoError(t, err)

	// Test updating tokens
	err = chargeGrantRateLimits(store, context.Background(), vk, schemas.OpenAI, 500, true, false)
	assert.NoError(t, err, "Rate limit update should succeed")

	// Retrieve the updated rate limit from the main RateLimits map
	governanceData := store.GetGovernanceData(context.Background())
	updatedRateLimit, exists := governanceData.RateLimits["rl1"]
	require.True(t, exists, "Rate limit should exist")
	require.NotNil(t, updatedRateLimit)

	assert.Equal(t, int64(500), updatedRateLimit.TokenCurrentUsage, "Token usage should be updated")
	assert.Equal(t, int64(0), updatedRateLimit.RequestCurrentUsage, "Request usage should not change")

	// Test updating requests
	err = chargeGrantRateLimits(store, context.Background(), vk, schemas.OpenAI, 0, false, true)
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil, nil)
	require.NoError(t, err)

	// Reset expired rate limits
	expiredRateLimits := store.ResetExpiredRateLimitsInMemory(context.Background(), true)
	err = store.ResetExpiredRateLimits(context.Background(), expiredRateLimits)
	assert.NoError(t, err, "Reset should succeed")

	// Retrieve the updated VK to check rate limit changes
	updatedVK, _ := store.GetVirtualKey(context.Background(), "sk-bf-test")
	require.NotNil(t, updatedVK)
	require.NotNil(t, updatedVK.RateLimit)

	assert.Equal(t, int64(0), updatedVK.RateLimit.TokenCurrentUsage, "Token usage should be reset")
	assert.Equal(t, int64(0), updatedVK.RateLimit.RequestCurrentUsage, "Request usage should be reset")
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	// Reset expired budgets
	expiredBudgets := store.ResetExpiredBudgetsInMemory(context.Background(), true)
	err = store.ResetExpiredBudgets(context.Background(), expiredBudgets)
	assert.NoError(t, err, "Reset should succeed")

	// Retrieve the updated VK to check budget changes
	updatedVK, _ := store.GetVirtualKey(context.Background(), "sk-bf-test")
	require.NotNil(t, updatedVK)
	require.True(t, len(updatedVK.Budgets) > 0, "VK should have budgets")

	assert.Equal(t, 0.0, updatedVK.Budgets[0].CurrentUsage, "Budget usage should be reset")
}

// TestGovernanceStoreResetBudgetAdvancesOverride verifies each existing reset consumes one finite cycle.
func TestGovernanceStoreResetBudgetAdvancesOverride(t *testing.T) {
	store := newStandaloneStore(t)
	finite := buildBudgetWithUsage("finite-override", 100, 75, "1d")
	require.NoError(t, finite.SetOverride(25, configstoreTables.BudgetOverrideModeCycles, 2))
	store.budgets.Store(finite.ID, finite)

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
	store.budgets.Store(permanent.ID, permanent)
	reset, ok = store.ResetBudgetAt(context.Background(), permanent.ID, permanent.LastReset.Add(24*time.Hour))
	require.True(t, ok)
	assert.True(t, reset.HasActiveOverride())
	assert.Equal(t, 150.0, reset.EffectiveMaxLimit())
}

// TestGovernanceStoreUpsertBudgetConfigRefreshesOverride verifies cache refreshes config without clobbering runtime counters.
func TestGovernanceStoreUpsertBudgetConfigRefreshesOverride(t *testing.T) {
	store := newStandaloneStore(t)
	lastReset := time.Now().Add(-30 * time.Minute)
	live := buildBudgetWithUsage("cache-override", 100, 40, "1h")
	live.LastReset = lastReset
	store.budgets.Store(live.ID, live)

	refreshed := buildBudgetWithUsage(live.ID, 120, 0, "2h")
	require.NoError(t, refreshed.SetOverride(30, configstoreTables.BudgetOverrideModeCycles, 3))
	store.UpsertBudgetConfig(context.Background(), live.ID, refreshed)

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
	configStore, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: t.TempDir() + "/governance.db"},
	}, logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, configStore.Close(ctx)) })

	budget := buildBudgetWithUsage("persisted-override", 100, 75, "1d")
	require.NoError(t, budget.SetOverride(25, configstoreTables.BudgetOverrideModeCycles, 2))
	require.NoError(t, configStore.CreateBudget(ctx, budget))

	store, err := NewLocalGovernanceStore(ctx, logger, configStore, nil, nil, nil)
	require.NoError(t, err)
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Budgets: budgets,
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
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)

	// Create a rate limit with 1000 token limit
	limit := int64(1000)
	rateLimitID := "provider:openai:ratelimit"
	rl := &configstoreTables.TableRateLimit{
		ID:                rateLimitID,
		TokenMaxLimit:     &limit,
		TokenCurrentUsage: 500,
	}

	store.rateLimits.Store(rateLimitID, rl)

	// Create a provider config that references the rate limit
	providerConfig := &configstoreTables.TableProvider{
		Name:        "openai",
		RateLimitID: &rateLimitID,
	}
	store.providers.Store("openai", providerConfig)

	// Get status
	status := store.GetBudgetAndRateLimitStatus(emptyCtx(), schemas.ModelProvider("openai"), "", nil, nil, nil)

	assert.NotNil(t, status)
	assert.Equal(t, 50.0, status.RateLimitTokenPercentUsed)

	// Update usage to exhausted state
	rl.TokenCurrentUsage = 1000
	status = store.GetBudgetAndRateLimitStatus(emptyCtx(), schemas.ModelProvider("openai"), "", nil, nil, nil)

	assert.Equal(t, 100.0, status.RateLimitTokenPercentUsed)
}

// Routing can ask for status on a request nothing has resolved access for yet. That is not an error
// and must not panic: the deployment's own limits still apply, and there is simply no holder to add.
func TestGovernanceStore_StatusWithNoResolvedAccess(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)

	limit := int64(1000)
	rateLimitID := "provider:openai:ratelimit"
	store.rateLimits.Store(rateLimitID, &configstoreTables.TableRateLimit{
		ID:                rateLimitID,
		TokenMaxLimit:     &limit,
		TokenCurrentUsage: 500,
	})
	store.providers.Store("openai", &configstoreTables.TableProvider{Name: "openai", RateLimitID: &rateLimitID})

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
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)

	budgetID := "provider:openai:budget"
	budget := &configstoreTables.TableBudget{
		ID:           budgetID,
		MaxLimit:     100.0,
		CurrentUsage: 60.0,
	}

	store.budgets.Store(budgetID, budget)

	// Create a provider config that references the budget
	providerConfig := &configstoreTables.TableProvider{
		Name:     "openai",
		BudgetID: &budgetID,
	}
	store.providers.Store("openai", providerConfig)

	// Get status
	status := store.GetBudgetAndRateLimitStatus(emptyCtx(), schemas.ModelProvider("openai"), "", nil, nil, nil)

	assert.NotNil(t, status)
	assert.Equal(t, 60.0, status.BudgetPercentUsed)

	// Update usage to exhausted state
	budget.CurrentUsage = 100.0
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	store.virtualKeys.Store(vkValue, vk)

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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	store.virtualKeys.Store(vkValue, vk)

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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*mc},
		Budgets:      []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	store.virtualKeys.Store(vkValue, vk)

	// Settled the way the funnel settles it: the grant carries the attempt's limits, and the
	// accounted set is read from there.
	ctx := resolverCtx(store, vkValue)
	limits := resolveLimits(ctx, store, schemas.ModelProvider("anthropic"), "")
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
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
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)

	store.CreateTeamInMemory(context.Background(), buildTeam("team-1", "Platform", nil))
	store.CreateCustomerInMemory(context.Background(), buildCustomer("cust-1", "ACME", nil))

	assert.Equal(t, "Platform", store.GetTeamName(context.Background(), "team-1"))
	assert.Equal(t, "ACME", store.GetCustomerName(context.Background(), "cust-1"))

	assert.Empty(t, store.GetTeamName(context.Background(), "unknown"))
	assert.Empty(t, store.GetCustomerName(context.Background(), "unknown"))
	assert.Empty(t, store.GetTeamName(context.Background(), ""))
	assert.Empty(t, store.GetCustomerName(context.Background(), ""))
}

// TestGovernanceStore_Customer_CalendarAligned_CreateInMemory verifies that
// CreateCustomerInMemory stamps IsCalendarAligned on the in-memory budget and
// rate limit so ResetExpiredBudgetsInMemory uses the calendar-aligned reset path.
func TestGovernanceStore_Customer_CalendarAligned_CreateInMemory(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)

	budgetID := "cust-bud-1"
	rlID := "cust-rl-1"
	budget := &configstoreTables.TableBudget{
		ID:            budgetID,
		MaxLimit:      100.0,
		ResetDuration: "1M",
		LastReset:     time.Now(),
	}
	rl := &configstoreTables.TableRateLimit{
		ID:               rlID,
		TokenMaxLimit:    ptrInt64(1000),
		TokenLastReset:   time.Now(),
		RequestLastReset: time.Now(),
	}
	customer := buildCustomer("cust-1", "ACME", budget)
	customer.CalendarAligned = true
	customer.RateLimit = rl
	customer.RateLimitID = &rlID

	store.CreateCustomerInMemory(context.Background(), customer)

	rawBudget, ok := store.budgets.Load(budgetID)
	require.True(t, ok, "budget should be in memory after create")
	storedBudget, ok := rawBudget.(*configstoreTables.TableBudget)
	require.True(t, ok)
	assert.True(t, storedBudget.IsCalendarAligned, "budget.IsCalendarAligned should be true when customer.CalendarAligned=true")

	rawRL, ok := store.rateLimits.Load(rlID)
	require.True(t, ok, "rate limit should be in memory after create")
	storedRL, ok := rawRL.(*configstoreTables.TableRateLimit)
	require.True(t, ok)
	assert.True(t, storedRL.IsCalendarAligned, "rate_limit.IsCalendarAligned should be true when customer.CalendarAligned=true")
}

// TestGovernanceStore_Customer_CalendarAligned_CreateInMemory_False verifies that
// IsCalendarAligned is false when the customer does not have calendar alignment enabled.
func TestGovernanceStore_Customer_CalendarAligned_CreateInMemory_False(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)

	budgetID := "cust-bud-2"
	budget := &configstoreTables.TableBudget{
		ID:            budgetID,
		MaxLimit:      50.0,
		ResetDuration: "1d",
		LastReset:     time.Now(),
	}
	customer := buildCustomer("cust-2", "Globex", budget)
	customer.CalendarAligned = false

	store.CreateCustomerInMemory(context.Background(), customer)

	rawBudget, ok := store.budgets.Load(budgetID)
	require.True(t, ok)
	storedBudget, ok := rawBudget.(*configstoreTables.TableBudget)
	require.True(t, ok)
	assert.False(t, storedBudget.IsCalendarAligned, "budget.IsCalendarAligned should be false when customer.CalendarAligned=false")
}

// TestGovernanceStore_Customer_CalendarAligned_UpdateInMemory verifies that
// UpdateCustomerInMemory re-stamps IsCalendarAligned on the budget and rate limit
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
		ID:               rlID,
		TokenMaxLimit:    ptrInt64(500),
		TokenLastReset:   time.Now(),
		RequestLastReset: time.Now(),
	}
	customer := buildCustomer("cust-3", "Initech", budget)
	customer.CalendarAligned = false
	customer.RateLimit = rl
	customer.RateLimitID = &rlID

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Customers: []configstoreTables.TableCustomer{*customer},
		Budgets:   []configstoreTables.TableBudget{*budget},
	}, nil, nil)
	require.NoError(t, err)

	// Budget and rate limit should start as non-calendar-aligned
	rawBudget, _ := store.budgets.Load(budgetID)
	assert.False(t, rawBudget.(*configstoreTables.TableBudget).IsCalendarAligned)

	// Toggle calendar_aligned to true and update in memory
	customer.CalendarAligned = true
	store.UpdateCustomerInMemory(context.Background(), customer, nil)

	rawBudget, ok := store.budgets.Load(budgetID)
	require.True(t, ok)
	assert.True(t, rawBudget.(*configstoreTables.TableBudget).IsCalendarAligned, "budget.IsCalendarAligned should be true after update with CalendarAligned=true")

	rawRL, ok := store.rateLimits.Load(rlID)
	require.True(t, ok)
	assert.True(t, rawRL.(*configstoreTables.TableRateLimit).IsCalendarAligned, "rate_limit.IsCalendarAligned should be true after update with CalendarAligned=true")
}

// Utility functions for tests
func ptrInt64(i int64) *int64 {
	return &i
}

// An access profile's per-model budgets materialise as user-scoped model configs
// naming one model and provider. A batch create carries no top-level model, so the
// in-flight collection matches only the wildcard tiers and those per-model budgets
// are invisible to it — which is why batch spend never reached them. Settlement
// knows the models and asks for them by name.
func TestCollectModelScopedGovernanceIDs_FindsExactModelConfigMissedAtCreateTime(t *testing.T) {
	logger := NewMockLogger()
	ctx := context.Background()
	providerName := "openai"
	userID := "user-alice"

	perModel := buildBudgetWithUsage("ap-model-budget", 100.0, 0.0, "1M")
	perModelMC := buildModelConfig("mc-user-gpt5", "gpt-5", &providerName, perModel, nil)
	perModelMC.Scope = configstoreTables.ModelConfigScopeUser
	perModelMC.ScopeID = &userID

	wildcard := buildBudgetWithUsage("ap-wildcard-budget", 100.0, 0.0, "1M")
	wildcardMC := buildModelConfig("mc-user-all", configstoreTables.ModelConfigAllModels, &providerName, wildcard, nil)
	wildcardMC.Scope = configstoreTables.ModelConfigScopeUser
	wildcardMC.ScopeID = &userID

	store, err := NewLocalGovernanceStore(ctx, logger, nil, &configstore.GovernanceConfig{
		ModelConfigs: []configstoreTables.TableModelConfig{*perModelMC, *wildcardMC},
		Budgets:      []configstoreTables.TableBudget{*perModel, *wildcard},
	}, nil, nil)
	require.NoError(t, err)

	// What batch create settles onto the request: no model, so only the wildcard is gathered. The
	// user is a scope of its own, read off the request rather than off a permit, so no access is
	// needed to reach it.
	inFlightCtx := grantedCtx(ctx)
	inFlightCtx.SetValue(schemas.BifrostContextKeyUserID, userID)
	inFlightBudgets, _ := gatherLimits(inFlightCtx, store, nil, schemas.ModelProvider(providerName), "")
	inFlight := limitIDsOf(inFlightBudgets)
	assert.Contains(t, inFlight, wildcard.ID)
	assert.NotContains(t, inFlight, perModel.ID, "the per-model budget cannot be matched without a model")

	// What settlement sees: the model is known, so the per-model budget is reachable.
	atSettlement, _ := store.CollectModelScopedGovernanceIDs(ctx, "", userID, schemas.ModelProvider(providerName), "gpt-5")
	assert.Contains(t, atSettlement, perModel.ID)
	// The wildcard is returned too and overlaps the in-flight set by design; callers
	// subtract what they already charged rather than relying on it being absent.
	assert.Contains(t, atSettlement, wildcard.ID)

	// A model with no config of its own still finds the wildcard and nothing else.
	otherModel, _ := store.CollectModelScopedGovernanceIDs(ctx, "", userID, schemas.ModelProvider(providerName), "gpt-4o")
	assert.NotContains(t, otherModel, perModel.ID)
	assert.Contains(t, otherModel, wildcard.ID)

	// Scope isolation: another user's id must not reach these configs.
	otherUser, _ := store.CollectModelScopedGovernanceIDs(ctx, "", "user-bob", schemas.ModelProvider(providerName), "gpt-5")
	assert.Empty(t, otherUser)
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
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
		limits := resolveLimits(resolverCtx(store, "sk-bf-collect"), store, provider, model)
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

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
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

		openaiBudgets, _ := store.ProviderLimits(ctx, access.Bases()[0], schemas.OpenAI)
		bedrockBudgets, _ := store.ProviderLimits(ctx, access.Bases()[0], schemas.Bedrock)
		assert.Equal(t, []string{"b-key-openai"}, limitIDsOf(openaiBudgets))
		assert.Equal(t, []string{"b-key-bedrock"}, limitIDsOf(bedrockBudgets))
		assert.Nil(t, ctx.Grant().Limits(), "nothing is settled yet, which is not the same as nothing applying")
	})

	t.Run("after it runs, the grant carries exactly what this pair answers to", func(t *testing.T) {
		ctx := resolverCtx(store, "sk-bf-resolve")

		settled := resolveLimits(ctx, store, schemas.OpenAI, "gpt-4o")

		require.NotNil(t, settled)
		assert.ElementsMatch(t,
			[]string{"b-provider", "b-model", "b-key", "b-key-openai", "b-team"},
			limitIDsOf(settled.Budgets()))
		assert.NotContains(t, limitIDsOf(settled.Budgets()), "b-key-bedrock",
			"a provider this request is not using is not funding it")
	})

	t.Run("the settled limits are what everything downstream reads", func(t *testing.T) {
		ctx := resolverCtx(store, "sk-bf-resolve")

		settled := resolveLimits(ctx, store, schemas.OpenAI, "gpt-4o")

		assert.Same(t, settled, ctx.Grant().Limits(),
			"recorded, or the check and the charge would each resolve their own")
	})

	t.Run("the next attempt replaces them whole", func(t *testing.T) {
		// A request that fails over changes its provider, not its caller: the access stays, and the
		// limits are settled again for the pair the new attempt uses.
		ctx := resolverCtx(store, "sk-bf-resolve")
		access := ctx.Grant().Access()

		first := resolveLimits(ctx, store, schemas.OpenAI, "gpt-4o")
		second := resolveLimits(ctx, store, schemas.Bedrock, "claude-sonnet-4")

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

		settled := resolveLimits(ctx, store, schemas.OpenAI, "gpt-4o")

		require.NotNil(t, settled)
		assert.ElementsMatch(t, []string{"b-provider", "b-model"}, limitIDsOf(settled.Budgets()))
		assert.Nil(t, ctx.Grant().Access(), "nothing resolved is not access permitting nothing")
	})

	t.Run("a context carrying no grant has nowhere to settle them", func(t *testing.T) {
		// The transport installs a grant on every request; a context without one is a wiring fault,
		// and nothing here papers over it.
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

		assert.Nil(t, resolveLimits(ctx, store, schemas.OpenAI, "gpt-4o"))
		assert.Nil(t, ctx.Grant())
	})

	t.Run("a modelless attempt still answers to its provider", func(t *testing.T) {
		ctx := resolverCtx(store, "sk-bf-resolve")

		settled := resolveLimits(ctx, store, schemas.OpenAI, "")

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
		store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, cfg, nil, nil)
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
