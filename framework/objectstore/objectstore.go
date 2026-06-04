// Package objectstore provides an S3-compatible object storage abstraction.
// It can be used by any part of the system that needs to store or retrieve
// objects from S3, GCS (via S3 interop), MinIO, R2, or other S3-compatible stores.
package objectstore

import "context"

// ObjectStore abstracts S3-compatible blob storage operations.
type ObjectStore interface {
	// Put uploads data to the given key with optional tags.
	// The implementation handles compression (e.g., gzip) internally.
	Put(ctx context.Context, key string, data []byte, tags map[string]string) error

	// Get retrieves and decompresses data for the given key.
	Get(ctx context.Context, key string) ([]byte, error)

	// Delete removes an object by key.
	Delete(ctx context.Context, key string) error

	// DeleteBatch removes multiple objects by key.
	DeleteBatch(ctx context.Context, keys []string) error

	// Ping checks connectivity to the storage backend.
	Ping(ctx context.Context) error

	// Close releases resources held by the store.
	Close() error
}
