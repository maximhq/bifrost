package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"gorm.io/gorm"
)

const (
	// chStatusAcked marks an acknowledged delivery. ClickHouse cannot cheaply
	// delete one row, so Ack writes this tombstone and the janitor deletes it.
	chStatusAcked = "acked"
	// chAbandonedPublishAge is how long a delivery may wait for its message
	// before the janitor treats the publish as abandoned and deletes it, so a
	// publish that failed half way and was never retried stops blocking its
	// key.
	chAbandonedPublishAge = 5 * time.Minute
	// chClaimBudget bounds waiting for the group lock plus the claim itself;
	// a claim that cannot get the lock in time returns nothing and retries
	// on the next poll.
	chClaimBudget = 10 * time.Second
	// chInitialVer is the version of a delivery's first row. Every state change
	// is written with now64(9), so a duplicate first row (a publisher retry, an
	// overlapping backfill) never outranks a claim, ack or retry.
	chInitialVer = "fromUnixTimestamp64Nano(1)"
	chNowMicros  = "toUnixTimestamp64Micro(now64(6))"
	// chCappedVer versions a write after a claim: now, but never later than
	// the end of the lease the writer read (bound in nanoseconds).
	chCappedVer = "fromUnixTimestamp64Nano(least(toUnixTimestamp64Nano(now64(9)), ?))"
	// chRowLockStripes shards the in-process row locks.
	chRowLockStripes = 64
)

// chSchemaStore is the logstore hook that creates extension tables with the
// logstore's own cluster and replication settings.
type chSchemaStore interface {
	EnsureClickHouseTable(ctx context.Context, model any, table, partitionBy, orderBy, ttl string, skipIndexes []string) error
	ReconcileClickHouseTTL(ctx context.Context, table, ttl string) error
}

// clickhouseStore is the Store for the ClickHouse logstore. ClickHouse has no
// row locks or conditional updates, so every state change re-inserts the row
// into a ReplacingMergeTree with a newer `ver`, reads use FINAL, and
// read-then-write sequences are serialised with distributed locks:
//
//   - a topic lock around Append, EnsureGroup and message purges, so a
//     group's registration and backfill, the group list a publish fans out
//     to, and the deletion of messages nothing needs are never interleaved;
//   - a group lock around Claim, so one claimer at a time reads a group's
//     due deliveries and leases them.
//
// Writes after a claim take no distributed lock; see update for how a late
// write is kept from overwriting a newer claim.
//
// Without a shared locker the store can publish, taking its topic locks in
// process, but cannot consume.
type clickhouseStore struct {
	db        *gorm.DB
	locker    logstore.DistributedLocker // shared; nil allows publishing only
	topics    logstore.DistributedLocker // locker, or an in-process one without it
	retention time.Duration
	logger    schemas.Logger
	budget    time.Duration // lock wait plus claim, chClaimBudget
	abandoned time.Duration // chAbandonedPublishAge

	mu     sync.Mutex
	groups map[string]chQueueGroup // topic/group -> registration, cached once seen

	rowLocks [chRowLockStripes]sync.Mutex // serialise this instance's read-then-insert on a delivery
}

// lockRows takes the row-lock stripes of ids in ascending order and returns
// their release. Without it, a heartbeat that read a delivery just before
// the handler acknowledged it would write the older, leased version back
// with a newer ver, and the delivery would run again.
func (s *clickhouseStore) lockRows(ids []string) func() {
	var stripes []int
	for _, id := range ids {
		h := fnv.New32a()
		_, _ = h.Write([]byte(id))
		stripe := int(h.Sum32() % chRowLockStripes)
		if !slices.Contains(stripes, stripe) {
			stripes = append(stripes, stripe)
		}
	}
	slices.Sort(stripes)
	for _, i := range stripes {
		s.rowLocks[i].Lock()
	}
	return func() {
		for _, i := range slices.Backward(stripes) {
			s.rowLocks[i].Unlock()
		}
	}
}

