package tables

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

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
