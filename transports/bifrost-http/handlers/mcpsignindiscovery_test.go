package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/mcp_headers"
	"github.com/maximhq/bifrost/framework/mcpdiscovery"
	"github.com/maximhq/bifrost/framework/oauth2"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// signInMCPManager answers verification the way a client's upstream would, with
// one tool, and records the credential each verification was handed. duringVerify,
// when set, runs inside an OAuth verification, as something that happens while
// discovery dials; verifyErr, when set, fails it, as an upstream that is down.
type signInMCPManager struct {
	MCPManager
	mu           sync.Mutex
	tokens       []string
	headers      []map[string]string
	duringVerify func()
	verifyErr    error
}

func (m *signInMCPManager) VerifyPerUserOAuthConnection(_ context.Context, config *schemas.MCPClientConfig, accessToken string) (*schemas.MCPDiscovery, error) {
	m.mu.Lock()
	m.tokens = append(m.tokens, accessToken)
	hook := m.duringVerify
	m.mu.Unlock()
	if hook != nil {
		hook()
	}
	if m.verifyErr != nil {
		return nil, m.verifyErr
	}
	return signInDiscovery(config.Name), nil
}

func (m *signInMCPManager) VerifyHeadersConnection(_ context.Context, config *schemas.MCPClientConfig, headers map[string]string) (*schemas.MCPDiscovery, error) {
	m.mu.Lock()
	m.headers = append(m.headers, headers)
	m.mu.Unlock()
	return signInDiscovery(config.Name), nil
}

// signInDiscovery is what verifying a sign-in finds on the client: a tool, the server's
// instructions, a resource and a prompt.
func signInDiscovery(clientName string) *schemas.MCPDiscovery {
	name := clientName + "-search"
	return &schemas.MCPDiscovery{
		Tools:           map[string]schemas.ChatTool{name: {Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: name}}},
		ToolNameMapping: map[string]string{"search": "search"},
		Instructions:    "Search before you write.",
		Resources:       []mcpgo.Resource{mcpgo.NewResource("file:///handbook.md", "handbook")},
		Prompts:         []mcpgo.Prompt{mcpgo.NewPrompt(clientName + "-review")},
	}
}

// requireSignInResourcesAndPrompts checks a stored discovery kept the resource and prompt
// verification found, with the prompt stored under its upstream name.
func requireSignInResourcesAndPrompts(t *testing.T, row *tables.TableMCPPerUserDiscovery) {
	t.Helper()
	require.Len(t, row.Resources, 1)
	assert.Equal(t, "file:///handbook.md", row.Resources[0].URI)
	require.Len(t, row.Prompts, 1)
	assert.Equal(t, "review", row.Prompts[0].Name)
}

// newSignInConfig returns a config over a real store with the providers and the
// per-user discovery store sign-in uses.
func newSignInConfig(t *testing.T) (*lib.Config, configstore.ConfigStore) {
	t.Helper()
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	return &lib.Config{
		ConfigStore:              store,
		ClientConfig:             &configstore.ClientConfig{},
		OAuthProvider:            oauth2.NewOAuth2Provider(store, logger),
		MCPHeadersProvider:       mcp_headers.NewProvider(store, logger),
		MCPPerUserDiscoveryStore: mcpdiscovery.NewPerUserStore(store, logger),
	}, store
}

// failingDiscoveryWrites is a config store whose per-user discovery writes fail.
type failingDiscoveryWrites struct {
	configstore.ConfigStore
}

func (failingDiscoveryWrites) UpsertMCPPerUserDiscovery(context.Context, *tables.TableMCPPerUserDiscovery) error {
	return errors.New("disk full")
}

// failDiscoveryWrites makes cfg's per-user discovery store fail every write.
func failDiscoveryWrites(cfg *lib.Config, store configstore.ConfigStore) {
	cfg.MCPPerUserDiscoveryStore = mcpdiscovery.NewPerUserStore(failingDiscoveryWrites{store}, bifrost.NewDefaultLogger(schemas.LogLevelError))
}

// createSignInClient stores client, keeping one discovery per caller when perUser.
func createSignInClient(t *testing.T, store configstore.ConfigStore, client *schemas.MCPClientConfig, perUser bool) {
	t.Helper()
	client.PerUserDiscovery = schemas.Ptr(perUser)
	require.NoError(t, store.CreateMCPClientConfig(context.Background(), client))
}