// newClickHouseStore creates the queue tables through the logstore and
// returns a store over its database. locker may be nil, which allows
// publishing but not consuming.
func newClickHouseStore(ctx context.Context, db *gorm.DB, schema chSchemaStore, locker logstore.DistributedLocker, retention time.Duration, logger schemas.Logger) (*clickhouseStore, error) {
	hours := max(int64(retention/time.Hour), 1)
	// A row expires a full retention after the latest of when it was stored,
	// when it became due and when it was dead-lettered, so a scheduled message
	// never expires before it runs and a failure record (and its message) is
	// kept for the retention after the failure. The interval is written the way
	// ClickHouse stores it, so an unchanged TTL is recognised on the next start
	// and a changed one is applied.
	ttl := fmt.Sprintf("greatest(toDateTime(created_at), toDateTime(fromUnixTimestamp64Micro(deliver_at)), "+
		"toDateTime(fromUnixTimestamp64Micro(dead_at))) + toIntervalHour(%d)", hours)
	tables := []struct {
		model   any
		name    string
		orderBy string
		ttl     string
	}{
		{&chQueueGroup{}, "queue_groups", "(topic, group_name)", ""},
		{&chQueueMessage{}, "queue_messages", "(topic, id)", ttl},
		// The sorting key is the identity ReplacingMergeTree deduplicates on, so
		// it must be exactly (topic, group, delivery id), never include state.
		{&chQueueDelivery{}, "queue_deliveries", "(topic, group_name, id)", ttl},
	}
	create := func() error {
		for _, t := range tables {
			if err := schema.EnsureClickHouseTable(ctx, t.model, t.name, "", t.orderBy, t.ttl, nil); err != nil {
				return err
			}
			if t.ttl != "" {
				if err := schema.ReconcileClickHouseTTL(ctx, t.name, t.ttl); err != nil {
					return err
				}
			}
		}
		return nil
	}
	// The DDL is idempotent; the lock only keeps instances from racing ALTERs.
	if locker != nil {
		held, release, err := locker.Acquire(ctx, "queue:migrate")
		if err != nil {
			return nil, fmt.Errorf("queue: clickhouse migration lock: %w", err)
		}
		defer release()
		ctx = held
	}
	if err := create(); err != nil {
		return nil, fmt.Errorf("queue: create clickhouse tables: %w", err)
	}
	topics := locker
	if topics == nil {
		topics = NewProcessLocker()
	}
	return &clickhouseStore{
		db:        db,
		locker:    locker,
		topics:    topics,
		retention: retention,
		logger:    logger,
		budget:    chClaimBudget,
		abandoned: chAbandonedPublishAge,
		groups:    map[string]chQueueGroup{},
	}, nil
}

// CanConsume reports whether Subscribe may run on this store.
func (s *clickhouseStore) CanConsume() error {
	if s.locker == nil {
		return fmt.Errorf("%w: consuming from the clickhouse logstore needs a shared distributed locker", ErrNoLocker)
	}
	return nil
}

// groupLockKey bounds the lock key whatever the name lengths.
func groupLockKey(topic, group string) string {
	sum := sha256.Sum256([]byte(topic + "\x00" + group))
	return "queue:" + hex.EncodeToString(sum[:16])
}

// topicLockKey is distinct from every groupLockKey: "t" is not a hex digit.
func topicLockKey(topic string) string {
	sum := sha256.Sum256([]byte(topic))
	return "queue:t:" + hex.EncodeToString(sum[:16])
}

// lockTopics takes the topic locks of topics in sorted order, so two
// multi-topic publishes cannot deadlock, and returns a context that is
// cancelled if any of them is lost.
func (s *clickhouseStore) lockTopics(ctx context.Context, topics []string) (context.Context, func(), error) {
	sorted := slices.Sorted(slices.Values(topics))
	var releases []func()
	release := func() {
		for _, r := range slices.Backward(releases) {
			r()
		}
	}
	held := ctx
	for _, topic := range slices.Compact(sorted) {
		next, r, err := s.topics.Acquire(held, topicLockKey(topic))
		if err != nil {
			release()
			return nil, nil, fmt.Errorf("queue: lock topic %s: %w", topic, err)
		}
		held = next
		releases = append(releases, r)
	}
	return held, release, nil
}

// liveWhere drops rows past retention that TTL has not physically removed
// yet, so a surviving older version can never be read back.
func (s *clickhouseStore) liveWhere() (string, int64) {
	return "greatest(toUnixTimestamp64Micro(created_at), deliver_at, dead_at) > " + chNowMicros + " - ?", s.retention.Microseconds()
}

// EnsureGroup registers group under the topic lock, so no publish fans out
// while it does. An Earliest group's backfill runs before the group row is
// written: until that row exists nothing claims for the group, and a
// backfill that fails part way is finished by the next EnsureGroup, which
// skips the deliveries already written.
func (s *clickhouseStore) EnsureGroup(ctx context.Context, topic, group string, startFrom StartFrom) error {
	if err := s.CanConsume(); err != nil {
		return err
	}
	held, release, err := s.lockTopics(ctx, []string{topic})
	if err != nil {
		return err
	}
	defer release()
	if _, ok, err := s.loadGroup(held, topic, group); err != nil || ok {
		return err
	}
	if startFrom == StartFromEarliest {
		if err := s.backfill(held, topic, group); err != nil {
			return err
		}
	}
	if err := s.db.WithContext(held).Exec("INSERT INTO queue_groups (topic, group_name, start_from, created_at, ver) VALUES (?, ?, ?, now64(3), now64(9))",
		topic, group, string(startFrom)).Error; err != nil {
		return fmt.Errorf("register group: %w", err)
	}
	return nil
}

