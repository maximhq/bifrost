package governance

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// newStandaloneStore is a governance store over an empty live store with a database, so a test
// can add limits with putBudget and putRateLimit.
func newStandaloneStore(tb testing.TB) *LocalGovernanceStore {
	tb.Helper()
	h := newLiveHarness(tb, nil, nil)
	store, err := NewLocalGovernanceStore(context.Background(), NewMockLogger(), nil, h.live, nil)
	require.NoError(tb, err)
	tb.Cleanup(store.Close)
	return store
}

// putBudget makes the store hold budget under id outright, the way storing it in the old budgets
// map did: its row is written with id and brought into the live store, which installs its settings
// and override, and its usage, last reset and reset evidence replace the live ones. A budget the store did not hold
// counts from 0; one it held keeps its baseline and reset generation. The row is written as
// writeLimitRow writes it.
func putBudget(tb testing.TB, store *LocalGovernanceStore, id string, budget *configstoreTables.TableBudget) {
	tb.Helper()
	row := *budget
	row.ID = id
	_, held := store.budgetUsage.Get(id)
	recordCalendarFlag(tb, store, livestore.KindBudget, id, budget.IsCalendarAligned)
	writeLimitRow(tb, store, &row)
	refreshLimit(tb, store, livestore.KindBudget, id, false)
	setUsage(store.budgetUsage, id, func(s *LimitState) {
		s.Usage[BudgetSlot] = DollarCount(budget.CurrentUsage)
		s.setLastReset(BudgetSlot, budget.LastReset)
		s.ResetEpoch[BudgetSlot] = budget.UsageResetEpoch
		if !held {
			s.LastDBUsage[BudgetSlot] = Count{}
		}
	})
}

// putRateLimit is putBudget for rate limits.
func putRateLimit(tb testing.TB, store *LocalGovernanceStore, id string, rateLimit *configstoreTables.TableRateLimit) {
	tb.Helper()
	row := *rateLimit
	row.ID = id
	_, held := store.rateLimitUsage.Get(id)
	recordCalendarFlag(tb, store, livestore.KindRateLimit, id, rateLimit.IsCalendarAligned)
	writeLimitRow(tb, store, &row)
	refreshLimit(tb, store, livestore.KindRateLimit, id, false)
	setUsage(store.rateLimitUsage, id, func(s *LimitState) {
		s.Usage[TokenSlot] = WholeCount(rateLimit.TokenCurrentUsage)
		s.Usage[RequestSlot] = WholeCount(rateLimit.RequestCurrentUsage)
		s.setLastReset(TokenSlot, rateLimit.TokenLastReset)
		s.setLastReset(RequestSlot, rateLimit.RequestLastReset)
		s.ResetEpoch[TokenSlot] = rateLimit.TokenUsageResetEpoch
		s.ResetEpoch[RequestSlot] = rateLimit.RequestUsageResetEpoch
		if !held {
			s.LastDBUsage = [2]Count{}
		}
	})
}

// writeLimitRow writes a budget or rate limit row to the database store's live store reads,
// replacing a row with the same ID. The table's hooks are skipped, so a test may write settings
// they would refuse; a budget's reset config is serialized as they would. It reports a failure with
// Errorf, so it may run off the test's goroutine.
func writeLimitRow(tb testing.TB, store *LocalGovernanceStore, row any) {
	tb.Helper()
	if budget, ok := row.(*configstoreTables.TableBudget); ok {
		budget.ResetConfigJSON = ""
		if budget.ResetConfig != nil {
			data, err := json.Marshal(budget.ResetConfig)
			if err != nil {
				tb.Errorf("serialize reset config: %v", err)
				return
			}
			budget.ResetConfigJSON = string(data)
		}
	}
	var createdAt time.Time
	switch r := row.(type) {
	case *configstoreTables.TableBudget:
		createdAt = r.CreatedAt
	case *configstoreTables.TableRateLimit:
		createdAt = r.CreatedAt
	}
	db := harnessDB(tb, store).Session(&gorm.Session{SkipHooks: true})
	if err := db.Clauses(clause.OnConflict{UpdateAll: true}).Create(row).Error; err != nil {
		tb.Errorf("write limit row: %v", err)
		return
	}
	// A limit written with no creation time keeps none, as an in-memory limit did: the database
	// would stamp the write's time, and a window anchored there treats an earlier last reset as
	// expired. With none, each counter's window anchors at its own last reset.
	if createdAt.IsZero() {
		if err := db.Model(row).UpdateColumn("created_at", time.Time{}).Error; err != nil {
			tb.Errorf("clear limit row creation time: %v", err)
		}
	}
}

