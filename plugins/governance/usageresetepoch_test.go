package governance

import (
	"context"
	"testing"
	"time"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// TestUsageResetEpochsIgnoreAdoption verifies retained usage never produces reset
// evidence, while actual resets publish evidence before hooks run.
func TestUsageResetEpochsIgnoreAdoption(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStoreForResetBenchmark()
	now := time.Date(2026, 2, 5, 12, 0, 0, 0, time.UTC)
	old := time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)
	budget := buildBudgetWithUsage("budget", 100, 42, "1M")
	budget.LastReset, budget.CreatedAt = old, old
	store.budgets.Store(budget.ID, budget)
	rate := buildRateLimit("rate", 1000, 1000)
	month := "1M"
	rate.TokenResetDuration, rate.RequestResetDuration = &month, &month
	rate.TokenLastReset, rate.RequestLastReset, rate.CreatedAt = old, old, old
	rate.TokenCurrentUsage, rate.RequestCurrentUsage = 17, 19
	store.rateLimits.Store(rate.ID, rate)
	if !store.AdoptCalendarAlignmentInMemory(ctx, budget.ID, now) || !store.AdoptRateLimitCalendarAlignmentInMemory(ctx, rate.ID, now) {
		t.Fatal("expected calendar adoption")
	}
	if store.LoadBudget(ctx, budget.ID).UsageResetEpoch != 0 {
		t.Fatal("adoption recorded budget reset")
	}
	tokens, requests := store.LoadRateLimit(ctx, rate.ID).TokenUsageResetEpoch, store.LoadRateLimit(ctx, rate.ID).RequestUsageResetEpoch
	if tokens != 0 || requests != 0 {
		t.Fatal("adoption recorded rate reset")
	}
	target := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	reset, ok := store.ResetRateLimitAt(ctx, rate.ID, &target, nil)
	if !ok || reset.TokenCurrentUsage != 0 || reset.RequestCurrentUsage != 19 {
		t.Fatal("token reset changed retained requests")
	}
	// Immutable snapshots remain readable under archive baseline locks.
	store.LastDBUsagesRateLimitsRequestsMu.Lock()
	store.LastDBUsagesRateLimitsTokensMu.Lock()
	tokens, requests = store.LoadRateLimit(ctx, rate.ID).TokenUsageResetEpoch, store.LoadRateLimit(ctx, rate.ID).RequestUsageResetEpoch
	store.LastDBUsagesRateLimitsTokensMu.Unlock()
	store.LastDBUsagesRateLimitsRequestsMu.Unlock()
	if tokens != target.UnixNano() || requests != 0 {
		t.Fatalf("independent evidence = %v, %v", tokens, requests)
	}
	if _, ok := store.ResetBudgetAt(ctx, budget.ID, target); !ok {
		t.Fatal("expected budget reset")
	}
	store.LastDBUsagesBudgetsMu.Lock()
	epoch := store.LoadBudget(ctx, budget.ID).UsageResetEpoch
	store.LastDBUsagesBudgetsMu.Unlock()
	if epoch != target.UnixNano() {
		t.Fatalf("budget evidence = %v", epoch)
	}
	store.UpsertBudgetConfig(ctx, budget.ID, budget)
	store.UpsertRateLimitConfig(ctx, rate.ID, rate)
	if store.LoadBudget(ctx, budget.ID).UsageResetEpoch != target.UnixNano() {
		t.Fatal("config replacement lost evidence")
	}
	tokens, requests = store.LoadRateLimit(ctx, rate.ID).TokenUsageResetEpoch, store.LoadRateLimit(ctx, rate.ID).RequestUsageResetEpoch
	if tokens != target.UnixNano() || requests != 0 {
		t.Fatal("config replacement changed rate evidence")
	}
	store.DeleteBudget(ctx, budget.ID)
	store.DeleteRateLimit(ctx, rate.ID)
	if store.LoadBudget(ctx, budget.ID) != nil {
		t.Fatal("deleted budget retained evidence")
	}
	if store.LoadRateLimit(ctx, rate.ID) != nil {
		t.Fatal("deleted rate retained evidence")
	}
}

