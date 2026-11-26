package governance

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// limitStateFixture is a store over a live store holding one key with budget "held-budget" and
// rate limit "held-rate", with usage on both rows.
type limitStateFixture struct {
	h         *liveHarness
	store     *LocalGovernanceStore
	vk        *configstoreTables.TableVirtualKey
	budget    *configstoreTables.TableBudget
	rateLimit *configstoreTables.TableRateLimit
	lastReset time.Time
}

// newLimitStateFixture writes the key, its budget and its rate limit and builds a store over them.
func newLimitStateFixture(t *testing.T) *limitStateFixture {
	t.Helper()
	lastReset := time.Now().UTC().Truncate(time.Second)
	budget := buildBudgetWithUsage("held-budget", 100, 42.5, "24h")
	budget.LastReset = lastReset
	budget.CreatedAt = lastReset
	rateLimit := buildRateLimitWithUsage("held-rate", 1_000_000, 300, 1_000, 7)
	hour := "1h"
	rateLimit.TokenResetDuration, rateLimit.RequestResetDuration = &hour, &hour
	rateLimit.TokenLastReset, rateLimit.RequestLastReset = lastReset, lastReset.Add(-time.Minute)
	rateLimit.CreatedAt = lastReset
	vk := buildVirtualKeyWithBudget("vk-held", "sk-bf-held", "held", budget)
	vk.RateLimit = rateLimit
	vk.RateLimitID = &rateLimit.ID
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		RateLimits:  []configstoreTables.TableRateLimit{*rateLimit},
	}, nil)
	return &limitStateFixture{h: h, store: h.mustGovernanceStore(NewMockLogger(), nil), vk: vk, budget: budget, rateLimit: rateLimit, lastReset: lastReset}
}

// TestLimitStateSeedsFromRecord pins that a limit the live store holds starts from the usage its
// row carries, counted from that row: only usage added after the read is unsaved.
func TestLimitStateSeedsFromRecord(t *testing.T) {
	f := newLimitStateFixture(t)

	st, ok := f.store.BudgetState(f.budget.ID)
	require.True(t, ok, "a budget the live store holds is seeded")
	assert.Equal(t, 42.5, st.Usage[BudgetSlot].Dollars())
	assert.Equal(t, 42.5, st.LastDBUsage[BudgetSlot].Dollars(), "usage read from the row counts from the row")
	assert.True(t, st.lastResetAt(BudgetSlot).Equal(f.lastReset))

	rst, ok := f.store.RateLimitState(f.rateLimit.ID)
	require.True(t, ok, "a rate limit the live store holds is seeded")
	assert.Equal(t, [2]Count{WholeCount(300), WholeCount(7)}, rst.Usage)
	assert.Equal(t, [2]Count{WholeCount(300), WholeCount(7)}, rst.LastDBUsage)
	assert.True(t, rst.lastResetAt(TokenSlot).Equal(f.lastReset))
	assert.True(t, rst.lastResetAt(RequestSlot).Equal(f.lastReset.Add(-time.Minute)))

	loaded := f.store.LoadRateLimit(context.Background(), f.rateLimit.ID)
	require.NotNil(t, loaded)
	assert.Equal(t, int64(300), loaded.TokenCurrentUsage)
	assert.Equal(t, int64(7), loaded.RequestCurrentUsage)
}

