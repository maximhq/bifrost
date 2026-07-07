package sidekiq

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// Store is the subset of the configstore the runner needs. Keeping it narrow lets
// the runner be tested with a fake and avoids a hard dependency on the full store.
type Store interface {
	CreateSidekiqJob(ctx context.Context, job *tables.TableSidekiqJob) error
	GetSidekiqJob(ctx context.Context, id string) (*tables.TableSidekiqJob, error)
	MarkSidekiqJobRunning(ctx context.Context, id string) error
	UpdateSidekiqJobProgress(ctx context.Context, id, metadata string) error
	CompleteSidekiqJob(ctx context.Context, id, metadata string) error
	FailSidekiqJob(ctx context.Context, id, metadata, lastErr string) error
	ListIncompleteSidekiqJobs(ctx context.Context) ([]tables.TableSidekiqJob, error)
	MarkStaleSidekiqJobsFailed(ctx context.Context, staleBefore time.Time) (int64, error)
}

// ProgressFunc persists a checkpoint: it replaces the job's metadata blob and
// bumps the heartbeat. Handlers call it after each unit of work (e.g. each page).
type ProgressFunc func(metadata string) error

// HandlerFunc processes one job. It is given the job (read its Metadata for the
// resume cursor) and a progress callback to checkpoint after each unit of work.
// It returns the final metadata to persist and an error. A nil error completes the
// job; a non-nil error fails it (the returned metadata is still stored so a later
// resume can read the last cursor).
type HandlerFunc func(ctx context.Context, job tables.TableSidekiqJob, progress ProgressFunc) (finalMetadata string, err error)

const (
	ReaperInterval = 1 * time.Minute
	StaleAfter     = 15 * time.Minute
)

// Runner owns the handler registry and the lifecycle of job goroutines.
type Runner struct {
	store    Store
	logger   schemas.Logger
	handlers map[string]HandlerFunc
	mu       sync.RWMutex

	baseCtx context.Context
	cancel  context.CancelFunc
	sem     chan struct{}
	wg      sync.WaitGroup
}

// New creates a Runner. maxConcurrent bounds how many job goroutines run at once
// (<=0 defaults to 4). Jobs run on a background context derived here, never on a
// request context, so they outlive the HTTP request that enqueued them.
func New(store Store, logger schemas.Logger, maxConcurrent int) *Runner {
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{
		store:    store,
		logger:   logger,
		handlers: make(map[string]HandlerFunc),
		baseCtx:  ctx,
		cancel:   cancel,
		sem:      make(chan struct{}, maxConcurrent),
	}
}

// Register binds a handler to a job kind. Call during startup, before enqueuing.
func (r *Runner) Register(kind string, fn HandlerFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[kind] = fn
}

// handlerFor returns the registered handler for a kind, if any.
func (r *Runner) handlerFor(kind string) (HandlerFunc, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn, ok := r.handlers[kind]
	return fn, ok
}

// Enqueue persists a new pending job and starts its goroutine. The caller supplies
// the id (also usable as a UI operation id), the kind, and the initial metadata
// JSON. It returns once the row is committed, so the HTTP handler can respond
// immediately while processing continues in the background.
func (r *Runner) Enqueue(ctx context.Context, id, kind, metadata string) error {
	if _, ok := r.handlerFor(kind); !ok {
		return fmt.Errorf("sidekiq: no handler registered for kind %q", kind)
	}
	job := &tables.TableSidekiqJob{
		ID:       id,
		Kind:     kind,
		Status:   tables.SidekiqStatusPending,
		Metadata: metadata,
	}
	if err := r.store.CreateSidekiqJob(ctx, job); err != nil {
		return err
	}
	r.spawn(*job)
	return nil
}

// spawn runs a job in its own goroutine, bounded by the concurrency semaphore.
func (r *Runner) spawn(job tables.TableSidekiqJob) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		select {
		case r.sem <- struct{}{}:
		case <-r.baseCtx.Done():
			return
		}
		defer func() { <-r.sem }()
		r.execute(job)
	}()
}

