package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/oauth2"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestParseMCPSessionsListQuery_Defaults(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/mcp/sessions")
	q, ok := parseMCPSessionsListQuery(ctx)
	if !ok {
		t.Fatalf("expected parse to succeed: body=%s", string(ctx.Response.Body()))
	}
	if q.Limit != mcpSessionsDefaultLimit {
		t.Fatalf("expected default limit %d, got %d", mcpSessionsDefaultLimit, q.Limit)
	}
	if q.Offset != 0 {
		t.Fatalf("expected default offset 0, got %d", q.Offset)
	}
	if q.Filters.Search != "" || len(q.Filters.Statuses) != 0 || len(q.Filters.AuthModes) != 0 || len(q.Filters.MCPClientIDs) != 0 || len(q.Kinds) != 0 {
		t.Fatalf("expected zero-value filters, got %#v", q)
	}
}

func TestParseMCPSessionsListQuery_AllParams(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/mcp/sessions?q=hello&kind=token,header&status=active,orphaned&auth_mode=user,vk&mcp_client_id=c1,c2&limit=25&offset=50")
	q, ok := parseMCPSessionsListQuery(ctx)
	if !ok {
		t.Fatalf("parse failed: body=%s", string(ctx.Response.Body()))
	}
	if q.Filters.Search != "hello" {
		t.Fatalf("search=%q", q.Filters.Search)
	}
	if strings.Join(q.Kinds, ",") != "token,header" {
		t.Fatalf("kinds=%v", q.Kinds)
	}
	if strings.Join(q.Filters.Statuses, ",") != "active,orphaned" {
		t.Fatalf("statuses=%v", q.Filters.Statuses)
	}
	if strings.Join(q.Filters.AuthModes, ",") != "user,vk" {
		t.Fatalf("auth_modes=%v", q.Filters.AuthModes)
	}
	if strings.Join(q.Filters.MCPClientIDs, ",") != "c1,c2" {
		t.Fatalf("mcp_client_ids=%v", q.Filters.MCPClientIDs)
	}
	if q.Limit != 25 {
		t.Fatalf("limit=%d", q.Limit)
	}
	if q.Offset != 50 {
		t.Fatalf("offset=%d", q.Offset)
	}
}

func TestParseMCPSessionsListQuery_LimitCappedAtMax(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/mcp/sessions?limit=9999")
	q, ok := parseMCPSessionsListQuery(ctx)
	if !ok {
		t.Fatalf("parse failed")
	}
	if q.Limit != mcpSessionsMaxLimit {
		t.Fatalf("expected limit capped at %d, got %d", mcpSessionsMaxLimit, q.Limit)
	}
}

func TestParseMCPSessionsListQuery_InvalidLimit(t *testing.T) {
	for _, raw := range []string{"abc", "-5", "0"} {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("/api/mcp/sessions?limit=" + raw)
		if _, ok := parseMCPSessionsListQuery(ctx); ok {
			t.Fatalf("limit=%q should have failed validation", raw)
		}
		if code := ctx.Response.StatusCode(); code != fasthttp.StatusBadRequest {
			t.Fatalf("limit=%q: expected 400, got %d", raw, code)
		}
	}
}

func TestParseMCPSessionsListQuery_InvalidOffset(t *testing.T) {
	for _, raw := range []string{"abc", "-1"} {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("/api/mcp/sessions?offset=" + raw)
		if _, ok := parseMCPSessionsListQuery(ctx); ok {
			t.Fatalf("offset=%q should have failed validation", raw)
		}
		if code := ctx.Response.StatusCode(); code != fasthttp.StatusBadRequest {
			t.Fatalf("offset=%q: expected 400, got %d", raw, code)
		}
	}
}

func TestMCPSessionsListQuery_KindAllowed(t *testing.T) {
	empty := mcpSessionsListQuery{}
	if !empty.kindAllowed("token") || !empty.kindAllowed("flow") {
		t.Fatalf("empty filter must allow all kinds")
	}
	q := mcpSessionsListQuery{Kinds: []string{"token", "header"}}
	if !q.kindAllowed("token") || !q.kindAllowed("header") {
		t.Fatalf("filter should match listed kinds")
	}
	if q.kindAllowed("flow") {
		t.Fatalf("flow should not match {token, header}")
	}
}

