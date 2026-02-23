// Package lib provides core functionality for the Bifrost HTTP service,
// including context propagation, header management, and integration with monitoring systems.
package lib

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/maximhq/bifrost/framework/envutils"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/mcpcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/oauth2"
	plugins "github.com/maximhq/bifrost/framework/plugins"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/semanticcache"
	"gorm.io/gorm"
)

// StreamChunkInterceptor intercepts streaming chunks before they're sent to clients.
// Implementations can modify, filter, or observe chunks in real-time.
// This interface enables proper dependency injection for streaming handlers.
type StreamChunkInterceptor interface {
	// InterceptChunk processes a chunk before it's written to the client.
	// Returns the (potentially modified) chunk, or nil to skip the chunk entirely.
	InterceptChunk(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error)
}

// HandlerStore provides access to runtime configuration values for handlers.
// This interface allows handlers to access only the configuration they need
// without depending on the entire ConfigStore, improving testability and decoupling.
type HandlerStore interface {
	// ShouldAllowDirectKeys returns whether direct API keys in headers are allowed
	ShouldAllowDirectKeys() bool
	// GetHeaderFilterConfig returns the global header filter configuration
	GetHeaderFilterConfig() *configstoreTables.GlobalHeaderFilterConfig
	// GetAvailableProviders returns the list of available providers
	GetAvailableProviders() []schemas.ModelProvider
	// GetStreamChunkInterceptor returns the interceptor for streaming chunks.
	// Returns nil if no plugins are loaded or streaming interception is not needed.
	GetStreamChunkInterceptor() StreamChunkInterceptor
	// GetAsyncJobExecutor returns the cached async job executor.
	// Returns nil if LogsStore or governance plugin is not configured.
	GetAsyncJobExecutor() *logstore.AsyncJobExecutor
	// GetAsyncJobResultTTL returns the default TTL for async job results in seconds.
	GetAsyncJobResultTTL() int
}

// Retry backoff constants for validation
const (
	MinRetryBackoff = 100 * time.Millisecond     // Minimum retry backoff: 100ms
	MaxRetryBackoff = 1000000 * time.Millisecond // Maximum retry backoff: 1000000ms (1000 seconds)
)

const (
	DBLookupMaxRetries = 5
	DBLookupDelay      = 1 * time.Second
)

// getWeight safely dereferences a *float64 weight pointer, returning 1.0 as default if nil.
// This allows distinguishing between "not set" (nil -> 1.0) and "explicitly set to 0" (0.0).
func getWeight(w *float64) float64 {
	if w == nil {
		return 1.0
	}
	return *w
}

// ConfigData represents the configuration data for the Bifrost HTTP transport.
// It contains the client configuration, provider configurations, MCP configuration,
// vector store configuration, config store configuration, and logs store configuration.
type ConfigData struct {
	Client        *configstore.ClientConfig `json:"client"`
	EncryptionKey *schemas.EnvVar           `json:"encryption_key"`
	// Deprecated: Use GovernanceConfig.AuthConfig instead
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

// UnmarshalJSON unmarshals the ConfigData from JSON using internal unmarshallers
// for VectorStoreConfig, ConfigStoreConfig, and LogsStoreConfig to ensure proper
// type safety and configuration parsing.
func (cd *ConfigData) UnmarshalJSON(data []byte) error {
	// First, unmarshal into a temporary struct to get all fields except the complex configs
	type TempConfigData struct {
		FrameworkConfig   json.RawMessage                       `json:"framework,omitempty"`
		Client            *configstore.ClientConfig             `json:"client"`
		EncryptionKey     *schemas.EnvVar                       `json:"encryption_key"`
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
	// Initialize providers map if nil
	if cd.Providers == nil {
		cd.Providers = make(map[string]configstore.ProviderConfig)
	}
	// Extract provider configs from virtual keys.
	// Keys can be either full definitions (with value) or references (name only).
	// References are resolved by looking up the key by name from the providers section.
	// NOTE: Only FULL key definitions (with Value) should be added to the provider.
	// Reference lookups are for virtual key resolution only - they should NOT be added
	// back to the provider since they already exist there.
	if cd.Governance != nil && cd.Governance.VirtualKeys != nil {
		for _, virtualKey := range cd.Governance.VirtualKeys {
			if virtualKey.ProviderConfigs != nil {
				for _, providerConfig := range virtualKey.ProviderConfigs {
					// Only collect keys with Value (full definitions) to add to provider
					var keysToAddToProvider []schemas.Key
					for _, tableKey := range providerConfig.Keys {
						if tableKey.Value.GetValue() != "" {
							// Full key definition - add to provider
							keysToAddToProvider = append(keysToAddToProvider, schemas.Key{
								ID:               tableKey.KeyID,
								Name:             tableKey.Name,
								Value:            tableKey.Value,
								Models:           tableKey.Models,
								Weight:           getWeight(tableKey.Weight),
								Enabled:          tableKey.Enabled,
								UseForBatchAPI:   tableKey.UseForBatchAPI,
								AzureKeyConfig:   tableKey.AzureKeyConfig,
								VertexKeyConfig:  tableKey.VertexKeyConfig,
								BedrockKeyConfig: tableKey.BedrockKeyConfig,
								ConfigHash:       tableKey.ConfigHash,
							})
						}
						// Reference lookups (no Value) are NOT added to provider - they already exist there
					}

					// Merge or create provider entry - only for full key definitions
					if len(keysToAddToProvider) > 0 {
						if existing, ok := cd.Providers[providerConfig.Provider]; ok {
							existing.Keys = append(existing.Keys, keysToAddToProvider...)
							cd.Providers[providerConfig.Provider] = existing
						} else {
							cd.Providers[providerConfig.Provider] = configstore.ProviderConfig{
								Keys: keysToAddToProvider,
							}
						}
					}
				}
			}
		}
	}
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
	ProxyConfig      *configstoreTables.GlobalProxyConfig

	// Plugin Storage (SINGLE SOURCE OF TRUTH)
	// All plugins are stored in BasePlugins. Interface-specific caches are
	// derived views rebuilt automatically on any plugin change.
	// Lock-free reads via atomic.Pointer for hot-path performance.
	pluginsMu            sync.Mutex                                    // Protects structural changes to BasePlugins
	BasePlugins          atomic.Pointer[[]schemas.BasePlugin]          // Master list of all plugins
	LLMPlugins           atomic.Pointer[[]schemas.LLMPlugin]           // Derived cache (auto-rebuilt)
	MCPPlugins           atomic.Pointer[[]schemas.MCPPlugin]           // Derived cache (auto-rebuilt)
	HTTPTransportPlugins atomic.Pointer[[]schemas.HTTPTransportPlugin] // Derived cache (auto-rebuilt)
	PluginLoader         plugins.PluginLoader

	// Plugin metadata from config file/database
	PluginConfigs []*schemas.PluginConfig

	// Plugin status tracking (co-located with plugin instances)
	pluginStatusMu sync.RWMutex
	pluginStatus   map[string]schemas.PluginStatus // name -> status

	OAuthProvider      *oauth2.OAuth2Provider
	TokenRefreshWorker *oauth2.TokenRefreshWorker

	// Async job executor (initialized during setup if LogsStore + governance are available)
	AsyncJobExecutor *logstore.AsyncJobExecutor

	// Catalog managers
	ModelCatalog *modelcatalog.ModelCatalog
	MCPCatalog   *mcpcatalog.MCPCatalog

	// Optional event broadcaster for real-time updates (e.g., WebSocket).
	// Set by HTTP server at startup; may be nil in non-HTTP usage.
	EventBroadcaster schemas.EventBroadcaster
}

var DefaultClientConfig = configstore.ClientConfig{
	DropExcessRequests:      false,
	PrometheusLabels:        []string{},
	InitialPoolSize:         schemas.DefaultInitialPoolSize,
	EnableLogging:           true,
	DisableContentLogging:   false,
	EnableGovernance:        true,
	EnforceAuthOnInference:  false,
	AllowDirectKeys:         false,
	AllowedOrigins:          []string{"*"},
	AllowedHeaders:          []string{},
	MaxRequestBodySizeMB:    100,
	MCPAgentDepth:           10,
	MCPToolExecutionTimeout: 30,
	MCPCodeModeBindingLevel: string(schemas.CodeModeBindingLevelServer),
	EnableLiteLLMFallbacks:  false,
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
		Providers:  make(map[schemas.ModelProvider]configstore.ProviderConfig),
		LLMPlugins: atomic.Pointer[[]schemas.LLMPlugin]{},
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
			return loadConfigFromDefaults(ctx, config, configDBPath, logsDBPath)
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}
	// If file exists, we will do a quick check if that file includes "$schema":"https://www.getbifrost.ai/schema", If not we will show a warning in a box - yellow color
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("failed to unmarshal schema: %w", err)
	}
	if schema["$schema"] != "https://www.getbifrost.ai/schema" {
		// Print warning in yellow ASCII box
		yellowColor := "\033[33m"
		resetColor := "\033[0m"
		message := fmt.Sprintf("config file %s does not include \"$schema\":\"https://www.getbifrost.ai/schema\". Use our official schema file to avoid unexpected behavior.", absConfigFilePath)

		// Fixed box width, content width is box - 4 (for "║ " and " ║")
		boxWidth := 100
		contentWidth := boxWidth - 4

		// Word wrap the message into lines
		words := strings.Fields(message)
		var lines []string
		currentLine := ""
		for _, word := range words {
			if currentLine == "" {
				currentLine = word
			} else if len(currentLine)+1+len(word) <= contentWidth {
				currentLine += " " + word
			} else {
				lines = append(lines, currentLine)
				currentLine = word
			}
		}
		if currentLine != "" {
			lines = append(lines, currentLine)
		}

		// Print top border
		fmt.Printf("%s╔%s╗%s\n", yellowColor, strings.Repeat("═", boxWidth-2), resetColor)

		// Print each line with proper padding
		for _, l := range lines {
			padding := contentWidth - len(l)
			if padding < 0 {
				padding = 0
			}
			fmt.Printf("%s║ %s%s ║%s\n", yellowColor, l, strings.Repeat(" ", padding), resetColor)
		}

		// Print bottom border
		fmt.Printf("%s╚%s╝%s\n", yellowColor, strings.Repeat("═", boxWidth-2), resetColor)
		fmt.Println("")
		logger.Warn("config file %s does not include \"$schema\":\"https://www.getbifrost.ai/schema\". Use our official schema file to avoid unexpected behavior.", absConfigFilePath)
	}
	// Validate config file against the schema - fatal on validation errors
	if err := ValidateConfigSchema(data); err != nil {
		logger.Error("config validation failed: %v. You can find the official schema at https://www.getbifrost.ai/schema. Some features may not work as expected unless you fix the config file.", err)
	}
	// If config file exists, we will use it to bootstrap config tables
	logger.Info("loading configuration from: %s", absConfigFilePath)
	return loadConfigFromFile(ctx, config, data)
}

// loadConfigFromFile initializes configuration from a JSON config file.
// It merges config file data with existing database config, with store taking priority.
func loadConfigFromFile(ctx context.Context, config *Config, data []byte) (*Config, error) {
	var configData ConfigData
	if err := json.Unmarshal(data, &configData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}
	var err error
	// Initialize encryption before stores so BeforeSave hooks and EncryptPlaintextRows work correctly
	if err = initEncryptionFromFile(&configData); err != nil {
		return nil, err
	}
	// Initialize stores from config file
	if err = initStoresFromFile(ctx, config, &configData); err != nil {
		return nil, err
	}
	// From now on, config store gets priority if enabled and we find data.
	// If we don't find any data in the store, then we resort to config file.
	// NOTE: We follow a standard practice: store -> config file -> update store.
	// Load client config
	loadClientConfigFromFile(ctx, config, &configData)
	// Load providers config with hash reconciliation
	if err = loadProvidersFromFile(ctx, config, &configData); err != nil {
		return nil, err
	}
	// Load MCP config
	loadMCPConfigFromFile(ctx, config, &configData)
	// Load governance config
	loadGovernanceConfigFromFile(ctx, config, &configData)
	// Load auth config
	loadAuthConfigFromFile(ctx, config, &configData)
	// Load plugins
	loadPluginsFromFile(ctx, config, &configData)
	// Initialize framework config and pricing manager
	initFrameworkConfigFromFile(ctx, config, &configData)
	// Sync encryption: encrypt any plaintext rows written during config loading
	syncEncryption(ctx, config)
	return config, nil
}

// initStoresFromFile initializes config, logs, and vector stores from config file
func initStoresFromFile(ctx context.Context, config *Config, configData *ConfigData) error {
	var err error
	// Initialize config store
	if configData.ConfigStoreConfig != nil && configData.ConfigStoreConfig.Enabled {
		config.ConfigStore, err = configstore.NewConfigStore(ctx, configData.ConfigStoreConfig, logger)
		if err != nil {
			return err
		}
		logger.Info("config store initialized")
		// Clear restart required flag on server startup
		if err = config.ConfigStore.ClearRestartRequiredConfig(ctx); err != nil {
			logger.Warn("failed to clear restart required config: %v", err)
		}
	}
	// Initialize log store
	if configData.LogsStoreConfig != nil && configData.LogsStoreConfig.Enabled {
		config.LogsStore, err = logstore.NewLogStore(ctx, configData.LogsStoreConfig, logger)
		if err != nil {
			return err
		}
		logger.Info("logs store initialized")
	}
	// Initialize vector store
	if configData.VectorStoreConfig != nil && configData.VectorStoreConfig.Enabled {
		logger.Info("connecting to vectorstore")
		config.VectorStore, err = vectorstore.NewVectorStore(ctx, configData.VectorStoreConfig, logger)
		if err != nil {
			logger.Fatal("failed to connect to vector store: %v", err)
		}
		if config.ConfigStore != nil {
			if err = config.ConfigStore.UpdateVectorStoreConfig(ctx, configData.VectorStoreConfig); err != nil {
				logger.Warn("failed to update vector store config: %v", err)
			}
		}
	}
	return nil
}

