package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/mcp_headers"
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
	server, _ := newLoopbackOAuthMCPServerWithCIMD(t, false)
	return server
}

// newLoopbackOAuthMCPServerWithCIMD is newLoopbackOAuthMCPServer, optionally advertising
// Client ID Metadata Document support. It counts requests to its OAuth endpoints.
func newLoopbackOAuthMCPServerWithCIMD(t *testing.T, supportsCIMD bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var oauthRequests atomic.Int32
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		oauthRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                server.URL,
			"authorization_endpoint":                server.URL + "/authorize",
			"token_endpoint":                        server.URL + "/token",
			"registration_endpoint":                 server.URL + "/register",
			"client_id_metadata_document_supported": supportsCIMD,
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, _ *http.Request) {
		oauthRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "loopback-client"})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, &oauthRequests
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

// Across the shared and per-user auth types, OAuth ones present Bifrost's Client ID Metadata
// Document as client_id when the upstream supports it (no dynamic registration), and header ones
// never contact the authorization server.
func TestAddMCPClient_OAuthUsesClientIDMetadataDocument(t *testing.T) {
	oauth2.SetDiscoveryDialContextForTests(nil)
	t.Cleanup(func() {
		oauth2.SetDiscoveryDialContextForTests((&net.Dialer{}).DialContext)
		require.NoError(t, lib.ApplyMCPOAuthPrivateAllowlist(nil))
	})
	require.NoError(t, lib.ApplyMCPOAuthPrivateAllowlist([]string{"127.0.0.1"}))
	const documentURL = "https://bifrost.example.com/api/oauth/client-metadata.json"

	for _, tc := range []struct {
		authType   string
		extra      string // extra request fields
		wantStatus int    // header types stop at the stub manager's connect
		wantCIMD   bool
	}{
		{authType: "oauth", extra: `,"oauth_config":{}`, wantStatus: fasthttp.StatusOK, wantCIMD: true},
		{authType: "per_user_oauth", extra: `,"oauth_config":{}`, wantStatus: fasthttp.StatusOK, wantCIMD: true},
		{authType: "headers", extra: `,"headers":{"Authorization":"Bearer shared"}`, wantStatus: fasthttp.StatusInternalServerError},
		{authType: "per_user_headers", extra: `,"per_user_header_keys":["Authorization"],"user_headers":{"Authorization":"Bearer sample"}`, wantStatus: fasthttp.StatusUnprocessableEntity},
	} {
		t.Run(tc.authType, func(t *testing.T) {
			SetLogger(&mockLogger{})
			server, oauthRequests := newLoopbackOAuthMCPServerWithCIMD(t, true)
			store := newRealOAuth2Store(t)
			provider := oauth2.NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))
			cfg := &lib.Config{ConfigStore: store, OAuthProvider: provider, ClientConfig: &configstore.ClientConfig{
				MCPExternalClientURL: schemas.NewSecretVar("https://bifrost.example.com"),
			}}
			h := &MCPHandler{store: cfg, mcpManager: failingMCPManager{}, oauthHandler: NewOAuthHandler(provider, nil, cfg)}

			var req fasthttp.Request
			req.Header.SetMethod(fasthttp.MethodPost)
			req.Header.SetContentType("application/json")
			req.Header.SetHost("bifrost.example.com")
			req.SetBodyString(`{"name":"cimd_` + tc.authType + `","connection_type":"http","connection_string":"` + server.URL + `/mcp","auth_type":"` + tc.authType + `"` + tc.extra + `}`)
			ctx := initCtx(&req)
			h.addMCPClient(ctx)

			body := string(ctx.Response.Body())
			require.Equal(t, tc.wantStatus, ctx.Response.StatusCode(), body)
			if !tc.wantCIMD {
				assert.Contains(t, body, "manager must not be reached", "reaches the connection step")
				assert.NotContains(t, body, "authorize_url")
				assert.Zero(t, oauthRequests.Load(), "header auth never contacts the authorization server")
				return
			}
			var resp struct {
				AuthorizeURL string `json:"authorize_url"`
			}
			require.NoError(t, json.Unmarshal(ctx.Response.Body(), &resp))
			authorize, err := url.Parse(resp.AuthorizeURL)
			require.NoError(t, err)
			assert.Equal(t, documentURL, authorize.Query().Get("client_id"))
			assert.Equal(t, int32(1), oauthRequests.Load(), "metadata fetched once, no dynamic registration")
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

// TestDeleteMCPClient_ReloadsTheKeysThatGrantedIt pins that deleting a client reloads every virtual key
// that granted it. The delete cascades over those grants in the store, but the keys cached in memory
// kept them, so a client created again under the same client_id was granted to them once more.
func TestDeleteMCPClient_ReloadsTheKeysThatGrantedIt(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := context.Background()
	store := newRealOAuth2Store(t)
	require.NoError(t, store.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID:               "mcp-delete-granted",
		Name:             "delete_granted",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"),
	}))
	row, err := store.GetMCPClientByID(ctx, "mcp-delete-granted")
	require.NoError(t, err)
	active := true
	for _, id := range []string{"vk-grant-a", "vk-grant-b", "vk-no-grant"} {
		require.NoError(t, store.CreateVirtualKey(ctx, &configtables.TableVirtualKey{ID: id, Name: id, Value: *schemas.NewSecretVar("sk-bf-" + id), IsActive: &active}))
	}
	for _, id := range []string{"vk-grant-a", "vk-grant-b"} {
		require.NoError(t, store.CreateVirtualKeyMCPConfig(ctx, &configtables.TableVirtualKeyMCPConfig{VirtualKeyID: id, MCPClientID: row.ID, ToolsToExecute: []string{"*"}}))
	}

	governance := &reloadCountingGovernanceManager{}
	h := &MCPHandler{store: &lib.Config{ConfigStore: store, ClientConfig: &configstore.ClientConfig{}}, mcpManager: &removeRecordingMCPManager{}, governanceManager: governance}
	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodDelete)
	reqCtx := initCtx(&req)
	reqCtx.SetUserValue("id", "mcp-delete-granted")
	h.deleteMCPClient(reqCtx)
	require.Equal(t, fasthttp.StatusOK, reqCtx.Response.StatusCode(), string(reqCtx.Response.Body()))

	var reloaded []string
	for _, batch := range governance.reloadedBatches {
		reloaded = append(reloaded, batch...)
	}
	reloaded = append(reloaded, governance.reloaded...)
	sort.Strings(reloaded)
	assert.Equal(t, []string{"vk-grant-a", "vk-grant-b"}, reloaded, "exactly the keys that granted the client are reloaded")
	remaining, err := store.GetVirtualKeyMCPConfigsByMCPClientID(ctx, row.ID)
	require.NoError(t, err)
	assert.Empty(t, remaining, "the grants are gone from the store, so the reload drops them from memory")
}