// TestRevokeMCPSession_PendingOAuthFlow pins that a pending OAuth flow, which the
// sessions list shows as kind "flow" with auth_kind "oauth", can be revoked like the
// other rows: 204, and the flow row is gone, so a callback that has not yet claimed
// it finds nothing to claim and can no longer mint a token, while a credential
// already stored for the same binding is kept. It was a 404 "MCP session not found".
// An unknown id stays a 404.
func TestRevokeMCPSession_PendingOAuthFlow(t *testing.T) {
	h, _ := newOpenAPITestHandler(t)
	ctx := context.Background()
	vk := "vk-1"
	now := time.Now()
	require.NoError(t, h.store.ConfigStore.CreateOauthUserSession(ctx, &tables.TableMCPOauthFlow{
		ID: "flow-1", MCPClientID: "client-1", OauthConfigID: "oauth-config-1", State: "state-1",
		VirtualKeyID: &vk, FlowMode: "vk", Status: "pending",
		ExpiresAt: now.Add(15 * time.Minute), CreatedAt: now, UpdatedAt: now,
	}))
	// A re-authentication pending on a stored credential: the flow sits beside the
	// binding's token, and revoking the flow must leave that token in place.
	require.NoError(t, h.store.ConfigStore.CreateOauthToken(ctx, &tables.TableMCPOauthToken{
		ID: "token-1", AuthMode: "vk", VirtualKeyID: &vk, MCPClientID: "client-1", OauthConfigID: "oauth-config-1",
		Status: "active", AccessToken: "secret", TokenType: "Bearer", Scopes: "null", CreatedAt: now, UpdatedAt: now,
	}))
	sessions := NewMCPSessionsHandler(h.store, nil, nil)

	status, body := doMCPRequest(t, sessions.revoke, fasthttp.MethodDelete, "", "flow-1", false)
	require.Equal(t, fasthttp.StatusNoContent, status, body)
	flow, err := h.store.ConfigStore.GetOauthUserSessionByID(ctx, "flow-1")
	require.NoError(t, err)
	assert.Nil(t, flow, "the flow row is deleted")
	token, err := h.store.ConfigStore.GetOauthUserTokenByID(ctx, "token-1")
	require.NoError(t, err)
	require.NotNil(t, token, "a credential already stored for the binding is kept")
	assert.Equal(t, "active", token.Status)
	claimed, err := h.store.ConfigStore.ClaimOauthUserSessionByState(ctx, "state-1")
	require.NoError(t, err)
	assert.Nil(t, claimed, "a later callback carrying the flow's state has nothing to claim")

	status, body = doMCPRequest(t, sessions.revoke, fasthttp.MethodDelete, "", "no-such-row", false)
	require.Equal(t, fasthttp.StatusNotFound, status, body)
	assert.Contains(t, body, "MCP session not found")
}

// TestFlowStart_ExpiredOAuthFlowIsGone: starting an OAuth flow past its expiry answers 410, so the
// user restarts authentication, rather than an error that reads as an outage.
func TestFlowStart_ExpiredOAuthFlowIsGone(t *testing.T) {
	h, _ := newOpenAPITestHandler(t)
	ctx := context.Background()
	vk := "vk-1"
	expired := time.Now().Add(-time.Minute)
	require.NoError(t, h.store.ConfigStore.CreateOauthUserSession(ctx, &tables.TableMCPOauthFlow{
		ID: "flow-expired", MCPClientID: "client-1", OauthConfigID: "oauth-config-1", State: "state-expired",
		VirtualKeyID: &vk, FlowMode: "vk", Status: "pending",
		ExpiresAt: expired, CreatedAt: expired.Add(-15 * time.Minute), UpdatedAt: expired,
	}))
	h.store.OAuthProvider = oauth2.NewOAuth2Provider(h.store.ConfigStore, &mockLogger{})
	sessions := NewMCPSessionsHandler(h.store, nil, nil)

	status, body := doMCPRequest(t, sessions.flowStart, fasthttp.MethodGet, "", "flow-expired", false)
	assert.Equal(t, fasthttp.StatusGone, status, body)
	assert.Contains(t, body, "OAuth flow is no longer pending; restart authentication")
}

// evictionLog records every cache eviction a handler asks for, one line per call.
type evictionLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *evictionLog) add(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

func (l *evictionLog) logged() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

func (l *evictionLog) EvictOauthTokenCacheByID(_ context.Context, id string) { l.add("token " + id) }
func (l *evictionLog) EvictOauthTokenCacheByMCPClient(_ context.Context, id string) {
	l.add("tokens of " + id)
}
func (l *evictionLog) EvictMCPHeaderCredentialCacheByID(_ context.Context, id string) {
	l.add("header credential " + id)
}
func (l *evictionLog) EvictMCPHeaderCredentialCacheByMCPClient(_ context.Context, id string) {
	l.add("header credentials of " + id)
}
func (l *evictionLog) EvictMCPPerUserDiscoveryCache(_ context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) {
	l.add("discovery " + string(mode) + "/" + identity + "/" + mcpClientID)
}
func (l *evictionLog) EvictMCPPerUserDiscoveryCacheByMCPClient(_ context.Context, id string) {
	l.add("discoveries of " + id)
}

// seedDiscovery stores a discovery of clientID for mode and identity.
func seedDiscovery(t *testing.T, store configstore.ConfigStore, mode schemas.MCPAuthMode, identity, clientID string) {
	t.Helper()
	row := &tables.TableMCPPerUserDiscovery{
		MCPClientID:   clientID,
		AuthMode:      string(mode),
		Tools:         signInDiscovery("notes").Tools,
		DiscoveryHash: "hash-" + identity,
	}
	switch mode {
	case schemas.MCPAuthModeUser:
		row.UserID = schemas.Ptr(identity)
	case schemas.MCPAuthModeVK:
		row.VirtualKeyID = schemas.Ptr(identity)
	}
	require.NoError(t, store.UpsertMCPPerUserDiscovery(context.Background(), row))
}

// callRevoke runs route for the credential row id as the dashboard admin and
// returns the status.
func callRevoke(route func(*fasthttp.RequestCtx), id string) int {
	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodDelete)
	var ctx fasthttp.RequestCtx
	ctx.Init(&req, nil, nil)
	ctx.SetUserValue("id", id)
	route(&ctx)
	return ctx.Response.StatusCode()
}

