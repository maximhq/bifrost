package anthropic

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToAnthropicChatRequest converts a Bifrost request to Anthropic format
// This is the reverse of ConvertChatRequestToBifrost for provider-side usage
func ToAnthropicChatRequest(ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostChatRequest) (*AnthropicMessageRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil, fmt.Errorf("bifrost request is nil or input is nil")
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
		if bifrostReq.Params.ResponseFormat != nil {
			// Vertex doesn't support native structured outputs, so convert to tool
			if bifrostReq.Provider == schemas.Vertex {
				responseFormatTool := convertChatResponseFormatToTool(ctx, bifrostReq.Params)
				if responseFormatTool != nil {
					anthropicReq.Tools = append(anthropicReq.Tools, *responseFormatTool)
					// Force the model to use this specific tool
					anthropicReq.ToolChoice = &AnthropicToolChoice{
						Type: "tool",
						Name: responseFormatTool.Name,
					}
				}
			} else {
				anthropicReq.OutputFormat = convertChatResponseFormatToAnthropicOutputFormat(bifrostReq.Params.ResponseFormat)
			}
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
					anthropicTool.Description = tool.Function.Description
				}

				// Convert function parameters to input_schema
				if tool.Function.Parameters != nil && (tool.Function.Parameters.Type != "" || tool.Function.Parameters.Properties != nil) {
					anthropicTool.InputSchema = &schemas.ToolFunctionParameters{
						Type:                 tool.Function.Parameters.Type,
						Description:          tool.Function.Parameters.Description,
						Properties:           tool.Function.Parameters.Properties,
						Required:             tool.Function.Parameters.Required,
						Enum:                 tool.Function.Parameters.Enum,
						AdditionalProperties: tool.Function.Parameters.AdditionalProperties,
						// JSON Schema definition fields
						Defs:        tool.Function.Parameters.Defs,
						Definitions: tool.Function.Parameters.Definitions,
						Ref:         tool.Function.Parameters.Ref,
						// Array schema fields
						Items:    tool.Function.Parameters.Items,
						MinItems: tool.Function.Parameters.MinItems,
						MaxItems: tool.Function.Parameters.MaxItems,
						// Composition fields
						AnyOf: tool.Function.Parameters.AnyOf,
						OneOf: tool.Function.Parameters.OneOf,
						AllOf: tool.Function.Parameters.AllOf,
						// String validation fields
						Format:    tool.Function.Parameters.Format,
						Pattern:   tool.Function.Parameters.Pattern,
						MinLength: tool.Function.Parameters.MinLength,
						MaxLength: tool.Function.Parameters.MaxLength,
						// Number validation fields
						Minimum: tool.Function.Parameters.Minimum,
						Maximum: tool.Function.Parameters.Maximum,
						// Misc fields
						Title:    tool.Function.Parameters.Title,
						Default:  tool.Function.Parameters.Default,
						Nullable: tool.Function.Parameters.Nullable,
					}
				}

				if tool.CacheControl != nil {
					anthropicTool.CacheControl = tool.CacheControl
				}

				tools = append(tools, anthropicTool)
			}
			if anthropicReq.Tools == nil {
				anthropicReq.Tools = tools
			} else {
				anthropicReq.Tools = append(anthropicReq.Tools, tools...)
			}
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

		// Convert reasoning
		if bifrostReq.Params.Reasoning != nil {
			if bifrostReq.Params.Reasoning.MaxTokens != nil {
				budgetTokens := *bifrostReq.Params.Reasoning.MaxTokens
				if *bifrostReq.Params.Reasoning.MaxTokens == -1 {
					// anthropic does not support dynamic reasoning budget like gemini
					// setting it to default max tokens
					budgetTokens = MinimumReasoningMaxTokens
				}
				if budgetTokens < MinimumReasoningMaxTokens {
					return nil, fmt.Errorf("reasoning.max_tokens must be >= %d for anthropic", MinimumReasoningMaxTokens)
				}
				anthropicReq.Thinking = &AnthropicThinking{
					Type:         "enabled",
					BudgetTokens: schemas.Ptr(budgetTokens),
				}
			} else if bifrostReq.Params.Reasoning.Effort != nil && *bifrostReq.Params.Reasoning.Effort != "none" {
				budgetTokens, err := providerUtils.GetBudgetTokensFromReasoningEffort(*bifrostReq.Params.Reasoning.Effort, MinimumReasoningMaxTokens, anthropicReq.MaxTokens)
				if err != nil {
					return nil, err
				}
				anthropicReq.Thinking = &AnthropicThinking{
					Type:         "enabled",
					BudgetTokens: schemas.Ptr(budgetTokens),
				}
			} else {
				anthropicReq.Thinking = &AnthropicThinking{
					Type: "disabled",
				}
			}
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
				if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
					systemContent = &AnthropicContent{ContentStr: msg.Content.ContentStr}
				} else if msg.Content.ContentBlocks != nil {
					blocks := make([]AnthropicContentBlock, 0, len(msg.Content.ContentBlocks))
					for _, block := range msg.Content.ContentBlocks {
						if block.Text != nil && *block.Text != "" {
							blocks = append(blocks, AnthropicContentBlock{
								Type:         AnthropicContentBlockTypeText,
								Text:         block.Text,
								CacheControl: block.CacheControl,
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
						Type:      AnthropicContentBlockTypeToolResult,
						ToolUseID: toolMsg.ChatToolMessage.ToolCallID,
					}

					// Convert tool result content
					if toolMsg.Content != nil {
						if toolMsg.Content.ContentStr != nil && *toolMsg.Content.ContentStr != "" {
							toolResult.Content = &AnthropicContent{ContentStr: toolMsg.Content.ContentStr}
						} else if toolMsg.Content.ContentBlocks != nil {
							blocks := make([]AnthropicContentBlock, 0, len(toolMsg.Content.ContentBlocks))
							for _, block := range toolMsg.Content.ContentBlocks {
								if block.Text != nil && *block.Text != "" {
									blocks = append(blocks, AnthropicContentBlock{
										Type:         AnthropicContentBlockTypeText,
										Text:         block.Text,
										CacheControl: block.CacheControl,
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

			// First add reasoning details
			if msg.ChatAssistantMessage != nil && msg.ChatAssistantMessage.ReasoningDetails != nil {
				for _, reasoningDetail := range msg.ChatAssistantMessage.ReasoningDetails {
					content = append(content, AnthropicContentBlock{
						Type:      AnthropicContentBlockTypeThinking,
						Signature: reasoningDetail.Signature,
						Thinking:  reasoningDetail.Text,
					})
				}
			}

			if msg.Content != nil {
				// Convert text content
				if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
					content = append(content, AnthropicContentBlock{
						Type: AnthropicContentBlockTypeText,
						Text: msg.Content.ContentStr,
					})
				} else if msg.Content.ContentBlocks != nil {
					for _, block := range msg.Content.ContentBlocks {
						if block.Text != nil && *block.Text != "" {
							content = append(content, AnthropicContentBlock{
								Type:         AnthropicContentBlockTypeText,
								Text:         block.Text,
								CacheControl: block.CacheControl,
							})
						} else if block.ImageURLStruct != nil {
							content = append(content, ConvertToAnthropicImageBlock(block))
						} else if block.File != nil {
							content = append(content, ConvertToAnthropicDocumentBlock(block))
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

	return anthropicReq, nil
}

// ToBifrostChatResponse converts an Anthropic message response to Bifrost format
func (response *AnthropicMessageResponse) ToBifrostChatResponse(ctx *schemas.BifrostContext) *schemas.BifrostChatResponse {
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
		Created: int(time.Now().Unix()),
	}

	// Check if we have a structured output tool
	var structuredOutputToolName string
	if ctx != nil {
		if toolName, ok := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string); ok {
			structuredOutputToolName = toolName
		}
	}

	// Collect all content and tool calls into a single message
	var toolCalls []schemas.ChatAssistantMessageToolCall
	var contentBlocks []schemas.ChatContentBlock
	var reasoningDetails []schemas.ChatReasoningDetails
	var reasoningText string
	var contentStr *string

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
					// Check if this is the structured output tool - if so, convert to text content
					if structuredOutputToolName != "" && *c.Name == structuredOutputToolName {
						// This is a structured output tool - convert to text content
						var jsonStr string
						if c.Input != nil {
							if argBytes, err := sonic.Marshal(c.Input); err == nil {
								jsonStr = string(argBytes)
							} else {
								jsonStr = fmt.Sprintf("%v", c.Input)
							}
						} else {
							jsonStr = "{}"
						}
						contentStr = &jsonStr
						continue // Skip adding to toolCalls
					}

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
						Index:    uint16(len(toolCalls)),
						Type:     schemas.Ptr(string(schemas.ChatToolTypeFunction)),
						ID:       c.ID,
						Function: function,
					})
				}
			case AnthropicContentBlockTypeThinking:
				reasoningDetails = append(reasoningDetails, schemas.ChatReasoningDetails{
					Index:     len(reasoningDetails),
					Type:      schemas.BifrostReasoningDetailsTypeText,
					Text:      c.Thinking,
					Signature: c.Signature,
				})
				if c.Thinking != nil {
					reasoningText += *c.Thinking + "\n"
				}
			}
		}
	}

	if len(contentBlocks) == 1 && contentBlocks[0].Type == schemas.ChatContentBlockTypeText {
		contentStr = contentBlocks[0].Text
		contentBlocks = nil
	}

	// Create a single choice with the collected content
	// Create message content
	messageContent := schemas.ChatMessageContent{
		ContentStr:    contentStr,
		ContentBlocks: contentBlocks,
	}

	// Create the assistant message
	var assistantMessage *schemas.ChatAssistantMessage

	// Create AssistantMessage if we have tool calls or thinking
	if len(toolCalls) > 0 {
		assistantMessage = &schemas.ChatAssistantMessage{
			ToolCalls: toolCalls,
		}
	}

	if len(reasoningDetails) > 0 {
		if assistantMessage == nil {
			assistantMessage = &schemas.ChatAssistantMessage{}
		}
		assistantMessage.ReasoningDetails = reasoningDetails
		if reasoningText != "" {
			assistantMessage.Reasoning = &reasoningText
		}
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
			if response.StopReason != "" {
				mapped := ConvertAnthropicFinishReasonToBifrost(response.StopReason)
				return &mapped
			}
			return nil
		}(),
	}

	bifrostResponse.Choices = []schemas.BifrostResponseChoice{choice}

	// Convert usage information
	if response.Usage != nil {
		bifrostResponse.Usage = &schemas.BifrostLLMUsage{
			PromptTokens: response.Usage.InputTokens,
			PromptTokensDetails: &schemas.ChatPromptTokensDetails{
				CachedTokens: response.Usage.CacheReadInputTokens,
			},
			CompletionTokens: response.Usage.OutputTokens,
			CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{
				CachedTokens: response.Usage.CacheCreationInputTokens,
			},
			TotalTokens: response.Usage.InputTokens + response.Usage.OutputTokens,
		}
	}

	return bifrostResponse
}

// ToAnthropicChatResponse converts a Bifrost response to Anthropic format
func ToAnthropicChatResponse(bifrostResp *schemas.BifrostChatResponse) *AnthropicMessageResponse {
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

		// NOTE: We cannot segregate between cache creation and cache read tokens, so we will use the total cached tokens as the cache read tokens
		if bifrostResp.Usage.PromptTokensDetails != nil && bifrostResp.Usage.PromptTokensDetails.CachedTokens > 0 {
			anthropicResp.Usage.CacheReadInputTokens = bifrostResp.Usage.PromptTokensDetails.CachedTokens
		}
		if bifrostResp.Usage.CompletionTokensDetails != nil && bifrostResp.Usage.CompletionTokensDetails.CachedTokens > 0 {
			anthropicResp.Usage.CacheCreationInputTokens = bifrostResp.Usage.CompletionTokensDetails.CachedTokens
		}
	}

	// Convert choices to content
	var content []AnthropicContentBlock
	if len(bifrostResp.Choices) > 0 {
		choice := bifrostResp.Choices[0] // Anthropic typically returns one choice

		if choice.FinishReason != nil {
			anthropicResp.StopReason = ConvertBifrostFinishReasonToAnthropic(*choice.FinishReason)
		}
		if choice.ChatNonStreamResponseChoice != nil && choice.StopString != nil {
			anthropicResp.StopSequence = choice.StopString
		}

		// Add reasoning content
		if choice.ChatNonStreamResponseChoice != nil && choice.Message != nil && choice.Message.ChatAssistantMessage != nil && choice.Message.ChatAssistantMessage.ReasoningDetails != nil {
			for _, reasoningDetail := range choice.Message.ChatAssistantMessage.ReasoningDetails {
				if reasoningDetail.Type == schemas.BifrostReasoningDetailsTypeText && reasoningDetail.Text != nil &&
					((reasoningDetail.Text != nil && *reasoningDetail.Text != "") ||
						(reasoningDetail.Signature != nil && *reasoningDetail.Signature != "")) {
					content = append(content, AnthropicContentBlock{
						Type:      AnthropicContentBlockTypeThinking,
						Thinking:  reasoningDetail.Text,
						Signature: reasoningDetail.Signature,
					})
				}
			}
		}

		// Add text content
		if choice.ChatNonStreamResponseChoice != nil && choice.Message != nil && choice.Message.Content != nil && choice.Message.Content.ContentStr != nil && *choice.Message.Content.ContentStr != "" {
			content = append(content, AnthropicContentBlock{
				Type: AnthropicContentBlockTypeText,
				Text: choice.Message.Content.ContentStr,
			})
		} else if choice.ChatNonStreamResponseChoice != nil && choice.Message != nil && choice.Message.Content != nil && choice.Message.Content.ContentBlocks != nil {
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
		if choice.ChatNonStreamResponseChoice != nil && choice.Message != nil && choice.Message.ChatAssistantMessage != nil && choice.Message.ChatAssistantMessage.ToolCalls != nil {
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
func (chunk *AnthropicStreamEvent) ToBifrostChatCompletionStream(ctx *schemas.BifrostContext, structuredOutputToolName string) (*schemas.BifrostChatResponse, *schemas.BifrostError, bool) {
	switch chunk.Type {
	case AnthropicStreamEventTypeMessageStart:
		return nil, nil, false

	case AnthropicStreamEventTypeMessageStop:
		return nil, nil, true

	case AnthropicStreamEventTypeContentBlockStart:
		// Emit tool-call metadata when starting a tool_use content block
		if chunk.Index != nil && chunk.ContentBlock != nil && chunk.ContentBlock.Type == AnthropicContentBlockTypeToolUse {
			// Check if this is the structured output tool - if so, skip emitting tool call metadata
			if structuredOutputToolName != "" && chunk.ContentBlock.Name != nil && *chunk.ContentBlock.Name == structuredOutputToolName {
				// Skip emitting tool call for structured output - it will be emitted as content later
				return nil, nil, false
			}

			// Create streaming response with tool call metadata
			streamResponse := &schemas.BifrostChatResponse{
				Object: "chat.completion.chunk",
				Choices: []schemas.BifrostResponseChoice{
					{
						Index: 0,
						ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
							Delta: &schemas.ChatStreamResponseChoiceDelta{
								ToolCalls: []schemas.ChatAssistantMessageToolCall{
									{
										Type: schemas.Ptr(string(schemas.ChatToolTypeFunction)),
										ID:   chunk.ContentBlock.ID,
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
								Index: 0,
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
				if chunk.Delta.PartialJSON != nil {
					if structuredOutputToolName != "" {
						// Structured output: stream JSON as content
						streamResponse := &schemas.BifrostChatResponse{
							Object: "chat.completion.chunk",
							Choices: []schemas.BifrostResponseChoice{
								{
									Index: 0,
									ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
										Delta: &schemas.ChatStreamResponseChoiceDelta{
											Content: chunk.Delta.PartialJSON,
										},
									},
								},
							},
						}
						return streamResponse, nil, false
					}
					// Create streaming response for tool input delta
					streamResponse := &schemas.BifrostChatResponse{
						Object: "chat.completion.chunk",
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: 0,
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
					thinkingText := *chunk.Delta.Thinking
					// Create streaming response for thinking delta
					streamResponse := &schemas.BifrostChatResponse{
						Object: "chat.completion.chunk",
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: 0,
								ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
									Delta: &schemas.ChatStreamResponseChoiceDelta{
										Reasoning: schemas.Ptr(thinkingText),
										ReasoningDetails: []schemas.ChatReasoningDetails{
											{
												Index: 0,
												Type:  schemas.BifrostReasoningDetailsTypeText,
												Text:  schemas.Ptr(thinkingText),
											},
										},
									},
								},
							},
						},
					}

					return streamResponse, nil, false
				}

			case AnthropicStreamDeltaTypeSignature:
				if chunk.Delta.Signature != nil && *chunk.Delta.Signature != "" {
					// Create streaming response for signature delta
					streamResponse := &schemas.BifrostChatResponse{
						Object: "chat.completion.chunk",
						Choices: []schemas.BifrostResponseChoice{
							{
								Index: 0,
								ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
									Delta: &schemas.ChatStreamResponseChoiceDelta{
										ReasoningDetails: []schemas.ChatReasoningDetails{
											{
												Index:     0,
												Type:      schemas.BifrostReasoningDetailsTypeText,
												Signature: chunk.Delta.Signature,
											},
										},
									},
								},
							},
						},
					}
					return streamResponse, nil, false
				}
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

// ToAnthropicChatStreamResponse converts a Bifrost streaming response to Anthropic SSE string format
func ToAnthropicChatStreamResponse(bifrostResp *schemas.BifrostChatResponse) string {
	if bifrostResp == nil {
		return ""
	}

	streamResp := &AnthropicStreamEvent{}

	// Handle different streaming event types based on the response content
	if len(bifrostResp.Choices) > 0 {
		choice := bifrostResp.Choices[0] // Anthropic typically returns one choice

		// Handle streaming responses
		if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
			delta := choice.ChatStreamResponseChoice.Delta

			// Handle text content deltas
			if delta.Content != nil {
				streamResp.Type = "content_block_delta"
				streamResp.Index = &choice.Index
				streamResp.Delta = &AnthropicStreamDelta{
					Type: AnthropicStreamDeltaTypeText,
					Text: delta.Content,
				}
			} else if delta.Reasoning != nil {
				// Handle thinking content deltas
				streamResp.Type = "content_block_delta"
				streamResp.Index = &choice.Index
				streamResp.Delta = &AnthropicStreamDelta{
					Type:     AnthropicStreamDeltaTypeThinking,
					Thinking: delta.Reasoning,
				}
			} else if len(delta.ReasoningDetails) > 0 && delta.ReasoningDetails[0].Signature != nil && *delta.ReasoningDetails[0].Signature != "" {
				// Handle signature deltas
				streamResp.Type = "content_block_delta"
				streamResp.Index = &choice.Index
				streamResp.Delta = &AnthropicStreamDelta{
					Type:      AnthropicStreamDeltaTypeSignature,
					Signature: delta.ReasoningDetails[0].Signature,
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
				stopReason := ConvertBifrostFinishReasonToAnthropic(*choice.FinishReason)
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

// ToAnthropicChatStreamError converts a BifrostError to Anthropic streaming error in SSE format
func ToAnthropicChatStreamError(bifrostErr *schemas.BifrostError) string {
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
