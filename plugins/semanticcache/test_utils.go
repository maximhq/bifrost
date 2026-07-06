package semanticcache

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
	mocker "github.com/maximhq/bifrost/plugins/mocker"
)

// embeddingThrottle bounds the number of concurrent live embedding calls
// across every parallel test in the process. The semantic cache plugin embeds
// each request against real OpenAI for similarity search; with no cap, the
// burst from many t.Parallel() tests trips OpenAI's embeddings rate limit
// (429), and each affected test then self-skips via the "upstream request
// error" guard - silently hiding dozens of tests. A small cap smooths the
// burst so the test account's retry/backoff (MaxRetries in GetConfigForProvider)
// can absorb any residual throttling. Override with
// SEMCACHE_TEST_EMBED_CONCURRENCY; defaults to 2.
var embeddingThrottle = make(chan struct{}, embeddingConcurrency())

func embeddingConcurrency() int {
	if v := os.Getenv("SEMCACHE_TEST_EMBED_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 2
}

// throttledEmbeddingExecutor wraps an EmbeddingRequestExecutor with the
// package-level concurrency cap above so embedding bursts stay under the
// provider's rate limit regardless of the test runner or -parallel setting.
func throttledEmbeddingExecutor(inner EmbeddingRequestExecutor) EmbeddingRequestExecutor {
	return func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		select {
		case embeddingThrottle <- struct{}{}:
		case <-ctx.Done():
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "embedding throttle wait cancelled: " + ctx.Err().Error()}}
		}
		defer func() { <-embeddingThrottle }()
		return inner(ctx, req)
	}
}

// isTransientUpstreamError reports whether a BifrostError reflects a
// transient upstream condition (timeout, rate-limit, 5xx) where skipping
// the test is reasonable. All other errors — including missing API keys,
// client-side issues, or non-HTTP failures — should fail the test rather
// than mask regressions behind a green skip.
func isTransientUpstreamError(err *schemas.BifrostError) bool {
	if err == nil || err.StatusCode == nil {
		return false
	}
	code := *err.StatusCode
	return code == 408 || code == 425 || code == 429 || code >= 500
}

// withTestRequestID stamps a fresh BifrostContextKeyRequestID on the context.
// Unit tests that call PreLLMHook/PostLLMHook directly need this so the plugin
// can anchor per-request state. In integration tests the framework overwrites
// it, so setting it here is safe in either path.
func withTestRequestID(ctx *schemas.BifrostContext) *schemas.BifrostContext {
	ctx.SetValue(schemas.BifrostContextKeyRequestID, uuid.NewString())
	return ctx
}

// keyForTest returns a cache key namespaced by t.Name(). All tests should
// derive their cache keys via this helper so two tests running in parallel
// (t.Parallel) cannot see each other's entries through the shared Weaviate
// namespace — direct lookups encode cache_key into the storage ID and
// semantic search filters by it.
//
// Pass suffix="" for the most common single-key-per-test case. For tests
// that exercise multiple distinct cache keys (e.g. cross-key isolation
// tests), pass suffixes to disambiguate within the test.
func keyForTest(t testing.TB, suffix string) string {
	t.Helper()
	if suffix == "" {
		return t.Name()
	}
	return t.Name() + "/" + suffix
}

// newBaseTestContext returns a BifrostContext with a fresh request ID stamped.
// Replaces bare schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
// in tests that call plugin.PreLLMHook / PostLLMHook directly — the plugin
// requires a request ID to anchor per-request state.
func newBaseTestContext() *schemas.BifrostContext {
	return withTestRequestID(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline))
}

// getWeaviateConfigFromEnv retrieves Weaviate configuration from environment variables
func getWeaviateConfigFromEnv() vectorstore.WeaviateConfig {
	scheme := os.Getenv("WEAVIATE_SCHEME")
	if scheme == "" {
		scheme = "http"
	}
	host := schemas.NewSecretVar("env.WEAVIATE_HOST")
	if host.GetValue() == "" {
		host = schemas.NewSecretVar("localhost:9000")
	}

	apiKey := schemas.NewSecretVar("env.WEAVIATE_API_KEY")

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
		Timeout: schemas.Duration(time.Duration(timeout) * time.Second),
	}
}

