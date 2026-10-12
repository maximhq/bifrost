package queue

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

const (
	// storeOpTimeout bounds each store call the engine makes on its own
	// behalf, such as acknowledging a finished delivery.
	storeOpTimeout = 10 * time.Second
	// storeOpRetries is how many times a transient store error is retried.
	storeOpRetries = 3
	// purgeBatch is the number of rows one Purge call may remove.
	purgeBatch = 1000
	// maxPurgeBatchesPerPass stops one janitor pass from monopolising the store.
	maxPurgeBatchesPerPass = 50
	// maxAckBatch bounds how many acknowledgements share one store call.
	maxAckBatch = 256
)

// Headers added to the copy of a dead delivery published to a dead-letter
// topic.
const (
	HeaderDeadLetterSourceTopic = "x-queue-dlq-source-topic"
	HeaderDeadLetterGroup       = "x-queue-dlq-group"
	HeaderDeadLetterDeliveryID  = "x-queue-dlq-delivery-id"
	HeaderDeadLetterMessageID   = "x-queue-dlq-message-id"
	HeaderDeadLetterAttempts    = "x-queue-dlq-attempts"
	HeaderDeadLetterError       = "x-queue-dlq-error"
)

// ErrDeadLetterLoop is returned by Subscribe when the dead-letter topic is the
// subscribed topic: every dead-lettered copy would be consumed by the same
// subscription, fail again, and be dead-lettered again, forever.
var ErrDeadLetterLoop = errors.New("queue: dead letter topic is the subscribed topic")

// storeQueue is the generic Queue engine over a Store.
type storeQueue struct {
	store    Store
	logger   schemas.Logger
	runnerID string
	cfg      EngineConfig

	mu     sync.Mutex
	closed bool
	subs   map[*subscription]struct{}

	janitorStop      chan struct{}
	janitorCtx       context.Context // parent of janitor store calls; cancelled by Close
	janitorStopCalls context.CancelFunc
	janitorDone      chan struct{}
	stopListen       context.CancelFunc // ends the store's WakeSource, if any
	listenDone       chan struct{}
	closeOnce        sync.Once
	closeErr         error
}

// NewStoreQueue builds a Queue on top of store. It takes ownership of store
// and closes it from Queue.Close. External Store implementations use this to
// get the full engine without reimplementing it.
func NewStoreQueue(store Store, cfg EngineConfig, logger schemas.Logger) (Queue, error) {
	if store == nil {
		return nil, errors.New("queue: nil store")
	}
	cfg = cfg.WithDefaults()
	janitorEvery, err := cfg.Janitor()
	if err != nil {
		return nil, err
	}
	q := &storeQueue{
		store:       store,
		logger:      logger,
		runnerID:    newRunnerID(),
		cfg:         cfg,
		subs:        map[*subscription]struct{}{},
		janitorStop: make(chan struct{}),
		janitorDone: make(chan struct{}),
		listenDone:  make(chan struct{}),
	}
	q.janitorCtx, q.janitorStopCalls = context.WithCancel(context.Background())
	go q.runJanitor(janitorEvery)
	q.startListening()
	return q, nil
}

// startListening wakes local subscriptions on publishes other instances
// make, when the store can report them.
func (q *storeQueue) startListening() {
	ws, ok := q.store.(WakeSource)
	if !ok {
		close(q.listenDone)
		q.stopListen = func() {}
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	q.stopListen = cancel
	go func() {
		defer close(q.listenDone)
		if err := ws.Listen(ctx, q.wakeTopic); err != nil && !errors.Is(err, ErrUnsupported) {
			q.warn("queue: cross-instance wakeups stopped: %v", err)
		}
	}()
}

// newRunnerID identifies this process in claimed_by columns.
func newRunnerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return host + "-" + uuid.NewString()[:8]
}

func (q *storeQueue) Publish(ctx context.Context, msgs ...*Message) error {
	if len(msgs) == 0 {
		return nil
	}
	if q.isClosed() {
		return ErrClosed
	}
	return q.publish(ctx, msgs)
}

