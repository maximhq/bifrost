// Package lib provides core functionality for the Bifrost HTTP service,
// including context propagation, header management, and integration with monitoring systems.
package lib

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/semanticcache"
	"gorm.io/gorm"
)

// HandlerStore provides access to runtime configuration values for handlers.
// This interface allows handlers to access only the configuration they need
// without depending on the entire ConfigStore, improving testability and decoupling.
type HandlerStore interface {
	// ShouldAllowDirectKeys returns whether direct API keys in headers are allowed
	ShouldAllowDirectKeys() bool
}

// ConfigData represents the configuration data for the Bifrost HTTP transport.
// It contains the client configuration, provider configurations, MCP configuration,
// vector store configuration, config store configuration, and logs store configuration.
type ConfigData struct {
	Client            *configstore.ClientConfig             `json:"client"`
	Providers         map[string]configstore.ProviderConfig `json:"providers"`
	MCP               *schemas.MCPConfig                    `json:"mcp,omitempty"`
	Governance        *configstore.GovernanceConfig         `json:"governance,omitempty"`
	VectorStoreConfig *vectorstore.Config                   `json:"vector_store,omitempty"`
	ConfigStoreConfig *configstore.Config                   `json:"config_store,omitempty"`
	LogsStoreConfig   *logstore.Config                      `json:"logs_store,omitempty"`
	Plugins           []*schemas.PluginConfig               `json:"plugins,omitempty"`
}

// UnmarshalJSON unmarshals the ConfigData from JSON using internal unmarshallers
// for VectorStoreConfig, ConfigStoreConfig, and LogsStoreConfig to ensure proper
// type safety and configuration parsing.
func (cd *ConfigData) UnmarshalJSON(data []byte) error {
	// First, unmarshal into a temporary struct to get all fields except the complex configs
	type TempConfigData struct {
		Client            *configstore.ClientConfig             `json:"client"`
		Providers         map[string]configstore.ProviderConfig `json:"providers"`
		MCP               *schemas.MCPConfig                    `json:"mcp,omitempty"`
		Governance        *configstore.GovernanceConfig         `json:"governance,omitempty"`
		VectorStoreConfig json.RawMessage                       `json:"vector_store,omitempty"`
		ConfigStoreConfig json.RawMessage                       `json:"config_store,omitempty"`
		LogsStoreConfig   json.RawMessage                       `json:"logs_store,omitempty"`
		Plugins           []*schemas.PluginConfig               `json:"plugins,omitempty"`
	}

	var temp TempConfigData
	if err := json.Unmarshal(data, &temp); err != nil {
		return fmt.Errorf("failed to unmarshal config data: %w", err)
	}

	// Set simple fields
	cd.Client = temp.Client
	cd.Providers = temp.Providers
	cd.MCP = temp.MCP
	cd.Governance = temp.Governance
	cd.Plugins = temp.Plugins

	// Parse VectorStoreConfig using its internal unmarshaler
	if len(temp.VectorStoreConfig) > 0 {
		var vectorStoreConfig vectorstore.Config
		if err := json.Unmarshal(temp.VectorStoreConfig, &vectorStoreConfig); err != nil {
			return fmt.Errorf("failed to unmarshal vector store config: %w", err)
		}
		cd.VectorStoreConfig = &vectorStoreConfig
	}

	// Parse ConfigStoreConfig using its internal unmarshaler
	if len(temp.ConfigStoreConfig) > 0 {
		var configStoreConfig configstore.Config
		if err := json.Unmarshal(temp.ConfigStoreConfig, &configStoreConfig); err != nil {
			return fmt.Errorf("failed to unmarshal config store config: %w", err)
		}
		cd.ConfigStoreConfig = &configStoreConfig
	}

	// Parse LogsStoreConfig using its internal unmarshaler
	if len(temp.LogsStoreConfig) > 0 {
		var logsStoreConfig logstore.Config
		if err := json.Unmarshal(temp.LogsStoreConfig, &logsStoreConfig); err != nil {
			return fmt.Errorf("failed to unmarshal logs store config: %w", err)
		}
		cd.LogsStoreConfig = &logsStoreConfig
	}
	return nil
}

// Config represents a high-performance in-memory configuration store for Bifrost.
// It provides thread-safe access to provider configurations with database persistence.
//
// Features:
//   - Pure in-memory storage for ultra-fast access
//   - Environment variable processing for API keys and key-level configurations
//   - Thread-safe operations with read-write mutexes
//   - Real-time configuration updates via HTTP API
//   - Automatic database persistence for all changes
//   - Support for provider-specific key configurations (Azure, Vertex, Bedrock)
type Config struct {
	mu     sync.RWMutex
	muMCP  sync.RWMutex
	client *bifrost.Bifrost

	configPath string

	// Stores
	ConfigStore configstore.ConfigStore
	VectorStore vectorstore.VectorStore
	LogsStore   logstore.LogStore

	// In-memory storage
	ClientConfig     configstore.ClientConfig
	Providers        map[schemas.ModelProvider]configstore.ProviderConfig
	MCPConfig        *schemas.MCPConfig
	GovernanceConfig *configstore.GovernanceConfig

	// Track which keys come from environment variables
	EnvKeys map[string][]configstore.EnvKeyInfo

	// Plugin configs
	Plugins []*schemas.PluginConfig
}

var DefaultClientConfig = configstore.ClientConfig{
	DropExcessRequests:      false,
	PrometheusLabels:        []string{},
	InitialPoolSize:         300,
	EnableLogging:           true,
	EnableGovernance:        true,
	EnforceGovernanceHeader: false,
	AllowDirectKeys:         false,
	AllowedOrigins:          []string{},
	MaxRequestBodySizeMB:    100,
}

