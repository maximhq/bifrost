package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"gorm.io/gorm"
)

// RDBConfigStore represents a configuration store that uses a relational database.
type RDBConfigStore struct {
	db     *gorm.DB
	logger schemas.Logger
}

// UpdateClientConfig updates the client configuration in the database.
func (s *RDBConfigStore) UpdateClientConfig(ctx context.Context, config *ClientConfig) error {
	dbConfig := TableClientConfig{
		DropExcessRequests:      config.DropExcessRequests,
		InitialPoolSize:         config.InitialPoolSize,
		EnableLogging:           config.EnableLogging,
		EnableGovernance:        config.EnableGovernance,
		EnforceGovernanceHeader: config.EnforceGovernanceHeader,
		AllowDirectKeys:         config.AllowDirectKeys,
		PrometheusLabels:        config.PrometheusLabels,
		AllowedOrigins:          config.AllowedOrigins,
		MaxRequestBodySizeMB:    config.MaxRequestBodySizeMB,
	}
	// Delete existing client config and create new one in a transaction
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableClientConfig{}).Error; err != nil {
			return err
		}
		return tx.Create(&dbConfig).Error
	})
}

// GetClientConfig retrieves the client configuration from the database.
func (s *RDBConfigStore) GetClientConfig(ctx context.Context) (*ClientConfig, error) {
	var dbConfig TableClientConfig
	if err := s.db.WithContext(ctx).First(&dbConfig).Error; err != nil {
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
		MaxRequestBodySizeMB:    dbConfig.MaxRequestBodySizeMB,
	}, nil
}

// UpdateProvidersConfig updates the client configuration in the database.
func (s *RDBConfigStore) UpdateProvidersConfig(ctx context.Context, providers map[schemas.ModelProvider]ProviderConfig) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Delete all existing providers (cascades to keys)
		if err := tx.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableProvider{}).Error; err != nil {
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
				CustomProviderConfig:     providerConfig.CustomProviderConfig,
			}

			// Create provider first
			if err := tx.WithContext(ctx).Create(&dbProvider).Error; err != nil {
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
				result := tx.WithContext(ctx).Where("key_id = ?", dbKey.KeyID).First(&existingKey)

				if result.Error == nil {
					// Update existing key with new data
					dbKey.ID = existingKey.ID // Keep the same database ID
					if err := tx.WithContext(ctx).Save(&dbKey).Error; err != nil {
						return err
					}
				} else if errors.Is(result.Error, gorm.ErrRecordNotFound) {
					// Create new key
					if err := tx.WithContext(ctx).Create(&dbKey).Error; err != nil {
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

// UpdateProviderById updates a single provider configuration in the database without deleting/recreating.
func (s *RDBConfigStore) UpdateProvider(ctx context.Context, provider schemas.ModelProvider, config ProviderConfig, envKeys map[string][]EnvKeyInfo) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Find the existing provider
		var dbProvider TableProvider
		if err := tx.WithContext(ctx).Where("name = ?", string(provider)).First(&dbProvider).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		// Create a deep copy of the config to avoid modifying the original
		configCopy, err := deepCopy(config)
		if err != nil {
			return err
		}
		// Substitute environment variables back to their original form
		substituteEnvVars(&configCopy, provider, envKeys)

		// Update provider fields
		dbProvider.NetworkConfig = configCopy.NetworkConfig
		dbProvider.ConcurrencyAndBufferSize = configCopy.ConcurrencyAndBufferSize
		dbProvider.ProxyConfig = configCopy.ProxyConfig
		dbProvider.SendBackRawResponse = configCopy.SendBackRawResponse
		dbProvider.CustomProviderConfig = configCopy.CustomProviderConfig

		// Save the updated provider
		if err := tx.WithContext(ctx).Save(&dbProvider).Error; err != nil {
			return err
		}

		// Get existing keys for this provider
		var existingKeys []TableKey
		if err := tx.WithContext(ctx).Where("provider_id = ?", dbProvider.ID).Find(&existingKeys).Error; err != nil {
			return err
		}

		// Create a map of existing keys by KeyID for quick lookup
		existingKeysMap := make(map[string]TableKey)
		for _, key := range existingKeys {
			existingKeysMap[key.KeyID] = key
		}

		// Process each key in the new config
		for _, key := range configCopy.Keys {
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

			// Check if this key already exists
			if existingKey, exists := existingKeysMap[key.ID]; exists {
				// Update existing key - preserve the database ID
				dbKey.ID = existingKey.ID
				if err := tx.WithContext(ctx).Save(&dbKey).Error; err != nil {
					return err
				}
				// Remove from map to track which keys are still in use
				delete(existingKeysMap, key.ID)
			} else {
				// Create new key
				if err := tx.WithContext(ctx).Create(&dbKey).Error; err != nil {
					return err
				}
			}
		}

		// Delete keys that are no longer in the new config
		for _, keyToDelete := range existingKeysMap {
			if err := tx.WithContext(ctx).Delete(&keyToDelete).Error; err != nil {
				return err
			}
		}

		return nil
	})
}

