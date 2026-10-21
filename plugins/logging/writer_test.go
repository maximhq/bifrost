package logging

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/postgresconn"
)

// failingBatchStore wraps a real store and fails BatchCreateIfNotExists with a
// caller-chosen error, recording the row count of every attempt so a test can
// tell a whole-batch retry from a per-row fan-out.
type failingBatchStore struct {
	logstore.LogStore
	mu             sync.Mutex
	callSizes      []int
	agentCallSizes []int
	// ctxErrs records ctx.Err() as seen by each call, so a test can prove the
	// writer hands its recovery context (not the plugin's parent) to the store.
	ctxErrs      []error
	agentCtxErrs []error
	// fail decides whether a call of the given size fails and with what.
	fail      func(size int) error
	agentFail func(size int) error
}

func (s *failingBatchStore) BatchCreateIfNotExists(ctx context.Context, entries []*logstore.Log) error {
	s.mu.Lock()
	s.callSizes = append(s.callSizes, len(entries))
	s.ctxErrs = append(s.ctxErrs, ctx.Err())
	s.mu.Unlock()
	if err := s.fail(len(entries)); err != nil {
		return err
	}
	return s.LogStore.BatchCreateIfNotExists(ctx, entries)
}

func (s *failingBatchStore) BatchCreateAgentLogsIfNotExists(ctx context.Context, entries []*logstore.AgentLog) ([]string, error) {
	s.mu.Lock()
	s.agentCallSizes = append(s.agentCallSizes, len(entries))
	s.agentCtxErrs = append(s.agentCtxErrs, ctx.Err())
	s.mu.Unlock()
	if s.agentFail != nil {
		if err := s.agentFail(len(entries)); err != nil {
			return nil, err
		}
	}
	return s.LogStore.BatchCreateAgentLogsIfNotExists(ctx, entries)
}

func (s *failingBatchStore) sizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.callSizes...)
}

func makeTestAgentLog(id string) *logstore.AgentLog {
	now := time.Now().UTC()
	return &logstore.AgentLog{
		ID:         id,
		Timestamp:  now,
		CreatedAt:  now,
		RecordKind: "request",
		Status:     "success",
		AgentName:  "fixture",
		RequestID:  id,
	}
}

// TestProcessBatchConnectionErrorDoesNotFanOutPerRow pins the writer's error
// handling from issue #7843: when the whole-batch insert fails for a reason
// that is not specific to any row (here the database is unreachable), retrying
// each row as its own INSERT cannot succeed either. It only multiplies the
// failed statements by the batch size, which is what let the write queue back
// up and the heap grow. Such a batch must be retried whole a bounded number of
// times and then counted as dropped, never fanned out row by row.
func TestProcessBatchConnectionErrorDoesNotFanOutPerRow(t *testing.T) {
	connRefused := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	store := &failingBatchStore{
		LogStore: newTestStore(t),
		fail:     func(int) error { return fmt.Errorf("write logs: %w", connRefused) },
	}
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })

	const N = 50
	batch := make([]*writeQueueEntry, 0, N)
	for i := 0; i < N; i++ {
		batch = append(batch, &writeQueueEntry{log: makeTestLog(fmt.Sprintf("conn-%d", i))})
	}
	plugin.processBatch(context.Background(), batch)

	sizes := store.sizes()
	for _, size := range sizes {
		if size != N {
			t.Fatalf("connection-level batch failure fanned out to a %d-row insert; attempts were %v", size, sizes)
		}
	}
	if len(sizes) > 4 {
		t.Fatalf("expected a bounded number of whole-batch attempts, got %d", len(sizes))
	}
	if dropped := plugin.droppedRequests.Load(); dropped != N {
		t.Fatalf("expected %d dropped requests after retries were exhausted, got %d", N, dropped)
	}
}

// TestProcessBatchSQLiteBusyRetriesWholeBatch pins the writer's handling of a
// contended SQLite writer lock. SQLITE_BUSY (or SQLITE_LOCKED) says nothing
// about the rows in the batch, so the batch must be retried whole with the
// backoff and land once the lock clears. Fanning out into per-row inserts
// instead turns one lock wait into one per row (each up to busy_timeout), which
// is what put the SQLite writer minutes behind under a parallel load.
func TestProcessBatchSQLiteBusyRetriesWholeBatch(t *testing.T) {
	for name, busyErr := range map[string]error{
		"busy":   sqlite3.Error{Code: sqlite3.ErrBusy},
		"locked": sqlite3.Error{Code: sqlite3.ErrLocked},
	} {
		t.Run(name, func(t *testing.T) {
			var attempts atomic.Int32
			store := &failingBatchStore{
				LogStore: newTestStore(t),
				fail: func(int) error {
					if attempts.Add(1) == 1 {
						return fmt.Errorf("write logs: %w", busyErr)
					}
					return nil
				},
			}
			plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
			if err != nil {
				t.Fatalf("Init() error = %v", err)
			}
			t.Cleanup(func() { _ = plugin.Cleanup() })

			const N = 20
			batch := make([]*writeQueueEntry, 0, N)
			for i := 0; i < N; i++ {
				batch = append(batch, &writeQueueEntry{log: makeTestLog(fmt.Sprintf("busy-%s-%d", name, i))})
			}
			if unwritten := plugin.processBatch(context.Background(), batch); len(unwritten) != 0 {
				t.Fatalf("expected every entry written, %d handed back", len(unwritten))
			}

			sizes := store.sizes()
			for _, size := range sizes {
				if size != N {
					t.Fatalf("SQLite %s fanned out to a %d-row insert; attempts were %v", name, size, sizes)
				}
			}
			if len(sizes) != 2 {
				t.Fatalf("expected one failed whole-batch attempt and one successful retry, got attempts %v", sizes)
			}
			if dropped := plugin.droppedRequests.Load(); dropped != 0 {
				t.Fatalf("expected 0 dropped requests, got %d", dropped)
			}
			for i := 0; i < N; i++ {
				if _, err := store.FindByID(context.Background(), fmt.Sprintf("busy-%s-%d", name, i)); err != nil {
					t.Fatalf("row %d missing after retry: %v", i, err)
				}
			}
		})
	}
}

// TestProcessBatchUnknownErrorStillFallsBackPerRow guards the existing
// behaviour for errors the writer cannot classify: a bad row in the batch is
// isolated by retrying each row alone, so one poisoned entry never drops the
// other N-1.
func TestProcessBatchUnknownErrorStillFallsBackPerRow(t *testing.T) {
	store := &failingBatchStore{
		LogStore: newTestStore(t),
		fail: func(size int) error {
			if size > 1 {
				return errors.New("one row in this batch is unacceptable")
			}
			return nil
		},
	}
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })

	const N = 20
	batch := make([]*writeQueueEntry, 0, N)
	for i := 0; i < N; i++ {
		batch = append(batch, &writeQueueEntry{log: makeTestLog(fmt.Sprintf("row-%d", i))})
	}
	plugin.processBatch(context.Background(), batch)

	sizes := store.sizes()
	single := 0
	for _, size := range sizes {
		if size == 1 {
			single++
		}
	}
	if single != N {
		t.Fatalf("expected %d per-row fallback inserts, got %d (attempts %v)", N, single, sizes)
	}
	if dropped := plugin.droppedRequests.Load(); dropped != 0 {
		t.Fatalf("expected 0 dropped requests, got %d", dropped)
	}
}

// sqlStateErr is a driver-shaped error carrying a SQLSTATE, matching the
// SQLState() method on *pgconn.PgError without importing pgx here.
type sqlStateErr string

func (e sqlStateErr) Error() string    { return "SQLSTATE " + string(e) }
func (e sqlStateErr) SQLState() string { return string(e) }

// TestProcessBatchStatementTimeoutSplitsBatch pins the recovery for
// statement_timeout (SQLSTATE 57014): a batch too large for the budget is
// halved until the pieces fit, so every row lands and nothing is dropped, and
// the same oversized statement is not retried verbatim.
func TestProcessBatchStatementTimeoutSplitsBatch(t *testing.T) {
	const fits = 8
	store := &failingBatchStore{
		LogStore: newTestStore(t),
		fail: func(size int) error {
			if size > fits {
				return fmt.Errorf("insert: %w", sqlStateErr("57014"))
			}
			return nil
		},
	}
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })

	const N = 50
	batch := make([]*writeQueueEntry, 0, N)
	for i := 0; i < N; i++ {
		batch = append(batch, &writeQueueEntry{log: makeTestLog(fmt.Sprintf("split-%d", i))})
	}
	plugin.processBatch(context.Background(), batch)

	sizes := store.sizes()
	oversized := 0
	for _, size := range sizes {
		if size == 1 {
			t.Fatalf("statement timeout must split, never fan out per row; attempts were %v", sizes)
		}
		if size > fits {
			oversized++
		}
	}
	// 50, 25, 25, 13, 12, 13, 12 time out (7 oversized statements, each sent once).
	if oversized != 7 {
		t.Fatalf("expected each oversized chunk to be sent once (7), got %d; attempts were %v", oversized, sizes)
	}
	if dropped := plugin.droppedRequests.Load(); dropped != 0 {
		t.Fatalf("expected 0 dropped requests, got %d", dropped)
	}
	res, err := store.LogStore.SearchLogs(context.Background(), logstore.SearchFilters{}, logstore.PaginationOptions{Limit: N})
	if err != nil {
		t.Fatalf("SearchLogs() error = %v", err)
	}
	if res.Pagination.TotalCount != N {
		t.Fatalf("expected %d stored logs, got %d", N, res.Pagination.TotalCount)
	}
}

// TestProcessBatchStatementTimeoutSingleRowDropped pins the floor of the split:
// a row that times out on its own cannot be made smaller, so it is dropped and
// counted, with no whole-batch retries along the way.
func TestProcessBatchStatementTimeoutSingleRowDropped(t *testing.T) {
	store := &failingBatchStore{
		LogStore: newTestStore(t),
		fail:     func(int) error { return sqlStateErr("57014") },
	}
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })

	const N = 4
	batch := make([]*writeQueueEntry, 0, N)
	for i := 0; i < N; i++ {
		batch = append(batch, &writeQueueEntry{log: makeTestLog(fmt.Sprintf("floor-%d", i))})
	}
	plugin.processBatch(context.Background(), batch)

	// 4 -> 2, 2 -> 1, 1, 1, 1: seven statements, each sent exactly once.
	if sizes := store.sizes(); len(sizes) != 7 {
		t.Fatalf("expected 7 attempts (no verbatim retries of a timed-out statement), got %d: %v", len(sizes), sizes)
	}
	if dropped := plugin.droppedRequests.Load(); dropped != N {
		t.Fatalf("expected %d dropped requests, got %d", N, dropped)
	}
}

