package configstore

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetAdminOauthTokenByMCPClientID pins the admin-mode counterpart of
// GetSharedOauthTokenByConfigID: it must resolve the auth_mode='admin' row
// for an MCP client and must not cross-match a 'shared'-mode row on the same
// mcp_client_id, since the two modes back entirely different callers
// (GetAdminAccessToken vs GetAccessToken).
func TestGetAdminOauthTokenByMCPClientID(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
		ID: "tok-admin", AuthMode: "admin", OauthConfigID: "cfg-1", MCPClientID: "client-1", Status: "active",
		AccessToken: "at-admin", TokenType: "Bearer", CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
		ID: "tok-shared", AuthMode: "shared", OauthConfigID: "cfg-1", MCPClientID: "client-1", Status: "active",
		AccessToken: "at-shared", TokenType: "Bearer", CreatedAt: now, UpdatedAt: now,
	}).Error)

	got, err := s.GetAdminOauthTokenByMCPClientID(ctx, "client-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "tok-admin", got.ID, "must resolve the admin-mode row, not the shared-mode row for the same client")

	// A client with no admin-mode row at all resolves to (nil, nil).
	got, err = s.GetAdminOauthTokenByMCPClientID(ctx, "client-no-admin")
	require.NoError(t, err)
	assert.Nil(t, got)

	// Empty mcpClientID short-circuits to (nil, nil) without querying.
	got, err = s.GetAdminOauthTokenByMCPClientID(ctx, "")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestGetAdminOauthTokensByMCPClientIDs pins the batch counterpart of
// GetAdminOauthTokenByMCPClientID: one query keyed by mcp_client_id, admin
// rows only, no status filter (needs_reauth rows must come back so the
// registry list can project them), and empty input short-circuits.
func TestGetAdminOauthTokensByMCPClientIDs(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	now := time.Now()

	rows := []*tables.TableMCPOauthToken{
		{ID: "tok-a-active", AuthMode: "admin", MCPClientID: "client-a", Status: "active",
			AccessToken: "x", TokenType: "Bearer", CreatedAt: now, UpdatedAt: now},
		// token_exchange-shaped admin row: no oauth_config_id at all.
		{ID: "tok-b-reauth", AuthMode: "admin", MCPClientID: "client-b", Status: "needs_reauth",
			AccessToken: "x", TokenType: "Bearer", CreatedAt: now, UpdatedAt: now},
		// Non-admin rows on the same clients must be excluded.
		{ID: "tok-a-shared", AuthMode: "shared", MCPClientID: "client-a", Status: "active",
			AccessToken: "x", TokenType: "Bearer", CreatedAt: now, UpdatedAt: now},
		// A client with only a shared row must be absent from the result.
		{ID: "tok-c-shared", AuthMode: "shared", MCPClientID: "client-c", Status: "active",
			AccessToken: "x", TokenType: "Bearer", CreatedAt: now, UpdatedAt: now},
	}
	for _, r := range rows {
		require.NoError(t, s.DB().Create(r).Error)
	}

	got, err := s.GetAdminOauthTokensByMCPClientIDs(ctx, []string{"client-a", "client-b", "client-c", "client-missing"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.NotNil(t, got["client-a"])
	assert.Equal(t, "tok-a-active", got["client-a"].ID)
	assert.Equal(t, "active", got["client-a"].Status)
	require.NotNil(t, got["client-b"], "needs_reauth admin rows must be returned, not filtered by status")
	assert.Equal(t, "tok-b-reauth", got["client-b"].ID)
	assert.Equal(t, "needs_reauth", got["client-b"].Status)
	assert.NotContains(t, got, "client-c", "a client with only non-admin rows must be absent")
	assert.NotContains(t, got, "client-missing")

	// Empty input returns an empty map without querying.
	got, err = s.GetAdminOauthTokensByMCPClientIDs(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestGetAdminMCPPerUserHeaderCredentialsByClientIDs pins the batch
// counterpart of GetMCPPerUserHeaderCredentialByMode's admin branch: one
// query keyed by mcp_client_id, admin rows only, no status filter
// (needs_update rows must come back so the registry list can project them),
// and empty input short-circuits.
func TestGetAdminMCPPerUserHeaderCredentialsByClientIDs(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	now := time.Now()
	uid := "user-1"

	rows := []*tables.TableMCPPerUserHeaderCredential{
		{ID: "cred-a-active", MCPClientID: "client-a", AuthMode: "admin", Status: "active",
			HeadersJSON: "{}", CreatedAt: now, UpdatedAt: now},
		{ID: "cred-b-update", MCPClientID: "client-b", AuthMode: "admin", Status: "needs_update",
			HeadersJSON: "{}", CreatedAt: now, UpdatedAt: now},
		// Non-admin rows on the same clients must be excluded.
		{ID: "cred-a-user", MCPClientID: "client-a", AuthMode: "user", UserID: &uid, Status: "active",
			HeadersJSON: "{}", CreatedAt: now, UpdatedAt: now},
		// A client with only a per-identity row must be absent from the result.
		{ID: "cred-c-user", MCPClientID: "client-c", AuthMode: "user", UserID: &uid, Status: "needs_update",
			HeadersJSON: "{}", CreatedAt: now, UpdatedAt: now},
	}
	for _, r := range rows {
		require.NoError(t, s.DB().Create(r).Error)
	}

	got, err := s.GetAdminMCPPerUserHeaderCredentialsByClientIDs(ctx, []string{"client-a", "client-b", "client-c", "client-missing"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.NotNil(t, got["client-a"])
	assert.Equal(t, "cred-a-active", got["client-a"].ID)
	require.NotNil(t, got["client-b"], "needs_update admin rows must be returned, not filtered by status")
	assert.Equal(t, "cred-b-update", got["client-b"].ID)
	assert.Equal(t, "needs_update", got["client-b"].Status)
	assert.NotContains(t, got, "client-c", "a client with only non-admin rows must be absent")
	assert.NotContains(t, got, "client-missing")

	// Empty input returns an empty map without querying.
	got, err = s.GetAdminMCPPerUserHeaderCredentialsByClientIDs(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestPromoteSharedOauthTokenToAdmin_ReplacesExistingAdminRow pins the
// repair shape: a fresh active shared row's credential fields are copied
// onto the existing admin row (which keeps its ID and CreatedAt, since the
// row represents the binding), the admin row returns to 'active', and the
// shared row is deleted so exactly one row remains for the config.
func TestPromoteSharedOauthTokenToAdmin_ReplacesExistingAdminRow(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	adminCreated := time.Now().Add(-24 * time.Hour)

	sharedExpiresAt := time.Now().Add(2 * time.Hour)
	require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
		ID: "tok-admin", AuthMode: "admin", OauthConfigID: "cfg-1", MCPClientID: "client-1",
		Status: "needs_reauth", AccessToken: "at-dead", RefreshToken: "rt-dead",
		TokenType: "MAC", Scopes: `["dead"]`, EncryptionStatus: tables.EncryptionStatusPlainText,
		CreatedAt: adminCreated, UpdatedAt: adminCreated,
	}).Error)
	require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
		ID: "tok-shared", AuthMode: "shared", OauthConfigID: "cfg-1", Status: "active",
		AccessToken: "at-fresh", RefreshToken: "rt-fresh", TokenType: "Bearer",
		ExpiresAt: &sharedExpiresAt, Scopes: `["read","write"]`,
		EncryptionStatus: tables.EncryptionStatusEncrypted,
	}).Error)

	require.NoError(t, s.PromoteSharedOauthTokenToAdmin(ctx, "cfg-1", "client-1"))

	admin, err := s.GetAdminOauthTokenByMCPClientID(ctx, "client-1")
	require.NoError(t, err)
	require.NotNil(t, admin)
	assert.Equal(t, "tok-admin", admin.ID, "existing admin row's ID must be preserved")
	assert.True(t, adminCreated.Equal(admin.CreatedAt), "existing admin row's CreatedAt must be preserved: got %v, want %v", admin.CreatedAt, adminCreated)
	assert.Equal(t, "active", admin.Status)
	assert.Equal(t, "at-fresh", admin.AccessToken)
	assert.Equal(t, "rt-fresh", admin.RefreshToken)
	assert.Equal(t, "client-1", admin.MCPClientID)
	assert.Equal(t, "Bearer", admin.TokenType, "TokenType must be transferred from the shared row")
	require.NotNil(t, admin.ExpiresAt, "ExpiresAt must be transferred from the shared row")
	assert.True(t, sharedExpiresAt.Equal(*admin.ExpiresAt), "ExpiresAt must be transferred from the shared row: got %v, want %v", admin.ExpiresAt, sharedExpiresAt)
	assert.Equal(t, `["read","write"]`, admin.Scopes, "Scopes must be transferred from the shared row")
	assert.Equal(t, tables.EncryptionStatusEncrypted, admin.EncryptionStatus, "EncryptionStatus must be transferred from the shared row")
	assert.NotNil(t, admin.LastRefreshedAt, "repair must stamp LastRefreshedAt")

	shared, err := s.GetSharedOauthTokenByConfigID(ctx, "cfg-1")
	require.NoError(t, err)
	assert.Nil(t, shared, "the promoted shared row must be deleted")

	var count int64
	require.NoError(t, s.DB().Model(&tables.TableMCPOauthToken{}).
		Where("oauth_config_id = ?", "cfg-1").Count(&count).Error)
	assert.Equal(t, int64(1), count, "exactly one row must remain for the config")
}

// TestPromoteSharedOauthTokenToAdmin_RetagsWhenNoAdminRow pins the
// first-time-bootstrap shape: with no admin row present the shared row
// itself is retagged to auth_mode='admin' with MCPClientID set.
func TestPromoteSharedOauthTokenToAdmin_RetagsWhenNoAdminRow(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
		ID: "tok-shared", AuthMode: "shared", OauthConfigID: "cfg-1", Status: "active",
		AccessToken: "at-fresh", TokenType: "Bearer",
	}).Error)

	require.NoError(t, s.PromoteSharedOauthTokenToAdmin(ctx, "cfg-1", "client-1"))

	admin, err := s.GetAdminOauthTokenByMCPClientID(ctx, "client-1")
	require.NoError(t, err)
	require.NotNil(t, admin)
	assert.Equal(t, "tok-shared", admin.ID, "the shared row itself must be retagged, keeping its ID")
	assert.Equal(t, "client-1", admin.MCPClientID)
	assert.Equal(t, "active", admin.Status)

	shared, err := s.GetSharedOauthTokenByConfigID(ctx, "cfg-1")
	require.NoError(t, err)
	assert.Nil(t, shared, "no shared row must remain after promotion")
}

