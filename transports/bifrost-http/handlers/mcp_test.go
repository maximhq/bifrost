package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/oauth2"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// TestMain points the OAuth discovery/registration client at an unguarded
// dialer for this package: the MCP OAuth tests here register against
// loopback-bound httptest.Server instances, which the production guard in
// framework/oauth2 refuses to dial. The guard has its own coverage there.
//
// It also installs the package logger before any test runs. Production sets
// it at startup, and code such as the WebSocket panic recovery logs through it
// unconditionally, so a test that reaches that code must not depend on an
// earlier test in the run having set it.
func TestMain(m *testing.M) {
	oauth2.SetDiscoveryDialContextForTests((&net.Dialer{}).DialContext)
	SetLogger(&mockLogger{})
	os.Exit(m.Run())
}

// =============================================================================
// POST /api/mcp/client/{id}/reregister and /reauthorize, driven through
// startMCPClientReauthorization against a real sqlite store, a real
// OAuth2Provider, and a fake RFC 7591 authorization server. Only the two
// collaborators whose calls are under test (the credential cache and the MCP
// manager) are fakes.
// =============================================================================

// reregisterAuthServer is the registration half of an authorization server,
// with the two answers the handler has to tell apart scripted in.
type reregisterAuthServer struct {
	mu            sync.Mutex
	registrations int
	refuseWith    int    // when non-zero, /register answers with this status
	reissue       string // when set, /register hands back this client_id
	server        *httptest.Server
}

func newReregisterAuthServer(t *testing.T) *reregisterAuthServer {
	t.Helper()
	as := &reregisterAuthServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/register", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		as.mu.Lock()
		if as.refuseWith != 0 {
			status := as.refuseWith
			as.mu.Unlock()
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client_metadata"})
			return
		}
		as.registrations++
		clientID := as.reissue
		if clientID == "" {
			clientID = fmt.Sprintf("dyn-client-%d", as.registrations)
		}
		as.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": clientID})
	})
	as.server = httptest.NewServer(mux)
	t.Cleanup(as.server.Close)
	return as
}

func (as *reregisterAuthServer) set(refuseWith int, reissue string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.refuseWith, as.reissue = refuseWith, reissue
}

func (as *reregisterAuthServer) registrationCount() int {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.registrations
}

// recordingCredentialCache embeds the interface so any eviction other than the
// one under test panics rather than passing unnoticed.
type recordingCredentialCache struct {
	MCPCredentialCacheManager
	evictedClients []string
}

func (c *recordingCredentialCache) EvictOauthTokenCacheByMCPClient(_ context.Context, mcpClientID string) {
	c.evictedClients = append(c.evictedClients, mcpClientID)
}

// recordingMCPManager embeds the interface for the same reason.
type recordingMCPManager struct {
	MCPManager
	closedClients []string
}

func (m *recordingMCPManager) CloseAndMarkNeedsReauth(_ context.Context, id string) error {
	m.closedClients = append(m.closedClients, id)
	return nil
}

// flowFailingStore is the real store with one failure scripted in: the flow row
// the consent step needs cannot be written. Everything before it, the rotation
// included, goes through to sqlite and commits for real.
type flowFailingStore struct {
	configstore.ConfigStore
	failFlowCreate bool
}

func (s *flowFailingStore) CreateOauthUserSession(ctx context.Context, session *configtables.TableMCPOauthFlow) error {
	if s.failFlowCreate {
		return errors.New("database is locked")
	}
	return s.ConfigStore.CreateOauthUserSession(ctx, session)
}

const reregisterMCPClientID = "mcp-grafana"

type reregisterFixture struct {
	handler            *MCPHandler
	store              *flowFailingStore
	as                 *reregisterAuthServer
	cache              *recordingCredentialCache
	manager            *recordingMCPManager
	oauthConfigID      string
	registeredClientID string
	lastCtx            *fasthttp.RequestCtx // the request the last call ran, for audit assertions
}

