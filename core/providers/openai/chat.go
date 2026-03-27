package openai

import (
	"strings"

	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostChatRequest converts an OpenAI chat request to Bifrost format
func (req *OpenAIChatRequest) ToBifrostChatRequest(ctx *schemas.BifrostContext) *schemas.BifrostChatRequest {
	provider, model := schemas.ParseModelString(req.Model, utils.CheckAndSetDefaultProvider(ctx, schemas.OpenAI))

	return &schemas.BifrostChatRequest{
		Provider:  provider,
		Model:     model,
		Input:     ConvertOpenAIMessagesToBifrostMessages(req.Messages),
		Params:    &req.ChatParameters,
		Fallbacks: schemas.ParseFallbacks(req.Fallbacks),
	}
}

// ToOpenAIChatRequest converts a Bifrost chat completion request to OpenAI format
func ToOpenAIChatRequest(ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostChatRequest) *OpenAIChatRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	openaiReq := &OpenAIChatRequest{
		Model:    bifrostReq.Model,
		Messages: ConvertBifrostMessagesToOpenAIMessages(bifrostReq.Input),
	}

	if bifrostReq.Params != nil {
		openaiReq.ChatParameters = *bifrostReq.Params
		if openaiReq.ChatParameters.MaxCompletionTokens != nil && *openaiReq.ChatParameters.MaxCompletionTokens < MinMaxCompletionTokens {
			openaiReq.ChatParameters.MaxCompletionTokens = schemas.Ptr(MinMaxCompletionTokens)
		}
		// Drop user field if it exceeds OpenAI's 64 character limit
		openaiReq.ChatParameters.User = SanitizeUserField(openaiReq.ChatParameters.User)
		openaiReq.ExtraParams = bifrostReq.Params.ExtraParams

		// Normalize tool parameters for deterministic JSON serialization (improves prompt caching)
		if len(openaiReq.ChatParameters.Tools) > 0 {
			normalizedTools := make([]schemas.ChatTool, len(openaiReq.ChatParameters.Tools))
			for i, tool := range openaiReq.ChatParameters.Tools {
				normalizedTools[i] = tool
				if tool.Function != nil && tool.Function.Parameters != nil {
					funcCopy := *tool.Function
					funcCopy.Parameters = tool.Function.Parameters.Normalized()
					normalizedTools[i].Function = &funcCopy
				}
			}
			openaiReq.ChatParameters.Tools = normalizedTools
		}
	}
	switch bifrostReq.Provider {
	case schemas.OpenAI, schemas.Azure:
		return openaiReq
	case schemas.XAI:
		openaiReq.filterOpenAISpecificParameters(ctx)
		openaiReq.applyXAICompatibility(ctx, bifrostReq.Model)
		return openaiReq
	case schemas.Gemini:
		openaiReq.filterOpenAISpecificParameters(ctx)
		// Removing extra parameters that are not supported by Gemini
		if openaiReq.ServiceTier != nil {
			schemas.AppendToContextList(ctx, schemas.BifrostContextKeyDroppedParams, "service_tier")
		}
		openaiReq.ServiceTier = nil
		return openaiReq
	case schemas.Mistral:
		openaiReq.filterOpenAISpecificParameters(ctx)
		openaiReq.applyMistralCompatibility(ctx)
		return openaiReq
	case schemas.Vertex:
		openaiReq.filterOpenAISpecificParameters(ctx)

		// Apply Mistral-specific transformations for Vertex Mistral models
		if schemas.IsMistralModel(bifrostReq.Model) {
			openaiReq.applyMistralCompatibility(ctx)
		}
		return openaiReq
	default:
		// Check if provider is a custom provider
		if isCustomProvider, ok := ctx.Value(schemas.BifrostContextKeyIsCustomProvider).(bool); ok && isCustomProvider {
			return openaiReq
		}
		openaiReq.filterOpenAISpecificParameters(ctx)
		return openaiReq
	}
}

