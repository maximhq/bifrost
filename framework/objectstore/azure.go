package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
)

// AzureObjectStore implements ObjectStore using an existing Azure Blob container.
// Client authentication is resolved once, not on each object operation.
type AzureObjectStore struct {
	client    *azblob.Client
	container string
	compress  bool
}

var (
	_ ObjectStore   = (*AzureObjectStore)(nil)
	_ OptionsPutter = (*AzureObjectStore)(nil)
)

// NewAzureObjectStore uses an explicit connection string or the default Azure
// credential chain with a Blob service endpoint. It never creates containers.
func NewAzureObjectStore(_ context.Context, cfg *Config, _ schemas.Logger) (*AzureObjectStore, error) {
	if cfg == nil {
		return nil, fmt.Errorf("objectstore: config is nil")
	}
	container := strings.TrimSpace(cfg.Bucket.GetValue())
	if container == "" {
		return nil, fmt.Errorf("objectstore: azure container (bucket) is required")
	}
	if cfg.ConnectionString != nil && cfg.Endpoint != nil {
		return nil, fmt.Errorf("objectstore: azure connection_string and endpoint are mutually exclusive")
	}

	var client *azblob.Client
	var err error
	if cfg.ConnectionString != nil {
		connectionString := strings.TrimSpace(cfg.ConnectionString.GetValue())
		if connectionString == "" {
			return nil, fmt.Errorf("objectstore: azure connection_string must resolve to a non-empty value")
		}
		client, err = azblob.NewClientFromConnectionString(connectionString, nil)
	} else {
		if cfg.Endpoint == nil || strings.TrimSpace(cfg.Endpoint.GetValue()) == "" {
			return nil, fmt.Errorf("objectstore: azure endpoint must resolve to a non-empty Blob service URL when using DefaultAzureCredential")
		}
		credential, credentialErr := azidentity.NewDefaultAzureCredential(nil)
		if credentialErr != nil {
			return nil, fmt.Errorf("objectstore: create azure default credential: %w", credentialErr)
		}
		client, err = azblob.NewClient(strings.TrimSpace(cfg.Endpoint.GetValue()), credential, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("objectstore: create azure client: %w", err)
	}
	return &AzureObjectStore{client: client, container: container, compress: cfg.gzipObjects()}, nil
}

// Put uploads a block blob, overwriting any object at the same key.
func (a *AzureObjectStore) Put(ctx context.Context, key string, data []byte, tags map[string]string) error {
	return a.PutWithOptions(ctx, key, data, tags, PutOptions{})
}

// PutWithOptions preserves per-object content types and can bypass whole-object
// gzip for Parquet, whose compression is internal to its pages.
func (a *AzureObjectStore) PutWithOptions(ctx context.Context, key string, data []byte, tags map[string]string, opts PutOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	contentType := opts.ContentType
	if contentType == "" {
		contentType = ContentTypeJSON
	}
	headers := &blob.HTTPHeaders{BlobContentType: &contentType}
	body := data
	if a.compress && !opts.SkipCompression {
		var err error
		body, err = gzipCompress(data)
		if err != nil {
			return fmt.Errorf("objectstore: gzip compress: %w", err)
		}
		encoding := "gzip"
		headers.BlobContentEncoding = &encoding
	}
	var metadata map[string]*string
	if len(tags) > 0 {
		metadata = make(map[string]*string, len(tags))
		for key, value := range tags {
			// Metadata avoids index-tag permissions and character restrictions.
			// HTTP header values must be ASCII, even for Unicode log metadata.
			encoded := url.QueryEscape(value)
			metadata[key] = &encoded
		}
	}
	_, err := a.client.UploadBuffer(ctx, a.container, key, body, &azblob.UploadBufferOptions{
		HTTPHeaders: headers,
		Metadata:    metadata,
	})
	if err != nil {
		return fmt.Errorf("objectstore: azure put %s: %w", key, err)
	}
	return nil
}

// Get decompresses only objects whose response still declares gzip encoding.
func (a *AzureObjectStore) Get(ctx context.Context, key string) ([]byte, error) {
	response, err := a.client.DownloadStream(ctx, a.container, key, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound) {
			return nil, fmt.Errorf("objectstore: azure get %s: %w", key, ErrNotFound)
		}
		return nil, fmt.Errorf("objectstore: azure get %s: %w", key, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("objectstore: azure read body %s: %w", key, err)
	}
	// Go's HTTP transport removes Content-Encoding when it transparently
	// decompresses a response, so ContentEncoding is nil in that case.
	if response.ContentEncoding != nil && strings.EqualFold(*response.ContentEncoding, "gzip") {
		body, err = gzipDecompress(body)
		if err != nil {
			return nil, fmt.Errorf("objectstore: azure gzip decompress %s: %w", key, err)
		}
	}
	return body, nil
}

// ListByPrefix follows all service continuation markers and preserves timestamps.
func (a *AzureObjectStore) ListByPrefix(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	pager := a.client.NewListBlobsFlatPager(a.container, &azblob.ListBlobsFlatOptions{Prefix: &prefix})
	objects := make([]ObjectInfo, 0)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("objectstore: azure list prefix %s: %w", prefix, err)
		}
		if page.Segment == nil {
			continue
		}
		for _, item := range page.Segment.BlobItems {
			if item == nil || item.Name == nil || *item.Name == "" {
				continue
			}
			info := ObjectInfo{Key: *item.Name}
			if item.Properties != nil && item.Properties.LastModified != nil {
				info.LastModified = *item.Properties.LastModified
			}
			objects = append(objects, info)
		}
	}
	return objects, nil
}

// Delete is idempotent for missing blobs, but not missing containers.
func (a *AzureObjectStore) Delete(ctx context.Context, key string) error {
	_, err := a.client.DeleteBlob(ctx, a.container, key, nil)
	if err != nil && !bloberror.HasCode(err, bloberror.BlobNotFound) {
		return fmt.Errorf("objectstore: azure delete %s: %w", key, err)
	}
	return nil
}

// DeleteBatch uses individual deletes, like GCS, avoiding a separate batch
// authorization path. All keys are attempted and failures are aggregated.
func (a *AzureObjectStore) DeleteBatch(ctx context.Context, keys []string) error {
	var errs []error
	for _, key := range keys {
		if err := a.Delete(ctx, key); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Ping proves upload and delete access without provisioning a container.
func (a *AzureObjectStore) Ping(ctx context.Context) error {
	key := "__bifrost_ping__/" + uuid.NewString()
	if err := a.PutWithOptions(ctx, key, []byte("ping"), nil, PutOptions{SkipCompression: true}); err != nil {
		return fmt.Errorf("objectstore: azure ping write: %w", err)
	}
	if err := a.Delete(ctx, key); err != nil {
		return fmt.Errorf("objectstore: azure ping cleanup: %w", err)
	}
	return nil
}

// Close is a no-op; the Azure SDK client has no resources requiring closure.
func (a *AzureObjectStore) Close() error {
	return nil
}
