package evals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/queue"
)

var (
	// ErrNoEvalKey is returned when an evaluator needs a model call and no provider key
	// is enabled for evaluations on that provider and model.
	ErrNoEvalKey = errors.New("no provider key is enabled for evaluations")
	// ErrJobNotFound is returned by a JobStore for a job that does not exist.
	ErrJobNotFound = errors.New("evaluation job not found")
	// ErrPlacementUnknown matches a Submit or Resume error whose queue publish failed in a
	// way that may still have stored the message. The job must stay queued: the message
	// runs if it exists, and the sweep publishes the job again if it does not.
	ErrPlacementUnknown = errors.New("the evaluation may or may not have been queued")
)

// Queue names. Every evaluation is a message: evaluators that call a model wait on the
// topic of the provider key they will use, and the rest share one local topic.
const (
	runnerGroup      = "evals"
	runnerLocalTopic = "evals.local"
	// runnerDeadTopic receives a job message that failed every attempt; its handler ends
	// the job with an error result.
	runnerDeadTopic = "evals.dead"
	runnerKeyTopic  = "evals.key."
)

// Runner defaults.
const (
	// DefaultEvalKeyConcurrency applies to an eval key that sets no concurrency.
	DefaultEvalKeyConcurrency = 4
	// MaxEvalKeyConcurrency bounds an eval key's concurrency. Each slot starts a queue
	// worker on every instance, so a typo must not start thousands of them.
	MaxEvalKeyConcurrency     = 256
	defaultLocalConcurrency   = 10
	defaultRunnerMaxAttempts  = 3
	defaultRunnerSyncInterval = 30 * time.Second
	// A queued job is only looked at by the sweep once it is this old. Younger jobs are
	// left to the handler of the topic they are on.
	defaultRunnerStrandedAfter = 2 * time.Minute
	// A job routed to a key that is being consumed counts as having lost its message once
	// it was routed this many StrandedAfter periods ago. A long backlog on a busy key gets
	// one extra message per routing, which the claim then drops.
	runnerLostMessageFactor = 10
	// DefaultExecutionRetention is how long a completed job's result is kept.
	DefaultExecutionRetention  = 7 * 24 * time.Hour
	runnerPurgeInterval        = time.Hour
	runnerSweepBatch           = 200
	defaultRunnerCloseDeadline = 30 * time.Second
	// completeWriteTimeout bounds the write of a finished job's result.
	completeWriteTimeout = 5 * time.Second
)

// EvalKey is a provider key that is enabled for evaluations.
type EvalKey struct {
	ID          string
	Name        string // the key's display name; never the secret
	Provider    string
	Concurrency int                     // max evaluations in flight on this key; across instances only on a shared queue
	Supports    func(model string) bool // nil means every model
}

// KeySource returns the provider keys currently enabled for evaluations.
type KeySource func(ctx context.Context) ([]EvalKey, error)

// Run runs one evaluator on one input in place, with the runner's engine. A job that
// brings its own work (Exec) uses it to call the evaluator once it has checked the job
// is still its own to run.
func (r *Runner) Run(ctx context.Context, def Definition, input Input) Result {
	return r.engine.Run(ctx, def, input)
}

// Keys returns the provider keys currently enabled for evaluations, read from the source.
func (r *Runner) Keys(ctx context.Context) ([]EvalKey, error) {
	return r.keys(ctx)
}

// ModelRef names the model a job calls, which decides the eval key it waits on.
type ModelRef struct {
	Provider string
	Model    string
}

// Job is one unit of work for the runner. Most jobs are one evaluator run on one input:
// callers that submit one set ID, Definition and Input, and the store fills in every
// other field when it loads the job. A job can instead bring its own work in Exec, with
// the model it calls in Model: a test run uses this to send a target's model call through
// the same per-key queues, so every model call an evaluation makes is under the same
// concurrency cap. The store sets Exec when it loads such a job, since a job is sent
// through the queue by ID only; Exec gets a context that carries the eval key it runs on.
// A nil Model runs the job on the local topic. KeyID is "" until the job is routed to a
// key's queue. A started job cannot be moved to another key's queue. ClaimID and
// StartedAt are filled in only for a started job. Complete, when a store sets it,
// replaces JobStore.CompleteJob for this job, so a store that already holds what
// completing it needs does not read the job a second time; an error it returns has the
// job delivered again.
type Job struct {
	ID         string
	Definition Definition
	Input      Input
	Model      *ModelRef                                      // the model an Exec job calls; nil runs on the local topic
	Exec       func(ctx context.Context) Result               // the job's own work, run in place of the evaluator
	KeyID      string                                         // the key whose queue the job waits on, "" for none
	Done       bool                                           // the job already has a result
	Started    bool                                           // a worker began the job and it has no result yet
	RoutedAt   time.Time                                      // when the job was last put on a key's queue
	CreatedAt  time.Time                                      // when the job was stored
	ClaimID    string                                         // the queue delivery that started the job
	StartedAt  time.Time                                      // when the job started
	Complete   func(ctx context.Context, result Result) error // stores the result in place of CompleteJob
}

// JobStore persists jobs. The runner sends only a job's ID through the queue, so inputs
// of any size stay out of queue messages.
type JobStore interface {
	// LoadJob returns the job, or ErrJobNotFound.
	LoadJob(ctx context.Context, id string) (*Job, error)
	// StartJob claims the job for the delivery about to run it, on keyID ("" for none),
	// and reports whether the claim held. See JobClaim.
	StartJob(ctx context.Context, id, keyID string, claim JobClaim) (bool, error)
	// CompleteJob stores the result. Delivery is at least once, so it must tolerate
	// being called again for a job that already completed.
	CompleteJob(ctx context.Context, id string, result Result) error
	// CompleteQueuedJob stores the result of a job that has not started and is still
	// queued on keyID ("" for one not routed yet), and reports whether it did. A job that
	// moved to another key or started since it was read is left as it is, and so is one
	// that already completed; neither is an error.
	CompleteQueuedJob(ctx context.Context, id, keyID string, result Result) (bool, error)
	// RouteJob records that a waiting job moved from one key's queue to another's ("" for
	// no key). It must be a claim: it moves the job only if it has not started and is
	// still on fromKey, and reports whether it did.
	RouteJob(ctx context.Context, id, fromKey, toKey string, routedAt time.Time) (bool, error)
	// StaleJobs returns jobs queued before the cutoff that have not started, at most
	// limit of them. skipKeys are the keys that are being consumed: a job waiting on
	// one of them is in no trouble, and a store should leave it out, so that a long
	// line of healthy jobs cannot hide the stranded ones behind it.
	//
	// routedBefore brings back a job on one of skipKeys anyway once it was put on that
	// key's queue before the cutoff: its message may have been lost between routing and
	// publishing.
	//
	// routedBefore is also the lost-claim cutoff for started jobs: a job that started
	// before it, on a key that is not one of skipKeys ("" excluded), has no result and is
	// returned too, with Started, ClaimID and StartedAt filled. Nothing consumes that
	// key's topic, so the sweep ends it.
	//
	// It returns one page, oldest first, of the rows after the cursor, and the cursor of
	// the next page. The cursor is built from the last row read, decodable or not, and is
	// the zero cursor on the last page. The sweep keeps it from one sweep to the next, so
	// rows it cannot act on never fill every page and hide the newer ones behind them.
	//
	// A page may come with an error: the jobs and the cursor returned with it are still
	// used, and the error is reported.
	StaleJobs(ctx context.Context, queuedBefore, routedBefore time.Time, skipKeys []string, after JobCursor, limit int) ([]*Job, JobCursor, error)
	// PurgeJobs deletes jobs that completed before the cutoff.
	PurgeJobs(ctx context.Context, completedBefore time.Time) (int64, error)
}

