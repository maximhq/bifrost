package cohere

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ConvertFromResponsesAPIRequest converts a BifrostRequest (ResponsesAPI structure) back to CohereChatRequest
func ConvertFromResponsesAPIRequest(bifrostReq *schemas.BifrostRequest) (*CohereChatRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrostReq cannot be nil")
	}

	cohereReq := &CohereChatRequest{
		Model: bifrostReq.Model,
	}

	// Map basic parameters
	if bifrostReq.Params != nil {
		if bifrostReq.Params.MaxTokens != nil {
			cohereReq.MaxTokens = bifrostReq.Params.MaxTokens
		}
		if bifrostReq.Params.Temperature != nil {
			cohereReq.Temperature = bifrostReq.Params.Temperature
		}
		if bifrostReq.Params.TopP != nil {
			cohereReq.P = bifrostReq.Params.TopP
		}
		if bifrostReq.Params.TopK != nil {
			cohereReq.K = bifrostReq.Params.TopK
		}
		if bifrostReq.Params.StopSequences != nil {
			cohereReq.StopSequences = bifrostReq.Params.StopSequences
		}
		if bifrostReq.Params.FrequencyPenalty != nil {
			cohereReq.FrequencyPenalty = bifrostReq.Params.FrequencyPenalty
		}
		if bifrostReq.Params.PresencePenalty != nil {
			cohereReq.PresencePenalty = bifrostReq.Params.PresencePenalty
		}
	}

	// TODO: Convert tools and tool choice - field mapping to be resolved
	// For now, skip tool conversion to avoid import cycle issues

	// Process ChatCompletionInput (which contains the ResponsesAPI items)
	if bifrostReq.Input.ChatCompletionInput != nil {
		messages, err := convertResponsesAPIItemsToCohereMessages(*bifrostReq.Input.ChatCompletionInput)
		if err != nil {
			return nil, fmt.Errorf("failed to convert messages: %w", err)
		}
		cohereReq.Messages = messages
	}

	return cohereReq, nil
}

