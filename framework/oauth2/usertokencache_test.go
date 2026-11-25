package oauth2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore/livestoretest"
)

func testToken(id, access string, expiresAt *time.Time) cachedUserToken {
	return cachedUserToken{tokenID: id, accessToken: access, expiresAt: expiresAt}
}

func fillWith(v cachedUserToken) func() (cachedUserToken, error) {
	return func() (cachedUserToken, error) { return v, nil }
}

func TestUserTokenCache_HitAndMiss(t *testing.T) {
	c := newTestTokenCache(t)

	_, ok := c.Get("missing")
	assert.False(t, ok, "empty cache must miss")

	v, err := c.Fill(context.Background(), "k1", fillWith(testToken("t1", "access-1", nil)))
	require.NoError(t, err)
	assert.Equal(t, "access-1", v.accessToken)

	got, ok := c.Get("k1")
	require.True(t, ok, "filled key must hit")
	assert.Equal(t, "access-1", got.accessToken)
	assert.Equal(t, "t1", got.tokenID)

	_, ok = c.Get("k2")
	assert.False(t, ok, "unrelated key must miss")
}

func TestUserTokenCache_ExpiredEntryIsMissAndRemoved(t *testing.T) {
	c := newTestTokenCache(t)
	past := time.Now().Add(-1 * time.Minute)

	_, err := c.Fill(context.Background(), "k1", fillWith(testToken("t1", "stale-access", &past)))
	require.NoError(t, err)
	assert.Equal(t, 0, c.Len(), "an expired token is never held")

	_, ok := c.Get("k1")
	assert.False(t, ok, "expired entry must read as a miss")
}

func TestUserTokenCache_EvictExactKey(t *testing.T) {
	c := newTestTokenCache(t)
	_, err := c.Fill(context.Background(), "k1", fillWith(testToken("t1", "access-1", nil)))
	require.NoError(t, err)

	c.Evict("k1")
	_, ok := c.Get("k1")
	assert.False(t, ok)
	assert.Equal(t, 0, c.Len())

	// Evicting an absent key is a no-op, not a panic.
	c.Evict("never-set")
}

func TestUserTokenCache_EvictByTokenID(t *testing.T) {
	c := newTestTokenCache(t)
	_, err := c.Fill(context.Background(), "k1", fillWith(testToken("t1", "access-1", nil)))
	require.NoError(t, err)
	_, err = c.Fill(context.Background(), "k2", fillWith(testToken("t2", "access-2", nil)))
	require.NoError(t, err)

	c.EvictByTokenID("t1")
	_, ok := c.Get("k1")
	assert.False(t, ok, "entry holding the evicted token ID must be gone")
	_, ok = c.Get("k2")
	assert.True(t, ok, "unrelated entry must survive")

	// Unknown token IDs are a no-op.
	c.EvictByTokenID("unknown")
	assert.Equal(t, 1, c.Len())
}

func TestUserTokenCache_EvictByMCPClient(t *testing.T) {
	c := newTestTokenCache(t)
	keyA1 := userTokenCacheKey(schemas.MCPAuthModeUser, "u1", "client-a", "")
	keyA2 := userTokenCacheKey(schemas.MCPAuthModeVK, "vk1", "client-a", "")
	keyB := userTokenCacheKey(schemas.MCPAuthModeUser, "u1", "client-b", "")
	for i, k := range []string{keyA1, keyA2, keyB} {
		_, err := c.Fill(context.Background(), k, fillWith(testToken(fmt.Sprintf("t%d", i), "access", nil)))
		require.NoError(t, err)
	}

	c.EvictByMCPClient("client-a")
	_, ok := c.Get(keyA1)
	assert.False(t, ok, "user-mode entry for the client must be gone")
	_, ok = c.Get(keyA2)
	assert.False(t, ok, "vk-mode entry for the client must be gone")
	_, ok = c.Get(keyB)
	assert.True(t, ok, "entry for another client must survive")
	assert.Equal(t, 1, c.Len())

	// Empty client ID and unknown client IDs are no-ops.
	c.EvictByMCPClient("")
	c.EvictByMCPClient("client-x")
	assert.Equal(t, 1, c.Len())
}

