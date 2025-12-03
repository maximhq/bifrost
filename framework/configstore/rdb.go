package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/framework/envutils"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/migrator"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RDBConfigStore represents a configuration store that uses a relational database.
type RDBConfigStore struct {
	db     *gorm.DB
	logger schemas.Logger
}

// UpdateClientConfig updates the client configuration in the database.
func (s *RDBConfigStore) UpdateClientConfig(ctx context.Context, config *ClientConfig) error {
	dbConfig := tables.TableClientConfig{
		DropExcessRequests:      config.DropExcessRequests,
		InitialPoolSize:         config.InitialPoolSize,
		EnableLogging:           config.EnableLogging,
		DisableContentLogging:   config.DisableContentLogging,
		LogRetentionDays:        config.LogRetentionDays,
		EnableGovernance:        config.EnableGovernance,
		EnforceGovernanceHeader: config.EnforceGovernanceHeader,
		AllowDirectKeys:         config.AllowDirectKeys,
		PrometheusLabels:        config.PrometheusLabels,
		AllowedOrigins:          config.AllowedOrigins,
		MaxRequestBodySizeMB:    config.MaxRequestBodySizeMB,
		EnableLiteLLMFallbacks:  config.EnableLiteLLMFallbacks,
	}
	// Delete existing client config and create new one in a transaction
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&tables.TableClientConfig{}).Error; err != nil {
			return err
		}
		return tx.Create(&dbConfig).Error
	})
}

// Ping checks if the database is reachable.
func (s *RDBConfigStore) Ping(ctx context.Context) error {
	return s.db.WithContext(ctx).Exec("SELECT 1").Error
}

// DB returns the underlying database connection.
func (s *RDBConfigStore) DB() *gorm.DB {
	return s.db
}

// parseGormError parses GORM errors to provide user-friendly error messages.
// Currently handles unique constraint violations and is designed to be extended
// for other error types in the future (e.g., foreign key violations, not null constraints).
func (s *RDBConfigStore) parseGormError(err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}

	errMsg := err.Error()

	// Check for unique constraint violations
	// SQLite format: "UNIQUE constraint failed: table_name.column_name"
	// PostgreSQL format: "ERROR: duplicate key value violates unique constraint"

	if strings.Contains(errMsg, "UNIQUE constraint failed") ||
		strings.Contains(errMsg, "duplicate key value violates unique constraint") {

		// Extract column name from error message
		var columnName string

		// SQLite: extract from "UNIQUE constraint failed: table.column"
		if strings.Contains(errMsg, "UNIQUE constraint failed") {
			parts := strings.Split(errMsg, "UNIQUE constraint failed:")
			if len(parts) > 1 {
				tableColumn := strings.TrimSpace(parts[1])
				// Extract column name after the last dot
				if dotIndex := strings.LastIndex(tableColumn, "."); dotIndex != -1 {
					columnName = tableColumn[dotIndex+1:]
				} else {
					columnName = tableColumn
				}
			}
		} else if strings.Contains(errMsg, "duplicate key value violates unique constraint") {
			// PostgreSQL: try to extract from constraint name or detail
			// Example: duplicate key value violates unique constraint "idx_key_name"
			// Detail: Key (name)=(value) already exists.

			// First try to extract from Detail
			if strings.Contains(errMsg, "Key (") {
				startIdx := strings.Index(errMsg, "Key (")
				if startIdx != -1 {
					rest := errMsg[startIdx+5:]
					endIdx := strings.Index(rest, ")")
					if endIdx != -1 {
						columnName = rest[:endIdx]
					}
				}
			}
			// If not found, try to parse from constraint name
			if columnName == "" {
				// Extract constraint name
				if strings.Contains(errMsg, `"`) {
					parts := strings.Split(errMsg, `"`)
					if len(parts) >= 2 {
						constraintName := parts[1]
						// Remove idx_ prefix and try to extract column name
						if strings.HasPrefix(constraintName, "idx_") {
							constraintName = constraintName[4:]
							// Find the last underscore to get column name
							if lastUnderscore := strings.LastIndex(constraintName, "_"); lastUnderscore != -1 {
								columnName = constraintName[lastUnderscore+1:]
							} else {
								columnName = constraintName
							}
						}
					}
				}
			}
		}
		// Clean up column name (remove underscores, convert to readable format)
		if columnName != "" {
			// Convert snake_case to space-separated words
			columnName = strings.ReplaceAll(columnName, "_", " ")
			return fmt.Errorf("a record with this %s already exists. Please use a different value", columnName)
		}
		// Fallback message if we couldn't parse the column name
		return fmt.Errorf("a record with this value already exists. Please use a different value")
	}

	// For other errors, return the original error
	// Future: add handling for foreign key violations, not null constraints, etc.
	return err
}

// UpdateFrameworkConfig updates the framework configuration in the database.
func (s *RDBConfigStore) UpdateFrameworkConfig(ctx context.Context, config *tables.TableFrameworkConfig) error {
	// Update the framework configuration
	return s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&tables.TableFrameworkConfig{}).Error; err != nil {
			return err
		}
		return tx.Create(config).Error
	})

}

// GetFrameworkConfig retrieves the framework configuration from the database.
func (s *RDBConfigStore) GetFrameworkConfig(ctx context.Context) (*tables.TableFrameworkConfig, error) {
	var dbConfig tables.TableFrameworkConfig
	if err := s.db.WithContext(ctx).First(&dbConfig).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &dbConfig, nil
}

// GetClientConfig retrieves the client configuration from the database.
func (s *RDBConfigStore) GetClientConfig(ctx context.Context) (*ClientConfig, error) {
	var dbConfig tables.TableClientConfig
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
		DisableContentLogging:   dbConfig.DisableContentLogging,
		LogRetentionDays:        dbConfig.LogRetentionDays,
		EnableGovernance:        dbConfig.EnableGovernance,
		EnforceGovernanceHeader: dbConfig.EnforceGovernanceHeader,
		AllowDirectKeys:         dbConfig.AllowDirectKeys,
		AllowedOrigins:          dbConfig.AllowedOrigins,
		MaxRequestBodySizeMB:    dbConfig.MaxRequestBodySizeMB,
		EnableLiteLLMFallbacks:  dbConfig.EnableLiteLLMFallbacks,
	}, nil
}

