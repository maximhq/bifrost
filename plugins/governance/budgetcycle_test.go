package governance

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cycleTestWindow is the reset duration every budget-cycle test uses. One
// minute is short enough to walk many windows with a simulated clock and long
// enough that the jitter offsets below stay well inside a single window.
const cycleTestWindow = time.Minute

// cycleTestDuration is cycleTestWindow rendered the way ResetDuration expects.
const cycleTestDuration = "1m"

// cycleTestAnchor is the fixed creation instant every budget-cycle test anchors
// on. It is a lattice point for every duration these tests use, so expected
// boundaries are exact multiples of the window away from it.
func cycleTestAnchor() time.Time {
	return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
}

// newCycleTestBudget builds a budget whose creation instant and last reset both
// sit on cycleTestAnchor, which is the state a freshly created budget has in the
// database. Tests drive it forward with a simulated clock rather than sleeping.
func newCycleTestBudget(id string, maxLimit, currentUsage float64) *configstoreTables.TableBudget {
	anchor := cycleTestAnchor()
	budget := buildBudgetWithUsage(id, maxLimit, currentUsage, cycleTestDuration)
	budget.CreatedAt = anchor
	budget.UpdatedAt = anchor
	budget.LastReset = anchor
	return budget
}

// sweepBudgetAt runs one reset sweep for a single budget against a simulated
// clock, mirroring what the 10s ticker does on a real node. Returns the reset
// snapshot, or nil when the budget was not due at that instant.
func sweepBudgetAt(t *testing.T, store *LocalGovernanceStore, budgetID string, now time.Time) *configstoreTables.TableBudget {
	t.Helper()
	ctx := context.Background()
	current := store.LoadBudget(ctx, budgetID)
	if current == nil {
		t.Fatalf("budget %s missing from store", budgetID)
	}
	return store.resetExpiredBudgetFromSnapshot(ctx, current, now)
}

// TestBudgetResetTargetIsDeterministicAcrossNodes proves two nodes holding the
// same budget row converge on the same reset boundary even though their reset
// tickers fire at different phases.
//
// This is the core cluster defect: the rolling branch of budgetResetTarget
// writes time.Now(), so LastReset becomes a function of whichever instant that
// node's ticker happened to fire. Each node then measures its next window from
// its own stamp, so the phase error compounds every cycle and no two nodes ever
// agree on when a window closed. Overrides sit on top of that disagreement,
// which is why a cycles override grants a different number of windows per node.
func TestBudgetResetTargetIsDeterministicAcrossNodes(t *testing.T) {
	anchor := cycleTestAnchor()
	// Two independent nodes, identical starting row, tickers ~7s out of phase.
	nodeA := newStandaloneStore(t)
	nodeB := newStandaloneStore(t)
	nodeA.budgets.Store("shared-budget", newCycleTestBudget("shared-budget", 100, 10))
	nodeB.budgets.Store("shared-budget", newCycleTestBudget("shared-budget", 100, 10))

	// Node A's ticker lands 1s into each window, node B's lands 8s in.
	phaseA := []time.Duration{61 * time.Second, 125 * time.Second, 181 * time.Second}
	phaseB := []time.Duration{68 * time.Second, 133 * time.Second, 188 * time.Second}
	for i := range phaseA {
		sweepBudgetAt(t, nodeA, "shared-budget", anchor.Add(phaseA[i]))
		sweepBudgetAt(t, nodeB, "shared-budget", anchor.Add(phaseB[i]))
	}

	ctx := context.Background()
	gotA := nodeA.LoadBudget(ctx, "shared-budget")
	gotB := nodeB.LoadBudget(ctx, "shared-budget")
	require.NotNil(t, gotA)
	require.NotNil(t, gotB)

	assert.True(t, gotA.LastReset.Equal(gotB.LastReset),
		"nodes disagree on the window boundary: node A at %s, node B at %s (%s apart)",
		gotA.LastReset.UTC(), gotB.LastReset.UTC(), gotB.LastReset.Sub(gotA.LastReset))

	// The boundary must also be a lattice point, not a ticker timestamp, so the
	// cadence stays exactly one window instead of window plus ticker jitter.
	wantBoundary := anchor.Add(3 * cycleTestWindow)
	assert.True(t, gotA.LastReset.Equal(wantBoundary),
		"boundary is not lattice aligned: got %s, want %s", gotA.LastReset.UTC(), wantBoundary.UTC())
}

// TestBudgetResetCadenceIsExactlyOneWindow proves consecutive boundaries differ
// by exactly the reset duration. Today each boundary is the ticker instant, so
// successive boundaries differ by the duration plus that tick's jitter, and the
// budget window silently runs long every single cycle.
func TestBudgetResetCadenceIsExactlyOneWindow(t *testing.T) {
	anchor := cycleTestAnchor()
	store := newStandaloneStore(t)
	store.budgets.Store("cadence-budget", newCycleTestBudget("cadence-budget", 100, 10))

	// Deliberately ragged tick offsets inside each window, as a real 10s ticker
	// produces.
	offsets := []time.Duration{
		63 * time.Second,
		127 * time.Second,
		188 * time.Second,
		247 * time.Second,
		309 * time.Second,
	}
	var boundaries []time.Time
	for _, offset := range offsets {
		if reset := sweepBudgetAt(t, store, "cadence-budget", anchor.Add(offset)); reset != nil {
			boundaries = append(boundaries, reset.LastReset)
		}
	}

	require.GreaterOrEqual(t, len(boundaries), 2, "expected at least two resets, got %d", len(boundaries))
	for i := 1; i < len(boundaries); i++ {
		gap := boundaries[i].Sub(boundaries[i-1])
		assert.Equal(t, cycleTestWindow, gap,
			"boundary %d to %d gap is %s, want exactly %s (boundaries: %v)",
			i-1, i, gap, cycleTestWindow, boundaries)
	}
}

