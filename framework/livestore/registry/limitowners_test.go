package registry

import (
	"context"
	"maps"
	"slices"
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
		rows, next, err := LimitReads{}.BudgetsPage(ctx, db, after, 2)
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
	byID, err := LimitReads{}.BudgetsByID(ctx, db, ids)
	require.NoError(t, err)
	require.Equal(t, budgets, budgetDigests(byID), "budgets by ID")
	for path, read := range map[string]func() ([]*Budget, error){
		"keys":         func() ([]*Budget, error) { return LimitReads{}.BudgetsByKeys(ctx, db, []string{"vk-a", "vk-b"}) },
		"teams":        func() ([]*Budget, error) { return LimitReads{}.BudgetsByTeams(ctx, db, []string{"team-a"}) },
		"customers":    func() ([]*Budget, error) { return LimitReads{}.BudgetsByCustomers(ctx, db, []string{"cust-a"}) },
		"modelConfigs": func() ([]*Budget, error) { return LimitReads{}.BudgetsByModelConfigs(ctx, db, []string{"mc-a"}) },
		"providers":    func() ([]*Budget, error) { return LimitReads{}.BudgetsByProviders(ctx, db, []string{"openai"}) },
	} {
		rows, err := read()
		require.NoError(t, err, path)
		requireSubset(t, budgets, budgetDigests(rows), "budgets by "+path)
	}

	var walkedRateLimits []*RateLimit
	for after := ""; ; {
		rows, next, err := LimitReads{}.RateLimitsPage(ctx, db, after, 2)
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
	byIDRates, err := LimitReads{}.RateLimitsByID(ctx, db, ids)
	require.NoError(t, err)
	require.Equal(t, rateLimits, rateLimitDigests(byIDRates), "rate limits by ID")
	for path, read := range map[string]func() ([]*RateLimit, error){
		"keys":         func() ([]*RateLimit, error) { return LimitReads{}.RateLimitsByKeys(ctx, db, []string{"vk-a"}) },
		"teams":        func() ([]*RateLimit, error) { return LimitReads{}.RateLimitsByTeams(ctx, db, []string{"team-a"}) },
		"customers":    func() ([]*RateLimit, error) { return LimitReads{}.RateLimitsByCustomers(ctx, db, []string{"cust-a"}) },
		"modelConfigs": func() ([]*RateLimit, error) { return LimitReads{}.RateLimitsByModelConfigs(ctx, db, []string{"mc-a"}) },
		"providers":    func() ([]*RateLimit, error) { return LimitReads{}.RateLimitsByProviders(ctx, db, []string{"openai"}) },
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

// buildOwnerReads is a LimitReads whose build owners live in build_limit_owners, a table this test
// creates: each row holds a budget or a rate limit with its own calendar flag.
func buildOwnerReads(t *testing.T, db *gorm.DB) LimitReads {
	t.Helper()
	require.NoError(t, db.Exec("CREATE TABLE build_limit_owners (id VARCHAR(64) PRIMARY KEY, budget_id VARCHAR(64), rate_limit_id VARCHAR(64), calendar_aligned BOOLEAN NOT NULL)").Error)
	return LimitReads{
		BudgetOwners:    []LimitOwner{{From: "build_limit_owners o", Column: "o.budget_id", Calendar: "o.calendar_aligned"}},
		RateLimitOwners: []LimitOwner{{From: "build_limit_owners o", Column: "o.rate_limit_id", Calendar: "o.calendar_aligned"}},
	}
}

// buildOwnerFlags writes build owners over the holders limitHolders wrote and returns what a page
// walk gives every budget and rate limit.
func buildOwnerFlags(t *testing.T, db *gorm.DB) (budgets, rateLimits map[string]limitDigest) {
	t.Helper()
	ctx := context.Background()
	limitHolders(t, db)
	reads := buildOwnerReads(t, db)
	for _, row := range []struct {
		id, budget, rateLimit string
		aligned               bool
	}{
		{"o-1", "b-orphan", "rl-orphan", false}, // an unaligned owner alone leaves the flag off...
		{"o-2", "b-orphan", "", true},           // ...one aligned owner turns it on
		{"o-3", "b-bu", "", false},              // an unheld budget with only unaligned owners stays rolling
		{"o-4", "b-key-b", "rl-team", true},     // a registry holder's limit keeps its holder's flag
		{"o-5", "b-prov", "rl-prov", true},      // a provider's limit is held: no build flag
	} {
		require.NoError(t, db.Exec("INSERT INTO build_limit_owners (id, budget_id, rate_limit_id, calendar_aligned) VALUES (?, ?, ?, ?)",
			row.id, row.budget, row.rateLimit, row.aligned).Error)
	}
	var walkedBudgets []*Budget
	for after := ""; ; {
		rows, next, err := reads.BudgetsPage(ctx, db, after, 2)
		require.NoError(t, err)
		walkedBudgets = append(walkedBudgets, rows...)
		if next == "" {
			break
		}
		after = next
	}
	var walkedRateLimits []*RateLimit
	for after := ""; ; {
		rows, next, err := reads.RateLimitsPage(ctx, db, after, 2)
		require.NoError(t, err)
		walkedRateLimits = append(walkedRateLimits, rows...)
		if next == "" {
			break
		}
		after = next
	}
	return budgetDigests(walkedBudgets), rateLimitDigests(walkedRateLimits)
}

// TestBuildOwnersFlagOnlyUnheldLimits checks, on SQLite and on Postgres, that the owners a build
// adds are read in the same query as the registry's own and only decide the calendar flag of a limit
// no registry holder holds: aligned when any of them holding it is, rolling otherwise; every other
// limit keeps exactly what the registry's owners give it.
func TestBuildOwnersFlagOnlyUnheldLimits(t *testing.T) {
	db := newProviderDB(t, nil).DB()
	plainBudgets, plainRateLimits := limitReadPaths(t, newProviderDB(t, nil).DB())
	budgets, rateLimits := buildOwnerFlags(t, db)

	wantBudgets := maps.Clone(plainBudgets)
	wantBudgets["b-orphan"] = limitDigest{calendar: true}
	wantRateLimits := maps.Clone(plainRateLimits)
	require.Equal(t, wantBudgets, budgets)
	require.Equal(t, wantRateLimits, rateLimits, "rl-orphan's only owner is not aligned; held rate limits keep their holder's flag")
	t.Run("postgres", func(t *testing.T) {
		pgBudgets, pgRateLimits := buildOwnerFlags(t, newPostgresConfigStore(t).DB())
		require.Equal(t, budgets, pgBudgets)
		require.Equal(t, rateLimits, pgRateLimits)
	})
}

// parentReads is a LimitReads whose build owners hold limits as children of two build kinds,
// in tables this test creates: build_widget, keyed by text, holds budgets through a link table and a
// rate limit on its own row; build_gadget, keyed by an integer, does the same.
func parentReads(t *testing.T, db *gorm.DB) LimitReads {
	t.Helper()
	for _, ddl := range []string{
		"CREATE TABLE build_widgets (id VARCHAR(64) PRIMARY KEY, rate_limit_id VARCHAR(64), calendar_aligned BOOLEAN NOT NULL)",
		"CREATE TABLE build_widget_budgets (widget_id VARCHAR(64) NOT NULL, budget_id VARCHAR(64) NOT NULL)",
		"CREATE TABLE build_gadgets (id INTEGER PRIMARY KEY, rate_limit_id VARCHAR(64), calendar_aligned BOOLEAN NOT NULL)",
		"CREATE TABLE build_gadget_budgets (gadget_id INTEGER NOT NULL, budget_id VARCHAR(64) NOT NULL)",
	} {
		require.NoError(t, db.Exec(ddl).Error)
	}
	return LimitReads{
		BudgetOwners: []LimitOwner{
			{From: "build_widget_budgets l", Column: "l.budget_id", Joins: "JOIN build_widgets o ON o.id = l.widget_id", Calendar: "o.calendar_aligned", Parent: "build_widget", HolderID: "o.id"},
			{From: "build_gadget_budgets l", Column: "l.budget_id", Joins: "JOIN build_gadgets o ON o.id = l.gadget_id", Calendar: "o.calendar_aligned", Parent: "build_gadget", HolderID: "o.id", NumericHolder: true},
		},
		RateLimitOwners: []LimitOwner{
			{From: "build_widgets o", Column: "o.rate_limit_id", Calendar: "o.calendar_aligned", Parent: "build_widget", HolderID: "o.id"},
			{From: "build_gadgets o", Column: "o.rate_limit_id", Calendar: "o.calendar_aligned", Parent: "build_gadget", HolderID: "o.id", NumericHolder: true},
		},
	}
}

// parentDigest is what the loader decides for one limit a build's owner may hold: the holder its
// links give it, "kind/id" or "", and its calendar flag.
type parentDigest struct {
	parent  string
	aligned bool
}

// parentOf names the build holder each link gives rec, "kind/id", or "" when none does.
func parentOf[T any](links []LimitLink[T], rec *T) string {
	for _, l := range links {
		if id := l.ParentID(rec); id != "" {
			return l.Parent + "/" + id
		}
	}
	return ""
}

// buildParents writes build holders over the holders limitHolders wrote and returns, by limit ID,
// the holder each budget and rate limit names through the links and the flag it takes, and the
// child IDs each link reads for its holders.
func buildParents(t *testing.T, db *gorm.DB) (budgets, rateLimits map[string]parentDigest, children map[string][]string) {
	t.Helper()
	ctx := context.Background()
	limitHolders(t, db)
	reads := parentReads(t, db)
	insert(t, db, budgetRow("b-widget", 11), budgetRow("b-both", 12), budgetRow("b-gadget", 13), rateLimitRow("rl-widget", 11), rateLimitRow("rl-gadget", 12))
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO build_widgets (id, rate_limit_id, calendar_aligned) VALUES (?, ?, ?)", []any{"w-2", "rl-widget", true}},
		{"INSERT INTO build_widgets (id, rate_limit_id, calendar_aligned) VALUES (?, ?, ?)", []any{"w-1", "rl-team", false}},
		{"INSERT INTO build_gadgets (id, rate_limit_id, calendar_aligned) VALUES (?, ?, ?)", []any{7, "rl-gadget", false}},
		{"INSERT INTO build_gadgets (id, rate_limit_id, calendar_aligned) VALUES (?, ?, ?)", []any{42, nil, true}},
		{"INSERT INTO build_widget_budgets (widget_id, budget_id) VALUES (?, ?)", []any{"w-2", "b-widget"}},
		{"INSERT INTO build_widget_budgets (widget_id, budget_id) VALUES (?, ?)", []any{"w-1", "b-widget"}},
		{"INSERT INTO build_widget_budgets (widget_id, budget_id) VALUES (?, ?)", []any{"w-2", "b-both"}},
		{"INSERT INTO build_gadget_budgets (gadget_id, budget_id) VALUES (?, ?)", []any{42, "b-both"}},
		{"INSERT INTO build_gadget_budgets (gadget_id, budget_id) VALUES (?, ?)", []any{7, "b-gadget"}},
		{"INSERT INTO build_gadget_budgets (gadget_id, budget_id) VALUES (?, ?)", []any{42, "b-gadget"}},
		{"INSERT INTO build_gadget_budgets (gadget_id, budget_id) VALUES (?, ?)", []any{42, "b-key"}},
	} {
		require.NoError(t, db.Exec(stmt.sql, stmt.args...).Error)
	}
	budgetLinks, err := reads.BudgetLinks()
	require.NoError(t, err)
	rateLimitLinks, err := reads.RateLimitLinks()
	require.NoError(t, err)
	require.Len(t, budgetLinks, 2)
	require.Len(t, rateLimitLinks, 2)

	budgets, rateLimits = map[string]parentDigest{}, map[string]parentDigest{}
	walkedBudgets, _, err := reads.BudgetsPage(ctx, db, "", 100)
	require.NoError(t, err)
	for _, b := range walkedBudgets {
		budgets[b.ID] = parentDigest{parentOf(budgetLinks, b), b.IsCalendarAligned}
	}
	byID, err := reads.BudgetsByID(ctx, db, []string{"b-widget", "b-both", "b-gadget"})
	require.NoError(t, err)
	readByID := map[string]parentDigest{}
	for _, b := range byID {
		readByID[b.ID] = parentDigest{parentOf(budgetLinks, b), b.IsCalendarAligned}
	}
	require.Equal(t, map[string]parentDigest{"b-widget": budgets["b-widget"], "b-both": budgets["b-both"], "b-gadget": budgets["b-gadget"]}, readByID,
		"a read by ID returns every budget asked for, decided as the page walk decides it")
	walkedRateLimits, _, err := reads.RateLimitsPage(ctx, db, "", 100)
	require.NoError(t, err)
	for _, r := range walkedRateLimits {
		rateLimits[r.ID] = parentDigest{parentOf(rateLimitLinks, r), r.IsCalendarAligned}
	}

	children = map[string][]string{}
	read := func(name string, childIDs func(context.Context, *gorm.DB, []string) ([]string, error), parentIDs ...string) {
		ids, err := childIDs(ctx, db, parentIDs)
		require.NoError(t, err, name)
		slices.Sort(ids)
		children[name] = ids
	}
	read("budgets of w-1, w-2", budgetLinks[0].ChildIDs, "w-1", "w-2")
	read("budgets of 7, 42", budgetLinks[1].ChildIDs, NumericID(7), NumericID(42))
	read("rate limits of w-1, w-2", rateLimitLinks[0].ChildIDs, "w-1", "w-2")
	read("rate limits of 7, 42", rateLimitLinks[1].ChildIDs, NumericID(7), NumericID(42))
	return budgets, rateLimits, children
}

// TestBuildOwnersWithAParentNameTheirHolder checks, on SQLite and on Postgres, that an owner a
// build adds with a Parent names its holder on the limits it holds, a numeric holder by its record
// ID: a limit two holders of one owner hold names the one whose ID sorts first as text, and is
// calendar aligned when either is, a limit two owners hold names the first owner's holder, and a
// limit a registry holder holds names the build's holder too. Each link reads the IDs of the limits
// its holders hold, through every owner of its kind. Owners without a Parent give no link, and
// owners a link cannot be built from are refused.
func TestBuildOwnersWithAParentNameTheirHolder(t *testing.T) {
	budgets, rateLimits, children := buildParents(t, newProviderDB(t, nil).DB())
	widget, gadget := "build_widget/", "build_gadget/"
	require.Equal(t, parentDigest{widget + "w-1", true}, budgets["b-widget"], "two holders of one owner: the lower ID, aligned since w-2 is")
	require.Equal(t, parentDigest{widget + "w-2", true}, budgets["b-both"], "two owners: the first one's holder")
	require.Equal(t, parentDigest{gadget + NumericID(42), true}, budgets["b-gadget"], "numeric holders sort as text: 42 before 7")
	require.Equal(t, parentDigest{gadget + NumericID(42), true}, budgets["b-key"], "a key's budget names the build's holder too, with the key's flag")
	require.Equal(t, parentDigest{}, budgets["b-orphan"])
	require.Equal(t, parentDigest{widget + "w-2", true}, rateLimits["rl-widget"])
	require.Equal(t, parentDigest{widget + "w-1", true}, rateLimits["rl-team"], "a team's rate limit names the build's holder too, with the team's flag")
	require.Equal(t, parentDigest{gadget + NumericID(7), false}, rateLimits["rl-gadget"])
	require.Equal(t, parentDigest{}, rateLimits["rl-orphan"])
	require.Equal(t, map[string][]string{
		"budgets of w-1, w-2":     {"b-both", "b-widget", "b-widget"},
		"budgets of 7, 42":        {"b-both", "b-gadget", "b-gadget", "b-key"},
		"rate limits of w-1, w-2": {"rl-team", "rl-widget"},
		"rate limits of 7, 42":    {"rl-gadget"},
	}, children)
	t.Run("postgres", func(t *testing.T) {
		pgBudgets, pgRateLimits, pgChildren := buildParents(t, newPostgresConfigStore(t).DB())
		require.Equal(t, budgets, pgBudgets)
		require.Equal(t, rateLimits, pgRateLimits)
		require.Equal(t, children, pgChildren)
	})

	noParent := LimitReads{BudgetOwners: []LimitOwner{
		{From: "build_limit_owners o", Column: "o.budget_id", Calendar: "o.calendar_aligned"},
		{From: "build_limit_owners o", Column: "o.budget_id", HolderID: "o.id"},
	}}
	links, err := noParent.BudgetLinks()
	require.NoError(t, err)
	require.Empty(t, links, "an owner without a Parent gives no link, with a HolderID or without")
	for name, owners := range map[string][]LimitOwner{
		"parent without holder":  {{From: "t o", Column: "o.budget_id", Parent: "build_widget"}},
		"numeric without holder": {{From: "t o", Column: "o.budget_id", NumericHolder: true}},
		"numeric disagreement": {
			{From: "t o", Column: "o.budget_id", Parent: "build_widget", HolderID: "o.id"},
			{From: "u o", Column: "o.budget_id", Parent: "build_widget", HolderID: "o.id", NumericHolder: true},
		},
	} {
		_, err := LimitReads{BudgetOwners: owners}.BudgetLinks()
		require.Error(t, err, name)
	}
}
