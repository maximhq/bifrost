package tables

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTableBudgetOverrideLifecycle verifies replacement, effective-limit, and clear behavior.
func TestTableBudgetOverrideLifecycle(t *testing.T) {
	budget := &TableBudget{MaxLimit: 100}
	require.NoError(t, budget.SetOverride(25, BudgetOverrideModeCycles, 4))
	assert.True(t, budget.HasActiveOverride())
	assert.Equal(t, 125.0, budget.EffectiveMaxLimit())
	assert.Equal(t, 4, budget.OverrideCyclesRemaining)

	require.NoError(t, budget.SetOverride(50, BudgetOverrideModeForever, 0))
	assert.True(t, budget.HasActiveOverride())
	assert.Equal(t, 150.0, budget.EffectiveMaxLimit())
	assert.Equal(t, BudgetOverrideModeForever, budget.OverrideMode)

	budget.ClearOverride()
	assert.False(t, budget.HasActiveOverride())
	assert.Equal(t, 100.0, budget.EffectiveMaxLimit())
	assert.Zero(t, budget.OverrideAmount)
	assert.Empty(t, budget.OverrideMode)
	assert.Zero(t, budget.OverrideCyclesRemaining)
}

// TestTableBudgetSetOverrideRestoresPreviousState verifies invalid replacements are non-mutating.
func TestTableBudgetSetOverrideRestoresPreviousState(t *testing.T) {
	budget := &TableBudget{MaxLimit: 100}
	require.NoError(t, budget.SetOverride(25, BudgetOverrideModeCycles, 2))

	require.Error(t, budget.SetOverride(50, BudgetOverrideModeCycles, 0))
	assert.Equal(t, 25.0, budget.OverrideAmount)
	assert.Equal(t, BudgetOverrideModeCycles, budget.OverrideMode)
	assert.Equal(t, 2, budget.OverrideCyclesRemaining)
}

// TestTableBudgetRefreshOverrideCyclesRemaining verifies finite overrides expire
// as windows close while permanent overrides survive resets. The remaining count
// is derived from the grant and LastReset, so advancing LastReset is what spends
// a cycle.
func TestTableBudgetRefreshOverrideCyclesRemaining(t *testing.T) {
	grantedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	finite := &TableBudget{MaxLimit: 100, ResetDuration: "1h", LastReset: grantedAt}
	require.NoError(t, finite.SetOverride(25, BudgetOverrideModeCycles, 2))
	assert.Equal(t, 2, finite.OverrideCyclesTotal)
	require.NotNil(t, finite.OverrideAnchorReset)
	assert.True(t, finite.OverrideAnchorReset.Equal(grantedAt))

	finite.LastReset = grantedAt.Add(time.Hour)
	finite.RefreshOverrideCyclesRemaining()
	assert.Equal(t, 1, finite.OverrideCyclesRemaining)
	assert.Equal(t, 125.0, finite.EffectiveMaxLimit())

	finite.LastReset = grantedAt.Add(2 * time.Hour)
	finite.RefreshOverrideCyclesRemaining()
	assert.False(t, finite.HasActiveOverride())
	assert.Equal(t, 100.0, finite.EffectiveMaxLimit())

	permanent := &TableBudget{MaxLimit: 100, ResetDuration: "1h", LastReset: grantedAt}
	require.NoError(t, permanent.SetOverride(50, BudgetOverrideModeForever, 0))
	permanent.LastReset = grantedAt.Add(5 * time.Hour)
	permanent.RefreshOverrideCyclesRemaining()
	assert.True(t, permanent.HasActiveOverride())
	assert.Equal(t, 150.0, permanent.EffectiveMaxLimit())
}

// TestTableBudgetRefreshOverrideCyclesRemainingIsIdempotent verifies that
// refreshing repeatedly at a fixed LastReset does not spend extra cycles. This is
// the property that makes it safe for every node, the ticker, the request path and
// every config reload to all call it freely.
func TestTableBudgetRefreshOverrideCyclesRemainingIsIdempotent(t *testing.T) {
	grantedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	budget := &TableBudget{MaxLimit: 100, ResetDuration: "1h", LastReset: grantedAt}
	require.NoError(t, budget.SetOverride(25, BudgetOverrideModeCycles, 3))

	budget.LastReset = grantedAt.Add(time.Hour)
	for i := 0; i < 10; i++ {
		budget.RefreshOverrideCyclesRemaining()
	}
	assert.Equal(t, 2, budget.OverrideCyclesRemaining, "repeated refreshes at one boundary must spend exactly one cycle")
}