// refreshLimit brings a budget or rate limit record into the live store from its row, or drops it
// with remove, as a write to its row followed by its holder's reload does. It reports a failure with
// Errorf, so it may run off the test's goroutine.
func refreshLimit(tb testing.TB, store *LocalGovernanceStore, kind livestore.Kind, id string, remove bool) {
	tb.Helper()
	if err := store.live.Refresh(context.Background(), []livestore.Ref{{Kind: kind, ID: id, Remove: remove}}); err != nil {
		tb.Errorf("refresh %s %s: %v", kind, id, err)
	}
}

// reloadBudget writes budget as id's row and brings it into the live store, as a reload of its
// holder after a write does: usage the store holds is kept, a budget it does not hold is seeded
// from the row, and the row's override replaces the held one.
func reloadBudget(tb testing.TB, store *LocalGovernanceStore, id string, budget *configstoreTables.TableBudget) {
	tb.Helper()
	row := *budget
	row.ID = id
	recordCalendarFlag(tb, store, livestore.KindBudget, id, budget.IsCalendarAligned)
	writeLimitRow(tb, store, &row)
	refreshLimit(tb, store, livestore.KindBudget, id, false)
}

// reloadRateLimit is reloadBudget for rate limits.
func reloadRateLimit(tb testing.TB, store *LocalGovernanceStore, id string, rateLimit *configstoreTables.TableRateLimit) {
	tb.Helper()
	row := *rateLimit
	row.ID = id
	recordCalendarFlag(tb, store, livestore.KindRateLimit, id, rateLimit.IsCalendarAligned)
	writeLimitRow(tb, store, &row)
	refreshLimit(tb, store, livestore.KindRateLimit, id, false)
}

// dropLimit removes a budget or rate limit record from the live store, as deleting its row does.
func dropLimit(tb testing.TB, store *LocalGovernanceStore, kind livestore.Kind, id string) {
	tb.Helper()
	refreshLimit(tb, store, kind, id, true)
}

// setUsage stores set's change of the state id holds in t, or of an empty state when it holds
// none, retrying against concurrent writers.
func setUsage(t *livestore.Table[string, *LimitState], id string, set func(*LimitState)) {
	for {
		cur, _ := t.Get(id)
		var next LimitState
		if cur != nil {
			next = *cur
		}
		set(&next)
		if livestore.CompareAndSwap(t, id, cur, &next) {
			return
		}
	}
}

// setBudgetBaseline records baseline as the usage a budget last persisted, as RebaseBudget does.
func setBudgetBaseline(tb testing.TB, store *LocalGovernanceStore, id string, baseline float64) {
	tb.Helper()
	require.True(tb, store.UpdateBudgetState(id, func(s LimitState) (LimitState, bool) {
		s.LastDBUsage[BudgetSlot] = DollarCount(baseline)
		return s, true
	}), "budget %s holds no state", id)
}

// setRateLimitBaselines is setBudgetBaseline for a rate limit's token and request counters.
func setRateLimitBaselines(tb testing.TB, store *LocalGovernanceStore, id string, tokens, requests int64) {
	tb.Helper()
	require.True(tb, store.UpdateRateLimitState(id, func(s LimitState) (LimitState, bool) {
		s.LastDBUsage[TokenSlot] = WholeCount(tokens)
		s.LastDBUsage[RequestSlot] = WholeCount(requests)
		return s, true
	}), "rate limit %s holds no state", id)
}

// budgetBaseline is the baseline a budget's state holds, and false when it holds no state.
func budgetBaseline(store *LocalGovernanceStore, id string) (float64, bool) {
	st, ok := store.BudgetState(id)
	return st.LastDBUsage[BudgetSlot].Dollars(), ok
}