// execute marks the job running, invokes its handler, and records the terminal
// state. A panic in the handler is recovered and recorded as a failure so one bad
// job cannot crash the process.
func (r *Runner) execute(job tables.TableSidekiqJob) {
	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("sidekiq: job %s (%s) panicked: %v", job.ID, job.Kind, rec)
			if err := r.store.FailSidekiqJob(r.baseCtx, job.ID, "", fmt.Sprintf("panic: %v", rec)); err != nil {
				r.logger.Error("sidekiq: failed to mark panicked job %s failed: %v", job.ID, err)
			}
		}
	}()

	fn, ok := r.handlerFor(job.Kind)
	if !ok {
		if err := r.store.FailSidekiqJob(r.baseCtx, job.ID, "", "no handler registered for kind "+job.Kind); err != nil {
			r.logger.Error("sidekiq: failed to fail unhandled job %s: %v", job.ID, err)
		}
		return
	}

	if err := r.store.MarkSidekiqJobRunning(r.baseCtx, job.ID); err != nil {
		r.logger.Error("sidekiq: failed to mark job %s running: %v", job.ID, err)
		if ferr := r.store.FailSidekiqJob(r.baseCtx, job.ID, job.Metadata, err.Error()); ferr != nil {
			r.logger.Error("sidekiq: failed to fail job %s after running-mark failure: %v", job.ID, ferr)
		}
		return
	}

	progress := func(metadata string) error {
		return r.store.UpdateSidekiqJobProgress(r.baseCtx, job.ID, metadata)
	}

	finalMetadata, err := fn(r.baseCtx, job, progress)
	if err != nil {
		r.logger.Error("sidekiq: job %s (%s) failed: %v", job.ID, job.Kind, err)
		if ferr := r.store.FailSidekiqJob(r.baseCtx, job.ID, finalMetadata, err.Error()); ferr != nil {
			r.logger.Error("sidekiq: failed to mark job %s failed: %v", job.ID, ferr)
		}
		return
	}
	if cerr := r.store.CompleteSidekiqJob(r.baseCtx, job.ID, finalMetadata); cerr != nil {
		r.logger.Error("sidekiq: failed to mark job %s completed: %v", job.ID, cerr)
	}
}

// RecoverIncomplete re-runs jobs left pending or running by a previous process
// (a restart or crash). Each handler resumes from the cursor stored in its
// metadata; because per-item work is idempotent, reprocessing the in-flight unit
// is safe. In a multi-node cluster this may double-run a job across nodes, which
// idempotency tolerates; it does not de-duplicate work across nodes by design,
// matching the choice to keep the runner simple (no leader election).
func (r *Runner) RecoverIncomplete(ctx context.Context) error {
	jobs, err := r.store.ListIncompleteSidekiqJobs(ctx)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if _, ok := r.handlerFor(job.Kind); !ok {
			r.logger.Warn("sidekiq: skipping recovery of job %s, no handler for kind %s", job.ID, job.Kind)
			continue
		}
		r.logger.Info("sidekiq: recovering incomplete job %s (%s)", job.ID, job.Kind)
		r.spawn(job)
	}
	return nil
}

// StartReaper periodically marks running jobs whose heartbeat is older than
// staleAfter as failed, catching goroutines or nodes that died without recording a
// terminal state. It returns a stop function. Both interval and staleAfter must be
// positive; staleAfter should comfortably exceed the handler's per-checkpoint time.
func (r *Runner) StartReaper(interval, staleAfter time.Duration) (stop func()) {
	// Guard against invalid durations: time.NewTicker panics on interval <= 0, and
	// a non-positive staleAfter would make every running job look stale. Fall back
	// to the package defaults rather than crashing or reaping live jobs.
	if interval <= 0 {
		interval = ReaperInterval
	}
	if staleAfter <= 0 {
		staleAfter = StaleAfter
	}
	ticker := time.NewTicker(interval)
	done := make(chan struct{})
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-r.baseCtx.Done():
				return
			case <-ticker.C:
				n, err := r.store.MarkStaleSidekiqJobsFailed(r.baseCtx, time.Now().Add(-staleAfter))
				if err != nil {
					r.logger.Error("sidekiq: reaper failed: %v", err)
					continue
				}
				if n > 0 {
					r.logger.Warn("sidekiq: reaper marked %d stale job(s) as failed", n)
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// Shutdown cancels the background context and waits for in-flight goroutines to
// return. In-flight jobs observe baseCtx cancellation and stop at their next
// checkpoint, leaving a resumable cursor in metadata.
func (r *Runner) Shutdown() {
	r.cancel()
	r.wg.Wait()
}