// grantReadFailingStore is a config store whose read of a client's virtual key grants fails.
type grantReadFailingStore struct{ configstore.ConfigStore }

// GetVirtualKeyMCPConfigsByMCPClientID always fails.
func (grantReadFailingStore) GetVirtualKeyMCPConfigsByMCPClientID(context.Context, uint) ([]configtables.TableVirtualKeyMCPConfig, error) {
	return nil, errors.New("database unavailable")
}

// TestDeleteMCPClient_RefusesWhenItsGrantsCannotBeRead pins that a delete whose granting keys cannot
// be read deletes nothing. It used to go ahead with no keys to reload: the grants left the store but
// stayed cached on the keys, where they applied again to a client created later under the same id.
func TestDeleteMCPClient_RefusesWhenItsGrantsCannotBeRead(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := context.Background()
	store := newRealOAuth2Store(t)
	require.NoError(t, store.CreateMCPClientConfig(ctx, &schemas.MCPClientConfig{
		ID:               "mcp-delete-unread",
		Name:             "delete_unread",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"),
	}))
	mgr := &removeRecordingMCPManager{}
	h := &MCPHandler{store: &lib.Config{ConfigStore: grantReadFailingStore{store}, ClientConfig: &configstore.ClientConfig{}}, mcpManager: mgr, governanceManager: &reloadCountingGovernanceManager{}}
	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodDelete)
	reqCtx := initCtx(&req)
	reqCtx.SetUserValue("id", "mcp-delete-unread")
	h.deleteMCPClient(reqCtx)

	assert.Equal(t, fasthttp.StatusInternalServerError, reqCtx.Response.StatusCode(), string(reqCtx.Response.Body()))
	assert.Contains(t, string(reqCtx.Response.Body()), "nothing was deleted")
	_, err := store.GetMCPClientByID(ctx, "mcp-delete-unread")
	assert.NoError(t, err, "the client is still stored")
	assert.Empty(t, mgr.removed, "the runtime client is untouched")
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

// The use_client_metadata_document toggle reaches the provider on every bootstrap path: the create
// request (through InitiateOAuthFlow) and a config.json client's stored pending config.
func TestUseClientMetadataDocument_ReachesTheProvider(t *testing.T) {
	handler := NewOAuthHandler(oauth2.NewOAuth2Provider(nil, bifrost.NewDefaultLogger(schemas.LogLevelError)), nil, nil)
	_, err := handler.InitiateOAuthFlow(context.Background(), OAuthInitiationRequest{
		ClientID:                  schemas.NewSecretVar("preset"),
		ClientMetadataURL:         "https://bifrost.example.com/api/oauth/client-metadata.json",
		UseClientMetadataDocument: true,
	})
	require.ErrorIs(t, err, oauth2.ErrClientMetadataDocumentWithClientID, "the toggle was dropped before the provider")

	req := pendingOAuthConfigToRequest(&schemas.OAuth2Config{UseClientMetadataDocument: true})
	assert.True(t, req.UseClientMetadataDocument, "a config.json client's toggle survives to initiate-verification")
}

// A toggle the caller set wrongly is a 400, not a server error.
func TestOAuthBootstrapErrorStatus(t *testing.T) {
	assert.Equal(t, fasthttp.StatusBadRequest, oauthBootstrapErrorStatus(fmt.Errorf("x: %w", oauth2.ErrClientMetadataDocumentWithClientID)))
	assert.Equal(t, fasthttp.StatusBadRequest, oauthBootstrapErrorStatus(fmt.Errorf("x: %w", oauth2.ErrClientMetadataDocumentUnavailable)))
	assert.Equal(t, fasthttp.StatusInternalServerError, oauthBootstrapErrorStatus(errors.New("discovery failed")))
}

// capturingAddMCPManager records the config the add handler hands the manager, then stops the add.
type capturingAddMCPManager struct {
	failingMCPManager
	got *schemas.MCPClientConfig
}

func (m *capturingAddMCPManager) AddMCPClient(_ context.Context, config *schemas.MCPClientConfig) error {
	m.got = config
	return errors.New("stop after capture")
}

func (m *capturingAddMCPManager) VerifyHeadersConnection(_ context.Context, config *schemas.MCPClientConfig, _ map[string]string) (*schemas.MCPDiscovery, error) {
	m.got = config
	return nil, errors.New("stop after capture")
}

// A new MCP client follows the global max protocol version unless the caller sets one; an
// unsupported value is rejected before anything is dialed.
func TestAddMCPClient_MaxProtocolVersion(t *testing.T) {
	for _, tc := range []struct {
		name, field, want string
		wantStatus        int
	}{
		{"omitted follows the global setting", ``, "", fasthttp.StatusInternalServerError},
		{"explicit cap is kept", `,"max_protocol_version":"2025-11-25"`, schemas.MCPProtocolVersion20251125, fasthttp.StatusInternalServerError},
		{"unsupported value rejected", `,"max_protocol_version":"2024-11-05"`, "", fasthttp.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetLogger(&mockLogger{})
			store := newRealOAuth2Store(t)
			manager := &capturingAddMCPManager{}
			h := &MCPHandler{store: &lib.Config{ConfigStore: store, ClientConfig: &configstore.ClientConfig{}}, mcpManager: manager}
			var req fasthttp.Request
			req.Header.SetMethod(fasthttp.MethodPost)
			req.Header.SetContentType("application/json")
			req.SetBodyString(`{"name":"capped","connection_type":"http","connection_string":"https://mcp.example.com/mcp","auth_type":"headers","headers":{"Authorization":"Bearer x"}` + tc.field + `}`)
			ctx := initCtx(&req)
			h.addMCPClient(ctx)

			require.Equal(t, tc.wantStatus, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			if tc.wantStatus == fasthttp.StatusBadRequest {
				assert.Nil(t, manager.got, "rejected before dialing")
				assert.Contains(t, string(ctx.Response.Body()), "max_protocol_version")
				return
			}
			require.NotNil(t, manager.got)
			assert.Equal(t, tc.want, manager.got.MaxProtocolVersion)
		})
	}
}