// rateLimitBaselines are the token and request baselines a rate limit's state holds, and false when
// it holds no state.
func rateLimitBaselines(store *LocalGovernanceStore, id string) (int64, int64, bool) {
	st, ok := store.RateLimitState(id)
	return st.LastDBUsage[TokenSlot].Whole(), st.LastDBUsage[RequestSlot].Whole(), ok
}

// TestGetVirtualKeyByID exercises the ID-keyed lookup directly without taking
// the full governance snapshot path.
func TestGetVirtualKeyByID(t *testing.T) {
	vk := &configstoreTables.TableVirtualKey{ID: "vk-id", Name: "test", Value: *schemas.NewSecretVar("sk-bf-test")}
	store, err := newTestGovernanceStore(t, NewMockLogger(), &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)

	got, found := store.GetVirtualKeyByID(context.Background(), vk.ID)
	require.True(t, found)
	assert.Same(t, storedVirtualKey(t, store, vk.ID), got, "the lookup hands back the live record itself")
	assert.Equal(t, "test", got.Name)

	got, found = store.GetVirtualKeyByID(context.Background(), "missing")
	assert.False(t, found)
	assert.Nil(t, got)
}

// TestBumpBudgetUsage_NoLostIncrements proves the CAS retry loop in
// BumpBudgetUsage never drops a concurrent increment. Without the CAS, the
// Load→clone→mutate→Store sequence races and the final CurrentUsage ends up
// strictly less than N*cost under contention.
func TestBumpBudgetUsage_NoLostIncrements(t *testing.T) {
	store := newStandaloneStore(t)
	budgetID := "concurrent-budget"
	putBudget(t, store, budgetID, buildBudget(budgetID, 1_000_000_000, "24h"))

	const goroutines = 256
	const perGoroutine = 50
	const cost = 1.0
	expected := float64(goroutines * perGoroutine)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				assert.NoError(t, store.BumpBudgetUsage(context.Background(), budgetID, cost))
			}
		}()
	}
	wg.Wait()

	final := store.LoadBudget(context.Background(), budgetID)
	require.NotNil(t, final)
	assert.Equal(t, expected, final.CurrentUsage, "CurrentUsage must equal total increments — any shortfall is a dropped write")
}

// TestBumpRateLimitUsage_NoLostIncrements covers the rate-limit variant of
// the same race: token and request counters are independent int64 fields
// updated on the same struct, and both must survive contention intact.
func TestBumpRateLimitUsage_NoLostIncrements(t *testing.T) {
	store := newStandaloneStore(t)
	rlID := "concurrent-rate-limit"
	putRateLimit(t, store, rlID, buildRateLimit(rlID, 1_000_000_000, 1_000_000_000))

	const goroutines = 256
	const perGoroutine = 50
	const tokensPerCall = int64(7)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				assert.NoError(t, store.BumpRateLimitUsage(context.Background(), rlID, tokensPerCall, true, true))
			}
		}()
	}
	wg.Wait()

	final := store.LoadRateLimit(context.Background(), rlID)
	require.NotNil(t, final)
	assert.Equal(t, int64(goroutines*perGoroutine)*tokensPerCall, final.TokenCurrentUsage, "TokenCurrentUsage dropped increments")
	assert.Equal(t, int64(goroutines*perGoroutine), final.RequestCurrentUsage, "RequestCurrentUsage dropped increments")
}

// TestResetBudgetAt_ConcurrentResettersCollapse confirms that many goroutines
// all trying to reset the same budget to the same newLastReset deduplicate
// cleanly via CAS — exactly one resetter observes the transition, everyone
// else gets (nil, false). Without the re-check inside ResetBudgetAt, each
// goroutine would re-zero the counter and drop any increments applied in
// between.
func TestResetBudgetAt_ConcurrentResettersCollapse(t *testing.T) {
	store := newStandaloneStore(t)
	budgetID := "reset-collapse"
	const goroutines = 128
	// Exactly one window between the grant anchor and the reset target, so the
	// derived remaining count is unambiguous: 5 granted minus 1 window closed.
	newLastReset := time.Now().Truncate(time.Second)
	grantAnchor := newLastReset.Add(-time.Hour)

	old := buildBudget(budgetID, 1000, "1h")
	old.CreatedAt = grantAnchor
	old.LastReset = grantAnchor
	old.CurrentUsage = 999
	require.NoError(t, old.SetOverrideAt(25, configstoreTables.BudgetOverrideModeCycles, 5, grantAnchor))
	putBudget(t, store, budgetID, old)

	var successes atomic.Int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if _, ok := store.ResetBudgetAt(context.Background(), budgetID, newLastReset); ok {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int64(1), successes.Load(), "exactly one resetter should win the CAS when all target the same newLastReset")
	final := store.LoadBudget(context.Background(), budgetID)
	require.NotNil(t, final)
	assert.Equal(t, 0.0, final.CurrentUsage)
	assert.True(t, final.LastReset.Equal(newLastReset))
	assert.Equal(t, 4, final.OverrideCyclesRemaining, "the single winning reset should consume exactly one override cycle")
}