// newReregisterFixture bootstraps a shared-OAuth MCP client whose client_id was
// issued through dynamic registration, which is the only kind of client the
// reregister route has anything to do for.
func newReregisterFixture(t *testing.T) *reregisterFixture {
	t.Helper()
	SetLogger(&mockLogger{})
	ctx := context.Background()

	as := newReregisterAuthServer(t)
	store := &flowFailingStore{ConfigStore: newRealOAuth2Store(t)}
	provider := oauth2.NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))

	initiation, err := provider.InitiateOAuthFlow(ctx, &schemas.OAuth2Config{
		AuthorizeURL:    as.server.URL + "/authorize",
		TokenURL:        as.server.URL + "/token",
		RegistrationURL: bifrost.Ptr(as.server.URL + "/register"),
		RedirectURI:     "http://localhost:8080/api/oauth/callback",
		ServerURL:       as.server.URL,
		Scopes:          []string{"openid"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, as.registrationCount(), "bootstrap must dynamically register exactly one client")

	bootstrapped, err := store.GetOauthConfigByID(ctx, initiation.OauthConfigID)
	require.NoError(t, err)
	require.NotNil(t, bootstrapped)

	require.NoError(t, store.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID:               reregisterMCPClientID,
		Name:             "grafana",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(as.server.URL + "/mcp"),
		AuthType:         schemas.MCPAuthTypeOauth,
		OauthConfigID:    bifrost.Ptr(initiation.OauthConfigID),
	}))

	cache := &recordingCredentialCache{}
	manager := &recordingMCPManager{}
	cfg := &lib.Config{
		ConfigStore:   store,
		ClientConfig:  &configstore.ClientConfig{},
		OAuthProvider: provider,
	}
	return &reregisterFixture{
		handler:            &MCPHandler{store: cfg, mcpManager: manager, mcpCredentialCacheManager: cache},
		store:              store,
		as:                 as,
		cache:              cache,
		manager:            manager,
		oauthConfigID:      initiation.OauthConfigID,
		registeredClientID: bootstrapped.GetResolvedClientID(),
	}
}

// call runs one of the two routes and returns the status and decoded body.
func (f *reregisterFixture) call(t *testing.T, route func(*fasthttp.RequestCtx)) (int, map[string]any, string) {
	t.Helper()
	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodPost)
	req.Header.SetHost("bifrost.example.com")
	ctx := initCtx(&req)
	ctx.SetUserValue("id", reregisterMCPClientID)

	route(ctx)
	f.lastCtx = ctx

	raw := string(ctx.Response.Body())
	var body map[string]any
	_ = json.Unmarshal(ctx.Response.Body(), &body)
	return ctx.Response.StatusCode(), body, raw
}

func (f *reregisterFixture) storedClientID(t *testing.T) string {
	t.Helper()
	row, err := f.store.GetOauthConfigByID(context.Background(), f.oauthConfigID)
	require.NoError(t, err)
	require.NotNil(t, row)
	return row.GetResolvedClientID()
}

// TestReregisterMCPClient_InvalidatesInMemoryStateWhenItRotates pins that a
// rotation through this route leaves nothing in memory still serving the
// credentials it just invalidated.
//
// RotateMCPOAuthConfig cascades every bound token to needs_reauth in the
// database, but two things outlive that: the OAuth provider's token cache, which
// GetUserAccessTokenByMode answers from before it ever reads a row, and a
// shared client's live connection, which has the old bearer baked into its
// transport. The update-client route has always dropped both after rotating;
// this route rotates through the same store call and has to as well. Eviction
// goes through the handler's cache manager rather than the provider directly
// because a clustered deployment overrides it to notify peers.
func TestReregisterMCPClient_InvalidatesInMemoryStateWhenItRotates(t *testing.T) {
	f := newReregisterFixture(t)

	status, body, raw := f.call(t, f.handler.reregisterMCPClient)
	require.Equal(t, fasthttp.StatusOK, status, raw)
	require.NotEqual(t, f.registeredClientID, body["registered_client_id"], "fixture check: a replacement was issued")

	assert.Equal(t, []string{reregisterMCPClientID}, f.cache.evictedClients,
		"cached tokens issued under the replaced client must be evicted, or they keep being served over a needs_reauth row")
	assert.Equal(t, []string{reregisterMCPClientID}, f.manager.closedClients,
		"the live connection still carries the replaced client's bearer and must be closed")
}