// AddProvider creates a new provider configuration in the database.
func (s *RDBConfigStore) AddProvider(ctx context.Context, provider schemas.ModelProvider, config ProviderConfig, envKeys map[string][]EnvKeyInfo) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Check if provider already exists
		var existingProvider TableProvider
		if err := tx.WithContext(ctx).Where("name = ?", string(provider)).First(&existingProvider).Error; err == nil {
			return fmt.Errorf("provider %s already exists", provider)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		// Create a deep copy of the config to avoid modifying the original
		configCopy, err := deepCopy(config)
		if err != nil {
			return err
		}
		// Substitute environment variables back to their original form
		substituteEnvVars(&configCopy, provider, envKeys)

		// Create new provider
		dbProvider := TableProvider{
			Name:                     string(provider),
			NetworkConfig:            configCopy.NetworkConfig,
			ConcurrencyAndBufferSize: configCopy.ConcurrencyAndBufferSize,
			ProxyConfig:              configCopy.ProxyConfig,
			SendBackRawResponse:      configCopy.SendBackRawResponse,
			CustomProviderConfig:     configCopy.CustomProviderConfig,
		}

		// Create the provider
		if err := tx.WithContext(ctx).Create(&dbProvider).Error; err != nil {
			return err
		}

		// Create keys for this provider
		for _, key := range configCopy.Keys {
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

			// Create the key
			if err := tx.WithContext(ctx).Create(&dbKey).Error; err != nil {
				return err
			}
		}

		return nil
	})
}

// DeleteProvider deletes a single provider and all its associated keys from the database.
func (s *RDBConfigStore) DeleteProvider(ctx context.Context, provider schemas.ModelProvider) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Find the existing provider
		var dbProvider TableProvider
		if err := tx.WithContext(ctx).Where("name = ?", string(provider)).First(&dbProvider).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		// Delete the provider (keys will be deleted due to CASCADE constraint)
		if err := tx.WithContext(ctx).Delete(&dbProvider).Error; err != nil {
			return err
		}

		return nil
	})
}

