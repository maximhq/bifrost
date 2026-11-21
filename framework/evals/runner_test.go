package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
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
	completed map[string]int       // completions per job
	skipped   [][]string           // the keys each StaleJobs call was told are being consumed
	claims    map[string]string    // job id to the delivery that started it
	startedAt map[string]time.Time // when each started job started
	routedAt  map[string]time.Time // when each job was last routed to a key
	pages     []int                // the limit of each UnfinishedJobs call
	failWrite map[string]int       // completions of a job that fail before one succeeds
	loads     map[string]int       // LoadJob calls per job: one per handled delivery
}

func newMemoryJobStore() *memoryJobStore {
	return &memoryJobStore{
		jobs: map[string]*Job{}, started: map[string]string{}, results: map[string]Result{}, completed: map[string]int{},
		routed: map[string]string{}, queuedAt: map[string]time.Time{},
		claims: map[string]string{}, startedAt: map[string]time.Time{}, routedAt: map[string]time.Time{}, failWrite: map[string]int{}, loads: map[string]int{},
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
	s.routedAt[job.ID] = time.Now().Add(-age)
}

// view returns a job as the store would load it now.
func (s *memoryJobStore) view(id string) *Job {
	job := *s.jobs[id]
	job.KeyID = s.routed[id]
	_, job.Done = s.results[id]
	_, job.Started = s.started[id]
	job.RoutedAt = s.routedAt[id]
	job.CreatedAt = s.queuedAt[id]
	job.ClaimID, job.StartedAt = s.claims[id], s.startedAt[id]
	return &job
}

// UnfinishedJobs returns a page of the jobs without a result, oldest first.
func (s *memoryJobStore) UnfinishedJobs(_ context.Context, after JobCursor, limit int) ([]*Job, JobCursor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pages = append(s.pages, limit)
	var ids []string
	for id := range s.jobs {
		if _, done := s.results[id]; !done {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(a, b string) int {
		if c := s.queuedAt[a].Compare(s.queuedAt[b]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	var out []*Job
	next := after
	for _, id := range ids {
		at := s.queuedAt[id]
		if after.ID != "" && (at.Before(after.CreatedAt) || (at.Equal(after.CreatedAt) && id <= after.ID)) {
			continue
		}
		if limit > 0 && len(out) == limit {
			break
		}
		out = append(out, s.view(id))
		next = JobCursor{CreatedAt: at, ID: id}
	}
	if limit <= 0 || len(out) < limit {
		next = JobCursor{} // the last page
	}
	return out, next, nil
}

// markStarted records that a job began running on a key, as a worker that then died
// would have left it.
func (s *memoryJobStore) markStarted(id, keyID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started[id] = keyID
	s.startedAt[id] = time.Now()
}

func (s *memoryJobStore) LoadJob(_ context.Context, id string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads[id]++
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
	s.routedAt[id] = time.Now()
	return true, nil
}

func (s *memoryJobStore) StaleJobs(_ context.Context, queuedBefore, routedBefore time.Time, skipKeys []string, _ int) ([]*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipped = append(s.skipped, skipKeys)
	var out []*Job
	// In ID order, as a store pages them in a fixed order.
	for _, id := range slices.Sorted(maps.Keys(s.jobs)) {
		_, started := s.started[id]
		_, done := s.results[id]
		if started && !done && s.routed[id] != "" && !slices.Contains(skipKeys, s.routed[id]) && s.startedAt[id].Before(routedBefore) {
			out = append(out, s.view(id))
			continue
		}
		if s.routed[id] != "" && slices.Contains(skipKeys, s.routed[id]) && !s.routedAt[id].Before(routedBefore) {
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

// StartJob claims a job the way the execution store does: queued on the delivery's
// key, or running under the same delivery, or running since before claim.StartedBefore.
func (s *memoryJobStore) StartJob(_ context.Context, id, keyID string, claim JobClaim) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, done := s.results[id]; done || s.routed[id] != keyID {
		return false, nil
	}
	if _, started := s.started[id]; started && s.claims[id] != claim.DeliveryID && !s.startedAt[id].Before(claim.StartedBefore) {
		return false, nil
	}
	s.started[id], s.claims[id], s.startedAt[id] = keyID, claim.DeliveryID, time.Now()
	return true, nil
}

func (s *memoryJobStore) CompleteJob(_ context.Context, id string, result Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failWrite[id] > 0 {
		s.failWrite[id]--
		return errors.New("the store is unavailable")
	}
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

// awaitLoads waits until the job has been loaded n times: every delivery's handler loads
// the job first, so after this the deliveries in question have been handled.
func (s *memoryJobStore) awaitLoads(t *testing.T, id string, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.loads[id] >= n
	}, 10*time.Second, 2*time.Millisecond, "the deliveries for %s were never handled", id)
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
	gate    *atomic.Pointer[chan struct{}] // when set, the AI evaluator waits for it to close
}

// newRunnerHarness builds a runner on an in-memory queue. The AI evaluator is a fake
// that records which key it ran on and holds its slot for hold.
func newRunnerHarness(t *testing.T, keys []EvalKey, hold time.Duration) *runnerHarness {
	t.Helper()
	store := newMemoryJobStore()
	h := newRunnerHarnessOn(t, keys, hold, store)
	h.store = store
	return h
}

// newRunnerHarnessOn is newRunnerHarness over any JobStore. Its store field is nil.
func newRunnerHarnessOn(t *testing.T, keys []EvalKey, hold time.Duration, store JobStore) *runnerHarness {
	t.Helper()
	q, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)

	tracker := newKeyTracker()
	gate := &atomic.Pointer[chan struct{}]{}
	engine := NewEngine(EngineOptions{})
	engine.Register(EvaluatorTypeAI, "", func(Definition, Config) (Evaluator, error) {
		return &fakeEvaluator{fn: func(ctx context.Context, _ Input) (Result, error) {
			key := KeyIDFromContext(ctx)
			tracker.enter(key)
			defer tracker.leave(key)
			if g := gate.Load(); g != nil {
				<-*g
			}
			time.Sleep(hold)
			return Result{Score: BoolScore(true), Reasoning: "ran on " + key}, nil
		}}, nil
	})
	RegisterProgrammaticEvaluator(engine, ProgrammaticOptions{})

	current := &atomic.Pointer[[]EvalKey]{}
	current.Store(&keys)
	runner := NewRunner(q, engine, func(context.Context) ([]EvalKey, error) { return *current.Load(), nil }, store, RunnerOptions{PollInterval: 5 * time.Millisecond})
	require.NoError(t, runner.Start(context.Background()))
	t.Cleanup(func() {
		if g := gate.Swap(nil); g != nil {
			select {
			case <-*g:
			default:
				close(*g)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = q.Close(ctx)
	})
	return &runnerHarness{queue: q, runner: runner, tracker: tracker, keys: current, gate: gate}
}

// hold makes the AI evaluator wait until the returned release is called.
func (h *runnerHarness) hold() (release func()) {
	ch := make(chan struct{})
	h.gate.Store(&ch)
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

// awaitRuns waits until the AI evaluator has started n runs on a key.
func (h *runnerHarness) awaitRuns(t *testing.T, key string, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, runs := h.tracker.snapshot()
		return runs[key] >= n
	}, 10*time.Second, 2*time.Millisecond, "the evaluation on %s never started", key)
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
	}, 0)
	release := h.hold()

	var ids []string
	for i := range 6 {
		id := fmt.Sprintf("job-%d", i)
		h.submit(t, aiJob(id, "openai", "gpt-4o-mini"))
		ids = append(ids, id)
	}
	// The first job holds key-a's only slot, so the other five are still queued on it.
	h.awaitRuns(t, "key-a", 1)

	// key-a is unmarked and key-b takes over while the jobs are queued on key-a.
	replacement := []EvalKey{{ID: "key-b", Provider: "openai", Concurrency: 2, Supports: anyModel}}
	h.keys.Store(&replacement)
	require.NoError(t, h.runner.Sync(context.Background()))
	release()
	h.await(t, ids...)

	_, runs := h.tracker.snapshot()
	assert.Equal(t, 1, runs["key-a"], "only the job that had started ran on key-a")
	assert.Equal(t, 5, runs["key-b"], "the queued jobs moved to the key that is now eligible")
}

// With no eligible key left, a queued job ends with an error instead of waiting forever.
func TestRunner_FailsQueuedJobsWhenNoKeyRemains(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0)
	release := h.hold()

	var ids []string
	for i := range 4 {
		id := fmt.Sprintf("job-%d", i)
		h.submit(t, aiJob(id, "openai", "gpt-4o-mini"))
		ids = append(ids, id)
	}
	h.awaitRuns(t, "key-a", 1)
	none := []EvalKey{}
	h.keys.Store(&none)
	require.NoError(t, h.runner.Sync(context.Background()))
	release()
	h.await(t, ids...)

	failed := 0
	for _, id := range ids {
		result, _ := h.store.result(id)
		if result.Score.Status == ScoreStatusError {
			failed++
			assert.Contains(t, result.Reasoning, "enabled for evaluations")
		}
	}
	assert.Equal(t, 3, failed, "every job still queued when the last key went ends with an error")
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
	// Old, but on a key that still has a consumer, and routed there too recently for its
	// message to count as lost.
	h.store.addStranded(aiJob("waiting", "openai", "gpt-4o-mini"), "key-a", 5*time.Minute)
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

// The result of a finished evaluation is written with a live context, even when the
// delivery context ends right after the evaluation (a shutdown, a lease that ran out).
// A failed write would leave the job running with its result lost.
func TestRunner_CompletesWithALiveContext(t *testing.T) {
	r := newRunnerHarness(t, nil, 0).runner
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var writeErr error
	job := programmaticJob("live-ctx")
	job.Complete = func(writeCtx context.Context, _ Result) error {
		cancel() // the delivery context ends while the write is in progress
		writeErr = writeCtx.Err()
		return writeCtx.Err()
	}
	require.NoError(t, r.complete(ctx, job, Result{Score: NumberScore(1)}))
	assert.NoError(t, writeErr, "the store write must not inherit the canceled delivery context")
}

// A delivery whose context ended before its result was stored has lost its lease: the
// queue delivers the job again, and that delivery stores the result. Storing this one
// as well would let two attempts write.
func TestRunner_DoesNotStoreAResultAfterTheLeaseIsLost(t *testing.T) {
	r := newRunnerHarness(t, nil, 0).runner
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stored := false
	job := programmaticJob("lease-lost")
	job.Complete = func(context.Context, Result) error {
		stored = true
		return nil
	}
	err := r.complete(ctx, job, Result{Score: NumberScore(1)})
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, stored, "a result from an attempt that lost its lease is not stored")
}

// A nil job is refused, not dereferenced.
func TestRunner_SubmitRefusesANilJob(t *testing.T) {
	r := newRunnerHarness(t, nil, 0).runner
	assert.Error(t, r.Submit(context.Background(), nil))
	assert.Error(t, r.Submit(context.Background(), &Job{}))
}

// An evaluation cut short by the delivery context ending is not a result. It is left
// for the queue to deliver again rather than stored as a failure.
func TestRunner_DoesNotStoreACancellationAsAResult(t *testing.T) {
	r := newRunnerHarness(t, nil, 0).runner
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stored := false
	job := programmaticJob("canceled")
	job.Complete = func(context.Context, Result) error {
		stored = true
		return nil
	}
	err := r.complete(ctx, job, errorResult(context.Canceled))
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, stored, "a canceled evaluation is redelivered, not recorded as failed")
}

// A job that was moved to another key leaves its old message behind. When the old key
// is consumed again, that message must not run the job a second time.
func TestRunner_SkipsAMessageForAJobOnAnotherKey(t *testing.T) {
	keys := []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}, {ID: "key-b", Provider: "openai", Concurrency: 1, Supports: anyModel}}
	h := newRunnerHarness(t, keys, 0)
	h.store.addStranded(aiJob("moved", "openai", "gpt-4o-mini"), "key-b", 0)
	require.NoError(t, h.queue.Publish(context.Background(), &queue.Message{ID: "left-behind", Topic: keyTopic("key-a"), Payload: []byte(`{"job_id":"moved"}`)}))

	h.store.awaitLoads(t, "moved", 1)
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

// failingSubscribeQueue fails the next Subscribe to one topic when failNext is set, and
// remembers the subscriptions it handed out for that topic.
type failingSubscribeQueue struct {
	queue.Queue
	topic    string
	failNext atomic.Bool
	mu       sync.Mutex
	subs     []queue.Subscription
	calls    int
}

func (q *failingSubscribeQueue) Subscribe(ctx context.Context, topic, group string, handler queue.Handler, opts ...queue.SubscribeOption) (queue.Subscription, error) {
	if topic != q.topic {
		return q.Queue.Subscribe(ctx, topic, group, handler, opts...)
	}
	q.mu.Lock()
	q.calls++
	q.mu.Unlock()
	if q.failNext.Swap(false) {
		return nil, errors.New("the queue is unavailable")
	}
	sub, err := q.Queue.Subscribe(ctx, topic, group, handler, opts...)
	if err == nil {
		q.mu.Lock()
		q.subs = append(q.subs, sub)
		q.mu.Unlock()
	}
	return sub, err
}

// A resubscribe closes the old subscription before it subscribes again. When that
// subscribe fails, the closed subscription must not stay recorded as the key's
// consumer: the next sync would see it as current and leave the key unconsumed.
func TestRunner_FailedResubscribeIsRetried(t *testing.T) {
	inner, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	q := &failingSubscribeQueue{Queue: inner, topic: keyTopic("key-a")}
	runner := NewRunner(q, NewEngine(EngineOptions{}), func(context.Context) ([]EvalKey, error) { return nil, nil }, newMemoryJobStore(), RunnerOptions{PollInterval: 5 * time.Millisecond, SyncInterval: time.Hour})
	require.NoError(t, runner.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = inner.Close(ctx)
	})
	ctx := context.Background()
	key := EvalKey{ID: "key-a", Provider: "openai", Concurrency: 4}
	require.NoError(t, runner.ensureSubscribed(ctx, key, false))
	q.failNext.Store(true)
	require.Error(t, runner.ensureSubscribed(ctx, EvalKey{ID: "key-a", Provider: "openai", Concurrency: 2}, true))
	require.NoError(t, runner.ensureSubscribed(ctx, key, true))

	q.mu.Lock()
	calls, first := q.calls, q.subs[0]
	q.mu.Unlock()
	assert.Equal(t, 3, calls, "the key is subscribed again after the failed resubscribe")
	runner.mu.Lock()
	current := runner.subs["key-a"].sub
	runner.mu.Unlock()
	assert.NotSame(t, first, current, "the recorded consumer is not the subscription that was closed")
}