// capturingUpdateMCPManager records the config a live update hands the manager.
type capturingUpdateMCPManager struct {
	fakeMCPManagerVerifyOnly
	got *schemas.MCPClientConfig
}

func (m *capturingUpdateMCPManager) UpdateMCPClient(_ context.Context, _ string, config *schemas.MCPClientConfig) error {
	m.got = config
	return nil
}

// capturingUpdateConfigStore records the row the update handler writes.
type capturingUpdateConfigStore struct {
	mockUpdateConfigStore
	row *configtables.TableMCPClient
}

func (s *capturingUpdateConfigStore) UpdateMCPClientConfig(_ context.Context, _ string, row *configtables.TableMCPClient) error {
	s.row = row
	return nil
}

// An update sets, keeps or rejects the per-client max protocol version, and both the stored row and
// the live config get the result.
func TestUpdateMCPClient_MaxProtocolVersion(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		want       string
		wantStatus int
	}{
		{"set", `{"max_protocol_version":"2025-06-18"}`, schemas.MCPProtocolVersion20250618, fasthttp.StatusOK},
		{"back to the global setting", `{"max_protocol_version":""}`, "", fasthttp.StatusOK},
		{"omitted keeps the current value", `{"is_code_mode_client":false}`, schemas.MCPProtocolVersion20251125, fasthttp.StatusOK},
		{"unsupported value rejected", `{"max_protocol_version":"latest"}`, "", fasthttp.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetLogger(&mockLogger{})
			existing := &schemas.MCPClientConfig{
				ID: "client-1", Name: "capped", ConnectionType: schemas.MCPConnectionTypeHTTP, AuthType: schemas.MCPAuthTypeNone,
				MaxProtocolVersion: schemas.MCPProtocolVersion20251125,
			}
			cfg := &lib.Config{MCPConfig: &schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{existing}}, ClientConfig: &configstore.ClientConfig{}}
			store := &capturingUpdateConfigStore{}
			cfg.ConfigStore = store
			manager := &capturingUpdateMCPManager{}
			h := &MCPHandler{store: cfg, mcpManager: manager}
			ctx := &fasthttp.RequestCtx{}
			ctx.SetUserValue("id", existing.ID)
			ctx.Request.SetBodyString(tc.body)
			h.updateMCPClient(ctx)

			require.Equal(t, tc.wantStatus, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			if tc.wantStatus != fasthttp.StatusOK {
				assert.Nil(t, store.row, "nothing stored")
				assert.Nil(t, manager.got, "nothing applied")
				return
			}
			require.NotNil(t, store.row)
			require.NotNil(t, manager.got)
			assert.Equal(t, tc.want, store.row.MaxProtocolVersion, "stored")
			assert.Equal(t, tc.want, manager.got.MaxProtocolVersion, "applied live")
		})
	}
}

// An ordinary settings edit of a client with saved discovery keeps its saved resources, prompts and
// instructions: the edit carries the saved tools forward, and that is not a new discovery.
func TestUpdateMCPClient_SettingsEditKeepsSavedSnapshot(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := context.Background()
	store := newTestConfigStore(t)
	existing := &schemas.MCPClientConfig{
		ID: "client-1", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP, AuthType: schemas.MCPAuthTypeNone,
		ConnectionString: schemas.NewSecretVar("https://example.invalid/mcp"),
	}
	require.NoError(t, store.CreateMCPClientConfig(ctx, existing))
	require.NoError(t, store.UpdateMCPClientDiscovery(ctx, existing.ID, &schemas.MCPDiscovery{
		Tools:        map[string]schemas.ChatTool{"docs-search": {Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "docs-search"}}},
		Instructions: "Use the handbook first.",
		Resources:    []mcpgo.Resource{mcpgo.NewResource("file:///handbook.md", "handbook")},
		Prompts:      []mcpgo.Prompt{mcpgo.NewPrompt("docs-review")},
	}))
	cfg := &lib.Config{MCPConfig: &schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{existing}}, ClientConfig: &configstore.ClientConfig{}, ConfigStore: store}
	h := &MCPHandler{store: cfg, mcpManager: &capturingUpdateMCPManager{}}
	// Init attaches a server, so the store's queries can use the request as their context.
	var req fasthttp.Request
	req.SetBodyString(`{"is_code_mode_client":true}`)
	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.Init(&req, nil, nil)
	reqCtx.SetUserValue("id", existing.ID)
	h.updateMCPClient(reqCtx)
	require.Equal(t, fasthttp.StatusOK, reqCtx.Response.StatusCode(), string(reqCtx.Response.Body()))

	row, err := store.GetMCPClientByID(ctx, existing.ID)
	require.NoError(t, err)
	assert.True(t, row.IsCodeModeClient, "fixture check: the edit was stored")
	assert.Len(t, row.DiscoveredTools, 1)
	assert.Len(t, row.DiscoveredResources, 1, "the saved resources survive the edit")
	assert.Len(t, row.DiscoveredPrompts, 1, "the saved prompts survive the edit")
	assert.Equal(t, "Use the handbook first.", row.DiscoveredInstructions)
}

