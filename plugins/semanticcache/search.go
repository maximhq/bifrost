package semanticcache

import (
	"context"
	"errors"
	"fmt"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

func (plugin *Plugin) performDirectSearch(ctx *context.Context, req *schemas.BifrostRequest, requestType bifrost.RequestType) (*schemas.PluginShortCircuit, error) {
	// Generate hash for the request
	hash, err := plugin.generateRequestHash(req, requestType)
	if err != nil {
		return nil, fmt.Errorf("failed to generate request hash: %w", err)
	}

	// Store hash in context
	*ctx = context.WithValue(*ctx, requestHashKey, hash)

	// Look for cached hash first using pattern-based search
	hashPattern := plugin.generateCachePattern(req, "", "hash")

	// Get all hash keys matching the pattern
	var hashKeys []string
	var cursor *string
	for {
		batch, c, err := plugin.store.GetAll(*ctx, hashPattern, cursor, 1000)
		if err != nil {
			return nil, fmt.Errorf("failed to scan hash keys: %w", err)
		}
		hashKeys = append(hashKeys, batch...)
		cursor = c
		if cursor == nil {
			break
		}
	}

	if len(hashKeys) == 0 {
		return nil, nil
	}

	// Get all hash values
	hashData, err := plugin.store.GetChunks(*ctx, hashKeys)
	if err != nil {
		if errors.Is(err, vectorstore.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to retrieve cached hashes: %w", err)
	}

	// Find matching hash
	var matchingRequestID string
	for i, data := range hashData {
		if data != nil {
			storedHash, ok := data.(string)
			if !ok {
				plugin.logger.Debug(PluginLoggerPrefix + " Cached hash value is not a string; skipping key")
				continue
			}

			if storedHash == hash {
				// Extract request ID from the hash key
				// Hash key format: {prefix}{provider}-{model}-reqid-{uuid}-hash
				hashKey := hashKeys[i]

				// Remove prefix first
				keyWithoutPrefix := strings.TrimPrefix(hashKey, plugin.config.Prefix)

				// Look for "reqid-" pattern and extract UUID after it
				reqidIndex := strings.Index(keyWithoutPrefix, "-reqid-")
				if reqidIndex != -1 {
					// Extract everything after "-reqid-"
					afterReqid := keyWithoutPrefix[reqidIndex+7:] // 7 = len("-reqid-")

					// Remove the suffix ("-hash", "-emb", "-response")
					if strings.HasSuffix(afterReqid, "-hash") {
						matchingRequestID = strings.TrimSuffix(afterReqid, "-hash")
						break
					}
				}
			}
		}
	}

	if matchingRequestID == "" {
		return nil, nil
	}

	if plugin.isStreamingRequest(requestType) {
		return plugin.getStreamingResponseForRequestID(ctx, req, matchingRequestID, CacheTypeDirect)
	} else {
		return plugin.getNonStreamingResponseForRequestID(ctx, req, matchingRequestID, CacheTypeDirect)
	}
}

// performSemanticSearch performs semantic similarity search and returns matching response if found.
func (plugin *Plugin) performSemanticSearch(ctx *context.Context, req *schemas.BifrostRequest, requestType bifrost.RequestType) (*schemas.PluginShortCircuit, error) {
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
		threshold, ok := (*ctx).Value(ContextKey(plugin.config.CacheThresholdKey)).(float64)
		if !ok {
			plugin.logger.Warn(PluginLoggerPrefix + " Threshold is not a float64, using default threshold")
		} else {
			cacheThreshold = threshold
		}
	}

	// For semantic search, we want semantic similarity in content but exact parameter matching
	results, err := plugin.store.SearchSemanticCache(*ctx, SemanticIndexName, embedding, metadata, cacheThreshold, 1)
	if err != nil {
		// Handle unsupported operations as soft misses (e.g., Redis Cluster without RediSearch)
		if errors.Is(err, vectorstore.ErrNotSupported) {
			return nil, fmt.Errorf("semantic search not supported by vectorstore")
		}
		return nil, fmt.Errorf("failed to search semantic cache: %w", err)
	}

	if len(results) == 0 {
		return nil, nil
	}

	// Extract request ID from the similar embedding key
	// Embedding key format: {plugin_prefix}{provider}-{model}-reqid-{uuid}-emb
	embeddingKey := results[0].Key
	keyWithoutPrefix := strings.TrimPrefix(embeddingKey, plugin.config.Prefix)

	// Look for "reqid-" pattern and extract UUID after it
	reqidIndex := strings.Index(keyWithoutPrefix, "-reqid-")
	if reqidIndex == -1 {
		return nil, fmt.Errorf("invalid embedding key format, missing reqid: %s", embeddingKey)
	}

	// Extract everything after "-reqid-"
	afterReqid := keyWithoutPrefix[reqidIndex+7:] // 7 = len("-reqid-")
	// Remove the suffix ("-emb")
	similarRequestID := strings.TrimSuffix(afterReqid, "-emb")

	// Look up the corresponding response
	if plugin.isStreamingRequest(requestType) {
		return plugin.getStreamingResponseForRequestID(ctx, req, similarRequestID, CacheTypeSemantic)
	} else {
		return plugin.getNonStreamingResponseForRequestID(ctx, req, similarRequestID, CacheTypeSemantic)
	}
}
