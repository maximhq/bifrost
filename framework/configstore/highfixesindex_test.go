package configstore

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// indexIsUnique reports whether the named index on table is a UNIQUE index, on either dialect.
func indexIsUnique(t *testing.T, db *gorm.DB, table, name string) bool {
	t.Helper()
	if db.Dialector.Name() == "postgres" {
		var unique bool
		require.NoError(t, db.Raw(`
			SELECT COALESCE(bool_and(pi.indisunique), false)
			FROM pg_class pc
			JOIN pg_index pi ON pi.indrelid = pc.oid
			JOIN pg_class ic ON ic.oid = pi.indexrelid
			WHERE pc.relname = ? AND ic.relname = ? AND pg_catalog.pg_table_is_visible(pc.oid)
		`, table, name).Scan(&unique).Error)
		return unique
	}
	type indexRow struct {
		Name   string
		Unique int
	}
	var rows []indexRow
	require.NoError(t, db.Raw(fmt.Sprintf("PRAGMA index_list(%s)", table)).Scan(&rows).Error)
	for _, r := range rows {
		if r.Name == name {
			return r.Unique == 1
		}
	}
	return false
}

// indexColumns returns the ordered column list of a SQLite index.
func indexColumns(t *testing.T, db *gorm.DB, name string) []string {
	t.Helper()
	type colRow struct {
		Seqno int
		Name  string
	}
	var rows []colRow
	require.NoError(t, db.Raw(fmt.Sprintf("PRAGMA index_info(%s)", name)).Scan(&rows).Error)
	cols := make([]string, 0, len(rows))
	for _, r := range rows {
		cols = append(cols, r.Name)
	}
	return cols
}

// sqliteIndexSQL returns the CREATE INDEX statement SQLite stored for an index.
func sqliteIndexSQL(t *testing.T, db *gorm.DB, name string) string {
	t.Helper()
	var sql string
	require.NoError(t, db.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?", name).Scan(&sql).Error)
	return sql
}

// freshSQLiteDB opens an empty file-backed SQLite database for tag tests.
func freshSQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "fresh.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	return db
}

// createHashTables creates minimal pre-tag versions of governance_virtual_keys and
// sessions, the shape an upgraded database has after migrationAddEncryptionColumns
// added the hash columns without their indexes.
func createHashTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE TABLE governance_virtual_keys (id VARCHAR(255) PRIMARY KEY, value_hash VARCHAR(64))`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE sessions (id INTEGER PRIMARY KEY, token_hash VARCHAR(64))`).Error)
}

// runHashIndexMigration runs migrationAddValueHashAndTokenHashIndexes once.
func runHashIndexMigration(ctx context.Context, db *gorm.DB) error {
	return migrationAddValueHashAndTokenHashIndexes(ctx, db, testMigrationLogger)
}

// TestHighFixHashIndexesBuildUnique pins H1: an upgraded database without the tag
// indexes gets UNIQUE idx_virtual_key_value_hash and idx_session_token_hash. Many
// NULL and empty-string hashes must not block the unique build, and the migration re-runs cleanly.
func TestHighFixHashIndexesBuildUnique(t *testing.T) {
	for _, ndb := range indexMigrationDBs(t) {
		t.Run(ndb.name, func(t *testing.T) {
			db := ndb.db
			createHashTables(t, db)
			require.NoError(t, db.Exec(`INSERT INTO governance_virtual_keys (id, value_hash) VALUES
				('vk-1', 'h1'), ('vk-2', 'h2'), ('vk-3', NULL), ('vk-4', NULL), ('vk-5', ''), ('vk-6', '')`).Error)
			require.NoError(t, db.Exec(`INSERT INTO sessions (id, token_hash) VALUES
				(1, 't1'), (2, 't2'), (3, NULL), (4, ''), (5, '')`).Error)

			runIndexMigrationTwice(t, db, "add_value_hash_and_token_hash_indexes", runHashIndexMigration,
				"governance_virtual_keys", "idx_virtual_key_value_hash")
			require.True(t, indexIsUnique(t, db, "governance_virtual_keys", "idx_virtual_key_value_hash"))
			require.True(t, indexExists(t, db, "sessions", "idx_session_token_hash"))
			require.True(t, indexIsUnique(t, db, "sessions", "idx_session_token_hash"))
			require.False(t, indexExists(t, db, "governance_virtual_keys", "idx_virtual_key_value_hash_nonunique"))

			// '' hashes are normalized to NULL, the value the encryption migration uses.
			var empties int64
			require.NoError(t, db.Raw("SELECT COUNT(*) FROM governance_virtual_keys WHERE value_hash = ''").Scan(&empties).Error)
			require.Zero(t, empties)
			require.NoError(t, db.Raw("SELECT COUNT(*) FROM sessions WHERE token_hash = ''").Scan(&empties).Error)
			require.Zero(t, empties)
		})
	}
}

