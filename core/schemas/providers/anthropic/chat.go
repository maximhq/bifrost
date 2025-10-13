package anthropic

import (
	"encoding/json"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostRequest converts an Anthropic messages request to Bifrost format
func (request *AnthropicMessageRequest) ToBifrostChatRequest() *schemas.BifrostChatRequest {
	provider, model := schemas.ParseModelString(request.Model, schemas.Anthropic)

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
	}

	messages := []schemas.ChatMessage{}

	// Add system message if present
	if request.System != nil {
		if request.System.ContentStr != nil && *request.System.ContentStr != "" {
			messages = append(messages, schemas.ChatMessage{
				Role: schemas.ChatMessageRoleSystem,
				Content: &schemas.ChatMessageContent{
					ContentStr: request.System.ContentStr,
				},
			})
		} else if request.System.ContentBlocks != nil {
			contentBlocks := []schemas.ChatContentBlock{}
			for _, block := range request.System.ContentBlocks {
				if block.Text != nil { // System messages will only have text content
					contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
						Type: schemas.ChatContentBlockTypeText,
						Text: block.Text,
					})
				}
			}
			messages = append(messages, schemas.ChatMessage{
				Role: schemas.ChatMessageRoleSystem,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: contentBlocks,
				},
			})
		}
	}

	// Convert messages
	for _, msg := range request.Messages {
		if msg.Content.ContentStr != nil {
			// Simple text message
			bifrostMsg := schemas.ChatMessage{
				Role: schemas.ChatMessageRole(msg.Role),
				Content: &schemas.ChatMessageContent{
					ContentStr: msg.Content.ContentStr,
				},
			}
			messages = append(messages, bifrostMsg)
		} else if msg.Content.ContentBlocks != nil {
			// Check if this is a user message with multiple tool results
			var toolResults []AnthropicContentBlock
			var nonToolContent []AnthropicContentBlock

			for _, content := range msg.Content.ContentBlocks {
				if content.Type == AnthropicContentBlockTypeToolResult {
					toolResults = append(toolResults, content)
				} else {
					nonToolContent = append(nonToolContent, content)
				}
			}

			// If we have tool results, create separate messages for each
			if len(toolResults) > 0 {
				for _, toolResult := range toolResults {
					if toolResult.ToolUseID != nil {
						var contentBlocks []schemas.ChatContentBlock

						// Convert tool result content
						if toolResult.Content.ContentStr != nil {
							contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
								Type: schemas.ChatContentBlockTypeText,
								Text: toolResult.Content.ContentStr,
							})
						} else if toolResult.Content.ContentBlocks != nil {
							for _, block := range toolResult.Content.ContentBlocks {
								if block.Text != nil {
									contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
										Type: schemas.ChatContentBlockTypeText,
										Text: block.Text,
									})
								} else if block.Source != nil {
									contentBlocks = append(contentBlocks, block.ToBifrostContentImageBlock())
								}
							}
						}

						toolMsg := schemas.ChatMessage{
							Role: schemas.ChatMessageRoleTool,
							ChatToolMessage: &schemas.ChatToolMessage{
								ToolCallID: toolResult.ToolUseID,
							},
							Content: &schemas.ChatMessageContent{
								ContentBlocks: contentBlocks,
							},
						}
						messages = append(messages, toolMsg)
					}
				}
			}

			// Handle non-tool content (regular user/assistant message)
			if len(nonToolContent) > 0 {
				var bifrostMsg schemas.ChatMessage
				bifrostMsg.Role = schemas.ChatMessageRole(msg.Role)

				var toolCalls []schemas.ChatAssistantMessageToolCall
				var contentBlocks []schemas.ChatContentBlock

				for _, content := range nonToolContent {
					switch content.Type {
					case AnthropicContentBlockTypeText:
						if content.Text != nil {
							contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
								Type: schemas.ChatContentBlockTypeText,
								Text: content.Text,
							})
						}
					case AnthropicContentBlockTypeImage:
						if content.Source != nil {
							contentBlocks = append(contentBlocks, content.ToBifrostContentImageBlock())
						}
					case AnthropicContentBlockTypeToolUse:
						if content.ID != nil && content.Name != nil {
							tc := schemas.ChatAssistantMessageToolCall{
								Type: schemas.Ptr(string(schemas.ChatToolChoiceTypeFunction)),
								ID:   content.ID,
								Function: schemas.ChatAssistantMessageToolCallFunction{
									Name:      content.Name,
									Arguments: schemas.JsonifyInput(content.Input),
								},
							}
							toolCalls = append(toolCalls, tc)
						}
					}
				}

				// Set content
				if len(contentBlocks) > 0 {
					bifrostMsg.Content = &schemas.ChatMessageContent{
						ContentBlocks: contentBlocks,
					}
				}

				// Set tool calls for assistant messages
				if len(toolCalls) > 0 && msg.Role == AnthropicMessageRoleAssistant {
					bifrostMsg.ChatAssistantMessage = &schemas.ChatAssistantMessage{
						ToolCalls: toolCalls,
					}
				}

				messages = append(messages, bifrostMsg)
			}
		}
	}

	bifrostReq.Input = messages

	// Convert parameters
	if request.MaxTokens > 0 || request.Temperature != nil || request.TopP != nil || request.TopK != nil || request.StopSequences != nil {
		params := &schemas.ChatParameters{
			ExtraParams: make(map[string]interface{}),
		}

		if request.MaxTokens > 0 {
			params.MaxCompletionTokens = &request.MaxTokens
		}
		if request.Temperature != nil {
			params.Temperature = request.Temperature
		}
		if request.TopP != nil {
			params.TopP = request.TopP
		}
		if request.TopK != nil {
			params.ExtraParams["top_k"] = *request.TopK
		}
		if request.StopSequences != nil {
			params.Stop = request.StopSequences
		}

		bifrostReq.Params = params
	}

	// Convert tools
	if request.Tools != nil {
		tools := []schemas.ChatTool{}
		for _, tool := range request.Tools {
			// Convert input_schema to FunctionParameters
			params := schemas.ToolFunctionParameters{
				Type: "object",
			}
			if tool.InputSchema != nil {
				params.Type = tool.InputSchema.Type
				params.Required = tool.InputSchema.Required
				params.Properties = tool.InputSchema.Properties
			}

			tools = append(tools, schemas.ChatTool{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        tool.Name,
					Description: schemas.Ptr(tool.Description),
					Parameters:  &params,
				},
			})
		}
		if bifrostReq.Params == nil {
			bifrostReq.Params = &schemas.ChatParameters{}
		}
		bifrostReq.Params.Tools = tools
	}

	// Convert tool choice
	if request.ToolChoice != nil {
		if bifrostReq.Params == nil {
			bifrostReq.Params = &schemas.ChatParameters{}
		}
		toolChoice := &schemas.ChatToolChoice{
			ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
				Type: func() schemas.ChatToolChoiceType {
					if request.ToolChoice.Type == "tool" {
						return schemas.ChatToolChoiceTypeFunction
					}
					return schemas.ChatToolChoiceType(request.ToolChoice.Type)
				}(),
			},
		}
		if request.ToolChoice.Type == "tool" && request.ToolChoice.Name != "" {
			toolChoice.ChatToolChoiceStruct.Function = schemas.ChatToolChoiceFunction{
				Name: request.ToolChoice.Name,
			}
		}
		bifrostReq.Params.ToolChoice = toolChoice
	}

	return bifrostReq
}

