package queue

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// openTestSQLite opens a file database with the logstore's SQLite settings.
func openTestSQLite(t testing.TB) *gorm.DB {
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

func newTestSQLiteStore(t testing.TB) *sqlStore {
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

// pgTestSchema keeps this package's tables apart from other test packages
// sharing the tests/docker-compose.yml Postgres database.
const pgTestSchema = "queue_test"

const pgTestDSN = "host=localhost user=bifrost password=bifrost_password dbname=bifrost port=5432 sslmode=disable search_path=" + pgTestSchema

// openTestPostgres opens a new connection pool to the test schema, or skips
// the test when Postgres is not running. Each call is a separate pool, which
// is how separate Bifrost instances look to the database.
func openTestPostgres(t testing.TB) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.Open(pgTestDSN), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := sqlDB.Ping(); err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	require.NoError(t, db.Exec("CREATE SCHEMA IF NOT EXISTS "+pgTestSchema).Error)
	return db
}

// closeTestDB closes db's pool now rather than when the test ends.
func closeTestDB(db *gorm.DB) {
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

// resetTestPostgres drops the queue tables and their ledger rows so the next
// store runs its migrations from scratch.
func resetTestPostgres(t testing.TB) *gorm.DB {
	t.Helper()
	db := openTestPostgres(t)
	for _, table := range []string{"queue_deliveries", "queue_messages", "queue_groups", "queue_topics"} {
		require.NoError(t, db.Exec("DROP TABLE IF EXISTS "+table).Error)
	}
	if db.Migrator().HasTable("migrations") {
		require.NoError(t, db.Exec("DELETE FROM migrations WHERE id LIKE 'queue\\_%'").Error)
	}
	return db
}

func TestPostgresStoreContract(t *testing.T) {
	db := resetTestPostgres(t)
	runStoreContract(t, func(t *testing.T) Store {
		s, err := newSQLStore(context.Background(), db, nil)
		require.NoError(t, err)
		return s
	})
}

// TestPostgresConcurrentStartup starts several instances against an empty
// schema at once: the migration advisory lock must let exactly one create the
// tables while the rest wait and then find them.
func TestPostgresConcurrentStartup(t *testing.T) {
	resetTestPostgres(t)
	const instances = 5
	dbs := make([]*gorm.DB, instances)
	for i := range dbs {
		dbs[i] = openTestPostgres(t)
	}
	var wg sync.WaitGroup
	errs := make([]error, instances)
	for i := range dbs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = newSQLStore(context.Background(), dbs[i], nil)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		assert.NoError(t, err, "instance %d", i)
	}
	var n int64
	require.NoError(t, dbs[0].Table("migrations").Where("id LIKE ?", "queue_%").Count(&n).Error)
	assert.Equal(t, int64(len(sqlMigrations)), n)
}

// TestPostgresLeasesUseDatabaseClock gives one instance a clock an hour
// fast. If leases were computed from the app clock, it would see the other
// instance's live lease as expired and take the delivery.
func TestPostgresLeasesUseDatabaseClock(t *testing.T) {
	resetTestPostgres(t)
	ctx := context.Background()
	a, err := newSQLStore(ctx, openTestPostgres(t), nil)
	require.NoError(t, err)
	b, err := newSQLStore(ctx, openTestPostgres(t), nil)
	require.NoError(t, err)
	b.now = func() time.Time { return time.Now().Add(time.Hour) }

	mustGroup(t, a, "clock", "g", StartFromLatest)
	mustAppend(t, a, newMsg("clock", "", "x"))
	require.Len(t, mustClaim(t, a, "clock", "g", 1, time.Minute), 1)
	assert.Empty(t, mustClaim(t, b, "clock", "g", 1, time.Minute))
}

// TestPostgresJanitorRunsOnOneInstance holds the janitor lock from another
// session: a concurrent Purge must skip rather than wait or double-delete.
func TestPostgresJanitorRunsOnOneInstance(t *testing.T) {
	resetTestPostgres(t)
	ctx := context.Background()
	s, err := newSQLStore(ctx, openTestPostgres(t), nil)
	require.NoError(t, err)
	mustAppend(t, s, newMsg("nogroups", "", "orphan"))

	other := openTestPostgres(t)
	tx := other.Begin()
	require.NoError(t, tx.Error)
	var ok bool
	require.NoError(t, tx.Raw("SELECT pg_try_advisory_xact_lock(?)", pgJanitorLockKey).Scan(&ok).Error)
	require.True(t, ok)

	n, err := s.Purge(ctx, PurgePolicy{}, 100)
	require.NoError(t, err)
	assert.Zero(t, n, "skipped while another instance holds the janitor lock")

	require.NoError(t, tx.Rollback().Error)
	n, err = s.Purge(ctx, PurgePolicy{}, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}

func TestPostgresMultiInstance(t *testing.T) {
	resetTestPostgres(t)
	runMultiInstanceSimulation(t, func(t *testing.T, logger schemas.Logger) Queue {
		// One pool per instance, as separate pods would have.
		s, err := newSQLStore(context.Background(), openTestPostgres(t), nil)
		require.NoError(t, err)
		q, err := NewStoreQueue(s, EngineConfig{}, logger)
		require.NoError(t, err)
		return q
	}, 4, 400)
}

// TestPostgresGroupRegistrationRacesPublish registers an Earliest group on
// one instance while others publish as fast as they can. The topic row lock
// must order the two, so every message is delivered to the new group exactly
// once: either through the backfill or through its publish's own fan-out.
func TestPostgresGroupRegistrationRacesPublish(t *testing.T) {
	resetTestPostgres(t)
	ctx := context.Background()
	const publishers, perPublisher = 6, 60
	stores := make([]*sqlStore, publishers+1)
	for i := range stores {
		s, err := newSQLStore(ctx, openTestPostgres(t), nil)
		require.NoError(t, err)
		stores[i] = s
	}
	topic := "race" + uniqueTopic(t)
	mustGroup(t, stores[0], topic, "early", StartFromLatest)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for p := range publishers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range perPublisher {
				assert.NoError(t, stores[p].Append(ctx, []*Message{newMsg(topic, "", fmt.Sprintf("%d-%d", p, i))}))
			}
		}()
	}
	close(start)
	time.Sleep(20 * time.Millisecond)
	require.NoError(t, stores[publishers].EnsureGroup(ctx, topic, "late", StartFromEarliest))
	wg.Wait()

	db := stores[0].db
	var msgs, late int64
	require.NoError(t, db.Table("queue_messages").Where("topic = ?", topic).Count(&msgs).Error)
	require.NoError(t, db.Table("queue_deliveries").Where("topic = ? AND group_name = ?", topic, "late").Count(&late).Error)
	assert.Equal(t, int64(publishers*perPublisher), msgs)
	assert.Equal(t, msgs, late, "a message published during registration missed the new group")
}

