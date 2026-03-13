package openai

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// CustomResponseHandler is a function that produces a Bifrost response from a Bifrost request.
// T is the concrete Bifrost response type (e.g. BifrostEmbeddingResponse, BifrostTextCompletionResponse, BifrostChatResponse, BifrostResponsesResponse, BifrostImageGenerationResponse, BifrostTranscriptionResponse).
type responseHandler[T any] func(responseBody []byte, response *T, requestBody []byte, sendBackRawRequest bool, sendBackRawResponse bool) (rawRequest interface{}, rawResponse interface{}, bifrostErr *schemas.BifrostError)

func ConvertOpenAIMessagesToBifrostMessages(messages []OpenAIMessage) []schemas.ChatMessage {
	bifrostMessages := make([]schemas.ChatMessage, len(messages))
	for i, message := range messages {
		bifrostMessages[i] = schemas.ChatMessage{
			Name:            message.Name,
			Role:            message.Role,
			Content:         message.Content,
			ChatToolMessage: message.ChatToolMessage,
		}
		if message.OpenAIChatAssistantMessage != nil {
			bifrostMessages[i].ChatAssistantMessage = &schemas.ChatAssistantMessage{
				Refusal:     message.OpenAIChatAssistantMessage.Refusal,
				Reasoning:   message.OpenAIChatAssistantMessage.Reasoning,
				Annotations: message.OpenAIChatAssistantMessage.Annotations,
				ToolCalls:   message.OpenAIChatAssistantMessage.ToolCalls,
			}
		}
	}
	return bifrostMessages
}

func ConvertBifrostMessagesToOpenAIMessages(messages []schemas.ChatMessage) []OpenAIMessage {
	openaiMessages := make([]OpenAIMessage, len(messages))
	for i, message := range messages {
		openaiMessages[i] = OpenAIMessage{
			Name:            message.Name,
			Role:            message.Role,
			Content:         message.Content,
			ChatToolMessage: message.ChatToolMessage,
		}
		if message.ChatAssistantMessage != nil {
			openaiMessages[i].OpenAIChatAssistantMessage = &OpenAIChatAssistantMessage{
				Refusal:     message.ChatAssistantMessage.Refusal,
				Reasoning:   message.ChatAssistantMessage.Reasoning,
				Annotations: message.ChatAssistantMessage.Annotations,
				ToolCalls:   message.ChatAssistantMessage.ToolCalls,
			}
		}
	}
	return openaiMessages
}

// isOpenAIReasoningModel checks if the given model is an OpenAI reasoning model
// that supports the reasoning.effort parameter.
// OpenAI reasoning models include o1, o3, o4 series and GPT-5.x variants.
// Note: -pro and -codex variants (e.g. gpt-5.2-pro, gpt-5.2-codex) are always-reasoning
// models that do NOT support effort "none" — callers must handle top_p stripping separately.
// TODO we need to find a better way to check if a model is an OpenAI reasoning model
func isOpenAIReasoningModel(model string) bool {
	_, parsedModel := schemas.ParseModelString(model, schemas.OpenAI)
	if parsedModel != "" {
		model = parsedModel
	}
	modelLower := strings.ToLower(model)
	// Check for o1 or o3 series models
	// Match patterns like: o1, o1-mini, o1-preview, o3, o3-mini, etc.
	// Also match gpt-oss models which support reasoning
	if strings.Contains(modelLower, "gpt-oss") {
		return true
	}
	// Check for o1/o3/o4 series - these are reasoning models
	// The pattern matches "o1", "o3", or "o4" followed by end of string, hyphen, or underscore
	for _, prefix := range []string{"o1", "o3", "o4"} {
		if strings.HasPrefix(modelLower, prefix) {
			// Check if it's exactly the prefix or followed by a separator
			if len(modelLower) == len(prefix) ||
				modelLower[len(prefix)] == '-' ||
				modelLower[len(prefix)] == '_' {
				return true
			}
		}
		// Also check for models like "openai-o1-mini" where prefix is not at start
		if strings.Contains(modelLower, "-"+prefix+"-") ||
			strings.Contains(modelLower, "_"+prefix+"_") ||
			strings.HasSuffix(modelLower, "-"+prefix) ||
			strings.HasSuffix(modelLower, "_"+prefix) {
			return true
		}
	}
	// Check for GPT-5 series models which support reasoning.effort
	if strings.HasPrefix(modelLower, "gpt-5") {
		return true
	}
	return false
}

// OpenAI enforces a 64 character maximum on the user field
const MaxUserFieldLength = 64

// SanitizeUserField returns nil if user exceeds MaxUserFieldLength, otherwise returns the original value
func SanitizeUserField(user *string) *string {
	if user != nil && len(*user) > MaxUserFieldLength {
		return nil
	}
	return user
}