// TestUpsertBudgetConfigDoesNotResurrectOverrideCycles proves a config reload
// cannot hand back a cycle the reset path already spent.
//
// UpsertBudgetConfig preserves CurrentUsage and LastReset from the live snapshot
// but takes the override fields wholesale from the incoming config. Every reload
// path funnels through it, so a reload carrying the database's pre-reset row
// restores the old cycle count while LastReset stays advanced. That window's
// cycle can never be spent again, so the override outlives the grant it was
// given, and it does so by a different amount on every node.
func TestUpsertBudgetConfigDoesNotResurrectOverrideCycles(t *testing.T) {
	ctx := context.Background()
	anchor := cycleTestAnchor()
	store := newStandaloneStore(t)

	budget := newCycleTestBudget("resurrect-budget", 100, 75)
	require.NoError(t, budget.SetOverride(25, configstoreTables.BudgetOverrideModeCycles, 3))
	store.budgets.Store(budget.ID, budget)

	// The snapshot a reload would carry: read before the reset, so it still
	// holds the pre-reset cycle count. This is exactly what a database row looks
	// like between the in-memory reset and the leader's next dump.
	stale := store.LoadBudget(ctx, budget.ID)
	require.NotNil(t, stale)
	staleCycles := stale.OverrideCyclesRemaining
	require.Equal(t, 3, staleCycles)

	reset := sweepBudgetAt(t, store, budget.ID, anchor.Add(61*time.Second))
	require.NotNil(t, reset, "budget should have been due one window after the anchor")
	require.Equal(t, 2, reset.OverrideCyclesRemaining, "one window closed, so one cycle should be spent")

	// Now the reload lands, carrying the stale pre-reset override state.
	store.UpsertBudgetConfig(ctx, budget.ID, stale)

	got := store.LoadBudget(ctx, budget.ID)
	require.NotNil(t, got)
	assert.Equal(t, 2, got.OverrideCyclesRemaining,
		"config reload resurrected a spent override cycle: %d remaining after reload, want 2", got.OverrideCyclesRemaining)
}

// TestBudgetCycleSurvivesInterleavedConfigReload proves an N cycle override
// grants exactly N windows even when a config reload fires inside every window.
//
// This is the end to end shape of the reported bug. With the override count held
// as independently mutable state, a reload mid window replays the pre-reset
// count and the override never converges on its grant.
func TestBudgetCycleSurvivesInterleavedConfigReload(t *testing.T) {
	ctx := context.Background()
	anchor := cycleTestAnchor()
	store := newStandaloneStore(t)

	const grantedCycles = 3
	budget := newCycleTestBudget("reload-race-budget", 100, 0)
	require.NoError(t, budget.SetOverride(25, configstoreTables.BudgetOverrideModeCycles, grantedCycles))
	store.budgets.Store(budget.ID, budget)

	// The pristine row as the database holds it at grant time. A reload replays
	// this same config every time, which is what makes the race reproducible.
	pristine := *budget

	// Walk well past the grant so an override that fails to expire is caught.
	activeWindows := 0
	for window := 1; window <= grantedCycles+3; window++ {
		tick := anchor.Add(time.Duration(window)*cycleTestWindow + time.Second)
		if reset := sweepBudgetAt(t, store, budget.ID, tick); reset != nil && reset.HasActiveOverride() {
			activeWindows++
		}
		// A config reload lands mid window, carrying the pristine grant.
		reloaded := pristine
		store.UpsertBudgetConfig(ctx, budget.ID, &reloaded)
	}

	got := store.LoadBudget(ctx, budget.ID)
	require.NotNil(t, got)
	assert.False(t, got.HasActiveOverride(),
		"override outlived its %d cycle grant: mode=%q remaining=%d", grantedCycles, got.OverrideMode, got.OverrideCyclesRemaining)
	assert.Equal(t, grantedCycles-1, activeWindows,
		"override stayed active across %d post reset windows, want %d", activeWindows, grantedCycles-1)
}

// TestBudgetCycleGrantsExactlyNWindows pins the single node baseline: without
// any reload or cluster interference an N cycle override must span exactly N
// windows and then clear itself.
func TestBudgetCycleGrantsExactlyNWindows(t *testing.T) {
	ctx := context.Background()
	anchor := cycleTestAnchor()
	store := newStandaloneStore(t)

	const grantedCycles = 3
	budget := newCycleTestBudget("exact-cycles-budget", 100, 0)
	require.NoError(t, budget.SetOverride(25, configstoreTables.BudgetOverrideModeCycles, grantedCycles))
	store.budgets.Store(budget.ID, budget)

	for window := 1; window <= grantedCycles+2; window++ {
		sweepBudgetAt(t, store, budget.ID, anchor.Add(time.Duration(window)*cycleTestWindow+time.Second))
	}

	got := store.LoadBudget(ctx, budget.ID)
	require.NotNil(t, got)
	assert.False(t, got.HasActiveOverride(), "override should have cleared after %d windows", grantedCycles)
	assert.Equal(t, 100.0, got.EffectiveMaxLimit(), "limit should be back to the base max after expiry")
}

// TestBudgetCycleLongIdleSkipsToCurrentWindow proves a long outage collapses to
// a single reset that lands on the current window, and spends the whole override
// grant rather than replaying one cycle per elapsed window.
//
// Consuming one cycle per elapsed window would let an operator extend an
// override by stopping a node, and catching up 240 windows one at a time would
// either spin the sweep or leave the budget perpetually due.
func TestBudgetCycleLongIdleSkipsToCurrentWindow(t *testing.T) {
	ctx := context.Background()
	anchor := cycleTestAnchor()
	store := newStandaloneStore(t)

	budget := newCycleTestBudget("long-idle-budget", 100, 90)
	require.NoError(t, budget.SetOverride(25, configstoreTables.BudgetOverrideModeCycles, 3))
	store.budgets.Store(budget.ID, budget)

	// Ten days of downtime on a one minute window: 14400 windows elapsed.
	const idleWindows = 14400
	resumeAt := anchor.Add(idleWindows*cycleTestWindow + 17*time.Second)
	reset := sweepBudgetAt(t, store, budget.ID, resumeAt)
	require.NotNil(t, reset, "budget should be due after a long outage")

	assert.Zero(t, reset.CurrentUsage, "usage should be zeroed by the catch up reset")
	assert.True(t, reset.LastReset.Equal(anchor.Add(idleWindows*cycleTestWindow)),
		"catch up reset should land on the current window boundary: got %s", reset.LastReset.UTC())
	assert.False(t, reset.HasActiveOverride(),
		"a 3 cycle override should be fully spent after %d elapsed windows, got remaining=%d", idleWindows, reset.OverrideCyclesRemaining)

	// A second sweep at the same instant must be a no-op, proving the target
	// converged in one application instead of staying perpetually due.
	assert.Nil(t, sweepBudgetAt(t, store, budget.ID, resumeAt), "budget should not still be due after the catch up reset")

	_ = ctx
}

