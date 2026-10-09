package queue

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newTestSQLiteLogStore(t *testing.T) logstore.LogStore {
	t.Helper()
	ls, err := logstore.NewLogStore(context.Background(), &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypeSQLite,
		Config:  &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ls.Close(context.Background()) })
	return ls
}

func TestLogStoreQueueEndToEnd(t *testing.T) {
	ls := newTestSQLiteLogStore(t)
	q, err := NewQueue(context.Background(), &Config{
		Enabled: true,
		Type:    QueueTypeLogStore,
		Config:  &LogStoreQueueConfig{EngineConfig: EngineConfig{RetentionHours: 1}},
	}, Dependencies{LogStore: ls}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	require.NoError(t, q.Ping(context.Background()))

	got := make(chan *Delivery, 1)
	subscribe(t, q, "events", "consumer", func(ctx context.Context, d *Delivery) error {
		got <- d
		return nil
	})
	m := &Message{Topic: "events", Key: "k", Payload: []byte("hello"), Headers: map[string]string{"a": "b"}}
	publish(t, q, m)
	select {
	case d := <-got:
		assert.Equal(t, m.ID, d.ID)
		assert.Equal(t, "k", d.Key)
		assert.Equal(t, []byte("hello"), d.Payload)
		assert.Equal(t, map[string]string{"a": "b"}, d.Headers)
	case <-time.After(5 * time.Second):
		t.Fatal("message not delivered")
	}
	waitStats(t, q, "events", "consumer", Stats{})

	// Closing the queue leaves the logstore usable: it does not own it.
	require.NoError(t, q.Close(context.Background()))
	require.NoError(t, ls.Ping(context.Background()))
}

// delegatingLogStore wraps a logstore and forwards ScopedDB the way
// HybridLogStore does when it wraps an SQL logstore.
type delegatingLogStore struct {
	logstore.LogStore
	inner scopedDBStore
}

func (d delegatingLogStore) ScopedDB(ctx context.Context) *gorm.DB {
	if d.inner == nil {
		return nil
	}
	return d.inner.ScopedDB(ctx)
}

func TestLogStoreQueueThroughWrapper(t *testing.T) {
	ls := newTestSQLiteLogStore(t)
	wrapped := delegatingLogStore{LogStore: ls, inner: ls.(scopedDBStore)}
	q, err := NewQueue(context.Background(), &Config{Enabled: true, Type: QueueTypeLogStore}, Dependencies{LogStore: wrapped}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close(context.Background()) })
	require.NoError(t, q.Ping(context.Background()))
}

// noDBLogStore is a logstore with no SQL surface at all.
type noDBLogStore struct{ logstore.LogStore }

func TestLogStoreQueueUnsupportedStores(t *testing.T) {
	ctx := context.Background()
	cfg := &Config{Enabled: true, Type: QueueTypeLogStore}

	_, err := NewQueue(ctx, cfg, Dependencies{}, nil)
	assert.Error(t, err, "a logstore is required")

	_, err = NewQueue(ctx, cfg, Dependencies{LogStore: noDBLogStore{}}, nil)
	assert.ErrorIs(t, err, ErrUnsupported)

	// A hybrid store over a backend without a SQL handle returns nil.
	_, err = NewQueue(ctx, cfg, Dependencies{LogStore: delegatingLogStore{}}, nil)
	assert.ErrorIs(t, err, ErrUnsupported)

	_, err = NewQueue(ctx, &Config{Enabled: true, Type: QueueTypeLogStore, Config: &MemoryQueueConfig{}}, Dependencies{LogStore: newTestSQLiteLogStore(t)}, nil)
	assert.Error(t, err, "wrong config type is rejected")
}

func TestLogStoreQueueSharesTheLogstoreDatabase(t *testing.T) {
	ls := newTestSQLiteLogStore(t)
	q, err := NewQueue(context.Background(), &Config{Enabled: true, Type: QueueTypeLogStore}, Dependencies{LogStore: ls}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = q.Close(context.Background()) })
	db := ls.(scopedDBStore).ScopedDB(context.Background())
	for _, table := range []string{"queue_topics", "queue_groups", "queue_messages", "queue_deliveries", "logs"} {
		assert.True(t, db.Migrator().HasTable(table), table)
	}
}

// logStoreEngineBackend builds the engine contract's stores the production
// way: through the logstore adapter, over the logstore's own database pool.
func logStoreEngineBackend(newLogStore func(t *testing.T) logstore.LogStore, deps func(ls logstore.LogStore) Dependencies) engineBackend {
	return func(t *testing.T, n int) []Store {
		ls := newLogStore(t)
		out := make([]Store, n)
		for i := range out {
			s, err := newLogStoreBackend(context.Background(), deps(ls), nil)
			require.NoError(t, err)
			out[i] = s
		}
		return out
	}
}

func TestSQLiteEngineContract(t *testing.T) {
	runEngineContract(t, logStoreEngineBackend(newTestSQLiteLogStore, func(ls logstore.LogStore) Dependencies {
		return Dependencies{LogStore: ls}
	}))
}
