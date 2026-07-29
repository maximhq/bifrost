package oauth2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// testConfigStore is a minimal in-memory implementation of configstore.ConfigStore
// for use in oauth2 tests. Embeds the interface so unneeded methods panic if called.
type testConfigStore struct {
	configstore.ConfigStore

	mu           sync.Mutex
	oauthConfigs map[string]*tables.TableOauthConfig
	oauthTokens  map[string]*tables.TableMCPOauthToken
	clientConfig *configstore.ClientConfig
}

func newTestConfigStore() *testConfigStore {
	return &testConfigStore{
		oauthConfigs: make(map[string]*tables.TableOauthConfig),
		oauthTokens:  make(map[string]*tables.TableMCPOauthToken),
	}
}

func (s *testConfigStore) GetOauthConfigByID(_ context.Context, id string) (*tables.TableOauthConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.oauthConfigs[id]
	if cfg == nil {
		return nil, nil
	}
	return bifrost.Ptr(*cfg), nil
}

func (s *testConfigStore) UpdateOauthConfig(_ context.Context, cfg *tables.TableOauthConfig, _ ...*gorm.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.oauthConfigs[cfg.ID] = bifrost.Ptr(*cfg)
	return nil
}

func (s *testConfigStore) GetOauthTokenByID(_ context.Context, id string) (*tables.TableMCPOauthToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token := s.oauthTokens[id]
	if token == nil {
		return nil, nil
	}
	return bifrost.Ptr(*token), nil
}

// GetSharedOauthTokenByConfigID resolves the shared-mode token row for a
// config — the test-double equivalent of the real store's
// (oauth_config_id, auth_mode='shared') lookup, the replacement for the
// retired TableOauthConfig.TokenID FK shortcut.
func (s *testConfigStore) GetSharedOauthTokenByConfigID(_ context.Context, oauthConfigID string) (*tables.TableMCPOauthToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, token := range s.oauthTokens {
		if token.OauthConfigID == oauthConfigID && token.AuthMode == "shared" {
			return bifrost.Ptr(*token), nil
		}
	}
	return nil, nil
}

func (s *testConfigStore) UpdateOauthToken(_ context.Context, token *tables.TableMCPOauthToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.oauthTokens[token.ID] = bifrost.Ptr(*token)
	return nil
}

// MarkOauthUserTokenNeedsReauthByID flips a token's status to 'needs_reauth',
// the test-double equivalent of the real store's method of the same name —
// which, despite the historical "UserToken" naming, is not scoped away from
// auth_mode='shared' (see its doc comment on the real ConfigStore interface).
func (s *testConfigStore) MarkOauthUserTokenNeedsReauthByID(_ context.Context, tokenID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	token := s.oauthTokens[tokenID]
	if token == nil {
		return nil
	}
	token.Status = "needs_reauth"
	return nil
}

func (s *testConfigStore) GetClientConfig(_ context.Context) (*configstore.ClientConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clientConfig == nil {
		return nil, nil
	}
	return bifrost.Ptr(*s.clientConfig), nil
}

func (s *testConfigStore) GetExpiringOauthTokens(_ context.Context, before time.Time, authModes []string) ([]*tables.TableMCPOauthToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var expiring []*tables.TableMCPOauthToken
	for _, token := range s.oauthTokens {
		if !slices.Contains(authModes, token.AuthMode) {
			continue
		}
		if token.ExpiresAt != nil && token.ExpiresAt.Before(before) {
			expiring = append(expiring, bifrost.Ptr(*token))
		}
	}
	return expiring, nil
}

// seedFixtures inserts an authorized oauth_config + shared-mode token pair
// into the store, linked via the token's OauthConfigID (the replacement for
// the retired TableOauthConfig.TokenID FK shortcut). The token expires 1
// minute from now so GetExpiringOauthTokens will find it.
func seedFixtures(t *testing.T, store *testConfigStore, tokenURL string) (oauthConfigID, tokenID string) {
	t.Helper()

	oauthConfigID = "test-oauth-config-id"
	store.oauthConfigs[oauthConfigID] = &tables.TableOauthConfig{
		ID:          oauthConfigID,
		ClientID:    schemas.NewSecretVar("test-client-id"),
		TokenURL:    tokenURL,
		RedirectURI: "http://localhost/callback",
		Scopes:      `["read"]`,
		Status:      "authorized",
	}

	tokenID = "test-token-id"
	store.oauthTokens[tokenID] = &tables.TableMCPOauthToken{
		ID:            tokenID,
		AuthMode:      "shared",
		OauthConfigID: oauthConfigID,
		Status:        "active",
		AccessToken:   "old-access-token",
		RefreshToken:  "refresh-token",
		TokenType:     "bearer",
		ExpiresAt:     new(time.Now().Add(1 * time.Minute)),
		Scopes:        "[]",
	}

	return oauthConfigID, tokenID
}

