package gemini

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToGeminiChatCompletionRequest converts a BifrostChatRequest to Gemini's generation request format for chat completion
func ToGeminiChatCompletionRequest(ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostChatRequest) (*GeminiGenerationRequest, error) {
	return ToGeminiChatCompletionRequestWithImageURLSchemes(ctx, bifrostReq, defaultGeminiImageURLSchemes...)
}

// ToGeminiChatCompletionRequestWithImageURLSchemes converts a BifrostChatRequest
// to Gemini format using the provider-specific allowlist for non-data image URLs.
func ToGeminiChatCompletionRequestWithImageURLSchemes(ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostChatRequest, allowedImageURLSchemes ...string) (*GeminiGenerationRequest, error) {
	if bifrostReq == nil {
		return nil, nil
	}

	bifrostReq.Model = NormalizeModelName(bifrostReq.Model)

	// Create the base Gemini generation request
	geminiReq := &GeminiGenerationRequest{
		Model: bifrostReq.Model,
	}

	// Canonical model for capability gating only; wire model is untouched.
	capModel := NormalizeModelName(schemas.ResolveCanonicalModel(ctx, bifrostReq.Model))

	// Convert parameters to generation config
	if bifrostReq.Params != nil {
		geminiReq.ExtraParams = bifrostReq.Params.ExtraParams
		var err error
		geminiReq.GenerationConfig, err = convertParamsToGenerationConfig(bifrostReq.Params, []string{}, bifrostReq.Provider, capModel)
		if err != nil {
			return nil, err
		}
		// Handle tool-related parameters
		if len(bifrostReq.Params.Tools) > 0 {
			geminiReq.Tools, err = convertBifrostToolsToGemini(bifrostReq.Params.Tools)
			if err != nil {
				return nil, err
			}

			// Convert tool choice if present, but only when function declarations exist.
			// Gemini rejects functionCallingConfig without function_declarations
			// (e.g. a tools list holding only non-function types yields no declarations).
			if bifrostReq.Params.ToolChoice != nil {
				hasFunctionDeclarations := false
				for _, tool := range geminiReq.Tools {
					if len(tool.FunctionDeclarations) > 0 {
						hasFunctionDeclarations = true
						break
					}
				}
				if hasFunctionDeclarations {
					geminiReq.ToolConfig = convertToolChoiceToToolConfig(bifrostReq.Params.ToolChoice)
				}
			}
		}

		// Map OpenAI web_search_options to Google Search grounding
		if bifrostReq.Params.WebSearchOptions != nil {
			googleSearch := &GoogleSearch{}
			if filters := bifrostReq.Params.WebSearchOptions.Filters; filters != nil {
				googleSearch.ExcludeDomains = filters.BlockedDomains
				if filters.TimeRangeFilter != nil {
					googleSearch.TimeRangeFilter = &Interval{
						StartTime: filters.TimeRangeFilter.StartTime,
						EndTime:   filters.TimeRangeFilter.EndTime,
					}
				}
			}
			geminiReq.Tools = append(geminiReq.Tools, Tool{GoogleSearch: googleSearch})
		}

		if bifrostReq.Params.IncludeServerSideToolInvocations != nil && *bifrostReq.Params.IncludeServerSideToolInvocations {
			applyServerSideToolInvocations(geminiReq)
		}

		if bifrostReq.Params.ServiceTier != nil {
			geminiReq.ServiceTier = mapBifrostServiceTierToGemini(*bifrostReq.Params.ServiceTier)
		}

		// Handle extra parameters
		if bifrostReq.Params.ExtraParams != nil {
			// Safety settings
			if safetySettings, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "safety_settings"); ok {
				delete(geminiReq.ExtraParams, "safety_settings")
				if settings, ok := SafeExtractSafetySettings(safetySettings); ok {
					geminiReq.SafetySettings = settings
				}
			}

			// Cached content
			if cachedContent, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["cached_content"]); ok {
				delete(geminiReq.ExtraParams, "cached_content")
				geminiReq.CachedContent = cachedContent
			}

			// Labels
			if labels, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "labels"); ok {
				delete(geminiReq.ExtraParams, "labels")
				if labelMap, ok := schemas.SafeExtractStringMap(labels); ok {
					geminiReq.Labels = labelMap
				}
			}
		}
	}
	// Convert chat completion messages to Gemini format.
	//
	// The trailing-assistant trim is the Chat Completions counterpart of the one in
	// convertResponsesMessagesToGeminiContents: Gemini answers 400 for any conversation whose
	// last turn is role:"model", regardless of which Bifrost API shaped it. See
	// trimTrailingAssistantPrefill for why prefill is the only trailing model turn dropped.
	input := trimTrailingChatAssistantPrefill(bifrostReq.Input, schemas.ResolveModelCaps(bifrostReq.Provider, capModel))
	contents, systemInstruction, err := convertBifrostMessagesToGemini(input, allowedImageURLSchemes...)
	if err != nil {
		return nil, err
	}
	if systemInstruction != nil {
		geminiReq.SystemInstruction = systemInstruction
	}
	geminiReq.Contents = contents
	return geminiReq, nil
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

	// Process candidate content to extract text, tool calls, and reasoning.
	//
	// The guard covers only the parts loop, not the choice below it. A thinking model
	// that spends its whole output budget before emitting a visible token returns a
	// candidate with no Content at all -- MAX_TOKENS, reasoning tokens billed, nothing
	// to show. That is a successful empty answer rather than a filtered one (MAX_TOKENS
	// is deliberately absent from isErrorFinishReason), and the chat-completions
	// contract has no way to say "no choices": a nil array marshals to `"choices":null`,
	// which OpenAI-shaped clients dereference blind. OpenAI answers the same truncation
	// with one choice carrying empty content and finish_reason "length", so Bifrost does
	// too. ToBifrostChatCompletionStream already builds its choice outside this guard.
	if candidate.Content != nil {
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
				// Add thought signature to reasoning details if present with text
				if len(part.ThoughtSignature) > 0 {
					thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
					reasoningDetails = append(reasoningDetails, schemas.ChatReasoningDetails{
						Index:     len(reasoningDetails),
						Type:      schemas.BifrostReasoningDetailsTypeEncrypted,
						Signature: &thoughtSig,
					})
				}
			}
			if part.FunctionCall != nil {
				function := schemas.ChatAssistantMessageToolCallFunction{
					Name: &part.FunctionCall.Name,
				}

				if len(part.FunctionCall.Args) > 0 {
					function.Arguments = string(part.FunctionCall.Args)
				}

				callID := part.FunctionCall.Name
				if part.FunctionCall.ID != "" {
					callID = part.FunctionCall.ID
				}

				// Embed thought signature into CallID if present (matches responses.go pattern)
				if len(part.ThoughtSignature) > 0 && !strings.Contains(callID, thoughtSignatureSeparator) {
					encoded := base64.RawURLEncoding.EncodeToString(part.ThoughtSignature)
					callID = fmt.Sprintf("%s%s%s", callID, thoughtSignatureSeparator, encoded)
				}

				toolCall := schemas.ChatAssistantMessageToolCall{
					Index:    uint16(len(toolCalls)),
					Type:     schemas.Ptr(string(schemas.ChatToolChoiceTypeFunction)),
					ID:       &callID,
					Function: function,
				}

				toolCalls = append(toolCalls, toolCall)

				// Also add to reasoning details for backward compatibility
				if len(part.ThoughtSignature) > 0 {
					thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
					// Extract base ID without signature for reasoning detail lookup
					baseCallID := callID
					if strings.Contains(callID, thoughtSignatureSeparator) {
						parts := strings.SplitN(callID, thoughtSignatureSeparator, 2)
						if len(parts) == 2 {
							baseCallID = parts[0]
						}
					}
					reasoningDetails = append(reasoningDetails, schemas.ChatReasoningDetails{
						Index:     len(reasoningDetails),
						Type:      schemas.BifrostReasoningDetailsTypeEncrypted,
						Signature: &thoughtSig,
						ID:        schemas.Ptr(fmt.Sprintf("tool_call_%s", baseCallID)),
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

			// Handle code execution results
			if part.CodeExecutionResult != nil {
				output := part.CodeExecutionResult.Output
				if part.CodeExecutionResult.Outcome != OutcomeOK {
					output = "Error: " + output
				}
				if output != "" {
					contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
						Type: schemas.ChatContentBlockTypeText,
						Text: &output,
					})
				}
			}

			// Handle executable code
			if part.ExecutableCode != nil {
				codeContent := "```" + part.ExecutableCode.Language + "\n" + part.ExecutableCode.Code + "\n```"
				contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
					Type: schemas.ChatContentBlockTypeText,
					Text: &codeContent,
				})
			}

			// Handle inline data (images, audio generated by the model). Gemini's
			// image-generation models return the picture as a Part carrying only
			// InlineData; the Responses and dedicated image converters already
			// preserve this field, so Chat Completions must too.
			if part.InlineData != nil && part.InlineData.Data != "" {
				if block := convertGeminiInlineDataToChatContentBlock(part.InlineData); block != nil {
					contentBlocks = append(contentBlocks, *block)
				}
			}

			// Handle standalone thought signature (not associated with function call or text)
			if len(part.ThoughtSignature) > 0 && part.FunctionCall == nil && part.Text == "" {
				thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
				reasoningDetails = append(reasoningDetails, schemas.ChatReasoningDetails{
					Index:     len(reasoningDetails),
					Type:      schemas.BifrostReasoningDetailsTypeEncrypted,
					Signature: &thoughtSig,
				})
			}
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

	// A candidate with no visible content and no tool call still needs a content field
	// a client can read: ChatMessageContent marshals to JSON null when both halves are
	// nil, which is the same blind-dereference hazard as a null Choices array. OpenAI
	// answers a truncated generation with an empty string, so match that. Tool-call turns
	// keep a nil content, which is what OpenAI sends for them.
	if contentStr == nil && len(contentBlocks) == 0 && len(toolCalls) == 0 {
		contentStr = new("")
	}

	message.Content = &schemas.ChatMessageContent{
		ContentStr:    contentStr,
		ContentBlocks: contentBlocks,
	}

	// Map Google Search grounding supports to OpenAI url_citation annotations
	annotations := convertGroundingMetadataToChatAnnotations(candidate.GroundingMetadata)

	if len(toolCalls) > 0 || len(reasoningDetails) > 0 || len(annotations) > 0 {
		message.ChatAssistantMessage = &schemas.ChatAssistantMessage{
			ToolCalls:        toolCalls,
			ReasoningDetails: reasoningDetails,
			Annotations:      annotations,
		}
	}

	// Convert finish reason to Bifrost format.
	// Gemini uses "STOP" for both normal text completions and tool call responses —
	// it has no dedicated finish reason for tool calls. Override to "tool_calls" when
	// tool calls are present so downstream consumers see a uniform signal.
	finishReason := ConvertGeminiFinishReasonToBifrost(candidate.FinishReason)
	if len(toolCalls) > 0 && finishReason == "stop" {
		finishReason = "tool_calls"
	}

	bifrostResp.Choices = append(bifrostResp.Choices, schemas.BifrostResponseChoice{
		Index:        0,
		FinishReason: &finishReason,
		LogProbs:     ConvertGeminiLogprobsResultToBifrost(candidate.LogprobsResult),
		ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
			Message: message,
		},
	})

	// Set usage information
	bifrostResp.Usage = ConvertGeminiUsageMetadataToChatUsage(response.UsageMetadata)
	applyGeminiSearchQueryChatUsage(bifrostResp.Usage, candidate.GroundingMetadata, response.ModelVersion)

	if response.UsageMetadata != nil {
		if t := mapGeminiTrafficTypeToBifrost(response.UsageMetadata.TrafficType); t != nil {
			bifrostResp.ServiceTier = t
		} else if response.UsageMetadata.ServiceTier != "" {
			tier := mapGeminiServiceTierToBifrost(response.UsageMetadata.ServiceTier)
			bifrostResp.ServiceTier = &tier
		}
	}

	return bifrostResp
}