// A new client exposes every prompt and resource unless the caller lists them; an invalid list is
// rejected before anything is dialed.
func TestAddMCPClient_ExposeLists(t *testing.T) {
	for _, tc := range []struct {
		name, field        string
		prompts, resources schemas.WhiteList
		wantStatus         int
	}{
		{"omitted exposes everything", ``, nil, nil, fasthttp.StatusInternalServerError},
		{"lists are kept", `,"prompts_to_expose":[],"resources_to_expose":["repo://{owner}/{name}"]`, schemas.WhiteList{}, schemas.WhiteList{"repo://{owner}/{name}"}, fasthttp.StatusInternalServerError},
		{"invalid list rejected", `,"prompts_to_expose":["*","review"]`, nil, nil, fasthttp.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetLogger(&mockLogger{})
			manager := &capturingAddMCPManager{}
			h := &MCPHandler{store: &lib.Config{ConfigStore: newRealOAuth2Store(t), ClientConfig: &configstore.ClientConfig{}}, mcpManager: manager}
			var req fasthttp.Request
			req.Header.SetMethod(fasthttp.MethodPost)
			req.Header.SetContentType("application/json")
			req.SetBodyString(`{"name":"docs","connection_type":"http","connection_string":"https://mcp.example.com/mcp","auth_type":"headers","headers":{"Authorization":"Bearer x"}` + tc.field + `}`)
			ctx := initCtx(&req)
			h.addMCPClient(ctx)

			require.Equal(t, tc.wantStatus, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			if tc.wantStatus == fasthttp.StatusBadRequest {
				assert.Nil(t, manager.got, "rejected before dialing")
				assert.Contains(t, string(ctx.Response.Body()), "prompts_to_expose")
				return
			}
			require.NotNil(t, manager.got)
			assert.Equal(t, tc.prompts, manager.got.PromptsToExpose)
			assert.Equal(t, tc.resources, manager.got.ResourcesToExpose)
		})
	}
}

// An update sets, keeps or rejects the allow-lists, and both the stored row and the live config get the result.
func TestUpdateMCPClient_ExposeLists(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		want       schemas.WhiteList
		wantStatus int
	}{
		{"set to none", `{"prompts_to_expose":[]}`, schemas.WhiteList{}, fasthttp.StatusOK},
		{"back to all", `{"prompts_to_expose":["*"]}`, schemas.WhiteList{"*"}, fasthttp.StatusOK},
		{"omitted keeps the current list", `{"is_code_mode_client":false}`, schemas.WhiteList{"review"}, fasthttp.StatusOK},
		{"invalid list rejected", `{"resources_to_expose":["file:///a","FILE:///A"]}`, nil, fasthttp.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetLogger(&mockLogger{})
			existing := &schemas.MCPClientConfig{
				ID: "client-1", Name: "docs", ConnectionType: schemas.MCPConnectionTypeHTTP, AuthType: schemas.MCPAuthTypeNone,
				PromptsToExpose: schemas.WhiteList{"review"},
			}
			cfg := &lib.Config{MCPConfig: &schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{existing}}, ClientConfig: &configstore.ClientConfig{}}
			store := &capturingUpdateConfigStore{}
			cfg.ConfigStore = store
			manager := &capturingUpdateMCPManager{}
			h := &MCPHandler{store: cfg, mcpManager: manager}
			ctx := &fasthttp.RequestCtx{}
			ctx.SetUserValue("id", existing.ID)
			ctx.Request.SetBodyString(tc.body)
			h.updateMCPClient(ctx)

			require.Equal(t, tc.wantStatus, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			if tc.wantStatus != fasthttp.StatusOK {
				assert.Nil(t, store.row, "nothing stored")
				assert.Nil(t, manager.got, "nothing applied")
				return
			}
			require.NotNil(t, store.row)
			require.NotNil(t, manager.got)
			assert.Equal(t, tc.want, store.row.PromptsToExpose, "stored")
			assert.Equal(t, tc.want, manager.got.PromptsToExpose, "applied live")
		})
	}
}

// refusingExchangeResolver is an available identity-provider integration that refuses every
// token_exchange settings it is asked to check. Its other methods panic through the nil embedded
// interface, since a refused save must not resolve or exchange anything.
type refusingExchangeResolver struct {
	schemas.TokenExchangeIdPResolver
}

// Available reports the integration as configured, so token_exchange clients can be created.
func (refusingExchangeResolver) Available() bool { return true }

// ValidateTokenExchangeConfig refuses the settings with a synthetic reason.
func (refusingExchangeResolver) ValidateTokenExchangeConfig(context.Context, *schemas.MCPTokenExchangeConfig) error {
	return errors.New("synthetic refusal of these token_exchange settings")
}

// newRefusingExchangeProvider is an OAuth provider whose identity-provider integration is refusingExchangeResolver.
func newRefusingExchangeProvider(store configstore.ConfigStore) *oauth2.OAuth2Provider {
	provider := oauth2.NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))
	provider.SetTokenExchangeIdPResolver(refusingExchangeResolver{})
	return provider
}

// A token_exchange client whose settings the identity-provider integration refuses is rejected at
// create with the integration's reason, before anything is stored or dialed.
func TestAddMCPClient_TokenExchangeSettingsRefused(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	manager := &capturingAddMCPManager{}
	h := &MCPHandler{store: &lib.Config{ConfigStore: store, ClientConfig: &configstore.ClientConfig{}, OAuthProvider: newRefusingExchangeProvider(store)}, mcpManager: manager}
	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodPost)
	req.Header.SetContentType("application/json")
	req.SetBodyString(`{"name":"exchange","connection_type":"http","connection_string":"http://127.0.0.1:1/mcp","auth_type":"token_exchange","token_exchange":{"audience":"api://exchange","use_idp_credentials":true}}`)
	ctx := initCtx(&req)
	ctx.SetUserValue(schemas.BifrostContextKeyIsEnterprise, true)
	h.addMCPClient(ctx)

	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	assert.Contains(t, string(ctx.Response.Body()), "synthetic refusal of these token_exchange settings")
	assert.Nil(t, manager.got, "rejected before dialing")
}

