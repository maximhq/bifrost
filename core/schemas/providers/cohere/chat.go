package cohere

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ConvertChatRequestToCohere converts a Bifrost request to Cohere v2 format
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
		if msg.Content.ContentStr != nil {
			cohereMsg.Content = NewStringContent(*msg.Content.ContentStr)
		} else if msg.Content.ContentBlocks != nil {
			var contentBlocks []CohereContentBlock
			for _, block := range *msg.Content.ContentBlocks {
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
			for _, toolCall := range *msg.ChatAssistantMessage.ToolCalls {
				cohereToolCall := CohereToolCall{
					ID:   toolCall.ID,
					Type: "function",
					Function: &CohereFunction{
						Name:      toolCall.Function.Name,
						Arguments: toolCall.Function.Arguments,
					},
				}
				toolCalls = append(toolCalls, cohereToolCall)
			}
			cohereMsg.ToolCalls = &toolCalls
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
			if thinkingParam, ok := bifrostReq.Params.ExtraParams["thinking"]; ok {
				if thinkingMap, ok := thinkingParam.(map[string]interface{}); ok {
					thinking := &CohereThinking{}

					if typeStr, ok := thinkingMap["type"].(string); ok {
						thinking.Type = CohereThinkingType(typeStr)
					}

					if tokenBudget, ok := thinkingMap["token_budget"].(int); ok {
						thinking.TokenBudget = &tokenBudget
					} else if tokenBudgetFloat, ok := thinkingMap["token_budget"].(float64); ok {
						tokenBudgetInt := int(tokenBudgetFloat)
						thinking.TokenBudget = &tokenBudgetInt
					}

					cohereReq.Thinking = thinking
				}
			}

			// Handle other Cohere-specific extra params
			if safetyMode, ok := bifrostReq.Params.ExtraParams["safety_mode"].(string); ok {
				cohereReq.SafetyMode = &safetyMode
			}

			if logProbs, ok := bifrostReq.Params.ExtraParams["log_probs"].(bool); ok {
				cohereReq.LogProbs = &logProbs
			}

			if strictToolChoice, ok := bifrostReq.Params.ExtraParams["strict_tool_choice"].(bool); ok {
				cohereReq.StrictToolChoice = &strictToolChoice
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
			cohereReq.Tools = &cohereTools
		}

		// Convert tool choice
		if bifrostReq.Params.ToolChoice != nil {
			toolChoice := bifrostReq.Params.ToolChoice

			if toolChoice.ChatToolChoiceStr != nil {
				toolChoice := CohereToolChoice(*toolChoice.ChatToolChoiceStr)
				cohereReq.ToolChoice = &toolChoice
			} else if bifrostReq.Params.ToolChoice.ChatToolChoiceStruct != nil {
				switch bifrostReq.Params.ToolChoice.ChatToolChoiceStruct.Type {
				case schemas.ChatToolChoiceTypeFunction:
					toolChoice := ToolChoiceRequired
					cohereReq.ToolChoice = &toolChoice
				default:
					cohereReq.ToolChoice = nil
				}
			}
		}
	}

	return cohereReq
}

// ToBifrostResponse converts a Cohere v2 response to Bifrost format
func (cohereResp *CohereChatResponse) ToBifrostResponse() *schemas.BifrostResponse {
	if cohereResp == nil {
		return nil
	}

	bifrostResponse := &schemas.BifrostResponse{
		ID:     cohereResp.ID,
		Object: "chat.completion",
		Choices: []schemas.BifrostChatResponseChoice{
			{
				Index: 0,
				BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
					Message: schemas.ChatMessage{
						Role: schemas.ChatMessageRoleAssistant,
					},
				},
			},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.Cohere,
		},
	}

	// Convert message content
	if cohereResp.Message != nil {
		if cohereResp.Message.Content != nil {
			if cohereResp.Message.Content.IsString() {
				content := cohereResp.Message.Content.GetString()
				bifrostResponse.Choices[0].BifrostNonStreamResponseChoice.Message.Content = schemas.ChatMessageContent{
					ContentStr: content,
				}
			} else if cohereResp.Message.Content.IsBlocks() {
				blocks := cohereResp.Message.Content.GetBlocks()
				if blocks != nil {
					var contentBlocks []schemas.ChatContentBlock
					for _, block := range *blocks {
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
					if len(contentBlocks) > 0 {
						bifrostResponse.Choices[0].BifrostNonStreamResponseChoice.Message.Content = schemas.ChatMessageContent{
							ContentBlocks: &contentBlocks,
						}
					}
				}
			}
		}

		// Convert tool calls
		if cohereResp.Message.ToolCalls != nil {
			var toolCalls []schemas.ChatAssistantMessageToolCall
			for _, toolCall := range *cohereResp.Message.ToolCalls {
				bifrostToolCall := schemas.ChatAssistantMessageToolCall{
					ID: toolCall.ID,
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      toolCall.Function.Name,
						Arguments: toolCall.Function.Arguments,
					},
				}
				toolCalls = append(toolCalls, bifrostToolCall)
			}
			bifrostResponse.Choices[0].BifrostNonStreamResponseChoice.Message.ChatAssistantMessage = &schemas.ChatAssistantMessage{
				ToolCalls: &toolCalls,
			}
		}
	}

	// Convert finish reason
	if cohereResp.FinishReason != nil {
		finishReason := string(*cohereResp.FinishReason)
		bifrostResponse.Choices[0].FinishReason = &finishReason
	}

	// Convert usage information
	if cohereResp.Usage != nil {
		usage := &schemas.LLMUsage{}

		if cohereResp.Usage.Tokens != nil {
			if cohereResp.Usage.Tokens.InputTokens != nil {
				usage.PromptTokens = int(*cohereResp.Usage.Tokens.InputTokens)
			}
			if cohereResp.Usage.Tokens.OutputTokens != nil {
				usage.CompletionTokens = int(*cohereResp.Usage.Tokens.OutputTokens)
			}
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}

		bifrostResponse.Usage = usage

		// Convert billed usage
		if cohereResp.Usage.BilledUnits != nil {
			bifrostResponse.ExtraFields.BilledUsage = &schemas.BilledLLMUsage{}
			if cohereResp.Usage.BilledUnits.InputTokens != nil {
				bifrostResponse.ExtraFields.BilledUsage.PromptTokens = cohereResp.Usage.BilledUnits.InputTokens
			}
			if cohereResp.Usage.BilledUnits.OutputTokens != nil {
				bifrostResponse.ExtraFields.BilledUsage.CompletionTokens = cohereResp.Usage.BilledUnits.OutputTokens
			}
		}
	}

	return bifrostResponse
}
