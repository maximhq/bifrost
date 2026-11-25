package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// The document's client_id equals its URL and redirect_uris holds the callback. With a hosted document
// configured, client_id is the hosted URL and the callback stays on Bifrost's own base.
func TestClientMetadataDocument(t *testing.T) {
	const hosted = "https://public.example.com/bifrost/client.json"
	cases := []struct {
		name         string
		externalURL  string
		hostedURL    string
		host         string
		proto        string
		wantBase     string
		wantClientID string
		wantAppType  string
	}{
		{"external URL configured", "https://bifrost.example.com", "", "10.0.0.5:8080", "", "https://bifrost.example.com", "", "web"},
		{"from the request behind a TLS proxy", "", "", "gateway.example.com", "https", "https://gateway.example.com", "", "web"},
		{"local deployment", "", "", "localhost:8080", "", "http://localhost:8080", "", "native"},
		{"hosted document", "https://bifrost.internal.example", hosted, "10.0.0.5:8080", "", "https://bifrost.internal.example", hosted, "web"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clientConfig := &configstore.ClientConfig{}
			if tc.externalURL != "" {
				clientConfig.MCPExternalClientURL = schemas.NewSecretVar(tc.externalURL)
			}
			if tc.hostedURL != "" {
				clientConfig.MCPClientMetadataURL = schemas.NewSecretVar(tc.hostedURL)
			}
			if tc.wantClientID == "" {
				tc.wantClientID = tc.wantBase + oauthClientMetadataPath
			}
			h := NewOAuthHandler(newTestOAuthProvider(t, nil, bifrost.NewDefaultLogger(schemas.LogLevelError)), nil, &lib.Config{ClientConfig: clientConfig}, nil)
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.SetHost(tc.host)
			if tc.proto != "" {
				ctx.Request.Header.Set("X-Forwarded-Proto", tc.proto)
			}
			ctx.Request.SetRequestURI(oauthClientMetadataPath)

			h.handleClientMetadataDocument(ctx)

			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
			assert.Equal(t, "application/json", string(ctx.Response.Header.ContentType()))
			var doc map[string]any
			require.NoError(t, json.Unmarshal(ctx.Response.Body(), &doc))
			assert.Equal(t, tc.wantClientID, doc["client_id"], "client_id must equal the URL the document is fetched from")
			assert.Equal(t, []any{tc.wantBase + "/api/oauth/callback"}, doc["redirect_uris"])
			assert.Equal(t, "Bifrost MCP Gateway", doc["client_name"])
			assert.Equal(t, "none", doc["token_endpoint_auth_method"])
			assert.Equal(t, []any{"authorization_code", "refresh_token"}, doc["grant_types"])
			assert.Equal(t, []any{"code"}, doc["response_types"])
			assert.Equal(t, tc.wantAppType, doc["application_type"])
		})
	}
}

// BenchmarkClientMetadataDocument measures serving Bifrost's Client ID Metadata Document, with a
// configured external URL and with the base derived from the request.
func BenchmarkClientMetadataDocument(b *testing.B) {
	for name, externalURL := range map[string]string{"external URL": "https://bifrost.example.com", "from request": ""} {
		b.Run(name, func(b *testing.B) {
			clientConfig := &configstore.ClientConfig{}
			if externalURL != "" {
				clientConfig.MCPExternalClientURL = schemas.NewSecretVar(externalURL)
			}
			h := NewOAuthHandler(newTestOAuthProvider(b, nil, bifrost.NewDefaultLogger(schemas.LogLevelError)), nil, &lib.Config{ClientConfig: clientConfig}, nil)
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.SetHost("gateway.example.com")
			ctx.Request.SetRequestURI(oauthClientMetadataPath)
			b.ReportAllocs()
			for b.Loop() {
				ctx.Response.Reset()
				h.handleClientMetadataDocument(ctx)
			}
		})
	}
}