// createOAuthConfig stores the OAuth config "oauth-notes", whose token endpoint
// issues accessToken.
func createOAuthConfig(t *testing.T, store configstore.ConfigStore, accessToken string) {
	t.Helper()
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": accessToken, "token_type": "bearer", "expires_in": 3600})
	}))
	t.Cleanup(tokens.Close)
	require.NoError(t, store.CreateOauthConfig(context.Background(), &tables.TableOauthConfig{
		ID:           "oauth-notes",
		ClientID:     schemas.NewSecretVar("bifrost"),
		TokenURL:     tokens.URL,
		AuthorizeURL: "https://idp.example/authorize",
		RedirectURI:  "http://localhost/api/oauth/callback",
		Status:       "authorized",
	}))
}

// createUserOAuthFlow stores a pending user-mode flow for userID under state.
func createUserOAuthFlow(t *testing.T, store configstore.ConfigStore, clientID, userID, state string) {
	t.Helper()
	require.NoError(t, store.CreateOauthUserSession(context.Background(), &tables.TableMCPOauthFlow{
		ID:            "flow-" + state,
		MCPClientID:   clientID,
		OauthConfigID: "oauth-notes",
		State:         state,
		CodeVerifier:  "verifier",
		UserID:        schemas.Ptr(userID),
		FlowMode:      string(schemas.MCPAuthModeUser),
		Status:        "pending",
		ExpiresAt:     time.Now().Add(10 * time.Minute),
	}))
}

// newOAuthSignIn returns a config holding a per_user_oauth client "notes" and a
// pending flow for alice under "state-1", whose token endpoint issues
// "alice-token".
func newOAuthSignIn(t *testing.T, perUser bool) (*lib.Config, configstore.ConfigStore, *schemas.MCPClientConfig) {
	t.Helper()
	cfg, store := newSignInConfig(t)
	createOAuthConfig(t, store, "alice-token")
	client := &schemas.MCPClientConfig{
		ID:               "client-notes",
		Name:             "notes",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("http://upstream.invalid/mcp"),
		AuthType:         schemas.MCPAuthTypePerUserOauth,
		OauthConfigID:    schemas.Ptr("oauth-notes"),
		ToolsToExecute:   []string{"*"},
	}
	createSignInClient(t, store, client, perUser)
	createUserOAuthFlow(t, store, client.ID, "alice", "state-1")
	return cfg, store, client
}

// callOAuthCallback completes the flow under state and returns where the browser
// is redirected.
func callOAuthCallback(t *testing.T, h *OAuthHandler, state string) string {
	t.Helper()
	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodGet)
	req.SetRequestURI("/api/oauth/callback?state=" + state + "&code=code-1")
	var ctx fasthttp.RequestCtx
	ctx.Init(&req, nil, nil)
	h.handleOAuthCallback(&ctx)
	require.Equal(t, fasthttp.StatusFound, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	return string(ctx.Response.Header.Peek(fasthttp.HeaderLocation))
}

// seedEarlierDiscovery stores a discovery of client for mode and identity, as an
// earlier sign-in, possibly with another account, would have left.
func seedEarlierDiscovery(t *testing.T, store configstore.ConfigStore, client *schemas.MCPClientConfig, mode schemas.MCPAuthMode, identity string) {
	t.Helper()
	earlier := mcpdiscovery.NewPerUserStore(store, bifrost.NewDefaultLogger(schemas.LogLevelError))
	tools := map[string]schemas.ChatTool{client.Name + "-old": {Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: client.Name + "-old"}}}
	require.NoError(t, earlier.SetDiscovery(signInCallerContext(context.Background(), mode, identity), client, &schemas.MCPDiscovery{Tools: tools}))
	require.NotNil(t, storedDiscovery(store, mode, identity, client.ID))
}

func storedDiscovery(store configstore.ConfigStore, mode schemas.MCPAuthMode, identity, clientID string) *tables.TableMCPPerUserDiscovery {
	row, err := store.GetMCPPerUserDiscoveryByMode(context.Background(), mode, identity, clientID)
	if err != nil {
		return nil
	}
	return row
}