// TestProcessBatchCancelledContextHandsBatchBack pins the shutdown path: the
// store call runs under the context processBatch was given (batchWriter passes
// batchCtx, so Cleanup's cancel reaches a blocked write), a transient failure
// under a done context is not retried, and the entries are returned for the
// drain instead of being dropped or having their callbacks run.
func TestProcessBatchCancelledContextHandsBatchBack(t *testing.T) {
	connRefused := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	store := &failingBatchStore{
		LogStore: newTestStore(t),
		fail:     func(int) error { return connRefused },
	}
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	const N = 10
	var callbacks atomic.Int32
	batch := make([]*writeQueueEntry, 0, N)
	for i := 0; i < N; i++ {
		batch = append(batch, &writeQueueEntry{
			log:      makeTestLog(fmt.Sprintf("cancel-%d", i)),
			callback: func(*logstore.Log) { callbacks.Add(1) },
		})
	}
	left := plugin.processBatch(ctx, batch)

	if sizes := store.sizes(); len(sizes) != 1 {
		t.Fatalf("expected a single attempt once the batch context is done, got %v", sizes)
	}
	store.mu.Lock()
	sawCancelled := len(store.ctxErrs) == 1 && store.ctxErrs[0] != nil
	store.mu.Unlock()
	if !sawCancelled {
		t.Fatalf("store must receive the writer's recovery context; it saw an active one")
	}
	if len(left) != N {
		t.Fatalf("expected all %d entries handed back for the drain, got %d", N, len(left))
	}
	if dropped := plugin.droppedRequests.Load(); dropped != 0 {
		t.Fatalf("interrupted entries must not count as dropped, got %d", dropped)
	}
	if callbacks.Load() != 0 {
		t.Fatalf("callbacks must not run for entries that were not persisted")
	}
}

func TestProcessBatchCancelledAgentContextHandsBatchBack(t *testing.T) {
	store := &failingBatchStore{
		LogStore:  newTestStore(t),
		agentFail: func(int) error { return context.Canceled },
	}
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	const N = 10
	batch := make([]*writeQueueEntry, 0, N)
	for i := 0; i < N; i++ {
		batch = append(batch, &writeQueueEntry{agentLog: makeTestAgentLog(fmt.Sprintf("agent-cancel-%d", i))})
	}
	left := plugin.processBatch(ctx, batch)

	store.mu.Lock()
	callSizes := append([]int(nil), store.agentCallSizes...)
	ctxErrs := append([]error(nil), store.agentCtxErrs...)
	store.mu.Unlock()
	if len(callSizes) != 1 {
		t.Fatalf("expected a single Agent write attempt once the batch context is done, got %v", callSizes)
	}
	if len(ctxErrs) != 1 || ctxErrs[0] == nil {
		t.Fatalf("Agent store must receive the cancelled processBatch context")
	}
	if len(left) != N {
		t.Fatalf("expected all %d Agent entries handed back for the drain, got %d", N, len(left))
	}
	for i, entry := range left {
		if entry != batch[i] {
			t.Fatalf("handed-back Agent entry %d does not match the original queue entry", i)
		}
	}
	if dropped := plugin.droppedRequests.Load(); dropped != 0 {
		t.Fatalf("interrupted Agent entries must not count as dropped, got %d", dropped)
	}
}

// blockingOnceStore blocks its first batch write until the caller's context
// ends, then returns that context's error. Every later call writes normally.
// It stands in for a database that has stopped answering right as Cleanup
// begins, so the test can prove the flush is interrupted and its entries are
// carried into the drain rather than lost.
type blockingOnceStore struct {
	logstore.LogStore
	blocked atomic.Bool
}

func (s *blockingOnceStore) BatchCreateIfNotExists(ctx context.Context, entries []*logstore.Log) error {
	if s.blocked.CompareAndSwap(false, true) {
		<-ctx.Done()
		return fmt.Errorf("write logs: %w", ctx.Err())
	}
	return s.LogStore.BatchCreateIfNotExists(ctx, entries)
}

type blockingOnceAgentStore struct {
	logstore.LogStore
	started chan struct{}
	blocked atomic.Bool
}

func (s *blockingOnceAgentStore) BatchCreateAgentLogsIfNotExists(ctx context.Context, entries []*logstore.AgentLog) ([]string, error) {
	if s.blocked.CompareAndSwap(false, true) {
		close(s.started)
		<-ctx.Done()
		return nil, fmt.Errorf("write Agent logs: %w", ctx.Err())
	}
	return s.LogStore.BatchCreateAgentLogsIfNotExists(ctx, entries)
}

func TestCleanupInterruptsBlockedAgentFlushAndDrainsIt(t *testing.T) {
	inner := newTestStore(t)
	store := &blockingOnceAgentStore{LogStore: inner, started: make(chan struct{})}
	const N = 5
	plugin, err := Init(context.Background(), &Config{Writer: &logstore.WriterConfig{
		MaxBatchSize:  N,
		BatchInterval: "10ms",
	}}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	for i := 0; i < N; i++ {
		plugin.enqueueAgentLogEntry(makeTestAgentLog(fmt.Sprintf("agent-blocked-%d", i)))
	}
	select {
	case <-store.started:
	case <-time.After(2 * time.Second):
		t.Fatal("batch writer never reached the Agent store")
	}

	start := time.Now()
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Cleanup took %s; a blocked Agent store write must be interrupted, not waited on", took)
	}

	for i := 0; i < N; i++ {
		id := fmt.Sprintf("agent-blocked-%d", i)
		if _, err := inner.FindAgentLog(context.Background(), id); err != nil {
			t.Fatalf("expected the drain to persist Agent log %s: %v", id, err)
		}
	}
	if dropped := plugin.droppedRequests.Load(); dropped != 0 {
		t.Fatalf("expected 0 dropped Agent requests, got %d", dropped)
	}
}

// TestCleanupInterruptsBlockedFlushAndDrainsIt is the end-to-end pin for the
// shutdown contract: a flush stuck in the store is cancelled by Cleanup within
// the batch writer's context, the unwritten entries travel through
// recoveredBatch into drainPending, and the drain persists every one of them.
func TestCleanupInterruptsBlockedFlushAndDrainsIt(t *testing.T) {
	inner := newTestStore(t)
	store := &blockingOnceStore{LogStore: inner}
	const N = 5
	plugin, err := Init(context.Background(), &Config{Writer: &logstore.WriterConfig{
		MaxBatchSize:  N,
		BatchInterval: "10ms",
	}}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	for i := 0; i < N; i++ {
		plugin.enqueueLogEntry(makeTestLog(fmt.Sprintf("blocked-%d", i)), nil)
	}
	// Let the batch writer flush into the blocked store call.
	deadline := time.Now().Add(2 * time.Second)
	for !store.blocked.Load() {
		if time.Now().After(deadline) {
			t.Fatal("batch writer never reached the store")
		}
		time.Sleep(5 * time.Millisecond)
	}

	start := time.Now()
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Cleanup took %s; a blocked store write must be interrupted, not waited on", took)
	}

	res, err := inner.SearchLogs(context.Background(), logstore.SearchFilters{}, logstore.PaginationOptions{Limit: N})
	if err != nil {
		t.Fatalf("SearchLogs() error = %v", err)
	}
	if res.Pagination.TotalCount != N {
		t.Fatalf("expected the drain to persist all %d interrupted entries, got %d", N, res.Pagination.TotalCount)
	}
	if dropped := plugin.droppedRequests.Load(); dropped != 0 {
		t.Fatalf("expected 0 dropped requests, got %d", dropped)
	}
}

// gatedStore wraps a real store and lets a test toggle the logstore
// MaintenanceGate, standing in for another node holding the migration lock.
// It also counts batch inserts so a test can prove nothing reached the store.
type gatedStore struct {
	logstore.LogStore
	migrating atomic.Bool
	batches   atomic.Int64
}

func (s *gatedStore) MigrationInProgress(context.Context) bool { return s.migrating.Load() }

func (s *gatedStore) BatchCreateIfNotExists(ctx context.Context, entries []*logstore.Log) error {
	s.batches.Add(1)
	return s.LogStore.BatchCreateIfNotExists(ctx, entries)
}

// shrinkMaintenanceTimers makes the pause loop fast enough for a unit test and
// restores the production values afterwards.
func shrinkMaintenanceTimers(t *testing.T, poll, maxPause time.Duration) {
	t.Helper()
	prevPoll, prevMax := maintenancePollInterval, maintenanceMaxPause
	maintenancePollInterval, maintenanceMaxPause = poll, maxPause
	t.Cleanup(func() { maintenancePollInterval, maintenanceMaxPause = prevPoll, prevMax })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waitForWithin(t, 3*time.Second, what, cond)
}

