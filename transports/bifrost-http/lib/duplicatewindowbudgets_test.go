package lib

import (
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSkipDuplicateWindowBudgets pins the config.json rule for team and customer budgets: one budget
// per reset window, the first in file order kept, later ones skipped with a warning, and the rest of
// the file left alone. The API refuses the same shapes with "duplicate reset_duration in budgets".
func TestSkipDuplicateWindowBudgets(t *testing.T) {
	teamID, customerID := "team-1", "cust-1"
	configData := &ConfigData{Governance: &configstore.GovernanceConfig{
		Teams: []configstoreTables.TableTeam{{ID: teamID, Budgets: []configstoreTables.TableBudget{
			{ID: "t-month", MaxLimit: 10, ResetDuration: "1M"},
			{ID: "t-month-again", MaxLimit: 20, ResetDuration: "1M"},
			{ID: "t-week", MaxLimit: 5, ResetDuration: "1w"},
		}}},
		Customers: []configstoreTables.TableCustomer{{ID: customerID, Budgets: []configstoreTables.TableBudget{
			{MaxLimit: 10, ResetDuration: "1d"},
		}}},
		Budgets: []configstoreTables.TableBudget{
			{ID: "row-team-week", MaxLimit: 7, ResetDuration: "1w", TeamID: &teamID},
			{ID: "row-team-year", MaxLimit: 70, ResetDuration: "1Y", TeamID: &teamID},
			{ID: "row-cust-day", MaxLimit: 3, ResetDuration: "1d", CustomerID: &customerID},
			{ID: "row-unowned", MaxLimit: 1, ResetDuration: "1d"},
		},
	}}
	var warnings []string
	skipDuplicateWindowBudgets(configData, func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})

	gov := configData.Governance
	teamBudgets := []string{}
	for _, b := range gov.Teams[0].Budgets {
		teamBudgets = append(teamBudgets, b.ID)
	}
	assert.Equal(t, []string{"t-month", "t-week"}, teamBudgets, "the second inline budget on a window is skipped")
	require.Len(t, gov.Customers[0].Budgets, 1)

	rows := []string{}
	for _, b := range gov.Budgets {
		rows = append(rows, b.ID)
	}
	assert.Equal(t, []string{"row-team-year", "row-unowned"}, rows,
		"a governance.budgets row on a window its owner already has is dropped; rows on new windows and unowned rows stay")
	assert.Len(t, warnings, 3, "one warning per skipped budget")
}

// TestSkipDuplicateWindowBudgetsKeepsOneBudgetNamedTwice pins that a budget named by the same owner
// both inline and as a governance.budgets row is one budget, not a duplicate.
func TestSkipDuplicateWindowBudgetsKeepsOneBudgetNamedTwice(t *testing.T) {
	teamID := "team-1"
	configData := &ConfigData{Governance: &configstore.GovernanceConfig{
		Teams:   []configstoreTables.TableTeam{{ID: teamID, Budgets: []configstoreTables.TableBudget{{ID: "b1", MaxLimit: 10, ResetDuration: "1M"}}}},
		Budgets: []configstoreTables.TableBudget{{ID: "b1", MaxLimit: 10, ResetDuration: "1M", TeamID: &teamID}},
	}}
	warned := false
	skipDuplicateWindowBudgets(configData, func(string, ...any) { warned = true })
	assert.False(t, warned)
	assert.Len(t, configData.Governance.Budgets, 1)
	assert.Len(t, configData.Governance.Teams[0].Budgets, 1)
}