// An edit to a token_exchange client's settings that the identity-provider integration refuses is
// rejected with the integration's reason, and neither the stored nor the live client changes.
func TestUpdateMCPClient_TokenExchangeSettingsRefused(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	existing := &schemas.MCPClientConfig{
		ID: "client-1", Name: "exchange", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("http://127.0.0.1:1/mcp"),
		AuthType:         schemas.MCPAuthTypeTokenExchange,
		TokenExchange:    &schemas.MCPTokenExchangeConfig{Audience: "api://exchange", UseIdPCredentials: true},
	}
	require.NoError(t, store.CreateMCPClientConfig(context.Background(), existing))
	manager := &capturingUpdateMCPManager{}
	cfg := &lib.Config{
		MCPConfig:     &schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{existing}},
		ClientConfig:  &configstore.ClientConfig{},
		ConfigStore:   store,
		OAuthProvider: newRefusingExchangeProvider(store),
	}
	h := &MCPHandler{store: cfg, mcpManager: manager, mcpCredentialCacheManager: &recordingCredentialCache{}}
	// Init attaches a server, so the store's queries can use the request as their context.
	var req fasthttp.Request
	req.SetBodyString(`{"token_exchange":{"audience":"api://edited","use_idp_credentials":true}}`)
	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&req, nil, nil)
	ctx.SetUserValue("id", existing.ID)
	h.updateMCPClient(ctx)

	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	assert.Contains(t, string(ctx.Response.Body()), "synthetic refusal of these token_exchange settings")
	assert.Nil(t, manager.got, "nothing applied")
	assert.Equal(t, "api://exchange", existing.TokenExchange.Audience, "the live client keeps its settings")
	stored, err := store.GetMCPClientConfigByID(context.Background(), existing.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.TokenExchange)
	assert.Equal(t, "api://exchange", stored.TokenExchange.Audience, "the stored client keeps its settings")
}

// Moving an OAuth client to new endpoints needs the secret resent only when one is stored: a public
// client has none to carry over, so it can follow its MCP server to a new authorization server.
func TestUpdateMCPClient_EndpointChangeNeedsStoredSecretResent(t *testing.T) {
	cases := []struct {
		name         string
		storedSecret string
		sentSecret   string
		wantStatus   int
	}{
		{name: "public client", wantStatus: fasthttp.StatusOK},
		{name: "confidential client without the secret", storedSecret: "old-secret", wantStatus: fasthttp.StatusBadRequest},
		{name: "confidential client resending the secret", storedSecret: "old-secret", sentSecret: "new-secret", wantStatus: fasthttp.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			SetLogger(&mockLogger{})
			store := newRealOAuth2Store(t)
			bg := context.Background()
			oauthRow := &configtables.TableOauthConfig{
				ID:           "oauth-1",
				ClientID:     schemas.NewSecretVar("old-client"),
				AuthorizeURL: "https://old-as.example.com/authorize",
				TokenURL:     "https://old-as.example.com/token",
				RedirectURI:  "http://localhost:8080/api/oauth/callback",
				Status:       "authorized",
				ServerURL:    "https://mcp.example.com/mcp",
			}
			if tc.storedSecret != "" {
				oauthRow.ClientSecret = schemas.NewSecretVar(tc.storedSecret)
			}
			require.NoError(t, store.CreateOauthConfig(bg, oauthRow))
			existing := &schemas.MCPClientConfig{
				ID: "client-1", Name: "moved", ConnectionType: schemas.MCPConnectionTypeHTTP,
				ConnectionString: schemas.NewSecretVar("https://mcp.example.com/mcp"),
				AuthType:         schemas.MCPAuthTypeOauth,
				OauthConfigID:    bifrost.Ptr(oauthRow.ID),
			}
			require.NoError(t, store.CreateMCPClientConfig(bg, existing))
			cfg := &lib.Config{
				MCPConfig:     &schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{existing}},
				ClientConfig:  &configstore.ClientConfig{},
				ConfigStore:   store,
				OAuthProvider: oauth2.NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError)),
			}
			h := &MCPHandler{store: cfg, mcpManager: &capturingUpdateMCPManager{}, mcpCredentialCacheManager: &recordingCredentialCache{}}

			secret := ""
			if tc.sentSecret != "" {
				secret = `,"client_secret":"` + tc.sentSecret + `"`
			}
			var req fasthttp.Request
			req.SetBodyString(`{"oauth_config":{"client_id":"new-client","authorize_url":"https://new-as.example.com/authorize","token_url":"https://new-as.example.com/token"` + secret + `}}`)
			ctx := &fasthttp.RequestCtx{}
			ctx.Init(&req, nil, nil)
			ctx.SetUserValue("id", existing.ID)
			h.updateMCPClient(ctx)

			require.Equal(t, tc.wantStatus, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			stored, err := store.GetOauthConfigByID(bg, oauthRow.ID)
			require.NoError(t, err)
			require.NotNil(t, stored)
			if tc.wantStatus != fasthttp.StatusOK {
				assert.Contains(t, string(ctx.Response.Body()), "client_secret must be resent")
				assert.Equal(t, "https://old-as.example.com/token", stored.TokenURL, "a refused edit changes nothing")
				return
			}
			assert.Equal(t, "https://new-as.example.com/token", stored.TokenURL)
			assert.Equal(t, "new-client", stored.GetResolvedClientID())
			assert.Equal(t, tc.sentSecret, stored.ClientSecret.GetValue(), "a public client stays without a secret")
		})
	}
}

// The resources and prompts lists are always arrays on the wire, empty when the server offers none, so clients
// can iterate them like tools.
func TestMCPClientResponse_ResourcesAndPromptsAreAlwaysArrays(t *testing.T) {
	var empty MCPClientResponse
	empty.setResourcesAndPrompts(nil)
	data, err := json.Marshal(empty)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"resources":[]`)
	assert.Contains(t, string(data), `"resource_templates":[]`)
	assert.Contains(t, string(data), `"prompts":[]`)

	var full MCPClientResponse
	full.setResourcesAndPrompts(&schemas.MCPClient{
		Resources: []mcpgo.Resource{mcpgo.NewResource("file:///handbook.md", "handbook")},
		Prompts:   []mcpgo.Prompt{mcpgo.NewPrompt("docs-review")},
	})
	data, err = json.Marshal(full)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"uri":"file:///handbook.md"`)
	assert.Contains(t, string(data), `"name":"docs-review"`)
	assert.Contains(t, string(data), `"resource_templates":[]`)
}

