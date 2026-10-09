package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/maximhq/bifrost/core/schemas"
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

func TestR2DeleteBatchObjectErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
		want     string
	}{
		{
			name:     "failure details",
			response: `<Error><Key>logs/failed.json</Key><Code>AccessDenied</Code><Message>Access denied</Message></Error><Error><Key>logs/other.json</Key><Code>InternalError</Code></Error>`,
			want:     "objectstore: r2 2 objects failed to delete in batch starting at index 1000 (first: key=logs/failed.json code=AccessDenied message=Access denied)",
		},
		{
			name:     "missing optional details",
			response: `<Error><Key>logs/failed.json</Key></Error>`,
			want:     "objectstore: r2 1 objects failed to delete in batch starting at index 1000 (first: key=logs/failed.json code= message=)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls++
				if req.Method != http.MethodPost || !req.URL.Query().Has("delete") {
					t.Errorf("unexpected request: %s %s", req.Method, req.URL)
				}
				w.Header().Set("Content-Type", "application/xml")
				failures := ""
				if calls == 2 {
					failures = tc.response
				}
				fmt.Fprintf(w, `<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">%s</DeleteResult>`, failures)
			}))
			defer server.Close()
			store := &R2ObjectStore{
				client: s3.New(s3.Options{
					Region:                     "auto",
					BaseEndpoint:               aws.String(server.URL),
					Credentials:                aws.AnonymousCredentials{},
					UsePathStyle:               true,
					RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
				}),
				bucket: "test-bucket",
			}
			keys := make([]string, 1002)
			for i := range keys {
				keys[i] = fmt.Sprintf("logs/%d.json", i)
			}
			keys[1000], keys[1001] = "logs/failed.json", "logs/other.json"
			err := store.DeleteBatch(context.Background(), keys)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("DeleteBatch error: got %v, want %s", err, tc.want)
			}
			if calls != 2 {
				t.Fatalf("got %d requests, want 2", calls)
			}
		})
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

	// ListByPrefix
	_ = store.Put(ctx, "prefix/one", []byte("1"), nil)
	_ = store.Put(ctx, "prefix/two", []byte("2"), nil)
	_ = store.Put(ctx, "other/three", []byte("3"), nil)
	objects, err := store.ListByPrefix(ctx, "prefix/")
	if err != nil {
		t.Fatalf("ListByPrefix: %v", err)
	}
	if len(objects) != 2 {
		t.Fatalf("ListByPrefix got %d objects, want 2: %v", len(objects), objects)
	}
	gotKeys := []string{objects[0].Key, objects[1].Key}
	sort.Strings(gotKeys)
	wantKeys := []string{"prefix/one", "prefix/two"}
	if gotKeys[0] != wantKeys[0] || gotKeys[1] != wantKeys[1] {
		t.Fatalf("ListByPrefix keys: got %v, want %v", gotKeys, wantKeys)
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

	if err := store.Put(ctx, "key", []byte("data"), nil); err != nil {
		t.Fatalf("Put: %v", err)
	}
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

// TestObjectStoreEndpointSecurity ensures R2 requires HTTPS while S3 retains
// HTTP support for local and internal S3-compatible services.
func TestObjectStoreEndpointSecurity(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/config")
	t.Setenv("BIFROST_TEST_R2_ENDPOINT", "http://account.r2.cloudflarestorage.com")
	for _, tc := range []struct {
		name     string
		backend  StoreType
		endpoint string
		valid    bool
	}{
		{"r2 https", StoreTypeR2, "https://account.r2.cloudflarestorage.com", true},
		{"r2 jurisdiction https", StoreTypeR2, "https://account.eu.r2.cloudflarestorage.com", true},
		{"r2 http", StoreTypeR2, "http://account.r2.cloudflarestorage.com", false},
		{"r2 local http", StoreTypeR2, "http://localhost:9000", false},
		{"r2 http from environment", StoreTypeR2, "env.BIFROST_TEST_R2_ENDPOINT", false},
		{"s3 local http", StoreTypeS3, "http://localhost:9000", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Type:            tc.backend,
				Bucket:          *schemas.NewSecretVar("test-bucket"),
				Region:          schemas.NewSecretVar("auto"),
				Endpoint:        schemas.NewSecretVar(tc.endpoint),
				AccessKeyID:     schemas.NewSecretVar("test-key"),
				SecretAccessKey: schemas.NewSecretVar("test-secret"),
			}
			store, err := NewObjectStore(context.Background(), cfg, nil)
			if tc.valid {
				if err != nil {
					t.Fatalf("valid endpoint rejected: %v", err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "HTTPS") {
				t.Fatalf("expected HTTPS validation error, got %v", err)
			}
		})
	}
}
