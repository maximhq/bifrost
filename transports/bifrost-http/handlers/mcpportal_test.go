package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	mcpheaders "github.com/maximhq/bifrost/framework/mcp_headers"
	"github.com/maximhq/bifrost/framework/oauth2"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type portalResolverFunc func(*schemas.BifrostContext) (schemas.Access, error)

func (f portalResolverFunc) ResolveAccess(ctx *schemas.BifrostContext) (schemas.Access, error) {
	return f(ctx)
}

type portalClients []schemas.MCPClient

func (c portalClients) GetMCPClients() ([]schemas.MCPClient, error) { return c, nil }

type portalScopedStore struct {
	configstore.ConfigStore
	forbiddenVK string
	lastCaller  string
	listErr     error
}

func (s *portalScopedStore) GetVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error) {
	s.lastCaller, _ = ctx.Value(schemas.BifrostContextKeyUserID).(string)
	if id == s.forbiddenVK {
		return nil, configstore.ErrNotFound
	}
	return s.ConfigStore.GetVirtualKey(ctx, id)
}
func (s *portalScopedStore) GetMCPConfig(ctx context.Context) (*schemas.MCPConfig, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.ConfigStore.GetMCPConfig(ctx)
}

type portalFixture struct {
	h       *MCPPortalHandler
	store   *portalScopedStore
	configs []*schemas.MCPClientConfig
}

func portalAccess(clients ...string) schemas.Access {
	permits := make([]schemas.MCPPermit, 0, len(clients))
	for _, name := range clients {
		permits = append(permits, schemas.MCPPermit{Client: name, ClientName: name, Tools: []string{"read"}})
	}
	return grant.NewAccess([]schemas.Permit{grant.NewPermit("access_profile", "profile", "Profile", true, false, nil, permits)}, nil, "", nil)
}

func newPortalFixture(t *testing.T) *portalFixture {
	t.Helper()
	store := &portalScopedStore{ConfigStore: newRealOAuth2Store(t)}
	ctx := context.Background()
	require.NoError(t, store.CreateOauthConfig(ctx, &tables.TableOauthConfig{
		ID: "portal-oauth", ClientID: schemas.NewSecretVar("client-id"), ClientSecret: schemas.NewSecretVar("oauth-client-secret"),
		AuthorizeURL: "https://upstream.example/authorize", TokenURL: "https://upstream.example/token", RedirectURI: "https://bifrost.example/api/oauth/callback", Status: "authorized",
	}))
	configs := []*schemas.MCPClientConfig{
		{ID: "oauth", Name: "oauth", AuthType: schemas.MCPAuthTypePerUserOauth, OauthConfigID: bifrost.Ptr("portal-oauth")},
		{ID: "headers", Name: "headers", AuthType: schemas.MCPAuthTypePerUserHeaders, PerUserHeaderKeys: []string{"x-api-key"}},
		{ID: "shared", Name: "shared", AuthType: schemas.MCPAuthTypeHeaders, Headers: map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar("shared-secret")}},
		{ID: "denied", Name: "denied", AuthType: schemas.MCPAuthTypeNone},
	}
	var runtime portalClients
	for _, client := range configs {
		client.ConnectionType = schemas.MCPConnectionTypeHTTP
		client.ConnectionString = schemas.NewSecretVar("https://upstream.example/secret-endpoint")
		require.NoError(t, store.CreateMCPClientConfig(ctx, client))
		runtime = append(runtime, schemas.MCPClient{Config: client, State: schemas.MCPConnectionStateHealthy})
	}
	for _, id := range []string{"vk-a", "vk-b"} {
		require.NoError(t, store.CreateVirtualKey(ctx, &tables.TableVirtualKey{ID: id, Name: id, Value: *schemas.NewSecretVar("sk-bf-" + id), IsActive: bifrost.Ptr(true)}))
	}
	cfg := &lib.Config{ConfigStore: store, ClientConfig: &configstore.ClientConfig{}, OAuthProvider: oauth2.NewOAuth2Provider(store, nil), MCPHeadersProvider: mcpheaders.NewProvider(store, nil)}
	h := NewMCPPortalHandler(cfg, portalResolverFunc(func(*schemas.BifrostContext) (schemas.Access, error) {
		return portalAccess("oauth", "headers", "shared"), nil
	}), runtime)
	return &portalFixture{h: h, store: store, configs: configs}
}