// TestAdoptCalendarAlignmentInMemoryPreservesConcurrentSpend pins that adopting a
// budget onto the calendar grid keeps whatever usage landed while the switch was
// in flight.
//
// Adoption cannot read usage, then write it back: a request bumping the same
// budget between those two steps would have its spend silently dropped. The CAS
// loop has to carry the usage it observed at swap time, which is what this test
// forces by bumping usage from another goroutine during the adoption.
func TestAdoptCalendarAlignmentInMemoryPreservesConcurrentSpend(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStore(t)
	now := time.Date(2026, time.February, 5, 12, 0, 0, 0, time.UTC)
	monthStart := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)

	budget := &configstoreTables.TableBudget{
		ID:                "adopt-live-budget",
		MaxLimit:          1000,
		CurrentUsage:      0,
		ResetDuration:     "1M",
		IsCalendarAligned: true,
		CreatedAt:         time.Date(2026, time.January, 10, 9, 0, 0, 0, time.UTC),
		LastReset:         time.Date(2026, time.January, 10, 9, 0, 0, 0, time.UTC),
	}
	putBudget(t, store, budget.ID, budget)

	// Usage is bumped by a direct state swap rather than through BumpBudgetUsage, which
	// consults the real clock: a January window is long overdue against it, so the
	// request path would reset the budget onto the current real month and the fixed
	// `now` below could no longer move it. The property under test is that the
	// adoption CAS carries whatever usage it observed, and a plain increment races
	// it just as well without dragging real time into the fixture.
	const bumps = 50
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < bumps; i++ {
			require.True(t, store.UpdateBudgetState(budget.ID, func(s LimitState) (LimitState, bool) {
				s.Usage[BudgetSlot] = DollarCount(s.Usage[BudgetSlot].Dollars() + 1)
				return s, true
			}))
		}
	}()
	adopted := store.AdoptCalendarAlignmentInMemory(ctx, budget.ID, now)
	wg.Wait()

	assert.True(t, adopted, "a window opened before the boundary must be adopted")

	live := store.LoadBudget(ctx, budget.ID)
	require.NotNil(t, live)
	assert.True(t, live.LastReset.Equal(monthStart), "the window is re-anchored on the boundary")
	assert.Equal(t, float64(bumps), live.CurrentUsage,
		"every concurrent bump survived: adoption changed the boundary, not the accounting")
}

// TestApplyBaselinesPreservesChargesAndWindows checks same-window shifts and
// rejects old-window values independently for token and request counters.
func TestApplyBaselinesPreservesChargesAndWindows(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStore(t)
	boundary := time.Now().Truncate(time.Hour)
	putBudget(t, store, "budget", &configstoreTables.TableBudget{ID: "budget", CurrentUsage: 107, LastReset: boundary})
	setBudgetBaseline(t, store, "budget", 100)
	require.True(t, store.ApplyBudgetBaselineWithResetEpoch(ctx, "budget", 150, boundary, 0))
	require.Equal(t, 157.0, store.LoadBudget(ctx, "budget").CurrentUsage)
	next := boundary.Add(time.Hour)
	_, reset := store.ResetBudgetAt(ctx, "budget", next)
	require.True(t, reset)
	require.False(t, store.ApplyBudgetBaselineWithResetEpoch(ctx, "budget", 200, boundary, 0))
	require.Zero(t, store.LoadBudget(ctx, "budget").CurrentUsage)
	budgetLastDB, _ := budgetBaseline(store, "budget")
	require.Zero(t, budgetLastDB)
	putRateLimit(t, store, "rate", &configstoreTables.TableRateLimit{ID: "rate", TokenCurrentUsage: 11, RequestCurrentUsage: 7, TokenLastReset: next, RequestLastReset: boundary})
	setRateLimitBaselines(t, store, "rate", 0, 5)
	tokens, requests := store.ApplyRateLimitBaselinesWithResetEpochs(ctx, "rate", 200, 10, boundary, boundary, 0, 0)
	require.False(t, tokens)
	require.True(t, requests)
	require.Equal(t, int64(11), store.LoadRateLimit(ctx, "rate").TokenCurrentUsage)
	require.Equal(t, int64(12), store.LoadRateLimit(ctx, "rate").RequestCurrentUsage)
	tokenLastDB, requestLastDB, _ := rateLimitBaselines(store, "rate")
	require.Zero(t, tokenLastDB)
	require.Equal(t, int64(10), requestLastDB)
}

