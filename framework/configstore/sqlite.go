package configstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

// SQLiteConfig represents the configuration for a SQLite database.
type SQLiteConfig struct {
	Path string `json:"path"`
}

// SQLiteConfigStore represents a configuration store that uses a SQLite database.
type SQLiteConfigStore struct {
	db     *gorm.DB
	logger schemas.Logger
}

// UpdateClientConfig updates the client configuration in the database.
func (s *SQLiteConfigStore) UpdateClientConfig(config *ClientConfig) error {
	dbConfig := TableClientConfig{
		DropExcessRequests:      config.DropExcessRequests,
		InitialPoolSize:         config.InitialPoolSize,
		EnableLogging:           config.EnableLogging,
		EnableGovernance:        config.EnableGovernance,
		EnforceGovernanceHeader: config.EnforceGovernanceHeader,
		AllowDirectKeys:         config.AllowDirectKeys,
		PrometheusLabels:        config.PrometheusLabels,
		AllowedOrigins:          config.AllowedOrigins,
	}
	// Delete existing client config and create new one in a transaction
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableClientConfig{}).Error; err != nil {
			return err
		}
		return tx.Create(&dbConfig).Error
	})
}

// GetClientConfig retrieves the client configuration from the database.
func (s *SQLiteConfigStore) GetClientConfig() (*ClientConfig, error) {
	var dbConfig TableClientConfig
	if err := s.db.First(&dbConfig).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &ClientConfig{
		DropExcessRequests:      dbConfig.DropExcessRequests,
		InitialPoolSize:         dbConfig.InitialPoolSize,
		PrometheusLabels:        dbConfig.PrometheusLabels,
		EnableLogging:           dbConfig.EnableLogging,
		EnableGovernance:        dbConfig.EnableGovernance,
		EnforceGovernanceHeader: dbConfig.EnforceGovernanceHeader,
		AllowDirectKeys:         dbConfig.AllowDirectKeys,
		AllowedOrigins:          dbConfig.AllowedOrigins,
	}, nil
}

// UpdateProvidersConfig updates the client configuration in the database.
func (s *SQLiteConfigStore) UpdateProvidersConfig(providers map[schemas.ModelProvider]ProviderConfig) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Delete all existing providers (cascades to keys)
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableProvider{}).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		for providerName, providerConfig := range providers {
			dbProvider := TableProvider{
				Name:                     string(providerName),
				NetworkConfig:            providerConfig.NetworkConfig,
				ConcurrencyAndBufferSize: providerConfig.ConcurrencyAndBufferSize,
				ProxyConfig:              providerConfig.ProxyConfig,
				SendBackRawResponse:      providerConfig.SendBackRawResponse,
			}

			// Create provider first
			if err := tx.Create(&dbProvider).Error; err != nil {
				return err
			}

			// Create keys for this provider
			dbKeys := make([]TableKey, 0, len(providerConfig.Keys))
			for _, key := range providerConfig.Keys {
				dbKey := TableKey{
					Provider:         dbProvider.Name,
					ProviderID:       dbProvider.ID,
					KeyID:            key.ID,
					Value:            key.Value,
					Models:           key.Models,
					Weight:           key.Weight,
					AzureKeyConfig:   key.AzureKeyConfig,
					VertexKeyConfig:  key.VertexKeyConfig,
					BedrockKeyConfig: key.BedrockKeyConfig,
				}

				// Handle Azure config
				if key.AzureKeyConfig != nil {
					dbKey.AzureEndpoint = &key.AzureKeyConfig.Endpoint
					dbKey.AzureAPIVersion = key.AzureKeyConfig.APIVersion
				}

				// Handle Vertex config
				if key.VertexKeyConfig != nil {
					dbKey.VertexProjectID = &key.VertexKeyConfig.ProjectID
					dbKey.VertexRegion = &key.VertexKeyConfig.Region
					dbKey.VertexAuthCredentials = &key.VertexKeyConfig.AuthCredentials
				}

				// Handle Bedrock config
				if key.BedrockKeyConfig != nil {
					dbKey.BedrockAccessKey = &key.BedrockKeyConfig.AccessKey
					dbKey.BedrockSecretKey = &key.BedrockKeyConfig.SecretKey
					dbKey.BedrockSessionToken = key.BedrockKeyConfig.SessionToken
					dbKey.BedrockRegion = key.BedrockKeyConfig.Region
					dbKey.BedrockARN = key.BedrockKeyConfig.ARN
				}

				dbKeys = append(dbKeys, dbKey)
			}

			// Upsert keys to handle duplicates properly
			for _, dbKey := range dbKeys {
				// First try to find existing key by KeyID
				var existingKey TableKey
				result := tx.Where("key_id = ?", dbKey.KeyID).First(&existingKey)

				if result.Error == nil {
					// Update existing key with new data
					dbKey.ID = existingKey.ID // Keep the same database ID
					if err := tx.Save(&dbKey).Error; err != nil {
						return err
					}
				} else if errors.Is(result.Error, gorm.ErrRecordNotFound) {
					// Create new key
					if err := tx.Create(&dbKey).Error; err != nil {
						return err
					}
				} else {
					// Other error occurred
					return result.Error
				}
			}
		}
		return nil
	})
}

