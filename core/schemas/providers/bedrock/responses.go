package bedrock

import (
	"encoding/json"
	"fmt"
	"time"

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
			bedrockReq.System = systemMessages
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
			if stop, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["stop"]); ok {
				inferenceConfig.StopSequences = stop
			}
		}

		bedrockReq.InferenceConfig = inferenceConfig
	}

	// Convert tools
	if bifrostReq.Params != nil && bifrostReq.Params.Tools != nil {
		var bedrockTools []BedrockTool
		for _, tool := range bifrostReq.Params.Tools {
			if tool.ResponsesToolFunction != nil {
				// Create the complete schema object that Bedrock expects
				var schemaObject interface{}
				if tool.ResponsesToolFunction.Parameters != nil {
					schemaObject = tool.ResponsesToolFunction.Parameters
				} else {
					// Fallback to empty object schema if no parameters
					schemaObject = map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{},
					}
				}

				if tool.Name == nil || *tool.Name == "" {
					return nil, fmt.Errorf("responses tool is missing required name for Bedrock function conversion")
				}
				name := *tool.Name

				// Use the tool description if available, otherwise use a generic description
				description := "Function tool"
				if tool.Description != nil {
					description = *tool.Description
				}

				bedrockTool := BedrockTool{
					ToolSpec: &BedrockToolSpec{
						Name:        name,
						Description: &description,
						InputSchema: BedrockToolInputSchema{
							JSON: schemaObject,
						},
					},
				}
				bedrockTools = append(bedrockTools, bedrockTool)
			}
		}

		if len(bedrockTools) > 0 {
			bedrockReq.ToolConfig = &BedrockToolConfig{
				Tools: bedrockTools,
			}
		}
	}

	// Convert tool choice
	if bifrostReq.Params != nil && bifrostReq.Params.ToolChoice != nil {
		bedrockToolChoice := convertResponsesToolChoice(*bifrostReq.Params.ToolChoice)
		if bedrockToolChoice != nil {
			if bedrockReq.ToolConfig == nil {
				bedrockReq.ToolConfig = &BedrockToolConfig{}
			}
			bedrockReq.ToolConfig.ToolChoice = bedrockToolChoice
		}
	}

	// Ensure tool config is present when tool content exists (similar to Chat Completions)
	ensureResponsesToolConfigForConversation(bifrostReq, bedrockReq)

	return bedrockReq, nil
}

// ensureResponsesToolConfigForConversation ensures toolConfig is present when tool content exists
func ensureResponsesToolConfigForConversation(bifrostReq *schemas.BifrostResponsesRequest, bedrockReq *BedrockConverseRequest) {
	if bedrockReq.ToolConfig != nil {
		return // Already has tool config
	}

	hasToolContent, tools := extractToolsFromResponsesConversationHistory(bifrostReq.Input)
	if hasToolContent && len(tools) > 0 {
		bedrockReq.ToolConfig = &BedrockToolConfig{Tools: tools}
	}
}

// extractToolsFromResponsesConversationHistory extracts tools from Responses conversation history
func extractToolsFromResponsesConversationHistory(messages []schemas.ResponsesMessage) (bool, []BedrockTool) {
	var hasToolContent bool
	toolMap := make(map[string]*schemas.ResponsesTool) // Use map to deduplicate by name

	for _, msg := range messages {
		// Check if message contains tool use or tool result
		if msg.Type != nil {
			switch *msg.Type {
			case schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesMessageTypeFunctionCallOutput:
				hasToolContent = true
				// Try to infer tool definition from tool call/result
				if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Name != nil {
					toolName := *msg.ResponsesToolMessage.Name
					if _, exists := toolMap[toolName]; !exists {
						// Create a minimal tool definition
						toolMap[toolName] = &schemas.ResponsesTool{
							Type: "function",
							Name: &toolName,
							ResponsesToolFunction: &schemas.ResponsesToolFunction{
								Parameters: &schemas.ToolFunctionParameters{
									Type:       "object",
									Properties: make(map[string]interface{}),
								},
							},
						}
					}
				}
			}
		}
	}

	// Convert map to slice
	var tools []BedrockTool
	for _, tool := range toolMap {
		if tool.Name != nil && tool.ResponsesToolFunction != nil {
			schemaObject := tool.ResponsesToolFunction.Parameters
			if schemaObject == nil {
				schemaObject = &schemas.ToolFunctionParameters{
					Type:       "object",
					Properties: make(map[string]interface{}),
				}
			}

			description := "Function tool"
			if tool.Description != nil {
				description = *tool.Description
			}

			bedrockTool := BedrockTool{
				ToolSpec: &BedrockToolSpec{
					Name:        *tool.Name,
					Description: &description,
					InputSchema: BedrockToolInputSchema{
						JSON: schemaObject,
					},
				},
			}
			tools = append(tools, bedrockTool)
		}
	}

	return hasToolContent, tools
}