// TestCheckBudgetHonoursResetSemantics proves the enforcement path agrees with
// the reset path about whether a window has closed.
//
// CheckBudget open-codes its own expiry predicate instead of reusing
// budgetResetTarget, and the two disagree in both directions. It ignores
// IsCalendarAligned, so a calendar aligned budget is judged on a rolling clock
// for enforcement while it resets on the calendar. It also has no guard against
// a non-positive duration, and because time.Since is always at least zero such
// a budget is skipped past enforcement forever, which is unlimited spend.
func TestCheckBudgetHonoursResetSemantics(t *testing.T) {
	ctx := context.Background()

	t.Run("non-positive duration still enforces", func(t *testing.T) {
		store := newStandaloneStore(t)
		budget := buildBudgetWithUsage("zero-duration-budget", 10, 50, "0s")
		budget.CreatedAt = cycleTestAnchor()
		budget.LastReset = time.Now()
		store.budgets.Store(budget.ID, budget)

		decision, err := store.CheckBudget(ctx, EntityWiseBudgets{
			"virtual_key": {budget},
		}, nil)
		assert.Equal(t, DecisionBudgetExceeded, decision,
			"a budget with a non-positive reset duration must still be enforced, got %v", decision)
		assert.Error(t, err, "an over-limit budget should report why it was blocked")
	})

	t.Run("calendar aligned budget expires on the calendar boundary", func(t *testing.T) {
		now := time.Now().UTC()
		periodStart := configstoreTables.GetCalendarPeriodStart("1Y", now, configstoreTables.QuarterStartNotApplicable)
		// Guard against the final hours of the year, where the rolling
		// approximation of a year coincides with the calendar boundary and the
		// two predicates would agree by accident.
		if now.Sub(periodStart) > 300*24*time.Hour {
			t.Skip("too close to the year boundary for the two predicates to disagree")
		}

		store := newStandaloneStore(t)
		budget := buildBudgetWithUsage("calendar-budget", 10, 50, "1Y")
		budget.CreatedAt = periodStart.AddDate(-1, 0, 0)
		// One second before this year's boundary: the calendar period has
		// rolled over, so the budget is expired and must not be enforced.
		budget.LastReset = periodStart.Add(-time.Second)
		budget.IsCalendarAligned = true
		store.budgets.Store(budget.ID, budget)

		decision, _ := store.CheckBudget(ctx, EntityWiseBudgets{
			"virtual_key": {budget},
		}, nil)
		assert.Equal(t, DecisionAllow, decision,
			"calendar aligned budget crossed its period boundary and must be treated as expired, got %v", decision)
	})
}

// TestResetExpiredRateLimitsPersistsLastResetUnderTraffic proves a rate-limit
// reset advances the persisted boundary even when a request bumps the counter
// back above zero before the write lands.
//
// ResetExpiredRateLimits decides what to write by testing whether the in-memory
// counter is still zero. Under sustained traffic it never is, so the whole field
// pair is dropped, including the boundary column. The database boundary then
// never advances and the counter reads as perpetually due on the next restart.
func TestResetExpiredRateLimitsPersistsLastResetUnderTraffic(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	configStore, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: t.TempDir() + "/ratelimitreset.db"},
	}, logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, configStore.Close(ctx)) })

	anchor := cycleTestAnchor()
	rateLimit := buildRateLimitWithUsage("traffic-rate-limit", 1000, 900, 1000, 900)
	rateLimit.CreatedAt = anchor
	rateLimit.TokenLastReset = anchor
	rateLimit.RequestLastReset = anchor
	require.NoError(t, configStore.CreateRateLimit(ctx, rateLimit))

	store, err := NewLocalGovernanceStore(ctx, logger, configStore, nil, nil)
	require.NoError(t, err)

	boundary := anchor.Add(cycleTestWindow)
	reset, ok := store.ResetRateLimitAt(ctx, rateLimit.ID, &boundary, &boundary)
	require.True(t, ok, "rate limit should have been resettable one window after the anchor")

	// A request lands between the in-memory reset and the database write, which
	// is the normal case for any rate limit that is actually carrying traffic.
	raced := *reset
	raced.TokenCurrentUsage = 5
	raced.RequestCurrentUsage = 1
	require.NoError(t, store.ResetExpiredRateLimits(ctx, []*configstoreTables.TableRateLimit{&raced}))

	persisted, err := configStore.GetRateLimit(ctx, rateLimit.ID)
	require.NoError(t, err)
	assert.True(t, persisted.TokenLastReset.Equal(boundary),
		"token boundary was not persisted: got %s, want %s", persisted.TokenLastReset.UTC(), boundary.UTC())
	assert.True(t, persisted.RequestLastReset.Equal(boundary),
		"request boundary was not persisted: got %s, want %s", persisted.RequestLastReset.UTC(), boundary.UTC())
}

// TestVirtualKeyReloadDoesNotResurrectOverrideCycles proves the virtual-key reload
// path cannot hand back an override cycle the reset path already spent.
//
// This is the hole the in-process budget tests missed and a live cluster run found.
// UpsertBudgetConfig was documented as the single funnel for installing a budget, but
// UpdateVirtualKeyInMemory (which every VK reload goes through) wrote into the budgets
// map directly. It hand-rolled the CurrentUsage and LastReset preservation and so
// looked correct, but it never re-derived the override lifecycle, meaning the persisted
// row's stale cycle count won and the override outlived its grant.
func TestVirtualKeyReloadDoesNotResurrectOverrideCycles(t *testing.T) {
	ctx := context.Background()
	anchor := cycleTestAnchor()
	store := newStandaloneStore(t)

	budget := newCycleTestBudget("vk-reload-budget", 100, 0)
	require.NoError(t, budget.SetOverride(25, configstoreTables.BudgetOverrideModeCycles, 3))
	vk := buildVirtualKeyWithMultiBudgets("vk-reload", "sk-bf-vk-reload", "VK Reload", []configstoreTables.TableBudget{*budget})
	store.CreateVirtualKeyInMemory(ctx, vk)

	// The snapshot a reload carries: the row as the database holds it before the
	// leader has flushed the spent cycle.
	pristine := buildVirtualKeyWithMultiBudgets(vk.ID, "sk-bf-vk-reload", vk.Name, []configstoreTables.TableBudget{*budget})

	reset := sweepBudgetAt(t, store, budget.ID, anchor.Add(61*time.Second))
	require.NotNil(t, reset, "budget should be due one window after the anchor")
	require.Equal(t, 2, reset.OverrideCyclesRemaining, "one window closed, so one cycle should be spent")

	// A virtual-key reload lands, replaying the pristine grant.
	store.UpdateVirtualKeyInMemory(ctx, pristine, nil, nil, nil)

	got := store.LoadBudget(ctx, budget.ID)
	require.NotNil(t, got)
	assert.Equal(t, 2, got.OverrideCyclesRemaining,
		"virtual key reload resurrected a spent override cycle: %d remaining after reload, want 2", got.OverrideCyclesRemaining)
	assert.True(t, got.LastReset.Equal(anchor.Add(time.Minute)),
		"virtual key reload should preserve the advanced boundary, got %s", got.LastReset.UTC())
}

