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

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/maximhq/bifrost/framework/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
	failRoute map[string]bool      // jobs whose RouteJob fails
}

func newMemoryJobStore() *memoryJobStore {
	return &memoryJobStore{
		jobs: map[string]*Job{}, started: map[string]string{}, results: map[string]Result{}, completed: map[string]int{},
		routed: map[string]string{}, queuedAt: map[string]time.Time{},
		claims: map[string]string{}, startedAt: map[string]time.Time{}, routedAt: map[string]time.Time{}, failWrite: map[string]int{}, loads: map[string]int{},
		failRoute: map[string]bool{},
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

func (s *memoryJobStore) RouteJob(_ context.Context, id, fromKey, toKey string, routedAt time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failRoute[id] {
		return false, errors.New("the store is unavailable")
	}
	_, known := s.jobs[id]
	_, done := s.results[id]
	if _, started := s.started[id]; !known || done || started || s.routed[id] != fromKey {
		return false, nil
	}
	s.routed[id] = toKey
	s.routedAt[id] = routedAt
	return true, nil
}

func (s *memoryJobStore) StaleJobs(_ context.Context, queuedBefore, routedBefore time.Time, skipKeys []string, after JobCursor, limit int) ([]*Job, JobCursor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipped = append(s.skipped, skipKeys)
	// Oldest first, by creation time and then ID, as the store pages them.
	ids := slices.SortedFunc(maps.Keys(s.jobs), func(a, b string) int {
		if c := s.queuedAt[a].Compare(s.queuedAt[b]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	var out []*Job
	read := 0
	next := JobCursor{}
	for _, id := range ids {
		at := s.queuedAt[id]
		if after.ID != "" && (at.Before(after.CreatedAt) || (at.Equal(after.CreatedAt) && id <= after.ID)) {
			continue
		}
		if limit > 0 && read == limit {
			break
		}
		_, started := s.started[id]
		_, done := s.results[id]
		lost := started && !done && s.routed[id] != "" && (!slices.Contains(skipKeys, s.routed[id]) || s.claims[id] == configstore.EvalEndLostClaim) && s.startedAt[id].Before(routedBefore)
		waiting := s.routed[id] != "" && slices.Contains(skipKeys, s.routed[id]) && !s.routedAt[id].Before(routedBefore)
		if !lost && (waiting || started || done || !at.Before(queuedBefore)) {
			continue
		}
		read++
		next = JobCursor{CreatedAt: at, ID: id}
		out = append(out, s.view(id))
	}
	if limit <= 0 || read < limit {
		next = JobCursor{} // the last page
	}
	return out, next, nil
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
	_, started := s.started[id]
	if !claim.RecoverStartedAt.IsZero() && (!started || s.claims[id] != claim.DeliveryID || !s.startedAt[id].Equal(claim.RecoverStartedAt)) {
		return false, nil
	}
	if started && s.claims[id] != claim.DeliveryID && !s.startedAt[id].Before(claim.StartedBefore) {
		return false, nil
	}
	s.started[id], s.claims[id] = keyID, claim.DeliveryID
	if claim.RecoverStartedAt.IsZero() {
		s.startedAt[id] = time.Now() // a recovery claim keeps the start it matched
	} else {
		s.claims[id] = configstore.EvalEndLostClaim // and moves the claim to the sweep's own ID
	}
	return true, nil
}

func (s *memoryJobStore) CompleteJob(_ context.Context, id string, result Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failWrite[id] > 0 {
		s.failWrite[id]--
		return errors.New("the store is unavailable")
	}
	if _, done := s.results[id]; !done {
		s.results[id] = result
	}
	s.completed[id]++
	return nil
}

// TestMemoryJobStoreKeepsTheExecutionStoreContract pins the fake to what the SQL store
// does, so runner tests cannot pass on behaviour production never has: only a queued job
// moves, and a repeat completion keeps the first result while still being counted, which
// is how the "runs once" tests see a second run.
func TestMemoryJobStoreKeepsTheExecutionStoreContract(t *testing.T) {
	ctx := context.Background()
	s := newMemoryJobStore()
	moved, err := s.RouteJob(ctx, "unknown", "", "key-a", time.Now())
	require.NoError(t, err)
	assert.False(t, moved, "an unknown job does not move")

	s.add(aiJob("done", "openai", "gpt-4o-mini"))
	require.NoError(t, s.CompleteJob(ctx, "done", Result{Score: BoolScore(true), Reasoning: "first"}))
	moved, err = s.RouteJob(ctx, "done", "", "key-a", time.Now())
	require.NoError(t, err)
	assert.False(t, moved, "a completed job does not move")

	require.NoError(t, s.CompleteJob(ctx, "done", Result{Score: BoolScore(false), Reasoning: "second"}))
	result, ok := s.result("done")
	require.True(t, ok)
	assert.Equal(t, "first", result.Reasoning, "the first result stands")
	s.mu.Lock()
	assert.Equal(t, 2, s.completed["done"], "the repeat is still counted")
	s.mu.Unlock()
}

// CompleteQueuedJob completes a job only while it has not started and is still queued on
// keyID, as the execution store does.
func (s *memoryJobStore) CompleteQueuedJob(_ context.Context, id, keyID string, result Result) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[id]; !ok {
		return false, ErrJobNotFound
	}
	_, started := s.started[id]
	_, done := s.results[id]
	if started || done || s.routed[id] != keyID {
		return false, nil
	}
	if s.failWrite[id] > 0 {
		s.failWrite[id]--
		return false, errors.New("the store is unavailable")
	}
	s.results[id] = result
	s.completed[id]++
	return true, nil
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
	}, 0)
	// Every evaluation holds its slot until release, so the parallel run on key-large is
	// observed while it happens rather than hoped for within a short sleep.
	release := h.hold()
	defer release()

	var ids []string
	for i := range 8 {
		openaiID, anthropicID := fmt.Sprintf("openai-%d", i), fmt.Sprintf("anthropic-%d", i)
		h.submit(t, aiJob(openaiID, "openai", "gpt-4o-mini"))
		h.submit(t, aiJob(anthropicID, "anthropic", "claude-sonnet-5-5"))
		ids = append(ids, openaiID, anthropicID)
	}
	require.Eventually(t, func() bool {
		h.tracker.mu.Lock()
		defer h.tracker.mu.Unlock()
		return h.tracker.inFlight["key-large"] >= 2 && h.tracker.inFlight["key-small"] >= 1
	}, 5*time.Second, 2*time.Millisecond, "key-large never ran two evaluations at once")
	held, _ := h.tracker.snapshot()
	assert.Equal(t, 1, held["key-small"], "a key with concurrency 1 never runs two at once, even with every slot held")
	release()
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

// scopeRecordingStore records whether any delivery's LoadJob saw a query scope.
type scopeRecordingStore struct {
	*memoryJobStore
	scoped atomic.Bool
	loads  atomic.Int32
}

func (s *scopeRecordingStore) LoadJob(ctx context.Context, id string) (*Job, error) {
	if queryscope.FromContext(ctx) != nil {
		s.scoped.Store(true)
	}
	s.loads.Add(1)
	return s.memoryJobStore.LoadJob(ctx, id)
}

// A Submit that subscribes a key for the first time does so on behalf of the whole
// instance, so the subscription must not keep the request's values: the request context
// is recycled once the handler returns, and every later delivery on the key would read
// it, scope included.
func TestRunner_SubscriptionKeepsNoRequestValues(t *testing.T) {
	q, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	engine := NewEngine(EngineOptions{})
	engine.Register(EvaluatorTypeAI, "", func(Definition, Config) (Evaluator, error) {
		return &fakeEvaluator{fn: scoring(BoolScore(true))}, nil
	})
	current := &atomic.Pointer[[]EvalKey]{}
	none := []EvalKey{}
	current.Store(&none)
	store := &scopeRecordingStore{memoryJobStore: newMemoryJobStore()}
	runner := NewRunner(q, engine, func(context.Context) ([]EvalKey, error) { return *current.Load(), nil }, store,
		RunnerOptions{PollInterval: 5 * time.Millisecond, SyncInterval: time.Hour})
	require.NoError(t, runner.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = q.Close(ctx)
	})

	current.Store(&[]EvalKey{{ID: "key-new", Provider: "openai", Concurrency: 1, Supports: anyModel}})
	hideAll := queryscope.QueryScope(func(db *gorm.DB) *gorm.DB { return db.Where("1 = 0") })
	requestCtx, cancelRequest := context.WithCancel(queryscope.WithQueryScope(context.Background(), hideAll))
	job := aiJob("scoped", "openai", "gpt-4o-mini")
	store.add(job)
	require.NoError(t, runner.Submit(requestCtx, job))
	runner.mu.Lock()
	_, subscribed := runner.subs["key-new"]
	runner.mu.Unlock()
	require.True(t, subscribed, "the Submit subscribed the key")
	cancelRequest() // the request ends; the subscription outlives it

	require.Eventually(t, func() bool {
		_, done := store.result("scoped")
		return done
	}, 10*time.Second, 5*time.Millisecond, "the job runs after the request that queued it ended")
	assert.False(t, store.scoped.Load(), "a delivery read the request's query scope through the subscription context")
}

// A Submit that subscribes a key for the first time may run on a request context that a
// shutdown has drained (Done closed, Err nil). Subscribing must not start a watcher on it.
func TestRunner_FirstSubscribeSurvivesADrainedRequestContext(t *testing.T) {
	h := newRunnerHarness(t, nil, 0)
	h.keys.Store(&[]EvalKey{{ID: "key-new", Provider: "openai", Concurrency: 1, Supports: anyModel}})
	job := aiJob("drained", "openai", "gpt-4o-mini")
	h.store.add(job)
	require.NotPanics(t, func() { _ = h.runner.Submit(newDrainedCtx(), job) })
}

// A caller with a deadline and a Done channel (a request handler's context) subscribes a
// key for the first time. The watcher that follows the caller's end must not read the
// subscribe context while the deadline is applied to it: run under -race.
func TestRunner_SubscribeWithACallerDeadlineDoesNotRace(t *testing.T) {
	h := newRunnerHarness(t, nil, 0)
	for i := range 20 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		key := EvalKey{ID: fmt.Sprintf("key-deadline-%d", i), Provider: "openai", Concurrency: 1, Supports: anyModel}
		require.NoError(t, h.runner.ensureSubscribed(ctx, key, false))
		cancel()
	}
}

// staleKeysCtx marks a refresh whose key read is stale: it was taken before the key
// was marked, and its answer is stored after a fresher one.
type staleKeysCtx struct{}

// Submit places a job with the keys its own refresh read. A concurrent refresh that read
// the keys earlier can store its older answer in between, and Submit must not pick from
// that one and refuse the job.
func TestRunner_SubmitPicksFromItsOwnRefresh(t *testing.T) {
	q, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	engine := NewEngine(EngineOptions{})
	engine.Register(EvaluatorTypeAI, "", func(Definition, Config) (Evaluator, error) {
		return &fakeEvaluator{fn: scoring(BoolScore(true))}, nil
	})
	marked := []EvalKey{{ID: "key-new", Provider: "openai", Concurrency: 1, Supports: anyModel}}
	keys := func(ctx context.Context) ([]EvalKey, error) {
		if ctx.Value(staleKeysCtx{}) != nil {
			return nil, nil
		}
		return marked, nil
	}
	store := newMemoryJobStore()
	runner := NewRunner(q, engine, keys, store, RunnerOptions{PollInterval: time.Hour, SyncInterval: time.Hour})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = q.Close(ctx)
	})

	stop := make(chan struct{})
	var stale sync.WaitGroup
	staleCtx := context.WithValue(context.Background(), staleKeysCtx{}, true)
	for range 4 {
		stale.Add(1)
		go func() {
			defer stale.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = runner.refresh(staleCtx)
				}
			}
		}()
	}
	var refused int
	for i := range 300 {
		job := aiJob(fmt.Sprintf("job-%d", i), "openai", "gpt-4o-mini")
		store.add(job)
		if err := runner.Submit(context.Background(), job); err != nil {
			require.ErrorIs(t, err, ErrNoEvalKey)
			refused++
		}
	}
	close(stop)
	stale.Wait()
	assert.Zero(t, refused, "Submit's own refresh found a key, so no job is refused")
}

