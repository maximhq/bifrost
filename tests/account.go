package tests

import (
	"fmt"
	"os"

	"github.com/maximhq/bifrost/interfaces"

	"github.com/maximhq/maxim-go"
)

// BaseAccount provides a basic implementation of the Account interface for Anthropic and OpenAI providers
type BaseAccount struct{}

// GetInitiallyConfiguredProviderKeys returns all provider keys
func (baseAccount *BaseAccount) GetInitiallyConfiguredProviders() ([]interfaces.SupportedModelProvider, error) {
	return []interfaces.SupportedModelProvider{interfaces.OpenAI, interfaces.Anthropic, interfaces.Bedrock}, nil
}

// GetKeysForProvider returns all keys associated with a provider
func (baseAccount *BaseAccount) GetKeysForProvider(providerKey interfaces.SupportedModelProvider) ([]interfaces.Key, error) {
	switch providerKey {
	case interfaces.OpenAI:
		return []interfaces.Key{
			{
				Value:  os.Getenv("OPEN_AI_API_KEY"),
				Models: []string{"gpt-4o-mini", "gpt-4-turbo"},
				Weight: 1.0,
			},
		}, nil
	case interfaces.Anthropic:
		return []interfaces.Key{
			{
				Value:  os.Getenv("ANTHROPIC_API_KEY"),
				Models: []string{"claude-3-7-sonnet-20250219", "claude-2.1"},
				Weight: 1.0,
			},
		}, nil
	case interfaces.Bedrock:
		return []interfaces.Key{
			{
				Value:  os.Getenv("BEDROCK_API_KEY"),
				Models: []string{"anthropic.claude-v2:1", "mistral.mixtral-8x7b-instruct-v0:1", "mistral.mistral-large-2402-v1:0", "anthropic.claude-3-sonnet-20240229-v1:0"},
				Weight: 1.0,
			},
		}, nil
	case interfaces.Cohere:
		return []interfaces.Key{
			{
				Value:  os.Getenv("COHERE_API_KEY"),
				Models: []string{"command-a-03-2025"},
				Weight: 1.0,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerKey)
	}
}

// GetConcurrencyAndBufferSizeForProvider returns the concurrency and buffer size settings for a provider
func (baseAccount *BaseAccount) GetConfigForProvider(providerKey interfaces.SupportedModelProvider) (*interfaces.ProviderConfig, error) {
	switch providerKey {
	case interfaces.OpenAI:
		return &interfaces.ProviderConfig{
			NetworkConfig: interfaces.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
			},
			ConcurrencyAndBufferSize: interfaces.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	case interfaces.Anthropic:
		return &interfaces.ProviderConfig{
			NetworkConfig: interfaces.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
			},
			ConcurrencyAndBufferSize: interfaces.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	case interfaces.Bedrock:
		return &interfaces.ProviderConfig{
			NetworkConfig: interfaces.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
			},
			MetaConfig: &interfaces.MetaConfig{
				SecretAccessKey: maxim.StrPtr(os.Getenv("BEDROCK_ACCESS_KEY")),
				Region:          maxim.StrPtr("us-east-1"),
			},
			ConcurrencyAndBufferSize: interfaces.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	case interfaces.Cohere:
		return &interfaces.ProviderConfig{
			NetworkConfig: interfaces.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
			},
			ConcurrencyAndBufferSize: interfaces.ConcurrencyAndBufferSize{
				Concurrency: 3,
				BufferSize:  10,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerKey)
	}
}
