package cohere

import (
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToCohereChatCompletionRequest converts a Bifrost request to Cohere v2 format
func ToCohereChatCompletionRequest(bifrostReq *schemas.BifrostChatRequest) *CohereChatRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	messages := bifrostReq.Input
	cohereReq := &CohereChatRequest{
		Model: bifrostReq.Model,
	}

	// Convert messages to Cohere v2 format
	var cohereMessages []CohereMessage
	for _, msg := range messages {
		cohereMsg := CohereMessage{
			Role: string(msg.Role),
		}

		// Convert content
		if msg.Content != nil && msg.Content.ContentStr != nil {
			cohereMsg.Content = NewStringContent(*msg.Content.ContentStr)
		} else if msg.Content != nil && msg.Content.ContentBlocks != nil {
			var contentBlocks []CohereContentBlock
			for _, block := range msg.Content.ContentBlocks {
				if block.Text != nil {
					contentBlocks = append(contentBlocks, CohereContentBlock{
						Type: CohereContentBlockTypeText,
						Text: block.Text,
					})
				} else if block.ImageURLStruct != nil {
					contentBlocks = append(contentBlocks, CohereContentBlock{
						Type: CohereContentBlockTypeImage,
						ImageURL: &CohereImageURL{
							URL: block.ImageURLStruct.URL,
						},
					})
				}
			}
			if len(contentBlocks) > 0 {
				cohereMsg.Content = NewBlocksContent(contentBlocks)
			}
		}

		// Convert tool calls for assistant messages
		if msg.ChatAssistantMessage != nil && msg.ChatAssistantMessage.ToolCalls != nil {
			var toolCalls []CohereToolCall
			for _, toolCall := range msg.ChatAssistantMessage.ToolCalls {
				// Safely extract function name and arguments
				var functionName *string
				var functionArguments string

				if toolCall.Function.Name != nil {
					functionName = toolCall.Function.Name
				} else {
					// Use empty string if Name is nil
					functionName = schemas.Ptr("")
				}

				// Arguments is a string, not a pointer, so it's safe to access directly
				functionArguments = toolCall.Function.Arguments

				cohereToolCall := CohereToolCall{
					ID:   toolCall.ID,
					Type: "function",
					Function: &CohereFunction{
						Name:      functionName,
						Arguments: functionArguments,
					},
				}
				toolCalls = append(toolCalls, cohereToolCall)
			}
			cohereMsg.ToolCalls = toolCalls
		}

		// Convert tool messages
		if msg.ChatToolMessage != nil && msg.ChatToolMessage.ToolCallID != nil {
			cohereMsg.ToolCallID = msg.ChatToolMessage.ToolCallID
		}

		cohereMessages = append(cohereMessages, cohereMsg)
	}

	cohereReq.Messages = cohereMessages

	// Convert parameters
	if bifrostReq.Params != nil {
		cohereReq.MaxTokens = bifrostReq.Params.MaxCompletionTokens
		cohereReq.Temperature = bifrostReq.Params.Temperature
		cohereReq.P = bifrostReq.Params.TopP
		cohereReq.StopSequences = bifrostReq.Params.Stop
		cohereReq.FrequencyPenalty = bifrostReq.Params.FrequencyPenalty
		cohereReq.PresencePenalty = bifrostReq.Params.PresencePenalty

		// Convert extra params
		if bifrostReq.Params.ExtraParams != nil {
			// Handle thinking parameter
			if thinkingParam, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "thinking"); ok {
				if thinkingMap, ok := thinkingParam.(map[string]interface{}); ok {
					thinking := &CohereThinking{}
					if typeStr, ok := schemas.SafeExtractString(thinkingMap["type"]); ok {
						thinking.Type = CohereThinkingType(typeStr)
					}
					if tokenBudget, ok := schemas.SafeExtractIntPointer(thinkingMap["token_budget"]); ok {
						thinking.TokenBudget = tokenBudget
					}
					cohereReq.Thinking = thinking
				}
			}

			// Handle other Cohere-specific extra params
			if safetyMode, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["safety_mode"]); ok {
				cohereReq.SafetyMode = safetyMode
			}

			if logProbs, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["log_probs"]); ok {
				cohereReq.LogProbs = logProbs
			}

			if strictToolChoice, ok := schemas.SafeExtractBoolPointer(bifrostReq.Params.ExtraParams["strict_tool_choice"]); ok {
				cohereReq.StrictToolChoice = strictToolChoice
			}
		}

		// Convert tools to Cohere-specific format (without "strict" field)
		if bifrostReq.Params.Tools != nil {
			cohereTools := make([]CohereChatRequestTool, len(bifrostReq.Params.Tools))
			for i, tool := range bifrostReq.Params.Tools {
				cohereTools[i] = CohereChatRequestTool{
					Type: string(tool.Type),
				}
				if tool.Function != nil {
					cohereTools[i].Function = CohereChatRequestFunction{
						Name:        tool.Function.Name,
						Description: tool.Function.Description,
						Parameters:  tool.Function.Parameters, // Convert to map
						// Note: No "strict" field - Cohere doesn't support it
					}
				}
			}
			cohereReq.Tools = cohereTools
		}

		// Convert tool choice
		if bifrostReq.Params.ToolChoice != nil {
			toolChoice := bifrostReq.Params.ToolChoice

			if toolChoice.ChatToolChoiceStr != nil {
				switch schemas.ChatToolChoiceType(*toolChoice.ChatToolChoiceStr) {
				case schemas.ChatToolChoiceTypeNone:
					toolChoice := ToolChoiceNone
					cohereReq.ToolChoice = &toolChoice
				default:
					toolChoice := ToolChoiceRequired
					cohereReq.ToolChoice = &toolChoice
				}
			} else if toolChoice.ChatToolChoiceStruct != nil {
				switch toolChoice.ChatToolChoiceStruct.Type {
				case schemas.ChatToolChoiceTypeFunction:
					toolChoice := ToolChoiceRequired
					cohereReq.ToolChoice = &toolChoice
				default:
					toolChoice := ToolChoiceAuto
					cohereReq.ToolChoice = &toolChoice
				}
			}
		}
	}

	return cohereReq
}

