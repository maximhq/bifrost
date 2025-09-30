package anthropic

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func (request *AnthropicMessageRequest) ToResponsesBifrostRequest() *schemas.BifrostResponsesRequest {
	provider, model := schemas.ParseModelString(request.Model, schemas.Anthropic)

	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    model,
	}

	// Convert basic parameters
	if request.MaxTokens > 0 || request.Temperature != nil || request.TopP != nil || request.Stream != nil {
		params := &schemas.ResponsesParameters{}
		if request.MaxTokens > 0 {
			params.MaxOutputTokens = &request.MaxTokens
		}
		if request.Temperature != nil {
			params.Temperature = request.Temperature
		}
		if request.TopP != nil {
			params.TopP = request.TopP
		}
		bifrostReq.Params = params
	}

	// Convert messages directly to ChatMessage format
	var bifrostMessages []schemas.ResponsesMessage

	// Handle system message - convert Anthropic system field to first message with role "system"
	if request.System != nil {
		var systemText string
		if request.System.ContentStr != nil {
			systemText = *request.System.ContentStr
		} else if request.System.ContentBlocks != nil {
			// Combine text blocks from system content
			var textParts []string
			for _, block := range *request.System.ContentBlocks {
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
	for _, msg := range request.Messages {
		convertedMessages := convertAnthropicMessageToBifrostResponsesMessages(&msg)
		bifrostMessages = append(bifrostMessages, convertedMessages...)
	}

	// Convert tools if present
	if request.Tools != nil {
		var bifrostTools []schemas.ResponsesTool
		for _, tool := range *request.Tools {
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
	if request.ToolChoice != nil {
		bifrostToolChoice := convertAnthropicToolChoiceToBifrost(request.ToolChoice)
		if bifrostToolChoice != nil {
			bifrostReq.Params.ToolChoice = bifrostToolChoice
		}
	}

	// Set the converted messages
	if len(bifrostMessages) > 0 {
		bifrostReq.Input = bifrostMessages
	}

	// Add parameters
	if bifrostReq.Params == nil {
		bifrostReq.Params = &schemas.ResponsesParameters{}
	}
	if request.MaxTokens > 0 {
		bifrostReq.Params.MaxOutputTokens = &request.MaxTokens
	}
	if request.Temperature != nil {
		bifrostReq.Params.Temperature = request.Temperature
	}
	if request.TopP != nil {
		bifrostReq.Params.TopP = request.TopP
	}
	if request.TopK != nil {
		bifrostReq.Params.ExtraParams["top_k"] = *request.TopK
	}
	if request.StopSequences != nil {
		bifrostReq.Params.ExtraParams["stop"] = *request.StopSequences
	}

	return bifrostReq
}

// ToAnthropicResponsesRequest converts a BifrostRequest with Responses structure back to AnthropicMessageRequest
func ToAnthropicResponsesRequest(bifrostReq *schemas.BifrostResponsesRequest) *AnthropicMessageRequest {
	anthropicReq := &AnthropicMessageRequest{
		Model: bifrostReq.Model,
	}

	// Convert basic parameters
	if bifrostReq.Params != nil {
		if bifrostReq.Params.MaxOutputTokens != nil {
			anthropicReq.MaxTokens = *bifrostReq.Params.MaxOutputTokens
		} else {
			anthropicReq.MaxTokens = AnthropicDefaultMaxTokens // Anthropic default
		}
		if bifrostReq.Params.Temperature != nil {
			anthropicReq.Temperature = bifrostReq.Params.Temperature
		}
		if bifrostReq.Params.TopP != nil {
			anthropicReq.TopP = bifrostReq.Params.TopP
		}
		if bifrostReq.Params.TopLogProbs != nil {
			anthropicReq.TopK = bifrostReq.Params.TopLogProbs
		}
		if bifrostReq.Params.ExtraParams != nil {
			if stop, ok := bifrostReq.Params.ExtraParams["stop"].([]string); ok {
				anthropicReq.StopSequences = &stop
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
				anthropicReq.Tools = &anthropicTools
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

// ToAnthropicResponsesResponse converts an Anthropic response to BifrostResponse with Responses structure
func (anthropicResp *AnthropicMessageResponse) ToResponsesBifrostResponse() *schemas.BifrostResponse {
	if anthropicResp == nil {
		return nil
	}

	// Create the BifrostResponse with Responses structure
	bifrostResp := &schemas.BifrostResponse{
		ID:    anthropicResp.ID,
		Model: anthropicResp.Model,
		ResponsesResponse: &schemas.ResponsesResponse{
			CreatedAt: int(time.Now().Unix()),
		},
	}

	// Convert usage information
	if anthropicResp.Usage != nil {
		bifrostResp.Usage = &schemas.LLMUsage{
			TotalTokens: anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens,
			ResponsesExtendedResponseUsage: &schemas.ResponsesExtendedResponseUsage{
				InputTokens:  anthropicResp.Usage.InputTokens,
				OutputTokens: anthropicResp.Usage.OutputTokens,
			},
		}

		// Handle cached tokens if present
		if anthropicResp.Usage.CacheReadInputTokens > 0 {
			if bifrostResp.Usage.ResponsesExtendedResponseUsage.InputTokensDetails == nil {
				bifrostResp.Usage.ResponsesExtendedResponseUsage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{}
			}
			bifrostResp.Usage.ResponsesExtendedResponseUsage.InputTokensDetails.CachedTokens = anthropicResp.Usage.CacheReadInputTokens
		}
	}

	// Convert content to Responses output messages
	outputMessages := convertAnthropicContentBlocksToResponsesMessages(anthropicResp.Content)
	if len(outputMessages) > 0 {
		bifrostResp.ResponsesResponse.Output = outputMessages
	}

	return bifrostResp
}

// ConvertBifrostResponseToAnthropic converts a BifrostResponse with Responses structure back to AnthropicMessageResponse
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
		for _, block := range *msg.Content.ContentBlocks {
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
							ContentBlocks: &[]schemas.ResponsesMessageContentBlock{block.toBifrostResponsesImageBlock()},
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
						if block.Content.ContentStr != nil {
							bifrostMsg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputStr = block.Content.ContentStr
						} else if block.Content.ContentBlocks != nil {
							var toolMsgContentBlocks []schemas.ResponsesMessageContentBlock
							for _, contentBlock := range *block.Content.ContentBlocks {
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
							bifrostMsg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputBlocks = &toolMsgContentBlocks
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

	// Group items by logical messages
	i := 0
	for i < len(messages) {
		bifrostMsg := messages[i]

		if bifrostMsg.Type != nil && *bifrostMsg.Type == schemas.ResponsesMessageTypeMessage {
			if bifrostMsg.Role == schemas.Ptr(schemas.ResponsesInputMessageRoleSystem) {
				// Extract system content
				if bifrostMsg.Content.ContentStr != nil {
					systemContent = &AnthropicContent{
						ContentStr: bifrostMsg.Content.ContentStr,
					}
				} else if bifrostMsg.Content.ContentBlocks != nil {
					// Convert content blocks
					contentBlocks := []AnthropicContentBlock{}
					for _, block := range *bifrostMsg.Content.ContentBlocks {
						anthropicBlock := convertContentBlockToAnthropic(block)
						if anthropicBlock != nil {
							contentBlocks = append(contentBlocks, *anthropicBlock)
						}
					}
					if len(contentBlocks) > 0 {
						systemContent = &AnthropicContent{
							ContentBlocks: &contentBlocks,
						}
					}
				}
				i++
				continue
			}

			// Regular message - collect associated tool calls/results
			msg := AnthropicMessage{
				Role: AnthropicMessageRole(*bifrostMsg.Role),
			}

			contentBlocks := []AnthropicContentBlock{}

			// Add text/image content if present
			if bifrostMsg.Content.ContentStr != nil {
				msg.Content = AnthropicContent{
					ContentStr: bifrostMsg.Content.ContentStr,
				}
			} else if bifrostMsg.Content.ContentBlocks != nil {
				for _, block := range *bifrostMsg.Content.ContentBlocks {
					anthropicBlock := convertContentBlockToAnthropic(block)
					if anthropicBlock != nil {
						contentBlocks = append(contentBlocks, *anthropicBlock)
					}
				}
			}

			i++

			// Look ahead for related tool calls and results
			for i < len(messages) && messages[i].Type != nil &&
				(*messages[i].Type == schemas.ResponsesMessageTypeFunctionCall || *messages[i].Type == schemas.ResponsesMessageTypeFunctionCallOutput) {

				toolItem := messages[i]
				if *toolItem.Type == schemas.ResponsesMessageTypeFunctionCall && toolItem.ResponsesToolMessage != nil && toolItem.ResponsesToolMessage.Name != nil {
					// Convert to tool_use block
					toolBlock := AnthropicContentBlock{
						Type:  "tool_use",
						ID:    toolItem.ResponsesToolMessage.CallID,
						Name:  toolItem.ResponsesToolMessage.Name,
						Input: parseJSONInput(*toolItem.ResponsesToolMessage.Arguments),
					}
					contentBlocks = append(contentBlocks, toolBlock)
				} else if *toolItem.Type == schemas.ResponsesMessageTypeFunctionCallOutput && toolItem.ResponsesToolMessage != nil && toolItem.ResponsesToolMessage.ResponsesFunctionToolCallOutput != nil {
					// Convert to tool_result block
					resultContent := AnthropicContent{}

					if toolItem.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputStr != nil {
						resultContent.ContentStr = toolItem.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputStr
					} else if toolItem.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputBlocks != nil {
						contentBlocks := []AnthropicContentBlock{}
						for _, block := range *toolItem.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputBlocks {
							anthropicBlock := convertContentBlockToAnthropic(block)
							if anthropicBlock != nil {
								contentBlocks = append(contentBlocks, *anthropicBlock)
							}
						}
						resultContent.ContentBlocks = &contentBlocks
					}

					toolBlock := AnthropicContentBlock{
						Type:      AnthropicContentBlockTypeToolResult,
						ToolUseID: toolItem.ResponsesToolMessage.CallID,
						Content:   &resultContent,
					}
					contentBlocks = append(contentBlocks, toolBlock)
				}
				i++
			}

			// Set content blocks if we have any
			if len(contentBlocks) > 0 {
				msg.Content = AnthropicContent{
					ContentBlocks: &contentBlocks,
				}
			}

			anthropicMessages = append(anthropicMessages, msg)
		} else {
			i++
		}
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
		Type: schemas.Ptr("function"),
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
	if toolChoice == nil || toolChoice.ResponsesToolChoiceStruct == nil {
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
	}

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
						ContentBlocks: &[]schemas.ResponsesMessageContentBlock{
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
					for _, block := range *msg.Content.ContentBlocks {
						anthropicBlock := convertContentBlockToAnthropic(block)
						if anthropicBlock != nil {
							contentBlocks = append(contentBlocks, *anthropicBlock)
						}
					}
				}

			case schemas.ResponsesMessageTypeFunctionCall:
				// Tool use block - need to extract from AssistantMessage.ToolCalls
				if msg.ResponsesToolMessage.CallID != nil {
					toolBlock := AnthropicContentBlock{
						Type: "tool_use",
						ID:   msg.ResponsesToolMessage.CallID,
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
					// Try to get content from the tool message structure
					if msg.Content.ContentStr != nil {
						resultBlock.Content = &AnthropicContent{
							ContentStr: msg.Content.ContentStr,
						}
					} else if msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput != nil {
						resultBlock.Content = &AnthropicContent{
							ContentStr: msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputStr,
						}
					} else if msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputBlocks != nil {
						var resultBlocks []AnthropicContentBlock
						for _, block := range *msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputBlocks {
							if block.Type == schemas.ResponsesInputMessageContentBlockTypeText {
								resultBlocks = append(resultBlocks, AnthropicContentBlock{
									Type: AnthropicContentBlockTypeText,
									Text: block.Text,
								})
							} else if block.Type == schemas.ResponsesInputMessageContentBlockTypeImage {
								if block.ResponsesInputMessageContentBlockImage.ImageURL != nil {
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
							ContentBlocks: &resultBlocks,
						}
					}
				}

				contentBlocks = append(contentBlocks, resultBlock)

			case schemas.ResponsesMessageTypeReasoning:
				// Thinking block (Claude 3.5 Sonnet specific)
				if msg.Content.ContentStr != nil {
					contentBlock := AnthropicContentBlock{
						Type: AnthropicContentBlockTypeThinking,
					}

					if msg.ResponsesReasoning != nil {
						var thinking string
						if msg.ResponsesReasoning.Summary != nil {
							for _, block := range msg.ResponsesReasoning.Summary {
								thinking += block.Text
							}
						}
						contentBlock.Thinking = &thinking
					}
					contentBlocks = append(contentBlocks, contentBlock)
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
		// Handle image blocks - would need proper conversion
		// This is a placeholder implementation
		return &AnthropicContentBlock{
			Type: AnthropicContentBlockTypeImage,
			Source: &AnthropicImageSource{
				Type: "url",
				URL:  block.ResponsesInputMessageContentBlockImage.ImageURL,
			},
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
