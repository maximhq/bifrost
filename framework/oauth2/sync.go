package oauth2

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// OAuthTokenRefreshWorker manages automatic token refresh for expiring OAuth tokens
type OAuthTokenRefreshWorker struct {
	provider        *OAuth2Provider
	refreshInterval time.Duration
	lookAheadWindow time.Duration // How far ahead to look for expiring tokens
	// AuthModes restricts proactive refresh to tokens whose AuthMode is in
	// this set. Defaults to {"shared", "admin"} — shared-client tokens and
	// retained per_user_oauth bootstrap credentials (auth_mode='admin') are
	// the only ones with no live caller to trigger a lazy/inline refresh:
	// admin-mode tokens back only the periodic tool-discovery syncer, never
	// real end-user traffic, so nothing else would ever refresh them.
	// Widening this further (e.g. to include "user"/"vk"/"session") opts
	// specific per-identity auth modes into the same proactive sweep.
	//
	// Set this before calling Start, not after: the background goroutine
	// reads this field directly on every sweep with no synchronization, so a
	// mutation concurrent with a running sweep is a data race. Every current
	// caller already treats this as write-once-before-Start; there is no
	// supported way to change an already-started worker's scope short of
	// Stop and constructing a new worker.
	AuthModes []string
	stopCh    chan struct{}
	stopOnce  sync.Once
	cancel    context.CancelFunc
	logger    schemas.Logger

	// onTokenRefreshed, when set, is invoked after each successful proactive
	// refresh with the token's owning MCP client ID and auth mode. Stored as
	// an atomic pointer because the worker goroutine reads it while the
	// serving layer installs it after the worker has already started.
	onTokenRefreshed atomic.Pointer[func(mcpClientID, authMode string)]

	// shouldRefresh, when set, gates whether a sweep tick actually queries and
	// refreshes tokens (see SetShouldRefreshGate's doc comment). Stored as an
	// atomic pointer for the same reason as onTokenRefreshed.
	shouldRefresh atomic.Pointer[func(ctx context.Context) bool]
}

// NewOAuthTokenRefreshWorker creates a new token refresh worker
func NewOAuthTokenRefreshWorker(provider *OAuth2Provider, logger schemas.Logger) *OAuthTokenRefreshWorker {
	if logger == nil {
		logger = bifrost.NewNoOpLogger()
	}
	if provider.configStore == nil {
		logger.Warn("config store is nil, skipping token refresh worker")
		return nil
	}
	return &OAuthTokenRefreshWorker{
		provider:        provider,
		refreshInterval: 5 * time.Minute,             // Check every 5 minutes
		lookAheadWindow: 5 * time.Minute,             // Refresh tokens expiring in next 5 minutes
		AuthModes:       []string{"shared", "admin"}, // Proactive refresh is shared + admin (retained bootstrap credentials) by default
		stopCh:          make(chan struct{}),
		logger:          logger,
	}
}

// SetOnTokenRefreshed registers a callback invoked after each successful
// proactive token refresh, with the owning MCP client ID and the token's
// auth mode. It lets the serving layer react to a freshened credential, for
// example by recycling a connection that captured the old one. Tokens with
// no MCP client ID (legacy rows) never fire the callback. Safe to call at
// any time, including while the worker is running; passing nil clears the
// callback.
func (w *OAuthTokenRefreshWorker) SetOnTokenRefreshed(cb func(mcpClientID, authMode string)) {
	if w == nil {
		return
	}
	if cb == nil {
		w.onTokenRefreshed.Store(nil)
		return
	}
	w.onTokenRefreshed.Store(&cb)
}

// SetShouldRefreshGate installs a predicate consulted at the start of every
// sweep tick; a tick proceeds only when the gate is unset or returns true.
// Lets a deployment with multiple workers sharing the same token store
// restrict sweeps to one at a time, avoiding concurrent refreshes of the
// same token. Safe to call at any time, including while the worker is
// running; passing nil restores the default (always run).
func (w *OAuthTokenRefreshWorker) SetShouldRefreshGate(gate func(ctx context.Context) bool) {
	if w == nil {
		return
	}
	if gate == nil {
		w.shouldRefresh.Store(nil)
		return
	}
	w.shouldRefresh.Store(&gate)
}

// Start begins the token refresh worker in a background goroutine
func (w *OAuthTokenRefreshWorker) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(runCtx)
	w.logger.Info("Token refresh worker started")
}