// getRedisConfigFromEnv retrieves Redis configuration from environment variables
func getRedisConfigFromEnv() vectorstore.RedisConfig {
	addr := schemas.NewSecretVar("env.REDIS_ADDR")
	if addr.GetValue() == "" {
		addr = schemas.NewSecretVar("localhost:6379")
	}
	username := schemas.NewSecretVar("env.REDIS_USERNAME")
	password := schemas.NewSecretVar("env.REDIS_PASSWORD")
	db := schemas.NewSecretVar("env.REDIS_DB")

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
		DB:             db,
		ContextTimeout: schemas.Duration(timeout),
	}
}

// getQdrantConfigFromEnv retrieves Qdrant configuration from environment variables
func getQdrantConfigFromEnv() vectorstore.QdrantConfig {
	host := schemas.NewSecretVar("env.QDRANT_HOST")
	if host.GetValue() == "" {
		host = schemas.NewSecretVar("localhost")
	}
	port := schemas.NewSecretVar("env.QDRANT_PORT")
	if port.GetValue() == "" {
		port = schemas.NewSecretVar("6334")
	}
	apiKey := schemas.NewSecretVar("env.QDRANT_API_KEY")
	useTLS := schemas.NewSecretVar("env.QDRANT_USE_TLS")
	if useTLS.GetValue() == "" {
		useTLS = schemas.NewSecretVar("false")
	}

	return vectorstore.QdrantConfig{
		Host:   *host,
		Port:   *port,
		APIKey: *apiKey,
		UseTLS: *useTLS,
	}
}

// getPineconeConfigFromEnv retrieves Pinecone configuration from environment variables
func getPineconeConfigFromEnv() vectorstore.PineconeConfig {
	apiKey := schemas.NewSecretVar("env.PINECONE_API_KEY")
	if apiKey.GetValue() == "" {
		apiKey = schemas.NewSecretVar("pclocal") // Pinecone Local doesn't validate API keys
	}
	indexHost := schemas.NewSecretVar("env.PINECONE_INDEX_HOST")
	if indexHost.GetValue() == "" {
		indexHost = schemas.NewSecretVar("localhost:5081") // Pinecone Local default port
	}

	return vectorstore.PineconeConfig{
		APIKey:    *apiKey,
		IndexHost: *indexHost,
	}
}

// storeConfigForType returns the env-derived connection config for a vector
// store type. The bool is false for an unrecognized type. Shared by test setup
// and the TestMain namespace sweep so both stay in lockstep when a new backend
// is added.
func storeConfigForType(storeType vectorstore.VectorStoreType) (interface{}, bool) {
	switch storeType {
	case vectorstore.VectorStoreTypeWeaviate:
		return getWeaviateConfigFromEnv(), true
	case vectorstore.VectorStoreTypeRedis:
		return getRedisConfigFromEnv(), true
	case vectorstore.VectorStoreTypeQdrant:
		return getQdrantConfigFromEnv(), true
	case vectorstore.VectorStoreTypePinecone:
		return getPineconeConfigFromEnv(), true
	default:
		return nil, false
	}
}

// BaseAccount implements the schemas.Account interface for testing purposes.
type BaseAccount struct{}

func (baseAccount *BaseAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI}, nil
}