// The periodic sync runs under a context that Close cancels, so a key source that hangs
// does not outlive the runner, and Close waits for the loop to end.
func TestRunner_CloseCancelsThePeriodicSync(t *testing.T) {
	q, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close(context.Background()) })
	var block atomic.Bool
	entered := make(chan struct{}, 1)
	sawCancel := make(chan struct{}, 1)
	giveUp := make(chan struct{})
	t.Cleanup(func() { close(giveUp) })
	keys := func(ctx context.Context) ([]EvalKey, error) {
		if !block.Load() {
			return nil, nil
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			select {
			case sawCancel <- struct{}{}:
			default:
			}
			return nil, ctx.Err()
		case <-giveUp:
			return nil, errors.New("the test ended")
		}
	}
	runner := NewRunner(q, NewEngine(EngineOptions{}), keys, newMemoryJobStore(), RunnerOptions{PollInterval: 5 * time.Millisecond, SyncInterval: 10 * time.Millisecond})
	require.NoError(t, runner.Start(context.Background()))
	block.Store(true)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the periodic sync never ran")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	began := time.Now()
	require.NoError(t, runner.Close(ctx))
	assert.Less(t, time.Since(began), 2*time.Second, "Close returns before its deadline")
	select {
	case <-sawCancel:
	default:
		t.Fatal("the key source of the periodic sync never saw its context end")
	}
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

