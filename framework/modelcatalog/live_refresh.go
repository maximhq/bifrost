package modelcatalog

import (
	"context"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// liveRefreshTickerPeriod is the check granularity for the periodic live
// list-models refresher — deliberately its own ticker, independent of the
// pricing/MCP-library syncTicker (syncWorkerTickerPeriod = 5m). Accepted
// list_models_refresh_interval_sec values go down to
// minListModelsRefreshIntervalSec (30s, see transports/bifrost-http/handlers/providers.go),
// so piggybacking on the 5-minute ticker would make any interval under 5
// minutes unable to fire on schedule.
const liveRefreshTickerPeriod = 10 * time.Second

// liveRefreshEntry tracks the periodic-refresh schedule for one provider's
// live list-models cache. Providers with no entry (or interval <= 0) keep the
// pre-existing reactive-only behavior (refreshed on boot / provider add-update
// / key add-update, never on a timer).
type liveRefreshEntry struct {
	interval      time.Duration
	lastFetchedAt time.Time
	fetching      bool
}

// liveRefreshTickerMap is the per-provider schedule state for the periodic
// live-models refresher. Kept on ModelCatalog (the composer) rather than on
// live.Store, matching the existing pattern where live.Store stays a passive
// cache and all scheduling lives in the composer (see syncTick for the
// pricing/MCP-library equivalent).
type liveRefreshTickerMap struct {
	mu      sync.Mutex
	entries map[schemas.ModelProvider]*liveRefreshEntry
}

func newLiveRefreshTickerMap() *liveRefreshTickerMap {
	return &liveRefreshTickerMap{entries: make(map[schemas.ModelProvider]*liveRefreshEntry)}
}

// SetLiveRefreshHook registers the callback the ticker invokes for a due
// provider. The HTTP server wires this to RefreshLiveModelsForProvider (with
// the provider's current enabled keys) once s.Client exists — the composer
// itself has no access to the Bifrost client or provider key list.
func (mc *ModelCatalog) SetLiveRefreshHook(fn func(ctx context.Context, provider schemas.ModelProvider)) {
	mc.liveRefreshMu.Lock()
	defer mc.liveRefreshMu.Unlock()
	mc.liveRefreshHook = fn
}

// SetLiveRefreshInterval enables (interval > 0) or disables (nil or <= 0)
// periodic live-models refresh for a provider. Called whenever the server has
// a fresh provider config in hand: boot seeding, ReloadProvider (covers both
// add and update), and provider delete (with nil to remove the schedule).
func (mc *ModelCatalog) SetLiveRefreshInterval(provider schemas.ModelProvider, intervalSec *int64) {
	mc.liveTickers.mu.Lock()
	defer mc.liveTickers.mu.Unlock()
	if intervalSec == nil || *intervalSec <= 0 {
		delete(mc.liveTickers.entries, provider)
		return
	}
	interval := time.Duration(*intervalSec) * time.Second
	entry, ok := mc.liveTickers.entries[provider]
	if !ok {
		// Seed lastFetchedAt to now rather than the zero value: this call
		// site (boot seeding / ReloadProvider) always accompanies a reactive
		// fetch of its own, so treating the schedule as freshly satisfied
		// avoids the next tick immediately re-fetching a provider that was
		// just fetched moments ago.
		mc.liveTickers.entries[provider] = &liveRefreshEntry{interval: interval, lastFetchedAt: time.Now()}
		return
	}
	entry.interval = interval
}

// MarkLiveRefreshInFlight marks a provider's schedule as "fetching" before a
// reactive fetch outside the ticker begins (e.g. ReloadProvider). Without
// this, a slow reactive fetch that outlives the tick interval leaves the
// schedule looking idle, so the periodic ticker can start a second, redundant
// concurrent fetch for the same provider before the reactive one finishes and
// calls NoteLiveRefreshCompleted. A no-op if the provider has no schedule.
func (mc *ModelCatalog) MarkLiveRefreshInFlight(provider schemas.ModelProvider) {
	mc.liveTickers.mu.Lock()
	defer mc.liveTickers.mu.Unlock()
	if entry, ok := mc.liveTickers.entries[provider]; ok {
		entry.fetching = true
	}
}

// NoteLiveRefreshCompleted resets a provider's schedule to "just fetched",
// pushing its next due time a full interval out. Callers that perform their
// own reactive fetch outside the ticker (e.g. ReloadProvider, which always
// refetches immediately on provider/key edits) must call this afterward —
// otherwise, if the schedule's old lastFetchedAt was already close to due,
// the ticker fires again moments later and duplicates the fetch that just
// happened. A no-op if the provider has no periodic schedule.
func (mc *ModelCatalog) NoteLiveRefreshCompleted(provider schemas.ModelProvider) {
	mc.liveTickers.markFetchDone(provider, time.Now())
}

// RemoveLiveRefreshConfig drops the provider's refresh schedule entirely.
// Called on provider delete.
func (mc *ModelCatalog) RemoveLiveRefreshConfig(provider schemas.ModelProvider) {
	mc.liveTickers.mu.Lock()
	defer mc.liveTickers.mu.Unlock()
	delete(mc.liveTickers.entries, provider)
}

// dueProviders returns the providers whose refresh interval has elapsed since
// their last fetch, and marks each as "fetching" so a slow fetch (many keys)
// can't be re-triggered by the next tick before it completes.
func (t *liveRefreshTickerMap) dueProviders(now time.Time) []schemas.ModelProvider {
	t.mu.Lock()
	defer t.mu.Unlock()
	var due []schemas.ModelProvider
	for provider, entry := range t.entries {
		if entry.fetching {
			continue
		}
		if entry.lastFetchedAt.IsZero() || now.Sub(entry.lastFetchedAt) >= entry.interval {
			entry.fetching = true
			due = append(due, provider)
		}
	}
	return due
}

// markFetchDone clears the in-flight flag and records the fetch time so the
// next due-check is measured from completion, not from when it started.
func (t *liveRefreshTickerMap) markFetchDone(provider schemas.ModelProvider, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if entry, ok := t.entries[provider]; ok {
		entry.fetching = false
		entry.lastFetchedAt = at
	}
}

// runLiveRefreshTick fires the registered hook for every provider whose
// interval is due, each in its own goroutine tracked by mc.wg (so Cleanup
// drains them) rather than blocking the caller — the pricing/MCP sync tick
// that calls this should not stall behind a slow provider fetch. A nil hook
// (composer used without the HTTP server, e.g. tests) makes this a no-op.
func (mc *ModelCatalog) runLiveRefreshTick(ctx context.Context) {
	mc.liveRefreshMu.RLock()
	hook := mc.liveRefreshHook
	mc.liveRefreshMu.RUnlock()
	if hook == nil {
		return
	}
	due := mc.liveTickers.dueProviders(time.Now())
	for _, provider := range due {
		mc.wg.Add(1)
		go func(p schemas.ModelProvider) {
			defer mc.wg.Done()
			// A plain `defer markFetchDone(p, time.Now())` would evaluate
			// time.Now() now, at goroutine start, not at completion — wrap in
			// a closure so it reflects when the fetch actually finished.
			defer func() { mc.liveTickers.markFetchDone(p, time.Now()) }()
			hook(ctx, p)
		}(provider)
	}
}

// startLiveRefreshWorker starts the dedicated ticker for periodic live
// list-models refresh. Runs independently of startSyncWorker's pricing/MCP
// ticker so short intervals (down to minListModelsRefreshIntervalSec) can
// actually fire on schedule.
func (mc *ModelCatalog) startLiveRefreshWorker(ctx context.Context) {
	mc.liveRefreshTicker = time.NewTicker(liveRefreshTickerPeriod)
	mc.wg.Add(1)
	go mc.liveRefreshWorker(ctx)
}

func (mc *ModelCatalog) liveRefreshWorker(ctx context.Context) {
	ticker := mc.liveRefreshTicker
	defer mc.wg.Done()
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			mc.runLiveRefreshTick(ctx)
		case <-mc.done:
			return
		}
	}
}
