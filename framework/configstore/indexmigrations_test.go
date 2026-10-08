package configstore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// indexMigrationPGSchema isolates the index migration tests from the other
// Postgres-backed tests in this package, which drop and recreate shared tables.
const indexMigrationPGSchema = "configstore_idxmig_test"

// indexMigrationDBs returns a fresh SQLite database and, when the local test
// Postgres is reachable, a Postgres connection in a dedicated schema. Both start
// empty so each test creates exactly the tables it needs.
func indexMigrationDBs(t *testing.T) []namedDB {
	t.Helper()
	sqliteDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "idx.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	dbs := []namedDB{{name: "sqlite", db: sqliteDB}}

	dsn := strings.Replace(postgresDSN, "search_path="+pgTestSchema, "search_path="+indexMigrationPGSchema, 1)
	pgDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return dbs
	}
	sqlDB, err := pgDB.DB()
	if err != nil || sqlDB.Ping() != nil {
		return dbs
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, pgDB.Exec("DROP SCHEMA IF EXISTS "+indexMigrationPGSchema+" CASCADE").Error)
	require.NoError(t, pgDB.Exec("CREATE SCHEMA "+indexMigrationPGSchema).Error)
	return append(dbs, namedDB{name: "postgres", db: pgDB})
}

// indexExists reports whether index name exists on table, on either dialect.
func indexExists(t *testing.T, db *gorm.DB, table, name string) bool {
	t.Helper()
	if db.Dialector.Name() == "postgres" {
		valid, err := postgresIndexIsValid(db, table, name)
		require.NoError(t, err)
		return valid
	}
	return db.Migrator().HasIndex(table, name)
}

// runIndexMigrationTwice runs an index migration, asserts the index exists, then
// clears the migration record and runs it again to prove the migration is idempotent.
func runIndexMigrationTwice(t *testing.T, db *gorm.DB, id string, run func(context.Context, *gorm.DB) error, table, index string) {
	t.Helper()
	ctx := context.Background()
	require.False(t, indexExists(t, db, table, index), "index %s must not exist before the migration", index)
	require.NoError(t, run(ctx, db))
	require.True(t, indexExists(t, db, table, index), "index %s must exist after the migration", index)
	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", id).Error)
	require.NoError(t, run(ctx, db))
	require.True(t, indexExists(t, db, table, index))
}

// TestMigrationAddVKProviderConfigVirtualKeyIDIndex pins that upgraded databases,
// whose provider-config table was created before the struct tag existed, get the
// virtual_key_id index, that the migration is safe to re-run, and that its
// rollback drops the index (concurrently on Postgres) and is safe to re-run.
func TestMigrationAddVKProviderConfigVirtualKeyIDIndex(t *testing.T) {
	for _, ndb := range indexMigrationDBs(t) {
		t.Run(ndb.name, func(t *testing.T) {
			require.NoError(t, ndb.db.Exec(`CREATE TABLE governance_virtual_key_provider_configs (
				id INTEGER PRIMARY KEY, virtual_key_id VARCHAR(255) NOT NULL, provider VARCHAR(50) NOT NULL)`).Error)
			runIndexMigrationTwice(t, ndb.db, "add_vk_provider_config_virtual_key_id_index",
				func(ctx context.Context, db *gorm.DB) error {
					return migrationAddVKProviderConfigVirtualKeyIDIndex(ctx, db, testMigrationLogger)
				},
				"governance_virtual_key_provider_configs", "idx_vk_provider_configs_virtual_key_id")

			rollback := vkProviderConfigVirtualKeyIDIndexMigration(context.Background(), "add_vk_provider_config_virtual_key_id_index").Rollback
			require.NoError(t, rollback(ndb.db))
			require.False(t, indexExists(t, ndb.db, "governance_virtual_key_provider_configs", "idx_vk_provider_configs_virtual_key_id"),
				"index must not exist after the rollback")
			require.NoError(t, rollback(ndb.db))
		})
	}
}

// TestVKProviderConfigTagsBuildVirtualKeyIDIndex pins that a fresh install, which
// creates the table from struct tags, gets the same index the upgrade migration adds.
func TestVKProviderConfigTagsBuildVirtualKeyIDIndex(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "fresh.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Migrator().CreateTable(&tables.TableVirtualKeyProviderConfig{}))
	require.True(t, db.Migrator().HasIndex(&tables.TableVirtualKeyProviderConfig{}, "idx_vk_provider_configs_virtual_key_id"))
}

// TestIndexHelpersNeverDropAnotherTablesIndex pins that the concurrent index
// helpers only ever drop the index on their own table. With the search path set
// to the test schema and then another schema, and only the other schema holding
// an index of the same name, neither building the index on the test schema's
// table nor rolling the migration back may drop the other schema's index: an
// unqualified DROP INDEX resolves through search_path and would find it.
func TestIndexHelpersNeverDropAnotherTablesIndex(t *testing.T) {
	const otherSchema = "configstore_idxmig_other"
	const table = "governance_virtual_key_provider_configs"
	const index = "idx_vk_provider_configs_virtual_key_id"
	for _, ndb := range indexMigrationDBs(t) {
		if ndb.name != "postgres" {
			continue
		}
		t.Run(ndb.name, func(t *testing.T) {
			ctx := context.Background()
			sqlDB, err := ndb.db.DB()
			require.NoError(t, err)
			// One connection, so the search_path set below applies to every statement.
			sqlDB.SetMaxOpenConns(1)
			db := ndb.db
			require.NoError(t, db.Exec("DROP SCHEMA IF EXISTS "+otherSchema+" CASCADE").Error)
			t.Cleanup(func() { _ = db.Exec("DROP SCHEMA IF EXISTS " + otherSchema + " CASCADE").Error })
			require.NoError(t, db.Exec("CREATE SCHEMA "+otherSchema).Error)
			require.NoError(t, db.Exec("CREATE TABLE "+otherSchema+"."+table+" (id INTEGER PRIMARY KEY, virtual_key_id VARCHAR(255))").Error)
			require.NoError(t, db.Exec("CREATE INDEX "+index+" ON "+otherSchema+"."+table+" (virtual_key_id)").Error)
			require.NoError(t, db.Exec(`CREATE TABLE `+table+` (
				id INTEGER PRIMARY KEY, virtual_key_id VARCHAR(255) NOT NULL, provider VARCHAR(50) NOT NULL)`).Error)
			require.NoError(t, db.Exec("SET search_path TO "+indexMigrationPGSchema+", "+otherSchema).Error)

			otherIndexExists := func() bool {
				var n int64
				require.NoError(t, db.Raw(`SELECT COUNT(*) FROM pg_indexes WHERE schemaname = ? AND indexname = ?`, otherSchema, index).Scan(&n).Error)
				return n == 1
			}

			require.NoError(t, ensureIndexConcurrently(db, table, index, "virtual_key_id", false))
			require.True(t, otherIndexExists(), "building the index on this schema's table dropped another schema's index")
			require.True(t, indexExists(t, db, table, index), "the index must be built on this schema's table")

			require.NoError(t, db.Exec("DROP INDEX "+indexMigrationPGSchema+"."+index).Error)
			require.NoError(t, vkProviderConfigVirtualKeyIDIndexMigration(ctx, "add_vk_provider_config_virtual_key_id_index").Rollback(db))
			require.True(t, otherIndexExists(), "rolling back with no index on this table dropped another schema's index")
		})
	}
}
