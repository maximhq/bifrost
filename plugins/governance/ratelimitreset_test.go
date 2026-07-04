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