// A delivery judges its key with the keys its own refresh read. A concurrent refresh that
// read the keys earlier can store its older answer in between, and the delivery must not
// end a started job it owns with errKeyGone, or move a queued one, because of it.
func TestRunner_DeliveryJudgesItsKeyFromItsOwnRefresh(t *testing.T) {
	q, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	engine := NewEngine(EngineOptions{})
	engine.Register(EvaluatorTypeAI, "", func(Definition, Config) (Evaluator, error) {
		return &fakeEvaluator{fn: scoring(BoolScore(true))}, nil
	})
	marked := []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}
	keys := func(ctx context.Context) ([]EvalKey, error) {
		if ctx.Value(staleKeysCtx{}) != nil {
			return nil, nil
		}
		return marked, nil
	}
	store := newMemoryJobStore()
	runner := NewRunner(q, engine, keys, store, RunnerOptions{PollInterval: time.Hour, SyncInterval: time.Hour})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = q.Close(ctx)
	})

	stop := make(chan struct{})
	var stale sync.WaitGroup
	staleCtx := context.WithValue(context.Background(), staleKeysCtx{}, true)
	for range 4 {
		stale.Add(1)
		go func() {
			defer stale.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = runner.refresh(staleCtx)
				}
			}
		}()
	}
	handle := runner.handler("key-a")
	var ended int
	for i := range 300 {
		id := fmt.Sprintf("held-%d", i)
		store.addStranded(aiJob(id, "openai", "gpt-4o-mini"), "key-a", time.Second)
		store.mu.Lock()
		store.started[id], store.claims[id], store.startedAt[id] = "key-a", "owner", time.Now()
		store.mu.Unlock()
		payload, _ := json.Marshal(jobMessage{JobID: id})
		require.NoError(t, handle(context.Background(), &queue.Delivery{Message: queue.Message{ID: id, Payload: payload}, DeliveryID: "owner"}))
		result, done := store.result(id)
		require.True(t, done)
		if result.Score.Status != ScoreStatusOK {
			ended++
		}
	}
	close(stop)
	stale.Wait()
	assert.Zero(t, ended, "the delivery's own refresh found the key serving, so no owned job ends with errKeyGone")
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

