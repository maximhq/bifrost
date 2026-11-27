package handlers

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// rotationTestGovernanceManager reloads the rotated key straight from the
// store, which is all RotateVirtualKey needs from the manager here.
type rotationTestGovernanceManager struct {
	pricingOverrideTestGovernanceManager
	store configstore.ConfigStore
}

func (m rotationTestGovernanceManager) ReloadVirtualKey(ctx context.Context, id string) (*configstoreTables.TableVirtualKey, error) {
	return m.store.GetVirtualKey(ctx, id)
}

func newRotationTestStore(t *testing.T) *configstore.RDBConfigStore {
	t.Helper()
	store, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { store.Close(context.Background()) })
	rdb, ok := store.(*configstore.RDBConfigStore)
	require.True(t, ok, "sqlite config store is the RDB implementation")
	return rdb
}

func seedRotationRefreshToken(t *testing.T, store *configstore.RDBConfigStore, id, bfMode, bfSub string) {
	t.Helper()
	require.NoError(t, store.DB().Create(&configstoreTables.TableOAuth2RefreshToken{
		ID:        id,
		TokenHash: "hash-" + id,
		FamilyID:  "family-" + id,
		ClientID:  "client",
		BfMode:    bfMode,
		BfSub:     bfSub,
		Scope:     "mcp",
		Resource:  "https://bifrost.test/mcp",
		CreatedAt: time.Now(),
	}).Error)
}

// TestRotateVirtualKeyRevokesItsOAuthGrants: a vk-mode MCP OAuth grant is bound
// to the key's row id, so it is only as retired as the rotation makes it. Rotating
// the value revokes every grant that key minted; grants of other keys and of
// other identity modes are untouched.
func TestRotateVirtualKeyRevokesItsOAuthGrants(t *testing.T) {
	store := newRotationTestStore(t)
	ctx := context.Background()
	active := true
	for _, id := range []string{"vk-rotated", "vk-other"} {
		require.NoError(t, store.CreateVirtualKey(ctx, &configstoreTables.TableVirtualKey{
			ID:       id,
			Name:     id,
			Value:    *schemas.NewSecretVar("sk-bf-" + id),
			IsActive: &active,
		}))
	}
	seedRotationRefreshToken(t, store, "grant-1", string(schemas.MCPAuthModeVK), "vk-rotated")
	seedRotationRefreshToken(t, store, "grant-2", string(schemas.MCPAuthModeVK), "vk-rotated")
	seedRotationRefreshToken(t, store, "grant-other-key", string(schemas.MCPAuthModeVK), "vk-other")
	seedRotationRefreshToken(t, store, "grant-user", string(schemas.MCPAuthModeUser), "vk-rotated")

	// A consented, not yet exchanged authorization code for the rotated key.
	require.NoError(t, store.DB().Create(&configstoreTables.TableOAuth2AuthorizeRequest{
		ID: "ar-rotated", ClientID: "client", RedirectURI: "http://127.0.0.1/cb", State: "s", Scope: "mcp",
		Resource: "https://bifrost.test/mcp", CodeChallenge: "ch", CodeChallengeMethod: "S256",
		Status: configstoreTables.OAuth2AuthorizeRequestStatusConsented, CodeHash: schemas.Ptr("code-rotated"),
		BfMode: string(schemas.MCPAuthModeVK), BfSub: "vk-rotated",
		ExpiresAt: time.Now().Add(time.Minute), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}).Error)

	rotator := NewVirtualKeyRotator(store, rotationTestGovernanceManager{store: store})
	rotated, err := rotator.RotateVirtualKey(ctx, "vk-rotated")
	require.NoError(t, err)

	// The pending code cannot be exchanged for a fresh grant under the new value.
	err = store.ConsumeOAuth2AuthorizeRequest(ctx, "ar-rotated", &configstoreTables.TableOAuth2RefreshToken{
		ID: "grant-late", TokenHash: "hash-grant-late", FamilyID: "ar-rotated", ClientID: "client",
		BfMode: string(schemas.MCPAuthModeVK), BfSub: "vk-rotated", Scope: "mcp", Resource: "https://bifrost.test/mcp", CreatedAt: time.Now(),
	})
	assert.ErrorIs(t, err, configstore.ErrNotFound)
	assert.NotEqual(t, "sk-bf-vk-rotated", rotated.Value.GetValue(), "the value is rotated as before")

	// Both of the rotated key's grants are revoked...
	_, err = store.GetOAuth2RefreshTokenByHash(ctx, "hash-grant-1")
	assert.ErrorIs(t, err, configstore.ErrNotFound)
	_, err = store.GetOAuth2RefreshTokenByHash(ctx, "hash-grant-2")
	assert.ErrorIs(t, err, configstore.ErrNotFound)
	// ...and the rows are kept as revoked rather than deleted, so replay detection still works.
	revoked, err := store.GetOAuth2RefreshTokenByHashAny(ctx, "hash-grant-1")
	require.NoError(t, err)
	assert.NotNil(t, revoked.RevokedAt)

	// Another key's grant and a user-mode grant survive.
	_, err = store.GetOAuth2RefreshTokenByHash(ctx, "hash-grant-other-key")
	require.NoError(t, err)
	_, err = store.GetOAuth2RefreshTokenByHash(ctx, "hash-grant-user")
	require.NoError(t, err)
}