func portalRequest(t *testing.T, h *MCPPortalHandler, method, target, body, user string) *fasthttp.RequestCtx {
	t.Helper()
	var req fasthttp.Request
	req.Header.SetMethod(method)
	req.Header.SetHost("bifrost.example")
	req.SetRequestURI(target)
	req.SetBodyString(body)
	ctx := initCtx(&req)
	if user != "" {
		ctx.SetUserValue(schemas.BifrostContextKeyUserID, user)
	}
	r := router.New()
	h.RegisterRoutes(r)
	r.Handler(ctx)
	return ctx
}

func portalBody(t *testing.T, ctx *fasthttp.RequestCtx) mcpPortalResponse {
	t.Helper()
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	var body mcpPortalResponse
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &body))
	return body
}

func TestMCPPortalIdentityIsolationAndNoSecrets(t *testing.T) {
	f := newPortalFixture(t)
	ctx := portalRequest(t, f.h, "GET", "/api/mcp/portal", "", "")
	body := portalBody(t, ctx)
	assert.Nil(t, body.Identity)
	assert.Empty(t, body.Servers)
	assert.Contains(t, string(ctx.Response.Body()), `"servers":[]`)

	// A header that resembles the trusted context key must never select a user.
	var req fasthttp.Request
	req.SetRequestURI("/api/mcp/portal")
	req.Header.Set("bifrost-user-id", "alice")
	forged := initCtx(&req)
	f.h.list(forged)
	assert.Nil(t, portalBody(t, forged).Identity)

	ctx = portalRequest(t, f.h, "GET", "/api/mcp/portal", "", "alice")
	body = portalBody(t, ctx)
	require.NotNil(t, body.Identity)
	assert.Equal(t, "alice", body.Identity.ID)
	assert.Equal(t, schemas.MCPAuthModeUser, body.Identity.Mode)
	assert.Equal(t, 3, body.Count)
	assert.Equal(t, "headers", body.Servers[0].ID)
	assert.Equal(t, "missing", body.Servers[0].CredentialStatus)
	assert.Equal(t, "admin_managed", body.Servers[2].CredentialStatus)
	assert.False(t, body.Servers[2].CanConfigure)
	for _, secret := range []string{"shared-secret", "oauth-client-secret", "secret-endpoint", "sk-bf-", "denied"} {
		assert.NotContains(t, string(ctx.Response.Body()), secret)
	}
	assert.Equal(t, fasthttp.StatusBadRequest, portalRequest(t, f.h, "GET", "/api/mcp/portal?user_id=bob", "", "alice").Response.StatusCode())
	assert.Equal(t, fasthttp.StatusBadRequest, portalRequest(t, f.h, "POST", "/api/mcp/portal/oauth/authenticate", `{"user_id":"bob"}`, "alice").Response.StatusCode())
	f.h.resolver = portalResolverFunc(func(ctx *schemas.BifrostContext) (schemas.Access, error) {
		user := ctx.Grant().Identity().User()
		require.NotNil(t, user)
		if user.ID == "alice" {
			return portalAccess("headers"), nil
		}
		return portalAccess("oauth"), nil
	})
	alice := portalBody(t, portalRequest(t, f.h, "GET", "/api/mcp/portal", "", "alice"))
	bob := portalBody(t, portalRequest(t, f.h, "GET", "/api/mcp/portal", "", "bob"))
	require.Len(t, alice.Servers, 1)
	require.Len(t, bob.Servers, 1)
	assert.Equal(t, "headers", alice.Servers[0].ID)
	assert.Equal(t, "oauth", bob.Servers[0].ID)
}

