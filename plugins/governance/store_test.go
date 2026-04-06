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
			vk, exists := store.GetVirtualKey(tt.vkValue)
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
				vk, exists := store.GetVirtualKey("sk-bf-test")
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
	vk, _ = store.GetVirtualKey("sk-bf-test")

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

			testVK, _ = testStore.GetVirtualKey("sk-bf-test")
			err := testStore.CheckBudget(context.Background(), testVK, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
			if tt.shouldErr {
				assert.Error(t, err, "Expected error for usage check")
			} else {
				assert.NoError(t, err, "Expected no error for usage check")
			}
		})
	}
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

	vk, _ = store.GetVirtualKey("sk-bf-test")

	// Test: All budgets under limit should pass
	err = store.CheckBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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
	err = store.CheckBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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

	vk, _ = store.GetVirtualKey("sk-bf-test")
	err = store.CheckBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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

	vk, _ = store.GetVirtualKey("sk-bf-test")
	err = store.CheckBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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

	vk, _ = store.GetVirtualKey("sk-bf-test")
	err = store.CheckBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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

	vk, _ = store.GetVirtualKey("sk-bf-test")

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
	err = store.CheckBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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

	vk, _ = store.GetVirtualKey("sk-bf-test")
	err = store.CheckBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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

	vk, _ = store.GetVirtualKey("sk-bf-test")

	// Provider config budget exceeded → should block even though VK budget is fine
	err = store.CheckBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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

	resolver := NewBudgetResolver(store, nil, logger)
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

	resolver := NewBudgetResolver(store, nil, logger)
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

	resolver := NewBudgetResolver(store, nil, logger)

	// Request 1: $0.80 — both budgets fine
	vk, _ = store.GetVirtualKey("sk-bf-test")
	err = store.UpdateVirtualKeyBudgetUsageInMemory(context.Background(), vk, schemas.OpenAI, 0.80)
	require.NoError(t, err)

	ctx := &schemas.BifrostContext{}
	result := resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)
	assertDecision(t, DecisionAllow, result)

	// Request 2: $0.80 — still fine ($1.60 total)
	vk, _ = store.GetVirtualKey("sk-bf-test")
	err = store.UpdateVirtualKeyBudgetUsageInMemory(context.Background(), vk, schemas.OpenAI, 0.80)
	require.NoError(t, err)

	ctx = &schemas.BifrostContext{}
	result = resolver.EvaluateVirtualKeyRequest(ctx, "sk-bf-test", schemas.OpenAI, "gpt-4", schemas.ChatCompletionRequest, false)
	assertDecision(t, DecisionAllow, result)

	// Request 3: $0.80 — pushes hourly to $2.40 > $2.00 limit → blocked
	vk, _ = store.GetVirtualKey("sk-bf-test")
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
	vk, _ = store.GetVirtualKey("sk-bf-test")
	assert.True(t, vk.CalendarAligned, "VK should have calendar_aligned=true")

	// Both under limit — should pass
	err = store.CheckBudget(context.Background(), vk, &EvaluationRequest{Provider: schemas.OpenAI}, nil)
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
	store.CreateVirtualKeyInMemory(vk)

	_, exists := store.budgets.Load("b1")
	assert.True(t, exists, "Budget b1 should be in memory after create")
	_, exists = store.budgets.Load("b2")
	assert.True(t, exists, "Budget b2 should be in memory after create")

	retrieved, found := store.GetVirtualKey("sk-bf-test")
	require.True(t, found)
	assert.Len(t, retrieved.Budgets, 2, "VK should have 2 budgets")

	// Delete
	store.DeleteVirtualKeyInMemory("vk1")

	_, exists = store.budgets.Load("b1")
	assert.False(t, exists, "Budget b1 should be removed after delete")
	_, exists = store.budgets.Load("b2")
	assert.False(t, exists, "Budget b2 should be removed after delete")

	_, found = store.GetVirtualKey("sk-bf-test")
	assert.False(t, found, "VK should not be found after delete")
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
	governanceData := store.GetGovernanceData()
	updatedRateLimit, exists := governanceData.RateLimits["rl1"]
	require.True(t, exists, "Rate limit should exist")
	require.NotNil(t, updatedRateLimit)

	assert.Equal(t, int64(500), updatedRateLimit.TokenCurrentUsage, "Token usage should be updated")
	assert.Equal(t, int64(0), updatedRateLimit.RequestCurrentUsage, "Request usage should not change")

	// Test updating requests
	err = store.UpdateVirtualKeyRateLimitUsageInMemory(context.Background(), vk, schemas.OpenAI, 0, false, true)
	assert.NoError(t, err, "Rate limit update should succeed")

	// Retrieve the updated rate limit again
	governanceData = store.GetGovernanceData()
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
	expiredRateLimits := store.ResetExpiredRateLimitsInMemory(context.Background())
	err = store.ResetExpiredRateLimits(context.Background(), expiredRateLimits)
	assert.NoError(t, err, "Reset should succeed")

	// Retrieve the updated VK to check rate limit changes
	updatedVK, _ := store.GetVirtualKey("sk-bf-test")
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
	expiredBudgets := store.ResetExpiredBudgetsInMemory(context.Background())
	err = store.ResetExpiredBudgets(context.Background(), expiredBudgets)
	assert.NoError(t, err, "Reset should succeed")

	// Retrieve the updated VK to check budget changes
	updatedVK, _ := store.GetVirtualKey("sk-bf-test")
	require.NotNil(t, updatedVK)
	require.True(t, len(updatedVK.Budgets) > 0, "VK should have budgets")

	assert.Equal(t, 0.0, updatedVK.Budgets[0].CurrentUsage, "Budget usage should be reset")
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

	allBudgets := store.GetGovernanceData().Budgets
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
		Enabled:       true,
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
		Enabled:       true,
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
	err = store.UpdateRoutingRuleInMemory(rule1)
	require.NoError(t, err)
	err = store.UpdateRoutingRuleInMemory(rule2)
	require.NoError(t, err)

	// Test retrieval by scope
	globalRules := store.GetScopedRoutingRules("global", "")
	assert.Equal(t, 1, len(globalRules))
	assert.Equal(t, "Global Rule", globalRules[0].Name)

	teamRules := store.GetScopedRoutingRules("team", teamID)
	assert.Equal(t, 1, len(teamRules))
	assert.Equal(t, "Team Rule", teamRules[0].Name)

	// Test ListRoutingRules
	allRules := store.GetAllRoutingRules()
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
			Enabled:  true,
		},
		{
			ID:       "2",
			Name:     "Priority 20",
			Priority: 20,
			Scope:    "global",
			ScopeID:  nil,
			Enabled:  true,
		},
		{
			ID:       "3",
			Name:     "Priority 10",
			Priority: 10,
			Scope:    "global",
			ScopeID:  nil,
			Enabled:  true,
		},
	}

	for _, rule := range rules {
		err := store.UpdateRoutingRuleInMemory(rule)
		require.NoError(t, err)
	}

	// Retrieve and verify ordering (sorted by priority ASC, so lower numbers first)
	retrieved := store.GetScopedRoutingRules("global", "")
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
		Enabled: true,
		Scope:   "global",
		ScopeID: nil,
	}

	disabledRule := &configstoreTables.TableRoutingRule{
		ID:      "2",
		Name:    "Disabled Rule",
		Enabled: false,
		Scope:   "global",
		ScopeID: nil,
	}

	err = store.UpdateRoutingRuleInMemory(enabledRule)
	require.NoError(t, err)
	err = store.UpdateRoutingRuleInMemory(disabledRule)
	require.NoError(t, err)

	// Only enabled rules should be returned
	retrieved := store.GetScopedRoutingRules("global", "")
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
		Enabled: true,
		Scope:   "global",
		ScopeID: nil,
	}

	// Add rule
	err = store.UpdateRoutingRuleInMemory(rule)
	require.NoError(t, err)

	retrieved := store.GetScopedRoutingRules("global", "")
	assert.Equal(t, 1, len(retrieved))

	// Delete rule
	err = store.DeleteRoutingRuleInMemory(rule.ID)
	require.NoError(t, err)

	// Verify deletion
	retrieved = store.GetScopedRoutingRules("global", "")
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

