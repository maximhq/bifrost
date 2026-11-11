package warp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// What the log store deletes, the vector store has to forget too, or a
// deleted request keeps turning up in semantic search and in topic runs
// with nothing behind its id. Two things delete logs: a person, through the
// Logs API, which the logging plugin announces to ForgetLogs; and retention,
// which is age, so the hourly upkeep sweeps vectors older than the logs'
// retention on its own clock. The assignment and pool rows go with the log
// inside the log store itself (see deleteWarpTopicRowsForLogs).
//
// The vector store is not in the log store's transaction, so a vector delete
// can fail after its log is gone. ForgetLogs records each one that does in the
// log store, and the hourly upkeep retries them until they go. Retention would
// not catch them: with none set nothing is swept, and a vector left behind
// keeps the deleted request's metadata - who sent it, its team, its cost - for
// as long as the deployment runs.

// SetLogRetention tells the service how long the deployment keeps logs, as
// a function so a retention changed at runtime is read at each sweep. Zero
// or no function means no sweep.
func (s *Service) SetLogRetention(days func() int) {
	s.mu.Lock()
	s.logRetention = days
	s.mu.Unlock()
}

// vectorDeleteRetryBatch is how many pending vector deletes one upkeep pass
// retries.
const vectorDeleteRetryBatch = 500

// ForgetLogs removes the vectors of deleted logs. It never fails the delete
// that was asked for, which has already happened. A vector that is already
// gone is not an error; one it cannot remove - the vector store is down, or
// the request was cancelled - is recorded for upkeep to retry.
func (s *Service) ForgetLogs(ctx context.Context, ids []string) {
	s.mu.RLock()
	vectors := s.vectorStore
	s.mu.RUnlock()
	if vectors == nil || len(ids) == 0 {
		return
	}
	// The stored namespace, whether or not Warp is switched on: a log deleted
	// while it is off still has a vector, and Config refuses a disabled Warp.
	// Without an embedding model nothing was ever indexed. A configuration
	// that cannot be read leaves every delete to the retry, which reads the
	// namespace then.
	namespace := ""
	view, err := s.ConfigView(ctx)
	switch {
	case errors.Is(err, ErrUnavailable):
		return
	case err == nil && view.EmbeddingModel == "":
		return
	case err == nil:
		namespace = view.LogVectorStoreNamespace
	}
	var failed []logstore.WarpVectorDelete
	for _, id := range ids {
		if err == nil {
			err = vectors.Delete(ctx, namespace, id)
			if err == nil || errors.Is(err, vectorstore.ErrNotFound) {
				err = nil
				continue
			}
		}
		// Once one fails the rest are recorded without trying: the store that
		// refused one, or the context that ended, refuses them all.
		failed = append(failed, logstore.WarpVectorDelete{LogID: id, Namespace: namespace})
	}
	if len(failed) == 0 {
		return
	}
	s.recordVectorDeletes(ctx, failed, err)
}

// recordVectorDeletes keeps the deletes ForgetLogs could not do, on a context
// of its own: the request's may be the reason they failed.
func (s *Service) recordVectorDeletes(ctx context.Context, failed []logstore.WarpVectorDelete, cause error) {
	store := s.topicStore()
	if store == nil {
		s.warnf("failed to remove the vectors of %d deleted log(s), and there is no log store to retry them from: %v", len(failed), cause)
		return
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), topicsCleanupTimeout)
	defer cancel()
	if err := store.AddWarpVectorDeletes(recordCtx, failed); err != nil {
		s.warnf("failed to remove the vectors of %d deleted log(s) (%v), and could not record them to retry: %v", len(failed), cause, err)
		return
	}
	s.warnf("failed to remove the vectors of %d deleted log(s); topic upkeep will retry: %v", len(failed), cause)
}

// retryVectorDeletes retries the vector deletes ForgetLogs recorded, oldest
// first, and reports how many vectors are now gone. It stops at the first
// failure: the store that refused one is most likely refusing all of them,
// and the rest wait for the next pass.
func (s *Service) retryVectorDeletes(ctx context.Context) (int, error) {
	s.mu.RLock()
	vectors := s.vectorStore
	s.mu.RUnlock()
	store := s.topicStore()
	if vectors == nil || store == nil {
		return 0, nil
	}
	rows, err := store.ListWarpVectorDeletes(ctx, vectorDeleteRetryBatch)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	configured := ""
	done := make([]string, 0, len(rows))
	// A ClickHouse table lists a log recorded twice twice until it merges.
	seen := make(map[string]struct{}, len(rows))
	var failure error
	for _, row := range rows {
		if _, repeat := seen[row.LogID]; repeat {
			continue
		}
		seen[row.LogID] = struct{}{}
		namespace := row.Namespace
		if namespace == "" {
			if configured == "" {
				view, err := s.ConfigView(ctx)
				if err != nil {
					failure = err
					break
				}
				configured = view.LogVectorStoreNamespace
			}
			namespace = configured
		}
		if err := vectors.Delete(ctx, namespace, row.LogID); err != nil && !errors.Is(err, vectorstore.ErrNotFound) {
			failure = err
			break
		}
		done = append(done, row.LogID)
	}
	if len(done) > 0 {
		if err := store.DeleteWarpVectorDeletes(ctx, done); err != nil {
			return 0, fmt.Errorf("clear retried vector deletes: %w", err)
		}
	}
	if failure != nil {
		return len(done), fmt.Errorf("retry vector delete: %w", failure)
	}
	return len(done), nil
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
