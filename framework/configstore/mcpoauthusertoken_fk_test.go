package configstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupOauthUserTokenFKStore opens a fresh in-memory SQLite DB with foreign_keys on
// (mirrors the production DSN in sqlite.go, unlike setupRDBTestStore's bare
// ":memory:" DSN, which leaves FK enforcement off and so cannot reproduce a
// constraint-violation bug), runs the full migration chain, and wraps it in a
// minimal RDBConfigStore.
func setupOauthUserTokenFKStore(t *testing.T) (*RDBConfigStore, *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	n := time.Now().UnixNano() + testDBCounter
	testDBCounter++
	dsn := fmt.Sprintf("file:oauthuserfk_%d?mode=memory&cache=shared&_foreign_keys=on", n)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, triggerMigrations(ctx, db, testMigrationLogger))

	s := &RDBConfigStore{}
	s.db.Store(db)
	s.migrateOnFreshFn = func(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
		return fn(ctx, s.DB())
	}
	s.refreshPoolFn = func(ctx context.Context) error { return nil }
	return s, db
}

// seedLegacyOauthUserToken plants a row in oauth_user_tokens the way pre-merge code
// used to write one: the shape migrationMergeOauthTokenTables's own comment says is
// left "completely untouched and undropped" by that migration, and that nothing
// currently deletes on an MCP client's behalf.
func seedLegacyOauthUserToken(t *testing.T, db *gorm.DB, id, mcpClientID, oauthConfigID string) {
	t.Helper()
	now := time.Now()
	require.NoError(t, db.Create(&tables.TableOauthUserToken{
		ID:            id,
		UserID:        stringPtr("user-legacy"),
		MCPClientID:   mcpClientID,
		AuthMode:      "user",
		Status:        "active",
		OauthConfigID: oauthConfigID,
		AccessToken:   "legacy-access-token",
		TokenType:     "Bearer",
		CreatedAt:     now,
		UpdatedAt:     now,
	}).Error)
}

func stringPtr(s string) *string { return &s }

// TestDeleteMCPClientConfig_LegacyOauthUserTokenRow reproduces the reported bug: an
// MCP client that had per-user OAuth activity before the mcp_oauth_tokens merge
// keeps an orphaned-but-FK-enforced row in oauth_user_tokens, and DeleteMCPClientConfig
// (which only cleans up mcp_oauth_tokens now) can no longer delete that client at all.
func TestDeleteMCPClientConfig_LegacyOauthUserTokenRow(t *testing.T) {
	store, db := setupOauthUserTokenFKStore(t)
	ctx := context.Background()

	client := tables.TableMCPClient{
		ClientID:       "client-legacy-peruser",
		Name:           "legacy-peruser-client",
		ConnectionType: "http",
		AuthType:       "per_user_oauth",
	}
	require.NoError(t, db.Create(&client).Error)
	seedLegacyOauthUserToken(t, db, "tok-legacy-1", client.ClientID, "cfg-legacy-1")

	err := store.DeleteMCPClientConfig(ctx, client.ClientID)
	require.NoError(t, err, "a client with only a legacy oauth_user_tokens row must still be deletable")

	var clientCount int64
	require.NoError(t, db.Model(&tables.TableMCPClient{}).Where("client_id = ?", client.ClientID).Count(&clientCount).Error)
	assert.Equal(t, int64(0), clientCount, "the MCP client row should be gone")

	// The legacy row itself is untouched — the fix removes the constraint, not the
	// data. migrationMergeOauthTokenTables explicitly kept oauth_user_tokens as a
	// rollback safety net; this pins that nothing here starts deleting from it.
	var legacyCount int64
	require.NoError(t, db.Model(&tables.TableOauthUserToken{}).Where("id = ?", "tok-legacy-1").Count(&legacyCount).Error)
	assert.Equal(t, int64(1), legacyCount, "the legacy oauth_user_tokens row must be left in place, not deleted")
}

// TestMigrationDropLegacyOauthUserFKConstraints_RemovesAllFour pins the migration's
// own effect directly. It cannot use setupOauthUserTokenFKStore/triggerMigrations for
// its starting state: the struct tags are already fixed, so the full chain's own
// table creation never produces these constraints in the first place, and a test
// that never creates them proves nothing about DropConstraint actually removing them
// (confirmed by temporarily disabling this migration's registry entry - both this
// test and TestDeleteMCPClientConfig_LegacyOauthUserTokenRow still passed). Instead,
// this builds the four tables directly and recreates each constraint by hand -
// exactly the starting state every deployment that ran the pre-fix
// migrationAddPerUserOAuthTables actually has - then calls the migration function
// directly (mirroring TestMigrationAddModelConfigBudgetsFKConstraint_PreCleansOrphans)
// so it isn't skipped by triggerMigrations' own already-applied bookkeeping either.
func TestMigrationDropLegacyOauthUserFKConstraints_RemovesAllFour(t *testing.T) {
	ctx := context.Background()
	n := time.Now().UnixNano() + testDBCounter
	testDBCounter++
	dsn := fmt.Sprintf("file:fkdroplegacy_%d?mode=memory&cache=shared", n)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&tables.TableMCPClient{},
		&tables.TableVirtualKey{},
		&tables.TableOauthUserToken{},
		&tables.TableOauthUserSession{},
	))
	mig := db.Migrator()

	targets := []struct {
		model any
		field string
	}{
		{&tables.TableOauthUserToken{}, "MCPClient"},
		{&tables.TableOauthUserToken{}, "VirtualKey"},
		{&tables.TableOauthUserSession{}, "MCPClient"},
		{&tables.TableOauthUserSession{}, "VirtualKey"},
	}
	for _, target := range targets {
		require.NoError(t, mig.CreateConstraint(target.model, target.field))
	}
	for _, target := range targets {
		require.True(t, mig.HasConstraint(target.model, target.field),
			"setup should have created the %s constraint on %T", target.field, target.model)
	}

	require.NoError(t, migrationDropLegacyOauthUserFKConstraints(ctx, db, testMigrationLogger))

	for _, target := range targets {
		assert.False(t, mig.HasConstraint(target.model, target.field),
			"the %s constraint on %T should be gone", target.field, target.model)
	}

	// Idempotent: already-clean must not error on a second pass.
	require.NoError(t, migrationDropLegacyOauthUserFKConstraints(ctx, db, testMigrationLogger))
}

