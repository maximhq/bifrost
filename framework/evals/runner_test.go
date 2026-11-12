package evals

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memoryJobStore is an in-memory JobStore for runner tests.
type memoryJobStore struct {
	mu        sync.Mutex
	jobs      map[string]*Job
	routed    map[string]string    // job id to the key whose queue it waits on
	queuedAt  map[string]time.Time // when each job was created
	purgedAt  []time.Time          // cutoffs PurgeJobs was called with
	started   map[string]string    // job id to key id
	results   map[string]Result
	completed map[string]int // completions per job
	skipped   [][]string     // the keys each StaleJobs call was told are being consumed
}

func newMemoryJobStore() *memoryJobStore {
	return &memoryJobStore{
		jobs: map[string]*Job{}, started: map[string]string{}, results: map[string]Result{}, completed: map[string]int{},
		routed: map[string]string{}, queuedAt: map[string]time.Time{},
	}
}

func (s *memoryJobStore) add(job *Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
	s.queuedAt[job.ID] = time.Now()
}

// addStranded stores a job as another instance would have left it: queued a while ago
// on a key's topic, with nothing in this runner's queue for it.
func (s *memoryJobStore) addStranded(job *Job, keyID string, age time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
	s.routed[job.ID] = keyID
	s.queuedAt[job.ID] = time.Now().Add(-age)
}

// view returns a job as the store would load it now.
func (s *memoryJobStore) view(id string) *Job {
	job := *s.jobs[id]
	job.KeyID = s.routed[id]
	_, job.Done = s.results[id]
	_, job.Started = s.started[id]
	return &job
}

// UnfinishedJobs returns every job without a result.
func (s *memoryJobStore) UnfinishedJobs(context.Context) ([]*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Job
	for id := range s.jobs {
		if _, done := s.results[id]; !done {
			out = append(out, s.view(id))
		}
	}
	return out, nil
}

// markStarted records that a job began running on a key, as a worker that then died
// would have left it.
func (s *memoryJobStore) markStarted(id, keyID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started[id] = keyID
}

func (s *memoryJobStore) LoadJob(_ context.Context, id string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[id]; !ok {
		return nil, ErrJobNotFound
	}
	return s.view(id), nil
}

func (s *memoryJobStore) RouteJob(_ context.Context, id, fromKey, toKey string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, started := s.started[id]; started || s.routed[id] != fromKey {
		return false, nil
	}
	s.routed[id] = toKey
	return true, nil
}

func (s *memoryJobStore) StaleJobs(_ context.Context, queuedBefore time.Time, skipKeys []string, _ int) ([]*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipped = append(s.skipped, skipKeys)
	var out []*Job
	for id := range s.jobs {
		_, started := s.started[id]
		_, done := s.results[id]
		if s.routed[id] != "" && slices.Contains(skipKeys, s.routed[id]) {
			continue
		}
		if !started && !done && s.queuedAt[id].Before(queuedBefore) {
			out = append(out, s.view(id))
		}
	}
	return out, nil
}

func (s *memoryJobStore) PurgeJobs(_ context.Context, completedBefore time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgedAt = append(s.purgedAt, completedBefore)
	return 0, nil
}

func (s *memoryJobStore) routedTo(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.routed[id]
}

func (s *memoryJobStore) StartJob(_ context.Context, id, keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started[id] = keyID
	return nil
}

func (s *memoryJobStore) CompleteJob(_ context.Context, id string, result Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[id] = result
	s.completed[id]++
	return nil
}

func (s *memoryJobStore) result(id string) (Result, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, ok := s.results[id]
	return result, ok
}

func (s *memoryJobStore) keyOf(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started[id]
}

// keyTracker records, per provider key, how many evaluations ran and the most that ran
// at once.
type keyTracker struct {
	mu       sync.Mutex
	inFlight map[string]int
	peak     map[string]int
	runs     map[string]int
}

func newKeyTracker() *keyTracker {
	return &keyTracker{inFlight: map[string]int{}, peak: map[string]int{}, runs: map[string]int{}}
}

func (k *keyTracker) enter(key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.inFlight[key]++
	k.runs[key]++
	if k.inFlight[key] > k.peak[key] {
		k.peak[key] = k.inFlight[key]
	}
}

