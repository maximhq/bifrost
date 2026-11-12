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
	"github.com/maximhq/bifrost/framework/queue"
)

var (
	// ErrNoEvalKey is returned when an evaluator needs a model call and no provider key
	// is enabled for evaluations on that provider and model.
	ErrNoEvalKey = errors.New("no provider key is enabled for evaluations")
	// ErrJobNotFound is returned by a JobStore for a job that does not exist.
	ErrJobNotFound = errors.New("evaluation job not found")
)

// Queue names. Every evaluation is a message: evaluators that call a model wait on the
// topic of the provider key they will use, and the rest share one local topic.
const (
	runnerGroup      = "evals"
	runnerLocalTopic = "evals.local"
	runnerKeyTopic   = "evals.key."
)

// Runner defaults.
const (
	// DefaultEvalKeyConcurrency applies to an eval key that sets no concurrency.
	DefaultEvalKeyConcurrency = 4
	defaultLocalConcurrency   = 10
	defaultRunnerMaxAttempts  = 3
	defaultRunnerSyncInterval = 30 * time.Second
	// A queued job is only looked at by the sweep once it is this old. Younger jobs are
	// left to the handler of the topic they are on.
	defaultRunnerStrandedAfter = 2 * time.Minute
	// DefaultExecutionRetention is how long a completed job's result is kept.
	DefaultExecutionRetention  = 7 * 24 * time.Hour
	runnerPurgeInterval        = time.Hour
	runnerSweepBatch           = 200
	defaultRunnerCloseDeadline = 30 * time.Second
)

// EvalKey is a provider key that is enabled for evaluations.
type EvalKey struct {
	ID          string
	Name        string // the key's display name; never the secret
	Provider    string
	Concurrency int                     // max evaluations in flight on this key across all instances
	Supports    func(model string) bool // nil means every model
}

// KeySource returns the provider keys currently enabled for evaluations.
type KeySource func(ctx context.Context) ([]EvalKey, error)

// Keys returns the provider keys currently enabled for evaluations, read from the source.
func (r *Runner) Keys(ctx context.Context) ([]EvalKey, error) {
	return r.keys(ctx)
}

// Job is one evaluator run on one input.
type Job struct {
	ID         string
	Definition Definition
	Input      Input
	// KeyID is the key whose queue the job waits on, or "" when it has not been routed
	// to one. The store fills it in; callers that submit a job leave it empty.
	KeyID string
	// Done reports that the job already has a result. The store fills it in.
	Done bool
	// Started reports that a worker began running the job and it has no result yet.
	// The store fills it in. A started job cannot be moved to another key's queue.
	Started bool
	// Complete, when set, stores the job's result in place of JobStore.CompleteJob. A
	// store sets it when it loads a job and already holds what completing it needs, so
	// the job is not read a second time. An error it returns has the job delivered again.
	Complete func(ctx context.Context, result Result) error
}

// JobStore persists jobs. The runner sends only a job's ID through the queue, so inputs
// of any size stay out of queue messages.
type JobStore interface {
	// LoadJob returns the job, or ErrJobNotFound.
	LoadJob(ctx context.Context, id string) (*Job, error)
	// StartJob records that the job began running, and on which key ("" for none).
	StartJob(ctx context.Context, id, keyID string) error
	// CompleteJob stores the result. Delivery is at least once, so it must tolerate
	// being called again for a job that already completed.
	CompleteJob(ctx context.Context, id string, result Result) error
	// RouteJob records that a waiting job moved from one key's queue to another's ("" for
	// no key). It must be a claim: it moves the job only if it has not started and is
	// still on fromKey, and reports whether it did.
	RouteJob(ctx context.Context, id, fromKey, toKey string) (bool, error)
	// StaleJobs returns jobs queued before the cutoff that have not started, at most
	// limit of them. skipKeys are the keys that are being consumed: a job waiting on
	// one of them is in no trouble, and a store should leave it out, so that a long
	// line of healthy jobs cannot hide the stranded ones behind it.
	StaleJobs(ctx context.Context, queuedBefore time.Time, skipKeys []string, limit int) ([]*Job, error)
	// PurgeJobs deletes jobs that completed before the cutoff.
	PurgeJobs(ctx context.Context, completedBefore time.Time) (int64, error)
}

