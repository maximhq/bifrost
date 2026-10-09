package queue

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// sqlChunk bounds rows per multi-row statement, well under SQLite's and
// Postgres's bind-parameter limits.
const sqlChunk = 500

// sqlDialect isolates what differs between the SQL backends: where "now"
// comes from, and the row and advisory locks a shared database needs.
type sqlDialect interface {
	// nowMicros writes the current time, in unix microseconds, into b.
	nowMicros(b *sqlBuilder)
	// claimLock is appended to the claim's candidate subquery.
	claimLock() string
	// lockTopics locks topic rows in tx: shared for publishing, exclusive for
	// registering a group.
	lockTopics(tx *gorm.DB, topics []string, exclusive bool) error
	// migrate runs fn under whatever cross-instance lock migrations need.
	migrate(ctx context.Context, db *gorm.DB, fn func() error) error
	// purgeLock takes the janitor lock for the rest of tx and reports whether
	// this instance got it.
	purgeLock(tx *gorm.DB) (bool, error)
	// lockKeys serialises, for the rest of tx, every change to which delivery
	// heads the given key lanes, so concurrent publishes, acknowledgements and
	// dead-lettering cannot leave a lane with two heads or none.
	lockKeys(tx *gorm.DB, keys []laneKey) error
	// lockGroup serialises capped claims of one consumer group for the rest
	// of tx, so concurrent claimers cannot both see the same headroom.
	lockGroup(tx *gorm.DB, topic, group string) error
	// headroom writes max(0, min(max, limit - inFlight)) for integer
	// expressions written by the callbacks.
	headroom(b *sqlBuilder, max, limit, inFlight func())
}

// laneKey is one key's ordering lane within a consumer group: at most one of
// its deliveries is pending or leased, the rest wait behind it.
type laneKey struct{ topic, group, key string }

// sqliteDialect: a single process owns the database file, and SQLite's single
// writer replaces row locks.
type sqliteDialect struct{}

// nowMicros reads SQLite's clock as the statement runs, which is after it
// holds the write lock: a time bound before a long lock wait would hand out
// leases that have already expired.
func (sqliteDialect) nowMicros(b *sqlBuilder) {
	b.add("CAST((julianday('now') - 2440587.5) * 86400000000.0 AS INTEGER)")
}
func (sqliteDialect) claimLock() string { return "" }
func (sqliteDialect) lockTopics(*gorm.DB, []string, bool) error {
	return nil
}
func (sqliteDialect) migrate(_ context.Context, _ *gorm.DB, fn func() error) error { return fn() }
func (sqliteDialect) purgeLock(*gorm.DB) (bool, error)                             { return true, nil }
func (sqliteDialect) lockGroup(*gorm.DB, string, string) error                     { return nil }
func (sqliteDialect) headroom(b *sqlBuilder, max, limit, inFlight func()) {
	b.add("max(0, min(")
	max()
	b.add(", ")
	limit()
	b.add(" - ")
	inFlight()
	b.add("))")
}
func (sqliteDialect) lockKeys(*gorm.DB, []laneKey) error { return nil }

// sqlBuilder accumulates SQL text and its positional arguments together, so a
// fragment like "now" can carry its own bind values.
type sqlBuilder struct {
	sb   strings.Builder
	args []any
}

func (b *sqlBuilder) add(sql string, args ...any) *sqlBuilder {
	b.sb.WriteString(sql)
	b.args = append(b.args, args...)
	return b
}

func (b *sqlBuilder) String() string { return b.sb.String() }

// sqlStore is the Store for SQLite and Postgres. It borrows its *gorm.DB and
// never closes it.
type sqlStore struct {
	db      *gorm.DB
	dialect sqlDialect
	now     func() time.Time // stamps created_at; leases use the dialect clock
	logger  schemas.Logger
}