// loadGroup reads a group's registration, caching it once found.
func (s *clickhouseStore) loadGroup(ctx context.Context, topic, group string) (chQueueGroup, bool, error) {
	key := topic + "/" + group
	s.mu.Lock()
	g, ok := s.groups[key]
	s.mu.Unlock()
	if ok {
		return g, true, nil
	}
	var rows []chQueueGroup
	if err := s.db.WithContext(ctx).Raw("SELECT topic, group_name, start_from, created_at FROM queue_groups FINAL WHERE topic = ? AND group_name = ?",
		topic, group).Scan(&rows).Error; err != nil {
		return chQueueGroup{}, false, fmt.Errorf("load group: %w", err)
	}
	if len(rows) == 0 {
		return chQueueGroup{}, false, nil
	}
	s.mu.Lock()
	s.groups[key] = rows[0]
	s.mu.Unlock()
	return rows[0], true, nil
}

// backfill creates group's delivery for every retained message on topic that
// does not have one yet, in publish order. The caller holds the topic lock,
// so no publish or purge runs meanwhile.
func (s *clickhouseStore) backfill(ctx context.Context, topic, group string) error {
	live, liveArg := s.liveWhere()
	var msgs []chQueueMessage
	if err := s.db.WithContext(ctx).Raw("SELECT id, topic, msg_key, seq, deliver_at, created_at, dead_at FROM queue_messages FINAL WHERE topic = ? AND "+live+
		" AND id NOT IN (SELECT message_id FROM queue_deliveries FINAL WHERE topic = ? AND group_name = ?) ORDER BY seq, id",
		topic, liveArg, topic, group).Scan(&msgs).Error; err != nil {
		return fmt.Errorf("backfill scan: %w", err)
	}
	dels := make([]chQueueDelivery, len(msgs))
	for i, m := range msgs {
		dels[i] = newCHDelivery(m, group)
	}
	return s.insertDeliveries(ctx, dels, chInitialVer)
}

// newCHDelivery returns m's first delivery row for group. It shares the
// message's created_at and dead_at, so the two expire together even when
// another group's dead delivery is what keeps the message.
func newCHDelivery(m chQueueMessage, group string) chQueueDelivery {
	return chQueueDelivery{
		ID:            DeliveryID(m.ID, group),
		MessageID:     m.ID,
		Topic:         m.Topic,
		GroupName:     group,
		MsgKey:        m.MsgKey,
		Seq:           m.Seq,
		Status:        statusPending,
		DeliverAt:     m.DeliverAt,
		NextAttemptAt: m.DeliverAt, // 0 is always due
		CreatedAt:     m.CreatedAt,
		DeadAt:        m.DeadAt,
	}
}

// existingIDs returns which of ids, all on topics, are present in table.
// Every lookup by id also filters on the sorting key's leading column, so
// ClickHouse reads only the granules of those topics.
func (s *clickhouseStore) existingIDs(ctx context.Context, table string, topics, ids []string) (map[string]bool, error) {
	found := make(map[string]bool, len(ids))
	for chunk := range slices.Chunk(ids, sqlChunk) {
		var got []string
		if err := s.db.WithContext(ctx).Raw("SELECT id FROM "+table+" FINAL WHERE topic IN ? AND id IN ?", topics, chunk).Scan(&got).Error; err != nil {
			return nil, fmt.Errorf("existence check on %s: %w", table, err)
		}
		for _, id := range got {
			found[id] = true
		}
	}
	return found, nil
}

// insertDeliveries writes one version of each row. ver is a SQL expression.
// Every version carries the row's original created_at, so all of them expire
// together.
func (s *clickhouseStore) insertDeliveries(ctx context.Context, rows []chQueueDelivery, ver string) error {
	return s.insertVersions(ctx, rows, ver, nil)
}

// insertVersions is insertDeliveries with one argument per row bound into
// ver, when verArgs is not nil.
func (s *clickhouseStore) insertVersions(ctx context.Context, rows []chQueueDelivery, ver string, verArgs []int64) error {
	for start := 0; start < len(rows); start += sqlChunk {
		chunk := rows[start:min(start+sqlChunk, len(rows))]
		var b sqlBuilder
		b.add("INSERT INTO queue_deliveries (id, message_id, topic, group_name, msg_key, seq, status, attempts, " +
			"deliver_at, next_attempt_at, claimed_by, claim_token, claimed_until, last_error, created_at, dead_at, ver) VALUES ")
		for i, r := range chunk {
			if i > 0 {
				b.add(", ")
			}
			// Times are bound as epoch milliseconds: the driver formats a
			// time.Time parameter to whole seconds.
			args := []any{r.ID, r.MessageID, r.Topic, r.GroupName, r.MsgKey, r.Seq, r.Status, r.Attempts, r.DeliverAt, r.NextAttemptAt,
				r.ClaimedBy, r.ClaimToken, r.ClaimedUntil, r.LastError, r.CreatedAt.UnixMilli(), r.DeadAt}
			if verArgs != nil {
				args = append(args, verArgs[start+i])
			}
			b.add("(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, fromUnixTimestamp64Milli(?), ?, "+ver+")", args...)
		}
		if err := s.db.WithContext(ctx).Exec(b.String(), b.args...).Error; err != nil {
			return fmt.Errorf("insert deliveries: %w", err)
		}
	}
	return nil
}

