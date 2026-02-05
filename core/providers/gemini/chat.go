package gemini

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToGeminiChatCompletionRequest converts a BifrostChatRequest to Gemini's generation request format for chat completion
func ToGeminiChatCompletionRequest(bifrostReq *schemas.BifrostChatRequest) *GeminiGenerationRequest {
	if bifrostReq == nil {
		return nil
	}

	// Create the base Gemini generation request
	geminiReq := &GeminiGenerationRequest{
		Model: bifrostReq.Model,
	}

	// Convert parameters to generation config
	if bifrostReq.Params != nil {
		geminiReq.GenerationConfig = convertParamsToGenerationConfig(bifrostReq.Params, []string{}, bifrostReq.Model)

		// Handle tool-related parameters
		if len(bifrostReq.Params.Tools) > 0 {
			geminiReq.Tools = convertBifrostToolsToGemini(bifrostReq.Params.Tools)

			// Convert tool choice to tool config
			if bifrostReq.Params.ToolChoice != nil {
				geminiReq.ToolConfig = convertToolChoiceToToolConfig(bifrostReq.Params.ToolChoice)
			}
		}

		// Handle extra parameters
		if bifrostReq.Params.ExtraParams != nil {
			// Safety settings
			if safetySettings, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "safety_settings"); ok {
				if settings, ok := safetySettings.([]SafetySetting); ok {
					geminiReq.SafetySettings = settings
				}
			}

			// Cached content
			if cachedContent, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["cached_content"]); ok {
				geminiReq.CachedContent = cachedContent
			}

			// Labels
			if labels, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "labels"); ok {
				if labelMap, ok := labels.(map[string]string); ok {
					geminiReq.Labels = labelMap
				}
			}
		}
	}

	// Convert chat completion messages to Gemini format
	contents, systemInstruction := convertBifrostMessagesToGemini(bifrostReq.Input)
	if systemInstruction != nil {
		geminiReq.SystemInstruction = systemInstruction
	}
	geminiReq.Contents = contents

	return geminiReq
}

// ToBifrostChatResponse converts a GenerateContentResponse to a BifrostChatResponse
func (response *GenerateContentResponse) ToBifrostChatResponse() *schemas.BifrostChatResponse {
	bifrostResp := &schemas.BifrostChatResponse{
		ID:     response.ResponseID,
		Model:  response.ModelVersion,
		Object: "chat.completion",
	}

	// Set creation timestamp if available
	if !response.CreateTime.IsZero() {
		bifrostResp.Created = int(response.CreateTime.Unix())
	}

	// Collect all content and tool calls into a single message
	var toolCalls []schemas.ChatAssistantMessageToolCall
	var contentBlocks []schemas.ChatContentBlock
	var reasoningDetails []schemas.ChatReasoningDetails
	var contentStr *string

	// Process candidates to extract text content
	if len(response.Candidates) > 0 {
		candidate := response.Candidates[0]
		if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
						Type: schemas.ChatContentBlockTypeText,
						Text: &part.Text,
					})
				}

				if part.FunctionCall != nil {
					function := schemas.ChatAssistantMessageToolCallFunction{
						Name: &part.FunctionCall.Name,
					}

					if part.FunctionCall.Args != nil {
						jsonArgs, err := json.Marshal(part.FunctionCall.Args)
						if err != nil {
							jsonArgs = []byte(fmt.Sprintf("%v", part.FunctionCall.Args))
						}
						function.Arguments = string(jsonArgs)
					}

					callID := part.FunctionCall.Name
					if part.FunctionCall.ID != "" {
						callID = part.FunctionCall.ID
					}

					toolCall := schemas.ChatAssistantMessageToolCall{
						Index:    uint16(len(toolCalls)),
						Type:     schemas.Ptr(string(schemas.ChatToolChoiceTypeFunction)),
						ID:       &callID,
						Function: function,
					}

					toolCalls = append(toolCalls, toolCall)
				}

				if part.FunctionResponse != nil {
					// Extract the output from the response
					output := extractFunctionResponseOutput(part.FunctionResponse)

					// Add as text content block
					if output != "" {
						contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
							Type: schemas.ChatContentBlockTypeText,
							Text: &output,
						})
					}
				}
				if part.ThoughtSignature != nil {
					thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
					reasoningDetail := schemas.ChatReasoningDetails{
						Index:     len(reasoningDetails),
						Type:      schemas.BifrostReasoningDetailsTypeEncrypted,
						Signature: &thoughtSig,
					}

					// check if part is tool call
					if part.FunctionCall != nil {
						callID := part.FunctionCall.Name
						if part.FunctionCall.ID != "" {
							callID = part.FunctionCall.ID
						}
						reasoningDetail.ID = schemas.Ptr(fmt.Sprintf("tool_call_%s", callID))
					}

					reasoningDetails = append(reasoningDetails, reasoningDetail)
				}
			}

			// Build the choice with message
			message := &schemas.ChatMessage{
				Role: schemas.ChatMessageRoleAssistant,
			}

			if len(contentBlocks) == 1 && contentBlocks[0].Type == schemas.ChatContentBlockTypeText {
				contentStr = contentBlocks[0].Text
				contentBlocks = nil
			}

			message.Content = &schemas.ChatMessageContent{
				ContentStr:    contentStr,
				ContentBlocks: contentBlocks,
			}

			if len(toolCalls) > 0 || len(reasoningDetails) > 0 {
				message.ChatAssistantMessage = &schemas.ChatAssistantMessage{
					ToolCalls:        toolCalls,
					ReasoningDetails: reasoningDetails,
				}
			}

			bifrostResp.Choices = append(bifrostResp.Choices, schemas.BifrostResponseChoice{
				Index: 0,
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: message,
				},
			})
		}
	}

	// Set usage information
	bifrostResp.Usage = convertGeminiUsageMetadataToChatUsage(response.UsageMetadata)

	return bifrostResp
}

