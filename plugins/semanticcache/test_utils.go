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
		Scheme:  scheme,
		Host:    host,
		ApiKey:  apiKey,
		Timeout: time.Duration(timeout) * time.Second,
	}
}

// getRedisConfigFromEnv retrieves Redis configuration from environment variables
func getRedisConfigFromEnv() vectorstore.RedisConfig {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	username := os.Getenv("REDIS_USERNAME")
	password := os.Getenv("REDIS_PASSWORD")
	db := os.Getenv("REDIS_DB")
	if db == "" {
		db = "0"
	}
	dbInt, err := strconv.Atoi(db)
	if err != nil {
		dbInt = 0
	}

	timeoutStr := os.Getenv("REDIS_TIMEOUT")
	if timeoutStr == "" {
		timeoutStr = "10s"
	}
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		timeout = 10 * time.Second
	}

	return vectorstore.RedisConfig{
		Addr:           addr,
		Username:       username,
		Password:       password,
		DB:             dbInt,
		ContextTimeout: timeout,
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
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 60,
			MaxRetries:                     5,
			RetryBackoffInitial:            100 * time.Millisecond,
			RetryBackoffMax:                10 * time.Second,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 5,
			BufferSize:  10,
		},
	}, nil
}

// TestSetup contains common test setup components
type TestSetup struct {
	Logger schemas.Logger
	Store  vectorstore.VectorStore
	Plugin schemas.Plugin
	Client *bifrost.Bifrost
	Config *Config
}

