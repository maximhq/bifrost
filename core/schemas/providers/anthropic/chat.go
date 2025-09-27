package anthropic

import (
	"encoding/json"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

var fnTypePtr = schemas.Ptr(string(schemas.ToolChoiceTypeFunction))

// ToChatCompletionRequest converts an Anthropic messages request to Bifrost format
func (request *AnthropicMessageRequest) ToBifrostRequest() *schemas.BifrostRequest {
	provider, model := schemas.ParseModelString(request.Model, schemas.Anthropic)

	bifrostReq := &schemas.BifrostRequest{
		Provider: provider,
		Model:    model,
	}

	messages := []schemas.BifrostMessage{}

	// Add system message if present
	if request.System != nil {
		if request.System.ContentStr != nil && *request.System.ContentStr != "" {
			messages = append(messages, schemas.BifrostMessage{
				Role: schemas.ModelChatMessageRoleSystem,
				Content: schemas.MessageContent{
					ContentStr: request.System.ContentStr,
				},
			})
		} else if request.System.ContentBlocks != nil {
			contentBlocks := []schemas.ContentBlock{}
			for _, block := range *request.System.ContentBlocks {
				contentBlocks = append(contentBlocks, schemas.ContentBlock{
					Type: schemas.ContentBlockTypeText,
					Text: block.Text,
				})
			}
			messages = append(messages, schemas.BifrostMessage{
				Role: schemas.ModelChatMessageRoleSystem,
				Content: schemas.MessageContent{
					ContentBlocks: &contentBlocks,
				},
			})
		}
	}

	// Convert messages
	for _, msg := range request.Messages {
		var bifrostMsg schemas.BifrostMessage
		bifrostMsg.Role = schemas.ModelChatMessageRole(msg.Role)

		if msg.Content.ContentStr != nil {
			bifrostMsg.Content = schemas.MessageContent{
				ContentStr: msg.Content.ContentStr,
			}
		} else if msg.Content.ContentBlocks != nil {
			// Handle different content types
			var toolCalls []schemas.ToolCall
			var contentBlocks []schemas.ContentBlock

			for _, content := range *msg.Content.ContentBlocks {
				switch content.Type {
				case "text":
					if content.Text != nil {
						contentBlocks = append(contentBlocks, schemas.ContentBlock{
							Type: schemas.ContentBlockTypeText,
							Text: content.Text,
						})
					}
				case "image":
					if content.Source != nil {
						contentBlocks = append(contentBlocks, schemas.ContentBlock{
							Type: schemas.ContentBlockTypeImage,
							ImageURL: &schemas.ImageURLStruct{
								URL: func() string {
									if content.Source.Data != nil {
										mime := "image/png"
										if content.Source.MediaType != nil && *content.Source.MediaType != "" {
											mime = *content.Source.MediaType
										}
										return "data:" + mime + ";base64," + *content.Source.Data
									}
									if content.Source.URL != nil {
										return *content.Source.URL
									}
									return ""
								}(),
							},
						})
					}
				case "tool_use":
					if content.ID != nil && content.Name != nil {
						tc := schemas.ToolCall{
							Type: fnTypePtr,
							ID:   content.ID,
							Function: schemas.FunctionCall{
								Name:      content.Name,
								Arguments: jsonifyInput(content.Input),
							},
						}
						toolCalls = append(toolCalls, tc)
					}
				case "tool_result":
					if content.ToolUseID != nil {
						bifrostMsg.ToolMessage = &schemas.ToolMessage{
							ToolCallID: content.ToolUseID,
						}
						if content.Content.ContentStr != nil {
							contentBlocks = append(contentBlocks, schemas.ContentBlock{
								Type: schemas.ContentBlockTypeText,
								Text: content.Content.ContentStr,
							})
						} else if content.Content.ContentBlocks != nil {
							for _, block := range *content.Content.ContentBlocks {
								if block.Text != nil {
									contentBlocks = append(contentBlocks, schemas.ContentBlock{
										Type: schemas.ContentBlockTypeText,
										Text: block.Text,
									})
								} else if block.Source != nil {
									contentBlocks = append(contentBlocks, schemas.ContentBlock{
										Type: schemas.ContentBlockTypeImage,
										ImageURL: &schemas.ImageURLStruct{
											URL: func() string {
												if block.Source.Data != nil {
													mime := "image/png"
													if block.Source.MediaType != nil && *block.Source.MediaType != "" {
														mime = *block.Source.MediaType
													}
													return "data:" + mime + ";base64," + *block.Source.Data
												}
												if block.Source.URL != nil {
													return *block.Source.URL
												}
												return ""
											}()},
									})
								}
							}
						}
						bifrostMsg.Role = schemas.ModelChatMessageRoleTool
					}
				}
			}

			// Concatenate all text contents
			if len(contentBlocks) > 0 {
				bifrostMsg.Content = schemas.MessageContent{
					ContentBlocks: &contentBlocks,
				}
			}

			if len(toolCalls) > 0 && msg.Role == string(schemas.ModelChatMessageRoleAssistant) {
				bifrostMsg.AssistantMessage = &schemas.AssistantMessage{
					ToolCalls: &toolCalls,
				}
			}
		}
		messages = append(messages, bifrostMsg)
	}

	bifrostReq.Input.ChatCompletionInput = &messages

	// Convert parameters
	if request.MaxTokens > 0 || request.Temperature != nil || request.TopP != nil || request.TopK != nil || request.StopSequences != nil {
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
		if request.TopK != nil {
			params.TopK = request.TopK
		}
		if request.StopSequences != nil {
			params.StopSequences = request.StopSequences
		}

		bifrostReq.Params = params
	}

	// Convert tools
	if request.Tools != nil {
		tools := []schemas.Tool{}
		for _, tool := range *request.Tools {
			// Convert input_schema to FunctionParameters
			params := schemas.FunctionParameters{
				Type: "object",
			}
			if tool.InputSchema != nil {
				params.Type = tool.InputSchema.Type
				params.Required = tool.InputSchema.Required
				params.Properties = tool.InputSchema.Properties
			}

			tools = append(tools, schemas.Tool{
				Type: "function",
				Function: schemas.Function{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  params,
				},
			})
		}
		if bifrostReq.Params == nil {
			bifrostReq.Params = &schemas.ModelParameters{}
		}
		bifrostReq.Params.Tools = &tools
	}

	// Convert tool choice
	if request.ToolChoice != nil {
		if bifrostReq.Params == nil {
			bifrostReq.Params = &schemas.ModelParameters{}
		}
		toolChoice := &schemas.ToolChoice{
			ToolChoiceStruct: &schemas.ToolChoiceStruct{
				Type: func() schemas.ToolChoiceType {
					if request.ToolChoice.Type == "tool" {
						return schemas.ToolChoiceTypeFunction
					}
					return schemas.ToolChoiceType(request.ToolChoice.Type)
				}(),
			},
		}
		if request.ToolChoice.Type == "tool" && request.ToolChoice.Name != "" {
			toolChoice.ToolChoiceStruct.Function = schemas.ToolChoiceFunction{
				Name: request.ToolChoice.Name,
			}
		}
		bifrostReq.Params.ToolChoice = toolChoice
	}

	// Apply parameter validation
	if bifrostReq.Params != nil {
		bifrostReq.Params = schemas.ValidateAndFilterParamsForProvider(provider, bifrostReq.Params)
	}

	return bifrostReq
}

// ToChatCompletionResponse converts an Anthropic message response to Bifrost format
func (response *AnthropicMessageResponse) ToBifrostResponse() *schemas.BifrostResponse {
	if response == nil {
		return nil
	}

	// Initialize Bifrost response
	bifrostResponse := &schemas.BifrostResponse{
		ID:    response.ID,
		Model: response.Model,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.Anthropic,
		},
	}

	// Collect all content and tool calls into a single message
	var toolCalls []schemas.ToolCall
	var thinking string
	var contentBlocks []schemas.ContentBlock

	// Process content and tool calls
	for _, c := range response.Content {
		switch c.Type {
		case "thinking":
			if c.Thinking != nil {
				thinking = *c.Thinking
			}
		case "text":
			if c.Text != nil {
				contentBlocks = append(contentBlocks, schemas.ContentBlock{
					Type: schemas.ContentBlockTypeText,
					Text: c.Text,
				})
			}
		case "tool_use":
			if c.ID != nil && c.Name != nil {
				function := schemas.FunctionCall{
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

				toolCalls = append(toolCalls, schemas.ToolCall{
					Type:     schemas.Ptr("function"),
					ID:       c.ID,
					Function: function,
				})
			}
		}
	}

	// Create the assistant message
	var assistantMessage *schemas.AssistantMessage

	// Create AssistantMessage if we have tool calls or thinking
	if len(toolCalls) > 0 || thinking != "" {
		assistantMessage = &schemas.AssistantMessage{}
		if len(toolCalls) > 0 {
			assistantMessage.ToolCalls = &toolCalls
		}
		if thinking != "" {
			assistantMessage.Thought = &thinking
		}
	}

	// Create a single choice with the collected content
	bifrostResponse.Choices = []schemas.BifrostResponseChoice{
		{
			Index: 0,
			BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
				Message: schemas.BifrostMessage{
					Role: schemas.ModelChatMessageRoleAssistant,
					Content: schemas.MessageContent{
						ContentBlocks: &contentBlocks,
					},
					AssistantMessage: assistantMessage,
				},
				StopString: response.StopSequence,
			},
			FinishReason: func() *string {
				if response.StopReason != nil && *response.StopReason != "" {
					mapped := MapAnthropicFinishReason(*response.StopReason)
					return &mapped
				}
				return nil
			}(),
		},
	}

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
func ToAnthropicChatCompletionRequest(bifrostReq *schemas.BifrostRequest) *AnthropicMessageRequest {
	if bifrostReq == nil || bifrostReq.Input.ChatCompletionInput == nil {
		return nil
	}

	messages := *bifrostReq.Input.ChatCompletionInput
	anthropicReq := &AnthropicMessageRequest{
		Model: bifrostReq.Model,
	}

	// Convert parameters
	if bifrostReq.Params != nil {
		if bifrostReq.Params.MaxTokens != nil {
			anthropicReq.MaxTokens = *bifrostReq.Params.MaxTokens
		} else {
			anthropicReq.MaxTokens = AnthropicDefaultMaxTokens // Anthropic default
		}

		anthropicReq.Temperature = bifrostReq.Params.Temperature
		anthropicReq.TopP = bifrostReq.Params.TopP
		anthropicReq.TopK = bifrostReq.Params.TopK
		anthropicReq.StopSequences = bifrostReq.Params.StopSequences

		// Convert tools
		if bifrostReq.Params.Tools != nil {
			tools := make([]AnthropicTool, 0, len(*bifrostReq.Params.Tools))
			for _, tool := range *bifrostReq.Params.Tools {
				anthropicTool := AnthropicTool{
					Name:        tool.Function.Name,
					Description: tool.Function.Description,
				}

				// Convert function parameters to input_schema
				if tool.Function.Parameters.Type != "" || tool.Function.Parameters.Properties != nil {
					anthropicTool.InputSchema = &struct {
						Type       string                 `json:"type"`
						Properties map[string]interface{} `json:"properties"`
						Required   []string               `json:"required"`
					}{
						Type:       tool.Function.Parameters.Type,
						Properties: tool.Function.Parameters.Properties,
						Required:   tool.Function.Parameters.Required,
					}
				}

				tools = append(tools, anthropicTool)
			}
			anthropicReq.Tools = &tools
		}

		// Convert tool choice
		if bifrostReq.Params.ToolChoice != nil {
			toolChoice := &AnthropicToolChoice{}

			if bifrostReq.Params.ToolChoice.ToolChoiceStr != nil {
				toolChoice.Type = *bifrostReq.Params.ToolChoice.ToolChoiceStr
			} else if bifrostReq.Params.ToolChoice.ToolChoiceStruct != nil {
				switch bifrostReq.Params.ToolChoice.ToolChoiceStruct.Type {
				case schemas.ToolChoiceTypeFunction:
					toolChoice.Type = "tool"
					toolChoice.Name = bifrostReq.Params.ToolChoice.ToolChoiceStruct.Function.Name
				default:
					toolChoice.Type = string(bifrostReq.Params.ToolChoice.ToolChoiceStruct.Type)
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
		case schemas.ModelChatMessageRoleSystem:
			// Handle system message separately
			if msg.Content.ContentStr != nil {
				systemContent = &AnthropicContent{ContentStr: msg.Content.ContentStr}
			} else if msg.Content.ContentBlocks != nil {
				blocks := make([]AnthropicContentBlock, 0, len(*msg.Content.ContentBlocks))
				for _, block := range *msg.Content.ContentBlocks {
					if block.Text != nil {
						blocks = append(blocks, AnthropicContentBlock{
							Type: "text",
							Text: block.Text,
						})
					}
				}
				if len(blocks) > 0 {
					systemContent = &AnthropicContent{ContentBlocks: &blocks}
				}
			}

		case schemas.ModelChatMessageRoleTool:
			// Convert tool message to user message with tool_result content
			if msg.ToolMessage != nil && msg.ToolMessage.ToolCallID != nil {
				content := make([]AnthropicContentBlock, 0, 1)

				toolResult := AnthropicContentBlock{
					Type:      "tool_result",
					ToolUseID: msg.ToolMessage.ToolCallID,
				}

				// Convert tool result content
				if msg.Content.ContentStr != nil {
					toolResult.Content = &AnthropicContent{ContentStr: msg.Content.ContentStr}
				} else if msg.Content.ContentBlocks != nil {
					blocks := make([]AnthropicContentBlock, 0, len(*msg.Content.ContentBlocks))
					for _, block := range *msg.Content.ContentBlocks {
						if block.Text != nil {
							blocks = append(blocks, AnthropicContentBlock{
								Type: "text",
								Text: block.Text,
							})
						} else if block.ImageURL != nil {
							blocks = append(blocks, convertImageBlock(block))
						}
					}
					if len(blocks) > 0 {
						toolResult.Content = &AnthropicContent{ContentBlocks: &blocks}
					}
				}

				content = append(content, toolResult)
				anthropicMessages = append(anthropicMessages, AnthropicMessage{
					Role:    "user", // Tool results are sent as user messages in Anthropic
					Content: AnthropicContent{ContentBlocks: &content},
				})
			}

		default:
			// Handle user and assistant messages
			anthropicMsg := AnthropicMessage{
				Role: string(msg.Role),
			}

			var content []AnthropicContentBlock

			// Convert text content
			if msg.Content.ContentStr != nil {
				content = append(content, AnthropicContentBlock{
					Type: "text",
					Text: msg.Content.ContentStr,
				})
			} else if msg.Content.ContentBlocks != nil {
				for _, block := range *msg.Content.ContentBlocks {
					if block.Text != nil {
						content = append(content, AnthropicContentBlock{
							Type: "text",
							Text: block.Text,
						})
					} else if block.ImageURL != nil {
						content = append(content, convertImageBlock(block))
					}
				}
			}

			// Convert thinking content
			if msg.AssistantMessage != nil && msg.AssistantMessage.Thought != nil {
				content = append(content, AnthropicContentBlock{
					Type:     "thinking",
					Thinking: msg.AssistantMessage.Thought,
				})
			}

			// Convert tool calls
			if msg.AssistantMessage != nil && msg.AssistantMessage.ToolCalls != nil {
				for _, toolCall := range *msg.AssistantMessage.ToolCalls {
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
				anthropicMsg.Content = AnthropicContent{ContentBlocks: &content}
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
		Role:  string(schemas.ModelChatMessageRoleAssistant),
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

		// Add thinking content if present
		if choice.Message.AssistantMessage != nil && choice.Message.AssistantMessage.Thought != nil && *choice.Message.AssistantMessage.Thought != "" {
			content = append(content, AnthropicContentBlock{
				Type: "thinking",
				Text: choice.Message.AssistantMessage.Thought,
			})
		}

		// Add text content
		if choice.Message.Content.ContentStr != nil && *choice.Message.Content.ContentStr != "" {
			content = append(content, AnthropicContentBlock{
				Type: "text",
				Text: choice.Message.Content.ContentStr,
			})
		} else if choice.Message.Content.ContentBlocks != nil {
			for _, block := range *choice.Message.Content.ContentBlocks {
				if block.Text != nil {
					content = append(content, AnthropicContentBlock{
						Type: "text",
						Text: block.Text,
					})
				}
			}
		}

		// Add tool calls as tool_use content
		if choice.Message.AssistantMessage != nil && choice.Message.AssistantMessage.ToolCalls != nil {
			for _, toolCall := range *choice.Message.AssistantMessage.ToolCalls {
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
		Type:    "",
		Message: bifrostErr.Error.Message,
	}

	if bifrostErr.Error.Type != nil {
		errorStruct.Type = *bifrostErr.Error.Type
	}

	return &AnthropicMessageError{
		Type:  errorType,
		Error: errorStruct,
	}
}
