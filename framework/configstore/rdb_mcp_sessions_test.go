package configstore

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

// setupMCPSessionsTestStore extends setupRDBTestStore with the two
// per-user-headers tables. The shared setup intentionally leaves these
// off to keep unrelated tests fast.
func setupMCPSessionsTestStore(t *testing.T) *RDBConfigStore {
	store := setupRDBTestStore(t)
	require.NoError(t, store.DB().AutoMigrate(
		&tables.TableMCPPerUserHeaderCredential{},
		&tables.TableMCPPerUserHeaderFlow{},
	))
	return store
}

// seedMCPSessionsFixture creates one MCP client, one VK, and one row in
// each of the four sessions tables — wired together so search/filter
// tests have a meaningful joined dataset.
func seedMCPSessionsFixture(t *testing.T, store *RDBConfigStore) {
	t.Helper()
	ctx := context.Background()
	// MCP client
	mcp := &tables.TableMCPClient{
		ClientID:       "github-prod",
		Name:           "GitHub (Prod)",
		ConnectionType: "stdio",
	}
	require.NoError(t, store.DB().WithContext(ctx).Create(mcp).Error)

	// Virtual key
	vk := &tables.TableVirtualKey{
		ID:    "vk-alpha",
		Name:  "Alpha VK",
		Value: *schemas.NewSecretVar("sk-bf-alpha"),
	}
	require.NoError(t, store.DB().WithContext(ctx).Create(vk).Error)

	// OAuth token rows — one active vk-mode, one orphaned user-mode, one needs_reauth session-mode
	vkID := "vk-alpha"
	uid := "user-42"
	tok1 := &tables.TableMCPOauthToken{
		ID: "tok-active", MCPClientID: "github-prod", VirtualKeyID: &vkID, AuthMode: "vk",
		Status: "active", AccessToken: "x", TokenType: "Bearer", OauthConfigID: "cfg-1",
	}
	tok2 := &tables.TableMCPOauthToken{
		ID: "tok-orphan", MCPClientID: "github-prod", UserID: &uid, AuthMode: "user",
		Status: "orphaned", AccessToken: "x", TokenType: "Bearer", OauthConfigID: "cfg-1",
	}
	tok3 := &tables.TableMCPOauthToken{
		ID: "tok-reauth", MCPClientID: "github-prod", SessionID: "sess-xyz", AuthMode: "session",
		Status: "needs_reauth", AccessToken: "x", TokenType: "Bearer", OauthConfigID: "cfg-1",
	}
	require.NoError(t, store.DB().WithContext(ctx).Create(tok1).Error)
	require.NoError(t, store.DB().WithContext(ctx).Create(tok2).Error)
	require.NoError(t, store.DB().WithContext(ctx).Create(tok3).Error)

	// Pending OAuth flow
	sess := &tables.TableMCPOauthFlow{
		ID: "sess-pending", MCPClientID: "github-prod", OauthConfigID: "cfg-1",
		FlowMode: "user", Status: "pending", UserID: &uid,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, store.DB().WithContext(ctx).Create(sess).Error)

	// Header credential
	cred := &tables.TableMCPPerUserHeaderCredential{
		ID: "cred-1", MCPClientID: "github-prod", VirtualKeyID: &vkID, AuthMode: "vk",
		Status: "active", HeadersJSON: "{}",
	}
	require.NoError(t, store.DB().WithContext(ctx).Create(cred).Error)

	// Pending header flow
	hf := &tables.TableMCPPerUserHeaderFlow{
		ID: "hf-1", MCPClientID: "github-prod", FlowMode: "user", Status: "pending",
		UserID: &uid, ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, store.DB().WithContext(ctx).Create(hf).Error)
}

func TestListOauthUserTokens_NoFilters(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	got, err := store.ListOauthUserTokens(context.Background(), MCPSessionsFilterParams{})
	require.NoError(t, err)
	require.Len(t, got, 3)
}