// TestUpsertBudgetConfigDoesNotAliasOrMutateCallerConfig proves the store never
// mutates the budget it is handed, and never publishes it as the live map entry.
//
// Both first-write branches used to refresh the caller's struct in place and then
// store that same pointer. Callers legitimately pass a pointer into a slice they keep
// using (BulkLoadUserAccessProfiles passes &p.Budgets[j]), so the caller's copy would
// be silently rewritten and any later write through it would race BumpBudgetUsage's
// clone-and-CAS. sync.Map makes the map operation atomic, not the pointed-to struct.
func TestUpsertBudgetConfigDoesNotAliasOrMutateCallerConfig(t *testing.T) {
	ctx := context.Background()
	anchor := cycleTestAnchor()

	for _, testCase := range []struct {
		name string
		// seed installs a pre-existing entry so the second branch is exercised.
		seed func(store *LocalGovernanceStore, budgetID string)
	}{
		{name: "first write", seed: func(*LocalGovernanceStore, string) {}},
		{name: "replacing an unusable entry", seed: func(store *LocalGovernanceStore, budgetID string) {
			// A non-budget value forces the type-assertion branch.
			store.budgets.Store(budgetID, "not a budget")
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := newStandaloneStore(t)

			// Three windows have closed since the grant, so a refresh would drop the
			// remaining count from 3 to 1 and be plainly visible on the caller's copy.
			caller := newCycleTestBudget("alias-budget", 100, 0)
			require.NoError(t, caller.SetOverride(25, configstoreTables.BudgetOverrideModeCycles, 3))
			caller.LastReset = anchor.Add(2 * cycleTestWindow)
			testCase.seed(store, caller.ID)

			before := *caller
			store.UpsertBudgetConfig(ctx, caller.ID, caller)

			assert.Equal(t, before.OverrideCyclesRemaining, caller.OverrideCyclesRemaining,
				"UpsertBudgetConfig rewrote the caller's remaining count in place")
			assert.True(t, before.LastReset.Equal(caller.LastReset),
				"UpsertBudgetConfig rewrote the caller's boundary in place")

			stored := store.LoadBudget(ctx, caller.ID)
			require.NotNil(t, stored)
			assert.NotSame(t, caller, stored,
				"the caller's pointer became the live map entry, so caller-side writes would race the usage CAS")
			// The stored copy is the one that carries the derived count.
			assert.Equal(t, 1, stored.OverrideCyclesRemaining,
				"stored budget should have two of three granted windows spent")
		})
	}
}

// TestDumpBudgetsPersistsUsageAndNeverRewindsBoundary covers both directions of the
// dump guard.
//
// The usage statement writes last_reset, and the derived override count is a function
// of (anchor, last_reset), so a stale snapshot must not move the persisted boundary
// backwards: that re-opens a window the cluster already spent. Reachable on leader
// change, when a follower still on the older boundary takes over and dumps before its
// own sweep catches up. Equally important is the other direction, which is why the
// guard is <= and not <: in steady state the persisted boundary equals the in-memory
// one, and a < guard would match zero rows and stop persisting usage entirely.
//
// Boundaries here are anchored on the present, not on cycleTestAnchor, so the budget is
// never already due and no sweep moves the boundary out from under the assertions.
func TestDumpBudgetsPersistsUsageAndNeverRewindsBoundary(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	const window = 24 * time.Hour
	now := time.Now().UTC().Truncate(time.Second)

	newStore := func(t *testing.T, seeded *configstoreTables.TableBudget) (*LocalGovernanceStore, configstore.ConfigStore) {
		t.Helper()
		configStore, err := configstore.NewConfigStore(ctx, &configstore.Config{
			Enabled: true,
			Type:    configstore.ConfigStoreTypeSQLite,
			Config:  &configstore.SQLiteConfig{Path: t.TempDir() + "/dump.db"},
		}, logger)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, configStore.Close(ctx)) })
		require.NoError(t, configStore.CreateBudget(ctx, seeded))
		store, err := NewLocalGovernanceStore(ctx, logger, configStore, nil, nil)
		require.NoError(t, err)
		return store, configStore
	}

	// seedBudget builds a budget whose current window opened at now, so WindowStart(now)
	// equals LastReset and nothing is due.
	seedBudget := func(id string) *configstoreTables.TableBudget {
		budget := buildBudgetWithUsage(id, 1000, 0, "24h")
		budget.CreatedAt = now
		budget.UpdatedAt = now
		budget.LastReset = now
		return budget
	}

	t.Run("unchanged boundary still persists usage", func(t *testing.T) {
		seeded := seedBudget("dump-steady-budget")
		store, configStore := newStore(t, seeded)

		require.NoError(t, store.BumpBudgetUsage(ctx, seeded.ID, 12.5))
		live := store.LoadBudget(ctx, seeded.ID)
		require.NotNil(t, live)
		require.True(t, live.LastReset.Equal(now), "precondition: no sweep should have moved the boundary")
		require.NoError(t, store.DumpBudgets(ctx, nil))

		persisted, err := configStore.GetBudget(ctx, seeded.ID)
		require.NoError(t, err)
		assert.Equal(t, 12.5, persisted.CurrentUsage,
			"usage must persist when the boundary has not moved: a < guard would match zero rows here")
	})

	t.Run("stale boundary cannot rewind the persisted one", func(t *testing.T) {
		// The database already holds the current boundary, as an earlier leader left it.
		seeded := seedBudget("dump-rewind-budget")
		store, configStore := newStore(t, seeded)

		// This node is a window behind and has not swept yet.
		stale := store.LoadBudget(ctx, seeded.ID)
		require.NotNil(t, stale)
		rewound := *stale
		rewound.LastReset = now.Add(-window)
		rewound.CurrentUsage = 5
		store.budgets.Store(seeded.ID, &rewound)

		require.NoError(t, store.DumpBudgets(ctx, nil))

		persisted, err := configStore.GetBudget(ctx, seeded.ID)
		require.NoError(t, err)
		assert.True(t, persisted.LastReset.UTC().Equal(now),
			"a stale snapshot rewound the persisted boundary to %s, re-opening a spent window", persisted.LastReset.UTC())
	})
}

