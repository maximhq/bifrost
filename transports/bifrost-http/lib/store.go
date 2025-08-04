// Package lib provides core functionality for the Bifrost HTTP service,
// including context propagation, header management, and integration with monitoring systems.
package lib

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/meta"
	"gorm.io/gorm"
)

// ConfigStore represents a high-performance in-memory configuration store for Bifrost.
// It provides thread-safe access to provider configurations with database persistence.
//
// Features:
//   - Pure in-memory storage for ultra-fast access
//   - Environment variable processing for API keys and key-level configurations
//   - Thread-safe operations with read-write mutexes
//   - Real-time configuration updates via HTTP API
//   - Automatic database persistence for all changes
//   - Support for provider-specific key configurations (Azure, Vertex) and meta configurations (Bedrock)
type ConfigStore struct {
	mu         sync.RWMutex
	muMCP      sync.RWMutex
	logger     schemas.Logger
	db         *gorm.DB // GORM database connection
	client     *bifrost.Bifrost
	configPath string // Path to the config file

	// In-memory storage
	ClientConfig ClientConfig
	Providers    map[schemas.ModelProvider]ProviderConfig
	MCPConfig    *schemas.MCPConfig

	// Track which keys come from environment variables
	EnvKeys map[string][]EnvKeyInfo
}

// EnvKeyInfo stores information about a key sourced from environment
type EnvKeyInfo struct {
	EnvVar     string // The environment variable name (without env. prefix)
	Provider   string // The provider this key belongs to (empty for core/mcp configs)
	KeyType    string // Type of key (e.g., "api_key", "azure_config", "vertex_config", "meta_config", "connection_string")
	ConfigPath string // Path in config where this env var is used
	KeyID      string // The key ID this env var belongs to (empty for non-key configs like meta_config, connection_string)
}

var DefaultClientConfig = ClientConfig{
	DropExcessRequests:      false,
	PrometheusLabels:        []string{},
	InitialPoolSize:         300,
	EnableLogging:           true,
	EnableGovernance:        true,
	EnforceGovernanceHeader: false,
}

// NewConfigStore creates a new in-memory configuration store instance with database connection.
func NewConfigStore(logger schemas.Logger, db *gorm.DB, configPath string) (*ConfigStore, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection cannot be nil")
	}

	store := &ConfigStore{
		logger:     logger,
		db:         db,
		configPath: configPath,
		Providers:  make(map[schemas.ModelProvider]ProviderConfig),
		EnvKeys:    make(map[string][]EnvKeyInfo),
	}

	// Auto-migrate database tables
	if err := store.autoMigrate(); err != nil {
		return nil, fmt.Errorf("failed to auto-migrate tables: %w", err)
	}

	return store, nil
}

// LoadFromConfig loads initial configuration from a JSON config file into memory
// with full preprocessing including environment variable resolution and key config parsing.
// All processing is done upfront to ensure zero latency when retrieving data.
//
// If the config file doesn't exist, the system starts with default configuration
// and users can add providers dynamically via the HTTP API.
//
// This method handles:
//   - JSON config file parsing
//   - Environment variable substitution for API keys (env.VARIABLE_NAME)
//   - Key-level config processing for Azure and Vertex (Endpoint, APIVersion, ProjectID, Region, AuthCredentials)
//   - Provider-specific meta config processing (Bedrock only)
//   - Case conversion for provider names (e.g., "OpenAI" -> "openai")
//   - In-memory storage for ultra-fast access during request processing
//   - Graceful handling of missing config files
func (s *ConfigStore) LoadFromConfig(configPath string) error {

	s.configPath = configPath
	s.logger.Info(fmt.Sprintf("Loading configuration from: %s", configPath))

	// Check if config file exists
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return s.loadDefaultConfig()
		}
		return fmt.Errorf("failed to read config file: %w", err)
	}

	// Parse the JSON directly
	var configData struct {
		Client    json.RawMessage            `json:"client"`
		Providers map[string]json.RawMessage `json:"providers"`
		MCP       json.RawMessage            `json:"mcp,omitempty"`
	}

	if err := json.Unmarshal(data, &configData); err != nil {
		return fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Process core configuration if present, otherwise use defaults
	if len(configData.Client) > 0 {
		var clientConfig ClientConfig
		if err := json.Unmarshal(configData.Client, &clientConfig); err != nil {
			return fmt.Errorf("failed to unmarshal client config: %w", err)
		}
		s.ClientConfig = clientConfig
	} else {
		s.ClientConfig = DefaultClientConfig
	}

	// Process provider configurations
	processedProviders := make(map[schemas.ModelProvider]ProviderConfig)

	if len(configData.Providers) > 0 {
		// First unmarshal providers into a map with string keys to handle case conversion
		var rawProviders map[string]ProviderConfig
		if providersBytes, err := json.Marshal(configData.Providers); err != nil {
			return fmt.Errorf("failed to marshal providers: %w", err)
		} else if err := json.Unmarshal(providersBytes, &rawProviders); err != nil {
			return fmt.Errorf("failed to unmarshal providers: %w", err)
		}

		// Create a temporary structure to unmarshal the full JSON with proper meta configs
		var tempConfig struct {
			Providers map[string]struct {
				MetaConfig json.RawMessage `json:"meta_config"`
			} `json:"providers"`
		}

		if err := json.Unmarshal(data, &tempConfig); err != nil {
			return fmt.Errorf("failed to unmarshal configuration file: %w", err)
		}

		// Process each provider configuration
		for rawProviderName, cfg := range rawProviders {
			newEnvKeys := make(map[string]struct{})

			provider := schemas.ModelProvider(strings.ToLower(rawProviderName))

			// Process meta config if it exists
			if tempProvider, exists := tempConfig.Providers[rawProviderName]; exists && len(tempProvider.MetaConfig) > 0 {
				processedMetaConfig, envKeys, err := s.processMetaConfigEnvVars(tempProvider.MetaConfig, provider)

				if err != nil {
					s.cleanupEnvKeys(string(provider), "", envKeys)
					s.logger.Warn(fmt.Sprintf("failed to process env vars in meta config for %s: %v", provider, err))
					continue
				}

				// Parse and set the meta config
				metaConfig, err := s.parseMetaConfig(processedMetaConfig, provider)
				if err != nil {
					s.cleanupEnvKeys(string(provider), "", envKeys)
					s.logger.Warn(fmt.Sprintf("failed to process meta config for %s: %v", provider, err))
					continue
				} else {
					cfg.MetaConfig = metaConfig
				}
			}

			// Process environment variables in keys (including key-level configs)
			for i, key := range cfg.Keys {
				if key.ID == "" {
					cfg.Keys[i].ID = uuid.NewString()
				}

				// Process API key value
				processedValue, envVar, err := s.processEnvValue(key.Value)
				if err != nil {
					s.cleanupEnvKeys(string(provider), "", newEnvKeys)
					s.logger.Warn(fmt.Sprintf("failed to process env vars in keys for %s: %v", provider, err))
					continue
				}
				cfg.Keys[i].Value = processedValue

				// Track environment key if it came from env
				if envVar != "" {
					newEnvKeys[envVar] = struct{}{}
					s.EnvKeys[envVar] = append(s.EnvKeys[envVar], EnvKeyInfo{
						EnvVar:     envVar,
						Provider:   string(provider),
						KeyType:    "api_key",
						ConfigPath: fmt.Sprintf("providers.%s.keys[%s]", provider, key.ID),
						KeyID:      key.ID,
					})
				}

				// Process Azure key config if present
				if key.AzureKeyConfig != nil {
					if err := s.processAzureKeyConfigEnvVars(&cfg.Keys[i], provider, i, newEnvKeys); err != nil {
						s.cleanupEnvKeys(string(provider), "", newEnvKeys)
						s.logger.Warn(fmt.Sprintf("failed to process Azure key config env vars for %s: %v", provider, err))
						continue
					}
				}

				// Process Vertex key config if present
				if key.VertexKeyConfig != nil {
					if err := s.processVertexKeyConfigEnvVars(&cfg.Keys[i], provider, i, newEnvKeys); err != nil {
						s.cleanupEnvKeys(string(provider), "", newEnvKeys)
						s.logger.Warn(fmt.Sprintf("failed to process Vertex key config env vars for %s: %v", provider, err))
						continue
					}
				}
			}

			processedProviders[provider] = cfg
		}

		// Store processed configurations in memory
		s.Providers = processedProviders
	} else {
		s.autoDetectProviders()
	}

	// Parse MCP config if present
	if len(configData.MCP) > 0 {
		var mcpConfig schemas.MCPConfig
		if err := json.Unmarshal(configData.MCP, &mcpConfig); err != nil {
			s.logger.Warn(fmt.Sprintf("failed to parse MCP config: %v", err))
		} else {
			// Process environment variables in MCP config
			s.MCPConfig = &mcpConfig
			s.processMCPEnvVars()
		}
	}

	s.logger.Info("Successfully loaded configuration.")
	return nil
}

// autoMigrate creates/updates the database tables using GORM
func (s *ConfigStore) autoMigrate() error {
	return s.db.AutoMigrate(
		&DBConfigHash{},
		&DBProvider{},
		&DBKey{},
		&DBMCPClient{},
		&DBClientConfig{},
		&DBEnvKey{},
	)
}