// GetProvidersConfig retrieves the provider configuration from the database.
func (s *SQLiteConfigStore) GetProvidersConfig() (map[schemas.ModelProvider]ProviderConfig, error) {
	var dbProviders []TableProvider
	if err := s.db.Preload("Keys").Find(&dbProviders).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if len(dbProviders) == 0 {
		// No providers in database, auto-detect from environment
		return nil, nil
	}
	processedProviders := make(map[schemas.ModelProvider]ProviderConfig)
	for _, dbProvider := range dbProviders {
		provider := schemas.ModelProvider(dbProvider.Name)
		// Convert database keys to schemas.Key
		keys := make([]schemas.Key, len(dbProvider.Keys))
		for i, dbKey := range dbProvider.Keys {
			keys[i] = schemas.Key{
				ID:               dbKey.KeyID,
				Value:            dbKey.Value,
				Models:           dbKey.Models,
				Weight:           dbKey.Weight,
				AzureKeyConfig:   dbKey.AzureKeyConfig,
				VertexKeyConfig:  dbKey.VertexKeyConfig,
				BedrockKeyConfig: dbKey.BedrockKeyConfig,
			}
		}
		providerConfig := ProviderConfig{
			Keys:                     keys,
			NetworkConfig:            dbProvider.NetworkConfig,
			ConcurrencyAndBufferSize: dbProvider.ConcurrencyAndBufferSize,
			ProxyConfig:              dbProvider.ProxyConfig,
			SendBackRawResponse:      dbProvider.SendBackRawResponse,
		}
		processedProviders[provider] = providerConfig
	}
	return processedProviders, nil
}

// GetMCPConfig retrieves the MCP configuration from the database.
func (s *SQLiteConfigStore) GetMCPConfig() (*schemas.MCPConfig, error) {
	var dbMCPClients []TableMCPClient
	if err := s.db.Find(&dbMCPClients).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if len(dbMCPClients) == 0 {
		return nil, nil
	}
	clientConfigs := make([]schemas.MCPClientConfig, len(dbMCPClients))
	for i, dbClient := range dbMCPClients {
		clientConfigs[i] = schemas.MCPClientConfig{
			Name:             dbClient.Name,
			ConnectionType:   schemas.MCPConnectionType(dbClient.ConnectionType),
			ConnectionString: dbClient.ConnectionString,
			StdioConfig:      dbClient.StdioConfig,
			ToolsToExecute:   dbClient.ToolsToExecute,
			ToolsToSkip:      dbClient.ToolsToSkip,
		}
	}
	return &schemas.MCPConfig{
		ClientConfigs: clientConfigs,
	}, nil
}

