package queue

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contractLease is short so lease-expiry cases stay fast; contractWait
// comfortably outlasts it, including on a database clock.
const (
	contractLease = 400 * time.Millisecond
	contractWait  = 700 * time.Millisecond
	longLease     = time.Minute
)

// storeFactory returns a fresh, empty Store. It registers its own cleanup.
type storeFactory func(t *testing.T) Store

// runStoreContract checks the semantics every Store backend must share. Each
// backend's test calls it with its own factory.
func runStoreContract(t *testing.T, newStore storeFactory) {
	cases := []struct {
		name string
		fn   func(t *testing.T, s Store)
	}{
		{"FanOutToEveryGroup", contractFanOut},
		{"LatestSkipsEarlierMessages", contractStartFrom},
		{"AppendIsIdempotent", contractAppendIdempotent},
		{"ConcurrentClaimersAreExclusive", contractExclusiveClaims},
		{"ExpiredLeaseIsReclaimedAndFenced", contractLeaseExpiry},
		{"RetryHonoursBackoff", contractRetry},
		{"KillDeadLetters", contractKill},
		{"ReleaseKeepsAttempts", contractRelease},
		{"PerKeyFIFO", contractPerKeyFIFO},
		{"ExtendReportsHeldLeases", contractExtend},
		{"Purge", contractPurge},
		{"Stats", contractStats},
		{"PayloadsAndHeadersRoundTrip", contractPayloadsRoundTrip},
		{"EarliestBackfillKeepsPublishOrder", contractBackfillOrder},
		{"LargeBatchesCrossChunkBoundaries", contractLargeBatches},
		{"MaxLengthIdentifiers", contractMaxLengthIdentifiers},
		{"MultiTopicAppend", contractMultiTopicAppend},
		{"AckBatchReportsHeld", contractAckBatch},
		{"WaitingDeliveriesStayBehindTheirHead", contractWaitingBehindHead},
		{"AckedMessagesKeptForAckedRetention", contractAckedRetention},
		{"MaxInFlightCapsLeases", contractMaxInFlight},
		{"ScheduledDeliveryWaits", contractScheduled},
		{"RetentionFromDeathAndDueTime", contractRetentionFromDeathAndDueTime},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(t, newStore(t))
		})
	}
}

func TestMemoryStoreContract(t *testing.T) {
	runStoreContract(t, func(t *testing.T) Store {
		s := newMemoryStore()
		t.Cleanup(func() { _ = s.Close(context.Background()) })
		return s
	})
}

// ackOne acknowledges a single delivery and reports whether it was held.
func ackOne(ctx context.Context, s Store, c *Claimed) (bool, error) {
	held, err := s.Ack(ctx, []*Claimed{c})
	return len(held) == 1 && held[0] == c.DeliveryID, err
}

func newMsg(topic, key, payload string) *Message {
	return &Message{
		ID:          uuid.NewString(),
		Topic:       topic,
		Key:         key,
		Payload:     []byte(payload),
		Headers:     map[string]string{"p": payload},
		PublishedAt: time.Now().UTC(),
	}
}

func uniqueTopic(t *testing.T) string {
	return "t" + uuid.NewString()[:8]
}

func payloads(cs []*Claimed) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c.Message.Payload)
	}
	return out
}

func mustClaim(t *testing.T, s Store, topic, group string, max int, lease time.Duration) []*Claimed {
	t.Helper()
	cs, err := s.Claim(context.Background(), ClaimRequest{Topic: topic, Group: group, RunnerID: "runner-" + group, Max: max, Lease: lease})
	require.NoError(t, err)
	return cs
}

func mustAppend(t *testing.T, s Store, msgs ...*Message) {
	t.Helper()
	require.NoError(t, s.Append(context.Background(), msgs))
}

func mustGroup(t *testing.T, s Store, topic, group string, from StartFrom) {
	t.Helper()
	require.NoError(t, s.EnsureGroup(context.Background(), topic, group, from))
}

func contractFanOut(t *testing.T, s Store) {
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g1", StartFromLatest)
	mustGroup(t, s, topic, "g2", StartFromLatest)
	m1, m2 := newMsg(topic, "", "a"), newMsg(topic, "k", "b")
	mustAppend(t, s, m1, m2)
	mustAppend(t, s, newMsg("other"+topic, "", "elsewhere"))

	for _, g := range []string{"g1", "g2"} {
		cs := mustClaim(t, s, topic, g, 10, longLease)
		require.Len(t, cs, 2, g)
		assert.Equal(t, []string{"a", "b"}, payloads(cs), g)
		for i, want := range []*Message{m1, m2} {
			c := cs[i]
			assert.Equal(t, DeliveryID(want.ID, g), c.DeliveryID)
			assert.Equal(t, g, c.Group)
			assert.Equal(t, 1, c.Attempt)
			assert.NotEmpty(t, c.ClaimToken)
			assert.Equal(t, want.ID, c.Message.ID)
			assert.Equal(t, want.Topic, c.Message.Topic)
			assert.Equal(t, want.Key, c.Message.Key)
			assert.Equal(t, want.Headers, c.Message.Headers)
			assert.WithinDuration(t, want.PublishedAt, c.Message.PublishedAt, time.Millisecond)
		}
		assert.Empty(t, mustClaim(t, s, topic, g, 10, longLease), "nothing left for %s", g)
	}
}

