package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
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
	case schemas.MCPAuthModeSession:
		row.SessionID = identity
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

			require.Equal(t, fasthttp.StatusNoContent, callRevoke(NewMCPSessionsHandler(cfg, log).revoke, rowID))

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
		revoke := NewMCPSessionsHandler(cfg, &evictionLog{}).revoke
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
		revoke := NewMCPSessionsHandler(cfg, &evictionLog{}).revoke
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
	seedDiscovery(t, store, schemas.MCPAuthModeSession, "sess-1", notes.ID)
	headerCredential(wiki.ID, schemas.MCPAuthModeUser, "alice")
	seedDiscovery(t, store, schemas.MCPAuthModeUser, "alice", wiki.ID)
	require.NoError(t, store.CreateOauthToken(ctx, &tables.TableMCPOauthToken{ID: "tok-erin", MCPClientID: notes.ID, AuthMode: "user", UserID: schemas.Ptr("erin"), Status: "active", AccessToken: "x", TokenType: "Bearer", OauthConfigID: "oauth-notes"}))
	seedDiscovery(t, store, schemas.MCPAuthModeUser, "erin", notes.ID)
	createHeadersFlow(t, store, &tables.TableMCPPerUserHeaderFlow{ID: "flow-dave", MCPClientID: notes.ID, FlowMode: string(schemas.MCPAuthModeUser), UserID: schemas.Ptr("dave")})

	h := NewMCPSessionsHandler(cfg, &evictionLog{})
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