// gatedQueue holds every Subscribe to one topic until release is closed, signalling
// arrived as each caller enters, and counts the subscriptions to it that are still open.
type gatedQueue struct {
	queue.Queue
	topic   string
	arrived chan struct{}
	release chan struct{}
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
	<-q.release
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
	q := &gatedQueue{Queue: inner, topic: keyTopic("key-new"), arrived: make(chan struct{}, 2), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(q.release) }) }
	engine := NewEngine(EngineOptions{})
	runner := NewRunner(q, engine, func(context.Context) ([]EvalKey, error) { return nil, nil }, newMemoryJobStore(), RunnerOptions{PollInterval: 5 * time.Millisecond, SyncInterval: time.Hour})
	require.NoError(t, runner.Start(context.Background()))
	t.Cleanup(func() {
		release()
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
	// Both callers must be inside Subscribe at once, or the race is not exercised.
	deadline := time.After(5 * time.Second)
	for i := range 2 {
		select {
		case <-q.arrived:
		case <-deadline:
			release()
			wg.Wait()
			t.Fatalf("only %d of 2 subscribe attempts arrived", i)
		}
	}
	release()
	wg.Wait()
	assert.EqualValues(t, 1, q.open.Load(), "one consumer on the key's topic")
}

// The loser of that race closes the surplus subscription under a deadline. Its caller can
// be a request whose context a shutdown drained (Done closed, Err nil), and the close
// deadline must not be derived from it, or the watcher panics.
func TestRunner_ConcurrentSubscribesSurviveADrainedRequestContext(t *testing.T) {
	inner, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	q := &gatedQueue{Queue: inner, topic: keyTopic("key-new"), arrived: make(chan struct{}, 2), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(q.release) }) }
	runner := NewRunner(q, NewEngine(EngineOptions{}), func(context.Context) ([]EvalKey, error) { return nil, nil }, newMemoryJobStore(), RunnerOptions{PollInterval: 5 * time.Millisecond, SyncInterval: time.Hour})
	require.NoError(t, runner.Start(context.Background()))
	t.Cleanup(func() {
		release()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = inner.Close(ctx)
	})

	key := EvalKey{ID: "key-new", Provider: "openai", Concurrency: 1}
	panics := make(chan any, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { panics <- recover() }()
			_ = runner.ensureSubscribed(newDrainedCtx(), key, false)
		}()
	}
	// Both callers must be inside Subscribe at once, or no surplus subscription is closed.
	deadline := time.After(5 * time.Second)
	for i := range 2 {
		select {
		case <-q.arrived:
		case <-deadline:
			release()
			wg.Wait()
			t.Fatalf("only %d of 2 subscribe attempts arrived", i)
		}
	}
	release()
	wg.Wait()
	close(panics)
	for p := range panics {
		assert.Nil(t, p, "ensureSubscribed panicked on a drained request context")
	}
	assert.EqualValues(t, 1, q.open.Load(), "one consumer on the key's topic")
}

// failingSubscribeQueue fails the next Subscribe to one topic when failNext is set, and
// remembers the subscriptions it handed out for that topic.
type failingSubscribeQueue struct {
	queue.Queue
	topic    string // "" matches every key topic
	failNext atomic.Bool
	mu       sync.Mutex
	subs     []queue.Subscription
	calls    int
	failed   string // the topic whose subscribe failed
}

func (q *failingSubscribeQueue) Subscribe(ctx context.Context, topic, group string, handler queue.Handler, opts ...queue.SubscribeOption) (queue.Subscription, error) {
	matches := topic == q.topic || (q.topic == "" && strings.HasPrefix(topic, runnerKeyTopic))
	if !matches {
		return q.Queue.Subscribe(ctx, topic, group, handler, opts...)
	}
	q.mu.Lock()
	q.calls++
	q.mu.Unlock()
	if q.failNext.Swap(false) {
		q.mu.Lock()
		q.failed = topic
		q.mu.Unlock()
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

// A key whose subscribe fails must not stop the rest of the sync: the other keys are
// still subscribed, and the resume retry, the sweep and the purge still run. The failure
// is still returned.
//
// The runner starts with no key, and both keys are new to the sync. The first subscribe,
// whichever key map order puts first, fails, so the test goes red if the sync stops at the
// first failure: the other key would then have no consumer.
func TestRunner_SyncContinuesPastAFailedSubscribe(t *testing.T) {
	inner, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	q := &failingSubscribeQueue{Queue: inner}
	engine := NewEngine(EngineOptions{})
	RegisterProgrammaticEvaluator(engine, ProgrammaticOptions{})
	engine.Register(EvaluatorTypeAI, "", func(Definition, Config) (Evaluator, error) {
		return &fakeEvaluator{fn: func(context.Context, Input) (Result, error) { return Result{Score: BoolScore(true)}, nil }}, nil
	})
	store := newMemoryJobStore()
	keys := &atomic.Pointer[[]EvalKey]{}
	keys.Store(&[]EvalKey{})
	runner := NewRunner(q, engine, func(context.Context) ([]EvalKey, error) { return *keys.Load(), nil }, store, RunnerOptions{PollInterval: 5 * time.Millisecond, SyncInterval: time.Hour})
	require.NoError(t, runner.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = inner.Close(ctx)
	})

	keyX := EvalKey{ID: "key-x", Provider: "openai", Concurrency: 1, Supports: anyModel}
	keyY := EvalKey{ID: "key-y", Provider: "anthropic", Concurrency: 1, Supports: anyModel}
	keys.Store(&[]EvalKey{keyX, keyY})
	store.addStranded(programmaticJob("never-published"), "", 30*time.Minute)
	q.failNext.Store(true)
	require.Error(t, runner.Sync(context.Background()), "the failed subscribe is reported")

	q.mu.Lock()
	failed := q.failed
	q.mu.Unlock()
	survivor := keyX
	switch failed {
	case keyTopic(keyX.ID):
		survivor = keyY
	case keyTopic(keyY.ID):
	default:
		t.Fatalf("no key subscribe failed, the failing topic was %q", failed)
	}
	// Checked before any publish: a publish subscribes its key on its own.
	runner.mu.Lock()
	_, subscribed := runner.subs[survivor.ID]
	runner.mu.Unlock()
	assert.True(t, subscribed, "the sync subscribed %s past the failed subscribe", survivor.ID)
	h := &runnerHarness{runner: runner, store: store}
	h.submit(t, aiJob("on-survivor", survivor.Provider, "some-model"))
	h.await(t, "never-published", "on-survivor")
}

// closeFailsQueue hands out subscriptions to one topic whose Close stops them, as every
// Close does, and then reports an error, as a lease that could not be released does.
type closeFailsQueue struct {
	queue.Queue
	topic string
}

type closeFailsSubscription struct{ queue.Subscription }

func (s closeFailsSubscription) Close(ctx context.Context) error {
	_ = s.Subscription.Close(ctx)
	return errors.New("the lease could not be released")
}

func (q *closeFailsQueue) Subscribe(ctx context.Context, topic, group string, handler queue.Handler, opts ...queue.SubscribeOption) (queue.Subscription, error) {
	sub, err := q.Queue.Subscribe(ctx, topic, group, handler, opts...)
	if err != nil || topic != q.topic {
		return sub, err
	}
	return closeFailsSubscription{sub}, nil
}

// A retired subscription is stopped by its Close even when Close reports an error. It must
// not stay recorded as the key's consumer: when the key comes back at the same
// concurrency, the sync would find it current and never subscribe again.
func TestRunner_RetireForgetsASubscriptionWhoseCloseFailed(t *testing.T) {
	inner, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	q := &closeFailsQueue{Queue: inner, topic: keyTopic("key-a")}
	engine := NewEngine(EngineOptions{})
	engine.Register(EvaluatorTypeAI, "", func(Definition, Config) (Evaluator, error) {
		return &fakeEvaluator{fn: func(context.Context, Input) (Result, error) { return Result{Score: BoolScore(true)}, nil }}, nil
	})
	store := newMemoryJobStore()
	eligible := []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}
	keys := &atomic.Pointer[[]EvalKey]{}
	keys.Store(&eligible)
	runner := NewRunner(q, engine, func(context.Context) ([]EvalKey, error) { return *keys.Load(), nil }, store, RunnerOptions{PollInterval: 5 * time.Millisecond, SyncInterval: time.Hour})
	require.NoError(t, runner.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = inner.Close(ctx)
	})

	keys.Store(&[]EvalKey{})
	require.NoError(t, runner.Sync(context.Background()))
	runner.mu.Lock()
	_, kept := runner.subs["key-a"]
	runner.mu.Unlock()
	assert.False(t, kept, "the stopped subscription is forgotten whatever its Close returned")

	keys.Store(&eligible)
	require.NoError(t, runner.Sync(context.Background()))
	h := &runnerHarness{runner: runner, store: store}
	h.submit(t, aiJob("after-return", "openai", "gpt-4o-mini"))
	h.await(t, "after-return")
}

