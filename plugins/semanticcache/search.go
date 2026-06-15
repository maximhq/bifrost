package semanticcache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// performDirectSearch does an O(1) point fetch on the deterministic directCacheID
// derived from (provider, model, cacheKey, request_hash, params_hash). Caller
// supplies the prebuilt metadata + paramsHash so we don't recompute them when
// semantic search runs as well.
func (plugin *Plugin) performDirectSearch(ctx *schemas.BifrostContext, state *cacheState, req *schemas.BifrostRequest, cacheKey string, metadata map[string]interface{}, paramsHash string) (*schemas.LLMPluginShortCircuit, error) {
	requestHash, err := plugin.generateRequestHash(req, metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to generate request hash: %w", err)
	}

	provider, model, _ := req.GetRequestFields()
	directCacheID, err := plugin.generateDirectCacheID(provider, model, cacheKey, requestHash, paramsHash)
	if err != nil {
		return nil, fmt.Errorf("failed to generate direct cache ID: %w", err)
	}
	state.DirectCacheID = directCacheID

	// All filters (cacheKey, provider, model, requestHash, paramsHash) are
	// encoded into directCacheID, so a Get-by-ID is sufficient.
	result, err := plugin.store.GetChunk(ctx, plugin.config.VectorStoreNamespace, directCacheID)
	if err != nil {
		errMsg := strings.ToLower(err.Error())
		isMiss := errors.Is(err, vectorstore.ErrNotFound) ||
			strings.Contains(errMsg, "not found") ||
			strings.Contains(errMsg, "status code: 404")
		if isMiss {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to fetch direct cache chunk: %w", err)
	}
	return plugin.buildResponseFromResult(ctx, state, req, result, CacheTypeDirect, nil, nil)
}

// performSemanticSearch performs semantic similarity search and returns matching response if found.
// Caller supplies the prebuilt paramsHash so it isn't recomputed.
func (plugin *Plugin) performSemanticSearch(ctx *schemas.BifrostContext, state *cacheState, req *schemas.BifrostRequest, cacheKey string, paramsHash string) (*schemas.LLMPluginShortCircuit, error) {
	text, err := plugin.extractTextForEmbedding(state, req)
	if err != nil {
		return nil, fmt.Errorf("failed to extract text for embedding: %w", err)
	}

	embedding, inputTokens, err := plugin.generateEmbedding(ctx, text)
	if err != nil {
		// Note: silent skip — provider misconfig or transient embedding errors
		// fall through to the upstream LLM call.
		return nil, fmt.Errorf("failed to generate embedding: %w", err)
	}

	state.Embeddings = embedding
	state.EmbeddingsInputTokens = inputTokens

	cacheThreshold := plugin.config.Threshold
	if v := ctx.Value(CacheThresholdKey); v != nil {
		if threshold, ok := v.(float64); ok {
			cacheThreshold = threshold
		} else {
			plugin.logger.Warn("Threshold is not a float64, using default threshold")
		}
	}

	provider, model, _ := req.GetRequestFields()
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

	selectFields := selectFieldsForRequest(req.RequestType)
	results, err := plugin.store.GetNearest(ctx, plugin.config.VectorStoreNamespace, embedding, strictFilters, selectFields, cacheThreshold, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to search semantic cache: %w", err)
	}
	if len(results) == 0 {
		return nil, nil
	}
	return plugin.buildResponseFromResult(ctx, state, req, results[0], CacheTypeSemantic, &cacheThreshold, &inputTokens)
}

// selectFieldsStream / selectFieldsNonStream are precomputed at package init
// because selectFieldsForRequest is called on every cache lookup.
var (
	selectFieldsStream    = filterSelectFields("response")
	selectFieldsNonStream = filterSelectFields("stream_chunks")
)

// filterSelectFields returns SelectFields with the named field removed. Used
// at package init to precompute the per-request projection lists.
func filterSelectFields(skip string) []string {
	out := make([]string, 0, len(SelectFields))
	for _, f := range SelectFields {
		if f != skip {
			out = append(out, f)
		}
	}
	return out
}

// selectFieldsForRequest returns the projection list trimmed to the response
// shape we actually need (single response vs stream chunks).
func selectFieldsForRequest(requestType schemas.RequestType) []string {
	if bifrost.IsStreamRequestType(requestType) {
		return selectFieldsStream
	}
	return selectFieldsNonStream
}

// generateEmbedding generates an embedding for the given text using the configured provider.
func (plugin *Plugin) generateEmbedding(ctx *schemas.BifrostContext, text string) ([]float32, int, error) {
	embeddingReq := &schemas.BifrostEmbeddingRequest{
		Provider: plugin.config.Provider,
		Model:    plugin.config.EmbeddingModel,
		Input: &schemas.EmbeddingInput{
			Text: &text,
		},
	}

	embeddingCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	// Cancel the derived context once we're done. NewBifrostContext starts a
	// watchCancellation goroutine that holds a reference to ctx (the scoped
	// plugin context). Without this, that goroutine outlives the plugin call
	// and may dereference fields on a parent context that has already been
	// released back to its sync.Pool — see core/schemas.ReleasePluginScope.
	defer embeddingCtx.Cancel()
	embeddingCtx.SetValue(schemas.BifrostContextKeySkipPluginPipeline, true)
	if plugin.embeddingRequestExecutor == nil {
		return nil, 0, fmt.Errorf("embedding request executor is not configured")
	}
	response, err := plugin.embeddingRequestExecutor(embeddingCtx, embeddingReq)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to generate embedding: %v", err)
	}

	if len(response.Data) == 0 {
		return nil, 0, fmt.Errorf("no embeddings returned from provider")
	}

	embedding := response.Data[0].Embedding
	inputTokens := 0
	if response.Usage != nil {
		inputTokens = response.Usage.TotalTokens
	}

	switch {
	case embedding.EmbeddingStr != nil:
		var vals []float32
		if err := json.Unmarshal([]byte(*embedding.EmbeddingStr), &vals); err != nil {
			return nil, 0, fmt.Errorf("failed to parse string embedding: %w", err)
		}
		return vals, inputTokens, nil
	case embedding.EmbeddingArray != nil:
		return float64ToFloat32Embedding(embedding.EmbeddingArray), inputTokens, nil
	case len(embedding.Embedding2DArray) > 0:
		return flattenToFloat32Embedding(embedding.Embedding2DArray), inputTokens, nil
	case embedding.EmbeddingInt8Array != nil:
		// Quantized int8/binary embedding format. Promote to float32 so the
		// cosine-similarity path treats it uniformly.
		return int8ToFloat32Embedding(embedding.EmbeddingInt8Array), inputTokens, nil
	case embedding.EmbeddingInt32Array != nil:
		return int32ToFloat32Embedding(embedding.EmbeddingInt32Array), inputTokens, nil
	}
	return nil, 0, fmt.Errorf("embedding data is not in expected format")
}