// UpdateProvidersConfig updates the client configuration in the database.
func (s *RDBConfigStore) UpdateProvidersConfig(ctx context.Context, providers map[schemas.ModelProvider]ProviderConfig, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	for providerName, providerConfig := range providers {
		dbProvider := tables.TableProvider{
			Name:                     string(providerName),
			NetworkConfig:            providerConfig.NetworkConfig,
			ConcurrencyAndBufferSize: providerConfig.ConcurrencyAndBufferSize,
			ProxyConfig:              providerConfig.ProxyConfig,
			SendBackRawRequest:       providerConfig.SendBackRawRequest,
			SendBackRawResponse:      providerConfig.SendBackRawResponse,
			CustomProviderConfig:     providerConfig.CustomProviderConfig,
			ConfigHash:               providerConfig.ConfigHash,
		}

		// Upsert provider (create or update if exists)
		if err := txDB.WithContext(ctx).Clauses(
			clause.OnConflict{
				Columns:   []clause.Column{{Name: "name"}},
				UpdateAll: true,
			},
			clause.Returning{Columns: []clause.Column{{Name: "id"}}},
		).Create(&dbProvider).Error; err != nil {
			return s.parseGormError(err)
		}

		// Create keys for this provider
		dbKeys := make([]tables.TableKey, 0, len(providerConfig.Keys))
		for _, key := range providerConfig.Keys {
			// Generate key hash
			keyHash, err := GenerateKeyHash(key)
			if err != nil {
				return fmt.Errorf("failed to generate key hash: %w", err)
			}
			dbKey := tables.TableKey{
				Provider:         dbProvider.Name,
				ProviderID:       dbProvider.ID,
				KeyID:            key.ID,
				Name:             key.Name,
				Value:            key.Value,
				Models:           key.Models,
				Weight:           key.Weight,
				AzureKeyConfig:   key.AzureKeyConfig,
				VertexKeyConfig:  key.VertexKeyConfig,
				BedrockKeyConfig: key.BedrockKeyConfig,
				ConfigHash:       keyHash,
			}

			// Handle Azure config
			if key.AzureKeyConfig != nil {
				dbKey.AzureEndpoint = &key.AzureKeyConfig.Endpoint
				dbKey.AzureAPIVersion = key.AzureKeyConfig.APIVersion
			}

			// Handle Vertex config
			if key.VertexKeyConfig != nil {
				dbKey.VertexProjectID = &key.VertexKeyConfig.ProjectID
				dbKey.VertexProjectNumber = &key.VertexKeyConfig.ProjectNumber
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
			var existingKey tables.TableKey
			result := txDB.WithContext(ctx).Where("key_id = ?", dbKey.KeyID).First(&existingKey)

			if result.Error == nil {
				// Update existing key with new data
				dbKey.ID = existingKey.ID                 // Keep the same database ID
				dbKey.ProviderID = existingKey.ProviderID // Preserve the existing ProviderID
				if err := txDB.WithContext(ctx).Save(&dbKey).Error; err != nil {
					return s.parseGormError(err)
				}
			} else if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				// Create new key
				if err := txDB.WithContext(ctx).Create(&dbKey).Error; err != nil {
					return s.parseGormError(err)
				}
			} else {
				// Other error occurred
				return result.Error
			}
		}
	}
	return nil
}

// UpdateProvider updates a single provider configuration in the database without deleting/recreating.
func (s *RDBConfigStore) UpdateProvider(ctx context.Context, provider schemas.ModelProvider, config ProviderConfig, envKeys map[string][]EnvKeyInfo, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	// Find the existing provider
	var dbProvider tables.TableProvider
	if err := txDB.WithContext(ctx).Where("name = ?", string(provider)).First(&dbProvider).Error; err != nil {
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
	dbProvider.SendBackRawRequest = configCopy.SendBackRawRequest
	dbProvider.SendBackRawResponse = configCopy.SendBackRawResponse
	dbProvider.CustomProviderConfig = configCopy.CustomProviderConfig
	dbProvider.ConfigHash = configCopy.ConfigHash

	// Save the updated provider
	if err := txDB.WithContext(ctx).Save(&dbProvider).Error; err != nil {
		return s.parseGormError(err)
	}

	// Get existing keys for this provider
	var existingKeys []tables.TableKey
	if err := txDB.WithContext(ctx).Where("provider_id = ?", dbProvider.ID).Find(&existingKeys).Error; err != nil {
		return err
	}

	// Create a map of existing keys by KeyID for quick lookup
	existingKeysMap := make(map[string]tables.TableKey)
	for _, key := range existingKeys {
		existingKeysMap[key.KeyID] = key
	}

	// Process each key in the new config
	for _, key := range configCopy.Keys {
		// Generate key hash
		keyHash, err := GenerateKeyHash(key)
		if err != nil {
			return fmt.Errorf("failed to generate key hash: %w", err)
		}
		dbKey := tables.TableKey{
			Provider:         dbProvider.Name,
			ProviderID:       dbProvider.ID,
			KeyID:            key.ID,
			Name:             key.Name,
			Value:            key.Value,
			Models:           key.Models,
			Weight:           key.Weight,
			AzureKeyConfig:   key.AzureKeyConfig,
			VertexKeyConfig:  key.VertexKeyConfig,
			BedrockKeyConfig: key.BedrockKeyConfig,
			ConfigHash:       keyHash,
		}

		// Handle Azure config
		if key.AzureKeyConfig != nil {
			dbKey.AzureEndpoint = &key.AzureKeyConfig.Endpoint
			dbKey.AzureAPIVersion = key.AzureKeyConfig.APIVersion
		}

		// Handle Vertex config
		if key.VertexKeyConfig != nil {
			dbKey.VertexProjectID = &key.VertexKeyConfig.ProjectID
			dbKey.VertexProjectNumber = &key.VertexKeyConfig.ProjectNumber
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
			if err := txDB.WithContext(ctx).Save(&dbKey).Error; err != nil {
				return s.parseGormError(err)
			}
			// Remove from map to track which keys are still in use
			delete(existingKeysMap, key.ID)
		} else {
			// Create new key
			if err := txDB.WithContext(ctx).Create(&dbKey).Error; err != nil {
				return s.parseGormError(err)
			}
		}
	}

	// Delete keys that are no longer in the new config
	for _, keyToDelete := range existingKeysMap {
		if err := txDB.WithContext(ctx).Delete(&keyToDelete).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
	}

	return nil
}

// AddProvider creates a new provider configuration in the database.
func (s *RDBConfigStore) AddProvider(ctx context.Context, provider schemas.ModelProvider, config ProviderConfig, envKeys map[string][]EnvKeyInfo, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	// Create a deep copy of the config to avoid modifying the original
	configCopy, err := deepCopy(config)
	if err != nil {
		return err
	}
	// Substitute environment variables back to their original form
	substituteEnvVars(&configCopy, provider, envKeys)

	// Create new provider
	dbProvider := tables.TableProvider{
		Name:                     string(provider),
		NetworkConfig:            configCopy.NetworkConfig,
		ConcurrencyAndBufferSize: configCopy.ConcurrencyAndBufferSize,
		ProxyConfig:              configCopy.ProxyConfig,
		SendBackRawRequest:       configCopy.SendBackRawRequest,
		SendBackRawResponse:      configCopy.SendBackRawResponse,
		CustomProviderConfig:     configCopy.CustomProviderConfig,
		ConfigHash:               configCopy.ConfigHash,
	}

	// Create the provider
	if err := txDB.WithContext(ctx).Create(&dbProvider).Error; err != nil {
		return s.parseGormError(err)
	}

	// Create keys for this provider
	for _, key := range configCopy.Keys {
		// Generate key hash
		keyHash, err := GenerateKeyHash(key)
		if err != nil {
			return fmt.Errorf("failed to generate key hash: %w", err)
		}
		dbKey := tables.TableKey{
			Provider:         dbProvider.Name,
			ProviderID:       dbProvider.ID,
			KeyID:            key.ID,
			Name:             key.Name,
			Value:            key.Value,
			Models:           key.Models,
			Weight:           key.Weight,
			AzureKeyConfig:   key.AzureKeyConfig,
			VertexKeyConfig:  key.VertexKeyConfig,
			BedrockKeyConfig: key.BedrockKeyConfig,
			ConfigHash:       keyHash,
		}

		// Handle Azure config
		if key.AzureKeyConfig != nil {
			dbKey.AzureEndpoint = &key.AzureKeyConfig.Endpoint
			dbKey.AzureAPIVersion = key.AzureKeyConfig.APIVersion
		}

		// Handle Vertex config
		if key.VertexKeyConfig != nil {
			dbKey.VertexProjectID = &key.VertexKeyConfig.ProjectID
			dbKey.VertexProjectNumber = &key.VertexKeyConfig.ProjectNumber
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
		if err := txDB.WithContext(ctx).Create(&dbKey).Error; err != nil {
			return s.parseGormError(err)
		}
	}

	return nil
}

// DeleteProvider deletes a single provider and all its associated keys from the database.
func (s *RDBConfigStore) DeleteProvider(ctx context.Context, provider schemas.ModelProvider, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	// Find the existing provider
	var dbProvider tables.TableProvider
	if err := txDB.WithContext(ctx).Where("name = ?", string(provider)).First(&dbProvider).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}

	// Delete the provider (keys will be deleted due to CASCADE constraint)
	if err := txDB.WithContext(ctx).Delete(&dbProvider).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}

	return nil
}

