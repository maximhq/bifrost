package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/oauth2"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

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
