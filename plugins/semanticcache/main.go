// Package semanticcache provides semantic caching integration for Bifrost plugin.
// This plugin caches responses using both direct hash matching (xxhash) and semantic similarity search (embeddings).
// It supports configurable caching behavior via the VectorStore abstraction, with TTL management and streaming response handling.
package semanticcache

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// Config contains configuration for the semantic cache plugin.
// The VectorStore abstraction handles the underlying storage implementation and its defaults.
// Only specify values you want to override from the semantic cache defaults.
type Config struct {
	// Embedding Model settings - REQUIRED for semantic caching
	Provider       schemas.ModelProvider `json:"provider"`
	Keys           []schemas.Key         `json:"keys"`
	EmbeddingModel string                `json:"embedding_model,omitempty"` // Model to use for generating embeddings (optional)

	// Plugin behavior settings
	CleanUpOnShutdown    bool          `json:"cleanup_on_shutdown,omitempty"`    // Clean up cache on shutdown (default: false)
	TTL                  time.Duration `json:"ttl,omitempty"`                    // Time-to-live for cached responses (default: 5min)
	Threshold            float64       `json:"threshold,omitempty"`              // Cosine similarity threshold for semantic matching (default: 0.8)
	VectorStoreNamespace string        `json:"vector_store_namespace,omitempty"` // Namespace for vector store (optional)
	Dimension            int           `json:"dimension"`                        // Dimension for vector store

	// Advanced caching behavior
	DefaultCacheKey              string `json:"default_cache_key,omitempty"`              // Default cache key used when no per-request key is provided (optional, caching is disabled when empty and no per-request key is set)
	ConversationHistoryThreshold int    `json:"conversation_history_threshold,omitempty"` // Skip caching for requests with more than this number of messages in the conversation history (default: 3)
	CacheByModel                 *bool  `json:"cache_by_model,omitempty"`                // Include model in cache key (default: true)
	CacheByProvider              *bool  `json:"cache_by_provider,omitempty"`             // Include provider in cache key (default: true)
	ExcludeSystemPrompt          *bool  `json:"exclude_system_prompt,omitempty"`         // Exclude system prompt in cache key (default: false)
}

// UnmarshalJSON implements custom JSON unmarshaling for semantic cache Config.
// It supports TTL parsing from both string durations ("1m", "1hr") and numeric seconds for configurable cache behavior.
func (c *Config) UnmarshalJSON(data []byte) error {
	// Define a temporary struct to avoid infinite recursion
	type TempConfig struct {
		Provider                     string        `json:"provider"`
		Keys                         []schemas.Key `json:"keys"`
		EmbeddingModel               string        `json:"embedding_model,omitempty"`
		CleanUpOnShutdown            bool          `json:"cleanup_on_shutdown,omitempty"`
		Dimension                    int           `json:"dimension"`
		TTL                          interface{}   `json:"ttl,omitempty"`
		Threshold                    float64       `json:"threshold,omitempty"`
		VectorStoreNamespace         string        `json:"vector_store_namespace,omitempty"`
		ConversationHistoryThreshold int           `json:"conversation_history_threshold,omitempty"`
		CacheByModel                 *bool         `json:"cache_by_model,omitempty"`
		CacheByProvider              *bool         `json:"cache_by_provider,omitempty"`
		ExcludeSystemPrompt          *bool         `json:"exclude_system_prompt,omitempty"`
	}

	var temp TempConfig
	if err := json.Unmarshal(data, &temp); err != nil {
		return fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Set simple fields
	c.Provider = schemas.ModelProvider(temp.Provider)
	c.Keys = temp.Keys
	c.EmbeddingModel = temp.EmbeddingModel
	c.CleanUpOnShutdown = temp.CleanUpOnShutdown
	c.Dimension = temp.Dimension
	c.CacheByModel = temp.CacheByModel
	c.CacheByProvider = temp.CacheByProvider
	c.VectorStoreNamespace = temp.VectorStoreNamespace
	c.ConversationHistoryThreshold = temp.ConversationHistoryThreshold
	c.Threshold = temp.Threshold
	c.ExcludeSystemPrompt = temp.ExcludeSystemPrompt
	// Handle TTL field with custom parsing for VectorStore-backed cache behavior
	if temp.TTL != nil {
		switch v := temp.TTL.(type) {
		case string:
			// Try parsing as duration string (e.g., "1m", "1hr") for semantic cache TTL
			duration, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("failed to parse TTL duration string '%s': %w", v, err)
			}
			c.TTL = duration
		case int:
			// Handle integer seconds for semantic cache TTL
			c.TTL = time.Duration(v) * time.Second
		default:
			// Try converting to string and parsing as number for semantic cache TTL
			ttlStr := fmt.Sprintf("%v", v)
			if seconds, err := strconv.ParseFloat(ttlStr, 64); err == nil {
				c.TTL = time.Duration(seconds * float64(time.Second))
			} else {
				return fmt.Errorf("unsupported TTL type: %T (value: %v)", v, v)
			}
		}
	}

	return nil
}

