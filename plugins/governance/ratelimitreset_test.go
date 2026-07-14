package governance

import (
	"context"
	"fmt"
	"testing"
	"time"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

const resetBenchmarkVirtualKeys = 5000

// TestRequestTimeRateLimitResetPerformance verifies request-time rate-limit
// reset stays constant-time with thousands of embedded references. Runs as a
// regular test so it executes in CI via go test / make test-governance.
func TestRequestTimeRateLimitResetPerformance(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStoreForResetBenchmark()
	rateLimitID := seedResetBenchmarkVirtualKeys(ctx, store, resetBenchmarkVirtualKeys)

	const iterations = 1000
	start := time.Now()
	for i := 0; i < iterations; i++ {
		markRequestRateLimitExpired(store, rateLimitID)
		if err := store.BumpRateLimitUsage(ctx, rateLimitID, 0, false, true); err != nil {
			t.Fatal(err)
		}
	}
	nsPerOp := float64(time.Since(start).Nanoseconds()) / float64(iterations)
	// Request-time reset must stay O(1). With the reference-refresh regression
	// this exceeds 500µs per op; the fix keeps it well under 100µs.
	const maxNsPerOp = 100_000 // 100µs
	if nsPerOp > maxNsPerOp {
		t.Fatalf("request-time reset too slow: %.0f ns/op (max %d ns/op) — likely running expensive O(N) work on the request hot path", nsPerOp, maxNsPerOp)
	}
	t.Logf("request-time reset: %.0f ns/op (%d iterations)", nsPerOp, iterations)
}

// BenchmarkSingleRequestTimeRateLimitResetDoesNotRefreshReferences runs the same
// reset path exactly once per benchmark iteration for fixed-count profiles.
func BenchmarkSingleRequestTimeRateLimitResetDoesNotRefreshReferences(b *testing.B) {
	ctx := context.Background()

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		store := newStandaloneStoreForResetBenchmark()
		rateLimitID := seedResetBenchmarkVirtualKeys(ctx, store, resetBenchmarkVirtualKeys)
		markRequestRateLimitExpired(store, rateLimitID)

		b.StartTimer()
		if err := store.BumpRateLimitUsage(ctx, rateLimitID, 0, false, true); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
	}
}

// TestRequestTimeRateLimitResetSkipsReferenceRefresh verifies request-time resets
// keep canonical side effects without scanning embedded references.
func TestRequestTimeRateLimitResetSkipsReferenceRefresh(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStoreForResetBenchmark()
	rateLimitID := seedResetBenchmarkVirtualKeys(ctx, store, 10)
	staleRateLimit := store.LoadRateLimit(ctx, rateLimitID)
	markRequestRateLimitExpired(store, rateLimitID)

	resetHookCalls := 0
	store.SetResetHooks(nil, func(resetRateLimits []*configstoreTables.TableRateLimit) {
		resetHookCalls++
		if len(resetRateLimits) != 1 {
			t.Fatalf("expected one reset rate limit, got %d", len(resetRateLimits))
		}
		if resetRateLimits[0].ID != rateLimitID {
			t.Fatalf("expected reset rate limit %q, got %q", rateLimitID, resetRateLimits[0].ID)
		}
	})

	if err := store.BumpRateLimitUsage(ctx, rateLimitID, 0, false, true); err != nil {
		t.Fatal(err)
	}
	resetRateLimit := store.LoadRateLimit(ctx, rateLimitID)
	if resetRateLimit == nil {
		t.Fatal("expected reset rate limit to remain loaded")
	}
	if resetRateLimit.RequestCurrentUsage != 1 {
		t.Fatalf("expected request usage to be bumped after reset, got %d", resetRateLimit.RequestCurrentUsage)
	}
	if resetRateLimit.RequestLastReset.Before(staleRateLimit.RequestLastReset) || resetRateLimit.RequestLastReset.Equal(staleRateLimit.RequestLastReset) {
		t.Fatalf("expected request reset timestamp to advance beyond %s, got %s", staleRateLimit.RequestLastReset, resetRateLimit.RequestLastReset)
	}
	if resetHookCalls != 1 {
		t.Fatalf("expected request-time reset hook to fire once, got %d", resetHookCalls)
	}

	store.LastDBUsagesRateLimitsRequestsMu.RLock()
	lastDBRequests := store.LastDBUsagesRequestsRateLimits[rateLimitID]
	store.LastDBUsagesRateLimitsRequestsMu.RUnlock()
	if lastDBRequests != 0 {
		t.Fatalf("expected request LastDB baseline to reset to 0, got %d", lastDBRequests)
	}

	rawVK, ok := store.virtualKeys.Load("sk-bf-reset-00000")
	if !ok || rawVK == nil {
		t.Fatal("expected seeded virtual key to remain loaded")
	}
	vk, ok := rawVK.(*configstoreTables.TableVirtualKey)
	if !ok || vk == nil {
		t.Fatal("expected seeded virtual key to have the correct type")
	}
	// Embedded reference should remain stale (not refreshed) after request-time reset.
	// It may be non-nil because CreateVirtualKeyInMemory keeps embedded references,
	// but it must NOT point at the freshly reset canonical object.
	if vk.RateLimit == resetRateLimit {
		t.Fatal("expected request-time reset NOT to refresh embedded virtual-key rate-limit reference")
	}
	if vk.RateLimitID == nil || *vk.RateLimitID != rateLimitID {
		t.Fatal("expected request-time reset to preserve owner-to-rate-limit ID mapping")
	}
}

