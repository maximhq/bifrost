package logging

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
)

// recordingStore wraps a LogStore and records every log ID that reaches
// BatchCreateIfNotExists. Optional delay simulates a slow underlying store
// so we can force entries to back up between the writeQueue channel buffer
// and batchWriter's local batch slice at Cleanup time.
type recordingStore struct {
	logstore.LogStore
	mu       sync.Mutex
	received []string
	delay    time.Duration
}

func (s *recordingStore) BatchCreateIfNotExists(ctx context.Context, entries []*logstore.Log) error {
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	if err := s.LogStore.BatchCreateIfNotExists(ctx, entries); err != nil {
		return err
	}
	s.mu.Lock()
	for _, e := range entries {
		s.received = append(s.received, e.ID)
	}
	s.mu.Unlock()
	return nil
}

func (s *recordingStore) uniqueIDs() map[string]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]struct{}, len(s.received))
	for _, id := range s.received {
		out[id] = struct{}{}
	}
	return out
}

func makeTestLog(id string) *logstore.Log {
	now := time.Now()
	return &logstore.Log{
		ID:        id,
		Timestamp: now,
		CreatedAt: now,
		Provider:  "test-provider",
		Model:     "test-model",
		Status:    "success",
	}
}

// TestCleanupDrainsRecoveredBatchNoDrops covers the recovered-batch handoff:
// entries sit in batchWriter's in-memory batch (well under maxBatchSize, so
// no automatic flush has happened) when Cleanup runs. drainPending must
// recover and persist all of them.
func TestCleanupDrainsRecoveredBatchNoDrops(t *testing.T) {
	rec := &recordingStore{LogStore: newTestStore(t)}
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, rec, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	const N = 500 // well under maxBatchSize (1000); batchWriter will not auto-flush
	for i := 0; i < N; i++ {
		plugin.enqueueLogEntry(makeTestLog(fmt.Sprintf("recovered-%d", i)), nil)
	}

	// Let batchWriter dequeue everything into its local batch. 100ms is far
	// less than batchInterval (2s), so no automatic flush should have run.
	time.Sleep(100 * time.Millisecond)

	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	got := rec.uniqueIDs()
	if len(got) != N {
		t.Fatalf("expected %d unique entries persisted via recovered batch, got %d", N, len(got))
	}
	for i := 0; i < N; i++ {
		id := fmt.Sprintf("recovered-%d", i)
		if _, ok := got[id]; !ok {
			t.Fatalf("entry %s was dropped during Cleanup", id)
		}
	}
	if dropped := plugin.droppedRequests.Load(); dropped != 0 {
		t.Fatalf("expected 0 dropped requests, got %d", dropped)
	}
}

// TestCleanupDrainsCombinedQueueAndBatchNoDrops covers the mixed path: a
// large burst combined with a slow underlying store guarantees that some
// entries land in writeQueue's channel buffer while others sit in
// batchWriter's local batch when Cleanup arrives. Both paths must be drained.
func TestCleanupDrainsCombinedQueueAndBatchNoDrops(t *testing.T) {
	rec := &recordingStore{
		LogStore: newTestStore(t),
		delay:    25 * time.Millisecond, // slow store keeps batchWriter busy so the channel buffer fills
	}
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, rec, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	const N = 2500 // > maxBatchSize so batchWriter triggers at least two intermediate flushes
	for i := 0; i < N; i++ {
		plugin.enqueueLogEntry(makeTestLog(fmt.Sprintf("combined-%d", i)), nil)
	}

	// Call Cleanup while batchWriter is likely mid-processBatch (the 25ms
	// per-batch delay means the queue is still partially full). drainPending
	// must wait for the ownership handoff, then drain the remainder.
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	got := rec.uniqueIDs()
	if len(got) != N {
		t.Fatalf("expected %d unique entries persisted (mix of batchWriter + drainPending), got %d; dropped=%d",
			N, len(got), plugin.droppedRequests.Load())
	}
	for i := 0; i < N; i++ {
		id := fmt.Sprintf("combined-%d", i)
		if _, ok := got[id]; !ok {
			t.Fatalf("entry %s was dropped during Cleanup", id)
		}
	}
}