func newTestWorker(store *testConfigStore) *TokenRefreshWorker {
	noopLogger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	provider := NewOAuth2Provider(store, noopLogger)
	provider.retryBaseDelay = 1 * time.Millisecond // speed up retry backoff in tests
	return NewTokenRefreshWorker(provider, noopLogger)
}

func newTestProvider(store *testConfigStore) *OAuth2Provider {
	noopLogger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	provider := NewOAuth2Provider(store, noopLogger)
	provider.retryBaseDelay = 1 * time.Millisecond
	return provider
}

// TestRefreshAccessToken_CallerCancellationDoesNotAbortOtherWaiters exercises
// the race RefreshAccessToken's DoChan (rather than Do) shape exists to
// close: a caller's own context cancellation must only stop that caller from
// waiting, never abort the shared refresh work other concurrent callers for
// the same token are depending on.
func TestRefreshAccessToken_CallerCancellationDoesNotAbortOtherWaiters(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})
	var startedOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(started) })
		<-unblock
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access-token",
			"refresh_token": "new-refresh-token",
			"token_type":    "bearer",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	store := newTestConfigStore()
	_, tokenID := seedFixtures(t, store, server.URL+"/token")
	provider := newTestProvider(store)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	var leaderErr error
	go func() {
		defer wg.Done()
		leaderErr = provider.RefreshAccessToken(leaderCtx, tokenID)
	}()

	<-started
	// The leader disconnects before the upstream call finishes. Its own
	// RefreshAccessToken call returns immediately on this (it only selects
	// on its own ctx vs the shared result), without waiting for or
	// affecting the shared work still blocked in the handler below.
	cancelLeader()
	wg.Wait()

	// Only now let the shared refresh's upstream call actually complete.
	close(unblock)

	// A second caller for the same token, with its own live context, must
	// get the refresh's real result — not the leader's cancellation.
	followerErr := provider.RefreshAccessToken(context.Background(), tokenID)

	assert.ErrorIs(t, leaderErr, context.Canceled, "the leader itself should observe its own cancellation")
	assert.NoError(t, followerErr, "a follower with its own live context must not inherit the leader's cancellation")

	token, err := store.GetOauthTokenByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Equal(t, "new-access-token", token.AccessToken, "the shared refresh must have completed and persisted, unaffected by the leader's cancellation")
}

func TestTestConfigStore_GetExpiringOauthTokens(t *testing.T) {
	t.Run("ignores nil expiry tokens", func(t *testing.T) {
		store := newTestConfigStore()
		now := time.Now()
		before := now.Add(5 * time.Minute)

		store.oauthTokens["nil-expiry"] = &tables.TableMCPOauthToken{
			ID:           "nil-expiry",
			AuthMode:     "shared",
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			TokenType:    "bearer",
			ExpiresAt:    nil,
			Scopes:       "[]",
		}
		store.oauthTokens["expiring"] = &tables.TableMCPOauthToken{
			ID:           "expiring",
			AuthMode:     "shared",
			AccessToken:  "access-token-2",
			RefreshToken: "refresh-token-2",
			TokenType:    "bearer",
			ExpiresAt:    bifrost.Ptr(now.Add(1 * time.Minute)),
			Scopes:       "[]",
		}

		tokens, err := store.GetExpiringOauthTokens(context.Background(), before, []string{"shared"})
		require.NoError(t, err)
		require.Len(t, tokens, 1)
		assert.Equal(t, "expiring", tokens[0].ID)
	})

	t.Run("filters by the given auth modes", func(t *testing.T) {
		store := newTestConfigStore()
		now := time.Now()
		before := now.Add(5 * time.Minute)

		store.oauthTokens["shared-tok"] = &tables.TableMCPOauthToken{
			ID:           "shared-tok",
			AuthMode:     "shared",
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			TokenType:    "bearer",
			ExpiresAt:    bifrost.Ptr(now.Add(1 * time.Minute)),
			Scopes:       "[]",
		}
		store.oauthTokens["user-tok"] = &tables.TableMCPOauthToken{
			ID:           "user-tok",
			AuthMode:     "user",
			AccessToken:  "access-token-2",
			RefreshToken: "refresh-token-2",
			TokenType:    "bearer",
			ExpiresAt:    bifrost.Ptr(now.Add(1 * time.Minute)),
			Scopes:       "[]",
		}

		tokens, err := store.GetExpiringOauthTokens(context.Background(), before, []string{"shared"})
		require.NoError(t, err)
		require.Len(t, tokens, 1)
		assert.Equal(t, "shared-tok", tokens[0].ID)

		tokens, err = store.GetExpiringOauthTokens(context.Background(), before, []string{"shared", "user"})
		require.NoError(t, err)
		assert.Len(t, tokens, 2)
	})
}

