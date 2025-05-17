// Package lib provides core functionality for the Bifrost HTTP service,
// including context propagation, header management, and integration with monitoring systems.
package lib

import (
	"encoding/json"
	"log"
	"os"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/meta"
)

// ProviderConfig represents the configuration for a specific AI model provider.
// It includes API keys, network settings, provider-specific metadata, and concurrency settings.
type ProviderConfig struct {
	Keys                     []schemas.Key                     `json:"keys"`                                  // API keys for the provider
	NetworkConfig            *schemas.NetworkConfig            `json:"network_config,omitempty"`              // Network-related settings
	MetaConfig               *schemas.MetaConfig               `json:"-"`                                     // Provider-specific metadata
	ConcurrencyAndBufferSize *schemas.ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size,omitempty"` // Concurrency settings
}

// ConfigMap maps provider names to their configurations.
type ConfigMap map[schemas.ModelProvider]ProviderConfig

// readConfig reads and parses the configuration file.
// It handles case conversion for provider names and sets up provider-specific metadata.
// Returns a ConfigMap containing all provider configurations.
// Panics if the config file cannot be read or parsed.
//
// In the config file, use placeholder keys (e.g., env.OPENAI_API_KEY) instead of hardcoding actual values.
// These placeholders will be replaced with the corresponding values from the environment variables.
// Example:
//
//	"keys":[{
//		 "value": "env.OPENAI_API_KEY"
//	     "models": ["gpt-4o-mini", "gpt-4-turbo"],
//	     "weight": 1.0
//	}]
//
// In this example, OPENAI_API_KEY refers to a key in the environment variables. At runtime, its value will be used to replace the placeholder.
// Same setup applies to keys in meta configs of all the providers.
// Example:
//
//	"meta_config": {
//		"secret_access_key": "env.BEDROCK_ACCESS_KEY"
//		"region": "env.BEDROCK_REGION"
//	}
//
// In this example, BEDROCK_ACCESS_KEY and BEDROCK_REGION refer to keys in environment variables.
func ReadConfig(configLocation string) ConfigMap {
	data, err := os.ReadFile(configLocation)
	if err != nil {
		log.Fatalf("failed to read config JSON file: %v", err)
	}

	// First unmarshal into a map with string keys to handle case conversion
	var rawConfig map[string]ProviderConfig
	if err := json.Unmarshal(data, &rawConfig); err != nil {
		log.Fatalf("failed to unmarshal JSON: %v", err)
	}

	if rawConfig == nil {
		log.Fatalf("provided config is nil")
	}

	// Create a new config map with lowercase provider names
	config := make(ConfigMap)
	for rawProvider, cfg := range rawConfig {
		provider := schemas.ModelProvider(strings.ToLower(rawProvider))

		switch provider {
		case schemas.Azure:
			var azureMetaConfig meta.AzureMetaConfig
			if err := json.Unmarshal(data, &struct {
				Azure struct {
					MetaConfig *meta.AzureMetaConfig `json:"meta_config"`
				} `json:"Azure"`
			}{Azure: struct {
				MetaConfig *meta.AzureMetaConfig `json:"meta_config"`
			}{&azureMetaConfig}}); err != nil {
				log.Printf("warning: failed to unmarshal Azure meta config: %v", err)
			}
			var metaConfig schemas.MetaConfig = &azureMetaConfig
			cfg.MetaConfig = &metaConfig
		case schemas.Bedrock:
			var bedrockMetaConfig meta.BedrockMetaConfig
			if err := json.Unmarshal(data, &struct {
				Bedrock struct {
					MetaConfig *meta.BedrockMetaConfig `json:"meta_config"`
				} `json:"Bedrock"`
			}{Bedrock: struct {
				MetaConfig *meta.BedrockMetaConfig `json:"meta_config"`
			}{&bedrockMetaConfig}}); err != nil {
				log.Printf("warning: failed to unmarshal Bedrock meta config: %v", err)
			}
			var metaConfig schemas.MetaConfig = &bedrockMetaConfig
			cfg.MetaConfig = &metaConfig
		case schemas.Vertex:
			var vertexMetaConfig meta.VertexMetaConfig
			if err := json.Unmarshal(data, &struct {
				Vertex struct {
					MetaConfig *meta.VertexMetaConfig `json:"meta_config"`
				} `json:"Vertex"`
			}{Vertex: struct {
				MetaConfig *meta.VertexMetaConfig `json:"meta_config"`
			}{&vertexMetaConfig}}); err != nil {
				log.Printf("warning: failed to unmarshal Vertex meta config: %v", err)
			}
			var metaConfig schemas.MetaConfig = &vertexMetaConfig
			cfg.MetaConfig = &metaConfig
		}

		config[provider] = cfg
	}

	return config
}