// TestGovernanceStore_RoutingRules_MultipleScopes tests rules with multiple scopes
func TestGovernanceStore_RoutingRules_MultipleScopes(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	customerID := "cust-123"
	teamID := "team-456"

	// Create rules for different scopes
	globalRule := &configstoreTables.TableRoutingRule{
		ID: "1", Name: "Global", Scope: "global", ScopeID: nil, Priority: 10, Enabled: true,
	}
	customerRule := &configstoreTables.TableRoutingRule{
		ID: "2", Name: "Customer", Scope: "customer", ScopeID: &customerID, Priority: 20, Enabled: true,
	}
	teamRule := &configstoreTables.TableRoutingRule{
		ID: "3", Name: "Team", Scope: "team", ScopeID: &teamID, Priority: 30, Enabled: true,
	}

	require.NoError(t, store.UpdateRoutingRuleInMemory(globalRule))
	require.NoError(t, store.UpdateRoutingRuleInMemory(customerRule))
	require.NoError(t, store.UpdateRoutingRuleInMemory(teamRule))

	// Test global scope
	globalRules := store.GetScopedRoutingRules("global", "")
	assert.Equal(t, 1, len(globalRules))
	assert.Equal(t, "Global", globalRules[0].Name)

	// Test customer scope
	custRules := store.GetScopedRoutingRules("customer", customerID)
	assert.Equal(t, 1, len(custRules))
	assert.Equal(t, "Customer", custRules[0].Name)

	// Test team scope
	teamRules := store.GetScopedRoutingRules("team", teamID)
	assert.Equal(t, 1, len(teamRules))
	assert.Equal(t, "Team", teamRules[0].Name)

	// ListAll should return all rules sorted by priority ASC (lower numbers = higher priority)
	allRules := store.GetAllRoutingRules()
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
		Enabled: true,
	}

	// First compilation
	program1, err := store.GetRoutingProgram(rule)
	require.NoError(t, err)
	assert.NotNil(t, program1)

	// Verify it's cached - second call should return cached program
	program2, err := store.GetRoutingProgram(rule)
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
		Enabled: true,
	}

	_, err = store.GetRoutingProgram(rule)
	assert.Error(t, err)

	// Invalid rule should not be cached - attempting to get it again should fail
	_, err = store.GetRoutingProgram(rule)
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
		Enabled: true,
		Scope:   "global",
	}

	// Compile and cache
	program1, err := store.GetRoutingProgram(rule)
	require.NoError(t, err)
	assert.NotNil(t, program1)

	// Update rule in memory (should invalidate cache)
	rule.CelExpression = "model == 'gpt-4-turbo'"
	err = store.UpdateRoutingRuleInMemory(rule)
	require.NoError(t, err)

	// Recompile should work
	program2, err := store.GetRoutingProgram(rule)
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
		Enabled: true,
		Scope:   "global",
	}

	// Compile and cache
	_, err = store.GetRoutingProgram(rule)
	require.NoError(t, err)

	// Delete rule (should invalidate cache)
	err = store.DeleteRoutingRuleInMemory(rule.ID)
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
		Enabled: true,
	}

	program, err := store.GetRoutingProgram(rule)
	require.NoError(t, err)
	assert.NotNil(t, program)

	// Verify caching works - second call should return same program
	program2, err := store.GetRoutingProgram(rule)
	require.NoError(t, err)
	assert.NotNil(t, program2)
	assert.Equal(t, program, program2)
}

// Utility functions for tests
func ptrInt64(i int64) *int64 {
	return &i
}
