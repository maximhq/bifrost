package kvstore

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type recordingSyncDelegate struct {
	mu        sync.Mutex
	setCalls  int
	lastKey   string
	lastValue []byte
}

func (d *recordingSyncDelegate) OnSet(key string, valueJSON []byte, _ int64, _ int64) {
	d.mu.Lock()
	d.setCalls++
	d.lastKey = key
	d.lastValue = append(d.lastValue[:0], valueJSON...)
	d.mu.Unlock()
}

func (d *recordingSyncDelegate) OnDelete(string, int64) {}

func (d *recordingSyncDelegate) snapshot() (calls int, key string, value []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.setCalls, d.lastKey, append([]byte(nil), d.lastValue...)
}

func TestStoreSetGetDelete(t *testing.T) {
	store, err := New(Config{})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.Set("k1", "v1"); err != nil {
		t.Fatalf("set failed: %v", err)
	}

	v, err := store.Get("k1")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if v.(string) != "v1" {
		t.Fatalf("unexpected value: %v", v)
	}

	deleted, err := store.Delete("k1")
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if !deleted {
		t.Fatal("expected key to be deleted")
	}

	if _, err := store.Get("k1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

func TestStoreTTLExpiration(t *testing.T) {
	store, err := New(Config{
		CleanupInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetWithTTL("exp", "value", 25*time.Millisecond); err != nil {
		t.Fatalf("set with ttl failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if _, err := store.Get("exp"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after expiry, got: %v", err)
	}
}

func TestStoreGetAndDelete(t *testing.T) {
	store, err := New(Config{})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.Set("k", "v"); err != nil {
		t.Fatalf("set failed: %v", err)
	}

	v, err := store.GetAndDelete("k")
	if err != nil {
		t.Fatalf("get and delete failed: %v", err)
	}
	if v.(string) != "v" {
		t.Fatalf("unexpected value: %v", v)
	}

	if _, err := store.Get("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected missing key after get-and-delete, got: %v", err)
	}
}

func TestStoreUpdateWithTTL(t *testing.T) {
	store, err := New(Config{})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetWithTTL("counter", 1, time.Second); err != nil {
		t.Fatalf("set failed: %v", err)
	}

	value, found, err := store.UpdateWithTTL("counter", time.Second, func(current any) (any, error) {
		return current.(int) + 1, nil
	})
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if !found {
		t.Fatal("expected existing key to be updated")
	}
	if value.(int) != 2 {
		t.Fatalf("unexpected updated value: %v", value)
	}

	stored, err := store.Get("counter")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if stored.(int) != 2 {
		t.Fatalf("unexpected stored value: %v", stored)
	}
}

// A nil callback is a programming error, not a no-op update: without the guard
// the store would panic while holding its write lock and take every other
// caller down with it.
func TestStoreUpdateWithTTLNilCallback(t *testing.T) {
	store, err := New(Config{})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.Set("key", "original"); err != nil {
		t.Fatalf("set failed: %v", err)
	}

	value, found, err := store.UpdateWithTTL("key", time.Second, nil)
	if !errors.Is(err, ErrNilUpdate) {
		t.Fatalf("expected ErrNilUpdate, got: %v", err)
	}
	if value != nil || found {
		t.Fatalf("expected no result for a nil callback, got value=%v found=%v", value, found)
	}
}

func TestStoreUpdateWithTTLMissingOrExpired(t *testing.T) {
	store, err := New(Config{CleanupInterval: time.Hour})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	var calls atomic.Int32
	update := func(current any) (any, error) {
		calls.Add(1)
		return current, nil
	}

	if value, found, err := store.UpdateWithTTL("missing", time.Second, update); err != nil || found || value != nil {
		t.Fatalf("expected missing result, got value=%v found=%v err=%v", value, found, err)
	}

	if err := store.SetWithTTL("expired", "value", time.Millisecond); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if value, found, err := store.UpdateWithTTL("expired", time.Second, update); err != nil || found || value != nil {
		t.Fatalf("expected expired result, got value=%v found=%v err=%v", value, found, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("update callback ran %d times for missing records", calls.Load())
	}
}

func TestStoreUpdateWithTTLFailureDoesNotMutate(t *testing.T) {
	store, err := New(Config{})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.Set("key", "original"); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	wantErr := errors.New("update failed")
	_, found, err := store.UpdateWithTTL("key", time.Second, func(any) (any, error) {
		return "replacement", wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected update error, got: %v", err)
	}
	if !found {
		t.Fatal("expected update to report the existing key")
	}

	value, err := store.Get("key")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if value.(string) != "original" {
		t.Fatalf("failed update changed value to %v", value)
	}
}

func TestStoreUpdateWithTTLConcurrent(t *testing.T) {
	store, err := New(Config{})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.Set("counter", 0); err != nil {
		t.Fatalf("set failed: %v", err)
	}

	const updates = 100
	errCh := make(chan error, updates)
	var wg sync.WaitGroup
	for range updates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, found, err := store.UpdateWithTTL("counter", time.Second, func(current any) (any, error) {
				return current.(int) + 1, nil
			})
			if err != nil {
				errCh <- err
				return
			}
			if !found {
				errCh <- ErrNotFound
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent update failed: %v", err)
	}

	value, err := store.Get("counter")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if value.(int) != updates {
		t.Fatalf("lost concurrent updates: got %v, want %d", value, updates)
	}
}

func TestStoreUpdateWithTTLDelegate(t *testing.T) {
	store, err := New(Config{})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if store.HasSyncDelegate() {
		t.Fatal("new store unexpectedly has a sync delegate")
	}
	delegate := &recordingSyncDelegate{}
	store.SetDelegate(delegate)
	if !store.HasSyncDelegate() {
		t.Fatal("expected configured sync delegate")
	}

	if err := store.Set("counter", 1); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	if _, _, err := store.UpdateWithTTL("counter", time.Second, func(current any) (any, error) {
		return current.(int) + 1, nil
	}); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	calls, key, valueJSON := delegate.snapshot()
	if calls != 2 {
		t.Fatalf("unexpected delegate call count: got %d, want 2", calls)
	}
	if key != "counter" {
		t.Fatalf("unexpected delegate key: %q", key)
	}
	var value int
	if err := json.Unmarshal(valueJSON, &value); err != nil {
		t.Fatalf("decode delegated value: %v", err)
	}
	if value != 2 {
		t.Fatalf("unexpected delegated value: got %d, want 2", value)
	}

	store.SetDelegate(nil)
	if store.HasSyncDelegate() {
		t.Fatal("expected sync delegate to be cleared")
	}
}

func TestStoreUpdateWithTTLMarshalFailureDoesNotMutate(t *testing.T) {
	store, err := New(Config{})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()
	store.SetDelegate(&recordingSyncDelegate{})

	if err := store.Set("key", "original"); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	_, found, err := store.UpdateWithTTL("key", time.Second, func(any) (any, error) {
		return make(chan struct{}), nil
	})
	if err == nil {
		t.Fatal("expected marshal failure")
	}
	if !found {
		t.Fatal("expected update to report the existing key")
	}

	value, getErr := store.Get("key")
	if getErr != nil {
		t.Fatalf("get failed: %v", getErr)
	}
	if value.(string) != "original" {
		t.Fatalf("marshal failure changed value to %v", value)
	}
}

func TestStoreClose(t *testing.T) {
	store, err := New(Config{})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	if err := store.Set("k", "v"); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed on set, got: %v", err)
	}
	if _, err := store.Get("k"); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed on get, got: %v", err)
	}
}