// failingRouteStore fails every RouteJob of one job, as a store that cannot write that
// row would.
type failingRouteStore struct {
	*memoryJobStore
	failID string
}

func (s *failingRouteStore) RouteJob(ctx context.Context, id, fromKey, toKey string) (bool, error) {
	if id == s.failID {
		return false, errors.New("the store cannot move this job")
	}
	return s.memoryJobStore.RouteJob(ctx, id, fromKey, toKey)
}

// One job the sweep cannot recover must not keep it from recovering the others: the
// sweep goes on through the batch and reports the failure at the end.
func TestRunner_SweepContinuesPastAFailedRecovery(t *testing.T) {
	inner := newMemoryJobStore()
	store := &failingRouteStore{memoryJobStore: inner, failID: "bad"}
	h := newRunnerHarnessOn(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 2, Supports: anyModel}}, 0, store)
	h.store = inner
	inner.addStranded(aiJob("bad", "openai", "gpt-4o-mini"), "key-gone", time.Hour)
	inner.addStranded(aiJob("good", "openai", "gpt-4o-mini"), "key-gone", time.Hour)

	assert.Error(t, h.runner.Sweep(context.Background()), "the failed recovery is reported")
	assert.Equal(t, "key-a", inner.routedTo("good"), "the other stranded job was moved anyway")
}

