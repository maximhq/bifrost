// Package configstore provides a persistent configuration store for Bifrost.
package configstore

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/migrator"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"gorm.io/gorm"
)

// ConfigStore is the interface for the config store.
type ConfigStore interface {
	// Health check
	Ping(ctx context.Context) error

	// Client config CRUD
	UpdateClientConfig(ctx context.Context, config *ClientConfig) error
	GetClientConfig(ctx context.Context) (*ClientConfig, error)

	// Framework config CRUD
	UpdateFrameworkConfig(ctx context.Context, config *tables.TableFrameworkConfig) error
	GetFrameworkConfig(ctx context.Context) (*tables.TableFrameworkConfig, error)

	// Provider config CRUD
	UpdateProvidersConfig(ctx context.Context, providers map[schemas.ModelProvider]ProviderConfig, tx ...*gorm.DB) error
	AddProvider(ctx context.Context, provider schemas.ModelProvider, config ProviderConfig, envKeys map[string][]EnvKeyInfo, tx ...*gorm.DB) error
	UpdateProvider(ctx context.Context, provider schemas.ModelProvider, config ProviderConfig, envKeys map[string][]EnvKeyInfo, tx ...*gorm.DB) error
	DeleteProvider(ctx context.Context, provider schemas.ModelProvider, tx ...*gorm.DB) error
	GetProvidersConfig(ctx context.Context) (map[schemas.ModelProvider]ProviderConfig, error)

	// MCP config CRUD
	GetMCPConfig(ctx context.Context) (*schemas.MCPConfig, error)
	GetMCPClientByName(ctx context.Context, name string) (*tables.TableMCPClient, error)
	CreateMCPClientConfig(ctx context.Context, clientConfig schemas.MCPClientConfig, envKeys map[string][]EnvKeyInfo) error
	UpdateMCPClientConfig(ctx context.Context, id string, clientConfig schemas.MCPClientConfig, envKeys map[string][]EnvKeyInfo) error
	DeleteMCPClientConfig(ctx context.Context, id string) error

	// Vector store config CRUD
	UpdateVectorStoreConfig(ctx context.Context, config *vectorstore.Config) error
	GetVectorStoreConfig(ctx context.Context) (*vectorstore.Config, error)

	// Logs store config CRUD
	UpdateLogsStoreConfig(ctx context.Context, config *logstore.Config) error
	GetLogsStoreConfig(ctx context.Context) (*logstore.Config, error)

	// ENV keys CRUD
	UpdateEnvKeys(ctx context.Context, keys map[string][]EnvKeyInfo, tx ...*gorm.DB) error
	GetEnvKeys(ctx context.Context) (map[string][]EnvKeyInfo, error)

	// Config CRUD
	GetConfig(ctx context.Context, key string) (*tables.TableGovernanceConfig, error)
	UpdateConfig(ctx context.Context, config *tables.TableGovernanceConfig, tx ...*gorm.DB) error

	// Plugins CRUD
	GetPlugins(ctx context.Context) ([]*tables.TablePlugin, error)
	GetPlugin(ctx context.Context, name string) (*tables.TablePlugin, error)
	CreatePlugin(ctx context.Context, plugin *tables.TablePlugin, tx ...*gorm.DB) error
	UpsertPlugin(ctx context.Context, plugin *tables.TablePlugin, tx ...*gorm.DB) error
	UpdatePlugin(ctx context.Context, plugin *tables.TablePlugin, tx ...*gorm.DB) error
	DeletePlugin(ctx context.Context, name string, tx ...*gorm.DB) error

	// Governance config CRUD
	GetVirtualKeys(ctx context.Context) ([]tables.TableVirtualKey, error)
	GetRedactedVirtualKeys(ctx context.Context, ids []string) ([]tables.TableVirtualKey, error) // leave ids empty to get all
	GetVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error)
	GetVirtualKeyByValue(ctx context.Context, value string) (*tables.TableVirtualKey, error)
	CreateVirtualKey(ctx context.Context, virtualKey *tables.TableVirtualKey, tx ...*gorm.DB) error
	UpdateVirtualKey(ctx context.Context, virtualKey *tables.TableVirtualKey, tx ...*gorm.DB) error
	DeleteVirtualKey(ctx context.Context, id string) error

	// Virtual key provider config CRUD
	GetVirtualKeyProviderConfigs(ctx context.Context, virtualKeyID string) ([]tables.TableVirtualKeyProviderConfig, error)
	CreateVirtualKeyProviderConfig(ctx context.Context, virtualKeyProviderConfig *tables.TableVirtualKeyProviderConfig, tx ...*gorm.DB) error
	UpdateVirtualKeyProviderConfig(ctx context.Context, virtualKeyProviderConfig *tables.TableVirtualKeyProviderConfig, tx ...*gorm.DB) error
	DeleteVirtualKeyProviderConfig(ctx context.Context, id uint, tx ...*gorm.DB) error

	// Virtual key MCP config CRUD
	GetVirtualKeyMCPConfigs(ctx context.Context, virtualKeyID string) ([]tables.TableVirtualKeyMCPConfig, error)
	CreateVirtualKeyMCPConfig(ctx context.Context, virtualKeyMCPConfig *tables.TableVirtualKeyMCPConfig, tx ...*gorm.DB) error
	UpdateVirtualKeyMCPConfig(ctx context.Context, virtualKeyMCPConfig *tables.TableVirtualKeyMCPConfig, tx ...*gorm.DB) error
	DeleteVirtualKeyMCPConfig(ctx context.Context, id uint, tx ...*gorm.DB) error

	// Team CRUD
	GetTeams(ctx context.Context, customerID string) ([]tables.TableTeam, error)
	GetTeam(ctx context.Context, id string) (*tables.TableTeam, error)
	CreateTeam(ctx context.Context, team *tables.TableTeam, tx ...*gorm.DB) error
	UpdateTeam(ctx context.Context, team *tables.TableTeam, tx ...*gorm.DB) error
	DeleteTeam(ctx context.Context, id string) error

	// Customer CRUD
	GetCustomers(ctx context.Context) ([]tables.TableCustomer, error)
	GetCustomer(ctx context.Context, id string) (*tables.TableCustomer, error)
	CreateCustomer(ctx context.Context, customer *tables.TableCustomer, tx ...*gorm.DB) error
	UpdateCustomer(ctx context.Context, customer *tables.TableCustomer, tx ...*gorm.DB) error
	DeleteCustomer(ctx context.Context, id string) error

	// Rate limit CRUD
	GetRateLimits(ctx context.Context) ([]tables.TableRateLimit, error)
	GetRateLimit(ctx context.Context, id string) (*tables.TableRateLimit, error)
	CreateRateLimit(ctx context.Context, rateLimit *tables.TableRateLimit, tx ...*gorm.DB) error
	UpdateRateLimit(ctx context.Context, rateLimit *tables.TableRateLimit, tx ...*gorm.DB) error
	UpdateRateLimits(ctx context.Context, rateLimits []*tables.TableRateLimit, tx ...*gorm.DB) error

	// Budget CRUD
	GetBudgets(ctx context.Context) ([]tables.TableBudget, error)
	GetBudget(ctx context.Context, id string, tx ...*gorm.DB) (*tables.TableBudget, error)
	CreateBudget(ctx context.Context, budget *tables.TableBudget, tx ...*gorm.DB) error
	UpdateBudget(ctx context.Context, budget *tables.TableBudget, tx ...*gorm.DB) error
	UpdateBudgets(ctx context.Context, budgets []*tables.TableBudget, tx ...*gorm.DB) error

	// Governance config CRUD
	GetGovernanceConfig(ctx context.Context) (*GovernanceConfig, error)

	// Auth config CRUD
	GetAuthConfig(ctx context.Context) (*AuthConfig, error)
	UpdateAuthConfig(ctx context.Context, config *AuthConfig) error

	// Proxy config CRUD
	GetProxyConfig(ctx context.Context) (*tables.GlobalProxyConfig, error)
	UpdateProxyConfig(ctx context.Context, config *tables.GlobalProxyConfig) error

	// Session CRUD
	GetSession(ctx context.Context, token string) (*tables.SessionsTable, error)
	CreateSession(ctx context.Context, session *tables.SessionsTable) error
	DeleteSession(ctx context.Context, token string) error

	// Model pricing CRUD
	GetModelPrices(ctx context.Context) ([]tables.TableModelPricing, error)
	CreateModelPrices(ctx context.Context, pricing *tables.TableModelPricing, tx ...*gorm.DB) error
	DeleteModelPrices(ctx context.Context, tx ...*gorm.DB) error

	// Key management
	GetKeysByIDs(ctx context.Context, ids []string) ([]tables.TableKey, error)
	GetKeysByProvider(ctx context.Context, provider string) ([]tables.TableKey, error)
	GetAllRedactedKeys(ctx context.Context, ids []string) ([]schemas.Key, error) // leave ids empty to get all

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
