package anthropic

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostResponsesRequest converts an Anthropic message request to Bifrost format
func (request *AnthropicMessageRequest) ToBifrostResponsesRequest() *schemas.BifrostResponsesRequest {
	provider, model := schemas.ParseModelString(request.Model, schemas.Anthropic)

	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    model,
	}

	// Convert basic parameters
	params := &schemas.ResponsesParameters{
		ExtraParams: make(map[string]interface{}),
	}

	if request.MaxTokens > 0 {
		params.MaxOutputTokens = &request.MaxTokens
	}
	if request.Temperature != nil {
		params.Temperature = request.Temperature
	}
	if request.TopP != nil {
		params.TopP = request.TopP
	}
	if request.Metadata != nil && request.Metadata.UserID != nil {
		params.User = request.Metadata.UserID
	}
	if request.TopK != nil {
		params.ExtraParams["top_k"] = *request.TopK
	}
	if request.StopSequences != nil {
		params.ExtraParams["stop"] = request.StopSequences
	}
	if request.Thinking != nil {
		params.ExtraParams["thinking"] = request.Thinking
	}

	// Add trucation parameter if computer tool is being used
	if provider == schemas.OpenAI && request.Tools != nil {
		for _, tool := range request.Tools {
			if tool.Type != nil && *tool.Type == AnthropicToolTypeComputer20250124 {
				params.Truncation = schemas.Ptr("auto")
				break
			}
		}
	}

	bifrostReq.Params = params

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
			for _, block := range request.System.ContentBlocks {
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
		for _, tool := range request.Tools {
			bifrostTool := convertAnthropicToolToBifrost(&tool)
			if bifrostTool != nil {
				bifrostTools = append(bifrostTools, *bifrostTool)
			}
		}
		if len(bifrostTools) > 0 {
			bifrostReq.Params.Tools = bifrostTools
		}
	}

	if request.MCPServers != nil {
		var bifrostMCPTools []schemas.ResponsesTool
		for _, mcpServer := range request.MCPServers {
			bifrostMCPTool := convertAnthropicMCPServerToBifrostTool(&mcpServer)
			if bifrostMCPTool != nil {
				bifrostMCPTools = append(bifrostMCPTools, *bifrostMCPTool)
			}
		}
		if len(bifrostMCPTools) > 0 {
			bifrostReq.Params.Tools = append(bifrostReq.Params.Tools, bifrostMCPTools...)
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
		if bifrostReq.Params.User != nil {
			anthropicReq.Metadata = &AnthropicMetaData{
				UserID: bifrostReq.Params.User,
			}
		}
		if bifrostReq.Params.ExtraParams != nil {
			topK, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["top_k"])
			if ok {
				anthropicReq.TopK = topK
			}
			if stop, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["stop"]); ok {
				anthropicReq.StopSequences = stop
			}
			if thinking, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "thinking"); ok {
				if anthropicThinking, ok := thinking.(*AnthropicThinking); ok {
					anthropicReq.Thinking = anthropicThinking
				} else if thinkingMap, ok := thinking.(map[string]interface{}); ok {
					anthropicThinking := &AnthropicThinking{}
					if thinkingType, ok := thinkingMap["type"].(string); ok {
						anthropicThinking.Type = thinkingType
					}
					// Handle budget_tokens - JSON numbers can be float64 or int
					budgetTokens, ok := schemas.SafeExtractInt(thinkingMap["budget_tokens"])
					if ok {
						anthropicThinking.BudgetTokens = &budgetTokens
					}
					anthropicReq.Thinking = anthropicThinking
				}
			}
		}

		// Convert tools
		if bifrostReq.Params.Tools != nil {
			anthropicTools := []AnthropicTool{}
			mcpServers := []AnthropicMCPServer{}
			for _, tool := range bifrostReq.Params.Tools {
				// handle mcp tool differently
				if tool.Type == schemas.ResponsesToolTypeMCP && tool.ResponsesToolMCP != nil {
					mcpServer := convertBifrostMCPToolToAnthropicServer(&tool)
					if mcpServer != nil {
						mcpServers = append(mcpServers, *mcpServer)
					}
					continue // Skip converting MCP tools to anthropicTools since they're handled separately
				}
				anthropicTool := convertBifrostToolToAnthropic(&tool)
				if anthropicTool != nil {
					anthropicTools = append(anthropicTools, *anthropicTool)
				}
			}
			if len(anthropicTools) > 0 {
				anthropicReq.Tools = anthropicTools
			}
			if len(mcpServers) > 0 {
				anthropicReq.MCPServers = mcpServers
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

// ToBifrostResponsesResponse converts an Anthropic response to BifrostResponse with Responses structure
func (response *AnthropicMessageResponse) ToBifrostResponsesResponse() *schemas.BifrostResponsesResponse {
	if response == nil {
		return nil
	}

	// Create the BifrostResponse with Responses structure
	bifrostResp := &schemas.BifrostResponsesResponse{
		ID:        schemas.Ptr(response.ID),
		CreatedAt: int(time.Now().Unix()),
	}

	// Convert usage information
	if response.Usage != nil {
		bifrostResp.Usage = &schemas.ResponsesResponseUsage{
			InputTokens:  response.Usage.InputTokens,
			OutputTokens: response.Usage.OutputTokens,
			TotalTokens:  response.Usage.InputTokens + response.Usage.OutputTokens,
		}

		// Handle cached tokens if present
		if response.Usage.CacheReadInputTokens > 0 {
			if bifrostResp.Usage.InputTokensDetails == nil {
				bifrostResp.Usage.InputTokensDetails = &schemas.ResponsesResponseInputTokens{}
			}
			bifrostResp.Usage.InputTokensDetails.CachedTokens = response.Usage.CacheReadInputTokens
		}
	}

	// Convert content to Responses output messages
	outputMessages := convertAnthropicContentBlocksToResponsesMessages(response.Content)
	if len(outputMessages) > 0 {
		bifrostResp.Output = outputMessages
	}

	return bifrostResp
}

// ToAnthropicResponsesResponse converts a BifrostResponse with Responses structure back to AnthropicMessageResponse
func ToAnthropicResponsesResponse(bifrostResp *schemas.BifrostResponsesResponse) *AnthropicMessageResponse {
	anthropicResp := &AnthropicMessageResponse{
		Type: "message",
		Role: "assistant",
	}
	if bifrostResp.ID != nil {
		anthropicResp.ID = *bifrostResp.ID
	}

	// Convert usage information
	if bifrostResp.Usage != nil {
		anthropicResp.Usage = &AnthropicUsage{
			InputTokens:  bifrostResp.Usage.InputTokens,
			OutputTokens: bifrostResp.Usage.OutputTokens,
		}

		if bifrostResp.Usage.InputTokensDetails != nil && bifrostResp.Usage.InputTokensDetails.CachedTokens > 0 {
			anthropicResp.Usage.CacheReadInputTokens = bifrostResp.Usage.InputTokensDetails.CachedTokens
		}
	}

	// Convert output messages to Anthropic content blocks
	var contentBlocks []AnthropicContentBlock
	if bifrostResp.Output != nil {
		contentBlocks = convertBifrostMessagesToAnthropicContent(bifrostResp.Output)
	}

	if len(contentBlocks) > 0 {
		anthropicResp.Content = contentBlocks
	} else {
		anthropicResp.Content = []AnthropicContentBlock{}
	}

	// Set default stop reason - could be enhanced based on additional context
	anthropicResp.StopReason = AnthropicStopReasonEndTurn

	// Check if there are tool calls to set appropriate stop reason
	for _, block := range contentBlocks {
		if block.Type == AnthropicContentBlockTypeToolUse {
			anthropicResp.StopReason = AnthropicStopReasonToolUse
			break
		}
	}

	return anthropicResp
}

// ToBifrostResponsesStream converts an Anthropic stream event to a Bifrost Responses Stream response
func (chunk *AnthropicStreamEvent) ToBifrostResponsesStream(sequenceNumber int) (*schemas.BifrostResponsesStreamResponse, *schemas.BifrostError, bool) {
	switch chunk.Type {
	case AnthropicStreamEventTypeMessageStart:
		// Message start - create output item added event
		if chunk.Message != nil {
			messageType := schemas.ResponsesMessageTypeMessage
			role := schemas.ResponsesInputMessageRoleAssistant

			item := &schemas.ResponsesMessage{
				ID:   &chunk.Message.ID,
				Type: &messageType,
				Role: &role,
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{}, // Empty blocks slice for mutation support
				},
			}

			return &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
				SequenceNumber: sequenceNumber,
				OutputIndex:    schemas.Ptr(0), // Assuming single output for now
				Item:           item,
			}, nil, false
		}

	case AnthropicStreamEventTypeContentBlockStart:
		// Content block start - create content part added event
		if chunk.ContentBlock != nil && chunk.Index != nil {
			var contentType schemas.ResponsesMessageContentBlockType
			var part *schemas.ResponsesMessageContentBlock

			switch chunk.ContentBlock.Type {
			case AnthropicContentBlockTypeText:
				contentType = schemas.ResponsesOutputMessageContentTypeText
				part = &schemas.ResponsesMessageContentBlock{
					Type: contentType,
					Text: schemas.Ptr(""), // Empty text initially
				}
			case AnthropicContentBlockTypeToolUse:
				// This is a function call starting - create function call message

				item := &schemas.ResponsesMessage{
					ID:   chunk.ContentBlock.ToolUseID,
					Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:    chunk.ContentBlock.ToolUseID,
						Name:      chunk.ContentBlock.Name,
						Arguments: schemas.Ptr(""), // Arguments will be filled by deltas
					},
				}

				return &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(0),
					ContentIndex:   chunk.Index,
					Item:           item,
				}, nil, false
			case AnthropicContentBlockTypeMCPToolUse:
				// This is an MCP tool call starting - create MCP call message
				item := &schemas.ResponsesMessage{
					ID:   chunk.ContentBlock.ID,
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMCPCall),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						Name:      chunk.ContentBlock.Name,
						Arguments: schemas.Ptr(""), // Arguments will be filled by deltas
					},
				}

				// Set server name if present
				if chunk.ContentBlock.ServerName != nil {
					item.ResponsesToolMessage.ResponsesMCPToolCall = &schemas.ResponsesMCPToolCall{
						ServerLabel: *chunk.ContentBlock.ServerName,
					}
				}

				// First emit output_item.added
				outputItemAddedResp := &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(0),
					ContentIndex:   chunk.Index,
					Item:           item,
				}

				return outputItemAddedResp, nil, false
			}

			if part != nil {
				return &schemas.BifrostResponsesStreamResponse{
					Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
					SequenceNumber: sequenceNumber,
					OutputIndex:    schemas.Ptr(0),
					ContentIndex:   chunk.Index,
					Part:           part,
				}, nil, false
			}
		}

	case AnthropicStreamEventTypeContentBlockDelta:
		if chunk.Index != nil && chunk.Delta != nil {
			// Handle different delta types
			switch chunk.Delta.Type {
			case AnthropicStreamDeltaTypeText:
				if chunk.Delta.Text != nil && *chunk.Delta.Text != "" {
					// Text content delta
					return &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
						SequenceNumber: sequenceNumber,
						OutputIndex:    schemas.Ptr(0),
						ContentIndex:   chunk.Index,
						Delta:          chunk.Delta.Text,
					}, nil, false
				}

			case AnthropicStreamDeltaTypeInputJSON:
				// Function call arguments delta
				if chunk.Delta.PartialJSON != nil && *chunk.Delta.PartialJSON != "" {
					return &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
						SequenceNumber: sequenceNumber,
						OutputIndex:    schemas.Ptr(0),
						ContentIndex:   chunk.Index,
						Delta:          chunk.Delta.PartialJSON,
					}, nil, false
				}

			case AnthropicStreamDeltaTypeThinking:
				// Reasoning/thinking content delta
				if chunk.Delta.Thinking != nil && *chunk.Delta.Thinking != "" {
					return &schemas.BifrostResponsesStreamResponse{
						Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
						SequenceNumber: sequenceNumber,
						OutputIndex:    schemas.Ptr(0),
						ContentIndex:   chunk.Index,
						Delta:          chunk.Delta.Thinking,
					}, nil, false
				}

			case AnthropicStreamDeltaTypeSignature:
				// Handle signature verification for thinking content
				// This is used to verify the integrity of thinking content
				// For now, we don't need to emit a specific event for signatures
				return nil, nil, false
			}
		}

	case AnthropicStreamEventTypeContentBlockStop:
		// Content block is complete
		if chunk.Index != nil {
			return &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
				SequenceNumber: sequenceNumber,
				OutputIndex:    schemas.Ptr(0),
				ContentIndex:   chunk.Index,
			}, nil, false
		}

	case AnthropicStreamEventTypeMessageDelta:
		// Message-level updates (like stop reason, usage, etc.)
		if chunk.Delta != nil && chunk.Delta.StopReason != nil {
			// Indicate the output item is done
			return &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
				SequenceNumber: sequenceNumber,
				OutputIndex:    schemas.Ptr(0),
			}, nil, false
		}

	case AnthropicStreamEventTypeMessageStop:
		// Message stop - this is the final chunk indicating stream completion
		return &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeCompleted,
			SequenceNumber: sequenceNumber,
		}, nil, true // Indicate stream is complete

	case AnthropicStreamEventTypePing:
		// Ping events are just keepalive, no action needed
		return nil, nil, false

	case AnthropicStreamEventTypeError:
		if chunk.Error != nil {
			// Send error event
			bifrostErr := &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    &chunk.Error.Type,
					Message: chunk.Error.Message,
				},
			}

			return &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeError,
				SequenceNumber: sequenceNumber,
				Message:        &chunk.Error.Message,
			}, bifrostErr, false
		}
	}

	return nil, nil, false
}