// StreamChunk represents a single chunk from a streaming response
type StreamChunk struct {
	Timestamp    time.Time                // When chunk was received
	Response     *schemas.BifrostResponse // The actual response chunk
	FinishReason *string                  // If this is the final chunk
}

// StreamAccumulator manages accumulation of streaming chunks for caching
type StreamAccumulator struct {
	RequestID      string                 // The request ID
	Chunks         []*StreamChunk         // All chunks for this stream
	IsComplete     bool                   // Whether the stream is complete
	HasError       bool                   // Whether any chunk in the stream had an error
	FinalTimestamp time.Time              // When the stream completed
	Embedding      []float32              // Embedding for the original request
	Metadata       map[string]interface{} // Metadata for caching
	TTL            time.Duration          // TTL for this cache entry
	mu             sync.Mutex             // Protects chunk operations
}

// Plugin implements the schemas.LLMPlugin interface for semantic caching.
// It caches responses using a two-tier approach: direct hash matching for exact requests
// and semantic similarity search for related content. The plugin supports configurable caching behavior
// via the VectorStore abstraction, including TTL management and streaming response handling.
//
// Fields:
//   - store: VectorStore instance for semantic cache operations
//   - config: Plugin configuration including semantic cache and caching settings
//   - logger: Logger instance for plugin operations
type Plugin struct {
	store              vectorstore.VectorStore
	config             *Config
	logger             schemas.Logger
	client             *bifrost.Bifrost
	streamAccumulators sync.Map // Track stream accumulators by request ID
	waitGroup          sync.WaitGroup
}

// Plugin constants
const (
	PluginName                          string        = "semantic_cache"
	DefaultVectorStoreNamespace         string        = "BifrostSemanticCachePlugin"
	PluginLoggerPrefix                  string        = "[Semantic Cache]"
	CacheConnectionTimeout              time.Duration = 5 * time.Second
	CreateNamespaceTimeout              time.Duration = 30 * time.Second
	CacheSetTimeout                     time.Duration = 30 * time.Second
	DefaultCacheTTL                     time.Duration = 5 * time.Minute
	DefaultCacheThreshold               float64       = 0.8
	DefaultConversationHistoryThreshold int           = 3
)

var SelectFields = []string{"request_hash", "response", "stream_chunks", "expires_at", "cache_key", "provider", "model"}

var VectorStoreProperties = map[string]vectorstore.VectorStoreProperties{
	"request_hash": {
		DataType:    vectorstore.VectorStorePropertyTypeString,
		Description: "The hash of the request",
	},
	"response": {
		DataType:    vectorstore.VectorStorePropertyTypeString,
		Description: "The response from the provider",
	},
	"stream_chunks": {
		DataType:    vectorstore.VectorStorePropertyTypeStringArray,
		Description: "The stream chunks from the provider",
	},
	"expires_at": {
		DataType:    vectorstore.VectorStorePropertyTypeInteger,
		Description: "The expiration time of the cache entry",
	},
	"cache_key": {
		DataType:    vectorstore.VectorStorePropertyTypeString,
		Description: "The cache key from the request",
	},
	"provider": {
		DataType:    vectorstore.VectorStorePropertyTypeString,
		Description: "The provider used for the request",
	},
	"model": {
		DataType:    vectorstore.VectorStorePropertyTypeString,
		Description: "The model used for the request",
	},
	"params_hash": {
		DataType:    vectorstore.VectorStorePropertyTypeString,
		Description: "The hash of the parameters used for the request",
	},
	"from_bifrost_semantic_cache_plugin": {
		DataType:    vectorstore.VectorStorePropertyTypeBoolean,
		Description: "Whether the cache entry was created by the BifrostSemanticCachePlugin",
	},
}

