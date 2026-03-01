package bifrost

import (
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// mockProvider implements schemas.Provider for testing
type mockProvider struct {
	key schemas.ModelProvider
}

func (m *mockProvider) GetProviderKey() schemas.ModelProvider { return m.key }

// Implement remaining Provider interface methods as no-ops
func (m *mockProvider) ListModels(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) TextCompletion(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) TextCompletionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) ChatCompletion(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) ChatCompletionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) Responses(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) ResponsesStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) Embedding(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) Speech(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) SpeechStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) Transcription(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) TranscriptionStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) ImageGeneration(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) ImageGenerationStream(_ *schemas.BifrostContext, _ schemas.PostHookRunner, _ schemas.Key, _ *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) FileUpload(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) FileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) FileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) FileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) FileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, nil
}
func (m *mockProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, nil
}

func TestProviderRegistry_NewProviderRegistry(t *testing.T) {
	registry := NewProviderRegistry()
	if registry == nil {
		t.Fatal("NewProviderRegistry returned nil")
	}
	if registry.constructors == nil {
		t.Error("constructors map was not initialized")
	}
	if len(registry.constructors) != 0 {
		t.Errorf("expected empty constructors map, got %d entries", len(registry.constructors))
	}
}

func TestProviderRegistry_Register(t *testing.T) {
	registry := NewProviderRegistry()
	providerKey := schemas.ModelProvider("custom-provider")

	constructor := func(config *schemas.ProviderConfig, logger schemas.Logger) (schemas.Provider, error) {
		return &mockProvider{key: providerKey}, nil
	}

	registry.Register(providerKey, constructor)

	if !registry.IsRegistered(providerKey) {
		t.Errorf("provider %s should be registered", providerKey)
	}
}

func TestProviderRegistry_Get(t *testing.T) {
	registry := NewProviderRegistry()
	providerKey := schemas.ModelProvider("custom-provider")

	// Test getting non-existent provider
	if got := registry.Get(providerKey); got != nil {
		t.Errorf("expected nil for non-existent provider, got %v", got)
	}

	// Register and test retrieval
	constructor := func(config *schemas.ProviderConfig, logger schemas.Logger) (schemas.Provider, error) {
		return &mockProvider{key: providerKey}, nil
	}
	registry.Register(providerKey, constructor)

	got := registry.Get(providerKey)
	if got == nil {
		t.Fatal("expected constructor, got nil")
	}

	// Verify the constructor works
	provider, err := got(nil, nil)
	if err != nil {
		t.Errorf("unexpected error from constructor: %v", err)
	}
	if provider.GetProviderKey() != providerKey {
		t.Errorf("expected provider key %s, got %s", providerKey, provider.GetProviderKey())
	}
}

func TestProviderRegistry_IsRegistered(t *testing.T) {
	registry := NewProviderRegistry()
	providerKey := schemas.ModelProvider("custom-provider")

	if registry.IsRegistered(providerKey) {
		t.Error("provider should not be registered initially")
	}

	registry.Register(providerKey, func(config *schemas.ProviderConfig, logger schemas.Logger) (schemas.Provider, error) {
		return &mockProvider{key: providerKey}, nil
	})

	if !registry.IsRegistered(providerKey) {
		t.Error("provider should be registered after Register call")
	}
}

func TestProviderRegistry_RegisteredProviders(t *testing.T) {
	registry := NewProviderRegistry()

	// Test empty registry
	providers := registry.RegisteredProviders()
	if len(providers) != 0 {
		t.Errorf("expected 0 providers, got %d", len(providers))
	}

	// Register multiple providers
	keys := []schemas.ModelProvider{"provider-1", "provider-2", "provider-3"}
	for _, key := range keys {
		registry.Register(key, func(config *schemas.ProviderConfig, logger schemas.Logger) (schemas.Provider, error) {
			return &mockProvider{key: key}, nil
		})
	}

	providers = registry.RegisteredProviders()
	if len(providers) != len(keys) {
		t.Errorf("expected %d providers, got %d", len(keys), len(providers))
	}

	// Verify all keys are present (order may vary)
	keyMap := make(map[schemas.ModelProvider]bool)
	for _, p := range providers {
		keyMap[p] = true
	}
	for _, key := range keys {
		if !keyMap[key] {
			t.Errorf("expected provider %s to be in list", key)
		}
	}
}

