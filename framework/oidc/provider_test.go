package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testOIDCServer creates a mock OIDC server that serves discovery and JWKS endpoints.
type testOIDCServer struct {
	server     *httptest.Server
	privateKey *rsa.PrivateKey
	kid        string
	jwksFetches atomic.Int64
}

func newTestOIDCServer(t *testing.T) *testOIDCServer {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	s := &testOIDCServer{
		privateKey: privateKey,
		kid:        "test-key-1",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", s.handleDiscovery)
	mux.HandleFunc("/protocol/openid-connect/certs", s.handleJWKS)

	s.server = httptest.NewServer(mux)
	return s
}

func (s *testOIDCServer) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	discovery := map[string]interface{}{
		"issuer":                 s.server.URL,
		"authorization_endpoint": s.server.URL + "/protocol/openid-connect/auth",
		"token_endpoint":         s.server.URL + "/protocol/openid-connect/token",
		"jwks_uri":               s.server.URL + "/protocol/openid-connect/certs",
		"subject_types_supported": []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(discovery)
}

func (s *testOIDCServer) handleJWKS(w http.ResponseWriter, r *http.Request) {
	s.jwksFetches.Add(1)

	// Build JWKS response with RSA public key
	n := s.privateKey.PublicKey.N
	e := s.privateKey.PublicKey.E

	jwks := map[string]interface{}{
		"keys": []map[string]interface{}{
			{
				"kty": "RSA",
				"use": "sig",
				"kid": s.kid,
				"alg": "RS256",
				"n":   base64urlEncode(n.Bytes()),
				"e":   base64urlEncode(big.NewInt(int64(e)).Bytes()),
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jwks)
}

func (s *testOIDCServer) close() {
	s.server.Close()
}

func (s *testOIDCServer) issuerURL() string {
	return s.server.URL
}

// signToken creates a signed JWT with the given claims using the test server's private key.
func (s *testOIDCServer) signToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = s.kid

	signed, err := token.SignedString(s.privateKey)
	require.NoError(t, err)
	return signed
}

// base64urlEncode encodes bytes to base64url without padding (per JWK spec).
func base64urlEncode(data []byte) string {
	const base64url = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	result := make([]byte, 0, len(data)*4/3+4)
	for i := 0; i < len(data); i += 3 {
		var b uint32
		remaining := len(data) - i
		switch {
		case remaining >= 3:
			b = uint32(data[i])<<16 | uint32(data[i+1])<<8 | uint32(data[i+2])
			result = append(result, base64url[b>>18&0x3F], base64url[b>>12&0x3F], base64url[b>>6&0x3F], base64url[b&0x3F])
		case remaining == 2:
			b = uint32(data[i])<<16 | uint32(data[i+1])<<8
			result = append(result, base64url[b>>18&0x3F], base64url[b>>12&0x3F], base64url[b>>6&0x3F])
		case remaining == 1:
			b = uint32(data[i]) << 16
			result = append(result, base64url[b>>18&0x3F], base64url[b>>12&0x3F])
		}
	}
	return string(result)
}

func TestNewOIDCProvider_Success(t *testing.T) {
	srv := newTestOIDCServer(t)
	defer srv.close()

	config := &OIDCConfig{
		IssuerURL: srv.issuerURL(),
		ClientID:  "test-client",
	}

	provider, err := NewOIDCProvider(config)
	require.NoError(t, err)
	require.NotNil(t, provider)
	assert.Equal(t, config, provider.GetConfig())
}

func TestNewOIDCProvider_InvalidIssuerURL(t *testing.T) {
	config := &OIDCConfig{
		IssuerURL: "http://localhost:1/nonexistent",
		ClientID:  "test-client",
	}

	provider, err := NewOIDCProvider(config)
	require.Error(t, err)
	assert.Nil(t, provider)
	assert.Contains(t, err.Error(), "OIDC discovery failed")
}

func TestNewOIDCProvider_InvalidConfig(t *testing.T) {
	config := &OIDCConfig{
		// Missing required fields
	}

	provider, err := NewOIDCProvider(config)
	require.Error(t, err)
	assert.Nil(t, provider)
	assert.Contains(t, err.Error(), "invalid OIDC config")
}

func TestValidateToken_ValidJWT(t *testing.T) {
	srv := newTestOIDCServer(t)
	defer srv.close()

	config := &OIDCConfig{
		IssuerURL: srv.issuerURL(),
		ClientID:  "test-client",
	}

	provider, err := NewOIDCProvider(config)
	require.NoError(t, err)

	now := time.Now()
	claims := jwt.MapClaims{
		"iss":             srv.issuerURL(),
		"aud":             "test-client",
		"sub":             "user-123",
		"email":           "user@example.com",
		"email_verified":  true,
		"name":            "Test User",
		"organization_id": "org-456",
		"groups":          []string{"admin"},
		"exp":             now.Add(1 * time.Hour).Unix(),
		"iat":             now.Unix(),
	}

	token := srv.signToken(t, claims)
	result, err := provider.ValidateToken(context.Background(), token)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "user-123", result.Subject)
	assert.Equal(t, "user@example.com", result.Email)
	assert.Equal(t, "org-456", result.OrgID)
	assert.Equal(t, []string{"admin"}, result.Groups)
}

func TestValidateToken_ExpiredJWT(t *testing.T) {
	srv := newTestOIDCServer(t)
	defer srv.close()

	config := &OIDCConfig{
		IssuerURL: srv.issuerURL(),
		ClientID:  "test-client",
	}

	provider, err := NewOIDCProvider(config)
	require.NoError(t, err)

	past := time.Now().Add(-2 * time.Hour)
	claims := jwt.MapClaims{
		"iss": srv.issuerURL(),
		"aud": "test-client",
		"sub": "user-123",
		"exp": past.Unix(),
		"iat": past.Add(-1 * time.Hour).Unix(),
	}

	token := srv.signToken(t, claims)
	result, err := provider.ValidateToken(context.Background(), token)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "token verification failed")
}

