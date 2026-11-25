package logstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

// The same wrapper must reach the store's ReclaimFreeSpace, or a wrapped SQLite logs.db never shrinks.
func TestLogStoreWrapperForwardsReclaimFreeSpace(t *testing.T) {
	type wrapper struct{ LogStore }
	_, ok := any(wrapper{}).(FreeSpaceReclaimer)
	assert.True(t, ok, "a wrapper embedding LogStore must satisfy FreeSpaceReclaimer")
}

// reclaimingRetention is a fullRetention store that can give freed disk back. It
// counts the requests and records the expired rows still left at the last one.
type reclaimingRetention struct {
	fullRetention
	reclaims      int
	leftAtReclaim int64
}

func (m *reclaimingRetention) ReclaimFreeSpace(context.Context) (int64, error) {
	m.reclaims++
	m.leftAtReclaim = m.logs.expired + m.mcp.expired + m.assignments.expired + m.unmatched.expired
	return 1 << 20, nil
}

// The disk a pass frees is given back once, after every table is drained, so the
// reclaim covers all of them. A pass that deletes nothing still asks, which
// resumes a reclaim that shutdown cut short; a stopped pass leaves it to the next.
func TestLogsCleanerReclaimsFreeSpaceAfterDraining(t *testing.T) {
	manager := &reclaimingRetention{}
	manager.logs.expired = batchSize + 10
	manager.mcp.expired = 3
	manager.assignments.expired = 2
	manager.unmatched.expired = 7
	cleaner := NewLogsCleaner(manager, CleanerConfig{RetentionDays: 30}, testLogger{})

	cleaner.cleanupOldLogs(context.Background())
	assert.Equal(t, 1, manager.reclaims)
	assert.Zero(t, manager.leftAtReclaim, "the reclaim must follow the drain of every table")

	cleaner.cleanupOldLogs(context.Background())
	assert.Equal(t, 2, manager.reclaims, "a pass with nothing to delete still reclaims")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cleaner.cleanupOldLogs(ctx)
	assert.Equal(t, 2, manager.reclaims, "a stopped pass must not start a reclaim")
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

// A retention pass on SQLite must give the pages it frees back to the
// filesystem. Before #9738 they stayed on the database's freelist, so logs.db
// never shrank below the largest size it had reached.
func TestSQLiteRetentionPassShrinksDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "logs.db")
	store, err := newSqliteLogStore(ctx, &SQLiteConfig{Path: path}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(ctx) })

	payload := strings.Repeat("x", 4096)
	seed := func(prefix string, createdAt time.Time, n int) {
		entries := make([]*Log, n)
		for i := range entries {
			entries[i] = &Log{
				ID: fmt.Sprintf("%s-%04d", prefix, i), Timestamp: createdAt, CreatedAt: createdAt,
				Object: "chat.completion", Provider: "openai", Model: "gpt-4o", Status: "success",
				RawResponse: payload,
			}
		}
		require.NoError(t, store.BatchCreateIfNotExists(ctx, entries))
	}
	seed("old", time.Now().UTC().AddDate(0, 0, -40), 2000)
	seed("new", time.Now().UTC(), 50)
	// Measure only what production leaves on disk: no forced checkpoint, and the
	// WAL counts too, because freed pages can move into logs.db-wal.
	fileSizes := func() (db, wal int64) {
		t.Helper()
		info, err := os.Stat(path)
		require.NoError(t, err)
		if walInfo, err := os.Stat(path + "-wal"); err == nil {
			wal = walInfo.Size()
		}
		return info.Size(), wal
	}
	beforeDB, beforeWAL := fileSizes()

	NewLogsCleaner(store, CleanerConfig{RetentionDays: 30}, testLogger{}).cleanupOldLogs(ctx)

	var kept int64
	require.NoError(t, store.db.Model(&Log{}).Count(&kept).Error)
	require.Equal(t, int64(50), kept, "only the logs inside retention survive")
	assert.Zero(t, sqlitePragma(t, store, "freelist_count"), "the freed pages must leave the file, not wait on its freelist")
	afterDB, afterWAL := fileSizes()
	t.Logf("logs.db %d -> %d bytes, logs.db-wal %d -> %d bytes", beforeDB, afterDB, beforeWAL, afterWAL)
	assert.Less(t, afterDB, beforeDB/4, "logs.db must shrink back toward its live size")
	assert.Less(t, afterDB+afterWAL, beforeDB+beforeWAL, "the space must leave the files, not move into the WAL")
}

