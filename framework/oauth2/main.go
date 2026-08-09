package oauth2

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/temptoken"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

const (
	maxTokenRetries = 3
	networkTimeout  = 30 * time.Second
	// refreshAccessTokenTimeout bounds the detached work RefreshAccessToken
	// hands to refreshGroup: up to maxTokenRetries network attempts at
	// networkTimeout each, plus retry backoff, can approach 100s worst case —
	// rounded up with headroom so a stuck upstream can't wedge the
	// singleflight entry forever.
	refreshAccessTokenTimeout = 2 * time.Minute
	// terminalStatusUpdateTimeout bounds the detached needs_reauth status
	// write after a permanent OAuth rejection (see refreshAccessTokenLocked).
	terminalStatusUpdateTimeout = 10 * time.Second

	// maxTokenEndpointResponseBytes caps how much of an OAuth token endpoint
	// response callTokenEndpoint reads. A legitimate response is a small
	// JSON object; this only guards against an unbounded/misbehaving
	// endpoint, mirroring the cap core/providers/utils/largeresponse.go uses
	// for provider error bodies.
	maxTokenEndpointResponseBytes = 512 * 1024
)

// OAuth2Provider implements the schemas.OAuth2Provider interface
// It provides OAuth 2.0 authentication functionality with database persistence
type OAuth2Provider struct {
	configStore    configstore.ConfigStore
	mu             sync.RWMutex
	retryBaseDelay time.Duration // base delay for token endpoint retry backoff; doubles each attempt (1×, 2×, 4×)

	// refreshGroup serializes RefreshAccessToken per token ID instead of
	// provider-wide: exchangeRefreshToken can make up to maxTokenRetries
	// network attempts at networkTimeout each, and one slow identity
	// provider's refresh must not stall every other token's refresh on this
	// provider. Concurrent refreshes of the *same* token still collapse into
	// a single call and share its result, which is what actually needs to
	// be prevented (a racing double refresh_token redemption, most upstreams
	// reject the second as replay).
	refreshGroup singleflight.Group

	// tempTokens, when non-nil and enabled in client config, is used by
	// InitiateUserOAuthFlow to mint a short-lived mcp_auth temp token and
	// embed it in the returned auth-page URL as a fragment. Optional — when
	// nil or disabled, the URL is returned without a fragment and the page
	// works only for callers already authenticated to the dashboard.
	//
	// Held as an atomic.Pointer rather than under p.mu: it is written once at
	// startup and read on the request path, and p.mu is write-locked across
	// RevokeToken's database I/O. Sharing p.mu would stall flow init/cleanup
	// reads behind unrelated revoke traffic.
	tempTokens atomic.Pointer[temptoken.Service]

	// userTokens caches per-user access-token lookups keyed by
	// (auth mode, identity, mcp client). It owns its own locking and must
	// never be guarded by p.mu, for the same reason tempTokens is not:
	// p.mu is write-locked across token-endpoint network I/O, and a cached
	// read that had to wait on it would lose the entire point of the cache.
	// Write paths in this file evict inline; handler-level database writes
	// that bypass the provider evict through the exported eviction methods.
	// Exchanged tokens (token_exchange auth) share this cache: same key
	// scheme, same expiry-as-miss validator, same lifecycle evictions —
	// they just carry no token row ID because nothing is persisted for them.
	userTokens *userTokenCache

	// exchangeIdP holds the identity-provider resolver backing delegated
	// token exchange. Same atomic-pointer rationale as tempTokens: written
	// once at startup, read on the request path, and never guarded by p.mu.
	exchangeIdP atomic.Pointer[schemas.TokenExchangeIdPResolver]
}

// NewOAuth2Provider creates a new OAuth provider instance
func NewOAuth2Provider(configStore configstore.ConfigStore, logger schemas.Logger) *OAuth2Provider {
	if logger == nil {
		logger = bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	}
	SetLogger(logger)
	return &OAuth2Provider{
		configStore:    configStore,
		retryBaseDelay: time.Second,
		userTokens:     newUserTokenCache(defaultUserTokenCacheCapacity),
	}
}

// SetTempTokenService installs the temp-token service used by
// InitiateUserOAuthFlow to mint the mcp_auth token embedded in the
// auth-page URL fragment. Called by server startup once both services
// have been constructed (the provider is built first by lib/config.go,
// the service later by the HTTP transport).
func (p *OAuth2Provider) SetTempTokenService(svc *temptoken.Service) {
	p.tempTokens.Store(svc)
}

// tempTokenService returns the current temp-token service. Lock-free: the
// pointer is read atomically so request-path callers never contend with the
// p.mu write lock held across token-refresh network I/O.
func (p *OAuth2Provider) tempTokenService() *temptoken.Service {
	return p.tempTokens.Load()
}

// mcpTempTokenAuthEnabled reports whether MCP per-user OAuth links may include temp-token auth.
func (p *OAuth2Provider) mcpTempTokenAuthEnabled(ctx context.Context) bool {
	if p.configStore == nil {
		return false
	}
	clientConfig, err := p.configStore.GetClientConfig(ctx)
	if err != nil {
		logger.Warn("Failed to read MCP temp-token auth setting: %v", err)
		return false
	}
	return clientConfig != nil && clientConfig.MCPEnableTempTokenAuth
}

// cleanupFlow deletes the flow row and any temp tokens minted for it. Called
// on every terminal transition (success or any failure) so the auth-page
// link stops working as soon as the work it authorized ends.
//
// Detached from the caller's context via WithoutCancel so a client cancellation
// (e.g. the browser closing the tab after the upstream OAuth bounce) can't
// short-circuit the deletes and leave the rows alive until the sweep. Mirrors
// the pattern used by RefreshAccessToken's terminal status flip in this file.
func (p *OAuth2Provider) cleanupFlow(ctx context.Context, sessionID string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := p.configStore.DeleteOauthUserSession(cleanupCtx, sessionID); err != nil {
		logger.Warn("per-user OAuth flow row cleanup failed: session_id=%s err=%v", sessionID, err)
	}
	if svc := p.tempTokenService(); svc != nil {
		if _, err := svc.DeleteByResourceID(cleanupCtx, temptoken.MCPAuthScopeName, sessionID); err != nil {
			logger.Warn("per-user OAuth temp-token cleanup failed: session_id=%s err=%v", sessionID, err)
		}
	}
}

// GetAccessToken retrieves the access token for a given oauth_config_id
func (p *OAuth2Provider) GetAccessToken(ctx context.Context, oauthConfigID string) (string, error) {
	// Load oauth_config by ID
	oauthConfig, err := p.configStore.GetOauthConfigByID(ctx, oauthConfigID)
	if err != nil {
		return "", fmt.Errorf("failed to load oauth config: %w", err)
	}
	if oauthConfig == nil {
		return "", schemas.ErrOAuth2ConfigNotFound
	}

	// Resolve the shared token row via (oauth_config_id, auth_mode='shared')
	// — the replacement for the retired TableOauthConfig.TokenID FK shortcut.
	token, err := p.configStore.GetSharedOauthTokenByConfigID(ctx, oauthConfigID)
	if err != nil {
		return "", fmt.Errorf("failed to load oauth token: %w", err)
	}
	if token == nil {
		return "", fmt.Errorf("no token linked to oauth config")
	}

	return p.resolveAccessToken(ctx, token)
}

// resolveAccessToken takes an already-loaded MCP OAuth token row, refreshes
// it if needed, and returns a usable access token. Factored out of
// GetAccessToken so GetAdminAccessToken (same status/refresh/expiry/sanitize
// logic, just resolving the row via a different lookup) doesn't duplicate it.
func (p *OAuth2Provider) resolveAccessToken(ctx context.Context, token *tables.TableMCPOauthToken) (string, error) {
	// The token's own Status ('active' | 'orphaned' | 'needs_reauth') is the
	// sole source of truth for whether this credential is usable.
	// oauthConfig.Status only tracks the one-time bootstrap lifecycle
	// (pending/authorized/failed) and is never consulted here — matching how
	// GetUserAccessTokenByMode has always worked.
	if token.Status != "active" {
		return "", fmt.Errorf("oauth token is not active, status: %s: %w", token.Status, schemas.ErrOAuth2TokenExpired)
	}

	// Refresh only when the token has known expiry and a renewal path: a
	// refresh token, or the identity-provider integration for
	// token-exchange admin rows (which can re-mint without one).
	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) &&
		(strings.TrimSpace(token.RefreshToken) != "" || isExchangeBackedTokenRow(token)) {
		// Attempt automatic refresh
		if err := p.RefreshAccessToken(ctx, token.ID); err != nil {
			return "", fmt.Errorf("token expired and refresh failed: %w", err)
		}
		// Reload token after refresh
		var err error
		token, err = p.configStore.GetOauthTokenByID(ctx, token.ID)
		if err != nil || token == nil {
			return "", fmt.Errorf("failed to reload token after refresh: %w", err)
		}
	}
	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) {
		return "", fmt.Errorf("token expired and no refresh token is available; re-authorization required: %w", schemas.ErrOAuth2TokenExpired)
	}

	// Sanitize and return access token (trim whitespace/newlines that may cause header formatting issues)
	accessToken := strings.TrimSpace(token.AccessToken)
	if accessToken == "" {
		return "", fmt.Errorf("access token is empty after sanitization")
	}
	return accessToken, nil
}

