// Package logstore provides a logs store for Bifrost.
package logstore

import (
	"encoding/json"
	"fmt"
)

// Config represents the configuration for the logs store.
type Config struct {
	Enabled       bool         `json:"enabled"`
	Type          LogStoreType `json:"type"`
	RetentionDays int          `json:"retention_days"`
	Config        any          `json:"config"`
}

// UnmarshalJSON is the custom unmarshal logic for Config
func (c *Config) UnmarshalJSON(data []byte) error {
	// First, unmarshal into a temporary struct to get the basic fields
	type TempConfig struct {
		Enabled       bool            `json:"enabled"`
		Type          LogStoreType    `json:"type"`
		Config        json.RawMessage `json:"config"` // Keep as raw JSON
		RetentionDays int             `json:"retention_days"`
	}

	var temp TempConfig
	if err := json.Unmarshal(data, &temp); err != nil {
		return fmt.Errorf("failed to unmarshal logs config: %w", err)
	}

	// Set basic fields
	c.Enabled = temp.Enabled
	c.Type = temp.Type
	c.RetentionDays = temp.RetentionDays
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
	default:
		return fmt.Errorf("unknown log store type: %s", temp.Type)
	}
	return nil
}
