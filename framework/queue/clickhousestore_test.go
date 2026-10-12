package queue

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// clickhouseTestConfig targets the tests/docker-compose.yml ClickHouse, with
// the same overrides the logstore suite honours.
func clickhouseTestConfig(t testing.TB) *logstore.ClickHouseConfig {
	t.Helper()
	env := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}
	cfg := &logstore.ClickHouseConfig{
		Host:     schemas.NewSecretVar(env("BIFROST_TEST_CLICKHOUSE_HOST", "localhost")),
		Port:     schemas.NewSecretVar(env("BIFROST_TEST_CLICKHOUSE_PORT", "9001")),
		Database: schemas.NewSecretVar(env("BIFROST_TEST_CLICKHOUSE_DB", "bifrost")),
		Username: schemas.NewSecretVar(env("BIFROST_TEST_CLICKHOUSE_USER", "bifrost")),
		Password: schemas.NewSecretVar(env("BIFROST_TEST_CLICKHOUSE_PASSWORD", "bifrost_password")),
	}
	if os.Getenv("BIFROST_TEST_CLICKHOUSE_DB") != "" && !strings.Contains(strings.ToLower(cfg.Database.GetValue()), "test") {
		t.Fatalf("refusing to drop queue tables in ClickHouse database %q: overrides must name a database containing \"test\"", cfg.Database.GetValue())
	}
	return cfg
}

type clickhouseTestEnv struct {
	logStore logstore.LogStore
	db       *gorm.DB
	schema   chSchemaStore
}

// setupClickHouse opens the logstore and drops the queue tables so each test
// creates them from scratch. It skips when ClickHouse is not running locally
// and fails in CI, where the compose service is expected to be up.
func setupClickHouse(t testing.TB) clickhouseTestEnv {
	t.Helper()
	ls, err := logstore.NewLogStore(context.Background(), &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypeClickHouse,
		Config:  clickhouseTestConfig(t),
	}, bifrost.NewNoOpLogger())
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("ClickHouse not available in CI: %v", err)
		}
		t.Skipf("ClickHouse not available: %v", err)
	}
	t.Cleanup(func() { _ = ls.Close(context.Background()) })
	db := ls.(scopedDBStore).ScopedDB(context.Background())
	require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("queue_test:hook", runCHTestHook))
	require.NoError(t, db.Callback().Row().Before("gorm:row").Register("queue_test:capture", runCHCapture))
	for _, table := range []string{"queue_deliveries", "queue_messages", "queue_groups"} {
		require.NoError(t, db.Exec("DROP TABLE IF EXISTS "+table+" SYNC").Error)
	}
	return clickhouseTestEnv{logStore: ls, db: db, schema: ls.(chSchemaStore)}
}

func (e clickhouseTestEnv) store(t testing.TB, locker logstore.DistributedLocker) *clickhouseStore {
	t.Helper()
	s, err := newClickHouseStore(context.Background(), e.db, e.schema, locker, DefaultRetentionHours*time.Hour, nil)
	require.NoError(t, err)
	return s
}

func TestClickHouseStoreContract(t *testing.T) {
	env := setupClickHouse(t)
	locker := NewProcessLocker()
	runStoreContract(t, func(t *testing.T) Store {
		return env.store(t, locker)
	})
}

// TestClickHouseDuplicateFirstRowNeverOutranksAClaim replays a delivery's
// first row after it was claimed and acknowledged, as a retried publish or
// an overlapping backfill would. ReplacingMergeTree keeps the highest ver,
// so the replay must carry a lower one than any state change.
func TestClickHouseDuplicateFirstRowNeverOutranksAClaim(t *testing.T) {
	env := setupClickHouse(t)
	s := env.store(t, NewProcessLocker())
	ctx := context.Background()
	mustGroup(t, s, "replay", "g", StartFromLatest)
	m := newMsg("replay", "", "x")
	mustAppend(t, s, m)
	cs := mustClaim(t, s, "replay", "g", 1, longLease)
	require.Len(t, cs, 1)
	held, err := ackOne(ctx, s, cs[0])
	require.NoError(t, err)
	require.True(t, held)

	first := newCHDelivery(chQueueMessage{ID: m.ID, Topic: m.Topic}, "g")
	require.NoError(t, s.insertDeliveries(ctx, []chQueueDelivery{first}, chInitialVer))

	st, err := s.Stats(ctx, "replay", "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{}, st)
	assert.Empty(t, mustClaim(t, s, "replay", "g", 1, longLease))
}

func TestClickHousePublishWithoutLockerConsumeRefused(t *testing.T) {
	env := setupClickHouse(t)
	q, err := NewQueue(context.Background(), &Config{Enabled: true, Type: QueueTypeLogStore}, Dependencies{LogStore: env.logStore}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close(context.Background()) })

	require.NoError(t, q.Publish(context.Background(), &Message{Topic: "nolock", Payload: []byte("x")}))
	_, err = q.Subscribe(context.Background(), "nolock", "g", func(context.Context, *Delivery) error { return nil })
	assert.ErrorIs(t, err, ErrNoLocker)
}