func TestUserTokenCache_EvictByVirtualKey(t *testing.T) {
	c := newTestTokenCache(t)
	keyVK1A := userTokenCacheKey(schemas.MCPAuthModeVK, "vk1", "client-a", "")
	keyVK1B := userTokenCacheKey(schemas.MCPAuthModeVK, "vk1", "client-b", "")
	keyVK2 := userTokenCacheKey(schemas.MCPAuthModeVK, "vk2", "client-a", "")
	// A user-mode identity that happens to equal the VK ID must survive:
	// the eviction is scoped to vk-mode entries only.
	keyUser := userTokenCacheKey(schemas.MCPAuthModeUser, "vk1", "client-a", "")
	for i, k := range []string{keyVK1A, keyVK1B, keyVK2, keyUser} {
		_, err := c.Fill(context.Background(), k, fillWith(testToken(fmt.Sprintf("vt%d", i), "access", nil)))
		require.NoError(t, err)
	}

	c.EvictByVirtualKey("vk1")
	_, ok := c.Get(keyVK1A)
	assert.False(t, ok)
	_, ok = c.Get(keyVK1B)
	assert.False(t, ok, "the VK's entries must be evicted across clients")
	_, ok = c.Get(keyVK2)
	assert.True(t, ok, "another VK's entry must survive")
	_, ok = c.Get(keyUser)
	assert.True(t, ok, "a user-mode identity equal to the VK ID must survive")

	// Empty and unknown VK IDs are no-ops.
	c.EvictByVirtualKey("")
	c.EvictByVirtualKey("vk-x")
	assert.Equal(t, 2, c.Len())
}

func TestUserTokenCache_EvictByUser(t *testing.T) {
	c := newTestTokenCache(t)
	keyU1A := userTokenCacheKey(schemas.MCPAuthModeUser, "u1", "client-a", "")
	keyU1B := userTokenCacheKey(schemas.MCPAuthModeUser, "u1", "client-b", "")
	keyU2 := userTokenCacheKey(schemas.MCPAuthModeUser, "u2", "client-a", "")
	// A vk-mode identity that happens to equal the user ID must survive:
	// the eviction is scoped to user-mode entries only.
	keyVK := userTokenCacheKey(schemas.MCPAuthModeVK, "u1", "client-a", "")
	for i, k := range []string{keyU1A, keyU1B, keyU2, keyVK} {
		_, err := c.Fill(context.Background(), k, fillWith(testToken(fmt.Sprintf("ut%d", i), "access", nil)))
		require.NoError(t, err)
	}

	c.EvictByUser("u1")
	_, ok := c.Get(keyU1A)
	assert.False(t, ok)
	_, ok = c.Get(keyU1B)
	assert.False(t, ok, "the user's entries must be evicted across clients")
	_, ok = c.Get(keyU2)
	assert.True(t, ok, "another user's entry must survive")
	_, ok = c.Get(keyVK)
	assert.True(t, ok, "a vk-mode identity equal to the user ID must survive")

	// Empty and unknown user IDs are no-ops.
	c.EvictByUser("")
	c.EvictByUser("u-x")
	assert.Equal(t, 2, c.Len())
}

func TestUserTokenCache_Flush(t *testing.T) {
	c := newTestTokenCache(t)
	for i := 1; i <= 3; i++ {
		_, err := c.Fill(context.Background(), fmt.Sprintf("k%d", i), fillWith(testToken(fmt.Sprintf("t%d", i), "access", nil)))
		require.NoError(t, err)
	}

	c.Flush()
	assert.Equal(t, 0, c.Len())
	for i := 1; i <= 3; i++ {
		_, ok := c.Get(fmt.Sprintf("k%d", i))
		assert.False(t, ok)
	}
}