// Append publishes msgs under their topics' locks, so the group list it reads
// is exactly the groups that must receive them.
//
// Messages are numbered from the database clock in nanoseconds plus their
// position in the batch. The topic lock orders publishes, and the clock moves
// on by more than a batch's length between two of them, since writing a
// batch takes longer than a nanosecond per message.
//
// Deliveries are written before their messages, so a message row marks a
// complete publish. A retry after a partial one finds the message missing and
// writes everything again; its first rows tie on ver with the abandoned
// attempt's, and either copy is the same pending delivery. Claim skips a
// delivery until its message lands, and the janitor deletes deliveries whose
// publish was never retried.
func (s *clickhouseStore) Append(ctx context.Context, msgs []*Message) error {
	if len(msgs) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(msgs))
	var unique []*Message
	var ids, topics []string
	for _, m := range msgs {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		unique = append(unique, m)
		ids = append(ids, m.ID)
		if !slices.Contains(topics, m.Topic) {
			topics = append(topics, m.Topic)
		}
	}
	held, release, err := s.lockTopics(ctx, topics)
	if err != nil {
		return err
	}
	defer release()

	// Republishing a known message must not fan it out again: its deliveries
	// may already be acknowledged.
	existing, err := s.existingIDs(held, "queue_messages", topics, ids)
	if err != nil {
		return err
	}
	var groups []chQueueGroup
	if err := s.db.WithContext(held).Raw("SELECT topic, group_name FROM queue_groups FINAL WHERE topic IN ?", topics).Scan(&groups).Error; err != nil {
		return fmt.Errorf("load groups: %w", err)
	}
	var base int64
	if err := s.db.WithContext(held).Raw("SELECT toUnixTimestamp64Nano(now64(9))").Scan(&base).Error; err != nil {
		return fmt.Errorf("read clock: %w", err)
	}
	createdAt := time.Unix(0, base)

	var rows []chQueueMessage
	for _, m := range unique {
		if existing[m.ID] {
			continue
		}
		headers := "{}"
		if len(m.Headers) > 0 {
			if headers, err = sonic.MarshalString(m.Headers); err != nil {
				return fmt.Errorf("encode headers of message %s: %w", m.ID, err)
			}
		}
		rows = append(rows, chQueueMessage{
			ID: m.ID, Topic: m.Topic, MsgKey: m.Key, Seq: base + int64(len(rows)), Payload: string(m.Payload), Headers: headers,
			PublishedAt: m.PublishedAt, DeliverAt: deliverAtMicros(m.DeliverAt), CreatedAt: createdAt,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	var dels []chQueueDelivery
	for _, r := range rows {
		for _, g := range groups {
			if g.Topic == r.Topic {
				dels = append(dels, newCHDelivery(r, g.GroupName))
			}
		}
	}
	if err := s.insertDeliveries(held, dels, chInitialVer); err != nil {
		return err
	}
	for chunk := range slices.Chunk(rows, sqlChunk) {
		var b sqlBuilder
		b.add("INSERT INTO queue_messages (id, topic, msg_key, seq, payload, headers, published_at, deliver_at, created_at, ver) VALUES ")
		for i, r := range chunk {
			if i > 0 {
				b.add(", ")
			}
			// Payloads are hex-encoded on the wire so arbitrary bytes survive
			// the driver's client-side literal binding.
			b.add("(?, ?, ?, ?, unhex(?), ?, fromUnixTimestamp64Milli(?), ?, fromUnixTimestamp64Milli(?), now64(9))", r.ID, r.Topic, r.MsgKey, r.Seq,
				hex.EncodeToString([]byte(r.Payload)), r.Headers, r.PublishedAt.UnixMilli(), r.DeliverAt, r.CreatedAt.UnixMilli())
		}
		if err := s.db.WithContext(held).Exec(b.String(), b.args...).Error; err != nil {
			return fmt.Errorf("insert messages: %w", err)
		}
	}
	return nil
}

const chDeliveryColumns = "id, message_id, topic, group_name, msg_key, seq, status, attempts, deliver_at, next_attempt_at, " +
	"claimed_by, claim_token, claimed_until, last_error, created_at, dead_at"

// dbNow reads the database clock in unix microseconds. Lease and retry
// deadlines are computed from it, never from this instance's clock.
func (s *clickhouseStore) dbNow(ctx context.Context) (int64, error) {
	var now int64
	if err := s.db.WithContext(ctx).Raw("SELECT " + chNowMicros).Scan(&now).Error; err != nil {
		return 0, fmt.Errorf("read clock: %w", err)
	}
	return now, nil
}

func (s *clickhouseStore) Claim(ctx context.Context, req ClaimRequest) ([]*Claimed, error) {
	if err := s.CanConsume(); err != nil {
		return nil, err
	}
	topic, group, max := req.Topic, req.Group, req.Max
	if max <= 0 {
		return nil, nil
	}
	if _, ok, err := s.loadGroup(ctx, topic, group); err != nil || !ok {
		return nil, err
	}
	// Skip the lock round-trip when nothing is due.
	if due, err := s.countDue(ctx, topic, group); err != nil || due == 0 {
		return nil, err
	}

	budgetCtx, cancel := context.WithTimeout(ctx, s.budget)
	defer cancel()
	held, release, err := s.locker.Acquire(budgetCtx, groupLockKey(topic, group))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, nil // another instance is claiming; try next poll
		}
		return nil, fmt.Errorf("lock group %s on %s: %w", group, topic, err)
	}
	defer release()

	dbNow, err := s.dbNow(held)
	if err != nil {
		return nil, err
	}
	if req.MaxInFlight > 0 {
		// The group lock is held, so no other claim can change the count.
		live, liveArg := s.liveWhere()
		var inFlight int64
		if err := s.db.WithContext(held).Raw("SELECT count() FROM queue_deliveries FINAL WHERE topic = ? AND group_name = ? "+
			"AND status = 'leased' AND claimed_until >= ? AND "+live, topic, group, dbNow, liveArg).Scan(&inFlight).Error; err != nil {
			return nil, fmt.Errorf("count in flight: %w", err)
		}
		if max = min(max, req.MaxInFlight-int(inFlight)); max <= 0 {
			return nil, nil
		}
	}
	// Same eligibility as the SQL stores, evaluated in ClickHouse so only the
	// claimed rows come back: due, and either unkeyed or the oldest live
	// delivery of its key. The live set is read once (FINAL) as a named
	// subquery.
	live, liveArg := s.liveWhere()
	var picked []chQueueDelivery
	if err := s.db.WithContext(held).Raw("WITH active AS (SELECT "+chDeliveryColumns+" FROM queue_deliveries FINAL "+
		"WHERE topic = ? AND group_name = ? AND status IN ('pending', 'leased') AND "+live+") "+
		"SELECT "+chDeliveryColumns+" FROM active "+
		"WHERE ((status = 'pending' AND next_attempt_at <= ?) OR (status = 'leased' AND claimed_until < ?)) "+
		"AND (msg_key = '' OR id IN (SELECT argMin(id, (seq, id)) FROM active WHERE msg_key != '' GROUP BY msg_key)) "+
		"ORDER BY seq, id LIMIT ?",
		topic, group, liveArg, dbNow, dbNow, max).Scan(&picked).Error; err != nil {
		return nil, fmt.Errorf("claim scan: %w", err)
	}
	if len(picked) == 0 {
		return nil, nil
	}

	msgIDs := make([]string, len(picked))
	for i, r := range picked {
		msgIDs[i] = r.MessageID
	}
	msgs, err := s.loadMessages(held, topic, msgIDs)
	if err != nil {
		return nil, err
	}
	var leased []chQueueDelivery
	var out []*Claimed
	for _, r := range picked {
		m, ok := msgs[r.MessageID]
		if !ok {
			continue // its message has not landed yet; claim it on a later poll
		}
		d := r
		if d.Status == statusLeased {
			d.Attempts++ // the previous holder died mid-attempt
		}
		d.Status = statusLeased
		d.ClaimedBy = req.RunnerID
		d.ClaimToken = uuid.NewString()
		d.ClaimedUntil = dbNow + req.Lease.Microseconds()
		leased = append(leased, d)
		out = append(out, &Claimed{DeliveryID: d.ID, ClaimToken: d.ClaimToken, Group: group, Attempt: int(d.Attempts) + 1, Message: m})
	}
	if err := s.insertDeliveries(held, leased, "now64(9)"); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *clickhouseStore) countDue(ctx context.Context, topic, group string) (int64, error) {
	live, liveArg := s.liveWhere()
	var n int64
	err := s.db.WithContext(ctx).Raw("SELECT count() FROM queue_deliveries FINAL WHERE topic = ? AND group_name = ? AND "+live+
		" AND ((status = 'pending' AND next_attempt_at <= "+chNowMicros+") OR (status = 'leased' AND claimed_until < "+chNowMicros+"))",
		topic, group, liveArg).Scan(&n).Error
	if err != nil {
		return 0, fmt.Errorf("count due: %w", err)
	}
	return n, nil
}

