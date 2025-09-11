package bedrock

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBedrockResponsesAPIRequest converts a BifrostRequest (ResponsesAPI structure) back to BedrockConverseRequest
func ToBedrockResponsesAPIRequest(bifrostReq *schemas.BifrostRequest) (*BedrockConverseRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost request is nil")
	}

	bedrockReq := &BedrockConverseRequest{
		ModelID: bifrostReq.Model,
	}

	// map bifrost messages to bedrock messages
	if bifrostReq.Input.ResponsesInput != nil {
		messages, systemMessages, err := convertResponsesAPIItemsToBedrockMessages(*bifrostReq.Input.ResponsesInput)
		if err != nil {
			return nil, fmt.Errorf("failed to convert ResponsesAPI messages: %w", err)
		}
		bedrockReq.Messages = messages
		if len(systemMessages) > 0 {
			bedrockReq.System = &systemMessages
		}
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

	// Convert tools
	if bifrostReq.Params.Tools != nil {
		var bedrockTools []BedrockTool
		for _, tool := range *bifrostReq.Params.Tools {
			if tool.ToolFunction != nil {
				bedrockTool := BedrockTool{
					ToolSpec: &BedrockToolSpec{
						Name:        *tool.Name,
						Description: tool.Description,
						InputSchema: BedrockToolInputSchema{
							JSON: tool.ToolFunction.Parameters,
						},
					},
				}
				bedrockTools = append(bedrockTools, bedrockTool)
			}
		}

		if len(bedrockTools) > 0 {
			bedrockReq.ToolConfig = &BedrockToolConfig{
				Tools: &bedrockTools,
			}
		}
	}

	// Convert tool choice
	if bifrostReq.Params.ToolChoice != nil {
		bedrockToolChoice := convertToolChoice(*bifrostReq.Params.ToolChoice)
		if bedrockToolChoice != nil {
			bedrockReq.ToolConfig.ToolChoice = bedrockToolChoice
		}
	}

	return bedrockReq, nil
}

// ToBedrockResponsesAPIResponse converts BedrockConverseResponse to BifrostResponse (ResponsesAPI structure)
func (bedrockResp *BedrockConverseResponse) ToResponsesAPIBifrostResponse() (*schemas.BifrostResponse, error) {
	if bedrockResp == nil {
		return nil, fmt.Errorf("bedrock response is nil")
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
		bifrostResp.ResponseAPIExtendedResponse.Output = outputMessages
	}

	return bifrostResp, nil
}

// Helper functions

// convertResponsesAPIItemsToBedrockMessages converts ResponsesAPI items back to Bedrock messages
func convertResponsesAPIItemsToBedrockMessages(messages []schemas.ChatMessage) ([]BedrockMessage, []BedrockSystemMessage, error) {
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
				// Handle function calls from ResponsesAPI
				if msg.AssistantMessage != nil && msg.AssistantMessage.ResponsesAPIExtendedAssistantMessage != nil {
					if msg.AssistantMessage.ResponsesAPIExtendedAssistantMessage.FunctionToolCall != nil {
						// Create tool use content block
						var toolUseID string
						if msg.ResponsesAPIExtendedBifrostMessage.ID != nil {
							toolUseID = *msg.ResponsesAPIExtendedBifrostMessage.ID
						}

						// Get function name from ToolMessage
						var functionName string
						if msg.ToolMessage != nil && msg.ToolMessage.ResponsesAPIToolMessage != nil && msg.Name != nil {
							functionName = *msg.Name
						}

						toolUseBlock := BedrockContentBlock{
							ToolUse: &BedrockToolUse{
								ToolUseID: toolUseID,
								Name:      functionName,
								Input:     parseToolCallArguments(*msg.ResponsesAPIExtendedBifrostMessage.Arguments),
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
				// Handle function call outputs from ResponsesAPI
				if msg.ToolMessage != nil && msg.ToolMessage.ResponsesAPIToolMessage != nil {
					var toolUseID string
					if msg.ToolMessage.ResponsesAPIToolMessage.CallID != nil {
						toolUseID = *msg.ToolMessage.ResponsesAPIToolMessage.CallID
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
					} else if msg.ToolMessage.ResponsesAPIToolMessage.FunctionToolCallOutput != nil {
						toolResultBlock.ToolResult.Content = []BedrockToolResultContent{
							{Text: &msg.ToolMessage.ResponsesAPIToolMessage.FunctionToolCallOutput.Output},
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
		}
	}

	return bedrockMessages, systemMessages, nil
}

// convertBifrostContentToBedrockBlocks converts Bifrost content to Bedrock content blocks
func convertBifrostContentToBedrockBlocks(content schemas.ChatMessageContent) []BedrockContentBlock {
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
						Bytes: &block.ImageURL.URL, // Simplified - may need base64 decoding
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

// convertBedrockMessageToResponsesAPIOutput converts Bedrock message to ChatMessage output format
func convertBedrockMessageToResponsesAPIOutput(bedrockMsg BedrockMessage) []schemas.ChatMessage {
	var outputMessages []schemas.ChatMessage

	for _, block := range bedrockMsg.Content {
		if block.Text != nil {
			// Text content
			outputMessages = append(outputMessages, schemas.ChatMessage{
				Role: schemas.ChatMessageRoleAssistant,
				Content: schemas.ChatMessageContent{
					ContentStr: block.Text,
				},
				ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
					Type: schemas.Ptr("message"),
				},
			})
		} else if block.ToolUse != nil {
			// Tool use content
			toolMsg := schemas.ChatMessage{
				Name:    &block.ToolUse.Name,
				Role:    schemas.ChatMessageRoleAssistant,
				Content: schemas.ChatMessageContent{},
				ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
					Type:      schemas.Ptr("function_call"),
					ID:        &block.ToolUse.ToolUseID,
					Status:    schemas.Ptr("completed"),
					Arguments: schemas.Ptr(convertInterfaceToString(block.ToolUse.Input)),
				},
				ToolMessage: &schemas.ToolMessage{
					ResponsesAPIToolMessage: &schemas.ResponsesAPIToolMessage{
						CallID: &block.ToolUse.ToolUseID,
					},
				},
			}
			outputMessages = append(outputMessages, toolMsg)
		} else if block.ToolResult != nil {
			// Tool result content - typically not in assistant output but handled for completeness
			var resultContent string
			if len(block.ToolResult.Content) > 0 && block.ToolResult.Content[0].Text != nil {
				resultContent = *block.ToolResult.Content[0].Text
			}

			resultMsg := schemas.ChatMessage{
				Role: schemas.ModelChatMessageRoleTool,
				Content: schemas.ChatMessageContent{
					ContentStr: &resultContent,
				},
				ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
					Type: schemas.Ptr("function_call_output"),
				},
				ToolMessage: &schemas.ToolMessage{
					ResponsesAPIToolMessage: &schemas.ResponsesAPIToolMessage{
						CallID: &block.ToolResult.ToolUseID,
						FunctionToolCallOutput: &schemas.FunctionToolCallOutput{
							Output: resultContent,
						},
					},
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
	// unmarshal the arguments
	var result map[string]interface{}
	err := sonic.Unmarshal([]byte(args), &result)
	if err != nil {
		return nil
	}
	return result
}

// convertInterfaceToString converts interface{} to string for tool arguments
func convertInterfaceToString(input interface{}) string {
	if input == nil {
		return ""
	}

	// Try to marshal to JSON string
	if jsonBytes, err := sonic.Marshal(input); err == nil {
		return string(jsonBytes)
	}

	// Fallback to string conversion
	return fmt.Sprintf("%v", input)
}