// GeminiStreamState tracks tool-call index across streaming chunks.
type GeminiStreamState struct {
	nextToolCallIndex int
	hadToolCalls      bool // true if any tool calls were seen in this stream
}

// NewGeminiStreamState returns initialised stream state for one streaming response.
func NewGeminiStreamState() *GeminiStreamState {
	return &GeminiStreamState{}
}

// ToBifrostChatCompletionStream converts a Gemini streaming response to Bifrost Chat
// Completion stream chunks. A Gemini chunk usually maps to one delta, but a chunk
// whose parts mix text with inline media (a generated image or audio blob) is split
// so that each inline-media part becomes its own delta, in the original part order,
// and consecutive non-media parts share one. The streaming delta has no content-block
// array, so splitting is the only way to keep the boundary between text and media on
// the wire; ToBifrostResponsesStream emits several events per chunk for the same
// reason on the Responses path.
//
// Returns the deltas in order, the error (if any), and whether this Gemini chunk was
// the last one. Stream-level state (tool call indices, finish reason tracking) is
// carried across chunks in state.
func (response *GenerateContentResponse) ToBifrostChatCompletionStream(state *GeminiStreamState) ([]*schemas.BifrostChatResponse, *schemas.BifrostError, bool) {
	if response == nil {
		return nil, nil, false
	}
	if state == nil {
		state = NewGeminiStreamState()
	}

	var chunks []*schemas.BifrostChatResponse
	isLastChunk := false
	for _, piece := range response.splitInlineMediaParts() {
		chunk, bifrostErr, isLast := piece.toBifrostChatCompletionStreamDelta(state)
		if bifrostErr != nil {
			return nil, bifrostErr, isLast
		}
		if chunk != nil {
			chunks = append(chunks, chunk)
		}
		isLastChunk = isLastChunk || isLast
	}
	return chunks, nil, isLastChunk
}

