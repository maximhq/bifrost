package handlers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	oidcpkg "github.com/maximhq/bifrost/framework/oidc"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// oidcTestServer creates a mock OIDC server for testing the middleware.
type oidcTestServer struct {
	server     *httptest.Server
	privateKey *rsa.PrivateKey
	kid        string
}

func newOIDCTestServer(t *testing.T) *oidcTestServer {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	s := &oidcTestServer{
		privateKey: privateKey,
		kid:        "test-key-1",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", s.handleDiscovery)
	mux.HandleFunc("/protocol/openid-connect/certs", s.handleJWKS)

	s.server = httptest.NewServer(mux)
	return s
}

func (s *oidcTestServer) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	discovery := map[string]interface{}{
		"issuer":                                s.server.URL,
		"authorization_endpoint":                s.server.URL + "/protocol/openid-connect/auth",
		"token_endpoint":                        s.server.URL + "/protocol/openid-connect/token",
		"jwks_uri":                              s.server.URL + "/protocol/openid-connect/certs",
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(discovery)
}

func (s *oidcTestServer) handleJWKS(w http.ResponseWriter, r *http.Request) {
	n := s.privateKey.PublicKey.N
	e := s.privateKey.PublicKey.E

	jwks := map[string]interface{}{
		"keys": []map[string]interface{}{
			{
				"kty": "RSA",
				"use": "sig",
				"kid": s.kid,
				"alg": "RS256",
				"n":   oidcBase64urlEncode(n.Bytes()),
				"e":   oidcBase64urlEncode(big.NewInt(int64(e)).Bytes()),
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jwks)
}

func (s *oidcTestServer) close() {
	s.server.Close()
}

func (s *oidcTestServer) issuerURL() string {
	return s.server.URL
}

func (s *oidcTestServer) signToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = s.kid
	signed, err := token.SignedString(s.privateKey)
	require.NoError(t, err)
	return signed
}

// oidcBase64urlEncode encodes bytes to base64url without padding (per JWK spec).
func oidcBase64urlEncode(data []byte) string {
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

// mockConfigStore implements a minimal configstore.ConfigStore for testing.
// Only GetCustomer and GetTeams are needed; all others panic.
type mockConfigStore struct {
	configstore.ConfigStore
	customers map[string]*tables.TableCustomer
	teams     map[string][]tables.TableTeam
}

func newMockConfigStore() *mockConfigStore {
	return &mockConfigStore{
		customers: make(map[string]*tables.TableCustomer),
		teams:     make(map[string][]tables.TableTeam),
	}
}

func (m *mockConfigStore) GetCustomer(_ context.Context, id string) (*tables.TableCustomer, error) {
	if c, ok := m.customers[id]; ok {
		return c, nil
	}
	return nil, nil
}

func (m *mockConfigStore) GetTeams(_ context.Context, customerID string) ([]tables.TableTeam, error) {
	if t, ok := m.teams[customerID]; ok {
		return t, nil
	}
	return nil, nil
}

// createTestProvider creates an OIDCProvider pointing to the test server.
func createTestProvider(t *testing.T, srv *oidcTestServer) *oidcpkg.OIDCProvider {
	t.Helper()
	config := &oidcpkg.OIDCConfig{
		IssuerURL: srv.issuerURL(),
		ClientID:  "test-client",
	}
	provider, err := oidcpkg.NewOIDCProvider(config)
	require.NoError(t, err)
	return provider
}

// validClaims returns default valid JWT claims for testing.
func validClaims(issuerURL string) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":             issuerURL,
		"aud":             "test-client",
		"sub":             "user-123",
		"email":           "user@example.com",
		"email_verified":  true,
		"name":            "Test User",
		"organization_id": "org-456",
		"groups":          []string{"engineering", "platform"},
		"exp":             now.Add(1 * time.Hour).Unix(),
		"iat":             now.Unix(),
	}
}

func TestOIDCMiddleware_ValidJWT_SetsContextKeys(t *testing.T) {
	srv := newOIDCTestServer(t)
	defer srv.close()

	provider := createTestProvider(t, srv)
	store := newMockConfigStore()
	store.customers["org-456"] = &tables.TableCustomer{ID: "org-456", Name: "Test Org"}
	store.teams["org-456"] = []tables.TableTeam{
		{ID: "team-1", Name: "engineering"},
		{ID: "team-2", Name: "platform"},
	}

	SetLogger(&mockLogger{})

	token := srv.signToken(t, validClaims(srv.issuerURL()))

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer "+token)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := OIDCMiddleware(provider, store)
	handler := middleware(next)
	handler(ctx)

	assert.True(t, nextCalled, "next handler should be called")

	// Verify OIDC context keys are set
	assert.Equal(t, true, ctx.UserValue(oidcpkg.BifrostContextKeyOIDCAuthenticated))
	assert.Equal(t, "user-123", ctx.UserValue(oidcpkg.BifrostContextKeyOIDCSub))
	assert.Equal(t, "user@example.com", ctx.UserValue(oidcpkg.BifrostContextKeyOIDCEmail))
	assert.Equal(t, "org-456", ctx.UserValue(oidcpkg.BifrostContextKeyOIDCOrgID))
	assert.Equal(t, []string{"engineering", "platform"}, ctx.UserValue(oidcpkg.BifrostContextKeyOIDCGroups))

	// Verify governance entity resolution
	assert.Equal(t, "org-456", ctx.UserValue("BifrostContextKeyOIDCCustomerID"))
	teamIDs, ok := ctx.UserValue("BifrostContextKeyOIDCTeamIDs").([]string)
	assert.True(t, ok, "team IDs should be a string slice")
	assert.ElementsMatch(t, []string{"team-1", "team-2"}, teamIDs)
}

func TestOIDCMiddleware_ValidJWT_CustomerNotFound_Returns403(t *testing.T) {
	srv := newOIDCTestServer(t)
	defer srv.close()

	provider := createTestProvider(t, srv)
	store := newMockConfigStore()
	// No customer with org-456 in store

	SetLogger(&mockLogger{})

	token := srv.signToken(t, validClaims(srv.issuerURL()))

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer "+token)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := OIDCMiddleware(provider, store)
	handler := middleware(next)
	handler(ctx)

	assert.False(t, nextCalled, "next handler should NOT be called when customer not found")
	assert.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode())
}