func (s *clickhouseStore) loadMessages(ctx context.Context, topic string, ids []string) (map[string]*Message, error) {
	out := make(map[string]*Message, len(ids))
	for chunk := range slices.Chunk(ids, sqlChunk) {
		var rows []chQueueMessage
		if err := s.db.WithContext(ctx).Raw("SELECT id, topic, msg_key, payload, headers, published_at, deliver_at FROM queue_messages FINAL WHERE topic = ? AND id IN ?",
			topic, chunk).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("load messages: %w", err)
		}
		for _, r := range rows {
			m := &Message{ID: r.ID, Topic: r.Topic, Key: r.MsgKey, Payload: []byte(r.Payload), PublishedAt: r.PublishedAt.UTC()}
			if r.DeliverAt > 0 {
				m.DeliverAt = time.UnixMicro(r.DeliverAt).UTC()
			}
			if r.Headers != "" && r.Headers != "{}" {
				if err := sonic.UnmarshalString(r.Headers, &m.Headers); err != nil {
					return nil, fmt.Errorf("decode headers of message %s: %w", r.ID, err)
				}
			}
			out[r.ID] = m
		}
	}
	return out, nil
}

// heldRows returns the current versions of the deliveries whose claim tokens
// are still the ones given, keyed by claim token, and the database clock.
func (s *clickhouseStore) heldRows(ctx context.Context, cs []*Claimed) (map[string]chQueueDelivery, int64, error) {
	out := make(map[string]chQueueDelivery, len(cs))
	now, err := s.dbNow(ctx)
	if err != nil {
		return nil, 0, err
	}
	for chunk := range slices.Chunk(cs, sqlChunk) {
		ids := make([]string, len(chunk))
		var topics, groups []string
		for i, c := range chunk {
			ids[i] = c.DeliveryID
			if !slices.Contains(topics, c.Message.Topic) {
				topics = append(topics, c.Message.Topic)
			}
			if !slices.Contains(groups, c.Group) {
				groups = append(groups, c.Group)
			}
		}
		var rows []chQueueDelivery
		if err := s.db.WithContext(ctx).Raw("SELECT "+chDeliveryColumns+" FROM queue_deliveries FINAL WHERE topic IN ? AND group_name IN ? AND id IN ?",
			topics, groups, ids).Scan(&rows).Error; err != nil {
			return nil, 0, fmt.Errorf("read held deliveries: %w", err)
		}
		for _, r := range rows {
			if r.Status == statusLeased && r.ClaimToken != "" {
				out[r.ClaimToken] = r
			}
		}
	}
	return out, now, nil
}

