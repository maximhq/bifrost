package postgresconn

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// advisoryTestDSN matches the postgres service in tests/docker-compose.yml.
const advisoryTestDSN = "host=localhost port=5432 user=bifrost password=bifrost_password dbname=bifrost sslmode=disable"

// advisoryTestKey is a key no production code uses.
const advisoryTestKey int64 = 990_000_001

// openAdvisoryTestDB opens a pool tagged with application_name, or skips the test
// when the local test Postgres is unreachable.
func openAdvisoryTestDB(t *testing.T, app string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", advisoryTestDSN+" application_name="+quoteLibpqValue(app))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Skip("local test Postgres is not reachable")
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// holdAdvisoryKey takes the test key on a dedicated connection of db and returns
// a release func.
func holdAdvisoryKey(t *testing.T, db *sql.DB) func() {
	t.Helper()
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), "SELECT pg_advisory_lock($1)", advisoryTestKey)
	require.NoError(t, err)
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", advisoryTestKey)
		_ = conn.Close()
	}
	t.Cleanup(release)
	return release
}

// waiterConn returns a dedicated connection for the pod that waits for the lock.
func waiterConn(t *testing.T) *sql.Conn {
	t.Helper()
	db := openAdvisoryTestDB(t, "bifrost-migrate:waiter-pod")
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock_all()")
		_ = conn.Close()
	})
	return conn
}

// TestWaitForAdvisoryLockWaitsOutBusyHolder reproduces the 2.3.0 crash loop: the
// pod holding the migration lock was alive, running a CREATE INDEX CONCURRENTLY
// on a sibling connection that waited behind another replica's long refresh. The
// lock connection itself sat idle, and the waiter gave up after 60s blaming a
// stale session. A visibly busy holder must now earn the longer budget.
func TestWaitForAdvisoryLockWaitsOutBusyHolder(t *testing.T) {
	holderPod := openAdvisoryTestDB(t, "bifrost-migrate:holder-pod")
	release := holdAdvisoryKey(t, holderPod)

	// The holder pod's DDL, running on a sibling connection.
	sleeper, err := holderPod.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sleeper.Close() })
	go func() { _, _ = sleeper.ExecContext(context.Background(), "SELECT pg_sleep(4)") }()
	time.AfterFunc(3*time.Second, release)

	conn := waiterConn(t)
	start := time.Now()
	err = WaitForAdvisoryLock(context.Background(), conn, nil, AdvisoryWaitOptions{
		Key: advisoryTestKey, Label: "test", RetryInterval: 200 * time.Millisecond,
		Timeout: time.Second, BusyTimeout: 10 * time.Second, RecentActivityWindow: 500 * time.Millisecond,
	})
	require.NoError(t, err, "a busy holder must be waited out past the idle timeout")
	require.GreaterOrEqual(t, time.Since(start), 2*time.Second)
}

// TestWaitForAdvisoryLockGivesUpOnIdleHolder pins that a holder with no running
// work (a crashed pod's lingering session) still fails fast with stale-session
// guidance.
func TestWaitForAdvisoryLockGivesUpOnIdleHolder(t *testing.T) {
	holderPod := openAdvisoryTestDB(t, "bifrost-migrate:crashed-pod")
	holdAdvisoryKey(t, holderPod)
	time.Sleep(600 * time.Millisecond) // let the holder go quiet

	conn := waiterConn(t)
	start := time.Now()
	err := WaitForAdvisoryLock(context.Background(), conn, nil, AdvisoryWaitOptions{
		Key: advisoryTestKey, Label: "test", RetryInterval: 200 * time.Millisecond,
		Timeout: time.Second, BusyTimeout: 30 * time.Second, RecentActivityWindow: 500 * time.Millisecond,
	})
	require.Error(t, err)
	require.Less(t, time.Since(start), 5*time.Second, "an idle holder must not earn the busy budget")
	require.Contains(t, err.Error(), "lingering database session")
	require.Contains(t, err.Error(), "bifrost-migrate:crashed-pod")
}

// TestWaitForAdvisoryLockBusyErrorDescribesHolder pins that when even the busy
// budget runs out, the error says what the live holder is doing and warns
// against terminating it, instead of pointing at a stale session.
func TestWaitForAdvisoryLockBusyErrorDescribesHolder(t *testing.T) {
	holderPod := openAdvisoryTestDB(t, "bifrost-migrate:slow-pod")
	holdAdvisoryKey(t, holderPod)
	sleeper, err := holderPod.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sleeper.Close() })
	go func() { _, _ = sleeper.ExecContext(context.Background(), "SELECT pg_sleep(5)") }()
	time.Sleep(200 * time.Millisecond)

	conn := waiterConn(t)
	err = WaitForAdvisoryLock(context.Background(), conn, nil, AdvisoryWaitOptions{
		Key: advisoryTestKey, Label: "test", RetryInterval: 200 * time.Millisecond,
		Timeout: 500 * time.Millisecond, BusyTimeout: 1500 * time.Millisecond, RecentActivityWindow: 500 * time.Millisecond,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "pg_sleep")
	require.Contains(t, err.Error(), "Do not terminate it")
	require.False(t, strings.Contains(err.Error(), "lingering database session"))
}

// TestMigrationDSNTagsApplicationName pins the application_name that lets lock
// waiters on other pods find the holder pod's sibling sessions.
func TestMigrationDSNTagsApplicationName(t *testing.T) {
	dsn := MigrationDSN("host=h dbname=d")
	require.Contains(t, dsn, "default_query_exec_mode=simple_protocol")
	require.Contains(t, dsn, "application_name='"+migrationApplicationPrefix)
	require.LessOrEqual(t, len(migrationApplicationName()), 63)
}

// TestWaitForAdvisoryLockRetriesWhenHolderJustReleased pins that a waiter whose
// holder lookup finds nobody (the lock was released between its try-lock and the
// lookup) retries instead of giving up on an idle-holder budget it has already
// spent. The busy holder here releases after the idle budget has run out.
func TestWaitForAdvisoryLockRetriesWhenHolderJustReleased(t *testing.T) {
	holderPod := openAdvisoryTestDB(t, "bifrost-migrate:releasing-pod")
	release := holdAdvisoryKey(t, holderPod)
	sleeper, err := holderPod.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sleeper.Close() })
	go func() { _, _ = sleeper.ExecContext(context.Background(), "SELECT pg_sleep(2)") }()
	// Release right as the busy work ends, well past the 500ms idle budget.
	time.AfterFunc(2*time.Second, release)

	conn := waiterConn(t)
	err = WaitForAdvisoryLock(context.Background(), conn, nil, AdvisoryWaitOptions{
		Key: advisoryTestKey, Label: "test", RetryInterval: 20 * time.Millisecond,
		Timeout: 500 * time.Millisecond, BusyTimeout: 10 * time.Second, RecentActivityWindow: 50 * time.Millisecond,
	})
	require.NoError(t, err, "the waiter must take the lock once it is released, not give up")
}