func TestOIDCMiddleware_ValidJWT_EmptyOrgID_Returns403(t *testing.T) {
	srv := newOIDCTestServer(t)
	defer srv.close()

	provider := createTestProvider(t, srv)
	store := newMockConfigStore()

	SetLogger(&mockLogger{})

	claims := validClaims(srv.issuerURL())
	claims["organization_id"] = ""
	token := srv.signToken(t, claims)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer "+token)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := OIDCMiddleware(provider, store)
	handler := middleware(next)
	handler(ctx)

	assert.False(t, nextCalled, "next handler should NOT be called when org_id is empty")
	assert.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode())
}

func TestOIDCMiddleware_ValidJWT_UnmappedGroups_SilentlySkipped(t *testing.T) {
	srv := newOIDCTestServer(t)
	defer srv.close()

	provider := createTestProvider(t, srv)
	store := newMockConfigStore()
	store.customers["org-456"] = &tables.TableCustomer{ID: "org-456", Name: "Test Org"}
	store.teams["org-456"] = []tables.TableTeam{
		{ID: "team-1", Name: "engineering"},
		// "platform" group has no matching team -- should be silently skipped
	}

	SetLogger(&mockLogger{})

	token := srv.signToken(t, validClaims(srv.issuerURL()))

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer "+token)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := OIDCMiddleware(provider, store)
	handler := middleware(next)
	handler(ctx)

	assert.True(t, nextCalled)

	// Only "engineering" should be resolved, "platform" silently skipped
	teamIDs, ok := ctx.UserValue("BifrostContextKeyOIDCTeamIDs").([]string)
	assert.True(t, ok)
	assert.Equal(t, []string{"team-1"}, teamIDs)
}

func TestOIDCMiddleware_ExpiredJWT_Returns401(t *testing.T) {
	srv := newOIDCTestServer(t)
	defer srv.close()

	provider := createTestProvider(t, srv)
	store := newMockConfigStore()

	SetLogger(&mockLogger{})

	past := time.Now().Add(-2 * time.Hour)
	claims := jwt.MapClaims{
		"iss":             srv.issuerURL(),
		"aud":             "test-client",
		"sub":             "user-123",
		"email":           "user@example.com",
		"organization_id": "org-456",
		"exp":             past.Unix(),
		"iat":             past.Add(-1 * time.Hour).Unix(),
	}
	token := srv.signToken(t, claims)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer "+token)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := OIDCMiddleware(provider, store)
	handler := middleware(next)
	handler(ctx)

	assert.False(t, nextCalled, "next handler should NOT be called for expired JWT")
	assert.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode())
}

