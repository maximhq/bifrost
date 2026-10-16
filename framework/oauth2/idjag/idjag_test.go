package idjag

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAudience = "https://bifrost.example.com"
	testResource = testAudience + "/mcp"
	testClientID = "claude-client"
)

// testIdP is an httptest IdP serving OIDC discovery and a JWKS with one RSA key.
type testIdP struct {
	srv       *httptest.Server
	key       *rsa.PrivateKey
	kid       string
	jwksHits  atomic.Int32
	discovery bool
}

// newTestIdP starts an IdP; with discovery=false only the JWKS route is served.
func newTestIdP(t *testing.T, discovery bool) *testIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	idp := &testIdP{key: key, kid: "k1", discovery: discovery}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if !idp.discovery {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": idp.srv.URL, "jwks_uri": idp.srv.URL + "/keys"})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, r *http.Request) {
		idp.jwksHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": idp.kid, "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(idp.key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(idp.key.E)).Bytes()),
		}}})
	})
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

// mint signs an ID-JAG with valid defaults, then applies mutate to claims and header.
func (idp *testIdP) mint(t *testing.T, mutate func(claims jwt.MapClaims, header map[string]any)) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": idp.srv.URL, "sub": "00u123", "aud": testAudience, "client_id": testClientID,
		"jti": "jti-1", "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"scope": "mcp", "resource": testResource, "email": "alice@example.com",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["typ"] = TokenType
	tok.Header["kid"] = idp.kid
	if mutate != nil {
		mutate(claims, tok.Header)
	}
	signed, err := tok.SignedString(idp.key)
	require.NoError(t, err)
	return signed
}

// verifier returns a verifier trusting idp.
func (idp *testIdP) verifier() *Verifier {
	return NewVerifier(Config{TrustedIssuer: idp.srv.URL, ExpectedAudience: testAudience, HTTPClient: idp.srv.Client})
}

func TestVerify_ValidAssertion(t *testing.T) {
	idp := newTestIdP(t, true)
	claims, err := idp.verifier().Verify(t.Context(), idp.mint(t, nil), testClientID, testResource)
	require.NoError(t, err)
	assert.Equal(t, "00u123", claims.Subject)
	assert.Equal(t, "alice@example.com", claims.Email)
	assert.Equal(t, "jti-1", claims.JTI)
	assert.Equal(t, []string{testResource}, claims.Resource)
}

func TestVerify_Rejections(t *testing.T) {
	idp := newTestIdP(t, true)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tests := []struct {
		name   string
		token  func() string
		client string
		want   error
	}{
		{"not a jwt", func() string { return "abc.def" }, testClientID, ErrMalformed},
		{"plain jwt typ", func() string {
			return idp.mint(t, func(_ jwt.MapClaims, h map[string]any) { h["typ"] = "JWT" })
		}, testClientID, ErrType},
		{"untrusted issuer", func() string {
			return idp.mint(t, func(c jwt.MapClaims, _ map[string]any) { c["iss"] = "https://evil.example.com" })
		}, testClientID, ErrIssuer},
		{"wrong signing key", func() string {
			tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
				"iss": idp.srv.URL, "sub": "s", "aud": testAudience, "client_id": testClientID,
				"jti": "j", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(),
			})
			tok.Header["typ"] = TokenType
			tok.Header["kid"] = idp.kid
			s, _ := tok.SignedString(other)
			return s
		}, testClientID, ErrSignature},
		{"unknown kid", func() string {
			return idp.mint(t, func(_ jwt.MapClaims, h map[string]any) { h["kid"] = "nope" })
		}, testClientID, ErrKey},
		{"expired", func() string {
			return idp.mint(t, func(c jwt.MapClaims, _ map[string]any) {
				c["exp"] = time.Now().Add(-10 * time.Minute).Unix()
				c["iat"] = time.Now().Add(-20 * time.Minute).Unix()
			})
		}, testClientID, ErrExpired},
		{"missing exp", func() string {
			return idp.mint(t, func(c jwt.MapClaims, _ map[string]any) { delete(c, "exp") })
		}, testClientID, ErrExpired},
		{"issued in the future", func() string {
			return idp.mint(t, func(c jwt.MapClaims, _ map[string]any) { c["iat"] = time.Now().Add(time.Hour).Unix() })
		}, testClientID, ErrExpired},
		{"audience mismatch", func() string {
			return idp.mint(t, func(c jwt.MapClaims, _ map[string]any) { c["aud"] = "https://other.example.com" })
		}, testClientID, ErrAudience},
		{"two audiences", func() string {
			return idp.mint(t, func(c jwt.MapClaims, _ map[string]any) { c["aud"] = []string{testAudience, "https://x"} })
		}, testClientID, ErrAudience},
		{"client mismatch", func() string { return idp.mint(t, nil) }, "someone-else", ErrClient},
		{"resource mismatch", func() string {
			return idp.mint(t, func(c jwt.MapClaims, _ map[string]any) { c["resource"] = "https://other.example.com/mcp" })
		}, testClientID, ErrResource},
		{"missing jti", func() string {
			return idp.mint(t, func(c jwt.MapClaims, _ map[string]any) { delete(c, "jti") })
		}, testClientID, ErrClaims},
		{"missing sub", func() string {
			return idp.mint(t, func(c jwt.MapClaims, _ map[string]any) { delete(c, "sub") })
		}, testClientID, ErrClaims},
	}
	v := idp.verifier()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := v.Verify(t.Context(), tc.token(), tc.client, testResource)
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestVerify_HMACRejectedBeforeKeyLookup(t *testing.T) {
	idp := newTestIdP(t, true)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": idp.srv.URL})
	tok.Header["typ"] = TokenType
	signed, err := tok.SignedString([]byte("secret"))
	require.NoError(t, err)
	_, err = idp.verifier().Verify(t.Context(), signed, testClientID, testResource)
	require.ErrorIs(t, err, ErrAlgorithm)
	assert.Zero(t, idp.jwksHits.Load(), "no key fetch for a rejected algorithm")
}

