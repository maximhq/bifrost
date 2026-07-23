package sidekiq

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// Store is the narrow subset of configstore the runner needs.
type Store interface {
	CreateSidekiqJob(ctx context.Context, job *tables.TableSidekiqJob) error
	GetSidekiqJob(ctx context.Context, id string) (*tables.TableSidekiqJob, error)
	// ClaimSidekiqJob atomically claims a job for runnerID; returns true only for the winner.
	ClaimSidekiqJob(ctx context.Context, id, runnerID string, staleBefore time.Time) (bool, error)
	// HeartbeatSidekiqJob bumps updated_at for a job still owned by runnerID; returns false on lost ownership.
	HeartbeatSidekiqJob(ctx context.Context, id, runnerID string) (bool, error)
	UpdateSidekiqJobProgress(ctx context.Context, id, runnerID, metadata string) error
	CompleteSidekiqJob(ctx context.Context, id, runnerID, metadata string) error
	FailSidekiqJob(ctx context.Context, id, runnerID, metadata, lastErr string) error
	// ListClaimableSidekiqJobs returns pending jobs and running jobs whose heartbeat is older than staleBefore.
	ListClaimableSidekiqJobs(ctx context.Context, staleBefore time.Time) ([]tables.TableSidekiqJob, error)
}

// ProgressFunc persists a checkpoint and bumps the heartbeat. Handlers call it after each unit of work.
type ProgressFunc func(metadata string) error

// HandlerFunc processes one job. Receives the job row (read Metadata for the resume cursor) and a
// progress callback. Returns final metadata and an error. Nil error completes the job; non-nil fails it.
// The context is cancelled if this node loses ownership or on shutdown.
type HandlerFunc func(ctx context.Context, job tables.TableSidekiqJob, progress ProgressFunc) (finalMetadata string, err error)

const (
	DispatchInterval  = 30 * time.Second
	HeartbeatInterval = 1 * time.Minute
	StaleAfter        = 15 * time.Minute
	// MaxAttempts caps re-claims before a job is permanently failed, preventing poison-job loops.
	MaxAttempts = 5
)

// Runner owns the handler registry and job goroutine lifecycle.
type Runner struct {
	store    Store
	logger   schemas.Logger
	handlers map[string]HandlerFunc
	mu       sync.RWMutex

	// runnerID fences job mutations to the node that claimed the job.
	// Empty string disables the stale window so crashed jobs are immediately re-claimable.
	runnerID   string
	staleAfter atomic.Int64 // nanoseconds

	heartbeatInterval time.Duration

	// inflight tracks job IDs being processed on this node to prevent duplicate goroutines.
	inflightMu sync.Mutex
	inflight   map[string]struct{}

	baseCtx context.Context
	cancel  context.CancelFunc
	sem     chan struct{}
	wg      sync.WaitGroup
}

// New creates a Runner. maxConcurrent bounds simultaneous job goroutines (<=0 defaults to 4).
// Pass the node ID as runnerID in cluster mode. Pass "" to make any running job immediately
// re-claimable on restart (no stale window).
func New(store Store, logger schemas.Logger, maxConcurrent int, runnerID string) *Runner {
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runner{
		store:             store,
		logger:            logger,
		handlers:          make(map[string]HandlerFunc),
		runnerID:          runnerID,
		heartbeatInterval: HeartbeatInterval,
		inflight:          make(map[string]struct{}),
		baseCtx:           ctx,
		cancel:            cancel,
		sem:               make(chan struct{}, maxConcurrent),
	}
	if runnerID == "" {
		r.staleAfter.Store(0)
	} else {
		r.staleAfter.Store(int64(StaleAfter))
	}
	return r
}

// Register binds a handler to a job kind. Call before enqueuing.
func (r *Runner) Register(kind string, fn HandlerFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[kind] = fn
}

func (r *Runner) handlerFor(kind string) (HandlerFunc, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn, ok := r.handlers[kind]
	return fn, ok
}

