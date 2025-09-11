package bedrock

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// ConvertFromResponsesAPIRequest converts a BifrostRequest (ResponsesAPI structure) back to BedrockConverseRequest
func ConvertFromResponsesAPIRequest(bifrostReq *schemas.BifrostRequest) (*BedrockConverseRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrostReq cannot be nil")
	}

	bedrockReq := &BedrockConverseRequest{
		ModelID: bifrostReq.Model,
	}

	// Map basic parameters to inference config
	if bifrostReq.Params != nil {
		inferenceConfig := &BedrockInferenceConfig{}

		if bifrostReq.Params.MaxTokens != nil {
			inferenceConfig.MaxTokens = bifrostReq.Params.MaxTokens
		}
		if bifrostReq.Params.Temperature != nil {
			inferenceConfig.Temperature = bifrostReq.Params.Temperature
		}
		if bifrostReq.Params.TopP != nil {
			inferenceConfig.TopP = bifrostReq.Params.TopP
		}
		if bifrostReq.Params.StopSequences != nil {
			inferenceConfig.StopSequences = bifrostReq.Params.StopSequences
		}

		bedrockReq.InferenceConfig = inferenceConfig
	}

	// TODO: Convert tools - field mapping to be resolved
	// For now, skip tool conversion to avoid import cycle issues

	// Process ChatCompletionInput (which contains the ResponsesAPI items)
	if bifrostReq.Input.ChatCompletionInput != nil {
		messages, systemMessages, err := convertResponsesAPIItemsToBedrockMessages(*bifrostReq.Input.ChatCompletionInput)
		if err != nil {
			return nil, fmt.Errorf("failed to convert messages: %w", err)
		}
		bedrockReq.Messages = messages
		if len(systemMessages) > 0 {
			bedrockReq.System = &systemMessages
		}
	}

	return bedrockReq, nil
}

// ConvertBedrockResponseToResponsesAPI converts BedrockConverseResponse to BifrostResponse (ResponsesAPI structure)
func ConvertBedrockResponseToResponsesAPI(bedrockResp *BedrockConverseResponse) (*schemas.BifrostResponse, error) {
	if bedrockResp == nil {
		return nil, fmt.Errorf("bedrockResp cannot be nil")
	}

	bifrostResp := &schemas.BifrostResponse{
		ID:                          "", // Bedrock doesn't provide response ID
		Model:                       "", // Will be set by provider
		ResponseAPIExtendedResponse: &schemas.ResponseAPIExtendedResponse{},
	}

	// Convert usage information
	usage := &schemas.LLMUsage{
		PromptTokens:     bedrockResp.Usage.InputTokens,
		CompletionTokens: bedrockResp.Usage.OutputTokens,
		TotalTokens:      bedrockResp.Usage.TotalTokens,
	}
	bifrostResp.Usage = usage

	// Convert output message to ResponsesAPI format
	if bedrockResp.Output.Message != nil {
		outputMessages := convertBedrockMessageToResponsesAPIOutput(*bedrockResp.Output.Message)
		bifrostResp.ResponseAPIExtendedResponse.Output = &outputMessages
	}

	return bifrostResp, nil
}

// Helper functions