// racingCloseQueue hands out subscriptions to one topic whose first Close lets a publish
// win the race with the retirement: before the subscription stops, the key comes back
// and the publish path makes sure the key has a consumer.
type racingCloseQueue struct {
	queue.Queue
	topic  string
	before atomic.Pointer[func()]
	closed atomic.Pointer[queue.Subscription]
}

type racingCloseSubscription struct {
	queue.Subscription
	q *racingCloseQueue
}

func (s *racingCloseSubscription) Close(ctx context.Context) error {
	if before := s.q.before.Swap(nil); before != nil {
		var self queue.Subscription = s
		s.q.closed.Store(&self)
		(*before)()
	}
	return s.Subscription.Close(ctx)
}

func (q *racingCloseQueue) Subscribe(ctx context.Context, topic, group string, handler queue.Handler, opts ...queue.SubscribeOption) (queue.Subscription, error) {
	sub, err := q.Queue.Subscribe(ctx, topic, group, handler, opts...)
	if err != nil || topic != q.topic {
		return sub, err
	}
	return &racingCloseSubscription{Subscription: sub, q: q}, nil
}

// A key that comes back while its drained subscription is being retired keeps a consumer:
// the retirement forgets the subscription before it closes it, so the publish that marked
// the key eligible again installs a fresh one instead of reusing the one about to stop.
func TestRunner_RetireLeavesAConsumerForAKeyThatCameBack(t *testing.T) {
	inner, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	q := &racingCloseQueue{Queue: inner, topic: keyTopic("key-a")}
	engine := NewEngine(EngineOptions{})
	engine.Register(EvaluatorTypeAI, "", func(Definition, Config) (Evaluator, error) {
		return &fakeEvaluator{fn: func(context.Context, Input) (Result, error) { return Result{Score: BoolScore(true)}, nil }}, nil
	})
	store := newMemoryJobStore()
	keyA := EvalKey{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}
	eligible := []EvalKey{keyA}
	keys := &atomic.Pointer[[]EvalKey]{}
	keys.Store(&eligible)
	runner := NewRunner(q, engine, func(context.Context) ([]EvalKey, error) { return *keys.Load(), nil }, store, RunnerOptions{PollInterval: 5 * time.Millisecond, SyncInterval: time.Hour})
	require.NoError(t, runner.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = inner.Close(ctx)
	})

	var raceErr error
	race := func() {
		keys.Store(&eligible)
		if _, err := runner.refresh(context.Background()); err != nil {
			raceErr = err
			return
		}
		raceErr = runner.ensureSubscribed(context.Background(), keyA, false)
	}
	q.before.Store(&race)
	keys.Store(&[]EvalKey{})
	require.NoError(t, runner.Sync(context.Background()))
	require.NoError(t, raceErr)
	closed := q.closed.Load()
	require.NotNil(t, closed, "the retirement closed the drained subscription")

	runner.mu.Lock()
	current, ok := runner.subs["key-a"]
	runner.mu.Unlock()
	require.True(t, ok, "the key that came back has a subscription")
	assert.NotSame(t, *closed, current.sub, "it is not the one the retirement closed")

	h := &runnerHarness{runner: runner, store: store}
	h.submit(t, aiJob("after-race", "openai", "gpt-4o-mini"))
	h.await(t, "after-race")
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
	inner, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	q := &countingPublishQueue{Queue: inner, published: map[string]int{}}
	store := newMemoryJobStore()
	store.addStranded(aiJob("stranded", "openai", "gpt-4o-mini"), "key-gone", time.Hour)
	tracker := newKeyTracker()
	engine := NewEngine(EngineOptions{})
	engine.Register(EvaluatorTypeAI, "", func(Definition, Config) (Evaluator, error) {
		return &fakeEvaluator{fn: func(ctx context.Context, _ Input) (Result, error) {
			key := KeyIDFromContext(ctx)
			tracker.enter(key)
			defer tracker.leave(key)
			time.Sleep(20 * time.Millisecond)
			return Result{Score: BoolScore(true)}, nil
		}}, nil
	})
	keys := []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 2, Supports: anyModel}}
	runner := NewRunner(q, engine, func(context.Context) ([]EvalKey, error) { return keys, nil }, store, RunnerOptions{PollInterval: 5 * time.Millisecond, SyncInterval: time.Hour})
	// Not started: Start's own sweep would move the job before the four below begin, and
	// they would find nothing to race over. publish subscribes the key it picks.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = inner.Close(ctx)
	})

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, runner.Sweep(context.Background()))
		}()
	}
	wg.Wait()
	(&runnerHarness{store: store}).await(t, "stranded")
	time.Sleep(80 * time.Millisecond)

	assert.Equal(t, 1, q.publishes("stranded"), "the four concurrent sweeps requeued the job once")
	_, runs := tracker.snapshot()
	assert.Equal(t, 1, runs["key-a"], "the job ran once")
}

// failingRouteStore fails every RouteJob of one job, as a store that cannot write that
// row would.
type failingRouteStore struct {
	*memoryJobStore
	failID string
}

