package semanticcache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

func (plugin *Plugin) performDirectSearch(ctx *context.Context, req *schemas.BifrostRequest, requestType bifrost.RequestType, cacheKey string) (*schemas.PluginShortCircuit, error) {
	// Generate hash for the request
	hash, err := plugin.generateRequestHash(req, requestType)
	if err != nil {
		return nil, fmt.Errorf("failed to generate request hash: %w", err)
	}

	plugin.logger.Debug(PluginLoggerPrefix + " Generated Hash for Request: " + hash)

	// Store hash in context
	*ctx = context.WithValue(*ctx, requestHashKey, hash)

	// Extract metadata for strict filtering
	_, metadata, err := plugin.extractTextForEmbedding(req, requestType)
	if err != nil {
		return nil, fmt.Errorf("failed to extract metadata for filtering: %w", err)
	}

	// Build strict filters for direct hash search
	filters := []vectorstore.Query{
		{Field: "request_hash", Operator: vectorstore.QueryOperatorEqual, Value: hash},
		{Field: "cache_key", Operator: vectorstore.QueryOperatorEqual, Value: cacheKey},
	}

	if plugin.config.CacheByProvider != nil && *plugin.config.CacheByProvider {
		filters = append(filters, vectorstore.Query{Field: "provider", Operator: vectorstore.QueryOperatorEqual, Value: string(req.Provider)})
	}
	if plugin.config.CacheByModel != nil && *plugin.config.CacheByModel {
		filters = append(filters, vectorstore.Query{Field: "model", Operator: vectorstore.QueryOperatorEqual, Value: req.Model})
	}

	// Add strict filters for ALL params
	for key, value := range metadata {
		filters = append(filters, vectorstore.Query{
			Field:    "params." + key,
			Operator: vectorstore.QueryOperatorEqual,
			Value:    value,
		})
	}

	plugin.logger.Debug(fmt.Sprintf("%s Searching for direct hash match with %d filters", PluginLoggerPrefix, len(filters)))

	// Make a full copy so we don't mutate the original backing array
	selectFields := append([]string(nil), SelectFields...)
	if plugin.isStreamingRequest(requestType) {
		selectFields = removeField(selectFields, "response")
	} else {
		selectFields = removeField(selectFields, "stream_chunks")
	}

	// Search for entries with matching hash and all params
	var cursor *string
	results, _, err := plugin.store.GetAll(*ctx, filters, selectFields, cursor, 1)
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
	return plugin.buildResponseFromResult(ctx, req, result, CacheTypeDirect, 1.0)
}

// performSemanticSearch performs semantic similarity search and returns matching response if found.
func (plugin *Plugin) performSemanticSearch(ctx *context.Context, req *schemas.BifrostRequest, requestType bifrost.RequestType, cacheKey string) (*schemas.PluginShortCircuit, error) {
	// Extract text and metadata for embedding
	text, metadata, err := plugin.extractTextForEmbedding(req, requestType)
	if err != nil {
		return nil, fmt.Errorf("failed to extract text for embedding: %w", err)
	}

	// Generate embedding
	embedding, err := plugin.generateEmbedding(*ctx, text)
	if err != nil {
		return nil, fmt.Errorf("failed to generate embedding: %w", err)
	}

	// Store embedding and metadata in context for PostHook
	*ctx = context.WithValue(*ctx, requestEmbeddingKey, embedding)
	*ctx = context.WithValue(*ctx, requestMetadataKey, metadata)

	cacheThreshold := plugin.config.Threshold

	if plugin.config.CacheThresholdKey != "" {
		thresholdValue := (*ctx).Value(ContextKey(plugin.config.CacheThresholdKey))
		if thresholdValue != nil {
			threshold, ok := thresholdValue.(float64)
			if !ok {
				plugin.logger.Warn(PluginLoggerPrefix + " Threshold is not a float64, using default threshold")
			} else {
				cacheThreshold = threshold
			}
		}
	}

	// Build strict metadata filters as Query slices (provider, model, and all params)
	strictFilters := []vectorstore.Query{
		{Field: "cache_key", Operator: vectorstore.QueryOperatorEqual, Value: cacheKey},
	}

	if plugin.config.CacheByProvider != nil && *plugin.config.CacheByProvider {
		strictFilters = append(strictFilters, vectorstore.Query{Field: "provider", Operator: vectorstore.QueryOperatorEqual, Value: string(req.Provider)})
	}
	if plugin.config.CacheByModel != nil && *plugin.config.CacheByModel {
		strictFilters = append(strictFilters, vectorstore.Query{Field: "model", Operator: vectorstore.QueryOperatorEqual, Value: req.Model})
	}

	// Add all params as strict filters
	for key, value := range metadata {
		strictFilters = append(strictFilters, vectorstore.Query{
			Field:    "params." + key,
			Operator: vectorstore.QueryOperatorEqual,
			Value:    value,
		})
	}

	plugin.logger.Debug(fmt.Sprintf("%s Performing semantic search with %d metadata filters", PluginLoggerPrefix, len(strictFilters)))

	// Make a full copy so we don't mutate the original backing array
	selectFields := append([]string(nil), SelectFields...)
	if plugin.isStreamingRequest(requestType) {
		selectFields = removeField(selectFields, "response")
	} else {
		selectFields = removeField(selectFields, "stream_chunks")
	}

	// For semantic search, we want semantic similarity in content but exact parameter matching
	results, err := plugin.store.GetNearest(*ctx, embedding, strictFilters, selectFields, cacheThreshold, 1)
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
	return plugin.buildResponseFromResult(ctx, req, result, CacheTypeSemantic, cacheThreshold)
}

