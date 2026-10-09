package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/maximhq/bifrost/core/schemas"
)

// R2ObjectStore implements ObjectStore using Cloudflare R2's S3-compatible API.
// It owns its client and operations independently of the S3 backend.
type R2ObjectStore struct {
	client   *s3.Client
	bucket   string
	compress bool
	logger   schemas.Logger
}

// NewR2ObjectStore creates an R2 object store with R2-compatible request settings.
func NewR2ObjectStore(ctx context.Context, cfg *Config, logger schemas.Logger) (*R2ObjectStore, error) {
	if cfg == nil {
		return nil, fmt.Errorf("objectstore: config is nil")
	}
	if cfg.Bucket.GetValue() == "" {
		return nil, fmt.Errorf("objectstore: r2 bucket is required")
	}
	if cfg.Endpoint == nil || cfg.Endpoint.GetValue() == "" {
		return nil, fmt.Errorf("objectstore: r2 endpoint is required")
	}
	endpoint := cfg.Endpoint.GetValue()
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("objectstore: r2 endpoint must be an HTTPS URL without credentials, query, or fragment")
	}
	if cfg.AccessKeyID == nil || cfg.SecretAccessKey == nil || cfg.AccessKeyID.GetValue() == "" || cfg.SecretAccessKey.GetValue() == "" {
		return nil, fmt.Errorf("objectstore: r2 access_key_id and secret_access_key must resolve to non-empty values")
	}
	if cfg.RoleARN != nil && cfg.RoleARN.GetValue() != "" {
		return nil, fmt.Errorf("objectstore: r2 does not support role_arn")
	}
	region := "auto"
	if cfg.Region != nil && cfg.Region.GetValue() != "" {
		region = cfg.Region.GetValue()
	}
	sessionToken := ""
	if cfg.SessionToken != nil {
		sessionToken = cfg.SessionToken.GetValue()
	}
	client := s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID.GetValue(), cfg.SecretAccessKey.GetValue(), sessionToken),
		UsePathStyle: cfg.ForcePathStyle,
		// Optional checksum trailers can cause SignatureDoesNotMatch for gzip
		// uploads to R2. Required checksums (e.g. DeleteObjects) are retained.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
	})
	return &R2ObjectStore{client: client, bucket: cfg.Bucket.GetValue(), compress: cfg.Compress, logger: logger}, nil
}

// Put uploads data without tags, which R2 does not support.
// When compression is enabled, data is gzip-compressed before upload.
func (r *R2ObjectStore) Put(ctx context.Context, key string, data []byte, _ map[string]string) error {
	body := data
	if r.compress {
		compressed, err := gzipCompress(data)
		if err != nil {
			return fmt.Errorf("objectstore: r2 gzip compress: %w", err)
		}
		body = compressed
	}

	input := &s3.PutObjectInput{
		Bucket:      aws.String(r.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/json"),
	}
	if r.compress {
		input.ContentEncoding = aws.String("gzip")
	}

	_, err := r.client.PutObject(ctx, input)
	if err != nil {
		return fmt.Errorf("objectstore: r2 put object %s: %w", key, err)
	}
	return nil
}

// Get retrieves and decompresses an object by key.
func (r *R2ObjectStore) Get(ctx context.Context, key string) ([]byte, error) {
	output, err := r.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("objectstore: r2 get object %s: %w", key, err)
	}
	defer output.Body.Close()

	body, err := io.ReadAll(output.Body)
	if err != nil {
		return nil, fmt.Errorf("objectstore: r2 read body %s: %w", key, err)
	}

	// Only attempt decompression when the object was stored with gzip encoding.
	if aws.ToString(output.ContentEncoding) == "gzip" {
		decompressed, err := gzipDecompress(body)
		if err != nil {
			if r.logger != nil {
				r.logger.Warn("objectstore: r2 gzip decompress failed for %s: %v, returning raw bytes", key, err)
			}
			return body, nil
		}
		return decompressed, nil
	}

	return body, nil
}

// Delete removes a single object by key.
func (r *R2ObjectStore) Delete(ctx context.Context, key string) error {
	_, err := r.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("objectstore: r2 delete object %s: %w", key, err)
	}
	return nil
}

// DeleteBatch removes multiple objects. It uses the S3 DeleteObjects API
// which supports up to 1000 keys per call.
func (r *R2ObjectStore) DeleteBatch(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}

	const maxBatchSize = 1000
	for i := 0; i < len(keys); i += maxBatchSize {
		end := i + maxBatchSize
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[i:end]

		objects := make([]types.ObjectIdentifier, len(batch))
		for j, key := range batch {
			objects[j] = types.ObjectIdentifier{Key: aws.String(key)}
		}

		output, err := r.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(r.bucket),
			Delete: &types.Delete{
				Objects: objects,
				Quiet:   aws.Bool(true),
			},
		})
		if err != nil {
			return fmt.Errorf("objectstore: r2 delete objects batch starting at index %d: %w", i, err)
		}
		if len(output.Errors) > 0 {
			first := output.Errors[0]
			return fmt.Errorf("objectstore: r2 %d objects failed to delete in batch starting at index %d (first: key=%s code=%s message=%s)",
				len(output.Errors), i, aws.ToString(first.Key), aws.ToString(first.Code), aws.ToString(first.Message))
		}
	}
	return nil
}

// ListByPrefix returns all non-empty object keys matching the given prefix.
func (r *R2ObjectStore) ListByPrefix(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	paginator := s3.NewListObjectsV2Paginator(r.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(r.bucket),
		Prefix: aws.String(prefix),
	})

	objects := make([]ObjectInfo, 0)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("objectstore: r2 list objects with prefix %s: %w", prefix, err)
		}
		for _, object := range page.Contents {
			key := aws.ToString(object.Key)
			if key != "" {
				info := ObjectInfo{Key: key}
				if object.LastModified != nil {
					info.LastModified = *object.LastModified
				}
				objects = append(objects, info)
			}
		}
	}

	return objects, nil
}

// Ping checks connectivity by performing a HeadBucket call.
func (r *R2ObjectStore) Ping(ctx context.Context) error {
	_, err := r.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(r.bucket),
	})
	if err != nil {
		return fmt.Errorf("objectstore: r2 head bucket %s: %w", r.bucket, err)
	}
	return nil
}

// Close is a no-op; the SDK manages the HTTP connection pool.
func (r *R2ObjectStore) Close() error {
	return nil
}