// GetProvidersConfig retrieves the provider configuration from the database.
func (s *RDBConfigStore) GetProvidersConfig(ctx context.Context) (map[schemas.ModelProvider]ProviderConfig, error) {
	var dbProviders []tables.TableProvider
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
			processedValue, err := envutils.ProcessEnvValue(dbKey.Value)
			if err != nil {
				// If env var not found, keep the original value
				processedValue = dbKey.Value
			}

			// Process Azure config if present
			azureConfig := dbKey.AzureKeyConfig
			if azureConfig != nil {
				azureConfigCopy := *azureConfig
				if processedEndpoint, err := envutils.ProcessEnvValue(azureConfig.Endpoint); err == nil {
					azureConfigCopy.Endpoint = processedEndpoint
				}
				if azureConfig.APIVersion != nil {
					if processedAPIVersion, err := envutils.ProcessEnvValue(*azureConfig.APIVersion); err == nil {
						azureConfigCopy.APIVersion = &processedAPIVersion
					}
				}
				azureConfig = &azureConfigCopy
			}

			// Process Vertex config if present
			vertexConfig := dbKey.VertexKeyConfig
			if vertexConfig != nil {
				vertexConfigCopy := *vertexConfig
				if processedProjectID, err := envutils.ProcessEnvValue(vertexConfig.ProjectID); err == nil {
					vertexConfigCopy.ProjectID = processedProjectID
				}
				if processedProjectNumber, err := envutils.ProcessEnvValue(vertexConfig.ProjectNumber); err == nil {
					vertexConfigCopy.ProjectNumber = processedProjectNumber
				}
				if processedRegion, err := envutils.ProcessEnvValue(vertexConfig.Region); err == nil {
					vertexConfigCopy.Region = processedRegion
				}
				if processedAuthCredentials, err := envutils.ProcessEnvValue(vertexConfig.AuthCredentials); err == nil {
					vertexConfigCopy.AuthCredentials = processedAuthCredentials
				}
				vertexConfig = &vertexConfigCopy
			}

			// Process Bedrock config if present
			bedrockConfig := dbKey.BedrockKeyConfig
			if bedrockConfig != nil {
				bedrockConfigCopy := *bedrockConfig
				if processedAccessKey, err := envutils.ProcessEnvValue(bedrockConfig.AccessKey); err == nil {
					bedrockConfigCopy.AccessKey = processedAccessKey
				}
				if processedSecretKey, err := envutils.ProcessEnvValue(bedrockConfig.SecretKey); err == nil {
					bedrockConfigCopy.SecretKey = processedSecretKey
				}
				if bedrockConfig.SessionToken != nil {
					if processedSessionToken, err := envutils.ProcessEnvValue(*bedrockConfig.SessionToken); err == nil {
						bedrockConfigCopy.SessionToken = &processedSessionToken
					}
				}
				if bedrockConfig.Region != nil {
					if processedRegion, err := envutils.ProcessEnvValue(*bedrockConfig.Region); err == nil {
						bedrockConfigCopy.Region = &processedRegion
					}
				}
				if bedrockConfig.ARN != nil {
					if processedARN, err := envutils.ProcessEnvValue(*bedrockConfig.ARN); err == nil {
						bedrockConfigCopy.ARN = &processedARN
					}
				}
				bedrockConfig = &bedrockConfigCopy
			}

			keys[i] = schemas.Key{
				ID:               dbKey.KeyID,
				Name:             dbKey.Name,
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
			SendBackRawRequest:       dbProvider.SendBackRawRequest,
			SendBackRawResponse:      dbProvider.SendBackRawResponse,
			CustomProviderConfig:     dbProvider.CustomProviderConfig,
			ConfigHash:               dbProvider.ConfigHash,
		}
		processedProviders[provider] = providerConfig
	}
	return processedProviders, nil
}

// GetMCPConfig retrieves the MCP configuration from the database.
func (s *RDBConfigStore) GetMCPConfig(ctx context.Context) (*schemas.MCPConfig, error) {
	var dbMCPClients []tables.TableMCPClient
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
			processedValue, err := envutils.ProcessEnvValue(*dbClient.ConnectionString)
			if err != nil {
				// If env var not found, keep the original value
				processedValue = *dbClient.ConnectionString
			}
			processedConnectionString = &processedValue
		}

		// Process headers
		var processedHeaders map[string]string
		if dbClient.Headers != nil {
			processedHeaders = make(map[string]string, len(dbClient.Headers))
			for header, value := range dbClient.Headers {
				processedValue, err := envutils.ProcessEnvValue(value)
				if err == nil {
					processedHeaders[header] = processedValue
				} else {
					processedHeaders[header] = value
				}
			}
		}

		clientConfigs[i] = schemas.MCPClientConfig{
			ID:               dbClient.ClientID,
			Name:             dbClient.Name,
			ConnectionType:   schemas.MCPConnectionType(dbClient.ConnectionType),
			ConnectionString: processedConnectionString,
			StdioConfig:      dbClient.StdioConfig,
			ToolsToExecute:   dbClient.ToolsToExecute,
			Headers:          processedHeaders,
		}
	}
	return &schemas.MCPConfig{
		ClientConfigs: clientConfigs,
	}, nil
}

