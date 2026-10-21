package handlers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/oauth2"
	"github.com/maximhq/bifrost/framework/oauth2/idjag"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// fakeIdP is a minimal enterprise IdP: OIDC discovery plus one RSA signing key.
type fakeIdP struct {
	srv *httptest.Server
	key *rsa.PrivateKey
}

// newFakeIdP starts an IdP whose issuer is its own httptest URL.
func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	idp := &fakeIdP{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": idp.srv.URL, "jwks_uri": idp.srv.URL + "/keys"})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "idp-1", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

// mint signs an ID-JAG for clientID addressed to the test issuer.
func (idp *fakeIdP) mint(t *testing.T, clientID string, mutate func(jwt.MapClaims)) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss": idp.srv.URL, "sub": "00u-alice", "aud": testIssuer, "client_id": clientID,
		"jti": "jti-1", "iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(),
		"resource": testMCPResource, "email": "alice@example.com",
	}
	if mutate != nil {
		mutate(claims)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["typ"] = idjag.TokenType
	tok.Header["kid"] = "idp-1"
	signed, err := tok.SignedString(idp.key)
	require.NoError(t, err)
	return signed
}

// managedAuthFixture is an issuance handler with managed auth on, one active VK and an IdP.
type managedAuthFixture struct {
	h     *OAuth2IssuanceHandler
	store configstore.ConfigStore
	cfg   *lib.Config
	idp   *fakeIdP
	vkID  string
}

// newManagedAuthFixture wires a real sqlite store, managed-auth settings and the fake IdP.
func newManagedAuthFixture(t *testing.T, resolver OAuth2IdentityResolver) *managedAuthFixture {
	t.Helper()
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	idp := newFakeIdP(t)
	active := true
	vk := &configtables.TableVirtualKey{ID: "vk-xaa", Name: "xaa", Value: *schemas.NewSecretVar("sk-bf-xaa"), IsActive: &active}
	require.NoError(t, store.CreateVirtualKey(context.Background(), vk))
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)
	cfg.ClientConfig.OAuth2ServerConfig.ManagedAuth = &configtables.ManagedAuthConfig{Enabled: true, TrustedIssuer: idp.srv.URL}
	return &managedAuthFixture{h: NewOAuth2IssuanceHandler(cfg, nil, resolver), store: store, cfg: cfg, idp: idp, vkID: vk.ID}
}

// seedManagedClient stores a managed client; a non-empty secret makes it confidential.
func (f *managedAuthFixture) seedManagedClient(t *testing.T, clientID, secret, identity string) *configtables.TableOAuth2ManagedClient {
	t.Helper()
	c := &configtables.TableOAuth2ManagedClient{
		ID: "row-" + clientID, ClientID: clientID, Identity: identity, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if identity == configtables.ManagedClientIdentityVK {
		c.VirtualKeyID = f.vkID
	}
	if secret != "" {
		c.ClientSecretHash = hashSHA256Hex(secret)
	}
	require.NoError(t, f.store.CreateOAuth2ManagedClient(context.Background(), c))
	return c
}

// tokenRequest posts a jwt-bearer grant; basicID/basicSecret switch to Basic auth.
func (f *managedAuthFixture) tokenRequest(form url.Values, basicID, basicSecret string) (*fasthttp.RequestCtx, map[string]any) {
	form.Set("grant_type", idjag.GrantTypeJWTBearer)
	ctx := formPostCtx(form.Encode())
	if basicID != "" {
		creds := url.QueryEscape(basicID) + ":" + url.QueryEscape(basicSecret)
		ctx.Request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(creds)))
	}
	f.h.handleToken(ctx)
	var body map[string]any
	_ = json.Unmarshal(ctx.Response.Body(), &body)
	return ctx, body
}

