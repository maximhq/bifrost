package cohere

import (
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToCohereResponsesRequest converts a BifrostRequest (Responses structure) to CohereChatRequest
func ToCohereResponsesRequest(bifrostReq *schemas.BifrostResponsesRequest) *CohereChatRequest {
	if bifrostReq == nil {
		return nil
	}

	cohereReq := AcquireChatRequest()
	cohereReq.Model = bifrostReq.Model

	// Map basic parameters
	if bifrostReq.Params != nil {
		if bifrostReq.Params.MaxOutputTokens != nil {
			cohereReq.MaxTokens = bifrostReq.Params.MaxOutputTokens
		}
		if bifrostReq.Params.Temperature != nil {
			cohereReq.Temperature = bifrostReq.Params.Temperature
		}
		if bifrostReq.Params.TopP != nil {
			cohereReq.P = bifrostReq.Params.TopP
		}
		if bifrostReq.Params.ExtraParams != nil {
			if topK, ok := schemas.SafeExtractIntPointer(bifrostReq.Params.ExtraParams["top_k"]); ok {
				cohereReq.K = topK
			}
			if stop, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["stop"]); ok {
				cohereReq.StopSequences = stop
			}
			if frequencyPenalty, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["frequency_penalty"]); ok {
				cohereReq.FrequencyPenalty = frequencyPenalty
			}
			if presencePenalty, ok := schemas.SafeExtractFloat64Pointer(bifrostReq.Params.ExtraParams["presence_penalty"]); ok {
				cohereReq.PresencePenalty = presencePenalty
			}
		}
	}

	// Convert tools
	if bifrostReq.Params != nil && bifrostReq.Params.Tools != nil {
		cohereTools := acquireCohereTools()
		for _, tool := range bifrostReq.Params.Tools {
			if tool.ResponsesToolFunction != nil && tool.Name != nil {
				cohereTool := CohereChatRequestTool{
					Type: "function",
					Function: CohereChatRequestFunction{
						Name:        *tool.Name,
						Description: tool.Description,
						Parameters:  tool.ResponsesToolFunction.Parameters,
					},
				}
				cohereTools = append(cohereTools, cohereTool)
			}
		}

		if len(cohereTools) > 0 {
			cohereReq.Tools = cohereTools
		}
	}

	// Convert tool choice
	if bifrostReq.Params != nil && bifrostReq.Params.ToolChoice != nil {
		cohereReq.ToolChoice = convertBifrostToolChoiceToCohereToolChoice(*bifrostReq.Params.ToolChoice)
	}

	// Process ResponsesInput (which contains the Responses items)
	if bifrostReq.Input != nil {
		cohereReq.Messages = convertResponsesMessagesToCohereMessages(bifrostReq.Input)
	}

	return cohereReq
}

// ToResponsesBifrostResponse converts CohereChatResponse to BifrostResponse (Responses structure)
func (cohereResp *CohereChatResponse) ToResponsesBifrostResponse() *schemas.BifrostResponse {
	if cohereResp == nil {
		return nil
	}

	bifrostResp := &schemas.BifrostResponse{
		ID:     cohereResp.ID,
		Object: "response",
		ResponsesResponse: &schemas.ResponsesResponse{
			CreatedAt: int(time.Now().Unix()), // Set current timestamp
		},
	}

	// Convert usage information
	if cohereResp.Usage != nil {
		usage := &schemas.LLMUsage{
			ResponsesExtendedResponseUsage: &schemas.ResponsesExtendedResponseUsage{},
		}

		if cohereResp.Usage.Tokens != nil {
			if cohereResp.Usage.Tokens.InputTokens != nil {
				usage.PromptTokens = int(*cohereResp.Usage.Tokens.InputTokens)
			}
			if cohereResp.Usage.Tokens.OutputTokens != nil {
				usage.CompletionTokens = int(*cohereResp.Usage.Tokens.OutputTokens)
			}
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}

		bifrostResp.Usage = usage
	}

	// Convert output message to Responses format
	if cohereResp.Message != nil {
		outputMessages := convertCohereMessageToResponsesOutput(*cohereResp.Message)
		bifrostResp.ResponsesResponse.Output = outputMessages
	}

	return bifrostResp
}

// Helper functions

// convertBifrostToolChoiceToCohere converts schemas.ToolChoice to CohereToolChoice
func convertBifrostToolChoiceToCohereToolChoice(toolChoice schemas.ResponsesToolChoice) *CohereToolChoice {
	toolChoiceString := toolChoice.ResponsesToolChoiceStr

	if toolChoiceString != nil {
		switch *toolChoiceString {
		case "none":
			choice := ToolChoiceNone
			return &choice
		case "required", "auto", "function":
			choice := ToolChoiceRequired
			return &choice
		default:
			choice := ToolChoiceRequired
			return &choice
		}
	}

	return nil
}

