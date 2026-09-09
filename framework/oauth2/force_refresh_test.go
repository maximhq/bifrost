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

// GetOauthUserTokenByMode is the test-double equivalent of the real store's
// per-identity lookup (framework/configstore.RDBConfigStore's method of the
// same name): scoped to (auth_mode, identity-for-that-mode, mcp_client_id)
// and, mirroring the real implementation, only returns rows whose Status is
// "active".
func (s *testConfigStore) GetOauthUserTokenByMode(_ context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) (*tables.TableMCPOauthToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, token := range s.oauthTokens {
		if token.MCPClientID != mcpClientID || token.Status != "active" {
			continue
		}
		switch mode {
		case schemas.MCPAuthModeUser:
			if token.AuthMode == "user" && token.UserID != nil && *token.UserID == identity {
				return bifrost.Ptr(*token), nil
			}
		case schemas.MCPAuthModeVK:
			if token.AuthMode == "vk" && token.VirtualKeyID != nil && *token.VirtualKeyID == identity {
				return bifrost.Ptr(*token), nil
			}
		case schemas.MCPAuthModeSession:
			if token.AuthMode == "session" && token.SessionID == identity {
				return bifrost.Ptr(*token), nil
			}
		}
	}
	return nil, nil
}

func newForceRefreshTestProvider(store *testConfigStore) *OAuth2Provider {
	return NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))
}

func tokenRefreshServer(t *testing.T, accessToken string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": accessToken,
			"token_type":   "bearer",
			"expires_in":   3600,
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// (a) MCPAuthTypeOauth resolves the shared token via config.OauthConfigID and
// calls through to a real refresh, exercising the real ForceRefreshAccessToken
// end to end (not a re-implementation of its logic).
func TestForceRefreshAccessToken_SharedOAuth_RefreshesResolvedToken(t *testing.T) {
	server := tokenRefreshServer(t, "forced-new-access-token")

	store := newTestConfigStore()
	oauthConfigID, tokenID := seedFixtures(t, store, server.URL+"/token")

	provider := newForceRefreshTestProvider(store)
	config := &schemas.MCPClientConfig{
		ID:            "test-mcp-client",
		Name:          "Test Client",
		AuthType:      schemas.MCPAuthTypeOauth,
		OauthConfigID: &oauthConfigID,
	}

	err := provider.ForceRefreshAccessToken(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), config)
	require.NoError(t, err)

	token, err := store.GetOauthTokenByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Equal(t, "forced-new-access-token", token.AccessToken, "ForceRefreshAccessToken must resolve the shared token and refresh it via the real RefreshAccessToken path")
}

// (b) MCPAuthTypeOauth with OauthConfigID nil/missing returns a sensible
// error instead of panicking.
func TestForceRefreshAccessToken_SharedOAuth_MissingOauthConfigID_ReturnsError(t *testing.T) {
	store := newTestConfigStore()
	provider := newForceRefreshTestProvider(store)

	t.Run("nil OauthConfigID", func(t *testing.T) {
		config := &schemas.MCPClientConfig{
			ID:       "test-mcp-client",
			Name:     "Test Client",
			AuthType: schemas.MCPAuthTypeOauth,
		}
		err := provider.ForceRefreshAccessToken(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), config)
		require.Error(t, err)
		assert.ErrorIs(t, err, schemas.ErrOAuth2ConfigNotFound)
	})

	t.Run("empty OauthConfigID", func(t *testing.T) {
		empty := ""
		config := &schemas.MCPClientConfig{
			ID:            "test-mcp-client",
			Name:          "Test Client",
			AuthType:      schemas.MCPAuthTypeOauth,
			OauthConfigID: &empty,
		}
		err := provider.ForceRefreshAccessToken(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), config)
		require.Error(t, err)
		assert.ErrorIs(t, err, schemas.ErrOAuth2ConfigNotFound)
	})
}