func TestOAuthCallback_StoresTheCallersDiscoveryBeforeRedirecting(t *testing.T) {
	cfg, store, client := newOAuthSignIn(t, true)
	manager := &signInMCPManager{}

	location := callOAuthCallback(t, NewOAuthHandler(cfg.OAuthProvider, nil, cfg, manager), "state-1")

	row := storedDiscovery(store, schemas.MCPAuthModeUser, "alice", client.ID)
	require.NotNil(t, row, "sign-in leaves the caller with a discovery, without a connect call")
	assert.Contains(t, row.Tools, "search")
	assert.Equal(t, "Search before you write.", row.Instructions)
	requireSignInResourcesAndPrompts(t, row)
	assert.Equal(t, []string{"alice-token"}, manager.tokens, "discovery uses the token sign-in just stored")
	assert.NotContains(t, location, "tools=", "the success page has nothing to add")
}

// TestOAuthCallback_RedirectsSayingTheToolsArePendingWhenDiscoveryFails still
// completes sign-in when its discovery fails, and sends the browser to a success
// page that says the client's tools are not loaded yet, naming only the client.
// The discovery an earlier sign-in left is dropped, so the caller is offered the
// connect tool, which discovers with the new token, rather than the old tools.
func TestOAuthCallback_RedirectsSayingTheToolsArePendingWhenDiscoveryFails(t *testing.T) {
	for _, tc := range []struct {
		name    string
		manager *signInMCPManager
		failing bool
	}{
		{name: "upstream down", manager: &signInMCPManager{verifyErr: errors.New("upstream unavailable")}},
		{name: "store write fails", manager: &signInMCPManager{}, failing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, store, client := newOAuthSignIn(t, true)
			seedEarlierDiscovery(t, store, client, schemas.MCPAuthModeUser, "alice")
			if tc.failing {
				failDiscoveryWrites(cfg, store)
			}

			location := callOAuthCallback(t, NewOAuthHandler(cfg.OAuthProvider, nil, cfg, tc.manager), "state-1")

			assert.Contains(t, location, "/workspace/mcp-sessions/auth-success?tools=pending&client=notes")
			assert.NotContains(t, location, "unavailable", "the error stays out of the URL")
			assert.NotContains(t, location, "disk", "the error stays out of the URL")
			assert.Nil(t, storedDiscovery(store, schemas.MCPAuthModeUser, "alice", client.ID), "the earlier sign-in's discovery is dropped")
			token, err := cfg.OAuthProvider.GetUserAccessTokenByMode(context.Background(), schemas.MCPAuthModeUser, "alice", client.ID)
			require.NoError(t, err, "the token is stored either way, for connect to discover with")
			assert.Equal(t, "alice-token", token)
		})
	}
}

func TestOAuthCallback_DiscoversNothingForAClientWithoutPerUserDiscovery(t *testing.T) {
	cfg, store, client := newOAuthSignIn(t, false)
	manager := &signInMCPManager{}

	location := callOAuthCallback(t, NewOAuthHandler(cfg.OAuthProvider, nil, cfg, manager), "state-1")

	assert.Empty(t, manager.tokens, "a shared client dials nothing after sign-in")
	assert.Nil(t, storedDiscovery(store, schemas.MCPAuthModeUser, "alice", client.ID))
	assert.NotContains(t, location, "tools=")
}

func TestOAuthCallback_StoresNothingWhenTheTokenIsRevokedDuringDiscovery(t *testing.T) {
	cfg, store, client := newOAuthSignIn(t, true)
	manager := &signInMCPManager{duringVerify: func() {
		token, err := store.GetOauthUserTokenByMode(context.Background(), schemas.MCPAuthModeUser, "alice", client.ID)
		require.NoError(t, err)
		require.NoError(t, store.DeleteOauthToken(context.Background(), token.ID))
		cfg.OAuthProvider.EvictUserTokenByID(token.ID)
	}}

	location := callOAuthCallback(t, NewOAuthHandler(cfg.OAuthProvider, nil, cfg, manager), "state-1")

	assert.Len(t, manager.tokens, 1)
	assert.Nil(t, storedDiscovery(store, schemas.MCPAuthModeUser, "alice", client.ID), "a revoke that lands while discovery runs is not undone")
	assert.NotContains(t, location, "tools=", "connecting again would not help a revoked caller")
}