// TestApplyBaselinesConcurrentReset keeps a completed reset's zero baselines
// paired with its counters, regardless of which operation wins the CAS.
func TestApplyBaselinesConcurrentReset(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStore(t)
	boundary := time.Now().Truncate(time.Hour)
	for range 500 {
		putBudget(t, store, "budget", &configstoreTables.TableBudget{ID: "budget", CurrentUsage: 107, LastReset: boundary})
		setBudgetBaseline(t, store, "budget", 100)
		putRateLimit(t, store, "rate", &configstoreTables.TableRateLimit{ID: "rate", TokenCurrentUsage: 107, RequestCurrentUsage: 107, TokenLastReset: boundary, RequestLastReset: boundary})
		setRateLimitBaselines(t, store, "rate", 100, 100)
		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		go func() {
			defer wg.Done()
			<-start
			store.ApplyBudgetBaselineWithResetEpoch(ctx, "budget", 150, boundary, 0)
			store.ApplyRateLimitBaselinesWithResetEpochs(ctx, "rate", 150, 150, boundary, boundary, 0, 0)
		}()
		go func() {
			defer wg.Done()
			<-start
			next := boundary.Add(time.Hour)
			store.ResetBudgetAt(ctx, "budget", next)
			store.ResetRateLimitAt(ctx, "rate", &next, &next)
		}()
		close(start)
		wg.Wait()
		require.Zero(t, store.LoadBudget(ctx, "budget").CurrentUsage)
		budgetLastDB, _ := budgetBaseline(store, "budget")
		require.Zero(t, budgetLastDB)
		require.Zero(t, store.LoadRateLimit(ctx, "rate").TokenCurrentUsage)
		require.Zero(t, store.LoadRateLimit(ctx, "rate").RequestCurrentUsage)
		tokenLastDB, requestLastDB, _ := rateLimitBaselines(store, "rate")
		require.Zero(t, tokenLastDB)
		require.Zero(t, requestLastDB)
	}
}

// TestSeedConfigPublishesUsageWithItsBaseline keeps a seed from ever being observed half done: a
// capture reading while a limit is seeded sees no counter, or the counter with the baseline its
// usage counts from, never a counter with baseline zero. Usage and baseline are one state, stored
// in one swap, so no lock is needed for this.
func TestSeedConfigPublishesUsageWithItsBaseline(t *testing.T) {
	for _, dimension := range []string{"budget", "rate limit"} {
		t.Run(dimension, func(t *testing.T) {
			ctx := context.Background()
			for range 100 {
				store := newStandaloneStore(t)
				done := make(chan struct{})
				go func() {
					defer close(done)
					if dimension == "budget" {
						reloadBudget(t, store, "seed", &configstoreTables.TableBudget{ID: "seed", CurrentUsage: 100})
					} else {
						reloadRateLimit(t, store, "seed", &configstoreTables.TableRateLimit{ID: "seed", TokenCurrentUsage: 100, RequestCurrentUsage: 20})
					}
				}()
				for finished := false; !finished; {
					select {
					case <-done:
						finished = true
					default:
					}
					var st LimitState
					var held, loaded bool
					if dimension == "budget" {
						st, held = store.BudgetState("seed")
						loaded = store.LoadBudget(ctx, "seed") != nil
					} else {
						st, held = store.RateLimitState("seed")
						loaded = store.LoadRateLimit(ctx, "seed") != nil
					}
					if held {
						require.Equal(t, st.Usage, st.LastDBUsage, "a capture saw seeded usage without its baseline")
					}
					if finished {
						require.True(t, held && loaded, "the seed published nothing")
					}
				}
			}
		})
	}
}