func TestPostgresPurgeRemovesDeadAndOrphans(t *testing.T) {
	resetTestPostgres(t)
	s, err := newSQLStore(context.Background(), openTestPostgres(t), nil)
	require.NoError(t, err)
	sqlPurgeRemovesDeadAndOrphans(t, s)
}

func TestPostgresPurgeInBatches(t *testing.T) {
	resetTestPostgres(t)
	s, err := newSQLStore(context.Background(), openTestPostgres(t), nil)
	require.NoError(t, err)
	sqlPurgeInBatches(t, s)
}

func TestPostgresClaimDeadLettersOrphanedDelivery(t *testing.T) {
	resetTestPostgres(t)
	s, err := newSQLStore(context.Background(), openTestPostgres(t), nil)
	require.NoError(t, err)
	sqlClaimDeadLettersOrphanedDelivery(t, s)
}

// TestPostgresConcurrentMultiTopicPublishersDoNotDeadlock has publishers
// write to the same new topics in opposite orders while groups register on
// them. Topic rows are created and locked in sorted order, so Postgres never
// has to break a deadlock by failing one of them.
func TestPostgresConcurrentMultiTopicPublishersDoNotDeadlock(t *testing.T) {
	resetTestPostgres(t)
	ctx := context.Background()
	const workers, rounds = 12, 40
	stores := make([]*sqlStore, workers)
	for i := range stores {
		s, err := newSQLStore(ctx, openTestPostgres(t), nil)
		require.NoError(t, err)
		stores[i] = s
	}
	var errs []error
	var mu sync.Mutex
	for r := range rounds {
		// Every worker starts the round together on the same fresh topics,
		// half of them listing the topics in reverse.
		start := make(chan struct{})
		var wg sync.WaitGroup
		for w := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				topics := []string{fmt.Sprintf("dl-%d-a", r), fmt.Sprintf("dl-%d-b", r), fmt.Sprintf("dl-%d-c", r)}
				if w%2 == 1 {
					slices.Reverse(topics)
				}
				msgs := make([]*Message, len(topics))
				for i, topic := range topics {
					msgs[i] = newMsg(topic, "", "x")
				}
				<-start
				err := stores[w].Append(ctx, msgs)
				if err == nil && w%3 == 0 {
					err = stores[w].EnsureGroup(ctx, topics[1], fmt.Sprintf("g%d", w), StartFromEarliest)
				}
				if err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}()
		}
		close(start)
		wg.Wait()
	}
	for _, err := range errs {
		t.Errorf("publish or registration failed: %v", err)
	}
}

