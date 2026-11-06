package logstore

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// gatedRetentionManager is a retention manager whose MaintenanceGate a test can
// toggle, counting every delete batch that reaches it.
type gatedRetentionManager struct {
	migrating atomic.Bool
	deletes   atomic.Int64
}

func (m *gatedRetentionManager) DeleteLogsBatch(context.Context, time.Time, int) (int64, error) {
	m.deletes.Add(1)
	return 0, nil
}

func (m *gatedRetentionManager) MigrationInProgress(context.Context) bool { return m.migrating.Load() }

func shrinkCleanupPoll(t *testing.T, d time.Duration) {
	t.Helper()
	prev := cleanupMigrationPollInterval
	cleanupMigrationPollInterval = d
	t.Cleanup(func() { cleanupMigrationPollInterval = prev })
}

// TestDrainExpiredWaitsForMigrationWindow pins that retention deletes hold off
// while another node holds the migration lock and resume once it is released.
func TestDrainExpiredWaitsForMigrationWindow(t *testing.T) {
	shrinkCleanupPoll(t, 5*time.Millisecond)
	manager := &gatedRetentionManager{}
	manager.migrating.Store(true)
	cleaner := NewLogsCleaner(manager, CleanerConfig{RetentionDays: 7}, testLogger{})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- cleaner.drainExpired(ctx, "logs", time.Now(), manager.DeleteLogsBatch) }()

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int64(0), manager.deletes.Load(), "no delete may be issued while the lock is held")
	manager.migrating.Store(false)

	select {
	case ok := <-done:
		assert.True(t, ok, "the pass completes normally once the lock clears")
	case <-time.After(2 * time.Second):
		t.Fatal("drainExpired did not resume after the migration lock was released")
	}
	assert.Equal(t, int64(1), manager.deletes.Load())
}

// TestDrainExpiredMigrationWaitBoundedByContext pins that the wait never
// outlives the pass deadline: a lock held for the whole pass ends it cleanly
// without a single delete.
func TestDrainExpiredMigrationWaitBoundedByContext(t *testing.T) {
	shrinkCleanupPoll(t, 5*time.Millisecond)
	manager := &gatedRetentionManager{}
	manager.migrating.Store(true)
	cleaner := NewLogsCleaner(manager, CleanerConfig{RetentionDays: 7}, testLogger{})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	start := time.Now()
	require.False(t, cleaner.drainExpired(ctx, "logs", time.Now(), manager.DeleteLogsBatch))
	assert.Less(t, time.Since(start), time.Second)
	assert.Equal(t, int64(0), manager.deletes.Load())
}

// TestDrainExpiredWithoutGateIsUnchanged pins the non-regression invariant: a
// manager that has no MaintenanceGate never pauses.
func TestDrainExpiredWithoutGateIsUnchanged(t *testing.T) {
	calls := 0
	deleteBatch := func(context.Context, time.Time, int) (int64, error) { calls++; return 0, nil }
	cleaner := NewLogsCleaner(plainRetentionManager{}, CleanerConfig{RetentionDays: 7}, testLogger{})
	require.True(t, cleaner.drainExpired(context.Background(), "logs", time.Now(), deleteBatch))
	assert.Equal(t, 1, calls)
}

type plainRetentionManager struct{}

func (plainRetentionManager) DeleteLogsBatch(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

// retentionTable is one table's expired backlog, drained the way a store
// drains it: at most batchSize rows a call.
type retentionTable struct {
	mu      sync.Mutex
	expired int64
	calls   int
	cutoff  time.Time
	// err, when set, fails every call. endless never runs out of rows, and
	// each call takes pause.
	err     error
	endless bool
	pause   time.Duration
}

func (r *retentionTable) deleteBatch(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.cutoff = cutoff
	if r.err != nil {
		return 0, r.err
	}
	if r.pause > 0 {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(r.pause):
		}
	}
	if r.endless {
		return int64(batchSize), nil
	}
	deleted := min(r.expired, int64(batchSize))
	r.expired -= deleted
	return deleted, nil
}

// logsOnlyRetention is a manager that predates every optional interface.
type logsOnlyRetention struct{ logs retentionTable }