// GetAdminAccessToken is GetAccessToken's admin-mode counterpart: resolves
// the retained bootstrap-verification credential for a per-user client's
// periodic tool-discovery refresh (ClientToolSyncer.performSync), rather
// than the shared-mode production credential. Keyed by the MCP client ID,
// which every admin row carries regardless of whether an oauth_configs
// template sits behind the credential.
func (p *OAuth2Provider) GetAdminAccessToken(ctx context.Context, mcpClientID string) (string, error) {
	token, err := p.configStore.GetAdminOauthTokenByMCPClientID(ctx, mcpClientID)
	if err != nil {
		return "", fmt.Errorf("failed to load admin oauth token: %w", err)
	}
	if token == nil {
		return "", fmt.Errorf("no admin token retained for this MCP client")
	}
	return p.resolveAccessToken(ctx, token)
}

// RefreshAccessToken refreshes any MCP OAuth token row — the single shared
// client credential (AuthMode="shared") or a per-identity credential
// (AuthMode "user"/"vk"/"session") alike — looked up by the token row's own
// primary-key ID. This is the one refresh path for every kind of MCP OAuth
// credential: GetAccessToken and GetUserAccessTokenByMode both funnel their
// lazy pre-flight refresh through here, unifying what used to be two
// separate functions (one keyed by oauth_config_id and reachable only via
// TableOauthConfig.TokenID, one keyed by token ID directly).
//
// Never writes to TableOauthConfig: the template config row (client_id,
// token_url, etc.) is read-only input to the token exchange here. Credential
// health lives entirely on the token row's own Status field.
//
// The actual refresh runs on a context detached from any single caller —
// via DoChan rather than Do — because it is shared work: with Do, the first
// caller's ctx backs the whole call, so that caller disconnecting (client
// abort, gateway timeout) would cancel the in-flight upstream request and
// deliver that same cancellation error to every other concurrent caller
// waiting on the same token, even though their own requests are still live.
// Each caller here instead only ever stops waiting on its own ctx; the
// shared refresh keeps running to completion (bounded by
// refreshAccessTokenTimeout) for whoever else still needs its result.
func (p *OAuth2Provider) RefreshAccessToken(ctx context.Context, tokenID string) error {
	ch := p.refreshGroup.DoChan(tokenID, func() (any, error) {
		refreshCtx, cancel := context.WithTimeout(context.Background(), refreshAccessTokenTimeout)
		defer cancel()
		return nil, p.refreshAccessTokenLocked(refreshCtx, tokenID)
	})
	select {
	case res := <-ch:
		return res.Err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// refreshAccessTokenLocked is RefreshAccessToken's body, run inside
// p.refreshGroup.DoChan so concurrent refreshes of the same token ID
// collapse into one call instead of each redeeming the refresh token
// independently.
func (p *OAuth2Provider) refreshAccessTokenLocked(ctx context.Context, tokenID string) error {
	token, err := p.configStore.GetOauthTokenByID(ctx, tokenID)
	if err != nil {
		return fmt.Errorf("failed to load oauth token: %w", err)
	}
	if token == nil {
		return fmt.Errorf("oauth token not found: %s", tokenID)
	}

	// Token-exchange admin rows have no oauth_configs template; renewal runs
	// through the identity-provider integration instead of the template's
	// token endpoint (and may not need a refresh token at all — the
	// client-credentials fallback re-mints).
	if isExchangeBackedTokenRow(token) {
		return p.refreshExchangeAdminToken(ctx, token)
	}

	if strings.TrimSpace(token.RefreshToken) == "" {
		return fmt.Errorf("no refresh token available")
	}

	// Load template OAuth config for token_url, client_id, etc. Split nil
	// from a real lookup failure so a missing config doesn't surface as a
	// useless "%!w(<nil>)" wrapped error.
	templateConfig, err := p.configStore.GetOauthConfigByID(ctx, token.OauthConfigID)
	if err != nil {
		return fmt.Errorf("failed to load template oauth config for refresh: %w", err)
	}
	if templateConfig == nil {
		return schemas.ErrOAuth2ConfigNotFound
	}

	// Call OAuth provider's token endpoint with refresh_token
	newTokenResponse, err := p.exchangeRefreshToken(
		ctx,
		templateConfig.TokenURL,
		templateConfig.GetResolvedClientID(),
		templateConfig.GetResolvedClientSecret(),
		token.RefreshToken,
		strings.TrimSpace(templateConfig.Resource),
	)
	if err != nil {
		// Permanent rejection (HTTP 401, or 400 with invalid_grant /
		// unauthorized_client per RFC 6749 §5.2) means the refresh token is
		// dead — revoked, expired, or bound to a grant the AS no longer
		// honors. Flip the row to 'needs_reauth' and signal
		// re-authentication via the ErrOAuth2TokenExpired sentinel,
		// regardless of which kind of holder (shared client or per-identity)
		// this row belongs to. Keeping the row (rather than deleting)
		// preserves binding identity + audit history; a fresh OAuth
		// completion (reauthorize for shared, re-auth flow for per-identity)
		// updates the same row back to 'active'.
		if permErr, ok := errors.AsType[*PermanentOAuthError](err); ok {
			// Detached, bounded context for the terminal status flip: ctx
			// here is already refreshAccessTokenTimeout-bounded and detached
			// from any single caller (see RefreshAccessToken), but it may be
			// close to its own deadline by the time we get the
			// 'invalid_grant' / 401 after retries. A fresh, short-timeout
			// context ensures this write isn't starved by that budget, and
			// isn't unbounded either — an unbounded write here could wedge
			// the singleflight entry indefinitely if the store hangs.
			statusCtx, cancel := context.WithTimeout(context.Background(), terminalStatusUpdateTimeout)
			defer cancel()
			if markErr := p.configStore.MarkOauthUserTokenNeedsReauthByID(statusCtx, token.ID); markErr != nil {
				return fmt.Errorf("oauth refresh permanently rejected but status update failed (mcp_client=%s auth_mode=%s upstream_status=%d): %w",
					token.MCPClientID, token.AuthMode, permErr.StatusCode, markErr)
			}
			// The row is no longer 'active'; drop any cached copy so lookups
			// surface the re-auth requirement instead of a dead access token.
			p.EvictUserTokenByID(token.ID)
			logger.Debug("OAuth refresh permanently rejected; token marked needs_reauth: mcp_client=%s auth_mode=%s upstream_status=%d",
				token.MCPClientID, token.AuthMode, permErr.StatusCode)
			return fmt.Errorf("refresh token rejected by upstream OAuth server, re-authentication required: %w", schemas.ErrOAuth2TokenExpired)
		}
		// Transient failure (5xx, network blip, non-permanent 400) — keep the
		// row so the next call retries refresh.
		return fmt.Errorf("token refresh failed: %w", err)
	}
	// Persist the refreshed credentials (sanitize tokens to prevent header
	// formatting issues). Sent as a status-gated write, not a blind full-row
	// Save keyed on token — this call started before the network round-trip
	// above and can finish after a concurrent credential rotation
	// (RotateMCPOAuthConfig) has already flipped this same row to
	// 'needs_reauth'; a plain Save of the in-memory (pre-rotation) token
	// would silently overwrite that back to 'active'.
	now := time.Now()
	var newExpiresAt *time.Time
	if newTokenResponse.ExpiresIn > 0 {
		exp := now.Add(time.Duration(newTokenResponse.ExpiresIn) * time.Second)
		newExpiresAt = bifrost.Ptr(exp)
	}
	newAccessToken := strings.TrimSpace(newTokenResponse.AccessToken)
	newRefreshToken := token.RefreshToken
	if newTokenResponse.RefreshToken != "" {
		newRefreshToken = strings.TrimSpace(newTokenResponse.RefreshToken)
	}

	// expectedPriorRefreshToken is token.RefreshToken as read above, before
	// the upstream exchangeRefreshToken call — it's the value this refresh
	// actually redeemed. The store only commits if that's still the row's
	// live refresh_token, so a concurrent refresh of the same row (e.g. from
	// another cluster node) that already won the race is never overwritten.
	updated, err := p.configStore.RefreshOauthTokenFieldsIfActive(ctx, token.ID, token.RefreshToken, newAccessToken, newRefreshToken, newExpiresAt, now)
	if err != nil {
		return fmt.Errorf("failed to update token: %w", err)
	}
	if !updated {
		// The CAS was lost — someone else (another cluster node, a concurrent
		// refresh, an explicit reauthorize) already moved refresh_token past
		// what this attempt read. That's not necessarily bad news: drop any
		// stale cached copy and reload the row fresh. If it's still active,
		// the winner already supplied current credentials — this attempt's
		// own (now-discarded) result was simply redundant, not a sign the
		// credential is dead. Only report ErrOAuth2TokenExpired (which
		// callers map to NeedsReauth) when the row is genuinely gone or
		// landed in a non-active terminal state.
		p.EvictUserTokenByID(token.ID)
		reloaded, reloadErr := p.configStore.GetOauthTokenByID(ctx, token.ID)
		if reloadErr == nil && reloaded != nil && reloaded.Status == "active" {
			logger.Debug("OAuth refresh CAS lost but the row is still active (refreshed elsewhere): token_id=%s auth_mode=%s", token.ID, token.AuthMode)
			return nil
		}
		return fmt.Errorf("token was reauthorized, rotated, or already refreshed elsewhere during this refresh, discarding this refresh: %w", schemas.ErrOAuth2TokenExpired)
	}

	// Drop any cached copy of the old access token; the next lookup reads
	// the freshly written row.
	p.EvictUserTokenByID(token.ID)

	logger.Debug("OAuth token refreshed successfully: token_id=%s auth_mode=%s", token.ID, token.AuthMode)

	return nil
}

// ForceRefreshAccessToken resolves the MCP OAuth token row backing config —
// the shared token linked to config.OauthConfigID for MCPAuthTypeOauth (same
// lookup GetAccessToken performs), or the caller's per-identity token for
// MCPAuthTypePerUserOauth (same lookup GetUserAccessTokenByMode performs,
// with (mode, identity) derived from ctx) — and refreshes it unconditionally
// via RefreshAccessToken, regardless of whether the token's own ExpiresAt
// says it's still good. RefreshAccessToken itself never consults ExpiresAt —
// only its callers (GetAccessToken, GetUserAccessTokenByMode) gate on it
// before deciding to call it — so resolving the right token row and going
// straight to RefreshAccessToken is the entire "force" behavior.
func (p *OAuth2Provider) ForceRefreshAccessToken(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) error {
	switch config.AuthType {
	case schemas.MCPAuthTypeOauth:
		if config.OauthConfigID == nil || *config.OauthConfigID == "" {
			return schemas.ErrOAuth2ConfigNotFound
		}
		token, err := p.configStore.GetSharedOauthTokenByConfigID(ctx, *config.OauthConfigID)
		if err != nil {
			return fmt.Errorf("failed to load oauth token: %w", err)
		}
		if token == nil {
			return fmt.Errorf("no token linked to oauth config")
		}
		// GetSharedOauthTokenByConfigID is not filtered by status (its doc
		// comment makes status-checking the caller's responsibility) — mirror
		// GetAccessToken's own check here so a token already marked
		// needs_reauth short-circuits locally instead of attempting a live
		// refresh call that's doomed to fail.
		if token.Status != "active" {
			return fmt.Errorf("oauth token is not active, status: %s: %w", token.Status, schemas.ErrOAuth2TokenExpired)
		}
		return p.RefreshAccessToken(ctx, token.ID)
	case schemas.MCPAuthTypePerUserOauth:
		mode := ctx.MCPAuthMode()
		identity := ctx.MCPIdentity(mode)
		if identity == "" {
			return fmt.Errorf("per-user OAuth for %s requires an identity to force-refresh its token", config.Name)
		}
		token, err := p.configStore.GetOauthUserTokenByMode(ctx, mode, identity, config.ID)
		if err != nil {
			return fmt.Errorf("failed to load per-user oauth token (mode=%s): %w", mode, err)
		}
		if token == nil {
			return schemas.ErrOAuth2TokenNotFound
		}
		// Force-refresh means the caller just saw the current access token
		// rejected upstream. Drop the cached copy for this binding up front,
		// in addition to RefreshAccessToken's own post-write eviction, so
		// the follow-up lookup re-reads the database even if the refresh
		// call itself fails.
		p.EvictUserToken(mode, identity, config.ID)
		return p.RefreshAccessToken(ctx, token.ID)
	case schemas.MCPAuthTypeTokenExchange:
		// Nothing is persisted for callers' exchanged tokens, so
		// force-refresh is evict + re-exchange: drop the cached copy for
		// this binding and perform a fresh exchange so the retry that
		// follows sends a token the identity provider just minted.
		mode := ctx.MCPAuthMode()
		identity := ctx.MCPIdentity(mode)
		if identity == "" {
			return schemas.ErrExchangeSubjectTokenMissing
		}
		p.EvictExchangedToken(mode, identity, config.ID)
		_, err := p.GetExchangedAccessToken(ctx, config)
		return err
	default:
		return fmt.Errorf("force-refresh is not supported for MCP auth type %q", config.AuthType)
	}
}

// ValidateToken checks if the token is still valid
func (p *OAuth2Provider) ValidateToken(ctx context.Context, oauthConfigID string) (bool, error) {
	oauthConfig, err := p.configStore.GetOauthConfigByID(ctx, oauthConfigID)
	if err != nil || oauthConfig == nil {
		return false, nil
	}

	token, err := p.configStore.GetSharedOauthTokenByConfigID(ctx, oauthConfigID)
	if err != nil || token == nil {
		return false, nil
	}

	// Status is the sole source of truth for usability, same as GetAccessToken:
	// a needs_reauth/orphaned row must not report valid just because its
	// ExpiresAt happens to be nil or in the future.
	if token.Status != "active" {
		return false, nil
	}

	// Tokens with unknown/non-expiring semantics are considered valid until upstream rejection.
	if token.ExpiresAt == nil {
		return true, nil
	}
	return time.Now().Before(*token.ExpiresAt), nil
}

// RevokeToken revokes the OAuth token
func (p *OAuth2Provider) RevokeToken(ctx context.Context, oauthConfigID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	oauthConfig, err := p.configStore.GetOauthConfigByID(ctx, oauthConfigID)
	if err != nil || oauthConfig == nil {
		return fmt.Errorf("oauth config not found: %w", err)
	}

	token, err := p.configStore.GetSharedOauthTokenByConfigID(ctx, oauthConfigID)
	if err != nil {
		return fmt.Errorf("failed to load oauth token: %w", err)
	}
	if token == nil {
		return fmt.Errorf("no token linked to oauth config")
	}

	// Optionally call provider's revocation endpoint (if supported)
	// This is best-effort - we'll delete the token even if revocation fails

	// Delete every shared token row for this config, not just the one
	// GetSharedOauthTokenByConfigID picked above (used only to confirm a
	// token exists) — nothing guarantees at most one auth_mode='shared' row
	// per oauth_config_id (see GetSharedOauthTokenByConfigID's doc comment),
	// and deleting a single row would leave a stale duplicate usable.
	// oauth_configs itself is left untouched — it's a reusable credential
	// template (client_id/client_secret/endpoints) independent of any one
	// token's lifecycle, and no longer carries a token reference or a
	// revoked-ness of its own to clean up.
	if err := p.configStore.DeleteSharedOauthTokensByConfigID(ctx, oauthConfigID); err != nil {
		return fmt.Errorf("failed to delete token: %w", err)
	}
	p.EvictUserTokenByID(token.ID)

	logger.Debug("OAuth token revoked", "oauth_config_id", oauthConfigID)

	return nil
}

// StorePendingMCPClient stores an MCP client config that's waiting for OAuth completion
// The config is persisted in the database (oauth_configs.mcp_client_config_json) to support
// multi-instance deployments where OAuth callback may hit a different server instance.
func (p *OAuth2Provider) StorePendingMCPClient(oauthConfigID string, mcpClientConfig schemas.MCPClientConfig) error {
	ctx := context.Background()

	oauthConfig, err := p.configStore.GetOauthConfigByID(ctx, oauthConfigID)
	if err != nil {
		return fmt.Errorf("failed to get oauth config: %w", err)
	}
	if oauthConfig == nil {
		return fmt.Errorf("oauth config not found: %s", oauthConfigID)
	}

	configJSON, err := json.Marshal(mcpClientConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal MCP client config: %w", err)
	}
	configStr := string(configJSON)
	oauthConfig.MCPClientConfigJSON = &configStr

	if err := p.configStore.UpdateOauthConfig(ctx, oauthConfig); err != nil {
		return fmt.Errorf("failed to update oauth config with MCP client config: %w", err)
	}

	logger.Debug("Stored pending MCP client config", "oauth_config_id", oauthConfigID)
	return nil
}

// GetPendingMCPClient retrieves an MCP client config by oauth_config_id.
// Returns nil if no pending config is found. Unlike before PKCE/expiry
// fields moved off TableOauthConfig, this no longer applies its own expiry
// gate: the only caller (completeMCPClientOAuth) already requires
// oauthConfig.Status=="authorized" before reaching this call, which is a
// stronger, more truthful signal than a raw timestamp comparison — a config
// row that reached "authorized" represents a real completed OAuth exchange
// regardless of how much wall-clock time has passed since the bootstrap
// stash was written.
func (p *OAuth2Provider) GetPendingMCPClient(oauthConfigID string) (*schemas.MCPClientConfig, error) {
	ctx := context.Background()

	oauthConfig, err := p.configStore.GetOauthConfigByID(ctx, oauthConfigID)
	if err != nil {
		return nil, fmt.Errorf("failed to get oauth config: %w", err)
	}
	if oauthConfig == nil {
		return nil, nil
	}

	if oauthConfig.MCPClientConfigJSON == nil || *oauthConfig.MCPClientConfigJSON == "" {
		return nil, nil
	}

	var config schemas.MCPClientConfig
	if err := json.Unmarshal([]byte(*oauthConfig.MCPClientConfigJSON), &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal MCP client config: %w", err)
	}

	return &config, nil
}

// RemovePendingMCPClient clears the pending MCP client config from the oauth config
// This is called after OAuth completion to clean up
func (p *OAuth2Provider) RemovePendingMCPClient(oauthConfigID string) error {
	ctx := context.Background()

	oauthConfig, err := p.configStore.GetOauthConfigByID(ctx, oauthConfigID)
	if err != nil {
		return fmt.Errorf("failed to get oauth config: %w", err)
	}
	if oauthConfig == nil {
		return nil // Already removed or doesn't exist
	}

	oauthConfig.MCPClientConfigJSON = nil

	if err := p.configStore.UpdateOauthConfig(ctx, oauthConfig); err != nil {
		return fmt.Errorf("failed to clear pending MCP client config: %w", err)
	}

	logger.Debug("Removed pending MCP client config", "oauth_config_id", oauthConfigID)
	return nil
}

// InitiateOAuthFlow creates an OAuth config and returns the authorization URL
// Supports OAuth discovery and PKCE
func (p *OAuth2Provider) InitiateOAuthFlow(ctx context.Context, config *schemas.OAuth2Config) (*schemas.OAuth2FlowInitiation, error) {
	// Generate state token for CSRF protection
	state, err := generateSecureRandomString(32)
	if err != nil {
		return nil, fmt.Errorf("failed to generate state token: %w", err)
	}

	// Create oauth config ID
	oauthConfigID := uuid.New().String()

	// Determine OAuth endpoints (discovery or provided)
	authorizeURL := config.AuthorizeURL
	tokenURL := config.TokenURL
	registrationURL := config.RegistrationURL // Accept user-provided registration URL
	scopes := config.Scopes
	resource := strings.TrimSpace(config.Resource)

	// Perform OAuth discovery ONLY if required URLs are missing
	// This allows users to:
	// 1. Provide all URLs manually (no discovery)
	// 2. Provide some URLs manually (partial discovery for missing ones)
	// 3. Provide no URLs (full discovery from server_url)
	needsDiscovery := (authorizeURL == "" || tokenURL == "")

	if needsDiscovery {
		if config.ServerURL == "" {
			return nil, fmt.Errorf("server_url is required for OAuth discovery when authorize_url or token_url is not provided")
		}

		logger.Debug("Performing OAuth discovery for missing endpoints", "server_url", config.ServerURL)

		metadata, err := DiscoverOAuthMetadata(ctx, config.ServerURL)
		if err != nil {
			return nil, fmt.Errorf("OAuth discovery failed: %w. Please provide authorize_url, token_url, and registration_url manually", err)
		}

		// Use discovered values only for missing fields (prefer user-provided values)
		if authorizeURL == "" {
			authorizeURL = metadata.AuthorizationURL
			if authorizeURL == "" {
				return nil, fmt.Errorf("authorize_url could not be discovered. Please provide it manually")
			}
			logger.Debug("Discovered authorize_url", "url", authorizeURL)
		}
		if tokenURL == "" {
			tokenURL = metadata.TokenURL
			if tokenURL == "" {
				return nil, fmt.Errorf("token_url could not be discovered. Please provide it manually")
			}
			logger.Debug("Discovered token_url", "url", tokenURL)
		}
		if registrationURL == nil && metadata.RegistrationURL != nil {
			registrationURL = metadata.RegistrationURL
			logger.Debug("Discovered registration_url", "url", *registrationURL)
		}
		if resource == "" && metadata.Resource != "" {
			resource = metadata.Resource
			logger.Debug("Discovered OAuth resource", "resource", resource)
		}
		// Merge scopes: use discovered scopes if user didn't provide any
		if len(scopes) == 0 && len(metadata.ScopesSupported) > 0 {
			scopes = metadata.ScopesSupported
			logger.Debug("Discovered scopes", "scopes", scopes)
		}

		logger.Debug("OAuth discovery completed successfully")
	}

	// Validate required fields after discovery
	if authorizeURL == "" {
		return nil, fmt.Errorf("authorize_url is required (provide manually or ensure server supports OAuth discovery)")
	}
	if tokenURL == "" {
		return nil, fmt.Errorf("token_url is required (provide manually or ensure server supports OAuth discovery)")
	}

	// Dynamic Client Registration (RFC 7591)
	// If client_id is NOT provided, attempt dynamic registration
	clientID := config.ClientID // carries env./vault. reference metadata; resolved at use time
	clientSecret := config.ClientSecret

	if !clientID.IsSet() {
		// Check if registration URL is available
		if registrationURL == nil || *registrationURL == "" {
			return nil, fmt.Errorf("client_id is required when the OAuth provider does not support dynamic client registration (RFC 7591). Please provide client_id manually or use an OAuth provider that supports dynamic registration")
		}

		logger.Debug("client_id not provided, attempting dynamic client registration (RFC 7591)")

		// Prepare registration request
		regReq := &DynamicClientRegistrationRequest{
			ClientName:              "Bifrost MCP Gateway",
			RedirectURIs:            []string{config.RedirectURI},
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
			TokenEndpointAuthMethod: "none", // Public client with PKCE (no client secret needed)
		}

		// Add scopes if available
		if len(scopes) > 0 {
			regReq.Scope = strings.Join(scopes, " ")
		}

		// Perform dynamic registration
		regResp, err := RegisterDynamicClient(ctx, *registrationURL, regReq)
		if err != nil {
			return nil, fmt.Errorf("dynamic client registration failed: %w. Please provide client_id manually", err)
		}

		// Use dynamically registered credentials. Literal values, not
		// NewSecretVar: these are opaque strings from an external
		// authorization server's registration response, and NewSecretVar
		// would parse an "env."/"vault." prefix as a reference — a
		// registered client_id/client_secret happening to start with one of
		// those prefixes would then resolve a local deployment secret
		// instead of being stored as-is.
		clientID = &schemas.SecretVar{Val: regResp.ClientID}
		clientSecret = &schemas.SecretVar{Val: regResp.ClientSecret} // May be empty for public clients

		logger.Debug("Dynamic client registration successful: client_id: %s, has_secret: %t", regResp.ClientID, clientSecret.IsSet())
	}

	// Generate PKCE challenge
	codeVerifier, codeChallenge, err := GeneratePKCEChallenge()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE challenge: %w", err)
	}

	// Serialize scopes
	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize scopes: %w", err)
	}

	// Create oauth_config record (using dynamically registered or user-provided client_id).
	// This row is a pure registration/credential template past this point —
	// State/CodeVerifier/RedirectURI/ExpiresAt live on the flow row created
	// below instead (FlowMode='admin'), not here. CodeChallenge is used only
	// to build the authorize URL below and is never persisted at all — the
	// per-user flow (InitiateUserOAuthFlow) has never stored it either, since
	// it's recomputed deterministically from CodeVerifier when needed again
	// (see BuildUpstreamAuthorizeURL).
	expiresAt := time.Now().Add(15 * time.Minute)
	oauthConfigRecord := &tables.TableOauthConfig{
		ID:              oauthConfigID,
		ClientID:        clientID, // May be from dynamic registration
		ClientSecret:    clientSecret,
		AuthorizeURL:    authorizeURL,
		TokenURL:        tokenURL,
		RegistrationURL: registrationURL,
		RedirectURI:     config.RedirectURI,
		Scopes:          string(scopesJSON),
		Resource:        resource,
		Status:          "pending",
		ServerURL:       config.ServerURL,
		UseDiscovery:    config.UseDiscovery,
	}

	// MCPClientID: best-effort, same lookup CompleteOAuthFlow uses at
	// callback time (see the comment there). Nothing has linked
	// config_mcp_clients.oauth_config_id to this row yet at this point in
	// either caller (the link is established after this function returns —
	// initiateMCPClientVerification for a re-authorizing client, or the
	// StorePendingMCPClient bootstrap path for one still being created), so
	// this is expected to resolve empty in the common case — same
	// tolerated-empty shape as TableMCPOauthToken's MCPClientID (see its
	// field comment).
	//
	// The config row and its flow row are created in one transaction: before
	// PKCE/state moved onto a separate flow row, this was a single INSERT and
	// therefore atomic by construction. A partial failure here (config
	// committed, flow row not) would otherwise leak a permanently orphaned
	// 'pending' oauth_configs row — nothing sweeps stale configs the way
	// DeleteExpiredOauthUserSessions sweeps stale flows.
	flowRow := &tables.TableMCPOauthFlow{
		ID:            uuid.New().String(),
		OauthConfigID: oauthConfigID,
		State:         state,
		RedirectURI:   config.RedirectURI,
		CodeVerifier:  codeVerifier,
		FlowMode:      "admin",
		Status:        "pending",
		ExpiresAt:     expiresAt,
	}
	if err := p.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Create(oauthConfigRecord).Error; err != nil {
			return fmt.Errorf("failed to create oauth config: %w", err)
		}
		var mcpClient tables.TableMCPClient
		if err := tx.Where("oauth_config_id = ?", oauthConfigID).First(&mcpClient).Error; err == nil {
			flowRow.MCPClientID = mcpClient.ClientID
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("failed to look up mcp client for oauth config: %w", err)
		}
		if err := tx.Create(flowRow).Error; err != nil {
			return fmt.Errorf("failed to create oauth flow: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Resolve env var reference to actual value for use in the authorize URL.
	// The reference ("env.MY_VAR") is stored in DB; the resolved value is sent to the provider.
	resolvedClientID := clientID.GetValue()

	// Build authorize URL with PKCE (using dynamically registered or user-provided client_id)
	authURL := p.buildAuthorizeURLWithPKCE(
		authorizeURL,
		resolvedClientID,
		config.RedirectURI,
		state,
		codeChallenge,
		scopes,
		resource,
	)

	logger.Debug("OAuth flow initiated successfully: oauth_config_id: %s, client_id: %s", oauthConfigID, resolvedClientID)

	return &schemas.OAuth2FlowInitiation{
		OauthConfigID: oauthConfigID,
		AuthorizeURL:  authURL,
		State:         state,
		ExpiresAt:     expiresAt,
	}, nil
}

// CompleteOAuthFlow handles the OAuth callback and exchanges code for tokens
// Supports PKCE verification. Handles the admin-mode flow — the shared
// client's production authorize and a per-user client's bootstrap-test
// authorize alike (see the FlowMode field comment on TableMCPOauthFlow).
func (p *OAuth2Provider) CompleteOAuthFlow(ctx context.Context, state, code string) error {
	// Atomically claim the admin-mode flow row by state. Scoped to
	// FlowMode='admin' so this can never claim a per-identity flow row out
	// from under CompleteUserOAuthFlow's own claim on the same table (see
	// ClaimOauthFlowByState).
	flow, err := p.configStore.ClaimOauthFlowByState(ctx, state)
	if err != nil {
		return fmt.Errorf("failed to claim oauth flow: %w", err)
	}
	if flow == nil {
		return fmt.Errorf("invalid state token")
	}

	oauthConfig, err := p.configStore.GetOauthConfigByID(ctx, flow.OauthConfigID)
	if err != nil {
		p.cleanupFlow(ctx, flow.ID)
		return fmt.Errorf("failed to lookup oauth config: %w", err)
	}
	if oauthConfig == nil {
		p.cleanupFlow(ctx, flow.ID)
		return fmt.Errorf("oauth config not found for flow %s", flow.ID)
	}

	// Check expiry. "failed" (not a since-retired "expired") is the terminal
	// bootstrap status here — the same value the token-exchange failure branch
	// below uses for a different kind of bootstrap-attempt failure.
	if time.Now().After(flow.ExpiresAt) {
		oauthConfig.Status = "failed"
		p.configStore.UpdateOauthConfig(ctx, oauthConfig)
		p.cleanupFlow(ctx, flow.ID)
		return fmt.Errorf("oauth flow expired")
	}

	redirectURI := flow.RedirectURI
	if redirectURI == "" {
		redirectURI = oauthConfig.RedirectURI
	}

	// Log token exchange attempt for debugging
	logger.Debug("Attempting token exchange",
		"token_url", oauthConfig.TokenURL,
		"client_id", oauthConfig.GetResolvedClientID(),
		"has_client_secret", oauthConfig.GetResolvedClientSecret() != "",
		"has_pkce_verifier", flow.CodeVerifier != "")

	// Exchange code for tokens with PKCE verifier
	tokenResponse, err := p.exchangeCodeForTokensWithPKCE(
		ctx,
		oauthConfig.TokenURL,
		code,
		oauthConfig.GetResolvedClientID(),
		oauthConfig.GetResolvedClientSecret(),
		redirectURI,
		flow.CodeVerifier, // PKCE verifier
		strings.TrimSpace(oauthConfig.Resource),
	)
	if err != nil {
		oauthConfig.Status = "failed"
		p.configStore.UpdateOauthConfig(ctx, oauthConfig)
		logger.Error("Token exchange failed",
			"error", err.Error(),
			"client_id", oauthConfig.GetResolvedClientID(),
			"token_url", oauthConfig.TokenURL)
		p.cleanupFlow(ctx, flow.ID)
		return fmt.Errorf("token exchange failed: %w", err)
	}

	// Parse scopes
	var scopes []string
	if tokenResponse.Scope != "" {
		scopes = strings.Split(tokenResponse.Scope, " ")
	}
	scopesJSON, _ := json.Marshal(scopes)

	// Create oauth_token record (sanitize tokens to prevent header formatting
	// issues). AuthMode is always 'shared' here, even when the flow belongs to
	// a per_user_oauth client: this callback cannot classify the token (it is
	// still unverified, and during initial creation the MCP client row does
	// not exist yet), so 'shared' doubles as a staging label. For shared
	// clients the row is the production credential and stays as-is; for
	// per-user clients the complete-oauth step verifies it and then either
	// promotes it to auth_mode='admin' (PromoteSharedOauthTokenToAdmin) or
	// revokes it.
	tokenID := uuid.New().String()
	var expiresAt *time.Time
	if tokenResponse.ExpiresIn > 0 {
		exp := time.Now().Add(time.Duration(tokenResponse.ExpiresIn) * time.Second)
		expiresAt = bifrost.Ptr(exp)
	}
	tokenRecord := &tables.TableMCPOauthToken{
		ID:            tokenID,
		AuthMode:      "shared",
		OauthConfigID: oauthConfig.ID,
		Status:        "active",
		AccessToken:   strings.TrimSpace(tokenResponse.AccessToken),
		RefreshToken:  strings.TrimSpace(tokenResponse.RefreshToken),
		TokenType:     tokenResponse.TokenType,
		ExpiresAt:     expiresAt,
		Scopes:        string(scopesJSON),
	}

	// MCPClientID: seeded from the flow row (populated best-effort at
	// InitiateOAuthFlow time), then re-resolved here — for a client
	// re-authorizing an existing credential, initiateMCPClientVerification
	// links config_mcp_clients.oauth_config_id to this row's ID before the
	// browser ever leaves for the upstream provider, which happens AFTER
	// InitiateOAuthFlow returns, so the flow row's own MCPClientID is stale
	// (empty) for that case even though the link exists by the time we get
	// here. For a client still being created (the StorePendingMCPClient
	// bootstrap path, where the config_mcp_clients row isn't inserted until
	// after this callback completes), no such row exists yet either way —
	// GetMCPClientByOauthConfigID returns ErrNotFound and MCPClientID stays
	// "", the same tolerated-empty shape the migration backfill uses for the
	// equivalent case (see the MCPClientID field comment on
	// TableMCPOauthToken).
	tokenRecord.MCPClientID = flow.MCPClientID
	if mcpClient, err := p.configStore.GetMCPClientByOauthConfigID(ctx, oauthConfig.ID); err == nil {
		tokenRecord.MCPClientID = mcpClient.ClientID
	} else if !errors.Is(err, configstore.ErrNotFound) {
		p.cleanupFlow(ctx, flow.ID)
		return fmt.Errorf("failed to look up mcp client for oauth config: %w", err)
	}

	// A shared config can be re-authorized (e.g. via the reauthorize
	// endpoint) after already having a live token row. Clear any existing
	// shared rows for this config, create the replacement, and link the
	// config, all in one transaction: a failure partway through must not be
	// able to leave the client with zero working shared credentials (delete
	// succeeded, create/update didn't) any more than it should leave a live
	// credential unlinked and untracked as "authorized" (CWE-459).
	oauthConfig.Status = "authorized"
	if err := p.configStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		if err := p.configStore.DeleteSharedOauthTokensByConfigID(ctx, oauthConfig.ID, tx); err != nil {
			return fmt.Errorf("failed to clear existing shared oauth tokens before re-completion: %w", err)
		}
		if err := p.configStore.CreateOauthToken(ctx, tokenRecord, tx); err != nil {
			return fmt.Errorf("failed to create oauth token: %w", err)
		}
		if err := p.configStore.UpdateOauthConfig(ctx, oauthConfig, tx); err != nil {
			return fmt.Errorf("failed to update oauth config: %w", err)
		}
		return nil
	}); err != nil {
		p.cleanupFlow(ctx, flow.ID)
		return err
	}
	// CreateOauthToken upserts by binding and rewrites tokenRecord.ID to the
	// reused row's ID when one existed, so this evicts exactly the row that
	// was written. A fresh row has nothing cached and the evict is a no-op.
	p.EvictUserTokenByID(tokenRecord.ID)

	// Flow row's purpose ends here — same reasoning as CompleteUserOAuthFlow
	// (see cleanupFlow): the token row is the durable record now. Harmless
	// no-op for the temp-token half of cleanupFlow, since admin flows never
	// mint one (only InitiateUserOAuthFlow does).
	p.cleanupFlow(ctx, flow.ID)

	logger.Debug("OAuth flow completed successfully", "oauth_config_id", oauthConfig.ID)

	return nil
}

