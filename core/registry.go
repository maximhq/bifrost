// Package bifrost provides the core implementation of the Bifrost system.
package bifrost

import (
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
)

// ProviderConstructor is the function signature for creating providers.
type ProviderConstructor func(config *schemas.ProviderConfig, logger schemas.Logger) (schemas.Provider, error)

// ProviderRegistry holds registered provider constructors for a Bifrost instance.
type ProviderRegistry struct {
	mu           sync.RWMutex
	constructors map[schemas.ModelProvider]ProviderConstructor
}

// NewProviderRegistry creates a new empty provider registry.
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{
		constructors: make(map[schemas.ModelProvider]ProviderConstructor),
	}
}

// Register adds a provider constructor to the registry.
func (r *ProviderRegistry) Register(provider schemas.ModelProvider, constructor ProviderConstructor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.constructors[provider] = constructor
}

// Get retrieves a provider constructor from the registry.
func (r *ProviderRegistry) Get(provider schemas.ModelProvider) ProviderConstructor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.constructors[provider]
}

// IsRegistered checks if a provider is registered.
func (r *ProviderRegistry) IsRegistered(provider schemas.ModelProvider) bool {
	return r.Get(provider) != nil
}

// RegisteredProviders returns all registered provider keys.
func (r *ProviderRegistry) RegisteredProviders() []schemas.ModelProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	providers := make([]schemas.ModelProvider, 0, len(r.constructors))
	for key := range r.constructors {
		providers = append(providers, key)
	}
	return providers
}
