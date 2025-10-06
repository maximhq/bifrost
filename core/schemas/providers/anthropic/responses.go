package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToResponsesBifrostRequest converts an Anthropic message request to Bifrost format
func (mr *AnthropicMessageRequest) ToResponsesBifrostRequest() *schemas.BifrostResponsesRequest {
	provider, model := schemas.ParseModelString(mr.Model, schemas.Anthropic)

	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    model,
	}

	// Convert basic parameters
	params := &schemas.ResponsesParameters{
		ExtraParams: make(map[string]interface{}),
	}

	if mr.MaxTokens > 0 {
		params.MaxOutputTokens = &mr.MaxTokens
	}
	if mr.Temperature != nil {
		params.Temperature = mr.Temperature
	}
	if mr.TopP != nil {
		params.TopP = mr.TopP
	}
	if mr.TopK != nil {
		params.ExtraParams["top_k"] = *mr.TopK
	}
	if mr.StopSequences != nil {
		params.ExtraParams["stop"] = mr.StopSequences
	}
	bifrostReq.Params = params

	// Convert messages directly to ChatMessage format
	var bifrostMessages []schemas.ResponsesMessage

	// Handle system message - convert Anthropic system field to first message with role "system"
	if mr.System != nil {
		var systemText string
		if mr.System.ContentStr != nil {
			systemText = *mr.System.ContentStr
		} else if mr.System.ContentBlocks != nil {
			// Combine text blocks from system content
			var textParts []string
			for _, block := range mr.System.ContentBlocks {
				if block.Text != nil {
					textParts = append(textParts, *block.Text)
				}
			}
			systemText = strings.Join(textParts, "\n")
		}

		if systemText != "" {
			systemMsg := schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &systemText,
				},
			}
			bifrostMessages = append(bifrostMessages, systemMsg)
		}
	}

	// Convert regular messages
	for _, msg := range mr.Messages {
		convertedMessages := convertAnthropicMessageToBifrostResponsesMessages(&msg)
		bifrostMessages = append(bifrostMessages, convertedMessages...)
	}

	// Convert tools if present
	if mr.Tools != nil {
		var bifrostTools []schemas.ResponsesTool
		for _, tool := range mr.Tools {
			bifrostTool := convertAnthropicToolToBifrost(&tool)
			if bifrostTool != nil {
				bifrostTools = append(bifrostTools, *bifrostTool)
			}
		}
		if len(bifrostTools) > 0 {
			bifrostReq.Params.Tools = bifrostTools
		}
	}

	// Convert tool choice if present
	if mr.ToolChoice != nil {
		bifrostToolChoice := convertAnthropicToolChoiceToBifrost(mr.ToolChoice)
		if bifrostToolChoice != nil {
			bifrostReq.Params.ToolChoice = bifrostToolChoice
		}
	}

	// Set the converted messages
	if len(bifrostMessages) > 0 {
		bifrostReq.Input = bifrostMessages
	}

	return bifrostReq
}

// ToAnthropicResponsesRequest converts a BifrostRequest with Responses structure back to AnthropicMessageRequest
func ToAnthropicResponsesRequest(bifrostReq *schemas.BifrostResponsesRequest) *AnthropicMessageRequest {
	anthropicReq := &AnthropicMessageRequest{
		Model:     bifrostReq.Model,
		MaxTokens: AnthropicDefaultMaxTokens,
	}

	// Convert basic parameters
	if bifrostReq.Params != nil {
		if bifrostReq.Params.MaxOutputTokens != nil {
			anthropicReq.MaxTokens = *bifrostReq.Params.MaxOutputTokens
		}
		if bifrostReq.Params.Temperature != nil {
			anthropicReq.Temperature = bifrostReq.Params.Temperature
		}
		if bifrostReq.Params.TopP != nil {
			anthropicReq.TopP = bifrostReq.Params.TopP
		}
		if bifrostReq.Params.ExtraParams != nil {
			topK, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["top_k"])
			if ok {
				anthropicReq.TopK = topK
			}
			if stop, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["stop"]); ok {
				anthropicReq.StopSequences = stop
			}
		}

		// Convert tools
		if bifrostReq.Params.Tools != nil {
			anthropicTools := []AnthropicTool{}
			for _, tool := range bifrostReq.Params.Tools {
				anthropicTool := convertBifrostToolToAnthropic(&tool)
				if anthropicTool != nil {
					anthropicTools = append(anthropicTools, *anthropicTool)
				}
			}
			if len(anthropicTools) > 0 {
				anthropicReq.Tools = anthropicTools
			}
		}

		// Convert tool choice
		if bifrostReq.Params.ToolChoice != nil {
			anthropicToolChoice := convertResponsesToolChoiceToAnthropic(bifrostReq.Params.ToolChoice)
			if anthropicToolChoice != nil {
				anthropicReq.ToolChoice = anthropicToolChoice
			}
		}
	}

	if bifrostReq.Input != nil {
		anthropicMessages, systemContent := convertResponsesMessagesToAnthropicMessages(bifrostReq.Input)

		// Set system message if present
		if systemContent != nil {
			anthropicReq.System = systemContent
		}

		// Set regular messages
		anthropicReq.Messages = anthropicMessages
	}

	return anthropicReq
}

