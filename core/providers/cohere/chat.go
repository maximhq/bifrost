package cohere

import (
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToCohereChatCompletionRequest converts a Bifrost request to Cohere v2 format
func ToCohereChatCompletionRequest(bifrostReq *schemas.BifrostChatRequest) (*CohereChatRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil, fmt.Errorf("bifrost request is nil")
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

		// Convert reasoning
		if bifrostReq.Params.Reasoning != nil {
			if bifrostReq.Params.Reasoning.MaxTokens != nil {
				thinking := &CohereThinking{
					Type: ThinkingTypeEnabled,
				}
				if *bifrostReq.Params.Reasoning.MaxTokens == -1 {
					// cohere does not support dynamic reasoning budget like gemini
					// setting it to minimum reasoning budget
					thinking.TokenBudget = schemas.Ptr(anthropic.MinimumReasoningMaxTokens)
				} else {
					thinking.TokenBudget = bifrostReq.Params.Reasoning.MaxTokens
				}
				cohereReq.Thinking = thinking
			} else if bifrostReq.Params.Reasoning.Effort != nil {
				if *bifrostReq.Params.Reasoning.Effort != "none" {
					maxCompletionTokens := DefaultCompletionMaxTokens
					if bifrostReq.Params.MaxCompletionTokens != nil {
						maxCompletionTokens = *bifrostReq.Params.MaxCompletionTokens
					}
					budgetTokens, err := providerUtils.GetBudgetTokensFromReasoningEffort(*bifrostReq.Params.Reasoning.Effort, MinimumReasoningMaxTokens, maxCompletionTokens)
					if err != nil {
						return nil, err
					}
					cohereReq.Thinking = &CohereThinking{
						Type:        ThinkingTypeEnabled,
						TokenBudget: schemas.Ptr(budgetTokens), // Max tokens for reasoning
					}
				} else {
					cohereReq.Thinking = &CohereThinking{
						Type: ThinkingTypeDisabled,
					}
				}
			}
		}

		// Convert response format
		if bifrostReq.Params.ResponseFormat != nil {
			cohereReq.ResponseFormat = convertResponseFormatToCohere(bifrostReq.Params.ResponseFormat)
		}

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

	return cohereReq, nil
}

// ToBifrostChatRequest converts a Cohere v2 chat request to Bifrost format
func (req *CohereChatRequest) ToBifrostChatRequest(ctx *schemas.BifrostContext) *schemas.BifrostChatRequest {
	if req == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(req.Model, providerUtils.CheckAndSetDefaultProvider(ctx, schemas.Cohere))

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Params:   &schemas.ChatParameters{},
	}
	// Convert messages
	if req.Messages != nil {
		bifrostMessages := make([]schemas.ChatMessage, len(req.Messages))
		for i, message := range req.Messages {
			bifrostMessages[i] = *message.ToBifrostChatMessage()
		}
		bifrostReq.Input = bifrostMessages
	}
	// Convert parameters
	if req.MaxTokens != nil {
		bifrostReq.Params.MaxCompletionTokens = req.MaxTokens
	}
	if req.Temperature != nil {
		bifrostReq.Params.Temperature = req.Temperature
	}
	if req.P != nil {
		bifrostReq.Params.TopP = req.P
	}
	if req.StopSequences != nil {
		bifrostReq.Params.Stop = req.StopSequences
	}
	if req.FrequencyPenalty != nil {
		bifrostReq.Params.FrequencyPenalty = req.FrequencyPenalty
	}
	if req.PresencePenalty != nil {
		bifrostReq.Params.PresencePenalty = req.PresencePenalty
	}

	// Convert reasoning
	if req.Thinking != nil {
		if req.Thinking.Type == ThinkingTypeDisabled {
			bifrostReq.Params.Reasoning = &schemas.ChatReasoning{
				Effort: schemas.Ptr("none"),
			}
		} else {
			bifrostReq.Params.Reasoning = &schemas.ChatReasoning{
				Effort: schemas.Ptr("auto"),
			}
			if req.Thinking.TokenBudget != nil {
				bifrostReq.Params.Reasoning.MaxTokens = req.Thinking.TokenBudget
			}
		}
	}
	if req.ResponseFormat != nil {
		bifrostReq.Params.ResponseFormat = convertCohereResponseFormatToBifrost(req.ResponseFormat)
	}

	// Convert tools
	if req.Tools != nil {
		bifrostTools := make([]schemas.ChatTool, len(req.Tools))
		for i, tool := range req.Tools {
			bifrostTools[i] = schemas.ChatTool{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        tool.Function.Name,
					Description: tool.Function.Description,
					Parameters:  convertInterfaceToToolFunctionParameters(tool.Function.Parameters),
				},
			}
		}
		bifrostReq.Params.Tools = bifrostTools
	}

	// Convert tool choice
	if req.ToolChoice != nil {
		switch *req.ToolChoice {
		case ToolChoiceNone:
			bifrostReq.Params.ToolChoice = &schemas.ChatToolChoice{
				ChatToolChoiceStr: schemas.Ptr(string(schemas.ChatToolChoiceTypeNone)),
			}
		case ToolChoiceRequired:
			bifrostReq.Params.ToolChoice = &schemas.ChatToolChoice{
				ChatToolChoiceStr: schemas.Ptr(string(schemas.ChatToolChoiceTypeRequired)),
			}
		case ToolChoiceAuto:
			bifrostReq.Params.ToolChoice = &schemas.ChatToolChoice{
				ChatToolChoiceStr: schemas.Ptr(string(schemas.ChatToolChoiceTypeAny)),
			}
		}
	}

	// Convert extra params
	extraParams := make(map[string]interface{})
	if req.SafetyMode != nil {
		extraParams["safety_mode"] = *req.SafetyMode
	}
	if req.LogProbs != nil {
		extraParams["log_probs"] = *req.LogProbs
	}
	if req.StrictToolChoice != nil {
		extraParams["strict_tool_choice"] = *req.StrictToolChoice
	}
	if req.Thinking != nil {
		thinkingMap := map[string]interface{}{
			"type": string(req.Thinking.Type),
		}
		if req.Thinking.TokenBudget != nil {
			thinkingMap["token_budget"] = *req.Thinking.TokenBudget
		}
		extraParams["thinking"] = thinkingMap
	}
	if len(extraParams) > 0 {
		bifrostReq.Params.ExtraParams = extraParams
	}

	return bifrostReq
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
				Index:                       0,
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{},
			},
		},
		Created: int(time.Now().Unix()),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.ChatCompletionRequest,
			Provider:    schemas.Cohere,
		},
	}

	// Convert messages
	if response.Message != nil {
		bifrostMessage := response.Message.ToBifrostChatMessage()
		bifrostResponse.Choices[0].ChatNonStreamResponseChoice.Message = bifrostMessage
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
				usage.PromptTokens = *response.Usage.Tokens.InputTokens
			}
			if response.Usage.Tokens.OutputTokens != nil {
				usage.CompletionTokens = *response.Usage.Tokens.OutputTokens
			}
			if response.Usage.CachedTokens != nil {
				usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
					CachedTokens: *response.Usage.CachedTokens,
				}
			}
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}

		bifrostResponse.Usage = usage
	}

	return bifrostResponse
}