func waitForWithin(t *testing.T, budget time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func countLogs(t *testing.T, store logstore.LogStore) int64 {
	t.Helper()
	res, err := store.SearchLogs(context.Background(), logstore.SearchFilters{}, logstore.PaginationOptions{Limit: 100})
	if err != nil {
		t.Fatalf("SearchLogs() error = %v", err)
	}
	return res.Pagination.TotalCount
}

// TestBatchWriterPausesWhileMigrationLockHeld pins the gate: while another node
// holds the logstore migration lock the batch writer issues no inserts, the
// write queue is the only buffer (overflow drops as always, now also counted
// under droppedDuringMaintenance), and everything buffered lands once the lock
// is released.
func TestBatchWriterPausesWhileMigrationLockHeld(t *testing.T) {
	shrinkMaintenanceTimers(t, 10*time.Millisecond, time.Minute)
	inner := newTestStore(t)
	store := &gatedStore{LogStore: inner}
	store.migrating.Store(true)
	const batch, capacity, overflow = 2, 3, 2
	plugin, err := Init(context.Background(), &Config{Writer: &logstore.WriterConfig{
		MaxBatchSize:       batch,
		BatchInterval:      "10ms",
		WriteQueueCapacity: capacity,
	}}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	for i := 0; i < batch; i++ {
		plugin.enqueueLogEntry(makeTestLog(fmt.Sprintf("paused-batch-%d", i)), nil)
	}
	waitFor(t, "writer to pause on the held lock", plugin.writerPaused.Load)

	// The writer is not consuming: these fill the channel, then two more overflow.
	for i := 0; i < capacity+overflow; i++ {
		plugin.enqueueLogEntry(makeTestLog(fmt.Sprintf("paused-queued-%d", i)), nil)
	}
	time.Sleep(50 * time.Millisecond)
	if got := store.batches.Load(); got != 0 {
		t.Fatalf("expected no inserts while the migration lock is held, got %d batch calls", got)
	}
	if got := plugin.droppedRequests.Load(); got != overflow {
		t.Fatalf("expected %d queue-full drops, got %d", overflow, got)
	}
	if got := plugin.droppedDuringMaintenance.Load(); got != overflow {
		t.Fatalf("expected the %d drops to be attributed to the migration window, got %d", overflow, got)
	}

	store.migrating.Store(false)
	waitFor(t, "buffered entries to be written after the lock cleared", func() bool {
		return countLogs(t, inner) == batch+capacity
	})
	waitFor(t, "writer to clear its paused flag", func() bool { return !plugin.writerPaused.Load() })
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if got := plugin.droppedRequests.Load(); got != overflow {
		t.Fatalf("resume must not drop anything further: expected %d, got %d", overflow, got)
	}
}

// TestBatchWriterForcedFlushAfterMaxPause pins the safety valve: a lock that is
// never released cannot stall writes forever; after maintenanceMaxPause the
// writer flushes exactly as it did before the gate existed.
func TestBatchWriterForcedFlushAfterMaxPause(t *testing.T) {
	shrinkMaintenanceTimers(t, 10*time.Millisecond, 40*time.Millisecond)
	inner := newTestStore(t)
	store := &gatedStore{LogStore: inner}
	store.migrating.Store(true)
	const N = 2
	plugin, err := Init(context.Background(), &Config{Writer: &logstore.WriterConfig{
		MaxBatchSize:  N,
		BatchInterval: "10ms",
	}}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })

	for i := 0; i < N; i++ {
		plugin.enqueueLogEntry(makeTestLog(fmt.Sprintf("forced-%d", i)), nil)
	}
	waitFor(t, "forced flush after the pause cap", func() bool { return countLogs(t, inner) == N })
	if plugin.droppedRequests.Load() != 0 {
		t.Fatalf("forced flush must not drop entries, got %d", plugin.droppedRequests.Load())
	}
}

// TestCleanupDuringMigrationPauseStillDrains pins shutdown behaviour: Cleanup
// interrupts a paused writer at once and its drain writes both the held batch
// and the queue without consulting the gate.
func TestCleanupDuringMigrationPauseStillDrains(t *testing.T) {
	shrinkMaintenanceTimers(t, 10*time.Millisecond, time.Minute)
	inner := newTestStore(t)
	store := &gatedStore{LogStore: inner}
	store.migrating.Store(true)
	const batch, queued = 2, 3
	plugin, err := Init(context.Background(), &Config{Writer: &logstore.WriterConfig{
		MaxBatchSize:       batch,
		BatchInterval:      "10ms",
		WriteQueueCapacity: queued,
	}}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	for i := 0; i < batch; i++ {
		plugin.enqueueLogEntry(makeTestLog(fmt.Sprintf("drain-batch-%d", i)), nil)
	}
	waitFor(t, "writer to pause on the held lock", plugin.writerPaused.Load)
	for i := 0; i < queued; i++ {
		plugin.enqueueLogEntry(makeTestLog(fmt.Sprintf("drain-queued-%d", i)), nil)
	}

	start := time.Now()
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Cleanup took %s; a paused writer must hand off immediately", took)
	}
	if got := countLogs(t, inner); got != batch+queued {
		t.Fatalf("expected the drain to persist all %d entries despite the held lock, got %d", batch+queued, got)
	}
	if plugin.droppedRequests.Load() != 0 {
		t.Fatalf("expected 0 dropped requests, got %d", plugin.droppedRequests.Load())
	}
}

// TestBatchWriterGateFalseWritesAsUsual pins the non-regression invariant: a
// store whose gate reports no migration behaves exactly like a store without
// a gate at all.
func TestBatchWriterGateFalseWritesAsUsual(t *testing.T) {
	const N = 5
	for name, mk := range map[string]func(inner logstore.LogStore) logstore.LogStore{
		"no gate":    func(inner logstore.LogStore) logstore.LogStore { return &recordingStore{LogStore: inner} },
		"gate false": func(inner logstore.LogStore) logstore.LogStore { return &gatedStore{LogStore: inner} },
	} {
		t.Run(name, func(t *testing.T) {
			inner := newTestStore(t)
			plugin, err := Init(context.Background(), &Config{Writer: &logstore.WriterConfig{
				MaxBatchSize:  N,
				BatchInterval: "10ms",
			}}, testLogger{}, mk(inner), nil, nil, nil)
			if err != nil {
				t.Fatalf("Init() error = %v", err)
			}
			for i := 0; i < N; i++ {
				plugin.enqueueLogEntry(makeTestLog(fmt.Sprintf("plain-%d", i)), nil)
			}
			waitFor(t, "all entries to be written", func() bool { return countLogs(t, inner) == N })
			if err := plugin.Cleanup(); err != nil {
				t.Fatalf("Cleanup() error = %v", err)
			}
			if plugin.writerPaused.Load() || plugin.droppedDuringMaintenance.Load() != 0 || plugin.droppedRequests.Load() != 0 {
				t.Fatalf("no pause or drops expected: paused=%v maintenanceDrops=%d drops=%d",
					plugin.writerPaused.Load(), plugin.droppedDuringMaintenance.Load(), plugin.droppedRequests.Load())
			}
		})
	}
}

// TestBatchWriterKeepsWritingAfterPauseCapUntilLockClears pins the cap's reach:
// once maintenanceMaxPause has been spent on a migration window, later batches
// flush at once instead of each waiting out another cap, and the next window
// (after the lock has been seen released) pauses again.
func TestBatchWriterKeepsWritingAfterPauseCapUntilLockClears(t *testing.T) {
	const poll, maxPause = 10 * time.Millisecond, 200 * time.Millisecond
	shrinkMaintenanceTimers(t, poll, maxPause)
	inner := newTestStore(t)
	store := &gatedStore{LogStore: inner}
	store.migrating.Store(true)
	const batch = 2
	plugin, err := Init(context.Background(), &Config{Writer: &logstore.WriterConfig{
		MaxBatchSize:  batch,
		BatchInterval: "10ms",
	}}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })
	enqueue := func(prefix string, n int) {
		for i := 0; i < n; i++ {
			plugin.enqueueLogEntry(makeTestLog(fmt.Sprintf("%s-%d", prefix, i)), nil)
		}
	}

	enqueue("cap-first", batch)
	waitFor(t, "forced flush after the pause cap", func() bool { return countLogs(t, inner) == batch })

	const more = 3 * batch
	enqueue("cap-later", more)
	deadline := time.Now().Add(maxPause)
	for countLogs(t, inner) < batch+more {
		if time.Now().After(deadline) {
			t.Fatalf("after the pause cap, later batches must flush at once while the lock stays held: wrote %d of %d within %s",
				countLogs(t, inner)-batch, more, maxPause)
		}
		time.Sleep(poll)
	}

	store.migrating.Store(false)
	enqueue("cap-released", batch)
	waitFor(t, "writes after the lock cleared", func() bool { return countLogs(t, inner) == 2*batch+more })
	store.migrating.Store(true)
	enqueue("cap-again", batch)
	waitFor(t, "writer to pause for the next migration window", plugin.writerPaused.Load)
	if plugin.droppedRequests.Load() != 0 {
		t.Fatalf("expected 0 dropped requests, got %d", plugin.droppedRequests.Load())
	}
}

// usageGatedStore is a gatedStore that also counts the deferred-usage lookups
// and updates so a test can tell whether the usage path touched the store.
type usageGatedStore struct {
	gatedStore
	present atomic.Bool
	lookups atomic.Int64
	updates atomic.Int64
}

func (s *usageGatedStore) IsLogEntryPresent(context.Context, string) (bool, error) {
	s.lookups.Add(1)
	return s.present.Load(), nil
}

func (s *usageGatedStore) Update(context.Context, string, any) error {
	s.updates.Add(1)
	return nil
}

// scheduleUsage runs scheduleDeferredUsageUpdate for requestID and delivers the
// trailing usage, so the worker goroutine moves on to the store.
func scheduleUsage(plugin *LoggerPlugin, requestID string) {
	ch := make(chan *schemas.BifrostLLMUsage, 1)
	ctx := schemas.NewBifrostContextWithValue(context.Background(), time.Time{},
		schemas.BifrostContextKeyDeferredUsage, (<-chan *schemas.BifrostLLMUsage)(ch))
	plugin.scheduleDeferredUsageUpdate(ctx, requestID, false)
	ch <- &schemas.BifrostLLMUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}
	close(ch)
}

func newUsageGatedPlugin(t *testing.T) (*LoggerPlugin, *usageGatedStore) {
	t.Helper()
	store := &usageGatedStore{gatedStore: gatedStore{LogStore: newTestStore(t)}}
	store.present.Store(true)
	store.migrating.Store(true)
	plugin, err := Init(context.Background(), &Config{Writer: &logstore.WriterConfig{
		MaxBatchSize:  2,
		BatchInterval: "10ms",
	}}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })
	return plugin, store
}

// TestDeferredUsageWaitsForMigrationAndWriterResume pins the usage path's gate:
// while another node holds the migration lock no lookup or update is issued;
// once the lock clears the update still waits for our own paused batch writer
// to resume (it polls the lock on its own cadence, so the row it holds may not
// have landed yet); then the lookup and update run as usual.
func TestDeferredUsageWaitsForMigrationAndWriterResume(t *testing.T) {
	shrinkMaintenanceTimers(t, 10*time.Millisecond, time.Minute)
	plugin, store := newUsageGatedPlugin(t)
	// Nothing is enqueued, so the batch writer never flushes and the paused flag
	// is the test's to drive.
	plugin.writerPaused.Store(true)

	scheduleUsage(plugin, "usage-gated")
	time.Sleep(50 * time.Millisecond)
	if l, u := store.lookups.Load(), store.updates.Load(); l != 0 || u != 0 {
		t.Fatalf("no store access expected while the migration lock is held: lookups=%d updates=%d", l, u)
	}

	store.migrating.Store(false)
	time.Sleep(50 * time.Millisecond)
	if l := store.lookups.Load(); l != 0 {
		t.Fatalf("lookup ran %d time(s) before the paused writer resumed; the row it holds cannot be present yet", l)
	}

	plugin.writerPaused.Store(false)
	waitFor(t, "deferred usage update after the writer resumed", func() bool { return store.updates.Load() == 1 })
	if l := store.lookups.Load(); l != 1 {
		t.Fatalf("expected exactly one presence lookup, got %d", l)
	}
	if d := plugin.droppedDeferredUsage.Load(); d != 0 {
		t.Fatalf("expected no dropped deferred usage, got %d", d)
	}
}

// TestDeferredUsageDroppedWhenMigrationOutlivesBudget pins the bound: a lock
// still held when the deferred-usage budget runs out drops the update, counts
// it, and never touches the store.
func TestDeferredUsageDroppedWhenMigrationOutlivesBudget(t *testing.T) {
	shrinkMaintenanceTimers(t, 10*time.Millisecond, time.Minute)
	prev := deferredUsageDBTimeout
	deferredUsageDBTimeout = 100 * time.Millisecond
	t.Cleanup(func() { deferredUsageDBTimeout = prev })
	plugin, store := newUsageGatedPlugin(t)

	scheduleUsage(plugin, "usage-budget")
	waitFor(t, "the deferred usage update to be dropped", func() bool { return plugin.droppedDeferredUsage.Load() == 1 })
	if l, u := store.lookups.Load(), store.updates.Load(); l != 0 || u != 0 {
		t.Fatalf("a dropped update must not touch the store: lookups=%d updates=%d", l, u)
	}
}

// testPostgresConnConfig points at the Postgres from tests/docker-compose.yml.
func testPostgresConnConfig() *postgresconn.Config {
	return &postgresconn.Config{
		Host:     schemas.NewSecretVar("localhost"),
		Port:     schemas.NewSecretVar("5432"),
		User:     schemas.NewSecretVar("bifrost"),
		Password: schemas.NewSecretVar("bifrost_password"),
		DBName:   schemas.NewSecretVar("bifrost"),
		SSLMode:  schemas.NewSecretVar("disable"),
	}
}