// generateRequestHash creates an xxhash of the (normalized input, params).
// Fallbacks are excluded since they only affect error handling.
func (plugin *Plugin) generateRequestHash(req *schemas.BifrostRequest, params map[string]interface{}) (string, error) {
	hashInput := map[string]interface{}{
		"input":  plugin.getNormalizedInputForCaching(req),
		"params": params,
	}
	jsonData, err := schemas.MarshalDeeplySorted(hashInput)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request for hashing: %w", err)
	}
	return fmt.Sprintf("%x", xxhash.Sum64(jsonData)), nil
}

// generateDirectCacheID returns a deterministic UUIDv5 derived from the cache
// key, request hash, params hash, and (optionally) provider/model. The same
// inputs always produce the same ID, which is what makes the direct path an
// O(1) point fetch.
func (plugin *Plugin) generateDirectCacheID(provider schemas.ModelProvider, model string, cacheKey string, requestHash string, paramsHash string) (string, error) {
	idInput := struct {
		CacheKey    string `json:"cache_key"`
		RequestHash string `json:"request_hash"`
		ParamsHash  string `json:"params_hash"`
		Provider    string `json:"provider,omitempty"`
		Model       string `json:"model,omitempty"`
	}{
		CacheKey:    cacheKey,
		RequestHash: requestHash,
		ParamsHash:  paramsHash,
	}
	if plugin.config.CacheByProvider != nil && *plugin.config.CacheByProvider {
		idInput.Provider = string(provider)
	}
	if plugin.config.CacheByModel != nil && *plugin.config.CacheByModel {
		idInput.Model = model
	}
	data, err := schemas.MarshalDeeplySorted(idInput)
	if err != nil {
		return "", err
	}
	return uuid.NewSHA1(directCacheNamespace, data).String(), nil
}

