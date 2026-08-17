package configstore

import (
	"context"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// sessionExpiryStore is deliberately narrower than ConfigStore so the worker
// has exactly the persistence capability it needs.
type sessionExpiryStore interface {
	DeleteOrphanedSessions(ctx context.Context, olderThan time.Duration) (int64, error)
}

// SessionSweepWorker periodically removes expired dashboard authentication
// sessions. Authentication still checks ExpiresAt on every request; this worker
// prevents expired rows from accumulating when a browser never returns.
type SessionSweepWorker struct {
	store           sessionExpiryStore
	sweepInterval   time.Duration
	orphanRetention time.Duration
	stopCh          chan struct{}
	stopOnce        sync.Once
	cancel          context.CancelFunc
	logger          schemas.Logger
}

// NewSessionSweepWorker constructs the dashboard-session janitor. The worker
// deletes only sessions that have been expired longer than orphanRetention.
// A nil store or non-positive sweep interval disables the worker so bootstrap
// code can wire it conditionally.
func NewSessionSweepWorker(store sessionExpiryStore, sweepInterval, orphanRetention time.Duration, logger schemas.Logger) *SessionSweepWorker {
	if store == nil {
		if logger != nil {
			logger.Warn("session sweep worker not started: store is nil")
		}
		return nil
	}
	if sweepInterval <= 0 {
		if logger != nil {
			logger.Warn("session sweep worker not started: sweep interval must be positive")
		}
		return nil
	}
	return &SessionSweepWorker{
		store:           store,
		sweepInterval:   sweepInterval,
		orphanRetention: orphanRetention,
		stopCh:          make(chan struct{}),
		logger:          logger,
	}
}

// Start begins an immediate sweep followed by periodic cleanup passes.
func (w *SessionSweepWorker) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(runCtx)
	if w.logger != nil {
		w.logger.Info("session sweep worker started (interval=%s, orphan_retention=%s)", w.sweepInterval, w.orphanRetention)
	}
}

// Stop cancels any active database call and makes shutdown idempotent.
func (w *SessionSweepWorker) Stop() {
	w.stopOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
		close(w.stopCh)
		if w.logger != nil {
			w.logger.Info("session sweep worker stopped")
		}
	})
}

func (w *SessionSweepWorker) run(ctx context.Context) {
	ticker := time.NewTicker(w.sweepInterval)
	defer ticker.Stop()

	w.sweep(ctx)
	for {
		select {
		case <-ticker.C:
			w.sweep(ctx)
		case <-w.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (w *SessionSweepWorker) sweep(ctx context.Context) {
	if w.orphanRetention <= 0 {
		return
	}
	n, err := w.store.DeleteOrphanedSessions(ctx, w.orphanRetention)
	if err != nil {
		if w.logger != nil {
			w.logger.Error("session sweep failed: %v", err)
		}
		return
	}
	if n > 0 && w.logger != nil {
		w.logger.Debug("session sweep removed %d orphaned rows", n)
	}
}