func headersNotesClient() *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID:                "client-notes",
		Name:              "notes",
		ConnectionType:    schemas.MCPConnectionTypeHTTP,
		ConnectionString:  schemas.NewSecretVar("http://upstream.invalid/mcp"),
		AuthType:          schemas.MCPAuthTypePerUserHeaders,
		PerUserHeaderKeys: []string{"x-api-key"},
		ToolsToExecute:    []string{"*"},
	}
}

// submitHeaders submits flow's headers as dashboardUser, requires it to succeed,
// and returns the response body.
func submitHeaders(t *testing.T, h *MCPPerUserHeadersHandler, flowID, dashboardUser string) map[string]any {
	t.Helper()
	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodPut)
	req.SetBodyString(`{"headers":{"x-api-key":"key-1"}}`)
	var ctx fasthttp.RequestCtx
	ctx.Init(&req, nil, nil)
	ctx.SetUserValue("id", flowID)
	if dashboardUser != "" {
		ctx.SetUserValue(schemas.BifrostContextKeyUserID, dashboardUser)
	}
	h.flowSubmit(&ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	var body map[string]any
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &body))
	return body
}

func createHeadersFlow(t *testing.T, store configstore.ConfigStore, flow *tables.TableMCPPerUserHeaderFlow) {
	t.Helper()
	flow.Status = "pending"
	flow.ExpiresAt = time.Now().Add(10 * time.Minute)
	require.NoError(t, store.CreateMCPPerUserHeaderFlow(context.Background(), flow))
}

func TestHeadersSubmit_KeepsTheVerificationAsTheCallersDiscovery(t *testing.T) {
	client := headersNotesClient()
	cfg, store := newSignInConfig(t)
	createSignInClient(t, store, client, true)
	createHeadersFlow(t, store, &tables.TableMCPPerUserHeaderFlow{ID: "flow-1", MCPClientID: client.ID, FlowMode: string(schemas.MCPAuthModeUser), UserID: schemas.Ptr("alice")})
	manager := &signInMCPManager{}

	body := submitHeaders(t, NewMCPPerUserHeadersHandler(manager, nil, cfg, nil), "flow-1", "alice")

	row := storedDiscovery(store, schemas.MCPAuthModeUser, "alice", client.ID)
	require.NotNil(t, row, "the verification submit already ran becomes the caller's discovery")
	assert.Contains(t, row.Tools, "search")
	requireSignInResourcesAndPrompts(t, row)
	assert.Len(t, manager.headers, 1, "no second handshake")
	assert.NotContains(t, body, "discovery_error")
}

// TestHeadersSubmit_ReportsADiscoveryItCouldNotStore still stores the submitted
// credential when its discovery cannot be stored, and says why in the response.
// The discovery an earlier submit left is dropped, so the caller is offered the
// connect tool rather than tools found with the headers they replaced.
func TestHeadersSubmit_ReportsADiscoveryItCouldNotStore(t *testing.T) {
	client := headersNotesClient()
	cfg, store := newSignInConfig(t)
	createSignInClient(t, store, client, true)
	seedEarlierDiscovery(t, store, client, schemas.MCPAuthModeUser, "alice")
	failDiscoveryWrites(cfg, store)
	createHeadersFlow(t, store, &tables.TableMCPPerUserHeaderFlow{ID: "flow-1", MCPClientID: client.ID, FlowMode: string(schemas.MCPAuthModeUser), UserID: schemas.Ptr("alice")})

	body := submitHeaders(t, NewMCPPerUserHeadersHandler(&signInMCPManager{}, nil, cfg, nil), "flow-1", "alice")

	assert.Equal(t, "success", body["status"])
	assert.Contains(t, body["discovery_error"], "disk full")
	assert.Nil(t, storedDiscovery(store, schemas.MCPAuthModeUser, "alice", client.ID), "the earlier submit's discovery is dropped")
	cred, err := cfg.MCPHeadersProvider.GetCredentialByMode(context.Background(), schemas.MCPAuthModeUser, "alice", client.ID)
	require.NoError(t, err)
	require.NotNil(t, cred, "the credential is stored either way, for connect to discover with")
	assert.Equal(t, schemas.MCPHeadersUserCredentialStatusActive, cred.Status)
}

