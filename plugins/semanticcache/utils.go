package semanticcache

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/cespare/xxhash/v2"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// normalizeText applies consistent normalization to text inputs for better cache hit rates.
// It converts text to lowercase and trims whitespace to reduce cache misses due to minor variations.
func normalizeText(text string) string {
	return strings.ToLower(strings.TrimSpace(text))
}

// generateEmbedding generates an embedding for the given text using the configured provider.
func (plugin *Plugin) generateEmbedding(ctx *schemas.BifrostContext, text string) ([]float32, int, error) {
	// Create embedding request
	embeddingReq := &schemas.BifrostEmbeddingRequest{
		Provider: plugin.config.Provider,
		Model:    plugin.config.EmbeddingModel,
		Input: &schemas.EmbeddingInput{
			Text: &text,
		},
	}

	// Generate embedding using bifrost client
	response, err := plugin.client.EmbeddingRequest(ctx, embeddingReq)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to generate embedding: %v", err)
	}

	// Extract the first embedding from response
	if len(response.Data) == 0 {
		return nil, 0, fmt.Errorf("no embeddings returned from provider")
	}

	// Get the embedding from the first data item
	embedding := response.Data[0].Embedding
	inputTokens := 0
	if response.Usage != nil {
		inputTokens = response.Usage.TotalTokens
	}

	if embedding.EmbeddingStr != nil {
		// decode embedding.EmbeddingStr to []float32
		var vals []float32
		if err := json.Unmarshal([]byte(*embedding.EmbeddingStr), &vals); err != nil {
			return nil, 0, fmt.Errorf("failed to parse string embedding: %w", err)
		}
		return vals, inputTokens, nil
	} else if embedding.EmbeddingArray != nil {
		return embedding.EmbeddingArray, inputTokens, nil
	} else if len(embedding.Embedding2DArray) > 0 {
		// Flatten 2D array into single embedding
		var flattened []float32
		for _, arr := range embedding.Embedding2DArray {
			flattened = append(flattened, arr...)
		}
		return flattened, inputTokens, nil
	}

	return nil, 0, fmt.Errorf("embedding data is not in expected format")
}

// generateRequestHash creates an xxhash of the request for semantic cache key generation.
// It normalizes the request by including all relevant fields that affect the response:
// - Input (chat completion, text completion, etc.)
// - Parameters (temperature, max_tokens, tools, etc.)
// - Provider (if CacheByProvider is true)
// - Model (if CacheByModel is true)
//
// Note: Fallbacks are excluded as they only affect error handling, not the actual response.
//
// Parameters:
//   - req: The Bifrost request to hash for semantic cache key generation
//
// Returns:
//   - string: Hexadecimal representation of the xxhash
//   - error: Any error that occurred during request normalization or hashing
func (plugin *Plugin) generateRequestHash(req *schemas.BifrostRequest) (string, error) {
	// Create a hash input structure that includes both input and parameters
	hashInput := struct {
		Input  interface{} `json:"input"`
		Params interface{} `json:"params,omitempty"`
		Stream bool        `json:"stream,omitempty"`
	}{
		Input:  plugin.getNormalizedInputForCaching(req),
		Stream: bifrost.IsStreamRequestType(req.RequestType),
	}

	switch req.RequestType {
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		hashInput.Params = req.TextCompletionRequest.Params
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		hashInput.Params = req.ChatRequest.Params
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		hashInput.Params = req.ResponsesRequest.Params
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		if req.SpeechRequest != nil {
			hashInput.Params = req.SpeechRequest.Params
		}
	case schemas.EmbeddingRequest:
		hashInput.Params = req.EmbeddingRequest.Params
	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		hashInput.Params = req.TranscriptionRequest.Params
	case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest:
		hashInput.Params = req.ImageGenerationRequest.Params
	}

	// Marshal to JSON for consistent hashing
	jsonData, err := json.Marshal(hashInput)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request for hashing: %w", err)
	}

	// Generate hash based on configured algorithm
	hash := xxhash.Sum64(jsonData)
	return fmt.Sprintf("%x", hash), nil
}

