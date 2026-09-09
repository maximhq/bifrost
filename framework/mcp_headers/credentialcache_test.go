package mcp_headers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

func testCredential(id string, headers map[string]string) cachedHeaderCredential {
	return cachedHeaderCredential{
		credentialID: id,
		credential: &schemas.MCPHeadersUserCredential{
			ID:      id,
			Headers: headers,
			Status:  schemas.MCPHeadersUserCredentialStatusActive,
		},
	}
}

func fillWithCredential(v cachedHeaderCredential) func() (cachedHeaderCredential, error) {
	return func() (cachedHeaderCredential, error) { return v, nil }
}

func TestHeaderCredentialCache_HitAndMiss(t *testing.T) {
	c := newHeaderCredentialCache(4)

	_, ok := c.Get("missing")
	assert.False(t, ok, "empty cache must miss")

	v, err := c.Fill(context.Background(), "k1", fillWithCredential(testCredential("c1", map[string]string{"X-Api-Key": "v1"})))
	require.NoError(t, err)
	assert.Equal(t, "c1", v.credentialID)

	got, ok := c.Get("k1")
	require.True(t, ok, "filled key must hit")
	assert.Equal(t, "c1", got.credentialID)
	assert.Equal(t, "v1", got.credential.Headers["X-Api-Key"])

	_, ok = c.Get("k2")
	assert.False(t, ok, "unrelated key must miss")
}

func TestHeaderCredentialCache_CapacityEviction(t *testing.T) {
	c := newHeaderCredentialCache(2)

	for i := 1; i <= 3; i++ {
		_, err := c.Fill(context.Background(), 
			fmt.Sprintf("k%d", i),
			fillWithCredential(testCredential(fmt.Sprintf("c%d", i), nil)),
		)
		require.NoError(t, err)
	}

	assert.Equal(t, 2, c.Len(), "cache must stay at capacity")
	_, ok := c.Get("k1")
	assert.False(t, ok, "least recently used entry must be evicted")
	_, ok = c.Get("k2")
	assert.True(t, ok)
	_, ok = c.Get("k3")
	assert.True(t, ok)

	// The evicted entry's credential-ID index mapping must be gone: evicting
	// by its row ID must not disturb the surviving entries.
	c.EvictByCredentialID("c1")
	assert.Equal(t, 2, c.Len())
}

func TestHeaderCredentialCache_EvictExactKey(t *testing.T) {
	c := newHeaderCredentialCache(4)
	_, err := c.Fill(context.Background(), "k1", fillWithCredential(testCredential("c1", nil)))
	require.NoError(t, err)

	c.Evict("k1")
	_, ok := c.Get("k1")
	assert.False(t, ok)
	assert.Equal(t, 0, c.Len())

	// Evicting an absent key is a no-op, not a panic.
	c.Evict("never-set")
}

func TestHeaderCredentialCache_EvictByCredentialID(t *testing.T) {
	c := newHeaderCredentialCache(4)
	_, err := c.Fill(context.Background(), "k1", fillWithCredential(testCredential("c1", nil)))
	require.NoError(t, err)
	_, err = c.Fill(context.Background(), "k2", fillWithCredential(testCredential("c2", nil)))
	require.NoError(t, err)

	c.EvictByCredentialID("c1")
	_, ok := c.Get("k1")
	assert.False(t, ok, "entry holding the evicted credential ID must be gone")
	_, ok = c.Get("k2")
	assert.True(t, ok, "unrelated entry must survive")

	// Unknown credential IDs are a no-op.
	c.EvictByCredentialID("unknown")
	assert.Equal(t, 1, c.Len())
}