// buildResponseFromResult constructs a LLMPluginShortCircuit response from a cached VectorEntry result.
//
// Return contract:
//   - (shortCircuit, nil): cache hit — caller should return shortCircuit to short-circuit upstream.
//   - (nil, nil): treat as a miss. Used for both genuine misses and "soft" misses
//     (expired entry, unparseable expires_at, format mismatch). Caller proceeds to upstream.
//   - (nil, err): hard error worth logging; caller logs and proceeds to upstream.
func (plugin *Plugin) buildResponseFromResult(ctx *schemas.BifrostContext, state *cacheState, req *schemas.BifrostRequest, result vectorstore.SearchResult, cacheType CacheType, threshold *float64, inputTokens *int) (*schemas.LLMPluginShortCircuit, error) {
	properties := result.Properties
	if properties == nil {
		return nil, fmt.Errorf("no properties found in cached result")
	}

	if expired, miss := isExpiredEntry(properties); expired {
		// Async best-effort cleanup of the stale entry. Tracked on writersWg
		// so WaitForPendingOperations + Cleanup block until it finishes,
		// avoiding a delete racing with namespace teardown.
		plugin.writersWg.Add(1)
		go func() {
			defer plugin.writersWg.Done()
			deleteCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := plugin.store.Delete(deleteCtx, plugin.config.VectorStoreNamespace, result.ID); err != nil {
				plugin.logger.Warn("Failed to delete expired entry %s: %v", result.ID, err)
			}
		}()
		return nil, nil
	} else if miss {
		// Unparseable expires_at — treat as miss to be safe.
		return nil, nil
	}

	similarity := 0.0
	if result.Score != nil {
		similarity = *result.Score
	}

	isStream := bifrost.IsStreamRequestType(req.RequestType)
	if isStream {
		streamResponses, ok := properties["stream_chunks"]
		if ok && streamResponses != nil {
			streamChunks, err := plugin.parseStreamChunks(streamResponses)
			if err == nil && len(streamChunks) > 0 {
				return plugin.buildStreamingResponseFromResult(ctx, state, req, result, streamChunks, cacheType, threshold, &similarity, inputTokens)
			}
		}
	} else {
		singleResponse, ok := properties["response"]
		if ok && singleResponse != nil {
			return plugin.buildNonStreamingResponseFromResult(ctx, state, req, result, singleResponse, cacheType, threshold, &similarity, inputTokens)
		}
	}

	msg := fmt.Sprintf("cache entry %s format mismatch (isStream=%t), treating as miss — entry may be corrupt", result.ID, isStream)
	plugin.logger.Warn(msg)
	ctx.Log(schemas.LogLevelWarn, msg)
	return nil, nil
}

// isExpiredEntry returns (expired, parseFailed). A nil/missing expires_at is
// treated as never-expires.
func isExpiredEntry(properties map[string]interface{}) (bool, bool) {
	expiresAtRaw, exists := properties["expires_at"]
	if !exists || expiresAtRaw == nil {
		return false, false
	}
	var expiresAt int64
	switch v := expiresAtRaw.(type) {
	case string:
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return false, true
		}
		expiresAt = parsed
	case float64:
		expiresAt = int64(v)
	case int64:
		expiresAt = v
	case int:
		expiresAt = int64(v)
	default:
		return false, true
	}
	return expiresAt < time.Now().Unix(), false
}

// buildNonStreamingResponseFromResult constructs a single response from cached data.
func (plugin *Plugin) buildNonStreamingResponseFromResult(ctx *schemas.BifrostContext, state *cacheState, req *schemas.BifrostRequest, result vectorstore.SearchResult, responseData interface{}, cacheType CacheType, threshold *float64, similarity *float64, inputTokens *int) (*schemas.LLMPluginShortCircuit, error) {
	requestedProvider, requestedModel, _ := req.GetRequestFields()

	responseStr, ok := responseData.(string)
	if !ok {
		return nil, fmt.Errorf("cached response is not a string")
	}
	var cachedResponse schemas.BifrostResponse
	if err := json.Unmarshal([]byte(responseStr), &cachedResponse); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cached response: %w", err)
	}

	plugin.stampCacheDebugForHit(state, cachedResponse.GetExtraFields(), result.ID, requestedProvider, requestedModel, cacheType, threshold, similarity, inputTokens)
	state.ShortCircuited = true
	return &schemas.LLMPluginShortCircuit{Response: &cachedResponse}, nil
}

