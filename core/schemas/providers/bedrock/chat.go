package bedrock

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBedrockChatCompletionRequest converts a Bifrost request to Bedrock Converse API format
func ToBedrockChatCompletionRequest(bifrostReq *schemas.BifrostChatRequest) (*BedrockConverseRequest, error) {
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
	convertChatParameters(bifrostReq, bedrockReq)

	// Ensure tool config is present when needed
	ensureChatToolConfigForConversation(bifrostReq, bedrockReq)

	return bedrockReq, nil
}

// ToBifrostResponse converts a Bedrock Converse API response to Bifrost format
func (bedrockResp *BedrockConverseResponse) ToBifrostResponse() (*schemas.BifrostResponse, error) {
	if bedrockResp == nil {
		return nil, fmt.Errorf("bedrock response is nil")
	}

	// Convert content blocks and tool calls
	var contentBlocks []schemas.ChatContentBlock
	var toolCalls []schemas.ChatAssistantMessageToolCall

	if bedrockResp.Output.Message != nil {
		for _, contentBlock := range bedrockResp.Output.Message.Content {
			// Handle text content
			if contentBlock.Text != nil && *contentBlock.Text != "" {
				contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
					Type: schemas.ChatContentBlockTypeText,
					Text: contentBlock.Text,
				})
			}

			// Handle tool use
			if contentBlock.ToolUse != nil {
				// Marshal the tool input to JSON string
				var arguments string
				if contentBlock.ToolUse.Input != nil {
					if argBytes, err := sonic.Marshal(contentBlock.ToolUse.Input); err == nil {
						arguments = string(argBytes)
					} else {
						arguments = "{}"
					}
				} else {
					arguments = "{}"
				}

				// Create copies of the values to avoid range loop variable capture
				toolUseID := contentBlock.ToolUse.ToolUseID
				toolUseName := contentBlock.ToolUse.Name

				toolCalls = append(toolCalls, schemas.ChatAssistantMessageToolCall{
					Type: schemas.Ptr("function"),
					ID:   &toolUseID,
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      &toolUseName,
						Arguments: arguments,
					},
				})
			}
		}
	}

	// Create assistant message if we have tool calls
	var assistantMessage *schemas.ChatAssistantMessage
	if len(toolCalls) > 0 {
		assistantMessage = &schemas.ChatAssistantMessage{
			ToolCalls: toolCalls,
		}
	}

	// Create the message content
	messageContent := schemas.ChatMessageContent{}
	if len(contentBlocks) > 0 {
		messageContent.ContentBlocks = contentBlocks
	}

	// Create the response choice
	choices := []schemas.BifrostChatResponseChoice{
		{
			Index: 0,
			BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
				Message: schemas.ChatMessage{
					Role:                 schemas.ChatMessageRoleAssistant,
					Content:              messageContent,
					ChatAssistantMessage: assistantMessage,
				},
			},
			FinishReason: &bedrockResp.StopReason,
		},
	}
	var usage *schemas.LLMUsage
	if bedrockResp.Usage != nil {
		// Convert usage information
		usage = &schemas.LLMUsage{
			PromptTokens:     bedrockResp.Usage.InputTokens,
			CompletionTokens: bedrockResp.Usage.OutputTokens,
			TotalTokens:      bedrockResp.Usage.TotalTokens,
		}
	}

	// Calculate latency
	var latency float64
	if bedrockResp.Metrics != nil {
		latency = float64(bedrockResp.Metrics.LatencyMs)
	}

	// Create the final Bifrost response
	bifrostResponse := &schemas.BifrostResponse{
		Choices: choices,
		Usage:   usage,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.ChatCompletionRequest,
			Latency:     &latency,
			Provider:    schemas.Bedrock,
		},
	}

	return bifrostResponse, nil
}
