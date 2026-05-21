package semanticcache

import (
	"time"
)

// cacheState holds per-request state for the semantic cache plugin. It's
// keyed by the request ID and lives between PreLLMHook (where it's populated)
// and PostLLMHook (where it's consumed and cleared).
//
// Centralizes what used to be a set of stringly-typed BifrostContext keys
// (directCacheID, paramsHash, embeddings, embedding input tokens) into one
// struct so the lifecycle is explicit and consumers don't have to chase
// ctx.Value/SetValue calls across files.
//
// No mutex is needed: per-request access is serialized — PreLLMHook runs once,
// PostLLMHook runs once per chunk in order, and the only async path
// (PostLLMHook's storage goroutine) snapshots the values it needs into locals
// before launching.
type cacheState struct {
	DirectCacheID         string
	ParamsHash            string
	Embeddings            []float32
	EmbeddingsInputTokens int

	// FilteredInput caches getInputForCaching(req) so attachment extraction,
	// embedding text extraction, and history-threshold checks reuse the same
	// filtered slice instead of re-filtering on each call.
	FilteredInput interface{}

	// ShortCircuited is set when PreLLMHook served the response from cache
	// (returned a non-nil LLMPluginShortCircuit). PostLLMHook uses this to
	// skip the entire cache-write path: only the FINAL replay chunk carries
	// CacheDebug.CacheHit=true, so shouldSkipCaching() can't catch the
	// non-final chunks on its own — without this flag they'd flow into
	// addStreamingResponse and trigger a duplicate write at the same
	// directCacheID (Weaviate 422 "id already exists").
	ShortCircuited bool

	CreatedAt time.Time
}

// cacheStateMaxAge bounds how long an orphaned cacheState may live in memory
// before being reaped.
const cacheStateMaxAge = 60 * time.Minute

// cacheStateCleanupInterval bounds the worst-case staleness of an orphaned
// state to ~maxAge + interval.
const cacheStateCleanupInterval = 5 * time.Minute

// createCacheState writes a fresh state for requestID, overwriting any prior.
// PreLLMHook calls this at the top so retries / reused requestIDs don't
// inherit stale fields.
func (p *Plugin) createCacheState(requestID string) *cacheState {
	state := &cacheState{CreatedAt: time.Now()}
	p.cacheStates.Store(requestID, state)
	return state
}

// getCacheState returns the cacheState for requestID, or nil if none exists.
func (p *Plugin) getCacheState(requestID string) *cacheState {
	if v, ok := p.cacheStates.Load(requestID); ok {
		return v.(*cacheState)
	}
	return nil
}

// clearCacheState drops the cacheState entry for requestID. It's safe to call
// when no entry exists.
func (p *Plugin) clearCacheState(requestID string) {
	p.cacheStates.Delete(requestID)
}

// runCacheStateCleanupLoop reaps stale cacheStates on a ticker until stopCh
// is closed. Started by Init, stopped by Cleanup.
func (p *Plugin) runCacheStateCleanupLoop() {
	defer p.cleanupWg.Done()
	ticker := time.NewTicker(cacheStateCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.cleanupOldCacheStates()
		}
	}
}

// cleanupOldCacheStates deletes every cacheState whose CreatedAt is older
// than cacheStateMaxAge. Entries this old indicate a request that never
// reached PostLLMHook (client disconnect, framework bug); reaping them
// bounds memory under abnormal traffic.
func (p *Plugin) cleanupOldCacheStates() {
	cutoff := time.Now().Add(-cacheStateMaxAge)
	var toDelete []string
	p.cacheStates.Range(func(key, value interface{}) bool {
		state := value.(*cacheState)
		if state.CreatedAt.Before(cutoff) {
			toDelete = append(toDelete, key.(string))
		}
		return true
	})
	for _, k := range toDelete {
		p.cacheStates.Delete(k)
	}
	if len(toDelete) > 0 {
		p.logger.Debug("Reaped %d stale cache states", len(toDelete))
	}
}