// TestHighFixHashIndexesFallBackOnDuplicates pins H1's safety path: duplicate
// hashes make a unique build impossible, so the migration builds a non-unique
// index under a separate name instead of failing startup.
func TestHighFixHashIndexesFallBackOnDuplicates(t *testing.T) {
	for _, ndb := range indexMigrationDBs(t) {
		t.Run(ndb.name, func(t *testing.T) {
			db := ndb.db
			createHashTables(t, db)
			require.NoError(t, db.Exec(`INSERT INTO governance_virtual_keys (id, value_hash) VALUES
				('vk-1', 'dup'), ('vk-2', 'dup'), ('vk-3', 'h3')`).Error)
			require.NoError(t, db.Exec(`INSERT INTO sessions (id, token_hash) VALUES (1, 'td'), (2, 'td'), (3, 'td'), (4, 't4')`).Error)

			require.NoError(t, runHashIndexMigration(context.Background(), db))
			require.False(t, indexExists(t, db, "governance_virtual_keys", "idx_virtual_key_value_hash"))
			require.True(t, indexExists(t, db, "governance_virtual_keys", "idx_virtual_key_value_hash_nonunique"))
			require.False(t, indexIsUnique(t, db, "governance_virtual_keys", "idx_virtual_key_value_hash_nonunique"))
			require.False(t, indexExists(t, db, "sessions", "idx_session_token_hash"))
			require.True(t, indexExists(t, db, "sessions", "idx_session_token_hash_nonunique"))

			// Re-running is safe.
			require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", "add_value_hash_and_token_hash_indexes").Error)
			require.NoError(t, runHashIndexMigration(context.Background(), db))
			require.True(t, indexExists(t, db, "governance_virtual_keys", "idx_virtual_key_value_hash_nonunique"))
		})
	}
}

// TestHighFixSetBasedIndexMigrations pins H13, H14 and H27: upgraded databases get
// the lookup indexes that the new struct tags build on fresh installs.
func TestHighFixSetBasedIndexMigrations(t *testing.T) {
	for _, ndb := range indexMigrationDBs(t) {
		t.Run(ndb.name, func(t *testing.T) {
			db := ndb.db
			require.NoError(t, db.Exec(`CREATE TABLE governance_virtual_key_mcp_configs (
				id INTEGER PRIMARY KEY, virtual_key_id VARCHAR(255) NOT NULL, mcp_client_id INTEGER NOT NULL)`).Error)
			require.NoError(t, db.Exec(`CREATE TABLE governance_virtual_key_provider_configs (
				id INTEGER PRIMARY KEY, virtual_key_id VARCHAR(255) NOT NULL, provider VARCHAR(50) NOT NULL)`).Error)
			require.NoError(t, db.Exec(`CREATE TABLE governance_virtual_key_provider_config_keys (
				table_virtual_key_provider_config_id INTEGER NOT NULL, table_key_id INTEGER NOT NULL,
				PRIMARY KEY (table_virtual_key_provider_config_id, table_key_id))`).Error)
			require.NoError(t, db.Exec(`CREATE TABLE governance_virtual_keys (
				id VARCHAR(255) PRIMARY KEY, created_at TIMESTAMP NOT NULL)`).Error)

			runIndexMigrationTwice(t, db, "add_vk_mcp_configs_mcp_client_id_index",
				func(ctx context.Context, db *gorm.DB) error {
					return migrationAddVKMCPConfigsMCPClientIDIndex(ctx, db, testMigrationLogger)
				},
				"governance_virtual_key_mcp_configs", "idx_vk_mcp_configs_mcp_client_id")
			runIndexMigrationTwice(t, db, "add_vk_provider_config_provider_and_key_indexes",
				func(ctx context.Context, db *gorm.DB) error {
					return migrationAddVKProviderConfigProviderAndKeyIndexes(ctx, db, testMigrationLogger)
				},
				"governance_virtual_key_provider_configs", "idx_vk_provider_configs_provider")
			require.True(t, indexExists(t, db, "governance_virtual_key_provider_config_keys", "idx_vkpc_keys_table_key_id"))
			runIndexMigrationTwice(t, db, "add_virtual_keys_created_at_id_index",
				func(ctx context.Context, db *gorm.DB) error {
					return migrationAddVirtualKeysCreatedAtIDIndex(ctx, db, testMigrationLogger)
				},
				"governance_virtual_keys", "idx_virtual_keys_created_at_id")
		})
	}
}

