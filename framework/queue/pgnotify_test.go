package queue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/postgresconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// pgProbeTopic is notified by tests to learn that a LISTEN session is live;
// signalStore swallows it.
const pgProbeTopic = "listen-probe"

// signalStore reports when its LISTEN session receives a probe and when its
// first claim has run, so tests wait on events rather than sleeps.
type signalStore struct {
	*sqlStore
	probes    chan struct{}
	claimed   chan struct{}
	claimOnce sync.Once
}

func (s *signalStore) Listen(ctx context.Context, wake func(topic string)) error {
	return s.sqlStore.Listen(ctx, func(topic string) {
		if topic != pgProbeTopic {
			wake(topic)
			return
		}
		select {
		case s.probes <- struct{}{}:
		default:
		}
	})
}

func (s *signalStore) Claim(ctx context.Context, req ClaimRequest) ([]*Claimed, error) {
	cs, err := s.sqlStore.Claim(ctx, req)
	s.claimOnce.Do(func() { close(s.claimed) })
	return cs, err
}

// pgTestInstance is one instance with its own connection pool.
type pgTestInstance struct {
	Queue
	db    *gorm.DB
	store *signalStore
}

func newPostgresTestQueue(t *testing.T) *pgTestInstance {
	t.Helper()
	db := openTestPostgres(t)
	s, err := newSQLStore(context.Background(), db, nil)
	require.NoError(t, err)
	ss := &signalStore{sqlStore: s, probes: make(chan struct{}, 1), claimed: make(chan struct{})}
	q, err := NewStoreQueue(ss, EngineConfig{}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close(context.Background()) })
	return &pgTestInstance{Queue: q, db: db, store: ss}
}

// awaitListening notifies probes until p's LISTEN session reports one.
func (p *pgTestInstance) awaitListening(t *testing.T) {
	t.Helper()
	select {
	case <-p.store.probes: // a stale probe from an earlier session
	default:
	}
	deadline := time.After(10 * time.Second)
	for {
		require.NoError(t, p.db.Exec("SELECT pg_notify(?, ?)", pgNotifyChannel, pgProbeTopic).Error)
		select {
		case <-p.store.probes:
			return
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatal("the LISTEN session never came up")
		}
	}
}

// awaitDelivery subscribes on consumer with a poll interval far longer than
// the test, waits until its first, empty claim has run and its LISTEN session
// is live, publishes on producer, and returns how long delivery took.
func awaitDelivery(t *testing.T, consumer, producer *pgTestInstance, topic string, publishAfter func()) time.Duration {
	t.Helper()
	got := make(chan time.Time, 1)
	_, err := consumer.Subscribe(context.Background(), topic, "g", func(ctx context.Context, d *Delivery) error {
		got <- time.Now()
		return nil
	}, WithPollInterval(time.Minute))
	require.NoError(t, err)
	select {
	case <-consumer.store.claimed:
	case <-time.After(10 * time.Second):
		t.Fatal("the subscription never claimed")
	}
	consumer.awaitListening(t)
	if publishAfter != nil {
		publishAfter()
	}
	sent := time.Now()
	require.NoError(t, producer.Publish(context.Background(), &Message{Topic: topic, Payload: []byte("x")}))
	select {
	case at := <-got:
		return at.Sub(sent)
	case <-time.After(10 * time.Second):
		t.Fatal("delivery waited for the poll interval: no cross-instance wakeup")
		return 0
	}
}

func TestPostgresNotifyWakesOtherInstances(t *testing.T) {
	resetTestPostgres(t)
	consumer, producer := newPostgresTestQueue(t), newPostgresTestQueue(t)
	assert.Less(t, awaitDelivery(t, consumer, producer, "wake", nil), 2*time.Second)
}

// TestPostgresListenReconnects kills the listening session, as a database
// failover or idle-connection reaper would, and expects LISTEN to come back.
func TestPostgresListenReconnects(t *testing.T) {
	db := resetTestPostgres(t)
	consumer, producer := newPostgresTestQueue(t), newPostgresTestQueue(t)
	elapsed := awaitDelivery(t, consumer, producer, "reconnect", func() {
		var killed []bool
		require.NoError(t, db.Raw("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE query = ? AND pid <> pg_backend_pid()",
			"LISTEN "+pgNotifyChannel).Scan(&killed).Error)
		require.NotEmpty(t, killed, "no listening session found")
		consumer.awaitListening(t)
	})
	assert.Less(t, elapsed, 2*time.Second)
}

func TestSQLiteHasNoWakeSource(t *testing.T) {
	err := newTestSQLiteStore(t).Listen(context.Background(), func(string) {})
	assert.ErrorIs(t, err, ErrUnsupported)
}

// listenerGoroutines counts goroutines inside the LISTEN session.
func listenerGoroutines() int {
	buf := make([]byte, 1<<22)
	buf = buf[:runtime.Stack(buf, true)]
	return strings.Count(string(buf), "queue.listenOnce")
}

// TestPostgresListenerStopsOnClose checks the LISTEN goroutine runs while the
// queue is open and is gone after Close, with its connection given back.
func TestPostgresListenerStopsOnClose(t *testing.T) {
	resetTestPostgres(t)
	db := openTestPostgres(t)
	baseline := listenerGoroutines()
	s, err := newSQLStore(context.Background(), db, nil)
	require.NoError(t, err)
	q, err := NewStoreQueue(s, EngineConfig{}, nil)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return listenerGoroutines() > baseline }, 5*time.Second, 10*time.Millisecond)

	require.NoError(t, q.Close(context.Background()))
	require.Eventually(t, func() bool { return listenerGoroutines() == baseline }, 5*time.Second, 10*time.Millisecond)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	assert.Zero(t, sqlDB.Stats().InUse, "the listener holds no pooled connection")
}