// TestSeedConfigPreservesUsageAndReset keeps imported usage paired with its
// baseline across first publication, concurrent resets, and later config reloads.
func TestSeedConfigPreservesUsageAndReset(t *testing.T) {
	ctx := context.Background()
	boundary := time.Now().Truncate(time.Hour)
	next := boundary.Add(time.Hour)
	for range 200 {
		store := newStandaloneStore(t)
		budget := &configstoreTables.TableBudget{ID: "budget", CurrentUsage: 100, LastReset: boundary}
		rate := &configstoreTables.TableRateLimit{ID: "rate", TokenCurrentUsage: 200, RequestCurrentUsage: 20, TokenLastReset: boundary, RequestLastReset: boundary}
		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		go func() {
			defer wg.Done()
			<-start
			reloadBudget(t, store, budget.ID, budget)
			reloadRateLimit(t, store, rate.ID, rate)
		}()
		go func() {
			defer wg.Done()
			<-start
			store.ResetBudgetAt(ctx, budget.ID, next)
			store.ResetRateLimitAt(ctx, rate.ID, &next, &next)
		}()
		close(start)
		wg.Wait()
		b := store.LoadBudget(ctx, budget.ID)
		r := store.LoadRateLimit(ctx, rate.ID)
		requireBaselinesMatch(t, store, budget.ID, b, rate.ID, r)
		require.NoError(t, store.BumpBudgetUsageBy(ctx, budget.ID, 7))
		require.NoError(t, store.BumpRateLimitUsageBy(ctx, rate.ID, 11, 3))
		reloadBudget(t, store, budget.ID, budget)
		reloadRateLimit(t, store, rate.ID, rate)
		require.Equal(t, b.CurrentUsage+7, store.LoadBudget(ctx, budget.ID).CurrentUsage)
		require.Equal(t, b.LastReset, store.LoadBudget(ctx, budget.ID).LastReset)
		require.Equal(t, r.TokenCurrentUsage+11, store.LoadRateLimit(ctx, rate.ID).TokenCurrentUsage)
		require.Equal(t, r.RequestCurrentUsage+3, store.LoadRateLimit(ctx, rate.ID).RequestCurrentUsage)
		require.Equal(t, r.TokenLastReset, store.LoadRateLimit(ctx, rate.ID).TokenLastReset)
		require.Equal(t, r.RequestLastReset, store.LoadRateLimit(ctx, rate.ID).RequestLastReset)
		requireBaselinesMatch(t, store, budget.ID, b, rate.ID, r)
	}
}

// requireBaselinesMatch requires the budget's and rate limit's baselines to equal the usage of b
// and r.
func requireBaselinesMatch(t *testing.T, store *LocalGovernanceStore, budgetID string, b *configstoreTables.TableBudget, rateLimitID string, r *configstoreTables.TableRateLimit) {
	t.Helper()
	budgetLastDB, ok := budgetBaseline(store, budgetID)
	require.True(t, ok)
	require.Equal(t, b.CurrentUsage, budgetLastDB)
	tokenLastDB, requestLastDB, ok := rateLimitBaselines(store, rateLimitID)
	require.True(t, ok)
	require.Equal(t, r.TokenCurrentUsage, tokenLastDB)
	require.Equal(t, r.RequestCurrentUsage, requestLastDB)
}

