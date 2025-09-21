package openai

import "github.com/maximhq/bifrost/core/schemas"

// ToOpenAIStreamResponse converts a Bifrost response to OpenAI streaming format
func ToOpenAIChatCompletionStreamResponse(bifrostResp *schemas.BifrostResponse) *OpenAIChatCompletionStreamResponse {
	if bifrostResp == nil {
		return nil
	}

	streamResp := &OpenAIChatCompletionStreamResponse{
		ID:                bifrostResp.ID,
		Object:            "chat.completion.chunk",
		Created:           bifrostResp.Created,
		Model:             bifrostResp.Model,
		SystemFingerprint: bifrostResp.SystemFingerprint,
		Usage:             bifrostResp.Usage,
	}

	// Convert choices to streaming format
	for _, choice := range bifrostResp.Choices {
		streamChoice := OpenAIChatCompletionStreamChoice{
			Index:        choice.Index,
			FinishReason: choice.FinishReason,
		}

		var delta *OpenAIChatCompletionStreamDelta

		// Handle streaming vs non-streaming choices
		if choice.BifrostStreamResponseChoice != nil {
			// This is a streaming response - use the delta directly
			delta = &OpenAIChatCompletionStreamDelta{}

			// Only set fields that are not nil
			if choice.BifrostStreamResponseChoice.Delta.Role != nil {
				delta.Role = choice.BifrostStreamResponseChoice.Delta.Role
			}
			if choice.BifrostStreamResponseChoice.Delta.Content != nil {
				delta.Content = choice.BifrostStreamResponseChoice.Delta.Content
			}
			if len(choice.BifrostStreamResponseChoice.Delta.ToolCalls) > 0 {
				delta.ToolCalls = &choice.BifrostStreamResponseChoice.Delta.ToolCalls
			}
		} else if choice.BifrostNonStreamResponseChoice != nil {
			// This is a non-streaming response - convert message to delta format
			delta = &OpenAIChatCompletionStreamDelta{}

			// Convert role
			role := string(choice.BifrostNonStreamResponseChoice.Message.Role)
			delta.Role = &role

			// Convert content
			if choice.BifrostNonStreamResponseChoice.Message.Content.ContentStr != nil {
				delta.Content = choice.BifrostNonStreamResponseChoice.Message.Content.ContentStr
			}

			// Convert tool calls if present (from AssistantMessage)
			if choice.BifrostNonStreamResponseChoice.Message.AssistantMessage != nil &&
				choice.BifrostNonStreamResponseChoice.Message.AssistantMessage.ToolCalls != nil {
				delta.ToolCalls = choice.BifrostNonStreamResponseChoice.Message.AssistantMessage.ToolCalls
			}

			// Set LogProbs from non-streaming choice
			if choice.LogProbs != nil {
				streamChoice.LogProbs = choice.LogProbs
			}
		}

		// Ensure we have a valid delta with at least one field set
		// If all fields are nil, we should skip this chunk or set an empty content
		if delta != nil {
			hasValidField := (delta.Role != nil) || (delta.Content != nil) || (delta.ToolCalls != nil)
			if !hasValidField {
				// Set empty content to ensure we have at least one field
				emptyContent := ""
				delta.Content = &emptyContent
			}
			streamChoice.Delta = delta
		}

		streamResp.Choices = append(streamResp.Choices, streamChoice)
	}

	return streamResp
}