func TestManagedAuth_DisabledIsUnsupportedGrant(t *testing.T) {
	f := newManagedAuthFixture(t, nil)
	f.cfg.ClientConfig.OAuth2ServerConfig.ManagedAuth.Enabled = false
	ctx, body := f.tokenRequest(url.Values{"assertion": {"x"}, "client_id": {"c"}}, "", "")
	assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	assert.Equal(t, "unsupported_grant_type", body["error"])
}

func TestManagedAuth_ConfidentialVKClientGetsAccessOnlyToken(t *testing.T) {
	f := newManagedAuthFixture(t, nil)
	client := f.seedManagedClient(t, "claude-web", "s3cret", configtables.ManagedClientIdentityVK)
	ctx, body := f.tokenRequest(url.Values{
		"assertion": {f.idp.mint(t, "claude-web", nil)}, "resource": {testMCPResource}, "scope": {"openid profile"},
	}, "claude-web", "s3cret")
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	assert.NotContains(t, body, "refresh_token")
	assert.Equal(t, float64(configtables.DefaultManagedAuthAccessTokenTTL), body["expires_in"])
	assert.Equal(t, "mcp", body["scope"], "the request's scope parameter is not echoed; only mcp is issued")

	key, err := f.cfg.GetOAuth2SigningKey(bgCtx())
	require.NoError(t, err)
	claims, err := verifyMCPJWT(bgCtx(), body["access_token"].(string), f.cfg, key)
	require.NoError(t, err, "the minted token passes the /mcp verifier")
	assert.Equal(t, string(schemas.MCPAuthModeVK), claims.BfMode)
	assert.Equal(t, f.vkID, claims.Subject)

	stored, err := f.store.GetOAuth2ManagedClientByID(context.Background(), client.ID)
	require.NoError(t, err)
	assert.NotNil(t, stored.LastUsedAt)
}

// setScopes stores the scopes managed client id may be granted.
func (f *managedAuthFixture) setScopes(t *testing.T, id, scopes string) {
	t.Helper()
	require.NoError(t, f.store.UpdateOAuth2ManagedClient(context.Background(), id, configtables.OAuth2ManagedClientPatch{Scopes: &scopes}))
}

// TestManagedAuth_ScopeFromAssertion checks the IdP's scope decision is applied
// within the client's allowed scopes: no claim grants everything the client
// allows, a claim grants what it names that the client allows (other scopes are
// ignored), and nothing granted is invalid_scope. The audience follows the
// granted scopes, so an llm-only token cannot reach /mcp.
func TestManagedAuth_ScopeFromAssertion(t *testing.T) {
	for name, tc := range map[string]struct {
		clientScopes string // "" keeps the default, mcp
		scope        any    // nil leaves the claim out
		wantStatus   int
		wantScope    string
		wantError    string
	}{
		"no claim":                       {"", nil, fasthttp.StatusOK, "mcp", ""},
		"mcp":                            {"", "mcp", fasthttp.StatusOK, "mcp", ""},
		"mcp among others":               {"", "openid mcp chat.read", fasthttp.StatusOK, "mcp", ""},
		"only other scopes":              {"", "chat.read chat.write", fasthttp.StatusBadRequest, "", "invalid_scope"},
		"empty string claim":             {"", "", fasthttp.StatusOK, "mcp", ""},
		"llm not allowed for the client": {"", "llm", fasthttp.StatusBadRequest, "", "invalid_scope"},
		"llm allowed, no claim":          {"mcp llm", nil, fasthttp.StatusOK, "mcp llm", ""},
		"llm allowed, both claimed":      {"mcp llm", "llm mcp", fasthttp.StatusOK, "mcp llm", ""},
		"llm allowed, mcp claimed":       {"mcp llm", "mcp", fasthttp.StatusOK, "mcp", ""},
		"llm allowed, llm claimed":       {"mcp llm", "llm", fasthttp.StatusOK, "llm", ""},
		"llm-only client, mcp claimed":   {"llm", "mcp", fasthttp.StatusBadRequest, "", "invalid_scope"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newManagedAuthFixture(t, nil)
			client := f.seedManagedClient(t, "pub", "", configtables.ManagedClientIdentityVK)
			if tc.clientScopes != "" {
				f.setScopes(t, client.ID, tc.clientScopes)
			}
			assertion := f.idp.mint(t, "pub", func(c jwt.MapClaims) {
				if tc.scope != nil {
					c["scope"] = tc.scope
				}
			})
			ctx, body := f.tokenRequest(url.Values{"assertion": {assertion}, "client_id": {"pub"}}, "", "")
			require.Equal(t, tc.wantStatus, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			if tc.wantError != "" {
				assert.Equal(t, tc.wantError, body["error"])
				return
			}
			assert.Equal(t, tc.wantScope, body["scope"])
			parsed, _, err := jwt.NewParser().ParseUnverified(body["access_token"].(string), jwt.MapClaims{})
			require.NoError(t, err)
			aud, err := parsed.Claims.GetAudience()
			require.NoError(t, err)
			var wantAud []string
			for _, s := range strings.Fields(tc.wantScope) {
				wantAud = append(wantAud, map[string]string{"mcp": testMCPResource, "llm": testIssuer}[s])
			}
			assert.Equal(t, wantAud, []string(aud), "one audience per granted scope")
		})
	}
}

