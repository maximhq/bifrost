package configstore

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// forceConcurrentIndexBuilds makes every table read as large for the rest of the
// test, so ensureIndexConcurrently takes the CREATE INDEX CONCURRENTLY path that
// the race tests below exercise rather than the small-table plain build.
func forceConcurrentIndexBuilds(t *testing.T) {
	t.Helper()
	prevBytes, prevRows := smallTableIndexMaxBytes, smallTableIndexMaxRows
	smallTableIndexMaxBytes, smallTableIndexMaxRows = 0, 0
	t.Cleanup(func() { smallTableIndexMaxBytes, smallTableIndexMaxRows = prevBytes, prevRows })
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

// TestMigrationAddBatchJobsFileIDIndexes pins that upgraded databases get the indexes the
// file-ownership fallback (GetProviderJobsByFileIDs) filters on, that the migration is safe to
// re-run, and that its rollback drops them.
func TestMigrationAddBatchJobsFileIDIndexes(t *testing.T) {
	for _, ndb := range indexMigrationDBs(t) {
		t.Run(ndb.name, func(t *testing.T) {
			require.NoError(t, ndb.db.Exec(`CREATE TABLE batch_jobs (
				id VARCHAR(512) PRIMARY KEY, kind VARCHAR(50) NOT NULL DEFAULT 'batch', provider VARCHAR(255) NOT NULL,
				input_file_id VARCHAR(255), output_file_id VARCHAR(255), error_file_id VARCHAR(255))`).Error)
			runIndexMigrationTwice(t, ndb.db, "add_batch_jobs_file_id_indexes",
				func(ctx context.Context, db *gorm.DB) error {
					return migrationAddBatchJobsFileIDIndexes(ctx, db, testMigrationLogger)
				},
				"batch_jobs", "idx_batch_jobs_input_file_id")
			require.True(t, indexExists(t, ndb.db, "batch_jobs", "idx_batch_jobs_output_file_id"))
			require.True(t, indexExists(t, ndb.db, "batch_jobs", "idx_batch_jobs_error_file_id"))

			rollback := batchJobsFileIDIndexesMigration(context.Background(), "add_batch_jobs_file_id_indexes").Rollback
			require.NoError(t, rollback(ndb.db))
			for _, index := range []string{"idx_batch_jobs_input_file_id", "idx_batch_jobs_output_file_id", "idx_batch_jobs_error_file_id"} {
				require.False(t, indexExists(t, ndb.db, "batch_jobs", index), "rollback drops %s", index)
			}
			require.NoError(t, rollback(ndb.db), "rollback is safe to re-run")
		})
	}
}

// TestProviderObjectsTableRollbackRefuses pins that rolling back the provider_objects
// migration fails instead of dropping the table: its rows are the only record of which
// virtual key created each file, container and cached content, and without them every
// such object becomes reachable to every key allowed on the provider. The table is
// additive, so an older binary runs fine with it in place.
func TestProviderObjectsTableRollbackRefuses(t *testing.T) {
	db := freshSQLiteDB(t)
	require.NoError(t, db.Migrator().CreateTable(&tables.TableProviderObject{}))

	err := providerObjectsTableMigration(context.Background(), "add_provider_objects_table").Rollback(db)
	require.Error(t, err, "the provider_objects migration must refuse to roll back")
	require.Contains(t, err.Error(), "non-rollbackable")
	require.True(t, db.Migrator().HasTable(&tables.TableProviderObject{}), "a refused rollback must keep the table")
}

// parkedIndexBuild is an ensureIndexConcurrently that pod 1 started and that is
// still running, as the build of a killed pod keeps running in Postgres.
type parkedIndexBuild struct {
	pod2    *gorm.DB   // a second connection pool, standing in for the next pod
	done    chan error // pod 1's ensureIndexConcurrently result
	pid     int64      // backend running pod 1's build
	oid     uint32     // pg_class oid of the index pod 1 is building
	release func()     // commits the transaction the build is waiting on
}

// parkIndexBuild starts pod 1's build of idx_race_rows_v behind an open
// REPEATABLE READ transaction (an old pod serving traffic), so the build parks in
// "waiting for old snapshots" with its index already in pg_index as INVALID: the
// state the next pod finds after a pod is killed mid-migration.
func parkIndexBuild(t *testing.T) parkedIndexBuild {
	t.Helper()
	forceConcurrentIndexBuilds(t)
	var pod1 *gorm.DB
	for _, ndb := range indexMigrationDBs(t) {
		if ndb.name == "postgres" {
			pod1 = ndb.db
		}
	}
	if pod1 == nil {
		t.Skip("local test Postgres is not reachable")
	}
	dsn := strings.Replace(postgresDSN, "search_path="+pgTestSchema, "search_path="+indexMigrationPGSchema, 1)
	pod2, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pod2SQL, err := pod2.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pod2SQL.Close() })
	require.NoError(t, pod1.Exec(`CREATE TABLE race_rows (id INTEGER PRIMARY KEY, v VARCHAR(255) NOT NULL)`).Error)
	require.NoError(t, pod1.Exec(`INSERT INTO race_rows SELECT g, 'v' || g FROM generate_series(1, 5000) g`).Error)

	ctx := context.Background()
	pod1SQL, err := pod1.DB()
	require.NoError(t, err)
	blocker, err := pod1SQL.Conn(ctx)
	require.NoError(t, err)
	_, err = blocker.ExecContext(ctx, "BEGIN ISOLATION LEVEL REPEATABLE READ")
	require.NoError(t, err)
	_, err = blocker.ExecContext(ctx, "SELECT 1")
	require.NoError(t, err)
	var once sync.Once
	release := func() {
		once.Do(func() {
			_, _ = blocker.ExecContext(ctx, "COMMIT")
			_ = blocker.Close()
		})
	}
	t.Cleanup(release)

	build := parkedIndexBuild{pod2: pod2, done: make(chan error, 1), release: release}
	go func() { build.done <- ensureIndexConcurrently(pod1, "race_rows", "idx_race_rows_v", "v", false) }()
	require.Eventually(t, func() bool {
		return pod1.Raw(`SELECT COALESCE((SELECT pid FROM pg_stat_progress_create_index
			WHERE relid = to_regclass('race_rows') AND phase = 'waiting for old snapshots'), 0)`).Scan(&build.pid).Error == nil && build.pid != 0
	}, 20*time.Second, 50*time.Millisecond, "pod 1's build must park in 'waiting for old snapshots'")
	require.NoError(t, pod1.Raw(`SELECT to_regclass('idx_race_rows_v')::oid`).Scan(&build.oid).Error)
	valid, err := postgresIndexIsValid(pod1, "race_rows", "idx_race_rows_v")
	require.NoError(t, err)
	require.False(t, valid, "a build still in progress must read as INVALID in pg_index")
	return build
}