// LoadFromDatabase loads initial configuration from the database into memory
// with full preprocessing including environment variable resolution and key config parsing.
// All processing is done upfront to ensure zero latency when retrieving data.
//
// If no configuration exists in the database, the system starts with default configuration
// and users can add providers dynamically via the HTTP API.
//
// This method handles:
//   - Database configuration loading
//   - Environment variable substitution for API keys (env.VARIABLE_NAME)
//   - Key-level config processing for Azure and Vertex (Endpoint, APIVersion, ProjectID, Region, AuthCredentials)
//   - Provider-specific meta config processing (Bedrock only)
//   - In-memory storage for ultra-fast access during request processing
//   - Auto-detection of providers from environment variables if database is empty
func (s *ConfigStore) LoadFromDatabase() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.logger.Info("Loading configuration from database")

	// Load client configuration
	if err := s.loadClientConfigFromDB(); err != nil {
		s.logger.Warn(fmt.Sprintf("Failed to load client config from database, using defaults: %v", err))
		s.ClientConfig = DefaultClientConfig
	}

	// Load providers configuration
	if err := s.loadProvidersFromDB(); err != nil {
		s.logger.Warn(fmt.Sprintf("Failed to load providers from database: %v", err))
		// Auto-detect providers if database load fails
		s.autoDetectProviders()
	}

	// Load MCP configuration
	if err := s.loadMCPFromDB(); err != nil {
		s.logger.Warn(fmt.Sprintf("Failed to load MCP config from database: %v", err))
		s.MCPConfig = nil
	}

	// Load environment variable tracking
	if err := s.loadEnvKeysFromDB(); err != nil {
		s.logger.Warn(fmt.Sprintf("Failed to load env keys from database: %v", err))
		s.EnvKeys = make(map[string][]EnvKeyInfo)
	}

	s.logger.Info("Successfully loaded configuration from database.")
	return nil
}

// loadClientConfigFromDB loads client configuration from database
func (s *ConfigStore) loadClientConfigFromDB() error {
	var dbConfig DBClientConfig
	if err := s.db.First(&dbConfig).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// No client config in database, use defaults
			s.ClientConfig = DefaultClientConfig
			return nil
		}
		return err
	}

	s.ClientConfig = ClientConfig{
		DropExcessRequests:      dbConfig.DropExcessRequests,
		PrometheusLabels:        dbConfig.PrometheusLabels,
		InitialPoolSize:         dbConfig.InitialPoolSize,
		EnableLogging:           dbConfig.EnableLogging,
		EnableGovernance:        dbConfig.EnableGovernance,
		EnforceGovernanceHeader: dbConfig.EnforceGovernanceHeader,
	}

	return nil
}

// loadProvidersFromDB loads all providers and their keys from database
func (s *ConfigStore) loadProvidersFromDB() error {
	var dbProviders []DBProvider
	if err := s.db.Preload("Keys").Find(&dbProviders).Error; err != nil {
		return err
	}

	if len(dbProviders) == 0 {
		// No providers in database, auto-detect from environment
		s.autoDetectProviders()
		return nil
	}

	processedProviders := make(map[schemas.ModelProvider]ProviderConfig)

	for _, dbProvider := range dbProviders {
		provider := schemas.ModelProvider(dbProvider.Name)

		// Convert database keys to schemas.Key
		keys := make([]schemas.Key, len(dbProvider.Keys))
		for i, dbKey := range dbProvider.Keys {
			keys[i] = schemas.Key{
				ID:              dbKey.KeyID,
				Value:           dbKey.Value,
				Models:          dbKey.Models,
				Weight:          dbKey.Weight,
				AzureKeyConfig:  dbKey.AzureKeyConfig,
				VertexKeyConfig: dbKey.VertexKeyConfig,
			}
		}

		providerConfig := ProviderConfig{
			Keys:                     keys,
			NetworkConfig:            dbProvider.NetworkConfig,
			ConcurrencyAndBufferSize: dbProvider.ConcurrencyAndBufferSize,
			MetaConfig:               dbProvider.MetaConfig,
		}

		processedProviders[provider] = providerConfig
	}

	s.Providers = processedProviders
	return nil
}

// loadMCPFromDB loads MCP configuration from database
func (s *ConfigStore) loadMCPFromDB() error {
	var dbClients []DBMCPClient
	if err := s.db.Find(&dbClients).Error; err != nil {
		return err
	}

	if len(dbClients) == 0 {
		s.MCPConfig = nil
		return nil
	}

	clientConfigs := make([]schemas.MCPClientConfig, len(dbClients))
	for i, dbClient := range dbClients {
		clientConfigs[i] = schemas.MCPClientConfig{
			Name:             dbClient.Name,
			ConnectionType:   schemas.MCPConnectionType(dbClient.ConnectionType),
			ConnectionString: dbClient.ConnectionString,
			StdioConfig:      dbClient.StdioConfig,
			ToolsToExecute:   dbClient.ToolsToExecute,
			ToolsToSkip:      dbClient.ToolsToSkip,
		}
	}

	s.MCPConfig = &schemas.MCPConfig{
		ClientConfigs: clientConfigs,
	}

	return nil
}

// loadEnvKeysFromDB loads environment variable tracking from database
func (s *ConfigStore) loadEnvKeysFromDB() error {
	var dbEnvKeys []DBEnvKey
	if err := s.db.Find(&dbEnvKeys).Error; err != nil {
		return err
	}

	s.EnvKeys = make(map[string][]EnvKeyInfo)
	for _, dbEnvKey := range dbEnvKeys {
		s.EnvKeys[dbEnvKey.EnvVar] = append(s.EnvKeys[dbEnvKey.EnvVar], EnvKeyInfo{
			EnvVar:     dbEnvKey.EnvVar,
			Provider:   dbEnvKey.Provider,
			KeyType:    dbEnvKey.KeyType,
			ConfigPath: dbEnvKey.ConfigPath,
			KeyID:      dbEnvKey.KeyID,
		})
	}

	return nil
}

// processEnvValue checks and replaces environment variable references in configuration values.
// Returns the processed value and the environment variable name if it was an env reference.
// Supports the "env.VARIABLE_NAME" syntax for referencing environment variables.
// This enables secure configuration management without hardcoding sensitive values.
//
// Examples:
//   - "env.OPENAI_API_KEY" -> actual value from OPENAI_API_KEY environment variable
//   - "sk-1234567890" -> returned as-is (no env prefix)
func (s *ConfigStore) processEnvValue(value string) (string, string, error) {
	if strings.HasPrefix(value, "env.") {
		envKey := strings.TrimPrefix(value, "env.")
		if envValue := os.Getenv(envKey); envValue != "" {
			return envValue, envKey, nil
		}
		return "", envKey, fmt.Errorf("environment variable %s not found", envKey)
	}
	return value, "", nil
}

// writeConfigToFile writes the current in-memory configuration back to a JSON file
// in the exact same format that LoadFromConfig expects. This enables persistence
// of runtime configuration changes with environment variable references restored.
func (s *ConfigStore) writeConfigToFile(configPath string) error {

	s.logger.Debug(fmt.Sprintf("Writing current configuration to: %s", configPath))

	// Create a map for quick lookup of env vars by provider and path
	envVarsByPath := make(map[string]string)
	for envVar, infos := range s.EnvKeys {
		for _, info := range infos {
			envVarsByPath[info.ConfigPath] = envVar
		}
	}

	// Prepare the output structure
	output := struct {
		Providers map[string]interface{} `json:"providers"`
		MCP       *schemas.MCPConfig     `json:"mcp,omitempty"`
		Client    ClientConfig           `json:"client,omitempty"`
	}{
		Providers: make(map[string]interface{}),
		MCP:       s.getRestoredMCPConfig(envVarsByPath),
		Client:    s.ClientConfig,
	}

	// Convert providers back to the original format with env variable restoration
	for provider, config := range s.Providers {
		providerName := string(provider)

		// Create redacted keys that restore env.* references
		redactedKeys := make([]schemas.Key, len(config.Keys))
		for i, key := range config.Keys {
			redactedKeys[i] = schemas.Key{
				ID:     key.ID,
				Models: key.Models,
				Weight: key.Weight,
			}

			// Restore API key value
			path := fmt.Sprintf("providers.%s.keys[%s]", provider, key.ID)
			if envVar, ok := envVarsByPath[path]; ok {
				redactedKeys[i].Value = "env." + envVar
			} else {
				redactedKeys[i].Value = key.Value // Keep actual value, no asterisk redaction
			}

			// Restore Azure key config if present
			if key.AzureKeyConfig != nil {
				azureConfig := &schemas.AzureKeyConfig{
					Deployments: key.AzureKeyConfig.Deployments,
				}

				// Restore Endpoint
				path = fmt.Sprintf("providers.%s.keys[%s].azure_key_config.endpoint", provider, key.ID)
				if envVar, ok := envVarsByPath[path]; ok {
					azureConfig.Endpoint = "env." + envVar
				} else {
					azureConfig.Endpoint = key.AzureKeyConfig.Endpoint
				}

				// Restore APIVersion if present
				if key.AzureKeyConfig.APIVersion != nil {
					path = fmt.Sprintf("providers.%s.keys[%s].azure_key_config.api_version", provider, key.ID)
					if envVar, ok := envVarsByPath[path]; ok {
						apiVersion := "env." + envVar
						azureConfig.APIVersion = &apiVersion
					} else {
						azureConfig.APIVersion = key.AzureKeyConfig.APIVersion
					}
				}

				redactedKeys[i].AzureKeyConfig = azureConfig
			}

			// Restore Vertex key config if present
			if key.VertexKeyConfig != nil {
				vertexConfig := &schemas.VertexKeyConfig{}

				// Restore ProjectID
				path = fmt.Sprintf("providers.%s.keys[%s].vertex_key_config.project_id", provider, key.ID)
				if envVar, ok := envVarsByPath[path]; ok {
					vertexConfig.ProjectID = "env." + envVar
				} else {
					vertexConfig.ProjectID = key.VertexKeyConfig.ProjectID
				}

				// Restore Region
				path = fmt.Sprintf("providers.%s.keys[%s].vertex_key_config.region", provider, key.ID)
				if envVar, ok := envVarsByPath[path]; ok {
					vertexConfig.Region = "env." + envVar
				} else {
					vertexConfig.Region = key.VertexKeyConfig.Region
				}

				// Restore AuthCredentials
				path = fmt.Sprintf("providers.%s.keys[%s].vertex_key_config.auth_credentials", provider, key.ID)
				if envVar, ok := envVarsByPath[path]; ok {
					vertexConfig.AuthCredentials = "env." + envVar
				} else {
					vertexConfig.AuthCredentials = key.VertexKeyConfig.AuthCredentials
				}

				redactedKeys[i].VertexKeyConfig = vertexConfig
			}
		}

		// Create provider config with restored env references
		providerConfig := map[string]interface{}{
			"keys": redactedKeys,
		}

		if config.NetworkConfig != nil {
			providerConfig["network_config"] = config.NetworkConfig
		}

		if config.ConcurrencyAndBufferSize != nil {
			providerConfig["concurrency_and_buffer_size"] = config.ConcurrencyAndBufferSize
		}

		// Handle meta config with env variable restoration
		if config.MetaConfig != nil {
			restoredMetaConfig := s.restoreMetaConfigEnvVars(provider, *config.MetaConfig, envVarsByPath)
			providerConfig["meta_config"] = restoredMetaConfig
		}

		output.Providers[providerName] = providerConfig
	}

	// Marshal to JSON with proper formatting
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write to file
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	s.logger.Debug(fmt.Sprintf("Successfully wrote configuration to: %s", configPath))
	return nil
}

