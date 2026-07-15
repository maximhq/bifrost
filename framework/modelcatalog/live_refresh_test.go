package modelcatalog

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func ptrInt64(v int64) *int64 { return &v }

// TestSetLiveRefreshInterval_EnableDisable covers the sentinel semantics: nil
// or <= 0 removes the schedule entirely (today's reactive-only behavior),
// anything > 0 enables (or updates) it.
func TestSetLiveRefreshInterval_EnableDisable(t *testing.T) {
	mc := NewTestCatalog(nil)

	mc.SetLiveRefreshInterval(schemas.OpenAI, ptrInt64(60))
	mc.liveTickers.mu.Lock()
	entry, ok := mc.liveTickers.entries[schemas.OpenAI]
	mc.liveTickers.mu.Unlock()
	if !ok || entry.interval != 60*time.Second {
		t.Fatalf("expected 60s interval entry, got %+v (ok=%v)", entry, ok)
	}

	// Updating the interval on an existing entry preserves lastFetchedAt.
	mc.liveTickers.mu.Lock()
	mc.liveTickers.entries[schemas.OpenAI].lastFetchedAt = time.Unix(1000, 0)
	mc.liveTickers.mu.Unlock()
	mc.SetLiveRefreshInterval(schemas.OpenAI, ptrInt64(120))
	mc.liveTickers.mu.Lock()
	entry = mc.liveTickers.entries[schemas.OpenAI]
	mc.liveTickers.mu.Unlock()
	if entry.interval != 120*time.Second || entry.lastFetchedAt != time.Unix(1000, 0) {
		t.Fatalf("expected interval updated to 120s with lastFetchedAt preserved, got %+v", entry)
	}

	// nil disables.
	mc.SetLiveRefreshInterval(schemas.OpenAI, nil)
	mc.liveTickers.mu.Lock()
	_, ok = mc.liveTickers.entries[schemas.OpenAI]
	mc.liveTickers.mu.Unlock()
	if ok {
		t.Fatalf("expected entry removed after nil interval")
	}

	// <= 0 disables too.
	mc.SetLiveRefreshInterval(schemas.Anthropic, ptrInt64(60))
	mc.SetLiveRefreshInterval(schemas.Anthropic, ptrInt64(0))
	mc.liveTickers.mu.Lock()
	_, ok = mc.liveTickers.entries[schemas.Anthropic]
	mc.liveTickers.mu.Unlock()
	if ok {
		t.Fatalf("expected entry removed after 0 interval")
	}
}

// TestSetLiveRefreshInterval_SeedsLastFetchedAtOnCreate verifies a brand-new
// entry's lastFetchedAt is seeded to "now", not left at the zero value —
// otherwise dueProviders would treat it as immediately due and duplicate the
// reactive fetch that the same call site (boot seeding / ReloadProvider)
// already triggers.
func TestSetLiveRefreshInterval_SeedsLastFetchedAtOnCreate(t *testing.T) {
	mc := NewTestCatalog(nil)
	before := time.Now()
	mc.SetLiveRefreshInterval(schemas.OpenAI, ptrInt64(60))
	after := time.Now()

	mc.liveTickers.mu.Lock()
	lastFetchedAt := mc.liveTickers.entries[schemas.OpenAI].lastFetchedAt
	mc.liveTickers.mu.Unlock()

	if lastFetchedAt.IsZero() {
		t.Fatal("expected lastFetchedAt to be seeded on creation, got zero value")
	}
	if lastFetchedAt.Before(before) || lastFetchedAt.After(after) {
		t.Fatalf("expected lastFetchedAt within [%v, %v], got %v", before, after, lastFetchedAt)
	}
}

// TestRemoveLiveRefreshConfig_DropsSchedule covers the provider-delete path.
func TestRemoveLiveRefreshConfig_DropsSchedule(t *testing.T) {
	mc := NewTestCatalog(nil)
	mc.SetLiveRefreshInterval(schemas.OpenAI, ptrInt64(60))
	mc.RemoveLiveRefreshConfig(schemas.OpenAI)
	mc.liveTickers.mu.Lock()
	_, ok := mc.liveTickers.entries[schemas.OpenAI]
	mc.liveTickers.mu.Unlock()
	if ok {
		t.Fatalf("expected entry removed after RemoveLiveRefreshConfig")
	}
}

// TestDueProviders_RespectsInterval verifies due-ness math: not due before the
// interval elapses, due once it has, and never-fetched (zero lastFetchedAt)
// counts as immediately due.
func TestDueProviders_RespectsInterval(t *testing.T) {
	tm := newLiveRefreshTickerMap()
	base := time.Unix(10_000, 0)
	tm.entries[schemas.OpenAI] = &liveRefreshEntry{interval: 100 * time.Second, lastFetchedAt: base}
	tm.entries[schemas.Anthropic] = &liveRefreshEntry{interval: 100 * time.Second} // zero value: never fetched

	due := tm.dueProviders(base.Add(50 * time.Second))
	if len(due) != 1 || due[0] != schemas.Anthropic {
		t.Fatalf("at +50s expected only never-fetched Anthropic due, got %v", due)
	}
	// Anthropic was marked fetching by the call above; reset for the next check.
	tm.markFetchDone(schemas.Anthropic, base)

	due = tm.dueProviders(base.Add(150 * time.Second))
	gotSet := map[schemas.ModelProvider]bool{}
	for _, p := range due {
		gotSet[p] = true
	}
	if !gotSet[schemas.OpenAI] {
		t.Errorf("at +150s expected OpenAI due (interval elapsed), got %v", due)
	}
}