func (m *logsOnlyRetention) DeleteLogsBatch(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	return m.logs.deleteBatch(ctx, cutoff, batchSize)
}

// fullRetention is a manager with every table the cleaner knows about.
type fullRetention struct {
	logsOnlyRetention
	mcp, assignments, unmatched retentionTable
}

func (m *fullRetention) DeleteMCPToolLogsBatch(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	return m.mcp.deleteBatch(ctx, cutoff, batchSize)
}

func (m *fullRetention) DeleteWarpTopicAssignmentsBatch(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	return m.assignments.deleteBatch(ctx, cutoff, batchSize)
}

func (m *fullRetention) DeleteWarpTopicUnmatchedBatch(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	return m.unmatched.deleteBatch(ctx, cutoff, batchSize)
}

func TestLogsCleanerExpiresWarpTopicRows(t *testing.T) {
	manager := &fullRetention{}
	manager.logs.expired = batchSize + 10
	manager.mcp.expired = 3
	manager.assignments.expired = 2*batchSize + 1
	manager.unmatched.expired = 7
	cleaner := NewLogsCleaner(manager, CleanerConfig{RetentionDays: 30}, testLogger{})

	cleaner.cleanupOldLogs(context.Background())

	assert.Zero(t, manager.logs.expired)
	assert.Zero(t, manager.mcp.expired)
	assert.Zero(t, manager.assignments.expired, "assignments past retention must be drained to the end")
	assert.Equal(t, 4, manager.assignments.calls, "two full batches, the short one, and the empty one that ends the drain")
	assert.Zero(t, manager.unmatched.expired)
	// Every table expires on the same instant: the one the logs expire on.
	want := time.Now().UTC().AddDate(0, 0, -30)
	for name, table := range map[string]*retentionTable{
		"logs": &manager.logs, "mcp": &manager.mcp, "assignments": &manager.assignments, "unmatched": &manager.unmatched,
	} {
		assert.WithinDuration(t, want, table.cutoff, time.Minute, name)
		assert.True(t, table.cutoff.Equal(manager.logs.cutoff), "%s must use the logs cutoff", name)
	}
}

// A manager that only knows about logs is still a manager.
func TestLogsCleanerWithoutWarpTopicRetention(t *testing.T) {
	manager := &logsOnlyRetention{}
	manager.logs.expired = 12
	cleaner := NewLogsCleaner(manager, CleanerConfig{RetentionDays: 30}, testLogger{})

	require.NotPanics(t, func() { cleaner.cleanupOldLogs(context.Background()) })
	assert.Zero(t, manager.logs.expired)
}

// Logs come first and can go wrong in two ways: every delete fails, or the
// backlog outlasts the pass. Neither may leave the topic tables uncleaned,
// because they would then keep rows about logs that retention has removed.
func TestLogsCleanerLogsTroubleDoesNotStarveWarpTopicRows(t *testing.T) {
	t.Run("logs delete fails", func(t *testing.T) {
		manager := &fullRetention{}
		manager.logs.err = errors.New("deadlock detected")
		manager.assignments.expired = 4
		manager.unmatched.expired = 2
		cleaner := NewLogsCleaner(manager, CleanerConfig{RetentionDays: 30}, testLogger{})

		cleaner.cleanupOldLogs(context.Background())

		assert.Zero(t, manager.assignments.expired)
		assert.Zero(t, manager.unmatched.expired)
	})
	t.Run("logs backlog outlasts the pass", func(t *testing.T) {
		manager := &fullRetention{}
		manager.logs.endless = true
		manager.logs.pause = 5 * time.Millisecond
		manager.assignments.expired = 4
		manager.unmatched.expired = 2
		cleaner := NewLogsCleaner(manager, CleanerConfig{RetentionDays: 30}, testLogger{})

		ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
		defer cancel()
		cleaner.cleanupOldLogs(ctx)

		require.NoError(t, ctx.Err(), "the pass must end with time to spare, not at its deadline")
		assert.Zero(t, manager.assignments.expired, "a share of the pass is reserved from logs")
		assert.Zero(t, manager.unmatched.expired)
	})
	// The same holds for every table after logs: an endless MCP backlog must not
	// eat the time the topic tables need, nor an assignment backlog the pool's.
	t.Run("MCP backlog outlasts the pass", func(t *testing.T) {
		manager := &fullRetention{}
		manager.mcp.endless = true
		manager.mcp.pause = 5 * time.Millisecond
		manager.assignments.expired = 4
		manager.unmatched.expired = 2
		cleaner := NewLogsCleaner(manager, CleanerConfig{RetentionDays: 30}, testLogger{})

		ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
		defer cancel()
		cleaner.cleanupOldLogs(ctx)

		assert.Zero(t, manager.assignments.expired, "MCP must leave time for the topic tables")
		assert.Zero(t, manager.unmatched.expired)
	})
	t.Run("assignment backlog outlasts the pass", func(t *testing.T) {
		manager := &fullRetention{}
		manager.assignments.endless = true
		manager.assignments.pause = 5 * time.Millisecond
		manager.unmatched.expired = 2
		cleaner := NewLogsCleaner(manager, CleanerConfig{RetentionDays: 30}, testLogger{})

		ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
		defer cancel()
		cleaner.cleanupOldLogs(ctx)

		assert.Zero(t, manager.unmatched.expired, "assignments must leave time for the unmatched pool")
	})
}

