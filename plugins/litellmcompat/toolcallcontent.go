package litellmcompat

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// transformToolCallContentResponse populates empty content with tool call arguments
// when a chat response has tool_calls but no content.
// This is a response-only transform.
func transformToolCallContentResponse(resp *schemas.BifrostResponse) *schemas.BifrostResponse {
	// Only process chat responses
	if resp == nil || resp.ChatResponse == nil {
		return resp
	}
	chatResp := resp.ChatResponse
	// Process each choice
	for i := range chatResp.Choices {
		choice := &chatResp.Choices[i]
		// Handle non-streaming response
		if choice.ChatNonStreamResponseChoice != nil && choice.ChatNonStreamResponseChoice.Message != nil {
			msg := choice.ChatNonStreamResponseChoice.Message
			// Check if we have tool calls but empty content
			if msg.ChatAssistantMessage != nil && len(msg.ChatAssistantMessage.ToolCalls) > 0 {
				if isContentEmpty(msg.Content) {
					// Build content from tool call arguments
					content := buildContentFromToolCalls(msg.ChatAssistantMessage.ToolCalls)
					if content != "" {
						msg.Content = &schemas.ChatMessageContent{
							ContentStr: &content,
						}
					}
					resp.ChatResponse.ExtraFields.LiteLLMCompat = true
				}
			}
		}
		// Handle streaming response
		if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
			delta := choice.ChatStreamResponseChoice.Delta

			// Check if we have tool calls but empty content
			if len(delta.ToolCalls) > 0 {
				if delta.Content == nil || *delta.Content == "" {
					// Build content from tool call arguments
					content := buildContentFromToolCalls(delta.ToolCalls)
					if content != "" {
						delta.Content = &content
					}
				}
			}
		}
	}
	isLastChunk := chatResp.Choices[len(chatResp.Choices)-1].FinishReason != nil && chatResp.Usage != nil
	if isLastChunk {
		resp.ChatResponse.ExtraFields.LiteLLMCompat = true
	}
	return resp
}

// isContentEmpty checks if ChatMessageContent is empty or nil
func isContentEmpty(content *schemas.ChatMessageContent) bool {
	if content == nil {
		return true
	}
	if content.ContentStr != nil && *content.ContentStr != "" {
		return false
	}
	if len(content.ContentBlocks) > 0 {
		return false
	}
	return true
}

// buildContentFromToolCalls concatenates tool call arguments into a single string
func buildContentFromToolCalls(toolCalls []schemas.ChatAssistantMessageToolCall) string {
	if len(toolCalls) == 0 {
		return ""
	}
	// If single tool call, return its arguments directly
	if len(toolCalls) == 1 {
		return toolCalls[0].Function.Arguments
	}
	// Multiple tool calls: concatenate with newlines
	var result string
	for i, tc := range toolCalls {
		if i > 0 {
			result += "\n"
		}
		result += tc.Function.Arguments
	}
	return result
}
