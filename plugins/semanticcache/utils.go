package semanticcache

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// directCacheNamespace is a fixed namespace UUID for generating deterministic
// UUID v5 cache IDs via uuid.NewSHA1, used by generateDirectCacheID. The
// bytes are arbitrary — they only need to be stable across restarts so the
// same (cache_key, request_hash, params_hash) tuple maps to the same ID.
var directCacheNamespace = uuid.MustParse("b1f3c2d4-e5a6-7890-abcd-ef1234567890")

// isSemanticCacheSupportedRequestType reports whether semantic cache supports
// this request type for cache lookup and storage. Unsupported types are skipped.
//
// IMPORTANT: this list must stay in sync with the switch in buildRequestMetadataForCaching.
// When adding a new case there, add it here too.
func isSemanticCacheSupportedRequestType(requestType schemas.RequestType) bool {
	switch requestType {
	case schemas.TextCompletionRequest,
		schemas.TextCompletionStreamRequest,
		schemas.ChatCompletionRequest,
		schemas.ChatCompletionStreamRequest,
		schemas.ResponsesRequest,
		schemas.ResponsesStreamRequest,
		schemas.WebSocketResponsesRequest,
		schemas.SpeechRequest,
		schemas.SpeechStreamRequest,
		schemas.EmbeddingRequest,
		schemas.TranscriptionRequest,
		schemas.TranscriptionStreamRequest,
		schemas.ImageGenerationRequest,
		schemas.ImageGenerationStreamRequest:
		return true
	default:
		return false
	}
}

// hashSortedSet returns a deterministic hex hash for an order-insensitive
// list of items. Some request fields are semantically sets but JSON-encoded
// as lists (most notably Tools, where MCP's randomized map iteration would
// otherwise perturb the request hash). The caller supplies a key extractor
// because shapes differ across fields (e.g. ChatTool.Function.Name vs
// ResponsesTool.Name). Use this for set-shaped fields large enough to be
// worth compressing; for short []string sets, prefer sortedStringSet which
// keeps the metadata human-debuggable.
func hashSortedSet[T any](items []T, key func(T) string) (string, error) {
	if len(items) == 0 {
		return "", nil
	}
	sorted := make([]T, len(items))
	copy(sorted, items)
	sort.SliceStable(sorted, func(i, j int) bool {
		return key(sorted[i]) < key(sorted[j])
	})
	payload := make([]any, len(sorted))
	for i, t := range sorted {
		payload[i] = t
	}
	itemsJSON, err := schemas.MarshalDeeplySorted(payload)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", xxhash.Sum64(itemsJSON)), nil
}

// hashMap returns a deterministic xxhash hex digest of the map. Uses
// MarshalDeeplySorted because plain json.Marshal doesn't guarantee key
// ordering on Go maps.
func hashMap(m map[string]interface{}) (string, error) {
	jsonData, err := schemas.MarshalDeeplySorted(m)
	if err != nil {
		return "", fmt.Errorf("failed to marshal metadata for metadata hash: %w", err)
	}
	return fmt.Sprintf("%x", xxhash.Sum64(jsonData)), nil
}

// sortedStringSet returns a sorted copy of a string slice that is semantically
// a set (e.g. modalities, stop sequences, include flags). Sorting in place
// would mutate the caller's parameters, so a copy is returned.
func sortedStringSet(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sorted := make([]string, len(values))
	copy(sorted, values)
	sort.Strings(sorted)
	return sorted
}

// putIfSet writes m[key] = *v when v is non-nil. Used by extract*ParametersToMetadata
// to collapse the if-nil-set boilerplate that dominates those functions.
func putIfSet[T any](m map[string]any, key string, v *T) {
	if v != nil {
		m[key] = *v
	}
}

// putSortedSetIfNonEmpty writes m[key] = sortedStringSet(values) when values
// has any entries — otherwise leaves the key absent so the resulting metadata
// hash treats "unset" and "empty" identically.
func putSortedSetIfNonEmpty(m map[string]any, key string, values []string) {
	if len(values) > 0 {
		m[key] = sortedStringSet(values)
	}
}

// normalizeText applies consistent normalization to text inputs for better cache hit rates.
// It converts text to lowercase and trims whitespace to reduce cache misses due to minor variations.
func normalizeText(text string) string {
	return strings.ToLower(strings.TrimSpace(text))
}

// float64ToFloat32Embedding converts a []float64 to a []float32. The semantic cache
// keeps vector payloads as float32 even though the embedding APIs now
// preserve full float64 precision — the cosine similarity used at query
// time is well within float32 range.
func float64ToFloat32Embedding(values []float64) []float32 {
	if len(values) == 0 {
		return nil
	}

	embedding := make([]float32, len(values))
	for i, value := range values {
		embedding[i] = float32(value)
	}

	return embedding
}

// int8ToFloat32Embedding promotes a quantized int8 embedding (used for
// binary/quantized formats by some providers) to float32 so the cache can
// store and compare it uniformly against float32 entries.
func int8ToFloat32Embedding(values []int8) []float32 {
	if len(values) == 0 {
		return nil
	}
	embedding := make([]float32, len(values))
	for i, value := range values {
		embedding[i] = float32(value)
	}
	return embedding
}

