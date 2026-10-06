package logging

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
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