func (k *keyTracker) leave(key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.inFlight[key]--
}

func (k *keyTracker) snapshot() (peak, runs map[string]int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	peak, runs = map[string]int{}, map[string]int{}
	for key, v := range k.peak {
		peak[key] = v
	}
	for key, v := range k.runs {
		runs[key] = v
	}
	return peak, runs
}

type runnerHarness struct {
	queue   queue.Queue
	runner  *Runner
	store   *memoryJobStore
	tracker *keyTracker
	keys    *atomic.Pointer[[]EvalKey]
}

// newRunnerHarness builds a runner on an in-memory queue. The AI evaluator is a fake
// that records which key it ran on and holds its slot for hold.
func newRunnerHarness(t *testing.T, keys []EvalKey, hold time.Duration) *runnerHarness {
	t.Helper()
	q, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)

	tracker := newKeyTracker()
	engine := NewEngine(EngineOptions{})
	engine.Register(EvaluatorTypeAI, "", func(Definition, Config) (Evaluator, error) {
		return &fakeEvaluator{fn: func(ctx context.Context, _ Input) (Result, error) {
			key := KeyIDFromContext(ctx)
			tracker.enter(key)
			defer tracker.leave(key)
			time.Sleep(hold)
			return Result{Score: BoolScore(true), Reasoning: "ran on " + key}, nil
		}}, nil
	})
	RegisterProgrammaticEvaluator(engine, ProgrammaticOptions{})

	current := &atomic.Pointer[[]EvalKey]{}
	current.Store(&keys)
	store := newMemoryJobStore()
	runner := NewRunner(q, engine, func(context.Context) ([]EvalKey, error) { return *current.Load(), nil }, store, RunnerOptions{PollInterval: 5 * time.Millisecond})
	require.NoError(t, runner.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = q.Close(ctx)
	})
	return &runnerHarness{queue: q, runner: runner, store: store, tracker: tracker, keys: current}
}

func aiJob(id, provider, model string) *Job {
	config := fmt.Sprintf(`{"instructions":"grade","grading_style":"yes_no","provider":%q,"model":%q}`, provider, model)
	return &Job{ID: id, Definition: Definition{ID: "ev-" + id, Type: EvaluatorTypeAI, Version: 1, Config: []byte(config)}}
}

func programmaticJob(id string) *Job {
	return &Job{
		ID:         id,
		Definition: Definition{ID: "ev-" + id, Type: EvaluatorTypeProgrammatic, Version: 1, Config: []byte(`{"code":"def validate(output):\n    return len(output) > 0\n","response_type":"yes_no"}`)},
		Input:      Input{Variables: map[string]string{"output": "hello"}},
	}
}

func (h *runnerHarness) submit(t *testing.T, job *Job) {
	t.Helper()
	h.store.add(job)
	require.NoError(t, h.runner.Submit(context.Background(), job))
}

func (h *runnerHarness) await(t *testing.T, ids ...string) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, id := range ids {
			if _, done := h.store.result(id); !done {
				return false
			}
		}
		return true
	}, 10*time.Second, 5*time.Millisecond, "jobs did not complete")
}

func anyModel(string) bool { return true }

func TestRunner_AIJobRunsOnAnEvalKey(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-evals", Provider: "openai", Concurrency: 2, Supports: anyModel}}, 0)

	h.submit(t, aiJob("job-1", "openai", "gpt-4o-mini"))
	h.await(t, "job-1")

	result, _ := h.store.result("job-1")
	assert.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.Equal(t, "ran on key-evals", result.Reasoning, "the evaluator sees the key it must use")
	assert.Equal(t, "key-evals", h.store.keyOf("job-1"), "the key is recorded on the job")
	assert.Equal(t, "ev-job-1", result.EvaluatorID)
}