// int32ToFloat32Embedding promotes a uint8/ubinary-style int32 embedding to
// float32 for the same reason as int8ToFloat32Embedding.
func int32ToFloat32Embedding(values []int32) []float32 {
	if len(values) == 0 {
		return nil
	}
	embedding := make([]float32, len(values))
	for i, value := range values {
		embedding[i] = float32(value)
	}
	return embedding
}

// flattenToFloat32Embedding concatenates a 2D embedding (one inner slice per
// input chunk) into a single flat []float32. Used when the provider returns
// per-chunk embeddings that we want to store as a single vector.
func flattenToFloat32Embedding(values [][]float64) []float32 {
	total := 0
	for _, arr := range values {
		total += len(arr)
	}
	if total == 0 {
		return nil
	}

	embedding := make([]float32, 0, total)
	for _, arr := range values {
		embedding = append(embedding, float64ToFloat32Embedding(arr)...)
	}

	return embedding
}

// buildRequestMetadataForCaching extracts the canonical, hashable parameter
// set for the request: anything that should change the cache key when it
// changes. The returned map is fed to hashMap to derive params_hash, which
// then anchors both direct and semantic lookups.
func (plugin *Plugin) buildRequestMetadataForCaching(state *cacheState, req *schemas.BifrostRequest) (map[string]interface{}, error) {
	metadata := map[string]interface{}{
		"stream": bifrost.IsStreamRequestType(req.RequestType),
	}

	if attachments := plugin.extractAttachmentsForCaching(state, req); len(attachments) > 0 {
		metadata["attachments"] = attachments
	}

	switch req.RequestType {
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		if req.TextCompletionRequest == nil {
			return nil, fmt.Errorf("text completion payload is nil")
		}
		if req.TextCompletionRequest != nil && req.TextCompletionRequest.Params != nil {
			plugin.extractTextCompletionParametersToMetadata(req.TextCompletionRequest.Params, metadata)
		}
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		if req.ChatRequest == nil {
			return nil, fmt.Errorf("chat payload is nil")
		}
		if req.ChatRequest != nil && req.ChatRequest.Params != nil {
			plugin.extractChatParametersToMetadata(req.ChatRequest.Params, metadata)
		}
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest, schemas.WebSocketResponsesRequest:
		if req.ResponsesRequest == nil {
			return nil, fmt.Errorf("responses payload is nil")
		}
		if req.ResponsesRequest != nil && req.ResponsesRequest.Params != nil {
			plugin.extractResponsesParametersToMetadata(req.ResponsesRequest.Params, metadata)
		}
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		if req.SpeechRequest == nil {
			return nil, fmt.Errorf("speech payload is nil")
		}
		if req.SpeechRequest != nil && req.SpeechRequest.Params != nil {
			plugin.extractSpeechParametersToMetadata(req.SpeechRequest.Params, metadata)
		}
	case schemas.EmbeddingRequest:
		if req.EmbeddingRequest == nil {
			return nil, fmt.Errorf("embedding payload is nil")
		}
		if req.EmbeddingRequest != nil && req.EmbeddingRequest.Params != nil {
			plugin.extractEmbeddingParametersToMetadata(req.EmbeddingRequest.Params, metadata)
		}
	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		if req.TranscriptionRequest == nil {
			return nil, fmt.Errorf("transcription payload is nil")
		}
		if req.TranscriptionRequest != nil && req.TranscriptionRequest.Params != nil {
			plugin.extractTranscriptionParametersToMetadata(req.TranscriptionRequest.Params, metadata)
		}
	case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest:
		if req.ImageGenerationRequest == nil {
			return nil, fmt.Errorf("image generation payload is nil")
		}
		if req.ImageGenerationRequest != nil && req.ImageGenerationRequest.Params != nil {
			plugin.extractImageGenerationParametersToMetadata(req.ImageGenerationRequest.Params, metadata)
		}
	default:
		return nil, fmt.Errorf("unsupported request type for semantic caching")
	}

	return metadata, nil
}

// extractAttachmentsForCaching collects image/file URLs referenced by the
// request input in document order. Attachments are part of the cache key —
// two messages with identical text but different images must not collide.
// Honors ExcludeSystemPrompt via getInputForCaching. Returns nil for
// request types without attachment-bearing content blocks.
func (plugin *Plugin) extractAttachmentsForCaching(state *cacheState, req *schemas.BifrostRequest) []string {
	switch req.RequestType {
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		messages, ok := plugin.getInputForCaching(state, req).([]schemas.ChatMessage)
		if !ok {
			return nil
		}
		var attachments []string
		for _, msg := range messages {
			if msg.Content == nil || msg.Content.ContentBlocks == nil {
				continue
			}
			for _, block := range msg.Content.ContentBlocks {
				if block.ImageURLStruct != nil && block.ImageURLStruct.URL != "" {
					attachments = append(attachments, block.ImageURLStruct.URL)
				}
			}
		}
		return attachments
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest, schemas.WebSocketResponsesRequest:
		messages, ok := plugin.getInputForCaching(state, req).([]schemas.ResponsesMessage)
		if !ok {
			return nil
		}
		var attachments []string
		for _, msg := range messages {
			if msg.Content == nil || msg.Content.ContentBlocks == nil {
				continue
			}
			for _, block := range msg.Content.ContentBlocks {
				if block.ResponsesInputMessageContentBlockImage != nil && block.ResponsesInputMessageContentBlockImage.ImageURL != nil {
					attachments = append(attachments, *block.ResponsesInputMessageContentBlockImage.ImageURL)
				}
				if block.ResponsesInputMessageContentBlockFile != nil && block.ResponsesInputMessageContentBlockFile.FileURL != nil {
					attachments = append(attachments, *block.ResponsesInputMessageContentBlockFile.FileURL)
				}
			}
		}
		return attachments
	}
	return nil
}