// newQuarterlyBudget builds a calendar-aligned quarterly budget with the given
// fiscal start, created well before the window under test.
func newQuarterlyBudget(id string, quarterStartMonth time.Month, usage float64) *configstoreTables.TableBudget {
	createdAt := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	budget := buildBudgetWithUsage(id, 1000, usage, "1Q")
	budget.CreatedAt = createdAt
	budget.UpdatedAt = createdAt
	budget.IsCalendarAligned = true
	budget.ResetConfig = &configstoreTables.BudgetResetConfig{QuarterStartMonth: int(quarterStartMonth)}
	budget.LastReset = configstoreTables.GetCalendarPeriodStart("1Q", createdAt, quarterStartMonth)
	return budget
}

// TestQuarterlyBudgetResetTargetIsDeterministicAcrossNodes proves two nodes
// holding the same quarterly budget converge on the same boundary even when
// their reset tickers fire days apart.
//
// This is the cluster property the whole feature rests on. Each node derives the
// boundary from its own copy of the row, so if a peer failed to load the quarter
// definition - the AfterFind hook not firing on a preload, or an access profile
// materialising a managed key without copying it - the two nodes would land on
// different boundaries and reset each other's usage repeatedly.
func TestQuarterlyBudgetResetTargetIsDeterministicAcrossNodes(t *testing.T) {
	for _, quarterStart := range []time.Month{time.January, time.February, time.April, time.November} {
		t.Run(quarterStart.String(), func(t *testing.T) {
			nodeA := newStandaloneStore(t)
			nodeB := newStandaloneStore(t)
			nodeA.budgets.Store("shared-quarterly", newQuarterlyBudget("shared-quarterly", quarterStart, 250))
			nodeB.budgets.Store("shared-quarterly", newQuarterlyBudget("shared-quarterly", quarterStart, 250))

			// Sweep across two years. Node A's ticker lands early in each month,
			// node B's lands late, so any dependence on wall-clock phase shows up.
			for month := 0; month < 24; month++ {
				base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC).AddDate(0, month, 0)
				sweepBudgetAt(t, nodeA, "shared-quarterly", base.Add(2*time.Hour))
				sweepBudgetAt(t, nodeB, "shared-quarterly", base.AddDate(0, 0, 19).Add(21*time.Hour))
			}

			ctx := context.Background()
			gotA := nodeA.LoadBudget(ctx, "shared-quarterly")
			gotB := nodeB.LoadBudget(ctx, "shared-quarterly")
			require.NotNil(t, gotA)
			require.NotNil(t, gotB)

			assert.True(t, gotA.LastReset.Equal(gotB.LastReset),
				"nodes disagree on the quarter boundary: A at %s, B at %s",
				gotA.LastReset.UTC(), gotB.LastReset.UTC())

			// The boundary must be a real fiscal quarter start, not a ticker instant.
			assert.Equal(t, 1, gotA.LastReset.UTC().Day(), "boundary is not the 1st: %s", gotA.LastReset.UTC())
			assert.Zero(t, gotA.LastReset.UTC().Hour())
			expectedMonthOffset := (int(gotA.LastReset.UTC().Month()) - int(quarterStart) + 12) % 3
			assert.Zero(t, expectedMonthOffset,
				"boundary month %s is not a quarter start for fiscal year opening in %s",
				gotA.LastReset.UTC().Month(), quarterStart)
		})
	}
}

// TestQuarterlyBudgetIsNotDueImmediatelyAfterReset is the runtime guard against
// the perpetually-due failure (issue #4851 class).
//
// If WindowStart ever falls through to returning now for a quarterly budget, the
// reset path finds it due on every ticker tick and zeroes usage continuously,
// which reads as "the budget never enforces" rather than as an error.
func TestQuarterlyBudgetIsNotDueImmediatelyAfterReset(t *testing.T) {
	store := newStandaloneStore(t)
	store.budgets.Store("quarterly", newQuarterlyBudget("quarterly", time.February, 500))

	// First sweep well inside a quarter: the budget is due because LastReset is
	// still on the 2025 boundary.
	now := time.Date(2026, time.June, 15, 12, 0, 0, 0, time.UTC)
	require.NotNil(t, sweepBudgetAt(t, store, "quarterly", now), "expected the stale window to reset")

	// Every subsequent sweep inside the same quarter must be a no-op.
	for _, offset := range []time.Duration{time.Second, time.Hour, 24 * time.Hour, 20 * 24 * time.Hour} {
		assert.Nil(t, sweepBudgetAt(t, store, "quarterly", now.Add(offset)),
			"budget reported due again %s into the same quarter", offset)
	}

	ctx := context.Background()
	got := store.LoadBudget(ctx, "quarterly")
	require.NotNil(t, got)
	// February start puts 15 June in the May-Jul quarter.
	assert.Equal(t, time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC), got.LastReset.UTC())
	assert.Zero(t, got.CurrentUsage)
}

// TestQuarterlyBudgetResetsExactlyOncePerQuarter verifies the cadence: sweeping
// daily across a year produces four resets, not one per sweep and not one per
// month.
func TestQuarterlyBudgetResetsExactlyOncePerQuarter(t *testing.T) {
	store := newStandaloneStore(t)
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	budget := newQuarterlyBudget("quarterly", time.April, 100)
	budget.LastReset = configstoreTables.GetCalendarPeriodStart("1Q", start, time.April)
	store.budgets.Store("quarterly", budget)

	resets := 0
	for day := 1; day <= 365; day++ {
		if sweepBudgetAt(t, store, "quarterly", start.AddDate(0, 0, day)) != nil {
			resets++
		}
	}

	assert.Equal(t, 4, resets, "a year must contain exactly four quarterly resets")
}