func TestHeaderCredentialCache_EvictByMCPClient(t *testing.T) {
	c := newHeaderCredentialCache(8)
	keyA1 := headerCredentialCacheKey(schemas.MCPAuthModeUser, "u1", "client-a")
	keyA2 := headerCredentialCacheKey(schemas.MCPAuthModeVK, "vk1", "client-a")
	// Admin-mode entries carry an empty identity component and must be
	// swept with the rest of the client's entries.
	keyA3 := headerCredentialCacheKey(schemas.MCPAuthModeAdmin, "", "client-a")
	keyB := headerCredentialCacheKey(schemas.MCPAuthModeUser, "u1", "client-b")
	for i, k := range []string{keyA1, keyA2, keyA3, keyB} {
		_, err := c.Fill(context.Background(), k, fillWithCredential(testCredential(fmt.Sprintf("c%d", i), nil)))
		require.NoError(t, err)
	}

	c.EvictByMCPClient("client-a")
	_, ok := c.Get(keyA1)
	assert.False(t, ok, "user-mode entry for the client must be gone")
	_, ok = c.Get(keyA2)
	assert.False(t, ok, "vk-mode entry for the client must be gone")
	_, ok = c.Get(keyA3)
	assert.False(t, ok, "admin-mode entry for the client must be gone")
	_, ok = c.Get(keyB)
	assert.True(t, ok, "entry for another client must survive")
	assert.Equal(t, 1, c.Len())

	// Empty client ID and unknown client IDs are no-ops.
	c.EvictByMCPClient("")
	c.EvictByMCPClient("client-x")
	assert.Equal(t, 1, c.Len())
}

