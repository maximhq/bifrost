package queue

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastOpts keeps engine tests quick: tight polling and millisecond backoff.
func fastOpts(extra ...SubscribeOption) []SubscribeOption {
	return append([]SubscribeOption{
		WithPollInterval(20 * time.Millisecond),
		WithLeaseDuration(400 * time.Millisecond),
		WithBackoff(time.Millisecond, 5*time.Millisecond),
	}, extra...)
}

func newTestQueue(t *testing.T) (*storeQueue, *memoryStore) {
	t.Helper()
	store := newMemoryStore()
	q, err := NewStoreQueue(store, EngineConfig{}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close(context.Background()) })
	return q.(*storeQueue), store
}

func publish(t *testing.T, q Queue, msgs ...*Message) {
	t.Helper()
	require.NoError(t, q.Publish(context.Background(), msgs...))
}

func subscribe(t *testing.T, q Queue, topic, group string, h Handler, opts ...SubscribeOption) Subscription {
	t.Helper()
	sub, err := q.Subscribe(context.Background(), topic, group, h, fastOpts(opts...)...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Close(context.Background()) })
	return sub
}

func waitStats(t *testing.T, q Queue, topic, group string, want Stats) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		st, err := q.Stats(context.Background(), topic, group)
		assert.NoError(c, err)
		assert.Equal(c, want, st)
	}, 5*time.Second, 10*time.Millisecond)
}

// engineBackend returns n stores that share one backend, one per simulated
// instance. The engine contract runs the same cases over every backend.
type engineBackend func(t *testing.T, n int) []Store

// engineQueues starts one engine per store, closed when the test ends.
func engineQueues(t *testing.T, stores []Store) []*storeQueue {
	t.Helper()
	out := make([]*storeQueue, len(stores))
	for i, s := range stores {
		q, err := NewStoreQueue(s, EngineConfig{}, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = q.Close(context.Background()) })
		out[i] = q.(*storeQueue)
	}
	return out
}

// runEngineContract checks engine behaviour end to end over a real backend:
// retries, dead-lettering, panics, lease heartbeats and lease loss,
// shutdown, and publish wakeups.
func runEngineContract(t *testing.T, backend engineBackend) {
	cases := []struct {
		name string
		fn   func(t *testing.T, backend engineBackend)
	}{
		{"PublishSubscribe", engineContractPublishSubscribe},
		{"RetriesThenDeadLetters", engineContractRetriesThenDeadLetters},
		{"PermanentSkipsRetries", engineContractPermanentSkipsRetries},
		{"RecoversPanics", engineContractRecoversPanics},
		{"HeartbeatKeepsLongHandlersExclusive", engineContractHeartbeatKeepsLongHandlersExclusive},
		{"LeaseLossCancelsHandler", engineContractLeaseLossCancelsHandler},
		{"AttemptsSpentByLostLeasesDeadLetterWithoutRunning", engineContractAttemptsSpentByLostLeases},
		{"CloseDrainsInFlight", engineContractCloseDrainsInFlight},
		{"CloseDeadlineKeepsRunningLease", engineContractCloseDeadlineKeepsRunningLease},
		{"CloseReleasesUnstartedWithoutSpendingAttempts", engineContractCloseReleasesUnstarted},
		{"WakeDeliversBeforePoll", engineContractWakeDeliversBeforePoll},
		{"TransientPublishErrorIsRetried", engineContractTransientPublishErrorIsRetried},
		{"ClaimErrorsDoNotStopConsuming", engineContractClaimErrorsDoNotStopConsuming},
		{"AckErrorRedeliversAfterLease", engineContractAckErrorRedeliversAfterLease},
		{"DeadLetterPublishFailureRetries", engineContractDeadLetterPublishFailureRetries},
		{"HeartbeatErrorKeepsLease", engineContractHeartbeatErrorKeepsLease},
		{"RepeatedHeartbeatErrorsLoseLease", engineContractRepeatedHeartbeatErrorsLoseLease},
		{"LapsedLeaseCancelsWithoutFreeSlots", engineContractLapsedLeaseCancelsWithoutFreeSlots},
		{"StaleHandlerDoesNotDeadLetter", engineContractStaleHandlerDoesNotDeadLetter},
		{"ReleaseErrorIsReported", engineContractReleaseErrorIsReported},
		{"ConcurrencyLimit", engineContractConcurrencyLimit},
		{"BatchSizeAndImmediateRepoll", engineContractBatchSizeAndImmediateRepoll},
		{"SubscriptionsInOneGroupCompete", engineContractSubscriptionsInOneGroupCompete},
		{"SubscribeContextOnlyRegisters", engineContractSubscribeContextOnlyRegisters},
		{"MultiTopicPublish", engineContractMultiTopicPublish},
		{"CloseLeavesNoGoroutines", engineContractCloseLeavesNoGoroutines},
		{"AcksAreBatched", engineContractAcksAreBatched},
		{"MaxInFlightAcrossInstances", engineContractMaxInFlightAcrossInstances},
		{"ScheduledMessageRunsOnTime", engineContractScheduledOnTime},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.fn(t, backend) })
	}
}

func TestMemoryEngineContract(t *testing.T) {
	runEngineContract(t, func(t *testing.T, n int) []Store {
		store := newMemoryStore()
		t.Cleanup(func() { _ = store.Close(context.Background()) })
		out := make([]Store, n)
		for i := range out {
			out[i] = sharedStore{store}
		}
		return out
	})
}

func engineContractPublishSubscribe(t *testing.T, backend engineBackend) {
	q := engineQueues(t, backend(t, 1))[0]
	topic := uniqueTopic(t)
	var mu sync.Mutex
	got := map[string][]string{}
	handler := func(group string) Handler {
		return func(ctx context.Context, d *Delivery) error {
			mu.Lock()
			defer mu.Unlock()
			got[group] = append(got[group], string(d.Payload))
			assert.Equal(t, DeliveryID(d.ID, group), d.DeliveryID)
			assert.Equal(t, 1, d.Attempt)
			return nil
		}
	}
	subscribe(t, q, topic, "billing", handler("billing"))
	subscribe(t, q, topic, "shipping", handler("shipping"))

	m := &Message{Topic: topic, Payload: []byte("o1"), Headers: map[string]string{"h": "v"}}
	publish(t, q, m, &Message{Topic: topic, Payload: []byte("o2")})
	assert.NotEmpty(t, m.ID, "Publish fills the ID")
	assert.False(t, m.PublishedAt.IsZero(), "Publish fills the publish time")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got["billing"]) == 2 && len(got["shipping"]) == 2
	}, 10*time.Second, 10*time.Millisecond)
	waitStats(t, q, topic, "billing", Stats{})
	waitStats(t, q, topic, "shipping", Stats{})
}

func engineContractRetriesThenDeadLetters(t *testing.T, backend engineBackend) {
	q := engineQueues(t, backend(t, 1))[0]
	topic := uniqueTopic(t)
	dlqTopic := topic + ".dlq"
	var attempts []int
	var mu sync.Mutex
	subscribe(t, q, topic, "workers", func(ctx context.Context, d *Delivery) error {
		mu.Lock()
		attempts = append(attempts, d.Attempt)
		mu.Unlock()
		return errors.New("still failing")
	}, WithMaxAttempts(3), WithDeadLetterTopic(dlqTopic))

	dlq := make(chan *Delivery, 1)
	subscribe(t, q, dlqTopic, "inspect", func(ctx context.Context, d *Delivery) error {
		dlq <- d
		return nil
	})

	m := &Message{Topic: topic, Key: "k", Payload: []byte("p"), Headers: map[string]string{"orig": "1"}}
	publish(t, q, m)

	select {
	case d := <-dlq:
		assert.Equal(t, []byte("p"), d.Payload)
		assert.Equal(t, "k", d.Key)
		assert.Equal(t, "1", d.Headers["orig"])
		assert.Equal(t, topic, d.Headers[HeaderDeadLetterSourceTopic])
		assert.Equal(t, "workers", d.Headers[HeaderDeadLetterGroup])
		assert.Equal(t, DeliveryID(m.ID, "workers"), d.Headers[HeaderDeadLetterDeliveryID])
		assert.Equal(t, m.ID, d.Headers[HeaderDeadLetterMessageID])
		assert.Equal(t, "3", d.Headers[HeaderDeadLetterAttempts])
		assert.Equal(t, "still failing", d.Headers[HeaderDeadLetterError])
		assert.Equal(t, deadLetterMessageID(DeliveryID(m.ID, "workers")), d.ID)
	case <-time.After(10 * time.Second):
		t.Fatal("no dead-letter copy")
	}
	waitStats(t, q, topic, "workers", Stats{Dead: 1})
	mu.Lock()
	assert.Equal(t, []int{1, 2, 3}, attempts)
	mu.Unlock()
}