// openTestPostgres returns a plain pool on the test Postgres for a test's own
// locking, counting and cleanup, or skips the test when it is unreachable.
func openTestPostgres(t *testing.T, cfg *postgresconn.Config) *sql.DB {
	t.Helper()
	db, err := postgresconn.Open(postgresconn.BuildDSN(cfg), cfg, nil)
	if err != nil {
		t.Skipf("Postgres not available, skipping test: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Skipf("Postgres not available, skipping test: %v", err)
	}
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		t.Skipf("Postgres not available, skipping test: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return sqlDB
}

// newTestPostgresStore opens the logging plugin's log store against the test
// Postgres. Unlike the SQLite store this one carries the real migration-lock
// probe, so the gate under test is the production one.
func newTestPostgresStore(t *testing.T, cfg *postgresconn.Config) logstore.LogStore {
	t.Helper()
	store, err := logstore.NewLogStore(context.Background(), &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypePostgres,
		Config:  &logstore.PostgresConfig{Config: *cfg},
	}, testLogger{})
	if err != nil {
		t.Fatalf("NewLogStore(postgres) error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	return store
}

// logstoreMigrationLockKey mirrors logstore's migrationAdvisoryLockKey. If the
// key ever changes this test stops seeing the writer pause and fails, which is
// the point: the gate must watch the key the migrator actually takes.
const logstoreMigrationLockKey int64 = 1000011

// holdMigrationLockLikeMigrator takes the logstore migration advisory lock on a
// dedicated session exactly as the migrator does (pg_try_advisory_lock on its
// own connection, held for the whole run) and returns that session plus a
// release func that unlocks and closes it.
//
// Advisory locks are scoped per database, not per schema, so the
// framework/logstore tests (which run the real migrator against the same
// `bifrost` database) can legitimately hold this key for a few seconds when
// both packages run at once. Like the migrator, the helper retries instead of
// treating a held lock as a failure, and only gives up after a deadline.
func holdMigrationLockLikeMigrator(t *testing.T, sqlDB *sql.DB) (*sql.Conn, func()) {
	t.Helper()
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn() error = %v", err)
	}
	const lockWait = 30 * time.Second
	deadline := time.Now().Add(lockWait)
	for {
		var got bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", logstoreMigrationLockKey).Scan(&got); err != nil {
			_ = conn.Close()
			t.Fatalf("pg_try_advisory_lock error = %v", err)
		}
		if got {
			break
		}
		if time.Now().After(deadline) {
			_ = conn.Close()
			t.Fatalf("migration lock %d still held by another session after %s", logstoreMigrationLockKey, lockWait)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			_, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", logstoreMigrationLockKey)
			_ = conn.Close()
		})
	}
	t.Cleanup(release)
	return conn, release
}

// TestBatchWriterGateUnderSteadyLoadPostgres is the end-to-end check of the gate
// against a real Postgres store with production timers. Logs arrive through the
// writer at 100/s for the whole run. Part way through, a migrator-style session
// takes the migration advisory lock and then the ACCESS EXCLUSIVE lock on logs
// that ALTER TABLE needs. The writer must pause within one probe TTL plus a
// flush, the table lock must be granted without waiting behind inserts, no row
// may land while the advisory lock is held, and every log must land with zero
// drops once it clears.
func TestBatchWriterGateUnderSteadyLoadPostgres(t *testing.T) {
	cfg := testPostgresConnConfig()
	aux := openTestPostgres(t, cfg)
	store := newTestPostgresStore(t, cfg)
	ctx := context.Background()

	prefix := fmt.Sprintf("gate-load-%d-", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = aux.ExecContext(ctx, "DELETE FROM logs WHERE id LIKE $1", prefix+"%")
	})
	written := func() int64 {
		var n int64
		if err := aux.QueryRowContext(ctx, "SELECT count(*) FROM logs WHERE id LIKE $1", prefix+"%").Scan(&n); err != nil {
			t.Fatalf("count error = %v", err)
		}
		return n
	}

	plugin, err := Init(ctx, &Config{Writer: &logstore.WriterConfig{
		MaxBatchSize:  50,
		BatchInterval: "500ms",
	}}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })

	// Producer: one log every 10ms (100 logs/s) until stopped.
	var enqueued atomic.Int64
	stop := make(chan struct{})
	producerDone := make(chan struct{})
	var stopOnce sync.Once
	stopProducer := func() {
		stopOnce.Do(func() { close(stop) })
		<-producerDone
	}
	t.Cleanup(stopProducer)
	go func() {
		defer close(producerDone)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			plugin.enqueueLogEntry(makeTestLog(fmt.Sprintf("%s%d", prefix, i)), nil)
			enqueued.Add(1)
		}
	}()

	// Steady state first: the writer is landing rows under load.
	waitForWithin(t, 5*time.Second, "rows to land under steady load", func() bool { return written() >= 100 })

	// Another node starts a migration.
	conn, release := holdMigrationLockLikeMigrator(t, aux)
	lockedAt := time.Now()
	waitForWithin(t, 5*time.Second, "writer to pause once the migration lock is held", plugin.writerPaused.Load)
	t.Logf("writer paused %s after the migration lock was taken (probe TTL %s, batch interval 500ms)", time.Since(lockedAt).Round(time.Millisecond), time.Second)
	atPause := written()

	// The migrator's DDL needs ACCESS EXCLUSIVE on logs. With the writer paused
	// nothing is queued ahead of it, so it must be granted without waiting.
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx error = %v", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit; keeps conn.Close from hanging on a failed lock
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '5s'"); err != nil {
		t.Fatalf("SET LOCAL lock_timeout error = %v", err)
	}
	ddlStart := time.Now()
	if _, err := tx.ExecContext(ctx, "LOCK TABLE logs IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatalf("ALTER-style lock on logs must not wait behind log inserts while the gate is engaged: %v", err)
	}
	t.Logf("ACCESS EXCLUSIVE lock on logs granted in %s", time.Since(ddlStart).Round(time.Millisecond))
	time.Sleep(1500 * time.Millisecond) // the "migration" runs while ~150 more logs arrive
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit error = %v", err)
	}

	// Advisory lock still held: nothing may have landed, and nothing was dropped
	// (the queue holds 10000; a few seconds at 100/s is nowhere near it).
	if got := written(); got != atPause {
		t.Fatalf("%d rows were written while the migration lock was held (had %d at pause)", got-atPause, atPause)
	}
	time.Sleep(time.Second)
	if got := written(); got != atPause {
		t.Fatalf("%d rows were written while the migration lock was still held (had %d at pause)", got-atPause, atPause)
	}
	if d := plugin.droppedRequests.Load(); d != 0 {
		t.Fatalf("expected no drops while buffering through the pause, got %d", d)
	}

	// Migration done: the lock clears, the writer resumes and the backlog lands.
	release()
	stopProducer()
	total := enqueued.Load()
	if total-atPause < 100 {
		t.Fatalf("expected at least 100 logs to arrive during the pause, got %d", total-atPause)
	}
	waitForWithin(t, 15*time.Second, "every log to land after the lock cleared", func() bool { return written() == total })
	waitFor(t, "writer to clear its paused flag", func() bool { return !plugin.writerPaused.Load() })
	if d, m := plugin.droppedRequests.Load(), plugin.droppedDuringMaintenance.Load(); d != 0 || m != 0 {
		t.Fatalf("expected zero drops end to end, got dropped=%d droppedDuringMaintenance=%d", d, m)
	}
	t.Logf("%d logs enqueued at 100/s, %d written before the pause, all %d landed after the lock cleared with 0 drops", total, atPause, total)
}

// evictablePending builds a pending entry whose idle clock is already past
// pendingLogTTL, so the next cleanupStalePendingLogs sweep evicts it.
func evictablePending(id, input string) *PendingLogData {
	stale := time.Now().Add(-pendingLogTTL - time.Minute)
	pending := &PendingLogData{
		RequestID: id,
		Timestamp: time.Now().Add(-2 * time.Hour),
		Status:    "processing",
		CreatedAt: stale,
		InitialData: &InitialLogData{
			Object:   "chat.completion",
			Provider: "openai",
			Model:    "gpt-4o",
			InputHistory: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &input},
			}},
		},
	}
	pending.LastActivity.Store(stale.UnixNano())
	return pending
}

// evictionChatResult is a successful non-streaming chat response with usage and
// an assistant message, used to finish an evicted request.
func evictionChatResult(output string) *schemas.BifrostResponse {
	finish := "stop"
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{
				Index:        0,
				FinishReason: &finish,
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role:    schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{ContentStr: &output},
					},
				},
			}},
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-4o",
				ResolvedModelUsed:      "gpt-4o",
			},
		},
	}
}

func evictionRequestCtx(id string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, id)
	return ctx
}

// TestPersistEvictedPartialSkipsEntryAlreadyTaken: eviction must own the pending
// entry before persisting anything. When PostLLMHook has already taken the entry
// (the completion will insert the full row), eviction writes neither a partial
// row nor a marker, otherwise the partial could land first and the completed
// IfNotExists insert would no-op, leaving the row stuck in "processing".
func TestPersistEvictedPartialSkipsEntryAlreadyTaken(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	pending := evictablePending("req-taken", "input")
	plugin.pendingLogsEntries.Store("req-taken", pending)
	// PostLLMHook won the entry between the sweep's Range and the eviction.
	plugin.pendingLogsEntries.Delete("req-taken")

	plugin.persistEvictedPartial("req-taken", pending)
	if _, marked := plugin.evictedPartials.Load("req-taken"); marked {
		t.Fatal("expected no eviction marker for an entry PostLLMHook already took")
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := store.FindByID(context.Background(), "req-taken"); err == nil {
		t.Fatal("expected no partial row for an entry PostLLMHook already took")
	}
}

// TestConcurrentEvictionAndCompletionKeepsEveryLog races the stale sweep against
// terminal PostLLMHook calls for the same requests: whichever side wins each
// entry, every request must end as a single success row with its input.
func TestConcurrentEvictionAndCompletionKeepsEveryLog(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	const n = 300
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("race-%d", i)
		plugin.pendingLogsEntries.Store(id, evictablePending(id, "input-"+id))
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		plugin.cleanupStalePendingLogs()
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("race-%d", i)
			if _, _, err := plugin.PostLLMHook(evictionRequestCtx(id), evictionChatResult("out"), nil); err != nil {
				t.Errorf("PostLLMHook(%s) error = %v", id, err)
			}
		}
	}()
	wg.Wait()
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	for i := 0; i < n; i++ {
		id := fmt.Sprintf("race-%d", i)
		row, err := store.FindByID(context.Background(), id)
		if err != nil {
			t.Fatalf("FindByID(%s) error = %v (log lost)", id, err)
		}
		if row.Status != "success" {
			t.Fatalf("%s: expected success, got %q", id, row.Status)
		}
		if len(row.InputHistoryParsed) != 1 {
			t.Fatalf("%s: expected input preserved, got %d messages", id, len(row.InputHistoryParsed))
		}
	}
	if dropped := plugin.droppedRequests.Load(); dropped != 0 {
		t.Fatalf("expected 0 dropped requests, got %d", dropped)
	}
}

