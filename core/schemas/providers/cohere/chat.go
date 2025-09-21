package cohere

import "github.com/maximhq/bifrost/core/schemas"

// ConvertChatRequestToCohere converts a Bifrost request to Cohere v2 format
func ToCohereChatCompletionRequest(bifrostReq *schemas.BifrostRequest) *CohereChatRequest {
	if bifrostReq == nil || bifrostReq.Input.ChatCompletionInput == nil {
		return nil
	}

	messages := *bifrostReq.Input.ChatCompletionInput
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
						Type: "text",
						Text: block.Text,
					})
				} else if block.ImageURL != nil {
					contentBlocks = append(contentBlocks, CohereContentBlock{
						Type: "image_url",
						ImageURL: &CohereImageURL{
							URL: block.ImageURL.URL,
						},
					})
				}
			}
			if len(contentBlocks) > 0 {
				cohereMsg.Content = NewBlocksContent(contentBlocks)
			}
		}

		// Convert tool calls for assistant messages
		if msg.AssistantMessage != nil && msg.AssistantMessage.ToolCalls != nil {
			var toolCalls []CohereToolCall
			for _, toolCall := range *msg.AssistantMessage.ToolCalls {
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
		if msg.ToolMessage != nil && msg.ToolMessage.ToolCallID != nil {
			cohereMsg.ToolCallID = msg.ToolMessage.ToolCallID
		}

		cohereMessages = append(cohereMessages, cohereMsg)
	}

	cohereReq.Messages = cohereMessages

	// Convert parameters
	if bifrostReq.Params != nil {
		cohereReq.MaxTokens = bifrostReq.Params.MaxTokens
		cohereReq.Temperature = bifrostReq.Params.Temperature
		cohereReq.P = bifrostReq.Params.TopP
		cohereReq.K = bifrostReq.Params.TopK
		cohereReq.StopSequences = bifrostReq.Params.StopSequences
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

		// Convert tools - direct assignment since formats are identical
		if bifrostReq.Params.Tools != nil {
			cohereReq.Tools = bifrostReq.Params.Tools
		}

		// Convert tool choice
		if bifrostReq.Params.ToolChoice != nil {
			if bifrostReq.Params.ToolChoice.ToolChoiceStr != nil {
				toolChoice := CohereToolChoice(*bifrostReq.Params.ToolChoice.ToolChoiceStr)
				cohereReq.ToolChoice = &toolChoice
			} else if bifrostReq.Params.ToolChoice.ToolChoiceStruct != nil {
				switch bifrostReq.Params.ToolChoice.ToolChoiceStruct.Type {
				case schemas.ToolChoiceTypeFunction:
					toolChoice := CohereToolChoice("REQUIRED")
					cohereReq.ToolChoice = &toolChoice
				case schemas.ToolChoiceTypeNone:
					toolChoice := CohereToolChoice("NONE")
					cohereReq.ToolChoice = &toolChoice
				default:
					toolChoiceStr := string(bifrostReq.Params.ToolChoice.ToolChoiceStruct.Type)
					toolChoice := CohereToolChoice(toolChoiceStr)
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
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
					Message: schemas.BifrostMessage{
						Role: schemas.ModelChatMessageRoleAssistant,
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
				bifrostResponse.Choices[0].BifrostNonStreamResponseChoice.Message.Content = schemas.MessageContent{
					ContentStr: content,
				}
			} else if cohereResp.Message.Content.IsBlocks() {
				blocks := cohereResp.Message.Content.GetBlocks()
				if blocks != nil {
					var contentBlocks []schemas.ContentBlock
					for _, block := range *blocks {
						if block.Type == "text" && block.Text != nil {
							contentBlocks = append(contentBlocks, schemas.ContentBlock{
								Type: "text",
								Text: block.Text,
							})
						} else if block.Type == "image_url" && block.ImageURL != nil {
							contentBlocks = append(contentBlocks, schemas.ContentBlock{
								Type: "image_url",
								ImageURL: &schemas.ImageURLStruct{
									URL: block.ImageURL.URL,
								},
							})
						}
					}
					if len(contentBlocks) > 0 {
						bifrostResponse.Choices[0].BifrostNonStreamResponseChoice.Message.Content = schemas.MessageContent{
							ContentBlocks: &contentBlocks,
						}
					}
				}
			}
		}

		// Convert tool calls
		if cohereResp.Message.ToolCalls != nil {
			var toolCalls []schemas.ToolCall
			for _, toolCall := range *cohereResp.Message.ToolCalls {
				bifrostToolCall := schemas.ToolCall{
					ID: toolCall.ID,
					Function: schemas.FunctionCall{
						Name:      toolCall.Function.Name,
						Arguments: toolCall.Function.Arguments,
					},
				}
				toolCalls = append(toolCalls, bifrostToolCall)
			}
			bifrostResponse.Choices[0].BifrostNonStreamResponseChoice.Message.AssistantMessage = &schemas.AssistantMessage{
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