// TestBackgroundRateLimitResetRefreshesReferences verifies non-request resets still hydrate embedded references.
func TestBackgroundRateLimitResetRefreshesReferences(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStoreForResetBenchmark()
	rateLimitID := seedResetBenchmarkVirtualKeys(ctx, store, 10)
	staleRateLimit := store.LoadRateLimit(ctx, rateLimitID)
	markRequestRateLimitExpired(store, rateLimitID)

	resetRateLimits := store.ResetExpiredRateLimitsInMemory(ctx, true, rateLimitID)
	if len(resetRateLimits) != 1 {
		t.Fatalf("expected one background reset, got %d", len(resetRateLimits))
	}

	rawVK, ok := store.virtualKeys.Load("sk-bf-reset-00000")
	if !ok || rawVK == nil {
		t.Fatal("expected seeded virtual key to remain loaded")
	}
	vk, ok := rawVK.(*configstoreTables.TableVirtualKey)
	if !ok || vk == nil {
		t.Fatal("expected seeded virtual key to have the correct type")
	}
	if vk.RateLimit == nil {
		t.Fatal("expected background reset to refresh virtual-key rate-limit reference")
	}
	if vk.RateLimit != resetRateLimits[0] {
		t.Fatal("expected background reset to point virtual-key reference at canonical reset object")
	}
	if vk.RateLimitID == nil || *vk.RateLimitID != rateLimitID {
		t.Fatal("expected background reset to preserve owner-to-rate-limit ID mapping")
	}
	if resetRateLimits[0] == staleRateLimit {
		t.Fatal("expected canonical reset to replace stale rate-limit snapshot")
	}
}

// TestRateLimitResetConvergesForCalendarAlignedSubDayDuration reproduces
// issue #4851: an owner with calendar_aligned=true stamps IsCalendarAligned
// onto a rate limit whose reset durations are sub-day ("1m"), for which
// GetCalendarPeriodStart has no calendar boundary and returns now. The reset
// target is then "due" again immediately after every reset, so the
// reset-then-recheck loop in BumpRateLimitUsage never reaches its exit
// condition and pins one postHookWorker goroutine per request at 100% CPU
// on nanosecond-resolution clocks (Linux). This test asserts the exit
// condition directly, which is deterministic on every platform: after one
// reset, the reset target must no longer be due.
func TestRateLimitResetConvergesForCalendarAlignedSubDayDuration(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStoreForResetBenchmark()
	rateLimitID := "calendar-aligned-subday-converge-rate-limit"
	rateLimit := buildRateLimit(rateLimitID, 1_000_000, 1_000_000) // "1m" durations
	rateLimit.IsCalendarAligned = true
	rateLimit.TokenLastReset = time.Now().Add(-2 * time.Minute)
	rateLimit.RequestLastReset = time.Now().Add(-2 * time.Minute)
	store.rateLimits.Store(rateLimitID, rateLimit)

	tokenTarget, requestTarget := store.rateLimitResetTargets(rateLimit, time.Now())
	if tokenTarget == nil && requestTarget == nil {
		t.Fatal("expected expired rate limit to be due for reset before the first reset")
	}
	if reset := store.ResetExpiredRateLimitsInMemory(ctx, false, rateLimitID); len(reset) != 1 {
		t.Fatalf("expected one rate limit reset, got %d", len(reset))
	}

	tokenTarget, requestTarget = store.rateLimitResetTargets(store.LoadRateLimit(ctx, rateLimitID), time.Now())
	if tokenTarget != nil || requestTarget != nil {
		t.Fatalf("reset target still due immediately after reset (token=%v request=%v): BumpRateLimitUsage would spin forever (issue #4851)", tokenTarget, requestTarget)
	}
}

