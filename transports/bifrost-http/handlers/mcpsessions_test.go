package handlers

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/oauth2"
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
	sessions := NewMCPSessionsHandler(h.store, nil)

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
	sessions := NewMCPSessionsHandler(h.store, nil)

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