// RunnerOptions tunes the runner. Zero values take the defaults.
type RunnerOptions struct {
	LocalConcurrency int           // evaluations without model calls running at once, per instance
	PollInterval     time.Duration // queue poll interval; zero takes the queue default
	MaxAttempts      int           // deliveries before a job's message is dead-lettered
	// SyncInterval is how often subscriptions are reconciled with the eval keys. A key
	// marked on one instance is picked up by the others within this interval.
	SyncInterval time.Duration
	// StrandedAfter is how long a job may sit queued before the sweep checks whether the
	// key it waits on still exists.
	StrandedAfter time.Duration
	// Retention is how long a completed job's result is kept. Negative keeps results
	// forever.
	Retention time.Duration
	// Volatile says the queue forgets its messages when the gateway stops, as the
	// in-memory queue does. The jobs stay stored as queued or running, so on Start the
	// runner puts every unfinished job back on the queue. It needs a JobStore that is
	// also a JobResumer.
	Volatile bool
}

// JobResumer is implemented by a JobStore that can list the jobs a queue lost.
type JobResumer interface {
	// UnfinishedJobs returns every job that is queued or was started and has no
	// result. It is only read when the gateway starts on a queue that forgets.
	UnfinishedJobs(ctx context.Context) ([]*Job, error)
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
// in-flight cap of that key's topic, so it holds across every instance.
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
	waiters  map[string][]chan struct{}
	closed   bool
	stop     chan struct{} // closed by Close to end the sync loop
	// lastPurge is when this instance last removed old results.
	lastPurge time.Time
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
	return &Runner{
		queue:    q,
		engine:   engine,
		keys:     keys,
		store:    store,
		opts:     opts,
		eligible: make(map[string]EvalKey),
		subs:     make(map[string]keySubscription),
		waiters:  make(map[string][]chan struct{}),
		stop:     make(chan struct{}),
	}
}

// Start begins consuming the local topic and the topic of every eval key.
func (r *Runner) Start(ctx context.Context) error {
	local, err := r.queue.Subscribe(ctx, runnerLocalTopic, runnerGroup, r.handler(""), r.subscribeOptions(r.opts.LocalConcurrency, 0)...)
	if err != nil {
		return fmt.Errorf("failed to subscribe to the local evaluation topic: %w", err)
	}
	r.mu.Lock()
	r.local = local
	r.mu.Unlock()
	if err := r.Sync(ctx); err != nil {
		return err
	}
	if r.opts.Volatile {
		if err := r.resume(ctx); err != nil {
			return err
		}
	}
	go r.syncLoop()
	return nil
}

// errKeyGone ends a job that had started on a key that is no longer an eval key. A
// started job cannot be moved to another key's queue, so it cannot be run again.
var errKeyGone = fmt.Errorf("%w: the key this evaluation started on is no longer enabled for evaluations", ErrNoEvalKey)

// resume puts every unfinished job back on a queue that forgot its messages. It runs
// once, on Start, before this runner has published anything, so no job is queued twice.
func (r *Runner) resume(ctx context.Context) error {
	resumer, ok := r.store.(JobResumer)
	if !ok {
		return nil
	}
	jobs, err := resumer.UnfinishedJobs(ctx)
	if err != nil {
		return fmt.Errorf("failed to list the evaluation jobs to resume: %w", err)
	}
	for _, job := range jobs {
		// One job that cannot be resumed must not keep the others from running.
		_ = r.resumeJob(ctx, job)
	}
	return nil
}

