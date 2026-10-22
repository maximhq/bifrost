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

// Object formats accepted by Config.Format.
const (
	FormatJSON    = "json"
	FormatParquet = "parquet"
)

// Compression codecs accepted by Config.Compression.
const (
	CompressionNone   = "none"
	CompressionGzip   = "gzip"
	CompressionSnappy = "snappy"
	CompressionZstd   = "zstd"
)

// Content types written for each object format.
const (
	ContentTypeJSON    = "application/json"
	ContentTypeParquet = "application/vnd.apache.parquet"
)

// Config holds the configuration for an object store.
type Config struct {
	Type   StoreType         `json:"type"` // "s3" or "gcs"
	Bucket schemas.SecretVar `json:"bucket"`

	// Common fields (apply to all store types)
	Prefix   string `json:"prefix,omitempty"`   // Key prefix for all stored objects. Default: "bifrost".
	Compress bool   `json:"compress,omitempty"` // Legacy: gzip stored objects; same as compression "gzip".

	Format      string `json:"format,omitempty"`      // "json" (default) or "parquet".
	Compression string `json:"compression,omitempty"` // "none", "gzip", "snappy" or "zstd".

	// S3 fields (used when Type == "s3")
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

// ResolvedFormat returns the configured object format, defaulting to json.
func (c *Config) ResolvedFormat() string {
	if c.Format == "" {
		return FormatJSON
	}
	return c.Format
}

// ResolvedCompression returns the effective codec. An explicit compression
// wins; otherwise the legacy compress flag means gzip; otherwise json objects
// are stored uncompressed and parquet objects use snappy pages. For json the
// codec is applied to the whole object (Content-Encoding); for parquet it is
// the page codec inside the file.
func (c *Config) ResolvedCompression() string {
	if c.Compression != "" {
		return c.Compression
	}
	if c.Compress {
		return CompressionGzip
	}
	if c.ResolvedFormat() == FormatParquet {
		return CompressionSnappy
	}
	return CompressionNone
}

// gzipObjects reports whether plain Put calls gzip the object body. Parquet
// payloads opt out per call, so for a parquet store this only follows the
// legacy compress flag, which still applies to non-payload objects.
func (c *Config) gzipObjects() bool {
	if c.ResolvedFormat() == FormatJSON {
		return c.ResolvedCompression() == CompressionGzip
	}
	return c.Compress
}

// Validate rejects unknown formats and codecs, and combinations the backend
// cannot serve.
func (c *Config) Validate() error {
	format := c.ResolvedFormat()
	compression := c.ResolvedCompression()
	switch format {
	case FormatJSON:
		if compression != CompressionNone && compression != CompressionGzip {
			return fmt.Errorf("objectstore: compression %q is not supported for json objects (use \"none\" or \"gzip\")", compression)
		}
	case FormatParquet:
		switch compression {
		case CompressionNone, CompressionGzip, CompressionSnappy, CompressionZstd:
		default:
			return fmt.Errorf("objectstore: unsupported compression %q", compression)
		}
		if c.Type == StoreTypeGCS {
			return fmt.Errorf("objectstore: parquet format is not supported for gcs yet")
		}
	default:
		return fmt.Errorf("objectstore: unsupported format %q", format)
	}
	return nil
}

// NewObjectStore creates the appropriate ObjectStore implementation based on config type.
func NewObjectStore(ctx context.Context, cfg *Config, logger schemas.Logger) (ObjectStore, error) {
	if cfg == nil {
		return nil, fmt.Errorf("objectstore: config is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
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
