package sidekiq

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jobState is the fake's in-memory model of one row.
type jobState struct {
	kind      string
	status    string
	owner     string
	metadata  string
	attempts  int
	updatedAt time.Time
}

// fakeStore is an in-memory Store for exercising the runner without a database. It
// models the atomic claim and owner fencing so multi-owner behaviour can be tested.
type fakeStore struct {
	mu         sync.Mutex
	jobs       map[string]*jobState
	created    []tables.TableSidekiqJob
	running    map[string]int // successful claim count per id
	progress   map[string]string
	completed  map[string]string
	failedMeta map[string]string
	failedErr  map[string]string
	terminal   chan string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		jobs:       map[string]*jobState{},
		running:    map[string]int{},
		progress:   map[string]string{},
		completed:  map[string]string{},
		failedMeta: map[string]string{},
		failedErr:  map[string]string{},
		terminal:   make(chan string, 16),
	}
}

// seed inserts a job directly, bypassing Create (used to simulate rows left by a
// previous process). staleAge sets how long ago the last heartbeat was.
func (f *fakeStore) seed(id, kind, status string, staleAge time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs[id] = &jobState{kind: kind, status: status, metadata: "{}", updatedAt: time.Now().Add(-staleAge)}
}

func (f *fakeStore) CreateSidekiqJob(_ context.Context, job *tables.TableSidekiqJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, *job)
	f.jobs[job.ID] = &jobState{kind: job.Kind, status: tables.SidekiqStatusPending, metadata: job.Metadata, updatedAt: time.Now()}
	return nil
}

func (f *fakeStore) GetSidekiqJob(_ context.Context, id string) (*tables.TableSidekiqJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return nil, nil
	}
	return &tables.TableSidekiqJob{ID: id, Kind: j.kind, Status: j.status, RunnerID: j.owner, Metadata: j.metadata, Attempts: j.attempts}, nil
}

func (f *fakeStore) ClaimSidekiqJob(_ context.Context, id, runnerID string, staleBefore time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return false, nil
	}
	claimable := j.status == tables.SidekiqStatusPending ||
		(j.status == tables.SidekiqStatusRunning && j.updatedAt.Before(staleBefore))
	if !claimable {
		return false, nil
	}
	j.status = tables.SidekiqStatusRunning
	j.owner = runnerID
	j.attempts++
	j.updatedAt = time.Now()
	f.running[id]++
	return true, nil
}

func (f *fakeStore) HeartbeatSidekiqJob(_ context.Context, id, runnerID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok || j.owner != runnerID || j.status != tables.SidekiqStatusRunning {
		return false, nil
	}
	j.updatedAt = time.Now()
	return true, nil
}

func (f *fakeStore) UpdateSidekiqJobProgress(_ context.Context, id, runnerID, metadata string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok || j.owner != runnerID {
		return errors.New("not owned by caller")
	}
	j.metadata = metadata
	j.updatedAt = time.Now()
	f.progress[id] = metadata
	return nil
}

func (f *fakeStore) CompleteSidekiqJob(_ context.Context, id, runnerID, metadata string) error {
	f.mu.Lock()
	j, ok := f.jobs[id]
	if !ok || j.owner != runnerID || j.status != tables.SidekiqStatusRunning {
		f.mu.Unlock()
		return errors.New("not owned by caller or no longer running")
	}
	j.status = tables.SidekiqStatusCompleted
	j.metadata = metadata
	f.completed[id] = metadata
	f.mu.Unlock()
	f.terminal <- id
	return nil
}

func (f *fakeStore) FailSidekiqJob(_ context.Context, id, runnerID, metadata, lastErr string) error {
	f.mu.Lock()
	j, ok := f.jobs[id]
	if !ok || j.owner != runnerID || j.status != tables.SidekiqStatusRunning {
		f.mu.Unlock()
		return errors.New("not owned by caller or no longer running")
	}
	j.status = tables.SidekiqStatusFailed
	if metadata != "" {
		j.metadata = metadata
	}
	f.failedMeta[id] = metadata
	f.failedErr[id] = lastErr
	f.mu.Unlock()
	f.terminal <- id
	return nil
}

func (f *fakeStore) ListClaimableSidekiqJobs(_ context.Context, staleBefore time.Time) ([]tables.TableSidekiqJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tables.TableSidekiqJob
	for id, j := range f.jobs {
		if j.status == tables.SidekiqStatusPending ||
			(j.status == tables.SidekiqStatusRunning && j.updatedAt.Before(staleBefore)) {
			out = append(out, tables.TableSidekiqJob{ID: id, Kind: j.kind, Status: j.status, Metadata: j.metadata, Attempts: j.attempts})
		}
	}
	return out, nil
}

