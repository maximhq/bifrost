package semanticcache

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/cespare/xxhash/v2"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// generateEmbedding generates an embedding for the given text using the configured provider.
func (plugin *Plugin) generateEmbedding(ctx context.Context, text string) ([]float32, error) {
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
		return nil, fmt.Errorf("failed to generate embedding: %v", err)
	}

	// Extract the first embedding from response
	if len(response.Data) == 0 {
		return nil, fmt.Errorf("no embeddings returned from provider")
	}

	// Get the embedding from the first data item
	embedding := response.Data[0].Embedding
	if embedding.EmbeddingArray != nil {
		return *embedding.EmbeddingArray, nil
	} else if embedding.Embedding2DArray != nil && len(*embedding.Embedding2DArray) > 0 {
		// Flatten 2D array into single embedding
		var flattened []float32
		for _, arr := range *embedding.Embedding2DArray {
			flattened = append(flattened, arr...)
		}
		return flattened, nil
	}

	return nil, fmt.Errorf("embedding data is not in expected format")
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
func (plugin *Plugin) generateRequestHash(req *schemas.BifrostRequest, requestType bifrost.RequestType) (string, error) {
	// Create a hash input structure that includes both input and parameters
	hashInput := struct {
		Input  schemas.RequestInput     `json:"input"`
		Params *schemas.ModelParameters `json:"params,omitempty"`
		Stream bool                     `json:"stream,omitempty"`
	}{
		Input:  req.Input,
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
func (plugin *Plugin) extractTextForEmbedding(req *schemas.BifrostRequest, requestType bifrost.RequestType) (string, map[string]interface{}, error) {
	metadata := map[string]interface{}{}

	attachments := []string{}

	// Add parameters as metadata if present
	if req.Params != nil {
		if req.Params.ToolChoice != nil {
			if req.Params.ToolChoice.ToolChoiceStr != nil {
				metadata["tool_choice"] = *req.Params.ToolChoice.ToolChoiceStr
			} else if req.Params.ToolChoice.ToolChoiceStruct != nil {
				metadata["tool_choice"] = (*req.Params.ToolChoice.ToolChoiceStruct).Function.Name
			}
		}
		if req.Params.Temperature != nil {
			metadata["temperature"] = *req.Params.Temperature
		}
		if req.Params.TopP != nil {
			metadata["top_p"] = *req.Params.TopP
		}
		if req.Params.TopK != nil {
			metadata["top_k"] = *req.Params.TopK
		}
		if req.Params.MaxTokens != nil {
			metadata["max_tokens"] = *req.Params.MaxTokens
		}
		if req.Params.StopSequences != nil {
			metadata["stop_sequences"] = *req.Params.StopSequences
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
		return *req.Input.TextCompletionInput, metadata, nil

	case req.Input.ChatCompletionInput != nil:
		// Serialize chat messages for embedding
		var textParts []string
		for _, msg := range *req.Input.ChatCompletionInput {
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
					if block.ImageURL != nil && block.ImageURL.URL != "" {
						attachments = append(attachments, block.ImageURL.URL)
					}
				}
				content = strings.Join(blockTexts, " ")
			}

			if content != "" {
				textParts = append(textParts, fmt.Sprintf("%s: %s", msg.Role, content))
			}
		}

		if len(textParts) == 0 {
			return "", nil, fmt.Errorf("no text content found in chat messages")
		}

		if len(attachments) > 0 {
			metadata["attachments"] = attachments
		}

		return strings.Join(textParts, "\n"), metadata, nil

	case req.Input.SpeechInput != nil:
		if req.Input.SpeechInput.Input != "" {
			if req.Input.SpeechInput.VoiceConfig.Voice != nil {
				metadata["voice"] = *req.Input.SpeechInput.VoiceConfig.Voice
			}
			return req.Input.SpeechInput.Input, metadata, nil
		}
		return "", nil, fmt.Errorf("no input text found in speech request")

	case req.Input.EmbeddingInput != nil:
		// Skip semantic caching for embedding requests
		return "", nil, fmt.Errorf("embedding requests are not supported for semantic caching")

	case req.Input.TranscriptionInput != nil:
		// Skip semantic caching for transcription requests
		return "", nil, fmt.Errorf("transcription requests are not supported for semantic caching")

	default:
		return "", nil, fmt.Errorf("unsupported input type for semantic caching")
	}
}

// generateCachePattern creates search patterns for cache lookup.
// It uses the format: {provider}-{model}-*-{suffix}
// Provider and model are included based on CacheByProvider and CacheByModel configuration.
//
// Parameters:
//   - req: The Bifrost request
//   - suffix: Either "hash" or "response"
//
// Returns:
//   - string: The formatted cache pattern for searching
func (plugin *Plugin) generateCachePattern(req *schemas.BifrostRequest, requestID string, suffix string) string {
	var provider, model, reqid string

	// Include provider based on configuration
	if plugin.config.CacheByProvider != nil && *plugin.config.CacheByProvider {
		provider = string(req.Provider)
	} else {
		provider = "*"
	}

	// Include model based on configuration
	if plugin.config.CacheByModel != nil && *plugin.config.CacheByModel {
		model = req.Model
	} else {
		model = "*"
	}

	if requestID != "" {
		reqid = requestID
	} else {
		reqid = "*"
	}

	return fmt.Sprintf("%s%s-%s-reqid-%s-%s", plugin.config.Prefix, provider, model, reqid, suffix)
}

// generateCacheKey creates cache keys for storing hash and response.
// It uses the format: {provider}-{model}-{reqid}-{suffix}
// Provider and model are included based on CacheByProvider and CacheByModel configuration.
//
// Parameters:
//   - req: The Bifrost request
//   - requestID: The UUID for this request
//   - suffix: Either "hash" or "response"
//
// Returns:
//   - string: The formatted cache key
func (plugin *Plugin) generateCacheKey(provider schemas.ModelProvider, model string, requestID, suffix string) string {
	// Include provider based on configuration
	if !(plugin.config.CacheByProvider != nil && *plugin.config.CacheByProvider) {
		provider = "*"
	}

	// Include model based on configuration
	if !(plugin.config.CacheByModel != nil && *plugin.config.CacheByModel) {
		model = "*"
	}

	return fmt.Sprintf("%s%s-%s-reqid-%s-%s", plugin.config.Prefix, provider, model, requestID, suffix)
}

// Note: getNonStreamingResponseForRequestID moved to search.go with new VectorStore interface implementation

// Note: getStreamingResponseForRequestID moved to search.go with new VectorStore interface implementation

func (plugin *Plugin) isStreamingRequest(requestType bifrost.RequestType) bool {
	return requestType == bifrost.ChatCompletionStreamRequest ||
		requestType == bifrost.SpeechStreamRequest ||
		requestType == bifrost.TranscriptionStreamRequest
}