// TestPostgresMigrationLockHeldFailsClearly holds the migration lock in
// another session, as a crashed instance's lingering connection might:
// startup must give up with an explanation instead of hanging.
func TestPostgresMigrationLockHeldFailsClearly(t *testing.T) {
	resetTestPostgres(t)
	ctx := context.Background()
	holder := openTestPostgres(t)
	sqlDB, err := holder.DB()
	require.NoError(t, err)
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", pgMigrationLockKey)
	require.NoError(t, err)
	defer func() { _, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", pgMigrationLockKey) }()

	prev := pgLockTimeout
	pgLockTimeout = time.Second
	t.Cleanup(func() { pgLockTimeout = prev })
	start := time.Now()
	_, err = newSQLStore(ctx, openTestPostgres(t), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still held by another session")
	assert.Less(t, time.Since(start), 10*time.Second)
}

// TestPostgresKeyLanesStayConsistentUnderConcurrency publishes to a few hot
// keys from several instances while others claim and acknowledge them. Lane
// locks must keep every key with exactly one head: each message is delivered
// once and in publish order per key, and nothing is left waiting at the end.
func TestPostgresKeyLanesStayConsistentUnderConcurrency(t *testing.T) {
	resetTestPostgres(t)
	ctx := context.Background()
	const instances, perPublisher, keys = 4, 150, 3
	stores := make([]*sqlStore, instances)
	for i := range stores {
		s, err := newSQLStore(ctx, openTestPostgres(t), nil)
		require.NoError(t, err)
		stores[i] = s
	}
	topic := "lanes"
	mustGroup(t, stores[0], topic, "g", StartFromLatest)

	var wg sync.WaitGroup
	for p := range instances {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perPublisher {
				m := newMsg(topic, fmt.Sprintf("k%d", i%keys), fmt.Sprintf("%d-%05d", p, i))
				assert.NoError(t, stores[p].Append(ctx, []*Message{m}))
			}
		}()
	}

	var mu sync.Mutex
	seen := map[string]int{}
	lastPerKeyPublisher := map[string]string{} // key/publisher -> last payload seen
	var consumers sync.WaitGroup
	// Consumers stop, and their in-flight database calls are cancelled,
	// however the test ends: registered before they start, this cleanup runs
	// before the pools close.
	consumeCtx, stopConsumers := context.WithCancel(ctx)
	t.Cleanup(func() {
		stopConsumers()
		consumers.Wait()
	})
	for c := range instances {
		consumers.Add(1)
		go func() {
			defer consumers.Done()
			for consumeCtx.Err() == nil {
				cs, err := stores[c].Claim(consumeCtx, ClaimRequest{Topic: topic, Group: "g", RunnerID: fmt.Sprintf("c%d", c), Max: 8, Lease: time.Minute})
				if consumeCtx.Err() != nil {
					return
				}
				if !assert.NoError(t, err) {
					return
				}
				if len(cs) == 0 {
					select {
					case <-consumeCtx.Done():
						return
					case <-time.After(5 * time.Millisecond):
						continue
					}
				}
				mu.Lock()
				for _, cl := range cs {
					payload := string(cl.Message.Payload)
					seen[cl.Message.ID]++
					publisher := payload[:strings.Index(payload, "-")]
					k := cl.Message.Key + "/" + publisher
					assert.Less(t, lastPerKeyPublisher[k], payload, "key %s out of order", cl.Message.Key)
					lastPerKeyPublisher[k] = payload
				}
				mu.Unlock()
				if _, err = stores[c].Ack(consumeCtx, cs); consumeCtx.Err() == nil {
					assert.NoError(t, err)
				}
			}
		}()
	}
	wg.Wait()
	require.Eventually(t, func() bool {
		st, err := stores[0].Stats(ctx, topic, "g")
		return err == nil && st == Stats{}
	}, 60*time.Second, 50*time.Millisecond, "deliveries left waiting or pending")
	stopConsumers()
	consumers.Wait()
	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, seen, instances*perPublisher)
	for id, n := range seen {
		assert.Equal(t, 1, n, "message %s delivered %d times", id, n)
	}
}