// GetProvidersConfig retrieves the provider configuration from the database.
func (s *RDBConfigStore) GetProvidersConfig(ctx context.Context) (map[schemas.ModelProvider]ProviderConfig, error) {
	var dbProviders []TableProvider
	if err := s.db.WithContext(ctx).Preload("Keys").Find(&dbProviders).Error; err != nil {
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
			// Process main key value
			processedValue, err := processEnvValue(dbKey.Value, s.logger)
			if err != nil {
				// If env var not found, keep the original value
				processedValue = dbKey.Value
			}

			// Process Azure config if present
			azureConfig := dbKey.AzureKeyConfig
			if azureConfig != nil {
				azureConfigCopy := *azureConfig
				if processedEndpoint, err := processEnvValue(azureConfig.Endpoint, s.logger); err == nil {
					azureConfigCopy.Endpoint = processedEndpoint
				}
				if azureConfig.APIVersion != nil {
					if processedAPIVersion, err := processEnvValue(*azureConfig.APIVersion, s.logger); err == nil {
						azureConfigCopy.APIVersion = &processedAPIVersion
					}
				}
				azureConfig = &azureConfigCopy
			}

			// Process Vertex config if present
			vertexConfig := dbKey.VertexKeyConfig
			if vertexConfig != nil {
				vertexConfigCopy := *vertexConfig
				if processedProjectID, err := processEnvValue(vertexConfig.ProjectID, s.logger); err == nil {
					vertexConfigCopy.ProjectID = processedProjectID
				}
				if processedRegion, err := processEnvValue(vertexConfig.Region, s.logger); err == nil {
					vertexConfigCopy.Region = processedRegion
				}
				if processedAuthCredentials, err := processEnvValue(vertexConfig.AuthCredentials, s.logger); err == nil {
					vertexConfigCopy.AuthCredentials = processedAuthCredentials
				}
				vertexConfig = &vertexConfigCopy
			}

			// Process Bedrock config if present
			bedrockConfig := dbKey.BedrockKeyConfig
			if bedrockConfig != nil {
				bedrockConfigCopy := *bedrockConfig
				if processedAccessKey, err := processEnvValue(bedrockConfig.AccessKey, s.logger); err == nil {
					bedrockConfigCopy.AccessKey = processedAccessKey
				}
				if processedSecretKey, err := processEnvValue(bedrockConfig.SecretKey, s.logger); err == nil {
					bedrockConfigCopy.SecretKey = processedSecretKey
				}
				if bedrockConfig.SessionToken != nil {
					if processedSessionToken, err := processEnvValue(*bedrockConfig.SessionToken, s.logger); err == nil {
						bedrockConfigCopy.SessionToken = &processedSessionToken
					}
				}
				if bedrockConfig.Region != nil {
					if processedRegion, err := processEnvValue(*bedrockConfig.Region, s.logger); err == nil {
						bedrockConfigCopy.Region = &processedRegion
					}
				}
				if bedrockConfig.ARN != nil {
					if processedARN, err := processEnvValue(*bedrockConfig.ARN, s.logger); err == nil {
						bedrockConfigCopy.ARN = &processedARN
					}
				}
				bedrockConfig = &bedrockConfigCopy
			}

			keys[i] = schemas.Key{
				ID:               dbKey.KeyID,
				Value:            processedValue,
				Models:           dbKey.Models,
				Weight:           dbKey.Weight,
				AzureKeyConfig:   azureConfig,
				VertexKeyConfig:  vertexConfig,
				BedrockKeyConfig: bedrockConfig,
			}
		}
		providerConfig := ProviderConfig{
			Keys:                     keys,
			NetworkConfig:            dbProvider.NetworkConfig,
			ConcurrencyAndBufferSize: dbProvider.ConcurrencyAndBufferSize,
			ProxyConfig:              dbProvider.ProxyConfig,
			SendBackRawResponse:      dbProvider.SendBackRawResponse,
			CustomProviderConfig:     dbProvider.CustomProviderConfig,
		}
		processedProviders[provider] = providerConfig
	}
	return processedProviders, nil
}

