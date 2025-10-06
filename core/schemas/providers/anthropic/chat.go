package anthropic

import (
	"encoding/json"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

var fnTypePtr = schemas.Ptr(string(schemas.ChatToolChoiceTypeFunction))

// ToBifrostRequest converts an Anthropic messages request to Bifrost format
func (mr *AnthropicMessageRequest) ToBifrostRequest() *schemas.BifrostChatRequest {
	provider, model := schemas.ParseModelString(mr.Model, schemas.Anthropic)

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
	}

	messages := []schemas.ChatMessage{}

	// Add system message if present
	if mr.System != nil {
		if mr.System.ContentStr != nil && *mr.System.ContentStr != "" {
			messages = append(messages, schemas.ChatMessage{
				Role: schemas.ChatMessageRoleSystem,
				Content: schemas.ChatMessageContent{
					ContentStr: mr.System.ContentStr,
				},
			})
		} else if mr.System.ContentBlocks != nil {
			contentBlocks := []schemas.ChatContentBlock{}
			for _, block := range mr.System.ContentBlocks {
				if block.Text != nil { // System messages will only have text content
					contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
						Type: schemas.ChatContentBlockTypeText,
						Text: block.Text,
					})
				}
			}
			messages = append(messages, schemas.ChatMessage{
				Role: schemas.ChatMessageRoleSystem,
				Content: schemas.ChatMessageContent{
					ContentBlocks: contentBlocks,
				},
			})
		}
	}

	// Convert messages
	for _, msg := range mr.Messages {
		var bifrostMsg schemas.ChatMessage
		bifrostMsg.Role = schemas.ChatMessageRole(msg.Role)

		if msg.Content.ContentStr != nil {
			bifrostMsg.Content = schemas.ChatMessageContent{
				ContentStr: msg.Content.ContentStr,
			}
		} else if msg.Content.ContentBlocks != nil {
			// Handle different content types
			var toolCalls []schemas.ChatAssistantMessageToolCall
			var contentBlocks []schemas.ChatContentBlock

			for _, content := range msg.Content.ContentBlocks {
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
							Type: fnTypePtr,
							ID:   content.ID,
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      content.Name,
								Arguments: schemas.JsonifyInput(content.Input),
							},
						}
						toolCalls = append(toolCalls, tc)
					}
				case AnthropicContentBlockTypeToolResult:
					if content.ToolUseID != nil {
						bifrostMsg.ChatToolMessage = &schemas.ChatToolMessage{
							ToolCallID: content.ToolUseID,
						}
						if content.Content.ContentStr != nil {
							contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
								Type: schemas.ChatContentBlockTypeText,
								Text: content.Content.ContentStr,
							})
						} else if content.Content.ContentBlocks != nil {
							for _, block := range content.Content.ContentBlocks {
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
						bifrostMsg.Role = schemas.ChatMessageRoleTool
					}
				}
			}

			// Concatenate all text contents
			if len(contentBlocks) > 0 {
				bifrostMsg.Content = schemas.ChatMessageContent{
					ContentBlocks: contentBlocks,
				}
			}

			if len(toolCalls) > 0 && msg.Role == AnthropicMessageRoleAssistant {
				bifrostMsg.ChatAssistantMessage = &schemas.ChatAssistantMessage{
					ToolCalls: toolCalls,
				}
			}
		}
		messages = append(messages, bifrostMsg)
	}

	bifrostReq.Input = messages

	// Convert parameters
	if mr.MaxTokens > 0 || mr.Temperature != nil || mr.TopP != nil || mr.TopK != nil || mr.StopSequences != nil {
		params := &schemas.ChatParameters{
			ExtraParams: make(map[string]interface{}),
		}

		if mr.MaxTokens > 0 {
			params.MaxCompletionTokens = &mr.MaxTokens
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
			params.Stop = mr.StopSequences
		}

		bifrostReq.Params = params
	}

	// Convert tools
	if mr.Tools != nil {
		tools := []schemas.ChatTool{}
		for _, tool := range mr.Tools {
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
	if mr.ToolChoice != nil {
		if bifrostReq.Params == nil {
			bifrostReq.Params = &schemas.ChatParameters{}
		}
		toolChoice := &schemas.ChatToolChoice{
			ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
				Type: func() schemas.ChatToolChoiceType {
					if mr.ToolChoice.Type == "tool" {
						return schemas.ChatToolChoiceTypeFunction
					}
					return schemas.ChatToolChoiceType(mr.ToolChoice.Type)
				}(),
			},
		}
		if mr.ToolChoice.Type == "tool" && mr.ToolChoice.Name != "" {
			toolChoice.ChatToolChoiceStruct.Function = schemas.ChatToolChoiceFunction{
				Name: mr.ToolChoice.Name,
			}
		}
		bifrostReq.Params.ToolChoice = toolChoice
	}

	return bifrostReq
}