// TestReregisterMCPClient_LeavesInMemoryStateAloneWhenNothingRotated is the
// other side of the gate: a provider that hands back the registration it
// already holds invalidated nothing, so there is nothing to tear down, and
// closing a working connection over it would be self-inflicted.
func TestReregisterMCPClient_LeavesInMemoryStateAloneWhenNothingRotated(t *testing.T) {
	f := newReregisterFixture(t)
	f.as.set(0, f.registeredClientID)

	status, body, raw := f.call(t, f.handler.reregisterMCPClient)
	require.Equal(t, fasthttp.StatusOK, status, raw)
	require.Equal(t, f.registeredClientID, body["registered_client_id"], "fixture check: the provider re-issued the same client")

	assert.Empty(t, f.cache.evictedClients)
	assert.Empty(t, f.manager.closedClients)
}

// TestReauthorizeMCPClient_NeverInvalidatesInMemoryState keeps the new
// invalidation on its own side of the route split: plain reauthorize rotates
// nothing and must not start behaving as though it had.
func TestReauthorizeMCPClient_NeverInvalidatesInMemoryState(t *testing.T) {
	f := newReregisterFixture(t)

	status, _, raw := f.call(t, f.handler.reauthorizeMCPClient)
	require.Equal(t, fasthttp.StatusOK, status, raw)

	assert.Equal(t, 1, f.as.registrationCount(), "plain reauthorize must not register anything")
	assert.Empty(t, f.cache.evictedClients)
	assert.Empty(t, f.manager.closedClients)
}

// TestReregisterMCPClient_ReportsACommittedRotationWhenConsentSetupFails pins
// what the caller is told when the route half-succeeds.
//
// Registration and rotation commit before the consent flow is opened, and the
// upstream registration cannot be rolled back, so a failure opening the flow
// leaves the replacement client installed. A bare "failed to initiate
// reauthorization" reads as "nothing happened, try again", and trying again
// registers yet another client. The response has to say the rotation is done,
// name both ids, and point at the route that finishes the job without
// registering anything.
func TestReregisterMCPClient_ReportsACommittedRotationWhenConsentSetupFails(t *testing.T) {
	f := newReregisterFixture(t)
	f.store.failFlowCreate = true

	status, _, raw := f.call(t, f.handler.reregisterMCPClient)

	replacement := f.storedClientID(t)
	require.NotEqual(t, f.registeredClientID, replacement, "fixture check: the rotation committed before the failure")

	assert.Equal(t, fasthttp.StatusInternalServerError, status)
	assert.Contains(t, raw, replacement, "must name the client_id that is now installed")
	assert.Contains(t, raw, f.registeredClientID, "must name the client_id it replaced")
	assert.Contains(t, raw, "reauthorize", "must point at the route that completes consent without registering another client")
	assert.Contains(t, raw, "database is locked", "must still carry the underlying cause")

	assert.Equal(t, []string{reregisterMCPClientID}, f.cache.evictedClients,
		"the rotation committed, so its invalidation must have run regardless of what failed after it")
}

func TestReregisterMCPClient_AuditsCommittedRotationWhenConsentSetupFails(t *testing.T) {
	f := newReregisterFixture(t)
	f.store.failFlowCreate = true

	f.call(t, f.handler.reregisterMCPClient)

	change := requireSingleAuditChange(t, f.lastCtx, "mcp_client.reregistered")
	assert.Equal(t, reregisterMCPClientID, change.TargetID)
}

// TestReauthorizeMCPClient_ConsentSetupFailure_ClaimsNoRotation is the
// counterpart: with nothing committed there is nothing to report, and the
// plain failure message stays what it was.
func TestReauthorizeMCPClient_ConsentSetupFailure_ClaimsNoRotation(t *testing.T) {
	f := newReregisterFixture(t)
	f.store.failFlowCreate = true

	status, _, raw := f.call(t, f.handler.reauthorizeMCPClient)

	assert.Equal(t, fasthttp.StatusInternalServerError, status)
	assert.Contains(t, raw, "Failed to initiate reauthorization")
	assert.NotContains(t, raw, "registered")
}