func (r *Runner) resumeJob(ctx context.Context, job *Job) error {
	_, _, needsModel := r.engine.modelOf(job.Definition)
	switch {
	case job.KeyID != "" && r.isEligible(job.KeyID):
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
func (r *Runner) syncLoop() {
	ticker := time.NewTicker(r.opts.SyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			// A failed sync is retried on the next tick; the current subscriptions stay.
			_ = r.Sync(context.Background())
		}
	}
}

// Sync reconciles this instance's subscriptions with the current eval keys. Call it
// when provider keys change. A key that is no longer eligible keeps its subscription,
// which from then on only moves that key's queued jobs to a key that is.
func (r *Runner) Sync(ctx context.Context) error {
	eligible, err := r.refresh(ctx)
	if err != nil {
		return err
	}
	for _, key := range eligible {
		if err := r.ensureSubscribed(ctx, key, true); err != nil {
			return err
		}
	}
	return r.Sweep(ctx)
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
	stale, err := r.store.StaleJobs(ctx, time.Now().Add(-r.opts.StrandedAfter), consumed, runnerSweepBatch)
	if err != nil {
		return fmt.Errorf("failed to list stale evaluation jobs: %w", err)
	}
	for _, job := range stale {
		if job.KeyID == "" {
			// Never routed. A job that needs no model sits on the local topic by design; one
			// that needs a model was stored and then never published.
			if _, _, needsModel := r.engine.modelOf(job.Definition); !needsModel {
				continue
			}
		} else if r.isEligible(job.KeyID) {
			continue // waiting its turn on a key that is being consumed
		}
		if err := r.reroute(ctx, job, job.KeyID); err != nil {
			return err
		}
	}
	return r.purge(ctx)
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
	sub, err := r.queue.Subscribe(ctx, keyTopic(key.ID), runnerGroup, r.handler(key.ID), r.subscribeOptions(key.Concurrency, key.Concurrency)...)
	if err != nil {
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
	closeCtx, cancel := context.WithTimeout(ctx, defaultRunnerCloseDeadline)
	_ = surplus.Close(closeCtx)
	cancel()
	if closed {
		return queue.ErrClosed
	}
	return nil
}

func (r *Runner) subscribeOptions(concurrency, maxInFlight int) []queue.SubscribeOption {
	opts := []queue.SubscribeOption{
		queue.WithConcurrency(concurrency),
		queue.WithMaxAttempts(r.opts.MaxAttempts),
		// A job can be published before any instance has registered the group.
		queue.WithStartFrom(queue.StartFromEarliest),
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
	if provider, model, needsModel := r.engine.modelOf(job.Definition); needsModel {
		if _, err := r.refresh(ctx); err != nil {
			return err
		}
		key, ok := r.pickKey(provider, model)
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
		moved, err := r.store.RouteJob(ctx, job.ID, fromKey, toKey)
		if err != nil {
			return err
		}
		if !moved {
			return nil // another instance moved it, or it has started
		}
	}
	if err := r.queue.Publish(ctx, &queue.Message{ID: messageID, Topic: topic, Payload: payload, Headers: headers}); err != nil {
		if claimed {
			// Give the claim back so a later sweep can try again.
			_, _ = r.store.RouteJob(ctx, job.ID, toKey, fromKey)
		}
		return err
	}
	return nil
}

// pickKey chooses an eligible key for a provider and model. Keys are weighted by their
// concurrency, so a key with twice the room takes twice the jobs.
func (r *Runner) pickKey(provider, model string) (EvalKey, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var candidates []EvalKey
	total := 0
	for _, key := range r.eligible {
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

		if keyID != "" {
			// The key may have been unmarked since the job was queued on it.
			if _, err := r.refresh(ctx); err != nil {
				return err
			}
			if !r.isEligible(keyID) {
				if job.Started {
					// A worker died partway through it. It cannot be moved any more.
					return r.complete(ctx, job, errorResult(errKeyGone))
				}
				return r.reroute(ctx, job, keyID)
			}
		}

		if err := r.store.StartJob(ctx, job.ID, keyID); err != nil {
			return err
		}
		result := r.engine.Run(WithKeyID(ctx, keyID), job.Definition, job.Input)
		return r.complete(ctx, job, result)
	}
}

// reroute moves a job off a key that is no longer eligible. With no eligible key left
// the job ends with an error, so nothing waits on it forever.
func (r *Runner) reroute(ctx context.Context, job *Job, fromKey string) error {
	err := r.publish(ctx, job, "evaljob:"+job.ID+":"+uuid.NewString(), fromKey)
	if errors.Is(err, ErrNoEvalKey) {
		return r.complete(ctx, job, errorResult(err))
	}
	return err
}

func (r *Runner) complete(ctx context.Context, job *Job, result Result) error {
	store := r.store.CompleteJob
	if job.Complete != nil {
		store = func(ctx context.Context, _ string, result Result) error { return job.Complete(ctx, result) }
	}
	if err := store(ctx, job.ID, result); err != nil {
		return err
	}
	r.notify(job.ID)
	return nil
}

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
	close(r.stop)
	subs := make([]queue.Subscription, 0, len(r.subs)+1)
	if r.local != nil {
		subs = append(subs, r.local)
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
	return firstErr
}

// keyTopic names the topic of a provider key. Key IDs come from user config and may
// hold characters a topic name cannot, so the name is a digest of the ID.
func keyTopic(keyID string) string {
	sum := sha256.Sum256([]byte(keyID))
	return runnerKeyTopic + hex.EncodeToString(sum[:16])
}