// ToBifrostResponse converts an Anthropic message response to Bifrost format
func (response *AnthropicMessageResponse) ToBifrostChatResponse() *schemas.BifrostChatResponse {
	if response == nil {
		return nil
	}

	// Initialize Bifrost response
	bifrostResponse := &schemas.BifrostChatResponse{
		ID:    response.ID,
		Model: response.Model,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.ChatCompletionRequest,
			Provider:    schemas.Anthropic,
		},
	}

	// Collect all content and tool calls into a single message
	var toolCalls []schemas.ChatAssistantMessageToolCall
	var contentBlocks []schemas.ChatContentBlock

	// Process content and tool calls
	if response.Content != nil {
		for _, c := range response.Content {
			switch c.Type {
			case AnthropicContentBlockTypeText:
				if c.Text != nil {
					contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
						Type: schemas.ChatContentBlockTypeText,
						Text: c.Text,
					})
				}
			case AnthropicContentBlockTypeToolUse:
				if c.ID != nil && c.Name != nil {
					function := schemas.ChatAssistantMessageToolCallFunction{
						Name: c.Name,
					}

					// Marshal the input to JSON string
					if c.Input != nil {
						args, err := json.Marshal(c.Input)
						if err != nil {
							function.Arguments = fmt.Sprintf("%v", c.Input)
						} else {
							function.Arguments = string(args)
						}
					} else {
						function.Arguments = "{}"
					}

					toolCalls = append(toolCalls, schemas.ChatAssistantMessageToolCall{
						Type:     schemas.Ptr(string(schemas.ChatToolTypeFunction)),
						ID:       c.ID,
						Function: function,
					})
				}
			}
		}
	}

	// Create the assistant message
	var assistantMessage *schemas.ChatAssistantMessage

	// Create AssistantMessage if we have tool calls or thinking
	if len(toolCalls) > 0 {
		assistantMessage = &schemas.ChatAssistantMessage{
			ToolCalls: toolCalls,
		}
	}

	// Create a single choice with the collected content
	// Create message content
	messageContent := schemas.ChatMessageContent{
		ContentBlocks: contentBlocks,
	}

	// Create message
	message := schemas.ChatMessage{
		Role:                 schemas.ChatMessageRoleAssistant,
		Content:              &messageContent,
		ChatAssistantMessage: assistantMessage,
	}

	// Create choice
	choice := schemas.BifrostResponseChoice{
		Index: 0,
		ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
			Message:    &message,
			StopString: response.StopSequence,
		},
		FinishReason: func() *string {
			if response.StopReason != nil && *response.StopReason != "" {
				mapped := schemas.MapProviderFinishReasonToBifrost(*response.StopReason, schemas.Anthropic)
				return &mapped
			}
			return nil
		}(),
	}

	bifrostResponse.Choices = []schemas.BifrostResponseChoice{choice}

	// Convert usage information
	if response.Usage != nil {
		bifrostResponse.Usage = &schemas.BifrostLLMUsage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.InputTokens + response.Usage.OutputTokens,
		}
	}

	return bifrostResponse
}