func engineContractPermanentSkipsRetries(t *testing.T, backend engineBackend) {
	q := engineQueues(t, backend(t, 1))[0]
	topic := uniqueTopic(t)
	var calls atomic.Int32
	subscribe(t, q, topic, "w", func(ctx context.Context, d *Delivery) error {
		calls.Add(1)
		return Permanent(errors.New("malformed"))
	}, WithMaxAttempts(5))
	publish(t, q, &Message{Topic: topic})
	waitStats(t, q, topic, "w", Stats{Dead: 1})
	assert.Equal(t, int32(1), calls.Load())
}

func engineContractRecoversPanics(t *testing.T, backend engineBackend) {
	q := engineQueues(t, backend(t, 1))[0]
	topic := uniqueTopic(t)
	var calls atomic.Int32
	done := make(chan int, 1)
	subscribe(t, q, topic, "w", func(ctx context.Context, d *Delivery) error {
		if calls.Add(1) == 1 {
			panic("kaboom")
		}
		done <- d.Attempt
		return nil
	})
	publish(t, q, &Message{Topic: topic})
	select {
	case attempt := <-done:
		assert.Equal(t, 2, attempt, "a panic counts as a failed attempt")
	case <-time.After(10 * time.Second):
		t.Fatal("handler never succeeded after panic")
	}
	waitStats(t, q, topic, "w", Stats{})
}