func TestClickHouseLogStoreQueueEndToEnd(t *testing.T) {
	env := setupClickHouse(t)
	q, err := NewQueue(context.Background(), &Config{Enabled: true, Type: QueueTypeLogStore},
		Dependencies{LogStore: env.logStore, Locker: NewProcessLocker()}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close(context.Background()) })

	got := make(chan *Delivery, 1)
	subscribe(t, q, "events", "consumer", func(ctx context.Context, d *Delivery) error {
		got <- d
		return nil
	})
	publish(t, q, &Message{Topic: "events", Key: "k", Payload: []byte("hello")})
	select {
	case d := <-got:
		assert.Equal(t, []byte("hello"), d.Payload)
		assert.Equal(t, "k", d.Key)
	case <-time.After(10 * time.Second):
		t.Fatal("message not delivered")
	}
	waitStats(t, q, "events", "consumer", Stats{})
}

// TestClickHouseMultiInstance runs the simulation with a real
// configstore.DistributedLockManager per instance, all backed by one shared
// lock table, which is how separate pods coordinate in production.
func TestClickHouseMultiInstance(t *testing.T) {
	env := setupClickHouse(t)
	cs, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close(context.Background()) })

	runMultiInstanceSimulation(t, func(t *testing.T, logger schemas.Logger) Queue {
		locker := configstore.NewDistributedLockManager(cs, bifrost.NewNoOpLogger(), configstore.WithDefaultTTL(30*time.Second),
			configstore.WithRetryInterval(20*time.Millisecond))
		q, err := NewStoreQueue(env.store(t, locker), EngineConfig{}, logger)
		require.NoError(t, err)
		return q
	}, 3, 150)
}

func TestClickHouseEngineContract(t *testing.T) {
	env := setupClickHouse(t)
	locker := NewProcessLocker()
	runEngineContract(t, func(t *testing.T, n int) []Store {
		out := make([]Store, n)
		for i := range out {
			// Through the adapter, as the gateway builds it.
			s, err := newLogStoreBackend(context.Background(), Dependencies{LogStore: env.logStore, Locker: locker}, EngineConfig{}.WithDefaults(), nil)
			require.NoError(t, err)
			out[i] = s
		}
		return out
	})
}

// TestClickHouseLeasesUseDatabaseClock checks a second instance cannot take
// over a live lease. Leases come from now64() and the store never reads an
// instance's clock, so instances whose clocks disagree agree on every lease.
func TestClickHouseLeasesUseDatabaseClock(t *testing.T) {
	env := setupClickHouse(t)
	locker := NewProcessLocker()
	a, b := env.store(t, locker), env.store(t, locker)

	mustGroup(t, a, "clock", "g", StartFromLatest)
	mustAppend(t, b, newMsg("clock", "", "x")) // published by the skewed instance
	require.Len(t, mustClaim(t, a, "clock", "g", 1, time.Minute), 1)
	assert.Empty(t, mustClaim(t, b, "clock", "g", 1, time.Minute))
}