// ToAnthropicChatCompletionRequest converts a Bifrost request to Anthropic format
// This is the reverse of ConvertChatRequestToBifrost for provider-side usage
func ToAnthropicChatCompletionRequest(bifrostReq *schemas.BifrostChatRequest) *AnthropicMessageRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	messages := bifrostReq.Input
	anthropicReq := &AnthropicMessageRequest{
		Model:     bifrostReq.Model,
		MaxTokens: AnthropicDefaultMaxTokens,
	}

	// Convert parameters
	if bifrostReq.Params != nil {
		if bifrostReq.Params.MaxCompletionTokens != nil {
			anthropicReq.MaxTokens = *bifrostReq.Params.MaxCompletionTokens
		}

		anthropicReq.Temperature = bifrostReq.Params.Temperature
		anthropicReq.TopP = bifrostReq.Params.TopP
		anthropicReq.StopSequences = bifrostReq.Params.Stop
		topK, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["top_k"])
		if ok {
			anthropicReq.TopK = topK
		}

		// Convert tools
		if bifrostReq.Params.Tools != nil {
			tools := make([]AnthropicTool, 0, len(bifrostReq.Params.Tools))
			for _, tool := range bifrostReq.Params.Tools {
				if tool.Function == nil {
					continue
				}
				anthropicTool := AnthropicTool{
					Name: tool.Function.Name,
				}
				if tool.Function.Description != nil {
					anthropicTool.Description = *tool.Function.Description
				}

				// Convert function parameters to input_schema
				if tool.Function.Parameters != nil && (tool.Function.Parameters.Type != "" || tool.Function.Parameters.Properties != nil) {
					anthropicTool.InputSchema = &schemas.ToolFunctionParameters{
						Type:       tool.Function.Parameters.Type,
						Properties: tool.Function.Parameters.Properties,
						Required:   tool.Function.Parameters.Required,
					}
				}

				tools = append(tools, anthropicTool)
			}
			anthropicReq.Tools = tools
		}

		// Convert tool choice
		if bifrostReq.Params.ToolChoice != nil {
			toolChoice := &AnthropicToolChoice{}
			if bifrostReq.Params.ToolChoice.ChatToolChoiceStr != nil {
				switch schemas.ChatToolChoiceType(*bifrostReq.Params.ToolChoice.ChatToolChoiceStr) {
				case schemas.ChatToolChoiceTypeAny:
					toolChoice.Type = "any"
				case schemas.ChatToolChoiceTypeRequired:
					toolChoice.Type = "any"
				case schemas.ChatToolChoiceTypeNone:
					toolChoice.Type = "none"
				default:
					toolChoice.Type = "auto"
				}
			} else if bifrostReq.Params.ToolChoice.ChatToolChoiceStruct != nil {
				switch bifrostReq.Params.ToolChoice.ChatToolChoiceStruct.Type {
				case schemas.ChatToolChoiceTypeFunction:
					toolChoice.Type = "tool"
					toolChoice.Name = bifrostReq.Params.ToolChoice.ChatToolChoiceStruct.Function.Name
				case schemas.ChatToolChoiceTypeAllowedTools:
					toolChoice.Type = "any"
				case schemas.ChatToolChoiceTypeCustom:
					toolChoice.Type = "auto"
				default:
					toolChoice.Type = "auto"
				}
			}
			anthropicReq.ToolChoice = toolChoice
		}
	}

	// Convert messages - group consecutive tool messages into single user messages
	var anthropicMessages []AnthropicMessage
	var systemContent *AnthropicContent

	i := 0
	for i < len(messages) {
		msg := messages[i]

		switch msg.Role {
		case schemas.ChatMessageRoleSystem:
			// Handle system message separately
			if msg.Content != nil {
				if msg.Content.ContentStr != nil {
					systemContent = &AnthropicContent{ContentStr: msg.Content.ContentStr}
				} else if msg.Content.ContentBlocks != nil {
					blocks := make([]AnthropicContentBlock, 0, len(msg.Content.ContentBlocks))
					for _, block := range msg.Content.ContentBlocks {
						if block.Text != nil {
							blocks = append(blocks, AnthropicContentBlock{
								Type: "text",
								Text: block.Text,
							})
						}
					}
					if len(blocks) > 0 {
						systemContent = &AnthropicContent{ContentBlocks: blocks}
					}
				}
			}
			i++

		case schemas.ChatMessageRoleTool:
			// Group consecutive tool messages into a single user message
			var toolResults []AnthropicContentBlock

			// Collect all consecutive tool messages
			for i < len(messages) && messages[i].Role == schemas.ChatMessageRoleTool {
				toolMsg := messages[i]
				if toolMsg.ChatToolMessage != nil && toolMsg.ChatToolMessage.ToolCallID != nil {
					toolResult := AnthropicContentBlock{
						Type:      "tool_result",
						ToolUseID: toolMsg.ChatToolMessage.ToolCallID,
					}

					// Convert tool result content
					if toolMsg.Content != nil {
						if toolMsg.Content.ContentStr != nil {
							toolResult.Content = &AnthropicContent{ContentStr: toolMsg.Content.ContentStr}
						} else if toolMsg.Content.ContentBlocks != nil {
							blocks := make([]AnthropicContentBlock, 0, len(toolMsg.Content.ContentBlocks))
							for _, block := range toolMsg.Content.ContentBlocks {
								if block.Text != nil {
									blocks = append(blocks, AnthropicContentBlock{
										Type: "text",
										Text: block.Text,
									})
								} else if block.ImageURLStruct != nil {
									blocks = append(blocks, ConvertToAnthropicImageBlock(block))
								}
							}
							if len(blocks) > 0 {
								toolResult.Content = &AnthropicContent{ContentBlocks: blocks}
							}
						}
					}

					toolResults = append(toolResults, toolResult)
				}
				i++
			}

			// Create a single user message with all tool results
			if len(toolResults) > 0 {
				anthropicMessages = append(anthropicMessages, AnthropicMessage{
					Role:    "user", // Tool results are sent as user messages in Anthropic
					Content: AnthropicContent{ContentBlocks: toolResults},
				})
			}

		default:
			// Handle user and assistant messages
			anthropicMsg := AnthropicMessage{
				Role: AnthropicMessageRole(msg.Role),
			}

			var content []AnthropicContentBlock

			if msg.Content != nil {
				// Convert text content
				if msg.Content.ContentStr != nil {
					content = append(content, AnthropicContentBlock{
						Type: AnthropicContentBlockTypeText,
						Text: msg.Content.ContentStr,
					})
				} else if msg.Content.ContentBlocks != nil {
					for _, block := range msg.Content.ContentBlocks {
						if block.Text != nil {
							content = append(content, AnthropicContentBlock{
								Type: AnthropicContentBlockTypeText,
								Text: block.Text,
							})
						} else if block.ImageURLStruct != nil {
							content = append(content, ConvertToAnthropicImageBlock(block))
						}
					}
				}
			}

			// Convert tool calls
			if msg.ChatAssistantMessage != nil && msg.ChatAssistantMessage.ToolCalls != nil {
				for _, toolCall := range msg.ChatAssistantMessage.ToolCalls {
					toolUse := AnthropicContentBlock{
						Type: AnthropicContentBlockTypeToolUse,
						ID:   toolCall.ID,
						Name: toolCall.Function.Name,
					}

					// Parse arguments JSON to interface{}
					if toolCall.Function.Arguments != "" {
						var input interface{}
						if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &input); err == nil {
							toolUse.Input = input
						}
					}

					content = append(content, toolUse)
				}
			}

			// Set content
			if len(content) == 1 && content[0].Type == AnthropicContentBlockTypeText {
				// Single text content can be string
				anthropicMsg.Content = AnthropicContent{ContentStr: content[0].Text}
			} else if len(content) > 0 {
				// Multiple content blocks
				anthropicMsg.Content = AnthropicContent{ContentBlocks: content}
			}

			anthropicMessages = append(anthropicMessages, anthropicMsg)
			i++
		}
	}

	anthropicReq.Messages = anthropicMessages
	anthropicReq.System = systemContent

	return anthropicReq
}

