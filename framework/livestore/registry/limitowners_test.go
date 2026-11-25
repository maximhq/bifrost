package registry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// limitDigest is what the loader decides for one limit: the holder it names and its calendar flag.
type limitDigest struct {
	key, team, customer, modelConfig, provider string
	calendar                                   bool
}

// budgetDigests digests budget records by ID.
func budgetDigests(rows []*Budget) map[string]limitDigest {
	out := make(map[string]limitDigest, len(rows))
	for _, b := range rows {
		out[b.ID] = limitDigest{BudgetKeyID(b), BudgetTeamID(b), BudgetCustomerID(b), BudgetModelConfigID(b), BudgetProvider(b), b.IsCalendarAligned}
	}
	return out
}

// rateLimitDigests digests rate limit records by ID.
func rateLimitDigests(rows []*RateLimit) map[string]limitDigest {
	out := make(map[string]limitDigest, len(rows))
	for _, r := range rows {
		out[r.ID] = limitDigest{RateLimitKeyID(r), RateLimitTeamID(r), RateLimitCustomerID(r), RateLimitModelConfigID(r), RateLimitProvider(r), r.IsCalendarAligned}
	}
	return out
}

// requireSubset fails unless every limit in part is in whole with the same digest.
func requireSubset(t *testing.T, whole, part map[string]limitDigest, path string) {
	t.Helper()
	require.NotEmpty(t, part, path)
	for id, d := range part {
		require.Equal(t, whole[id], d, "%s: %s", path, id)
	}
}

// limitReadPaths walks every budget and rate limit page by page, checks that reading them by ID and
// through each holder reader gives each limit the same holder and flag, and returns the page walks'
// digests.
func limitReadPaths(t *testing.T, db *gorm.DB) (budgets, rateLimits map[string]limitDigest) {
	t.Helper()
	ctx := context.Background()
	limitHolders(t, db)

	var walkedBudgets []*Budget
	for after := ""; ; {
		rows, next, err := BudgetsPage(ctx, db, after, 2)
		require.NoError(t, err)
		walkedBudgets = append(walkedBudgets, rows...)
		if next == "" {
			break
		}
		after = next
	}
	budgets = budgetDigests(walkedBudgets)
	ids := make([]string, 0, len(budgets))
	for id := range budgets {
		ids = append(ids, id)
	}
	byID, err := BudgetsByID(ctx, db, ids)
	require.NoError(t, err)
	require.Equal(t, budgets, budgetDigests(byID), "budgets by ID")
	for path, read := range map[string]func() ([]*Budget, error){
		"keys":         func() ([]*Budget, error) { return BudgetsByKeys(ctx, db, []string{"vk-a", "vk-b"}) },
		"teams":        func() ([]*Budget, error) { return BudgetsByTeams(ctx, db, []string{"team-a"}) },
		"customers":    func() ([]*Budget, error) { return BudgetsByCustomers(ctx, db, []string{"cust-a"}) },
		"modelConfigs": func() ([]*Budget, error) { return BudgetsByModelConfigs(ctx, db, []string{"mc-a"}) },
		"providers":    func() ([]*Budget, error) { return BudgetsByProviders(ctx, db, []string{"openai"}) },
	} {
		rows, err := read()
		require.NoError(t, err, path)
		requireSubset(t, budgets, budgetDigests(rows), "budgets by "+path)
	}

	var walkedRateLimits []*RateLimit
	for after := ""; ; {
		rows, next, err := RateLimitsPage(ctx, db, after, 2)
		require.NoError(t, err)
		walkedRateLimits = append(walkedRateLimits, rows...)
		if next == "" {
			break
		}
		after = next
	}
	rateLimits = rateLimitDigests(walkedRateLimits)
	ids = ids[:0]
	for id := range rateLimits {
		ids = append(ids, id)
	}
	byIDRates, err := RateLimitsByID(ctx, db, ids)
	require.NoError(t, err)
	require.Equal(t, rateLimits, rateLimitDigests(byIDRates), "rate limits by ID")
	for path, read := range map[string]func() ([]*RateLimit, error){
		"keys":         func() ([]*RateLimit, error) { return RateLimitsByKeys(ctx, db, []string{"vk-a"}) },
		"teams":        func() ([]*RateLimit, error) { return RateLimitsByTeams(ctx, db, []string{"team-a"}) },
		"customers":    func() ([]*RateLimit, error) { return RateLimitsByCustomers(ctx, db, []string{"cust-a"}) },
		"modelConfigs": func() ([]*RateLimit, error) { return RateLimitsByModelConfigs(ctx, db, []string{"mc-a"}) },
		"providers":    func() ([]*RateLimit, error) { return RateLimitsByProviders(ctx, db, []string{"openai"}) },
	} {
		rows, err := read()
		require.NoError(t, err, path)
		requireSubset(t, rateLimits, rateLimitDigests(rows), "rate limits by "+path)
	}
	return budgets, rateLimits
}

// TestLimitReadPathsAgree checks that every read of budgets and rate limits (pages, by ID and through
// each holder) gives a limit the same holder and calendar flag, on SQLite and on Postgres, and that
// the two databases give the same answers the holder tests pin: the owners of every limit are read
// in one query, whose SQL both dialects must accept and answer alike.
func TestLimitReadPathsAgree(t *testing.T) {
	sqliteBudgets, sqliteRateLimits := limitReadPaths(t, newProviderDB(t, nil).DB())
	require.Equal(t, limitDigest{key: "vk-a", calendar: true}, sqliteBudgets["b-pc"], "a provider config's budget names its key")
	require.Equal(t, limitDigest{provider: "openai"}, sqliteRateLimits["rl-prov"], "a provider's rate limit names the provider, flag off")
	t.Run("postgres", func(t *testing.T) {
		budgets, rateLimits := limitReadPaths(t, newPostgresConfigStore(t).DB())
		require.Equal(t, sqliteBudgets, budgets)
		require.Equal(t, sqliteRateLimits, rateLimits)
	})
}
