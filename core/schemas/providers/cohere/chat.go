package cohere

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ToCohereChatCompletionRequest converts a Bifrost request to Cohere v2 format
func ToCohereChatCompletionRequest(bifrostReq *schemas.BifrostChatRequest) *CohereChatRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	messages := bifrostReq.Input
	cohereReq := AcquireChatRequest()
	cohereReq.Model = bifrostReq.Model

	// Convert messages to Cohere v2 format
	cohereMessages := acquireCohereMessages()
	for _, msg := range messages {
		cohereMsg := CohereMessage{
			Role: string(msg.Role),
		}

		// Convert content
		if msg.Content.ContentStr != nil {
			cohereMsg.Content = acquireCohereMessageContent()
			cohereMsg.Content.StringContent = msg.Content.ContentStr
		} else if msg.Content.ContentBlocks != nil {
			contentBlocks := acquireCohereContentBlocks()
			for _, block := range msg.Content.ContentBlocks {
				if block.Text != nil {
					contentBlocks = append(contentBlocks, CohereContentBlock{
						Type: CohereContentBlockTypeText,
						Text: block.Text,
					})
				} else if block.ImageURLStruct != nil {
					imageURL := acquireCohereImageURL()
					imageURL.URL = block.ImageURLStruct.URL
					contentBlocks = append(contentBlocks, CohereContentBlock{
						Type:     CohereContentBlockTypeImage,
						ImageURL: imageURL,
					})
				}
			}
			if len(contentBlocks) > 0 {
				cohereMsg.Content = acquireCohereMessageContent()
				cohereMsg.Content.BlocksContent = contentBlocks
			} else {
				releaseCohereContentBlocks(contentBlocks)
			}
		}

		// Convert tool calls for assistant messages
		if msg.ChatAssistantMessage != nil && msg.ChatAssistantMessage.ToolCalls != nil {
			toolCalls := acquireCohereToolCalls()
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

				function := acquireCohereFunction()
				function.Name = functionName
				function.Arguments = functionArguments

				cohereToolCall := CohereToolCall{
					ID:       toolCall.ID,
					Type:     "function",
					Function: function,
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
			cohereTools := acquireCohereTools()
			for _, tool := range bifrostReq.Params.Tools {
				cohereTool := CohereChatRequestTool{
					Type: string(tool.Type),
				}
				if tool.Function != nil {
					cohereTool.Function = CohereChatRequestFunction{
						Name:        tool.Function.Name,
						Description: tool.Function.Description,
						Parameters:  tool.Function.Parameters, // Convert to map
						// Note: No "strict" field - Cohere doesn't support it
					}
				}
				cohereTools = append(cohereTools, cohereTool)
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
					Message: &schemas.ChatMessage{
						Role: schemas.ChatMessageRoleAssistant,
					},
				},
			},
		},
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.ChatCompletionRequest,
			Provider:    schemas.Cohere,
		},
	}

	// Convert message content
	if cohereResp.Message != nil {
		if cohereResp.Message.Content != nil {
			if cohereResp.Message.Content.StringContent != nil {
				content := cohereResp.Message.Content.StringContent
				bifrostResponse.Choices[0].BifrostNonStreamResponseChoice.Message.Content = &schemas.ChatMessageContent{
					ContentStr: content,
				}
			} else if cohereResp.Message.Content.BlocksContent != nil {
				blocks := cohereResp.Message.Content.BlocksContent
				if blocks != nil {
					var contentBlocks []schemas.ChatContentBlock
					for _, block := range blocks {
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
						bifrostResponse.Choices[0].BifrostNonStreamResponseChoice.Message.Content = &schemas.ChatMessageContent{
							ContentBlocks: contentBlocks,
						}
					}
				}
			}
		}

		// Convert tool calls
		if cohereResp.Message.ToolCalls != nil {
			var toolCalls []schemas.ChatAssistantMessageToolCall
			for _, toolCall := range cohereResp.Message.ToolCalls {
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
			bifrostResponse.Choices[0].BifrostNonStreamResponseChoice.Message.ChatAssistantMessage = &schemas.ChatAssistantMessage{
				ToolCalls: toolCalls,
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
