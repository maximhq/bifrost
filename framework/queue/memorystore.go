package queue

import (
	"context"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Delivery states shared by every backend. Acknowledged deliveries are
// deleted, except on ClickHouse, which writes an acked tombstone.
const (
	statusPending = "pending"
	statusLeased  = "leased"
	statusDead    = "dead"
	// statusWaiting marks a keyed delivery queued behind a live delivery of
	// the same key. It is promoted to pending when that one is acknowledged
	// or dead-lettered, so a claim never has to look past it.
	statusWaiting = "waiting"
)

// memoryStore is an in-process Store. It has the same semantics as the
// database stores and backs the engine tests and single-instance queues.
type memoryStore struct {
	mu       sync.Mutex
	now      func() time.Time
	closed   bool
	seq      int64
	groups   map[string]map[string]bool       // topic -> group -> registered
	messages map[string]*memoryMessage        // message id -> message
	byTopic  map[string][]*memoryMessage      // topic -> messages in publish order
	queues   map[memoryGroupKey][]*memoryItem // (topic, group) -> deliveries in seq order
	items    map[string]*memoryItem           // delivery id -> delivery
	keyLive  map[memoryKey]int                // (topic, group, key) -> pending, leased and waiting deliveries
	waiting  map[memoryKey][]*memoryItem      // (topic, group, key) -> waiting deliveries in seq order
	scanned  atomic.Int64                     // deliveries Claim has examined; lets tests check scan cost
}

type memoryGroupKey struct{ topic, group string }

type memoryKey struct{ topic, group, key string }

type memoryMessage struct {
	msg      Message
	storedAt time.Time
}

type memoryItem struct {
	id            string
	seq           int64
	group         string
	message       *memoryMessage
	status        string
	attempts      int
	nextAttemptAt time.Time
	claimedBy     string
	claimToken    string
	claimedUntil  time.Time
	lastError     string
	createdAt     time.Time
	deadAt        time.Time // when it was dead-lettered; dead retention runs from here
	removed       bool      // acked or purged; dropped from its queue on the next scan
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		now:      time.Now,
		groups:   map[string]map[string]bool{},
		messages: map[string]*memoryMessage{},
		byTopic:  map[string][]*memoryMessage{},
		queues:   map[memoryGroupKey][]*memoryItem{},
		items:    map[string]*memoryItem{},
		keyLive:  map[memoryKey]int{},
		waiting:  map[memoryKey][]*memoryItem{},
	}
}

func (s *memoryStore) EnsureGroup(ctx context.Context, topic, group string, startFrom StartFrom) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.groups[topic] == nil {
		s.groups[topic] = map[string]bool{}
	}
	if s.groups[topic][group] {
		return nil
	}
	s.groups[topic][group] = true
	if startFrom == StartFromEarliest {
		for _, m := range s.byTopic[topic] {
			s.addItemLocked(m, group)
		}
	}
	return nil
}

func (s *memoryStore) Append(ctx context.Context, msgs []*Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	now := s.now()
	for _, in := range msgs {
		if _, exists := s.messages[in.ID]; exists {
			continue
		}
		m := &memoryMessage{msg: *in, storedAt: now}
		m.msg.Payload = append([]byte(nil), in.Payload...)
		m.msg.Headers = maps.Clone(in.Headers)
		s.messages[in.ID] = m
		s.byTopic[in.Topic] = append(s.byTopic[in.Topic], m)
		for group := range s.groups[in.Topic] {
			s.addItemLocked(m, group)
		}
	}
	return nil
}

// addItemLocked creates m's delivery to group unless it already exists.
func (s *memoryStore) addItemLocked(m *memoryMessage, group string) {
	id := DeliveryID(m.msg.ID, group)
	if _, exists := s.items[id]; exists {
		return
	}
	s.seq++
	now := s.now()
	it := &memoryItem{
		id:            id,
		seq:           s.seq,
		group:         group,
		message:       m,
		status:        statusPending,
		nextAttemptAt: dueAt(now, m.msg.DeliverAt),
		createdAt:     now,
	}
	if key := m.msg.Key; key != "" {
		mk := memoryKey{m.msg.Topic, group, key}
		if s.keyLive[mk] > 0 {
			it.status = statusWaiting
			s.waiting[mk] = append(s.waiting[mk], it)
		}
		s.keyLive[mk]++
	}
	s.items[id] = it
	k := memoryGroupKey{m.msg.Topic, group}
	s.queues[k] = append(s.queues[k], it)
}