// TestReregisterMCPClient_StatusSaysWhoseProblemItIs pins the status each
// registration failure is reported under. 400 tells the caller its request can
// be changed to succeed, which is true of exactly two failures: there is no
// registration endpoint to ask (set one, or reauthorize instead), and the
// provider understood the request and refused it. A provider outage is neither,
// and reporting it as the caller's mistake sends them to fix a request that was
// never wrong.
func TestReregisterMCPClient_StatusSaysWhoseProblemItIs(t *testing.T) {
	t.Run("no registration endpoint is the caller's to fix", func(t *testing.T) {
		f := newReregisterFixture(t)
		ctx := context.Background()
		row, err := f.store.GetOauthConfigByID(ctx, f.oauthConfigID)
		require.NoError(t, err)
		row.RegistrationURL = nil
		require.NoError(t, f.store.UpdateOauthConfig(ctx, row))

		status, _, raw := f.call(t, f.handler.reregisterMCPClient)
		assert.Equal(t, fasthttp.StatusBadRequest, status, raw)
		assert.Contains(t, raw, "registration endpoint")
	})

	t.Run("a provider refusal is the caller's to fix", func(t *testing.T) {
		f := newReregisterFixture(t)
		f.as.set(http.StatusForbidden, "")

		status, _, raw := f.call(t, f.handler.reregisterMCPClient)
		assert.Equal(t, fasthttp.StatusBadRequest, status, raw)
		assert.Contains(t, raw, "status 403")
	})

	t.Run("a provider outage is not", func(t *testing.T) {
		f := newReregisterFixture(t)
		f.as.set(http.StatusServiceUnavailable, "")

		status, _, raw := f.call(t, f.handler.reregisterMCPClient)
		assert.Equal(t, fasthttp.StatusInternalServerError, status, raw)
		assert.Contains(t, raw, "status 503")
		assert.Equal(t, f.registeredClientID, f.storedClientID(t), "a failed attempt must leave the stored client alone")
		assert.Empty(t, f.cache.evictedClients, "nothing rotated, so nothing may be invalidated")
	})
}

// =============================================================================
// POST /api/mcp/client with auth_type oauth against an OAuth-protected MCP
// server on loopback, with the production OAuth dialer.
// =============================================================================

// newLoopbackOAuthMCPServer serves the RFC 8414 discovery document and RFC 7591
// registration an OAuth-protected MCP server on the operator's own host
// exposes (the shape of examples/mcps/oauth-demo-server).
func newLoopbackOAuthMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 server.URL,
			"authorization_endpoint": server.URL + "/authorize",
			"token_endpoint":         server.URL + "/token",
			"registration_endpoint":  server.URL + "/register",
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "loopback-client"})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestAddMCPClient_OAuthOnLoopbackFollowsPrivateAllowlist pins the fix for MCP
// OAuth servers on localhost: once the OAuth chain was put behind the
// public-only dialer, an authenticated admin could not add one at all. With
// the server's host on mcp_oauth_private_allowlist the admin's registration
// discovers, registers and returns an authorize URL; without it the chain is
// still refused, and a caller with auth bypassed is refused regardless.
func TestAddMCPClient_OAuthOnLoopbackFollowsPrivateAllowlist(t *testing.T) {
	oauth2.SetDiscoveryDialContextForTests(nil)
	t.Cleanup(func() {
		oauth2.SetDiscoveryDialContextForTests((&net.Dialer{}).DialContext)
		require.NoError(t, lib.ApplyMCPOAuthPrivateAllowlist(nil))
	})

	for _, tc := range []struct {
		name         string
		allow        []string
		authBypassed bool
		wantStatus   int
		wantBody     string
	}{
		{name: "authenticated, loopback allowlisted", allow: []string{"127.0.0.1"}, wantStatus: fasthttp.StatusOK, wantBody: "authorize_url"},
		{name: "authenticated, no allowlist", wantStatus: fasthttp.StatusInternalServerError, wantBody: "blocked connection to non-public address"},
		{name: "auth bypassed, loopback allowlisted", allow: []string{"127.0.0.1"}, authBypassed: true, wantStatus: fasthttp.StatusForbidden, wantBody: "unauthenticated callers cannot register MCP clients"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetLogger(&mockLogger{})
			require.NoError(t, lib.ApplyMCPOAuthPrivateAllowlist(tc.allow))
			server := newLoopbackOAuthMCPServer(t)
			store := newRealOAuth2Store(t)
			provider := oauth2.NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))
			cfg := &lib.Config{ConfigStore: store, ClientConfig: &configstore.ClientConfig{}, OAuthProvider: provider}
			h := &MCPHandler{store: cfg, mcpManager: failingMCPManager{}, oauthHandler: NewOAuthHandler(provider, nil, cfg)}

			var req fasthttp.Request
			req.Header.SetMethod(fasthttp.MethodPost)
			req.Header.SetContentType("application/json")
			req.Header.SetHost("bifrost.example.com")
			req.SetBodyString(`{"name":"local_oauth","connection_type":"http","connection_string":"` + server.URL + `/mcp","auth_type":"oauth","oauth_config":{}}`)
			ctx := initCtx(&req)
			if tc.authBypassed {
				ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
			}
			h.addMCPClient(ctx)

			body := string(ctx.Response.Body())
			require.Equal(t, tc.wantStatus, ctx.Response.StatusCode(), body)
			assert.Contains(t, body, tc.wantBody)
		})
	}
}