// waitTerminal blocks until a job reaches a terminal state or the test times out.
func waitTerminal(t *testing.T, f *fakeStore) string {
	t.Helper()
	select {
	case id := <-f.terminal:
		return id
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for job to reach a terminal state")
		return ""
	}
}

func testRunner(store Store) *Runner {
	return New(store, bifrost.NewDefaultLogger(schemas.LogLevelError), 4, "")
}

func TestEnqueueRunsHandlerAndCompletes(t *testing.T) {
	store := newFakeStore()
	r := testRunner(store)
	r.Register("k", func(_ context.Context, job tables.TableSidekiqJob, progress ProgressFunc) (string, error) {
		_ = progress("checkpoint")
		return "final", nil
	})

	if err := r.Enqueue(context.Background(), "job1", "k", "{}", ""); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	id := waitTerminal(t, store)
	if id != "job1" {
		t.Fatalf("terminal id = %q, want job1", id)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.running["job1"] != 1 {
		t.Errorf("claimed %d times, want 1", store.running["job1"])
	}
	if store.progress["job1"] != "checkpoint" {
		t.Errorf("progress = %q, want checkpoint", store.progress["job1"])
	}
	if store.completed["job1"] != "final" {
		t.Errorf("completed metadata = %q, want final", store.completed["job1"])
	}
}

func TestHandlerErrorMarksFailed(t *testing.T) {
	store := newFakeStore()
	r := testRunner(store)
	r.Register("k", func(_ context.Context, job tables.TableSidekiqJob, _ ProgressFunc) (string, error) {
		return "partial", errors.New("boom")
	})

	if err := r.Enqueue(context.Background(), "job2", "k", "{}", ""); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitTerminal(t, store)

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.failedErr["job2"] != "boom" {
		t.Errorf("failed err = %q, want boom", store.failedErr["job2"])
	}
	if store.failedMeta["job2"] != "partial" {
		t.Errorf("failed metadata = %q, want partial", store.failedMeta["job2"])
	}
}

func TestHandlerPanicRecovered(t *testing.T) {
	store := newFakeStore()
	r := testRunner(store)
	r.Register("k", func(_ context.Context, job tables.TableSidekiqJob, _ ProgressFunc) (string, error) {
		panic("kaboom")
	})

	if err := r.Enqueue(context.Background(), "job3", "k", "{}", ""); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	waitTerminal(t, store)

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.failedErr["job3"] == "" {
		t.Errorf("expected a failure recorded for a panicking handler")
	}
}

func TestEnqueueUnknownKindErrors(t *testing.T) {
	store := newFakeStore()
	r := testRunner(store)
	if err := r.Enqueue(context.Background(), "job4", "missing", "{}", ""); err == nil {
		t.Fatal("expected error enqueuing an unregistered kind")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.created) != 0 {
		t.Errorf("no job should be created for an unknown kind, got %d", len(store.created))
	}
}

// TestDispatcherRunsClaimableJobs verifies both a pending job and a stale running
// job (orphaned by a dead owner) are picked up and run.
func TestDispatcherRunsClaimableJobs(t *testing.T) {
	store := newFakeStore()
	store.seed("r1", "k", tables.SidekiqStatusRunning, 30*time.Minute) // stale, orphaned
	store.seed("r2", "k", tables.SidekiqStatusPending, 0)              // fresh pending
	r := testRunner(store)
	r.Register("k", func(_ context.Context, job tables.TableSidekiqJob, _ ProgressFunc) (string, error) {
		return "done", nil
	})

	r.dispatchOnce()

	got := map[string]bool{}
	got[waitTerminal(t, store)] = true
	got[waitTerminal(t, store)] = true
	if !got["r1"] || !got["r2"] {
		t.Errorf("expected both r1 and r2 to be dispatched, got %v", got)
	}
}

// TestClaimIsExclusiveAcrossOwners verifies only one owner wins a claim while the
// job is running with a fresh heartbeat.
func TestClaimIsExclusiveAcrossOwners(t *testing.T) {
	store := newFakeStore()
	store.seed("j", "k", tables.SidekiqStatusPending, 0)
	staleBefore := time.Now().Add(-StaleAfter)

	ok1, err := store.ClaimSidekiqJob(context.Background(), "j", "runner-A", staleBefore)
	if err != nil || !ok1 {
		t.Fatalf("first claim should win: ok=%v err=%v", ok1, err)
	}
	ok2, err := store.ClaimSidekiqJob(context.Background(), "j", "runner-B", staleBefore)
	if err != nil || ok2 {
		t.Fatalf("second claim should lose while job is running fresh: ok=%v err=%v", ok2, err)
	}
}

// TestRunnerRaceSingleWinner runs two runners (distinct owners) against one shared
// store and one pending job; exactly one must run it.
func TestRunnerRaceSingleWinner(t *testing.T) {
	store := newFakeStore()
	store.seed("race", "k", tables.SidekiqStatusPending, 0)
	handler := func(_ context.Context, job tables.TableSidekiqJob, _ ProgressFunc) (string, error) {
		return "done", nil
	}
	r1, r2 := testRunner(store), testRunner(store)
	r1.Register("k", handler)
	r2.Register("k", handler)

	go r1.dispatchOnce()
	go r2.dispatchOnce()

	waitTerminal(t, store)

	// No second terminal should arrive.
	select {
	case id := <-store.terminal:
		t.Fatalf("job %s reached a terminal state twice; not a single winner", id)
	case <-time.After(200 * time.Millisecond):
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.running["race"] != 1 {
		t.Errorf("job claimed %d times, want exactly 1 winner", store.running["race"])
	}
}

// TestMaxAttemptsExhaustedFailsWithoutRunningHandler verifies that a job whose
// pre-claim attempt count has already reached MaxAttempts is failed permanently and
// its handler is never invoked. This guards the poison-job boundary in execute().
func TestMaxAttemptsExhaustedFailsWithoutRunningHandler(t *testing.T) {
	store := newFakeStore()
	store.seed("poison", "k", tables.SidekiqStatusPending, 0)
	// Simulate a job that has already been claimed MaxAttempts times.
	store.mu.Lock()
	store.jobs["poison"].attempts = MaxAttempts
	store.mu.Unlock()

	r := testRunner(store)
	handlerCalled := make(chan struct{}, 1)
	r.Register("k", func(_ context.Context, job tables.TableSidekiqJob, _ ProgressFunc) (string, error) {
		handlerCalled <- struct{}{}
		return "done", nil
	})

	r.dispatchOnce()

	if id := waitTerminal(t, store); id != "poison" {
		t.Fatalf("terminal id = %q, want poison", id)
	}
	select {
	case <-handlerCalled:
		t.Fatal("handler must not run for a job that has exhausted its attempts")
	default:
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.jobs["poison"].status != tables.SidekiqStatusFailed {
		t.Errorf("status = %q, want failed", store.jobs["poison"].status)
	}
	if _, ok := store.failedErr["poison"]; !ok {
		t.Error("expected a failure to be recorded for the exhausted job")
	}
	if _, ok := store.completed["poison"]; ok {
		t.Error("exhausted job must not be completed")
	}
}

// TestCompleteDoesNotOverwriteReapedFailure verifies the status guard on the
// terminal writes: once the reaper has flipped a running job to failed, a late
// CompleteSidekiqJob from its former owner must not resurrect it to completed.
func TestCompleteDoesNotOverwriteReapedFailure(t *testing.T) {
	store := newFakeStore()
	store.seed("stale", "k", tables.SidekiqStatusPending, 0)
	staleBefore := time.Now().Add(-StaleAfter)

	// Owner claims the job (status -> running).
	if ok, err := store.ClaimSidekiqJob(context.Background(), "stale", "runner-A", staleBefore); err != nil || !ok {
		t.Fatalf("claim should win: ok=%v err=%v", ok, err)
	}
	// Reaper fails the job out from under the still-running owner.
	store.mu.Lock()
	store.jobs["stale"].status = tables.SidekiqStatusFailed
	store.mu.Unlock()

	// The former owner finishing late must not overwrite the failed state.
	if err := store.CompleteSidekiqJob(context.Background(), "stale", "runner-A", "final"); err == nil {
		t.Fatal("CompleteSidekiqJob should fail once the job is no longer running")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.jobs["stale"].status != tables.SidekiqStatusFailed {
		t.Errorf("status = %q, want failed (must not be resurrected)", store.jobs["stale"].status)
	}
}

// TestHeartbeatCancelsOnLostOwnership verifies the owning runner cancels its
// in-flight handler when it discovers the job was re-claimed elsewhere.
func TestHeartbeatCancelsOnLostOwnership(t *testing.T) {
	store := newFakeStore()
	r := testRunner(store)
	r.heartbeatInterval = 5 * time.Millisecond // beat frequently for the test

	started := make(chan struct{})
	cancelled := make(chan struct{})
	r.Register("k", func(ctx context.Context, job tables.TableSidekiqJob, _ ProgressFunc) (string, error) {
		close(started)
		<-ctx.Done() // block until the heartbeat cancels us
		close(cancelled)
		return "", ctx.Err()
	})

	if err := r.Enqueue(context.Background(), "hb", "k", "{}", ""); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	<-started

	// Steal ownership out from under the runner; the next heartbeat sees the mismatch.
	store.mu.Lock()
	store.jobs["hb"].owner = "someone-else"
	store.mu.Unlock()

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not cancelled after losing ownership")
	}
}

// TestConcurrencySemaphoreBound verifies that at most maxConcurrent handlers run at
// the same time. Extra jobs wait behind the semaphore and run once a slot frees.
func TestConcurrencySemaphoreBound(t *testing.T) {
	const maxConcurrent = 2
	store := newFakeStore()
	r := New(store, testRunner(store).logger, maxConcurrent, "")

	// Gate that keeps the first batch of handlers running until we release them.
	gate := make(chan struct{})
	var inFlight sync.WaitGroup

	r.Register("k", func(_ context.Context, _ tables.TableSidekiqJob, _ ProgressFunc) (string, error) {
		inFlight.Done() // signal that we entered the handler
		<-gate          // block until the test releases us
		return "", nil
	})

	// Enqueue maxConcurrent jobs and wait until all are inside the handler.
	inFlight.Add(maxConcurrent)
	for i := range maxConcurrent {
		require.NoError(t, r.Enqueue(context.Background(), fmt.Sprintf("job-%d", i), "k", "{}", ""))
	}
	inFlight.Wait() // both slots are now busy

	// Enqueue a third job — it should not start while the semaphore is full.
	require.NoError(t, r.Enqueue(context.Background(), "job-extra", "k", "{}", ""))
	// Give it a moment to (not) run.
	time.Sleep(50 * time.Millisecond)

	store.mu.Lock()
	extraStatus := store.jobs["job-extra"].status
	store.mu.Unlock()
	assert.Equal(t, tables.SidekiqStatusPending, extraStatus, "extra job must still be pending while semaphore is full")

	// Release the first batch; the extra job should now run.
	close(gate)
	waitTerminal(t, store)
	waitTerminal(t, store)
	waitTerminal(t, store)
}

// TestInflightDedupPreventsDoubleSpawn verifies that repeated dispatchOnce calls while
// the semaphore is full do not double-claim a job once a slot eventually frees.
func TestInflightDedupPreventsDoubleSpawn(t *testing.T) {
	const maxConcurrent = 1
	store := newFakeStore()
	r := New(store, testRunner(store).logger, maxConcurrent, "")

	gate := make(chan struct{})

	store.seed("blocker", "k", tables.SidekiqStatusPending, 0)
	r.Register("k", func(_ context.Context, job tables.TableSidekiqJob, _ ProgressFunc) (string, error) {
		if job.ID == "blocker" {
			<-gate
		}
		return "", nil
	})
	r.dispatchOnce() // claims the sole slot with "blocker"
	time.Sleep(20 * time.Millisecond)

	// Seed waiter while semaphore is full; repeated ticks must not cause a double-claim later.
	store.seed("waiter", "k", tables.SidekiqStatusPending, 0)
	r.dispatchOnce() // semaphore full — skips waiter
	r.dispatchOnce() // still full
	r.dispatchOnce() // still full

	// Release blocker and wait for its slot to free before dispatching again.
	close(gate)
	waitTerminal(t, store)            // blocker done
	time.Sleep(10 * time.Millisecond) // let deferred semaphore release run

	r.dispatchOnce()       // slot is now free — picks up waiter
	waitTerminal(t, store) // waiter done

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.running["waiter"] != 1 {
		t.Errorf("waiter claimed %d time(s), want exactly 1", store.running["waiter"])
	}
}

// TestDispatcherSkipsUnknownKind verifies that a job whose kind has no registered
// handler is skipped by the dispatcher without claiming or failing it.
func TestDispatcherSkipsUnknownKind(t *testing.T) {
	store := newFakeStore()
	store.seed("j", "unregistered", tables.SidekiqStatusPending, 0)
	r := testRunner(store)
	// No handler registered for "unregistered".
	r.dispatchOnce()
	time.Sleep(50 * time.Millisecond)

	store.mu.Lock()
	defer store.mu.Unlock()
	assert.Equal(t, tables.SidekiqStatusPending, store.jobs["j"].status, "job with no handler stays pending")
	if _, ok := store.completed["j"]; ok {
		t.Error("job with no handler must not be completed")
	}
	if _, ok := store.failedErr["j"]; ok {
		t.Error("job with no handler must not be failed")
	}
}

// TestResumePicksUpCheckpointCursor verifies the dispatcher-driven recovery flow:
// a job left running-but-stale (simulating a dead node) is re-claimed and its
// handler receives the metadata cursor from the last progress checkpoint.
func TestResumePicksUpCheckpointCursor(t *testing.T) {
	store := newFakeStore()
	// Seed a stale running job with a progress cursor in its metadata.
	store.seed("resume-job", "k", tables.SidekiqStatusRunning, 30*time.Minute)
	store.mu.Lock()
	store.jobs["resume-job"].metadata = `{"cursor":42}`
	store.mu.Unlock()

	r := testRunner(store)
	var gotCursor string
	r.Register("k", func(_ context.Context, job tables.TableSidekiqJob, _ ProgressFunc) (string, error) {
		gotCursor = job.Metadata
		return job.Metadata, nil
	})
	r.dispatchOnce()

	id := waitTerminal(t, store)
	assert.Equal(t, "resume-job", id)
	assert.Equal(t, `{"cursor":42}`, gotCursor, "handler must receive the checkpoint cursor")
}

// TestShutdownDrainsInFlight verifies that Shutdown waits for in-flight handlers to
// return rather than orphaning goroutines.
func TestShutdownDrainsInFlight(t *testing.T) {
	store := newFakeStore()
	r := testRunner(store)

	started := make(chan struct{})
	finished := make(chan struct{})
	r.Register("k", func(_ context.Context, _ tables.TableSidekiqJob, _ ProgressFunc) (string, error) {
		close(started)
		time.Sleep(50 * time.Millisecond)
		close(finished)
		return "", nil
	})

	require.NoError(t, r.Enqueue(context.Background(), "j", "k", "{}", ""))
	<-started

	done := make(chan struct{})
	go func() {
		r.Shutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not return after in-flight handler finished")
	}
	select {
	case <-finished:
	default:
		t.Error("Shutdown returned before the handler finished")
	}
}

// TestDispatcherQueuesOverflowOnSubsequentTicks verifies that when there are more
// pending jobs than concurrency slots, the dispatcher picks up only what fits now
// and leaves the rest for the next tick — without spawning a goroutine per job.
func TestDispatcherQueuesOverflowOnSubsequentTicks(t *testing.T) {
	const (
		maxConcurrent = 2
		totalJobs     = 6
	)
	store := newFakeStore()
	r := New(store, testRunner(store).logger, maxConcurrent, "node-1")

	for i := range totalJobs {
		store.seed(fmt.Sprintf("job-%d", i), "k", tables.SidekiqStatusPending, 0)
	}

	gate := make(chan struct{})
	started := make(chan struct{}, totalJobs)

	r.Register("k", func(_ context.Context, _ tables.TableSidekiqJob, _ ProgressFunc) (string, error) {
		started <- struct{}{}
		<-gate
		return "", nil
	})

	stop := r.StartDispatcher(30*time.Millisecond, StaleAfter)
	defer stop()

	// Wait for the first batch (maxConcurrent) to enter their handlers.
	for range maxConcurrent {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for first batch to start")
		}
	}

	// Semaphore is full — remaining jobs must still be pending.
	store.mu.Lock()
	pendingCount := 0
	for i := range totalJobs {
		if store.jobs[fmt.Sprintf("job-%d", i)].status == tables.SidekiqStatusPending {
			pendingCount++
		}
	}
	store.mu.Unlock()
	require.Equal(t, totalJobs-maxConcurrent, pendingCount, "overflow jobs must remain pending while semaphore is full")

	// Release all handlers; the dispatcher picks up the remaining jobs in subsequent ticks.
	close(gate)
	for range totalJobs {
		waitTerminal(t, store)
	}

	// Every job must have been claimed exactly once.
	store.mu.Lock()
	defer store.mu.Unlock()
	for i := range totalJobs {
		id := fmt.Sprintf("job-%d", i)
		assert.Equal(t, 1, store.running[id], "job %s must be claimed exactly once", id)
	}
}