// GetMCPClientByName retrieves an MCP client by name from the database.
func (s *RDBConfigStore) GetMCPClientByName(ctx context.Context, name string) (*tables.TableMCPClient, error) {
	var mcpClient tables.TableMCPClient
	if err := s.db.WithContext(ctx).Where("name = ?", name).First(&mcpClient).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &mcpClient, nil
}

// CreateMCPClientConfig creates a new MCP client configuration in the database.
func (s *RDBConfigStore) CreateMCPClientConfig(ctx context.Context, clientConfig schemas.MCPClientConfig, envKeys map[string][]EnvKeyInfo) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Create a deep copy to avoid modifying the original
		clientConfigCopy, err := deepCopy(clientConfig)
		if err != nil {
			return err
		}

		// Substitute environment variables back to their original form
		substituteMCPClientEnvVars(&clientConfigCopy, envKeys)

		// Create new client
		dbClient := tables.TableMCPClient{
			ClientID:         clientConfigCopy.ID,
			Name:             clientConfigCopy.Name,
			ConnectionType:   string(clientConfigCopy.ConnectionType),
			ConnectionString: clientConfigCopy.ConnectionString,
			StdioConfig:      clientConfigCopy.StdioConfig,
			ToolsToExecute:   clientConfigCopy.ToolsToExecute,
			Headers:          clientConfigCopy.Headers,
		}

		if err := tx.WithContext(ctx).Create(&dbClient).Error; err != nil {
			return s.parseGormError(err)
		}
		return nil
	})
}

// UpdateMCPClientConfig updates an existing MCP client configuration in the database.
func (s *RDBConfigStore) UpdateMCPClientConfig(ctx context.Context, id string, clientConfig schemas.MCPClientConfig, envKeys map[string][]EnvKeyInfo) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Find existing client
		var existingClient tables.TableMCPClient
		if err := tx.WithContext(ctx).Where("client_id = ?", id).First(&existingClient).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("MCP client with id '%s' not found", id)
			}
			return err
		}

		// Create a deep copy to avoid modifying the original
		clientConfigCopy, err := deepCopy(clientConfig)
		if err != nil {
			return err
		}

		// Substitute environment variables back to their original form
		substituteMCPClientEnvVars(&clientConfigCopy, envKeys)

		// Update existing client
		existingClient.Name = clientConfigCopy.Name
		existingClient.ConnectionType = string(clientConfigCopy.ConnectionType)
		existingClient.ConnectionString = clientConfigCopy.ConnectionString
		existingClient.StdioConfig = clientConfigCopy.StdioConfig
		existingClient.ToolsToExecute = clientConfigCopy.ToolsToExecute
		existingClient.Headers = clientConfigCopy.Headers

		if err := tx.WithContext(ctx).Updates(&existingClient).Error; err != nil {
			return s.parseGormError(err)
		}
		return nil
	})
}

// DeleteMCPClientConfig deletes an MCP client configuration from the database.
func (s *RDBConfigStore) DeleteMCPClientConfig(ctx context.Context, id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Find existing client
		var existingClient tables.TableMCPClient
		if err := tx.WithContext(ctx).Where("client_id = ?", id).First(&existingClient).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("MCP client with id '%s' not found", id)
			}
			return err
		}

		// Delete any virtual key MCP configs that reference this client
		if err := tx.WithContext(ctx).Where("mcp_client_id = ?", existingClient.ID).Delete(&tables.TableVirtualKeyMCPConfig{}).Error; err != nil {
			return err
		}

		// Delete the client (this will also handle foreign key cascades)
		return tx.WithContext(ctx).Delete(&existingClient).Error
	})
}

// GetVectorStoreConfig retrieves the vector store configuration from the database.
func (s *RDBConfigStore) GetVectorStoreConfig(ctx context.Context) (*vectorstore.Config, error) {
	var vectorStoreTableConfig tables.TableVectorStoreConfig
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
		if err := tx.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&tables.TableVectorStoreConfig{}).Error; err != nil {
			return err
		}
		jsonConfig, err := marshalToStringPtr(config.Config)
		if err != nil {
			return err
		}
		var record = &tables.TableVectorStoreConfig{
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
	var dbConfig tables.TableLogStoreConfig
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
		if err := tx.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&tables.TableLogStoreConfig{}).Error; err != nil {
			return err
		}
		jsonConfig, err := marshalToStringPtr(config)
		if err != nil {
			return err
		}
		var record = &tables.TableLogStoreConfig{
			Enabled: config.Enabled,
			Type:    string(config.Type),
			Config:  jsonConfig,
		}
		return tx.WithContext(ctx).Create(record).Error
	})
}