// RFC 9207: the callback checks iss against the issuer the flow recorded before it acts on the
// code or on an error, and on a mismatch acts on neither.
func TestHandleOAuthCallback_ChecksIssuer(t *testing.T) {
	const issuer = "https://auth.example.com"
	const (
		refused  = "did not come from"
		denied   = "denied or failed"
		redeemed = "OAuth authentication failed." // the code reached the (unreachable) token endpoint
	)
	for _, tc := range []struct {
		name   string
		query  string
		wanted string
	}{
		{"matching iss", "code=c&iss=" + url.QueryEscape(issuer), redeemed},
		{"another issuer", "code=c&iss=" + url.QueryEscape("https://attacker.example"), refused},
		{"missing iss the server advertises", "code=c", refused},
		{"error from the expected issuer", "error=access_denied&iss=" + url.QueryEscape(issuer), denied},
		{"error from another issuer", "error=access_denied&iss=" + url.QueryEscape("https://attacker.example"), refused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetLogger(&mockLogger{})
			store := newRealOAuth2Store(t)
			provider := newTestOAuthProvider(t, store, bifrost.NewDefaultLogger(schemas.LogLevelError))
			h := NewOAuthHandler(provider, nil, &lib.Config{ConfigStore: store, ClientConfig: &configstore.ClientConfig{}, OAuthProvider: provider}, nil)
			ctx := bgCtx()
			require.NoError(t, store.CreateOauthConfig(ctx, &tables.TableOauthConfig{
				ID: "cfg-1", ClientID: schemas.NewSecretVar("client"), AuthorizeURL: issuer + "/authorize", TokenURL: "http://127.0.0.1:1/token",
				RedirectURI: "http://localhost:8080/api/oauth/callback", Status: "pending", Issuer: issuer, IssParameterSupported: true,
			}))
			require.NoError(t, store.CreateOauthUserSession(ctx, &tables.TableMCPOauthFlow{
				ID: "flow-1", MCPClientID: "mcp-1", OauthConfigID: "cfg-1", State: "state-1", CodeVerifier: "verifier",
				RedirectURI: "http://localhost:8080/api/oauth/callback", FlowMode: "admin", Status: "pending", ExpiresAt: time.Now().Add(time.Minute),
				Issuer: issuer, IssParameterSupported: true,
			}))

			var req fasthttp.Request
			req.SetRequestURI("/api/oauth/callback?state=state-1&" + tc.query)
			callback := initCtx(&req)
			h.handleOAuthCallback(callback)

			require.Equal(t, fasthttp.StatusFound, callback.Response.StatusCode())
			location, err := url.QueryUnescape(string(callback.Response.Header.Peek("Location")))
			require.NoError(t, err)
			assert.Contains(t, location, "/workspace/mcp-registry/oauth-callback?status=failed")
			assert.Contains(t, location, tc.wanted)
		})
	}
}

