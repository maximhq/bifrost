package semanticcache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

func (plugin *Plugin) performDirectSearch(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, cacheKey string) (*schemas.PluginShortCircuit, error) {
	// Generate hash for the request
	hash, err := plugin.generateRequestHash(req)
	if err != nil {
		return nil, fmt.Errorf("failed to generate request hash: %w", err)
	}

	plugin.logger.Debug(PluginLoggerPrefix + " Generated Hash for Request: " + hash)

	// Extract metadata for strict filtering
	_, paramsHash, err := plugin.extractTextForEmbedding(req)
	if err != nil {
		return nil, fmt.Errorf("failed to extract metadata for filtering: %w", err)
	}

	// Store has and metadata in context
	ctx.SetValue(requestHashKey, hash)
	ctx.SetValue(requestParamsHashKey, paramsHash)

	provider, model, _ := req.GetRequestFields()

	// Build strict filters for direct hash search
	filters := []vectorstore.Query{
		{Field: "request_hash", Operator: vectorstore.QueryOperatorEqual, Value: hash},
		{Field: "cache_key", Operator: vectorstore.QueryOperatorEqual, Value: cacheKey},
		{Field: "params_hash", Operator: vectorstore.QueryOperatorEqual, Value: paramsHash},
		{Field: "from_bifrost_semantic_cache_plugin", Operator: vectorstore.QueryOperatorEqual, Value: true},
	}

	if plugin.config.CacheByProvider != nil && *plugin.config.CacheByProvider {
		filters = append(filters, vectorstore.Query{Field: "provider", Operator: vectorstore.QueryOperatorEqual, Value: string(provider)})
	}
	if plugin.config.CacheByModel != nil && *plugin.config.CacheByModel {
		filters = append(filters, vectorstore.Query{Field: "model", Operator: vectorstore.QueryOperatorEqual, Value: model})
	}

	plugin.logger.Debug(fmt.Sprintf("%s Searching for direct hash match with %d filters", PluginLoggerPrefix, len(filters)))

	// Make a full copy so we don't mutate the original backing array
	selectFields := append([]string(nil), SelectFields...)
	if bifrost.IsStreamRequestType(req.RequestType) {
		selectFields = removeField(selectFields, "response")
	} else {
		selectFields = removeField(selectFields, "stream_chunks")
	}

	// Search for entries with matching hash and all params
	var cursor *string
	results, _, err := plugin.store.GetAll(ctx, plugin.config.VectorStoreNamespace, filters, selectFields, cursor, 1)
	if err != nil {
		if errors.Is(err, vectorstore.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to search for direct hash match: %w", err)
	}

	if len(results) == 0 {
		plugin.logger.Debug(PluginLoggerPrefix + " No direct hash match found")
		return nil, nil
	}

	// Found a matching entry - extract the response
	result := results[0]
	plugin.logger.Debug(fmt.Sprintf("%s Found direct hash match with ID: %s", PluginLoggerPrefix, result.ID))

	// Build response from cached result
	return plugin.buildResponseFromResult(ctx, req, result, CacheTypeDirect, 1.0, 0)
}

// generateEmbeddingsForStorage generates embeddings and stores them in context for PostHook storage.
// This is used when the vector store requires vectors but we're in direct-only cache mode.
// Unlike performSemanticSearch, this function does not perform any search - it only generates
// and stores embeddings so they can be persisted with the cache entry.
func (plugin *Plugin) generateEmbeddingsForStorage(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	// Extract text and metadata for embedding
	text, paramsHash, err := plugin.extractTextForEmbedding(req)
	if err != nil {
		return fmt.Errorf("failed to extract text for embedding: %w", err)
	}

	// Generate embedding
	embedding, inputTokens, err := plugin.generateEmbedding(ctx, text)
	if err != nil {
		return fmt.Errorf("failed to generate embedding: %w", err)
	}

	// Store embedding and metadata in context for PostHook
	ctx.SetValue(requestEmbeddingKey, embedding)
	ctx.SetValue(requestEmbeddingTokensKey, inputTokens)
	ctx.SetValue(requestParamsHashKey, paramsHash)

	return nil
}

// performSemanticSearch performs semantic similarity search and returns matching response if found.
func (plugin *Plugin) performSemanticSearch(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, cacheKey string) (*schemas.PluginShortCircuit, error) {
	// Extract text and metadata for embedding
	text, paramsHash, err := plugin.extractTextForEmbedding(req)
	if err != nil {
		return nil, fmt.Errorf("failed to extract text for embedding: %w", err)
	}

	// Generate embedding
	embedding, inputTokens, err := plugin.generateEmbedding(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("failed to generate embedding: %w", err)
	}

	// Store embedding and metadata in context for PostHook
	ctx.SetValue(requestEmbeddingKey, embedding)
	ctx.SetValue(requestEmbeddingTokensKey, inputTokens)
	ctx.SetValue(requestParamsHashKey, paramsHash)

	cacheThreshold := plugin.config.Threshold

	thresholdValue := ctx.Value(CacheThresholdKey)
	if thresholdValue != nil {
		threshold, ok := thresholdValue.(float64)
		if !ok {
			plugin.logger.Warn(PluginLoggerPrefix + " Threshold is not a float64, using default threshold")
		} else {
			cacheThreshold = threshold
		}
	}

	provider, model, _ := req.GetRequestFields()

	// Build strict metadata filters as Query slices (provider, model, and all params)
	strictFilters := []vectorstore.Query{
		{Field: "cache_key", Operator: vectorstore.QueryOperatorEqual, Value: cacheKey},
		{Field: "params_hash", Operator: vectorstore.QueryOperatorEqual, Value: paramsHash},
		{Field: "from_bifrost_semantic_cache_plugin", Operator: vectorstore.QueryOperatorEqual, Value: true},
	}

	if plugin.config.CacheByProvider != nil && *plugin.config.CacheByProvider {
		strictFilters = append(strictFilters, vectorstore.Query{Field: "provider", Operator: vectorstore.QueryOperatorEqual, Value: string(provider)})
	}
	if plugin.config.CacheByModel != nil && *plugin.config.CacheByModel {
		strictFilters = append(strictFilters, vectorstore.Query{Field: "model", Operator: vectorstore.QueryOperatorEqual, Value: model})
	}

	plugin.logger.Debug(fmt.Sprintf("%s Performing semantic search with %d metadata filters", PluginLoggerPrefix, len(strictFilters)))

	// Make a full copy so we don't mutate the original backing array
	selectFields := append([]string(nil), SelectFields...)
	if bifrost.IsStreamRequestType(req.RequestType) {
		selectFields = removeField(selectFields, "response")
	} else {
		selectFields = removeField(selectFields, "stream_chunks")
	}

	// For semantic search, we want semantic similarity in content but exact parameter matching
	results, err := plugin.store.GetNearest(ctx, plugin.config.VectorStoreNamespace, embedding, strictFilters, selectFields, cacheThreshold, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to search semantic cache: %w", err)
	}

	if len(results) == 0 {
		plugin.logger.Debug(PluginLoggerPrefix + " No semantic match found")
		return nil, nil
	}

	// Found a semantically similar entry
	result := results[0]
	plugin.logger.Debug(fmt.Sprintf("%s Found semantic match with ID: %s, Score: %f", PluginLoggerPrefix, result.ID, *result.Score))

	// Build response from cached result
	return plugin.buildResponseFromResult(ctx, req, result, CacheTypeSemantic, cacheThreshold, inputTokens)
}

// buildResponseFromResult constructs a PluginShortCircuit response from a cached VectorEntry result
func (plugin *Plugin) buildResponseFromResult(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, result vectorstore.SearchResult, cacheType CacheType, threshold float64, inputTokens int) (*schemas.PluginShortCircuit, error) {
	// Extract response data from the result properties
	properties := result.Properties
	if properties == nil {
		return nil, fmt.Errorf("no properties found in cached result")
	}

	// Check TTL - if entry has expired, delete it and return cache miss
	if expiresAtRaw, exists := properties["expires_at"]; exists && expiresAtRaw != nil {
		var expiresAt int64
		var validType bool
		switch v := expiresAtRaw.(type) {
		case string:
			var err error
			expiresAt, err = strconv.ParseInt(v, 10, 64)
			if err != nil {
				validType = false
			} else {
				validType = true
			}
		case float64:
			expiresAt = int64(v)
			validType = true
		case int64:
			expiresAt = v
			validType = true
		case int:
			expiresAt = int64(v)
			validType = true
		}
		if validType {
			currentTime := time.Now().Unix()
			if expiresAt < currentTime {
				// Entry has expired, delete it asynchronously
				go func() {
					deleteCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					err := plugin.store.Delete(deleteCtx, plugin.config.VectorStoreNamespace, result.ID)
					if err != nil {
						plugin.logger.Warn("%s Failed to delete expired entry %s: %v", PluginLoggerPrefix, result.ID, err)
					}
				}()
				// Return nil to indicate cache miss
				return nil, nil
			}
		}
	}

	// Check if this is a streaming response - need to check for non-null values
	streamResponses, hasStreamingResponse := properties["stream_chunks"]
	singleResponse, hasSingleResponse := properties["response"]

	// Consider fields present only if they're not null
	hasValidSingleResponse := hasSingleResponse && singleResponse != nil
	hasValidStreamingResponse := hasStreamingResponse && streamResponses != nil

	// Parse stream_chunks
	streamChunks, err := plugin.parseStreamChunks(streamResponses)
	if err != nil || len(streamChunks) == 0 {
		hasValidStreamingResponse = false
	}

	similarity := 0.0
	if result.Score != nil {
		similarity = *result.Score
	}

	if hasValidStreamingResponse && !hasValidSingleResponse {
		// Handle streaming response
		return plugin.buildStreamingResponseFromResult(ctx, req, result, streamResponses, cacheType, threshold, similarity, inputTokens)
	} else if hasValidSingleResponse && !hasValidStreamingResponse {
		// Handle single response
		return plugin.buildSingleResponseFromResult(ctx, req, result, singleResponse, cacheType, threshold, similarity, inputTokens)
	} else {
		return nil, fmt.Errorf("cached result has invalid response data: both or neither response/stream_chunks are present (response: %v, stream_chunks: %v)", singleResponse, streamResponses)
	}
}

// buildSingleResponseFromResult constructs a single response from cached data
func (plugin *Plugin) buildSingleResponseFromResult(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, result vectorstore.SearchResult, responseData interface{}, cacheType CacheType, threshold float64, similarity float64, inputTokens int) (*schemas.PluginShortCircuit, error) {
	provider, _, _ := req.GetRequestFields()

	responseStr, ok := responseData.(string)
	if !ok {
		return nil, fmt.Errorf("cached response is not a string")
	}

	// Unmarshal the cached response
	var cachedResponse schemas.BifrostResponse
	if err := json.Unmarshal([]byte(responseStr), &cachedResponse); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cached response: %w", err)
	}

	extraFields := cachedResponse.GetExtraFields()

	if extraFields.CacheDebug == nil {
		extraFields.CacheDebug = &schemas.BifrostCacheDebug{}
	}
	extraFields.CacheDebug.CacheHit = true
	extraFields.CacheDebug.HitType = bifrost.Ptr(string(cacheType))
	extraFields.CacheDebug.CacheID = bifrost.Ptr(result.ID)
	if cacheType == CacheTypeSemantic {
		extraFields.CacheDebug.ProviderUsed = bifrost.Ptr(string(plugin.config.Provider))
		extraFields.CacheDebug.ModelUsed = bifrost.Ptr(plugin.config.EmbeddingModel)
		extraFields.CacheDebug.Threshold = &threshold
		extraFields.CacheDebug.Similarity = &similarity
		extraFields.CacheDebug.InputTokens = &inputTokens
	} else {
		extraFields.CacheDebug.ProviderUsed = nil
		extraFields.CacheDebug.ModelUsed = nil
		extraFields.CacheDebug.Threshold = nil
		extraFields.CacheDebug.Similarity = nil
		extraFields.CacheDebug.InputTokens = nil
	}

	extraFields.Provider = provider

	ctx.SetValue(isCacheHitKey, true)
	ctx.SetValue(cacheHitTypeKey, cacheType)

	return &schemas.PluginShortCircuit{
		Response: &cachedResponse,
	}, nil
}

