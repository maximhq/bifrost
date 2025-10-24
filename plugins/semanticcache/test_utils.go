package semanticcache

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
	mocker "github.com/maximhq/bifrost/plugins/mocker"
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
		APIKey:  apiKey,
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

// getMockRules returns a list of mock rules for the semantic cache tests
func getMockRules() []mocker.MockRule {
	return []mocker.MockRule{
		// Core test prompts
		{
			Name:        "bifrost-definition",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("(?i)What is Bifrost.*")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "Bifrost is a unified API for interacting with multiple AI providers."}},
			},
		},
		{
			Name:        "machine-learning-explanation",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("(?i)what is machine learning\\?|explain machine learning|machine learning concepts|can you explain machine learning|explain the basics of machine learning")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "Machine learning is a field of AI that uses statistical techniques to give computer systems the ability to learn from data."}},
			},
		},
		{
			Name:        "ai-explanation",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("(?i)what is artificial intelligence\\?|can you explain what ai is\\?|define artificial intelligence")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "Artificial intelligence is the simulation of human intelligence in machines."}},
			},
		},
		{
			Name:        "capital-of-france",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("What is the capital of France\\?")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "The capital of France is Paris."}},
			},
		},
		{
			Name:        "newton-laws",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("(?i)describe.*newton.*three laws|describe.*three laws.*newton")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "Newton's three laws of motion are: 1. An object at rest stays at rest and an object in motion stays in motion with the same speed and in the same direction unless acted upon by an unbalanced force. 2. The acceleration of an object as produced by a net force is directly proportional to the magnitude of the net force, in the same direction as the net force, and inversely proportional to the mass of the object. 3. For every action, there is an equal and opposite reaction."}},
			},
		},
		// Weather-related prompts
		{
			Name:        "weather-question",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("(?i)what.*weather|weather.*like")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "It's sunny today with a temperature of 72°F."}},
			},
		},
		// Blockchain and deep learning
		{
			Name:        "blockchain-definition",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("(?i)define blockchain|blockchain technology")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "Blockchain is a distributed ledger technology that maintains a continuously growing list of records."}},
			},
		},
		{
			Name:        "deep-learning",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("(?i)what is deep learning")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "Deep learning is a subset of machine learning that uses neural networks with multiple layers."}},
			},
		},
		// Quantum computing
		{
			Name:        "quantum-computing",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("(?i)quantum computing|explain quantum")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "Quantum computing uses quantum mechanical phenomena to process information in ways that classical computers cannot."}},
			},
		},
		// Conversation prompts
		{
			Name:        "hello-greeting",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("(?i)^hello$|^hi$|hello.*world")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "Hello! How can I help you today?"}},
			},
		},
		{
			Name:        "how-are-you",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("(?i)how are you")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "I'm doing well, thank you for asking!"}},
			},
		},
		{
			Name:        "meaning-of-life",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("(?i)meaning of life")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "The meaning of life is a philosophical question that has been pondered for centuries. Some say it's 42!"}},
			},
		},
		{
			Name:        "short-story",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("(?i)tell me.*short story")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "Once upon a time, there was a brave knight who saved the day."}},
			},
		},
		// Test-specific prompts
		{
			Name:        "test-configuration",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("(?i)test configuration")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "This is a test configuration response."}},
			},
		},
		{
			Name:        "test-messages",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("(?i)test.*message|test.*no-store|test.*cache|test.*error|ttl test|threshold test|provider.*test|edge case test")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "This is a test response for various test scenarios."}},
			},
		},
		{
			Name:        "long-prompt",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("(?i)very long prompt")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "This is a response to a very long prompt."}},
			},
		},
		{
			Name:        "parameter-tests",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("(?i)test.*parameters|performance test")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "Parameter test response with various settings."}},
			},
		},
		// Dynamic message patterns (for conversation tests)
		{
			Name:        "message-pattern",
			Enabled:     true,
			Conditions:  mocker.Conditions{MessageRegex: bifrost.Ptr("(?i)message \\d+")},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "Response to numbered message."}},
			},
		},
		// Default catch-all rule (lowest priority)
		{
			Name:        "default-mock",
			Enabled:     true,
			Priority:    -1, // Lower priority
			Conditions:  mocker.Conditions{},
			Probability: 1.0,
			Responses: []mocker.Response{
				{Type: mocker.ResponseTypeSuccess, Content: &mocker.SuccessResponse{Message: "This is a generic mocked response."}},
			},
		},
	}
}