// TestPostgresMaxInFlightHoldsUnderConcurrentClaims has instances with their
// own pools claim one group at once under a cap: the group's leases never
// exceed it, however the claims interleave.
func TestPostgresMaxInFlightHoldsUnderConcurrentClaims(t *testing.T) {
	resetTestPostgres(t)
	ctx := context.Background()
	const instances, cap, total = 6, 5, 300
	stores := make([]*sqlStore, instances)
	for i := range stores {
		s, err := newSQLStore(ctx, openTestPostgres(t), nil)
		require.NoError(t, err)
		stores[i] = s
	}
	topic := "capped"
	mustGroup(t, stores[0], topic, "g", StartFromLatest)
	msgs := make([]*Message, total)
	for i := range msgs {
		msgs[i] = newMsg(topic, "", fmt.Sprint(i))
	}
	mustAppend(t, stores[0], msgs...)

	var mu sync.Mutex
	inFlight, peak, done := 0, 0, 0
	var wg sync.WaitGroup
	// A stall must fail the test, not hang the suite: the workers stop at
	// this deadline and the assertions below report what was left.
	runCtx, stop := context.WithTimeout(ctx, 60*time.Second)
	defer stop()
	for i := range instances {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for runCtx.Err() == nil {
				cs, err := stores[i].Claim(runCtx, ClaimRequest{Topic: topic, Group: "g", RunnerID: fmt.Sprint(i), Max: 4, Lease: time.Minute, MaxInFlight: cap})
				if runCtx.Err() != nil {
					return
				}
				if !assert.NoError(t, err) {
					return
				}
				mu.Lock()
				inFlight += len(cs)
				peak = max(peak, inFlight)
				finished := done >= total
				mu.Unlock()
				if finished {
					return
				}
				if len(cs) == 0 {
					time.Sleep(2 * time.Millisecond)
					continue
				}
				time.Sleep(3 * time.Millisecond)
				mu.Lock()
				inFlight -= len(cs)
				done += len(cs)
				mu.Unlock()
				if _, err = stores[i].Ack(runCtx, cs); runCtx.Err() == nil {
					assert.NoError(t, err)
				}
			}
		}()
	}
	wg.Wait()
	require.NoError(t, runCtx.Err(), "workers stalled: %d of %d done", done, total)
	assert.Equal(t, total, done)
	assert.LessOrEqual(t, peak, cap, "the group's leases exceeded the cap")
	assert.Equal(t, cap, peak, "the cap was reached")
}