func TestListOauthUserTokens_FilterByStatus(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	got, err := store.ListOauthUserTokens(context.Background(), MCPSessionsFilterParams{
		Statuses: []string{"orphaned", "needs_reauth"},
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
	for _, r := range got {
		require.NotEqual(t, "active", r.Status)
	}
}

func TestListOauthUserTokens_FilterByAuthMode(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	got, err := store.ListOauthUserTokens(context.Background(), MCPSessionsFilterParams{
		AuthModes: []string{"vk"},
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "tok-active", got[0].ID)
}

func TestListOauthUserTokens_FilterByMCPClient(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	got, err := store.ListOauthUserTokens(context.Background(), MCPSessionsFilterParams{
		MCPClientIDs: []string{"github-prod"},
	})
	require.NoError(t, err)
	require.Len(t, got, 3)
	got, err = store.ListOauthUserTokens(context.Background(), MCPSessionsFilterParams{
		MCPClientIDs: []string{"slack-dev"},
	})
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestListOauthUserTokens_SearchMatchesMCPClientName(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	got, err := store.ListOauthUserTokens(context.Background(), MCPSessionsFilterParams{
		Search: "GitHub",
	})
	require.NoError(t, err)
	require.Len(t, got, 3, "all three tokens have github mcp client; search by name should match all")
}

func TestListOauthUserTokens_SearchMatchesVKName(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	got, err := store.ListOauthUserTokens(context.Background(), MCPSessionsFilterParams{
		Search: "Alpha VK",
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "tok-active", got[0].ID, "only vk-mode token has a virtual_key_id; vk-name search should match only it")
}

func TestListOauthUserTokens_SearchMatchesUserID(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	got, err := store.ListOauthUserTokens(context.Background(), MCPSessionsFilterParams{
		Search: "user-42",
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "tok-orphan", got[0].ID)
}

func TestListOauthUserTokens_MatchedUserIDsBroadensSearch(t *testing.T) {
	// Search string "Alice" doesn't match any column on the row, but
	// MatchedUserIDs surfaces tok-orphan because its user_id ("user-42")
	// is in the supplied set — mirrors the caller-resolved-from-directory
	// case.
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	got, err := store.ListOauthUserTokens(context.Background(), MCPSessionsFilterParams{
		Search:         "Alice",
		MatchedUserIDs: []string{"user-42"},
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "tok-orphan", got[0].ID)
}

func TestListOauthUserTokens_MatchedUserIDsIgnoredWhenSearchEmpty(t *testing.T) {
	// Empty Search means the search WHERE branch never runs, so
	// MatchedUserIDs has no effect — the filter returns all rows.
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	got, err := store.ListOauthUserTokens(context.Background(), MCPSessionsFilterParams{
		MatchedUserIDs: []string{"user-42"},
	})
	require.NoError(t, err)
	require.Len(t, got, 3) // all seeded tokens
}

func TestListOauthUserTokens_SearchMatchesSessionID(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	got, err := store.ListOauthUserTokens(context.Background(), MCPSessionsFilterParams{
		Search: "sess-xyz",
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "tok-reauth", got[0].ID)
}

func TestListOauthUserTokens_PreloadsMCPClientAndVK(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	got, err := store.ListOauthUserTokens(context.Background(), MCPSessionsFilterParams{
		AuthModes: []string{"vk"},
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].MCPClient, "MCPClient preload should populate")
	require.Equal(t, "GitHub (Prod)", got[0].MCPClient.Name)
	require.NotNil(t, got[0].VirtualKey, "VirtualKey preload should populate")
	require.Equal(t, "Alpha VK", got[0].VirtualKey.Name)
}

func TestListPendingOauthUserSessions_OnlyReturnsPending(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	got, err := store.ListPendingOauthUserSessions(context.Background(), MCPSessionsFilterParams{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "sess-pending", got[0].ID)
}

func TestListPendingOauthUserSessions_FlowModeFilter(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	got, err := store.ListPendingOauthUserSessions(context.Background(), MCPSessionsFilterParams{
		AuthModes: []string{"vk"},
	})
	require.NoError(t, err)
	require.Empty(t, got, "fixture's only session is user-mode; vk filter should match none")
}

func TestGetOauthUserSessionByID_ExcludesAdminMode(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)

	// The fixture's own user-mode flow is still reachable by ID.
	got, err := store.GetOauthUserSessionByID(context.Background(), "sess-pending")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "sess-pending", got.ID)

	// An admin-mode flow (the shared client's or a bootstrap-test's one-time
	// setup flow) must never be reachable through this per-user-facing
	// lookup, even by exact ID — this is the same auth_mode discipline
	// GetOauthUserTokenByID already applies on the token side.
	adminFlow := &tables.TableMCPOauthFlow{
		ID: "sess-admin", MCPClientID: "github-prod", OauthConfigID: "cfg-1",
		FlowMode: "admin", Status: "pending",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, store.DB().WithContext(context.Background()).Create(adminFlow).Error)

	got, err = store.GetOauthUserSessionByID(context.Background(), "sess-admin")
	require.NoError(t, err)
	require.Nil(t, got, "admin-mode flow must not be returned by a per-user-facing ID lookup")
}

// TestGetOauthUserSessionByModeIdentityAndMCPClient_AdminMode covers the
// admin branch added alongside MCPAuthModeAdmin: unlike the per-identity
// modes, an admin-mode flow has no identity column at all, so the lookup is
// scoped by (flow_mode='admin', mcp_client_id) alone. It also pins that the
// pre-existing per-identity modes (user/vk/session) keep their original
// behavior — still rejecting an empty identity, still resolving their own
// rows by identity.
func TestGetOauthUserSessionByModeIdentityAndMCPClient_AdminMode(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	ctx := context.Background()

	client1 := &tables.TableMCPClient{ClientID: "mcp-1", Name: "MCP One", ConnectionType: "stdio"}
	client2 := &tables.TableMCPClient{ClientID: "mcp-2", Name: "MCP Two", ConnectionType: "stdio"}
	client3 := &tables.TableMCPClient{ClientID: "mcp-3", Name: "MCP Three", ConnectionType: "stdio"}
	require.NoError(t, store.DB().WithContext(ctx).Create(client1).Error)
	require.NoError(t, store.DB().WithContext(ctx).Create(client2).Error)
	require.NoError(t, store.DB().WithContext(ctx).Create(client3).Error)

	uid := "user-1"
	vkID := "vk-1"
	adminFlow1 := &tables.TableMCPOauthFlow{
		ID: "flow-admin-1", MCPClientID: "mcp-1", OauthConfigID: "cfg-1",
		FlowMode: "admin", Status: "pending", ExpiresAt: time.Now().Add(time.Hour),
	}
	adminFlow2 := &tables.TableMCPOauthFlow{
		ID: "flow-admin-2", MCPClientID: "mcp-2", OauthConfigID: "cfg-1",
		FlowMode: "admin", Status: "pending", ExpiresAt: time.Now().Add(time.Hour),
	}
	userFlow := &tables.TableMCPOauthFlow{
		ID: "flow-user-1", MCPClientID: "mcp-1", OauthConfigID: "cfg-1",
		FlowMode: "user", Status: "pending", UserID: &uid, ExpiresAt: time.Now().Add(time.Hour),
	}
	vkFlow := &tables.TableMCPOauthFlow{
		ID: "flow-vk-1", MCPClientID: "mcp-1", OauthConfigID: "cfg-1",
		FlowMode: "vk", Status: "pending", VirtualKeyID: &vkID, ExpiresAt: time.Now().Add(time.Hour),
	}
	sessionFlow := &tables.TableMCPOauthFlow{
		ID: "flow-session-1", MCPClientID: "mcp-1", OauthConfigID: "cfg-1",
		FlowMode: "session", Status: "pending", SessionID: "sess-tok-1", ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, store.DB().WithContext(ctx).Create(adminFlow1).Error)
	require.NoError(t, store.DB().WithContext(ctx).Create(adminFlow2).Error)
	require.NoError(t, store.DB().WithContext(ctx).Create(userFlow).Error)
	require.NoError(t, store.DB().WithContext(ctx).Create(vkFlow).Error)
	require.NoError(t, store.DB().WithContext(ctx).Create(sessionFlow).Error)

	// Admin mode is found with an empty identity — admin rows have none.
	got, err := store.GetOauthUserSessionByModeIdentityAndMCPClient(ctx, schemas.MCPAuthModeAdmin, "", "mcp-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "flow-admin-1", got.ID)

	// A different mcp_client_id's admin row is a distinct binding — resolves
	// to its own row, not mcp-1's.
	got, err = store.GetOauthUserSessionByModeIdentityAndMCPClient(ctx, schemas.MCPAuthModeAdmin, "", "mcp-2")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "flow-admin-2", got.ID)

	// A client with no admin row of its own must not fall through to another
	// client's admin row.
	got, err = store.GetOauthUserSessionByModeIdentityAndMCPClient(ctx, schemas.MCPAuthModeAdmin, "", "mcp-3")
	require.NoError(t, err)
	require.Nil(t, got)

	// Per-identity modes still require a non-empty identity (unaffected by
	// the admin branch's identity-optional carve-out).
	got, err = store.GetOauthUserSessionByModeIdentityAndMCPClient(ctx, schemas.MCPAuthModeUser, "", "mcp-1")
	require.NoError(t, err)
	require.Nil(t, got, "user mode must still require non-empty identity")

	got, err = store.GetOauthUserSessionByModeIdentityAndMCPClient(ctx, schemas.MCPAuthModeVK, "", "mcp-1")
	require.NoError(t, err)
	require.Nil(t, got, "vk mode must still require non-empty identity")

	got, err = store.GetOauthUserSessionByModeIdentityAndMCPClient(ctx, schemas.MCPAuthModeSession, "", "mcp-1")
	require.NoError(t, err)
	require.Nil(t, got, "session mode must still require non-empty identity")

	// Per-identity modes still resolve their own rows correctly by identity.
	got, err = store.GetOauthUserSessionByModeIdentityAndMCPClient(ctx, schemas.MCPAuthModeUser, uid, "mcp-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "flow-user-1", got.ID)

	got, err = store.GetOauthUserSessionByModeIdentityAndMCPClient(ctx, schemas.MCPAuthModeVK, vkID, "mcp-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "flow-vk-1", got.ID)

	got, err = store.GetOauthUserSessionByModeIdentityAndMCPClient(ctx, schemas.MCPAuthModeSession, "sess-tok-1", "mcp-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "flow-session-1", got.ID)
}

// TestGetOauthFlowByID covers GetOauthUserSessionByID's admin-mode
// counterpart, pinning the security boundary it exists for: a
// caller-supplied ID must only ever resolve a flow_mode='admin' row through
// this method, never a per-identity row, even when the ID is an exact match.
func TestGetOauthFlowByID(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	ctx := context.Background()

	adminFlow := &tables.TableMCPOauthFlow{
		ID: "flow-admin-x", MCPClientID: "github-prod", OauthConfigID: "cfg-1",
		FlowMode: "admin", Status: "pending", ExpiresAt: time.Now().Add(time.Hour),
	}
	vkID := "vk-alpha"
	vkFlow := &tables.TableMCPOauthFlow{
		ID: "flow-vk-x", MCPClientID: "github-prod", OauthConfigID: "cfg-1",
		FlowMode: "vk", Status: "pending", VirtualKeyID: &vkID, ExpiresAt: time.Now().Add(time.Hour),
	}
	sessionFlow := &tables.TableMCPOauthFlow{
		ID: "flow-session-x", MCPClientID: "github-prod", OauthConfigID: "cfg-1",
		FlowMode: "session", Status: "pending", SessionID: "sess-tok-x", ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, store.DB().WithContext(ctx).Create(adminFlow).Error)
	require.NoError(t, store.DB().WithContext(ctx).Create(vkFlow).Error)
	require.NoError(t, store.DB().WithContext(ctx).Create(sessionFlow).Error)

	// Finds a flow_mode='admin' row by ID.
	got, err := store.GetOauthFlowByID(ctx, "flow-admin-x")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "flow-admin-x", got.ID)

	// Returns nil for flow_mode='user'/'vk'/'session' rows even with the
	// correct ID — the security boundary this whole method exists for.
	for _, id := range []string{"sess-pending" /* user-mode, from the shared fixture */, "flow-vk-x", "flow-session-x"} {
		got, err := store.GetOauthFlowByID(ctx, id)
		require.NoError(t, err)
		require.Nil(t, got, "non-admin flow %q must not be returned by the admin-only ID lookup", id)
	}

	// Nonexistent ID.
	got, err = store.GetOauthFlowByID(ctx, "does-not-exist")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestListMCPPerUserHeaderCredentials_NoFilters(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	got, err := store.ListMCPPerUserHeaderCredentials(context.Background(), MCPSessionsFilterParams{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "cred-1", got[0].ID)
}

// TestListMCPPerUserHeaderCredentials_ExcludesAdminMode pins that the
// retained admin discovery credential (auth_mode='admin') never leaks into
// the per-identity sessions list, mirroring ListOauthUserTokens'
// auth_mode='shared' exclusion on the token table.
func TestListMCPPerUserHeaderCredentials_ExcludesAdminMode(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	require.NoError(t, store.DB().Create(&tables.TableMCPPerUserHeaderCredential{
		ID: "cred-admin", MCPClientID: "github-prod", AuthMode: "admin",
		Status: "active", HeadersJSON: "{}",
	}).Error)

	got, err := store.ListMCPPerUserHeaderCredentials(context.Background(), MCPSessionsFilterParams{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "cred-1", got[0].ID, "only the per-identity row must be listed; the admin row must be excluded")
}

func TestListPendingMCPPerUserHeaderFlows_NoFilters(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	got, err := store.ListPendingMCPPerUserHeaderFlows(context.Background(), MCPSessionsFilterParams{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "hf-1", got[0].ID)
}