// decodeSyncInterval runs a raw JSON value through the request field exactly
// as a handler would, so these tests exercise the real decode path rather than
// a restatement of it.
func decodeSyncInterval(t *testing.T, raw string) (*MCPSyncInterval, error) {
	t.Helper()
	var body struct {
		ToolSyncInterval *MCPSyncInterval `json:"tool_sync_interval,omitempty"`
	}
	err := json.Unmarshal([]byte(`{"tool_sync_interval":`+raw+`}`), &body)
	return body.ToolSyncInterval, err
}

func TestSyncIntervalAcceptsDurationStrings(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{`"1s"`, time.Second},
		{`"30s"`, 30 * time.Second},
		{`"5m"`, 5 * time.Minute},
		{`"1h30m"`, 90 * time.Minute},
		{`"90s"`, 90 * time.Second},
		{`"10m"`, 10 * time.Minute},
	} {
		got, err := decodeSyncInterval(t, tc.raw)
		if err != nil {
			t.Errorf("tool_sync_interval=%s should decode, got %v", tc.raw, err)
			continue
		}
		if got.Duration != tc.want {
			t.Errorf("tool_sync_interval=%s: expected %v, got %v", tc.raw, tc.want, got.Duration)
		}
		if err := got.Validate(); err != nil {
			t.Errorf("tool_sync_interval=%s should validate, got %v", tc.raw, err)
		}
	}
}

// TestSyncIntervalBareNumberStillMeansMinutes is the compatibility guarantee.
// Every deployment already sending a plain number means minutes by it, and
// reinterpreting those values would change how often each of them checks its
// MCP servers without anyone asking for it.
func TestSyncIntervalBareNumberStillMeansMinutes(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{`0`, 0},
		{`1`, time.Minute},
		{`10`, 10 * time.Minute},
		{`60`, time.Hour},
		{`1440`, 24 * time.Hour},
	} {
		got, err := decodeSyncInterval(t, tc.raw)
		if err != nil {
			t.Errorf("tool_sync_interval=%s should decode, got %v", tc.raw, err)
			continue
		}
		if got.Duration != tc.want {
			t.Errorf("tool_sync_interval=%s: expected %v, got %v", tc.raw, tc.want, got.Duration)
		}
	}
}

// TestSyncIntervalRejectsFractionalSeconds covers the trap the string form
// introduces, across its whole range rather than just the tail. The column
// stores whole seconds, so "500ms" truncates to 0 and means "follow the global
// setting", the opposite of what a caller asking for a short interval wants,
// while "90500ms" keeps its seconds and silently drops the rest. Both are the
// same loss.
func TestSyncIntervalRejectsFractionalSeconds(t *testing.T) {
	for _, raw := range []string{`"1ms"`, `"500ms"`, `"999ms"`, `"1ns"`, `"1500ms"`, `"90500ms"`} {
		got, err := decodeSyncInterval(t, raw)
		if err != nil {
			continue // rejected at decode is equally acceptable
		}
		if err := got.Validate(); err == nil {
			t.Errorf("tool_sync_interval=%s loses precision on persistence and must be rejected", raw)
		}
	}
}

func TestSyncIntervalRejectsNegatives(t *testing.T) {
	for _, raw := range []string{`-1`, `-10`, `"-5m"`, `"-1s"`} {
		got, err := decodeSyncInterval(t, raw)
		if err != nil {
			continue
		}
		if err := got.Validate(); err == nil {
			t.Errorf("tool_sync_interval=%s is negative and must be rejected", raw)
		}
	}
}

// TestSyncIntervalRejectsNanosecondEchoes keeps the protection that predates
// the string form: a caller echoing back a nanosecond count as a bare number
// must still be refused, since it would otherwise be read as that many minutes
// and park tool sync centuries out.
func TestSyncIntervalRejectsNanosecondEchoes(t *testing.T) {
	for _, override := range []time.Duration{time.Minute, 10 * time.Minute, time.Hour} {
		nanos := int64(override) // what an older GET response carried for this override
		got, err := decodeSyncInterval(t, jsonInt(nanos))
		if err != nil {
			continue
		}
		if err := got.Validate(); err == nil {
			t.Errorf("tool_sync_interval=%d (nanoseconds for a %v override) must be rejected as a bare number", nanos, override)
		}
	}
}

// TestSyncIntervalRejectsFractionalBareNumbers pins that a fractional number is
// an error rather than being truncated, so "5.5" cannot quietly become five
// minutes.
func TestSyncIntervalRejectsFractionalBareNumbers(t *testing.T) {
	for _, raw := range []string{`5.5`, `0.5`, `1e3`} {
		if _, err := decodeSyncInterval(t, raw); err == nil {
			t.Errorf("tool_sync_interval=%s must be rejected; a bare number has to be whole minutes", raw)
		}
	}
}

func TestSyncIntervalRejectsUnparseableStrings(t *testing.T) {
	for _, raw := range []string{`"soon"`, `"5 minutes"`, `"m5"`, `"5x"`} {
		if _, err := decodeSyncInterval(t, raw); err == nil {
			t.Errorf("tool_sync_interval=%s must be rejected as an invalid duration", raw)
		}
	}
}

// TestSyncIntervalSurvivesPersistence asserts an accepted value round-trips
// through the seconds column it is stored in, which is the conversion that
// makes sub-second values meaningless in the first place.
func TestSyncIntervalSurvivesPersistence(t *testing.T) {
	for _, raw := range []string{`"1s"`, `"30s"`, `"5m"`, `10`, `1440`} {
		got, err := decodeSyncInterval(t, raw)
		if err != nil {
			t.Fatalf("tool_sync_interval=%s should decode, got %v", raw, err)
		}
		if err := got.Validate(); err != nil {
			t.Fatalf("tool_sync_interval=%s should validate, got %v", raw, err)
		}
		seconds := int(got.Duration / time.Second)
		if restored := time.Duration(seconds) * time.Second; restored != got.Duration {
			t.Errorf("tool_sync_interval=%s does not round-trip through the seconds column: %v became %v",
				raw, got.Duration, restored)
		}
	}
}