type PluginAccount struct {
	provider schemas.ModelProvider
	keys     []schemas.Key
}

func (pa *PluginAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{pa.provider}, nil
}

func (pa *PluginAccount) GetKeysForProvider(ctx context.Context, providerKey schemas.ModelProvider) ([]schemas.Key, error) {
	return pa.keys, nil
}

func (pa *PluginAccount) GetConfigForProvider(providerKey schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	return &schemas.ProviderConfig{
		NetworkConfig:            schemas.DefaultNetworkConfig,
		ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
	}, nil
}

// Dependencies is a list of dependencies that the plugin requires.
var Dependencies []framework.FrameworkDependency = []framework.FrameworkDependency{framework.FrameworkDependencyVectorStore}

// ProvidersWithEmbeddingSupport lists all providers that support embedding operations.
// Providers not in this list will return UnsupportedOperationError for embedding requests.
var ProvidersWithEmbeddingSupport = map[schemas.ModelProvider]bool{
	schemas.OpenAI:      true,
	schemas.Azure:       true,
	schemas.Bedrock:     true,
	schemas.Cohere:      true,
	schemas.Gemini:      true,
	schemas.Vertex:      true,
	schemas.Mistral:     true,
	schemas.Ollama:      true,
	schemas.Nebius:      true,
	schemas.HuggingFace: true,
	schemas.SGL:         true,
}

const (
	CacheKey          schemas.BifrostContextKey = "semantic_cache_key"        // To set the cache key for a request - REQUIRED for all requests
	CacheTTLKey       schemas.BifrostContextKey = "semantic_cache_ttl"        // To explicitly set the TTL for a request
	CacheThresholdKey schemas.BifrostContextKey = "semantic_cache_threshold"  // To explicitly set the threshold for a request
	CacheTypeKey      schemas.BifrostContextKey = "semantic_cache_cache_type" // To explicitly set the cache type for a request
	CacheNoStoreKey   schemas.BifrostContextKey = "semantic_cache_no_store"   // To explicitly disable storing the response in the cache

	// context keys for internal usage
	requestIDKey              schemas.BifrostContextKey = "semantic_cache_request_id"
	requestHashKey            schemas.BifrostContextKey = "semantic_cache_request_hash"
	requestEmbeddingKey       schemas.BifrostContextKey = "semantic_cache_embedding"
	requestEmbeddingTokensKey schemas.BifrostContextKey = "semantic_cache_embedding_tokens"
	requestParamsHashKey      schemas.BifrostContextKey = "semantic_cache_params_hash"
	requestModelKey           schemas.BifrostContextKey = "semantic_cache_model"
	requestProviderKey        schemas.BifrostContextKey = "semantic_cache_provider"
	isCacheHitKey             schemas.BifrostContextKey = "semantic_cache_is_cache_hit"
	cacheHitTypeKey           schemas.BifrostContextKey = "semantic_cache_cache_hit_type"
)

type CacheType string

const (
	CacheTypeDirect   CacheType = "direct"
	CacheTypeSemantic CacheType = "semantic"
)

