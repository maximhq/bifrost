package configstore

import (
	"encoding/json"
	"fmt"
)

var (
	ConfigStoreTypeSqlite string = "sqlite"
)

// Config represents the configuration for the config store.
type Config struct {
	Enabled bool   `json:"enabled"`
	Type    string `json:"type"`
	Config  any    `json:"config"`
}

// UnmarshalJSON unmarshals the config from JSON.
func (c *Config) UnmarshalJSON(data []byte) error {
	// First, unmarshal into a temporary struct to get the basic fields
	type TempConfig struct {
		Enabled bool            `json:"enabled"`
		Type    string          `json:"type"`
		Config  json.RawMessage `json:"config"` // Keep as raw JSON
	}

	var temp TempConfig
	if err := json.Unmarshal(data, &temp); err != nil {
		return fmt.Errorf("failed to unmarshal config store config: %w", err)
	}

	// Set basic fields
	c.Enabled = temp.Enabled
	c.Type = temp.Type

	// Parse the config field based on type
	switch temp.Type {
	case ConfigStoreTypeSqlite:
		var sqliteConfig SQLiteConfig
		if err := json.Unmarshal(temp.Config, &sqliteConfig); err != nil {
			return fmt.Errorf("failed to unmarshal sqlite config: %w", err)
		}
		c.Config = sqliteConfig

	default:
		return fmt.Errorf("unknown config store type: %s", temp.Type)
	}

	return nil
}
