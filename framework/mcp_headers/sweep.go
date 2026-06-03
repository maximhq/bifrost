package mcp_headers

import (
	"context"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// CredentialSweepWorker periodically purges stale per-user header credentials
// AND expired pending submission-flow rows. Two independent sweeps run on the
// same goroutine:
//
//   - Orphan credential sweep: rows that have been in 'orphaned' state
//     (VK lost access) longer than OrphanRetention are hard-deleted.
//   - Expired flow sweep: pending flow rows whose ExpiresAt has passed
//     (caller never completed the submission, link aged out) are
//     hard-deleted. Tighter cadence than the orphan sweep because flow rows
//     are short-lived (15 min TTL).
//
// Mirrors oauth2.PerUserOAuthSweepWorker, which combines the same two
// concerns on the OAuth side.
//
// Defaults: 24h orphan cadence, 15 min flow-expiry cadence. Non-positive
// orphanRetention disables the orphan sweep entirely; the expired-flow sweep
// always runs because flow rows have no semantic value past their expiry.
type CredentialSweepWorker struct {
	provider           *Provider
	orphanSweepEvery   time.Duration
	orphanRetention    time.Duration
	expiredFlowEvery   time.Duration
	stopCh             chan struct{}
	stopOnce           sync.Once
	logger             schemas.Logger
}

// NewCredentialSweepWorker creates a sweep worker with sensible defaults.
// orphanRetention <= 0 disables the orphan sweep (the worker still starts but
// the tick is a no-op — keeps wiring uniform).
func NewCredentialSweepWorker(provider *Provider, orphanRetention time.Duration, logger schemas.Logger) *CredentialSweepWorker {
	if provider == nil || provider.configStore == nil {
		if logger != nil {
			logger.Warn("per-user headers credential sweep worker not started: provider or config store is nil")
		}
		return nil
	}
	return &CredentialSweepWorker{
		provider:         provider,
		orphanSweepEvery: 24 * time.Hour,
		orphanRetention:  orphanRetention,
		expiredFlowEvery: 15 * time.Minute,
		stopCh:           make(chan struct{}),
		logger:           logger,
	}
}

// Start begins the sweep worker in a background goroutine.
func (w *CredentialSweepWorker) Start(ctx context.Context) {
	go w.run(ctx)
	if w.logger != nil {
		w.logger.Info("Per-user headers sweep worker started (orphan=%s, retention=%s, expired_flow=%s)",
			w.orphanSweepEvery, w.orphanRetention, w.expiredFlowEvery)
	}
}

// Stop gracefully stops the sweep worker. sync.Once guards against double-close
// panics from redundant shutdown paths.
func (w *CredentialSweepWorker) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
		if w.logger != nil {
			w.logger.Info("Per-user headers credential sweep worker stopped")
		}
	})
}

func (w *CredentialSweepWorker) run(ctx context.Context) {
	orphanTicker := time.NewTicker(w.orphanSweepEvery)
	defer orphanTicker.Stop()
	expiredFlowTicker := time.NewTicker(w.expiredFlowEvery)
	defer expiredFlowTicker.Stop()

	// Run once on start so a deploy doesn't have to wait a full interval.
	w.sweepOrphanedCredentials(ctx)
	w.sweepExpiredFlows(ctx)

	for {
		select {
		case <-orphanTicker.C:
			w.sweepOrphanedCredentials(ctx)
		case <-expiredFlowTicker.C:
			w.sweepExpiredFlows(ctx)
		case <-w.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (w *CredentialSweepWorker) sweepOrphanedCredentials(ctx context.Context) {
	if w.orphanRetention <= 0 {
		return
	}
	n, err := w.provider.configStore.DeleteOrphanedMCPPerUserHeaderCredentials(ctx, w.orphanRetention)
	if err != nil {
		if w.logger != nil {
			w.logger.Error("per-user headers orphan sweep failed: %v", err)
		}
		return
	}
	if n > 0 && w.logger != nil {
		w.logger.Info("per-user headers orphan sweep removed %d rows older than %s", n, w.orphanRetention)
	}
}

func (w *CredentialSweepWorker) sweepExpiredFlows(ctx context.Context) {
	n, err := w.provider.configStore.DeleteExpiredMCPPerUserHeaderFlows(ctx)
	if err != nil {
		if w.logger != nil {
			w.logger.Error("per-user headers expired-flow sweep failed: %v", err)
		}
		return
	}
	if n > 0 && w.logger != nil {
		w.logger.Info("per-user headers expired-flow sweep removed %d rows", n)
	}
}

// SetOrphanSweepInterval updates the orphan-sweep cadence (for testing).
// Non-positive durations are ignored — run() feeds the field straight into
// time.NewTicker, which panics on d <= 0.
func (w *CredentialSweepWorker) SetOrphanSweepInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	w.orphanSweepEvery = d
}