// engineContractHeartbeatKeepsLongHandlersExclusive runs a handler for
// several lease durations while a second instance competes for the same
// group: the heartbeat must keep the lease, so the delivery runs exactly once.
func engineContractHeartbeatKeepsLongHandlersExclusive(t *testing.T, backend engineBackend) {
	pods := engineQueues(t, backend(t, 2))
	topic := uniqueTopic(t)
	var calls atomic.Int32
	h := func(ctx context.Context, d *Delivery) error {
		calls.Add(1)
		select {
		case <-time.After(1500 * time.Millisecond): // ~4 leases
			return nil
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	for _, p := range pods {
		subscribe(t, p, topic, "g", h)
	}
	publish(t, pods[0], &Message{Topic: topic})
	waitStats(t, pods[0], topic, "g", Stats{})
	assert.Equal(t, int32(1), calls.Load())
}

// slowExtendStore stalls every heartbeat past the lease, like a database
// that stops answering, so the real backend reports the lease as no longer
// held. It ignores the heartbeat's own deadline on purpose: honouring it
// would let renewals through in time.
type slowExtendStore struct {
	Store
	delay time.Duration
}

func (s slowExtendStore) Extend(ctx context.Context, cs []*Claimed, lease time.Duration) ([]string, error) {
	time.Sleep(s.delay)
	return s.Store.Extend(context.WithoutCancel(ctx), cs, lease)
}

func engineContractLeaseLossCancelsHandler(t *testing.T, backend engineBackend) {
	const lease = 400 * time.Millisecond
	q := engineQueues(t, []Store{slowExtendStore{Store: backend(t, 1)[0], delay: 2 * lease}})[0]
	topic := uniqueTopic(t)
	started := make(chan struct{})
	cause := make(chan error, 1)
	subscribe(t, q, topic, "w", func(ctx context.Context, d *Delivery) error {
		if d.Attempt > 1 {
			return nil
		}
		close(started)
		<-ctx.Done()
		cause <- context.Cause(ctx)
		return ctx.Err()
	}, WithLeaseDuration(lease))
	publish(t, q, &Message{Topic: topic})
	<-started
	select {
	case err := <-cause:
		assert.ErrorIs(t, err, errLeaseLost)
	case <-time.After(10 * time.Second):
		t.Fatal("handler was not cancelled after losing its lease")
	}
	// The redelivery completes, and nothing is left behind.
	waitStats(t, q, topic, "w", Stats{})
}

func engineContractAttemptsSpentByLostLeases(t *testing.T, backend engineBackend) {
	stores := backend(t, 1)
	q := engineQueues(t, stores)[0]
	ctx := context.Background()
	topic := uniqueTopic(t)
	require.NoError(t, stores[0].EnsureGroup(ctx, topic, "w", StartFromLatest))
	publish(t, q, &Message{Topic: topic})
	// Two holders "crash": each lease expires unfinished.
	for range 2 {
		require.Eventually(t, func() bool {
			cs, err := stores[0].Claim(ctx, ClaimRequest{Topic: topic, Group: "w", RunnerID: "crashed", Max: 1, Lease: time.Millisecond})
			require.NoError(t, err)
			return len(cs) == 1
		}, 10*time.Second, 20*time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	var calls atomic.Int32
	subscribe(t, q, topic, "w", func(ctx context.Context, d *Delivery) error {
		calls.Add(1)
		return nil
	}, WithMaxAttempts(2))
	waitStats(t, q, topic, "w", Stats{Dead: 1})
	assert.Zero(t, calls.Load(), "a poison delivery past MaxAttempts is not run again")
}

func engineContractCloseDrainsInFlight(t *testing.T, backend engineBackend) {
	q := engineQueues(t, backend(t, 1))[0]
	topic := uniqueTopic(t)
	started := make(chan struct{})
	release := make(chan struct{})
	sub, err := q.Subscribe(context.Background(), topic, "w", func(ctx context.Context, d *Delivery) error {
		close(started)
		<-release
		return nil
	}, fastOpts()...)
	require.NoError(t, err)
	publish(t, q, &Message{Topic: topic})
	<-started

	closed := make(chan error, 1)
	go func() { closed <- sub.Close(context.Background()) }()
	select {
	case <-closed:
		t.Fatal("Close returned while a handler was running")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-closed)
	waitStats(t, q, topic, "w", Stats{})
}

func engineContractCloseDeadlineKeepsRunningLease(t *testing.T, backend engineBackend) {
	q := engineQueues(t, backend(t, 1))[0]
	topic := uniqueTopic(t)
	started := make(chan struct{})
	cause := make(chan error, 1)
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	sub, err := q.Subscribe(context.Background(), topic, "w", func(ctx context.Context, d *Delivery) error {
		close(started)
		<-ctx.Done()
		cause <- context.Cause(ctx)
		<-stuck // ignores cancellation and keeps running
		return nil
	}, fastOpts(WithLeaseDuration(time.Minute))...)
	require.NoError(t, err)
	publish(t, q, &Message{Topic: topic})
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.NoError(t, sub.Close(ctx))
	assert.ErrorIs(t, <-cause, errSubscriptionClosed)
	// The handler outlived Close, so its lease is not handed back: nobody else
	// may start the delivery while it might still be running.
	st, err := q.Stats(context.Background(), topic, "w")
	require.NoError(t, err)
	assert.Equal(t, Stats{Leased: 1}, st)
}

func engineContractCloseReleasesUnstarted(t *testing.T, backend engineBackend) {
	stores := backend(t, 1)
	q := engineQueues(t, stores)[0]
	store := stores[0]
	ctx := context.Background()
	topic := uniqueTopic(t)
	s := newSubscription(q, topic, "w", func(context.Context, *Delivery) error { return nil }, SubscribeOptions{}.WithDefaults(), ctx)
	require.NoError(t, store.EnsureGroup(ctx, topic, "w", StartFromLatest))
	publish(t, q, &Message{Topic: topic})
	cs, err := store.Claim(ctx, ClaimRequest{Topic: topic, Group: "w", RunnerID: q.runnerID, Max: 1, Lease: time.Minute})
	require.NoError(t, err)
	require.Len(t, cs, 1)
	// Tracked but never handed to a worker, as when Close races a claim.
	require.True(t, s.track(cs[0], time.Now().Add(time.Minute)))
	close(s.claimerDone)
	close(s.work)
	go s.heartbeatLoop()

	require.NoError(t, s.Close(ctx))
	again, err := store.Claim(ctx, ClaimRequest{Topic: topic, Group: "w", RunnerID: "other", Max: 1, Lease: time.Minute})
	require.NoError(t, err)
	require.Len(t, again, 1, "released immediately")
	assert.Equal(t, 1, again[0].Attempt)
}

func engineContractWakeDeliversBeforePoll(t *testing.T, backend engineBackend) {
	q := engineQueues(t, backend(t, 1))[0]
	topic := uniqueTopic(t)
	got := make(chan time.Time, 1)
	subscribe(t, q, topic, "w", func(ctx context.Context, d *Delivery) error {
		got <- time.Now()
		return nil
	}, WithPollInterval(time.Minute))
	time.Sleep(200 * time.Millisecond) // let the first, empty poll pass
	sent := time.Now()
	publish(t, q, &Message{Topic: topic})
	select {
	case at := <-got:
		assert.Less(t, at.Sub(sent), 2*time.Second)
	case <-time.After(10 * time.Second):
		t.Fatal("publish did not wake the subscription")
	}
}

// faultStore wraps a real store and makes chosen operations fail a set number
// of times, to drive the engine's error paths over every backend.
type faultStore struct {
	Store
	mu       sync.Mutex
	failures map[string]int
	errs     map[string]error
	calls    map[string]int
	maxClaim int           // largest max a Claim was called with
	ackSizes []int         // batch size of every Ack call
	ackDelay time.Duration // added to every Ack, as a database round trip would
}

func newFaultStore(s Store) *faultStore {
	return &faultStore{Store: s, failures: map[string]int{}, errs: map[string]error{}, calls: map[string]int{}}
}

// inject makes the next n calls of op fail with err.
func (f *faultStore) inject(op string, n int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[op] = n
	f.errs[op] = err
}

func (f *faultStore) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[op]
}

func (f *faultStore) fault(op string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[op]++
	if f.failures[op] > 0 {
		f.failures[op]--
		return f.errs[op]
	}
	return nil
}

func (f *faultStore) Append(ctx context.Context, msgs []*Message) error {
	if err := f.fault("Append"); err != nil {
		return err
	}
	return f.Store.Append(ctx, msgs)
}

func (f *faultStore) Claim(ctx context.Context, req ClaimRequest) ([]*Claimed, error) {
	f.mu.Lock()
	f.maxClaim = max(f.maxClaim, req.Max)
	f.mu.Unlock()
	if err := f.fault("Claim"); err != nil {
		return nil, err
	}
	return f.Store.Claim(ctx, req)
}

func (f *faultStore) Ack(ctx context.Context, cs []*Claimed) ([]string, error) {
	f.mu.Lock()
	f.ackSizes = append(f.ackSizes, len(cs))
	delay := f.ackDelay
	f.mu.Unlock()
	time.Sleep(delay)
	if err := f.fault("Ack"); err != nil {
		return nil, err
	}
	return f.Store.Ack(ctx, cs)
}

func (f *faultStore) Retry(ctx context.Context, c *Claimed, backoff time.Duration, lastErr string) (bool, error) {
	if err := f.fault("Retry"); err != nil {
		return false, err
	}
	return f.Store.Retry(ctx, c, backoff, lastErr)
}

func (f *faultStore) Kill(ctx context.Context, c *Claimed, lastErr string) (bool, error) {
	if err := f.fault("Kill"); err != nil {
		return false, err
	}
	return f.Store.Kill(ctx, c, lastErr)
}

func (f *faultStore) Extend(ctx context.Context, cs []*Claimed, lease time.Duration) ([]string, error) {
	if err := f.fault("Extend"); err != nil {
		return nil, err
	}
	return f.Store.Extend(ctx, cs, lease)
}

func (f *faultStore) Release(ctx context.Context, cs []*Claimed) error {
	if err := f.fault("Release"); err != nil {
		return err
	}
	return f.Store.Release(ctx, cs)
}

var (
	// errTransient is classified as retryable by logstore.IsTransientWriteError.
	errTransient = fmt.Errorf("injected: %w", driver.ErrBadConn)
	errPermanent = errors.New("injected: permanent store failure")
)

func engineContractTransientPublishErrorIsRetried(t *testing.T, backend engineBackend) {
	f := newFaultStore(backend(t, 1)[0])
	q := engineQueues(t, []Store{f})[0]
	topic := uniqueTopic(t)
	f.inject("Append", storeOpRetries, errTransient)
	require.NoError(t, q.Publish(context.Background(), &Message{Topic: topic}))
	assert.Equal(t, storeOpRetries+1, f.count("Append"), "retried until it succeeded")

	f.inject("Append", storeOpRetries+1, errTransient)
	assert.ErrorIs(t, q.Publish(context.Background(), &Message{Topic: topic}), driver.ErrBadConn, "gives up after the retry budget")

	before := f.count("Append")
	f.inject("Append", 1, errPermanent)
	assert.ErrorIs(t, q.Publish(context.Background(), &Message{Topic: topic}), errPermanent)
	assert.Equal(t, before+1, f.count("Append"), "a permanent error is not retried")
}

func engineContractClaimErrorsDoNotStopConsuming(t *testing.T, backend engineBackend) {
	f := newFaultStore(backend(t, 1)[0])
	q := engineQueues(t, []Store{f})[0]
	topic := uniqueTopic(t)
	got := make(chan struct{}, 1)
	f.inject("Claim", 5, errPermanent)
	subscribe(t, q, topic, "w", func(ctx context.Context, d *Delivery) error {
		got <- struct{}{}
		return nil
	})
	publish(t, q, &Message{Topic: topic})
	select {
	case <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("subscription stopped consuming after claim errors")
	}
	assert.GreaterOrEqual(t, f.count("Claim"), 6)
}

func engineContractAckErrorRedeliversAfterLease(t *testing.T, backend engineBackend) {
	f := newFaultStore(backend(t, 1)[0])
	q := engineQueues(t, []Store{f})[0]
	topic := uniqueTopic(t)
	var calls atomic.Int32
	f.inject("Ack", 1, errPermanent)
	subscribe(t, q, topic, "w", func(ctx context.Context, d *Delivery) error {
		calls.Add(1)
		return nil
	}, WithLeaseDuration(400*time.Millisecond))
	publish(t, q, &Message{Topic: topic})
	waitStats(t, q, topic, "w", Stats{})
	assert.Equal(t, int32(2), calls.Load(), "the unacknowledged delivery runs again once its lease expires")
}

func engineContractDeadLetterPublishFailureRetries(t *testing.T, backend engineBackend) {
	f := newFaultStore(backend(t, 1)[0])
	q := engineQueues(t, []Store{f})[0]
	topic := uniqueTopic(t)
	dlqTopic := topic + ".dlq"
	dlq := make(chan *Delivery, 4)
	subscribe(t, q, dlqTopic, "inspect", func(ctx context.Context, d *Delivery) error {
		dlq <- d
		return nil
	})
	var calls atomic.Int32
	failed := make(chan struct{})
	subscribe(t, q, topic, "w", func(ctx context.Context, d *Delivery) error {
		if calls.Add(1) == 1 {
			// The next Append is the dead-letter copy: make it fail.
			f.inject("Append", 1, errPermanent)
			close(failed)
		}
		return errors.New("boom")
	}, WithMaxAttempts(1), WithDeadLetterTopic(dlqTopic))
	publish(t, q, &Message{Topic: topic})
	<-failed

	select {
	case <-dlq:
	case <-time.After(10 * time.Second):
		t.Fatal("dead-letter copy never published")
	}
	waitStats(t, q, topic, "w", Stats{Dead: 1})
	// The failed copy turns the dead-lettering into a retry; the retry is
	// already past MaxAttempts, so it dead-letters again without re-running
	// the handler.
	assert.Equal(t, int32(1), calls.Load())
	assert.GreaterOrEqual(t, f.count("Retry"), 1, "retried rather than dead-lettered without a copy")
	select {
	case d := <-dlq:
		t.Fatalf("a second dead-letter copy %s was published", d.ID)
	case <-time.After(300 * time.Millisecond):
	}
}

// engineContractHeartbeatErrorKeepsLease fails one heartbeat while a handler
// runs for two leases. Heartbeats come every lease/3, so one failed renewal
// leaves the lease live and the next one renews it.
func engineContractHeartbeatErrorKeepsLease(t *testing.T, backend engineBackend) {
	f := newFaultStore(backend(t, 1)[0])
	q := engineQueues(t, []Store{f})[0]
	topic := uniqueTopic(t)
	var calls atomic.Int32
	cancelled := make(chan error, 1)
	subscribe(t, q, topic, "w", func(ctx context.Context, d *Delivery) error {
		if calls.Add(1) == 1 {
			f.inject("Extend", 1, errPermanent)
		}
		select {
		case <-time.After(1200 * time.Millisecond):
			return nil
		case <-ctx.Done():
			cancelled <- context.Cause(ctx)
			return ctx.Err()
		}
	}, WithLeaseDuration(600*time.Millisecond))
	publish(t, q, &Message{Topic: topic})
	waitStats(t, q, topic, "w", Stats{})
	assert.Equal(t, int32(1), calls.Load())
	assert.Empty(t, cancelled)
	assert.GreaterOrEqual(t, f.count("Extend"), 3, "heartbeats kept going after the failure")
}

// engineContractLapsedLeaseCancelsWithoutFreeSlots fails every heartbeat
// while the only worker slot is busy, so the subscription cannot re-claim the
// delivery itself: the handler must still be cancelled once the lease it last
// confirmed has run out, before another instance may take the delivery over.
func engineContractLapsedLeaseCancelsWithoutFreeSlots(t *testing.T, backend engineBackend) {
	const lease = 400 * time.Millisecond
	f := newFaultStore(backend(t, 1)[0])
	q := engineQueues(t, []Store{f})[0]
	topic := uniqueTopic(t)
	started := make(chan time.Time, 1)
	cause := make(chan error, 1)
	release := make(chan struct{})
	subscribe(t, q, topic, "w", func(ctx context.Context, d *Delivery) error {
		if d.Attempt > 1 {
			return nil
		}
		f.inject("Extend", 1_000_000, errPermanent)
		started <- time.Now()
		select {
		case <-ctx.Done():
			cause <- context.Cause(ctx)
			return ctx.Err()
		case <-release:
			return nil
		}
	}, WithConcurrency(1), WithLeaseDuration(lease))
	// Registered after subscribe's own cleanup, so it runs first and a red
	// run cannot leave Close waiting on the blocked handler.
	t.Cleanup(func() { close(release) })
	publish(t, q, &Message{Topic: topic})
	at := <-started
	select {
	case err := <-cause:
		assert.ErrorIs(t, err, errLeaseLost)
		assert.Less(t, time.Since(at), 2*lease, "cancelled soon after the lease lapsed")
	case <-time.After(10 * time.Second):
		t.Fatal("handler kept running on a lapsed lease with no free slot to re-claim it")
	}
	f.inject("Extend", 0, nil)
	waitStats(t, q, topic, "w", Stats{})
}

// engineContractStaleHandlerDoesNotDeadLetter lets a handler lose its lease,
// then return a Permanent error: the delivery now belongs to another claim,
// so no dead-letter copy may be published for it.
func engineContractStaleHandlerDoesNotDeadLetter(t *testing.T, backend engineBackend) {
	const lease = 400 * time.Millisecond
	q := engineQueues(t, []Store{slowExtendStore{Store: backend(t, 1)[0], delay: 2 * lease}})[0]
	topic := uniqueTopic(t)
	dlqTopic := topic + ".dlq"
	dlq := make(chan *Delivery, 4)
	subscribe(t, q, dlqTopic, "inspect", func(ctx context.Context, d *Delivery) error {
		dlq <- d
		return nil
	})
	var calls atomic.Int32
	subscribe(t, q, topic, "w", func(ctx context.Context, d *Delivery) error {
		if calls.Add(1) > 1 {
			return nil
		}
		<-ctx.Done() // lease lost and taken over
		return Permanent(errors.New("too late"))
	}, WithConcurrency(2), WithLeaseDuration(lease), WithDeadLetterTopic(dlqTopic))
	publish(t, q, &Message{Topic: topic})
	waitStats(t, q, topic, "w", Stats{})
	select {
	case d := <-dlq:
		t.Fatalf("a stale handler dead-lettered delivery %s", d.Headers[HeaderDeadLetterDeliveryID])
	case <-time.After(300 * time.Millisecond):
	}
}

// engineContractRepeatedHeartbeatErrorsLoseLease fails every heartbeat: the
// lease runs out, the handler is cancelled, and the delivery runs again.
func engineContractRepeatedHeartbeatErrorsLoseLease(t *testing.T, backend engineBackend) {
	f := newFaultStore(backend(t, 1)[0])
	q := engineQueues(t, []Store{f})[0]
	topic := uniqueTopic(t)
	var calls atomic.Int32
	cause := make(chan error, 1)
	subscribe(t, q, topic, "w", func(ctx context.Context, d *Delivery) error {
		if calls.Add(1) > 1 {
			return nil
		}
		f.inject("Extend", 1_000_000, errPermanent)
		<-ctx.Done()
		cause <- context.Cause(ctx)
		f.inject("Extend", 0, nil)
		return ctx.Err()
	}, WithLeaseDuration(400*time.Millisecond))
	publish(t, q, &Message{Topic: topic})
	select {
	case err := <-cause:
		assert.ErrorIs(t, err, errLeaseLost)
	case <-time.After(10 * time.Second):
		t.Fatal("handler kept running on a lease that could not be renewed")
	}
	waitStats(t, q, topic, "w", Stats{})
	assert.Equal(t, int32(2), calls.Load())
}

func engineContractReleaseErrorIsReported(t *testing.T, backend engineBackend) {
	stores := backend(t, 1)
	f := newFaultStore(stores[0])
	q := engineQueues(t, []Store{f})[0]
	ctx := context.Background()
	topic := uniqueTopic(t)
	s := newSubscription(q, topic, "w", func(context.Context, *Delivery) error { return nil }, SubscribeOptions{}.WithDefaults(), ctx)
	require.NoError(t, f.EnsureGroup(ctx, topic, "w", StartFromLatest))
	publish(t, q, &Message{Topic: topic})
	cs, err := f.Claim(ctx, ClaimRequest{Topic: topic, Group: "w", RunnerID: q.runnerID, Max: 1, Lease: time.Minute})
	require.NoError(t, err)
	require.Len(t, cs, 1)
	require.True(t, s.track(cs[0], time.Now().Add(time.Minute)))
	close(s.claimerDone)
	close(s.work)
	go s.heartbeatLoop()

	f.inject("Release", 1, errPermanent)
	assert.ErrorIs(t, s.Close(ctx), errPermanent, "Close reports leases it could not hand back")
}

// engineContractConcurrencyLimit floods a subscription and checks the number
// of handlers running at once never exceeds Concurrency, and reaches it.
func engineContractConcurrencyLimit(t *testing.T, backend engineBackend) {
	q := engineQueues(t, backend(t, 1))[0]
	topic := uniqueTopic(t)
	const limit, total = 3, 24
	var running, peak, done atomic.Int32
	subscribe(t, q, topic, "w", func(ctx context.Context, d *Delivery) error {
		n := running.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(40 * time.Millisecond)
		running.Add(-1)
		done.Add(1)
		return nil
	}, WithConcurrency(limit), WithBatchSize(10))
	msgs := make([]*Message, total)
	for i := range msgs {
		msgs[i] = &Message{Topic: topic}
	}
	publish(t, q, msgs...)
	require.Eventually(t, func() bool { return done.Load() == total }, 20*time.Second, 10*time.Millisecond)
	assert.Equal(t, int32(limit), peak.Load())
}

// engineContractBatchSizeAndImmediateRepoll checks claims never ask for more
// than BatchSize, and that a full batch is followed by another claim at once
// rather than after the (here, very long) poll interval.
func engineContractBatchSizeAndImmediateRepoll(t *testing.T, backend engineBackend) {
	f := newFaultStore(backend(t, 1)[0])
	q := engineQueues(t, []Store{f})[0]
	topic := uniqueTopic(t)
	const total = 20
	var done atomic.Int32
	subscribe(t, q, topic, "w", func(ctx context.Context, d *Delivery) error {
		done.Add(1)
		return nil
	}, WithBatchSize(2), WithConcurrency(8), WithPollInterval(time.Minute))
	msgs := make([]*Message, total)
	for i := range msgs {
		msgs[i] = &Message{Topic: topic}
	}
	publish(t, q, msgs...)
	require.Eventually(t, func() bool { return done.Load() == total }, 10*time.Second, 10*time.Millisecond,
		"a full batch must trigger the next claim without waiting for the poll interval")
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.LessOrEqual(t, f.maxClaim, 2)
}

// engineContractSubscriptionsInOneGroupCompete runs two subscriptions of the
// same group in one instance: together they handle each message once.
func engineContractSubscriptionsInOneGroupCompete(t *testing.T, backend engineBackend) {
	q := engineQueues(t, backend(t, 1))[0]
	topic := uniqueTopic(t)
	var mu sync.Mutex
	seen := map[string]int{}
	per := [2]int{}
	for i := range 2 {
		subscribe(t, q, topic, "shared", func(ctx context.Context, d *Delivery) error {
			time.Sleep(30 * time.Millisecond)
			mu.Lock()
			seen[d.ID]++
			per[i]++
			mu.Unlock()
			return nil
		}, WithConcurrency(1))
	}
	const total = 20
	for range total {
		publish(t, q, &Message{Topic: topic})
	}
	waitStats(t, q, topic, "shared", Stats{})
	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, seen, total)
	for id, n := range seen {
		assert.Equal(t, 1, n, "message %s handled %d times", id, n)
	}
	assert.Positive(t, per[0], "first subscription got work")
	assert.Positive(t, per[1], "second subscription got work")
}

type ctxKey struct{}

// engineContractSubscribeContextOnlyRegisters cancels Subscribe's context
// right away: the subscription keeps running until Close, and handlers still
// see the context's values.
func engineContractSubscribeContextOnlyRegisters(t *testing.T, backend engineBackend) {
	q := engineQueues(t, backend(t, 1))[0]
	topic := uniqueTopic(t)
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "request-scoped"))
	got := make(chan any, 1)
	sub, err := q.Subscribe(ctx, topic, "w", func(hctx context.Context, d *Delivery) error {
		got <- hctx.Value(ctxKey{})
		return hctx.Err()
	}, fastOpts()...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Close(context.Background()) })
	cancel()

	publish(t, q, &Message{Topic: topic})
	select {
	case v := <-got:
		assert.Equal(t, "request-scoped", v)
	case <-time.After(10 * time.Second):
		t.Fatal("cancelling Subscribe's context stopped the subscription")
	}
	waitStats(t, q, topic, "w", Stats{})
}