// TestDueProviders_MarksFetchingToPreventOverlap ensures a provider already
// marked in-flight is not returned again by a concurrent/subsequent due-check
// before markFetchDone runs — the guard against overlapping fetches when one
// take longer than the tick period.
func TestDueProviders_MarksFetchingToPreventOverlap(t *testing.T) {
	tm := newLiveRefreshTickerMap()
	now := time.Now()
	tm.entries[schemas.OpenAI] = &liveRefreshEntry{interval: time.Second, lastFetchedAt: now.Add(-time.Hour)}

	first := tm.dueProviders(now)
	if len(first) != 1 || first[0] != schemas.OpenAI {
		t.Fatalf("expected OpenAI due on first check, got %v", first)
	}

	second := tm.dueProviders(now)
	if len(second) != 0 {
		t.Fatalf("expected no providers due while a fetch is in flight, got %v", second)
	}

	tm.markFetchDone(schemas.OpenAI, now)
	third := tm.dueProviders(now.Add(2 * time.Second))
	if len(third) != 1 || third[0] != schemas.OpenAI {
		t.Fatalf("expected OpenAI due again after markFetchDone + interval elapsed, got %v", third)
	}
}

// TestRunLiveRefreshTick_InvokesHookForDueProviders is the end-to-end wiring
// test: a provider with an elapsed interval gets its hook invoked exactly
// once, and lastFetchedAt is updated so the same tick isn't re-triggered.
func TestRunLiveRefreshTick_InvokesHookForDueProviders(t *testing.T) {
	mc := NewTestCatalog(nil)
	mc.SetLiveRefreshInterval(schemas.OpenAI, ptrInt64(30))
	// New entries seed lastFetchedAt to "now" (the caller just did a reactive
	// fetch), so it isn't immediately due. Backdate it to simulate the
	// interval having elapsed since that seed.
	mc.liveTickers.mu.Lock()
	mc.liveTickers.entries[schemas.OpenAI].lastFetchedAt = time.Now().Add(-time.Hour)
	mc.liveTickers.mu.Unlock()

	var mu sync.Mutex
	var calledFor []schemas.ModelProvider
	mc.SetLiveRefreshHook(func(_ context.Context, provider schemas.ModelProvider) {
		mu.Lock()
		calledFor = append(calledFor, provider)
		mu.Unlock()
	})

	mc.runLiveRefreshTick(context.Background())

	// Wait for mc.wg (not just the hook returning) so the goroutine's own
	// deferred markFetchDone has definitely run before we assert on it below
	// — the hook firing and markFetchDone running are two separate steps in
	// the same goroutine, and the hook returning doesn't imply the deferred
	// call has completed yet.
	done := make(chan struct{})
	go func() {
		mc.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for live refresh hook to finish")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calledFor) != 1 || calledFor[0] != schemas.OpenAI {
		t.Fatalf("expected hook invoked once for OpenAI, got %v", calledFor)
	}

	mc.liveTickers.mu.Lock()
	entry := mc.liveTickers.entries[schemas.OpenAI]
	mc.liveTickers.mu.Unlock()
	if entry.fetching {
		t.Errorf("expected fetching=false after hook completes")
	}
	if entry.lastFetchedAt.IsZero() {
		t.Errorf("expected lastFetchedAt set after hook completes")
	}
}

// TestRunLiveRefreshTick_NilHookIsNoop covers composers built without the
// HTTP server wiring (e.g. NewTestCatalog callers) — must not panic.
func TestRunLiveRefreshTick_NilHookIsNoop(t *testing.T) {
	mc := NewTestCatalog(nil)
	mc.SetLiveRefreshInterval(schemas.OpenAI, ptrInt64(30))
	mc.runLiveRefreshTick(context.Background()) // must not panic
}

// TestRunLiveRefreshTick_SkipsProvidersNotDue ensures a provider with a
// not-yet-elapsed interval is left alone.
func TestRunLiveRefreshTick_SkipsProvidersNotDue(t *testing.T) {
	mc := NewTestCatalog(nil)
	mc.SetLiveRefreshInterval(schemas.OpenAI, ptrInt64(3600))
	mc.liveTickers.mu.Lock()
	mc.liveTickers.entries[schemas.OpenAI].lastFetchedAt = time.Now()
	mc.liveTickers.mu.Unlock()

	called := false
	mc.SetLiveRefreshHook(func(_ context.Context, _ schemas.ModelProvider) {
		called = true
	})
	mc.runLiveRefreshTick(context.Background())

	// Give any (unexpected) goroutine a moment to run before asserting.
	time.Sleep(50 * time.Millisecond)
	if called {
		t.Errorf("expected hook not invoked for a provider whose interval has not elapsed")
	}
}
