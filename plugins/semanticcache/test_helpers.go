package semanticcache

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// Test constants
const (
	TestCacheKey = "x-test-cache-key"
)

// getWeaviateConfigFromEnv retrieves Weaviate configuration from environment variables
func getWeaviateConfigFromEnv() vectorstore.WeaviateConfig {
	scheme := os.Getenv("WEAVIATE_SCHEME")
	if scheme == "" {
		scheme = "http"
	}

	host := os.Getenv("WEAVIATE_HOST")
	if host == "" {
		host = "localhost:9000"
	}

	apiKey := os.Getenv("WEAVIATE_API_KEY")

	timeoutStr := os.Getenv("WEAVIATE_TIMEOUT")
	timeout := 30 // default
	if timeoutStr != "" {
		if t, err := strconv.Atoi(timeoutStr); err == nil {
			timeout = t
		}
	}

	return vectorstore.WeaviateConfig{
		Scheme:    scheme,
		Host:      host,
		ApiKey:    apiKey,
		Timeout:   time.Duration(timeout) * time.Second,
		ClassName: "TestWeaviateSemanticCache",
	}
}

// BaseAccount implements the schemas.Account interface for testing purposes.
type BaseAccount struct{}

func (baseAccount *BaseAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI}, nil
}

func (baseAccount *BaseAccount) GetKeysForProvider(ctx *context.Context, providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{
		{
			Value:  os.Getenv("OPENAI_API_KEY"),
			Models: []string{}, // Empty models array means it supports ALL models
			Weight: 1.0,
		},
	}, nil
}

func (baseAccount *BaseAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	return &schemas.ProviderConfig{
		NetworkConfig:            schemas.DefaultNetworkConfig,
		ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
	}, nil
}

// TestSetup contains common test setup components
type TestSetup struct {
	Logger schemas.Logger
	Store  vectorstore.VectorStore
	Plugin schemas.Plugin
	Client *bifrost.Bifrost
	Config Config
}

// NewTestSetup creates a new test setup with default configuration
func NewTestSetup(t *testing.T) *TestSetup {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY is not set, skipping test")
	}

	return NewTestSetupWithConfig(t, Config{
		CacheKey:       TestCacheKey,
		Provider:       schemas.OpenAI,
		EmbeddingModel: "text-embedding-3-small",
		Threshold:      0.8,
		Keys: []schemas.Key{
			{
				Value:  os.Getenv("OPENAI_API_KEY"),
				Models: []string{},
				Weight: 1.0,
			},
		},
	})
}

// NewTestSetupWithConfig creates a new test setup with custom configuration
func NewTestSetupWithConfig(t *testing.T, config Config) *TestSetup {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)

	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Type:    vectorstore.VectorStoreTypeWeaviate,
		Config:  getWeaviateConfigFromEnv(),
		Enabled: true,
	}, logger)
	if err != nil {
		t.Fatalf("Vector store not available or failed to connect: %v", err)
	}

	plugin, err := Init(context.Background(), config, logger, store)
	if err != nil {
		t.Fatalf("Failed to initialize plugin: %v", err)
	}

	// Clear test keys
	pluginImpl := plugin.(*Plugin)
	clearTestKeysWithStore(t, pluginImpl.store)

	account := &BaseAccount{}
	client, err := bifrost.Init(schemas.BifrostConfig{
		Account: account,
		Plugins: []schemas.Plugin{plugin},
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("Error initializing Bifrost: %v", err)
	}

	return &TestSetup{
		Logger: logger,
		Store:  store,
		Plugin: plugin,
		Client: client,
		Config: config,
	}
}

// NewRedisClusterTestSetup creates a test setup for Redis Cluster testing
func NewRedisClusterTestSetup(t *testing.T) *TestSetup {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY is not set, skipping Redis Cluster test")
	}

	config := Config{
		CacheKey:       TestCacheKey,
		Provider:       schemas.OpenAI,
		EmbeddingModel: "text-embedding-3-small",
		Threshold:      0.8,
		Keys: []schemas.Key{
			{
				Value:  os.Getenv("OPENAI_API_KEY"),
				Models: []string{},
				Weight: 1.0,
			},
		},
	}

	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)

	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Type:   vectorstore.VectorStoreTypeWeaviate,
		Config: getWeaviateConfigFromEnv(),
	}, logger)
	if err != nil {
		t.Fatalf("Vector store not available or failed to connect: %v", err)
	}

	plugin, err := Init(context.Background(), config, logger, store)
	if err != nil {
		t.Fatalf("Failed to initialize plugin with vector store: %v", err)
	}

	// Clear test keys
	pluginImpl := plugin.(*Plugin)
	clearTestKeysWithStore(t, pluginImpl.store)

	account := &BaseAccount{}
	client, err := bifrost.Init(schemas.BifrostConfig{
		Account: account,
		Plugins: []schemas.Plugin{plugin},
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("Error initializing Bifrost with Redis Cluster: %v", err)
	}

	return &TestSetup{
		Logger: logger,
		Store:  store,
		Plugin: plugin,
		Client: client,
		Config: config,
	}
}