// loadClientConfigFromFile loads and merges client config from file with store using hash-based reconciliation
func loadClientConfigFromFile(ctx context.Context, config *Config, configData *ConfigData) {
	var clientConfig *configstore.ClientConfig
	var err error
	if config.ConfigStore != nil {
		clientConfig, err = config.ConfigStore.GetClientConfig(ctx)
		if err != nil {
			logger.Warn("failed to get client config from store: %v", err)
		}
	}
	// Case 1: No config in DB - use file config (or defaults)
	if clientConfig == nil {
		logger.Debug("client config not found in store, using config file")
		if configData.Client != nil {
			config.ClientConfig = *configData.Client
			if config.ClientConfig.MaxRequestBodySizeMB == 0 {
				config.ClientConfig.MaxRequestBodySizeMB = DefaultClientConfig.MaxRequestBodySizeMB
			}
			// Generate hash for the file config
			fileHash, hashErr := configData.Client.GenerateClientConfigHash()
			if hashErr != nil {
				logger.Warn("failed to generate client config hash: %v", hashErr)
			} else {
				config.ClientConfig.ConfigHash = fileHash
			}
		} else {
			config.ClientConfig = DefaultClientConfig
			// Generate hash for default config
			defaultHash, hashErr := config.ClientConfig.GenerateClientConfigHash()
			if hashErr != nil {
				logger.Warn("failed to generate default client config hash: %v", hashErr)
			} else {
				config.ClientConfig.ConfigHash = defaultHash
			}
		}
		if config.ConfigStore != nil {
			logger.Debug("updating client config in store")
			if err = config.ConfigStore.UpdateClientConfig(ctx, &config.ClientConfig); err != nil {
				logger.Warn("failed to update client config: %v", err)
			}
		}
		return
	}
	// Case 2: Config exists in DB
	config.ClientConfig = *clientConfig
	// For backward compatibility, handle cases where max request body size is not set
	if config.ClientConfig.MaxRequestBodySizeMB == 0 {
		config.ClientConfig.MaxRequestBodySizeMB = DefaultClientConfig.MaxRequestBodySizeMB
	}
	// Case 2a: No file config - use DB config as-is
	if configData.Client == nil {
		logger.Debug("no client config in file, using DB config")
		return
	}
	// Case 2b: Both DB and file config exist - use hash-based reconciliation
	fileHash, hashErr := configData.Client.GenerateClientConfigHash()
	if hashErr != nil {
		logger.Warn("failed to generate client config hash from file: %v", hashErr)
		return
	}
	if clientConfig.ConfigHash != fileHash {
		// Hash mismatch - config.json was changed, sync from file
		logger.Info("client config was updated in config.json, syncing. Note that: file config takes precedence.")
		config.ClientConfig = *configData.Client
		config.ClientConfig.ConfigHash = fileHash
		// Apply defaults for zero values
		if config.ClientConfig.MaxRequestBodySizeMB == 0 {
			config.ClientConfig.MaxRequestBodySizeMB = DefaultClientConfig.MaxRequestBodySizeMB
		}
		// Update store with file config
		if config.ConfigStore != nil {
			logger.Debug("updating client config in store from file")
			if err = config.ConfigStore.UpdateClientConfig(ctx, &config.ClientConfig); err != nil {
				logger.Warn("failed to update client config: %v", err)
			}
		}
	} else {
		// Hash matches - keep DB config (preserves UI changes)
		logger.Debug("client config hash matches, keeping DB config")
	}
}

// loadProvidersFromFile loads and merges providers from file with store using hash reconciliation
func loadProvidersFromFile(ctx context.Context, config *Config, configData *ConfigData) error {
	var providersInConfigStore map[schemas.ModelProvider]configstore.ProviderConfig
	var err error
	if config.ConfigStore != nil {
		logger.Debug("getting providers config from store")
		providersInConfigStore, err = config.ConfigStore.GetProvidersConfig(ctx)
		if err != nil {
			logger.Warn("failed to get providers config from store: %v", err)
		}
	}
	if providersInConfigStore == nil {
		logger.Debug("no providers config found in store, processing from config file")
		providersInConfigStore = make(map[schemas.ModelProvider]configstore.ProviderConfig)
	}
	// Process provider configurations from file
	if configData.Providers != nil {
		for providerName, providerCfgInFile := range configData.Providers {
			if err = processProviderFromFile(config, providerName, providerCfgInFile, providersInConfigStore); err != nil {
				logger.Warn("failed to process provider %s: %v", providerName, err)
			}
		}
	} else {
		config.autoDetectProviders(ctx)
	}
	// Update store and config
	if config.ConfigStore != nil {
		logger.Debug("updating providers config in store")
		if err = config.ConfigStore.UpdateProvidersConfig(ctx, providersInConfigStore); err != nil {
			logger.Fatal("failed to update providers config: %v", err)
		}
	}
	config.Providers = providersInConfigStore
	return nil
}

// processProviderFromFile processes a single provider configuration from config file
func processProviderFromFile(
	config *Config,
	providerName string,
	providerCfgInFile configstore.ProviderConfig,
	providersInConfigStore map[schemas.ModelProvider]configstore.ProviderConfig,
) error {
	provider := schemas.ModelProvider(strings.ToLower(providerName))

	// Process environment variables in keys (including key-level configs)
	for i, providerKeyInFile := range providerCfgInFile.Keys {
		if providerKeyInFile.ID == "" {
			providerCfgInFile.Keys[i].ID = uuid.NewString()
		}
	}
	// Generate hash from config.json provider config
	fileProviderConfigHash, err := providerCfgInFile.GenerateConfigHash(string(provider))
	if err != nil {
		logger.Warn("failed to generate config hash for %s: %v", provider, err)
	}
	providerCfgInFile.ConfigHash = fileProviderConfigHash
	// Merge with existing config using hash-based reconciliation
	mergeProviderWithHash(provider, providerCfgInFile, providersInConfigStore)
	return nil
}

// mergeProviderWithHash merges provider config using hash-based reconciliation
func mergeProviderWithHash(
	provider schemas.ModelProvider,
	providerCfgInFile configstore.ProviderConfig,
	providersInConfigStore map[schemas.ModelProvider]configstore.ProviderConfig,
) {
	existingCfg, exists := providersInConfigStore[provider]
	if !exists {
		// New provider - add from config.json
		providersInConfigStore[provider] = providerCfgInFile
		return
	}
	// Provider exists in DB - compare hashes
	if existingCfg.ConfigHash != providerCfgInFile.ConfigHash {
		// Hash mismatch - config.json was changed, sync from file
		logger.Debug("config hash mismatch for provider %s, syncing from config file", provider)
		mergedKeys := mergeProviderKeys(provider, providerCfgInFile.Keys, existingCfg.Keys)
		providerCfgInFile.Keys = mergedKeys
		providersInConfigStore[provider] = providerCfgInFile
	} else {
		// Provider hash matches - but still check individual keys
		logger.Debug("config hash matches for provider %s, checking individual keys", provider)
		mergedKeys := reconcileProviderKeys(provider, providerCfgInFile.Keys, existingCfg.Keys)
		existingCfg.Keys = mergedKeys
		providersInConfigStore[provider] = existingCfg
	}
}

// mergeProviderKeys syncs keys when provider hash has changed (file is source of truth).
// Keys in file are kept, keys only in DB are removed.
func mergeProviderKeys(provider schemas.ModelProvider, fileKeys, dbKeys []schemas.Key) []schemas.Key {
	mergedKeys := fileKeys
	for _, dbKey := range dbKeys {
		found := false
		for i, fileKey := range fileKeys {
			// Compare by hash to detect changes
			fileKeyHash, err := configstore.GenerateKeyHash(fileKey)
			if err != nil {
				logger.Warn("failed to generate key hash for file key %s (%s): %v, falling back to name comparison", fileKey.Name, provider, err)
				if fileKey.Name == dbKey.Name {
					fileKeys[i].ID = dbKey.ID
					fileKeys[i].Status = dbKey.Status
					fileKeys[i].Description = dbKey.Description
					found = true
					break
				}
				continue
			}
			// Assign ConfigHash to file key (marks it as from config.json)
			fileKeys[i].ConfigHash = fileKeyHash
			// Use stored ConfigHash for comparison if available
			if dbKey.ConfigHash != "" {
				if fileKeyHash == dbKey.ConfigHash || fileKey.Name == dbKey.Name {
					fileKeys[i].ID = dbKey.ID
					fileKeys[i].Status = dbKey.Status
					fileKeys[i].Description = dbKey.Description
					found = true
					break
				}
			} else {
				// No stored hash (legacy) - fall back to generating fresh hash
				dbKeyHash, err := configstore.GenerateKeyHash(schemas.Key{
					Name:             dbKey.Name,
					Value:            dbKey.Value,
					Models:           dbKey.Models,
					Weight:           dbKey.Weight,
					AzureKeyConfig:   dbKey.AzureKeyConfig,
					VertexKeyConfig:  dbKey.VertexKeyConfig,
					BedrockKeyConfig: dbKey.BedrockKeyConfig,
				})
				if err != nil {
					logger.Warn("failed to generate key hash for db key %s (%s): %v, falling back to name comparison", dbKey.Name, provider, err)
					if fileKey.Name == dbKey.Name {
						fileKeys[i].ID = dbKey.ID
						fileKeys[i].Status = dbKey.Status
						fileKeys[i].Description = dbKey.Description
						found = true
						break
					}
					continue
				}
				if fileKeyHash == dbKeyHash || fileKey.Name == dbKey.Name {
					fileKeys[i].ID = dbKey.ID
					fileKeys[i].Status = dbKey.Status
					fileKeys[i].Description = dbKey.Description
					found = true
					break
				}
			}
		}
		if !found {
			// Key exists in DB but not in file - skip it (file is source of truth when hash changed)
			logger.Debug("key %s exists in DB but not in file for provider %s, removing", dbKey.Name, provider)
		}
	}
	return mergedKeys
}

// reconcileProviderKeys reconciles keys when provider hash matches
func reconcileProviderKeys(provider schemas.ModelProvider, fileKeys, dbKeys []schemas.Key) []schemas.Key {
	mergedKeys := make([]schemas.Key, 0)
	fileKeysByName := make(map[string]int) // name -> index in file keys
	for i, fileKey := range fileKeys {
		fileKeysByName[fileKey.Name] = i
	}
	// Process DB keys - check if they exist in file and compare hashes
	for _, dbKey := range dbKeys {
		if fileIdx, exists := fileKeysByName[dbKey.Name]; exists {
			fileKey := fileKeys[fileIdx]
			fileKeyHash, err := configstore.GenerateKeyHash(fileKey)
			if err != nil {
				logger.Warn("failed to generate key hash for file key %s (%s): %v", fileKey.Name, provider, err)
				mergedKeys = append(mergedKeys, dbKey)
				delete(fileKeysByName, dbKey.Name)
				continue
			}

			// Compare file hash against STORED config hash (not fresh hash from DB values)
			// This ensures DB updates are preserved when config.json hasn't changed
			if dbKey.ConfigHash != "" {
				if fileKeyHash == dbKey.ConfigHash {
					// File unchanged - keep DB version (preserves user updates)
					mergedKeys = append(mergedKeys, dbKey)
				} else {
					// File changed - use file version but preserve ID and set ConfigHash
					logger.Debug("key %s changed in config file for provider %s, updating", fileKey.Name, provider)
					fileKey.ID = dbKey.ID
					fileKey.ConfigHash = fileKeyHash
					fileKey.Status = dbKey.Status
					fileKey.Description = dbKey.Description
					mergedKeys = append(mergedKeys, fileKey)
				}
			} else {
				// No stored hash (legacy) - fall back to generating fresh hash for comparison
				dbKeyHash, err := configstore.GenerateKeyHash(schemas.Key{
					Name:             dbKey.Name,
					Value:            dbKey.Value,
					Models:           dbKey.Models,
					Weight:           dbKey.Weight,
					AzureKeyConfig:   dbKey.AzureKeyConfig,
					VertexKeyConfig:  dbKey.VertexKeyConfig,
					BedrockKeyConfig: dbKey.BedrockKeyConfig,
				})
				if err != nil {
					logger.Warn("failed to generate key hash for db key %s (%s): %v", dbKey.Name, provider, err)
					mergedKeys = append(mergedKeys, dbKey)
					delete(fileKeysByName, dbKey.Name)
					continue
				}
				if fileKeyHash != dbKeyHash {
					// Key changed in file - use file version but preserve ID and set ConfigHash
					logger.Debug("key %s changed in config file for provider %s, updating", fileKey.Name, provider)
					fileKey.ID = dbKey.ID
					fileKey.ConfigHash = fileKeyHash
					fileKey.Status = dbKey.Status
					fileKey.Description = dbKey.Description
					mergedKeys = append(mergedKeys, fileKey)
				} else {
					// Key unchanged - keep DB version
					mergedKeys = append(mergedKeys, dbKey)
				}
			}
			delete(fileKeysByName, dbKey.Name) // Mark as processed
		} else {
			// Key only in DB - preserve it (added via dashboard)
			mergedKeys = append(mergedKeys, dbKey)
		}
	}
	// Add keys only in file (new keys from config.json)
	for _, idx := range fileKeysByName {
		fileKey := fileKeys[idx]
		// Generate and assign ConfigHash for new keys from config.json
		fileKeyHash, err := configstore.GenerateKeyHash(fileKey)
		if err != nil {
			logger.Warn("failed to generate key hash for new file key %s (%s): %v", fileKey.Name, provider, err)
		} else {
			fileKey.ConfigHash = fileKeyHash
		}
		mergedKeys = append(mergedKeys, fileKey)
	}
	return mergedKeys
}

// loadMCPConfigFromFile loads and merges MCP config from file
func loadMCPConfigFromFile(ctx context.Context, config *Config, configData *ConfigData) {
	if config.ConfigStore == nil {
		if configData.MCP != nil && len(configData.MCP.ClientConfigs) > 0 {
			logger.Warn("config store is disabled - MCP manager will not be initialized. MCP clients require config store for persistence.")
		}
		return
	}
	if config.ConfigStore != nil {
		logger.Debug("getting MCP config from store")
		tableMCPConfig, err := config.ConfigStore.GetMCPConfig(ctx)
		if err != nil {
			logger.Warn("failed to get MCP config from store: %v", err)
		} else if tableMCPConfig != nil {
			config.MCPConfig = tableMCPConfig
		}
	}

	if config.MCPConfig != nil {
		// Merge with config file if present
		if configData.MCP != nil && len(configData.MCP.ClientConfigs) > 0 {
			mergeMCPConfig(ctx, config, configData, config.MCPConfig)
		}
	} else if configData.MCP != nil {
		// MCP config not in store, use config file
		logger.Debug("no MCP config found in store, processing from config file")
		config.MCPConfig = configData.MCP
		if config.ConfigStore != nil && config.MCPConfig != nil {
			logger.Debug("updating MCP config in store")
			for _, clientConfig := range config.MCPConfig.ClientConfigs {
				if clientConfig != nil {
					if clientConfig.ID == "" {
						clientConfig.ID = uuid.NewString()
					}
					if err := config.ConfigStore.CreateMCPClientConfig(ctx, clientConfig); err != nil {
						logger.Warn("failed to create MCP client config: %v", err)
					}
				}
			}
		}
	}
}

// mergeMCPConfig merges MCP config from file with store
func mergeMCPConfig(ctx context.Context, config *Config, configData *ConfigData, mcpConfig *schemas.MCPConfig) {
	logger.Debug("merging MCP config from config file with store")

	if configData.MCP == nil {
		return
	}
	tempMCPConfig := configData.MCP
	config.MCPConfig = tempMCPConfig
	// Merge ClientConfigs arrays by ClientID or Name
	clientConfigsToAdd := make([]*schemas.MCPClientConfig, 0)
	for _, newClientConfig := range tempMCPConfig.ClientConfigs {
		if newClientConfig.ID == "" {
			newClientConfig.ID = uuid.NewString()
		}
		found := false
		for _, existingClientConfig := range mcpConfig.ClientConfigs {
			if newClientConfig.Name != "" && existingClientConfig.Name == newClientConfig.Name {
				found = true
				break
			}
		}
		if !found {
			clientConfigsToAdd = append(clientConfigsToAdd, newClientConfig)
		}
	}
	// Add new client configs to existing ones
	config.MCPConfig.ClientConfigs = append(mcpConfig.ClientConfigs, clientConfigsToAdd...)
	// Update store with merged config
	if config.ConfigStore != nil && len(clientConfigsToAdd) > 0 {
		logger.Debug("updating MCP config in store with %d new client configs", len(clientConfigsToAdd))
		for _, clientConfig := range clientConfigsToAdd {
			if clientConfig != nil {
				if err := config.ConfigStore.CreateMCPClientConfig(ctx, clientConfig); err != nil {
					logger.Warn("failed to create MCP client config: %v", err)
				}
			}
		}
	}
}

