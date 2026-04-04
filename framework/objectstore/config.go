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
)

// Config holds the configuration for an object store.
type Config struct {
	Type   StoreType      `json:"type"` // "s3" or "gcs"
	Bucket schemas.EnvVar `json:"bucket"`

	// Common fields (apply to all store types)
	Prefix   string `json:"prefix,omitempty"`   // Key prefix for all stored objects. Default: "bifrost".
	Compress bool   `json:"compress,omitempty"` // Enables gzip compression for stored objects. Default: false.

	// S3 fields (used when Type == "s3")
	Region          *schemas.EnvVar `json:"region,omitempty"`
	Endpoint        *schemas.EnvVar `json:"endpoint,omitempty"`
	AccessKeyID     *schemas.EnvVar `json:"access_key_id,omitempty"`
	SecretAccessKey *schemas.EnvVar `json:"secret_access_key,omitempty"`
	SessionToken    *schemas.EnvVar `json:"session_token,omitempty"`
	RoleARN         *schemas.EnvVar `json:"role_arn,omitempty"`
	ForcePathStyle  bool            `json:"force_path_style,omitempty"`

	// GCS fields (used when Type == "gcs")
	Credentials     *schemas.EnvVar `json:"credentials,omitempty"`      // Deprecated: use credentials_json
	CredentialsJSON *schemas.EnvVar `json:"credentials_json,omitempty"` // Service account JSON or path
	ProjectID       *schemas.EnvVar `json:"project_id,omitempty"`       // GCP project ID override
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
	default:
		return nil, fmt.Errorf("objectstore: unsupported type %q", cfg.Type)
	}
}
