package configstore

import (
	"context"
	"testing"
	"time"

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
		Value: "sk-bf-alpha",
	}
	require.NoError(t, store.DB().WithContext(ctx).Create(vk).Error)

	// OAuth token rows — one active vk-mode, one orphaned user-mode, one needs_reauth session-mode
	vkID := "vk-alpha"
	uid := "user-42"
	tok1 := &tables.TableOauthUserToken{
		ID: "tok-active", MCPClientID: "github-prod", VirtualKeyID: &vkID, AuthMode: "vk",
		Status: "active", AccessToken: "x", TokenType: "Bearer", OauthConfigID: "cfg-1",
	}
	tok2 := &tables.TableOauthUserToken{
		ID: "tok-orphan", MCPClientID: "github-prod", UserID: &uid, AuthMode: "user",
		Status: "orphaned", AccessToken: "x", TokenType: "Bearer", OauthConfigID: "cfg-1",
	}
	tok3 := &tables.TableOauthUserToken{
		ID: "tok-reauth", MCPClientID: "github-prod", SessionID: "sess-xyz", AuthMode: "session",
		Status: "needs_reauth", AccessToken: "x", TokenType: "Bearer", OauthConfigID: "cfg-1",
	}
	require.NoError(t, store.DB().WithContext(ctx).Create(tok1).Error)
	require.NoError(t, store.DB().WithContext(ctx).Create(tok2).Error)
	require.NoError(t, store.DB().WithContext(ctx).Create(tok3).Error)

	// Pending OAuth session
	sess := &tables.TableOauthUserSession{
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

func TestListMCPPerUserHeaderCredentials_NoFilters(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	got, err := store.ListMCPPerUserHeaderCredentials(context.Background(), MCPSessionsFilterParams{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "cred-1", got[0].ID)
}

func TestListPendingMCPPerUserHeaderFlows_NoFilters(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	got, err := store.ListPendingMCPPerUserHeaderFlows(context.Background(), MCPSessionsFilterParams{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "hf-1", got[0].ID)
}