// ToResponsesBifrostResponse converts an Anthropic response to BifrostResponse with Responses structure
func (response *AnthropicMessageResponse) ToResponsesBifrostResponse() *schemas.BifrostResponse {
	if response == nil {
		return nil
	}

	// Create the BifrostResponse with Responses structure
	bifrostResp := &schemas.BifrostResponse{
		ID:     response.ID,
		Model:  response.Model,
		Object: "response",
		ResponsesResponse: &schemas.ResponsesResponse{
			CreatedAt: int(time.Now().Unix()),
		},
	}

	// Convert usage information
	if response.Usage != nil {
		bifrostResp.Usage = &schemas.LLMUsage{
			TotalTokens: response.Usage.InputTokens + response.Usage.OutputTokens,
			ResponsesExtendedResponseUsage: &schemas.ResponsesExtendedResponseUsage{
				InputTokens:  response.Usage.InputTokens,
				OutputTokens: response.Usage.OutputTokens,
			},
		}

		// Handle cached tokens if present
		if response.Usage.CacheReadInputTokens > 0 {
			if bifrostResp.Usage.ResponsesExtendedResponseUsage.InputTokensDetails == nil {
				bifrostResp.Usage.ResponsesExtendedResponseUsage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{}
			}
			bifrostResp.Usage.ResponsesExtendedResponseUsage.InputTokensDetails.CachedTokens = response.Usage.CacheReadInputTokens
		}
	}

	// Convert content to Responses output messages
	outputMessages := convertAnthropicContentBlocksToResponsesMessages(response.Content)
	if len(outputMessages) > 0 {
		bifrostResp.ResponsesResponse.Output = outputMessages
	}

	return bifrostResp
}

// ToAnthropicResponsesResponse converts a BifrostResponse with Responses structure back to AnthropicMessageResponse
func ToAnthropicResponsesResponse(bifrostResp *schemas.BifrostResponse) *AnthropicMessageResponse {
	anthropicResp := &AnthropicMessageResponse{
		ID:    bifrostResp.ID,
		Model: bifrostResp.Model,
		Type:  "message",
		Role:  "assistant",
	}

	// Convert usage information
	if bifrostResp.Usage != nil {
		anthropicResp.Usage = &AnthropicUsage{
			InputTokens:  bifrostResp.Usage.PromptTokens,
			OutputTokens: bifrostResp.Usage.CompletionTokens,
		}

		responsesUsage := bifrostResp.Usage.ResponsesExtendedResponseUsage

		if responsesUsage != nil && responsesUsage.InputTokens > 0 {
			anthropicResp.Usage.InputTokens = responsesUsage.InputTokens
		}

		if responsesUsage != nil && responsesUsage.OutputTokens > 0 {
			anthropicResp.Usage.OutputTokens = responsesUsage.OutputTokens
		}

		// Handle cached tokens if present
		if responsesUsage != nil &&
			responsesUsage.InputTokensDetails != nil &&
			responsesUsage.InputTokensDetails.CachedTokens > 0 {
			anthropicResp.Usage.CacheReadInputTokens = responsesUsage.InputTokensDetails.CachedTokens
		}
	}

	// Convert output messages to Anthropic content blocks
	var contentBlocks []AnthropicContentBlock
	if bifrostResp.ResponsesResponse != nil && bifrostResp.ResponsesResponse.Output != nil {
		contentBlocks = convertBifrostMessagesToAnthropicContent(bifrostResp.ResponsesResponse.Output)
	}

	if len(contentBlocks) > 0 {
		anthropicResp.Content = contentBlocks
	}

	// Set default stop reason - could be enhanced based on additional context
	stopReason := "end_turn"
	anthropicResp.StopReason = &stopReason

	// Check if there are tool calls to set appropriate stop reason
	for _, block := range contentBlocks {
		if block.Type == AnthropicContentBlockTypeToolUse {
			toolStopReason := "tool_use"
			anthropicResp.StopReason = &toolStopReason
			break
		}
	}

	return anthropicResp
}

