package logging

import (
	"strings"
	"testing"
	"time"
)

// newTestPlugin returns a minimal LoggerPlugin suitable for sweeper unit tests.
// It wires up a real SQLite store (via newTestStore) and a buffered write queue
// so that enqueueLogEntry works without a running batchWriter goroutine.
func newTestPlugin(t *testing.T) *LoggerPlugin {
	t.Helper()
	store := newTestStore(t)
	return &LoggerPlugin{
		store:      store,
		logger:     testLogger{},
		writeQueue: make(chan *writeQueueEntry, writeQueueCapacity),
	}
}

// drainQueue reads all entries currently buffered in the plugin's writeQueue
// and returns them as a slice.
func drainQueue(p *LoggerPlugin) []*writeQueueEntry {
	var out []*writeQueueEntry
	for {
		select {
		case e := <-p.writeQueue:
			out = append(out, e)
		default:
			return out
		}
	}
}

// TestSweeperFlushesOrphanedPendingLogsEntry verifies that cleanupStalePendingLogs
// enqueues a synthesized error row before deleting a stale pendingLogsEntries
// entry whose TTL has expired.
func TestSweeperFlushesOrphanedPendingLogsEntry(t *testing.T) {
	plugin := newTestPlugin(t)

	requestID := "req-orphan-1"
	staleTime := time.Now().Add(-(pendingLogTTL + time.Minute))

	pending := &PendingLogData{
		RequestID: requestID,
		Timestamp: staleTime,
		CreatedAt: staleTime,
		InitialData: &InitialLogData{
			Object:   "chat_completion",
			Provider: "openai",
			Model:    "gpt-4o",
		},
	}
	plugin.pendingLogsEntries.Store(requestID, pending)

	plugin.cleanupStalePendingLogs()

	// The entry must be removed from the in-memory map.
	if _, ok := plugin.pendingLogsEntries.Load(requestID); ok {
		t.Fatal("expected pendingLogsEntries to be cleared after TTL eviction")
	}

	// Exactly one write must have been enqueued.
	entries := drainQueue(plugin)
	if len(entries) != 1 {
		t.Fatalf("expected 1 write queue entry, got %d", len(entries))
	}

	log := entries[0].log
	if log == nil {
		t.Fatal("expected non-nil log entry in write queue")
	}
	if log.ID != requestID {
		t.Fatalf("log.ID = %q, want %q", log.ID, requestID)
	}
	if log.Status != "error" {
		t.Fatalf("log.Status = %q, want %q", log.Status, "error")
	}
	if !strings.Contains(log.ErrorDetails, "abandoned") {
		t.Fatalf("log.ErrorDetails = %q, expected it to mention abandoned", log.ErrorDetails)
	}
	if log.Provider != "openai" {
		t.Fatalf("log.Provider = %q, want %q", log.Provider, "openai")
	}
	if log.Model != "gpt-4o" {
		t.Fatalf("log.Model = %q, want %q", log.Model, "gpt-4o")
	}
}

// TestSweeperDoesNotFlushFreshPendingLogsEntry verifies that a pendingLogsEntries
// entry that has not yet exceeded its TTL is left untouched.
func TestSweeperDoesNotFlushFreshPendingLogsEntry(t *testing.T) {
	plugin := newTestPlugin(t)

	requestID := "req-fresh-1"
	pending := &PendingLogData{
		RequestID: requestID,
		Timestamp: time.Now(),
		CreatedAt: time.Now(),
		InitialData: &InitialLogData{
			Object:   "chat_completion",
			Provider: "openai",
			Model:    "gpt-4o",
		},
	}
	plugin.pendingLogsEntries.Store(requestID, pending)

	plugin.cleanupStalePendingLogs()

	// The entry must still be present.
	if _, ok := plugin.pendingLogsEntries.Load(requestID); !ok {
		t.Fatal("expected fresh pendingLogsEntries entry to survive TTL sweep")
	}

	// No writes should have been enqueued.
	entries := drainQueue(plugin)
	if len(entries) != 0 {
		t.Fatalf("expected no write queue entries for fresh entry, got %d", len(entries))
	}
}

// TestSweeperFlushesMultipleOrphanedEntries verifies that all stale entries
// are individually flushed when the sweeper runs.
func TestSweeperFlushesMultipleOrphanedEntries(t *testing.T) {
	plugin := newTestPlugin(t)

	staleTime := time.Now().Add(-(pendingLogTTL + time.Minute))
	ids := []string{"req-multi-1", "req-multi-2", "req-multi-3"}
	for _, id := range ids {
		plugin.pendingLogsEntries.Store(id, &PendingLogData{
			RequestID: id,
			Timestamp: staleTime,
			CreatedAt: staleTime,
			InitialData: &InitialLogData{
				Object:   "chat_completion",
				Provider: "openai",
				Model:    "gpt-4o",
			},
		})
	}

	plugin.cleanupStalePendingLogs()

	// All entries must be evicted.
	for _, id := range ids {
		if _, ok := plugin.pendingLogsEntries.Load(id); ok {
			t.Fatalf("expected entry %q to be evicted", id)
		}
	}

	// One write per entry.
	entries := drainQueue(plugin)
	if len(entries) != len(ids) {
		t.Fatalf("expected %d write queue entries, got %d", len(ids), len(entries))
	}
	for _, e := range entries {
		if e.log.Status != "error" {
			t.Fatalf("entry %q: status = %q, want error", e.log.ID, e.log.Status)
		}
	}
}

// TestSweeperStalePendingLogsToInjectEvictedWithoutPanic verifies that the
// pendingLogsToInject cleanup still runs without error when stale entries are
// present. The inject entries are not DB-flushed (they lack a requestID
// anchor) so this test focuses on panic-free eviction only.
func TestSweeperStalePendingLogsToInjectEvictedWithoutPanic(t *testing.T) {
	plugin := newTestPlugin(t)

	staleTime := time.Now().Add(-(pendingLogTTL + time.Minute))
	plugin.pendingLogsToInject.Store("trace-stale", &pendingInjectEntries{
		createdAt: staleTime,
	})
	plugin.pendingLogsToInject.Store("trace-fresh", &pendingInjectEntries{
		createdAt: time.Now(),
	})

	plugin.cleanupStalePendingLogs()

	if _, ok := plugin.pendingLogsToInject.Load("trace-stale"); ok {
		t.Fatal("expected stale pendingLogsToInject entry to be evicted")
	}
	if _, ok := plugin.pendingLogsToInject.Load("trace-fresh"); !ok {
		t.Fatal("expected fresh pendingLogsToInject entry to survive TTL sweep")
	}
}