func TestMCPPortalSelectedVKUsesScopedLookupAndFreshGrant(t *testing.T) {
	f := newPortalFixture(t)
	f.store.forbiddenVK = "vk-b"
	f.h.resolver = portalResolverFunc(func(ctx *schemas.BifrostContext) (schemas.Access, error) {
		require.Nil(t, ctx.Grant().Access(), "the dashboard caller's prior access must not survive a VK selection")
		assert.Equal(t, "sk-bf-vk-a", ctx.Grant().Identity().Credential().Value)
		return portalAccess("headers"), nil
	})
	body := portalBody(t, portalRequest(t, f.h, "GET", "/api/mcp/portal?virtual_key_id=vk-a", "", "alice"))
	assert.Equal(t, "alice", f.store.lastCaller)
	assert.Equal(t, schemas.MCPAuthModeVK, body.Identity.Mode)
	assert.Equal(t, "vk-a", body.Identity.ID)
	require.Len(t, body.Servers, 1)
	assert.Equal(t, "headers", body.Servers[0].ID)
	assert.Equal(t, fasthttp.StatusNotFound, portalRequest(t, f.h, "GET", "/api/mcp/portal?virtual_key_id=vk-b", "", "alice").Response.StatusCode())

	vk, err := f.store.ConfigStore.GetVirtualKey(context.Background(), "vk-a")
	require.NoError(t, err)
	vk.IsActive = bifrost.Ptr(false)
	require.NoError(t, f.store.UpdateVirtualKey(context.Background(), vk))
	assert.Equal(t, fasthttp.StatusForbidden, portalRequest(t, f.h, "GET", "/api/mcp/portal?virtual_key_id=vk-a", "", "alice").Response.StatusCode())
	assert.Equal(t, fasthttp.StatusForbidden, portalRequest(t, f.h, "POST", "/api/mcp/portal/headers/authenticate", `{"virtual_key_id":"vk-a"}`, "alice").Response.StatusCode())
	vk.IsActive = bifrost.Ptr(true)
	vk.ExpiresAt = bifrost.Ptr(time.Now().Add(-time.Minute))
	require.NoError(t, f.store.UpdateVirtualKey(context.Background(), vk))
	assert.Equal(t, fasthttp.StatusForbidden, portalRequest(t, f.h, "GET", "/api/mcp/portal?virtual_key_id=vk-a", "", "").Response.StatusCode())
}