// convertAnthropicMessageToBifrostResponsesMessages converts AnthropicMessage to ChatMessage format
func convertAnthropicMessageToBifrostResponsesMessages(msg *AnthropicMessage) []schemas.ResponsesMessage {
	var bifrostMessages []schemas.ResponsesMessage

	// Handle text content
	if msg.Content.ContentStr != nil {
		bifrostMsg := schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: schemas.Ptr(schemas.ResponsesMessageRoleType(msg.Role)),
			Content: &schemas.ResponsesMessageContent{
				ContentStr: msg.Content.ContentStr,
			},
		}
		bifrostMessages = append(bifrostMessages, bifrostMsg)
	} else if msg.Content.ContentBlocks != nil {
		// Handle content blocks
		for _, block := range msg.Content.ContentBlocks {
			switch block.Type {
			case AnthropicContentBlockTypeText:
				if block.Text != nil {
					bifrostMsg := schemas.ResponsesMessage{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesMessageRoleType(msg.Role)),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: block.Text,
						},
					}
					bifrostMessages = append(bifrostMessages, bifrostMsg)
				}
			case AnthropicContentBlockTypeImage:
				if block.Source != nil {
					bifrostMsg := schemas.ResponsesMessage{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Role: schemas.Ptr(schemas.ResponsesMessageRoleType(msg.Role)),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{block.toBifrostResponsesImageBlock()},
						},
					}
					bifrostMessages = append(bifrostMessages, bifrostMsg)
				}
			case AnthropicContentBlockTypeToolUse:
				// Convert tool use to function call message
				if block.ID != nil && block.Name != nil {
					bifrostMsg := schemas.ResponsesMessage{
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
						Status: schemas.Ptr("completed"),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID:    block.ID,
							Name:      block.Name,
							Arguments: schemas.Ptr(schemas.JsonifyInput(block.Input)),
						},
					}
					bifrostMessages = append(bifrostMessages, bifrostMsg)
				}
			case AnthropicContentBlockTypeToolResult:
				// Convert tool result to function call output message
				if block.ToolUseID != nil {
					if block.Content != nil {
						bifrostMsg := schemas.ResponsesMessage{
							Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
							Status: schemas.Ptr("completed"),
							ResponsesToolMessage: &schemas.ResponsesToolMessage{
								CallID: block.ToolUseID,
							},
						}
						// Initialize the nested struct before any writes
						bifrostMsg.ResponsesToolMessage.ResponsesFunctionToolCallOutput = &schemas.ResponsesFunctionToolCallOutput{}

						if block.Content.ContentStr != nil {
							bifrostMsg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputStr = block.Content.ContentStr
						} else if block.Content.ContentBlocks != nil {
							var toolMsgContentBlocks []schemas.ResponsesMessageContentBlock
							for _, contentBlock := range block.Content.ContentBlocks {
								switch contentBlock.Type {
								case AnthropicContentBlockTypeText:
									if contentBlock.Text != nil {
										toolMsgContentBlocks = append(toolMsgContentBlocks, schemas.ResponsesMessageContentBlock{
											Type: schemas.ResponsesInputMessageContentBlockTypeText,
											Text: contentBlock.Text,
										})
									}
								case AnthropicContentBlockTypeImage:
									if contentBlock.Source != nil {
										toolMsgContentBlocks = append(toolMsgContentBlocks, contentBlock.toBifrostResponsesImageBlock())
									}
								}
							}
							bifrostMsg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputBlocks = toolMsgContentBlocks
						}
						bifrostMessages = append(bifrostMessages, bifrostMsg)
					}
				}
			}
		}
	}

	return bifrostMessages
}

// convertAnthropicToolToBifrost converts AnthropicTool to schemas.Tool
func convertAnthropicToolToBifrost(tool *AnthropicTool) *schemas.ResponsesTool {
	if tool == nil {
		return nil
	}

	bifrostTool := &schemas.ResponsesTool{
		Type:        "function",
		Name:        &tool.Name,
		Description: &tool.Description,
		ResponsesToolFunction: &schemas.ResponsesToolFunction{
			Parameters: tool.InputSchema,
		},
	}

	return bifrostTool
}

// convertAnthropicToolChoiceToBifrost converts AnthropicToolChoice to schemas.ToolChoice
func convertAnthropicToolChoiceToBifrost(toolChoice *AnthropicToolChoice) *schemas.ResponsesToolChoice {
	if toolChoice == nil {
		return nil
	}

	bifrostToolChoice := &schemas.ResponsesToolChoice{}

	// Handle string format
	if toolChoice.Type != "" {
		switch toolChoice.Type {
		case "auto":
			bifrostToolChoice.ResponsesToolChoiceStr = schemas.Ptr(string(schemas.ResponsesToolChoiceTypeAuto))
		case "any":
			bifrostToolChoice.ResponsesToolChoiceStr = schemas.Ptr(string(schemas.ResponsesToolChoiceTypeAny))
		case "none":
			bifrostToolChoice.ResponsesToolChoiceStr = schemas.Ptr(string(schemas.ResponsesToolChoiceTypeNone))
		case "tool":
			// Handle forced tool choice with specific function name
			bifrostToolChoice.ResponsesToolChoiceStruct = &schemas.ResponsesToolChoiceStruct{
				Type: schemas.ResponsesToolChoiceTypeFunction,
				Name: &toolChoice.Name,
			}
			return bifrostToolChoice
		default:
			bifrostToolChoice.ResponsesToolChoiceStr = schemas.Ptr(string(schemas.ResponsesToolChoiceTypeAuto))
		}
	}

	return bifrostToolChoice
}