// TestPostgresRenewalCannotLiftCap holds a heartbeat's renewal open, its row
// updated but not committed, until the lease it renews has run out, and runs
// a capped claim meanwhile. The claim must not count the renewed lease as
// expired and claim past the cap beside it.
func TestPostgresRenewalCannotLiftCap(t *testing.T) {
	db := resetTestPostgres(t)
	ctx := context.Background()
	holder, err := newSQLStore(ctx, openTestPostgres(t), nil)
	require.NoError(t, err)
	claimer, err := newSQLStore(ctx, openTestPostgres(t), nil)
	require.NoError(t, err)
	topic := "renew-capped"
	mustGroup(t, holder, topic, "g", StartFromLatest)
	mustAppend(t, holder, newMsg(topic, "", "a"), newMsg(topic, "", "b"))
	const lease = time.Second
	held, err := holder.Claim(ctx, ClaimRequest{Topic: topic, Group: "g", RunnerID: "holder", Max: 1, Lease: lease, MaxInFlight: 1})
	require.NoError(t, err)
	require.Len(t, held, 1)
	claimedAt := time.Now()

	// A renewal keeps the token and moves the deadline; this trigger stalls
	// it, row locked, until the test lets go of advisory lock 4242.
	gate, err := db.DB()
	require.NoError(t, err)
	gateConn, err := gate.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = gateConn.Close() })
	_, err = gateConn.ExecContext(ctx, "SELECT pg_advisory_lock(4242)")
	require.NoError(t, err)
	var once sync.Once
	open := func() { once.Do(func() { _, _ = gateConn.ExecContext(ctx, "SELECT pg_advisory_unlock(4242)") }) }
	t.Cleanup(open)
	require.NoError(t, db.Exec(`CREATE FUNCTION queue_test_stall() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_advisory_lock(4242); PERFORM pg_advisory_unlock(4242); RETURN NEW; END $$`).Error)
	require.NoError(t, db.Exec(`CREATE TRIGGER queue_test_stall AFTER UPDATE ON queue_deliveries FOR EACH ROW
		WHEN (OLD.claim_token = NEW.claim_token AND OLD.claimed_until IS DISTINCT FROM NEW.claimed_until)
		EXECUTE FUNCTION queue_test_stall()`).Error)
	t.Cleanup(func() {
		_ = db.Exec("DROP TRIGGER IF EXISTS queue_test_stall ON queue_deliveries").Error
		_ = db.Exec("DROP FUNCTION IF EXISTS queue_test_stall()").Error
	})

	renewed := make(chan []string, 1)
	go func() {
		ids, err := holder.Extend(ctx, held, time.Minute)
		assert.NoError(t, err)
		renewed <- ids
	}()
	// The renewal has started; wait out the lease it is renewing.
	time.Sleep(time.Until(claimedAt.Add(lease + 300*time.Millisecond)))
	claimed := make(chan []*Claimed, 1)
	go func() {
		cs, err := claimer.Claim(ctx, ClaimRequest{Topic: topic, Group: "g", RunnerID: "claimer", Max: 1, Lease: time.Minute, MaxInFlight: 1})
		assert.NoError(t, err)
		claimed <- cs
	}()
	var cs []*Claimed
	select {
	case cs = <-claimed: // claimed beside the renewal
	case <-time.After(500 * time.Millisecond): // waiting for the renewal
	}
	open()
	ids := <-renewed
	if cs == nil {
		cs = <-claimed
	}

	var live int64
	require.NoError(t, db.Raw("SELECT count(*) FROM queue_deliveries WHERE topic = ? AND status = 'leased' AND claimed_until >= CAST(EXTRACT(EPOCH FROM clock_timestamp()) * 1000000 AS BIGINT)", topic).Scan(&live).Error)
	assert.Equal(t, []string{held[0].DeliveryID}, ids, "the renewal holds its lease")
	assert.Empty(t, cs, "the claim went past the cap")
	assert.Equal(t, int64(1), live, "live leases under a cap of 1")
}