// TestTableBudgetRefreshOverrideCyclesRemainingSpendsWholeGrantAfterLongGap
// verifies a multi-window gap collapses to the correct count in one call rather
// than needing one call per elapsed window.
func TestTableBudgetRefreshOverrideCyclesRemainingSpendsWholeGrantAfterLongGap(t *testing.T) {
	grantedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	budget := &TableBudget{MaxLimit: 100, ResetDuration: "1h", LastReset: grantedAt}
	require.NoError(t, budget.SetOverride(25, BudgetOverrideModeCycles, 3))

	budget.LastReset = grantedAt.Add(240 * time.Hour)
	budget.RefreshOverrideCyclesRemaining()
	assert.False(t, budget.HasActiveOverride(), "a 3 cycle grant must be fully spent after 240 elapsed windows")
}

// overrideAnchor returns a fixed grant anchor for validation table cases.
func overrideAnchor() *time.Time {
	anchor := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	return &anchor
}

// TestTableBudgetValidateOverride verifies that persisted override fields cannot form an ambiguous state.
func TestTableBudgetValidateOverride(t *testing.T) {
	tests := []struct {
		name      string
		budget    TableBudget
		wantError bool
	}{
		{name: "no override"},
		{
			name: "forever override",
			budget: TableBudget{
				OverrideAmount: 25,
				OverrideMode:   BudgetOverrideModeForever,
			},
		},
		{
			name: "finite override",
			budget: TableBudget{
				OverrideAmount:          25,
				OverrideMode:            BudgetOverrideModeCycles,
				OverrideCyclesRemaining: 3,
				OverrideCyclesTotal:     3,
				OverrideAnchorReset:     overrideAnchor(),
			},
		},
		{
			name: "finite override partway through its grant",
			budget: TableBudget{
				OverrideAmount:          25,
				OverrideMode:            BudgetOverrideModeCycles,
				OverrideCyclesRemaining: 1,
				OverrideCyclesTotal:     3,
				OverrideAnchorReset:     overrideAnchor(),
			},
		},
		{
			name: "cycles mode without an anchor",
			budget: TableBudget{
				OverrideAmount:          25,
				OverrideMode:            BudgetOverrideModeCycles,
				OverrideCyclesRemaining: 3,
				OverrideCyclesTotal:     3,
			},
			wantError: true,
		},
		{
			name: "cycles mode with total below remaining",
			budget: TableBudget{
				OverrideAmount:          25,
				OverrideMode:            BudgetOverrideModeCycles,
				OverrideCyclesRemaining: 4,
				OverrideCyclesTotal:     3,
				OverrideAnchorReset:     overrideAnchor(),
			},
			wantError: true,
		},
		{
			name: "forever mode with a grant anchor",
			budget: TableBudget{
				OverrideAmount:      25,
				OverrideMode:        BudgetOverrideModeForever,
				OverrideAnchorReset: overrideAnchor(),
			},
			wantError: true,
		},
		{
			name:      "no mode with a grant anchor",
			budget:    TableBudget{OverrideAnchorReset: overrideAnchor()},
			wantError: true,
		},
		{name: "amount without mode", budget: TableBudget{OverrideAmount: 25}, wantError: true},
		{name: "cycles without mode", budget: TableBudget{OverrideCyclesRemaining: 1}, wantError: true},
		{
			name:      "cycles mode without amount",
			budget:    TableBudget{OverrideMode: BudgetOverrideModeCycles, OverrideCyclesRemaining: 1},
			wantError: true,
		},
		{
			name:      "cycles mode without remaining cycles",
			budget:    TableBudget{OverrideAmount: 25, OverrideMode: BudgetOverrideModeCycles},
			wantError: true,
		},
		{
			name: "forever mode with remaining cycles",
			budget: TableBudget{
				OverrideAmount:          25,
				OverrideMode:            BudgetOverrideModeForever,
				OverrideCyclesRemaining: 1,
			},
			wantError: true,
		},
		{
			name:      "unknown mode",
			budget:    TableBudget{OverrideAmount: 25, OverrideMode: BudgetOverrideMode("unknown")},
			wantError: true,
		},
		{
			name:      "negative amount",
			budget:    TableBudget{OverrideAmount: -1, OverrideMode: BudgetOverrideModeForever},
			wantError: true,
		},
		{
			name:      "not a number amount",
			budget:    TableBudget{OverrideAmount: math.NaN(), OverrideMode: BudgetOverrideModeForever},
			wantError: true,
		},
		{
			name:      "infinite amount",
			budget:    TableBudget{OverrideAmount: math.Inf(1), OverrideMode: BudgetOverrideModeForever},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.budget.validateOverride()
			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