// TestPromoteSharedOauthTokenToAdmin_ClearsStrayShareRows pins the fix for a
// stale/orphaned extra shared row: nothing in the schema guarantees at most
// one auth_mode='shared' row per oauth_config_id, so promotion must select
// the active row (not an arbitrary one) and delete every remaining shared
// row for the config, not just the one it promoted — otherwise
// GetSharedOauthTokenByConfigID keeps returning a shared row after a
// "completed" promotion, which the OAuth-callback replay guard would read as
// a new repair flow instead of a finished one. Covers both the
// replace-existing-admin-row and first-time-retag branches.
func TestPromoteSharedOauthTokenToAdmin_ClearsStrayShareRows(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()

	t.Run("replace-admin-row branch", func(t *testing.T) {
		require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
			ID: "tok-admin-1", AuthMode: "admin", OauthConfigID: "cfg-1", MCPClientID: "client-1",
			Status: "needs_reauth", AccessToken: "at-dead", TokenType: "Bearer",
		}).Error)
		// A stale shared row (older, non-active) that a bare First() with no
		// ORDER BY could select instead of the active one below.
		require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
			ID: "tok-shared-stale", AuthMode: "shared", OauthConfigID: "cfg-1", Status: "needs_reauth",
			AccessToken: "at-stale", TokenType: "Bearer",
		}).Error)
		require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
			ID: "tok-shared-active", AuthMode: "shared", OauthConfigID: "cfg-1", Status: "active",
			AccessToken: "at-fresh", TokenType: "Bearer",
		}).Error)

		require.NoError(t, s.PromoteSharedOauthTokenToAdmin(ctx, "cfg-1", "client-1"))

		admin, err := s.GetAdminOauthTokenByMCPClientID(ctx, "client-1")
		require.NoError(t, err)
		require.NotNil(t, admin)
		assert.Equal(t, "at-fresh", admin.AccessToken, "the active shared row must be the one promoted, not the stale one")

		shared, err := s.GetSharedOauthTokenByConfigID(ctx, "cfg-1")
		require.NoError(t, err)
		assert.Nil(t, shared, "every shared row for the config must be cleared, including the stray stale one")

		var sharedCount int64
		require.NoError(t, s.DB().Model(&tables.TableMCPOauthToken{}).
			Where("oauth_config_id = ? AND auth_mode = ?", "cfg-1", "shared").Count(&sharedCount).Error)
		assert.Equal(t, int64(0), sharedCount)
	})

	t.Run("first-time-retag branch", func(t *testing.T) {
		require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
			ID: "tok-shared-stale-2", AuthMode: "shared", OauthConfigID: "cfg-2", Status: "needs_reauth",
			AccessToken: "at-stale-2", TokenType: "Bearer",
		}).Error)
		require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
			ID: "tok-shared-active-2", AuthMode: "shared", OauthConfigID: "cfg-2", Status: "active",
			AccessToken: "at-fresh-2", TokenType: "Bearer",
		}).Error)

		require.NoError(t, s.PromoteSharedOauthTokenToAdmin(ctx, "cfg-2", "client-2"))

		admin, err := s.GetAdminOauthTokenByMCPClientID(ctx, "client-2")
		require.NoError(t, err)
		require.NotNil(t, admin)
		assert.Equal(t, "tok-shared-active-2", admin.ID, "the active shared row must be retagged in place")

		shared, err := s.GetSharedOauthTokenByConfigID(ctx, "cfg-2")
		require.NoError(t, err)
		assert.Nil(t, shared, "the stray stale shared row must also be cleared")
	})
}