// A per-user sign-in is finished in the browser that started it. Starting a flow
// from the authentication page leaves a cookie tied to that flow, and the callback
// only redeems the code when the cookie comes back with it. A callback arriving in
// any other browser leaves the flow pending and tells the user to start again.
// Admin flows are driven through the API as well as the dashboard, so they are
// not tied to a browser.
func TestHandleOAuthCallback_PerUserFlowFinishesInTheBrowserThatStartedIt(t *testing.T) {
	const (
		otherBrowser = "different browser"
		redeemed     = "OAuth authentication failed" // the code reached the (unreachable) token endpoint
	)
	setup := func(t *testing.T, flowMode string) (*OAuthHandler, configstore.ConfigStore) {
		SetLogger(&mockLogger{})
		store := newRealOAuth2Store(t)
		provider := newTestOAuthProvider(t, store, bifrost.NewDefaultLogger(schemas.LogLevelError))
		h := NewOAuthHandler(provider, nil, &lib.Config{ConfigStore: store, ClientConfig: &configstore.ClientConfig{}, OAuthProvider: provider}, nil)
		ctx := bgCtx()
		require.NoError(t, store.CreateOauthConfig(ctx, &tables.TableOauthConfig{
			ID: "cfg-1", ClientID: schemas.NewSecretVar("client"), AuthorizeURL: "https://auth.example.com/authorize", TokenURL: "http://127.0.0.1:1/token",
			RedirectURI: "http://localhost:8080/api/oauth/callback", Status: "pending",
		}))
		require.NoError(t, store.CreateOauthUserSession(ctx, &tables.TableMCPOauthFlow{
			ID: "flow-1", MCPClientID: "mcp-1", OauthConfigID: "cfg-1", State: "state-1", CodeVerifier: "verifier",
			RedirectURI: "http://localhost:8080/api/oauth/callback", FlowMode: flowMode, Status: "pending", ExpiresAt: time.Now().Add(time.Minute),
		}))
		return h, store
	}
	callback := func(h *OAuthHandler, cookieFrom *fasthttp.RequestCtx) string {
		var req fasthttp.Request
		req.SetRequestURI("/api/oauth/callback?state=state-1&code=c")
		if cookieFrom != nil {
			cookieFrom.Response.Header.VisitAllCookie(func(key, value []byte) {
				var c fasthttp.Cookie
				require.NoError(t, c.ParseBytes(value))
				req.Header.SetCookieBytesKV(c.Key(), c.Value())
			})
		}
		ctx := initCtx(&req)
		h.handleOAuthCallback(ctx)
		require.Equal(t, fasthttp.StatusFound, ctx.Response.StatusCode())
		location, err := url.QueryUnescape(string(ctx.Response.Header.Peek("Location")))
		require.NoError(t, err)
		return location
	}
	started := func(t *testing.T, store configstore.ConfigStore, upstreamURL string) *fasthttp.RequestCtx {
		var req fasthttp.Request
		ctx := initCtx(&req)
		require.NoError(t, bindOAuthFlowToBrowser(ctx, store, "flow-1", upstreamURL, time.Now().Add(time.Minute)))
		return ctx
	}

	t.Run("another browser is turned away and the flow stays pending", func(t *testing.T) {
		h, store := setup(t, "vk")
		assert.Contains(t, callback(h, nil), otherBrowser)
		flow, err := store.GetOauthUserSessionByState(bgCtx(), "state-1")
		require.NoError(t, err)
		require.NotNil(t, flow)
		assert.Equal(t, "pending", flow.Status)
	})
	t.Run("a cookie for another flow does not count", func(t *testing.T) {
		h, store := setup(t, "user")
		require.NoError(t, store.CreateOauthUserSession(bgCtx(), &tables.TableMCPOauthFlow{
			ID: "flow-2", MCPClientID: "mcp-2", OauthConfigID: "cfg-1", State: "some-other-state", CodeVerifier: "verifier",
			RedirectURI: "http://localhost:8080/api/oauth/callback", FlowMode: "user", Status: "pending", ExpiresAt: time.Now().Add(time.Minute),
		}))
		var req fasthttp.Request
		other := initCtx(&req)
		require.NoError(t, bindOAuthFlowToBrowser(other, store, "flow-2", "https://auth.example.com/authorize?state=some-other-state", time.Now().Add(time.Minute)))
		assert.Contains(t, callback(h, other), otherBrowser)
	})
	t.Run("a flow that is no longer pending is not bound", func(t *testing.T) {
		_, store := setup(t, "vk")
		var req fasthttp.Request
		ctx := initCtx(&req)
		err := bindOAuthFlowToBrowser(ctx, store, "flow-1", "https://auth.example.com/authorize?state=a-renewed-state", time.Now().Add(time.Minute))
		require.ErrorIs(t, err, schemas.ErrOAuth2FlowNotPending)
		assert.Empty(t, ctx.Response.Header.Peek("Set-Cookie"), "no cookie is set for a flow that was not bound")
	})
	// The state is in the authorize URL and in the callback URL, so anything worked out from it
	// is known to whoever holds either URL. Only the value the starting browser was given counts.
	t.Run("a cookie computed from the state does not count", func(t *testing.T) {
		h, store := setup(t, "vk")
		sum := sha256.Sum256([]byte("state-1"))
		digest := hex.EncodeToString(sum[:])
		var req fasthttp.Request
		req.SetRequestURI("/api/oauth/callback?state=state-1&code=c")
		req.Header.SetCookie(oauthFlowCookiePrefix+digest[:12], digest[12:])
		ctx := initCtx(&req)
		h.handleOAuthCallback(ctx)
		location, err := url.QueryUnescape(string(ctx.Response.Header.Peek("Location")))
		require.NoError(t, err)
		assert.Contains(t, location, otherBrowser)
		flow, err := store.GetOauthUserSessionByState(bgCtx(), "state-1")
		require.NoError(t, err)
		assert.Equal(t, "pending", flow.Status)
	})
	// The flow is bound, and the callback carries the flow's own cookie name with another value.
	// This is the comparison itself: the name alone opens nothing.
	t.Run("the flow's cookie with a wrong value does not count", func(t *testing.T) {
		h, store := setup(t, "vk")
		started(t, store, "https://auth.example.com/authorize?client_id=client&state=state-1")
		var req fasthttp.Request
		req.SetRequestURI("/api/oauth/callback?state=state-1&code=c")
		req.Header.SetCookie(oauthFlowCookieName("state-1"), "not-the-value-this-browser-was-given")
		ctx := initCtx(&req)
		h.handleOAuthCallback(ctx)
		location, err := url.QueryUnescape(string(ctx.Response.Header.Peek("Location")))
		require.NoError(t, err)
		assert.Contains(t, location, otherBrowser)
		flow, err := store.GetOauthUserSessionByState(bgCtx(), "state-1")
		require.NoError(t, err)
		require.NotNil(t, flow)
		assert.Equal(t, "pending", flow.Status)
	})
	t.Run("the browser that started the flow completes it", func(t *testing.T) {
		h, store := setup(t, "vk")
		location := callback(h, started(t, store, "https://auth.example.com/authorize?client_id=client&state=state-1"))
		assert.NotContains(t, location, otherBrowser)
		assert.Contains(t, location, redeemed)
	})
	t.Run("an admin flow is not tied to a browser", func(t *testing.T) {
		h, _ := setup(t, "admin")
		location := callback(h, nil)
		assert.NotContains(t, location, otherBrowser)
		assert.Contains(t, location, redeemed)
	})
	t.Run("the cookie is HttpOnly and expires with the flow", func(t *testing.T) {
		_, store := setup(t, "vk")
		ctx := started(t, store, "https://auth.example.com/authorize?state=state-1")
		var seen int
		ctx.Response.Header.VisitAllCookie(func(_, value []byte) {
			var c fasthttp.Cookie
			require.NoError(t, c.ParseBytes(value))
			seen++
			assert.True(t, c.HTTPOnly())
			assert.Equal(t, fasthttp.CookieSameSiteLaxMode, c.SameSite())
			assert.NotContains(t, string(c.Key())+string(c.Value()), "state-1", "the state itself is not stored in the cookie")
			flow, err := store.GetOauthUserSessionByState(bgCtx(), "state-1")
			require.NoError(t, err)
			assert.Equal(t, oauthFlowBindingHash(c.Value()), flow.BrowserBindingHash, "the flow stores the hash of the value")
			assert.NotEqual(t, string(c.Value()), flow.BrowserBindingHash, "the value itself is not stored")
		})
		assert.Equal(t, 1, seen)
	})
}