func TestHeaderCredentialCache_EvictByVirtualKey(t *testing.T) {
	c := newHeaderCredentialCache(8)
	keyVK1A := headerCredentialCacheKey(schemas.MCPAuthModeVK, "vk1", "client-a")
	keyVK1B := headerCredentialCacheKey(schemas.MCPAuthModeVK, "vk1", "client-b")
	keyVK2 := headerCredentialCacheKey(schemas.MCPAuthModeVK, "vk2", "client-a")
	// A user-mode identity that happens to equal the VK ID must survive:
	// the eviction is scoped to vk-mode entries only.
	keyUser := headerCredentialCacheKey(schemas.MCPAuthModeUser, "vk1", "client-a")
	for i, k := range []string{keyVK1A, keyVK1B, keyVK2, keyUser} {
		_, err := c.Fill(context.Background(), k, fillWithCredential(testCredential(fmt.Sprintf("vc%d", i), nil)))
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

func TestHeaderCredentialCache_EvictByUser(t *testing.T) {
	c := newHeaderCredentialCache(8)
	keyU1A := headerCredentialCacheKey(schemas.MCPAuthModeUser, "u1", "client-a")
	keyU1B := headerCredentialCacheKey(schemas.MCPAuthModeUser, "u1", "client-b")
	keyU2 := headerCredentialCacheKey(schemas.MCPAuthModeUser, "u2", "client-a")
	// A vk-mode identity that happens to equal the user ID must survive:
	// the eviction is scoped to user-mode entries only.
	keyVK := headerCredentialCacheKey(schemas.MCPAuthModeVK, "u1", "client-a")
	for i, k := range []string{keyU1A, keyU1B, keyU2, keyVK} {
		_, err := c.Fill(context.Background(), k, fillWithCredential(testCredential(fmt.Sprintf("uc%d", i), nil)))
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

func TestHeaderCredentialCache_Flush(t *testing.T) {
	c := newHeaderCredentialCache(4)
	for i := 1; i <= 3; i++ {
		_, err := c.Fill(context.Background(), fmt.Sprintf("k%d", i), fillWithCredential(testCredential(fmt.Sprintf("c%d", i), nil)))
		require.NoError(t, err)
	}

	c.Flush()
	assert.Equal(t, 0, c.Len())
	for i := 1; i <= 3; i++ {
		_, ok := c.Get(fmt.Sprintf("k%d", i))
		assert.False(t, ok)
	}
}

func TestHeaderCredentialCache_InflightDedup(t *testing.T) {
	c := newHeaderCredentialCache(4)

	var calls atomic.Int64
	release := make(chan struct{})
	started := make(chan struct{})

	fill := func() (cachedHeaderCredential, error) {
		calls.Add(1)
		close(started)
		<-release
		return testCredential("c1", map[string]string{"X-Api-Key": "shared"}), nil
	}

	var wg sync.WaitGroup
	results := make([]cachedHeaderCredential, 2)
	fillErrs := make([]error, 2)
	wg.Add(1)
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
		v, err := c.Fill(context.Background(), "k1", func() (cachedHeaderCredential, error) {
			calls.Add(1)
			return testCredential("c-other", nil), nil
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
	assert.Equal(t, "c1", results[0].credentialID)
	assert.Equal(t, "c1", results[1].credentialID, "waiter must share the leader's result")
}

func TestHeaderCredentialCache_ErrorSharedNotCached(t *testing.T) {
	c := newHeaderCredentialCache(4)

	var calls atomic.Int64
	release := make(chan struct{})
	started := make(chan struct{})
	fillErr := errors.New("db unavailable")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := c.Fill(context.Background(), "k1", func() (cachedHeaderCredential, error) {
			calls.Add(1)
			close(started)
			<-release
			return cachedHeaderCredential{}, fillErr
		})
		errs[0] = err
	}()
	<-started

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := c.Fill(context.Background(), "k1", func() (cachedHeaderCredential, error) {
			calls.Add(1)
			return cachedHeaderCredential{}, fillErr
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
	_, err := c.Fill(context.Background(), "k1", func() (cachedHeaderCredential, error) {
		calls.Add(1)
		return testCredential("c1", nil), nil
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), calls.Load())
	got, ok := c.Get("k1")
	require.True(t, ok)
	assert.Equal(t, "c1", got.credentialID)
}

func TestHeaderCredentialCache_GenerationGuardDiscardsStaleFill(t *testing.T) {
	c := newHeaderCredentialCache(4)

	inFill := make(chan struct{})
	release := make(chan struct{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		v, err := c.Fill(context.Background(), "k1", func() (cachedHeaderCredential, error) {
			close(inFill)
			<-release
			return testCredential("c-stale", nil), nil
		})
		// The caller still receives the value it read; only the cache
		// install is discarded. assert, not require: require.NoError calls
		// t.FailNow(), which per the testing package's own contract must
		// only be called from the goroutine running the test.
		assert.NoError(t, err)
		assert.Equal(t, "c-stale", v.credentialID)
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

func TestHeaderCredentialCache_UpsertRebindsCredentialIDIndex(t *testing.T) {
	c := newHeaderCredentialCache(4)
	_, err := c.Fill(context.Background(), "k1", fillWithCredential(testCredential("c-old", nil)))
	require.NoError(t, err)

	// Same key, new backing row (the binding resubmitted onto a fresh row):
	// the index must follow the new row ID and drop the old one.
	c.Evict("k1")
	_, err = c.Fill(context.Background(), "k1", fillWithCredential(testCredential("c-new", nil)))
	require.NoError(t, err)

	c.EvictByCredentialID("c-old")
	_, ok := c.Get("k1")
	assert.True(t, ok, "stale credential-ID mapping must not evict the rebound entry")

	c.EvictByCredentialID("c-new")
	_, ok = c.Get("k1")
	assert.False(t, ok, "current credential-ID mapping must evict the entry")
}

func TestHeaderCredentialCache_NilSafety(t *testing.T) {
	var c *headerCredentialCache
	_, ok := c.Get("k")
	assert.False(t, ok)
	c.Evict("k")
	c.EvictByCredentialID("c")
	c.EvictByMCPClient("client")
	c.EvictByVirtualKey("vk")
	c.Flush()
	assert.Equal(t, 0, c.Len())
	v, err := c.Fill(context.Background(), "k", fillWithCredential(testCredential("c-pass", nil)))
	require.NoError(t, err)
	assert.Equal(t, "c-pass", v.credentialID)
}

// ---------- Integration through GetCredentialByMode ----------

// UpsertMCPPerUserHeaderCredential mirrors the real store's binding-keyed
// upsert semantics on the test double: an existing row for the same MCP
// client is reused and the passed row's ID is rewritten to it, exactly the
// contract the provider's post-upsert eviction relies on.
func (s *testConfigStore) UpsertMCPPerUserHeaderCredential(_ context.Context, cred *tables.TableMCPPerUserHeaderCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.credentials[cred.MCPClientID]; ok {
		cred.ID = existing.ID
		cred.CreatedAt = existing.CreatedAt
	} else if cred.ID == "" {
		cred.ID = uuid.NewString()
	}
	s.credentials[cred.MCPClientID] = bifrost.Ptr(*cred)
	return nil
}

func (s *testConfigStore) DeleteMCPPerUserHeaderCredential(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for clientID, row := range s.credentials {
		if row.ID == id {
			delete(s.credentials, clientID)
		}
	}
	return nil
}

// seedHeaderCredential inserts a session-mode credential row into the double.
func seedHeaderCredential(store *testConfigStore, id, mcpClientID, status, headersJSON string) {
	now := time.Now()
	store.mu.Lock()
	defer store.mu.Unlock()
	store.credentials[mcpClientID] = &tables.TableMCPPerUserHeaderCredential{
		ID:          id,
		MCPClientID: mcpClientID,
		AuthMode:    "session",
		SessionID:   "sess-1",
		Status:      status,
		HeadersJSON: headersJSON,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func lookupCalls(store *testConfigStore) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.credentialLookupCalls
}

func TestGetCredentialByMode_AdminIdentityNormalizedToOneCacheEntry(t *testing.T) {
	store := newTestConfigStore()
	now := time.Now()
	store.mu.Lock()
	store.credentials["mcp-1"] = &tables.TableMCPPerUserHeaderCredential{
		ID: "cred-admin", MCPClientID: "mcp-1", AuthMode: "admin",
		Status: "active", HeadersJSON: `{"X-Api-Key":"v1"}`,
		CreatedAt: now, UpdatedAt: now,
	}
	store.mu.Unlock()
	provider := newTestProvider(store)
	ctx := context.Background()

	// The store lookup ignores identity for admin mode, so lookups with and
	// without a stray identity must share one cache entry; otherwise the
	// byID index could only evict one of the aliases.
	_, err := provider.GetCredentialByMode(ctx, schemas.MCPAuthModeAdmin, "stray", "mcp-1")
	require.NoError(t, err)
	_, err = provider.GetCredentialByMode(ctx, schemas.MCPAuthModeAdmin, "", "mcp-1")
	require.NoError(t, err)
	assert.Equal(t, 1, lookupCalls(store), "admin lookups must share one cache entry regardless of identity")

	provider.EvictCredentialByID("cred-admin")
	_, err = provider.GetCredentialByMode(ctx, schemas.MCPAuthModeAdmin, "stray", "mcp-1")
	require.NoError(t, err)
	assert.Equal(t, 2, lookupCalls(store), "byID eviction must invalidate the admin entry for every identity spelling")
}

func TestGetCredentialByMode_SecondCallServedFromCache(t *testing.T) {
	store := newTestConfigStore()
	seedHeaderCredential(store, "cred-1", "mcp-1", "active", `{"X-Api-Key":"v1"}`)
	provider := newTestProvider(store)
	ctx := context.Background()

	cred, err := provider.GetCredentialByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)
	assert.Equal(t, "v1", cred.Headers["X-Api-Key"])
	assert.Equal(t, 1, lookupCalls(store))

	cred, err = provider.GetCredentialByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)
	assert.Equal(t, "v1", cred.Headers["X-Api-Key"])
	assert.Equal(t, 1, lookupCalls(store), "second call must be served from cache, not the store")
}

func TestGetCredentialByMode_CachedHitReturnsIsolatedCopy(t *testing.T) {
	store := newTestConfigStore()
	seedHeaderCredential(store, "cred-1", "mcp-1", "active", `{"X-Api-Key":"v1"}`)
	provider := newTestProvider(store)
	ctx := context.Background()

	first, err := provider.GetCredentialByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)
	// A caller scribbling on its copy must never leak into the cache.
	first.Headers["X-Api-Key"] = "mutated"
	first.Status = schemas.MCPHeadersUserCredentialStatusOrphaned

	second, err := provider.GetCredentialByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)
	assert.Equal(t, "v1", second.Headers["X-Api-Key"], "cached value must be isolated from caller mutation")
	assert.Equal(t, schemas.MCPHeadersUserCredentialStatusActive, second.Status)
}

func TestGetCredentialByMode_UpsertEvictsSoNextReadSeesNewValues(t *testing.T) {
	store := newTestConfigStore()
	seedHeaderCredential(store, "cred-1", "mcp-1", "active", `{"X-Api-Key":"old"}`)
	provider := newTestProvider(store)
	ctx := context.Background()

	cred, err := provider.GetCredentialByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)
	require.Equal(t, "old", cred.Headers["X-Api-Key"])

	sessionID := "sess-1"
	require.NoError(t, provider.UpsertCredential(ctx, &schemas.MCPHeadersUserCredential{
		MCPClientID: "mcp-1",
		AuthMode:    schemas.MCPAuthModeSession,
		SessionID:   &sessionID,
		Headers:     map[string]string{"X-Api-Key": "new"},
		Status:      schemas.MCPHeadersUserCredentialStatusActive,
	}))

	cred, err = provider.GetCredentialByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)
	assert.Equal(t, "new", cred.Headers["X-Api-Key"], "upsert must evict so the next read serves the new values")
	assert.Equal(t, 2, lookupCalls(store), "post-upsert read must come from the store")
}

func TestGetCredentialByMode_DeleteEvictsSoNextReadIsNotFound(t *testing.T) {
	store := newTestConfigStore()
	seedHeaderCredential(store, "cred-1", "mcp-1", "active", `{"X-Api-Key":"v1"}`)
	provider := newTestProvider(store)
	ctx := context.Background()

	_, err := provider.GetCredentialByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)

	require.NoError(t, provider.DeleteCredential(ctx, "cred-1"))

	_, err = provider.GetCredentialByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, schemas.ErrHeadersCredentialNotFound, "post-delete lookup must see the delete, not the cached credential")
}

func TestGetCredentialByMode_EvictByIDAfterDirectStoreDelete(t *testing.T) {
	store := newTestConfigStore()
	seedHeaderCredential(store, "cred-1", "mcp-1", "active", `{"X-Api-Key":"v1"}`)
	provider := newTestProvider(store)
	ctx := context.Background()

	_, err := provider.GetCredentialByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)

	// Delete the row directly through the store (as the sessions revoke
	// handler does) and evict by ID (as its cache callback does).
	store.mu.Lock()
	delete(store.credentials, "mcp-1")
	store.mu.Unlock()
	provider.EvictCredentialByID("cred-1")

	_, err = provider.GetCredentialByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, schemas.ErrHeadersCredentialNotFound)
}

func TestGetCredentialByMode_NeedsUpdateRowIsCachedWithStatus(t *testing.T) {
	store := newTestConfigStore()
	seedHeaderCredential(store, "cred-1", "mcp-1", "needs_update", `{"X-Api-Key":"stale"}`)
	provider := newTestProvider(store)
	ctx := context.Background()

	cred, err := provider.GetCredentialByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)
	assert.Equal(t, schemas.MCPHeadersUserCredentialStatusNeedsUpdate, cred.Status)

	cred, err = provider.GetCredentialByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)
	assert.Equal(t, schemas.MCPHeadersUserCredentialStatusNeedsUpdate, cred.Status, "the cached copy must carry the row's status through")
	assert.Equal(t, "stale", cred.Headers["X-Api-Key"])
	assert.Equal(t, 1, lookupCalls(store), "needs_update rows are cacheable like active rows")
}