// TestBudgetResetConvergesForCalendarAlignedSubDayDuration covers the same
// defect on the budget path: a calendar-aligned owner with a sub-day budget
// reset duration ("5h") must not be due for reset again immediately after
// resetting, or BumpBudgetUsage spins forever.
func TestBudgetResetConvergesForCalendarAlignedSubDayDuration(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStoreForResetBenchmark()
	budgetID := "calendar-aligned-subday-converge-budget"
	budget := buildBudgetWithUsage(budgetID, 100, 42, "5h")
	budget.IsCalendarAligned = true
	budget.LastReset = time.Now().Add(-6 * time.Hour)
	store.budgets.Store(budgetID, budget)

	if store.budgetResetTarget(budget, time.Now()) == nil {
		t.Fatal("expected expired budget to be due for reset before the first reset")
	}
	if reset := store.ResetExpiredBudgetsInMemory(ctx, false, budgetID); len(reset) != 1 {
		t.Fatalf("expected one budget reset, got %d", len(reset))
	}

	if target := store.budgetResetTarget(store.LoadBudget(ctx, budgetID), time.Now()); target != nil {
		t.Fatalf("reset target still due immediately after reset (%v): BumpBudgetUsage would spin forever (issue #4851)", target)
	}
}

// TestNonPositiveDurationsAreNeverDue covers the second spin route of issue
// #4851: "0s" and negative durations parse successfully, and on the rolling
// path now.Sub(lastReset) >= duration is then true on every check, making the
// reset target perpetually due. Such durations must be treated as invalid
// (never due), and BumpRateLimitUsage/BumpBudgetUsage must still terminate
// and apply the increment.
func TestNonPositiveDurationsAreNeverDue(t *testing.T) {
	ctx := context.Background()
	for _, duration := range []string{"0s", "-5m"} {
		t.Run(duration, func(t *testing.T) {
			store := newStandaloneStoreForResetBenchmark()
			d := duration

			rateLimitID := "non-positive-duration-rate-limit-" + duration
			rateLimit := buildRateLimit(rateLimitID, 1_000_000, 1_000_000)
			rateLimit.TokenResetDuration = &d
			rateLimit.RequestResetDuration = &d
			rateLimit.TokenLastReset = time.Now().Add(-time.Hour)
			rateLimit.RequestLastReset = time.Now().Add(-time.Hour)
			store.rateLimits.Store(rateLimitID, rateLimit)

			tokenTarget, requestTarget := store.rateLimitResetTargets(rateLimit, time.Now())
			if tokenTarget != nil || requestTarget != nil {
				t.Fatalf("non-positive duration %q must never be due (token=%v request=%v): a due target here spins BumpRateLimitUsage forever (issue #4851)", duration, tokenTarget, requestTarget)
			}
			if err := store.BumpRateLimitUsage(ctx, rateLimitID, 7, true, true); err != nil {
				t.Fatal(err)
			}
			bumped := store.LoadRateLimit(ctx, rateLimitID)
			if bumped.TokenCurrentUsage != 7 || bumped.RequestCurrentUsage != 1 {
				t.Fatalf("expected usage 7/1 after bump, got %d/%d", bumped.TokenCurrentUsage, bumped.RequestCurrentUsage)
			}

			budgetID := "non-positive-duration-budget-" + duration
			budget := buildBudgetWithUsage(budgetID, 100, 5, duration)
			budget.LastReset = time.Now().Add(-time.Hour)
			store.budgets.Store(budgetID, budget)

			if target := store.budgetResetTarget(budget, time.Now()); target != nil {
				t.Fatalf("non-positive duration %q must never be due (%v): a due target here spins BumpBudgetUsage forever (issue #4851)", duration, target)
			}
			if err := store.BumpBudgetUsage(ctx, budgetID, 1.5); err != nil {
				t.Fatal(err)
			}
			if bumpedBudget := store.LoadBudget(ctx, budgetID); bumpedBudget.CurrentUsage != 6.5 {
				t.Fatalf("expected budget usage 6.5 after bump, got %f", bumpedBudget.CurrentUsage)
			}
		})
	}
}