// TestBudgetOverrideIsState pins that a budget's override is held with its usage, not its
// settings: it is seeded from the row, every read of the row replaces it, an update of the state
// replaces it as UpdateBudgetState does, and a read derives its remaining cycles from
// the budget's last reset.
func TestBudgetOverrideIsState(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStore(t)
	anchor := time.Now().Truncate(time.Second).Add(-90 * time.Minute)
	budget := &configstoreTables.TableBudget{ID: "override", MaxLimit: 100, ResetDuration: "1h", LastReset: anchor, CreatedAt: anchor}
	require.NoError(t, budget.SetOverride(25, configstoreTables.BudgetOverrideModeCycles, 3))
	reloadBudget(t, store, budget.ID, budget)

	st, ok := store.BudgetState(budget.ID)
	require.True(t, ok)
	assert.Equal(t, 25.0, st.Override.Amount, "the override is seeded from the row")
	assert.Equal(t, 3, st.Override.CyclesTotal)
	loaded := store.LoadBudget(ctx, budget.ID)
	require.NotNil(t, loaded)
	assert.Equal(t, 125.0, loaded.EffectiveMaxLimit(), "a read carries the override")

	cleared := *budget
	cleared.ClearOverride()
	reloadBudget(t, store, budget.ID, &cleared)
	assert.False(t, store.LoadBudget(ctx, budget.ID).HasActiveOverride(), "a read of the row replaces it")

	require.True(t, store.UpdateBudgetState(budget.ID, func(s LimitState) (LimitState, bool) {
		s.Override = BudgetOverride{Amount: 40, Mode: configstoreTables.BudgetOverrideModeForever}
		return s, true
	}))
	loaded = store.LoadBudget(ctx, budget.ID)
	assert.Equal(t, 140.0, loaded.EffectiveMaxLimit(), "an update of the state replaces it, as accounting does")
	assert.Equal(t, 100.0, loaded.MaxLimit, "the setting itself is untouched")

	spent := *budget
	require.NoError(t, spent.SetOverride(25, configstoreTables.BudgetOverrideModeCycles, 1))
	reloadBudget(t, store, budget.ID, &spent)
	setUsage(store.budgetUsage, budget.ID, func(s *LimitState) { s.setLastReset(BudgetSlot, anchor.Add(2*time.Hour)) })
	assert.False(t, store.LoadBudget(ctx, budget.ID).HasActiveOverride(), "a read derives the cycles a later last reset spent")
}

// TestLimitStateSurvivesSettingsChange pins that changing a limit's settings through its record,
// whether a holder holds it or not, keeps its live usage and baseline: settings and usage are held
// apart.
func TestLimitStateSurvivesSettingsChange(t *testing.T) {
	ctx := context.Background()

	t.Run("record", func(t *testing.T) {
		f := newLimitStateFixture(t)
		require.NoError(t, f.store.BumpBudgetUsageBy(ctx, f.budget.ID, 10))
		require.NoError(t, f.store.BumpRateLimitUsageBy(ctx, f.rateLimit.ID, 50, 2))

		edited := *f.budget
		edited.MaxLimit = 250
		edited.CurrentUsage = 0
		f.h.putBudget(&edited)
		editedRate := *f.rateLimit
		limit := int64(5_000)
		editedRate.RequestMaxLimit = &limit
		editedRate.TokenCurrentUsage, editedRate.RequestCurrentUsage = 0, 0
		f.h.putRateLimit(&editedRate)

		budget := f.store.LoadBudget(ctx, f.budget.ID)
		require.NotNil(t, budget)
		assert.Equal(t, 250.0, budget.MaxLimit, "the record's new settings apply")
		assert.Equal(t, 52.5, budget.CurrentUsage, "a settings change keeps the live usage, not the row's")
		baseline, _ := budgetBaseline(f.store, f.budget.ID)
		assert.Equal(t, 42.5, baseline, "and its baseline")

		rateLimit := f.store.LoadRateLimit(ctx, f.rateLimit.ID)
		require.NotNil(t, rateLimit)
		assert.Equal(t, int64(5_000), *rateLimit.RequestMaxLimit)
		assert.Equal(t, int64(350), rateLimit.TokenCurrentUsage)
		assert.Equal(t, int64(9), rateLimit.RequestCurrentUsage)
	})

	t.Run("unheld row", func(t *testing.T) {
		store := newStandaloneStore(t)
		reloadBudget(t, store, "unheld", &configstoreTables.TableBudget{ID: "unheld", MaxLimit: 10, ResetDuration: "1d", CurrentUsage: 4, LastReset: time.Now()})
		require.NoError(t, store.BumpBudgetUsageBy(ctx, "unheld", 1))
		reloadBudget(t, store, "unheld", &configstoreTables.TableBudget{ID: "unheld", MaxLimit: 20, ResetDuration: "1d"})

		budget := store.LoadBudget(ctx, "unheld")
		require.NotNil(t, budget)
		assert.Equal(t, 20.0, budget.MaxLimit)
		assert.Equal(t, 5.0, budget.CurrentUsage)
		baseline, _ := budgetBaseline(store, "unheld")
		assert.Equal(t, 4.0, baseline)
	})
}

