package queue

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"
)

var (
	// errLeaseLost is the cancellation cause of a handler whose delivery was
	// claimed by another consumer after its lease expired.
	errLeaseLost = errors.New("queue: lease lost")
	// errSubscriptionClosed is the cancellation cause of a handler still
	// running when Subscription.Close gives up waiting.
	errSubscriptionClosed = errors.New("queue: subscription closed")
)

// subscription runs one consumer: a claimer that leases due deliveries,
// Concurrency workers that run the handler, and a heartbeat that keeps the
// leases of in-flight deliveries alive.
type subscription struct {
	q       *storeQueue
	topic   string
	group   string
	handler Handler
	opts    SubscribeOptions
	base    context.Context // parent of every handler context; never cancelled

	work        chan *Claimed   // claimed deliveries waiting for a worker
	wake        chan struct{}   // published to this topic, or a worker slot freed
	stop        chan struct{}   // closed by Close; stops claiming
	claimerDone chan struct{}   // closed when the claimer exits
	acks        chan ackRequest // finished deliveries waiting to be acknowledged in a batch
	stopBeat    chan struct{}   // closed once no handler can still be running
	beatCtx     context.Context // parent of heartbeat store calls; cancelled when Close stops waiting
	beatCancel  context.CancelFunc
	beatDone    chan struct{}  // closed when the heartbeat exits
	pool        sync.WaitGroup // workers

	mu       sync.Mutex
	leftover []*Claimed           // claimed while Close was starting; Close releases them
	inflight map[string]*inflight // claim token -> claim, from claim until finished
	current  map[string]string    // delivery id -> token of its newest claim here
	stopping bool

	closeOnce sync.Once
	closeErr  error
}

type inflight struct {
	claim      *Claimed
	started    bool
	lost       bool      // lease taken over; must not start, and is not renewed
	leaseUntil time.Time // last confirmed lease end, on this instance's clock
	cancel     context.CancelCauseFunc
}

func newSubscription(q *storeQueue, topic, group string, h Handler, o SubscribeOptions, base context.Context) *subscription {
	beatCtx, beatCancel := context.WithCancel(base)
	return &subscription{
		beatCtx:     beatCtx,
		beatCancel:  beatCancel,
		q:           q,
		topic:       topic,
		group:       group,
		handler:     h,
		opts:        o,
		base:        base,
		work:        make(chan *Claimed, o.Concurrency),
		wake:        make(chan struct{}, 1),
		stop:        make(chan struct{}),
		claimerDone: make(chan struct{}),
		acks:        make(chan ackRequest, o.Concurrency),
		stopBeat:    make(chan struct{}),
		beatDone:    make(chan struct{}),
		inflight:    map[string]*inflight{},
		current:     map[string]string{},
	}
}

func (s *subscription) start() {
	for range s.opts.Concurrency {
		s.pool.Add(1)
		go s.worker()
	}
	go s.ackLoop()
	go func() {
		// Only workers send acknowledgements; once they are all gone the acker
		// can stop.
		s.pool.Wait()
		close(s.acks)
	}()
	go s.heartbeatLoop()
	go s.claimLoop()
}