// UpdateMCPConfig updates the MCP configuration in the database.
func (s *SQLiteConfigStore) UpdateMCPConfig(config *schemas.MCPConfig) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Removing existing MCP clients
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableMCPClient{}).Error; err != nil {
			return err
		}

		if config == nil {
			return nil
		}

		dbClients := make([]TableMCPClient, 0, len(config.ClientConfigs))
		for _, clientConfig := range config.ClientConfigs {
			dbClient := TableMCPClient{
				Name:             clientConfig.Name,
				ConnectionType:   string(clientConfig.ConnectionType),
				ConnectionString: clientConfig.ConnectionString,
				StdioConfig:      clientConfig.StdioConfig,
				ToolsToExecute:   clientConfig.ToolsToExecute,
				ToolsToSkip:      clientConfig.ToolsToSkip,
			}

			dbClients = append(dbClients, dbClient)
		}

		if len(dbClients) > 0 {
			if err := tx.CreateInBatches(dbClients, 100).Error; err != nil {
				return err
			}
		}

		return nil
	})
}

// GetVectorStoreConfig retrieves the vector store configuration from the database.
func (s *SQLiteConfigStore) GetVectorStoreConfig() (*vectorstore.Config, error) {
	var vectorStoreType TableVectorStoreConfig
	if err := s.db.First(&vectorStoreType).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Return default cache configuration
			return &vectorstore.Config{
				Enabled: false,
			}, nil
		}
		return nil, err
	}
	// Marshalling config
	var vectorStoreConfig vectorstore.Config
	if err := json.Unmarshal([]byte(*vectorStoreType.Config), &vectorStoreConfig); err != nil {
		return nil, err
	}
	return &vectorstore.Config{
		Enabled: vectorStoreType.Enabled,
		Config:  &vectorStoreConfig,
		Type:    vectorstore.VectorStoreType(*vectorStoreType.Type),
	}, nil
}

// UpdateVectorStoreConfig updates the vector store configuration in the database.
func (s *SQLiteConfigStore) UpdateVectorStoreConfig(config *vectorstore.Config) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Delete existing cache config
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableVectorStoreConfig{}).Error; err != nil {
			return err
		}
		jsonConfig, err := bifrost.MarshalToStringPtr(config.Config)
		if err != nil {
			return err
		}
		var record = &TableVectorStoreConfig{
			Enabled: config.Enabled,
			Config:  jsonConfig,
		}
		// Create new cache config
		return tx.Create(record).Error
	})
}

// GetLogsStoreConfig retrieves the logs store configuration from the database.
func (s *SQLiteConfigStore) GetLogsStoreConfig() (*logstore.Config, error) {
	var dbConfig TableLogStoreConfig
	if err := s.db.First(&dbConfig).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if dbConfig.Config == nil || *dbConfig.Config == "" {
		return &logstore.Config{Enabled: dbConfig.Enabled}, nil
	}
	var logStoreConfig logstore.Config
	if err := json.Unmarshal([]byte(*dbConfig.Config), &logStoreConfig); err != nil {
		return nil, err
	}
	return &logStoreConfig, nil
}

// UpdateLogsStoreConfig updates the logs store configuration in the database.
func (s *SQLiteConfigStore) UpdateLogsStoreConfig(config *logstore.Config) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableLogStoreConfig{}).Error; err != nil {
			return err
		}
		jsonConfig, err := bifrost.MarshalToStringPtr(config)
		if err != nil {
			return err
		}
		var record = &TableLogStoreConfig{
			Enabled: config.Enabled,
			Type:    string(config.Type),
			Config:  jsonConfig,
		}
		return tx.Create(record).Error
	})
}

// GetEnvKeys retrieves the environment keys from the database.
func (s *SQLiteConfigStore) GetEnvKeys() (map[string][]EnvKeyInfo, error) {
	var dbEnvKeys []TableEnvKey
	if err := s.db.Find(&dbEnvKeys).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	envKeys := make(map[string][]EnvKeyInfo)
	for _, dbEnvKey := range dbEnvKeys {
		envKeys[dbEnvKey.EnvVar] = append(envKeys[dbEnvKey.EnvVar], EnvKeyInfo{
			EnvVar:     dbEnvKey.EnvVar,
			Provider:   schemas.ModelProvider(dbEnvKey.Provider),
			KeyType:    EnvKeyType(dbEnvKey.KeyType),
			ConfigPath: dbEnvKey.ConfigPath,
			KeyID:      dbEnvKey.KeyID,
		})
	}
	return envKeys, nil
}

