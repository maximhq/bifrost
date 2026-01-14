// Package logstore provides a logs store for Bifrost.
package logstore

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/framework/envutils"
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
		// Checking if any of the values start with env. If so, we need to process them.
		if postgresConfig.DBName != "" && strings.HasPrefix(postgresConfig.DBName, "env.") {
			postgresConfig.DBName, err = envutils.ProcessEnvValue(postgresConfig.DBName)
			if err != nil {
				return fmt.Errorf("failed to process env value for db name: %w", err)
			}
		}
		if postgresConfig.Password != "" && strings.HasPrefix(postgresConfig.Password, "env.") {
			postgresConfig.Password, err = envutils.ProcessEnvValue(postgresConfig.Password)
			if err != nil {
				return fmt.Errorf("failed to process env value for password: %w", err)
			}
		}
		if postgresConfig.User != "" && strings.HasPrefix(postgresConfig.User, "env.") {
			postgresConfig.User, err = envutils.ProcessEnvValue(postgresConfig.User)
			if err != nil {
				return fmt.Errorf("failed to process env value for user: %w", err)
			}
		}
		if postgresConfig.Host != "" && strings.HasPrefix(postgresConfig.Host, "env.") {
			postgresConfig.Host, err = envutils.ProcessEnvValue(postgresConfig.Host)
			if err != nil {
				return fmt.Errorf("failed to process env value for host: %w", err)
			}
		}
		if postgresConfig.Port != "" && strings.HasPrefix(postgresConfig.Port, "env.") {
			postgresConfig.Port, err = envutils.ProcessEnvValue(postgresConfig.Port)
			if err != nil {
				return fmt.Errorf("failed to process env value for port: %w", err)
			}
		}
		if postgresConfig.SSLMode != "" && strings.HasPrefix(postgresConfig.SSLMode, "env.") {
			postgresConfig.SSLMode, err = envutils.ProcessEnvValue(postgresConfig.SSLMode)
			if err != nil {
				return fmt.Errorf("failed to process env value for ssl mode: %w", err)
			}
		}
		c.Config = &postgresConfig
	default:
		return fmt.Errorf("unknown log store type: %s", temp.Type)
	}
	return nil
}
