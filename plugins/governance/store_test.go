package governance

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGovernanceStore_GetVirtualKey tests lock-free VK retrieval
func TestGovernanceStore_GetVirtualKey(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{
			*buildVirtualKey("vk1", "sk-bf-test1", "Test VK 1", true),
			*buildVirtualKey("vk2", "sk-bf-test2", "Test VK 2", false),
		},
	}, nil)
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
	}, nil)
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
	}, nil)
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
			}, nil)

			testVK, _ = testStore.GetVirtualKey(context.Background(), "sk-bf-test")
			_, err := testStore.CheckVirtualKeyBudget(context.Background(), testVK, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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
	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, &configstore.GovernanceConfig{}, nil)
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
	}, nil)
	require.NoError(t, err)

	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")

	// Test: All budgets under limit should pass
	_, err = store.CheckVirtualKeyBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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
	_, err = store.CheckVirtualKeyBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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
	}, nil)
	require.NoError(t, err)

	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	_, err = store.CheckVirtualKeyBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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
	}, nil)
	require.NoError(t, err)

	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	_, err = store.CheckVirtualKeyBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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
	}, nil)
	require.NoError(t, err)

	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	_, err = store.CheckVirtualKeyBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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
	}, nil)
	require.NoError(t, err)

	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")

	// Simulate a $3.50 request
	err = store.UpdateVirtualKeyBudgetUsageInMemory(context.Background(), vk, schemas.OpenAI, 3.50)
	require.NoError(t, err)

	// Both budgets should reflect the cost
	hourlyVal, exists := store.budgets.Load("hourly")
	require.True(t, exists)
	assert.InDelta(t, 3.50, hourlyVal.(*configstoreTables.TableBudget).CurrentUsage, 0.01, "Hourly budget should reflect usage")

	dailyVal, exists := store.budgets.Load("daily")
	require.True(t, exists)
	assert.InDelta(t, 3.50, dailyVal.(*configstoreTables.TableBudget).CurrentUsage, 0.01, "Daily budget should reflect usage")

	// Second request: $7.00 — should push hourly over limit
	err = store.UpdateVirtualKeyBudgetUsageInMemory(context.Background(), vk, schemas.OpenAI, 7.00)
	require.NoError(t, err)

	hourlyVal, _ = store.budgets.Load("hourly")
	assert.InDelta(t, 10.50, hourlyVal.(*configstoreTables.TableBudget).CurrentUsage, 0.01, "Hourly budget should accumulate")

	dailyVal, _ = store.budgets.Load("daily")
	assert.InDelta(t, 10.50, dailyVal.(*configstoreTables.TableBudget).CurrentUsage, 0.01, "Daily budget should accumulate")

	// Now CheckBudget should fail (hourly exceeded)
	_, err = store.CheckVirtualKeyBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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
	}, nil)
	require.NoError(t, err)

	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	_, err = store.CheckVirtualKeyBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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
	}, nil)
	require.NoError(t, err)

	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")

	// Provider config budget exceeded → should block even though VK budget is fine
	_, err = store.CheckVirtualKeyBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)
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
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)
	ctx := &schemas.BifrostContext{}

	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)
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
	}, nil)
	require.NoError(t, err)

	resolver := NewBudgetResolver(store, nil, logger, nil)

	// Request 1: $0.80 — both budgets fine
	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	err = store.UpdateVirtualKeyBudgetUsageInMemory(context.Background(), vk, schemas.OpenAI, 0.80)
	require.NoError(t, err)

	ctx := &schemas.BifrostContext{}
	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)
	assertDecision(t, DecisionAllow, result)

	// Request 2: $0.80 — still fine ($1.60 total)
	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	err = store.UpdateVirtualKeyBudgetUsageInMemory(context.Background(), vk, schemas.OpenAI, 0.80)
	require.NoError(t, err)

	ctx = &schemas.BifrostContext{}
	result = resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)
	assertDecision(t, DecisionAllow, result)

	// Request 3: $0.80 — pushes hourly to $2.40 > $2.00 limit → blocked
	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	err = store.UpdateVirtualKeyBudgetUsageInMemory(context.Background(), vk, schemas.OpenAI, 0.80)
	require.NoError(t, err)

	ctx = &schemas.BifrostContext{}
	result = resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)
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
	}, nil)
	require.NoError(t, err)

	// Verify VK-level calendar_aligned is set
	vk, _ = store.GetVirtualKey(context.Background(), "sk-bf-test")
	assert.True(t, vk.CalendarAligned, "VK should have calendar_aligned=true")

	// Both under limit — should pass
	_, err = store.CheckVirtualKeyBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
	assert.NoError(t, err)
}

