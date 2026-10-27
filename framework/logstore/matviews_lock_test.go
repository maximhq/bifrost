package logstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
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
	stop := startMatViewRefresher(context.Background(), nil, 0, time.Minute, testLogger{}, nil, matViewSessionLimits{})
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

		require.NoError(t, refreshMatViews(context.Background(), db, 0))

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
		require.NoError(t, refreshMatViews(ctx, db, 0))
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
			err := refreshMatViews(ctx, db, 0)
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

		require.NoError(t, refreshMatViews(context.Background(), db, 0))

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
			  AND oid = to_regclass(?)
		)
	`, view).Scan(&exists).Error
	require.NoError(t, err)
	return exists
}

// TestMatViewRefreshTickSkippedWhileMigrating pins that a refresh tick does
// not touch the database at all while another node holds the migration lock:
// a nil *gorm.DB would panic inside refreshMatViews if the tick ran.
func TestMatViewRefreshTickSkippedWhileMigrating(t *testing.T) {
	now := time.Now()
	gate := &migrationLockProbe{now: func() time.Time { return now }, checkedAt: now, held: true}
	var ready atomic.Bool
	require.NotPanics(t, func() {
		runMatViewRefreshTick(context.Background(), nil, gate, time.Minute, time.Minute, testLogger{}, &ready, nil)
	})
	assert.False(t, ready.Load(), "a skipped tick must not mark the matviews ready")
}

// refreshBackendCount counts sessions running a REFRESH MATERIALIZED VIEW.
func refreshBackendCount(t *testing.T, db *gorm.DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.Raw(`SELECT count(*) FROM pg_stat_activity
		WHERE state = 'active' AND ltrim(query) ILIKE 'REFRESH MATERIALIZED VIEW%'`).Scan(&n).Error)
	return n
}

// backendAlive reports whether the backend with pid still exists.
func backendAlive(t *testing.T, db *gorm.DB, pid int) bool {
	t.Helper()
	var n int
	require.NoError(t, db.Raw("SELECT count(*) FROM pg_stat_activity WHERE pid = ?", pid).Scan(&n).Error)
	return n > 0
}

// blockMatViewRefresh holds an ACCESS EXCLUSIVE lock on logs in an
// open transaction, so any REFRESH of it waits in Postgres until the returned
// release runs. That stands in for a pass that runs for hours.
func blockMatViewRefresh(t *testing.T, db *gorm.DB) (release func()) {
	t.Helper()
	tx := db.Begin()
	require.NoError(t, tx.Error)
	require.NoError(t, tx.Exec("LOCK TABLE logs IN ACCESS EXCLUSIVE MODE").Error)
	released := false
	release = func() {
		if !released {
			released = true
			tx.Rollback()
		}
	}
	t.Cleanup(release)
	return release
}

// deadClientHelperEnv makes the test binary act as the refresher process that
// TestMatViewSessionDeadClientReleasesRefreshLock kills. Its value says whether
// the helper applies the matview session bounds ("bounded") or not ("plain").
const deadClientHelperEnv = "BIFROST_MATVIEW_DEAD_CLIENT_HELPER"

// TestMatViewDeadClientHelper is not a test on its own: run as a child process,
// it takes the refresh lock and runs a long statement until it is killed.
func TestMatViewDeadClientHelper(t *testing.T) {
	mode := os.Getenv(deadClientHelperEnv)
	if mode == "" {
		t.Skip("helper process only")
	}
	db := pauseTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	switch mode {
	case "bounded":
		_, err = matViewSessionLimits{}.apply(ctx, conn, 0)
		require.NoError(t, err)
	case "timed":
		// A refresh pass whose server statement timeout fires while its client
		// is frozen, as when the node running it disappears.
		_, err = matViewSessionLimits{idleSessionTimeout: 2 * time.Second}.apply(ctx, conn, 2*time.Second)
		require.NoError(t, err)
	}
	var acquired bool
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", matviewRefreshAdvisoryLockKey).Scan(&acquired))
	require.True(t, acquired)
	_, _ = conn.ExecContext(ctx, "SELECT pg_sleep(300) /* matview dead client helper */")
}

// startDeadClientHelper runs TestMatViewDeadClientHelper in a child process and
// returns it with the backend pid it is running on, once its statement runs.
func startDeadClientHelper(t *testing.T, observer *gorm.DB, mode string) (*exec.Cmd, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMatViewDeadClientHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), deadClientHelperEnv+"="+mode)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	var pid int
	require.Eventually(t, func() bool {
		return observer.Raw(`SELECT COALESCE(max(pid), 0) FROM pg_stat_activity
			WHERE state = 'active' AND query LIKE '%matview dead client helper%' AND pid <> pg_backend_pid()`).Scan(&pid).Error == nil && pid != 0
	}, 20*time.Second, 100*time.Millisecond, "helper statement never started")
	return cmd, pid
}

// TestMatViewSessionDeadClientReleasesRefreshLock reproduces a pod killed mid
// refresh: the process holding the refresh lock gets SIGKILL, so no cancel
// request is ever sent. The client check on the bounded session must make the
// server end the statement and the session, which frees the refresh lock. The
// unbounded control shows the gap this closes: that backend keeps running.
func TestMatViewSessionDeadClientReleasesRefreshLock(t *testing.T) {
	if os.Getenv(deadClientHelperEnv) != "" {
		t.Skip("running as helper")
	}
	observer := pauseTestDB(t)

	t.Run("unbounded session outlives its killed client", func(t *testing.T) {
		cmd, pid := startDeadClientHelper(t, observer, "plain")
		require.NoError(t, cmd.Process.Kill())
		_ = cmd.Wait()
		time.Sleep(3 * time.Second)
		require.True(t, backendAlive(t, observer, pid), "without the client check the statement keeps running")
		require.NoError(t, observer.Exec("SELECT pg_terminate_backend(?)", pid).Error)
		require.Eventually(t, func() bool { return !backendAlive(t, observer, pid) }, 5*time.Second, 50*time.Millisecond)
	})

	t.Run("bounded session ends with its killed client", func(t *testing.T) {
		cmd, pid := startDeadClientHelper(t, observer, "bounded")
		require.NoError(t, cmd.Process.Kill())
		_ = cmd.Wait()
		require.Eventually(t, func() bool { return !backendAlive(t, observer, pid) },
			matViewClientCheckInterval+10*time.Second, 200*time.Millisecond,
			"the server must end a statement whose client is gone")
		other := awaitTestAdvisoryLock(t, observer, matviewRefreshAdvisoryLockKey, 5*time.Second)
		releaseTestAdvisoryLock(t, other, matviewRefreshAdvisoryLockKey)
	})

	// SIGSTOP keeps the helper's socket open without a FIN, which is what the
	// server sees when the node running a refresh disappears. The statement
	// timeout cancels the statement, but the advisory lock is session-scoped, so
	// only ending the idle session frees it for the other replicas.
	t.Run("timed out session of a vanished client releases the refresh lock", func(t *testing.T) {
		cmd, pid := startDeadClientHelper(t, observer, "timed")
		require.NoError(t, cmd.Process.Signal(syscall.SIGSTOP))
		t.Cleanup(func() { _ = cmd.Process.Signal(syscall.SIGCONT) })
		require.Eventually(t, func() bool { return !backendAlive(t, observer, pid) },
			20*time.Second, 200*time.Millisecond,
			"the server must end the idle session that still holds the refresh lock")
		other := awaitTestAdvisoryLock(t, observer, matviewRefreshAdvisoryLockKey, 5*time.Second)
		releaseTestAdvisoryLock(t, other, matviewRefreshAdvisoryLockKey)
	})
}

// TestMatViewServerStatementTimeout pins the per-pass server bound: the time
// left on ctx plus the grace, and none without a deadline.
func TestMatViewServerStatementTimeout(t *testing.T) {
	require.Zero(t, matViewServerStatementTimeout(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	got := matViewServerStatementTimeout(ctx)
	require.Greater(t, got, time.Minute+matViewServerTimeoutGrace-5*time.Second)
	require.LessOrEqual(t, got, time.Minute+matViewServerTimeoutGrace)

	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	require.Equal(t, matViewServerTimeoutGrace, matViewServerStatementTimeout(expired))
}

// TestCloseCancelsRunningMatViewRefresh pins graceful shutdown: Close must
// cancel a REFRESH that is still running on the server and free the refresh
// lock, instead of leaving it running after the process exits.
func TestCloseCancelsRunningMatViewRefresh(t *testing.T) {
	_, db := setupPerfTestDB(t)
	observer := pauseTestDB(t)
	resetTestMatViewRefreshGate()
	blockMatViewRefresh(t, observer)

	storeDB := pauseTestDB(t)
	store := &RDBLogStore{db: storeDB, logger: testLogger{}}
	store.maintenanceCtx, store.maintenanceCancel = context.WithCancel(context.Background())
	passErr := make(chan error, 1)
	require.True(t, store.goMaintenance(func() {
		ctx, cancel := context.WithTimeout(store.maintenanceContext(), time.Hour)
		defer cancel()
		passErr <- refreshMatViewsLimited(ctx, storeDB, 0, store.matViewLimits)
	}))
	require.Eventually(t, func() bool { return refreshBackendCount(t, observer) == 1 },
		10*time.Second, 50*time.Millisecond, "the refresh must be running before Close")

	started := time.Now()
	require.NoError(t, store.Close(context.Background()))
	require.Less(t, time.Since(started), matViewShutdownWait, "Close must not wait out the refresh")

	err := <-passErr
	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled) || errors.Is(store.maintenanceCtx.Err(), context.Canceled))
	require.Zero(t, refreshBackendCount(t, observer), "no REFRESH may keep running after Close")
	other := awaitTestAdvisoryLock(t, db, matviewRefreshAdvisoryLockKey, 5*time.Second)
	releaseTestAdvisoryLock(t, other, matviewRefreshAdvisoryLockKey)

	require.False(t, store.goMaintenance(func() {}), "no maintenance may start after Close")
}

// TestCloseWithoutMaintenance pins that Close still works on a store built
// without newPostgresLogStore (no maintenance context).
func TestCloseWithoutMaintenance(t *testing.T) {
	db := pauseTestDB(t)
	store := &RDBLogStore{db: db, logger: testLogger{}}
	require.NoError(t, store.Close(context.Background()))
}

// TestMatViewTickStateTimeoutStreak pins the repeated-timeout warning: it fires
// once when the streak reaches the threshold and re-arms after a success.
func TestMatViewTickStateTimeoutStreak(t *testing.T) {
	var s matViewTickState
	require.False(t, s.recordOutcome(true))
	require.True(t, s.recordOutcome(true), "second timeout in a row warns")
	require.False(t, s.recordOutcome(true), "warn once per streak")
	require.False(t, s.recordOutcome(false))
	require.Zero(t, s.consecutiveTimeouts)
	require.False(t, s.recordOutcome(true))
	require.True(t, s.recordOutcome(true), "a new streak warns again")
}

// TestResolveMatViewTempFileLimit pins config handling: valid values pass,
// invalid ones are dropped so they cannot fail every pass.
func TestResolveMatViewTempFileLimit(t *testing.T) {
	require.Equal(t, "", resolveMatViewTempFileLimit("", testLogger{}))
	require.Equal(t, "20GB", resolveMatViewTempFileLimit(" 20GB ", testLogger{}))
	require.Equal(t, "", resolveMatViewTempFileLimit("20 gigs", testLogger{}))
	// Out of the server's range: dropped at startup, not failed on every pass.
	require.Equal(t, "", resolveMatViewTempFileLimit("2TB", testLogger{}))
}
