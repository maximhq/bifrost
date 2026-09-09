package oauth2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// newAccessTokenTestStore seeds an authorized oauth_config row (no token yet)
// pointed at tokenURL for refresh calls. Mirrors seedFixtures' config shape.
func newAccessTokenTestStore(tokenURL string) (*testConfigStore, string) {
	store := newTestConfigStore()
	oauthConfigID := "cfg-access-token"
	store.oauthConfigs[oauthConfigID] = &tables.TableOauthConfig{
		ID:          oauthConfigID,
		ClientID:    schemas.NewSecretVar("test-client-id"),
		TokenURL:    tokenURL,
		RedirectURI: "http://localhost/callback",
		Scopes:      `["read"]`,
		Status:      "authorized",
	}
	return store, oauthConfigID
}

// seedAccessToken inserts a token row of the given authMode ("shared" or
// "admin") linked to oauthConfigID.
func seedAccessToken(store *testConfigStore, oauthConfigID, authMode, tokenID, accessToken, refreshToken string, expiresAt *time.Time) {
	store.oauthTokens[tokenID] = &tables.TableMCPOauthToken{
		ID:            tokenID,
		AuthMode:      authMode,
		OauthConfigID: oauthConfigID,
		// Admin rows are looked up by mcp_client_id (the key every admin row
		// carries); the tests reuse the same identifier for both lookups.
		MCPClientID: oauthConfigID,
		Status:      "active",
		AccessToken:   accessToken,
		RefreshToken:  refreshToken,
		TokenType:     "Bearer",
		ExpiresAt:     expiresAt,
		Scopes:        "[]",
	}
}

// accessTokenGetter abstracts over GetAccessToken and GetAdminAccessToken so
// the four resolution cases below (active / expired-with-refresh /
// expired-without-refresh / missing token) run once per caller and pin that
// both funnel through the same resolveAccessToken behavior.
type accessTokenGetter struct {
	name     string
	authMode string
	get      func(p *OAuth2Provider, ctx context.Context, oauthConfigID string) (string, error)
}

var accessTokenGetters = []accessTokenGetter{
	{
		name:     "GetAccessToken",
		authMode: "shared",
		get: func(p *OAuth2Provider, ctx context.Context, oauthConfigID string) (string, error) {
			return p.GetAccessToken(ctx, oauthConfigID)
		},
	},
	{
		name:     "GetAdminAccessToken",
		authMode: "admin",
		get: func(p *OAuth2Provider, ctx context.Context, oauthConfigID string) (string, error) {
			return p.GetAdminAccessToken(ctx, oauthConfigID)
		},
	},
}

// TestAccessToken_ActiveToken_ReturnsItDirectly verifies a live, non-expired
// token is returned as-is (sanitized), with no refresh call made — for both
// GetAccessToken (shared-mode) and GetAdminAccessToken (admin-mode), since
// both funnel through the same resolveAccessToken helper.
func TestAccessToken_ActiveToken_ReturnsItDirectly(t *testing.T) {
	for _, g := range accessTokenGetters {
		t.Run(g.name, func(t *testing.T) {
			refreshCalled := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				refreshCalled = true
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"access_token": "should-not-be-used", "token_type": "bearer"})
			}))
			defer server.Close()

			store, oauthConfigID := newAccessTokenTestStore(server.URL + "/token")
			seedAccessToken(store, oauthConfigID, g.authMode, "tok-1", " current-access-token \n", "refresh-token", bifrost.Ptr(time.Now().Add(time.Hour)))

			provider := NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))
			token, err := g.get(provider, context.Background(), oauthConfigID)
			require.NoError(t, err)
			assert.Equal(t, "current-access-token", token, "must return the sanitized (trimmed) access token")
			assert.False(t, refreshCalled, "an unexpired token must not trigger a refresh call")
		})
	}
}

// TestAccessToken_ExpiredTokenWithRefresh_RefreshesAndReturnsNewToken
// verifies an expired token with a refresh token available is refreshed via
// the upstream token endpoint and the new access token is returned.
func TestAccessToken_ExpiredTokenWithRefresh_RefreshesAndReturnsNewToken(t *testing.T) {
	for _, g := range accessTokenGetters {
		t.Run(g.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"access_token": "refreshed-access-token",
					"token_type":   "bearer",
					"expires_in":   3600,
				})
			}))
			defer server.Close()

			store, oauthConfigID := newAccessTokenTestStore(server.URL + "/token")
			seedAccessToken(store, oauthConfigID, g.authMode, "tok-1", "old-access-token", "refresh-token", bifrost.Ptr(time.Now().Add(-time.Minute)))

			provider := NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))
			token, err := g.get(provider, context.Background(), oauthConfigID)
			require.NoError(t, err)
			assert.Equal(t, "refreshed-access-token", token)

			stored, err := store.GetOauthTokenByID(context.Background(), "tok-1")
			require.NoError(t, err)
			assert.Equal(t, "refreshed-access-token", stored.AccessToken, "the reload-after-refresh path must persist the new token")
		})
	}
}