// TestCleanupRejectsNewSendsAfterClosed verifies the producer-side guard
// (p.closed.Load() in enqueueLogEntry) takes effect once Cleanup has run:
// subsequent enqueues are dropped at the source and do not panic.
func TestCleanupRejectsNewSendsAfterClosed(t *testing.T) {
	rec := &recordingStore{LogStore: newTestStore(t)}
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, rec, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	plugin.enqueueLogEntry(makeTestLog("pre-cleanup"), nil)

	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	// After Cleanup, the channel is closed and p.closed is true. Producers
	// must short-circuit via the closed check (no panic, no enqueue).
	dropsBefore := plugin.droppedRequests.Load()
	plugin.enqueueLogEntry(makeTestLog("post-cleanup"), nil)
	dropsAfter := plugin.droppedRequests.Load()

	// The closed-check path returns silently without incrementing
	// droppedRequests (matches existing semantics at writer.go:251-253).
	if dropsBefore != dropsAfter {
		t.Fatalf("post-cleanup enqueue should be a silent no-op; droppedRequests went from %d to %d",
			dropsBefore, dropsAfter)
	}

	got := rec.uniqueIDs()
	if _, ok := got["pre-cleanup"]; !ok {
		t.Fatalf("pre-cleanup entry was dropped during Cleanup")
	}
	if _, ok := got["post-cleanup"]; ok {
		t.Fatalf("post-cleanup entry should never have reached the store")
	}
}

// TestCleanupIsIdempotent verifies the cleanupOnce guard: a second Cleanup
// call must be a no-op rather than re-cancelling, re-closing channels, or
// panicking.
func TestCleanupIsIdempotent(t *testing.T) {
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, newTestStore(t), nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("first Cleanup() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("second Cleanup() error = %v", err)
	}
}

func TestWriterConfigDefaultsAndInitOverrides(t *testing.T) {
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, newTestStore(t), nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if plugin.writerConfig.MaxBatchSize != logstore.DefaultWriterMaxBatchSize {
		t.Fatalf("MaxBatchSize = %d, want %d", plugin.writerConfig.MaxBatchSize, logstore.DefaultWriterMaxBatchSize)
	}
	if cap(plugin.writeQueue) != logstore.DefaultWriterQueueCapacity {
		t.Fatalf("writeQueue cap = %d, want %d", cap(plugin.writeQueue), logstore.DefaultWriterQueueCapacity)
	}
	if cap(plugin.deferredUsageSem) != logstore.DefaultWriterDeferredUsageConcurrency {
		t.Fatalf("deferredUsageSem cap = %d, want %d", cap(plugin.deferredUsageSem), logstore.DefaultWriterDeferredUsageConcurrency)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	writer := &logstore.WriterConfig{
		MaxBatchSize:             7,
		BatchInterval:            "10ms",
		MaxBatchBytes:            1024,
		WriteQueueCapacity:       11,
		DeferredUsageConcurrency: 2,
	}
	plugin, err = Init(context.Background(), &Config{Writer: writer}, testLogger{}, newTestStore(t), nil, nil)
	if err != nil {
		t.Fatalf("Init() with writer error = %v", err)
	}
	if plugin.writerConfig != writer.WithDefaults() {
		t.Fatalf("writerConfig = %#v, want %#v", plugin.writerConfig, writer.WithDefaults())
	}
	if cap(plugin.writeQueue) != writer.WriteQueueCapacity {
		t.Fatalf("writeQueue cap = %d, want %d", cap(plugin.writeQueue), writer.WriteQueueCapacity)
	}
	if cap(plugin.deferredUsageSem) != writer.DeferredUsageConcurrency {
		t.Fatalf("deferredUsageSem cap = %d, want %d", cap(plugin.deferredUsageSem), writer.DeferredUsageConcurrency)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() with writer error = %v", err)
	}
}