// loadGovernanceConfigFromFile loads and merges governance config from file
func loadGovernanceConfigFromFile(ctx context.Context, config *Config, configData *ConfigData) {
	var governanceConfig *configstore.GovernanceConfig
	var err error
	// Checking from the store
	if config.ConfigStore != nil {
		logger.Debug("getting governance config from store")
		governanceConfig, err = config.ConfigStore.GetGovernanceConfig(ctx)
		if err != nil {
			logger.Warn("failed to get governance config from store: %v", err)
		}
	} else {
		logger.Debug("config.ConfigStore is nil, skipping store lookup")
	}
	// Merging config
	if governanceConfig != nil {
		config.GovernanceConfig = governanceConfig
		// Merge with config file if present
		if configData.Governance != nil {
			mergeGovernanceConfig(ctx, config, configData, governanceConfig)
		}
	} else if configData.Governance != nil {
		// No governance config in store, use config file
		logger.Debug("no governance config found in store, processing from config file")
		config.GovernanceConfig = configData.Governance
		createGovernanceConfigInStore(ctx, config)
	} else {
		logger.Debug("no governance config in store or config file")
	}
}

// mergeGovernanceConfig merges governance config from file with store
func mergeGovernanceConfig(ctx context.Context, config *Config, configData *ConfigData, governanceConfig *configstore.GovernanceConfig) {
	logger.Debug("merging governance config from config file with store")
	// Merge Budgets by ID with hash comparison
	budgetsToAdd := make([]configstoreTables.TableBudget, 0)
	budgetsToUpdate := make([]configstoreTables.TableBudget, 0)
	for i, newBudget := range configData.Governance.Budgets {
		fileBudgetHash, err := configstore.GenerateBudgetHash(newBudget)
		if err != nil {
			logger.Warn("failed to generate budget hash for %s: %v", newBudget.ID, err)
			continue
		}
		configData.Governance.Budgets[i].ConfigHash = fileBudgetHash
		// Replacing budgets
		found := false
		for j, existingBudget := range governanceConfig.Budgets {
			if existingBudget.ID == newBudget.ID {
				found = true
				if existingBudget.ConfigHash != fileBudgetHash {
					logger.Debug("config hash mismatch for budget %s, syncing from config file", newBudget.ID)
					configData.Governance.Budgets[i].ConfigHash = fileBudgetHash
					budgetsToUpdate = append(budgetsToUpdate, configData.Governance.Budgets[i])
					governanceConfig.Budgets[j] = configData.Governance.Budgets[i]
				} else {
					logger.Debug("config hash matches for budget %s, keeping DB config", newBudget.ID)
				}
				break
			}
		}
		if !found {
			configData.Governance.Budgets[i].ConfigHash = fileBudgetHash
			budgetsToAdd = append(budgetsToAdd, configData.Governance.Budgets[i])
		}
	}
	// Merge RateLimits by ID with hash comparison
	rateLimitsToAdd := make([]configstoreTables.TableRateLimit, 0)
	rateLimitsToUpdate := make([]configstoreTables.TableRateLimit, 0)
	for i, newRateLimit := range configData.Governance.RateLimits {
		fileRLHash, err := configstore.GenerateRateLimitHash(newRateLimit)
		if err != nil {
			logger.Warn("failed to generate rate limit hash for %s: %v", newRateLimit.ID, err)
			continue
		}
		configData.Governance.RateLimits[i].ConfigHash = fileRLHash

		found := false
		for j, existingRateLimit := range governanceConfig.RateLimits {
			if existingRateLimit.ID == newRateLimit.ID {
				found = true
				if existingRateLimit.ConfigHash != fileRLHash {
					logger.Debug("config hash mismatch for rate limit %s, syncing from config file", newRateLimit.ID)
					configData.Governance.RateLimits[i].ConfigHash = fileRLHash
					rateLimitsToUpdate = append(rateLimitsToUpdate, configData.Governance.RateLimits[i])
					governanceConfig.RateLimits[j] = configData.Governance.RateLimits[i]
				} else {
					logger.Debug("config hash matches for rate limit %s, keeping DB config", newRateLimit.ID)
				}
				break
			}
		}
		if !found {
			configData.Governance.RateLimits[i].ConfigHash = fileRLHash
			rateLimitsToAdd = append(rateLimitsToAdd, configData.Governance.RateLimits[i])
		}
	}
	// Merge Customers by ID with hash comparison
	customersToAdd := make([]configstoreTables.TableCustomer, 0)
	customersToUpdate := make([]configstoreTables.TableCustomer, 0)
	for i, newCustomer := range configData.Governance.Customers {
		fileCustomerHash, err := configstore.GenerateCustomerHash(newCustomer)
		if err != nil {
			logger.Warn("failed to generate customer hash for %s: %v", newCustomer.ID, err)
			continue
		}
		configData.Governance.Customers[i].ConfigHash = fileCustomerHash

		found := false
		for j, existingCustomer := range governanceConfig.Customers {
			if existingCustomer.ID == newCustomer.ID {
				found = true
				if existingCustomer.ConfigHash != fileCustomerHash {
					logger.Debug("config hash mismatch for customer %s, syncing from config file", newCustomer.ID)
					configData.Governance.Customers[i].ConfigHash = fileCustomerHash
					customersToUpdate = append(customersToUpdate, configData.Governance.Customers[i])
					governanceConfig.Customers[j] = configData.Governance.Customers[i]
				} else {
					logger.Debug("config hash matches for customer %s, keeping DB config", newCustomer.ID)
				}
				break
			}
		}
		if !found {
			configData.Governance.Customers[i].ConfigHash = fileCustomerHash
			customersToAdd = append(customersToAdd, configData.Governance.Customers[i])
		}
	}
	// Merge Teams by ID with hash comparison
	teamsToAdd := make([]configstoreTables.TableTeam, 0)
	teamsToUpdate := make([]configstoreTables.TableTeam, 0)
	for i, newTeam := range configData.Governance.Teams {
		fileTeamHash, err := configstore.GenerateTeamHash(newTeam)
		if err != nil {
			logger.Warn("failed to generate team hash for %s: %v", newTeam.ID, err)
			continue
		}
		configData.Governance.Teams[i].ConfigHash = fileTeamHash

		found := false
		for j, existingTeam := range governanceConfig.Teams {
			if existingTeam.ID == newTeam.ID {
				found = true
				if existingTeam.ConfigHash != fileTeamHash {
					logger.Debug("config hash mismatch for team %s, syncing from config file", newTeam.ID)
					configData.Governance.Teams[i].ConfigHash = fileTeamHash
					teamsToUpdate = append(teamsToUpdate, configData.Governance.Teams[i])
					governanceConfig.Teams[j] = configData.Governance.Teams[i]
				} else {
					logger.Debug("config hash matches for team %s, keeping DB config", newTeam.ID)
				}
				break
			}
		}
		if !found {
			configData.Governance.Teams[i].ConfigHash = fileTeamHash
			teamsToAdd = append(teamsToAdd, configData.Governance.Teams[i])
		}
	}
	// Merge VirtualKeys by ID with hash comparison
	virtualKeysToAdd := make([]configstoreTables.TableVirtualKey, 0)
	virtualKeysToUpdate := make([]configstoreTables.TableVirtualKey, 0)
	for i, newVirtualKey := range configData.Governance.VirtualKeys {
		fileVKHash, err := configstore.GenerateVirtualKeyHash(newVirtualKey)
		if err != nil {
			logger.Warn("failed to generate virtual key hash for %s: %v", newVirtualKey.ID, err)
			continue
		}
		configData.Governance.VirtualKeys[i].ConfigHash = fileVKHash
		// Preparing hash
		found := false
		for j, existingVirtualKey := range governanceConfig.VirtualKeys {
			if existingVirtualKey.ID == newVirtualKey.ID {
				found = true
				if existingVirtualKey.ConfigHash != fileVKHash {
					logger.Debug("config hash mismatch for virtual key %s, syncing from config file", newVirtualKey.ID)
					configData.Governance.VirtualKeys[i].ConfigHash = fileVKHash
					// This is added for backward compatibility with existing configs
					if configData.Governance.VirtualKeys[i].Value == "" && existingVirtualKey.Value != "" {
						configData.Governance.VirtualKeys[i].Value = existingVirtualKey.Value
					}
					// Process environment variable for virtual key value
					if strings.HasPrefix(configData.Governance.VirtualKeys[i].Value, "env.") {
						// Resolving the environment variable value
						envValue, err := envutils.ProcessEnvValue(configData.Governance.VirtualKeys[i].Value)
						if err != nil {
							logger.Warn("failed to process environment variable for virtual key %s: %v", newVirtualKey.ID, err)
							continue
						}
						configData.Governance.VirtualKeys[i].Value = envValue
					}
					// If the virtual key value is not a valid virtual key, we will generate a new one
					if !strings.HasPrefix(configData.Governance.VirtualKeys[i].Value, governance.VirtualKeyPrefix) {
						if configData.Governance.VirtualKeys[i].Value != "" {
							logger.Warn("virtual key %s has a value in the config file that does not have %s prefix. We are generating a new one for you.", newVirtualKey.ID, governance.VirtualKeyPrefix)
						}
						configData.Governance.VirtualKeys[i].Value = governance.GenerateVirtualKey()
					}
					// Resolve MCP client names to IDs for config file mcp_configs
					configData.Governance.VirtualKeys[i].MCPConfigs = resolveMCPConfigClientIDs(
						ctx, config.ConfigStore, configData.Governance.VirtualKeys[i].MCPConfigs, newVirtualKey.ID)
					virtualKeysToUpdate = append(virtualKeysToUpdate, configData.Governance.VirtualKeys[i])
					governanceConfig.VirtualKeys[j] = configData.Governance.VirtualKeys[i]
				} else {
					logger.Debug("config hash matches for virtual key %s, keeping DB config", newVirtualKey.ID)
				}
				break
			}
		}
		if !found {
			configData.Governance.VirtualKeys[i].ConfigHash = fileVKHash
			// if the virtual key value is env.VIRTUAL_KEY_VALUE, then we will need to resolve the environment variable
			// Process environment variable for virtual key value
			if strings.HasPrefix(configData.Governance.VirtualKeys[i].Value, "env.") {
				// Resolving the environment variable value
				envValue, err := envutils.ProcessEnvValue(configData.Governance.VirtualKeys[i].Value)
				if err != nil {
					logger.Warn("failed to process environment variable for virtual key %s: %v", newVirtualKey.ID, err)
					continue
				}
				configData.Governance.VirtualKeys[i].Value = envValue
			}
			if !strings.HasPrefix(configData.Governance.VirtualKeys[i].Value, governance.VirtualKeyPrefix) {
				if configData.Governance.VirtualKeys[i].Value != "" {
					logger.Warn("virtual key %s has a value in the config file that does not have %s prefix. We are generating a new one for you.", newVirtualKey.ID, governance.VirtualKeyPrefix)
				}
				configData.Governance.VirtualKeys[i].Value = governance.GenerateVirtualKey()
			}
			// Resolve MCP client names to IDs for config file mcp_configs
			configData.Governance.VirtualKeys[i].MCPConfigs = resolveMCPConfigClientIDs(
				ctx, config.ConfigStore, configData.Governance.VirtualKeys[i].MCPConfigs, newVirtualKey.ID)
			virtualKeysToAdd = append(virtualKeysToAdd, configData.Governance.VirtualKeys[i])
		}
	}
	// Merge RoutingRules by ID with hash comparison
	routingRulesToAdd := make([]configstoreTables.TableRoutingRule, 0)
	routingRulesToUpdate := make([]configstoreTables.TableRoutingRule, 0)
	for i, newRoutingRule := range configData.Governance.RoutingRules {
		fileRoutingRuleHash, err := configstore.GenerateRoutingRuleHash(newRoutingRule)
		if err != nil {
			logger.Warn("failed to generate routing rule hash for %s: %v", newRoutingRule.ID, err)
			continue
		}
		configData.Governance.RoutingRules[i].ConfigHash = fileRoutingRuleHash

		found := false
		for j, existingRoutingRule := range governanceConfig.RoutingRules {
			if existingRoutingRule.ID == newRoutingRule.ID {
				found = true
				if existingRoutingRule.ConfigHash != fileRoutingRuleHash {
					logger.Debug("config hash mismatch for routing rule %s, syncing from config file", newRoutingRule.ID)
					configData.Governance.RoutingRules[i].ConfigHash = fileRoutingRuleHash
					routingRulesToUpdate = append(routingRulesToUpdate, configData.Governance.RoutingRules[i])
					governanceConfig.RoutingRules[j] = configData.Governance.RoutingRules[i]
				} else {
					logger.Debug("config hash matches for routing rule %s, keeping DB config", newRoutingRule.ID)
				}
				break
			}
		}
		if !found {
			configData.Governance.RoutingRules[i].ConfigHash = fileRoutingRuleHash
			routingRulesToAdd = append(routingRulesToAdd, configData.Governance.RoutingRules[i])
		}
	}
	// Merge PricingOverrides by ID with hash comparison
	pricingOverridesToAdd := make([]configstoreTables.TablePricingOverride, 0)
	pricingOverridesToUpdate := make([]configstoreTables.TablePricingOverride, 0)
	for i, newPricingOverride := range configData.Governance.PricingOverrides {
		filePricingOverrideHash, err := configstore.GeneratePricingOverrideHash(newPricingOverride)
		if err != nil {
			logger.Warn("failed to generate pricing override hash for %s: %v", newPricingOverride.ID, err)
			continue
		}
		configData.Governance.PricingOverrides[i].ConfigHash = filePricingOverrideHash

		found := false
		for j, existingPricingOverride := range governanceConfig.PricingOverrides {
			if existingPricingOverride.ID == newPricingOverride.ID {
				found = true
				if existingPricingOverride.ConfigHash != filePricingOverrideHash {
					logger.Debug("config hash mismatch for pricing override %s, syncing from config file", newPricingOverride.ID)
					configData.Governance.PricingOverrides[i].ConfigHash = filePricingOverrideHash
					pricingOverridesToUpdate = append(pricingOverridesToUpdate, configData.Governance.PricingOverrides[i])
					governanceConfig.PricingOverrides[j] = configData.Governance.PricingOverrides[i]
				} else {
					logger.Debug("config hash matches for pricing override %s, keeping DB config", newPricingOverride.ID)
				}
				break
			}
		}
		if !found {
			configData.Governance.PricingOverrides[i].ConfigHash = filePricingOverrideHash
			pricingOverridesToAdd = append(pricingOverridesToAdd, configData.Governance.PricingOverrides[i])
		}
	}
	// Add merged items to config
	config.GovernanceConfig.Budgets = append(governanceConfig.Budgets, budgetsToAdd...)
	config.GovernanceConfig.RateLimits = append(governanceConfig.RateLimits, rateLimitsToAdd...)
	config.GovernanceConfig.Customers = append(governanceConfig.Customers, customersToAdd...)
	config.GovernanceConfig.Teams = append(governanceConfig.Teams, teamsToAdd...)
	config.GovernanceConfig.VirtualKeys = append(governanceConfig.VirtualKeys, virtualKeysToAdd...)
	config.GovernanceConfig.RoutingRules = append(governanceConfig.RoutingRules, routingRulesToAdd...)
	config.GovernanceConfig.PricingOverrides = append(governanceConfig.PricingOverrides, pricingOverridesToAdd...)
	// Update store with merged config items
	hasChanges := len(budgetsToAdd) > 0 || len(budgetsToUpdate) > 0 ||
		len(rateLimitsToAdd) > 0 || len(rateLimitsToUpdate) > 0 ||
		len(customersToAdd) > 0 || len(customersToUpdate) > 0 ||
		len(teamsToAdd) > 0 || len(teamsToUpdate) > 0 ||
		len(virtualKeysToAdd) > 0 || len(virtualKeysToUpdate) > 0 ||
		len(routingRulesToAdd) > 0 || len(routingRulesToUpdate) > 0 ||
		len(pricingOverridesToAdd) > 0 || len(pricingOverridesToUpdate) > 0
	if config.ConfigStore != nil && hasChanges {
		err := updateGovernanceConfigInStore(ctx, config,
			budgetsToAdd, budgetsToUpdate,
			rateLimitsToAdd, rateLimitsToUpdate,
			customersToAdd, customersToUpdate,
			teamsToAdd, teamsToUpdate,
			virtualKeysToAdd, virtualKeysToUpdate,
			routingRulesToAdd, routingRulesToUpdate,
			pricingOverridesToAdd, pricingOverridesToUpdate)
		if err != nil {
			logger.Fatal("failed to sync governance config: %v", err)
		}
	}
}

