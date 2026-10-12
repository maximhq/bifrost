package logstore

import (
	"context"
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