// Stop gracefully stops the token refresh worker. Safe to call multiple times
// — guarded by sync.Once so a redundant call from a secondary shutdown path
// can't panic by re-closing the channel.
func (w *OAuthTokenRefreshWorker) Stop() {
	w.stopOnce.Do(func() {
		// Cancel any in-flight refresh so a blocked DB call unwinds promptly,
		// then signal run() to exit its ticker loop.
		if w.cancel != nil {
			w.cancel()
		}
		close(w.stopCh)
		w.logger.Info("Token refresh worker stopped")
	})
}

// run is the main worker loop
func (w *OAuthTokenRefreshWorker) run(ctx context.Context) {
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
func (w *OAuthTokenRefreshWorker) refreshExpiredTokens(ctx context.Context) {
	if gate := w.shouldRefresh.Load(); gate != nil && !(*gate)(ctx) {
		return
	}
	expiryThreshold := time.Now().Add(w.lookAheadWindow)

	// Get tokens expiring before the threshold, restricted to the configured
	// auth modes (defaults to shared + admin — see AuthModes' doc comment).
	tokens, err := w.provider.configStore.GetExpiringOauthTokens(ctx, expiryThreshold, w.AuthModes)
	if err != nil {
		w.logger.Error("Failed to get expiring tokens: %v", err)
		return
	}

	if len(tokens) == 0 {
		return
	}
	w.logger.Debug("Found expiring tokens to refresh: %d", len(tokens))

	// Refresh each expiring token directly by its own ID — RefreshAccessToken
	// resolves the owning oauth_config internally, and (on permanent
	// rejection) flips the token's own Status to 'needs_reauth' itself, so
	// there's nothing left for this loop to do on failure beyond logging.
	for _, token := range tokens {
		// Logged at Debug: transient failures (DNS, timeout, offline) recur
		// on every tick and would spam the error log, while permanent
		// rejections are already surfaced by the token's Status flipping to
		// "needs_reauth" inside RefreshAccessToken.
		if err := w.provider.RefreshAccessToken(ctx, token.ID); err != nil {
			w.logger.Debug("Failed to refresh token: token_id: %s, mcp_client_id: %s, error: %s", token.ID, token.MCPClientID, err.Error())
		} else {
			w.logger.Debug("Successfully refreshed token: %s", token.ID)
			if cb := w.onTokenRefreshed.Load(); cb != nil && token.MCPClientID != "" {
				(*cb)(token.MCPClientID, token.AuthMode)
			}
		}
	}
}

// SetRefreshInterval updates the refresh check interval (for testing)
func (w *OAuthTokenRefreshWorker) SetRefreshInterval(interval time.Duration) {
	w.refreshInterval = interval
}

// SetLookAheadWindow updates the look-ahead window for token expiry (for testing)
func (w *OAuthTokenRefreshWorker) SetLookAheadWindow(window time.Duration) {
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
	cancel           context.CancelFunc
	logger           schemas.Logger
}

// NewPerUserOAuthSweepWorker creates a sweep worker with sensible defaults.
// orphanRetention <= 0 disables the orphan-token sweep.
func NewPerUserOAuthSweepWorker(provider *OAuth2Provider, orphanRetention time.Duration, logger schemas.Logger) *PerUserOAuthSweepWorker {
	if logger == nil {
		logger = bifrost.NewNoOpLogger()
	}
	if provider == nil || provider.configStore == nil {
		logger.Warn("per-user OAuth sweep worker not started: provider or config store is nil")
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
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(runCtx)
	w.logger.Info("Per-user OAuth sweep worker started (flow=%s, orphan=%s, retention=%s)",
		w.flowSweepEvery, w.orphanSweepEvery, w.orphanRetention)
}

// Stop gracefully stops the sweep worker. sync.Once guards against double-close
// panics when called from multiple shutdown paths.
func (w *PerUserOAuthSweepWorker) Stop() {
	w.stopOnce.Do(func() {
		// Cancel any in-flight sweep so a blocked DB call unwinds promptly,
		// then signal run() to exit its ticker loop.
		if w.cancel != nil {
			w.cancel()
		}
		close(w.stopCh)
		w.logger.Info("Per-user OAuth sweep worker stopped")
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
		w.logger.Error("per-user OAuth flow sweep failed: %v", err)
		return
	}
	if n > 0 {
		w.logger.Debug("per-user OAuth flow sweep removed %d expired pending flows", n)
	}
}

func (w *PerUserOAuthSweepWorker) sweepOrphanedTokens(ctx context.Context) {
	if w.orphanRetention <= 0 {
		return
	}
	n, err := w.provider.configStore.DeleteOrphanedOauthUserTokens(ctx, w.orphanRetention)
	if err != nil {
		w.logger.Error("per-user OAuth orphan-token sweep failed: %v", err)
		return
	}
	if n > 0 {
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