// TestEvictionWithoutQueuedPartialKeepsPendingEntry: when the write queue is
// full the partial insert cannot be queued. The pending entry must stay in
// memory (no marker, nothing counted as dropped yet) so the next sweep retries
// the insert or a completion still writes the full row; otherwise the input
// would be gone with no copy anywhere.
func TestEvictionWithoutQueuedPartialKeepsPendingEntry(t *testing.T) {
	plugin := &LoggerPlugin{ctx: context.Background(), store: newTestStore(t), logger: testLogger{}, writeQueue: make(chan *writeQueueEntry, 1)}
	plugin.writeQueue <- &writeQueueEntry{}

	pending := evictablePending("req-full", "input")
	plugin.pendingLogsEntries.Store("req-full", pending)
	plugin.persistEvictedPartial("req-full", pending)

	if _, marked := plugin.evictedPartials.Load("req-full"); marked {
		t.Fatal("expected no eviction marker when the partial insert was not queued")
	}
	if _, kept := plugin.pendingLogsEntries.Load("req-full"); !kept {
		t.Fatal("expected the pending entry kept for a retry when the partial insert was not queued")
	}
	if got := plugin.droppedRequests.Load(); got != 0 {
		t.Fatalf("expected nothing counted as dropped while a retry is pending, got %d", got)
	}

	// The queue drains; the next sweep persists the partial and records the marker.
	<-plugin.writeQueue
	plugin.persistEvictedPartial("req-full", pending)
	if _, marked := plugin.evictedPartials.Load("req-full"); !marked {
		t.Fatal("expected the retried eviction to record the marker")
	}
	if _, kept := plugin.pendingLogsEntries.Load("req-full"); kept {
		t.Fatal("expected the pending entry released once the partial was queued")
	}
	queued := <-plugin.writeQueue
	if queued.log == nil || queued.log.ID != "req-full" || len(queued.log.InputHistoryParsed) != 1 {
		t.Fatalf("expected the queued partial to carry the input, got %+v", queued.log)
	}
}

// TestEvictionGivesUpAfterRepeatedQueueFailures: retries are capped so a queue
// that stays full cannot pin evicted entries in memory forever; after the last
// attempt the entry is dropped and counted.
func TestEvictionGivesUpAfterRepeatedQueueFailures(t *testing.T) {
	plugin := &LoggerPlugin{ctx: context.Background(), store: newTestStore(t), logger: testLogger{}, writeQueue: make(chan *writeQueueEntry, 1)}
	plugin.writeQueue <- &writeQueueEntry{}

	pending := evictablePending("req-stuck", "input")
	plugin.pendingLogsEntries.Store("req-stuck", pending)
	for i := 0; i < maxEvictionAttempts; i++ {
		plugin.persistEvictedPartial("req-stuck", pending)
	}

	if _, kept := plugin.pendingLogsEntries.Load("req-stuck"); kept {
		t.Fatalf("expected the entry dropped after %d failed attempts", maxEvictionAttempts)
	}
	if got := plugin.droppedRequests.Load(); got != 1 {
		t.Fatalf("expected exactly one drop counted when giving up, got %d", got)
	}
	if _, marked := plugin.evictedPartials.Load("req-stuck"); marked {
		t.Fatal("expected no marker for an entry that was never persisted")
	}
}

// TestEvictionKeepsVirtualKeyHiddenPolicy: with retain_content_in_object_storage
// on, a virtual key that disables content logging still has its input captured
// (for hidden storage). Eviction must keep that decision instead of stamping the
// global, visible policy on the partial row.
func TestEvictionKeepsVirtualKeyHiddenPolicy(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	retain := true
	plugin.retainContentInObjectStorage = &retain
	plugin.objectStorageEnabled = true

	ctx := evictionRequestCtx("req-vk-hidden")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceDisableContentLogging, true)
	if _, _, err := plugin.PreLLMHook(ctx, &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtrLogging("secret prompt")}}},
			Params:   &schemas.ChatParameters{},
		},
	}); err != nil {
		t.Fatalf("PreLLMHook() error = %v", err)
	}
	pendingVal, ok := plugin.pendingLogsEntries.Load("req-vk-hidden")
	if !ok {
		t.Fatal("expected pending entry after PreLLMHook")
	}
	pending := pendingVal.(*PendingLogData)
	if len(pending.InitialData.InputHistory) != 1 {
		t.Fatalf("expected input captured for hidden storage, got %d messages", len(pending.InitialData.InputHistory))
	}
	stale := time.Now().Add(-pendingLogTTL - time.Minute)
	pending.CreatedAt = stale
	pending.LastActivity.Store(stale.UnixNano())

	plugin.cleanupStalePendingLogs()
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	row, err := store.FindByID(context.Background(), "req-vk-hidden")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if !row.ContentHidden {
		t.Fatal("expected the partial row to keep the virtual key's hidden decision")
	}
	if len(row.InputHistoryParsed) != 1 {
		t.Fatalf("expected hidden input retained on the partial row, got %d messages", len(row.InputHistoryParsed))
	}
}

func strPtrLogging(s string) *string { return &s }

// chunkCountingTracer records streaming chunks handed to the accumulator and
// reports its trace as alive.
type chunkCountingTracer struct {
	schemas.NoOpTracer
	chunks atomic.Int64
}

func (c *chunkCountingTracer) ProcessStreamingChunk(_ *schemas.BifrostContext, _ string, _ bool, _ *schemas.BifrostResponse, _ *schemas.BifrostError) *schemas.StreamAccumulatorResult {
	c.chunks.Add(1)
	return nil
}

func (c *chunkCountingTracer) GetSpanHandleByID(_ string, _ *string) schemas.SpanHandle {
	return struct{}{}
}

// TestResumedStreamChunksAccumulateAfterEviction: once a stream's pending entry
// has been evicted, its later non-final chunks must still reach the accumulator
// (without probing the DB) so the final reconcile carries the resumed output.
func TestResumedStreamChunksAccumulateAfterEviction(t *testing.T) {
	spy := &fallbackSpyStore{LogStore: newTestStore(t)}
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, spy, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	plugin.evictedPartials.Store("req-resumed", time.Now())

	tracer := &chunkCountingTracer{}
	chunk := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.ChatCompletionStreamRequest, Provider: schemas.OpenAI},
		},
	}
	for i := 0; i < 3; i++ {
		ctx := evictionRequestCtx("req-resumed")
		ctx.SetValue(schemas.BifrostContextKeyTracer, tracer)
		ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-resumed")
		if _, _, err := plugin.PostLLMHook(ctx, chunk, nil); err != nil {
			t.Fatalf("PostLLMHook() chunk %d error = %v", i, err)
		}
	}
	if got := tracer.chunks.Load(); got != 3 {
		t.Fatalf("expected 3 resumed chunks accumulated after eviction, got %d", got)
	}
	if got := spy.presentCalls.Load(); got != 0 {
		t.Fatalf("expected no DB probe for non-final chunks, got %d", got)
	}
	if _, marked := plugin.evictedPartials.Load("req-resumed"); !marked {
		t.Fatal("expected the eviction marker kept until the terminal chunk")
	}
}

// blockingPresenceStore blocks IsLogEntryPresent until its context ends, like a
// store that has stopped answering.
type blockingPresenceStore struct {
	logstore.LogStore
}

func (s *blockingPresenceStore) IsLogEntryPresent(ctx context.Context, _ string) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

// TestEvictedPartialProbeIsBounded: the DB probe on the terminal no-pending path
// runs synchronously inside the hook, so a store that never answers must not
// hold the response.
func TestEvictedPartialProbeIsBounded(t *testing.T) {
	prev := evictedPartialProbeTimeout
	evictedPartialProbeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { evictedPartialProbeTimeout = prev })

	plugin, err := Init(context.Background(), &Config{}, testLogger{}, &blockingPresenceStore{LogStore: newTestStore(t)}, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	t.Cleanup(func() { _ = plugin.Cleanup() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = plugin.PostLLMHook(evictionRequestCtx("req-slow-probe"), evictionChatResult("out"), nil)
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("PostLLMHook blocked on the presence probe instead of bounding it")
	}
}

// TestEvictionPersistsMediaInputs: the partial row must carry every captured
// input kind, not only chat and embedding history, or a late completion of a
// speech/transcription/image/video request loses its input for good.
func TestEvictionPersistsMediaInputs(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	pending := evictablePending("req-speech", "")
	pending.InitialData.Object = "audio.speech"
	pending.InitialData.InputHistory = nil
	pending.InitialData.SpeechInput = &schemas.SpeechInput{Input: "read this aloud"}
	plugin.pendingLogsEntries.Store("req-speech", pending)

	plugin.cleanupStalePendingLogs()
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	row, err := store.FindByID(context.Background(), "req-speech")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if row.Status != "processing" {
		t.Fatalf("expected processing partial, got %q", row.Status)
	}
	if row.SpeechInputParsed == nil || row.SpeechInputParsed.Input != "read this aloud" {
		t.Fatalf("expected speech input preserved on the partial row, got %+v", row.SpeechInputParsed)
	}
}

// seedProcessingRow inserts a partial "processing" row (input only) that mimics
// what cleanupStalePendingLogs persists before evicting an idle pending entry.
func seedProcessingRow(t *testing.T, store logstore.LogStore, id string, origTimestamp time.Time) {
	t.Helper()
	content := "hello from input"
	pending := &PendingLogData{
		RequestID: id,
		Timestamp: origTimestamp,
		InitialData: &InitialLogData{
			Object:   "chat.completion",
			Provider: "openai",
			Model:    "gpt-4o",
			InputHistory: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &content},
			}},
		},
	}
	partial := buildInitialLogEntry(pending)
	partial.CreatedAt = time.Now().UTC()
	if err := store.CreateIfNotExists(context.Background(), partial); err != nil {
		t.Fatalf("seed CreateIfNotExists() error = %v", err)
	}
}

// TestCleanupStalePendingPersistsPartialRow verifies the persist-before-evict
// half of the durability fallback: a stale LLM pending entry is written to the
// DB as a "processing" row (input preserved, CreatedAt reset to now, original
// Timestamp kept) before it is dropped from memory.
func TestCleanupStalePendingPersistsPartialRow(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	origTimestamp := time.Now().Add(-2 * time.Hour)
	stale := time.Now().Add(-pendingLogTTL - time.Minute)
	content := "input that must survive eviction"
	pending := &PendingLogData{
		RequestID: "req-evicted",
		Timestamp: origTimestamp,
		Status:    "processing",
		CreatedAt: stale,
		InitialData: &InitialLogData{
			Object:   "chat.completion",
			Provider: "openai",
			Model:    "gpt-4o",
			InputHistory: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &content},
			}},
		},
	}
	pending.LastActivity.Store(stale.UnixNano())
	plugin.pendingLogsEntries.Store("req-evicted", pending)

	plugin.cleanupStalePendingLogs()

	if _, ok := plugin.pendingLogsEntries.Load("req-evicted"); ok {
		t.Fatal("expected stale pending entry to be removed from memory")
	}

	// Flush the async write queue.
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	row, err := store.FindByID(context.Background(), "req-evicted")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if row.Status != "processing" {
		t.Fatalf("expected status processing, got %q", row.Status)
	}
	if row.Provider != "openai" || row.Model != "gpt-4o" {
		t.Fatalf("expected input fields preserved, got provider=%q model=%q", row.Provider, row.Model)
	}
	if len(row.InputHistoryParsed) != 1 {
		t.Fatalf("expected input history preserved, got %d messages", len(row.InputHistoryParsed))
	}
	// Timestamp keeps the original request time; CreatedAt is reset to ~now so the
	// processing-row Flush window restarts.
	if !row.Timestamp.Equal(origTimestamp.UTC()) {
		t.Fatalf("expected original timestamp %v, got %v", origTimestamp.UTC(), row.Timestamp)
	}
	if row.CreatedAt.Before(time.Now().Add(-5 * time.Minute)) {
		t.Fatalf("expected CreatedAt reset to ~now, got %v", row.CreatedAt)
	}
}