func contractStartFrom(t *testing.T, s Store) {
	topic := uniqueTopic(t)
	early := newMsg(topic, "", "early")
	mustAppend(t, s, early)

	mustGroup(t, s, topic, "latest", StartFromLatest)
	mustGroup(t, s, topic, "earliest", StartFromEarliest)
	assert.Empty(t, mustClaim(t, s, topic, "latest", 10, longLease))
	assert.Equal(t, []string{"early"}, payloads(mustClaim(t, s, topic, "earliest", 10, longLease)))

	// Re-registering an existing group never backfills again.
	mustGroup(t, s, topic, "latest", StartFromEarliest)
	assert.Empty(t, mustClaim(t, s, topic, "latest", 10, longLease))

	late := newMsg(topic, "", "late")
	mustAppend(t, s, late)
	assert.Equal(t, []string{"late"}, payloads(mustClaim(t, s, topic, "latest", 10, longLease)))
	assert.Equal(t, []string{"late"}, payloads(mustClaim(t, s, topic, "earliest", 10, longLease)))
}

func contractAppendIdempotent(t *testing.T, s Store) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	m := newMsg(topic, "", "once")
	// A duplicate inside one batch is stored once.
	mustAppend(t, s, m, m)

	cs := mustClaim(t, s, topic, "g", 10, longLease)
	require.Len(t, cs, 1)
	held, err := ackOne(ctx, s, cs[0])
	require.NoError(t, err)
	require.True(t, held)

	// A producer retry after the delivery completed must not reopen it.
	mustAppend(t, s, m)
	assert.Empty(t, mustClaim(t, s, topic, "g", 10, longLease))

	// A retry while the delivery is leased must not reset the lease.
	m2 := newMsg(topic, "", "leased")
	mustAppend(t, s, m2)
	cs = mustClaim(t, s, topic, "g", 10, longLease)
	require.Len(t, cs, 1)
	mustAppend(t, s, m2)
	assert.Empty(t, mustClaim(t, s, topic, "g", 10, longLease))
	held, err = ackOne(ctx, s, cs[0])
	require.NoError(t, err)
	assert.True(t, held)
}

func contractExclusiveClaims(t *testing.T, s Store) {
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	const total = 120
	msgs := make([]*Message, total)
	for i := range msgs {
		msgs[i] = newMsg(topic, "", fmt.Sprint(i))
	}
	mustAppend(t, s, msgs...)

	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				cs, err := s.Claim(context.Background(), ClaimRequest{Topic: topic, Group: "g", RunnerID: fmt.Sprintf("w%d", w), Max: 7, Lease: longLease})
				if !assert.NoError(t, err) || len(cs) == 0 {
					return
				}
				mu.Lock()
				for _, c := range cs {
					seen[c.DeliveryID]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Len(t, seen, total)
	for id, n := range seen {
		assert.Equal(t, 1, n, "delivery %s claimed %d times", id, n)
	}
}

func contractLeaseExpiry(t *testing.T, s Store) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	mustAppend(t, s, newMsg(topic, "", "x"))

	first := mustClaim(t, s, topic, "g", 1, contractLease)
	require.Len(t, first, 1)
	assert.Empty(t, mustClaim(t, s, topic, "g", 1, contractLease), "a live lease is not claimable")

	time.Sleep(contractWait)
	second := mustClaim(t, s, topic, "g", 1, longLease)
	require.Len(t, second, 1)
	assert.Equal(t, first[0].DeliveryID, second[0].DeliveryID)
	assert.NotEqual(t, first[0].ClaimToken, second[0].ClaimToken)
	assert.Equal(t, 2, second[0].Attempt, "an expired lease counts as a spent attempt")

	// Every write under the stale token is a no-op.
	held, err := ackOne(ctx, s, first[0])
	require.NoError(t, err)
	assert.False(t, held)
	held, err = s.Retry(ctx, first[0], 0, "stale")
	require.NoError(t, err)
	assert.False(t, held)
	held, err = s.Kill(ctx, first[0], "stale")
	require.NoError(t, err)
	assert.False(t, held)
	ids, err := s.Extend(ctx, first, longLease)
	require.NoError(t, err)
	assert.Empty(t, ids)
	require.NoError(t, s.Release(ctx, first))
	assert.Empty(t, mustClaim(t, s, topic, "g", 1, longLease), "stale release must not free the new lease")

	held, err = ackOne(ctx, s, second[0])
	require.NoError(t, err)
	assert.True(t, held)
}

