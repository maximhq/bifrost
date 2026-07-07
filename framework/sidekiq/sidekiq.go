package sidekiq

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// Store is the subset of the configstore the runner needs. Keeping it narrow lets
// the runner be tested with a fake and avoids a hard dependency on the full store.
type Store interface {
	CreateSidekiqJob(ctx context.Context, job *tables.TableSidekiqJob) error
	GetSidekiqJob(ctx context.Context, id string) (*tables.TableSidekiqJob, error)
	// ClaimSidekiqJob atomically claims a job for ownerID; returns true only for the
	// single node that wins. staleBefore is the heartbeat cutoff below which a running
	// job is treated as orphaned and re-claimable.
	ClaimSidekiqJob(ctx context.Context, id, ownerID string, staleBefore time.Time) (bool, error)
	// HeartbeatSidekiqJob bumps the heartbeat for a job still owned by ownerID; returns
	// false when ownership has been lost (the job was reaped and re-claimed).
	HeartbeatSidekiqJob(ctx context.Context, id, ownerID string) (bool, error)
	UpdateSidekiqJobProgress(ctx context.Context, id, ownerID, metadata string) error
	CompleteSidekiqJob(ctx context.Context, id, ownerID, metadata string) error
	FailSidekiqJob(ctx context.Context, id, ownerID, metadata, lastErr string) error
	// ListClaimableSidekiqJobs returns jobs that can be picked up: pending, or running
	// but stale (heartbeat older than staleBefore).
	ListClaimableSidekiqJobs(ctx context.Context, staleBefore time.Time) ([]tables.TableSidekiqJob, error)
}

// ProgressFunc persists a checkpoint: it replaces the job's metadata blob and
// bumps the heartbeat. Handlers call it after each unit of work (e.g. each page).
type ProgressFunc func(metadata string) error

// HandlerFunc processes one job. It is given the job (read its Metadata for the
// resume cursor) and a progress callback to checkpoint after each unit of work.
// It returns the final metadata to persist and an error. A nil error completes the
// job; a non-nil error fails it (the returned metadata is still stored so a later
// resume can read the last cursor). The context is cancelled if this node loses
// ownership of the job (reaped + re-claimed elsewhere) or on shutdown; handlers
// should honour it and stop at their next checkpoint.
type HandlerFunc func(ctx context.Context, job tables.TableSidekiqJob, progress ProgressFunc) (finalMetadata string, err error)

const (
	// DispatchInterval is how often each node scans for claimable jobs.
	DispatchInterval = 30 * time.Second
	// HeartbeatInterval is how often the owner of a running job bumps its heartbeat.
	HeartbeatInterval = 1 * time.Minute
	// StaleAfter is how long a running job may go without a heartbeat before it is
	// considered orphaned and eligible for re-claim. Must comfortably exceed
	// HeartbeatInterval so a live-but-slow node is not reclaimed out from under itself.
	StaleAfter = 15 * time.Minute
	// MaxAttempts caps how many times a job is (re)claimed before it is marked failed
	// permanently, so a poison job cannot loop forever across the cluster.
	MaxAttempts = 5
)

// Runner owns the handler registry and the lifecycle of job goroutines.
type Runner struct {
	store    Store
	logger   schemas.Logger
	handlers map[string]HandlerFunc
	mu       sync.RWMutex

	// ownerID uniquely identifies this runner process for the lifetime of the
	// process. It fences job mutations: only the owner may heartbeat/progress/
	// complete/fail a job it claimed, so a revived stale node cannot stomp a job
	// another node has re-claimed.
	ownerID string
	// staleAfter is the heartbeat cutoff below which a running job is treated as
	// orphaned. Stored atomically (nanoseconds) because StartDispatcher may write it
	// concurrently with execute/dispatchOnce goroutines already reading it (e.g. a
	// job enqueued during the startup window before the dispatcher starts).
	staleAfter        atomic.Int64
	heartbeatInterval time.Duration

	// inflight tracks job IDs currently being processed by a goroutine on this node,
	// so repeated dispatch ticks do not stack up redundant goroutines behind the
	// concurrency semaphore for a job that is already claimed/queued here.
	inflightMu sync.Mutex
	inflight   map[string]struct{}

	baseCtx context.Context
	cancel  context.CancelFunc
	sem     chan struct{}
	wg      sync.WaitGroup
}