// TestLimitStateDroppedWithLimit pins that removing a limit's record drops its usage, whether a
// holder's removal takes it or it is removed on its own: the state tables follow the records.
func TestLimitStateDroppedWithLimit(t *testing.T) {
	ctx := context.Background()

	t.Run("record", func(t *testing.T) {
		f := newLimitStateFixture(t)
		f.h.deleteVirtualKey(f.vk.ID)

		_, ok := f.store.BudgetState(f.budget.ID)
		assert.False(t, ok, "a budget the live store removed keeps no usage")
		_, ok = f.store.RateLimitState(f.rateLimit.ID)
		assert.False(t, ok, "a rate limit the live store removed keeps no usage")
		assert.Nil(t, f.store.LoadBudget(ctx, f.budget.ID))
		assert.Nil(t, f.store.LoadRateLimit(ctx, f.rateLimit.ID))
	})

	t.Run("unheld row", func(t *testing.T) {
		store := newStandaloneStore(t)
		reloadBudget(t, store, "unheld", &configstoreTables.TableBudget{ID: "unheld", MaxLimit: 10, ResetDuration: "1d", CurrentUsage: 4})
		reloadRateLimit(t, store, "unheld", &configstoreTables.TableRateLimit{ID: "unheld", TokenCurrentUsage: 4})
		dropLimit(t, store, livestore.KindBudget, "unheld")
		dropLimit(t, store, livestore.KindRateLimit, "unheld")

		_, ok := store.BudgetState("unheld")
		assert.False(t, ok)
		_, ok = store.RateLimitState("unheld")
		assert.False(t, ok)
		assert.False(t, store.UpdateBudgetState("unheld", func(s LimitState) (LimitState, bool) { return s, true }),
			"a removed budget has no state to update")
	})
}