// poke wakes the claimer without blocking.
func (s *subscription) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// claimLoop leases deliveries whenever a worker slot is free.
func (s *subscription) claimLoop() {
	defer close(s.claimerDone)
	defer close(s.work)
	ctx, cancel := context.WithCancel(s.base)
	defer cancel()
	go func() {
		select {
		case <-s.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	for {
		want := min(s.opts.BatchSize, s.freeSlots())
		got := 0
		if want > 0 {
			// The lease runs from no later than now, so this bounds it on our clock.
			leaseUntil := time.Now().Add(s.opts.LeaseDuration)
			claimed, err := s.q.store.Claim(ctx, ClaimRequest{
				Topic:       s.topic,
				Group:       s.group,
				RunnerID:    s.q.runnerID,
				Max:         want,
				Lease:       s.opts.LeaseDuration,
				MaxInFlight: s.opts.MaxInFlight,
			})
			if err != nil && ctx.Err() == nil {
				s.q.warn("queue: claim on %s/%s failed: %v", s.topic, s.group, err)
			}
			for _, c := range claimed {
				if !s.track(c, leaseUntil) {
					// Closing raced the claim: Close hands the lease back.
					s.mu.Lock()
					s.leftover = append(s.leftover, c)
					s.mu.Unlock()
					continue
				}
				s.work <- c
				got++
			}
		}
		// A full batch means more is probably due; go again immediately.
		if want > 0 && got == want {
			select {
			case <-s.stop:
				return
			default:
				continue
			}
		}
		timer := time.NewTimer(jitterInterval(s.opts.PollInterval, nil))
		select {
		case <-s.stop:
			timer.Stop()
			return
		case <-s.wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// freeSlots is how many more deliveries this subscription may hold.
func (s *subscription) freeSlots() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts.Concurrency - len(s.inflight)
}

// track records a claim, leased until leaseUntil on this instance's clock, as
// in flight; false means the subscription is closing. Claiming a delivery this
// subscription already holds means the earlier lease expired (a heartbeat
// failed for too long), so the earlier run is marked lost and cancelled
// before the new one is tracked.
func (s *subscription) track(c *Claimed, leaseUntil time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return false
	}
	if prev, ok := s.current[c.DeliveryID]; ok {
		if f := s.inflight[prev]; f != nil {
			s.markLostLocked(f)
		}
	}
	s.inflight[c.ClaimToken] = &inflight{claim: c, leaseUntil: leaseUntil}
	s.current[c.DeliveryID] = c.ClaimToken
	return true
}

// markLostLocked records that f's lease belongs to someone else now.
func (s *subscription) markLostLocked(f *inflight) {
	f.lost = true
	if f.cancel != nil {
		f.cancel(errLeaseLost)
	}
}

func (s *subscription) untrack(c *Claimed) {
	s.mu.Lock()
	delete(s.inflight, c.ClaimToken)
	if s.current[c.DeliveryID] == c.ClaimToken {
		delete(s.current, c.DeliveryID)
	}
	s.mu.Unlock()
	s.poke()
}

func (s *subscription) worker() {
	defer s.pool.Done()
	for c := range s.work {
		ctx, ok := s.begin(c)
		if !ok {
			continue
		}
		err := s.invoke(ctx, c)
		s.finish(c, err)
	}
}

// begin marks c as started and returns its handler context. It returns false
// when c must not run: the subscription is closing (c stays tracked so Close
// releases it), or c's lease was lost while it waited (c is dropped; its new
// holder owns it).
func (s *subscription) begin(c *Claimed) (context.Context, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.inflight[c.ClaimToken]
	if f == nil || s.stopping {
		return nil, false
	}
	if f.lost {
		delete(s.inflight, c.ClaimToken)
		if s.current[c.DeliveryID] == c.ClaimToken {
			delete(s.current, c.DeliveryID)
		}
		s.poke()
		return nil, false
	}
	ctx, cancel := context.WithCancelCause(s.base)
	f.started = true
	f.cancel = cancel
	return ctx, true
}

// invoke runs the handler, turning a panic into a retryable error. A
// delivery past MaxAttempts (its earlier holders died mid-attempt) is not run
// at all.
func (s *subscription) invoke(ctx context.Context, c *Claimed) (err error) {
	if c.Attempt > s.opts.MaxAttempts {
		return fmt.Errorf("queue: attempt %d exceeds max attempts %d after lost leases", c.Attempt, s.opts.MaxAttempts)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("queue: handler panic: %v\n%s", r, debug.Stack())
		}
	}()
	return s.handler(ctx, s.delivery(c))
}

func (s *subscription) delivery(c *Claimed) *Delivery {
	return &Delivery{
		Message:    *c.Message,
		DeliveryID: c.DeliveryID,
		Group:      c.Group,
		Attempt:    c.Attempt,
	}
}

// finish records the handler's outcome. Store writes are fenced on the claim
// token, so if the lease was lost meanwhile they change nothing.
func (s *subscription) finish(c *Claimed, handlerErr error) {
	defer s.untrack(c)
	s.mu.Lock()
	lost := false
	if f := s.inflight[c.ClaimToken]; f != nil {
		lost = f.lost
		if f.cancel != nil {
			f.cancel(nil)
		}
	}
	s.mu.Unlock()
	if lost {
		// Another claim owns the delivery now; any outcome from this run,
		// a dead-letter copy above all, would act on work it no longer owns.
		s.q.debug("queue: dropping the outcome of delivery %s on %s/%s: its lease was lost", c.DeliveryID, s.topic, s.group)
		return
	}

	ctx, cancel := context.WithTimeout(s.base, storeOpTimeout)
	defer cancel()

	var held bool
	var err error
	op := "ack"
	switch {
	case handlerErr == nil:
		done := make(chan ackResult, 1)
		s.acks <- ackRequest{claim: c, done: done}
		res := <-done
		held, err = res.held, res.err
	case IsPermanent(handlerErr) || c.Attempt >= s.opts.MaxAttempts:
		op = "dead-letter"
		held, err = s.deadLetter(ctx, c, handlerErr)
	default:
		op = "retry"
		backoff := s.opts.Backoff(c.Attempt, nil)
		err = s.q.withRetry(ctx, func(ctx context.Context) error {
			held, err = s.q.store.Retry(ctx, c, backoff, truncateError(handlerErr.Error()))
			return err
		})
	}
	switch {
	case err != nil:
		s.q.warn("queue: %s of delivery %s on %s/%s failed: %v", op, c.DeliveryID, s.topic, s.group, err)
	case !held:
		s.q.warn("queue: lease lost before %s of delivery %s on %s/%s; it will be redelivered", op, c.DeliveryID, s.topic, s.group)
	}
}

// deadLetter publishes the dead-letter copy first, then marks the delivery
// dead. The copy has a deterministic ID, so a crash between the two steps
// redelivers and republishes without duplicating it. If the copy cannot be
// published the delivery is retried rather than lost.
func (s *subscription) deadLetter(ctx context.Context, c *Claimed, cause error) (bool, error) {
	lastErr := ""
	if cause != nil {
		lastErr = truncateError(cause.Error())
	}
	if s.opts.DeadLetterTopic != "" {
		// Renewing the lease proves this claim still owns the delivery and
		// keeps it owned while the copy is published: a copy must never be
		// written for work another consumer has taken over.
		held, err := s.q.store.Extend(ctx, []*Claimed{c}, s.opts.LeaseDuration)
		if err != nil {
			return false, fmt.Errorf("confirm ownership before dead-lettering: %w", err)
		}
		if len(held) != 1 {
			return false, nil
		}
		if err := s.q.publish(ctx, []*Message{deadLetterCopy(s.opts.DeadLetterTopic, c, cause)}); err != nil {
			backoff := s.opts.Backoff(c.Attempt, nil)
			_, retryErr := s.q.store.Retry(ctx, c, backoff, lastErr)
			return false, errors.Join(fmt.Errorf("publish to dead letter topic %s: %w", s.opts.DeadLetterTopic, err), retryErr)
		}
	}
	var held bool
	err := s.q.withRetry(ctx, func(ctx context.Context) error {
		var err error
		held, err = s.q.store.Kill(ctx, c, lastErr)
		return err
	})
	return held, err
}

// heartbeatLoop renews the leases of everything in flight and cancels the
// handlers whose deliveries were taken over.
func (s *subscription) heartbeatLoop() {
	defer close(s.beatDone)
	ticker := time.NewTicker(s.opts.LeaseDuration / 3)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopBeat:
			return
		case <-ticker.C:
		}
		s.heartbeat()
	}
}