// Cleanup cleans up test resources
func (ts *TestSetup) Cleanup() {
	if ts.Client != nil {
		ts.Client.Cleanup()
	}
	if ts.Store != nil {
		ts.Store.Close(context.Background())
	}
}

// clearTestKeysWithStore removes all keys matching the test prefix using the store interface
func clearTestKeysWithStore(t *testing.T, store vectorstore.VectorStore) {
	// With the new unified VectorStore interface, cleanup is typically handled
	// by the vector store implementation (e.g., dropping entire classes)
	t.Logf("Test cleanup delegated to vector store implementation")
}

// CreateBasicChatRequest creates a basic chat completion request for testing
func CreateBasicChatRequest(content string, temperature float64, maxTokens int) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: schemas.RequestInput{
			ChatCompletionInput: &[]schemas.BifrostMessage{
				{
					Role: "user",
					Content: schemas.MessageContent{
						ContentStr: &content,
					},
				},
			},
		},
		Params: &schemas.ModelParameters{
			Temperature: &temperature,
			MaxTokens:   &maxTokens,
		},
	}
}

// CreateStreamingChatRequest creates a streaming chat completion request for testing
func CreateStreamingChatRequest(content string, temperature float64, maxTokens int) *schemas.BifrostRequest {
	return CreateBasicChatRequest(content, temperature, maxTokens)
}

// CreateSpeechRequest creates a speech synthesis request for testing
func CreateSpeechRequest(input string, voice string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		Provider: schemas.OpenAI,
		Model:    "tts-1",
		Input: schemas.RequestInput{
			SpeechInput: &schemas.SpeechInput{
				Input: input,
				VoiceConfig: schemas.SpeechVoiceInput{
					Voice: &voice,
				},
			},
		},
	}
}

// AssertCacheHit verifies that a response was served from cache
func AssertCacheHit(t *testing.T, response *schemas.BifrostResponse, expectedCacheType string) {
	if response.ExtraFields.RawResponse == nil {
		t.Error("Response ExtraFields.RawResponse is nil - expected cache metadata")
		return
	}

	rawMap, ok := response.ExtraFields.RawResponse.(map[string]interface{})
	if !ok {
		t.Error("Response ExtraFields.RawResponse is not a map - expected cache metadata")
		return
	}

	cachedFlag, exists := rawMap["bifrost_cached"]
	if !exists {
		t.Error("Response missing 'bifrost_cached' flag - expected cache hit")
		return
	}

	cachedBool, ok := cachedFlag.(bool)
	if !ok {
		t.Error("'bifrost_cached' flag is not a boolean")
		return
	}

	if !cachedBool {
		t.Error("'bifrost_cached' flag is false - expected cache hit")
		return
	}

	if expectedCacheType != "" {
		cacheType, exists := rawMap["bifrost_cache_type"]
		if !exists {
			t.Error("Cache metadata missing 'bifrost_cache_type'")
			return
		}

		cacheTypeStr, ok := cacheType.(string)
		if !ok {
			t.Error("'bifrost_cache_type' is not a string")
			return
		}

		if cacheTypeStr != expectedCacheType {
			t.Errorf("Expected cache type '%s', got '%s'", expectedCacheType, cacheTypeStr)
			return
		}
	}

	t.Log("✅ Response correctly served from cache")
}

// AssertNoCacheHit verifies that a response was NOT served from cache
func AssertNoCacheHit(t *testing.T, response *schemas.BifrostResponse) {
	if response.ExtraFields.RawResponse == nil {
		t.Log("✅ Response correctly not served from cache (no cache metadata)")
		return
	}

	rawMap, ok := response.ExtraFields.RawResponse.(map[string]interface{})
	if !ok {
		t.Log("✅ Response correctly not served from cache (cache metadata not a map)")
		return
	}

	cachedFlag, exists := rawMap["bifrost_cached"]
	if !exists {
		t.Log("✅ Response correctly not served from cache (no 'bifrost_cached' flag)")
		return
	}

	cachedBool, ok := cachedFlag.(bool)
	if !ok || !cachedBool {
		t.Log("✅ Response correctly not served from cache")
		return
	}

	t.Error("❌ Response was cached when it shouldn't be")
}

// WaitForCache waits for async cache operations to complete
func WaitForCache() {
	time.Sleep(200 * time.Millisecond)
}

// CreateContextWithCacheKey creates a context with the test cache key
func CreateContextWithCacheKey(value string) context.Context {
	return context.WithValue(context.Background(), ContextKey(TestCacheKey), value)
}
