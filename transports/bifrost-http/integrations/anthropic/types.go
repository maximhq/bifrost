package anthropic

import (
	"encoding/json"
	"fmt"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
)

var fnTypePtr = bifrost.Ptr(string(schemas.ToolChoiceTypeFunction))

// AnthropicContentBlock represents content in Anthropic message format
type AnthropicContentBlock struct {
	Type      string                `json:"type"`                  // "text", "image", "tool_use", "tool_result"
	Text      *string               `json:"text,omitempty"`        // For text content
	ToolUseID *string               `json:"tool_use_id,omitempty"` // For tool_result content
	ID        *string               `json:"id,omitempty"`          // For tool_use content
	Name      *string               `json:"name,omitempty"`        // For tool_use content
	Input     interface{}           `json:"input,omitempty"`       // For tool_use content
	Content   AnthropicContent      `json:"content,omitempty"`     // For tool_result content
	Source    *AnthropicImageSource `json:"source,omitempty"`      // For image content
}

// AnthropicImageSource represents image source in Anthropic format
type AnthropicImageSource struct {
	Type      string  `json:"type"`                 // "base64" or "url"
	MediaType *string `json:"media_type,omitempty"` // "image/jpeg", "image/png", etc.
	Data      *string `json:"data,omitempty"`       // Base64-encoded image data
	URL       *string `json:"url,omitempty"`        // URL of the image
}

// AnthropicMessage represents a message in Anthropic format
type AnthropicMessage struct {
	Role    string           `json:"role"`    // "user", "assistant"
	Content AnthropicContent `json:"content"` // Array of content blocks
}

type AnthropicContent struct {
	ContentStr    *string
	ContentBlocks *[]AnthropicContentBlock
}

// AnthropicTool represents a tool in Anthropic format
type AnthropicTool struct {
	Name        string  `json:"name"`
	Type        *string `json:"type,omitempty"`
	Description string  `json:"description"`
	InputSchema *struct {
		Type       string                 `json:"type"` // "object"
		Properties map[string]interface{} `json:"properties"`
		Required   []string               `json:"required"`
	} `json:"input_schema,omitempty"`
}

// AnthropicToolChoice represents tool choice in Anthropic format
type AnthropicToolChoice struct {
	Type string `json:"type"`           // "auto", "any", "tool"
	Name string `json:"name,omitempty"` // For type "tool"
}

// AnthropicMessageRequest represents an Anthropic messages API request
type AnthropicMessageRequest struct {
	Model         string               `json:"model"`
	MaxTokens     int                  `json:"max_tokens"`
	Messages      []AnthropicMessage   `json:"messages"`
	System        *AnthropicContent    `json:"system,omitempty"`
	Temperature   *float64             `json:"temperature,omitempty"`
	TopP          *float64             `json:"top_p,omitempty"`
	TopK          *int                 `json:"top_k,omitempty"`
	StopSequences *[]string            `json:"stop_sequences,omitempty"`
	Stream        *bool                `json:"stream,omitempty"`
	Tools         *[]AnthropicTool     `json:"tools,omitempty"`
	ToolChoice    *AnthropicToolChoice `json:"tool_choice,omitempty"`
}

// IsStreamingRequested implements the StreamingRequest interface
func (r *AnthropicMessageRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}

// AnthropicMessageResponse represents an Anthropic messages API response
type AnthropicMessageResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Content      []AnthropicContentBlock `json:"content"`
	Model        string                  `json:"model"`
	StopReason   *string                 `json:"stop_reason,omitempty"`
	StopSequence *string                 `json:"stop_sequence,omitempty"`
	Usage        *AnthropicUsage         `json:"usage,omitempty"`
}

// AnthropicUsage represents usage information in Anthropic format
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// AnthropicMessageError represents an Anthropic messages API error response
type AnthropicMessageError struct {
	Type  string                      `json:"type"`  // always "error"
	Error AnthropicMessageErrorStruct `json:"error"` // Error details
}

// AnthropicMessageErrorStruct represents the error structure of an Anthropic messages API error response
type AnthropicMessageErrorStruct struct {
	Type    string `json:"type"`    // Error type
	Message string `json:"message"` // Error message
}

