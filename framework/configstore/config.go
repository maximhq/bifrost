package configstore

import (
	"encoding/json"
	"fmt"
)

// ConfigStoreType represents the type of config store.
type ConfigStoreType string

// ConfigStoreTypeSQLite is the type of config store for SQLite.
const (
	ConfigStoreTypeSQLite   ConfigStoreType = "sqlite"
	ConfigStoreTypePostgres ConfigStoreType = "postgres"
)

// Config represents the configuration for the config store.
type Config struct {
	Enabled bool            `json:"enabled"`
	Type    ConfigStoreType `json:"type"`
	Config  any             `json:"config"`
}

// UnmarshalJSON unmarshals the config from JSON.
func (c *Config) UnmarshalJSON(data []byte) error {
	// First, unmarshal into a temporary struct to get the basic fields
	type TempConfig struct {
		Enabled bool            `json:"enabled"`
		Type    ConfigStoreType `json:"type"`
		Config  json.RawMessage `json:"config"` // Keep as raw JSON
	}

	var temp TempConfig
	if err := json.Unmarshal(data, &temp); err != nil {
		return fmt.Errorf("failed to unmarshal config store config: %w", err)
	}

	// Set basic fields
	c.Enabled = temp.Enabled
	c.Type = temp.Type

	if !temp.Enabled {
		c.Config = nil
		return nil
	}

	// Parse the config field based on type
	switch temp.Type {
	case ConfigStoreTypeSQLite:
		var sqliteConfig SQLiteConfig
		if err := json.Unmarshal(temp.Config, &sqliteConfig); err != nil {
			return fmt.Errorf("failed to unmarshal sqlite config: %w", err)
		}
		c.Config = &sqliteConfig
	case ConfigStoreTypePostgres:
		var postgresConfig PostgresConfig
		var err error
		if err = json.Unmarshal(temp.Config, &postgresConfig); err != nil {
			return fmt.Errorf("failed to unmarshal postgres config: %w", err)
		}
		c.Config = &postgresConfig
	default:
		return fmt.Errorf("unknown config store type: %s", temp.Type)
	}

	return nil
}
