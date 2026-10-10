package tokencache

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rsaPEM(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func ecPEM(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

// parseAssertion verifies the posted assertion with the public half of key and returns its claims.
func parseAssertion(t *testing.T, assertion string, pub any) (jwt.MapClaims, map[string]any) {
	t.Helper()
	claims := jwt.MapClaims{}
	tok, err := jwt.ParseWithClaims(assertion, claims, func(*jwt.Token) (any, error) { return pub, nil },
		jwt.WithoutClaimsValidation())
	require.NoError(t, err)
	return claims, tok.Header
}

func TestJWTBearerMinter(t *testing.T) {
	t.Run("posts a signed RS256 assertion with the RFC 7523 claims", func(t *testing.T) {
		key, pemKey := rsaPEM(t)
		ts := newTokenServer(t)
		mint := JWTBearerMinter(JWTBearerConfig{
			TokenURL:      ts.URL,
			PrivateKeyPEM: pemKey,
			Issuer:        "client-123",
			Audience:      "https://idp.example/oauth2/token",
			KeyID:         "kid-1",
			Scopes:        []string{"read", "write"},
			ExtraParams:   url.Values{"resource": {"api://x"}},
		}, authClient(t))

		before := time.Now()
		e, bErr := mint(context.Background(), nil)
		require.Nil(t, bErr)
		assert.Equal(t, "minted", e.Value)
		assert.WithinDuration(t, time.Now().Add(time.Hour), e.ExpiresAt, 5*time.Second)

		_, form, _, _, _ := ts.snapshot()
		assert.Equal(t, jwtBearerGrantType, form.Get("grant_type"))
		assert.Equal(t, "read write", form.Get("scope"))
		assert.Equal(t, "api://x", form.Get("resource"))
		claims, header := parseAssertion(t, form.Get("assertion"), &key.PublicKey)
		assert.Equal(t, "RS256", header["alg"])
		assert.Equal(t, "kid-1", header["kid"])
		assert.Equal(t, "client-123", claims["iss"])
		assert.Equal(t, "client-123", claims["sub"], "sub defaults to iss")
		assert.Equal(t, "https://idp.example/oauth2/token", claims["aud"])
		assert.NotEmpty(t, claims["jti"])
		iat := time.Unix(int64(claims["iat"].(float64)), 0)
		exp := time.Unix(int64(claims["exp"].(float64)), 0)
		nbf := time.Unix(int64(claims["nbf"].(float64)), 0)
		assert.True(t, iat.Before(before.Add(time.Second)), "iat is backdated for clock skew")
		assert.Equal(t, iat, nbf)
		assert.WithinDuration(t, before.Add(DefaultAssertionLifetime), exp, 5*time.Second)
	})

	t.Run("honours subject, algorithm and lifetime", func(t *testing.T) {
		key, pemKey := rsaPEM(t)
		ts := newTokenServer(t)
		mint := JWTBearerMinter(JWTBearerConfig{
			TokenURL: ts.URL, PrivateKeyPEM: pemKey, Issuer: "iss", Subject: "user@example", Audience: "aud",
			Algorithm: "PS384", Lifetime: 90 * time.Second,
		}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.Nil(t, bErr)
		_, form, _, _, _ := ts.snapshot()
		claims, header := parseAssertion(t, form.Get("assertion"), &key.PublicKey)
		assert.Equal(t, "PS384", header["alg"])
		assert.Nil(t, header["kid"])
		assert.Equal(t, "user@example", claims["sub"])
		iat := int64(claims["iat"].(float64))
		exp := int64(claims["exp"].(float64))
		assert.Equal(t, int64(90+assertionIssuedAtSkew/time.Second), exp-iat)
	})

	t.Run("signs with an EC key for ES256", func(t *testing.T) {
		key, pemKey := ecPEM(t)
		ts := newTokenServer(t)
		mint := JWTBearerMinter(JWTBearerConfig{TokenURL: ts.URL, PrivateKeyPEM: pemKey, Issuer: "iss", Audience: "aud", Algorithm: "ES256"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.Nil(t, bErr)
		_, form, _, _, _ := ts.snapshot()
		_, header := parseAssertion(t, form.Get("assertion"), &key.PublicKey)
		assert.Equal(t, "ES256", header["alg"])
	})

	t.Run("accepts a PEM whose newlines survived as literal backslash-n", func(t *testing.T) {
		_, pemKey := rsaPEM(t)
		mangled := ""
		for i, r := range pemKey {
			if r == '\n' && i != len(pemKey)-1 {
				mangled += `\n`
				continue
			}
			mangled += string(r)
		}
		ts := newTokenServer(t)
		mint := JWTBearerMinter(JWTBearerConfig{TokenURL: ts.URL, PrivateKeyPEM: mangled, Issuer: "iss", Audience: "aud"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		assert.Nil(t, bErr)
	})

	t.Run("a bad key is a configuration fault that blocks fallbacks and never reaches the network", func(t *testing.T) {
		ts := newTokenServer(t)
		mint := JWTBearerMinter(JWTBearerConfig{TokenURL: ts.URL, PrivateKeyPEM: "not a key", Issuer: "iss", Audience: "aud"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "PKCS#1 or PKCS#8")
		require.NotNil(t, bErr.AllowFallbacks)
		assert.False(t, *bErr.AllowFallbacks)
		hits, _, _, _, _ := ts.snapshot()
		assert.Equal(t, 0, hits)
	})

	t.Run("an RSA key with an EC algorithm is rejected", func(t *testing.T) {
		_, pemKey := rsaPEM(t)
		mint := JWTBearerMinter(JWTBearerConfig{TokenURL: "https://x/token", PrivateKeyPEM: pemKey, Issuer: "iss", Audience: "aud", Algorithm: "ES256"}, nil)
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "EC PEM for ES256")
	})

	t.Run("an unknown algorithm is rejected", func(t *testing.T) {
		_, pemKey := rsaPEM(t)
		mint := JWTBearerMinter(JWTBearerConfig{TokenURL: "https://x/token", PrivateKeyPEM: pemKey, Issuer: "iss", Audience: "aud", Algorithm: "HS256"}, nil)
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "not supported")
	})

	t.Run("an endpoint rejection carries status and the standard error fields only", func(t *testing.T) {
		_, pemKey := rsaPEM(t)
		ts := newTokenServer(t)
		ts.set(http.StatusBadRequest, `{"error":"invalid_grant","error_description":"assertion expired","debug":"secret"}`, nil)
		mint := JWTBearerMinter(JWTBearerConfig{TokenURL: ts.URL, PrivateKeyPEM: pemKey, Issuer: "iss", Audience: "aud"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Equal(t, http.StatusBadRequest, *bErr.StatusCode)
		assert.Contains(t, bErr.Error.Message, "invalid_grant assertion expired")
		assert.NotContains(t, bErr.Error.Message, "debug")
		require.NotNil(t, bErr.AllowFallbacks)
		assert.False(t, *bErr.AllowFallbacks)
		assert.True(t, IsPermanentError(bErr))
	})

	t.Run("a rate limit carries the retry hint and allows fallbacks", func(t *testing.T) {
		_, pemKey := rsaPEM(t)
		ts := newTokenServer(t)
		ts.set(http.StatusTooManyRequests, `{}`, http.Header{"Retry-After": {"3"}})
		mint := JWTBearerMinter(JWTBearerConfig{TokenURL: ts.URL, PrivateKeyPEM: pemKey, Issuer: "iss", Audience: "aud"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Equal(t, int64(3000), bErr.ExtraFields.RetryAfter)
		assert.False(t, IsPermanentError(bErr))
		assert.Nil(t, bErr.AllowFallbacks, "a temporary token-endpoint outage must not stop a healthy fallback provider")
	})

	t.Run("a server error at the token endpoint allows fallbacks", func(t *testing.T) {
		_, pemKey := rsaPEM(t)
		ts := newTokenServer(t)
		ts.set(http.StatusBadGateway, `{"error":"upstream"}`, nil)
		mint := JWTBearerMinter(JWTBearerConfig{TokenURL: ts.URL, PrivateKeyPEM: pemKey, Issuer: "iss", Audience: "aud"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.False(t, IsPermanentError(bErr))
		assert.Nil(t, bErr.AllowFallbacks)
	})

	t.Run("an unreachable endpoint never leaks the token url query into the wrapped error", func(t *testing.T) {
		_, pemKey := rsaPEM(t)
		mint := JWTBearerMinter(JWTBearerConfig{TokenURL: "http://127.0.0.1:1/token?api_key=leaked-value", PrivateKeyPEM: pemKey, Issuer: "iss", Audience: "aud"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		require.NotNil(t, bErr.Error.Error)
		assert.NotContains(t, bErr.Error.Error.Error(), "leaked-value")
		assert.NotContains(t, bErr.Error.Message, "leaked-value")
	})

	t.Run("a redirect from https to plain http never carries the assertion", func(t *testing.T) {
		// A 307/308 replays the POST form, and the form is the signed assertion.
		_, pemKey := rsaPEM(t)
		plain := newTokenServer(t)
		tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plain.URL+"/token", http.StatusTemporaryRedirect)
		}))
		t.Cleanup(tls.Close)

		mint := JWTBearerMinter(JWTBearerConfig{TokenURL: tls.URL + "/token", PrivateKeyPEM: pemKey, Issuer: "iss", Audience: "aud"}, tls.Client())
		_, bErr := mint(context.Background(), nil)
		hits, form, _, _, _ := plain.snapshot()
		assert.Equal(t, 0, hits, "the cleartext endpoint must never be reached; it saw an assertion: %v", form.Get("assertion") != "")
		require.NotNil(t, bErr)
		require.NotNil(t, bErr.Error.Error)
		assert.Contains(t, bErr.Error.Error.Error(), "redirect from https to http refused")
		assert.False(t, IsPermanentError(bErr))
	})

	t.Run("a nil client gets a bounded fallback client, never http.DefaultClient", func(t *testing.T) {
		_, pemKey := rsaPEM(t)
		ts := newTokenServer(t)
		mint := JWTBearerMinter(JWTBearerConfig{TokenURL: ts.URL, PrivateKeyPEM: pemKey, Issuer: "iss", Audience: "aud"}, nil)
		e, bErr := mint(context.Background(), nil)
		require.Nil(t, bErr)
		assert.Equal(t, "minted", e.Value)
		assert.Equal(t, DefaultExchangeTimeout, exchangeClient(nil).Timeout, "a stalled endpoint must not hold the mint forever")
		assert.NotSame(t, http.DefaultClient, exchangeClient(nil))
		custom := &http.Client{}
		assert.Same(t, custom, exchangeClient(custom))
		// The minters compose the two: the fallback keeps its timeout and gains the redirect policy.
		composed := guardRedirects(exchangeClient(nil))
		assert.Equal(t, DefaultExchangeTimeout, composed.Timeout)
		assert.NotNil(t, composed.CheckRedirect)
	})

	t.Run("a missing expires_in falls back to a short lifetime", func(t *testing.T) {
		_, pemKey := rsaPEM(t)
		ts := newTokenServer(t)
		ts.set(http.StatusOK, `{"access_token":"minted","token_type":"Bearer"}`, nil)
		mint := JWTBearerMinter(JWTBearerConfig{TokenURL: ts.URL, PrivateKeyPEM: pemKey, Issuer: "iss", Audience: "aud"}, authClient(t))
		e, bErr := mint(context.Background(), nil)
		require.Nil(t, bErr)
		assert.WithinDuration(t, time.Now().Add(fallbackTokenLifetime), e.ExpiresAt, 5*time.Second)
	})

	t.Run("an empty access_token is rejected", func(t *testing.T) {
		_, pemKey := rsaPEM(t)
		ts := newTokenServer(t)
		ts.set(http.StatusOK, `{"token_type":"Bearer","expires_in":60}`, nil)
		mint := JWTBearerMinter(JWTBearerConfig{TokenURL: ts.URL, PrivateKeyPEM: pemKey, Issuer: "iss", Audience: "aud"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "returned no access_token")
		require.NotNil(t, bErr.StatusCode)
		assert.Equal(t, http.StatusBadGateway, *bErr.StatusCode, "a failed mint must not surface as the endpoint's 200")
	})

	t.Run("an unreachable endpoint is transient", func(t *testing.T) {
		_, pemKey := rsaPEM(t)
		mint := JWTBearerMinter(JWTBearerConfig{TokenURL: "http://127.0.0.1:1/token", PrivateKeyPEM: pemKey, Issuer: "iss", Audience: "aud"}, authClient(t))
		_, bErr := mint(context.Background(), nil)
		require.NotNil(t, bErr)
		assert.Equal(t, 0, *bErr.StatusCode)
		assert.False(t, IsPermanentError(bErr))
	})
}