// AnthropicStreamResponse represents a single chunk in the Anthropic streaming response
// This matches the format expected by Anthropic's streaming API clients
type AnthropicStreamResponse struct {
	Type         string                  `json:"type"`
	ID           *string                 `json:"id,omitempty"`
	Model        *string                 `json:"model,omitempty"`
	Index        *int                    `json:"index,omitempty"`
	Message      *AnthropicStreamMessage `json:"message,omitempty"`
	ContentBlock *AnthropicContentBlock  `json:"content_block,omitempty"`
	Delta        *AnthropicStreamDelta   `json:"delta,omitempty"`
	Usage        *AnthropicUsage         `json:"usage,omitempty"`
}

// AnthropicStreamMessage represents the message structure in streaming events
type AnthropicStreamMessage struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Content      []AnthropicContentBlock `json:"content"`
	Model        string                  `json:"model"`
	StopReason   *string                 `json:"stop_reason,omitempty"`
	StopSequence *string                 `json:"stop_sequence,omitempty"`
	Usage        *AnthropicUsage         `json:"usage,omitempty"`
}

// AnthropicStreamDelta represents the incremental content in a streaming chunk
type AnthropicStreamDelta struct {
	Type         string  `json:"type"`
	Text         *string `json:"text,omitempty"`
	Thinking     *string `json:"thinking,omitempty"`
	PartialJSON  *string `json:"partial_json,omitempty"`
	StopReason   *string `json:"stop_reason,omitempty"`
	StopSequence *string `json:"stop_sequence,omitempty"`
}

// MarshalJSON implements custom JSON marshalling for MessageContent.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (mc AnthropicContent) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if mc.ContentStr != nil && mc.ContentBlocks != nil {
		return nil, fmt.Errorf("both ContentStr and ContentBlocks are set; only one should be non-nil")
	}

	if mc.ContentStr != nil {
		return json.Marshal(*mc.ContentStr)
	}
	if mc.ContentBlocks != nil {
		return json.Marshal(*mc.ContentBlocks)
	}
	// If both are nil, return null
	return json.Marshal(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for MessageContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
// It also handles direct string/array content without a wrapper object.
func (mc *AnthropicContent) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := json.Unmarshal(data, &stringContent); err == nil {
		mc.ContentStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var arrayContent []AnthropicContentBlock
	if err := json.Unmarshal(data, &arrayContent); err == nil {
		mc.ContentBlocks = &arrayContent
		return nil
	}

	return fmt.Errorf("content field is neither a string nor an array of ContentBlock")
}

// ConvertToBifrostRequest converts an Anthropic messages request to Bifrost format
func (r *AnthropicMessageRequest) ConvertToBifrostRequest() *schemas.BifrostRequest {
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
		for _, tool := range *r.Tools {
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
		if r.ToolChoice.Type == "tool" && r.ToolChoice.Name != "" {
			toolChoice.ToolChoiceStruct.Function = schemas.ToolChoiceFunction{
				Name: r.ToolChoice.Name,
			}
		}
		bifrostReq.Params.ToolChoice = toolChoice
	}

	return bifrostReq
}

// Helper function to convert interface{} to JSON string
func jsonifyInput(input interface{}) string {
	if input == nil {
		return "{}"
	}
	jsonBytes, err := json.Marshal(input)
	if err != nil {
		return "{}"
	}
	return string(jsonBytes)
}

// DeriveAnthropicFromBifrostResponse converts a Bifrost response to Anthropic format
func DeriveAnthropicFromBifrostResponse(bifrostResp *schemas.BifrostResponse) *AnthropicMessageResponse {
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
			anthropicResp.StopReason = choice.FinishReason
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

// DeriveAnthropicStreamFromBifrostResponse converts a Bifrost streaming response to Anthropic SSE string format
func DeriveAnthropicStreamFromBifrostResponse(bifrostResp *schemas.BifrostResponse) string {
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
				// Handle finish reason
				streamResp.Type = "message_delta"
				streamResp.Delta = &AnthropicStreamDelta{
					Type:       "message_delta",
					StopReason: choice.FinishReason,
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
		streamResp.Index = bifrost.Ptr(0)
		streamResp.Delta = &AnthropicStreamDelta{
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
func DeriveAnthropicErrorFromBifrostError(bifrostErr *schemas.BifrostError) *AnthropicMessageError {
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