// TestQuarterDefinitionSurvivesVirtualKeyReload closes the last link in the
// cluster propagation chain.
//
// A quarterly budget's fiscal calendar never travels over the wire. The gossip
// payload carries usage only, and a config change broadcasts nothing but an
// entity ID and action; each peer answers it by calling ReloadVirtualKey, which
// re-reads the row with Preload("Budgets") and hands the result to
// UpdateVirtualKeyInMemory. Two links in that chain are already covered - the
// AfterFind hook firing on a nested preload, and the migration persisting the
// column - and this covers the third: the in-memory store keeping the
// definition rather than rebuilding budgets from a field list.
//
// If it were lost here, the node that served the edit would enforce April
// quarters while every peer enforced January ones, with nothing logged.
func TestQuarterDefinitionSurvivesVirtualKeyReload(t *testing.T) {
	ctx := context.Background()

	newQuarterlyVK := func(quarterStart time.Month, usage float64) *configstoreTables.TableVirtualKey {
		budget := buildBudgetWithUsage("vk-quarterly-budget", 1000, usage, "1Q")
		budget.ResetConfig = &configstoreTables.BudgetResetConfig{QuarterStartMonth: int(quarterStart)}
		vk := buildVirtualKeyWithBudget("vk-quarterly", "bf-vk-quarterly", "quarterly", budget)
		vk.CalendarAligned = true
		return vk
	}

	// February start puts 15 June in the May-Jul quarter, where a January default
	// would say Apr-Jun and disagree by a whole month.
	now := time.Date(2026, time.June, 15, 12, 0, 0, 0, time.UTC)
	wantWindow := time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC)

	assertQuarterly := func(t *testing.T, loaded *configstoreTables.TableBudget, wantUsage float64) {
		t.Helper()
		require.NotNil(t, loaded)
		require.NotNil(t, loaded.ResetConfig, "the peer's in-memory budget lost its quarter definition")
		assert.Equal(t, int(time.February), loaded.ResetConfig.QuarterStartMonth)
		assert.True(t, loaded.IsCalendarAligned, "calendar alignment is stamped from the owning virtual key")
		// The definition has to actually drive the window, not merely be stored.
		assert.Equal(t, wantWindow, loaded.WindowStart(now))
		// Config travels on a reload; live counters do not.
		assert.Equal(t, wantUsage, loaded.CurrentUsage)
	}

	// A node seeing the virtual key for the first time: UpdateVirtualKeyInMemory
	// delegates to CreateVirtualKeyInMemory when nothing is cached yet.
	t.Run("first load", func(t *testing.T) {
		store := newStandaloneStore(t)
		store.UpdateVirtualKeyInMemory(ctx, newQuarterlyVK(time.February, 250), nil, nil, nil)
		assertQuarterly(t, store.LoadBudget(ctx, "vk-quarterly-budget"), 250)
	})

	// The path a peer actually takes after an ID-only config broadcast: the
	// virtual key is already cached, so the update branch rebuilds its budgets.
	// This branch is distinct from the create branch above and preserves live
	// usage from the cached row, so it has to be exercised separately - a
	// definition dropped only here would survive every first-load test.
	t.Run("reload over an existing virtual key", func(t *testing.T) {
		store := newStandaloneStore(t)
		store.UpdateVirtualKeyInMemory(ctx, newQuarterlyVK(time.January, 0), nil, nil, nil)

		// Simulate accrued spend on the cached copy, then reload with an edited
		// fiscal calendar, exactly as a peer would after the operator changes it.
		cached := store.LoadBudget(ctx, "vk-quarterly-budget")
		require.NotNil(t, cached)
		cached.CurrentUsage = 250
		store.storeBudget(cached.ID, cached)

		store.UpdateVirtualKeyInMemory(ctx, newQuarterlyVK(time.February, 0), nil, nil, nil)
		assertQuarterly(t, store.LoadBudget(ctx, "vk-quarterly-budget"), 250)
	})
}

// TestResetBudgetUsageInMemory covers the primitive behind an operator-triggered
// usage reset.
//
// It must zero usage without touching LastReset. Moving the boundary is ruled
// out: every persistence path guards it forward-only, and the intended target
// would often be earlier than the current value. Clearing the LastDBUsages
// baseline alongside is not optional - the dump path writes the delta between
// in-memory usage and that baseline, so a stale baseline would immediately
// re-add the spend that was just cleared.
func TestResetBudgetUsageInMemory(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStore(t)

	anchor := cycleTestAnchor()
	budget := buildBudgetWithUsage("operator-reset", 1000, 425, "1M")
	budget.LastReset = anchor
	store.budgets.Store(budget.ID, budget)
	store.LastDBUsagesBudgetsMu.Lock()
	store.LastDBUsagesBudgets[budget.ID] = 300
	store.LastDBUsagesBudgetsMu.Unlock()

	reset, ok := store.ResetBudgetUsageInMemory(ctx, budget.ID)
	require.True(t, ok, "reset should apply to a budget that exists")
	require.NotNil(t, reset)

	assert.Zero(t, reset.CurrentUsage, "usage must be cleared")
	assert.True(t, reset.LastReset.Equal(anchor), "the reset boundary must not move")

	loaded := store.LoadBudget(ctx, budget.ID)
	require.NotNil(t, loaded)
	assert.Zero(t, loaded.CurrentUsage, "the stored budget must reflect the reset")
	assert.True(t, loaded.LastReset.Equal(anchor))

	store.LastDBUsagesBudgetsMu.RLock()
	baseline := store.LastDBUsagesBudgets[budget.ID]
	store.LastDBUsagesBudgetsMu.RUnlock()
	assert.Zero(t, baseline, "a stale baseline would re-add the cleared spend on the next dump")
}

// TestResetBudgetUsageInMemoryMissingBudget verifies an unknown ID is reported
// rather than silently succeeding, so a caller cannot believe it reset something
// that does not exist.
func TestResetBudgetUsageInMemoryMissingBudget(t *testing.T) {
	store := newStandaloneStore(t)
	reset, ok := store.ResetBudgetUsageInMemory(context.Background(), "does-not-exist")
	assert.False(t, ok)
	assert.Nil(t, reset)
}