// update re-reads cs and writes the next version of each delivery a claim
// still holds, as next returns it (or leaves it alone when next returns
// false). It returns the IDs of the deliveries the caller may treat as
// changed.
//
// ClickHouse cannot make the write conditional, so a holder whose lease runs
// out between its read and its write could overwrite the claim of whoever
// took the delivery over. Three rules prevent that:
//
//   - only a lease that has not expired is held, so a holder that is already
//     late changes nothing;
//   - the write is versioned no later than the end of the lease it read,
//     while a claim of an expired lease is versioned after that end, so a
//     write that lands after a takeover always loses to it;
//   - a delivery is reported changed only when the write completed before
//     the lease ended, measured on this instance's monotonic clock from
//     before the read. Only then can no claim have seen the lease expire
//     before the write was visible. A write that may have lost is reported as
//     a lost lease, which at-least-once delivery allows for.
//
// The row locks serialise this instance's own writes of one delivery, such
// as a heartbeat and an acknowledgement of the same claim.
func (s *clickhouseStore) update(ctx context.Context, cs []*Claimed, next func(d chQueueDelivery, now int64) (chQueueDelivery, bool)) ([]string, error) {
	ids := make([]string, len(cs))
	for i, c := range cs {
		ids[i] = c.DeliveryID
	}
	defer s.lockRows(ids)()
	start := time.Now()
	rows, now, err := s.heldRows(ctx, cs)
	if err != nil {
		return nil, err
	}
	var out []chQueueDelivery
	var caps []int64
	for _, c := range cs {
		d, ok := rows[c.ClaimToken]
		if !ok || d.ID != c.DeliveryID || d.ClaimedUntil <= now {
			continue
		}
		leaseEnd := d.ClaimedUntil
		if d, ok = next(d, now); ok {
			out = append(out, d)
			caps = append(caps, leaseEnd*1000)
		}
	}
	if err := s.insertVersions(ctx, out, chCappedVer, caps); err != nil {
		return nil, err
	}
	elapsed := time.Since(start).Microseconds()
	var held []string
	for i, d := range out {
		if elapsed < caps[i]/1000-now {
			held = append(held, d.ID)
		}
	}
	return held, nil
}