// dueAt is when a delivery of a message scheduled for deliverAt first
// becomes claimable: deliverAt, or now when unscheduled or already past.
func dueAt(now, deliverAt time.Time) time.Time {
	if deliverAt.After(now) {
		return deliverAt
	}
	return now
}

// finishKeyLocked runs when it stops being live (acknowledged or dead):
// the next waiting delivery of its key, if any, becomes due.
func (s *memoryStore) finishKeyLocked(it *memoryItem) {
	key := it.message.msg.Key
	if key == "" {
		return
	}
	mk := memoryKey{it.message.msg.Topic, it.group, key}
	if s.keyLive[mk]--; s.keyLive[mk] <= 0 {
		delete(s.keyLive, mk)
	}
	if queue := s.waiting[mk]; len(queue) > 0 {
		next := queue[0]
		next.status = statusPending
		next.nextAttemptAt = dueAt(s.now(), next.message.msg.DeliverAt)
		if len(queue) == 1 {
			delete(s.waiting, mk)
		} else {
			s.waiting[mk] = queue[1:]
		}
	}
}

func (s *memoryStore) Claim(ctx context.Context, req ClaimRequest) ([]*Claimed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	now := s.now()
	k := memoryGroupKey{req.Topic, req.Group}
	queue := s.queues[k]
	max := req.Max
	if req.MaxInFlight > 0 {
		inFlight := 0
		for _, it := range queue {
			if !it.removed && it.status == statusLeased && !it.claimedUntil.Before(now) {
				inFlight++
			}
		}
		max = min(max, req.MaxInFlight-inFlight)
	}
	if max <= 0 {
		return nil, nil
	}
	var out []*Claimed
	i := 0
	for ; i < len(queue) && len(out) < max; i++ {
		it := queue[i]
		s.scanned.Add(1)
		if it.removed {
			continue
		}
		// Waiting deliveries are never heads; pending and leased ones always are.
		if it.status == statusDead || it.status == statusWaiting {
			continue
		}
		switch {
		case it.status == statusPending && !it.nextAttemptAt.After(now):
		case it.status == statusLeased && it.claimedUntil.Before(now):
			// The previous holder died without finishing: that attempt is spent.
			it.attempts++
		default:
			continue
		}
		it.status = statusLeased
		it.claimedBy = req.RunnerID
		it.claimToken = uuid.NewString()
		it.claimedUntil = now.Add(req.Lease)
		out = append(out, s.claimedLocked(it))
	}
	// Compact only the scanned prefix, packing its live items against the
	// unscanned tail and reslicing, so the tail never moves and a claim costs
	// what it scanned rather than the whole backlog.
	w := i
	for j := i - 1; j >= 0; j-- {
		if !queue[j].removed {
			w--
			queue[w] = queue[j]
		}
	}
	clear(queue[:w])
	s.queues[k] = queue[w:]
	return out, nil
}

func (s *memoryStore) claimedLocked(it *memoryItem) *Claimed {
	msg := it.message.msg
	msg.Payload = append([]byte(nil), msg.Payload...)
	msg.Headers = maps.Clone(msg.Headers)
	return &Claimed{
		DeliveryID: it.id,
		ClaimToken: it.claimToken,
		Group:      it.group,
		Attempt:    it.attempts + 1,
		Message:    &msg,
	}
}

// heldLocked returns c's delivery when c still holds its lease.
func (s *memoryStore) heldLocked(c *Claimed) *memoryItem {
	it := s.items[c.DeliveryID]
	if it == nil || it.removed || it.status != statusLeased || it.claimToken != c.ClaimToken {
		return nil
	}
	return it
}

func (s *memoryStore) Ack(ctx context.Context, cs []*Claimed) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	held := make([]string, 0, len(cs))
	for _, c := range cs {
		it := s.heldLocked(c)
		if it == nil {
			continue
		}
		it.removed = true
		delete(s.items, it.id)
		s.finishKeyLocked(it)
		held = append(held, it.id)
	}
	return held, nil
}