func (s *failingRouteStore) RouteJob(ctx context.Context, id, fromKey, toKey string, routedAt time.Time) (bool, error) {
	if id == s.failID {
		return false, errors.New("the store cannot move this job")
	}
	return s.memoryJobStore.RouteJob(ctx, id, fromKey, toKey, routedAt)
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

// redeliveringCompleteStore lets the queue redeliver the message that started a job right
// before the sweep's error write lands: CompleteJob first tries to restart the job under
// that delivery, and records whether the restart held.
type redeliveringCompleteStore struct {
	*memoryJobStore
	id, keyID, deliveryID string
	restarted             []bool
}

func (s *redeliveringCompleteStore) CompleteJob(ctx context.Context, id string, result Result) error {
	if id == s.id {
		started, err := s.memoryJobStore.StartJob(ctx, id, s.keyID, JobClaim{DeliveryID: s.deliveryID})
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.restarted = append(s.restarted, started)
		s.mu.Unlock()
	}
	return s.memoryJobStore.CompleteJob(ctx, id, result)
}

// Once the sweep has claimed a lost job, a redelivery of the message that started it
// cannot restart it before the sweep's error write: the recovery claim moved the job to
// the sweep's own claim ID.
func TestRunner_SweepClaimFencesARedeliveryBeforeTheErrorWrite(t *testing.T) {
	inner := newMemoryJobStore()
	store := &redeliveringCompleteStore{memoryJobStore: inner, id: "lost", keyID: "key-gone", deliveryID: "D"}
	h := newRunnerHarnessOn(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0, store)
	h.store = inner
	inner.addStranded(aiJob("lost", "openai", "gpt-4o-mini"), "key-gone", 2*time.Hour)
	inner.mu.Lock()
	inner.started["lost"], inner.claims["lost"], inner.startedAt["lost"] = "key-gone", "D", time.Now().Add(-2*time.Hour)
	inner.mu.Unlock()

	require.NoError(t, h.runner.Sweep(context.Background()))
	store.mu.Lock()
	restarted := slices.Clone(store.restarted)
	store.mu.Unlock()
	require.NotEmpty(t, restarted, "the sweep wrote the error")
	assert.False(t, restarted[0], "the redelivery of D is refused once the sweep owns the job")
	result, done := inner.result("lost")
	require.True(t, done)
	assert.Contains(t, result.Reasoning, "no longer enabled for evaluations")
}

// A job started long ago on a key that is no longer an eval key is still running while
// the delivery that started it holds a renewed lease: the queue keeps leases alive apart
// from the job row, so the job's age is no proof the worker is gone. The sweep leaves it
// alone until that delivery is gone, and then ends it.
func TestRunner_SweepKeepsALostJobWhoseDeliveryHoldsALiveLease(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0)
	ctx := context.Background()
	messageID := "evaljob:lost:held"
	claimed, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	sub, err := h.queue.Subscribe(ctx, keyTopic("key-gone"), runnerGroup, func(ctx context.Context, _ *queue.Delivery) error {
		close(claimed)
		<-release
		return nil
	}, queue.WithLeaseDuration(time.Second))
	require.NoError(t, err)
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		_ = sub.Close(context.Background())
	})
	// The group is registered from the latest message, so publish after subscribing.
	require.NoError(t, h.queue.Publish(ctx, &queue.Message{ID: messageID, Topic: keyTopic("key-gone"), Payload: []byte(`{"job_id":"lost"}`)}))
	select {
	case <-claimed:
	case <-time.After(5 * time.Second):
		t.Fatal("the held delivery was never claimed")
	}

	h.store.addStranded(aiJob("lost", "openai", "gpt-4o-mini"), "key-gone", 2*time.Hour)
	h.store.mu.Lock()
	h.store.started["lost"], h.store.claims["lost"], h.store.startedAt["lost"] = "key-gone", queue.DeliveryID(messageID, runnerGroup), time.Now().Add(-2*time.Hour)
	h.store.mu.Unlock()

	time.Sleep(1500 * time.Millisecond) // past the first lease: only a renewal keeps it
	require.NoError(t, h.runner.Sweep(ctx))
	_, done := h.store.result("lost")
	assert.False(t, done, "the delivery still holds a live lease, so the job is not ended")

	releaseOnce.Do(func() { close(release) })
	require.Eventually(t, func() bool {
		st, err := h.queue.Delivery(ctx, keyTopic("key-gone"), queue.DeliveryID(messageID, runnerGroup))
		return err == nil && st.Status == queue.DeliveryMissing
	}, 2*time.Second, 10*time.Millisecond, "the delivery is acknowledged")
	require.NoError(t, h.runner.Sweep(ctx))
	result, done := h.store.result("lost")
	require.True(t, done, "with the delivery gone, the sweep ends the job")
	assert.Contains(t, result.Reasoning, "no longer enabled for evaluations")
}

// When the error write after the sweep's claim fails, the job stays under the sweep's
// claim with its start kept, and the next sweep's recovery claim matches it again.
func TestRunner_SweepRetriesItsOwnClaimAfterAFailedWrite(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0)
	h.store.addStranded(aiJob("lost", "openai", "gpt-4o-mini"), "key-gone", 2*time.Hour)
	startedAt := time.Now().Add(-2 * time.Hour)
	h.store.mu.Lock()
	h.store.started["lost"], h.store.claims["lost"], h.store.startedAt["lost"] = "key-gone", "D", startedAt
	h.store.failWrite["lost"] = 1
	h.store.mu.Unlock()

	require.Error(t, h.runner.Sweep(context.Background()), "the failed write is reported")
	h.store.mu.Lock()
	claim, kept := h.store.claims["lost"], h.store.startedAt["lost"]
	h.store.mu.Unlock()
	assert.Equal(t, configstore.EvalEndLostClaim, claim, "the sweep holds the claim")
	assert.True(t, kept.Equal(startedAt), "the claim leaves the start it matched")

	require.NoError(t, h.runner.Sweep(context.Background()))
	result, done := h.store.result("lost")
	require.True(t, done, "the next sweep ends the job")
	assert.Contains(t, result.Reasoning, "no longer enabled for evaluations")
}

// A job held under the sweep's claim after a failed write is ended by the next sweep even
// when its key is an eval key again by then. No delivery has the sweep's claim, so on a
// queue that keeps its messages nothing else would ever end it.
func TestRunner_SweepEndsItsOwnClaimOnAKeyEnabledAgain(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0)
	h.store.addStranded(aiJob("lost", "openai", "gpt-4o-mini"), "key-gone", 2*time.Hour)
	startedAt := time.Now().Add(-2 * time.Hour)
	h.store.mu.Lock()
	h.store.started["lost"], h.store.claims["lost"], h.store.startedAt["lost"] = "key-gone", "D", startedAt
	h.store.failWrite["lost"] = 1
	h.store.mu.Unlock()
	require.Error(t, h.runner.Sweep(context.Background()), "the failed write is reported")

	back := []EvalKey{
		{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel},
		{ID: "key-gone", Provider: "openai", Concurrency: 1, Supports: anyModel},
	}
	h.keys.Store(&back)
	require.NoError(t, h.runner.Sweep(context.Background()))
	result, done := h.store.result("lost")
	require.True(t, done, "the next sweep ends the job though its key is eligible again")
	assert.Contains(t, result.Reasoning, "no longer enabled for evaluations")
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

// A stranded job on a removed key is published once at startup, not once by resume and
// again by the sweep. The evaluator is held, so a second message would be in flight
// while the first run is still going instead of landing on a completed job.
func TestRunner_VolatileStartPublishesAStrandedJobOnce(t *testing.T) {
	inner, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	q := &countingPublishQueue{Queue: inner, published: map[string]int{}}
	store := newMemoryJobStore()
	store.addStranded(aiJob("stranded", "openai", "gpt-4o-mini"), "key-gone", time.Hour)
	release := make(chan struct{})
	var releaseOnce sync.Once
	var runs atomic.Int32
	engine := NewEngine(EngineOptions{})
	engine.Register(EvaluatorTypeAI, "", func(Definition, Config) (Evaluator, error) {
		return &fakeEvaluator{fn: func(ctx context.Context, _ Input) (Result, error) {
			runs.Add(1)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return Result{Score: BoolScore(true)}, nil
		}}, nil
	})
	keys := []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 4, Supports: anyModel}}
	runner := NewRunner(q, engine, func(context.Context) ([]EvalKey, error) { return keys, nil }, store, RunnerOptions{PollInterval: 5 * time.Millisecond, SyncInterval: time.Hour, Volatile: true})
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = inner.Close(ctx)
	})
	require.NoError(t, runner.Start(context.Background()))
	require.Eventually(t, func() bool { return runs.Load() >= 1 }, 2*time.Second, 5*time.Millisecond, "the stranded job runs")
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, q.publishes("stranded"), "startup publishes the stranded job once")
	assert.Equal(t, int32(1), runs.Load(), "only one run is in flight")

	releaseOnce.Do(func() { close(release) })
	(&runnerHarness{store: store}).await(t, "stranded")
	store.mu.Lock()
	assert.Equal(t, 1, store.completed["stranded"])
	store.mu.Unlock()
}

