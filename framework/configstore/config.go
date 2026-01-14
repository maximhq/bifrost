package configstore

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/framework/envutils"
)

// ConfigStoreType represents the type of config store.
type ConfigStoreType string

// ConfigStoreTypeSQLite is the type of config store for SQLite.
const (
	ConfigStoreTypeSQLite ConfigStoreType = "sqlite"
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
		return fmt.Errorf("unknown config store type: %s", temp.Type)
	}

	return nil
}
