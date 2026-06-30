package handlers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// testIssuer is a stable issuer URL so token verification does not depend on the
// request Host header. Configuring it on OAuth2ServerConfig makes oauth2IssuerURL
// deterministic across mint and verify.
const testIssuer = "https://bifrost.test"

// testMCPResource is the canonical /mcp resource audience derived from the issuer.
const testMCPResource = testIssuer + "/mcp"

// mockOAuth2Store is an embedded-interface ConfigStore stub for the /mcp auth
// tests. It serves a fixed signing key and a small set of virtual keys; every
// other ConfigStore method panics if a test reaches it unexpectedly.
type mockOAuth2Store struct {
	configstore.ConfigStore
	signingKey *configtables.OAuth2SigningKey
	signingErr error
	vksByID    map[string]*configtables.TableVirtualKey
	vksByValue map[string]*configtables.TableVirtualKey
	authReqs   map[string]*configtables.TableOAuth2AuthorizeRequest
	clients    map[string]*configtables.TableOAuth2Client

	sessionRows []configstore.OAuth2SessionRow
	sessionByID map[string]*configtables.TableOAuth2RefreshToken
	listErr     error
	revokeErr   error
	revokedIDs  []string
}

func (m *mockOAuth2Store) GetOAuth2AuthorizeRequestByID(_ context.Context, id string) (*configtables.TableOAuth2AuthorizeRequest, error) {
	if r, ok := m.authReqs[id]; ok {
		// Return a copy so a handler mutating the loaded struct does not mutate the
		// stored row in place — the real store loads a fresh copy from the DB.
		cp := *r
		return &cp, nil
	}
	return nil, configstore.ErrNotFound
}

func (m *mockOAuth2Store) GetOAuth2ClientByClientID(_ context.Context, clientID string) (*configtables.TableOAuth2Client, error) {
	if c, ok := m.clients[clientID]; ok {
		return c, nil
	}
	return nil, configstore.ErrNotFound
}

func (m *mockOAuth2Store) ListOAuth2Sessions(_ context.Context, _ configstore.OAuth2SessionsQueryParams) ([]configstore.OAuth2SessionRow, int64, error) {
	if m.listErr != nil {
		return nil, 0, m.listErr
	}
	return m.sessionRows, int64(len(m.sessionRows)), nil
}

func (m *mockOAuth2Store) GetOAuth2SessionByID(_ context.Context, id string) (*configtables.TableOAuth2RefreshToken, error) {
	if r, ok := m.sessionByID[id]; ok {
		return r, nil
	}
	return nil, configstore.ErrNotFound
}

func (m *mockOAuth2Store) RevokeOAuth2Session(_ context.Context, id string) error {
	if m.revokeErr != nil {
		return m.revokeErr
	}
	if _, ok := m.sessionByID[id]; !ok {
		return configstore.ErrNotFound
	}
	m.revokedIDs = append(m.revokedIDs, id)
	return nil
}

func (m *mockOAuth2Store) ConsentOAuth2AuthorizeRequest(_ context.Context, req *configtables.TableOAuth2AuthorizeRequest) error {
	existing, ok := m.authReqs[req.ID]
	if !ok || existing.Status != configtables.OAuth2AuthorizeRequestStatusPending {
		return configstore.ErrNotFound
	}
	existing.Status = configtables.OAuth2AuthorizeRequestStatusConsented
	existing.CodeHash = req.CodeHash
	existing.BfMode = req.BfMode
	existing.BfSub = req.BfSub
	return nil
}

func (m *mockOAuth2Store) GetOAuth2SigningKey(_ context.Context) (*configtables.OAuth2SigningKey, error) {
	if m.signingErr != nil {
		return nil, m.signingErr
	}
	return m.signingKey, nil
}

func (m *mockOAuth2Store) GetVirtualKey(_ context.Context, id string) (*configtables.TableVirtualKey, error) {
	if vk, ok := m.vksByID[id]; ok {
		return vk, nil
	}
	return nil, configstore.ErrNotFound
}

