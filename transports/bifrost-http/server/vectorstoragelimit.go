package server

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// Each configured vector store's storage limit (its max_size_mb) bounds the
// estimated size of the vectors built from logs that it holds, together: Warp's
// log index and every complexity router's captured requests, when they are in
// that store. It is enforced by one hourly pass here rather than by each
// feature, since only one place sees all of them and can delete the oldest
// first across them (see vectorstore.EnforceStorageLimit). Two names that share
// a connection are one store, under the settings of the one that owns the
// connection. Every node runs the pass; they find the same cutoff, and a delete
// of what another node already deleted is a no-op. It never runs on the request
// path.

const (
	vectorStorageLimitInterval   = time.Hour
	vectorStorageLimitFirstDelay = 5 * time.Minute // off the boot path
	vectorStorageLimitTimeout    = 10 * time.Minute
)

// vectorStorageLimitBytes reads a store's limit now, in bytes; 0 is none. Read
// at each pass, since it can change at runtime.
func (s *BifrostHTTPServer) vectorStorageLimitBytes(name string) int64 {
	_, maxSizeMB := s.vectorStoreSettings(name)
	return int64(max(maxSizeMB, 0)) * 1024 * 1024
}

// vectorStorageLimitIndexes is every log-derived index in store: Warp's, when
// it is there, and every capture namespace in it. The captures are listed from
// the store, not asked of the routing plugin, so those of a router another node
// serves, or of a routing plugin that is off, still count.
func (s *BifrostHTTPServer) vectorStorageLimitIndexes(ctx context.Context, store vectorstore.VectorStore) ([]vectorstore.StorageLimitIndex, error) {
	var indexes []vectorstore.StorageLimitIndex
	if s.WarpHandler != nil {
		for _, index := range s.WarpHandler.Service().VectorStorageLimitIndexes(ctx) {
			if index.Store == store {
				indexes = append(indexes, index)
			}
		}
	}
	captures := complexity.CaptureStore(store)
	if captures == nil {
		return indexes, nil
	}
	namespaces, err := complexity.ListCaptureNamespaces(ctx, captures)
	if err != nil {
		return nil, err
	}
	for _, ns := range namespaces {
		if _, ok := complexity.CaptureNamespaceRouterKey(ns); ok {
			indexes = append(indexes, complexity.CaptureStorageLimitIndex(captures, ns))
		}
	}
	return indexes, nil
}

// enforceVectorStorageLimits runs one pass over every connected store that
// owns its connection and has a limit.
func (s *BifrostHTTPServer) enforceVectorStorageLimits(ctx context.Context, now time.Time) {
	if s.Config == nil {
		return
	}
	for _, entry := range s.Config.VectorStores.All() {
		if entry.Status != vectorstore.StoreConnected || entry.SharedWith != "" {
			continue
		}
		s.enforceVectorStorageLimit(ctx, entry, now)
	}
}

// enforceVectorStorageLimit runs one pass over one store. A pass that fails is
// logged and left for the next: the limit is a bound on growth, and an hour
// over it is no harm.
func (s *BifrostHTTPServer) enforceVectorStorageLimit(ctx context.Context, entry *vectorstore.NamedStore, now time.Time) {
	limit := s.vectorStorageLimitBytes(entry.Name)
	if limit <= 0 {
		return
	}
	passCtx, cancel := context.WithTimeout(ctx, vectorStorageLimitTimeout)
	defer cancel()
	indexes, err := s.vectorStorageLimitIndexes(passCtx, entry.Store)
	if err != nil {
		logger.Warn("vector storage limit of %q: failed to list the indexes it covers, so nothing was checked this hour: %v", entry.Name, err)
		return
	}
	result, err := vectorstore.EnforceStorageLimit(passCtx, limit, indexes, now)
	for _, skipped := range result.Skipped {
		logger.Warn("vector storage limit of %q: left out of this hour's estimate, and not trimmed, because it could not be counted: %s", entry.Name, skipped)
	}
	if err != nil {
		logger.Warn("vector storage limit of %q: vectors built from logs are estimated at %d MB against a limit of %d MB, and trimming them failed: %v", entry.Name, result.EstimatedBytes>>20, limit>>20, err)
		return
	}
	if result.Deleted > 0 {
		logger.Info("vector storage limit of %q: vectors built from logs were estimated at %d MB, over the %d MB limit; deleted %d vector(s) older than %s", entry.Name, result.EstimatedBytes>>20, limit>>20, result.Deleted, result.Cutoff.Format(time.RFC3339))
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
				s.enforceVectorStorageLimits(ctx, time.Now().UTC())
				timer.Reset(vectorStorageLimitInterval)
			}
		}
	}()
}
