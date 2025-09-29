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
func (plugin *Plugin) generateEmbedding(ctx context.Context, text string) ([]float32, int, error) {
	// Create embedding request
	embeddingReq := &schemas.BifrostRequest{
		Provider: plugin.config.Provider,
		Model:    plugin.config.EmbeddingModel,
		Input: schemas.RequestInput{
			EmbeddingInput: &schemas.EmbeddingInput{
				Texts: []string{text},
			},
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
		return *embedding.EmbeddingArray, inputTokens, nil
	} else if embedding.Embedding2DArray != nil && len(*embedding.Embedding2DArray) > 0 {
		// Flatten 2D array into single embedding
		var flattened []float32
		for _, arr := range *embedding.Embedding2DArray {
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
func (plugin *Plugin) generateRequestHash(req *schemas.BifrostRequest, requestType schemas.RequestType) (string, error) {
	// Create a hash input structure that includes both input and parameters
	hashInput := struct {
		Input  schemas.RequestInput     `json:"input"`
		Params *schemas.ModelParameters `json:"params,omitempty"`
		Stream bool                     `json:"stream,omitempty"`
	}{
		Input:  *plugin.getInputForCaching(req),
		Params: req.Params,
		Stream: plugin.isStreamingRequest(requestType),
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
func (plugin *Plugin) extractTextForEmbedding(req *schemas.BifrostRequest, requestType schemas.RequestType) (string, string, error) {
	metadata := map[string]interface{}{}

	attachments := []string{}

	// Add parameters as metadata if present
	if req.Params != nil {
		if req.Params.ToolChoice != nil {
			if req.Params.ToolChoice.ToolChoiceStr != nil {
				metadata["tool_choice"] = *req.Params.ToolChoice.ToolChoiceStr
			} else if req.Params.ToolChoice.ChatToolChoice != nil {
				metadata["tool_choice"] = req.Params.ToolChoice.ChatToolChoice.Function.Name
			}
		}
		if req.Params.Temperature != nil {
			metadata["temperature"] = *req.Params.Temperature
		}
		if req.Params.TopP != nil {
			metadata["top_p"] = *req.Params.TopP
		}
		if req.Params.MaxCompletionTokens != nil {
			metadata["max_tokens"] = *req.Params.MaxCompletionTokens
		}
		if req.Params.Stop != nil {
			metadata["stop_sequences"] = *req.Params.Stop
		}
		if req.Params.PresencePenalty != nil {
			metadata["presence_penalty"] = *req.Params.PresencePenalty
		}
		if req.Params.FrequencyPenalty != nil {
			metadata["frequency_penalty"] = *req.Params.FrequencyPenalty
		}
		if req.Params.ParallelToolCalls != nil {
			metadata["parallel_tool_calls"] = *req.Params.ParallelToolCalls
		}
		if req.Params.User != nil {
			metadata["user"] = *req.Params.User
		}

		if len(req.Params.ExtraParams) > 0 {
			maps.Copy(metadata, req.Params.ExtraParams)
		}
	}

	metadata["stream"] = plugin.isStreamingRequest(requestType)

	if req.Params != nil && req.Params.Tools != nil {
		if toolsJSON, err := json.Marshal(*req.Params.Tools); err != nil {
			plugin.logger.Warn(fmt.Sprintf("%s Failed to marshal tools for metadata: %v", PluginLoggerPrefix, err))
		} else {
			toolHash := xxhash.Sum64(toolsJSON)
			metadata["tools_hash"] = fmt.Sprintf("%x", toolHash)
		}
	}

	switch {
	case req.Input.TextCompletionInput != nil:
		metadataHash, err := getMetadataHash(metadata)
		if err != nil {
			return "", "", fmt.Errorf("failed to marshal metadata for metadata hash: %w", err)
		}

		var textContent string
		if req.Input.TextCompletionInput.Prompt != nil {
			textContent = *req.Input.TextCompletionInput.Prompt
		} else if len(req.Input.TextCompletionInput.PromptArray) > 0 {
			textContent = strings.Join(req.Input.TextCompletionInput.PromptArray, " ")
		}
		return textContent, metadataHash, nil

	case req.Input.ChatCompletionInput != nil:
		reqInput := plugin.getInputForCaching(req)

		// Serialize chat messages for embedding
		var textParts []string
		for _, msg := range *reqInput.ChatCompletionInput {
			// Extract content as string
			var content string
			if msg.Content.ContentStr != nil {
				content = *msg.Content.ContentStr
			} else if msg.Content.ContentBlocks != nil {
				// For content blocks, extract text parts
				var blockTexts []string
				for _, block := range *msg.Content.ContentBlocks {
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
				textParts = append(textParts, fmt.Sprintf("%s: %s", msg.Role, content))
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

	case req.Input.SpeechInput != nil:
		if req.Input.SpeechInput.Input != "" {
			if req.Input.SpeechInput.VoiceConfig.Voice != nil {
				metadata["voice"] = *req.Input.SpeechInput.VoiceConfig.Voice
			}

			metadataHash, err := getMetadataHash(metadata)
			if err != nil {
				return "", "", fmt.Errorf("failed to marshal metadata for metadata hash: %w", err)
			}

			return req.Input.SpeechInput.Input, metadataHash, nil
		}
		return "", "", fmt.Errorf("no input text found in speech request")

	case req.Input.EmbeddingInput != nil:
		metadataHash, err := getMetadataHash(metadata)
		if err != nil {
			return "", "", fmt.Errorf("failed to marshal metadata for metadata hash: %w", err)
		}

		texts := req.Input.EmbeddingInput.Texts

		if len(texts) == 0 && req.Input.EmbeddingInput.Text != nil {
			texts = []string{*req.Input.EmbeddingInput.Text}
		}

		var text string
		for _, t := range texts {
			text += t + " "
		}

		return strings.TrimSpace(text), metadataHash, nil

	case req.Input.TranscriptionInput != nil:
		// Skip semantic caching for transcription requests
		return "", "", fmt.Errorf("transcription requests are not supported for semantic caching")

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

// isStreamingRequest checks if the request is a streaming request
func (plugin *Plugin) isStreamingRequest(requestType schemas.RequestType) bool {
	return requestType == schemas.ChatCompletionStreamRequest ||
		requestType == schemas.SpeechStreamRequest ||
		requestType == schemas.TranscriptionStreamRequest
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
	} else if res != nil && len(res.Choices) > 0 {
		choice := res.Choices[0]
		if choice.BifrostStreamResponseChoice != nil {
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
			plugin.logger.Warn(fmt.Sprintf("%s Failed to process accumulated stream for request %s: %v", PluginLoggerPrefix, responseID, processErr))
		}
	}

	return nil
}

// getInputForCaching returns a normalized and sanitized copy of req.Input for hashing/embedding.
// It applies text normalization (lowercase + trim) and optionally removes system messages.
func (plugin *Plugin) getInputForCaching(req *schemas.BifrostRequest) *schemas.RequestInput {
	reqInput := req.Input

	// Handle text completion normalization
	if reqInput.TextCompletionInput != nil {
		if reqInput.TextCompletionInput.Prompt != nil {
			normalizedText := normalizeText(*reqInput.TextCompletionInput.Prompt)
			reqInput.TextCompletionInput.Prompt = &normalizedText
		} else if len(reqInput.TextCompletionInput.PromptArray) > 0 {
			for i, prompt := range reqInput.TextCompletionInput.PromptArray {
				reqInput.TextCompletionInput.PromptArray[i] = normalizeText(prompt)
			}
		}
	}

	// Handle chat completion normalization
	if reqInput.ChatCompletionInput != nil {
		originalMessages := *reqInput.ChatCompletionInput
		normalizedMessages := make([]schemas.ChatMessage, 0, len(originalMessages))

		for _, msg := range originalMessages {
			// Skip system messages if configured to exclude them
			if plugin.config.ExcludeSystemPrompt != nil && *plugin.config.ExcludeSystemPrompt && msg.Role == schemas.ChatMessageRoleSystem {
				continue
			}

			// Create a copy of the message with normalized content
			normalizedMsg := msg

			// Normalize message content
			if msg.Content.ContentStr != nil {
				normalizedContent := normalizeText(*msg.Content.ContentStr)
				normalizedMsg.Content.ContentStr = &normalizedContent
			} else if msg.Content.ContentBlocks != nil {
				// Create a copy of content blocks with normalized text
				normalizedBlocks := make([]schemas.ChatContentBlock, len(*msg.Content.ContentBlocks))
				for i, block := range *msg.Content.ContentBlocks {
					normalizedBlocks[i] = block
					if block.Text != nil {
						normalizedText := normalizeText(*block.Text)
						normalizedBlocks[i].Text = &normalizedText
					}
				}
				normalizedMsg.Content.ContentBlocks = &normalizedBlocks
			}

			normalizedMessages = append(normalizedMessages, normalizedMsg)
		}

		reqInput.ChatCompletionInput = &normalizedMessages
	}

	if reqInput.SpeechInput != nil {
		normalizedInput := normalizeText(reqInput.SpeechInput.Input)
		reqInput.SpeechInput.Input = normalizedInput
	}

	return &reqInput
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

// isConversationHistoryThresholdExceeded checks if the conversation history threshold is exceeded
func (plugin *Plugin) isConversationHistoryThresholdExceeded(req *schemas.BifrostRequest) bool {
	switch {
	case req.Input.ChatCompletionInput != nil:
		input := plugin.getInputForCaching(req)
		if len(*input.ChatCompletionInput) > plugin.config.ConversationHistoryThreshold {
			return true
		}
		return false
	default:
		return false
	}
}