// TestTokenRefreshWorker_DefaultAuthModes_ExcludesUserModeTokens confirms the
// worker's default AuthModes ({"shared"}) leaves a per-user (auth_mode=
// "user") token that's expiring untouched — the same coverage the previous
// hardcoded `auth_mode = 'shared'` SQL filter gave for free, now that the
// scoping lives in the worker's configurable field instead.
func TestTokenRefreshWorker_DefaultAuthModes_ExcludesUserModeTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access-token",
			"token_type":   "bearer",
			"expires_in":   3600,
		})
	}))
	defer server.Close()

	store := newTestConfigStore()
	oauthConfigID, sharedTokenID := seedFixtures(t, store, server.URL+"/token")

	userTokenID := "test-user-token-id"
	store.oauthTokens[userTokenID] = &tables.TableMCPOauthToken{
		ID:            userTokenID,
		AuthMode:      "user",
		OauthConfigID: oauthConfigID,
		Status:        "active",
		AccessToken:   "old-user-access-token",
		RefreshToken:  "user-refresh-token",
		TokenType:     "bearer",
		ExpiresAt:     bifrost.Ptr(time.Now().Add(1 * time.Minute)),
		Scopes:        "[]",
	}

	worker := newTestWorker(store)
	assert.Equal(t, []string{"shared"}, worker.AuthModes, "constructor must default AuthModes to shared-only")
	worker.refreshExpiredTokens(context.Background())

	sharedToken, err := store.GetOauthTokenByID(context.Background(), sharedTokenID)
	require.NoError(t, err)
	assert.Equal(t, "new-access-token", sharedToken.AccessToken, "shared token must be refreshed under the default auth modes")

	userToken, err := store.GetOauthTokenByID(context.Background(), userTokenID)
	require.NoError(t, err)
	assert.Equal(t, "old-user-access-token", userToken.AccessToken, "user-mode token must not be picked up under the default auth modes")
}

func TestMCPTempTokenAuthEnabled(t *testing.T) {
	store := newTestConfigStore()
	provider := NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))

	assert.False(t, provider.mcpTempTokenAuthEnabled(context.Background()))

	store.clientConfig = &configstore.ClientConfig{}
	assert.False(t, provider.mcpTempTokenAuthEnabled(context.Background()))

	store.clientConfig.MCPEnableTempTokenAuth = true
	assert.True(t, provider.mcpTempTokenAuthEnabled(context.Background()))
}

func TestBuildAuthorizeURLWithPKCE_IncludesResource(t *testing.T) {
	provider := NewOAuth2Provider(newTestConfigStore(), bifrost.NewDefaultLogger(schemas.LogLevelError))

	authURL := provider.buildAuthorizeURLWithPKCE(
		"https://auth.example.com/oauth/authorize?existing=1",
		"client-id",
		"https://bifrost.example.com/api/oauth/callback",
		"state-token",
		"challenge",
		[]string{"mcp:read", "mcp:write"},
		"https://mcp.cloudflare.com/mcp",
	)

	parsed, err := url.Parse(authURL)
	require.NoError(t, err)
	q := parsed.Query()
	assert.Equal(t, "1", q.Get("existing"))
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, "client-id", q.Get("client_id"))
	assert.Equal(t, "https://mcp.cloudflare.com/mcp", q.Get("resource"))
	assert.Equal(t, "mcp:read mcp:write", q.Get("scope"))
}

