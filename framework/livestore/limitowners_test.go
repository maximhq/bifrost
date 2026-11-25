package livestore

import (
	"context"
	"strconv"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The kinds a test build adds, whose records hold budgets and rate limits through owners with a
// Parent: a widget is keyed by text and holds budgets through a link table and a rate limit on its
// own row; a gadget is keyed by an integer and holds a rate limit on its row.
const (
	kindWidget Kind = "widget"
	kindGadget Kind = "gadget"
)

// widget is a test build's record keyed by text.
type widget struct{ ID, Name string }

// gadget is a test build's record keyed by an integer, by its NumericID.
type gadget struct{ ID string }

// widgetBudgetOwners and widgetRateLimitOwners are the owners the test build adds through
// LimitOwners.
var (
	widgetBudgetOwners = []registry.LimitOwner{
		{From: "build_widget_budgets l", Column: "l.budget_id", Joins: "JOIN build_widgets o ON o.id = l.widget_id", Calendar: "o.calendar_aligned", Parent: string(kindWidget), HolderID: "o.id"},
	}
	widgetRateLimitOwners = []registry.LimitOwner{
		{From: "build_widgets o", Column: "o.rate_limit_id", Calendar: "o.calendar_aligned", Parent: string(kindWidget), HolderID: "o.id"},
		{From: "build_gadgets o", Column: "o.rate_limit_id", Calendar: "o.calendar_aligned", Parent: string(kindGadget), HolderID: "o.id", NumericHolder: true},
	}
)

// widgetDB returns governanceDB's database with the test build's tables: widgets w-1 and w-2, w-1
// holding budgets b-w1 and b-move and rate limit rl-w1, w-2 holding budgets b-w2 and b-swap, and
// gadget 7 holding rate limit rl-g.
func widgetDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := governanceDB(t).DB()
	exec(t, db,
		"CREATE TABLE build_widgets (id VARCHAR(64) PRIMARY KEY, name VARCHAR(64) NOT NULL, rate_limit_id VARCHAR(64), calendar_aligned BOOLEAN NOT NULL DEFAULT FALSE)",
		"CREATE TABLE build_widget_budgets (widget_id VARCHAR(64) NOT NULL, budget_id VARCHAR(64) NOT NULL)",
		"CREATE TABLE build_gadgets (id INTEGER PRIMARY KEY, rate_limit_id VARCHAR(64), calendar_aligned BOOLEAN NOT NULL DEFAULT FALSE)",
		"INSERT INTO build_widgets (id, name, rate_limit_id) VALUES ('w-1', 'one', 'rl-w1'), ('w-2', 'two', NULL)",
		"INSERT INTO build_widget_budgets (widget_id, budget_id) VALUES ('w-1', 'b-w1'), ('w-1', 'b-move'), ('w-2', 'b-w2'), ('w-2', 'b-swap')",
		"INSERT INTO build_gadgets (id, rate_limit_id) VALUES (7, 'rl-g')",
	)
	addBudgets(t, db, "b-w1", "b-w2", "b-move", "b-swap")
	window, requests := "1m", int64(1)
	for _, id := range []string{"rl-w1", "rl-g", "rl-g2"} {
		require.NoError(t, db.Create(&tables.TableRateLimit{ID: id, RequestMaxLimit: &requests, RequestResetDuration: &window}).Error)
	}
	return db
}

// addBudgets writes a budget row, held by no registry holder, for each of ids.
func addBudgets(t *testing.T, db *gorm.DB, ids ...string) {
	t.Helper()
	for _, id := range ids {
		require.NoError(t, db.Create(&tables.TableBudget{ID: id, MaxLimit: 1, ResetDuration: "1d"}).Error)
	}
}

// exec runs each statement on db.
func exec(t *testing.T, db *gorm.DB, stmts ...string) {
	t.Helper()
	for _, stmt := range stmts {
		require.NoError(t, db.Exec(stmt).Error, stmt)
	}
}