// engineContractMultiTopicPublish publishes to two topics in one call with
// caller-set IDs and publish times, which must be kept.
func engineContractMultiTopicPublish(t *testing.T, backend engineBackend) {
	q := engineQueues(t, backend(t, 1))[0]
	a, b := uniqueTopic(t), uniqueTopic(t)
	got := make(chan *Delivery, 2)
	for _, topic := range []string{a, b} {
		subscribe(t, q, topic, "w", func(ctx context.Context, d *Delivery) error {
			got <- d
			return nil
		})
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.UTC)
	ma := &Message{ID: "fixed-" + a, Topic: a, PublishedAt: at}
	mb := &Message{ID: "fixed-" + b, Topic: b, PublishedAt: at}
	publish(t, q, ma, mb)
	byTopic := map[string]*Delivery{}
	for range 2 {
		select {
		case d := <-got:
			byTopic[d.Topic] = d
		case <-time.After(10 * time.Second):
			t.Fatal("multi-topic publish not delivered to both topics")
		}
	}
	for _, m := range []*Message{ma, mb} {
		d := byTopic[m.Topic]
		require.NotNil(t, d, m.Topic)
		assert.Equal(t, m.ID, d.ID)
		assert.WithinDuration(t, at, d.PublishedAt, time.Millisecond, "caller-set publish time kept")
	}
}

