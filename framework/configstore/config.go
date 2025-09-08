package configstore

import (
	"encoding/json"
	"fmt"
)

// ConfigStoreType represents the type of config store.
type ConfigStoreType string

// Supported config store types.
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

// SQLiteConfig represents configuration for SQLite.
type SQLiteConfig struct {
	Path string `json:"path"` // File path to the SQLite database
}

// PostgresConfig represents configuration for Postgres.
type PostgresConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	DBName   string `json:"dbName"`
	SSLMode  string `json:"sslMode"`
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
		var pgConfig PostgresConfig
		if err := json.Unmarshal(temp.Config, &pgConfig); err != nil {
			return fmt.Errorf("failed to unmarshal postgres config: %w", err)
		}
		c.Config = &pgConfig

	default:
		return fmt.Errorf("unknown config store type: %s", temp.Type)
	}

	return nil
}