// convertResponsesMessagesToCohereMessages converts Responses items to Cohere messages
func convertResponsesMessagesToCohereMessages(messages []schemas.ResponsesMessage) []CohereMessage {
	cohereMessages := acquireCohereMessages()
	systemContent := acquireCohereStringSlice()

	for _, msg := range messages {
		// Handle nil Type with default
		msgType := schemas.ResponsesMessageTypeMessage
		if msg.Type != nil {
			msgType = *msg.Type
		}

		switch msgType {
		case schemas.ResponsesMessageTypeMessage:
			// Handle nil Role with default
			role := "user"
			if msg.Role != nil {
				role = string(*msg.Role)
			}

			if role == "system" {
				// Collect system messages separately for Cohere
				if msg.Content != nil {
					if msg.Content.ContentStr != nil {
						systemContent = append(systemContent, *msg.Content.ContentStr)
					} else if msg.Content.ContentBlocks != nil {
						for _, block := range msg.Content.ContentBlocks {
							if block.Text != nil {
								systemContent = append(systemContent, *block.Text)
							}
						}
					}
				}
			} else {
				cohereMsg := CohereMessage{
					Role: role,
				}

				// Convert content - only if Content is not nil
				if msg.Content != nil {
					if msg.Content.ContentStr != nil {
						cohereMsg.Content = acquireCohereMessageContent()
						cohereMsg.Content.StringContent = msg.Content.ContentStr
					} else if msg.Content.ContentBlocks != nil {
						cohereMsg.Content = acquireCohereMessageContent()
						cohereMsg.Content.BlocksContent = convertResponsesMessageContentBlocksToCohere(msg.Content.ContentBlocks)
					}
				}

				cohereMessages = append(cohereMessages, cohereMsg)
			}

		case "function_call":
			// Handle function calls from Responses
			assistantMsg := CohereMessage{
				Role: "assistant",
			}

			// Extract function call details
			var cohereToolCalls []CohereToolCall
			toolCall := CohereToolCall{
				Type:     "function",
				Function: &CohereFunction{},
			}

			if msg.ID != nil {
				toolCall.ID = msg.ID
			}

			// Get function details from AssistantMessage
			if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Arguments != nil {
				toolCall.Function.Arguments = *msg.ResponsesToolMessage.Arguments
			}

			// Get name from ToolMessage if available
			if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.Name != nil {
				toolCall.Function.Name = msg.ResponsesToolMessage.Name
			}

			cohereToolCalls = append(cohereToolCalls, toolCall)

			if len(cohereToolCalls) > 0 {
				assistantMsg.ToolCalls = cohereToolCalls
			}

			cohereMessages = append(cohereMessages, assistantMsg)

		case "function_call_output":
			// Handle function call outputs
			if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.CallID != nil {
				toolMsg := CohereMessage{
					Role: "tool",
				}

				// Extract content from ResponsesFunctionToolCallOutput if Content is not set
				// This is needed for OpenAI Responses API which uses an "output" field
				content := msg.Content
				if content == nil && msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput != nil {
					content = &schemas.ResponsesMessageContent{}
					if msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputStr != nil {
						content.ContentStr = msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputStr
					} else if msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputBlocks != nil {
						content.ContentBlocks = msg.ResponsesToolMessage.ResponsesFunctionToolCallOutput.ResponsesFunctionToolCallOutputBlocks
					}
				}

				// Convert content - only if Content is not nil
				if content != nil {
					if content.ContentStr != nil {
						toolMsg.Content = &CohereMessageContent{
							StringContent: content.ContentStr,
						}
					} else if content.ContentBlocks != nil {
						toolMsg.Content = &CohereMessageContent{
							BlocksContent: convertResponsesMessageContentBlocksToCohere(content.ContentBlocks),
						}
					}
				}

				toolMsg.ToolCallID = msg.ResponsesToolMessage.CallID

				cohereMessages = append(cohereMessages, toolMsg)
			}
		}
	}

	// Prepend system messages if any
	if len(systemContent) > 0 {
		systemMsg := CohereMessage{
			Role: "system",
			Content: &CohereMessageContent{
				StringContent: schemas.Ptr(strings.Join(systemContent, "\n")),
			},
		}
		cohereMessages = append([]CohereMessage{systemMsg}, cohereMessages...)
	}

	return cohereMessages
}