// A job that started on a key which was then removed, on a queue that keeps its
// messages, has nothing left to deliver it: no instance consumes the removed key's
// topic, and a started job cannot move. Once its claim is long expired the sweep ends it
// with an error, so it does not stay running for ever.
func TestRunner_SweepEndsAStartedJobOnARemovedKey(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0)
	h.store.addStranded(aiJob("lost", "openai", "gpt-4o-mini"), "key-gone", 2*time.Hour)
	h.store.mu.Lock()
	h.store.started["lost"], h.store.claims["lost"], h.store.startedAt["lost"] = "key-gone", "dead-delivery", time.Now().Add(-2*time.Hour)
	h.store.mu.Unlock()

	require.NoError(t, h.runner.Sweep(context.Background()))
	result, done := h.store.result("lost")
	require.True(t, done, "the started job on the removed key was ended")
	assert.Equal(t, ScoreStatusError, result.Score.Status)
	assert.Contains(t, result.Reasoning, "no longer enabled for evaluations")
}

// A job on a removed key that started recently may still have a live owner, so the
// sweep leaves it running.
func TestRunner_SweepLeavesARecentlyStartedJobOnARemovedKey(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0)
	h.store.addStranded(aiJob("fresh", "openai", "gpt-4o-mini"), "key-gone", 2*time.Hour)
	h.store.mu.Lock()
	h.store.started["fresh"], h.store.claims["fresh"], h.store.startedAt["fresh"] = "key-gone", "owner", time.Now()
	h.store.mu.Unlock()

	require.NoError(t, h.runner.Sweep(context.Background()))
	_, done := h.store.result("fresh")
	assert.False(t, done, "a job whose claim may still be live is not ended")
	assert.Equal(t, "key-gone", h.store.routedTo("fresh"), "a started job is not moved")
}