func (chunk *CohereStreamEvent) ToBifrostChatCompletionStream() (*schemas.BifrostChatResponse, *schemas.BifrostError, bool) {
	switch chunk.Type {
	case StreamEventMessageStart:
		if chunk.Delta != nil && chunk.Delta.Message != nil && chunk.Delta.Message.Role != nil {
			// Create streaming response for this delta
			streamResponse := &schemas.BifrostChatResponse{
				Object: "chat.completion.chunk",
				Choices: []schemas.BifrostResponseChoice{
					{
						Index: 0,
						ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
							Delta: &schemas.ChatStreamResponseChoiceDelta{
								Role: chunk.Delta.Message.Role,
							},
						},
					},
				},
			}

			return streamResponse, nil, false
		}

	case StreamEventContentDelta:
		if chunk.Delta != nil &&
			chunk.Delta.Message != nil &&
			chunk.Delta.Message.Content != nil &&
			chunk.Delta.Message.Content.CohereStreamContentObject != nil {
			if chunk.Delta.Message.Content.CohereStreamContentObject.Text != nil {
				// Try to cast content to CohereStreamContent
				streamResponse := &schemas.BifrostChatResponse{
					Object: "chat.completion.chunk",
					Choices: []schemas.BifrostResponseChoice{
						{
							Index: 0,
							ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
								Delta: &schemas.ChatStreamResponseChoiceDelta{
									Content: chunk.Delta.Message.Content.CohereStreamContentObject.Text,
								},
							},
						},
					},
				}

				return streamResponse, nil, false
			} else if chunk.Delta.Message.Content.CohereStreamContentObject.Thinking != nil {
				thinkingText := *chunk.Delta.Message.Content.CohereStreamContentObject.Thinking
				streamResponse := &schemas.BifrostChatResponse{
					Object: "chat.completion.chunk",
					Choices: []schemas.BifrostResponseChoice{
						{
							Index: 0,
							ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
								Delta: &schemas.ChatStreamResponseChoiceDelta{
									Reasoning: schemas.Ptr(thinkingText),
									ReasoningDetails: []schemas.ChatReasoningDetails{
										{
											Index: 0,
											Type:  schemas.BifrostReasoningDetailsTypeText,
											Text:  schemas.Ptr(thinkingText),
										},
									},
								},
							},
						},
					},
				}

				return streamResponse, nil, false
			}
		}

	case StreamEventToolPlanDelta:
		if chunk.Delta != nil && chunk.Delta.Message != nil && chunk.Delta.Message.ToolPlan != nil {
			streamResponse := &schemas.BifrostChatResponse{
				Object: "chat.completion.chunk",
				Choices: []schemas.BifrostResponseChoice{
					{
						Index: 0,
						ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
							Delta: &schemas.ChatStreamResponseChoiceDelta{
								Reasoning: chunk.Delta.Message.ToolPlan,
							},
						},
					},
				},
			}

			return streamResponse, nil, false
		}

	case StreamEventContentStart:
		// Content start event - just continue, actual content comes in content-delta
		return nil, nil, false

	case StreamEventToolCallStart, StreamEventToolCallDelta:
		if chunk.Delta != nil && chunk.Delta.Message != nil && chunk.Delta.Message.ToolCalls != nil && chunk.Delta.Message.ToolCalls.CohereToolCallObject != nil {
			// Handle single tool call object (tool-call-start/delta events)
			cohereToolCall := chunk.Delta.Message.ToolCalls.CohereToolCallObject
			toolCall := schemas.ChatAssistantMessageToolCall{}

			if cohereToolCall.ID != nil {
				toolCall.ID = cohereToolCall.ID
			}

			if cohereToolCall.Function != nil {
				if cohereToolCall.Function.Name != nil {
					toolCall.Function.Name = cohereToolCall.Function.Name
				}
				toolCall.Function.Arguments = cohereToolCall.Function.Arguments
			}

			streamResponse := &schemas.BifrostChatResponse{
				Object: "chat.completion.chunk",
				Choices: []schemas.BifrostResponseChoice{
					{
						Index: 0,
						ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
							Delta: &schemas.ChatStreamResponseChoiceDelta{
								ToolCalls: []schemas.ChatAssistantMessageToolCall{toolCall},
							},
						},
					},
				},
			}

			return streamResponse, nil, false
		}

	case StreamEventToolCallEnd:
		return nil, nil, false

	case StreamEventContentEnd:
		return nil, nil, false

	case StreamEventMessageEnd:
		if chunk.Delta != nil {
			var finishReason string
			usage := &schemas.BifrostLLMUsage{}
			// Set finish reason
			if chunk.Delta.FinishReason != nil {
				finishReason = ConvertCohereFinishReasonToBifrost(*chunk.Delta.FinishReason)
			}

			// Set usage information
			if chunk.Delta.Usage != nil {
				if chunk.Delta.Usage.Tokens != nil {
					if chunk.Delta.Usage.Tokens.InputTokens != nil {
						usage.PromptTokens = *chunk.Delta.Usage.Tokens.InputTokens
					}
					if chunk.Delta.Usage.Tokens.OutputTokens != nil {
						usage.CompletionTokens = *chunk.Delta.Usage.Tokens.OutputTokens
					}
					usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
				}
			}

			streamResponse := &schemas.BifrostChatResponse{
				Object: "chat.completion.chunk",
				Choices: []schemas.BifrostResponseChoice{
					{
						Index:        0,
						FinishReason: &finishReason,
						ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
							Delta: &schemas.ChatStreamResponseChoiceDelta{},
						},
					},
				},
				Usage: usage,
			}

			return streamResponse, nil, true
		}
		return nil, nil, false
	}

	return nil, nil, false
}