func (baseAccount *BaseAccount) GetKeysForProvider(ctx context.Context, providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{
		{
			Value:  *schemas.NewSecretVar("env.OPENAI_API_KEY"),
			Models: schemas.WhiteList{"*"}, // "*" means allow all models
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
			RetryBackoffMax:                30 * time.Second,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 10,
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
func getMockedBifrostClient(t *testing.T, ctx *schemas.BifrostContext, logger schemas.Logger, semanticCachePlugin schemas.LLMPlugin) *bifrost.Bifrost {
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
		Account:    account,
		LLMPlugins: []schemas.LLMPlugin{semanticCachePlugin, mockerPlugin},
		Logger:     logger,
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
	Plugin schemas.LLMPlugin
	Client *bifrost.Bifrost
	Config *Config
}

// NewTestSetup creates a new test setup with default configuration
func NewTestSetup(t *testing.T) *TestSetup {
	return NewTestSetupWithConfig(t, &Config{
		Provider:       schemas.OpenAI,
		EmbeddingModel: "text-embedding-3-small",
		Dimension:      1536,
		Threshold:      0.8,
	})
}

// NewTestSetupWithConfig creates a new test setup with custom configuration
func NewTestSetupWithConfig(t *testing.T, config *Config) *TestSetup {
	return NewTestSetupWithVectorStore(t, config, vectorstore.VectorStoreTypeWeaviate)
}

// SharedTestNamespace is the single Weaviate class all parallel tests share.
// Mirrors production: many concurrent requests hit one namespace, isolated
// by per-test cache_keys (see keyForTest). Distinct from the plugin's
// production default so test runs can't collide with a real cache.
const SharedTestNamespace = "BifrostSemanticCachePluginTest"

var (
	sharedTestNamespaceOnce sync.Once
	sharedTestNamespaceErr  error
)

// ensureSharedTestNamespace creates the shared test class exactly once per
// test process — sync.Once gates the TOCTOU race between concurrent
// Plugin.Init calls (each of which would otherwise check-then-create against
// the shared store and one would lose the race).
//
// Subsequent Plugin.Init calls in tests still invoke CreateNamespace, but the
// vectorstore implementations short-circuit when the class already exists.
func ensureSharedTestNamespace(ctx context.Context, store vectorstore.VectorStore, dim int) error {
	sharedTestNamespaceOnce.Do(func() {
		sharedTestNamespaceErr = store.CreateNamespace(ctx, SharedTestNamespace, dim, VectorStoreProperties)
	})
	return sharedTestNamespaceErr
}

// NewTestSetupWithVectorStore creates a new test setup with custom configuration and vector store type
func NewTestSetupWithVectorStore(t *testing.T, config *Config, storeType vectorstore.VectorStoreType) *TestSetup {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)

	// All tests share one namespace; isolation comes from per-test cache_keys.
	if config.VectorStoreNamespace == "" {
		config.VectorStoreNamespace = SharedTestNamespace
	}

	// Get the appropriate config for the vector store type
	storeConfig, ok := storeConfigForType(storeType)
	if !ok {
		t.Fatalf("Unsupported vector store type: %s", storeType)
	}

	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Type:    storeType,
		Config:  storeConfig,
		Enabled: true,
	}, logger)
	if err != nil {
		t.Skipf("Vector store %s not available or failed to connect: %v", storeType, err)
	}

	// Pre-create the shared namespace exactly once across the test process so
	// concurrent Plugin.Init calls don't lose the TOCTOU race inside the
	// vector store driver (check-then-create).
	preCreateCtx, preCreateCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer preCreateCancel()
	if err := ensureSharedTestNamespace(preCreateCtx, store, config.Dimension); err != nil {
		t.Fatalf("Failed to create shared test namespace: %v", err)
	}

	plugin, err := Init(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), config, logger, store)
	if err != nil {
		t.Fatalf("Failed to initialize plugin: %v", err)
	}

	// Clear test keys
	pluginImpl := plugin.(*Plugin)
	clearTestKeysWithStore(t, pluginImpl.store)

	// Get a mocked Bifrost client
	client := getMockedBifrostClient(t, ctx, logger, plugin)

	// Wire the global client as the embedding executor so semantic search works.
	pluginImpl.SetEmbeddingRequestExecutor(throttledEmbeddingExecutor(client.EmbeddingRequest))

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

// WaitForCache waits for async cache operations to complete.
//
// WaitForPendingOperations now drains the writersWg accurately (every
// PostLLMHook goroutine + the expired-entry async delete is tracked), so the
// stored entries are guaranteed durable when this returns. The small sleep
// below is a buffer for vector store index visibility on stores with eventual
// consistency (Weaviate is usually immediate on single-node, but cloud or
// multi-shard setups may need a tick to make the entry queryable).
//
// Override via SEMCACHE_TEST_INDEX_DELAY_MS for slower stores / CI.
func WaitForCache(plugin schemas.LLMPlugin) {
	if p, ok := plugin.(*Plugin); ok {
		p.WaitForPendingOperations()
	}
	delayMs := 100
	if v := os.Getenv("SEMCACHE_TEST_INDEX_DELAY_MS"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 {
			delayMs = parsed
		}
	}
	if delayMs > 0 {
		time.Sleep(time.Duration(delayMs) * time.Millisecond)
	}
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

// CreateImageGenerationRequest creates an image generation request for testing
func CreateImageGenerationRequest(prompt string, size string, quality string) *schemas.BifrostImageGenerationRequest {
	return &schemas.BifrostImageGenerationRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-image-1",
		Input: &schemas.ImageGenerationInput{
			Prompt: prompt,
		},
		Params: &schemas.ImageGenerationParameters{
			Size:    bifrost.Ptr(size),
			Quality: bifrost.Ptr(quality),
			N:       bifrost.Ptr(1),
		},
	}
}