// BuildUpstreamAuthorizeURL reconstructs the upstream provider authorization
// URL for a pending per-user OAuth flow. Called by the frontend sessions tab
// when the user clicks "Authenticate" — at which point the flow row already
// exists (from a prior InitiateUserOAuthFlow), the CSRF state + PKCE verifier
// are stored on it, and we just need to hand the user the upstream redirect.
//
// The code_challenge is recomputed deterministically from the stored verifier
// so we don't have to persist it separately.
func (p *OAuth2Provider) BuildUpstreamAuthorizeURL(ctx context.Context, flowID string) (string, error) {
	flow, err := p.configStore.GetOauthUserSessionByID(ctx, flowID)
	if err != nil {
		return "", fmt.Errorf("failed to load pending oauth flow: %w", err)
	}
	if flow == nil {
		return "", schemas.ErrOAuth2NotPerUserSession
	}
	return p.buildUpstreamAuthorizeURLForFlow(ctx, flow)
}

// BuildAdminUpstreamAuthorizeURL is BuildUpstreamAuthorizeURL's admin-mode
// counterpart: reconstructs the upstream provider authorization URL for a
// pending flow_mode='admin' row, used by the MCP client reauthorize endpoint
// to hand OAuth2Authorizer's popup a real provider URL to open directly
// (unlike the per-user page-based flow, which navigates to a Bifrost page
// that itself resolves the upstream URL later). Reads through GetOauthFlowByID
// rather than widening BuildUpstreamAuthorizeURL's own GetOauthUserSessionByID
// call: that lookup is deliberately scoped away from admin-mode rows (see its
// doc comment) so a caller-supplied flow ID from a per-user-facing endpoint
// can never reach an admin flow's PKCE state.
func (p *OAuth2Provider) BuildAdminUpstreamAuthorizeURL(ctx context.Context, flowID string) (string, error) {
	flow, err := p.configStore.GetOauthFlowByID(ctx, flowID)
	if err != nil {
		return "", fmt.Errorf("failed to load pending oauth flow: %w", err)
	}
	if flow == nil {
		return "", fmt.Errorf("oauth flow not found")
	}
	return p.buildUpstreamAuthorizeURLForFlow(ctx, flow)
}