// TestDumpBudgetsCannotUndoOperatorReset covers the interleaving where an operator
// reset lands after a dump has snapshotted a budget but before that snapshot
// reaches the database.
//
// The two halves of a dump are separated by one database transaction per batch, so
// this window is real work, not an instant. A reset installs a fresh pointer via
// CAS, which the snapshot cannot see, and deliberately leaves LastReset alone, so
// the "<=" guard still matches. Without the reset generation the stale usage is
// written straight back over the operator's reset, and nothing reports an error:
// the number simply reappears.
func TestDumpBudgetsCannotUndoOperatorReset(t *testing.T) {
	ctx := context.Background()
	logger := NewMockLogger()
	now := time.Now().UTC().Truncate(time.Second)

	newStore := func(t *testing.T, seeded *configstoreTables.TableBudget) (*LocalGovernanceStore, configstore.ConfigStore) {
		t.Helper()
		configStore, err := configstore.NewConfigStore(ctx, &configstore.Config{
			Enabled: true,
			Type:    configstore.ConfigStoreTypeSQLite,
			Config:  &configstore.SQLiteConfig{Path: t.TempDir() + "/resetrace.db"},
		}, logger)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, configStore.Close(ctx)) })
		require.NoError(t, configStore.CreateBudget(ctx, seeded))
		store, err := NewLocalGovernanceStore(ctx, logger, configStore, nil, nil)
		require.NoError(t, err)
		return store, configStore
	}

	seed := buildBudgetWithUsage("dump-reset-race-budget", 1000, 500, "24h")
	seed.CreatedAt = now
	seed.UpdatedAt = now
	seed.LastReset = now

	store, configStore := newStore(t, seed)

	live := store.LoadBudget(ctx, seed.ID)
	require.NotNil(t, live)
	require.Equal(t, 500.0, live.CurrentUsage, "precondition: the budget carries spend to be reset")
	require.True(t, live.LastReset.Equal(now), "precondition: no sweep moved the boundary")

	// The dump reads the pre-reset usage, then stalls before writing.
	rows, gens := store.snapshotBudgetRows(nil)
	require.NotEmpty(t, rows)

	// The operator reset lands in the gap: memory is cleared here, and the handler's
	// own transaction clears the database.
	reset, ok := store.ResetBudgetUsageInMemory(ctx, seed.ID)
	require.True(t, ok)
	require.Zero(t, reset.CurrentUsage)
	require.True(t, reset.LastReset.Equal(now),
		"an operator reset must leave the boundary alone, which is why the <= guard cannot catch this")
	require.NoError(t, configStore.UpdateBudgetUsage(ctx, seed.ID, 0))

	// The stalled dump now writes. Its rows are stale and must be dropped.
	require.NoError(t, store.writeBudgetRows(ctx, rows, gens))

	persisted, err := configStore.GetBudget(ctx, seed.ID)
	require.NoError(t, err)
	assert.Zero(t, persisted.CurrentUsage,
		"a dump that snapshotted before the reset wrote %.2f back and silently undid the operator's reset", persisted.CurrentUsage)
	assert.True(t, persisted.LastReset.UTC().Equal(now),
		"dropping a stale row must not disturb the boundary")

	// The drop is scoped to the reset, not permanent: the next cycle persists the
	// post-reset value normally.
	require.NoError(t, store.BumpBudgetUsage(ctx, seed.ID, 7.5))
	require.NoError(t, store.DumpBudgets(ctx, nil))
	persisted, err = configStore.GetBudget(ctx, seed.ID)
	require.NoError(t, err)
	assert.Equal(t, 7.5, persisted.CurrentUsage,
		"the dump after a reset must resume persisting usage, or a reset would stop accounting for good")
}