// A job restarted since the sweep listed it belongs to the delivery that restarted it,
// so the sweep's claim fails and the job is left to that delivery.
func TestRunner_SweepLeavesAStartedJobThatWasRestarted(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0)
	h.store.addStranded(aiJob("restarted", "openai", "gpt-4o-mini"), "key-gone", 2*time.Hour)
	listed := time.Now().Add(-2 * time.Hour)
	h.store.mu.Lock()
	h.store.started["restarted"], h.store.claims["restarted"], h.store.startedAt["restarted"] = "key-gone", "old-delivery", listed
	h.store.mu.Unlock()
	job := h.store.view("restarted")
	h.store.mu.Lock()
	h.store.claims["restarted"], h.store.startedAt["restarted"] = "new-delivery", time.Now()
	h.store.mu.Unlock()

	require.NoError(t, h.runner.endLostJob(context.Background(), job))
	_, done := h.store.result("restarted")
	assert.False(t, done, "a job another delivery restarted is not ended")
}

// A message for a job that already completed is acknowledged without running it again.
// This is what makes a late duplicate harmless.
func TestRunner_SkipsJobsThatAlreadyCompleted(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0)
	job := aiJob("once", "openai", "gpt-4o-mini")
	h.submit(t, job)
	h.await(t, "once")

	h.store.mu.Lock()
	before := h.store.loads["once"]
	h.store.mu.Unlock()
	require.NoError(t, h.runner.publish(context.Background(), h.store.view("once"), "evaljob:once:duplicate", "key-a"))
	h.store.awaitLoads(t, "once", before+1)

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

