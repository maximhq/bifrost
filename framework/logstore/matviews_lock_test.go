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
	// "off" and non-positive durations disable maintenance rather than clamping
	// up to the floor.
	assert.Equal(t, time.Duration(0), resolveMatViewRefreshInterval("off", testLogger{}))
	assert.Equal(t, time.Duration(0), resolveMatViewRefreshInterval("0s", testLogger{}))
	assert.Equal(t, time.Duration(0), resolveMatViewRefreshInterval("-1m", testLogger{}))
}

func TestStartMatViewRefresherDisabled(t *testing.T) {
	// A disabled interval must not start a ticker (time.NewTicker panics on
	// non-positive intervals); the returned stop function is a no-op.
	stop := startMatViewRefresher(context.Background(), nil, 0, time.Minute, testLogger{}, nil)
	stop()
}

func TestSelfHealSkippedWhenMaintenanceDisabled(t *testing.T) {
	// With maintenance disabled, self-heal must not recreate the views (a nil
	// db would panic if the repair goroutine ran) and must not arm the
	// single-flight state.
	s := &RDBLogStore{matViewMaintenanceDisabled: true}
	s.triggerMatViewSelfHeal()
	assert.False(t, s.matViewHealInFlight.Load())
}

func TestResolveMatViewRefreshTimeoutDefaults(t *testing.T) {
	// Unset derives from the interval: 5x, floored at 5m.
	assert.Equal(t, matViewRefreshTimeoutFloor, resolveMatViewRefreshTimeout("", time.Minute, testLogger{}))
	assert.Equal(t, 25*time.Minute, resolveMatViewRefreshTimeout("", 5*time.Minute, testLogger{}))
	// Derived value is capped.
	assert.Equal(t, maxMatViewRefreshTimeout, resolveMatViewRefreshTimeout("", time.Hour, testLogger{}))
	// Explicit values are honoured, clamped at both ends.
	assert.Equal(t, 90*time.Second, resolveMatViewRefreshTimeout("90s", time.Minute, testLogger{}))
	assert.Equal(t, minMatViewRefreshTimeout, resolveMatViewRefreshTimeout("1s", time.Minute, testLogger{}))
	assert.Equal(t, maxMatViewRefreshTimeout, resolveMatViewRefreshTimeout("2h", time.Minute, testLogger{}))
	// A bad string falls back to the derived default rather than failing startup.
	assert.Equal(t, matViewRefreshTimeoutFloor, resolveMatViewRefreshTimeout("not-a-duration", time.Minute, testLogger{}))
}

// TestMatViewRefreshOrderRotates guards against tail starvation: if a refresh pass
// is consistently cut short by its deadline, a fixed order would mean the last
// views never refresh again.
func TestMatViewRefreshOrderRotates(t *testing.T) {
	if len(filterMatViews) < 2 {
		t.Fatalf("expected at least 2 filter matviews to test rotation, got %d", len(filterMatViews))
	}

	// mv_logs_hourly must stay pinned first — dashboards depend on it.
	firstSeen := make(map[string]struct{})
	for range len(filterMatViews) {
		order := matViewRefreshOrder()
		require.Len(t, order, len(filterMatViews)+1)
		assert.Equal(t, "mv_logs_hourly", order[0], "mv_logs_hourly must always refresh first")
		firstSeen[order[1]] = struct{}{}

		// Every view must still appear exactly once per pass.
		assert.ElementsMatch(t, allMatViewNames(), order)
	}

	assert.Equal(t, len(filterMatViews), len(firstSeen),
		"each filter matview should lead a pass across a full rotation, got %v", firstSeen)
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

	// A refresh cut short by its own deadline must still release the advisory lock.
	// The deferred unlock runs on a context derived with WithoutCancel precisely
	// because ctx is already expired at that point; if the unlock cannot be
	// confirmed, the pooled connection is discarded so Postgres reclaims the lock.
	// Either way the invariant is the same: the lock is free when the call returns.
	t.Run("deadline-exceeded refresh still releases lock", func(t *testing.T) {
		timedOut := false
		for _, budget := range []time.Duration{time.Millisecond, 5 * time.Millisecond, 25 * time.Millisecond} {
			resetTestMatViewRefreshGate()

			ctx, cancel := context.WithTimeout(context.Background(), budget)
			err := refreshMatViews(ctx, db)
			cancel()
			if err != nil {
				timedOut = true
			}

			// Whether or not this budget was tight enough to cut the refresh short,
			// the lock must become available.
			conn := awaitTestAdvisoryLock(t, db, matviewRefreshAdvisoryLockKey, 5*time.Second)
			releaseTestAdvisoryLock(t, conn, matviewRefreshAdvisoryLockKey)
		}
		if !timedOut {
			t.Log("no budget was tight enough to interrupt a refresh on this database; lock-release invariant still verified")
		}
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
	require.NoError(t, ensureMatViews(ctx, db))
	require.False(t, testMatViewExists(t, db, "mv_filter_users"), "ensureMatViews should skip while refresh lock is held elsewhere")

	releaseTestAdvisoryLock(t, holder, matviewRefreshAdvisoryLockKey)
	require.NoError(t, ensureMatViews(ctx, db))
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

// awaitTestAdvisoryLock polls until the advisory lock can be taken. When a refresh
// is cancelled mid-statement the backing session is torn down rather than cleanly
// unlocked, and Postgres reclaims the lock when it reaps that backend — which is not
// synchronous with the client call returning. On timeout it reports who still holds
// the lock so a genuine leak is distinguishable from teardown lag.
func awaitTestAdvisoryLock(t *testing.T, db *gorm.DB, key int64, wait time.Duration) *sql.Conn {
	t.Helper()

	sqlDB, err := db.DB()
	require.NoError(t, err)

	deadline := time.Now().Add(wait)
	for {
		conn, err := sqlDB.Conn(context.Background())
		require.NoError(t, err)

		var acquired bool
		err = conn.QueryRowContext(context.Background(), "SELECT pg_try_advisory_lock($1)", key).Scan(&acquired)
		if err == nil && acquired {
			return conn
		}
		_ = conn.Close()

		if time.Now().After(deadline) {
			var holders string
			_ = db.Raw(`
				SELECT COALESCE(string_agg(format('pid=%s state=%s query=%s', a.pid, a.state, left(a.query, 60)), '; '), 'none')
				FROM pg_locks l
				JOIN pg_stat_activity a ON a.pid = l.pid
				WHERE l.locktype = 'advisory' AND l.objid = ?
			`, key).Scan(&holders).Error
			require.Failf(t, "advisory lock never released",
				"key %d still held after %s; holders: %s (lastErr=%v)", key, wait, holders, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
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