// TestManagedAuth_LLMScopeToken checks VerifyManagedLLMToken accepts only a
// managed-auth token granted llm, keeps /mcp on the mcp scope, and refuses the
// token once the client loses llm, is disabled, or managed auth is turned off.
func TestManagedAuth_LLMScopeToken(t *testing.T) {
	issue := func(t *testing.T, f *managedAuthFixture, claim any) string {
		t.Helper()
		ctx, body := f.tokenRequest(url.Values{"assertion": {f.idp.mint(t, "pub", func(c jwt.MapClaims) {
			if claim != nil {
				c["scope"] = claim
			}
		})}, "client_id": {"pub"}}, "", "")
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		return body["access_token"].(string)
	}
	bearerCtx := func(token string) *fasthttp.RequestCtx {
		ctx := initCtx(&fasthttp.Request{})
		ctx.Request.Header.Set("Authorization", "Bearer "+token)
		return ctx
	}
	newHandler := func(f *managedAuthFixture, resolver OAuth2IdentityResolver) (*MCPServerHandler, *ManagedClientStatusCache) {
		cache := NewManagedClientStatusCache(f.cfg)
		h := newTestMCPHandler(f.cfg)
		h.identityResolver = resolver
		h.SetManagedClientStatusCache(cache)
		return h, cache
	}

	t.Run("vk client", func(t *testing.T) {
		f := newManagedAuthFixture(t, nil)
		client := f.seedManagedClient(t, "pub", "", configtables.ManagedClientIdentityVK)
		f.setScopes(t, client.ID, "mcp llm")
		h, cache := newHandler(f, nil)
		both, llmOnly, mcpOnly := issue(t, f, nil), issue(t, f, "llm"), issue(t, f, "mcp")

		p, err := h.VerifyManagedLLMToken(bearerCtx(both), both)
		require.NoError(t, err)
		assert.Equal(t, schemas.MCPAuthModeVK, p.Mode)
		assert.Equal(t, "sk-bf-xaa", p.VirtualKey, "the principal carries the key value governance resolves")
		assert.Equal(t, client.ID, p.ManagedClientID)
		_, err = h.VerifyManagedLLMToken(bearerCtx(llmOnly), llmOnly)
		require.NoError(t, err)
		_, err = h.VerifyManagedLLMToken(bearerCtx(mcpOnly), mcpOnly)
		require.Error(t, err, "an mcp-only token is not an inference credential")

		authMCP := func(token string) error {
			return h.authenticate(bearerCtx(token), schemas.NewBifrostContext(context.Background(), schemas.NoDeadline))
		}
		require.NoError(t, authMCP(both), "a token granted both still reaches /mcp")
		require.Error(t, authMCP(llmOnly), "an llm-only token does not reach /mcp")

		f.setScopes(t, client.ID, "mcp")
		cache.Evict(client.ID)
		_, err = h.VerifyManagedLLMToken(bearerCtx(both), both)
		require.Error(t, err, "removing llm from the client revokes its inference tokens")
		require.NoError(t, authMCP(both), "while its /mcp access stands")

		disabled := true
		require.NoError(t, f.store.UpdateOAuth2ManagedClient(context.Background(), client.ID, configtables.OAuth2ManagedClientPatch{Disabled: &disabled}))
		f.setScopes(t, client.ID, "mcp llm")
		cache.Evict(client.ID)
		_, err = h.VerifyManagedLLMToken(bearerCtx(both), both)
		require.Error(t, err, "a disabled client's tokens are refused")
	})

	t.Run("user client", func(t *testing.T) {
		r := &managedAuthResolver{fakeResolver: fakeResolver{userModeAvailable: true}, resolvedUserID: "user-alice"}
		f := newManagedAuthFixture(t, r)
		client := f.seedManagedClient(t, "pub", "", configtables.ManagedClientIdentityUser)
		f.setScopes(t, client.ID, "mcp llm")
		token := issue(t, f, "mcp llm")
		h, _ := newHandler(f, r)
		p, err := h.VerifyManagedLLMToken(bearerCtx(token), token)
		require.NoError(t, err)
		assert.Equal(t, schemas.MCPAuthModeUser, p.Mode)
		assert.Equal(t, "user-alice", p.UserID)

		r.userInactive = true
		_, err = h.VerifyManagedLLMToken(bearerCtx(token), token)
		require.Error(t, err, "a deactivated user's token is refused")
	})

	t.Run("tokens from other grants never qualify", func(t *testing.T) {
		f := newManagedAuthFixture(t, nil)
		h, _ := newHandler(f, nil)
		// A DCR client may register any scope string; without bf_mc its token is still refused.
		ctx := initCtx(&fasthttp.Request{})
		forged, err := f.h.signAccessToken(ctx, string(schemas.MCPAuthModeVK), f.vkID, "mcp llm", []string{testMCPResource, testIssuer}, 300, "")
		require.NoError(t, err)
		_, err = h.VerifyManagedLLMToken(bearerCtx(forged), forged)
		require.Error(t, err)
	})

	t.Run("managed auth off", func(t *testing.T) {
		f := newManagedAuthFixture(t, nil)
		client := f.seedManagedClient(t, "pub", "", configtables.ManagedClientIdentityVK)
		f.setScopes(t, client.ID, "mcp llm")
		token := issue(t, f, nil)
		h, _ := newHandler(f, nil)
		off := *oauth2ServerCfg(f.cfg)
		off.ManagedAuth = &configtables.ManagedAuthConfig{Enabled: false}
		f.cfg.ClientConfig.OAuth2ServerConfig = &off
		_, err := h.VerifyManagedLLMToken(bearerCtx(token), token)
		require.Error(t, err)
	})
}