// scriptedRetention returns one scripted result per call, then empty batches.
type scriptedRetention struct {
	results  []scriptedBatch
	calls    int
	interval time.Duration
}

type scriptedBatch struct {
	deleted int64
	err     error
}

func (m *scriptedRetention) DeleteLogsBatch(context.Context, time.Time, int) (int64, error) {
	m.calls++
	if m.calls > len(m.results) {
		return 0, nil
	}
	r := m.results[m.calls-1]
	return r.deleted, r.err
}

func (m *scriptedRetention) RetentionInterval() time.Duration { return m.interval }

// A short batch is what SKIP LOCKED returns when another session holds some
// expired rows, so it must not end the pass the way it did before.
func TestDrainExpiredContinuesPastShortBatch(t *testing.T) {
	m := &scriptedRetention{results: []scriptedBatch{{deleted: batchSize}, {deleted: 40}, {deleted: batchSize}, {deleted: 7}}}
	cleaner := NewLogsCleaner(m, CleanerConfig{RetentionDays: 3}, testLogger{})
	require.True(t, cleaner.drainExpired(context.Background(), "logs", time.Now(), m.DeleteLogsBatch))
	assert.Equal(t, 5, m.calls, "only the empty fifth batch ends the drain")
}

// Another replica holding the table's lock ends this replica's drain of that
// table quietly, and the pass still moves on to the other tables.
func TestDrainExpiredYieldsToLockHolder(t *testing.T) {
	m := &scriptedRetention{results: []scriptedBatch{{deleted: batchSize}, {err: ErrRetentionBusy}, {deleted: batchSize}}}
	cleaner := NewLogsCleaner(m, CleanerConfig{RetentionDays: 3}, testLogger{})
	require.True(t, cleaner.drainExpired(context.Background(), "logs", time.Now(), m.DeleteLogsBatch), "yielding is not a failure")
	assert.Equal(t, 2, m.calls, "no batch may follow the yield")

	manager := &fullRetention{}
	manager.logs.err = ErrRetentionBusy
	manager.assignments.expired = 4
	NewLogsCleaner(manager, CleanerConfig{RetentionDays: 3}, testLogger{}).cleanupOldLogs(context.Background())
	assert.Zero(t, manager.assignments.expired, "a busy logs table must not hold up the tables after it")
}

// Retention keys share Postgres' advisory key space with every other lock, so a shared key makes
// replicas yield a table nobody is draining. 1000031 and 1000032 are held by enterprise.
func TestRetentionAdvisoryLockKeysStayInOwnBlock(t *testing.T) {
	seen := map[int64]string{}
	for table, key := range retentionAdvisoryLockKeys {
		assert.GreaterOrEqual(t, key, int64(1000040), table)
		assert.LessOrEqual(t, key, int64(1000049), table)
		assert.Empty(t, seen[key], "%s shares its key", table)
		seen[key] = table
	}
	assert.Empty(t, seen[retentionAdvisoryLockKeyOther], "the fallback key must be its own")
}