// TestGovernanceStore_MultiBudget_InMemoryCreateAndDelete tests CreateVirtualKeyInMemory and DeleteVirtualKeyInMemory
// properly store and clean up multi-budget entries
func TestGovernanceStore_MultiBudget_InMemoryCreateAndDelete(t *testing.T) {
	logger := NewMockLogger()

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
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
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
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
	}, nil)
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

// TestGovernanceStore_UpdateRateLimitUsage_TokensAndRequests tests atomic rate limit usage updates
func TestGovernanceStore_UpdateRateLimitUsage_TokensAndRequests(t *testing.T) {
	logger := NewMockLogger()

	rateLimit := buildRateLimitWithUsage("rl1", 10000, 0, 1000, 0)
	vk := buildVirtualKeyWithRateLimit("vk1", "sk-bf-test", "Test VK", rateLimit)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	require.NoError(t, err)

	// Test updating tokens
	err = store.UpdateVirtualKeyRateLimitUsageInMemory(context.Background(), vk, schemas.OpenAI, 500, true, false)
	assert.NoError(t, err, "Rate limit update should succeed")

	// Retrieve the updated rate limit from the main RateLimits map
	governanceData := store.GetGovernanceData(context.Background())
	updatedRateLimit, exists := governanceData.RateLimits["rl1"]
	require.True(t, exists, "Rate limit should exist")
	require.NotNil(t, updatedRateLimit)

	assert.Equal(t, int64(500), updatedRateLimit.TokenCurrentUsage, "Token usage should be updated")
	assert.Equal(t, int64(0), updatedRateLimit.RequestCurrentUsage, "Request usage should not change")

	// Test updating requests
	err = store.UpdateVirtualKeyRateLimitUsageInMemory(context.Background(), vk, schemas.OpenAI, 0, false, true)
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
	}, nil)
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
	}, nil)
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

	store, err := NewLocalGovernanceStore(ctx, logger, configStore, nil, nil)
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
	}, nil)
	require.NoError(t, err)

	allBudgets := store.GetGovernanceData(context.Background()).Budgets
	assert.Equal(t, 3, len(allBudgets), "Should have 3 budgets")
	assert.NotNil(t, allBudgets["budget1"])
	assert.NotNil(t, allBudgets["budget2"])
	assert.NotNil(t, allBudgets["budget3"])
}

// TestGovernanceStore_RoutingRules_CreateAndRetrieve tests creating and retrieving routing rules
func TestGovernanceStore_RoutingRules_CreateAndRetrieve(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	// Create a global routing rule
	rule1 := &configstoreTables.TableRoutingRule{
		ID:            "1",
		Name:          "Global Rule",
		Description:   "Test global routing rule",
		Enabled:       bifrost.Ptr(true),
		CelExpression: "model == 'gpt-4o'",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("openai"), Model: bifrost.Ptr("gpt-4"), Weight: 1.0},
		},
		Fallbacks:       nil,
		ParsedFallbacks: []string{"azure/gpt-4-turbo"},
		Scope:           "global",
		ScopeID:         nil,
		Priority:        10,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	// Create a team-scoped routing rule
	teamID := "team-123"
	rule2 := &configstoreTables.TableRoutingRule{
		ID:            "2",
		Name:          "Team Rule",
		Description:   "Test team routing rule",
		Enabled:       bifrost.Ptr(true),
		CelExpression: "model in ['gpt-4o', 'gpt-4-turbo']",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("azure"), Weight: 1.0},
		},
		Fallbacks:       nil,
		ParsedFallbacks: []string{"groq/mixtral-8x7b"},
		Scope:           "team",
		ScopeID:         &teamID,
		Priority:        20,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	// Store rules in memory
	err = store.UpdateRoutingRuleInMemory(context.Background(), rule1)
	require.NoError(t, err)
	err = store.UpdateRoutingRuleInMemory(context.Background(), rule2)
	require.NoError(t, err)

	// Test retrieval by scope
	globalRules := store.GetScopedRoutingRules(context.Background(), "global", "")
	assert.Equal(t, 1, len(globalRules))
	assert.Equal(t, "Global Rule", globalRules[0].Name)

	teamRules := store.GetScopedRoutingRules(context.Background(), "team", teamID)
	assert.Equal(t, 1, len(teamRules))
	assert.Equal(t, "Team Rule", teamRules[0].Name)

	// Test ListRoutingRules
	allRules := store.GetAllRoutingRules(context.Background())
	assert.Equal(t, 2, len(allRules))
}

