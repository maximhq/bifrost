package bedrock

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// contentBlockMeta stores type and length of a content block for multi-turn reconstruction.
type contentBlockMeta struct {
	T string `json:"t"` // "thinking" or "text"
	L int    `json:"l"` // length in UTF-8 bytes
}

// ToBedrockChatCompletionRequest converts a Bifrost request to Bedrock Converse API format
func ToBedrockChatCompletionRequest(ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostChatRequest) (*BedrockConverseRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost request is nil")
	}

	if bifrostReq.Input == nil {
		return nil, fmt.Errorf("only chat completion requests are supported for Bedrock Converse API")
	}

	bedrockReq := &BedrockConverseRequest{
		ModelID: bifrostReq.Model,
	}

	// Convert messages and system messages
	messages, systemMessages, err := convertMessages(bifrostReq.Input)
	if err != nil {
		return nil, fmt.Errorf("failed to convert messages: %w", err)
	}
	bedrockReq.Messages = messages
	if len(systemMessages) > 0 {
		bedrockReq.System = systemMessages
	}

	// Convert parameters and configurations
	if err := convertChatParameters(ctx, bifrostReq, bedrockReq); err != nil {
		return nil, fmt.Errorf("failed to convert chat parameters: %w", err)
	}

	// Ensure tool config is present when needed
	ensureChatToolConfigForConversation(bifrostReq, bedrockReq)

	return bedrockReq, nil
}