// Helper function to convert ResponsesInputItems back to AnthropicMessages
func convertResponsesMessagesToAnthropicMessages(messages []schemas.ResponsesMessage) ([]AnthropicMessage, *AnthropicContent) {
	var anthropicMessages []AnthropicMessage
	var systemContent *AnthropicContent
	var pendingToolCalls []AnthropicContentBlock
	var currentAssistantMessage *AnthropicMessage

	for _, msg := range messages {
		// Handle nil Type as regular message
		msgType := schemas.ResponsesMessageTypeMessage
		if msg.Type != nil {
			msgType = *msg.Type
		}

		switch msgType {
		case schemas.ResponsesMessageTypeMessage:
			// Flush any pending tool calls first
			if len(pendingToolCalls) > 0 && currentAssistantMessage != nil {
				// Copy the slice to avoid aliasing issues
				copied := make([]AnthropicContentBlock, len(pendingToolCalls))
				copy(copied, pendingToolCalls)
				currentAssistantMessage.Content = AnthropicContent{
					ContentBlocks: copied,
				}
				anthropicMessages = append(anthropicMessages, *currentAssistantMessage)
				pendingToolCalls = nil
				currentAssistantMessage = nil
			}

			// Handle system messages separately
			if msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleSystem {
				if msg.Content != nil {
					if msg.Content.ContentStr != nil {
						systemContent = &AnthropicContent{
							ContentStr: msg.Content.ContentStr,
						}
					} else if msg.Content.ContentBlocks != nil {
						contentBlocks := []AnthropicContentBlock{}
						for _, block := range msg.Content.ContentBlocks {
							if anthropicBlock := convertContentBlockToAnthropic(block); anthropicBlock != nil {
								contentBlocks = append(contentBlocks, *anthropicBlock)
							}
						}
						if len(contentBlocks) > 0 {
							systemContent = &AnthropicContent{
								ContentBlocks: contentBlocks,
							}
						}
					}
				}
				continue
			}

			// Regular user/assistant message
			anthropicMsg := AnthropicMessage{}

			// Set role
			if msg.Role != nil {
				switch *msg.Role {
				case schemas.ResponsesInputMessageRoleUser:
					anthropicMsg.Role = AnthropicMessageRoleUser
				case schemas.ResponsesInputMessageRoleAssistant:
					anthropicMsg.Role = AnthropicMessageRoleAssistant
				default:
					anthropicMsg.Role = AnthropicMessageRoleUser // Default fallback
				}
			} else {
				anthropicMsg.Role = AnthropicMessageRoleUser // Default fallback
			}

			// Convert content
			if msg.Content != nil {
				if msg.Content.ContentStr != nil {
					anthropicMsg.Content = AnthropicContent{
						ContentStr: msg.Content.ContentStr,
					}
				} else if msg.Content.ContentBlocks != nil {
					contentBlocks := []AnthropicContentBlock{}
					for _, block := range msg.Content.ContentBlocks {
						if anthropicBlock := convertContentBlockToAnthropic(block); anthropicBlock != nil {
							contentBlocks = append(contentBlocks, *anthropicBlock)
						}
					}
					if len(contentBlocks) > 0 {
						anthropicMsg.Content = AnthropicContent{
							ContentBlocks: contentBlocks,
						}
					}
				}
			}

			anthropicMessages = append(anthropicMessages, anthropicMsg)

		case schemas.ResponsesMessageTypeReasoning:
			// Handle reasoning as thinking content
			if msg.ResponsesReasoning != nil && len(msg.ResponsesReasoning.Summary) > 0 {
				// Find the last assistant message or create one
				var targetMsg *AnthropicMessage
				if len(anthropicMessages) > 0 && anthropicMessages[len(anthropicMessages)-1].Role == AnthropicMessageRoleAssistant {
					targetMsg = &anthropicMessages[len(anthropicMessages)-1]
				} else {
					// Create new assistant message for reasoning
					newMsg := AnthropicMessage{
						Role: AnthropicMessageRoleAssistant,
					}
					anthropicMessages = append(anthropicMessages, newMsg)
					targetMsg = &anthropicMessages[len(anthropicMessages)-1]
				}

				// Add thinking blocks
				var contentBlocks []AnthropicContentBlock
				if targetMsg.Content.ContentBlocks != nil {
					contentBlocks = targetMsg.Content.ContentBlocks
				}

				for _, reasoningContent := range msg.ResponsesReasoning.Summary {
					thinkingBlock := AnthropicContentBlock{
						Type:     AnthropicContentBlockTypeThinking,
						Thinking: &reasoningContent.Text,
					}
					contentBlocks = append(contentBlocks, thinkingBlock)
				}

				targetMsg.Content = AnthropicContent{
					ContentBlocks: contentBlocks,
				}
			}

		case schemas.ResponsesMessageTypeFunctionCall:
			// Start accumulating tool calls for assistant message
			if currentAssistantMessage == nil {
				currentAssistantMessage = &AnthropicMessage{
					Role: AnthropicMessageRoleAssistant,
				}
			}

			if msg.ResponsesToolMessage != nil {
				toolUseBlock := AnthropicContentBlock{
					Type: AnthropicContentBlockTypeToolUse,
				}

				if msg.ResponsesToolMessage.CallID != nil {
					toolUseBlock.ID = msg.ResponsesToolMessage.CallID
				}
				if msg.ResponsesToolMessage.Name != nil {
					toolUseBlock.Name = msg.ResponsesToolMessage.Name
				}

				// Parse arguments as JSON input
				if msg.ResponsesToolMessage.Arguments != nil && *msg.ResponsesToolMessage.Arguments != "" {
					toolUseBlock.Input = parseJSONInput(*msg.ResponsesToolMessage.Arguments)
				}

				pendingToolCalls = append(pendingToolCalls, toolUseBlock)
			}

		case schemas.ResponsesMessageTypeFunctionCallOutput:
			// Flush any pending tool calls first before processing tool results
			if len(pendingToolCalls) > 0 && currentAssistantMessage != nil {
				// Copy the slice to avoid aliasing issues
				copied := make([]AnthropicContentBlock, len(pendingToolCalls))
				copy(copied, pendingToolCalls)
				currentAssistantMessage.Content = AnthropicContent{
					ContentBlocks: copied,
				}
				anthropicMessages = append(anthropicMessages, *currentAssistantMessage)
				pendingToolCalls = nil
				currentAssistantMessage = nil
			}

			// Handle tool call output - convert to user message with tool_result
			if msg.ResponsesToolMessage != nil {
				toolResultMsg := AnthropicMessage{
					Role: AnthropicMessageRoleUser,
				}

				toolResultBlock := AnthropicContentBlock{
					Type: AnthropicContentBlockTypeToolResult,
				}

				if msg.ResponsesToolMessage.CallID != nil {
					toolResultBlock.ToolUseID = msg.ResponsesToolMessage.CallID
				}

				// Convert tool output content
				if msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput != nil {
					output := msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput
					if output.ResponsesFunctionToolCallOutputStr != nil {
						toolResultBlock.Content = &AnthropicContent{
							ContentStr: output.ResponsesFunctionToolCallOutputStr,
						}
					} else if output.ResponsesFunctionToolCallOutputBlocks != nil {
						var resultContentBlocks []AnthropicContentBlock
						for _, block := range output.ResponsesFunctionToolCallOutputBlocks {
							if convertedBlock := convertContentBlockToAnthropic(block); convertedBlock != nil {
								resultContentBlocks = append(resultContentBlocks, *convertedBlock)
							}
						}
						if len(resultContentBlocks) > 0 {
							toolResultBlock.Content = &AnthropicContent{
								ContentBlocks: resultContentBlocks,
							}
						}
					}
				}

				toolResultMsg.Content = AnthropicContent{
					ContentBlocks: []AnthropicContentBlock{toolResultBlock},
				}

				anthropicMessages = append(anthropicMessages, toolResultMsg)
			}

		case schemas.ResponsesMessageTypeItemReference:
			// Handle item reference as regular text message
			if msg.Content != nil && msg.Content.ContentStr != nil {
				referenceMsg := AnthropicMessage{
					Role: AnthropicMessageRoleUser, // Default to user for references
				}
				if msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleAssistant {
					referenceMsg.Role = AnthropicMessageRoleAssistant
				}

				referenceMsg.Content = AnthropicContent{
					ContentStr: msg.Content.ContentStr,
				}

				anthropicMessages = append(anthropicMessages, referenceMsg)
			}

		// Handle other tool call types that are not natively supported by Anthropic
		case schemas.ResponsesMessageTypeFileSearchCall,
			schemas.ResponsesMessageTypeComputerCall,
			schemas.ResponsesMessageTypeWebSearchCall,
			schemas.ResponsesMessageTypeCodeInterpreterCall,
			schemas.ResponsesMessageTypeLocalShellCall,
			schemas.ResponsesMessageTypeMCPCall,
			schemas.ResponsesMessageTypeCustomToolCall,
			schemas.ResponsesMessageTypeImageGenerationCall:
			// Convert unsupported tool calls to regular text messages
			if msg.ResponsesToolMessage != nil {
				toolCallMsg := AnthropicMessage{
					Role: AnthropicMessageRoleAssistant,
				}

				var description string
				if msg.ResponsesToolMessage.Name != nil {
					description = fmt.Sprintf("Tool call: %s", *msg.ResponsesToolMessage.Name)
					if msg.ResponsesToolMessage.Arguments != nil {
						description += fmt.Sprintf(" with arguments: %s", *msg.ResponsesToolMessage.Arguments)
					}
				} else {
					description = fmt.Sprintf("Tool call of type: %s", msgType)
				}

				toolCallMsg.Content = AnthropicContent{
					ContentStr: &description,
				}

				anthropicMessages = append(anthropicMessages, toolCallMsg)
			}

		case schemas.ResponsesMessageTypeComputerCallOutput,
			schemas.ResponsesMessageTypeLocalShellCallOutput,
			schemas.ResponsesMessageTypeCustomToolCallOutput:
			// Handle tool outputs as user messages
			if msg.ResponsesToolMessage != nil {
				toolOutputMsg := AnthropicMessage{
					Role: AnthropicMessageRoleUser,
				}

				var outputText string
				// Try to extract output text based on tool type
				switch msgType {
				case schemas.ResponsesMessageTypeLocalShellCallOutput:
					if msg.ResponsesToolMessage.ResponsesLocalShellCallOutput != nil {
						outputText = msg.ResponsesToolMessage.ResponsesLocalShellCallOutput.Output
					}
				case schemas.ResponsesMessageTypeCustomToolCallOutput:
					if msg.ResponsesToolMessage.ResponsesCustomToolCallOutput != nil {
						outputText = msg.ResponsesToolMessage.ResponsesCustomToolCallOutput.Output
					}
				}

				if outputText != "" {
					toolOutputMsg.Content = AnthropicContent{
						ContentStr: &outputText,
					}
					anthropicMessages = append(anthropicMessages, toolOutputMsg)
				}
			}

		default:
			// Skip unknown message types or log them for debugging
			continue
		}
	}

	// Flush any remaining pending tool calls
	if len(pendingToolCalls) > 0 && currentAssistantMessage != nil {
		// Copy the slice to avoid aliasing issues
		copied := make([]AnthropicContentBlock, len(pendingToolCalls))
		copy(copied, pendingToolCalls)
		currentAssistantMessage.Content = AnthropicContent{
			ContentBlocks: copied,
		}
		anthropicMessages = append(anthropicMessages, *currentAssistantMessage)
	}

	return anthropicMessages, systemContent
}