// UpdateEnvKeys updates the environment keys in the database.
func (s *SQLiteConfigStore) UpdateEnvKeys(keys map[string][]EnvKeyInfo) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Delete existing env keys
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableEnvKey{}).Error; err != nil {
			return err
		}
		var dbEnvKeys []TableEnvKey
		for envVar, infos := range keys {
			for _, info := range infos {
				dbEnvKey := TableEnvKey{
					EnvVar:     envVar,
					Provider:   string(info.Provider),
					KeyType:    string(info.KeyType),
					ConfigPath: info.ConfigPath,
					KeyID:      info.KeyID,
				}
				dbEnvKeys = append(dbEnvKeys, dbEnvKey)
			}
		}
		if len(dbEnvKeys) > 0 {
			if err := tx.CreateInBatches(dbEnvKeys, 100).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// GetConfig retrieves a specific config from the database.
func (s *SQLiteConfigStore) GetConfig(key string) (*TableConfig, error) {
	var config TableConfig
	if err := s.db.First(&config, "key = ?", key).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &config, nil
}

// UpdateConfig updates a specific config in the database.
func (s *SQLiteConfigStore) UpdateConfig(config *TableConfig, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.Save(config).Error
}

// GetModelPrices retrieves all model pricing records from the database.
func (s *SQLiteConfigStore) GetModelPrices() ([]TableModelPricing, error) {
	var modelPrices []TableModelPricing
	if err := s.db.Find(&modelPrices).Error; err != nil {
		return nil, err
	}
	return modelPrices, nil
}

// CreateModelPrices creates a new model pricing record in the database.
func (s *SQLiteConfigStore) CreateModelPrices(pricing *TableModelPricing, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.Create(pricing).Error
}

// DeleteModelPrices deletes all model pricing records from the database.
func (s *SQLiteConfigStore) DeleteModelPrices(tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableModelPricing{}).Error
}

// GOVERNANCE METHODS

// GetVirtualKeys retrieves all virtual keys from the database.
func (s *SQLiteConfigStore) GetVirtualKeys() ([]TableVirtualKey, error) {
	var virtualKeys []TableVirtualKey

	// Preload all relationships for complete information
	if err := s.db.Preload("Team").
		Preload("Customer").
		Preload("Budget").
		Preload("RateLimit").
		Preload("Keys", func(db *gorm.DB) *gorm.DB {
			return db.Select("id, key_id, models_json")
		}).Find(&virtualKeys).Error; err != nil {
		return nil, err
	}

	return virtualKeys, nil
}

// GetVirtualKey retrieves a virtual key from the database.
func (s *SQLiteConfigStore) GetVirtualKey(id string) (*TableVirtualKey, error) {
	var virtualKey TableVirtualKey
	if err := s.db.Preload("Team").
		Preload("Customer").
		Preload("Budget").
		Preload("RateLimit").
		Preload("Keys", func(db *gorm.DB) *gorm.DB {
			return db.Select("id, key_id, models_json")
		}).First(&virtualKey, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &virtualKey, nil
}

func (s *SQLiteConfigStore) CreateVirtualKey(virtualKey *TableVirtualKey, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}

	// Create virtual key first
	if err := txDB.Create(virtualKey).Error; err != nil {
		return err
	}

	// Create key associations after the virtual key has an ID
	if len(virtualKey.Keys) > 0 {
		if err := txDB.Model(virtualKey).Association("Keys").Append(virtualKey.Keys); err != nil {
			return err
		}
	}

	return nil
}

func (s *SQLiteConfigStore) UpdateVirtualKey(virtualKey *TableVirtualKey, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}

	// Store the keys before Save() clears them
	keysToAssociate := virtualKey.Keys

	// Update virtual key first (this will clear the Keys field)
	if err := txDB.Save(virtualKey).Error; err != nil {
		return err
	}

	// Clear existing key associations
	if err := txDB.Model(virtualKey).Association("Keys").Clear(); err != nil {
		return err
	}

	// Create new key associations using the stored keys
	if len(keysToAssociate) > 0 {
		if err := txDB.Model(virtualKey).Association("Keys").Append(keysToAssociate); err != nil {
			return err
		}
	}

	return nil
}