func unlease(d *chQueueDelivery) {
	d.ClaimedBy = ""
	d.ClaimToken = ""
	d.ClaimedUntil = 0
}

// Ack writes acked tombstones for every delivery still held, with one read
// and one insert for the whole batch.
func (s *clickhouseStore) Ack(ctx context.Context, cs []*Claimed) ([]string, error) {
	if len(cs) == 0 {
		return nil, nil
	}
	return s.update(ctx, cs, func(d chQueueDelivery, _ int64) (chQueueDelivery, bool) {
		d.Status = chStatusAcked
		unlease(&d)
		return d, true
	})
}

func (s *clickhouseStore) Retry(ctx context.Context, c *Claimed, backoff time.Duration, lastErr string) (bool, error) {
	ids, err := s.update(ctx, []*Claimed{c}, func(d chQueueDelivery, now int64) (chQueueDelivery, bool) {
		d.Status = statusPending
		d.Attempts++
		d.NextAttemptAt = now + backoff.Microseconds()
		d.LastError = lastErr
		unlease(&d)
		return d, true
	})
	return len(ids) == 1, err
}

// Kill dead-letters c's delivery. Retention for the dead row and its message
// then runs from now, so the message is stamped first: if the dead row's write
// fails afterwards, the message is only kept a little longer.
func (s *clickhouseStore) Kill(ctx context.Context, c *Claimed, lastErr string) (bool, error) {
	// A server-side copy of the message's current version, so the payload never
	// leaves ClickHouse.
	if err := s.db.WithContext(ctx).Exec("INSERT INTO queue_messages (id, topic, msg_key, seq, payload, headers, published_at, deliver_at, created_at, dead_at, ver) "+
		"SELECT id, topic, msg_key, seq, payload, headers, published_at, deliver_at, created_at, "+chNowMicros+", now64(9) "+
		"FROM queue_messages FINAL WHERE topic = ? AND id = ?", c.Message.Topic, c.Message.ID).Error; err != nil {
		return false, fmt.Errorf("retain message %s: %w", c.Message.ID, err)
	}
	ids, err := s.update(ctx, []*Claimed{c}, func(d chQueueDelivery, now int64) (chQueueDelivery, bool) {
		d.Status = statusDead
		d.Attempts++
		d.LastError = lastErr
		d.DeadAt = now
		unlease(&d)
		return d, true
	})
	return len(ids) == 1, err
}

func (s *clickhouseStore) Extend(ctx context.Context, cs []*Claimed, lease time.Duration) ([]string, error) {
	if len(cs) == 0 {
		return nil, nil
	}
	return s.update(ctx, cs, func(d chQueueDelivery, now int64) (chQueueDelivery, bool) {
		d.ClaimedUntil = now + lease.Microseconds()
		return d, true
	})
}

func (s *clickhouseStore) Release(ctx context.Context, cs []*Claimed) error {
	if len(cs) == 0 {
		return nil
	}
	_, err := s.update(ctx, cs, func(d chQueueDelivery, now int64) (chQueueDelivery, bool) {
		d.Status = statusPending
		d.NextAttemptAt = now
		unlease(&d)
		return d, true
	})
	return err
}