// TestHighFixBatchJobsDueMigration pins H28's upgrade path: terminal rows keep no
// next_check_at (backfilled in primary-key batches), non-terminal rows are
// untouched, and the partial due index exists.
func TestHighFixBatchJobsDueMigration(t *testing.T) {
	prev := batchJobsDueBackfillBatchSize
	batchJobsDueBackfillBatchSize = 3
	t.Cleanup(func() { batchJobsDueBackfillBatchSize = prev })

	for _, ndb := range indexMigrationDBs(t) {
		t.Run(ndb.name, func(t *testing.T) {
			db := ndb.db
			require.NoError(t, db.Exec(`CREATE TABLE batch_jobs (
				id VARCHAR(512) PRIMARY KEY, kind VARCHAR(50) NOT NULL DEFAULT 'batch', provider VARCHAR(255) NOT NULL,
				accounting_status VARCHAR(50) NOT NULL, next_check_at TIMESTAMP)`).Error)
			due := time.Now().UTC().Add(-time.Hour)
			statuses := []string{
				tables.ProviderJobAccountingStatusAccounted, tables.ProviderJobAccountingStatusUnpriceable,
				tables.ProviderJobAccountingStatusPending, tables.ProviderJobAccountingStatusError,
				tables.ProviderJobAccountingStatusProcessing,
			}
			for i := 0; i < 20; i++ {
				status := statuses[i%len(statuses)]
				require.NoError(t, db.Exec("INSERT INTO batch_jobs (id, kind, provider, accounting_status, next_check_at) VALUES (?, 'batch', 'openai', ?, ?)",
					fmt.Sprintf("job-%02d", i), status, due).Error)
			}

			runIndexMigrationTwice(t, db, "add_batch_jobs_due_index",
				func(ctx context.Context, db *gorm.DB) error {
					return migrationAddBatchJobsDueIndex(ctx, db, testMigrationLogger)
				},
				"batch_jobs", "idx_batch_jobs_due")

			var terminalWithNext, nonTerminalWithNext int64
			require.NoError(t, db.Raw("SELECT COUNT(*) FROM batch_jobs WHERE accounting_status IN ('accounted','unpriceable') AND next_check_at IS NOT NULL").Scan(&terminalWithNext).Error)
			require.NoError(t, db.Raw("SELECT COUNT(*) FROM batch_jobs WHERE accounting_status NOT IN ('accounted','unpriceable') AND next_check_at IS NOT NULL").Scan(&nonTerminalWithNext).Error)
			require.Zero(t, terminalWithNext)
			require.EqualValues(t, 12, nonTerminalWithNext)

			if db.Dialector.Name() == "postgres" {
				var def string
				require.NoError(t, db.Raw("SELECT indexdef FROM pg_indexes WHERE indexname = 'idx_batch_jobs_due' AND schemaname = current_schema()").Scan(&def).Error)
				require.Contains(t, def, "WHERE (next_check_at IS NOT NULL)")
			} else {
				require.Contains(t, sqliteIndexSQL(t, db, "idx_batch_jobs_due"), "WHERE next_check_at IS NOT NULL")
			}
		})
	}
}

// TestHighFixTagsBuildIndexes pins that fresh installs, which build tables from
// struct tags through CreateTable, get the same indexes the upgrade migrations add.
func TestHighFixTagsBuildIndexes(t *testing.T) {
	db := freshSQLiteDB(t)
	mg := db.Migrator()
	require.NoError(t, mg.CreateTable(&tables.TableVirtualKey{}))
	require.NoError(t, mg.CreateTable(&tables.SessionsTable{}))
	require.NoError(t, mg.CreateTable(&tables.TableVirtualKeyMCPConfig{}))
	require.NoError(t, mg.CreateTable(&tables.TableVirtualKeyProviderConfig{}))
	require.NoError(t, mg.CreateTable(&tables.TableVirtualKeyProviderConfigKey{}))
	require.NoError(t, mg.CreateTable(&tables.TableProviderJob{}))

	require.True(t, indexIsUnique(t, db, "governance_virtual_keys", "idx_virtual_key_value_hash"))
	require.True(t, indexIsUnique(t, db, "sessions", "idx_session_token_hash"))

	require.Equal(t, []string{"mcp_client_id"}, indexColumns(t, db, "idx_vk_mcp_configs_mcp_client_id"))
	require.Equal(t, []string{"virtual_key_id", "mcp_client_id"}, indexColumns(t, db, "idx_vk_mcpclient"))
	require.True(t, indexIsUnique(t, db, "governance_virtual_key_mcp_configs", "idx_vk_mcpclient"))

	require.Equal(t, []string{"provider"}, indexColumns(t, db, "idx_vk_provider_configs_provider"))
	require.Equal(t, []string{"table_key_id"}, indexColumns(t, db, "idx_vkpc_keys_table_key_id"))

	require.Equal(t, []string{"created_at", "id"}, indexColumns(t, db, "idx_virtual_keys_created_at_id"))
	require.True(t, mg.HasIndex(&tables.TableVirtualKey{}, "idx_governance_virtual_keys_created_at"), "the single-column created_at index must stay")

	require.Equal(t, []string{"kind", "next_check_at"}, indexColumns(t, db, "idx_batch_jobs_due"))
	require.True(t, strings.Contains(sqliteIndexSQL(t, db, "idx_batch_jobs_due"), "WHERE next_check_at IS NOT NULL"))
}