// TestEnablingCalendarAlignmentCanResetAtTheBoundaryAlreadyPassed records what
// enabling alignment actually does today, which is not what this feature's docs
// describe. It is a characterization test, not an endorsement.
//
// budgetResetTarget returns WindowStart(now) whenever that is after LastReset, and
// for an aligned budget WindowStart is the most recent calendar boundary. So a
// budget whose window opened before that boundary is already due the moment
// alignment is switched on, and the next sweep clears its usage.
//
// The documented promise is that alignment applies from the next period and the
// current window keeps its usage. That holds only when LastReset is newer than the
// most recent boundary, which is the case the existing coverage exercises: it
// creates the budget at test time, so LastReset is always inside the current
// period. Both cases are pinned below so the difference is visible.
//
// The gap is closed by adoption: the owner handlers now call
// AdoptCalendarAlignmentInMemory on the switch-over, which moves each open window
// forward onto its boundary so the sweep below never finds it overdue. These
// assertions stay as they are on purpose - budgetResetTarget itself is unchanged,
// and it is precisely its "reset whatever is overdue" rule that makes adoption
// necessary. Read them as the reason the switch-over needs a step, not as a
// description of what an operator sees.
func TestEnablingCalendarAlignmentCanResetAtTheBoundaryAlreadyPassed(t *testing.T) {
	store := newStandaloneStore(t)
	now := time.Date(2026, time.February, 5, 12, 0, 0, 0, time.UTC)
	monthStart := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)

	alignedBudget := func(lastReset time.Time) *configstoreTables.TableBudget {
		return &configstoreTables.TableBudget{
			ID:                "align-boundary-budget",
			MaxLimit:          100,
			CurrentUsage:      42,
			ResetDuration:     "1M",
			IsCalendarAligned: true,
			CreatedAt:         lastReset,
			LastReset:         lastReset,
		}
	}

	t.Run("a window opened before the boundary is due at once", func(t *testing.T) {
		target := store.budgetResetTarget(alignedBudget(time.Date(2026, time.January, 10, 9, 0, 0, 0, time.UTC)), now)
		require.NotNil(t, target,
			"current behaviour: the budget is due immediately, so its $42 of usage is cleared on the next reset evaluation - the sweep or the next request, whichever lands first")
		assert.True(t, target.Equal(monthStart),
			"the reset lands on the boundary that already passed, not on the next one")
	})

	// Being due is evaluated on two independent paths, not one. The subtest above
	// asks budgetResetTarget the question the 10s ticker asks; BumpBudgetUsage asks
	// the identical question on the request path and resets before recording the
	// new cost. So an already-due window is cleared by the very next request even
	// on a node whose sweep has not fired yet, which is why the transition is
	// documented as the next reset *evaluation* rather than the next sweep.
	t.Run("the request path clears an already-due window without any sweep", func(t *testing.T) {
		ctx := context.Background()
		budget := alignedBudget(time.Date(2026, time.January, 10, 9, 0, 0, 0, time.UTC))
		budget.ID = "align-boundary-request-path-budget"
		openedAt := budget.LastReset
		store.budgets.Store(budget.ID, budget)

		// No sweepBudgetAt call anywhere in this subtest: the bump is the only
		// thing that touches the budget.
		require.NoError(t, store.BumpBudgetUsage(ctx, budget.ID, 2.5))

		bumped := store.LoadBudget(ctx, budget.ID)
		require.NotNil(t, bumped, "expected the budget to remain loaded after the bump")
		assert.Equal(t, 2.5, bumped.CurrentUsage,
			"the 42 already accumulated was cleared by the request itself rather than carried forward, so the new window holds only this request's cost")
		assert.True(t, bumped.LastReset.After(openedAt),
			"the request path advanced the window to its boundary, exactly as a sweep would")
	})

	t.Run("a window opened after the boundary is left alone", func(t *testing.T) {
		target := store.budgetResetTarget(alignedBudget(time.Date(2026, time.February, 3, 9, 0, 0, 0, time.UTC)), now)
		assert.Nil(t, target,
			"this is the case the docs describe, and the only one existing coverage reaches")
	})

	// calendar_aligned is an owner-level flag: the same switch drives the owner's
	// rate limits, and rateLimitResetTarget applies the identical
	// "boundary after lastReset" rule. Pinned here so the documented transition
	// cannot claim to cover rate limits while only budgets are actually checked -
	// and so the eventual behaviour fix is reminded it owes them the same rule.
	t.Run("rate limits carry the same transition", func(t *testing.T) {
		duration := "1M"

		before := store.rateLimitResetTarget(&duration, true,
			time.Time{}, time.Date(2026, time.January, 10, 9, 0, 0, 0, time.UTC), now)
		require.NotNil(t, before,
			"a rate-limit window opened before the boundary is due at once, exactly like a budget")
		assert.True(t, before.Equal(monthStart),
			"the reset lands on the boundary that already passed")

		after := store.rateLimitResetTarget(&duration, true,
			time.Time{}, time.Date(2026, time.February, 3, 9, 0, 0, 0, time.UTC), now)
		assert.Nil(t, after,
			"a window opened after the boundary is left alone, exactly like a budget")
	})

	// One owner holds several windows on independent cadences: every budget has its
	// own ResetDuration, and a rate limit has two more in TokenResetDuration and
	// RequestResetDuration, each with its own LastReset. The owner-level flag picks
	// the alignment *mode*; it does not give them a shared boundary. Pinned because
	// the documented behaviour is easy to state as "everything snaps together",
	// which is wrong in both directions below.
	t.Run("each window aligns on its own duration", func(t *testing.T) {
		lastReset := time.Date(2026, time.January, 10, 9, 0, 0, 0, time.UTC)
		monthly, daily := "1M", "1d"

		monthlyTarget := store.rateLimitResetTarget(&monthly, true, time.Time{}, lastReset, now)
		dailyTarget := store.rateLimitResetTarget(&daily, true, time.Time{}, lastReset, now)
		require.NotNil(t, monthlyTarget)
		require.NotNil(t, dailyTarget)
		assert.True(t, monthlyTarget.Equal(monthStart),
			"a monthly window aligns to the month boundary")
		assert.True(t, dailyTarget.Equal(time.Date(2026, time.February, 5, 0, 0, 0, 0, time.UTC)),
			"a daily window aligns to midnight, not to the month boundary the budget uses")
		assert.False(t, monthlyTarget.Equal(*dailyTarget),
			"two windows on one aligned owner do not share a boundary")
	})

	// A sub-day counter has no calendar boundary to snap to, so rateLimitResetTarget
	// falls through to the rolling branch and the owner-level flag changes nothing
	// for it. Documenting alignment as owner-wide without this exception promises a
	// behaviour the code does not have.
	t.Run("sub-day durations stay rolling even when aligned", func(t *testing.T) {
		hourly := "1h"
		anchor := time.Date(2026, time.February, 5, 9, 30, 0, 0, time.UTC)

		aligned := store.rateLimitResetTarget(&hourly, true, anchor, anchor, now)
		rolling := store.rateLimitResetTarget(&hourly, false, anchor, anchor, now)
		require.NotNil(t, aligned)
		require.NotNil(t, rolling)
		assert.True(t, aligned.Equal(*rolling),
			"calendar_aligned is inert on a sub-day window: it resets on its rolling anchor either way")
		assert.False(t, aligned.Equal(time.Date(2026, time.February, 5, 0, 0, 0, 0, time.UTC)),
			"and specifically it does not snap to midnight")
	})

	// The combination is accepted, not refused. BeforeSave validates owner count,
	// duration format, a positive duration, max_limit, the override fields and
	// reset_config - and nothing ties alignment to the duration, at the table layer
	// or in the handlers. So "sub-day plus aligned" persists happily and is simply
	// ignored at reset time, which is what the docs have to say.
	t.Run("a sub-day aligned budget is accepted, not rejected", func(t *testing.T) {
		ctx := context.Background()
		logger := NewMockLogger()
		configStore, err := configstore.NewConfigStore(ctx, &configstore.Config{
			Enabled: true,
			Type:    configstore.ConfigStoreTypeSQLite,
			Config:  &configstore.SQLiteConfig{Path: t.TempDir() + "/subdayaligned.db"},
		}, logger)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, configStore.Close(ctx)) })

		budget := buildBudgetWithUsage("sub-day-aligned-budget", 100, 0, "1h")
		budget.IsCalendarAligned = true
		require.NoError(t, configStore.CreateBudget(ctx, budget),
			"nothing validates alignment against the duration, so this must save")

		stored, err := configStore.GetBudget(ctx, budget.ID)
		require.NoError(t, err)
		assert.Equal(t, "1h", stored.ResetDuration,
			"the sub-day duration is kept as written rather than corrected or refused")
	})
}