// TestBumpRateLimitUsageCalendarAlignedSubDayDurationTerminates guards the
// same defect end to end: BumpRateLimitUsage must return and apply the
// increment. On microsecond-resolution clocks (darwin) the broken loop can
// exit by luck when two consecutive time.Now() calls land on the same wall
// tick, so the convergence test above is the deterministic reproduction;
// this one catches the hang on nanosecond-resolution clocks (Linux CI).
func TestBumpRateLimitUsageCalendarAlignedSubDayDurationTerminates(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStoreForResetBenchmark()
	rateLimitID := "calendar-aligned-subday-rate-limit"
	rateLimit := buildRateLimit(rateLimitID, 1_000_000, 1_000_000) // "1m" durations
	rateLimit.IsCalendarAligned = true
	store.rateLimits.Store(rateLimitID, rateLimit)

	done := make(chan error, 1)
	go func() {
		done <- store.BumpRateLimitUsage(ctx, rateLimitID, 10, true, true)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("BumpRateLimitUsage did not return within 5s: infinite request-time reset loop on calendar-aligned sub-day duration (issue #4851)")
	}

	bumped := store.LoadRateLimit(ctx, rateLimitID)
	if bumped == nil {
		t.Fatal("expected rate limit to remain loaded")
	}
	if bumped.TokenCurrentUsage != 10 {
		t.Fatalf("expected token usage 10 after bump, got %d", bumped.TokenCurrentUsage)
	}
	if bumped.RequestCurrentUsage != 1 {
		t.Fatalf("expected request usage 1 after bump, got %d", bumped.RequestCurrentUsage)
	}
}

// TestBumpBudgetUsageCalendarAlignedSubDayDurationTerminates covers the same
// defect on the budget path: calendar-aligned owner with a sub-day budget
// reset duration ("5h") must not spin BumpBudgetUsage forever.
func TestBumpBudgetUsageCalendarAlignedSubDayDurationTerminates(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStoreForResetBenchmark()
	budgetID := "calendar-aligned-subday-budget"
	budget := buildBudgetWithUsage(budgetID, 100, 0, "5h")
	budget.IsCalendarAligned = true
	store.budgets.Store(budgetID, budget)

	done := make(chan error, 1)
	go func() {
		done <- store.BumpBudgetUsage(ctx, budgetID, 2.5)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("BumpBudgetUsage did not return within 5s: infinite request-time reset loop on calendar-aligned sub-day duration (issue #4851)")
	}

	bumped := store.LoadBudget(ctx, budgetID)
	if bumped == nil {
		t.Fatal("expected budget to remain loaded")
	}
	if bumped.CurrentUsage != 2.5 {
		t.Fatalf("expected budget usage 2.5 after bump, got %f", bumped.CurrentUsage)
	}
}

// newStandaloneStoreForResetBenchmark creates an in-memory-only store so benchmark
// profiles isolate sync.Map/reference-update CPU rather than DB IO.
func newStandaloneStoreForResetBenchmark() *LocalGovernanceStore {
	return &LocalGovernanceStore{
		logger:                         NewMockLogger(),
		LastDBUsagesBudgets:            map[string]float64{},
		LastDBUsagesTokensRateLimits:   map[string]int64{},
		LastDBUsagesRequestsRateLimits: map[string]int64{},
	}
}

// seedResetBenchmarkVirtualKeys creates many VK owner mappings to one rate limit so
// the benchmark guards against reintroducing global owner scans.
func seedResetBenchmarkVirtualKeys(ctx context.Context, store *LocalGovernanceStore, virtualKeys int) string {
	rateLimitID := "request-reset-rate-limit"
	rateLimit := buildRateLimit(rateLimitID, 1_000_000_000, 1_000_000_000)
	store.rateLimits.Store(rateLimitID, rateLimit)

	for i := 0; i < virtualKeys; i++ {
		vk := buildVirtualKeyWithRateLimit(
			fmt.Sprintf("reset-vk-%05d", i),
			fmt.Sprintf("sk-bf-reset-%05d", i),
			fmt.Sprintf("Reset VK %05d", i),
			rateLimit,
		)
		store.CreateVirtualKeyInMemory(ctx, vk)
	}

	return rateLimitID
}

// markRequestRateLimitExpired forces BumpRateLimitUsage down the request-time
// reset path on the next call.
func markRequestRateLimitExpired(store *LocalGovernanceStore, rateLimitID string) {
	raw, ok := store.rateLimits.Load(rateLimitID)
	if !ok || raw == nil {
		return
	}
	rateLimit, ok := raw.(*configstoreTables.TableRateLimit)
	if !ok || rateLimit == nil {
		return
	}
	clone := *rateLimit
	clone.RequestCurrentUsage = 123
	clone.RequestLastReset = time.Now().Add(-2 * time.Minute)
	store.rateLimits.Store(rateLimitID, &clone)
}