// TestMCPSessionsRevoke_DropsTheRevokedCredentialsDiscovery revokes a caller's
// credential and their discovery with it: the row in the same write, and every
// node's cached copy through the cache manager. A session-mode credential keeps
// no discovery, so nothing is evicted for it.
func TestMCPSessionsRevoke_DropsTheRevokedCredentialsDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     schemas.MCPAuthMode
		identity string
		seed     func(t *testing.T, store configstore.ConfigStore, clientID string) string
	}{
		{
			name: "header credential", mode: schemas.MCPAuthModeUser, identity: "alice",
			seed: func(t *testing.T, store configstore.ConfigStore, clientID string) string {
				cred := &tables.TableMCPPerUserHeaderCredential{MCPClientID: clientID, AuthMode: "user", UserID: schemas.Ptr("alice"), Status: "active", HeadersJSON: `{"x-api-key":"k"}`}
				require.NoError(t, store.UpsertMCPPerUserHeaderCredential(context.Background(), cred))
				return cred.ID
			},
		},
		{
			name: "oauth token", mode: schemas.MCPAuthModeVK, identity: "vk-1",
			seed: func(t *testing.T, store configstore.ConfigStore, clientID string) string {
				token := &tables.TableMCPOauthToken{ID: "tok-vk", MCPClientID: clientID, AuthMode: "vk", VirtualKeyID: schemas.Ptr("vk-1"), Status: "active", AccessToken: "x", TokenType: "Bearer", OauthConfigID: "oauth-notes"}
				require.NoError(t, store.CreateOauthToken(context.Background(), token))
				return token.ID
			},
		},
		{
			name: "session-mode header credential", mode: schemas.MCPAuthModeSession, identity: "sess-1",
			seed: func(t *testing.T, store configstore.ConfigStore, clientID string) string {
				cred := &tables.TableMCPPerUserHeaderCredential{MCPClientID: clientID, AuthMode: "session", SessionID: "sess-1", Status: "active", HeadersJSON: `{"x-api-key":"k"}`}
				require.NoError(t, store.UpsertMCPPerUserHeaderCredential(context.Background(), cred))
				return cred.ID
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, store := newSignInConfig(t)
			client := headersNotesClient()
			createSignInClient(t, store, client, true)
			require.NoError(t, store.CreateVirtualKey(context.Background(), &tables.TableVirtualKey{ID: "vk-1", Name: "vk-1", Value: *schemas.NewSecretVar("sk-bf-vk-1"), IsActive: schemas.Ptr(true)}))
			rowID := tc.seed(t, store, client.ID)
			seedDiscovery(t, store, schemas.MCPAuthModeUser, "bob", client.ID)
			if tc.mode != schemas.MCPAuthModeSession {
				seedDiscovery(t, store, tc.mode, tc.identity, client.ID)
			}
			log := &evictionLog{}

			require.Equal(t, fasthttp.StatusNoContent, callRevoke(NewMCPSessionsHandler(cfg, log, nil).revoke, rowID))

			assert.Nil(t, storedDiscovery(store, tc.mode, tc.identity, client.ID))
			assert.NotNil(t, storedDiscovery(store, schemas.MCPAuthModeUser, "bob", client.ID), "other callers keep theirs")
			binding := "discovery " + string(tc.mode) + "/" + tc.identity + "/" + client.ID
			if tc.mode == schemas.MCPAuthModeSession {
				assert.NotContains(t, strings.Join(log.logged(), "\n"), "discovery ")
			} else {
				assert.Contains(t, log.logged(), binding, "every node drops its cached copy")
			}
		})
	}
}

// TestMCPSessionsRevoke_HoldsAgainstASignInFinishingDuringIt revokes a caller's
// credential while their sign-in to the same client is completing: the revoke
// lands during the OAuth code exchange, or during the verification of submitted
// headers. Revoke deletes the caller's flows before the credential, so the
// completion that was under way must not store the credential it produced.
func TestMCPSessionsRevoke_HoldsAgainstASignInFinishingDuringIt(t *testing.T) {
	t.Run("oauth token", func(t *testing.T) {
		cfg, store := newSignInConfig(t)
		const existingID = "tok-alice"

		// The token endpoint is where the code exchange waits on the provider:
		// the revoke runs there, before the new token comes back.
		revoke := NewMCPSessionsHandler(cfg, &evictionLog{}, nil).revoke
		var revokeStatus atomic.Int32
		tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			revokeStatus.Store(int32(callRevoke(revoke, existingID)))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "alice-token", "token_type": "bearer", "expires_in": 3600})
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
		client := &schemas.MCPClientConfig{
			ID:               "client-notes",
			Name:             "notes",
			ConnectionType:   schemas.MCPConnectionTypeHTTP,
			ConnectionString: schemas.NewSecretVar("http://upstream.invalid/mcp"),
			AuthType:         schemas.MCPAuthTypePerUserOauth,
			OauthConfigID:    schemas.Ptr("oauth-notes"),
			ToolsToExecute:   []string{"*"},
		}
		createSignInClient(t, store, client, false)
		require.NoError(t, store.CreateOauthToken(context.Background(), &tables.TableMCPOauthToken{ID: existingID, MCPClientID: client.ID, AuthMode: "user", UserID: schemas.Ptr("alice"), Status: "active", AccessToken: "old", TokenType: "Bearer", OauthConfigID: "oauth-notes"}))
		createUserOAuthFlow(t, store, client.ID, "alice", "state-1")

		location := callOAuthCallback(t, NewOAuthHandler(cfg.OAuthProvider, nil, cfg, &signInMCPManager{}), "state-1")

		require.Equal(t, int32(fasthttp.StatusNoContent), revokeStatus.Load())
		token, err := store.GetOauthUserTokenByMode(context.Background(), schemas.MCPAuthModeUser, "alice", client.ID)
		require.NoError(t, err)
		assert.Nil(t, token, "the revoke holds: the sign-in that was finishing stores no token")
		assert.NotContains(t, location, "auth-success")
	})

	t.Run("header credential", func(t *testing.T) {
		cfg, store := newSignInConfig(t)
		client := headersNotesClient()
		createSignInClient(t, store, client, false)
		existing := &tables.TableMCPPerUserHeaderCredential{MCPClientID: client.ID, AuthMode: "user", UserID: schemas.Ptr("alice"), Status: "active", HeadersJSON: `{"x-api-key":"old"}`}
		require.NoError(t, store.UpsertMCPPerUserHeaderCredential(context.Background(), existing))
		createHeadersFlow(t, store, &tables.TableMCPPerUserHeaderFlow{ID: "flow-1", MCPClientID: client.ID, FlowMode: string(schemas.MCPAuthModeUser), UserID: schemas.Ptr("alice")})

		// Verification dials the upstream with the submitted values: the revoke
		// runs while it does.
		revoke := NewMCPSessionsHandler(cfg, &evictionLog{}, nil).revoke
		var revokeStatus int
		manager := &signInMCPManager{duringVerify: func() { revokeStatus = callRevoke(revoke, existing.ID) }}

		status, body := callSubmitHeaders(NewMCPPerUserHeadersHandler(manager, nil, cfg, nil), "flow-1", "alice")

		require.Equal(t, fasthttp.StatusNoContent, revokeStatus)
		assert.Equal(t, fasthttp.StatusNotFound, status, string(body))
		cred, err := store.GetMCPPerUserHeaderCredentialByMode(context.Background(), schemas.MCPAuthModeUser, "alice", client.ID)
		require.NoError(t, err)
		assert.Nil(t, cred, "the revoke holds: the submit that was finishing stores no credential")
	})
}