// GetEnvKeys retrieves the environment keys from the database.
func (s *RDBConfigStore) GetEnvKeys(ctx context.Context) (map[string][]EnvKeyInfo, error) {
	var dbEnvKeys []tables.TableEnvKey
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
func (s *RDBConfigStore) UpdateEnvKeys(ctx context.Context, keys map[string][]EnvKeyInfo, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	// Delete existing env keys
	if err := txDB.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&tables.TableEnvKey{}).Error; err != nil {
		return err
	}
	var dbEnvKeys []tables.TableEnvKey
	for envVar, infos := range keys {
		for _, info := range infos {
			dbEnvKey := tables.TableEnvKey{
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
		if err := txDB.WithContext(ctx).CreateInBatches(dbEnvKeys, 100).Error; err != nil {
			return err
		}
	}
	return nil
}

// GetConfig retrieves a specific config from the database.
func (s *RDBConfigStore) GetConfig(ctx context.Context, key string) (*tables.TableGovernanceConfig, error) {
	var config tables.TableGovernanceConfig
	if err := s.db.WithContext(ctx).First(&config, "key = ?", key).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &config, nil
}

// UpdateConfig updates a specific config in the database.
func (s *RDBConfigStore) UpdateConfig(ctx context.Context, config *tables.TableGovernanceConfig, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Save(config).Error
}

// GetModelPrices retrieves all model pricing records from the database.
func (s *RDBConfigStore) GetModelPrices(ctx context.Context) ([]tables.TableModelPricing, error) {
	var modelPrices []tables.TableModelPricing
	if err := s.db.WithContext(ctx).Find(&modelPrices).Error; err != nil {
		return nil, err
	}
	return modelPrices, nil
}

// CreateModelPrices creates a new model pricing record in the database.
func (s *RDBConfigStore) CreateModelPrices(ctx context.Context, pricing *tables.TableModelPricing, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	if err := txDB.WithContext(ctx).Create(pricing).Error; err != nil {
		return s.parseGormError(err)
	}
	return nil
}

// DeleteModelPrices deletes all model pricing records from the database.
func (s *RDBConfigStore) DeleteModelPrices(ctx context.Context, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&tables.TableModelPricing{}).Error
}

// PLUGINS METHODS

func (s *RDBConfigStore) GetPlugins(ctx context.Context) ([]*tables.TablePlugin, error) {
	var plugins []*tables.TablePlugin
	if err := s.db.WithContext(ctx).Find(&plugins).Error; err != nil {
		return nil, err
	}
	return plugins, nil
}

func (s *RDBConfigStore) GetPlugin(ctx context.Context, name string) (*tables.TablePlugin, error) {
	var plugin tables.TablePlugin
	if err := s.db.WithContext(ctx).First(&plugin, "name = ?", name).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &plugin, nil
}

// CreatePlugin creates a new plugin in the database.
func (s *RDBConfigStore) CreatePlugin(ctx context.Context, plugin *tables.TablePlugin, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	// Mark plugin as custom if path is not empty
	if plugin.Path != nil && strings.TrimSpace(*plugin.Path) != "" {
		plugin.IsCustom = true
	} else {
		plugin.IsCustom = false
	}
	if err := txDB.WithContext(ctx).Create(plugin).Error; err != nil {
		return s.parseGormError(err)
	}
	return nil
}

// UpsertPlugin creates a new plugin in the database if it doesn't exist, otherwise updates it.
func (s *RDBConfigStore) UpsertPlugin(ctx context.Context, plugin *tables.TablePlugin, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	// Mark plugin as custom if path is not empty
	if plugin.Path != nil && strings.TrimSpace(*plugin.Path) != "" {
		plugin.IsCustom = true
	} else {
		plugin.IsCustom = false
	}
	// Check if plugin exists and compare versions
	// If the plugin exists and the version is lower, do nothing
	var existing tables.TablePlugin
	err := txDB.WithContext(ctx).Where("name = ?", plugin.Name).First(&existing).Error
	if err == nil {
		// Plugin exists, check version
		if plugin.Version < existing.Version {
			return nil
		}
	}
	// Upsert plugin (create or update if exists based on unique name)
	if err := txDB.WithContext(ctx).Clauses(
		clause.OnConflict{
			Columns:   []clause.Column{{Name: "name"}},
			UpdateAll: true,
		},
	).Create(plugin).Error; err != nil {
		return s.parseGormError(err)
	}
	return nil
}

// UpdatePlugin updates an existing plugin in the database.
func (s *RDBConfigStore) UpdatePlugin(ctx context.Context, plugin *tables.TablePlugin, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	var localTx bool

	if len(tx) > 0 {
		txDB = tx[0]
		localTx = false
	} else {
		txDB = s.db.Begin()
		localTx = true
	}

	// Mark plugin as custom if path is not empty
	if plugin.Path != nil && strings.TrimSpace(*plugin.Path) != "" {
		plugin.IsCustom = true
	} else {
		plugin.IsCustom = false
	}

	if err := txDB.WithContext(ctx).Delete(&tables.TablePlugin{}, "name = ?", plugin.Name).Error; err != nil {
		if localTx {
			txDB.Rollback()
		}
		return err
	}

	if err := txDB.WithContext(ctx).Create(plugin).Error; err != nil {
		if localTx {
			txDB.Rollback()
		}
		return s.parseGormError(err)
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
	return txDB.WithContext(ctx).Delete(&tables.TablePlugin{}, "name = ?", name).Error
}

// GOVERNANCE METHODS

func (s *RDBConfigStore) GetRedactedVirtualKeys(ctx context.Context, ids []string) ([]tables.TableVirtualKey, error) {
	var virtualKeys []tables.TableVirtualKey

	if len(ids) > 0 {
		err := s.db.WithContext(ctx).Select("id, name, description, is_active").Where("id IN ?", ids).Find(&virtualKeys).Error
		if err != nil {
			return nil, err
		}
	} else {
		err := s.db.WithContext(ctx).Select("id, name, description, is_active").Find(&virtualKeys).Error
		if err != nil {
			return nil, err
		}
	}
	return virtualKeys, nil
}

// GetVirtualKeys retrieves all virtual keys from the database.
func (s *RDBConfigStore) GetVirtualKeys(ctx context.Context) ([]tables.TableVirtualKey, error) {
	var virtualKeys []tables.TableVirtualKey

	// Preload all relationships for complete information
	if err := s.db.WithContext(ctx).
		Preload("Team").
		Preload("Team.Customer").
		Preload("Customer").
		Preload("Budget").
		Preload("RateLimit").
		Preload("ProviderConfigs").
		Preload("ProviderConfigs.Budget").
		Preload("ProviderConfigs.RateLimit").
		Preload("ProviderConfigs.Keys", func(db *gorm.DB) *gorm.DB {
			return db.Select("id, name, key_id, models_json, provider")
		}).
		Preload("MCPConfigs").
		Preload("MCPConfigs.MCPClient").
		Find(&virtualKeys).Error; err != nil {
		return nil, err
	}

	return virtualKeys, nil
}

// GetVirtualKey retrieves a virtual key from the database.
func (s *RDBConfigStore) GetVirtualKey(ctx context.Context, id string) (*tables.TableVirtualKey, error) {
	var virtualKey tables.TableVirtualKey
	if err := s.db.WithContext(ctx).
		Preload("Team").
		Preload("Team.Customer").
		Preload("Customer").
		Preload("Budget").
		Preload("RateLimit").
		Preload("ProviderConfigs").
		Preload("ProviderConfigs.Budget").
		Preload("ProviderConfigs.RateLimit").
		Preload("ProviderConfigs.Keys", func(db *gorm.DB) *gorm.DB {
			return db.Select("id, name, key_id, models_json, provider")
		}).
		Preload("MCPConfigs").
		Preload("MCPConfigs.MCPClient").
		First(&virtualKey, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &virtualKey, nil
}

// GetVirtualKeyByValue retrieves a virtual key by its value
func (s *RDBConfigStore) GetVirtualKeyByValue(ctx context.Context, value string) (*tables.TableVirtualKey, error) {
	var virtualKey tables.TableVirtualKey
	if err := s.db.WithContext(ctx).
		Preload("Team").
		Preload("Team.Customer").
		Preload("Customer").
		Preload("Budget").
		Preload("RateLimit").
		Preload("ProviderConfigs").
		Preload("ProviderConfigs.Budget").
		Preload("ProviderConfigs.RateLimit").
		Preload("ProviderConfigs.Keys", func(db *gorm.DB) *gorm.DB {
			return db.Select("id, name, key_id, models_json, provider")
		}).
		Preload("MCPConfigs").
		Preload("MCPConfigs.MCPClient").
		First(&virtualKey, "value = ?", value).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &virtualKey, nil
}

func (s *RDBConfigStore) CreateVirtualKey(ctx context.Context, virtualKey *tables.TableVirtualKey, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	// Create virtual key
	if err := txDB.WithContext(ctx).Create(virtualKey).Error; err != nil {
		return s.parseGormError(err)
	}
	return nil
}

func (s *RDBConfigStore) UpdateVirtualKey(ctx context.Context, virtualKey *tables.TableVirtualKey, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}

	// Update virtual key
	// Use Select() to explicitly update all fields, including nil pointer fields
	// This ensures TeamID gets set to NULL when switching from team to customer association
	if err := txDB.WithContext(ctx).Select("name", "description", "value", "is_active", "team_id", "customer_id", "budget_id", "rate_limit_id", "config_hash", "updated_at").Updates(virtualKey).Error; err != nil {
		return s.parseGormError(err)
	}
	return nil
}

// GetKeysByIDs retrieves multiple keys by their IDs
func (s *RDBConfigStore) GetKeysByIDs(ctx context.Context, ids []string) ([]tables.TableKey, error) {
	if len(ids) == 0 {
		return []tables.TableKey{}, nil
	}
	var keys []tables.TableKey
	if err := s.db.WithContext(ctx).Where("key_id IN ?", ids).Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

// GetKeysByProvider retrieves all keys for a specific provider
func (s *RDBConfigStore) GetKeysByProvider(ctx context.Context, provider string) ([]tables.TableKey, error) {
	var keys []tables.TableKey
	if err := s.db.WithContext(ctx).Where("provider = ?", provider).Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

// GetAllRedactedKeys retrieves all redacted keys from the database.
func (s *RDBConfigStore) GetAllRedactedKeys(ctx context.Context, ids []string) ([]schemas.Key, error) {
	var keys []tables.TableKey
	if len(ids) > 0 {
		err := s.db.WithContext(ctx).Select("id, key_id, name, models_json, weight").Where("key_id IN ?", ids).Find(&keys).Error
		if err != nil {
			return nil, err
		}
	} else {
		err := s.db.WithContext(ctx).Select("id, key_id, name, models_json, weight").Find(&keys).Error
		if err != nil {
			return nil, err
		}
	}
	redactedKeys := make([]schemas.Key, len(keys))
	for i, key := range keys {
		models := key.Models
		if models == nil {
			models = []string{} // Ensure models is never nil in JSON response
		}
		redactedKeys[i] = schemas.Key{
			ID:     key.KeyID,
			Name:   key.Name,
			Models: models,
			Weight: key.Weight,
		}
	}
	return redactedKeys, nil
}

// DeleteVirtualKey deletes a virtual key from the database.
func (s *RDBConfigStore) DeleteVirtualKey(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Delete(&tables.TableVirtualKey{}, "id = ?", id).Error
}

// GetVirtualKeyProviderConfigs retrieves all virtual key provider configs from the database.
func (s *RDBConfigStore) GetVirtualKeyProviderConfigs(ctx context.Context, virtualKeyID string) ([]tables.TableVirtualKeyProviderConfig, error) {
	var virtualKey tables.TableVirtualKey
	if err := s.db.WithContext(ctx).First(&virtualKey, "id = ?", virtualKeyID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return []tables.TableVirtualKeyProviderConfig{}, nil
		}
		return nil, err
	}
	if virtualKey.ID == "" {
		return nil, nil
	}
	var providerConfigs []tables.TableVirtualKeyProviderConfig
	if err := s.db.WithContext(ctx).Where("virtual_key_id = ?", virtualKey.ID).Find(&providerConfigs).Error; err != nil {
		return nil, err
	}
	return providerConfigs, nil
}

// CreateVirtualKeyProviderConfig creates a new virtual key provider config in the database.
func (s *RDBConfigStore) CreateVirtualKeyProviderConfig(ctx context.Context, virtualKeyProviderConfig *tables.TableVirtualKeyProviderConfig, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	// Store keys before create
	keysToAssociate := virtualKeyProviderConfig.Keys

	if err := txDB.WithContext(ctx).Create(virtualKeyProviderConfig).Error; err != nil {
		return s.parseGormError(err)
	}

	// Associate keys after the provider config has an ID
	if len(keysToAssociate) > 0 {
		if err := txDB.WithContext(ctx).Model(virtualKeyProviderConfig).Association("Keys").Append(keysToAssociate); err != nil {
			return err
		}
	}
	return nil
}

// UpdateVirtualKeyProviderConfig updates a virtual key provider config in the database.
func (s *RDBConfigStore) UpdateVirtualKeyProviderConfig(ctx context.Context, virtualKeyProviderConfig *tables.TableVirtualKeyProviderConfig, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}

	// Store keys before save
	keysToAssociate := virtualKeyProviderConfig.Keys

	if err := txDB.WithContext(ctx).Save(virtualKeyProviderConfig).Error; err != nil {
		return s.parseGormError(err)
	}

	// Clear existing key associations and set new ones
	if err := txDB.WithContext(ctx).Model(virtualKeyProviderConfig).Association("Keys").Clear(); err != nil {
		return err
	}
	if len(keysToAssociate) > 0 {
		if err := txDB.WithContext(ctx).Model(virtualKeyProviderConfig).Association("Keys").Append(keysToAssociate); err != nil {
			return err
		}
	}
	return nil
}

// DeleteVirtualKeyProviderConfig deletes a virtual key provider config from the database.
func (s *RDBConfigStore) DeleteVirtualKeyProviderConfig(ctx context.Context, id uint, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Delete(&tables.TableVirtualKeyProviderConfig{}, "id = ?", id).Error
}

// GetVirtualKeyMCPConfigs retrieves all virtual key MCP configs from the database.
func (s *RDBConfigStore) GetVirtualKeyMCPConfigs(ctx context.Context, virtualKeyID string) ([]tables.TableVirtualKeyMCPConfig, error) {
	var virtualKey tables.TableVirtualKey
	if err := s.db.WithContext(ctx).First(&virtualKey, "id = ?", virtualKeyID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return []tables.TableVirtualKeyMCPConfig{}, nil
		}
		return nil, err
	}
	if virtualKey.ID == "" {
		return nil, nil
	}
	var mcpConfigs []tables.TableVirtualKeyMCPConfig
	if err := s.db.WithContext(ctx).Where("virtual_key_id = ?", virtualKey.ID).Find(&mcpConfigs).Error; err != nil {
		return nil, err
	}
	return mcpConfigs, nil
}

// CreateVirtualKeyMCPConfig creates a new virtual key MCP config in the database.
func (s *RDBConfigStore) CreateVirtualKeyMCPConfig(ctx context.Context, virtualKeyMCPConfig *tables.TableVirtualKeyMCPConfig, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	if err := txDB.WithContext(ctx).Create(virtualKeyMCPConfig).Error; err != nil {
		return s.parseGormError(err)
	}
	return nil
}

// UpdateVirtualKeyMCPConfig updates a virtual key provider config in the database.
func (s *RDBConfigStore) UpdateVirtualKeyMCPConfig(ctx context.Context, virtualKeyMCPConfig *tables.TableVirtualKeyMCPConfig, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	if err := txDB.WithContext(ctx).Save(virtualKeyMCPConfig).Error; err != nil {
		return s.parseGormError(err)
	}
	return nil
}

// DeleteVirtualKeyMCPConfig deletes a virtual key provider config from the database.
func (s *RDBConfigStore) DeleteVirtualKeyMCPConfig(ctx context.Context, id uint, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	return txDB.WithContext(ctx).Delete(&tables.TableVirtualKeyMCPConfig{}, "id = ?", id).Error
}

// GetTeams retrieves all teams from the database.
func (s *RDBConfigStore) GetTeams(ctx context.Context, customerID string) ([]tables.TableTeam, error) {
	// Preload relationships for complete information
	query := s.db.WithContext(ctx).Preload("Customer").Preload("Budget")
	// Optional filtering by customer
	if customerID != "" {
		query = query.Where("customer_id = ?", customerID)
	}
	var teams []tables.TableTeam
	if err := query.Find(&teams).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return teams, nil
}

// GetTeam retrieves a specific team from the database.
func (s *RDBConfigStore) GetTeam(ctx context.Context, id string) (*tables.TableTeam, error) {
	var team tables.TableTeam
	if err := s.db.WithContext(ctx).Preload("Customer").Preload("Budget").First(&team, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &team, nil
}

// CreateTeam creates a new team in the database.
func (s *RDBConfigStore) CreateTeam(ctx context.Context, team *tables.TableTeam, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	if err := txDB.WithContext(ctx).Create(team).Error; err != nil {
		return s.parseGormError(err)
	}
	return nil
}

// UpdateTeam updates an existing team in the database.
func (s *RDBConfigStore) UpdateTeam(ctx context.Context, team *tables.TableTeam, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	if err := txDB.WithContext(ctx).Save(team).Error; err != nil {
		return s.parseGormError(err)
	}
	return nil
}

// DeleteTeam deletes a team from the database.
func (s *RDBConfigStore) DeleteTeam(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Delete(&tables.TableTeam{}, "id = ?", id).Error
}

// GetCustomers retrieves all customers from the database.
func (s *RDBConfigStore) GetCustomers(ctx context.Context) ([]tables.TableCustomer, error) {
	var customers []tables.TableCustomer
	if err := s.db.WithContext(ctx).Preload("Teams").Preload("Budget").Find(&customers).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return customers, nil
}

// GetCustomer retrieves a specific customer from the database.
func (s *RDBConfigStore) GetCustomer(ctx context.Context, id string) (*tables.TableCustomer, error) {
	var customer tables.TableCustomer
	if err := s.db.WithContext(ctx).Preload("Teams").Preload("Budget").First(&customer, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &customer, nil
}

// CreateCustomer creates a new customer in the database.
func (s *RDBConfigStore) CreateCustomer(ctx context.Context, customer *tables.TableCustomer, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	if err := txDB.WithContext(ctx).Create(customer).Error; err != nil {
		return s.parseGormError(err)
	}
	return nil
}

// UpdateCustomer updates an existing customer in the database.
func (s *RDBConfigStore) UpdateCustomer(ctx context.Context, customer *tables.TableCustomer, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	if err := txDB.WithContext(ctx).Save(customer).Error; err != nil {
		return s.parseGormError(err)
	}
	return nil
}

// DeleteCustomer deletes a customer from the database.
func (s *RDBConfigStore) DeleteCustomer(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Delete(&tables.TableCustomer{}, "id = ?", id).Error
}

// GetRateLimit retrieves a specific rate limit from the database.
func (s *RDBConfigStore) GetRateLimit(ctx context.Context, id string) (*tables.TableRateLimit, error) {
	var rateLimit tables.TableRateLimit
	if err := s.db.WithContext(ctx).First(&rateLimit, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &rateLimit, nil
}

// CreateRateLimit creates a new rate limit in the database.
func (s *RDBConfigStore) CreateRateLimit(ctx context.Context, rateLimit *tables.TableRateLimit, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	if err := txDB.WithContext(ctx).Create(rateLimit).Error; err != nil {
		return s.parseGormError(err)
	}
	return nil
}

// UpdateRateLimit updates a rate limit in the database.
func (s *RDBConfigStore) UpdateRateLimit(ctx context.Context, rateLimit *tables.TableRateLimit, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	if err := txDB.WithContext(ctx).Save(rateLimit).Error; err != nil {
		return s.parseGormError(err)
	}
	return nil
}

// UpdateRateLimits updates multiple rate limits in the database.
func (s *RDBConfigStore) UpdateRateLimits(ctx context.Context, rateLimits []*tables.TableRateLimit, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	for _, rl := range rateLimits {
		if err := txDB.WithContext(ctx).Save(rl).Error; err != nil {
			return s.parseGormError(err)
		}
	}
	return nil
}

// GetBudgets retrieves all budgets from the database.
func (s *RDBConfigStore) GetBudgets(ctx context.Context) ([]tables.TableBudget, error) {
	var budgets []tables.TableBudget
	if err := s.db.WithContext(ctx).Find(&budgets).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return budgets, nil
}

// GetBudget retrieves a specific budget from the database.
func (s *RDBConfigStore) GetBudget(ctx context.Context, id string, tx ...*gorm.DB) (*tables.TableBudget, error) {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	var budget tables.TableBudget
	if err := txDB.WithContext(ctx).First(&budget, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &budget, nil
}

// CreateBudget creates a new budget in the database.
func (s *RDBConfigStore) CreateBudget(ctx context.Context, budget *tables.TableBudget, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	if err := txDB.WithContext(ctx).Create(budget).Error; err != nil {
		return s.parseGormError(err)
	}
	return nil
}

// UpdateBudgets updates multiple budgets in the database.
func (s *RDBConfigStore) UpdateBudgets(ctx context.Context, budgets []*tables.TableBudget, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	for _, b := range budgets {
		if err := txDB.WithContext(ctx).Save(b).Error; err != nil {
			return s.parseGormError(err)
		}
	}
	return nil
}

// UpdateBudget updates a budget in the database.
func (s *RDBConfigStore) UpdateBudget(ctx context.Context, budget *tables.TableBudget, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	if err := txDB.WithContext(ctx).Save(budget).Error; err != nil {
		return s.parseGormError(err)
	}
	return nil
}

// GetGovernanceConfig retrieves the governance configuration from the database.
func (s *RDBConfigStore) GetGovernanceConfig(ctx context.Context) (*GovernanceConfig, error) {
	var virtualKeys []tables.TableVirtualKey
	var teams []tables.TableTeam
	var customers []tables.TableCustomer
	var budgets []tables.TableBudget
	var rateLimits []tables.TableRateLimit
	var governanceConfigs []tables.TableGovernanceConfig

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
	// Fetching governance config for username and password
	if err := s.db.WithContext(ctx).Find(&governanceConfigs).Error; err != nil {
		return nil, err
	}
	// Check if any config is present
	if len(virtualKeys) == 0 && len(teams) == 0 && len(customers) == 0 && len(budgets) == 0 && len(rateLimits) == 0 && len(governanceConfigs) == 0 {
		return nil, nil
	}
	var authConfig *AuthConfig
	if len(governanceConfigs) > 0 {
		// Checking if username and password is present
		var username *string
		var password *string
		var isEnabled bool
		for _, entry := range governanceConfigs {
			switch entry.Key {
			case tables.ConfigAdminUsernameKey:
				username = bifrost.Ptr(entry.Value)
			case tables.ConfigAdminPasswordKey:
				password = bifrost.Ptr(entry.Value)
			case tables.ConfigIsAuthEnabledKey:
				isEnabled = entry.Value == "true"
			}
		}
		if username != nil && password != nil {
			authConfig = &AuthConfig{
				AdminUserName: *username,
				AdminPassword: *password,
				IsEnabled:     isEnabled,
			}
		}
	}
	return &GovernanceConfig{
		VirtualKeys: virtualKeys,
		Teams:       teams,
		Customers:   customers,
		Budgets:     budgets,
		RateLimits:  rateLimits,
		AuthConfig:  authConfig,
	}, nil
}

// GetAuthConfig retrieves the auth configuration from the database.
func (s *RDBConfigStore) GetAuthConfig(ctx context.Context) (*AuthConfig, error) {
	var username *string
	var password *string
	var isEnabled bool
	var disableAuthOnInference bool
	if err := s.db.WithContext(ctx).First(&tables.TableGovernanceConfig{}, "key = ?", tables.ConfigAdminUsernameKey).Select("value").Scan(&username).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	if err := s.db.WithContext(ctx).First(&tables.TableGovernanceConfig{}, "key = ?", tables.ConfigAdminPasswordKey).Select("value").Scan(&password).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}

	}
	if err := s.db.WithContext(ctx).First(&tables.TableGovernanceConfig{}, "key = ?", tables.ConfigIsAuthEnabledKey).Select("value").Scan(&isEnabled).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	if err := s.db.WithContext(ctx).First(&tables.TableGovernanceConfig{}, "key = ?", tables.ConfigDisableAuthOnInferenceKey).Select("value").Scan(&disableAuthOnInference).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	if username == nil || password == nil {
		return nil, nil
	}
	return &AuthConfig{
		AdminUserName:          *username,
		AdminPassword:          *password,
		IsEnabled:              isEnabled,
		DisableAuthOnInference: disableAuthOnInference,
	}, nil
}

// UpdateAuthConfig updates the auth configuration in the database.
func (s *RDBConfigStore) UpdateAuthConfig(ctx context.Context, config *AuthConfig) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&tables.TableGovernanceConfig{
			Key:   tables.ConfigAdminUsernameKey,
			Value: config.AdminUserName,
		}).Error; err != nil {
			return err
		}
		if err := tx.Save(&tables.TableGovernanceConfig{
			Key:   tables.ConfigAdminPasswordKey,
			Value: config.AdminPassword,
		}).Error; err != nil {
			return err
		}
		if err := tx.Save(&tables.TableGovernanceConfig{
			Key:   tables.ConfigIsAuthEnabledKey,
			Value: fmt.Sprintf("%t", config.IsEnabled),
		}).Error; err != nil {
			return err
		}
		if err := tx.Save(&tables.TableGovernanceConfig{
			Key:   tables.ConfigDisableAuthOnInferenceKey,
			Value: fmt.Sprintf("%t", config.DisableAuthOnInference),
		}).Error; err != nil {
			return err
		}
		return nil
	})
}