func (s *subscription) heartbeat() {
	s.mu.Lock()
	claims := make([]*Claimed, 0, len(s.inflight))
	for _, f := range s.inflight {
		if !f.lost {
			claims = append(claims, f.claim)
		}
	}
	s.mu.Unlock()
	if len(claims) == 0 {
		return
	}

	// A renewed lease runs from no later than now, on our clock.
	renewedUntil := time.Now().Add(s.opts.LeaseDuration)
	ctx, cancel := context.WithTimeout(s.beatCtx, s.opts.LeaseDuration/3)
	defer cancel()
	held, err := s.q.store.Extend(ctx, claims, s.opts.LeaseDuration)
	if err != nil {
		s.q.warn("queue: lease heartbeat on %s/%s failed: %v", s.topic, s.group, err)
	}
	stillHeld := make(map[string]struct{}, len(held))
	for _, id := range held {
		stillHeld[id] = struct{}{}
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range claims {
		f := s.inflight[c.ClaimToken]
		if f == nil || f.lost {
			continue // finished or superseded meanwhile
		}
		switch _, ok := stillHeld[c.DeliveryID]; {
		case ok:
			f.leaseUntil = renewedUntil
		case err == nil:
			// The store answered and no longer has this lease.
			s.q.debug("queue: lease lost for delivery %s on %s/%s", c.DeliveryID, s.topic, s.group)
			s.markLostLocked(f)
		case f.leaseUntil.Before(now):
			// Renewals keep failing and the last confirmed lease has run out:
			// another instance may own the delivery now, whether or not this
			// one has a free slot to notice by re-claiming it.
			s.q.debug("queue: lease lapsed for delivery %s on %s/%s", c.DeliveryID, s.topic, s.group)
			s.markLostLocked(f)
		}
	}
}

// Close stops claiming, waits for running handlers until ctx is done, and
// releases every lease that was claimed but never started. Leases of running
// handlers keep being renewed while Close waits. Handlers still running when
// ctx ends are cancelled; their leases are left to expire rather than
// released, so no other consumer starts the same delivery while they may
// still be running.
func (s *subscription) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.stopping = true
		s.mu.Unlock()
		close(s.stop)
		<-s.claimerDone // work is closed; workers drain it without starting anything new

		drained := make(chan struct{})
		go func() {
			s.pool.Wait()
			close(drained)
		}()
		select {
		case <-drained:
		case <-ctx.Done():
			s.mu.Lock()
			for _, f := range s.inflight {
				if f.started && f.cancel != nil {
					f.cancel(errSubscriptionClosed)
				}
			}
			s.mu.Unlock()
		}

		// Nothing runs any more (or Close gave up waiting): an in-flight
		// renewal must not hold Close past its deadline.
		s.beatCancel()
		close(s.stopBeat)
		<-s.beatDone

		s.mu.Lock()
		unstarted := s.leftover
		s.leftover = nil
		for token, f := range s.inflight {
			if !f.started {
				if !f.lost {
					unstarted = append(unstarted, f.claim)
				}
				delete(s.inflight, token)
			}
		}
		s.mu.Unlock()
		s.closeErr = s.releaseNow(ctx, unstarted)
		s.q.removeSubscription(s)
	})
	return s.closeErr
}