// CreateContextWithCacheKey creates a context with the test cache key
// CreateContextWithCacheKey creates a context with a per-test cache key.
// suffix may be "" for tests using only one cache key.
func CreateContextWithCacheKey(t testing.TB, suffix string) *schemas.BifrostContext {
	return withTestRequestID(schemas.NewBifrostContextWithValue(context.Background(), schemas.NoDeadline, CacheKey, keyForTest(t, suffix)))
}

// CreateContextWithCacheKeyAndType creates a context with cache key and cache type
func CreateContextWithCacheKeyAndType(t testing.TB, suffix string, cacheType CacheType) *schemas.BifrostContext {
	return withTestRequestID(schemas.NewBifrostContextWithValue(context.Background(), schemas.NoDeadline, CacheKey, keyForTest(t, suffix)).WithValue(CacheTypeKey, cacheType))
}

// CreateContextWithCacheKeyAndTTL creates a context with cache key and custom TTL
func CreateContextWithCacheKeyAndTTL(t testing.TB, suffix string, ttl time.Duration) *schemas.BifrostContext {
	return withTestRequestID(schemas.NewBifrostContextWithValue(context.Background(), schemas.NoDeadline, CacheKey, keyForTest(t, suffix)).WithValue(CacheTTLKey, ttl))
}

// CreateContextWithCacheKeyAndThreshold creates a context with cache key and custom threshold
func CreateContextWithCacheKeyAndThreshold(t testing.TB, suffix string, threshold float64) *schemas.BifrostContext {
	return withTestRequestID(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline).WithValue(CacheKey, keyForTest(t, suffix)).WithValue(CacheThresholdKey, threshold))
}

// CreateContextWithCacheKeyAndNoStore creates a context with cache key and no-store flag
func CreateContextWithCacheKeyAndNoStore(t testing.TB, suffix string, noStore bool) *schemas.BifrostContext {
	return withTestRequestID(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline).WithValue(CacheKey, keyForTest(t, suffix)).WithValue(CacheNoStoreKey, noStore))
}

// CreateTestSetupWithConversationThreshold creates a test setup with custom conversation history threshold
func CreateTestSetupWithConversationThreshold(t *testing.T, threshold int) *TestSetup {
	config := &Config{
		Provider:                     schemas.OpenAI,
		EmbeddingModel:               "text-embedding-3-small",
		Dimension:                    1536,
		Threshold:                    0.8,
		ConversationHistoryThreshold: threshold,
	}

	return NewTestSetupWithConfig(t, config)
}

// CreateTestSetupWithExcludeSystemPrompt creates a test setup with ExcludeSystemPrompt setting
func CreateTestSetupWithExcludeSystemPrompt(t *testing.T, excludeSystem bool) *TestSetup {
	config := &Config{
		Provider:            schemas.OpenAI,
		EmbeddingModel:      "text-embedding-3-small",
		Dimension:           1536,
		Threshold:           0.8,
		ExcludeSystemPrompt: &excludeSystem,
	}

	return NewTestSetupWithConfig(t, config)
}

// CreateTestSetupWithThresholdAndExcludeSystem creates a test setup with both conversation threshold and exclude system prompt settings
func CreateTestSetupWithThresholdAndExcludeSystem(t *testing.T, threshold int, excludeSystem bool) *TestSetup {
	config := &Config{
		Provider:                     schemas.OpenAI,
		EmbeddingModel:               "text-embedding-3-small",
		Dimension:                    1536,
		Threshold:                    0.8,
		ConversationHistoryThreshold: threshold,
		ExcludeSystemPrompt:          &excludeSystem,
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
