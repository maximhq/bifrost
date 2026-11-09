package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/oauth2"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// The document's client_id equals its URL and redirect_uris holds the callback.
func TestClientMetadataDocument(t *testing.T) {
	cases := []struct {
		name        string
		externalURL string
		host        string
		proto       string
		wantBase    string
		wantAppType string
	}{
		{"external URL configured", "https://bifrost.example.com", "10.0.0.5:8080", "", "https://bifrost.example.com", "web"},
		{"from the request behind a TLS proxy", "", "gateway.example.com", "https", "https://gateway.example.com", "web"},
		{"local deployment", "", "localhost:8080", "", "http://localhost:8080", "native"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clientConfig := &configstore.ClientConfig{}
			if tc.externalURL != "" {
				clientConfig.MCPExternalClientURL = schemas.NewSecretVar(tc.externalURL)
			}
			h := &OAuthHandler{store: &lib.Config{ClientConfig: clientConfig}}
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
			assert.Equal(t, tc.wantBase+oauthClientMetadataPath, doc["client_id"], "client_id must equal the URL the document is served at")
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
			h := &OAuthHandler{store: &lib.Config{ClientConfig: clientConfig}}
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
			provider := oauth2.NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))
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

// setFlowOrigin records origin as the dashboard page the flow under state started from.
func setFlowOrigin(t *testing.T, store configstore.ConfigStore, state, origin string) {
	t.Helper()
	flow, err := store.GetOauthUserSessionByState(context.Background(), state)
	require.NoError(t, err)
	require.NotNil(t, flow)
	flow.Origin = origin
	require.NoError(t, store.UpdateOauthUserSession(context.Background(), flow))
}

// signInDashboardSession stores a live dashboard session and returns its cookie value.
func signInDashboardSession(t *testing.T, store configstore.ConfigStore) string {
	t.Helper()
	const token = "dashboard-session"
	require.NoError(t, store.CreateSession(context.Background(), &tables.SessionsTable{Token: token, ExpiresAt: time.Now().Add(time.Hour)}))
	return token
}

// callOAuthCallbackAs runs the callback with query, sending cookie as the dashboard session
// when it is set, and returns the path and query the browser is redirected to.
func callOAuthCallbackAs(t *testing.T, h *OAuthHandler, query, cookie string) string {
	t.Helper()
	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodGet)
	req.SetRequestURI("/api/oauth/callback?" + query)
	if cookie != "" {
		req.Header.SetCookie("token", cookie)
	}
	var ctx fasthttp.RequestCtx
	ctx.Init(&req, nil, nil)
	h.handleOAuthCallback(&ctx)
	require.Equal(t, fasthttp.StatusFound, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	location, err := url.Parse(string(ctx.Response.Header.Peek(fasthttp.HeaderLocation)))
	require.NoError(t, err)
	return location.RequestURI()
}

// A per-user sign-in started from the Home page sends the browser back to Home, naming the client
// and how sign-in went, whether or not the callback request carries a session cookie this handler
// recognizes: only connect records an origin, and connect needs a signed-in user.
func TestOAuthCallback_ReturnsToThePageTheSignInStartedFrom(t *testing.T) {
	const home = "/workspace/home?tab=access"
	const code = "state=state-1&code=code-1"

	t.Run("signed in", func(t *testing.T) {
		cfg, store, _ := newOAuthSignIn(t, false)
		setFlowOrigin(t, store, "state-1", tables.MCPOauthFlowOriginHome)

		location := callOAuthCallbackAs(t, NewOAuthHandler(cfg.OAuthProvider, nil, cfg, &signInMCPManager{}), code, signInDashboardSession(t, store))

		assert.Equal(t, home+"&sign_in=connected&mcp_client_id=client-notes", location)
	})

	t.Run("without a session cookie", func(t *testing.T) {
		cfg, store, _ := newOAuthSignIn(t, false)
		setFlowOrigin(t, store, "state-1", tables.MCPOauthFlowOriginHome)

		location := callOAuthCallbackAs(t, NewOAuthHandler(cfg.OAuthProvider, nil, cfg, &signInMCPManager{}), code, "")

		assert.Equal(t, home+"&sign_in=connected&mcp_client_id=client-notes", location)
	})

	t.Run("denied at the provider, without a session cookie", func(t *testing.T) {
		cfg, store, _ := newOAuthSignIn(t, false)
		setFlowOrigin(t, store, "state-1", tables.MCPOauthFlowOriginHome)

		location := callOAuthCallbackAs(t, NewOAuthHandler(cfg.OAuthProvider, nil, cfg, &signInMCPManager{}), "state=state-1&error=access_denied", "")

		assert.Equal(t, home+"&sign_in=failed&mcp_client_id=client-notes&error="+url.QueryEscape("Authentication was denied or failed. Please try again."), location)
	})

	t.Run("under a base path", func(t *testing.T) {
		lib.SetBasePath("/bifrost")
		t.Cleanup(func() { lib.SetBasePath("") })
		cfg, store, _ := newOAuthSignIn(t, false)
		setFlowOrigin(t, store, "state-1", tables.MCPOauthFlowOriginHome)

		location := callOAuthCallbackAs(t, NewOAuthHandler(cfg.OAuthProvider, nil, cfg, &signInMCPManager{}), code, signInDashboardSession(t, store))

		assert.Equal(t, "/bifrost"+home+"&sign_in=connected&mcp_client_id=client-notes", location)
	})

	t.Run("tools could not be loaded", func(t *testing.T) {
		cfg, store, _ := newOAuthSignIn(t, true)
		setFlowOrigin(t, store, "state-1", tables.MCPOauthFlowOriginHome)
		manager := &signInMCPManager{verifyErr: errors.New("upstream unavailable")}

		location := callOAuthCallbackAs(t, NewOAuthHandler(cfg.OAuthProvider, nil, cfg, manager), code, signInDashboardSession(t, store))

		assert.Equal(t, home+"&sign_in=connected&mcp_client_id=client-notes&tools=pending&client=notes", location)
	})

	t.Run("denied at the provider", func(t *testing.T) {
		cfg, store, _ := newOAuthSignIn(t, false)
		setFlowOrigin(t, store, "state-1", tables.MCPOauthFlowOriginHome)

		location := callOAuthCallbackAs(t, NewOAuthHandler(cfg.OAuthProvider, nil, cfg, &signInMCPManager{}), "state=state-1&error=access_denied", signInDashboardSession(t, store))

		assert.Equal(t, home+"&sign_in=failed&mcp_client_id=client-notes&error="+url.QueryEscape("Authentication was denied or failed. Please try again."), location)
	})

	t.Run("token exchange fails", func(t *testing.T) {
		// Completing a flow deletes its row when the exchange fails, so the page is read before.
		cfg, store := newSignInConfig(t)
		require.NoError(t, store.CreateOauthConfig(context.Background(), &tables.TableOauthConfig{
			ID: "oauth-notes", ClientID: schemas.NewSecretVar("bifrost"), TokenURL: "http://127.0.0.1:1/token",
			AuthorizeURL: "https://idp.example/authorize", RedirectURI: "http://localhost/api/oauth/callback", Status: "authorized",
		}))
		createSignInClient(t, store, &schemas.MCPClientConfig{
			ID: "client-notes", Name: "notes", ConnectionType: schemas.MCPConnectionTypeHTTP,
			ConnectionString: schemas.NewSecretVar("http://upstream.invalid/mcp"), AuthType: schemas.MCPAuthTypePerUserOauth,
			OauthConfigID: schemas.Ptr("oauth-notes"), ToolsToExecute: []string{"*"},
		}, false)
		createUserOAuthFlow(t, store, "client-notes", "alice", "state-1")
		setFlowOrigin(t, store, "state-1", tables.MCPOauthFlowOriginHome)

		location := callOAuthCallbackAs(t, NewOAuthHandler(cfg.OAuthProvider, nil, cfg, &signInMCPManager{}), code, signInDashboardSession(t, store))

		assert.Equal(t, home+"&sign_in=failed&mcp_client_id=client-notes&error="+url.QueryEscape("OAuth authentication failed. Please try again."), location)
	})
}

// The callback keeps the default page for a flow with no origin, or whose origin names no page it
// knows: a visitor without a dashboard session lands on the public page, a signed-in one on the
// sessions list.
func TestOAuthCallback_KeepsTheDefaultPageForAnUnknownOrigin(t *testing.T) {
	for _, tc := range []struct {
		name     string
		origin   string
		signedIn bool
		want     string
	}{
		{name: "not signed in, no origin", want: "/workspace/mcp-sessions/auth-success"},
		{name: "not signed in, unknown origin", origin: "settings", want: "/workspace/mcp-sessions/auth-success"},
		{name: "no origin", signedIn: true, want: "/workspace/mcp-sessions?completed=1"},
		{name: "unknown origin", origin: "settings", signedIn: true, want: "/workspace/mcp-sessions?completed=1"},
		{name: "a path, not an origin", origin: "/workspace/home", signedIn: true, want: "/workspace/mcp-sessions?completed=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, store, _ := newOAuthSignIn(t, false)
			setFlowOrigin(t, store, "state-1", tc.origin)
			cookie := ""
			if tc.signedIn {
				cookie = signInDashboardSession(t, store)
			}

			location := callOAuthCallbackAs(t, NewOAuthHandler(cfg.OAuthProvider, nil, cfg, &signInMCPManager{}), "state=state-1&code=code-1", cookie)

			assert.Equal(t, tc.want, location)
		})
	}
}