func TestUserTokenCache_InflightDedup(t *testing.T) {
	c := newTestTokenCache(t)

	var calls atomic.Int64
	release := make(chan struct{})
	started := make(chan struct{})

	fill := func() (cachedUserToken, error) {
		calls.Add(1)
		close(started)
		<-release
		return testToken("t1", "shared-access", nil), nil
	}

	var wg sync.WaitGroup
	results := make([]cachedUserToken, 2)
	wg.Add(1)
	fillErrs := make([]error, 2)
	go func() {
		defer wg.Done()
		v, err := c.Fill(context.Background(), "k1", fill)
		results[0], fillErrs[0] = v, err
	}()
	<-started

	wg.Add(1)
	go func() {
		defer wg.Done()
		// This fill func must never run: the leader is already in flight.
		v, err := c.Fill(context.Background(), "k1", func() (cachedUserToken, error) {
			calls.Add(1)
			return testToken("t-other", "other", nil), nil
		})
		results[1], fillErrs[1] = v, err
	}()

	// Give the second goroutine a moment to register as a waiter, then let
	// the leader finish.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	// require, not assert, calls t.FailNow(), which per the testing
	// package's own contract must only be called from the goroutine running
	// the test — hence collecting errors above and asserting them here.
	require.NoError(t, fillErrs[0])
	require.NoError(t, fillErrs[1])

	assert.Equal(t, int64(1), calls.Load(), "concurrent fills for one key must run the handler once")
	assert.Equal(t, "shared-access", results[0].accessToken)
	assert.Equal(t, "shared-access", results[1].accessToken, "waiter must share the leader's result")
}

func TestUserTokenCache_ErrorSharedNotCached(t *testing.T) {
	c := newTestTokenCache(t)

	var calls atomic.Int64
	release := make(chan struct{})
	started := make(chan struct{})
	fillErr := errors.New("db unavailable")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := c.Fill(context.Background(), "k1", func() (cachedUserToken, error) {
			calls.Add(1)
			close(started)
			<-release
			return cachedUserToken{}, fillErr
		})
		errs[0] = err
	}()
	<-started

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := c.Fill(context.Background(), "k1", func() (cachedUserToken, error) {
			calls.Add(1)
			return cachedUserToken{}, fillErr
		})
		errs[1] = err
	}()

	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	assert.Equal(t, int64(1), calls.Load(), "waiter must share the leader's error, not re-run the fill")
	assert.ErrorIs(t, errs[0], fillErr)
	assert.ErrorIs(t, errs[1], fillErr, "error must be shared with waiters")
	assert.Equal(t, 0, c.Len(), "errors must never be cached")

	// A later fill runs the handler again: the failure was not cached.
	_, err := c.Fill(context.Background(), "k1", func() (cachedUserToken, error) {
		calls.Add(1)
		return testToken("t1", "recovered", nil), nil
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), calls.Load())
	got, ok := c.Get("k1")
	require.True(t, ok)
	assert.Equal(t, "recovered", got.accessToken)
}