// newSQLStore migrates the queue tables into db and returns a store over it.
func newSQLStore(ctx context.Context, db *gorm.DB, logger schemas.Logger) (*sqlStore, error) {
	var d sqlDialect
	switch name := db.Dialector.Name(); name {
	case "sqlite":
		d = sqliteDialect{}
	default:
		return nil, fmt.Errorf("%w: sql dialect %q", ErrUnsupported, name)
	}
	s := &sqlStore{db: db, dialect: d, now: time.Now, logger: logger}
	if err := d.migrate(ctx, db, func() error { return runSQLMigrations(ctx, db, logger) }); err != nil {
		return nil, err
	}
	return s, nil
}

// stamp is a created_at value: UTC at microsecond precision, so SQLite's
// string comparisons and Postgres's timestamp precision agree.
func (s *sqlStore) stamp() time.Time {
	return s.now().UTC().Truncate(time.Microsecond)
}

func (s *sqlStore) EnsureGroup(ctx context.Context, topic, group string, startFrom StartFrom) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.ensureTopics(tx, []string{topic}); err != nil {
			return err
		}
		if err := s.dialect.lockTopics(tx, []string{topic}, true); err != nil {
			return err
		}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&tableQueueGroup{Topic: topic, GroupName: group, CreatedAt: s.stamp()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 || startFrom != StartFromEarliest {
			return nil // existing group, or nothing to backfill
		}
		return s.backfill(tx, topic, group)
	})
}

// backfill creates group's deliveries for every message retained on topic,
// in publish order. The caller holds the topic lock, so no publish can slip
// between the backfill and the group becoming visible.
func (s *sqlStore) backfill(tx *gorm.DB, topic, group string) error {
	var after int64
	for {
		var msgs []tableQueueMessage
		if err := tx.Select("seq", "id", "topic", "msg_key", "deliver_at").
			Where("topic = ? AND seq > ?", topic, after).
			Order("seq").Limit(sqlChunk).Find(&msgs).Error; err != nil {
			return fmt.Errorf("backfill scan: %w", err)
		}
		if len(msgs) == 0 {
			return nil
		}
		dels := make([]tableQueueDelivery, 0, len(msgs))
		for _, m := range msgs {
			dels = append(dels, s.newDelivery(m.ID, topic, group, m.MsgKey, m.DeliverAt))
		}
		// No key locks needed: the topic lock keeps every other writer away
		// from a group that does not exist yet.
		if err := s.queueBehindLiveHeads(tx, dels); err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&dels).Error; err != nil {
			return fmt.Errorf("backfill insert: %w", err)
		}
		after = msgs[len(msgs)-1].Seq
	}
}

// newDelivery builds a pending delivery that first becomes due at deliverAt
// (unix µs); 0 means at once.
func (s *sqlStore) newDelivery(messageID, topic, group string, key *string, deliverAt int64) tableQueueDelivery {
	return tableQueueDelivery{
		ID:            DeliveryID(messageID, group),
		MessageID:     messageID,
		Topic:         topic,
		GroupName:     group,
		MsgKey:        key,
		Status:        statusPending,
		DeliverAt:     deliverAt,
		NextAttemptAt: deliverAt,
		CreatedAt:     s.stamp(),
	}
}

// deliverAtMicros is a message's schedule as stored: unix µs, 0 when unset.
func deliverAtMicros(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMicro()
}

// ensureTopics inserts any missing topic rows, in bounded statements.
func (s *sqlStore) ensureTopics(tx *gorm.DB, topics []string) error {
	rows := make([]tableQueueTopic, len(topics))
	now := s.stamp()
	for i, t := range topics {
		rows[i] = tableQueueTopic{Topic: t, CreatedAt: now}
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&rows, sqlChunk).Error
}