func TestBuildAuthorizeURLWithPKCE_OmitsEmptyResource(t *testing.T) {
	provider := NewOAuth2Provider(newTestConfigStore(), bifrost.NewDefaultLogger(schemas.LogLevelError))

	authURL := provider.buildAuthorizeURLWithPKCE(
		"https://auth.example.com/oauth/authorize",
		"client-id",
		"https://bifrost.example.com/api/oauth/callback",
		"state-token",
		"challenge",
		nil,
		"",
	)

	parsed, err := url.Parse(authURL)
	require.NoError(t, err)
	_, exists := parsed.Query()["resource"]
	assert.False(t, exists)
}

func TestExchangeCodeForTokensWithPKCE_IncludesResource(t *testing.T) {
	var got url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		got = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-token",
			"token_type":   "bearer",
		})
	}))
	defer server.Close()

	provider := NewOAuth2Provider(newTestConfigStore(), bifrost.NewDefaultLogger(schemas.LogLevelError))
	_, err := provider.exchangeCodeForTokensWithPKCE(
		context.Background(),
		server.URL,
		"auth-code",
		"client-id",
		"",
		"https://bifrost.example.com/api/oauth/callback",
		"verifier",
		"https://mcp.cloudflare.com/mcp",
	)

	require.NoError(t, err)
	assert.Equal(t, "authorization_code", got.Get("grant_type"))
	assert.Equal(t, "https://mcp.cloudflare.com/mcp", got.Get("resource"))
}

func TestExchangeCodeForTokensWithPKCE_OmitsEmptyResource(t *testing.T) {
	var got url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		got = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-token",
			"token_type":   "bearer",
		})
	}))
	defer server.Close()

	provider := NewOAuth2Provider(newTestConfigStore(), bifrost.NewDefaultLogger(schemas.LogLevelError))
	_, err := provider.exchangeCodeForTokensWithPKCE(
		context.Background(),
		server.URL,
		"auth-code",
		"client-id",
		"",
		"https://bifrost.example.com/api/oauth/callback",
		"verifier",
		"",
	)

	require.NoError(t, err)
	_, exists := got["resource"]
	assert.False(t, exists)
}

func TestExchangeRefreshToken_IncludesResource(t *testing.T) {
	var got url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		got = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access-token",
			"token_type":   "bearer",
		})
	}))
	defer server.Close()

	provider := NewOAuth2Provider(newTestConfigStore(), bifrost.NewDefaultLogger(schemas.LogLevelError))
	_, err := provider.exchangeRefreshToken(
		context.Background(),
		server.URL,
		"client-id",
		"",
		"refresh-token",
		"https://mcp.cloudflare.com/mcp",
	)

	require.NoError(t, err)
	assert.Equal(t, "refresh_token", got.Get("grant_type"))
	assert.Equal(t, "https://mcp.cloudflare.com/mcp", got.Get("resource"))
}

func TestExchangeRefreshToken_OmitsEmptyResource(t *testing.T) {
	var got url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		got = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access-token",
			"token_type":   "bearer",
		})
	}))
	defer server.Close()

	provider := NewOAuth2Provider(newTestConfigStore(), bifrost.NewDefaultLogger(schemas.LogLevelError))
	_, err := provider.exchangeRefreshToken(
		context.Background(),
		server.URL,
		"client-id",
		"",
		"refresh-token",
		"",
	)

	require.NoError(t, err)
	_, exists := got["resource"]
	assert.False(t, exists)
}

func TestTokenRefreshWorker_TransientError_DoesNotMarkNeedsReauth(t *testing.T) {
	// A 503 response from the token server is a transient failure.
	// The token must stay "active" so the connection can heal
	// automatically when the server recovers.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	store := newTestConfigStore()
	_, tokenID := seedFixtures(t, store, server.URL+"/token")

	worker := newTestWorker(store)
	worker.refreshExpiredTokens(context.Background())

	token, err := store.GetOauthTokenByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Equal(t, "active", token.Status, "transient server error must not mark token needs_reauth")
}