// TestEnsureIndexConcurrentlyWaitsForLiveBuild pins that the next pod waits for a
// concurrent build another backend is still running instead of dropping it. A
// killed pod's idle advisory-lock session ends at once but its busy build keeps
// running, so the next pod holds the migration lock while that build is alive.
// Dropping it blocked behind the build and then failed one side with
// "tuple concurrently updated".
func TestEnsureIndexConcurrentlyWaitsForLiveBuild(t *testing.T) {
	build := parkIndexBuild(t)
	pod2Done := make(chan error, 1)
	go func() { pod2Done <- ensureIndexConcurrently(build.pod2, "race_rows", "idx_race_rows_v", "v", false) }()

	var drops int64
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && drops == 0; time.Sleep(20 * time.Millisecond) {
		require.NoError(t, build.pod2.Raw(`SELECT COUNT(*) FROM pg_stat_activity
			WHERE query ILIKE 'DROP INDEX CONCURRENTLY%idx_race_rows_v%'`).Scan(&drops).Error)
	}
	build.release()
	pod1Err, pod2Err := <-build.done, <-pod2Done

	require.Zero(t, drops, "pod 2 issued DROP INDEX CONCURRENTLY against a build another backend was still running")
	require.NoError(t, pod1Err)
	require.NoError(t, pod2Err)
	require.True(t, indexExists(t, build.pod2, "race_rows", "idx_race_rows_v"))
	var finalOID uint32
	require.NoError(t, build.pod2.Raw(`SELECT to_regclass('idx_race_rows_v')::oid`).Scan(&finalOID).Error)
	require.Equal(t, build.oid, finalOID, "pod 1's finished build must be the index that remains, not a second rebuild")
}

