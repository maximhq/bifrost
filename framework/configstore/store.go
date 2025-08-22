// Package configstore provides a persistent configuration store for Bifrost.
package configstore

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"gorm.io/gorm"
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

	// Config CRUD
	GetConfig(key string) (*TableConfig, error)
	UpdateConfig(config *TableConfig, tx ...*gorm.DB) error

	// Plugins CRUD
	GetPlugins() ([]TablePlugin, error)
	GetPlugin(name string) (*TablePlugin, error)
	CreatePlugin(plugin *TablePlugin, tx ...*gorm.DB) error
	UpdatePlugin(plugin *TablePlugin, tx ...*gorm.DB) error
	DeletePlugin(name string) error

	// Governance config CRUD
	GetVirtualKeys() ([]TableVirtualKey, error)
	GetVirtualKey(id string) (*TableVirtualKey, error)
	CreateVirtualKey(virtualKey *TableVirtualKey, tx ...*gorm.DB) error
	UpdateVirtualKey(virtualKey *TableVirtualKey, tx ...*gorm.DB) error
	DeleteVirtualKey(id string) error

	// Team CRUD
	GetTeams(customerID string) ([]TableTeam, error)
	GetTeam(id string) (*TableTeam, error)
	CreateTeam(team *TableTeam, tx ...*gorm.DB) error
	UpdateTeam(team *TableTeam, tx ...*gorm.DB) error
	DeleteTeam(id string) error

	// Customer CRUD
	GetCustomers() ([]TableCustomer, error)
	GetCustomer(id string) (*TableCustomer, error)
	CreateCustomer(customer *TableCustomer, tx ...*gorm.DB) error
	UpdateCustomer(customer *TableCustomer, tx ...*gorm.DB) error
	DeleteCustomer(id string) error

	// Rate limit CRUD
	GetRateLimit(id string) (*TableRateLimit, error)
	CreateRateLimit(rateLimit *TableRateLimit, tx ...*gorm.DB) error
	UpdateRateLimit(rateLimit *TableRateLimit, tx ...*gorm.DB) error
	UpdateRateLimits(rateLimits []*TableRateLimit, tx ...*gorm.DB) error

	// Budget CRUD
	GetBudgets() ([]TableBudget, error)
	GetBudget(id string, tx ...*gorm.DB) (*TableBudget, error)
	CreateBudget(budget *TableBudget, tx ...*gorm.DB) error
	UpdateBudget(budget *TableBudget, tx ...*gorm.DB) error
	UpdateBudgets(budgets []*TableBudget, tx ...*gorm.DB) error

	// Model pricing CRUD
	GetModelPrices() ([]TableModelPricing, error)
	CreateModelPrices(pricing *TableModelPricing, tx ...*gorm.DB) error
	DeleteModelPrices(tx ...*gorm.DB) error

	// Key management
	GetKeysByIDs(ids []string) ([]TableKey, error)

	// Generic transaction manager
	ExecuteTransaction(fn func(tx *gorm.DB) error) error
}

// NewConfigStore creates a new config store based on the configuration
func NewConfigStore(config *Config, logger schemas.Logger) (ConfigStore, error) {
	if !config.Enabled {
		return nil, nil
	}
	switch config.Type {
	case ConfigStoreTypeSQLite:
		if sqliteConfig, ok := config.Config.(*SQLiteConfig); ok {
			return newSqliteConfigStore(sqliteConfig, logger)
		}
		return nil, fmt.Errorf("invalid sqlite config: %T", config.Config)
	}
	return nil, fmt.Errorf("unsupported config store type: %s", config.Type)
}