// volatileRunner starts a runner on a fresh in-memory queue, as a gateway without a log
// store does, over the given store and keys.
func volatileRunner(t *testing.T, store *memoryJobStore, keys []EvalKey) (*Runner, *keyTracker) {
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
			time.Sleep(50 * time.Millisecond) // still running when startup reads the store
			return Result{Score: BoolScore(true)}, nil
		}}, nil
	})
	RegisterProgrammaticEvaluator(engine, ProgrammaticOptions{})
	runner := NewRunner(q, engine, func(context.Context) ([]EvalKey, error) { return keys, nil }, store, RunnerOptions{PollInterval: 5 * time.Millisecond, Volatile: true})
	require.NoError(t, runner.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = q.Close(ctx)
	})
	return runner, tracker
}

// Two messages for one job, from a retried publish or a sweep that raced a handler, run
// it once. The second delivery's claim is refused.
func TestRunner_RunsAJobOnceForTwoMessages(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 2, Supports: anyModel}}, 30*time.Millisecond)
	h.store.addStranded(aiJob("twice", "openai", "gpt-4o-mini"), "key-a", 0)
	for _, id := range []string{"first", "second"} {
		require.NoError(t, h.queue.Publish(context.Background(), &queue.Message{ID: id, Topic: keyTopic("key-a"), Payload: []byte(`{"job_id":"twice"}`)}))
	}
	h.await(t, "twice")
	h.store.awaitLoads(t, "twice", 2)
	_, runs := h.tracker.snapshot()
	assert.Equal(t, 1, runs["key-a"], "the job ran once")
}

// On a queue that forgets, startup puts the stored jobs back and then sweeps. A job
// stranded on a removed key is published once between the two, and runs once.
func TestRunner_VolatileStartRunsAStrandedJobOnce(t *testing.T) {
	store := newMemoryJobStore()
	store.addStranded(aiJob("stranded", "openai", "gpt-4o-mini"), "key-gone", time.Hour)
	_, tracker := volatileRunner(t, store, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 2, Supports: anyModel}})
	h := &runnerHarness{store: store}
	h.await(t, "stranded")
	time.Sleep(100 * time.Millisecond)
	_, runs := tracker.snapshot()
	assert.Equal(t, 1, runs["key-a"])
	store.mu.Lock()
	assert.Equal(t, 1, store.completed["stranded"])
	store.mu.Unlock()
}

// A resume that fails, here the write that ends a job whose key is gone, is retried on
// a later sync instead of leaving the job running with nothing queued for it.
func TestRunner_RetriesAFailedResume(t *testing.T) {
	store := newMemoryJobStore()
	store.addStranded(aiJob("orphan", "openai", "gpt-4o-mini"), "key-gone", time.Hour)
	store.markStarted("orphan", "key-gone")
	store.failWrite["orphan"] = 1
	runner, _ := volatileRunner(t, store, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}})
	_, done := store.result("orphan")
	require.False(t, done, "the first write failed")

	require.NoError(t, runner.Sync(context.Background()))
	result, done := store.result("orphan")
	require.True(t, done, "the next sync finished the resume")
	assert.Equal(t, ScoreStatusError, result.Score.Status)
}