func contractRetry(t *testing.T, s Store) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	mustAppend(t, s, newMsg(topic, "", "r"))

	cs := mustClaim(t, s, topic, "g", 1, longLease)
	require.Len(t, cs, 1)
	held, err := s.Retry(ctx, cs[0], contractLease, "boom")
	require.NoError(t, err)
	require.True(t, held)

	assert.Empty(t, mustClaim(t, s, topic, "g", 1, longLease), "not due during backoff")
	time.Sleep(contractWait)
	again := mustClaim(t, s, topic, "g", 1, longLease)
	require.Len(t, again, 1)
	assert.Equal(t, 2, again[0].Attempt)
}

func contractKill(t *testing.T, s Store) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	mustAppend(t, s, newMsg(topic, "", "dead"))

	cs := mustClaim(t, s, topic, "g", 1, longLease)
	require.Len(t, cs, 1)
	forged := *cs[0]
	forged.ClaimToken = uuid.NewString()
	held, err := s.Kill(ctx, &forged, "forged")
	require.NoError(t, err)
	assert.False(t, held, "a wrong token cannot kill")

	held, err = s.Kill(ctx, cs[0], "boom")
	require.NoError(t, err)
	require.True(t, held)
	assert.Empty(t, mustClaim(t, s, topic, "g", 1, longLease))

	st, err := s.Stats(ctx, topic, "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{Dead: 1}, st)
}

func contractRelease(t *testing.T, s Store) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	mustAppend(t, s, newMsg(topic, "", "rel"))

	cs := mustClaim(t, s, topic, "g", 1, longLease)
	require.Len(t, cs, 1)
	require.NoError(t, s.Release(ctx, cs))
	again := mustClaim(t, s, topic, "g", 1, longLease)
	require.Len(t, again, 1, "released deliveries are claimable at once")
	assert.Equal(t, 1, again[0].Attempt, "release does not spend an attempt")
}

func contractPerKeyFIFO(t *testing.T, s Store) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	for _, m := range []*Message{
		newMsg(topic, "k1", "a"), newMsg(topic, "k1", "b"), newMsg(topic, "k2", "c"),
		newMsg(topic, "", "d"), newMsg(topic, "", "e"), newMsg(topic, "k3", "x"), newMsg(topic, "k3", "y"),
	} {
		// Separate appends give each message its own, strictly later position.
		mustAppend(t, s, m)
	}

	first := mustClaim(t, s, topic, "g", 10, longLease)
	assert.Equal(t, []string{"a", "c", "d", "e", "x"}, payloads(first), "only key heads and unkeyed messages")
	byPayload := map[string]*Claimed{}
	for _, c := range first {
		byPayload[string(c.Message.Payload)] = c
	}

	// A head in retry backoff still blocks its key.
	held, err := s.Retry(ctx, byPayload["a"], contractLease, "boom")
	require.NoError(t, err)
	require.True(t, held)
	assert.Empty(t, mustClaim(t, s, topic, "g", 10, longLease), "b waits behind a's backoff")

	// A dead head unblocks its key.
	held, err = s.Kill(ctx, byPayload["x"], "boom")
	require.NoError(t, err)
	require.True(t, held)
	assert.Equal(t, []string{"y"}, payloads(mustClaim(t, s, topic, "g", 10, longLease)))

	time.Sleep(contractWait)
	retried := mustClaim(t, s, topic, "g", 10, longLease)
	require.Equal(t, []string{"a"}, payloads(retried), "a comes back before b")
	held, err = ackOne(ctx, s, retried[0])
	require.NoError(t, err)
	require.True(t, held)
	assert.Equal(t, []string{"b"}, payloads(mustClaim(t, s, topic, "g", 10, longLease)))
}

func contractExtend(t *testing.T, s Store) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	mustAppend(t, s, newMsg(topic, "", "1"), newMsg(topic, "", "2"))

	cs := mustClaim(t, s, topic, "g", 2, contractLease)
	require.Len(t, cs, 2)
	held, err := s.Extend(ctx, cs, longLease)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{cs[0].DeliveryID, cs[1].DeliveryID}, held)

	// Extended past the original lease: still not claimable.
	time.Sleep(contractWait)
	assert.Empty(t, mustClaim(t, s, topic, "g", 2, longLease))

	ok, err := ackOne(ctx, s, cs[0])
	require.NoError(t, err)
	require.True(t, ok)
	held, err = s.Extend(ctx, cs, longLease)
	require.NoError(t, err)
	assert.Equal(t, []string{cs[1].DeliveryID}, held)

	held, err = s.Extend(ctx, nil, longLease)
	require.NoError(t, err)
	assert.Empty(t, held)
}