// GetMCPConfig retrieves the MCP configuration from the database.
func (s *RDBConfigStore) GetMCPConfig(ctx context.Context) (*schemas.MCPConfig, error) {
	var dbMCPClients []TableMCPClient
	if err := s.db.WithContext(ctx).Find(&dbMCPClients).Error; err != nil {
		return nil, err
	}
	if len(dbMCPClients) == 0 {
		return nil, nil
	}
	clientConfigs := make([]schemas.MCPClientConfig, len(dbMCPClients))
	for i, dbClient := range dbMCPClients {
		// Process connection string for environment variables
		var processedConnectionString *string
		if dbClient.ConnectionString != nil {
			processedValue, err := processEnvValue(*dbClient.ConnectionString, s.logger)
			if err != nil {
				// If env var not found, keep the original value
				processedValue = *dbClient.ConnectionString
			}
			processedConnectionString = &processedValue
		}

		clientConfigs[i] = schemas.MCPClientConfig{
			Name:             dbClient.Name,
			ConnectionType:   schemas.MCPConnectionType(dbClient.ConnectionType),
			ConnectionString: processedConnectionString,
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
func (s *RDBConfigStore) UpdateMCPConfig(ctx context.Context, config *schemas.MCPConfig, envKeys map[string][]EnvKeyInfo) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Removing existing MCP clients
		if err := tx.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableMCPClient{}).Error; err != nil {
			return err
		}

		if config == nil {
			return nil
		}

		// Create a deep copy of the config to avoid modifying the original
		configCopy, err := deepCopy(config)
		if err != nil {
			return err
		}
		// Substitute environment variables back to their original form
		substituteMCPEnvVars(configCopy, envKeys)

		dbClients := make([]TableMCPClient, 0, len(configCopy.ClientConfigs))
		for _, clientConfig := range configCopy.ClientConfigs {
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
			if err := tx.WithContext(ctx).CreateInBatches(dbClients, 100).Error; err != nil {
				return err
			}
		}

		return nil
	})
}

// GetVectorStoreConfig retrieves the vector store configuration from the database.
func (s *RDBConfigStore) GetVectorStoreConfig(ctx context.Context) (*vectorstore.Config, error) {
	var vectorStoreTableConfig TableVectorStoreConfig
	if err := s.db.WithContext(ctx).First(&vectorStoreTableConfig).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Return default cache configuration
			return nil, nil
		}
		return nil, err
	}
	return &vectorstore.Config{
		Enabled: vectorStoreTableConfig.Enabled,
		Config:  vectorStoreTableConfig.Config,
		Type:    vectorstore.VectorStoreType(vectorStoreTableConfig.Type),
	}, nil
}

// UpdateVectorStoreConfig updates the vector store configuration in the database.
func (s *RDBConfigStore) UpdateVectorStoreConfig(ctx context.Context, config *vectorstore.Config) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Delete existing cache config
		if err := tx.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableVectorStoreConfig{}).Error; err != nil {
			return err
		}
		jsonConfig, err := marshalToStringPtr(config.Config)
		if err != nil {
			return err
		}
		var record = &TableVectorStoreConfig{
			Type:    string(config.Type),
			Enabled: config.Enabled,
			Config:  jsonConfig,
		}
		// Create new cache config
		return tx.WithContext(ctx).Create(record).Error
	})
}

// GetLogsStoreConfig retrieves the logs store configuration from the database.
func (s *RDBConfigStore) GetLogsStoreConfig(ctx context.Context) (*logstore.Config, error) {
	var dbConfig TableLogStoreConfig
	if err := s.db.WithContext(ctx).First(&dbConfig).Error; err != nil {
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
func (s *RDBConfigStore) UpdateLogsStoreConfig(ctx context.Context, config *logstore.Config) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableLogStoreConfig{}).Error; err != nil {
			return err
		}
		jsonConfig, err := marshalToStringPtr(config)
		if err != nil {
			return err
		}
		var record = &TableLogStoreConfig{
			Enabled: config.Enabled,
			Type:    string(config.Type),
			Config:  jsonConfig,
		}
		return tx.WithContext(ctx).Create(record).Error
	})
}