// engineContractAcksAreBatched finishes handlers in waves of eight while each
// Ack takes a few milliseconds, as a database round trip does: completions
// that arrive during an Ack share the next one instead of taking a round trip
// each, and every delivery is still acknowledged exactly once.
func engineContractAcksAreBatched(t *testing.T, backend engineBackend) {
	f := newFaultStore(backend(t, 1)[0])
	f.ackDelay = 5 * time.Millisecond
	q := engineQueues(t, []Store{f})[0]
	topic := uniqueTopic(t)
	const total, wave = 64, 8
	var mu sync.Mutex
	gate := make(chan struct{})
	var started, done atomic.Int32
	subscribe(t, q, topic, "w", func(ctx context.Context, d *Delivery) error {
		mu.Lock()
		g := gate
		mu.Unlock()
		started.Add(1)
		<-g
		done.Add(1)
		return nil
	}, WithConcurrency(wave), WithBatchSize(wave))
	msgs := make([]*Message, total)
	for i := range msgs {
		msgs[i] = &Message{Topic: topic}
	}
	publish(t, q, msgs...)
	for released := 0; released < total; released += wave {
		require.Eventually(t, func() bool { return started.Load() == int32(released+wave) }, 10*time.Second, time.Millisecond)
		// Release the whole wave at once.
		mu.Lock()
		close(gate)
		gate = make(chan struct{})
		mu.Unlock()
	}
	waitStats(t, q, topic, "w", Stats{})
	assert.Equal(t, int32(total), done.Load())

	f.mu.Lock()
	defer f.mu.Unlock()
	sum, largest := 0, 0
	for _, n := range f.ackSizes {
		sum += n
		largest = max(largest, n)
	}
	assert.Equal(t, total, sum, "every delivery acknowledged once")
	assert.Less(t, len(f.ackSizes), total/2, "acknowledgements shared store calls")
	assert.Greater(t, largest, 1)
}

