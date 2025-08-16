// Package logstore provides a logs store for Bifrost.
package logstore

import (
	"encoding/json"
	"fmt"
)

// LogStoreType represents the type of log store.
type LogStoreType string

const (
	LogStoreTypeSQLite LogStoreType = "sqlite"
)

// Config represents the configuration for the logs store.
type Config struct {
	Enabled bool         `json:"enabled"`
	Type    LogStoreType `json:"type"`
	Config  any          `json:"config"`
}

// UnmarshalJSON is the custom unmarshal logic for Config
func (c *Config) UnmarshalJSON(data []byte) error {
	// First, unmarshal into a temporary struct to get the basic fields
	type TempConfig struct {
		Enabled bool            `json:"enabled"`
		Type    LogStoreType    `json:"type"`
		Config  json.RawMessage `json:"config"` // Keep as raw JSON
	}

	var temp TempConfig
	if err := json.Unmarshal(data, &temp); err != nil {
		return fmt.Errorf("failed to unmarshal logs config: %w", err)
	}

	// Set basic fields
	c.Enabled = temp.Enabled
	c.Type = temp.Type

	// Parse the config field based on type
	switch temp.Type {
	case LogStoreTypeSQLite:
		var sqliteConfig SQLiteConfig
		if err := json.Unmarshal(temp.Config, &sqliteConfig); err != nil {
			return fmt.Errorf("failed to unmarshal sqlite config: %w", err)
		}
		c.Config = sqliteConfig

	default:
		return fmt.Errorf("unknown log store type: %s", temp.Type)
	}
	return nil
}