// buildUpstreamAuthorizeURLForFlow is the shared core of
// BuildUpstreamAuthorizeURL and BuildAdminUpstreamAuthorizeURL: given an
// already-loaded, already-mode-scoped flow row, validate it's still usable
// and reconstruct the upstream authorize URL. The code_challenge is
// recomputed deterministically from the stored verifier so we don't have to
// persist it separately.
func (p *OAuth2Provider) buildUpstreamAuthorizeURLForFlow(ctx context.Context, flow *tables.TableMCPOauthFlow) (string, error) {
	if flow.Status != "pending" {
		return "", fmt.Errorf("oauth flow %s is %s: %w", flow.ID, flow.Status, schemas.ErrOAuth2FlowNotPending)
	}
	if time.Now().After(flow.ExpiresAt) {
		return "", fmt.Errorf("oauth flow %s: %w", flow.ID, schemas.ErrOAuth2FlowExpired)
	}
	templateConfig, err := p.configStore.GetOauthConfigByID(ctx, flow.OauthConfigID)
	if err != nil {
		return "", fmt.Errorf("failed to load template oauth config: %w", err)
	}
	if templateConfig == nil {
		return "", schemas.ErrOAuth2ConfigNotFound
	}
	// Recompute the PKCE challenge from the stored verifier (deterministic).
	hash := sha256.Sum256([]byte(flow.CodeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(hash[:])

	var scopes []string
	if templateConfig.Scopes != "" {
		if err := json.Unmarshal([]byte(templateConfig.Scopes), &scopes); err != nil {
			return "", fmt.Errorf("failed to parse oauth scopes for flow %s: %w", flow.ID, err)
		}
	}
	redirectURI := flow.RedirectURI
	if redirectURI == "" {
		redirectURI = templateConfig.RedirectURI
	}
	return p.buildAuthorizeURLWithPKCE(
		templateConfig.AuthorizeURL,
		templateConfig.GetResolvedClientID(),
		redirectURI,
		flow.State,
		codeChallenge,
		scopes,
		strings.TrimSpace(templateConfig.Resource),
	), nil
}

// buildAuthorizeURLWithPKCE constructs the OAuth authorization URL with PKCE
// parameters, preserving any query params already present on the stored
// authorizeURL. Naive "authorizeURL + ?" + params.Encode() concatenation
// would produce malformed URLs like "...?existing=1?response_type=code" when
// the provider's stored URL is already parameterized.
func (p *OAuth2Provider) buildAuthorizeURLWithPKCE(authorizeURL, clientID, redirectURI, state, codeChallenge string, scopes []string, resource string) string {
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		// Fall back to the naive form rather than dropping the auth URL —
		// callers expect a non-empty return. The OAuth provider will reject
		// a malformed URL, which is the same end state as not redirecting.
		params := url.Values{}
		params.Set("response_type", "code")
		params.Set("client_id", clientID)
		params.Set("redirect_uri", redirectURI)
		params.Set("state", state)
		params.Set("code_challenge", codeChallenge)
		params.Set("code_challenge_method", "S256")
		if len(scopes) > 0 {
			params.Set("scope", strings.Join(scopes, " "))
		}
		if resource != "" {
			params.Set("resource", resource)
		}
		return authorizeURL + "?" + params.Encode()
	}
	q := parsed.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256") // SHA-256 hashing
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	if resource != "" {
		q.Set("resource", resource)
	}
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

// exchangeCodeForTokensWithPKCE exchanges authorization code for access/refresh tokens with PKCE verifier
func (p *OAuth2Provider) exchangeCodeForTokensWithPKCE(ctx context.Context, tokenURL, code, clientID, clientSecret, redirectURI, codeVerifier, resource string) (*schemas.OAuth2TokenExchangeResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)
	data.Set("client_id", clientID)
	data.Set("code_verifier", codeVerifier) // PKCE verifier
	if resource != "" {
		data.Set("resource", resource)
	}

	// Only include client_secret if provided (optional for public clients with PKCE)
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}

	return p.callTokenEndpoint(ctx, tokenURL, data)
}