// TestPromoteSharedOauthTokenToAdmin_Errors pins the guard rails: an empty
// oauthConfigID or mcpClientID, a missing shared row, and a non-active shared
// row all error without touching an existing admin row.
func TestPromoteSharedOauthTokenToAdmin_Errors(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()

	require.Error(t, s.PromoteSharedOauthTokenToAdmin(ctx, "", "client-1"), "empty oauthConfigID must error before starting the transaction")
	require.Error(t, s.PromoteSharedOauthTokenToAdmin(ctx, "cfg-1", ""), "empty mcpClientID must error before starting the transaction")

	var count int64
	require.NoError(t, s.DB().Model(&tables.TableMCPOauthToken{}).Count(&count).Error)
	assert.Equal(t, int64(0), count, "the empty-argument guard must reject before any row is touched")

	require.Error(t, s.PromoteSharedOauthTokenToAdmin(ctx, "cfg-none", "client-1"), "missing shared row must error")

	require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
		ID: "tok-admin", AuthMode: "admin", OauthConfigID: "cfg-1", MCPClientID: "client-1",
		Status: "needs_reauth", AccessToken: "at-dead", TokenType: "Bearer",
	}).Error)
	require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
		ID: "tok-shared", AuthMode: "shared", OauthConfigID: "cfg-1", Status: "needs_reauth",
		AccessToken: "at-stale", TokenType: "Bearer",
	}).Error)

	require.Error(t, s.PromoteSharedOauthTokenToAdmin(ctx, "cfg-1", "client-1"), "non-active shared row must error")

	admin, err := s.GetAdminOauthTokenByMCPClientID(ctx, "client-1")
	require.NoError(t, err)
	require.NotNil(t, admin)
	assert.Equal(t, "needs_reauth", admin.Status, "a failed promotion must leave the admin row untouched")
	assert.Equal(t, "at-dead", admin.AccessToken)
}