// TestHighFixHashNormalizationIsBatched pins that normalizing empty hashes to
// NULL runs in bounded, separately committed batches rather than one UPDATE over
// the whole table, which would hold row locks on every matching row at once on
// a large sessions table. Five empty hashes with a batch size of two need at
// least three statements per table, and none may be left afterwards.
func TestHighFixHashNormalizationIsBatched(t *testing.T) {
	restore := hashNormalizeBatchSize
	hashNormalizeBatchSize = 2
	t.Cleanup(func() { hashNormalizeBatchSize = restore })
	for _, ndb := range indexMigrationDBs(t) {
		t.Run(ndb.name, func(t *testing.T) {
			db := ndb.db
			createHashTables(t, db)
			require.NoError(t, db.Exec(`INSERT INTO governance_virtual_keys (id, value_hash) VALUES
				('vk-1', ''), ('vk-2', ''), ('vk-3', 'h3'), ('vk-4', ''), ('vk-5', ''), ('vk-6', '')`).Error)
			require.NoError(t, db.Exec(`INSERT INTO sessions (id, token_hash) VALUES
				(1, ''), (2, ''), (3, 't3'), (4, ''), (5, ''), (6, '')`).Error)

			updates := map[string]int{}
			name := "test:count-hash-normalize:" + t.Name()
			require.NoError(t, db.Callback().Raw().After("gorm:raw").Register(name, func(tx *gorm.DB) {
				sql := tx.Statement.SQL.String()
				for _, col := range []string{"value_hash", "token_hash"} {
					if strings.HasPrefix(sql, "UPDATE") && strings.Contains(sql, "SET "+col+" = NULL") {
						updates[col]++
					}
				}
			}))
			t.Cleanup(func() { _ = db.Callback().Raw().Remove(name) })

			require.NoError(t, runHashIndexMigration(context.Background(), db))
			require.GreaterOrEqual(t, updates["value_hash"], 3, "empty value hashes must be normalized in bounded batches")
			require.GreaterOrEqual(t, updates["token_hash"], 3, "empty token hashes must be normalized in bounded batches")
			var empties int64
			require.NoError(t, db.Raw("SELECT COUNT(*) FROM governance_virtual_keys WHERE value_hash = ''").Scan(&empties).Error)
			require.Zero(t, empties)
			require.NoError(t, db.Raw("SELECT COUNT(*) FROM sessions WHERE token_hash = ''").Scan(&empties).Error)
			require.Zero(t, empties)
			require.True(t, indexIsUnique(t, db, "governance_virtual_keys", "idx_virtual_key_value_hash"))
		})
	}
}

// TestHighFixHashIndexRollbackRefuses pins that rolling back the hash-index
// migration fails instead of dropping the unique lookup indexes: on a fresh
// install the struct tags built them before this migration ran, and the ledger
// cannot tell whether the migration or the tags created them.
func TestHighFixHashIndexRollbackRefuses(t *testing.T) {
	db := freshSQLiteDB(t)
	require.NoError(t, db.Migrator().CreateTable(&tables.TableVirtualKey{}, &tables.SessionsTable{}))
	require.True(t, indexExists(t, db, "governance_virtual_keys", "idx_virtual_key_value_hash"))

	err := valueHashAndTokenHashIndexesMigration(context.Background(), "add_value_hash_and_token_hash_indexes", testMigrationLogger).Rollback(db)
	require.Error(t, err, "the hash-index migration must refuse to roll back")
	require.Contains(t, err.Error(), "non-rollbackable")
	require.True(t, indexExists(t, db, "governance_virtual_keys", "idx_virtual_key_value_hash"), "a refused rollback must keep the index")
}

// TestHighFixBatchJobsDueRollbackRefuses pins that rolling back the batch-jobs
// migration fails: it cleared next_check_at on finished jobs, which dropping the
// index cannot restore.
func TestHighFixBatchJobsDueRollbackRefuses(t *testing.T) {
	err := batchJobsDueIndexMigration(context.Background(), "add_batch_jobs_due_index").Rollback(freshSQLiteDB(t))
	require.Error(t, err, "the batch-jobs migration must refuse to roll back")
	require.Contains(t, err.Error(), "non-rollbackable")
}