// publish stores msgs without the closed check, so subscriptions draining
// during Close can still publish dead-letter copies.
func (q *storeQueue) publish(ctx context.Context, msgs []*Message) error {
	now := time.Now().UTC()
	for _, m := range msgs {
		if err := validateMessage(m, messageLimits{maxPayload: q.cfg.MaxPayloadBytes, maxDelay: q.cfg.Retention(), now: now}); err != nil {
			return err
		}
		if m.ID == "" {
			m.ID = uuid.NewString()
		}
		if m.PublishedAt.IsZero() {
			m.PublishedAt = now
		}
	}
	if err := q.withRetry(ctx, func(ctx context.Context) error { return q.store.Append(ctx, msgs) }); err != nil {
		return fmt.Errorf("queue: publish: %w", err)
	}
	q.wakeTopics(msgs)
	return nil
}

// wakeTopics nudges local subscriptions of the published topics so they claim
// now instead of waiting for their next poll.
func (q *storeQueue) wakeTopics(msgs []*Message) {
	topics := make(map[string]struct{}, 1)
	for _, m := range msgs {
		topics[m.Topic] = struct{}{}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for s := range q.subs {
		if _, ok := topics[s.topic]; ok {
			s.poke()
		}
	}
}

// wakeTopic nudges local subscriptions of topic.
func (q *storeQueue) wakeTopic(topic string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for s := range q.subs {
		if s.topic == topic {
			s.poke()
		}
	}
}

func (q *storeQueue) Subscribe(ctx context.Context, topic, group string, h Handler, opts ...SubscribeOption) (Subscription, error) {
	if err := ValidateName("topic", topic); err != nil {
		return nil, err
	}
	if err := ValidateName("group", group); err != nil {
		return nil, err
	}
	if h == nil {
		return nil, errors.New("queue: nil handler")
	}
	o, err := NewSubscribeOptions(opts...)
	if err != nil {
		return nil, err
	}
	if o.DeadLetterTopic == topic {
		return nil, fmt.Errorf("%w: %s", ErrDeadLetterLoop, topic)
	}
	if q.isClosed() {
		return nil, ErrClosed
	}
	if c, ok := q.store.(ConsumeChecker); ok {
		if err := c.CanConsume(); err != nil {
			return nil, err
		}
	}
	if err := q.withRetry(ctx, func(ctx context.Context) error { return q.store.EnsureGroup(ctx, topic, group, o.StartFrom) }); err != nil {
		return nil, fmt.Errorf("queue: register group %s on %s: %w", group, topic, err)
	}

	s := newSubscription(q, topic, group, h, o, context.WithoutCancel(ctx))
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil, ErrClosed
	}
	q.subs[s] = struct{}{}
	q.mu.Unlock()
	s.start()
	return s, nil
}

func (q *storeQueue) Stats(ctx context.Context, topic, group string) (Stats, error) {
	if q.isClosed() {
		return Stats{}, ErrClosed
	}
	return q.store.Stats(ctx, topic, group)
}

func (q *storeQueue) Ping(ctx context.Context) error {
	if q.isClosed() {
		return ErrClosed
	}
	return q.store.Ping(ctx)
}

func (q *storeQueue) Close(ctx context.Context) error {
	q.closeOnce.Do(func() {
		q.mu.Lock()
		q.closed = true
		subs := make([]*subscription, 0, len(q.subs))
		for s := range q.subs {
			subs = append(subs, s)
		}
		q.mu.Unlock()

		var errs []error
		var wg sync.WaitGroup
		var errMu sync.Mutex
		for _, s := range subs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.Close(ctx); err != nil {
					errMu.Lock()
					errs = append(errs, err)
					errMu.Unlock()
				}
			}()
		}
		wg.Wait()

		q.janitorStopCalls() // an in-flight purge must not hold Close
		close(q.janitorStop)
		<-q.janitorDone
		q.stopListen()
		select {
		case <-q.listenDone:
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("queue: wakeup listener did not stop: %w", ctx.Err()))
		}
		if err := q.store.Close(ctx); err != nil {
			errs = append(errs, err)
		}
		q.closeErr = errors.Join(errs...)
	})
	return q.closeErr
}