// countingDiscoverySummaries counts the per-user discovery summary reads made
// through it.
type countingDiscoverySummaries struct {
	configstore.ConfigStore
	reads atomic.Int32
}

func (c *countingDiscoverySummaries) GetMCPPerUserDiscoverySummaries(ctx context.Context, bindings []configstore.MCPPerUserDiscoveryBinding) (map[configstore.MCPPerUserDiscoveryBinding]tables.TableMCPPerUserDiscovery, error) {
	c.reads.Add(1)
	return c.ConfigStore.GetMCPPerUserDiscoverySummaries(ctx, bindings)
}

// callList runs the sessions list as the dashboard admin with query and
// returns the page it answered with.
func callList(t *testing.T, h *MCPSessionsHandler, query string) mcpSessionsListResponse {
	t.Helper()
	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodGet)
	req.SetRequestURI("/api/mcp/sessions?" + query)
	var ctx fasthttp.RequestCtx
	ctx.Init(&req, nil, nil)
	h.list(&ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	var page mcpSessionsListResponse
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &page))
	return page
}

// TestMCPSessionsList_CarriesEachCallersDiscovery lists credential rows with the
// discovery their binding holds on a client with per_user_discovery on, read
// once for the whole page. A pending flow has no credential yet, a session-bound
// caller keeps no discovery, and a client without the flag keeps one shared
// discovery, so none of those rows carries one.
func TestMCPSessionsList_CarriesEachCallersDiscovery(t *testing.T) {
	cfg, store := newSignInConfig(t)
	counting := &countingDiscoverySummaries{ConfigStore: store}
	cfg.ConfigStore = counting
	ctx := context.Background()

	notes := headersNotesClient()
	createSignInClient(t, store, notes, true)
	wiki := headersNotesClient()
	wiki.ID, wiki.Name = "client-wiki", "wiki"
	createSignInClient(t, store, wiki, false)
	require.NoError(t, store.CreateVirtualKey(ctx, &tables.TableVirtualKey{ID: "vk-1", Name: "vk-1", Value: *schemas.NewSecretVar("sk-bf-vk-1"), IsActive: schemas.Ptr(true)}))

	headerCredential := func(clientID string, mode schemas.MCPAuthMode, identity string) {
		cred := &tables.TableMCPPerUserHeaderCredential{MCPClientID: clientID, AuthMode: string(mode), Status: "active", HeadersJSON: `{"x-api-key":"k"}`}
		switch mode {
		case schemas.MCPAuthModeUser:
			cred.UserID = schemas.Ptr(identity)
		case schemas.MCPAuthModeVK:
			cred.VirtualKeyID = schemas.Ptr(identity)
		case schemas.MCPAuthModeSession:
			cred.SessionID = identity
		}
		require.NoError(t, store.UpsertMCPPerUserHeaderCredential(ctx, cred))
	}
	headerCredential(notes.ID, schemas.MCPAuthModeUser, "alice")
	seedDiscovery(t, store, schemas.MCPAuthModeUser, "alice", notes.ID)
	headerCredential(notes.ID, schemas.MCPAuthModeVK, "vk-1")
	seedDiscovery(t, store, schemas.MCPAuthModeVK, "vk-1", notes.ID)
	headerCredential(notes.ID, schemas.MCPAuthModeUser, "bob")
	headerCredential(notes.ID, schemas.MCPAuthModeSession, "sess-1")
	headerCredential(wiki.ID, schemas.MCPAuthModeUser, "alice")
	seedDiscovery(t, store, schemas.MCPAuthModeUser, "alice", wiki.ID)
	require.NoError(t, store.CreateOauthToken(ctx, &tables.TableMCPOauthToken{ID: "tok-erin", MCPClientID: notes.ID, AuthMode: "user", UserID: schemas.Ptr("erin"), Status: "active", AccessToken: "x", TokenType: "Bearer", OauthConfigID: "oauth-notes"}))
	seedDiscovery(t, store, schemas.MCPAuthModeUser, "erin", notes.ID)
	createHeadersFlow(t, store, &tables.TableMCPPerUserHeaderFlow{ID: "flow-dave", MCPClientID: notes.ID, FlowMode: string(schemas.MCPAuthModeUser), UserID: schemas.Ptr("dave")})

	h := NewMCPSessionsHandler(cfg, &evictionLog{}, nil)
	rows := map[string]mcpSessionRow{}
	for _, row := range callList(t, h, "").Sessions {
		identity := ""
		switch {
		case row.UserID != nil:
			identity = *row.UserID
		case row.VirtualKey != nil:
			identity = row.VirtualKey.ID
		case row.SessionID != nil:
			identity = *row.SessionID
		}
		rows[row.MCPClient.Name+" "+row.Kind+" "+identity] = row
	}
	require.Len(t, rows, 7)

	for _, key := range []string{"notes header alice", "notes header vk-1", "notes token erin"} {
		row := rows[key]
		assert.True(t, row.MCPClient.PerUserDiscovery, key)
		require.NotNil(t, row.Discovery, key)
		assert.Equal(t, 1, row.Discovery.ToolCount, key)
		assert.Zero(t, row.Discovery.PromptCount, key)
		assert.Zero(t, row.Discovery.ResourceCount, key)
		assert.NotEmpty(t, row.Discovery.DiscoveredAt, key)
	}
	for key, why := range map[string]string{
		"notes header bob":    "bob has not connected since signing in",
		"notes header sess-1": "a session-bound caller keeps no discovery",
		"notes flow dave":     "a pending sign-in has no credential yet",
	} {
		assert.True(t, rows[key].MCPClient.PerUserDiscovery, key)
		assert.Nil(t, rows[key].Discovery, why)
	}
	assert.False(t, rows["wiki header alice"].MCPClient.PerUserDiscovery)
	assert.Nil(t, rows["wiki header alice"].Discovery, "a client without the flag keeps one shared discovery")
	assert.EqualValues(t, 1, counting.reads.Load(), "one read for the whole page")

	// A page without a row that can hold a discovery reads none.
	counting.reads.Store(0)
	page := callList(t, h, "mcp_client_id="+wiki.ID)
	require.Len(t, page.Sessions, 1)
	assert.Zero(t, counting.reads.Load())
}

