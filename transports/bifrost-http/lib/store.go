// Package lib provides core functionality for the Bifrost HTTP service,
// including context propagation, header management, and integration with monitoring systems.
package lib

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/meta"
)

// ConfigStore represents a high-performance in-memory configuration store for Bifrost.
// It provides thread-safe access to provider configurations with the ability to
// persist changes back to the original JSON configuration file.
//
// Features:
//   - Pure in-memory storage for ultra-fast access
//   - Environment variable processing for API keys and meta configurations
//   - Thread-safe operations with read-write mutexes
//   - Real-time configuration updates via HTTP API
//   - Explicit persistence control via WriteConfigToFile()
//   - Support for all provider-specific meta configurations (Azure, Bedrock, Vertex)
type ConfigStore struct {
	mu         sync.RWMutex
	logger     schemas.Logger
	configPath string // Path to the original JSON config file

	// In-memory storage
	providers map[schemas.ModelProvider]ProviderConfig
	mcpConfig *schemas.MCPConfig
}

// NewConfigStore creates a new in-memory configuration store instance.
func NewConfigStore(logger schemas.Logger) (*ConfigStore, error) {
	return &ConfigStore{
		logger:    logger,
		providers: make(map[schemas.ModelProvider]ProviderConfig),
	}, nil
}

// LoadFromConfig loads initial configuration from a JSON config file into memory
// with full preprocessing including environment variable resolution and meta config parsing.
// All processing is done upfront to ensure zero latency when retrieving data.
//
// This method handles:
//   - JSON config file parsing
//   - Environment variable substitution for API keys (env.VARIABLE_NAME)
//   - Provider-specific meta config processing (Azure, Bedrock, Vertex)
//   - Case conversion for provider names (e.g., "OpenAI" -> "openai")
//   - In-memory storage for ultra-fast access during request processing
func (s *ConfigStore) LoadFromConfig(configPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.configPath = configPath
	s.logger.Info(fmt.Sprintf("Loading configuration from: %s", configPath))

	// Read and parse the JSON config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	// Parse the JSON directly
	var configData struct {
		Providers map[string]json.RawMessage `json:"providers"`
		MCP       json.RawMessage            `json:"mcp,omitempty"`
	}

	if err := json.Unmarshal(data, &configData); err != nil {
		return fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Process provider configurations
	processedProviders := make(map[schemas.ModelProvider]ProviderConfig)

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
		provider := schemas.ModelProvider(strings.ToLower(rawProviderName))

		// Process meta config if it exists
		if tempProvider, exists := tempConfig.Providers[rawProviderName]; exists && len(tempProvider.MetaConfig) > 0 {
			processedMetaConfig, err := s.processMetaConfigEnvVars(tempProvider.MetaConfig, provider)
			if err != nil {
				s.logger.Warn(fmt.Sprintf("failed to process env vars in meta config for %s: %v", provider, err))
				continue
			}

			// Parse and set the meta config
			metaConfig, err := s.parseMetaConfig(processedMetaConfig, provider)
			if err != nil {
				s.logger.Warn(fmt.Sprintf("failed to parse meta config for %s: %v", provider, err))
				continue
			} else {
				cfg.MetaConfig = metaConfig
			}
		}

		// Process environment variables in keys
		for i, key := range cfg.Keys {
			processedValue, err := s.replaceEnvValue(key.Value)
			if err != nil {
				s.logger.Warn(fmt.Sprintf("failed to process env vars in keys for %s: %v", provider, err))
				continue
			}
			cfg.Keys[i].Value = processedValue
		}

		processedProviders[provider] = cfg
	}

	// Store processed configurations in memory
	s.providers = processedProviders

	// Parse MCP config if present
	if len(configData.MCP) > 0 {
		var mcpConfig schemas.MCPConfig
		if err := json.Unmarshal(configData.MCP, &mcpConfig); err != nil {
			s.logger.Warn(fmt.Sprintf("failed to parse MCP config: %v", err))
		} else {
			// Process environment variables in MCP config
			s.mcpConfig = &mcpConfig
			s.processMCPEnvVars()
		}
	}

	s.logger.Info("Successfully loaded configuration.")
	return nil
}

