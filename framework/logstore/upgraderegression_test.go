package logstore

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/postgresconn"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Regression tests for the 2.2.5 -> 2.3.0 rolling upgrade that could not converge
// on a large log store (customer report, 2026-10-14). The new pod's
// logs_add_embedding_input_column migration (ALTER TABLE logs ADD COLUMN, which
// needs ACCESS EXCLUSIVE on logs) failed its 5s lock_timeout behind an old
// replica's materialized-view refresh and the pod exited. Each test rolls a fully
// migrated log store back to the 2.2.5 schema, reproduces the blocker, and runs
// the real migrations as the new pod; control cases switch the fix off and assert
// the original fatal error.

// logstoreUpgradeSchema isolates these tests from the package's other Postgres tests.
const logstoreUpgradeSchema = "logstore_upgrade_test"

// upgrade230LogsColumns are logstore migrations new in 2.3.0 that add a column to
// logs, with the column each one adds.
var upgrade230LogsColumns = map[string]string{
	"logs_add_embedding_input_column": "embedding_input",
	"logs_add_live_session_column":    "live_session",
}

// openLogstoreUpgradePod opens a pool on the upgrade schema tagged with app as its
// application_name, or skips the test when the local test Postgres is unreachable.
func openLogstoreUpgradePod(t *testing.T, app string) *gorm.DB {
	t.Helper()
	dsn := strings.Replace(postgresDSN, "search_path="+pgTestSchema, "search_path="+logstoreUpgradeSchema, 1) +
		" application_name='" + app + "'"
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Skip("Postgres not available")
	}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	if sqlDB.Ping() != nil {
		t.Skip("Postgres not available")
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// setupLogstoreAt225 migrates a fresh schema to the current version, then rolls
// the 2.3.0 logs-column migrations back (drops the columns and the migration
// rows), leaving the schema a 2.3.0 pod finds when it replaces 2.2.5.
func setupLogstoreAt225(t *testing.T) *gorm.DB {
	t.Helper()
	db := openLogstoreUpgradePod(t, "bifrost-migrate:setup")
	require.NoError(t, db.Exec("DROP SCHEMA IF EXISTS "+logstoreUpgradeSchema+" CASCADE").Error)
	require.NoError(t, db.Exec("CREATE SCHEMA "+logstoreUpgradeSchema).Error)
	t.Cleanup(func() { _ = db.Exec("DROP SCHEMA IF EXISTS " + logstoreUpgradeSchema + " CASCADE").Error })
	require.NoError(t, triggerMigrations(context.Background(), db, testLogger{}))
	for id, column := range upgrade230LogsColumns {
		require.NoError(t, db.Exec("ALTER TABLE logs DROP COLUMN IF EXISTS "+column).Error)
		require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = ?", id).Error)
	}
	return db
}

// requireUpgradeColumnsApplied asserts every 2.3.0 logs-column migration ran.
func requireUpgradeColumnsApplied(t *testing.T, db *gorm.DB) {
	t.Helper()
	for id, column := range upgrade230LogsColumns {
		var n int64
		require.NoError(t, db.Raw("SELECT count(*) FROM migrations WHERE id = ?", id).Scan(&n).Error)
		require.EqualValues(t, 1, n, "migration %s must be recorded", id)
		require.True(t, db.Migrator().HasColumn("logs", column), "logs.%s must exist", column)
	}
}

// startOldReplicaLogsRefresh reproduces a 2.2.5 replica's refresh pass on a large
// logs table: pg_try_advisory_lock on the matview refresh lock (as every release
// does), then REFRESH MATERIALIZED VIEW CONCURRENTLY over a view that reads logs
// and takes sleepSecs to compute. The statement holds ACCESS SHARE on logs for its
// whole run, which ALTER TABLE logs cannot get past. The channel yields its result.
func startOldReplicaLogsRefresh(t *testing.T, db *gorm.DB, sleepSecs int) <-chan error {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE TABLE old_replica_knob (secs float8 NOT NULL)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO old_replica_knob VALUES (0)`).Error)
	require.NoError(t, db.Exec(`CREATE MATERIALIZED VIEW mv_old_replica_hourly AS
		SELECT 1 AS k, (SELECT count(*) FROM logs) AS n,
		       length((SELECT pg_sleep(secs)::text FROM old_replica_knob)) AS slept`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX mv_old_replica_hourly_uniq ON mv_old_replica_hourly (k)`).Error)
	require.NoError(t, db.Exec(`UPDATE old_replica_knob SET secs = ?`, sleepSecs).Error)

	oldPod := openLogstoreUpgradePod(t, "bifrost-2.2.5-replica")
	sqlDB, err := oldPod.DB()
	require.NoError(t, err)
	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		var got bool
		return conn.QueryRowContext(context.Background(), "SELECT pg_try_advisory_lock($1)", matviewRefreshAdvisoryLockKey).Scan(&got) == nil && got
	}, time.Minute, 50*time.Millisecond)

	done := make(chan error, 1)
	go func() {
		_, err := conn.ExecContext(context.Background(), "REFRESH MATERIALIZED VIEW CONCURRENTLY mv_old_replica_hourly")
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", matviewRefreshAdvisoryLockKey)
		_ = conn.Close()
		done <- err
	}()
	t.Cleanup(func() {
		_ = db.Exec(`SELECT pg_cancel_backend(pid) FROM pg_stat_activity WHERE datname = current_database() AND query LIKE 'REFRESH MATERIALIZED VIEW CONCURRENTLY mv_old_replica_hourly%' AND pid <> pg_backend_pid()`).Error
	})
	require.Eventually(t, func() bool {
		var n int64
		return db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND state = 'active' AND query LIKE 'REFRESH MATERIALIZED VIEW CONCURRENTLY mv_old_replica_hourly%'`).Scan(&n).Error == nil && n == 1
	}, 10*time.Second, 50*time.Millisecond, "old replica refresh must be running")
	return done
}

// startLongLogsReader holds ACCESS SHARE on logs from a transaction that is not a
// matview refresh, so the pause can neither wait it out quickly nor cancel it. It
// returns a func that ends the transaction.
func startLongLogsReader(t *testing.T, db *gorm.DB) func() {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), "BEGIN")
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

// setLogstoreStepRetry overrides the lock-timeout retry for the rest of the test.
func setLogstoreStepRetry(t *testing.T, opts postgresconn.LockRetryOptions) {
	t.Helper()
	prev := migrationStepLockRetry
	migrationStepLockRetry = opts
	t.Cleanup(func() { migrationStepLockRetry = prev })
}

// disableMatViewPause switches the migration pause off for the rest of the test,
// which is how 2.3.0 behaved.
func disableMatViewPause(t *testing.T) {
	t.Helper()
	prev := pauseMatViewsForMigrations
	pauseMatViewsForMigrations = func(context.Context, *gorm.DB, schemas.Logger, MatViewPauseOptions) func() { return func() {} }
	t.Cleanup(func() { pauseMatViewsForMigrations = prev })
}

// noLockRetry fails a step on its first lock timeout, as 2.3.0 did.
var noLockRetry = postgresconn.LockRetryOptions{Budget: time.Nanosecond}

// migrateLogstorePod runs the real logstore migrations as a new pod would, on its
// own tagged pool, bounded by budget.
func migrateLogstorePod(t *testing.T, budget time.Duration, pause MatViewPauseOptions) error {
	t.Helper()
	pod := openLogstoreUpgradePod(t, "bifrost-migrate:new-pod")
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	return triggerMigrationsWithPause(ctx, pod, testLogger{}, pause)
}

// TestUpgradeLogstoreAddColumnCompletesBehindOldReplicaRefresh reproduces the
// fatal exit: logs_add_embedding_input_column timed out behind an old replica's
// refresh, whose ACCESS SHARE on logs outlasts any lock_timeout. With the fix the
// new pod pauses refreshes, cancels the in-flight pass, and the column lands.
func TestUpgradeLogstoreAddColumnCompletesBehindOldReplicaRefresh(t *testing.T) {
	t.Run("fixed", func(t *testing.T) {
		db := setupLogstoreAt225(t)
		refreshDone := startOldReplicaLogsRefresh(t, db, 300)

		start := time.Now()
		require.NoError(t, migrateLogstorePod(t, 2*time.Minute, MatViewPauseOptions{CancelAfter: time.Second}))
		require.Less(t, time.Since(start), time.Minute, "migrations must not wait out the 300s refresh")
		select {
		case err := <-refreshDone:
			require.ErrorContains(t, err, "canceling statement", "the old replica's refresh must have been cancelled")
		case <-time.After(10 * time.Second):
			t.Fatal("old replica refresh still running after migrations finished")
		}
		requireUpgradeColumnsApplied(t, db)
	})

	t.Run("control_without_pause_or_retry_reproduces_fatal_exit", func(t *testing.T) {
		db := setupLogstoreAt225(t)
		disableMatViewPause(t)
		setLogstoreStepRetry(t, noLockRetry)
		startOldReplicaLogsRefresh(t, db, 300)

		err := migrateLogstorePod(t, time.Minute, MatViewPauseOptions{CancelAfter: time.Second})
		require.Error(t, err)
		require.True(t, postgresconn.IsLockNotAvailableError(err), "expected the 2.3.0 lock_timeout failure, got: %v", err)
		require.ErrorContains(t, err, "column")
	})
}

// TestUpgradeLogstoreAddColumnRetriesBehindLongReader pins the retry with a
// blocker the pause cannot remove: a non-refresh reader holding ACCESS SHARE on
// logs past one 5s lock_timeout. The step must retry and succeed once it ends.
func TestUpgradeLogstoreAddColumnRetriesBehindLongReader(t *testing.T) {
	t.Run("fixed", func(t *testing.T) {
		db := setupLogstoreAt225(t)
		setLogstoreStepRetry(t, postgresconn.LockRetryOptions{Budget: time.Minute, InitialBackoff: 500 * time.Millisecond, MaxBackoff: time.Second})
		endReader := startLongLogsReader(t, db)
		time.AfterFunc(8*time.Second, endReader)

		start := time.Now()
		require.NoError(t, migrateLogstorePod(t, 2*time.Minute, MatViewPauseOptions{CancelAfter: time.Second}))
		require.GreaterOrEqual(t, time.Since(start), 5*time.Second, "the ALTER must have hit at least one lock timeout")
		requireUpgradeColumnsApplied(t, db)
	})

	t.Run("control_without_retry_reproduces_fatal_exit", func(t *testing.T) {
		db := setupLogstoreAt225(t)
		setLogstoreStepRetry(t, noLockRetry)
		startLongLogsReader(t, db)

		err := migrateLogstorePod(t, time.Minute, MatViewPauseOptions{CancelAfter: time.Second})
		require.Error(t, err)
		require.True(t, postgresconn.IsLockNotAvailableError(err), "expected the 2.3.0 lock_timeout failure, got: %v", err)
	})
}

// startOldSnapshot opens a REPEATABLE READ transaction that reads a table other
// than the one being indexed, as an old replica's unrelated long query does.
// CREATE INDEX CONCURRENTLY waits for every transaction older than it, whatever
// table that transaction reads. It returns a func that ends the transaction.
func startOldSnapshot(t *testing.T, db *gorm.DB) func() {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), "BEGIN ISOLATION LEVEL REPEATABLE READ")
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), "SELECT count(*) FROM migrations")
	require.NoError(t, err)
	var once sync.Once
	end := func() {
		once.Do(func() {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			_ = conn.Close()
		})
	}
	t.Cleanup(end)
	return end
}

// resetWarpUpdatedAtIndex drops idx_warp_conversations_updated_at and un-records
// its migration, the state a 2.2.x store is in before the 2.3.0 step runs.
func resetWarpUpdatedAtIndex(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec("DROP INDEX IF EXISTS idx_warp_conversations_updated_at").Error)
	require.NoError(t, db.Exec("DELETE FROM migrations WHERE id = 'logs_add_warp_conversations_updated_at_index'").Error)
}

// TestUpgradeLogstoreWarpIndexCompletesBehindOldSnapshot pins the small-table
// path of logs_add_warp_conversations_updated_at_index: the new table's index
// builds without CONCURRENTLY, so an old replica's open transaction elsewhere in
// the database cannot stall boot. The control forces the concurrent build and
// shows it waiting out that transaction.
func TestUpgradeLogstoreWarpIndexCompletesBehindOldSnapshot(t *testing.T) {
	t.Run("fixed", func(t *testing.T) {
		db := setupLogstoreAt225(t)
		resetWarpUpdatedAtIndex(t, db)
		startOldSnapshot(t, db)

		start := time.Now()
		require.NoError(t, migrateLogstorePod(t, time.Minute, MatViewPauseOptions{CancelAfter: time.Second}))
		require.Less(t, time.Since(start), 20*time.Second, "the warp index must not wait for the old snapshot")
		var valid bool
		require.NoError(t, db.Raw(`SELECT COALESCE(bool_and(indisvalid), false) FROM pg_index
			WHERE indexrelid = to_regclass('idx_warp_conversations_updated_at')`).Scan(&valid).Error)
		require.True(t, valid, "idx_warp_conversations_updated_at must exist and be valid")
	})

	t.Run("control_concurrent_build_waits_for_old_snapshot", func(t *testing.T) {
		db := setupLogstoreAt225(t)
		resetWarpUpdatedAtIndex(t, db)
		prev := smallTableForPlainIndex
		smallTableForPlainIndex = func(*gorm.DB, string) (bool, error) { return false, nil }
		t.Cleanup(func() { smallTableForPlainIndex = prev })
		startOldSnapshot(t, db)

		err := migrateLogstorePod(t, 15*time.Second, MatViewPauseOptions{CancelAfter: time.Second})
		require.Error(t, err, "the concurrent build must still be waiting on the old snapshot")
		t.Cleanup(func() { _ = db.Exec("DROP INDEX IF EXISTS idx_warp_conversations_updated_at").Error })
	})
}

// TestDescribeTableLockHoldersNamesOldReader pins the operator hint logged on the
// first lock timeout: it names the session holding logs by pid, lock mode and
// statement kind, never by statement text, and a lookup with no holders stays
// empty.
func TestDescribeTableLockHoldersNamesOldReader(t *testing.T) {
	db := setupLogstoreAt225(t)
	require.Empty(t, postgresconn.DescribeTableLockHolders(context.Background(), db, "logs"))
	startLongLogsReader(t, db)
	// A second holder whose statement hides a literal behind a newline and a
	// third that opens with a comment: neither may leak into the hint.
	for _, stmt := range []string{"SELECT\n'secret-literal'\nFROM logs", "/* secret-comment */ SELECT count(*) FROM logs"} {
		sqlDB, err := db.DB()
		require.NoError(t, err)
		conn, err := sqlDB.Conn(context.Background())
		require.NoError(t, err)
		_, err = conn.ExecContext(context.Background(), "BEGIN")
		require.NoError(t, err)
		_, err = conn.ExecContext(context.Background(), stmt)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = conn.ExecContext(context.Background(), "COMMIT"); _ = conn.Close() })
	}
	got := postgresconn.DescribeTableLockHolders(context.Background(), db, "logs")
	require.Contains(t, got, "AccessShareLock")
	require.Contains(t, got, "statement=SELECT")
	require.Contains(t, got, "statement=UNKNOWN", "a statement opening with a comment must classify as UNKNOWN")
	require.NotContains(t, got, "FROM logs", "the statement text must never be logged; only its kind")
	require.NotContains(t, got, "secret", "literals and comments must never reach the hint")
	require.Contains(t, got, "pg_cancel_backend")
}

// TestUpgradeLogstoreConfiguredLockRetryBudgetApplies pins that the log store's
// migration_lock_retry_budget reaches the step runner: with a budget of one
// nanosecond, a step blocked by a long reader fails on its first lock timeout
// instead of retrying for the default ten minutes.
func TestUpgradeLogstoreConfiguredLockRetryBudgetApplies(t *testing.T) {
	db := setupLogstoreAt225(t)
	setLogstoreStepRetry(t, postgresconn.LockRetryOptions{InitialBackoff: 100 * time.Millisecond, MaxBackoff: 100 * time.Millisecond})
	startLongLogsReader(t, db)

	pod := openLogstoreUpgradePod(t, "bifrost-migrate:new-pod")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg := &PostgresConfig{}
	cfg.MigrationLockRetryBudget = "1ns"
	start := time.Now()
	err := triggerMigrationsWithOptions(ctx, pod, testLogger{}, MatViewPauseOptions{CancelAfter: time.Second},
		postgresconn.MigrationLockRetryOptions(&cfg.Config, testLogger{}))
	require.Error(t, err)
	require.True(t, postgresconn.IsLockNotAvailableError(err), "expected a lock timeout, got: %v", err)
	require.Less(t, time.Since(start), 30*time.Second, "the configured budget must end the retries at once")
}