// extractTextForEmbedding extracts meaningful text from different input types for embedding generation.
// Returns the text to embed and metadata for storage.
//
// Text serialization format (for cache consistency):
//   - Chat API: "role: content"
//   - Responses API: "role: msgType: content" (when msgType is present), "role: content" (when msgType is empty)
//
// Note: Format updated to conditionally include msgType to avoid double colons and maintain consistency.
func (plugin *Plugin) extractTextForEmbedding(req *schemas.BifrostRequest) (string, string, error) {
	metadata := map[string]interface{}{}

	attachments := []string{}

	// Add parameters as metadata if present - handle segregated parameters
	metadata["stream"] = bifrost.IsStreamRequestType(req.RequestType)

	// Extract parameters based on request type
	switch req.RequestType {
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		if req.TextCompletionRequest != nil && req.TextCompletionRequest.Params != nil {
			plugin.extractTextCompletionParametersToMetadata(req.TextCompletionRequest.Params, metadata)
		}
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		if req.ChatRequest != nil && req.ChatRequest.Params != nil {
			plugin.extractChatParametersToMetadata(req.ChatRequest.Params, metadata)
		}
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		if req.ResponsesRequest != nil && req.ResponsesRequest.Params != nil {
			plugin.extractResponsesParametersToMetadata(req.ResponsesRequest.Params, metadata)
		}
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		if req.SpeechRequest != nil && req.SpeechRequest.Params != nil {
			plugin.extractSpeechParametersToMetadata(req.SpeechRequest.Params, metadata)
		}
	case schemas.EmbeddingRequest:
		if req.EmbeddingRequest != nil && req.EmbeddingRequest.Params != nil {
			plugin.extractEmbeddingParametersToMetadata(req.EmbeddingRequest.Params, metadata)
		}
	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		if req.TranscriptionRequest != nil && req.TranscriptionRequest.Params != nil {
			plugin.extractTranscriptionParametersToMetadata(req.TranscriptionRequest.Params, metadata)
		}
	case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest:
		if req.ImageGenerationRequest != nil && req.ImageGenerationRequest.Params != nil {
			plugin.extractImageGenerationParametersToMetadata(req.ImageGenerationRequest.Params, metadata)
		}
	}

	switch {
	case req.TextCompletionRequest != nil:
		metadataHash, err := getMetadataHash(metadata)
		if err != nil {
			return "", "", fmt.Errorf("failed to marshal metadata for metadata hash: %w", err)
		}

		var textContent string
		if req.TextCompletionRequest.Input.PromptStr != nil {
			textContent = normalizeText(*req.TextCompletionRequest.Input.PromptStr)
		} else if len(req.TextCompletionRequest.Input.PromptArray) > 0 {
			textContent = normalizeText(strings.Join(req.TextCompletionRequest.Input.PromptArray, " "))
		}
		return textContent, metadataHash, nil

	case req.ChatRequest != nil:
		reqInput, ok := plugin.getInputForCaching(req).([]schemas.ChatMessage)
		if !ok {
			return "", "", fmt.Errorf("failed to cast request input to chat messages")
		}

		// Serialize chat messages for embedding
		var textParts []string
		for _, msg := range reqInput {
			// Extract content as string
			var content string
			if msg.Content.ContentStr != nil {
				content = *msg.Content.ContentStr
			} else if msg.Content.ContentBlocks != nil {
				// For content blocks, extract text parts
				var blockTexts []string
				for _, block := range msg.Content.ContentBlocks {
					if block.Text != nil {
						blockTexts = append(blockTexts, *block.Text)
					}
					if block.ImageURLStruct != nil && block.ImageURLStruct.URL != "" {
						attachments = append(attachments, block.ImageURLStruct.URL)
					}
				}
				content = strings.Join(blockTexts, " ")
			}

			if content != "" {
				textParts = append(textParts, fmt.Sprintf("%s: %s", msg.Role, normalizeText(content)))
			}
		}

		if len(textParts) == 0 {
			return "", "", fmt.Errorf("no text content found in chat messages")
		}

		if len(attachments) > 0 {
			metadata["attachments"] = attachments
		}

		metadataHash, err := getMetadataHash(metadata)
		if err != nil {
			return "", "", fmt.Errorf("failed to marshal metadata for metadata hash: %w", err)
		}

		return strings.Join(textParts, "\n"), metadataHash, nil

	case req.ResponsesRequest != nil:
		reqInput, ok := plugin.getInputForCaching(req).([]schemas.ResponsesMessage)
		if !ok {
			return "", "", fmt.Errorf("failed to cast request input to responses messages")
		}

		// Serialize chat messages for embedding
		var textParts []string
		for _, msg := range reqInput {
			// Extract content as string
			var content string
			if msg.Content.ContentStr != nil {
				content = normalizeText(*msg.Content.ContentStr)
			} else if msg.Content.ContentBlocks != nil {
				// For content blocks, extract text parts
				var blockTexts []string
				for _, block := range msg.Content.ContentBlocks {
					if block.Text != nil {
						blockTexts = append(blockTexts, normalizeText(*block.Text))
					}
					if block.ResponsesInputMessageContentBlockImage != nil && block.ResponsesInputMessageContentBlockImage.ImageURL != nil {
						attachments = append(attachments, *block.ResponsesInputMessageContentBlockImage.ImageURL)
					}
					if block.ResponsesInputMessageContentBlockFile != nil && block.ResponsesInputMessageContentBlockFile.FileURL != nil {
						attachments = append(attachments, *block.ResponsesInputMessageContentBlockFile.FileURL)
					}
				}
				content = strings.Join(blockTexts, " ")
			}

			role := ""
			msgType := ""
			if msg.Role != nil {
				role = string(*msg.Role)
			}
			if msg.Type != nil {
				msgType = string(*msg.Type)
			}

			if content != "" {
				textParts = append(textParts, fmt.Sprintf("%s: %s: %s", role, msgType, content))
			}
		}

		if len(textParts) == 0 {
			return "", "", fmt.Errorf("no text content found in chat messages")
		}

		if len(attachments) > 0 {
			metadata["attachments"] = attachments
		}

		metadataHash, err := getMetadataHash(metadata)
		if err != nil {
			return "", "", fmt.Errorf("failed to marshal metadata for metadata hash: %w", err)
		}

		return strings.Join(textParts, "\n"), metadataHash, nil

	case req.SpeechRequest != nil:
		if req.SpeechRequest.Input.Input != "" {
			metadataHash, err := getMetadataHash(metadata)
			if err != nil {
				return "", "", fmt.Errorf("failed to marshal metadata for metadata hash: %w", err)
			}

			return req.SpeechRequest.Input.Input, metadataHash, nil
		}
		return "", "", fmt.Errorf("no input text found in speech request")

	case req.EmbeddingRequest != nil:
		metadataHash, err := getMetadataHash(metadata)
		if err != nil {
			return "", "", fmt.Errorf("failed to marshal metadata for metadata hash: %w", err)
		}

		texts := req.EmbeddingRequest.Input.Texts

		if len(texts) == 0 && req.EmbeddingRequest.Input.Text != nil {
			texts = []string{*req.EmbeddingRequest.Input.Text}
		}

		var text string
		for _, t := range texts {
			text += t + " "
		}

		return strings.TrimSpace(text), metadataHash, nil

	case req.TranscriptionRequest != nil:
		// Skip semantic caching for transcription requests
		return "", "", fmt.Errorf("transcription requests are not supported for semantic caching")

	case req.ImageGenerationRequest != nil:
		if req.ImageGenerationRequest.Input == nil || req.ImageGenerationRequest.Input.Prompt == "" {
			return "", "", fmt.Errorf("no prompt found in image generation request")
		}
		metadataHash, err := getMetadataHash(metadata)
		if err != nil {
			return "", "", fmt.Errorf("failed to marshal metadata for metadata hash: %w", err)
		}
		return normalizeText(req.ImageGenerationRequest.Input.Prompt), metadataHash, nil

	default:
		return "", "", fmt.Errorf("unsupported input type for semantic caching")
	}
}