// updateGovernanceConfigInStore updates governance config items in the store
func updateGovernanceConfigInStore(
	ctx context.Context,
	config *Config,
	budgetsToAdd []configstoreTables.TableBudget,
	budgetsToUpdate []configstoreTables.TableBudget,
	rateLimitsToAdd []configstoreTables.TableRateLimit,
	rateLimitsToUpdate []configstoreTables.TableRateLimit,
	customersToAdd []configstoreTables.TableCustomer,
	customersToUpdate []configstoreTables.TableCustomer,
	teamsToAdd []configstoreTables.TableTeam,
	teamsToUpdate []configstoreTables.TableTeam,
	virtualKeysToAdd []configstoreTables.TableVirtualKey,
	virtualKeysToUpdate []configstoreTables.TableVirtualKey,
	routingRulesToAdd []configstoreTables.TableRoutingRule,
	routingRulesToUpdate []configstoreTables.TableRoutingRule,
	pricingOverridesToAdd []configstoreTables.TablePricingOverride,
	pricingOverridesToUpdate []configstoreTables.TablePricingOverride,
) error {
	logger.Debug("updating governance config in store with merged items")
	return config.ConfigStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		// Create budgets
		for _, budget := range budgetsToAdd {
			if err := config.ConfigStore.CreateBudget(ctx, &budget, tx); err != nil {
				return fmt.Errorf("failed to create budget %s: %w", budget.ID, err)
			}
		}

		// Update budgets (config.json changed)
		for _, budget := range budgetsToUpdate {
			if err := config.ConfigStore.UpdateBudget(ctx, &budget, tx); err != nil {
				return fmt.Errorf("failed to update budget %s: %w", budget.ID, err)
			}
		}

		// Create rate limits
		for _, rateLimit := range rateLimitsToAdd {
			if err := config.ConfigStore.CreateRateLimit(ctx, &rateLimit, tx); err != nil {
				return fmt.Errorf("failed to create rate limit %s: %w", rateLimit.ID, err)
			}
		}

		// Update rate limits (config.json changed)
		for _, rateLimit := range rateLimitsToUpdate {
			if err := config.ConfigStore.UpdateRateLimit(ctx, &rateLimit, tx); err != nil {
				return fmt.Errorf("failed to update rate limit %s: %w", rateLimit.ID, err)
			}
		}

		// Create customers
		for _, customer := range customersToAdd {
			if err := config.ConfigStore.CreateCustomer(ctx, &customer, tx); err != nil {
				return fmt.Errorf("failed to create customer %s: %w", customer.ID, err)
			}
		}

		// Update customers (config.json changed)
		for _, customer := range customersToUpdate {
			if err := config.ConfigStore.UpdateCustomer(ctx, &customer, tx); err != nil {
				return fmt.Errorf("failed to update customer %s: %w", customer.ID, err)
			}
		}

		// Create teams
		for _, team := range teamsToAdd {
			if err := config.ConfigStore.CreateTeam(ctx, &team, tx); err != nil {
				return fmt.Errorf("failed to create team %s: %w", team.ID, err)
			}
		}

		// Update teams (config.json changed)
		for _, team := range teamsToUpdate {
			if err := config.ConfigStore.UpdateTeam(ctx, &team, tx); err != nil {
				return fmt.Errorf("failed to update team %s: %w", team.ID, err)
			}
		}

		// Create virtual keys with explicit association handling
		for i := range virtualKeysToAdd {
			virtualKey := &virtualKeysToAdd[i]
			providerConfigs := virtualKey.ProviderConfigs
			mcpConfigs := virtualKey.MCPConfigs
			virtualKey.ProviderConfigs = nil
			virtualKey.MCPConfigs = nil
			// Here we wll filter provider / keys that are not available
			if err := config.ConfigStore.CreateVirtualKey(ctx, virtualKey, tx); err != nil {
				return fmt.Errorf("failed to create virtual key %s: %w", virtualKey.ID, err)
			}
			for j := range providerConfigs {
				providerConfigs[j].VirtualKeyID = virtualKey.ID
				if err := config.ConfigStore.CreateVirtualKeyProviderConfig(ctx, &providerConfigs[j], tx); err != nil {
					return fmt.Errorf("failed to create provider config for virtual key %s: %w", virtualKey.ID, err)
				}
			}
			for j := range mcpConfigs {
				mcpConfigs[j].VirtualKeyID = virtualKey.ID
				if err := config.ConfigStore.CreateVirtualKeyMCPConfig(ctx, &mcpConfigs[j], tx); err != nil {
					return fmt.Errorf("failed to create MCP config for virtual key %s: %w", virtualKey.ID, err)
				}
			}

			virtualKey.ProviderConfigs = providerConfigs
			virtualKey.MCPConfigs = mcpConfigs
		}

		// Update virtual keys (config.json changed)
		for _, virtualKey := range virtualKeysToUpdate {
			if err := reconcileVirtualKeyAssociations(ctx, config.ConfigStore, tx, virtualKey.ID, virtualKey.ProviderConfigs, virtualKey.MCPConfigs); err != nil {
				return fmt.Errorf("failed to reconcile associations for virtual key %s: %w", virtualKey.ID, err)
			}
			if err := config.ConfigStore.UpdateVirtualKey(ctx, &virtualKey, tx); err != nil {
				return fmt.Errorf("failed to update virtual key %s: %w", virtualKey.ID, err)
			}
		}

		// Create routing rules (new from config.json)
		for _, rule := range routingRulesToAdd {
			if err := config.ConfigStore.CreateRoutingRule(ctx, &rule, tx); err != nil {
				return fmt.Errorf("failed to create routing rule %s: %w", rule.ID, err)
			}
		}

		// Update routing rules (config.json changed)
		for _, rule := range routingRulesToUpdate {
			if err := config.ConfigStore.UpdateRoutingRule(ctx, &rule, tx); err != nil {
				return fmt.Errorf("failed to update routing rule %s: %w", rule.ID, err)
			}
		}

		// Create pricing overrides (new from config.json)
		for _, pricingOverride := range pricingOverridesToAdd {
			if err := config.ConfigStore.CreatePricingOverride(ctx, &pricingOverride, tx); err != nil {
				return fmt.Errorf("failed to create pricing override %s: %w", pricingOverride.ID, err)
			}
		}

		// Update pricing overrides (config.json changed)
		for _, pricingOverride := range pricingOverridesToUpdate {
			if err := config.ConfigStore.UpdatePricingOverride(ctx, &pricingOverride, tx); err != nil {
				return fmt.Errorf("failed to update pricing override %s: %w", pricingOverride.ID, err)
			}
		}

		return nil
	})
}

// createGovernanceConfigInStore creates governance config in store from config file
func createGovernanceConfigInStore(ctx context.Context, config *Config) {
	if config.ConfigStore == nil {
		logger.Debug("createGovernanceConfigInStore: ConfigStore is nil, skipping")
		return
	}
	logger.Debug("createGovernanceConfigInStore: creating %d budgets, %d rate_limits, %d virtual_keys, %d routing_rules, %d pricing_overrides",
		len(config.GovernanceConfig.Budgets),
		len(config.GovernanceConfig.RateLimits),
		len(config.GovernanceConfig.VirtualKeys),
		len(config.GovernanceConfig.RoutingRules),
		len(config.GovernanceConfig.PricingOverrides))
	if err := config.ConfigStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
		for i := range config.GovernanceConfig.Budgets {
			budget := &config.GovernanceConfig.Budgets[i]
			budgetHash, err := configstore.GenerateBudgetHash(*budget)
			if err != nil {
				logger.Warn("failed to generate budget hash for %s: %v", budget.ID, err)
			} else {
				budget.ConfigHash = budgetHash
			}
			if err := config.ConfigStore.CreateBudget(ctx, budget, tx); err != nil {
				return fmt.Errorf("failed to create budget %s: %w", budget.ID, err)
			}
		}

		for i := range config.GovernanceConfig.RateLimits {
			rateLimit := &config.GovernanceConfig.RateLimits[i]
			rlHash, err := configstore.GenerateRateLimitHash(*rateLimit)
			if err != nil {
				logger.Warn("failed to generate rate limit hash for %s: %v", rateLimit.ID, err)
			} else {
				rateLimit.ConfigHash = rlHash
			}
			if err := config.ConfigStore.CreateRateLimit(ctx, rateLimit, tx); err != nil {
				return fmt.Errorf("failed to create rate limit %s: %w", rateLimit.ID, err)
			}
		}

		for i := range config.GovernanceConfig.Customers {
			customer := &config.GovernanceConfig.Customers[i]
			customerHash, err := configstore.GenerateCustomerHash(*customer)
			if err != nil {
				logger.Warn("failed to generate customer hash for %s: %v", customer.ID, err)
			} else {
				customer.ConfigHash = customerHash
			}
			if err := config.ConfigStore.CreateCustomer(ctx, customer, tx); err != nil {
				return fmt.Errorf("failed to create customer %s: %w", customer.ID, err)
			}
		}

		for i := range config.GovernanceConfig.Teams {
			team := &config.GovernanceConfig.Teams[i]
			teamHash, err := configstore.GenerateTeamHash(*team)
			if err != nil {
				logger.Warn("failed to generate team hash for %s: %v", team.ID, err)
			} else {
				team.ConfigHash = teamHash
			}
			if err := config.ConfigStore.CreateTeam(ctx, team, tx); err != nil {
				return fmt.Errorf("failed to create team %s: %w", team.ID, err)
			}
		}

		for i := range config.GovernanceConfig.RoutingRules {
			rule := &config.GovernanceConfig.RoutingRules[i]
			ruleHash, err := configstore.GenerateRoutingRuleHash(*rule)
			if err != nil {
				logger.Warn("failed to generate routing rule hash for %s: %v", rule.ID, err)
			} else {
				rule.ConfigHash = ruleHash
			}
			if err := config.ConfigStore.CreateRoutingRule(ctx, rule, tx); err != nil {
				return fmt.Errorf("failed to create routing rule %s: %w", rule.ID, err)
			}
		}

		for i := range config.GovernanceConfig.PricingOverrides {
			override := &config.GovernanceConfig.PricingOverrides[i]
			overrideHash, err := configstore.GeneratePricingOverrideHash(*override)
			if err != nil {
				logger.Warn("failed to generate pricing override hash for %s: %v", override.ID, err)
			} else {
				override.ConfigHash = overrideHash
			}
			if err := config.ConfigStore.CreatePricingOverride(ctx, override, tx); err != nil {
				return fmt.Errorf("failed to create pricing override %s: %w", override.ID, err)
			}
		}

		for i := range config.GovernanceConfig.VirtualKeys {
			virtualKey := &config.GovernanceConfig.VirtualKeys[i]
			logger.Debug("creating virtual key: id=%s, name=%s, value=%s", virtualKey.ID, virtualKey.Name, virtualKey.Value)
			vkHash, err := configstore.GenerateVirtualKeyHash(*virtualKey)
			if err != nil {
				logger.Warn("failed to generate virtual key hash for %s: %v", virtualKey.ID, err)
			} else {
				virtualKey.ConfigHash = vkHash
			}
			providerConfigs := virtualKey.ProviderConfigs
			mcpConfigs := virtualKey.MCPConfigs
			virtualKey.ProviderConfigs = nil
			virtualKey.MCPConfigs = nil

			if err := config.ConfigStore.CreateVirtualKey(ctx, virtualKey, tx); err != nil {
				logger.Error("failed to create virtual key %s: %v", virtualKey.ID, err)
				return fmt.Errorf("failed to create virtual key %s: %w", virtualKey.ID, err)
			}
			logger.Debug("created virtual key %s successfully", virtualKey.ID)

			for _, pc := range providerConfigs {
				pc.VirtualKeyID = virtualKey.ID
				logger.Debug("creating provider config for VK %s: provider=%s, keys=%d", virtualKey.ID, pc.Provider, len(pc.Keys))
				if err := config.ConfigStore.CreateVirtualKeyProviderConfig(ctx, &pc, tx); err != nil {
					logger.Error("failed to create provider config for virtual key %s: %v", virtualKey.ID, err)
					return fmt.Errorf("failed to create provider config for virtual key %s: %w", virtualKey.ID, err)
				}
			}

			// Resolve MCP client names to IDs for config file mcp_configs
			mcpConfigs = resolveMCPConfigClientIDs(ctx, config.ConfigStore, mcpConfigs, virtualKey.ID)

			for _, mc := range mcpConfigs {
				mc.VirtualKeyID = virtualKey.ID
				if err := config.ConfigStore.CreateVirtualKeyMCPConfig(ctx, &mc, tx); err != nil {
					return fmt.Errorf("failed to create MCP config for virtual key %s: %w", virtualKey.ID, err)
				}
			}

			virtualKey.ProviderConfigs = providerConfigs
			virtualKey.MCPConfigs = mcpConfigs
		}

		return nil
	}); err != nil {
		logger.Warn("failed to update governance config: %v", err)
	}
}

// isBcryptHash checks if a string looks like a bcrypt hash
func isBcryptHash(s string) bool {
	return strings.HasPrefix(s, "$2a$") ||
		strings.HasPrefix(s, "$2b$") ||
		strings.HasPrefix(s, "$2y$")
}

