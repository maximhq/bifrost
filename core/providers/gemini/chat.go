package gemini

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

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
				if settings, ok := SafeExtractSafetySettings(safetySettings); ok {
					geminiReq.SafetySettings = settings
				}
			}

			// Cached content
			if cachedContent, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["cached_content"]); ok {
				geminiReq.CachedContent = cachedContent
			}

			// Labels
			if labels, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "labels"); ok {
				if labelMap, ok := schemas.SafeExtractStringMap(labels); ok {
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

	// Handle empty candidates (filtered/malformed responses)
	if len(response.Candidates) == 0 {
		finishReason := ConvertGeminiFinishReasonToBifrost(FinishReasonMalformedFunctionCall)
		return createErrorResponse(response, finishReason, false)
	}

	candidate := response.Candidates[0]

	// Check for filtered finish reasons that indicate errors
	if isErrorFinishReason(candidate.FinishReason) {
		finishReason := ConvertGeminiFinishReasonToBifrost(candidate.FinishReason)
		return createErrorResponse(response, finishReason, false)
	}

	// Collect all content and tool calls into a single message
	var toolCalls []schemas.ChatAssistantMessageToolCall
	var contentBlocks []schemas.ChatContentBlock
	var reasoningDetails []schemas.ChatReasoningDetails
	var contentStr *string

	// Process candidate content to extract text, tool calls, and reasoning
	if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
		for _, part := range candidate.Content.Parts {
			// Handle thought/reasoning text separately - add to reasoning details
			if part.Text != "" && part.Thought {
				reasoningDetails = append(reasoningDetails, schemas.ChatReasoningDetails{
					Index: len(reasoningDetails),
					Type:  schemas.BifrostReasoningDetailsTypeText,
					Text:  &part.Text,
				})
				continue
			}

			// Handle regular text
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

				// Extract thought signature from CallID if embedded (Gemini 3 behavior)
				var extractedSig []byte
				if strings.Contains(callID, thoughtSignatureSeparator) {
					parts := strings.SplitN(callID, thoughtSignatureSeparator, 2)
					if len(parts) == 2 {
						if decoded, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
							extractedSig = decoded
							callID = parts[0] // Use base ID without signature for the tool call
						}
					}
				}

				toolCall := schemas.ChatAssistantMessageToolCall{
					Index:    uint16(len(toolCalls)),
					Type:     schemas.Ptr(string(schemas.ChatToolChoiceTypeFunction)),
					ID:       &callID,
					Function: function,
				}

				toolCalls = append(toolCalls, toolCall)

				// If we extracted a signature from CallID, add it to reasoning details
				if len(extractedSig) > 0 {
					thoughtSig := base64.StdEncoding.EncodeToString(extractedSig)
					reasoningDetails = append(reasoningDetails, schemas.ChatReasoningDetails{
						Index:     len(reasoningDetails),
						Type:      schemas.BifrostReasoningDetailsTypeEncrypted,
						Signature: &thoughtSig,
						ID:        schemas.Ptr(fmt.Sprintf("tool_call_%s", callID)),
					})
				}
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

			// Handle standalone thought signature (not embedded in CallID)
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
					// Strip signature from ID if present
					if strings.Contains(callID, thoughtSignatureSeparator) {
						parts := strings.SplitN(callID, thoughtSignatureSeparator, 2)
						if len(parts) == 2 {
							callID = parts[0]
						}
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

	// Handle empty candidates (filtered/malformed responses)
	if len(response.Candidates) == 0 {
		finishReason := ConvertGeminiFinishReasonToBifrost(FinishReasonMalformedFunctionCall)
		return createErrorResponse(response, finishReason, true), nil, true
	}

	candidate := response.Candidates[0]

	// Check for filtered finish reasons that indicate errors
	if isErrorFinishReason(candidate.FinishReason) {
		finishReason := ConvertGeminiFinishReasonToBifrost(candidate.FinishReason)
		return createErrorResponse(response, finishReason, true), nil, true
	}

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
		var toolCalls []schemas.ChatAssistantMessageToolCall
		var reasoningDetails []schemas.ChatReasoningDetails

		for _, part := range candidate.Content.Parts {
			switch {
			case part.Text != "" && part.Thought:
				// Thought/reasoning content - add to reasoning details
				reasoningDetails = append(reasoningDetails, schemas.ChatReasoningDetails{
					Index: len(reasoningDetails),
					Type:  schemas.BifrostReasoningDetailsTypeText,
					Text:  &part.Text,
				})

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

				// Extract thought signature from CallID if embedded (Gemini 3 behavior)
				var extractedSig []byte
				if strings.Contains(callID, thoughtSignatureSeparator) {
					parts := strings.SplitN(callID, thoughtSignatureSeparator, 2)
					if len(parts) == 2 {
						if decoded, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
							extractedSig = decoded
							callID = parts[0] // Use base ID without signature for the tool call
						}
					}
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

				// If we extracted a signature from CallID, add it to reasoning details
				if len(extractedSig) > 0 {
					thoughtSig := base64.StdEncoding.EncodeToString(extractedSig)
					reasoningDetails = append(reasoningDetails, schemas.ChatReasoningDetails{
						Index:     len(reasoningDetails),
						Type:      schemas.BifrostReasoningDetailsTypeEncrypted,
						Signature: &thoughtSig,
					})
				}

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
	hasDeltaContent := delta.Role != nil || delta.Content != nil || len(delta.ToolCalls) > 0 || len(delta.ReasoningDetails) > 0
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

// isErrorFinishReason checks if a finish reason indicates a filtered or error response
func isErrorFinishReason(reason FinishReason) bool {
	return reason == FinishReasonSafety ||
		reason == FinishReasonRecitation ||
		reason == FinishReasonMalformedFunctionCall ||
		reason == FinishReasonBlocklist ||
		reason == FinishReasonProhibitedContent ||
		reason == FinishReasonSPII ||
		reason == FinishReasonImageSafety ||
		reason == FinishReasonUnexpectedToolCall
}

// createErrorResponse creates a complete BifrostChatResponse for error cases
func createErrorResponse(response *GenerateContentResponse, finishReason string, isStream bool) *schemas.BifrostChatResponse {
	var choice schemas.BifrostResponseChoice
	if isStream {
		choice = schemas.BifrostResponseChoice{
			Index:        0,
			FinishReason: &finishReason,
			ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
				Delta: &schemas.ChatStreamResponseChoiceDelta{},
			},
		}
	} else {
		choice = schemas.BifrostResponseChoice{
			Index:        0,
			FinishReason: &finishReason,
			ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
				Message: &schemas.ChatMessage{
					Role:    schemas.ChatMessageRoleAssistant,
					Content: &schemas.ChatMessageContent{},
				},
			},
		}
	}

	objectType := "chat.completion"
	if isStream {
		objectType = "chat.completion.chunk"
	}

	errorResp := &schemas.BifrostChatResponse{
		ID:      response.ResponseID,
		Model:   response.ModelVersion,
		Object:  objectType,
		Choices: []schemas.BifrostResponseChoice{choice},
		Usage:   convertGeminiUsageMetadataToChatUsage(response.UsageMetadata),
	}

	if !response.CreateTime.IsZero() {
		errorResp.Created = int(response.CreateTime.Unix())
	}

	return errorResp
}