// TestGetMCPPerUserHeaderCredentialByMode_AdminMode pins the new admin
// branch: an admin-mode row is scoped by mcp_client_id alone (identity is
// allowed empty), while every other mode is unaffected by the widening and
// still rejects an empty identity by returning (nil, nil).
func TestGetMCPPerUserHeaderCredentialByMode_AdminMode(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, s.DB().Create(&tables.TableMCPPerUserHeaderCredential{
		ID: "cred-admin", MCPClientID: "client-1", AuthMode: "admin", Status: "active",
		HeadersJSON: "{}", CreatedAt: now, UpdatedAt: now,
	}).Error)

	got, err := s.GetMCPPerUserHeaderCredentialByMode(ctx, schemas.MCPAuthModeAdmin, "", "client-1")
	require.NoError(t, err)
	require.NotNil(t, got, "admin mode must resolve by mcp_client_id alone, with an empty identity")
	assert.Equal(t, "cred-admin", got.ID)

	for _, mode := range []schemas.MCPAuthMode{schemas.MCPAuthModeUser, schemas.MCPAuthModeVK, schemas.MCPAuthModeSession} {
		got, err := s.GetMCPPerUserHeaderCredentialByMode(ctx, mode, "", "client-1")
		require.NoError(t, err)
		assert.Nil(t, got, "mode %s with an empty identity must still return nil, nil — unaffected by the admin widening", mode)
	}
}