// preserveEnvVar returns a new EnvVar with the given value but preserving
// env var metadata (EnvVar reference and FromEnv flag) from the source.
// This allows the hashed password to be used as the value while retaining
// the original env var reference for display in the UI.
func preserveEnvVar(source *schemas.EnvVar, value string) *schemas.EnvVar {
	if source == nil {
		return schemas.NewEnvVar(value)
	}
	return &schemas.EnvVar{
		Val:     value,
		EnvVar:  source.EnvVar,
		FromEnv: source.FromEnv,
	}
}

// loadAuthConfigFromFile loads auth config from file.
// File config (configData) always takes precedence over DB config.
func loadAuthConfigFromFile(ctx context.Context, config *Config, configData *ConfigData) {
	hasFileConfig := configData != nil && configData.AuthConfig != nil
	if !hasFileConfig && (config.GovernanceConfig == nil || config.GovernanceConfig.AuthConfig == nil) {
		return
	}
	// Ensure GovernanceConfig is initialized
	if config.GovernanceConfig == nil {
		config.GovernanceConfig = &configstore.GovernanceConfig{}
	}
	if config.ConfigStore == nil {
		logger.Warn("config store is required to load auth config from file")
		if hasFileConfig {
			config.GovernanceConfig.AuthConfig = configData.AuthConfig
		}
		return
	}
	// Load existing auth config from DB
	dbAuthConfig, err := config.ConfigStore.GetAuthConfig(ctx)
	if err != nil {
		logger.Warn("failed to get auth config from store: %v", err)
		return
	}
	// If no file config, use DB config and return (no write needed)
	if !hasFileConfig {
		if dbAuthConfig != nil {
			config.GovernanceConfig.AuthConfig = dbAuthConfig
		}
		return
	}
	// File config present: validate env vars
	if configData.AuthConfig.AdminUserName != nil && configData.AuthConfig.AdminUserName.GetValue() == "" && configData.AuthConfig.AdminUserName.IsFromEnv() {
		logger.Fatal("username set with env var but value is empty: %s", configData.AuthConfig.AdminUserName.EnvVar)
	}
	if configData.AuthConfig.AdminPassword != nil && configData.AuthConfig.AdminPassword.GetValue() == "" && configData.AuthConfig.AdminPassword.IsFromEnv() {
		logger.Fatal("password set with env var but value is empty: %s", configData.AuthConfig.AdminPassword.EnvVar)
	}
	filePassword := configData.AuthConfig.AdminPassword.GetValue()
	// If DB already matches file config, skip hashing and DB write
	if dbAuthConfig != nil {
		usernameMatch := dbAuthConfig.AdminUserName.GetValue() == configData.AuthConfig.AdminUserName.GetValue()
		boolsMatch := dbAuthConfig.IsEnabled == configData.AuthConfig.IsEnabled &&
			dbAuthConfig.DisableAuthOnInference == configData.AuthConfig.DisableAuthOnInference
		var passwordMatch bool
		if filePassword == "" {
			passwordMatch = dbAuthConfig.AdminPassword.GetValue() == ""
		} else if isBcryptHash(filePassword) {
			passwordMatch = dbAuthConfig.AdminPassword.GetValue() == filePassword
		} else {
			passwordMatch, _ = encrypt.CompareHash(dbAuthConfig.AdminPassword.GetValue(), filePassword)
		}
		if usernameMatch && passwordMatch && boolsMatch {
			// DB matches file -- use DB hash but preserve file env var references
			config.GovernanceConfig.AuthConfig = &configstore.AuthConfig{
				AdminUserName:          configData.AuthConfig.AdminUserName,
				AdminPassword:          preserveEnvVar(configData.AuthConfig.AdminPassword, dbAuthConfig.AdminPassword.GetValue()),
				IsEnabled:              configData.AuthConfig.IsEnabled,
				DisableAuthOnInference: configData.AuthConfig.DisableAuthOnInference,
			}
			return
		}
		if !passwordMatch {
			// Here we nuke all sessions
			if err := config.ConfigStore.FlushSessions(ctx); err != nil {
				logger.Warn("failed to flush sessions: %v", err)
			}
		}
	}
	// Hash password if it's plaintext (not already a bcrypt hash)
	hashedPassword := filePassword
	if hashedPassword != "" && !isBcryptHash(hashedPassword) {
		var err error
		hashedPassword, err = encrypt.Hash(hashedPassword)
		if err != nil {
			logger.Warn("failed to hash auth password: %v", err)
			// Fall back to DB config if available rather than leaving AuthConfig unset
			if dbAuthConfig != nil {
				config.GovernanceConfig.AuthConfig = dbAuthConfig
			}
			return
		}
	}
	// Build auth config with hashed password but preserve env var references
	config.GovernanceConfig.AuthConfig = &configstore.AuthConfig{
		AdminUserName:          configData.AuthConfig.AdminUserName,
		AdminPassword:          preserveEnvVar(configData.AuthConfig.AdminPassword, hashedPassword),
		IsEnabled:              configData.AuthConfig.IsEnabled,
		DisableAuthOnInference: configData.AuthConfig.DisableAuthOnInference,
	}
	// Persist to config store
	if err := config.ConfigStore.UpdateAuthConfig(ctx, config.GovernanceConfig.AuthConfig); err != nil {
		logger.Warn("failed to update auth config: %v", err)
	}
}

// loadPluginsFromFile loads and merges plugins from file
func loadPluginsFromFile(ctx context.Context, config *Config, configData *ConfigData) {
	// First load plugins from DB
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

	// Merge with config file plugins
	if len(configData.Plugins) > 0 {
		mergePluginsFromFile(ctx, config, configData)
	}
}

// mergePluginsFromFile merges plugins from config file with existing config
func mergePluginsFromFile(ctx context.Context, config *Config, configData *ConfigData) {
	logger.Debug("processing plugins from config file")
	if len(config.PluginConfigs) == 0 {
		logger.Debug("no plugins found in store, using plugins from config file")
		config.PluginConfigs = configData.Plugins
	} else {
		// Merge new plugins and update if version is higher
		for _, plugin := range configData.Plugins {
			if plugin.Version == nil {
				plugin.Version = bifrost.Ptr(int16(1))
			}
			existingIdx := slices.IndexFunc(config.PluginConfigs, func(p *schemas.PluginConfig) bool {
				return p.Name == plugin.Name
			})
			if existingIdx == -1 {
				logger.Debug("adding new plugin %s to config.PluginConfigs", plugin.Name)
				config.PluginConfigs = append(config.PluginConfigs, plugin)
			} else {
				existingPlugin := config.PluginConfigs[existingIdx]
				existingVersion := int16(1)
				if existingPlugin.Version != nil {
					existingVersion = *existingPlugin.Version
				}
				if *plugin.Version > existingVersion {
					logger.Debug("replacing plugin %s with higher version %d (was %d)", plugin.Name, *plugin.Version, existingVersion)
					config.PluginConfigs[existingIdx] = plugin
				}
			}
		}
	}

	// Process semantic cache plugin
	for i, plugin := range config.PluginConfigs {
		if plugin.Name == semanticcache.PluginName {
			if err := config.AddProviderKeysToSemanticCacheConfig(plugin); err != nil {
				logger.Warn("failed to add provider keys to semantic cache config: %v", err)
			}
			config.PluginConfigs[i] = plugin
		}
	}

	// Update store
	if config.ConfigStore != nil {
		logger.Debug("updating plugins in store")
		for _, plugin := range config.PluginConfigs {
			pluginConfigCopy, err := DeepCopy(plugin.Config)
			if err != nil {
				logger.Warn("failed to deep copy plugin config, skipping database update: %v", err)
				continue
			}
			if plugin.Version == nil {
				plugin.Version = bifrost.Ptr(int16(1))
			}
			pluginConfig := &configstoreTables.TablePlugin{
				Name:    plugin.Name,
				Enabled: plugin.Enabled,
				Config:  pluginConfigCopy,
				Path:    plugin.Path,
				Version: *plugin.Version,
			}
			if plugin.Name == semanticcache.PluginName {
				if err := config.RemoveProviderKeysFromSemanticCacheConfig(pluginConfig); err != nil {
					logger.Warn("failed to remove provider keys from semantic cache config: %v", err)
				}
			}
			if err := config.ConfigStore.UpsertPlugin(ctx, pluginConfig); err != nil {
				logger.Warn("failed to update plugin: %v", err)
			}
		}
	}
}

// convertSchemasMCPClientConfigToTable converts schemas.MCPClientConfig to tables.TableMCPClient
func convertSchemasMCPClientConfigToTable(clientConfig *schemas.MCPClientConfig) *configstoreTables.TableMCPClient {
	return &configstoreTables.TableMCPClient{
		ClientID:           clientConfig.ID,
		Name:               clientConfig.Name,
		IsCodeModeClient:   clientConfig.IsCodeModeClient,
		ConnectionType:     string(clientConfig.ConnectionType),
		ConnectionString:   clientConfig.ConnectionString,
		StdioConfig:        clientConfig.StdioConfig,
		ToolsToExecute:     clientConfig.ToolsToExecute,
		ToolsToAutoExecute: clientConfig.ToolsToAutoExecute,
		Headers:            clientConfig.Headers,
		AuthType:           string(clientConfig.AuthType),
		OauthConfigID:      clientConfig.OauthConfigID,
	}
}

// buildMCPPricingDataFromStore builds MCP pricing data from the config store
func buildMCPPricingDataFromStore(ctx context.Context, configStore configstore.ConfigStore) mcpcatalog.MCPPricingData {
	mcpPricingData := mcpcatalog.MCPPricingData{}
	mcpConfig, err := configStore.GetMCPConfig(ctx)
	if err != nil {
		logger.Warn("failed to get MCP config from store: %v", err)
		return mcpPricingData
	}
	if mcpConfig != nil {
		for _, clientConfig := range mcpConfig.ClientConfigs {
			dbClientConfig, err := configStore.GetMCPClientByName(ctx, clientConfig.Name)
			if err != nil {
				logger.Warn("failed to get MCP client config from store: %v", err)
				continue
			}
			if dbClientConfig == nil {
				logger.Warn("MCP client config is nil for client: %s", clientConfig.Name)
				continue
			}
			for toolName, costPerExecution := range dbClientConfig.ToolPricing {
				// Tool names in the DB are stored without the client/server prefix.
				// Build the key using fmt.Sprintf("%s/%s", clientName, toolName) to match
				// buildMCPPricingDataFromFile and EditMCPClient patterns.
				mcpPricingData[fmt.Sprintf("%s/%s", dbClientConfig.Name, toolName)] = mcpcatalog.PricingEntry{
					Server:           dbClientConfig.Name,
					ToolName:         toolName,
					CostPerExecution: costPerExecution,
				}
			}
		}
	}
	return mcpPricingData
}

func buildMCPPricingDataFromFile(ctx context.Context, configData *ConfigData) mcpcatalog.MCPPricingData {
	mcpPricingData := mcpcatalog.MCPPricingData{}
	if configData == nil || configData.MCP == nil {
		return mcpPricingData
	}
	for _, clientConfig := range configData.MCP.ClientConfigs {
		for toolName, costPerExecution := range clientConfig.ToolPricing {
			mcpPricingData[fmt.Sprintf("%s/%s", clientConfig.Name, toolName)] = mcpcatalog.PricingEntry{
				Server:           clientConfig.Name,
				ToolName:         toolName,
				CostPerExecution: costPerExecution,
			}
		}
	}
	return mcpPricingData
}

// initFrameworkConfigFromFile initializes framework config and pricing manager from file
func initFrameworkConfigFromFile(ctx context.Context, config *Config, configData *ConfigData) {
	pricingConfig := &modelcatalog.Config{}
	mcpPricingConfig := &mcpcatalog.Config{}
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
		mcpPricingConfig.PricingData = buildMCPPricingDataFromStore(ctx, config.ConfigStore)
	} else if configData.FrameworkConfig != nil && configData.FrameworkConfig.Pricing != nil {
		pricingConfig.PricingURL = configData.FrameworkConfig.Pricing.PricingURL
		syncDuration := time.Duration(*configData.FrameworkConfig.Pricing.PricingSyncInterval) * time.Second
		pricingConfig.PricingSyncInterval = &syncDuration
	}

	// Initialize OAuth provider
	config.OAuthProvider = oauth2.NewOAuth2Provider(config.ConfigStore, logger)

	// Start token refresh worker for automatic OAuth token refresh
	config.TokenRefreshWorker = oauth2.NewTokenRefreshWorker(config.OAuthProvider, logger)
	if config.TokenRefreshWorker != nil {
		config.TokenRefreshWorker.Start(ctx)
	}

	config.FrameworkConfig = &framework.FrameworkConfig{
		Pricing: pricingConfig,
	}

	var pricingManager *modelcatalog.ModelCatalog
	var err error

	// Use default modelcatalog initialization when no enterprise overrides are provided
	pricingManager, err = modelcatalog.Init(ctx, pricingConfig, config.ConfigStore, nil, logger)
	if err != nil {
		logger.Error("failed to initialize pricing manager: %v", err)
	} else {
		config.ModelCatalog = pricingManager
		applyGovernancePricingOverrides(config.ModelCatalog, config.GovernanceConfig)
	}

	// Initialize MCP catalog
	mcpCatalog, err := mcpcatalog.Init(ctx, &mcpcatalog.Config{
		PricingData: buildMCPPricingDataFromFile(ctx, configData),
	}, logger)
	if err != nil {
		logger.Warn("failed to initialize MCP catalog: %v", err)
	}
	config.MCPCatalog = mcpCatalog
}

// initEncryptionFromFile initializes encryption from config file
func initEncryptionFromFile(configData *ConfigData) error {
	if configData.EncryptionKey == nil || configData.EncryptionKey.GetValue() == "" {
		// Checking if BIFROST_ENCRYPTION_KEY environment variable is set
		if os.Getenv("BIFROST_ENCRYPTION_KEY") != "" {
			configData.EncryptionKey = schemas.NewEnvVar("env.BIFROST_ENCRYPTION_KEY")
		}
	}
	// Checking if encryption key is set
	if configData.EncryptionKey != nil && configData.EncryptionKey.GetValue() != "" {
		encrypt.Init(configData.EncryptionKey.GetValue(), logger)
	}
	return nil
}

// syncEncryption encrypts all plaintext rows in the config store if encryption is enabled.
// Called during bootup after encryption key is initialized and all config data has been loaded.
func syncEncryption(ctx context.Context, config *Config) {
	if !encrypt.IsEnabled() || config.ConfigStore == nil {
		return
	}
	if err := config.ConfigStore.EncryptPlaintextRows(ctx); err != nil {
		logger.Error("failed to sync encryption for plaintext rows: %v", err)
	}
}

