// Package objectstore provides object storage using S3-compatible backends,
// Google Cloud Storage, and Azure Blob Storage.
package objectstore

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is wrapped by Get when the key definitely has no object, so
// callers can tell "nothing stored" from a read that failed for another reason
// (and so must not assume the object is absent).
var ErrNotFound = errors.New("objectstore: object not found")

// ObjectInfo describes a stored object returned by listing operations.
type ObjectInfo struct {
	Key          string
	LastModified time.Time
}

// ObjectStore abstracts object storage operations.
type ObjectStore interface {
	// Put uploads data to the given key with optional tags.
	// The implementation handles compression (e.g., gzip) internally.
	Put(ctx context.Context, key string, data []byte, tags map[string]string) error

	// Get retrieves and decompresses data for the given key.
	Get(ctx context.Context, key string) ([]byte, error)

	// ListByPrefix returns objects matching the given prefix with metadata.
	ListByPrefix(ctx context.Context, prefix string) ([]ObjectInfo, error)

	// Delete removes an object by key.
	Delete(ctx context.Context, key string) error

	// DeleteBatch removes multiple objects by key.
	DeleteBatch(ctx context.Context, keys []string) error

	// Ping checks connectivity to the storage backend.
	Ping(ctx context.Context) error

	// Close releases resources held by the store.
	Close() error
}

// PutOptions overrides how a single object is written.
type PutOptions struct {
	ContentType     string // Defaults to application/json.
	SkipCompression bool   // Store the body as is, with no gzip Content-Encoding.
}

// OptionsPutter is implemented by stores that accept per-object write options.
// It is separate from ObjectStore so existing implementations keep compiling.
type OptionsPutter interface {
	PutWithOptions(ctx context.Context, key string, data []byte, tags map[string]string, opts PutOptions) error
}

// PutWith writes through PutWithOptions when the store supports it and falls
// back to Put otherwise. Callers that need the options honoured (for example a
// parquet body that must not be gzipped again) should only pass stores that
// implement OptionsPutter; NewObjectStore and the in-memory store all do.
func PutWith(ctx context.Context, store ObjectStore, key string, data []byte, tags map[string]string, opts PutOptions) error {
	if p, ok := store.(OptionsPutter); ok {
		return p.PutWithOptions(ctx, key, data, tags, opts)
	}
	return store.Put(ctx, key, data, tags)
}
