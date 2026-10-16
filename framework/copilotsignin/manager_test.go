package copilotsignin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memStore is an in-memory Store with the same version and lease rules as the RDB store.
type memStore struct {
	mu   sync.Mutex
	rows map[string]tables.TableGithubCopilotSignIn
}

func newMemStore() *memStore {
	return &memStore{rows: map[string]tables.TableGithubCopilotSignIn{}}
}

func (s *memStore) GetGithubCopilotSignIns(context.Context) ([]tables.TableGithubCopilotSignIn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]tables.TableGithubCopilotSignIn, 0, len(s.rows))
	for _, r := range s.rows {
		out = append(out, r)
	}
	return out, nil
}

func (s *memStore) GetGithubCopilotSignIn(_ context.Context, keyID string) (*tables.TableGithubCopilotSignIn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[keyID]
	if !ok {
		return nil, nil
	}
	return &r, nil
}

func (s *memStore) ReplaceGithubCopilotSignIn(_ context.Context, row *tables.TableGithubCopilotSignIn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *row
	c.Version = s.rows[row.KeyID].Version + 1
	c.RefreshLeaseUntil = nil
	c.UpdatedAt = time.Now().UTC()
	s.rows[row.KeyID] = c
	row.Version = c.Version
	return nil
}

func (s *memStore) UpdateGithubCopilotSignInIfVersion(_ context.Context, row *tables.TableGithubCopilotSignIn, expected int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.rows[row.KeyID]
	if !ok || existing.Version != expected {
		return false, nil
	}
	c := *row
	c.Version = expected + 1
	c.RefreshLeaseUntil = nil
	c.UpdatedAt = time.Now().UTC()
	s.rows[row.KeyID] = c
	row.Version = c.Version
	return true, nil
}

func (s *memStore) ClaimGithubCopilotSignInRefresh(_ context.Context, keyID string, version int64, until time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[keyID]
	if !ok || r.Version != version || (r.RefreshLeaseUntil != nil && r.RefreshLeaseUntil.After(time.Now())) {
		return false, nil
	}
	r.RefreshLeaseUntil = &until
	s.rows[keyID] = r
	return true, nil
}

func (s *memStore) DeleteGithubCopilotSignIn(_ context.Context, keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, keyID)
	return nil
}

// put seeds a row directly, bypassing the version bump.
func (s *memStore) put(row tables.TableGithubCopilotSignIn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[row.KeyID] = row
}

// fakeGithub serves the device-code, token and user endpoints.
type fakeGithub struct {
	server        *httptest.Server
	pollReply     atomic.Value // map[string]any returned for device_code polls
	refreshReply  atomic.Value // map[string]any returned for refresh grants
	refreshHits   atomic.Int64
	lastRefreshed atomic.Value // refresh_token GitHub was asked to redeem
	deviceClient  atomic.Value // client_id sent to the device-code endpoint
}

func newFakeGithub(t *testing.T) *fakeGithub {
	f := &fakeGithub{}
	f.pollReply.Store(map[string]any{"error": "authorization_pending"})
	f.refreshReply.Store(map[string]any{
		"access_token": "ghu_refreshed", "expires_in": 28800,
		"refresh_token": "ghr_rotated", "refresh_token_expires_in": 15897600,
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/login/device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.deviceClient.Store(r.PostForm.Get("client_id"))
		if r.PostForm.Get("client_id") == "Iv1.disabled" {
			writeJSON(w, map[string]any{"error": "device_flow_disabled"})
			return
		}
		writeJSON(w, map[string]any{
			"device_code": "dc_123", "user_code": "ABCD-1234",
			"verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5,
		})
	})
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") == "refresh_token" {
			f.refreshHits.Add(1)
			f.lastRefreshed.Store(r.PostForm.Get("refresh_token"))
			writeJSON(w, f.refreshReply.Load())
			return
		}
		writeJSON(w, f.pollReply.Load())
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghu_new" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"login": "octocat"})
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

type noopLogger struct{}