// ToBifrostChatResponse converts a Bedrock Converse API response to Bifrost format
func (response *BedrockConverseResponse) ToBifrostChatResponse(ctx context.Context, model string) (*schemas.BifrostChatResponse, error) {
	if response == nil {
		return nil, fmt.Errorf("bedrock response is nil")
	}

	// Convert content blocks and tool calls
	var contentStr *string
	var contentBlocks []schemas.ChatContentBlock
	var toolCalls []schemas.ChatAssistantMessageToolCall
	var reasoningDetails []schemas.ChatReasoningDetails
	var reasoningText string

	if response.Output.Message != nil {
		for _, contentBlock := range response.Output.Message.Content {
			// Handle text content
			if contentBlock.Text != nil && *contentBlock.Text != "" {
				chatContentBlock := schemas.ChatContentBlock{
					Type: schemas.ChatContentBlockTypeText,
					Text: contentBlock.Text,
				}
				contentBlocks = append(contentBlocks, chatContentBlock)
			}

			if contentBlock.ToolUse != nil {
				// Check if this is the structured output tool
				if structuredOutputToolName, ok := ctx.Value(schemas.BifrostContextKeyStructuredOutputToolName).(string); ok && contentBlock.ToolUse.Name == structuredOutputToolName {
					// This is structured output - set contentStr and skip adding to toolCalls
					if contentBlock.ToolUse.Input != nil {
						jsonStr := string(contentBlock.ToolUse.Input)
						contentStr = &jsonStr
					}
					continue // Skip adding to toolCalls
				}

				// Regular tool call processing
				var arguments string
				if contentBlock.ToolUse.Input != nil {
					arguments = string(contentBlock.ToolUse.Input)
				} else {
					arguments = "{}"
				}

				toolUseID := contentBlock.ToolUse.ToolUseID
				toolUseName := contentBlock.ToolUse.Name

				toolCalls = append(toolCalls, schemas.ChatAssistantMessageToolCall{
					Index: uint16(len(toolCalls)),
					Type:  schemas.Ptr("function"),
					ID:    &toolUseID,
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      &toolUseName,
						Arguments: arguments,
					},
				})
			}

			// Handle reasoning content
			if contentBlock.ReasoningContent != nil {
				if contentBlock.ReasoningContent.ReasoningText == nil {
					continue
				}
				reasoningDetails = append(reasoningDetails, schemas.ChatReasoningDetails{
					Index:     len(reasoningDetails),
					Type:      schemas.BifrostReasoningDetailsTypeText,
					Text:      contentBlock.ReasoningContent.ReasoningText.Text,
					Signature: contentBlock.ReasoningContent.ReasoningText.Signature,
				})
				if contentBlock.ReasoningContent.ReasoningText.Text != nil {
					reasoningText += *contentBlock.ReasoningContent.ReasoningText.Text + "\n"
				}
			}

			// Handle document content
			if contentBlock.Document != nil {
				fileBlock := schemas.ChatContentBlock{
					Type: schemas.ChatContentBlockTypeFile,
					File: &schemas.ChatInputFile{},
				}

				// Set filename from document name
				if contentBlock.Document.Name != "" {
					fileBlock.File.Filename = &contentBlock.Document.Name
				}

				// Set file type based on format
				if contentBlock.Document.Format != "" {
					var fileType string
					switch contentBlock.Document.Format {
					case "pdf":
						fileType = "application/pdf"
					case "txt":
						fileType = "text/plain"
					case "md":
						fileType = "text/markdown"
					case "html":
						fileType = "text/html"
					case "csv":
						fileType = "text/csv"
					case "doc":
						fileType = "application/msword"
					case "docx":
						fileType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
					case "xls":
						fileType = "application/vnd.ms-excel"
					case "xlsx":
						fileType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
					default:
						fileType = "application/pdf"
					}
					fileBlock.File.FileType = &fileType
				}

				// Convert document source data
				if contentBlock.Document.Source != nil {
					if contentBlock.Document.Source.Bytes != nil {
						fileBlock.File.FileData = contentBlock.Document.Source.Bytes
					} else if contentBlock.Document.Source.Text != nil {
						fileBlock.File.FileData = contentBlock.Document.Source.Text
					}
				}

				contentBlocks = append(contentBlocks, fileBlock)
			}
		}
	}

	// We do merging of text blocks for Anthropic models while using /openai integration
	if len(contentBlocks) > 0 && schemas.IsAnthropicModel(model) && ctx.Value(schemas.BifrostContextKeyIntegrationType) == "openai" {
		allText := true
		for _, block := range contentBlocks {
			if block.Type != schemas.ChatContentBlockTypeText {
				allText = false
				break
			}
		}
		if allText {
			needsCombine := len(contentBlocks) > 1 || len(reasoningDetails) > 0
			if !needsCombine {
				// Single text block, no thinking — simple collapse
				contentStr = contentBlocks[0].Text
			} else {
				// Combine thinking (first) + text blocks into a single string
				var parts []string
				var blockMeta []contentBlockMeta

				// Thinking blocks first
				for _, rd := range reasoningDetails {
					if rd.Type == schemas.BifrostReasoningDetailsTypeText && rd.Text != nil {
						parts = append(parts, *rd.Text)
						blockMeta = append(blockMeta, contentBlockMeta{T: "thinking", L: len(*rd.Text)})
					}
				}

				// Then text blocks top to bottom
				for _, block := range contentBlocks {
					if block.Text != nil {
						parts = append(parts, *block.Text)
						blockMeta = append(blockMeta, contentBlockMeta{T: "text", L: len(*block.Text)})
					}
				}

				joined := strings.Join(parts, "\n\n")
				contentStr = &joined

				// Record boundaries for multi-turn reconstruction
				if len(blockMeta) > 1 {
					if metaJSON, err := providerUtils.MarshalSorted(blockMeta); err == nil {
						metaStr := string(metaJSON)
						reasoningDetails = append(reasoningDetails, schemas.ChatReasoningDetails{
							Index: len(reasoningDetails),
							Type:  schemas.BifrostReasoningDetailsTypeContentBlocks,
							Text:  &metaStr,
						})
					}
				}

				// Clear thinking text from reasoning_details (keep signature only)
				for i := range reasoningDetails {
					if reasoningDetails[i].Type == schemas.BifrostReasoningDetailsTypeText {
						reasoningDetails[i].Text = nil
					}
				}
			}
			contentBlocks = nil
		}
	}

	// Create the message content
	messageContent := schemas.ChatMessageContent{
		ContentStr:    contentStr,
		ContentBlocks: contentBlocks,
	}

	// Create assistant message if we have tool calls
	var assistantMessage *schemas.ChatAssistantMessage
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

	// Create the response choice
	choices := []schemas.BifrostResponseChoice{
		{
			Index: 0,
			ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
				Message: &schemas.ChatMessage{
					Role:                 schemas.ChatMessageRoleAssistant,
					Content:              &messageContent,
					ChatAssistantMessage: assistantMessage,
				},
			},
			FinishReason: schemas.Ptr(convertBedrockStopReason(response.StopReason)),
		},
	}
	var usage *schemas.BifrostLLMUsage
	if response.Usage != nil {
		// Convert usage information
		usage = &schemas.BifrostLLMUsage{
			PromptTokens:     response.Usage.InputTokens,
			CompletionTokens: response.Usage.OutputTokens,
			TotalTokens:      response.Usage.TotalTokens,
		}
		// Handle cached tokens if present
		if response.Usage.CacheReadInputTokens > 0 {
			if usage.PromptTokensDetails == nil {
				usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
			}
			usage.PromptTokensDetails.CachedReadTokens = response.Usage.CacheReadInputTokens
			usage.PromptTokens = usage.PromptTokens + response.Usage.CacheReadInputTokens
		}
		if response.Usage.CacheWriteInputTokens > 0 {
			if usage.PromptTokensDetails == nil {
				usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{}
			}
			usage.PromptTokensDetails.CachedWriteTokens = response.Usage.CacheWriteInputTokens
			usage.PromptTokens = usage.PromptTokens + response.Usage.CacheWriteInputTokens
		}
	}

	// Create the final Bifrost response
	bifrostResponse := &schemas.BifrostChatResponse{
		ID:      uuid.New().String(),
		Model:   model,
		Object:  "chat.completion",
		Choices: choices,
		Usage:   usage,
		Created: int(time.Now().Unix()),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.ChatCompletionRequest,
			Provider:    schemas.Bedrock,
		},
	}

	if response.ServiceTier != nil && response.ServiceTier.Type != "" {
		bifrostResponse.ServiceTier = &response.ServiceTier.Type
	}

	return bifrostResponse, nil
}