// splitInlineMediaParts partitions the first candidate's parts into homogeneous
// groups for streaming: each inline-media part (image or audio InlineData) becomes
// its own group and consecutive non-media parts form one group, preserving order.
// Candidate metadata that closes the stream (finishReason, usageMetadata, grounding,
// logprobs, safety ratings) is attached only to the final group so the finish reason
// and usage are emitted exactly once. A response that needs no split is returned
// unchanged as the single element. A candidate whose finish reason is an error
// (safety, image safety, recitation, and so on) is never split: the per-delta
// converter turns it into a single error response and suppresses every part, and
// splitting it would leak the pre-final text or media before that error.
func (response *GenerateContentResponse) splitInlineMediaParts() []*GenerateContentResponse {
	if len(response.Candidates) == 0 || response.Candidates[0] == nil ||
		response.Candidates[0].Content == nil || len(response.Candidates[0].Content.Parts) < 2 ||
		isErrorFinishReason(response.Candidates[0].FinishReason) {
		return []*GenerateContentResponse{response}
	}
	candidate := response.Candidates[0]

	var groups [][]*Part
	var run []*Part
	for _, part := range candidate.Content.Parts {
		if isInlineMediaPart(part) {
			if len(run) > 0 {
				groups = append(groups, run)
				run = nil
			}
			groups = append(groups, []*Part{part})
			continue
		}
		run = append(run, part)
	}
	if len(run) > 0 {
		groups = append(groups, run)
	}
	if len(groups) < 2 {
		return []*GenerateContentResponse{response}
	}

	pieces := make([]*GenerateContentResponse, 0, len(groups))
	for i, group := range groups {
		pieceCandidate := &Candidate{
			Index:   candidate.Index,
			Content: &Content{Role: candidate.Content.Role, Parts: group},
		}
		piece := &GenerateContentResponse{
			ResponseID:     response.ResponseID,
			ModelVersion:   response.ModelVersion,
			CreateTime:     response.CreateTime,
			PromptFeedback: response.PromptFeedback,
			Candidates:     []*Candidate{pieceCandidate},
		}
		if i == len(groups)-1 {
			pieceCandidate.FinishReason = candidate.FinishReason
			pieceCandidate.FinishMessage = candidate.FinishMessage
			pieceCandidate.TokenCount = candidate.TokenCount
			pieceCandidate.CitationMetadata = candidate.CitationMetadata
			pieceCandidate.URLContextMetadata = candidate.URLContextMetadata
			pieceCandidate.AvgLogprobs = candidate.AvgLogprobs
			pieceCandidate.GroundingMetadata = candidate.GroundingMetadata
			pieceCandidate.LogprobsResult = candidate.LogprobsResult
			pieceCandidate.SafetyRatings = candidate.SafetyRatings
			piece.UsageMetadata = response.UsageMetadata
		}
		pieces = append(pieces, piece)
	}
	return pieces
}