// TestUpsertMCPPerUserHeaderCredential_AdminMode pins the new admin
// upsert-key case: since an admin-mode row has no per-caller identity to key
// on, a second upsert for the same mcp_client_id must update the SAME row
// (reusing its ID and preserving CreatedAt) instead of inserting a second
// row — which would violate the migration's new partial unique index on
// (mcp_client_id) WHERE auth_mode='admin'.
func TestUpsertMCPPerUserHeaderCredential_AdminMode(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()

	cred := &tables.TableMCPPerUserHeaderCredential{
		MCPClientID: "client-1",
		AuthMode:    "admin",
		Status:      "active",
	}
	require.NoError(t, cred.SetHeaders(map[string]string{"X-Api-Key": "v1"}))
	require.NoError(t, s.UpsertMCPPerUserHeaderCredential(ctx, cred))
	require.NotEmpty(t, cred.ID)
	firstID := cred.ID
	firstCreatedAt := cred.CreatedAt

	// Re-upsert with a fresh in-memory row (no ID set) for the same
	// mcp_client_id — the caller-side shape a re-submitted admin credential
	// takes (see credentialToRow, which always builds a fresh row).
	cred2 := &tables.TableMCPPerUserHeaderCredential{
		MCPClientID: "client-1",
		AuthMode:    "admin",
		Status:      "active",
	}
	require.NoError(t, cred2.SetHeaders(map[string]string{"X-Api-Key": "v2"}))
	require.NoError(t, s.UpsertMCPPerUserHeaderCredential(ctx, cred2))

	assert.Equal(t, firstID, cred2.ID, "second upsert for the same mcp_client_id must reuse the same row ID")
	// SQLite round-trips time.Time with a bare UTC-offset Location rather than
	// time.Local, so assert.Equal (which also compares the Location pointer)
	// would spuriously fail on an identical instant; .Equal compares the
	// instant only.
	assert.True(t, firstCreatedAt.Equal(cred2.CreatedAt), "second upsert must preserve the original CreatedAt: got %v, want %v", cred2.CreatedAt, firstCreatedAt)

	var count int64
	require.NoError(t, s.DB().Model(&tables.TableMCPPerUserHeaderCredential{}).
		Where("mcp_client_id = ? AND auth_mode = ?", "client-1", "admin").Count(&count).Error)
	assert.Equal(t, int64(1), count, "must not insert a duplicate row for the same mcp_client_id")

	got, err := s.GetMCPPerUserHeaderCredentialByMode(ctx, schemas.MCPAuthModeAdmin, "", "client-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	headers, err := got.GetHeaders()
	require.NoError(t, err)
	assert.Equal(t, "v2", headers["X-Api-Key"], "stored row must reflect the second upsert's values, confirming it was an update")
}