// buildStreamingResponseFromResult constructs a streaming response from cached data.
// The replay goroutine guards every send with ctx.Done() so a dropped consumer
// can't leak the goroutine (and its captured chunks) for the lifetime of the
// process.
func (plugin *Plugin) buildStreamingResponseFromResult(ctx *schemas.BifrostContext, state *cacheState, req *schemas.BifrostRequest, result vectorstore.SearchResult, streamArray []string, cacheType CacheType, threshold *float64, similarity *float64, inputTokens *int) (*schemas.LLMPluginShortCircuit, error) {
	requestedProvider, requestedModel, _ := req.GetRequestFields()
	streamChan := make(chan *schemas.BifrostStreamChunk)
	done := ctx.Done()

	// We deliberately do NOT pre-decode all chunks up front — that would
	// add O(N) latency before the first chunk is delivered, defeating the
	// purpose of streaming for long responses. A malformed chunk is
	// extremely unlikely (we wrote it as JSON ourselves), and on the rare
	// occasion it happens we log+skip rather than truncate the user's view.
	go func() {
		defer close(streamChan)
		for i, chunkStr := range streamArray {
			var cachedResponse schemas.BifrostResponse
			if err := json.Unmarshal([]byte(chunkStr), &cachedResponse); err != nil {
				plugin.logger.Warn("Failed to unmarshal stream chunk %d, skipping: %v", i, err)
				continue
			}

			// Ensure RequestType is set on every chunk so downstream consumers
			// (logging, telemetry) correctly identify this as a streaming response.
			if ef := cachedResponse.GetExtraFields(); ef != nil && ef.RequestType == "" {
				ef.RequestType = req.RequestType
			}

			if i == len(streamArray)-1 {
				// stampCacheDebugForHit marks this chunk as the cache-hit final
				// chunk; cache.PostLLMHook keys off CacheDebug.CacheHit=true to
				// set BifrostContextKeyStreamEndIndicator on the root ctx
				// synchronously (same goroutine as logging.PostLLMHook).
				//
				// We deliberately do NOT call ctx.Root().SetValue here. Doing
				// so races against the receiver's PostLLMHook for the previous
				// chunk: the cache replay can advance to iteration N (and
				// write the indicator) while the receiver is still running
				// PostLLMHooks for chunk N-1, poisoning that chunk's
				// IsFinalChunk read and causing duplicate "final" events.
				plugin.stampCacheDebugForHit(state, cachedResponse.GetExtraFields(), result.ID, requestedProvider, requestedModel, cacheType, threshold, similarity, inputTokens)
			}

			chunk := &schemas.BifrostStreamChunk{
				BifrostTextCompletionResponse:        cachedResponse.TextCompletionResponse,
				BifrostChatResponse:                  cachedResponse.ChatResponse,
				BifrostResponsesStreamResponse:       cachedResponse.ResponsesStreamResponse,
				BifrostSpeechStreamResponse:          cachedResponse.SpeechStreamResponse,
				BifrostTranscriptionStreamResponse:   cachedResponse.TranscriptionStreamResponse,
				BifrostImageGenerationStreamResponse: cachedResponse.ImageGenerationStreamResponse,
			}

			select {
			case streamChan <- chunk:
			case <-done:
				return
			}
		}
	}()

	state.ShortCircuited = true
	return &schemas.LLMPluginShortCircuit{Stream: streamChan}, nil
}

// stampCacheDebugForHit stamps the cache-hit telemetry on the response. For
// CacheTypeDirect, the embedding-related fields are explicitly cleared so
// stale carry-over from semantic hits never leaks through. CacheHitLatency
// is computed from state.CreatedAt (set at PreLLMHook entry) so consumers
// can distinguish cache-serve time from the original provider latency
// preserved in the cached response.
func (plugin *Plugin) stampCacheDebugForHit(
	state *cacheState,
	extraFields *schemas.BifrostResponseExtraFields,
	cacheID string,
	requestedProvider schemas.ModelProvider,
	requestedModel string,
	cacheType CacheType,
	threshold *float64,
	similarity *float64,
	inputTokens *int,
) {
	// GetExtraFields() can return nil for older/corrupted cache entries that
	// were written without ExtraFields populated. Bail rather than panic —
	// the chunk will still be delivered, just without CacheDebug telemetry.
	if extraFields == nil {
		return
	}
	if extraFields.CacheDebug == nil {
		extraFields.CacheDebug = &schemas.BifrostCacheDebug{}
	}
	cd := extraFields.CacheDebug
	cd.CacheHit = true
	cd.HitType = bifrost.Ptr(string(cacheType))
	cd.CacheID = bifrost.Ptr(cacheID)
	cd.RequestedProvider = bifrost.Ptr(string(requestedProvider))
	cd.RequestedModel = bifrost.Ptr(requestedModel)
	cd.CacheHitLatency = bifrost.Ptr(time.Since(state.CreatedAt).Milliseconds())
	if cacheType == CacheTypeSemantic {
		cd.ProviderUsed = bifrost.Ptr(string(plugin.config.Provider))
		cd.ModelUsed = bifrost.Ptr(plugin.config.EmbeddingModel)
		cd.Threshold = threshold
		cd.Similarity = similarity
		cd.InputTokens = inputTokens
	} else {
		cd.ProviderUsed = nil
		cd.ModelUsed = nil
		cd.Threshold = nil
		cd.Similarity = nil
		cd.InputTokens = nil
	}
}