// isInlineMediaPart reports whether part carries a generated image or audio blob.
func isInlineMediaPart(part *Part) bool {
	if part == nil || part.InlineData == nil || part.InlineData.Data == "" {
		return false
	}
	return strings.HasPrefix(part.InlineData.MIMEType, "image/") ||
		strings.HasPrefix(part.InlineData.MIMEType, "audio/")
}

// toBifrostChatCompletionStreamDelta converts one Gemini chunk into a single Bifrost
// chat completion delta. Chunks must arrive here through ToBifrostChatCompletionStream,
// which splits mixed text and inline-media parts first: a chunk reaching this function
// carries at most one inline-media part and, when it does, no text beside it, so inline
// media never shares Content with text.
// Returns the delta (nil when the chunk carries nothing worth emitting), the error (if
// any), and whether this is the last chunk.
func (response *GenerateContentResponse) toBifrostChatCompletionStreamDelta(state *GeminiStreamState) (*schemas.BifrostChatResponse, *schemas.BifrostError, bool) {
	if response == nil {
		return nil, nil, false
	}

	if state == nil {
		state = NewGeminiStreamState()
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
		var imageDataURL string
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
				if len(part.FunctionCall.Args) > 0 {
					jsonArgs = string(part.FunctionCall.Args)
				}

				// Use ID if available, otherwise use function name
				callID := part.FunctionCall.Name
				if part.FunctionCall.ID != "" {
					callID = part.FunctionCall.ID
				}

				// Embed thought signature into CallID if present
				if len(part.ThoughtSignature) > 0 && !strings.Contains(callID, thoughtSignatureSeparator) {
					encoded := base64.RawURLEncoding.EncodeToString(part.ThoughtSignature)
					callID = fmt.Sprintf("%s%s%s", callID, thoughtSignatureSeparator, encoded)
				}

				toolCallIdx := state.nextToolCallIndex
				state.nextToolCallIndex++

				toolCall := schemas.ChatAssistantMessageToolCall{
					Index: uint16(toolCallIdx),
					Type:  schemas.Ptr(string(schemas.ChatToolTypeFunction)),
					ID:    &callID,
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      &part.FunctionCall.Name,
						Arguments: jsonArgs,
					},
				}

				toolCalls = append(toolCalls, toolCall)

				// Also add thought signature to reasoning details if present
				if len(part.ThoughtSignature) > 0 {
					thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
					// Extract base ID without signature for reasoning detail lookup
					baseCallID := callID
					if strings.Contains(callID, thoughtSignatureSeparator) {
						parts := strings.SplitN(callID, thoughtSignatureSeparator, 2)
						if len(parts) == 2 {
							baseCallID = parts[0]
						}
					}
					reasoningDetails = append(reasoningDetails, schemas.ChatReasoningDetails{
						Index:     len(reasoningDetails),
						Type:      schemas.BifrostReasoningDetailsTypeEncrypted,
						Signature: &thoughtSig,
						ID:        schemas.Ptr(fmt.Sprintf("tool_call_%s", baseCallID)),
					})
				}

			case part.FunctionResponse != nil:
				// Extract the output from the response and add to text content
				output := extractFunctionResponseOutput(part.FunctionResponse)
				if output != "" {
					textContent += output
				}
			case part.CodeExecutionResult != nil:
				output := part.CodeExecutionResult.Output
				if part.CodeExecutionResult.Outcome != OutcomeOK {
					output = "Error: " + output
				}
				if output != "" {
					textContent += output
				}
			case part.ExecutableCode != nil:
				codeContent := "```" + part.ExecutableCode.Language + "\n" + part.ExecutableCode.Code + "\n```"
				textContent += codeContent

			case part.InlineData != nil && part.InlineData.Data != "" && strings.HasPrefix(part.InlineData.MIMEType, "audio/"):
				// Audio has a dedicated streaming delta field (matches OpenAI's own
				// audio-output stream chunk shape), so it does not need to share Content.
				delta.Audio = &schemas.ChatAudioMessageAudio{
					Data: part.InlineData.Data,
				}

			case part.InlineData != nil && part.InlineData.Data != "" && strings.HasPrefix(part.InlineData.MIMEType, "image/"):
				// The streaming delta has no content-block array, only a string Content
				// field, so the image is carried the same way the non-stream path's
				// image_url block carries it: as a data URL. It is held apart from
				// textContent rather than appended to it; ToBifrostChatCompletionStream
				// has already split the chunk so no text part travels with this one.
				imageDataURL = "data:" + part.InlineData.MIMEType + ";base64," + part.InlineData.Data
			}

			// Handle thought signature separately (not part of the switch since it can co-exist with other types)
			if len(part.ThoughtSignature) > 0 && part.FunctionCall == nil {
				thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
				reasoningDetails = append(reasoningDetails, schemas.ChatReasoningDetails{
					Index:     len(reasoningDetails),
					Type:      schemas.BifrostReasoningDetailsTypeEncrypted,
					Signature: &thoughtSig,
				})
			}
		}

		// Set content if present. A split chunk carries either text or an image, never
		// both, so the image only fills Content when there is no text to displace.
		if textContent != "" {
			delta.Content = &textContent
		} else if imageDataURL != "" {
			delta.Content = &imageDataURL
		}

		// Set reasoning details if present
		if len(reasoningDetails) > 0 {
			delta.ReasoningDetails = reasoningDetails
		}

		// Set tool calls if present
		if len(toolCalls) > 0 {
			delta.ToolCalls = toolCalls
			state.hadToolCalls = true
		}
	}

	// Map Google Search grounding supports to OpenAI url_citation annotations.
	// Gemini sends complete groundingMetadata on the finish-reason chunk; read it only
	// there (matching the Responses path) so re-sends can't drop or duplicate citations.
	if candidate.FinishReason != "" {
		delta.Annotations = convertGroundingMetadataToChatAnnotations(candidate.GroundingMetadata)
	}

	// Check if delta has any content - if not and it's not the last chunk, skip it
	hasDeltaContent := delta.Role != nil || delta.Content != nil || delta.Audio != nil || len(delta.ToolCalls) > 0 || len(delta.ReasoningDetails) > 0 || len(delta.Annotations) > 0
	if !hasDeltaContent && !isLastChunk {
		return nil, nil, false
	}

	// Build the choice
	var finishReason *string
	if isLastChunk && candidate.FinishReason != "" {
		reason := ConvertGeminiFinishReasonToBifrost(candidate.FinishReason)
		// Gemini uses "STOP" for both text completions and tool call responses.
		// Override to "tool_calls" when tool calls were seen in this stream for uniformity.
		if (len(delta.ToolCalls) > 0 || state.hadToolCalls) && reason == "stop" {
			reason = "tool_calls"
		}
		finishReason = &reason
	}

	choice := schemas.BifrostResponseChoice{
		Index:        int(candidate.Index),
		FinishReason: finishReason,
		LogProbs:     ConvertGeminiLogprobsResultToBifrost(candidate.LogprobsResult),
		ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
			Delta: delta,
		},
	}

	streamResponse.Choices = []schemas.BifrostResponseChoice{choice}

	// Add usage information if this is the last chunk
	if isLastChunk && response.UsageMetadata != nil {
		streamResponse.Usage = ConvertGeminiUsageMetadataToChatUsage(response.UsageMetadata)
		applyGeminiSearchQueryChatUsage(streamResponse.Usage, candidate.GroundingMetadata, response.ModelVersion)
		if t := mapGeminiTrafficTypeToBifrost(response.UsageMetadata.TrafficType); t != nil {
			streamResponse.ServiceTier = t
		} else if response.UsageMetadata.ServiceTier != "" {
			tier := mapGeminiServiceTierToBifrost(response.UsageMetadata.ServiceTier)
			streamResponse.ServiceTier = &tier
		}
	}

	return streamResponse, nil, isLastChunk
}