// Helper function to parse JSON input arguments back to interface{}
func parseJSONInput(jsonStr string) interface{} {
	if jsonStr == "" || jsonStr == "{}" {
		return map[string]interface{}{}
	}

	var result interface{}
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		// If parsing fails, return as string
		return jsonStr
	}

	return result
}

// Helper function to convert Tool back to AnthropicTool
func convertBifrostToolToAnthropic(tool *schemas.ResponsesTool) *AnthropicTool {
	if tool == nil {
		return nil
	}

	anthropicTool := &AnthropicTool{
		Type: schemas.Ptr(AnthropicToolTypeCustom),
	}

	// Try to extract from ResponsesExtendedTool if present
	if tool.Name != nil {
		anthropicTool.Name = *tool.Name
	}

	if tool.Description != nil {
		anthropicTool.Description = *tool.Description
	}

	// Convert parameters from ToolFunction
	if tool.ResponsesToolFunction != nil {
		anthropicTool.InputSchema = tool.ResponsesToolFunction.Parameters
	}

	return anthropicTool
}

// Helper function to convert ResponsesToolChoice back to AnthropicToolChoice
func convertResponsesToolChoiceToAnthropic(toolChoice *schemas.ResponsesToolChoice) *AnthropicToolChoice {
	if toolChoice == nil {
		return nil
	}
	// String-form choices (auto/any/none/required) have no struct payload.
	if toolChoice.ResponsesToolChoiceStruct == nil && toolChoice.ResponsesToolChoiceStr != nil {
		switch schemas.ResponsesToolChoiceType(*toolChoice.ResponsesToolChoiceStr) {
		case schemas.ResponsesToolChoiceTypeAuto:
			return &AnthropicToolChoice{Type: "auto"}
		case schemas.ResponsesToolChoiceTypeAny, schemas.ResponsesToolChoiceTypeRequired:
			return &AnthropicToolChoice{Type: "any"}
		case schemas.ResponsesToolChoiceTypeNone:
			return &AnthropicToolChoice{Type: "none"}
		default:
			return nil
		}
	}

	if toolChoice.ResponsesToolChoiceStruct == nil {
		return nil
	}

	anthropicChoice := &AnthropicToolChoice{}

	var toolChoiceType *string
	if toolChoice.ResponsesToolChoiceStruct != nil {
		toolChoiceType = schemas.Ptr(string(toolChoice.ResponsesToolChoiceStruct.Type))
	} else {
		toolChoiceType = toolChoice.ResponsesToolChoiceStr
	}

	switch *toolChoiceType {
	case "auto":
		anthropicChoice.Type = "auto"
	case "required":
		anthropicChoice.Type = "any"
	case "function":
		// Handle function type - set as "tool" with specific function name
		if toolChoice.ResponsesToolChoiceStruct != nil && toolChoice.ResponsesToolChoiceStruct.Name != nil {
			anthropicChoice.Type = "tool"
			anthropicChoice.Name = *toolChoice.ResponsesToolChoiceStruct.Name
		}
		return anthropicChoice
	}

	// Legacy fallback: also check for Name field (for backward compatibility)
	if toolChoice.ResponsesToolChoiceStruct != nil && toolChoice.ResponsesToolChoiceStruct.Name != nil {
		anthropicChoice.Type = "tool"
		anthropicChoice.Name = *toolChoice.ResponsesToolChoiceStruct.Name
	}

	return anthropicChoice
}