func TestVerify_AcceptsVariantShapes(t *testing.T) {
	idp := newTestIdP(t, true)
	v := idp.verifier()
	for name, mutate := range map[string]func(jwt.MapClaims, map[string]any){
		"aud array":           func(c jwt.MapClaims, _ map[string]any) { c["aud"] = []string{testAudience} },
		"aud trailing slash":  func(c jwt.MapClaims, _ map[string]any) { c["aud"] = testAudience + "/" },
		"no resource claim":   func(c jwt.MapClaims, _ map[string]any) { delete(c, "resource") },
		"application/ typ":    func(_ jwt.MapClaims, h map[string]any) { h["typ"] = "application/" + TokenType },
		"resource array":      func(c jwt.MapClaims, _ map[string]any) { c["resource"] = []string{"https://x", testResource} },
		"uppercase typ value": func(_ jwt.MapClaims, h map[string]any) { h["typ"] = "OAUTH-ID-JAG+JWT" },
	} {
		t.Run(name, func(t *testing.T) {
			_, err := v.Verify(t.Context(), idp.mint(t, mutate), testClientID, testResource)
			require.NoError(t, err)
		})
	}
}

func TestVerify_ExplicitJWKSURLSkipsDiscovery(t *testing.T) {
	idp := newTestIdP(t, false)
	v := NewVerifier(Config{TrustedIssuer: idp.srv.URL, JWKSURL: idp.srv.URL + "/keys", ExpectedAudience: testAudience, HTTPClient: idp.srv.Client})
	_, err := v.Verify(t.Context(), idp.mint(t, nil), testClientID, testResource)
	require.NoError(t, err)

	_, err = idp.verifier().Verify(t.Context(), idp.mint(t, nil), testClientID, testResource)
	require.ErrorIs(t, err, ErrKey, "without discovery or an explicit jwks_url there is no key")
}

func TestVerify_KeysCachedAndUnknownKidRefetchRateLimited(t *testing.T) {
	idp := newTestIdP(t, true)
	v := idp.verifier()
	for range 3 {
		_, err := v.Verify(t.Context(), idp.mint(t, nil), testClientID, testResource)
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), idp.jwksHits.Load(), "valid kids reuse the cached set")

	for range 3 {
		_, err := v.Verify(t.Context(), idp.mint(t, func(_ jwt.MapClaims, h map[string]any) { h["kid"] = "rotated" }), testClientID, testResource)
		require.ErrorIs(t, err, ErrKey)
	}
	assert.Equal(t, int32(1), idp.jwksHits.Load(), "unknown kids inside the cooldown do not refetch")
}