// releaseNow gives leases back so another consumer can claim them at once.
// It stays within ctx's deadline: a lease it cannot hand back in time simply
// expires.
func (s *subscription) releaseNow(ctx context.Context, cs []*Claimed) error {
	if len(cs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, storeOpTimeout)
	defer cancel()
	if err := s.q.store.Release(ctx, cs); err != nil {
		return fmt.Errorf("queue: release %d leases on %s/%s: %w", len(cs), s.topic, s.group, err)
	}
	return nil
}

type ackRequest struct {
	claim *Claimed
	done  chan ackResult // buffered; receives this delivery's outcome
}

type ackResult struct {
	held bool
	err  error
}

// ackLoop acknowledges finished deliveries in batches. It takes whatever is
// queued when it wakes, up to maxAckBatch, and writes it in one store call:
// an idle queue acknowledges at once, a busy one shares each round trip
// between every handler that finished meanwhile.
func (s *subscription) ackLoop() {
	for first := range s.acks {
		batch := []ackRequest{first}
	drain:
		for len(batch) < maxAckBatch {
			select {
			case r, ok := <-s.acks:
				if !ok {
					break drain
				}
				batch = append(batch, r)
			default:
				break drain
			}
		}
		s.flushAcks(batch)
	}
}

func (s *subscription) flushAcks(batch []ackRequest) {
	cs := make([]*Claimed, len(batch))
	for i, r := range batch {
		cs[i] = r.claim
	}
	ctx, cancel := context.WithTimeout(s.base, storeOpTimeout)
	defer cancel()
	var held []string
	err := s.q.withRetry(ctx, func(ctx context.Context) error {
		var err error
		held, err = s.q.store.Ack(ctx, cs)
		return err
	})
	ok := make(map[string]bool, len(held))
	for _, id := range held {
		ok[id] = true
	}
	for _, r := range batch {
		r.done <- ackResult{held: ok[r.claim.DeliveryID], err: err}
	}
}