// NewTestSetup creates a new test setup with default configuration
func NewTestSetup(t *testing.T) *TestSetup {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY is not set, skipping test")
	}

	return NewTestSetupWithConfig(t, &Config{
		Provider:          schemas.OpenAI,
		EmbeddingModel:    "text-embedding-3-small",
		Threshold:         0.8,
		CleanUpOnShutdown: true,
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
func NewTestSetupWithConfig(t *testing.T, config *Config) *TestSetup {
	ctx := context.Background()
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
	client, err := bifrost.Init(ctx, schemas.BifrostConfig{
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

// Cleanup cleans up test resources
func (ts *TestSetup) Cleanup() {
	if ts.Client != nil {
		ts.Client.Shutdown()
	}
}

// clearTestKeysWithStore removes all keys matching the test prefix using the store interface
func clearTestKeysWithStore(t *testing.T, store vectorstore.VectorStore) {
	// With the new unified VectorStore interface, cleanup is typically handled
	// by the vector store implementation (e.g., dropping entire classes)
	t.Logf("Test cleanup delegated to vector store implementation")
}

// CreateBasicChatRequest creates a basic chat completion request for testing
func CreateBasicChatRequest(content string, temperature float64, maxTokens int) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{
				Role: "user",
				Content: schemas.ChatMessageContent{
					ContentStr: &content,
				},
			},
		},
		Params: &schemas.ChatParameters{
			Temperature:         &temperature,
			MaxCompletionTokens: &maxTokens,
		},
	}
}

// CreateStreamingChatRequest creates a streaming chat completion request for testing
func CreateStreamingChatRequest(content string, temperature float64, maxTokens int) *schemas.BifrostChatRequest {
	return CreateBasicChatRequest(content, temperature, maxTokens)
}

// CreateSpeechRequest creates a speech synthesis request for testing
func CreateSpeechRequest(input string, voice string) *schemas.BifrostSpeechRequest {
	return &schemas.BifrostSpeechRequest{
		Provider: schemas.OpenAI,
		Model:    "tts-1",
		Input: &schemas.SpeechInput{
			Input: input,
		},
		Params: &schemas.SpeechParameters{
			VoiceConfig: &schemas.SpeechVoiceInput{
				Voice: &voice,
			},
			ResponseFormat: "mp3",
		},
	}
}

// AssertCacheHit verifies that a response was served from cache
func AssertCacheHit(t *testing.T, response *schemas.BifrostResponse, expectedCacheType string) {
	if response.ExtraFields.CacheDebug == nil {
		t.Error("Cache metadata missing 'cache_debug'")
		return
	}

	// Check that it's actually a cache hit
	if !response.ExtraFields.CacheDebug.CacheHit {
		t.Error("❌ Expected cache hit but response was not cached")
		return
	}

	if expectedCacheType != "" {
		cacheType := response.ExtraFields.CacheDebug.HitType
		if cacheType != nil && *cacheType != expectedCacheType {
			t.Errorf("Expected cache type '%s', got '%s'", expectedCacheType, *cacheType)
			return
		}

		t.Log("✅ Response correctly served from cache")
	}

	t.Log("✅ Response correctly served from cache")
}

// AssertNoCacheHit verifies that a response was NOT served from cache
func AssertNoCacheHit(t *testing.T, response *schemas.BifrostResponse) {
	if response.ExtraFields.CacheDebug == nil {
		t.Log("✅ Response correctly not served from cache (no 'cache_debug' flag)")
		return
	}

	// Check the actual CacheHit field instead of just checking if CacheDebug exists
	if response.ExtraFields.CacheDebug.CacheHit {
		t.Error("❌ Response was cached when it shouldn't be")
		return
	}

	t.Log("✅ Response correctly not served from cache (cache_debug present but CacheHit=false)")
}

// WaitForCache waits for async cache operations to complete
func WaitForCache() {
	time.Sleep(1 * time.Second)
}

// CreateEmbeddingRequest creates an embedding request for testing
func CreateEmbeddingRequest(texts []string) *schemas.BifrostEmbeddingRequest {
	return &schemas.BifrostEmbeddingRequest{
		Provider: schemas.OpenAI,
		Model:    "text-embedding-3-small",
		Input: &schemas.EmbeddingInput{
			Texts: texts,
		},
	}
}

// CreateBasicResponsesRequest creates a basic Responses API request for testing
func CreateBasicResponsesRequest(content string, temperature float64, maxTokens int) *schemas.BifrostResponsesRequest {
	userRole := schemas.ResponsesInputMessageRoleUser
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ResponsesMessage{
			{
				Role: &userRole,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &content,
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Temperature:     &temperature,
			MaxOutputTokens: &maxTokens,
		},
	}
}

// CreateResponsesRequestWithTools creates a Responses API request with tools for testing
func CreateResponsesRequestWithTools(content string, temperature float64, maxTokens int, tools []schemas.ResponsesTool) *schemas.BifrostResponsesRequest {
	req := CreateBasicResponsesRequest(content, temperature, maxTokens)
	req.Params.Tools = tools
	return req
}

// CreateResponsesRequestWithInstructions creates a Responses API request with system instructions
func CreateResponsesRequestWithInstructions(content string, instructions string, temperature float64, maxTokens int) *schemas.BifrostResponsesRequest {
	req := CreateBasicResponsesRequest(content, temperature, maxTokens)
	req.Params.Instructions = &instructions
	return req
}

// CreateStreamingResponsesRequest creates a streaming Responses API request for testing
func CreateStreamingResponsesRequest(content string, temperature float64, maxTokens int) *schemas.BifrostResponsesRequest {
	return CreateBasicResponsesRequest(content, temperature, maxTokens)
}

// CreateContextWithCacheKey creates a context with the test cache key
func CreateContextWithCacheKey(value string) context.Context {
	return context.WithValue(context.Background(), CacheKey, value)
}

// CreateContextWithCacheKeyAndType creates a context with cache key and cache type
func CreateContextWithCacheKeyAndType(value string, cacheType CacheType) context.Context {
	ctx := context.WithValue(context.Background(), CacheKey, value)
	return context.WithValue(ctx, CacheTypeKey, cacheType)
}

// CreateContextWithCacheKeyAndTTL creates a context with cache key and custom TTL
func CreateContextWithCacheKeyAndTTL(value string, ttl time.Duration) context.Context {
	ctx := context.WithValue(context.Background(), CacheKey, value)
	return context.WithValue(ctx, CacheTTLKey, ttl)
}

// CreateContextWithCacheKeyAndThreshold creates a context with cache key and custom threshold
func CreateContextWithCacheKeyAndThreshold(value string, threshold float64) context.Context {
	ctx := context.WithValue(context.Background(), CacheKey, value)
	return context.WithValue(ctx, CacheThresholdKey, threshold)
}

// CreateContextWithCacheKeyAndNoStore creates a context with cache key and no-store flag
func CreateContextWithCacheKeyAndNoStore(value string, noStore bool) context.Context {
	ctx := context.WithValue(context.Background(), CacheKey, value)
	return context.WithValue(ctx, CacheNoStoreKey, noStore)
}

// CreateTestSetupWithConversationThreshold creates a test setup with custom conversation history threshold
func CreateTestSetupWithConversationThreshold(t *testing.T, threshold int) *TestSetup {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY is not set, skipping test")
	}

	config := &Config{
		Provider:                     schemas.OpenAI,
		EmbeddingModel:               "text-embedding-3-small",
		CleanUpOnShutdown:            true,
		Threshold:                    0.8,
		ConversationHistoryThreshold: threshold,
		Keys: []schemas.Key{
			{
				Value:  os.Getenv("OPENAI_API_KEY"),
				Models: []string{},
				Weight: 1.0,
			},
		},
	}

	return NewTestSetupWithConfig(t, config)
}

// CreateTestSetupWithExcludeSystemPrompt creates a test setup with ExcludeSystemPrompt setting
func CreateTestSetupWithExcludeSystemPrompt(t *testing.T, excludeSystem bool) *TestSetup {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY is not set, skipping test")
	}

	config := &Config{
		Provider:            schemas.OpenAI,
		EmbeddingModel:      "text-embedding-3-small",
		CleanUpOnShutdown:   true,
		Threshold:           0.8,
		ExcludeSystemPrompt: &excludeSystem,
		Keys: []schemas.Key{
			{
				Value:  os.Getenv("OPENAI_API_KEY"),
				Models: []string{},
				Weight: 1.0,
			},
		},
	}

	return NewTestSetupWithConfig(t, config)
}