// RunnerOptions tunes the runner. Zero values take the defaults. A key marked on one
// instance is picked up by the others within SyncInterval. A negative Retention keeps
// results forever. Volatile is for a queue that forgets its messages when the gateway
// stops, as the in-memory queue does: the jobs stay stored as queued or running, so on
// Start the runner puts every unfinished job back on the queue, which needs a JobStore
// that is also a JobResumer.
type RunnerOptions struct {
	LocalConcurrency int            // evaluations without model calls running at once, per instance
	PollInterval     time.Duration  // queue poll interval; zero takes the queue default
	MaxAttempts      int            // deliveries before a job's message is dead-lettered
	SyncInterval     time.Duration  // how often subscriptions are reconciled with the eval keys
	StrandedAfter    time.Duration  // how long a job sits queued before the sweep checks its key
	Retention        time.Duration  // how long a completed job's result is kept
	Volatile         bool           // the queue forgets its messages when the gateway stops
	Logger           schemas.Logger // optional; warns about failures no caller sees, such as an uncertain publish
}

// JobClaim is what a delivery claims a job with. A job is claimed when it is queued on
// the delivery's key, or when it is running under the same delivery: the queue
// redelivers a message whose worker died, and that redelivery runs the job again. A
// second message for the same job, from a retried publish or a sweep, is a different
// delivery and is refused, so the job runs once. StartedBefore also lets a delivery take
// over a job that started before that time: a runner on a queue that forgets sets it to
// its own start, since any job still running from before then belongs to a worker of a
// gateway that has stopped. RecoverStartedAt, when set, makes the claim one that only
// takes back a running job from the very start it names: the job must still be running
// under DeliveryID with that start time, read from the store. The sweep ends a lost job
// this way, so a job restarted since it was listed, by any delivery, the same one
// included, is left alone. Such a claim leaves the start time as it was, so a completion
// that fails after it is retried by the next sweep. StartedBefore is not used with it.
type JobClaim struct {
	DeliveryID       string    // stable across redeliveries of one message
	StartedBefore    time.Time // a job started before this may be taken over
	RecoverStartedAt time.Time // set: only take back a job still running under DeliveryID from this exact start
}

// JobCursor marks a place in the listing of unfinished jobs, or of stale jobs.
type JobCursor struct {
	CreatedAt time.Time
	ID        string
}

// JobResumer is implemented by a JobStore that can list the jobs a queue lost.
type JobResumer interface {
	// UnfinishedJobs returns a page of the jobs that are queued or were started and have
	// no result, oldest first, after the cursor, at most limit of them, and the cursor of
	// the next page. The cursor is built from the last row read, decodable or not, and is
	// the zero cursor on the last page. It is only read when the gateway starts on a
	// queue that forgets.
	UnfinishedJobs(ctx context.Context, after JobCursor, limit int) ([]*Job, JobCursor, error)
}

type keyContext struct{}

// WithKeyID marks ctx with the provider key an evaluation must use for its model calls.
func WithKeyID(ctx context.Context, keyID string) context.Context {
	if keyID == "" {
		return ctx
	}
	return context.WithValue(ctx, keyContext{}, keyID)
}

// KeyIDFromContext returns the provider key an evaluation must use, or "".
func KeyIDFromContext(ctx context.Context) string {
	keyID, _ := ctx.Value(keyContext{}).(string)
	return keyID
}

type keySubscription struct {
	sub         queue.Subscription
	concurrency int
}

// Runner is the single path every evaluation takes: a job is published to a queue and a
// consumer runs it. A provider key's eval concurrency is enforced by the queue as the
// in-flight cap of that key's topic. It holds across every instance on a shared queue
// (a Postgres or ClickHouse log store) and per instance on a SQLite or in-memory queue,
// which only one process consumes. Each sweep reads
// sweepBatch stale jobs from sweepAfter, the page after the last one, and wraps to the
// oldest row after the last page.
type Runner struct {
	queue  queue.Queue
	engine *Engine
	keys   KeySource
	store  JobStore
	opts   RunnerOptions

	mu       sync.Mutex
	eligible map[string]EvalKey         // keys currently enabled for evaluations, by ID
	subs     map[string]keySubscription // key topics this instance consumes, by key ID
	local    queue.Subscription
	dead     queue.Subscription
	waiters  map[string][]chan struct{}
	closed   bool
	loopCtx  context.Context    // the periodic sync runs under it; Close cancels it
	stopLoop context.CancelFunc // cancels loopCtx
	loopDone sync.WaitGroup     // the periodic sync goroutine

	lastPurge   time.Time // when this instance last removed old results
	startedAt   time.Time // when Start ran; on a queue that forgets, older running jobs are taken over
	pending     []string  // IDs of jobs whose resume failed, retried on each Sync
	resumeAfter JobCursor // where an interrupted resume walk goes on
	resumeOpen  bool      // the resume walk stopped on a failed listing and is not finished
	sweepBatch  int       // how many stale jobs one sweep reads
	sweepAfter  JobCursor // where the next sweep resumes
}

// NewRunner returns a runner. Call Start before submitting jobs.
func NewRunner(q queue.Queue, engine *Engine, keys KeySource, store JobStore, opts RunnerOptions) *Runner {
	if opts.LocalConcurrency <= 0 {
		opts.LocalConcurrency = defaultLocalConcurrency
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = defaultRunnerMaxAttempts
	}
	if opts.SyncInterval <= 0 {
		opts.SyncInterval = defaultRunnerSyncInterval
	}
	if opts.StrandedAfter <= 0 {
		opts.StrandedAfter = defaultRunnerStrandedAfter
	}
	if opts.Retention == 0 {
		opts.Retention = DefaultExecutionRetention
	}
	loopCtx, stopLoop := context.WithCancel(context.Background())
	return &Runner{
		queue:    q,
		engine:   engine,
		keys:     keys,
		store:    store,
		opts:     opts,
		eligible: make(map[string]EvalKey),
		subs:     make(map[string]keySubscription),
		waiters:  make(map[string][]chan struct{}),
		loopCtx:  loopCtx,
		stopLoop: stopLoop,

		sweepBatch: runnerSweepBatch,
	}
}

