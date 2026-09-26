package logstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// batchTable is a table of expired rows that deletes up to the asked batch per call and records
// every call's cutoff and batch size.
type batchTable struct {
	remaining int64
	cutoffs   []time.Time
	sizes     []int
	err       error
}

// deleteBatch removes up to size rows, as a SQL store's batched delete does.
func (b *batchTable) deleteBatch(_ context.Context, cutoff time.Time, size int) (int64, error) {
	b.cutoffs = append(b.cutoffs, cutoff)
	b.sizes = append(b.sizes, size)
	if b.err != nil {
		return 0, b.err
	}
	n := min(int64(size), b.remaining)
	b.remaining -= n
	return n, nil
}

// batchLogs adapts a batchTable to LogRetentionManager for the logs themselves.
type batchLogs struct{ *batchTable }

// DeleteLogsBatch deletes one batch of logs.
func (b batchLogs) DeleteLogsBatch(ctx context.Context, cutoff time.Time, size int) (int64, error) {
	return b.deleteBatch(ctx, cutoff, size)
}

// TestLogsCleaner_RetentionTargets checks registered targets are drained after the logs with the
// same cutoff, each in its own batch size, and that a failure stops the run.
func TestLogsCleaner_RetentionTargets(t *testing.T) {
	logs := &batchTable{remaining: 250}
	frames := &batchTable{remaining: 2500}
	events := &batchTable{remaining: 30}
	cleaner := NewLogsCleaner(batchLogs{logs}, CleanerConfig{RetentionDays: 7}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	cleaner.AddRetentionTarget(RetentionTarget{Name: "frames", BatchSize: 1000, DeleteBatch: frames.deleteBatch})
	cleaner.AddRetentionTarget(RetentionTarget{Name: "events", DeleteBatch: events.deleteBatch})
	cleaner.AddRetentionTarget(RetentionTarget{Name: "ignored"})

	cleaner.cleanupOldLogs(context.Background())

	require.Zero(t, logs.remaining)
	require.Zero(t, frames.remaining)
	require.Zero(t, events.remaining)
	require.Equal(t, []int{100, 100, 100}, logs.sizes, "the logs keep the cleaner's batch size")
	require.Equal(t, []int{1000, 1000, 1000}, frames.sizes, "a target's own batch size is used")
	require.Equal(t, []int{100}, events.sizes, "a target without one uses the cleaner's")
	cutoff := logs.cutoffs[0]
	require.WithinDuration(t, time.Now().UTC().AddDate(0, 0, -7), cutoff, time.Minute)
	for _, c := range append(frames.cutoffs, events.cutoffs...) {
		require.Equal(t, cutoff, c, "every table uses the logs cutoff")
	}

	// A failing target stops the run before the targets after it.
	failing := &batchTable{remaining: 10, err: errors.New("boom")}
	after := &batchTable{remaining: 10}
	stop := NewLogsCleaner(batchLogs{&batchTable{}}, CleanerConfig{RetentionDays: 7}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	stop.AddRetentionTarget(RetentionTarget{Name: "failing", DeleteBatch: failing.deleteBatch})
	stop.AddRetentionTarget(RetentionTarget{Name: "after", DeleteBatch: after.deleteBatch})
	stop.cleanupOldLogs(context.Background())
	require.Empty(t, after.cutoffs)

	var nilCleaner *LogsCleaner
	nilCleaner.AddRetentionTarget(RetentionTarget{Name: "x", DeleteBatch: after.deleteBatch})
}

// registeringLogs runs register before every logs batch, as a caller racing a run would.
type registeringLogs struct {
	*batchTable
	register func()
}

// DeleteLogsBatch registers, then deletes one batch of logs.
func (r registeringLogs) DeleteLogsBatch(ctx context.Context, cutoff time.Time, size int) (int64, error) {
	r.register()
	return r.deleteBatch(ctx, cutoff, size)
}

// TestLogsCleaner_TargetRegisteredDuringARunWaitsForTheNext checks the AddRetentionTarget contract:
// a target registered while the logs are draining is left to the next run, not joined mid-run.
func TestLogsCleaner_TargetRegisteredDuringARunWaitsForTheNext(t *testing.T) {
	late := &batchTable{remaining: 10}
	var cleaner *LogsCleaner
	var once sync.Once
	logs := registeringLogs{batchTable: &batchTable{remaining: 150}, register: func() {
		once.Do(func() { cleaner.AddRetentionTarget(RetentionTarget{Name: "late", DeleteBatch: late.deleteBatch}) })
	}}
	cleaner = NewLogsCleaner(logs, CleanerConfig{RetentionDays: 7}, bifrost.NewDefaultLogger(schemas.LogLevelError))

	cleaner.cleanupOldLogs(context.Background())
	require.Empty(t, late.cutoffs, "a target registered mid-run waits for the next run")

	cleaner.cleanupOldLogs(context.Background())
	require.Zero(t, late.remaining, "the next run drains it")
}
