package registry

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

// TestTeamsAndCustomersPageAndRecord checks that team and customer pages walk every row in ID
// order, that a record carries its customer, rate limit ID, budget IDs in creation order,
// calendar flag and tri-state content-logging decision, that an ID with no row is left out, and
// that TeamsByCustomers finds a customer's teams.
func TestTeamsAndCustomersPageAndRecord(t *testing.T) {
	ctx := context.Background()
	db := newProviderDB(t, nil).DB()
	now := time.Now()
	insert(t, db,
		rateLimitRow("rl-team", 1), rateLimitRow("rl-cust", 2),
		&tables.TableCustomer{ID: "cust-c", Name: "Charlie"},
		&tables.TableCustomer{ID: "cust-a", Name: "Alpha", RateLimitID: ptr("rl-cust"), CalendarAligned: true},
		&tables.TableCustomer{ID: "cust-b", Name: "Bravo"},
		&tables.TableTeam{ID: "team-c", Name: "Core", DisableContentLogging: ptr(false)},
		&tables.TableTeam{ID: "team-a", Name: "Apps", CustomerID: ptr("cust-a"), RateLimitID: ptr("rl-team"), CalendarAligned: true, DisableContentLogging: ptr(true)},
		&tables.TableTeam{ID: "team-b", Name: "Billing", CustomerID: ptr("cust-a")},
	)
	later, earlier, custBudget := budgetRow("tb-1", 1), budgetRow("tb-2", 2), budgetRow("cb-1", 3)
	later.TeamID, later.CreatedAt = ptr("team-a"), now
	earlier.TeamID, earlier.CreatedAt = ptr("team-a"), now.Add(-time.Hour)
	custBudget.CustomerID = ptr("cust-a")
	insert(t, db, later, earlier, custBudget)

	var teams []*Team
	after := ""
	for {
		rows, next, err := TeamsPage(ctx, db, after, 2)
		require.NoError(t, err)
		teams = append(teams, rows...)
		if next == "" {
			break
		}
		after = next
	}
	require.Len(t, teams, 3)
	require.Equal(t, []string{"team-a", "team-b", "team-c"}, []string{TeamID(teams[0]), TeamID(teams[1]), TeamID(teams[2])})
	require.Equal(t, &Team{
		ID: "team-a", Name: "Apps", CustomerID: "cust-a", RateLimitID: "rl-team",
		BudgetIDs: []string{"tb-2", "tb-1"}, CalendarAligned: true, DisableContentLogging: ptr(true),
	}, teams[0], "budgets in the order they were created")
	require.Nil(t, teams[1].DisableContentLogging, "a team that inherits")
	require.Equal(t, ptr(false), teams[2].DisableContentLogging, "a team that keeps content on")
	require.Equal(t, "cust-a", TeamCustomerID(teams[1]))
	require.Empty(t, TeamCustomerID(teams[2]))
	require.Empty(t, teams[2].BudgetIDs)

	var customers []*Customer
	after = ""
	for {
		rows, next, err := CustomersPage(ctx, db, after, 2)
		require.NoError(t, err)
		customers = append(customers, rows...)
		if next == "" {
			break
		}
		after = next
	}
	require.Len(t, customers, 3)
	require.Equal(t, []string{"cust-a", "cust-b", "cust-c"}, []string{CustomerID(customers[0]), CustomerID(customers[1]), CustomerID(customers[2])})
	require.Equal(t, &Customer{ID: "cust-a", Name: "Alpha", RateLimitID: "rl-cust", BudgetIDs: []string{"cb-1"}, CalendarAligned: true}, customers[0])

	gotTeams, err := TeamsByID(ctx, db, []string{"team-b", "missing"})
	require.NoError(t, err)
	require.Len(t, gotTeams, 1)
	require.Equal(t, "team-b", gotTeams[0].ID)
	gotCustomers, err := CustomersByID(ctx, db, []string{"missing", "cust-c"})
	require.NoError(t, err)
	require.Len(t, gotCustomers, 1)
	require.Equal(t, "cust-c", gotCustomers[0].ID)
	gotCustomers, err = CustomersByID(ctx, db, []string{"missing"})
	require.NoError(t, err)
	require.Empty(t, gotCustomers)

	gotTeams, err = TeamsByCustomers(ctx, db, []string{"cust-a"})
	require.NoError(t, err)
	ids := make([]string, 0, len(gotTeams))
	for _, team := range gotTeams {
		ids = append(ids, team.ID)
	}
	require.ElementsMatch(t, []string{"team-a", "team-b"}, ids)
}