// ToAnthropicChatCompletionResponse converts a Bifrost response to Anthropic format
func ToAnthropicChatCompletionResponse(bifrostResp *schemas.BifrostChatResponse) *AnthropicMessageResponse {
	if bifrostResp == nil {
		return nil
	}

	anthropicResp := &AnthropicMessageResponse{
		ID:    bifrostResp.ID,
		Type:  "message",
		Role:  string(schemas.ChatMessageRoleAssistant),
		Model: bifrostResp.Model,
	}

	// Convert usage information
	if bifrostResp.Usage != nil {
		anthropicResp.Usage = &AnthropicUsage{
			InputTokens:  bifrostResp.Usage.PromptTokens,
			OutputTokens: bifrostResp.Usage.CompletionTokens,
		}
	}

	// Convert choices to content
	var content []AnthropicContentBlock
	if len(bifrostResp.Choices) > 0 {
		choice := bifrostResp.Choices[0] // Anthropic typically returns one choice

		if choice.FinishReason != nil {
			mappedReason := schemas.MapBifrostFinishReasonToProvider(*choice.FinishReason, schemas.Anthropic)
			anthropicResp.StopReason = &mappedReason
		}
		if choice.StopString != nil {
			anthropicResp.StopSequence = choice.StopString
		}

		// Add text content
		if choice.Message.Content.ContentStr != nil && *choice.Message.Content.ContentStr != "" {
			content = append(content, AnthropicContentBlock{
				Type: AnthropicContentBlockTypeText,
				Text: choice.Message.Content.ContentStr,
			})
		} else if choice.Message.Content.ContentBlocks != nil {
			for _, block := range choice.Message.Content.ContentBlocks {
				if block.Text != nil {
					content = append(content, AnthropicContentBlock{
						Type: AnthropicContentBlockTypeText,
						Text: block.Text,
					})
				}
			}
		}

		// Add tool calls as tool_use content
		if choice.Message.ChatAssistantMessage != nil && choice.Message.ChatAssistantMessage.ToolCalls != nil {
			for _, toolCall := range choice.Message.ChatAssistantMessage.ToolCalls {
				// Parse arguments JSON string back to map
				var input map[string]interface{}
				if toolCall.Function.Arguments != "" {
					if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &input); err != nil {
						input = map[string]interface{}{}
					}
				} else {
					input = map[string]interface{}{}
				}

				content = append(content, AnthropicContentBlock{
					Type:  AnthropicContentBlockTypeToolUse,
					ID:    toolCall.ID,
					Name:  toolCall.Function.Name,
					Input: input,
				})
			}
		}
	}

	if content == nil {
		content = []AnthropicContentBlock{}
	}

	anthropicResp.Content = content
	return anthropicResp
}