// TestLimitAppliesOnceItsStateIsSeeded pins that a newly published limit applies to checks and
// charges alike only once the store has seeded its usage state. In the moment between the record
// being published and the seed, a check skips the limit and a charge leaves no usage on it, the
// same for both; once the seed has run, the next check enforces it and the next charge counts. A
// subscriber registered before the store's runs in exactly that moment, as a request gathering the
// new record can. A limit whose record is gone is skipped again.
func TestLimitAppliesOnceItsStateIsSeeded(t *testing.T) {
	ctx := context.Background()
	h := newLiveHarness(t, nil, nil)
	limits := []schemas.Limit{{ID: "fresh"}}
	type window struct {
		ran, seeded bool
		decision    Decision
		errs        []error
	}
	var store *LocalGovernanceStore
	var budget, rateLimit window
	t.Cleanup(h.live.Collections.Budgets.Subscribe(func(ctx context.Context, changes []livestore.Change[registry.Budget]) {
		if budget.ran || len(changes) == 0 || changes[0].New == nil {
			return
		}
		budget.ran = true
		_, budget.seeded = store.BudgetState("fresh")
		var err error
		budget.decision, err = store.CheckBudgets(ctx, limits, nil)
		budget.errs = append(budget.errs, err, store.BumpBudgetUsage(ctx, "fresh", 2), store.BumpBudgetUsageBy(ctx, "fresh", 1))
	}))
	t.Cleanup(h.live.Collections.RateLimits.Subscribe(func(ctx context.Context, changes []livestore.Change[registry.RateLimit]) {
		if rateLimit.ran || len(changes) == 0 || changes[0].New == nil {
			return
		}
		rateLimit.ran = true
		_, rateLimit.seeded = store.RateLimitState("fresh")
		var err error
		rateLimit.decision, err = store.CheckRateLimits(ctx, limits, nil, nil)
		rateLimit.errs = append(rateLimit.errs, err, store.BumpRateLimitUsage(ctx, "fresh", 3, true, true), store.BumpRateLimitUsageBy(ctx, "fresh", 2, 1))
	}))
	var err error
	store, err = NewLocalGovernanceStore(ctx, NewMockLogger(), nil, h.live, nil)
	require.NoError(t, err)
	t.Cleanup(store.Close)

	// Both limits are already exhausted by the usage their rows carry.
	now := time.Now().UTC().Truncate(time.Second)
	reloadBudget(t, store, "fresh", &configstoreTables.TableBudget{ID: "fresh", MaxLimit: 10, ResetDuration: "24h", CurrentUsage: 10, LastReset: now, CreatedAt: now})
	tokenMax, hour := int64(5), "1h"
	reloadRateLimit(t, store, "fresh", &configstoreTables.TableRateLimit{ID: "fresh", TokenMaxLimit: &tokenMax, TokenResetDuration: &hour, TokenCurrentUsage: 5, TokenLastReset: now, CreatedAt: now})

	require.True(t, budget.ran)
	require.False(t, budget.seeded, "the check and charges ran before the store seeded the budget's state")
	assert.Equal(t, DecisionAllow, budget.decision, "a check skips a budget whose state is not seeded yet")
	assert.Equal(t, []error{nil, nil, nil}, budget.errs)
	st, ok := store.BudgetState("fresh")
	require.True(t, ok, "the store seeds the budget's state")
	assert.Equal(t, 10.0, st.Usage[BudgetSlot].Dollars(), "a charge before the seed leaves no usage on the budget")

	require.True(t, rateLimit.ran)
	require.False(t, rateLimit.seeded, "the check and charges ran before the store seeded the rate limit's state")
	assert.Equal(t, DecisionAllow, rateLimit.decision, "a check skips a rate limit whose state is not seeded yet")
	assert.Equal(t, []error{nil, nil, nil}, rateLimit.errs)
	rst, ok := store.RateLimitState("fresh")
	require.True(t, ok, "the store seeds the rate limit's state")
	assert.Equal(t, [2]Count{WholeCount(5), WholeCount(0)}, rst.Usage, "a charge before the seed leaves no usage on the rate limit")

	decision, err := store.CheckBudgets(ctx, limits, nil)
	assert.Equal(t, DecisionBudgetExceeded, decision, "once seeded, the budget is enforced")
	assert.Error(t, err)
	decision, err = store.CheckRateLimits(ctx, limits, nil, nil)
	assert.Equal(t, DecisionTokenLimited, decision, "once seeded, the rate limit is enforced")
	assert.Error(t, err)
	require.NoError(t, store.BumpBudgetUsage(ctx, "fresh", 2))
	require.NoError(t, store.BumpRateLimitUsage(ctx, "fresh", 3, true, true))
	st, _ = store.BudgetState("fresh")
	assert.Equal(t, 12.0, st.Usage[BudgetSlot].Dollars(), "once seeded, a charge counts")
	rst, _ = store.RateLimitState("fresh")
	assert.Equal(t, [2]Count{WholeCount(8), WholeCount(1)}, rst.Usage, "once seeded, a charge counts")

	dropLimit(t, store, livestore.KindBudget, "fresh")
	dropLimit(t, store, livestore.KindRateLimit, "fresh")
	decision, err = store.CheckBudgets(ctx, limits, nil)
	assert.Equal(t, DecisionAllow, decision, "a budget whose record is gone is skipped")
	assert.NoError(t, err)
	decision, err = store.CheckRateLimits(ctx, limits, nil, nil)
	assert.Equal(t, DecisionAllow, decision, "a rate limit whose record is gone is skipped")
	assert.NoError(t, err)
}

// TestLimitStateConcurrentChargesAndBaselines pins that charges, folded deltas and baseline writes
// racing on one limit all land: each is a compare-and-swap of the whole state, so none overwrites
// another. Run under the race detector.
func TestLimitStateConcurrentChargesAndBaselines(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStore(t)
	putBudget(t, store, "budget", buildBudget("budget", 1e12, "24h"))
	putRateLimit(t, store, "rate", buildRateLimit("rate", 1e12, 1e12))

	const chargers, perCharger, baselineWrites = 32, 50, 500
	var wg sync.WaitGroup
	for range chargers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perCharger {
				assert.NoError(t, store.BumpBudgetUsage(ctx, "budget", 1))
				assert.NoError(t, store.BumpBudgetUsageBy(ctx, "budget", 0.5))
				assert.NoError(t, store.BumpRateLimitUsage(ctx, "rate", 3, true, true))
				assert.NoError(t, store.BumpRateLimitUsageBy(ctx, "rate", 2, 1))
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= baselineWrites; i++ {
			assert.True(t, store.UpdateBudgetState("budget", func(s LimitState) (LimitState, bool) {
				s.LastDBUsage[BudgetSlot] = DollarCount(float64(i))
				return s, true
			}))
			assert.True(t, store.UpdateRateLimitState("rate", func(s LimitState) (LimitState, bool) {
				s.LastDBUsage = [2]Count{WholeCount(int64(i)), WholeCount(int64(2 * i))}
				return s, true
			}))
		}
	}()
	wg.Wait()

	st, ok := store.BudgetState("budget")
	require.True(t, ok)
	assert.Equal(t, float64(chargers*perCharger)*1.5, st.Usage[BudgetSlot].Dollars(), "a charge was lost to a racing write")
	assert.Equal(t, float64(baselineWrites), st.LastDBUsage[BudgetSlot].Dollars(), "a baseline write was lost to a racing charge")

	rst, ok := store.RateLimitState("rate")
	require.True(t, ok)
	assert.Equal(t, [2]Count{WholeCount(chargers * perCharger * 5), WholeCount(chargers * perCharger * 2)}, rst.Usage)
	assert.Equal(t, [2]Count{WholeCount(baselineWrites), WholeCount(2 * baselineWrites)}, rst.LastDBUsage)
	rateLimit := store.LoadRateLimit(ctx, "rate")
	require.NotNil(t, rateLimit)
	assert.Equal(t, int64(chargers*perCharger*5), rateLimit.TokenCurrentUsage)
	assert.Equal(t, int64(chargers*perCharger*2), rateLimit.RequestCurrentUsage)
}

