package queue

import (
	"encoding/json"
	"fmt"
	"time"
)

// QueueType names a queue implementation. Built-in types are listed below;
// any other type must be added with Register before NewQueue can build it.
type QueueType string

const (
	// QueueTypeLogStore stores messages in the logstore database (SQLite,
	// Postgres or ClickHouse).
	QueueTypeLogStore QueueType = "logstore"
	// QueueTypeMemory keeps messages in process. It is for tests and
	// single-instance deployments; messages do not survive a restart.
	QueueTypeMemory QueueType = "memory"
)

// Engine defaults applied by EngineConfig.WithDefaults.
const (
	DefaultRetentionHours      = 168
	DefaultAckedRetentionHours = 24
	DefaultJanitorInterval     = "10m"
	DefaultMaxPayloadBytes     = 1 << 20
)

// Config selects and configures a queue implementation.
type Config struct {
	Enabled bool      `json:"enabled"`
	Type    QueueType `json:"type"`
	Config  any       `json:"config,omitempty"` // *LogStoreQueueConfig, *MemoryQueueConfig, or json.RawMessage for registered types
}

// EngineConfig tunes the generic engine shared by the database-backed and
// in-memory queues.
type EngineConfig struct {
	RetentionHours      int    `json:"retention_hours,omitempty"`       // how long dead-lettered deliveries are kept
	AckedRetentionHours int    `json:"acked_retention_hours,omitempty"` // how long a message is kept once every group has finished with it
	JanitorInterval     string `json:"janitor_interval,omitempty"`      // Go duration between purge passes
	MaxPayloadBytes     int    `json:"max_payload_bytes,omitempty"`     // per-message payload limit
}

// LogStoreQueueConfig configures the logstore-backed queue.
type LogStoreQueueConfig struct {
	EngineConfig
}

// MemoryQueueConfig configures the in-memory queue.
type MemoryQueueConfig struct {
	EngineConfig
}

// WithDefaults returns a copy of c with zero-value fields filled.
func (c EngineConfig) WithDefaults() EngineConfig {
	if c.RetentionHours <= 0 {
		c.RetentionHours = DefaultRetentionHours
	}
	if c.AckedRetentionHours <= 0 {
		c.AckedRetentionHours = DefaultAckedRetentionHours
	}
	if c.JanitorInterval == "" {
		c.JanitorInterval = DefaultJanitorInterval
	}
	if c.MaxPayloadBytes <= 0 {
		c.MaxPayloadBytes = DefaultMaxPayloadBytes
	}
	return c
}

// Retention returns the configured retention as a duration.
func (c EngineConfig) Retention() time.Duration {
	return time.Duration(c.WithDefaults().RetentionHours) * time.Hour
}

// PurgePolicy returns the configured retentions for the janitor.
func (c EngineConfig) PurgePolicy() PurgePolicy {
	c = c.WithDefaults()
	return PurgePolicy{
		DeadRetention:  time.Duration(c.RetentionHours) * time.Hour,
		AckedRetention: time.Duration(c.AckedRetentionHours) * time.Hour,
	}
}

// Janitor parses JanitorInterval.
func (c EngineConfig) Janitor() (time.Duration, error) {
	d, err := time.ParseDuration(c.WithDefaults().JanitorInterval)
	if err != nil {
		return 0, fmt.Errorf("queue: invalid janitor_interval: %w", err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("queue: janitor_interval must be positive, got %s", d)
	}
	return d, nil
}

// UnmarshalJSON decodes the type-specific config block into its typed
// struct. Types this package does not know keep the raw JSON, which the
// registered factory decodes itself.
func (c *Config) UnmarshalJSON(data []byte) error {
	type TempConfig struct {
		Enabled bool            `json:"enabled"`
		Type    QueueType       `json:"type"`
		Config  json.RawMessage `json:"config"`
	}

	var temp TempConfig
	if err := json.Unmarshal(data, &temp); err != nil {
		return fmt.Errorf("failed to unmarshal queue config: %w", err)
	}

	c.Enabled = temp.Enabled
	c.Type = temp.Type

	if !temp.Enabled {
		c.Config = nil
		return nil
	}

	switch temp.Type {
	case QueueTypeLogStore:
		var cfg LogStoreQueueConfig
		if err := decodeOptional(temp.Config, &cfg); err != nil {
			return fmt.Errorf("failed to unmarshal logstore queue config: %w", err)
		}
		c.Config = &cfg
	case QueueTypeMemory:
		var cfg MemoryQueueConfig
		if err := decodeOptional(temp.Config, &cfg); err != nil {
			return fmt.Errorf("failed to unmarshal memory queue config: %w", err)
		}
		c.Config = &cfg
	case "":
		return fmt.Errorf("queue type is required when the queue is enabled")
	default:
		c.Config = temp.Config
	}
	return nil
}

// decodeOptional decodes raw into v, treating an absent or null block as
// empty so every field takes its default.
func decodeOptional(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, v)
}