// convertBifrostContentBlocksToCohere converts Bifrost content blocks to Cohere format
func convertResponsesMessageContentBlocksToCohere(blocks []schemas.ResponsesMessageContentBlock) []CohereContentBlock {
	cohereBlocks := acquireCohereContentBlocks()

	for _, block := range blocks {
		switch block.Type {
		case schemas.ResponsesInputMessageContentBlockTypeText:
			if block.Text != nil {
				cohereBlocks = append(cohereBlocks, CohereContentBlock{
					Type: CohereContentBlockTypeText,
					Text: block.Text,
				})
			}
		case schemas.ResponsesInputMessageContentBlockTypeImage:
			if block.ResponsesInputMessageContentBlockImage != nil && block.ResponsesInputMessageContentBlockImage.ImageURL != nil && *block.ResponsesInputMessageContentBlockImage.ImageURL != "" {
				imageURL := acquireCohereImageURL()
				imageURL.URL = *block.ResponsesInputMessageContentBlockImage.ImageURL
				cohereBlocks = append(cohereBlocks, CohereContentBlock{
					Type:     CohereContentBlockTypeImage,
					ImageURL: imageURL,
				})
			}
		case schemas.ResponsesOutputMessageContentTypeReasoning:
			if block.Text != nil {
				cohereBlocks = append(cohereBlocks, CohereContentBlock{
					Type:     CohereContentBlockTypeThinking,
					Thinking: block.Text,
				})
			}
		}
	}

	return cohereBlocks
}

// convertCohereMessageToResponsesOutput converts Cohere message to Responses output format
func convertCohereMessageToResponsesOutput(cohereMsg CohereMessage) []schemas.ResponsesMessage {
	var outputMessages []schemas.ResponsesMessage

	// Handle text content first
	if cohereMsg.Content != nil {
		var content schemas.ResponsesMessageContent

		var contentBlocks []schemas.ResponsesMessageContentBlock

		if cohereMsg.Content.StringContent != nil {
			contentBlocks = append(contentBlocks, schemas.ResponsesMessageContentBlock{
				Type: schemas.ResponsesInputMessageContentBlockTypeText,
				Text: cohereMsg.Content.StringContent,
			})
		} else if cohereMsg.Content.BlocksContent != nil {
			// Convert content blocks
			for _, block := range cohereMsg.Content.BlocksContent {
				contentBlocks = append(contentBlocks, convertCohereContentBlockToBifrost(block))
			}
		}
		content.ContentBlocks = contentBlocks

		// Create message output
		if content.ContentBlocks != nil {
			outputMsg := schemas.ResponsesMessage{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &content,
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			}

			outputMessages = append(outputMessages, outputMsg)
		}
	}

	// Handle tool calls
	if cohereMsg.ToolCalls != nil {
		for _, toolCall := range cohereMsg.ToolCalls {
			// Check if Function is nil to avoid nil pointer dereference
			if toolCall.Function == nil {
				// Skip this tool call if Function is nil
				continue
			}

			// Safely extract function name and arguments
			var functionName *string
			var functionArguments *string

			if toolCall.Function.Name != nil {
				functionName = toolCall.Function.Name
			} else {
				// Use empty string if Name is nil
				functionName = schemas.Ptr("")
			}

			// Arguments is a string, not a pointer, so it's safe to access directly
			functionArguments = schemas.Ptr(toolCall.Function.Arguments)

			toolCallMsg := schemas.ResponsesMessage{
				ID:     toolCall.ID,
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Name:      functionName,
					CallID:    toolCall.ID,
					Arguments: functionArguments,
				},
			}

			outputMessages = append(outputMessages, toolCallMsg)
		}
	}

	return outputMessages
}

// convertCohereContentBlockToBifrost converts CohereContentBlock to schemas.ContentBlock for Responses
func convertCohereContentBlockToBifrost(cohereBlock CohereContentBlock) schemas.ResponsesMessageContentBlock {
	switch cohereBlock.Type {
	case CohereContentBlockTypeText:
		return schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesInputMessageContentBlockTypeText,
			Text: cohereBlock.Text,
		}
	case CohereContentBlockTypeImage:
		// For images, create a text block describing the image
		if cohereBlock.ImageURL == nil {
			// Skip invalid image blocks without ImageURL
			return schemas.ResponsesMessageContentBlock{}
		}
		return schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesInputMessageContentBlockTypeImage,
			ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
				ImageURL: &cohereBlock.ImageURL.URL,
			},
		}
	case CohereContentBlockTypeThinking:
		return schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesOutputMessageContentTypeReasoning,
			Text: cohereBlock.Thinking,
		}
	default:
		// Fallback to text block
		return schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesInputMessageContentBlockTypeText,
			Text: schemas.Ptr(string(cohereBlock.Type)),
		}
	}
}