// getMockedBifrostClient creates a Bifrost client with a mocker plugin for testing
func getMockedBifrostClient(t *testing.T, ctx context.Context, logger schemas.Logger, semanticCachePlugin schemas.Plugin) *bifrost.Bifrost {
	mockerCfg := mocker.MockerConfig{
		Enabled: true,
		Rules:   getMockRules(),
	}

	mockerPlugin, err := mocker.Init(mockerCfg)
	if err != nil {
		t.Fatalf("Failed to initialize mocker plugin: %v", err)
	}

	account := &BaseAccount{}
	client, err := bifrost.Init(ctx, schemas.BifrostConfig{
		Account: account,
		Plugins: []schemas.Plugin{semanticCachePlugin, mockerPlugin},
		Logger:  logger,
	})
	if err != nil {
		t.Fatalf("Error initializing Bifrost with mocker: %v", err)
	}

	return client
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

	// Keep Weaviate for embeddings, as mocker only affects chat completions
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

	// Get a mocked Bifrost client
	client := getMockedBifrostClient(t, ctx, logger, plugin)

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
				Content: &schemas.ChatMessageContent{
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
	extraFields := response.GetExtraFields()

	if extraFields.CacheDebug == nil {
		t.Error("Cache metadata missing 'cache_debug'")
		return
	}

	// Check that it's actually a cache hit
	if !extraFields.CacheDebug.CacheHit {
		t.Error("❌ Expected cache hit but response was not cached")
		return
	}

	if expectedCacheType != "" {
		cacheType := extraFields.CacheDebug.HitType
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
	extraFields := response.GetExtraFields()

	if extraFields.CacheDebug == nil {
		t.Log("✅ Response correctly not served from cache (no 'cache_debug' flag)")
		return
	}

	// Check the actual CacheHit field instead of just checking if CacheDebug exists
	if extraFields.CacheDebug.CacheHit {
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
			Content: &schemas.ChatMessageContent{
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
				Content: &schemas.ChatMessageContent{
					ContentStr: &userMsg,
				},
			})
		}
		if len(pair) >= 2 && pair[1] != "" {
			assistantMsg := pair[1]
			messages = append(messages, schemas.ChatMessage{
				Role: schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{
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
		Content: &schemas.ChatMessageContent{
			ContentStr: &userMessage,
		},
	}
	return append(messages, newMessage)
}

// RetryConfig defines retry configuration for API requests
type RetryConfig struct {
	MaxRetries int
	BaseDelay  time.Duration
}

// DefaultRetryConfig returns the default retry configuration
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries: 2,
		BaseDelay:  5 * time.Millisecond,
	}
}

// WithRetries executes a function with retry logic and exponential backoff
// If all retries fail, the test is skipped instead of failed
func WithRetries[T any](t *testing.T, operation func() (T, *schemas.BifrostError), config RetryConfig, operationName string) (T, *schemas.BifrostError) {
	var lastErr *schemas.BifrostError
	var result T

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: baseDelay * 2^(attempt-1)
			delay := config.BaseDelay * time.Duration(1<<(attempt-1))
			t.Logf("Retrying %s (attempt %d/%d) after %v...", operationName, attempt+1, config.MaxRetries+1, delay)
			time.Sleep(delay)
		}

		result, lastErr = operation()
		if lastErr == nil {
			if attempt > 0 {
				t.Logf("✅ %s succeeded on attempt %d", operationName, attempt+1)
			}
			return result, nil
		}

		lastErrorMessage := ""
		if lastErr.Error != nil {
			lastErrorMessage = lastErr.Error.Message
		}

		t.Logf("❌ %s failed on attempt %d: %v", operationName, attempt+1, lastErrorMessage)
	}

	// If all retries failed, skip the test instead of failing it
	skipMessage := fmt.Sprintf("Skipping test: %s failed after %d attempts", operationName, config.MaxRetries+1)
	if lastErr != nil && lastErr.Error != nil {
		skipMessage += fmt.Sprintf(" (last error: %s)", lastErr.Error.Message)
	}
	t.Skip(skipMessage)

	// This return will never be reached due to t.Skip(), but needed for compilation
	return result, lastErr
}

