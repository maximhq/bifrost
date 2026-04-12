package oauth2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// testConfigStore is a minimal in-memory implementation of configstore.ConfigStore
// for use in oauth2 tests. Embeds the interface so unneeded methods panic if called.
type testConfigStore struct {
	configstore.ConfigStore

	mu           sync.Mutex
	oauthConfigs map[string]*tables.TableOauthConfig
	oauthTokens  map[string]*tables.TableOauthToken
}

func newTestConfigStore() *testConfigStore {
	return &testConfigStore{
		oauthConfigs: make(map[string]*tables.TableOauthConfig),
		oauthTokens:  make(map[string]*tables.TableOauthToken),
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

func (s *testConfigStore) GetOauthConfigByTokenID(_ context.Context, tokenID string) (*tables.TableOauthConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cfg := range s.oauthConfigs {
		if cfg.TokenID != nil && *cfg.TokenID == tokenID {
			return bifrost.Ptr(*cfg), nil
		}
	}
	return nil, nil
}

func (s *testConfigStore) UpdateOauthConfig(_ context.Context, cfg *tables.TableOauthConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.oauthConfigs[cfg.ID] = bifrost.Ptr(*cfg)
	return nil
}

func (s *testConfigStore) GetOauthTokenByID(_ context.Context, id string) (*tables.TableOauthToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token := s.oauthTokens[id]
	if token == nil {
		return nil, nil
	}
	return bifrost.Ptr(*token), nil
}

func (s *testConfigStore) UpdateOauthToken(_ context.Context, token *tables.TableOauthToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.oauthTokens[token.ID] = bifrost.Ptr(*token)
	return nil
}

func (s *testConfigStore) GetExpiringOauthTokens(_ context.Context, before time.Time) ([]*tables.TableOauthToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var expiring []*tables.TableOauthToken
	for _, token := range s.oauthTokens {
		if token.ExpiresAt.Before(before) {
			expiring = append(expiring, bifrost.Ptr(*token))
		}
	}
	return expiring, nil
}

// seedFixtures inserts an authorized oauth_config + token pair into the store.
// The token expires 1 minute from now so GetExpiringOauthTokens will find it.
func seedFixtures(t *testing.T, store *testConfigStore, tokenURL string) (oauthConfigID string) {
	t.Helper()

	tokenID := "test-token-id"
	store.oauthTokens[tokenID] = &tables.TableOauthToken{
		ID:           tokenID,
		AccessToken:  "old-access-token",
		RefreshToken: "refresh-token",
		TokenType:    "bearer",
		ExpiresAt:    time.Now().Add(1 * time.Minute),
		Scopes:       "[]",
	}

	oauthConfigID = "test-oauth-config-id"
	store.oauthConfigs[oauthConfigID] = &tables.TableOauthConfig{
		ID:          oauthConfigID,
		ClientID:    "test-client-id",
		TokenURL:    tokenURL,
		RedirectURI: "http://localhost/callback",
		Scopes:      `["read"]`,
		Status:      "authorized",
		TokenID:     bifrost.Ptr(tokenID),
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	}

	return oauthConfigID
}

func newTestWorker(store *testConfigStore) *TokenRefreshWorker {
	noopLogger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	provider := NewOAuth2Provider(store, noopLogger)
	provider.retryBaseDelay = 1 * time.Millisecond // speed up retry backoff in tests
	return NewTokenRefreshWorker(provider, noopLogger)
}

func TestTokenRefreshWorker_TransientError_DoesNotMarkExpired(t *testing.T) {
	// A 503 response from the token server is a transient failure.
	// The oauth_config must stay "authorized" so the connection can
	// heal automatically when the server recovers.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	store := newTestConfigStore()
	oauthConfigID := seedFixtures(t, store, server.URL+"/token")

	worker := newTestWorker(store)
	worker.refreshExpiredTokens(context.Background())

	cfg, err := store.GetOauthConfigByID(context.Background(), oauthConfigID)
	require.NoError(t, err)
	assert.Equal(t, "authorized", cfg.Status, "transient server error must not mark config as expired")
}

func TestTokenRefreshWorker_PermanentError_MarksExpired(t *testing.T) {
	// A 401 invalid_grant response is a permanent rejection from the auth server.
	// The oauth_config must be marked "expired" to prompt the user to re-authorize.
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
	oauthConfigID := seedFixtures(t, store, server.URL+"/token")

	worker := newTestWorker(store)
	worker.refreshExpiredTokens(context.Background())

	cfg, err := store.GetOauthConfigByID(context.Background(), oauthConfigID)
	require.NoError(t, err)
	assert.Equal(t, "expired", cfg.Status, "permanent auth rejection must mark config as expired")
}

func TestTokenRefreshWorker_SuccessfulRefresh_UpdatesToken(t *testing.T) {
	// A successful refresh must update the stored access token and
	// leave the oauth_config status as "authorized".
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
	oauthConfigID := seedFixtures(t, store, server.URL+"/token")

	worker := newTestWorker(store)
	worker.refreshExpiredTokens(context.Background())

	cfg, err := store.GetOauthConfigByID(context.Background(), oauthConfigID)
	require.NoError(t, err)
	assert.Equal(t, "authorized", cfg.Status)

	token, err := store.GetOauthTokenByID(context.Background(), *cfg.TokenID)
	require.NoError(t, err)
	assert.Equal(t, "new-access-token", token.AccessToken)
}

func TestTokenRefreshWorker_ConnectionRefused_DoesNotMarkExpired(t *testing.T) {
	// This is the exact failure mode that triggered this fix: the machine goes
	// offline, DNS fails, and the token endpoint is unreachable. The transport
	// error (client.Do fails) must not mark the config expired.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	tokenURL := server.URL + "/token"
	server.Close() // close immediately so all connection attempts are refused

	store := newTestConfigStore()
	oauthConfigID := seedFixtures(t, store, tokenURL)

	worker := newTestWorker(store)
	worker.refreshExpiredTokens(context.Background())

	cfg, err := store.GetOauthConfigByID(context.Background(), oauthConfigID)
	require.NoError(t, err)
	assert.Equal(t, "authorized", cfg.Status, "connection refused must not mark config as expired")
}

func TestTokenRefreshWorker_400InvalidGrant_MarksExpired(t *testing.T) {
	// 400 invalid_grant is the canonical RFC 6749 signal that a refresh token
	// has been revoked. Must mark the config expired.
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
	oauthConfigID := seedFixtures(t, store, server.URL+"/token")

	worker := newTestWorker(store)
	worker.refreshExpiredTokens(context.Background())

	cfg, err := store.GetOauthConfigByID(context.Background(), oauthConfigID)
	require.NoError(t, err)
	assert.Equal(t, "expired", cfg.Status, "400 invalid_grant must mark config as expired")
}

func TestTokenRefreshWorker_429RateLimit_DoesNotMarkExpired(t *testing.T) {
	// 429 Too Many Requests is a transient rate limit — not a permanent auth
	// rejection. Must not mark the config expired.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	store := newTestConfigStore()
	oauthConfigID := seedFixtures(t, store, server.URL+"/token")

	worker := newTestWorker(store)
	worker.refreshExpiredTokens(context.Background())

	cfg, err := store.GetOauthConfigByID(context.Background(), oauthConfigID)
	require.NoError(t, err)
	assert.Equal(t, "authorized", cfg.Status, "429 rate limit must not mark config as expired")
}

func TestTokenRefreshWorker_400InvalidRequest_DoesNotMarkExpired(t *testing.T) {
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
	oauthConfigID := seedFixtures(t, store, server.URL+"/token")

	worker := newTestWorker(store)
	worker.refreshExpiredTokens(context.Background())

	cfg, err := store.GetOauthConfigByID(context.Background(), oauthConfigID)
	require.NoError(t, err)
	assert.Equal(t, "authorized", cfg.Status, "400 invalid_request must not mark config as expired")
}

func TestTokenRefreshWorker_400UnauthorizedClient_MarksExpired(t *testing.T) {
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
	oauthConfigID := seedFixtures(t, store, server.URL+"/token")

	worker := newTestWorker(store)
	worker.refreshExpiredTokens(context.Background())

	cfg, err := store.GetOauthConfigByID(context.Background(), oauthConfigID)
	require.NoError(t, err)
	assert.Equal(t, "expired", cfg.Status, "400 unauthorized_client must mark config as expired")
}
