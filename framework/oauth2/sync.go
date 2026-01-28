package oauth2

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TokenRefreshWorker manages automatic token refresh for expiring OAuth tokens
type TokenRefreshWorker struct {
	provider        *OAuth2Provider
	refreshInterval time.Duration
	lookAheadWindow time.Duration // How far ahead to look for expiring tokens
	stopCh          chan struct{}
	logger          schemas.Logger
}

// NewTokenRefreshWorker creates a new token refresh worker
func NewTokenRefreshWorker(provider *OAuth2Provider, logger schemas.Logger) *TokenRefreshWorker {
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

// Stop gracefully stops the token refresh worker
func (w *TokenRefreshWorker) Stop() {
	close(w.stopCh)
	if w.logger != nil {
		w.logger.Info("Token refresh worker stopped")
	}
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

			// Mark the oauth_config as expired so user knows to re-authorize
			oauthConfig.Status = "expired"
			if updateErr := w.provider.configStore.UpdateOauthConfig(ctx, oauthConfig); updateErr != nil {
				if w.logger != nil {
					w.logger.Error("Failed to update oauth config status: %s, error: %s", oauthConfig.ID, updateErr.Error())
				}
			}
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