// engineContractMaxInFlightAcrossInstances runs three instances of one group,
// each allowed four handlers, under a cluster-wide cap of two: no more than
// two handlers ever run at once across them, and the cap is reached.
func engineContractMaxInFlightAcrossInstances(t *testing.T, backend engineBackend) {
	pods := engineQueues(t, backend(t, 3))
	topic := uniqueTopic(t)
	const limit, total = 2, 18
	var running, peak, done atomic.Int32
	for _, p := range pods {
		subscribe(t, p, topic, "g", func(ctx context.Context, d *Delivery) error {
			n := running.Add(1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			time.Sleep(60 * time.Millisecond)
			running.Add(-1)
			done.Add(1)
			return nil
		}, WithConcurrency(4), WithMaxInFlight(limit))
	}
	msgs := make([]*Message, total)
	for i := range msgs {
		msgs[i] = &Message{Topic: topic}
	}
	publish(t, pods[0], msgs...)
	require.Eventually(t, func() bool { return done.Load() == total }, 30*time.Second, 10*time.Millisecond)
	assert.Equal(t, int32(limit), peak.Load())
}

// engineContractScheduledOnTime publishes a message for half a second ahead:
// the handler never runs early and runs within a poll interval of the time.
func engineContractScheduledOnTime(t *testing.T, backend engineBackend) {
	q := engineQueues(t, backend(t, 1))[0]
	topic := uniqueTopic(t)
	got := make(chan *Delivery, 1)
	ran := make(chan time.Time, 1)
	const poll = 100 * time.Millisecond
	subscribe(t, q, topic, "w", func(ctx context.Context, d *Delivery) error {
		ran <- time.Now()
		got <- d
		return nil
	}, WithPollInterval(poll))
	at := time.Now().Add(500 * time.Millisecond)
	publish(t, q, &Message{Topic: topic, DeliverAt: at})
	select {
	case when := <-ran:
		d := <-got
		assert.False(t, when.Before(at), "ran %s early", at.Sub(when))
		assert.Less(t, when.Sub(at), poll*3, "ran %s late", when.Sub(at))
		assert.WithinDuration(t, at, d.DeliverAt, time.Millisecond)
	case <-time.After(10 * time.Second):
		t.Fatal("scheduled message never ran")
	}
}

// queueGoroutines counts goroutines running queue engine or store code, so a
// leak check is not confused by driver or pool goroutines.
func queueGoroutines() int {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	n := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		for _, frame := range []string{"queue.(*subscription)", "queue.(*storeQueue)", "queue.listenPostgres", "queue.listenOnce"} {
			if strings.Contains(g, frame) {
				n++
				break
			}
		}
	}
	return n
}

// engineContractCloseLeavesNoGoroutines runs a busy queue, closes it, and
// checks that every engine goroutine exits.
func engineContractCloseLeavesNoGoroutines(t *testing.T, backend engineBackend) {
	stores := backend(t, 1)
	baseline := queueGoroutines()
	q, err := NewStoreQueue(stores[0], EngineConfig{}, nil)
	require.NoError(t, err)
	topic := uniqueTopic(t)
	for _, g := range []string{"a", "b"} {
		_, err := q.Subscribe(context.Background(), topic, g, func(context.Context, *Delivery) error { return nil }, fastOpts(WithConcurrency(4))...)
		require.NoError(t, err)
	}
	require.NoError(t, q.Publish(context.Background(), &Message{Topic: topic}, &Message{Topic: topic}))
	require.Eventually(t, func() bool { return queueGoroutines() > baseline }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, q.Close(context.Background()))
	require.Eventually(t, func() bool { return queueGoroutines() <= baseline }, 5*time.Second, 10*time.Millisecond,
		"engine goroutines still running after Close: %d (baseline %d)", queueGoroutines(), baseline)
}

// blockingStore makes chosen operations wait until their context ends, like
// a store that stopped answering.
type blockingStore struct {
	Store
	block map[string]bool
}

func (b blockingStore) wait(ctx context.Context, op string) error {
	if !b.block[op] {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func (b blockingStore) Extend(ctx context.Context, cs []*Claimed, lease time.Duration) ([]string, error) {
	if err := b.wait(ctx, "Extend"); err != nil {
		return nil, err
	}
	return b.Store.Extend(ctx, cs, lease)
}

func (b blockingStore) Release(ctx context.Context, cs []*Claimed) error {
	if err := b.wait(ctx, "Release"); err != nil {
		return err
	}
	return b.Store.Release(ctx, cs)
}

func (b blockingStore) Purge(ctx context.Context, policy PurgePolicy, batch int) (int64, error) {
	if err := b.wait(ctx, "Purge"); err != nil {
		return 0, err
	}
	return b.Store.Purge(ctx, policy, batch)
}

// TestCloseHonoursDeadlineWithStuckStoreCalls closes a subscription and a
// queue while a heartbeat, a release and a purge are each stuck in the store:
// Close must return close to its own deadline instead of waiting out the
// calls' internal timeouts.
func TestCloseHonoursDeadlineWithStuckStoreCalls(t *testing.T) {
	const deadline = 100 * time.Millisecond
	const slack = 400 * time.Millisecond

	t.Run("heartbeat", func(t *testing.T) {
		q, err := NewStoreQueue(blockingStore{Store: newMemoryStore(), block: map[string]bool{"Extend": true}}, EngineConfig{}, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = q.Close(context.Background()) })
		started := make(chan struct{})
		sub, err := q.Subscribe(context.Background(), "t", "g", func(ctx context.Context, d *Delivery) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}, WithLeaseDuration(3*time.Second), WithPollInterval(20*time.Millisecond))
		require.NoError(t, err)
		require.NoError(t, q.Publish(context.Background(), &Message{Topic: "t"}))
		<-started
		time.Sleep(1100 * time.Millisecond) // the first heartbeat is now stuck in Extend
		ctx, cancel := context.WithTimeout(context.Background(), deadline)
		defer cancel()
		start := time.Now()
		_ = sub.Close(ctx)
		assert.Less(t, time.Since(start), deadline+slack)
	})

	t.Run("release", func(t *testing.T) {
		store := newMemoryStore()
		q, err := NewStoreQueue(blockingStore{Store: store, block: map[string]bool{"Release": true}}, EngineConfig{}, nil)
		require.NoError(t, err)
		sq := q.(*storeQueue)
		t.Cleanup(func() { _ = q.Close(context.Background()) })
		ctx := context.Background()
		require.NoError(t, store.EnsureGroup(ctx, "t", "g", StartFromLatest))
		require.NoError(t, q.Publish(ctx, &Message{Topic: "t"}))
		s := newSubscription(sq, "t", "g", func(context.Context, *Delivery) error { return nil }, SubscribeOptions{}.WithDefaults(), ctx)
		cs, err := store.Claim(ctx, ClaimRequest{Topic: "t", Group: "g", RunnerID: sq.runnerID, Max: 1, Lease: time.Minute})
		require.NoError(t, err)
		require.True(t, s.track(cs[0], time.Now().Add(time.Minute)))
		close(s.claimerDone)
		close(s.work)
		go s.heartbeatLoop()
		closeCtx, cancel := context.WithTimeout(ctx, deadline)
		defer cancel()
		start := time.Now()
		_ = s.Close(closeCtx)
		assert.Less(t, time.Since(start), deadline+slack)
	})

	t.Run("purge", func(t *testing.T) {
		q, err := NewStoreQueue(blockingStore{Store: newMemoryStore(), block: map[string]bool{"Purge": true}},
			EngineConfig{JanitorInterval: "10ms"}, nil)
		require.NoError(t, err)
		time.Sleep(100 * time.Millisecond) // a janitor pass is now stuck in Purge
		ctx, cancel := context.WithTimeout(context.Background(), deadline)
		defer cancel()
		start := time.Now()
		_ = q.Close(ctx)
		assert.Less(t, time.Since(start), deadline+slack)
	})
}

func TestTruncateErrorKeepsValidUTF8(t *testing.T) {
	// "é" is two bytes; place it so byte maxErrorLength falls inside it.
	s := strings.Repeat("a", maxErrorLength-1) + "é" + "tail"
	got := truncateError(s)
	assert.True(t, utf8.ValidString(got), "truncated text must stay valid UTF-8")
	assert.LessOrEqual(t, len(got), maxErrorLength)
	assert.Equal(t, strings.Repeat("a", maxErrorLength-1), got)
	assert.Equal(t, "short", truncateError("short"))
}

