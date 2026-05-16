package governance

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	old := buildBudget(budgetID, 1000, "1h")
	old.LastReset = time.Now().Add(-2 * time.Hour)
	old.CurrentUsage = 999
	store.budgets.Store(budgetID, old)

	const goroutines = 128
	newLastReset := time.Now()

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
}
