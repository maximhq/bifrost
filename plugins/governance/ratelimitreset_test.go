package governance

import (
	"context"
	"fmt"
	"testing"
	"time"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

const resetBenchmarkVirtualKeys = 5000

// scaleTimingBudget widens a wall-clock budget when the race detector is on,
// since it slows execution 2-20x. The regressions these budgets guard are
// orders of magnitude, so the extra headroom still catches them.
func scaleTimingBudget(budget time.Duration) time.Duration {
	if raceEnabled {
		return budget * 20
	}
	return budget
}

// timingBudgetAttempts is how many times a wall-clock assertion re-measures
// before failing. Host noise (GC pause, scheduler stall, slow runner) inflates
// one run, not all of them; the algorithmic regressions these tests guard are
// orders of magnitude over budget and fail every attempt.
const timingBudgetAttempts = 3

// TestRequestTimeRateLimitResetPerformance verifies request-time rate-limit
// reset stays constant-time with thousands of embedded references. Runs as a
// regular test so it executes in CI via go test / make test-governance.
func TestRequestTimeRateLimitResetPerformance(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStoreForResetBenchmark()
	rateLimitID := seedResetBenchmarkVirtualKeys(ctx, store, resetBenchmarkVirtualKeys)

	const iterations = 1000
	// Request-time reset must stay O(1). With the reference-refresh regression
	// this exceeds 500µs per op; the fix keeps it well under 100µs. Best of
	// timingBudgetAttempts runs, so a single noisy run cannot flake the test.
	maxPerOp := scaleTimingBudget(200 * time.Microsecond)
	var bestPerOp time.Duration
	for attempt := 0; attempt < timingBudgetAttempts; attempt++ {
		start := time.Now()
		for i := 0; i < iterations; i++ {
			markRequestRateLimitExpired(store, rateLimitID)
			if err := store.BumpRateLimitUsage(ctx, rateLimitID, 0, false, true); err != nil {
				t.Fatal(err)
			}
		}
		perOp := time.Since(start) / iterations
		if bestPerOp == 0 || perOp < bestPerOp {
			bestPerOp = perOp
		}
		if bestPerOp <= maxPerOp {
			break
		}
	}
	if bestPerOp > maxPerOp {
		t.Fatalf("request-time reset too slow: %v per op (max %v) across %d attempts - likely running expensive O(N) work on the request hot path", bestPerOp, maxPerOp, timingBudgetAttempts)
	}
	t.Logf("request-time reset: %v per op (%d iterations)", bestPerOp, iterations)
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

// TestBackgroundBudgetResetRefreshesReferences verifies the batched budget
// reference refresh: one background sweep must update embedded budget copies
// on every owner type (VK, team, customer), replace only the budgets that
// actually reset, and leave unexpired sibling budgets untouched.
func TestBackgroundBudgetResetRefreshesReferences(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStoreForResetBenchmark()
	now := time.Now()

	expired := buildBudgetWithUsage("bg-budget-expired", 100, 42, "1h")
	expired.LastReset = now.Add(-2 * time.Hour)
	fresh := buildBudgetWithUsage("bg-budget-fresh", 100, 7, "1h")
	fresh.LastReset = now.Add(-10 * time.Minute)
	vk := buildVirtualKey("bg-vk", "sk-bf-bg-refresh", "Budget refresh VK", true)
	vk.Budgets = []configstoreTables.TableBudget{*expired, *fresh}
	store.CreateVirtualKeyInMemory(ctx, vk)

	teamBudget := buildBudgetWithUsage("bg-team-budget", 200, 33, "1h")
	teamBudget.LastReset = now.Add(-2 * time.Hour)
	store.CreateTeamInMemory(ctx, buildTeam("bg-team", "Budget refresh team", teamBudget))

	customerBudget := buildBudgetWithUsage("bg-customer-budget", 300, 21, "1h")
	customerBudget.LastReset = now.Add(-2 * time.Hour)
	store.CreateCustomerInMemory(ctx, buildCustomer("bg-customer", "Budget refresh customer", customerBudget))

	reset := store.ResetExpiredBudgetsInMemory(ctx, true)
	if len(reset) != 3 {
		t.Fatalf("expected 3 expired budgets to reset (vk, team, customer), got %d", len(reset))
	}

	rawVK, ok := store.virtualKeys.Load("sk-bf-bg-refresh")
	if !ok || rawVK == nil {
		t.Fatal("expected virtual key to remain loaded")
	}
	gotVK := rawVK.(*configstoreTables.TableVirtualKey)
	var gotExpired, gotFresh *configstoreTables.TableBudget
	for i := range gotVK.Budgets {
		switch gotVK.Budgets[i].ID {
		case "bg-budget-expired":
			gotExpired = &gotVK.Budgets[i]
		case "bg-budget-fresh":
			gotFresh = &gotVK.Budgets[i]
		}
	}
	if gotExpired == nil || gotFresh == nil {
		t.Fatal("expected both embedded VK budgets to survive the refresh")
	}
	if gotExpired.CurrentUsage != 0 {
		t.Fatalf("expected embedded expired VK budget zeroed after background refresh, got %f", gotExpired.CurrentUsage)
	}
	if !gotExpired.LastReset.After(now.Add(-2 * time.Hour)) {
		t.Fatalf("expected embedded expired VK budget LastReset to advance, got %v", gotExpired.LastReset)
	}
	if gotFresh.CurrentUsage != 7 {
		t.Fatalf("expected unexpired sibling budget untouched at 7, got %f", gotFresh.CurrentUsage)
	}

	rawTeam, ok := store.teams.Load("bg-team")
	if !ok || rawTeam == nil {
		t.Fatal("expected team to remain loaded")
	}
	gotTeam := rawTeam.(*configstoreTables.TableTeam)
	if len(gotTeam.Budgets) != 1 || gotTeam.Budgets[0].CurrentUsage != 0 {
		t.Fatalf("expected embedded team budget zeroed after background refresh, got %+v", gotTeam.Budgets)
	}

	rawCustomer, ok := store.customers.Load("bg-customer")
	if !ok || rawCustomer == nil {
		t.Fatal("expected customer to remain loaded")
	}
	gotCustomer := rawCustomer.(*configstoreTables.TableCustomer)
	if len(gotCustomer.Budgets) != 1 || gotCustomer.Budgets[0].CurrentUsage != 0 {
		t.Fatalf("expected embedded customer budget zeroed after background refresh, got %+v", gotCustomer.Budgets)
	}
}

// TestRateLimitResetAccuracyEdgeCases verifies that resets fire exactly when
// they should and only touch the counter that is actually expired, across the
// rolling and calendar-aligned semantics and their boundary conditions.
func TestRateLimitResetAccuracyEdgeCases(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	t.Run("mixed expiry resets only the expired counter", func(t *testing.T) {
		store := newStandaloneStoreForResetBenchmark()
		tokenDuration, requestDuration := "1m", "1h"
		rl := buildRateLimit("mixed-expiry", 1_000_000, 1_000_000)
		rl.TokenResetDuration = &tokenDuration
		rl.RequestResetDuration = &requestDuration
		rl.TokenCurrentUsage = 555
		rl.TokenLastReset = now.Add(-2 * time.Minute) // expired
		rl.RequestCurrentUsage = 7
		rl.RequestLastReset = now.Add(-30 * time.Minute) // NOT expired
		store.rateLimits.Store(rl.ID, rl)

		if err := store.BumpRateLimitUsage(ctx, rl.ID, 10, true, true); err != nil {
			t.Fatal(err)
		}
		bumped := store.LoadRateLimit(ctx, rl.ID)
		if bumped.TokenCurrentUsage != 10 {
			t.Fatalf("expected token counter reset then bumped to 10, got %d", bumped.TokenCurrentUsage)
		}
		if bumped.RequestCurrentUsage != 8 {
			t.Fatalf("expected unexpired request counter preserved and bumped to 8, got %d", bumped.RequestCurrentUsage)
		}
	})

	t.Run("exact rolling boundary is due", func(t *testing.T) {
		store := newStandaloneStoreForResetBenchmark()
		rl := buildRateLimit("exact-boundary", 1_000_000, 1_000_000)
		probe := time.Now()
		rl.TokenLastReset = probe.Add(-time.Minute) // exactly one window ago
		tok, _ := store.rateLimitResetTargets(rl, probe)
		if tok == nil {
			t.Fatal("expected token counter to be due exactly at the window boundary (>= semantics)")
		}
	})

	t.Run("one nanosecond before the boundary is not due", func(t *testing.T) {
		store := newStandaloneStoreForResetBenchmark()
		rl := buildRateLimit("pre-boundary", 1_000_000, 1_000_000)
		probe := time.Now()
		rl.TokenLastReset = probe.Add(-time.Minute + time.Nanosecond)
		rl.RequestLastReset = probe.Add(-time.Minute + time.Nanosecond)
		tok, req := store.rateLimitResetTargets(rl, probe)
		if tok != nil || req != nil {
			t.Fatalf("expected no reset just before the window boundary, got token=%v request=%v", tok, req)
		}
	})

	t.Run("calendar day snaps LastReset to period start not now", func(t *testing.T) {
		store := newStandaloneStoreForResetBenchmark()
		duration := "1d"
		rl := buildRateLimit("calendar-day", 1_000_000, 1_000_000)
		rl.TokenResetDuration = &duration
		rl.RequestResetDuration = &duration
		rl.IsCalendarAligned = true
		rl.TokenLastReset = now.Add(-26 * time.Hour) // last reset yesterday
		rl.RequestLastReset = now.Add(-26 * time.Hour)
		store.rateLimits.Store(rl.ID, rl)

		if reset := store.ResetExpiredRateLimitsInMemory(ctx, false, rl.ID); len(reset) != 1 {
			t.Fatalf("expected one reset, got %d", len(reset))
		}
		wantPeriodStart := configstoreTables.GetCalendarPeriodStart(duration, now)
		got := store.LoadRateLimit(ctx, rl.ID)
		if !got.TokenLastReset.Equal(wantPeriodStart) {
			t.Fatalf("calendar reset must snap TokenLastReset to the period start %v, got %v", wantPeriodStart, got.TokenLastReset)
		}
	})

	t.Run("calendar aligned within current period is not due", func(t *testing.T) {
		store := newStandaloneStoreForResetBenchmark()
		duration := "1d"
		rl := buildRateLimit("calendar-current-period", 1_000_000, 1_000_000)
		rl.TokenResetDuration = &duration
		rl.RequestResetDuration = &duration
		rl.IsCalendarAligned = true
		periodStart := configstoreTables.GetCalendarPeriodStart(duration, now)
		rl.TokenLastReset = periodStart
		rl.RequestLastReset = periodStart
		tok, req := store.rateLimitResetTargets(rl, now)
		if tok != nil || req != nil {
			t.Fatalf("already reset at period start must not be due again, got token=%v request=%v", tok, req)
		}
	})

	t.Run("future LastReset from clock skew is not due and does not spin", func(t *testing.T) {
		store := newStandaloneStoreForResetBenchmark()
		rl := buildRateLimit("future-last-reset", 1_000_000, 1_000_000)
		rl.TokenCurrentUsage = 3
		rl.TokenLastReset = now.Add(time.Hour) // another node's clock ahead
		rl.RequestLastReset = now.Add(time.Hour)
		store.rateLimits.Store(rl.ID, rl)

		if err := store.BumpRateLimitUsage(ctx, rl.ID, 4, true, true); err != nil {
			t.Fatal(err)
		}
		bumped := store.LoadRateLimit(ctx, rl.ID)
		if bumped.TokenCurrentUsage != 7 {
			t.Fatalf("expected future-dated counter preserved and bumped to 7, got %d", bumped.TokenCurrentUsage)
		}
	})
}

// TestBackgroundResetReferenceRefreshScales demonstrates the background-sweep
// scale problem: ResetExpiredRateLimitsInMemory(ctx, true) calls
// updateRateLimitReferences once per reset rate limit, and each call Ranges
// over EVERY virtual key (cloning each VK before even checking for a match).
// With per-VK rate limits the tick cost is O(dueLimits x totalVKs): quadratic
// in key count. The per-reset budget below is what an O(owners) refresh
// (reverse index or one batched pass per tick) sustains; the current
// implementation exceeds it by orders of magnitude, and since per-reset cost
// grows linearly with VK count, at 100k keys a single 10s tick becomes
// minutes of CPU - permanent 100% CPU without any infinite loop.
func TestBackgroundResetReferenceRefreshScales(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStoreForResetBenchmark()
	const vkCount = 3000

	for i := 0; i < vkCount; i++ {
		rl := buildRateLimit(fmt.Sprintf("scale-rl-%06d", i), 1_000_000, 1_000_000)
		rl.RequestCurrentUsage = 9
		rl.RequestLastReset = time.Now().Add(-2 * time.Minute) // "1m" window: expired
		rl.TokenLastReset = time.Now().Add(-2 * time.Minute)
		store.rateLimits.Store(rl.ID, rl)
		vk := buildVirtualKeyWithRateLimit(
			fmt.Sprintf("scale-vk-%06d", i),
			fmt.Sprintf("sk-bf-scale-%06d", i),
			fmt.Sprintf("Scale VK %06d", i),
			rl,
		)
		store.CreateVirtualKeyInMemory(ctx, vk)
	}

	// Best of timingBudgetAttempts sweeps; each retry re-expires every rate
	// limit so the sweep does full work again, filtering host noise without
	// weakening the O(dueLimits x totalVKs) regression guard.
	maxPerReset := scaleTimingBudget(500 * time.Microsecond)
	var bestPerReset time.Duration
	for attempt := 0; attempt < timingBudgetAttempts; attempt++ {
		if attempt > 0 {
			for i := 0; i < vkCount; i++ {
				markRequestRateLimitExpired(store, fmt.Sprintf("scale-rl-%06d", i))
			}
		}
		start := time.Now()
		reset := store.ResetExpiredRateLimitsInMemory(ctx, true) // background/ticker path
		elapsed := time.Since(start)

		if len(reset) != vkCount {
			t.Fatalf("expected all %d expired rate limits to reset, got %d", vkCount, len(reset))
		}
		perReset := elapsed / vkCount
		if bestPerReset == 0 || perReset < bestPerReset {
			bestPerReset = perReset
		}
		if bestPerReset <= maxPerReset {
			break
		}
	}
	// Reset accuracy at scale: spot-check that counters actually zeroed.
	for _, i := range []int{0, vkCount / 2, vkCount - 1} {
		rl := store.LoadRateLimit(ctx, fmt.Sprintf("scale-rl-%06d", i))
		if rl.RequestCurrentUsage != 0 {
			t.Fatalf("rate limit %06d not zeroed after background reset: %d", i, rl.RequestCurrentUsage)
		}
	}

	if bestPerReset > maxPerReset {
		t.Fatalf("background reset cost %v per reset (budget %v) with %d VKs across %d attempts; cost scales linearly with VK count, so at 100k keys one sweep is roughly %v of CPU every 10s tick - the reference refresh needs an O(owners) index or one batched pass per tick",
			bestPerReset, maxPerReset, vkCount, timingBudgetAttempts,
			time.Duration(int64(bestPerReset)*int64(100_000/vkCount))*100_000)
	}
	t.Logf("background reset: %v per reset for %d per-VK rate limits", bestPerReset, vkCount)
}

// seed100kBudgetVK creates one VK owning one budget with the given duration,
// alignment and last-reset, returning the budget ID.
func seed100kBudgetVK(ctx context.Context, store *LocalGovernanceStore, i int, duration string, calendarAligned bool, lastReset time.Time, usage float64) string {
	budgetID := fmt.Sprintf("acc-budget-%06d", i)
	budget := buildBudgetWithUsage(budgetID, 1_000_000, usage, duration)
	budget.LastReset = lastReset
	vk := buildVirtualKey(
		fmt.Sprintf("acc-vk-%06d", i),
		fmt.Sprintf("sk-bf-acc-%06d", i),
		fmt.Sprintf("Accuracy VK %06d", i),
		true,
	)
	// Alignment lives on the OWNER: CreateVirtualKeyInMemory stamps
	// IsCalendarAligned onto stored budgets from vk.CalendarAligned, exactly
	// like production config loading. Setting it on the budget alone is lost.
	vk.CalendarAligned = calendarAligned
	vk.Budgets = []configstoreTables.TableBudget{*budget}
	store.CreateVirtualKeyInMemory(ctx, vk)
	return budgetID
}

// TestBudgetResetAccuracyAt100kKeysAllDue seeds 100k VKs, every one owning a
// budget on a proper calendar-scale duration (1d/1w/1M, alternating rolling
// and calendar-aligned), all expired, then runs ONE background sweep and
// verifies reset accuracy on every single key: usage zeroed, rolling
// LastReset advanced past the stale value, calendar LastReset snapped exactly
// to the period start, and the embedded VK reference refreshed.
func TestBudgetResetAccuracyAt100kKeysAllDue(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStoreForResetBenchmark()
	const keyCount = 100_000
	durations := []string{"1d", "1w", "1M"}
	// Stale offsets guaranteed expired for both rolling and calendar semantics.
	staleOffsets := map[string]time.Duration{
		"1d": -25 * time.Hour,
		"1w": -8 * 24 * time.Hour,
		"1M": -31 * 24 * time.Hour,
	}

	seedNow := time.Now()
	staleResets := make([]time.Time, keyCount)
	for i := 0; i < keyCount; i++ {
		duration := durations[i%3]
		stale := seedNow.Add(staleOffsets[duration])
		staleResets[i] = stale
		seed100kBudgetVK(ctx, store, i, duration, i%2 == 0, stale, float64(i%997)+1)
	}

	// Best of timingBudgetAttempts sweeps; each retry re-stales every budget so
	// the sweep does full work again, filtering host noise on slow runners.
	sweepBudget := scaleTimingBudget(10 * time.Second)
	var bestElapsed time.Duration
	var beforeSweep, afterSweep time.Time
	for attempt := 0; attempt < timingBudgetAttempts; attempt++ {
		if attempt > 0 {
			for i := 0; i < keyCount; i++ {
				id := fmt.Sprintf("acc-budget-%06d", i)
				raw, ok := store.budgets.Load(id)
				if !ok || raw == nil {
					t.Fatalf("budget %06d missing before retry", i)
				}
				clone := *(raw.(*configstoreTables.TableBudget))
				clone.LastReset = staleResets[i]
				clone.CurrentUsage = float64(i%997) + 1
				store.budgets.Store(id, &clone)
			}
		}
		beforeSweep = time.Now()
		reset := store.ResetExpiredBudgetsInMemory(ctx, true)
		elapsed := time.Since(beforeSweep)
		afterSweep = time.Now()

		if len(reset) != keyCount {
			t.Fatalf("expected all %d budgets to reset, got %d", keyCount, len(reset))
		}
		if bestElapsed == 0 || elapsed < bestElapsed {
			bestElapsed = elapsed
		}
		if bestElapsed <= sweepBudget {
			break
		}
	}
	for i := 0; i < keyCount; i++ {
		duration := durations[i%3]
		budget := store.LoadBudget(ctx, fmt.Sprintf("acc-budget-%06d", i))
		if budget == nil {
			t.Fatalf("budget %06d missing after sweep", i)
		}
		if budget.CurrentUsage != 0 {
			t.Fatalf("budget %06d (%s) usage not zeroed: %f", i, duration, budget.CurrentUsage)
		}
		if i%2 == 0 {
			// Calendar-aligned: LastReset must snap exactly to the period start
			// (computed before or after the sweep - identical unless the sweep
			// straddles a period boundary).
			want1 := configstoreTables.GetCalendarPeriodStart(duration, beforeSweep)
			want2 := configstoreTables.GetCalendarPeriodStart(duration, afterSweep)
			if !budget.LastReset.Equal(want1) && !budget.LastReset.Equal(want2) {
				t.Fatalf("budget %06d (%s, calendar) LastReset %v, want period start %v", i, duration, budget.LastReset, want1)
			}
		} else if !budget.LastReset.After(staleResets[i]) {
			t.Fatalf("budget %06d (%s, rolling) LastReset did not advance: %v", i, duration, budget.LastReset)
		}
		rawVK, ok := store.virtualKeys.Load(fmt.Sprintf("sk-bf-acc-%06d", i))
		if !ok || rawVK == nil {
			t.Fatalf("vk %06d missing after sweep", i)
		}
		vk := rawVK.(*configstoreTables.TableVirtualKey)
		if len(vk.Budgets) != 1 || vk.Budgets[0].CurrentUsage != 0 {
			t.Fatalf("vk %06d embedded budget not refreshed: %+v", i, vk.Budgets)
		}
	}
	t.Logf("dense sweep: %d budgets reset in %v (%v per reset)", keyCount, bestElapsed, bestElapsed/keyCount)
	if bestElapsed > sweepBudget {
		t.Fatalf("dense 100k sweep took %v (budget %v) across %d attempts; the background tick budget is 10s", bestElapsed, sweepBudget, timingBudgetAttempts)
	}
}

// TestBudgetResetAccuracyAt100kKeysSparse covers the sparse structure: 100k
// keys where 10%% own no budget at all, and only ~1%% of the budgeted keys are
// actually due. The sweep must reset exactly the due set, leave every
// untouched budget byte-identical in usage and LastReset, and stay fast.
func TestBudgetResetAccuracyAt100kKeysSparse(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStoreForResetBenchmark()
	const keyCount = 100_000

	seedNow := time.Now()
	staleWeek := seedNow.Add(-8 * 24 * time.Hour)
	freshResets := make([]time.Time, keyCount)
	dueCount := 0
	for i := 0; i < keyCount; i++ {
		if i%10 == 7 { // sparse ownership: every 10th key has no budget
			vk := buildVirtualKey(
				fmt.Sprintf("acc-vk-%06d", i),
				fmt.Sprintf("sk-bf-acc-%06d", i),
				fmt.Sprintf("Accuracy VK %06d", i),
				true,
			)
			store.CreateVirtualKeyInMemory(ctx, vk)
			continue
		}
		if i%100 == 0 { // ~1% due: rolling weekly, expired
			seed100kBudgetVK(ctx, store, i, "1w", false, staleWeek, 500)
			dueCount++
			continue
		}
		// Not due: rolling weekly, reset recently, distinct usage per key.
		fresh := seedNow.Add(-time.Duration(i%5000+1) * time.Minute)
		freshResets[i] = fresh
		seed100kBudgetVK(ctx, store, i, "1w", false, fresh, float64(i%997)+1)
	}

	// Best of timingBudgetAttempts sweeps; each retry re-stales only the due
	// set so the sweep does the same work again, filtering host noise.
	sweepBudget := scaleTimingBudget(5 * time.Second)
	var bestElapsed time.Duration
	for attempt := 0; attempt < timingBudgetAttempts; attempt++ {
		if attempt > 0 {
			for i := 0; i < keyCount; i += 100 {
				id := fmt.Sprintf("acc-budget-%06d", i)
				raw, ok := store.budgets.Load(id)
				if !ok || raw == nil {
					t.Fatalf("budget %06d missing before retry", i)
				}
				clone := *(raw.(*configstoreTables.TableBudget))
				clone.LastReset = staleWeek
				clone.CurrentUsage = 500
				store.budgets.Store(id, &clone)
			}
		}
		start := time.Now()
		reset := store.ResetExpiredBudgetsInMemory(ctx, true)
		elapsed := time.Since(start)

		if len(reset) != dueCount {
			t.Fatalf("expected exactly %d due budgets to reset, got %d", dueCount, len(reset))
		}
		if bestElapsed == 0 || elapsed < bestElapsed {
			bestElapsed = elapsed
		}
		if bestElapsed <= sweepBudget {
			break
		}
	}
	for i := 0; i < keyCount; i++ {
		if i%10 == 7 {
			continue
		}
		budget := store.LoadBudget(ctx, fmt.Sprintf("acc-budget-%06d", i))
		if budget == nil {
			t.Fatalf("budget %06d missing after sweep", i)
		}
		if i%100 == 0 {
			if budget.CurrentUsage != 0 || !budget.LastReset.After(staleWeek) {
				t.Fatalf("due budget %06d not reset: usage=%f lastReset=%v", i, budget.CurrentUsage, budget.LastReset)
			}
			continue
		}
		if wantUsage := float64(i%997) + 1; budget.CurrentUsage != wantUsage {
			t.Fatalf("untouched budget %06d usage changed: got %f want %f", i, budget.CurrentUsage, wantUsage)
		}
		if !budget.LastReset.Equal(freshResets[i]) {
			t.Fatalf("untouched budget %06d LastReset changed: got %v want %v", i, budget.LastReset, freshResets[i])
		}
	}
	t.Logf("sparse sweep: %d/%d budgets reset in %v", dueCount, keyCount, bestElapsed)
	if bestElapsed > sweepBudget {
		t.Fatalf("sparse 100k sweep took %v (budget %v) across %d attempts; expected well under the 10s tick", bestElapsed, sweepBudget, timingBudgetAttempts)
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
