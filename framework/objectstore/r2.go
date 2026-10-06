package objectstore

import (
	"context"
	"fmt"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/maximhq/bifrost/core/schemas"
)

// R2ObjectStore implements ObjectStore using Cloudflare R2's S3-compatible API.
// R2-specific request behavior is kept here so the generic S3 backend retains
// the AWS S3 defaults.
type R2ObjectStore struct {
	*S3ObjectStore
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
	store := &S3ObjectStore{client: client, bucket: cfg.Bucket.GetValue(), compress: cfg.Compress, logger: logger}

	return &R2ObjectStore{S3ObjectStore: store}, nil
}

// Put uploads without object tags, which R2 does not support.
func (r *R2ObjectStore) Put(ctx context.Context, key string, data []byte, tags map[string]string) error {
	return r.S3ObjectStore.Put(ctx, key, data, nil)
}
