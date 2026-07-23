package server

import (
	"context"
	"sync"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
)

// oauth2SweepWorker periodically removes expired authorize requests and old
// revoked refresh tokens from the database, mirroring the pattern used by the
// temp-token sweep worker.
type oauth2SweepWorker struct {
	store             configstore.ConfigStore
	sweepInterval     time.Duration
	revokedRetention  time.Duration
	orphanClientGrace time.Duration
	// shouldSweep is consulted before each pass; when it returns false the pass
	// is skipped. The deletes touch shared state, so when several instances use
	// one config store a single sweeper suffices — the gate is re-checked every
	// interval because which instance should sweep can change at runtime. nil
	// means always sweep.
	shouldSweep func() bool
	stopCh      chan struct{}
	stopOnce    sync.Once
	cancel      context.CancelFunc
}

func newOAuth2SweepWorker(store configstore.ConfigStore, shouldSweep func() bool) *oauth2SweepWorker {
	if store == nil {
		return nil
	}
	return &oauth2SweepWorker{
		store:            store,
		sweepInterval:    10 * time.Minute,
		revokedRetention: 30 * 24 * time.Hour,
		// Grace before a token-less client is collected. Must exceed the
		// authorization code TTL so a client mid-handshake (code issued, not yet
		// exchanged) is not swept before it can mint its first token.
		orphanClientGrace: time.Hour,
		shouldSweep:       shouldSweep,
		stopCh:            make(chan struct{}),
	}
}

func (w *oauth2SweepWorker) start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(runCtx)
}

func (w *oauth2SweepWorker) stop() {
	w.stopOnce.Do(func() {
		// Cancel any in-flight sweep so a blocked DB call unwinds promptly,
		// then signal run() to exit its ticker loop.
		if w.cancel != nil {
			w.cancel()
		}
		close(w.stopCh)
	})
}

func (w *oauth2SweepWorker) run(ctx context.Context) {
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

func (w *oauth2SweepWorker) sweep(ctx context.Context) {
	if w.shouldSweep != nil && !w.shouldSweep() {
		return
	}
	if err := w.store.SweepExpiredOAuth2AuthorizeRequests(ctx); err != nil {
		logger.Warn("oauth2 authorize request sweep failed: %v", err)
	}
	if n, err := w.store.SweepOAuth2RefreshTokens(ctx, w.revokedRetention); err != nil {
		logger.Warn("oauth2 refresh token sweep failed: %v", err)
	} else if n > 0 {
		logger.Debug("oauth2 refresh token sweep removed %d revoked rows", n)
	}
	// Runs after the refresh token sweep so a client is only collected once its
	// tokens have aged out of their retention window, never while they are still
	// kept for reuse detection.
	if n, err := w.store.SweepOrphanedOAuth2Clients(ctx, w.orphanClientGrace); err != nil {
		logger.Warn("oauth2 orphaned client sweep failed: %v", err)
	} else if n > 0 {
		logger.Debug("oauth2 orphaned client sweep removed %d rows", n)
	}
}