// Covers the fix for the status-check asymmetry: GetSharedOauthTokenByConfigID
// is not filtered by status (that's the caller's responsibility per its own
// doc comment), so a token already marked needs_reauth must short-circuit
// locally with ErrOAuth2TokenExpired, exactly like GetAccessToken's own
// status check does, instead of reaching the (doomed to fail) live refresh
// call.
func TestForceRefreshAccessToken_SharedOAuth_InactiveToken_ShortCircuitsWithoutNetworkCall(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "should-not-be-used", "token_type": "bearer"})
	}))
	defer server.Close()

	store := newTestConfigStore()
	oauthConfigID, tokenID := seedFixtures(t, store, server.URL+"/token")
	store.oauthTokens[tokenID].Status = "needs_reauth"

	provider := newForceRefreshTestProvider(store)
	config := &schemas.MCPClientConfig{
		ID:            "test-mcp-client",
		Name:          "Test Client",
		AuthType:      schemas.MCPAuthTypeOauth,
		OauthConfigID: &oauthConfigID,
	}

	err := provider.ForceRefreshAccessToken(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), config)
	require.Error(t, err)
	assert.ErrorIs(t, err, schemas.ErrOAuth2TokenExpired)
	assert.False(t, called, "an inactive token must short-circuit locally, never reach the refresh endpoint")
}

// (c) MCPAuthTypePerUserOauth resolves the per-user token via (mode,
// identity) derived from context and refreshes it via the real
// RefreshAccessToken path. Identity is carried via
// BifrostContextKeyMCPSessionID (session mode), the same context
// construction per_user_oauth_test.go's ConnectionHeaders coverage uses.
func TestForceRefreshAccessToken_PerUserOAuth_ResolvesAndRefreshesToken(t *testing.T) {
	server := tokenRefreshServer(t, "forced-new-user-access-token")

	store := newTestConfigStore()
	oauthConfigID, _ := seedFixtures(t, store, server.URL+"/token")

	mcpClientID := "test-mcp-client"
	userTokenID := "test-user-token-id"
	store.oauthTokens[userTokenID] = &tables.TableMCPOauthToken{
		ID:            userTokenID,
		AuthMode:      "session",
		MCPClientID:   mcpClientID,
		OauthConfigID: oauthConfigID,
		SessionID:     "sess-1",
		Status:        "active",
		AccessToken:   "old-user-access-token",
		RefreshToken:  "user-refresh-token",
		TokenType:     "bearer",
		ExpiresAt:     bifrost.Ptr(time.Now().Add(1 * time.Minute)),
		Scopes:        "[]",
	}

	provider := newForceRefreshTestProvider(store)
	config := &schemas.MCPClientConfig{
		ID:            mcpClientID,
		Name:          "Test Client",
		AuthType:      schemas.MCPAuthTypePerUserOauth,
		OauthConfigID: &oauthConfigID,
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyMCPSessionID, "sess-1")

	err := provider.ForceRefreshAccessToken(ctx, config)
	require.NoError(t, err)

	token, err := store.GetOauthTokenByID(context.Background(), userTokenID)
	require.NoError(t, err)
	assert.Equal(t, "forced-new-user-access-token", token.AccessToken, "ForceRefreshAccessToken must resolve the per-identity token from ctx and refresh it via the real RefreshAccessToken path")
}

// (d) MCPAuthTypePerUserOauth with no identity in context returns a sensible
// error instead of panicking.
func TestForceRefreshAccessToken_PerUserOAuth_NoIdentity_ReturnsError(t *testing.T) {
	store := newTestConfigStore()
	provider := newForceRefreshTestProvider(store)
	config := &schemas.MCPClientConfig{
		ID:       "test-mcp-client",
		Name:     "Test Client",
		AuthType: schemas.MCPAuthTypePerUserOauth,
	}

	// No BifrostContextKeyUserID / GovernanceVirtualKeyID / MCPSessionID set
	// -> ctx.MCPAuthMode() resolves to MCPAuthModeNone and ctx.MCPIdentity
	// returns "".
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	err := provider.ForceRefreshAccessToken(ctx, config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires an identity")
}

// (e) An unsupported/default AuthType returns the "unsupported auth type"
// error rather than silently no-op'ing or panicking.
func TestForceRefreshAccessToken_UnsupportedAuthType_ReturnsError(t *testing.T) {
	store := newTestConfigStore()
	provider := newForceRefreshTestProvider(store)
	config := &schemas.MCPClientConfig{
		ID:       "test-mcp-client",
		Name:     "Test Client",
		AuthType: schemas.MCPAuthTypeHeaders,
	}

	err := provider.ForceRefreshAccessToken(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "force-refresh is not supported")
}