// ToAnthropicResponsesStreamResponse converts a Bifrost Responses stream response to Anthropic SSE string format
func ToAnthropicResponsesStreamResponse(bifrostResp *schemas.BifrostResponsesStreamResponse) string {
	if bifrostResp == nil {
		return ""
	}

	streamResp := &AnthropicStreamEvent{}

	// Map ResponsesStreamResponse types to Anthropic stream events
	switch bifrostResp.Type {
	case schemas.ResponsesStreamResponseTypeOutputItemAdded:
		// Check if this is a function call (tool use) message
		if bifrostResp.Item != nil && bifrostResp.Item.Type != nil && *bifrostResp.Item.Type == schemas.ResponsesMessageTypeFunctionCall {
			// Convert function call to tool_use content_block_start event
			streamResp.Type = AnthropicStreamEventTypeContentBlockStart
			if bifrostResp.ContentIndex != nil {
				streamResp.Index = bifrostResp.ContentIndex
			}

			contentBlock := &AnthropicContentBlock{
				Type: AnthropicContentBlockTypeToolUse,
			}

			if bifrostResp.Item.ResponsesToolMessage != nil {
				if bifrostResp.Item.ResponsesToolMessage.CallID != nil {
					contentBlock.ID = bifrostResp.Item.ResponsesToolMessage.CallID
				}
				if bifrostResp.Item.ResponsesToolMessage.Name != nil {
					contentBlock.Name = bifrostResp.Item.ResponsesToolMessage.Name
				}
			}

			streamResp.ContentBlock = contentBlock
		} else {
			// Regular message start event
			streamResp.Type = AnthropicStreamEventTypeMessageStart
			if bifrostResp.Item != nil {
				// Create message start event
				streamMessage := &AnthropicMessageResponse{
					Type: "message",
					Role: string(schemas.ResponsesInputMessageRoleAssistant),
				}
				if bifrostResp.Item.ID != nil {
					streamMessage.ID = *bifrostResp.Item.ID
				}
				streamResp.Message = streamMessage
			}
		}

	case schemas.ResponsesStreamResponseTypeContentPartAdded:
		streamResp.Type = AnthropicStreamEventTypeContentBlockStart
		if bifrostResp.ContentIndex != nil {
			streamResp.Index = bifrostResp.ContentIndex
		}
		if bifrostResp.Part != nil {
			contentBlock := &AnthropicContentBlock{}
			switch bifrostResp.Part.Type {
			case schemas.ResponsesOutputMessageContentTypeText:
				contentBlock.Type = AnthropicContentBlockTypeText
				if bifrostResp.Part.Text != nil {
					contentBlock.Text = bifrostResp.Part.Text
				}
			}
			streamResp.ContentBlock = contentBlock
		}

	case schemas.ResponsesStreamResponseTypeOutputTextDelta:
		streamResp.Type = AnthropicStreamEventTypeContentBlockDelta
		if bifrostResp.ContentIndex != nil {
			streamResp.Index = bifrostResp.ContentIndex
		}
		if bifrostResp.Delta != nil {
			streamResp.Delta = &AnthropicStreamDelta{
				Type: AnthropicStreamDeltaTypeText,
				Text: bifrostResp.Delta,
			}
		}

	case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
		streamResp.Type = AnthropicStreamEventTypeContentBlockDelta
		if bifrostResp.ContentIndex != nil {
			streamResp.Index = bifrostResp.ContentIndex
		}
		if bifrostResp.Arguments != nil {
			streamResp.Delta = &AnthropicStreamDelta{
				Type:        AnthropicStreamDeltaTypeInputJSON,
				PartialJSON: bifrostResp.Arguments,
			}
		}

	case schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta:
		streamResp.Type = AnthropicStreamEventTypeContentBlockDelta
		if bifrostResp.ContentIndex != nil {
			streamResp.Index = bifrostResp.ContentIndex
		}
		if bifrostResp.Delta != nil {
			streamResp.Delta = &AnthropicStreamDelta{
				Type:     AnthropicStreamDeltaTypeThinking,
				Thinking: bifrostResp.Delta,
			}
		}

	case schemas.ResponsesStreamResponseTypeContentPartDone:
		streamResp.Type = AnthropicStreamEventTypeContentBlockStop
		if bifrostResp.ContentIndex != nil {
			streamResp.Index = bifrostResp.ContentIndex
		}

	case schemas.ResponsesStreamResponseTypeOutputItemDone:
		streamResp.Type = AnthropicStreamEventTypeMessageDelta

	case schemas.ResponsesStreamResponseTypeCompleted:
		streamResp.Type = AnthropicStreamEventTypeMessageStop

	case schemas.ResponsesStreamResponseTypeMCPCallArgumentsDelta:
		// MCP call arguments delta - convert to content_block_delta with input_json
		streamResp.Type = AnthropicStreamEventTypeContentBlockDelta
		if bifrostResp.ContentIndex != nil {
			streamResp.Index = bifrostResp.ContentIndex
		} else if bifrostResp.OutputIndex != nil {
			streamResp.Index = bifrostResp.OutputIndex
		}
		if bifrostResp.Delta != nil {
			streamResp.Delta = &AnthropicStreamDelta{
				Type:        AnthropicStreamDeltaTypeInputJSON,
				PartialJSON: bifrostResp.Delta,
			}
		}

	case schemas.ResponsesStreamResponseTypeMCPCallCompleted:
		// MCP call completed - emit content_block_stop
		streamResp.Type = AnthropicStreamEventTypeContentBlockStop
		if bifrostResp.ContentIndex != nil {
			streamResp.Index = bifrostResp.ContentIndex
		} else if bifrostResp.OutputIndex != nil {
			streamResp.Index = bifrostResp.OutputIndex
		}

	case schemas.ResponsesStreamResponseTypeMCPCallFailed:
		// MCP call failed - emit error event
		streamResp.Type = AnthropicStreamEventTypeError
		errorMsg := "MCP call failed"
		if bifrostResp.Message != nil {
			errorMsg = *bifrostResp.Message
		}
		streamResp.Error = &AnthropicStreamError{
			Type:    "error",
			Message: errorMsg,
		}

	case schemas.ResponsesStreamResponseTypeError:
		streamResp.Type = AnthropicStreamEventTypeError
		if bifrostResp.Message != nil {
			streamResp.Error = &AnthropicStreamError{
				Type:    "error",
				Message: *bifrostResp.Message,
			}
		}

	default:
		// Unknown event type, return empty
		return ""
	}

	// Marshal to JSON and format as SSE
	jsonData, err := json.Marshal(streamResp)
	if err != nil {
		return ""
	}

	// Format as Anthropic SSE
	return fmt.Sprintf("event: %s\ndata: %s\n\n", streamResp.Type, jsonData)
}