// TestPostLLMHookReconcilesEvictedPartialRowSuccess verifies the finish half: a
// successful response whose in-memory pending was already evicted reconciles
// output onto the persisted partial row via UPDATE, preserving the input.
func TestPostLLMHookReconcilesEvictedPartialRowSuccess(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	const id = "req-reconcile-success"
	origTimestamp := time.Now().Add(-90 * time.Minute)
	seedProcessingRow(t, store, id, origTimestamp)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, id)

	finish := "stop"
	usage := &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	result := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{{Index: 0, FinishReason: &finish}},
			Usage:   usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-4o",
				ResolvedModelUsed:      "gpt-4o",
			},
		},
	}

	if _, _, err := plugin.PostLLMHook(ctx, result, nil); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	row, err := store.FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if row.Status != "success" {
		t.Fatalf("expected status success after reconcile, got %q", row.Status)
	}
	if row.TotalTokens != 15 {
		t.Fatalf("expected total tokens 15 from reconciled output, got %d", row.TotalTokens)
	}
	// Input from the partial row must be preserved (UPDATE writes non-zero output
	// fields only, leaving input columns intact).
	if len(row.InputHistoryParsed) != 1 {
		t.Fatalf("expected input history preserved through reconcile, got %d messages", len(row.InputHistoryParsed))
	}
}

// TestPostLLMHookReconcilesEvictedPartialRowError verifies the error variant of
// the reconcile path: a terminal error with no in-memory pending updates the
// persisted partial row to status=error with error details.
func TestPostLLMHookReconcilesEvictedPartialRowError(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	const id = "req-reconcile-error"
	seedProcessingRow(t, store, id, time.Now().Add(-90*time.Minute))

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, id)

	statusCode := 500
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Error:          &schemas.ErrorField{Message: "provider failed late"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType:            schemas.ChatCompletionRequest,
			Provider:               schemas.OpenAI,
			OriginalModelRequested: "gpt-4o",
			ResolvedModelUsed:      "gpt-4o",
		},
	}

	if _, _, err := plugin.PostLLMHook(ctx, nil, bifrostErr); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	row, err := store.FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if row.Status != "error" {
		t.Fatalf("expected status error after reconcile, got %q", row.Status)
	}
	if row.ErrorDetails == "" && row.ErrorDetailsParsed == nil {
		t.Fatal("expected error details persisted on reconciled row")
	}
	if len(row.InputHistoryParsed) != 1 {
		t.Fatalf("expected input history preserved through reconcile, got %d messages", len(row.InputHistoryParsed))
	}
}

// fallbackSpyStore counts the DB-fallback probe (IsLogEntryPresent) so we can
// assert the terminal-call guard prevents per-chunk DB hits.
type fallbackSpyStore struct {
	logstore.LogStore
	presentCalls atomic.Int64
}

func (s *fallbackSpyStore) IsLogEntryPresent(ctx context.Context, id string) (bool, error) {
	s.presentCalls.Add(1)
	// Report absent so the fallback falls through to existing behavior; this test
	// only cares about whether the probe is invoked.
	return false, nil
}

// TestPostLLMHookNoDBProbeOnNonFinalChunk verifies that a non-final streaming
// chunk with no in-memory pending does NOT probe the DB (avoiding the per-chunk
// hammering that caused the original warning storm), while a terminal call does.
func TestPostLLMHookNoDBProbeOnNonFinalChunk(t *testing.T) {
	spy := &fallbackSpyStore{LogStore: newTestStore(t)}
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, spy, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	streamResult := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionStreamRequest,
				Provider:    schemas.OpenAI,
			},
		},
	}

	// Non-final chunk: no StreamEndIndicator set => IsFinalChunk == false.
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-stream-nopending")
	if _, _, err := plugin.PostLLMHook(ctx, streamResult, nil); err != nil {
		t.Fatalf("PostLLMHook() non-final error = %v", err)
	}
	if got := spy.presentCalls.Load(); got != 0 {
		t.Fatalf("expected no DB probe on non-final chunk, got %d calls", got)
	}

	// Final chunk: StreamEndIndicator set => terminal call => DB probe runs.
	finalCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	finalCtx.SetValue(schemas.BifrostContextKeyRequestID, "req-stream-nopending")
	finalCtx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	if _, _, err := plugin.PostLLMHook(finalCtx, streamResult, nil); err != nil {
		t.Fatalf("PostLLMHook() final error = %v", err)
	}
	if got := spy.presentCalls.Load(); got != 1 {
		t.Fatalf("expected exactly one DB probe on the final chunk, got %d calls", got)
	}
}

// slowStore wraps a LogStore and injects latency into the batched write paths so
// inserts and updates back up in the writeQueue/batch — the worst case for the
// evict→persist→reconcile ordering guarantee.
type slowStore struct {
	logstore.LogStore
	delay time.Duration
}

func (s *slowStore) BatchCreateIfNotExists(ctx context.Context, entries []*logstore.Log) error {
	time.Sleep(s.delay)
	return s.LogStore.BatchCreateIfNotExists(ctx, entries)
}

func (s *slowStore) BatchUpdate(ctx context.Context, entries []*logstore.Log) error {
	time.Sleep(s.delay)
	return s.LogStore.BatchUpdate(ctx, entries)
}

// TestBulkEvictReconcileIntegritySQLite is the end-to-end stress test: a large
// batch of requests is evicted (bulk partial-row inserts) and then finished (bulk
// reconcile updates) against a deliberately slow store so the two write lanes back
// up and interleave. It asserts that EVERY request ends up with a single row whose
// input (from the partial) AND output (from the reconcile) are intact, the final
// status is correct, and nothing was dropped — proving the ordering guarantee holds
// under load on sqlite.
func TestBulkEvictReconcileIntegritySQLite(t *testing.T) {
	base := newTestStore(t)
	slow := &slowStore{LogStore: base, delay: 3 * time.Millisecond}
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, slow, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	const N = 800
	origTimestamp := time.Now().Add(-2 * time.Hour)
	stale := time.Now().Add(-pendingLogTTL - time.Minute)

	// Phase 1: seed N pending entries that are all past the idle TTL, then evict
	// them in one sweep — this bulk-enqueues N partial "processing" inserts.
	for i := 0; i < N; i++ {
		id := fmt.Sprintf("bulk-%d", i)
		content := "input-" + id
		pending := &PendingLogData{
			RequestID: id,
			Timestamp: origTimestamp,
			Status:    "processing",
			CreatedAt: stale,
			InitialData: &InitialLogData{
				Object:   "chat.completion",
				Provider: "openai",
				Model:    "gpt-4o",
				InputHistory: []schemas.ChatMessage{{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: &content},
				}},
			},
		}
		pending.LastActivity.Store(stale.UnixNano())
		plugin.pendingLogsEntries.Store(id, pending)
	}
	plugin.cleanupStalePendingLogs()

	// Phase 2: finish all N requests with no in-memory pending (the entries were
	// just evicted) — this bulk-enqueues N reconcile updates while the inserts are
	// still draining through the slow store.
	finish := "stop"
	for i := 0; i < N; i++ {
		id := fmt.Sprintf("bulk-%d", i)
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyRequestID, id)
		result := &schemas.BifrostResponse{
			ChatResponse: &schemas.BifrostChatResponse{
				Choices: []schemas.BifrostResponseChoice{{Index: 0, FinishReason: &finish}},
				Usage:   &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType:            schemas.ChatCompletionRequest,
					Provider:               schemas.OpenAI,
					OriginalModelRequested: "gpt-4o",
					ResolvedModelUsed:      "gpt-4o",
				},
			},
		}
		if _, _, err := plugin.PostLLMHook(ctx, result, nil); err != nil {
			t.Fatalf("PostLLMHook(%s) error = %v", id, err)
		}
	}

	// Phase 3: drain the queue fully.
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	// Phase 4: every request must have exactly one row with input + output intact.
	for i := 0; i < N; i++ {
		id := fmt.Sprintf("bulk-%d", i)
		row, err := base.FindByID(context.Background(), id)
		if err != nil {
			t.Fatalf("FindByID(%s) error = %v (log lost)", id, err)
		}
		if row.Status != "success" {
			t.Fatalf("%s: expected reconciled status success, got %q", id, row.Status)
		}
		if row.TotalTokens != 15 {
			t.Fatalf("%s: expected output tokens 15, got %d", id, row.TotalTokens)
		}
		if len(row.InputHistoryParsed) != 1 {
			t.Fatalf("%s: expected input preserved through reconcile, got %d messages", id, len(row.InputHistoryParsed))
		}
		if !row.Timestamp.Equal(origTimestamp.UTC()) {
			t.Fatalf("%s: expected original timestamp preserved, got %v", id, row.Timestamp)
		}
	}
	if dropped := plugin.droppedRequests.Load(); dropped != 0 {
		t.Fatalf("expected 0 dropped requests, got %d", dropped)
	}
}

// TestProcessBatchAppliesInsertsBeforeUpdates pins the ordering the reconcile
// path relies on: within one batch the partial-row insert runs before the
// update even when the update was queued first.
func TestProcessBatchAppliesInsertsBeforeUpdates(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{ctx: context.Background(), store: store, logger: testLogger{}}

	partial := buildInitialLogEntry(evictablePending("order-1", "input first"))
	partial.CreatedAt = time.Now().UTC()
	update := &logstore.Log{ID: "order-1", Status: "success", TotalTokens: 15}

	if left := plugin.processBatch(context.Background(), []*writeQueueEntry{{logUpdate: update}, {log: partial}}); len(left) != 0 {
		t.Fatalf("expected no unwritten entries, got %d", len(left))
	}

	row, err := store.FindByID(context.Background(), "order-1")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if row.Status != "success" || row.TotalTokens != 15 {
		t.Fatalf("expected update applied after insert, got status=%q tokens=%d", row.Status, row.TotalTokens)
	}
	if len(row.InputHistoryParsed) != 1 {
		t.Fatalf("expected input from the insert preserved, got %d messages", len(row.InputHistoryParsed))
	}
}

// TestReconcileLeavesPartialMetadataIntact guards the metadata column: the
// stub has no request metadata, and a load-balancer attempt on the finishing
// context must not replace what the partial row recorded.
func TestReconcileLeavesPartialMetadataIntact(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	const id = "req-meta"
	partial := buildInitialLogEntry(evictablePending(id, "input"))
	partial.CreatedAt = time.Now().UTC()
	partial.MetadataParsed = map[string]interface{}{"x": "y"}
	if err := store.CreateIfNotExists(context.Background(), partial); err != nil {
		t.Fatalf("seed error = %v", err)
	}

	ctx := evictionRequestCtx(id)
	ctx.SetValue(schemas.BifrostContextKeyLoadBalancerAttempt, map[string]string{schemas.LoadBalancerMetadataPrefix + "node": "a"})
	if _, _, err := plugin.PostLLMHook(ctx, evictionChatResult("late output"), nil); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	row, err := store.FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if row.Status != "success" {
		t.Fatalf("expected reconciled status success, got %q", row.Status)
	}
	if got := row.MetadataParsed["x"]; got != "y" {
		t.Fatalf("expected partial metadata preserved, got %v", row.MetadataParsed)
	}
}