// GetKeysByIDs retrieves multiple keys by their IDs
func (s *SQLiteConfigStore) GetKeysByIDs(ids []string) ([]TableKey, error) {
	if len(ids) == 0 {
		return []TableKey{}, nil
	}

	var keys []TableKey
	if err := s.db.Where("key_id IN ?", ids).Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

// DeleteVirtualKey deletes a virtual key from the database.
func (s *SQLiteConfigStore) DeleteVirtualKey(id string) error {
	return s.db.Delete(&TableVirtualKey{}, "id = ?", id).Error
}

// GetTeams retrieves all teams from the database.
func (s *SQLiteConfigStore) GetTeams(customerID string) ([]TableTeam, error) {
	// Preload relationships for complete information
	query := s.db.Preload("Customer").Preload("Budget")

	// Optional filtering by customer
	if customerID != "" {
		query = query.Where("customer_id = ?", customerID)
	}

	var teams []TableTeam
	if err := query.Find(&teams).Error; err != nil {
		return nil, err
	}
	return teams, nil
}

// GetTeam retrieves a specific team from the database.
func (s *SQLiteConfigStore) GetTeam(id string) (*TableTeam, error) {
	var team TableTeam
	if err := s.db.Preload("Customer").Preload("Budget").First(&team, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &team, nil
}

// CreateTeam creates a new team in the database.
func (s *SQLiteConfigStore) CreateTeam(team *TableTeam, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.Create(team).Error
}

// UpdateTeam updates an existing team in the database.
func (s *SQLiteConfigStore) UpdateTeam(team *TableTeam, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.Save(team).Error
}

// DeleteTeam deletes a team from the database.
func (s *SQLiteConfigStore) DeleteTeam(id string) error {
	return s.db.Delete(&TableTeam{}, "id = ?", id).Error
}

// GetCustomers retrieves all customers from the database.
func (s *SQLiteConfigStore) GetCustomers() ([]TableCustomer, error) {
	var customers []TableCustomer
	if err := s.db.Preload("Teams").Preload("Budget").Find(&customers).Error; err != nil {
		return nil, err
	}
	return customers, nil
}

// GetCustomer retrieves a specific customer from the database.
func (s *SQLiteConfigStore) GetCustomer(id string) (*TableCustomer, error) {
	var customer TableCustomer
	if err := s.db.Preload("Teams").Preload("Budget").First(&customer, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &customer, nil
}

// CreateCustomer creates a new customer in the database.
func (s *SQLiteConfigStore) CreateCustomer(customer *TableCustomer, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.Create(customer).Error
}

// UpdateCustomer updates an existing customer in the database.
func (s *SQLiteConfigStore) UpdateCustomer(customer *TableCustomer, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.Save(customer).Error
}

// DeleteCustomer deletes a customer from the database.
func (s *SQLiteConfigStore) DeleteCustomer(id string) error {
	return s.db.Delete(&TableCustomer{}, "id = ?", id).Error
}

// GetRateLimit retrieves a specific rate limit from the database.
func (s *SQLiteConfigStore) GetRateLimit(id string) (*TableRateLimit, error) {
	var rateLimit TableRateLimit
	if err := s.db.First(&rateLimit, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &rateLimit, nil
}

// CreateRateLimit creates a new rate limit in the database.
func (s *SQLiteConfigStore) CreateRateLimit(rateLimit *TableRateLimit, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.Create(rateLimit).Error
}

// UpdateRateLimit updates a rate limit in the database.
func (s *SQLiteConfigStore) UpdateRateLimit(rateLimit *TableRateLimit, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.Save(rateLimit).Error
}

// UpdateRateLimits updates multiple rate limits in the database.
func (s *SQLiteConfigStore) UpdateRateLimits(rateLimits []*TableRateLimit, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	for _, rl := range rateLimits {
		if err := txDB.Save(rl).Error; err != nil {
			return err
		}
	}
	return nil
}

// GetBudgets retrieves all budgets from the database.
func (s *SQLiteConfigStore) GetBudgets() ([]TableBudget, error) {
	var budgets []TableBudget
	if err := s.db.Find(&budgets).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return budgets, nil
}

// GetBudget retrieves a specific budget from the database.
func (s *SQLiteConfigStore) GetBudget(id string, tx ...*gorm.DB) (*TableBudget, error) {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	var budget TableBudget
	if err := txDB.First(&budget, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &budget, nil
}

// CreateBudget creates a new budget in the database.
func (s *SQLiteConfigStore) CreateBudget(budget *TableBudget, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.Create(budget).Error
}

// UpdateBudgets updates multiple budgets in the database.
func (s *SQLiteConfigStore) UpdateBudgets(budgets []*TableBudget, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	s.logger.Debug("updating budgets: %+v", budgets)
	for _, b := range budgets {
		if err := txDB.Save(b).Error; err != nil {
			return err
		}
	}
	return nil
}

// UpdateBudget updates a budget in the database.
func (s *SQLiteConfigStore) UpdateBudget(budget *TableBudget, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.Save(budget).Error
}

// ExecuteTransaction executes a transaction.
func (s *SQLiteConfigStore) ExecuteTransaction(fn func(tx *gorm.DB) error) error {
	return s.db.Transaction(fn)
}

func (s *SQLiteConfigStore) doesTableExist(tableName string) bool {
	var count int64
	if err := s.db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", tableName).Scan(&count).Error; err != nil {
		return false
	}
	return count > 0
}

// removeNullKeys removes null keys from the database.
func (s *SQLiteConfigStore) removeNullKeys() error {
	return s.db.Exec("DELETE FROM config_keys WHERE key_id IS NULL OR value IS NULL").Error
}

// removeDuplicateKeysAndNullKeys removes duplicate keys based on key_id and value combination
// Keeps the record with the smallest ID (oldest record) and deletes duplicates
func (s *SQLiteConfigStore) removeDuplicateKeysAndNullKeys() error {
	s.logger.Debug("removing duplicate keys and null keys from the database")
	// Check if the config_keys table exists first
	if !s.doesTableExist("config_keys") {
		return nil
	}
	s.logger.Debug("removing null keys from the database")
	// First, remove null keys
	if err := s.removeNullKeys(); err != nil {
		return fmt.Errorf("failed to remove null keys: %w", err)
	}
	s.logger.Debug("deleting duplicate keys from the database")
	// Find and delete duplicate keys, keeping only the one with the smallest ID
	// This query deletes all records except the one with the minimum ID for each (key_id, value) pair
	result := s.db.Exec(`
		DELETE FROM config_keys 
		WHERE id NOT IN (
			SELECT MIN(id) 
			FROM config_keys 
			GROUP BY key_id, value
		)
	`)

	if result.Error != nil {
		return fmt.Errorf("failed to remove duplicate keys: %w", result.Error)
	}
	s.logger.Debug("migration complete")
	return nil
}

// newSqliteConfigStore creates a new SQLite config store.
func newSqliteConfigStore(config *SQLiteConfig, logger schemas.Logger) (ConfigStore, error) {		
	if _, err := os.Stat(config.Path); os.IsNotExist(err) {
		// Create DB file
		f, err := os.Create(config.Path)
		if err != nil {
			return nil, err
		}
		_ = f.Close()
	}
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=10000&_busy_timeout=60000&_wal_autocheckpoint=1000", config.Path)
	logger.Debug("opening DB with dsn: %s", dsn)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormLogger.Default.LogMode(gormLogger.Silent),
	})

	if err != nil {
		return nil, err
	}
	logger.Debug("db opened for configstore")
	s := &SQLiteConfigStore{db: db, logger: logger}
	logger.Debug("running migration to remove duplicate keys")
	// Run migration to remove duplicate keys before AutoMigrate
	if err := s.removeDuplicateKeysAndNullKeys(); err != nil {
		return nil, fmt.Errorf("failed to remove duplicate keys: %w", err)
	}	
	// Auto migrate to all new tables
	if err := db.AutoMigrate(
		&TableConfigHash{},
		&TableProvider{},
		&TableKey{},
		&TableModel{},
		&TableMCPClient{},
		&TableClientConfig{},
		&TableEnvKey{},
		&TableVectorStoreConfig{},
		&TableLogStoreConfig{},
		&TableBudget{},
		&TableRateLimit{},
		&TableCustomer{},
		&TableTeam{},
		&TableVirtualKey{},
		&TableConfig{},
		&TableModelPricing{},
	); err != nil {
		return nil, err
	}
	return s, nil
}