// removeRecordingMCPManager records RemoveMCPClient calls; any other manager call panics.
type removeRecordingMCPManager struct {
	MCPManager
	removed []string
}

func (m *removeRecordingMCPManager) RemoveMCPClient(_ context.Context, id string) error {
	m.removed = append(m.removed, id)
	return nil
}

// TestDeleteMCPClient_UnknownIDIsNotFound pins that deleting an MCP client id with
// no row answers 404 and leaves the runtime manager untouched, while a known id is
// still removed from both the store and the manager.
func TestDeleteMCPClient_UnknownIDIsNotFound(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	manager := &removeRecordingMCPManager{}
	h := &MCPHandler{store: &lib.Config{ConfigStore: store, ClientConfig: &configstore.ClientConfig{}}, mcpManager: manager}
	del := func(id string) (int, string) {
		var req fasthttp.Request
		req.Header.SetMethod(fasthttp.MethodDelete)
		ctx := initCtx(&req)
		ctx.SetUserValue("id", id)
		h.deleteMCPClient(ctx)
		return ctx.Response.StatusCode(), string(ctx.Response.Body())
	}

	status, body := del("3f8e2a1c-0000-4000-8000-000000000000")
	assert.Equal(t, fasthttp.StatusNotFound, status, body)
	assert.Contains(t, body, "not found")
	assert.Empty(t, manager.removed, "an unknown id must not reach the runtime manager")

	require.NoError(t, store.CreateMCPClientConfig(context.Background(), &schemas.MCPClientConfig{
		ID:               "mcp-delete-known",
		Name:             "delete_known",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"),
	}))
	status, body = del("mcp-delete-known")
	assert.Equal(t, fasthttp.StatusOK, status, body)
	assert.Equal(t, []string{"mcp-delete-known"}, manager.removed)
	_, err := store.GetMCPClientByID(context.Background(), "mcp-delete-known")
	assert.ErrorIs(t, err, configstore.ErrNotFound)
}

// =============================================================================
// GET /api/mcp/library installed flag
// =============================================================================

// libraryInstalledStore serves one library page and a client table of any size,
// honoring the limit/offset the handler asks for.
type libraryInstalledStore struct {
	configstore.ConfigStore
	entries     []configtables.TableMCPLibrary
	clients     []configtables.TableMCPClient
	clientPages int
}

func (s *libraryInstalledStore) GetMCPLibraryPaginated(_ context.Context, _ configstore.MCPLibraryQueryParams) ([]configtables.TableMCPLibrary, int64, error) {
	return s.entries, int64(len(s.entries)), nil
}

func (s *libraryInstalledStore) GetMCPClientsPaginated(_ context.Context, params configstore.MCPClientsQueryParams) ([]configtables.TableMCPClient, int64, error) {
	s.clientPages++
	if params.Offset >= len(s.clients) {
		return nil, int64(len(s.clients)), nil
	}
	end := min(params.Offset+params.Limit, len(s.clients))
	return s.clients[params.Offset:end], int64(len(s.clients)), nil
}

func fillerMCPClients(n int) []configtables.TableMCPClient {
	clients := make([]configtables.TableMCPClient, n)
	for i := range clients {
		clients[i] = configtables.TableMCPClient{
			ClientID:         fmt.Sprintf("filler-%d", i),
			Name:             fmt.Sprintf("filler_%d", i),
			EndpointSlug:     fmt.Sprintf("filler-%d", i),
			ConnectionType:   string(schemas.MCPConnectionTypeHTTP),
			ConnectionString: schemas.NewSecretVar(fmt.Sprintf("https://filler.example.com/%d", i)),
		}
	}
	return clients
}

