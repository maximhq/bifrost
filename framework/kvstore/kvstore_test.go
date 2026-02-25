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

func TestStoreTouch(t *testing.T) {
	store, err := New(Config{})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.SetWithTTL("k", "v", 20*time.Millisecond); err != nil {
		t.Fatalf("set with ttl failed: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	updated, err := store.Touch("k", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("touch failed: %v", err)
	}
	if !updated {
		t.Fatal("expected touch to update ttl")
	}

	time.Sleep(25 * time.Millisecond)
	if _, err := store.Get("k"); err != nil {
		t.Fatalf("expected key to still exist after touch, got: %v", err)
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