// ToBedrockResponsesResponse converts BedrockConverseResponse to BifrostResponse (Responses structure)
func (bedrockResp *BedrockConverseResponse) ToResponsesBifrostResponse() (*schemas.BifrostResponse, error) {
	if bedrockResp == nil {
		return nil, fmt.Errorf("bedrock response is nil")
	}

	bifrostResp := &schemas.BifrostResponse{
		ID:     "", // Bedrock doesn't provide response ID
		Model:  "", // Will be set by provider
		Object: "response",
		ResponsesResponse: &schemas.ResponsesResponse{
			CreatedAt: int(time.Now().Unix()),
		},
	}

	// Convert usage information
	usage := &schemas.LLMUsage{
		ResponsesExtendedResponseUsage: &schemas.ResponsesExtendedResponseUsage{},
	}

	// Handle potential nil Usage field
	if bedrockResp.Usage != nil {
		usage.ResponsesExtendedResponseUsage.InputTokens = bedrockResp.Usage.InputTokens
		usage.ResponsesExtendedResponseUsage.OutputTokens = bedrockResp.Usage.OutputTokens
		usage.TotalTokens = bedrockResp.Usage.TotalTokens
	}
	bifrostResp.Usage = usage

	// Convert output message to Responses format
	if bedrockResp.Output != nil && bedrockResp.Output.Message != nil {
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
		case schemas.ResponsesToolChoiceTypeAny, schemas.ResponsesToolChoiceTypeRequired:
			return &BedrockToolChoice{
				Any: &BedrockToolChoiceAny{},
			}
		case schemas.ResponsesToolChoiceTypeNone:
			// Bedrock doesn't have explicit "none" - just don't include tools
			return nil
		}
	}

	// Check if it's a struct choice
	if toolChoice.ResponsesToolChoiceStruct != nil {
		switch toolChoice.ResponsesToolChoiceStruct.Type {
		case schemas.ResponsesToolChoiceTypeFunction:
			// Extract the actual function name from the struct
			if toolChoice.ResponsesToolChoiceStruct.Name != nil && *toolChoice.ResponsesToolChoiceStruct.Name != "" {
				return &BedrockToolChoice{
					Tool: &BedrockToolChoiceTool{
						Name: *toolChoice.ResponsesToolChoiceStruct.Name,
					},
				}
			}
			// If Name is nil or empty, return nil as we can't construct a valid tool choice
			return nil
		case schemas.ResponsesToolChoiceTypeAuto, schemas.ResponsesToolChoiceTypeAny, schemas.ResponsesToolChoiceTypeRequired:
			return &BedrockToolChoice{
				Any: &BedrockToolChoiceAny{},
			}
		case schemas.ResponsesToolChoiceTypeNone:
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
		msgType := schemas.ResponsesMessageTypeMessage
		if msg.Type != nil {
			msgType = *msg.Type
		}
		switch msgType {
		case schemas.ResponsesMessageTypeMessage:
			// Check if Role is present, skip message if not
			if msg.Role == nil {
				continue
			}

			// Extract role from the Responses message structure
			role := *msg.Role

			if role == schemas.ResponsesInputMessageRoleSystem {
				// Convert to system message
				// Ensure Content and ContentStr are present
				if msg.Content != nil {
					if msg.Content.ContentStr != nil {
						systemMessages = append(systemMessages, BedrockSystemMessage{
							Text: msg.Content.ContentStr,
						})
					} else if msg.Content.ContentBlocks != nil {
						for _, block := range msg.Content.ContentBlocks {
							if block.Text != nil {
								systemMessages = append(systemMessages, BedrockSystemMessage{
									Text: block.Text,
								})
							}
						}
					}
				}
				// Skip system messages with no content
			} else {
				// Convert regular message
				// Ensure Content is present
				if msg.Content == nil {
					// Skip messages without content or create with empty content
					continue
				}

				bedrockMsg := BedrockMessage{
					Role: BedrockMessageRole(role),
				}

				// Convert content
				contentBlocks, err := convertBifrostResponsesMessageContentBlocksToBedrockContentBlocks(*msg.Content)
				if err != nil {
					return nil, nil, fmt.Errorf("failed to convert content blocks: %w", err)
				}
				bedrockMsg.Content = contentBlocks

				bedrockMessages = append(bedrockMessages, bedrockMsg)
			}

		case schemas.ResponsesMessageTypeFunctionCall:
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
					functionName = *msg.ResponsesToolMessage.Name
				}

				// Parse JSON arguments into interface{}
				var input interface{} = map[string]interface{}{}
				if msg.ResponsesToolMessage.Arguments != nil {
					var parsedInput interface{}
					if err := json.Unmarshal([]byte(*msg.ResponsesToolMessage.Arguments), &parsedInput); err != nil {
						return nil, nil, fmt.Errorf("failed to parse tool arguments JSON: %w", err)
					}
					input = parsedInput
				}

				toolUseBlock := BedrockContentBlock{
					ToolUse: &BedrockToolUse{
						ToolUseID: toolUseID,
						Name:      functionName,
						Input:     input,
					},
				}

				// Create assistant message with tool use
				assistantMsg := BedrockMessage{
					Role:    BedrockMessageRoleAssistant,
					Content: []BedrockContentBlock{toolUseBlock},
				}
				bedrockMessages = append(bedrockMessages, assistantMsg)

			}

		case schemas.ResponsesMessageTypeFunctionCallOutput:
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
					raw := *msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputStr
					var parsed interface{}
					if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
						toolResultBlock.ToolResult.Content = []BedrockContentBlock{
							{JSON: parsed},
						}
					} else {
						toolResultBlock.ToolResult.Content = []BedrockContentBlock{
							{Text: &raw},
						}
					}
				} else if msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputBlocks != nil {
					toolResultContent, err := convertBifrostResponsesMessageContentBlocksToBedrockContentBlocks(schemas.ResponsesMessageContent{
						ContentBlocks: msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputBlocks,
					})
					if err != nil {
						return nil, nil, fmt.Errorf("failed to convert tool result content blocks: %w", err)
					}
					toolResultBlock.ToolResult.Content = toolResultContent
				}

				// Create user message with tool result
				userMsg := BedrockMessage{
					Role:    BedrockMessageRoleUser,
					Content: []BedrockContentBlock{toolResultBlock},
				}
				bedrockMessages = append(bedrockMessages, userMsg)
			}
		}
	}

	return bedrockMessages, systemMessages, nil
}