// TestClickHousePurgeDeletesTombstonesAndDead checks the janitor's
// deletions: acked tombstones at once, dead rows DeadRetention after they
// died, then the messages nothing refers to any more, every version at once,
// and nothing comes back.
func TestClickHousePurgeDeletesTombstonesAndDead(t *testing.T) {
	env := setupClickHouse(t)
	s := env.store(t, NewProcessLocker())
	ctx := context.Background()
	policy := PurgePolicy{DeadRetention: 400 * time.Millisecond, AckedRetention: time.Millisecond}
	mustGroup(t, s, "purge", "g", StartFromLatest)
	mustAppend(t, s, newMsg("purge", "", "acked"), newMsg("purge", "", "dead"))
	cs := mustClaim(t, s, "purge", "g", 2, longLease)
	require.Len(t, cs, 2)
	held, err := ackOne(ctx, s, cs[0])
	require.NoError(t, err)
	require.True(t, held)
	held, err = s.Kill(ctx, cs[1], "boom")
	require.NoError(t, err)
	require.True(t, held)

	time.Sleep(50 * time.Millisecond)
	n, err := s.Purge(ctx, policy, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n, "the tombstone and its message; the dead row is younger than DeadRetention")
	st, err := s.Stats(ctx, "purge", "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{Dead: 1}, st)

	time.Sleep(policy.DeadRetention + 100*time.Millisecond)
	n, err = s.Purge(ctx, policy, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n, "the dead row, then its message")

	var rows int64
	require.NoError(t, env.db.Raw("SELECT count() FROM queue_deliveries WHERE id IN ?",
		[]string{cs[0].DeliveryID, cs[1].DeliveryID}).Scan(&rows).Error)
	assert.Zero(t, rows, "every version of both rows is gone")
	require.NoError(t, env.db.Raw("SELECT count() FROM queue_messages WHERE id IN ?",
		[]string{cs[0].Message.ID, cs[1].Message.ID}).Scan(&rows).Error)
	assert.Zero(t, rows, "both messages are gone")
	st, err = s.Stats(ctx, "purge", "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{}, st)
	assert.Empty(t, mustClaim(t, s, "purge", "g", 10, longLease), "nothing is revived")
}

// TestClickHouseConcurrentStartup starts several instances against missing
// tables at once, with and without a shared locker: the DDL must be
// idempotent and every instance must come up.
func TestClickHouseConcurrentStartup(t *testing.T) {
	for _, withLocker := range []bool{false, true} {
		env := setupClickHouse(t)
		var locker logstore.DistributedLocker
		if withLocker {
			locker = NewProcessLocker()
		}
		const instances = 5
		stores := make([]*clickhouseStore, instances)
		errs := make([]error, instances)
		var wg sync.WaitGroup
		for i := range instances {
			wg.Add(1)
			go func() {
				defer wg.Done()
				stores[i], errs[i] = newClickHouseStore(context.Background(), env.db, env.schema, locker, DefaultRetentionHours*time.Hour, nil)
			}()
		}
		wg.Wait()
		for i, err := range errs {
			require.NoError(t, err, "instance %d (locker %v)", i, withLocker)
		}
		if withLocker {
			s := stores[0]
			mustGroup(t, s, "startup", "g", StartFromLatest)
			mustAppend(t, stores[1], newMsg("startup", "", "x"))
			assert.Len(t, mustClaim(t, stores[2], "startup", "g", 1, longLease), 1)
		}
	}
}

// TestClickHouseClaimSkipsDeliveryUntilMessageLands writes a delivery whose
// message row has not arrived yet, as Append does between writing deliveries
// and their message: it stays pending, is not handed out, and is claimed
// once the message lands.
func TestClickHouseClaimSkipsDeliveryUntilMessageLands(t *testing.T) {
	env := setupClickHouse(t)
	s := env.store(t, NewProcessLocker())
	ctx := context.Background()
	mustGroup(t, s, "landing", "g", StartFromLatest)
	id := uuid.NewString()
	require.NoError(t, s.insertDeliveries(ctx, []chQueueDelivery{newCHDelivery(chQueueMessage{ID: id, Topic: "landing", CreatedAt: time.Now()}, "g")}, chInitialVer))

	assert.Empty(t, mustClaim(t, s, "landing", "g", 10, longLease))
	st, err := s.Stats(ctx, "landing", "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{Pending: 1}, st, "still pending, not dead-lettered")

	require.NoError(t, s.db.Exec("INSERT INTO queue_messages (id, topic, msg_key, seq, payload, headers, published_at, created_at, ver) "+
		"VALUES (?, 'landing', '', 1, unhex(?), '{}', now64(3), now64(3), now64(9))", id, hex.EncodeToString([]byte("late"))).Error)
	assert.Equal(t, []string{"late"}, payloads(mustClaim(t, s, "landing", "g", 10, longLease)))
}

// TestClickHouseRetentionHidesExpiredRows shortens retention below the
// table TTL's granularity: rows past retention must disappear from claims and
// stats before TTL physically removes them, so no stale version is read.
func TestClickHouseRetentionHidesExpiredRows(t *testing.T) {
	env := setupClickHouse(t)
	s := env.store(t, NewProcessLocker())
	s.retention = time.Second
	ctx := context.Background()
	mustGroup(t, s, "expiry", "g", StartFromLatest)
	mustAppend(t, s, newMsg("expiry", "", "old"))
	st, err := s.Stats(ctx, "expiry", "g")
	require.NoError(t, err)
	require.Equal(t, Stats{Pending: 1}, st)

	time.Sleep(s.retention + 300*time.Millisecond)
	st, err = s.Stats(ctx, "expiry", "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{}, st)
	assert.Empty(t, mustClaim(t, s, "expiry", "g", 10, longLease))
}

// TestClickHouseClaimUnderLockContention holds a group's lock elsewhere: a
// claim gives up after its budget with nothing claimed and no error, and
// succeeds once the lock is free.
func TestClickHouseClaimUnderLockContention(t *testing.T) {
	env := setupClickHouse(t)
	locker := NewProcessLocker()
	s := env.store(t, locker)
	s.budget = 300 * time.Millisecond
	ctx := context.Background()
	topic := "contended"
	mustGroup(t, s, topic, "g", StartFromLatest)
	mustAppend(t, s, newMsg(topic, "", "x"))

	_, release, err := locker.Acquire(ctx, groupLockKey(topic, "g"))
	require.NoError(t, err)
	start := time.Now()
	cs, err := s.Claim(ctx, ClaimRequest{Topic: topic, Group: "g", RunnerID: "r", Max: 1, Lease: longLease})
	assert.NoError(t, err)
	assert.Empty(t, cs)
	assert.Less(t, time.Since(start), 3*time.Second)

	release()
	assert.Len(t, mustClaim(t, s, topic, "g", 1, longLease), 1)
}

// lostLocker grants the lock but hands back a context that is already
// cancelled, as when the lock's lease is lost right after acquiring it.
type lostLocker struct{}

func (lostLocker) Acquire(ctx context.Context, key string) (context.Context, func(), error) {
	held, cancel := context.WithCancelCause(ctx)
	cancel(errors.New("lock lost"))
	return held, func() {}, nil
}

// TestClickHouseClaimAbortsWhenLockIsLost checks a claim does all its work on
// the lock's context: with the lock lost, it fails and writes no lease.
func TestClickHouseClaimAbortsWhenLockIsLost(t *testing.T) {
	env := setupClickHouse(t)
	ctx := context.Background()
	setup := env.store(t, NewProcessLocker())
	topic := "lost-lock"
	mustGroup(t, setup, topic, "g", StartFromLatest)
	mustAppend(t, setup, newMsg(topic, "", "x"))

	// Startup's table creation also runs under the lock, so lose it only now.
	s := env.store(t, NewProcessLocker())
	s.locker = lostLocker{}
	_, err := s.Claim(ctx, ClaimRequest{Topic: topic, Group: "g", RunnerID: "r", Max: 1, Lease: longLease})
	assert.Error(t, err)
	st, err := setup.Stats(ctx, topic, "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{Pending: 1}, st, "no lease was written")
}

// TestClickHouseScheduledRowsLiveUntilDue shortens retention below a
// message's schedule: retention is counted from when a row is due, so the
// message stays visible after its publish time has aged out and is claimed
// once due.
func TestClickHouseScheduledRowsLiveUntilDue(t *testing.T) {
	env := setupClickHouse(t)
	s := env.store(t, NewProcessLocker())
	s.retention = time.Second
	ctx := context.Background()
	mustGroup(t, s, "due", "g", StartFromLatest)
	m := newMsg("due", "", "later")
	m.DeliverAt = time.Now().Add(1500 * time.Millisecond)
	mustAppend(t, s, m)

	time.Sleep(1200 * time.Millisecond) // past retention from publish, not yet due
	st, err := s.Stats(ctx, "due", "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{Pending: 1}, st, "still retained")
	assert.Empty(t, mustClaim(t, s, "due", "g", 1, longLease))

	time.Sleep(time.Until(m.DeliverAt) + 100*time.Millisecond)
	assert.Equal(t, []string{"later"}, payloads(mustClaim(t, s, "due", "g", 1, longLease)))
}

// chHookKey carries a chTestHook on a context.
type chHookKey struct{}

// chTestHook runs before every statement a store executes with Exec on a
// context carrying it, and fails the statement when it returns an error.
type chTestHook func(sql string) error

func withCHHook(ctx context.Context, h chTestHook) context.Context {
	return context.WithValue(ctx, chHookKey{}, h)
}

func runCHTestHook(tx *gorm.DB) {
	if h, ok := tx.Statement.Context.Value(chHookKey{}).(chTestHook); ok {
		if err := h(tx.Statement.SQL.String()); err != nil {
			_ = tx.AddError(err)
		}
	}
}

// chCaptureKey carries a function that receives every query a store reads
// rows with, its arguments inlined.
type chCaptureKey struct{}

func runCHCapture(tx *gorm.DB) {
	if f, ok := tx.Statement.Context.Value(chCaptureKey{}).(func(string)); ok {
		f(tx.Dialector.Explain(tx.Statement.SQL.String(), tx.Statement.Vars...))
	}
}

// failOnce fails the first statement starting with prefix.
func failOnce(prefix string) chTestHook {
	var once sync.Once
	return func(sql string) error {
		var err error
		if strings.HasPrefix(sql, prefix) {
			once.Do(func() { err = errors.New("injected failure: " + prefix) })
		}
		return err
	}
}

// pauseAt blocks the first statement starting with prefix until release is
// called. paused is closed once that statement is blocked.
func pauseAt(prefix string) (h chTestHook, paused <-chan struct{}, release func()) {
	p, r := make(chan struct{}), make(chan struct{})
	var once, releaseOnce sync.Once
	return func(sql string) error {
			if strings.HasPrefix(sql, prefix) {
				once.Do(func() {
					close(p)
					<-r
				})
			}
			return nil
		}, p, func() {
			releaseOnce.Do(func() { close(r) })
		}
}

// releaseAfter calls release once done is closed or after wait, whichever is
// first. A paused operation that holds a lock the other side needs is only
// released by the timeout, which is the behaviour these tests want.
func releaseAfter(done <-chan struct{}, wait time.Duration, release func()) {
	go func() {
		select {
		case <-done:
		case <-time.After(wait):
		}
		release()
	}()
}

// TestClickHousePublishRetryRepairsPartialPublish fails one half of a
// publish's writes. Retrying the publish must leave every registered group
// with exactly one delivery, whichever half failed.
func TestClickHousePublishRetryRepairsPartialPublish(t *testing.T) {
	env := setupClickHouse(t)
	s := env.store(t, NewProcessLocker())
	ctx := context.Background()
	for _, table := range []string{"queue_deliveries", "queue_messages"} {
		topic := "partial-" + table
		mustGroup(t, s, topic, "g1", StartFromLatest)
		mustGroup(t, s, topic, "g2", StartFromLatest)
		m := newMsg(topic, "", "x")
		require.Error(t, s.Append(withCHHook(ctx, failOnce("INSERT INTO "+table)), []*Message{m}))
		require.NoError(t, s.Append(ctx, []*Message{m}), table)
		for _, g := range []string{"g1", "g2"} {
			st, err := s.Stats(ctx, topic, g)
			require.NoError(t, err)
			assert.Equal(t, Stats{Pending: 1}, st, "%s failed, group %s", table, g)
			assert.Equal(t, []string{"x"}, payloads(mustClaim(t, s, topic, g, 10, longLease)), "%s failed, group %s", table, g)
		}
	}
}

// TestClickHouseKeyOrderSurvivesLargeBatches publishes more than a thousand
// messages on one key in one batch: the first two must be delivered first
// and second.
func TestClickHouseKeyOrderSurvivesLargeBatches(t *testing.T) {
	env := setupClickHouse(t)
	s := env.store(t, NewProcessLocker())
	ctx := context.Background()
	mustGroup(t, s, "big", "g", StartFromLatest)
	msgs := make([]*Message, 1001)
	for i := range msgs {
		msgs[i] = newMsg("big", "k", strconv.Itoa(i))
	}
	mustAppend(t, s, msgs...)
	for _, want := range []string{"0", "1"} {
		cs := mustClaim(t, s, "big", "g", 10, longLease)
		require.Equal(t, []string{want}, payloads(cs))
		held, err := ackOne(ctx, s, cs[0])
		require.NoError(t, err)
		require.True(t, held)
	}
}

// TestClickHouseKeyOrderAcrossInstances publishes one key's messages from two
// instances in turn: publish order is delivery order. Messages are numbered
// from the database clock, so the instances' own clocks play no part.
func TestClickHouseKeyOrderAcrossInstances(t *testing.T) {
	env := setupClickHouse(t)
	locker := NewProcessLocker()
	a, b := env.store(t, locker), env.store(t, locker)
	ctx := context.Background()
	mustGroup(t, a, "skew", "g", StartFromLatest)
	mustAppend(t, b, newMsg("skew", "k", "first"))
	mustAppend(t, a, newMsg("skew", "k", "second"))
	for _, want := range []string{"first", "second"} {
		cs := mustClaim(t, a, "skew", "g", 10, longLease)
		require.Equal(t, []string{want}, payloads(cs))
		held, err := ackOne(ctx, a, cs[0])
		require.NoError(t, err)
		require.True(t, held)
	}
}

// TestClickHouseFailedBackfillResumes fails an Earliest group's backfill
// half way: registering the group again must finish it.
func TestClickHouseFailedBackfillResumes(t *testing.T) {
	env := setupClickHouse(t)
	s := env.store(t, NewProcessLocker())
	ctx := context.Background()
	mustAppend(t, s, newMsg("resume", "", "a"), newMsg("resume", "", "b"), newMsg("resume", "", "c"))
	require.Error(t, s.EnsureGroup(withCHHook(ctx, failOnce("INSERT INTO queue_deliveries")), "resume", "g", StartFromEarliest))
	require.NoError(t, s.EnsureGroup(ctx, "resume", "g", StartFromEarliest))
	st, err := s.Stats(ctx, "resume", "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{Pending: 3}, st)
}

// TestClickHouseRegistrationWaitsForInFlightPublish pauses a publish half way
// while an Earliest group registers on its topic: the group must still get
// the message, however long the publish takes to finish.
func TestClickHouseRegistrationWaitsForInFlightPublish(t *testing.T) {
	env := setupClickHouse(t)
	s := env.store(t, NewProcessLocker())
	ctx := context.Background()
	hook, paused, release := pauseAt("INSERT INTO queue_")
	defer release()
	published := make(chan error, 1)
	go func() { published <- s.Append(withCHHook(ctx, hook), []*Message{newMsg("inflight", "", "x")}) }()
	<-paused

	registered := make(chan struct{})
	releaseAfter(registered, time.Second, release)
	require.NoError(t, s.EnsureGroup(ctx, "inflight", "g", StartFromEarliest))
	close(registered)
	require.NoError(t, <-published)

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, []string{"x"}, payloads(mustClaim(t, s, "inflight", "g", 10, longLease)))
}

// TestClickHousePurgeWaitsForEarliestBackfill pauses an Earliest group's
// backfill and purges meanwhile: the message the backfill is delivering must
// survive.
func TestClickHousePurgeWaitsForEarliestBackfill(t *testing.T) {
	env := setupClickHouse(t)
	s := env.store(t, NewProcessLocker())
	ctx := context.Background()
	mustAppend(t, s, newMsg("purgerace", "", "x"))
	time.Sleep(50 * time.Millisecond)

	hook, paused, release := pauseAt("INSERT INTO queue_deliveries")
	defer release()
	registered := make(chan error, 1)
	go func() { registered <- s.EnsureGroup(withCHHook(ctx, hook), "purgerace", "g", StartFromEarliest) }()
	<-paused
	purged := make(chan struct{})
	releaseAfter(purged, time.Second, release)
	_, err := s.Purge(ctx, PurgePolicy{DeadRetention: time.Millisecond, AckedRetention: time.Millisecond}, 100)
	close(purged)
	require.NoError(t, err)
	require.NoError(t, <-registered)

	assert.Equal(t, []string{"x"}, payloads(mustClaim(t, s, "purgerace", "g", 10, longLease)))
}

// TestClickHouseRetentionChangeUpdatesTTL restarts with a longer retention:
// the existing tables' TTL must follow it.
func TestClickHouseRetentionChangeUpdatesTTL(t *testing.T) {
	env := setupClickHouse(t)
	_, err := newClickHouseStore(context.Background(), env.db, env.schema, nil, time.Hour, nil)
	require.NoError(t, err)
	_, err = newClickHouseStore(context.Background(), env.db, env.schema, nil, 3*time.Hour, nil)
	require.NoError(t, err)
	for _, table := range []string{"queue_messages", "queue_deliveries"} {
		var engine string
		require.NoError(t, env.db.Raw("SELECT engine_full FROM system.tables WHERE database = currentDatabase() AND name = ?", table).Scan(&engine).Error)
		assert.Contains(t, engine, "toIntervalHour(3)", table)
	}
}

// TestClickHouseDeliveryExpiresWithItsMessage checks deliveries take their
// message's created_at, whether fanned out on publish or backfilled later,
// so the two always expire together.
func TestClickHouseDeliveryExpiresWithItsMessage(t *testing.T) {
	env := setupClickHouse(t)
	s := env.store(t, NewProcessLocker())
	mustGroup(t, s, "expire", "latest", StartFromLatest)
	mustAppend(t, s, newMsg("expire", "", "x"))
	time.Sleep(100 * time.Millisecond)
	mustGroup(t, s, "expire", "earliest", StartFromEarliest)

	var rows []struct {
		GroupName string
		Delivery  int64
		Message   int64
	}
	require.NoError(t, env.db.Raw("SELECT d.group_name AS group_name, toUnixTimestamp64Milli(d.created_at) AS delivery, "+
		"toUnixTimestamp64Milli(m.created_at) AS message FROM queue_deliveries AS d FINAL "+
		"INNER JOIN queue_messages AS m FINAL ON m.id = d.message_id WHERE d.topic = 'expire'").Scan(&rows).Error)
	require.Len(t, rows, 2)
	for _, r := range rows {
		assert.Equal(t, r.Message, r.Delivery, r.GroupName)
	}
}

// TestClickHouseStaleRetryCannotEraseNewClaim lets a lease lapse, pauses the
// old holder's Retry between its read and its write, and has another
// instance claim the delivery meanwhile. The new holder's claim must survive.
func TestClickHouseStaleRetryCannotEraseNewClaim(t *testing.T) {
	env := setupClickHouse(t)
	locker := NewProcessLocker()
	a, b := env.store(t, locker), env.store(t, locker)
	ctx := context.Background()
	mustGroup(t, a, "stale", "g", StartFromLatest)
	mustAppend(t, a, newMsg("stale", "", "x"))
	old := mustClaim(t, a, "stale", "g", 1, 100*time.Millisecond)
	require.Len(t, old, 1)
	time.Sleep(300 * time.Millisecond)

	hook, paused, release := pauseAt("INSERT INTO queue_deliveries")
	defer release()
	retried := make(chan error, 1)
	go func() {
		_, err := a.Retry(withCHHook(ctx, hook), old[0], 0, "slow handler")
		retried <- err
	}()
	// A Retry that sees its lease has expired writes nothing and never pauses.
	var cs []*Claimed
	select {
	case <-paused:
		claimed := make(chan struct{})
		releaseAfter(claimed, time.Second, release)
		cs = mustClaim(t, b, "stale", "g", 1, longLease)
		close(claimed)
		require.NoError(t, <-retried)
	case err := <-retried:
		require.NoError(t, err)
		cs = mustClaim(t, b, "stale", "g", 1, longLease)
	}

	require.Len(t, cs, 1)
	held, err := ackOne(ctx, b, cs[0])
	require.NoError(t, err)
	assert.True(t, held, "the new holder's claim was overwritten")
}

// TestClickHouseClaimPausedAfterScanCannotReviveAck pauses a claim between
// reading an expired lease and writing its own, while the old holder
// acknowledges the delivery. The acknowledgement and the claim cannot both
// succeed, or the delivery runs again.
func TestClickHouseClaimPausedAfterScanCannotReviveAck(t *testing.T) {
	env := setupClickHouse(t)
	locker := NewProcessLocker()
	a, b := env.store(t, locker), env.store(t, locker)
	ctx := context.Background()
	mustGroup(t, a, "revive-ack", "g", StartFromLatest)
	mustAppend(t, a, newMsg("revive-ack", "", "x"))
	old := mustClaim(t, b, "revive-ack", "g", 1, 100*time.Millisecond)
	require.Len(t, old, 1)
	time.Sleep(300 * time.Millisecond)

	hook, paused, release := pauseAt("INSERT INTO queue_deliveries")
	defer release()
	type claimResult struct {
		cs  []*Claimed
		err error
	}
	claimed := make(chan claimResult, 1)
	go func() {
		cs, err := a.Claim(withCHHook(ctx, hook), ClaimRequest{Topic: "revive-ack", Group: "g", RunnerID: "a", Max: 1, Lease: longLease})
		claimed <- claimResult{cs, err}
	}()
	<-paused
	acked := make(chan struct{})
	releaseAfter(acked, time.Second, release)
	ackHeld, err := ackOne(ctx, b, old[0])
	close(acked)
	require.NoError(t, err)
	res := <-claimed
	require.NoError(t, res.err)

	assert.False(t, ackHeld && len(res.cs) == 1, "acknowledged and claimed again")
	assert.True(t, ackHeld || len(res.cs) == 1, "neither the acknowledgement nor the claim took effect")
}

// TestClickHouseAbandonedPublishStopsBlockingItsKey fails a keyed publish
// after its deliveries were written and never retries it. Its delivery heads
// the key until the janitor deletes it, and a retry after that is delivered.
func TestClickHouseAbandonedPublishStopsBlockingItsKey(t *testing.T) {
	env := setupClickHouse(t)
	s := env.store(t, NewProcessLocker())
	s.abandoned = 200 * time.Millisecond
	ctx := context.Background()
	mustGroup(t, s, "abandoned", "g", StartFromLatest)
	first := newMsg("abandoned", "k", "first")
	require.Error(t, s.Append(withCHHook(ctx, failOnce("INSERT INTO queue_messages")), []*Message{first}))
	mustAppend(t, s, newMsg("abandoned", "k", "second"))
	assert.Empty(t, mustClaim(t, s, "abandoned", "g", 10, longLease), "the abandoned delivery heads the key")

	time.Sleep(s.abandoned + 100*time.Millisecond)
	n, err := s.Purge(ctx, PurgePolicy{DeadRetention: time.Hour, AckedRetention: time.Hour}, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	cs := mustClaim(t, s, "abandoned", "g", 10, longLease)
	require.Equal(t, []string{"second"}, payloads(cs))
	held, err := ackOne(ctx, s, cs[0])
	require.NoError(t, err)
	require.True(t, held)

	mustAppend(t, s, first)
	assert.Equal(t, []string{"first"}, payloads(mustClaim(t, s, "abandoned", "g", 10, longLease)), "a late retry is delivered")
}

// TestClickHouseLateRetryLosesToTakeover pauses a Retry between reading a
// live lease and writing, until the lease has expired and another instance
// has claimed the delivery. The late write must not overwrite that claim, and
// must not be reported as held.
func TestClickHouseLateRetryLosesToTakeover(t *testing.T) {
	env := setupClickHouse(t)
	locker := NewProcessLocker()
	a, b := env.store(t, locker), env.store(t, locker)
	ctx := context.Background()
	mustGroup(t, a, "late-retry", "g", StartFromLatest)
	mustAppend(t, a, newMsg("late-retry", "", "x"))
	const lease = 500 * time.Millisecond
	old := mustClaim(t, a, "late-retry", "g", 1, lease)
	require.Len(t, old, 1)

	hook, paused, release := pauseAt("INSERT INTO queue_deliveries")
	defer release()
	type result struct {
		held bool
		err  error
	}
	retried := make(chan result, 1)
	go func() {
		held, err := a.Retry(withCHHook(ctx, hook), old[0], 0, "slow handler")
		retried <- result{held, err}
	}()
	<-paused
	time.Sleep(lease + 200*time.Millisecond)
	cs := mustClaim(t, b, "late-retry", "g", 1, longLease)
	release()
	res := <-retried
	require.NoError(t, res.err)

	require.Len(t, cs, 1)
	assert.False(t, res.held, "a write that landed after the lease ended is not held")
	held, err := ackOne(ctx, b, cs[0])
	require.NoError(t, err)
	assert.True(t, held, "the new holder's claim was overwritten")
}

// TestClickHouseLateAckLosesToTakeover pauses an acknowledgement between
// reading a live lease and writing, until another instance has claimed the
// delivery. The acknowledgement and the claim cannot both succeed.
func TestClickHouseLateAckLosesToTakeover(t *testing.T) {
	env := setupClickHouse(t)
	locker := NewProcessLocker()
	a, b := env.store(t, locker), env.store(t, locker)
	ctx := context.Background()
	mustGroup(t, a, "late-ack", "g", StartFromLatest)
	mustAppend(t, a, newMsg("late-ack", "", "x"))
	const lease = 500 * time.Millisecond
	old := mustClaim(t, b, "late-ack", "g", 1, lease)
	require.Len(t, old, 1)

	hook, paused, release := pauseAt("INSERT INTO queue_deliveries")
	defer release()
	type result struct {
		held bool
		err  error
	}
	acked := make(chan result, 1)
	go func() {
		held, err := ackOne(withCHHook(ctx, hook), b, old[0])
		acked <- result{held, err}
	}()
	<-paused
	time.Sleep(lease + 200*time.Millisecond)
	cs := mustClaim(t, a, "late-ack", "g", 1, longLease)
	release()
	res := <-acked
	require.NoError(t, res.err)

	assert.False(t, res.held && len(cs) == 1, "acknowledged and claimed again")
	assert.True(t, res.held || len(cs) == 1, "neither the acknowledgement nor the claim took effect")
	st, err := a.Stats(ctx, "late-ack", "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{Leased: 1}, st, "the takeover's lease stands")
}

// TestClickHouseDeadRowsKeptFromDeath kills a delivery after the retention
// counted from its message's storage has passed: the dead row and its message
// must stay visible, since retention for them runs from the death.
func TestClickHouseDeadRowsKeptFromDeath(t *testing.T) {
	env := setupClickHouse(t)
	s := env.store(t, NewProcessLocker())
	s.retention = time.Second
	ctx := context.Background()
	mustGroup(t, s, "dies-late", "g", StartFromLatest)
	m := newMsg("dies-late", "", "x")
	mustAppend(t, s, m)
	cs := mustClaim(t, s, "dies-late", "g", 1, longLease)
	require.Len(t, cs, 1)
	time.Sleep(s.retention + 300*time.Millisecond)

	held, err := s.Kill(ctx, cs[0], "boom")
	require.NoError(t, err)
	require.True(t, held)
	st, err := s.Stats(ctx, "dies-late", "g")
	require.NoError(t, err)
	assert.Equal(t, Stats{Dead: 1}, st, "the dead row is retained from its death")
	live, liveArg := s.liveWhere()
	var n int64
	require.NoError(t, env.db.Raw("SELECT count() FROM queue_messages FINAL WHERE topic = ? AND id = ? AND "+live, m.Topic, m.ID, liveArg).Scan(&n).Error)
	assert.Equal(t, int64(1), n, "the dead delivery's message is retained with it")
	for _, table := range []string{"queue_messages", "queue_deliveries"} {
		var engine string
		require.NoError(t, env.db.Raw("SELECT engine_full FROM system.tables WHERE database = currentDatabase() AND name = ?", table).Scan(&engine).Error)
		assert.Contains(t, engine, "dead_at", "%s TTL counts from the death", table)
	}
}

// TestClickHouseBackfillKeepsDeadRetention kills a message after its own
// retention has passed, so only its death keeps it, then registers an
// Earliest group: the group's new delivery must be kept as long as the
// message and reach the handler.
func TestClickHouseBackfillKeepsDeadRetention(t *testing.T) {
	env := setupClickHouse(t)
	s := env.store(t, NewProcessLocker())
	s.retention = time.Second
	ctx := context.Background()
	mustGroup(t, s, "dead-backfill", "g", StartFromLatest)
	mustAppend(t, s, newMsg("dead-backfill", "", "x"))
	cs := mustClaim(t, s, "dead-backfill", "g", 1, longLease)
	require.Len(t, cs, 1)
	time.Sleep(s.retention + 300*time.Millisecond)
	held, err := s.Kill(ctx, cs[0], "boom")
	require.NoError(t, err)
	require.True(t, held)

	mustGroup(t, s, "dead-backfill", "late", StartFromEarliest)
	assert.Equal(t, []string{"x"}, payloads(mustClaim(t, s, "dead-backfill", "late", 10, longLease)),
		"the backfilled delivery expired before its message")
}

// TestClickHouseLookupsUseSortingKey captures the lookups by id that publish,
// claim and acknowledge run, and checks with EXPLAIN that each prunes on the
// tables' sorting key instead of merging every part with FINAL.
func TestClickHouseLookupsUseSortingKey(t *testing.T) {
	env := setupClickHouse(t)
	s := env.store(t, NewProcessLocker())
	ctx := context.Background()
	// Many topics, each with more rows than a granule holds across them all:
	// a granule then spans several topics, so only the sorting key's leading
	// columns can rule it out, as in a real deployment.
	for i := range 50 {
		topic := fmt.Sprintf("bulk-%02d", i)
		mustGroup(t, s, topic, "g", StartFromLatest)
		msgs := make([]*Message, 400)
		for j := range msgs {
			msgs[j] = newMsg(topic, "", "b")
		}
		mustAppend(t, s, msgs...)
	}
	mustGroup(t, s, "probe", "g", StartFromLatest)

	var mu sync.Mutex
	var queries []string
	capture := context.WithValue(ctx, chCaptureKey{}, func(q string) {
		mu.Lock()
		queries = append(queries, q)
		mu.Unlock()
	})
	require.NoError(t, s.Append(capture, []*Message{newMsg("probe", "", "p")}))
	cs, err := s.Claim(capture, ClaimRequest{Topic: "probe", Group: "g", RunnerID: "r", Max: 1, Lease: longLease})
	require.NoError(t, err)
	require.Len(t, cs, 1)
	_, err = s.Ack(capture, cs)
	require.NoError(t, err)

	granules := regexp.MustCompile(`Granules: (\d+)/(\d+)`)
	checked := 0
	for _, q := range queries {
		// The claim's own scan already filters on topic and group.
		if !strings.Contains(q, " id IN (") || strings.HasPrefix(q, "WITH ") {
			continue
		}
		var plan []string
		require.NoError(t, env.db.Raw("EXPLAIN indexes = 1 "+q).Scan(&plan).Error)
		matches := granules.FindAllStringSubmatch(strings.Join(plan, "\n"), -1)
		require.NotEmpty(t, matches, q)
		last := matches[len(matches)-1]
		assert.NotEqual(t, last[2], last[1], "reads every granule: %s", q)
		checked++
	}
	assert.GreaterOrEqual(t, checked, 3, "existence check, message load and held-row read")
}

func BenchmarkClickHouseQueue(b *testing.B) {
	runBenchmarks(b, benchBackend{
		fresh: func(b *testing.B) (Store, func()) {
			env := setupClickHouse(b)
			return env.store(b, NewProcessLocker()), func() { _ = env.logStore.Close(context.Background()) }
		},
		footprint: func(b *testing.B, s Store) int64 {
			var bytes int64
			require.NoError(b, s.(*clickhouseStore).db.Raw("SELECT sum(bytes_on_disk) FROM system.parts WHERE active AND database = currentDatabase() "+
				"AND table IN ('queue_groups', 'queue_messages', 'queue_deliveries')").Scan(&bytes).Error)
			return bytes
		},
		compact: func(b *testing.B, s Store) {
			for _, table := range []string{"queue_groups", "queue_messages", "queue_deliveries"} {
				require.NoError(b, s.(*clickhouseStore).db.Exec("OPTIMIZE TABLE "+table+" FINAL").Error)
			}
		},
		backlogs: []int{10_000, 100_000},
	})
}