// loadConfigFromDefaults initializes configuration when no config file exists.
// It creates a default SQLite config store and loads/creates default configurations.
func loadConfigFromDefaults(ctx context.Context, config *Config, configDBPath, logsDBPath string) (*Config, error) {
	var err error
	// Initialize encryption before stores so BeforeSave hooks and EncryptPlaintextRows work correctly
	encryptionKey := schemas.NewEnvVar("env.BIFROST_ENCRYPTION_KEY")
	if encryptionKey != nil && encryptionKey.GetValue() != "" {
		encrypt.Init(encryptionKey.GetValue(), logger)
	}
	// Initialize default config store
	if err = initDefaultConfigStore(ctx, config, configDBPath); err != nil {
		return nil, err
	}
	// Clear restart required flag on server startup
	if err = config.ConfigStore.ClearRestartRequiredConfig(ctx); err != nil {
		logger.Warn("failed to clear restart required config: %v", err)
	}
	// Load or create default client config
	if err = loadDefaultClientConfig(ctx, config); err != nil {
		return nil, err
	}
	// Initialize logs store
	if err = initDefaultLogsStore(ctx, config, logsDBPath); err != nil {
		return nil, err
	}
	// Load or auto-detect providers
	if err = loadDefaultProviders(ctx, config); err != nil {
		return nil, err
	}
	// Load governance config
	loadDefaultGovernanceConfig(ctx, config)
	// Load MCP config
	if err = loadDefaultMCPConfig(ctx, config); err != nil {
		return nil, err
	}
	// Load plugins
	if err = loadDefaultPlugins(ctx, config); err != nil {
		return nil, err
	}
	// Initialize framework config and pricing manager
	if err = initDefaultFrameworkConfig(ctx, config); err != nil {
		return nil, err
	}
	// Sync encryption: encrypt any plaintext rows written during config loading
	syncEncryption(ctx, config)
	return config, nil
}

// initDefaultConfigStore initializes a default SQLite config store
func initDefaultConfigStore(ctx context.Context, config *Config, configDBPath string) error {
	var err error
	config.ConfigStore, err = configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config: &configstore.SQLiteConfig{
			Path: configDBPath,
		},
	}, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize config store: %w", err)
	}
	return nil
}

// loadDefaultClientConfig loads or creates default client configuration
func loadDefaultClientConfig(ctx context.Context, config *Config) error {
	clientConfig, err := config.ConfigStore.GetClientConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to get client config: %w", err)
	}
	if clientConfig == nil {
		clientConfig = &DefaultClientConfig
	} else {
		// For backward compatibility, handle cases where max request body size is not set
		if clientConfig.MaxRequestBodySizeMB == 0 {
			clientConfig.MaxRequestBodySizeMB = DefaultClientConfig.MaxRequestBodySizeMB
		}
	}
	if err = config.ConfigStore.UpdateClientConfig(ctx, clientConfig); err != nil {
		return fmt.Errorf("failed to update client config: %w", err)
	}
	config.ClientConfig = *clientConfig
	return nil
}

// initDefaultLogsStore initializes or loads the logs store configuration
func initDefaultLogsStore(ctx context.Context, config *Config, logsDBPath string) error {
	logStoreConfig, err := config.ConfigStore.GetLogsStoreConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to get logs store config: %w", err)
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
	// Initialize logs store
	config.LogsStore, err = logstore.NewLogStore(ctx, logStoreConfig, logger)
	if err != nil {
		// Handle case where stored path doesn't exist, create new at default path
		if logStoreConfig.Type == logstore.LogStoreTypeSQLite && os.IsNotExist(err) {
			storedPath := ""
			if sqliteConfig, ok := logStoreConfig.Config.(*logstore.SQLiteConfig); ok {
				storedPath = sqliteConfig.Path
			}
			if storedPath != logsDBPath {
				logger.Warn("failed to locate logstore file at path: %s: %v. Creating new one at path: %s", storedPath, err, logsDBPath)
				logStoreConfig = &logstore.Config{
					Enabled: true,
					Type:    logstore.LogStoreTypeSQLite,
					Config: &logstore.SQLiteConfig{
						Path: logsDBPath,
					},
				}
				config.LogsStore, err = logstore.NewLogStore(ctx, logStoreConfig, logger)
				if err != nil {
					return fmt.Errorf("failed to initialize logs store: %v", err)
				}
			} else {
				return fmt.Errorf("failed to initialize logs store: %v", err)
			}
		} else {
			return fmt.Errorf("failed to initialize logs store: %v", err)
		}
	}
	logger.Info("logs store initialized.")
	if err = config.ConfigStore.UpdateLogsStoreConfig(ctx, logStoreConfig); err != nil {
		return fmt.Errorf("failed to update logs store config: %w", err)
	}
	return nil
}

// loadDefaultProviders loads providers from DB or auto-detects from environment
func loadDefaultProviders(ctx context.Context, config *Config) error {
	providers, err := config.ConfigStore.GetProvidersConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to get providers config: %w", err)
	}
	if providers == nil {
		config.autoDetectProviders(ctx)
		providers = config.Providers
		// Store providers config in database
		if err = config.ConfigStore.UpdateProvidersConfig(ctx, providers); err != nil {
			return fmt.Errorf("failed to update providers config: %w", err)
		}
	} else {
		processedProviders := make(map[schemas.ModelProvider]configstore.ProviderConfig)
		for providerKey, dbProvider := range providers {
			provider := schemas.ModelProvider(providerKey)
			// Convert database keys to schemas.Key
			keys := make([]schemas.Key, len(dbProvider.Keys))
			for i, dbKey := range dbProvider.Keys {
				keys[i] = schemas.Key{
					ID:                 dbKey.ID,
					Name:               dbKey.Name,
					Value:              dbKey.Value,
					Models:             dbKey.Models,
					Weight:             dbKey.Weight,
					Enabled:            dbKey.Enabled,
					UseForBatchAPI:     dbKey.UseForBatchAPI,
					AzureKeyConfig:     dbKey.AzureKeyConfig,
					VertexKeyConfig:    dbKey.VertexKeyConfig,
					BedrockKeyConfig:   dbKey.BedrockKeyConfig,
					ReplicateKeyConfig: dbKey.ReplicateKeyConfig,
					ConfigHash:         dbKey.ConfigHash,
					Status:             dbKey.Status,
					Description:        dbKey.Description,
				}
			}
			providerConfig := configstore.ProviderConfig{
				Keys:                     keys,
				NetworkConfig:            dbProvider.NetworkConfig,
				ConcurrencyAndBufferSize: dbProvider.ConcurrencyAndBufferSize,
				ProxyConfig:              dbProvider.ProxyConfig,
				SendBackRawRequest:       dbProvider.SendBackRawRequest,
				SendBackRawResponse:      dbProvider.SendBackRawResponse,
				CustomProviderConfig:     dbProvider.CustomProviderConfig,
				ConfigHash:               dbProvider.ConfigHash,
			}
			if err := ValidateCustomProvider(providerConfig, provider); err != nil {
				logger.Warn("invalid custom provider config for %s: %v", provider, err)
				continue
			}
			processedProviders[provider] = providerConfig
		}
		config.Providers = processedProviders
	}
	return nil
}

// loadDefaultGovernanceConfig loads governance configuration from the store
func loadDefaultGovernanceConfig(ctx context.Context, config *Config) {
	governanceConfig, err := config.ConfigStore.GetGovernanceConfig(ctx)
	if err != nil {
		logger.Warn("failed to get governance config from store: %v", err)
		return
	}
	if governanceConfig != nil {
		config.GovernanceConfig = governanceConfig
	}
}

// loadDefaultMCPConfig loads or creates MCP configuration
func loadDefaultMCPConfig(ctx context.Context, config *Config) error {
	tableMCPConfig, err := config.ConfigStore.GetMCPConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to get MCP config: %w", err)
	}
	if tableMCPConfig == nil {
		if config.MCPConfig != nil {
			for _, clientConfig := range config.MCPConfig.ClientConfigs {
				if clientConfig != nil {
					if err := config.ConfigStore.CreateMCPClientConfig(ctx, clientConfig); err != nil {
						logger.Warn("failed to create MCP client config: %v", err)
						continue
					}
				}
			}
			// Refresh from store to ensure parity with persisted state
			if tableMCPConfig, err = config.ConfigStore.GetMCPConfig(ctx); err != nil {
				return fmt.Errorf("failed to get MCP config after update: %w", err)
			}
			if tableMCPConfig != nil {
				config.MCPConfig = tableMCPConfig
			}
		}
	} else {
		config.MCPConfig = tableMCPConfig
	}
	return nil
}