// convertBifrostResponsesMessageContentBlocksToBedrockContentBlocks converts Bifrost content to Bedrock content blocks
func convertBifrostResponsesMessageContentBlocksToBedrockContentBlocks(content schemas.ResponsesMessageContent) ([]BedrockContentBlock, error) {
	var blocks []BedrockContentBlock

	if content.ContentStr != nil {
		blocks = append(blocks, BedrockContentBlock{
			Text: content.ContentStr,
		})
	} else if content.ContentBlocks != nil {
		for _, block := range content.ContentBlocks {

			bedrockBlock := BedrockContentBlock{}

			switch block.Type {
			case schemas.ResponsesInputMessageContentBlockTypeText:
				bedrockBlock.Text = block.Text
			case schemas.ResponsesInputMessageContentBlockTypeImage:
				if block.ResponsesInputMessageContentBlockImage != nil && block.ResponsesInputMessageContentBlockImage.ImageURL != nil {
					imageSource, err := convertImageToBedrockSource(*block.ResponsesInputMessageContentBlockImage.ImageURL)
					if err != nil {
						return nil, fmt.Errorf("failed to convert image in responses content block: %w", err)
					}
					bedrockBlock.Image = imageSource
				}
			default:
				// Don't add anything
			}

			blocks = append(blocks, bedrockBlock)
		}
	}

	return blocks, nil
}