// callRefresh runs h's refresh for the session row id as the dashboard caller callerUserID, or as
// one with no user identity when it is empty, and returns the status and body.
func callRefresh(h *MCPSessionsHandler, id, callerUserID string) (int, []byte) {
	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodPost)
	var ctx fasthttp.RequestCtx
	ctx.Init(&req, nil, nil)
	ctx.SetUserValue("id", id)
	if callerUserID != "" {
		ctx.SetUserValue(schemas.BifrostContextKeyUserID, callerUserID)
	}
	h.refresh(&ctx)
	return ctx.Response.StatusCode(), append([]byte(nil), ctx.Response.Body()...)
}

// refreshCase is a caller's credential row on a client with per_user_discovery on, of one kind.
type refreshCase struct {
	name string
	// seed stores the row for mode and identity on the client it creates, returning the client and
	// the row's id.
	seed func(t *testing.T, store configstore.ConfigStore, mode schemas.MCPAuthMode, identity string) (*schemas.MCPClientConfig, string)
	// handed is the stored credential the verification was given, as the fake manager recorded it.
	handed func(m *signInMCPManager) []string
}

var refreshCases = []refreshCase{
	{
		name: "header credential",
		seed: func(t *testing.T, store configstore.ConfigStore, mode schemas.MCPAuthMode, identity string) (*schemas.MCPClientConfig, string) {
			client := headersNotesClient()
			createSignInClient(t, store, client, true)
			cred := &tables.TableMCPPerUserHeaderCredential{MCPClientID: client.ID, AuthMode: string(mode), Status: "active", HeadersJSON: `{"x-api-key":"stored-key"}`}
			if mode == schemas.MCPAuthModeUser {
				cred.UserID = schemas.Ptr(identity)
			} else {
				cred.VirtualKeyID = schemas.Ptr(identity)
			}
			require.NoError(t, store.UpsertMCPPerUserHeaderCredential(context.Background(), cred))
			return client, cred.ID
		},
		handed: func(m *signInMCPManager) []string {
			m.mu.Lock()
			defer m.mu.Unlock()
			var out []string
			for _, headers := range m.headers {
				for _, value := range headers {
					out = append(out, value)
				}
			}
			return out
		},
	},
	{
		name: "oauth token",
		seed: func(t *testing.T, store configstore.ConfigStore, mode schemas.MCPAuthMode, identity string) (*schemas.MCPClientConfig, string) {
			createOAuthConfig(t, store, "unused")
			client := &schemas.MCPClientConfig{
				ID:               "client-notes",
				Name:             "notes",
				ConnectionType:   schemas.MCPConnectionTypeHTTP,
				ConnectionString: schemas.NewSecretVar("http://upstream.invalid/mcp"),
				AuthType:         schemas.MCPAuthTypePerUserOauth,
				OauthConfigID:    schemas.Ptr("oauth-notes"),
				ToolsToExecute:   []string{"*"},
			}
			createSignInClient(t, store, client, true)
			token := &tables.TableMCPOauthToken{ID: "tok-" + identity, MCPClientID: client.ID, AuthMode: string(mode), Status: "active", AccessToken: "stored-token", TokenType: "Bearer", OauthConfigID: "oauth-notes"}
			if mode == schemas.MCPAuthModeUser {
				token.UserID = schemas.Ptr(identity)
			} else {
				token.VirtualKeyID = schemas.Ptr(identity)
			}
			require.NoError(t, store.CreateOauthToken(context.Background(), token))
			return client, token.ID
		},
		handed: func(m *signInMCPManager) []string {
			m.mu.Lock()
			defer m.mu.Unlock()
			return append([]string(nil), m.tokens...)
		},
	},
}