// ToBifrostResponse converts an Anthropic message response to Bifrost format
func (response *AnthropicMessageResponse) ToBifrostResponse() *schemas.BifrostResponse {
	if response == nil {
		return nil
	}

	// Initialize Bifrost response
	bifrostResponse := &schemas.BifrostResponse{
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
		Content:              messageContent,
		ChatAssistantMessage: assistantMessage,
	}

	// Create choice
	choice := schemas.BifrostChatResponseChoice{
		Index: 0,
		BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
			Message:    message,
			StopString: response.StopSequence,
		},
		FinishReason: func() *string {
			if response.StopReason != nil && *response.StopReason != "" {
				mapped := MapAnthropicFinishReasonToBifrost(*response.StopReason)
				return &mapped
			}
			return nil
		}(),
	}

	bifrostResponse.Choices = []schemas.BifrostChatResponseChoice{choice}

	// Convert usage information
	if response.Usage != nil {
		bifrostResponse.Usage = &schemas.LLMUsage{
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

	// Convert messages
	var anthropicMessages []AnthropicMessage
	var systemContent *AnthropicContent

	for _, msg := range messages {
		switch msg.Role {
		case schemas.ChatMessageRoleSystem:
			// Handle system message separately
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

		case schemas.ChatMessageRoleTool:
			// Convert tool message to user message with tool_result content
			if msg.ChatToolMessage != nil && msg.ChatToolMessage.ToolCallID != nil {
				content := make([]AnthropicContentBlock, 0, 1)

				toolResult := AnthropicContentBlock{
					Type:      "tool_result",
					ToolUseID: msg.ChatToolMessage.ToolCallID,
				}

				// Convert tool result content
				if msg.Content.ContentStr != nil {
					toolResult.Content = &AnthropicContent{ContentStr: msg.Content.ContentStr}
				} else if msg.Content.ContentBlocks != nil {
					blocks := make([]AnthropicContentBlock, 0, len(msg.Content.ContentBlocks))
					for _, block := range msg.Content.ContentBlocks {
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

				content = append(content, toolResult)
				anthropicMessages = append(anthropicMessages, AnthropicMessage{
					Role:    "user", // Tool results are sent as user messages in Anthropic
					Content: AnthropicContent{ContentBlocks: content},
				})
			}

		default:
			// Handle user and assistant messages
			anthropicMsg := AnthropicMessage{
				Role: AnthropicMessageRole(msg.Role),
			}

			var content []AnthropicContentBlock

			// Convert text content
			if msg.Content.ContentStr != nil {
				content = append(content, AnthropicContentBlock{
					Type: "text",
					Text: msg.Content.ContentStr,
				})
			} else if msg.Content.ContentBlocks != nil {
				for _, block := range msg.Content.ContentBlocks {
					if block.Text != nil {
						content = append(content, AnthropicContentBlock{
							Type: "text",
							Text: block.Text,
						})
					} else if block.ImageURLStruct != nil {
						content = append(content, ConvertToAnthropicImageBlock(block))
					}
				}
			}

			// Convert tool calls
			if msg.ChatAssistantMessage != nil && msg.ChatAssistantMessage.ToolCalls != nil {
				for _, toolCall := range msg.ChatAssistantMessage.ToolCalls {
					toolUse := AnthropicContentBlock{
						Type: "tool_use",
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
			if len(content) == 1 && content[0].Type == "text" {
				// Single text content can be string
				anthropicMsg.Content = AnthropicContent{ContentStr: content[0].Text}
			} else if len(content) > 0 {
				// Multiple content blocks
				anthropicMsg.Content = AnthropicContent{ContentBlocks: content}
			}

			anthropicMessages = append(anthropicMessages, anthropicMsg)
		}
	}

	anthropicReq.Messages = anthropicMessages
	anthropicReq.System = systemContent

	return anthropicReq
}

// ToAnthropicChatCompletionResponse converts a Bifrost response to Anthropic format
func ToAnthropicChatCompletionResponse(bifrostResp *schemas.BifrostResponse) *AnthropicMessageResponse {
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
			mappedReason := schemas.MapFinishReasonToProvider(*choice.FinishReason, schemas.Anthropic)
			anthropicResp.StopReason = &mappedReason
		}
		if choice.StopString != nil {
			anthropicResp.StopSequence = choice.StopString
		}

		// Add text content
		if choice.Message.Content.ContentStr != nil && *choice.Message.Content.ContentStr != "" {
			content = append(content, AnthropicContentBlock{
				Type: "text",
				Text: choice.Message.Content.ContentStr,
			})
		} else if choice.Message.Content.ContentBlocks != nil {
			for _, block := range choice.Message.Content.ContentBlocks {
				if block.Text != nil {
					content = append(content, AnthropicContentBlock{
						Type: "text",
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
					Type:  "tool_use",
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

// ToAnthropicChatCompletionStreamResponse converts a Bifrost streaming response to Anthropic SSE string format
func ToAnthropicChatCompletionStreamResponse(bifrostResp *schemas.BifrostResponse) string {
	if bifrostResp == nil {
		return ""
	}

	streamResp := &AnthropicStreamResponse{}

	// Handle different streaming event types based on the response content
	if len(bifrostResp.Choices) > 0 {
		choice := bifrostResp.Choices[0] // Anthropic typically returns one choice

		// Handle streaming responses
		if choice.BifrostStreamResponseChoice != nil {
			delta := choice.BifrostStreamResponseChoice.Delta

			// Handle text content deltas
			if delta.Content != nil {
				streamResp.Type = "content_block_delta"
				streamResp.Index = &choice.Index
				streamResp.Delta = &AnthropicStreamDelta{
					Type: "text_delta",
					Text: delta.Content,
				}
			} else if delta.Thought != nil {
				// Handle thinking content deltas
				streamResp.Type = "content_block_delta"
				streamResp.Index = &choice.Index
				streamResp.Delta = &AnthropicStreamDelta{
					Type:     "thinking_delta",
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
						Type: "tool_use",
						ID:   toolCall.ID,
						Name: toolCall.Function.Name,
					}
				} else if toolCall.Function.Arguments != "" {
					// Tool input delta
					streamResp.Type = "content_block_delta"
					streamResp.Index = &choice.Index
					streamResp.Delta = &AnthropicStreamDelta{
						Type:        "input_json_delta",
						PartialJSON: &toolCall.Function.Arguments,
					}
				}
			} else if choice.FinishReason != nil && *choice.FinishReason != "" {
				// Handle finish reason - map back to Anthropic format
				stopReason := schemas.MapFinishReasonToProvider(*choice.FinishReason, schemas.Anthropic)
				streamResp.Type = "message_delta"
				streamResp.Delta = &AnthropicStreamDelta{
					Type:       "message_delta",
					StopReason: &stopReason,
				}
			}

		} else if choice.BifrostNonStreamResponseChoice != nil {
			// Handle non-streaming response converted to streaming format
			streamResp.Type = "message_start"

			// Create message start event
			streamMessage := &AnthropicStreamMessage{
				ID:    bifrostResp.ID,
				Type:  "message",
				Role:  string(choice.BifrostNonStreamResponseChoice.Message.Role),
				Model: bifrostResp.Model,
			}

			// Convert content
			var content []AnthropicContentBlock
			if choice.BifrostNonStreamResponseChoice.Message.Content.ContentStr != nil {
				content = append(content, AnthropicContentBlock{
					Type: "text",
					Text: choice.BifrostNonStreamResponseChoice.Message.Content.ContentStr,
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
		streamResp.Model = &bifrostResp.Model
	}

	// Default to empty content_block_delta if no specific type was set
	if streamResp.Type == "" {
		streamResp.Type = "content_block_delta"
		streamResp.Index = schemas.Ptr(0)
		streamResp.Delta = &AnthropicStreamDelta{
			Type: "text_delta",
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