func TestProviderRegistry_ConcurrentAccess(t *testing.T) {
	registry := NewProviderRegistry()
	const numGoroutines = 100
	const numOperations = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines * 3) // 3 types of operations

	// Concurrent writes
	for i := range numGoroutines {
		go func() {
			defer wg.Done()
			for range numOperations {
				key := schemas.ModelProvider("provider-" + string(rune('a'+i%26)))
				registry.Register(key, func(config *schemas.ProviderConfig, logger schemas.Logger) (schemas.Provider, error) {
					return &mockProvider{key: key}, nil
				})
			}
		}()
	}

	// Concurrent reads (Get)
	for i := range numGoroutines {
		go func() {
			defer wg.Done()
			for range numOperations {
				key := schemas.ModelProvider("provider-" + string(rune('a'+i%26)))
				_ = registry.Get(key)
			}
		}()
	}

	// Concurrent reads (IsRegistered)
	for i := range numGoroutines {
		go func() {
			defer wg.Done()
			for range numOperations {
				key := schemas.ModelProvider("provider-" + string(rune('a'+i%26)))
				_ = registry.IsRegistered(key)
			}
		}()
	}

	wg.Wait()
}

func TestBifrost_ProviderRegistry(t *testing.T) {
	// Create a minimal Bifrost instance without full initialization
	bifrost := &Bifrost{}

	// Get registry - should be lazily initialized via sync.Once
	registry := bifrost.ProviderRegistry()
	if registry == nil {
		t.Fatal("ProviderRegistry() returned nil")
	}

	// Test subsequent calls return the same registry
	registry2 := bifrost.ProviderRegistry()
	if registry != registry2 {
		t.Error("ProviderRegistry() should return the same instance")
	}
}

func TestBifrost_CreateProvider_FallbackToRegistry(t *testing.T) {
	// Create a minimal Bifrost instance
	bifrost := &Bifrost{
		logger: NewDefaultLogger(schemas.LogLevelError),
	}

	customProviderKey := schemas.ModelProvider("my-custom-provider")
	config := &schemas.ProviderConfig{}

	// Without registry, should fail for unknown provider
	_, err := bifrost.createProvider(customProviderKey, config)
	if err == nil {
		t.Error("expected error for unknown provider without registry")
	}

	// Register custom provider
	bifrost.ProviderRegistry().Register(customProviderKey, func(config *schemas.ProviderConfig, logger schemas.Logger) (schemas.Provider, error) {
		return &mockProvider{key: customProviderKey}, nil
	})

	// Now should succeed via registry fallback
	provider, err := bifrost.createProvider(customProviderKey, config)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if provider == nil {
		t.Fatal("expected provider, got nil")
	}
	if provider.GetProviderKey() != customProviderKey {
		t.Errorf("expected provider key %s, got %s", customProviderKey, provider.GetProviderKey())
	}
}

func TestBifrost_CreateProvider_BuiltInFirst(t *testing.T) {
	// Create a minimal Bifrost instance
	bifrost := &Bifrost{
		logger: NewDefaultLogger(schemas.LogLevelError),
	}

	// Register a custom constructor for a built-in provider key
	customCalled := false
	bifrost.ProviderRegistry().Register(schemas.OpenAI, func(config *schemas.ProviderConfig, logger schemas.Logger) (schemas.Provider, error) {
		customCalled = true
		return &mockProvider{key: schemas.OpenAI}, nil
	})

	config := &schemas.ProviderConfig{}

	// Should use built-in provider, not the registry
	provider, err := bifrost.createProvider(schemas.OpenAI, config)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if provider == nil {
		t.Fatal("expected provider, got nil")
	}
	if customCalled {
		t.Error("registry constructor should not have been called for built-in provider")
	}
}