// extractChatMessageContent flattens a ChatMessage's content (string or
// blocks) into a single space-joined string. Returns "" when the message
// carries no text (e.g. assistant tool-call messages with nil content).
func extractChatMessageContent(msg schemas.ChatMessage) string {
	if msg.Content == nil {
		return ""
	}
	if msg.Content.ContentStr != nil {
		return *msg.Content.ContentStr
	}
	if msg.Content.ContentBlocks != nil {
		var parts []string
		for _, block := range msg.Content.ContentBlocks {
			if block.Text != nil {
				parts = append(parts, *block.Text)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// extractResponsesMessageContent flattens a ResponsesMessage's content into a
// single string, mirroring extractChatMessageContent but for the Responses API.
func extractResponsesMessageContent(msg schemas.ResponsesMessage) string {
	if msg.Content == nil {
		return ""
	}
	if msg.Content.ContentStr != nil {
		return *msg.Content.ContentStr
	}
	if msg.Content.ContentBlocks != nil {
		var parts []string
		for _, block := range msg.Content.ContentBlocks {
			if block.Text != nil {
				parts = append(parts, *block.Text)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// extractTextForEmbedding flattens the request input into a single string
// suitable for embedding generation. PreLLMHook short-circuits embedding and
// transcription requests before this is called (their inputs aren't
// themselves embeddable), so this function only handles request types that
// reach performSemanticSearch.
//
// Text serialization format (for cache consistency):
//   - Chat API: "role: content"
//   - Responses API: "role: msgType: content" (when msgType is present), "role: content" (when msgType is empty)
func (plugin *Plugin) extractTextForEmbedding(state *cacheState, req *schemas.BifrostRequest) (string, error) {
	switch {
	case req.TextCompletionRequest != nil:
		if req.TextCompletionRequest.Input.PromptStr != nil {
			return normalizeText(*req.TextCompletionRequest.Input.PromptStr), nil
		}
		if len(req.TextCompletionRequest.Input.PromptArray) > 0 {
			return normalizeText(strings.Join(req.TextCompletionRequest.Input.PromptArray, " ")), nil
		}
		return "", fmt.Errorf("no prompt found in text completion request")

	case req.ChatRequest != nil:
		reqInput, ok := plugin.getInputForCaching(state, req).([]schemas.ChatMessage)
		if !ok {
			return "", fmt.Errorf("failed to cast request input to chat messages")
		}
		var textParts []string
		for _, msg := range reqInput {
			content := extractChatMessageContent(msg)
			if content == "" {
				continue
			}
			textParts = append(textParts, fmt.Sprintf("%s: %s", msg.Role, normalizeText(content)))
		}
		if len(textParts) == 0 {
			return "", fmt.Errorf("no text content found in chat messages")
		}
		return strings.Join(textParts, "\n"), nil

	case req.ResponsesRequest != nil:
		reqInput, ok := plugin.getInputForCaching(state, req).([]schemas.ResponsesMessage)
		if !ok {
			return "", fmt.Errorf("failed to cast request input to responses messages")
		}
		var textParts []string
		for _, msg := range reqInput {
			content := extractResponsesMessageContent(msg)
			if content == "" {
				continue
			}
			content = normalizeText(content)
			role := ""
			msgType := ""
			if msg.Role != nil {
				role = string(*msg.Role)
			}
			if msg.Type != nil {
				msgType = string(*msg.Type)
			}
			if msgType != "" {
				textParts = append(textParts, fmt.Sprintf("%s: %s: %s", role, msgType, content))
			} else {
				textParts = append(textParts, fmt.Sprintf("%s: %s", role, content))
			}
		}
		if len(textParts) == 0 {
			return "", fmt.Errorf("no text content found in responses messages")
		}
		return strings.Join(textParts, "\n"), nil

	case req.SpeechRequest != nil:
		if req.SpeechRequest.Input.Input == "" {
			return "", fmt.Errorf("no input text found in speech request")
		}
		return normalizeText(req.SpeechRequest.Input.Input), nil

	case req.ImageGenerationRequest != nil:
		if req.ImageGenerationRequest.Input == nil || req.ImageGenerationRequest.Input.Prompt == "" {
			return "", fmt.Errorf("no prompt found in image generation request")
		}
		return normalizeText(req.ImageGenerationRequest.Input.Prompt), nil

	default:
		return "", fmt.Errorf("unsupported input type for semantic caching")
	}
}

// buildUnifiedMetadata builds the property map written alongside the cache
// entry: the columns the vector store indexes for filtering (cache_key,
// provider, model, params_hash, expires_at) plus the from_bifrost marker
// used by Cleanup and ClearCacheForKey to scope deletes. Caller still adds
// the response payload (response or stream_chunks) before Add.
func (plugin *Plugin) buildUnifiedMetadata(provider schemas.ModelProvider, model string, paramsHash string, cacheKey string, ttl time.Duration) map[string]interface{} {
	unifiedMetadata := make(map[string]interface{})
	unifiedMetadata["provider"] = string(provider)
	unifiedMetadata["model"] = model
	unifiedMetadata["cache_key"] = cacheKey
	unifiedMetadata["from_bifrost_semantic_cache_plugin"] = true
	unifiedMetadata["expires_at"] = time.Now().Add(ttl).Unix()
	if paramsHash != "" {
		unifiedMetadata["params_hash"] = paramsHash
	}
	return unifiedMetadata
}

// addNonStreamingResponse marshals the response and writes it as a single
// cache entry. The metadata map is mutated (response + stream_chunks added)
// — safe because the calling goroutine owns it. The ttl parameter is
// retained for symmetry with addStreamingResponse; the actual expiry is
// already encoded in metadata["expires_at"] by buildUnifiedMetadata.
func (plugin *Plugin) addNonStreamingResponse(ctx context.Context, responseID string, res *schemas.BifrostResponse, embedding []float32, metadata map[string]interface{}, ttl time.Duration) error {
	responseData, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("failed to marshal response: %w", err)
	}
	metadata["response"] = string(responseData)
	metadata["stream_chunks"] = []string{}

	if err := plugin.store.Add(ctx, plugin.config.VectorStoreNamespace, responseID, embedding, metadata); err != nil {
		return fmt.Errorf("failed to store unified cache entry: %w", err)
	}

	plugin.logger.Debug("Successfully cached single response with ID: %s", responseID)
	return nil
}

// addStreamingResponse appends one chunk to the per-request accumulator and,
// when the final chunk arrives, flushes the accumulated stream to the cache.
// Errors never reach this function: PostLLMHook returns early on bifrostErr
// (errors are always delivered as the final chunk), so an errored stream
// simply leaves its accumulator behind for the periodic reaper.
func (plugin *Plugin) addStreamingResponse(ctx context.Context, requestID string, storageID string, res *schemas.BifrostResponse, embedding []float32, metadata map[string]interface{}, ttl time.Duration, isFinalChunk bool) error {
	accumulator := plugin.getOrCreateStreamAccumulator(requestID, storageID, embedding, metadata, ttl)

	chunk := &StreamChunk{
		Timestamp: time.Now(),
		Response:  res,
	}
	if err := plugin.addStreamChunk(requestID, chunk); err != nil {
		return fmt.Errorf("failed to add stream chunk: %w", err)
	}

	if !isFinalChunk {
		return nil
	}

	// Gate final processing so it runs exactly once even if multiple chunks
	// race here (shouldn't happen in practice but cheap insurance).
	accumulator.mu.Lock()
	alreadyComplete := accumulator.IsComplete
	if !alreadyComplete {
		accumulator.IsComplete = true
	}
	accumulator.mu.Unlock()

	if alreadyComplete {
		return nil
	}
	if err := plugin.processAccumulatedStream(ctx, requestID); err != nil {
		plugin.logger.Warn("Failed to process accumulated stream for request %s: %v", requestID, err)
	}
	return nil
}

// parseStreamChunks parses stream_chunks data from the various shapes
// different vector store drivers hand back (Weaviate's JSON-decoded
// []interface{}, typed []string, or Redis's JSON-encoded string) into a
// flat []string of per-chunk JSON payloads.
//
// Non-string elements in the []interface{} case are dropped with a warning
// rather than failing the whole replay — partial cache hits are better than
// no hit at all.
func (plugin *Plugin) parseStreamChunks(streamData interface{}) ([]string, error) {
	if streamData == nil {
		return nil, fmt.Errorf("stream data is nil")
	}

	switch v := streamData.(type) {
	case []string:
		return v, nil
	case []interface{}:
		result := make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				plugin.logger.Warn("Stream chunk %d is not a string (got %T), skipping", i, item)
				continue
			}
			result = append(result, s)
		}
		return result, nil
	case string:
		// Redis: stream_chunks stored as a JSON-encoded array of strings.
		var stringArray []string
		if err := json.Unmarshal([]byte(v), &stringArray); err != nil {
			return nil, fmt.Errorf("failed to parse JSON string: %w", err)
		}
		return stringArray, nil
	default:
		return nil, fmt.Errorf("unsupported stream data type: %T", streamData)
	}
}

// getInputForCaching extracts request input for hashing/embedding without
// normalization. For Chat/Responses requests, system messages are filtered
// out when ExcludeSystemPrompt is enabled — that path returns a fresh slice;
// otherwise the original slice is returned by reference (no allocation).
// Other request types always return the underlying input directly.
//
// The slice for Chat/Responses is memoized on state so attachment extraction,
// embedding text extraction, and the history-threshold check reuse the same
// slice instead of re-walking on each call. State may be nil (tests /
// pre-state callers), in which case nothing is cached.
func (plugin *Plugin) getInputForCaching(state *cacheState, req *schemas.BifrostRequest) interface{} {
	if state != nil && state.FilteredInput != nil {
		return state.FilteredInput
	}
	excludeSystem := plugin.config.ExcludeSystemPrompt != nil && *plugin.config.ExcludeSystemPrompt
	var out interface{}
	switch req.RequestType {
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		out = req.TextCompletionRequest.Input
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		out = filterChatMessages(req.ChatRequest.Input, excludeSystem)
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest, schemas.WebSocketResponsesRequest:
		out = filterResponsesMessages(req.ResponsesRequest.Input, excludeSystem)
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		out = req.SpeechRequest.Input.Input
	case schemas.EmbeddingRequest:
		out = req.EmbeddingRequest.Input
	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		out = req.TranscriptionRequest.Input
	case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest:
		out = req.ImageGenerationRequest.Input
	default:
		return nil
	}
	if state != nil {
		state.FilteredInput = out
	}
	return out
}

// filterChatMessages returns msgs unchanged when excludeSystem is false.
// Otherwise, returns a copy with system messages dropped.
func filterChatMessages(msgs []schemas.ChatMessage, excludeSystem bool) []schemas.ChatMessage {
	if !excludeSystem {
		return msgs
	}
	out := make([]schemas.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == schemas.ChatMessageRoleSystem {
			continue
		}
		out = append(out, m)
	}
	return out
}

// filterResponsesMessages returns msgs unchanged when excludeSystem is false.
// Otherwise, returns a copy with system messages dropped.
func filterResponsesMessages(msgs []schemas.ResponsesMessage, excludeSystem bool) []schemas.ResponsesMessage {
	if !excludeSystem {
		return msgs
	}
	out := make([]schemas.ResponsesMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role != nil && *m.Role == schemas.ResponsesInputMessageRoleSystem {
			continue
		}
		out = append(out, m)
	}
	return out
}

// getNormalizedInputForCaching returns a copy of req.Input with text fields
// lowercased + trimmed, suitable for hashing/embedding. System messages are
// dropped when ExcludeSystemPrompt is enabled.
//
// Allocation strategy: the original request must never be mutated, but the
// returned value only needs to round-trip through json.Marshal — it's hashed,
// not stored. So we shallow-copy each message struct and rewrite Content
// (the only field we normalize), sharing all other pointer fields with the
// original. This avoids the per-call message-graph deep copy that
// schemas.DeepCopy*Message would otherwise do.
func (plugin *Plugin) getNormalizedInputForCaching(req *schemas.BifrostRequest) interface{} {
	excludeSystem := plugin.config.ExcludeSystemPrompt != nil && *plugin.config.ExcludeSystemPrompt
	switch req.RequestType {
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		input := req.TextCompletionRequest.Input
		out := schemas.TextCompletionInput{}
		if input.PromptStr != nil {
			ns := normalizeText(*input.PromptStr)
			out.PromptStr = &ns
		} else if len(input.PromptArray) > 0 {
			arr := make([]string, len(input.PromptArray))
			for i, p := range input.PromptArray {
				arr[i] = normalizeText(p)
			}
			out.PromptArray = arr
		}
		return out
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		originalMessages := req.ChatRequest.Input
		normalizedMessages := make([]schemas.ChatMessage, 0, len(originalMessages))
		for _, msg := range originalMessages {
			if excludeSystem && msg.Role == schemas.ChatMessageRoleSystem {
				continue
			}
			normalizedMessages = append(normalizedMessages, normalizeChatMessage(msg))
		}
		return normalizedMessages
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest, schemas.WebSocketResponsesRequest:
		originalMessages := req.ResponsesRequest.Input
		normalizedMessages := make([]schemas.ResponsesMessage, 0, len(originalMessages))
		for _, msg := range originalMessages {
			if excludeSystem && msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleSystem {
				continue
			}
			normalizedMessages = append(normalizedMessages, normalizeResponsesMessage(msg))
		}
		return normalizedMessages
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		return normalizeText(req.SpeechRequest.Input.Input)
	case schemas.EmbeddingRequest:
		input := req.EmbeddingRequest.Input
		out := schemas.EmbeddingInput{}
		if input.Text != nil {
			ns := normalizeText(*input.Text)
			out.Text = &ns
		} else if len(input.Texts) > 0 {
			arr := make([]string, len(input.Texts))
			for i, t := range input.Texts {
				arr[i] = normalizeText(t)
			}
			out.Texts = arr
		} else if input.Embedding != nil {
			// Numeric embeddings aren't text-normalizable but must still appear
			// in the hash payload, so copy the slice to avoid aliasing.
			out.Embedding = append([]int(nil), input.Embedding...)
		} else if input.Embeddings != nil {
			out.Embeddings = append([][]int(nil), input.Embeddings...)
		}
		return out
	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		return req.TranscriptionRequest.Input
	case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest:
		if req.ImageGenerationRequest != nil && req.ImageGenerationRequest.Input != nil {
			return &schemas.ImageGenerationInput{
				Prompt: normalizeText(req.ImageGenerationRequest.Input.Prompt),
			}
		}
		return nil
	default:
		return nil
	}
}

// normalizeChatMessage returns a shallow copy of msg with its Content
// rewritten so text fields are lowercased + trimmed. Other pointer fields
// (ToolCalls, Annotations, ChatToolMessage, ChatAssistantMessage) are
// aliased — safe because we don't mutate them.
func normalizeChatMessage(msg schemas.ChatMessage) schemas.ChatMessage {
	out := msg
	if msg.Content == nil {
		return out
	}
	nc := *msg.Content
	if msg.Content.ContentStr != nil {
		ns := normalizeText(*msg.Content.ContentStr)
		nc.ContentStr = &ns
	} else if msg.Content.ContentBlocks != nil {
		blocks := make([]schemas.ChatContentBlock, len(msg.Content.ContentBlocks))
		for i, b := range msg.Content.ContentBlocks {
			blocks[i] = b
			if b.Text != nil {
				nt := normalizeText(*b.Text)
				blocks[i].Text = &nt
			}
		}
		nc.ContentBlocks = blocks
	}
	out.Content = &nc
	return out
}

// normalizeResponsesMessage mirrors normalizeChatMessage for the Responses API.
func normalizeResponsesMessage(msg schemas.ResponsesMessage) schemas.ResponsesMessage {
	out := msg
	if msg.Content == nil {
		return out
	}
	nc := *msg.Content
	if msg.Content.ContentStr != nil {
		ns := normalizeText(*msg.Content.ContentStr)
		nc.ContentStr = &ns
	} else if msg.Content.ContentBlocks != nil {
		blocks := make([]schemas.ResponsesMessageContentBlock, len(msg.Content.ContentBlocks))
		for i, b := range msg.Content.ContentBlocks {
			blocks[i] = b
			if b.Text != nil {
				nt := normalizeText(*b.Text)
				blocks[i].Text = &nt
			}
		}
		nc.ContentBlocks = blocks
	}
	out.Content = &nc
	return out
}

// extractChatParametersToMetadata extracts Chat API parameters into metadata map.
func (plugin *Plugin) extractChatParametersToMetadata(params *schemas.ChatParameters, metadata map[string]interface{}) {
	if params.ToolChoice != nil {
		if params.ToolChoice.ChatToolChoiceStr != nil {
			metadata["tool_choice"] = *params.ToolChoice.ChatToolChoiceStr
		} else if params.ToolChoice.ChatToolChoiceStruct != nil && params.ToolChoice.ChatToolChoiceStruct.Function != nil && params.ToolChoice.ChatToolChoiceStruct.Function.Name != "" {
			metadata["tool_choice"] = params.ToolChoice.ChatToolChoiceStruct.Function.Name
		}
	}
	putIfSet(metadata, "temperature", params.Temperature)
	putIfSet(metadata, "top_p", params.TopP)
	putIfSet(metadata, "max_tokens", params.MaxCompletionTokens)
	putSortedSetIfNonEmpty(metadata, "stop_sequences", params.Stop)
	putIfSet(metadata, "presence_penalty", params.PresencePenalty)
	putIfSet(metadata, "frequency_penalty", params.FrequencyPenalty)
	putIfSet(metadata, "parallel_tool_calls", params.ParallelToolCalls)
	putIfSet(metadata, "user", params.User)
	putIfSet(metadata, "logit_bias", params.LogitBias)
	putIfSet(metadata, "logprobs", params.LogProbs)
	putSortedSetIfNonEmpty(metadata, "modalities", params.Modalities)
	putIfSet(metadata, "prompt_cache_key", params.PromptCacheKey)
	if params.Reasoning != nil {
		putIfSet(metadata, "reasoning_enabled", params.Reasoning.Enabled)
		putIfSet(metadata, "reasoning_effort", params.Reasoning.Effort)
	}
	if params.ResponseFormat != nil {
		// ResponseFormat is a struct pointer that callers expect to round-trip
		// through JSON; store the pointer directly so MarshalDeeplySorted walks it.
		metadata["response_format"] = params.ResponseFormat
	}
	putIfSet(metadata, "safety_identifier", params.SafetyIdentifier)
	putIfSet(metadata, "seed", params.Seed)
	putIfSet(metadata, "service_tier", params.ServiceTier)
	putIfSet(metadata, "store", params.Store)
	putIfSet(metadata, "top_logprobs", params.TopLogProbs)
	putIfSet(metadata, "verbosity", params.Verbosity)
	if len(params.ExtraParams) > 0 {
		maps.Copy(metadata, params.ExtraParams)
	}
	if len(params.Tools) > 0 {
		// Tools are an order-insensitive set; producer-side ordering (notably
		// MCP's randomized map iteration) must not perturb the request hash.
		if toolsHash, err := hashSortedSet(params.Tools, func(t schemas.ChatTool) string {
			if t.Function == nil {
				return ""
			}
			return t.Function.Name
		}); err != nil {
			plugin.logger.Warn("Failed to marshal tools for metadata: %v", err)
		} else if toolsHash != "" {
			metadata["tools_hash"] = toolsHash
		}
	}
}

// extractResponsesParametersToMetadata extracts Responses API parameters into metadata map.
func (plugin *Plugin) extractResponsesParametersToMetadata(params *schemas.ResponsesParameters, metadata map[string]interface{}) {
	if params.ToolChoice != nil {
		if params.ToolChoice.ResponsesToolChoiceStr != nil {
			metadata["tool_choice"] = *params.ToolChoice.ResponsesToolChoiceStr
		} else if params.ToolChoice.ResponsesToolChoiceStruct != nil && params.ToolChoice.ResponsesToolChoiceStruct.Name != nil {
			metadata["tool_choice"] = *params.ToolChoice.ResponsesToolChoiceStruct.Name
		}
	}
	putIfSet(metadata, "temperature", params.Temperature)
	putIfSet(metadata, "top_p", params.TopP)
	putIfSet(metadata, "max_tokens", params.MaxOutputTokens)
	putIfSet(metadata, "parallel_tool_calls", params.ParallelToolCalls)
	putIfSet(metadata, "background", params.Background)
	putIfSet(metadata, "conversation", params.Conversation)
	putSortedSetIfNonEmpty(metadata, "include", params.Include)
	putIfSet(metadata, "instructions", params.Instructions)
	putIfSet(metadata, "max_tool_calls", params.MaxToolCalls)
	putIfSet(metadata, "previous_response_id", params.PreviousResponseID)
	putIfSet(metadata, "prompt_cache_key", params.PromptCacheKey)
	if params.Reasoning != nil {
		putIfSet(metadata, "reasoning_effort", params.Reasoning.Effort)
		putIfSet(metadata, "reasoning_max_tokens", params.Reasoning.MaxTokens)
		putIfSet(metadata, "reasoning_summary", params.Reasoning.Summary)
	}
	putIfSet(metadata, "safety_identifier", params.SafetyIdentifier)
	putIfSet(metadata, "service_tier", params.ServiceTier)
	putIfSet(metadata, "store", params.Store)
	if params.Text != nil {
		putIfSet(metadata, "text_verbosity", params.Text.Verbosity)
		if params.Text.Format != nil {
			metadata["text_format_type"] = params.Text.Format.Type
		}
	}
	putIfSet(metadata, "top_logprobs", params.TopLogProbs)
	putIfSet(metadata, "truncation", params.Truncation)
	if len(params.ExtraParams) > 0 {
		maps.Copy(metadata, params.ExtraParams)
	}
	if len(params.Tools) > 0 {
		// Tools are an order-insensitive set; producer-side ordering (notably
		// MCP's randomized map iteration) must not perturb the request hash.
		if toolsHash, err := hashSortedSet(params.Tools, func(t schemas.ResponsesTool) string {
			if t.Name == nil {
				return ""
			}
			return *t.Name
		}); err != nil {
			plugin.logger.Warn("Failed to marshal tools for metadata: %v", err)
		} else if toolsHash != "" {
			metadata["tools_hash"] = toolsHash
		}
	}
}

// extractTextCompletionParametersToMetadata extracts Text Completion parameters into metadata map.
func (plugin *Plugin) extractTextCompletionParametersToMetadata(params *schemas.TextCompletionParameters, metadata map[string]interface{}) {
	putIfSet(metadata, "temperature", params.Temperature)
	putIfSet(metadata, "top_p", params.TopP)
	putIfSet(metadata, "max_tokens", params.MaxTokens)
	putSortedSetIfNonEmpty(metadata, "stop_sequences", params.Stop)
	putIfSet(metadata, "presence_penalty", params.PresencePenalty)
	putIfSet(metadata, "frequency_penalty", params.FrequencyPenalty)
	putIfSet(metadata, "user", params.User)
	putIfSet(metadata, "best_of", params.BestOf)
	putIfSet(metadata, "echo", params.Echo)
	putIfSet(metadata, "logit_bias", params.LogitBias)
	putIfSet(metadata, "logprobs", params.LogProbs)
	putIfSet(metadata, "n", params.N)
	putIfSet(metadata, "seed", params.Seed)
	putIfSet(metadata, "suffix", params.Suffix)
	if len(params.ExtraParams) > 0 {
		maps.Copy(metadata, params.ExtraParams)
	}
}

// extractSpeechParametersToMetadata extracts Speech parameters into metadata map.
func (plugin *Plugin) extractSpeechParametersToMetadata(params *schemas.SpeechParameters, metadata map[string]interface{}) {
	if params == nil {
		return
	}
	putIfSet(metadata, "speed", params.Speed)
	if params.ResponseFormat != "" {
		metadata["response_format"] = params.ResponseFormat
	}
	if params.Instructions != "" {
		metadata["instructions"] = params.Instructions
	}
	putIfSet(metadata, "voice", params.VoiceConfig.Voice)
	if len(params.VoiceConfig.MultiVoiceConfig) > 0 {
		flattenedVC := make([]string, len(params.VoiceConfig.MultiVoiceConfig))
		for i, vc := range params.VoiceConfig.MultiVoiceConfig {
			flattenedVC[i] = fmt.Sprintf("%s:%s", vc.Speaker, vc.Voice)
		}
		metadata["multi_voice_count"] = flattenedVC
	}
	if len(params.PronunciationDictionaryLocators) > 0 {
		if hash, err := hashSortedSet(params.PronunciationDictionaryLocators, func(l schemas.SpeechPronunciationDictionaryLocator) string {
			return l.PronunciationDictionaryID
		}); err != nil {
			plugin.logger.Warn("Failed to marshal pronunciation_dictionary_locators for metadata: %v", err)
		} else if hash != "" {
			metadata["pronunciation_dictionary_locators_hash"] = hash
		}
	}
	if len(params.ExtraParams) > 0 {
		maps.Copy(metadata, params.ExtraParams)
	}
}

// extractEmbeddingParametersToMetadata extracts Embedding parameters into metadata map.
func (plugin *Plugin) extractEmbeddingParametersToMetadata(params *schemas.EmbeddingParameters, metadata map[string]interface{}) {
	putIfSet(metadata, "encoding_format", params.EncodingFormat)
	putIfSet(metadata, "dimensions", params.Dimensions)
	if len(params.ExtraParams) > 0 {
		maps.Copy(metadata, params.ExtraParams)
	}
}

// extractTranscriptionParametersToMetadata extracts Transcription parameters into metadata map.
func (plugin *Plugin) extractTranscriptionParametersToMetadata(params *schemas.TranscriptionParameters, metadata map[string]interface{}) {
	putIfSet(metadata, "language", params.Language)
	putIfSet(metadata, "response_format", params.ResponseFormat)
	putIfSet(metadata, "prompt", params.Prompt)
	putIfSet(metadata, "file_format", params.Format)
	putSortedSetIfNonEmpty(metadata, "timestamp_granularities", params.TimestampGranularities)
	putSortedSetIfNonEmpty(metadata, "include", params.Include)
	if len(params.AdditionalFormats) > 0 {
		if hash, err := hashSortedSet(params.AdditionalFormats, func(f schemas.TranscriptionAdditionalFormat) string {
			return string(f.Format)
		}); err != nil {
			plugin.logger.Warn("Failed to marshal additional_formats for metadata: %v", err)
		} else if hash != "" {
			metadata["additional_formats_hash"] = hash
		}
	}
	if len(params.ExtraParams) > 0 {
		maps.Copy(metadata, params.ExtraParams)
	}
}

// extractImageGenerationParametersToMetadata extracts Image Generation parameters into metadata map.
func (plugin *Plugin) extractImageGenerationParametersToMetadata(params *schemas.ImageGenerationParameters, metadata map[string]interface{}) {
	if params == nil {
		return
	}
	putIfSet(metadata, "n", params.N)
	putIfSet(metadata, "background", params.Background)
	putIfSet(metadata, "moderation", params.Moderation)
	putIfSet(metadata, "partial_images", params.PartialImages)
	putIfSet(metadata, "size", params.Size)
	putIfSet(metadata, "quality", params.Quality)
	putIfSet(metadata, "output_compression", params.OutputCompression)
	putIfSet(metadata, "output_format", params.OutputFormat)
	putIfSet(metadata, "style", params.Style)
	putIfSet(metadata, "response_format", params.ResponseFormat)
	putIfSet(metadata, "seed", params.Seed)
	putIfSet(metadata, "negative_prompt", params.NegativePrompt)
	putIfSet(metadata, "num_inference_steps", params.NumInferenceSteps)
	putIfSet(metadata, "user", params.User)
	if len(params.InputImages) > 0 {
		metadata["input_images"] = params.InputImages
	}
	if len(params.ExtraParams) > 0 {
		maps.Copy(metadata, params.ExtraParams)
	}
}

// isConversationHistoryThresholdExceeded returns true when the request's
// conversation history is longer than ConversationHistoryThreshold. Long
// histories are unlikely to repeat and unlikely to be semantically similar
// to other requests, so caching them mostly bloats the store; PreLLMHook
// uses this to skip caching such requests entirely.
func (plugin *Plugin) isConversationHistoryThresholdExceeded(state *cacheState, req *schemas.BifrostRequest) bool {
	switch {
	case req.ChatRequest != nil:
		input, ok := plugin.getInputForCaching(state, req).([]schemas.ChatMessage)
		if !ok {
			return false
		}
		return len(input) > plugin.config.ConversationHistoryThreshold
	case req.ResponsesRequest != nil:
		input, ok := plugin.getInputForCaching(state, req).([]schemas.ResponsesMessage)
		if !ok {
			return false
		}
		return len(input) > plugin.config.ConversationHistoryThreshold
	default:
		return false
	}
}
