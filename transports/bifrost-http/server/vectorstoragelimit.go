package server

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// The vector storage limit (client_config.vector_max_size_mb) bounds the estimated
// size of every vector built from logs, together: Warp's log index and every
// complexity router's captured requests. It is enforced by one hourly pass
// here rather than by each feature, since only one place sees all of them and
// can delete the oldest first across them (see vectorstore.EnforceStorageLimit).
// Every node runs the pass; they find the same cutoff, and a delete of what
// another node already deleted is a no-op. It never runs on the request path.

const (
	vectorStorageLimitInterval   = time.Hour
	vectorStorageLimitFirstDelay = 5 * time.Minute // off the boot path
	vectorStorageLimitTimeout    = 10 * time.Minute
)

// vectorStorageLimitBytes reads the limit now, in bytes; 0 is none. Read at each
// pass, since it can change at runtime.
func (s *BifrostHTTPServer) vectorStorageLimitBytes() int64 {
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

// vectorStorageLimitIndexes is every log-derived index: Warp's, when it has one, and
// every capture namespace in the shared store. The captures are listed from the
// store, not asked of the routing plugin, so those of a router another node
// serves, or of a routing plugin that is off, still count.
func (s *BifrostHTTPServer) vectorStorageLimitIndexes(ctx context.Context) ([]vectorstore.StorageLimitIndex, error) {
	var indexes []vectorstore.StorageLimitIndex
	if s.WarpHandler != nil {
		indexes = append(indexes, s.WarpHandler.Service().VectorStorageLimitIndexes(ctx)...)
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
			indexes = append(indexes, complexity.CaptureStorageLimitIndex(store, ns))
		}
	}
	return indexes, nil
}

// enforceVectorStorageLimit runs one pass. A pass that fails is logged and left for
// the next: the limit is a bound on growth, and an hour over it is no harm.
func (s *BifrostHTTPServer) enforceVectorStorageLimit(ctx context.Context, now time.Time) {
	limit := s.vectorStorageLimitBytes()
	if limit <= 0 {
		return
	}
	passCtx, cancel := context.WithTimeout(ctx, vectorStorageLimitTimeout)
	defer cancel()
	indexes, err := s.vectorStorageLimitIndexes(passCtx)
	if err != nil {
		logger.Warn("vector storage limit: failed to list the indexes it covers, so nothing was checked this hour: %v", err)
		return
	}
	result, err := vectorstore.EnforceStorageLimit(passCtx, limit, indexes, now)
	for _, skipped := range result.Skipped {
		logger.Warn("vector storage limit: left out of this hour's estimate, and not trimmed, because it could not be counted: %s", skipped)
	}
	if err != nil {
		logger.Warn("vector storage limit: vectors built from logs are estimated at %d MB against a limit of %d MB, and trimming them failed: %v", result.EstimatedBytes>>20, limit>>20, err)
		return
	}
	if result.Deleted > 0 {
		logger.Info("vector storage limit: vectors built from logs were estimated at %d MB, over the %d MB limit; deleted %d vector(s) older than %s", result.EstimatedBytes>>20, limit>>20, result.Deleted, result.Cutoff.Format(time.RFC3339))
	}
}

// startVectorStorageLimit runs the pass every hour until ctx ends.
func (s *BifrostHTTPServer) startVectorStorageLimit(ctx context.Context) {
	go func() {
		timer := time.NewTimer(vectorStorageLimitFirstDelay)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.enforceVectorStorageLimit(ctx, time.Now().UTC())
				timer.Reset(vectorStorageLimitInterval)
			}
		}
	}()
}
