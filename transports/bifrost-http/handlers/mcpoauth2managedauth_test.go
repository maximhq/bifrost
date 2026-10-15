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

// TestManagedAuth_ScopeFromAssertion checks the IdP's scope decision is applied:
// no scope claim grants mcp, a claim must include mcp, and other scopes in it
// are ignored rather than refused.
func TestManagedAuth_ScopeFromAssertion(t *testing.T) {
	for name, tc := range map[string]struct {
		scope      any // nil leaves the claim out
		wantStatus int
		wantError  string
	}{
		"no claim":           {nil, fasthttp.StatusOK, ""},
		"mcp":                {"mcp", fasthttp.StatusOK, ""},
		"mcp among others":   {"openid mcp chat.read", fasthttp.StatusOK, ""},
		"only other scopes":  {"chat.read chat.write", fasthttp.StatusBadRequest, "invalid_scope"},
		"empty string claim": {"", fasthttp.StatusOK, ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := newManagedAuthFixture(t, nil)
			f.seedManagedClient(t, "pub", "", configtables.ManagedClientIdentityVK)
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
			assert.Equal(t, "mcp", body["scope"])
		})
	}
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
