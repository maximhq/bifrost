package logstore

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/postgresconn"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// pauseTestDB returns the package's Postgres test DB or skips the test.
func pauseTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available")
	}
	return db
}

// dedicatedConn returns a connection outside db's pool reuse for the test.
func dedicatedConn(t *testing.T, db *gorm.DB) *sql.Conn {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock_all()")
		_ = conn.Close()
	})
	return conn
}

// takeRefreshLock takes the matview refresh lock on conn the way every real
// refresher does, with pg_try_advisory_lock retried until it succeeds. A blocking
// pg_advisory_lock must not be used here: while it waits it holds a snapshot, and
// a CREATE INDEX CONCURRENTLY run by another test package's migrations (which
// hold this same lock through their pause) would wait on that snapshot forever.
func takeRefreshLock(t *testing.T, conn *sql.Conn) {
	t.Helper()
	require.Eventually(t, func() bool {
		var got bool
		return conn.QueryRowContext(context.Background(), "SELECT pg_try_advisory_lock($1)", matviewRefreshAdvisoryLockKey).Scan(&got) == nil && got
	}, 2*time.Minute, 50*time.Millisecond, "matview refresh lock never became free")
}

// refreshLockFree reports whether some session other than the pause holder can
// take the matview refresh lock right now, releasing it again if it could. This
// is the exact check every refresher, of every release, makes before a pass.
func refreshLockFree(t *testing.T, conn *sql.Conn) bool {
	t.Helper()
	var got bool
	require.NoError(t, conn.QueryRowContext(context.Background(), "SELECT pg_try_advisory_lock($1)", matviewRefreshAdvisoryLockKey).Scan(&got))
	if got {
		_, err := conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", matviewRefreshAdvisoryLockKey)
		require.NoError(t, err)
	}
	return got
}

