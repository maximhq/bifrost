package openai

import (
	"strings"

	"github.com/maximhq/bifrost/core/providers/utils"
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
			// Callers replay assistant reasoning under any of three keys. Normalize them
			// onto Reasoning so downstream provider logic sees replayed reasoning
			// regardless of spelling — DeepSeek in particular gates thinking on it.
			reasoning := message.OpenAIChatAssistantMessage.Reasoning
			if reasoning == nil {
				reasoning = message.OpenAIChatAssistantMessage.ReasoningAlias
			}
			if reasoning == nil {
				for _, detail := range message.OpenAIChatAssistantMessage.ReasoningDetails {
					if detail.Text != nil {
						reasoning = detail.Text
						break
					}
				}
			}
			bifrostMessages[i].ChatAssistantMessage = &schemas.ChatAssistantMessage{
				Refusal:          message.OpenAIChatAssistantMessage.Refusal,
				Reasoning:        reasoning,
				ReasoningDetails: message.OpenAIChatAssistantMessage.ReasoningDetails,
				Annotations:      message.OpenAIChatAssistantMessage.Annotations,
				ToolCalls:        message.OpenAIChatAssistantMessage.ToolCalls,
			}
		}
	}
	return bifrostMessages
}

// ConvertBifrostMessagesToOpenAIMessages converts Bifrost chat messages to the
// OpenAI wire format, dropping neutral-format fields the OpenAI wire has no
// carrier for. Over-long tool call IDs are stripped of embedded provider
// reasoning signatures, and tool messages lose is_error so providers that reject
// unknown message parameters never see it.
// The caller's messages are never mutated: shared pointers are cloned before edit.
func ConvertBifrostMessagesToOpenAIMessages(messages []schemas.ChatMessage) []OpenAIMessage {
	openaiMessages := make([]OpenAIMessage, len(messages))
	for i, message := range messages {
		openaiMessages[i] = OpenAIMessage{
			Name:            message.Name,
			Role:            message.Role,
			Content:         message.Content,
			ChatToolMessage: message.ChatToolMessage,
		}
		// Strip provider reasoning signatures (e.g. Gemini thoughtSignatures embedded in
		// call_id as "<baseID>_ts_<sig>") from the tool result's tool_call_id, but only when it
		// exceeds OpenAI's limit — shorter IDs are left intact so distinct upstream IDs are
		// preserved. Clone first — ChatToolMessage is shared with the caller's input.
		if message.ChatToolMessage != nil && message.ChatToolMessage.ToolCallID != nil &&
			len(*message.ChatToolMessage.ToolCallID) > MaxToolCallIDLength {
			if stripped := utils.StripThoughtSignature(*message.ChatToolMessage.ToolCallID); stripped != *message.ChatToolMessage.ToolCallID {
				toolMsgCopy := *message.ChatToolMessage
				toolMsgCopy.ToolCallID = &stripped
				openaiMessages[i].ChatToolMessage = &toolMsgCopy
			}
		}
		// The OpenAI wire format has no tool-error field; strip is_error so
		// providers that reject unknown message parameters never see it. Clone
		// first — ChatToolMessage is shared with the caller's input.
		if openaiMessages[i].ChatToolMessage != nil && openaiMessages[i].ChatToolMessage.IsError != nil {
			toolMsgCopy := *openaiMessages[i].ChatToolMessage
			toolMsgCopy.IsError = nil
			openaiMessages[i].ChatToolMessage = &toolMsgCopy
		}
		if message.ChatAssistantMessage != nil {
			// Strip the same embedded signature from over-long assistant tool call IDs. Clone the
			// slice only when a strip is actually needed so the caller's input is never mutated.
			toolCalls := message.ChatAssistantMessage.ToolCalls
			needsStrip := false
			for j := range toolCalls {
				if toolCalls[j].ID != nil && len(*toolCalls[j].ID) > MaxToolCallIDLength &&
					strings.Contains(*toolCalls[j].ID, utils.ThoughtSignatureSeparator) {
					needsStrip = true
					break
				}
			}
			if needsStrip {
				cloned := make([]schemas.ChatAssistantMessageToolCall, len(toolCalls))
				copy(cloned, toolCalls)
				for j := range cloned {
					if cloned[j].ID != nil && len(*cloned[j].ID) > MaxToolCallIDLength {
						stripped := utils.StripThoughtSignature(*cloned[j].ID)
						cloned[j].ID = &stripped
					}
				}
				toolCalls = cloned
			}
			openaiMessages[i].OpenAIChatAssistantMessage = &OpenAIChatAssistantMessage{
				Refusal:     message.ChatAssistantMessage.Refusal,
				Reasoning:   message.ChatAssistantMessage.Reasoning,
				Annotations: message.ChatAssistantMessage.Annotations,
				ToolCalls:   toolCalls,
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
	if strings.Contains(modelLower, "gpt-5") {
		return true
	}
	return false
}

func normalizeOpenAIReasoningEffort(model string, effort string) string {
	switch effort {
	case "minimal":
		if supportsOpenAIMinimalReasoningEffort(model) {
			return effort
		}
		return "low"
	case "max":
		if supportsMaxReasoningEffort(model) {
			return effort
		}
		if supportsOpenAIXHighReasoningEffort(model) {
			return "xhigh"
		}
		return "high"
	case "xhigh":
		if supportsOpenAIXHighReasoningEffort(model) {
			return "xhigh"
		}
		return "high"
	default:
		return effort
	}
}

func supportsOpenAIXHighReasoningEffort(model string) bool {
	_, parsedModel := schemas.ParseModelString(model, schemas.OpenAI)
	if parsedModel != "" {
		model = parsedModel
	}
	modelLower := strings.ToLower(model)
	return strings.HasPrefix(modelLower, "gpt-5.2") ||
		strings.HasPrefix(modelLower, "gpt-5.3-codex") ||
		strings.HasPrefix(modelLower, "gpt-5.4") ||
		strings.HasPrefix(modelLower, "gpt-5.5") ||
		strings.HasPrefix(modelLower, "gpt-5.6")
}

// supportsOpenAIMinimalReasoningEffort reports models that natively accept "minimal" effort.
// Per OpenAI's official docs (developers.openai.com/api/docs/guides/latest-model), the original
// GPT-5 family — "gpt-5", "gpt-5-mini", "gpt-5-nano" — supports "minimal, low, medium, high".
// Every later GPT-5 dot-revision (5.1, 5.2, 5.3-codex, 5.4, 5.5, 5.6-family, and their own
// mini/nano/pro/codex variants) dropped "minimal" from their reasoning.effort enum in favor of
// "none"/"xhigh"/"max". o1/o3/o4-series and gpt-oss also do not support it. Models without
// confirmed capability data conservatively fall back to "low".
func supportsOpenAIMinimalReasoningEffort(model string) bool {
	_, parsedModel := schemas.ParseModelString(model, schemas.OpenAI)
	if parsedModel != "" {
		model = parsedModel
	}
	modelLower := strings.ToLower(model)
	switch modelLower {
	case "gpt-5", "gpt-5-mini", "gpt-5-nano":
		return true
	default:
		return false
	}
}

// supportsMaxReasoningEffort reports models that natively accept "max" effort (e.g. GPT-5.6, DeepSeek V4, GLM-5.2).
func supportsMaxReasoningEffort(model string) bool {
	_, parsedModel := schemas.ParseModelString(model, schemas.OpenAI)
	if parsedModel != "" {
		model = parsedModel
	}
	modelLower := strings.ToLower(model)
	return strings.HasPrefix(modelLower, "gpt-5.6") ||
		strings.HasPrefix(modelLower, "deepseek-v4") ||
		strings.HasPrefix(modelLower, "glm-5.2")
}

// MaxUserFieldLength for OpenAI enforces a 64 character maximum on the user field
const MaxUserFieldLength = 64

// MaxToolCallIDLength is OpenAI's 64 character maximum on tool call IDs (call_id / input[].id).
const MaxToolCallIDLength = 64

// SanitizeUserField returns nil if user exceeds MaxUserFieldLength, otherwise returns the original value
func SanitizeUserField(user *string) *string {
	if user != nil && len(*user) > MaxUserFieldLength {
		return nil
	}
	return user
}