func (s *sqlStore) Append(ctx context.Context, msgs []*Message) error {
	if len(msgs) == 0 {
		return nil
	}
	// First occurrence wins, like ON CONFLICT DO NOTHING.
	seen := make(map[string]bool, len(msgs))
	unique := make([]*Message, 0, len(msgs))
	var topics []string
	for _, m := range msgs {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		unique = append(unique, m)
		if !slices.Contains(topics, m.Topic) {
			topics = append(topics, m.Topic)
		}
	}
	slices.Sort(topics) // consistent lock order across publishers

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.ensureTopics(tx, topics); err != nil {
			return err
		}
		// Groups are read only after the topic lock is held, so a group
		// registered concurrently either sees this publish in its backfill or
		// is seen here.
		// Chunks keep each statement within the bind-parameter limit; the
		// topics are sorted, so the locks are still taken in one global order.
		var groups []tableQueueGroup
		for chunk := range slices.Chunk(topics, sqlChunk) {
			if err := s.dialect.lockTopics(tx, chunk, false); err != nil {
				return err
			}
		}
		for chunk := range slices.Chunk(topics, sqlChunk) {
			var found []tableQueueGroup
			if err := tx.Where("topic IN ?", chunk).Order("group_name").Find(&found).Error; err != nil {
				return fmt.Errorf("load groups: %w", err)
			}
			groups = append(groups, found...)
		}
		inserted, err := s.insertMessages(tx, unique)
		if err != nil {
			return err
		}
		var dels []tableQueueDelivery
		for _, m := range unique {
			if !inserted[m.ID] {
				continue // already published; its deliveries keep their state
			}
			for _, g := range groups {
				if g.Topic == m.Topic {
					dels = append(dels, s.newDelivery(m.ID, m.Topic, g.GroupName, optionalString(m.Key), deliverAtMicros(m.DeliverAt)))
				}
			}
		}
		if len(dels) == 0 {
			return nil
		}
		if err := s.dialect.lockKeys(tx, lanesOf(dels)); err != nil {
			return err
		}
		if err := s.queueBehindLiveHeads(tx, dels); err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&dels, sqlChunk).Error
	})
}

// lanesOf returns the distinct key lanes of dels, sorted.
func lanesOf(dels []tableQueueDelivery) []laneKey {
	seen := map[laneKey]bool{}
	var lanes []laneKey
	for _, d := range dels {
		if d.MsgKey == nil {
			continue
		}
		k := laneKey{d.Topic, d.GroupName, *d.MsgKey}
		if !seen[k] {
			seen[k] = true
			lanes = append(lanes, k)
		}
	}
	slices.SortFunc(lanes, func(a, b laneKey) int {
		return strings.Compare(a.topic+"\x00"+a.group+"\x00"+a.key, b.topic+"\x00"+b.group+"\x00"+b.key)
	})
	return lanes
}

// queueBehindLiveHeads marks each keyed delivery in dels as waiting when its
// lane already has a live delivery, or an earlier one in dels: only a lane's
// first live delivery is pending, so a claim never has to look past it.
func (s *sqlStore) queueBehindLiveHeads(tx *gorm.DB, dels []tableQueueDelivery) error {
	lanes := lanesOf(dels)
	if len(lanes) == 0 {
		return nil
	}
	busy := make(map[laneKey]bool, len(lanes))
	for chunk := range slices.Chunk(lanes, sqlChunk/3) {
		tuples := make([][]any, len(chunk))
		for i, l := range chunk {
			tuples[i] = []any{l.topic, l.group, l.key}
		}
		var rows []struct {
			Topic     string
			GroupName string
			MsgKey    string
		}
		if err := tx.Raw("SELECT DISTINCT topic, group_name, msg_key FROM queue_deliveries "+
			"WHERE (topic, group_name, msg_key) IN ? AND status <> 'dead'", tuples).Scan(&rows).Error; err != nil {
			return fmt.Errorf("find live key heads: %w", err)
		}
		for _, r := range rows {
			busy[laneKey{r.Topic, r.GroupName, r.MsgKey}] = true
		}
	}
	for i := range dels {
		if dels[i].MsgKey == nil {
			continue
		}
		k := laneKey{dels[i].Topic, dels[i].GroupName, *dels[i].MsgKey}
		if busy[k] {
			dels[i].Status = statusWaiting
		}
		busy[k] = true
	}
	return nil
}

