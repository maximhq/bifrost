package queue

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// openTestSQLite opens a file database with the logstore's SQLite settings.
func openTestSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue.db")
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=60000&_foreign_keys=1", path)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func newTestSQLiteStore(t *testing.T) *sqlStore {
	t.Helper()
	s, err := newSQLStore(context.Background(), openTestSQLite(t), nil)
	require.NoError(t, err)
	return s
}

func TestSQLiteStoreContract(t *testing.T) {
	runStoreContract(t, func(t *testing.T) Store { return newTestSQLiteStore(t) })
}

func TestSQLiteMigrationsAreIdempotent(t *testing.T) {
	db := openTestSQLite(t)
	ctx := context.Background()
	_, err := newSQLStore(ctx, db, nil)
	require.NoError(t, err)
	_, err = newSQLStore(ctx, db, nil)
	require.NoError(t, err)

	for _, m := range sqlMigrations {
		assert.True(t, db.Migrator().HasTable(m.model), m.id)
		for _, idx := range m.indexes {
			assert.True(t, db.Migrator().HasIndex(m.model, idx), "%s %s", m.id, idx)
		}
	}
	var ids []string
	require.NoError(t, db.Table("migrations").Where("id LIKE ?", "queue_%").Order("id").Pluck("id", &ids).Error)
	assert.Equal(t, []string{"queue_deliveries_init", "queue_groups_init", "queue_messages_init", "queue_topics_init"}, ids)
}

// TestSQLiteRepublishAfterAckDoesNotRedeliver pins the reason Append only
// fans out messages it newly inserted: acknowledged deliveries are deleted,
// so re-inserting deliveries for a known message would resurrect them.
func TestSQLiteRepublishAfterAckDoesNotRedeliver(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()
	mustGroup(t, s, "t", "g", StartFromLatest)
	m := newMsg("t", "", "x")
	mustAppend(t, s, m)
	cs := mustClaim(t, s, "t", "g", 1, longLease)
	require.Len(t, cs, 1)
	held, err := ackOne(ctx, s, cs[0])
	require.NoError(t, err)
	require.True(t, held)

	var n int64
	require.NoError(t, s.db.Table("queue_deliveries").Count(&n).Error)
	require.Zero(t, n, "acknowledged deliveries are deleted")

	mustAppend(t, s, m)
	require.NoError(t, s.db.Table("queue_deliveries").Count(&n).Error)
	assert.Zero(t, n)
}

func TestSQLiteMultiInstance(t *testing.T) {
	db := openTestSQLite(t)
	runMultiInstanceSimulation(t, func(t *testing.T, logger schemas.Logger) Queue {
		s, err := newSQLStore(context.Background(), db, nil)
		require.NoError(t, err)
		q, err := NewStoreQueue(s, EngineConfig{}, logger)
		require.NoError(t, err)
		return q
	}, 3, 200)
}

// sqlPurgeRemovesDeadAndOrphans pins what the SQL janitor deletes: dead
// deliveries and messages no live delivery needs, past retention.
func sqlPurgeRemovesDeadAndOrphans(t *testing.T, s *sqlStore) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	mustAppend(t, s, newMsg(topic, "", "dead"))
	cs := mustClaim(t, s, topic, "g", 1, longLease)
	require.Len(t, cs, 1)
	held, err := s.Kill(ctx, cs[0], "boom")
	require.NoError(t, err)
	require.True(t, held)
	orphan := newMsg("nogroups"+topic, "", "orphan")
	mustAppend(t, s, orphan)

	time.Sleep(20 * time.Millisecond)
	n, err := s.Purge(ctx, PurgePolicy{DeadRetention: time.Millisecond, AckedRetention: time.Millisecond}, 100)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, n, int64(3), "dead delivery, its message, and the orphan")
	st, err := s.Stats(ctx, topic, "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{}, st)
	var left int64
	require.NoError(t, s.db.Table("queue_messages").Where("id = ?", orphan.ID).Count(&left).Error)
	assert.Zero(t, left)
}

func TestSQLitePurgeRemovesDeadAndOrphans(t *testing.T) {
	sqlPurgeRemovesDeadAndOrphans(t, newTestSQLiteStore(t))
}