func (m *mockOAuth2Store) GetVirtualKeyByValue(_ context.Context, value string) (*configtables.TableVirtualKey, error) {
	if vk, ok := m.vksByValue[value]; ok {
		return vk, nil
	}
	return nil, configstore.ErrNotFound
}

// newTestSigningKey generates an RS2048 keypair and returns it both as the stored
// OAuth2SigningKey (PKCS8 PEM) and the raw private key for signing test tokens.
func newTestSigningKey(t *testing.T) (*configtables.OAuth2SigningKey, *rsa.PrivateKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	privPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	require.NoError(t, err)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	return &configtables.OAuth2SigningKey{KID: "test-kid", PrivateKeyPEM: privPEM, PublicKeyPEM: pubPEM}, priv
}

// newTestOAuth2Config builds a lib.Config wired to the given store with a stable
// issuer, for the requested /mcp auth mode and auth-enforcement setting.
func newTestOAuth2Config(store configstore.ConfigStore, authMode configtables.MCPServerAuthMode, enforceAuth bool) *lib.Config {
	return &lib.Config{
		ConfigStore: store,
		ClientConfig: &configstore.ClientConfig{
			MCPServerAuthMode:      authMode,
			EnforceAuthOnInference: enforceAuth,
			OAuth2ServerConfig: &configtables.OAuth2ServerConfig{
				IssuerURL:      schemas.NewSecretVar(testIssuer),
				AuthCodeTTL:    configtables.DefaultAuthCodeTTL,
				AccessTokenTTL: configtables.DefaultAccessTokenTTL,
			},
		},
	}
}

// mintTestToken signs a token with valid defaults (vk mode), applying mutate to
// override claims/header for negative cases. signMethod and signKey let callers
// force a non-RS256 algorithm or a wrong signing key.
func mintTestToken(t *testing.T, priv *rsa.PrivateKey, kid string, mutate func(jwt.MapClaims)) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":     testIssuer,
		"aud":     jwt.ClaimStrings{testMCPResource},
		"sub":     "vk-123",
		"bf_mode": string(schemas.MCPAuthModeVK),
		"scope":   "mcp",
		"iat":     now.Unix(),
		"nbf":     now.Unix(),
		"exp":     now.Add(10 * time.Minute).Unix(),
	}
	if mutate != nil {
		mutate(claims)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(priv)
	require.NoError(t, err)
	return signed
}

func TestExtractBearerJWT(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{name: "jwt bearer", header: "Bearer eyJhbGciOiJSUzI1NiJ9.x.y", want: "eyJhbGciOiJSUzI1NiJ9.x.y"},
		{name: "case-insensitive scheme", header: "bearer eyJabc", want: "eyJabc"},
		{name: "virtual key is not a jwt", header: "Bearer sk-bf-abc123", want: ""},
		{name: "non-bearer scheme", header: "Basic eyJabc", want: ""},
		{name: "empty header", header: "", want: ""},
		{name: "bearer without token", header: "Bearer ", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			if tc.header != "" {
				ctx.Request.Header.Set("Authorization", tc.header)
			}
			assert.Equal(t, tc.want, extractBearerJWT(ctx))
		})
	}
}

func TestVerifyMCPJWT_ValidEachMode(t *testing.T) {
	key, priv := newTestSigningKey(t)
	store := &mockOAuth2Store{signingKey: key}
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)

	modes := []struct {
		mode schemas.MCPAuthMode
		sub  string
	}{
		{schemas.MCPAuthModeUser, "user-1"},
		{schemas.MCPAuthModeVK, "vk-123"},
		{schemas.MCPAuthModeSession, "session-abc"},
	}
	for _, m := range modes {
		t.Run(string(m.mode), func(t *testing.T) {
			raw := mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
				c["bf_mode"] = string(m.mode)
				c["sub"] = m.sub
			})
			ctx := &fasthttp.RequestCtx{}
			claims, err := verifyMCPJWT(ctx, raw, cfg, key)
			require.NoError(t, err)
			require.NotNil(t, claims)
			assert.Equal(t, string(m.mode), claims.BfMode)
			assert.Equal(t, m.sub, claims.Subject)
		})
	}
}