func TestMCPPortalCredentialStatusesAreIdentityScoped(t *testing.T) {
	f := newPortalFixture(t)
	ctx := context.Background()
	for _, user := range []string{"alice", "bob"} {
		cred := &tables.TableMCPPerUserHeaderCredential{ID: "headers-" + user, AuthMode: "user", MCPClientID: "headers", UserID: bifrost.Ptr(user), Status: "active"}
		require.NoError(t, cred.SetHeaders(map[string]string{"x-api-key": "private-" + user}))
		require.NoError(t, f.store.UpsertMCPPerUserHeaderCredential(ctx, cred))
	}
	rows, err := f.store.ListMCPPerUserHeaderCredentials(ctx, configstore.MCPSessionsFilterParams{Identity: "alice"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	rows[0].Status = "needs_update"
	require.NoError(t, f.store.UpsertMCPPerUserHeaderCredential(ctx, &rows[0]))
	for _, tc := range []struct{ user, want string }{{"alice", "needs_update"}, {"bob", "ready"}, {"charlie", "missing"}} {
		result := portalRequest(t, f.h, "GET", "/api/mcp/portal", "", tc.user)
		body := portalBody(t, result)
		assert.Equal(t, tc.want, body.Servers[0].CredentialStatus)
		assert.NotContains(t, string(result.Response.Body()), "private-")
	}
	require.NoError(t, f.store.CreateOauthToken(ctx, &tables.TableMCPOauthToken{ID: "alice-token", OauthConfigID: "portal-oauth", MCPClientID: "oauth", AuthMode: "user", UserID: bifrost.Ptr("alice"), Status: "needs_reauth", AccessToken: "access-secret", RefreshToken: "refresh-secret"}))
	result := portalRequest(t, f.h, "GET", "/api/mcp/portal", "", "alice")
	assert.Equal(t, "needs_reauth", portalBody(t, result).Servers[1].CredentialStatus)
	assert.Equal(t, "missing", portalBody(t, portalRequest(t, f.h, "GET", "/api/mcp/portal", "", "bob")).Servers[1].CredentialStatus)
	assert.NotContains(t, string(result.Response.Body()), "access-secret")
	assert.NotContains(t, string(result.Response.Body()), "refresh-secret")
}

func TestMCPPortalAuthenticationBindsExistingFlows(t *testing.T) {
	f := newPortalFixture(t)
	for _, mode := range []schemas.MCPAuthMode{schemas.MCPAuthModeUser, schemas.MCPAuthModeVK} {
		for _, client := range []string{"oauth", "headers"} {
			payload, identity := `{}`, "alice"
			if mode == schemas.MCPAuthModeVK {
				payload, identity = `{"virtual_key_id":"vk-a"}`, "vk-a"
			}
			var firstID string
			for attempt := range 2 {
				result := portalRequest(t, f.h, "POST", "/api/mcp/portal/"+client+"/authenticate", payload, "alice")
				require.Equal(t, fasthttp.StatusOK, result.Response.StatusCode(), string(result.Response.Body()))
				var body map[string]string
				require.NoError(t, json.Unmarshal(result.Response.Body(), &body))
				require.Len(t, body, 1)
				parsed, err := url.Parse(body["auth_url"])
				require.NoError(t, err)
				assert.Equal(t, "/workspace/mcp-sessions/auth", parsed.Path)
				flowID := parsed.Query().Get("flow")
				require.NotEmpty(t, flowID)
				if attempt == 0 {
					firstID = flowID
				} else {
					assert.Equal(t, firstID, flowID, "reuse the provider's existing pending flow")
				}
				if client == "headers" {
					assert.Equal(t, "headers", parsed.Query().Get("kind"))
					flow, err := f.store.GetMCPPerUserHeaderFlowByID(context.Background(), flowID)
					require.NoError(t, err)
					require.NotNil(t, flow)
					assert.True(t, portalMatchesIdentity(&mcpPortalIdentity{Mode: mode, ID: identity}, flow.FlowMode, flow.UserID, flow.VirtualKeyID))
				} else {
					flow, err := f.store.GetOauthUserSessionByID(context.Background(), flowID)
					require.NoError(t, err)
					require.NotNil(t, flow)
					assert.True(t, portalMatchesIdentity(&mcpPortalIdentity{Mode: mode, ID: identity}, flow.FlowMode, flow.UserID, flow.VirtualKeyID))
				}
			}
		}
	}
	assert.Equal(t, fasthttp.StatusUnauthorized, portalRequest(t, f.h, "POST", "/api/mcp/portal/oauth/authenticate", `{}`, "").Response.StatusCode())
	assert.Equal(t, fasthttp.StatusForbidden, portalRequest(t, f.h, "POST", "/api/mcp/portal/denied/authenticate", `{}`, "alice").Response.StatusCode())
	assert.Equal(t, fasthttp.StatusConflict, portalRequest(t, f.h, "POST", "/api/mcp/portal/shared/authenticate", `{}`, "alice").Response.StatusCode())
	// A grant removed after a successful listing must be checked again at POST.
	f.h.resolver = portalResolverFunc(func(*schemas.BifrostContext) (schemas.Access, error) { return portalAccess(), nil })
	assert.Equal(t, fasthttp.StatusForbidden, portalRequest(t, f.h, "POST", "/api/mcp/portal/oauth/authenticate", `{}`, "alice").Response.StatusCode())
}

func TestMCPPortalAuthenticationRechecksPersistedConfiguration(t *testing.T) {
	f := newPortalFixture(t)
	body := portalBody(t, portalRequest(t, f.h, "GET", "/api/mcp/portal", "", "alice"))
	require.True(t, body.Servers[0].CanConfigure)
	row, err := f.store.GetMCPClientByID(context.Background(), "headers")
	require.NoError(t, err)
	row.Disabled = true
	require.NoError(t, f.store.UpdateMCPClientConfig(context.Background(), "headers", row))
	assert.Equal(t, fasthttp.StatusConflict, portalRequest(t, f.h, "POST", "/api/mcp/portal/headers/authenticate", `{}`, "alice").Response.StatusCode())
	flow, err := f.store.GetMCPPerUserHeaderFlowByModeIdentityAndMCPClient(context.Background(), schemas.MCPAuthModeUser, "alice", "headers")
	require.NoError(t, err)
	assert.Nil(t, flow, "disabled configuration must not mint a submission flow")
}

func TestMCPPortalConfigStatesAndResolverFailures(t *testing.T) {
	f := newPortalFixture(t)
	for _, state := range []schemas.MCPConnectionState{schemas.MCPConnectionStateDisabled, schemas.MCPConnectionStatePendingVerification, schemas.MCPConnectionStateError} {
		f.h.clients = portalClients{{Config: f.configs[0], State: state}, {Config: f.configs[1], State: schemas.MCPConnectionStateHealthy}}
		body := portalBody(t, portalRequest(t, f.h, "GET", "/api/mcp/portal", "", "alice"))
		assert.False(t, body.Servers[1].CanConfigure)
		assert.Equal(t, state, body.Servers[1].ConnectionState)
		assert.Equal(t, fasthttp.StatusConflict, portalRequest(t, f.h, "POST", "/api/mcp/portal/oauth/authenticate", `{}`, "alice").Response.StatusCode())
	}
	f.h.resolver = portalResolverFunc(func(*schemas.BifrostContext) (schemas.Access, error) { return nil, errors.New("resolver down") })
	assert.Equal(t, fasthttp.StatusInternalServerError, portalRequest(t, f.h, "GET", "/api/mcp/portal", "", "alice").Response.StatusCode())
	f.h.resolver = portalResolverFunc(func(*schemas.BifrostContext) (schemas.Access, error) { return nil, nil })
	assert.Equal(t, fasthttp.StatusForbidden, portalRequest(t, f.h, "GET", "/api/mcp/portal?virtual_key_id=vk-a", "", "alice").Response.StatusCode())
}

type portalGovernanceRegistry struct{ governance.InMemoryStore }

func (portalGovernanceRegistry) GetMCPClientsAllowedByDefault() map[string]string {
	return map[string]string{"headers": "headers", "denied": "denied"}
}
func (portalGovernanceRegistry) GetMCPClientNames() map[string]string {
	return map[string]string{"headers": "headers", "denied": "denied", "oauth": "oauth", "shared": "shared"}
}

func TestMCPPortalUsesEffectiveDefaultExplicitAndDeniedGrants(t *testing.T) {
	f := newPortalFixture(t)
	vk, err := f.store.GetVirtualKey(context.Background(), "vk-a")
	require.NoError(t, err)
	vk.MCPConfigs = []tables.TableVirtualKeyMCPConfig{
		{MCPClientID: 1, MCPClient: tables.TableMCPClient{ClientID: "oauth", Name: "oauth"}, ToolsToExecute: []string{"read"}},
		{MCPClientID: 2, MCPClient: tables.TableMCPClient{ClientID: "denied", Name: "denied"}, ToolsToExecute: []string{}},
	}
	local, err := governance.NewLocalGovernanceStore(context.Background(), bifrost.NewDefaultLogger(schemas.LogLevelError), nil, &configstore.GovernanceConfig{VirtualKeys: []tables.TableVirtualKey{*vk}}, nil, portalGovernanceRegistry{})
	require.NoError(t, err)
	f.h.resolver = portalResolverFunc(func(ctx *schemas.BifrostContext) (schemas.Access, error) {
		bases, scoping, mode := local.ResolvePermits(ctx)
		return grant.NewAccess(bases, scoping, mode, nil), nil
	})
	body := portalBody(t, portalRequest(t, f.h, "GET", "/api/mcp/portal?virtual_key_id=vk-a", "", ""))
	require.Len(t, body.Servers, 2)
	assert.Equal(t, "headers", body.Servers[0].ID, "default grant is visible")
	assert.Equal(t, "oauth", body.Servers[1].ID, "one explicitly granted tool makes the client visible")
	assert.Equal(t, fasthttp.StatusForbidden, portalRequest(t, f.h, "POST", "/api/mcp/portal/denied/authenticate", `{"virtual_key_id":"vk-a"}`, "").Response.StatusCode())
	local.CreateVirtualMCPInMemory(&tables.TableVirtualMCP{ID: 1, Name: "virtual", EndpointSlug: "virtual", Enabled: true, ParsedTools: []tables.MCPToolSpec{{MCPClientID: "shared", ToolNames: []string{"read"}}}})
	local.AttachVirtualMCPInMemory("vk-a", 1)
	body = portalBody(t, portalRequest(t, f.h, "GET", "/api/mcp/portal?virtual_key_id=vk-a", "", ""))
	require.Len(t, body.Servers, 3, "assigned virtual MCPs confer access through the same resolver")
	assert.Equal(t, "shared", body.Servers[2].ID)
}