// loadDefaultPlugins loads plugins from the config store
func loadDefaultPlugins(ctx context.Context, config *Config) error {
	plugins, err := config.ConfigStore.GetPlugins(ctx)
	if err != nil {
		return fmt.Errorf("failed to get plugins: %w", err)
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
	return nil
}

// initDefaultFrameworkConfig initializes framework configuration and pricing manager
func initDefaultFrameworkConfig(ctx context.Context, config *Config) error {
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

	// Update DB with latest config
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
	if err = config.ConfigStore.UpdateFrameworkConfig(ctx, &configstoreTables.TableFrameworkConfig{
		ID:                  configID,
		PricingURL:          pricingConfig.PricingURL,
		PricingSyncInterval: bifrost.Ptr(durationSec),
	}); err != nil {
		return fmt.Errorf("failed to update framework config: %w", err)
	}

	// Initialize OAuth provider
	config.OAuthProvider = oauth2.NewOAuth2Provider(config.ConfigStore, logger)

	// Start token refresh worker for automatic OAuth token refresh
	config.TokenRefreshWorker = oauth2.NewTokenRefreshWorker(config.OAuthProvider, logger)
	if config.TokenRefreshWorker != nil {
		config.TokenRefreshWorker.Start(ctx)
	}

	config.FrameworkConfig = &framework.FrameworkConfig{
		Pricing: pricingConfig,
	}

	// Initialize pricing manager
	var modelCatalog *modelcatalog.ModelCatalog
	// Use default modelcatalog initialization when no enterprise overrides are provided
	modelCatalog, err = modelcatalog.Init(ctx, pricingConfig, config.ConfigStore, nil, logger)
	if err != nil {
		logger.Error("failed to initialize model catalog: %v", err)
	} else {
		config.ModelCatalog = modelCatalog
		applyGovernancePricingOverrides(config.ModelCatalog, config.GovernanceConfig)
	}

	// Initialize MCP catalog
	var mcpCatalog *mcpcatalog.MCPCatalog

	// Build MCP pricing data from database
	mcpPricingData := buildMCPPricingDataFromStore(ctx, config.ConfigStore)

	mcpCatalog, err = mcpcatalog.Init(ctx, &mcpcatalog.Config{
		PricingData: mcpPricingData,
	}, logger)
	if err != nil {
		logger.Warn("failed to initialize MCP catalog: %v", err)
	}

	config.MCPCatalog = mcpCatalog
	return nil
}

// resolveMCPConfigClientIDs resolves MCPClientName to MCPClientID for each MCP config.
// This is needed when parsing virtual keys from config.json, which uses "mcp_client_name"
// instead of "mcp_client_id". The function looks up each MCP client by name and sets the
// corresponding MCPClientID. Configs with unresolvable names are logged and skipped.
// Returns the filtered slice containing only configs with valid MCPClientIDs.
func resolveMCPConfigClientIDs(
	ctx context.Context,
	store configstore.ConfigStore,
	mcpConfigs []configstoreTables.TableVirtualKeyMCPConfig,
	virtualKeyID string,
) []configstoreTables.TableVirtualKeyMCPConfig {
	if store == nil || len(mcpConfigs) == 0 {
		return mcpConfigs
	}

	resolvedConfigs := make([]configstoreTables.TableVirtualKeyMCPConfig, 0, len(mcpConfigs))

	for i := range mcpConfigs {
		mc := &mcpConfigs[i]

		// If MCPClientID is already set (e.g., from database or direct construction), keep it
		if mc.MCPClientID != 0 {
			resolvedConfigs = append(resolvedConfigs, *mc)
			continue
		}

		// If MCPClientName is set (from config.json parsing), resolve it to MCPClientID
		if mc.MCPClientName != "" {
			mcpClient, err := store.GetMCPClientByName(ctx, mc.MCPClientName)
			if err != nil {
				logger.Warn("virtual key %s: failed to resolve MCP client '%s': %v (skipping this MCP config)",
					virtualKeyID, mc.MCPClientName, err)
				continue
			}
			if mcpClient == nil {
				logger.Warn("virtual key %s: MCP client '%s' not found (skipping this MCP config)",
					virtualKeyID, mc.MCPClientName)
				continue
			}
			mc.MCPClientID = mcpClient.ID
			resolvedConfigs = append(resolvedConfigs, *mc)
			continue
		}

		// Neither MCPClientID nor MCPClientName is set - skip this config
		logger.Warn("virtual key %s: MCP config has neither mcp_client_id nor mcp_client_name set (skipping)",
			virtualKeyID)
	}

	return resolvedConfigs
}

// reconcileVirtualKeyAssociations reconciles ProviderConfigs and MCPConfigs associations
// for a virtual key when config.json changes (hash mismatch already detected at VK level).
//
// NOTE: This function is ONLY called when the virtual key's hash has changed,
// meaning something in config.json was modified for this VK. It is NOT called
// when hashes match (in that case, DB config is kept as-is).
//
// Reconciliation strategy (file is source of truth when hash changes):
// - Configs in both file and DB → update from file
// - Configs only in file → create new
// - Configs only in DB → DELETE (file is source of truth, extra configs are removed)
func reconcileVirtualKeyAssociations(
	ctx context.Context,
	store configstore.ConfigStore,
	tx *gorm.DB,
	vkID string,
	newProviderConfigs []configstoreTables.TableVirtualKeyProviderConfig,
	newMCPConfigs []configstoreTables.TableVirtualKeyMCPConfig,
) error {
	// Reconcile ProviderConfigs
	existingProviderConfigs, err := store.GetVirtualKeyProviderConfigs(ctx, vkID)
	if err != nil {
		return fmt.Errorf("failed to get existing provider configs: %w", err)
	}

	// Build lookup map for existing configs by Provider (unique per VK)
	existingByProvider := make(map[string]configstoreTables.TableVirtualKeyProviderConfig)
	for _, pc := range existingProviderConfigs {
		existingByProvider[pc.Provider] = pc
	}

	// Process provider configs from config.json
	newProviderSet := make(map[string]bool)
	for _, newPC := range newProviderConfigs {
		newProviderSet[newPC.Provider] = true
		newPC.VirtualKeyID = vkID
		if existing, found := existingByProvider[newPC.Provider]; found {
			// Update existing provider config from file
			existing.Weight = newPC.Weight
			existing.AllowedModels = newPC.AllowedModels
			existing.BudgetID = newPC.BudgetID
			existing.RateLimitID = newPC.RateLimitID
			existing.Keys = newPC.Keys
			if err := store.UpdateVirtualKeyProviderConfig(ctx, &existing, tx); err != nil {
				return fmt.Errorf("failed to update provider config for %s: %w", newPC.Provider, err)
			}
		} else {
			// Create new provider config from file
			if err := store.CreateVirtualKeyProviderConfig(ctx, &newPC, tx); err != nil {
				return fmt.Errorf("failed to create provider config for %s: %w", newPC.Provider, err)
			}
		}
	}

	// Delete provider configs that exist in DB but not in file
	for provider, existing := range existingByProvider {
		if !newProviderSet[provider] {
			if err := store.DeleteVirtualKeyProviderConfig(ctx, existing.ID, tx); err != nil {
				return fmt.Errorf("failed to delete provider config for %s: %w", provider, err)
			}
		}
	}

	// Reconcile MCPConfigs
	existingMCPConfigs, err := store.GetVirtualKeyMCPConfigs(ctx, vkID)
	if err != nil {
		return fmt.Errorf("failed to get existing MCP configs: %w", err)
	}

	// Build lookup map for existing MCP configs by MCPClientID
	existingByMCPClientID := make(map[uint]configstoreTables.TableVirtualKeyMCPConfig)
	for _, mc := range existingMCPConfigs {
		existingByMCPClientID[mc.MCPClientID] = mc
	}

	// Process MCP configs from config.json
	newMCPSet := make(map[uint]bool)
	for _, newMC := range newMCPConfigs {
		newMCPSet[newMC.MCPClientID] = true
		newMC.VirtualKeyID = vkID
		if existing, found := existingByMCPClientID[newMC.MCPClientID]; found {
			// Update existing MCP config from file
			existing.ToolsToExecute = newMC.ToolsToExecute
			if err := store.UpdateVirtualKeyMCPConfig(ctx, &existing, tx); err != nil {
				return fmt.Errorf("failed to update MCP config for client %d: %w", newMC.MCPClientID, err)
			}
		} else {
			// Create new MCP config from file
			if err := store.CreateVirtualKeyMCPConfig(ctx, &newMC, tx); err != nil {
				return fmt.Errorf("failed to create MCP config for client %d: %w", newMC.MCPClientID, err)
			}
		}
	}

	// Delete MCP configs that exist in DB but not in file
	for mcpClientID, existing := range existingByMCPClientID {
		if !newMCPSet[mcpClientID] {
			if err := store.DeleteVirtualKeyMCPConfig(ctx, existing.ID, tx); err != nil {
				return fmt.Errorf("failed to delete MCP config for client %d: %w", mcpClientID, err)
			}
		}
	}

	return nil
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

// GetHeaderFilterConfig returns the global header filter configuration
// Note: This method doesn't use locking for performance. In rare cases during
// config updates, it may return stale data, but this is acceptable since pointer
// reads are atomic and won't cause panics.
func (c *Config) GetHeaderFilterConfig() *configstoreTables.GlobalHeaderFilterConfig {
	return c.ClientConfig.HeaderFilterConfig
}

// GetLoadedLLMPlugins returns the current snapshot of loaded LLM plugins.
// This method is lock-free and safe for concurrent access from hot paths.
// It returns the plugin slice from the atomic pointer, which is safe to iterate
// even if plugins are being updated concurrently.
// Do not modify the returned slice; it is a shared snapshot and must be treated read-only.
func (c *Config) GetLoadedLLMPlugins() []schemas.LLMPlugin {
	if plugins := c.LLMPlugins.Load(); plugins != nil {
		return slices.Clone(*plugins)
	}
	return nil
}

// pluginChunkInterceptor implements StreamChunkInterceptor by calling plugin hooks
type pluginChunkInterceptor struct {
	plugins []schemas.HTTPTransportPlugin
}

// InterceptChunk processes a chunk through all plugin HTTPTransportStreamChunkHook methods.
// Plugins are called in reverse order (same as PostHook) so modifications chain correctly.
func (i *pluginChunkInterceptor) InterceptChunk(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, stream *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	for j := len(i.plugins) - 1; j >= 0; j-- {
		modified, err := i.plugins[j].HTTPTransportStreamChunkHook(ctx, req, stream)
		if err != nil {
			return modified, fmt.Errorf("failed to intercept chunk with plugin %s: %w", i.plugins[j].GetName(), err)
		}
		if modified == nil {
			return nil, nil // Plugin wants to skip this chunk
		}
		stream = modified
	}
	return stream, nil
}

// GetStreamChunkInterceptor returns the chunk interceptor for streaming responses.
// Returns nil if no plugins are loaded.
func (c *Config) GetStreamChunkInterceptor() StreamChunkInterceptor {
	plugins := c.GetLoadedHTTPTransportPlugins()
	if len(plugins) == 0 {
		return nil
	}
	return &pluginChunkInterceptor{plugins: plugins}
}

// GetAsyncJobExecutor returns the async job executor.
// Returns nil if LogsStore or governance plugin is not configured.
func (c *Config) GetAsyncJobExecutor() *logstore.AsyncJobExecutor {
	return c.AsyncJobExecutor
}

// GetAsyncJobResultTTL returns the default TTL for async job results in seconds.
func (c *Config) GetAsyncJobResultTTL() int {
	if c.ClientConfig.AsyncJobResultTTL > 0 {
		return c.ClientConfig.AsyncJobResultTTL
	}
	return logstore.DefaultAsyncJobResultTTL
}

// GetLoadedMCPPlugins returns the current snapshot of loaded MCP plugins.
// This method is lock-free and safe for concurrent access from hot paths.
// It returns the plugin slice from the atomic pointer, which is safe to iterate
// even if plugins are being updated concurrently.
// Do not modify the returned slice; it is a shared snapshot and must be treated read-only.
func (c *Config) GetLoadedMCPPlugins() []schemas.MCPPlugin {
	if plugins := c.MCPPlugins.Load(); plugins != nil {
		return slices.Clone(*plugins)
	}
	return nil
}

// GetLoadedHTTPTransportPlugins returns all loaded plugins that implement HTTPTransportPlugin interface.
// This method returns a cached list that is updated on plugin add/reload/remove operations.
// It is lock-free and safe for concurrent access from hot paths.
// Do not modify the returned slice; it is a shared snapshot and must be treated read-only.
func (c *Config) GetLoadedHTTPTransportPlugins() []schemas.HTTPTransportPlugin {
	if plugins := c.HTTPTransportPlugins.Load(); plugins != nil {
		return slices.Clone(*plugins)
	}
	return nil
}

// rebuildInterfaceCaches rebuilds all plugin interface caches from BasePlugins
// This is called automatically after any RegisterPlugin/UnregisterPlugin operation
// PERFORMANCE: Single-pass implementation - iterates BasePlugins once and checks all interfaces
// This is 3x faster than the old approach of separate rebuilds (O(N) instead of O(3N))
func (c *Config) rebuildInterfaceCaches() {
	basePlugins := c.BasePlugins.Load()
	if basePlugins == nil {
		// Clear all caches atomically
		emptyLLM := []schemas.LLMPlugin{}
		emptyMCP := []schemas.MCPPlugin{}
		emptyHTTP := []schemas.HTTPTransportPlugin{}

		c.LLMPlugins.Store(&emptyLLM)
		c.MCPPlugins.Store(&emptyMCP)
		c.HTTPTransportPlugins.Store(&emptyHTTP)
		return
	}

	// Single pass through all plugins - check all interfaces in one iteration
	var llm []schemas.LLMPlugin
	var mcp []schemas.MCPPlugin
	var httpTransport []schemas.HTTPTransportPlugin

	for _, p := range *basePlugins {
		if llmPlugin, ok := p.(schemas.LLMPlugin); ok {
			llm = append(llm, llmPlugin)
		}
		if mcpPlugin, ok := p.(schemas.MCPPlugin); ok {
			mcp = append(mcp, mcpPlugin)
		}
		if httpPlugin, ok := p.(schemas.HTTPTransportPlugin); ok {
			httpTransport = append(httpTransport, httpPlugin)
		}
	}

	// Atomic stores of all caches
	c.LLMPlugins.Store(&llm)
	c.MCPPlugins.Store(&mcp)
	c.HTTPTransportPlugins.Store(&httpTransport)
}

// IsPluginLoaded checks if a plugin with the given name is currently loaded.
// This method is lock-free and safe for concurrent access from hot paths.
func (c *Config) IsPluginLoaded(name string) bool {
	basePlugins := c.BasePlugins.Load()
	if basePlugins == nil {
		return false
	}

	for _, p := range *basePlugins {
		if p.GetName() == name {
			return true
		}
	}

	return false
}

// UpdatePluginOverallStatus updates the overall status of a plugin
func (c *Config) UpdatePluginOverallStatus(name string, displayName string, status string, logs []string, types []schemas.PluginType) {
	c.pluginStatusMu.Lock()
	defer c.pluginStatusMu.Unlock()

	if c.pluginStatus == nil {
		c.pluginStatus = make(map[string]schemas.PluginStatus)
	}

	logsCopy := make([]string, len(logs))
	copy(logsCopy, logs)

	typesCopy := make([]schemas.PluginType, len(types))
	copy(typesCopy, types)

	c.pluginStatus[name] = schemas.PluginStatus{
		Name:   displayName,
		Status: status,
		Logs:   logsCopy,
		Types:  typesCopy,
	}
}

// UpdatePluginDisplayName updates the display name of a plugin
func (c *Config) UpdatePluginDisplayName(name string, displayName string) error {
	c.pluginStatusMu.Lock()
	defer c.pluginStatusMu.Unlock()

	// Make sure that the display name is not already in use
	seen := false
	for _, status := range c.pluginStatus {
		if status.Name == displayName {
			seen = true
			break
		}
	}
	if seen {
		return fmt.Errorf("display name %s already in use", displayName)
	}

	if _, ok := c.pluginStatus[name]; ok {
		c.pluginStatus[name] = schemas.PluginStatus{
			Name:   displayName,
			Status: c.pluginStatus[name].Status,
			Logs:   c.pluginStatus[name].Logs,
			Types:  c.pluginStatus[name].Types,
		}
		return nil
	}
	return fmt.Errorf("plugin %s not found", name)
}

// UpdatePluginStatus updates the status of a plugin
func (c *Config) UpdatePluginStatus(name string, status string) error {
	c.pluginStatusMu.Lock()
	defer c.pluginStatusMu.Unlock()

	oldEntry, ok := c.pluginStatus[name]
	if !ok {
		return fmt.Errorf("plugin %s not found", name)
	}

	newEntry := oldEntry
	newEntry.Status = status

	c.pluginStatus[name] = newEntry
	return nil
}

// AppendPluginStateLogs appends logs to a plugin status entry
func (c *Config) AppendPluginStateLogs(name string, logs []string) error {
	c.pluginStatusMu.Lock()
	defer c.pluginStatusMu.Unlock()
	oldEntry, ok := c.pluginStatus[name]
	if !ok {
		return fmt.Errorf("plugin %s not found", name)
	}
	newEntry := oldEntry
	newEntry.Logs = append(oldEntry.Logs, logs...)
	c.pluginStatus[name] = newEntry
	return nil
}

// GetPluginNameByDisplayName returns the name of a plugin by its display name
func (c *Config) GetPluginNameByDisplayName(displayName string) (string, bool) {
	c.pluginStatusMu.RLock()
	defer c.pluginStatusMu.RUnlock()
	for name, status := range c.pluginStatus {
		if status.Name == displayName {
			return name, true
		}
	}
	return "", false
}

// DeletePluginStatus completely removes a plugin status entry
func (c *Config) DeletePluginOverallStatus(name string) {
	c.pluginStatusMu.Lock()
	defer c.pluginStatusMu.Unlock()

	delete(c.pluginStatus, name)
}

// GetPluginStatus returns the status of all plugins
func (c *Config) GetPluginStatus() map[string]schemas.PluginStatus {
	c.pluginStatusMu.RLock()
	defer c.pluginStatusMu.RUnlock()

	result := make(map[string]schemas.PluginStatus, len(c.pluginStatus))
	maps.Copy(result, c.pluginStatus)

	return result
}

// GetPluginStatusByName returns the status of a specific plugin
func (c *Config) GetPluginStatusByName(name string) (schemas.PluginStatus, bool) {
	c.pluginStatusMu.RLock()
	defer c.pluginStatusMu.RUnlock()

	status, ok := c.pluginStatus[name]
	return status, ok
}

// ReloadPlugin adds or updates a plugin in the registry
// This is the single entry point for all plugin additions/updates
// If a plugin with the same name exists, it will be replaced (atomic find-and-replace)
// If no plugin exists with that name, it will be added
func (c *Config) ReloadPlugin(plugin schemas.BasePlugin) error {
	c.pluginsMu.Lock()
	defer c.pluginsMu.Unlock()

	name := plugin.GetName()

	for {
		oldPlugins := c.BasePlugins.Load()
		var newPlugins []schemas.BasePlugin

		if oldPlugins == nil {
			newPlugins = []schemas.BasePlugin{plugin}
		} else {
			newPlugins = make([]schemas.BasePlugin, 0, len(*oldPlugins)+1)

			replaced := false
			for _, p := range *oldPlugins {
				if p.GetName() == name {
					newPlugins = append(newPlugins, plugin) // Replace with new
					replaced = true
				} else {
					newPlugins = append(newPlugins, p) // Keep existing
				}
			}

			if !replaced {
				newPlugins = append(newPlugins, plugin) // Add as new
			}
		}

		if c.BasePlugins.CompareAndSwap(oldPlugins, &newPlugins) {
			c.rebuildInterfaceCaches()
			return nil
		}
		// CAS failed, retry with new snapshot
	}
}

// UnregisterPlugin removes a plugin from the registry
func (c *Config) UnregisterPlugin(name string) error {
	c.pluginsMu.Lock()
	defer c.pluginsMu.Unlock()

	for {
		oldPlugins := c.BasePlugins.Load()
		if oldPlugins == nil {
			return fmt.Errorf("plugin %s not found", name)
		}

		newPlugins := make([]schemas.BasePlugin, 0, len(*oldPlugins))
		found := false
		for _, p := range *oldPlugins {
			if p.GetName() == name {
				found = true
				continue
			}
			newPlugins = append(newPlugins, p)
		}

		if !found {
			return fmt.Errorf("plugin %s not found", name)
		}

		if c.BasePlugins.CompareAndSwap(oldPlugins, &newPlugins) {
			c.rebuildInterfaceCaches()
			return nil
		}
		// CAS failed, retry with new snapshot
	}
}

// FindPluginAs finds a plugin by name in the given config and returns it as type T
// Returns error if plugin not found or doesn't implement T
// This is a type-safe finder that eliminates manual type assertions
// Usage: plugin, err := lib.FindPluginAs[*mypackage.MyPluginType](config, "plugin-name")
func FindPluginAs[T any](c *Config, name string) (T, error) {
	var zero T

	basePlugins := c.BasePlugins.Load()
	if basePlugins == nil {
		return zero, fmt.Errorf("plugin %s not found", name)
	}

	for _, p := range *basePlugins {
		if p.GetName() == name {
			if typed, ok := p.(T); ok {
				return typed, nil
			}
			return zero, fmt.Errorf("plugin %s does not implement required interface", name)
		}
	}

	return zero, fmt.Errorf("plugin %s not found", name)
}

// FindLLMPlugin is a convenience wrapper for finding LLM plugins
func (c *Config) FindLLMPlugin(name string) (schemas.LLMPlugin, error) {
	return FindPluginAs[schemas.LLMPlugin](c, name)
}

// FindMCPPlugin is a convenience wrapper for finding MCP plugins
func (c *Config) FindMCPPlugin(name string) (schemas.MCPPlugin, error) {
	return FindPluginAs[schemas.MCPPlugin](c, name)
}

// FindPluginByName returns a plugin as BasePlugin
// For most cases, use FindPluginAs[T] for type-safe access
func (c *Config) FindPluginByName(name string) (schemas.BasePlugin, error) {
	return FindPluginAs[schemas.BasePlugin](c, name)
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

	return config.Redacted(), nil
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
		return fmt.Errorf("provider %s: %w", provider, ErrAlreadyExists)
	}
	// Validate CustomProviderConfig if present
	if err := ValidateCustomProvider(config, provider); err != nil {
		return err
	}
	for i, key := range config.Keys {
		if key.ID == "" {
			config.Keys[i].ID = uuid.NewString()
		}
	}
	// First add the provider to the store
	skipDBUpdate := false
	if ctx.Value(schemas.BifrostContextKeySkipDBUpdate) != nil {
		if skip, ok := ctx.Value(schemas.BifrostContextKeySkipDBUpdate).(bool); ok {
			skipDBUpdate = skip
		}
	}
	if c.ConfigStore != nil && !skipDBUpdate {
		if err := c.ConfigStore.AddProvider(ctx, provider, config); err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				return ErrNotFound
			}
			// If the provider already exists in the DB (e.g., from a previous failed attempt)
			// but not in the in-memory map, sync it to memory and return ErrAlreadyExists
			// so the caller can proceed with an update instead of failing.
			if errors.Is(err, configstore.ErrAlreadyExists) {
				c.Providers[provider] = config
				logger.Info("provider %s already exists in DB, synced to memory", provider)
				return fmt.Errorf("provider %s: %w", provider, ErrAlreadyExists)
			}
			return fmt.Errorf("failed to update provider config in store: %w", err)
		}
	}
	c.Providers[provider] = config
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
	// Preserve the existing ConfigHash - this is the original hash from config.json
	// and must be retained so that on server restart, the hash comparison works correctly
	// and user's key value changes are preserved (not overwritten by config.json)
	config.ConfigHash = existingConfig.ConfigHash
	// Update in-memory configuration first (so client can read updated config)
	c.Providers[provider] = config
	for i, key := range config.Keys {
		if key.ID == "" {
			config.Keys[i].ID = uuid.NewString()
		}
	}
	skipDBUpdate := false
	if ctx.Value(schemas.BifrostContextKeySkipDBUpdate) != nil {
		if skip, ok := ctx.Value(schemas.BifrostContextKeySkipDBUpdate).(bool); ok {
			skipDBUpdate = skip
		}
	}
	if c.ConfigStore != nil && !skipDBUpdate {
		// Process environment variables in keys (including key-level configs)
		// Update provider in database within a transaction
		dbErr := c.ConfigStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			if err := c.ConfigStore.UpdateProvider(ctx, provider, config, tx); err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return ErrNotFound
				}
				return fmt.Errorf("failed to update provider config in store: %w", err)
			}
			return nil
		})
		if dbErr != nil {
			// Rollback in-memory changes if database transaction failed
			c.Providers[provider] = existingConfig
			return dbErr
		}
	}
	// Release lock before calling client.UpdateProvider to avoid deadlock
	// client.UpdateProvider will call GetConfigForProvider which needs RLock
	c.Mu.Unlock()

	// Update client provider - this may acquire its own locks
	clientErr := c.client.UpdateProvider(provider)

	// Re-acquire lock for cleanup (defer will unlock at function return)
	c.Mu.Lock()

	if clientErr != nil {
		// Rollback in-memory changes if client update failed and the current config is still the one this call applied to
		if reflect.DeepEqual(c.Providers[provider], config) {
			c.Providers[provider] = existingConfig
		}
		// If database was updated, we can't rollback the transaction here
		// but the in-memory state will be consistent
		return fmt.Errorf("failed to update provider: %w", clientErr)
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
	// Delete from DB first to avoid memory/DB inconsistency if DB delete fails
	skipDBUpdate := false
	if ctx.Value(schemas.BifrostContextKeySkipDBUpdate) != nil {
		if skip, ok := ctx.Value(schemas.BifrostContextKeySkipDBUpdate).(bool); ok {
			skipDBUpdate = skip
		}
	}
	if c.ConfigStore != nil && !skipDBUpdate {
		if err := c.ConfigStore.DeleteProvider(ctx, provider); err != nil {
			return fmt.Errorf("failed to delete provider config from store: %w", err)
		}
	}
	delete(c.Providers, provider)
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
			models := key.Models
			if models == nil {
				models = []string{}
			}
			keys = append(keys, configstoreTables.TableKey{
				KeyID:      key.ID,
				Name:       key.Name,
				Value:      *schemas.NewEnvVar(""),
				Models:     models,
				Weight:     bifrost.Ptr(key.Weight),
				Provider:   string(providerKey),
				ConfigHash: key.ConfigHash,
			})
		}
	}

	return keys, nil
}