func getMetadataHash(metadata map[string]interface{}) (string, error) {
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("failed to marshal metadata for metadata hash: %w", err)
	}
	return fmt.Sprintf("%x", xxhash.Sum64(metadataJSON)), nil
}

// buildUnifiedMetadata constructs the unified metadata structure for VectorEntry
func (plugin *Plugin) buildUnifiedMetadata(provider schemas.ModelProvider, model string, paramsHash string, requestHash string, cacheKey string, ttl time.Duration) map[string]interface{} {
	unifiedMetadata := make(map[string]interface{})

	// Top-level fields (outside params)
	unifiedMetadata["provider"] = string(provider)
	unifiedMetadata["model"] = model
	unifiedMetadata["request_hash"] = requestHash
	unifiedMetadata["cache_key"] = cacheKey
	unifiedMetadata["from_bifrost_semantic_cache_plugin"] = true

	// Calculate expiration timestamp (current time + TTL)
	expiresAt := time.Now().Add(ttl).Unix()
	unifiedMetadata["expires_at"] = expiresAt

	// Individual param fields will be stored as params_* by the vectorstore
	// We pass the params map to the vectorstore, and it handles the individual field storage
	if paramsHash != "" {
		unifiedMetadata["params_hash"] = paramsHash
	}

	return unifiedMetadata
}

// addSingleResponse stores a single (non-streaming) response in unified VectorEntry format
func (plugin *Plugin) addSingleResponse(ctx context.Context, responseID string, res *schemas.BifrostResponse, embedding []float32, metadata map[string]interface{}, ttl time.Duration) error {
	// Marshal response as string
	responseData, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("failed to marshal response: %w", err)
	}

	// Add response field to metadata
	metadata["response"] = string(responseData)
	metadata["stream_chunks"] = []string{}

	// Store unified entry using new VectorStore interface
	if err := plugin.store.Add(ctx, plugin.config.VectorStoreNamespace, responseID, embedding, metadata); err != nil {
		return fmt.Errorf("failed to store unified cache entry: %w", err)
	}

	plugin.logger.Debug(fmt.Sprintf("%s Successfully cached single response with ID: %s", PluginLoggerPrefix, responseID))
	return nil
}

