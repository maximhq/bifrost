package kvstore

import (
	"errors"
	"testing"
	"time"
)

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

func TestStoreCompareAndSwapStringWithTTL(t *testing.T) {
	store, err := New(Config{})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetWithTTL("binding", "a", time.Hour); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	if swapped, err := store.CompareAndSwapStringWithTTL("binding", "wrong", "b", time.Hour); err != nil || swapped {
		t.Fatalf("mismatched CAS should lose without error, swapped=%v err=%v", swapped, err)
	}
	if swapped, err := store.CompareAndSwapStringWithTTL("binding", "a", "b", time.Hour); err != nil || !swapped {
		t.Fatalf("matching CAS should win, swapped=%v err=%v", swapped, err)
	}
	if value, err := store.Get("binding"); err != nil || value != "b" {
		t.Fatalf("expected replacement b, got %v err=%v", value, err)
	}
}

func TestStoreCompareAndSwapStringWithTTLDoesNotResurrectMissingOrExpired(t *testing.T) {
	store, err := New(Config{})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if swapped, err := store.CompareAndSwapStringWithTTL("missing", "a", "b", time.Hour); err != nil || swapped {
		t.Fatalf("missing CAS should lose without error, swapped=%v err=%v", swapped, err)
	}
	if err := store.SetWithTTL("expired", "a", time.Nanosecond); err != nil {
		t.Fatalf("set expired failed: %v", err)
	}
	time.Sleep(time.Millisecond)
	if swapped, err := store.CompareAndSwapStringWithTTL("expired", "a", "b", time.Hour); err != nil || swapped {
		t.Fatalf("expired CAS should lose without resurrection, swapped=%v err=%v", swapped, err)
	}
}

func TestStoreCompareAndSwapStringWithTTLHandlesJSONBytes(t *testing.T) {
	store, err := New(Config{})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	store.mu.Lock()
	store.data["json"] = entry{value: []byte(`"a"`), writtenAt: time.Now().UnixNano()}
	store.mu.Unlock()
	if swapped, err := store.CompareAndSwapStringWithTTL("json", "a", "b", time.Hour); err != nil || !swapped {
		t.Fatalf("JSON string CAS should win, swapped=%v err=%v", swapped, err)
	}
}

func TestStoreCompareAndSwapStringWithTTLRejectsDelegatedStore(t *testing.T) {
	store, err := New(Config{})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()
	store.SetDelegate(testSyncDelegate{})
	if swapped, err := store.CompareAndSwapStringWithTTL("binding", "a", "b", time.Hour); !errors.Is(err, ErrConditionalUnsupported) || swapped {
		t.Fatalf("delegated CAS should fail closed, swapped=%v err=%v", swapped, err)
	}
}

type testSyncDelegate struct{}

func (testSyncDelegate) OnSet(string, []byte, int64, int64) {}
func (testSyncDelegate) OnDelete(string, int64)             {}

func TestStoreDelegateAttachConcurrentWithCAS(t *testing.T) {
	store, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_ = store.Set("binding", "a")
	start := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-start
		for i := 0; i < 1000; i++ {
			store.SetDelegate(testSyncDelegate{})
			store.SetDelegate(nil)
		}
	}()
	close(start)
	for i := 0; i < 1000; i++ {
		store.SupportsProcessLocalCAS()
		// Delegate attachment may reject CAS; other errors are unexpected.
		if _, err := store.CompareAndSwapStringWithTTL("binding", "a", "a", time.Hour); err != nil && !errors.Is(err, ErrConditionalUnsupported) {
			t.Errorf("unexpected CAS error during delegate attachment: %v", err)
		}
	}
	<-done
	store.SetDelegate(testSyncDelegate{})
	if ok, e := store.CompareAndSwapStringWithTTL("binding", "a", "b", time.Hour); ok || !errors.Is(e, ErrConditionalUnsupported) {
		t.Fatalf("CAS accepted delegated store: %v %v", ok, e)
	}
}
