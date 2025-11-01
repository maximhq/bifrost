// Package lib provides core functionality for the Bifrost HTTP service,
// including context propagation, header management, and integration with monitoring systems.
package lib

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
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
	EncryptionKey     string                                `json:"encryption_key"`
	AuthConfig        *configstore.AuthConfig               `json:"auth_config,omitempty"`
	Providers         map[string]configstore.ProviderConfig `json:"providers"`
	FrameworkConfig   *framework.FrameworkConfig            `json:"framework,omitempty"`
	MCP               *schemas.MCPConfig                    `json:"mcp,omitempty"`
	Governance        *configstore.GovernanceConfig         `json:"governance,omitempty"`
	VectorStoreConfig *vectorstore.Config                   `json:"vector_store,omitempty"`
	ConfigStoreConfig *configstore.Config                   `json:"config_store,omitempty"`
	LogsStoreConfig   *logstore.Config                      `json:"logs_store,omitempty"`
	Plugins           []*schemas.PluginConfig               `json:"plugins,omitempty"`
}

// UnmarshalJSON umarshals the ConfigData from JSON using internal unmarshallers
// for VectorStoreConfig, ConfigStoreConfig, and LogsStoreConfig to ensure proper
// type safety and configuration parsing.
func (cd *ConfigData) UnmarshalJSON(data []byte) error {
	// First, unmarshal into a temporary struct to get all fields except the complex configs
	type TempConfigData struct {
		FrameworkConfig   json.RawMessage                       `json:"framework,omitempty"`
		Client            *configstore.ClientConfig             `json:"client"`
		EncryptionKey     string                                `json:"encryption_key"`
		AuthConfig        *configstore.AuthConfig               `json:"auth_config,omitempty"`
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
	cd.EncryptionKey = temp.EncryptionKey
	cd.AuthConfig = temp.AuthConfig
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

	// Parse FrameworkConfig using its internal unmarshaler
	if len(temp.FrameworkConfig) > 0 {
		var frameworkConfig framework.FrameworkConfig
		if err := json.Unmarshal(temp.FrameworkConfig, &frameworkConfig); err != nil {
			return fmt.Errorf("failed to unmarshal framework config: %w", err)
		}
		cd.FrameworkConfig = &frameworkConfig
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
//   - Lock-free plugin reads via atomic.Pointer for minimal hot-path latency
type Config struct {
	Mu     sync.RWMutex // Exported for direct access from handlers (governance plugin)
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
	FrameworkConfig  *framework.FrameworkConfig

	// Track which keys come from environment variables
	EnvKeys map[string][]configstore.EnvKeyInfo

	// Plugin configs - atomic for lock-free reads with CAS updates
	Plugins atomic.Pointer[[]schemas.Plugin]

	// Plugin configs from config file/database
	PluginConfigs []*schemas.PluginConfig

	// Pricing manager
	PricingManager *modelcatalog.ModelCatalog
}

var DefaultClientConfig = configstore.ClientConfig{
	DropExcessRequests:      false,
	PrometheusLabels:        []string{},
	InitialPoolSize:         schemas.DefaultInitialPoolSize,
	EnableLogging:           true,
	EnableGovernance:        true,
	EnforceGovernanceHeader: false,
	AllowDirectKeys:         false,
	AllowedOrigins:          []string{},
	MaxRequestBodySizeMB:    100,
	EnableLiteLLMFallbacks:  false,
}

// initializeEncryption initializes the encryption key
func (c *Config) initializeEncryption(configKey string) error {
	encryptionKey := ""
	if configKey != "" {
		if strings.HasPrefix(configKey, "env.") {
			var err error
			if encryptionKey, _, err = c.processEnvValue(configKey); err != nil {
				return fmt.Errorf("failed to process encryption key: %w", err)
			}
		} else {
			logger.Warn("encryption_key should reference an environment variable (env.VAR_NAME) rather than storing the key directly in the config file")
			encryptionKey = configKey
		}
	}
	if encryptionKey == "" {
		if os.Getenv("BIFROST_ENCRYPTION_KEY") != "" {
			encryptionKey = os.Getenv("BIFROST_ENCRYPTION_KEY")
		}
	}
	encrypt.Init(encryptionKey, logger)
	return nil
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
	// Initialize config
	config := &Config{
		configPath: configFilePath,
		EnvKeys:    make(map[string][]configstore.EnvKeyInfo),
		Providers:  make(map[schemas.ModelProvider]configstore.ProviderConfig),
		Plugins:    atomic.Pointer[[]schemas.Plugin]{},
	}
	// Getting absolute path for config file
	absConfigFilePath, err := filepath.Abs(configFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path for config file: %w", err)
	}
	// Check if config file exists
	data, err := os.ReadFile(configFilePath)
	if err != nil {
		// If config file doesn't exist, we will directly use the config store (create one if it doesn't exist)
		if os.IsNotExist(err) {
			logger.Info("config file not found at path: %s, initializing with default values", absConfigFilePath)
			// Initializing with default values
			config.ConfigStore, err = configstore.NewConfigStore(ctx, &configstore.Config{
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
			clientConfig, err := config.ConfigStore.GetClientConfig(ctx)
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
			err = config.ConfigStore.UpdateClientConfig(ctx, clientConfig)
			if err != nil {
				return nil, fmt.Errorf("failed to update client config: %w", err)
			}
			config.ClientConfig = *clientConfig
			// Checking if log store config already exist
			logStoreConfig, err := config.ConfigStore.GetLogsStoreConfig(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to get logs store config: %w", err)
			}
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
			config.LogsStore, err = logstore.NewLogStore(ctx, logStoreConfig, logger)
			if err != nil {
				return nil, fmt.Errorf("failed to initialize logs store: %v", err)
			}
			logger.Info("logs store initialized.")
			err = config.ConfigStore.UpdateLogsStoreConfig(ctx, logStoreConfig)
			if err != nil {
				return nil, fmt.Errorf("failed to update logs store config: %w", err)
			}
			// No providers in database, auto-detect from environment
			providers, err := config.ConfigStore.GetProvidersConfig(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to get providers config: %w", err)
			}
			if providers == nil {
				config.autoDetectProviders(ctx)
				providers = config.Providers
				// Store providers config in database
				err = config.ConfigStore.UpdateProvidersConfig(ctx, providers)
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
							Name:             dbKey.Name,
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
			// Loading governance config
			var governanceConfig *configstore.GovernanceConfig
			if config.ConfigStore != nil {
				governanceConfig, err = config.ConfigStore.GetGovernanceConfig(ctx)
				if err != nil {
					logger.Warn("failed to get governance config from store: %v", err)
				}
			}
			if governanceConfig != nil {
				config.GovernanceConfig = governanceConfig
			}
			// Updating auth config if present in config
			// Checking if MCP config already exists
			mcpConfig, err := config.ConfigStore.GetMCPConfig(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to get MCP config: %w", err)
			}
			if mcpConfig == nil {
				if err := config.processMCPEnvVars(); err != nil {
					logger.Warn("failed to process MCP env vars: %v", err)
				}
				if config.ConfigStore != nil && config.MCPConfig != nil {
					for _, clientConfig := range config.MCPConfig.ClientConfigs {
						if err := config.ConfigStore.CreateMCPClientConfig(ctx, clientConfig, config.EnvKeys); err != nil {
							logger.Warn("failed to create MCP client config: %v", err)
							continue
						}
					}
					// Refresh from store to ensure parity with persisted state
					if mcpConfig, err = config.ConfigStore.GetMCPConfig(ctx); err != nil {
						return nil, fmt.Errorf("failed to get MCP config after update: %w", err)
					}
					config.MCPConfig = mcpConfig
				}
			} else {
				// Use the saved config from the store
				config.MCPConfig = mcpConfig
			}
			// Checking if plugins already exist
			plugins, err := config.ConfigStore.GetPlugins(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to get plugins: %w", err)
			}
			if plugins == nil {
				config.PluginConfigs = []*schemas.PluginConfig{}
			} else {
				config.PluginConfigs = make([]*schemas.PluginConfig, len(plugins))
				for i, plugin := range plugins {
					pluginConfig := &schemas.PluginConfig{
						Name:    plugin.Name,
						Enabled: plugin.Enabled,
						Config:  plugin.Config,
						Path:    plugin.Path,
					}
					if plugin.Name == semanticcache.PluginName {
						if err := config.AddProviderKeysToSemanticCacheConfig(pluginConfig); err != nil {
							logger.Warn("failed to add provider keys to semantic cache config: %v", err)
						}
					}
					config.PluginConfigs[i] = pluginConfig
				}
			}
			// Load environment variable tracking
			var dbEnvKeys map[string][]configstore.EnvKeyInfo
			if dbEnvKeys, err = config.ConfigStore.GetEnvKeys(ctx); err != nil {
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
			err = config.ConfigStore.UpdateEnvKeys(ctx, config.EnvKeys)
			if err != nil {
				return nil, fmt.Errorf("failed to update env keys: %w", err)
			}
			// Fetching framework config if present
			frameworkConfig, err := config.ConfigStore.GetFrameworkConfig(ctx)
			if err != nil {
				logger.Warn("failed to get framework config from store: %v", err)
			}
			pricingConfig := &modelcatalog.Config{}
			if frameworkConfig != nil && frameworkConfig.PricingURL != nil {
				pricingConfig.PricingURL = frameworkConfig.PricingURL
			} else {
				pricingConfig.PricingURL = bifrost.Ptr(modelcatalog.DefaultPricingURL)
			}
			if frameworkConfig != nil && frameworkConfig.PricingSyncInterval != nil && *frameworkConfig.PricingSyncInterval > 0 {
				syncDuration := time.Duration(*frameworkConfig.PricingSyncInterval) * time.Second
				pricingConfig.PricingSyncInterval = &syncDuration
			} else {
				pricingConfig.PricingSyncInterval = bifrost.Ptr(modelcatalog.DefaultPricingSyncInterval)
			}
			// Updating DB with latest config
			configID := uint(0)
			if frameworkConfig != nil {
				configID = frameworkConfig.ID
			}
			var durationSec int64
			if pricingConfig.PricingSyncInterval != nil {
				durationSec = int64((*pricingConfig.PricingSyncInterval).Seconds())
			} else {
				d := modelcatalog.DefaultPricingSyncInterval
				durationSec = int64(d.Seconds())
			}
			logger.Debug("updating framework config with duration: %d", durationSec)
			err = config.ConfigStore.UpdateFrameworkConfig(ctx, &configstoreTables.TableFrameworkConfig{
				ID:                  configID,
				PricingURL:          pricingConfig.PricingURL,
				PricingSyncInterval: bifrost.Ptr(durationSec),
			})
			if err != nil {
				return nil, fmt.Errorf("failed to update framework config: %w", err)
			}
			config.FrameworkConfig = &framework.FrameworkConfig{
				Pricing: pricingConfig,
			}
			// Initializing pricing manager
			pricingManager, err := modelcatalog.Init(ctx, pricingConfig, config.ConfigStore, logger)
			if err != nil {
				logger.Warn("failed to initialize pricing manager: %v", err)
			}
			config.PricingManager = pricingManager
			// We check the encryption key is present in the environment variables
			encryptionKey := ""
			if os.Getenv("BIFROST_ENCRYPTION_KEY") != "" {
				encryptionKey = os.Getenv("BIFROST_ENCRYPTION_KEY")
			}
			if err := config.initializeEncryption(encryptionKey); err != nil {
				return nil, fmt.Errorf("failed to initialize encryption: %w", err)
			}
			return config, nil
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// If config file exists, we will use it to only bootstrap config tables.

	logger.Info("loading configuration from: %s", absConfigFilePath)

	var configData ConfigData
	if err := json.Unmarshal(data, &configData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Initializing config store
	if configData.ConfigStoreConfig != nil && configData.ConfigStoreConfig.Enabled {
		config.ConfigStore, err = configstore.NewConfigStore(ctx, configData.ConfigStoreConfig, logger)
		if err != nil {
			return nil, err
		}
		logger.Info("config store initialized")
	}

	// Initializing log store
	if configData.LogsStoreConfig != nil && configData.LogsStoreConfig.Enabled {
		config.LogsStore, err = logstore.NewLogStore(ctx, configData.LogsStoreConfig, logger)
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
			err = config.ConfigStore.UpdateVectorStoreConfig(ctx, configData.VectorStoreConfig)
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
		clientConfig, err = config.ConfigStore.GetClientConfig(ctx)
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
			err = config.ConfigStore.UpdateClientConfig(ctx, &config.ClientConfig)
			if err != nil {
				logger.Warn("failed to update client config: %v", err)
			}
		}
	}

	// 2. Check for Providers

	var processedProviders map[schemas.ModelProvider]configstore.ProviderConfig
	if config.ConfigStore != nil {
		logger.Debug("getting providers config from store")
		processedProviders, err = config.ConfigStore.GetProvidersConfig(ctx)
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
						if err := config.processAzureKeyConfigEnvVars(&cfg.Keys[i], provider, newEnvKeys); err != nil {
							config.cleanupEnvKeys(provider, "", newEnvKeys)
							logger.Warn("failed to process Azure key config env vars for %s: %v", provider, err)
							continue
						}
					}

					// Process Vertex key config if present
					if key.VertexKeyConfig != nil {
						if err := config.processVertexKeyConfigEnvVars(&cfg.Keys[i], provider, newEnvKeys); err != nil {
							config.cleanupEnvKeys(provider, "", newEnvKeys)
							logger.Warn("failed to process Vertex key config env vars for %s: %v", provider, err)
							continue
						}
					}

					// Process Bedrock key config if present
					if key.BedrockKeyConfig != nil {
						if err := config.processBedrockKeyConfigEnvVars(&cfg.Keys[i], provider, newEnvKeys); err != nil {
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
			config.autoDetectProviders(ctx)
		}
		if config.ConfigStore != nil {
			logger.Debug("updating providers config in store")
			err = config.ConfigStore.UpdateProvidersConfig(ctx, processedProviders)
			if err != nil {
				logger.Warn("failed to update providers config: %v", err)
			}
			if err := config.ConfigStore.UpdateEnvKeys(ctx, config.EnvKeys); err != nil {
				logger.Warn("failed to update env keys: %v", err)
			}
		}
	}

	// 3. Check for MCP Config

	var mcpConfig *schemas.MCPConfig
	if config.ConfigStore != nil {
		logger.Debug("getting MCP config from store")
		mcpConfig, err = config.ConfigStore.GetMCPConfig(ctx)
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
		if config.ConfigStore != nil && config.MCPConfig != nil {
			logger.Debug("updating MCP config in store")
			for _, clientConfig := range config.MCPConfig.ClientConfigs {
				if err := config.ConfigStore.CreateMCPClientConfig(ctx, clientConfig, config.EnvKeys); err != nil {
					logger.Warn("failed to create MCP client config: %v", err)
					continue
				}
			}
		}
	}

	// 4. Check for Governance Config

	var governanceConfig *configstore.GovernanceConfig
	if config.ConfigStore != nil {
		logger.Debug("getting governance config from store")
		governanceConfig, err = config.ConfigStore.GetGovernanceConfig(ctx)
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
			if err := config.ConfigStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
				// Create budgets
				for _, budget := range config.GovernanceConfig.Budgets {
					if err := config.ConfigStore.CreateBudget(ctx, &budget, tx); err != nil {
						return fmt.Errorf("failed to create budget %s: %w", budget.ID, err)
					}
				}

				// Create rate limits
				for _, rateLimit := range config.GovernanceConfig.RateLimits {
					if err := config.ConfigStore.CreateRateLimit(ctx, &rateLimit, tx); err != nil {
						return fmt.Errorf("failed to create rate limit %s: %w", rateLimit.ID, err)
					}
				}

				// Create customers
				for _, customer := range config.GovernanceConfig.Customers {
					if err := config.ConfigStore.CreateCustomer(ctx, &customer, tx); err != nil {
						return fmt.Errorf("failed to create customer %s: %w", customer.ID, err)
					}
				}

				// Create teams
				for _, team := range config.GovernanceConfig.Teams {
					if err := config.ConfigStore.CreateTeam(ctx, &team, tx); err != nil {
						return fmt.Errorf("failed to create team %s: %w", team.ID, err)
					}
				}

				// Create virtual keys
				for _, virtualKey := range config.GovernanceConfig.VirtualKeys {
					// Look up existing provider keys by key_id and populate the Keys field
					var existingKeys []configstoreTables.TableKey
					for _, keyRef := range virtualKey.Keys {
						if keyRef.KeyID != "" {
							var existingKey configstoreTables.TableKey
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

					if err := config.ConfigStore.CreateVirtualKey(ctx, &virtualKey, tx); err != nil {
						return fmt.Errorf("failed to create virtual key %s: %w", virtualKey.ID, err)
					}
				}

				return nil
			}); err != nil {
				logger.Warn("failed to update governance config: %v", err)
			}
		}
	}

	if configData.AuthConfig != nil {
		if config.ConfigStore != nil {
			configStoreAuthConfig, err := config.ConfigStore.GetAuthConfig(ctx)
			if err == nil && configStoreAuthConfig == nil {
				// Adding this config
				if err := config.ConfigStore.UpdateAuthConfig(ctx, configData.AuthConfig); err != nil {
					logger.Warn("failed to update auth config: %v", err)
				}
			}
		}else if governanceConfig != nil && governanceConfig.AuthConfig == nil {
			// Adding this config
			governanceConfig.AuthConfig = configData.AuthConfig
		}
	}

	// 5. Check for Plugins

	if config.ConfigStore != nil {
		logger.Debug("getting plugins from store")
		plugins, err := config.ConfigStore.GetPlugins(ctx)
		if err != nil {
			logger.Warn("failed to get plugins from store: %v", err)
		}
		if plugins != nil {
			config.PluginConfigs = make([]*schemas.PluginConfig, len(plugins))
			for i, plugin := range plugins {
				pluginConfig := &schemas.PluginConfig{
					Name:    plugin.Name,
					Enabled: plugin.Enabled,
					Config:  plugin.Config,
					Path:    plugin.Path,
				}
				if plugin.Name == semanticcache.PluginName {
					if err := config.AddProviderKeysToSemanticCacheConfig(pluginConfig); err != nil {
						logger.Warn("failed to add provider keys to semantic cache config: %v", err)
					}
				}
				config.PluginConfigs[i] = pluginConfig
			}
		}
	}

	// First we are loading plugins from the db
	if len(configData.Plugins) > 0 {
		logger.Debug("no plugins found in store, processing from config file")
		if len(config.PluginConfigs) == 0 {
			config.PluginConfigs = configData.Plugins
		} else {
			// Here we will append new plugins to the config.PluginConfigs
			for _, plugin := range configData.Plugins {
				if !slices.ContainsFunc(config.PluginConfigs, func(p *schemas.PluginConfig) bool {
					return p.Name == plugin.Name
				}) {
					config.PluginConfigs = append(config.PluginConfigs, plugin)
				}
			}
		}

		for i, plugin := range config.PluginConfigs {
			if plugin.Name == semanticcache.PluginName {
				if err := config.AddProviderKeysToSemanticCacheConfig(plugin); err != nil {
					logger.Warn("failed to add provider keys to semantic cache config: %v", err)
				}
				config.PluginConfigs[i] = plugin
			}
		}

		if config.ConfigStore != nil {
			logger.Debug("updating plugins in store")
			for _, plugin := range config.PluginConfigs {
				pluginConfigCopy, err := DeepCopy(plugin.Config)
				if err != nil {
					logger.Warn("failed to deep copy plugin config, skipping database update: %v", err)
					continue
				}

				pluginConfig := &configstoreTables.TablePlugin{
					Name:    plugin.Name,
					Enabled: plugin.Enabled,
					Config:  pluginConfigCopy,
					Path:    plugin.Path,
				}
				if plugin.Name == semanticcache.PluginName {
					if err := config.RemoveProviderKeysFromSemanticCacheConfig(pluginConfig); err != nil {
						logger.Warn("failed to remove provider keys from semantic cache config: %v", err)
					}
				}
				if err := config.ConfigStore.CreatePlugin(ctx, pluginConfig); err != nil {
					logger.Warn("failed to update plugin: %v", err)
				}
			}
		}
	}

	// 6. Check for Env Keys in config store

	// Initialize env keys
	if config.ConfigStore != nil {
		envKeys, err := config.ConfigStore.GetEnvKeys(ctx)
		if err != nil {
			logger.Warn("failed to get env keys from store: %v", err)
		}
		config.EnvKeys = envKeys
	}

	if config.EnvKeys == nil {
		config.EnvKeys = make(map[string][]configstore.EnvKeyInfo)
	}

	// Initializing pricing manager
	pricingConfig := &modelcatalog.Config{}
	if config.ConfigStore != nil {
		frameworkConfig, err := config.ConfigStore.GetFrameworkConfig(ctx)
		if err != nil {
			logger.Warn("failed to get framework config from store: %v", err)
		}
		if frameworkConfig != nil && frameworkConfig.PricingURL != nil {
			pricingConfig.PricingURL = frameworkConfig.PricingURL
		}
		if frameworkConfig != nil && frameworkConfig.PricingSyncInterval != nil {
			syncDuration := time.Duration(*frameworkConfig.PricingSyncInterval) * time.Second
			pricingConfig.PricingSyncInterval = &syncDuration
		}
	} else if configData.FrameworkConfig != nil && configData.FrameworkConfig.Pricing != nil {
		pricingConfig.PricingURL = configData.FrameworkConfig.Pricing.PricingURL
		syncDuration := time.Duration(*configData.FrameworkConfig.Pricing.PricingSyncInterval) * time.Second
		pricingConfig.PricingSyncInterval = &syncDuration
	}
	// Updating framework config
	config.FrameworkConfig = &framework.FrameworkConfig{
		Pricing: pricingConfig,
	}
	// Creating pricing manager
	pricingManager, err := modelcatalog.Init(ctx, pricingConfig, config.ConfigStore, logger)
	if err != nil {
		logger.Warn("failed to initialize pricing manager: %v", err)
	}
	config.PricingManager = pricingManager

	// Initializing encryption
	var encryptionKey string
	if configData.EncryptionKey != "" {
		if strings.HasPrefix(configData.EncryptionKey, "env.") {
			if encryptionKey, _, err = config.processEnvValue(configData.EncryptionKey); err != nil {
				return nil, fmt.Errorf("failed to process encryption key: %w", err)
			}
		} else {
			logger.Warn("encryption_key should reference an environment variable (env.VAR_NAME) rather than storing the key directly in the config file")
			encryptionKey = configData.EncryptionKey
		}
	}
	if encryptionKey == "" {
		// We will try to read from the default environment variable
		if os.Getenv("BIFROST_ENCRYPTION_KEY") != "" {
			encryptionKey = os.Getenv("BIFROST_ENCRYPTION_KEY")
		}
	}
	if err := config.initializeEncryption(encryptionKey); err != nil {
		return nil, fmt.Errorf("failed to initialize encryption: %w", err)
	}
	// Done initializing encryption
	return config, nil
}

// GetRawConfigString returns the raw configuration string.
func (c *Config) GetRawConfigString() string {
	data, err := os.ReadFile(c.configPath)
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
func (c *Config) processEnvValue(value string) (string, string, error) {
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

// GetProviderConfigRaw retrieves the raw, unredacted provider configuration from memory.
// This method is for internal use only, particularly by the account implementation.
//
// Performance characteristics:
//   - Memory access: ultra-fast direct memory access
//   - No database I/O or JSON parsing overhead
//   - Thread-safe with read locks for concurrent access
//
// Returns a copy of the configuration to prevent external modifications.
func (c *Config) GetProviderConfigRaw(provider schemas.ModelProvider) (*configstore.ProviderConfig, error) {
	c.Mu.RLock()
	defer c.Mu.RUnlock()

	config, exists := c.Providers[provider]
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
func (c *Config) ShouldAllowDirectKeys() bool {
	return c.ClientConfig.AllowDirectKeys
}

// GetLoadedPlugins returns the current snapshot of loaded plugins.
// This method is lock-free and safe for concurrent access from hot paths.
// It returns the plugin slice from the atomic pointer, which is safe to iterate
// even if plugins are being updated concurrently.
func (c *Config) GetLoadedPlugins() []schemas.Plugin {
	if plugins := c.Plugins.Load(); plugins != nil {
		return *plugins
	}
	return nil
}

// IsPluginLoaded checks if a plugin with the given name is currently loaded.
// This method is lock-free and safe for concurrent access from hot paths.
// It iterates through the plugin slice (typically 5-10 plugins, ~50ns overhead).
// For small plugin counts, this is faster than maintaining a separate map.
func (c *Config) IsPluginLoaded(name string) bool {
	plugins := c.Plugins.Load()
	if plugins == nil {
		return false
	}
	for _, p := range *plugins {
		if p.GetName() == name {
			return true
		}
	}
	return false
}

// GetProviderConfigRedacted retrieves a provider configuration with sensitive values redacted.
// This method is intended for external API responses and logging.
//
// The returned configuration has sensitive values redacted:
// - API keys are redacted using RedactKey()
// - Values from environment variables show the original env var name (env.VAR_NAME)
//
// Returns a new copy with redacted values that is safe to expose externally.
func (c *Config) GetProviderConfigRedacted(provider schemas.ModelProvider) (*configstore.ProviderConfig, error) {
	c.Mu.RLock()
	defer c.Mu.RUnlock()

	config, exists := c.Providers[provider]
	if !exists {
		return nil, ErrNotFound
	}

	// Create a map for quick lookup of env vars for this provider
	envVarsByPath := make(map[string]string)
	for envVar, infos := range c.EnvKeys {
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
			Name:   key.Name,
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
				azureConfig.Endpoint = key.AzureKeyConfig.Endpoint
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
func (c *Config) GetAllProviders() ([]schemas.ModelProvider, error) {
	c.Mu.RLock()
	defer c.Mu.RUnlock()

	providers := make([]schemas.ModelProvider, 0, len(c.Providers))
	for provider := range c.Providers {
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
func (c *Config) AddProvider(ctx context.Context, provider schemas.ModelProvider, config configstore.ProviderConfig) error {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	// Check if provider already exists
	if _, exists := c.Providers[provider]; exists {
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
		processedValue, envVar, err := c.processEnvValue(key.Value)
		if err != nil {
			c.cleanupEnvKeys(provider, "", newEnvKeys)
			return fmt.Errorf("failed to process env var in key: %w", err)
		}
		config.Keys[i].Value = processedValue

		// Track environment key if it came from env
		if envVar != "" {
			newEnvKeys[envVar] = struct{}{}
			c.EnvKeys[envVar] = append(c.EnvKeys[envVar], configstore.EnvKeyInfo{
				EnvVar:     envVar,
				Provider:   provider,
				KeyType:    "api_key",
				ConfigPath: fmt.Sprintf("providers.%s.keys[%s]", provider, config.Keys[i].ID),
				KeyID:      config.Keys[i].ID,
			})
		}

		// Process Azure key config if present
		if key.AzureKeyConfig != nil {
			if err := c.processAzureKeyConfigEnvVars(&config.Keys[i], provider, newEnvKeys); err != nil {
				c.cleanupEnvKeys(provider, "", newEnvKeys)
				return fmt.Errorf("failed to process Azure key config env vars: %w", err)
			}
		}

		// Process Vertex key config if present
		if key.VertexKeyConfig != nil {
			if err := c.processVertexKeyConfigEnvVars(&config.Keys[i], provider, newEnvKeys); err != nil {
				c.cleanupEnvKeys(provider, "", newEnvKeys)
				return fmt.Errorf("failed to process Vertex key config env vars: %w", err)
			}
		}

		// Process Bedrock key config if present
		if key.BedrockKeyConfig != nil {
			if err := c.processBedrockKeyConfigEnvVars(&config.Keys[i], provider, newEnvKeys); err != nil {
				c.cleanupEnvKeys(provider, "", newEnvKeys)
				return fmt.Errorf("failed to process Bedrock key config env vars: %w", err)
			}
		}
	}

	c.Providers[provider] = config

	if c.ConfigStore != nil {
		if err := c.ConfigStore.AddProvider(ctx, provider, config, c.EnvKeys); err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				return ErrNotFound
			}
			return fmt.Errorf("failed to update provider config in store: %w", err)
		}
		if err := c.ConfigStore.UpdateEnvKeys(ctx, c.EnvKeys); err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				return ErrNotFound
			}
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
func (c *Config) UpdateProviderConfig(ctx context.Context, provider schemas.ModelProvider, config configstore.ProviderConfig) error {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	// Get existing configuration for validation
	existingConfig, exists := c.Providers[provider]
	if !exists {
		return ErrNotFound
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
		processedValue, envVar, err := c.processEnvValue(key.Value)
		if err != nil {
			c.cleanupEnvKeys(provider, "", newEnvKeys) // Clean up only new vars on failure
			return fmt.Errorf("failed to process env var in key: %w", err)
		}
		config.Keys[i].Value = processedValue

		// Track environment key if it came from env
		if envVar != "" {
			newEnvKeys[envVar] = struct{}{}
			c.EnvKeys[envVar] = append(c.EnvKeys[envVar], configstore.EnvKeyInfo{
				EnvVar:     envVar,
				Provider:   provider,
				KeyType:    "api_key",
				ConfigPath: fmt.Sprintf("providers.%s.keys[%s]", provider, config.Keys[i].ID),
				KeyID:      config.Keys[i].ID,
			})
		}

		// Process Azure key config if present
		if key.AzureKeyConfig != nil {
			if err := c.processAzureKeyConfigEnvVars(&config.Keys[i], provider, newEnvKeys); err != nil {
				c.cleanupEnvKeys(provider, "", newEnvKeys)
				return fmt.Errorf("failed to process Azure key config env vars: %w", err)
			}
		}

		// Process Vertex key config if present
		if key.VertexKeyConfig != nil {
			if err := c.processVertexKeyConfigEnvVars(&config.Keys[i], provider, newEnvKeys); err != nil {
				c.cleanupEnvKeys(provider, "", newEnvKeys)
				return fmt.Errorf("failed to process Vertex key config env vars: %w", err)
			}
		}

		// Process Bedrock key config if present
		if key.BedrockKeyConfig != nil {
			if err := c.processBedrockKeyConfigEnvVars(&config.Keys[i], provider, newEnvKeys); err != nil {
				c.cleanupEnvKeys(provider, "", newEnvKeys)
				return fmt.Errorf("failed to process Bedrock key config env vars: %w", err)
			}
		}
	}

	c.Providers[provider] = config

	if c.ConfigStore != nil {
		if err := c.ConfigStore.UpdateProvider(ctx, provider, config, c.EnvKeys); err != nil {
			return fmt.Errorf("failed to update provider config in store: %w", err)
		}
		if err := c.ConfigStore.UpdateEnvKeys(ctx, c.EnvKeys); err != nil {
			logger.Warn("failed to update env keys: %v", err)
		}
	}

	logger.Info("Updated configuration for provider: %s", provider)
	return nil
}

// RemoveProvider removes a provider configuration from memory.
func (c *Config) RemoveProvider(ctx context.Context, provider schemas.ModelProvider) error {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	if _, exists := c.Providers[provider]; !exists {
		return ErrNotFound
	}

	delete(c.Providers, provider)
	c.cleanupEnvKeys(provider, "", nil)

	if c.ConfigStore != nil {
		if err := c.ConfigStore.DeleteProvider(ctx, provider); err != nil {
			return fmt.Errorf("failed to update provider config in store: %w", err)
		}
		if err := c.ConfigStore.UpdateEnvKeys(ctx, c.EnvKeys); err != nil {
			logger.Warn("failed to update env keys: %v", err)
		}
	}

	logger.Info("Removed provider: %s", provider)
	return nil
}

// GetAllKeys returns the redacted keys
func (c *Config) GetAllKeys() ([]configstoreTables.TableKey, error) {
	c.Mu.RLock()
	defer c.Mu.RUnlock()

	keys := make([]configstoreTables.TableKey, 0)
	for providerKey, provider := range c.Providers {
		for _, key := range provider.Keys {
			keys = append(keys, configstoreTables.TableKey{
				KeyID:    key.ID,
				Name:     key.Name,
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
func (c *Config) processMCPEnvVars() error {
	if c.MCPConfig == nil {
		return nil
	}

	var missingEnvVars []string

	// Process each client config
	for i, clientConfig := range c.MCPConfig.ClientConfigs {
		// Process ConnectionString if present
		if clientConfig.ConnectionString != nil {
			newValue, envVar, err := c.processEnvValue(*clientConfig.ConnectionString)
			if err != nil {
				logger.Warn("failed to process env vars in MCP client %s: %v", clientConfig.Name, err)
				missingEnvVars = append(missingEnvVars, envVar)
				continue
			}
			if envVar != "" {
				c.EnvKeys[envVar] = append(c.EnvKeys[envVar], configstore.EnvKeyInfo{
					EnvVar:     envVar,
					Provider:   "",
					KeyType:    "connection_string",
					ConfigPath: fmt.Sprintf("mcp.client_configs.%s.connection_string", clientConfig.Name),
					KeyID:      "", // Empty for MCP connection strings
				})
			}
			c.MCPConfig.ClientConfigs[i].ConnectionString = &newValue
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
func (c *Config) SetBifrostClient(client *bifrost.Bifrost) {
	c.muMCP.Lock()
	defer c.muMCP.Unlock()

	c.client = client
}

// AddMCPClient adds a new MCP client to the configuration.
// This method is called when a new MCP client is added via the HTTP API.
//
// The method:
//   - Validates that the MCP client doesn't already exist
//   - Processes environment variables in the MCP client configuration
//   - Stores the processed configuration in memory
func (c *Config) AddMCPClient(ctx context.Context, clientConfig schemas.MCPClientConfig) error {
	if c.client == nil {
		return fmt.Errorf("bifrost client not set")
	}

	c.muMCP.Lock()
	defer c.muMCP.Unlock()

	if c.MCPConfig == nil {
		c.MCPConfig = &schemas.MCPConfig{}
	}

	// Track new environment variables
	newEnvKeys := make(map[string]struct{})

	c.MCPConfig.ClientConfigs = append(c.MCPConfig.ClientConfigs, clientConfig)

	// Process environment variables in the new client config
	if clientConfig.ConnectionString != nil {
		processedValue, envVar, err := c.processEnvValue(*clientConfig.ConnectionString)
		if err != nil {
			c.MCPConfig.ClientConfigs = c.MCPConfig.ClientConfigs[:len(c.MCPConfig.ClientConfigs)-1]
			return fmt.Errorf("failed to process env var in connection string: %w", err)
		}
		if envVar != "" {
			newEnvKeys[envVar] = struct{}{}
			c.EnvKeys[envVar] = append(c.EnvKeys[envVar], configstore.EnvKeyInfo{
				EnvVar:     envVar,
				Provider:   "",
				KeyType:    "connection_string",
				ConfigPath: fmt.Sprintf("mcp.client_configs.%s.connection_string", clientConfig.Name),
				KeyID:      "", // Empty for MCP connection strings
			})
		}
		c.MCPConfig.ClientConfigs[len(c.MCPConfig.ClientConfigs)-1].ConnectionString = &processedValue
	}

	// Config with processed env vars
	if err := c.client.AddMCPClient(c.MCPConfig.ClientConfigs[len(c.MCPConfig.ClientConfigs)-1]); err != nil {
		c.MCPConfig.ClientConfigs = c.MCPConfig.ClientConfigs[:len(c.MCPConfig.ClientConfigs)-1]
		c.cleanupEnvKeys("", clientConfig.Name, newEnvKeys)
		return fmt.Errorf("failed to add MCP client: %w", err)
	}

	if c.ConfigStore != nil {
		if err := c.ConfigStore.CreateMCPClientConfig(ctx, clientConfig, c.EnvKeys); err != nil {
			return fmt.Errorf("failed to create MCP client config in store: %w", err)
		}
		if err := c.ConfigStore.UpdateEnvKeys(ctx, c.EnvKeys); err != nil {
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
func (c *Config) RemoveMCPClient(ctx context.Context, name string) error {
	if c.client == nil {
		return fmt.Errorf("bifrost client not set")
	}

	c.muMCP.Lock()
	defer c.muMCP.Unlock()

	if c.MCPConfig == nil {
		return fmt.Errorf("no MCP config found")
	}

	if err := c.client.RemoveMCPClient(name); err != nil {
		return fmt.Errorf("failed to remove MCP client: %w", err)
	}

	for i, clientConfig := range c.MCPConfig.ClientConfigs {
		if clientConfig.Name == name {
			c.MCPConfig.ClientConfigs = append(c.MCPConfig.ClientConfigs[:i], c.MCPConfig.ClientConfigs[i+1:]...)
			break
		}
	}

	c.cleanupEnvKeys("", name, nil)

	if c.ConfigStore != nil {
		if err := c.ConfigStore.DeleteMCPClientConfig(ctx, name); err != nil {
			return fmt.Errorf("failed to delete MCP client config from store: %w", err)
		}
		if err := c.ConfigStore.UpdateEnvKeys(ctx, c.EnvKeys); err != nil {
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
//   - toolsToExecute: Tools to execute from the client
func (c *Config) EditMCPClientTools(ctx context.Context, name string, toolsToExecute []string) error {
	if c.client == nil {
		return fmt.Errorf("bifrost client not set")
	}

	c.muMCP.Lock()
	defer c.muMCP.Unlock()

	if c.MCPConfig == nil {
		return fmt.Errorf("no MCP config found")
	}

	if err := c.client.EditMCPClientTools(name, toolsToExecute); err != nil {
		return fmt.Errorf("failed to edit MCP client tools: %w", err)
	}

	// Find the client config and create an updated version
	var updatedClientConfig schemas.MCPClientConfig
	var found bool
	for i, clientConfig := range c.MCPConfig.ClientConfigs {
		if clientConfig.Name == name {
			c.MCPConfig.ClientConfigs[i].ToolsToExecute = toolsToExecute
			updatedClientConfig = c.MCPConfig.ClientConfigs[i]
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("MCP client '%s' not found", name)
	}

	if c.ConfigStore != nil {
		if err := c.ConfigStore.UpdateMCPClientConfig(ctx, name, updatedClientConfig, c.EnvKeys); err != nil {
			return fmt.Errorf("failed to update MCP client config in store: %w", err)
		}
		if err := c.ConfigStore.UpdateEnvKeys(ctx, c.EnvKeys); err != nil {
			logger.Warn("failed to update env keys: %v", err)
		}
	}

	return nil
}

// RedactMCPClientConfig creates a redacted copy of an MCP client configuration.
// Connection strings are either redacted or replaced with their environment variable names.
func (c *Config) RedactMCPClientConfig(config schemas.MCPClientConfig) schemas.MCPClientConfig {
	// Create a copy with basic fields
	configCopy := schemas.MCPClientConfig{
		Name:             config.Name,
		ConnectionType:   config.ConnectionType,
		ConnectionString: config.ConnectionString,
		StdioConfig:      config.StdioConfig,
		ToolsToExecute:   append([]string{}, config.ToolsToExecute...),
	}

	// Handle connection string if present
	if config.ConnectionString != nil {
		connStr := *config.ConnectionString

		// Check if this value came from an env var
		for envVar, infos := range c.EnvKeys {
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
func (c *Config) cleanupEnvKeys(provider schemas.ModelProvider, mcpClientName string, envVarsToRemove map[string]struct{}) {
	// If envVarsToRemove is provided, only clean those specific vars
	if envVarsToRemove != nil {
		for envVar := range envVarsToRemove {
			c.cleanupEnvVar(envVar, provider, mcpClientName)
		}
		return
	}

	// If envVarsToRemove is nil, clean all vars for the provider/client
	for envVar := range c.EnvKeys {
		c.cleanupEnvVar(envVar, provider, mcpClientName)
	}
}

// cleanupEnvVar removes entries for a specific environment variable based on provider/client.
// This is a helper function to avoid duplicating the filtering logic.
func (c *Config) cleanupEnvVar(envVar string, provider schemas.ModelProvider, mcpClientName string) {
	infos := c.EnvKeys[envVar]
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
		delete(c.EnvKeys, envVar)
	} else {
		c.EnvKeys[envVar] = filteredInfos
	}
}

// CleanupEnvKeysForKeys removes environment variable entries for specific keys that are being deleted.
// This function targets key-specific environment variables based on key IDs.
//
// Parameters:
//   - provider: Provider name the keys belong to
//   - keysToDelete: List of keys being deleted (uses their IDs to identify env vars to clean up)
func (c *Config) CleanupEnvKeysForKeys(provider schemas.ModelProvider, keysToDelete []schemas.Key) {
	// Create a set of key IDs to delete for efficient lookup
	keyIDsToDelete := make(map[string]bool)
	for _, key := range keysToDelete {
		keyIDsToDelete[key.ID] = true
	}

	// Iterate through all environment variables and remove entries for deleted keys
	for envVar, infos := range c.EnvKeys {
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
			delete(c.EnvKeys, envVar)
		} else {
			c.EnvKeys[envVar] = filteredInfos
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
func (c *Config) CleanupEnvKeysForUpdatedKeys(provider schemas.ModelProvider, keysToUpdate []schemas.Key, oldKeys []schemas.Key, mergedKeys []schemas.Key) {
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
	for envVar, infos := range c.EnvKeys {
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
				!c.isEnvVarReferenceChanging(mergedKeysMap[info.KeyID], oldKeysMap[info.KeyID], info.ConfigPath)

			if shouldKeep {
				filteredInfos = append(filteredInfos, info)
			}
		}

		// Update or delete the environment variable entry
		if len(filteredInfos) == 0 {
			delete(c.EnvKeys, envVar)
		} else {
			c.EnvKeys[envVar] = filteredInfos
		}
	}
}

// isEnvVarReferenceChanging checks if an environment variable reference is changing between old and merged key
func (c *Config) isEnvVarReferenceChanging(mergedKey, oldKey schemas.Key, configPath string) bool {
	// Extract the field name from the config path
	// e.g., "providers.vertex.keys[123].vertex_key_config.project_id" -> "project_id"
	pathParts := strings.Split(configPath, ".")
	if len(pathParts) < 2 {
		return false
	}
	fieldName := pathParts[len(pathParts)-1]

	// Get the old and merged values for this field
	oldValue := c.getFieldValue(oldKey, fieldName)
	mergedValue := c.getFieldValue(mergedKey, fieldName)

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
func (c *Config) getFieldValue(key schemas.Key, fieldName string) string {
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
func (c *Config) autoDetectProviders(ctx context.Context) {
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
							Name:   fmt.Sprintf("%s_auto_detected", envVar),
							Value:  apiKey,
							Models: []string{}, // Empty means all supported models
							Weight: 1.0,
						},
					},
					ConcurrencyAndBufferSize: &schemas.DefaultConcurrencyAndBufferSize,
				}

				// Add to providers map
				c.Providers[provider] = providerConfig

				// Track the environment variable
				c.EnvKeys[envVar] = append(c.EnvKeys[envVar], configstore.EnvKeyInfo{
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
		if c.ConfigStore != nil {
			if err := c.ConfigStore.UpdateProvidersConfig(ctx, c.Providers); err != nil {
				logger.Error("failed to update providers in store: %v", err)
			}
		}
	}
}

// processAzureKeyConfigEnvVars processes environment variables in Azure key configuration
func (c *Config) processAzureKeyConfigEnvVars(key *schemas.Key, provider schemas.ModelProvider, newEnvKeys map[string]struct{}) error {
	azureConfig := key.AzureKeyConfig

	// Process Endpoint
	processedEndpoint, envVar, err := c.processEnvValue(azureConfig.Endpoint)
	if err != nil {
		return err
	}
	if envVar != "" {
		newEnvKeys[envVar] = struct{}{}
		c.EnvKeys[envVar] = append(c.EnvKeys[envVar], configstore.EnvKeyInfo{
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
		processedAPIVersion, envVar, err := c.processEnvValue(*azureConfig.APIVersion)
		if err != nil {
			return err
		}
		if envVar != "" {
			newEnvKeys[envVar] = struct{}{}
			c.EnvKeys[envVar] = append(c.EnvKeys[envVar], configstore.EnvKeyInfo{
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
func (c *Config) processVertexKeyConfigEnvVars(key *schemas.Key, provider schemas.ModelProvider, newEnvKeys map[string]struct{}) error {
	vertexConfig := key.VertexKeyConfig

	// Process ProjectID
	processedProjectID, envVar, err := c.processEnvValue(vertexConfig.ProjectID)
	if err != nil {
		return err
	}
	if envVar != "" {
		newEnvKeys[envVar] = struct{}{}
		c.EnvKeys[envVar] = append(c.EnvKeys[envVar], configstore.EnvKeyInfo{
			EnvVar:     envVar,
			Provider:   provider,
			KeyType:    "vertex_config",
			ConfigPath: fmt.Sprintf("providers.%s.keys[%s].vertex_key_config.project_id", provider, key.ID),
			KeyID:      key.ID,
		})
	}
	vertexConfig.ProjectID = processedProjectID

	// Process Region
	processedRegion, envVar, err := c.processEnvValue(vertexConfig.Region)
	if err != nil {
		return err
	}
	if envVar != "" {
		newEnvKeys[envVar] = struct{}{}
		c.EnvKeys[envVar] = append(c.EnvKeys[envVar], configstore.EnvKeyInfo{
			EnvVar:     envVar,
			Provider:   provider,
			KeyType:    "vertex_config",
			ConfigPath: fmt.Sprintf("providers.%s.keys[%s].vertex_key_config.region", provider, key.ID),
			KeyID:      key.ID,
		})
	}
	vertexConfig.Region = processedRegion

	// Process AuthCredentials
	processedAuthCredentials, envVar, err := c.processEnvValue(vertexConfig.AuthCredentials)
	if err != nil {
		return err
	}
	if envVar != "" {
		newEnvKeys[envVar] = struct{}{}
		c.EnvKeys[envVar] = append(c.EnvKeys[envVar], configstore.EnvKeyInfo{
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
func (c *Config) processBedrockKeyConfigEnvVars(key *schemas.Key, provider schemas.ModelProvider, newEnvKeys map[string]struct{}) error {
	bedrockConfig := key.BedrockKeyConfig

	// Process AccessKey
	processedAccessKey, envVar, err := c.processEnvValue(bedrockConfig.AccessKey)
	if err != nil {
		return err
	}
	if envVar != "" {
		newEnvKeys[envVar] = struct{}{}
		c.EnvKeys[envVar] = append(c.EnvKeys[envVar], configstore.EnvKeyInfo{
			EnvVar:     envVar,
			Provider:   provider,
			KeyType:    "bedrock_config",
			ConfigPath: fmt.Sprintf("providers.%s.keys[%s].bedrock_key_config.access_key", provider, key.ID),
			KeyID:      key.ID,
		})
	}
	bedrockConfig.AccessKey = processedAccessKey

	// Process SecretKey
	processedSecretKey, envVar, err := c.processEnvValue(bedrockConfig.SecretKey)
	if err != nil {
		return err
	}
	if envVar != "" {
		newEnvKeys[envVar] = struct{}{}
		c.EnvKeys[envVar] = append(c.EnvKeys[envVar], configstore.EnvKeyInfo{
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
		processedSessionToken, envVar, err := c.processEnvValue(*bedrockConfig.SessionToken)
		if err != nil {
			return err
		}
		if envVar != "" {
			newEnvKeys[envVar] = struct{}{}
			c.EnvKeys[envVar] = append(c.EnvKeys[envVar], configstore.EnvKeyInfo{
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
		processedRegion, envVar, err := c.processEnvValue(*bedrockConfig.Region)
		if err != nil {
			return err
		}
		if envVar != "" {
			newEnvKeys[envVar] = struct{}{}
			c.EnvKeys[envVar] = append(c.EnvKeys[envVar], configstore.EnvKeyInfo{
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
		processedARN, envVar, err := c.processEnvValue(*bedrockConfig.ARN)
		if err != nil {
			return err
		}
		if envVar != "" {
			newEnvKeys[envVar] = struct{}{}
			c.EnvKeys[envVar] = append(c.EnvKeys[envVar], configstore.EnvKeyInfo{
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
func (c *Config) GetVectorStoreConfigRedacted(ctx context.Context) (*vectorstore.Config, error) {
	var err error
	var vectorStoreConfig *vectorstore.Config
	if c.ConfigStore != nil {
		vectorStoreConfig, err = c.ConfigStore.GetVectorStoreConfig(ctx)
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
		if redactedWeaviateConfig.APIKey != "" {
			redactedWeaviateConfig.APIKey = RedactKey(redactedWeaviateConfig.APIKey)
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

func (c *Config) AddProviderKeysToSemanticCacheConfig(config *schemas.PluginConfig) error {
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

	keys, err := c.GetProviderConfigRaw(schemas.ModelProvider(provider))
	if err != nil {
		return fmt.Errorf("failed to get provider config for %s: %w", provider, err)
	}

	configMap["keys"] = keys.Keys

	return nil
}

func (c *Config) RemoveProviderKeysFromSemanticCacheConfig(config *configstoreTables.TablePlugin) error {
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
