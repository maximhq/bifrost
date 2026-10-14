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
		_ = db.Exec(`SELECT pg_cancel_backend(pid) FROM pg_stat_activity WHERE query LIKE 'REFRESH MATERIALIZED VIEW CONCURRENTLY mv_old_replica_hourly%' AND pid <> pg_backend_pid()`).Error
	})
	require.Eventually(t, func() bool {
		var n int64
		return db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE state = 'active' AND query LIKE 'REFRESH MATERIALIZED VIEW CONCURRENTLY mv_old_replica_hourly%'`).Scan(&n).Error == nil && n == 1
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