// TestMemoryPurgeDropsQueueReferences purges dead deliveries of a group that
// never claims again: the group's queue must not keep them, or their
// payloads, alive.
func TestMemoryPurgeDropsQueueReferences(t *testing.T) {
	s := newMemoryStore()
	ctx := context.Background()
	mustGroup(t, s, "t", "g", StartFromLatest)
	for i := range 3 {
		mustAppend(t, s, newMsg("t", "", fmt.Sprint(i)))
	}
	for _, c := range mustClaim(t, s, "t", "g", 3, time.Minute) {
		ok, err := s.Kill(ctx, c, "boom")
		require.NoError(t, err)
		require.True(t, ok)
	}
	s.mu.Lock()
	realNow := s.now
	s.now = func() time.Time { return realNow().Add(time.Hour) }
	s.mu.Unlock()
	_, err := s.Purge(ctx, PurgePolicy{DeadRetention: time.Minute, AckedRetention: time.Minute}, 100)
	require.NoError(t, err)
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Empty(t, s.queues[memoryGroupKey{"t", "g"}], "purged deliveries are dropped from their queue")
}

// TestMemoryClaimStopsScanningWhenFull claims one delivery from a large
// backlog: the claim must not walk the whole queue under the store lock.
func TestMemoryClaimStopsScanningWhenFull(t *testing.T) {
	s := newMemoryStore()
	mustGroup(t, s, "t", "g", StartFromLatest)
	msgs := make([]*Message, 10_000)
	for i := range msgs {
		msgs[i] = newMsg("t", "", fmt.Sprint(i))
	}
	mustAppend(t, s, msgs...)
	before := s.scanned.Load()
	require.Len(t, mustClaim(t, s, "t", "g", 1, time.Minute), 1)
	assert.Less(t, s.scanned.Load()-before, int64(10), "a one-delivery claim scanned the whole backlog")
}

// TestMemoryClaimLeavesUnscannedTailInPlace claims behind acknowledged
// deliveries: compacting the scanned prefix must not move the rest of the
// backlog, or every claim copies the whole queue.
func TestMemoryClaimLeavesUnscannedTailInPlace(t *testing.T) {
	ctx := context.Background()
	s := newMemoryStore()
	mustGroup(t, s, "t", "g", StartFromLatest)
	msgs := make([]*Message, 1_000)
	for i := range msgs {
		msgs[i] = newMsg("t", "", fmt.Sprint(i))
	}
	mustAppend(t, s, msgs...)
	cs := mustClaim(t, s, "t", "g", 10, time.Minute)
	held, err := s.Ack(ctx, cs)
	require.NoError(t, err)
	require.Len(t, held, 10)

	k := memoryGroupKey{"t", "g"}
	last := &s.queues[k][len(s.queues[k])-1]
	require.Len(t, mustClaim(t, s, "t", "g", 10, time.Minute), 10)
	queue := s.queues[k]
	assert.Len(t, queue, 990, "the acknowledged deliveries are compacted away")
	assert.Same(t, last, &queue[len(queue)-1], "the unscanned tail was copied")
}

func TestEngineValidation(t *testing.T) {
	q, _ := newTestQueue(t)
	ctx := context.Background()
	assert.ErrorIs(t, q.Publish(ctx, &Message{Topic: "bad topic"}), ErrInvalidName)
	assert.ErrorIs(t, q.Publish(ctx, &Message{Topic: "t", Payload: make([]byte, DefaultMaxPayloadBytes+1)}), ErrPayloadTooLarge)
	assert.NoError(t, q.Publish(ctx))

	noop := func(context.Context, *Delivery) error { return nil }
	_, err := q.Subscribe(ctx, "t", "bad group", noop)
	assert.ErrorIs(t, err, ErrInvalidName)
	_, err = q.Subscribe(ctx, "t", "g", nil)
	assert.Error(t, err)
	_, err = q.Subscribe(ctx, "t", "g", noop, WithLeaseDuration(time.Millisecond))
	assert.Error(t, err)
	// A dead-letter copy published back onto the consumed topic would be
	// consumed, fail, and be dead-lettered again under a new ID, forever.
	_, err = q.Subscribe(ctx, "t", "g", noop, WithDeadLetterTopic("t"))
	assert.ErrorIs(t, err, ErrDeadLetterLoop)
}

func TestEngineClosedQueue(t *testing.T) {
	store := newMemoryStore()
	q, err := NewStoreQueue(store, EngineConfig{}, nil)
	require.NoError(t, err)
	subscribe(t, q, "t", "g", func(context.Context, *Delivery) error { return nil })
	require.NoError(t, q.Close(context.Background()))
	require.NoError(t, q.Close(context.Background()), "Close is idempotent")

	ctx := context.Background()
	assert.ErrorIs(t, q.Publish(ctx, &Message{Topic: "t"}), ErrClosed)
	_, err = q.Subscribe(ctx, "t", "g", func(context.Context, *Delivery) error { return nil })
	assert.ErrorIs(t, err, ErrClosed)
	assert.ErrorIs(t, q.Ping(ctx), ErrClosed)
	assert.ErrorIs(t, store.Ping(ctx), ErrClosed, "the queue owns and closes its store")
}

func TestEngineJanitorPurges(t *testing.T) {
	store := newMemoryStore()
	q, err := NewStoreQueue(store, EngineConfig{JanitorInterval: "20ms", RetentionHours: 1, AckedRetentionHours: 1}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close(context.Background()) })
	publish(t, q, &Message{Topic: "nobody-listens"})
	// Age everything past retention.
	store.mu.Lock()
	realNow := store.now
	store.now = func() time.Time { return realNow().Add(2 * time.Hour) }
	store.mu.Unlock()
	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(store.messages) == 0
	}, 5*time.Second, 10*time.Millisecond)
}

func TestEngineRejectsScheduleBeyondRetention(t *testing.T) {
	ctx := context.Background()
	q, err := NewStoreQueue(newMemoryStore(), EngineConfig{RetentionHours: 1}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close(ctx) })
	assert.NoError(t, q.Publish(ctx, &Message{Topic: "t", DeliverAt: time.Now().Add(50 * time.Minute)}))
	assert.ErrorIs(t, q.Publish(ctx, &Message{Topic: "t", DeliverAt: time.Now().Add(2 * time.Hour)}), ErrScheduleTooFar,
		"a message must not outlive its retention before it runs")
}

func TestNewQueueAppliesEngineConfig(t *testing.T) {
	ctx := context.Background()
	q, err := NewQueue(ctx, &Config{Enabled: true, Type: QueueTypeMemory, Config: &MemoryQueueConfig{EngineConfig: EngineConfig{MaxPayloadBytes: 10}}}, Dependencies{}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close(ctx) })
	assert.NoError(t, q.Publish(ctx, &Message{Topic: "t", Payload: make([]byte, 10)}))
	assert.ErrorIs(t, q.Publish(ctx, &Message{Topic: "t", Payload: make([]byte, 11)}), ErrPayloadTooLarge)

	_, err = NewQueue(ctx, &Config{Enabled: true, Type: QueueTypeMemory, Config: &MemoryQueueConfig{EngineConfig: EngineConfig{JanitorInterval: "soon"}}}, Dependencies{}, nil)
	assert.Error(t, err, "an invalid janitor interval fails construction")
}

// purgeCountingStore reports a full purge batch a set number of times.
type purgeCountingStore struct {
	Store
	full  atomic.Int32 // remaining full batches to report
	calls atomic.Int32
}

func (p *purgeCountingStore) Purge(ctx context.Context, policy PurgePolicy, batch int) (int64, error) {
	p.calls.Add(1)
	if p.full.Add(-1) >= 0 {
		return int64(batch), nil
	}
	return 0, nil
}