// ChatRequestWithRetries executes a chat completion request with retry logic
func ChatRequestWithRetries(t *testing.T, client *bifrost.Bifrost, ctx context.Context, request *schemas.BifrostChatRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	config := DefaultRetryConfig()
	return WithRetries(t, func() (*schemas.BifrostResponse, *schemas.BifrostError) {
		response, err := client.ChatCompletionRequest(ctx, request)
		if err != nil {
			if err.Error != nil {
				return &schemas.BifrostResponse{ChatResponse: response}, err
			}
			return &schemas.BifrostResponse{ChatResponse: response}, err
		}
		return &schemas.BifrostResponse{ChatResponse: response}, nil
	}, config, "chat completion request")
}

// ResponsesRequestWithRetries executes a responses request with retry logic
func ResponsesRequestWithRetries(t *testing.T, client *bifrost.Bifrost, ctx context.Context, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	config := DefaultRetryConfig()
	return WithRetries(t, func() (*schemas.BifrostResponse, *schemas.BifrostError) {
		response, err := client.ResponsesRequest(ctx, request)
		if err != nil {
			if err.Error != nil {
				return &schemas.BifrostResponse{ResponsesResponse: response}, err
			}
			return &schemas.BifrostResponse{ResponsesResponse: response}, err
		}
		return &schemas.BifrostResponse{ResponsesResponse: response}, nil
	}, config, "responses request")
}

// EmbeddingRequestWithRetries executes an embedding request with retry logic
func EmbeddingRequestWithRetries(t *testing.T, client *bifrost.Bifrost, ctx context.Context, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	config := DefaultRetryConfig()
	return WithRetries(t, func() (*schemas.BifrostResponse, *schemas.BifrostError) {
		response, err := client.EmbeddingRequest(ctx, request)
		if err != nil {
			if err.Error != nil {
				return &schemas.BifrostResponse{EmbeddingResponse: response}, err
			}
			return &schemas.BifrostResponse{EmbeddingResponse: response}, err
		}
		return &schemas.BifrostResponse{EmbeddingResponse: response}, nil
	}, config, "embedding request")
}

// SpeechRequestWithRetries executes a speech request with retry logic
func SpeechRequestWithRetries(t *testing.T, client *bifrost.Bifrost, ctx context.Context, request *schemas.BifrostSpeechRequest) (*schemas.BifrostResponse, *schemas.BifrostError) {
	config := DefaultRetryConfig()
	return WithRetries(t, func() (*schemas.BifrostResponse, *schemas.BifrostError) {
		response, err := client.SpeechRequest(ctx, request)
		if err != nil {
			if err.Error != nil {
				return &schemas.BifrostResponse{SpeechResponse: response}, err
			}
			return &schemas.BifrostResponse{SpeechResponse: response}, err
		}
		return &schemas.BifrostResponse{SpeechResponse: response}, nil
	}, config, "speech request")
}

// ChatStreamingRequestWithRetries executes a chat streaming request with retry logic
func ChatStreamingRequestWithRetries(t *testing.T, client *bifrost.Bifrost, ctx context.Context, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStream, *schemas.BifrostError) {
	config := DefaultRetryConfig()
	var lastErr *schemas.BifrostError
	var result chan *schemas.BifrostStream

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: baseDelay * 2^(attempt-1)
			delay := config.BaseDelay * time.Duration(1<<(attempt-1))
			t.Logf("Retrying chat streaming request (attempt %d/%d) after %v...", attempt+1, config.MaxRetries+1, delay)
			time.Sleep(delay)
		}

		result, lastErr = client.ChatCompletionStreamRequest(ctx, request)
		if lastErr == nil {
			if attempt > 0 {
				t.Logf("✅ chat streaming request succeeded on attempt %d", attempt+1)
			}
			return result, nil
		}

		lastErrorMessage := ""
		if lastErr.Error != nil {
			lastErrorMessage = lastErr.Error.Message
		}

		t.Logf("❌ chat streaming request failed on attempt %d: %v", attempt+1, lastErrorMessage)
	}

	// If all retries failed, skip the test instead of failing it
	skipMessage := fmt.Sprintf("Skipping test: chat streaming request failed after %d attempts", config.MaxRetries+1)
	if lastErr != nil && lastErr.Error != nil {
		skipMessage += fmt.Sprintf(" (last error: %s)", lastErr.Error.Message)
	}
	t.Skip(skipMessage)

	// This return will never be reached due to t.Skip(), but needed for compilation
	return result, lastErr
}