// TestRotateVirtualKeyLeavesKeyUntouchedWhenGrantRevocationFails: grant revocation
// runs before the key value is persisted, so a store failure there leaves the key,
// the database and in-memory governance exactly as they were.
func TestRotateVirtualKeyLeavesKeyUntouchedWhenGrantRevocationFails(t *testing.T) {
	SetLogger(&mockLogger{})
	active := true
	store := &mockRotateConfigStore{
		virtualKeys: map[string]*configstoreTables.TableVirtualKey{
			"vk-1": {ID: "vk-1", Name: "Production", Value: *schemas.NewSecretVar("sk-bf-old"), IsActive: &active},
		},
		revokeErr: errors.New("grants table unavailable"),
	}
	manager := &mockRotateGovernanceManager{store: store}

	_, err := NewVirtualKeyRotator(store, manager).RotateVirtualKey(context.Background(), "vk-1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "grants table unavailable")
	assert.Equal(t, 0, store.updates, "the key value must not be persisted when its grants could not be revoked")
	assert.Equal(t, "sk-bf-old", store.virtualKeys["vk-1"].Value.GetValue())
}

// failingUpdateStore is a real SQLite config store whose virtual-key update fails, standing in for
// any write error that lands after grant revocation has already run.
type failingUpdateStore struct {
	*configstore.RDBConfigStore
}

func (s failingUpdateStore) UpdateVirtualKey(context.Context, *configstoreTables.TableVirtualKey, ...*gorm.DB) error {
	return errors.New("virtual key write failed")
}

// TestRotateVirtualKeyRollsBackGrantRevocationWhenKeyUpdateFails: revoking the key's grants and
// storing its new value commit together. If the key cannot be stored, the grants are not revoked
// either, so the database never holds the old key with its grants gone.
func TestRotateVirtualKeyRollsBackGrantRevocationWhenKeyUpdateFails(t *testing.T) {
	rdb := newRotationTestStore(t)
	ctx := context.Background()
	active := true
	require.NoError(t, rdb.CreateVirtualKey(ctx, &configstoreTables.TableVirtualKey{
		ID: "vk-rotated", Name: "vk-rotated", Value: *schemas.NewSecretVar("sk-bf-vk-rotated"), IsActive: &active,
	}))
	seedRotationRefreshToken(t, rdb, "grant-1", string(schemas.MCPAuthModeVK), "vk-rotated")

	store := failingUpdateStore{rdb}
	_, err := NewVirtualKeyRotator(store, rotationTestGovernanceManager{store: rdb}).RotateVirtualKey(ctx, "vk-rotated")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "virtual key write failed")
	_, err = rdb.GetOAuth2RefreshTokenByHash(ctx, "hash-grant-1")
	assert.NoError(t, err, "the grant must survive a rotation that did not store the new key")
	stored, err := rdb.GetVirtualKey(ctx, "vk-rotated")
	require.NoError(t, err)
	assert.Equal(t, "sk-bf-vk-rotated", stored.Value.GetValue())
}