// sqlPurgeInBatches kills more deliveries than one batch and checks each
// Purge call stays within its batch while repeated calls remove everything.
func sqlPurgeInBatches(t *testing.T, s *sqlStore) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	const dead, batch = 7, 2
	for i := range dead {
		mustAppend(t, s, newMsg(topic, "", fmt.Sprint(i)))
	}
	cs := mustClaim(t, s, topic, "g", dead, longLease)
	require.Len(t, cs, dead)
	for _, c := range cs {
		ok, err := s.Kill(ctx, c, "boom")
		require.NoError(t, err)
		require.True(t, ok)
	}
	time.Sleep(20 * time.Millisecond)

	var total int64
	for range 20 {
		n, err := s.Purge(ctx, PurgePolicy{DeadRetention: time.Millisecond, AckedRetention: time.Millisecond}, batch)
		require.NoError(t, err)
		// One call deletes up to batch dead deliveries and batch messages.
		assert.LessOrEqual(t, n, int64(2*batch))
		total += n
		if n == 0 {
			break
		}
	}
	assert.Equal(t, int64(2*dead), total, "every dead delivery and its message")
	st, err := s.Stats(ctx, topic, "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{}, st)
}

func TestSQLitePurgeInBatches(t *testing.T) {
	sqlPurgeInBatches(t, newTestSQLiteStore(t))
}

// sqlClaimDeadLettersOrphanedDelivery removes a message row behind a pending
// delivery, which only manual tampering can do: the claim must dead-letter
// the delivery instead of handing out a delivery with no message.
func sqlClaimDeadLettersOrphanedDelivery(t *testing.T, s *sqlStore) {
	ctx := context.Background()
	topic := uniqueTopic(t)
	mustGroup(t, s, topic, "g", StartFromLatest)
	orphan, kept := newMsg(topic, "", "orphan"), newMsg(topic, "", "kept")
	mustAppend(t, s, orphan, kept)
	require.NoError(t, s.db.Exec("DELETE FROM queue_messages WHERE id = ?", orphan.ID).Error)

	cs := mustClaim(t, s, topic, "g", 10, longLease)
	assert.Equal(t, []string{"kept"}, payloads(cs))
	st, err := s.Stats(ctx, topic, "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{Leased: 1, Dead: 1}, st)

	// A keyed orphan is a lane head: dead-lettering it promotes the next one.
	keyed := uniqueTopic(t)
	mustGroup(t, s, keyed, "g", StartFromLatest)
	head, next := newMsg(keyed, "k", "head"), newMsg(keyed, "k", "next")
	mustAppend(t, s, head)
	mustAppend(t, s, next)
	require.NoError(t, s.db.Exec("DELETE FROM queue_messages WHERE id = ?", head.ID).Error)
	assert.Empty(t, mustClaim(t, s, keyed, "g", 10, longLease), "the orphaned head is dead-lettered")
	assert.Equal(t, []string{"next"}, payloads(mustClaim(t, s, keyed, "g", 10, longLease)), "and its successor promoted")
}

func TestSQLiteClaimDeadLettersOrphanedDelivery(t *testing.T) {
	sqlClaimDeadLettersOrphanedDelivery(t, newTestSQLiteStore(t))
}

// TestSQLiteLeaseStartsAfterTheWriteLock holds SQLite's write lock for
// longer than a claim's lease while the claim waits for it: the lease must
// run from when the claim actually executed, not from when it was built, or
// the delivery is handed out already expired and can be claimed again.
func TestSQLiteLeaseStartsAfterTheWriteLock(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()
	mustGroup(t, s, "lock", "g", StartFromLatest)
	mustAppend(t, s, newMsg("lock", "", "x"))

	sqlDB, err := s.db.DB()
	require.NoError(t, err)
	holder, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	defer holder.Close()
	_, err = holder.ExecContext(ctx, "BEGIN IMMEDIATE")
	require.NoError(t, err)

	const lease = 300 * time.Millisecond
	claimed := make(chan []*Claimed, 1)
	go func() {
		cs, err := s.Claim(ctx, ClaimRequest{Topic: "lock", Group: "g", RunnerID: "first", Max: 1, Lease: lease})
		assert.NoError(t, err)
		claimed <- cs
	}()
	time.Sleep(2 * lease) // the claim is waiting for the write lock
	_, err = holder.ExecContext(ctx, "COMMIT")
	require.NoError(t, err)
	require.Len(t, <-claimed, 1)

	again, err := s.Claim(ctx, ClaimRequest{Topic: "lock", Group: "g", RunnerID: "second", Max: 1, Lease: lease})
	require.NoError(t, err)
	assert.Empty(t, again, "the first claim's lease was already expired when it was handed out")
}

// TestSQLiteAppendManyDistinctTopics publishes one batch across more distinct
// topics than one statement can carry bind parameters for.
func TestSQLiteAppendManyDistinctTopics(t *testing.T) {
	s := newTestSQLiteStore(t)
	const topics = 20_000
	msgs := make([]*Message, topics)
	for i := range msgs {
		msgs[i] = newMsg(fmt.Sprintf("topic-%05d", i), "", "x")
	}
	mustGroup(t, s, "topic-00042", "g", StartFromLatest)
	require.NoError(t, s.Append(context.Background(), msgs))
	assert.Len(t, mustClaim(t, s, "topic-00042", "g", 10, longLease), 1)
}