// ToBifrostChatResponse converts a Cohere v2 response to Bifrost format
func (response *CohereChatResponse) ToBifrostChatResponse(model string) *schemas.BifrostChatResponse {
	if response == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostChatResponse{
		ID:     response.ID,
		Model:  model,
		Object: "chat.completion",
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role: schemas.ChatMessageRoleAssistant,
					},
				},
			},
		},
		Created: int(time.Now().Unix()),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.ChatCompletionRequest,
			Provider:    schemas.Cohere,
		},
	}

	var content *string
	var contentBlocks []schemas.ChatContentBlock
	var toolCalls []schemas.ChatAssistantMessageToolCall

	// Convert message content
	if response.Message != nil {
		if response.Message.Content != nil {
			if response.Message.Content.IsString() ||
				(response.Message.Content.IsBlocks() &&
					len(response.Message.Content.GetBlocks()) == 1 &&
					response.Message.Content.GetBlocks()[0].Type == CohereContentBlockTypeText) {
				if response.Message.Content.IsString() {
					content = response.Message.Content.GetString()
				} else {
					content = response.Message.Content.GetBlocks()[0].Text
				}
			} else if response.Message.Content.IsBlocks() {
				for _, block := range response.Message.Content.GetBlocks() {
					if block.Type == CohereContentBlockTypeText && block.Text != nil {
						contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
							Type: schemas.ChatContentBlockTypeText,
							Text: block.Text,
						})
					} else if block.Type == CohereContentBlockTypeImage && block.ImageURL != nil {
						contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
							Type: schemas.ChatContentBlockTypeImage,
							ImageURLStruct: &schemas.ChatInputImage{
								URL: block.ImageURL.URL,
							},
						})
					}
				}
			}
		}

		// Create the message content
		messageContent := &schemas.ChatMessageContent{
			ContentStr:    content,
			ContentBlocks: contentBlocks,
		}

		// Convert tool calls
		if response.Message.ToolCalls != nil {
			for _, toolCall := range response.Message.ToolCalls {
				// Check if Function is nil to avoid nil pointer dereference
				if toolCall.Function == nil {
					// Skip this tool call if Function is nil
					continue
				}

				// Safely extract function name and arguments
				var functionName *string
				var functionArguments string

				if toolCall.Function.Name != nil {
					functionName = toolCall.Function.Name
				} else {
					// Use empty string if Name is nil
					functionName = schemas.Ptr("")
				}

				// Arguments is a string, not a pointer, so it's safe to access directly
				functionArguments = toolCall.Function.Arguments

				bifrostToolCall := schemas.ChatAssistantMessageToolCall{
					ID: toolCall.ID,
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      functionName,
						Arguments: functionArguments,
					},
				}
				toolCalls = append(toolCalls, bifrostToolCall)
			}
		}

		// Create assistant message if we have tool calls
		var assistantMessage *schemas.ChatAssistantMessage
		if len(toolCalls) > 0 {
			assistantMessage = &schemas.ChatAssistantMessage{
				ToolCalls: toolCalls,
			}
		}

		bifrostResponse.Choices[0].ChatNonStreamResponseChoice.Message = &schemas.ChatMessage{
			Role:                 schemas.ChatMessageRoleAssistant,
			Content:              messageContent,
			ChatAssistantMessage: assistantMessage,
		}
	}

	// Convert finish reason
	if response.FinishReason != nil {
		finishReason := ConvertCohereFinishReasonToBifrost(*response.FinishReason)
		bifrostResponse.Choices[0].FinishReason = schemas.Ptr(finishReason)
	}

	// Convert usage information
	if response.Usage != nil {
		usage := &schemas.BifrostLLMUsage{}

		if response.Usage.Tokens != nil {
			if response.Usage.Tokens.InputTokens != nil {
				usage.PromptTokens = int(*response.Usage.Tokens.InputTokens)
			}
			if response.Usage.Tokens.OutputTokens != nil {
				usage.CompletionTokens = int(*response.Usage.Tokens.OutputTokens)
			}
			if response.Usage.CachedTokens != nil {
				usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
					CachedTokens: int(*response.Usage.CachedTokens),
				}
			}
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}

		bifrostResponse.Usage = usage
	}

	return bifrostResponse
}