// Startup recovery reads unfinished jobs a page at a time, never the whole backlog.
func TestRunner_ResumeReadsInPages(t *testing.T) {
	store := newMemoryJobStore()
	ids := make([]string, 0, 2*runnerSweepBatch+50)
	for i := range 2*runnerSweepBatch + 50 {
		id := fmt.Sprintf("local-%04d", i)
		store.add(programmaticJob(id))
		ids = append(ids, id)
	}
	volatileRunner(t, store, nil)
	h := &runnerHarness{store: store}
	h.await(t, ids...)
	store.mu.Lock()
	defer store.mu.Unlock()
	require.GreaterOrEqual(t, len(store.pages), 3)
	for _, limit := range store.pages {
		assert.Positive(t, limit)
		assert.LessOrEqual(t, limit, runnerSweepBatch)
	}
}

// droppingResumer leaves one job out of its pages, as a store does for a row it cannot
// decode, while its cursor still moves past that row.
type droppingResumer struct {
	*memoryJobStore
	drop string
}

func (d *droppingResumer) UnfinishedJobs(ctx context.Context, after JobCursor, limit int) ([]*Job, JobCursor, error) {
	jobs, next, err := d.memoryJobStore.UnfinishedJobs(ctx, after, limit)
	return slices.DeleteFunc(jobs, func(job *Job) bool { return job.ID == d.drop }), next, err
}

// A page that is short only because a row was left out is not the last page: recovery
// follows the cursor, so the jobs after it are resumed too.
func TestRunner_ResumeContinuesPastADroppedRow(t *testing.T) {
	store := newMemoryJobStore()
	base := time.Now().Add(-time.Minute)
	var ids []string
	for i := range runnerSweepBatch + 5 {
		id := fmt.Sprintf("local-%04d", i)
		store.add(programmaticJob(id))
		store.queuedAt[id] = base.Add(time.Duration(i) * time.Millisecond)
		if i > 0 {
			ids = append(ids, id)
		}
	}
	q, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	engine := NewEngine(EngineOptions{})
	RegisterProgrammaticEvaluator(engine, ProgrammaticOptions{})
	runner := NewRunner(q, engine, func(context.Context) ([]EvalKey, error) { return nil, nil }, &droppingResumer{memoryJobStore: store, drop: "local-0000"},
		RunnerOptions{PollInterval: 5 * time.Millisecond, Volatile: true, SyncInterval: time.Hour})
	require.NoError(t, runner.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = q.Close(ctx)
	})
	h := &runnerHarness{store: store}
	h.await(t, ids...)
}

// A job routed to a key long ago with nothing on that key's queue lost its message
// between routing and publishing. The sweep publishes it again.
func TestRunner_SweepRepublishesAJobWhoseMessageWasLost(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0)
	h.store.addStranded(aiJob("lost", "openai", "gpt-4o-mini"), "key-a", time.Hour)
	require.NoError(t, h.runner.Sweep(context.Background()))
	h.await(t, "lost")
	assert.Equal(t, "key-a", h.store.routedTo("lost"))
}

// A job that needs no model, stored and then never published (the gateway stopped in between),
// is put on the local queue by the sweep once its message counts as lost.
func TestRunner_SweepPublishesALocalJobThatNeverReachedTheQueue(t *testing.T) {
	h := newRunnerHarness(t, nil, 0)
	h.store.addStranded(programmaticJob("local-lost"), "", (runnerLostMessageFactor+1)*defaultRunnerStrandedAfter)
	require.NoError(t, h.runner.Sweep(context.Background()))
	h.await(t, "local-lost")
	result, _ := h.store.result("local-lost")
	assert.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
}

