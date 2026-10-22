package configstore

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// oauthFlowStateMigrationID is the id the state-uniqueness migration records.
const oauthFlowStateMigrationID = "make_mcp_oauth_flows_state_unique"

// createUpgradedOauthFlowsTable creates the mcp_oauth_flows shape an upgraded
// database has: the state column carries the NON-unique idx_mcp_oauth_flows_state
// the old struct tag built, which made the later CREATE UNIQUE INDEX IF NOT EXISTS
// a no-op.
func createUpgradedOauthFlowsTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE TABLE mcp_oauth_flows (
		id VARCHAR(255) PRIMARY KEY, state VARCHAR(255) NOT NULL, status VARCHAR(50) NOT NULL,
		expires_at TIMESTAMP NOT NULL)`).Error)
	require.NoError(t, db.Exec(`CREATE INDEX idx_mcp_oauth_flows_state ON mcp_oauth_flows (state)`).Error)
}

// insertOauthFlow inserts one flow row with the given state, status and expiry.
func insertOauthFlow(t *testing.T, db *gorm.DB, id, state, status string, expiresAt time.Time) {
	t.Helper()
	require.NoError(t, db.Exec("INSERT INTO mcp_oauth_flows (id, state, status, expires_at) VALUES (?, ?, ?, ?)",
		id, state, status, expiresAt).Error)
}

// oauthFlowIDs returns every flow id, sorted.
func oauthFlowIDs(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var ids []string
	require.NoError(t, db.Raw("SELECT id FROM mcp_oauth_flows ORDER BY id").Scan(&ids).Error)
	return ids
}

// runOauthFlowStateMigration runs migrationMakeMCPOauthFlowsStateUnique once.
func runOauthFlowStateMigration(ctx context.Context, db *gorm.DB) error {
	return migrationMakeMCPOauthFlowsStateUnique(ctx, db, testMigrationLogger)
}

// TestFollowupsOauthFlowStateTagIsUnique pins that a fresh install, which builds
// mcp_oauth_flows from struct tags, gets a UNIQUE idx_mcp_oauth_flows_state.
func TestFollowupsOauthFlowStateTagIsUnique(t *testing.T) {
	db := freshSQLiteDB(t)
	require.NoError(t, db.Migrator().CreateTable(&tables.TableMCPOauthFlow{}))
	require.True(t, indexIsUnique(t, db, "mcp_oauth_flows", "idx_mcp_oauth_flows_state"))
	require.Equal(t, []string{"state"}, indexColumns(t, db, "idx_mcp_oauth_flows_state"))
}

// TestFollowupsOauthFlowStateUniqueAfterAllMigrations pins that the full
// migration chain leaves state UNIQUE on both dialects.
func TestFollowupsOauthFlowStateUniqueAfterAllMigrations(t *testing.T) {
	for _, ns := range inListLimitStores(t) {
		t.Run(ns.name, func(t *testing.T) {
			require.True(t, indexIsUnique(t, ns.db, "mcp_oauth_flows", "idx_mcp_oauth_flows_state"))
			require.False(t, indexExists(t, ns.db, "mcp_oauth_flows", "idx_mcp_oauth_flows_state_unique"))
		})
	}
}

// TestFollowupsOauthFlowStateConvertsToUnique pins the upgrade path: a
// non-unique idx_mcp_oauth_flows_state becomes UNIQUE under the same name. An
// expired pending or claiming copy of a duplicated state, which the expiry sweep
// would delete anyway, is removed first; every other row, including an expired
// row whose state is not duplicated, is kept. Re-running is a no-op.
func TestFollowupsOauthFlowStateConvertsToUnique(t *testing.T) {
	for _, ndb := range indexMigrationDBs(t) {
		t.Run(ndb.name, func(t *testing.T) {
			db := ndb.db
			createUpgradedOauthFlowsTable(t, db)
			past := time.Now().Add(-time.Hour)
			future := time.Now().Add(time.Hour)
			insertOauthFlow(t, db, "f1", "s1", "pending", future)
			insertOauthFlow(t, db, "f2", "s1", "pending", past)  // expired dup: swept
			insertOauthFlow(t, db, "f3", "s2", "claiming", past) // expired dup: swept
			insertOauthFlow(t, db, "f4", "s2", "authorized", past)
			insertOauthFlow(t, db, "f5", "s3", "pending", past) // expired, not a dup: kept
			require.False(t, indexIsUnique(t, db, "mcp_oauth_flows", "idx_mcp_oauth_flows_state"))

			require.NoError(t, runOauthFlowStateMigration(context.Background(), db))
			require.True(t, indexIsUnique(t, db, "mcp_oauth_flows", "idx_mcp_oauth_flows_state"))
			require.False(t, indexExists(t, db, "mcp_oauth_flows", "idx_mcp_oauth_flows_state_unique"))
			require.Equal(t, []string{"f1", "f4", "f5"}, oauthFlowIDs(t, db))
			require.Error(t, db.Exec("INSERT INTO mcp_oauth_flows (id, state, status, expires_at) VALUES ('f6', 's1', 'pending', ?)", future).Error)

			require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", oauthFlowStateMigrationID).Error)
			require.NoError(t, runOauthFlowStateMigration(context.Background(), db))
			require.True(t, indexIsUnique(t, db, "mcp_oauth_flows", "idx_mcp_oauth_flows_state"))
		})
	}
}

// TestFollowupsOauthFlowStateKeepsNonUniqueOnLiveDuplicates pins the safety
// path: duplicated states the sweep would not delete make UNIQUE impossible, so
// the migration keeps the non-unique index, deletes nothing, and still succeeds.
func TestFollowupsOauthFlowStateKeepsNonUniqueOnLiveDuplicates(t *testing.T) {
	for _, ndb := range indexMigrationDBs(t) {
		t.Run(ndb.name, func(t *testing.T) {
			db := ndb.db
			createUpgradedOauthFlowsTable(t, db)
			future := time.Now().Add(time.Hour)
			insertOauthFlow(t, db, "f1", "dup", "pending", future)
			insertOauthFlow(t, db, "f2", "dup", "authorized", future)
			insertOauthFlow(t, db, "f3", "s3", "pending", future)

			require.NoError(t, runOauthFlowStateMigration(context.Background(), db))
			require.True(t, indexExists(t, db, "mcp_oauth_flows", "idx_mcp_oauth_flows_state"))
			require.False(t, indexIsUnique(t, db, "mcp_oauth_flows", "idx_mcp_oauth_flows_state"))
			require.False(t, indexExists(t, db, "mcp_oauth_flows", "idx_mcp_oauth_flows_state_unique"))
			require.Equal(t, []string{"f1", "f2", "f3"}, oauthFlowIDs(t, db))

			// The recovery the fallback warning describes: remove the duplicate,
			// delete the migration's ledger row, and the next run enforces uniqueness.
			require.NoError(t, db.Exec("DELETE FROM mcp_oauth_flows WHERE id = ?", "f2").Error)
			require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", oauthFlowStateMigrationID).Error)
			require.NoError(t, runOauthFlowStateMigration(context.Background(), db))
			require.True(t, indexIsUnique(t, db, "mcp_oauth_flows", "idx_mcp_oauth_flows_state"), "the retried migration must make state unique")
		})
	}
}

// TestFollowupsOauthFlowStateResumesAfterInterruptedSwap pins that a run which
// stopped after dropping the old index but before the rename finishes the swap
// on the next start instead of rebuilding.
func TestFollowupsOauthFlowStateResumesAfterInterruptedSwap(t *testing.T) {
	for _, ndb := range indexMigrationDBs(t) {
		t.Run(ndb.name, func(t *testing.T) {
			db := ndb.db
			require.NoError(t, db.Exec(`CREATE TABLE mcp_oauth_flows (
				id VARCHAR(255) PRIMARY KEY, state VARCHAR(255) NOT NULL, status VARCHAR(50) NOT NULL,
				expires_at TIMESTAMP NOT NULL)`).Error)
			require.NoError(t, db.Exec(`CREATE UNIQUE INDEX idx_mcp_oauth_flows_state_unique ON mcp_oauth_flows (state)`).Error)

			require.NoError(t, runOauthFlowStateMigration(context.Background(), db))
			require.True(t, indexIsUnique(t, db, "mcp_oauth_flows", "idx_mcp_oauth_flows_state"))
			require.False(t, indexExists(t, db, "mcp_oauth_flows", "idx_mcp_oauth_flows_state_unique"))
		})
	}
}

// TestFollowupsOauthFlowStateRollbackRefuses pins that rolling back the state
// uniqueness migration fails instead of reporting success: the migration deletes
// expired duplicate flows and swaps in a UNIQUE index, neither of which can be
// undone, and a nil rollback would let the runner drop the migration record.
func TestFollowupsOauthFlowStateRollbackRefuses(t *testing.T) {
	m := mcpOauthFlowsStateUniqueMigration(context.Background(), oauthFlowStateMigrationID, testMigrationLogger)
	err := m.Rollback(nil)
	require.Error(t, err, "an irreversible migration must refuse to roll back")
	require.Contains(t, err.Error(), "non-rollbackable")
}
