package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var errConfigStoreUnavailable = errors.New("config store unavailable")

// stubDimensionConfigStore is the one config row this store owns, without a
// database behind it.
type stubDimensionConfigStore struct {
	configstore.ConfigStore
	rows            map[string]string
	getErr          error
	putErr          error
	getCalls        int
	putCalls        int
	lockUpdateCalls int
	lockUpdateErr   error

	lockMu sync.Mutex
	locks  map[string]*tables.TableDistributedLock
	// stealLockDuringSweep makes every ownership check report the lock as held
	// by somebody else, standing in for a lease that lapsed mid-sweep.
	stealLockDuringSweep bool
}

// expireLocks drops every held lock, as a lapsed lease would.
func (s *stubDimensionConfigStore) expireLocks() {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	s.locks = map[string]*tables.TableDistributedLock{}
}

func newStubDimensionConfigStore() *stubDimensionConfigStore {
	return &stubDimensionConfigStore{rows: map[string]string{}}
}

func (s *stubDimensionConfigStore) GetConfig(_ context.Context, key string) (*tables.TableGovernanceConfig, error) {
	s.getCalls++
	if s.getErr != nil {
		return nil, s.getErr
	}
	value, ok := s.rows[key]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return &tables.TableGovernanceConfig{Key: key, Value: value}, nil
}

func (s *stubDimensionConfigStore) UpdateConfig(_ context.Context, config *tables.TableGovernanceConfig, _ ...*gorm.DB) error {
	s.putCalls++
	if s.putErr != nil {
		return s.putErr
	}
	s.rows[config.Key] = config.Value
	return nil
}

// The registry takes a distributed lock through this same store, so the stub
// implements LockStore too — in memory, which is all a single-process test
// needs to exercise mutual exclusion.
func (s *stubDimensionConfigStore) TryAcquireLock(_ context.Context, lock *tables.TableDistributedLock) (bool, error) {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	if s.locks == nil {
		s.locks = map[string]*tables.TableDistributedLock{}
	}
	if existing, held := s.locks[lock.LockKey]; held && existing.ExpiresAt.After(time.Now()) {
		return false, nil
	}
	copied := *lock
	s.locks[lock.LockKey] = &copied
	return true, nil
}

func (s *stubDimensionConfigStore) GetLock(_ context.Context, lockKey string) (*tables.TableDistributedLock, error) {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	if s.stealLockDuringSweep {
		// Held, but by someone else: exactly what a node sees after its own
		// lease expired and a peer took over.
		return &tables.TableDistributedLock{LockKey: lockKey, HolderID: "node-peer", ExpiresAt: time.Now().Add(time.Minute)}, nil
	}
	return s.locks[lockKey], nil
}

func (s *stubDimensionConfigStore) UpdateLockExpiry(_ context.Context, lockKey, holderID string, expiresAt time.Time) error {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	s.lockUpdateCalls++
	if s.lockUpdateErr != nil {
		return s.lockUpdateErr
	}
	if lock, held := s.locks[lockKey]; held && lock.HolderID == holderID {
		lock.ExpiresAt = expiresAt
	}
	return nil
}

func (s *stubDimensionConfigStore) ReleaseLock(_ context.Context, lockKey, holderID string) (bool, error) {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	if lock, held := s.locks[lockKey]; held && lock.HolderID == holderID {
		delete(s.locks, lockKey)
		return true, nil
	}
	return false, nil
}

func (s *stubDimensionConfigStore) CleanupExpiredLocks(context.Context) (int64, error) {
	return 0, nil
}

func (s *stubDimensionConfigStore) CleanupExpiredLockByKey(_ context.Context, lockKey string) (bool, error) {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	if lock, held := s.locks[lockKey]; held && !lock.ExpiresAt.After(time.Now()) {
		delete(s.locks, lockKey)
		return true, nil
	}
	return false, nil
}