// TestGovernanceStore_RoutingRules_PriorityOrdering tests that rules are sorted by priority
func TestGovernanceStore_RoutingRules_PriorityOrdering(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	// Create rules with different priorities
	rules := []*configstoreTables.TableRoutingRule{
		{
			ID:       "1",
			Name:     "Priority 5",
			Priority: 5,
			Scope:    "global",
			ScopeID:  nil,
			Enabled:  bifrost.Ptr(true),
		},
		{
			ID:       "2",
			Name:     "Priority 20",
			Priority: 20,
			Scope:    "global",
			ScopeID:  nil,
			Enabled:  bifrost.Ptr(true),
		},
		{
			ID:       "3",
			Name:     "Priority 10",
			Priority: 10,
			Scope:    "global",
			ScopeID:  nil,
			Enabled:  bifrost.Ptr(true),
		},
	}

	for _, rule := range rules {
		err := store.UpdateRoutingRuleInMemory(context.Background(), rule)
		require.NoError(t, err)
	}

	// Retrieve and verify ordering (sorted by priority ASC, so lower numbers first)
	retrieved := store.GetScopedRoutingRules(context.Background(), "global", "")
	assert.Equal(t, 3, len(retrieved))
	assert.Equal(t, 5, retrieved[0].Priority)
	assert.Equal(t, 10, retrieved[1].Priority)
	assert.Equal(t, 20, retrieved[2].Priority)
}

// TestGovernanceStore_RoutingRules_DisabledRulesFiltered tests that disabled rules are filtered out
func TestGovernanceStore_RoutingRules_DisabledRulesFiltered(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	enabledRule := &configstoreTables.TableRoutingRule{
		ID:      "1",
		Name:    "Enabled Rule",
		Enabled: bifrost.Ptr(true),
		Scope:   "global",
		ScopeID: nil,
	}

	disabledRule := &configstoreTables.TableRoutingRule{
		ID:      "2",
		Name:    "Disabled Rule",
		Enabled: bifrost.Ptr(false),
		Scope:   "global",
		ScopeID: nil,
	}

	err = store.UpdateRoutingRuleInMemory(context.Background(), enabledRule)
	require.NoError(t, err)
	err = store.UpdateRoutingRuleInMemory(context.Background(), disabledRule)
	require.NoError(t, err)

	// Only enabled rules should be returned
	retrieved := store.GetScopedRoutingRules(context.Background(), "global", "")
	assert.Equal(t, 1, len(retrieved))
	assert.Equal(t, "Enabled Rule", retrieved[0].Name)
}

// TestGovernanceStore_RoutingRules_DeleteRule tests deleting a routing rule
func TestGovernanceStore_RoutingRules_DeleteRule(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:      "1",
		Name:    "Test Rule",
		Enabled: bifrost.Ptr(true),
		Scope:   "global",
		ScopeID: nil,
	}

	// Add rule
	err = store.UpdateRoutingRuleInMemory(context.Background(), rule)
	require.NoError(t, err)

	retrieved := store.GetScopedRoutingRules(context.Background(), "global", "")
	assert.Equal(t, 1, len(retrieved))

	// Delete rule
	err = store.DeleteRoutingRuleInMemory(context.Background(), rule.ID)
	require.NoError(t, err)

	// Verify deletion
	retrieved = store.GetScopedRoutingRules(context.Background(), "global", "")
	assert.Equal(t, 0, len(retrieved))
}

