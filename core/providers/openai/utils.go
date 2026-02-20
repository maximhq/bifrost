package openai

import "github.com/maximhq/bifrost/core/schemas"

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

// OpenAI enforces a 64 character maximum on the user field
const MaxUserFieldLength = 64

// SanitizeUserField returns nil if user exceeds MaxUserFieldLength, otherwise returns the original value
func SanitizeUserField(user *string) *string {
	if user != nil && len(*user) > MaxUserFieldLength {
		return nil
	}
	return user
}