// newRefreshConfig returns a sign-in config whose store holds the virtual key vk-1.
func newRefreshConfig(t *testing.T) (*lib.Config, configstore.ConfigStore) {
	t.Helper()
	cfg, store := newSignInConfig(t)
	require.NoError(t, store.CreateVirtualKey(context.Background(), &tables.TableVirtualKey{ID: "vk-1", Name: "vk-1", Value: *schemas.NewSecretVar("sk-bf-vk-1"), IsActive: schemas.Ptr(true)}))
	return cfg, store
}

// TestMCPSessionsRefresh_DiscoversTheCallersToolsAgainWithTheirStoredCredential refreshes a
// virtual key's session on a client with per_user_discovery on: its tools, prompts and resources
// are discovered again with the credential the row stores, the new discovery replaces the old one,
// and the answer gives the new counts. A virtual-key session is refreshed by whoever can see it.
func TestMCPSessionsRefresh_DiscoversTheCallersToolsAgainWithTheirStoredCredential(t *testing.T) {
	for _, tc := range refreshCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, store := newRefreshConfig(t)
			client, rowID := tc.seed(t, store, schemas.MCPAuthModeVK, "vk-1")
			seedDiscovery(t, store, schemas.MCPAuthModeVK, "vk-1", client.ID)
			manager := &signInMCPManager{}

			status, body := callRefresh(NewMCPSessionsHandler(cfg, &evictionLog{}, manager), rowID, "")

			require.Equal(t, fasthttp.StatusOK, status, string(body))
			var counts struct {
				ToolCount     int `json:"tool_count"`
				PromptCount   int `json:"prompt_count"`
				ResourceCount int `json:"resource_count"`
			}
			require.NoError(t, json.Unmarshal(body, &counts))
			assert.Equal(t, 1, counts.ToolCount)
			assert.Equal(t, 1, counts.PromptCount)
			assert.Equal(t, 1, counts.ResourceCount)
			handed := tc.handed(manager)
			require.Len(t, handed, 1, "one discovery, with the stored credential")
			assert.Contains(t, handed[0], "stored-")
			row := storedDiscovery(store, schemas.MCPAuthModeVK, "vk-1", client.ID)
			require.NotNil(t, row)
			requireSignInResourcesAndPrompts(t, row)
		})
	}
}

// TestMCPSessionsRefresh_OnlyTheBoundUserRefreshesTheirOwnSession refreshes a user's session the
// way it is reauthorized: only that user may, since the refresh discovers with their credential.
// Anyone else, a dashboard caller with no user identity included, is refused before anything is
// dialed, and the user's discovery stays as it was.
func TestMCPSessionsRefresh_OnlyTheBoundUserRefreshesTheirOwnSession(t *testing.T) {
	for _, tc := range refreshCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, store := newRefreshConfig(t)
			client, rowID := tc.seed(t, store, schemas.MCPAuthModeUser, "alice")
			seedDiscovery(t, store, schemas.MCPAuthModeUser, "alice", client.ID)
			manager := &signInMCPManager{}
			h := NewMCPSessionsHandler(cfg, &evictionLog{}, manager)

			for _, other := range []string{"bob", ""} {
				status, body := callRefresh(h, rowID, other)
				assert.Equal(t, fasthttp.StatusForbidden, status, "caller %q: %s", other, body)
			}
			assert.Empty(t, tc.handed(manager), "nothing was dialed for them")
			assert.Empty(t, storedDiscovery(store, schemas.MCPAuthModeUser, "alice", client.ID).Prompts, "alice's discovery is as it was")

			status, body := callRefresh(h, rowID, "alice")
			require.Equal(t, fasthttp.StatusOK, status, string(body))
			requireSignInResourcesAndPrompts(t, storedDiscovery(store, schemas.MCPAuthModeUser, "alice", client.ID))
		})
	}
}

// TestMCPSessionsRefresh_KeepsTheDiscoveryWhenDiscoveringFails: a refresh that cannot discover,
// because the upstream is down or refuses the credential, answers with the reason and takes nothing
// away. The caller keeps the tools their credential last discovered.
func TestMCPSessionsRefresh_KeepsTheDiscoveryWhenDiscoveringFails(t *testing.T) {
	for _, tc := range refreshCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, store := newRefreshConfig(t)
			client, rowID := tc.seed(t, store, schemas.MCPAuthModeVK, "vk-1")
			seedDiscovery(t, store, schemas.MCPAuthModeVK, "vk-1", client.ID)
			manager := &signInMCPManager{verifyErr: errors.New("upstream unavailable")}

			status, body := callRefresh(NewMCPSessionsHandler(cfg, &evictionLog{}, manager), rowID, "")

			assert.Equal(t, fasthttp.StatusBadGateway, status, string(body))
			assert.Contains(t, string(body), "upstream unavailable")
			row := storedDiscovery(store, schemas.MCPAuthModeVK, "vk-1", client.ID)
			require.NotNil(t, row, "the caller keeps their discovery")
			assert.Equal(t, "hash-vk-1", row.DiscoveryHash, "unchanged")
		})
	}
}