// GetProxyConfig retrieves the proxy configuration from the database.
func (s *RDBConfigStore) GetProxyConfig(ctx context.Context) (*tables.GlobalProxyConfig, error) {
	var configEntry tables.TableGovernanceConfig
	if err := s.db.WithContext(ctx).First(&configEntry, "key = ?", tables.ConfigProxyKey).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if configEntry.Value == "" {
		return nil, nil
	}
	var proxyConfig tables.GlobalProxyConfig
	if err := json.Unmarshal([]byte(configEntry.Value), &proxyConfig); err != nil {
		return nil, fmt.Errorf("failed to unmarshal proxy config: %w", err)
	}
	// Decrypt the password if it's not empty
	if proxyConfig.Password != "" {
		decryptedPassword, err := encrypt.Decrypt(proxyConfig.Password)
		if err != nil {
			// If decryption fails due to uninitialized key, the password might be stored in plaintext
			// (from before encryption was enabled), so we return it as-is
			if !errors.Is(err, encrypt.ErrEncryptionKeyNotInitialized) {
				return nil, fmt.Errorf("failed to decrypt proxy password: %w", err)
			}
		} else {
			proxyConfig.Password = decryptedPassword
		}
	}
	return &proxyConfig, nil
}

// UpdateProxyConfig updates the proxy configuration in the database.
func (s *RDBConfigStore) UpdateProxyConfig(ctx context.Context, config *tables.GlobalProxyConfig) error {
	// Create a copy to avoid modifying the original config
	configCopy := *config

	// Encrypt the password if it's not empty
	if configCopy.Password != "" {
		encryptedPassword, err := encrypt.Encrypt(configCopy.Password)
		if err != nil {
			return fmt.Errorf("failed to encrypt proxy password: %w", err)
		}
		configCopy.Password = encryptedPassword
	}

	configJSON, err := json.Marshal(&configCopy)
	if err != nil {
		return fmt.Errorf("failed to marshal proxy config: %w", err)
	}
	return s.db.WithContext(ctx).Save(&tables.TableGovernanceConfig{
		Key:   tables.ConfigProxyKey,
		Value: string(configJSON),
	}).Error
}

// GetSession retrieves a session from the database.
func (s *RDBConfigStore) GetSession(ctx context.Context, token string) (*tables.SessionsTable, error) {
	var session tables.SessionsTable
	if err := s.db.WithContext(ctx).First(&session, "token = ?", token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &session, nil
}

// CreateSession creates a new session in the database.
func (s *RDBConfigStore) CreateSession(ctx context.Context, session *tables.SessionsTable) error {
	return s.db.WithContext(ctx).Create(session).Error
}

// DeleteSession deletes a session from the database.
func (s *RDBConfigStore) DeleteSession(ctx context.Context, token string) error {
	return s.db.WithContext(ctx).Delete(&tables.SessionsTable{}, "token = ?", token).Error
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

// RunMigration runs a migration.
func (s *RDBConfigStore) RunMigration(ctx context.Context, migration *migrator.Migration) error {
	if migration == nil {
		return fmt.Errorf("migration cannot be nil")
	}
	m := migrator.New(s.db, migrator.DefaultOptions, []*migrator.Migration{migration})
	return m.Migrate()
}

// Close closes the SQLite config store.
func (s *RDBConfigStore) Close(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
