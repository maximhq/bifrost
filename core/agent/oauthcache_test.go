package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestAgentOAuthInspectionCacheRetention bounds ad hoc inspection credentials
// without requiring durable registrations, including unsuccessful token fetches.
func TestAgentOAuthInspectionCacheRetention(t *testing.T) {
	const inspections = maxOAuthCacheEntries + 32
	for _, success := range []bool{true, false} {
		t.Run(fmt.Sprintf("success=%t", success), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if !success {
					http.Error(w, "invalid credentials", http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"inspection-token","expires_in":3600}`))
			}))
			defer server.Close()
			m := &Manager{
				privateAllowlistFn: func() []string { return []string{"127.0.0.1"} },
				httpClient:         server.Client(),
			}
			auth := &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeOauth, OAuth: &schemas.UpstreamOAuthConfig{
				TokenURL: server.URL, ClientID: schemas.NewSecretVar("client"), ClientSecret: schemas.NewSecretVar("secret"),
			}}
			for i := range inspections {
				// Inspect derives this key from the submitted card URL, not a registration.
				id := upstreamCredentialID(fmt.Sprintf("https://agent.example/card?inspection=%d", i), "discovery")
				_, err := m.oauthAccessToken(context.Background(), id, auth)
				if success {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			}
			m.oauthMu.Lock()
			tokens, fingerprints := len(m.oauthTokens), len(m.oauthFingerprints)
			m.oauthMu.Unlock()
			t.Logf("%d unique inspections retained %d tokens and %d fingerprints", inspections, tokens, fingerprints)
			if success {
				require.Equal(t, maxOAuthCacheEntries, fingerprints)
				require.Equal(t, maxOAuthCacheEntries, tokens)
			} else {
				require.Zero(t, fingerprints)
				require.Zero(t, tokens)
			}
		})
	}
}

// TestAgentOAuthCachePrefersExpiredEntries checks that cache pressure preserves
// live tokens when expired entries can release the required space.
func TestAgentOAuthCachePrefersExpiredEntries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"new-token","expires_in":3600}`))
	}))
	defer server.Close()
	m := &Manager{
		privateAllowlistFn: func() []string { return []string{"127.0.0.1"} },
		httpClient:         server.Client(), oauthTokens: make(map[string]oauthToken),
		oauthFingerprints: make(map[string][sha256.Size]byte),
	}
	for i := range maxOAuthCacheEntries {
		id := fmt.Sprintf("expired-%d", i)
		m.oauthTokens[id] = oauthToken{expiresAt: time.Now().Add(-time.Minute)}
		m.oauthFingerprints[id] = [sha256.Size]byte{}
	}
	m.oauthTokens["expired-0"] = oauthToken{accessToken: "live-token", expiresAt: time.Now().Add(time.Hour)}
	auth := &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeOauth, OAuth: &schemas.UpstreamOAuthConfig{
		TokenURL: server.URL, ClientID: schemas.NewSecretVar("client"), ClientSecret: schemas.NewSecretVar("secret"),
	}}
	token, err := m.oauthAccessToken(context.Background(), "new", auth)
	require.NoError(t, err)
	require.Equal(t, "new-token", token)
	require.Len(t, m.oauthTokens, 2)
	require.Len(t, m.oauthFingerprints, 2)
	require.Equal(t, "live-token", m.oauthTokens["expired-0"].accessToken)
}

