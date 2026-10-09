package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// TestIsNotFound distinguishes missing objects from unrelated storage failures,
// including when either kind of error is wrapped by the object store or caller.
func TestIsNotFound(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sentinel", ErrNotFound, true},
		{"s3-key", &types.NoSuchKey{}, true},
		{"s3-not-found", &smithy.GenericAPIError{Code: "NotFound"}, true},
		{"gcs-object", storage.ErrObjectNotExist, true},
		{"s3-bucket", &types.NoSuchBucket{}, false},
		{"gcs-bucket", storage.ErrBucketNotExist, false},
		{"access-denied", &smithy.GenericAPIError{Code: "AccessDenied"}, false},
		{"unavailable", &smithy.GenericAPIError{Code: "ServiceUnavailable"}, false},
		{"credentials-file", &os.PathError{Op: "open", Path: "credentials.json", Err: os.ErrNotExist}, false},
		{"error-text", fmt.Errorf("objectstore: object not found"), false},
		{"cancelled", context.Canceled, false},
		{"timeout", context.DeadlineExceeded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNotFound(tc.err); got != tc.want {
				t.Fatalf("IsNotFound(%v) = %t, want %t", tc.err, got, tc.want)
			}
			if tc.err != nil {
				wrapped := fmt.Errorf("fetch payload: %w", tc.err)
				if got := IsNotFound(wrapped); got != tc.want {
					t.Fatalf("IsNotFound(%v) = %t, want %t", wrapped, got, tc.want)
				}
			}
		})
	}
}

// TestS3GetNotFound checks classification through the SDK's HTTP error decoding.
func TestS3GetNotFound(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"missing-key", http.StatusNotFound, "<Error><Code>NoSuchKey</Code></Error>", true},
		{"empty-not-found", http.StatusNotFound, "", true},
		{"missing-bucket", http.StatusNotFound, "<Error><Code>NoSuchBucket</Code></Error>", false},
		{"access-denied", http.StatusForbidden, "<Error><Code>AccessDenied</Code></Error>", false},
		{"unavailable", http.StatusServiceUnavailable, "<Error><Code>ServiceUnavailable</Code></Error>", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/logs/missing.json" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			store := &S3ObjectStore{
				bucket: "logs",
				client: s3.New(s3.Options{
					Region: "us-east-1", BaseEndpoint: aws.String(server.URL), UsePathStyle: true,
					Credentials:      credentials.NewStaticCredentialsProvider("test", "test", ""),
					RetryMaxAttempts: 1,
				}),
			}
			_, err := store.Get(context.Background(), "missing.json")
			if err == nil {
				t.Fatal("expected Get to fail")
			}
			if got := IsNotFound(err); got != tc.want {
				t.Fatalf("IsNotFound(%v) = %t, want %t", err, got, tc.want)
			}
		})
	}
}

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

// TestInMemoryObjectStore covers storage operations and missing-object errors.
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
	if !IsNotFound(err) {
		t.Fatalf("expected missing-object error, got %v", err)
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