func TestRunner_SubmitFailsWithoutAnEligibleKey(t *testing.T) {
	onlyGPT := func(model string) bool { return model == "gpt-4o-mini" }
	h := newRunnerHarness(t, []EvalKey{{ID: "key-evals", Provider: "openai", Concurrency: 2, Supports: onlyGPT}}, 0)

	t.Run("no key for the provider", func(t *testing.T) {
		job := aiJob("job-a", "anthropic", "claude-sonnet-5-5")
		h.store.add(job)
		err := h.runner.Submit(context.Background(), job)
		require.ErrorIs(t, err, ErrNoEvalKey)
		assert.Contains(t, err.Error(), "anthropic")
	})

	t.Run("key does not serve the model", func(t *testing.T) {
		job := aiJob("job-b", "openai", "o3")
		h.store.add(job)
		require.ErrorIs(t, h.runner.Submit(context.Background(), job), ErrNoEvalKey)
	})

	_, ran := h.store.result("job-a")
	assert.False(t, ran, "a job that could not be placed is never run on some other key")
}

func TestRunner_EvaluatorsWithoutModelCallsNeedNoKey(t *testing.T) {
	h := newRunnerHarness(t, nil, 0)

	h.submit(t, programmaticJob("job-1"))
	h.await(t, "job-1")

	result, _ := h.store.result("job-1")
	assert.Equal(t, BoolScore(true), result.Score, result.Reasoning)
	assert.Empty(t, h.store.keyOf("job-1"))
}

// The key's eval concurrency is the ceiling on evaluations in flight on that key.
func TestRunner_RespectsPerKeyConcurrency(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{
		{ID: "key-small", Provider: "openai", Concurrency: 1, Supports: anyModel},
		{ID: "key-large", Provider: "anthropic", Concurrency: 3, Supports: anyModel},
	}, 20*time.Millisecond)

	var ids []string
	for i := range 8 {
		openaiID, anthropicID := fmt.Sprintf("openai-%d", i), fmt.Sprintf("anthropic-%d", i)
		h.submit(t, aiJob(openaiID, "openai", "gpt-4o-mini"))
		h.submit(t, aiJob(anthropicID, "anthropic", "claude-sonnet-5-5"))
		ids = append(ids, openaiID, anthropicID)
	}
	h.await(t, ids...)

	peak, runs := h.tracker.snapshot()
	assert.Equal(t, 8, runs["key-small"])
	assert.Equal(t, 8, runs["key-large"])
	assert.Equal(t, 1, peak["key-small"], "a key with concurrency 1 never runs two at once")
	assert.LessOrEqual(t, peak["key-large"], 3)
	assert.Greater(t, peak["key-large"], 1, "a key with headroom does run in parallel")
}

// Several eval keys for one provider share the work.
func TestRunner_SpreadsJobsAcrossKeysOfAProvider(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{
		{ID: "key-a", Provider: "openai", Concurrency: 2, Supports: anyModel},
		{ID: "key-b", Provider: "openai", Concurrency: 2, Supports: anyModel},
	}, 2*time.Millisecond)

	var ids []string
	for i := range 40 {
		id := fmt.Sprintf("job-%d", i)
		h.submit(t, aiJob(id, "openai", "gpt-4o-mini"))
		ids = append(ids, id)
	}
	h.await(t, ids...)

	_, runs := h.tracker.snapshot()
	assert.Equal(t, 40, runs["key-a"]+runs["key-b"])
	assert.Greater(t, runs["key-a"], 0)
	assert.Greater(t, runs["key-b"], 0)
}

// A key that stops being eligible hands its queued jobs to another eligible key.
func TestRunner_ReroutesWhenAKeyLosesEligibility(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{
		{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel},
	}, 30*time.Millisecond)

	var ids []string
	for i := range 6 {
		id := fmt.Sprintf("job-%d", i)
		h.submit(t, aiJob(id, "openai", "gpt-4o-mini"))
		ids = append(ids, id)
	}

	// key-a is unmarked and key-b takes over while most jobs are still queued on key-a.
	replacement := []EvalKey{{ID: "key-b", Provider: "openai", Concurrency: 2, Supports: anyModel}}
	h.keys.Store(&replacement)
	require.NoError(t, h.runner.Sync(context.Background()))
	h.await(t, ids...)

	_, runs := h.tracker.snapshot()
	assert.Equal(t, 6, runs["key-a"]+runs["key-b"], "every job ran exactly once")
	assert.Greater(t, runs["key-b"], 0, "queued jobs moved to the key that is now eligible")
}