// TestListenRetryResetsAfterListening feeds the reconnect loop sessions that
// fail before or after reaching LISTEN. Failed connection attempts back off
// further each time; a session that was listening starts again from the
// shortest delay.
func TestListenRetryResetsAfterListening(t *testing.T) {
	defer func(lo, hi time.Duration) { pgListenRetryMin, pgListenRetryMax = lo, hi }(pgListenRetryMin, pgListenRetryMax)
	pgListenRetryMin, pgListenRetryMax = time.Millisecond, 30*time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listened := []bool{false, false, false, true, false}
	var waits []time.Duration
	var call int
	err := listenLoop(ctx, func(ctx context.Context, listening func()) error {
		if call == len(listened) {
			cancel()
			return ctx.Err()
		}
		if listened[call] {
			listening()
		}
		call++
		return errors.New("dropped")
	}, func(_ string, args ...any) {
		waits = append(waits, args[0].(time.Duration))
	})
	require.NoError(t, err)
	ms := time.Millisecond
	assert.Equal(t, []time.Duration{ms, 2 * ms, 4 * ms, ms, 2 * ms}, waits)
}

// TestPostgresListenerLeavesPoolFree runs a queue on a pool of one
// connection: the LISTEN session must not take it from the queue's own work.
func TestPostgresListenerLeavesPoolFree(t *testing.T) {
	resetTestPostgres(t)
	db := openTestPostgres(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	s, err := newSQLStore(context.Background(), db, nil)
	require.NoError(t, err)
	ss := &signalStore{sqlStore: s, probes: make(chan struct{}, 1), claimed: make(chan struct{})}
	q, err := NewStoreQueue(ss, EngineConfig{}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close(context.Background()) })
	(&pgTestInstance{Queue: q, db: db, store: ss}).awaitListening(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := make(chan struct{}, 1)
	_, err = q.Subscribe(ctx, "one-conn", "g", func(context.Context, *Delivery) error {
		got <- struct{}{}
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, q.Publish(ctx, &Message{Topic: "one-conn", Payload: []byte("x")}))
	select {
	case <-got:
	case <-ctx.Done():
		t.Fatal("not delivered: the listener holds the pool's only connection")
	}
}

// TestPostgresListenerUsesFreshCredentials rotates the password of a pool
// that fetches it with a password command, after the pool has connected: a
// new LISTEN session must run the command again rather than reuse the
// password a pooled connection was opened with.
func TestPostgresListenerUsesFreshCredentials(t *testing.T) {
	admin := openTestPostgres(t)
	const role = "queue_test_rotating"
	require.NoError(t, admin.Exec("DROP ROLE IF EXISTS "+role).Error)
	require.NoError(t, admin.Exec("CREATE ROLE "+role+" LOGIN PASSWORD 'first'").Error)
	t.Cleanup(func() { _ = admin.Exec("DROP ROLE IF EXISTS " + role).Error })
	passwordFile := filepath.Join(t.TempDir(), "password")
	require.NoError(t, os.WriteFile(passwordFile, []byte("first"), 0o600))

	db, err := postgresconn.Open("host=localhost port=5432 user="+role+" dbname=bifrost sslmode=disable",
		&postgresconn.Config{PasswordCommand: &postgresconn.PasswordCommandConfig{Command: "cat", Args: []string{passwordFile}, CacheTTL: "1ms"}},
		gormlogger.Default.LogMode(gormlogger.Silent))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, sqlDB.Ping(), "the pool connects with the first password")

	require.NoError(t, admin.Exec("ALTER ROLE "+role+" PASSWORD 'second'").Error)
	require.NoError(t, os.WriteFile(passwordFile, []byte("second"), 0o600))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listening := make(chan struct{})
	ended := make(chan error, 1)
	go func() { ended <- listenOnce(ctx, sqlDB, func(string) {}, func() { close(listening) }) }()
	select {
	case <-listening:
	case err := <-ended:
		t.Fatalf("LISTEN failed after the password rotated: %v", err)
	case <-ctx.Done():
		t.Fatal("LISTEN never came up")
	}
	cancel()
	<-ended
}

// stuckWakeStore is a memory store whose Listen ignores cancellation, like a
// listener stuck on a stalled connection.
type stuckWakeStore struct {
	*memoryStore
	unblock chan struct{}
}

func (s stuckWakeStore) Listen(ctx context.Context, wake func(string)) error {
	<-s.unblock
	return nil
}

// TestCloseStopsWaitingForAStuckListener closes a queue whose listener never
// returns: Close must give up at its deadline and say so.
func TestCloseStopsWaitingForAStuckListener(t *testing.T) {
	store := stuckWakeStore{memoryStore: newMemoryStore(), unblock: make(chan struct{})}
	var once sync.Once
	defer once.Do(func() { close(store.unblock) })
	q, err := NewStoreQueue(store, EngineConfig{}, nil)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	closed := make(chan error, 1)
	go func() { closed <- q.Close(ctx) }()
	select {
	case err := <-closed:
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(3 * time.Second):
		once.Do(func() { close(store.unblock) })
		<-closed
		t.Fatal("Close waited for the listener past its deadline")
	}
}