// TestJanitorPurgesInBatches checks one janitor pass keeps purging while
// batches come back full, and stops at its per-pass cap.
func TestJanitorPurgesInBatches(t *testing.T) {
	for _, tc := range []struct {
		full      int32
		wantCalls int32
	}{
		{full: 0, wantCalls: 1},
		{full: 3, wantCalls: 4},
		{full: 1000, wantCalls: maxPurgeBatchesPerPass},
	} {
		store := &purgeCountingStore{Store: newMemoryStore()}
		store.full.Store(tc.full)
		q, err := NewStoreQueue(store, EngineConfig{JanitorInterval: "1h"}, nil)
		require.NoError(t, err)
		q.(*storeQueue).purgeOnce()
		assert.Equal(t, tc.wantCalls, store.calls.Load(), "full batches %d", tc.full)
		require.NoError(t, q.Close(context.Background()))
	}
}

func TestMemoryQueueRegistered(t *testing.T) {
	q, err := NewQueue(context.Background(), &Config{Enabled: true, Type: QueueTypeMemory}, Dependencies{}, nil)
	require.NoError(t, err)
	require.NotNil(t, q)
	require.NoError(t, q.Ping(context.Background()))
	require.NoError(t, q.Close(context.Background()))

	_, err = NewQueue(context.Background(), &Config{Enabled: true, Type: QueueTypeMemory, Config: &LogStoreQueueConfig{}}, Dependencies{}, nil)
	assert.Error(t, err, "wrong config type is rejected")
}

func TestMemoryMultiInstance(t *testing.T) {
	store := newMemoryStore()
	runMultiInstanceSimulation(t, func(t *testing.T, logger schemas.Logger) Queue {
		q, err := NewStoreQueue(sharedStore{store}, EngineConfig{}, logger)
		require.NoError(t, err)
		return q
	}, 3, 300)
}

// leaseLossLogger records the deliveries the engine reports as having lost
// their lease, which are the only ones allowed to run more than once.
type leaseLossLogger struct {
	schemas.Logger
	mu   sync.Mutex
	lost map[string]bool
}

func newLeaseLossLogger() *leaseLossLogger {
	return &leaseLossLogger{Logger: bifrost.NewNoOpLogger(), lost: map[string]bool{}}
}

func (l *leaseLossLogger) record(msg string, args []any, idArg int) {
	if !strings.HasPrefix(msg, "queue: lease lost") || len(args) <= idArg {
		return
	}
	if id, ok := args[idArg].(string); ok {
		l.mu.Lock()
		l.lost[id] = true
		l.mu.Unlock()
	}
}

// Warn sees "lease lost before <op> of delivery <id>"; Debug sees the
// heartbeat's "lease lost for delivery <id>".
func (l *leaseLossLogger) Warn(msg string, args ...any)  { l.record(msg, args, 1) }
func (l *leaseLossLogger) Debug(msg string, args ...any) { l.record(msg, args, 0) }

func (l *leaseLossLogger) wasLost(deliveryID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lost[deliveryID]
}

// sharedStore lets several engine instances ("pods") share one store without
// the first Close closing it for the others.
type sharedStore struct{ Store }

func (sharedStore) Close(context.Context) error { return nil }

// runMultiInstanceSimulation starts pods engine instances over one shared
// backend and drives perGroup keyed and unkeyed messages through three
// consumer groups, failing a fifth of first attempts. It asserts that every
// (message, group) pair completes, that per-key order holds in every group
// (including one registered while publishing is under way), and that a
// delivery runs twice, or on two instances at once, only when the engine
// reported its lease lost: at-least-once, never silently more.
func runMultiInstanceSimulation(t *testing.T, newPod func(t *testing.T, logger schemas.Logger) Queue, pods, perGroup int) {
	t.Helper()
	ctx := context.Background()
	topic := "sim" + uniqueTopic(t)
	groups := []string{"g1", "g2", "late"}
	const keys = 7

	type keyState struct {
		mu   sync.Mutex
		last map[string]int // group/key -> last sequence handled
	}
	order := keyState{last: map[string]int{}}
	var (
		mu       sync.Mutex
		handled  = map[string]int{}  // group/message -> times completed
		running  = map[string]bool{} // delivery id -> running now
		overlaps = map[string]bool{} // delivery ids that ran concurrently
	)
	losses := newLeaseLossLogger()
	handler := func(group string) Handler {
		return func(ctx context.Context, d *Delivery) error {
			mu.Lock()
			if running[d.DeliveryID] {
				overlaps[d.DeliveryID] = true
			}
			running[d.DeliveryID] = true
			mu.Unlock()
			defer func() {
				mu.Lock()
				delete(running, d.DeliveryID)
				mu.Unlock()
			}()

			if d.Key != "" {
				var seq int
				_, _ = fmt.Sscan(d.Headers["seq"], &seq)
				order.mu.Lock()
				k := group + "/" + d.Key
				if prev, ok := order.last[k]; ok && seq <= prev && d.Attempt == 1 {
					order.mu.Unlock()
					return Permanent(fmt.Errorf("key %s out of order: %d after %d", k, seq, prev))
				}
				order.last[k] = seq
				order.mu.Unlock()
			}
			// Fail a slice of first attempts to exercise retries under contention.
			if n, _ := strconv.Atoi(string(d.Payload)); d.Attempt == 1 && n%5 == 0 {
				return errors.New("transient")
			}
			mu.Lock()
			handled[group+"/"+d.ID]++
			mu.Unlock()
			return nil
		}
	}

	queues := make([]Queue, pods)
	for i := range queues {
		queues[i] = newPod(t, losses)
		t.Cleanup(func() { _ = queues[i].Close(context.Background()) })
		for _, g := range groups[:2] {
			_, err := queues[i].Subscribe(ctx, topic, g, handler(g), fastOpts(WithConcurrency(4), WithBatchSize(8), WithMaxAttempts(10))...)
			require.NoError(t, err)
		}
	}

	ids := make([]string, 0, perGroup)
	var lateOnce sync.Once
	for i := range perGroup {
		m := &Message{Topic: topic, Payload: []byte(fmt.Sprint(i)), Headers: map[string]string{"seq": fmt.Sprint(i)}}
		if i%2 == 0 {
			m.Key = fmt.Sprintf("k%d", i%keys)
		}
		require.NoError(t, queues[i%pods].Publish(ctx, m))
		ids = append(ids, m.ID)
		if i == perGroup/3 {
			// Register the late group mid-stream from another instance. It starts
			// from the earliest retained message, so it must still see everything.
			lateOnce.Do(func() {
				for _, q := range queues {
					_, err := q.Subscribe(ctx, topic, "late", handler("late"), fastOpts(WithConcurrency(4), WithBatchSize(8), WithMaxAttempts(10), WithStartFrom(StartFromEarliest))...)
					require.NoError(t, err)
				}
			})
		}
	}

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		mu.Lock()
		defer mu.Unlock()
		for _, g := range groups {
			for _, id := range ids {
				assert.GreaterOrEqual(c, handled[g+"/"+id], 1, "%s/%s", g, id)
			}
		}
	}, 60*time.Second, 50*time.Millisecond)

	for _, g := range groups {
		waitStats(t, queues[0], topic, g, Stats{})
	}
	mu.Lock()
	defer mu.Unlock()
	for id := range overlaps {
		assert.True(t, losses.wasLost(id), "delivery %s ran on two instances at once without losing its lease", id)
	}
	for _, g := range groups {
		for _, id := range ids {
			if handled[g+"/"+id] > 1 {
				assert.True(t, losses.wasLost(DeliveryID(id, g)), "%s/%s completed %d times without losing its lease", g, id, handled[g+"/"+id])
			}
		}
	}
}

func BenchmarkMemoryQueue(b *testing.B) {
	runBenchmarks(b, benchBackend{
		fresh: func(b *testing.B) (Store, func()) { return newMemoryStore(), func() {} },
		footprint: func(b *testing.B, s Store) int64 {
			runtime.GC()
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			runtime.KeepAlive(s) // an open queue's store is still reachable
			return int64(m.HeapAlloc)
		},
		compact:  func(b *testing.B, s Store) { runtime.GC() },
		backlogs: []int{10_000, 100_000},
	})
}