// With no eligible key left, a queued job ends with an error instead of waiting forever.
func TestRunner_FailsQueuedJobsWhenNoKeyRemains(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 30*time.Millisecond)

	var ids []string
	for i := range 4 {
		id := fmt.Sprintf("job-%d", i)
		h.submit(t, aiJob(id, "openai", "gpt-4o-mini"))
		ids = append(ids, id)
	}
	none := []EvalKey{}
	h.keys.Store(&none)
	require.NoError(t, h.runner.Sync(context.Background()))
	h.await(t, ids...)

	failed := 0
	for _, id := range ids {
		result, _ := h.store.result(id)
		if result.Score.Status == ScoreStatusError {
			failed++
			assert.Contains(t, result.Reasoning, "enabled for evaluations")
		}
	}
	assert.Greater(t, failed, 0)
}

func TestRunner_NotifiesWaiters(t *testing.T) {
	h := newRunnerHarness(t, nil, 0)
	job := programmaticJob("job-1")
	h.store.add(job)

	done, cancel := h.runner.Watch("job-1")
	defer cancel()
	require.NoError(t, h.runner.Submit(context.Background(), job))

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the waiter was never notified")
	}
	_, ok := h.store.result("job-1")
	assert.True(t, ok, "the result is stored before the waiter is woken")
}

// A message whose job is gone is dropped, and the runner keeps serving the next one.
func TestRunner_DropsMessagesForMissingJobs(t *testing.T) {
	h := newRunnerHarness(t, nil, 0)

	require.NoError(t, h.runner.Submit(context.Background(), programmaticJob("ghost"))) // never added to the store
	h.submit(t, programmaticJob("real"))
	h.await(t, "real")

	_, ran := h.store.result("ghost")
	assert.False(t, ran)
}

// An instance must start consuming a key that was marked after it booted, even if it
// never submits a job to that key itself: another instance may have queued one.
func TestRunner_PicksUpNewKeysOnItsOwn(t *testing.T) {
	q, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	engine := NewEngine(EngineOptions{})
	engine.Register(EvaluatorTypeAI, "", func(Definition, Config) (Evaluator, error) {
		return &fakeEvaluator{fn: scoring(BoolScore(true))}, nil
	})
	current := &atomic.Pointer[[]EvalKey]{}
	none := []EvalKey{}
	current.Store(&none)
	store := newMemoryJobStore()
	keys := func(context.Context) ([]EvalKey, error) { return *current.Load(), nil }

	consumer := NewRunner(q, engine, keys, store, RunnerOptions{PollInterval: 5 * time.Millisecond, SyncInterval: 20 * time.Millisecond})
	require.NoError(t, consumer.Start(context.Background()))
	// A second runner on the same queue plays the instance that receives the request.
	// It only publishes; its own consumers are closed so the first runner must do the work.
	publisher := NewRunner(q, engine, keys, store, RunnerOptions{PollInterval: time.Hour, SyncInterval: time.Hour})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = consumer.Close(ctx)
		_ = publisher.Close(ctx)
		_ = q.Close(ctx)
	})

	marked := []EvalKey{{ID: "key-late", Provider: "openai", Concurrency: 1, Supports: anyModel}}
	current.Store(&marked)
	job := aiJob("job-1", "openai", "gpt-4o-mini")
	store.addStranded(job, "key-late", 0) // routed to the key by the instance that queued it
	payload := []byte(`{"job_id":"job-1"}`)
	require.NoError(t, q.Publish(context.Background(), &queue.Message{ID: "from-another-instance", Topic: keyTopic("key-late"), Payload: payload}))

	require.Eventually(t, func() bool {
		_, done := store.result("job-1")
		return done
	}, 10*time.Second, 10*time.Millisecond, "the consumer never subscribed to the newly marked key")
	assert.Equal(t, "key-late", store.keyOf("job-1"))
}

