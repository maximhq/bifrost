package logging

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

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
func holdMigrationLockLikeMigrator(t *testing.T, sqlDB *sql.DB) (*sql.Conn, func()) {
	t.Helper()
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn() error = %v", err)
	}
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", logstoreMigrationLockKey).Scan(&got); err != nil {
		t.Fatalf("pg_try_advisory_lock error = %v", err)
	}
	if !got {
		t.Fatalf("migration lock %d is already held by another session", logstoreMigrationLockKey)
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