// Helper function to convert Anthropic content blocks to Responses output messages
func convertAnthropicContentBlocksToResponsesMessages(content []AnthropicContentBlock) []schemas.ResponsesMessage {
	var messages []schemas.ResponsesMessage

	for _, block := range content {
		switch block.Type {
		case "text":
			if block.Text != nil {
				// Append text to existing message
				messages = append(messages, schemas.ResponsesMessage{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentStr: block.Text,
					},
				})
			}

		case "thinking":
			if block.Thinking != nil {
				// Create reasoning message
				messages = append(messages, schemas.ResponsesMessage{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesOutputMessageContentTypeReasoning,
								Text: block.Thinking,
							},
						},
					},
				})
			}

		case "tool_use":
			if block.ID != nil && block.Name != nil {
				// Create function call message
				messages = append(messages, schemas.ResponsesMessage{
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					Status: schemas.Ptr("completed"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:    block.ID,
						Name:      block.Name,
						Arguments: schemas.Ptr(schemas.JsonifyInput(block.Input)),
					},
				})
			}
		case "tool_result":
			if block.ToolUseID != nil {
				// Create function call output message
				msg := schemas.ResponsesMessage{
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
					Status: schemas.Ptr("completed"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: block.ToolUseID,
					},
				}
				// Initialize nested output struct
				msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput = &schemas.ResponsesFunctionToolCallOutput{}
				if block.Content != nil {
					if block.Content.ContentStr != nil {
						msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.
							ResponsesFunctionToolCallOutputStr = block.Content.ContentStr
					} else if block.Content.ContentBlocks != nil {
						var outBlocks []schemas.ResponsesMessageContentBlock
						for _, cb := range block.Content.ContentBlocks {
							switch cb.Type {
							case AnthropicContentBlockTypeText:
								if cb.Text != nil {
									outBlocks = append(outBlocks, schemas.ResponsesMessageContentBlock{
										Type: schemas.ResponsesInputMessageContentBlockTypeText,
										Text: cb.Text,
									})
								}
							case AnthropicContentBlockTypeImage:
								if cb.Source != nil {
									outBlocks = append(outBlocks, cb.toBifrostResponsesImageBlock())
								}
							}
						}
						msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.
							ResponsesFunctionToolCallOutputBlocks = outBlocks
					}
				}
				messages = append(messages, msg)
			}

		default:
			// Handle other block types if needed
		}
	}
	return messages
}

