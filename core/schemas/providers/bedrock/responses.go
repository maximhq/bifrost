package bedrock

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToBedrockResponsesRequest converts a BifrostRequest (Responses structure) back to BedrockConverseRequest
func ToBedrockResponsesRequest(bifrostReq *schemas.BifrostResponsesRequest) (*BedrockConverseRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost request is nil")
	}

	bedrockReq := &BedrockConverseRequest{
		ModelID: bifrostReq.Model,
	}

	// map bifrost messages to bedrock messages
	if bifrostReq.Input != nil {
		messages, systemMessages, err := convertResponsesItemsToBedrockMessages(bifrostReq.Input)
		if err != nil {
			return nil, fmt.Errorf("failed to convert Responses messages: %w", err)
		}
		bedrockReq.Messages = messages
		if len(systemMessages) > 0 {
			bedrockReq.System = &systemMessages
		}
	}

	// Map basic parameters to inference config
	if bifrostReq.Params != nil {
		inferenceConfig := &BedrockInferenceConfig{}

		if bifrostReq.Params.MaxOutputTokens != nil {
			inferenceConfig.MaxTokens = bifrostReq.Params.MaxOutputTokens
		}
		if bifrostReq.Params.Temperature != nil {
			inferenceConfig.Temperature = bifrostReq.Params.Temperature
		}
		if bifrostReq.Params.TopP != nil {
			inferenceConfig.TopP = bifrostReq.Params.TopP
		}
		if bifrostReq.Params.ExtraParams != nil {
			if stop, ok := bifrostReq.Params.ExtraParams["stop"].([]string); ok {
				inferenceConfig.StopSequences = &stop
			}
		}

		bedrockReq.InferenceConfig = inferenceConfig
	}

	// Convert tools
	if bifrostReq.Params.Tools != nil {
		var bedrockTools []BedrockTool
		for _, tool := range bifrostReq.Params.Tools {
			if tool.ResponsesToolFunction != nil {
				bedrockTool := BedrockTool{
					ToolSpec: &BedrockToolSpec{
						Name:        *tool.Name,
						Description: tool.Description,
						InputSchema: BedrockToolInputSchema{
							JSON: tool.ResponsesToolFunction.Parameters,
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
		bedrockToolChoice := convertResponsesToolChoice(*bifrostReq.Params.ToolChoice)
		if bedrockToolChoice != nil {
			bedrockReq.ToolConfig.ToolChoice = bedrockToolChoice
		}
	}

	return bedrockReq, nil
}

// ToBedrockResponsesResponse converts BedrockConverseResponse to BifrostResponse (Responses structure)
func (bedrockResp *BedrockConverseResponse) ToResponsesBifrostResponse() (*schemas.BifrostResponse, error) {
	if bedrockResp == nil {
		return nil, fmt.Errorf("bedrock response is nil")
	}

	bifrostResp := &schemas.BifrostResponse{
		ID:    "", // Bedrock doesn't provide response ID
		Model: "", // Will be set by provider
	}

	// Convert usage information
	usage := &schemas.LLMUsage{
		ResponsesExtendedResponseUsage: &schemas.ResponsesExtendedResponseUsage{
			InputTokens:  bedrockResp.Usage.InputTokens,
			OutputTokens: bedrockResp.Usage.OutputTokens,
		},
		TotalTokens: bedrockResp.Usage.TotalTokens,
	}
	bifrostResp.Usage = usage

	// Convert output message to Responses format
	if bedrockResp.Output.Message != nil {
		outputMessages := convertBedrockMessageToResponsesMessages(*bedrockResp.Output.Message)
		bifrostResp.ResponsesResponse.Output = outputMessages
	}

	return bifrostResp, nil
}

// Helper functions

func convertResponsesToolChoice(toolChoice schemas.ResponsesToolChoice) *BedrockToolChoice {
	// Check if it's a string choice
	if toolChoice.ResponsesToolChoiceStr != nil {
		switch schemas.ResponsesToolChoiceType(*toolChoice.ResponsesToolChoiceStr) {
		case schemas.ResponsesToolChoiceTypeFunction:
			return &BedrockToolChoice{
				Auto: &BedrockToolChoiceAuto{},
			}
		case schemas.ResponsesToolChoiceTypeAny, schemas.ResponsesToolChoiceTypeRequired:
			return &BedrockToolChoice{
				Any: &BedrockToolChoiceAny{},
			}
		case schemas.ResponsesToolChoiceTypeNone:
			// Bedrock doesn't have explicit "none" - just don't include tools
			return nil
		}
	}

	return nil
}

// convertResponsesItemsToBedrockMessages converts Responses items back to Bedrock messages
func convertResponsesItemsToBedrockMessages(messages []schemas.ResponsesMessage) ([]BedrockMessage, []BedrockSystemMessage, error) {
	var bedrockMessages []BedrockMessage
	var systemMessages []BedrockSystemMessage

	for _, msg := range messages {
		// Handle Responses items
		if msg.Type != nil {
			switch *msg.Type {
			case "message":
				// Extract role from the Responses message structure
				role := string(*msg.Role)
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
						Role: BedrockMessageRole(role),
					}

					// Convert content
					contentBlocks := convertBifrostResponsesMessageContentBlocksToBedrockContentBlocks(*msg.Content)
					bedrockMsg.Content = contentBlocks

					bedrockMessages = append(bedrockMessages, bedrockMsg)
				}

			case "function_call":
				// Handle function calls from Responses
				if msg.ResponsesToolMessage != nil {
					// Create tool use content block
					var toolUseID string
					if msg.ResponsesToolMessage.CallID != nil {
						toolUseID = *msg.ResponsesToolMessage.CallID
					}

					// Get function name from ToolMessage
					var functionName string
					if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Name != nil {
						functionName = *msg.Name
					}

					toolUseBlock := BedrockContentBlock{
						ToolUse: &BedrockToolUse{
							ToolUseID: toolUseID,
							Name:      functionName,
							Input:     *msg.ResponsesToolMessage.Arguments,
						},
					}

					// Create assistant message with tool use
					assistantMsg := BedrockMessage{
						Role:    BedrockMessageRoleAssistant,
						Content: []BedrockContentBlock{toolUseBlock},
					}
					bedrockMessages = append(bedrockMessages, assistantMsg)

				}

			case "function_call_output":
				// Handle function call outputs from Responses
				if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput != nil {
					var toolUseID string
					if msg.ResponsesToolMessage.CallID != nil {
						toolUseID = *msg.ResponsesToolMessage.CallID
					}

					toolResultBlock := BedrockContentBlock{
						ToolResult: &BedrockToolResult{
							ToolUseID: toolUseID,
						},
					}

					// Set content based on available data
					if msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputStr != nil {
						toolResultBlock.ToolResult.Content = []BedrockContentBlock{
							{JSON: msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputStr},
						}
					} else if msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputBlocks != nil {
						toolResultBlock.ToolResult.Content = convertBifrostResponsesMessageContentBlocksToBedrockContentBlocks(schemas.ResponsesMessageContent{
							ContentBlocks: msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputBlocks,
						})
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

// convertBifrostResponsesMessageContentBlocksToBedrockContentBlocks converts Bifrost content to Bedrock content blocks
func convertBifrostResponsesMessageContentBlocksToBedrockContentBlocks(content schemas.ResponsesMessageContent) []BedrockContentBlock {
	var blocks []BedrockContentBlock

	if content.ContentStr != nil {
		blocks = append(blocks, BedrockContentBlock{
			Text: content.ContentStr,
		})
	} else if content.ContentBlocks != nil {
		for _, block := range *content.ContentBlocks {

			bedrockBlock := BedrockContentBlock{}

			switch block.Type {
			case schemas.ResponsesInputMessageContentBlockTypeText:
				bedrockBlock.Text = block.Text
			case schemas.ResponsesInputMessageContentBlockTypeImage:
				if block.ResponsesInputMessageContentBlockImage != nil {
					bedrockBlock.Image = &BedrockImageSource{
						Format: "png", // Default format
						Source: BedrockImageSourceData{
							Bytes: block.ImageURL, // Simplified - may need base64 decoding
						},
					}
				}
			default:
				// Don't add anything
			}

			blocks = append(blocks, bedrockBlock)
		}
	}

	return blocks
}

// convertBedrockMessageToResponsesMessages converts Bedrock message to ChatMessage output format
func convertBedrockMessageToResponsesMessages(bedrockMsg BedrockMessage) []schemas.ResponsesMessage {
	var outputMessages []schemas.ResponsesMessage

	for _, block := range bedrockMsg.Content {
		if block.Text != nil {
			// Text content
			outputMessages = append(outputMessages, schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: block.Text,
				},
			})
		} else if block.ToolUse != nil {
			// Tool use content
			toolMsg := schemas.ResponsesMessage{
				Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    &block.ToolUse.ToolUseID,
					Name:      &block.ToolUse.Name,
					Arguments: schemas.Ptr(schemas.JsonifyInput(block.ToolUse.Input)),
				},
			}
			outputMessages = append(outputMessages, toolMsg)
		} else if block.ToolResult != nil {
			// Tool result content - typically not in assistant output but handled for completeness
			var resultContent string
			if len(block.ToolResult.Content) > 0 && block.ToolResult.Content[0].Text != nil {
				resultContent = *block.ToolResult.Content[0].Text
			}

			resultMsg := schemas.ResponsesMessage{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &resultContent,
				},
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: &block.ToolResult.ToolUseID,
					ResponsesFunctionToolCallOutput: &schemas.ResponsesFunctionToolCallOutput{
						ResponsesFunctionToolCallOutputStr: &resultContent,
					},
				},
			}
			outputMessages = append(outputMessages, resultMsg)
		}
	}

	return outputMessages
}