// A key can be unmarked or deleted while every gateway is down. Its topic then has no
// consumer, so the jobs queued on it would wait forever. The sweep finds them by the
// key they were routed to and moves them.
func TestRunner_RecoversJobsStrandedOnARemovedKey(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 2, Supports: anyModel}}, 0)
	h.store.addStranded(aiJob("stranded", "openai", "gpt-4o-mini"), "key-gone", time.Hour)

	require.NoError(t, h.runner.Sweep(context.Background()))
	h.await(t, "stranded")

	result, _ := h.store.result("stranded")
	assert.Equal(t, ScoreStatusOK, result.Score.Status)
	assert.Equal(t, "key-a", h.store.keyOf("stranded"), "it ran on the key that is eligible now")
}

func TestRunner_FailsStrandedJobsWhenNoKeyRemains(t *testing.T) {
	h := newRunnerHarness(t, nil, 0)
	h.store.addStranded(aiJob("stranded", "openai", "gpt-4o-mini"), "key-gone", time.Hour)

	require.NoError(t, h.runner.Sweep(context.Background()))
	h.await(t, "stranded")

	result, _ := h.store.result("stranded")
	assert.Equal(t, ScoreStatusError, result.Score.Status)
	assert.Contains(t, result.Reasoning, "enabled for evaluations")
}

// A job that never got as far as a queue (the gateway died between storing and
// publishing it) is published by the sweep.
func TestRunner_PublishesJobsThatWereNeverQueued(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0)
	h.store.addStranded(aiJob("unrouted", "openai", "gpt-4o-mini"), "", time.Hour)

	require.NoError(t, h.runner.Sweep(context.Background()))
	h.await(t, "unrouted")
	assert.Equal(t, "key-a", h.store.keyOf("unrouted"))
}

// The sweep must not touch a job that is simply waiting its turn: requeueing it would
// run it twice.
func TestRunner_SweepLeavesWaitingJobsAlone(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0)
	// Old, but on a key that still has a consumer.
	h.store.addStranded(aiJob("waiting", "openai", "gpt-4o-mini"), "key-a", time.Hour)
	// On a removed key, but queued a moment ago: its own handler may still move it.
	h.store.addStranded(aiJob("fresh", "openai", "gpt-4o-mini"), "key-gone", 0)

	require.NoError(t, h.runner.Sweep(context.Background()))
	time.Sleep(80 * time.Millisecond)

	for _, id := range []string{"waiting", "fresh"} {
		_, done := h.store.result(id)
		assert.False(t, done, "%s was not requeued", id)
	}
	assert.Equal(t, "key-a", h.store.routedTo("waiting"))
	assert.Equal(t, "key-gone", h.store.routedTo("fresh"))
}

// The sweep tells the store which keys are being consumed, so the store can leave out
// the jobs that are only waiting their turn.
func TestRunner_SweepNamesTheKeysBeingConsumed(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0)
	require.NoError(t, h.runner.Sweep(context.Background()))
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	require.NotEmpty(t, h.store.skipped)
	assert.Equal(t, []string{"key-a"}, h.store.skipped[len(h.store.skipped)-1])
}

// A job that brings its own completion has its result stored through it, and the
// store is not asked to complete the job as well.
func TestRunner_UsesAJobsOwnCompletion(t *testing.T) {
	h := newRunnerHarness(t, nil, 0)
	got := make(chan Result, 1)
	job := programmaticJob("own-completion")
	job.Complete = func(_ context.Context, result Result) error {
		got <- result
		return nil
	}
	h.submit(t, job)
	select {
	case result := <-got:
		assert.Equal(t, ScoreStatusOK, result.Score.Status)
	case <-time.After(5 * time.Second):
		t.Fatal("the job's own completion was not called")
	}
	_, stored := h.store.result("own-completion")
	assert.False(t, stored)
}

// A job that was moved to another key leaves its old message behind. When the old key
// is consumed again, that message must not run the job a second time.
func TestRunner_SkipsAMessageForAJobOnAnotherKey(t *testing.T) {
	keys := []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}, {ID: "key-b", Provider: "openai", Concurrency: 1, Supports: anyModel}}
	h := newRunnerHarness(t, keys, 0)
	h.store.addStranded(aiJob("moved", "openai", "gpt-4o-mini"), "key-b", 0)
	require.NoError(t, h.queue.Publish(context.Background(), &queue.Message{ID: "left-behind", Topic: keyTopic("key-a"), Payload: []byte(`{"job_id":"moved"}`)}))

	time.Sleep(100 * time.Millisecond)
	_, ran := h.store.result("moved")
	assert.False(t, ran, "the message on the old key's topic is stale")
	_, runs := h.tracker.snapshot()
	assert.Zero(t, runs["key-a"])
}