// ToBifrostChatCompletionStream converts a Gemini streaming response to a Bifrost Chat Completion Stream response
// Returns the response, error (if any), and a boolean indicating if this is the last chunk
func (response *GenerateContentResponse) ToBifrostChatCompletionStream() (*schemas.BifrostChatResponse, *schemas.BifrostError, bool) {
	if response == nil {
		return nil, nil, false
	}

	// Check if we have candidates with content
	if len(response.Candidates) == 0 {
		return nil, nil, false
	}

	candidate := response.Candidates[0]

	// Determine if this is the last chunk based on finish reason and usage metadata
	isLastChunk := candidate.FinishReason != "" && response.UsageMetadata != nil

	// Create the streaming response
	streamResponse := &schemas.BifrostChatResponse{
		ID:     response.ResponseID,
		Model:  response.ModelVersion,
		Object: "chat.completion.chunk",
	}

	// Set creation timestamp if available
	if !response.CreateTime.IsZero() {
		streamResponse.Created = int(response.CreateTime.Unix())
	}

	// Build delta content
	delta := &schemas.ChatStreamResponseChoiceDelta{}

	// Process content parts
	if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
		// Set role from the first chunk (Gemini uses "model" for assistant)
		if candidate.Content.Role != "" {
			role := candidate.Content.Role
			if role == string(RoleModel) {
				role = string(schemas.ChatMessageRoleAssistant)
			}
			delta.Role = &role
		}

		var textContent string
		var thoughtContent string
		var toolCalls []schemas.ChatAssistantMessageToolCall
		var reasoningDetails []schemas.ChatReasoningDetails

		for _, part := range candidate.Content.Parts {
			switch {
			case part.Text != "" && part.Thought:
				// Thought/reasoning content
				thoughtContent += part.Text

			case part.Text != "":
				// Regular text content
				textContent += part.Text

			case part.FunctionCall != nil:
				// Function call
				jsonArgs := ""
				if part.FunctionCall.Args != nil {
					if argsBytes, err := json.Marshal(part.FunctionCall.Args); err == nil {
						jsonArgs = string(argsBytes)
					}
				}

				// Use ID if available, otherwise use function name
				callID := part.FunctionCall.Name
				if part.FunctionCall.ID != "" {
					callID = part.FunctionCall.ID
				}

				toolCall := schemas.ChatAssistantMessageToolCall{
					Index: uint16(len(toolCalls)),
					Type:  schemas.Ptr(string(schemas.ChatToolTypeFunction)),
					ID:    &callID,
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      &part.FunctionCall.Name,
						Arguments: jsonArgs,
					},
				}

				toolCalls = append(toolCalls, toolCall)

			case part.FunctionResponse != nil:
				// Extract the output from the response and add to text content
				output := extractFunctionResponseOutput(part.FunctionResponse)
				if output != "" {
					textContent += output
				}
			}

			// Handle thought signature separately (not part of the switch since it can co-exist with other types)
			if part.ThoughtSignature != nil {
				thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
				reasoningDetails = append(reasoningDetails, schemas.ChatReasoningDetails{
					Index:     len(reasoningDetails),
					Type:      schemas.BifrostReasoningDetailsTypeEncrypted,
					Signature: &thoughtSig,
				})
			}
		}

		// Set text content if present
		if textContent != "" {
			delta.Content = &textContent
		}

		// Set thought content if present
		if thoughtContent != "" {
			delta.Reasoning = &thoughtContent
		}

		// Set reasoning details if present
		if len(reasoningDetails) > 0 {
			delta.ReasoningDetails = reasoningDetails
		}

		// Set tool calls if present
		if len(toolCalls) > 0 {
			delta.ToolCalls = toolCalls
		}
	}

	// Check if delta has any content - if not and it's not the last chunk, skip it
	hasDeltaContent := delta.Role != nil || delta.Content != nil || delta.Reasoning != nil || len(delta.ToolCalls) > 0 || len(delta.ReasoningDetails) > 0
	if !hasDeltaContent && !isLastChunk {
		return nil, nil, false
	}

	// Build the choice
	var finishReason *string
	if isLastChunk && candidate.FinishReason != "" {
		reason := ConvertGeminiFinishReasonToBifrost(candidate.FinishReason)
		finishReason = &reason
	}

	choice := schemas.BifrostResponseChoice{
		Index:        int(candidate.Index),
		FinishReason: finishReason,
		ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
			Delta: delta,
		},
	}

	streamResponse.Choices = []schemas.BifrostResponseChoice{choice}

	// Add usage information if this is the last chunk
	if isLastChunk && response.UsageMetadata != nil {
		streamResponse.Usage = convertGeminiUsageMetadataToChatUsage(response.UsageMetadata)
	}

	return streamResponse, nil, isLastChunk
}