// exchangeRefreshToken exchanges refresh token for new access token
func (p *OAuth2Provider) exchangeRefreshToken(ctx context.Context, tokenURL, clientID, clientSecret, refreshToken, resource string) (*schemas.OAuth2TokenExchangeResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", clientID)
	if resource != "" {
		data.Set("resource", resource)
	}
	data.Set("client_secret", clientSecret)

	return p.callTokenEndpoint(ctx, tokenURL, data)
}

// PermanentOAuthError indicates the OAuth provider rejected the request in a way
// that requires user re-authorization (e.g. revoked refresh token, invalid_grant).
// Distinct from transient network failures which should be retried.
type PermanentOAuthError struct {
	StatusCode int
	Body       string
}

func (e *PermanentOAuthError) Error() string {
	return fmt.Sprintf("permanent oauth error (status %d): %s", e.StatusCode, e.Body)
}

// sleepIfNotLastAttempt waits with exponential backoff between retry attempts.
// No-ops on the final attempt to avoid sleeping before returning an error.
// Respects context cancellation so worker shutdown is not delayed.
func sleepIfNotLastAttempt(ctx context.Context, attempt int, baseDelay time.Duration) {
	if attempt < maxTokenRetries-1 {
		select {
		case <-time.After(time.Duration(1<<attempt) * baseDelay):
		case <-ctx.Done():
		}
	}
}