// promoteNext makes the oldest waiting delivery of lane due, unless the lane
// already has a head. The caller holds the lane's lock.
func (s *sqlStore) promoteNext(tx *gorm.DB, lane laneKey) error {
	var b sqlBuilder
	// A promoted delivery still waits for its own DeliverAt.
	b.add("UPDATE queue_deliveries SET status = 'pending', next_attempt_at = CASE WHEN deliver_at > ")
	s.dialect.nowMicros(&b)
	b.add(" THEN deliver_at ELSE ")
	s.dialect.nowMicros(&b)
	b.add(" END WHERE seq = (SELECT MIN(seq) FROM queue_deliveries WHERE topic = ? AND group_name = ? AND msg_key = ? "+
		"AND status <> 'dead' AND status = 'waiting') ", lane.topic, lane.group, lane.key)
	b.add("AND NOT EXISTS (SELECT 1 FROM queue_deliveries WHERE topic = ? AND group_name = ? AND msg_key = ? "+
		"AND status <> 'dead' AND status <> 'waiting')", lane.topic, lane.group, lane.key)
	if err := tx.Exec(b.String(), b.args...).Error; err != nil {
		return fmt.Errorf("promote next delivery of key %q: %w", lane.key, err)
	}
	return nil
}

// claimLanes returns the distinct key lanes of claimed deliveries, sorted.
func claimLanes(cs []*Claimed) []laneKey {
	dels := make([]tableQueueDelivery, 0, len(cs))
	for _, c := range cs {
		if c.Message != nil {
			dels = append(dels, tableQueueDelivery{Topic: c.Message.Topic, GroupName: c.Group, MsgKey: optionalString(c.Message.Key)})
		}
	}
	return lanesOf(dels)
}

