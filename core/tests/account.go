// Package tests provides test utilities and configurations for the Bifrost system.
// It includes test implementations of schemas, mock objects, and helper functions
// for testing the Bifrost functionality with various AI providers.
package tests

import (
	"fmt"
	"os"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/meta"
)

// BaseAccount provides a test implementation of the Account interface.
// It implements basic account functionality for testing purposes, supporting
// multiple AI providers including OpenAI, Anthropic, Bedrock, Cohere, and Azure.
// The implementation uses environment variables from the .env file for API keys and provides
// default configurations suitable for testing.
type BaseAccount struct{}

// GetConfiguredProviders returns the list of initially supported providers.
// This implementation returns OpenAI, Anthropic, and Bedrock as the default providers.
//
// Returns:
//   - []schemas.SupportedModelProvider: A slice containing the supported provider identifiers
//   - error: Always returns nil as this implementation doesn't produce errors
func (baseAccount *BaseAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic, schemas.Bedrock, schemas.Cohere, schemas.Azure, schemas.Vertex}, nil
}

// GetKeysForProvider returns the API keys and associated models for a given provider.
// It retrieves API keys from environment variables and maps them to their supported models.
// Each key includes a weight value for load balancing purposes.
//
// Parameters:
//   - providerKey: The identifier of the provider to get keys for
//
// Returns:
//   - []schemas.Key: A slice of Key objects containing API keys and their configurations
//   - error: An error if the provider is not supported
//
// Environment Variables Used:
//   - OPENAI_API_KEY: API key for OpenAI
//   - ANTHROPIC_API_KEY: API key for Anthropic
//   - BEDROCK_API_KEY: API key for AWS Bedrock
//   - COHERE_API_KEY: API key for Cohere
//   - AZURE_API_KEY: API key for Azure OpenAI
func (baseAccount *BaseAccount) GetKeysForProvider(providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	switch providerKey {
	case schemas.OpenAI:
		return []schemas.Key{
			{
				Value:  os.Getenv("OPENAI_API_KEY"),
				Models: []string{"gpt-4o-mini", "gpt-4-turbo"},
				Weight: 1.0,
			},
		}, nil
	case schemas.Anthropic:
		return []schemas.Key{
			{
				Value:  os.Getenv("ANTHROPIC_API_KEY"),
				Models: []string{"claude-3-7-sonnet-20250219", "claude-3-5-sonnet-20240620", "claude-2.1"},
				Weight: 1.0,
			},
		}, nil
	case schemas.Bedrock:
		return []schemas.Key{
			{
				Value:  os.Getenv("BEDROCK_API_KEY"),
				Models: []string{"anthropic.claude-v2:1", "mistral.mixtral-8x7b-instruct-v0:1", "mistral.mistral-large-2402-v1:0", "anthropic.claude-3-sonnet-20240229-v1:0"},
				Weight: 1.0,
			},
		}, nil
	case schemas.Cohere:
		return []schemas.Key{
			{
				Value:  os.Getenv("COHERE_API_KEY"),
				Models: []string{"command-a-03-2025"},
				Weight: 1.0,
			},
		}, nil
	case schemas.Azure:
		return []schemas.Key{
			{
				Value:  os.Getenv("AZURE_API_KEY"),
				Models: []string{"gpt-4o"},
				Weight: 1.0,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerKey)
	}
}

// GetConfigForProvider returns the configuration settings for a given provider.
// It provides standardized configuration settings for network operations,
// concurrency, and provider-specific metadata.
//
// Parameters:
//   - providerKey: The identifier of the provider to get configuration for
//
// Returns:
//   - *schemas.ProviderConfig: Configuration settings for the provider, including:
//   - Network settings (timeouts, retries, backoff)
//   - Concurrency and buffer size settings
//   - Provider-specific metadata (for Bedrock and Azure)
//   - error: An error if the provider is not supported
//
// Environment Variables Used:
//   - BEDROCK_ACCESS_KEY: AWS access key for Bedrock configuration
//   - AZURE_ENDPOINT: Azure endpoint for Azure OpenAI configuration
//
// Default Settings:
//   - Request Timeout: 30 seconds
//   - Max Retries: 1
//   - Initial Backoff: 100ms
//   - Max Backoff: 2s
//   - Concurrency: 3
//   - Buffer Size: 10
func (baseAccount *BaseAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	switch providerKey {
	case schemas.OpenAI:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	case schemas.Anthropic:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	case schemas.Bedrock:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			MetaConfig: &meta.BedrockMetaConfig{
				SecretAccessKey: os.Getenv("BEDROCK_ACCESS_KEY"),
				Region:          bifrost.Ptr("us-east-1"),
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	case schemas.Cohere:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	case schemas.Azure:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			MetaConfig: &meta.AzureMetaConfig{
				Endpoint: os.Getenv("AZURE_ENDPOINT"),
				Deployments: map[string]string{
					"gpt-4o": "gpt-4o-aug",
				},
				APIVersion: bifrost.Ptr("2024-08-01-preview"),
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	case schemas.Vertex:
		return &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
				MaxRetries:                     1,
				RetryBackoffInitial:            100 * time.Millisecond,
				RetryBackoffMax:                2 * time.Second,
			},
			MetaConfig: &meta.VertexMetaConfig{
				ProjectID:          os.Getenv("VERTEX_PROJECT_ID"),
				AuthCredentialPath: os.Getenv("VERTEX_CREDENTIALS_PATH"),
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerKey)
	}
}
