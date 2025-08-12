// Package configstore provides a persistent configuration store for Bifrost.
package configstore

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib/logstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib/vectorstore"
)

// ConfigStore is the interface for the config store.
type ConfigStore interface {

	// Client config CRUD
	UpdateClientConfig(config *ClientConfig) error
	GetClientConfig() (*ClientConfig, error)

	// Provider config CRUD
	UpdateProvidersConfig(providers map[schemas.ModelProvider]ProviderConfig) error
	GetProvidersConfig() (map[schemas.ModelProvider]ProviderConfig, error)

	// MCP config CRUD
	UpdateMCPConfig(config *schemas.MCPConfig) error
	GetMCPConfig() (*schemas.MCPConfig, error)

	// Vector store config CRUD
	UpdateVectorStoreConfig(config *vectorstore.Config) error
	GetVectorStoreConfig() (*vectorstore.Config, error)

	// Logs store config CRUD
	UpdateLogsStoreConfig(config *logstore.Config) error
	GetLogsStoreConfig() (*logstore.Config, error)

	// ENV keys CRUD
	UpdateEnvKeys(keys map[string][]EnvKeyInfo) error
	GetEnvKeys() (map[string][]EnvKeyInfo, error)
}

// NewConfigStore creates a new config store based on the configuration
func NewConfigStore(config *Config) (ConfigStore, error) {
	switch config.Type {
	case ConfigStoreTypeSqlite:
		if sqliteConfig, ok := config.Config.(SQLiteConfig); ok {
			return newSqliteConfigStore(&sqliteConfig)
		}
		return nil, fmt.Errorf("invalid sqlite config: %T", config.Config)
	}
	return nil, fmt.Errorf("unsupported config store type: %s", config.Type)
}