// TestManagedAuth_UsesOneConfigSnapshot checks a save that removes managed auth
// mid-request cannot nil out the settings the grant is already running with.
func TestManagedAuth_UsesOneConfigSnapshot(t *testing.T) {
	f := newManagedAuthFixture(t, nil)
	f.seedManagedClient(t, "pub", "", configtables.ManagedClientIdentityVK)
	snapshot := oauth2ServerCfg(f.cfg)
	removed := *snapshot
	removed.ManagedAuth = nil
	f.cfg.ClientConfig.OAuth2ServerConfig = &removed // a concurrent save swaps the pointer

	form := url.Values{"grant_type": {idjag.GrantTypeJWTBearer}, "assertion": {f.idp.mint(t, "pub", nil)}, "client_id": {"pub"}}
	ctx := formPostCtx(form.Encode())
	require.NotPanics(t, func() { f.h.handleTokenAssertion(ctx, snapshot) })
	assert.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
}

// TestManagedAuth_ClientIDFromQueryIgnored checks client credentials come from
// the body or Basic auth only, never the query string.
func TestManagedAuth_ClientIDFromQueryIgnored(t *testing.T) {
	f := newManagedAuthFixture(t, nil)
	f.seedManagedClient(t, "pub", "", configtables.ManagedClientIdentityVK)
	form := url.Values{"grant_type": {idjag.GrantTypeJWTBearer}, "assertion": {f.idp.mint(t, "pub", nil)}}
	ctx := formPostCtx(form.Encode())
	ctx.Request.SetRequestURI("/oauth2/token?client_id=pub")
	f.h.handleToken(ctx)
	assert.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode(), "a query-string client_id is not client authentication")
}