// convertGroundingMetadataToChatAnnotations converts Gemini grounding supports to OpenAI
// url_citation annotations, one per (support, chunk index) pair so multi-source segments
// are preserved. Indices are Gemini's byte offsets into the response text, passed through as-is.
func convertGroundingMetadataToChatAnnotations(metadata *GroundingMetadata) []schemas.ChatAssistantMessageAnnotation {
	if metadata == nil {
		return nil
	}
	var annotations []schemas.ChatAssistantMessageAnnotation
	for _, support := range metadata.GroundingSupports {
		if support.Segment == nil {
			continue
		}
		for _, chunkIdx := range support.GroundingChunkIndices {
			if chunkIdx < 0 || int(chunkIdx) >= len(metadata.GroundingChunks) {
				continue
			}
			chunk := metadata.GroundingChunks[chunkIdx]
			if chunk.Web == nil || chunk.Web.URI == "" {
				continue
			}
			annotation := schemas.ChatAssistantMessageAnnotation{
				Type: "url_citation",
				URLCitation: schemas.ChatAssistantMessageAnnotationCitation{
					StartIndex: int(support.Segment.StartIndex),
					EndIndex:   int(support.Segment.EndIndex),
					Title:      chunk.Web.Title,
					URL:        schemas.Ptr(chunk.Web.URI),
				},
			}
			if support.Segment.Text != "" {
				annotation.URLCitation.Text = schemas.Ptr(support.Segment.Text)
			}
			annotations = append(annotations, annotation)
		}
	}
	return annotations
}

