package tables

import (
	"math"
	"testing"

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
			},
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