// callTokenEndpoint makes a POST request to the OAuth token endpoint with retry logic.
// Transport errors and 5xx responses are retried up to maxTokenRetries times with
// exponential backoff. HTTP 4xx responses are returned immediately as PermanentOAuthError.
func (p *OAuth2Provider) callTokenEndpoint(ctx context.Context, tokenURL string, data url.Values) (*schemas.OAuth2TokenExchangeResponse, error) {
	client := &http.Client{Timeout: networkTimeout}
	var lastErr error

	for attempt := range maxTokenRetries {
		req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(data.Encode()))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			// Propagate context cancellation immediately — no point retrying.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// Transport error (DNS failure, timeout, connection refused) — retry
			lastErr = fmt.Errorf("token request failed: %w", err)
			sleepIfNotLastAttempt(ctx, attempt, p.retryBaseDelay)
			continue
		}

		// Capped read: an OAuth token endpoint response is normally a small
		// JSON object, but nothing stops a misconfigured or malicious
		// endpoint from streaming an unbounded body — matching the 512KB cap
		// core/providers/utils/largeresponse.go uses for error bodies.
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenEndpointResponseBytes))
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read response: %w", err)
			sleepIfNotLastAttempt(ctx, attempt, p.retryBaseDelay)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			if resp.StatusCode == http.StatusUnauthorized {
				return nil, &PermanentOAuthError{StatusCode: resp.StatusCode, Body: string(body)}
			}
			// Per RFC 6749 §5.2, only invalid_grant and unauthorized_client within a 400
			// require user re-authorization. Other 400s (invalid_request, unsupported_grant_type,
			// etc.) are configuration or request errors — fail fast without expiring the config.
			if resp.StatusCode == http.StatusBadRequest {
				var oauthErr struct {
					Error string `json:"error"`
				}
				if json.Unmarshal(body, &oauthErr) == nil {
					if oauthErr.Error == "invalid_grant" || oauthErr.Error == "unauthorized_client" {
						return nil, &PermanentOAuthError{StatusCode: resp.StatusCode, Body: string(body)}
					}
				}
				return nil, fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, string(body))
			}
			// Transient error (rate limit, server error, etc.) — retry
			lastErr = fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, string(body))
			sleepIfNotLastAttempt(ctx, attempt, p.retryBaseDelay)
			continue
		}

		// Try to parse as JSON first
		var tokenResponse schemas.OAuth2TokenExchangeResponse
		if err := json.Unmarshal(body, &tokenResponse); err != nil {
			// Fall back to URL-encoded form data (GitHub's OAuth endpoint returns this format)
			formValues, parseErr := url.ParseQuery(string(body))
			if parseErr != nil {
				return nil, fmt.Errorf("failed to parse token response as JSON or form data: JSON error: %w, form error: %v", err, parseErr)
			}
			tokenResponse.AccessToken = formValues.Get("access_token")
			tokenResponse.RefreshToken = formValues.Get("refresh_token")
			tokenResponse.TokenType = formValues.Get("token_type")
			tokenResponse.Scope = formValues.Get("scope")
			if expiresIn := formValues.Get("expires_in"); expiresIn != "" {
				fmt.Sscanf(expiresIn, "%d", &tokenResponse.ExpiresIn)
			}
		}

		if tokenResponse.AccessToken == "" {
			return nil, fmt.Errorf("token response missing access_token, body: %s", string(body))
		}

		return &tokenResponse, nil
	}

	return nil, fmt.Errorf("token request failed after %d attempts: %w", maxTokenRetries, lastErr)
}

// generateSecureRandomString generates a cryptographically secure random string
func generateSecureRandomString(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(bytes)[:length], nil
}

// ---------- Per-User OAuth Methods ----------