// convertGeminiInlineDataToChatContentBlock converts a Gemini inline data blob
// (an image or audio part generated by the model) into a Chat Completions
// content block, mirroring convertGeminiInlineDataToContentBlock in responses.go.
func convertGeminiInlineDataToChatContentBlock(blob *Blob) *schemas.ChatContentBlock {
	switch {
	case strings.HasPrefix(blob.MIMEType, "image/"):
		return &schemas.ChatContentBlock{
			Type: schemas.ChatContentBlockTypeImage,
			ImageURLStruct: &schemas.ChatInputImage{
				URL: "data:" + blob.MIMEType + ";base64," + blob.Data,
			},
		}
	case strings.HasPrefix(blob.MIMEType, "audio/"):
		format := strings.TrimPrefix(blob.MIMEType, "audio/")
		return &schemas.ChatContentBlock{
			Type: schemas.ChatContentBlockTypeInputAudio,
			InputAudio: &schemas.ChatInputAudio{
				Data:   blob.Data,
				Format: schemas.Ptr(format),
			},
		}
	default:
		return nil
	}
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
		reason == FinishReasonUnexpectedToolCall ||
		reason == FinishReasonMissingThoughtSignature ||
		reason == FinishReasonMalformedResponse ||
		reason == FinishReasonImageProhibitedContent ||
		reason == FinishReasonImageRecitation ||
		reason == FinishReasonTooManyToolCalls ||
		reason == FinishReasonNoImage
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
		Usage:   ConvertGeminiUsageMetadataToChatUsage(response.UsageMetadata),
	}

	if !response.CreateTime.IsZero() {
		errorResp.Created = int(response.CreateTime.Unix())
	}

	return errorResp
}