// convertBedrockMessageToResponsesMessages converts Bedrock message to ChatMessage output format
func convertBedrockMessageToResponsesMessages(bedrockMsg BedrockMessage) []schemas.ResponsesMessage {
	var outputMessages []schemas.ResponsesMessage

	for _, block := range bedrockMsg.Content {
		if block.Text != nil {
			// Text content
			outputMessages = append(outputMessages, schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: block.Text,
				},
			})
		} else if block.ToolUse != nil {
			// Tool use content
			// Create copies of the values to avoid range loop variable capture
			toolUseID := block.ToolUse.ToolUseID
			toolUseName := block.ToolUse.Name

			toolMsg := schemas.ResponsesMessage{
				Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    &toolUseID,
					Name:      &toolUseName,
					Arguments: schemas.Ptr(schemas.JsonifyInput(block.ToolUse.Input)),
				},
			}
			outputMessages = append(outputMessages, toolMsg)
		} else if block.ToolResult != nil {
			// Tool result content - typically not in assistant output but handled for completeness
			// Prefer JSON payloads without unmarshalling; fallback to text
			var resultContent string
			if len(block.ToolResult.Content) > 0 {
				// JSON first (no unmarshal; just one marshal to string when present)
				for _, c := range block.ToolResult.Content {
					if c.JSON != nil {
						resultContent = schemas.JsonifyInput(c.JSON)
						break
					}
				}
				// Fallback to first available text block
				if resultContent == "" {
					for _, c := range block.ToolResult.Content {
						if c.Text != nil {
							resultContent = *c.Text
							break
						}
					}
				}
			}

			// Create a copy of the value to avoid range loop variable capture
			toolResultID := block.ToolResult.ToolUseID

			resultMsg := schemas.ResponsesMessage{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: &resultContent,
				},
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: &toolResultID,
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

// ToBifrostResponsesStream converts a Bedrock stream event to a Bifrost Responses Stream response
func (chunk *BedrockStreamEvent) ToBifrostResponsesStream(sequenceNumber int) (*schemas.BifrostResponse, *schemas.BifrostError, bool) {
	// event with metrics/usage is the last and with stop reason is the second last
	switch {
	case chunk.Role != nil:
		// Message start - create output item added event
		messageType := schemas.ResponsesMessageTypeMessage
		role := schemas.ResponsesInputMessageRoleAssistant

		item := &schemas.ResponsesMessage{
			Type: &messageType,
			Role: &role,
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{}, // Empty blocks slice for mutation support
			},
		}

		return &schemas.BifrostResponse{
			ResponsesStreamResponse: &schemas.ResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
				SequenceNumber: sequenceNumber,
				OutputIndex:    schemas.Ptr(0), // Assuming single output for now
				Item:           item,
			},
		}, nil, false

	case chunk.Start != nil:
		// Handle content block start (text content or tool use)
		contentBlockIndex := 0
		if chunk.ContentBlockIndex != nil {
			contentBlockIndex = *chunk.ContentBlockIndex
		}

		// Check if this is a tool use start
		if chunk.Start.ToolUse != nil {
			// This is a function call starting - create function call message
			item := &schemas.ResponsesMessage{
				ID:   &chunk.Start.ToolUse.ToolUseID,
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    &chunk.Start.ToolUse.ToolUseID,
					Name:      &chunk.Start.ToolUse.Name,
					Arguments: schemas.Ptr(""), // Arguments will be filled by deltas
				},
			}

			return &schemas.BifrostResponse{
				ResponsesStreamResponse: &schemas.ResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(0),
					Item:           item,
				},
			}, nil, false
		} else {
			// This is text content - create content part added event
			part := &schemas.ResponsesMessageContentBlock{
				Type: schemas.ResponsesOutputMessageContentTypeText,
				Text: schemas.Ptr(""), // Empty initially
			}

			return &schemas.BifrostResponse{
				ResponsesStreamResponse: &schemas.ResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(0),
					ContentIndex:   &contentBlockIndex,
					Part:           part,
				},
			}, nil, false
		}

	case chunk.ContentBlockIndex != nil && chunk.Delta != nil:
		// Handle contentBlockDelta event
		contentBlockIndex := *chunk.ContentBlockIndex

		switch {
		case chunk.Delta.Text != nil:
			// Handle text delta
			text := *chunk.Delta.Text
			if text != "" {
				return &schemas.BifrostResponse{
					ResponsesStreamResponse: &schemas.ResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
						SequenceNumber: sequenceNumber,
						OutputIndex:    schemas.Ptr(0),
						ContentIndex:   &contentBlockIndex,
						Delta:          &text,
					},
				}, nil, false
			}

		case chunk.Delta.ToolUse != nil:
			// Handle tool use delta - function call arguments
			toolUseDelta := chunk.Delta.ToolUse

			if toolUseDelta.Input != "" {
				return &schemas.BifrostResponse{
					ResponsesStreamResponse: &schemas.ResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
						SequenceNumber: sequenceNumber,
						OutputIndex:    schemas.Ptr(0),
						ContentIndex:   &contentBlockIndex,
						Delta:          &toolUseDelta.Input,
					},
				}, nil, false
			}
		}

	case chunk.StopReason != nil:
		// Handle end of message - create output item done event
		return &schemas.BifrostResponse{
			ResponsesStreamResponse: &schemas.ResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
				SequenceNumber: sequenceNumber,
				OutputIndex:    schemas.Ptr(0),
			},
		}, nil, false
	}

	return nil, nil, false
}
