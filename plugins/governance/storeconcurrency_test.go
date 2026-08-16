package governance

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newStandaloneStore builds a LocalGovernanceStore with no config store /
// persistence — just the in-memory maps. Enough for exercising the CAS
// primitives without going through GovernanceConfig preload paths.
func newStandaloneStore(t *testing.T) *LocalGovernanceStore {
	t.Helper()
	return &LocalGovernanceStore{
		logger:                         NewMockLogger(),
		LastDBUsagesBudgets:            map[string]float64{},
		LastDBUsagesTokensRateLimits:   map[string]int64{},
		LastDBUsagesRequestsRateLimits: map[string]int64{},
	}
}

// TestGetVirtualKeyByID exercises the ID-keyed lookup directly without taking
// the full governance snapshot path.
func TestGetVirtualKeyByID(t *testing.T) {
	store := newStandaloneStore(t)
	vk := &configstoreTables.TableVirtualKey{ID: "vk-id", Name: "test", Value: *schemas.NewSecretVar("sk-bf-test")}
	store.storeVirtualKey(vk.Value.GetValue(), vk)

	got, found := store.GetVirtualKeyByID(context.Background(), vk.ID)
	require.True(t, found)
	assert.Same(t, vk, got)

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
	store.budgets.Store(budgetID, buildBudget(budgetID, 1_000_000_000, "24h"))

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
	store.rateLimits.Store(rlID, buildRateLimit(rlID, 1_000_000_000, 1_000_000_000))

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
	store.budgets.Store(budgetID, old)

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
	store.budgets.Store(budget.ID, budget)

	// Usage is bumped by direct CAS rather than through BumpBudgetUsage, which
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
			for {
				raw, ok := store.budgets.Load(budget.ID)
				require.True(t, ok)
				current := raw.(*configstoreTables.TableBudget)
				clone := *current
				clone.CurrentUsage++
				if store.budgets.CompareAndSwap(budget.ID, raw, &clone) {
					break
				}
			}
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