// Filter OpenAI Specific Parameters
func (req *OpenAIChatRequest) filterOpenAISpecificParameters(ctx *schemas.BifrostContext) {
	// Handle reasoning parameter: OpenAI uses effort-based reasoning
	// Priority: effort (native) > max_tokens (estimated)
	if req.ChatParameters.Reasoning != nil {
		if req.ChatParameters.Reasoning.Effort != nil {
			// Native field is provided, use it (and clear max_tokens)
			effort := *req.ChatParameters.Reasoning.Effort
			// Convert "minimal" to "low" for non-OpenAI providers
			if effort == "minimal" {
				req.ChatParameters.Reasoning.Effort = schemas.Ptr("low")
			}
			// Clear max_tokens since OpenAI doesn't use it
			if req.ChatParameters.Reasoning.MaxTokens != nil {
				schemas.AppendToContextList(ctx, schemas.BifrostContextKeyDroppedParams, "reasoning.max_tokens")
			}
			req.ChatParameters.Reasoning.MaxTokens = nil
		} else if req.ChatParameters.Reasoning.MaxTokens != nil {
			// Estimate effort from max_tokens
			maxTokens := *req.ChatParameters.Reasoning.MaxTokens
			maxCompletionTokens := DefaultCompletionMaxTokens
			if req.ChatParameters.MaxCompletionTokens != nil {
				maxCompletionTokens = *req.ChatParameters.MaxCompletionTokens
			}
			effort := utils.GetReasoningEffortFromBudgetTokens(maxTokens, MinReasoningMaxTokens, maxCompletionTokens)
			req.ChatParameters.Reasoning.Effort = schemas.Ptr(effort)
			// Clear max_tokens since OpenAI doesn't use it
			schemas.AppendToContextList(ctx, schemas.BifrostContextKeyDroppedParams, "reasoning.max_tokens")
			req.ChatParameters.Reasoning.MaxTokens = nil
		}
	}

	if req.ChatParameters.Prediction != nil {
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyDroppedParams, "prediction")
		req.ChatParameters.Prediction = nil
	}
	if req.ChatParameters.PromptCacheKey != nil {
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyDroppedParams, "prompt_cache_key")
		req.ChatParameters.PromptCacheKey = nil
	}
	if req.ChatParameters.PromptCacheRetention != nil {
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyDroppedParams, "prompt_cache_retention")
		req.ChatParameters.PromptCacheRetention = nil
	}
	if req.ChatParameters.Verbosity != nil {
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyDroppedParams, "verbosity")
		req.ChatParameters.Verbosity = nil
	}
	if req.ChatParameters.Store != nil {
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyDroppedParams, "store")
		req.ChatParameters.Store = nil
	}
	if req.ChatParameters.WebSearchOptions != nil {
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyDroppedParams, "web_search_options")
		req.ChatParameters.WebSearchOptions = nil
	}
}

// applyMistralCompatibility applies Mistral-specific transformations to the request
func (req *OpenAIChatRequest) applyMistralCompatibility(ctx *schemas.BifrostContext) {
	// Mistral uses max_tokens instead of max_completion_tokens
	if req.MaxCompletionTokens != nil {
		req.MaxTokens = req.MaxCompletionTokens
		req.MaxCompletionTokens = nil
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyDroppedParams, "max_completion_tokens")
	}

	// Mistral does not support ToolChoiceStruct, only simple tool choice strings are supported
	if req.ToolChoice != nil && req.ToolChoice.ChatToolChoiceStruct != nil {
		req.ToolChoice.ChatToolChoiceStr = schemas.Ptr("any")
		req.ToolChoice.ChatToolChoiceStruct = nil
	}
}

// applyXAICompatibility applies xAI-specific transformations to the request
func (req *OpenAIChatRequest) applyXAICompatibility(ctx *schemas.BifrostContext, model string) {
	// Only apply filters if this is a grok reasoning model
	if !schemas.IsGrokReasoningModel(model) {
		return
	}

	if req.ChatParameters.PresencePenalty != nil {
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyDroppedParams, "presence_penalty")
	}
	req.ChatParameters.PresencePenalty = nil

	// Only non-mini grok-3 models support frequency_penalty and stop
	// grok-3-mini only supports reasoning_effort in reasoning mode
	if !strings.Contains(model, "grok-3") || strings.Contains(model, "grok-3-mini") {
		if req.ChatParameters.FrequencyPenalty != nil {
			schemas.AppendToContextList(ctx, schemas.BifrostContextKeyDroppedParams, "frequency_penalty")
		}
		req.ChatParameters.FrequencyPenalty = nil
		if req.ChatParameters.Stop != nil {
			schemas.AppendToContextList(ctx, schemas.BifrostContextKeyDroppedParams, "stop")
		}
		req.ChatParameters.Stop = nil
	}

	// Only grok-3-mini supports reasoning_effort
	if req.ChatParameters.Reasoning != nil &&
		!strings.Contains(model, "grok-3-mini") {
		if req.ChatParameters.Reasoning.Effort != nil {
			schemas.AppendToContextList(ctx, schemas.BifrostContextKeyDroppedParams, "reasoning.effort")
		}
		// Clear reasoning_effort for non-grok-3-mini models
		req.ChatParameters.Reasoning.Effort = nil
	}
}