func TestHeadersSubmit_StoresAVirtualKeysDiscoveryUnderTheKey(t *testing.T) {
	client := headersNotesClient()
	cfg, store := newSignInConfig(t)
	createSignInClient(t, store, client, true)
	require.NoError(t, store.CreateVirtualKey(context.Background(), &tables.TableVirtualKey{ID: "vk-1", Name: "vk-1", Value: *schemas.NewSecretVar("sk-bf-vk-1"), IsActive: schemas.Ptr(true)}))
	createHeadersFlow(t, store, &tables.TableMCPPerUserHeaderFlow{ID: "flow-1", MCPClientID: client.ID, FlowMode: string(schemas.MCPAuthModeVK), VirtualKeyID: schemas.Ptr("vk-1")})

	submitHeaders(t, NewMCPPerUserHeadersHandler(&signInMCPManager{}, nil, cfg, nil), "flow-1", "bob")

	assert.NotNil(t, storedDiscovery(store, schemas.MCPAuthModeVK, "vk-1", client.ID), "stored under the key the credential belongs to")
	assert.Nil(t, storedDiscovery(store, schemas.MCPAuthModeUser, "bob", client.ID), "never under the dashboard user who submitted it")
}

func TestHeadersSubmit_StoresNoDiscoveryForAClientWithoutPerUserDiscovery(t *testing.T) {
	client := headersNotesClient()
	cfg, store := newSignInConfig(t)
	createSignInClient(t, store, client, false)
	createHeadersFlow(t, store, &tables.TableMCPPerUserHeaderFlow{ID: "flow-1", MCPClientID: client.ID, FlowMode: string(schemas.MCPAuthModeUser), UserID: schemas.Ptr("alice")})

	body := submitHeaders(t, NewMCPPerUserHeadersHandler(&signInMCPManager{}, nil, cfg, nil), "flow-1", "alice")
	assert.Nil(t, storedDiscovery(store, schemas.MCPAuthModeUser, "alice", client.ID))
	assert.NotContains(t, body, "discovery_error")
}

// recordingDiscoveryStore records the caller each discovery was stored under.
type recordingDiscoveryStore struct {
	schemas.MCPDiscoveryStore
	stored []string
}

func (s *recordingDiscoveryStore) SetDiscovery(ctx context.Context, client *schemas.MCPClientConfig, _ *schemas.MCPDiscovery) error {
	bctx := ctx.(*schemas.BifrostContext)
	mode := bctx.MCPAuthMode()
	s.stored = append(s.stored, string(mode)+"/"+bctx.MCPIdentity(mode)+"/"+client.ID)
	return nil
}

// TestStoreSignInDiscovery_StoresUnderTheFlowsCaller stores a sign-in's discovery
// under the caller the credential belongs to, for a client that keeps one per
// caller, whoever made the request that completed sign-in.
func TestStoreSignInDiscovery_StoresUnderTheFlowsCaller(t *testing.T) {
	client := &schemas.MCPClientConfig{ID: "client-notes", Name: "notes", PerUserDiscovery: schemas.Ptr(true)}
	shared := &schemas.MCPClientConfig{ID: "client-wiki", Name: "wiki"}

	for _, tc := range []struct {
		name   string
		client *schemas.MCPClientConfig
		mode   schemas.MCPAuthMode
		stored []string
	}{
		{name: "user", client: client, mode: schemas.MCPAuthModeUser, stored: []string{"user/alice/client-notes"}},
		{name: "virtual key", client: client, mode: schemas.MCPAuthModeVK, stored: []string{"vk/alice/client-notes"}},
		{name: "session-mode caller", client: client, mode: schemas.MCPAuthModeSession},
		{name: "shared client", client: shared, mode: schemas.MCPAuthModeUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &recordingDiscoveryStore{}
			// The request completing sign-in names someone else; the flow's identity wins.
			parent := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			parent.SetValue(schemas.BifrostContextKeyUserID, "dashboard-user")

			require.NoError(t, storeSignInDiscovery(parent, store, tc.client, tc.mode, "alice", &schemas.MCPDiscovery{}))
			assert.Equal(t, tc.stored, store.stored)
		})
	}
}