// InitiateUserOAuthFlow creates or refreshes the per-user OAuth flow row for a
// given (mode, identity, mcp_client) binding and returns the auth landing URL.
//
// Determinism: there is exactly one flow row per binding. If one already exists
// (from a prior auth attempt), it's updated in place — fresh CSRF state, fresh
// PKCE verifier, status reset to 'pending'. Otherwise a new row is inserted.
// Reauth never duplicates rows; revoke deletes them.
//
// The function errors out cleanly on any misconfig (missing identity, unknown
// mode, missing template config) — no fallbacks, no generated identities.
func (p *OAuth2Provider) InitiateUserOAuthFlow(ctx context.Context, oauthConfigID string, mcpClientID string, redirectURI string, flowMode schemas.MCPAuthMode) (*schemas.OAuth2FlowInitiation, string, error) {
	// 1. Load template OAuth config.
	templateConfig, err := p.configStore.GetOauthConfigByID(ctx, oauthConfigID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to load template oauth config: %w", err)
	}
	if templateConfig == nil {
		return nil, "", schemas.ErrOAuth2ConfigNotFound
	}

	// 2. Resolve identity from context for the given mode. Required (no
	//    fallbacks). Each mode populates exactly one of (vkId, uid, sessionID).
	var (
		vkId, uid           *string
		sessionID, lookupID string
	)
	switch flowMode {
	case schemas.MCPAuthModeUser:
		v, _ := ctx.Value(schemas.BifrostContextKeyUserID).(string)
		if v == "" {
			return nil, "", fmt.Errorf("user-mode flow requires a user identity in context")
		}
		uid = &v
		lookupID = v
	case schemas.MCPAuthModeVK:
		v, _ := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID).(string)
		if v == "" {
			return nil, "", fmt.Errorf("vk-mode flow requires a resolved virtual key in context")
		}
		vkId = &v
		lookupID = v
	case schemas.MCPAuthModeSession:
		v, _ := ctx.Value(schemas.BifrostContextKeyMCPSessionID).(string)
		if v == "" {
			return nil, "", fmt.Errorf("session-mode flow requires x-bf-mcp-session-id in context")
		}
		sessionID = v
		lookupID = v
	case schemas.MCPAuthModeAdmin:
		// No identity dimension, a shared credential's reauth is scoped to
		// the MCP client alone (see GetOauthUserSessionByModeIdentityAndMCPClient's
		// admin branch). lookupID stays "" and vkId/uid/sessionID all stay
		// nil/empty; the row this creates/reuses is found again purely by
		// (flow_mode='admin', mcp_client_id).
	default:
		return nil, "", fmt.Errorf("unknown auth mode for flow: %s", flowMode)
	}

	// 3. Generate fresh CSRF state + PKCE verifier for this attempt.
	state, err := generateSecureRandomString(32)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate state token: %w", err)
	}
	codeVerifier, _, err := GeneratePKCEChallenge()
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate PKCE challenge: %w", err)
	}
	expiresAt := time.Now().Add(15 * time.Minute)

	// 4. Single canonical lookup: one flow row per (mode, identity, mcp_client).
	//
	//    Only treat a 'pending' hit as reusable. A 'claiming' row means an
	//    upstream callback is mid-flight for this binding — rotating its
	//    state/PKCE here would invalidate that callback's state token and
	//    leave the user with an "OAuth flow not found" error. Falling through
	//    to insert a fresh row lets both flows complete independently.
	var existing *tables.TableMCPOauthFlow
	found, lookupErr := p.configStore.GetOauthUserSessionByModeIdentityAndMCPClient(ctx, flowMode, lookupID, mcpClientID)
	if lookupErr != nil {
		return nil, "", fmt.Errorf("failed to look up existing flow row: %w", lookupErr)
	}
	if found != nil && found.Status == "pending" {
		existing = found
	}

	var rowID string
	if existing != nil {
		// Reauth path: update the same row in place. Identity columns already
		// match (we looked them up); only rotate the OAuth-dance fields.
		existing.OauthConfigID = oauthConfigID
		existing.State = state
		existing.RedirectURI = redirectURI
		existing.CodeVerifier = codeVerifier
		existing.Status = "pending"
		existing.ExpiresAt = expiresAt
		if err := p.configStore.UpdateOauthUserSession(ctx, existing); err != nil {
			return nil, "", fmt.Errorf("failed to update flow row for reauth: %w", err)
		}
		rowID = existing.ID
	} else {
		// First-time auth: insert a new row. SessionID is populated only for
		// session-mode (the caller's x-bf-mcp-session-id); vk/user-mode rows
		// have an empty SessionID — their identity lives in virtual_key_id /
		// user_id.
		row := &tables.TableMCPOauthFlow{
			ID:            uuid.New().String(),
			MCPClientID:   mcpClientID,
			OauthConfigID: oauthConfigID,
			State:         state,
			RedirectURI:   redirectURI,
			CodeVerifier:  codeVerifier,
			SessionID:     sessionID,
			VirtualKeyID:  vkId,
			UserID:        uid,
			FlowMode:      string(flowMode),
			Status:        "pending",
			ExpiresAt:     expiresAt,
		}
		if err := p.configStore.CreateOauthUserSession(ctx, row); err != nil {
			return nil, "", fmt.Errorf("failed to create flow row: %w", err)
		}
		rowID = row.ID
	}
	sessionID = rowID

	// Frontend URL the user (or their teammate) opens in a browser. The
	// upstream provider URL is reconstructed on demand by the /flows/:id/start
	// endpoint using the stored CSRF state + PKCE verifier — we don't pin it
	// here so the row stays the single source of truth for those fields.
	//
	// Derive the Bifrost base URL from the OAuth callback redirect URI passed
	// in by the caller — it always has the shape "{base}/api/oauth/callback".
	// Flow ID rides as a query param to match the flat-route convention used
	// elsewhere in the dashboard UI.
	frontendURL := strings.TrimSuffix(redirectURI, "/api/oauth/callback") + "/workspace/mcp-sessions/auth?flow=" + sessionID

	// Mint a mcp_auth temp token bound to this flow's row ID and embed it in
	// the URL as a fragment so a browser hitting the auth page without a
	// dashboard session can still call the per-user flow endpoints. The
	// fragment never leaves the browser (not in server logs, not in the
	// upstream-OAuth Referer), unlike a query param.
	//
	// User-mode flows skip the mint: the handler-side identity gate requires
	// caller's user_id to match flow.UserID, which is only populated by normal
	// SCIM enforcement on the auth-page route. Minting a temp token would
	// route the request through the temp-token middleware branch that
	// bypasses cookie resolution, leaving caller user_id empty and the gate
	// would 403 even legitimate users. VK and session-mode flows are
	// intentionally shareable and continue to mint.
	//
	// Admin-mode flows skip the mint too: this flow is always admin-initiated
	// from an authenticated dashboard session (the MCP client reauthorize
	// endpoint), never a shareable magic link, there is no
	// caller-without-a-session case to support here, unlike vk/session-mode.
	if svc := p.tempTokenService(); svc != nil && p.mcpTempTokenAuthEnabled(ctx) && flowMode != schemas.MCPAuthModeUser && flowMode != schemas.MCPAuthModeAdmin {
		ttl := time.Until(expiresAt)
		if ttl > 0 {
			plaintext, mintErr := svc.Mint(ctx, temptoken.MCPAuthScopeName, sessionID, ttl)
			if mintErr != nil {
				logger.Warn("Failed to mint mcp_auth temp token for flow %s: %v (link still usable for dashboard-authenticated callers)", sessionID, mintErr)
			} else {
				frontendURL = frontendURL + "#t=" + plaintext
			}
		}
	}

	logger.Debug("Per-user OAuth flow initiated: session_id=%s, mcp_client_id=%s", sessionID, mcpClientID)

	return &schemas.OAuth2FlowInitiation{
		OauthConfigID: oauthConfigID,
		AuthorizeURL:  frontendURL,
		State:         state,
		ExpiresAt:     expiresAt,
	}, sessionID, nil
}