// TestPauseMatViewMaintenanceCancelsInFlightRefresh reproduces the 2.3.0 upgrade
// blocker with a synthetic slow view: an older replica holds the refresh lock and
// is mid-REFRESH when the new pod starts migrating. The pause must cancel that
// REFRESH after CancelAfter, take the lock so no replica starts another pass while
// migrations run, and give the lock back on release.
func TestPauseMatViewMaintenanceCancelsInFlightRefresh(t *testing.T) {
	db := pauseTestDB(t)
	require.NoError(t, db.Exec("DROP MATERIALIZED VIEW IF EXISTS mv_pause_test").Error)
	// Synthetic stand-in for mv_logs_hourly on a huge logs table: every refresh
	// takes 30s. WITH NO DATA so creating it does not sleep.
	require.NoError(t, db.Exec("CREATE MATERIALIZED VIEW mv_pause_test AS SELECT 1 AS x FROM pg_sleep(30) WITH NO DATA").Error)
	t.Cleanup(func() { _ = db.Exec("DROP MATERIALIZED VIEW IF EXISTS mv_pause_test").Error })

	// Advisory locks are database-wide, and other test packages migrating against
	// the same database briefly hold the refresh lock through their own pause, so
	// budgets below are generous and assertions are about cancellation, not speed.
	oldReplica := dedicatedConn(t, db)
	takeRefreshLock(t, oldReplica)
	refreshErr := make(chan error, 1)
	go func() {
		_, err := oldReplica.ExecContext(context.Background(), "REFRESH MATERIALIZED VIEW mv_pause_test")
		// Like refreshMatViews, the old replica unlocks once its pass ends.
		_, _ = oldReplica.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", matviewRefreshAdvisoryLockKey)
		refreshErr <- err
	}()
	require.Eventually(t, func() bool {
		var n int
		return db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE state = 'active' AND query LIKE 'REFRESH MATERIALIZED VIEW mv_pause_test%'`).Scan(&n).Error == nil && n == 1
	}, 10*time.Second, 50*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pauseResult := make(chan error, 1)
	var release func()
	go func() {
		var err error
		release, err = PauseMatViewMaintenance(ctx, db, testLogger{}, MatViewPauseOptions{CancelAfter: 500 * time.Millisecond})
		pauseResult <- err
	}()

	select {
	case err := <-refreshErr:
		require.Error(t, err)
		require.Contains(t, err.Error(), "canceling statement")
	case <-time.After(15 * time.Second):
		t.Fatal("the old replica's 30s REFRESH was not cancelled")
	}
	require.NoError(t, <-pauseResult)

	other := dedicatedConn(t, db)
	require.False(t, refreshLockFree(t, other), "no replica may start a refresh pass while migrations hold the pause")
	release()
	release() // idempotent
	require.True(t, refreshLockFree(t, other), "release must hand the refresh lock back")
}

// TestPauseMatViewMaintenanceNeverCancelsOtherWork pins that the pause only ever
// cancels REFRESH MATERIALIZED VIEW statements: a holder doing anything else is
// waited for, and when the budget runs out the pause gives up without touching it.
func TestPauseMatViewMaintenanceNeverCancelsOtherWork(t *testing.T) {
	db := pauseTestDB(t)
	holder := dedicatedConn(t, db)
	takeRefreshLock(t, holder)
	holderErr := make(chan error, 1)
	go func() {
		_, err := holder.ExecContext(context.Background(), "SELECT pg_sleep(3)")
		holderErr <- err
	}()
	time.Sleep(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_, err := PauseMatViewMaintenance(ctx, db, testLogger{}, MatViewPauseOptions{CancelAfter: 100 * time.Millisecond})
	require.Error(t, err, "the pause must give up rather than cancel unrelated work")
	require.NoError(t, <-holderErr, "a non-REFRESH statement must never be cancelled")
}

// TestPauseMatViewsForMigrationsFailsOpen pins that a pause that cannot be taken
// never fails migrations: the wrapper returns a usable no-op release.
func TestPauseMatViewsForMigrationsFailsOpen(t *testing.T) {
	db := pauseTestDB(t)
	holder := dedicatedConn(t, db)
	takeRefreshLock(t, holder)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	release := PauseMatViewsForMigrations(ctx, db, testLogger{}, MatViewPauseOptions{})
	require.NotNil(t, release)
	release()
}

// TestRunMigrationStepsRetriesLockTimeout reproduces the fatal 2.3.0 exit: ALTER
// TABLE logs ADD COLUMN under a short lock_timeout failed because an older
// replica's long read held ACCESS SHARE on logs, and the pod exited. The step must
// now be retried with backoff and succeed once the reader finishes.
func TestRunMigrationStepsRetriesLockTimeout(t *testing.T) {
	db := pauseTestDB(t)
	prev := migrationStepLockRetry
	migrationStepLockRetry = postgresconn.LockRetryOptions{Budget: 20 * time.Second, InitialBackoff: 200 * time.Millisecond, MaxBackoff: 500 * time.Millisecond}
	t.Cleanup(func() { migrationStepLockRetry = prev })

	require.NoError(t, db.Exec("DROP TABLE IF EXISTS lock_retry_logs").Error)
	require.NoError(t, db.Exec("CREATE TABLE lock_retry_logs (id int)").Error)
	t.Cleanup(func() { _ = db.Exec("DROP TABLE IF EXISTS lock_retry_logs").Error })

	reader := dedicatedConn(t, db)
	_, err := reader.ExecContext(context.Background(), "BEGIN")
	require.NoError(t, err)
	_, err = reader.ExecContext(context.Background(), "SELECT count(*) FROM lock_retry_logs")
	require.NoError(t, err)
	time.AfterFunc(2*time.Second, func() { _, _ = reader.ExecContext(context.Background(), "COMMIT") })

	attempts := 0
	step := migrationStep{IDs: []string{"test_lock_retry_step"}, run: func(ctx context.Context, db *gorm.DB, _ schemas.Logger) error {
		attempts++
		return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SET LOCAL lock_timeout = '300ms'").Error; err != nil {
				return err
			}
			return tx.Exec("ALTER TABLE lock_retry_logs ADD COLUMN IF NOT EXISTS embedding_input text").Error
		})
	}}
	require.NoError(t, runMigrationSteps(context.Background(), db, testLogger{}, []migrationStep{step}))
	require.Greater(t, attempts, 1, "the step should have hit at least one lock timeout before succeeding")
	require.True(t, db.Migrator().HasColumn("lock_retry_logs", "embedding_input"))
}

// TestRunMigrationStepsLockRetryIsBounded pins that the retry gives up once its
// budget is spent, returning the lock-timeout error instead of hanging.
func TestRunMigrationStepsLockRetryIsBounded(t *testing.T) {
	db := pauseTestDB(t)
	prev := migrationStepLockRetry
	migrationStepLockRetry = postgresconn.LockRetryOptions{Budget: time.Second, InitialBackoff: 100 * time.Millisecond, MaxBackoff: 200 * time.Millisecond}
	t.Cleanup(func() { migrationStepLockRetry = prev })

	require.NoError(t, db.Exec("DROP TABLE IF EXISTS lock_retry_bounded").Error)
	require.NoError(t, db.Exec("CREATE TABLE lock_retry_bounded (id int)").Error)
	t.Cleanup(func() { _ = db.Exec("DROP TABLE IF EXISTS lock_retry_bounded").Error })

	reader := dedicatedConn(t, db)
	_, err := reader.ExecContext(context.Background(), "BEGIN")
	require.NoError(t, err)
	_, err = reader.ExecContext(context.Background(), "SELECT count(*) FROM lock_retry_bounded")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = reader.ExecContext(context.Background(), "ROLLBACK") })

	step := migrationStep{IDs: []string{"test_lock_retry_bounded"}, run: func(ctx context.Context, db *gorm.DB, _ schemas.Logger) error {
		return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SET LOCAL lock_timeout = '100ms'").Error; err != nil {
				return err
			}
			return tx.Exec("ALTER TABLE lock_retry_bounded ADD COLUMN IF NOT EXISTS c int").Error
		})
	}}
	err = runMigrationSteps(context.Background(), db, testLogger{}, []migrationStep{step})
	require.Error(t, err)
	require.True(t, postgresconn.IsLockNotAvailableError(err))
}

// TestMigrationMatViewPauseOptionsReadsLogStoreSetting pins that the one
// migration_matview_cancel_after setting on a Postgres log store is what other
// stores' migrations (config store, enterprise) receive, including "off", and
// that anything else falls back to the default.
func TestMigrationMatViewPauseOptionsReadsLogStoreSetting(t *testing.T) {
	pg := func(v string) *Config {
		return &Config{Type: LogStoreTypePostgres, Config: &PostgresConfig{MigrationMatViewCancelAfter: v}}
	}
	require.Equal(t, 15*time.Second, MigrationMatViewPauseOptions(pg("15s"), testLogger{}).CancelAfter)
	require.Zero(t, MigrationMatViewPauseOptions(pg("off"), testLogger{}).CancelAfter, `"off" must reach other stores' migrations`)
	require.Equal(t, DefaultMatViewPauseCancelAfter, MigrationMatViewPauseOptions(pg(""), testLogger{}).CancelAfter)
	require.Equal(t, DefaultMatViewPauseCancelAfter, MigrationMatViewPauseOptions(nil, testLogger{}).CancelAfter)
	require.Equal(t, DefaultMatViewPauseCancelAfter, MigrationMatViewPauseOptions(&Config{Type: LogStoreTypeSQLite, Config: &SQLiteConfig{}}, testLogger{}).CancelAfter)
}