// TestManagedAuth_TokenRefusedOnceClientIsRevoked checks a managed-auth token
// names its client (bf_mc) and /mcp refuses it once the client is disabled or
// deleted, or managed auth is turned off. The status is cached, so a change is
// seen after an eviction (or the TTL), not on every request.
func TestManagedAuth_TokenRefusedOnceClientIsRevoked(t *testing.T) {
	f := newManagedAuthFixture(t, nil)
	client := f.seedManagedClient(t, "pub", "", configtables.ManagedClientIdentityVK)
	tokenCtx, body := f.tokenRequest(url.Values{"assertion": {f.idp.mint(t, "pub", nil)}, "client_id": {"pub"}}, "", "")
	require.Equal(t, fasthttp.StatusOK, tokenCtx.Response.StatusCode(), string(tokenCtx.Response.Body()))
	token := body["access_token"].(string)

	key, err := f.cfg.GetOAuth2SigningKey(bgCtx())
	require.NoError(t, err)
	claims, err := verifyMCPJWT(bgCtx(), token, f.cfg, key)
	require.NoError(t, err)
	assert.Equal(t, client.ID, claims.ManagedClientID, "the token names the managed client row")

	cache := NewManagedClientStatusCache(f.cfg)
	h := newTestMCPHandler(f.cfg)
	h.SetManagedClientStatusCache(cache)
	// An initialized RequestCtx: the status lookup reaches the store through it.
	mcpRequest := func(bearer string) (*fasthttp.RequestCtx, *schemas.BifrostContext) {
		ctx := initCtx(&fasthttp.Request{})
		ctx.Request.Header.Set("Authorization", "Bearer "+bearer)
		return ctx, schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}
	authenticate := func() error {
		ctx, bifrostCtx := mcpRequest(token)
		return h.authenticate(ctx, bifrostCtx)
	}
	setDisabled := func(disabled bool) {
		require.NoError(t, f.store.UpdateOAuth2ManagedClient(context.Background(), client.ID, configtables.OAuth2ManagedClientPatch{Disabled: &disabled}))
	}

	require.NoError(t, authenticate(), "an active client's token is admitted")
	setDisabled(true)
	require.NoError(t, authenticate(), "the cached status stands until evicted or expired")
	cache.Evict(client.ID)
	require.Error(t, authenticate(), "a disabled client's token is refused")

	setDisabled(false)
	cache.Evict(client.ID)
	require.NoError(t, authenticate(), "re-enabling admits it again")

	require.NoError(t, f.store.DeleteOAuth2ManagedClient(context.Background(), client.ID))
	cache.Evict(client.ID)
	require.Error(t, authenticate(), "a deleted client's token is refused")
	require.Error(t, authenticate(), "and the refusal is cached, not re-read")

	f2 := newManagedAuthFixture(t, nil)
	f2.seedManagedClient(t, "pub", "", configtables.ManagedClientIdentityVK)
	ctx2, body2 := f2.tokenRequest(url.Values{"assertion": {f2.idp.mint(t, "pub", nil)}, "client_id": {"pub"}}, "", "")
	require.Equal(t, fasthttp.StatusOK, ctx2.Response.StatusCode())
	off := *oauth2ServerCfg(f2.cfg)
	off.ManagedAuth = &configtables.ManagedAuthConfig{Enabled: false}
	f2.cfg.ClientConfig.OAuth2ServerConfig = &off
	h2 := newTestMCPHandler(f2.cfg)
	h2.SetManagedClientStatusCache(NewManagedClientStatusCache(f2.cfg))
	mcpCtx, bifrostCtx := mcpRequest(body2["access_token"].(string))
	require.Error(t, h2.authenticate(mcpCtx, bifrostCtx), "turning managed auth off refuses its tokens")
}