func TestGetCredentialByMode_NegativeNotCached(t *testing.T) {
	store := newTestConfigStore()
	provider := newTestProvider(store)
	ctx := context.Background()

	_, err := provider.GetCredentialByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, schemas.ErrHeadersCredentialNotFound)

	// The user completes a submission flow: a fresh row appears. No eviction
	// happens (there is nothing to evict) and the very next call must see it.
	seedHeaderCredential(store, "cred-1", "mcp-1", "active", `{"X-Api-Key":"fresh"}`)

	cred, err := provider.GetCredentialByMode(ctx, schemas.MCPAuthModeSession, "sess-1", "mcp-1")
	require.NoError(t, err)
	assert.Equal(t, "fresh", cred.Headers["X-Api-Key"], "a failed lookup must never be cached")
}

func TestGetCredentialByMode_EvictByMCPClientCoversAdminMode(t *testing.T) {
	store := newTestConfigStore()
	now := time.Now()
	store.mu.Lock()
	store.credentials["mcp-1"] = &tables.TableMCPPerUserHeaderCredential{
		ID:          "cred-admin",
		MCPClientID: "mcp-1",
		AuthMode:    "admin",
		Status:      "active",
		HeadersJSON: `{"X-Api-Key":"admin-v1"}`,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	store.mu.Unlock()
	provider := newTestProvider(store)
	ctx := context.Background()

	_, err := provider.GetCredentialByMode(ctx, schemas.MCPAuthModeAdmin, "", "mcp-1")
	require.NoError(t, err)
	require.Equal(t, 1, lookupCalls(store))

	provider.EvictCredentialsByMCPClient("mcp-1")

	_, err = provider.GetCredentialByMode(ctx, schemas.MCPAuthModeAdmin, "", "mcp-1")
	require.NoError(t, err)
	assert.Equal(t, 2, lookupCalls(store), "client-scoped eviction must also drop the admin-mode binding")
}