// A worker can die after it started a job. If the job's key is unmarked before the
// message is delivered again, the job can no longer be moved, because it has started.
// It ends with an error; it must not stay "running" for ever.
func TestRunner_EndsAStartedJobWhoseKeyIsGone(t *testing.T) {
	keys := []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}, {ID: "key-b", Provider: "openai", Concurrency: 1, Supports: anyModel}}
	h := newRunnerHarness(t, keys, 0)
	onlyB := keys[1:]
	h.keys.Store(&onlyB)
	h.store.addStranded(aiJob("orphan", "openai", "gpt-4o-mini"), "key-a", 0)
	h.store.markStarted("orphan", "key-a")
	require.NoError(t, h.queue.Publish(context.Background(), &queue.Message{ID: "redelivered", Topic: keyTopic("key-a"), Payload: []byte(`{"job_id":"orphan"}`)}))

	h.await(t, "orphan")
	result, _ := h.store.result("orphan")
	assert.Equal(t, ScoreStatusError, result.Score.Status)
	assert.Contains(t, result.Reasoning, "enabled for evaluations")
}

// gatedQueue holds every Subscribe to one topic until two callers are inside, and
// counts the subscriptions to it that are still open.
type gatedQueue struct {
	queue.Queue
	topic   string
	arrived chan struct{}
	open    atomic.Int32
}

type countedSubscription struct {
	queue.Subscription
	open *atomic.Int32
	once sync.Once
}

func (s *countedSubscription) Close(ctx context.Context) error {
	s.once.Do(func() { s.open.Add(-1) })
	return s.Subscription.Close(ctx)
}

func (q *gatedQueue) Subscribe(ctx context.Context, topic, group string, handler queue.Handler, opts ...queue.SubscribeOption) (queue.Subscription, error) {
	if topic != q.topic {
		return q.Queue.Subscribe(ctx, topic, group, handler, opts...)
	}
	q.arrived <- struct{}{}
	select {
	case <-time.After(300 * time.Millisecond): // the other caller never came: no race to force
	case q.arrived <- struct{}{}:
	case <-q.arrived:
	}
	sub, err := q.Queue.Subscribe(ctx, topic, group, handler, opts...)
	if err != nil {
		return nil, err
	}
	q.open.Add(1)
	return &countedSubscription{Subscription: sub, open: &q.open}, nil
}

// Two requests can both find a newly marked key without a consumer and both subscribe
// to its topic. Only one subscription may be left: a second one would keep consuming
// the topic with nothing to close it.
func TestRunner_ConcurrentSubscribesLeaveOneConsumer(t *testing.T) {
	inner, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	q := &gatedQueue{Queue: inner, topic: keyTopic("key-new"), arrived: make(chan struct{}, 1)}
	engine := NewEngine(EngineOptions{})
	runner := NewRunner(q, engine, func(context.Context) ([]EvalKey, error) { return nil, nil }, newMemoryJobStore(), RunnerOptions{PollInterval: 5 * time.Millisecond, SyncInterval: time.Hour})
	require.NoError(t, runner.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = inner.Close(ctx)
	})

	key := EvalKey{ID: "key-new", Provider: "openai", Concurrency: 1}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, runner.ensureSubscribed(context.Background(), key, false))
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, q.open.Load(), "one consumer on the key's topic")
}