// insertMessages inserts msgs and returns the IDs that were new. Only those
// get deliveries: re-publishing a message whose delivery was already
// acknowledged (and deleted) must not deliver it again.
func (s *sqlStore) insertMessages(tx *gorm.DB, msgs []*Message) (map[string]bool, error) {
	inserted := make(map[string]bool, len(msgs))
	now := s.stamp()
	for chunk := range slices.Chunk(msgs, sqlChunk) {
		var b sqlBuilder
		b.add("INSERT INTO queue_messages (id, topic, msg_key, payload, headers, published_at, deliver_at, created_at) VALUES ")
		for i, m := range chunk {
			headers := "{}"
			if len(m.Headers) > 0 {
				raw, err := sonic.MarshalString(m.Headers)
				if err != nil {
					return nil, fmt.Errorf("encode headers of message %s: %w", m.ID, err)
				}
				headers = raw
			}
			if i > 0 {
				b.add(", ")
			}
			b.add("(?, ?, ?, ?, ?, ?, ?, ?)", m.ID, m.Topic, optionalString(m.Key), m.Payload, headers,
				m.PublishedAt.UTC().Truncate(time.Microsecond), deliverAtMicros(m.DeliverAt), now)
		}
		b.add(" ON CONFLICT (id) DO NOTHING RETURNING id")
		var ids []string
		if err := tx.Raw(b.String(), b.args...).Scan(&ids).Error; err != nil {
			return nil, fmt.Errorf("insert messages: %w", err)
		}
		for _, id := range ids {
			inserted[id] = true
		}
	}
	return inserted, nil
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// claimedRow is what the claim's RETURNING clause yields.
type claimedRow struct {
	Seq        int64
	ID         string
	MessageID  string
	MsgKey     *string
	Attempts   int
	ClaimToken string
}

func (s *sqlStore) Claim(ctx context.Context, req ClaimRequest) ([]*Claimed, error) {
	topic, group := req.Topic, req.Group
	if req.Max <= 0 {
		return nil, nil
	}
	// Each delivery gets its own token: the batch prefix plus the delivery ID.
	batch := uuid.NewString() + ":"
	var b sqlBuilder
	b.add("UPDATE queue_deliveries SET status = 'leased', claimed_by = ?, claim_token = ? || id, claimed_until = ", req.RunnerID, batch)
	s.dialect.nowMicros(&b)
	b.add(" + ?, ", req.Lease.Microseconds())
	// A lease that expired unfinished means its holder died mid-attempt.
	b.add("attempts = attempts + CASE WHEN status = 'leased' THEN 1 ELSE 0 END ")
	// Waiting deliveries are never heads and pending or leased ones always
	// are, so no per-row key check is needed. The status terms repeat the
	// ready index's predicate so SQLite and Postgres both use it.
	b.add("WHERE seq IN (SELECT d.seq FROM queue_deliveries d WHERE d.topic = ? AND d.group_name = ? ", topic, group)
	b.add("AND d.status <> 'dead' AND d.status <> 'waiting' ")
	b.add("AND ((d.status = 'pending' AND d.next_attempt_at <= ")
	s.dialect.nowMicros(&b)
	b.add(") OR (d.status = 'leased' AND d.claimed_until < ")
	s.dialect.nowMicros(&b)
	b.add(")) ")
	b.add("ORDER BY d.seq LIMIT ")
	if req.MaxInFlight > 0 {
		// Only the headroom under the cap: unexpired leases of the group count.
		s.dialect.headroom(&b,
			func() { b.add("?", req.Max) },
			func() { b.add("?", req.MaxInFlight) },
			func() {
				b.add("(SELECT COUNT(*) FROM queue_deliveries l WHERE l.topic = ? AND l.group_name = ? "+
					"AND l.status <> 'dead' AND l.status <> 'waiting' AND l.status = 'leased' AND l.claimed_until >= ", topic, group)
				s.dialect.nowMicros(&b)
				b.add(")")
			})
	} else {
		b.add("?", req.Max)
	}
	if lock := s.dialect.claimLock(); lock != "" {
		b.add(" " + lock)
	}
	b.add(") RETURNING seq, id, message_id, msg_key, attempts, claim_token")

	var rows []claimedRow
	claim := func(db *gorm.DB) error {
		return db.Raw(b.String(), b.args...).Scan(&rows).Error
	}
	var err error
	if req.MaxInFlight > 0 {
		err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := s.dialect.lockGroup(tx, topic, group); err != nil {
				return err
			}
			return claim(tx)
		})
	} else {
		err = claim(s.db.WithContext(ctx))
	}
	if err != nil {
		return nil, fmt.Errorf("claim: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Seq < rows[j].Seq })

	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.MessageID
	}
	msgs, err := s.loadMessages(ctx, ids)
	if err != nil {
		// The leases expire on their own; nothing is lost.
		return nil, err
	}
	out := make([]*Claimed, 0, len(rows))
	var orphaned []*Claimed
	for _, r := range rows {
		c := &Claimed{DeliveryID: r.ID, ClaimToken: r.ClaimToken, Group: group, Attempt: r.Attempts + 1}
		if m, ok := msgs[r.MessageID]; ok {
			c.Message = m
			out = append(out, c)
		} else {
			// Its key still matters: dead-lettering it promotes the next
			// delivery of its lane.
			c.Message = &Message{ID: r.MessageID, Topic: topic}
			if r.MsgKey != nil {
				c.Message.Key = *r.MsgKey
			}
			orphaned = append(orphaned, c)
		}
	}
	for _, c := range orphaned {
		// Unreachable unless a message row was removed by hand.
		if _, err := s.Kill(ctx, c, "queue: message row missing"); err != nil && s.logger != nil {
			s.logger.Warn("queue: failed to dead-letter orphaned delivery %s: %v", c.DeliveryID, err)
		}
	}
	return out, nil
}

// messageRow is a queue_messages row as read back for a claim.
type messageRow struct {
	DeliverAt   int64
	ID          string
	Topic       string
	MsgKey      *string
	Payload     []byte
	Headers     string
	PublishedAt time.Time
}