// ConvertCohereResponseToResponsesAPI converts CohereChatResponse to BifrostResponse (ResponsesAPI structure)
func ConvertCohereResponseToResponsesAPI(cohereResp *CohereChatResponse) (*schemas.BifrostResponse, error) {
	if cohereResp == nil {
		return nil, fmt.Errorf("cohereResp cannot be nil")
	}

	bifrostResp := &schemas.BifrostResponse{
		ID:                          cohereResp.ID,
		Model:                       "", // Will be set by provider
		ResponseAPIExtendedResponse: &schemas.ResponseAPIExtendedResponse{},
	}

	// Convert usage information
	if cohereResp.Usage != nil {
		usage := &schemas.LLMUsage{}
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

	// Convert output message to ResponsesAPI format
	if cohereResp.Message != nil {
		outputMessages := convertCohereMessageToResponsesAPIOutput(*cohereResp.Message)
		bifrostResp.ResponseAPIExtendedResponse.Output = &outputMessages
	}

	// Set finish reason - note: ResponseAPIExtendedResponse doesn't have FinishReason field
	// This would be handled at the BifrostResponse level if needed

	return bifrostResp, nil
}

// Helper functions

// convertBifrostToolToCohere converts schemas.Tool to Cohere tool format
func convertBifrostToolToCohere(tool schemas.Tool) schemas.Tool {
	// Cohere uses the same tool format as Bifrost, so just return as-is
	return tool
}

// convertResponsesAPIToolChoiceToCohere converts schemas.ToolChoice to CohereToolChoice
func convertResponsesAPIToolChoiceToCohere(toolChoice schemas.ToolChoice) CohereToolChoice {
	// Simple conversion for now - to be enhanced when field structure is clarified
	return "" // Default to auto behavior
}

// convertResponsesAPIItemsToCohereMessages converts ResponsesAPI items back to Cohere messages
func convertResponsesAPIItemsToCohereMessages(messages []schemas.BifrostMessage) ([]CohereMessage, error) {
	var cohereMessages []CohereMessage
	var systemContent []string

	for _, msg := range messages {
		// Handle ResponsesAPI items
		if msg.ResponsesAPIExtendedBifrostMessage != nil && msg.ResponsesAPIExtendedBifrostMessage.Type != nil {
			switch *msg.ResponsesAPIExtendedBifrostMessage.Type {
			case "message":
				// Extract role from the ResponsesAPI message structure
				role := string(msg.Role)
				if role == "system" {
					// Collect system messages separately
					if msg.Content.ContentStr != nil {
						systemContent = append(systemContent, *msg.Content.ContentStr)
					}
				} else {
					// Convert regular message
					cohereMsg := CohereMessage{
						Role: role,
					}

					// Convert content
					if msg.Content.ContentStr != nil {
						cohereMsg.Content = NewStringContent(*msg.Content.ContentStr)
					} else if msg.Content.ContentBlocks != nil {
						contentBlocks := convertBifrostContentBlocksToCohere(*msg.Content.ContentBlocks)
						cohereMsg.Content = NewBlocksContent(contentBlocks)
					}

					cohereMessages = append(cohereMessages, cohereMsg)
				}

			case "function_call":
				// Handle tool calls - extract from AssistantMessage.ToolCalls
				if msg.AssistantMessage != nil &&
					msg.AssistantMessage.ChatCompletionsAssistantMessage != nil &&
					msg.AssistantMessage.ChatCompletionsAssistantMessage.ToolCalls != nil {

					for _, toolCall := range *msg.AssistantMessage.ChatCompletionsAssistantMessage.ToolCalls {
						cohereToolCall := CohereToolCall{
							Type: "function",
							Function: &CohereFunction{
								Name:      toolCall.Function.Name,
								Arguments: toolCall.Function.Arguments,
							},
						}
						if toolCall.ID != nil {
							cohereToolCall.ID = toolCall.ID
						}

						// Create or find assistant message for this tool call
						assistantMsg := CohereMessage{
							Role:      "assistant",
							ToolCalls: &[]CohereToolCall{cohereToolCall},
						}
						cohereMessages = append(cohereMessages, assistantMsg)
					}
				}

			case "function_call_output":
				// Handle tool results
				if msg.ToolMessage != nil && msg.ToolMessage.ChatCompletionsToolMessage != nil {
					resultMsg := CohereMessage{
						Role: "tool",
					}
					if msg.ToolMessage.ChatCompletionsToolMessage.ToolCallID != nil {
						resultMsg.ToolCallID = msg.ToolMessage.ChatCompletionsToolMessage.ToolCallID
					}
					if msg.Content.ContentStr != nil {
						resultMsg.Content = NewStringContent(*msg.Content.ContentStr)
					}
					cohereMessages = append(cohereMessages, resultMsg)
				}
			}
		} else {
			// Handle regular BifrostMessage format
			cohereMsg := CohereMessage{
				Role: string(msg.Role),
			}

			// Convert content
			if msg.Content.ContentStr != nil {
				cohereMsg.Content = NewStringContent(*msg.Content.ContentStr)
			} else if msg.Content.ContentBlocks != nil {
				contentBlocks := convertBifrostContentBlocksToCohere(*msg.Content.ContentBlocks)
				cohereMsg.Content = NewBlocksContent(contentBlocks)
			}

			// Handle tool calls for assistant messages
			if msg.Role == schemas.ModelChatMessageRoleAssistant &&
				msg.AssistantMessage != nil &&
				msg.AssistantMessage.ChatCompletionsAssistantMessage != nil &&
				msg.AssistantMessage.ChatCompletionsAssistantMessage.ToolCalls != nil {

				var cohereToolCalls []CohereToolCall
				for _, toolCall := range *msg.AssistantMessage.ChatCompletionsAssistantMessage.ToolCalls {
					cohereToolCall := CohereToolCall{
						Type: "function",
						ID:   toolCall.ID,
						Function: &CohereFunction{
							Name:      toolCall.Function.Name,
							Arguments: toolCall.Function.Arguments,
						},
					}
					cohereToolCalls = append(cohereToolCalls, cohereToolCall)
				}
				cohereMsg.ToolCalls = &cohereToolCalls
			}

			// Handle tool results
			if msg.Role == schemas.ModelChatMessageRoleTool &&
				msg.ToolMessage != nil &&
				msg.ToolMessage.ChatCompletionsToolMessage != nil {
				cohereMsg.ToolCallID = msg.ToolMessage.ChatCompletionsToolMessage.ToolCallID
			}

			cohereMessages = append(cohereMessages, cohereMsg)
		}
	}

	// Prepend system messages if any
	if len(systemContent) > 0 {
		systemMsg := CohereMessage{
			Role:    "system",
			Content: NewStringContent(strings.Join(systemContent, "\n")),
		}
		cohereMessages = append([]CohereMessage{systemMsg}, cohereMessages...)
	}

	return cohereMessages, nil
}

// convertBifrostContentBlocksToCohere converts Bifrost content blocks to Cohere format
func convertBifrostContentBlocksToCohere(blocks []schemas.ContentBlock) []CohereContentBlock {
	var cohereBlocks []CohereContentBlock

	for _, block := range blocks {
		switch block.Type {
		case schemas.ContentBlockTypeText:
			if block.Text != nil {
				cohereBlocks = append(cohereBlocks, CohereContentBlock{
					Type: "text",
					Text: block.Text,
				})
			}
		case schemas.ContentBlockTypeImage:
			if block.ImageURL != nil && *block.ImageURL != "" {
				cohereBlocks = append(cohereBlocks, CohereContentBlock{
					Type: "image",
					// Note: CohereContentBlock may need different field structure
				})
			}
		}
	}

	return cohereBlocks
}

// convertCohereMessageToResponsesAPIOutput converts Cohere message to BifrostMessage output format
func convertCohereMessageToResponsesAPIOutput(cohereMsg CohereMessage) []schemas.BifrostMessage {
	var outputMessages []schemas.BifrostMessage

	// Handle text content
	if cohereMsg.Content != nil {
		if cohereMsg.Content.StringContent != nil {
			outputMessages = append(outputMessages, schemas.BifrostMessage{
				Role: schemas.ModelChatMessageRoleAssistant,
				Content: schemas.MessageContent{
					ContentStr: cohereMsg.Content.StringContent,
				},
				ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
					Type: schemas.Ptr("message"),
				},
			})
		} else if cohereMsg.Content.BlocksContent != nil {
			// Process content blocks
			var contentBlocks []schemas.ContentBlock
			for _, block := range *cohereMsg.Content.BlocksContent {
				contentBlocks = append(contentBlocks, convertCohereContentBlockToBifrost(block))
			}

			outputMessages = append(outputMessages, schemas.BifrostMessage{
				Role: schemas.ModelChatMessageRoleAssistant,
				Content: schemas.MessageContent{
					ContentBlocks: &contentBlocks,
				},
				ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
					Type: schemas.Ptr("message"),
				},
			})
		}
	}

	// Handle tool calls
	if cohereMsg.ToolCalls != nil {
		for _, toolCall := range *cohereMsg.ToolCalls {
			toolMsg := schemas.BifrostMessage{
				Role:    schemas.ModelChatMessageRoleAssistant,
				Content: schemas.MessageContent{},
				ResponsesAPIExtendedBifrostMessage: &schemas.ResponsesAPIExtendedBifrostMessage{
					Type:   schemas.Ptr("function_call"),
					ID:     toolCall.ID,
					Status: schemas.Ptr("completed"),
				},
			}
			outputMessages = append(outputMessages, toolMsg)
		}
	}

	return outputMessages
}

// convertCohereContentBlockToBifrost converts CohereContentBlock to schemas.ContentBlock
func convertCohereContentBlockToBifrost(cohereBlock CohereContentBlock) schemas.ContentBlock {
	switch cohereBlock.Type {
	case "text":
		return schemas.ContentBlock{
			Type: schemas.ContentBlockTypeText,
			Text: cohereBlock.Text,
		}
	case "image":
		// Simple fallback for image blocks
		return schemas.ContentBlock{
			Type: schemas.ContentBlockTypeText,
			Text: schemas.Ptr("Image content"),
		}
	default:
		// Fallback to text block
		return schemas.ContentBlock{
			Type: schemas.ContentBlockTypeText,
			Text: &cohereBlock.Type,
		}
	}
}

// convertCohereFinishReasonToBifrost converts CohereFinishReason to Bifrost finish reason
func convertCohereFinishReasonToBifrost(reason CohereFinishReason) string {
	switch reason {
	case FinishReasonComplete:
		return "stop"
	case FinishReasonStopSequence:
		return "stop"
	case FinishReasonMaxTokens:
		return "length"
	case FinishReasonToolCall:
		return "tool_calls"
	case FinishReasonError:
		return "error"
	default:
		return "stop"
	}
}
