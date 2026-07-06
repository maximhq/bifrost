package oauth2

import (
	"context"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TokenRefreshWorker manages automatic token refresh for expiring OAuth tokens
type TokenRefreshWorker struct {
	provider        *OAuth2Provider
	refreshInterval time.Duration
	lookAheadWindow time.Duration // How far ahead to look for expiring tokens
	stopCh          chan struct{}
	stopOnce        sync.Once
	logger          schemas.Logger
}

// NewTokenRefreshWorker creates a new token refresh worker
func NewTokenRefreshWorker(provider *OAuth2Provider, logger schemas.Logger) *TokenRefreshWorker {
	if provider.configStore == nil {
		logger.Warn("config store is nil, skipping token refresh worker")
		return nil
	}
	return &TokenRefreshWorker{
		provider:        provider,
		refreshInterval: 5 * time.Minute, // Check every 5 minutes
		lookAheadWindow: 5 * time.Minute, // Refresh tokens expiring in next 5 minutes
		stopCh:          make(chan struct{}),
		logger:          logger,
	}
}

// Start begins the token refresh worker in a background goroutine
func (w *TokenRefreshWorker) Start(ctx context.Context) {
	go w.run(ctx)
	if w.logger != nil {
		w.logger.Info("Token refresh worker started")
	}
}

// Stop gracefully stops the token refresh worker. Safe to call multiple times
// — guarded by sync.Once so a redundant call from a secondary shutdown path
// can't panic by re-closing the channel.
func (w *TokenRefreshWorker) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
		if w.logger != nil {
			w.logger.Info("Token refresh worker stopped")
		}
	})
}

// run is the main worker loop
func (w *TokenRefreshWorker) run(ctx context.Context) {
	ticker := time.NewTicker(w.refreshInterval)
	defer ticker.Stop()

	// Run immediately on start
	w.refreshExpiredTokens(ctx)

	for {
		select {
		case <-ticker.C:
			w.refreshExpiredTokens(ctx)
		case <-w.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// refreshExpiredTokens queries and refreshes tokens that are expiring soon
func (w *TokenRefreshWorker) refreshExpiredTokens(ctx context.Context) {
	expiryThreshold := time.Now().Add(w.lookAheadWindow)

	// Get tokens expiring before the threshold
	tokens, err := w.provider.configStore.GetExpiringOauthTokens(ctx, expiryThreshold)
	if err != nil {
		if w.logger != nil {
			w.logger.Error("Failed to get expiring tokens", "error", err)
		}
		return
	}

	if len(tokens) == 0 {
		return
	}

	if w.logger != nil {
		w.logger.Debug("Found expiring tokens to refresh: %d", len(tokens))
	}

	// Refresh each expiring token
	for _, token := range tokens {
		// Find the oauth_config that references this token
		oauthConfig, err := w.provider.configStore.GetOauthConfigByTokenID(ctx, token.ID)
		if err != nil {
			if w.logger != nil {
				w.logger.Error("Failed to find oauth config for token: %s, error: %s", token.ID, err.Error())
			}
			continue
		}

		if oauthConfig == nil {
			if w.logger != nil {
				w.logger.Warn("No oauth config found for token: %s", token.ID)
			}
			continue
		}

		// Attempt to refresh the token
		if err := w.provider.RefreshAccessToken(ctx, oauthConfig.ID); err != nil {
			if w.logger != nil {
				w.logger.Error("Failed to refresh token", "oauth_config_id", oauthConfig.ID, "error", err)
			}

			// Only mark as expired for permanent auth rejections (e.g. invalid_grant, 401).
			// Transient failures (DNS, timeout, offline) are skipped — the worker will
			// retry on the next tick and the connection heals automatically when online.
			w.provider.markExpiredIfPermanent(ctx, oauthConfig, err)
		} else {
			if w.logger != nil {
				w.logger.Debug("Successfully refreshed token: %s", oauthConfig.ID)
			}
		}
	}
}

// SetRefreshInterval updates the refresh check interval (for testing)
func (w *TokenRefreshWorker) SetRefreshInterval(interval time.Duration) {
	w.refreshInterval = interval
}

// SetLookAheadWindow updates the look-ahead window for token expiry (for testing)
func (w *TokenRefreshWorker) SetLookAheadWindow(window time.Duration) {
	w.lookAheadWindow = window
}

// PerUserOAuthSweepWorker periodically purges stale per-user OAuth state:
//   - expired pending flow rows (oauth_user_sessions where ExpiresAt < now and status='pending')
//   - orphaned token rows older than OrphanRetention (oauth_user_tokens where status='orphaned')
//
// Pending-flow expiry is short (driven by the row's own ExpiresAt, default
// 15min) so the flow tick is fast. Orphan retention is operator-tunable
// (default 30 days) and the orphan tick is slow.
type PerUserOAuthSweepWorker struct {
	provider         *OAuth2Provider
	flowSweepEvery   time.Duration
	orphanSweepEvery time.Duration
	orphanRetention  time.Duration
	stopCh           chan struct{}
	stopOnce         sync.Once
	logger           schemas.Logger
}

// NewPerUserOAuthSweepWorker creates a sweep worker with sensible defaults.
// orphanRetention <= 0 disables the orphan-token sweep.
func NewPerUserOAuthSweepWorker(provider *OAuth2Provider, orphanRetention time.Duration, logger schemas.Logger) *PerUserOAuthSweepWorker {
	if provider == nil || provider.configStore == nil {
		if logger != nil {
			logger.Warn("per-user OAuth sweep worker not started: provider or config store is nil")
		}
		return nil
	}
	return &PerUserOAuthSweepWorker{
		provider:         provider,
		flowSweepEvery:   1 * time.Minute,
		orphanSweepEvery: 24 * time.Hour,
		orphanRetention:  orphanRetention,
		stopCh:           make(chan struct{}),
		logger:           logger,
	}
}

// Start begins the sweep worker in a background goroutine.
func (w *PerUserOAuthSweepWorker) Start(ctx context.Context) {
	go w.run(ctx)
	if w.logger != nil {
		w.logger.Info("Per-user OAuth sweep worker started (flow=%s, orphan=%s, retention=%s)",
			w.flowSweepEvery, w.orphanSweepEvery, w.orphanRetention)
	}
}

// Stop gracefully stops the sweep worker. sync.Once guards against double-close
// panics when called from multiple shutdown paths.
func (w *PerUserOAuthSweepWorker) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
		if w.logger != nil {
			w.logger.Info("Per-user OAuth sweep worker stopped")
		}
	})
}

