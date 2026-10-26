// Package logstore provides a logs store for Bifrost.
package logstore

import (
	"encoding/json"
	"fmt"
	"regexp"

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
	// ObjectStorageExcludeRequestTypes lists request types (log object_type values) whose logs stay fully in the database and are never offloaded.
	ObjectStorageExcludeRequestTypes []string             `json:"object_storage_exclude_request_types,omitempty"`
	ObjectStorageRoutes              []ObjectStorageRoute `json:"object_storage_routes,omitempty"` // extra buckets that copy matching logs
}

// ObjectStorageRoute names a bucket that receives a copy of the complete
// payload of each log its match selects, on top of the normal storage.
// Bifrost only writes these copies: it never reads or deletes them, so the
// bucket's own lifecycle policy owns their retention.
type ObjectStorageRoute struct {
	Name          string                  `json:"name"`
	Match         ObjectStorageRouteMatch `json:"match"`
	ObjectStorage *objectstore.Config     `json:"object_storage"`
}

// routeNamePattern keeps route names usable as log and metric labels.
var routeNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidateRoutes rejects unnamed, duplicate or malformed routes, and routes
// without a bucket or a known store type, so a bad route fails startup
// instead of silently mirroring nothing.
func (c *Config) ValidateRoutes() error {
	seen := make(map[string]struct{}, len(c.ObjectStorageRoutes))
	for i, route := range c.ObjectStorageRoutes {
		if !routeNamePattern.MatchString(route.Name) {
			return fmt.Errorf("object_storage_routes[%d]: name %q must match %s", i, route.Name, routeNamePattern)
		}
		if _, dup := seen[route.Name]; dup {
			return fmt.Errorf("object_storage_routes[%d]: duplicate name %q", i, route.Name)
		}
		seen[route.Name] = struct{}{}
		if err := route.Match.Validate(); err != nil {
			return fmt.Errorf("object_storage_routes %q: %w", route.Name, err)
		}
		if route.ObjectStorage == nil {
			return fmt.Errorf("object_storage_routes %q: object_storage is required", route.Name)
		}
		if route.ObjectStorage.Type != objectstore.StoreTypeS3 && route.ObjectStorage.Type != objectstore.StoreTypeGCS {
			return fmt.Errorf("object_storage_routes %q: object_storage type %q must be s3 or gcs", route.Name, route.ObjectStorage.Type)
		}
		if route.ObjectStorage.Bucket.GetValue() == "" {
			return fmt.Errorf("object_storage_routes %q: object_storage bucket is required", route.Name)
		}
		if err := route.ObjectStorage.Validate(); err != nil {
			return fmt.Errorf("object_storage_routes %q: %w", route.Name, err)
		}
	}
	return nil
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
		Enabled                          bool                 `json:"enabled"`
		Type                             LogStoreType         `json:"type"`
		Config                           json.RawMessage      `json:"config"` // Keep as raw JSON
		RetentionDays                    int                  `json:"retention_days"`
		Writer                           *WriterConfig        `json:"writer,omitempty"`
		ObjectStorage                    *objectstore.Config  `json:"object_storage,omitempty"`
		ObjectStorageExcludeFields       []string             `json:"object_storage_exclude_fields,omitempty"`
		ObjectStorageExcludeRequestTypes []string             `json:"object_storage_exclude_request_types,omitempty"`
		ObjectStorageRoutes              []ObjectStorageRoute `json:"object_storage_routes,omitempty"`
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
	c.ObjectStorageExcludeRequestTypes = temp.ObjectStorageExcludeRequestTypes
	c.ObjectStorageRoutes = temp.ObjectStorageRoutes
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