func TestNextRunDurationFollowsStoreInterval(t *testing.T) {
	hourly := NewLogsCleaner(&scriptedRetention{interval: time.Hour}, CleanerConfig{RetentionDays: 3}, testLogger{})
	daily := NewLogsCleaner(&logsOnlyRetention{}, CleanerConfig{RetentionDays: 3}, testLogger{})
	unset := NewLogsCleaner(&scriptedRetention{}, CleanerConfig{RetentionDays: 3}, testLogger{})
	for range 50 {
		d := hourly.nextRunDuration()
		assert.GreaterOrEqual(t, d, time.Hour+time.Hour/96)
		assert.LessOrEqual(t, d, time.Hour+time.Hour/48)

		d = daily.nextRunDuration()
		assert.GreaterOrEqual(t, d, 24*time.Hour+15*time.Minute, "the daily jitter is unchanged")
		assert.LessOrEqual(t, d, 24*time.Hour+30*time.Minute)

		assert.GreaterOrEqual(t, unset.nextRunDuration(), 24*time.Hour, "a zero interval falls back to daily")
	}
}

// Enterprise wraps the store in a struct embedding LogStore; the cleaner must still see the store's interval through it.
func TestLogStoreWrapperForwardsRetentionInterval(t *testing.T) {
	type wrapper struct{ LogStore }
	_, ok := any(wrapper{}).(RetentionScheduler)
	assert.True(t, ok, "a wrapper embedding LogStore must satisfy RetentionScheduler")
}

// setupRetentionPostgres migrates a fresh logs table holding expired old rows and fresh new ones.
func setupRetentionPostgres(t *testing.T, expired, fresh int) (*RDBLogStore, *gorm.DB) {
	t.Helper()
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available")
	}
	ctx := context.Background()
	dropAllManagedMatViews(db)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS mcp_tool_logs CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS async_jobs CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS webhook_deliveries CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS logs CASCADE").Error)
	require.NoError(t, db.Exec("CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("DELETE FROM migrations").Error)
	require.NoError(t, triggerMigrations(ctx, db, testLogger{}))
	t.Cleanup(func() { _ = db.Exec("TRUNCATE logs").Error })

	seed := `INSERT INTO logs (id, timestamp, created_at, provider, model, status, object_type)
		SELECT ? || g, NOW() - ?::interval + g * INTERVAL '1 millisecond', NOW() - ?::interval + g * INTERVAL '1 millisecond',
			'openai', 'gpt-4o', 'success', 'chat.completion'
		FROM generate_series(1, ?) AS g`
	require.NoError(t, db.Exec(seed, "old-", "40 days", "40 days", expired).Error)
	require.NoError(t, db.Exec(seed, "new-", "1 day", "1 day", fresh).Error)
	return &RDBLogStore{db: db, logger: testLogger{}}, db
}

func countLogs(t *testing.T, db *gorm.DB, prefix string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Raw("SELECT count(*) FROM logs WHERE id LIKE ?", prefix+"%").Scan(&n).Error)
	return n
}

// Replicas yielding to each other on the advisory lock must still drain every expired row between them.
func TestPostgresConcurrentCleanersDrainEverything(t *testing.T) {
	store, db := setupRetentionPostgres(t, 3*batchSize+123, 50)
	cutoff := time.Now().UTC().AddDate(0, 0, -30)

	const replicas = 8
	var wg sync.WaitGroup
	results := make([]bool, replicas)
	for i := range replicas {
		wg.Go(func() {
			cleaner := NewLogsCleaner(store, CleanerConfig{RetentionDays: 30}, testLogger{})
			results[i] = cleaner.drainExpired(context.Background(), "logs", cutoff, store.DeleteLogsBatch)
		})
	}
	wg.Wait()

	for i, ok := range results {
		assert.True(t, ok, "replica %d must finish or yield, not fail", i)
	}
	assert.Zero(t, countLogs(t, db, "old-"), "every expired row must be deleted")
	assert.Equal(t, int64(50), countLogs(t, db, "new-"), "rows inside retention must survive")
}

