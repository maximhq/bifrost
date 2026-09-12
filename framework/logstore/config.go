// Package logstore provides a logs store for Bifrost.
package logstore

import (
	"encoding/json"
	"fmt"

	"github.com/maximhq/bifrost/framework/objectstore"
)

// Config represents the configuration for the logs store.
type Config struct {
	Enabled       bool                `json:"enabled"`
	Type          LogStoreType        `json:"type"`
	RetentionDays int                 `json:"retention_days"`
	Config        any                 `json:"config"`
	Writer        *WriterConfig       `json:"writer,omitempty"`
	ObjectStorage *objectstore.Config `json:"object_storage,omitempty"`
	// ObjectStorageExcludeFields lists payload field names (DB column names) that
	// should NOT be offloaded to object storage and instead remain in the database.
	ObjectStorageExcludeFields []string `json:"object_storage_exclude_fields,omitempty"`
	// ContentAddressed enables in-database content-addressed payload storage
	// (dedup + zstd, byte-exact reconstruction). Mutually exclusive with
	// ObjectStorage.
	ContentAddressed *ContentAddressedConfig `json:"content_addressed,omitempty"`
}

// ContentAddressedConfig configures the CAS payload store. All blobs live in
// the same database as the logs table (SQLite BLOB / PostgreSQL BYTEA).
type ContentAddressedConfig struct {
	Enabled bool `json:"enabled"`
	// ExcludeFields lists payload field names (DB column names) that stay in
	// the log row and never go to CAS. token_usage and cache_debug are always
	// excluded (pricing metadata must be readable without hydration).
	ExcludeFields []string `json:"exclude_fields,omitempty"`
	// MinFieldBytes is the payload size threshold below which a field stays in
	// the row. Zero means the default.
	MinFieldBytes int `json:"min_field_bytes,omitempty"`
	// MinChunkBytes is the minimum size of a JSON array element to become a
	// deduplicated blob; smaller elements stay inline in the manifest.
	MinChunkBytes int `json:"min_chunk_bytes,omitempty"`
}

func (c *ContentAddressedConfig) minFieldBytes() int {
	if c == nil || c.MinFieldBytes <= 0 {
		return casDefaultMinFieldBytes
	}
	return c.MinFieldBytes
}

func (c *ContentAddressedConfig) minChunkBytes() int {
	if c == nil || c.MinChunkBytes <= 0 {
		return casDefaultMinChunkBytes
	}
	return c.MinChunkBytes
}

const (
	DefaultWriterMaxBatchSize             = 1000
	DefaultWriterBatchInterval            = "5s"
	DefaultWriterMaxBatchBytes            = 300 * 1024 * 1024
	DefaultWriterQueueCapacity            = 10000
	DefaultWriterDeferredUsageConcurrency = 5
)

// WriterConfig controls the async logging plugin writer queue and batch flush behavior.
type WriterConfig struct {
	MaxBatchSize             int    `json:"max_batch_size,omitempty"`
	BatchInterval            string `json:"batch_interval,omitempty"`
	MaxBatchBytes            int    `json:"max_batch_bytes,omitempty"`
	WriteQueueCapacity       int    `json:"write_queue_capacity,omitempty"`
	DeferredUsageConcurrency int    `json:"deferred_usage_concurrency,omitempty"`
}

// WithDefaults returns a copy of WriterConfig with zero-value fields filled.
func (c *WriterConfig) WithDefaults() WriterConfig {
	out := WriterConfig{}
	if c != nil {
		out = *c
	}
	if out.MaxBatchSize == 0 {
		out.MaxBatchSize = DefaultWriterMaxBatchSize
	}
	if out.BatchInterval == "" {
		out.BatchInterval = DefaultWriterBatchInterval
	}
	if out.MaxBatchBytes == 0 {
		out.MaxBatchBytes = DefaultWriterMaxBatchBytes
	}
	if out.WriteQueueCapacity == 0 {
		out.WriteQueueCapacity = DefaultWriterQueueCapacity
	}
	if out.DeferredUsageConcurrency == 0 {
		out.DeferredUsageConcurrency = DefaultWriterDeferredUsageConcurrency
	}
	return out
}

// UnmarshalJSON is the custom unmarshal logic for Config
func (c *Config) UnmarshalJSON(data []byte) error {
	// First, unmarshal into a temporary struct to get the basic fields
	type TempConfig struct {
		Enabled                    bool                `json:"enabled"`
		Type                       LogStoreType        `json:"type"`
		Config                     json.RawMessage     `json:"config"` // Keep as raw JSON
		RetentionDays              int                 `json:"retention_days"`
		Writer                     *WriterConfig       `json:"writer,omitempty"`
		ObjectStorage              *objectstore.Config       `json:"object_storage,omitempty"`
		ObjectStorageExcludeFields []string                  `json:"object_storage_exclude_fields,omitempty"`
		ContentAddressed           *ContentAddressedConfig   `json:"content_addressed,omitempty"`
	}

	var temp TempConfig
	if err := json.Unmarshal(data, &temp); err != nil {
		return fmt.Errorf("failed to unmarshal logs config: %w", err)
	}

	// Set basic fields
	c.Enabled = temp.Enabled
	c.Type = temp.Type
	c.RetentionDays = temp.RetentionDays
	c.Writer = temp.Writer
	c.ObjectStorage = temp.ObjectStorage
	c.ObjectStorageExcludeFields = temp.ObjectStorageExcludeFields
	c.ContentAddressed = temp.ContentAddressed
	if !temp.Enabled {
		c.Config = nil
		return nil
	}

	// Parse the config field based on type
	switch temp.Type {
	case LogStoreTypeSQLite:
		if len(temp.Config) == 0 {
			return fmt.Errorf("missing sqlite config payload")
		}
		var sqliteConfig SQLiteConfig
		if err := json.Unmarshal(temp.Config, &sqliteConfig); err != nil {
			return fmt.Errorf("failed to unmarshal sqlite config: %w", err)
		}
		c.Config = &sqliteConfig
	case LogStoreTypePostgres:
		var postgresConfig PostgresConfig
		var err error
		if err = json.Unmarshal(temp.Config, &postgresConfig); err != nil {
			return fmt.Errorf("failed to unmarshal postgres config: %w", err)
		}
		c.Config = &postgresConfig
	case LogStoreTypeClickHouse:
		var clickhouseConfig ClickHouseConfig
		if err := json.Unmarshal(temp.Config, &clickhouseConfig); err != nil {
			return fmt.Errorf("failed to unmarshal clickhouse config: %w", err)
		}
		c.Config = &clickhouseConfig
	default:
		return fmt.Errorf("unknown log store type: %s", temp.Type)
	}
	return nil
}