// getRestoredMCPConfig creates a copy of MCP config with env variable references restored
func (s *ConfigStore) getRestoredMCPConfig(envVarsByPath map[string]string) *schemas.MCPConfig {
	if s.MCPConfig == nil {
		return nil
	}

	// Create a copy of the MCP config
	mcpConfigCopy := &schemas.MCPConfig{
		ClientConfigs: make([]schemas.MCPClientConfig, len(s.MCPConfig.ClientConfigs)),
	}

	// Process each client config
	for i, clientConfig := range s.MCPConfig.ClientConfigs {
		configCopy := schemas.MCPClientConfig{
			Name:           clientConfig.Name,
			ConnectionType: clientConfig.ConnectionType,
			StdioConfig:    clientConfig.StdioConfig,
			ToolsToExecute: append([]string{}, clientConfig.ToolsToExecute...),
			ToolsToSkip:    append([]string{}, clientConfig.ToolsToSkip...),
		}

		// Handle connection string with env variable restoration
		if clientConfig.ConnectionString != nil {
			connStr := *clientConfig.ConnectionString
			path := fmt.Sprintf("mcp.client_configs[%d].connection_string", i)
			if envVar, ok := envVarsByPath[path]; ok {
				connStr = "env." + envVar
			}
			// If not from env var, keep actual value (no asterisk redaction)
			configCopy.ConnectionString = &connStr
		}

		mcpConfigCopy.ClientConfigs[i] = configCopy
	}

	return mcpConfigCopy
}

// restoreMetaConfigEnvVars creates a copy of meta config with env variable references restored
func (s *ConfigStore) restoreMetaConfigEnvVars(provider schemas.ModelProvider, metaConfig schemas.MetaConfig, envVarsByPath map[string]string) interface{} {
	switch m := metaConfig.(type) {
	case *meta.BedrockMetaConfig:
		bedrockConfig := *m // Copy the struct

		// Restore secret access key if it came from env var
		path := fmt.Sprintf("providers.%s.meta_config.secret_access_key", provider)
		if envVar, ok := envVarsByPath[path]; ok {
			bedrockConfig.SecretAccessKey = "env." + envVar
		}
		// Otherwise keep actual value (no asterisk redaction)

		// Restore region if it came from env var
		if bedrockConfig.Region != nil {
			path = fmt.Sprintf("providers.%s.meta_config.region", provider)
			if envVar, ok := envVarsByPath[path]; ok {
				region := "env." + envVar
				bedrockConfig.Region = &region
			}
			// Otherwise keep actual value (no asterisk redaction)
		}

		// Restore session token if it came from env var
		if bedrockConfig.SessionToken != nil {
			path = fmt.Sprintf("providers.%s.meta_config.session_token", provider)
			if envVar, ok := envVarsByPath[path]; ok {
				sessionToken := "env." + envVar
				bedrockConfig.SessionToken = &sessionToken
			}
			// Otherwise keep actual value (no asterisk redaction)
		}

		// Restore ARN if it came from env var
		if bedrockConfig.ARN != nil {
			path = fmt.Sprintf("providers.%s.meta_config.arn", provider)
			if envVar, ok := envVarsByPath[path]; ok {
				arn := "env." + envVar
				bedrockConfig.ARN = &arn
			}
			// Otherwise keep actual value (no asterisk redaction)
		}

		return bedrockConfig

	default:
		return metaConfig
	}
}

// SaveConfig writes the current configuration back to the database
func (s *ConfigStore) SaveConfig() error {
	// Save client config
	if err := s.saveClientConfigToDB(); err != nil {
		return fmt.Errorf("failed to save client config: %w", err)
	}

	// Save providers
	if err := s.saveProvidersToDB(); err != nil {
		return fmt.Errorf("failed to save providers: %w", err)
	}

	// Save MCP config
	if err := s.saveMCPToDB(); err != nil {
		return fmt.Errorf("failed to save MCP config: %w", err)
	}

	// Save env keys
	if err := s.saveEnvKeysToDB(); err != nil {
		return fmt.Errorf("failed to save env keys: %w", err)
	}

	return nil
}

// saveClientConfigToDB saves client configuration to database
func (s *ConfigStore) saveClientConfigToDB() error {
	dbConfig := DBClientConfig{
		DropExcessRequests:      s.ClientConfig.DropExcessRequests,
		InitialPoolSize:         s.ClientConfig.InitialPoolSize,
		EnableLogging:           s.ClientConfig.EnableLogging,
		EnableGovernance:        s.ClientConfig.EnableGovernance,
		EnforceGovernanceHeader: s.ClientConfig.EnforceGovernanceHeader,
		PrometheusLabels:        s.ClientConfig.PrometheusLabels,
	}

	// Delete existing client config and create new one
	if err := s.db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&DBClientConfig{}).Error; err != nil {
		return err
	}

	return s.db.Create(&dbConfig).Error
}