// convertResponsesAPIItemsToBedrockMessages converts ResponsesAPI items back to Bedrock messages
func convertResponsesAPIItemsToBedrockMessages(messages []schemas.BifrostMessage) ([]BedrockMessage, []BedrockSystemMessage, error) {
	var bedrockMessages []BedrockMessage
	var systemMessages []BedrockSystemMessage

	for _, msg := range messages {
		// Handle ResponsesAPI items
		if msg.ResponsesAPIExtendedBifrostMessage != nil && msg.ResponsesAPIExtendedBifrostMessage.Type != nil {
			switch *msg.ResponsesAPIExtendedBifrostMessage.Type {
			case "message":
				// Extract role from the ResponsesAPI message structure
				role := string(msg.Role)
				if role == "system" {
					// Convert to system message
					if msg.Content.ContentStr != nil {
						systemMessages = append(systemMessages, BedrockSystemMessage{
							Text: msg.Content.ContentStr,
						})
					}
				} else {
					// Convert regular message
					bedrockMsg := BedrockMessage{
						Role: role,
					}

					// Convert content
					contentBlocks := convertBifrostContentToBedrockBlocks(msg.Content)
					bedrockMsg.Content = contentBlocks

					bedrockMessages = append(bedrockMessages, bedrockMsg)
				}

			case "function_call":
				// Handle tool calls - extract from AssistantMessage.ToolCalls
				if msg.AssistantMessage != nil &&
					msg.AssistantMessage.ChatCompletionsAssistantMessage != nil &&
					msg.AssistantMessage.ChatCompletionsAssistantMessage.ToolCalls != nil {

					for _, toolCall := range *msg.AssistantMessage.ChatCompletionsAssistantMessage.ToolCalls {
						// Create tool use content block
						var toolUseID string
						if toolCall.ID != nil {
							toolUseID = *toolCall.ID
						}

						toolUseBlock := BedrockContentBlock{
							ToolUse: &BedrockToolUse{
								ToolUseID: toolUseID,
								Name:      *toolCall.Function.Name,
								Input:     parseToolCallArguments(toolCall.Function.Arguments),
							},
						}

						// Create assistant message with tool use
						assistantMsg := BedrockMessage{
							Role:    "assistant",
							Content: []BedrockContentBlock{toolUseBlock},
						}
						bedrockMessages = append(bedrockMessages, assistantMsg)
					}
				}

			case "function_call_output":
				// Handle tool results
				if msg.ToolMessage != nil && msg.ToolMessage.ChatCompletionsToolMessage != nil {
					var toolUseID string
					if msg.ToolMessage.ChatCompletionsToolMessage.ToolCallID != nil {
						toolUseID = *msg.ToolMessage.ChatCompletionsToolMessage.ToolCallID
					}

					toolResultBlock := BedrockContentBlock{
						ToolResult: &BedrockToolResult{
							ToolUseID: toolUseID,
						},
					}

					// Set content based on available data
					if msg.Content.ContentStr != nil {
						toolResultBlock.ToolResult.Content = []BedrockToolResultContent{
							{Text: msg.Content.ContentStr},
						}
					}

					// Create user message with tool result
					userMsg := BedrockMessage{
						Role:    "user",
						Content: []BedrockContentBlock{toolResultBlock},
					}
					bedrockMessages = append(bedrockMessages, userMsg)
				}
			}
		} else {
			// Handle regular BifrostMessage format
			role := string(msg.Role)

			if role == "system" {
				// Convert to system message
				if msg.Content.ContentStr != nil {
					systemMessages = append(systemMessages, BedrockSystemMessage{
						Text: msg.Content.ContentStr,
					})
				}
			} else {
				bedrockMsg := BedrockMessage{
					Role: role,
				}

				// Convert content
				contentBlocks := convertBifrostContentToBedrockBlocks(msg.Content)
				bedrockMsg.Content = contentBlocks

				// Handle tool calls for assistant messages
				if msg.Role == schemas.ModelChatMessageRoleAssistant &&
					msg.AssistantMessage != nil &&
					msg.AssistantMessage.ChatCompletionsAssistantMessage != nil &&
					msg.AssistantMessage.ChatCompletionsAssistantMessage.ToolCalls != nil {

					for _, toolCall := range *msg.AssistantMessage.ChatCompletionsAssistantMessage.ToolCalls {
						var toolUseID string
						if toolCall.ID != nil {
							toolUseID = *toolCall.ID
						}

						toolUseBlock := BedrockContentBlock{
							ToolUse: &BedrockToolUse{
								ToolUseID: toolUseID,
								Name:      *toolCall.Function.Name,
								Input:     parseToolCallArguments(toolCall.Function.Arguments),
							},
						}
						bedrockMsg.Content = append(bedrockMsg.Content, toolUseBlock)
					}
				}

				// Handle tool results
				if msg.Role == schemas.ModelChatMessageRoleTool &&
					msg.ToolMessage != nil &&
					msg.ToolMessage.ChatCompletionsToolMessage != nil {

					var toolUseID string
					if msg.ToolMessage.ChatCompletionsToolMessage.ToolCallID != nil {
						toolUseID = *msg.ToolMessage.ChatCompletionsToolMessage.ToolCallID
					}

					toolResultBlock := BedrockContentBlock{
						ToolResult: &BedrockToolResult{
							ToolUseID: toolUseID,
						},
					}

					if msg.Content.ContentStr != nil {
						toolResultBlock.ToolResult.Content = []BedrockToolResultContent{
							{Text: msg.Content.ContentStr},
						}
					}

					bedrockMsg.Content = append(bedrockMsg.Content, toolResultBlock)
				}

				bedrockMessages = append(bedrockMessages, bedrockMsg)
			}
		}
	}

	return bedrockMessages, systemMessages, nil
}

