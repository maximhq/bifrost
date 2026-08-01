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
		periodStart := configstoreTables.GetCalendarPeriodStart("1Y", now)
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