// TestDeleteConfigDropsUsageWithItsBaseline verifies counter removal and baseline cleanup are one
// step: a capture reading while a limit is deleted sees the seeded counter with its baseline, or
// neither, and nothing of the limit is left once the delete returns.
func TestDeleteConfigDropsUsageWithItsBaseline(t *testing.T) {
	for _, dimension := range []string{"budget", "rate limit"} {
		t.Run(dimension, func(t *testing.T) {
			ctx := context.Background()
			for range 100 {
				store := newStandaloneStore(t)
				reloadBudget(t, store, "seed", &configstoreTables.TableBudget{ID: "seed", CurrentUsage: 100})
				reloadRateLimit(t, store, "seed", &configstoreTables.TableRateLimit{ID: "seed", TokenCurrentUsage: 100, RequestCurrentUsage: 20})
				done := make(chan struct{})
				go func() {
					defer close(done)
					if dimension == "budget" {
						dropLimit(t, store, livestore.KindBudget, "seed")
					} else {
						dropLimit(t, store, livestore.KindRateLimit, "seed")
					}
				}()
				for finished := false; !finished; {
					select {
					case <-done:
						finished = true
					default:
					}
					var st LimitState
					var held, loaded bool
					if dimension == "budget" {
						st, held = store.BudgetState("seed")
						loaded = store.LoadBudget(ctx, "seed") != nil
					} else {
						st, held = store.RateLimitState("seed")
						loaded = store.LoadRateLimit(ctx, "seed") != nil
					}
					if held {
						require.Equal(t, st.Usage, st.LastDBUsage, "a capture saw the counter without its baseline")
					}
					if finished {
						require.False(t, held || loaded, "the delete left part of the limit behind")
					}
				}
			}
		})
	}
}

// TestSeedConfigConcurrentDeletion races a refresh of a limit's row against its removal and leaves
// either a complete seeded pair or no record, counter or baseline, whichever finishes last.
func TestSeedConfigConcurrentDeletion(t *testing.T) {
	ctx := context.Background()
	for range 200 {
		store := newStandaloneStore(t)
		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		go func() {
			defer wg.Done()
			<-start
			reloadBudget(t, store, "budget", &configstoreTables.TableBudget{ID: "budget", CurrentUsage: 100})
			reloadRateLimit(t, store, "rate", &configstoreTables.TableRateLimit{ID: "rate", TokenCurrentUsage: 200, RequestCurrentUsage: 20})
		}()
		go func() {
			defer wg.Done()
			<-start
			dropLimit(t, store, livestore.KindBudget, "budget")
			dropLimit(t, store, livestore.KindRateLimit, "rate")
		}()
		close(start)
		wg.Wait()
		lastDB, held := budgetBaseline(store, "budget")
		_, recorded := store.live.Collections.Budgets.Get("budget")
		if budget := store.LoadBudget(ctx, "budget"); budget != nil {
			require.True(t, held)
			require.Equal(t, budget.CurrentUsage, lastDB)
		} else {
			require.False(t, held, "a deleted budget kept its baseline")
			require.False(t, recorded, "a deleted budget kept its record")
		}
		tokenLastDB, requestLastDB, held := rateLimitBaselines(store, "rate")
		_, recorded = store.live.Collections.RateLimits.Get("rate")
		if rate := store.LoadRateLimit(ctx, "rate"); rate != nil {
			require.True(t, held)
			require.Equal(t, rate.TokenCurrentUsage, tokenLastDB)
			require.Equal(t, rate.RequestCurrentUsage, requestLastDB)
		} else {
			require.False(t, held, "a deleted rate limit kept its baselines")
			require.False(t, recorded, "a deleted rate limit kept its record")
		}
	}
}

