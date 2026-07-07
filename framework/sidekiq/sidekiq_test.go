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

// fakeStore is an in-memory Store for exercising the runner without a database.
type fakeStore struct {
	mu         sync.Mutex
	created    []tables.TableSidekiqJob
	running    map[string]int
	progress   map[string]string
	completed  map[string]string
	failedMeta map[string]string
	failedErr  map[string]string
	incomplete []tables.TableSidekiqJob
	staleCalls int
	terminal   chan string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		running:    map[string]int{},
		progress:   map[string]string{},
		completed:  map[string]string{},
		failedMeta: map[string]string{},
		failedErr:  map[string]string{},
		terminal:   make(chan string, 16),
	}
}

func (f *fakeStore) CreateSidekiqJob(_ context.Context, job *tables.TableSidekiqJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, *job)
	return nil
}

func (f *fakeStore) GetSidekiqJob(_ context.Context, _ string) (*tables.TableSidekiqJob, error) {
	return nil, nil
}

func (f *fakeStore) MarkSidekiqJobRunning(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running[id]++
	return nil
}

func (f *fakeStore) UpdateSidekiqJobProgress(_ context.Context, id, metadata string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.progress[id] = metadata
	return nil
}

func (f *fakeStore) CompleteSidekiqJob(_ context.Context, id, metadata string) error {
	f.mu.Lock()
	f.completed[id] = metadata
	f.mu.Unlock()
	f.terminal <- id
	return nil
}

func (f *fakeStore) FailSidekiqJob(_ context.Context, id, metadata, lastErr string) error {
	f.mu.Lock()
	f.failedMeta[id] = metadata
	f.failedErr[id] = lastErr
	f.mu.Unlock()
	f.terminal <- id
	return nil
}

func (f *fakeStore) ListIncompleteSidekiqJobs(_ context.Context) ([]tables.TableSidekiqJob, error) {
	return f.incomplete, nil
}

func (f *fakeStore) MarkStaleSidekiqJobsFailed(_ context.Context, _ time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.staleCalls++
	return 0, nil
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
		t.Errorf("MarkSidekiqJobRunning called %d times, want 1", store.running["job1"])
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

func TestRecoverIncompleteResumesJobs(t *testing.T) {
	store := newFakeStore()
	store.incomplete = []tables.TableSidekiqJob{
		{ID: "r1", Kind: "k", Status: tables.SidekiqStatusRunning, Metadata: "{}"},
		{ID: "r2", Kind: "k", Status: tables.SidekiqStatusPending, Metadata: "{}"},
	}
	r := testRunner(store)
	r.Register("k", func(_ context.Context, job tables.TableSidekiqJob, _ ProgressFunc) (string, error) {
		return "done", nil
	})

	if err := r.RecoverIncomplete(context.Background()); err != nil {
		t.Fatalf("RecoverIncomplete: %v", err)
	}
	got := map[string]bool{}
	got[waitTerminal(t, store)] = true
	got[waitTerminal(t, store)] = true
	if !got["r1"] || !got["r2"] {
		t.Errorf("expected both r1 and r2 to be recovered, got %v", got)
	}
}

func TestReaperInvokesStaleSweep(t *testing.T) {
	store := newFakeStore()
	r := testRunner(store)
	stop := r.StartReaper(10*time.Millisecond, time.Millisecond)
	defer stop()

	deadline := time.After(2 * time.Second)
	for {
		store.mu.Lock()
		n := store.staleCalls
		store.mu.Unlock()
		if n > 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("reaper never invoked the stale sweep")
		case <-time.After(5 * time.Millisecond):
		}
	}
}