// widgetStore returns a store built with the test build's limit owners and its widget and gadget
// collections, loaded from db.
func widgetStore(t *testing.T, db *gorm.DB) (*Store, *Collection[widget], *Collection[gadget]) {
	t.Helper()
	s, err := New(LimitOwners(widgetBudgetOwners, widgetRateLimitOwners))
	require.NoError(t, err)
	t.Cleanup(s.Close)
	limits := []Kind{KindBudget, KindRateLimit}
	widgets, err := s.Register(kindWidget, limits, Loader[widget]{
		ID: func(w *widget) string { return w.ID },
		Page: func(ctx context.Context, db *gorm.DB, after string, limit int) ([]*widget, string, error) {
			var rows []*widget
			err := db.WithContext(ctx).Raw("SELECT id, name FROM build_widgets WHERE id > ? ORDER BY id LIMIT ?", after, limit).Scan(&rows).Error
			if err != nil || len(rows) < limit {
				return rows, "", err
			}
			return rows, rows[len(rows)-1].ID, nil
		},
		ByID: func(ctx context.Context, db *gorm.DB, ids []string) ([]*widget, error) {
			var rows []*widget
			err := db.WithContext(ctx).Raw("SELECT id, name FROM build_widgets WHERE id IN ?", ids).Scan(&rows).Error
			return rows, err
		},
	})
	require.NoError(t, err)
	gadgetRows := func(ctx context.Context, db *gorm.DB, where string, args ...any) ([]*gadget, error) {
		var ids []uint
		if err := db.WithContext(ctx).Raw("SELECT id FROM build_gadgets"+where+" ORDER BY id", args...).Scan(&ids).Error; err != nil {
			return nil, err
		}
		rows := make([]*gadget, 0, len(ids))
		for _, id := range ids {
			rows = append(rows, &gadget{ID: registry.NumericID(id)})
		}
		return rows, nil
	}
	gadgets, err := s.Register(kindGadget, limits, Loader[gadget]{
		ID: func(g *gadget) string { return g.ID },
		Page: func(ctx context.Context, db *gorm.DB, after string, limit int) ([]*gadget, string, error) {
			rows, err := gadgetRows(ctx, db, "")
			return rows, "", err
		},
		ByID: func(ctx context.Context, db *gorm.DB, ids []string) ([]*gadget, error) {
			numbers := make([]uint, 0, len(ids))
			for _, id := range ids {
				n, err := strconv.ParseUint(id, 10, 64)
				if err != nil {
					return nil, err
				}
				numbers = append(numbers, uint(n))
			}
			return gadgetRows(ctx, db, " WHERE id IN ?", numbers)
		},
	})
	require.NoError(t, err)
	require.NoError(t, s.LoadAll(context.Background(), db))
	return s, widgets, gadgets
}

// requireHeld fails unless the store holds exactly the budgets and rate limits want marks true.
func requireHeld(t *testing.T, s *Store, step string, budgets, rateLimits map[string]bool) {
	t.Helper()
	for id, want := range budgets {
		_, ok := s.Collections.Budgets.Get(id)
		require.Equal(t, want, ok, "%s: budget %s", step, id)
	}
	for id, want := range rateLimits {
		_, ok := s.Collections.RateLimits.Get(id)
		require.Equal(t, want, ok, "%s: rate limit %s", step, id)
	}
}