// Postgres runs retention hourly because only the lock holder does work; every other dialect stays daily.
func TestRDBRetentionIntervalByDialect(t *testing.T) {
	onDialect := func(d gorm.Dialector) *RDBLogStore {
		return &RDBLogStore{db: &gorm.DB{Config: &gorm.Config{Dialector: d}}}
	}
	assert.Equal(t, time.Hour, onDialect(postgres.Dialector{Config: &postgres.Config{}}).RetentionInterval())
	assert.Equal(t, 24*time.Hour, onDialect(&sqlite.Dialector{}).RetentionInterval())
}

// timeoutAbove is a delete that times out above limit rows and drains backlog otherwise, recording each requested size.
type timeoutAbove struct {
	limit   int
	backlog int64
	sizes   []int
}

func (d *timeoutAbove) deleteBatch(_ context.Context, _ time.Time, size int) (int64, error) {
	d.sizes = append(d.sizes, size)
	if size > d.limit {
		return 0, &pgconn.PgError{Code: postgresSQLStateStatementTimeout}
	}
	deleted := min(d.backlog, int64(size))
	d.backlog -= deleted
	return deleted, nil
}

// A batch of wide rows that cannot finish inside statement_timeout must not fail every pass on the same rows.
func TestDrainExpiredShrinksBatchOnStatementTimeout(t *testing.T) {
	d := &timeoutAbove{limit: 1000, backlog: 2500}
	cleaner := NewLogsCleaner(plainRetentionManager{}, CleanerConfig{RetentionDays: 7}, testLogger{})
	require.True(t, cleaner.drainExpired(context.Background(), "logs", time.Now(), d.deleteBatch))
	assert.Zero(t, d.backlog)
	assert.Equal(t, []int{5000, 2500, 1250, 625, 625, 625, 625, 625}, d.sizes)

	d = &timeoutAbove{limit: 1000, backlog: 700}
	require.True(t, cleaner.drainExpired(context.Background(), "logs", time.Now(), d.deleteBatch))
	assert.Equal(t, 625, d.sizes[0], "the next pass starts at the size that fit")
	assert.Equal(t, batchSize, cleaner.batchSizeFor("mcp tool logs"), "each table learns its own size")
}

func TestDrainExpiredGrowsBatchBackAfterSuccesses(t *testing.T) {
	d := &timeoutAbove{limit: batchSize / 2, backlog: 1 << 30}
	calls := 0
	deleteBatch := func(ctx context.Context, cutoff time.Time, size int) (int64, error) {
		calls++
		if calls == 1 {
			d.sizes = append(d.sizes, size)
			return 0, &pgconn.PgError{Code: postgresSQLStateStatementTimeout}
		}
		if calls > cleanupGrowAfter+2 {
			d.sizes = append(d.sizes, size)
			return 0, nil
		}
		d.limit = batchSize
		return d.deleteBatch(ctx, cutoff, size)
	}
	cleaner := NewLogsCleaner(plainRetentionManager{}, CleanerConfig{RetentionDays: 7}, testLogger{})
	require.True(t, cleaner.drainExpired(context.Background(), "logs", time.Now(), deleteBatch))
	assert.Equal(t, batchSize/2, d.sizes[1])
	assert.Equal(t, batchSize, d.sizes[len(d.sizes)-1], "a run of successes restores the full batch")
	assert.Equal(t, batchSize, cleaner.batchSizeFor("logs"))
}

