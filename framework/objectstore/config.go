package objectstore

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// StoreType identifies the object storage backend.
type StoreType string

const (
	StoreTypeS3  StoreType = "s3"
	StoreTypeGCS StoreType = "gcs"
	StoreTypeR2  StoreType = "r2"
)

// Config holds the configuration for an object store.
type Config struct {
	Type   StoreType         `json:"type"` // "s3", "gcs", or "r2"
	Bucket schemas.SecretVar `json:"bucket"`

	// Common fields (apply to all store types)
	Prefix   string `json:"prefix,omitempty"`   // Key prefix for all stored objects. Default: "bifrost".
	Compress bool   `json:"compress,omitempty"` // Enables gzip compression for stored objects. Default: false.

	// S3 API fields (used by S3 and R2; RoleARN is S3-only).
	Region          *schemas.SecretVar `json:"region,omitempty"`
	Endpoint        *schemas.SecretVar `json:"endpoint,omitempty"`
	AccessKeyID     *schemas.SecretVar `json:"access_key_id,omitempty"`
	SecretAccessKey *schemas.SecretVar `json:"secret_access_key,omitempty"`
	SessionToken    *schemas.SecretVar `json:"session_token,omitempty"`
	RoleARN         *schemas.SecretVar `json:"role_arn,omitempty"`
	ForcePathStyle  bool               `json:"force_path_style,omitempty"`

	// GCS fields (used when Type == "gcs")
	Credentials     *schemas.SecretVar `json:"credentials,omitempty"`      // Deprecated: use credentials_json
	CredentialsJSON *schemas.SecretVar `json:"credentials_json,omitempty"` // Service account JSON or path
	ProjectID       *schemas.SecretVar `json:"project_id,omitempty"`       // GCP project ID override
}

// GetPrefix returns the configured prefix or "bifrost" as default.
func (c *Config) GetPrefix() string {
	if c.Prefix != "" {
		return c.Prefix
	}
	return "bifrost"
}

// NewObjectStore creates the appropriate ObjectStore implementation based on config type.
func NewObjectStore(ctx context.Context, cfg *Config, logger schemas.Logger) (ObjectStore, error) {
	if cfg == nil {
		return nil, fmt.Errorf("objectstore: config is required")
	}

	switch cfg.Type {
	case StoreTypeS3:
		return NewS3ObjectStore(ctx, cfg, logger)
	case StoreTypeGCS:
		return NewGCSObjectStore(ctx, cfg, logger)
	case StoreTypeR2:
		return NewR2ObjectStore(ctx, cfg, logger)
	default:
		return nil, fmt.Errorf("objectstore: unsupported type %q", cfg.Type)
	}
}
