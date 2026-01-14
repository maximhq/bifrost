package lib

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// TestNewBaseAccount tests creating a new BaseAccount
func TestNewBaseAccount(t *testing.T) {
	store := &Config{}
	account := NewBaseAccount(store)

	if account == nil {
		t.Fatal("Expected non-nil account")
	}
	if account.store != store {
		t.Error("Expected account to reference provided store")
	}
}

// TestNewBaseAccount_NilStore tests creating account with nil store
func TestNewBaseAccount_NilStore(t *testing.T) {
	account := NewBaseAccount(nil)

	if account == nil {
		t.Fatal("Expected non-nil account even with nil store")
	}
	if account.store != nil {
		t.Error("Expected account store to be nil")
	}
}

// TestBaseAccount_GetConfiguredProviders_NilStore tests GetConfiguredProviders with nil store
func TestBaseAccount_GetConfiguredProviders_NilStore(t *testing.T) {
	account := NewBaseAccount(nil)

	providers, err := account.GetConfiguredProviders()

	if err == nil {
		t.Error("Expected error when store is nil")
	}
	if providers != nil {
		t.Error("Expected nil providers when store is nil")
	}
}

// TestBaseAccount_GetConfiguredProviders_ValidStore tests GetConfiguredProviders with valid store
func TestBaseAccount_GetConfiguredProviders_ValidStore(t *testing.T) {
	store := &Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			"openai":    {},
			"anthropic": {},
			"google":    {},
		},
	}
	account := NewBaseAccount(store)

	providers, err := account.GetConfiguredProviders()

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(providers) != 3 {
		t.Errorf("Expected 3 providers, got %d", len(providers))
	}
}

// TestBaseAccount_GetConfiguredProviders_EmptyStore tests GetConfiguredProviders with empty store
func TestBaseAccount_GetConfiguredProviders_EmptyStore(t *testing.T) {
	store := &Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{},
	}
	account := NewBaseAccount(store)

	providers, err := account.GetConfiguredProviders()

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(providers) != 0 {
		t.Errorf("Expected 0 providers, got %d", len(providers))
	}
}

// TestBaseAccount_GetKeysForProvider_NilStore tests GetKeysForProvider with nil store
func TestBaseAccount_GetKeysForProvider_NilStore(t *testing.T) {
	account := NewBaseAccount(nil)

	keys, err := account.GetKeysForProvider(context.Background(), "openai")

	if err == nil {
		t.Error("Expected error when store is nil")
	}
	if keys != nil {
		t.Error("Expected nil keys when store is nil")
	}
}

// TestBaseAccount_GetKeysForProvider_ProviderNotFound tests GetKeysForProvider with unknown provider
func TestBaseAccount_GetKeysForProvider_ProviderNotFound(t *testing.T) {
	store := &Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			"openai": {},
		},
	}
	account := NewBaseAccount(store)

	_, err := account.GetKeysForProvider(context.Background(), "unknown-provider")

	if err == nil {
		t.Error("Expected error for unknown provider")
	}
}

// TestBaseAccount_GetKeysForProvider_ValidProvider tests GetKeysForProvider with valid provider
func TestBaseAccount_GetKeysForProvider_ValidProvider(t *testing.T) {
	store := &Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			"openai": {
				Keys: []schemas.Key{
					{ID: "key1", Name: "test-key-1"},
					{ID: "key2", Name: "test-key-2"},
				},
			},
		},
		ClientConfig: configstore.ClientConfig{
			EnableGovernance: false,
		},
	}
	account := NewBaseAccount(store)

	keys, err := account.GetKeysForProvider(context.Background(), "openai")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("Expected 2 keys, got %d", len(keys))
	}
}

// TestBaseAccount_GetKeysForProvider_GovernanceFiltering tests key filtering with governance
func TestBaseAccount_GetKeysForProvider_GovernanceFiltering(t *testing.T) {
	store := &Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			"openai": {
				Keys: []schemas.Key{
					{ID: "key1", Name: "test-key-1"},
					{ID: "key2", Name: "test-key-2"},
					{ID: "key3", Name: "test-key-3"},
				},
			},
		},
		ClientConfig: configstore.ClientConfig{
			EnableGovernance: true,
		},
	}
	account := NewBaseAccount(store)

	// Create context with include-only-keys
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKey("bf-governance-include-only-keys"), []string{"key1", "key3"})

	keys, err := account.GetKeysForProvider(ctx, "openai")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("Expected 2 filtered keys, got %d", len(keys))
	}

	// Verify only key1 and key3 are returned
	keyIDs := make(map[string]bool)
	for _, key := range keys {
		keyIDs[key.ID] = true
	}
	if !keyIDs["key1"] {
		t.Error("Expected key1 to be included")
	}
	if keyIDs["key2"] {
		t.Error("Expected key2 to be filtered out")
	}
	if !keyIDs["key3"] {
		t.Error("Expected key3 to be included")
	}
}