// TestGovernanceStore_RateLimitStatus tests rate limit status calculation
func TestGovernanceStore_RateLimitStatus(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
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
	status := store.GetBudgetAndRateLimitStatus(context.Background(), "", schemas.ModelProvider("openai"), nil, nil, nil, nil)

	assert.NotNil(t, status)
	assert.Equal(t, 50.0, status.RateLimitTokenPercentUsed)

	// Update usage to exhausted state
	rl.TokenCurrentUsage = 1000
	status = store.GetBudgetAndRateLimitStatus(context.Background(), "", schemas.ModelProvider("openai"), nil, nil, nil, nil)

	assert.Equal(t, 100.0, status.RateLimitTokenPercentUsed)
}

// TestGovernanceStore_BudgetStatus tests budget status calculation
func TestGovernanceStore_BudgetStatus(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
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
	status := store.GetBudgetAndRateLimitStatus(context.Background(), "", schemas.ModelProvider("openai"), nil, nil, nil, nil)

	assert.NotNil(t, status)
	assert.Equal(t, 60.0, status.BudgetPercentUsed)

	// Update usage to exhausted state
	budget.CurrentUsage = 100.0
	status = store.GetBudgetAndRateLimitStatus(context.Background(), "", schemas.ModelProvider("openai"), nil, nil, nil, nil)

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
	}, nil)
	require.NoError(t, err)

	store.virtualKeys.Store(vkValue, vk)

	status := store.GetBudgetAndRateLimitStatus(context.Background(), "gpt-5", schemas.ModelProvider(providerName), vk, nil, nil, nil)

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
	}, nil)
	require.NoError(t, err)

	store.virtualKeys.Store(vkValue, vk)

	// Query for anthropic — should not see the openai-scoped budget.
	status := store.GetBudgetAndRateLimitStatus(context.Background(), "claude-3-5-sonnet", schemas.ModelProvider("anthropic"), vk, nil, nil, nil)

	require.NotNil(t, status)
	assert.Equal(t, 0.0, status.BudgetPercentUsed, "openai VK-scoped budget must not appear for anthropic requests")
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
	}, nil)
	require.NoError(t, err)

	status := store.GetBudgetAndRateLimitStatus(context.Background(), "gpt-5", schemas.ModelProvider(providerName), nil, nil, nil, nil)

	require.NotNil(t, status)
	assert.Equal(t, 75.0, status.BudgetPercentUsed, "global model+provider budget must be visible to routing status")
}

// TestGovernanceStore_RoutingRules_MultipleScopes tests rules with multiple scopes
func TestGovernanceStore_RoutingRules_MultipleScopes(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	customerID := "cust-123"
	teamID := "team-456"

	// Create rules for different scopes
	globalRule := &configstoreTables.TableRoutingRule{
		ID: "1", Name: "Global", Scope: "global", ScopeID: nil, Priority: 10, Enabled: bifrost.Ptr(true),
	}
	customerRule := &configstoreTables.TableRoutingRule{
		ID: "2", Name: "Customer", Scope: "customer", ScopeID: &customerID, Priority: 20, Enabled: bifrost.Ptr(true),
	}
	teamRule := &configstoreTables.TableRoutingRule{
		ID: "3", Name: "Team", Scope: "team", ScopeID: &teamID, Priority: 30, Enabled: bifrost.Ptr(true),
	}

	require.NoError(t, store.UpdateRoutingRuleInMemory(context.Background(), globalRule))
	require.NoError(t, store.UpdateRoutingRuleInMemory(context.Background(), customerRule))
	require.NoError(t, store.UpdateRoutingRuleInMemory(context.Background(), teamRule))

	// Test global scope
	globalRules := store.GetScopedRoutingRules(context.Background(), "global", "")
	assert.Equal(t, 1, len(globalRules))
	assert.Equal(t, "Global", globalRules[0].Name)

	// Test customer scope
	custRules := store.GetScopedRoutingRules(context.Background(), "customer", customerID)
	assert.Equal(t, 1, len(custRules))
	assert.Equal(t, "Customer", custRules[0].Name)

	// Test team scope
	teamRules := store.GetScopedRoutingRules(context.Background(), "team", teamID)
	assert.Equal(t, 1, len(teamRules))
	assert.Equal(t, "Team", teamRules[0].Name)

	// ListAll should return all rules sorted by priority ASC (lower numbers = higher priority)
	allRules := store.GetAllRoutingRules(context.Background())
	assert.Equal(t, 3, len(allRules))
	assert.Equal(t, 10, allRules[0].Priority) // Global (highest)
	assert.Equal(t, 20, allRules[1].Priority) // Customer
	assert.Equal(t, 30, allRules[2].Priority) // Team (lowest)
}