func contractPurge(t *testing.T, s Store) {
	ctx := context.Background()
	// Earlier cases share the store; drain what they left that is purgeable
	// at any age, such as ClickHouse's acked tombstones.
	for range 20 {
		if n, err := s.Purge(ctx, PurgePolicy{DeadRetention: time.Hour, AckedRetention: time.Hour}, 1000); err != nil || n == 0 {
			require.NoError(t, err)
			break
		}
	}
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	dead, pending := newMsg(topic, "", "dead"), newMsg(topic, "", "pending")
	mustAppend(t, s, dead)
	cs := mustClaim(t, s, topic, "g", 1, longLease)
	require.Len(t, cs, 1)
	held, err := s.Kill(ctx, cs[0], "boom")
	require.NoError(t, err)
	require.True(t, held)
	mustAppend(t, s, pending)
	mustAppend(t, s, newMsg("nogroups"+topic, "", "orphan"))

	// Nothing is old enough yet.
	n, err := s.Purge(ctx, PurgePolicy{DeadRetention: time.Hour, AckedRetention: time.Hour}, 100)
	require.NoError(t, err)
	assert.Zero(t, n)

	time.Sleep(50 * time.Millisecond)
	_, err = s.Purge(ctx, PurgePolicy{DeadRetention: time.Millisecond, AckedRetention: time.Millisecond}, 100)
	require.NoError(t, err)
	// Backends may keep young dead rows longer than asked; what every backend
	// guarantees is that a purge never revives a dead delivery and never
	// drops a live one.
	st, err := s.Stats(ctx, topic, "g")
	require.NoError(t, err)
	assert.Equal(t, int64(1), st.Pending, "pending kept")
	assert.LessOrEqual(t, st.Dead, int64(1))
	cs = mustClaim(t, s, topic, "g", 10, longLease)
	assert.Equal(t, []string{"pending"}, payloads(cs), "a message with a live delivery survives purge; a dead one is not revived")
	assert.Empty(t, mustClaim(t, s, topic, "g", 10, longLease))
}

func contractStats(t *testing.T, s Store) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	mustGroup(t, s, topic, "other", StartFromLatest)
	mustAppend(t, s, newMsg(topic, "", "1"), newMsg(topic, "", "2"), newMsg(topic, "", "3"))

	cs := mustClaim(t, s, topic, "g", 2, longLease)
	require.Len(t, cs, 2)
	held, err := s.Kill(ctx, cs[0], "boom")
	require.NoError(t, err)
	require.True(t, held)

	st, err := s.Stats(ctx, topic, "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{Pending: 1, Leased: 1, Dead: 1}, st)
	st, err = s.Stats(ctx, topic, "other")
	require.NoError(t, err)
	assert.Equal(t, Stats{Pending: 3}, st)
	st, err = s.Stats(ctx, topic, "missing")
	require.NoError(t, err)
	assert.Equal(t, Stats{}, st)
}

// TestMemoryStoreClockSkew pins that leases follow the store's clock: an
// instance whose clock runs ahead cannot take over a lease that is still live
// on the clock that granted it. Shared backends use the database clock, so
// only the in-process store has a clock to skew.
func TestMemoryStoreClockSkew(t *testing.T) {
	s := newMemoryStore()
	topic := "skew"
	mustGroup(t, s, topic, "g", StartFromLatest)
	mustAppend(t, s, newMsg(topic, "", "x"))
	require.Len(t, mustClaim(t, s, topic, "g", 1, time.Minute), 1)

	// A Store whose callers pass only durations has no way to be handed a
	// skewed "now"; the store's own clock is the only one that matters.
	s.mu.Lock()
	realNow := s.now
	s.now = func() time.Time { return realNow().Add(30 * time.Second) }
	s.mu.Unlock()
	assert.Empty(t, mustClaim(t, s, topic, "g", 1, time.Minute))

	s.mu.Lock()
	s.now = func() time.Time { return realNow().Add(2 * time.Minute) }
	s.mu.Unlock()
	assert.Len(t, mustClaim(t, s, topic, "g", 1, time.Minute), 1)
}