// New creates a Runner. maxConcurrent bounds how many job goroutines run at once
// (<=0 defaults to 4). Jobs run on a background context derived here, never on a
// request context, so they outlive the HTTP request that enqueued them. A fresh
// ownerID is generated per process for claim fencing.
func New(store Store, logger schemas.Logger, maxConcurrent int) *Runner {
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runner{
		store:             store,
		logger:            logger,
		handlers:          make(map[string]HandlerFunc),
		ownerID:           uuid.New().String(),
		heartbeatInterval: HeartbeatInterval,
		inflight:          make(map[string]struct{}),
		baseCtx:           ctx,
		cancel:            cancel,
		sem:               make(chan struct{}, maxConcurrent),
	}
	r.staleAfter.Store(int64(StaleAfter))
	return r
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
// immediately while processing continues in the background. The enqueuing node
// spawns the job directly and wins the claim (the row is pending and unowned).
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

// staleBefore returns the heartbeat cutoff: a running job whose updated_at is older
// than this is treated as orphaned and re-claimable.
func (r *Runner) staleBefore() time.Time {
	return time.Now().Add(-time.Duration(r.staleAfter.Load()))
}

// tryMarkInflight records id as being processed on this node. It returns false if
// the id was already in-flight, letting the caller skip spawning a duplicate
// goroutine that would only block on the semaphore, lose the claim, and exit.
func (r *Runner) tryMarkInflight(id string) bool {
	r.inflightMu.Lock()
	defer r.inflightMu.Unlock()
	if _, ok := r.inflight[id]; ok {
		return false
	}
	r.inflight[id] = struct{}{}
	return true
}

// clearInflight removes id from the in-flight set once its goroutine returns.
func (r *Runner) clearInflight(id string) {
	r.inflightMu.Lock()
	delete(r.inflight, id)
	r.inflightMu.Unlock()
}

// spawn runs a job in its own goroutine, bounded by the concurrency semaphore. It
// is a no-op if a goroutine for this job id is already in-flight on this node, so a
// job stuck behind a full semaphore does not accumulate one blocked goroutine per
// dispatch tick.
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

// execute claims the job, and if this node wins the claim, runs its handler and
// records the terminal state. The atomic claim is the cluster-wide mutual
// exclusion: if another node already owns the job (fresh heartbeat), the claim
// returns false and this node quietly does nothing. A panic in the handler is
// recovered and recorded as a failure so one bad job cannot crash the process.
func (r *Runner) execute(job tables.TableSidekiqJob) {
	fn, ok := r.handlerFor(job.Kind)
	if !ok {
		// Defensive: the dispatcher and Enqueue both filter by handlerFor, so this
		// should not happen. Do not claim/fail — another node may have the handler.
		r.logger.Warn("sidekiq: no handler registered for kind %s, skipping job %s", job.Kind, job.ID)
		return
	}

	// Per-job context so the heartbeat can cancel the handler if we lose ownership,
	// and so shutdown propagates. Cancelled on return.
	jobCtx, cancel := context.WithCancel(r.baseCtx)
	defer cancel()

	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("sidekiq: job %s (%s) panicked: %v", job.ID, job.Kind, rec)
			if err := r.store.FailSidekiqJob(r.baseCtx, job.ID, r.ownerID, "", fmt.Sprintf("panic: %v", rec)); err != nil {
				r.logger.Error("sidekiq: failed to mark panicked job %s failed: %v", job.ID, err)
			}
		}
	}()

	claimed, err := r.store.ClaimSidekiqJob(r.baseCtx, job.ID, r.ownerID, r.staleBefore())
	if err != nil {
		r.logger.Error("sidekiq: failed to claim job %s: %v", job.ID, err)
		return
	}
	if !claimed {
		// Another node owns it (fresh heartbeat) — leave it alone.
		return
	}

	// Poison guard: job.Attempts is the count before this claim. Once a job has been
	// attempted MaxAttempts times, stop retrying and mark it failed permanently.
	if job.Attempts >= MaxAttempts {
		r.logger.Warn("sidekiq: job %s (%s) exceeded max attempts (%d), marking failed", job.ID, job.Kind, MaxAttempts)
		if ferr := r.store.FailSidekiqJob(r.baseCtx, job.ID, r.ownerID, job.Metadata, fmt.Sprintf("exceeded max attempts (%d)", MaxAttempts)); ferr != nil {
			r.logger.Error("sidekiq: failed to fail exhausted job %s: %v", job.ID, ferr)
		}
		return
	}

	stopHeartbeat := r.startHeartbeat(jobCtx, cancel, job.ID)
	defer stopHeartbeat()

	progress := func(metadata string) error {
		return r.store.UpdateSidekiqJobProgress(r.baseCtx, job.ID, r.ownerID, metadata)
	}

	finalMetadata, err := fn(jobCtx, job, progress)
	if err != nil {
		r.logger.Error("sidekiq: job %s (%s) failed: %v", job.ID, job.Kind, err)
		if ferr := r.store.FailSidekiqJob(r.baseCtx, job.ID, r.ownerID, finalMetadata, err.Error()); ferr != nil {
			r.logger.Error("sidekiq: failed to mark job %s failed: %v", job.ID, ferr)
		}
		return
	}
	if cerr := r.store.CompleteSidekiqJob(r.baseCtx, job.ID, r.ownerID, finalMetadata); cerr != nil {
		r.logger.Error("sidekiq: failed to mark job %s completed: %v", job.ID, cerr)
	}
}

// startHeartbeat runs a ticker that periodically bumps the job's heartbeat so a
// slow-but-alive job is not judged stale and re-claimed elsewhere. If the heartbeat
// reports lost ownership (the job was reaped and re-claimed on another node), it
// cancels the job context so the handler stops. Returns a stop function.
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
				ok, err := r.store.HeartbeatSidekiqJob(r.baseCtx, id, r.ownerID)
				if err != nil {
					// Transient DB error: log and keep the job running; the reaper's
					// stale window is much larger than one missed beat.
					r.logger.Error("sidekiq: heartbeat for job %s failed: %v", id, err)
					continue
				}
				if !ok {
					r.logger.Warn("sidekiq: lost ownership of job %s, cancelling in-flight work", id)
					cancel()
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// StartDispatcher periodically scans for claimable jobs (pending, or running but
// stale) and attempts to run each; the atomic claim inside execute ensures exactly
// one node in the cluster actually runs a given job. This subsumes both startup
// recovery (jobs left behind by a crash or restart) and ongoing pickup of jobs
// orphaned by a node that died mid-run — without leader election. It runs one scan
// immediately, then every interval. Returns a stop function.
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

// dispatchOnce lists claimable jobs and spawns those with a registered handler.
// Each spawned goroutine attempts an atomic claim; only the winner runs, so it is
// safe for every node to dispatch the same list concurrently.
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
		r.spawn(job)
	}
}

// Shutdown cancels the background context and waits for in-flight goroutines to
// return. In-flight jobs observe context cancellation and stop at their next
// checkpoint, leaving a resumable cursor in metadata.
func (r *Runner) Shutdown() {
	r.cancel()
	r.wg.Wait()
}