func TestManagedAuth_PublicClientWithBodyClientID(t *testing.T) {
	f := newManagedAuthFixture(t, nil)
	cimd := "https://claude.ai/oauth/claude-code-client-metadata"
	f.seedManagedClient(t, cimd, "", configtables.ManagedClientIdentityVK)
	f.cfg.ClientConfig.OAuth2ServerConfig.ManagedAuth.AccessTokenTTL = 120
	ctx, body := f.tokenRequest(url.Values{"assertion": {f.idp.mint(t, cimd, nil)}, "client_id": {cimd}}, "", "")
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	assert.Equal(t, float64(120), body["expires_in"])
}

func TestManagedAuth_ClientAuthenticationFailures(t *testing.T) {
	f := newManagedAuthFixture(t, nil)
	f.seedManagedClient(t, "conf", "s3cret", configtables.ManagedClientIdentityVK)
	f.seedManagedClient(t, "pub", "", configtables.ManagedClientIdentityVK)
	disabled := f.seedManagedClient(t, "off", "", configtables.ManagedClientIdentityVK)
	off := true
	require.NoError(t, f.store.UpdateOAuth2ManagedClient(context.Background(), disabled.ID, configtables.OAuth2ManagedClientPatch{Disabled: &off}))

	cases := []struct {
		name        string
		form        url.Values
		basicID     string
		basicSecret string
		wantBasic   bool
	}{
		{"wrong secret", url.Values{}, "conf", "nope", true},
		{"missing secret", url.Values{"client_id": {"conf"}}, "", "", false},
		{"secret for public client", url.Values{"client_id": {"pub"}, "client_secret": {"x"}}, "", "", false},
		{"unknown client", url.Values{"client_id": {"ghost"}}, "", "", false},
		{"disabled client", url.Values{"client_id": {"off"}}, "", "", false},
		{"missing client_id", url.Values{}, "", "", false},
		{"two auth methods", url.Values{"client_secret": {"s3cret"}}, "conf", "s3cret", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.form.Set("assertion", f.idp.mint(t, "conf", nil))
			ctx, body := f.tokenRequest(tc.form, tc.basicID, tc.basicSecret)
			assert.Equal(t, fasthttp.StatusUnauthorized, ctx.Response.StatusCode())
			assert.Equal(t, "invalid_client", body["error"])
			assert.Equal(t, tc.wantBasic, len(ctx.Response.Header.Peek("WWW-Authenticate")) > 0)
		})
	}
}

func TestManagedAuth_AssertionAndTargetFailures(t *testing.T) {
	f := newManagedAuthFixture(t, nil)
	f.seedManagedClient(t, "pub", "", configtables.ManagedClientIdentityVK)
	cases := []struct {
		name      string
		assertion string
		resource  string
		wantCode  string
	}{
		{"assertion for another client", f.idp.mint(t, "someone-else", nil), "", "invalid_grant"},
		{"assertion for another audience", f.idp.mint(t, "pub", func(c jwt.MapClaims) { c["aud"] = "https://other.example.com" }), "", "invalid_grant"},
		{"expired assertion", f.idp.mint(t, "pub", func(c jwt.MapClaims) {
			c["exp"] = time.Now().Add(-time.Hour).Unix()
			c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
		}), "", "invalid_grant"},
		{"other resource", f.idp.mint(t, "pub", nil), "https://other.example.com/mcp", "invalid_target"},
		{"missing assertion", "", "", "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"client_id": {"pub"}}
			if tc.assertion != "" {
				form.Set("assertion", tc.assertion)
			}
			if tc.resource != "" {
				form.Set("resource", tc.resource)
			}
			ctx, body := f.tokenRequest(form, "", "")
			assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
			assert.Equal(t, tc.wantCode, body["error"])
		})
	}
}