// GetEnvKeys retrieves the environment keys from the database.
func (s *RDBConfigStore) GetEnvKeys(ctx context.Context) (map[string][]EnvKeyInfo, error) {
	var dbEnvKeys []TableEnvKey
	if err := s.db.WithContext(ctx).Find(&dbEnvKeys).Error; err != nil {
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
func (s *RDBConfigStore) UpdateEnvKeys(ctx context.Context, keys map[string][]EnvKeyInfo) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Delete existing env keys
		if err := tx.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableEnvKey{}).Error; err != nil {
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
			if err := tx.WithContext(ctx).CreateInBatches(dbEnvKeys, 100).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// GetConfig retrieves a specific config from the database.
func (s *RDBConfigStore) GetConfig(ctx context.Context, key string) (*TableConfig, error) {
	var config TableConfig
	if err := s.db.WithContext(ctx).First(&config, "key = ?", key).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &config, nil
}

// UpdateConfig updates a specific config in the database.
func (s *RDBConfigStore) UpdateConfig(ctx context.Context, config *TableConfig, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Save(config).Error
}

// GetModelPrices retrieves all model pricing records from the database.
func (s *RDBConfigStore) GetModelPrices(ctx context.Context) ([]TableModelPricing, error) {
	var modelPrices []TableModelPricing
	if err := s.db.WithContext(ctx).Find(&modelPrices).Error; err != nil {
		return nil, err
	}
	return modelPrices, nil
}

// CreateModelPrices creates a new model pricing record in the database.
func (s *RDBConfigStore) CreateModelPrices(ctx context.Context, pricing *TableModelPricing, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Create(pricing).Error
}

// DeleteModelPrices deletes all model pricing records from the database.
func (s *RDBConfigStore) DeleteModelPrices(ctx context.Context, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&TableModelPricing{}).Error
}

// PLUGINS METHODS

func (s *RDBConfigStore) GetPlugins(ctx context.Context) ([]TablePlugin, error) {
	var plugins []TablePlugin
	if err := s.db.WithContext(ctx).Find(&plugins).Error; err != nil {
		return nil, err
	}
	return plugins, nil
}

func (s *RDBConfigStore) GetPlugin(ctx context.Context, name string) (*TablePlugin, error) {
	var plugin TablePlugin
	if err := s.db.WithContext(ctx).First(&plugin, "name = ?", name).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &plugin, nil
}

func (s *RDBConfigStore) CreatePlugin(ctx context.Context, plugin *TablePlugin, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Create(plugin).Error
}

func (s *RDBConfigStore) UpdatePlugin(ctx context.Context, plugin *TablePlugin, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	var localTx bool

	if len(tx) > 0 {
		txDB = tx[0]
		localTx = false
	} else {
		txDB = s.db.Begin()
		localTx = true
	}

	if err := txDB.WithContext(ctx).Delete(&TablePlugin{}, "name = ?", plugin.Name).Error; err != nil {
		if localTx {
			txDB.Rollback()
		}
		return err
	}

	if err := txDB.WithContext(ctx).Create(plugin).Error; err != nil {
		if localTx {
			txDB.Rollback()
		}
		return err
	}

	if localTx {
		return txDB.Commit().Error
	}

	return nil
}

func (s *RDBConfigStore) DeletePlugin(ctx context.Context, name string, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Delete(&TablePlugin{}, "name = ?", name).Error
}

// GOVERNANCE METHODS

// GetVirtualKeys retrieves all virtual keys from the database.
func (s *RDBConfigStore) GetVirtualKeys(ctx context.Context) ([]TableVirtualKey, error) {
	var virtualKeys []TableVirtualKey

	// Preload all relationships for complete information
	if err := s.db.WithContext(ctx).Preload("Team").
		Preload("Customer").
		Preload("Budget").
		Preload("RateLimit").
		Preload("ProviderConfigs").
		Preload("Keys", func(db *gorm.DB) *gorm.DB {
			return db.Select("id, key_id, models_json")
		}).Find(&virtualKeys).Error; err != nil {
		return nil, err
	}

	return virtualKeys, nil
}

// GetVirtualKey retrieves a virtual key from the database.
func (s *RDBConfigStore) GetVirtualKey(ctx context.Context, id string) (*TableVirtualKey, error) {
	var virtualKey TableVirtualKey
	if err := s.db.WithContext(ctx).Preload("Team").
		Preload("Customer").
		Preload("Budget").
		Preload("RateLimit").
		Preload("ProviderConfigs").
		Preload("Keys", func(db *gorm.DB) *gorm.DB {
			return db.Select("id, key_id, models_json")
		}).First(&virtualKey, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &virtualKey, nil
}

func (s *RDBConfigStore) CreateVirtualKey(ctx context.Context, virtualKey *TableVirtualKey, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}

	// Create virtual key first
	if err := txDB.WithContext(ctx).Create(virtualKey).Error; err != nil {
		return err
	}

	// Create key associations after the virtual key has an ID
	if len(virtualKey.Keys) > 0 {
		if err := txDB.WithContext(ctx).Model(virtualKey).Association("Keys").Append(virtualKey.Keys); err != nil {
			return err
		}
	}

	return nil
}

func (s *RDBConfigStore) UpdateVirtualKey(ctx context.Context, virtualKey *TableVirtualKey, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}

	// Store the keys before Save() clears them
	keysToAssociate := virtualKey.Keys

	// Update virtual key first (this will clear the Keys field)
	if err := txDB.WithContext(ctx).Save(virtualKey).Error; err != nil {
		return err
	}

	// Clear existing key associations
	if err := txDB.WithContext(ctx).Model(virtualKey).Association("Keys").Clear(); err != nil {
		return err
	}

	// Create new key associations using the stored keys
	if len(keysToAssociate) > 0 {
		if err := txDB.WithContext(ctx).Model(virtualKey).Association("Keys").Append(keysToAssociate); err != nil {
			return err
		}
	}

	return nil
}