// The in-memory queue forgets its messages when the gateway restarts, while the jobs
// stay stored as queued or running. A runner on such a queue puts them back on start.
func TestRunner_ResumesUnfinishedJobsOnAQueueThatForgets(t *testing.T) {
	q, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	engine := NewEngine(EngineOptions{})
	engine.Register(EvaluatorTypeAI, "", func(Definition, Config) (Evaluator, error) {
		return &fakeEvaluator{fn: scoring(BoolScore(true))}, nil
	})
	RegisterProgrammaticEvaluator(engine, ProgrammaticOptions{})
	store := newMemoryJobStore()
	store.add(programmaticJob("local-waiting"))
	store.addStranded(aiJob("key-waiting", "openai", "gpt-4o-mini"), "key-a", 0)
	store.addStranded(aiJob("key-running", "openai", "gpt-4o-mini"), "key-a", 0)
	store.markStarted("key-running", "key-a")
	store.addStranded(aiJob("gone-waiting", "openai", "gpt-4o-mini"), "key-gone", 0)
	store.addStranded(aiJob("gone-running", "openai", "gpt-4o-mini"), "key-gone", 0)
	store.markStarted("gone-running", "key-gone")
	done := programmaticJob("already-done")
	store.add(done)
	require.NoError(t, store.CompleteJob(context.Background(), "already-done", Result{Score: BoolScore(true)}))

	keys := []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 2, Supports: anyModel}}
	runner := NewRunner(q, engine, func(context.Context) ([]EvalKey, error) { return keys, nil }, store, RunnerOptions{PollInterval: 5 * time.Millisecond, Volatile: true})
	require.NoError(t, runner.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = q.Close(ctx)
	})

	h := &runnerHarness{queue: q, runner: runner, store: store}
	h.await(t, "local-waiting", "key-waiting", "key-running", "gone-waiting", "gone-running")
	for _, id := range []string{"local-waiting", "key-waiting", "key-running", "gone-waiting"} {
		result, _ := store.result(id)
		assert.Equal(t, ScoreStatusOK, result.Score.Status, id)
	}
	assert.Equal(t, "key-a", store.keyOf("gone-waiting"), "a waiting job moves to a key that is still marked")
	lost, _ := store.result("gone-running")
	assert.Equal(t, ScoreStatusError, lost.Score.Status, "a job that had started on a key that is gone cannot be moved")
	store.mu.Lock()
	assert.Equal(t, 1, store.completed["already-done"], "a finished job is not run again")
	store.mu.Unlock()
}

// Two instances that both sweep must requeue a stranded job once between them.
func TestRunner_SweepsOfTwoInstancesRequeueOnce(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 2, Supports: anyModel}}, 20*time.Millisecond)
	h.store.addStranded(aiJob("stranded", "openai", "gpt-4o-mini"), "key-gone", time.Hour)

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, h.runner.Sweep(context.Background()))
		}()
	}
	wg.Wait()
	h.await(t, "stranded")
	time.Sleep(80 * time.Millisecond)

	_, runs := h.tracker.snapshot()
	assert.Equal(t, 1, runs["key-a"], "the job ran once")
}

// A message for a job that already completed is acknowledged without running it again.
// This is what makes a late duplicate harmless.
func TestRunner_SkipsJobsThatAlreadyCompleted(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0)
	job := aiJob("once", "openai", "gpt-4o-mini")
	h.submit(t, job)
	h.await(t, "once")

	require.NoError(t, h.runner.publish(context.Background(), h.store.view("once"), "evaljob:once:duplicate", "key-a"))
	time.Sleep(80 * time.Millisecond)

	_, runs := h.tracker.snapshot()
	assert.Equal(t, 1, runs["key-a"])
}

// Results are kept for the retention period and then removed, so the table does not
// grow without bound.
func TestRunner_PurgesResultsPastRetention(t *testing.T) {
	q, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	store := newMemoryJobStore()
	runner := NewRunner(q, NewEngine(EngineOptions{}), func(context.Context) ([]EvalKey, error) { return nil, nil }, store,
		RunnerOptions{PollInterval: 5 * time.Millisecond, Retention: 48 * time.Hour})
	require.NoError(t, runner.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = q.Close(ctx)
	})

	require.NoError(t, runner.Sweep(context.Background()))
	require.NoError(t, runner.Sweep(context.Background()))

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Len(t, store.purgedAt, 1, "the purge is rate limited, not run on every sweep")
	assert.WithinDuration(t, time.Now().Add(-48*time.Hour), store.purgedAt[0], time.Minute)
}