// isChatAssistantPrefillMessage reports whether msg is a plain assistant text turn, the Chat
// Completions analogue of isAssistantPrefillMessage. A message carrying tool calls, reasoning, or
// audio is not a prefill: it is history the model must see replayed, so it is never trimmed.
func isChatAssistantPrefillMessage(msg *schemas.ChatMessage) bool {
	if msg.Role != schemas.ChatMessageRoleAssistant {
		return false
	}
	if a := msg.ChatAssistantMessage; a != nil {
		if len(a.ToolCalls) > 0 || a.Audio != nil || a.Reasoning != nil || len(a.ReasoningDetails) > 0 {
			return false
		}
	}
	return true
}

// trimTrailingChatAssistantPrefill drops the trailing run of assistant prefill turns so the
// conversation ends on a user or tool turn. See trimTrailingAssistantPrefill for the rationale
// and for the datasheet opt-out.
func trimTrailingChatAssistantPrefill(messages []schemas.ChatMessage, caps schemas.ModelCaps) []schemas.ChatMessage {
	if caps.SupportsAssistantPrefill(false) {
		return messages
	}
	trimmed := len(messages)
	for trimmed > 0 && isChatAssistantPrefillMessage(&messages[trimmed-1]) {
		trimmed--
	}
	return messages[:trimmed]
}
