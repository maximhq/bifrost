package anthropic

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func (request *AnthropicMessageRequest) ToResponsesAPIBifrostRequest() *schemas.BifrostRequest {
	provider, model := schemas.ParseModelString(request.Model, schemas.Anthropic)

	bifrostReq := &schemas.BifrostRequest{
		Provider: provider,
		Model:    model,
	}

	// Convert basic parameters
	if request.MaxTokens > 0 || request.Temperature != nil || request.TopP != nil || request.Stream != nil {
		params := &schemas.ModelParameters{}
		if request.MaxTokens > 0 {
			params.MaxTokens = &request.MaxTokens
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
	var bifrostMessages []schemas.ChatMessage

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
			systemMsg := schemas.ChatMessage{
				Role: schemas.ModelChatMessageRoleSystem,
				Content: schemas.ChatMessageContent{
					ContentStr: &systemText,
				},
				ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
					Type: schemas.Ptr("message"),
				},
			}
			bifrostMessages = append(bifrostMessages, systemMsg)
		}
	}

	// Convert regular messages
	for _, msg := range request.Messages {
		convertedMessages := convertAnthropicMessageToBifrostMessages(&msg)
		bifrostMessages = append(bifrostMessages, convertedMessages...)
	}

	// Convert tools if present
	if request.Tools != nil {
		var bifrostTools []schemas.Tool
		for _, tool := range *request.Tools {
			bifrostTool := convertAnthropicToolToBifrost(&tool)
			if bifrostTool != nil {
				bifrostTools = append(bifrostTools, *bifrostTool)
			}
		}
		if len(bifrostTools) > 0 {
			bifrostReq.Params.Tools = &bifrostTools
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
		bifrostReq.Input.ResponsesInput = &bifrostMessages
	}

	// Apply parameter validation
	bifrostReq.Params = schemas.ValidateAndFilterParamsForProvider(provider, bifrostReq.Params)

	return bifrostReq
}

// convertAnthropicMessageToBifrostMessages converts AnthropicMessage to ChatMessage format
func convertAnthropicMessageToBifrostMessages(msg *AnthropicMessage) []schemas.ChatMessage {
	var bifrostMessages []schemas.ChatMessage

	// Handle text content
	if msg.Content.ContentStr != nil {
		bifrostMsg := schemas.ChatMessage{
			Role: schemas.ModelChatMessageRole(msg.Role),
			Content: schemas.ChatMessageContent{
				ContentStr: msg.Content.ContentStr,
			},
			ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
				Type: schemas.Ptr("message"),
			},
		}
		bifrostMessages = append(bifrostMessages, bifrostMsg)
	} else if msg.Content.ContentBlocks != nil {
		// Handle content blocks
		for _, block := range *msg.Content.ContentBlocks {
			switch block.Type {
			case "text":
				if block.Text != nil {
					bifrostMsg := schemas.ChatMessage{
						Role: schemas.ModelChatMessageRole(msg.Role),
						Content: schemas.ChatMessageContent{
							ContentStr: block.Text,
						},
						ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
							Type: schemas.Ptr("message"),
						},
					}
					bifrostMessages = append(bifrostMessages, bifrostMsg)
				}
			case "image":
				if block.Source != nil {
					// Convert image to content block
					var imageURL string
					if block.Source.Data != nil {
						mime := "image/png"
						if block.Source.MediaType != nil && *block.Source.MediaType != "" {
							mime = *block.Source.MediaType
						}
						imageURL = "data:" + mime + ";base64," + *block.Source.Data
					} else if block.Source.URL != nil {
						imageURL = *block.Source.URL
					}

					contentBlock := schemas.ContentBlock{
						Type: schemas.ContentBlockTypeImage,
						ResponsesAPIExtendedContentBlock: &schemas.ResponsesAPIExtendedContentBlock{
							InputMessageContentBlockImage: &schemas.InputMessageContentBlockImage{
								ImageURL: &imageURL,
							},
						},
					}

					bifrostMsg := schemas.ChatMessage{
						Role: schemas.ModelChatMessageRole(msg.Role),
						Content: schemas.ChatMessageContent{
							ContentBlocks: &[]schemas.ContentBlock{contentBlock},
						},
						ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
							Type: schemas.Ptr("message"),
						},
					}
					bifrostMessages = append(bifrostMessages, bifrostMsg)
				}
			case "tool_use":
				// Convert tool use to function call message
				if block.ID != nil && block.Name != nil {
					bifrostMsg := schemas.ChatMessage{
						Name:    block.Name,
						Role:    schemas.ChatMessageRoleAssistant,
						Content: schemas.ChatMessageContent{},
						ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
							Type:      schemas.Ptr("function_call"),
							ID:        block.ID,
							Status:    schemas.Ptr("completed"),
							Arguments: schemas.Ptr(jsonifyInput(block.Input)),
						},
						ToolMessage: &schemas.ToolMessage{
							ResponsesAPIToolMessage: &schemas.ResponsesAPIToolMessage{
								CallID: block.ID,
							},
						},
					}
					bifrostMessages = append(bifrostMessages, bifrostMsg)
				}
			case "tool_result":
				// Convert tool result to function call output message
				if block.ToolUseID != nil {
					output := ""
					if block.Content != nil {
						if block.Content.ContentStr != nil {
							output = *block.Content.ContentStr
						} else if block.Content.ContentBlocks != nil {
							// Handle complex content by combining text blocks
							var textParts []string
							for _, resultBlock := range *block.Content.ContentBlocks {
								if resultBlock.Text != nil {
									textParts = append(textParts, *resultBlock.Text)
								}
							}
							if len(textParts) > 0 {
								output = strings.Join(textParts, "\n")
							}
						}
					}

					bifrostMsg := schemas.ChatMessage{
						Role: schemas.ModelChatMessageRoleTool,
						Content: schemas.ChatMessageContent{
							ContentStr: &output,
						},
						ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
							Type: schemas.Ptr("function_call_output"),
						},
						ToolMessage: &schemas.ToolMessage{
							ResponsesAPIToolMessage: &schemas.ResponsesAPIToolMessage{
								CallID: block.ToolUseID,
								FunctionToolCallOutput: &schemas.FunctionToolCallOutput{
									Output: output,
								},
							},
						},
					}
					bifrostMessages = append(bifrostMessages, bifrostMsg)
				}
			}
		}
	}

	return bifrostMessages
}