func (noopLogger) Debug(string, ...any)                   {}
func (noopLogger) Info(string, ...any)                    {}
func (noopLogger) Warn(string, ...any)                    {}
func (noopLogger) Error(string, ...any)                   {}
func (noopLogger) Fatal(string, ...any)                   {}
func (noopLogger) SetLevel(schemas.LogLevel)              {}
func (noopLogger) SetOutputType(schemas.LoggerOutputType) {}
func (noopLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func newTestManager(t *testing.T, store Store, keys ...string) (*Manager, *fakeGithub) {
	f := newFakeGithub(t)
	saved := map[string]bool{}
	for _, k := range keys {
		saved[k] = true
	}
	m := New(store, noopLogger{}, func(keyID string) bool { return saved[keyID] })
	m.endpoints = endpoints{
		deviceCodeURL: f.server.URL + "/login/device/code",
		tokenURL:      f.server.URL + "/login/oauth/access_token",
		userURL:       f.server.URL + "/user",
	}
	return m, f
}

func expiringRow(keyID string, version int64, accessLeft time.Duration) tables.TableGithubCopilotSignIn {
	now := time.Now().UTC()
	access := now.Add(accessLeft)
	refresh := now.Add(180 * 24 * time.Hour)
	return tables.TableGithubCopilotSignIn{
		KeyID: keyID, ClientID: "Iv23li", AccessToken: "ghu_old", RefreshToken: "ghr_old",
		AccessExpiresAt: &access, RefreshExpiresAt: &refresh,
		Status: tables.GithubCopilotSignInActive, Version: version, CreatedAt: now, UpdatedAt: now,
	}
}

func TestDeviceFlowSignIn(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	m, f := newTestManager(t, store, "key-1")

	code, err := m.StartSignIn(ctx, "Iv23li")
	require.NoError(t, err)
	assert.Equal(t, "ABCD-1234", code.UserCode)
	assert.Equal(t, "dc_123", code.DeviceCode)
	assert.Equal(t, "Iv23li", f.deviceClient.Load())

	res, err := m.PollSignIn(ctx, "key-1", "Iv23li", code.DeviceCode)
	require.NoError(t, err)
	assert.Equal(t, "pending", res.Status)
	_, ok := m.AccessToken("key-1")
	assert.False(t, ok, "nothing is usable before the operator approves")

	f.pollReply.Store(map[string]any{
		"access_token": "ghu_new", "expires_in": 28800,
		"refresh_token": "ghr_new", "refresh_token_expires_in": 15897600,
	})
	res, err = m.PollSignIn(ctx, "key-1", "Iv23li", code.DeviceCode)
	require.NoError(t, err)
	assert.Equal(t, "complete", res.Status)
	assert.Equal(t, "octocat", res.Login)

	token, ok := m.AccessToken("key-1")
	require.True(t, ok)
	assert.Equal(t, "ghu_new", token)

	row, _ := store.GetGithubCopilotSignIn(ctx, "key-1")
	require.NotNil(t, row)
	assert.Equal(t, "ghr_new", row.RefreshToken)
	assert.Equal(t, "Iv23li", row.ClientID, "the client ID is kept because refreshing needs it")
	require.NotNil(t, row.AccessExpiresAt)
	assert.WithinDuration(t, time.Now().Add(8*time.Hour), *row.AccessExpiresAt, time.Minute)

	status, err := m.Status(ctx, "key-1")
	require.NoError(t, err)
	assert.True(t, status.SignedIn)
	assert.Equal(t, "octocat", status.Login)
}

func TestPollOutcomes(t *testing.T) {
	ctx := context.Background()
	m, f := newTestManager(t, newMemStore(), "key-1")

	f.pollReply.Store(map[string]any{"error": "slow_down", "interval": 10})
	res, err := m.PollSignIn(ctx, "key-1", "Iv23li", "dc")
	require.NoError(t, err)
	assert.Equal(t, PollResult{Status: "pending", Interval: 10}, *res, "slow_down carries GitHub's new interval")

	f.pollReply.Store(map[string]any{"error": "expired_token"})
	res, err = m.PollSignIn(ctx, "key-1", "Iv23li", "dc")
	require.NoError(t, err)
	assert.Equal(t, "expired", res.Status)

	f.pollReply.Store(map[string]any{"error": "access_denied"})
	res, err = m.PollSignIn(ctx, "key-1", "Iv23li", "dc")
	require.NoError(t, err)
	assert.Equal(t, "denied", res.Status)

	_, err = m.StartSignIn(ctx, "Iv1.disabled")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Enable Device Flow", "the operator is told which App setting to change")

	_, err = m.StartSignIn(ctx, "bad id\nwith newline")
	require.Error(t, err)
}

func TestRefresh(t *testing.T) {
	ctx := context.Background()

	t.Run("a token near expiry is refreshed and the rotated refresh token stored", func(t *testing.T) {
		store := newMemStore()
		store.put(expiringRow("key-1", 3, 5*time.Minute))
		m, f := newTestManager(t, store, "key-1")

		m.tick(ctx)

		assert.Equal(t, "ghr_old", f.lastRefreshed.Load())
		token, ok := m.AccessToken("key-1")
		require.True(t, ok)
		assert.Equal(t, "ghu_refreshed", token)
		row, _ := store.GetGithubCopilotSignIn(ctx, "key-1")
		assert.Equal(t, "ghr_rotated", row.RefreshToken)
		assert.Equal(t, int64(4), row.Version)

		m.tick(ctx)
		assert.Equal(t, int64(1), f.refreshHits.Load(), "a fresh token is not refreshed again")
	})

	t.Run("a token with hours left is left alone", func(t *testing.T) {
		store := newMemStore()
		store.put(expiringRow("key-1", 1, 4*time.Hour))
		m, f := newTestManager(t, store, "key-1")

		m.tick(ctx)

		assert.Zero(t, f.refreshHits.Load())
		token, _ := m.AccessToken("key-1")
		assert.Equal(t, "ghu_old", token)
	})

	t.Run("a rejected refresh asks for a new sign-in", func(t *testing.T) {
		store := newMemStore()
		store.put(expiringRow("key-1", 1, 5*time.Minute))
		m, f := newTestManager(t, store, "key-1")
		f.refreshReply.Store(map[string]any{"error": "bad_refresh_token", "error_description": "The refresh token passed is incorrect or expired."})

		m.tick(ctx)

		_, ok := m.AccessToken("key-1")
		assert.False(t, ok)
		status, err := m.Status(ctx, "key-1")
		require.NoError(t, err)
		assert.False(t, status.SignedIn)
		assert.Equal(t, tables.GithubCopilotSignInNeedsReauth, status.Status)
		assert.Contains(t, status.StatusReason, "bad_refresh_token")
	})

	t.Run("a network failure keeps the current token", func(t *testing.T) {
		store := newMemStore()
		store.put(expiringRow("key-1", 1, 5*time.Minute))
		m, f := newTestManager(t, store, "key-1")
		f.server.Close()

		m.tick(ctx)

		token, ok := m.AccessToken("key-1")
		require.True(t, ok, "the old token still has minutes left")
		assert.Equal(t, "ghu_old", token)
		row, _ := store.GetGithubCopilotSignIn(ctx, "key-1")
		assert.Equal(t, tables.GithubCopilotSignInActive, row.Status)
	})

	t.Run("another node holding the lease means no second redemption", func(t *testing.T) {
		store := newMemStore()
		row := expiringRow("key-1", 1, 5*time.Minute)
		lease := time.Now().Add(time.Minute)
		row.RefreshLeaseUntil = &lease
		store.put(row)
		m, f := newTestManager(t, store, "key-1")

		m.tick(ctx)

		assert.Zero(t, f.refreshHits.Load(), "redeeming a refresh token twice signs the key out")
	})

	t.Run("a token that never expires is never refreshed", func(t *testing.T) {
		store := newMemStore()
		row := expiringRow("key-1", 1, 0)
		row.AccessExpiresAt, row.RefreshExpiresAt, row.RefreshToken = nil, nil, ""
		store.put(row)
		m, f := newTestManager(t, store, "key-1")

		m.tick(ctx)

		assert.Zero(t, f.refreshHits.Load())
		token, ok := m.AccessToken("key-1")
		require.True(t, ok)
		assert.Equal(t, "ghu_old", token)
	})

	t.Run("an expired refresh token asks for a new sign-in without calling GitHub", func(t *testing.T) {
		store := newMemStore()
		row := expiringRow("key-1", 1, -time.Minute)
		past := time.Now().Add(-time.Hour)
		row.RefreshExpiresAt = &past
		store.put(row)
		m, f := newTestManager(t, store, "key-1")

		m.tick(ctx)

		assert.Zero(t, f.refreshHits.Load())
		status, _ := m.Status(ctx, "key-1")
		assert.Equal(t, tables.GithubCopilotSignInNeedsReauth, status.Status)
	})
}

func TestTickPicksUpOtherNodesWrites(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	store.put(expiringRow("key-1", 1, 4*time.Hour))
	m, _ := newTestManager(t, store, "key-1")
	m.load(ctx)

	// Another node refreshed: the old access token is now dead on GitHub's side.
	newer := expiringRow("key-1", 2, 8*time.Hour)
	newer.AccessToken = "ghu_from_other_node"
	store.put(newer)
	m.tick(ctx)

	token, _ := m.AccessToken("key-1")
	assert.Equal(t, "ghu_from_other_node", token)
}

func TestSignOut(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	stale := expiringRow("key-1", 1, 4*time.Hour)
	store.put(stale)
	m, _ := newTestManager(t, store, "key-1")
	m.load(ctx)

	require.NoError(t, m.SignOut(ctx, "key-1"))

	_, ok := m.AccessToken("key-1")
	assert.False(t, ok)
	status, err := m.Status(ctx, "key-1")
	require.NoError(t, err)
	assert.False(t, status.SignedIn)
	assert.Empty(t, status.Status, "a signed-out key reads as never signed in")

	// A tick that read the store before the sign-out must not bring the token back.
	m.cache(&stale)
	_, ok = m.AccessToken("key-1")
	assert.False(t, ok)
}

func TestOrphanedSignIns(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	old := expiringRow("deleted-key", 1, 4*time.Hour)
	old.UpdatedAt = time.Now().Add(-2 * time.Hour)
	store.put(old)
	store.put(expiringRow("unsaved-key", 1, 4*time.Hour))
	m, _ := newTestManager(t, store)

	m.tick(ctx)

	row, _ := store.GetGithubCopilotSignIn(ctx, "deleted-key")
	assert.Nil(t, row, "a sign-in for a deleted key is removed")
	row, _ = store.GetGithubCopilotSignIn(ctx, "unsaved-key")
	assert.NotNil(t, row, "a key still being created keeps its sign-in during the grace period")
}

func TestAccessTokenExpiredIsUnavailable(t *testing.T) {
	store := newMemStore()
	m, _ := newTestManager(t, store, "key-1")
	row := expiringRow("key-1", 1, -time.Second)
	m.cache(&row)

	_, ok := m.AccessToken("key-1")
	assert.False(t, ok, "an expired token is never handed to a request")
}

func TestAccessTokenAllocations(t *testing.T) {
	store := newMemStore()
	m, _ := newTestManager(t, store, "key-1")
	row := expiringRow("key-1", 1, 4*time.Hour)
	m.cache(&row)

	allocs := testing.AllocsPerRun(1000, func() {
		_, _ = m.AccessToken("key-1")
	})
	assert.Zero(t, allocs, "the request path must not allocate")
}

func BenchmarkAccessToken(b *testing.B) {
	m := New(newMemStore(), noopLogger{}, func(string) bool { return true })
	row := expiringRow("key-1", 1, 4*time.Hour)
	m.cache(&row)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = m.AccessToken("key-1")
	}
}
