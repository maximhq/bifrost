package logstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
}