// Helper function to convert ChatMessage output to Anthropic content blocks
func convertBifrostMessagesToAnthropicContent(messages []schemas.ResponsesMessage) []AnthropicContentBlock {
	var contentBlocks []AnthropicContentBlock

	for _, msg := range messages {
		// Handle different message types based on Responses structure
		if msg.Type != nil {
			switch *msg.Type {
			case schemas.ResponsesMessageTypeMessage:
				// Regular text message
				if msg.Content.ContentStr != nil {
					contentBlocks = append(contentBlocks, AnthropicContentBlock{
						Type: "text",
						Text: msg.Content.ContentStr,
					})
				} else if msg.Content.ContentBlocks != nil {
					// Convert content blocks
					for _, block := range msg.Content.ContentBlocks {
						anthropicBlock := convertContentBlockToAnthropic(block)
						if anthropicBlock != nil {
							contentBlocks = append(contentBlocks, *anthropicBlock)
						}
					}
				}

			case schemas.ResponsesMessageTypeFunctionCall:
				if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.CallID != nil {
					toolBlock := AnthropicContentBlock{
						Type: AnthropicContentBlockTypeToolUse,
						ID:   msg.ResponsesToolMessage.CallID,
					}
					if msg.ResponsesToolMessage.Name != nil {
						toolBlock.Name = msg.ResponsesToolMessage.Name
					}
					if msg.ResponsesToolMessage.Arguments != nil && *msg.ResponsesToolMessage.Arguments != "" {
						toolBlock.Input = parseJSONInput(*msg.ResponsesToolMessage.Arguments)
					}
					contentBlocks = append(contentBlocks, toolBlock)
				}

			case schemas.ResponsesMessageTypeFunctionCallOutput:
				// Tool result block - need to extract from ToolMessage
				resultBlock := AnthropicContentBlock{
					Type: "tool_result",
				}

				// Extract result content from ToolMessage or Content
				if msg.ResponsesToolMessage != nil {
					// Copy the call ID to maintain association between result and call
					if msg.ResponsesToolMessage.CallID != nil {
						resultBlock.ToolUseID = msg.ResponsesToolMessage.CallID
					}

					// Try to get content from the tool message structure
					if msg.Content != nil && msg.Content.ContentStr != nil {
						resultBlock.Content = &AnthropicContent{
							ContentStr: msg.Content.ContentStr,
						}
					} else if msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput != nil {
						// Guard access to ResponsesFunctionToolCallOutput
						if msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputStr != nil {
							resultBlock.Content = &AnthropicContent{
								ContentStr: msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputStr,
							}
						} else if msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputBlocks != nil {
							var resultBlocks []AnthropicContentBlock
							for _, block := range msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputBlocks {
								if block.Type == schemas.ResponsesInputMessageContentBlockTypeText {
									resultBlocks = append(resultBlocks, AnthropicContentBlock{
										Type: AnthropicContentBlockTypeText,
										Text: block.Text,
									})
								} else if block.Type == schemas.ResponsesInputMessageContentBlockTypeImage {
									if block.ResponsesInputMessageContentBlockImage != nil && block.ResponsesInputMessageContentBlockImage.ImageURL != nil {
										resultBlocks = append(resultBlocks, AnthropicContentBlock{
											Type: AnthropicContentBlockTypeImage,
											Source: &AnthropicImageSource{
												Type: "url",
												URL:  block.ResponsesInputMessageContentBlockImage.ImageURL,
											},
										})
									}
								}
							}
							resultBlock.Content = &AnthropicContent{
								ContentBlocks: resultBlocks,
							}
						}
					}
				} else if msg.Content != nil {
					// Fallback to msg.Content when ResponsesToolMessage is nil
					if msg.Content.ContentStr != nil {
						resultBlock.Content = &AnthropicContent{
							ContentStr: msg.Content.ContentStr,
						}
					}
				}

				contentBlocks = append(contentBlocks, resultBlock)

			case schemas.ResponsesMessageTypeReasoning:
				// Build thinking from ResponsesReasoning summary, else from reasoning content blocks
				var thinking string
				if msg.ResponsesReasoning != nil && msg.ResponsesReasoning.Summary != nil {
					for _, b := range msg.ResponsesReasoning.Summary {
						thinking += b.Text
					}
				} else if msg.Content != nil && msg.Content.ContentBlocks != nil {
					for _, b := range msg.Content.ContentBlocks {
						if b.Type == schemas.ResponsesOutputMessageContentTypeReasoning && b.Text != nil {
							thinking += *b.Text
						}
					}
				}
				if thinking != "" {
					contentBlocks = append(contentBlocks, AnthropicContentBlock{
						Type:     AnthropicContentBlockTypeThinking,
						Thinking: &thinking,
					})
				}

			default:
				// Handle other types as text if they have content
				if msg.Content.ContentStr != nil {
					contentBlocks = append(contentBlocks, AnthropicContentBlock{
						Type: AnthropicContentBlockTypeText,
						Text: msg.Content.ContentStr,
					})
				}
			}
		}
	}

	return contentBlocks
}

