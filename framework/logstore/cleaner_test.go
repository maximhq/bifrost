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
	assert.Equal(t, 3, manager.assignments.calls, "two full batches and the short one that ends the drain")
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