func (cm *CohereMessage) ToBifrostChatMessage() *schemas.ChatMessage {
	if cm == nil {
		return nil
	}

	var content *string
	var contentBlocks []schemas.ChatContentBlock
	var toolCalls []schemas.ChatAssistantMessageToolCall
	var reasoningDetails []schemas.ChatReasoningDetails
	var reasoningText string

	// Convert message content
	if cm.Content != nil {
		if cm.Content.IsString() ||
			(cm.Content.IsBlocks() &&
				len(cm.Content.GetBlocks()) == 1 &&
				cm.Content.GetBlocks()[0].Type == CohereContentBlockTypeText) {
			if cm.Content.IsString() {
				content = cm.Content.GetString()
			} else {
				content = cm.Content.GetBlocks()[0].Text
			}
		} else if cm.Content.IsBlocks() {
			for _, block := range cm.Content.GetBlocks() {
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
				} else if block.Type == CohereContentBlockTypeThinking && block.Thinking != nil {
					reasoningDetails = append(reasoningDetails, schemas.ChatReasoningDetails{
						Index: len(reasoningDetails),
						Type:  schemas.BifrostReasoningDetailsTypeText,
						Text:  block.Thinking,
					})
					if len(reasoningText) > 0 {
						reasoningText += "\n"
					}
					reasoningText += *block.Thinking
				}
			}
		}
	}

	if len(contentBlocks) == 1 && contentBlocks[0].Type == schemas.ChatContentBlockTypeText {
		content = contentBlocks[0].Text
		contentBlocks = nil
	}

	// Create the message content
	messageContent := &schemas.ChatMessageContent{
		ContentStr:    content,
		ContentBlocks: contentBlocks,
	}

	// Convert tool calls
	if cm.ToolCalls != nil {
		for _, toolCall := range cm.ToolCalls {
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
				Index: uint16(len(toolCalls)),
				ID:    toolCall.ID,
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

	if len(reasoningDetails) > 0 {
		if assistantMessage == nil {
			assistantMessage = &schemas.ChatAssistantMessage{}
		}
		assistantMessage.ReasoningDetails = reasoningDetails
		assistantMessage.Reasoning = schemas.Ptr(reasoningText)
	}

	bifrostMessage := &schemas.ChatMessage{
		Role:                 schemas.ChatMessageRole(cm.Role),
		Content:              messageContent,
		ChatAssistantMessage: assistantMessage,
	}

	if cm.Role == "tool" {
		bifrostMessage.ChatToolMessage = &schemas.ChatToolMessage{
			ToolCallID: cm.ToolCallID,
		}
	}
	return bifrostMessage
}