// A job waiting a long time on a busy key still has its route's delivery queued. The
// sweep must not add a copy of it on every lost-message period, but it must put a new
// delivery on the queue once that one is gone: a stable message ID alone cannot, since
// the queue keeps an acknowledged message's row and ignores a second publish of it.
func TestRunner_RepublishAddsNoCopyWhileTheRouteIsQueued(t *testing.T) {
	ctx := context.Background()
	inner, err := queue.NewQueue(ctx, &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	q := &countingPublishQueue{Queue: inner, published: map[string]int{}}
	t.Cleanup(func() { _ = inner.Close(context.Background()) })
	topic := keyTopic("key-a")
	// Register the runner's group on the key without a consumer, so deliveries stay put.
	idle, err := inner.Subscribe(ctx, topic, runnerGroup, func(context.Context, *queue.Delivery) error { return nil })
	require.NoError(t, err)
	require.NoError(t, idle.Close(ctx))

	store := newMemoryJobStore()
	store.addStranded(aiJob("waiting", "openai", "gpt-4o-mini"), "key-a", 2*time.Hour)
	store.mu.Lock()
	routedAt := store.routedAt["waiting"].Truncate(time.Microsecond)
	store.routedAt["waiting"] = routedAt
	store.mu.Unlock()
	routed := routeMessageID("waiting", routedAt)
	require.NoError(t, q.Publish(ctx, &queue.Message{ID: routed, Topic: topic, Payload: []byte(`{"job_id":"waiting"}`)}))

	engine := NewEngine(EngineOptions{})
	keys := []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}
	runner := NewRunner(q, engine, func(context.Context) ([]EvalKey, error) { return keys, nil }, store, RunnerOptions{SyncInterval: time.Hour})

	require.NoError(t, runner.Sweep(ctx))
	assert.Equal(t, 1, q.publishes("waiting"), "the route's delivery is still queued, so no copy is added")

	// The route's delivery is acknowledged without the job finishing: the next sweep must
	// queue a new delivery for it.
	acked := make(chan struct{})
	var ackOnce sync.Once
	consumer, err := inner.Subscribe(ctx, topic, runnerGroup, func(context.Context, *queue.Delivery) error {
		ackOnce.Do(func() { close(acked) })
		return nil
	})
	require.NoError(t, err)
	select {
	case <-acked:
	case <-time.After(5 * time.Second):
		t.Fatal("the route's delivery was never consumed")
	}
	require.NoError(t, consumer.Close(ctx))
	require.Eventually(t, func() bool {
		st, err := inner.Delivery(ctx, topic, queue.DeliveryID(routed, runnerGroup))
		return err == nil && st.Status == queue.DeliveryMissing
	}, 2*time.Second, 10*time.Millisecond)

	require.NoError(t, runner.Sweep(ctx))
	assert.Equal(t, 2, q.publishes("waiting"), "with no live delivery, the sweep queues a new one")
	store.mu.Lock()
	moved := store.routedAt["waiting"]
	store.mu.Unlock()
	st, err := inner.Delivery(ctx, topic, queue.DeliveryID(routeMessageID("waiting", moved), runnerGroup))
	require.NoError(t, err)
	assert.Equal(t, queue.DeliveryPending, st.Status, "the new delivery is queued under the job's new route time")
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

// countingPublishQueue counts publishes per job ID and fails them while failing is set.
type countingPublishQueue struct {
	queue.Queue
	failing   atomic.Bool
	mu        sync.Mutex
	published map[string]int
}

func (q *countingPublishQueue) Publish(ctx context.Context, messages ...*queue.Message) error {
	if q.failing.Load() {
		return errors.New("the queue is unavailable")
	}
	q.mu.Lock()
	for _, message := range messages {
		var body jobMessage
		if json.Unmarshal(message.Payload, &body) == nil {
			q.published[body.JobID]++
		}
	}
	q.mu.Unlock()
	return q.Queue.Publish(ctx, messages...)
}

func (q *countingPublishQueue) publishes(jobID string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.published[jobID]
}

// A failed resume keeps only the job's ID, and the retry reads the job again: a job that
// completed in the meantime is not sent from a stale in-memory copy, and the backlog of a
// queue outage is not held in memory.
func TestRunner_RetriedResumeReloadsTheJob(t *testing.T) {
	inner, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	q := &countingPublishQueue{Queue: inner, published: map[string]int{}}
	q.failing.Store(true)
	store := newMemoryJobStore()
	ids := []string{"resume-0", "resume-1", "resume-2"}
	for _, id := range ids {
		store.add(programmaticJob(id))
	}
	engine := NewEngine(EngineOptions{})
	RegisterProgrammaticEvaluator(engine, ProgrammaticOptions{})
	runner := NewRunner(q, engine, func(context.Context) ([]EvalKey, error) { return nil, nil }, store, RunnerOptions{PollInterval: 5 * time.Millisecond, SyncInterval: time.Hour, Volatile: true})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = inner.Close(ctx)
	})
	require.NoError(t, runner.Start(context.Background()))
	runner.mu.Lock()
	pending := slices.Clone(runner.pending)
	runner.mu.Unlock()
	assert.ElementsMatch(t, ids, pending, "each failed resume is kept by its ID")

	require.NoError(t, store.CompleteJob(context.Background(), "resume-1", Result{Score: BoolScore(true)}))
	q.failing.Store(false)
	require.NoError(t, runner.Sync(context.Background()))
	h := &runnerHarness{store: store}
	h.await(t, "resume-0", "resume-2")
	assert.Zero(t, q.publishes("resume-1"), "the job that completed meanwhile is not sent again")
	runner.mu.Lock()
	assert.Empty(t, runner.pending)
	runner.mu.Unlock()
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

// The dead letter of a message from a route the job has left must not end it: the job
// waits on another key's queue now, and that queue's message runs it. Only a dead letter
// from the topic of the key the job is on ends a queued job.
func TestRunner_DeadLetterFromAnOldRouteLeavesAQueuedJob(t *testing.T) {
	h := newRunnerHarness(t, nil, 0)
	h.store.addStranded(aiJob("moved", "openai", "gpt-4o-mini"), "key-a", time.Second)
	moved, err := h.store.RouteJob(context.Background(), "moved", "key-a", "key-b", time.Now())
	require.NoError(t, err)
	require.True(t, moved)
	handle := h.runner.deadHandler()
	payload, _ := json.Marshal(jobMessage{JobID: "moved"})
	dead := func(topic string) *queue.Delivery {
		return &queue.Delivery{Message: queue.Message{ID: "dlq", Payload: payload, Headers: map[string]string{queue.HeaderDeadLetterSourceTopic: topic, queue.HeaderDeadLetterDeliveryID: "d"}}, DeliveryID: "dlq"}
	}

	require.NoError(t, handle(context.Background(), dead(keyTopic("key-a"))))
	_, done := h.store.result("moved")
	assert.False(t, done, "the dead letter of the route the job left does not end it")
	assert.Equal(t, "key-b", h.store.routedTo("moved"), "it stays queued on its current key")

	require.NoError(t, handle(context.Background(), dead(keyTopic("key-b"))))
	result, done := h.store.result("moved")
	require.True(t, done, "the dead letter of the current route ends it")
	assert.Equal(t, ScoreStatusError, result.Score.Status)
}

// movingLoadStore is a memoryJobStore whose next LoadJob returns the job as it was, and
// then, before returning, lets a sweep move it from one key to another and a worker
// start it there: the move lands between the read and whatever acts on it.
type movingLoadStore struct {
	*memoryJobStore
	armed    atomic.Bool
	from, to string
}

func (s *movingLoadStore) LoadJob(ctx context.Context, id string) (*Job, error) {
	job, err := s.memoryJobStore.LoadJob(ctx, id)
	if err != nil || !s.armed.CompareAndSwap(true, false) {
		return job, err
	}
	if moved, routeErr := s.memoryJobStore.RouteJob(ctx, id, s.from, s.to, time.Now()); routeErr != nil || !moved {
		return nil, fmt.Errorf("the test move failed: moved=%v err=%v", moved, routeErr)
	}
	if started, startErr := s.memoryJobStore.StartJob(ctx, id, s.to, JobClaim{DeliveryID: "D2"}); startErr != nil || !started {
		return nil, fmt.Errorf("the test start failed: started=%v err=%v", started, startErr)
	}
	return job, nil
}

