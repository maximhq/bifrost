// Package lib provides core functionality for the Bifrost HTTP service,
// including context propagation, header management, and integration with monitoring systems.
package lib

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// BaseAccount implements the Account interface for Bifrost.
// It manages provider configurations using a in-memory store for persistent storage.
// All data processing (environment variables, key configs) is done upfront in the store.
type BaseAccount struct {
	store *Config // store for in-memory configuration
}

// NewBaseAccount creates a new BaseAccount with the given store
func NewBaseAccount(store *Config) *BaseAccount {
	return &BaseAccount{
		store: store,
	}
}

// GetConfiguredProviders returns a list of all configured providers.
// Implements the Account interface.
func (baseAccount *BaseAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	if baseAccount.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	return baseAccount.store.GetAllProviders()
}

// GetKeysForProvider returns the API keys configured for a specific provider.
// Keys are already processed (environment variables resolved) by the store.
// Implements the Account interface.
func (baseAccount *BaseAccount) GetKeysForProvider(ctx context.Context, providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	if baseAccount.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	config, err := baseAccount.store.GetProviderConfigRaw(providerKey)
	if err != nil {
		return nil, err
	}
	keys := config.Keys
	if v := ctx.Value(schemas.BifrostContextKeyGovernanceIncludeOnlyKeys); v != nil {
		if includeOnlyKeys, ok := v.([]string); ok {
			if len(includeOnlyKeys) == 0 {
				// header present but empty means "no keys allowed"
				keys = nil
			} else {
				set := make(map[string]struct{}, len(includeOnlyKeys))
				for _, id := range includeOnlyKeys {
					set[id] = struct{}{}
				}
				filtered := make([]schemas.Key, 0, len(keys))
				for _, key := range keys {
					if _, ok := set[key.ID]; ok {
						filtered = append(filtered, key)
					}
				}
				keys = filtered
			}
		}
	}
	return keys, nil
}

// GetConfigForProvider returns the complete configuration for a specific provider.
// Configuration is already fully processed (environment variables, key configs) by the store.
// Implements the Account interface.
func (baseAccount *BaseAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if baseAccount.store == nil {
		return nil, fmt.Errorf("store not initialized")
	}
	config, err := baseAccount.store.GetProviderConfigRaw(providerKey)
	if err != nil {
		return nil, err
	}
	providerConfig := &schemas.ProviderConfig{}
	if config.ProxyConfig != nil {
		providerConfig.ProxyConfig = config.ProxyConfig
	}
	if config.NetworkConfig != nil {
		providerConfig.NetworkConfig = *config.NetworkConfig
	} else {
		providerConfig.NetworkConfig = schemas.DefaultNetworkConfig
	}
	if config.ConcurrencyAndBufferSize != nil {
		providerConfig.ConcurrencyAndBufferSize = *config.ConcurrencyAndBufferSize
	} else {
		providerConfig.ConcurrencyAndBufferSize = schemas.DefaultConcurrencyAndBufferSize
	}
	providerConfig.SendBackRawRequest = config.SendBackRawRequest
	providerConfig.SendBackRawResponse = config.SendBackRawResponse
	if config.CustomProviderConfig != nil {
		providerConfig.CustomProviderConfig = config.CustomProviderConfig
	}
	return providerConfig, nil
}