func TestValidateToken_WrongAudience(t *testing.T) {
	srv := newTestOIDCServer(t)
	defer srv.close()

	config := &OIDCConfig{
		IssuerURL: srv.issuerURL(),
		ClientID:  "test-client",
	}

	provider, err := NewOIDCProvider(config)
	require.NoError(t, err)

	now := time.Now()
	claims := jwt.MapClaims{
		"iss": srv.issuerURL(),
		"aud": "wrong-client", // Wrong audience
		"sub": "user-123",
		"exp": now.Add(1 * time.Hour).Unix(),
		"iat": now.Unix(),
	}

	token := srv.signToken(t, claims)
	result, err := provider.ValidateToken(context.Background(), token)
	require.Error(t, err)
	assert.Nil(t, result)
}

func TestSingleflightJWKSRefresh(t *testing.T) {
	srv := newTestOIDCServer(t)
	defer srv.close()

	config := &OIDCConfig{
		IssuerURL: srv.issuerURL(),
		ClientID:  "test-client",
	}

	provider, err := NewOIDCProvider(config)
	require.NoError(t, err)

	// Reset JWKS fetch counter after initial provider creation
	srv.jwksFetches.Store(0)

	// Launch multiple concurrent refreshAndRetry calls
	// All should result in a single JWKS fetch via singleflight
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": srv.issuerURL(),
		"aud": "test-client",
		"sub": "user-123",
		"exp": now.Add(1 * time.Hour).Unix(),
		"iat": now.Unix(),
	}
	token := srv.signToken(t, claims)

	var wg sync.WaitGroup
	concurrency := 10
	errors := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errors[idx] = provider.refreshAndRetry(context.Background(), token)
		}(i)
	}

	wg.Wait()

	// All calls should succeed
	for i, err := range errors {
		assert.NoError(t, err, "goroutine %d failed", i)
	}

	// Singleflight should have deduplicated: only 1 JWKS fetch
	// (but 2 is possible if the first finishes before others start)
	fetches := srv.jwksFetches.Load()
	assert.LessOrEqual(t, fetches, int64(2),
		"expected at most 2 JWKS fetches (singleflight dedup), got %d", fetches)
}

func TestIsJWT_ValidFormat(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{
			name:  "valid JWT format (3 dot-separated segments)",
			token: "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.signature",
			want:  true,
		},
		{
			name:  "minimal valid format",
			token: "abc.def.ghi",
			want:  true,
		},
		{
			name:  "UUID session token",
			token: "550e8400-e29b-41d4-a716-446655440000",
			want:  false,
		},
		{
			name:  "empty string",
			token: "",
			want:  false,
		},
		{
			name:  "short string",
			token: "a.b.c",
			want:  false,
		},
		{
			name:  "two segments only",
			token: "header.payload",
			want:  false,
		},
		{
			name:  "four segments",
			token: "a.b.c.d.e.f.g.h",
			want:  false,
		},
		{
			name:  "single segment",
			token: "justasingletoken",
			want:  false,
		},
		{
			name:  "empty segment in middle",
			token: "header..signature",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsJWT(tt.token)
			assert.Equal(t, tt.want, got, "IsJWT(%q) = %v, want %v", tt.token, got, tt.want)
		})
	}
}

func TestIsUnknownKidError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "failed to verify", err: fmt.Errorf("failed to verify signature"), want: true},
		{name: "no keys", err: fmt.Errorf("no keys found"), want: true},
		{name: "key not found", err: fmt.Errorf("key not found for kid xyz"), want: true},
		{name: "token expired", err: fmt.Errorf("token is expired"), want: false},
		{name: "invalid audience", err: fmt.Errorf("invalid audience"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isUnknownKidError(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}