func TestVerifyMCPJWT_Rejections(t *testing.T) {
	key, priv := newTestSigningKey(t)
	_, otherPriv := newTestSigningKey(t)
	store := &mockOAuth2Store{signingKey: key}
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)

	cases := []struct {
		name string
		// raw builds the token string under test.
		raw func() string
	}{
		{
			name: "expired token",
			raw: func() string {
				return mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
					c["exp"] = time.Now().Add(-time.Minute).Unix()
				})
			},
		},
		{
			name: "nbf in the future",
			raw: func() string {
				return mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
					c["nbf"] = time.Now().Add(time.Hour).Unix()
				})
			},
		},
		{
			name: "missing exp",
			raw: func() string {
				return mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
					delete(c, "exp")
				})
			},
		},
		{
			name: "missing iat",
			raw: func() string {
				return mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
					delete(c, "iat")
				})
			},
		},
		{
			name: "issuer mismatch",
			raw: func() string {
				return mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
					c["iss"] = "https://evil.example"
				})
			},
		},
		{
			name: "audience mismatch",
			raw: func() string {
				return mintTestToken(t, priv, key.KID, func(c jwt.MapClaims) {
					c["aud"] = jwt.ClaimStrings{"https://bifrost.test/other"}
				})
			},
		},
		{
			name: "unknown kid",
			raw: func() string {
				return mintTestToken(t, priv, "wrong-kid", nil)
			},
		},
		{
			name: "wrong signing key",
			raw: func() string {
				return mintTestToken(t, otherPriv, key.KID, nil)
			},
		},
		{
			name: "non-RS256 algorithm (HS256)",
			raw: func() string {
				tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
					"iss": testIssuer, "aud": jwt.ClaimStrings{testMCPResource},
					"sub": "vk-123", "bf_mode": "vk", "iat": time.Now().Unix(),
					"nbf": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
				})
				tok.Header["kid"] = key.KID
				signed, err := tok.SignedString([]byte("hs-secret"))
				require.NoError(t, err)
				return signed
			},
		},
		{
			name: "RS-family but not RS256 (RS384)",
			raw: func() string {
				tok := jwt.NewWithClaims(jwt.SigningMethodRS384, jwt.MapClaims{
					"iss": testIssuer, "aud": jwt.ClaimStrings{testMCPResource},
					"sub": "vk-123", "bf_mode": "vk", "iat": time.Now().Unix(),
					"nbf": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
				})
				tok.Header["kid"] = key.KID
				signed, err := tok.SignedString(priv)
				require.NoError(t, err)
				return signed
			},
		},
		{
			name: "alg none",
			raw: func() string {
				header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT","kid":"test-kid"}`))
				payload := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"https://bifrost.test","aud":["https://bifrost.test/mcp"],"sub":"vk-123","bf_mode":"vk","iat":1700000000,"nbf":1700000000,"exp":9999999999}`))
				return header + "." + payload + "."
			},
		},
		{
			name: "malformed garbage",
			raw: func() string {
				return "eyJ.not-a-valid.token"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			claims, err := verifyMCPJWT(ctx, tc.raw(), cfg, key)
			require.Error(t, err)
			assert.Nil(t, claims)
		})
	}
}

// TestVerifyMCPJWT_NilSigningKeyNotLabeledInvalidToken pins that a nil signing
// key — the caller's signal that loading the key failed (no config store,
// missing key) — surfaces as a config fault, never as the caller's token being
// invalid.
func TestVerifyMCPJWT_NilSigningKeyNotLabeledInvalidToken(t *testing.T) {
	key, priv := newTestSigningKey(t)
	raw := mintTestToken(t, priv, key.KID, nil)
	cfg := newTestOAuth2Config(&mockOAuth2Store{signingKey: key}, configtables.MCPServerAuthModeBoth, false)

	ctx := &fasthttp.RequestCtx{}
	_, err := verifyMCPJWT(ctx, raw, cfg, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signing key unavailable")
	assert.NotContains(t, err.Error(), "invalid token")
}

// TestCachedSigningKey_ConfigFaults pins that the handler's key loader — which
// now owns reading the signing key for the JWT verify path — surfaces
// infrastructure faults distinctly, never as the caller's token being invalid.
func TestCachedSigningKey_ConfigFaults(t *testing.T) {
	t.Run("nil config store", func(t *testing.T) {
		cfg := newTestOAuth2Config(nil, configtables.MCPServerAuthModeBoth, false)
		cfg.ConfigStore = nil
		h := &MCPServerHandler{config: cfg}
		_, err := h.config.GetOAuth2SigningKey(bgCtx())
		require.Error(t, err)
		assert.Equal(t, "config store unavailable", err.Error())
		assert.NotContains(t, err.Error(), "invalid token")
	})

	t.Run("signing key load error", func(t *testing.T) {
		store := &mockOAuth2Store{signingErr: configstore.ErrNotFound}
		cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)
		h := &MCPServerHandler{config: cfg}
		_, err := h.config.GetOAuth2SigningKey(bgCtx())
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "invalid token")
	})
}

func TestInjectJWTContext(t *testing.T) {
	activeVK := &configtables.TableVirtualKey{ID: "vk-row-1", Value: "sk-bf-active"}

	t.Run("user mode sets user id", func(t *testing.T) {
		bc := schemas.NewBifrostContext(context.Background(), time.Time{})
		err := injectJWTContext(bc, &jwtMCPClaims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1"}, BfMode: "user",
		}, nil)
		require.NoError(t, err)
		assert.Equal(t, "user-1", bc.Value(schemas.BifrostContextKeyUserID))
	})

	t.Run("vk mode sets the raw vk value and lets governance derive the id", func(t *testing.T) {
		bc := schemas.NewBifrostContext(context.Background(), time.Time{})
		err := injectJWTContext(bc, &jwtMCPClaims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "vk-row-1"}, BfMode: "vk",
		}, activeVK)
		require.NoError(t, err)
		assert.Equal(t, "sk-bf-active", bc.Value(schemas.BifrostContextKeyVirtualKey))
		// The VK row ID is resolved later by governance's PreMCPConnectionHook from
		// the value, not stamped here — mirrors the x-bf-vk header path.
		assert.Nil(t, bc.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID))
	})

	t.Run("vk mode without vk errors", func(t *testing.T) {
		bc := schemas.NewBifrostContext(context.Background(), time.Time{})
		err := injectJWTContext(bc, &jwtMCPClaims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "vk-row-1"}, BfMode: "vk",
		}, nil)
		require.Error(t, err)
	})

	t.Run("session mode sets session id", func(t *testing.T) {
		bc := schemas.NewBifrostContext(context.Background(), time.Time{})
		err := injectJWTContext(bc, &jwtMCPClaims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "session-abc"}, BfMode: "session",
		}, nil)
		require.NoError(t, err)
		assert.Equal(t, "session-abc", bc.Value(schemas.BifrostContextKeyMCPSessionID))
	})

	t.Run("missing sub errors", func(t *testing.T) {
		bc := schemas.NewBifrostContext(context.Background(), time.Time{})
		err := injectJWTContext(bc, &jwtMCPClaims{BfMode: "user"}, nil)
		require.Error(t, err)
	})

	t.Run("unknown bf_mode errors", func(t *testing.T) {
		bc := schemas.NewBifrostContext(context.Background(), time.Time{})
		err := injectJWTContext(bc, &jwtMCPClaims{
			RegisteredClaims: jwt.RegisteredClaims{Subject: "x"}, BfMode: "bogus",
		}, nil)
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "bf_mode"))
	})
}