func TestMemoryStoreClosed(t *testing.T) {
	s := newMemoryStore()
	require.NoError(t, s.Close(context.Background()))
	ctx := context.Background()
	assert.ErrorIs(t, s.Append(ctx, []*Message{newMsg("t", "", "x")}), ErrClosed)
	assert.ErrorIs(t, s.EnsureGroup(ctx, "t", "g", StartFromLatest), ErrClosed)
	_, err := s.Claim(ctx, ClaimRequest{Topic: "t", Group: "g", RunnerID: "r", Max: 1, Lease: time.Second})
	assert.ErrorIs(t, err, ErrClosed)
	assert.ErrorIs(t, s.Ping(ctx), ErrClosed)
}

// contractPayloadsRoundTrip stores payloads and headers that trip naive
// encodings: every byte value, an empty payload, quotes, backslashes,
// newlines and non-ASCII text.
func contractPayloadsRoundTrip(t *testing.T, s Store) {
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	allBytes := make([]byte, 256)
	for i := range allBytes {
		allBytes[i] = byte(i)
	}
	msgs := []*Message{
		newMsg(topic, "", ""),
		newMsg(topic, "", ""),
		newMsg(topic, "ключ-🔑", ""),
	}
	msgs[0].Payload = allBytes
	msgs[0].Headers = map[string]string{"quote": `it's "quoted" \ back`, "newline": "a\nb\r\n", "unicode": "héllo ✓"}
	msgs[1].Payload = nil
	msgs[1].Headers = nil
	msgs[2].Payload = []byte(`{"json":true,"nested":{"a":[1,2]}}`)
	for _, m := range msgs {
		mustAppend(t, s, m)
	}

	cs := mustClaim(t, s, topic, "g", 10, longLease)
	require.Len(t, cs, len(msgs))
	for i, want := range msgs {
		got := cs[i].Message
		assert.Equal(t, string(want.Payload), string(got.Payload), "payload %d", i)
		if len(want.Headers) == 0 {
			assert.Empty(t, got.Headers, "headers %d", i)
		} else {
			assert.Equal(t, want.Headers, got.Headers, "headers %d", i)
		}
		assert.Equal(t, want.Key, got.Key, "key %d", i)
	}
}

// contractBackfillOrder registers an Earliest group after a single batch and
// a run of separate publishes for one key: the backfill must replay them in
// publish order, one at a time.
func contractBackfillOrder(t *testing.T, s Store) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	var batch []*Message
	for i := range 10 {
		batch = append(batch, newMsg(topic, "k", fmt.Sprint(i)))
	}
	mustAppend(t, s, batch...)
	for i := 10; i < 15; i++ {
		mustAppend(t, s, newMsg(topic, "k", fmt.Sprint(i)))
	}
	mustGroup(t, s, topic, "late", StartFromEarliest)

	var got []string
	for range 15 {
		cs := mustClaim(t, s, topic, "late", 10, longLease)
		require.Len(t, cs, 1, "one delivery per key at a time")
		got = append(got, string(cs[0].Message.Payload))
		held, err := ackOne(ctx, s, cs[0])
		require.NoError(t, err)
		require.True(t, held)
	}
	want := make([]string, 15)
	for i := range want {
		want[i] = fmt.Sprint(i)
	}
	assert.Equal(t, want, got)
}

// contractLargeBatches pushes more rows than one SQL statement carries (500)
// through every batched path: one Append, an Earliest backfill, one Claim,
// Extend, Release, and acknowledging them all.
func contractLargeBatches(t *testing.T, s Store) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	const total = 1201
	mustGroup(t, s, topic, "live", StartFromLatest)
	msgs := make([]*Message, total)
	for i := range msgs {
		msgs[i] = newMsg(topic, "", fmt.Sprint(i))
	}
	mustAppend(t, s, msgs...)
	mustGroup(t, s, topic, "late", StartFromEarliest)

	for _, g := range []string{"live", "late"} {
		cs := mustClaim(t, s, topic, g, total+100, longLease)
		require.Len(t, cs, total, g)
		ids := map[string]bool{}
		for _, c := range cs {
			ids[c.Message.ID] = true
		}
		assert.Len(t, ids, total, "%s: every message exactly once", g)

		held, err := s.Extend(ctx, cs, longLease)
		require.NoError(t, err)
		assert.Len(t, held, total, "%s: every lease renewed", g)
		require.NoError(t, s.Release(ctx, cs))

		again := mustClaim(t, s, topic, g, total+100, longLease)
		require.Len(t, again, total, "%s: every lease released", g)
		for _, c := range again {
			require.Equal(t, 1, c.Attempt)
			ok, err := ackOne(ctx, s, c)
			require.NoError(t, err)
			require.True(t, ok)
		}
		st, err := s.Stats(ctx, topic, g)
		require.NoError(t, err)
		assert.Equal(t, Stats{}, st, g)
	}
}