// TestPostgresStartupWithSingleConnectionPool starts a store on a pool that
// allows one connection: the migration lock must not hold that connection
// while the migrations wait for another.
func TestPostgresStartupWithSingleConnectionPool(t *testing.T) {
	resetTestPostgres(t)
	db := openTestPostgres(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	started := make(chan error, 1)
	go func() {
		_, err := newSQLStore(context.Background(), db, nil)
		started <- err
	}()
	select {
	case err := <-started:
		assert.NoError(t, err)
	case <-time.After(10 * time.Second):
		// Closing the pool (in cleanup) unblocks the stuck startup.
		t.Fatal("startup deadlocked on a one-connection pool")
	}
}

// TestPostgresFailedUnlockDoesNotLeakLock makes releasing the migration lock
// fail: the session that still holds it must not go back to the pool, or
// every other instance fails startup until that connection is recycled.
func TestPostgresFailedUnlockDoesNotLeakLock(t *testing.T) {
	resetTestPostgres(t)
	prev := pgUnlockSQL
	pgUnlockSQL = "SELECT pg_advisory_unlock_missing($1)"
	t.Cleanup(func() { pgUnlockSQL = prev })
	_, _ = newSQLStore(context.Background(), openTestPostgres(t), nil)

	// The discarded session's backend exits, and frees the lock, only some
	// time after its client disconnects.
	other := openTestPostgres(t)
	assert.Eventually(t, func() bool {
		var acquired bool
		require.NoError(t, other.Raw("SELECT pg_try_advisory_lock(?)", pgMigrationLockKey).Scan(&acquired).Error)
		if acquired {
			require.NoError(t, other.Exec("SELECT pg_advisory_unlock(?)", pgMigrationLockKey).Error)
		}
		return acquired
	}, 5*time.Second, 50*time.Millisecond, "the migration lock is still held by a pooled connection")
}

// TestPostgresRetentionUsesDatabaseClock publishes from one instance and
// purges from another whose clock is two hours fast. Retention is measured on
// the database clock, so a message finished just now outlives an hour of
// AckedRetention and a late Earliest group still receives it.
func TestPostgresRetentionUsesDatabaseClock(t *testing.T) {
	db := resetTestPostgres(t)
	publisher, err := newSQLStore(context.Background(), db, nil)
	require.NoError(t, err)
	janitor, err := newSQLStore(context.Background(), db, nil)
	require.NoError(t, err)
	janitor.now = func() time.Time { return time.Now().Add(2 * time.Hour) }

	mustAppend(t, publisher, newMsg("clock-purge", "", "kept"))
	_, err = janitor.Purge(context.Background(), PurgePolicy{DeadRetention: time.Hour, AckedRetention: time.Hour}, 100)
	require.NoError(t, err)
	mustGroup(t, publisher, "clock-purge", "late", StartFromEarliest)
	assert.Equal(t, []string{"kept"}, payloads(mustClaim(t, publisher, "clock-purge", "late", 10, longLease)))
}

// TestPostgresPurgeWaitsForEarliestBackfill pauses a new Earliest group's
// backfill after it has read the topic's messages and purges meanwhile: the
// purge must not delete a message the backfill is about to deliver.
func TestPostgresPurgeWaitsForEarliestBackfill(t *testing.T) {
	resetTestPostgres(t)
	ctx := context.Background()
	registrar, err := newSQLStore(ctx, openTestPostgres(t), nil)
	require.NoError(t, err)
	janitor, err := newSQLStore(ctx, openTestPostgres(t), nil)
	require.NoError(t, err)
	mustAppend(t, registrar, newMsg("backfill", "", "kept"))
	time.Sleep(20 * time.Millisecond) // older than the acked retention used below

	scanned, resume := make(chan struct{}), make(chan struct{})
	var resumeOnce sync.Once
	release := func() { resumeOnce.Do(func() { close(resume) }) }
	testHookBackfillScanned = func() {
		close(scanned)
		<-resume
	}
	registered := make(chan error, 1)
	purged := make(chan error, 1)
	// Never leave a worker blocked in the hook or holding its transaction.
	t.Cleanup(func() {
		release()
		testHookBackfillScanned = nil
	})

	go func() { registered <- registrar.EnsureGroup(ctx, "backfill", "late", StartFromEarliest) }()
	// EnsureGroup can fail before it reaches the hook; report that instead of
	// waiting for a scan that never comes.
	select {
	case <-scanned:
	case err := <-registered:
		t.Fatalf("EnsureGroup returned before its backfill scan: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the backfill never scanned")
	}
	testHookBackfillScanned = nil
	go func() {
		_, err := janitor.Purge(ctx, PurgePolicy{DeadRetention: time.Hour, AckedRetention: time.Millisecond}, 100)
		purged <- err
	}()
	time.Sleep(300 * time.Millisecond) // the purge is waiting on the topic lock
	release()
	for _, done := range []chan error{registered, purged} {
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("registration or purge did not finish")
		}
	}

	assert.Equal(t, []string{"kept"}, payloads(mustClaim(t, registrar, "backfill", "late", 10, longLease)),
		"the purge deleted a message the backfill was delivering")
}