// Enqueue persists a new pending job and starts it as soon as a concurrency slot is free.
// Returns once the DB row is committed so the caller can respond immediately.
func (r *Runner) Enqueue(ctx context.Context, id, kind, metadata, createdBy string) error {
	if _, ok := r.handlerFor(kind); !ok {
		return fmt.Errorf("sidekiq: no handler registered for kind %q", kind)
	}
	job := &tables.TableSidekiqJob{
		ID:       id,
		Kind:     kind,
		Status:   tables.SidekiqStatusPending,
		Metadata: metadata,
	}
	if createdBy != "" {
		job.CreatedByUserID = &createdBy
	}
	if err := r.store.CreateSidekiqJob(ctx, job); err != nil {
		return err
	}
	r.spawn(*job)
	return nil
}

func (r *Runner) staleBefore() time.Time {
	return time.Now().Add(-time.Duration(r.staleAfter.Load()))
}

func (r *Runner) tryMarkInflight(id string) bool {
	r.inflightMu.Lock()
	defer r.inflightMu.Unlock()
	if _, ok := r.inflight[id]; ok {
		return false
	}
	r.inflight[id] = struct{}{}
	return true
}

func (r *Runner) clearInflight(id string) {
	r.inflightMu.Lock()
	delete(r.inflight, id)
	r.inflightMu.Unlock()
}

// spawn runs a job in its own goroutine, blocking until a concurrency slot is free.
// Used by Enqueue so an explicitly triggered job starts as soon as possible.
func (r *Runner) spawn(job tables.TableSidekiqJob) {
	if !r.tryMarkInflight(job.ID) {
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer r.clearInflight(job.ID)
		select {
		case r.sem <- struct{}{}:
		case <-r.baseCtx.Done():
			return
		}
		defer func() { <-r.sem }()
		r.execute(job)
	}()
}

// execute claims the job and runs its handler. Uses a non-blocking claim so multiple nodes
// racing for the same job are safe: only the winner (RowsAffected == 1) proceeds.
// Panics are recovered and recorded as failures.
func (r *Runner) execute(job tables.TableSidekiqJob) {
	fn, ok := r.handlerFor(job.Kind)
	if !ok {
		r.logger.Warn("sidekiq: no handler for kind %s, skipping job %s", job.Kind, job.ID)
		return
	}

	jobCtx, cancel := context.WithCancel(r.baseCtx)
	defer cancel()

	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("sidekiq: job %s (%s) panicked: %v", job.ID, job.Kind, rec)
			if err := r.store.FailSidekiqJob(r.baseCtx, job.ID, r.runnerID, "", fmt.Sprintf("panic: %v", rec)); err != nil {
				r.logger.Error("sidekiq: failed to mark panicked job %s failed: %v", job.ID, err)
			}
		}
	}()

	claimed, err := r.store.ClaimSidekiqJob(r.baseCtx, job.ID, r.runnerID, r.staleBefore())
	if err != nil {
		r.logger.Error("sidekiq: failed to claim job %s: %v", job.ID, err)
		return
	}
	if !claimed {
		return
	}

	// Re-fetch so the handler sees the latest metadata/cursor, not the snapshot from dispatch time.
	fresh, err := r.store.GetSidekiqJob(r.baseCtx, job.ID)
	if err != nil {
		r.logger.Error("sidekiq: failed to fetch job %s after claim: %v", job.ID, err)
		return
	}
	if fresh == nil {
		r.logger.Error("sidekiq: job %s vanished after claim", job.ID)
		return
	}
	job = *fresh

	if job.Attempts >= MaxAttempts {
		r.logger.Warn("sidekiq: job %s (%s) exceeded max attempts (%d)", job.ID, job.Kind, MaxAttempts)
		if ferr := r.store.FailSidekiqJob(r.baseCtx, job.ID, r.runnerID, job.Metadata, fmt.Sprintf("exceeded max attempts (%d)", MaxAttempts)); ferr != nil {
			r.logger.Error("sidekiq: failed to fail exhausted job %s: %v", job.ID, ferr)
		}
		return
	}

	stopHeartbeat := r.startHeartbeat(jobCtx, cancel, job.ID)
	defer stopHeartbeat()

	progress := func(metadata string) error {
		return r.store.UpdateSidekiqJobProgress(r.baseCtx, job.ID, r.runnerID, metadata)
	}

	finalMetadata, err := fn(jobCtx, job, progress)
	if err != nil {
		r.logger.Error("sidekiq: job %s (%s) failed: %v", job.ID, job.Kind, err)
		if ferr := r.store.FailSidekiqJob(r.baseCtx, job.ID, r.runnerID, finalMetadata, err.Error()); ferr != nil {
			r.logger.Error("sidekiq: failed to mark job %s failed: %v", job.ID, ferr)
		}
		return
	}
	if cerr := r.store.CompleteSidekiqJob(r.baseCtx, job.ID, r.runnerID, finalMetadata); cerr != nil {
		r.logger.Error("sidekiq: failed to mark job %s completed: %v", job.ID, cerr)
	}
}