// buildResponseFromResult constructs a PluginShortCircuit response from a cached VectorEntry result
func (plugin *Plugin) buildResponseFromResult(ctx *context.Context, req *schemas.BifrostRequest, result vectorstore.SearchResult, cacheType CacheType, threshold float64) (*schemas.PluginShortCircuit, error) {
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
					err := plugin.store.Delete(deleteCtx, result.ID)
					if err != nil {
						plugin.logger.Warn(fmt.Sprintf("%s Failed to delete expired entry %s: %v", PluginLoggerPrefix, result.ID, err))
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

	streamChunks, ok := streamResponses.([]interface{})
	if !ok || len(streamChunks) == 0 {
		hasValidStreamingResponse = false
	}

	similarity := 0.0
	if result.Score != nil {
		similarity = *result.Score
	}

	if hasValidStreamingResponse && !hasValidSingleResponse {
		// Handle streaming response
		return plugin.buildStreamingResponseFromResult(ctx, req, result, streamResponses, cacheType, threshold, similarity)
	} else if hasValidSingleResponse && !hasValidStreamingResponse {
		// Handle single response
		return plugin.buildSingleResponseFromResult(ctx, req, result, singleResponse, cacheType, threshold, similarity)
	} else {
		return nil, fmt.Errorf("cached result has invalid response data: both or neither response/stream_chunks are present (response: %v, stream_chunks: %v)", singleResponse, streamResponses)
	}
}

// buildSingleResponseFromResult constructs a single response from cached data
func (plugin *Plugin) buildSingleResponseFromResult(ctx *context.Context, req *schemas.BifrostRequest, result vectorstore.SearchResult, responseData interface{}, cacheType CacheType, threshold float64, similarity float64) (*schemas.PluginShortCircuit, error) {
	responseStr, ok := responseData.(string)
	if !ok {
		return nil, fmt.Errorf("cached response is not a string")
	}

	// Unmarshal the cached response
	var cachedResponse schemas.BifrostResponse
	if err := json.Unmarshal([]byte(responseStr), &cachedResponse); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cached response: %w", err)
	}

	if cachedResponse.ExtraFields.CacheDebug == nil {
		cachedResponse.ExtraFields.CacheDebug = &schemas.BifrostCacheDebug{}
	}
	cachedResponse.ExtraFields.CacheDebug.CacheHit = true
	cachedResponse.ExtraFields.CacheDebug.CacheHitType = string(cacheType)
	cachedResponse.ExtraFields.CacheDebug.CacheID = result.ID
	if cacheType == CacheTypeSemantic {
		cachedResponse.ExtraFields.CacheDebug.CacheThreshold = &threshold
		cachedResponse.ExtraFields.CacheDebug.CacheSimilarity = &similarity
	} else {
		cachedResponse.ExtraFields.CacheDebug.CacheThreshold = nil
		cachedResponse.ExtraFields.CacheDebug.CacheSimilarity = nil
	}

	cachedResponse.ExtraFields.Provider = req.Provider

	*ctx = context.WithValue(*ctx, isCacheHitKey, true)
	*ctx = context.WithValue(*ctx, CacheHitTypeKey, cacheType)

	return &schemas.PluginShortCircuit{
		Response: &cachedResponse,
	}, nil
}

// buildStreamingResponseFromResult constructs a streaming response from cached data
func (plugin *Plugin) buildStreamingResponseFromResult(ctx *context.Context, req *schemas.BifrostRequest, result vectorstore.SearchResult, streamData interface{}, cacheType CacheType, threshold float64, similarity float64) (*schemas.PluginShortCircuit, error) {
	streamArray, ok := streamData.([]interface{})
	if !ok {
		return nil, fmt.Errorf("cached stream_chunks is not an array")
	}

	// Create stream channel
	streamChan := make(chan *schemas.BifrostStream)

	go func() {
		defer close(streamChan)

		// Process each stream chunk
		for i, chunkData := range streamArray {
			chunkStr, ok := chunkData.(string)
			if !ok {
				plugin.logger.Warn(fmt.Sprintf("%s Stream chunk %d is not a string, skipping", PluginLoggerPrefix, i))
				continue
			}

			// Unmarshal the chunk as BifrostResponse
			var cachedResponse schemas.BifrostResponse
			if err := json.Unmarshal([]byte(chunkStr), &cachedResponse); err != nil {
				plugin.logger.Warn(fmt.Sprintf("%s Failed to unmarshal stream chunk %d, skipping: %v", PluginLoggerPrefix, i, err))
				continue
			}

			if cachedResponse.ExtraFields.CacheDebug == nil {
				cachedResponse.ExtraFields.CacheDebug = &schemas.BifrostCacheDebug{}
			}
			cachedResponse.ExtraFields.CacheDebug.CacheHit = true
			cachedResponse.ExtraFields.CacheDebug.CacheHitType = string(cacheType)
			cachedResponse.ExtraFields.CacheDebug.CacheID = result.ID
			if cacheType == CacheTypeSemantic {
				cachedResponse.ExtraFields.CacheDebug.CacheThreshold = &threshold
				cachedResponse.ExtraFields.CacheDebug.CacheSimilarity = &similarity
			} else {
				cachedResponse.ExtraFields.CacheDebug.CacheThreshold = nil
				cachedResponse.ExtraFields.CacheDebug.CacheSimilarity = nil
			}

			cachedResponse.ExtraFields.Provider = req.Provider

			// Send chunk to stream
			streamChan <- &schemas.BifrostStream{
				BifrostResponse: &cachedResponse,
			}
		}
	}()

	*ctx = context.WithValue(*ctx, isCacheHitKey, true)
	*ctx = context.WithValue(*ctx, CacheHitTypeKey, cacheType)

	return &schemas.PluginShortCircuit{
		Stream: streamChan,
	}, nil
}
