package anthropic

import (
	"encoding/json"
	"fmt"
	"log"
	
	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/api"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
)

// ConvertToBifrostRequest converts an Anthropic messages request to Bifrost format
func ConvertToBifrostRequest(r *api.AnthropicMessageRequest) *schemas.BifrostRequest {
	provider, model := integrations.ParseModelString(r.Model, schemas.Anthropic)

	bifrostReq := &schemas.BifrostRequest{
		Provider: provider,
		Model:    model,
	}

	messages := []schemas.BifrostMessage{}

	// Add system message if present
	if r.System != nil {
		if r.System.ContentStr != nil && *r.System.ContentStr != "" {
			messages = append(messages, schemas.BifrostMessage{
				Role: schemas.ModelChatMessageRoleSystem,
				Content: schemas.MessageContent{
					ContentStr: r.System.ContentStr,
				},
			})
		} else if r.System.ContentBlocks != nil {
			contentBlocks := []schemas.ContentBlock{}
			for _, block := range *r.System.ContentBlocks {
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
	for _, msg := range r.Messages {
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
							Type: bifrost.Ptr(string(schemas.ToolChoiceTypeFunction)),
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
	if r.MaxTokens > 0 || r.Temperature != nil || r.TopP != nil || r.TopK != nil || r.StopSequences != nil {
		params := &schemas.ModelParameters{}

		if r.MaxTokens > 0 {
			params.MaxTokens = &r.MaxTokens
		}
		if r.Temperature != nil {
			params.Temperature = r.Temperature
		}
		if r.TopP != nil {
			params.TopP = r.TopP
		}
		if r.TopK != nil {
			params.TopK = r.TopK
		}
		if r.StopSequences != nil {
			params.StopSequences = r.StopSequences
		}

		bifrostReq.Params = params
	}

	// Convert tools
	if r.Tools != nil {
		tools := []schemas.Tool{}
		for _, tool := range r.Tools {
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
		bifrostReq.Params.Tools = tools
	}

	// Convert tool choice
	if r.ToolChoice != nil {
		if bifrostReq.Params == nil {
			bifrostReq.Params = &schemas.ModelParameters{}
		}
		toolChoice := &schemas.ToolChoice{
			ToolChoiceStruct: &schemas.ToolChoiceStruct{
				Type: func() schemas.ToolChoiceType {
					if r.ToolChoice.Type == "tool" {
						return schemas.ToolChoiceTypeFunction
					}
					return schemas.ToolChoiceType(r.ToolChoice.Type)
				}(),
			},
		}
		if r.ToolChoice.Type == "tool" && r.ToolChoice.Name != nil {
			toolChoice.ToolChoiceStruct.Function = schemas.ToolChoiceFunction{
				Name: *r.ToolChoice.Name,
			}
		}
		bifrostReq.Params.ToolChoice = toolChoice
	}

	return bifrostReq
}

// DeriveAnthropicFromBifrostResponse converts a Bifrost response to Anthropic format
func DeriveAnthropicFromBifrostResponse(bifrostResp *schemas.BifrostResponse) *api.AnthropicMessageResponse {
	if bifrostResp == nil {
		return nil
	}

	anthropicResp := &api.AnthropicMessageResponse{
		ID:    bifrostResp.ID,
		Type:  "message",
		Role:  string(schemas.ModelChatMessageRoleAssistant),
		Model: bifrostResp.Model,
	}

	// Convert usage information
	if bifrostResp.Usage != nil {
		anthropicResp.Usage = &api.AnthropicUsage{
			InputTokens:  bifrostResp.Usage.PromptTokens,
			OutputTokens: bifrostResp.Usage.CompletionTokens,
		}
	}

	// Convert choices to content
	var content []api.AnthropicContentBlock
	if len(bifrostResp.Choices) > 0 {
		choice := bifrostResp.Choices[0] // Anthropic typically returns one choice

		if choice.FinishReason != nil {
			anthropicResp.StopReason = choice.FinishReason
		}
		if choice.StopString != nil {
			anthropicResp.StopSequence = choice.StopString
		}

		// Add thinking content if present
		if choice.Message.AssistantMessage != nil && choice.Message.AssistantMessage.Thought != nil && *choice.Message.AssistantMessage.Thought != "" {
			content = append(content, api.AnthropicContentBlock{
				Type: "thinking",
				Text: choice.Message.AssistantMessage.Thought,
			})
		}

		// Add text content
		if choice.Message.Content.ContentStr != nil && *choice.Message.Content.ContentStr != "" {
			content = append(content, api.AnthropicContentBlock{
				Type: "text",
				Text: choice.Message.Content.ContentStr,
			})
		} else if choice.Message.Content.ContentBlocks != nil {
			for _, block := range *choice.Message.Content.ContentBlocks {
				if block.Text != nil {
					content = append(content, api.AnthropicContentBlock{
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

				content = append(content, api.AnthropicContentBlock{
					Type:  "tool_use",
					ID:    toolCall.ID,
					Name:  toolCall.Function.Name,
					Input: input,
				})
			}
		}
	}

	if content == nil {
		content = []api.AnthropicContentBlock{}
	}

	anthropicResp.Content = content
	return anthropicResp
}

// DeriveAnthropicStreamFromBifrostResponse converts a Bifrost streaming response to Anthropic SSE string format
func DeriveAnthropicStreamFromBifrostResponse(bifrostResp *schemas.BifrostResponse) string {
	if bifrostResp == nil {
		return ""
	}

	streamResp := &api.AnthropicStreamResponse{}

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
				streamResp.Delta = &api.AnthropicStreamDelta{
					Type: "text_delta",
					Text: delta.Content,
				}
			} else if delta.Thought != nil {
				// Handle thinking content deltas
				streamResp.Type = "content_block_delta"
				streamResp.Index = &choice.Index
				streamResp.Delta = &api.AnthropicStreamDelta{
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
					streamResp.ContentBlock = &api.AnthropicContentBlock{
						Type: "tool_use",
						ID:   toolCall.ID,
						Name: toolCall.Function.Name,
					}
				} else if toolCall.Function.Arguments != "" {
					// Tool input delta
					streamResp.Type = "content_block_delta"
					streamResp.Index = &choice.Index
					streamResp.Delta = &api.AnthropicStreamDelta{
						Type:        "input_json_delta",
						PartialJSON: &toolCall.Function.Arguments,
					}
				}
			} else if choice.FinishReason != nil && *choice.FinishReason != "" {
				// Handle finish reason
				streamResp.Type = "message_delta"
				streamResp.Delta = &api.AnthropicStreamDelta{
					Type:       "message_delta",
					StopReason: choice.FinishReason,
				}
			}

		} else if choice.BifrostNonStreamResponseChoice != nil {
			// Handle non-streaming response converted to streaming format
			streamResp.Type = "message_start"

			// Create message start event
			streamMessage := &api.AnthropicStreamMessage{
				ID:    bifrostResp.ID,
				Type:  "message",
				Role:  string(choice.BifrostNonStreamResponseChoice.Message.Role),
				Model: bifrostResp.Model,
			}

			// Convert content
			var content []api.AnthropicContentBlock
			if choice.BifrostNonStreamResponseChoice.Message.Content.ContentStr != nil {
				content = append(content, api.AnthropicContentBlock{
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
		streamResp.Usage = &api.AnthropicUsage{
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
		streamResp.Index = bifrost.Ptr(0)
		streamResp.Delta = &api.AnthropicStreamDelta{
			Type: "text_delta",
			Text: bifrost.Ptr(""),
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

// DeriveAnthropicErrorFromBifrostError derives a AnthropicMessageError from a BifrostError
func DeriveAnthropicErrorFromBifrostError(bifrostErr *schemas.BifrostError) *api.AnthropicMessageError {
	if bifrostErr == nil {
		return nil
	}

	// Provide blank strings for nil pointer fields
	errorType := ""
	if bifrostErr.Type != nil {
		errorType = *bifrostErr.Type
	}

	// Handle nested error fields with nil checks
	errorStruct := api.AnthropicMessageErrorStruct{
		Type:    "",
		Message: bifrostErr.Error.Message,
	}

	if bifrostErr.Error.Type != nil {
		errorStruct.Type = *bifrostErr.Error.Type
	}

	return &api.AnthropicMessageError{
		Type:  errorType,
		Error: errorStruct,
	}
}

// DeriveAnthropicStreamFromBifrostError derives an Anthropic streaming error from a BifrostError in SSE format
func DeriveAnthropicStreamFromBifrostError(bifrostErr *schemas.BifrostError) string {
	errorResp := DeriveAnthropicErrorFromBifrostError(bifrostErr)
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

// Helper function to convert interface{} to JSON string
func jsonifyInput(input interface{}) string {
	if input == nil {
		return "{}"
	}
	jsonBytes, err := sonic.Marshal(input)
	if err != nil {
		log.Printf("Failed to marshal tool input: %v", err)
		return "{}"
	}
	return string(jsonBytes)
}
