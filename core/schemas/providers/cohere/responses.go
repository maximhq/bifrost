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

	cohereReq := &CohereChatRequest{
		Model: bifrostReq.Model,
	}

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
		if bifrostReq.Params.TopLogProbs != nil {
			cohereReq.K = bifrostReq.Params.TopLogProbs
		}
		if bifrostReq.Params.ExtraParams != nil {
			if stop, ok := bifrostReq.Params.ExtraParams["stop"].([]string); ok {
				cohereReq.StopSequences = &stop
			}
			if frequencyPenalty, ok := bifrostReq.Params.ExtraParams["frequency_penalty"].(float64); ok {
				cohereReq.FrequencyPenalty = &frequencyPenalty
			}
			if presencePenalty, ok := bifrostReq.Params.ExtraParams["presence_penalty"].(float64); ok {
				cohereReq.PresencePenalty = &presencePenalty
			}
		}
	}

	// Convert tools
	if bifrostReq.Params.Tools != nil {
		var cohereTools []CohereChatRequestTool
		for _, tool := range bifrostReq.Params.Tools {
			if tool.ResponsesToolFunction != nil {
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
			cohereReq.Tools = &cohereTools
		}
	}

	// Convert tool choice
	if bifrostReq.Params.ToolChoice != nil {
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
	var cohereMessages []CohereMessage
	var systemContent []string

	for _, msg := range messages {
		switch *msg.Type {
		case schemas.ResponsesMessageTypeMessage:
			role := string(*msg.Role)
			if role == "system" {
				// Collect system messages separately for Cohere
				if msg.Content.ContentStr != nil {
					systemContent = append(systemContent, *msg.Content.ContentStr)
				}
			} else {
				cohereMsg := CohereMessage{
					Role: role,
				}

				// Convert content
				if msg.Content.ContentStr != nil {
					cohereMsg.Content = NewStringContent(*msg.Content.ContentStr)
				} else if msg.Content.ContentBlocks != nil {
					contentBlocks := convertResponsesMessageContentBlocksToCohere(*msg.Content.ContentBlocks)
					cohereMsg.Content = NewBlocksContent(contentBlocks)
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
				assistantMsg.ToolCalls = &cohereToolCalls
			}

			cohereMessages = append(cohereMessages, assistantMsg)

		case "function_call_output":
			// Handle function call outputs
			if msg.ResponsesToolMessage != nil && msg.ResponsesToolMessage.CallID != nil {
				toolMsg := CohereMessage{
					Role: "tool",
				}

				if msg.ResponsesToolMessage.CallID != nil {
					toolMsg.ToolCallID = msg.ResponsesToolMessage.CallID
				}

				cohereMessages = append(cohereMessages, toolMsg)
			}
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

	return cohereMessages
}

// convertBifrostContentBlocksToCohere converts Bifrost content blocks to Cohere format
func convertResponsesMessageContentBlocksToCohere(blocks []schemas.ResponsesMessageContentBlock) []CohereContentBlock {
	var cohereBlocks []CohereContentBlock

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
			if block.ImageURL != nil && *block.ImageURL != "" {
				cohereBlocks = append(cohereBlocks, CohereContentBlock{
					Type: CohereContentBlockTypeImage,
					ImageURL: &CohereImageURL{
						URL: *block.ImageURL,
					},
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
			for _, block := range *cohereMsg.Content.BlocksContent {
				contentBlocks = append(contentBlocks, convertCohereContentBlockToBifrost(block))
			}

			content.ContentBlocks = &contentBlocks
		}

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
		for _, toolCall := range *cohereMsg.ToolCalls {
			toolCallMsg := schemas.ResponsesMessage{
				ID:     toolCall.ID,
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Name:      toolCall.Function.Name,
					CallID:    toolCall.ID,
					Arguments: schemas.Ptr(toolCall.Function.Arguments),
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