// TestAccessToken_ExpiredTokenNoRefresh_ReturnsExpiredError verifies an
// expired token with no refresh token available returns ErrOAuth2TokenExpired
// instead of attempting a refresh call.
func TestAccessToken_ExpiredTokenNoRefresh_ReturnsExpiredError(t *testing.T) {
	for _, g := range accessTokenGetters {
		t.Run(g.name, func(t *testing.T) {
			refreshCalled := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				refreshCalled = true
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			store, oauthConfigID := newAccessTokenTestStore(server.URL + "/token")
			seedAccessToken(store, oauthConfigID, g.authMode, "tok-1", "old-access-token", "", bifrost.Ptr(time.Now().Add(-time.Minute)))

			provider := NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))
			token, err := g.get(provider, context.Background(), oauthConfigID)
			require.Error(t, err)
			assert.ErrorIs(t, err, schemas.ErrOAuth2TokenExpired)
			assert.Empty(t, token)
			assert.False(t, refreshCalled, "no refresh token available must short-circuit locally, never reach the token endpoint")
		})
	}
}

// TestAccessToken_MissingToken_ReturnsError verifies that when the oauth
// config exists but has no token row of the caller's auth mode, a plain
// (non-sentinel) "no token linked" error is returned — even when an active
// token exists under the *other* auth mode, which pins the shared/admin
// credential boundary: a resolver bug that ignored auth_mode entirely would
// otherwise still pass this test with no token row seeded at all.
func TestAccessToken_MissingToken_ReturnsError(t *testing.T) {
	for _, g := range accessTokenGetters {
		t.Run(g.name, func(t *testing.T) {
			store, oauthConfigID := newAccessTokenTestStore("http://unused/token")
			// No token row seeded for this case's own auth mode, but one is
			// seeded under the opposite mode so the resolver can't accidentally
			// "succeed" by ignoring auth_mode.
			otherMode := "shared"
			if g.authMode == "shared" {
				otherMode = "admin"
			}
			seedAccessToken(store, oauthConfigID, otherMode, "tok-other-mode", "other-mode-token", "refresh-token", bifrost.Ptr(time.Now().Add(time.Hour)))

			provider := NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))
			token, err := g.get(provider, context.Background(), oauthConfigID)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "no")
			assert.Contains(t, err.Error(), "token", "a missing row must surface as a plain no-token error")
			assert.Empty(t, token)
		})
	}
}

// TestAccessToken_InactiveTokenStatus_ReturnsExpiredErrorWithoutNetworkCall
// verifies that a token whose own Status is not "active" (e.g. needs_reauth)
// is rejected locally, regardless of ExpiresAt — resolveAccessToken's Status
// check runs before the expiry/refresh check.
func TestAccessToken_InactiveTokenStatus_ReturnsExpiredErrorWithoutNetworkCall(t *testing.T) {
	for _, g := range accessTokenGetters {
		t.Run(g.name, func(t *testing.T) {
			refreshCalled := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				refreshCalled = true
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			store, oauthConfigID := newAccessTokenTestStore(server.URL + "/token")
			seedAccessToken(store, oauthConfigID, g.authMode, "tok-1", "old-access-token", "refresh-token", bifrost.Ptr(time.Now().Add(time.Hour)))
			store.oauthTokens["tok-1"].Status = "needs_reauth"

			provider := NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))
			token, err := g.get(provider, context.Background(), oauthConfigID)
			require.Error(t, err)
			assert.ErrorIs(t, err, schemas.ErrOAuth2TokenExpired)
			assert.Empty(t, token)
			assert.False(t, refreshCalled)
		})
	}
}

// TestGetAccessToken_MissingConfig_ReturnsConfigNotFoundError pins
// GetAccessToken's own pre-check (not shared with GetAdminAccessToken, which
// has no equivalent oauth_config lookup): a nonexistent oauth_config_id
// returns the ErrOAuth2ConfigNotFound sentinel.
func TestGetAccessToken_MissingConfig_ReturnsConfigNotFoundError(t *testing.T) {
	store := newTestConfigStore()
	provider := NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))

	token, err := provider.GetAccessToken(context.Background(), "does-not-exist")
	require.Error(t, err)
	assert.ErrorIs(t, err, schemas.ErrOAuth2ConfigNotFound)
	assert.Empty(t, token)
}