// TestOwnerCacheMissRefresh checks that loading an owner preserves the accounting of the limits
// the store already holds and seeds genuinely new limits from the rows. The live store re-reads an
// owner's budgets and rate limit with it and hands each to the store as a limit row, whichever kind
// of owner holds it.
func TestOwnerCacheMissRefresh(t *testing.T) {
	ctx := context.Background()
	for _, owner := range []string{"team", "customer", "virtual key", "virtual key provider"} {
		t.Run(owner, func(t *testing.T) {
			for _, mode := range []string{"existing limits", "new limits"} {
				t.Run(mode, func(t *testing.T) {
					h := newLiveHarness(t, &configstore.GovernanceConfig{}, nil)
					store := h.mustGovernanceStore(NewMockLogger(), nil)
					reset := time.Unix(100, 0)
					budget := configstoreTables.TableBudget{ID: "budget", MaxLimit: 100, CurrentUsage: 1, LastReset: reset}
					rate := *buildRateLimit("rate", 100, 100)
					rate.TokenCurrentUsage = 2
					rate.RequestCurrentUsage = 3
					rate.TokenLastReset = reset
					rate.RequestLastReset = reset
					wantBudget, wantRate := budget, rate
					if mode == "existing limits" {
						liveBudget, liveRate := budget, rate
						liveBudget.CurrentUsage = 20
						liveBudget.LastReset = reset.Add(time.Hour)
						liveBudget.UsageResetEpoch = 11
						liveRate.TokenCurrentUsage = 30
						liveRate.RequestCurrentUsage = 40
						liveRate.TokenLastReset = reset.Add(time.Hour)
						liveRate.RequestLastReset = reset.Add(2 * time.Hour)
						liveRate.TokenUsageResetEpoch = 12
						liveRate.RequestUsageResetEpoch = 13
						wantBudget, wantRate = liveBudget, liveRate
						liveBudget.MaxLimit = 10
						liveRate.TokenMaxLimit = nil
						liveRate.RequestMaxLimit = nil
						putBudget(t, store, budget.ID, &liveBudget)
						putRateLimit(t, store, rate.ID, &liveRate)
					}
					budgets := []configstoreTables.TableBudget{budget}
					switch owner {
					case "team":
						h.putTeam(&configstoreTables.TableTeam{ID: "team", Name: "team", Budgets: budgets, RateLimit: &rate})
					case "customer":
						h.putCustomer(&configstoreTables.TableCustomer{ID: "customer", Name: "customer", Budgets: budgets, RateLimit: &rate})
					case "virtual key":
						h.putVirtualKey(&configstoreTables.TableVirtualKey{ID: "key", Name: "key", Value: *schemas.NewSecretVar("sk-bf-test"), Budgets: budgets, RateLimit: &rate})
					case "virtual key provider":
						h.putVirtualKey(&configstoreTables.TableVirtualKey{
							ID: "key", Name: "key", Value: *schemas.NewSecretVar("sk-bf-test"),
							ProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{{Provider: "openai", Budgets: budgets, RateLimit: &rate}},
						})
					}
					requireLimitAccounting(t, &wantBudget, &wantRate, store.LoadBudget(ctx, budget.ID), store.LoadRateLimit(ctx, rate.ID))
				})
			}
		})
	}
}

// requireLimitAccounting checks that a budget and a rate limit loaded from a store carry the
// settings and accounting of want: the limits, and each counter's usage, last reset and reset
// evidence. Columns the database fills in are not compared.
func requireLimitAccounting(t *testing.T, wantBudget *configstoreTables.TableBudget, wantRate *configstoreTables.TableRateLimit, gotBudget *configstoreTables.TableBudget, gotRate *configstoreTables.TableRateLimit) {
	t.Helper()
	if wantBudget != nil {
		require.NotNil(t, gotBudget)
		require.Equal(t, wantBudget.MaxLimit, gotBudget.MaxLimit)
		require.Equal(t, wantBudget.CurrentUsage, gotBudget.CurrentUsage)
		require.True(t, wantBudget.LastReset.Equal(gotBudget.LastReset), "budget last reset: want %v, got %v", wantBudget.LastReset, gotBudget.LastReset)
		require.Equal(t, wantBudget.UsageResetEpoch, gotBudget.UsageResetEpoch)
	}
	if wantRate != nil {
		require.NotNil(t, gotRate)
		require.Equal(t, wantRate.TokenMaxLimit, gotRate.TokenMaxLimit)
		require.Equal(t, wantRate.RequestMaxLimit, gotRate.RequestMaxLimit)
		require.Equal(t, wantRate.TokenCurrentUsage, gotRate.TokenCurrentUsage)
		require.Equal(t, wantRate.RequestCurrentUsage, gotRate.RequestCurrentUsage)
		require.True(t, wantRate.TokenLastReset.Equal(gotRate.TokenLastReset), "token last reset: want %v, got %v", wantRate.TokenLastReset, gotRate.TokenLastReset)
		require.True(t, wantRate.RequestLastReset.Equal(gotRate.RequestLastReset), "request last reset: want %v, got %v", wantRate.RequestLastReset, gotRate.RequestLastReset)
		require.Equal(t, wantRate.TokenUsageResetEpoch, gotRate.TokenUsageResetEpoch)
		require.Equal(t, wantRate.RequestUsageResetEpoch, gotRate.RequestUsageResetEpoch)
	}
}