func TestOIDCMiddleware_InvalidSignature_Returns401(t *testing.T) {
	srv := newOIDCTestServer(t)
	defer srv.close()

	provider := createTestProvider(t, srv)
	store := newMockConfigStore()

	SetLogger(&mockLogger{})

	// Sign with a different key
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	now := time.Now()
	jwtToken := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss":             srv.issuerURL(),
		"aud":             "test-client",
		"sub":             "user-123",
		"organization_id": "org-456",
		"exp":             now.Add(1 * time.Hour).Unix(),
		"iat":             now.Unix(),
	})
	jwtToken.Header["kid"] = srv.kid
	token, err := jwtToken.SignedString(otherKey)
	require.NoError(t, err)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer "+token)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := OIDCMiddleware(provider, store)
	handler := middleware(next)
	handler(ctx)

	assert.False(t, nextCalled, "next handler should NOT be called for invalid signature")
	assert.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode())
}

func TestOIDCMiddleware_NonJWTBearer_PassesThrough(t *testing.T) {
	srv := newOIDCTestServer(t)
	defer srv.close()

	provider := createTestProvider(t, srv)
	store := newMockConfigStore()

	SetLogger(&mockLogger{})

	// Session UUID -- not a JWT
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer 550e8400-e29b-41d4-a716-446655440000")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := OIDCMiddleware(provider, store)
	handler := middleware(next)
	handler(ctx)

	assert.True(t, nextCalled, "next handler should be called for non-JWT Bearer token")
	// OIDC context keys should NOT be set
	assert.Nil(t, ctx.UserValue(oidcpkg.BifrostContextKeyOIDCAuthenticated))
}

func TestOIDCMiddleware_NoAuthHeader_PassesThrough(t *testing.T) {
	srv := newOIDCTestServer(t)
	defer srv.close()

	provider := createTestProvider(t, srv)
	store := newMockConfigStore()

	SetLogger(&mockLogger{})

	ctx := &fasthttp.RequestCtx{}
	// No Authorization header

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := OIDCMiddleware(provider, store)
	handler := middleware(next)
	handler(ctx)

	assert.True(t, nextCalled, "next handler should be called when no auth header")
	assert.Nil(t, ctx.UserValue(oidcpkg.BifrostContextKeyOIDCAuthenticated))
}

func TestOIDCMiddleware_BasicAuth_PassesThrough(t *testing.T) {
	srv := newOIDCTestServer(t)
	defer srv.close()

	provider := createTestProvider(t, srv)
	store := newMockConfigStore()

	SetLogger(&mockLogger{})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Basic dXNlcjpwYXNz")

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := OIDCMiddleware(provider, store)
	handler := middleware(next)
	handler(ctx)

	assert.True(t, nextCalled, "next handler should be called for Basic auth")
	assert.Nil(t, ctx.UserValue(oidcpkg.BifrostContextKeyOIDCAuthenticated))
}

func TestOIDCMiddleware_NilProvider_PassesThrough(t *testing.T) {
	store := newMockConfigStore()

	SetLogger(&mockLogger{})

	token := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.signature"
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer "+token)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := OIDCMiddleware(nil, store)
	handler := middleware(next)
	handler(ctx)

	assert.True(t, nextCalled, "next handler should be called when provider is nil")
	assert.Nil(t, ctx.UserValue(oidcpkg.BifrostContextKeyOIDCAuthenticated))
}

func TestOIDCMiddleware_ValidJWT_NoGroups_NoTeamIDs(t *testing.T) {
	srv := newOIDCTestServer(t)
	defer srv.close()

	provider := createTestProvider(t, srv)
	store := newMockConfigStore()
	store.customers["org-456"] = &tables.TableCustomer{ID: "org-456", Name: "Test Org"}

	SetLogger(&mockLogger{})

	claims := validClaims(srv.issuerURL())
	delete(claims, "groups")
	token := srv.signToken(t, claims)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer "+token)

	nextCalled := false
	next := func(ctx *fasthttp.RequestCtx) {
		nextCalled = true
	}

	middleware := OIDCMiddleware(provider, store)
	handler := middleware(next)
	handler(ctx)

	assert.True(t, nextCalled)
	assert.Equal(t, true, ctx.UserValue(oidcpkg.BifrostContextKeyOIDCAuthenticated))
	assert.Nil(t, ctx.UserValue("BifrostContextKeyOIDCTeamIDs"), "team IDs should not be set when no groups")
}
