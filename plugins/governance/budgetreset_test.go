package governance

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGovernanceStore_ResetBudget_ManualReset verifies a manual reset zeroes a not-yet-expired budget with all side effects.
func TestGovernanceStore_ResetBudget_ManualReset(t *testing.T) {
	logger := NewMockLogger()

	budget := buildBudgetWithUsage("budget1", 100.0, 75.0, "1M")
	budget.LastReset = time.Now().Add(-1 * time.Hour) // well within the reset window
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", budget)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	store.LastDBUsagesBudgetsMu.Lock()
	store.LastDBUsagesBudgets["budget1"] = 75.0
	store.LastDBUsagesBudgetsMu.Unlock()

	var hookBudgets []*configstoreTables.TableBudget
	store.SetResetHooks(func(budgets []*configstoreTables.TableBudget) {
		hookBudgets = append(hookBudgets, budgets...)
	}, nil)

	before := time.Now()
	resetBudget, err := store.ResetBudget(context.Background(), "budget1")
	require.NoError(t, err)
	require.NotNil(t, resetBudget)

	assert.Equal(t, 0.0, resetBudget.CurrentUsage, "usage should be zeroed")
	assert.False(t, resetBudget.LastReset.Before(before), "LastReset should advance to now")

	// The stored snapshot reflects the reset.
	stored := store.LoadBudget(context.Background(), "budget1")
	require.NotNil(t, stored)
	assert.Equal(t, 0.0, stored.CurrentUsage)

	// Embedded VK reference is refreshed.
	updatedVK, found := store.GetVirtualKey(context.Background(), "sk-bf-test")
	require.True(t, found)
	require.NotEmpty(t, updatedVK.Budgets)
	assert.Equal(t, 0.0, updatedVK.Budgets[0].CurrentUsage, "VK's embedded budget should be reset")

	// LastDB baseline is zeroed so the next dump doesn't re-add stale usage.
	store.LastDBUsagesBudgetsMu.RLock()
	baseline := store.LastDBUsagesBudgets["budget1"]
	store.LastDBUsagesBudgetsMu.RUnlock()
	assert.Equal(t, 0.0, baseline)

	// Reset hook fired with the reset budget.
	require.Len(t, hookBudgets, 1)
	assert.Equal(t, "budget1", hookBudgets[0].ID)
}

// TestGovernanceStore_ResetBudget_NotFound verifies unknown budget IDs surface ErrBudgetNotFound.
func TestGovernanceStore_ResetBudget_NotFound(t *testing.T) {
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil)
	require.NoError(t, err)

	resetBudget, err := store.ResetBudget(context.Background(), "missing")
	assert.Nil(t, resetBudget)
	assert.ErrorIs(t, err, ErrBudgetNotFound)
}

// TestGovernanceStore_ResetBudget_CalendarAligned verifies a manual reset doesn't break the next calendar-boundary reset.
func TestGovernanceStore_ResetBudget_CalendarAligned(t *testing.T) {
	logger := NewMockLogger()

	budget := buildBudgetWithUsage("cal-budget", 500.0, 120.0, "1M")
	// Already reset at the start of the current calendar period, i.e. not expired.
	budget.LastReset = configstoreTables.GetCalendarPeriodStart("1M", time.Now())
	vk := buildVirtualKeyWithBudget("vk1", "sk-bf-test", "Test VK", budget)
	vk.CalendarAligned = true

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	// Sanity: the expiry-driven path considers this budget up to date.
	assert.Empty(t, store.ResetExpiredBudgetsInMemory(context.Background(), true, "cal-budget"),
		"calendar-aligned budget should not be expired before the manual reset")

	resetBudget, err := store.ResetBudget(context.Background(), "cal-budget")
	require.NoError(t, err)
	require.NotNil(t, resetBudget)
	assert.Equal(t, 0.0, resetBudget.CurrentUsage)

	// Next calendar boundary must still be ahead of the manual LastReset.
	nextPeriodStart := configstoreTables.GetCalendarPeriodStart("1M", time.Now().AddDate(0, 1, 0))
	assert.True(t, nextPeriodStart.After(resetBudget.LastReset),
		"next calendar boundary must remain ahead of the manual reset timestamp")
}

// TestGovernanceStore_ResetBudget_Idempotent verifies a manual reset still zeroes
// usage when the stored LastReset is in the future (e.g. clock skew).
func TestGovernanceStore_ResetBudget_Idempotent(t *testing.T) {
	logger := NewMockLogger()

	budget := buildBudgetWithUsage("budget1", 100.0, 40.0, "1d")
	budget.LastReset = time.Now().Add(1 * time.Minute)

	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Budgets: []configstoreTables.TableBudget{*budget},
	}, nil)
	require.NoError(t, err)

	resetBudget, err := store.ResetBudget(context.Background(), "budget1")
	require.NoError(t, err)
	require.NotNil(t, resetBudget)
	assert.Equal(t, "budget1", resetBudget.ID)
	assert.Equal(t, 0.0, resetBudget.CurrentUsage, "usage must be zeroed even when LastReset is in the future")
}