func TestManagedAuth_InactiveVirtualKeyDenied(t *testing.T) {
	f := newManagedAuthFixture(t, nil)
	f.seedManagedClient(t, "pub", "", configtables.ManagedClientIdentityVK)
	require.NoError(t, f.store.DeleteVirtualKey(context.Background(), f.vkID))
	ctx, body := f.tokenRequest(url.Values{"assertion": {f.idp.mint(t, "pub", nil)}, "client_id": {"pub"}}, "", "")
	assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	assert.Equal(t, "invalid_grant", body["error"])
}

// TestManagedAuth_PrivateIssuerNeedsAllowlist checks the trusted issuer's keys are
// fetched through the guarded OAuth client: with the production dialer a loopback
// IdP is refused until an administrator allowlists it.
func TestManagedAuth_PrivateIssuerNeedsAllowlist(t *testing.T) {
	oauth2.SetDiscoveryDialContextForTests(nil)
	t.Cleanup(func() {
		oauth2.SetDiscoveryDialContextForTests((&net.Dialer{}).DialContext)
		oauth2.SetPrivateTargetAllowlist(nil)
	})
	exchange := func() (int, map[string]any) {
		f := newManagedAuthFixture(t, nil)
		f.seedManagedClient(t, "pub", "", configtables.ManagedClientIdentityVK)
		ctx, body := f.tokenRequest(url.Values{"assertion": {f.idp.mint(t, "pub", nil)}, "client_id": {"pub"}}, "", "")
		return ctx.Response.StatusCode(), body
	}
	_, body := exchange()
	assert.Equal(t, "invalid_grant", body["error"], "a loopback issuer is refused without an allowlist entry")

	allow, err := network.ValidatePrivateTargetAllowlist([]string{"127.0.0.1"})
	require.NoError(t, err)
	oauth2.SetPrivateTargetAllowlist(allow)
	status, body := exchange()
	assert.Equal(t, fasthttp.StatusOK, status, "an allowlisted issuer is reachable: %v", body)
	assert.NotEmpty(t, body["access_token"])
}

// managedAuthResolver is a fakeResolver that also maps ID-JAG subjects and records outcomes.
type managedAuthResolver struct {
	fakeResolver
	resolvedUserID string
	resolveErr     error
	gotSubject     string
	gotProvision   bool
	outcomes       []ManagedAuthOutcome
}

// ResolveManagedAuthUser returns the configured user or error.
func (r *managedAuthResolver) ResolveManagedAuthUser(_ context.Context, claims *idjag.Claims, provision bool) (string, error) {
	r.gotSubject = claims.Subject
	r.gotProvision = provision
	return r.resolvedUserID, r.resolveErr
}

// RecordManagedAuthOutcome captures every outcome.
func (r *managedAuthResolver) RecordManagedAuthOutcome(_ context.Context, o ManagedAuthOutcome) {
	r.outcomes = append(r.outcomes, o)
}