// ToBifrostChatCompletionStream converts an Anthropic stream event to a Bifrost Chat Completion Stream response
func (chunk *AnthropicStreamEvent) ToBifrostChatCompletionStream() (*schemas.BifrostChatResponse, *schemas.BifrostError, bool) {
	switch chunk.Type {
	case AnthropicStreamEventTypeMessageStart:
		return nil, nil, false

	case AnthropicStreamEventTypeMessageStop:
		return nil, nil, true

	case AnthropicStreamEventTypeContentBlockStart:
		// Emit tool-call metadata when starting a tool_use content block
		if chunk.Index != nil && chunk.ContentBlock != nil && chunk.ContentBlock.Type == AnthropicContentBlockTypeToolUse {
			// Create streaming response with tool call metadata
			streamResponse := &schemas.BifrostChatResponse{
				Object: "chat.completion.chunk",
				Choices: []schemas.BifrostResponseChoice{
					{
						Index: *chunk.Index,
						ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
							Delta: &schemas.ChatStreamResponseChoiceDelta{
								ToolCalls: []schemas.ChatAssistantMessageToolCall{
									{
										Type: schemas.Ptr(string(schemas.ChatToolTypeFunction)),
										ID:   chunk.ContentBlock.ToolUseID,
										Function: schemas.ChatAssistantMessageToolCallFunction{
											Name:      chunk.ContentBlock.Name,
											Arguments: "", // Empty arguments initially, will be filled by subsequent deltas
										},
									},
								},
							},
						},
					},
				},
			}

			return streamResponse, nil, false
		}

		return nil, nil, false

	case AnthropicStreamEventTypeContentBlockDelta:
		if chunk.Index != nil && chunk.Delta != nil {
			// Handle different delta types
			switch chunk.Delta.Type {
			case AnthropicStreamDeltaTypeText:
				if chunk.Delta.Text != nil && *chunk.Delta.Text != "" {
					// Create streaming response for this delta
					streamResponse := &schemas.BifrostChatResponse{
						Object: "chat.completion.chunk",
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: *chunk.Index,
								ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
									Delta: &schemas.ChatStreamResponseChoiceDelta{
										Content: chunk.Delta.Text,
									},
								},
							},
						},
					}

					return streamResponse, nil, false
				}

			case AnthropicStreamDeltaTypeInputJSON:
				// Handle tool use streaming - accumulate partial JSON
				if chunk.Delta.PartialJSON != nil && *chunk.Delta.PartialJSON != "" {
					// Create streaming response for tool input delta
					streamResponse := &schemas.BifrostChatResponse{
						Object: "chat.completion.chunk",
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: *chunk.Index,
								ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
									Delta: &schemas.ChatStreamResponseChoiceDelta{
										ToolCalls: []schemas.ChatAssistantMessageToolCall{
											{
												Type: func() *string { s := "function"; return &s }(),
												Function: schemas.ChatAssistantMessageToolCallFunction{
													Arguments: *chunk.Delta.PartialJSON,
												},
											},
										},
									},
								},
							},
						},
					}

					return streamResponse, nil, false
				}

			case AnthropicStreamDeltaTypeThinking:
				// Handle thinking content streaming
				if chunk.Delta.Thinking != nil && *chunk.Delta.Thinking != "" {
					// Create streaming response for thinking delta
					streamResponse := &schemas.BifrostChatResponse{
						Object: "chat.completion.chunk",
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: *chunk.Index,
								ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
									Delta: &schemas.ChatStreamResponseChoiceDelta{
										Thought: chunk.Delta.Thinking,
									},
								},
							},
						},
					}

					return streamResponse, nil, false
				}

			case AnthropicStreamDeltaTypeSignature:
				// Handle signature verification for thinking content
				// This is used to verify the integrity of thinking content

			}
		}

	case AnthropicStreamEventTypeContentBlockStop:
		// Content block is complete, no specific action needed for streaming
		return nil, nil, false

	case AnthropicStreamEventTypeMessageDelta:
		return nil, nil, false

	case AnthropicStreamEventTypePing:
		// Ping events are just keepalive, no action needed
		return nil, nil, false

	case AnthropicStreamEventTypeError:
		if chunk.Error != nil {
			// Send error through channel before closing
			bifrostErr := &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    &chunk.Error.Type,
					Message: chunk.Error.Message,
				},
			}

			return nil, bifrostErr, true
		}
	}

	return nil, nil, false
}