// ToAnthropicResponsesStreamError converts a BifrostError to Anthropic responses streaming error in SSE format
func ToAnthropicResponsesStreamError(bifrostErr *schemas.BifrostError) string {
	if bifrostErr == nil {
		return ""
	}

	streamResp := &AnthropicStreamEvent{
		Type: AnthropicStreamEventTypeError,
		Error: &AnthropicStreamError{
			Type:    "error",
			Message: bifrostErr.Error.Message,
		},
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(streamResp)
	if err != nil {
		return ""
	}

	// Format as Anthropic SSE error event
	return fmt.Sprintf("event: error\ndata: %s\n\n", jsonData)
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
							CallID: block.ID,
							Name:   block.Name,
						},
					}

					// here need to check for computer tool use
					if block.Name != nil && *block.Name == string(AnthropicToolNameComputer) {
						bifrostMsg.Type = schemas.Ptr(schemas.ResponsesMessageTypeComputerCall)
						bifrostMsg.ResponsesToolMessage.Name = nil
						if inputMap, ok := block.Input.(map[string]interface{}); ok {
							bifrostMsg.ResponsesToolMessage.Action = &schemas.ResponsesToolMessageActionStruct{
								ResponsesComputerToolCallAction: convertAnthropicToResponsesComputerAction(inputMap),
							}
						}
					} else {
						bifrostMsg.ResponsesToolMessage.Arguments = schemas.Ptr(schemas.JsonifyInput(block.Input))
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
						bifrostMsg.ResponsesToolMessage.Output = &schemas.ResponsesToolMessageOutputStruct{}

						if block.Content.ContentStr != nil {
							bifrostMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr = block.Content.ContentStr
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
							bifrostMsg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks = toolMsgContentBlocks
						}
						bifrostMessages = append(bifrostMessages, bifrostMsg)
					}
				}
			case AnthropicContentBlockTypeMCPToolUse:
				// Convert MCP tool use to MCP call (assistant's tool call)
				if block.ID != nil && block.Name != nil {
					bifrostMsg := schemas.ResponsesMessage{
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMCPCall),
						ID:   block.ID,
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							Name:      block.Name,
							Arguments: schemas.Ptr(schemas.JsonifyInput(block.Input)),
						},
					}
					if block.ServerName != nil {
						bifrostMsg.ResponsesToolMessage.ResponsesMCPToolCall = &schemas.ResponsesMCPToolCall{
							ServerLabel: *block.ServerName,
						}
					}
					bifrostMessages = append(bifrostMessages, bifrostMsg)
				}
			case AnthropicContentBlockTypeMCPToolResult:
				// Convert MCP tool result to MCP call (user's tool result)
				if block.ToolUseID != nil {
					bifrostMsg := schemas.ResponsesMessage{
						Type:   schemas.Ptr(schemas.ResponsesMessageTypeMCPCall),
						Status: schemas.Ptr("completed"),
						ResponsesToolMessage: &schemas.ResponsesToolMessage{
							CallID: block.ToolUseID,
						},
					}
					// Initialize the nested struct before any writes
					bifrostMsg.ResponsesToolMessage.Output = &schemas.ResponsesToolMessageOutputStruct{}

					if block.Content != nil {
						if block.Content.ContentStr != nil {
							bifrostMsg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr = block.Content.ContentStr
						} else if block.Content.ContentBlocks != nil {
							var toolMsgContentBlocks []schemas.ResponsesMessageContentBlock
							for _, contentBlock := range block.Content.ContentBlocks {
								if contentBlock.Type == AnthropicContentBlockTypeText {
									if contentBlock.Text != nil {
										toolMsgContentBlocks = append(toolMsgContentBlocks, schemas.ResponsesMessageContentBlock{
											Type: schemas.ResponsesInputMessageContentBlockTypeText,
											Text: contentBlock.Text,
										})
									}
								}
							}
							bifrostMsg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks = toolMsgContentBlocks
						}
					}
					bifrostMessages = append(bifrostMessages, bifrostMsg)
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

	// Handle special tool types first
	if tool.Type != nil {
		switch *tool.Type {
		case AnthropicToolTypeComputer20250124:
			bifrostTool := &schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeComputerUsePreview,
			}
			if tool.AnthropicToolComputerUse != nil {
				bifrostTool.ResponsesToolComputerUsePreview = &schemas.ResponsesToolComputerUsePreview{
					Environment: "browser", // Default environment
				}
				if tool.AnthropicToolComputerUse.DisplayWidthPx != nil {
					bifrostTool.ResponsesToolComputerUsePreview.DisplayWidth = *tool.AnthropicToolComputerUse.DisplayWidthPx
				}
				if tool.AnthropicToolComputerUse.DisplayHeightPx != nil {
					bifrostTool.ResponsesToolComputerUsePreview.DisplayHeight = *tool.AnthropicToolComputerUse.DisplayHeightPx
				}
			}
			return bifrostTool

		case AnthropicToolTypeWebSearch20250305:
			bifrostTool := &schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeWebSearch,
				Name: &tool.Name,
			}
			if tool.AnthropicToolWebSearch != nil {
				bifrostTool.ResponsesToolWebSearch = &schemas.ResponsesToolWebSearch{
					Filters: &schemas.ResponsesToolWebSearchFilters{
						AllowedDomains: tool.AnthropicToolWebSearch.AllowedDomains,
					},
				}
				if tool.AnthropicToolWebSearch.UserLocation != nil {
					bifrostTool.ResponsesToolWebSearch.UserLocation = &schemas.ResponsesToolWebSearchUserLocation{
						Type:     tool.AnthropicToolWebSearch.UserLocation.Type,
						City:     tool.AnthropicToolWebSearch.UserLocation.City,
						Country:  tool.AnthropicToolWebSearch.UserLocation.Country,
						Timezone: tool.AnthropicToolWebSearch.UserLocation.Timezone,
					}
				}
			}
			return bifrostTool

		case AnthropicToolTypeBash20250124:
			return &schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeLocalShell,
			}

		case AnthropicToolTypeTextEditor20250124:
			return &schemas.ResponsesTool{
				Type: schemas.ResponsesToolType(AnthropicToolTypeTextEditor20250124),
				Name: &tool.Name,
			}
		case AnthropicToolTypeTextEditor20250429:
			return &schemas.ResponsesTool{
				Type: schemas.ResponsesToolType(AnthropicToolTypeTextEditor20250429),
				Name: &tool.Name,
			}
		case AnthropicToolTypeTextEditor20250728:
			return &schemas.ResponsesTool{
				Type: schemas.ResponsesToolType(AnthropicToolTypeTextEditor20250728),
				Name: &tool.Name,
			}
		}
	}

	// Handle custom/default tool type (function)
	bifrostTool := &schemas.ResponsesTool{
		Type:        schemas.ResponsesToolTypeFunction,
		Name:        &tool.Name,
		Description: tool.Description,
	}

	if tool.InputSchema != nil {
		bifrostTool.ResponsesToolFunction = &schemas.ResponsesToolFunction{
			Parameters: tool.InputSchema,
		}
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

// flushPendingToolCalls is a helper that flushes accumulated tool calls into an assistant message
func flushPendingToolCalls(
	pendingToolCalls []AnthropicContentBlock,
	currentAssistantMessage *AnthropicMessage,
	anthropicMessages []AnthropicMessage,
) ([]AnthropicContentBlock, *AnthropicMessage, []AnthropicMessage) {
	if len(pendingToolCalls) > 0 && currentAssistantMessage != nil {
		// Copy the slice to avoid aliasing issues
		copied := make([]AnthropicContentBlock, len(pendingToolCalls))
		copy(copied, pendingToolCalls)
		currentAssistantMessage.Content = AnthropicContent{
			ContentBlocks: copied,
		}
		anthropicMessages = append(anthropicMessages, *currentAssistantMessage)
		// Return nil values to indicate flushed state
		return nil, nil, anthropicMessages
	}
	// Return unchanged values if no flush was needed
	return pendingToolCalls, currentAssistantMessage, anthropicMessages
}

// convertToolOutputToAnthropicContent converts tool output to Anthropic content format
func convertToolOutputToAnthropicContent(output *schemas.ResponsesToolMessageOutputStruct) *AnthropicContent {
	if output == nil {
		return nil
	}

	if output.ResponsesToolCallOutputStr != nil {
		return &AnthropicContent{
			ContentStr: output.ResponsesToolCallOutputStr,
		}
	}

	if output.ResponsesFunctionToolCallOutputBlocks != nil {
		var resultBlocks []AnthropicContentBlock
		for _, block := range output.ResponsesFunctionToolCallOutputBlocks {
			if converted := convertContentBlockToAnthropic(block); converted != nil {
				resultBlocks = append(resultBlocks, *converted)
			}
		}
		if len(resultBlocks) > 0 {
			return &AnthropicContent{
				ContentBlocks: resultBlocks,
			}
		}
	}

	if output.ResponsesComputerToolCallOutput != nil && output.ResponsesComputerToolCallOutput.ImageURL != nil {
		imgBlock := ConvertToAnthropicImageBlock(schemas.ChatContentBlock{
			Type: schemas.ChatContentBlockTypeImage,
			ImageURLStruct: &schemas.ChatInputImage{
				URL: *output.ResponsesComputerToolCallOutput.ImageURL,
			},
		})
		return &AnthropicContent{
			ContentBlocks: []AnthropicContentBlock{imgBlock},
		}
	}

	return nil
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
			pendingToolCalls, currentAssistantMessage, anthropicMessages = flushPendingToolCalls(
				pendingToolCalls, currentAssistantMessage, anthropicMessages)

			// Handle system messages separately
			if msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleSystem {
				if msg.Content != nil {
					if msg.Content.ContentStr != nil {
						systemContent = &AnthropicContent{
							ContentStr: msg.Content.ContentStr,
						}
					} else if msg.Content.ContentBlocks != nil {
						contentBlocks := convertBifrostContentBlocksToAnthropic(msg.Content.ContentBlocks)
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
					contentBlocks := convertBifrostContentBlocksToAnthropic(msg.Content.ContentBlocks)
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
			pendingToolCalls, currentAssistantMessage, anthropicMessages = flushPendingToolCalls(
				pendingToolCalls, currentAssistantMessage, anthropicMessages)

			// Handle tool call output - convert to user message with tool_result
			if msg.ResponsesToolMessage != nil {
				toolResultBlock := AnthropicContentBlock{
					Type:      AnthropicContentBlockTypeToolResult,
					ToolUseID: msg.ResponsesToolMessage.CallID,
				}

				if msg.ResponsesToolMessage.Output != nil {
					toolResultBlock.Content = convertToolOutputToAnthropicContent(msg.ResponsesToolMessage.Output)
				}

				toolResultMsg := AnthropicMessage{
					Role: AnthropicMessageRoleUser,
					Content: AnthropicContent{
						ContentBlocks: []AnthropicContentBlock{toolResultBlock},
					},
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
		case schemas.ResponsesMessageTypeComputerCall:
			// Start accumulating tool calls for assistant message
			if currentAssistantMessage == nil {
				currentAssistantMessage = &AnthropicMessage{
					Role: AnthropicMessageRoleAssistant,
				}
			}

			if msg.ResponsesToolMessage != nil {
				toolUseBlock := AnthropicContentBlock{
					Type: AnthropicContentBlockTypeToolUse,
					Name: schemas.Ptr(string(AnthropicToolNameComputer)),
				}
				if msg.ResponsesToolMessage.CallID != nil {
					toolUseBlock.ID = msg.ResponsesToolMessage.CallID
				}
				if msg.ResponsesToolMessage.Name != nil {
					toolUseBlock.Name = msg.ResponsesToolMessage.Name
				}

				if msg.ResponsesToolMessage.Action != nil && msg.ResponsesToolMessage.Action.ResponsesComputerToolCallAction != nil {
					toolUseBlock.Input = convertResponsesToAnthropicComputerAction(msg.ResponsesToolMessage.Action.ResponsesComputerToolCallAction)
				}

				pendingToolCalls = append(pendingToolCalls, toolUseBlock)
			}

		case schemas.ResponsesMessageTypeMCPCall:
			// Check if this is a tool use (from assistant) or tool result (from user)
			// Tool use: has Name and Arguments but no Output
			// Tool result: has CallID and Output
			if msg.ResponsesToolMessage != nil {
				// This is a tool use call (assistant calling a tool)
				if msg.ResponsesToolMessage.Name != nil {
					// Start accumulating MCP tool calls for assistant message
					if currentAssistantMessage == nil {
						currentAssistantMessage = &AnthropicMessage{
							Role: AnthropicMessageRoleAssistant,
						}
					}

					toolUseBlock := AnthropicContentBlock{
						Type: AnthropicContentBlockTypeMCPToolUse,
					}

					if msg.ID != nil {
						toolUseBlock.ID = msg.ID
					}
					toolUseBlock.Name = msg.ResponsesToolMessage.Name

					// Set server name if present
					if msg.ResponsesToolMessage.ResponsesMCPToolCall != nil && msg.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel != "" {
						toolUseBlock.ServerName = &msg.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel
					}

					// Parse arguments as JSON input
					if msg.ResponsesToolMessage.Arguments != nil && *msg.ResponsesToolMessage.Arguments != "" {
						toolUseBlock.Input = parseJSONInput(*msg.ResponsesToolMessage.Arguments)
					}

					pendingToolCalls = append(pendingToolCalls, toolUseBlock)
				} else if msg.ResponsesToolMessage.CallID != nil {
					// This is a tool result (user providing result of tool execution)
					toolResultBlock := AnthropicContentBlock{
						Type: AnthropicContentBlockTypeMCPToolResult,
						ID:   msg.ResponsesToolMessage.CallID,
					}

					if msg.ResponsesToolMessage.Output != nil {
						toolResultBlock.Content = convertToolOutputToAnthropicContent(msg.ResponsesToolMessage.Output)
					}

					toolResultMsg := AnthropicMessage{
						Role: AnthropicMessageRoleUser,
						Content: AnthropicContent{
							ContentBlocks: []AnthropicContentBlock{toolResultBlock},
						},
					}

					anthropicMessages = append(anthropicMessages, toolResultMsg)
				}
			}

		case schemas.ResponsesMessageTypeMCPApprovalRequest:
			// MCP approval request is OpenAI-specific for human-in-the-loop workflows
			// Convert to Anthropic's mcp_tool_use format (same as regular MCP calls)
			if currentAssistantMessage == nil {
				currentAssistantMessage = &AnthropicMessage{
					Role: AnthropicMessageRoleAssistant,
				}
			}

			if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Name != nil {
				toolUseBlock := AnthropicContentBlock{
					Type: AnthropicContentBlockTypeMCPToolUse,
				}

				if msg.ID != nil {
					toolUseBlock.ID = msg.ID
				}
				toolUseBlock.Name = msg.ResponsesToolMessage.Name

				// Set server name if present
				if msg.ResponsesToolMessage.ResponsesMCPToolCall != nil && msg.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel != "" {
					toolUseBlock.ServerName = &msg.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel
				}

				// Parse arguments as JSON input
				if msg.ResponsesToolMessage.Arguments != nil && *msg.ResponsesToolMessage.Arguments != "" {
					toolUseBlock.Input = parseJSONInput(*msg.ResponsesToolMessage.Arguments)
				}

				pendingToolCalls = append(pendingToolCalls, toolUseBlock)
			}

		// Handle other tool call types that are not natively supported by Anthropic
		case schemas.ResponsesMessageTypeFileSearchCall,
			schemas.ResponsesMessageTypeCodeInterpreterCall,
			schemas.ResponsesMessageTypeWebSearchCall,
			schemas.ResponsesMessageTypeLocalShellCall,
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

		case schemas.ResponsesMessageTypeComputerCallOutput:
			// Flush any pending tool calls first before processing tool results
			pendingToolCalls, currentAssistantMessage, anthropicMessages = flushPendingToolCalls(
				pendingToolCalls, currentAssistantMessage, anthropicMessages)

			// Handle computer call output - convert to user message with tool_result
			if msg.ResponsesToolMessage != nil {
				toolResultBlock := AnthropicContentBlock{
					Type:      AnthropicContentBlockTypeToolResult,
					ToolUseID: msg.ResponsesToolMessage.CallID,
				}

				if msg.ResponsesToolMessage.Output != nil {
					toolResultBlock.Content = convertToolOutputToAnthropicContent(msg.ResponsesToolMessage.Output)
				}

				toolResultMsg := AnthropicMessage{
					Role: AnthropicMessageRoleUser,
					Content: AnthropicContent{
						ContentBlocks: []AnthropicContentBlock{toolResultBlock},
					},
				}

				anthropicMessages = append(anthropicMessages, toolResultMsg)
			}

		case schemas.ResponsesMessageTypeLocalShellCallOutput,
			schemas.ResponsesMessageTypeCustomToolCallOutput:
			// Handle tool outputs as user messages
			if msg.ResponsesToolMessage != nil {
				toolOutputMsg := AnthropicMessage{
					Role: AnthropicMessageRoleUser,
				}

				var outputText string
				// Try to extract output text based on tool type
				if msg.ResponsesToolMessage.Output != nil && msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
					outputText = *msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
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
	pendingToolCalls, currentAssistantMessage, anthropicMessages = flushPendingToolCalls(
		pendingToolCalls, currentAssistantMessage, anthropicMessages)

	return anthropicMessages, systemContent
}

// Helper function to convert Tool back to AnthropicTool
func convertBifrostToolToAnthropic(tool *schemas.ResponsesTool) *AnthropicTool {
	if tool == nil {
		return nil
	}

	switch tool.Type {
	case schemas.ResponsesToolTypeComputerUsePreview:
		if tool.ResponsesToolComputerUsePreview != nil {
			return &AnthropicTool{
				Type: schemas.Ptr(AnthropicToolTypeComputer20250124),
				Name: string(AnthropicToolNameComputer),
				AnthropicToolComputerUse: &AnthropicToolComputerUse{
					DisplayWidthPx:  schemas.Ptr(tool.ResponsesToolComputerUsePreview.DisplayWidth),
					DisplayHeightPx: schemas.Ptr(tool.ResponsesToolComputerUsePreview.DisplayHeight),
					DisplayNumber:   schemas.Ptr(1),
				},
			}
		}
	case schemas.ResponsesToolTypeWebSearch:
		anthropicTool := &AnthropicTool{
			Type:                   schemas.Ptr(AnthropicToolTypeWebSearch20250305),
			Name:                   string(AnthropicToolNameWebSearch),
			AnthropicToolWebSearch: &AnthropicToolWebSearch{},
		}
		if tool.ResponsesToolWebSearch != nil {
			if tool.ResponsesToolWebSearch.Filters != nil {
				anthropicTool.AnthropicToolWebSearch.AllowedDomains = tool.ResponsesToolWebSearch.Filters.AllowedDomains
			}
			if tool.ResponsesToolWebSearch.UserLocation != nil {
				anthropicTool.AnthropicToolWebSearch.UserLocation = &AnthropicToolWebSearchUserLocation{
					Type:     tool.ResponsesToolWebSearch.UserLocation.Type,
					City:     tool.ResponsesToolWebSearch.UserLocation.City,
					Country:  tool.ResponsesToolWebSearch.UserLocation.Country,
					Timezone: tool.ResponsesToolWebSearch.UserLocation.Timezone,
				}
			}
		}

		return anthropicTool
	case schemas.ResponsesToolTypeLocalShell:
		return &AnthropicTool{
			Type: schemas.Ptr(AnthropicToolTypeBash20250124),
			Name: string(AnthropicToolNameBash),
		}
	case schemas.ResponsesToolType(AnthropicToolTypeTextEditor20250124):
		return &AnthropicTool{
			Type: schemas.Ptr(AnthropicToolTypeTextEditor20250124),
			Name: string(AnthropicToolNameTextEditor),
		}
	case schemas.ResponsesToolType(AnthropicToolTypeTextEditor20250429):
		return &AnthropicTool{
			Type: schemas.Ptr(AnthropicToolTypeTextEditor20250429),
			Name: string(AnthropicToolNameTextEditor),
		}
	case schemas.ResponsesToolType(AnthropicToolTypeTextEditor20250728):
		return &AnthropicTool{
			Type: schemas.Ptr(AnthropicToolTypeTextEditor20250728),
			Name: string(AnthropicToolNameTextEditor),
		}
	}

	anthropicTool := &AnthropicTool{
		Type: schemas.Ptr(AnthropicToolTypeCustom),
	}

	if tool.Name != nil {
		anthropicTool.Name = *tool.Name
	}

	if tool.Description != nil {
		anthropicTool.Description = tool.Description
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
		case AnthropicContentBlockTypeText:
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

		case AnthropicContentBlockTypeImage:
			if block.Source != nil {
				messages = append(messages, schemas.ResponsesMessage{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							block.toBifrostResponsesImageBlock(),
						},
					},
				})
			}

		case AnthropicContentBlockTypeThinking:
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
					ResponsesReasoning: &schemas.ResponsesReasoning{
						Summary: []schemas.ResponsesReasoningContent{
							{
								Text: *block.Thinking,
								Type: schemas.ResponsesReasoningContentBlockTypeSummaryText,
							},
						},
						EncryptedContent: block.Signature,
					},
				})
			}

		case AnthropicContentBlockTypeToolUse:
			if block.ID != nil && block.Name != nil {
				// Create function call message
				message := schemas.ResponsesMessage{
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					Status: schemas.Ptr("completed"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: block.ID,
						Name:   block.Name,
					},
				}

				if block.Name != nil && *block.Name == string(AnthropicToolNameComputer) {
					message.Type = schemas.Ptr(schemas.ResponsesMessageTypeComputerCall)
					message.ResponsesToolMessage.Name = nil
					if inputMap, ok := block.Input.(map[string]interface{}); ok {
						message.ResponsesToolMessage.Action = &schemas.ResponsesToolMessageActionStruct{
							ResponsesComputerToolCallAction: convertAnthropicToResponsesComputerAction(inputMap),
						}
					}
				} else {
					message.ResponsesToolMessage.Arguments = schemas.Ptr(schemas.JsonifyInput(block.Input))
				}

				messages = append(messages, message)
			}
		case AnthropicContentBlockTypeToolResult:
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
				msg.ResponsesToolMessage.Output = &schemas.ResponsesToolMessageOutputStruct{}
				if block.Content != nil {
					if block.Content.ContentStr != nil {
						msg.ResponsesToolMessage.Output.
							ResponsesToolCallOutputStr = block.Content.ContentStr
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
						msg.ResponsesToolMessage.Output.
							ResponsesFunctionToolCallOutputBlocks = outBlocks
					}
				}
				messages = append(messages, msg)
			}

		case AnthropicContentBlockTypeMCPToolUse:
			if block.ID != nil && block.Name != nil {
				// Create MCP call message (tool invocation from assistant)
				message := schemas.ResponsesMessage{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMCPCall),
					ID:   block.ID,
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						Name:      block.Name,
						Arguments: schemas.Ptr(schemas.JsonifyInput(block.Input)),
					},
				}

				// Set server name if present
				if block.ServerName != nil {
					message.ResponsesToolMessage.ResponsesMCPToolCall = &schemas.ResponsesMCPToolCall{
						ServerLabel: *block.ServerName,
					}
				}

				messages = append(messages, message)
			}

		case AnthropicContentBlockTypeMCPToolResult:
			if block.ToolUseID != nil {
				// Create MCP call message (tool result)
				msg := schemas.ResponsesMessage{
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeMCPCall),
					Status: schemas.Ptr("completed"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: block.ToolUseID,
					},
				}
				// Initialize nested output struct
				msg.ResponsesToolMessage.Output = &schemas.ResponsesToolMessageOutputStruct{}
				if block.Content != nil {
					if block.Content.ContentStr != nil {
						msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr = block.Content.ContentStr
					} else if block.Content.ContentBlocks != nil {
						var outBlocks []schemas.ResponsesMessageContentBlock
						for _, cb := range block.Content.ContentBlocks {
							if cb.Type == AnthropicContentBlockTypeText {
								if cb.Text != nil {
									outBlocks = append(outBlocks, schemas.ResponsesMessageContentBlock{
										Type: schemas.ResponsesOutputMessageContentTypeText,
										Text: cb.Text,
									})
								}
							}
						}
						msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks = outBlocks
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
				if msg.Content != nil {
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
					Type: AnthropicContentBlockTypeToolResult,
				}

				if msg.ResponsesToolMessage != nil {
					resultBlock.ToolUseID = msg.ResponsesToolMessage.CallID
					// Try content from msg.Content first, then Output
					if msg.Content != nil && msg.Content.ContentStr != nil {
						resultBlock.Content = &AnthropicContent{
							ContentStr: msg.Content.ContentStr,
						}
					} else if msg.ResponsesToolMessage.Output != nil {
						resultBlock.Content = convertToolOutputToAnthropicContent(msg.ResponsesToolMessage.Output)
					}
				} else if msg.Content != nil && msg.Content.ContentStr != nil {
					// Fallback to msg.Content when ResponsesToolMessage is nil
					resultBlock.Content = &AnthropicContent{
						ContentStr: msg.Content.ContentStr,
					}
				}

				contentBlocks = append(contentBlocks, resultBlock)

			case schemas.ResponsesMessageTypeReasoning:
				// Build thinking from ResponsesReasoning summary, else from reasoning content blocks
				var thinking string
				var signature *string
				if msg.ResponsesReasoning != nil && msg.ResponsesReasoning.Summary != nil {
					for _, b := range msg.ResponsesReasoning.Summary {
						thinking += b.Text
					}
					signature = msg.ResponsesReasoning.EncryptedContent
				} else if msg.Content != nil && msg.Content.ContentBlocks != nil {
					for _, b := range msg.Content.ContentBlocks {
						if b.Type == schemas.ResponsesOutputMessageContentTypeReasoning && b.Text != nil {
							thinking += *b.Text
						}
					}
				}
				if thinking != "" {
					contentBlocks = append(contentBlocks, AnthropicContentBlock{
						Type:      AnthropicContentBlockTypeThinking,
						Thinking:  &thinking,
						Signature: signature,
					})
				}

			case schemas.ResponsesMessageTypeComputerCall:
				if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.CallID != nil {
					toolBlock := AnthropicContentBlock{
						Type: AnthropicContentBlockTypeToolUse,
						ID:   msg.ResponsesToolMessage.CallID,
						Name: schemas.Ptr(string(AnthropicToolNameComputer)),
					}

					// Convert computer action to Anthropic input format
					if msg.ResponsesToolMessage.Action != nil && msg.ResponsesToolMessage.Action.ResponsesComputerToolCallAction != nil {
						toolBlock.Input = convertResponsesToAnthropicComputerAction(msg.ResponsesToolMessage.Action.ResponsesComputerToolCallAction)
					}
					contentBlocks = append(contentBlocks, toolBlock)
				}

			case schemas.ResponsesMessageTypeMCPCall:
				// Check if this is a tool use (from assistant) or tool result (from user)
				// Tool use: has Name and Arguments but no Output
				// Tool result: has CallID and Output
				if msg.ResponsesToolMessage != nil {
					if msg.ResponsesToolMessage.Name != nil {
						// This is a tool use call (assistant calling a tool)
						toolUseBlock := AnthropicContentBlock{
							Type: AnthropicContentBlockTypeMCPToolUse,
						}

						if msg.ID != nil {
							toolUseBlock.ID = msg.ID
						}

						if msg.ResponsesToolMessage.Name != nil {
							toolUseBlock.Name = msg.ResponsesToolMessage.Name
						}

						// Set server name if present
						if msg.ResponsesToolMessage.ResponsesMCPToolCall != nil && msg.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel != "" {
							toolUseBlock.ServerName = &msg.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel
						}

						// Parse arguments as JSON input
						if msg.ResponsesToolMessage.Arguments != nil && *msg.ResponsesToolMessage.Arguments != "" {
							toolUseBlock.Input = parseJSONInput(*msg.ResponsesToolMessage.Arguments)
						}

						contentBlocks = append(contentBlocks, toolUseBlock)
					} else if msg.ResponsesToolMessage.CallID != nil {
						// This is a tool result (user providing result of tool execution)
						resultBlock := AnthropicContentBlock{
							Type:      AnthropicContentBlockTypeMCPToolResult,
							ToolUseID: msg.ResponsesToolMessage.CallID,
						}

						if msg.ResponsesToolMessage.Output != nil {
							resultBlock.Content = convertToolOutputToAnthropicContent(msg.ResponsesToolMessage.Output)
						}

						contentBlocks = append(contentBlocks, resultBlock)
					}
				}

			case schemas.ResponsesMessageTypeMCPApprovalRequest:
				// MCP approval request is OpenAI-specific for human-in-the-loop workflows
				// Convert to Anthropic's mcp_tool_use format (same as regular MCP calls)
				if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Name != nil {
					toolUseBlock := AnthropicContentBlock{
						Type: AnthropicContentBlockTypeMCPToolUse,
					}

					if msg.ID != nil {
						toolUseBlock.ID = msg.ID
					}
					toolUseBlock.Name = msg.ResponsesToolMessage.Name

					// Set server name if present
					if msg.ResponsesToolMessage.ResponsesMCPToolCall != nil && msg.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel != "" {
						toolUseBlock.ServerName = &msg.ResponsesToolMessage.ResponsesMCPToolCall.ServerLabel
					}

					// Parse arguments as JSON input
					if msg.ResponsesToolMessage.Arguments != nil && *msg.ResponsesToolMessage.Arguments != "" {
						toolUseBlock.Input = parseJSONInput(*msg.ResponsesToolMessage.Arguments)
					}

					contentBlocks = append(contentBlocks, toolUseBlock)
				}

			default:
				// Handle other types as text if they have content
				if msg.Content != nil && msg.Content.ContentStr != nil {
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

// Helper to convert Bifrost content blocks slice to Anthropic content blocks
func convertBifrostContentBlocksToAnthropic(blocks []schemas.ResponsesMessageContentBlock) []AnthropicContentBlock {
	if len(blocks) == 0 {
		return nil
	}
	var result []AnthropicContentBlock
	for _, block := range blocks {
		if converted := convertContentBlockToAnthropic(block); converted != nil {
			result = append(result, *converted)
		}
	}
	if len(result) > 0 {
		return result
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

// Helper functions for MCP tool/server conversion
// convertAnthropicMCPServerToBifrostTool converts a single Anthropic MCP server to a Bifrost ResponsesTool
func convertAnthropicMCPServerToBifrostTool(mcpServer *AnthropicMCPServer) *schemas.ResponsesTool {
	if mcpServer == nil {
		return nil
	}

	bifrostTool := &schemas.ResponsesTool{
		Type: schemas.ResponsesToolTypeMCP,
		ResponsesToolMCP: &schemas.ResponsesToolMCP{
			ServerLabel: mcpServer.Name,
		},
	}

	// Set server URL if present
	if mcpServer.URL != "" {
		bifrostTool.ResponsesToolMCP.ServerURL = schemas.Ptr(mcpServer.URL)
	}

	// Set authorization token if present
	if mcpServer.AuthorizationToken != nil {
		bifrostTool.ResponsesToolMCP.Authorization = mcpServer.AuthorizationToken
	}

	// Set allowed tools from tool configuration
	if mcpServer.ToolConfiguration != nil && len(mcpServer.ToolConfiguration.AllowedTools) > 0 {
		bifrostTool.ResponsesToolMCP.AllowedTools = &schemas.ResponsesToolMCPAllowedTools{
			ToolNames: mcpServer.ToolConfiguration.AllowedTools,
		}
	}

	return bifrostTool
}

// convertBifrostMCPToolToAnthropicServer converts a Bifrost MCP tool back to an Anthropic MCP server
func convertBifrostMCPToolToAnthropicServer(tool *schemas.ResponsesTool) *AnthropicMCPServer {
	if tool == nil || tool.Type != schemas.ResponsesToolTypeMCP || tool.ResponsesToolMCP == nil {
		return nil
	}

	mcpServer := &AnthropicMCPServer{
		Type: "url",
		Name: tool.ResponsesToolMCP.ServerLabel,
		ToolConfiguration: &AnthropicMCPToolConfig{
			Enabled: true,
		},
	}

	// Set server URL if present
	if tool.ResponsesToolMCP.ServerURL != nil {
		mcpServer.URL = *tool.ResponsesToolMCP.ServerURL
	}

	// Set allowed tools if present
	if tool.ResponsesToolMCP.AllowedTools != nil && len(tool.ResponsesToolMCP.AllowedTools.ToolNames) > 0 {
		mcpServer.ToolConfiguration.AllowedTools = tool.ResponsesToolMCP.AllowedTools.ToolNames
	}

	// Set authorization token if present
	if tool.ResponsesToolMCP.Authorization != nil {
		mcpServer.AuthorizationToken = tool.ResponsesToolMCP.Authorization
	}

	return mcpServer
}

// convertResponsesToAnthropicComputerAction converts ResponsesComputerToolCallAction to Anthropic input map
func convertResponsesToAnthropicComputerAction(action *schemas.ResponsesComputerToolCallAction) map[string]any {
	input := map[string]any{}
	var actionStr string

	// Map action type from OpenAI to Anthropic format
	switch action.Type {
	case "screenshot":
		actionStr = "screenshot"

	case "click":
		// Map click with button variants
		if action.Button != nil {
			switch *action.Button {
			case "right":
				actionStr = "right_click"
			case "wheel":
				actionStr = "middle_click"
			default: // "left", "back", "forward" or others
				actionStr = "left_click"
			}
		} else {
			actionStr = "left_click"
		}
		// Add coordinates
		if action.X != nil && action.Y != nil {
			input["coordinate"] = []int{*action.X, *action.Y}
		}

	case "double_click":
		actionStr = "double_click"
		if action.X != nil && action.Y != nil {
			input["coordinate"] = []int{*action.X, *action.Y}
		}

	case "move":
		actionStr = "mouse_move"
		if action.X != nil && action.Y != nil {
			input["coordinate"] = []int{*action.X, *action.Y}
		}

	case "type":
		actionStr = "type"
		if action.Text != nil {
			input["text"] = *action.Text
		}

	case "keypress":
		actionStr = "key"
		if len(action.Keys) > 0 {
			// Convert array of keys to "key1+key2+..." format
			text := ""
			for i, key := range action.Keys {
				if i > 0 {
					text += "+"
				}
				text += key
			}
			input["text"] = text
		}

	case "scroll":
		actionStr = "scroll"
		if action.X != nil && action.Y != nil {
			input["coordinate"] = []int{*action.X, *action.Y}
		}

		// Handle scroll direction - Anthropic supports one direction at a time
		// If both ScrollX and ScrollY are present, use the one with larger absolute value
		scrollX := 0
		scrollY := 0
		if action.ScrollX != nil {
			scrollX = *action.ScrollX
		}
		if action.ScrollY != nil {
			scrollY = *action.ScrollY
		}

		if math.Abs(float64(scrollY)) >= math.Abs(float64(scrollX)) && scrollY != 0 {
			// Vertical scroll is dominant or only one present
			if scrollY > 0 {
				input["scroll_direction"] = "down"
				input["scroll_amount"] = scrollY / 100
			} else {
				input["scroll_direction"] = "up"
				input["scroll_amount"] = (-scrollY) / 100
			}
		} else if scrollX != 0 {
			// Horizontal scroll is dominant or only one present
			if scrollX > 0 {
				input["scroll_direction"] = "right"
				input["scroll_amount"] = scrollX / 100
			} else {
				input["scroll_direction"] = "left"
				input["scroll_amount"] = (-scrollX) / 100
			}
		}

	case "drag":
		actionStr = "left_click_drag"
		if len(action.Path) >= 2 {
			// Map first and last points as start and end coordinates
			input["start_coordinate"] = []int{action.Path[0].X, action.Path[0].Y}
			input["end_coordinate"] = []int{action.Path[len(action.Path)-1].X, action.Path[len(action.Path)-1].Y}
		}

	case "wait":
		actionStr = "wait"
		input["duration"] = 2

	default:
		// Pass through any unknown action types
		actionStr = action.Type
	}

	input["action"] = actionStr

	return input
}

// convertAnthropicToResponsesComputerAction converts Anthropic input map to ResponsesComputerToolCallAction
func convertAnthropicToResponsesComputerAction(inputMap map[string]interface{}) *schemas.ResponsesComputerToolCallAction {
	action := &schemas.ResponsesComputerToolCallAction{}

	// Extract action type
	actionStr, ok := inputMap["action"].(string)
	if !ok {
		return action
	}

	// Map action type from Anthropic to OpenAI format
	switch actionStr {
	case "screenshot":
		action.Type = "screenshot"

	case "left_click":
		action.Type = "click"
		action.Button = schemas.Ptr("left")

	case "right_click":
		action.Type = "click"
		action.Button = schemas.Ptr("right")

	case "middle_click":
		action.Type = "click"
		action.Button = schemas.Ptr("wheel")

	case "double_click":
		action.Type = "double_click"

	case "mouse_move":
		action.Type = "move"

	case "type":
		action.Type = "type"
		if text, ok := inputMap["text"].(string); ok {
			action.Text = schemas.Ptr(text)
		}

	case "key":
		action.Type = "keypress"
		if text, ok := inputMap["text"].(string); ok {
			// Convert "key1+key2+..." format to array of keys
			keys := strings.Split(text, "+")
			action.Keys = keys
		}

	case "scroll":
		action.Type = "scroll"
		// Convert scroll_direction and scroll_amount to pixel values
		if direction, ok := inputMap["scroll_direction"].(string); ok {
			amount := 100 // Default scroll amount in pixels
			if scrollAmount, ok := inputMap["scroll_amount"].(float64); ok {
				amount = int(scrollAmount) * 100 // Convert scroll units to pixels
			}
			switch direction {
			case "down":
				action.ScrollY = schemas.Ptr(amount)
				action.ScrollX = schemas.Ptr(0)
			case "up":
				action.ScrollY = schemas.Ptr(-amount)
				action.ScrollX = schemas.Ptr(0)
			case "right":
				action.ScrollX = schemas.Ptr(amount)
				action.ScrollY = schemas.Ptr(0)
			case "left":
				action.ScrollX = schemas.Ptr(-amount)
				action.ScrollY = schemas.Ptr(0)
			}
		}

	case "left_click_drag":
		action.Type = "drag"
		// Extract start and end coordinates
		if startCoord, ok := inputMap["start_coordinate"].([]interface{}); ok && len(startCoord) == 2 {
			if endCoord, ok := inputMap["end_coordinate"].([]interface{}); ok && len(endCoord) == 2 {
				// JSON unmarshaling produces float64 for numbers, so convert them
				startX, startXOk := startCoord[0].(float64)
				startY, startYOk := startCoord[1].(float64)
				endX, endXOk := endCoord[0].(float64)
				endY, endYOk := endCoord[1].(float64)
				if startXOk && startYOk && endXOk && endYOk {
					action.Path = []schemas.ResponsesComputerToolCallActionPath{
						{X: int(startX), Y: int(startY)},
						{X: int(endX), Y: int(endY)},
					}
				}
			}
		}

	case "wait":
		action.Type = "wait"

	default:
		// Pass through any unknown action types
		action.Type = actionStr
	}

	// Extract coordinates for all actions that use them (click, double_click, move, scroll, etc.)
	if coordinate, ok := inputMap["coordinate"].([]interface{}); ok && len(coordinate) == 2 {
		// JSON unmarshaling produces float64 for numbers, so convert them
		if x, xOk := coordinate[0].(float64); xOk {
			if y, yOk := coordinate[1].(float64); yOk {
				action.X = schemas.Ptr(int(x))
				action.Y = schemas.Ptr(int(y))
			}
		}
	}

	return action
}