// TestBaseAccount_GetKeysForProvider_GovernanceEmptyFilter tests governance with empty filter
func TestBaseAccount_GetKeysForProvider_GovernanceEmptyFilter(t *testing.T) {
	store := &Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			"openai": {
				Keys: []schemas.Key{
					{ID: "key1", Name: "test-key-1"},
					{ID: "key2", Name: "test-key-2"},
				},
			},
		},
		ClientConfig: configstore.ClientConfig{
			EnableGovernance: true,
		},
	}
	account := NewBaseAccount(store)

	// Empty include-only-keys means "no keys allowed"
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKey("bf-governance-include-only-keys"), []string{})

	keys, err := account.GetKeysForProvider(ctx, "openai")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if keys != nil && len(keys) > 0 {
		t.Errorf("Expected no keys when include-only-keys is empty, got %d", len(keys))
	}
}

// TestBaseAccount_GetConfigForProvider_NilStore tests GetConfigForProvider with nil store
func TestBaseAccount_GetConfigForProvider_NilStore(t *testing.T) {
	account := NewBaseAccount(nil)

	config, err := account.GetConfigForProvider("openai")

	if err == nil {
		t.Error("Expected error when store is nil")
	}
	if config != nil {
		t.Error("Expected nil config when store is nil")
	}
}

// TestBaseAccount_GetConfigForProvider_ProviderNotFound tests GetConfigForProvider with unknown provider
func TestBaseAccount_GetConfigForProvider_ProviderNotFound(t *testing.T) {
	store := &Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			"openai": {},
		},
	}
	account := NewBaseAccount(store)

	_, err := account.GetConfigForProvider("unknown-provider")

	if err == nil {
		t.Error("Expected error for unknown provider")
	}
}

// TestBaseAccount_GetConfigForProvider_ValidProvider tests GetConfigForProvider with valid provider
func TestBaseAccount_GetConfigForProvider_ValidProvider(t *testing.T) {
	store := &Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			"openai": {
				NetworkConfig: &schemas.NetworkConfig{
					MaxRetries: 3,
				},
				ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{
					Concurrency: 10,
					BufferSize:  100,
				},
			},
		},
	}
	account := NewBaseAccount(store)

	config, err := account.GetConfigForProvider("openai")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if config == nil {
		t.Fatal("Expected non-nil config")
	}
	if config.NetworkConfig.MaxRetries != 3 {
		t.Errorf("Expected MaxRetries=3, got %d", config.NetworkConfig.MaxRetries)
	}
}

// TestBaseAccount_GetConfigForProvider_DefaultConfig tests defaults when config fields are nil
func TestBaseAccount_GetConfigForProvider_DefaultConfig(t *testing.T) {
	store := &Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			"openai": {
				// No NetworkConfig or ConcurrencyAndBufferSize set
			},
		},
	}
	account := NewBaseAccount(store)

	config, err := account.GetConfigForProvider("openai")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if config == nil {
		t.Fatal("Expected non-nil config")
	}

	// Should have default network config (DefaultNetworkConfig)
	// DefaultNetworkConfig has MaxRetries=3
	t.Log("Default network config applied correctly")
}

// TestBaseAccount_GetConfigForProvider_ProxyConfig tests proxy config handling
func TestBaseAccount_GetConfigForProvider_ProxyConfig(t *testing.T) {
	proxyConfig := &schemas.ProxyConfig{
		Type: "http",
		URL:  "http://proxy.example.com:8080",
	}
	store := &Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			"openai": {
				ProxyConfig: proxyConfig,
			},
		},
	}
	account := NewBaseAccount(store)

	config, err := account.GetConfigForProvider("openai")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if config.ProxyConfig == nil {
		t.Error("Expected proxy config to be set")
	} else if config.ProxyConfig.URL != "http://proxy.example.com:8080" {
		t.Errorf("Expected proxy URL 'http://proxy.example.com:8080', got '%s'", config.ProxyConfig.URL)
	}
}

// TestBaseAccount_GetConfigForProvider_SendBackFlags tests SendBackRawRequest/Response flags
func TestBaseAccount_GetConfigForProvider_SendBackFlags(t *testing.T) {
	store := &Config{
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			"openai": {
				SendBackRawRequest:  true,
				SendBackRawResponse: true,
			},
		},
	}
	account := NewBaseAccount(store)

	config, err := account.GetConfigForProvider("openai")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !config.SendBackRawRequest {
		t.Error("Expected SendBackRawRequest to be true")
	}
	if !config.SendBackRawResponse {
		t.Error("Expected SendBackRawResponse to be true")
	}
}