// convertAnthropicToolToBifrost converts AnthropicTool to schemas.Tool
func convertAnthropicToolToBifrost(tool *AnthropicTool) *schemas.Tool {
	if tool == nil {
		return nil
	}

	bifrostTool := &schemas.Tool{
		Type: tool.Type,
		ResponsesAPIExtendedTool: &schemas.ResponsesAPIExtendedTool{
			Name:        &tool.Name,
			Description: &tool.Description,
			ToolFunction: &schemas.ToolFunction{
				Parameters: *tool.InputSchema,
			},
		},
	}

	return bifrostTool
}

// convertAnthropicToolChoiceToBifrost converts AnthropicToolChoice to schemas.ToolChoice
func convertAnthropicToolChoiceToBifrost(toolChoice *AnthropicToolChoice) *schemas.ToolChoice {
	if toolChoice == nil {
		return nil
	}

	bifrostToolChoice := &schemas.ToolChoice{}

	// Handle string format
	if toolChoice.Type != "" {
		switch toolChoice.Type {
		case "auto":
			bifrostToolChoice.ToolChoiceStr = schemas.Ptr("auto")
		case "any":
			bifrostToolChoice.ToolChoiceStr = schemas.Ptr("required")
		case "none":
			bifrostToolChoice.ToolChoiceStr = schemas.Ptr("none")
		default:
			bifrostToolChoice.ToolChoiceStr = schemas.Ptr("auto")
		}
	}

	return bifrostToolChoice
}

// ConvertBifrostResponseToAnthropic converts a BifrostResponse with ResponsesAPI structure back to AnthropicMessageResponse
func ToAnthropicResponsesAPIResponse(bifrostResp *schemas.BifrostResponse) *AnthropicMessageResponse {
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

		responsesAPIExtendedResponseUsage := bifrostResp.Usage.ResponsesAPIExtendedResponseUsage

		if responsesAPIExtendedResponseUsage != nil && responsesAPIExtendedResponseUsage.InputTokens > 0 {
			anthropicResp.Usage.InputTokens = responsesAPIExtendedResponseUsage.InputTokens
		}

		if responsesAPIExtendedResponseUsage != nil && responsesAPIExtendedResponseUsage.OutputTokens > 0 {
			anthropicResp.Usage.OutputTokens = responsesAPIExtendedResponseUsage.OutputTokens
		}

		// Handle cached tokens if present
		if responsesAPIExtendedResponseUsage != nil &&
			responsesAPIExtendedResponseUsage.InputTokensDetails != nil &&
			responsesAPIExtendedResponseUsage.InputTokensDetails.CachedTokens > 0 {
			anthropicResp.Usage.CacheReadInputTokens = responsesAPIExtendedResponseUsage.InputTokensDetails.CachedTokens
		}
	}

	// Convert output messages to Anthropic content blocks
	var contentBlocks []AnthropicContentBlock
	if bifrostResp.ResponseAPIExtendedResponse != nil && bifrostResp.ResponseAPIExtendedResponse.Output != nil {
		contentBlocks = convertBifrostMessagesToAnthropicContent(bifrostResp.ResponseAPIExtendedResponse.Output)
	}

	if len(contentBlocks) > 0 {
		anthropicResp.Content = contentBlocks
	}

	// Set default stop reason - could be enhanced based on additional context
	stopReason := "end_turn"
	anthropicResp.StopReason = &stopReason

	// Check if there are tool calls to set appropriate stop reason
	for _, block := range contentBlocks {
		if block.Type == "tool_use" {
			toolStopReason := "tool_use"
			anthropicResp.StopReason = &toolStopReason
			break
		}
	}

	return anthropicResp
}