// WriteConfigToFile writes the current in-memory configuration back to a JSON file
// in the exact same format that LoadFromConfig expects. This enables persistence
// of runtime configuration changes.
func (s *ConfigStore) WriteConfigToFile(configPath string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	s.logger.Info(fmt.Sprintf("Writing current configuration to: %s", configPath))

	// Prepare the output structure
	output := struct {
		Providers map[string]interface{} `json:"providers"`
		MCP       *schemas.MCPConfig     `json:"mcp,omitempty"`
	}{
		Providers: make(map[string]interface{}),
		MCP:       s.mcpConfig,
	}

	// Convert providers back to the original format
	for provider, config := range s.providers {
		providerName := string(provider)

		// Create provider config without processed values (keep env.* references)
		providerConfig := map[string]interface{}{
			"keys": config.Keys, // Note: This will contain actual values, not env refs
		}

		if config.NetworkConfig != nil {
			providerConfig["network_config"] = config.NetworkConfig
		}

		if config.ConcurrencyAndBufferSize != nil {
			providerConfig["concurrency_and_buffer_size"] = config.ConcurrencyAndBufferSize
		}

		if config.MetaConfig != nil {
			providerConfig["meta_config"] = *config.MetaConfig
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

	s.logger.Info(fmt.Sprintf("Successfully wrote configuration to: %s", configPath))
	return nil
}

// SaveConfig writes the current configuration back to the original config file path
func (s *ConfigStore) SaveConfig() error {
	if s.configPath == "" {
		return fmt.Errorf("no config path set - use LoadFromConfig first")
	}
	return s.WriteConfigToFile(s.configPath)
}

// parseMetaConfig converts raw JSON to the appropriate provider-specific meta config interface
func (s *ConfigStore) parseMetaConfig(rawMetaConfig json.RawMessage, provider schemas.ModelProvider) (*schemas.MetaConfig, error) {
	switch provider {
	case schemas.Azure:
		var azureMetaConfig meta.AzureMetaConfig
		if err := json.Unmarshal(rawMetaConfig, &azureMetaConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Azure meta config: %w", err)
		}
		var metaConfig schemas.MetaConfig = &azureMetaConfig
		return &metaConfig, nil

	case schemas.Bedrock:
		var bedrockMetaConfig meta.BedrockMetaConfig
		if err := json.Unmarshal(rawMetaConfig, &bedrockMetaConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Bedrock meta config: %w", err)
		}
		var metaConfig schemas.MetaConfig = &bedrockMetaConfig
		return &metaConfig, nil

	case schemas.Vertex:
		var vertexMetaConfig meta.VertexMetaConfig
		if err := json.Unmarshal(rawMetaConfig, &vertexMetaConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Vertex meta config: %w", err)
		}
		var metaConfig schemas.MetaConfig = &vertexMetaConfig
		return &metaConfig, nil
	}

	return nil, fmt.Errorf("unsupported provider for meta config: %s", provider)
}

// replaceEnvValue checks and replaces environment variable references in configuration values.
// Supports the "env.VARIABLE_NAME" syntax for referencing environment variables.
// This enables secure configuration management without hardcoding sensitive values.
//
// Examples:
//   - "env.OPENAI_API_KEY" -> actual value from OPENAI_API_KEY environment variable
//   - "sk-1234567890" -> returned as-is (no env prefix)
//
// Returns an error if the referenced environment variable is not found.
func (s *ConfigStore) replaceEnvValue(value string) (string, error) {
	if strings.HasPrefix(value, "env.") {
		envKey := strings.TrimPrefix(value, "env.")
		if envValue := os.Getenv(envKey); envValue != "" {
			return envValue, nil
		}
		return "", fmt.Errorf("environment variable %s not found", envKey)
	}
	return value, nil
}

// processMetaConfigEnvVars processes environment variables in provider-specific meta configurations.
// This method handles the provider-specific meta config structures and processes environment
// variables in their fields, ensuring type safety and proper field handling.
//
// Supported providers and their processed fields:
//   - Azure: Endpoint, APIVersion
//   - Bedrock: SecretAccessKey, Region, SessionToken, ARN
//   - Vertex: ProjectID, Region, AuthCredentials
//
// For unsupported providers, the meta config is returned unchanged.
// This approach ensures type safety while supporting environment variable substitution.
func (s *ConfigStore) processMetaConfigEnvVars(rawMetaConfig json.RawMessage, provider schemas.ModelProvider) (json.RawMessage, error) {
	switch provider {
	case schemas.Azure:
		var azureMetaConfig meta.AzureMetaConfig
		if err := json.Unmarshal(rawMetaConfig, &azureMetaConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Azure meta config: %w", err)
		}

		endpoint, err := s.replaceEnvValue(azureMetaConfig.Endpoint)
		if err != nil {
			return nil, err
		}
		azureMetaConfig.Endpoint = endpoint
		if azureMetaConfig.APIVersion != nil {
			apiVersion, err := s.replaceEnvValue(*azureMetaConfig.APIVersion)
			if err != nil {
				return nil, err
			}
			azureMetaConfig.APIVersion = &apiVersion
		}

		processedJSON, err := json.Marshal(azureMetaConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal processed Azure meta config: %w", err)
		}
		return processedJSON, nil

	case schemas.Bedrock:
		var bedrockMetaConfig meta.BedrockMetaConfig
		if err := json.Unmarshal(rawMetaConfig, &bedrockMetaConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Bedrock meta config: %w", err)
		}

		secretAccessKey, err := s.replaceEnvValue(bedrockMetaConfig.SecretAccessKey)
		if err != nil {
			return nil, err
		}
		bedrockMetaConfig.SecretAccessKey = secretAccessKey

		if bedrockMetaConfig.Region != nil {
			region, err := s.replaceEnvValue(*bedrockMetaConfig.Region)
			if err != nil {
				return nil, err
			}
			bedrockMetaConfig.Region = &region
		}

		if bedrockMetaConfig.SessionToken != nil {
			sessionToken, err := s.replaceEnvValue(*bedrockMetaConfig.SessionToken)
			if err != nil {
				return nil, err
			}
			bedrockMetaConfig.SessionToken = &sessionToken
		}

		if bedrockMetaConfig.ARN != nil {
			arn, err := s.replaceEnvValue(*bedrockMetaConfig.ARN)
			if err != nil {
				return nil, err
			}
			bedrockMetaConfig.ARN = &arn
		}

		processedJSON, err := json.Marshal(bedrockMetaConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal processed Bedrock meta config: %w", err)
		}
		return processedJSON, nil

	case schemas.Vertex:
		var vertexMetaConfig meta.VertexMetaConfig
		if err := json.Unmarshal(rawMetaConfig, &vertexMetaConfig); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Vertex meta config: %w", err)
		}

		projectID, err := s.replaceEnvValue(vertexMetaConfig.ProjectID)
		if err != nil {
			return nil, err
		}
		vertexMetaConfig.ProjectID = projectID

		region, err := s.replaceEnvValue(vertexMetaConfig.Region)
		if err != nil {
			return nil, err
		}
		vertexMetaConfig.Region = region

		authCredentials, err := s.replaceEnvValue(vertexMetaConfig.AuthCredentials)
		if err != nil {
			return nil, err
		}
		vertexMetaConfig.AuthCredentials = authCredentials

		processedJSON, err := json.Marshal(vertexMetaConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal processed Vertex meta config: %w", err)
		}
		return processedJSON, nil
	}

	return rawMetaConfig, nil
}

// GetProviderConfig retrieves a fully processed provider configuration from memory.
// This is the primary method called by the account interface and is optimized for minimal latency.
//
// Performance characteristics:
//   - Memory access: ultra-fast direct memory access
//   - No database I/O or JSON parsing overhead
//   - Thread-safe with read locks for concurrent access
//
// Returns a copy of the configuration to prevent external modifications.
func (s *ConfigStore) GetProviderConfig(provider schemas.ModelProvider) (*ProviderConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	config, exists := s.providers[provider]
	if !exists {
		return nil, fmt.Errorf("provider %s not found", provider)
	}

	// Return a copy to prevent external modifications
	configCopy := config
	return &configCopy, nil
}

// GetAllProviders returns all configured providers.
func (s *ConfigStore) GetAllProviders() ([]schemas.ModelProvider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	providers := make([]schemas.ModelProvider, 0, len(s.providers))
	for provider := range s.providers {
		providers = append(providers, provider)
	}

	return providers, nil
}

// UpdateProviderConfig updates a provider configuration in memory with full environment
// variable processing. This method is called when provider configurations are modified
// via the HTTP API and ensures all data processing is done upfront.
//
// The method:
//   - Processes environment variables in API keys and meta configurations
//   - Stores the processed configuration in memory
//   - Updates metadata and timestamps
//   - Thread-safe operation with write locks
func (s *ConfigStore) UpdateProviderConfig(provider schemas.ModelProvider, config ProviderConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Process environment variables in keys
	for i, key := range config.Keys {
		processedValue, err := s.replaceEnvValue(key.Value)
		if err != nil {
			return fmt.Errorf("failed to process env var in key: %w", err)
		}
		config.Keys[i].Value = processedValue
	}

	// Process environment variables in meta config if present
	if config.MetaConfig != nil {
		rawMetaData, err := json.Marshal(*config.MetaConfig)
		if err != nil {
			return fmt.Errorf("failed to marshal meta config: %w", err)
		}

		processedMetaData, err := s.processMetaConfigEnvVars(rawMetaData, provider)
		if err != nil {
			return fmt.Errorf("failed to process env vars in meta config: %w", err)
		}

		metaConfig, err := s.parseMetaConfig(processedMetaData, provider)
		if err != nil {
			return fmt.Errorf("failed to parse processed meta config: %w", err)
		}
		config.MetaConfig = metaConfig
	}

	s.providers[provider] = config

	s.logger.Info(fmt.Sprintf("Updated configuration for provider: %s", provider))
	return nil
}

// AddProvider adds a new provider configuration to memory with full environment variable
// processing. This method is called when new providers are added via the HTTP API.
//
// The method:
//   - Validates that the provider doesn't already exist
//   - Processes environment variables in API keys and meta configurations
//   - Stores the processed configuration in memory
//   - Updates metadata and timestamps
func (s *ConfigStore) AddProvider(provider schemas.ModelProvider, config ProviderConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if provider already exists
	if _, exists := s.providers[provider]; exists {
		return fmt.Errorf("provider %s already exists", provider)
	}

	// Process environment variables in keys
	for i, key := range config.Keys {
		processedValue, err := s.replaceEnvValue(key.Value)
		if err != nil {
			return fmt.Errorf("failed to process env var in key: %w", err)
		}
		config.Keys[i].Value = processedValue
	}

	// Process environment variables in meta config if present
	if config.MetaConfig != nil {
		rawMetaData, err := json.Marshal(*config.MetaConfig)
		if err != nil {
			return fmt.Errorf("failed to marshal meta config: %w", err)
		}

		processedMetaData, err := s.processMetaConfigEnvVars(rawMetaData, provider)
		if err != nil {
			return fmt.Errorf("failed to process env vars in meta config: %w", err)
		}

		metaConfig, err := s.parseMetaConfig(processedMetaData, provider)
		if err != nil {
			return fmt.Errorf("failed to parse processed meta config: %w", err)
		}
		config.MetaConfig = metaConfig
	}

	s.providers[provider] = config

	s.logger.Info(fmt.Sprintf("Added provider: %s", provider))
	return nil
}

// RemoveProvider removes a provider configuration from memory.
func (s *ConfigStore) RemoveProvider(provider schemas.ModelProvider) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.providers[provider]; !exists {
		return fmt.Errorf("provider %s not found", provider)
	}

	delete(s.providers, provider)

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
func (s *ConfigStore) processMCPEnvVars() {
	// Process each client config
	for i, clientConfig := range s.mcpConfig.ClientConfigs {
		// Process ConnectionString if present
		if clientConfig.ConnectionString != nil {
			newValue, err := s.replaceEnvValue(*clientConfig.ConnectionString)
			if err != nil {
				s.logger.Warn(fmt.Sprintf("failed to process env vars in MCP client %s: %v", clientConfig.Name, err))
				continue
			}
			s.mcpConfig.ClientConfigs[i].ConnectionString = &newValue
		}
	}
}

// GetMCPConfig retrieves the processed MCP configuration from memory.
// Returns nil if no MCP configuration was loaded.
// The returned configuration has all environment variables already processed.
func (s *ConfigStore) GetMCPConfig() *schemas.MCPConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.mcpConfig
}