func TestDrainExpiredFailsWhenOneRowTimesOut(t *testing.T) {
	d := &timeoutAbove{limit: 0}
	cleaner := NewLogsCleaner(plainRetentionManager{}, CleanerConfig{RetentionDays: 7}, testLogger{})
	require.False(t, cleaner.drainExpired(context.Background(), "logs", time.Now(), d.deleteBatch))
	assert.Equal(t, 1, d.sizes[len(d.sizes)-1])
	assert.Len(t, d.sizes, 13, "halving stops at one row")
}

// Other errors still end the table's drain at once.
func TestDrainExpiredDoesNotShrinkOnOtherErrors(t *testing.T) {
	calls := 0
	deleteBatch := func(context.Context, time.Time, int) (int64, error) {
		calls++
		return 0, &pgconn.PgError{Code: "40P01"}
	}
	cleaner := NewLogsCleaner(plainRetentionManager{}, CleanerConfig{RetentionDays: 7}, testLogger{})
	require.False(t, cleaner.drainExpired(context.Background(), "logs", time.Now(), deleteBatch))
	assert.Equal(t, 1, calls)
}

func TestRaiseRetentionStatementTimeout(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available")
	}
	for session, want := range map[string]string{"100ms": "10min", "2min": "10min", "0": "0", "1h": "1h"} {
		var got string
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			require.NoError(t, tx.Exec("SELECT set_config('statement_timeout', ?, true)", session).Error)
			require.NoError(t, raiseRetentionStatementTimeout(tx))
			return tx.Raw("SHOW statement_timeout").Scan(&got).Error
		}))
		assert.Equal(t, want, got, "session timeout %s", session)
	}
}

// A long id list goes out in several statements, and every row still goes.
func TestPostgresDeleteLogsChunksLargeIDList(t *testing.T) {
	store, db := setupRetentionPostgres(t, 3*deleteLogsChunkSize+7, 5)
	ids := make([]string, 0, 3*deleteLogsChunkSize+7)
	for i := 1; i <= 3*deleteLogsChunkSize+7; i++ {
		ids = append(ids, fmt.Sprintf("old-%d", i))
	}
	require.NoError(t, store.DeleteLogs(context.Background(), ids))
	assert.Zero(t, countLogs(t, db, "old-"))
	assert.Equal(t, int64(5), countLogs(t, db, "new-"))
}

// reclaimingRetentionManager reports a fixed reclaim result, so a test can see
// what the cleaner logs for it.
type reclaimingRetentionManager struct {
	plainRetentionManager
	reclaimed int64
	err       error
}

func (m reclaimingRetentionManager) ReclaimFreeSpace(context.Context) (int64, error) {
	return m.reclaimed, m.err
}

// warnRecorder keeps the Warn and Info messages that a cleaner logs.
type warnRecorder struct {
	testLogger
	warns []string
	infos []string
}

func (l *warnRecorder) Warn(format string, args ...any) {
	l.warns = append(l.warns, fmt.Sprintf(format, args...))
}

func (l *warnRecorder) Info(format string, args ...any) {
	l.infos = append(l.infos, fmt.Sprintf(format, args...))
}

// Shutdown or the pass deadline can stop a reclaim partway. The next pass
// resumes it, so the cleaner must not warn, but it still logs the bytes that
// the stopped reclaim released. Any other error is still a warning.
func TestReclaimFreeSpaceStopIsNotAWarning(t *testing.T) {
	for _, stop := range []error{context.Canceled, context.DeadlineExceeded, fmt.Errorf("pass stopped: %w", context.Canceled)} {
		logger := &warnRecorder{}
		manager := reclaimingRetentionManager{reclaimed: 4096, err: stop}
		NewLogsCleaner(manager, CleanerConfig{RetentionDays: 7}, logger).reclaimFreeSpace(context.Background())
		assert.Empty(t, logger.warns, stop.Error())
		assert.Len(t, logger.infos, 1, stop.Error())
	}
	logger := &warnRecorder{}
	manager := reclaimingRetentionManager{err: errors.New("disk I/O error")}
	NewLogsCleaner(manager, CleanerConfig{RetentionDays: 7}, logger).reclaimFreeSpace(context.Background())
	assert.Len(t, logger.warns, 1)
}