func TestUserTokenCache_GenerationGuardDiscardsStaleFill(t *testing.T) {
	c := newTestTokenCache(t)

	inFill := make(chan struct{})
	release := make(chan struct{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		v, err := c.Fill(context.Background(), "k1", func() (cachedUserToken, error) {
			close(inFill)
			<-release
			return testToken("t1", "stale-value", nil), nil
		})
		// The caller still receives the value it read; only the cache
		// install is discarded. assert, not require: require.NoError calls
		// t.FailNow(), which per the testing package's own contract must
		// only be called from the goroutine running the test.
		assert.NoError(t, err)
		assert.Equal(t, "stale-value", v.accessToken)
	}()

	<-inFill
	// An eviction lands while the fill is mid-read: whatever the fill read
	// is now suspect and must not be installed.
	c.Evict("k1")
	close(release)
	<-done

	_, ok := c.Get("k1")
	assert.False(t, ok, "a fill that raced an eviction must not install its result")
	assert.Equal(t, 0, c.Len())
}

func TestUserTokenCache_UpsertRebindsTokenIDIndex(t *testing.T) {
	c := newTestTokenCache(t)
	_, err := c.Fill(context.Background(), "k1", fillWith(testToken("t-old", "old", nil)))
	require.NoError(t, err)

	// Same key, new backing row (the binding re-authenticated onto a fresh
	// row): the index must follow the new row ID and drop the old one.
	c.Evict("k1")
	_, err = c.Fill(context.Background(), "k1", fillWith(testToken("t-new", "new", nil)))
	require.NoError(t, err)

	c.EvictByTokenID("t-old")
	_, ok := c.Get("k1")
	assert.True(t, ok, "stale token-ID mapping must not evict the rebound entry")

	c.EvictByTokenID("t-new")
	_, ok = c.Get("k1")
	assert.False(t, ok, "current token-ID mapping must evict the entry")
}

// ---------- Integration through GetUserAccessTokenByMode ----------

// countingConfigStore wraps testConfigStore with per-method call counters so
// integration tests can assert which lookups actually reached the store.
type countingConfigStore struct {
	*testConfigStore
	getUserTokenByModeCalls atomic.Int64
}

func (s *countingConfigStore) GetOauthUserTokenByMode(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) (*tables.TableMCPOauthToken, error) {
	s.getUserTokenByModeCalls.Add(1)
	return s.testConfigStore.GetOauthUserTokenByMode(ctx, mode, identity, mcpClientID)
}

func newCountingConfigStore() *countingConfigStore {
	return &countingConfigStore{testConfigStore: newTestConfigStore()}
}

// seedUserToken inserts an active session-mode per-user token row.
func seedUserToken(store *testConfigStore, tokenID, oauthConfigID, mcpClientID, sessionID, access string, expiresAt *time.Time) {
	store.oauthTokens[tokenID] = &tables.TableMCPOauthToken{
		ID:            tokenID,
		AuthMode:      "session",
		MCPClientID:   mcpClientID,
		OauthConfigID: oauthConfigID,
		SessionID:     sessionID,
		Status:        "active",
		AccessToken:   access,
		RefreshToken:  "refresh-token",
		TokenType:     "bearer",
		ExpiresAt:     expiresAt,
		Scopes:        "[]",
	}
}

func TestGetUserAccessTokenByMode_SecondCallServedFromCache(t *testing.T) {
	store := newCountingConfigStore()
	seedUserToken(store.testConfigStore, "tok-1", "cfg-1", "mcp-1", "sess-1", "cached-access", bifrost.Ptr(time.Now().Add(1*time.Hour)))

	provider := newCachingProvider(t, store, bifrost.NewDefaultLogger(schemas.LogLevelError))
	ctx := context.Background()

	access, err := provider.GetUserAccessTokenByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)
	assert.Equal(t, "cached-access", access)
	assert.Equal(t, int64(1), store.getUserTokenByModeCalls.Load())

	access, err = provider.GetUserAccessTokenByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)
	assert.Equal(t, "cached-access", access)
	assert.Equal(t, int64(1), store.getUserTokenByModeCalls.Load(), "second call must be served from cache, not the store")
}

func TestGetUserAccessTokenByMode_ExpiredRefreshesOnceUnderConcurrency(t *testing.T) {
	var tokenEndpointCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenEndpointCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "refreshed-access",
			"refresh_token": "new-refresh-token",
			"token_type":    "bearer",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	store := newCountingConfigStore()
	store.oauthConfigs["cfg-1"] = &tables.TableOauthConfig{
		ID:          "cfg-1",
		ClientID:    schemas.NewSecretVar("client-id"),
		TokenURL:    server.URL + "/token",
		RedirectURI: "http://localhost/callback",
		Scopes:      `["read"]`,
		Status:      "authorized",
	}
	seedUserToken(store.testConfigStore, "tok-1", "cfg-1", "mcp-1", "sess-1", "expired-access", bifrost.Ptr(time.Now().Add(-1*time.Minute)))

	provider := newCachingProvider(t, store, bifrost.NewDefaultLogger(schemas.LogLevelError))
	ctx := context.Background()

	const callers = 8
	var wg sync.WaitGroup
	results := make([]string, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = provider.GetUserAccessTokenByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
		}()
	}
	wg.Wait()

	for i := range callers {
		require.NoError(t, errs[i])
		assert.Equal(t, "refreshed-access", results[i])
	}
	assert.Equal(t, int64(1), tokenEndpointCalls.Load(), "concurrent callers must trigger exactly one upstream refresh")
}