func (w *PerUserOAuthSweepWorker) run(ctx context.Context) {
	flowTicker := time.NewTicker(w.flowSweepEvery)
	defer flowTicker.Stop()
	orphanTicker := time.NewTicker(w.orphanSweepEvery)
	defer orphanTicker.Stop()

	// Run once on start so a deploy doesn't have to wait a full interval.
	w.sweepExpiredFlows(ctx)
	w.sweepOrphanedTokens(ctx)

	for {
		select {
		case <-flowTicker.C:
			w.sweepExpiredFlows(ctx)
		case <-orphanTicker.C:
			w.sweepOrphanedTokens(ctx)
		case <-w.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (w *PerUserOAuthSweepWorker) sweepExpiredFlows(ctx context.Context) {
	n, err := w.provider.configStore.DeleteExpiredOauthUserSessions(ctx)
	if err != nil {
		if w.logger != nil {
			w.logger.Error("per-user OAuth flow sweep failed: %v", err)
		}
		return
	}
	if n > 0 && w.logger != nil {
		w.logger.Debug("per-user OAuth flow sweep removed %d expired pending flows", n)
	}
}

func (w *PerUserOAuthSweepWorker) sweepOrphanedTokens(ctx context.Context) {
	if w.orphanRetention <= 0 {
		return
	}
	n, err := w.provider.configStore.DeleteOrphanedOauthUserTokens(ctx, w.orphanRetention)
	if err != nil {
		if w.logger != nil {
			w.logger.Error("per-user OAuth orphan-token sweep failed: %v", err)
		}
		return
	}
	if n > 0 && w.logger != nil {
		w.logger.Info("per-user OAuth orphan-token sweep removed %d rows older than %s", n, w.orphanRetention)
	}
}

// SetFlowSweepInterval updates the pending-flow sweep cadence (for testing).
// Non-positive durations are ignored — run() feeds the field straight into
// time.NewTicker, which panics on d <= 0.
func (w *PerUserOAuthSweepWorker) SetFlowSweepInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	w.flowSweepEvery = d
}

// SetOrphanSweepInterval updates the orphan-token sweep cadence (for testing).
// Same non-positive guard as SetFlowSweepInterval.
func (w *PerUserOAuthSweepWorker) SetOrphanSweepInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	w.orphanSweepEvery = d
}