// TestResolveSyncIntervalKeepsExistingWhenAbsent pins the PATCH semantics the
// update endpoint documents: a field the caller did not send must not change.
// It also pins that Resolve is nil-safe, since an absent field is exactly a
// nil pointer and every call site relies on not having to check for it.
func TestResolveSyncIntervalKeepsExistingWhenAbsent(t *testing.T) {
	existing := 7 * time.Minute
	var absent *MCPSyncInterval
	got, err := absent.Resolve(existing)
	if err != nil {
		t.Fatalf("absent field should resolve cleanly, got %v", err)
	}
	if got != existing {
		t.Errorf("expected the existing %v to survive an absent field, got %v", existing, got)
	}

	// Zero is a value, not an absence: it hands the client back to the global
	// setting and must override an existing override.
	sent := &MCPSyncInterval{Duration: 0}
	got, err = sent.Resolve(existing)
	if err != nil {
		t.Fatalf("zero should resolve cleanly, got %v", err)
	}
	if got != 0 {
		t.Errorf("expected an explicit 0 to clear the override, got %v", got)
	}
}

func TestSyncIntervalMarshalsAsDurationString(t *testing.T) {
	out, err := json.Marshal(MCPSyncInterval{Duration: 90 * time.Second})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if string(out) != `"1m30s"` {
		t.Errorf(`expected "1m30s", got %s`, out)
	}
}

// jsonInt renders an int64 as a JSON number literal.
func jsonInt(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestCreateRequestShadowedFieldReceivesTheValue is the one that matters for
// the create path. MCPClientRequest embeds TableMCPClient, which carries its
// own tool_sync_interval column field, and the request field shadows it. Go
// resolves the shallower field, so the value must land on the request field
// and the embedded column field must stay untouched. Getting that backwards
// would silently drop the caller's interval on every create.
func TestCreateRequestShadowedFieldReceivesTheValue(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{`"30s"`, 30 * time.Second},
		{`"5m"`, 5 * time.Minute},
		{`10`, 10 * time.Minute},
	} {
		var req MCPClientRequest
		body := `{"name":"c","connection_type":"http","tool_sync_interval":` + tc.raw + `}`
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("%s should decode, got %v", body, err)
		}
		if req.ToolSyncInterval == nil {
			t.Fatalf("%s: the request field is nil, so the value was lost", body)
		}
		if req.ToolSyncInterval.Duration != tc.want {
			t.Errorf("%s: expected %v, got %v", body, tc.want, req.ToolSyncInterval.Duration)
		}
		// The embedded column field is written from the resolved duration at
		// the persistence boundary, not by the decoder, so it stays zero here.
		if req.TableMCPClient.ToolSyncInterval != 0 {
			t.Errorf("%s: the embedded column field should stay untouched, got %d",
				body, req.TableMCPClient.ToolSyncInterval)
		}
		// The value the handler resolves is what a create would persist.
		got, err := req.ToolSyncInterval.Resolve(0)
		if err != nil {
			t.Fatalf("%s should resolve, got %v", body, err)
		}
		if got != tc.want {
			t.Errorf("%s: resolved to %v, expected %v", body, got, tc.want)
		}
	}
}

// TestCreateRequestAbsentFieldResolvesToZero pins that omitting the field
// still means "use the global setting", which is what the create path relied
// on when the embedded int defaulted to 0.
func TestCreateRequestAbsentFieldResolvesToZero(t *testing.T) {
	var req MCPClientRequest
	if err := json.Unmarshal([]byte(`{"name":"c","connection_type":"http"}`), &req); err != nil {
		t.Fatalf("should decode, got %v", err)
	}
	if req.ToolSyncInterval != nil {
		t.Fatalf("expected an absent field to stay nil, got %+v", req.ToolSyncInterval)
	}
	got, err := req.ToolSyncInterval.Resolve(0)
	if err != nil || got != 0 {
		t.Errorf("expected an absent field to resolve to 0, got %v (%v)", got, err)
	}
}

// TestUpdateRequestDecodesBothForms covers the update struct, whose field was
// a plain *int before and where a nil pointer still has to mean "unchanged".
func TestUpdateRequestDecodesBothForms(t *testing.T) {
	var withValue MCPClientUpdateRequest
	if err := json.Unmarshal([]byte(`{"tool_sync_interval":"45s"}`), &withValue); err != nil {
		t.Fatalf("should decode, got %v", err)
	}
	got, err := withValue.ToolSyncInterval.Resolve(9 * time.Minute)
	if err != nil {
		t.Fatalf("should resolve, got %v", err)
	}
	if got != 45*time.Second {
		t.Errorf("expected 45s to override the existing value, got %v", got)
	}

	var absent MCPClientUpdateRequest
	if err := json.Unmarshal([]byte(`{"name":"n"}`), &absent); err != nil {
		t.Fatalf("should decode, got %v", err)
	}
	got, err = absent.ToolSyncInterval.Resolve(9 * time.Minute)
	if err != nil {
		t.Fatalf("should resolve, got %v", err)
	}
	if got != 9*time.Minute {
		t.Errorf("expected an absent field to keep the existing 9m, got %v", got)
	}
}

func TestSyncIntervalHTTPValidationErrors(t *testing.T) {
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
	existing := &schemas.MCPClientConfig{ID: "interval-client", Name: "interval-client", ToolSyncInterval: 7 * time.Minute}
	cfg.MCPConfig = &schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{existing}}
	h := &MCPHandler{store: cfg}
	for _, tc := range []struct{ raw, want string }{
		{`""`, "tool_sync_interval must not be blank"},
		{`"   "`, "tool_sync_interval must not be blank"},
		{`"soon"`, "expected a duration"},
		{`5.5`, "whole number of minutes"},
		{jsonInt(maxToolSyncIntervalMinutes + 1), "must be at most"},
		{`true`, "duration string or a number of minutes"},
	} {
		for _, update := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/update=%t", tc.raw, update), func(t *testing.T) {
				ctx := &fasthttp.RequestCtx{}
				ctx.Request.SetBodyString(`{"name":"interval-client","connection_type":"http","connection_string":"https://example.com/mcp","tool_sync_interval":` + tc.raw + `}`)
				if update {
					ctx.SetUserValue("id", existing.ID)
					h.updateMCPClient(ctx)
				} else {
					h.addMCPClient(ctx)
				}
				require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
				assert.Contains(t, string(ctx.Response.Body()), "tool_sync_interval")
				assert.Contains(t, string(ctx.Response.Body()), tc.want)
				assert.Equal(t, 7*time.Minute, existing.ToolSyncInterval)
			})
		}
	}
	// Unrelated decoder errors must retain the generic response.
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(`{"name":true}`)
	h.addMCPClient(ctx)
	assert.Contains(t, string(ctx.Response.Body()), "Invalid request payload")
}

