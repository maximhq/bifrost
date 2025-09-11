package openai

import "github.com/maximhq/bifrost/core/schemas"

func filterParams(provider schemas.ModelProvider, p *schemas.ModelParameters) *schemas.ModelParameters {
	if p == nil {
		return nil
	}
	return schemas.ValidateAndFilterParamsForProvider(provider, p)
}

// convertParameters converts OpenAI request parameters to Bifrost ModelParameters
// using direct field access for better performance and type safety.
func (r *OpenAIChatRequest) convertParameters() *schemas.ModelParameters {
	params := &schemas.ModelParameters{
		ExtraParams: make(map[string]interface{}),
	}

	params.Tools = r.Tools
	params.ToolChoice = r.ToolChoice

	// Direct field mapping
	if r.MaxCompletionTokens != nil {
		params.MaxCompletionTokens = r.MaxCompletionTokens
	}
	if r.Temperature != nil {
		params.Temperature = r.Temperature
	}
	if r.TopP != nil {
		params.TopP = r.TopP
	}
	if r.PresencePenalty != nil {
		params.PresencePenalty = r.PresencePenalty
	}
	if r.FrequencyPenalty != nil {
		params.FrequencyPenalty = r.FrequencyPenalty
	}
	if r.LogProbs != nil {
		params.LogProbs = r.LogProbs
	}
	if r.TopLogProbs != nil {
		params.TopLogProbs = r.TopLogProbs
	}
	if r.Stop != nil {
		params.Stop = r.Stop
	}
	if r.LogitBias != nil {
		params.LogitBias = r.LogitBias
	}
	if r.User != nil {
		params.User = r.User
	}
	if r.Seed != nil {
		params.Seed = r.Seed
	}
	if r.StreamOptions != nil {
		params.StreamOptions = r.StreamOptions
	}
	if r.ResponseFormat != nil {
		params.ResponseFormat = r.ResponseFormat
	}
	if r.ReasoningEffort != nil {
		params.ReasoningEffort = r.ReasoningEffort
	}

	return params
}

// convertEmbeddingParameters converts OpenAI embedding request parameters to Bifrost ModelParameters
func (r *OpenAIEmbeddingRequest) convertEmbeddingParameters() *schemas.ModelParameters {
	params := &schemas.ModelParameters{
		ExtraParams: make(map[string]interface{}),
	}

	// Add embedding-specific parameters
	if r.EncodingFormat != nil {
		params.EncodingFormat = r.EncodingFormat
	}
	if r.Dimensions != nil {
		params.Dimensions = r.Dimensions
	}
	if r.User != nil {
		params.User = r.User
	}

	return params
}

// prepareOpenAIChatRequest formats messages for the OpenAI API.
// It handles both text and image content in messages.
// Returns a slice of formatted messages and any additional parameters.
// sanitizeBifrostMessages performs in-place image URL sanitization for any content blocks
// with type image_url in the provided Bifrost messages and returns the sanitized slice.
func sanitizeChatImageInputs(messages []schemas.ChatMessage) []schemas.ChatMessage {
	if len(messages) == 0 {
		return messages
	}
	for mi := range messages {
		content := &messages[mi].Content
		if content.ContentBlocks == nil {
			continue
		}
		blocks := *content.ContentBlocks
		for bi := range blocks {
			if blocks[bi].Type == schemas.ChatContentBlockTypeImage && blocks[bi].ImageURLStruct != nil {
				if sanitizedURL, _ := schemas.SanitizeImageURL(blocks[bi].ImageURLStruct.URL); sanitizedURL != "" {
					blocks[bi].ImageURLStruct = &schemas.ChatInputImage{
						URL:    sanitizedURL,
						Detail: blocks[bi].ImageURLStruct.Detail,
					}
				}
			}
		}
		*content.ContentBlocks = blocks
	}
	return messages
}

func sanitizeResponsesImageInputs(messages []schemas.ResponsesMessage) []schemas.ResponsesMessage {
	if len(messages) == 0 {
		return messages
	}
	for mi := range messages {
		// Check if ResponsesInputMessage is not nil before accessing Content
		if messages[mi].Content == nil {
			continue
		}
		content := messages[mi].Content
		if content.ContentBlocks == nil {
			continue
		}
		blocks := *content.ContentBlocks
		for bi := range blocks {
			if blocks[bi].Type == schemas.ResponsesInputMessageContentBlockTypeImage && blocks[bi].ResponsesInputMessageContentBlockImage != nil {
				if blocks[bi].ResponsesInputMessageContentBlockImage.ImageURL != nil {
					if sanitizedURL, _ := schemas.SanitizeImageURL(*blocks[bi].ResponsesInputMessageContentBlockImage.ImageURL); sanitizedURL != "" {
						blocks[bi].ResponsesInputMessageContentBlockImage = &schemas.ResponsesInputMessageContentBlockImage{
							ImageURL: &sanitizedURL,
							Detail:   blocks[bi].ResponsesInputMessageContentBlockImage.Detail,
						}
					}
				}
			}
		}
		*content.ContentBlocks = blocks
	}
	return messages
}