func BenchmarkSQLiteQueue(b *testing.B) {
	runBenchmarks(b, benchBackend{
		// Through a real SQLite logstore, so the database runs with the
		// logstore's DSN settings (WAL, cache size, busy timeout).
		fresh: func(b *testing.B) (Store, func()) {
			dir := b.TempDir()
			ls := openTestSQLiteLogStore(b, dir)
			s, err := newLogStoreBackend(context.Background(), Dependencies{LogStore: ls}, EngineConfig{}.WithDefaults(), nil)
			require.NoError(b, err)
			return s, func() {
				_ = ls.Close(context.Background())
				_ = os.RemoveAll(dir)
			}
		},
		footprint: func(b *testing.B, s Store) int64 {
			// Pages in use after folding the WAL back into the database file.
			db := s.(*sqlStore).db
			require.NoError(b, db.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error)
			var pages, free, size int64
			require.NoError(b, db.Raw("PRAGMA page_count").Scan(&pages).Error)
			require.NoError(b, db.Raw("PRAGMA freelist_count").Scan(&free).Error)
			require.NoError(b, db.Raw("PRAGMA page_size").Scan(&size).Error)
			return (pages - free) * size
		},
		compact:  func(b *testing.B, s Store) { require.NoError(b, s.(*sqlStore).db.Exec("VACUUM").Error) },
		backlogs: []int{10_000, 100_000},
	})
}

func BenchmarkPostgresQueue(b *testing.B) {
	runBenchmarks(b, benchBackend{
		fresh: func(b *testing.B) (Store, func()) {
			closeTestDB(resetTestPostgres(b))
			db := openTestPostgres(b)
			s, err := newSQLStore(context.Background(), db, nil)
			require.NoError(b, err)
			return s, func() { closeTestDB(db) }
		},
		footprint: func(b *testing.B, s Store) int64 {
			// Heap, indexes and TOAST of every queue table.
			var bytes int64
			require.NoError(b, s.(*sqlStore).db.Raw("SELECT COALESCE(SUM(pg_total_relation_size(format('%I.%I', schemaname, tablename)::regclass)), 0) "+
				"FROM pg_tables WHERE schemaname = ? AND tablename LIKE 'queue\\_%'", pgTestSchema).Scan(&bytes).Error)
			return bytes
		},
		compact: func(b *testing.B, s Store) {
			for _, table := range []string{"queue_deliveries", "queue_messages", "queue_groups", "queue_topics"} {
				require.NoError(b, s.(*sqlStore).db.Exec("VACUUM FULL "+table).Error)
			}
		},
		backlogs: []int{10_000, 100_000},
	})
}