// addStreamingResponse handles streaming response storage by accumulating chunks
func (plugin *Plugin) addStreamingResponse(ctx context.Context, responseID string, res *schemas.BifrostResponse, bifrostErr *schemas.BifrostError, embedding []float32, metadata map[string]interface{}, ttl time.Duration, isFinalChunk bool) error {
	// Create accumulator if it doesn't exist
	accumulator := plugin.getOrCreateStreamAccumulator(responseID, embedding, metadata, ttl)

	// Create chunk from current response
	chunk := &StreamChunk{
		Timestamp: time.Now(),
		Response:  res,
	}

	// Check for finish reason or set error finish reason
	if bifrostErr != nil {
		// Error case - mark as final chunk with error
		chunk.FinishReason = bifrost.Ptr("error")
	} else if res != nil && res.ChatResponse != nil && len(res.ChatResponse.Choices) > 0 {
		choice := res.ChatResponse.Choices[0]
		if choice.ChatStreamResponseChoice != nil {
			chunk.FinishReason = choice.FinishReason
		}
	}

	// Add chunk to accumulator synchronously to maintain order
	if err := plugin.addStreamChunk(responseID, chunk, isFinalChunk); err != nil {
		return fmt.Errorf("failed to add stream chunk: %w", err)
	}

	// Check if this is the final chunk and gate final processing to ensure single invocation
	accumulator.mu.Lock()
	// Check for completion: either FinishReason is present, there's an error, or token usage exists
	alreadyComplete := accumulator.IsComplete

	// Track if any chunk has an error
	if bifrostErr != nil {
		accumulator.HasError = true
	}

	if isFinalChunk && !alreadyComplete {
		accumulator.IsComplete = true
		accumulator.FinalTimestamp = chunk.Timestamp
	}
	accumulator.mu.Unlock()

	// If this is the final chunk and hasn't been processed yet, process accumulated chunks
	// Note: processAccumulatedStream will check for errors and skip caching if any errors occurred
	if isFinalChunk && !alreadyComplete {
		if processErr := plugin.processAccumulatedStream(ctx, responseID); processErr != nil {
			plugin.logger.Warn("%s Failed to process accumulated stream for request %s: %v", PluginLoggerPrefix, responseID, processErr)
		}
	}

	return nil
}

// getInputForCaching extracts request input for hashing/embedding without normalization.
// For Chat/Responses requests, it filters out system messages if configured but returns shallow copies.
// For other request types, it returns direct references to the input.
func (plugin *Plugin) getInputForCaching(req *schemas.BifrostRequest) interface{} {
	switch req.RequestType {
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		return req.TextCompletionRequest.Input
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		originalMessages := req.ChatRequest.Input
		filteredMessages := make([]schemas.ChatMessage, 0, len(originalMessages))
		for _, msg := range originalMessages {
			// Skip system messages if configured to exclude them
			if plugin.config.ExcludeSystemPrompt != nil && *plugin.config.ExcludeSystemPrompt && msg.Role == schemas.ChatMessageRoleSystem {
				continue
			}
			filteredMessages = append(filteredMessages, msg)
		}
		return filteredMessages
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		originalMessages := req.ResponsesRequest.Input
		filteredMessages := make([]schemas.ResponsesMessage, 0, len(originalMessages))
		for _, msg := range originalMessages {
			// Skip system messages if configured to exclude them
			if plugin.config.ExcludeSystemPrompt != nil && *plugin.config.ExcludeSystemPrompt && msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleSystem {
				continue
			}
			filteredMessages = append(filteredMessages, msg)
		}
		return filteredMessages
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		return req.SpeechRequest.Input.Input
	case schemas.EmbeddingRequest:
		return req.EmbeddingRequest.Input
	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		return req.TranscriptionRequest.Input
	case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest:
		return req.ImageGenerationRequest.Input
	default:
		return nil
	}
}