// CompleteUserOAuthFlow handles the OAuth callback for a per-user flow.
// It looks up the session by state, exchanges code for tokens, and returns a session token.
func (p *OAuth2Provider) CompleteUserOAuthFlow(ctx context.Context, state string, code string) (string, error) {
	// Atomically claim session by state to prevent concurrent callback races
	session, err := p.configStore.ClaimOauthUserSessionByState(ctx, state)
	if err != nil {
		return "", fmt.Errorf("failed to claim per-user oauth session: %w", err)
	}
	if session == nil {
		// State not found or already claimed — not a per-user session
		return "", schemas.ErrOAuth2NotPerUserSession
	}

	// Check expiry. Flow rows are deleted on every terminal transition
	// (authorized / failed / expired) so the table doesn't accumulate
	// dead rows. The UI sees 404 on flow-detail and renders "expired
	// or already completed" — no audit trail to preserve here.
	if time.Now().After(session.ExpiresAt) {
		p.cleanupFlow(ctx, session.ID)
		return "", fmt.Errorf("per-user oauth flow expired")
	}

	// Load template OAuth config for token_url, client_id, etc. Split the
	// nil-config case from a real lookup failure — otherwise wrapping a nil
	// error with %w produces a useless "%!w(<nil>)" string and the real cause
	// (config row missing) is hidden.
	templateConfig, err := p.configStore.GetOauthConfigByID(ctx, session.OauthConfigID)
	if err != nil {
		p.cleanupFlow(ctx, session.ID)
		return "", fmt.Errorf("failed to load template oauth config: %w", err)
	}
	if templateConfig == nil {
		_ = p.configStore.DeleteOauthUserSession(ctx, session.ID)
		return "", schemas.ErrOAuth2ConfigNotFound
	}
	// Exchange code for tokens with PKCE verifier
	// Use the redirect URI stored in the session (same one used in authorize step)
	// to satisfy OAuth spec requirement that redirect_uri must match
	redirectURI := session.RedirectURI
	if redirectURI == "" {
		redirectURI = templateConfig.RedirectURI
	}
	tokenResponse, err := p.exchangeCodeForTokensWithPKCE(
		ctx,
		templateConfig.TokenURL,
		code,
		templateConfig.GetResolvedClientID(),
		templateConfig.GetResolvedClientSecret(),
		redirectURI,
		session.CodeVerifier,
		strings.TrimSpace(templateConfig.Resource),
	)
	if err != nil {
		p.cleanupFlow(ctx, session.ID)
		return "", fmt.Errorf("per-user token exchange failed: %w", err)
	}

	// SessionID is carried from the flow row as-is: populated for session-mode
	// flows (the caller's x-bf-mcp-session-id) and empty for vk/user-mode.
	// No fallback generation — identity for vk/user lives in their own columns.
	sessionID := session.SessionID

	// Parse scopes
	var scopes []string
	if tokenResponse.Scope != "" {
		scopes = strings.Split(tokenResponse.Scope, " ")
	}
	scopesJSON, _ := json.Marshal(scopes)

	// Resolve identity columns from the flow row, honoring its flow_mode. Only
	// the column that matches the mode is populated on the token row; the others
	// stay nil to maintain the single-identity invariant.
	flowMode := schemas.MCPAuthMode(session.FlowMode)
	var (
		tokenVKID   *string
		tokenUserID *string
	)
	switch flowMode {
	case schemas.MCPAuthModeUser:
		tokenUserID = session.UserID
		if tokenUserID == nil || *tokenUserID == "" {
			p.cleanupFlow(ctx, session.ID)
			return "", fmt.Errorf("user-mode oauth flow has no user_id at completion")
		}
	case schemas.MCPAuthModeVK:
		tokenVKID = session.VirtualKeyID
		if tokenVKID == nil || *tokenVKID == "" {
			p.cleanupFlow(ctx, session.ID)
			return "", fmt.Errorf("vk-mode oauth flow has no virtual_key_id at completion")
		}
	case schemas.MCPAuthModeSession:
		// Both identity columns left nil; row is keyed by session_id.
	default:
		// Bogus flow_mode on the row — drop it so a fresh flow can start
		// instead of leaving the row stuck in 'claiming' forever.
		_ = p.configStore.DeleteOauthUserSession(ctx, session.ID)
		return "", fmt.Errorf("invalid flow mode on session: %s", session.ID)
	}

	// Create per-user OAuth token record
	var expiresAt *time.Time
	if tokenResponse.ExpiresIn > 0 {
		exp := time.Now().Add(time.Duration(tokenResponse.ExpiresIn) * time.Second)
		expiresAt = bifrost.Ptr(exp)
	}
	tokenRecord := &tables.TableMCPOauthToken{
		ID:            uuid.New().String(),
		SessionID:     sessionID,
		VirtualKeyID:  tokenVKID,
		UserID:        tokenUserID,
		MCPClientID:   session.MCPClientID,
		OauthConfigID: session.OauthConfigID,
		AccessToken:   strings.TrimSpace(tokenResponse.AccessToken),
		RefreshToken:  strings.TrimSpace(tokenResponse.RefreshToken),
		TokenType:     tokenResponse.TokenType,
		ExpiresAt:     expiresAt,
		Scopes:        string(scopesJSON),
		AuthMode:      string(flowMode),
		Status:        "active",
	}
	if err := p.configStore.CreateOauthToken(ctx, tokenRecord); err != nil {
		// The flow row has already been claimed and the auth code has been
		// exchanged; leaving the row in 'claiming' state would block the
		// next initiation for this binding indefinitely. Drop it so the
		// caller can restart cleanly.
		_ = p.configStore.DeleteOauthUserSession(ctx, session.ID)
		return "", fmt.Errorf("failed to create per-user oauth token: %w", err)
	}
	// CreateOauthToken upserts by binding and rewrites tokenRecord.ID to the
	// reused row's ID when one existed, so this evicts exactly the row that
	// was written; the next lookup for this binding reads the new credential.
	p.EvictUserTokenByID(tokenRecord.ID)

	// Token row is written; the flow row's purpose ends here. cleanupFlow
	// deletes both the flow row (transient PKCE/state carrier — the token
	// row is the durable record now) and the mcp_auth temp token bound to
	// it, so the auth-page link stops working immediately. The UI shows
	// "expired or already completed" on the resulting 404, which is the
	// truthful read.
	p.cleanupFlow(ctx, session.ID)

	logger.Debug("Per-user OAuth flow completed: session_id=%s, mcp_client_id=%s", session.ID, session.MCPClientID)

	return sessionID, nil
}

// GetUserAccessTokenByMode retrieves the upstream access token using exactly
// one identity column determined by mode. No fallback chain. Filters
// status='active' so orphaned rows never satisfy a lookup.
//
// Reads are served from an in-memory cache when possible: a cached entry
// whose expiry has passed is dropped and the lookup falls through to the
// database path, which owns every expiry and refresh decision. Failed
// lookups are never cached, so a caller completing OAuth (or a token being
// reactivated) becomes visible on the very next call.
func (p *OAuth2Provider) GetUserAccessTokenByMode(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) (string, error) {
	key := userTokenCacheKey(mode, identity, mcpClientID, "")
	if cached, ok := p.userTokens.Get(key); ok {
		return cached.accessToken, nil
	}
	cached, err := p.userTokens.Fill(ctx, key, func() (cachedUserToken, error) {
		return p.loadUserAccessTokenByMode(ctx, mode, identity, mcpClientID)
	})
	if err != nil {
		return "", err
	}
	return cached.accessToken, nil
}

// loadUserAccessTokenByMode is GetUserAccessTokenByMode's cache-miss path:
// the full database lookup, including the lazy pre-flight refresh for a
// known-expired row with a refresh token available.
func (p *OAuth2Provider) loadUserAccessTokenByMode(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) (cachedUserToken, error) {
	token, err := p.configStore.GetOauthUserTokenByMode(ctx, mode, identity, mcpClientID)
	if err != nil {
		return cachedUserToken{}, fmt.Errorf("failed to load per-user oauth token (mode=%s): %w", mode, err)
	}
	if token == nil {
		return cachedUserToken{}, schemas.ErrOAuth2TokenNotFound
	}

	// Refresh only when known-expired and refresh token exists.
	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) && strings.TrimSpace(token.RefreshToken) != "" {
		if err := p.RefreshAccessToken(ctx, token.ID); err != nil {
			return cachedUserToken{}, fmt.Errorf("per-user token expired and refresh failed: %w", err)
		}
		token, err = p.configStore.GetOauthUserTokenByMode(ctx, mode, identity, mcpClientID)
		if err != nil || token == nil {
			return cachedUserToken{}, fmt.Errorf("failed to reload per-user token after refresh")
		}
	}
	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) {
		return cachedUserToken{}, fmt.Errorf("per-user token expired and no refresh token is available; re-authorization required: %w", schemas.ErrOAuth2TokenExpired)
	}

	accessToken := strings.TrimSpace(token.AccessToken)
	if accessToken == "" {
		return cachedUserToken{}, fmt.Errorf("per-user access token is empty after sanitization")
	}
	return cachedUserToken{tokenID: token.ID, accessToken: accessToken, expiresAt: token.ExpiresAt}, nil
}

// EvictUserToken drops the cached access token for one (mode, identity,
// mcp client) binding. Side-effect only and safe to call when the cache is
// absent; the next lookup for the binding reads the database.
func (p *OAuth2Provider) EvictUserToken(mode schemas.MCPAuthMode, identity, mcpClientID string) {
	if p == nil {
		return
	}
	p.userTokens.Evict(userTokenCacheKey(mode, identity, mcpClientID, ""))
}

// EvictUserTokenByID drops the cached access token backed by the given token
// row ID, if any binding currently holds it. Side-effect only and safe to
// call when the cache is absent or the ID is not cached.
func (p *OAuth2Provider) EvictUserTokenByID(tokenID string) {
	if p == nil {
		return
	}
	p.userTokens.EvictByTokenID(tokenID)
}

// EvictUserTokensByMCPClient drops every cached access token bound to the
// given MCP client, across all auth modes and identities. Used after
// client-level mutations that invalidate its token rows as a set, such as
// credential rotation, access reconciliation, or client deletion.
// Side-effect only and safe to call when the cache is absent.
func (p *OAuth2Provider) EvictUserTokensByMCPClient(mcpClientID string) {
	if p == nil {
		return
	}
	p.userTokens.EvictByMCPClient(mcpClientID)
}

// EvictUserTokensByVirtualKey drops every cached vk-mode access token bound
// to the given virtual key, across all MCP clients. Used after virtual key
// mutations that orphan or delete its token rows as a set. Side-effect only
// and safe to call when the cache is absent.
func (p *OAuth2Provider) EvictUserTokensByVirtualKey(virtualKeyID string) {
	if p == nil {
		return
	}
	p.userTokens.EvictByVirtualKey(virtualKeyID)
}

// EvictUserTokensByUser drops every cached user-mode access token bound to
// the given user, across all MCP clients. Used after user-level mutations
// that orphan or delete the user's token rows as a set. Side-effect only
// and safe to call when the cache is absent.
func (p *OAuth2Provider) EvictUserTokensByUser(userID string) {
	if p == nil {
		return
	}
	p.userTokens.EvictByUser(userID)
}

// FlushUserTokenCache drops every cached per-user access token. The coarse
// fallback for mutations whose blast radius cannot be scoped to one client
// or virtual key. Side-effect only.
func (p *OAuth2Provider) FlushUserTokenCache() {
	if p == nil {
		return
	}
	p.userTokens.Flush()
}
