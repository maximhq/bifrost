package objectstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

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
	bucket := cfg.Bucket.GetValue()
	if bucket == "" {
		return nil, fmt.Errorf("objectstore: gcs bucket is required")
	}

	var opts []option.ClientOption

	if cfg.CredentialsJSON != nil && cfg.CredentialsJSON.GetValue() != "" {
		creds := cfg.CredentialsJSON.GetValue()
		if json.Valid([]byte(creds)) {
			opts = append(opts, option.WithCredentialsJSON([]byte(creds)))
		} else {
			opts = append(opts, option.WithCredentialsFile(creds))
		}
	}

	if cfg.ProjectID != nil && cfg.ProjectID.GetValue() != "" {
		opts = append(opts, option.WithQuotaProject(cfg.ProjectID.GetValue()))
	}

	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("objectstore: failed to create GCS client: %w", err)
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
		return fmt.Errorf("objectstore: GCS write %s: %w", key, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("objectstore: GCS close writer %s: %w", key, err)
	}
	return nil
}

// Get retrieves and decompresses an object by key.
func (g *GCSObjectStore) Get(ctx context.Context, key string) ([]byte, error) {
	r, err := g.client.Bucket(g.bucket).Object(key).NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("objectstore: GCS read %s: %w", key, err)
	}
	defer r.Close()

	// GCS transparently decompresses objects stored with ContentEncoding: "gzip",
	// so the bytes returned by ReadAll are already decompressed.
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("objectstore: GCS read body %s: %w", key, err)
	}

	return body, nil
}

// Delete removes a single object by key.
func (g *GCSObjectStore) Delete(ctx context.Context, key string) error {
	if err := g.client.Bucket(g.bucket).Object(key).Delete(ctx); err != nil {
		return fmt.Errorf("objectstore: GCS delete %s: %w", key, err)
	}
	return nil
}

// DeleteBatch removes multiple objects.
func (g *GCSObjectStore) DeleteBatch(ctx context.Context, keys []string) error {
	var errs []error
	for _, key := range keys {
		if err := g.client.Bucket(g.bucket).Object(key).Delete(ctx); err != nil {
			g.logger.Warn("objectstore: GCS delete %s: %v", key, err)
			errs = append(errs, fmt.Errorf("objectstore: GCS delete %s: %w", key, err))
		}
	}
	return errors.Join(errs...)
}

// Ping checks connectivity by querying bucket attributes.
func (g *GCSObjectStore) Ping(ctx context.Context) error {
	_, err := g.client.Bucket(g.bucket).Attrs(ctx)
	if err != nil {
		return fmt.Errorf("objectstore: GCS ping bucket %s: %w", g.bucket, err)
	}
	return nil
}

// Close releases the GCS client resources.
func (g *GCSObjectStore) Close() error {
	return g.client.Close()
}
