package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/maximhq/bifrost/core/schemas"
)

// S3ObjectStore implements ObjectStore using an S3-compatible backend.
type S3ObjectStore struct {
	client   *s3.Client
	bucket   string
	compress bool
	logger   schemas.Logger
}

// NewS3ObjectStore creates a new S3-compatible object store from the given config.
func NewS3ObjectStore(ctx context.Context, cfg *Config, logger schemas.Logger) (*S3ObjectStore, error) {
	bucket := cfg.Bucket.GetValue()
	if bucket == "" {
		return nil, fmt.Errorf("objectstore: s3 bucket is required")
	}

	// Validate static credential fields: reject half-configured credentials.
	if (cfg.AccessKeyID != nil) != (cfg.SecretAccessKey != nil) {
		return nil, fmt.Errorf("objectstore: access_key_id and secret_access_key must be set together")
	}
	if cfg.AccessKeyID != nil && (cfg.AccessKeyID.GetValue() == "" || cfg.SecretAccessKey.GetValue() == "") {
		return nil, fmt.Errorf("objectstore: access_key_id and secret_access_key must resolve to non-empty values")
	}
	if cfg.SessionToken != nil && cfg.SessionToken.GetValue() != "" &&
		(cfg.AccessKeyID == nil || cfg.SecretAccessKey == nil || cfg.AccessKeyID.GetValue() == "" || cfg.SecretAccessKey.GetValue() == "") {
		return nil, fmt.Errorf("objectstore: session_token requires access_key_id and secret_access_key")
	}

	var opts []func(*awsconfig.LoadOptions) error
	if cfg.Region != nil && cfg.Region.GetValue() != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region.GetValue()))
	}

	// Static credentials if provided; otherwise default chain (IAM role, env vars, etc.)
	hasStaticConfig := cfg.AccessKeyID != nil || cfg.SecretAccessKey != nil || cfg.SessionToken != nil
	if hasStaticConfig {
		if cfg.AccessKeyID == nil || cfg.AccessKeyID.GetValue() == "" ||
			cfg.SecretAccessKey == nil || cfg.SecretAccessKey.GetValue() == "" {
			return nil, fmt.Errorf("objectstore: access_key_id and secret_access_key must both be set when using static credentials")
		}
		sessionToken := ""
		if cfg.SessionToken != nil {
			sessionToken = cfg.SessionToken.GetValue()
		}
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				cfg.AccessKeyID.GetValue(),
				cfg.SecretAccessKey.GetValue(),
				sessionToken,
			),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("objectstore: failed to load AWS config: %w", err)
	}

	// If a role ARN is configured, assume that role using STS.
	// Works on top of either static credentials or the default chain (instance role, env vars, etc.).
	if cfg.RoleARN != nil && cfg.RoleARN.GetValue() != "" {
		stsClient := sts.NewFromConfig(awsCfg)
		awsCfg.Credentials = aws.NewCredentialsCache(
			stscreds.NewAssumeRoleProvider(stsClient, cfg.RoleARN.GetValue()),
		)
	}

	s3Opts := func(o *s3.Options) {
		if cfg.Endpoint != nil && cfg.Endpoint.GetValue() != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint.GetValue())
		}
		if cfg.ForcePathStyle {
			o.UsePathStyle = true
		}
	}

	client := s3.NewFromConfig(awsCfg, s3Opts)

	return &S3ObjectStore{
		client:   client,
		bucket:   bucket,
		compress: cfg.Compress,
		logger:   logger,
	}, nil
}

// Put uploads data with optional S3 object tags. When compression is enabled,
// data is gzip-compressed before upload.
func (s *S3ObjectStore) Put(ctx context.Context, key string, data []byte, tags map[string]string) error {
	body := data
	if s.compress {
		compressed, err := gzipCompress(data)
		if err != nil {
			return fmt.Errorf("objectstore: gzip compress: %w", err)
		}
		body = compressed
	}

	input := &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/json"),
	}
	if s.compress {
		input.ContentEncoding = aws.String("gzip")
	}

	if len(tags) > 0 {
		input.Tagging = aws.String(encodeTags(tags))
	}

	_, err := s.client.PutObject(ctx, input)
	if err != nil {
		return fmt.Errorf("objectstore: PutObject %s: %w", key, err)
	}
	return nil
}

// Get retrieves and decompresses an object by key.
func (s *S3ObjectStore) Get(ctx context.Context, key string) ([]byte, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("objectstore: GetObject %s: %w", key, err)
	}
	defer output.Body.Close()

	body, err := io.ReadAll(output.Body)
	if err != nil {
		return nil, fmt.Errorf("objectstore: read body %s: %w", key, err)
	}

	// Only attempt decompression when the object was stored with gzip encoding.
	if aws.ToString(output.ContentEncoding) == "gzip" {
		decompressed, err := gzipDecompress(body)
		if err != nil {
			s.logger.Warn("objectstore: gzip decompress failed for %s: %v, returning raw bytes", key, err)
			return body, nil
		}
		return decompressed, nil
	}

	return body, nil
}

// Delete removes a single object by key.
func (s *S3ObjectStore) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("objectstore: DeleteObject %s: %w", key, err)
	}
	return nil
}

// DeleteBatch removes multiple objects. It uses the S3 DeleteObjects API
// which supports up to 1000 keys per call.
func (s *S3ObjectStore) DeleteBatch(ctx context.Context, keys []string) error {
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

		output, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &types.Delete{
				Objects: objects,
				Quiet:   aws.Bool(true),
			},
		})
		if err != nil {
			return fmt.Errorf("objectstore: DeleteObjects batch starting at index %d: %w", i, err)
		}
		if len(output.Errors) > 0 {
			return fmt.Errorf("objectstore: %d objects failed to delete in batch starting at index %d", len(output.Errors), i)
		}
	}
	return nil
}

// Ping checks connectivity by performing a HeadBucket call.
// Note: HeadBucket requires the s3:ListBucket IAM permission on the bucket resource.
func (s *S3ObjectStore) Ping(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(s.bucket),
	})
	if err != nil {
		return fmt.Errorf("objectstore: HeadBucket %s: %w", s.bucket, err)
	}
	return nil
}

// Close is a no-op for S3 (no persistent connections to release).
func (s *S3ObjectStore) Close() error {
	return nil
}

// encodeTags encodes a tag map into the S3 URL-encoded tagging format.
// Format: "key1=value1&key2=value2"
func encodeTags(tags map[string]string) string {
	parts := make([]string, 0, len(tags))
	for k, v := range tags {
		parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
	}
	return strings.Join(parts, "&")
}
