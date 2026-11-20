package server

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// The vector budget (client_config.vector_max_size_mb) bounds the estimated
// size of every vector built from logs, together: Warp's log index and every
// complexity router's captured requests. It is enforced by one hourly pass
// here rather than by each feature, since only one place sees all of them and
// can delete the oldest first across them (see vectorstore.EnforceBudget).
// Every node runs the pass; they find the same cutoff, and a delete of what
// another node already deleted is a no-op. It never runs on the request path.

const (
	vectorBudgetInterval   = time.Hour
	vectorBudgetFirstDelay = 5 * time.Minute // off the boot path
	vectorBudgetTimeout    = 10 * time.Minute
)

// vectorBudgetBytes reads the budget now, in bytes; 0 is none. Read at each
// pass, since it can change at runtime.
func (s *BifrostHTTPServer) vectorBudgetBytes() int64 {
	if s.Config == nil {
		return 0
	}
	s.Config.Mu.RLock()
	defer s.Config.Mu.RUnlock()
	if s.Config.ClientConfig == nil {
		return 0
	}
	return int64(max(s.Config.ClientConfig.VectorMaxSizeMB, 0)) * 1024 * 1024
}

// vectorBudgetIndexes is every log-derived index: Warp's, when it has one, and
// every capture namespace in the shared store. The captures are listed from the
// store, not asked of the routing plugin, so those of a router another node
// serves, or of a routing plugin that is off, still count.
func (s *BifrostHTTPServer) vectorBudgetIndexes(ctx context.Context) ([]vectorstore.BudgetIndex, error) {
	var indexes []vectorstore.BudgetIndex
	if s.WarpHandler != nil {
		indexes = append(indexes, s.WarpHandler.Service().VectorBudgetIndexes(ctx)...)
	}
	if s.Config == nil {
		return indexes, nil
	}
	store := complexity.CaptureStore(s.Config.VectorStore)
	if store == nil {
		return indexes, nil
	}
	namespaces, err := complexity.ListCaptureNamespaces(ctx, store)
	if err != nil {
		return nil, err
	}
	for _, ns := range namespaces {
		if _, ok := complexity.CaptureNamespaceRouterKey(ns); ok {
			indexes = append(indexes, complexity.CaptureBudgetIndex(store, ns))
		}
	}
	return indexes, nil
}

// enforceVectorBudget runs one pass. A pass that fails is logged and left for
// the next: the budget is a bound on growth, and an hour over it is no harm.
func (s *BifrostHTTPServer) enforceVectorBudget(ctx context.Context, now time.Time) {
	budget := s.vectorBudgetBytes()
	if budget <= 0 {
		return
	}
	passCtx, cancel := context.WithTimeout(ctx, vectorBudgetTimeout)
	defer cancel()
	indexes, err := s.vectorBudgetIndexes(passCtx)
	if err != nil {
		logger.Warn("vector budget: failed to list the indexes it covers, so nothing was checked this hour: %v", err)
		return
	}
	result, err := vectorstore.EnforceBudget(passCtx, budget, indexes, now)
	for _, skipped := range result.Skipped {
		logger.Warn("vector budget: left out of this hour's estimate, and not trimmed, because it could not be counted: %s", skipped)
	}
	if err != nil {
		logger.Warn("vector budget: vectors built from logs are estimated at %d MB against a budget of %d MB, and trimming them failed: %v", result.EstimatedBytes>>20, budget>>20, err)
		return
	}
	if result.Deleted > 0 {
		logger.Info("vector budget: vectors built from logs were estimated at %d MB, over the %d MB budget; deleted %d vector(s) older than %s", result.EstimatedBytes>>20, budget>>20, result.Deleted, result.Cutoff.Format(time.RFC3339))
	}
}

// startVectorBudget runs the pass every hour until ctx ends.
func (s *BifrostHTTPServer) startVectorBudget(ctx context.Context) {
	go func() {
		timer := time.NewTimer(vectorBudgetFirstDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.enforceVectorBudget(ctx, time.Now().UTC())
				timer.Reset(vectorBudgetInterval)
			}
		}
	}()
}