// contractMaxLengthIdentifiers round-trips names, IDs and keys at the limits
// validation allows, which must fit every backend's columns.
func contractMaxLengthIdentifiers(t *testing.T, s Store) {
	ctx := context.Background()
	topic := (uniqueTopic(t) + strings.Repeat("t", MaxNameLength))[:MaxNameLength]
	group := strings.Repeat("g", MaxNameLength)
	mustGroup(t, s, topic, group, StartFromLatest)
	m := newMsg(topic, strings.Repeat("k", MaxIDLength), "x")
	m.ID = (uuid.NewString() + strings.Repeat("i", MaxIDLength))[:MaxIDLength]
	mustAppend(t, s, m)
	cs := mustClaim(t, s, topic, group, 1, longLease)
	require.Len(t, cs, 1)
	assert.Equal(t, m.ID, cs[0].Message.ID)
	assert.Equal(t, m.Key, cs[0].Message.Key)
	assert.Equal(t, topic, cs[0].Message.Topic)
	ok, err := ackOne(ctx, s, cs[0])
	require.NoError(t, err)
	assert.True(t, ok)
}

// contractMultiTopicAppend appends to several topics in one call: each
// topic's groups get only that topic's messages.
func contractMultiTopicAppend(t *testing.T, s Store) {
	a, b := uniqueTopic(t), uniqueTopic(t)
	mustGroup(t, s, a, "g", StartFromLatest)
	mustGroup(t, s, b, "g", StartFromLatest)
	mustAppend(t, s, newMsg(a, "", "a1"), newMsg(b, "", "b1"), newMsg(a, "", "a2"), newMsg(b, "k", "b2"))
	assert.Equal(t, []string{"a1", "a2"}, payloads(mustClaim(t, s, a, "g", 10, longLease)))
	assert.Equal(t, []string{"b1", "b2"}, payloads(mustClaim(t, s, b, "g", 10, longLease)))
}

// contractAckBatch acknowledges several deliveries at once, one of them with
// a claim that has since been taken over: the others are acknowledged and
// reported, the stale one is untouched.
func contractAckBatch(t *testing.T, s Store) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	mustAppend(t, s, newMsg(topic, "", "a"), newMsg(topic, "", "b"), newMsg(topic, "", "stale"))
	cs := mustClaim(t, s, topic, "g", 2, longLease)
	require.Len(t, cs, 2)
	stale := mustClaim(t, s, topic, "g", 1, contractLease)
	require.Len(t, stale, 1)
	time.Sleep(contractWait)
	retaken := mustClaim(t, s, topic, "g", 1, longLease)
	require.Len(t, retaken, 1)

	held, err := s.Ack(ctx, append(cs, stale[0]))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{cs[0].DeliveryID, cs[1].DeliveryID}, held)
	st, err := s.Stats(ctx, topic, "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{Leased: 1}, st, "the re-taken delivery is still leased")

	held, err = s.Ack(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, held)
}

// contractWaitingBehindHead queues several deliveries behind one key: they
// count as pending, only the head is ever claimable, a retry or release keeps
// the same head, and acknowledging or dead-lettering the head promotes
// exactly the next one, immediately.
func contractWaitingBehindHead(t *testing.T, s Store) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	for i := range 5 {
		mustAppend(t, s, newMsg(topic, "hot", fmt.Sprint(i)))
	}
	mustAppend(t, s, newMsg(topic, "", "free"))
	st, err := s.Stats(ctx, topic, "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{Pending: 6}, st, "deliveries queued behind their key count as pending")

	first := mustClaim(t, s, topic, "g", 10, longLease)
	require.Equal(t, []string{"0", "free"}, payloads(first), "only the key's head and unkeyed deliveries")
	st, err = s.Stats(ctx, topic, "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{Pending: 4, Leased: 2}, st)
	assert.Empty(t, mustClaim(t, s, topic, "g", 10, longLease), "the rest of the key waits")
	head := first[0]

	// Releasing the head hands back the same head.
	require.NoError(t, s.Release(ctx, []*Claimed{head}))
	again := mustClaim(t, s, topic, "g", 10, longLease)
	require.Equal(t, []string{"0"}, payloads(again))
	head = again[0]

	// Acknowledging it promotes "1" at once; "2" keeps waiting.
	ok, err := ackOne(ctx, s, head)
	require.NoError(t, err)
	require.True(t, ok)
	next := mustClaim(t, s, topic, "g", 10, longLease)
	require.Equal(t, []string{"1"}, payloads(next))

	// A retry keeps "1" as the head, so "2" stays behind it during backoff.
	ok, err = s.Retry(ctx, next[0], contractLease, "boom")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, mustClaim(t, s, topic, "g", 10, longLease))
	time.Sleep(contractWait)
	next = mustClaim(t, s, topic, "g", 10, longLease)
	require.Equal(t, []string{"1"}, payloads(next))

	// Dead-lettering the head promotes "2".
	ok, err = s.Kill(ctx, next[0], "boom")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, []string{"2"}, payloads(mustClaim(t, s, topic, "g", 10, longLease)))
	st, err = s.Stats(ctx, topic, "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{Pending: 2, Leased: 2, Dead: 1}, st)
}