// A key that no longer serves the job's model (its model list changed) hands the job to a key
// that does, instead of running it where the call would fail.
func TestRunner_MessageOnAKeyThatNoLongerServesTheModelMovesTheJob(t *testing.T) {
	notMini := func(model string) bool { return model != "gpt-4o-mini" }
	h := newRunnerHarness(t, []EvalKey{
		{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: notMini},
		{ID: "key-b", Provider: "openai", Concurrency: 1, Supports: anyModel},
	}, 0)
	h.store.addStranded(aiJob("moved", "openai", "gpt-4o-mini"), "key-a", time.Second)
	require.NoError(t, h.runner.send(context.Background(), "moved", keyTopic("key-a"), "key-a"))
	h.await(t, "moved")
	_, runs := h.tracker.snapshot()
	assert.Equal(t, 1, runs["key-b"], "the job ran on the key that serves its model")
	assert.Zero(t, runs["key-a"])
}

// A started job on a key that was unmarked is ended only by its own delivery, whose worker died.
// Another message for it leaves it to the worker that holds it.
func TestRunner_OnlyTheOwningDeliveryEndsAStartedJobOnAGoneKey(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0)
	h.store.addStranded(aiJob("held", "openai", "gpt-4o"), "key-a", time.Second)
	h.store.mu.Lock()
	h.store.started["held"], h.store.claims["held"], h.store.startedAt["held"] = "key-a", "owner", time.Now()
	h.store.mu.Unlock()
	h.keys.Store(&[]EvalKey{})
	handle := h.runner.handler("key-a")
	payload, _ := json.Marshal(jobMessage{JobID: "held"})
	require.NoError(t, handle(context.Background(), &queue.Delivery{Message: queue.Message{ID: "dup", Payload: payload}, DeliveryID: "someone-else"}))
	_, done := h.store.result("held")
	assert.False(t, done, "a message that does not hold the claim does not end the job")
	require.NoError(t, handle(context.Background(), &queue.Delivery{Message: queue.Message{ID: "own", Payload: payload}, DeliveryID: "owner"}))
	result, done := h.store.result("held")
	require.True(t, done, "the owner's redelivery ends it")
	assert.Equal(t, ScoreStatusError, result.Score.Status)
}

// A dead-lettered message ends a started job only when its delivery is the one that
// started it. Another message's dead letter leaves the job to the delivery that holds it.
func TestRunner_DeadLetterOfAnotherDeliveryLeavesAStartedJob(t *testing.T) {
	h := newRunnerHarness(t, nil, 0)
	h.store.addStranded(programmaticJob("held"), "", time.Second)
	h.store.mu.Lock()
	h.store.started["held"], h.store.claims["held"], h.store.startedAt["held"] = "", "owner", time.Now()
	h.store.mu.Unlock()
	handle := h.runner.deadHandler()
	payload, _ := json.Marshal(jobMessage{JobID: "held"})
	dead := func(original string) *queue.Delivery {
		return &queue.Delivery{Message: queue.Message{ID: "dlq:" + original, Payload: payload, Headers: map[string]string{queue.HeaderDeadLetterDeliveryID: original}}, DeliveryID: "dlq:" + original}
	}
	require.NoError(t, handle(context.Background(), dead("someone-else")))
	_, done := h.store.result("held")
	assert.False(t, done, "the dead letter of a delivery that does not hold the claim does not end the job")
	require.NoError(t, handle(context.Background(), dead("owner")))
	result, done := h.store.result("held")
	require.True(t, done, "the owner's dead letter ends it")
	assert.Equal(t, ScoreStatusError, result.Score.Status)
}

// A job whose result cannot be written in every attempt is dead-lettered, and the dead-letter
// handler ends it with an error result, so it does not stay running with no message left.
func TestRunner_DeadLetteredJobEndsWithAnError(t *testing.T) {
	h := newRunnerHarness(t, nil, 0)
	job := programmaticJob("dead")
	h.store.mu.Lock()
	h.store.failWrite["dead"] = defaultRunnerMaxAttempts
	h.store.mu.Unlock()
	h.submit(t, job)
	require.Eventually(t, func() bool { _, done := h.store.result("dead"); return done }, 30*time.Second, 20*time.Millisecond, "the job never ended")
	result, _ := h.store.result("dead")
	assert.Equal(t, ScoreStatusError, result.Score.Status, result.Reasoning)
}