// Start begins consuming the local topic and the topic of every eval key. A failure on
// the local or dead topic fails Start; a key whose subscribe fails is logged and
// subscribed by the next sync, and so is a failed listing of the jobs to resume. On a queue
// that forgets, the stored jobs are put back before the first sweep: the sweep may route
// a stranded job and publish it, and resume would then publish it a second time.
func (r *Runner) Start(ctx context.Context) error {
	r.mu.Lock()
	r.startedAt = time.Now()
	r.mu.Unlock()
	local, err := r.subscribe(ctx, runnerLocalTopic, r.handler(""), r.subscribeOptions(r.opts.LocalConcurrency, 0)...)
	if err != nil {
		return fmt.Errorf("failed to subscribe to the local evaluation topic: %w", err)
	}
	dead, err := r.subscribe(ctx, runnerDeadTopic, r.deadHandler(), r.deadOptions()...)
	if err != nil {
		_ = local.Close(ctx)
		return fmt.Errorf("failed to subscribe to the dead evaluation topic: %w", err)
	}
	r.mu.Lock()
	r.local = local
	r.dead = dead
	r.mu.Unlock()
	// The runner works without a key's consumer, but not without the local and dead
	// topics. A key that cannot be subscribed now is subscribed by the next sync, which
	// calls subscribeEligible on every tick.
	if err := r.subscribeEligible(ctx); err != nil {
		r.warn("evals: subscribing to the eval keys failed, it is retried on the next sync: %v", err)
	}
	// A failed listing must not keep the runner from starting either: the walk stops at
	// the page that failed and the next sync goes on from there.
	if r.opts.Volatile {
		if err := r.resume(ctx); err != nil {
			r.warn("evals: listing the jobs to resume failed, it is retried on the next sync: %v", err)
		}
	}
	// A failed sweep must not keep the runner from starting: one job whose recovery fails
	// would otherwise disable every evaluation until a restart. The periodic sync sweeps
	// again, and the jobs that failed are retried then.
	if err := r.Sweep(ctx); err != nil {
		r.warn("evals: the startup sweep failed, it is retried on the next sync: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.loopDone.Add(1)
	go r.syncLoop()
	return nil
}

// errKeyGone ends a job that had started on a key that is no longer an eval key. A
// started job cannot be moved to another key's queue, so it cannot be run again.
var errKeyGone = fmt.Errorf("%w: the key this evaluation started on is no longer enabled for evaluations", ErrNoEvalKey)

// resume puts every unfinished job back on a queue that forgot its messages. It runs
// once, on Start, before the first sweep, and reads the jobs a page at a time. The ID of
// a job whose resume fails is kept and the job retried on each Sync, so it is not left
// waiting with nothing on any queue. Only the ID is kept: during a queue outage every
// unfinished job fails, and their definitions and inputs must not all be held in memory.
// A listing that fails stops the walk and keeps its cursor; continueResume goes on from
// there on each Sync until the walk ends, so pages already resumed are not read again.
func (r *Runner) resume(ctx context.Context) error {
	return r.resumeFrom(ctx, JobCursor{})
}

// continueResume goes on with a resume walk that a failed listing stopped. It costs one
// locked bool read on a Sync with nothing to continue.
func (r *Runner) continueResume(ctx context.Context) error {
	r.mu.Lock()
	open, after := r.resumeOpen, r.resumeAfter
	r.mu.Unlock()
	if !open {
		return nil
	}
	return r.resumeFrom(ctx, after)
}

func (r *Runner) resumeFrom(ctx context.Context, after JobCursor) error {
	resumer, ok := r.store.(JobResumer)
	if !ok {
		return nil
	}
	for {
		// A canceled start ends the walk even when the store does not watch ctx.
		if err := ctx.Err(); err != nil {
			return err
		}
		jobs, next, err := resumer.UnfinishedJobs(ctx, after, runnerSweepBatch)
		if err != nil {
			r.mu.Lock()
			r.resumeAfter, r.resumeOpen = after, true
			r.mu.Unlock()
			return fmt.Errorf("failed to list the evaluation jobs to resume: %w", err)
		}
		for _, job := range jobs {
			// One job that cannot be resumed must not keep the others from running.
			if err := r.resumeJob(ctx, job); err != nil {
				r.mu.Lock()
				r.pending = append(r.pending, job.ID)
				r.mu.Unlock()
			}
		}
		// The cursor, not the page length, ends the walk: a store leaves out a row it
		// cannot decode, and the page it was on is short without being the last.
		if next == (JobCursor{}) {
			r.mu.Lock()
			r.resumeAfter, r.resumeOpen = JobCursor{}, false
			r.mu.Unlock()
			return nil
		}
		after = next
	}
}

// retryResume resumes again the jobs whose resume failed. Each is read from the store
// first, so its current route and state are used: a job that is gone or already done is
// dropped, and one whose read fails is kept for the next Sync.
func (r *Runner) retryResume(ctx context.Context) {
	r.mu.Lock()
	ids := r.pending
	r.pending = nil
	r.mu.Unlock()
	var failed []string
	for _, id := range ids {
		job, err := r.store.LoadJob(ctx, id)
		if errors.Is(err, ErrJobNotFound) {
			continue
		}
		if err == nil && job.Done {
			continue
		}
		if err != nil || r.resumeJob(ctx, job) != nil {
			failed = append(failed, id)
		}
	}
	if len(failed) > 0 {
		r.mu.Lock()
		r.pending = append(r.pending, failed...)
		r.mu.Unlock()
	}
}

// Resume puts back on the queue a job that a queue which forgets its messages lost.
// Unlike Submit, it starts from where the store says the job is: on the queue of
// job.KeyID, and started if job.Started. A job routed to a key that is still eligible
// goes back to that key's queue; one routed to a key that is gone moves to another, or
// ends with an error when it had already started.
func (r *Runner) Resume(ctx context.Context, job *Job) error {
	if job == nil || job.ID == "" {
		return errors.New("an evaluation job needs an ID")
	}
	return r.resumeJob(ctx, job)
}

func (r *Runner) resumeJob(ctx context.Context, job *Job) error {
	_, _, needsModel := r.modelOf(job)
	switch {
	case job.KeyID != "" && r.eligibleServes(job.KeyID, job):
		// Back on the queue it was on. A job that had started runs again from the top.
		return r.send(ctx, job.ID, keyTopic(job.KeyID), job.KeyID)
	case job.KeyID == "" && !needsModel:
		return r.send(ctx, job.ID, runnerLocalTopic, "")
	case job.Started:
		return r.complete(ctx, job, errorResult(errKeyGone))
	default:
		return r.reroute(ctx, job, job.KeyID)
	}
}

// republish puts a job routed long ago back on its key's queue, unless the delivery of
// its current route is still there: a job waiting behind a long backlog keeps that one,
// and the claim would only drop a copy. When the route's delivery is gone (acknowledged
// without a result, dead-lettered, or never stored) a new one is needed, and since the
// queue ignores a second publish of a message ID it still holds, the job gets a new
// route time and its message a new ID. That time is truncated to StrandedAfter, so
// instances sweeping in the same period publish the same message, which the queue keeps
// once. A failed publish leaves the route as it was, for the next sweep.
func (r *Runner) republish(ctx context.Context, job *Job) error {
	topic := keyTopic(job.KeyID)
	state, err := r.queue.Delivery(ctx, topic, queue.DeliveryID(routeMessageID(job.ID, job.RoutedAt), runnerGroup))
	if err != nil && !errors.Is(err, queue.ErrUnsupported) {
		return fmt.Errorf("failed to read the delivery of a waiting evaluation job: %w", err)
	}
	if err == nil && (state.Status == queue.DeliveryPending || state.Status == queue.DeliveryLeased) {
		return nil
	}
	payload, err := json.Marshal(jobMessage{JobID: job.ID})
	if err != nil {
		return err
	}
	routedAt := time.Now().Truncate(r.opts.StrandedAfter)
	message := &queue.Message{
		ID:      routeMessageID(job.ID, routedAt),
		Topic:   topic,
		Payload: payload,
		Headers: map[string]string{"key_id": job.KeyID},
	}
	if err := r.queue.Publish(ctx, message); err != nil {
		return err
	}
	_, err = r.store.RouteJob(ctx, job.ID, job.KeyID, job.KeyID, routedAt)
	return err
}

// routeMessageID is the message ID of a job's route to a key made at routedAt. Every
// keyed publish uses it, so the sweep can ask the queue about the route's delivery.
func routeMessageID(jobID string, routedAt time.Time) string {
	return fmt.Sprintf("evaljob:%s:route:%d", jobID, routedAt.UnixNano())
}

// publishLocal publishes a job on the local topic under a given message ID.
func (r *Runner) publishLocal(ctx context.Context, jobID, messageID string) error {
	payload, err := json.Marshal(jobMessage{JobID: jobID})
	if err != nil {
		return err
	}
	return r.queue.Publish(ctx, &queue.Message{ID: messageID, Topic: runnerLocalTopic, Payload: payload})
}

// send publishes a message for a job on a topic, under a new message ID.
func (r *Runner) send(ctx context.Context, jobID, topic, keyID string) error {
	payload, err := json.Marshal(jobMessage{JobID: jobID})
	if err != nil {
		return err
	}
	message := &queue.Message{ID: "evaljob:" + jobID + ":" + uuid.NewString(), Topic: topic, Payload: payload}
	if keyID != "" {
		message.Headers = map[string]string{"key_id": keyID}
	}
	return r.queue.Publish(ctx, message)
}

// syncLoop reconciles subscriptions on a timer. Without it, an instance would only
// start consuming a newly marked key once it submitted a job to that key itself, and
// jobs queued by other instances would wait on them alone.
//
// Each sync runs under the loop context, which Close cancels, so a key source or store
// that stops answering does not outlive the runner.
func (r *Runner) syncLoop() {
	defer r.loopDone.Done()
	ticker := time.NewTicker(r.opts.SyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.loopCtx.Done():
			return
		case <-ticker.C:
			// A failed sync is retried on the next tick; the current subscriptions stay.
			_ = r.Sync(r.loopCtx)
		}
	}
}

// Sync reconciles this instance's subscriptions with the current eval keys. Call it
// when provider keys change. A key that is no longer eligible keeps its subscription
// while its topic still holds messages, which from then on only move that key's queued
// jobs to a key that is. Once the topic is drained the subscription is closed.
//
// A failed subscribe does not stop the rest: the retirement, the rest of an interrupted
// resume walk, the resume retry and the sweep run whatever happened, and every failure
// is returned together.
func (r *Runner) Sync(ctx context.Context) error {
	subscribeErr := r.subscribeEligible(ctx)
	r.retireDrained(ctx)
	resumeErr := r.continueResume(ctx)
	r.retryResume(ctx)
	return errors.Join(subscribeErr, resumeErr, r.Sweep(ctx))
}

// retireDrained closes the subscription of each key that is no longer eligible once
// nothing is pending or leased on its topic, so replacing keys does not leave idle
// consumers behind. A key that comes back is subscribed again by ensureSubscribed. A
// message published to the topic later, by an instance with a stale key list, belongs
// to a queued job on a key nothing consumes, which the sweep moves. A failed Stats keeps
// the subscription for the next Sync. Close stops the subscription even when it reports
// an error (a lease it could not release simply expires), so the subscription is forgotten
// before Close, whatever Close returns: keeping a stopped one would leave the key with no
// consumer here if it comes back at the same concurrency.
func (r *Runner) retireDrained(ctx context.Context) {
	r.mu.Lock()
	retired := make(map[string]queue.Subscription)
	for keyID, current := range r.subs {
		if _, ok := r.eligible[keyID]; !ok {
			retired[keyID] = current.sub
		}
	}
	r.mu.Unlock()
	for keyID, sub := range retired {
		stats, err := r.queue.Stats(ctx, keyTopic(keyID), runnerGroup)
		if err != nil || stats.Pending+stats.Leased > 0 {
			continue
		}
		// Only the subscription that was checked, and only while the key is still not
		// eligible: a Sync or publish may have replaced it or marked the key again. It is
		// forgotten in the same locked section, before Close: a publish that marks the key
		// again after this finds no subscription and installs a fresh one, instead of
		// reusing the one about to stop.
		r.mu.Lock()
		current, ok := r.subs[keyID]
		_, eligible := r.eligible[keyID]
		retire := ok && current.sub == sub && !eligible
		if retire {
			delete(r.subs, keyID)
		}
		r.mu.Unlock()
		if !retire {
			continue
		}
		closeCtx, cancel := context.WithTimeout(ctx, defaultRunnerCloseDeadline)
		err = sub.Close(closeCtx)
		cancel()
		if err != nil {
			r.warn("evals: closing the subscription of key %s reported an error, its leases expire on their own: %v", keyID, err)
		}
	}
}

// subscribeEligible reloads the eval keys and consumes the topic of each. A key whose
// subscribe fails does not keep the others from being subscribed; the failures are
// returned together.
func (r *Runner) subscribeEligible(ctx context.Context) error {
	eligible, err := r.refresh(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, key := range eligible {
		if err := r.ensureSubscribed(ctx, key, true); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Sweep does the housekeeping no job handler can do. It moves jobs that are stranded:
// queued on the topic of a key that is no longer eligible, which nothing consumes when
// the key was removed while every gateway was down. It also publishes jobs that were
// stored but never reached a queue, and removes results past their retention.
//
// It is safe to run on every instance at once. Moving a job is a claim in the store, so
// one instance wins and the others skip the job.
func (r *Runner) Sweep(ctx context.Context) error {
	eligible, err := r.refresh(ctx)
	if err != nil {
		return err
	}
	consumed := make([]string, 0, len(eligible))
	for keyID := range eligible {
		consumed = append(consumed, keyID)
	}
	lostBefore := time.Now().Add(-runnerLostMessageFactor * r.opts.StrandedAfter)
	r.mu.Lock()
	batch, after := r.sweepBatch, r.sweepAfter
	r.mu.Unlock()
	stale, next, listErr := r.store.StaleJobs(ctx, time.Now().Add(-r.opts.StrandedAfter), lostBefore, consumed, after, batch)
	if listErr != nil && len(stale) == 0 && next == (JobCursor{}) {
		return fmt.Errorf("failed to list stale evaluation jobs: %w", listErr)
	}
	// The next sweep reads the page after this one, whatever happens to its jobs: a job
	// whose recovery fails here is retried on the next lap, not on every sweep. A page
	// that came with an error is still processed and its cursor kept.
	r.mu.Lock()
	r.sweepAfter = next
	r.mu.Unlock()
	// One job that cannot be recovered must not keep the rest of the batch, or the purge,
	// from running. The failures are reported together at the end.
	var errs []error
	if listErr != nil {
		errs = append(errs, fmt.Errorf("failed to list stale evaluation jobs: %w", listErr))
	}
	for _, job := range stale {
		if job.Started {
			// Started on a key nothing consumes any more, with its claim long expired. On a
			// queue that keeps its messages nothing else would ever end it. A job held under
			// the sweep's own claim after a failed write is ended even on a key that is
			// eligible again: no delivery has that claim.
			if job.KeyID != "" && (!r.isEligible(job.KeyID) || job.ClaimID == configstore.EvalEndLostClaim) {
				if err := r.endLostJob(ctx, job); err != nil {
					errs = append(errs, err)
				}
			}
			continue
		}
		if job.KeyID == "" {
			// Never routed. A job that needs no model sits on the local topic by design; one
			// that needs a model was stored and then never published.
			if _, _, needsModel := r.modelOf(job); !needsModel {
				// It sits on the local topic by design, unless it was stored and never published
				// (the gateway stopped in between). Publishing Submit's message ID again is a no-op
				// while that message exists, so only a lost one is replaced.
				if !job.CreatedAt.IsZero() && job.CreatedAt.Before(lostBefore) {
					if err := r.publishLocal(ctx, job.ID, "evaljob:"+job.ID); err != nil {
						errs = append(errs, err)
					}
				}
				continue
			}
		} else if r.isEligible(job.KeyID) {
			// Waiting its turn on a key that is being consumed, unless it was routed there
			// so long ago that its message was lost between routing and publishing.
			if !job.RoutedAt.IsZero() && job.RoutedAt.Before(lostBefore) {
				if err := r.republish(ctx, job); err != nil {
					errs = append(errs, err)
				}
			}
			continue
		}
		if err := r.reroute(ctx, job, job.KeyID); err != nil {
			errs = append(errs, err)
		}
	}
	if err := r.purge(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// endLostJob ends, with an error, a job that started on a key that is no longer an eval
// key and whose claim is long expired. It first claims the job as the start it was listed
// with: the delivery that started it and the exact start time. A job restarted since then
// (the key came back and another delivery took it, or the queue redelivered the same
// message) belongs to that start and is left alone.
//
// The job's age is no proof its worker is gone: the queue renews a running delivery's
// lease apart from the job row. So while the delivery that started the job still holds
// a lease with time left, the job is left to it. The sweep's own claim has no delivery
// to ask about.
func (r *Runner) endLostJob(ctx context.Context, job *Job) error {
	if job.ClaimID != "" && job.ClaimID != configstore.EvalEndLostClaim {
		state, err := r.queue.Delivery(ctx, keyTopic(job.KeyID), job.ClaimID)
		if err != nil && !errors.Is(err, queue.ErrUnsupported) {
			return fmt.Errorf("failed to read the delivery of a lost evaluation job: %w", err)
		}
		if err == nil && state.Status == queue.DeliveryLeased && state.LeaseRemaining > 0 {
			return nil
		}
	}
	claim := JobClaim{DeliveryID: job.ClaimID, RecoverStartedAt: job.StartedAt}
	if job.StartedAt.IsZero() {
		// No start time was stored, so there is none to fence on: only the delivery is matched.
		claim = JobClaim{DeliveryID: job.ClaimID}
	}
	claimed, err := r.store.StartJob(ctx, job.ID, job.KeyID, claim)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	return r.complete(ctx, job, errorResult(errKeyGone))
}

// warn logs a failure no caller sees, when the runner has a logger.
func (r *Runner) warn(format string, args ...any) {
	if r.opts.Logger != nil {
		r.opts.Logger.Warn(format, args...)
	}
}

// purge removes results past their retention, at most once per purge interval.
func (r *Runner) purge(ctx context.Context) error {
	if r.opts.Retention < 0 {
		return nil
	}
	r.mu.Lock()
	due := time.Since(r.lastPurge) >= runnerPurgeInterval
	if due {
		r.lastPurge = time.Now()
	}
	r.mu.Unlock()
	if !due {
		return nil
	}
	if _, err := r.store.PurgeJobs(ctx, time.Now().Add(-r.opts.Retention)); err != nil {
		return fmt.Errorf("failed to purge old evaluation results: %w", err)
	}
	return nil
}

// refresh reloads the eligible keys without touching subscriptions. Job handlers use it
// rather than Sync, which may close a subscription and wait for the very handler that
// called it.
func (r *Runner) refresh(ctx context.Context) (map[string]EvalKey, error) {
	keys, err := r.keys(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list evaluation keys: %w", err)
	}
	eligible := make(map[string]EvalKey, len(keys))
	for _, key := range keys {
		if key.ID == "" {
			continue
		}
		if key.Concurrency <= 0 {
			key.Concurrency = DefaultEvalKeyConcurrency
		}
		eligible[key.ID] = key
	}
	r.mu.Lock()
	r.eligible = eligible
	r.mu.Unlock()
	return eligible, nil
}

// ensureSubscribed consumes a key's topic at the key's concurrency. With resubscribe it
// also replaces a subscription whose concurrency is out of date, which closes the old
// one and so must never run inside a job handler.
func (r *Runner) ensureSubscribed(ctx context.Context, key EvalKey, resubscribe bool) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return queue.ErrClosed
	}
	current, exists := r.subs[key.ID]
	r.mu.Unlock()
	if exists && (current.concurrency == key.Concurrency || !resubscribe) {
		return nil
	}
	if exists {
		// Leases the old subscription still holds are released and redelivered.
		closeCtx, cancel := context.WithTimeout(ctx, defaultRunnerCloseDeadline)
		_ = current.sub.Close(closeCtx)
		cancel()
	}
	sub, err := r.subscribe(ctx, keyTopic(key.ID), r.handler(key.ID), r.subscribeOptions(key.Concurrency, key.Concurrency)...)
	if err != nil {
		if exists {
			// The closed subscription must not stay recorded as the key's consumer, or the
			// next Sync or publish would find it current and never subscribe again. One that
			// another caller installed in the meantime is kept.
			r.mu.Lock()
			if installed, ok := r.subs[key.ID]; ok && installed.sub == current.sub {
				delete(r.subs, key.ID)
			}
			r.mu.Unlock()
		}
		return fmt.Errorf("failed to subscribe to the evaluation topic of key %s: %w", key.ID, err)
	}
	// The map was not locked while subscribing, so another caller may have installed
	// a subscription for this key in the meantime. One of the two has to go: a
	// subscription that is not in the map consumes the topic and is never closed.
	r.mu.Lock()
	installed, raced := r.subs[key.ID]
	raced = raced && installed.sub != current.sub
	keepTheirs := raced && (r.closed || installed.concurrency == key.Concurrency || !resubscribe)
	if !keepTheirs && !r.closed {
		r.subs[key.ID] = keySubscription{sub: sub, concurrency: key.Concurrency}
	}
	closed := r.closed
	r.mu.Unlock()

	surplus := sub
	switch {
	case closed || keepTheirs:
	case raced:
		surplus = installed.sub // ours has the concurrency that was asked for
	default:
		return nil
	}
	// ctx may be a request's (a Submit that subscribed the key first), so the deadline is
	// not derived from its cancellation: see subscribe.
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultRunnerCloseDeadline)
	_ = surplus.Close(closeCtx)
	cancel()
	if closed {
		return queue.ErrClosed
	}
	return nil
}

// subscribe consumes a topic under a context that carries ctx's cancellation and deadline
// but none of its values. A queue keeps the context it is given as the base of every
// delivery, and the caller may be a request handler (a Submit that subscribes a key for
// the first time) whose context, and the fasthttp request behind it, is recycled once the
// request ends: deliveries must not read its values, its query scope included.
//
// ctx's end is followed by a goroutine that selects on ctx.Done() and calls the runner's
// own cancel, never by context.AfterFunc: a *fasthttp.RequestCtx keeps a closed Done()
// with a nil Err() after a shutdown drains connections, and AfterFunc on such a parent
// panics. The deferred cancel ends the goroutine when subscribe returns.
func (r *Runner) subscribe(ctx context.Context, topic string, handler queue.Handler, opts ...queue.SubscribeOption) (queue.Subscription, error) {
	subCtx, cancel := context.WithCancel(r.loopCtx)
	defer cancel()
	// The watcher selects on this channel, never on the subCtx variable, which the
	// deadline below reassigns on this goroutine.
	stop := subCtx.Done()
	if done := ctx.Done(); done != nil {
		go func() {
			select {
			case <-done:
				cancel()
			case <-stop:
			}
		}()
	}
	if deadline, ok := ctx.Deadline(); ok {
		var cancelDeadline context.CancelFunc
		subCtx, cancelDeadline = context.WithDeadline(subCtx, deadline)
		defer cancelDeadline()
	}
	return r.queue.Subscribe(subCtx, topic, runnerGroup, handler, opts...)
}

func (r *Runner) subscribeOptions(concurrency, maxInFlight int) []queue.SubscribeOption {
	opts := []queue.SubscribeOption{
		queue.WithConcurrency(concurrency),
		queue.WithMaxAttempts(r.opts.MaxAttempts),
		// A job can be published before any instance has registered the group.
		queue.WithStartFrom(queue.StartFromEarliest),
		// A message that fails every attempt goes to the dead topic, whose handler ends the job.
		queue.WithDeadLetterTopic(runnerDeadTopic),
	}
	if maxInFlight > 0 {
		opts = append(opts, queue.WithMaxInFlight(maxInFlight))
	}
	if r.opts.PollInterval > 0 {
		opts = append(opts, queue.WithPollInterval(r.opts.PollInterval))
	}
	return opts
}

// Submit queues a job. An evaluator that calls a model is placed on an eval key for
// its provider; with no such key the job is refused, never run on some other key.
func (r *Runner) Submit(ctx context.Context, job *Job) error {
	if job == nil || job.ID == "" {
		return errors.New("an evaluation job needs an ID")
	}
	return r.publish(ctx, job, "evaljob:"+job.ID, "")
}

// publish puts a job on the queue of a key that can run it. fromKey is the key the job
// is recorded on now ("" for none); the move is claimed in the store first, so of
// several callers moving the same job only one publishes it.
func (r *Runner) publish(ctx context.Context, job *Job, messageID, fromKey string) error {
	if job == nil || job.ID == "" {
		return errors.New("an evaluation job needs an ID")
	}
	topic := runnerLocalTopic
	headers := map[string]string{}
	if provider, model, needsModel := r.modelOf(job); needsModel {
		// Pick from this call's own read: a concurrent refresh that read the keys earlier
		// can replace r.eligible with an older map in between.
		eligible, err := r.refresh(ctx)
		if err != nil {
			return err
		}
		key, ok := pickKey(eligible, provider, model)
		if !ok {
			return fmt.Errorf("%w on provider %q for model %q; mark a key \"use for evaluations\" in the provider's settings", ErrNoEvalKey, provider, model)
		}
		// A key marked since the last Sync has no consumer on this instance yet.
		if err := r.ensureSubscribed(ctx, key, false); err != nil {
			return err
		}
		topic = keyTopic(key.ID)
		headers["key_id"] = key.ID
	}
	payload, err := json.Marshal(jobMessage{JobID: job.ID})
	if err != nil {
		return err
	}
	toKey := headers["key_id"]
	claimed := toKey != fromKey
	if claimed {
		// The route's message is named after its route time, so the sweep can find its
		// delivery later. Microseconds survive every store's round trip.
		routedAt := time.Now().Truncate(time.Microsecond)
		moved, err := r.store.RouteJob(ctx, job.ID, fromKey, toKey, routedAt)
		if err != nil {
			return err
		}
		if !moved {
			return nil // another instance moved it, or it has started
		}
		if toKey != "" {
			messageID = routeMessageID(job.ID, routedAt)
		}
	}
	if err := r.queue.Publish(ctx, &queue.Message{ID: messageID, Topic: topic, Payload: payload, Headers: headers}); err != nil {
		if claimed {
			// Give the claim back so a later sweep can try again.
			_, _ = r.store.RouteJob(ctx, job.ID, toKey, fromKey, time.Now())
		}
		return &placementUnknownError{err: err}
	}
	return nil
}

// placementUnknownError is a failed queue publish. The message may still have been
// stored (a write that landed with its acknowledgement lost), so the job must not be
// ended with an error: if the message exists it runs, and if not the sweep publishes
// the job again.
type placementUnknownError struct{ err error }

func (e *placementUnknownError) Error() string {
	return "failed to queue the evaluation: " + e.err.Error()
}

func (e *placementUnknownError) Unwrap() error { return e.err }

// Is matches ErrPlacementUnknown, so a caller outside this package can tell an uncertain
// publish from a definite failure.
func (e *placementUnknownError) Is(target error) bool { return target == ErrPlacementUnknown }

// modelOf reports the model a job calls, if any: the one it names when it brings its
// own work, and the one its evaluator needs otherwise.
func (r *Runner) modelOf(job *Job) (provider, model string, needsModel bool) {
	if job.Exec != nil {
		if job.Model == nil {
			return "", "", false
		}
		return job.Model.Provider, job.Model.Model, true
	}
	return r.engine.modelOf(job.Definition)
}

// pickKey chooses a key of eligible for a provider and model. Keys are weighted by their
// concurrency, so a key with twice the room takes twice the jobs. refresh builds a new
// map per call and never changes it once stored, so eligible is read without r.mu.
func pickKey(eligible map[string]EvalKey, provider, model string) (EvalKey, bool) {
	var candidates []EvalKey
	total := 0
	for _, key := range eligible {
		if key.Provider != provider {
			continue
		}
		if key.Supports != nil && !key.Supports(model) {
			continue
		}
		candidates = append(candidates, key)
		total += key.Concurrency
	}
	if len(candidates) == 0 {
		return EvalKey{}, false
	}
	pick := rand.IntN(total)
	for _, key := range candidates {
		if pick < key.Concurrency {
			return key, true
		}
		pick -= key.Concurrency
	}
	return candidates[len(candidates)-1], true
}

func (r *Runner) isEligible(keyID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.eligible[keyID]
	return ok
}

// eligibleServes reports whether a key is in the current eligible map and can run the
// job, reading the key once under r.mu.
func (r *Runner) eligibleServes(keyID string, job *Job) bool {
	r.mu.Lock()
	key, ok := r.eligible[keyID]
	r.mu.Unlock()
	return ok && r.keyServes(key, job)
}

type jobMessage struct {
	JobID string `json:"job_id"`
}

// handler runs the jobs of one topic. keyID is the provider key the topic belongs to,
// or "" for the local topic.
func (r *Runner) handler(keyID string) queue.Handler {
	return func(ctx context.Context, d *queue.Delivery) error {
		var message jobMessage
		if err := json.Unmarshal(d.Payload, &message); err != nil || message.JobID == "" {
			return queue.Permanent(fmt.Errorf("malformed evaluation message %s", d.ID))
		}
		job, err := r.store.LoadJob(ctx, message.JobID)
		if errors.Is(err, ErrJobNotFound) {
			return queue.Permanent(err)
		}
		if err != nil {
			return err
		}
		if job.Done {
			return nil // a duplicate of a message that was already handled
		}
		if job.KeyID != keyID {
			// The job waits on another queue now: it was moved off this topic and this
			// is the message it left behind. Running it here would run it twice.
			return nil
		}

		// A job that calls no model needs no eval key, so a key that was unmarked since it
		// was queued does not stop it: it runs on the topic that delivered it. A test run's
		// repair of an entry left evaluating is such a job, and as a started entry it could
		// not be moved anyway.
		if _, _, needsModel := r.modelOf(job); keyID != "" && needsModel {
			// The key may have been unmarked since the job was queued on it. Judge it from
			// this call's own read: a concurrent refresh that read the keys earlier can
			// replace r.eligible with an older map in between.
			eligible, err := r.refresh(ctx)
			if err != nil {
				return err
			}
			if key, ok := eligible[keyID]; !ok || !r.keyServes(key, job) {
				if job.Started {
					// A worker died partway through it, and it cannot be moved any more. Only
					// the delivery that started it ends it; another message leaves it to that one.
					if !r.ownsStartedJob(job, d.DeliveryID) {
						return nil
					}
					return r.complete(ctx, job, errorResult(errKeyGone))
				}
				return r.reroute(ctx, job, keyID)
			}
		}

		claim := JobClaim{DeliveryID: d.DeliveryID}
		if r.opts.Volatile {
			r.mu.Lock()
			claim.StartedBefore = r.startedAt
			r.mu.Unlock()
		}
		claimed, err := r.store.StartJob(ctx, job.ID, keyID, claim)
		if err != nil {
			return err
		}
		if !claimed {
			return nil // another delivery of this job holds it
		}
		var result Result
		if job.Exec != nil {
			result = job.Exec(WithKeyID(ctx, keyID))
		} else {
			result = r.engine.Run(WithKeyID(ctx, keyID), job.Definition, job.Input)
		}
		return r.complete(ctx, job, result)
	}
}

// deadOptions are the dead topic's subscription options: no dead topic of its own, and
// retries until the job's error result is written.
func (r *Runner) deadOptions() []queue.SubscribeOption {
	opts := []queue.SubscribeOption{queue.WithConcurrency(1), queue.WithStartFrom(queue.StartFromEarliest), queue.WithMaxAttempts(1 << 30)}
	if r.opts.PollInterval > 0 {
		opts = append(opts, queue.WithPollInterval(r.opts.PollInterval))
	}
	return opts
}

// deadHandler ends a job whose message failed every attempt with an error result, so it
// does not stay queued or running with nothing on any queue. It returns an error until the
// result is written, so the dead topic retries it.
//
// A started job is ended only by the dead letter of the delivery that started it. The
// dead letter of another message for the job (a sweep's or a retried publish's) leaves
// it to its owner, whose own delivery finishes it or, if that worker died, is
// redelivered and goes through the handler's gone-key and owner checks.
//
// A queued job is ended only by the dead letter of a message on the topic it waits on
// now, and only while it is still queued on that key: a move or start that lands between
// LoadJob and the write leaves the job to its new route or claim.
func (r *Runner) deadHandler() queue.Handler {
	return func(ctx context.Context, d *queue.Delivery) error {
		var message jobMessage
		if err := json.Unmarshal(d.Payload, &message); err != nil || message.JobID == "" {
			return nil
		}
		job, err := r.store.LoadJob(ctx, message.JobID)
		if errors.Is(err, ErrJobNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if job.Done {
			return nil
		}
		if job.Started && !r.ownsStartedJob(job, d.Headers[queue.HeaderDeadLetterDeliveryID]) {
			return nil
		}
		// A queued job that moved to another key since this message was sent waits on that
		// key's queue now, and the message there runs it. Only the dead letter of its current
		// route ends it. A dead letter with no source topic keeps the terminal handling.
		if source, ok := d.Headers[queue.HeaderDeadLetterSourceTopic]; ok && !job.Started && source != topicOf(job.KeyID) {
			return nil
		}
		failed := errorResult(fmt.Errorf("evaluation failed after %d attempts", r.opts.MaxAttempts))
		if !job.Started {
			return r.completeQueued(context.WithoutCancel(ctx), job, job.KeyID, failed)
		}
		return r.complete(context.WithoutCancel(ctx), job, failed)
	}
}

// keyServes reports whether an eligible key can run the job: a job that needs a model needs a
// key of its provider that supports the model. A key's model list can change after a job
// was routed to it. The caller passes the key from the eligible map it read, so the
// answer never mixes two refreshes.
func (r *Runner) keyServes(key EvalKey, job *Job) bool {
	provider, model, needsModel := r.modelOf(job)
	if !needsModel {
		return true
	}
	return key.Provider == provider && (key.Supports == nil || key.Supports(model))
}

// ownsStartedJob reports whether a delivery may end a started job: the delivery that
// started it (its worker died and the message came back), any delivery for a job with no
// recorded claim (started before claims were stored, so it has no owner to wait for), or on
// a volatile queue any delivery for a job started before this runner started.
func (r *Runner) ownsStartedJob(job *Job, deliveryID string) bool {
	if job.ClaimID == "" || job.ClaimID == deliveryID {
		return true
	}
	if r.opts.Volatile && !job.StartedAt.IsZero() {
		r.mu.Lock()
		defer r.mu.Unlock()
		return job.StartedAt.Before(r.startedAt)
	}
	return false
}

// reroute moves a job off a key that is no longer eligible. With no eligible key left
// the job ends with an error, so nothing waits on it forever.
func (r *Runner) reroute(ctx context.Context, job *Job, fromKey string) error {
	err := r.publish(ctx, job, "evaljob:"+job.ID+":"+uuid.NewString(), fromKey)
	if errors.Is(err, ErrNoEvalKey) {
		// Another instance may have moved the job to a key it sees and started it since
		// this one refreshed its keys, so the write holds only while the job is still
		// queued on fromKey.
		return r.completeQueued(ctx, job, fromKey, errorResult(err))
	}
	return err
}

// completeQueued stores the result of a job that has not started, only while it is still
// queued on keyID. A job that moved or started since it was read is left to its new route
// or claim, and that is not an error. Like complete, it stores nothing for a delivery
// whose context has already ended, and the write has its own short deadline.
func (r *Runner) completeQueued(ctx context.Context, job *Job, keyID string, result Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), completeWriteTimeout)
	defer cancel()
	completed, err := r.store.CompleteQueuedJob(writeCtx, job.ID, keyID, result)
	if err != nil {
		return err
	}
	if completed {
		r.notify(job.ID)
	}
	return nil
}

// complete stores a job's result. A delivery whose context has already ended (a
// shutdown, a lease that ran out) stores nothing: the queue delivers the job again and
// that attempt stores the result, so two attempts never both write. Once started, the
// write gets its own short deadline, detached from the delivery context, so a context
// that ends during the write does not lose a result that was about to be stored.
func (r *Runner) complete(ctx context.Context, job *Job, result Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store := r.store.CompleteJob
	if job.Complete != nil {
		store = func(ctx context.Context, _ string, result Result) error { return job.Complete(ctx, result) }
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), completeWriteTimeout)
	defer cancel()
	// A failed write is retried here, with the same result, while this worker holds the
	// job. Returning it would have the queue redeliver the message under the same delivery,
	// whose claim holds, and the job would run again: a judge call made twice.
	for try := 0; ; try++ {
		err := store(writeCtx, job.ID, result)
		if err == nil {
			break
		}
		if errors.Is(err, ErrJobNotFound) || try >= len(completeWriteBackoff) {
			return err
		}
		timer := time.NewTimer(completeWriteBackoff[try])
		select {
		case <-writeCtx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
	r.notify(job.ID)
	return nil
}

// completeWriteBackoff is the wait before each retry of a failed result write: three tries
// in all, inside completeWriteTimeout.
var completeWriteBackoff = []time.Duration{100 * time.Millisecond, 400 * time.Millisecond}

// Watch returns a channel that is closed when this instance completes the job. A job
// may complete on another instance, so callers also poll the store. cancel releases
// the watch.
func (r *Runner) Watch(jobID string) (done <-chan struct{}, cancel func()) {
	ch := make(chan struct{})
	r.mu.Lock()
	r.waiters[jobID] = append(r.waiters[jobID], ch)
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		waiters := r.waiters[jobID]
		for i, waiter := range waiters {
			if waiter == ch {
				r.waiters[jobID] = append(waiters[:i], waiters[i+1:]...)
				break
			}
		}
		if len(r.waiters[jobID]) == 0 {
			delete(r.waiters, jobID)
		}
	}
}

func (r *Runner) notify(jobID string) {
	r.mu.Lock()
	waiters := r.waiters[jobID]
	delete(r.waiters, jobID)
	r.mu.Unlock()
	for _, waiter := range waiters {
		close(waiter)
	}
}

// Close stops consuming. It waits for running evaluations until ctx is done.
func (r *Runner) Close(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.stopLoop()
	subs := make([]queue.Subscription, 0, len(r.subs)+2)
	if r.local != nil {
		subs = append(subs, r.local)
	}
	if r.dead != nil {
		subs = append(subs, r.dead)
	}
	for _, sub := range r.subs {
		subs = append(subs, sub.sub)
	}
	r.mu.Unlock()

	var firstErr error
	for _, sub := range subs {
		if err := sub.Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// The sync in progress, if any, has seen its context end; wait for it within ctx.
	loopDone := make(chan struct{})
	go func() {
		r.loopDone.Wait()
		close(loopDone)
	}()
	select {
	case <-loopDone:
	case <-ctx.Done():
		if firstErr == nil {
			firstErr = ctx.Err()
		}
	}
	return firstErr
}

// topicOf names the topic a job routed to keyID waits on: the key's topic, or the local
// topic for "".
func topicOf(keyID string) string {
	if keyID == "" {
		return runnerLocalTopic
	}
	return keyTopic(keyID)
}

// keyTopic names the topic of a provider key. Key IDs come from user config and may
// hold characters a topic name cannot, so the name is a digest of the ID.
func keyTopic(keyID string) string {
	sum := sha256.Sum256([]byte(keyID))
	return runnerKeyTopic + hex.EncodeToString(sum[:16])
}