// TestCompileAndCacheProgram tests CEL program compilation and caching
func TestCompileAndCacheProgram(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "rule-1",
		Name:          "Test Rule",
		CelExpression: "model == 'gpt-4o' && tokens_used < 80.0",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("openai")},
		},
		Enabled: bifrost.Ptr(true),
	}

	// First compilation
	program1, err := store.GetRoutingProgram(context.Background(), rule)
	require.NoError(t, err)
	assert.NotNil(t, program1)

	// Verify it's cached - second call should return cached program
	program2, err := store.GetRoutingProgram(context.Background(), rule)
	require.NoError(t, err)
	assert.NotNil(t, program2)

	// Both should be the same cached instance
	assert.Equal(t, program1, program2)
}

// TestCompileAndCacheProgram_InvalidExpression tests error handling for invalid CEL
func TestCompileAndCacheProgram_InvalidExpression(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "rule-invalid",
		Name:          "Invalid Rule",
		CelExpression: "model == gpt-4o'", // Syntax error
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("openai")},
		},
		Enabled: bifrost.Ptr(true),
	}

	_, err = store.GetRoutingProgram(context.Background(), rule)
	assert.Error(t, err)

	// Invalid rule should not be cached - attempting to get it again should fail
	_, err = store.GetRoutingProgram(context.Background(), rule)
	assert.Error(t, err)
}

// TestCompileAndCacheProgram_CacheInvalidation tests cache invalidation on rule update
func TestCompileAndCacheProgram_CacheInvalidation(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "rule-update",
		Name:          "Update Rule",
		CelExpression: "model == 'gpt-4o'",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("openai")},
		},
		Enabled: bifrost.Ptr(true),
		Scope:   "global",
	}

	// Compile and cache
	program1, err := store.GetRoutingProgram(context.Background(), rule)
	require.NoError(t, err)
	assert.NotNil(t, program1)

	// Update rule in memory (should invalidate cache)
	rule.CelExpression = "model == 'gpt-4-turbo'"
	err = store.UpdateRoutingRuleInMemory(context.Background(), rule)
	require.NoError(t, err)

	// Recompile should work
	program2, err := store.GetRoutingProgram(context.Background(), rule)
	require.NoError(t, err)
	assert.NotNil(t, program2)
}

// TestCompileAndCacheProgram_CacheInvalidationOnDelete tests cache invalidation on rule deletion
func TestCompileAndCacheProgram_CacheInvalidationOnDelete(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "rule-delete",
		Name:          "Delete Rule",
		CelExpression: "provider == 'openai'",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("openai")},
		},
		Enabled: bifrost.Ptr(true),
		Scope:   "global",
	}

	// Compile and cache
	_, err = store.GetRoutingProgram(context.Background(), rule)
	require.NoError(t, err)

	// Delete rule (should invalidate cache)
	err = store.DeleteRoutingRuleInMemory(context.Background(), rule.ID)
	require.NoError(t, err)

	// After deletion, we can't verify cache directly, but the rule is gone from storage
}

// TestCompileAndCacheProgram_EmptyExpression tests compilation of empty CEL expression (defaults to "true")
func TestCompileAndCacheProgram_EmptyExpression(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	rule := &configstoreTables.TableRoutingRule{
		ID:            "rule-empty",
		Name:          "Empty Rule",
		CelExpression: "",
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: bifrost.Ptr("openai")},
		},
		Enabled: bifrost.Ptr(true),
	}

	program, err := store.GetRoutingProgram(context.Background(), rule)
	require.NoError(t, err)
	assert.NotNil(t, program)

	// Verify caching works - second call should return same program
	program2, err := store.GetRoutingProgram(context.Background(), rule)
	require.NoError(t, err)
	assert.NotNil(t, program2)
	assert.Equal(t, program, program2)
}

// TestGetTeamNameAndGetCustomerName verifies the display-name accessors the
// enterprise layer uses as the log-stamping fallback when its edge-driven name
// caches miss: known entities return their name, unknown/empty ids return "".
func TestGetTeamNameAndGetCustomerName(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
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
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
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
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
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
	}, nil)
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