// startHeartbeat periodically bumps updated_at so the job isn't judged stale.
// Cancels jobCtx if ownership is lost (job reaped and re-claimed elsewhere).
func (r *Runner) startHeartbeat(jobCtx context.Context, cancel context.CancelFunc, id string) (stop func()) {
	ticker := time.NewTicker(r.heartbeatInterval)
	done := make(chan struct{})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-jobCtx.Done():
				return
			case <-ticker.C:
				ok, err := r.store.HeartbeatSidekiqJob(r.baseCtx, id, r.runnerID)
				if err != nil {
					r.logger.Error("sidekiq: heartbeat for job %s failed: %v", id, err)
					continue
				}
				if !ok {
					r.logger.Warn("sidekiq: lost ownership of job %s, cancelling", id)
					cancel()
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// StartDispatcher scans for claimable jobs on an interval. Uses non-blocking semaphore
// acquisition so it never spawns more goroutines than available concurrency slots —
// remaining jobs are left for the next tick. Runs one scan immediately on start.
func (r *Runner) StartDispatcher(interval, staleAfter time.Duration) (stop func()) {
	if interval <= 0 {
		interval = DispatchInterval
	}
	if staleAfter <= 0 {
		staleAfter = StaleAfter
	}
	r.staleAfter.Store(int64(staleAfter))
	ticker := time.NewTicker(interval)
	done := make(chan struct{})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer ticker.Stop()
		r.dispatchOnce()
		for {
			select {
			case <-done:
				return
			case <-r.baseCtx.Done():
				return
			case <-ticker.C:
				r.dispatchOnce()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// dispatchOnce lists claimable jobs and spawns goroutines only for slots that are
// immediately available. Stops as soon as the semaphore is full so 1000 pending
// jobs never produce 1000 parked goroutines — the remainder are picked up next tick.
func (r *Runner) dispatchOnce() {
	jobs, err := r.store.ListClaimableSidekiqJobs(r.baseCtx, r.staleBefore())
	if err != nil {
		r.logger.Error("sidekiq: dispatcher failed to list claimable jobs: %v", err)
		return
	}
	for _, job := range jobs {
		if _, ok := r.handlerFor(job.Kind); !ok {
			r.logger.Warn("sidekiq: skipping job %s, no handler for kind %s", job.ID, job.Kind)
			continue
		}
		if !r.tryMarkInflight(job.ID) {
			continue
		}
		select {
		case r.sem <- struct{}{}:
		default:
			// Semaphore full; release inflight and stop — next tick picks up the rest.
			r.clearInflight(job.ID)
			return
		}
		job := job
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer r.clearInflight(job.ID)
			defer func() { <-r.sem }()
			r.execute(job)
		}()
	}
}

// Shutdown cancels the background context and waits for in-flight goroutines to return.
func (r *Runner) Shutdown() {
	r.cancel()
	r.wg.Wait()
}
