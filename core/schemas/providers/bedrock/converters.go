package bedrock

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// ConvertBifrostRequestToBedrock converts a Bifrost request to Bedrock Converse API format
func ConvertBifrostRequestToBedrock(bifrostReq *schemas.BifrostRequest) (*BedrockConverseRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost request is nil")
	}

	if bifrostReq.Input.ChatCompletionInput == nil {
		return nil, fmt.Errorf("only chat completion requests are supported for Bedrock Converse API")
	}

	bedrockReq := &BedrockConverseRequest{
		ModelID: bifrostReq.Model,
	}

	// Convert messages and system messages
	messages, systemMessages, err := convertMessages(*bifrostReq.Input.ChatCompletionInput)
	if err != nil {
		return nil, fmt.Errorf("failed to convert messages: %w", err)
	}
	bedrockReq.Messages = messages
	if len(systemMessages) > 0 {
		bedrockReq.System = &systemMessages
	}
	
	// Convert parameters and configurations
	convertParameters(bifrostReq, bedrockReq)

	// Ensure tool config is present when needed
	ensureToolConfigForConversation(bifrostReq, bedrockReq)

	return bedrockReq, nil
}

// ConvertBedrockResponseToBifrost converts a Bedrock Converse API response to Bifrost format
func ConvertBedrockResponseToBifrost(bedrockResp *BedrockConverseResponse, model string, providerName schemas.ModelProvider) (*schemas.BifrostResponse, error) {
	if bedrockResp == nil {
		return nil, fmt.Errorf("bedrock response is nil")
	}

	// Convert content blocks and tool calls
	var contentBlocks []schemas.ContentBlock
	var toolCalls []schemas.ToolCall

	if bedrockResp.Output.Message != nil {
		for _, contentBlock := range bedrockResp.Output.Message.Content {
			// Handle text content
			if contentBlock.Text != nil && *contentBlock.Text != "" {
				contentBlocks = append(contentBlocks, schemas.ContentBlock{
					Type: schemas.ContentBlockTypeText,
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

				toolCalls = append(toolCalls, schemas.ToolCall{
					Type: schemas.Ptr("function"),
					ID:   &contentBlock.ToolUse.ToolUseID,
					Function: schemas.FunctionCall{
						Name:      &contentBlock.ToolUse.Name,
						Arguments: arguments,
					},
				})
			}
		}
	}

	// Create assistant message if we have tool calls
	var assistantMessage *schemas.AssistantMessage
	if len(toolCalls) > 0 {
		assistantMessage = &schemas.AssistantMessage{
			ToolCalls: &toolCalls,
		}
	}

	// Create the message content
	messageContent := schemas.MessageContent{}
	if len(contentBlocks) > 0 {
		messageContent.ContentBlocks = &contentBlocks
	}

	// Create the response choice
	choices := []schemas.BifrostResponseChoice{
		{
			Index: 0,
			BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
				Message: schemas.BifrostMessage{
					Role:             schemas.ModelChatMessageRoleAssistant,
					Content:          messageContent,
					AssistantMessage: assistantMessage,
				},
			},
			FinishReason: &bedrockResp.StopReason,
		},
	}

	// Convert usage information
	usage := &schemas.LLMUsage{
		PromptTokens:     bedrockResp.Usage.InputTokens,
		CompletionTokens: bedrockResp.Usage.OutputTokens,
		TotalTokens:      bedrockResp.Usage.TotalTokens,
	}

	// Calculate latency
	latency := float64(bedrockResp.Metrics.LatencyMs)

	// Create the final Bifrost response
	bifrostResponse := &schemas.BifrostResponse{
		Choices: choices,
		Usage:   usage,
		Model:   model,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency:  &latency,
			Provider: providerName,
		},
	}

	return bifrostResponse, nil
}