// SetBifrostClient sets the Bifrost client in the store.
// This is used to allow the store to access the Bifrost client.
// This is useful for the MCP handler to access the Bifrost client.
func (c *Config) SetBifrostClient(client *bifrost.Bifrost) {
	c.muMCP.Lock()
	defer c.muMCP.Unlock()

	c.client = client
}

// GetMCPClient gets an MCP client configuration from the configuration.
// This method is called when an MCP client is reconnected via the HTTP API.
//
// Parameters:
//   - id: ID of the client to get
//
// Returns:
//   - *schemas.MCPClientConfig: The MCP client configuration (not redacted)
//   - error: Any retrieval error
func (c *Config) GetMCPClient(id string) (*schemas.MCPClientConfig, error) {
	c.muMCP.RLock()
	defer c.muMCP.RUnlock()

	if c.client == nil {
		return nil, fmt.Errorf("bifrost client not set")
	}

	if c.MCPConfig == nil {
		return nil, fmt.Errorf("no MCP config found")
	}

	for _, clientConfig := range c.MCPConfig.ClientConfigs {
		if clientConfig.ID == id {
			return clientConfig, nil
		}
	}

	return nil, fmt.Errorf("MCP client '%s' not found", id)
}

// AddMCPClient adds a new MCP client to the configuration.
// This method is called when a new MCP client is added via the HTTP API.
//
// The method:
//   - Validates that the MCP client doesn't already exist
//   - Processes environment variables in the MCP client configuration
//   - Stores the processed configuration in memory
func (c *Config) AddMCPClient(ctx context.Context, clientConfig *schemas.MCPClientConfig) error {
	if c.client == nil {
		return fmt.Errorf("bifrost client not set")
	}
	c.muMCP.Lock()
	defer c.muMCP.Unlock()
	if c.MCPConfig == nil {
		c.MCPConfig = &schemas.MCPConfig{}
	}
	// Track new environment variables
	c.MCPConfig.ClientConfigs = append(c.MCPConfig.ClientConfigs, clientConfig)
	// Config with processed env vars
	if err := c.client.AddMCPClient(clientConfig); err != nil {
		c.MCPConfig.ClientConfigs = c.MCPConfig.ClientConfigs[:len(c.MCPConfig.ClientConfigs)-1]
		return fmt.Errorf("failed to connect MCP client: %w", err)
	}
	// Update MCP catalog pricing data for the new client
	if c.MCPCatalog != nil && c.ConfigStore != nil {
		// Get the created client config from store to get tool_pricing
		dbClientConfig, err := c.ConfigStore.GetMCPClientByName(ctx, clientConfig.Name)
		if err != nil {
			logger.Warn("failed to get MCP client config for catalog update: %v", err)
		} else if dbClientConfig != nil {
			for toolName, costPerExecution := range dbClientConfig.ToolPricing {
				c.MCPCatalog.UpdatePricingData(dbClientConfig.Name, toolName, costPerExecution)
			}
			logger.Debug("updated MCP catalog pricing for client: %s (%d tools)", dbClientConfig.Name, len(dbClientConfig.ToolPricing))
		}
	}
	return nil
}

// UpdateMCPClient edits an MCP client configuration.
// This allows for dynamic MCP client management at runtime with proper env var handling.
//
// Parameters:
//   - id: ID of the client to edit
//   - updatedConfig: Updated MCP client configuration
func (c *Config) UpdateMCPClient(ctx context.Context, id string, updatedConfig *schemas.MCPClientConfig) error {
	if c.client == nil {
		return fmt.Errorf("bifrost client not set")
	}
	c.muMCP.Lock()
	defer c.muMCP.Unlock()

	if c.MCPConfig == nil {
		return fmt.Errorf("no MCP config found")
	}
	// Find the existing client config
	var oldConfig *schemas.MCPClientConfig
	var found bool
	var configIndex int
	for i, clientConfig := range c.MCPConfig.ClientConfigs {
		if clientConfig.ID == id {
			oldConfig = clientConfig
			configIndex = i
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("MCP client '%s' not found", id)
	}
	// Check if client is registered in Bifrost (can be not registered if client initialization failed)
	if clients, err := c.client.GetMCPClients(); err == nil && len(clients) > 0 {
		for _, client := range clients {
			if client.Config.ID == id {
				if err := c.client.UpdateMCPClient(id, updatedConfig); err != nil {
					// Rollback in-memory changes
					c.MCPConfig.ClientConfigs[configIndex] = oldConfig
					return fmt.Errorf("failed to edit MCP client: %w", err)
				}
				break
			}
		}
	}
	// Update MCP catalog pricing data for the edited client
	if c.MCPCatalog != nil {
		// If the client name has changed, delete all old pricing entries under the old name
		if updatedConfig.Name != oldConfig.Name {
			for toolName := range oldConfig.ToolPricing {
				c.MCPCatalog.DeletePricingData(oldConfig.Name, toolName)
			}
			logger.Debug("deleted old MCP catalog pricing for renamed client: %s -> %s (%d tools)", oldConfig.Name, updatedConfig.Name, len(oldConfig.ToolPricing))
		} else {
			// If name hasn't changed, remove pricing entries that were deleted
			for toolName := range oldConfig.ToolPricing {
				if _, exists := updatedConfig.ToolPricing[toolName]; !exists {
					c.MCPCatalog.DeletePricingData(updatedConfig.Name, toolName)
				}
			}
		}
		// Then, add or update pricing entries from the new config (with new name if changed)
		for toolName, costPerExecution := range updatedConfig.ToolPricing {
			c.MCPCatalog.UpdatePricingData(updatedConfig.Name, toolName, costPerExecution)
		}
		logger.Debug("updated MCP catalog pricing for client: %s (%d tools)", updatedConfig.Name, len(updatedConfig.ToolPricing))
	}
	// Update the in-memory configuration with only the fields that were changed
	// Preserve connection info (connection_type, connection_string, stdio_config) from oldConfig
	// as these are read-only and not sent in the update request
	c.MCPConfig.ClientConfigs[configIndex].Name = updatedConfig.Name
	c.MCPConfig.ClientConfigs[configIndex].IsCodeModeClient = updatedConfig.IsCodeModeClient
	c.MCPConfig.ClientConfigs[configIndex].Headers = updatedConfig.Headers
	c.MCPConfig.ClientConfigs[configIndex].ToolsToExecute = updatedConfig.ToolsToExecute
	c.MCPConfig.ClientConfigs[configIndex].ToolsToAutoExecute = updatedConfig.ToolsToAutoExecute
	c.MCPConfig.ClientConfigs[configIndex].ToolPricing = updatedConfig.ToolPricing
	c.MCPConfig.ClientConfigs[configIndex].IsPingAvailable = updatedConfig.IsPingAvailable
	c.MCPConfig.ClientConfigs[configIndex].ToolSyncInterval = updatedConfig.ToolSyncInterval
	return nil
}

// RemoveMCPClient removes an MCP client from the configuration.
// This method is called when an MCP client is removed via the HTTP API.
//
// The method:
//   - Validates that the MCP client exists
//   - Removes the MCP client from the configuration
//   - Removes the MCP client from the Bifrost client
func (c *Config) RemoveMCPClient(ctx context.Context, id string) error {
	if c.client == nil {
		return fmt.Errorf("bifrost client not set")
	}
	c.muMCP.Lock()
	defer c.muMCP.Unlock()
	if c.MCPConfig == nil {
		return fmt.Errorf("no MCP config found")
	}
	// Check if client is registered in Bifrost (can be not registered if client initialization failed)
	if clients, err := c.client.GetMCPClients(); err == nil && len(clients) > 0 {
		for _, client := range clients {
			if client.Config.ID == id {
				if err := c.client.RemoveMCPClient(id); err != nil {
					return fmt.Errorf("failed to remove MCP client: %w", err)
				}
				break
			}
		}
	}
	// Find and remove client from in-memory config
	for i, clientConfig := range c.MCPConfig.ClientConfigs {
		if clientConfig.ID == id {
			c.MCPConfig.ClientConfigs = append(c.MCPConfig.ClientConfigs[:i], c.MCPConfig.ClientConfigs[i+1:]...)
			break
		}
	}
	return nil
}

// RedactMCPClientConfig creates a redacted copy of a MCPClientConfig configuration.
// Connection strings and headers are redacted for safe external exposure.
func (c *Config) RedactMCPClientConfig(config *schemas.MCPClientConfig) *schemas.MCPClientConfig {
	// Create an actual copy of the struct (not just a pointer copy)
	// This prevents modifying the original config when redacting
	configCopy := *config

	// Redact connection string if present
	if config.ConnectionString != nil {
		configCopy.ConnectionString = config.ConnectionString.Redacted()
	}

	// Redact Header values if present
	if config.Headers != nil {
		configCopy.Headers = make(map[string]schemas.EnvVar, len(config.Headers))
		for header, value := range config.Headers {
			configCopy.Headers[header] = *value.Redacted()
		}
	}

	return &configCopy
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
							Value:  *schemas.NewEnvVar(apiKey),
							Models: []string{}, // Empty means all supported models
							Weight: 1.0,
						},
					},
					ConcurrencyAndBufferSize: &schemas.DefaultConcurrencyAndBufferSize,
				}
				// Add to providers map
				c.Providers[provider] = providerConfig
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
		if redactedWeaviateConfig.APIKey != nil {
			redactedWeaviateConfig.APIKey = redactedWeaviateConfig.APIKey.Redacted()
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

	// Reject Bedrock providers with IsKeyLess=true
	if cpc.BaseProviderType == schemas.Bedrock && cpc.IsKeyLess {
		return fmt.Errorf("custom provider validation failed: Bedrock providers cannot be keyless (is_key_less=true)")
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

	// Validate the new config (this will catch Bedrock+IsKeyLess configurations)
	if err := ValidateCustomProvider(newConfig, provider); err != nil {
		return err
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

func (c *Config) GetAvailableProviders() []schemas.ModelProvider {
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	availableProviders := []schemas.ModelProvider{}
	for provider, config := range c.Providers {
		// Check if the provider has at least one key with a non-empty value. If so, add the provider to the list.
		for _, key := range config.Keys {
			if key.Value.GetValue() != "" {
				if key.Enabled != nil && !*key.Enabled {
					continue
				}
				availableProviders = append(availableProviders, provider)
				break
			}
		}
	}
	return availableProviders
}

func DeepCopy[T any](in T) (T, error) {
	var out T
	b, err := sonic.Marshal(in)
	if err != nil {
		return out, err
	}
	err = sonic.Unmarshal(b, &out)
	return out, err
}

func applyGovernancePricingOverrides(catalog *modelcatalog.ModelCatalog, governanceConfig *configstore.GovernanceConfig) {
	if catalog == nil {
		return
	}
	if governanceConfig == nil {
		if err := catalog.SetPricingOverrides(nil); err != nil {
			logger.Warn("failed to clear pricing overrides from model catalog: %v", err)
		}
		return
	}
	if err := catalog.SetPricingOverrides(governanceConfig.PricingOverrides); err != nil {
		logger.Warn("failed to load scoped pricing overrides into model catalog: %v", err)
	}
}