// convertBifrostContentToBedrockBlocks converts Bifrost content to Bedrock content blocks
func convertBifrostContentToBedrockBlocks(content schemas.MessageContent) []BedrockContentBlock {
	var blocks []BedrockContentBlock

	if content.ContentStr != nil {
		blocks = append(blocks, BedrockContentBlock{
			Text: content.ContentStr,
		})
	} else if content.ContentBlocks != nil {
		for _, block := range *content.ContentBlocks {
			bedrockBlock := convertBifrostContentBlockToBedrock(block)
			blocks = append(blocks, bedrockBlock)
		}
	}

	return blocks
}

// convertBifrostContentBlockToBedrock converts schemas.ContentBlock to BedrockContentBlock
func convertBifrostContentBlockToBedrock(block schemas.ContentBlock) BedrockContentBlock {
	switch block.Type {
	case schemas.ContentBlockTypeText:
		return BedrockContentBlock{
			Text: block.Text,
		}
	case schemas.ContentBlockTypeImage:
		if block.ImageURL != nil {
			return BedrockContentBlock{
				Image: &BedrockImageSource{
					Format: "png", // Default format
					Source: BedrockImageSourceData{
						Bytes: block.ImageURL, // Simplified - may need base64 decoding
					},
				},
			}
		}
		// Fallback to text if image data is missing
		return BedrockContentBlock{
			Text: schemas.Ptr("Image content"),
		}
	default:
		// Fallback to text block
		blockType := string(block.Type)
		return BedrockContentBlock{
			Text: &blockType,
		}
	}
}

// convertBedrockMessageToResponsesAPIOutput converts Bedrock message to BifrostMessage output format
func convertBedrockMessageToResponsesAPIOutput(bedrockMsg BedrockMessage) []schemas.BifrostMessage {
	var outputMessages []schemas.BifrostMessage

	for _, block := range bedrockMsg.Content {
		if block.Text != nil {
			// Text content
			outputMessages = append(outputMessages, schemas.BifrostMessage{
				Role: schemas.ModelChatMessageRoleAssistant,
				Content: schemas.MessageContent{
					ContentStr: block.Text,
				},
				ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
					Type: schemas.Ptr("message"),
				},
			})
		} else if block.ToolUse != nil {
			// Tool use content
			toolMsg := schemas.BifrostMessage{
				Role:    schemas.ModelChatMessageRoleAssistant,
				Content: schemas.MessageContent{},
				ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
					Type:   schemas.Ptr("function_call"),
					ID:     &block.ToolUse.ToolUseID,
					Status: schemas.Ptr("completed"),
				},
			}
			outputMessages = append(outputMessages, toolMsg)
		} else if block.ToolResult != nil {
			// Tool result content - typically not in assistant output but handled for completeness
			var resultContent string
			if len(block.ToolResult.Content) > 0 && block.ToolResult.Content[0].Text != nil {
				resultContent = *block.ToolResult.Content[0].Text
			}

			resultMsg := schemas.BifrostMessage{
				Role: schemas.ModelChatMessageRoleTool,
				Content: schemas.MessageContent{
					ContentStr: &resultContent,
				},
				ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
					Type: schemas.Ptr("function_call_output"),
				},
			}
			outputMessages = append(outputMessages, resultMsg)
		}
	}

	return outputMessages
}

// parseToolCallArguments parses JSON string arguments back to interface{}
func parseToolCallArguments(args string) map[string]interface{} {
	if args == "" {
		return nil
	}

	// Simple parsing - in production would use proper JSON unmarshaling
	result := make(map[string]interface{})
	// For now, just return the string as-is in a map
	result["arguments"] = args
	return result
}