// ToAnthropicChatCompletionStreamResponse converts a Bifrost streaming response to Anthropic SSE string format
func ToAnthropicChatCompletionStreamResponse(bifrostResp *schemas.BifrostChatResponse) string {
	if bifrostResp == nil {
		return ""
	}

	streamResp := &AnthropicStreamEvent{}

	// Handle different streaming event types based on the response content
	if len(bifrostResp.Choices) > 0 {
		choice := bifrostResp.Choices[0] // Anthropic typically returns one choice

		// Handle streaming responses
		if choice.ChatStreamResponseChoice != nil {
			delta := choice.ChatStreamResponseChoice.Delta

			// Handle text content deltas
			if delta.Content != nil {
				streamResp.Type = "content_block_delta"
				streamResp.Index = &choice.Index
				streamResp.Delta = &AnthropicStreamDelta{
					Type: AnthropicStreamDeltaTypeText,
					Text: delta.Content,
				}
			} else if delta.Thought != nil {
				// Handle thinking content deltas
				streamResp.Type = "content_block_delta"
				streamResp.Index = &choice.Index
				streamResp.Delta = &AnthropicStreamDelta{
					Type:     AnthropicStreamDeltaTypeThinking,
					Thinking: delta.Thought,
				}
			} else if len(delta.ToolCalls) > 0 {
				// Handle tool call deltas
				toolCall := delta.ToolCalls[0] // Take first tool call

				if toolCall.Function.Name != nil && *toolCall.Function.Name != "" {
					// Tool use start event
					streamResp.Type = "content_block_start"
					streamResp.Index = &choice.Index
					streamResp.ContentBlock = &AnthropicContentBlock{
						Type: AnthropicContentBlockTypeToolUse,
						ID:   toolCall.ID,
						Name: toolCall.Function.Name,
					}
				} else if toolCall.Function.Arguments != "" {
					// Tool input delta
					streamResp.Type = "content_block_delta"
					streamResp.Index = &choice.Index
					streamResp.Delta = &AnthropicStreamDelta{
						Type:        AnthropicStreamDeltaTypeInputJSON,
						PartialJSON: &toolCall.Function.Arguments,
					}
				}
			} else if choice.FinishReason != nil && *choice.FinishReason != "" {
				// Handle finish reason - map back to Anthropic format
				stopReason := schemas.MapBifrostFinishReasonToProvider(*choice.FinishReason, schemas.Anthropic)
				streamResp.Type = "message_delta"
				streamResp.Delta = &AnthropicStreamDelta{
					Type:       "message_delta",
					StopReason: &stopReason,
				}
			}

		} else if choice.ChatNonStreamResponseChoice != nil {
			// Handle non-streaming response converted to streaming format
			streamResp.Type = "message_start"

			// Create message start event
			streamMessage := &AnthropicMessageResponse{
				ID:    bifrostResp.ID,
				Type:  "message",
				Role:  string(choice.ChatNonStreamResponseChoice.Message.Role),
				Model: bifrostResp.Model,
			}

			// Convert content
			var content []AnthropicContentBlock
			if choice.ChatNonStreamResponseChoice.Message.Content.ContentStr != nil {
				content = append(content, AnthropicContentBlock{
					Type: AnthropicContentBlockTypeText,
					Text: choice.ChatNonStreamResponseChoice.Message.Content.ContentStr,
				})
			}

			streamMessage.Content = content
			streamResp.Message = streamMessage
		}
	}

	// Handle usage information
	if bifrostResp.Usage != nil {
		if streamResp.Type == "" {
			streamResp.Type = "message_delta"
		}
		streamResp.Usage = &AnthropicUsage{
			InputTokens:  bifrostResp.Usage.PromptTokens,
			OutputTokens: bifrostResp.Usage.CompletionTokens,
		}
	}

	// Set common fields
	if bifrostResp.ID != "" {
		streamResp.ID = &bifrostResp.ID
	}
	if bifrostResp.Model != "" {
		if streamResp.Message == nil {
			streamResp.Message = &AnthropicMessageResponse{}
		}
		streamResp.Message.Model = bifrostResp.Model
	}

	// Default to empty content_block_delta if no specific type was set
	if streamResp.Type == "" {
		streamResp.Type = "content_block_delta"
		streamResp.Index = schemas.Ptr(0)
		streamResp.Delta = &AnthropicStreamDelta{
			Type: AnthropicStreamDeltaTypeText,
			Text: schemas.Ptr(""),
		}
	}

	// Marshal to JSON and format as SSE
	jsonData, err := json.Marshal(streamResp)
	if err != nil {
		return ""
	}

	// Format as Anthropic SSE
	return fmt.Sprintf("event: %s\ndata: %s\n\n", streamResp.Type, jsonData)
}

// ToAnthropicChatCompletionStreamError converts a BifrostError to Anthropic streaming error in SSE format
func ToAnthropicChatCompletionStreamError(bifrostErr *schemas.BifrostError) string {
	errorResp := ToAnthropicChatCompletionError(bifrostErr)
	if errorResp == nil {
		return ""
	}
	// Marshal to JSON
	jsonData, err := json.Marshal(errorResp)
	if err != nil {
		return ""
	}
	// Format as Anthropic SSE error event
	return fmt.Sprintf("event: error\ndata: %s\n\n", jsonData)
}

// ToAnthropicChatCompletionError converts a BifrostError to AnthropicMessageError
func ToAnthropicChatCompletionError(bifrostErr *schemas.BifrostError) *AnthropicMessageError {
	if bifrostErr == nil {
		return nil
	}

	// Provide blank strings for nil pointer fields
	errorType := ""
	if bifrostErr.Type != nil {
		errorType = *bifrostErr.Type
	}

	// Handle nested error fields with nil checks
	errorStruct := AnthropicMessageErrorStruct{
		Type:    errorType,
		Message: bifrostErr.Error.Message,
	}

	return &AnthropicMessageError{
		Type:  "error", // always "error" for Anthropic
		Error: errorStruct,
	}
}