func TestGetUserAccessTokenByMode_EvictByIDAfterDelete(t *testing.T) {
	store := newCountingConfigStore()
	seedUserToken(store.testConfigStore, "tok-1", "cfg-1", "mcp-1", "sess-1", "cached-access", bifrost.Ptr(time.Now().Add(1*time.Hour)))

	provider := newCachingProvider(t, store, bifrost.NewDefaultLogger(schemas.LogLevelError))
	ctx := context.Background()

	_, err := provider.GetUserAccessTokenByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)

	// Delete the row directly through the store (as the sessions revoke
	// handler does) and evict by ID (as its cache callback does).
	store.mu.Lock()
	delete(store.oauthTokens, "tok-1")
	store.mu.Unlock()
	provider.EvictUserTokenByID("tok-1")

	_, err = provider.GetUserAccessTokenByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, schemas.ErrOAuth2TokenNotFound, "post-eviction lookup must see the delete, not the cached token")
}

func TestGetUserAccessTokenByMode_FlushAfterNeedsReauth(t *testing.T) {
	store := newCountingConfigStore()
	seedUserToken(store.testConfigStore, "tok-1", "cfg-1", "mcp-1", "sess-1", "cached-access", bifrost.Ptr(time.Now().Add(1*time.Hour)))

	provider := newCachingProvider(t, store, bifrost.NewDefaultLogger(schemas.LogLevelError))
	ctx := context.Background()

	_, err := provider.GetUserAccessTokenByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)

	// Credential rotation marks every token row for the config
	// needs_reauth, then the handler-level callback flushes the cache.
	require.NoError(t, store.MarkTokensNeedsReauthByConfigID(ctx, "cfg-1", "rotated"))
	provider.FlushUserTokenCache()

	// The active-only lookup no longer matches the row, so the caller sees
	// the re-auth requirement instead of the cached token.
	_, err = provider.GetUserAccessTokenByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, schemas.ErrOAuth2TokenNotFound)
}

func TestGetUserAccessTokenByMode_NegativeNotCached(t *testing.T) {
	store := newCountingConfigStore()
	provider := newCachingProvider(t, store, bifrost.NewDefaultLogger(schemas.LogLevelError))
	ctx := context.Background()

	_, err := provider.GetUserAccessTokenByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, schemas.ErrOAuth2TokenNotFound)

	// The user completes OAuth: a fresh row appears. No eviction happens
	// (there is nothing to evict) and the very next call must see it.
	store.mu.Lock()
	seedUserToken(store.testConfigStore, "tok-1", "cfg-1", "mcp-1", "sess-1", "fresh-access", bifrost.Ptr(time.Now().Add(1*time.Hour)))
	store.mu.Unlock()

	access, err := provider.GetUserAccessTokenByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)
	assert.Equal(t, "fresh-access", access, "a failed lookup must never be cached")
}

func TestGetUserAccessTokenByMode_ForceRefreshEvictsAndServesNewToken(t *testing.T) {
	server := tokenRefreshServer(t, "force-refreshed-access")

	store := newCountingConfigStore()
	store.oauthConfigs["cfg-1"] = &tables.TableOauthConfig{
		ID:          "cfg-1",
		ClientID:    schemas.NewSecretVar("client-id"),
		TokenURL:    server.URL + "/token",
		RedirectURI: "http://localhost/callback",
		Scopes:      `["read"]`,
		Status:      "authorized",
	}
	// Not expired: the cached copy would keep serving without the forced
	// refresh's eviction.
	seedUserToken(store.testConfigStore, "tok-1", "cfg-1", "mcp-1", "sess-1", "rejected-upstream", bifrost.Ptr(time.Now().Add(1*time.Hour)))

	provider := newCachingProvider(t, store, bifrost.NewDefaultLogger(schemas.LogLevelError))
	ctx := context.Background()

	access, err := provider.GetUserAccessTokenByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)
	require.Equal(t, "rejected-upstream", access)

	oauthConfigID := "cfg-1"
	config := &schemas.MCPClientConfig{
		ID:            "mcp-1",
		Name:          "Test Client",
		AuthType:      schemas.MCPAuthTypePerUserOauth,
		OauthConfigID: &oauthConfigID,
	}
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bfCtx.SetValue(schemas.BifrostContextKeyMCPSessionID, "sess-1")
	require.NoError(t, provider.ForceRefreshAccessToken(bfCtx, config))

	access, err = provider.GetUserAccessTokenByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)
	assert.Equal(t, "force-refreshed-access", access, "force refresh must evict the cached copy so the next read serves the new token")
}