func newTestDimensionStore(t *testing.T, backing configstore.ConfigStore) *persistentEmbeddingDimensionStore {
	t.Helper()
	store := newPersistentEmbeddingDimensionStore(context.Background(), backing, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NotNil(t, store)
	return store
}

// TestDimensionStoreSurvivesARestart is the behaviour the row exists for: a new
// process reads back what an earlier one measured.
func TestDimensionStoreSurvivesARestart(t *testing.T) {
	backing := newStubDimensionConfigStore()

	first := newTestDimensionStore(t, backing)
	first.Remember("openai\x00text-embedding-3-small", 1536)

	// A fresh instance, as after a restart, sees the persisted value.
	second := newTestDimensionStore(t, backing)
	dimension, ok := second.Dimension("openai\x00text-embedding-3-small")
	require.True(t, ok)
	assert.Equal(t, 1536, dimension)

	_, ok = second.Dimension("openai\x00some-other-model")
	assert.False(t, ok)
}

// TestDimensionStoreWritesOnlyOnChange keeps a warm on every config save from
// rewriting an unchanged row.
func TestDimensionStoreWritesOnlyOnChange(t *testing.T) {
	backing := newStubDimensionConfigStore()
	store := newTestDimensionStore(t, backing)

	store.Remember("openai\x00model-a", 1536)
	require.Equal(t, 1, backing.putCalls)
	store.Remember("openai\x00model-a", 1536)
	assert.Equal(t, 1, backing.putCalls, "an unchanged width must not rewrite the row")

	// A model re-versioned under the same name overwrites rather than sticking.
	store.Remember("openai\x00model-a", 3072)
	assert.Equal(t, 2, backing.putCalls)
	dimension, ok := store.Dimension("openai\x00model-a")
	require.True(t, ok)
	assert.Equal(t, 3072, dimension)
}

// TestDimensionStoreRejectsUnusableValues keeps a width that cannot identify a
// generation out of the row.
func TestDimensionStoreRejectsUnusableValues(t *testing.T) {
	backing := newStubDimensionConfigStore()
	store := newTestDimensionStore(t, backing)

	store.Remember("", 1536)
	store.Remember("openai\x00model-a", 0)
	store.Remember("openai\x00model-a", 1)
	store.Remember("openai\x00model-a", -8)
	assert.Zero(t, backing.putCalls)

	_, ok := store.Dimension("")
	assert.False(t, ok)
}

// TestDimensionStoreToleratesAnUnreadableRow keeps a corrupt or foreign value
// from blocking a warm — the width is re-measured and the row replaced.
func TestDimensionStoreToleratesAnUnreadableRow(t *testing.T) {
	backing := newStubDimensionConfigStore()
	backing.rows[tables.ConfigComplexitySemanticDimensionsKey] = "{not json"

	store := newTestDimensionStore(t, backing)
	_, ok := store.Dimension("openai\x00model-a")
	assert.False(t, ok)

	store.Remember("openai\x00model-a", 1536)
	var decoded map[string]int
	require.NoError(t, json.Unmarshal([]byte(backing.rows[tables.ConfigComplexitySemanticDimensionsKey]), &decoded))
	assert.Equal(t, 1536, decoded["openai\x00model-a"])
}

// TestDimensionStoreToleratesAFailingConfigStore keeps persistence problems to
// their real cost — one batch of embeddings next boot, not a failed warm.
func TestDimensionStoreToleratesAFailingConfigStore(t *testing.T) {
	backing := newStubDimensionConfigStore()
	backing.getErr = errConfigStoreUnavailable
	store := newTestDimensionStore(t, backing)
	_, ok := store.Dimension("openai\x00model-a")
	assert.False(t, ok)

	writeBacking := newStubDimensionConfigStore()
	writeBacking.putErr = errConfigStoreUnavailable
	writeStore := newTestDimensionStore(t, writeBacking)
	assert.NotPanics(t, func() { writeStore.Remember("openai\x00model-a", 1536) })
	// The value is still usable in this process even though the row write failed.
	dimension, ok := writeStore.Dimension("openai\x00model-a")
	require.True(t, ok)
	assert.Equal(t, 1536, dimension)
}

// TestDimensionStoreNilBackingDisablesMemory documents deployments with no
// config store: every boot measures, exactly as before.
func TestDimensionStoreNilBackingDisablesMemory(t *testing.T) {
	assert.Nil(t, newPersistentEmbeddingDimensionStore(context.Background(), nil, nil))

	var store *persistentEmbeddingDimensionStore
	_, ok := store.Dimension("openai\x00model-a")
	assert.False(t, ok)
	assert.NotPanics(t, func() { store.Remember("openai\x00model-a", 1536) })
}

// TestDimensionStoreConcurrentRememberDoesNotPersistStaleSnapshot guards the
// row against reversed writes: two callers that mutate and then persist outside
// the lock can leave the store holding the older snapshot.
func TestDimensionStoreConcurrentRememberDoesNotPersistStaleSnapshot(t *testing.T) {
	backing := newStubDimensionConfigStore()
	store := newTestDimensionStore(t, backing)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			store.Remember(fmt.Sprintf("openai\x00model-%d", n), 1536+n)
		}(i)
	}
	wg.Wait()

	var persisted map[string]int
	require.NoError(t, json.Unmarshal([]byte(backing.rows[tables.ConfigComplexitySemanticDimensionsKey]), &persisted))
	assert.Len(t, persisted, 8, "every width must survive in the persisted row")
	for i := range 8 {
		identity := fmt.Sprintf("openai\x00model-%d", i)
		assert.Equal(t, 1536+i, persisted[identity], "width for %s lost to a reversed write", identity)
	}
}

// TestDimensionStoreRetriesAfterAFailedRead keeps one transient config-store
// error from disabling the memory for the life of the process, which would make
// every later save re-measure a width sitting unread in the row.
func TestDimensionStoreRetriesAfterAFailedRead(t *testing.T) {
	backing := newStubDimensionConfigStore()
	backing.rows[tables.ConfigComplexitySemanticDimensionsKey] = `{"openai\u0000model-a":1536}`
	backing.getErr = errConfigStoreUnavailable

	store := newTestDimensionStore(t, backing)
	_, ok := store.Dimension("openai\x00model-a")
	require.False(t, ok, "an unreadable row cannot answer")

	backing.getErr = nil
	dimension, ok := store.Dimension("openai\x00model-a")
	require.True(t, ok, "the store must retry the read after a transient failure")
	assert.Equal(t, 1536, dimension)
}
