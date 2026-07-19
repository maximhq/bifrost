package openai

import (
	"encoding/json"
	"strings"

	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostResponsesRequest converts an OpenAI responses request to Bifrost format
func (resp *OpenAIResponsesRequest) ToBifrostResponsesRequest(ctx *schemas.BifrostContext) *schemas.BifrostResponsesRequest {
	if resp == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(resp.Model, "")

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
func ToOpenAIResponsesRequest(ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostResponsesRequest) *OpenAIResponsesRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	// Canonical model for capability gating only; wire model is untouched.
	capModel := schemas.ResolveCanonicalModel(ctx, bifrostReq.Model)

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

		// OpenAI's Responses schema requires "detail" on input_image items, and strict
		// downstream validators (e.g. vLLM importing the official OpenAI types) reject
		// requests without it. Blocks converted from non-OpenAI surfaces (Anthropic,
		// Gemini, Cohere, chat bridge) never carry one, so default missing values to "auto".
		message = defaultImageDetail(message)

		// Strip provider reasoning signatures (e.g. Gemini thoughtSignatures smuggled into
		// call_id as "<baseID>_ts_<sig>") from tool call IDs, but only when the id exceeds
		// OpenAI's limit — shorter IDs are left intact so distinct upstream IDs are preserved.
		// Deterministic, so a call and its output still match. Clone first — the
		// ResponsesToolMessage pointer is shared with the caller's input.
		if message.ResponsesToolMessage != nil && message.ResponsesToolMessage.CallID != nil &&
			len(*message.ResponsesToolMessage.CallID) > MaxToolCallIDLength {
			if stripped := utils.StripThoughtSignature(*message.ResponsesToolMessage.CallID); stripped != *message.ResponsesToolMessage.CallID {
				toolMsgCopy := *message.ResponsesToolMessage
				toolMsgCopy.CallID = &stripped
				message.ResponsesToolMessage = &toolMsgCopy
			}
		}

		// OpenAI accepts role only on message input items.
		if (message.Type != nil && *message.Type != schemas.ResponsesMessageTypeMessage) ||
			(message.Type == nil && message.ResponsesReasoning != nil) {
			message.Role = nil
		}

		if message.ResponsesReasoning != nil {
			isGptOss := strings.Contains(capModel, "gpt-oss")
			isReasoning := isOpenAIReasoningModel(capModel)

			// For non-gpt-oss models, skip reasoning-only messages that have content blocks but no summaries.
			// For non-reasoning models (e.g., gpt-4o), also skip when EncryptedContent is present since
			// these models don't produce encrypted reasoning — any encrypted content is cross-provider
			// (e.g., Gemini ThoughtSignatures) and cannot be decrypted by OpenAI.
			if len(message.ResponsesReasoning.Summary) == 0 &&
				message.Content != nil &&
				len(message.Content.ContentBlocks) > 0 &&
				!isGptOss &&
				(message.ResponsesReasoning.EncryptedContent == nil || !isReasoning) {
				continue
			}

			// If the message has summaries but no content blocks and the model is gpt-oss, then convert the summaries to content blocks
			if len(message.ResponsesReasoning.Summary) > 0 && isGptOss &&
				(message.Content == nil || len(message.Content.ContentBlocks) == 0) {
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
				// Clone the embedded pointer to avoid mutating the original input
				reasoningCopy := *message.ResponsesReasoning
				message.ResponsesReasoning = &reasoningCopy
				// Strip cross-provider encrypted content that non-reasoning models cannot decrypt.
				// Reasoning models (o1/o3/o4/GPT-5) may use EncryptedContent for multi-turn state.
				// Compaction items always carry encrypted_content and must never be stripped.
				isCompactionMessage := message.Type != nil && *message.Type == schemas.ResponsesMessageTypeCompaction
				if !isReasoning && !isCompactionMessage {
					message.ResponsesReasoning.EncryptedContent = nil
				}
				// OpenAI types reasoning.content as an array of reasoning_text blocks, so a
				// string value is rejected ("expected an array ... got a string"). Replayed
				// reasoning items can arrive with content as a string (e.g. an empty "" round-tripped
				// through the response path). message is a value copy, so reassign its Content pointer
				// without mutating the caller's input: drop empty strings, promote non-empty ones to a block.
				if message.Content != nil {
					switch {
					case message.Content.ContentStr != nil:
						if text := *message.Content.ContentStr; text == "" {
							message.Content = nil
						} else {
							message.Content = &schemas.ResponsesMessageContent{
								ContentBlocks: []schemas.ResponsesMessageContentBlock{{
									Type: schemas.ResponsesOutputMessageContentTypeReasoning,
									Text: schemas.Ptr(text),
								}},
							}
						}
					case len(message.Content.ContentBlocks) == 0:
						message.Content = nil
					}
				}
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
		Model:    bifrostReq.Model,
		Provider: bifrostReq.Provider,
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
			// Clone the Reasoning pointer to avoid mutating the original params
			reasoningCopy := *req.ResponsesParameters.Reasoning
			req.ResponsesParameters.Reasoning = &reasoningCopy
			if req.ResponsesParameters.Reasoning.Effort != nil {
				// Native field is provided, use it (and clear max_tokens)
				effort := *req.ResponsesParameters.Reasoning.Effort
				req.ResponsesParameters.Reasoning.Effort = schemas.Ptr(normalizeOpenAIReasoningEffort(capModel, effort))
				// Clear max_tokens since OpenAI doesn't use it
				req.ResponsesParameters.Reasoning.MaxTokens = nil
			} else if req.ResponsesParameters.Reasoning.MaxTokens != nil {
				// Estimate effort from max_tokens
				maxTokens := *req.ResponsesParameters.Reasoning.MaxTokens
				maxOutputTokens := utils.GetMaxOutputTokensOrDefault(req.Model, DefaultCompletionMaxTokens)
				if req.ResponsesParameters.MaxOutputTokens != nil {
					maxOutputTokens = *req.ResponsesParameters.MaxOutputTokens
				}
				effort := utils.GetReasoningEffortFromBudgetTokens(maxTokens, MinReasoningMaxTokens, maxOutputTokens)
				req.ResponsesParameters.Reasoning.Effort = schemas.Ptr(effort)
				// Clear max_tokens since OpenAI doesn't use it
				req.ResponsesParameters.Reasoning.MaxTokens = nil
			}

			// summary:"none" is Anthropic-specific (maps to display:"omitted"); strip it for OpenAI.
			if req.ResponsesParameters.Reasoning.Summary != nil && *req.ResponsesParameters.Reasoning.Summary == "none" {
				req.ResponsesParameters.Reasoning.Summary = nil
			}

			// Handle xAI-specific parameter filtering
			// Only grok-3-mini supports reasoning_effort
			if bifrostReq.Provider == schemas.XAI &&
				schemas.IsGrokReasoningModel(capModel) &&
				!strings.Contains(capModel, "grok-3-mini") {
				// Clear reasoning_effort for non-grok-3-mini xAI reasoning models
				req.ResponsesParameters.Reasoning.Effort = nil
			}

			// Handle OpenAI-specific parameter filtering
			// Only o1/o3 series models support reasoning.effort
			// Regular models like gpt-4o, gpt-4, gpt-3.5-turbo don't support it
			if bifrostReq.Provider == schemas.OpenAI && !isOpenAIReasoningModel(capModel) {
				// Clear reasoning for non-reasoning OpenAI models to avoid API errors
				req.ResponsesParameters.Reasoning = nil
			}
		}

		// Strip top_p for OpenAI reasoning models (o1/o3 series) which reject it
		// GPT-5.x accept top_p when reasoning.effort is "none" (defaults to "none" when omitted)
		if isOpenAIReasoningModel(capModel) {
			stripTopP := true
			_, parsedModel := schemas.ParseModelString(capModel, schemas.OpenAI)
			modelLower := strings.ToLower(parsedModel)
			effort := ""
			if req.ResponsesParameters.Reasoning != nil &&
				req.ResponsesParameters.Reasoning.Effort != nil {
				effort = *req.ResponsesParameters.Reasoning.Effort
			}
			// GPT-5.x: reasoning defaults to "none" when omitted, and top_p is allowed in that case
			// Exception: -pro and -codex variants always reason (no "none" mode), so top_p must be stripped
			if strings.HasPrefix(modelLower, "gpt-5.") &&
				(effort == "" || effort == "none") &&
				!strings.Contains(modelLower, "-pro") &&
				!strings.Contains(modelLower, "-codex") {
				stripTopP = false
			}
			if stripTopP {
				req.ResponsesParameters.TopP = nil
			}
		}

		// Normalize function tool parameters for deterministic JSON serialization.
		// We must copy the Tools slice since it shares the backing array with bifrostReq.Params.Tools.
		if len(req.Tools) > 0 {
			normalizedTools := make([]schemas.ResponsesTool, len(req.Tools))
			copy(normalizedTools, req.Tools)
			for i, tool := range normalizedTools {
				if tool.Type == schemas.ResponsesToolTypeFunction &&
					tool.ResponsesToolFunction != nil &&
					tool.ResponsesToolFunction.Parameters != nil {
					funcCopy := *tool.ResponsesToolFunction
					funcCopy.Parameters = tool.ResponsesToolFunction.Parameters.Normalized()
					normalizedTools[i].ResponsesToolFunction = &funcCopy
				}
			}
			req.Tools = normalizedTools
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
// defaultImageDetail fills "auto" into any input_image content block missing the
// detail field. Clones content on write — the Content pointer and the image block
// pointers inside it are shared with the caller's input.
func defaultImageDetail(message schemas.ResponsesMessage) schemas.ResponsesMessage {
	if message.Content == nil || len(message.Content.ContentBlocks) == 0 {
		return message
	}

	needsDetail := func(block schemas.ResponsesMessageContentBlock) bool {
		return block.Type == schemas.ResponsesInputMessageContentBlockTypeImage &&
			block.ResponsesInputMessageContentBlockImage != nil &&
			block.ResponsesInputMessageContentBlockImage.Detail == nil
	}

	fixNeeded := false
	for _, block := range message.Content.ContentBlocks {
		if needsDetail(block) {
			fixNeeded = true
			break
		}
	}
	if !fixNeeded {
		return message
	}

	newBlocks := make([]schemas.ResponsesMessageContentBlock, len(message.Content.ContentBlocks))
	copy(newBlocks, message.Content.ContentBlocks)
	for i, block := range newBlocks {
		if needsDetail(block) {
			imageCopy := *block.ResponsesInputMessageContentBlockImage
			imageCopy.Detail = schemas.Ptr("auto")
			newBlocks[i].ResponsesInputMessageContentBlockImage = &imageCopy
		}
	}

	contentCopy := *message.Content
	contentCopy.ContentBlocks = newBlocks
	message.Content = &contentCopy
	return message
}

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
		schemas.ResponsesToolTypeWebFetch:           true,
		schemas.ResponsesToolTypeMCP:                true,
		schemas.ResponsesToolTypeCodeInterpreter:    true,
		schemas.ResponsesToolTypeImageGeneration:    true,
		schemas.ResponsesToolTypeLocalShell:         true,
		schemas.ResponsesToolTypeCustom:             true,
		schemas.ResponsesToolTypeWebSearchPreview:   true,
		schemas.ResponsesToolTypeMemory:             true,
		schemas.ResponsesToolTypeToolSearch:         true,
		schemas.ResponsesToolTypeNamespace:          true,
	}

	// Allow provider-native tools that are not part of the OpenAI spec
	if resp.Provider == schemas.XAI {
		supportedTypes[schemas.ResponsesToolTypeXSearch] = true
	}

	// Filter tools to only include supported types
	filteredTools := make([]schemas.ResponsesTool, 0, len(resp.Tools))
	for _, tool := range resp.Tools {
		// OpenRouter exposes server-side tools under the "openrouter:" namespace
		// (web_search, web_fetch, datetime, image_generation, apply_patch, subagent, ...).
		// They are native to OpenRouter and must not be stripped by the
		// OpenAI-oriented whitelist. Match the whole namespace so future tools are
		// covered without per-tool additions.
		isOpenRouterServerTool := resp.Provider == schemas.OpenRouter &&
			strings.HasPrefix(string(tool.Type), schemas.ResponsesToolTypeOpenRouterPrefix)
		if supportedTypes[tool.Type] || isOpenRouterServerTool {
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

				if tool.ResponsesToolWebSearch.ExternalWebAccess != nil {
					externalWebAccess := *tool.ResponsesToolWebSearch.ExternalWebAccess
					newWebSearch.ExternalWebAccess = &externalWebAccess
				}
				if len(tool.ResponsesToolWebSearch.SearchContentTypes) > 0 {
					newWebSearch.SearchContentTypes = append([]string(nil), tool.ResponsesToolWebSearch.SearchContentTypes...)
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

	// If every tool was stripped, a leftover tool_choice would cause a 400 from
	// the upstream ("tool_choice must be specified with tools").
	if len(resp.Tools) == 0 {
		resp.ToolChoice = nil
	}
}

// OpenAICompactionRequest is the wire format for POST /v1/responses/compact.
// It is a strict subset of OpenAIResponsesRequest — no tools, no sampling params, no streaming.
type OpenAICompactionRequest struct {
	Model                string                      `json:"model"`
	Input                OpenAIResponsesRequestInput `json:"input,omitempty"`
	Instructions         *string                     `json:"instructions,omitempty"`
	PreviousResponseID   *string                     `json:"previous_response_id,omitempty"`
	PromptCacheKey       *string                     `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention *string                     `json:"prompt_cache_retention,omitempty"`
	PromptCacheOptions   *schemas.PromptCacheOptions `json:"prompt_cache_options,omitempty"`
	ServiceTier          *schemas.BifrostServiceTier `json:"service_tier,omitempty"`
	ExtraParams          map[string]interface{}      `json:"-"`
}

// GetExtraParams implements RequestBodyWithExtraParams.
func (r *OpenAICompactionRequest) GetExtraParams() map[string]interface{} { return r.ExtraParams }

// MarshalJSON serializes the compaction request. The embedded Input is shadowed
// by a json.RawMessage so the OpenAIResponsesRequestInput union is written via
// its own MarshalJSON (a pointer-receiver method that default struct encoding of
// the value field would skip, emitting `input` as an object the endpoint
// rejects). Empty input is omitted, since a previous_response_id-only compaction
// is valid. Mirrors OpenAIResponsesRequest.MarshalJSON.
func (r *OpenAICompactionRequest) MarshalJSON() ([]byte, error) {
	type Alias OpenAICompactionRequest
	var input json.RawMessage
	if r.Input.OpenAIResponsesRequestInputStr != nil || r.Input.OpenAIResponsesRequestInputArray != nil {
		b, err := r.Input.MarshalJSON()
		if err != nil {
			return nil, err
		}
		input = json.RawMessage(b)
	}
	return utils.MarshalSorted(struct {
		*Alias
		Input json.RawMessage `json:"input,omitempty"`
	}{Alias: (*Alias)(r), Input: input})
}

// ToOpenAICompactionRequest converts a BifrostCompactionRequest to the OpenAI wire format.
func ToOpenAICompactionRequest(ctx *schemas.BifrostContext, req *schemas.BifrostCompactionRequest) *OpenAICompactionRequest {
	if req == nil {
		return nil
	}
	r := &OpenAICompactionRequest{
		Model:                req.Model,
		Instructions:         req.Instructions,
		PreviousResponseID:   req.PreviousResponseID,
		PromptCacheKey:       req.PromptCacheKey,
		PromptCacheRetention: req.PromptCacheRetention,
		PromptCacheOptions:   req.PromptCacheOptions,
		ServiceTier:          req.ServiceTier,
		ExtraParams:          req.ExtraParams,
	}
	if len(req.Input) > 0 {
		// Run through the same normalization as ToOpenAIResponsesRequest so reasoning
		// role cleanup, compaction-content conversion, etc. are applied consistently.
		normalized := ToOpenAIResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
			Provider: req.Provider,
			Model:    req.Model,
			Input:    req.Input,
		})
		if normalized != nil {
			r.Input = normalized.Input
		}
	}
	return r
}

// ToBifrostCompactionRequest converts an OpenAICompactionRequest to Bifrost format.
func (r *OpenAICompactionRequest) ToBifrostCompactionRequest(ctx *schemas.BifrostContext) *schemas.BifrostCompactionRequest {
	if r == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(r.Model, "")
	input := r.Input.OpenAIResponsesRequestInputArray
	if len(input) == 0 && r.Input.OpenAIResponsesRequestInputStr != nil {
		input = []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: r.Input.OpenAIResponsesRequestInputStr},
			},
		}
	}
	return &schemas.BifrostCompactionRequest{
		Provider:             provider,
		Model:                model,
		Input:                input,
		Instructions:         r.Instructions,
		PreviousResponseID:   r.PreviousResponseID,
		PromptCacheKey:       r.PromptCacheKey,
		PromptCacheRetention: r.PromptCacheRetention,
		PromptCacheOptions:   r.PromptCacheOptions,
		ServiceTier:          r.ServiceTier,
		ExtraParams:          r.ExtraParams,
	}
}