// TestBuildOwnedLimitsAreChildrenOfTheirHolders checks that the limits an owner a build adds with a
// Parent holds are children of that kind, as a key's or a team's are: a refresh of a holder finds a
// limit it was never told about, keeps one that moved to another holder of the kind in either
// refresh order (so the old holder's later refresh and its removal leave it alone), drops one no
// holder holds any more, and removing a holder removes its limits; a numeric holder's limits follow
// it by its record ID; and a refresh whose read of a holder's limits fails publishes nothing.
func TestBuildOwnedLimitsAreChildrenOfTheirHolders(t *testing.T) {
	ctx := context.Background()
	db := widgetDB(t)
	s, widgets, _ := widgetStore(t, db)
	requireHeld(t, s, "load", map[string]bool{"b-w1": true, "b-w2": true, "b-move": true, "b-swap": true}, map[string]bool{"rl-w1": true, "rl-g": true})

	addBudgets(t, db, "b-new")
	exec(t, db, "INSERT INTO build_widget_budgets (widget_id, budget_id) VALUES ('w-1', 'b-new')")
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: kindWidget, ID: "w-1"}}))
	requireHeld(t, s, "a refresh of w-1 finds its new budget", map[string]bool{"b-new": true}, nil)

	// b-move goes from w-1 to w-2, refreshed new holder first; b-swap from w-2 to w-1, refreshed old
	// holder first.
	exec(t, db,
		"UPDATE build_widget_budgets SET widget_id = 'w-2' WHERE budget_id = 'b-move'",
		"UPDATE build_widget_budgets SET widget_id = 'w-1' WHERE budget_id = 'b-swap'",
	)
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: kindWidget, ID: "w-2"}}))
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: kindWidget, ID: "w-1"}}))
	requireHeld(t, s, "moved budgets stay", map[string]bool{"b-move": true, "b-swap": true}, nil)

	exec(t, db, "DELETE FROM build_widget_budgets WHERE budget_id = 'b-w2'")
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: kindWidget, ID: "w-2"}}))
	requireHeld(t, s, "a budget no widget holds is dropped", map[string]bool{"b-w2": false, "b-move": true}, nil)

	exec(t, db, "DELETE FROM build_widget_budgets WHERE widget_id = 'w-1'", "DELETE FROM build_widgets WHERE id = 'w-1'")
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: kindWidget, ID: "w-1", Remove: true}}))
	requireHeld(t, s, "removing w-1 removes what it holds and nothing it gave up",
		map[string]bool{"b-w1": false, "b-new": false, "b-swap": false, "b-move": true}, map[string]bool{"rl-w1": false})

	gadgetID := registry.NumericID(7)
	exec(t, db, "UPDATE build_gadgets SET rate_limit_id = 'rl-g2' WHERE id = 7")
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: kindGadget, ID: gadgetID}}))
	requireHeld(t, s, "a gadget's refresh follows its rate limit", nil, map[string]bool{"rl-g": false, "rl-g2": true})
	exec(t, db, "DELETE FROM build_gadgets WHERE id = 7")
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: kindGadget, ID: gadgetID, Remove: true}}))
	requireHeld(t, s, "removing the gadget removes its rate limit", nil, map[string]bool{"rl-g2": false})

	addBudgets(t, db, "b-late")
	exec(t, db,
		"UPDATE build_widgets SET name = 'renamed' WHERE id = 'w-2'",
		"INSERT INTO build_widget_budgets (widget_id, budget_id) VALUES ('w-2', 'b-late')",
		"ALTER TABLE build_widget_budgets RENAME TO build_widget_budgets_gone",
	)
	require.Error(t, s.Refresh(ctx, []Ref{{Kind: kindWidget, ID: "w-2"}}))
	w, ok := widgets.Get("w-2")
	require.True(t, ok)
	require.Equal(t, "two", w.Name, "a failed refresh publishes nothing")
	requireHeld(t, s, "a failed refresh publishes nothing", map[string]bool{"b-late": false, "b-move": true}, nil)

	exec(t, db, "ALTER TABLE build_widget_budgets_gone RENAME TO build_widget_budgets")
	exec(t, db, "DELETE FROM build_widgets WHERE id = 'w-2'")
	require.NoError(t, s.Refresh(ctx, []Ref{{Kind: kindWidget, ID: "w-2", Remove: true}}))
	requireHeld(t, s, "removing w-2 removes the budget that moved to it", map[string]bool{"b-move": false}, nil)
}

// TestLimitLinksOnlyForOwnersWithAParent checks that the limit collections link to a kind of the
// build's own only for an owner that names it as Parent: a store without LimitOwners, or with owners
// that only set calendar flags, has exactly the registry's links, and owners a link cannot be built
// from fail New.
func TestLimitLinksOnlyForOwnersWithAParent(t *testing.T) {
	parentsOf := func(s *Store) map[Kind][]Kind {
		out := map[Kind][]Kind{}
		for parent, links := range s.children {
			for _, l := range links {
				if child := l.child.kindName(); child == KindBudget || child == KindRateLimit {
					out[child] = append(out[child], parent)
				}
			}
		}
		return out
	}
	registryParents := []Kind{KindVirtualKey, KindTeam, KindCustomer, KindModelConfig, KindProvider}
	flagOnly := []registry.LimitOwner{{From: "build_limit_owners o", Column: "o.budget_id", Calendar: "o.calendar_aligned"}}
	for name, opts := range map[string][]StoreOption{"no owners": nil, "flag-only owners": {LimitOwners(flagOnly, flagOnly)}} {
		s, err := New(opts...)
		require.NoError(t, err, name)
		got := parentsOf(s)
		require.ElementsMatch(t, registryParents, got[KindBudget], name)
		require.ElementsMatch(t, registryParents, got[KindRateLimit], name)
		s.Close()
	}

	s, err := New(LimitOwners(widgetBudgetOwners, widgetRateLimitOwners))
	require.NoError(t, err)
	got := parentsOf(s)
	require.ElementsMatch(t, append([]Kind{kindWidget}, registryParents...), got[KindBudget])
	require.ElementsMatch(t, append([]Kind{kindWidget, kindGadget}, registryParents...), got[KindRateLimit])
	s.Close()

	_, err = New(LimitOwners([]registry.LimitOwner{{From: "t o", Column: "o.budget_id", Parent: string(kindWidget)}}, nil))
	require.Error(t, err, "a Parent without a HolderID")
	_, err = New(LimitOwners([]registry.LimitOwner{{From: "t o", Column: "o.budget_id", Parent: string(KindTeam), HolderID: "o.id"}}, nil))
	require.Error(t, err, "a second link to a kind the registry already links")
}