// contractAckedRetention checks what the janitor keeps: a message every group
// has acknowledged survives for AckedRetention (so a late Earliest group can
// still replay it) and is then removed, while a dead delivery keeps its
// message for DeadRetention.
func contractAckedRetention(t *testing.T, s Store) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	acked, dead := newMsg(topic, "", "acked"), newMsg(topic, "", "dead")
	mustAppend(t, s, acked, dead)
	cs := mustClaim(t, s, topic, "g", 2, longLease)
	require.Len(t, cs, 2)
	ok, err := ackOne(ctx, s, cs[0])
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = s.Kill(ctx, cs[1], "boom")
	require.NoError(t, err)
	require.True(t, ok)
	time.Sleep(50 * time.Millisecond)

	purgeAll := func(policy PurgePolicy) {
		for range 20 {
			n, err := s.Purge(ctx, policy, 1000)
			require.NoError(t, err)
			if n == 0 {
				return
			}
		}
	}
	// Within the acked retention, a late Earliest group replays both.
	purgeAll(PurgePolicy{DeadRetention: time.Hour, AckedRetention: time.Hour})
	mustGroup(t, s, topic, "late1", StartFromEarliest)
	assert.ElementsMatch(t, []string{"acked", "dead"}, payloads(mustClaim(t, s, topic, "late1", 10, longLease)))

	// Past it, the acknowledged message is gone; the dead one's stays.
	// late1's own deliveries are released so only g's state matters.
	topic2 := uniqueTopic(t)
	mustGroup(t, s, topic2, "g", StartFromLatest)
	acked2, dead2 := newMsg(topic2, "", "acked"), newMsg(topic2, "", "dead")
	mustAppend(t, s, acked2, dead2)
	cs = mustClaim(t, s, topic2, "g", 2, longLease)
	require.Len(t, cs, 2)
	ok, err = ackOne(ctx, s, cs[0])
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = s.Kill(ctx, cs[1], "boom")
	require.NoError(t, err)
	require.True(t, ok)
	time.Sleep(50 * time.Millisecond)
	purgeAll(PurgePolicy{DeadRetention: time.Hour, AckedRetention: time.Millisecond})
	mustGroup(t, s, topic2, "late2", StartFromEarliest)
	assert.Equal(t, []string{"dead"}, payloads(mustClaim(t, s, topic2, "late2", 10, longLease)))
}

// contractMaxInFlight caps a group's unexpired leases: a claim takes only the
// headroom left, acknowledging frees a slot, and an expired lease no longer
// counts.
func contractMaxInFlight(t *testing.T, s Store) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	for i := range 10 {
		mustAppend(t, s, newMsg(topic, "", fmt.Sprint(i)))
	}
	claim := func(lease time.Duration) []*Claimed {
		t.Helper()
		cs, err := s.Claim(ctx, ClaimRequest{Topic: topic, Group: "g", RunnerID: "r", Max: 10, Lease: lease, MaxInFlight: 3})
		require.NoError(t, err)
		return cs
	}
	first := claim(longLease)
	require.Len(t, first, 3, "only the cap's worth")
	assert.Empty(t, claim(longLease), "no headroom left")

	ok, err := ackOne(ctx, s, first[0])
	require.NoError(t, err)
	require.True(t, ok)
	assert.Len(t, claim(contractLease), 1, "an acknowledgement frees one slot")

	// The short lease expires: that slot is free again, and the expired
	// delivery itself is claimable.
	time.Sleep(contractWait)
	assert.Len(t, claim(longLease), 1)
	assert.Empty(t, claim(longLease))

	// Without a cap the rest is claimable at once.
	cs, err := s.Claim(ctx, ClaimRequest{Topic: topic, Group: "g", RunnerID: "r", Max: 10, Lease: longLease})
	require.NoError(t, err)
	assert.Len(t, cs, 6)
}

