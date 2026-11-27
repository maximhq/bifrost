package configstore

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Regression tests for the 2.2.5 -> 2.3.0 rolling upgrade that could not converge
// on a large log store (customer report, 2026-10-14). Each test rolls a fully
// migrated configstore back to the 2.2.5 schema state, reproduces what the old
// replicas were doing, and runs the real triggerMigrations as the new pod. The
// control cases switch the relevant fix off and assert the original failure, so a
// regression in any fix fails here rather than in a customer's rollout.

// upgradeTestSchema isolates these tests from the package's other Postgres tests.
const upgradeTestSchema = "configstore_upgrade_test"

// upgradeTestDB is a database of its own for these tests. The matview refresh
// lock is a database-scoped advisory lock, and go test runs the logstore package
// against the same server at the same time: its pause tests take that lock and
// cancel whichever REFRESH holds it, which in the shared database is this file's
// old replica. A cancelled old replica unblocks the index builds, so the control
// cases stop reproducing the stall. A separate database gives these tests their
// own lock and their own old replica.
const upgradeTestDB = "configstore_upgrade_test"

// ensureUpgradeTestDB creates upgradeTestDB if it does not exist, or skips the
// test when the local test Postgres is unreachable.
func ensureUpgradeTestDB(t *testing.T) {
	t.Helper()
	admin, err := gorm.Open(postgres.Open(postgresDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Skip("local test Postgres is not reachable")
	}
	sqlDB, err := admin.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	if sqlDB.Ping() != nil {
		t.Skip("local test Postgres is not reachable")
	}
	var exists bool
	require.NoError(t, admin.Raw("SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = ?)", upgradeTestDB).Scan(&exists).Error)
	if exists {
		return
	}
	// A concurrent creator may win the race; only a real failure matters.
	if err := admin.Exec("CREATE DATABASE " + upgradeTestDB).Error; err != nil {
		require.NoError(t, admin.Raw("SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = ?)", upgradeTestDB).Scan(&exists).Error)
		require.True(t, exists, "create %s: %v", upgradeTestDB, err)
	}
}

// upgrade230IndexMigrations are configstore migrations new in 2.3.0 that build
// indexes, with the indexes each one creates. The incident timeline stalled on
// exactly these two.
var upgrade230IndexMigrations = map[string][]string{
	"add_vk_provider_config_virtual_key_id_index": {"idx_vk_provider_configs_virtual_key_id"},
	"add_batch_jobs_file_id_indexes":              {"idx_batch_jobs_input_file_id", "idx_batch_jobs_output_file_id", "idx_batch_jobs_error_file_id"},
}

// openUpgradePod opens a pool on the upgrade database and schema tagged with app
// as its application_name, the way a pod's migration pool is
// (postgresconn.MigrationDSN), or skips the test when the local test Postgres is
// unreachable.
func openUpgradePod(t *testing.T, app string) *gorm.DB {
	t.Helper()
	ensureUpgradeTestDB(t)
	dsn := strings.Replace(postgresDSN, "search_path="+pgTestSchema, "search_path="+upgradeTestSchema, 1)
	dsn = strings.Replace(dsn, "dbname=bifrost ", "dbname="+upgradeTestDB+" ", 1) +
		" application_name='" + app + "'"
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Skip("local test Postgres is not reachable")
	}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	if sqlDB.Ping() != nil {
		t.Skip("local test Postgres is not reachable")
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// setupConfigstoreAt225 migrates a fresh schema to the current version, then
// rolls the 2.3.0 index migrations back (drops their indexes and their migration
// rows), leaving the schema a 2.3.0 pod finds when it replaces 2.2.5. It also
// creates a logs table in the same database, standing in for the log store the
// customer shared the database with.
func setupConfigstoreAt225(t *testing.T) *gorm.DB {
	t.Helper()
	db := openUpgradePod(t, "bifrost-migrate:setup")
	require.NoError(t, db.Exec("DROP SCHEMA IF EXISTS "+upgradeTestSchema+" CASCADE").Error)
	require.NoError(t, db.Exec("CREATE SCHEMA "+upgradeTestSchema).Error)
	t.Cleanup(func() { _ = db.Exec("DROP SCHEMA IF EXISTS " + upgradeTestSchema + " CASCADE").Error })
	require.NoError(t, triggerMigrations(context.Background(), db, testMigrationLogger))

	for id, indexes := range upgrade230IndexMigrations {
		for _, idx := range indexes {
			require.NoError(t, db.Exec("DROP INDEX IF EXISTS "+idx).Error)
		}
		require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", id).Error)
	}
	require.NoError(t, db.Exec(`CREATE TABLE logs (id BIGSERIAL PRIMARY KEY, provider TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO logs (provider) SELECT 'p' || (g % 7) FROM generate_series(1, 2000) g`).Error)
	return db
}

// requireUpgradeIndexesApplied asserts every 2.3.0 index migration ran: its row
// is recorded and its indexes exist and are valid.
func requireUpgradeIndexesApplied(t *testing.T, db *gorm.DB) {
	t.Helper()
	for id, indexes := range upgrade230IndexMigrations {
		var n int64
		require.NoError(t, db.Raw("SELECT count(*) FROM migrations WHERE id = ?", id).Scan(&n).Error)
		require.EqualValues(t, 1, n, "migration %s must be recorded", id)
		for _, idx := range indexes {
			var valid bool
			require.NoError(t, db.Raw(`SELECT COALESCE((SELECT i.indisvalid FROM pg_index i WHERE i.indexrelid = to_regclass(?)), false)`, idx).Scan(&valid).Error)
			require.True(t, valid, "index %s must exist and be valid", idx)
		}
	}
}

// startOldReplicaRefresh reproduces a 2.2.5 replica's refresh pass on a large
// logs table: it takes the matview refresh lock with pg_try_advisory_lock (as
// every release does) and runs REFRESH MATERIALIZED VIEW CONCURRENTLY over a
// view that reads logs and takes sleepSecs to compute. That statement holds a
// snapshot, which every CREATE INDEX CONCURRENTLY in the database waits on, and
// an ACCESS SHARE lock on logs. The returned channel yields the REFRESH result.
func startOldReplicaRefresh(t *testing.T, db *gorm.DB, sleepSecs int) <-chan error {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE TABLE old_replica_knob (secs float8 NOT NULL)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO old_replica_knob VALUES (0)`).Error)
	require.NoError(t, db.Exec(`CREATE MATERIALIZED VIEW mv_old_replica_hourly AS
		SELECT 1 AS k, (SELECT count(*) FROM logs) AS n,
		       length((SELECT pg_sleep(secs)::text FROM old_replica_knob)) AS slept`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX mv_old_replica_hourly_uniq ON mv_old_replica_hourly (k)`).Error)
	require.NoError(t, db.Exec(`UPDATE old_replica_knob SET secs = ?`, sleepSecs).Error)

	oldPod := openUpgradePod(t, "bifrost-2.2.5-replica")
	sqlDB, err := oldPod.DB()
	require.NoError(t, err)
	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var got bool
		return conn.QueryRowContext(context.Background(), "SELECT pg_try_advisory_lock($1)", int64(1000015)).Scan(&got) == nil && got
	}, time.Minute, 50*time.Millisecond)

	done := make(chan error, 1)
	go func() {
		_, err := conn.ExecContext(context.Background(), "REFRESH MATERIALIZED VIEW CONCURRENTLY mv_old_replica_hourly")
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", int64(1000015))
		_ = conn.Close()
		done <- err
	}()
	t.Cleanup(func() {
		// Never leave a long refresh running into the next test.
		_ = db.Exec(`SELECT pg_cancel_backend(pid) FROM pg_stat_activity WHERE datname = current_database() AND query LIKE 'REFRESH MATERIALIZED VIEW CONCURRENTLY mv_old_replica_hourly%' AND pid <> pg_backend_pid()`).Error
	})
	require.Eventually(t, func() bool {
		var n int64
		return db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state = 'active' AND query LIKE 'REFRESH MATERIALIZED VIEW CONCURRENTLY mv_old_replica_hourly%'`).Scan(&n).Error == nil && n == 1
	}, 10*time.Second, 50*time.Millisecond, "old replica refresh must be running")
	return done
}

// startLongReader opens a REPEATABLE READ transaction that reads logs and stays
// open: a long-running reader that is not a matview refresh, so the pause can
// neither wait it out quickly nor cancel it. It returns a func that ends it.
func startLongReader(t *testing.T, db *gorm.DB) func() {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), "BEGIN ISOLATION LEVEL REPEATABLE READ")
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), "SELECT count(*) FROM logs")
	require.NoError(t, err)
	var once sync.Once
	end := func() {
		once.Do(func() {
			_, _ = conn.ExecContext(context.Background(), "COMMIT")
			_ = conn.Close()
		})
	}
	t.Cleanup(end)
	return end
}

// forceConcurrentIndexBuildsForUpgrade makes every table read as large, so index
// migrations take the CREATE INDEX CONCURRENTLY path: the path large config
// tables still take, and the only path that existed in 2.3.0.
func forceConcurrentIndexBuildsForUpgrade(t *testing.T) {
	t.Helper()
	prevBytes, prevRows := smallTableIndexMaxBytes, smallTableIndexMaxRows
	smallTableIndexMaxBytes, smallTableIndexMaxRows = 0, 0
	t.Cleanup(func() { smallTableIndexMaxBytes, smallTableIndexMaxRows = prevBytes, prevRows })
}

// setMigrationPause overrides the matview pause for the rest of the test; a nil
// opts switches the pause off entirely, which is how 2.3.0 behaved.
func setMigrationPause(t *testing.T, opts *logstore.MatViewPauseOptions) {
	t.Helper()
	prevFn, prevOpts := pauseMatViewsForMigrations, migrationMatViewPause
	if opts == nil {
		pauseMatViewsForMigrations = func(context.Context, *gorm.DB, schemas.Logger, logstore.MatViewPauseOptions) func() { return func() {} }
	} else {
		migrationMatViewPause = *opts
	}
	t.Cleanup(func() { pauseMatViewsForMigrations, migrationMatViewPause = prevFn, prevOpts })
}

// runNewPodMigrations runs the real configstore migrations as a 2.3.x pod would,
// on its own tagged pool, bounded by budget.
func runNewPodMigrations(t *testing.T, app string, budget time.Duration) error {
	t.Helper()
	return migratePod(openUpgradePod(t, app), budget)
}

// migratePod runs the real configstore migrations on pod, bounded by budget. It
// makes no test assertions, so it is safe to call from a spawned goroutine.
func migratePod(pod *gorm.DB, budget time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	return triggerMigrations(ctx, pod, testMigrationLogger)
}

// TestUpgradeConfigstoreMigrationsCompleteBehindOldReplicaRefresh reproduces the
// first stall in the incident: the new pod's CREATE INDEX CONCURRENTLY waited on
// an old replica's 10+ minute REFRESH of mv_logs_hourly. With the fix the new pod
// pauses refreshes, cancels the in-flight pass, and its migrations finish.
func TestUpgradeConfigstoreMigrationsCompleteBehindOldReplicaRefresh(t *testing.T) {
	t.Run("fixed", func(t *testing.T) {
		db := setupConfigstoreAt225(t)
		forceConcurrentIndexBuildsForUpgrade(t) // make the index builds depend on the pause alone
		setMigrationPause(t, &logstore.MatViewPauseOptions{CancelAfter: time.Second})
		refreshDone := startOldReplicaRefresh(t, db, 300)

		start := time.Now()
		require.NoError(t, runNewPodMigrations(t, "bifrost-migrate:new-pod", 2*time.Minute))
		require.Less(t, time.Since(start), time.Minute, "migrations must not wait out the 300s refresh")
		select {
		case err := <-refreshDone:
			require.ErrorContains(t, err, "canceling statement", "the old replica's refresh must have been cancelled")
		case <-time.After(10 * time.Second):
			t.Fatal("old replica refresh still running after migrations finished")
		}
		requireUpgradeIndexesApplied(t, db)
	})

	t.Run("control_without_pause_reproduces_stall", func(t *testing.T) {
		db := setupConfigstoreAt225(t)
		forceConcurrentIndexBuildsForUpgrade(t)
		setMigrationPause(t, nil)
		startOldReplicaRefresh(t, db, 300)

		start := time.Now()
		err := runNewPodMigrations(t, "bifrost-migrate:new-pod", 8*time.Second)
		require.Error(t, err, "without the pause the index build must stall behind the old replica's refresh")
		require.GreaterOrEqual(t, time.Since(start), 7*time.Second, "the run must have stalled until its budget, not failed early: %v", err)
	})
}

// TestUpgradeConfigstoreSmallIndexesIgnoreUnrelatedLongTransaction pins the
// small-table fix with a blocker the pause cannot remove: a long reader on logs
// that is not a refresh. CREATE INDEX CONCURRENTLY on a tiny config table waits
// for it; the plain small-table build does not.
func TestUpgradeConfigstoreSmallIndexesIgnoreUnrelatedLongTransaction(t *testing.T) {
	t.Run("fixed", func(t *testing.T) {
		db := setupConfigstoreAt225(t)
		startLongReader(t, db)

		start := time.Now()
		require.NoError(t, runNewPodMigrations(t, "bifrost-migrate:new-pod", time.Minute))
		require.Less(t, time.Since(start), 30*time.Second)
		requireUpgradeIndexesApplied(t, db)
	})

	t.Run("control_concurrent_build_reproduces_stall", func(t *testing.T) {
		db := setupConfigstoreAt225(t)
		forceConcurrentIndexBuildsForUpgrade(t)
		startLongReader(t, db)

		start := time.Now()
		err := runNewPodMigrations(t, "bifrost-migrate:new-pod", 8*time.Second)
		require.Error(t, err, "CREATE INDEX CONCURRENTLY must wait on the unrelated long transaction")
		require.GreaterOrEqual(t, time.Since(start), 7*time.Second, "the run must have stalled until its budget, not failed early: %v", err)
	})
}

// shortenMigrationLockWait makes the lock-wait budgets test-sized: busy is the
// budget a visibly busy holder earns; setting it equal to idle reproduces 2.3.0,
// where every holder got the same fixed budget.
func shortenMigrationLockWait(t *testing.T, idle, busy time.Duration) {
	t.Helper()
	prevRetry, prevIdle, prevBusy := advisoryLockRetryInterval, advisoryLockTimeout, advisoryLockBusyTimeout
	advisoryLockRetryInterval, advisoryLockTimeout, advisoryLockBusyTimeout = 200*time.Millisecond, idle, busy
	t.Cleanup(func() {
		advisoryLockRetryInterval, advisoryLockTimeout, advisoryLockBusyTimeout = prevRetry, prevIdle, prevBusy
	})
}

// stallFirstPod starts pod A's migrations and returns once A holds the migration
// lock with its index build parked behind the long reader, the state the second
// 2.3.0 pod found. The returned channel yields pod A's result.
func stallFirstPod(t *testing.T, db *gorm.DB) <-chan error {
	t.Helper()
	podA := make(chan error, 1)
	pod := openUpgradePod(t, "bifrost-migrate:pod-a")
	go func() { podA <- migratePod(pod, 2*time.Minute) }()
	require.Eventually(t, func() bool {
		var n int64
		return db.Raw(`SELECT count(*) FROM pg_stat_progress_create_index p
			JOIN pg_stat_activity a USING (pid)
			WHERE a.datname = current_database() AND a.application_name = 'bifrost-migrate:pod-a' AND p.phase LIKE 'waiting for%'`).Scan(&n).Error == nil && n > 0
	}, 30*time.Second, 50*time.Millisecond, "pod A's index build must park behind the long reader")
	return podA
}

// TestUpgradeSecondPodWaitsForBusyMigrationHolder reproduces the crash loop: the
// second 2.3.0 pod gave up on the configstore migration lock after 60s, blaming a
// stale session, while the first pod held it legitimately with its index build
// stuck behind an old replica. With the fix the second pod sees the holder is busy,
// keeps waiting, and finishes once the first pod does.
func TestUpgradeSecondPodWaitsForBusyMigrationHolder(t *testing.T) {
	t.Run("fixed", func(t *testing.T) {
		db := setupConfigstoreAt225(t)
		forceConcurrentIndexBuildsForUpgrade(t)
		endReader := startLongReader(t, db)
		shortenMigrationLockWait(t, 2*time.Second, time.Minute)
		podA := stallFirstPod(t, db)

		podB := make(chan error, 1)
		podBDB := openUpgradePod(t, "bifrost-migrate:pod-b")
		go func() { podB <- migratePod(podBDB, 2*time.Minute) }()
		select {
		case err := <-podB:
			t.Fatalf("pod B gave up while pod A was busy: %v", err)
		case <-time.After(6 * time.Second): // three times the idle budget
		}
		endReader()
		require.NoError(t, <-podA)
		require.NoError(t, <-podB, "pod B must find the migrations applied and start")
		requireUpgradeIndexesApplied(t, db)
	})

	t.Run("control_fixed_budget_reproduces_crash_loop", func(t *testing.T) {
		db := setupConfigstoreAt225(t)
		forceConcurrentIndexBuildsForUpgrade(t)
		endReader := startLongReader(t, db)
		shortenMigrationLockWait(t, 2*time.Second, 2*time.Second)
		podA := stallFirstPod(t, db)

		err := runNewPodMigrations(t, "bifrost-migrate:pod-b", time.Minute)
		require.ErrorContains(t, err, "failed to acquire configstore migration lock")
		// Even in this case the error must now describe a live holder, not a stale one.
		require.ErrorContains(t, err, "Do not terminate it")
		require.ErrorContains(t, err, "bifrost-migrate:pod-a")
		endReader()
		require.NoError(t, <-podA)
	})
}
