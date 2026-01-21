package openai

import (
	"strings"

	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostResponsesRequest converts an OpenAI responses request to Bifrost format
func (request *OpenAIResponsesRequest) ToBifrostResponsesRequest(ctx *schemas.BifrostContext) *schemas.BifrostResponsesRequest {
	if request == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(request.Model, utils.CheckAndSetDefaultProvider(ctx, schemas.OpenAI))

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
			// According to OpenAI's Responses API format specification, for non-gpt-oss models, a message
			// with ResponsesReasoning != nil and non-empty Content.ContentBlocks but empty Summary and
			// nil EncryptedContent represents a reasoning-only message that should be skipped, as these
			// models do not support reasoning content blocks in the output. This constraint ensures
			// compatibility with OpenAI's intended responses format behavior where reasoning-only messages
			// without summaries are not included in the request payload for non-gpt-oss models.
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
				message.Content == nil {
				var newMessage schemas.ResponsesMessage
				newMessage.ID = message.ID
				newMessage.Type = message.Type
				newMessage.Status = message.Status
				newMessage.Role = message.Role

				// Convert summaries to content blocks
				contentBlocks := make([]schemas.ResponsesMessageContentBlock, 0, len(message.ResponsesReasoning.Summary))
				for _, summary := range message.ResponsesReasoning.Summary {
					contentBlocks = append(contentBlocks, schemas.ResponsesMessageContentBlock{
						Type: schemas.ResponsesOutputMessageContentTypeReasoning,
						Text: schemas.Ptr(summary.Text),
					})
				}
				newMessage.Content = &schemas.ResponsesMessageContent{
					ContentBlocks: contentBlocks,
				}
				messages = append(messages, newMessage)
			} else {
				messages = append(messages, message)
			}
		} else if message.ResponsesToolMessage != nil &&
			message.ResponsesToolMessage.Action != nil &&
			message.ResponsesToolMessage.Action.ResponsesComputerToolCallAction != nil {
			action := message.ResponsesToolMessage.Action.ResponsesComputerToolCallAction
			if action.Type == "zoom" || action.Region != nil {
				// Copy action and modify
				newAction := *action
				newAction.Region = nil
				if newAction.Type == "zoom" {
					newAction.Type = "screenshot"
				}

				actionStructCopy := *message.ResponsesToolMessage.Action
				actionStructCopy.ResponsesComputerToolCallAction = &newAction

				toolMsgCopy := *message.ResponsesToolMessage
				toolMsgCopy.Action = &actionStructCopy

				message.ResponsesToolMessage = &toolMsgCopy
			}

			messages = append(messages, message)
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
		if req.ResponsesParameters.MaxOutputTokens != nil && *req.ResponsesParameters.MaxOutputTokens < MinMaxCompletionTokens {
			req.ResponsesParameters.MaxOutputTokens = schemas.Ptr(MinMaxCompletionTokens)
		}
		// Drop user field if it exceeds OpenAI's 64 character limit
		req.ResponsesParameters.User = SanitizeUserField(req.ResponsesParameters.User)

		// Handle reasoning parameter: OpenAI uses effort-based reasoning
		// Priority: effort (native) > max_tokens (estimated)
		if req.ResponsesParameters.Reasoning != nil {
			if req.ResponsesParameters.Reasoning.Effort != nil {
				// Native field is provided, use it (and clear max_tokens)
				effort := *req.ResponsesParameters.Reasoning.Effort
				// Convert "minimal" to "low" for non-OpenAI providers
				if effort == "minimal" {
					req.ResponsesParameters.Reasoning.Effort = schemas.Ptr("low")
				}
				// Clear max_tokens since OpenAI doesn't use it
				req.ResponsesParameters.Reasoning.MaxTokens = nil
			} else if req.ResponsesParameters.Reasoning.MaxTokens != nil {
				// Estimate effort from max_tokens
				maxTokens := *req.ResponsesParameters.Reasoning.MaxTokens
				maxOutputTokens := DefaultCompletionMaxTokens
				if req.ResponsesParameters.MaxOutputTokens != nil {
					maxOutputTokens = *req.ResponsesParameters.MaxOutputTokens
				}
				effort := utils.GetReasoningEffortFromBudgetTokens(maxTokens, MinReasoningMaxTokens, maxOutputTokens)
				req.ResponsesParameters.Reasoning.Effort = schemas.Ptr(effort)
				// Clear max_tokens since OpenAI doesn't use it
				req.ResponsesParameters.Reasoning.MaxTokens = nil
			}

			// Handle xAI-specific parameter filtering
			// Only grok-3-mini supports reasoning_effort
			if bifrostReq.Provider == schemas.XAI &&
				schemas.IsGrokReasoningModel(bifrostReq.Model) &&
				!strings.Contains(bifrostReq.Model, "grok-3-mini") {
				// Clear reasoning_effort for non-grok-3-mini xAI reasoning models
				req.ResponsesParameters.Reasoning.Effort = nil
			}
		}

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
			// check for computer use preview
			if tool.Type == schemas.ResponsesToolTypeComputerUsePreview && tool.ResponsesToolComputerUsePreview != nil && tool.ResponsesToolComputerUsePreview.EnableZoom != nil {
				newTool := tool
				newComputerUse := &schemas.ResponsesToolComputerUsePreview{
					DisplayHeight: tool.ResponsesToolComputerUsePreview.DisplayHeight,
					DisplayWidth:  tool.ResponsesToolComputerUsePreview.DisplayWidth,
					Environment:   tool.ResponsesToolComputerUsePreview.Environment,
					// EnableZoom is intentionally omitted (nil) - OpenAI doesn't support it
				}
				newTool.ResponsesToolComputerUsePreview = newComputerUse
				filteredTools = append(filteredTools, newTool)
			} else if tool.Type == schemas.ResponsesToolTypeWebSearch && tool.ResponsesToolWebSearch != nil {
				// Create a proper deep copy with new nested pointers to avoid mutating the original
				newTool := tool
				newWebSearch := &schemas.ResponsesToolWebSearch{}

				// MaxUses is intentionally omitted (nil) - OpenAI doesn't support it

				// Handle Filters: OpenAI doesn't support BlockedDomains or TimeRangeFilter
				if tool.ResponsesToolWebSearch.Filters != nil {
					hasAllowedDomains := len(tool.ResponsesToolWebSearch.Filters.AllowedDomains) > 0

					if hasAllowedDomains {
						// Keep only AllowedDomains (copy the slice to avoid sharing)
						newWebSearch.Filters = &schemas.ResponsesToolWebSearchFilters{
							AllowedDomains: append([]string(nil), tool.ResponsesToolWebSearch.Filters.AllowedDomains...),
							// BlockedDomains and TimeRangeFilter are intentionally omitted - OpenAI doesn't support it
						}
					}
					// If only blocked domains or both empty, Filters stays nil
				}

				// Copy other fields if they exist
				if tool.ResponsesToolWebSearch.UserLocation != nil {
					newWebSearch.UserLocation = tool.ResponsesToolWebSearch.UserLocation
				}
				if tool.ResponsesToolWebSearch.SearchContextSize != nil {
					newWebSearch.SearchContextSize = tool.ResponsesToolWebSearch.SearchContextSize
				}

				newTool.ResponsesToolWebSearch = newWebSearch
				filteredTools = append(filteredTools, newTool)
			} else {
				filteredTools = append(filteredTools, tool)
			}
		}
	}
	req.Tools = filteredTools
}
