package objectstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/maximhq/bifrost/core/schemas"
	"google.golang.org/api/option"
)

// GCSObjectStore implements ObjectStore using Google Cloud Storage.
type GCSObjectStore struct {
	client   *storage.Client
	bucket   string
	compress bool
	logger   schemas.Logger
}

// NewGCSObjectStore creates a new GCS object store from the given config.
func NewGCSObjectStore(ctx context.Context, cfg *Config, logger schemas.Logger) (*GCSObjectStore, error) {
	if cfg == nil {
		return nil, fmt.Errorf("objectstore: config is nil")
	}
	bucket := cfg.Bucket.GetValue()
	if bucket == "" {
		return nil, fmt.Errorf("objectstore: gcs bucket is required")
	}

	var opts []option.ClientOption

	// Prefer credentials_json (used by Helm/schema) over deprecated credentials field.
	// Check both non-nil and non-empty to avoid an empty credentials_json shadowing
	// a valid deprecated credentials value.
	var creds string
	switch {
	case cfg.CredentialsJSON != nil && strings.TrimSpace(cfg.CredentialsJSON.GetValue()) != "":
		creds = strings.TrimSpace(cfg.CredentialsJSON.GetValue())
	case cfg.Credentials != nil && strings.TrimSpace(cfg.Credentials.GetValue()) != "":
		creds = strings.TrimSpace(cfg.Credentials.GetValue())
	}
	if creds != "" {
		if strings.HasPrefix(creds, "{") {
			if !json.Valid([]byte(creds)) {
				return nil, fmt.Errorf("objectstore: gcs credentials look like JSON but are not valid; check for syntax errors")
			}
			opts = append(opts, option.WithCredentialsJSON([]byte(creds)))
		} else {
			opts = append(opts, option.WithCredentialsFile(creds))
		}
	}

	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("objectstore: failed to create gcs client: %w", err)
	}

	return &GCSObjectStore{
		client:   client,
		bucket:   bucket,
		compress: cfg.Compress,
		logger:   logger,
	}, nil
}

// Put uploads data with optional custom metadata. When compression is enabled,
// data is gzip-compressed before upload.
func (g *GCSObjectStore) Put(ctx context.Context, key string, data []byte, tags map[string]string) error {
	body := data
	if g.compress {
		compressed, err := gzipCompress(data)
		if err != nil {
			return fmt.Errorf("objectstore: gzip compress: %w", err)
		}
		body = compressed
	}

	obj := g.client.Bucket(g.bucket).Object(key)
	w := obj.NewWriter(ctx)
	w.ContentType = "application/json"
	if g.compress {
		w.ContentEncoding = "gzip"
	}
	if len(tags) > 0 {
		w.Metadata = tags
	}

	if _, err := io.Copy(w, bytes.NewReader(body)); err != nil {
		_ = w.Close()
		return fmt.Errorf("objectstore: gcs write %s: %w", key, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("objectstore: gcs close writer %s: %w", key, err)
	}
	return nil
}

// Get retrieves and decompresses an object by key.
func (g *GCSObjectStore) Get(ctx context.Context, key string) ([]byte, error) {
	r, err := g.client.Bucket(g.bucket).Object(key).NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("objectstore: gcs read %s: %w", key, err)
	}
	defer r.Close()

	// GCS transparently decompresses objects stored with ContentEncoding: "gzip",
	// so the bytes returned by ReadAll are already decompressed.
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("objectstore: gcs read body %s: %w", key, err)
	}

	return body, nil
}

// Delete removes a single object by key.
func (g *GCSObjectStore) Delete(ctx context.Context, key string) error {
	if err := g.client.Bucket(g.bucket).Object(key).Delete(ctx); err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("objectstore: gcs delete %s: %w", key, err)
	}
	return nil
}

// DeleteBatch removes multiple objects.
func (g *GCSObjectStore) DeleteBatch(ctx context.Context, keys []string) error {
	var errs []error
	for _, key := range keys {
		if err := g.client.Bucket(g.bucket).Object(key).Delete(ctx); err != nil {
			if errors.Is(err, storage.ErrObjectNotExist) {
				continue
			}
			g.logger.Warn("objectstore: gcs delete %s: %v", key, err)
			errs = append(errs, fmt.Errorf("objectstore: gcs delete %s: %w", key, err))
		}
	}
	return errors.Join(errs...)
}

// Ping checks connectivity by writing and deleting a small object, proving
// that the credentials have upload access (not just read). This is important
// because HybridLogStore strips DB payloads before async upload — a read-only
// principal would pass a read-based ping but silently fail all Put calls.
func (g *GCSObjectStore) Ping(ctx context.Context) error {
	key := fmt.Sprintf("__bifrost_ping__/%d", time.Now().UnixNano())
	obj := g.client.Bucket(g.bucket).Object(key)

	if err := obj.NewWriter(ctx).Close(); err != nil {
		return fmt.Errorf("objectstore: gcs ping write %s: %w", key, err)
	}
	if err := obj.Delete(ctx); err != nil && !errors.Is(err, storage.ErrObjectNotExist) {
		return fmt.Errorf("objectstore: gcs ping cleanup %s: %w", key, err)
	}
	return nil
}

// Close releases the GCS client resources.
func (g *GCSObjectStore) Close() error {
	return g.client.Close()
}