// LoadConfig loads initial configuration from a JSON config file into memory
// with full preprocessing including environment variable resolution and key config parsing.
// All processing is done upfront to ensure zero latency when retrieving data.
//
// If the config file doesn't exist, the system starts with default configuration
// and users can add providers dynamically via the HTTP API.
//
// This method handles:
//   - JSON config file parsing
//   - Environment variable substitution for API keys (env.VARIABLE_NAME)
//   - Key-level config processing for Azure, Vertex, and Bedrock (Endpoint, APIVersion, ProjectID, Region, AuthCredentials)
//   - Case conversion for provider names (e.g., "OpenAI" -> "openai")
//   - In-memory storage for ultra-fast access during request processing
//   - Graceful handling of missing config files
func LoadConfig(ctx context.Context, configDirPath string) (*Config, error) {
	// Initialize separate database connections for optimal performance at scale
	configFilePath := filepath.Join(configDirPath, "config.json")
	configDBPath := filepath.Join(configDirPath, "config.db")
	logsDBPath := filepath.Join(configDirPath, "logs.db")

	config := &Config{
		configPath: configFilePath,
		EnvKeys:    make(map[string][]configstore.EnvKeyInfo),
		Providers:  make(map[schemas.ModelProvider]configstore.ProviderConfig),
	}

	// Check if config file exists
	data, err := os.ReadFile(configFilePath)
	if err != nil {
		// If config file doesn't exist, we will directly use the config store (create one if it doesn't exist)
		if os.IsNotExist(err) {
			logger.Info("config file not found at path: %s, initializing with default values", configFilePath)
			// Initializing with default values
			config.ConfigStore, err = configstore.NewConfigStore(&configstore.Config{
				Enabled: true,
				Type:    configstore.ConfigStoreTypeSQLite,
				Config: &configstore.SQLiteConfig{
					Path: configDBPath,
				},
			}, logger)
			if err != nil {
				return nil, fmt.Errorf("failed to initialize config store: %w", err)
			}
			// Checking if client config already exist
			clientConfig, err := config.ConfigStore.GetClientConfig()
			if err != nil {
				return nil, fmt.Errorf("failed to get client config: %w", err)
			}
			if clientConfig == nil {
				clientConfig = &DefaultClientConfig
			} else {
				// For backward compatibility, we need to handle cases where config is already present but max request body size is not set
				if clientConfig.MaxRequestBodySizeMB == 0 {
					clientConfig.MaxRequestBodySizeMB = DefaultClientConfig.MaxRequestBodySizeMB
				}
			}
			err = config.ConfigStore.UpdateClientConfig(clientConfig)
			if err != nil {
				return nil, fmt.Errorf("failed to update client config: %w", err)
			}
			config.ClientConfig = *clientConfig
			// Checking if log store config already exist
			logStoreConfig, err := config.ConfigStore.GetLogsStoreConfig()
			if err != nil {
				return nil, fmt.Errorf("failed to get logs store config: %w", err)
			}
			logger.Debug("log store config from DB: %v", logStoreConfig)
			if logStoreConfig == nil {
				logStoreConfig = &logstore.Config{
					Enabled: true,
					Type:    logstore.LogStoreTypeSQLite,
					Config: &logstore.SQLiteConfig{
						Path: logsDBPath,
					},
				}
			}
			logger.Info("config store initialized; initializing logs store.")
			config.LogsStore, err = logstore.NewLogStore(logStoreConfig, logger)
			if err != nil {
				return nil, fmt.Errorf("failed to initialize logs store: %v", err)
			}
			err = config.ConfigStore.UpdateLogsStoreConfig(logStoreConfig)
			if err != nil {
				return nil, fmt.Errorf("failed to update logs store config: %w", err)
			}
			// No providers in database, auto-detect from environment
			providers, err := config.ConfigStore.GetProvidersConfig()
			if err != nil {
				return nil, fmt.Errorf("failed to get providers config: %w", err)
			}
			if providers == nil {
				config.autoDetectProviders()
				providers = config.Providers
				// Store providers config in database
				err = config.ConfigStore.UpdateProvidersConfig(providers)
				if err != nil {
					return nil, fmt.Errorf("failed to update providers config: %w", err)
				}
			} else {
				processedProviders := make(map[schemas.ModelProvider]configstore.ProviderConfig)
				for providerKey, dbProvider := range providers {
					provider := schemas.ModelProvider(providerKey)
					// Convert database keys to schemas.Key
					keys := make([]schemas.Key, len(dbProvider.Keys))
					for i, dbKey := range dbProvider.Keys {
						keys[i] = schemas.Key{
							ID:               dbKey.ID, // Key ID is passed in dbKey, not ID
							Value:            dbKey.Value,
							Models:           dbKey.Models,
							Weight:           dbKey.Weight,
							AzureKeyConfig:   dbKey.AzureKeyConfig,
							VertexKeyConfig:  dbKey.VertexKeyConfig,
							BedrockKeyConfig: dbKey.BedrockKeyConfig,
						}

					}
					providerConfig := configstore.ProviderConfig{
						Keys:                     keys,
						NetworkConfig:            dbProvider.NetworkConfig,
						ConcurrencyAndBufferSize: dbProvider.ConcurrencyAndBufferSize,
						ProxyConfig:              dbProvider.ProxyConfig,
						SendBackRawResponse:      dbProvider.SendBackRawResponse,
						CustomProviderConfig:     dbProvider.CustomProviderConfig,
					}
					if err := ValidateCustomProvider(providerConfig, provider); err != nil {
						logger.Warn("invalid custom provider config for %s: %v", provider, err)
						continue
					}
					processedProviders[provider] = providerConfig
				}
				config.Providers = processedProviders
			}
			// Checking if MCP config already exists
			mcpConfig, err := config.ConfigStore.GetMCPConfig()
			if err != nil {
				return nil, fmt.Errorf("failed to get MCP config: %w", err)
			}
			if mcpConfig == nil {
				if err := config.processMCPEnvVars(); err != nil {
					logger.Warn("failed to process MCP env vars: %v", err)
				}
				if err := config.ConfigStore.UpdateMCPConfig(config.MCPConfig, config.EnvKeys); err != nil {
					return nil, fmt.Errorf("failed to update MCP config: %w", err)
				}
				// Refresh from store to ensure parity with persisted state
				if mcpConfig, err = config.ConfigStore.GetMCPConfig(); err != nil {
					return nil, fmt.Errorf("failed to get MCP config after update: %w", err)
				}
				config.MCPConfig = mcpConfig
			} else {
				// Use the saved config from the store
				config.MCPConfig = mcpConfig
			}
			// Checking if plugins already exist
			plugins, err := config.ConfigStore.GetPlugins()
			if err != nil {
				return nil, fmt.Errorf("failed to get plugins: %w", err)
			}
			if plugins == nil {
				config.Plugins = []*schemas.PluginConfig{}
			} else {
				config.Plugins = make([]*schemas.PluginConfig, len(plugins))
				for i, plugin := range plugins {
					pluginConfig := &schemas.PluginConfig{
						Name:    plugin.Name,
						Enabled: plugin.Enabled,
						Config:  plugin.Config,
					}
					if plugin.Name == semanticcache.PluginName {
						if err := config.AddProviderKeysToSemanticCacheConfig(pluginConfig); err != nil {
							logger.Warn("failed to add provider keys to semantic cache config: %v", err)
						}
					}
					config.Plugins[i] = pluginConfig
				}
			}
			// Load environment variable tracking
			var dbEnvKeys map[string][]configstore.EnvKeyInfo
			if dbEnvKeys, err = config.ConfigStore.GetEnvKeys(); err != nil {
				return nil, err
			}
			config.EnvKeys = make(map[string][]configstore.EnvKeyInfo)
			for envVar, dbEnvKey := range dbEnvKeys {
				for _, dbEnvKey := range dbEnvKey {
					config.EnvKeys[envVar] = append(config.EnvKeys[envVar], configstore.EnvKeyInfo{
						EnvVar:     dbEnvKey.EnvVar,
						Provider:   dbEnvKey.Provider,
						KeyType:    dbEnvKey.KeyType,
						ConfigPath: dbEnvKey.ConfigPath,
						KeyID:      dbEnvKey.KeyID,
					})
				}
			}
			err = config.ConfigStore.UpdateEnvKeys(config.EnvKeys)
			if err != nil {
				return nil, fmt.Errorf("failed to update env keys: %w", err)
			}
			return config, nil
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// If config file exists, we will use it to only bootstrap config tables.

	logger.Info("loading configuration from: %s", configFilePath)

	var configData ConfigData
	if err := json.Unmarshal(data, &configData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Initializing config store
	if configData.ConfigStoreConfig != nil && configData.ConfigStoreConfig.Enabled {
		config.ConfigStore, err = configstore.NewConfigStore(configData.ConfigStoreConfig, logger)
		if err != nil {
			return nil, err
		}
		logger.Info("config store initialized")
	}

	// Initializing log store
	if configData.LogsStoreConfig != nil && configData.LogsStoreConfig.Enabled {
		config.LogsStore, err = logstore.NewLogStore(configData.LogsStoreConfig, logger)
		if err != nil {
			return nil, err
		}
		logger.Info("logs store initialized")
	}

	// Initializing vector store
	if configData.VectorStoreConfig != nil && configData.VectorStoreConfig.Enabled {
		logger.Info("connecting to vectorstore")
		// Checking type of the store
		config.VectorStore, err = vectorstore.NewVectorStore(ctx, configData.VectorStoreConfig, logger)
		if err != nil {
			logger.Fatal("failed to connect to vector store: %v", err)
		}
		if config.ConfigStore != nil {
			err = config.ConfigStore.UpdateVectorStoreConfig(configData.VectorStoreConfig)
			if err != nil {
				logger.Warn("failed to update vector store config: %v", err)
			}
		}
	}

	// From now on, config store gets the priority if enabled and we find data
	// if we don't find any data in the store, then we resort to config file

	//NOTE: We follow a standard practice here to first look in store -> not present then use config file -> if present in config file then update store.

	// 1. Check for Client Config

	var clientConfig *configstore.ClientConfig
	if config.ConfigStore != nil {
		clientConfig, err = config.ConfigStore.GetClientConfig()
		if err != nil {
			logger.Warn("failed to get client config from store: %v", err)
		}
	}

	if clientConfig != nil {
		config.ClientConfig = *clientConfig

		// For backward compatibility, we need to handle cases where config is already present but max request body size is not set
		if config.ClientConfig.MaxRequestBodySizeMB == 0 {
			config.ClientConfig.MaxRequestBodySizeMB = DefaultClientConfig.MaxRequestBodySizeMB
		}
	} else {
		logger.Debug("client config not found in store, using config file")
		// Process core configuration if present, otherwise use defaults
		if configData.Client != nil {
			config.ClientConfig = *configData.Client

			// For backward compatibility, we need to handle cases where config is already present but max request body size is not set
			if config.ClientConfig.MaxRequestBodySizeMB == 0 {
				config.ClientConfig.MaxRequestBodySizeMB = DefaultClientConfig.MaxRequestBodySizeMB
			}
		} else {
			config.ClientConfig = DefaultClientConfig
		}

		if config.ConfigStore != nil {
			logger.Debug("updating client config in store")
			err = config.ConfigStore.UpdateClientConfig(&config.ClientConfig)
			if err != nil {
				logger.Warn("failed to update client config: %v", err)
			}
		}
	}

	// 2. Check for Providers

	var processedProviders map[schemas.ModelProvider]configstore.ProviderConfig
	if config.ConfigStore != nil {
		logger.Debug("getting providers config from store")
		processedProviders, err = config.ConfigStore.GetProvidersConfig()
		if err != nil {
			logger.Warn("failed to get providers config from store: %v", err)
		}
	}

	if processedProviders != nil {
		config.Providers = processedProviders
	} else {
		// If we don't have any data in the store, we will process the data from the config file
		logger.Debug("no providers config found in store, processing from config file")
		processedProviders = make(map[schemas.ModelProvider]configstore.ProviderConfig)
		// Process provider configurations
		if configData.Providers != nil {
			// Process each provider configuration
			for providerName, cfg := range configData.Providers {
				newEnvKeys := make(map[string]struct{})
				provider := schemas.ModelProvider(strings.ToLower(providerName))

				// Process environment variables in keys (including key-level configs)
				for i, key := range cfg.Keys {
					if key.ID == "" {
						cfg.Keys[i].ID = uuid.NewString()
					}

					// Process API key value
					processedValue, envVar, err := config.processEnvValue(key.Value)
					if err != nil {
						config.cleanupEnvKeys(provider, "", newEnvKeys)
						if strings.Contains(err.Error(), "not found") {
							logger.Info("%s: %v", provider, err)
						} else {
							logger.Warn("failed to process env vars in keys for %s: %v", provider, err)
						}
						continue
					}
					cfg.Keys[i].Value = processedValue

					// Track environment key if it came from env
					if envVar != "" {
						newEnvKeys[envVar] = struct{}{}
						config.EnvKeys[envVar] = append(config.EnvKeys[envVar], configstore.EnvKeyInfo{
							EnvVar:     envVar,
							Provider:   provider,
							KeyType:    "api_key",
							ConfigPath: fmt.Sprintf("providers.%s.keys[%s]", provider, key.ID),
							KeyID:      key.ID,
						})
					}

					// Process Azure key config if present
					if key.AzureKeyConfig != nil {
						if err := config.processAzureKeyConfigEnvVars(&cfg.Keys[i], provider, i, newEnvKeys); err != nil {
							config.cleanupEnvKeys(provider, "", newEnvKeys)
							logger.Warn("failed to process Azure key config env vars for %s: %v", provider, err)
							continue
						}
					}

					// Process Vertex key config if present
					if key.VertexKeyConfig != nil {
						if err := config.processVertexKeyConfigEnvVars(&cfg.Keys[i], provider, i, newEnvKeys); err != nil {
							config.cleanupEnvKeys(provider, "", newEnvKeys)
							logger.Warn("failed to process Vertex key config env vars for %s: %v", provider, err)
							continue
						}
					}

					// Process Bedrock key config if present
					if key.BedrockKeyConfig != nil {
						if err := config.processBedrockKeyConfigEnvVars(&cfg.Keys[i], provider, i, newEnvKeys); err != nil {
							config.cleanupEnvKeys(provider, "", newEnvKeys)
							logger.Warn("failed to process Bedrock key config env vars for %s: %v", provider, err)
							continue
						}
					}
				}
				processedProviders[provider] = cfg
			}
			// Store processed configurations in memory
			config.Providers = processedProviders
		} else {
			config.autoDetectProviders()
		}
		if config.ConfigStore != nil {
			logger.Debug("updating providers config in store")
			err = config.ConfigStore.UpdateProvidersConfig(processedProviders)
			if err != nil {
				logger.Warn("failed to update providers config: %v", err)
			}
			if err := config.ConfigStore.UpdateEnvKeys(config.EnvKeys); err != nil {
				logger.Warn("failed to update env keys: %v", err)
			}
		}
	}

	// 3. Check for MCP Config

	var mcpConfig *schemas.MCPConfig
	if config.ConfigStore != nil {
		logger.Debug("getting MCP config from store")
		mcpConfig, err = config.ConfigStore.GetMCPConfig()
		if err != nil {
			logger.Warn("failed to get MCP config from store: %v", err)
		}
	}

	if mcpConfig != nil {
		config.MCPConfig = mcpConfig
	} else if configData.MCP != nil {
		// If MCP config is not present in the store, we will use the config file
		logger.Debug("no MCP config found in store, processing from config file")
		config.MCPConfig = configData.MCP
		if err := config.processMCPEnvVars(); err != nil {
			logger.Warn("failed to process MCP env vars: %v", err)
		}
		if config.ConfigStore != nil {
			logger.Debug("updating MCP config in store")
			err = config.ConfigStore.UpdateMCPConfig(config.MCPConfig, config.EnvKeys)
			if err != nil {
				logger.Warn("failed to update MCP config: %v", err)
			}
		}
	}

	// 4. Check for Governance Config

	var governanceConfig *configstore.GovernanceConfig
	if config.ConfigStore != nil {
		logger.Debug("getting governance config from store")
		governanceConfig, err = config.ConfigStore.GetGovernanceConfig()
		if err != nil {
			logger.Warn("failed to get governance config from store: %v", err)
		}
	}

	if governanceConfig != nil {
		config.GovernanceConfig = governanceConfig
	} else if configData.Governance != nil {
		logger.Debug("no governance config found in store, processing from config file")
		config.GovernanceConfig = configData.Governance

		if config.ConfigStore != nil {
			logger.Debug("updating governance config in store")
			if err := config.ConfigStore.ExecuteTransaction(func(tx *gorm.DB) error {
				// Create budgets
				for _, budget := range config.GovernanceConfig.Budgets {
					if err := config.ConfigStore.CreateBudget(&budget, tx); err != nil {
						return fmt.Errorf("failed to create budget %s: %w", budget.ID, err)
					}
				}

				// Create rate limits
				for _, rateLimit := range config.GovernanceConfig.RateLimits {
					if err := config.ConfigStore.CreateRateLimit(&rateLimit, tx); err != nil {
						return fmt.Errorf("failed to create rate limit %s: %w", rateLimit.ID, err)
					}
				}

				// Create customers
				for _, customer := range config.GovernanceConfig.Customers {
					if err := config.ConfigStore.CreateCustomer(&customer, tx); err != nil {
						return fmt.Errorf("failed to create customer %s: %w", customer.ID, err)
					}
				}

				// Create teams
				for _, team := range config.GovernanceConfig.Teams {
					if err := config.ConfigStore.CreateTeam(&team, tx); err != nil {
						return fmt.Errorf("failed to create team %s: %w", team.ID, err)
					}
				}

				// Create virtual keys
				for _, virtualKey := range config.GovernanceConfig.VirtualKeys {
					// Look up existing provider keys by key_id and populate the Keys field
					var existingKeys []configstore.TableKey
					for _, keyRef := range virtualKey.Keys {
						if keyRef.KeyID != "" {
							var existingKey configstore.TableKey
							if err := tx.Where("key_id = ?", keyRef.KeyID).First(&existingKey).Error; err != nil {
								if err == gorm.ErrRecordNotFound {
									logger.Warn("referenced key %s not found for virtual key %s", keyRef.KeyID, virtualKey.ID)
									continue
								}
								return fmt.Errorf("failed to lookup key %s for virtual key %s: %w", keyRef.KeyID, virtualKey.ID, err)
							}
							existingKeys = append(existingKeys, existingKey)
						}
					}
					virtualKey.Keys = existingKeys

					if err := config.ConfigStore.CreateVirtualKey(&virtualKey, tx); err != nil {
						return fmt.Errorf("failed to create virtual key %s: %w", virtualKey.ID, err)
					}
				}

				return nil
			}); err != nil {
				logger.Warn("failed to update governance config: %v", err)
			}
		}
	}

	// 5. Check for Plugins

	if config.ConfigStore != nil {
		logger.Debug("getting plugins from store")
		plugins, err := config.ConfigStore.GetPlugins()
		if err != nil {
			logger.Warn("failed to get plugins from store: %v", err)
		}
		if plugins != nil {
			config.Plugins = make([]*schemas.PluginConfig, len(plugins))
			for i, plugin := range plugins {
				pluginConfig := &schemas.PluginConfig{
					Name:    plugin.Name,
					Enabled: plugin.Enabled,
					Config:  plugin.Config,
				}
				if plugin.Name == semanticcache.PluginName {
					if err := config.AddProviderKeysToSemanticCacheConfig(pluginConfig); err != nil {
						logger.Warn("failed to add provider keys to semantic cache config: %v", err)
					}
				}
				config.Plugins[i] = pluginConfig
			}
		}
	}

	// If plugins are not present in the store, we will use the config file
	if len(config.Plugins) == 0 && len(configData.Plugins) > 0 {
		logger.Debug("no plugins found in store, processing from config file")
		config.Plugins = configData.Plugins

		for i, plugin := range config.Plugins {
			if plugin.Name == semanticcache.PluginName {
				if err := config.AddProviderKeysToSemanticCacheConfig(plugin); err != nil {
					logger.Warn("failed to add provider keys to semantic cache config: %v", err)
				}
				config.Plugins[i] = plugin
			}
		}

		if config.ConfigStore != nil {
			logger.Debug("updating plugins in store")
			for _, plugin := range config.Plugins {
				pluginConfigCopy, err := DeepCopy(plugin.Config)
				if err != nil {
					logger.Warn("failed to deep copy plugin config, skipping database update: %v", err)
					continue
				}

				pluginConfig := &configstore.TablePlugin{
					Name:    plugin.Name,
					Enabled: plugin.Enabled,
					Config:  pluginConfigCopy,
				}
				if plugin.Name == semanticcache.PluginName {
					if err := config.RemoveProviderKeysFromSemanticCacheConfig(pluginConfig); err != nil {
						logger.Warn("failed to remove provider keys from semantic cache config: %v", err)
					}
				}
				if err := config.ConfigStore.CreatePlugin(pluginConfig); err != nil {
					logger.Warn("failed to update plugin: %v", err)
				}
			}
		}
	}

	// 6. Check for Env Keys in config store

	// Initialize env keys
	if config.ConfigStore != nil {
		envKeys, err := config.ConfigStore.GetEnvKeys()
		if err != nil {
			logger.Warn("failed to get env keys from store: %v", err)
		}
		config.EnvKeys = envKeys
	}

	if config.EnvKeys == nil {
		config.EnvKeys = make(map[string][]configstore.EnvKeyInfo)
	}

	logger.Info("successfully loaded configuration")
	return config, nil
}

// GetRawConfigString returns the raw configuration string.
func (s *Config) GetRawConfigString() string {
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// processEnvValue checks and replaces environment variable references in configuration values.
// Returns the processed value and the environment variable name if it was an env reference.
// Supports the "env.VARIABLE_NAME" syntax for referencing environment variables.
// This enables secure configuration management without hardcoding sensitive values.
//
// Examples:
//   - "env.OPENAI_API_KEY" -> actual value from OPENAI_API_KEY environment variable
//   - "sk-1234567890" -> returned as-is (no env prefix)
func (s *Config) processEnvValue(value string) (string, string, error) {
	v := strings.TrimSpace(value)
	if !strings.HasPrefix(v, "env.") {
		return value, "", nil // do not trim non-env values
	}
	envKey := strings.TrimSpace(strings.TrimPrefix(v, "env."))
	if envKey == "" {
		return "", "", fmt.Errorf("environment variable name missing in %q", value)
	}
	if envValue, ok := os.LookupEnv(envKey); ok {
		return envValue, envKey, nil
	}
	return "", envKey, fmt.Errorf("environment variable %s not found", envKey)
}

// getRestoredMCPConfig creates a copy of MCP config with env variable references restored
func (s *Config) getRestoredMCPConfig(envVarsByPath map[string]string) *schemas.MCPConfig {
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

// GetProviderConfigRaw retrieves the raw, unredacted provider configuration from memory.
// This method is for internal use only, particularly by the account implementation.
//
// Performance characteristics:
//   - Memory access: ultra-fast direct memory access
//   - No database I/O or JSON parsing overhead
//   - Thread-safe with read locks for concurrent access
//
// Returns a copy of the configuration to prevent external modifications.
func (s *Config) GetProviderConfigRaw(provider schemas.ModelProvider) (*configstore.ProviderConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	config, exists := s.Providers[provider]
	if !exists {
		return nil, ErrNotFound
	}

	// Return direct reference for maximum performance - this is used by Bifrost core
	// CRITICAL: Never modify the returned data as it's shared
	return &config, nil
}

// HandlerStore interface implementation

// ShouldAllowDirectKeys returns whether direct API keys in headers are allowed
// Note: This method doesn't use locking for performance. In rare cases during
// config updates, it may return stale data, but this is acceptable since bool
// reads are atomic and won't cause panics.
func (s *Config) ShouldAllowDirectKeys() bool {
	return s.ClientConfig.AllowDirectKeys
}

// GetProviderConfigRedacted retrieves a provider configuration with sensitive values redacted.
// This method is intended for external API responses and logging.
//
// The returned configuration has sensitive values redacted:
// - API keys are redacted using RedactKey()
// - Values from environment variables show the original env var name (env.VAR_NAME)
//
// Returns a new copy with redacted values that is safe to expose externally.
func (s *Config) GetProviderConfigRedacted(provider schemas.ModelProvider) (*configstore.ProviderConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	config, exists := s.Providers[provider]
	if !exists {
		return nil, ErrNotFound
	}

	// Create a map for quick lookup of env vars for this provider
	envVarsByPath := make(map[string]string)
	for envVar, infos := range s.EnvKeys {
		for _, info := range infos {
			if info.Provider == provider {
				envVarsByPath[info.ConfigPath] = envVar
			}
		}
	}

	// Create redacted config with same structure but redacted values
	redactedConfig := configstore.ProviderConfig{
		NetworkConfig:            config.NetworkConfig,
		ConcurrencyAndBufferSize: config.ConcurrencyAndBufferSize,
		ProxyConfig:              config.ProxyConfig,
		SendBackRawResponse:      config.SendBackRawResponse,
		CustomProviderConfig:     config.CustomProviderConfig,
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
		} else if !strings.HasPrefix(key.Value, "env.") {
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
			} else if !strings.HasPrefix(key.AzureKeyConfig.Endpoint, "env.") {
				azureConfig.Endpoint = RedactKey(key.AzureKeyConfig.Endpoint)
			}

			// Redact APIVersion if present
			if key.AzureKeyConfig.APIVersion != nil {
				path = fmt.Sprintf("providers.%s.keys[%s].azure_key_config.api_version", provider, key.ID)
				if envVar, ok := envVarsByPath[path]; ok {
					azureConfig.APIVersion = bifrost.Ptr("env." + envVar)
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
			} else if !strings.HasPrefix(key.VertexKeyConfig.ProjectID, "env.") {
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
			} else if !strings.HasPrefix(key.VertexKeyConfig.AuthCredentials, "env.") {
				vertexConfig.AuthCredentials = RedactKey(key.VertexKeyConfig.AuthCredentials)
			}

			redactedConfig.Keys[i].VertexKeyConfig = vertexConfig
		}

		// Redact Bedrock key config if present
		if key.BedrockKeyConfig != nil {
			bedrockConfig := &schemas.BedrockKeyConfig{
				Deployments: key.BedrockKeyConfig.Deployments,
			}

			// Redact AccessKey
			path = fmt.Sprintf("providers.%s.keys[%s].bedrock_key_config.access_key", provider, key.ID)
			if envVar, ok := envVarsByPath[path]; ok {
				bedrockConfig.AccessKey = "env." + envVar
			} else if !strings.HasPrefix(key.BedrockKeyConfig.AccessKey, "env.") {
				bedrockConfig.AccessKey = RedactKey(key.BedrockKeyConfig.AccessKey)
			}

			// Redact SecretKey
			path = fmt.Sprintf("providers.%s.keys[%s].bedrock_key_config.secret_key", provider, key.ID)
			if envVar, ok := envVarsByPath[path]; ok {
				bedrockConfig.SecretKey = "env." + envVar
			} else if !strings.HasPrefix(key.BedrockKeyConfig.SecretKey, "env.") {
				bedrockConfig.SecretKey = RedactKey(key.BedrockKeyConfig.SecretKey)
			}

			// Redact SessionToken
			path = fmt.Sprintf("providers.%s.keys[%s].bedrock_key_config.session_token", provider, key.ID)
			if envVar, ok := envVarsByPath[path]; ok {
				bedrockConfig.SessionToken = bifrost.Ptr("env." + envVar)
			} else {
				bedrockConfig.SessionToken = key.BedrockKeyConfig.SessionToken
			}

			// Redact Region
			path = fmt.Sprintf("providers.%s.keys[%s].bedrock_key_config.region", provider, key.ID)
			if envVar, ok := envVarsByPath[path]; ok {
				bedrockConfig.Region = bifrost.Ptr("env." + envVar)
			} else {
				bedrockConfig.Region = key.BedrockKeyConfig.Region
			}

			// Redact ARN
			path = fmt.Sprintf("providers.%s.keys[%s].bedrock_key_config.arn", provider, key.ID)
			if envVar, ok := envVarsByPath[path]; ok {
				bedrockConfig.ARN = bifrost.Ptr("env." + envVar)
			} else {
				bedrockConfig.ARN = key.BedrockKeyConfig.ARN
			}

			redactedConfig.Keys[i].BedrockKeyConfig = bedrockConfig
		}
	}

	return &redactedConfig, nil
}

// GetAllProviders returns all configured provider names.
func (s *Config) GetAllProviders() ([]schemas.ModelProvider, error) {
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
//   - Processes environment variables in API keys, and key-level configs
//   - Stores the processed configuration in memory
//   - Updates metadata and timestamps
func (s *Config) AddProvider(provider schemas.ModelProvider, config configstore.ProviderConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if provider already exists
	if _, exists := s.Providers[provider]; exists {
		return fmt.Errorf("provider %s already exists", provider)
	}

	// Validate CustomProviderConfig if present
	if err := ValidateCustomProvider(config, provider); err != nil {
		return err
	}
	newEnvKeys := make(map[string]struct{})

	// Process environment variables in keys (including key-level configs)
	for i, key := range config.Keys {
		if key.ID == "" {
			config.Keys[i].ID = uuid.NewString()
		}

		// Process API key value
		processedValue, envVar, err := s.processEnvValue(key.Value)
		if err != nil {
			s.cleanupEnvKeys(provider, "", newEnvKeys)
			return fmt.Errorf("failed to process env var in key: %w", err)
		}
		config.Keys[i].Value = processedValue

		// Track environment key if it came from env
		if envVar != "" {
			newEnvKeys[envVar] = struct{}{}
			s.EnvKeys[envVar] = append(s.EnvKeys[envVar], configstore.EnvKeyInfo{
				EnvVar:     envVar,
				Provider:   provider,
				KeyType:    "api_key",
				ConfigPath: fmt.Sprintf("providers.%s.keys[%s]", provider, config.Keys[i].ID),
				KeyID:      config.Keys[i].ID,
			})
		}

		// Process Azure key config if present
		if key.AzureKeyConfig != nil {
			if err := s.processAzureKeyConfigEnvVars(&config.Keys[i], provider, i, newEnvKeys); err != nil {
				s.cleanupEnvKeys(provider, "", newEnvKeys)
				return fmt.Errorf("failed to process Azure key config env vars: %w", err)
			}
		}

		// Process Vertex key config if present
		if key.VertexKeyConfig != nil {
			if err := s.processVertexKeyConfigEnvVars(&config.Keys[i], provider, i, newEnvKeys); err != nil {
				s.cleanupEnvKeys(provider, "", newEnvKeys)
				return fmt.Errorf("failed to process Vertex key config env vars: %w", err)
			}
		}

		// Process Bedrock key config if present
		if key.BedrockKeyConfig != nil {
			if err := s.processBedrockKeyConfigEnvVars(&config.Keys[i], provider, i, newEnvKeys); err != nil {
				s.cleanupEnvKeys(provider, "", newEnvKeys)
				return fmt.Errorf("failed to process Bedrock key config env vars: %w", err)
			}
		}
	}

	s.Providers[provider] = config

	if s.ConfigStore != nil {
		if err := s.ConfigStore.AddProvider(provider, config, s.EnvKeys); err != nil {
			return fmt.Errorf("failed to update provider config in store: %w", err)
		}
		if err := s.ConfigStore.UpdateEnvKeys(s.EnvKeys); err != nil {
			logger.Warn("failed to update env keys: %v", err)
		}
	}

	logger.Info("added provider: %s", provider)
	return nil
}

// UpdateProviderConfig updates a provider configuration in memory with full environment
// variable processing. This method is called when provider configurations are modified
// via the HTTP API and ensures all data processing is done upfront.
//
// The method:
//   - Processes environment variables in API keys, and key-level configs
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
func (s *Config) UpdateProviderConfig(provider schemas.ModelProvider, config configstore.ProviderConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get existing configuration for validation
	existingConfig, exists := s.Providers[provider]
	if !exists {
		return fmt.Errorf("provider %s not found", provider)
	}

	// Validate CustomProviderConfig if present, ensuring immutable fields are not changed
	if err := ValidateCustomProviderUpdate(config, existingConfig, provider); err != nil {
		return err
	}
	// Track new environment variables being added
	newEnvKeys := make(map[string]struct{})

	// Process environment variables in keys (including key-level configs)
	for i, key := range config.Keys {
		if key.ID == "" {
			config.Keys[i].ID = uuid.NewString()
		}

		// Process API key value
		processedValue, envVar, err := s.processEnvValue(key.Value)
		if err != nil {
			s.cleanupEnvKeys(provider, "", newEnvKeys) // Clean up only new vars on failure
			return fmt.Errorf("failed to process env var in key: %w", err)
		}
		config.Keys[i].Value = processedValue

		// Track environment key if it came from env
		if envVar != "" {
			newEnvKeys[envVar] = struct{}{}
			s.EnvKeys[envVar] = append(s.EnvKeys[envVar], configstore.EnvKeyInfo{
				EnvVar:     envVar,
				Provider:   provider,
				KeyType:    "api_key",
				ConfigPath: fmt.Sprintf("providers.%s.keys[%s]", provider, config.Keys[i].ID),
				KeyID:      config.Keys[i].ID,
			})
		}

		// Process Azure key config if present
		if key.AzureKeyConfig != nil {
			if err := s.processAzureKeyConfigEnvVars(&config.Keys[i], provider, i, newEnvKeys); err != nil {
				s.cleanupEnvKeys(provider, "", newEnvKeys)
				return fmt.Errorf("failed to process Azure key config env vars: %w", err)
			}
		}

		// Process Vertex key config if present
		if key.VertexKeyConfig != nil {
			if err := s.processVertexKeyConfigEnvVars(&config.Keys[i], provider, i, newEnvKeys); err != nil {
				s.cleanupEnvKeys(provider, "", newEnvKeys)
				return fmt.Errorf("failed to process Vertex key config env vars: %w", err)
			}
		}

		// Process Bedrock key config if present
		if key.BedrockKeyConfig != nil {
			if err := s.processBedrockKeyConfigEnvVars(&config.Keys[i], provider, i, newEnvKeys); err != nil {
				s.cleanupEnvKeys(provider, "", newEnvKeys)
				return fmt.Errorf("failed to process Bedrock key config env vars: %w", err)
			}
		}
	}

	s.Providers[provider] = config

	if s.ConfigStore != nil {
		if err := s.ConfigStore.UpdateProvider(provider, config, s.EnvKeys); err != nil {
			return fmt.Errorf("failed to update provider config in store: %w", err)
		}
		if err := s.ConfigStore.UpdateEnvKeys(s.EnvKeys); err != nil {
			logger.Warn("failed to update env keys: %v", err)
		}
	}

	logger.Info("Updated configuration for provider: %s", provider)
	return nil
}

// RemoveProvider removes a provider configuration from memory.
func (s *Config) RemoveProvider(provider schemas.ModelProvider) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.Providers[provider]; !exists {
		return fmt.Errorf("provider %s not found", provider)
	}

	delete(s.Providers, provider)
	s.cleanupEnvKeys(provider, "", nil)

	if s.ConfigStore != nil {
		if err := s.ConfigStore.DeleteProvider(provider); err != nil {
			return fmt.Errorf("failed to update provider config in store: %w", err)
		}
		if err := s.ConfigStore.UpdateEnvKeys(s.EnvKeys); err != nil {
			logger.Warn("failed to update env keys: %v", err)
		}
	}

	logger.Info("Removed provider: %s", provider)
	return nil
}

// GetAllKeys returns the redacted keys
func (s *Config) GetAllKeys() ([]configstore.TableKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys := make([]configstore.TableKey, 0)
	for providerKey, provider := range s.Providers {
		for _, key := range provider.Keys {
			keys = append(keys, configstore.TableKey{
				KeyID:    key.ID,
				Value:    "",
				Models:   key.Models,
				Weight:   key.Weight,
				Provider: string(providerKey),
			})
		}
	}

	return keys, nil
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
func (s *Config) processMCPEnvVars() error {
	if s.MCPConfig == nil {
		return nil
	}

	var missingEnvVars []string

	// Process each client config
	for i, clientConfig := range s.MCPConfig.ClientConfigs {
		// Process ConnectionString if present
		if clientConfig.ConnectionString != nil {
			newValue, envVar, err := s.processEnvValue(*clientConfig.ConnectionString)
			if err != nil {
				logger.Warn("failed to process env vars in MCP client %s: %v", clientConfig.Name, err)
				missingEnvVars = append(missingEnvVars, envVar)
				continue
			}
			if envVar != "" {
				s.EnvKeys[envVar] = append(s.EnvKeys[envVar], configstore.EnvKeyInfo{
					EnvVar:     envVar,
					Provider:   "",
					KeyType:    "connection_string",
					ConfigPath: fmt.Sprintf("mcp.client_configs.%s.connection_string", clientConfig.Name),
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
func (s *Config) SetBifrostClient(client *bifrost.Bifrost) {
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
func (s *Config) AddMCPClient(clientConfig schemas.MCPClientConfig) error {
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
			s.EnvKeys[envVar] = append(s.EnvKeys[envVar], configstore.EnvKeyInfo{
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

	if s.ConfigStore != nil {
		if err := s.ConfigStore.UpdateMCPConfig(s.MCPConfig, s.EnvKeys); err != nil {
			return fmt.Errorf("failed to update MCP config in store: %w", err)
		}
		if err := s.ConfigStore.UpdateEnvKeys(s.EnvKeys); err != nil {
			logger.Warn("failed to update env keys: %v", err)
		}
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
func (s *Config) RemoveMCPClient(name string) error {
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

	if s.ConfigStore != nil {
		if err := s.ConfigStore.UpdateMCPConfig(s.MCPConfig, s.EnvKeys); err != nil {
			return fmt.Errorf("failed to update MCP config in store: %w", err)
		}
		if err := s.ConfigStore.UpdateEnvKeys(s.EnvKeys); err != nil {
			logger.Warn("failed to update env keys: %v", err)
		}
	}

	return nil
}

// EditMCPClientTools edits the tools of an MCP client.
// This allows for dynamic MCP client tool management at runtime.
//
// Parameters:
//   - name: Name of the client to edit
//   - toolsToAdd: Tools to add to the client
//   - toolsToRemove: Tools to remove from the client
func (s *Config) EditMCPClientTools(name string, toolsToAdd []string, toolsToRemove []string) error {
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

	if s.ConfigStore != nil {
		if err := s.ConfigStore.UpdateMCPConfig(s.MCPConfig, s.EnvKeys); err != nil {
			return fmt.Errorf("failed to update MCP config in store: %w", err)
		}
		if err := s.ConfigStore.UpdateEnvKeys(s.EnvKeys); err != nil {
			logger.Warn("failed to update env keys: %v", err)
		}
	}

	return nil
}

// RedactMCPClientConfig creates a redacted copy of an MCP client configuration.
// Connection strings are either redacted or replaced with their environment variable names.
func (s *Config) RedactMCPClientConfig(config schemas.MCPClientConfig) schemas.MCPClientConfig {
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

	if len(key) <= 8 {
		return strings.Count(key, "*") == len(key)
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
func (s *Config) cleanupEnvKeys(provider schemas.ModelProvider, mcpClientName string, envVarsToRemove map[string]struct{}) {
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
func (s *Config) cleanupEnvVar(envVar string, provider schemas.ModelProvider, mcpClientName string) {
	infos := s.EnvKeys[envVar]
	if len(infos) == 0 {
		return
	}

	// Keep entries that don't match the provider/client we're cleaning up
	filteredInfos := make([]configstore.EnvKeyInfo, 0, len(infos))
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
func (s *Config) CleanupEnvKeysForKeys(provider schemas.ModelProvider, keysToDelete []schemas.Key) {
	// Create a set of key IDs to delete for efficient lookup
	keyIDsToDelete := make(map[string]bool)
	for _, key := range keysToDelete {
		keyIDsToDelete[key.ID] = true
	}

	// Iterate through all environment variables and remove entries for deleted keys
	for envVar, infos := range s.EnvKeys {
		filteredInfos := make([]configstore.EnvKeyInfo, 0, len(infos))

		for _, info := range infos {
			// Keep entries that either:
			// 1. Don't belong to this provider, OR
			// 2. Don't have a KeyID (MCP), OR
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
// but only for fields where the environment variable reference has actually changed.
// This function is called after the merge to compare final values with original values.
//
// Parameters:
//   - provider: Provider name the keys belong to
//   - keysToUpdate: List of keys being updated
//   - oldKeys: List of original keys before update
//   - mergedKeys: List of final merged keys after update
func (s *Config) CleanupEnvKeysForUpdatedKeys(provider schemas.ModelProvider, keysToUpdate []schemas.Key, oldKeys []schemas.Key, mergedKeys []schemas.Key) {
	// Create maps for efficient lookup
	keysToUpdateMap := make(map[string]schemas.Key)
	for _, key := range keysToUpdate {
		keysToUpdateMap[key.ID] = key
	}

	oldKeysMap := make(map[string]schemas.Key)
	for _, key := range oldKeys {
		oldKeysMap[key.ID] = key
	}

	mergedKeysMap := make(map[string]schemas.Key)
	for _, key := range mergedKeys {
		mergedKeysMap[key.ID] = key
	}

	// Iterate through all environment variables and remove entries only for fields that are changing
	for envVar, infos := range s.EnvKeys {
		filteredInfos := make([]configstore.EnvKeyInfo, 0, len(infos))

		for _, info := range infos {
			// Keep entries that either:
			// 1. Don't belong to this provider, OR
			// 2. Don't have a KeyID (MCP), OR
			// 3. Have a KeyID that's not being updated, OR
			// 4. Have a KeyID that's being updated but the env var reference hasn't changed
			shouldKeep := info.Provider != provider ||
				info.KeyID == "" ||
				keysToUpdateMap[info.KeyID].ID == "" ||
				!s.isEnvVarReferenceChanging(mergedKeysMap[info.KeyID], oldKeysMap[info.KeyID], info.ConfigPath)

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

// isEnvVarReferenceChanging checks if an environment variable reference is changing between old and merged key
func (s *Config) isEnvVarReferenceChanging(mergedKey, oldKey schemas.Key, configPath string) bool {
	// Extract the field name from the config path
	// e.g., "providers.vertex.keys[123].vertex_key_config.project_id" -> "project_id"
	pathParts := strings.Split(configPath, ".")
	if len(pathParts) < 2 {
		return false
	}
	fieldName := pathParts[len(pathParts)-1]

	// Get the old and merged values for this field
	oldValue := s.getFieldValue(oldKey, fieldName)
	mergedValue := s.getFieldValue(mergedKey, fieldName)

	// If either value is an env var reference, check if they're different
	oldIsEnvVar := strings.HasPrefix(oldValue, "env.")
	mergedIsEnvVar := strings.HasPrefix(mergedValue, "env.")

	// If both are env vars, check if they reference the same variable
	if oldIsEnvVar && mergedIsEnvVar {
		return oldValue != mergedValue
	}

	// If one is env var and other isn't, or both are different types, it's changing
	return oldIsEnvVar != mergedIsEnvVar || oldValue != mergedValue
}

// getFieldValue extracts the value of a specific field from a key based on the field name
func (s *Config) getFieldValue(key schemas.Key, fieldName string) string {
	switch fieldName {
	case "project_id":
		if key.VertexKeyConfig != nil {
			return key.VertexKeyConfig.ProjectID
		}
	case "region":
		if key.VertexKeyConfig != nil {
			return key.VertexKeyConfig.Region
		}
	case "auth_credentials":
		if key.VertexKeyConfig != nil {
			return key.VertexKeyConfig.AuthCredentials
		}
	case "endpoint":
		if key.AzureKeyConfig != nil {
			return key.AzureKeyConfig.Endpoint
		}
	case "api_version":
		if key.AzureKeyConfig != nil && key.AzureKeyConfig.APIVersion != nil {
			return *key.AzureKeyConfig.APIVersion
		}
	case "access_key":
		if key.BedrockKeyConfig != nil {
			return key.BedrockKeyConfig.AccessKey
		}
	case "secret_key":
		if key.BedrockKeyConfig != nil {
			return key.BedrockKeyConfig.SecretKey
		}
	case "session_token":
		if key.BedrockKeyConfig != nil && key.BedrockKeyConfig.SessionToken != nil {
			return *key.BedrockKeyConfig.SessionToken
		}
	default:
		// For the main API key value
		if fieldName == "value" || strings.Contains(fieldName, "key") {
			return key.Value
		}
	}
	return ""
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
func (s *Config) autoDetectProviders() {
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
				providerConfig := configstore.ProviderConfig{
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
				s.EnvKeys[envVar] = append(s.EnvKeys[envVar], configstore.EnvKeyInfo{
					EnvVar:     envVar,
					Provider:   provider,
					KeyType:    "api_key",
					ConfigPath: fmt.Sprintf("providers.%s.keys[%s]", provider, keyID),
					KeyID:      keyID,
				})

				logger.Info("auto-detected %s provider from environment variable %s", provider, envVar)
				detectedCount++
				break // Only use the first found env var for each provider
			}
		}
	}

	if detectedCount > 0 {
		logger.Info("auto-configured %d provider(s) from environment variables", detectedCount)
		if s.ConfigStore != nil {
			if err := s.ConfigStore.UpdateProvidersConfig(s.Providers); err != nil {
				logger.Error("failed to update providers in store: %v", err)
			}
		}
	}
}

// processAzureKeyConfigEnvVars processes environment variables in Azure key configuration
func (s *Config) processAzureKeyConfigEnvVars(key *schemas.Key, provider schemas.ModelProvider, keyIndex int, newEnvKeys map[string]struct{}) error {
	azureConfig := key.AzureKeyConfig

	// Process Endpoint
	processedEndpoint, envVar, err := s.processEnvValue(azureConfig.Endpoint)
	if err != nil {
		return err
	}
	if envVar != "" {
		newEnvKeys[envVar] = struct{}{}
		s.EnvKeys[envVar] = append(s.EnvKeys[envVar], configstore.EnvKeyInfo{
			EnvVar:     envVar,
			Provider:   provider,
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
			s.EnvKeys[envVar] = append(s.EnvKeys[envVar], configstore.EnvKeyInfo{
				EnvVar:     envVar,
				Provider:   provider,
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
func (s *Config) processVertexKeyConfigEnvVars(key *schemas.Key, provider schemas.ModelProvider, keyIndex int, newEnvKeys map[string]struct{}) error {
	vertexConfig := key.VertexKeyConfig

	// Process ProjectID
	processedProjectID, envVar, err := s.processEnvValue(vertexConfig.ProjectID)
	if err != nil {
		return err
	}
	if envVar != "" {
		newEnvKeys[envVar] = struct{}{}
		s.EnvKeys[envVar] = append(s.EnvKeys[envVar], configstore.EnvKeyInfo{
			EnvVar:     envVar,
			Provider:   provider,
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
		s.EnvKeys[envVar] = append(s.EnvKeys[envVar], configstore.EnvKeyInfo{
			EnvVar:     envVar,
			Provider:   provider,
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
		s.EnvKeys[envVar] = append(s.EnvKeys[envVar], configstore.EnvKeyInfo{
			EnvVar:     envVar,
			Provider:   provider,
			KeyType:    "vertex_config",
			ConfigPath: fmt.Sprintf("providers.%s.keys[%s].vertex_key_config.auth_credentials", provider, key.ID),
			KeyID:      key.ID,
		})
	}
	vertexConfig.AuthCredentials = processedAuthCredentials

	return nil
}

// processBedrockKeyConfigEnvVars processes environment variables in Bedrock key configuration
func (s *Config) processBedrockKeyConfigEnvVars(key *schemas.Key, provider schemas.ModelProvider, keyIndex int, newEnvKeys map[string]struct{}) error {
	bedrockConfig := key.BedrockKeyConfig

	// Process AccessKey
	processedAccessKey, envVar, err := s.processEnvValue(bedrockConfig.AccessKey)
	if err != nil {
		return err
	}
	if envVar != "" {
		newEnvKeys[envVar] = struct{}{}
		s.EnvKeys[envVar] = append(s.EnvKeys[envVar], configstore.EnvKeyInfo{
			EnvVar:     envVar,
			Provider:   provider,
			KeyType:    "bedrock_config",
			ConfigPath: fmt.Sprintf("providers.%s.keys[%s].bedrock_key_config.access_key", provider, key.ID),
			KeyID:      key.ID,
		})
	}
	bedrockConfig.AccessKey = processedAccessKey

	// Process SecretKey
	processedSecretKey, envVar, err := s.processEnvValue(bedrockConfig.SecretKey)
	if err != nil {
		return err
	}
	if envVar != "" {
		newEnvKeys[envVar] = struct{}{}
		s.EnvKeys[envVar] = append(s.EnvKeys[envVar], configstore.EnvKeyInfo{
			EnvVar:     envVar,
			Provider:   provider,
			KeyType:    "bedrock_config",
			ConfigPath: fmt.Sprintf("providers.%s.keys[%s].bedrock_key_config.secret_key", provider, key.ID),
			KeyID:      key.ID,
		})
	}
	bedrockConfig.SecretKey = processedSecretKey

	// Process SessionToken if present
	if bedrockConfig.SessionToken != nil {
		processedSessionToken, envVar, err := s.processEnvValue(*bedrockConfig.SessionToken)
		if err != nil {
			return err
		}
		if envVar != "" {
			newEnvKeys[envVar] = struct{}{}
			s.EnvKeys[envVar] = append(s.EnvKeys[envVar], configstore.EnvKeyInfo{
				EnvVar:     envVar,
				Provider:   provider,
				KeyType:    "bedrock_config",
				ConfigPath: fmt.Sprintf("providers.%s.keys[%s].bedrock_key_config.session_token", provider, key.ID),
				KeyID:      key.ID,
			})
		}
		bedrockConfig.SessionToken = &processedSessionToken
	}

	// Process Region if present
	if bedrockConfig.Region != nil {
		processedRegion, envVar, err := s.processEnvValue(*bedrockConfig.Region)
		if err != nil {
			return err
		}
		if envVar != "" {
			newEnvKeys[envVar] = struct{}{}
			s.EnvKeys[envVar] = append(s.EnvKeys[envVar], configstore.EnvKeyInfo{
				EnvVar:     envVar,
				Provider:   provider,
				KeyType:    "bedrock_config",
				ConfigPath: fmt.Sprintf("providers.%s.keys[%s].bedrock_key_config.region", provider, key.ID),
				KeyID:      key.ID,
			})
		}
		bedrockConfig.Region = &processedRegion
	}

	// Process ARN if present
	if bedrockConfig.ARN != nil {
		processedARN, envVar, err := s.processEnvValue(*bedrockConfig.ARN)
		if err != nil {
			return err
		}
		if envVar != "" {
			newEnvKeys[envVar] = struct{}{}
			s.EnvKeys[envVar] = append(s.EnvKeys[envVar], configstore.EnvKeyInfo{
				EnvVar:     envVar,
				Provider:   provider,
				KeyType:    "bedrock_config",
				ConfigPath: fmt.Sprintf("providers.%s.keys[%s].bedrock_key_config.arn", provider, key.ID),
				KeyID:      key.ID,
			})
		}
		bedrockConfig.ARN = &processedARN
	}

	return nil
}

// GetVectorStoreConfigRedacted retrieves the vector store configuration with password redacted for safe external exposure
func (s *Config) GetVectorStoreConfigRedacted() (*vectorstore.Config, error) {
	var err error
	var vectorStoreConfig *vectorstore.Config
	if s.ConfigStore != nil {
		vectorStoreConfig, err = s.ConfigStore.GetVectorStoreConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to get vector store config: %w", err)
		}
	}
	if vectorStoreConfig == nil {
		return nil, nil
	}
	if vectorStoreConfig.Type == vectorstore.VectorStoreTypeWeaviate {
		weaviateConfig, ok := vectorStoreConfig.Config.(*vectorstore.WeaviateConfig)
		if !ok {
			return nil, fmt.Errorf("failed to cast vector store config to weaviate config")
		}
		// Create a copy to avoid modifying the original
		redactedWeaviateConfig := *weaviateConfig
		// Redact password if it exists
		if redactedWeaviateConfig.ApiKey != "" {
			redactedWeaviateConfig.ApiKey = RedactKey(redactedWeaviateConfig.ApiKey)
		}
		redactedVectorStoreConfig := *vectorStoreConfig
		redactedVectorStoreConfig.Config = &redactedWeaviateConfig
		return &redactedVectorStoreConfig, nil
	}
	return nil, nil
}

// ValidateCustomProvider validates the custom provider configuration
func ValidateCustomProvider(config configstore.ProviderConfig, provider schemas.ModelProvider) error {
	if config.CustomProviderConfig == nil {
		return nil
	}

	if bifrost.IsStandardProvider(provider) {
		return fmt.Errorf("custom provider validation failed: cannot be created on standard providers: %s", provider)
	}

	cpc := config.CustomProviderConfig

	// Validate base provider type
	if cpc.BaseProviderType == "" {
		return fmt.Errorf("custom provider validation failed: base_provider_type is required")
	}

	// Check if base provider is a supported base provider
	if !bifrost.IsSupportedBaseProvider(cpc.BaseProviderType) {
		return fmt.Errorf("custom provider validation failed: unsupported base_provider_type: %s", cpc.BaseProviderType)
	}
	return nil
}

// ValidateCustomProviderUpdate validates that immutable fields in CustomProviderConfig are not changed during updates
func ValidateCustomProviderUpdate(newConfig, existingConfig configstore.ProviderConfig, provider schemas.ModelProvider) error {
	// If neither config has CustomProviderConfig, no validation needed
	if newConfig.CustomProviderConfig == nil && existingConfig.CustomProviderConfig == nil {
		return nil
	}

	// If new config doesn't have CustomProviderConfig but existing does, return an error
	if newConfig.CustomProviderConfig == nil {
		return fmt.Errorf("custom_provider_config cannot be removed after creation for provider %s", provider)
	}

	// If existing config doesn't have CustomProviderConfig but new one does, that's fine (adding it)
	if existingConfig.CustomProviderConfig == nil {
		return ValidateCustomProvider(newConfig, provider)
	}

	// Both configs have CustomProviderConfig, validate immutable fields
	newCPC := newConfig.CustomProviderConfig
	existingCPC := existingConfig.CustomProviderConfig

	// CustomProviderKey is internally set and immutable, no validation needed

	// Check if BaseProviderType is being changed
	if newCPC.BaseProviderType != existingCPC.BaseProviderType {
		return fmt.Errorf("provider %s: base_provider_type cannot be changed from %s to %s after creation",
			provider, existingCPC.BaseProviderType, newCPC.BaseProviderType)
	}

	return nil
}

func (s *Config) AddProviderKeysToSemanticCacheConfig(config *schemas.PluginConfig) error {
	if config.Name != semanticcache.PluginName {
		return nil
	}

	// Check if config.Config exists
	if config.Config == nil {
		return fmt.Errorf("semantic_cache plugin config is nil")
	}

	// Type assert config.Config to map[string]interface{}
	configMap, ok := config.Config.(map[string]interface{})
	if !ok {
		return fmt.Errorf("semantic_cache plugin config must be a map, got %T", config.Config)
	}

	// Check if provider key exists and is a string
	providerVal, exists := configMap["provider"]
	if !exists {
		return fmt.Errorf("semantic_cache plugin missing required 'provider' field")
	}

	provider, ok := providerVal.(string)
	if !ok {
		return fmt.Errorf("semantic_cache plugin 'provider' field must be a string, got %T", providerVal)
	}

	if provider == "" {
		return fmt.Errorf("semantic_cache plugin 'provider' field cannot be empty")
	}

	keys, err := s.GetProviderConfigRaw(schemas.ModelProvider(provider))
	if err != nil {
		return fmt.Errorf("failed to get provider config for %s: %w", provider, err)
	}

	configMap["keys"] = keys.Keys

	return nil
}

func (s *Config) RemoveProviderKeysFromSemanticCacheConfig(config *configstore.TablePlugin) error {
	if config.Name != semanticcache.PluginName {
		return nil
	}

	// Check if config.Config exists
	if config.Config == nil {
		return fmt.Errorf("semantic_cache plugin config is nil")
	}

	// Type assert config.Config to map[string]interface{}
	configMap, ok := config.Config.(map[string]interface{})
	if !ok {
		return fmt.Errorf("semantic_cache plugin config must be a map, got %T", config.Config)
	}

	configMap["keys"] = []schemas.Key{}

	config.Config = configMap

	return nil
}

func DeepCopy[T any](in T) (T, error) {
	var out T
	b, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}