// A dead letter read on key-a must not end a job that a sweep moved to key-b and a worker
// started there after the read: the completion is conditioned on the job still being
// queued on key-a, and the worker on key-b finishes it.
func TestRunner_DeadLetterLeavesAJobThatMovedAfterTheRead(t *testing.T) {
	store := &movingLoadStore{memoryJobStore: newMemoryJobStore(), from: "key-a", to: "key-b"}
	h := newRunnerHarnessOn(t, nil, 0, store)
	store.addStranded(aiJob("moving", "openai", "gpt-4o-mini"), "key-a", time.Second)
	store.armed.Store(true)
	payload, _ := json.Marshal(jobMessage{JobID: "moving"})
	dead := &queue.Delivery{Message: queue.Message{ID: "dlq", Payload: payload, Headers: map[string]string{queue.HeaderDeadLetterSourceTopic: keyTopic("key-a"), queue.HeaderDeadLetterDeliveryID: "d"}}, DeliveryID: "dlq"}

	require.NoError(t, h.runner.deadHandler()(context.Background(), dead))
	_, done := store.result("moving")
	assert.False(t, done, "the dead letter of the old route does not end the job running on key-b")
	assert.Equal(t, "key-b", store.keyOf("moving"), "the job stays running on key-b")
}

// A reroute that finds no eval key must not end a job that another instance moved to a
// newly enabled key and started between this instance's key refresh and its write.
func TestRunner_NoKeyRerouteLeavesAJobThatMovedMeanwhile(t *testing.T) {
	q, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	engine := NewEngine(EngineOptions{})
	RegisterAIEvaluator(engine, func(context.Context, JudgeRequest) (JudgeResponse, error) { return JudgeResponse{}, nil })
	store := newMemoryJobStore()
	store.addStranded(aiJob("raced", "openai", "gpt-4o-mini"), "key-a", time.Hour)
	var calls atomic.Int32
	keys := func(ctx context.Context) ([]EvalKey, error) {
		// The sweep's own refresh is the first call; the second is reroute's, and the other
		// instance moves and starts the job while it runs.
		if calls.Add(1) == 2 {
			moved, _ := store.RouteJob(ctx, "raced", "key-a", "key-b", time.Now())
			started, _ := store.StartJob(ctx, "raced", "key-b", JobClaim{DeliveryID: "D2"})
			if !moved || !started {
				return nil, errors.New("the test move failed")
			}
		}
		return nil, nil
	}
	runner := NewRunner(q, engine, keys, store, RunnerOptions{PollInterval: 5 * time.Millisecond})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = q.Close(ctx)
	})

	require.NoError(t, runner.Sweep(context.Background()))
	require.GreaterOrEqual(t, calls.Load(), int32(2), "reroute refreshed the keys")
	_, done := store.result("raced")
	assert.False(t, done, "the job that started on key-b is not ended with ErrNoEvalKey")
	assert.Equal(t, "key-b", store.keyOf("raced"), "it is still running on key-b")
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

// failingSweepStore is a memoryJobStore whose first failures StaleJobs calls fail.
type failingSweepStore struct {
	*memoryJobStore
	failures atomic.Int32
	calls    atomic.Int32
}

func (s *failingSweepStore) StaleJobs(ctx context.Context, queuedBefore, routedBefore time.Time, skipKeys []string, after JobCursor, limit int) ([]*Job, JobCursor, error) {
	s.calls.Add(1)
	if s.failures.Add(-1) >= 0 {
		return nil, JobCursor{}, errors.New("the store is unavailable")
	}
	return s.memoryJobStore.StaleJobs(ctx, queuedBefore, routedBefore, skipKeys, after, limit)
}

// A sweep that fails on start must not keep the runner from starting: the queue would
// be closed and every evaluation disabled until a restart. The periodic sync sweeps again.
func TestRunner_StartSurvivesAFailedSweep(t *testing.T) {
	q, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	engine := NewEngine(EngineOptions{})
	RegisterProgrammaticEvaluator(engine, ProgrammaticOptions{})
	store := &failingSweepStore{memoryJobStore: newMemoryJobStore()}
	store.failures.Store(1)
	runner := NewRunner(q, engine, func(context.Context) ([]EvalKey, error) { return nil, nil }, store, RunnerOptions{PollInterval: 5 * time.Millisecond, SyncInterval: 10 * time.Millisecond})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = q.Close(ctx)
	})
	require.NoError(t, runner.Start(context.Background()), "a failed sweep on start is logged, not returned")

	job := programmaticJob("after-start")
	store.add(job)
	require.NoError(t, runner.Submit(context.Background(), job))
	require.Eventually(t, func() bool {
		_, done := store.result("after-start")
		return done
	}, 10*time.Second, 5*time.Millisecond, "a job submitted after the failed sweep runs")
	require.Eventually(t, func() bool { return store.calls.Load() >= 2 }, 5*time.Second, 5*time.Millisecond, "the periodic sync sweeps again")
}

// failingPageResumer fails the listing of unfinished jobs once, on the first page after
// the start, as a store that drops its connection partway through the walk does.
type failingPageResumer struct {
	*memoryJobStore
	failed   atomic.Bool
	reads    atomic.Int32 // listings of pages after the first
	cursorMu sync.Mutex
	afters   []JobCursor // the cursor of every listing, the failed one included
	failedAt JobCursor   // the cursor of the listing that failed
}

func (s *failingPageResumer) UnfinishedJobs(ctx context.Context, after JobCursor, limit int) ([]*Job, JobCursor, error) {
	s.cursorMu.Lock()
	s.afters = append(s.afters, after)
	s.cursorMu.Unlock()
	if after != (JobCursor{}) {
		s.reads.Add(1)
		if s.failed.CompareAndSwap(false, true) {
			s.cursorMu.Lock()
			s.failedAt = after
			s.cursorMu.Unlock()
			return nil, JobCursor{}, errors.New("the store is unavailable")
		}
	}
	return s.memoryJobStore.UnfinishedJobs(ctx, after, limit)
}

// listings returns how many listings ran and the cursor of the failed one.
func (s *failingPageResumer) listings() (int, JobCursor) {
	s.cursorMu.Lock()
	defer s.cursorMu.Unlock()
	return len(s.afters), s.failedAt
}

// afterAt returns the cursor of the listing at index i.
func (s *failingPageResumer) afterAt(i int) JobCursor {
	s.cursorMu.Lock()
	defer s.cursorMu.Unlock()
	return s.afters[i]
}

