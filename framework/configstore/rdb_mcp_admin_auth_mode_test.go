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

// TestGetAdminOauthTokenByConfigID pins the admin-mode counterpart of
// GetSharedOauthTokenByConfigID: it must resolve the auth_mode='admin' row
// for a config and must not cross-match a 'shared'-mode row on the same
// oauth_config_id, since the two modes back entirely different callers
// (GetAdminAccessToken vs GetAccessToken).
func TestGetAdminOauthTokenByConfigID(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
		ID: "tok-admin", AuthMode: "admin", OauthConfigID: "cfg-1", Status: "active",
		AccessToken: "at-admin", TokenType: "Bearer", CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
		ID: "tok-shared", AuthMode: "shared", OauthConfigID: "cfg-1", Status: "active",
		AccessToken: "at-shared", TokenType: "Bearer", CreatedAt: now, UpdatedAt: now,
	}).Error)

	got, err := s.GetAdminOauthTokenByConfigID(ctx, "cfg-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "tok-admin", got.ID, "must resolve the admin-mode row, not the shared-mode row for the same config")

	// A config with no admin-mode row at all resolves to (nil, nil).
	got, err = s.GetAdminOauthTokenByConfigID(ctx, "cfg-no-admin")
	require.NoError(t, err)
	assert.Nil(t, got)

	// Empty oauthConfigID short-circuits to (nil, nil) without querying.
	got, err = s.GetAdminOauthTokenByConfigID(ctx, "")
	require.NoError(t, err)
	assert.Nil(t, got)
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
