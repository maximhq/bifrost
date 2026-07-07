package sidekiq

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
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
	return &tables.TableSidekiqJob{ID: id, Kind: j.kind, Status: j.status, OwnerID: j.owner, Metadata: j.metadata, Attempts: j.attempts}, nil
}

func (f *fakeStore) ClaimSidekiqJob(_ context.Context, id, ownerID string, staleBefore time.Time) (bool, error) {
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
	j.owner = ownerID
	j.attempts++
	j.updatedAt = time.Now()
	f.running[id]++
	return true, nil
}

func (f *fakeStore) HeartbeatSidekiqJob(_ context.Context, id, ownerID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok || j.owner != ownerID || j.status != tables.SidekiqStatusRunning {
		return false, nil
	}
	j.updatedAt = time.Now()
	return true, nil
}

func (f *fakeStore) UpdateSidekiqJobProgress(_ context.Context, id, ownerID, metadata string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok || j.owner != ownerID {
		return errors.New("not owned by caller")
	}
	j.metadata = metadata
	j.updatedAt = time.Now()
	f.progress[id] = metadata
	return nil
}

func (f *fakeStore) CompleteSidekiqJob(_ context.Context, id, ownerID, metadata string) error {
	f.mu.Lock()
	j, ok := f.jobs[id]
	if !ok || j.owner != ownerID {
		f.mu.Unlock()
		return errors.New("not owned by caller")
	}
	j.status = tables.SidekiqStatusCompleted
	j.metadata = metadata
	f.completed[id] = metadata
	f.mu.Unlock()
	f.terminal <- id
	return nil
}

func (f *fakeStore) FailSidekiqJob(_ context.Context, id, ownerID, metadata, lastErr string) error {
	f.mu.Lock()
	j, ok := f.jobs[id]
	if !ok || j.owner != ownerID {
		f.mu.Unlock()
		return errors.New("not owned by caller")
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
	return New(store, bifrost.NewDefaultLogger(schemas.LogLevelError), 4)
}

func TestEnqueueRunsHandlerAndCompletes(t *testing.T) {
	store := newFakeStore()
	r := testRunner(store)
	r.Register("k", func(_ context.Context, job tables.TableSidekiqJob, progress ProgressFunc) (string, error) {
		_ = progress("checkpoint")
		return "final", nil
	})

	if err := r.Enqueue(context.Background(), "job1", "k", "{}"); err != nil {
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

	if err := r.Enqueue(context.Background(), "job2", "k", "{}"); err != nil {
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

	if err := r.Enqueue(context.Background(), "job3", "k", "{}"); err != nil {
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
	if err := r.Enqueue(context.Background(), "job4", "missing", "{}"); err == nil {
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

	ok1, err := store.ClaimSidekiqJob(context.Background(), "j", "owner-A", staleBefore)
	if err != nil || !ok1 {
		t.Fatalf("first claim should win: ok=%v err=%v", ok1, err)
	}
	ok2, err := store.ClaimSidekiqJob(context.Background(), "j", "owner-B", staleBefore)
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

	if err := r.Enqueue(context.Background(), "hb", "k", "{}"); err != nil {
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