// A resume whose listing fails partway must not keep the runner from starting: the
// queue would be closed and every evaluation disabled until a restart. The walk goes on
// from the page that failed on the next sync, without reading the earlier pages again.
func TestRunner_StartSurvivesAFailedResumeListing(t *testing.T) {
	store := newMemoryJobStore()
	base := time.Now().Add(-time.Second)
	var firstPage, secondPage []string
	for i := range runnerSweepBatch + 5 {
		id := fmt.Sprintf("local-%04d", i)
		store.add(programmaticJob(id))
		store.queuedAt[id] = base.Add(time.Duration(i) * time.Microsecond)
		if i < runnerSweepBatch {
			firstPage = append(firstPage, id)
		} else {
			secondPage = append(secondPage, id)
		}
	}
	resumer := &failingPageResumer{memoryJobStore: store}
	q, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	engine := NewEngine(EngineOptions{})
	RegisterProgrammaticEvaluator(engine, ProgrammaticOptions{})
	runner := NewRunner(q, engine, func(context.Context) ([]EvalKey, error) { return nil, nil }, resumer,
		RunnerOptions{PollInterval: 5 * time.Millisecond, Volatile: true, SyncInterval: time.Hour})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = q.Close(ctx)
	})
	require.NoError(t, runner.Start(context.Background()), "a failed resume listing on start is logged, not returned")
	h := &runnerHarness{store: store}
	h.await(t, firstPage...)

	job := programmaticJob("after-start")
	store.add(job)
	require.NoError(t, runner.Submit(context.Background(), job))
	h.await(t, "after-start")
	for _, id := range secondPage {
		_, done := store.result(id)
		require.False(t, done, "%s is on the page that failed and waits for the next sync", id)
	}

	pagesBefore := func() int { store.mu.Lock(); defer store.mu.Unlock(); return len(store.pages) }()
	listingsBefore, failedAt := resumer.listings()
	require.NotEqual(t, JobCursor{}, failedAt, "the failed listing was a later page")
	require.NoError(t, runner.Sync(context.Background()))
	h.await(t, secondPage...)
	// The store skips completed jobs, so a walk restarted from the beginning would also
	// finish the second page. The cursor shows where the sync started.
	listingsAfter, _ := resumer.listings()
	require.Greater(t, listingsAfter, listingsBefore, "the sync lists unfinished jobs")
	assert.Equal(t, failedAt, resumer.afterAt(listingsBefore), "the sync goes on from the failed listing's cursor, not from the start")
	store.mu.Lock()
	pagesAfter := len(store.pages)
	store.mu.Unlock()
	assert.Equal(t, 1, pagesAfter-pagesBefore, "the sync reads only the page that failed, not the pages already resumed")
	require.NoError(t, runner.Sync(context.Background()))
	store.mu.Lock()
	assert.Equal(t, pagesAfter, len(store.pages), "a finished walk is not read again")
	store.mu.Unlock()
}

// A key whose subscribe fails on start must not keep the runner from starting: the local
// and dead topics are consumed, and the next sync subscribes the key.
func TestRunner_StartSurvivesAFailedKeySubscribe(t *testing.T) {
	inner, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	q := &failingSubscribeQueue{Queue: inner, topic: keyTopic("key-bad")}
	q.failNext.Store(true)
	engine := NewEngine(EngineOptions{})
	RegisterProgrammaticEvaluator(engine, ProgrammaticOptions{})
	store := newMemoryJobStore()
	keys := func(context.Context) ([]EvalKey, error) {
		return []EvalKey{{ID: "key-bad", Provider: "openai", Concurrency: 1, Supports: anyModel}}, nil
	}
	runner := NewRunner(q, engine, keys, store, RunnerOptions{PollInterval: 5 * time.Millisecond, SyncInterval: time.Hour})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = inner.Close(ctx)
	})
	require.NoError(t, runner.Start(context.Background()), "a failed key subscribe on start is logged, not returned")
	q.mu.Lock()
	failed := q.failed
	q.mu.Unlock()
	require.Equal(t, keyTopic("key-bad"), failed, "the key's subscribe did fail")

	job := programmaticJob("after-start")
	store.add(job)
	require.NoError(t, runner.Submit(context.Background(), job))
	require.Eventually(t, func() bool {
		_, done := store.result("after-start")
		return done
	}, 10*time.Second, 5*time.Millisecond, "a job that needs no model runs")

	require.NoError(t, runner.Sync(context.Background()))
	runner.mu.Lock()
	_, subscribed := runner.subs["key-bad"]
	runner.mu.Unlock()
	assert.True(t, subscribed, "the next sync subscribes the key")
}

// The sweep reads one page of stale jobs at a time. Rows it cannot act on, such as local
// jobs that are old but not yet lost, must not fill that page on every sweep: the sweep
// resumes after the last row it read, so a job stranded behind them is reached.
func TestRunner_SweepReachesAStrandedJobBehindAFullPage(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 2, Supports: anyModel}}, 0)
	h.runner.mu.Lock()
	h.runner.sweepBatch = 3
	h.runner.mu.Unlock()
	for i := range 3 {
		h.store.addStranded(programmaticJob(fmt.Sprintf("local-%d", i)), "", 3*time.Minute)
	}
	h.store.addStranded(aiJob("stranded", "openai", "gpt-4o-mini"), "key-gone", 150*time.Second)

	require.NoError(t, h.runner.Sweep(context.Background()))
	require.NoError(t, h.runner.Sweep(context.Background()))
	h.await(t, "stranded")
	assert.Equal(t, "key-a", h.store.keyOf("stranded"), "it was moved to the key that is eligible now")
}

// A page whose recovery keeps failing must not pin every sweep: the next sweep reads the
// rows after it, and the failed ones are retried on the next lap.
func TestRunner_SweepMovesOnPastAPageThatFails(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 2, Supports: anyModel}}, 0)
	h.runner.mu.Lock()
	h.runner.sweepBatch = 2
	h.runner.mu.Unlock()
	for i := range 2 {
		id := fmt.Sprintf("failing-%d", i)
		h.store.addStranded(aiJob(id, "openai", "gpt-4o-mini"), "key-gone", time.Hour)
		h.store.mu.Lock()
		h.store.failRoute[id] = true
		h.store.mu.Unlock()
	}
	h.store.addStranded(aiJob("stranded", "openai", "gpt-4o-mini"), "key-gone", 30*time.Minute)

	require.Error(t, h.runner.Sweep(context.Background()), "the failing page is reported")
	require.NoError(t, h.runner.Sweep(context.Background()))
	h.await(t, "stranded")

	h.store.mu.Lock()
	clear(h.store.failRoute)
	h.store.mu.Unlock()
	require.NoError(t, h.runner.Sweep(context.Background()), "the next lap starts again from the oldest row")
	h.await(t, "failing-0", "failing-1")
}

// A key that leaves the eligible set keeps its subscription only while its topic still
// holds work. Once drained, Sync closes it, so replacing keys does not pile up idle
// consumers. While a job is still in flight on it, the subscription stays.
func TestRunner_SyncRetiresADrainedSubscription(t *testing.T) {
	h := newRunnerHarness(t, []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}, 0)
	subscribed := func(keyID string) bool {
		h.runner.mu.Lock()
		defer h.runner.mu.Unlock()
		_, ok := h.runner.subs[keyID]
		return ok
	}

	release := h.hold()
	h.submit(t, aiJob("in-flight", "openai", "gpt-4o-mini"))
	h.awaitRuns(t, "key-a", 1)
	none := []EvalKey{}
	h.keys.Store(&none)
	require.NoError(t, h.runner.Sync(context.Background()))
	assert.True(t, subscribed("key-a"), "the subscription stays while a job is in flight on it")

	release()
	h.await(t, "in-flight")
	require.Eventually(t, func() bool {
		st, err := h.queue.Stats(context.Background(), keyTopic("key-a"), runnerGroup)
		return err == nil && st.Pending+st.Leased == 0
	}, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, h.runner.Sync(context.Background()))
	assert.False(t, subscribed("key-a"), "a drained subscription of a key that is no longer eligible is closed")

	// The key coming back subscribes again.
	back := []EvalKey{{ID: "key-a", Provider: "openai", Concurrency: 1, Supports: anyModel}}
	h.keys.Store(&back)
	require.NoError(t, h.runner.Sync(context.Background()))
	assert.True(t, subscribed("key-a"))
	h.submit(t, aiJob("after-return", "openai", "gpt-4o-mini"))
	h.await(t, "after-return")
}