func TestTokenRefreshWorker_PermanentError_MarksNeedsReauth(t *testing.T) {
	// A 401 invalid_grant response is a permanent rejection from the auth server.
	// The token must be marked "needs_reauth" to prompt the user to re-authorize.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_grant",
			"error_description": "Refresh token expired or revoked",
		})
	}))
	defer server.Close()

	store := newTestConfigStore()
	_, tokenID := seedFixtures(t, store, server.URL+"/token")

	worker := newTestWorker(store)
	worker.refreshExpiredTokens(context.Background())

	token, err := store.GetOauthTokenByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Equal(t, "needs_reauth", token.Status, "permanent auth rejection must mark token needs_reauth")
}

func TestTokenRefreshWorker_SuccessfulRefresh_UpdatesToken(t *testing.T) {
	// A successful refresh must update the stored access token and
	// leave the token status as "active".
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access-token",
			"refresh_token": "new-refresh-token",
			"token_type":    "bearer",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	store := newTestConfigStore()
	_, tokenID := seedFixtures(t, store, server.URL+"/token")

	worker := newTestWorker(store)
	worker.refreshExpiredTokens(context.Background())

	token, err := store.GetOauthTokenByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Equal(t, "active", token.Status)
	assert.Equal(t, "new-access-token", token.AccessToken)
}

func TestTokenRefreshWorker_ConnectionRefused_DoesNotMarkNeedsReauth(t *testing.T) {
	// This is the exact failure mode that triggered this fix: the machine goes
	// offline, DNS fails, and the token endpoint is unreachable. The transport
	// error (client.Do fails) must not mark the token needs_reauth.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	tokenURL := server.URL + "/token"
	server.Close() // close immediately so all connection attempts are refused

	store := newTestConfigStore()
	_, tokenID := seedFixtures(t, store, tokenURL)

	worker := newTestWorker(store)
	worker.refreshExpiredTokens(context.Background())

	token, err := store.GetOauthTokenByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Equal(t, "active", token.Status, "connection refused must not mark token needs_reauth")
}

func TestTokenRefreshWorker_400InvalidGrant_MarksNeedsReauth(t *testing.T) {
	// 400 invalid_grant is the canonical RFC 6749 signal that a refresh token
	// has been revoked. Must mark the token needs_reauth.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_grant",
			"error_description": "The refresh token has been revoked",
		})
	}))
	defer server.Close()

	store := newTestConfigStore()
	_, tokenID := seedFixtures(t, store, server.URL+"/token")

	worker := newTestWorker(store)
	worker.refreshExpiredTokens(context.Background())

	token, err := store.GetOauthTokenByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Equal(t, "needs_reauth", token.Status, "400 invalid_grant must mark token needs_reauth")
}

func TestTokenRefreshWorker_429RateLimit_DoesNotMarkNeedsReauth(t *testing.T) {
	// 429 Too Many Requests is a transient rate limit — not a permanent auth
	// rejection. Must not mark the token needs_reauth.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	store := newTestConfigStore()
	_, tokenID := seedFixtures(t, store, server.URL+"/token")

	worker := newTestWorker(store)
	worker.refreshExpiredTokens(context.Background())

	token, err := store.GetOauthTokenByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Equal(t, "active", token.Status, "429 rate limit must not mark token needs_reauth")
}

func TestTokenRefreshWorker_400InvalidRequest_DoesNotMarkNeedsReauth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_request",
			"error_description": "Missing required parameter",
		})
	}))
	defer server.Close()

	store := newTestConfigStore()
	_, tokenID := seedFixtures(t, store, server.URL+"/token")

	worker := newTestWorker(store)
	worker.refreshExpiredTokens(context.Background())

	token, err := store.GetOauthTokenByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Equal(t, "active", token.Status, "400 invalid_request must not mark token needs_reauth")
}

func TestTokenRefreshWorker_400UnauthorizedClient_MarksNeedsReauth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error":             "unauthorized_client",
			"error_description": "Client is not authorized for this grant type",
		})
	}))
	defer server.Close()

	store := newTestConfigStore()
	_, tokenID := seedFixtures(t, store, server.URL+"/token")

	worker := newTestWorker(store)
	worker.refreshExpiredTokens(context.Background())

	token, err := store.GetOauthTokenByID(context.Background(), tokenID)
	require.NoError(t, err)
	assert.Equal(t, "needs_reauth", token.Status, "400 unauthorized_client must mark token needs_reauth")
}