func TestSyncIntervalBlankUpdateKeepsOverride(t *testing.T) {
	for _, raw := range []string{`""`, `"   "`} {
		field, err := decodeSyncInterval(t, raw)
		if err == nil {
			resolved, resolveErr := field.Resolve(7 * time.Minute)
			t.Fatalf("blank update must fail decoding; got interval %v, error %v", resolved, resolveErr)
		}
	}
}

func TestParseRequestDurationUnits(t *testing.T) {
	for _, tc := range []struct {
		field, label string
		unit         time.Duration
	}{
		{"sync_interval", "minutes", time.Minute},
		{"execution_timeout", "seconds", time.Second},
		{"retry_backoff", "milliseconds", time.Millisecond},
	} {
		t.Run(tc.field, func(t *testing.T) {
			got, err := parseRequestDuration([]byte(`2`), tc.field, tc.unit, tc.label)
			require.NoError(t, err)
			assert.Equal(t, 2*tc.unit, got)
			got, err = parseRequestDuration([]byte(`"1500ms"`), tc.field, tc.unit, tc.label)
			require.NoError(t, err)
			assert.Equal(t, 1500*time.Millisecond, got) // Precision is field-specific.
			max := int64(1<<63-1) / int64(tc.unit)
			got, err = parseRequestDuration([]byte(jsonInt(max)), tc.field, tc.unit, tc.label)
			require.NoError(t, err)
			assert.Equal(t, time.Duration(max)*tc.unit, got)
			for _, raw := range []string{`""`, `"  "`, `"soon"`, `-1`, `"-1s"`, `1.5`, `1e3`, `true`, jsonInt(max + 1)} {
				_, err := parseRequestDuration([]byte(raw), tc.field, tc.unit, tc.label)
				require.Error(t, err, raw)
				assert.Contains(t, requestDurationDecodeError(err), tc.field)
			}
			for _, raw := range []string{`0`, `"0s"`, `null`} {
				got, err := parseRequestDuration([]byte(raw), tc.field, tc.unit, tc.label)
				require.NoError(t, err)
				assert.Zero(t, got)
			}
		})
	}
}

// TestAddMCPClient_PerUserHeadersKeepsAdminCredential: creating a per_user_headers
// client keeps the admin's sample values as its admin credential, the one periodic
// tool discovery connects with, just as verify-headers keeps them. Without it the
// client's first discovery check finds no credential and marks it unstable.
func TestAddMCPClient_PerUserHeadersKeepsAdminCredential(t *testing.T) {
	h, mgr := newOpenAPITestHandler(t)
	h.store.MCPHeadersProvider = mcp_headers.NewProvider(h.store.ConfigStore, &mockLogger{})

	body, err := json.Marshal(map[string]any{
		"name": "acme_h", "connection_type": "http", "connection_string": "http://127.0.0.1:1/mcp",
		"auth_type": "per_user_headers", "per_user_header_keys": []string{"X-Api-Key"},
		"user_headers": map[string]string{"X-Api-Key": "sample-key"}, "tools_to_execute": []string{"*"},
	})
	require.NoError(t, err)
	status, respBody := doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(body), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	require.Len(t, mgr.added, 1)

	cred, err := h.store.MCPHeadersProvider.GetCredentialByMode(context.Background(), schemas.MCPAuthModeAdmin, "", mgr.added[0].ID)
	require.NoError(t, err, "the create call keeps an admin credential")
	require.NotNil(t, cred)
	assert.Equal(t, map[string]string{"x-api-key": "sample-key"}, cred.Headers, "the sample value, under its canonical key")
	assert.Equal(t, schemas.MCPHeadersUserCredentialStatusActive, cred.Status)
}

// failingHeaderCredentialStore is a config store whose header credential writes fail.
type failingHeaderCredentialStore struct{ configstore.ConfigStore }

// UpsertMCPPerUserHeaderCredential always fails.
func (failingHeaderCredentialStore) UpsertMCPPerUserHeaderCredential(context.Context, *configtables.TableMCPPerUserHeaderCredential) error {
	return errors.New("credential table unavailable")
}

// TestAddMCPClient_PerUserHeadersCreatesWhenTheCredentialCannotBeKept: keeping the
// sample values is best effort, so a failed credential write leaves the verified
// client created and registered; verify-headers can store the credential later.
func TestAddMCPClient_PerUserHeadersCreatesWhenTheCredentialCannotBeKept(t *testing.T) {
	h, mgr := newOpenAPITestHandler(t)
	h.store.MCPHeadersProvider = mcp_headers.NewProvider(failingHeaderCredentialStore{h.store.ConfigStore}, &mockLogger{})

	body, err := json.Marshal(map[string]any{
		"name": "acme_h", "connection_type": "http", "connection_string": "http://127.0.0.1:1/mcp",
		"auth_type": "per_user_headers", "per_user_header_keys": []string{"X-Api-Key"},
		"user_headers": map[string]string{"X-Api-Key": "sample-key"}, "tools_to_execute": []string{"*"},
	})
	require.NoError(t, err)
	status, respBody := doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(body), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	require.Len(t, mgr.added, 1, "the client is registered without the credential")
	_, err = h.store.MCPHeadersProvider.GetCredentialByMode(context.Background(), schemas.MCPAuthModeAdmin, "", mgr.added[0].ID)
	assert.Error(t, err, "no admin credential was kept")
}