// Init creates a new semantic cache plugin instance with the provided configuration.
// It uses the VectorStore abstraction for cache operations and returns a configured plugin.
//
// The VectorStore handles the underlying storage implementation and its defaults.
// The plugin only sets defaults for its own behavior (TTL, cache key generation, etc.).
//
// Parameters:
//   - config: Semantic cache and plugin configuration (CacheKey is required)
//   - logger: Logger instance for the plugin
//   - store: VectorStore instance for cache operations
//
// Returns:
//   - schemas.LLMPlugin: A configured semantic cache plugin instance
//   - error: Any error that occurred during plugin initialization
func Init(ctx context.Context, config *Config, logger schemas.Logger, store vectorstore.VectorStore) (schemas.LLMPlugin, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	// Set plugin-specific defaults
	if config.VectorStoreNamespace == "" {
		logger.Debug(PluginLoggerPrefix + " Vector store namespace is not set, using default of " + DefaultVectorStoreNamespace)
		config.VectorStoreNamespace = DefaultVectorStoreNamespace
	}
	if config.TTL == 0 {
		logger.Debug(PluginLoggerPrefix + " TTL is not set, using default of 5 minutes")
		config.TTL = DefaultCacheTTL
	}
	if config.Threshold == 0 {
		logger.Debug(PluginLoggerPrefix + " Threshold is not set, using default of " + strconv.FormatFloat(DefaultCacheThreshold, 'f', -1, 64))
		config.Threshold = DefaultCacheThreshold
	}
	if config.ConversationHistoryThreshold == 0 {
		logger.Debug(PluginLoggerPrefix + " Conversation history threshold is not set, using default of " + strconv.Itoa(DefaultConversationHistoryThreshold))
		config.ConversationHistoryThreshold = DefaultConversationHistoryThreshold
	}

	// Set cache behavior defaults
	if config.CacheByModel == nil {
		config.CacheByModel = bifrost.Ptr(true)
	}
	if config.CacheByProvider == nil {
		config.CacheByProvider = bifrost.Ptr(true)
	}

	plugin := &Plugin{
		store:     store,
		config:    config,
		logger:    logger,
		waitGroup: sync.WaitGroup{},
	}

	if config.Provider == "" || len(config.Keys) == 0 {
		logger.Warn(PluginLoggerPrefix + " Provider and keys are required for semantic cache, falling back to direct search only")
	} else {
		// Validate that the provider supports embeddings
		if bifrost.IsStandardProvider(config.Provider) && !ProvidersWithEmbeddingSupport[config.Provider] {
			return nil, fmt.Errorf("provider '%s' does not support embedding operations required for semantic cache. Supported providers: openai, azure, bedrock, cohere, gemini, vertex, mistral, ollama, nebius, huggingface, sgl. Note: custom providers based on embedding-capable providers are also supported", config.Provider)
		}

		bifrost, err := bifrost.Init(ctx, schemas.BifrostConfig{
			Logger: logger,
			Account: &PluginAccount{
				provider: config.Provider,
				keys:     config.Keys,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to initialize bifrost for semantic cache: %w", err)
		}

		plugin.client = bifrost
	}

	createCtx, cancel := context.WithTimeout(ctx, CreateNamespaceTimeout)
	defer cancel()
	if err := store.CreateNamespace(createCtx, config.VectorStoreNamespace, config.Dimension, VectorStoreProperties); err != nil {
		return nil, fmt.Errorf("failed to create namespace for semantic cache: %w", err)
	}

	return plugin, nil
}

// GetName returns the canonical name of the semantic cache plugin.
// This name is used for plugin identification and logging purposes.
//
// Returns:
//   - string: The plugin name for semantic cache
func (plugin *Plugin) GetName() string {
	return PluginName
}

// HTTPTransportPreHook is not used for this plugin
func (plugin *Plugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

// HTTPTransportPostHook is not used for this plugin
func (plugin *Plugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
}

// HTTPTransportStreamChunkHook passes through streaming chunks unchanged
func (plugin *Plugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// PreLLMHook is called before a request is processed by Bifrost.
// It performs a two-stage cache lookup: first direct hash matching, then semantic similarity search.
// Uses UUID-based keys for entries stored in the VectorStore.
//
// Parameters:
//   - ctx: Pointer to the schemas.BifrostContext
//   - req: The incoming Bifrost request
//
// Returns:
//   - *schemas.BifrostRequest: The original request
//   - *schemas.BifrostResponse: Cached response if found, nil otherwise
//   - error: Any error that occurred during cache lookup
func (plugin *Plugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	provider, model, _ := req.GetRequestFields()
	// Get the cache key from the context
	var cacheKey string
	var ok bool

	cacheKey, ok = ctx.Value(CacheKey).(string)
	if !ok || cacheKey == "" {
		if plugin.config.DefaultCacheKey != "" {
			cacheKey = plugin.config.DefaultCacheKey
			plugin.logger.Debug(PluginLoggerPrefix + " Using default cache key: " + cacheKey)
		} else {
			plugin.logger.Debug(PluginLoggerPrefix + " No cache key found in context, continuing without caching")
			return req, nil, nil
		}
	}

	if plugin.isConversationHistoryThresholdExceeded(req) {
		plugin.logger.Debug(PluginLoggerPrefix + " Skipping caching for request with conversation history threshold exceeded")
		return req, nil, nil
	}

	// Generate UUID for this request
	requestID := uuid.New().String()

	// Store request ID, model, and provider in context for PostLLMHook
	ctx.SetValue(requestIDKey, requestID)
	ctx.SetValue(requestModelKey, model)
	ctx.SetValue(requestProviderKey, provider)

	performDirectSearch, performSemanticSearch := true, true
	if ctx.Value(CacheTypeKey) != nil {
		cacheTypeVal, ok := ctx.Value(CacheTypeKey).(CacheType)
		if !ok {
			plugin.logger.Warn(PluginLoggerPrefix + " Cache type is not a CacheType, using all available cache types")
		} else {
			performDirectSearch = cacheTypeVal == CacheTypeDirect
			performSemanticSearch = cacheTypeVal == CacheTypeSemantic
		}
	}

	if performDirectSearch {
		shortCircuit, err := plugin.performDirectSearch(ctx, req, cacheKey)
		if err != nil {
			plugin.logger.Warn(PluginLoggerPrefix + " Direct search failed: " + err.Error())
			// Don't return - continue to semantic search fallback
			shortCircuit = nil // Ensure we don't use an invalid shortCircuit
		}

		if shortCircuit != nil {
			return req, shortCircuit, nil
		}
	}

	if performSemanticSearch && plugin.client != nil {
		if req.EmbeddingRequest != nil || req.TranscriptionRequest != nil {
			plugin.logger.Debug(PluginLoggerPrefix + " Skipping semantic search for embedding/transcription input")
			// For vector stores that require vectors, set a zero vector placeholder
			// This allows direct hash matching to work without the overhead of generating embeddings
			if plugin.store.RequiresVectors() && plugin.config.Dimension > 0 {
				zeroVector := make([]float32, plugin.config.Dimension)
				ctx.SetValue(requestEmbeddingKey, zeroVector)
				plugin.logger.Debug(PluginLoggerPrefix + " Using zero vector placeholder for embedding/transcription request storage")
			}
			return req, nil, nil
		}

		// Try semantic search as fallback
		shortCircuit, err := plugin.performSemanticSearch(ctx, req, cacheKey)
		if err != nil {
			return req, nil, nil
		}

		if shortCircuit != nil {
			return req, shortCircuit, nil
		}
	} else if !performSemanticSearch && plugin.store.RequiresVectors() && plugin.client != nil {
		// Vector store requires vectors but we're in direct-only mode
		// Generate embeddings for storage purposes (not for searching)
		if req.EmbeddingRequest != nil || req.TranscriptionRequest != nil {
			plugin.logger.Debug(PluginLoggerPrefix + " Skipping embedding generation for embedding/transcription input")
			// For vector stores that require vectors, set a zero vector placeholder
			// This allows direct hash matching to work without the overhead of generating embeddings
			if plugin.config.Dimension > 0 {
				zeroVector := make([]float32, plugin.config.Dimension)
				ctx.SetValue(requestEmbeddingKey, zeroVector)
				plugin.logger.Debug(PluginLoggerPrefix + " Using zero vector placeholder for embedding/transcription request storage")
			}
			return req, nil, nil
		}

		// Use zero vector for direct-only cache type to prevent semantic search matches
		// This preserves cache type isolation - direct-only entries won't be found by semantic search
		if plugin.config.Dimension > 0 {
			zeroVector := make([]float32, plugin.config.Dimension)
			ctx.SetValue(requestEmbeddingKey, zeroVector)
			plugin.logger.Debug(PluginLoggerPrefix + " Using zero vector for direct-only cache storage (preserves isolation)")
		}
	}

	return req, nil, nil
}

// PostLLMHook is called after a response is received from a provider.
// It caches responses in the VectorStore using UUID-based keys with unified metadata structure
// including provider, model, request hash, and TTL. Handles both single and streaming responses.
//
// The function performs the following operations:
// 1. Checks configurable caching behavior and skips caching for unsuccessful responses if configured
// 2. Retrieves the request hash and ID from the context (set during PreLLMHook)
// 3. Marshals the response for storage
// 4. Stores the unified cache entry in the VectorStore asynchronously (non-blocking)
//
// The VectorStore Add operation runs in a separate goroutine to avoid blocking the response.
// The function gracefully handles errors and continues without caching if any step fails,
// ensuring that response processing is never interrupted by caching issues.
//
// Parameters:
//   - ctx: Pointer to the schemas.BifrostContext containing the request hash and ID
//   - res: The response from the provider to be cached
//   - bifrostErr: The error from the provider, if any (used for success determination)
//
// Returns:
//   - *schemas.BifrostResponse: The original response, unmodified
//   - *schemas.BifrostError: The original error, unmodified
//   - error: Any error that occurred during caching preparation (always nil as errors are handled gracefully)
func (plugin *Plugin) PostLLMHook(ctx *schemas.BifrostContext, res *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if bifrostErr != nil {
		return res, bifrostErr, nil
	}

	isCacheHit := ctx.Value(isCacheHitKey)
	if isCacheHit != nil {
		isCacheHitValue, ok := isCacheHit.(bool)
		if ok && isCacheHitValue {
			return res, nil, nil
		}
	}

	// Check if caching is explicitly disabled
	noStore := ctx.Value(CacheNoStoreKey)
	if noStore != nil {
		noStoreValue, ok := noStore.(bool)
		if ok && noStoreValue {
			plugin.logger.Debug(PluginLoggerPrefix + " Caching is explicitly disabled for this request, continuing without caching")
			return res, nil, nil
		}
	}

	// Get the cache key from context
	cacheKey, ok := ctx.Value(CacheKey).(string)
	if !ok || cacheKey == "" {
		if plugin.config.DefaultCacheKey != "" {
			cacheKey = plugin.config.DefaultCacheKey
		} else {
			return res, nil, nil
		}
	}

	// Get the request ID from context
	requestID, ok := ctx.Value(requestIDKey).(string)
	if !ok {
		return res, nil, nil
	}
	// Check cache type to optimize embedding handling
	var embedding []float32
	var hash string
	var shouldStoreEmbeddings = true
	var shouldStoreHash = true

	if ctx.Value(CacheTypeKey) != nil {
		cacheTypeVal, ok := ctx.Value(CacheTypeKey).(CacheType)
		if ok {
			if cacheTypeVal == CacheTypeDirect {
				// For direct-only caching, skip embedding operations entirely
				// unless the vector store requires vectors for all entries
				if plugin.store.RequiresVectors() {
					// Vector stores like Qdrant and Pinecone require vectors for all entries
					// Keep embeddings enabled for storage, but lookups will still use direct hash matching
					plugin.logger.Debug(PluginLoggerPrefix + " Vector store requires vectors, keeping embedding generation enabled for storage")
				} else {
					shouldStoreEmbeddings = false
					plugin.logger.Debug(PluginLoggerPrefix + " Skipping embedding operations for direct-only cache type")
				}
			} else if cacheTypeVal == CacheTypeSemantic {
				shouldStoreHash = false
				plugin.logger.Debug(PluginLoggerPrefix + " Skipping hash operations for semantic cache type")
			}
		}
	}

	if shouldStoreHash {
		// Get the hash from context
		hash, ok = ctx.Value(requestHashKey).(string)
		if !ok {
			plugin.logger.Warn(PluginLoggerPrefix + " Hash is not a string. Continuing without caching")
			return res, nil, nil
		}
	}

	extraFields := res.GetExtraFields()
	requestType := extraFields.RequestType

	// Get embedding from context if available and needed
	// For embedding/transcription requests, we still need to retrieve the zero vector placeholder
	// if the vector store requires vectors for all entries
	isEmbeddingOrTranscription := requestType == schemas.EmbeddingRequest || requestType == schemas.TranscriptionRequest
	needsEmbedding := shouldStoreEmbeddings && !isEmbeddingOrTranscription
	needsZeroVector := isEmbeddingOrTranscription && plugin.store.RequiresVectors()

	if needsEmbedding || needsZeroVector {
		embeddingValue := ctx.Value(requestEmbeddingKey)
		if embeddingValue != nil {
			embedding, ok = embeddingValue.([]float32)
			if !ok {
				plugin.logger.Warn(PluginLoggerPrefix + " Embedding is not a []float32, continuing without caching")
				return res, nil, nil
			}
		}
		// Note: embedding can be nil for direct cache hits or when semantic search is disabled
		// This is fine - we can still cache using direct hash matching (unless store requires vectors)
	}

	// Get the provider from context
	provider, ok := ctx.Value(requestProviderKey).(schemas.ModelProvider)
	if !ok {
		plugin.logger.Warn(PluginLoggerPrefix + " Provider is not a schemas.ModelProvider, continuing without caching")
		return res, nil, nil
	}

	// Get the model from context
	model, ok := ctx.Value(requestModelKey).(string)
	if !ok {
		plugin.logger.Warn(PluginLoggerPrefix + " Model is not a string, continuing without caching")
		return res, nil, nil
	}

	isFinalChunk := bifrost.IsFinalChunk(ctx)

	// Get the input tokens from context (can be nil if not set)
	inputTokens, ok := ctx.Value(requestEmbeddingTokensKey).(int)
	if ok {
		isStreamRequest := bifrost.IsStreamRequestType(requestType)

		if !isStreamRequest || (isStreamRequest && isFinalChunk) {
			if extraFields.CacheDebug == nil {
				extraFields.CacheDebug = &schemas.BifrostCacheDebug{}
			}
			extraFields.CacheDebug.CacheHit = false
			extraFields.CacheDebug.ProviderUsed = bifrost.Ptr(string(plugin.config.Provider))
			extraFields.CacheDebug.ModelUsed = bifrost.Ptr(plugin.config.EmbeddingModel)
			extraFields.CacheDebug.InputTokens = &inputTokens
		}
	}

	cacheTTL := plugin.config.TTL

	ttlValue := ctx.Value(CacheTTLKey)
	if ttlValue != nil {
		// Get the request TTL from the context
		ttl, ok := ttlValue.(time.Duration)
		if !ok {
			plugin.logger.Warn(PluginLoggerPrefix + " TTL is not a time.Duration, using default TTL")
		} else {
			cacheTTL = ttl
		}
	}

	// Get metadata from context BEFORE goroutine to avoid race conditions
	// when the same context is reused across multiple requests
	paramsHash, _ := ctx.Value(requestParamsHashKey).(string)

	// Cache everything in a unified VectorEntry asynchronously to avoid blocking the response
	plugin.waitGroup.Add(1)
	go func() {
		defer plugin.waitGroup.Done()
		// Create a background context with timeout for the cache operation
		cacheCtx, cancel := context.WithTimeout(context.Background(), CacheSetTimeout)
		defer cancel()

		// Build unified metadata with provider, model, and all params
		unifiedMetadata := plugin.buildUnifiedMetadata(provider, model, paramsHash, hash, cacheKey, cacheTTL)

		// Handle streaming vs non-streaming responses
		// Pass nil for embedding if we're in direct-only mode to optimize storage
		embeddingToStore := embedding
		if !shouldStoreEmbeddings {
			embeddingToStore = nil
		}

		if bifrost.IsStreamRequestType(requestType) {
			if err := plugin.addStreamingResponse(cacheCtx, requestID, res, bifrostErr, embeddingToStore, unifiedMetadata, cacheTTL, isFinalChunk); err != nil {
				plugin.logger.Warn("%s Failed to cache streaming response: %v", PluginLoggerPrefix, err)
			}
		} else {
			if err := plugin.addSingleResponse(cacheCtx, requestID, res, embeddingToStore, unifiedMetadata, cacheTTL); err != nil {
				plugin.logger.Warn("%s Failed to cache single response: %v", PluginLoggerPrefix, err)
			}
		}
	}()

	return res, nil, nil
}

// WaitForPendingOperations blocks until all pending cache operations (goroutines) complete.
// This is useful in tests to ensure cache entries are stored before checking for cache hits.
func (plugin *Plugin) WaitForPendingOperations() {
	plugin.waitGroup.Wait()
}

// Cleanup performs cleanup operations for the semantic cache plugin.
// It removes all cached entries created by this plugin from the VectorStore only if CleanUpOnShutdown is true.
// Identifies cache entries by the presence of semantic cache-specific fields (request_hash, cache_key).
//
// The function performs the following operations:
// 1. Checks if cleanup is enabled via CleanUpOnShutdown config
// 2. Retrieves all entries and filters client-side to identify cache entries
// 3. Deletes all matching cache entries from the VectorStore in batches
//
// This method should be called when shutting down the application to ensure
// proper resource cleanup if configured to do so.
//
// Returns:
//   - error: Any error that occurred during cleanup operations
func (plugin *Plugin) Cleanup() error {
	plugin.waitGroup.Wait()

	// Clean up old stream accumulators first
	plugin.cleanupOldStreamAccumulators()

	// Shutdown the internal Bifrost client used for embeddings
	if plugin.client != nil {
		plugin.client.Shutdown()
	}

	// Only clean up cache entries if configured to do so
	if !plugin.config.CleanUpOnShutdown {
		plugin.logger.Debug(PluginLoggerPrefix + " Cleanup on shutdown is disabled, skipping cache cleanup")
		return nil
	}

	// Clean up all cache entries created by this plugin
	ctx, cancel := context.WithTimeout(context.Background(), CacheSetTimeout)
	defer cancel()

	plugin.logger.Debug(PluginLoggerPrefix + " Starting cleanup of cache entries...")

	// Delete all cache entries created by this plugin
	queries := []vectorstore.Query{
		{
			Field:    "from_bifrost_semantic_cache_plugin",
			Operator: vectorstore.QueryOperatorEqual,
			Value:    true,
		},
	}

	results, err := plugin.store.DeleteAll(ctx, plugin.config.VectorStoreNamespace, queries)
	if err != nil {
		return fmt.Errorf("failed to delete cache entries: %w", err)
	}

	for _, result := range results {
		if result.Status == vectorstore.DeleteStatusError {
			plugin.logger.Warn("%s Failed to delete cache entry: %s", PluginLoggerPrefix, result.Error)
		}
	}
	plugin.logger.Info("%s Cleanup completed - deleted all cache entries", PluginLoggerPrefix)

	if err := plugin.store.DeleteNamespace(ctx, plugin.config.VectorStoreNamespace); err != nil {
		return fmt.Errorf("failed to delete namespace: %w", err)
	}

	return nil
}

// Public Methods for External Use

// ClearCacheForKey deletes cache entries for a specific cache key.
// Uses the unified VectorStore interface for deletion of all entries with the given cache key.
//
// Parameters:
//   - cacheKey: The specific cache key to delete
//
// Returns:
//   - error: Any error that occurred during cache key deletion
func (plugin *Plugin) ClearCacheForKey(cacheKey string) error {
	// Delete all entries with "cache_key" equal to the given cacheKey
	queries := []vectorstore.Query{
		{
			Field:    "cache_key",
			Operator: vectorstore.QueryOperatorEqual,
			Value:    cacheKey,
		},
		{
			Field:    "from_bifrost_semantic_cache_plugin",
			Operator: vectorstore.QueryOperatorEqual,
			Value:    true,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), CacheSetTimeout)
	defer cancel()
	results, err := plugin.store.DeleteAll(ctx, plugin.config.VectorStoreNamespace, queries)
	if err != nil {
		plugin.logger.Warn("%s Failed to delete cache entries for key '%s': %v", PluginLoggerPrefix, cacheKey, err)
		return err
	}

	for _, result := range results {
		if result.Status == vectorstore.DeleteStatusError {
			plugin.logger.Warn("%s Failed to delete cache entry for key %s: %s", PluginLoggerPrefix, result.ID, result.Error)
		}
	}

	plugin.logger.Debug(fmt.Sprintf("%s Deleted all cache entries for key %s", PluginLoggerPrefix, cacheKey))

	return nil
}

// ClearCacheForRequestID deletes cache entries for a specific request ID.
// Uses the unified VectorStore interface to delete the single entry by its UUID.
//
// Parameters:
//   - requestID: The UUID-based request ID to delete cache entries for
//
// Returns:
//   - error: Any error that occurred during cache key deletion
func (plugin *Plugin) ClearCacheForRequestID(requestID string) error {
	// With the unified VectorStore interface, we delete the single entry by its UUID
	ctx, cancel := context.WithTimeout(context.Background(), CacheSetTimeout)
	defer cancel()
	if err := plugin.store.Delete(ctx, plugin.config.VectorStoreNamespace, requestID); err != nil {
		plugin.logger.Warn("%s Failed to delete cache entry: %v", PluginLoggerPrefix, err)
		return err
	}

	plugin.logger.Debug(fmt.Sprintf("%s Deleted cache entry for key %s", PluginLoggerPrefix, requestID))

	return nil
}