// ToAnthropicResponsesAPIRequest converts a BifrostRequest with ResponsesAPI structure back to AnthropicMessageRequest
func ToAnthropicResponsesAPIRequest(bifrostReq *schemas.BifrostRequest) *AnthropicMessageRequest {
	anthropicReq := &AnthropicMessageRequest{
		Model: bifrostReq.Model,
	}

	// Convert basic parameters
	if bifrostReq.Params != nil {
		if bifrostReq.Params.MaxTokens != nil {
			anthropicReq.MaxTokens = *bifrostReq.Params.MaxTokens
		}
		if bifrostReq.Params.Temperature != nil {
			anthropicReq.Temperature = bifrostReq.Params.Temperature
		}
		if bifrostReq.Params.TopP != nil {
			anthropicReq.TopP = bifrostReq.Params.TopP
		}
		if bifrostReq.Params.TopK != nil {
			anthropicReq.TopK = bifrostReq.Params.TopK
		}
		if bifrostReq.Params.StopSequences != nil {
			anthropicReq.StopSequences = bifrostReq.Params.StopSequences
		}

		// Convert tools
		if bifrostReq.Params.Tools != nil {
			anthropicTools := []AnthropicTool{}
			for _, tool := range *bifrostReq.Params.Tools {
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
			anthropicToolChoice := convertResponsesAPIToolChoiceToAnthropic(bifrostReq.Params.ToolChoice)
			if anthropicToolChoice != nil {
				anthropicReq.ToolChoice = anthropicToolChoice
			}
		}
	}

	// Convert messages - we need to process different input formats
	var inputItems []schemas.ResponsesAPIInputItem

	if bifrostReq.Input.ResponsesInput != nil {
		// Convert ChatMessage back to ResponsesAPIInputItem format
		inputItems = convertBifrostMessagesToResponsesAPIItems(*bifrostReq.Input.ResponsesInput)
	}

	if len(inputItems) > 0 {
		anthropicMessages, systemContent := convertResponsesAPIItemsToAnthropicMessages(inputItems)

		// Set system message if present
		if systemContent != nil {
			anthropicReq.System = systemContent
		}

		// Set regular messages
		anthropicReq.Messages = anthropicMessages
	}

	return anthropicReq
}

// ToAnthropicResponsesAPIResponse converts an Anthropic response to BifrostResponse with ResponsesAPI structure
func (anthropicResp *AnthropicMessageResponse) ToResponsesAPIBifrostResponse() *schemas.BifrostResponse {
	if anthropicResp == nil {
		return nil
	}

	_, model := schemas.ParseModelString(anthropicResp.Model, schemas.Anthropic)

	// Create the BifrostResponse with ResponsesAPI structure
	bifrostResp := &schemas.BifrostResponse{
		ID:    anthropicResp.ID,
		Model: model,
		ResponseAPIExtendedResponse: &schemas.ResponseAPIExtendedResponse{
			ResponsesAPIExtendedRequestParams: &schemas.ResponsesAPIExtendedRequestParams{
				Model: anthropicResp.Model,
			},
			CreatedAt: int(time.Now().Unix()),
		},
	}

	// Convert usage information
	if anthropicResp.Usage != nil {
		bifrostResp.Usage = &schemas.LLMUsage{
			PromptTokens:     anthropicResp.Usage.InputTokens,
			CompletionTokens: anthropicResp.Usage.OutputTokens,
			TotalTokens:      anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens,
			ResponsesAPIExtendedResponseUsage: &schemas.ResponsesAPIExtendedResponseUsage{
				InputTokens:  anthropicResp.Usage.InputTokens,
				OutputTokens: anthropicResp.Usage.OutputTokens,
			},
		}

		// Handle cached tokens if present
		if anthropicResp.Usage.CacheReadInputTokens > 0 {
			if bifrostResp.Usage.ResponsesAPIExtendedResponseUsage.InputTokensDetails == nil {
				bifrostResp.Usage.ResponsesAPIExtendedResponseUsage.InputTokensDetails = &schemas.ResponsesAPIResponseInputTokens{}
			}
			bifrostResp.Usage.ResponsesAPIExtendedResponseUsage.InputTokensDetails.CachedTokens = anthropicResp.Usage.CacheReadInputTokens
		}
	}

	// Convert content to ResponsesAPI output messages
	outputMessages := convertAnthropicContentToResponsesAPIOutput(anthropicResp.Content)
	if len(outputMessages) > 0 {
		bifrostResp.ResponseAPIExtendedResponse.Output = outputMessages
	}

	// Note: Finish reason would be handled at the choice level if needed
	// convertAnthropicStopReasonToFinishReason(*anthropicResp.StopReason)

	return bifrostResp
}

// Helper function to convert system message to ResponsesAPIInputItem
func convertSystemToInputItem(system *AnthropicContent) *schemas.ResponsesAPIInputItem {
	item := &schemas.ResponsesAPIInputItem{
		Type: schemas.Ptr("message"),
	}

	// Create InputMessage for system content
	inputMsg := &schemas.InputMessage{
		Role: "system",
	}

	if system.ContentStr != nil {
		inputMsg.Content = schemas.InputMessageContent{
			ContentStr: system.ContentStr,
		}
	} else if system.ContentBlocks != nil {
		contentBlocks := []schemas.InputMessageContentBlock{}
		for _, block := range *system.ContentBlocks {
			if block.Type == "text" && block.Text != nil {
				contentBlocks = append(contentBlocks, schemas.InputMessageContentBlock{
					Type: "input_text",
					InputMessageContentBlockText: &schemas.InputMessageContentBlockText{
						Text: *block.Text,
					},
				})
			}
		}
		if len(contentBlocks) > 0 {
			inputMsg.Content = schemas.InputMessageContent{
				ContentBlocks: &contentBlocks,
			}
		}
	}

	item.InputMessage = inputMsg
	return item
}

// Helper function to convert AnthropicMessage to ResponsesAPIInputItem(s)
// Returns a slice because tool_use and tool_result blocks create separate items
func convertAnthropicMessageToInputItems(msg *AnthropicMessage) []schemas.ResponsesAPIInputItem {
	var items []schemas.ResponsesAPIInputItem

	// Create main message item
	messageItem := schemas.ResponsesAPIInputItem{
		Type: schemas.Ptr("message"),
	}

	// Create InputMessage
	inputMsg := &schemas.InputMessage{
		Role: schemas.ModelChatMessageRole(msg.Role),
	}

	// Convert content
	if msg.Content.ContentStr != nil {
		inputMsg.Content = schemas.InputMessageContent{
			ContentStr: msg.Content.ContentStr,
		}
		messageItem.InputMessage = inputMsg
		items = append(items, messageItem)
	} else if msg.Content.ContentBlocks != nil {
		contentBlocks := []schemas.InputMessageContentBlock{}

		for _, block := range *msg.Content.ContentBlocks {
			switch block.Type {
			case "text":
				if block.Text != nil {
					contentBlocks = append(contentBlocks, schemas.InputMessageContentBlock{
						Type: "input_text",
						InputMessageContentBlockText: &schemas.InputMessageContentBlockText{
							Text: *block.Text,
						},
					})
				}
			case "image":
				if block.Source != nil {
					contentBlocks = append(contentBlocks, schemas.InputMessageContentBlock{
						Type: "input_image",
						InputMessageContentBlockImage: &schemas.InputMessageContentBlockImage{
							ImageURL: func() *string {
								if block.Source.Data != nil {
									mime := "image/png"
									if block.Source.MediaType != nil && *block.Source.MediaType != "" {
										mime = *block.Source.MediaType
									}
									url := "data:" + mime + ";base64," + *block.Source.Data
									return &url
								}
								if block.Source.URL != nil {
									return block.Source.URL
								}
								return nil
							}(),
						},
					})
				}
			case "tool_use":
				// Create separate function_call item for tool use
				if block.ID != nil && block.Name != nil {
					toolItem := schemas.ResponsesAPIInputItem{
						Type:      schemas.Ptr("function_call"),
						ID:        block.ID,
						CallID:    block.ID,
						Name:      block.Name,
						Arguments: schemas.Ptr(jsonifyInput(block.Input)),
					}
					items = append(items, toolItem)
				}
			case "tool_result":
				// Create separate function_call_output item for tool result
				if block.ToolUseID != nil {
					output := ""
					if block.Content != nil {
						if block.Content.ContentStr != nil {
							output = *block.Content.ContentStr
						} else if block.Content.ContentBlocks != nil {
							// Handle complex content by combining text blocks
							var textParts []string
							for _, resultBlock := range *block.Content.ContentBlocks {
								if resultBlock.Text != nil {
									textParts = append(textParts, *resultBlock.Text)
								}
							}
							if len(textParts) > 0 {
								output = strings.Join(textParts, "\n")
							}
						}
					}

					resultItem := schemas.ResponsesAPIInputItem{
						Type:   schemas.Ptr("function_call_output"),
						CallID: block.ToolUseID,
						FunctionToolCallOutput: &schemas.FunctionToolCallOutput{
							Output: output,
						},
					}
					items = append(items, resultItem)
				}
			}
		}

		// Add main message item if it has content blocks
		if len(contentBlocks) > 0 {
			inputMsg.Content = schemas.InputMessageContent{
				ContentBlocks: &contentBlocks,
			}
			messageItem.InputMessage = inputMsg
			items = append(items, messageItem)
		} else if len(items) == 0 {
			// If no content blocks and no tool items, still add the message item
			messageItem.InputMessage = inputMsg
			items = append(items, messageItem)
		}
	} else {
		// Empty content case
		messageItem.InputMessage = inputMsg
		items = append(items, messageItem)
	}

	return items
}

// Helper function to convert Anthropic tool to ResponsesAPI tool
func convertAnthropicToolToResponsesAPITool(tool *AnthropicTool) *schemas.ResponsesAPITool {
	return &schemas.ResponsesAPITool{
		Type: "function",
		ResponsesAPIExtendedTool: schemas.ResponsesAPIExtendedTool{
			Name:        &tool.Name,
			Description: &tool.Description,
			ToolFunction: &schemas.ToolFunction{
				Parameters: *tool.InputSchema,
			},
		},
	}
}

// Helper function to convert Anthropic tool choice to ResponsesAPI tool choice
func convertAnthropicToolChoiceToResponsesAPI(toolChoice *AnthropicToolChoice) *schemas.ResponsesAPIToolChoice {
	choice := &schemas.ResponsesAPIToolChoice{}

	switch toolChoice.Type {
	case "auto":
		choice.ResponsesAPIExtendedToolChoice = schemas.ResponsesAPIExtendedToolChoice{
			Mode: schemas.Ptr("auto"),
		}
	case "any":
		choice.ResponsesAPIExtendedToolChoice = schemas.ResponsesAPIExtendedToolChoice{
			Mode: schemas.Ptr("required"),
		}
	case "tool":
		if toolChoice.Name != "" {
			choice.Type = schemas.Ptr("function")
			choice.ResponsesAPIExtendedToolChoice = schemas.ResponsesAPIExtendedToolChoice{
				Name: &toolChoice.Name,
			}
		}
	}

	return choice
}

// Helper function to convert InputMessageContentBlocks to BifrostContentBlocks
func convertInputContentBlocksToBifrost(blocks *[]schemas.InputMessageContentBlock) *[]schemas.ContentBlock {
	if blocks == nil {
		return nil
	}

	bifrostBlocks := []schemas.ContentBlock{}
	for _, block := range *blocks {
		switch block.Type {
		case "input_text":
			if block.InputMessageContentBlockText != nil {
				bifrostBlocks = append(bifrostBlocks, schemas.ContentBlock{
					Type: schemas.ContentBlockTypeText,
					Text: &block.InputMessageContentBlockText.Text,
				})
			}
		case "input_image":
			if block.InputMessageContentBlockImage != nil && block.InputMessageContentBlockImage.ImageURL != nil {
				// Note: Image block conversion needs proper structure
				bifrostBlocks = append(bifrostBlocks, schemas.ContentBlock{
					Type: schemas.ContentBlockTypeImage,
					// Image URL structure needs to be handled properly
				})
			}
		}
	}

	if len(bifrostBlocks) == 0 {
		return nil
	}
	return &bifrostBlocks
}

// Helper function to convert ChatMessage back to ResponsesAPIInputItem format
func convertBifrostMessagesToResponsesAPIItems(messages []schemas.ChatMessage) []schemas.ResponsesAPIInputItem {
	var items []schemas.ResponsesAPIInputItem

	for _, msg := range messages {
		// Handle different message types
		if msg.Role == schemas.ModelChatMessageRoleSystem {
			// System message
			item := schemas.ResponsesAPIInputItem{
				Type: schemas.Ptr("message"),
				InputMessage: &schemas.InputMessage{
					Role: "system",
					Content: schemas.InputMessageContent{
						ContentStr:    msg.Content.ContentStr,
						ContentBlocks: convertBifrostContentBlocksToInput(msg.Content.ContentBlocks),
					},
				},
			}
			items = append(items, item)
		} else if msg.AssistantMessage != nil && msg.AssistantMessage.ResponsesAPIExtendedAssistantMessage != nil &&
			msg.AssistantMessage.ResponsesAPIExtendedAssistantMessage.FunctionToolCall != nil {

		} else if msg.ToolMessage != nil && msg.ToolMessage.ResponsesAPIToolMessage != nil {
			// Tool result message - convert to function_call_output item
			output := ""
			if msg.Content.ContentStr != nil {
				output = *msg.Content.ContentStr
			}

			item := schemas.ResponsesAPIInputItem{
				Type:   schemas.Ptr("function_call_output"),
				CallID: msg.ToolMessage.ResponsesAPIToolMessage.CallID,
				FunctionToolCallOutput: &schemas.FunctionToolCallOutput{
					Output: output,
				},
			}
			items = append(items, item)
		} else {
			// Regular message
			item := schemas.ResponsesAPIInputItem{
				Type: schemas.Ptr("message"),
				InputMessage: &schemas.InputMessage{
					Role: msg.Role,
					Content: schemas.InputMessageContent{
						ContentStr:    msg.Content.ContentStr,
						ContentBlocks: convertBifrostContentBlocksToInput(msg.Content.ContentBlocks),
					},
				},
			}
			items = append(items, item)
		}
	}

	return items
}

// Helper function to convert BifrostContentBlocks to InputMessageContentBlocks
func convertBifrostContentBlocksToInput(blocks *[]schemas.ContentBlock) *[]schemas.InputMessageContentBlock {
	if blocks == nil {
		return nil
	}

	var inputBlocks []schemas.InputMessageContentBlock
	for _, block := range *blocks {
		switch block.Type {
		case schemas.ContentBlockTypeText:
			if block.Text != nil {
				inputBlocks = append(inputBlocks, schemas.InputMessageContentBlock{
					Type: "input_text",
					InputMessageContentBlockText: &schemas.InputMessageContentBlockText{
						Text: *block.Text,
					},
				})
			}
		case schemas.ContentBlockTypeImage:
			// Handle image blocks - would need proper ImageURL extraction
			inputBlocks = append(inputBlocks, schemas.InputMessageContentBlock{
				Type:                          "input_image",
				InputMessageContentBlockImage: &schemas.InputMessageContentBlockImage{
					// Image URL would need to be extracted from the block structure
				},
			})
		}
	}

	if len(inputBlocks) == 0 {
		return nil
	}
	return &inputBlocks
}

// Helper function to convert ResponsesAPIInputItems back to AnthropicMessages
func convertResponsesAPIItemsToAnthropicMessages(items []schemas.ResponsesAPIInputItem) ([]AnthropicMessage, *AnthropicContent) {
	var messages []AnthropicMessage
	var systemContent *AnthropicContent

	// Group items by logical messages
	i := 0
	for i < len(items) {
		item := items[i]

		if item.Type != nil && *item.Type == "message" && item.InputMessage != nil {
			if item.InputMessage.Role == "system" {
				// Extract system content
				systemContent = convertInputMessageContentToAnthropic(item.InputMessage.Content)
				i++
				continue
			}

			// Regular message - collect associated tool calls/results
			msg := AnthropicMessage{
				Role: AnthropicMessageRole(item.InputMessage.Role),
			}

			contentBlocks := []AnthropicContentBlock{}

			// Add text/image content if present
			if item.InputMessage.Content.ContentStr != nil {
				msg.Content = AnthropicContent{
					ContentStr: item.InputMessage.Content.ContentStr,
				}
			} else if item.InputMessage.Content.ContentBlocks != nil {
				// Convert input content blocks to anthropic content blocks
				for _, inputBlock := range *item.InputMessage.Content.ContentBlocks {
					anthropicBlock := convertInputContentBlockToAnthropic(inputBlock)
					if anthropicBlock != nil {
						contentBlocks = append(contentBlocks, *anthropicBlock)
					}
				}
			}

			i++

			// Look ahead for related tool calls and results
			for i < len(items) && items[i].Type != nil &&
				(*items[i].Type == "function_call" || *items[i].Type == "function_call_output") {

				toolItem := items[i]
				if *toolItem.Type == "function_call" && toolItem.FunctionToolCall != nil && toolItem.Name != nil {
					// Convert to tool_use block
					toolBlock := AnthropicContentBlock{
						Type:  "tool_use",
						ID:    toolItem.ID,
						Name:  toolItem.Name,
						Input: parseJSONInput(*toolItem.Arguments),
					}
					contentBlocks = append(contentBlocks, toolBlock)
				} else if *toolItem.Type == "function_call_output" && toolItem.FunctionToolCallOutput != nil {
					// Convert to tool_result block
					resultContent := AnthropicContent{
						ContentStr: &toolItem.FunctionToolCallOutput.Output,
					}

					toolBlock := AnthropicContentBlock{
						Type:      "tool_result",
						ToolUseID: toolItem.CallID,
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

			messages = append(messages, msg)
		} else {
			i++
		}
	}

	return messages, systemContent
}

// Helper function to convert InputMessageContent to AnthropicContent
func convertInputMessageContentToAnthropic(content schemas.InputMessageContent) *AnthropicContent {
	if content.ContentStr != nil {
		return &AnthropicContent{
			ContentStr: content.ContentStr,
		}
	}

	if content.ContentBlocks != nil {
		var blocks []AnthropicContentBlock
		for _, block := range *content.ContentBlocks {
			anthropicBlock := convertInputContentBlockToAnthropic(block)
			if anthropicBlock != nil {
				blocks = append(blocks, *anthropicBlock)
			}
		}

		if len(blocks) > 0 {
			return &AnthropicContent{
				ContentBlocks: &blocks,
			}
		}
	}

	return nil
}

// Helper function to convert InputMessageContentBlock to AnthropicContentBlock
func convertInputContentBlockToAnthropic(block schemas.InputMessageContentBlock) *AnthropicContentBlock {
	switch block.Type {
	case "input_text":
		if block.InputMessageContentBlockText != nil {
			return &AnthropicContentBlock{
				Type: "text",
				Text: &block.InputMessageContentBlockText.Text,
			}
		}
	case "input_image":
		if block.InputMessageContentBlockImage != nil && block.InputMessageContentBlockImage.ImageURL != nil {
			// Parse the image URL to extract source information
			url := *block.InputMessageContentBlockImage.ImageURL
			source := parseImageURLToSource(url)
			return &AnthropicContentBlock{
				Type:   "image",
				Source: source,
			}
		}
	}

	return nil
}

// Helper function to parse image URL back to AnthropicImageSource
func parseImageURLToSource(url string) *AnthropicImageSource {
	// Handle data URLs
	if strings.HasPrefix(url, "data:") {
		// Extract media type and base64 data
		parts := strings.Split(url, ",")
		if len(parts) == 2 {
			header := parts[0]
			data := parts[1]

			// Extract media type
			mediaType := "image/png" // default
			if strings.Contains(header, "image/") {
				start := strings.Index(header, "image/")
				end := strings.Index(header[start:], ";")
				if end > 0 {
					mediaType = header[start : start+end]
				}
			}

			return &AnthropicImageSource{
				Type:      "base64",
				MediaType: &mediaType,
				Data:      &data,
			}
		}
	}

	// Handle regular URLs
	return &AnthropicImageSource{
		Type: "url",
		URL:  &url,
	}
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
func convertBifrostToolToAnthropic(tool *schemas.Tool) *AnthropicTool {
	if tool == nil {
		return nil
	}

	anthropicTool := &AnthropicTool{
		Type: schemas.Ptr("function"),
	}

	// Try to extract from ResponsesAPIExtendedTool if present
	if tool.ResponsesAPIExtendedTool != nil {
		if tool.ResponsesAPIExtendedTool.Name != nil {
			anthropicTool.Name = *tool.ResponsesAPIExtendedTool.Name
		}

		if tool.ResponsesAPIExtendedTool.Description != nil {
			anthropicTool.Description = *tool.ResponsesAPIExtendedTool.Description
		}

		// Convert parameters from ToolFunction
		if tool.ResponsesAPIExtendedTool.ToolFunction != nil && tool.ResponsesAPIExtendedTool.ToolFunction.Parameters.Type != "" {
			params := tool.ResponsesAPIExtendedTool.ToolFunction.Parameters

			inputSchema := &schemas.FunctionParameters{
				Type: "object",
			}

			if params.Type != "" {
				inputSchema.Type = params.Type
			}

			if params.Properties != nil {
				inputSchema.Properties = params.Properties
			}

			if params.Required != nil {
				inputSchema.Required = params.Required
			}

			anthropicTool.InputSchema = inputSchema
		}
	}

	return anthropicTool
}

// Helper function to convert ResponsesAPIToolChoice back to AnthropicToolChoice
func convertResponsesAPIToolChoiceToAnthropic(toolChoice *schemas.ToolChoice) *AnthropicToolChoice {
	if toolChoice == nil || toolChoice.ToolChoiceStruct == nil {
		return nil
	}

	anthropicChoice := &AnthropicToolChoice{}

	if toolChoice.ToolChoiceStruct.ResponsesAPIExtendedToolChoice != nil {
		ext := toolChoice.ToolChoiceStruct.ResponsesAPIExtendedToolChoice

		if ext.Mode != nil {
			switch *ext.Mode {
			case "auto":
				anthropicChoice.Type = "auto"
			case "required":
				anthropicChoice.Type = "any"
			}
		}

		if ext.Name != nil {
			anthropicChoice.Type = "tool"
			anthropicChoice.Name = *ext.Name
		}
	}

	return anthropicChoice
}

// Helper function to convert Anthropic content blocks to ResponsesAPI output messages
func convertAnthropicContentToResponsesAPIOutput(content []AnthropicContentBlock) []schemas.ChatMessage {
	var messages []schemas.ChatMessage
	var currentMessage *schemas.ChatMessage

	for _, block := range content {
		switch block.Type {
		case "text":
			if block.Text != nil {
				// Create or update text message
				if currentMessage == nil {
					currentMessage = &schemas.ChatMessage{
						Role: schemas.ChatMessageRoleAssistant,
						Content: schemas.ChatMessageContent{
							ContentStr: block.Text,
						},
						ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
							Type: schemas.Ptr("message"),
						},
					}
				} else {
					// Append text to existing message
					if currentMessage.Content.ContentStr != nil {
						combined := *currentMessage.Content.ContentStr + *block.Text
						currentMessage.Content.ContentStr = &combined
					} else {
						currentMessage.Content.ContentStr = block.Text
					}
				}
			}

		case "thinking":
			if block.Thinking != nil {
				// Create reasoning message
				reasoningMsg := schemas.ChatMessage{
					Role: schemas.ChatMessageRoleAssistant,
					Content: schemas.ChatMessageContent{
						ContentStr: block.Thinking,
					},
					ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
						Type: schemas.Ptr("reasoning"),
					},
				}
				messages = append(messages, reasoningMsg)
			}

		case "tool_use":
			if block.ID != nil && block.Name != nil {
				// Flush current message if exists
				if currentMessage != nil {
					messages = append(messages, *currentMessage)
					currentMessage = nil
				}

				// Create function call message
				toolMsg := schemas.ChatMessage{
					Role:    schemas.ChatMessageRoleAssistant,
					Content: schemas.ChatMessageContent{},
					ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
						Type:   schemas.Ptr("function_call"),
						ID:     block.ID,
						Status: schemas.Ptr("completed"),
					},
				}
				messages = append(messages, toolMsg)
			}

		default:
			// Handle other block types if needed
		}
	}

	// Add the current message if it exists
	if currentMessage != nil {
		messages = append(messages, *currentMessage)
	}

	return messages
}

// Helper function to convert Anthropic stop reason to finish reason
func convertAnthropicStopReasonToFinishReason(stopReason string) string {
	switch stopReason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	default:
		return "stop"
	}
}

// Helper function to convert ChatMessage output to Anthropic content blocks
func convertBifrostMessagesToAnthropicContent(messages []schemas.ChatMessage) []AnthropicContentBlock {
	var contentBlocks []AnthropicContentBlock

	for _, msg := range messages {
		// Handle different message types based on ResponsesAPI structure
		if msg.ResponsesAPIExtendedBifrostMessage != nil && msg.ResponsesAPIExtendedBifrostMessage.Type != nil {
			switch *msg.ResponsesAPIExtendedBifrostMessage.Type {
			case "message":
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

			case "function_call":
				// Tool use block - need to extract from AssistantMessage.ToolCalls
				if msg.ResponsesAPIExtendedBifrostMessage.ID != nil {
					toolBlock := AnthropicContentBlock{
						Type: "tool_use",
						ID:   msg.ResponsesAPIExtendedBifrostMessage.ID,
					}

					contentBlocks = append(contentBlocks, toolBlock)
				}

			case "function_call_output":
				// Tool result block - need to extract from ToolMessage
				resultBlock := AnthropicContentBlock{
					Type: "tool_result",
				}

				// Extract result content from ToolMessage or Content
				var resultContent string
				if msg.ToolMessage != nil && msg.ToolMessage.ResponsesAPIToolMessage != nil {
					// Try to get content from the tool message structure
					if msg.Content.ContentStr != nil {
						resultContent = *msg.Content.ContentStr
					}
				} else if msg.Content.ContentStr != nil {
					resultContent = *msg.Content.ContentStr
				}

				if resultContent != "" {
					resultBlock.Content = &AnthropicContent{
						ContentStr: &resultContent,
					}
				}

				contentBlocks = append(contentBlocks, resultBlock)

			case "reasoning":
				// Thinking block (Claude 3.5 Sonnet specific)
				if msg.Content.ContentStr != nil {
					contentBlocks = append(contentBlocks, AnthropicContentBlock{
						Type:     "thinking",
						Thinking: msg.Content.ContentStr,
					})
				}

			default:
				// Handle other types as text if they have content
				if msg.Content.ContentStr != nil {
					contentBlocks = append(contentBlocks, AnthropicContentBlock{
						Type: "text",
						Text: msg.Content.ContentStr,
					})
				}
			}
		}
	}

	return contentBlocks
}

// Helper function to convert ContentBlock to AnthropicContentBlock
func convertContentBlockToAnthropic(block schemas.ContentBlock) *AnthropicContentBlock {
	switch block.Type {
	case schemas.ContentBlockTypeText:
		if block.Text != nil {
			return &AnthropicContentBlock{
				Type: "text",
				Text: block.Text,
			}
		}
	case schemas.ContentBlockTypeImage:
		// Handle image blocks - would need proper conversion
		// This is a placeholder implementation
		return &AnthropicContentBlock{
			Type: "image",
			Source: &AnthropicImageSource{
				Type: "url", // Default to URL type
			},
		}
	}
	return nil
}

// Helper function to parse tool call arguments from JSON string
func parseToolCallArguments(jsonStr string) interface{} {
	if jsonStr == "" {
		return map[string]interface{}{}
	}

	var args interface{}
	if err := json.Unmarshal([]byte(jsonStr), &args); err != nil {
		// If parsing fails, return the string as-is
		return jsonStr
	}

	return args
}