// TestEnsureIndexConcurrentlyRebuildsAfterPeerBuildFails pins that waiting never
// keeps a broken index: when the build pod 2 waits on dies (cancelled here, as a
// terminated orphan would be), pod 2 drops the INVALID leftover and rebuilds.
func TestEnsureIndexConcurrentlyRebuildsAfterPeerBuildFails(t *testing.T) {
	build := parkIndexBuild(t)
	pod2Done := make(chan error, 1)
	go func() { pod2Done <- ensureIndexConcurrently(build.pod2, "race_rows", "idx_race_rows_v", "v", false) }()
	time.Sleep(500 * time.Millisecond)
	require.NoError(t, build.pod2.Exec("SELECT pg_cancel_backend(?)", build.pid).Error)
	require.Error(t, <-build.done, "the cancelled build must fail and leave an INVALID index")

	build.release()
	require.NoError(t, <-pod2Done)
	require.True(t, indexExists(t, build.pod2, "race_rows", "idx_race_rows_v"))
}

// TestEnsureIndexSmallTableDoesNotWaitOnOldSnapshots reproduces the 2.3.0 upgrade
// stall: an older replica's long transaction (a materialized-view refresh over a
// large logs table in the same database) held a snapshot, and every CREATE INDEX
// CONCURRENTLY in the startup migrations, even on a tiny config table, waited for
// it. A small table must now be indexed with a plain build that only needs its
// own table lock, so it finishes while the unrelated snapshot is still open.
func TestEnsureIndexSmallTableDoesNotWaitOnOldSnapshots(t *testing.T) {
	var db *gorm.DB
	for _, ndb := range indexMigrationDBs(t) {
		if ndb.name == "postgres" {
			db = ndb.db
		}
	}
	if db == nil {
		t.Skip("local test Postgres is not reachable")
	}
	require.NoError(t, db.Exec(`CREATE TABLE small_cfg (id INTEGER PRIMARY KEY, vk VARCHAR(64) NOT NULL)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE big_logs (id INTEGER PRIMARY KEY)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO small_cfg SELECT g, 'vk' || g FROM generate_series(1, 50) g`).Error)

	// The "old replica": a REPEATABLE READ transaction that has taken its snapshot
	// and touched only an unrelated table, standing in for a long REFRESH.
	ctx := context.Background()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	blocker, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	_, err = blocker.ExecContext(ctx, "BEGIN ISOLATION LEVEL REPEATABLE READ")
	require.NoError(t, err)
	_, err = blocker.ExecContext(ctx, "SELECT count(*) FROM big_logs")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = blocker.ExecContext(ctx, "COMMIT")
		_ = blocker.Close()
	})

	done := make(chan error, 1)
	go func() { done <- ensureIndexConcurrently(db, "small_cfg", "idx_small_cfg_vk", "vk", false) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("small-table index build waited on an unrelated old snapshot")
	}
	require.True(t, indexExists(t, db, "small_cfg", "idx_small_cfg_vk"))

	// Idempotent: a second run is a no-op on the now-valid index.
	require.NoError(t, ensureIndexConcurrently(db, "small_cfg", "idx_small_cfg_vk", "vk", false))
}

// TestEnsureIndexSmallTableRetriesLockTimeout pins that the plain small-table
// build waits out a writer holding a conflicting lock in bounded slices instead
// of queueing behind it indefinitely or failing on the first lock timeout.
func TestEnsureIndexSmallTableRetriesLockTimeout(t *testing.T) {
	var db *gorm.DB
	for _, ndb := range indexMigrationDBs(t) {
		if ndb.name == "postgres" {
			db = ndb.db
		}
	}
	if db == nil {
		t.Skip("local test Postgres is not reachable")
	}
	require.NoError(t, db.Exec(`CREATE TABLE small_locked (id INTEGER PRIMARY KEY, v VARCHAR(64))`).Error)

	ctx := context.Background()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	writer, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	_, err = writer.ExecContext(ctx, "BEGIN")
	require.NoError(t, err)
	// ROW EXCLUSIVE conflicts with the SHARE lock CREATE INDEX needs.
	_, err = writer.ExecContext(ctx, "INSERT INTO small_locked VALUES (1, 'a')")
	require.NoError(t, err)
	go func() {
		time.Sleep(7 * time.Second) // longer than one 5s lock_timeout attempt
		_, _ = writer.ExecContext(ctx, "COMMIT")
		_ = writer.Close()
	}()

	start := time.Now()
	require.NoError(t, ensureIndexConcurrently(db, "small_locked", "idx_small_locked_v", "v", false))
	require.GreaterOrEqual(t, time.Since(start), 5*time.Second, "the build should have hit at least one lock timeout")
	require.True(t, indexExists(t, db, "small_locked", "idx_small_locked_v"))
}