func TestManagedAuth_UserIdentity(t *testing.T) {
	t.Run("maps the subject to a user and audits the grant", func(t *testing.T) {
		r := &managedAuthResolver{fakeResolver: fakeResolver{userModeAvailable: true}, resolvedUserID: "user-alice"}
		f := newManagedAuthFixture(t, r)
		f.seedManagedClient(t, "pub", "", configtables.ManagedClientIdentityUser)
		ctx, body := f.tokenRequest(url.Values{"assertion": {f.idp.mint(t, "pub", nil)}, "client_id": {"pub"}}, "", "")
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		key, err := f.cfg.GetOAuth2SigningKey(bgCtx())
		require.NoError(t, err)
		claims, err := verifyMCPJWT(bgCtx(), body["access_token"].(string), f.cfg, key)
		require.NoError(t, err)
		assert.Equal(t, string(schemas.MCPAuthModeUser), claims.BfMode)
		assert.Equal(t, "user-alice", claims.Subject)
		assert.Equal(t, "00u-alice", r.gotSubject)
		assert.False(t, r.gotProvision, "provisioning is off unless provision_users is set")
		require.Len(t, r.outcomes, 1)
		assert.True(t, r.outcomes[0].Issued)
		assert.Equal(t, "jti-1", r.outcomes[0].JTI)
	})

	for name, tc := range map[string]struct {
		resolver *managedAuthResolver
		status   int
		code     string
	}{
		"denied user": {&managedAuthResolver{fakeResolver: fakeResolver{userModeAvailable: true},
			resolveErr: errors.Join(ErrManagedAuthUserDenied, errors.New("no role mapping"))}, fasthttp.StatusBadRequest, "invalid_grant"},
		"transient resolver error": {&managedAuthResolver{fakeResolver: fakeResolver{userModeAvailable: true},
			resolveErr: errors.New("db down")}, fasthttp.StatusInternalServerError, "server_error"},
		"inactive user": {&managedAuthResolver{fakeResolver: fakeResolver{userModeAvailable: true, userInactive: true},
			resolvedUserID: "user-bob"}, fasthttp.StatusBadRequest, "invalid_grant"},
		"user mode unavailable": {&managedAuthResolver{resolvedUserID: "user-bob"}, fasthttp.StatusBadRequest, "invalid_grant"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newManagedAuthFixture(t, tc.resolver)
			f.seedManagedClient(t, "pub", "", configtables.ManagedClientIdentityUser)
			ctx, body := f.tokenRequest(url.Values{"assertion": {f.idp.mint(t, "pub", nil)}, "client_id": {"pub"}}, "", "")
			assert.Equal(t, tc.status, ctx.Response.StatusCode())
			assert.Equal(t, tc.code, body["error"])
			require.Len(t, tc.resolver.outcomes, 1)
			assert.False(t, tc.resolver.outcomes[0].Issued)
			assert.NotEmpty(t, tc.resolver.outcomes[0].Reason)
		})
	}

	for name, provision := range map[string]bool{"provision_users off by default": false, "provision_users on": true} {
		t.Run(name+" reaches the resolver", func(t *testing.T) {
			r := &managedAuthResolver{fakeResolver: fakeResolver{userModeAvailable: true}, resolvedUserID: "user-alice"}
			f := newManagedAuthFixture(t, r)
			f.cfg.ClientConfig.OAuth2ServerConfig.ManagedAuth.ProvisionUsers = provision
			f.seedManagedClient(t, "pub", "", configtables.ManagedClientIdentityUser)
			ctx, _ := f.tokenRequest(url.Values{"assertion": {f.idp.mint(t, "pub", nil)}, "client_id": {"pub"}}, "", "")
			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
			assert.Equal(t, provision, r.gotProvision)
		})
	}

	t.Run("resolver without user mapping cannot serve user clients", func(t *testing.T) {
		f := newManagedAuthFixture(t, &fakeResolver{userModeAvailable: true})
		f.seedManagedClient(t, "pub", "", configtables.ManagedClientIdentityUser)
		ctx, body := f.tokenRequest(url.Values{"assertion": {f.idp.mint(t, "pub", nil)}, "client_id": {"pub"}}, "", "")
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Equal(t, "invalid_grant", body["error"])
	})
}
