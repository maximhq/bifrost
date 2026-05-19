package temptoken

import (
	"context"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// SweepWorker periodically deletes temp_tokens rows whose expires_at is in the
// past. It's the centralized expiry janitor for the temp-token table, mirroring
// the pattern used by PerUserOAuthSweepWorker for oauth_user_sessions.
//
// Tokens are also deleted eagerly by lifecycle owners (see Service.DeleteByResourceID,
// called from OAuth flow terminal transitions). The sweeper exists to catch rows
// whose owning resource timed out before any terminal transition fired, or whose
// scope no longer participates in lifecycle-driven cleanup at all.
type SweepWorker struct {
	service       *Service
	sweepInterval time.Duration
	stopCh        chan struct{}
	stopOnce      sync.Once
	logger        schemas.Logger
}

// NewSweepWorker constructs a worker bound to the given service. Returns nil
// when service is nil so callers can wire it unconditionally and check the
// result before starting.
func NewSweepWorker(service *Service, logger schemas.Logger) *SweepWorker {
	if service == nil {
		if logger != nil {
			logger.Warn("temp-token sweep worker not started: service is nil")
		}
		return nil
	}
	return &SweepWorker{
		service:       service,
		sweepInterval: 5 * time.Minute,
		stopCh:        make(chan struct{}),
		logger:        logger,
	}
}

// Start begins the sweep loop in a background goroutine.
func (w *SweepWorker) Start(ctx context.Context) {
	go w.run(ctx)
	if w.logger != nil {
		w.logger.Info("temp-token sweep worker started (interval=%s)", w.sweepInterval)
	}
}

// Stop gracefully stops the sweep worker. sync.Once guards against double-close
// panics from redundant shutdown paths.
func (w *SweepWorker) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
		if w.logger != nil {
			w.logger.Info("temp-token sweep worker stopped")
		}
	})
}

func (w *SweepWorker) run(ctx context.Context) {
	ticker := time.NewTicker(w.sweepInterval)
	defer ticker.Stop()

	// Run once on start so a deploy doesn't have to wait a full interval to
	// reap rows that expired while the process was down.
	w.sweepExpired(ctx)

	for {
		select {
		case <-ticker.C:
			w.sweepExpired(ctx)
		case <-w.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (w *SweepWorker) sweepExpired(ctx context.Context) {
	n, err := w.service.DeleteExpired(ctx, time.Now())
	if err != nil {
		if w.logger != nil {
			w.logger.Error("temp-token sweep failed: %v", err)
		}
		return
	}
	if n > 0 && w.logger != nil {
		w.logger.Debug("temp-token sweep removed %d expired rows", n)
	}
}

// SetSweepInterval updates the sweep cadence (for testing).
func (w *SweepWorker) SetSweepInterval(d time.Duration) {
	w.sweepInterval = d
}