// TestAgentOAuthOldConfigPreservesNewToken ensures a late result cannot remove
// or replace a token published by a newer credential configuration.
func TestAgentOAuthOldConfigPreservesNewToken(t *testing.T) {
	for _, oldSucceeds := range []bool{true, false} {
		t.Run(fmt.Sprintf("oldSucceeds=%t", oldSucceeds), func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if r.Form.Get("client_id") == "old" {
					close(started)
					<-release
					if oldSucceeds {
						_, _ = w.Write([]byte(`{"access_token":"old-token","expires_in":3600}`))
					} else {
						http.Error(w, "invalid credentials", http.StatusUnauthorized)
					}
					return
				}
				_, _ = w.Write([]byte(`{"access_token":"new-token","expires_in":3600}`))
			}))
			defer server.Close()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			m := &Manager{privateAllowlistFn: func() []string { return []string{"127.0.0.1"} }, httpClient: server.Client()}
			auth := func(id string) *schemas.UpstreamAuth {
				return &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeOauth, OAuth: &schemas.UpstreamOAuthConfig{
					TokenURL: server.URL, ClientID: schemas.NewSecretVar(id), ClientSecret: schemas.NewSecretVar("secret"),
				}}
			}
			oldDone := make(chan error, 1)
			go func() { _, err := m.oauthAccessToken(context.Background(), "credential", auth("old")); oldDone <- err }()
			<-started
			_, err := m.oauthAccessToken(context.Background(), "credential", auth("new"))
			require.NoError(t, err)
			close(release)
			if oldSucceeds {
				require.NoError(t, <-oldDone)
			} else {
				require.Error(t, <-oldDone)
			}
			m.oauthMu.Lock()
			cached, exists := m.oauthTokens["credential"]
			_, fingerprintExists := m.oauthFingerprints["credential"]
			m.oauthMu.Unlock()
			require.True(t, exists)
			require.True(t, fingerprintExists)
			require.Equal(t, "new-token", cached.accessToken)
		})
	}
}

// BenchmarkAgentOAuthCacheHit measures the request-path cost of reusing a token.
func BenchmarkAgentOAuthCacheHit(b *testing.B) {
	m := &Manager{}
	auth := &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeOauth, OAuth: &schemas.UpstreamOAuthConfig{
		TokenURL: "https://agent.example/token", ClientID: schemas.NewSecretVar("client"), ClientSecret: schemas.NewSecretVar("secret"),
	}}
	fingerprint := oauthConfigFingerprint(auth.OAuth)
	policy, err := m.outboundPolicy(context.Background())
	require.NoError(b, err)
	fingerprint = sha256.Sum256(append(fingerprint[:], policy.fingerprint[:]...))
	m.oauthTokens = map[string]oauthToken{"credential": {
		accessToken: "cached-token", expiresAt: time.Now().Add(time.Hour), fingerprint: fingerprint,
	}}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		token, err := m.oauthAccessToken(context.Background(), "credential", auth)
		if err != nil || token != "cached-token" {
			b.Fatalf("unexpected cached token %q: %v", token, err)
		}
	}
}

// BenchmarkAgentOAuthCacheNewCredentialAtCapacity measures the request-path cost
// of a new credential ID when the cache is full of live tokens: the expiry scan
// over every entry plus one eviction. The new ID's token is pre-placed so no
// fetch runs and only the cache work is timed.
func BenchmarkAgentOAuthCacheNewCredentialAtCapacity(b *testing.B) {
	m := &Manager{}
	auth := &schemas.UpstreamAuth{Type: schemas.MCPAuthTypeOauth, OAuth: &schemas.UpstreamOAuthConfig{
		TokenURL: "https://agent.example/token", ClientID: schemas.NewSecretVar("client"), ClientSecret: schemas.NewSecretVar("secret"),
	}}
	fingerprint := oauthConfigFingerprint(auth.OAuth)
	policy, err := m.outboundPolicy(context.Background())
	require.NoError(b, err)
	fingerprint = sha256.Sum256(append(fingerprint[:], policy.fingerprint[:]...))
	live := oauthToken{accessToken: "cached-token", expiresAt: time.Now().Add(time.Hour), fingerprint: fingerprint}
	m.oauthTokens = make(map[string]oauthToken, maxOAuthCacheEntries+1)
	m.oauthFingerprints = make(map[string][sha256.Size]byte, maxOAuthCacheEntries+1)
	for i := range maxOAuthCacheEntries {
		id := fmt.Sprintf("live-%d", i)
		m.oauthTokens[id] = live
		m.oauthFingerprints[id] = fingerprint
	}
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		id := fmt.Sprintf("new-%d", i)
		i++
		m.oauthMu.Lock()
		m.oauthTokens[id] = live
		m.oauthMu.Unlock()
		token, err := m.oauthAccessToken(context.Background(), id, auth)
		if err != nil || token != "cached-token" {
			b.Fatalf("unexpected cached token %q: %v", token, err)
		}
	}
}