// testLiveStore returns a new, empty live store closed when the test ends.
var testLiveStore = livestoretest.New

// newTestTokenCache returns a token cache in a live store of its own.
func newTestTokenCache(t testing.TB) *userTokenCache {
	t.Helper()
	c, err := newUserTokenCache(testLiveStore(t))
	if err != nil {
		t.Fatalf("token cache: %v", err)
	}
	return c
}

// newTestGrantCache returns an ID-JAG grant cache in a live store of its own.
func newTestGrantCache(t testing.TB) *idjagGrantCache {
	t.Helper()
	c, err := newIDJAGGrantCache(testLiveStore(t))
	if err != nil {
		t.Fatalf("grant cache: %v", err)
	}
	return c
}

// newCachingProvider returns a provider over store with its caches in a live store of its own, as
// startup builds it.
func newCachingProvider(t testing.TB, store configstore.ConfigStore, logger schemas.Logger) *OAuth2Provider {
	t.Helper()
	p, err := NewOAuth2Provider(store, testLiveStore(t), logger)
	if err != nil {
		t.Fatalf("oauth provider: %v", err)
	}
	return p
}

// sessionTokenStore answers session-mode token lookups from a read-only map keyed by session ID, so
// a benchmark's misses cost the cache and the load around the lookup, not a locked scan of every
// seeded row.
type sessionTokenStore struct {
	*testConfigStore
	bySession map[string]*tables.TableMCPOauthToken
}

// GetOauthUserTokenByMode returns a copy of the session's token, nil when it has none.
func (s *sessionTokenStore) GetOauthUserTokenByMode(_ context.Context, _ schemas.MCPAuthMode, identity, _ string) (*tables.TableMCPOauthToken, error) {
	token, ok := s.bySession[identity]
	if !ok {
		return nil, nil
	}
	return bifrost.Ptr(*token), nil
}

// BenchmarkGetUserAccessTokenByMode measures the per-request token read over 300 cached session
// bindings: a hit, hits from every core at once, and concurrent misses, each of which evicts its
// own binding and reloads it through the cache's fill.
func BenchmarkGetUserAccessTokenByMode(b *testing.B) {
	const sessions = 300
	expiresAt := time.Now().Add(time.Hour)
	store := &sessionTokenStore{testConfigStore: newTestConfigStore(), bySession: make(map[string]*tables.TableMCPOauthToken, sessions)}
	ids := make([]string, sessions)
	for i := range ids {
		ids[i] = fmt.Sprintf("sess-%03d", i)
		store.bySession[ids[i]] = &tables.TableMCPOauthToken{
			ID: fmt.Sprintf("tok-%03d", i), AuthMode: "session", MCPClientID: "mcp-1", OauthConfigID: "cfg-1",
			SessionID: ids[i], Status: "active", AccessToken: "access-" + ids[i], TokenType: "bearer", ExpiresAt: &expiresAt, Scopes: "[]",
		}
	}
	p := newCachingProvider(b, store, bifrost.NewDefaultLogger(schemas.LogLevelError))
	ctx := context.Background()
	read := func(i int) error {
		_, err := p.GetUserAccessTokenByMode(ctx, schemas.MCPAuthModeSession, ids[i%sessions], "mcp-1")
		return err
	}
	for i := range ids {
		if err := read(i); err != nil {
			b.Fatal(err)
		}
	}

	b.Run("hit", func(b *testing.B) {
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			if err := read(i); err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
	b.Run("hit-parallel", func(b *testing.B) {
		b.ReportAllocs()
		var next atomic.Int64
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if err := read(int(next.Add(1))); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
	b.Run("miss-parallel", func(b *testing.B) {
		b.ReportAllocs()
		var next atomic.Int64
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				i := int(next.Add(1)) % sessions
				p.EvictUserToken(schemas.MCPAuthModeSession, ids[i], "mcp-1")
				if err := read(i); err != nil {
					b.Error(err)
					return
				}
			}
		})
	})
}
