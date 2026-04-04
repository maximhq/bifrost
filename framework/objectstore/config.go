package objectstore

import "github.com/maximhq/bifrost/core/schemas"

// Config holds the configuration for an S3-compatible object store.
type Config struct {
	Bucket          schemas.EnvVar  `json:"bucket"`
	Region          schemas.EnvVar  `json:"region"`
	Endpoint        *schemas.EnvVar `json:"endpoint,omitempty"`
	AccessKeyID     *schemas.EnvVar `json:"access_key_id,omitempty"`
	SecretAccessKey *schemas.EnvVar `json:"secret_access_key,omitempty"`
	SessionToken    *schemas.EnvVar `json:"session_token,omitempty"`
	Prefix          string          `json:"prefix,omitempty"`
	ForcePathStyle  bool            `json:"force_path_style,omitempty"`
}

// GetPrefix returns the configured prefix or "bifrost" as default.
func (c *Config) GetPrefix() string {
	if c.Prefix != "" {
		return c.Prefix
	}
	return "bifrost"
}
