// Package configstore provides a persistent configuration store for Bifrost.
package configstore

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/migrator"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"gorm.io/gorm"
)

// ConfigStore is the interface for the config store.
type ConfigStore interface {

	// Client config CRUD
	UpdateClientConfig(ctx context.Context, config *ClientConfig) error
	GetClientConfig(ctx context.Context) (*ClientConfig, error)

	// Provider config CRUD
	UpdateProvidersConfig(ctx context.Context, providers map[schemas.ModelProvider]ProviderConfig) error
	AddProvider(ctx context.Context, provider schemas.ModelProvider, config ProviderConfig, envKeys map[string][]EnvKeyInfo) error
	UpdateProvider(ctx context.Context, provider schemas.ModelProvider, config ProviderConfig, envKeys map[string][]EnvKeyInfo) error
	DeleteProvider(ctx context.Context, provider schemas.ModelProvider) error
	GetProvidersConfig(ctx context.Context) (map[schemas.ModelProvider]ProviderConfig, error)

	// MCP config CRUD
	UpdateMCPConfig(ctx context.Context, config *schemas.MCPConfig, envKeys map[string][]EnvKeyInfo) error
	GetMCPConfig(ctx context.Context) (*schemas.MCPConfig, error)

	// Vector store config CRUD
	UpdateVectorStoreConfig(ctx context.Context, config *vectorstore.Config) error
	GetVectorStoreConfig(ctx context.Context) (*vectorstore.Config, error)

	// Logs store config CRUD
	UpdateLogsStoreConfig(ctx context.Context, config *logstore.Config) error
	GetLogsStoreConfig(ctx context.Context) (*logstore.Config, error)

	// ENV keys CRUD
	UpdateEnvKeys(ctx context.Context, keys map[string][]EnvKeyInfo) error
	GetEnvKeys(ctx context.Context) (map[string][]EnvKeyInfo, error)

	// Config CRUD
	GetConfig(ctx context.Context, key string) (*TableConfig, error)
	UpdateConfig(ctx context.Context, config *TableConfig, tx ...*gorm.DB) error

	// Plugins CRUD
	GetPlugins(ctx context.Context) ([]TablePlugin, error)
	GetPlugin(ctx context.Context, name string) (*TablePlugin, error)
	CreatePlugin(ctx context.Context, plugin *TablePlugin, tx ...*gorm.DB) error
	UpdatePlugin(ctx context.Context, plugin *TablePlugin, tx ...*gorm.DB) error
	DeletePlugin(ctx context.Context, name string, tx ...*gorm.DB) error

	// Governance config CRUD
	GetVirtualKeys(ctx context.Context) ([]TableVirtualKey, error)
	GetVirtualKey(ctx context.Context, id string) (*TableVirtualKey, error)
	GetVirtualKeyByValue(ctx context.Context, value string) (*TableVirtualKey, error)
	CreateVirtualKey(ctx context.Context, virtualKey *TableVirtualKey, tx ...*gorm.DB) error
	UpdateVirtualKey(ctx context.Context, virtualKey *TableVirtualKey, tx ...*gorm.DB) error
	DeleteVirtualKey(ctx context.Context, id string) error

	// Virtual key provider config CRUD
	GetVirtualKeyProviderConfigs(ctx context.Context, virtualKeyID string) ([]TableVirtualKeyProviderConfig, error)
	CreateVirtualKeyProviderConfig(ctx context.Context, virtualKeyProviderConfig *TableVirtualKeyProviderConfig, tx ...*gorm.DB) error
	UpdateVirtualKeyProviderConfig(ctx context.Context, virtualKeyProviderConfig *TableVirtualKeyProviderConfig, tx ...*gorm.DB) error
	DeleteVirtualKeyProviderConfig(ctx context.Context, id uint, tx ...*gorm.DB) error

	// Team CRUD
	GetTeams(ctx context.Context, customerID string) ([]TableTeam, error)
	GetTeam(ctx context.Context, id string) (*TableTeam, error)
	CreateTeam(ctx context.Context, team *TableTeam, tx ...*gorm.DB) error
	UpdateTeam(ctx context.Context, team *TableTeam, tx ...*gorm.DB) error
	DeleteTeam(ctx context.Context, id string) error

	// Customer CRUD
	GetCustomers(ctx context.Context) ([]TableCustomer, error)
	GetCustomer(ctx context.Context, id string) (*TableCustomer, error)
	CreateCustomer(ctx context.Context, customer *TableCustomer, tx ...*gorm.DB) error
	UpdateCustomer(ctx context.Context, customer *TableCustomer, tx ...*gorm.DB) error
	DeleteCustomer(ctx context.Context, id string) error

	// Rate limit CRUD
	GetRateLimit(ctx context.Context, id string) (*TableRateLimit, error)
	CreateRateLimit(ctx context.Context, rateLimit *TableRateLimit, tx ...*gorm.DB) error
	UpdateRateLimit(ctx context.Context, rateLimit *TableRateLimit, tx ...*gorm.DB) error
	UpdateRateLimits(ctx context.Context, rateLimits []*TableRateLimit, tx ...*gorm.DB) error

	// Budget CRUD
	GetBudgets(ctx context.Context) ([]TableBudget, error)
	GetBudget(ctx context.Context, id string, tx ...*gorm.DB) (*TableBudget, error)
	CreateBudget(ctx context.Context, budget *TableBudget, tx ...*gorm.DB) error
	UpdateBudget(ctx context.Context, budget *TableBudget, tx ...*gorm.DB) error
	UpdateBudgets(ctx context.Context, budgets []*TableBudget, tx ...*gorm.DB) error

	GetGovernanceConfig(ctx context.Context) (*GovernanceConfig, error)

	// Model pricing CRUD
	GetModelPrices(ctx context.Context) ([]TableModelPricing, error)
	CreateModelPrices(ctx context.Context, pricing *TableModelPricing, tx ...*gorm.DB) error
	DeleteModelPrices(ctx context.Context, tx ...*gorm.DB) error

	// Key management
	GetKeysByIDs(ctx context.Context, ids []string) ([]TableKey, error)

	// Generic transaction manager
	ExecuteTransaction(ctx context.Context, fn func(tx *gorm.DB) error) error

	// DB returns the underlying database connection.
	DB() *gorm.DB

	// Migration manager
	RunMigration(ctx context.Context, migration *migrator.Migration) error

	// Cleanup
	Close(ctx context.Context) error
}

// NewConfigStore creates a new config store based on the configuration
func NewConfigStore(ctx context.Context, config *Config, logger schemas.Logger) (ConfigStore, error) {
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}
	if !config.Enabled {
		return nil, nil
	}
	switch config.Type {
	case ConfigStoreTypeSQLite:
		if sqliteConfig, ok := config.Config.(*SQLiteConfig); ok {
			return newSqliteConfigStore(ctx, sqliteConfig, logger)
		}
		return nil, fmt.Errorf("invalid sqlite config: %T", config.Config)
	case ConfigStoreTypePostgres:
		if postgresConfig, ok := config.Config.(*PostgresConfig); ok {
			return newPostgresConfigStore(ctx, postgresConfig, logger)
		}
		return nil, fmt.Errorf("invalid postgres config: %T", config.Config)
	}
	return nil, fmt.Errorf("unsupported config store type: %s", config.Type)
}