// A replica that finds the table's retention lock taken skips the batch at
// once instead of queueing behind the holder.
func TestPostgresRetentionBatchYieldsWhileLockHeld(t *testing.T) {
	store, db := setupRetentionPostgres(t, 20, 0)
	cutoff := time.Now().UTC().AddDate(0, 0, -30)

	holder := db.Begin()
	require.NoError(t, holder.Error)
	t.Cleanup(func() { holder.Rollback() }) // releases the lock if the test stops before the commit below
	var acquired bool
	require.NoError(t, holder.Raw("SELECT pg_try_advisory_xact_lock(?)", retentionAdvisoryLockKeys["logs"]).Row().Scan(&acquired))
	require.True(t, acquired)

	_, err := store.DeleteLogsBatch(context.Background(), cutoff, batchSize)
	assert.ErrorIs(t, err, ErrRetentionBusy)
	assert.Equal(t, int64(20), countLogs(t, db, "old-"))

	other, err := store.DeleteMCPToolLogsBatch(context.Background(), cutoff, batchSize)
	assert.NoError(t, err, "each table has its own lock")
	assert.Zero(t, other)

	require.NoError(t, holder.Commit().Error)
	deleted, err := store.DeleteLogsBatch(context.Background(), cutoff, batchSize)
	require.NoError(t, err)
	assert.Equal(t, int64(20), deleted, "the lock goes with the holder's transaction")
}

// One Postgres batch deletes at most batchSize rows. drainExpired reads a larger count as ClickHouse's whole-range
// delete and stops, so a statement that overshoots the limit leaves expired rows behind.
func TestPostgresRetentionBatchNeverExceedsBatchSize(t *testing.T) {
	store, db := setupRetentionPostgres(t, 3*batchSize+123, 0)
	cutoff := time.Now().UTC().AddDate(0, 0, -30)

	for _, want := range []int64{batchSize, batchSize, batchSize, 123, 0} {
		deleted, err := store.DeleteLogsBatch(context.Background(), cutoff, batchSize)
		require.NoError(t, err)
		require.Equal(t, want, deleted, "each batch must stop at the limit")
	}
	assert.Zero(t, countLogs(t, db, "old-"))
}

// An operator's own pruner holding row locks must not stall the cleaner: the
// batch skips those rows and deletes the rest.
func TestPostgresRetentionBatchSkipsLockedRows(t *testing.T) {
	store, db := setupRetentionPostgres(t, 30, 0)
	cutoff := time.Now().UTC().AddDate(0, 0, -30)

	pruner := db.Begin()
	require.NoError(t, pruner.Error)
	defer pruner.Rollback()
	require.NoError(t, pruner.Exec("SELECT id FROM logs WHERE id LIKE 'old-%' ORDER BY created_at LIMIT 10 FOR UPDATE").Error)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deleted, err := store.DeleteLogsBatch(ctx, cutoff, batchSize)
	require.NoError(t, err, "the batch must not wait on the pruner's row locks")
	assert.Equal(t, int64(20), deleted)

	require.NoError(t, pruner.Rollback().Error)
	deleted, err = store.DeleteLogsBatch(context.Background(), cutoff, batchSize)
	require.NoError(t, err)
	assert.Equal(t, int64(10), deleted, "the skipped rows go on a later batch")
}

// Postgres runs retention hourly because only the lock holder does work; every other dialect stays daily.
func TestRDBRetentionIntervalByDialect(t *testing.T) {
	onDialect := func(d gorm.Dialector) *RDBLogStore {
		return &RDBLogStore{db: &gorm.DB{Config: &gorm.Config{Dialector: d}}}
	}
	assert.Equal(t, time.Hour, onDialect(postgres.Dialector{Config: &postgres.Config{}}).RetentionInterval())
	assert.Equal(t, 24*time.Hour, onDialect(&sqlite.Dialector{}).RetentionInterval())
}