// Purge deletes, by id so every version of a row goes at once and no older
// version can resurface:
//
//   - acked tombstones, which no write changes again;
//   - dead deliveries DeadRetention after they died (dead_at);
//   - messages no pending, leased or dead delivery refers to, AckedRetention
//     after they were published or became due, whichever is later;
//   - deliveries whose message never landed after chAbandonedPublishAge,
//     left by a publish that failed half way and was not retried.
//
// The last two are re-checked and deleted under the topic lock, so an Earliest
// backfill or a publish retry on the topic cannot interleave with them.
// Undelivered backlog expires with the table TTL.
func (s *clickhouseStore) Purge(ctx context.Context, policy PurgePolicy, batch int) (int64, error) {
	var finished []struct {
		Topic string
		ID    string
	}
	if err := s.db.WithContext(ctx).Raw("SELECT topic, id FROM queue_deliveries FINAL WHERE status = ? OR (status = ? AND dead_at < "+chNowMicros+" - ?) LIMIT ?",
		chStatusAcked, statusDead, policy.DeadRetention.Microseconds(), batch).Scan(&finished).Error; err != nil {
		return 0, fmt.Errorf("purge scan: %w", err)
	}
	var topics, ids []string
	for _, r := range finished {
		ids = append(ids, r.ID)
		if !slices.Contains(topics, r.Topic) {
			topics = append(topics, r.Topic)
		}
	}
	if err := s.deleteIDs(ctx, "queue_deliveries", topics, ids); err != nil {
		return 0, err
	}
	n := int64(len(ids))

	unneeded := "id NOT IN (SELECT message_id FROM queue_deliveries FINAL WHERE topic = ? AND status IN ('pending', 'leased', 'dead'))"
	msgs, err := s.purgeByTopic(ctx, "queue_messages", "SELECT topic, id FROM queue_messages FINAL WHERE "+
		"greatest(toUnixTimestamp64Micro(created_at), deliver_at) < "+chNowMicros+" - ? AND "+
		"id NOT IN (SELECT message_id FROM queue_deliveries FINAL WHERE status IN ('pending', 'leased', 'dead')) LIMIT ?",
		[]any{policy.AckedRetention.Microseconds(), batch}, unneeded)
	n += msgs
	if err != nil {
		return n, err
	}

	landed := "message_id NOT IN (SELECT id FROM queue_messages FINAL WHERE topic = ?)"
	orphans, err := s.purgeByTopic(ctx, "queue_deliveries", "SELECT topic, id FROM queue_deliveries FINAL WHERE status = ? AND "+
		"created_at < now64(3) - toIntervalMicrosecond(?) AND message_id NOT IN (SELECT id FROM queue_messages FINAL) LIMIT ?",
		[]any{statusPending, s.abandoned.Microseconds(), batch}, landed)
	return n + orphans, err
}

// purgeByTopic finds candidate rows of table with scan, which returns topic
// and id, then per topic, under its lock, keeps the ids that still satisfy
// recheck (a condition on the topic, bound as its only argument) and deletes
// them.
func (s *clickhouseStore) purgeByTopic(ctx context.Context, table, scan string, args []any, recheck string) (int64, error) {
	var rows []struct {
		Topic string
		ID    string
	}
	if err := s.db.WithContext(ctx).Raw(scan, args...).Scan(&rows).Error; err != nil {
		return 0, fmt.Errorf("purge scan of %s: %w", table, err)
	}
	byTopic := map[string][]string{}
	for _, r := range rows {
		byTopic[r.Topic] = append(byTopic[r.Topic], r.ID)
	}
	var n int64
	for _, topic := range slices.Sorted(maps.Keys(byTopic)) {
		deleted, err := func() (int64, error) {
			held, release, err := s.lockTopics(ctx, []string{topic})
			if err != nil {
				return 0, err
			}
			defer release()
			var ids []string
			if err := s.db.WithContext(held).Raw("SELECT id FROM "+table+" FINAL WHERE topic = ? AND id IN ? AND "+recheck,
				topic, byTopic[topic], topic).Scan(&ids).Error; err != nil {
				return 0, fmt.Errorf("purge recheck of %s: %w", table, err)
			}
			return int64(len(ids)), s.deleteIDs(held, table, []string{topic}, ids)
		}()
		if err != nil {
			return n, err
		}
		n += deleted
	}
	return n, nil
}

// deleteIDs removes every version of each id, all on topics, from table.
func (s *clickhouseStore) deleteIDs(ctx context.Context, table string, topics, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := s.db.WithContext(ctx).Exec("DELETE FROM "+table+" WHERE topic IN ? AND id IN ? SETTINGS lightweight_deletes_sync = 1", topics, ids).Error; err != nil {
		return fmt.Errorf("purge delete from %s: %w", table, err)
	}
	return nil
}

func (s *clickhouseStore) Stats(ctx context.Context, topic, group string) (Stats, error) {
	live, liveArg := s.liveWhere()
	var rows []struct {
		Status string
		N      int64
	}
	if err := s.db.WithContext(ctx).Raw("SELECT status, count() AS n FROM queue_deliveries FINAL WHERE topic = ? AND group_name = ? AND "+live+" GROUP BY status",
		topic, group, liveArg).Scan(&rows).Error; err != nil {
		return Stats{}, fmt.Errorf("stats: %w", err)
	}
	var st Stats
	for _, r := range rows {
		switch r.Status {
		case statusPending:
			st.Pending = r.N
		case statusLeased:
			st.Leased = r.N
		case statusDead:
			st.Dead = r.N
		}
	}
	return st, nil
}

func (s *clickhouseStore) Ping(ctx context.Context) error {
	return s.db.WithContext(ctx).Exec("SELECT 1").Error
}

// Close does nothing: the database handle belongs to the logstore.
func (s *clickhouseStore) Close(context.Context) error { return nil }

var (
	_ Store          = (*clickhouseStore)(nil)
	_ ConsumeChecker = (*clickhouseStore)(nil)
)