// TestDumpSkipsRowsReadBeforeAReset pins how a dump decides a row it read is stale: a reset, a
// delete, or a delete and a fresh seed of the same budget since the read each drop the row, while
// ordinary charges keep it.
func TestDumpSkipsRowsReadBeforeAReset(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStore(t)
	now := time.Now()
	seed := func(id string) {
		reloadBudget(t, store, id, &configstoreTables.TableBudget{ID: id, MaxLimit: 100, ResetDuration: "24h", CurrentUsage: 10, LastReset: now, CreatedAt: now})
	}
	for _, id := range []string{"untouched", "charged", "reset", "deleted", "reseeded"} {
		seed(id)
	}

	rows, gens := store.snapshotBudgetRows(nil)
	require.Len(t, rows, 5)

	require.NoError(t, store.BumpBudgetUsage(ctx, "charged", 2))
	_, ok := store.ResetBudgetUsageInMemory(ctx, "reset")
	require.True(t, ok)
	dropLimit(t, store, livestore.KindBudget, "deleted")
	dropLimit(t, store, livestore.KindBudget, "reseeded")
	seed("reseeded")

	var kept []string
	for _, row := range store.keepUnresetRows(rows, gens) {
		kept = append(kept, row.ID)
	}
	assert.ElementsMatch(t, []string{"untouched", "charged"}, kept,
		"rows read before a reset, a delete or a re-seed must not be written back")

	// A reset moves the generation once; the rows read after it are current again.
	rows, gens = store.snapshotBudgetRows(nil)
	assert.Len(t, store.keepUnresetRows(rows, gens), 4)
}

// TestCloseDropsLimitTables pins that Close releases every table the store registered, so a
// reloaded store can register them again and the old store's usage is gone with it.
func TestCloseDropsLimitTables(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStore(t)
	reloadBudget(t, store, "budget", &configstoreTables.TableBudget{ID: "budget", MaxLimit: 10, ResetDuration: "1d", CurrentUsage: 1})
	reloadRateLimit(t, store, "rate", &configstoreTables.TableRateLimit{ID: "rate", TokenCurrentUsage: 1})
	names := []string{store.modelConfigIndex.Name(), store.modelConfigKeyOf.Name(), store.budgetUsage.Name(), store.rateLimitUsage.Name()}

	store.Close()
	store.Close()

	for _, name := range names {
		_, err := store.live.NewTable[string, int](context.Background(), name)
		assert.NoError(t, err, "table %s is still registered after Close", name)
	}
	_, ok := store.BudgetState("budget")
	assert.False(t, ok)
	_, ok = store.RateLimitState("rate")
	assert.False(t, ok)
	assert.Nil(t, store.LoadBudget(ctx, "budget"))
}