func (q *storeQueue) isClosed() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.closed
}

func (q *storeQueue) removeSubscription(s *subscription) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.subs, s)
}

// runJanitor purges dead deliveries and unconsumed messages past retention.
func (q *storeQueue) runJanitor(every time.Duration) {
	defer close(q.janitorDone)
	timer := time.NewTimer(jitterInterval(every, nil))
	defer timer.Stop()
	for {
		select {
		case <-q.janitorStop:
			return
		case <-timer.C:
		}
		q.purgeOnce()
		timer.Reset(jitterInterval(every, nil))
	}
}

// purgeOnce runs one janitor pass in bounded batches.
func (q *storeQueue) purgeOnce() {
	policy := q.cfg.PurgePolicy()
	for range maxPurgeBatchesPerPass {
		select {
		case <-q.janitorStop:
			return
		default:
		}
		ctx, cancel := context.WithTimeout(q.janitorCtx, storeOpTimeout)
		n, err := q.store.Purge(ctx, policy, purgeBatch)
		cancel()
		if err != nil {
			q.warn("queue: purge failed: %v", err)
			return
		}
		if n < purgeBatch {
			return
		}
	}
}

// withRetry runs op, retrying transient store errors with a short backoff.
func (q *storeQueue) withRetry(ctx context.Context, op func(context.Context) error) error {
	var err error
	for attempt := range storeOpRetries + 1 {
		if err = op(ctx); err == nil || !logstore.IsTransientWriteError(err) || attempt == storeOpRetries {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		case <-time.After(time.Duration(100<<attempt) * time.Millisecond):
		}
	}
	return err
}

// deadLetterCopy builds the message published to the dead-letter topic for a
// delivery that exhausted its attempts. Its ID is deterministic, so
// republishing it after a crash is a no-op.
func deadLetterCopy(topic string, c *Claimed, cause error) *Message {
	headers := maps.Clone(c.Message.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	headers[HeaderDeadLetterSourceTopic] = c.Message.Topic
	headers[HeaderDeadLetterGroup] = c.Group
	headers[HeaderDeadLetterDeliveryID] = c.DeliveryID
	headers[HeaderDeadLetterMessageID] = c.Message.ID
	headers[HeaderDeadLetterAttempts] = fmt.Sprint(c.Attempt)
	if cause != nil {
		headers[HeaderDeadLetterError] = truncateError(cause.Error())
	}
	return &Message{
		ID:      deadLetterMessageID(c.DeliveryID),
		Topic:   topic,
		Key:     c.Message.Key,
		Payload: c.Message.Payload,
		Headers: headers,
	}
}

// maxErrorLength caps stored error text so a huge error cannot bloat rows.
const maxErrorLength = 2048

// truncateError cuts s to at most maxErrorLength bytes without splitting a
// UTF-8 character, which a store or header may reject.
func truncateError(s string) string {
	if len(s) <= maxErrorLength {
		return s
	}
	cut := maxErrorLength
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func (q *storeQueue) warn(msg string, args ...any) {
	if q.logger != nil {
		q.logger.Warn(msg, args...)
	}
}

func (q *storeQueue) debug(msg string, args ...any) {
	if q.logger != nil {
		q.logger.Debug(msg, args...)
	}
}

func init() {
	Register(QueueTypeMemory, func(ctx context.Context, cfg *Config, deps Dependencies, logger schemas.Logger) (Queue, error) {
		var engine EngineConfig
		switch c := cfg.Config.(type) {
		case nil:
		case *MemoryQueueConfig:
			if c != nil {
				engine = c.EngineConfig
			}
		default:
			return nil, fmt.Errorf("invalid memory queue config: %T", cfg.Config)
		}
		if logger != nil {
			logger.Warn("queue: the memory queue is single-process only; messages do not survive a restart")
		}
		return NewStoreQueue(newMemoryStore(), engine, logger)
	})
}