// TestPostgresDeleteMCPClientConfig_LegacyOauthUserTokenRow is
// TestDeleteMCPClientConfig_LegacyOauthUserTokenRow's real-Postgres twin. The
// reported bug's own error (SQLSTATE 23503) only ever surfaces on Postgres — SQLite
// needs foreign_keys=on to enforce anything at all, which the production DSN sets
// but nothing here can verify beyond "the constraint API agrees it's gone" — so this
// is the one place that error class is actually reproduced and confirmed fixed.
//
// triggerMigrations alone is not enough to set that up: the struct tags are already
// fixed, so the chain's own table creation never produces the legacy constraint in
// the first place (same reasoning as TestMigrationDropLegacyOauthUserFKConstraints_
// RemovesAllFour above). Running the full chain and then re-adding just the one
// constraint this test exercises reproduces the exact pre-fix deployment state
// without hand-building every table DeleteMCPClientConfig touches.
func TestPostgresDeleteMCPClientConfig_LegacyOauthUserTokenRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := gorm.Open(postgres.Open(postgresDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Skipf("postgres not available: %v", err)
	}
	require.NoError(t, db.Exec("DROP SCHEMA IF EXISTS "+pgTestSchema+" CASCADE").Error)
	require.NoError(t, db.Exec("CREATE SCHEMA "+pgTestSchema).Error)
	require.NoError(t, triggerMigrations(ctx, db, testMigrationLogger))

	// Re-add the legacy constraint the fix migration just dropped, putting the schema
	// back into the state every deployment that ran the pre-fix
	// migrationAddPerUserOAuthTables actually has.
	require.NoError(t, db.Migrator().CreateConstraint(&tables.TableOauthUserToken{}, "MCPClient"))

	store := &RDBConfigStore{logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	store.db.Store(db)
	store.migrateOnFreshFn = func(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
		return fn(ctx, store.DB())
	}
	store.refreshPoolFn = func(ctx context.Context) error { return nil }

	client := tables.TableMCPClient{
		ClientID:       "client-legacy-peruser-pg",
		Name:           "legacy-peruser-client-pg",
		ConnectionType: "http",
		AuthType:       "per_user_oauth",
	}
	require.NoError(t, db.Create(&client).Error)
	seedLegacyOauthUserToken(t, db, "tok-legacy-pg-1", client.ClientID, "cfg-legacy-pg-1")

	// With the legacy constraint back in place, deletion must fail with the exact
	// SQLSTATE 23503 the reported bug hit — proving this test can actually reproduce
	// the original failure, not just assert the migration's after-state.
	delErr := store.DeleteMCPClientConfig(ctx, client.ClientID)
	require.Error(t, delErr, "deletion must fail while the legacy FK constraint is in place")
	assert.Contains(t, delErr.Error(), "23503", "failure must be the FK violation the reported bug hit")

	var clientCountBeforeFix int64
	require.NoError(t, db.Model(&tables.TableMCPClient{}).Where("client_id = ?", client.ClientID).Count(&clientCountBeforeFix).Error)
	assert.Equal(t, int64(1), clientCountBeforeFix, "the failed transaction must have rolled back, leaving the client row in place")

	// Drop the constraint directly through the migrator rather than calling
	// migrationDropLegacyOauthUserFKConstraints again: triggerMigrations already
	// recorded that migration ID as applied, so gormigrate's own bookkeeping would
	// silently skip a second invocation with the same ID — the constraint was only
	// put back by hand above, not by re-running the migration.
	// TestMigrationDropLegacyOauthUserFKConstraints_RemovesAllFour already covers the
	// migration function's own logic directly; this test's job is the end-to-end
	// SQLSTATE 23503 reproduction, not a second proof of that function's internals.
	require.NoError(t, db.Migrator().DropConstraint(&tables.TableOauthUserToken{}, "MCPClient"))

	require.NoError(t, store.DeleteMCPClientConfig(ctx, client.ClientID),
		"a client with only a legacy oauth_user_tokens row must be deletable once the constraint is dropped")

	var clientCount int64
	require.NoError(t, db.Model(&tables.TableMCPClient{}).Where("client_id = ?", client.ClientID).Count(&clientCount).Error)
	assert.Equal(t, int64(0), clientCount, "the MCP client row should be gone")

	var legacyCount int64
	require.NoError(t, db.Model(&tables.TableOauthUserToken{}).Where("id = ?", "tok-legacy-pg-1").Count(&legacyCount).Error)
	assert.Equal(t, int64(1), legacyCount, "the legacy oauth_user_tokens row must be left in place, not deleted")
}
