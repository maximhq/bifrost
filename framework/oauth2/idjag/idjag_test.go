package idjag

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
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
	jwksDown  atomic.Bool // answers the JWKS route with 503
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
		if idp.jwksDown.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
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
		{"key-bound (cnf) without proof of possession", func() string {
			return idp.mint(t, func(c jwt.MapClaims, _ map[string]any) { c["cnf"] = map[string]any{"jkt": "abc"} })
		}, testClientID, ErrProofRequired},
		{"non-string scope", func() string {
			return idp.mint(t, func(c jwt.MapClaims, _ map[string]any) { c["scope"] = []string{"mcp"} })
		}, testClientID, ErrMalformed},
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

// TestVerify_KeysCachedAndUnknownKidRefetches checks a known kid reuses the
// cached set, and an unknown one refetches so a rotated key is picked up at once.
func TestVerify_KeysCachedAndUnknownKidRefetches(t *testing.T) {
	idp := newTestIdP(t, true)
	v := idp.verifier()
	for range 3 {
		_, err := v.Verify(t.Context(), idp.mint(t, nil), testClientID, testResource)
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), idp.jwksHits.Load(), "valid kids reuse the cached set")

	idp.kid = "rotated"
	_, err := v.Verify(t.Context(), idp.mint(t, nil), testClientID, testResource)
	require.NoError(t, err, "a rotated kid is fetched without waiting")
	assert.Equal(t, int32(2), idp.jwksHits.Load())
}

// TestVerify_RecoversOnceTheIdPIsBack checks a failed fetch is not remembered:
// the next request after the IdP recovers verifies.
func TestVerify_RecoversOnceTheIdPIsBack(t *testing.T) {
	idp := newTestIdP(t, true)
	idp.jwksDown.Store(true)
	v := idp.verifier()
	_, err := v.Verify(t.Context(), idp.mint(t, nil), testClientID, testResource)
	require.ErrorIs(t, err, ErrKey)
	idp.jwksDown.Store(false)
	_, err = v.Verify(t.Context(), idp.mint(t, nil), testClientID, testResource)
	require.NoError(t, err)
}

// TestVerify_StaleKeyUsedWhileTheIdPIsDown checks a key from an expired set
// still verifies when the refresh fails.
func TestVerify_StaleKeyUsedWhileTheIdPIsDown(t *testing.T) {
	idp := newTestIdP(t, true)
	v := idp.verifier()
	_, err := v.Verify(t.Context(), idp.mint(t, nil), testClientID, testResource)
	require.NoError(t, err)
	v.keys.mu.Lock()
	v.keys.fetchedAt = time.Now().Add(-2 * keyTTL)
	v.keys.mu.Unlock()
	idp.jwksDown.Store(true)
	_, err = v.Verify(t.Context(), idp.mint(t, nil), testClientID, testResource)
	require.NoError(t, err)
	assert.Equal(t, int32(2), idp.jwksHits.Load(), "the stale set was refreshed, and the refresh failed")
}

// TestVerify_CancelledCallerDoesNotAffectOthers checks a caller's cancellation
// fails only its own fetch.
func TestVerify_CancelledCallerDoesNotAffectOthers(t *testing.T) {
	idp := newTestIdP(t, true)
	v := idp.verifier()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := v.Verify(cancelled, idp.mint(t, nil), testClientID, testResource)
	require.ErrorIs(t, err, ErrKey, "the cancelled caller's own fetch fails")
	_, err = v.Verify(t.Context(), idp.mint(t, nil), testClientID, testResource)
	require.NoError(t, err, "the next caller fetches for itself")
}

// TestVerify_HTTPSIssuerNeverFetchesKeysOverHTTP checks an https issuer's keys
// are never fetched over plain http, whether discovery names an http jwks_uri or
// the https JWKS redirects to http, so no one on the path can substitute them.
func TestVerify_HTTPSIssuerNeverFetchesKeysOverHTTP(t *testing.T) {
	for name, redirect := range map[string]bool{"http jwks_uri": false, "redirect to http": true} {
		t.Run(name, func(t *testing.T) {
			cleartext := newTestIdP(t, true) // serves the real keys over plain http
			mux := http.NewServeMux()
			var tlsURL string
			mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
				jwksURI := cleartext.srv.URL + "/keys"
				if redirect {
					jwksURI = tlsURL + "/keys"
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"issuer": tlsURL, "jwks_uri": jwksURI})
			})
			mux.HandleFunc("GET /keys", func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, cleartext.srv.URL+"/keys", http.StatusFound)
			})
			srv := httptest.NewTLSServer(mux)
			t.Cleanup(srv.Close)
			tlsURL = srv.URL

			v := NewVerifier(Config{TrustedIssuer: tlsURL, ExpectedAudience: testAudience, HTTPClient: srv.Client})
			_, err := v.Verify(t.Context(), cleartext.mint(t, func(c jwt.MapClaims, _ map[string]any) { c["iss"] = tlsURL }), testClientID, testResource)
			require.ErrorIs(t, err, ErrKey)
			assert.Equal(t, int32(0), cleartext.jwksHits.Load(), "no key request went out over http")
		})
	}
}

// TestJWKPublicKey_EC checks EC keys parse through the stdlib point decoder:
// a valid P-256 key round-trips, a coordinate with its leading zero dropped is
// refused, and a point off the curve is refused.
func TestJWKPublicKey_EC(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	raw, err := key.PublicKey.Bytes() // 0x04 || X || Y
	require.NoError(t, err)
	x, y := raw[1:33], raw[33:]
	enc := base64.RawURLEncoding.EncodeToString

	pub, err := jwk{Kty: "EC", Crv: "P-256", X: enc(x), Y: enc(y)}.publicKey()
	require.NoError(t, err)
	assert.True(t, pub.(*ecdsa.PublicKey).Equal(&key.PublicKey))

	_, err = jwk{Kty: "EC", Crv: "P-256", X: enc(x[1:]), Y: enc(y)}.publicKey()
	assert.Error(t, err, "a short coordinate is refused")

	offCurve := append([]byte(nil), y...)
	offCurve[len(offCurve)-1] ^= 1
	_, err = jwk{Kty: "EC", Crv: "P-256", X: enc(x), Y: enc(offCurve)}.publicKey()
	assert.Error(t, err, "a point off the curve is refused")
}