// TestMCPSessionsRefresh_StoresNothingForACredentialRevokedDuringIt revokes the session while its
// refresh is discovering: the credential is read again before the discovery is stored, as a
// sign-in does, so the revoke holds and leaves the caller no discovery.
func TestMCPSessionsRefresh_StoresNothingForACredentialRevokedDuringIt(t *testing.T) {
	for _, tc := range refreshCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, store := newRefreshConfig(t)
			client, rowID := tc.seed(t, store, schemas.MCPAuthModeVK, "vk-1")
			seedDiscovery(t, store, schemas.MCPAuthModeVK, "vk-1", client.ID)
			revoke := NewMCPSessionsHandler(cfg, &evictionLog{}, nil).revoke
			var revokeStatus int
			manager := &signInMCPManager{duringVerify: func() { revokeStatus = callRevoke(revoke, rowID) }}

			status, body := callRefresh(NewMCPSessionsHandler(cfg, &evictionLog{}, manager), rowID, "")

			require.Equal(t, fasthttp.StatusNoContent, revokeStatus)
			assert.Equal(t, fasthttp.StatusConflict, status, string(body))
			assert.Nil(t, storedDiscovery(store, schemas.MCPAuthModeVK, "vk-1", client.ID), "the revoke holds")
		})
	}
}

// TestMCPSessionsRefresh_UsesTheTokenItsReadRefreshed: reading a session's expired access token
// refreshes it, and storing the new token moves the row's updated_at. That write is the read's own,
// made before discovery starts, so the refresh discovers with the new token instead of taking the
// moved row for a revoke or a replacement.
func TestMCPSessionsRefresh_UsesTheTokenItsReadRefreshed(t *testing.T) {
	cfg, store := newRefreshConfig(t)
	createOAuthConfig(t, store, "fresh-token")
	client := &schemas.MCPClientConfig{
		ID:               "client-notes",
		Name:             "notes",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar("http://upstream.invalid/mcp"),
		AuthType:         schemas.MCPAuthTypePerUserOauth,
		OauthConfigID:    schemas.Ptr("oauth-notes"),
		ToolsToExecute:   []string{"*"},
	}
	createSignInClient(t, store, client, true)
	expired := time.Now().Add(-time.Hour)
	token := &tables.TableMCPOauthToken{ID: "tok-vk-1", MCPClientID: client.ID, AuthMode: string(schemas.MCPAuthModeVK), VirtualKeyID: schemas.Ptr("vk-1"),
		Status: "active", AccessToken: "stale-token", RefreshToken: "refresh-1", TokenType: "Bearer", OauthConfigID: "oauth-notes", ExpiresAt: &expired}
	require.NoError(t, store.CreateOauthToken(context.Background(), token))
	seedDiscovery(t, store, schemas.MCPAuthModeVK, "vk-1", client.ID)
	manager := &signInMCPManager{}

	status, body := callRefresh(NewMCPSessionsHandler(cfg, &evictionLog{}, manager), token.ID, "")

	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, []string{"fresh-token"}, manager.tokens, "discovered with the refreshed token")
	requireSignInResourcesAndPrompts(t, storedDiscovery(store, schemas.MCPAuthModeVK, "vk-1", client.ID))
}

// TestMCPSessionsRefresh_RefusesWhatItCannotRefresh: only an active credential of a user or a virtual
// key, on a client that keeps a discovery per caller and is serving callers, can be refreshed.
// Everything else is refused before anything is dialed.
func TestMCPSessionsRefresh_RefusesWhatItCannotRefresh(t *testing.T) {
	headerRow := func(t *testing.T, store configstore.ConfigStore, client *schemas.MCPClientConfig, mode schemas.MCPAuthMode, status string) string {
		cred := &tables.TableMCPPerUserHeaderCredential{MCPClientID: client.ID, AuthMode: string(mode), Status: status, HeadersJSON: `{"x-api-key":"k"}`}
		switch mode {
		case schemas.MCPAuthModeVK:
			cred.VirtualKeyID = schemas.Ptr("vk-1")
		case schemas.MCPAuthModeSession:
			cred.SessionID = "sess-1"
		}
		require.NoError(t, store.UpsertMCPPerUserHeaderCredential(context.Background(), cred))
		return cred.ID
	}
	for _, tc := range []struct {
		name   string
		seed   func(t *testing.T, store configstore.ConfigStore) string
		status int
	}{
		{name: "an unknown session", status: fasthttp.StatusNotFound, seed: func(*testing.T, configstore.ConfigStore) string { return "no-such-row" }},
		{name: "a client without per-user discovery", status: fasthttp.StatusBadRequest, seed: func(t *testing.T, store configstore.ConfigStore) string {
			client := headersNotesClient()
			createSignInClient(t, store, client, false)
			return headerRow(t, store, client, schemas.MCPAuthModeVK, "active")
		}},
		{name: "a client awaiting admin verification", status: fasthttp.StatusBadRequest, seed: func(t *testing.T, store configstore.ConfigStore) string {
			client := headersNotesClient()
			createSignInClient(t, store, client, true)
			awaitAdminVerification(t, store, client.ID)
			return headerRow(t, store, client, schemas.MCPAuthModeVK, "active")
		}},
		{name: "headers that need updating", status: fasthttp.StatusBadRequest, seed: func(t *testing.T, store configstore.ConfigStore) string {
			client := headersNotesClient()
			createSignInClient(t, store, client, true)
			return headerRow(t, store, client, schemas.MCPAuthModeVK, "needs_update")
		}},
		{name: "a session-bound caller, who keeps no discovery", status: fasthttp.StatusBadRequest, seed: func(t *testing.T, store configstore.ConfigStore) string {
			client := headersNotesClient()
			createSignInClient(t, store, client, true)
			return headerRow(t, store, client, schemas.MCPAuthModeSession, "active")
		}},
		{name: "a token that needs reauthorizing", status: fasthttp.StatusBadRequest, seed: func(t *testing.T, store configstore.ConfigStore) string {
			client, rowID := refreshCases[1].seed(t, store, schemas.MCPAuthModeVK, "vk-1")
			require.NoError(t, store.DB().Model(&tables.TableMCPOauthToken{}).Where("id = ? AND mcp_client_id = ?", rowID, client.ID).Update("status", "needs_reauth").Error)
			return rowID
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, store := newRefreshConfig(t)
			rowID := tc.seed(t, store)
			manager := &signInMCPManager{}

			status, body := callRefresh(NewMCPSessionsHandler(cfg, &evictionLog{}, manager), rowID, "")

			assert.Equal(t, tc.status, status, string(body))
			assert.Empty(t, manager.tokens, "nothing was dialed")
			assert.Empty(t, manager.headers, "nothing was dialed")
		})
	}
}