// buildStreamingResponseFromResult constructs a streaming response from cached data
func (plugin *Plugin) buildStreamingResponseFromResult(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, result vectorstore.SearchResult, streamData interface{}, cacheType CacheType, threshold float64, similarity float64, inputTokens int) (*schemas.PluginShortCircuit, error) {
	provider, _, _ := req.GetRequestFields()

	// Parse stream_chunks
	streamArray, err := plugin.parseStreamChunks(streamData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse stream_chunks: %w", err)
	}

	// Mark cache-hit once to avoid concurrent ctx writes
	ctx.SetValue(isCacheHitKey, true)
	ctx.SetValue(cacheHitTypeKey, cacheType)

	// Create stream channel
	streamChan := make(chan *schemas.BifrostStreamChunk)

	go func() {
		defer close(streamChan)

		// Set cache-hit markers inside the streaming goroutine to avoid races
		ctx.SetValue(isCacheHitKey, true)
		ctx.SetValue(cacheHitTypeKey, cacheType)

		// Process each stream chunk
		for i, chunkData := range streamArray {
			chunkStr, ok := chunkData.(string)
			if !ok {
				plugin.logger.Warn("%s Stream chunk %d is not a string, skipping", PluginLoggerPrefix, i)
				continue
			}

			// Unmarshal the chunk as BifrostResponse
			var cachedResponse schemas.BifrostResponse
			if err := json.Unmarshal([]byte(chunkStr), &cachedResponse); err != nil {
				plugin.logger.Warn("%s Failed to unmarshal stream chunk %d, skipping: %v", PluginLoggerPrefix, i, err)
				continue
			}

			extraFields := cachedResponse.GetExtraFields()

			// Add cache debug to only the last chunk and set stream end indicator
			if i == len(streamArray)-1 {
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				cacheDebug := schemas.BifrostCacheDebug{
					CacheHit: true,
					HitType:  bifrost.Ptr(string(cacheType)),
					CacheID:  bifrost.Ptr(result.ID),
				}
				if cacheType == CacheTypeSemantic {
					cacheDebug.ProviderUsed = bifrost.Ptr(string(plugin.config.Provider))
					cacheDebug.ModelUsed = bifrost.Ptr(plugin.config.EmbeddingModel)
					cacheDebug.Threshold = &threshold
					cacheDebug.Similarity = &similarity
					cacheDebug.InputTokens = &inputTokens
				} else {
					cacheDebug.ProviderUsed = nil
					cacheDebug.ModelUsed = nil
					cacheDebug.Threshold = nil
					cacheDebug.Similarity = nil
					cacheDebug.InputTokens = nil
				}
				extraFields.CacheDebug = &cacheDebug
			}

			// extraField is a pointer so it'll automatically reflect on the parent struct
			extraFields.Provider = provider

			// Send chunk to stream
			streamChan <- &schemas.BifrostStreamChunk{
				BifrostTextCompletionResponse:        cachedResponse.TextCompletionResponse,
				BifrostChatResponse:                  cachedResponse.ChatResponse,
				BifrostResponsesStreamResponse:       cachedResponse.ResponsesStreamResponse,
				BifrostSpeechStreamResponse:          cachedResponse.SpeechStreamResponse,
				BifrostTranscriptionStreamResponse:   cachedResponse.TranscriptionStreamResponse,
				BifrostImageGenerationStreamResponse: cachedResponse.ImageGenerationStreamResponse,
			}
		}
	}()

	return &schemas.PluginShortCircuit{
		Stream: streamChan,
	}, nil
}

// parseStreamChunks parses stream_chunks data from various formats into []interface{}
// Handles []interface{}, []string, and JSON string formats
func (plugin *Plugin) parseStreamChunks(streamData interface{}) ([]interface{}, error) {
	if streamData == nil {
		return nil, fmt.Errorf("stream data is nil")
	}

	switch v := streamData.(type) {
	case []interface{}:
		return v, nil
	case []string:
		// Convert []string to []interface{}
		result := make([]interface{}, len(v))
		for i, s := range v {
			result[i] = s
		}
		return result, nil
	case string:
		// Parse JSON string from Redis
		var stringArray []string
		if err := json.Unmarshal([]byte(v), &stringArray); err != nil {
			return nil, fmt.Errorf("failed to parse JSON string: %w", err)
		}
		// Convert to []interface{}
		result := make([]interface{}, len(stringArray))
		for i, s := range stringArray {
			result[i] = s
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported stream data type: %T", streamData)
	}
}
