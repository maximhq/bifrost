package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
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

// getWeight safely dereferences a *float64 weight pointer, returning 1.0 as default if nil.
// This allows distinguishing between "not set" (nil -> 1.0) and "explicitly set to 0" (0.0).
func getWeight(w *float64) float64 {
	if w == nil {
		return 1.0
	}
	return *w
}

// UpdateClientConfig updates the client configuration in the database.
func (s *RDBConfigStore) UpdateClientConfig(ctx context.Context, config *ClientConfig) error {
	dbConfig := tables.TableClientConfig{
		DropExcessRequests:      config.DropExcessRequests,
		InitialPoolSize:         config.InitialPoolSize,
		EnableLogging:           config.EnableLogging,
		DisableContentLogging:   config.DisableContentLogging,
		DisableDBPingsInHealth:  config.DisableDBPingsInHealth,
		LogRetentionDays:        config.LogRetentionDays,
		EnableGovernance:        config.EnableGovernance,
		EnforceGovernanceHeader: config.EnforceGovernanceHeader,
		AllowDirectKeys:         config.AllowDirectKeys,
		PrometheusLabels:        config.PrometheusLabels,
		AllowedOrigins:          config.AllowedOrigins,
		AllowedHeaders:          config.AllowedHeaders,
		MaxRequestBodySizeMB:    config.MaxRequestBodySizeMB,
		EnableLiteLLMFallbacks:  config.EnableLiteLLMFallbacks,
		MCPAgentDepth:           config.MCPAgentDepth,
		MCPToolExecutionTimeout: config.MCPToolExecutionTimeout,
		MCPCodeModeBindingLevel: config.MCPCodeModeBindingLevel,
		HeaderFilterConfig:      config.HeaderFilterConfig,
		ConfigHash:              config.ConfigHash,
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
		DisableDBPingsInHealth:  dbConfig.DisableDBPingsInHealth,
		LogRetentionDays:        dbConfig.LogRetentionDays,
		EnableGovernance:        dbConfig.EnableGovernance,
		EnforceGovernanceHeader: dbConfig.EnforceGovernanceHeader,
		AllowDirectKeys:         dbConfig.AllowDirectKeys,
		AllowedOrigins:          dbConfig.AllowedOrigins,
		AllowedHeaders:          dbConfig.AllowedHeaders,
		MaxRequestBodySizeMB:    dbConfig.MaxRequestBodySizeMB,
		EnableLiteLLMFallbacks:  dbConfig.EnableLiteLLMFallbacks,
		MCPAgentDepth:           dbConfig.MCPAgentDepth,
		MCPToolExecutionTimeout: dbConfig.MCPToolExecutionTimeout,
		MCPCodeModeBindingLevel: dbConfig.MCPCodeModeBindingLevel,
		HeaderFilterConfig:      dbConfig.HeaderFilterConfig,
		ConfigHash:              dbConfig.ConfigHash,
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
			// Use existing ConfigHash if set (came from reconciliation with DB),
			// otherwise generate new hash (new key from config.json)
			keyHash := key.ConfigHash
			if keyHash == "" {
				var err error
				keyHash, err = GenerateKeyHash(key)
				if err != nil {
					return fmt.Errorf("failed to generate key hash: %w", err)
				}
			}
			dbKey := tables.TableKey{
				Provider:         dbProvider.Name,
				ProviderID:       dbProvider.ID,
				KeyID:            key.ID,
				Name:             key.Name,
				Value:            key.Value,
				Models:           key.Models,
				Weight:           &key.Weight,
				Enabled:          key.Enabled,
				UseForBatchAPI:   key.UseForBatchAPI,
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
				if key.BedrockKeyConfig.BatchS3Config != nil {
					data, err := sonic.Marshal(key.BedrockKeyConfig.BatchS3Config)
					if err != nil {
						return err
					}
					s := string(data)
					dbKey.BedrockBatchS3ConfigJSON = &s
				}
			} else {
				dbKey.BedrockBatchS3ConfigJSON = nil
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
				dbKey.Enabled = existingKey.Enabled       // Preserve the existing Enabled status
				if err := txDB.WithContext(ctx).Save(&dbKey).Error; err != nil {
					return s.parseGormError(err)
				}
			} else if errors.Is(result.Error, gorm.ErrRecordNotFound) {
				// KeyID not found, try fallback lookup by Name (handles config reload with new UUID)
				result = txDB.WithContext(ctx).Where("name = ?", dbKey.Name).First(&existingKey)
				if result.Error == nil {
					// Found by name - update existing key, preserve original KeyID
					dbKey.ID = existingKey.ID
					dbKey.KeyID = existingKey.KeyID // Preserve original KeyID
					dbKey.ProviderID = existingKey.ProviderID
					dbKey.Enabled = existingKey.Enabled
					if err := txDB.WithContext(ctx).Save(&dbKey).Error; err != nil {
						return s.parseGormError(err)
					}
				} else if errors.Is(result.Error, gorm.ErrRecordNotFound) {
					// Neither KeyID nor Name found - create new key
					if err := txDB.WithContext(ctx).Create(&dbKey).Error; err != nil {
						return s.parseGormError(err)
					}
				} else {
					// Other error occurred during name lookup
					return result.Error
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
func (s *RDBConfigStore) UpdateProvider(ctx context.Context, provider schemas.ModelProvider, config ProviderConfig, tx ...*gorm.DB) error {
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
	// Preserve ConfigHash (it has json:"-" tag so deepCopy via JSON doesn't copy it)
	configCopy.ConfigHash = config.ConfigHash
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
			Weight:           &key.Weight,
			Enabled:          key.Enabled,
			UseForBatchAPI:   key.UseForBatchAPI,
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
			if key.BedrockKeyConfig.BatchS3Config != nil {
				data, err := sonic.Marshal(key.BedrockKeyConfig.BatchS3Config)
				if err != nil {
					return err
				}
				s := string(data)
				dbKey.BedrockBatchS3ConfigJSON = &s
			} else {
				dbKey.BedrockBatchS3ConfigJSON = nil
			}
		}

		// Check if this key already exists
		if existingKey, exists := existingKeysMap[key.ID]; exists {
			// Update existing key - preserve the database ID and ConfigHash
			// ConfigHash should only be set during initial sync from config.json,
			// not when updating via UI (so DB updates aren't overwritten on restart)
			dbKey.ID = existingKey.ID
			dbKey.ConfigHash = existingKey.ConfigHash
			if err := txDB.WithContext(ctx).Save(&dbKey).Error; err != nil {
				return s.parseGormError(err)
			}
			// Remove from map to track which keys are still in use
			delete(existingKeysMap, key.ID)
		} else {
			// Create new key - ConfigHash is set from the generated hash above
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
func (s *RDBConfigStore) AddProvider(ctx context.Context, provider schemas.ModelProvider, config ProviderConfig, tx ...*gorm.DB) error {
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
	// Preserve ConfigHash (it has json:"-" tag so deepCopy via JSON doesn't copy it)
	configCopy.ConfigHash = config.ConfigHash
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
		dbKey := tables.TableKey{
			Provider:         dbProvider.Name,
			ProviderID:       dbProvider.ID,
			KeyID:            key.ID,
			Name:             key.Name,
			Value:            key.Value,
			Models:           key.Models,
			Weight:           &key.Weight,
			Enabled:          key.Enabled,
			UseForBatchAPI:   key.UseForBatchAPI,
			AzureKeyConfig:   key.AzureKeyConfig,
			VertexKeyConfig:  key.VertexKeyConfig,
			BedrockKeyConfig: key.BedrockKeyConfig,
			ConfigHash:       key.ConfigHash,
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
			if key.BedrockKeyConfig.BatchS3Config != nil {
				data, err := sonic.Marshal(key.BedrockKeyConfig.BatchS3Config)
				if err != nil {
					return err
				}
				s := string(data)
				dbKey.BedrockBatchS3ConfigJSON = &s
			} else {
				dbKey.BedrockBatchS3ConfigJSON = nil
			}
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

	// Store the budget and rate limit IDs before deleting
	budgetID := dbProvider.BudgetID
	rateLimitID := dbProvider.RateLimitID
	// Delete the provider first (keys will be deleted due to CASCADE constraint)
	if err := txDB.WithContext(ctx).Delete(&dbProvider).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}

	// Delete the budget if it exists
	if budgetID != nil {
		if err := txDB.WithContext(ctx).Delete(&tables.TableBudget{}, "id = ?", *budgetID).Error; err != nil {
			return err
		}
	}
	// Delete the rate limit if it exists
	if rateLimitID != nil {
		if err := txDB.WithContext(ctx).Delete(&tables.TableRateLimit{}, "id = ?", *rateLimitID).Error; err != nil {
			return err
		}
	}

	return nil
}

// GetProvidersConfig retrieves the provider configuration from the database.
func (s *RDBConfigStore) GetProvidersConfig(ctx context.Context) (map[schemas.ModelProvider]ProviderConfig, error) {
	var dbProviders []tables.TableProvider
	if err := s.db.WithContext(ctx).Preload("Keys").Find(&dbProviders).Error; err != nil {
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
				Name:             dbKey.Name,
				Value:            dbKey.Value,
				Models:           dbKey.Models,
				Weight:           getWeight(dbKey.Weight),
				Enabled:          dbKey.Enabled,
				UseForBatchAPI:   dbKey.UseForBatchAPI,
				AzureKeyConfig:   dbKey.AzureKeyConfig,
				VertexKeyConfig:  dbKey.VertexKeyConfig,
				BedrockKeyConfig: dbKey.BedrockKeyConfig,
				ConfigHash:       dbKey.ConfigHash,
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

// GetProviderConfig retrieves the provider configuration from the database.
func (s *RDBConfigStore) GetProviderConfig(ctx context.Context, provider schemas.ModelProvider) (*ProviderConfig, error) {
	var dbProvider tables.TableProvider
	if err := s.db.WithContext(ctx).Preload("Keys").Where("name = ?", string(provider)).First(&dbProvider).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	keys := make([]schemas.Key, len(dbProvider.Keys))
	for i, dbKey := range dbProvider.Keys {
		keys[i] = schemas.Key{
			ID:               dbKey.KeyID,
			Name:             dbKey.Name,
			Value:            dbKey.Value,
			Models:           dbKey.Models,
			Weight:           getWeight(dbKey.Weight),
			Enabled:          dbKey.Enabled,
			UseForBatchAPI:   dbKey.UseForBatchAPI,
			AzureKeyConfig:   dbKey.AzureKeyConfig,
			VertexKeyConfig:  dbKey.VertexKeyConfig,
			BedrockKeyConfig: dbKey.BedrockKeyConfig,
			ConfigHash:       dbKey.ConfigHash,
		}
	}
	return &ProviderConfig{
		Keys:                     keys,
		NetworkConfig:            dbProvider.NetworkConfig,
		ConcurrencyAndBufferSize: dbProvider.ConcurrencyAndBufferSize,
		ProxyConfig:              dbProvider.ProxyConfig,
		SendBackRawRequest:       dbProvider.SendBackRawRequest,
		SendBackRawResponse:      dbProvider.SendBackRawResponse,
		CustomProviderConfig:     dbProvider.CustomProviderConfig,
		ConfigHash:               dbProvider.ConfigHash,
	}, nil
}

// GetProviders retrieves all providers from the database with their governance relationships.
func (s *RDBConfigStore) GetProviders(ctx context.Context) ([]tables.TableProvider, error) {
	var providers []tables.TableProvider
	if err := s.db.WithContext(ctx).Preload("Budget").Preload("RateLimit").Find(&providers).Error; err != nil {
		return nil, err
	}
	return providers, nil
}

// GetProvider retrieves a provider by name from the database with governance relationships.
func (s *RDBConfigStore) GetProvider(ctx context.Context, provider schemas.ModelProvider) (*tables.TableProvider, error) {
	var providerInfo tables.TableProvider
	if err := s.db.WithContext(ctx).Preload("Budget").Preload("RateLimit").Where("name = ?", string(provider)).First(&providerInfo).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &providerInfo, nil
}

// GetProviderByName retrieves a provider by name from the database with governance relationships.
func (s *RDBConfigStore) GetProviderByName(ctx context.Context, name string) (*tables.TableProvider, error) {
	var provider tables.TableProvider
	if err := s.db.WithContext(ctx).Preload("Budget").Preload("RateLimit").Where("name = ?", name).First(&provider).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &provider, nil
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
		clientConfigs[i] = schemas.MCPClientConfig{
			ID:                 dbClient.ClientID,
			Name:               dbClient.Name,
			IsCodeModeClient:   dbClient.IsCodeModeClient,
			ConnectionType:     schemas.MCPConnectionType(dbClient.ConnectionType),
			ConnectionString:   dbClient.ConnectionString,
			StdioConfig:        dbClient.StdioConfig,
			ToolsToExecute:     dbClient.ToolsToExecute,
			ToolsToAutoExecute: dbClient.ToolsToAutoExecute,
			Headers:            dbClient.Headers,
			IsPingAvailable:    dbClient.IsPingAvailable,
		}
	}
	var clientConfig tables.TableClientConfig
	if err := s.db.WithContext(ctx).First(&clientConfig).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Return MCP config with default ToolManagerConfig if no client config exists
			// This will never happen, but just in case.
			return &schemas.MCPConfig{
				ClientConfigs: clientConfigs,
				ToolManagerConfig: &schemas.MCPToolManagerConfig{
					ToolExecutionTimeout: 30 * time.Second, // default from TableClientConfig
					MaxAgentDepth:        10,               // default from TableClientConfig
				},
			}, nil
		}
		return nil, err
	}
	toolManagerConfig := schemas.MCPToolManagerConfig{
		ToolExecutionTimeout: time.Duration(clientConfig.MCPToolExecutionTimeout) * time.Second,
		MaxAgentDepth:        clientConfig.MCPAgentDepth,
		CodeModeBindingLevel: schemas.CodeModeBindingLevel(clientConfig.MCPCodeModeBindingLevel),
	}
	return &schemas.MCPConfig{
		ClientConfigs:     clientConfigs,
		ToolManagerConfig: &toolManagerConfig,
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
func (s *RDBConfigStore) CreateMCPClientConfig(ctx context.Context, clientConfig schemas.MCPClientConfig) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Create a deep copy to avoid modifying the original
		clientConfigCopy, err := deepCopy(clientConfig)
		if err != nil {
			return err
		}
		// Create new client
		dbClient := tables.TableMCPClient{
			ClientID:           clientConfigCopy.ID,
			Name:               clientConfigCopy.Name,
			IsCodeModeClient:   clientConfigCopy.IsCodeModeClient,
			ConnectionType:     string(clientConfigCopy.ConnectionType),
			ConnectionString:   clientConfigCopy.ConnectionString,
			StdioConfig:        clientConfigCopy.StdioConfig,
			ToolsToExecute:     clientConfigCopy.ToolsToExecute,
			ToolsToAutoExecute: clientConfigCopy.ToolsToAutoExecute,
			Headers:            clientConfigCopy.Headers,
			IsPingAvailable:    clientConfigCopy.IsPingAvailable,
		}
		if err := tx.WithContext(ctx).Create(&dbClient).Error; err != nil {
			return s.parseGormError(err)
		}
		return nil
	})
}

// UpdateMCPClientConfig updates an existing MCP client configuration in the database.
func (s *RDBConfigStore) UpdateMCPClientConfig(ctx context.Context, id string, clientConfig schemas.MCPClientConfig) error {
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

		// Update existing client
		existingClient.Name = clientConfigCopy.Name
		existingClient.IsCodeModeClient = clientConfigCopy.IsCodeModeClient
		existingClient.ToolsToExecute = clientConfigCopy.ToolsToExecute
		existingClient.ToolsToAutoExecute = clientConfigCopy.ToolsToAutoExecute
		existingClient.Headers = clientConfigCopy.Headers
		existingClient.IsPingAvailable = clientConfigCopy.IsPingAvailable

		// Use Select to explicitly include IsCodeModeClient even when it's false (zero value)
		// GORM's Updates() skips zero values by default, so we need to explicitly select fields
		// Using struct field names - GORM will convert them to column names automatically
		if err := tx.WithContext(ctx).Select("name", "is_code_mode_client", "is_ping_available", "tools_to_execute_json", "tools_to_auto_execute_json", "headers_json", "updated_at").Updates(&existingClient).Error; err != nil {
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

// UpsertModelPrices creates or updates a model pricing record in the database.
func (s *RDBConfigStore) UpsertModelPrices(ctx context.Context, pricing *tables.TableModelPricing, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	// Upsert pricing (create or update if exists based on unique index: model, provider, mode)
	if err := txDB.WithContext(ctx).Clauses(
		clause.OnConflict{
			Columns:   []clause.Column{{Name: "model"}, {Name: "provider"}, {Name: "mode"}},
			UpdateAll: true,
		},
	).Create(pricing).Error; err != nil {
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

	// Check if record exists by ID or Name
	var existing tables.TableVirtualKey
	err := txDB.WithContext(ctx).
		Where("id = ? OR name = ?", virtualKey.ID, virtualKey.Name).
		First(&existing).Error

	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return s.parseGormError(err)
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Create new record
		if err := txDB.WithContext(ctx).Create(virtualKey).Error; err != nil {
			return s.parseGormError(err)
		}
	} else {
		// Update existing record (use existing.ID to ensure we update the found record)
		virtualKey.ID = existing.ID
		// Use Select() to explicitly update all fields, including nil pointer fields
		// This ensures TeamID gets set to NULL when switching from team to customer association
		if err := txDB.WithContext(ctx).
			Select("name", "description", "value", "is_active", "team_id", "customer_id", "budget_id", "rate_limit_id", "config_hash", "updated_at").
			Updates(virtualKey).Error; err != nil {
			return s.parseGormError(err)
		}
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
			Weight: getWeight(key.Weight),
		}
	}
	return redactedKeys, nil
}

// DeleteVirtualKey deletes a virtual key from the database.
func (s *RDBConfigStore) DeleteVirtualKey(ctx context.Context, id string) error {
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var virtualKey tables.TableVirtualKey
		if err := tx.WithContext(ctx).Preload("ProviderConfigs").First(&virtualKey, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}

		// Collect budget and rate limit IDs from provider configs before deletion
		var providerConfigBudgetIDs []string
		var providerConfigRateLimitIDs []string
		for _, pc := range virtualKey.ProviderConfigs {
			// Delete the keys join table entries
			if err := tx.WithContext(ctx).Exec("DELETE FROM governance_virtual_key_provider_config_keys WHERE table_virtual_key_provider_config_id = ?", pc.ID).Error; err != nil {
				return err
			}
			// Collect budget and rate limit IDs for deletion after provider config
			if pc.BudgetID != nil {
				providerConfigBudgetIDs = append(providerConfigBudgetIDs, *pc.BudgetID)
			}
			if pc.RateLimitID != nil {
				providerConfigRateLimitIDs = append(providerConfigRateLimitIDs, *pc.RateLimitID)
			}
		}

		// Delete all provider configs associated with the virtual key first
		if err := tx.WithContext(ctx).Delete(&tables.TableVirtualKeyProviderConfig{}, "virtual_key_id = ?", id).Error; err != nil {
			return err
		}
		// Now delete the collected budgets and rate limits
		for _, budgetID := range providerConfigBudgetIDs {
			if err := tx.WithContext(ctx).Delete(&tables.TableBudget{}, "id = ?", budgetID).Error; err != nil {
				return err
			}
		}
		for _, rateLimitID := range providerConfigRateLimitIDs {
			if err := tx.WithContext(ctx).Delete(&tables.TableRateLimit{}, "id = ?", rateLimitID).Error; err != nil {
				return err
			}
		}
		// Delete all MCP configs associated with the virtual key
		if err := tx.WithContext(ctx).Delete(&tables.TableVirtualKeyMCPConfig{}, "virtual_key_id = ?", id).Error; err != nil {
			return err
		}
		// Delete the budget associated with the virtual key
		budgetID := virtualKey.BudgetID
		rateLimitID := virtualKey.RateLimitID
		// Delete the virtual key
		if err := tx.WithContext(ctx).Delete(&tables.TableVirtualKey{}, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if budgetID != nil {
			if err := tx.WithContext(ctx).Delete(&tables.TableBudget{}, "id = ?", *budgetID).Error; err != nil {
				return err
			}
		}
		// Delete the rate limit associated with the virtual key
		if rateLimitID != nil {
			if err := tx.WithContext(ctx).Delete(&tables.TableRateLimit{}, "id = ?", *rateLimitID).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
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

	// Resolve keys by name/key_id if they don't have database IDs
	// This handles config file inputs that only specify name
	if len(keysToAssociate) > 0 {
		resolvedKeys := make([]tables.TableKey, 0, len(keysToAssociate))
		var unresolvedKeys []string
		for i, k := range keysToAssociate {
			// If key already has a database ID (from UI), use it directly
			if k.ID > 0 {
				resolvedKeys = append(resolvedKeys, k)
				continue
			}
			// Otherwise resolve by KeyID or Name (from config file)
			var dbKey tables.TableKey
			var resolved bool
			if k.KeyID != "" {
				if err := txDB.WithContext(ctx).Where("key_id = ?", k.KeyID).First(&dbKey).Error; err == nil {
					resolvedKeys = append(resolvedKeys, dbKey)
					resolved = true
				}
			}
			if !resolved && k.Name != "" {
				if err := txDB.WithContext(ctx).Where("name = ? AND provider = ?", k.Name, virtualKeyProviderConfig.Provider).First(&dbKey).Error; err == nil {
					resolvedKeys = append(resolvedKeys, dbKey)
					resolved = true
				}
			}
			if !resolved {
				// Collect identifier for unresolved key
				if k.KeyID != "" {
					unresolvedKeys = append(unresolvedKeys, fmt.Sprintf("key_id=%s", k.KeyID))
				} else if k.Name != "" {
					unresolvedKeys = append(unresolvedKeys, fmt.Sprintf("name=%s", k.Name))
				} else {
					unresolvedKeys = append(unresolvedKeys, fmt.Sprintf("key[%d]", i))
				}
			}
		}
		if len(unresolvedKeys) > 0 {
			return &ErrUnresolvedKeys{Identifiers: unresolvedKeys}
		}
		keysToAssociate = resolvedKeys
	}

	// Clear Keys before Create to prevent GORM from auto-associating unresolved keys (with ID=0)
	// We'll manually associate the resolved keys after Create
	virtualKeyProviderConfig.Keys = nil

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

	// Resolve keys by name/key_id if they don't have database IDs
	// This handles config file inputs that only specify name
	if len(keysToAssociate) > 0 {
		resolvedKeys := make([]tables.TableKey, 0, len(keysToAssociate))
		var unresolvedKeys []string
		for i, k := range keysToAssociate {
			// If key already has a database ID (from UI), use it directly
			if k.ID > 0 {
				resolvedKeys = append(resolvedKeys, k)
				continue
			}
			// Otherwise resolve by KeyID or Name (from config file)
			var dbKey tables.TableKey
			var resolved bool
			if k.KeyID != "" {
				if err := txDB.WithContext(ctx).Where("key_id = ?", k.KeyID).First(&dbKey).Error; err == nil {
					resolvedKeys = append(resolvedKeys, dbKey)
					resolved = true
				}
			}
			if !resolved && k.Name != "" {
				if err := txDB.WithContext(ctx).Where("name = ? AND provider = ?", k.Name, virtualKeyProviderConfig.Provider).First(&dbKey).Error; err == nil {
					resolvedKeys = append(resolvedKeys, dbKey)
					resolved = true
				}
			}
			if !resolved {
				// Collect identifier for unresolved key
				if k.KeyID != "" {
					unresolvedKeys = append(unresolvedKeys, fmt.Sprintf("key_id=%s", k.KeyID))
				} else if k.Name != "" {
					unresolvedKeys = append(unresolvedKeys, fmt.Sprintf("name=%s", k.Name))
				} else {
					unresolvedKeys = append(unresolvedKeys, fmt.Sprintf("key[%d]", i))
				}
			}
		}
		if len(unresolvedKeys) > 0 {
			return &ErrUnresolvedKeys{Identifiers: unresolvedKeys}
		}
		keysToAssociate = resolvedKeys
	}

	// Clear Keys before Save to prevent GORM from auto-associating unresolved keys (with ID=0)
	// We'll manually manage the association after Save
	virtualKeyProviderConfig.Keys = nil

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
	// First fetch the provider config to get budget and rate limit IDs
	var providerConfig tables.TableVirtualKeyProviderConfig
	if err := txDB.WithContext(ctx).First(&providerConfig, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	// Store the budget and rate limit IDs before deleting
	budgetID := providerConfig.BudgetID
	rateLimitID := providerConfig.RateLimitID
	// Delete the provider config first
	if err := txDB.WithContext(ctx).Delete(&tables.TableVirtualKeyProviderConfig{}, "id = ?", id).Error; err != nil {
		return err
	}
	// Delete the budget if it exists
	if budgetID != nil {
		if err := txDB.WithContext(ctx).Delete(&tables.TableBudget{}, "id = ?", *budgetID).Error; err != nil {
			return err
		}
	}
	// Delete the rate limit if it exists
	if rateLimitID != nil {
		if err := txDB.WithContext(ctx).Delete(&tables.TableRateLimit{}, "id = ?", *rateLimitID).Error; err != nil {
			return err
		}
	}
	return nil
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
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var team tables.TableTeam
		if err := tx.WithContext(ctx).Preload("Budget").First(&team, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		// Set team_id to null for all virtual keys associated with the team
		if err := tx.WithContext(ctx).Model(&tables.TableVirtualKey{}).Where("team_id = ?", id).Update("team_id", nil).Error; err != nil {
			return err
		}
		// Store the budget ID before deleting the team
		budgetID := team.BudgetID
		// Delete the team first
		if err := tx.WithContext(ctx).Delete(&tables.TableTeam{}, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		// Delete the team's budget if it exists
		if budgetID != nil {
			if err := tx.WithContext(ctx).Delete(&tables.TableBudget{}, "id = ?", *budgetID).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// GetCustomers retrieves all customers from the database.
func (s *RDBConfigStore) GetCustomers(ctx context.Context) ([]tables.TableCustomer, error) {
	var customers []tables.TableCustomer
	if err := s.db.WithContext(ctx).Preload("Teams").Preload("Budget").Find(&customers).Error; err != nil {
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
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var customer tables.TableCustomer
		if err := tx.WithContext(ctx).Preload("Budget").First(&customer, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		// Set customer_id to null for all virtual keys associated with the customer
		if err := tx.WithContext(ctx).Model(&tables.TableVirtualKey{}).Where("customer_id = ?", id).Update("customer_id", nil).Error; err != nil {
			return err
		}
		// Set customer_id to null for all teams associated with the customer
		if err := tx.WithContext(ctx).Model(&tables.TableTeam{}).Where("customer_id = ?", id).Update("customer_id", nil).Error; err != nil {
			return err
		}
		// Store the budget ID before deleting the customer
		budgetID := customer.BudgetID
		// Delete the customer first
		if err := tx.WithContext(ctx).Delete(&tables.TableCustomer{}, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		// Delete the customer's budget if it exists
		if budgetID != nil {
			if err := tx.WithContext(ctx).Delete(&tables.TableBudget{}, "id = ?", *budgetID).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// GetRateLimits retrieves all rate limits from the database.
func (s *RDBConfigStore) GetRateLimits(ctx context.Context) ([]tables.TableRateLimit, error) {
	var rateLimits []tables.TableRateLimit
	if err := s.db.WithContext(ctx).Find(&rateLimits).Error; err != nil {
		return nil, err
	}
	return rateLimits, nil
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

// UpdateBudgetUsage updates only the current_usage field of a budget.
// Uses SkipHooks to avoid triggering BeforeSave validation since we're only updating usage.
func (s *RDBConfigStore) UpdateBudgetUsage(ctx context.Context, id string, currentUsage float64) error {
	result := s.db.WithContext(ctx).
		Session(&gorm.Session{SkipHooks: true}).
		Model(&tables.TableBudget{}).
		Where("id = ?", id).
		Update("current_usage", currentUsage)
	if result.Error != nil {
		return s.parseGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateRateLimitUsage updates only the usage fields of a rate limit.
// Uses SkipHooks to avoid triggering BeforeSave validation since we're only updating usage.
func (s *RDBConfigStore) UpdateRateLimitUsage(ctx context.Context, id string, tokenCurrentUsage int64, requestCurrentUsage int64) error {
	result := s.db.WithContext(ctx).
		Session(&gorm.Session{SkipHooks: true}).
		Model(&tables.TableRateLimit{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"token_current_usage":   tokenCurrentUsage,
			"request_current_usage": requestCurrentUsage,
		})
	if result.Error != nil {
		return s.parseGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// GetModelConfigs retrieves all model configs from the database.
func (s *RDBConfigStore) GetModelConfigs(ctx context.Context) ([]tables.TableModelConfig, error) {
	var modelConfigs []tables.TableModelConfig
	if err := s.db.WithContext(ctx).Preload("Budget").Preload("RateLimit").Find(&modelConfigs).Error; err != nil {
		return nil, err
	}
	return modelConfigs, nil
}

// GetModelConfig retrieves a specific model config from the database by model name and optional provider.
func (s *RDBConfigStore) GetModelConfig(ctx context.Context, modelName string, provider *string) (*tables.TableModelConfig, error) {
	var modelConfig tables.TableModelConfig
	query := s.db.WithContext(ctx).Where("model_name = ?", modelName)
	if provider != nil {
		query = query.Where("provider = ?", *provider)
	} else {
		query = query.Where("provider IS NULL")
	}
	if err := query.Preload("Budget").Preload("RateLimit").First(&modelConfig).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &modelConfig, nil
}

// GetModelConfigByID retrieves a specific model config from the database by ID.
func (s *RDBConfigStore) GetModelConfigByID(ctx context.Context, id string) (*tables.TableModelConfig, error) {
	var modelConfig tables.TableModelConfig
	if err := s.db.WithContext(ctx).Preload("Budget").Preload("RateLimit").First(&modelConfig, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &modelConfig, nil
}

// CreateModelConfig creates a new model config in the database.
func (s *RDBConfigStore) CreateModelConfig(ctx context.Context, modelConfig *tables.TableModelConfig, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	if err := txDB.WithContext(ctx).Create(modelConfig).Error; err != nil {
		return s.parseGormError(err)
	}
	return nil
}

// UpdateModelConfig updates a model config in the database.
func (s *RDBConfigStore) UpdateModelConfig(ctx context.Context, modelConfig *tables.TableModelConfig, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	if err := txDB.WithContext(ctx).Save(modelConfig).Error; err != nil {
		return s.parseGormError(err)
	}
	return nil
}

// UpdateModelConfigs updates multiple model configs in the database.
func (s *RDBConfigStore) UpdateModelConfigs(ctx context.Context, modelConfigs []*tables.TableModelConfig, tx ...*gorm.DB) error {
	var txDB *gorm.DB
	if len(tx) > 0 {
		txDB = tx[0]
	} else {
		txDB = s.db
	}
	for _, mc := range modelConfigs {
		if err := txDB.WithContext(ctx).Save(mc).Error; err != nil {
			return s.parseGormError(err)
		}
	}
	return nil
}

// DeleteModelConfig deletes a model config from the database.
func (s *RDBConfigStore) DeleteModelConfig(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// First fetch the model config to get budget and rate limit IDs
		var modelConfig tables.TableModelConfig
		if err := tx.First(&modelConfig, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		// Store the budget and rate limit IDs before deleting
		budgetID := modelConfig.BudgetID
		rateLimitID := modelConfig.RateLimitID
		// Delete the model config first
		if err := tx.Delete(&tables.TableModelConfig{}, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return s.parseGormError(err)
		}
		// Delete the budget if it exists
		if budgetID != nil {
			if err := tx.Delete(&tables.TableBudget{}, "id = ?", *budgetID).Error; err != nil {
				return err
			}
		}
		// Delete the rate limit if it exists
		if rateLimitID != nil {
			if err := tx.Delete(&tables.TableRateLimit{}, "id = ?", *rateLimitID).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// GetGovernanceConfig retrieves the governance configuration from the database.
func (s *RDBConfigStore) GetGovernanceConfig(ctx context.Context) (*GovernanceConfig, error) {
	var virtualKeys []tables.TableVirtualKey
	var teams []tables.TableTeam
	var customers []tables.TableCustomer
	var budgets []tables.TableBudget
	var rateLimits []tables.TableRateLimit
	var modelConfigs []tables.TableModelConfig
	var providers []tables.TableProvider
	var governanceConfigs []tables.TableGovernanceConfig

	if err := s.db.WithContext(ctx).
		Preload("ProviderConfigs").
		Preload("ProviderConfigs.Keys", func(db *gorm.DB) *gorm.DB {
			return db.Select("id, name, key_id, models_json, provider")
		}).
		Find(&virtualKeys).Error; err != nil {
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
	if err := s.db.WithContext(ctx).Find(&modelConfigs).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Find(&providers).Error; err != nil {
		return nil, err
	}
	// Fetching governance config for username and password
	if err := s.db.WithContext(ctx).Find(&governanceConfigs).Error; err != nil {
		return nil, err
	}
	// Check if any config is present
	if len(virtualKeys) == 0 && len(teams) == 0 && len(customers) == 0 && len(budgets) == 0 && len(rateLimits) == 0 && len(modelConfigs) == 0 && len(providers) == 0 && len(governanceConfigs) == 0 {
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
		VirtualKeys:  virtualKeys,
		Teams:        teams,
		Customers:    customers,
		Budgets:      budgets,
		RateLimits:   rateLimits,
		ModelConfigs: modelConfigs,
		Providers:    providers,
		AuthConfig:   authConfig,
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

// GetRestartRequiredConfig retrieves the restart required configuration from the database.
func (s *RDBConfigStore) GetRestartRequiredConfig(ctx context.Context) (*tables.RestartRequiredConfig, error) {
	var configEntry tables.TableGovernanceConfig
	if err := s.db.WithContext(ctx).First(&configEntry, "key = ?", tables.ConfigRestartRequiredKey).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if configEntry.Value == "" {
		return nil, nil
	}
	var restartConfig tables.RestartRequiredConfig
	if err := json.Unmarshal([]byte(configEntry.Value), &restartConfig); err != nil {
		return nil, fmt.Errorf("failed to unmarshal restart required config: %w", err)
	}
	return &restartConfig, nil
}

// SetRestartRequiredConfig sets the restart required configuration in the database.
func (s *RDBConfigStore) SetRestartRequiredConfig(ctx context.Context, config *tables.RestartRequiredConfig) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal restart required config: %w", err)
	}
	return s.db.WithContext(ctx).Save(&tables.TableGovernanceConfig{
		Key:   tables.ConfigRestartRequiredKey,
		Value: string(configJSON),
	}).Error
}

// ClearRestartRequiredConfig clears the restart required configuration in the database.
func (s *RDBConfigStore) ClearRestartRequiredConfig(ctx context.Context) error {
	return s.db.WithContext(ctx).Save(&tables.TableGovernanceConfig{
		Key:   tables.ConfigRestartRequiredKey,
		Value: `{"required":false,"reason":""}`,
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

// FlushSessions flushes all sessions from the database.
func (s *RDBConfigStore) FlushSessions(ctx context.Context) error {
	return s.db.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&tables.SessionsTable{}).Error
}

// ExecuteTransaction executes a transaction.
func (s *RDBConfigStore) ExecuteTransaction(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return s.db.WithContext(ctx).Transaction(fn)
}

// RetryOnNotFound retries a function up to 3 times with 1-second delays if it returns ErrNotFound
func (s *RDBConfigStore) RetryOnNotFound(ctx context.Context, fn func(ctx context.Context) (any, error), maxRetries int, retryDelay time.Duration) (any, error) {
	var lastErr error
	for attempt := range maxRetries {
		result, err := fn(ctx)
		if err == nil {
			return result, nil
		}
		if !errors.Is(err, ErrNotFound) && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}

		lastErr = err

		// Don't wait after the last attempt
		if attempt < maxRetries-1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryDelay):
				// Continue to next retry
			}
		}
	}
	return nil, lastErr
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

// TryAcquireLock attempts to insert a lock row. Returns true if the lock was acquired.
// Uses INSERT ... ON CONFLICT DO NOTHING for atomic lock acquisition.
func (s *RDBConfigStore) TryAcquireLock(ctx context.Context, lock *tables.TableDistributedLock) (bool, error) {
	// Set CreatedAt if not already set
	if lock.CreatedAt.IsZero() {
		lock.CreatedAt = time.Now().UTC()
	}

	// Use GORM clause-based insert for dialect-appropriate SQL
	result := s.db.WithContext(ctx).Clauses(
		clause.OnConflict{
			Columns:   []clause.Column{{Name: "lock_key"}},
			DoNothing: true,
		},
	).Create(lock)

	if result.Error != nil {
		return false, fmt.Errorf("failed to acquire lock: %w", result.Error)
	}

	// If RowsAffected is 1, the lock was acquired
	return result.RowsAffected == 1, nil
}

// GetLock retrieves a lock by its key. Returns nil if the lock doesn't exist.
func (s *RDBConfigStore) GetLock(ctx context.Context, lockKey string) (*tables.TableDistributedLock, error) {
	var lock tables.TableDistributedLock
	result := s.db.WithContext(ctx).Where("lock_key = ?", lockKey).First(&lock)

	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get lock: %w", result.Error)
	}

	return &lock, nil
}

// UpdateLockExpiry updates the expiration time for an existing lock.
// Only succeeds if the holder ID matches the current lock holder.
func (s *RDBConfigStore) UpdateLockExpiry(ctx context.Context, lockKey, holderID string, expiresAt time.Time) error {
	result := s.db.WithContext(ctx).Model(&tables.TableDistributedLock{}).
		Where("lock_key = ? AND holder_id = ? AND expires_at > ?", lockKey, holderID, time.Now().UTC()).
		Update("expires_at", expiresAt)

	if result.Error != nil {
		return fmt.Errorf("failed to update lock expiry: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return ErrLockNotHeld
	}

	return nil
}

// ReleaseLock deletes a lock if the holder ID matches.
// Returns true if the lock was released, false if it wasn't held by the given holder.
func (s *RDBConfigStore) ReleaseLock(ctx context.Context, lockKey, holderID string) (bool, error) {
	result := s.db.WithContext(ctx).
		Where("lock_key = ? AND holder_id = ?", lockKey, holderID).
		Delete(&tables.TableDistributedLock{})

	if result.Error != nil {
		return false, fmt.Errorf("failed to release lock: %w", result.Error)
	}

	return result.RowsAffected > 0, nil
}

// CleanupExpiredLocks removes all locks that have expired.
// Returns the number of locks cleaned up.
func (s *RDBConfigStore) CleanupExpiredLocks(ctx context.Context) (int64, error) {
	result := s.db.WithContext(ctx).
		Where("expires_at < ?", time.Now().UTC()).
		Delete(&tables.TableDistributedLock{})

	if result.Error != nil {
		return 0, fmt.Errorf("failed to cleanup expired locks: %w", result.Error)
	}

	return result.RowsAffected, nil
}

// CleanupExpiredLockByKey atomically deletes a specific lock only if it has expired.
// Returns true if an expired lock was deleted, false if the lock doesn't exist or hasn't expired.
func (s *RDBConfigStore) CleanupExpiredLockByKey(ctx context.Context, lockKey string) (bool, error) {
	result := s.db.WithContext(ctx).
		Where("lock_key = ? AND expires_at < ?", lockKey, time.Now().UTC()).
		Delete(&tables.TableDistributedLock{})

	if result.Error != nil {
		return false, fmt.Errorf("failed to cleanup expired lock: %w", result.Error)
	}

	return result.RowsAffected > 0, nil
}
