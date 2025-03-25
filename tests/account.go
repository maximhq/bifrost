package tests

import (
	"bifrost/interfaces"
	"fmt"
	"os"
)

// BaseAccount provides a basic implementation of the Account interface for Anthropic and OpenAI providers
type BaseAccount struct{}

// GetInitiallyConfiguredProviderKeys returns all provider keys
func (baseAccount *BaseAccount) GetInitiallyConfiguredProviderKeys() ([]interfaces.SupportedModelProvider, error) {
	return []interfaces.SupportedModelProvider{interfaces.OpenAI, interfaces.Anthropic, interfaces.Bedrock}, nil
}

// GetKeysForProvider returns all keys associated with a provider
func (baseAccount *BaseAccount) GetKeysForProvider(provider interfaces.Provider) ([]interfaces.Key, error) {
	switch provider.GetProviderKey() {
	case interfaces.OpenAI:
		return []interfaces.Key{
			{
				Value:  os.Getenv("OPEN_AI_API_KEY"),
				Models: []string{"gpt-4o-mini"},
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
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider.GetProviderKey())
	}
}

// GetConcurrencyAndBufferSizeForProvider returns the concurrency and buffer size settings for a provider
func (baseAccount *BaseAccount) GetConcurrencyAndBufferSizeForProvider(provider interfaces.Provider) (*interfaces.ConcurrencyAndBufferSize, error) {
	switch provider.GetProviderKey() {
	case interfaces.OpenAI:
		return &interfaces.ConcurrencyAndBufferSize{
			Concurrency: 3,
			BufferSize:  10,
		}, nil
	case interfaces.Anthropic:
		return &interfaces.ConcurrencyAndBufferSize{
			Concurrency: 3,
			BufferSize:  10,
		}, nil
	case interfaces.Bedrock:
		return &interfaces.ConcurrencyAndBufferSize{
			Concurrency: 3,
			BufferSize:  10,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider.GetProviderKey())
	}
}
