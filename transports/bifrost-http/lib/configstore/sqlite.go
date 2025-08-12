package configstore

import (
	"encoding/json"
	"errors"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib/logstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib/vectorstore"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// SQLiteConfig represents the configuration for a SQLite database.
type SQLiteConfig struct {
	Path string `json:"path"`
}

// SQLiteConfigStore represents a configuration store that uses a SQLite database.
type SQLiteConfigStore struct {
	db *gorm.DB
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
		EnableCaching:           config.EnableCaching,
		PrometheusLabels:        config.PrometheusLabels,
		AllowedOrigins:          config.AllowedOrigins,
	}
	// Delete existing client config and create new one
	if err := s.db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableClientConfig{}).Error; err != nil {
		return err
	}
	return s.db.Create(&dbConfig).Error
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
		EnableCaching:           dbConfig.EnableCaching,
		AllowedOrigins:          dbConfig.AllowedOrigins,
	}, nil
}

// UpdateProvidersConfig updates the client configuration in the database.
func (s *SQLiteConfigStore) UpdateProvidersConfig(providers map[schemas.ModelProvider]ProviderConfig) error {
	if err := s.db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableProvider{}).Error; err != nil {
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
		if err := s.db.Create(&dbProvider).Error; err != nil {
			return err
		}

		// Create keys for this provider
		dbKeys := make([]TableKey, 0, len(providerConfig.Keys))
		for _, key := range providerConfig.Keys {
			dbKey := TableKey{
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

		if len(dbKeys) > 0 {
			if err := s.db.CreateInBatches(dbKeys, 100).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// GetProvidersConfig retrieves the provider configuration from the database.
func (s *SQLiteConfigStore) GetProvidersConfig() (map[schemas.ModelProvider]ProviderConfig, error) {
	var dbProviders []TableProvider
	if err := s.db.Preload("Keys").Find(&dbProviders).Error; err != nil {
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
	// Removing existing MCP clients
	if err := s.db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableMCPClient{}).Error; err != nil {
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
		if err := s.db.CreateInBatches(dbClients, 100).Error; err != nil {
			return err
		}
	}

	return nil
}

// GetVectorStoreConfig retrieves the vector store configuration from the database.
func (s *SQLiteConfigStore) GetVectorStoreConfig() (*vectorstore.Config, error) {
	var cacheConfig TableVectorStoreConfig
	if err := s.db.First(&cacheConfig).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Return default cache configuration
			return &vectorstore.Config{
				Enabled:         false,
				TTLSeconds:      300,
				CacheByModel:    true,
				CacheByProvider: true,
			}, nil
		}
		return nil, err
	}
	// Marshalling config
	var vectorStoreConfig vectorstore.Config
	if err := json.Unmarshal([]byte(*cacheConfig.Config), &vectorStoreConfig); err != nil {
		return nil, err
	}
	return &vectorstore.Config{
		Enabled:         cacheConfig.Enabled,
		TTLSeconds:      cacheConfig.TTLSeconds,
		CacheByModel:    cacheConfig.CacheByModel,
		CacheByProvider: cacheConfig.CacheByProvider,
		Config:          &vectorStoreConfig,
		Type:            *cacheConfig.Type,
	}, nil
}

// UpdateVectorStoreConfig updates the vector store configuration in the database.
func (s *SQLiteConfigStore) UpdateVectorStoreConfig(config *vectorstore.Config) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Delete existing cache config
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableVectorStoreConfig{}).Error; err != nil {
			return err
		}

		// Create new cache config
		return tx.Create(config).Error
	})
}

// GetLogsStoreConfig retrieves the logs store configuration from the database.
func (s *SQLiteConfigStore) GetLogsStoreConfig() (*logstore.Config, error) {
	return nil, nil
}

// UpdateLogsStoreConfig updates the logs store configuration in the database.
func (s *SQLiteConfigStore) UpdateLogsStoreConfig(config *logstore.Config) error {
	return nil
}

// GetEnvKeys retrieves the environment keys from the database.
func (s *SQLiteConfigStore) GetEnvKeys() (map[string][]EnvKeyInfo, error) {
	var dbEnvKeys []TableEnvKey
	if err := s.db.Find(&dbEnvKeys).Error; err != nil {
		return nil, err
	}
	envKeys := make(map[string][]EnvKeyInfo)
	for _, dbEnvKey := range dbEnvKeys {
		envKeys[dbEnvKey.EnvVar] = append(envKeys[dbEnvKey.EnvVar], EnvKeyInfo{
			EnvVar:     dbEnvKey.EnvVar,
			Provider:   dbEnvKey.Provider,
			KeyType:    dbEnvKey.KeyType,
			ConfigPath: dbEnvKey.ConfigPath,
			KeyID:      dbEnvKey.KeyID,
		})
	}
	return envKeys, nil
}

// UpdateEnvKeys updates the environment keys in the database.
func (s *SQLiteConfigStore) UpdateEnvKeys(keys map[string][]EnvKeyInfo) error {
	// Delete existing env keys
	if err := s.db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableEnvKey{}).Error; err != nil {
		return err
	}
	var dbEnvKeys []TableEnvKey
	for envVar, infos := range keys {
		for _, info := range infos {
			dbEnvKey := TableEnvKey{
				EnvVar:     envVar,
				Provider:   info.Provider,
				KeyType:    info.KeyType,
				ConfigPath: info.ConfigPath,
				KeyID:      info.KeyID,
			}

			dbEnvKeys = append(dbEnvKeys, dbEnvKey)
		}
	}
	if len(dbEnvKeys) > 0 {
		if err := s.db.CreateInBatches(dbEnvKeys, 100).Error; err != nil {
			return err
		}
	}
	return nil
}

// newSqliteConfigStore creates a new SQLite config store.
func newSqliteConfigStore(config *SQLiteConfig) (ConfigStore, error) {
	db, err := gorm.Open(sqlite.Open(config.Path), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(
		&TableConfigHash{},
		&TableProvider{},
		&TableKey{},
		&TableMCPClient{},
		&TableClientConfig{},
		&TableEnvKey{},
		&TableVectorStoreConfig{},
	); err != nil {
		return nil, err
	}
	return &SQLiteConfigStore{db: db}, nil
}
