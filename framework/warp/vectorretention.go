package warp

import (
	"context"
	"errors"
	"time"

	"github.com/maximhq/bifrost/framework/vectorstore"
)

// What the log store deletes, the vector store has to forget too, or a
// deleted request keeps turning up in semantic search and in topic runs
// with nothing behind its id. Two things delete logs: a person, through the
// Logs API, which the logging plugin announces to ForgetLogs; and retention,
// which is age, so the hourly upkeep sweeps vectors older than the logs'
// retention on its own clock. The assignment and pool rows go with the log
// inside the log store itself (see deleteWarpTopicRowsForLogs).

// SetLogRetention tells the service how long the deployment keeps logs, as
// a function so a retention changed at runtime is read at each sweep. Zero
// or no function means no sweep.
func (s *Service) SetLogRetention(days func() int) {
	s.mu.Lock()
	s.logRetention = days
	s.mu.Unlock()
}

// ForgetLogs removes the vectors of deleted logs. Best effort: a vector that
// is already gone, or a store that cannot delete, must not fail the delete
// that was asked for, which has already happened.
func (s *Service) ForgetLogs(ctx context.Context, ids []string) {
	s.mu.RLock()
	vectors := s.vectorStore
	s.mu.RUnlock()
	if vectors == nil || len(ids) == 0 {
		return
	}
	// The stored namespace, whether or not Warp is switched on: a log deleted
	// while it is off still has a vector, and Config refuses a disabled Warp.
	view, err := s.ConfigView(ctx)
	if err != nil {
		return
	}
	namespace := view.LogVectorStoreNamespace
	for _, id := range ids {
		if err := vectors.Delete(ctx, namespace, id); err != nil && !errors.Is(err, vectorstore.ErrNotFound) {
			s.warnf("failed to remove the vector of deleted log %s: %v", id, err)
		}
	}
}

// sweepExpiredVectors deletes Warp's vectors for logs older than the logs'
// retention, and reports how many went.
func (s *Service) sweepExpiredVectors(ctx context.Context, now time.Time) (int, error) {
	s.mu.RLock()
	vectors, retention := s.vectorStore, s.logRetention
	s.mu.RUnlock()
	if vectors == nil || retention == nil {
		return 0, nil
	}
	days := retention()
	if days <= 0 {
		return 0, nil
	}
	// The stored namespace, whether or not Warp is switched on, as ForgetLogs
	// reads it: logs keep expiring while Warp is off. Without an embedding
	// model nothing was ever indexed, so there is nothing to sweep.
	view, err := s.ConfigView(ctx)
	if errors.Is(err, ErrUnavailable) {
		return 0, nil
	}
	if err != nil || view.EmbeddingModel == "" {
		return 0, err
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	deleted, err := vectors.DeleteAll(ctx, view.LogVectorStoreNamespace, []vectorstore.Query{
		{Field: "warp_log", Operator: vectorstore.QueryOperatorEqual, Value: true},
		{Field: "timestamp", Operator: vectorstore.QueryOperatorLessThan, Value: cutoff.Unix()},
	})
	return len(deleted), err
}
