// Package huggingface provides a HuggingFace chat provider.
package huggingface

import (
	"fmt"

	"github.com/bytedance/sonic"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// sanitizeMessagesForHuggingFace removes unsupported ChatAssistantMessage fields
// from chat messages. HuggingFace's OpenAI-compatible API doesn't support fields
// like reasoning_details, reasoning, annotations, audio, and refusal.
// Only ToolCalls is preserved from ChatAssistantMessage.
func sanitizeMessagesForHuggingFace(messages []schemas.ChatMessage) []schemas.ChatMessage {
	sanitized := make([]schemas.ChatMessage, len(messages))
	for i, msg := range messages {
		sanitized[i] = schemas.ChatMessage{
			Name:            msg.Name,
			Role:            msg.Role,
			Content:         msg.Content,
			ChatToolMessage: msg.ChatToolMessage,
		}
		// Only preserve ToolCalls from ChatAssistantMessage
		if msg.ChatAssistantMessage != nil && len(msg.ChatAssistantMessage.ToolCalls) > 0 {
			sanitized[i].ChatAssistantMessage = &schemas.ChatAssistantMessage{
				ToolCalls: msg.ChatAssistantMessage.ToolCalls,
			}
		}
	}
	return sanitized
}

func ToHuggingFaceChatCompletionRequest(bifrostReq *schemas.BifrostChatRequest) (*HuggingFaceChatRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil, nil
	}

	// Create the HuggingFace request
	// Sanitize messages to remove unsupported fields like reasoning_details
	hfReq := &HuggingFaceChatRequest{
		Messages: sanitizeMessagesForHuggingFace(bifrostReq.Input),
		Model:    bifrostReq.Model,
	}

	// Map parameters if present
	if bifrostReq.Params != nil {
		params := bifrostReq.Params

		if params.FrequencyPenalty != nil {
			hfReq.FrequencyPenalty = params.FrequencyPenalty
		}
		if params.LogProbs != nil {
			hfReq.Logprobs = params.LogProbs
		}
		if params.MaxCompletionTokens != nil {
			hfReq.MaxTokens = params.MaxCompletionTokens
		}
		if params.PresencePenalty != nil {
			hfReq.PresencePenalty = params.PresencePenalty
		}
		if params.Seed != nil {
			hfReq.Seed = params.Seed
		}
		if len(params.Stop) > 0 {
			hfReq.Stop = params.Stop
		}
		if params.Temperature != nil {
			hfReq.Temperature = params.Temperature
		}
		if params.TopLogProbs != nil {
			hfReq.TopLogprobs = params.TopLogProbs
		}
		if params.TopP != nil {
			hfReq.TopP = params.TopP
		}

		// Handle response format (direct type assertion to avoid marshal→unmarshal round-trip)
		if params.ResponseFormat != nil {
			var hfRF *HuggingFaceResponseFormat
			if rfMap, ok := (*params.ResponseFormat).(map[string]interface{}); ok {
				hfRF = &HuggingFaceResponseFormat{}
				if t, ok := rfMap["type"].(string); ok {
					hfRF.Type = t
				}
				if jsVal, ok := rfMap["json_schema"]; ok {
					jsBytes, err := providerUtils.MarshalSorted(jsVal)
					if err != nil {
						return nil, fmt.Errorf("failed to marshal json_schema: %w", err)
					}
					var hfSchema HuggingFaceJSONSchema
					if err := sonic.Unmarshal(jsBytes, &hfSchema); err != nil {
						return nil, fmt.Errorf("failed to unmarshal json_schema: %w", err)
					}
					hfRF.JSONSchema = &hfSchema
				}
			} else if converted, err := schemas.ConvertViaJSON[HuggingFaceResponseFormat](*params.ResponseFormat); err == nil {
				hfRF = &converted
			}
			hfReq.ResponseFormat = hfRF
		}

		// Handle stream options
		if params.StreamOptions != nil {
			hfReq.StreamOptions = &schemas.ChatStreamOptions{
				IncludeUsage: params.StreamOptions.IncludeUsage,
			}
		}

		hfReq.Tools = params.Tools

		// Handle tool choice
		if params.ToolChoice != nil {
			hfToolChoice := &HuggingFaceToolChoice{}
			if params.ToolChoice.ChatToolChoiceStr != nil {
				switch *params.ToolChoice.ChatToolChoiceStr {
				case "auto":
					auto := EnumStringTypeAuto
					hfToolChoice.EnumValue = &auto
				case "none":
					none := EnumStringTypeNone
					hfToolChoice.EnumValue = &none
				case "required":
					required := EnumStringTypeRequired
					hfToolChoice.EnumValue = &required
				}
			} else if params.ToolChoice.ChatToolChoiceStruct != nil {
				if params.ToolChoice.ChatToolChoiceStruct.Type == schemas.ChatToolChoiceTypeFunction && params.ToolChoice.ChatToolChoiceStruct.Function != nil {
					hfToolChoice.Function = &schemas.ChatToolChoiceFunction{
						Name: params.ToolChoice.ChatToolChoiceStruct.Function.Name,
					}
				}
			}
			if hfToolChoice.EnumValue != nil || hfToolChoice.Function != nil {
				hfReq.ToolChoice = hfToolChoice
			}
		}
		hfReq.ExtraParams = bifrostReq.Params.ExtraParams
	}

	return hfReq, nil
}
