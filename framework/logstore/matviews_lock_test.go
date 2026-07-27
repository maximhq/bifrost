package logstore

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestResolveMatViewRefreshIntervalDefaults(t *testing.T) {
	assert.Equal(t, time.Minute, resolveMatViewRefreshInterval("", testLogger{}))
	assert.Equal(t, time.Minute, resolveMatViewRefreshInterval("not-a-duration", testLogger{}))
	assert.Equal(t, minMatViewRefreshInterval, resolveMatViewRefreshInterval("1s", testLogger{}))
	assert.Equal(t, 5*time.Minute, resolveMatViewRefreshInterval("5m", testLogger{}))
}

func TestRefreshMatViewsAdvisoryLockLifecycle(t *testing.T) {
	_, db := setupPerfTestDB(t)

	t.Run("normal refresh releases lock", func(t *testing.T) {
		resetTestMatViewRefreshGate()

		require.NoError(t, refreshMatViews(context.Background(), db))

		conn := acquireTestAdvisoryLock(t, db, matviewRefreshAdvisoryLockKey)
		releaseTestAdvisoryLock(t, conn, matviewRefreshAdvisoryLockKey)
	})

	t.Run("held lock makes refresh skip without blocking", func(t *testing.T) {
		resetTestMatViewRefreshGate()
		holder := acquireTestAdvisoryLock(t, db, matviewRefreshAdvisoryLockKey)
		defer releaseTestAdvisoryLock(t, holder, matviewRefreshAdvisoryLockKey)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		start := time.Now()
		require.NoError(t, refreshMatViews(ctx, db))
		assert.Less(t, time.Since(start), time.Second)
	})

	t.Run("closed holder session lets later refresh acquire lock", func(t *testing.T) {
		resetTestMatViewRefreshGate()
		holderDB, holder := acquireTestAdvisoryLockOnIsolatedPool(t, matviewRefreshAdvisoryLockKey)
		closeTestAdvisoryLockSession(t, holderDB, holder)

		require.NoError(t, refreshMatViews(context.Background(), db))

		conn := acquireTestAdvisoryLock(t, db, matviewRefreshAdvisoryLockKey)
		releaseTestAdvisoryLock(t, conn, matviewRefreshAdvisoryLockKey)
	})
}

func TestEnsureMatViewsSharesRefreshAdvisoryLock(t *testing.T) {
	_, db := setupPerfTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.Exec("DROP MATERIALIZED VIEW IF EXISTS mv_filter_users CASCADE").Error)
	require.False(t, testMatViewExists(t, db, "mv_filter_users"))

	holder := acquireTestAdvisoryLock(t, db, matviewRefreshAdvisoryLockKey)
	maintained, err := ensureMatViews(ctx, db)
	require.NoError(t, err)
	require.False(t, maintained, "ensureMatViews should report it did not maintain the views while the lock is held elsewhere")
	require.False(t, testMatViewExists(t, db, "mv_filter_users"), "ensureMatViews should skip while refresh lock is held elsewhere")

	releaseTestAdvisoryLock(t, holder, matviewRefreshAdvisoryLockKey)
	maintained, err = ensureMatViews(ctx, db)
	require.NoError(t, err)
	require.True(t, maintained, "ensureMatViews should report it maintained the views once the lock is free")
	require.True(t, testMatViewExists(t, db, "mv_filter_users"))
}

func TestMigrationLockContextCancellationAndSessionRelease(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping test")
	}

	holderDB, holder := acquireTestAdvisoryLockOnIsolatedPool(t, migrationAdvisoryLockKey)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	lock, err := acquireMigrationLock(ctx, db, testLogger{})
	require.Error(t, err)
	require.Nil(t, lock)

	closeTestAdvisoryLockSession(t, holderDB, holder)

	lock, err = acquireMigrationLock(context.Background(), db, testLogger{})
	require.NoError(t, err)
	lock.release(context.Background())
}

func resetTestMatViewRefreshGate() {
	refreshGate.mu.Lock()
	refreshGate.lastActivity = 0
	refreshGate.lastForcedAt = time.Time{}
	refreshGate.initialized = false
	refreshGate.mu.Unlock()
}

func acquireTestAdvisoryLock(t *testing.T, db *gorm.DB, key int64) *sql.Conn {
	t.Helper()

	sqlDB, err := db.DB()
	require.NoError(t, err)

	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)

	var acquired bool
	err = conn.QueryRowContext(context.Background(), "SELECT pg_try_advisory_lock($1)", key).Scan(&acquired)
	if err != nil {
		_ = conn.Close()
	}
	require.NoError(t, err)
	if !acquired {
		_ = conn.Close()
	}
	require.Truef(t, acquired, "expected to acquire advisory lock %d", key)

	return conn
}

func acquireTestAdvisoryLockOnIsolatedPool(t *testing.T, key int64) (*gorm.DB, *sql.Conn) {
	t.Helper()

	db := trySetupPostgresDB(t)
	require.NotNil(t, db, "Postgres not available")

	return db, acquireTestAdvisoryLock(t, db, key)
}

func closeTestAdvisoryLockSession(t *testing.T, db *gorm.DB, conn *sql.Conn) {
	t.Helper()

	require.NoError(t, conn.Close())
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
}

func releaseTestAdvisoryLock(t *testing.T, conn *sql.Conn, key int64) {
	t.Helper()
	if conn == nil {
		return
	}
	_, err := conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", key)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

func testMatViewExists(t *testing.T, db *gorm.DB, view string) bool {
	t.Helper()

	var exists bool
	err := db.Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM pg_class
			WHERE relkind = 'm'
			  AND relname = ?
		)
	`, view).Scan(&exists).Error
	require.NoError(t, err)
	return exists
}
