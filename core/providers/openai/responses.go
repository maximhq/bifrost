package openai

import (
	"strings"

	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostResponsesRequest converts an OpenAI responses request to Bifrost format
func (resp *OpenAIResponsesRequest) ToBifrostResponsesRequest(ctx *schemas.BifrostContext) *schemas.BifrostResponsesRequest {
	if resp == nil {
		return nil
	}

	defaultProvider := schemas.OpenAI

	// for requests coming from azure sdk without provider prefix, we need to set the default provider to azure
	if ctx != nil {
		if isAzureUser, ok := ctx.Value(schemas.BifrostContextKeyIsAzureUserAgent).(bool); ok && isAzureUser {
			defaultProvider = schemas.Azure
		}
	}

	provider, model := schemas.ParseModelString(resp.Model, utils.CheckAndSetDefaultProvider(ctx, defaultProvider))

	input := resp.Input.OpenAIResponsesRequestInputArray
	if len(input) == 0 {
		input = []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: resp.Input.OpenAIResponsesRequestInputStr},
			},
		}
	}

	return &schemas.BifrostResponsesRequest{
		Provider:  provider,
		Model:     model,
		Input:     input,
		Params:    &resp.ResponsesParameters,
		Fallbacks: schemas.ParseFallbacks(resp.Fallbacks),
	}
}

// ToOpenAIResponsesRequest converts a Bifrost responses request to OpenAI format
func ToOpenAIResponsesRequest(bifrostReq *schemas.BifrostResponsesRequest) *OpenAIResponsesRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	var messages []schemas.ResponsesMessage
	// OpenAI models (except for gpt-oss) do not support reasoning content blocks, so we need to convert them to summaries, if there are any
	// OpenAI also doesn't support compaction content blocks, so we need to convert them to text blocks
	messages = make([]schemas.ResponsesMessage, 0, len(bifrostReq.Input))
	for _, message := range bifrostReq.Input {
		// First, check if message has compaction content blocks and convert them to text
		if message.Content != nil && len(message.Content.ContentBlocks) > 0 {
			hasCompaction := false
			for _, block := range message.Content.ContentBlocks {
				if block.Type == schemas.ResponsesOutputMessageContentTypeCompaction {
					hasCompaction = true
					break
				}
			}

			if hasCompaction {
				// Create a new message with converted content blocks
				newMessage := message
				newContentBlocks := make([]schemas.ResponsesMessageContentBlock, 0, len(message.Content.ContentBlocks))

				for _, block := range message.Content.ContentBlocks {
					if block.Type == schemas.ResponsesOutputMessageContentTypeCompaction {
						// Convert compaction block to text block
						if block.ResponsesOutputMessageContentCompaction != nil && block.ResponsesOutputMessageContentCompaction.Summary != "" {
							newContentBlocks = append(newContentBlocks, schemas.ResponsesMessageContentBlock{
								Type: schemas.ResponsesOutputMessageContentTypeText,
								Text: schemas.Ptr(block.ResponsesOutputMessageContentCompaction.Summary),
							})
						}
						// If summary is empty, skip the block entirely
					} else {
						// Keep non-compaction blocks as-is
						newContentBlocks = append(newContentBlocks, block)
					}
				}

				// Only update if we have blocks remaining after conversion
				if len(newContentBlocks) > 0 {
					newMessage.Content = &schemas.ResponsesMessageContent{
						ContentBlocks: newContentBlocks,
					}
					message = newMessage
				} else {
					// If all blocks were compaction with empty summaries, skip message
					continue
				}
			}
		}

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

	if bifrostReq.Params != nil {
		req.ExtraParams = bifrostReq.Params.ExtraParams
	}
	return req
}

// filterUnsupportedTools removes tool types that OpenAI doesn't support
func (resp *OpenAIResponsesRequest) filterUnsupportedTools() {
	if len(resp.Tools) == 0 {
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
	filteredTools := make([]schemas.ResponsesTool, 0, len(resp.Tools))
	for _, tool := range resp.Tools {
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
	resp.Tools = filteredTools
}