func TestGetMCPLibrary_InstalledLooksPastFirstHundredClients(t *testing.T) {
	clients := append(fillerMCPClients(250), configtables.TableMCPClient{
		ClientID:         "context7",
		Name:             "Context7",
		EndpointSlug:     "context7",
		ConnectionType:   string(schemas.MCPConnectionTypeHTTP),
		ConnectionString: schemas.NewSecretVar("https://mcp.context7.com/mcp"),
	})
	store := &libraryInstalledStore{
		entries: []configtables.TableMCPLibrary{
			{Slug: "context7", Name: "Context7", ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionURL: "https://mcp.context7.com/mcp"},
			{Slug: "linear", Name: "Linear", ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionURL: "https://mcp.linear.app/mcp"},
		},
		clients: clients,
	}
	h := &MCPHandler{store: &lib.Config{ConfigStore: store}}

	ctx := &fasthttp.RequestCtx{}
	h.getMCPLibrary(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	var resp struct {
		Servers []struct {
			Slug      string `json:"slug"`
			Installed bool   `json:"installed"`
		} `json:"servers"`
	}
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &resp))
	require.Len(t, resp.Servers, 2)
	assert.Equal(t, "context7", resp.Servers[0].Slug)
	assert.True(t, resp.Servers[0].Installed, "client #251 installs context7")
	assert.Equal(t, "linear", resp.Servers[1].Slug)
	assert.False(t, resp.Servers[1].Installed)
	assert.Equal(t, 3, store.clientPages, "251 clients read in pages of 100")
}

func TestIsMCPLibraryEntryInstalled(t *testing.T) {
	httpEntry := configtables.TableMCPLibrary{
		Name:           "Context7",
		ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionURL:  "https://mcp.context7.com/mcp",
	}
	stdioEntry := configtables.TableMCPLibrary{
		Name:           "File System",
		ConnectionType: schemas.MCPConnectionTypeSTDIO,
		StdioConfig:    &schemas.MCPStdioConfig{Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem"}},
	}
	cases := []struct {
		name   string
		entry  configtables.TableMCPLibrary
		client configtables.TableMCPClient
		want   bool
	}{
		{
			name:   "http renamed client still matches by url",
			entry:  httpEntry,
			client: configtables.TableMCPClient{Name: "docs_lookup", EndpointSlug: "context7", ConnectionString: schemas.NewSecretVar("https://mcp.context7.com/mcp")},
			want:   true,
		},
		{
			name:   "http url with trailing slash matches",
			entry:  httpEntry,
			client: configtables.TableMCPClient{Name: "docs_lookup", EndpointSlug: "docs-lookup", ConnectionString: schemas.NewSecretVar("https://mcp.context7.com/mcp/")},
			want:   true,
		},
		{
			name:   "stdio renamed client matches by immutable endpoint slug",
			entry:  stdioEntry,
			client: configtables.TableMCPClient{Name: "my_files", EndpointSlug: "file-system", StdioConfig: &schemas.MCPStdioConfig{Command: "npx", Args: []string{"-y", "other"}}},
			want:   true,
		},
		{
			name:   "stdio renamed client matches by command and args",
			entry:  stdioEntry,
			client: configtables.TableMCPClient{Name: "my_files", EndpointSlug: "my-files", StdioConfig: &schemas.MCPStdioConfig{Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem"}}},
			want:   true,
		},
		{
			name:   "stdio client with same command but other args does not match",
			entry:  stdioEntry,
			client: configtables.TableMCPClient{Name: "my_files", EndpointSlug: "my-files", StdioConfig: &schemas.MCPStdioConfig{Command: "npx", Args: []string{"-y", "other"}}},
			want:   false,
		},
		{
			name:   "name match is case-insensitive",
			entry:  stdioEntry,
			client: configtables.TableMCPClient{Name: "file_system", EndpointSlug: "custom"},
			want:   true,
		},
		{
			name:   "unrelated client does not match",
			entry:  httpEntry,
			client: configtables.TableMCPClient{Name: "other", EndpointSlug: "other", ConnectionString: schemas.NewSecretVar("https://other.example.com/mcp")},
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isMCPLibraryEntryInstalled(&tc.entry, []configtables.TableMCPClient{tc.client})
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSanitizeMCPLibraryServerName(t *testing.T) {
	cases := map[string]string{
		"Context7":         "Context7",
		"Maxim AI":         "Maxim_AI",
		"  File - System ": "File_System",
		"a-_b":             "a__b",
		"-lead":            "lead",
		"1Password":        "mcp_1Password",
		"***":              "mcp_",
	}
	for in, want := range cases {
		assert.Equal(t, want, sanitizeMCPLibraryServerName(in), in)
	}
}
