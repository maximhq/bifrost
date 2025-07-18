// Package lib provides core functionality for the Bifrost HTTP service,
// including context propagation, header management, and integration with monitoring systems.
package lib

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/meta"
)

// ConfigStore represents a high-performance in-memory configuration store for Bifrost.
// It provides thread-safe access to provider configurations with the ability to
// persist changes back to the original JSON configuration file.
//
// Features:
//   - Pure in-memory storage for ultra-fast access
//   - Environment variable processing for API keys and key-level configurations
//   - Thread-safe operations with read-write mutexes
//   - Real-time configuration updates via HTTP API
//   - Explicit persistence control via WriteConfigToFile()
//   - Support for provider-specific key configurations (Azure, Vertex) and meta configurations (Bedrock)
type ConfigStore struct {
	mu         sync.RWMutex
	muMCP      sync.RWMutex
	logger     schemas.Logger
	configPath string // Path to the original JSON config file
	client     *bifrost.Bifrost

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
	DropExcessRequests: false,
	PrometheusLabels:   []string{},
	InitialPoolSize:    300,
	EnableLogging:      true,
}

// NewConfigStore creates a new in-memory configuration store instance.
func NewConfigStore(logger schemas.Logger) (*ConfigStore, error) {
	return &ConfigStore{
		logger:    logger,
		Providers: make(map[schemas.ModelProvider]ProviderConfig),
		EnvKeys:   make(map[string][]EnvKeyInfo),
	}, nil
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
	s.mu.Lock()
	defer s.mu.Unlock()

	s.configPath = configPath
	s.logger.Info(fmt.Sprintf("Loading configuration from: %s", configPath))

	// Check if config file exists
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			s.logger.Info(fmt.Sprintf("Config file %s not found, starting with default configuration. Providers can be added dynamically via UI.", configPath))

			// Initialize with default configuration
			s.ClientConfig = DefaultClientConfig
			s.Providers = make(map[schemas.ModelProvider]ProviderConfig)
			s.MCPConfig = nil

			// Auto-detect and configure providers from common environment variables
			s.autoDetectProviders()

			s.logger.Info("Successfully initialized with default configuration.")
			return nil
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
	s.mu.RLock()
	defer s.mu.RUnlock()

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

// SaveConfig writes the current configuration back to the original config file path
func (s *ConfigStore) SaveConfig() error {
	if s.configPath == "" {
		return fmt.Errorf("no config path set - use LoadFromConfig first")
	}
	return s.writeConfigToFile(s.configPath)
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