func (s *memoryStore) Retry(ctx context.Context, c *Claimed, backoff time.Duration, lastErr string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, ErrClosed
	}
	it := s.heldLocked(c)
	if it == nil {
		return false, nil
	}
	it.attempts++
	it.status = statusPending
	it.nextAttemptAt = s.now().Add(backoff)
	it.lastError = lastErr
	it.unlease()
	return true, nil
}

func (s *memoryStore) Kill(ctx context.Context, c *Claimed, lastErr string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, ErrClosed
	}
	it := s.heldLocked(c)
	if it == nil {
		return false, nil
	}
	it.attempts++
	it.status = statusDead
	it.deadAt = s.now()
	it.lastError = lastErr
	it.unlease()
	s.finishKeyLocked(it)
	return true, nil
}

func (s *memoryStore) Extend(ctx context.Context, cs []*Claimed, lease time.Duration) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	now := s.now()
	held := make([]string, 0, len(cs))
	for _, c := range cs {
		// An expired lease is not renewed: another consumer may already be
		// entitled to the delivery.
		if it := s.heldLocked(c); it != nil && !it.claimedUntil.Before(now) {
			it.claimedUntil = now.Add(lease)
			held = append(held, c.DeliveryID)
		}
	}
	return held, nil
}

func (s *memoryStore) Release(ctx context.Context, cs []*Claimed) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	now := s.now()
	for _, c := range cs {
		if it := s.heldLocked(c); it != nil {
			it.status = statusPending
			it.nextAttemptAt = now
			it.unlease()
		}
	}
	return nil
}

func (it *memoryItem) unlease() {
	it.claimedBy = ""
	it.claimToken = ""
	it.claimedUntil = time.Time{}
}

func (s *memoryStore) Purge(ctx context.Context, policy PurgePolicy, batch int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrClosed
	}
	now := s.now()
	var removed int64
	touched := map[memoryGroupKey]bool{}
	for id, it := range s.items {
		if removed >= int64(batch) {
			break
		}
		if it.status == statusDead && it.deadAt.Before(now.Add(-policy.DeadRetention)) {
			it.removed = true
			delete(s.items, id)
			touched[memoryGroupKey{it.message.msg.Topic, it.group}] = true
			removed++
		}
	}
	// Drop purged deliveries from their queues now, not on the group's next
	// claim, so their messages are not kept alive by a group that stopped
	// consuming.
	for k := range touched {
		queue := s.queues[k]
		live := queue[:0]
		for _, it := range queue {
			if !it.removed {
				live = append(live, it)
			}
		}
		clear(queue[len(live):])
		s.queues[k] = live
	}
	// A message is finished with once no delivery refers to it: every group
	// acknowledged it, its dead deliveries were purged, or it had no groups.
	// It is kept AckedRetention past the time it became due.
	referenced := map[string]bool{}
	for _, it := range s.items {
		referenced[it.message.msg.ID] = true
	}
	var dropped int64
	for topic, msgs := range s.byTopic {
		kept := msgs[:0]
		for _, m := range msgs {
			due := m.storedAt
			if m.msg.DeliverAt.After(due) {
				due = m.msg.DeliverAt
			}
			if dropped < int64(batch) && due.Before(now.Add(-policy.AckedRetention)) && !referenced[m.msg.ID] {
				delete(s.messages, m.msg.ID)
				dropped++
				continue
			}
			kept = append(kept, m)
		}
		clear(msgs[len(kept):])
		s.byTopic[topic] = kept
	}
	return removed + dropped, nil
}

func (s *memoryStore) Stats(ctx context.Context, topic, group string) (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Stats{}, ErrClosed
	}
	var st Stats
	for _, it := range s.queues[memoryGroupKey{topic, group}] {
		if it.removed {
			continue
		}
		switch it.status {
		case statusPending, statusWaiting:
			st.Pending++
		case statusLeased:
			st.Leased++
		case statusDead:
			st.Dead++
		}
	}
	return st, nil
}

func (s *memoryStore) Ping(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	return nil
}

func (s *memoryStore) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