func (s *sqlStore) loadMessages(ctx context.Context, ids []string) (map[string]*Message, error) {
	out := make(map[string]*Message, len(ids))
	for chunk := range slices.Chunk(ids, sqlChunk) {
		var rows []messageRow
		if err := s.db.WithContext(ctx).Table("queue_messages").
			Select("id", "topic", "msg_key", "payload", "headers", "published_at", "deliver_at").
			Where("id IN ?", chunk).Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("load messages: %w", err)
		}
		for _, r := range rows {
			m := &Message{ID: r.ID, Topic: r.Topic, Payload: r.Payload, PublishedAt: r.PublishedAt.UTC()}
			if r.DeliverAt > 0 {
				m.DeliverAt = time.UnixMicro(r.DeliverAt).UTC()
			}
			if r.MsgKey != nil {
				m.Key = *r.MsgKey
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

// heldWhere is the fence every post-claim write applies.
const heldWhere = "id = ? AND claim_token = ? AND status = 'leased'"

// Ack deletes the held deliveries in one transaction and promotes the next
// waiting delivery of every key lane it finished.
func (s *sqlStore) Ack(ctx context.Context, cs []*Claimed) ([]string, error) {
	if len(cs) == 0 {
		return nil, nil
	}
	var held []string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		held = held[:0]
		if err := s.dialect.lockKeys(tx, claimLanes(cs)); err != nil {
			return err
		}
		var finished []laneKey
		for chunk := range slices.Chunk(cs, sqlChunk) {
			tokens := make([]string, len(chunk))
			for i, c := range chunk {
				tokens[i] = c.ClaimToken
			}
			var rows []struct {
				ID        string
				Topic     string
				GroupName string
				MsgKey    *string
			}
			if err := tx.Raw("DELETE FROM queue_deliveries WHERE claim_token IN ? AND status = 'leased' "+
				"RETURNING id, topic, group_name, msg_key", tokens).Scan(&rows).Error; err != nil {
				return fmt.Errorf("ack: %w", err)
			}
			for _, r := range rows {
				held = append(held, r.ID)
				if r.MsgKey != nil {
					finished = append(finished, laneKey{r.Topic, r.GroupName, *r.MsgKey})
				}
			}
		}
		for _, lane := range finished {
			if err := s.promoteNext(tx, lane); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return held, nil
}

func (s *sqlStore) Retry(ctx context.Context, c *Claimed, backoff time.Duration, lastErr string) (bool, error) {
	var b sqlBuilder
	b.add("UPDATE queue_deliveries SET status = 'pending', attempts = attempts + 1, next_attempt_at = ")
	s.dialect.nowMicros(&b)
	b.add(" + ?, last_error = ?, claimed_by = NULL, claim_token = NULL, claimed_until = NULL WHERE "+heldWhere,
		backoff.Microseconds(), lastErr, c.DeliveryID, c.ClaimToken)
	res := s.db.WithContext(ctx).Exec(b.String(), b.args...)
	return res.RowsAffected == 1, res.Error
}

// Kill dead-letters the delivery and promotes the next waiting delivery of
// its key lane: a dead head does not block its key.
func (s *sqlStore) Kill(ctx context.Context, c *Claimed, lastErr string) (bool, error) {
	var held bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lanes := claimLanes([]*Claimed{c})
		if err := s.dialect.lockKeys(tx, lanes); err != nil {
			return err
		}
		// dead_at starts the dead retention: a delivery may have lived long
		// before it died.
		var b sqlBuilder
		b.add("UPDATE queue_deliveries SET status = 'dead', dead_at = ")
		s.dialect.nowMicros(&b)
		b.add(", attempts = attempts + 1, last_error = ?, "+
			"claimed_by = NULL, claim_token = NULL, claimed_until = NULL WHERE "+heldWhere, lastErr, c.DeliveryID, c.ClaimToken)
		res := tx.Exec(b.String(), b.args...)
		if res.Error != nil {
			return res.Error
		}
		held = res.RowsAffected == 1
		if held && len(lanes) == 1 {
			return s.promoteNext(tx, lanes[0])
		}
		return nil
	})
	return held, err
}

func (s *sqlStore) Extend(ctx context.Context, cs []*Claimed, lease time.Duration) ([]string, error) {
	held := make([]string, 0, len(cs))
	for chunk := range slices.Chunk(cs, sqlChunk) {
		tokens := make([]string, len(chunk))
		for i, c := range chunk {
			tokens[i] = c.ClaimToken
		}
		var b sqlBuilder
		b.add("UPDATE queue_deliveries SET claimed_until = ")
		s.dialect.nowMicros(&b)
		// An expired lease is not renewed: someone else may be entitled to it.
		b.add(" + ? WHERE claim_token IN ? AND status = 'leased' AND claimed_until >= ", lease.Microseconds(), tokens)
		s.dialect.nowMicros(&b)
		b.add(" RETURNING id")
		var ids []string
		if err := s.db.WithContext(ctx).Raw(b.String(), b.args...).Scan(&ids).Error; err != nil {
			return nil, fmt.Errorf("extend: %w", err)
		}
		held = append(held, ids...)
	}
	return held, nil
}

func (s *sqlStore) Release(ctx context.Context, cs []*Claimed) error {
	for chunk := range slices.Chunk(cs, sqlChunk) {
		tokens := make([]string, len(chunk))
		for i, c := range chunk {
			tokens[i] = c.ClaimToken
		}
		var b sqlBuilder
		b.add("UPDATE queue_deliveries SET status = 'pending', next_attempt_at = ")
		s.dialect.nowMicros(&b)
		b.add(", claimed_by = NULL, claim_token = NULL, claimed_until = NULL WHERE claim_token IN ? AND status = 'leased'", tokens)
		if err := s.db.WithContext(ctx).Exec(b.String(), b.args...).Error; err != nil {
			return fmt.Errorf("release: %w", err)
		}
	}
	return nil
}

func (s *sqlStore) Purge(ctx context.Context, policy PurgePolicy, batch int) (int64, error) {
	now := s.stamp()
	var removed int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ok, err := s.dialect.purgeLock(tx)
		if err != nil || !ok {
			return err // another instance is purging
		}
		var dead sqlBuilder
		dead.add("DELETE FROM queue_deliveries WHERE seq IN (SELECT seq FROM queue_deliveries WHERE status = 'dead' AND dead_at < ")
		s.dialect.nowMicros(&dead)
		dead.add(" - ? ORDER BY seq LIMIT ?)", policy.DeadRetention.Microseconds(), batch)
		res := tx.Exec(dead.String(), dead.args...)
		if res.Error != nil {
			return fmt.Errorf("purge dead deliveries: %w", res.Error)
		}
		removed += res.RowsAffected
		// A message is finished with once no delivery refers to it: every group
		// acknowledged it (acknowledged deliveries are deleted), its dead
		// deliveries were purged, or it had no groups. It stays for
		// AckedRetention past the time it became due, so a late Earliest group
		// can still replay it.
		ackedCutoff := now.Add(-policy.AckedRetention)
		res = tx.Exec("DELETE FROM queue_messages WHERE seq IN (SELECT m.seq FROM queue_messages m "+
			"WHERE m.created_at < ? AND m.deliver_at < ? AND NOT EXISTS (SELECT 1 FROM queue_deliveries d "+
			"WHERE d.message_id = m.id) ORDER BY m.seq LIMIT ?)", ackedCutoff, ackedCutoff.UnixMicro(), batch)
		if res.Error != nil {
			return fmt.Errorf("purge messages: %w", res.Error)
		}
		removed += res.RowsAffected
		return nil
	})
	return removed, err
}

func (s *sqlStore) Stats(ctx context.Context, topic, group string) (Stats, error) {
	var rows []struct {
		Status string
		N      int64
	}
	if err := s.db.WithContext(ctx).Table("queue_deliveries").Select("status, COUNT(*) AS n").
		Where("topic = ? AND group_name = ?", topic, group).Group("status").Scan(&rows).Error; err != nil {
		return Stats{}, fmt.Errorf("stats: %w", err)
	}
	var st Stats
	for _, r := range rows {
		switch r.Status {
		case statusPending, statusWaiting:
			st.Pending += r.N
		case statusLeased:
			st.Leased = r.N
		case statusDead:
			st.Dead = r.N
		}
	}
	return st, nil
}

func (s *sqlStore) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// Close does nothing: the database handle belongs to the logstore.
func (s *sqlStore) Close(context.Context) error { return nil }

var _ Store = (*sqlStore)(nil)
