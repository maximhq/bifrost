package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"testing"
)

func TestGzipRoundTrip(t *testing.T) {
	original := []byte(`{"input_history":"[{\"role\":\"user\",\"content\":\"hello world\"}]","output_message":"{\"role\":\"assistant\",\"content\":\"hi there\"}"}`)
	compressed, err := gzipCompress(original)
	if err != nil {
		t.Fatalf("gzipCompress: %v", err)
	}
	if len(compressed) >= len(original) {
		// For very small inputs gzip may be larger; just verify round-trip.
		t.Logf("compressed (%d) >= original (%d), but checking round-trip", len(compressed), len(original))
	}
	decompressed, err := gzipDecompress(compressed)
	if err != nil {
		t.Fatalf("gzipDecompress: %v", err)
	}
	if !bytes.Equal(original, decompressed) {
		t.Fatalf("round-trip mismatch: got %q, want %q", decompressed, original)
	}
}

func TestGzipDecompress_NonGzipData(t *testing.T) {
	// gzipDecompress should return error for non-gzip data.
	_, err := gzipDecompress([]byte("not gzip"))
	if err == nil {
		t.Fatal("expected error for non-gzip data")
	}
}

func TestEncodeTags(t *testing.T) {
	tags := map[string]string{
		"provider": "anthropic",
		"model":    "claude-3",
	}
	encoded := encodeTags(tags)
	// URL-encoded tags, order may vary.
	if encoded == "" {
		t.Fatal("expected non-empty encoded tags")
	}
	// Verify both tags are present.
	if !bytes.Contains([]byte(encoded), []byte("provider=anthropic")) {
		t.Errorf("missing provider tag in %q", encoded)
	}
	if !bytes.Contains([]byte(encoded), []byte("model=claude-3")) {
		t.Errorf("missing model tag in %q", encoded)
	}
}

func TestInMemoryObjectStore(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryObjectStore()

	// Put
	if err := store.Put(ctx, "key1", []byte("data1"), map[string]string{"tag": "val"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if store.Len() != 1 {
		t.Fatalf("Len: got %d, want 1", store.Len())
	}

	// Get
	data, err := store.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(data, []byte("data1")) {
		t.Fatalf("Get: got %q, want %q", data, "data1")
	}

	// GetTags
	tags := store.GetTags("key1")
	if tags["tag"] != "val" {
		t.Fatalf("GetTags: got %v", tags)
	}

	// Get missing key
	_, err = store.Get(ctx, "missing")
	if err == nil {
		t.Fatal("expected error for missing key")
	}

	// Delete
	if err := store.Delete(ctx, "key1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if store.Len() != 0 {
		t.Fatalf("Len after delete: got %d, want 0", store.Len())
	}

	// DeleteBatch
	_ = store.Put(ctx, "a", []byte("1"), nil)
	_ = store.Put(ctx, "b", []byte("2"), nil)
	_ = store.Put(ctx, "c", []byte("3"), nil)
	if err := store.DeleteBatch(ctx, []string{"a", "c"}); err != nil {
		t.Fatalf("DeleteBatch: %v", err)
	}
	if store.Len() != 1 {
		t.Fatalf("Len after batch delete: got %d, want 1", store.Len())
	}

	// Ping and Close
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestInMemoryObjectStore_SimulateErrors(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryObjectStore()

	store.PutErr = fmt.Errorf("simulated put error")
	if err := store.Put(ctx, "key", []byte("data"), nil); err == nil {
		t.Fatal("expected error from Put")
	}
	store.PutErr = nil

	_ = store.Put(ctx, "key", []byte("data"), nil)
	store.GetErr = fmt.Errorf("simulated get error")
	if _, err := store.Get(ctx, "key"); err == nil {
		t.Fatal("expected error from Get")
	}
}

func TestConfigGetPrefix(t *testing.T) {
	c := &Config{Prefix: "custom"}
	if got := c.GetPrefix(); got != "custom" {
		t.Fatalf("GetPrefix: got %q, want %q", got, "custom")
	}
	c2 := &Config{}
	if got := c2.GetPrefix(); got != "bifrost" {
		t.Fatalf("GetPrefix default: got %q, want %q", got, "bifrost")
	}
}
