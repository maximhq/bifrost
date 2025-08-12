// Package configstore provides a persistent configuration store for Bifrost.
package configstore

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib/logstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib/vectorstore"
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

	GetConfig(key string) (*TableConfig, error)
	UpdateConfig(tx *gorm.DB, config *TableConfig) error

	// Governance config CRUD
	GetVirtualKeys() ([]TableVirtualKey, error)
	GetVirtualKey(id string) (*TableVirtualKey, error)
	CreateVirtualKey(tx *gorm.DB, virtualKey *TableVirtualKey) error
	UpdateVirtualKey(tx *gorm.DB, virtualKey *TableVirtualKey) error
	DeleteVirtualKey(id string) error

	GetTeams(customerID string) ([]TableTeam, error)
	GetTeam(id string) (*TableTeam, error)
	CreateTeam(tx *gorm.DB, team *TableTeam) error
	UpdateTeam(tx *gorm.DB, team *TableTeam) error
	DeleteTeam(id string) error

	GetCustomers() ([]TableCustomer, error)
	GetCustomer(id string) (*TableCustomer, error)
	CreateCustomer(tx *gorm.DB, customer *TableCustomer) error
	UpdateCustomer(tx *gorm.DB, customer *TableCustomer) error
	DeleteCustomer(id string) error

	GetRateLimit(id string) (*TableRateLimit, error)
	CreateRateLimit(tx *gorm.DB, rateLimit *TableRateLimit) error
	UpdateRateLimit(tx *gorm.DB, rateLimit *TableRateLimit) error
	UpdateRateLimits(tx *gorm.DB, rateLimits []*TableRateLimit) error

	GetBudgets() ([]TableBudget, error)
	GetBudget(tx *gorm.DB, id string) (*TableBudget, error)
	CreateBudget(tx *gorm.DB, budget *TableBudget) error
	UpdateBudget(tx *gorm.DB, budget *TableBudget) error
	UpdateBudgets(tx *gorm.DB, budgets []*TableBudget) error

	GetModelPricings() ([]TableModelPricing, error)
	CreateModelPricing(tx *gorm.DB, pricing *TableModelPricing) error
	DeleteModelPricings(tx *gorm.DB) error

	// Key management
	GetKeysByIDs(ids []string) ([]TableKey, error)

	ExecuteTransaction(fn func(tx *gorm.DB) error) error
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