// TestUsageResetEpochsPublishedBeforeHooks covers normal expiry commits and
// verifies a rate hook cannot mistake an untouched request boundary for a reset.
func TestUsageResetEpochsPublishedBeforeHooks(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStoreForResetBenchmark()
	old := time.Now().Add(-2 * time.Hour)
	budget := buildBudgetWithUsage("budget", 100, 42, "1h")
	budget.LastReset = old
	store.budgets.Store(budget.ID, budget)
	rate := buildRateLimit("rate", 1000, 1000)
	rate.TokenLastReset = old
	rate.RequestLastReset = time.Now()
	store.rateLimits.Store(rate.ID, rate)
	budgetHook, rateHook := false, false
	store.SetResetHooks(func(rows []*configstoreTables.TableBudget) {
		budgetHook = true
		if store.LoadBudget(ctx, rows[0].ID).UsageResetEpoch != rows[0].LastReset.UnixNano() {
			t.Fatal("hook preceded evidence")
		}
	}, func(rows []*configstoreTables.TableRateLimit) {
		rateHook = true
		tokens, requests := store.LoadRateLimit(ctx, rows[0].ID).TokenUsageResetEpoch, store.LoadRateLimit(ctx, rows[0].ID).RequestUsageResetEpoch
		if tokens != rows[0].TokenLastReset.UnixNano() || requests != 0 {
			t.Fatal("hook has incorrect dimension evidence")
		}
	})
	store.ResetExpiredBudgetsInMemory(ctx, false, budget.ID)
	store.ResetExpiredRateLimitsInMemory(ctx, false, rate.ID)
	if !budgetHook || !rateHook {
		t.Fatal("expected reset hooks")
	}
}

// TestUsageResetEpochsReloadAndOperatorReset checks evidence follows resource
// lifetime through full reloads. Operator clears preserve window reset evidence.
func TestUsageResetEpochsReloadAndOperatorReset(t *testing.T) {
	ctx := context.Background()
	store := newStandaloneStoreForResetBenchmark()
	old := time.Now().Add(-time.Hour)
	target := old.Add(time.Minute)
	budget := buildBudgetWithUsage("budget", 100, 42, "1h")
	budget.LastReset = old
	rate := buildRateLimit("rate", 100, 100)
	rate.TokenLastReset, rate.RequestLastReset = old, old
	store.budgets.Store(budget.ID, budget)
	store.rateLimits.Store(rate.ID, rate)
	reset, ok := store.ResetBudgetAt(ctx, budget.ID, target)
	if !ok || store.LoadBudget(ctx, budget.ID).UsageResetEpoch != target.UnixNano() {
		t.Fatal("window reset must publish evidence")
	}
	if err := store.BumpBudgetUsageBy(ctx, budget.ID, 9); err != nil {
		t.Fatal(err)
	}
	reset, ok = store.ResetBudgetUsageInMemory(ctx, budget.ID)
	if !ok || reset.CurrentUsage != 0 || !reset.LastReset.Equal(target) || store.LoadBudget(ctx, budget.ID).UsageResetEpoch != target.UnixNano() {
		t.Fatal("operator clear must preserve the window and its reset evidence")
	}
	if _, ok := store.ResetRateLimitAt(ctx, rate.ID, nil, &target); !ok {
		t.Fatal("expected request reset")
	}
	tokens, requests := store.LoadRateLimit(ctx, rate.ID).TokenUsageResetEpoch, store.LoadRateLimit(ctx, rate.ID).RequestUsageResetEpoch
	if tokens != 0 || requests != target.UnixNano() {
		t.Fatal("request-only reset changed token evidence")
	}
	if _, ok := store.ResetRateLimitAt(ctx, rate.ID, nil, &old); ok {
		t.Fatal("stale reset succeeded")
	}
	store.rebuildInMemoryStructures(ctx, nil, nil, nil, []configstoreTables.TableBudget{*reset}, []configstoreTables.TableRateLimit{*store.LoadRateLimit(ctx, rate.ID)}, nil, nil)
	if store.LoadBudget(ctx, budget.ID).UsageResetEpoch != target.UnixNano() {
		t.Fatal("reload lost budget evidence")
	}
	tokens, requests = store.LoadRateLimit(ctx, rate.ID).TokenUsageResetEpoch, store.LoadRateLimit(ctx, rate.ID).RequestUsageResetEpoch
	if tokens != 0 || requests != target.UnixNano() {
		t.Fatal("reload changed rate evidence")
	}
	store.rebuildInMemoryStructures(ctx, nil, nil, nil, nil, nil, nil, nil)
	if store.LoadBudget(ctx, budget.ID) != nil {
		t.Fatal("reload retained removed budget evidence")
	}
	if store.LoadRateLimit(ctx, rate.ID) != nil {
		t.Fatal("reload retained removed rate evidence")
	}
}