// contractScheduled checks DeliverAt: a delivery is not claimable before it,
// a past time is immediate, an Earliest backfill honours it, a scheduled key
// head holds back its key, and a promoted successor still waits for its own
// time.
func contractScheduled(t *testing.T, s Store) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	later := time.Now().Add(contractLease).UTC()
	scheduled, past := newMsg(topic, "", "scheduled"), newMsg(topic, "", "past")
	scheduled.DeliverAt = later
	past.DeliverAt = time.Now().Add(-time.Hour)
	mustAppend(t, s, scheduled, past)

	assert.Equal(t, []string{"past"}, payloads(mustClaim(t, s, topic, "g", 10, longLease)))
	st, err := s.Stats(ctx, topic, "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{Pending: 1, Leased: 1}, st, "scheduled counts as pending")

	// An Earliest group registered now sees the same schedule.
	mustGroup(t, s, topic, "late", StartFromEarliest)
	assert.Equal(t, []string{"past"}, payloads(mustClaim(t, s, topic, "late", 10, longLease)))

	// Strict key order: an immediate message waits behind its scheduled head;
	// once the head runs, a scheduled successor still waits for its own time.
	keyed := uniqueTopic(t)
	mustGroup(t, s, keyed, "g", StartFromLatest)
	head := newMsg(keyed, "k", "head")
	head.DeliverAt = later
	mustAppend(t, s, head)
	mustAppend(t, s, newMsg(keyed, "k", "behind"))
	tail := newMsg(keyed, "k", "tail")
	tail.DeliverAt = later.Add(contractWait)
	mustAppend(t, s, tail)
	assert.Empty(t, mustClaim(t, s, keyed, "g", 10, longLease), "the key waits for its scheduled head")

	time.Sleep(time.Until(later) + 100*time.Millisecond)
	got := mustClaim(t, s, topic, "g", 10, longLease)
	require.Equal(t, []string{"scheduled"}, payloads(got))
	assert.WithinDuration(t, later, got[0].Message.DeliverAt, time.Millisecond, "DeliverAt round-trips")
	assert.Equal(t, []string{"scheduled"}, payloads(mustClaim(t, s, topic, "late", 10, longLease)))

	h := mustClaim(t, s, keyed, "g", 10, longLease)
	require.Equal(t, []string{"head"}, payloads(h))
	ok, err := ackOne(ctx, s, h[0])
	require.NoError(t, err)
	require.True(t, ok)
	b := mustClaim(t, s, keyed, "g", 10, longLease)
	require.Equal(t, []string{"behind"}, payloads(b))
	ok, err = ackOne(ctx, s, b[0])
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, mustClaim(t, s, keyed, "g", 10, longLease), "the promoted tail is not due yet")
	time.Sleep(time.Until(tail.DeliverAt) + 100*time.Millisecond)
	assert.Equal(t, []string{"tail"}, payloads(mustClaim(t, s, keyed, "g", 10, longLease)))
}

// contractRetentionFromDeathAndDueTime checks what retention is measured
// from: a dead delivery is kept DeadRetention after it died, not after it was
// created, and a finished message is kept AckedRetention after it became due,
// not after it was published.
func contractRetentionFromDeathAndDueTime(t *testing.T, s Store) {
	ctx := context.Background()
	const keep = 300 * time.Millisecond
	policy := PurgePolicy{DeadRetention: keep, AckedRetention: keep}
	purgeAll := func() {
		for range 20 {
			n, err := s.Purge(ctx, policy, 1000)
			require.NoError(t, err)
			if n == 0 {
				return
			}
		}
	}

	// A delivery that lives longer than DeadRetention before it dies.
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	mustAppend(t, s, newMsg(topic, "", "old"))
	time.Sleep(keep + 100*time.Millisecond)
	cs := mustClaim(t, s, topic, "g", 1, longLease)
	require.Len(t, cs, 1)
	ok, err := s.Kill(ctx, cs[0], "boom")
	require.NoError(t, err)
	require.True(t, ok)
	purgeAll()
	st, err := s.Stats(ctx, topic, "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{Dead: 1}, st, "kept for DeadRetention after it died")

	// A scheduled message, finished as soon as it is due.
	topic2 := uniqueTopic(t)
	mustGroup(t, s, topic2, "g", StartFromLatest)
	m := newMsg(topic2, "", "scheduled")
	m.DeliverAt = time.Now().Add(keep + 100*time.Millisecond)
	mustAppend(t, s, m)
	time.Sleep(time.Until(m.DeliverAt) + 50*time.Millisecond)
	cs = mustClaim(t, s, topic2, "g", 1, longLease)
	require.Len(t, cs, 1)
	ok, err = ackOne(ctx, s, cs[0])
	require.NoError(t, err)
	require.True(t, ok)
	purgeAll()
	mustGroup(t, s, topic2, "late", StartFromEarliest)
	assert.Equal(t, []string{"scheduled"}, payloads(mustClaim(t, s, topic2, "late", 10, longLease)),
		"kept for AckedRetention after it became due")
}