// TestMCPSessionsList_SaysWhichSessionsCanBeRefreshed: each row's can_refresh says what refresh
// accepts for it and its caller, so the page offers the action only where it would work.
func TestMCPSessionsList_SaysWhichSessionsCanBeRefreshed(t *testing.T) {
	cfg, store := newRefreshConfig(t)
	ctx := context.Background()
	notes := headersNotesClient()
	createSignInClient(t, store, notes, true)
	wiki := headersNotesClient()
	wiki.ID, wiki.Name = "client-wiki", "wiki"
	createSignInClient(t, store, wiki, false)
	docs := headersNotesClient()
	docs.ID, docs.Name = "client-docs", "docs"
	createSignInClient(t, store, docs, true)
	awaitAdminVerification(t, store, docs.ID)
	credential := func(clientID string, mode schemas.MCPAuthMode, identity, status string) {
		cred := &tables.TableMCPPerUserHeaderCredential{MCPClientID: clientID, AuthMode: string(mode), Status: status, HeadersJSON: `{"x-api-key":"k"}`}
		switch mode {
		case schemas.MCPAuthModeUser:
			cred.UserID = schemas.Ptr(identity)
		case schemas.MCPAuthModeVK:
			cred.VirtualKeyID = schemas.Ptr(identity)
		case schemas.MCPAuthModeSession:
			cred.SessionID = identity
		}
		require.NoError(t, store.UpsertMCPPerUserHeaderCredential(ctx, cred))
	}
	credential(notes.ID, schemas.MCPAuthModeVK, "vk-1", "active")
	credential(notes.ID, schemas.MCPAuthModeUser, "alice", "active")
	credential(notes.ID, schemas.MCPAuthModeUser, "bob", "needs_update")
	credential(notes.ID, schemas.MCPAuthModeSession, "sess-1", "active")
	credential(wiki.ID, schemas.MCPAuthModeVK, "vk-1", "active")
	credential(docs.ID, schemas.MCPAuthModeVK, "vk-1", "active")
	createHeadersFlow(t, store, &tables.TableMCPPerUserHeaderFlow{ID: "flow-dave", MCPClientID: notes.ID, FlowMode: string(schemas.MCPAuthModeUser), UserID: schemas.Ptr("dave")})

	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodGet)
	req.SetRequestURI("/api/mcp/sessions")
	var rctx fasthttp.RequestCtx
	rctx.Init(&req, nil, nil)
	rctx.SetUserValue(schemas.BifrostContextKeyUserID, "alice")
	NewMCPSessionsHandler(cfg, &evictionLog{}, nil).list(&rctx)
	require.Equal(t, fasthttp.StatusOK, rctx.Response.StatusCode(), string(rctx.Response.Body()))
	var page mcpSessionsListResponse
	require.NoError(t, json.Unmarshal(rctx.Response.Body(), &page))

	refreshable := map[string]bool{}
	for _, row := range page.Sessions {
		identity := ""
		switch {
		case row.UserID != nil:
			identity = *row.UserID
		case row.VirtualKey != nil:
			identity = row.VirtualKey.ID
		case row.SessionID != nil:
			identity = *row.SessionID
		}
		refreshable[row.MCPClient.Name+" "+row.Kind+" "+identity] = row.CanRefresh
	}
	assert.Equal(t, map[string]bool{
		"notes header vk-1":   true,  // a virtual key's session, by anyone
		"notes header alice":  true,  // the caller's own session
		"notes header bob":    false, // needs updated headers first
		"notes header sess-1": false, // a session-bound caller keeps no discovery
		"wiki header vk-1":    false, // a client without per-user discovery
		"docs header vk-1":    false, // a client awaiting admin verification serves no caller
		"notes flow dave":     false, // a pending sign-in has no credential yet
	}, refreshable)
}