// GetKeysByIDs retrieves multiple keys by their IDs
func (s *RDBConfigStore) GetKeysByIDs(ctx context.Context, ids []string) ([]TableKey, error) {
	if len(ids) == 0 {
		return []TableKey{}, nil
	}

	var keys []TableKey
	if err := s.db.WithContext(ctx).Where("key_id IN ?", ids).Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

// DeleteVirtualKey deletes a virtual key from the database.
func (s *RDBConfigStore) DeleteVirtualKey(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Delete(&TableVirtualKey{}, "id = ?", id).Error
}

// GetVirtualKeyProviderConfigs retrieves all virtual key provider configs from the database.
func (s *RDBConfigStore) GetVirtualKeyProviderConfigs(ctx context.Context, virtualKeyID string) ([]TableVirtualKeyProviderConfig, error) {
	var virtualKey TableVirtualKey
	if err := s.db.WithContext(ctx).First(&virtualKey, "id = ?", virtualKeyID).Error; err != nil {
		return nil, err
	}

	if virtualKey.ID == "" {
		return nil, nil
	}

	var providerConfigs []TableVirtualKeyProviderConfig
	if err := s.db.WithContext(ctx).Where("virtual_key_id = ?", virtualKey.ID).Find(&providerConfigs).Error; err != nil {
		return nil, err
	}
	return providerConfigs, nil
}

// CreateVirtualKeyProviderConfig creates a new virtual key provider config in the database.
func (s *RDBConfigStore) CreateVirtualKeyProviderConfig(ctx context.Context, virtualKeyProviderConfig *TableVirtualKeyProviderConfig, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Create(virtualKeyProviderConfig).Error
}

// UpdateVirtualKeyProviderConfig updates a virtual key provider config in the database.
func (s *RDBConfigStore) UpdateVirtualKeyProviderConfig(ctx context.Context, virtualKeyProviderConfig *TableVirtualKeyProviderConfig, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Save(virtualKeyProviderConfig).Error
}

// DeleteVirtualKeyProviderConfig deletes a virtual key provider config from the database.
func (s *RDBConfigStore) DeleteVirtualKeyProviderConfig(ctx context.Context, id uint, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Delete(&TableVirtualKeyProviderConfig{}, "id = ?", id).Error
}

// GetVirtualKeyByValue retrieves a virtual key by its value
func (s *RDBConfigStore) GetVirtualKeyByValue(ctx context.Context, value string) (*TableVirtualKey, error) {
	var virtualKey TableVirtualKey
	if err := s.db.WithContext(ctx).Preload("Team").
		Preload("Customer").
		Preload("Budget").
		Preload("RateLimit").
		Preload("ProviderConfigs").
		Preload("Keys", func(db *gorm.DB) *gorm.DB {
			return db.Select("id, key_id, models_json")
		}).First(&virtualKey, "value = ?", value).Error; err != nil {
		return nil, err
	}
	return &virtualKey, nil
}

// GetTeams retrieves all teams from the database.
func (s *RDBConfigStore) GetTeams(ctx context.Context, customerID string) ([]TableTeam, error) {
	// Preload relationships for complete information
	query := s.db.WithContext(ctx).Preload("Customer").Preload("Budget")

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
func (s *RDBConfigStore) GetTeam(ctx context.Context, id string) (*TableTeam, error) {
	var team TableTeam
	if err := s.db.WithContext(ctx).Preload("Customer").Preload("Budget").First(&team, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &team, nil
}

// CreateTeam creates a new team in the database.
func (s *RDBConfigStore) CreateTeam(ctx context.Context, team *TableTeam, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Create(team).Error
}

// UpdateTeam updates an existing team in the database.
func (s *RDBConfigStore) UpdateTeam(ctx context.Context, team *TableTeam, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Save(team).Error
}

// DeleteTeam deletes a team from the database.
func (s *RDBConfigStore) DeleteTeam(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Delete(&TableTeam{}, "id = ?", id).Error
}

// GetCustomers retrieves all customers from the database.
func (s *RDBConfigStore) GetCustomers(ctx context.Context) ([]TableCustomer, error) {
	var customers []TableCustomer
	if err := s.db.WithContext(ctx).Preload("Teams").Preload("Budget").Find(&customers).Error; err != nil {
		return nil, err
	}
	return customers, nil
}

// GetCustomer retrieves a specific customer from the database.
func (s *RDBConfigStore) GetCustomer(ctx context.Context, id string) (*TableCustomer, error) {
	var customer TableCustomer
	if err := s.db.WithContext(ctx).Preload("Teams").Preload("Budget").First(&customer, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &customer, nil
}

// CreateCustomer creates a new customer in the database.
func (s *RDBConfigStore) CreateCustomer(ctx context.Context, customer *TableCustomer, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Create(customer).Error
}

// UpdateCustomer updates an existing customer in the database.
func (s *RDBConfigStore) UpdateCustomer(ctx context.Context, customer *TableCustomer, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Save(customer).Error
}

// DeleteCustomer deletes a customer from the database.
func (s *RDBConfigStore) DeleteCustomer(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Delete(&TableCustomer{}, "id = ?", id).Error
}

// GetRateLimit retrieves a specific rate limit from the database.
func (s *RDBConfigStore) GetRateLimit(ctx context.Context, id string) (*TableRateLimit, error) {
	var rateLimit TableRateLimit
	if err := s.db.WithContext(ctx).First(&rateLimit, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &rateLimit, nil
}

// CreateRateLimit creates a new rate limit in the database.
func (s *RDBConfigStore) CreateRateLimit(ctx context.Context, rateLimit *TableRateLimit, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Create(rateLimit).Error
}

// UpdateRateLimit updates a rate limit in the database.
func (s *RDBConfigStore) UpdateRateLimit(ctx context.Context, rateLimit *TableRateLimit, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Save(rateLimit).Error
}

// UpdateRateLimits updates multiple rate limits in the database.
func (s *RDBConfigStore) UpdateRateLimits(ctx context.Context, rateLimits []*TableRateLimit, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	for _, rl := range rateLimits {
		if err := txDB.WithContext(ctx).Save(rl).Error; err != nil {
			return err
		}
	}
	return nil
}

// GetBudgets retrieves all budgets from the database.
func (s *RDBConfigStore) GetBudgets(ctx context.Context) ([]TableBudget, error) {
	var budgets []TableBudget
	if err := s.db.WithContext(ctx).Find(&budgets).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return budgets, nil
}

// GetBudget retrieves a specific budget from the database.
func (s *RDBConfigStore) GetBudget(ctx context.Context, id string, tx ...*gorm.DB) (*TableBudget, error) {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	var budget TableBudget
	if err := txDB.WithContext(ctx).First(&budget, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &budget, nil
}

// CreateBudget creates a new budget in the database.
func (s *RDBConfigStore) CreateBudget(ctx context.Context, budget *TableBudget, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Create(budget).Error
}

// UpdateBudgets updates multiple budgets in the database.
func (s *RDBConfigStore) UpdateBudgets(ctx context.Context, budgets []*TableBudget, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	s.logger.Debug("updating budgets: %+v", budgets)
	for _, b := range budgets {
		if err := txDB.WithContext(ctx).Save(b).Error; err != nil {
			return err
		}
	}
	return nil
}

// UpdateBudget updates a budget in the database.
func (s *RDBConfigStore) UpdateBudget(ctx context.Context, budget *TableBudget, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Save(budget).Error
}

// GetGovernanceConfig retrieves the governance configuration from the database.
func (s *RDBConfigStore) GetGovernanceConfig(ctx context.Context) (*GovernanceConfig, error) {
	var virtualKeys []TableVirtualKey
	var teams []TableTeam
	var customers []TableCustomer
	var budgets []TableBudget
	var rateLimits []TableRateLimit

	if err := s.db.WithContext(ctx).Preload("ProviderConfigs").Find(&virtualKeys).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Find(&teams).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Find(&customers).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Find(&budgets).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Find(&rateLimits).Error; err != nil {
		return nil, err
	}

	if len(virtualKeys) == 0 && len(teams) == 0 && len(customers) == 0 && len(budgets) == 0 && len(rateLimits) == 0 {
		return nil, nil
	}

	return &GovernanceConfig{
		VirtualKeys: virtualKeys,
		Teams:       teams,
		Customers:   customers,
		Budgets:     budgets,
		RateLimits:  rateLimits,
	}, nil
}

// ExecuteTransaction executes a transaction.
func (s *RDBConfigStore) ExecuteTransaction(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return s.db.WithContext(ctx).Transaction(fn)
}

// doesTableExist checks if a table exists in the database.
func (s *RDBConfigStore) doesTableExist(ctx context.Context, tableName string) bool {
	return s.db.WithContext(ctx).Migrator().HasTable(tableName)
}

// removeNullKeys removes null keys from the database.
func (s *RDBConfigStore) removeNullKeys(ctx context.Context) error {
	return s.db.WithContext(ctx).Exec("DELETE FROM config_keys WHERE key_id IS NULL OR value IS NULL").Error
}

// removeDuplicateKeysAndNullKeys removes duplicate keys based on key_id and value combination
// Keeps the record with the smallest ID (oldest record) and deletes duplicates
func (s *RDBConfigStore) removeDuplicateKeysAndNullKeys(ctx context.Context) error {
	s.logger.Debug("removing duplicate keys and null keys from the database")
	// Check if the config_keys table exists first
	if !s.doesTableExist(ctx, "config_keys") {
		return nil
	}
	s.logger.Debug("removing null keys from the database")
	// First, remove null keys
	if err := s.removeNullKeys(ctx); err != nil {
		return fmt.Errorf("failed to remove null keys: %w", err)
	}
	s.logger.Debug("deleting duplicate keys from the database")
	// Find and delete duplicate keys, keeping only the one with the smallest ID
	// This query deletes all records except the one with the minimum ID for each (key_id, value) pair
	result := s.db.WithContext(ctx).Exec(`
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

// Close closes the SQLite config store.
func (s *RDBConfigStore) Close(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