// CreateTestSetupWithThresholdAndExcludeSystem creates a test setup with both conversation threshold and exclude system prompt settings
func CreateTestSetupWithThresholdAndExcludeSystem(t *testing.T, threshold int, excludeSystem bool) *TestSetup {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY is not set, skipping test")
	}

	config := &Config{
		Provider:                     schemas.OpenAI,
		EmbeddingModel:               "text-embedding-3-small",
		CleanUpOnShutdown:            true,
		Threshold:                    0.8,
		ConversationHistoryThreshold: threshold,
		ExcludeSystemPrompt:          &excludeSystem,
		Keys: []schemas.Key{
			{
				Value:  os.Getenv("OPENAI_API_KEY"),
				Models: []string{},
				Weight: 1.0,
			},
		},
	}

	return NewTestSetupWithConfig(t, config)
}

// CreateConversationRequest creates a chat request with conversation history
func CreateConversationRequest(messages []schemas.ChatMessage, temperature float64, maxTokens int) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input:    messages,
		Params: &schemas.ChatParameters{
			Temperature:         &temperature,
			MaxCompletionTokens: &maxTokens,
		},
	}
}

// BuildConversationHistory creates a conversation history from pairs of user/assistant messages
func BuildConversationHistory(systemPrompt string, userAssistantPairs ...[]string) []schemas.ChatMessage {
	messages := []schemas.ChatMessage{}

	// Add system prompt if provided
	if systemPrompt != "" {
		messages = append(messages, schemas.ChatMessage{
			Role: schemas.ChatMessageRoleSystem,
			Content: schemas.ChatMessageContent{
				ContentStr: &systemPrompt,
			},
		})
	}

	// Add user/assistant pairs
	for _, pair := range userAssistantPairs {
		if len(pair) >= 1 && pair[0] != "" {
			userMsg := pair[0]
			messages = append(messages, schemas.ChatMessage{
				Role: schemas.ChatMessageRoleUser,
				Content: schemas.ChatMessageContent{
					ContentStr: &userMsg,
				},
			})
		}
		if len(pair) >= 2 && pair[1] != "" {
			assistantMsg := pair[1]
			messages = append(messages, schemas.ChatMessage{
				Role: schemas.ChatMessageRoleAssistant,
				Content: schemas.ChatMessageContent{
					ContentStr: &assistantMsg,
				},
			})
		}
	}

	return messages
}

// AddUserMessage adds a user message to existing conversation
func AddUserMessage(messages []schemas.ChatMessage, userMessage string) []schemas.ChatMessage {
	newMessage := schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: schemas.ChatMessageContent{
			ContentStr: &userMessage,
		},
	}
	return append(messages, newMessage)
}