// saveProvidersToDB saves all providers and their keys to database
func (s *ConfigStore) saveProvidersToDB() error {
	// Delete existing providers and keys (cascade will handle keys)
	if err := s.db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&DBProvider{}).Error; err != nil {
		return err
	}

	for providerName, providerConfig := range s.Providers {
		dbProvider := DBProvider{
			Name:                     string(providerName),
			NetworkConfig:            providerConfig.NetworkConfig,
			ConcurrencyAndBufferSize: providerConfig.ConcurrencyAndBufferSize,
			MetaConfig:               providerConfig.MetaConfig,
		}

		// Create provider first
		if err := s.db.Create(&dbProvider).Error; err != nil {
			return err
		}

		// Create keys for this provider
		dbKeys := make([]DBKey, 0, len(providerConfig.Keys))
		for _, key := range providerConfig.Keys {
			dbKey := DBKey{
				ProviderID:      dbProvider.ID,
				KeyID:           key.ID,
				Value:           key.Value,
				Models:          key.Models,
				Weight:          key.Weight,
				AzureKeyConfig:  key.AzureKeyConfig,
				VertexKeyConfig: key.VertexKeyConfig,
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

// saveMCPToDB saves MCP configuration to database
func (s *ConfigStore) saveMCPToDB() error {
	// Delete existing MCP clients
	if err := s.db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&DBMCPClient{}).Error; err != nil {
		return err
	}

	if s.MCPConfig == nil {
		return nil
	}

	dbClients := make([]DBMCPClient, 0, len(s.MCPConfig.ClientConfigs))
	for _, clientConfig := range s.MCPConfig.ClientConfigs {
		dbClient := DBMCPClient{
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

// saveEnvKeysToDB saves environment variable tracking to database
func (s *ConfigStore) saveEnvKeysToDB() error {
	// Delete existing env keys
	if err := s.db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&DBEnvKey{}).Error; err != nil {
		return err
	}

	var dbEnvKeys []DBEnvKey
	for envVar, infos := range s.EnvKeys {
		for _, info := range infos {
			dbEnvKey := DBEnvKey{
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

// parseMetaConfig converts raw JSON to the appropriate provider-specific meta config interface
func (s *ConfigStore) parseMetaConfig(rawMetaConfig json.RawMessage, provider schemas.ModelProvider) (*schemas.MetaConfig, error) {
	switch provider {
	case schemas.Bedrock:
		var bedrockMetaConfig meta.BedrockMetaConfig
		if err := json.Unmarshal(rawMetaConfig, &bedrockMetaConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Bedrock meta config: %w", err)
		}
		var metaConfig schemas.MetaConfig = &bedrockMetaConfig
		return &metaConfig, nil
	}

	return nil, fmt.Errorf("unsupported provider for meta config: %s", provider)
}

// processMetaConfigEnvVars processes environment variables in provider-specific meta configurations.
// This method handles the provider-specific meta config structures and processes environment
// variables in their fields, ensuring type safety and proper field handling.
//
// Supported providers and their processed fields:
//   - Bedrock: SecretAccessKey, Region, SessionToken, ARN
//
// For unsupported providers, the meta config is returned unchanged.
// This approach ensures type safety while supporting environment variable substitution.
func (s *ConfigStore) processMetaConfigEnvVars(rawMetaConfig json.RawMessage, provider schemas.ModelProvider) (json.RawMessage, map[string]struct{}, error) {
	// Track new environment variables
	newEnvKeys := make(map[string]struct{})

	switch provider {
	case schemas.Bedrock:
		var bedrockMetaConfig meta.BedrockMetaConfig
		if err := json.Unmarshal(rawMetaConfig, &bedrockMetaConfig); err != nil {
			return nil, newEnvKeys, fmt.Errorf("failed to unmarshal Bedrock meta config: %w", err)
		}

		secretAccessKey, envVar, err := s.processEnvValue(bedrockMetaConfig.SecretAccessKey)
		if err != nil {
			return nil, newEnvKeys, err
		}
		if envVar != "" {
			newEnvKeys[envVar] = struct{}{}
			s.EnvKeys[envVar] = append(s.EnvKeys[envVar], EnvKeyInfo{
				EnvVar:     envVar,
				Provider:   string(provider),
				KeyType:    "meta_config",
				ConfigPath: fmt.Sprintf("providers.%s.meta_config.secret_access_key", provider),
				KeyID:      "", // Empty for meta config entries
			})
		}
		bedrockMetaConfig.SecretAccessKey = secretAccessKey

		if bedrockMetaConfig.Region != nil {
			region, envVar, err := s.processEnvValue(*bedrockMetaConfig.Region)
			if err != nil {
				return nil, newEnvKeys, err
			}
			if envVar != "" {
				newEnvKeys[envVar] = struct{}{}
				s.EnvKeys[envVar] = append(s.EnvKeys[envVar], EnvKeyInfo{
					EnvVar:     envVar,
					Provider:   string(provider),
					KeyType:    "meta_config",
					ConfigPath: fmt.Sprintf("providers.%s.meta_config.region", provider),
					KeyID:      "", // Empty for meta config entries
				})
			}
			bedrockMetaConfig.Region = &region
		}

		if bedrockMetaConfig.SessionToken != nil {
			sessionToken, envVar, err := s.processEnvValue(*bedrockMetaConfig.SessionToken)
			if err != nil {
				return nil, newEnvKeys, err
			}
			if envVar != "" {
				newEnvKeys[envVar] = struct{}{}
				s.EnvKeys[envVar] = append(s.EnvKeys[envVar], EnvKeyInfo{
					EnvVar:     envVar,
					Provider:   string(provider),
					KeyType:    "meta_config",
					ConfigPath: fmt.Sprintf("providers.%s.meta_config.session_token", provider),
					KeyID:      "", // Empty for meta config entries
				})
			}
			bedrockMetaConfig.SessionToken = &sessionToken
		}

		if bedrockMetaConfig.ARN != nil {
			arn, envVar, err := s.processEnvValue(*bedrockMetaConfig.ARN)
			if err != nil {
				return nil, newEnvKeys, err
			}
			if envVar != "" {
				newEnvKeys[envVar] = struct{}{}
				s.EnvKeys[envVar] = append(s.EnvKeys[envVar], EnvKeyInfo{
					EnvVar:     envVar,
					Provider:   string(provider),
					KeyType:    "meta_config",
					ConfigPath: fmt.Sprintf("providers.%s.meta_config.arn", provider),
					KeyID:      "", // Empty for meta config entries
				})
			}
			bedrockMetaConfig.ARN = &arn
		}

		processedJSON, err := json.Marshal(bedrockMetaConfig)
		if err != nil {
			return nil, newEnvKeys, fmt.Errorf("failed to marshal processed Bedrock meta config: %w", err)
		}
		return processedJSON, newEnvKeys, nil
	}

	return rawMetaConfig, newEnvKeys, nil
}

// GetProviderConfigRaw retrieves the raw, unredacted provider configuration from memory.
// This method is for internal use only, particularly by the account implementation.
//
// Performance characteristics:
//   - Memory access: ultra-fast direct memory access
//   - No database I/O or JSON parsing overhead
//   - Thread-safe with read locks for concurrent access
//
// Returns a copy of the configuration to prevent external modifications.
func (s *ConfigStore) GetProviderConfigRaw(provider schemas.ModelProvider) (*ProviderConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	config, exists := s.Providers[provider]
	if !exists {
		return nil, fmt.Errorf("provider %s not found", provider)
	}

	// Return direct reference for maximum performance - this is used by Bifrost core
	// CRITICAL: Never modify the returned data as it's shared
	return &config, nil
}

func (s *ConfigStore) GetClientConfigFromDB() (*DBClientConfig, error) {
	var dbConfig DBClientConfig
	if err := s.db.First(&dbConfig).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &DBClientConfig{
				DropExcessRequests:      s.ClientConfig.DropExcessRequests,
				InitialPoolSize:         s.ClientConfig.InitialPoolSize,
				PrometheusLabels:        s.ClientConfig.PrometheusLabels,
				EnableLogging:           s.ClientConfig.EnableLogging,
				EnableGovernance:        s.ClientConfig.EnableGovernance,
				EnforceGovernanceHeader: s.ClientConfig.EnforceGovernanceHeader,
			}, nil
		}
		return nil, err
	}

	return &dbConfig, nil
}

// GetProviderConfigRedacted retrieves a provider configuration with sensitive values redacted.
// This method is intended for external API responses and logging.
//
// The returned configuration has sensitive values redacted:
// - API keys are redacted using RedactKey()
// - Values from environment variables show the original env var name (env.VAR_NAME)
//
// Returns a new copy with redacted values that is safe to expose externally.
func (s *ConfigStore) GetProviderConfigRedacted(provider schemas.ModelProvider) (*ProviderConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	config, exists := s.Providers[provider]
	if !exists {
		return nil, fmt.Errorf("provider %s not found", provider)
	}

	// Create a map for quick lookup of env vars for this provider
	envVarsByPath := make(map[string]string)
	for envVar, infos := range s.EnvKeys {
		for _, info := range infos {
			if info.Provider == string(provider) {
				envVarsByPath[info.ConfigPath] = envVar
			}
		}
	}

	// Create redacted config with same structure but redacted values
	redactedConfig := ProviderConfig{
		NetworkConfig:            config.NetworkConfig,
		ConcurrencyAndBufferSize: config.ConcurrencyAndBufferSize,
	}

	// Create redacted keys
	redactedConfig.Keys = make([]schemas.Key, len(config.Keys))
	for i, key := range config.Keys {
		redactedConfig.Keys[i] = schemas.Key{
			ID:     key.ID,
			Models: key.Models, // Copy slice reference - read-only so safe
			Weight: key.Weight,
		}

		// Redact API key value
		path := fmt.Sprintf("providers.%s.keys[%s]", provider, key.ID)
		if envVar, ok := envVarsByPath[path]; ok {
			redactedConfig.Keys[i].Value = "env." + envVar
		} else {
			redactedConfig.Keys[i].Value = RedactKey(key.Value)
		}

		// Redact Azure key config if present
		if key.AzureKeyConfig != nil {
			azureConfig := &schemas.AzureKeyConfig{
				Deployments: key.AzureKeyConfig.Deployments,
			}

			// Redact Endpoint
			path = fmt.Sprintf("providers.%s.keys[%s].azure_key_config.endpoint", provider, key.ID)
			if envVar, ok := envVarsByPath[path]; ok {
				azureConfig.Endpoint = "env." + envVar
			} else {
				azureConfig.Endpoint = RedactKey(key.AzureKeyConfig.Endpoint)
			}

			// Redact APIVersion if present
			if key.AzureKeyConfig.APIVersion != nil {
				path = fmt.Sprintf("providers.%s.keys[%s].azure_key_config.api_version", provider, key.ID)
				if envVar, ok := envVarsByPath[path]; ok {
					apiVersion := "env." + envVar
					azureConfig.APIVersion = &apiVersion
				} else {
					// APIVersion is not sensitive, keep as-is
					azureConfig.APIVersion = key.AzureKeyConfig.APIVersion
				}
			}

			redactedConfig.Keys[i].AzureKeyConfig = azureConfig
		}

		// Redact Vertex key config if present
		if key.VertexKeyConfig != nil {
			vertexConfig := &schemas.VertexKeyConfig{}

			// Redact ProjectID
			path = fmt.Sprintf("providers.%s.keys[%s].vertex_key_config.project_id", provider, key.ID)
			if envVar, ok := envVarsByPath[path]; ok {
				vertexConfig.ProjectID = "env." + envVar
			} else {
				vertexConfig.ProjectID = RedactKey(key.VertexKeyConfig.ProjectID)
			}

			// Region is not sensitive, handle env vars only
			path = fmt.Sprintf("providers.%s.keys[%s].vertex_key_config.region", provider, key.ID)
			if envVar, ok := envVarsByPath[path]; ok {
				vertexConfig.Region = "env." + envVar
			} else {
				vertexConfig.Region = key.VertexKeyConfig.Region
			}

			// Redact AuthCredentials
			path = fmt.Sprintf("providers.%s.keys[%s].vertex_key_config.auth_credentials", provider, key.ID)
			if envVar, ok := envVarsByPath[path]; ok {
				vertexConfig.AuthCredentials = "env." + envVar
			} else {
				vertexConfig.AuthCredentials = RedactKey(key.VertexKeyConfig.AuthCredentials)
			}

			redactedConfig.Keys[i].VertexKeyConfig = vertexConfig
		}
	}

	// Handle meta config redaction if present (Bedrock only)
	if config.MetaConfig != nil {
		redactedMetaConfig := s.redactMetaConfig(provider, *config.MetaConfig, envVarsByPath)
		redactedConfig.MetaConfig = &redactedMetaConfig
	}

	return &redactedConfig, nil
}

// redactMetaConfig creates a redacted copy of meta config based on provider type
// Note: Only Bedrock is supported for meta config now, Azure and Vertex moved to key level
func (s *ConfigStore) redactMetaConfig(provider schemas.ModelProvider, metaConfig schemas.MetaConfig, envVarsByPath map[string]string) schemas.MetaConfig {
	switch m := metaConfig.(type) {
	case *meta.BedrockMetaConfig:
		bedrockConfig := *m // Copy the struct
		path := fmt.Sprintf("providers.%s.meta_config.secret_access_key", provider)
		if envVar, ok := envVarsByPath[path]; ok {
			bedrockConfig.SecretAccessKey = "env." + envVar
		} else {
			bedrockConfig.SecretAccessKey = RedactKey(bedrockConfig.SecretAccessKey)
		}
		if bedrockConfig.Region != nil {
			path = fmt.Sprintf("providers.%s.meta_config.region", provider)
			if envVar, ok := envVarsByPath[path]; ok {
				region := "env." + envVar
				bedrockConfig.Region = &region
			}
		}
		if bedrockConfig.SessionToken != nil {
			path = fmt.Sprintf("providers.%s.meta_config.session_token", provider)
			if envVar, ok := envVarsByPath[path]; ok {
				sessionToken := "env." + envVar
				bedrockConfig.SessionToken = &sessionToken
			} else {
				sessionToken := RedactKey(*bedrockConfig.SessionToken)
				bedrockConfig.SessionToken = &sessionToken
			}
		}
		if bedrockConfig.ARN != nil {
			path = fmt.Sprintf("providers.%s.meta_config.arn", provider)
			if envVar, ok := envVarsByPath[path]; ok {
				arn := "env." + envVar
				bedrockConfig.ARN = &arn
			}
		}
		return &bedrockConfig

	default:
		return metaConfig
	}
}

// GetAllProviders returns all configured provider names.
func (s *ConfigStore) GetAllProviders() ([]schemas.ModelProvider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	providers := make([]schemas.ModelProvider, 0, len(s.Providers))
	for provider := range s.Providers {
		providers = append(providers, provider)
	}

	return providers, nil
}

// AddProvider adds a new provider configuration to memory with full environment variable
// processing. This method is called when new providers are added via the HTTP API.
//
// The method:
//   - Validates that the provider doesn't already exist
//   - Processes environment variables in API keys, key-level configs, and meta configurations
//   - Stores the processed configuration in memory
//   - Updates metadata and timestamps
func (s *ConfigStore) AddProvider(provider schemas.ModelProvider, config ProviderConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if provider already exists
	if _, exists := s.Providers[provider]; exists {
		return fmt.Errorf("provider %s already exists", provider)
	}

	newEnvKeys := make(map[string]struct{})

	// Process environment variables in meta config if present
	if config.MetaConfig != nil {
		rawMetaData, err := json.Marshal(*config.MetaConfig)
		if err != nil {
			return fmt.Errorf("failed to marshal meta config: %w", err)
		}

		processedMetaData, envKeys, err := s.processMetaConfigEnvVars(rawMetaData, provider)

		newEnvKeys = envKeys
		if err != nil {
			s.cleanupEnvKeys(string(provider), "", newEnvKeys)
			return fmt.Errorf("failed to process env vars in meta config: %w", err)
		}

		metaConfig, err := s.parseMetaConfig(processedMetaData, provider)
		if err != nil {
			s.cleanupEnvKeys(string(provider), "", newEnvKeys)
			return fmt.Errorf("failed to parse processed meta config: %w", err)
		}
		config.MetaConfig = metaConfig
	}

	// Process environment variables in keys (including key-level configs)
	for i, key := range config.Keys {
		if key.ID == "" {
			config.Keys[i].ID = uuid.NewString()
		}

		// Process API key value
		processedValue, envVar, err := s.processEnvValue(key.Value)
		if err != nil {
			s.cleanupEnvKeys(string(provider), "", newEnvKeys)
			return fmt.Errorf("failed to process env var in key: %w", err)
		}
		config.Keys[i].Value = processedValue

		// Track environment key if it came from env
		if envVar != "" {
			newEnvKeys[envVar] = struct{}{}
			s.EnvKeys[envVar] = append(s.EnvKeys[envVar], EnvKeyInfo{
				EnvVar:     envVar,
				Provider:   string(provider),
				KeyType:    "api_key",
				ConfigPath: fmt.Sprintf("providers.%s.keys[%s]", provider, key.ID),
				KeyID:      key.ID,
			})
		}

		// Process Azure key config if present
		if key.AzureKeyConfig != nil {
			if err := s.processAzureKeyConfigEnvVars(&config.Keys[i], provider, i, newEnvKeys); err != nil {
				s.cleanupEnvKeys(string(provider), "", newEnvKeys)
				return fmt.Errorf("failed to process Azure key config env vars: %w", err)
			}
		}

		// Process Vertex key config if present
		if key.VertexKeyConfig != nil {
			if err := s.processVertexKeyConfigEnvVars(&config.Keys[i], provider, i, newEnvKeys); err != nil {
				s.cleanupEnvKeys(string(provider), "", newEnvKeys)
				return fmt.Errorf("failed to process Vertex key config env vars: %w", err)
			}
		}
	}

	s.Providers[provider] = config

	s.logger.Info(fmt.Sprintf("Added provider: %s", provider))
	return nil
}

// UpdateProviderConfig updates a provider configuration in memory with full environment
// variable processing. This method is called when provider configurations are modified
// via the HTTP API and ensures all data processing is done upfront.
//
// The method:
//   - Processes environment variables in API keys, key-level configs, and meta configurations
//   - Stores the processed configuration in memory
//   - Updates metadata and timestamps
//   - Thread-safe operation with write locks
//
// Note: Environment variable cleanup for deleted/updated keys is now handled automatically
// by the mergeKeys function before this method is called.
//
// Parameters:
//   - provider: The provider to update
//   - config: The new configuration
func (s *ConfigStore) UpdateProviderConfig(provider schemas.ModelProvider, config ProviderConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Track new environment variables being added
	newEnvKeys := make(map[string]struct{})

	// Process environment variables in meta config if present
	if config.MetaConfig != nil {
		rawMetaData, err := json.Marshal(*config.MetaConfig)
		if err != nil {
			return fmt.Errorf("failed to marshal meta config: %w", err)
		}

		processedMetaData, envKeys, err := s.processMetaConfigEnvVars(rawMetaData, provider)
		if err != nil {
			s.cleanupEnvKeys(string(provider), "", envKeys) // Clean up only new vars on failure
			return fmt.Errorf("failed to process env vars in meta config: %w", err)
		}

		metaConfig, err := s.parseMetaConfig(processedMetaData, provider)
		if err != nil {
			s.cleanupEnvKeys(string(provider), "", envKeys) // Clean up only new vars on failure
			return fmt.Errorf("failed to parse processed meta config: %w", err)
		}
		config.MetaConfig = metaConfig

		// Add the new env vars to tracking
		for envVar := range envKeys {
			newEnvKeys[envVar] = struct{}{}
		}
	}

	// Process environment variables in keys (including key-level configs)
	for i, key := range config.Keys {
		if key.ID == "" {
			config.Keys[i].ID = uuid.NewString()
		}

		// Process API key value
		processedValue, envVar, err := s.processEnvValue(key.Value)
		if err != nil {
			s.cleanupEnvKeys(string(provider), "", newEnvKeys) // Clean up only new vars on failure
			return fmt.Errorf("failed to process env var in key: %w", err)
		}
		config.Keys[i].Value = processedValue

		// Track environment key if it came from env
		if envVar != "" {
			newEnvKeys[envVar] = struct{}{}
			s.EnvKeys[envVar] = append(s.EnvKeys[envVar], EnvKeyInfo{
				EnvVar:     envVar,
				Provider:   string(provider),
				KeyType:    "api_key",
				ConfigPath: fmt.Sprintf("providers.%s.keys[%s]", provider, key.ID),
				KeyID:      key.ID,
			})
		}

		// Process Azure key config if present
		if key.AzureKeyConfig != nil {
			if err := s.processAzureKeyConfigEnvVars(&config.Keys[i], provider, i, newEnvKeys); err != nil {
				s.cleanupEnvKeys(string(provider), "", newEnvKeys)
				return fmt.Errorf("failed to process Azure key config env vars: %w", err)
			}
		}

		// Process Vertex key config if present
		if key.VertexKeyConfig != nil {
			if err := s.processVertexKeyConfigEnvVars(&config.Keys[i], provider, i, newEnvKeys); err != nil {
				s.cleanupEnvKeys(string(provider), "", newEnvKeys)
				return fmt.Errorf("failed to process Vertex key config env vars: %w", err)
			}
		}
	}

	s.Providers[provider] = config

	s.logger.Info(fmt.Sprintf("Updated configuration for provider: %s", provider))
	return nil
}

// RemoveProvider removes a provider configuration from memory.
func (s *ConfigStore) RemoveProvider(provider schemas.ModelProvider) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.Providers[provider]; !exists {
		return fmt.Errorf("provider %s not found", provider)
	}

	delete(s.Providers, provider)
	s.cleanupEnvKeys(string(provider), "", nil)

	s.logger.Info(fmt.Sprintf("Removed provider: %s", provider))
	return nil
}

// processMCPEnvVars processes environment variables in the MCP configuration.
// This method handles the MCP config structures and processes environment
// variables in their fields, ensuring type safety and proper field handling.
//
// Supported fields that are processed:
//   - ConnectionString in each MCP ClientConfig
//
// Returns an error if any required environment variable is missing.
// This approach ensures type safety while supporting environment variable substitution.
func (s *ConfigStore) processMCPEnvVars() error {
	var missingEnvVars []string

	// Process each client config
	for i, clientConfig := range s.MCPConfig.ClientConfigs {
		// Process ConnectionString if present
		if clientConfig.ConnectionString != nil {
			newValue, envVar, err := s.processEnvValue(*clientConfig.ConnectionString)
			if err != nil {
				s.logger.Warn(fmt.Sprintf("failed to process env vars in MCP client %s: %v", clientConfig.Name, err))
				missingEnvVars = append(missingEnvVars, envVar)
				continue
			}
			if envVar != "" {
				s.EnvKeys[envVar] = append(s.EnvKeys[envVar], EnvKeyInfo{
					EnvVar:     envVar,
					Provider:   "",
					KeyType:    "connection_string",
					ConfigPath: fmt.Sprintf("mcp.client_configs[%d].connection_string", i),
					KeyID:      "", // Empty for MCP connection strings
				})
			}
			s.MCPConfig.ClientConfigs[i].ConnectionString = &newValue
		}
	}

	if len(missingEnvVars) > 0 {
		return fmt.Errorf("missing environment variables: %v", missingEnvVars)
	}

	return nil
}

// SetBifrostClient sets the Bifrost client in the store.
// This is used to allow the store to access the Bifrost client.
// This is useful for the MCP handler to access the Bifrost client.
func (s *ConfigStore) SetBifrostClient(client *bifrost.Bifrost) {
	s.muMCP.Lock()
	defer s.muMCP.Unlock()

	s.client = client
}

// AddMCPClient adds a new MCP client to the configuration.
// This method is called when a new MCP client is added via the HTTP API.
//
// The method:
//   - Validates that the MCP client doesn't already exist
//   - Processes environment variables in the MCP client configuration
//   - Stores the processed configuration in memory
func (s *ConfigStore) AddMCPClient(clientConfig schemas.MCPClientConfig) error {
	if s.client == nil {
		return fmt.Errorf("bifrost client not set")
	}

	s.muMCP.Lock()
	defer s.muMCP.Unlock()

	if s.MCPConfig == nil {
		s.MCPConfig = &schemas.MCPConfig{}
	}

	// Track new environment variables
	newEnvKeys := make(map[string]struct{})

	s.MCPConfig.ClientConfigs = append(s.MCPConfig.ClientConfigs, clientConfig)

	// Process environment variables in the new client config
	if clientConfig.ConnectionString != nil {
		processedValue, envVar, err := s.processEnvValue(*clientConfig.ConnectionString)
		if err != nil {
			s.MCPConfig.ClientConfigs = s.MCPConfig.ClientConfigs[:len(s.MCPConfig.ClientConfigs)-1]
			return fmt.Errorf("failed to process env var in connection string: %w", err)
		}
		if envVar != "" {
			newEnvKeys[envVar] = struct{}{}
			s.EnvKeys[envVar] = append(s.EnvKeys[envVar], EnvKeyInfo{
				EnvVar:     envVar,
				Provider:   "",
				KeyType:    "connection_string",
				ConfigPath: fmt.Sprintf("mcp.client_configs.%s.connection_string", clientConfig.Name),
				KeyID:      "", // Empty for MCP connection strings
			})
		}
		s.MCPConfig.ClientConfigs[len(s.MCPConfig.ClientConfigs)-1].ConnectionString = &processedValue
	}

	// Config with processed env vars
	if err := s.client.AddMCPClient(s.MCPConfig.ClientConfigs[len(s.MCPConfig.ClientConfigs)-1]); err != nil {
		s.MCPConfig.ClientConfigs = s.MCPConfig.ClientConfigs[:len(s.MCPConfig.ClientConfigs)-1]
		s.cleanupEnvKeys("", clientConfig.Name, newEnvKeys)
		return fmt.Errorf("failed to add MCP client: %w", err)
	}

	return nil
}

// RemoveMCPClient removes an MCP client from the configuration.
// This method is called when an MCP client is removed via the HTTP API.
//
// The method:
//   - Validates that the MCP client exists
//   - Removes the MCP client from the configuration
//   - Removes the MCP client from the Bifrost client
func (s *ConfigStore) RemoveMCPClient(name string) error {
	if s.client == nil {
		return fmt.Errorf("bifrost client not set")
	}

	s.muMCP.Lock()
	defer s.muMCP.Unlock()

	if s.MCPConfig == nil {
		return fmt.Errorf("no MCP config found")
	}

	if err := s.client.RemoveMCPClient(name); err != nil {
		return fmt.Errorf("failed to remove MCP client: %w", err)
	}

	for i, clientConfig := range s.MCPConfig.ClientConfigs {
		if clientConfig.Name == name {
			s.MCPConfig.ClientConfigs = append(s.MCPConfig.ClientConfigs[:i], s.MCPConfig.ClientConfigs[i+1:]...)
			break
		}
	}

	s.cleanupEnvKeys("", name, nil)

	return nil
}

// EditMCPClientTools edits the tools of an MCP client.
// This allows for dynamic MCP client tool management at runtime.
//
// Parameters:
//   - name: Name of the client to edit
//   - toolsToAdd: Tools to add to the client
//   - toolsToRemove: Tools to remove from the client
func (s *ConfigStore) EditMCPClientTools(name string, toolsToAdd []string, toolsToRemove []string) error {
	if s.client == nil {
		return fmt.Errorf("bifrost client not set")
	}

	s.muMCP.Lock()
	defer s.muMCP.Unlock()

	if s.MCPConfig == nil {
		return fmt.Errorf("no MCP config found")
	}

	if err := s.client.EditMCPClientTools(name, toolsToAdd, toolsToRemove); err != nil {
		return fmt.Errorf("failed to edit MCP client tools: %w", err)
	}

	for i, clientConfig := range s.MCPConfig.ClientConfigs {
		if clientConfig.Name == name {
			s.MCPConfig.ClientConfigs[i].ToolsToExecute = toolsToAdd
			s.MCPConfig.ClientConfigs[i].ToolsToSkip = toolsToRemove
			break
		}
	}

	return nil
}

// RedactMCPClientConfig creates a redacted copy of an MCP client configuration.
// Connection strings are either redacted or replaced with their environment variable names.
func (s *ConfigStore) RedactMCPClientConfig(config schemas.MCPClientConfig) schemas.MCPClientConfig {
	// Create a copy with basic fields
	configCopy := schemas.MCPClientConfig{
		Name:             config.Name,
		ConnectionType:   config.ConnectionType,
		ConnectionString: config.ConnectionString,
		StdioConfig:      config.StdioConfig,
		ToolsToExecute:   append([]string{}, config.ToolsToExecute...),
		ToolsToSkip:      append([]string{}, config.ToolsToSkip...),
	}

	// Handle connection string if present
	if config.ConnectionString != nil {
		connStr := *config.ConnectionString

		// Check if this value came from an env var
		for envVar, infos := range s.EnvKeys {
			for _, info := range infos {
				if info.Provider == "" && info.KeyType == "connection_string" && info.ConfigPath == fmt.Sprintf("mcp.client_configs.%s.connection_string", config.Name) {
					connStr = "env." + envVar
					break
				}
			}
		}

		// If not from env var, redact it
		if !strings.HasPrefix(connStr, "env.") {
			connStr = RedactKey(connStr)
		}
		configCopy.ConnectionString = &connStr
	}

	return configCopy
}

// RedactKey redacts sensitive key values by showing only the first and last 4 characters
func RedactKey(key string) string {
	if key == "" {
		return ""
	}

	// If key is 8 characters or less, just return all asterisks
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}

	// Show first 4 and last 4 characters, replace middle with asterisks
	prefix := key[:4]
	suffix := key[len(key)-4:]
	middle := strings.Repeat("*", 24)

	return prefix + middle + suffix
}

// IsRedacted checks if a key value is redacted, either by being an environment variable
// reference (env.VAR_NAME) or containing the exact redaction pattern from RedactKey.
func IsRedacted(key string) bool {
	if key == "" {
		return false
	}

	// Check if it's an environment variable reference
	if strings.HasPrefix(key, "env.") {
		return true
	}

	// Check for exact redaction pattern: 4 chars + 24 asterisks + 4 chars
	if len(key) == 32 {
		middle := key[4:28]
		if middle == strings.Repeat("*", 24) {
			return true
		}
	}

	return false
}

// cleanupEnvKeys removes environment variable entries from the store based on the given criteria.
// If envVarsToRemove is nil, it removes all env vars for the specified provider/client.
// If envVarsToRemove is provided, it only removes those specific env vars.
//
// Parameters:
//   - provider: Provider name to clean up (empty string for MCP clients)
//   - mcpClientName: MCP client name to clean up (empty string for providers)
//   - envVarsToRemove: Optional map of specific env vars to remove (nil to remove all)
func (s *ConfigStore) cleanupEnvKeys(provider string, mcpClientName string, envVarsToRemove map[string]struct{}) {
	// If envVarsToRemove is provided, only clean those specific vars
	if envVarsToRemove != nil {
		for envVar := range envVarsToRemove {
			s.cleanupEnvVar(envVar, provider, mcpClientName)
		}
		return
	}

	// If envVarsToRemove is nil, clean all vars for the provider/client
	for envVar := range s.EnvKeys {
		s.cleanupEnvVar(envVar, provider, mcpClientName)
	}
}

// cleanupEnvVar removes entries for a specific environment variable based on provider/client.
// This is a helper function to avoid duplicating the filtering logic.
func (s *ConfigStore) cleanupEnvVar(envVar, provider, mcpClientName string) {
	infos := s.EnvKeys[envVar]
	if len(infos) == 0 {
		return
	}

	// Keep entries that don't match the provider/client we're cleaning up
	filteredInfos := make([]EnvKeyInfo, 0, len(infos))
	for _, info := range infos {
		shouldKeep := false
		if provider != "" {
			shouldKeep = info.Provider != provider
		} else if mcpClientName != "" {
			shouldKeep = info.Provider != "" || !strings.HasPrefix(info.ConfigPath, fmt.Sprintf("mcp.client_configs.%s", mcpClientName))
		}
		if shouldKeep {
			filteredInfos = append(filteredInfos, info)
		}
	}

	if len(filteredInfos) == 0 {
		delete(s.EnvKeys, envVar)
	} else {
		s.EnvKeys[envVar] = filteredInfos
	}
}

// CleanupEnvKeysForKeys removes environment variable entries for specific keys that are being deleted.
// This function targets key-specific environment variables based on key IDs.
//
// Parameters:
//   - provider: Provider name the keys belong to
//   - keysToDelete: List of keys being deleted (uses their IDs to identify env vars to clean up)
func (s *ConfigStore) CleanupEnvKeysForKeys(provider string, keysToDelete []schemas.Key) {
	// Create a set of key IDs to delete for efficient lookup
	keyIDsToDelete := make(map[string]bool)
	for _, key := range keysToDelete {
		keyIDsToDelete[key.ID] = true
	}

	// Iterate through all environment variables and remove entries for deleted keys
	for envVar, infos := range s.EnvKeys {
		filteredInfos := make([]EnvKeyInfo, 0, len(infos))

		for _, info := range infos {
			// Keep entries that either:
			// 1. Don't belong to this provider, OR
			// 2. Don't have a KeyID (meta config, MCP), OR
			// 3. Have a KeyID that's not being deleted
			shouldKeep := info.Provider != provider ||
				info.KeyID == "" ||
				!keyIDsToDelete[info.KeyID]

			if shouldKeep {
				filteredInfos = append(filteredInfos, info)
			}
		}

		// Update or delete the environment variable entry
		if len(filteredInfos) == 0 {
			delete(s.EnvKeys, envVar)
		} else {
			s.EnvKeys[envVar] = filteredInfos
		}
	}
}

// CleanupEnvKeysForUpdatedKeys removes environment variable entries for keys that are being updated
// but whose environment variables are changing. This prevents stale env var references.
//
// Parameters:
//   - provider: Provider name the keys belong to
//   - keysToUpdate: List of keys being updated (uses their IDs to identify env vars to clean up)
func (s *ConfigStore) CleanupEnvKeysForUpdatedKeys(provider string, keysToUpdate []schemas.Key) {
	// Create a set of key IDs to update for efficient lookup
	keyIDsToUpdate := make(map[string]bool)
	for _, key := range keysToUpdate {
		keyIDsToUpdate[key.ID] = true
	}

	// Iterate through all environment variables and remove entries for updated keys
	// The updated keys will re-add their env vars during processing
	for envVar, infos := range s.EnvKeys {
		filteredInfos := make([]EnvKeyInfo, 0, len(infos))

		for _, info := range infos {
			// Keep entries that either:
			// 1. Don't belong to this provider, OR
			// 2. Don't have a KeyID (meta config, MCP), OR
			// 3. Have a KeyID that's not being updated
			shouldKeep := info.Provider != provider ||
				info.KeyID == "" ||
				!keyIDsToUpdate[info.KeyID]

			if shouldKeep {
				filteredInfos = append(filteredInfos, info)
			}
		}

		// Update or delete the environment variable entry
		if len(filteredInfos) == 0 {
			delete(s.EnvKeys, envVar)
		} else {
			s.EnvKeys[envVar] = filteredInfos
		}
	}
}

// autoDetectProviders automatically detects common environment variables and sets up providers
// when no configuration file exists. This enables zero-config startup when users have set
// standard environment variables like OPENAI_API_KEY, ANTHROPIC_API_KEY, etc.
//
// Supported environment variables:
//   - OpenAI: OPENAI_API_KEY, OPENAI_KEY
//   - Anthropic: ANTHROPIC_API_KEY, ANTHROPIC_KEY
//   - Mistral: MISTRAL_API_KEY, MISTRAL_KEY
//
// For each detected provider, it creates a default configuration with:
//   - The detected API key with weight 1.0
//   - Empty models list (provider will use default models)
//   - Default concurrency and buffer size settings
func (s *ConfigStore) autoDetectProviders() {
	// Define common environment variable patterns for each provider
	providerEnvVars := map[schemas.ModelProvider][]string{
		schemas.OpenAI:    {"OPENAI_API_KEY", "OPENAI_KEY"},
		schemas.Anthropic: {"ANTHROPIC_API_KEY", "ANTHROPIC_KEY"},
		schemas.Mistral:   {"MISTRAL_API_KEY", "MISTRAL_KEY"},
	}

	detectedCount := 0

	for provider, envVars := range providerEnvVars {
		for _, envVar := range envVars {
			if apiKey := os.Getenv(envVar); apiKey != "" {
				// Generate a unique ID for the auto-detected key
				keyID := uuid.NewString()

				// Create default provider configuration
				providerConfig := ProviderConfig{
					Keys: []schemas.Key{
						{
							ID:     keyID,
							Value:  apiKey,
							Models: []string{}, // Empty means all supported models
							Weight: 1.0,
						},
					},
					ConcurrencyAndBufferSize: &schemas.DefaultConcurrencyAndBufferSize,
				}

				// Add to providers map
				s.Providers[provider] = providerConfig

				// Track the environment variable
				s.EnvKeys[envVar] = append(s.EnvKeys[envVar], EnvKeyInfo{
					EnvVar:     envVar,
					Provider:   string(provider),
					KeyType:    "api_key",
					ConfigPath: fmt.Sprintf("providers.%s.keys[%s]", provider, keyID),
					KeyID:      keyID,
				})

				s.logger.Info(fmt.Sprintf("Auto-detected %s provider from environment variable %s", provider, envVar))
				detectedCount++
				break // Only use the first found env var for each provider
			}
		}
	}

	if detectedCount > 0 {
		s.logger.Info(fmt.Sprintf("Auto-configured %d provider(s) from environment variables", detectedCount))
	}
}

// processAzureKeyConfigEnvVars processes environment variables in Azure key configuration
func (s *ConfigStore) processAzureKeyConfigEnvVars(key *schemas.Key, provider schemas.ModelProvider, keyIndex int, newEnvKeys map[string]struct{}) error {
	azureConfig := key.AzureKeyConfig

	// Process Endpoint
	processedEndpoint, envVar, err := s.processEnvValue(azureConfig.Endpoint)
	if err != nil {
		return err
	}
	if envVar != "" {
		newEnvKeys[envVar] = struct{}{}
		s.EnvKeys[envVar] = append(s.EnvKeys[envVar], EnvKeyInfo{
			EnvVar:     envVar,
			Provider:   string(provider),
			KeyType:    "azure_config",
			ConfigPath: fmt.Sprintf("providers.%s.keys[%s].azure_key_config.endpoint", provider, key.ID),
			KeyID:      key.ID,
		})
	}
	azureConfig.Endpoint = processedEndpoint

	// Process APIVersion if present
	if azureConfig.APIVersion != nil {
		processedAPIVersion, envVar, err := s.processEnvValue(*azureConfig.APIVersion)
		if err != nil {
			return err
		}
		if envVar != "" {
			newEnvKeys[envVar] = struct{}{}
			s.EnvKeys[envVar] = append(s.EnvKeys[envVar], EnvKeyInfo{
				EnvVar:     envVar,
				Provider:   string(provider),
				KeyType:    "azure_config",
				ConfigPath: fmt.Sprintf("providers.%s.keys[%s].azure_key_config.api_version", provider, key.ID),
				KeyID:      key.ID,
			})
		}
		azureConfig.APIVersion = &processedAPIVersion
	}

	return nil
}

// processVertexKeyConfigEnvVars processes environment variables in Vertex key configuration
func (s *ConfigStore) processVertexKeyConfigEnvVars(key *schemas.Key, provider schemas.ModelProvider, keyIndex int, newEnvKeys map[string]struct{}) error {
	vertexConfig := key.VertexKeyConfig

	// Process ProjectID
	processedProjectID, envVar, err := s.processEnvValue(vertexConfig.ProjectID)
	if err != nil {
		return err
	}
	if envVar != "" {
		newEnvKeys[envVar] = struct{}{}
		s.EnvKeys[envVar] = append(s.EnvKeys[envVar], EnvKeyInfo{
			EnvVar:     envVar,
			Provider:   string(provider),
			KeyType:    "vertex_config",
			ConfigPath: fmt.Sprintf("providers.%s.keys[%s].vertex_key_config.project_id", provider, key.ID),
			KeyID:      key.ID,
		})
	}
	vertexConfig.ProjectID = processedProjectID

	// Process Region
	processedRegion, envVar, err := s.processEnvValue(vertexConfig.Region)
	if err != nil {
		return err
	}
	if envVar != "" {
		newEnvKeys[envVar] = struct{}{}
		s.EnvKeys[envVar] = append(s.EnvKeys[envVar], EnvKeyInfo{
			EnvVar:     envVar,
			Provider:   string(provider),
			KeyType:    "vertex_config",
			ConfigPath: fmt.Sprintf("providers.%s.keys[%s].vertex_key_config.region", provider, key.ID),
			KeyID:      key.ID,
		})
	}
	vertexConfig.Region = processedRegion

	// Process AuthCredentials
	processedAuthCredentials, envVar, err := s.processEnvValue(vertexConfig.AuthCredentials)
	if err != nil {
		return err
	}
	if envVar != "" {
		newEnvKeys[envVar] = struct{}{}
		s.EnvKeys[envVar] = append(s.EnvKeys[envVar], EnvKeyInfo{
			EnvVar:     envVar,
			Provider:   string(provider),
			KeyType:    "vertex_config",
			ConfigPath: fmt.Sprintf("providers.%s.keys[%s].vertex_key_config.auth_credentials", provider, key.ID),
			KeyID:      key.ID,
		})
	}
	vertexConfig.AuthCredentials = processedAuthCredentials

	return nil
}

// LoadConfiguration implements the hybrid file-database configuration loading approach.
// It checks for a config.json file on startup and compares its hash with the stored hash in the database.
// If the hash matches, it loads from the database (fast path).
// If the hash differs or no previous hash exists, it loads from the file and updates the database.
//
// Flow:
// 1. Check if config.json exists in app directory
// 2. If exists: Calculate hash and compare with DB hash
//   - Hash matches: Load from DB (fast path)
//   - Hash differs: Load from file → Update DB → Store new hash
//
// 3. If not exists: Load from DB only (current behavior)
func (s *ConfigStore) LoadConfiguration() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.logger.Info(fmt.Sprintf("Checking for configuration file: %s", s.configPath))

	// Check if config file exists
	if _, err := os.Stat(s.configPath); err == nil {
		// File exists - implement hash-based loading
		return s.loadWithFileCheck(s.configPath)
	} else {
		// No file - load from DB only
		s.logger.Info("No config.json file found, loading from database")
		return s.loadFromDatabaseInternal()
	}
}

func (s *ConfigStore) loadDefaultConfig() error {
	s.logger.Info(fmt.Sprintf("Config file %s not found, starting with default configuration. Providers can be added dynamically via UI.", s.configPath))

	// Initialize with default configuration
	s.ClientConfig = DefaultClientConfig
	s.Providers = make(map[schemas.ModelProvider]ProviderConfig)
	s.MCPConfig = nil

	// Auto-detect and configure providers from common environment variables
	s.autoDetectProviders()

	return s.db.Transaction(func(tx *gorm.DB) error {
		// Temporarily swap database for transaction
		oldDB := s.db
		s.db = tx
		defer func() { s.db = oldDB }()

		//update database with default config
		if err := s.SaveConfig(); err != nil {
			return fmt.Errorf("failed to sync to database: %w", err)
		}

		if err := s.writeConfigToFile(s.configPath); err != nil {
			return fmt.Errorf("failed to write config to file: %w", err)
		}

		hash, err := s.calculateFileHash(s.configPath)
		if err != nil {
			return fmt.Errorf("failed to calculate file hash: %w", err)
		}

		if err := s.storeConfigHash(tx, hash); err != nil {
			return err
		}

		s.logger.Info("Successfully initialized with default configuration.")
		return nil

	})
}

// loadWithFileCheck implements the hash comparison and loading logic
func (s *ConfigStore) loadWithFileCheck(configFile string) error {
	// 1. Calculate current file hash
	currentHash, err := s.calculateFileHash(configFile)
	if err != nil {
		return fmt.Errorf("failed to calculate file hash: %w", err)
	}

	// 2. Get latest stored hash from database
	var latestHash DBConfigHash
	err = s.db.Order("updated_at DESC").First(&latestHash).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("failed to get latest hash from database: %w", err)
	}

	// 3. Compare hashes
	if err == nil && latestHash.Hash == currentHash {
		// Hash matches - load from DB (fast path)
		s.logger.Info("Config file unchanged, loading from database")
		return s.loadFromDatabaseInternal()
	} else {
		// Hash differs or no previous hash - load from file
		s.logger.Info("Config file changed or no previous hash found, loading from file and updating database")
		return s.loadFromFileAndUpdateDB(configFile, currentHash)
	}
}

// calculateFileHash calculates SHA256 hash of the config file
func (s *ConfigStore) calculateFileHash(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// loadFromFileAndUpdateDB loads configuration from file and updates the database
func (s *ConfigStore) loadFromFileAndUpdateDB(configFile, hash string) error {
	// 1. Load config from file using existing LoadFromConfig method
	if err := s.LoadFromConfig(configFile); err != nil {
		return fmt.Errorf("failed to load from file: %w", err)
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		// Temporarily swap database for transaction
		oldDB := s.db
		s.db = tx
		defer func() { s.db = oldDB }()

		// 2. Update database with file data
		if err := s.SaveConfig(); err != nil {
			return fmt.Errorf("failed to sync to database: %w", err)
		}

		if err := s.storeConfigHash(tx, hash); err != nil {
			return err
		}

		s.logger.Info(fmt.Sprintf("Successfully loaded configuration from file and updated database with hash: %s", hash[:8]))
		return nil
	})
}

// loadFromDatabaseInternal is the internal version of LoadFromDatabase without locking
// (since LoadConfiguration already holds the lock)
func (s *ConfigStore) loadFromDatabaseInternal() error {
	s.logger.Info("Loading configuration from database")

	// Load client configuration
	if err := s.loadClientConfigFromDB(); err != nil {
		s.logger.Warn(fmt.Sprintf("Failed to load client config from database, using defaults: %v", err))
		s.ClientConfig = DefaultClientConfig
	}

	// Load providers configuration
	if err := s.loadProvidersFromDB(); err != nil {
		s.logger.Warn(fmt.Sprintf("Failed to load providers from database: %v", err))
		// Auto-detect providers if database load fails
		s.autoDetectProviders()
	}

	// Load MCP configuration
	if err := s.loadMCPFromDB(); err != nil {
		s.logger.Warn(fmt.Sprintf("Failed to load MCP config from database: %v", err))
		s.MCPConfig = nil
	}

	// Load environment variable tracking
	if err := s.loadEnvKeysFromDB(); err != nil {
		s.logger.Warn(fmt.Sprintf("Failed to load env keys from database: %v", err))
		s.EnvKeys = make(map[string][]EnvKeyInfo)
	}

	s.logger.Info("Successfully loaded configuration from database.")
	return nil
}

func (s *ConfigStore) storeConfigHash(tx *gorm.DB, hash string) error {
	var existingHash DBConfigHash
	if err := tx.Where("hash = ?", hash).First(&existingHash).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Hash doesn't exist, create new record
			newHash := DBConfigHash{
				Hash:      hash,
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			if err := tx.Create(&newHash).Error; err != nil {
				return fmt.Errorf("failed to store hash in database: %w", err)
			}
		} else {
			return fmt.Errorf("failed to check existing hash: %w", err)
		}
	} else {
		// Hash exists, update the UpdatedAt field
		if err := tx.Model(&existingHash).Update("updated_at", time.Now()).Error; err != nil {
			return fmt.Errorf("failed to update hash record: %w", err)
		}
	}
	return nil
}