// TestLimitStateEncoding pins the encodings LimitState relies on: the zero time as 0 and back, an
// instant through Unix nanoseconds back in the location it was given in (the local zone when none
// was kept), and rate-limit counts exact past 2^53, where a float64 would round them.
func TestLimitStateEncoding(t *testing.T) {
	assert.Zero(t, unixNanos(time.Time{}))
	assert.True(t, timeOfNanos(0, nil).IsZero())
	instant := time.Date(2026, time.November, 16, 10, 30, 0, 123456789, time.FixedZone("x", 5*3600))
	var st LimitState
	st.setLastReset(TokenSlot, instant)
	assert.Equal(t, instant, st.lastResetAt(TokenSlot), "a window start reads back in its own location")
	boundary := time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC)
	st.setLastReset(RequestSlot, boundary)
	assert.Equal(t, boundary, st.lastResetAt(RequestSlot), "a UTC calendar boundary stays UTC")
	assert.Equal(t, time.Local, timeOfNanos(unixNanos(instant), nil).Location())
	assert.Equal(t, int64(1)<<53+1, WholeCount(int64(1)<<53+1).Whole())
	assert.Equal(t, 0.0, Count{}.Dollars())
	assert.Zero(t, Count{}.Whole())
	assert.Equal(t, 12.75, DollarCount(12.75).Dollars())

	ctx := context.Background()
	store := newStandaloneStore(t)
	const big = int64(1)<<53 + 1
	reloadRateLimit(t, store, "big", &configstoreTables.TableRateLimit{ID: "big", TokenCurrentUsage: big, RequestCurrentUsage: big - 2})
	require.NoError(t, store.BumpRateLimitUsageBy(ctx, "big", 2, 1))
	rateLimit := store.LoadRateLimit(ctx, "big")
	require.NotNil(t, rateLimit)
	assert.Equal(t, big+2, rateLimit.TokenCurrentUsage)
	assert.Equal(t, big-1, rateLimit.RequestCurrentUsage)
}

// TestRangeLimitStatesHandOverSharedState pins that the ranges visit exactly the limits
// GetGovernanceUsageData lists, hand over the stored state itself rather than a copy, and allocate
// nothing per limit, where GetGovernanceUsageData assembles a row for every limit.
func TestRangeLimitStatesHandOverSharedState(t *testing.T) {
	ctx := context.Background()
	f := newLimitStateFixture(t)
	for i := 0; i < 600; i++ {
		f.h.putBudget(buildBudgetWithUsage(fmt.Sprintf("extra-%03d", i), 100, 1, "24h"))
	}
	usage := f.store.GetGovernanceUsageData(ctx)

	var budgetIDs []string
	f.store.RangeBudgetStates(func(id string, settings *configstoreTables.TableBudget, st *LimitState) bool {
		stored, ok := f.store.budgetUsage.Get(id)
		require.True(t, ok)
		require.Same(t, stored, st, "the stored state, not a copy")
		require.Equal(t, id, settings.ID)
		require.Equal(t, *usage.Budgets[id], BudgetView(settings, st), "the view a range builds equals the row the usage data assembles")
		budgetIDs = append(budgetIDs, id)
		return true
	})
	wantBudgets := make([]string, 0, len(usage.Budgets))
	for id := range usage.Budgets {
		wantBudgets = append(wantBudgets, id)
	}
	assert.ElementsMatch(t, wantBudgets, budgetIDs)
	require.Greater(t, len(budgetIDs), 600)

	var rateLimitIDs []string
	f.store.RangeRateLimitStates(func(id string, settings *configstoreTables.TableRateLimit, st *LimitState) bool {
		stored, ok := f.store.rateLimitUsage.Get(id)
		require.True(t, ok)
		require.Same(t, stored, st, "the stored state, not a copy")
		rateLimitIDs = append(rateLimitIDs, id)
		return true
	})
	wantRateLimits := make([]string, 0, len(usage.RateLimits))
	for id := range usage.RateLimits {
		wantRateLimits = append(wantRateLimits, id)
	}
	assert.ElementsMatch(t, wantRateLimits, rateLimitIDs)

	// Range copies each table shard's entries under its lock, two slices per non-empty shard, so its
	// allocations stop growing once every shard holds an entry; a row per limit is what the usage
	// data costs.
	ranged := testing.AllocsPerRun(10, func() {
		f.store.RangeBudgetStates(func(string, *configstoreTables.TableBudget, *LimitState) bool { return true })
	})
	copied := testing.AllocsPerRun(10, func() { f.store.GetGovernanceUsageData(ctx) })
	assert.Less(t, ranged, float64(len(budgetIDs))/4, "a range allocates per table shard, not per limit")
	assert.Greater(t, copied, float64(len(budgetIDs)), "the usage data assembles a row per limit")
}