// failingFlowLookupStore fails the lookup the browser check makes, as a database fault would.
type failingFlowLookupStore struct {
	configstore.ConfigStore
}

func (s failingFlowLookupStore) GetOauthUserSessionByState(context.Context, string) (*tables.TableMCPOauthFlow, error) {
	return nil, errors.New("database unavailable")
}

// When the browser check cannot read the flow it cannot tell whose browser this is, so the
// callback stops there. The code is not redeemed and the flow stays pending for a retry.
func TestHandleOAuthCallback_BrowserBindingLookupErrorDoesNotRedeem(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	provider := newTestOAuthProvider(t, store, bifrost.NewDefaultLogger(schemas.LogLevelError))
	h := NewOAuthHandler(provider, nil, &lib.Config{ConfigStore: failingFlowLookupStore{store}, ClientConfig: &configstore.ClientConfig{}, OAuthProvider: provider}, nil)
	ctx := bgCtx()
	require.NoError(t, store.CreateOauthConfig(ctx, &tables.TableOauthConfig{
		ID: "cfg-1", ClientID: schemas.NewSecretVar("client"), AuthorizeURL: "https://auth.example.com/authorize", TokenURL: "http://127.0.0.1:1/token",
		RedirectURI: "http://localhost:8080/api/oauth/callback", Status: "pending",
	}))
	require.NoError(t, store.CreateOauthUserSession(ctx, &tables.TableMCPOauthFlow{
		ID: "flow-1", MCPClientID: "mcp-1", OauthConfigID: "cfg-1", State: "state-1", CodeVerifier: "verifier",
		RedirectURI: "http://localhost:8080/api/oauth/callback", FlowMode: "vk", Status: "pending", ExpiresAt: time.Now().Add(time.Minute),
	}))

	var req fasthttp.Request
	req.SetRequestURI("/api/oauth/callback?state=state-1&code=c")
	callback := initCtx(&req)
	h.handleOAuthCallback(callback)

	location, err := url.QueryUnescape(string(callback.Response.Header.Peek("Location")))
	require.NoError(t, err)
	assert.NotContains(t, location, "OAuth authentication failed", "the code was sent to the token endpoint")
	flow, err := store.GetOauthUserSessionByState(ctx, "state-1")
	require.NoError(t, err)
	require.NotNil(t, flow)
	assert.Equal(t, "pending", flow.Status, "the flow was claimed although the browser was never checked")
}