// BedrockStreamState tracks per-stream tool call index state.
type BedrockStreamState struct {
	nextToolCallIndex         int
	contentBlockToToolCallIdx map[int]int
}

// NewBedrockStreamState returns initialised stream state for one streaming response.
func NewBedrockStreamState() *BedrockStreamState {
	return &BedrockStreamState{
		contentBlockToToolCallIdx: make(map[int]int),
	}
}

func (chunk *BedrockStreamEvent) ToBifrostChatCompletionStream(state *BedrockStreamState) (*schemas.BifrostChatResponse, *schemas.BifrostError, bool) {
	if state == nil {
		state = NewBedrockStreamState()
	} else if state.contentBlockToToolCallIdx == nil {
		state.contentBlockToToolCallIdx = make(map[int]int)
	}

	// event with metrics/usage is the last and with stop reason is the second last
	switch {
	case chunk.Role != nil:
		// Send empty response to signal start
		streamResponse := &schemas.BifrostChatResponse{
			Object: "chat.completion.chunk",
			Choices: []schemas.BifrostResponseChoice{
				{
					Index: 0,
					ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
						Delta: &schemas.ChatStreamResponseChoiceDelta{
							Role: chunk.Role,
						},
					},
				},
			},
		}

		return streamResponse, nil, false

	case chunk.Start != nil && chunk.Start.ToolUse != nil:
		toolUseStart := chunk.Start.ToolUse

		toolCallIdx := 0
		if chunk.ContentBlockIndex != nil {
			toolCallIdx = state.nextToolCallIndex
			state.contentBlockToToolCallIdx[*chunk.ContentBlockIndex] = toolCallIdx
			state.nextToolCallIndex++
		}

		// Create tool call structure for start event
		var toolCall schemas.ChatAssistantMessageToolCall
		toolCall.Index = uint16(toolCallIdx)
		toolCall.ID = schemas.Ptr(toolUseStart.ToolUseID)
		toolCall.Type = schemas.Ptr("function")
		toolCall.Function.Name = schemas.Ptr(toolUseStart.Name)
		toolCall.Function.Arguments = "" // Start with empty arguments

		streamResponse := &schemas.BifrostChatResponse{
			Object: "chat.completion.chunk",
			Choices: []schemas.BifrostResponseChoice{
				{
					Index: 0,
					ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
						Delta: &schemas.ChatStreamResponseChoiceDelta{
							ToolCalls: []schemas.ChatAssistantMessageToolCall{toolCall},
						},
					},
				},
			},
		}

		return streamResponse, nil, false

	case chunk.Delta != nil:
		switch {
		case chunk.Delta.Text != nil:
			// Handle text delta
			text := *chunk.Delta.Text
			if text != "" {
				streamResponse := &schemas.BifrostChatResponse{
					Object: "chat.completion.chunk",
					Choices: []schemas.BifrostResponseChoice{
						{
							Index: 0,
							ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
								Delta: &schemas.ChatStreamResponseChoiceDelta{
									Content: &text,
								},
							},
						},
					},
				}

				return streamResponse, nil, false
			}

		case chunk.Delta.ToolUse != nil:
			// Handle tool use delta
			toolUseDelta := chunk.Delta.ToolUse

			toolCallIdx := 0
			if chunk.ContentBlockIndex != nil {
				toolCallIdx = state.contentBlockToToolCallIdx[*chunk.ContentBlockIndex]
			}

			// Create tool call structure
			var toolCall schemas.ChatAssistantMessageToolCall
			toolCall.Index = uint16(toolCallIdx)
			toolCall.Type = schemas.Ptr("function")

			// For streaming, we need to accumulate tool use data
			// This is a simplified approach - in practice, you'd need to track tool calls across chunks
			toolCall.Function.Arguments = toolUseDelta.Input

			streamResponse := &schemas.BifrostChatResponse{
				Object: "chat.completion.chunk",
				Choices: []schemas.BifrostResponseChoice{
					{
						Index: 0,
						ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
							Delta: &schemas.ChatStreamResponseChoiceDelta{
								ToolCalls: []schemas.ChatAssistantMessageToolCall{toolCall},
							},
						},
					},
				},
			}

			return streamResponse, nil, false

		case chunk.Delta.ReasoningContent != nil:
			// Handle reasoning content delta
			reasoningContentDelta := chunk.Delta.ReasoningContent

			// Only construct and return a response when either Text or Signature is set
			if (reasoningContentDelta.Text == nil || *reasoningContentDelta.Text == "") && reasoningContentDelta.Signature == nil {
				return nil, nil, false
			}

			var streamResponse *schemas.BifrostChatResponse
			if reasoningContentDelta.Text != nil && *reasoningContentDelta.Text != "" {
				streamResponse = &schemas.BifrostChatResponse{
					Object: "chat.completion.chunk",
					Choices: []schemas.BifrostResponseChoice{
						{
							Index: 0,
							ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
								Delta: &schemas.ChatStreamResponseChoiceDelta{
									Reasoning: reasoningContentDelta.Text,
									ReasoningDetails: []schemas.ChatReasoningDetails{
										{
											Index: 0,
											Type:  schemas.BifrostReasoningDetailsTypeText,
											Text:  reasoningContentDelta.Text,
										},
									},
								},
							},
						},
					},
				}
			} else if reasoningContentDelta.Signature != nil {
				streamResponse = &schemas.BifrostChatResponse{
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
											Signature: reasoningContentDelta.Signature,
										},
									},
								},
							},
						},
					},
				}
			}

			return streamResponse, nil, false
		}
	}

	return nil, nil, false
}