// Helper function to convert ContentBlock to AnthropicContentBlock
func convertContentBlockToAnthropic(block schemas.ResponsesMessageContentBlock) *AnthropicContentBlock {
	switch block.Type {
	case schemas.ResponsesInputMessageContentBlockTypeText, schemas.ResponsesOutputMessageContentTypeText:
		if block.Text != nil {
			return &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeText,
				Text: block.Text,
			}
		}
	case schemas.ResponsesInputMessageContentBlockTypeImage:
		if block.ResponsesInputMessageContentBlockImage != nil && block.ResponsesInputMessageContentBlockImage.ImageURL != nil {
			// Convert using the same logic as ConvertToAnthropicImageBlock
			chatBlock := schemas.ChatContentBlock{
				Type: schemas.ChatContentBlockTypeImage,
				ImageURLStruct: &schemas.ChatInputImage{
					URL: *block.ResponsesInputMessageContentBlockImage.ImageURL,
				},
			}
			anthropicBlock := ConvertToAnthropicImageBlock(chatBlock)
			return &anthropicBlock
		}
	case schemas.ResponsesOutputMessageContentTypeReasoning:
		if block.Text != nil {
			return &AnthropicContentBlock{
				Type:     AnthropicContentBlockTypeThinking,
				Thinking: block.Text,
			}
		}
	}
	return nil
}

func (block AnthropicContentBlock) toBifrostResponsesImageBlock() schemas.ResponsesMessageContentBlock {
	return schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesInputMessageContentBlockTypeImage,
		ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
			ImageURL: schemas.Ptr(getImageURLFromBlock(block)),
		},
	}
}