// TestReconcileKeepsInputContentSummary guards content_summary: an output-only
// update must not regenerate the row's summary from the output alone.
func TestReconcileKeepsInputContentSummary(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	const id = "req-summary"
	seedProcessingRow(t, store, id, time.Now().Add(-time.Hour))
	before, err := store.FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if !strings.Contains(before.ContentSummary, "hello from input") {
		t.Fatalf("expected input-derived summary on the partial row, got %q", before.ContentSummary)
	}

	if _, _, err := plugin.PostLLMHook(evictionRequestCtx(id), evictionChatResult("late output"), nil); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	after, err := store.FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if after.Status != "success" || after.OutputMessage == "" {
		t.Fatalf("expected reconciled output, got status=%q output=%q", after.Status, after.OutputMessage)
	}
	if after.ContentSummary != before.ContentSummary {
		t.Fatalf("expected content summary unchanged %q, got %q", before.ContentSummary, after.ContentSummary)
	}
}

// TestEvictionHonoursGlobalContentPolicy: with content logging disabled, the
// partial row persisted at eviction carries no input and is marked hidden.
func TestEvictionHonoursGlobalContentPolicy(t *testing.T) {
	store := newTestStore(t)
	disabled := true
	plugin, err := Init(context.Background(), &Config{DisableContentLogging: &disabled}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	plugin.pendingLogsEntries.Store("req-hidden", evictablePending("req-hidden", "secret input"))
	plugin.cleanupStalePendingLogs()
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	row, err := store.FindByID(context.Background(), "req-hidden")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if row.Status != "processing" {
		t.Fatalf("expected processing partial, got %q", row.Status)
	}
	if row.InputHistory != "" || len(row.InputHistoryParsed) != 0 {
		t.Fatalf("expected no input persisted under disabled content logging, got %q", row.InputHistory)
	}
	if !row.ContentHidden {
		t.Fatal("expected partial row marked content hidden")
	}
}

// TestEvictionDropsProvisionalInput: input captured before the virtual key's
// content decision was known is never persisted by eviction, even when the
// global policy allows content.
func TestEvictionDropsProvisionalInput(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	pending := evictablePending("req-provisional", "undecided input")
	pending.ProvisionalInput = true
	plugin.pendingLogsEntries.Store("req-provisional", pending)
	plugin.cleanupStalePendingLogs()
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	row, err := store.FindByID(context.Background(), "req-provisional")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if row.InputHistory != "" || len(row.InputHistoryParsed) != 0 {
		t.Fatalf("expected provisional input dropped at eviction, got %q", row.InputHistory)
	}
	if row.ContentHidden {
		t.Fatal("expected row visible under the global policy")
	}
}

// TestReconcileCallbackReceivesFullRow: subscribers get the full row, not the
// output-only update entry.
func TestReconcileCallbackReceivesFullRow(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	const id = "req-callback"
	seedProcessingRow(t, store, id, time.Now().Add(-time.Hour))

	got := make(chan *logstore.Log, 1)
	unsubscribe := plugin.SubscribeLogCallback(func(_ context.Context, entry *logstore.Log) {
		select {
		case got <- entry:
		default:
		}
	})
	defer unsubscribe()

	if _, _, err := plugin.PostLLMHook(evictionRequestCtx(id), evictionChatResult("late output"), nil); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	select {
	case entry := <-got:
		if entry.ID != id || entry.Provider != "openai" || entry.Model != "gpt-4o" || entry.Timestamp.IsZero() {
			t.Fatalf("expected full row in callback, got id=%q provider=%q model=%q ts=%v", entry.ID, entry.Provider, entry.Model, entry.Timestamp)
		}
		if entry.Status != "success" || len(entry.InputHistoryParsed) != 1 {
			t.Fatalf("expected reconciled row with input, got status=%q messages=%d", entry.Status, len(entry.InputHistoryParsed))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the reconcile callback")
	}
}

// TestEvictedPartialMarkerExpires: stale markers are pruned, and a finish after
// that still reconciles through the DB probe while the row exists.
func TestEvictedPartialMarkerExpires(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	plugin.evictedPartials.Store("req-old", time.Now().Add(-pendingLogTTL-time.Minute))
	plugin.evictedPartials.Store("req-fresh", time.Now())
	plugin.cleanupStalePendingLogs()
	if _, ok := plugin.evictedPartials.Load("req-old"); ok {
		t.Fatal("expected stale marker pruned")
	}
	if _, ok := plugin.evictedPartials.Load("req-fresh"); !ok {
		t.Fatal("expected fresh marker kept")
	}

	seedProcessingRow(t, store, "req-old", time.Now().Add(-time.Hour))
	if _, _, err := plugin.PostLLMHook(evictionRequestCtx("req-old"), evictionChatResult("late output"), nil); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	row, err := store.FindByID(context.Background(), "req-old")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if row.Status != "success" {
		t.Fatalf("expected reconcile via DB probe after marker expiry, got %q", row.Status)
	}
}

// TestPersistCompletedEntryBypassesInject: with a live trace on the context the
// normal path parks entries for Inject; a reconcile must write directly, since
// Inject re-enqueues as an insert that would no-op against the partial row.
func TestPersistCompletedEntryBypassesInject(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	const id = "req-traced"
	seedProcessingRow(t, store, id, time.Now().Add(-time.Hour))

	ctx := evictionRequestCtx(id)
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-1")
	if _, _, err := plugin.PostLLMHook(ctx, evictionChatResult("late output"), nil); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if _, parked := plugin.pendingLogsToInject.Load("trace-1"); parked {
		t.Fatal("expected reconcile to bypass the Inject parking slot")
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	row, err := store.FindByID(context.Background(), id)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if row.Status != "success" || row.TotalTokens != 15 {
		t.Fatalf("expected output reconciled despite live trace, got status=%q tokens=%d", row.Status, row.TotalTokens)
	}
}

// TestEnqueueLogUpdateDropsWhenQueueFull: a full write queue drops the update
// and counts it, never blocking the request goroutine.
func TestEnqueueLogUpdateDropsWhenQueueFull(t *testing.T) {
	plugin := &LoggerPlugin{ctx: context.Background(), logger: testLogger{}, writeQueue: make(chan *writeQueueEntry, 1)}
	plugin.writeQueue <- &writeQueueEntry{}

	plugin.enqueueLogUpdate(&logstore.Log{ID: "req-full"}, nil)

	if got := plugin.droppedRequests.Load(); got != 1 {
		t.Fatalf("expected 1 dropped request, got %d", got)
	}
	if len(plugin.writeQueue) != 1 {
		t.Fatalf("expected queue untouched, got %d entries", len(plugin.writeQueue))
	}
}

// TestEvictionPersistsRequestMetadata: the partial row must carry the request
// metadata captured in PreLLMHook (logging headers, isAsyncRequest); the
// reconcile deliberately writes none, so eviction is its only source.
func TestEvictionPersistsRequestMetadata(t *testing.T) {
	store := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	pending := evictablePending("req-meta-evict", "input")
	pending.InitialData.Metadata = map[string]any{"x-team": "payments", "isAsyncRequest": true}
	plugin.pendingLogsEntries.Store("req-meta-evict", pending)
	plugin.cleanupStalePendingLogs()

	if _, _, err := plugin.PostLLMHook(evictionRequestCtx("req-meta-evict"), evictionChatResult("late"), nil); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	row, err := store.FindByID(context.Background(), "req-meta-evict")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if row.Status != "success" {
		t.Fatalf("expected reconciled status success, got %q", row.Status)
	}
	if got := row.MetadataParsed["x-team"]; got != "payments" {
		t.Fatalf("expected request metadata preserved through eviction and reconcile, got %v", row.MetadataParsed)
	}
	if got, _ := row.MetadataParsed["isAsyncRequest"].(bool); !got {
		t.Fatalf("expected isAsyncRequest preserved, got %v", row.MetadataParsed)
	}
}

// staleReadStore returns rows without their output, like a hybrid store whose
// stored object has not yet received the reconcile's merge upload.
type staleReadStore struct {
	logstore.LogStore
}

func (s *staleReadStore) FindByID(ctx context.Context, id string) (*logstore.Log, error) {
	row, err := s.LogStore.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	row.OutputMessage = ""
	row.OutputMessageParsed = nil
	return row, nil
}

// TestReconcileCallbackCarriesWrittenOutput: subscribers must see the completed
// response even when the re-read row's offloaded content lags behind the update.
func TestReconcileCallbackCarriesWrittenOutput(t *testing.T) {
	base := newTestStore(t)
	plugin, err := Init(context.Background(), &Config{}, testLogger{}, &staleReadStore{LogStore: base}, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	seedProcessingRow(t, base, "req-stale-cb", time.Now().Add(-time.Hour))

	got := make(chan *logstore.Log, 1)
	defer plugin.SubscribeLogCallback(func(_ context.Context, entry *logstore.Log) {
		select {
		case got <- entry:
		default:
		}
	})()

	if _, _, err := plugin.PostLLMHook(evictionRequestCtx("req-stale-cb"), evictionChatResult("the late answer"), nil); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	select {
	case entry := <-got:
		if !strings.Contains(entry.OutputMessage, "the late answer") {
			t.Fatalf("expected the written output on the notified row, got %q", entry.OutputMessage)
		}
		if entry.OutputMessageParsed == nil || entry.OutputMessageParsed.Content == nil || entry.OutputMessageParsed.Content.ContentStr == nil || *entry.OutputMessageParsed.Content.ContentStr != "the late answer" {
			t.Fatalf("expected parsed output on the notified row, got %+v", entry.OutputMessageParsed)
		}
		if len(entry.InputHistoryParsed) != 1 || entry.Provider != "openai" {
			t.Fatalf("expected the re-read input and identity kept, got %d messages provider=%q", len(entry.InputHistoryParsed), entry.Provider)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the reconcile callback")
	}
}

// TestReconcileAppliesFinalVisibility: the partial row is saved with the policy
// known at eviction (hidden here), and the virtual key's decision arriving at
// completion allows content. The reconcile must be able to write that final
// "visible" decision, not only flip a row to hidden.
func TestReconcileAppliesFinalVisibility(t *testing.T) {
	store := newTestStore(t)
	disabled := true
	plugin, err := Init(context.Background(), &Config{DisableContentLogging: &disabled}, testLogger{}, store, nil, nil, nil)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	pending := evictablePending("req-visible-late", "input")
	pending.ContentPolicy = &contentPolicy{storeContent: true, hidden: true}
	plugin.pendingLogsEntries.Store("req-visible-late", pending)
	plugin.cleanupStalePendingLogs()

	ctx := evictionRequestCtx("req-visible-late")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceDisableContentLogging, false)
	if _, _, err := plugin.PostLLMHook(ctx, evictionChatResult("allowed answer"), nil); err != nil {
		t.Fatalf("PostLLMHook() error = %v", err)
	}
	if err := plugin.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	row, err := store.FindByID(context.Background(), "req-visible-late")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if row.Status != "success" {
		t.Fatalf("expected reconciled status success, got %q", row.Status)
	}
	if row.ContentHidden {
		t.Fatal("expected the completion's visible decision applied to the row")
	}
}

// TestProcessBatchOmitsBinaryBodies pins that body columns holding binary data
// (an audio passthrough response, a multipart upload, a Bedrock event stream)
// are stored as a size placeholder instead of raw bytes, and that a text
// preview cut inside a multi-byte character keeps its text with the partial
// character dropped. Raw NUL bytes and invalid UTF-8 are what Postgres rejects
// with SQLSTATE 22021, failing the whole batch INSERT.
func TestProcessBatchOmitsBinaryBodies(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{ctx: context.Background(), store: store, logger: testLogger{}}

	audio := "ID3\x03\x00\x00\x00\x00\xff\xfb\x90"
	multipart := "--b\r\nContent-Type: audio/wav\r\n\r\nRIFF\x24\x00\x00\x00WAVE\r\n--b--"
	cutPreview := `{"text":"caf` + "\xc3"
	eventStream := "\x00\x00\x00\x8a\x00\x00\x00\x52event"

	insert := &logstore.Log{ID: "binary-insert", Timestamp: time.Now().UTC(), CreatedAt: time.Now().UTC(), Provider: "openai", Model: "whisper-1", Status: "success", Object: "audio.transcription",
		RawRequest: multipart, RawResponse: cutPreview, PassthroughRequestBody: `{"ok":true}`, PassthroughResponseBody: audio}
	update := &logstore.Log{ID: "binary-insert", RawResponse: eventStream}

	if left := plugin.processBatch(context.Background(), []*writeQueueEntry{{log: insert}, {logUpdate: update}}); len(left) != 0 {
		t.Fatalf("expected no unwritten entries, got %d", len(left))
	}
	row, err := store.FindByID(context.Background(), "binary-insert")
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if want := fmt.Sprintf("<binary content omitted: %d bytes>", len(audio)); row.PassthroughResponseBody != want {
		t.Errorf("passthrough_response_body = %q, want %q", row.PassthroughResponseBody, want)
	}
	if want := fmt.Sprintf("<binary content omitted: %d bytes>", len(multipart)); row.RawRequest != want {
		t.Errorf("raw_request = %q, want %q", row.RawRequest, want)
	}
	if want := fmt.Sprintf("<binary content omitted: %d bytes>", len(eventStream)); row.RawResponse != want {
		t.Errorf("raw_response after update = %q, want %q", row.RawResponse, want)
	}
	if row.PassthroughRequestBody != `{"ok":true}` {
		t.Errorf("text passthrough_request_body changed: %q", row.PassthroughRequestBody)
	}
}

// TestOmitBinaryBody pins the text/binary decision.
func TestOmitBinaryBody(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"empty", "", ""},
		{"json", `{"a":"b"}`, `{"a":"b"}`},
		{"multibyte_text", "café ☕", "café ☕"},
		{"preview_cut_mid_rune", "caf\xc3", "caf"},
		{"preview_cut_mid_4byte_rune", "a\xf0\x9f\x98", "a"},
		{"nul_byte", "a\x00b", "<binary content omitted: 3 bytes>"},
		{"invalid_byte", "a\xffb", "<binary content omitted: 3 bytes>"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := omitBinaryBody(c.in); got != c.want {
				t.Fatalf("omitBinaryBody(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// deferredGuardrailRow builds a row through storeOrEnqueueEntry for a request
// whose output-monitor guardrail result is still pending. The request already
// carries input-phase guardrail state, which the result must supersede.
func deferredGuardrailRow(t *testing.T, plugin *LoggerPlugin) (*logstore.Log, *schemas.DeferredGuardrailResult) {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	deferred := schemas.NewDeferredGuardrailResult()
	ctx.SetValue(schemas.BifrostContextKeyDeferredGuardrail, deferred)
	schemas.SetRedactionDataOnContext(ctx, schemas.RedactionData{
		ReversibleMappings: schemas.RedactionMapsByPhase{Input: map[string]string{"[EMAIL_1]": "alex@example.com"}},
	})
	entry := &logstore.Log{
		ID:                   "req-monitor",
		GuardrailDebugParsed: &schemas.BifrostGuardrailMetadata{Detections: []schemas.BifrostGuardrailDetection{{Phase: "input"}}},
	}
	plugin.storeOrEnqueueEntry(ctx, entry, nil)
	return entry, deferred
}

// monitorGuardrailResult is a complete request guardrail state: the input
// detection and mapping plus what the output-monitor evaluation added.
func monitorGuardrailResult() (*schemas.BifrostGuardrailMetadata, schemas.RedactionData) {
	metadata := &schemas.BifrostGuardrailMetadata{Detections: []schemas.BifrostGuardrailDetection{{Phase: "input"}, {Phase: "output", RuleName: "monitor"}}}
	redaction := schemas.RedactionData{ReversibleMappings: schemas.RedactionMapsByPhase{
		Input:  map[string]string{"[EMAIL_1]": "alex@example.com"},
		Output: map[string]string{"[EMAIL_2]": "sam@example.com"},
	}}
	return metadata, redaction
}

// TestDeferredGuardrailRowWaitsForResult verifies a row with a pending
// output-monitor result reaches the writer only once the result lands, carrying
// the result's guardrail metadata and redaction data in place of what it was
// built with.
func TestDeferredGuardrailRowWaitsForResult(t *testing.T) {
	plugin := &LoggerPlugin{logger: testLogger{}, writeQueue: make(chan *writeQueueEntry, 2)}
	_, deferred := deferredGuardrailRow(t, plugin)
	if len(plugin.writeQueue) != 0 {
		t.Fatal("row was written before its guardrail result landed")
	}

	metadata, redaction := monitorGuardrailResult()
	deferred.Complete(metadata, redaction, nil)
	if len(plugin.writeQueue) != 1 {
		t.Fatalf("expected the row to be written once after the result, got %d writes", len(plugin.writeQueue))
	}
	written := (<-plugin.writeQueue).log
	if written.DeferredGuardrail != nil {
		t.Fatal("written row still references the pending result")
	}
	if len(written.GuardrailDebugParsed.Detections) != 2 {
		t.Fatalf("expected input and output detections, got %+v", written.GuardrailDebugParsed.Detections)
	}
	if written.RedactionData == nil || written.RedactionData.ReversibleMappings.Output["[EMAIL_2]"] != "sam@example.com" ||
		written.RedactionData.ReversibleMappings.Input["[EMAIL_1]"] != "alex@example.com" {
		t.Fatalf("expected the result's input and output mappings, got %+v", written.RedactionData)
	}
}

// TestDeferredGuardrailRowAppendsMonitorPluginLogs verifies the evaluation's
// plugin log lines land on the row after the request's own, under the same
// plugin group, and that a row with no request plugin logs still gets them.
func TestDeferredGuardrailRowAppendsMonitorPluginLogs(t *testing.T) {
	monitorLogs := []schemas.PluginLogEntry{{PluginName: "guardrails", Level: schemas.LogLevelInfo, Message: "[output-monitor] completed", Timestamp: 2}}
	for name, requestLogs := range map[string][]schemas.PluginLogEntry{
		"with request logs": {{PluginName: "guardrails", Level: schemas.LogLevelInfo, Message: "[input] evaluated", Timestamp: 1}},
		"without":           nil,
	} {
		t.Run(name, func(t *testing.T) {
			plugin := &LoggerPlugin{logger: testLogger{}, writeQueue: make(chan *writeQueueEntry, 2)}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			deferred := schemas.NewDeferredGuardrailResult()
			ctx.SetValue(schemas.BifrostContextKeyDeferredGuardrail, deferred)
			plugin.storeOrEnqueueEntry(ctx, &logstore.Log{ID: "req-monitor-logs", PluginLogs: serializePluginLogs(requestLogs)}, nil)

			metadata, redaction := monitorGuardrailResult()
			deferred.Complete(metadata, redaction, monitorLogs)
			written := (<-plugin.writeQueue).log
			var grouped map[string][]schemas.PluginLogEntry
			if err := json.Unmarshal([]byte(written.PluginLogs), &grouped); err != nil {
				t.Fatalf("plugin logs are not valid JSON: %q", written.PluginLogs)
			}
			want := append(append([]schemas.PluginLogEntry{}, requestLogs...), monitorLogs...)
			if !reflect.DeepEqual(grouped["guardrails"], want) {
				t.Fatalf("expected request lines then monitor lines %+v, got %+v", want, grouped["guardrails"])
			}
		})
	}
}

// TestDeferredGuardrailRowWrittenDuringShutdown verifies a held row whose result
// lands after Cleanup has stopped new producers is still written, and that the
// shutdown release writes a still-held row even though the writer is closing.
func TestDeferredGuardrailRowWrittenDuringShutdown(t *testing.T) {
	t.Run("result lands while closing", func(t *testing.T) {
		plugin := &LoggerPlugin{logger: testLogger{}, writeQueue: make(chan *writeQueueEntry, 2)}
		_, deferred := deferredGuardrailRow(t, plugin)
		plugin.closed.Store(true)
		metadata, redaction := monitorGuardrailResult()
		deferred.Complete(metadata, redaction, nil)
		if len(plugin.writeQueue) != 1 {
			t.Fatalf("expected the held row to be written after closing, got %d writes", len(plugin.writeQueue))
		}
	})
	t.Run("release while closing", func(t *testing.T) {
		plugin := &LoggerPlugin{logger: testLogger{}, writeQueue: make(chan *writeQueueEntry, 2)}
		deferredGuardrailRow(t, plugin)
		plugin.closed.Store(true)
		plugin.releaseHeldGuardrailRows()
		if len(plugin.writeQueue) != 1 {
			t.Fatalf("expected the release to write the held row after closing, got %d writes", len(plugin.writeQueue))
		}
	})
}

// TestDeferredGuardrailHiddenRowGetsNoRedactionData verifies the result follows
// the row's content policy: a row whose content is hidden carries no mappings.
func TestDeferredGuardrailHiddenRowGetsNoRedactionData(t *testing.T) {
	disabled := true
	plugin := &LoggerPlugin{logger: testLogger{}, writeQueue: make(chan *writeQueueEntry, 2), disableContentLogging: &disabled}
	_, deferred := deferredGuardrailRow(t, plugin)

	metadata, redaction := monitorGuardrailResult()
	deferred.Complete(metadata, redaction, nil)
	written := (<-plugin.writeQueue).log
	if written.RedactionData != nil {
		t.Fatalf("hidden row must not carry redaction mappings, got %+v", written.RedactionData)
	}
	if len(written.GuardrailDebugParsed.Detections) != 2 {
		t.Fatalf("detections are recorded regardless of content policy, got %+v", written.GuardrailDebugParsed.Detections)
	}
}

// TestReleaseHeldGuardrailRowsWritesRowOnce verifies shutdown writes a held row
// as it was built and a result landing afterwards does not write it again.
func TestReleaseHeldGuardrailRowsWritesRowOnce(t *testing.T) {
	plugin := &LoggerPlugin{logger: testLogger{}, writeQueue: make(chan *writeQueueEntry, 2)}
	entry, deferred := deferredGuardrailRow(t, plugin)

	plugin.releaseHeldGuardrailRows()
	if len(plugin.writeQueue) != 1 {
		t.Fatalf("expected the held row to be written on release, got %d writes", len(plugin.writeQueue))
	}
	metadata, redaction := monitorGuardrailResult()
	deferred.Complete(metadata, redaction, nil)
	if len(plugin.writeQueue) != 1 {
		t.Fatalf("a result after release must not write the row again, got %d writes", len(plugin.writeQueue))
	}
	if written := (<-plugin.writeQueue).log; written != entry || len(written.GuardrailDebugParsed.Detections) != 1 {
		t.Fatalf("released row must be written as built, got %+v", written.GuardrailDebugParsed)
	}
}