// The browser check runs once per OAuth callback, which is one request per sign-in and not on
// the inference path. It adds one indexed read of the flow by its state, a SHA-256 of the cookie
// and a constant-time compare. Both outcomes are measured against a real SQLite config store.
func BenchmarkOAuthCallbackBrowserCheck(b *testing.B) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(b)
	provider := newTestOAuthProvider(b, store, bifrost.NewDefaultLogger(schemas.LogLevelError))
	h := NewOAuthHandler(provider, nil, &lib.Config{ConfigStore: store, ClientConfig: &configstore.ClientConfig{}, OAuthProvider: provider}, nil)
	require.NoError(b, store.CreateOauthUserSession(bgCtx(), &tables.TableMCPOauthFlow{
		ID: "flow-1", MCPClientID: "mcp-1", OauthConfigID: "cfg-1", State: "state-1", CodeVerifier: "verifier",
		RedirectURI: "http://localhost:8080/api/oauth/callback", FlowMode: "vk", Status: "pending", ExpiresAt: time.Now().Add(time.Hour),
	}))
	const browserValue = "benchmark-browser-value"
	recorded, err := store.RecordOAuthFlowBrowserBinding(bgCtx(), "flow-1", "state-1", oauthFlowBindingHash([]byte(browserValue)))
	require.NoError(b, err)
	require.True(b, recorded)

	for name, cookie := range map[string]string{"the starting browser": browserValue, "another browser": ""} {
		b.Run(name, func(b *testing.B) {
			var req fasthttp.Request
			req.SetRequestURI("/api/oauth/callback?state=state-1&code=c")
			if cookie != "" {
				req.Header.SetCookie(oauthFlowCookieName("state-1"), cookie)
			}
			ctx := initCtx(&req)
			b.ReportAllocs()
			for b.Loop() {
				ctx.Response.Reset()
				if refused := h.refuseCallbackFromAnotherBrowser(ctx, "state-1"); refused != (cookie == "") {
					b.Fatalf("refused = %v", refused)
				}
			}
		})
	}
}
