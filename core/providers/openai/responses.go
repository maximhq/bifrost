package openai

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostResponsesRequest converts an OpenAI responses request to Bifrost format
func (request *OpenAIResponsesRequest) ToBifrostResponsesRequest() *schemas.BifrostResponsesRequest {
	if request == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(request.Model, schemas.OpenAI)

	input := request.Input.OpenAIResponsesRequestInputArray
	if len(input) == 0 {
		input = []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: request.Input.OpenAIResponsesRequestInputStr},
			},
		}
	}

	return &schemas.BifrostResponsesRequest{
		Provider:  provider,
		Model:     model,
		Input:     input,
		Params:    &request.ResponsesParameters,
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}
}

// ToOpenAIResponsesRequest converts a Bifrost responses request to OpenAI format
func ToOpenAIResponsesRequest(bifrostReq *schemas.BifrostResponsesRequest) *OpenAIResponsesRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	var messages []schemas.ResponsesMessage
	// OpenAI models (except for gpt-oss) do not support reasoning content blocks, so we need to convert them to summaries, if there are any
	messages = make([]schemas.ResponsesMessage, 0, len(bifrostReq.Input))
	for _, message := range bifrostReq.Input {
		if message.ResponsesReasoning != nil {
			// If the message has no summaries and encrypted content but has content blocks, and the model is not gpt-oss, skip the message
			if len(message.ResponsesReasoning.Summary) == 0 &&
				message.Content != nil &&
				len(message.Content.ContentBlocks) > 0 &&
				!strings.Contains(bifrostReq.Model, "gpt-oss") &&
				message.ResponsesReasoning.EncryptedContent == nil {
				continue
			}

			// If the message has summaries but no content blocks and the model is gpt-oss, then convert the summaries to content blocks
			if len(message.ResponsesReasoning.Summary) > 0 &&
				strings.Contains(bifrostReq.Model, "gpt-oss") &&
				len(message.ResponsesReasoning.Summary) > 0 &&
				message.Content == nil {
				var newMessage schemas.ResponsesMessage
				newMessage.ID = message.ID
				newMessage.Type = message.Type
				newMessage.Status = message.Status
				newMessage.Role = message.Role

				// Convert summaries to content blocks
				var contentBlocks []schemas.ResponsesMessageContentBlock
				for _, summary := range message.ResponsesReasoning.Summary {
					contentBlocks = append(contentBlocks, schemas.ResponsesMessageContentBlock{
						Type: schemas.ResponsesOutputMessageContentTypeReasoning,
						Text: &summary.Text,
					})
				}
				newMessage.Content = &schemas.ResponsesMessageContent{
					ContentBlocks: contentBlocks,
				}
				messages = append(messages, newMessage)
			} else {
				messages = append(messages, message)
			}
		} else {
			messages = append(messages, message)
		}
	}
	// Updating params
	params := bifrostReq.Params
	// Create the responses request with properly mapped parameters
	req := &OpenAIResponsesRequest{
		Model: bifrostReq.Model,
		Input: OpenAIResponsesRequestInput{
			OpenAIResponsesRequestInputArray: messages,
		},
	}

	if params != nil {
		req.ResponsesParameters = *params
		// Filter out tools that OpenAI doesn't support
		req.filterUnsupportedTools()
	}

	return req
}

// filterUnsupportedTools removes tool types that OpenAI doesn't support
func (req *OpenAIResponsesRequest) filterUnsupportedTools() {
	if len(req.Tools) == 0 {
		return
	}

	// Define OpenAI-supported tool types
	supportedTypes := map[schemas.ResponsesToolType]bool{
		schemas.ResponsesToolTypeFunction:           true,
		schemas.ResponsesToolTypeFileSearch:         true,
		schemas.ResponsesToolTypeComputerUsePreview: true,
		schemas.ResponsesToolTypeWebSearch:          true,
		schemas.ResponsesToolTypeMCP:                true,
		schemas.ResponsesToolTypeCodeInterpreter:    true,
		schemas.ResponsesToolTypeImageGeneration:    true,
		schemas.ResponsesToolTypeLocalShell:         true,
		schemas.ResponsesToolTypeCustom:             true,
		schemas.ResponsesToolTypeWebSearchPreview:   true,
	}

	// Filter tools to only include supported types
	filteredTools := make([]schemas.ResponsesTool, 0, len(req.Tools))
	for _, tool := range req.Tools {
		if supportedTypes[tool.Type] {
			filteredTools = append(filteredTools, tool)
		}
	}
	req.Tools = filteredTools
}