// getNormalizedInputForCaching returns a copy of req.Input for hashing/embedding. The input is normalized.
// It applies text normalization (lowercase + trim) and optionally removes system messages.
func (plugin *Plugin) getNormalizedInputForCaching(req *schemas.BifrostRequest) interface{} {
	switch req.RequestType {
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		// Create a deep copy of the input to avoid mutating the original request
		copiedInput := schemas.TextCompletionInput{}
		if req.TextCompletionRequest.Input.PromptStr != nil {
			copiedPromptStr := *req.TextCompletionRequest.Input.PromptStr
			copiedInput.PromptStr = &copiedPromptStr
		} else if len(req.TextCompletionRequest.Input.PromptArray) > 0 {
			copiedPromptArray := make([]string, len(req.TextCompletionRequest.Input.PromptArray))
			copy(copiedPromptArray, req.TextCompletionRequest.Input.PromptArray)
			copiedInput.PromptArray = copiedPromptArray
		}

		if copiedInput.PromptStr != nil {
			normalizedText := normalizeText(*copiedInput.PromptStr)
			copiedInput.PromptStr = &normalizedText
		} else if len(copiedInput.PromptArray) > 0 {
			// Create a copy of the PromptArray and normalize each element
			normalizedPromptArray := make([]string, len(copiedInput.PromptArray))
			copy(normalizedPromptArray, copiedInput.PromptArray)
			for i, prompt := range normalizedPromptArray {
				normalizedPromptArray[i] = normalizeText(prompt)
			}
			copiedInput.PromptArray = normalizedPromptArray
		}
		return copiedInput
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		originalMessages := req.ChatRequest.Input
		normalizedMessages := make([]schemas.ChatMessage, 0, len(originalMessages))

		for _, msg := range originalMessages {
			// Skip system messages if configured to exclude them
			if plugin.config.ExcludeSystemPrompt != nil && *plugin.config.ExcludeSystemPrompt && msg.Role == schemas.ChatMessageRoleSystem {
				continue
			}

			// Create a deep copy of the message with normalized content
			normalizedMsg := schemas.DeepCopyChatMessage(msg)

			// Normalize message content
			if msg.Content.ContentStr != nil {
				normalizedContent := normalizeText(*msg.Content.ContentStr)
				normalizedMsg.Content.ContentStr = &normalizedContent
			} else if msg.Content.ContentBlocks != nil {
				// Create a copy of content blocks with normalized text
				normalizedBlocks := make([]schemas.ChatContentBlock, len(msg.Content.ContentBlocks))
				for i, block := range msg.Content.ContentBlocks {
					normalizedBlocks[i] = block
					if block.Text != nil {
						normalizedText := normalizeText(*block.Text)
						normalizedBlocks[i].Text = &normalizedText
					}
				}
				normalizedMsg.Content.ContentBlocks = normalizedBlocks
			}

			normalizedMessages = append(normalizedMessages, normalizedMsg)
		}
		return normalizedMessages
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		originalMessages := req.ResponsesRequest.Input
		normalizedMessages := make([]schemas.ResponsesMessage, 0, len(originalMessages))

		for _, msg := range originalMessages {
			// Skip system messages if configured to exclude them
			if plugin.config.ExcludeSystemPrompt != nil && *plugin.config.ExcludeSystemPrompt && msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleSystem {
				continue
			}

			// Create a deep copy of the message with normalized content
			normalizedMsg := schemas.DeepCopyResponsesMessage(msg)

			// Create a deep copy of the Content to avoid modifying the original
			if msg.Content != nil {
				if msg.Content.ContentStr != nil {
					normalizedText := normalizeText(*msg.Content.ContentStr)
					normalizedMsg.Content.ContentStr = &normalizedText
				} else if msg.Content.ContentBlocks != nil {
					// Create a copy of content blocks with normalized text
					normalizedBlocks := make([]schemas.ResponsesMessageContentBlock, len(msg.Content.ContentBlocks))
					for i, block := range msg.Content.ContentBlocks {
						normalizedBlocks[i] = block
						if block.Text != nil {
							normalizedText := normalizeText(*block.Text)
							normalizedBlocks[i].Text = &normalizedText
						}
					}
					normalizedMsg.Content.ContentBlocks = normalizedBlocks
				}
			}

			normalizedMessages = append(normalizedMessages, normalizedMsg)
		}
		return normalizedMessages
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		return normalizeText(req.SpeechRequest.Input.Input)
	case schemas.EmbeddingRequest:
		// Create a deep copy of the input to avoid mutating the original request
		copiedInput := schemas.EmbeddingInput{}
		if req.EmbeddingRequest.Input.Text != nil {
			copiedText := *req.EmbeddingRequest.Input.Text
			copiedInput.Text = &copiedText
		} else if len(req.EmbeddingRequest.Input.Texts) > 0 {
			copiedTexts := make([]string, len(req.EmbeddingRequest.Input.Texts))
			copy(copiedTexts, req.EmbeddingRequest.Input.Texts)
			copiedInput.Texts = copiedTexts
		} else if req.EmbeddingRequest.Input.Embedding != nil {
			copiedEmbedding := make([]int, len(req.EmbeddingRequest.Input.Embedding))
			copy(copiedEmbedding, req.EmbeddingRequest.Input.Embedding)
			copiedInput.Embedding = copiedEmbedding
		} else if req.EmbeddingRequest.Input.Embeddings != nil {
			copiedEmbeddings := make([][]int, len(req.EmbeddingRequest.Input.Embeddings))
			copy(copiedEmbeddings, req.EmbeddingRequest.Input.Embeddings)
			copiedInput.Embeddings = copiedEmbeddings
		}
		if copiedInput.Text != nil {
			normalizedText := normalizeText(*copiedInput.Text)
			copiedInput.Text = &normalizedText
		} else if len(copiedInput.Texts) > 0 {
			normalizedTexts := make([]string, len(copiedInput.Texts))
			for i, text := range copiedInput.Texts {
				normalizedTexts[i] = normalizeText(text)
			}
			copiedInput.Texts = normalizedTexts
		}
		return copiedInput
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

// removeField removes the first occurrence of target from the slice.
func removeField(arr []string, target string) []string {
	for i, v := range arr {
		if v == target {
			// remove element at index i
			return append(arr[:i], arr[i+1:]...)
		}
	}
	return arr // unchanged if target not found
}

// extractChatParametersToMetadata extracts Chat API parameters into metadata map
func (plugin *Plugin) extractChatParametersToMetadata(params *schemas.ChatParameters, metadata map[string]interface{}) {
	if params.ToolChoice != nil {
		if params.ToolChoice.ChatToolChoiceStr != nil {
			metadata["tool_choice"] = *params.ToolChoice.ChatToolChoiceStr
		} else if params.ToolChoice.ChatToolChoiceStruct != nil && params.ToolChoice.ChatToolChoiceStruct.Function.Name != "" {
			metadata["tool_choice"] = params.ToolChoice.ChatToolChoiceStruct.Function.Name
		}
	}
	if params.Temperature != nil {
		metadata["temperature"] = *params.Temperature
	}
	if params.TopP != nil {
		metadata["top_p"] = *params.TopP
	}
	if params.MaxCompletionTokens != nil {
		metadata["max_tokens"] = *params.MaxCompletionTokens
	}
	if params.Stop != nil {
		metadata["stop_sequences"] = params.Stop
	}
	if params.PresencePenalty != nil {
		metadata["presence_penalty"] = *params.PresencePenalty
	}
	if params.FrequencyPenalty != nil {
		metadata["frequency_penalty"] = *params.FrequencyPenalty
	}
	if params.ParallelToolCalls != nil {
		metadata["parallel_tool_calls"] = *params.ParallelToolCalls
	}
	if params.User != nil {
		metadata["user"] = *params.User
	}
	if params.LogitBias != nil {
		metadata["logit_bias"] = *params.LogitBias
	}
	if params.LogProbs != nil {
		metadata["logprobs"] = *params.LogProbs
	}
	if params.Modalities != nil {
		metadata["modalities"] = params.Modalities
	}
	if params.PromptCacheKey != nil {
		metadata["prompt_cache_key"] = *params.PromptCacheKey
	}
	if params.Reasoning != nil && params.Reasoning.Effort != nil {
		metadata["reasoning_effort"] = *params.Reasoning.Effort
	}
	if params.ResponseFormat != nil {
		metadata["response_format"] = params.ResponseFormat
	}
	if params.SafetyIdentifier != nil {
		metadata["safety_identifier"] = *params.SafetyIdentifier
	}
	if params.Seed != nil {
		metadata["seed"] = *params.Seed
	}
	if params.ServiceTier != nil {
		metadata["service_tier"] = *params.ServiceTier
	}
	if params.Store != nil {
		metadata["store"] = *params.Store
	}
	if params.TopLogProbs != nil {
		metadata["top_logprobs"] = *params.TopLogProbs
	}
	if params.Verbosity != nil {
		metadata["verbosity"] = *params.Verbosity
	}
	if len(params.ExtraParams) > 0 {
		maps.Copy(metadata, params.ExtraParams)
	}
	if len(params.Tools) > 0 {
		if toolsJSON, err := json.Marshal(params.Tools); err != nil {
			plugin.logger.Warn("%s Failed to marshal tools for metadata: %v", PluginLoggerPrefix, err)
		} else {
			toolHash := xxhash.Sum64(toolsJSON)
			metadata["tools_hash"] = fmt.Sprintf("%x", toolHash)
		}
	}
}

// extractResponsesParametersToMetadata extracts Responses API parameters into metadata map
func (plugin *Plugin) extractResponsesParametersToMetadata(params *schemas.ResponsesParameters, metadata map[string]interface{}) {
	if params.ToolChoice != nil {
		if params.ToolChoice.ResponsesToolChoiceStr != nil {
			metadata["tool_choice"] = *params.ToolChoice.ResponsesToolChoiceStr
		} else if params.ToolChoice.ResponsesToolChoiceStruct != nil && params.ToolChoice.ResponsesToolChoiceStruct.Name != nil {
			metadata["tool_choice"] = *params.ToolChoice.ResponsesToolChoiceStruct.Name
		}
	}
	if params.Temperature != nil {
		metadata["temperature"] = *params.Temperature
	}
	if params.TopP != nil {
		metadata["top_p"] = *params.TopP
	}
	if params.MaxOutputTokens != nil {
		metadata["max_tokens"] = *params.MaxOutputTokens
	}
	if params.ParallelToolCalls != nil {
		metadata["parallel_tool_calls"] = *params.ParallelToolCalls
	}
	if params.Background != nil {
		metadata["background"] = *params.Background
	}
	if params.Conversation != nil {
		metadata["conversation"] = *params.Conversation
	}
	if params.Include != nil {
		metadata["include"] = params.Include
	}
	if params.Instructions != nil {
		metadata["instructions"] = *params.Instructions
	}
	if params.MaxToolCalls != nil {
		metadata["max_tool_calls"] = *params.MaxToolCalls
	}
	if params.PreviousResponseID != nil {
		metadata["previous_response_id"] = *params.PreviousResponseID
	}
	if params.PromptCacheKey != nil {
		metadata["prompt_cache_key"] = *params.PromptCacheKey
	}
	if params.Reasoning != nil {
		if params.Reasoning.Effort != nil {
			metadata["reasoning_effort"] = *params.Reasoning.Effort
		}
		if params.Reasoning.MaxTokens != nil {
			metadata["reasoning_max_tokens"] = *params.Reasoning.MaxTokens
		}
		if params.Reasoning.Summary != nil {
			metadata["reasoning_summary"] = *params.Reasoning.Summary
		}
	}
	if params.SafetyIdentifier != nil {
		metadata["safety_identifier"] = *params.SafetyIdentifier
	}
	if params.ServiceTier != nil {
		metadata["service_tier"] = *params.ServiceTier
	}
	if params.Store != nil {
		metadata["store"] = *params.Store
	}
	if params.Text != nil {
		if params.Text.Verbosity != nil {
			metadata["text_verbosity"] = *params.Text.Verbosity
		}
		if params.Text.Format != nil {
			metadata["text_format_type"] = params.Text.Format.Type
		}
	}
	if params.TopLogProbs != nil {
		metadata["top_logprobs"] = *params.TopLogProbs
	}
	if params.Truncation != nil {
		metadata["truncation"] = *params.Truncation
	}
	if len(params.ExtraParams) > 0 {
		maps.Copy(metadata, params.ExtraParams)
	}
	if len(params.Tools) > 0 {
		if toolsJSON, err := json.Marshal(params.Tools); err != nil {
			plugin.logger.Warn("%s Failed to marshal tools for metadata: %v", PluginLoggerPrefix, err)
		} else {
			toolHash := xxhash.Sum64(toolsJSON)
			metadata["tools_hash"] = fmt.Sprintf("%x", toolHash)
		}
	}
}

// extractTextCompletionParametersToMetadata extracts Text Completion parameters into metadata map
func (plugin *Plugin) extractTextCompletionParametersToMetadata(params *schemas.TextCompletionParameters, metadata map[string]interface{}) {
	if params.Temperature != nil {
		metadata["temperature"] = *params.Temperature
	}
	if params.TopP != nil {
		metadata["top_p"] = *params.TopP
	}
	if params.MaxTokens != nil {
		metadata["max_tokens"] = *params.MaxTokens
	}
	if params.Stop != nil {
		metadata["stop_sequences"] = params.Stop
	}
	if params.PresencePenalty != nil {
		metadata["presence_penalty"] = *params.PresencePenalty
	}
	if params.FrequencyPenalty != nil {
		metadata["frequency_penalty"] = *params.FrequencyPenalty
	}
	if params.User != nil {
		metadata["user"] = *params.User
	}
	if params.BestOf != nil {
		metadata["best_of"] = *params.BestOf
	}
	if params.Echo != nil {
		metadata["echo"] = *params.Echo
	}
	if params.LogitBias != nil {
		metadata["logit_bias"] = *params.LogitBias
	}
	if params.LogProbs != nil {
		metadata["logprobs"] = *params.LogProbs
	}
	if params.N != nil {
		metadata["n"] = *params.N
	}
	if params.Seed != nil {
		metadata["seed"] = *params.Seed
	}
	if params.Suffix != nil {
		metadata["suffix"] = *params.Suffix
	}
	if len(params.ExtraParams) > 0 {
		maps.Copy(metadata, params.ExtraParams)
	}
}

// extractSpeechParametersToMetadata extracts Speech parameters into metadata map
func (plugin *Plugin) extractSpeechParametersToMetadata(params *schemas.SpeechParameters, metadata map[string]interface{}) {
	if params == nil {
		return
	}

	if params.Speed != nil {
		metadata["speed"] = *params.Speed
	}
	if params.ResponseFormat != "" {
		metadata["response_format"] = params.ResponseFormat
	}
	if params.Instructions != "" {
		metadata["instructions"] = params.Instructions
	}
	// Check if VoiceConfig.Voice is non-nil before accessing it
	if params.VoiceConfig.Voice != nil {
		metadata["voice"] = *params.VoiceConfig.Voice
	}
	if len(params.VoiceConfig.MultiVoiceConfig) > 0 {
		flattenedVC := make([]string, len(params.VoiceConfig.MultiVoiceConfig))
		for i, vc := range params.VoiceConfig.MultiVoiceConfig {
			flattenedVC[i] = fmt.Sprintf("%s:%s", vc.Speaker, vc.Voice)
		}
		metadata["multi_voice_count"] = flattenedVC
	}
	if len(params.ExtraParams) > 0 {
		maps.Copy(metadata, params.ExtraParams)
	}
}

// extractEmbeddingParametersToMetadata extracts Embedding parameters into metadata map
func (plugin *Plugin) extractEmbeddingParametersToMetadata(params *schemas.EmbeddingParameters, metadata map[string]interface{}) {
	if params.EncodingFormat != nil {
		metadata["encoding_format"] = *params.EncodingFormat
	}
	if params.Dimensions != nil {
		metadata["dimensions"] = *params.Dimensions
	}
	if len(params.ExtraParams) > 0 {
		maps.Copy(metadata, params.ExtraParams)
	}
}

// extractTranscriptionParametersToMetadata extracts Transcription parameters into metadata map
func (plugin *Plugin) extractTranscriptionParametersToMetadata(params *schemas.TranscriptionParameters, metadata map[string]interface{}) {
	if params.Language != nil {
		metadata["language"] = *params.Language
	}
	if params.ResponseFormat != nil {
		metadata["response_format"] = *params.ResponseFormat
	}
	if params.Prompt != nil {
		metadata["prompt"] = *params.Prompt
	}
	if params.Format != nil {
		metadata["file_format"] = *params.Format
	}
	if len(params.ExtraParams) > 0 {
		maps.Copy(metadata, params.ExtraParams)
	}
}

// extractImageGenerationParametersToMetadata extracts Image Generation parameters into metadata map
func (plugin *Plugin) extractImageGenerationParametersToMetadata(params *schemas.ImageGenerationParameters, metadata map[string]interface{}) {
	if params == nil {
		return
	}

	if params.N != nil {
		metadata["n"] = *params.N
	}
	if params.Background != nil {
		metadata["background"] = *params.Background
	}
	if params.Moderation != nil {
		metadata["moderation"] = *params.Moderation
	}
	if params.PartialImages != nil {
		metadata["partial_images"] = *params.PartialImages
	}
	if params.Size != nil {
		metadata["size"] = *params.Size
	}
	if params.Quality != nil {
		metadata["quality"] = *params.Quality
	}
	if params.OutputCompression != nil {
		metadata["output_compression"] = *params.OutputCompression
	}
	if params.OutputFormat != nil {
		metadata["output_format"] = *params.OutputFormat
	}
	if params.Style != nil {
		metadata["style"] = *params.Style
	}
	if params.ResponseFormat != nil {
		metadata["response_format"] = *params.ResponseFormat
	}
	if params.Seed != nil {
		metadata["seed"] = *params.Seed
	}
	if params.NegativePrompt != nil {
		metadata["negative_prompt"] = *params.NegativePrompt
	}
	if params.NumInferenceSteps != nil {
		metadata["num_inference_steps"] = *params.NumInferenceSteps
	}
	if params.User != nil {
		metadata["user"] = *params.User
	}

	if len(params.ExtraParams) > 0 {
		maps.Copy(metadata, params.ExtraParams)
	}
}

func (plugin *Plugin) isConversationHistoryThresholdExceeded(req *schemas.BifrostRequest) bool {
	switch {
	case req.ChatRequest != nil:
		input, ok := plugin.getInputForCaching(req).([]schemas.ChatMessage)
		if !ok {
			return false
		}
		if len(input) > plugin.config.ConversationHistoryThreshold {
			return true
		}
		return false
	case req.ResponsesRequest != nil:
		input, ok := plugin.getInputForCaching(req).([]schemas.ResponsesMessage)
		if !ok {
			return false
		}
		if len(input) > plugin.config.ConversationHistoryThreshold {
			return true
		}
		return false
	default:
		return false
	}
}
