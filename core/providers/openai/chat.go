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
	}

	// Some OpenAI-compatible providers (for example Fireworks) require reasoning_content
	// and reject reasoning in assistant history messages.
	openaiReq.applyReasoningMessageCompatibility(bifrostReq.Model)

	switch bifrostReq.Provider {
	case schemas.OpenAI, schemas.Azure:
		return openaiReq
	case schemas.XAI:
		openaiReq.filterOpenAISpecificParameters()
		openaiReq.applyXAICompatibility(bifrostReq.Model)
		return openaiReq
	case schemas.Gemini:
		openaiReq.filterOpenAISpecificParameters()
		// Removing extra parameters that are not supported by Gemini
		openaiReq.ServiceTier = nil
		return openaiReq
	case schemas.Mistral:
		openaiReq.filterOpenAISpecificParameters()
		openaiReq.applyMistralCompatibility()
		return openaiReq
	case schemas.Vertex:
		openaiReq.filterOpenAISpecificParameters()

		// Apply Mistral-specific transformations for Vertex Mistral models
		if schemas.IsMistralModel(bifrostReq.Model) {
			openaiReq.applyMistralCompatibility()
		}
		return openaiReq
	default:
		// Check if provider is a custom provider
		if isCustomProvider, ok := ctx.Value(schemas.BifrostContextKeyIsCustomProvider).(bool); ok && isCustomProvider {
			return openaiReq
		}
		openaiReq.filterOpenAISpecificParameters()
		return openaiReq
	}
}

// applyReasoningMessageCompatibility applies model-specific assistant message compatibility rules.
func (req *OpenAIChatRequest) applyReasoningMessageCompatibility(model string) {
	if req == nil || len(req.Messages) == 0 || !isFireworksModel(model) {
		return
	}

	for i := range req.Messages {
		assistant := req.Messages[i].OpenAIChatAssistantMessage
		if assistant == nil {
			continue
		}
		if assistant.ReasoningContent == nil && assistant.Reasoning != nil {
			assistant.ReasoningContent = assistant.Reasoning
		}
		if assistant.ReasoningContent != nil {
			assistant.Reasoning = nil
		}
	}
}

func isFireworksModel(model string) bool {
	model = strings.ToLower(model)
	return strings.Contains(model, "accounts/fireworks/models/") || strings.Contains(model, "/fireworks/")
}

// Filter OpenAI Specific Parameters
func (req *OpenAIChatRequest) filterOpenAISpecificParameters() {
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
			req.ChatParameters.Reasoning.MaxTokens = nil
		}
	}

	if req.ChatParameters.Prediction != nil {
		req.ChatParameters.Prediction = nil
	}
	if req.ChatParameters.PromptCacheKey != nil {
		req.ChatParameters.PromptCacheKey = nil
	}
	if req.ChatParameters.PromptCacheRetention != nil {
		req.ChatParameters.PromptCacheRetention = nil
	}
	if req.ChatParameters.Verbosity != nil {
		req.ChatParameters.Verbosity = nil
	}
	if req.ChatParameters.Store != nil {
		req.ChatParameters.Store = nil
	}
	if req.ChatParameters.WebSearchOptions != nil {
		req.ChatParameters.WebSearchOptions = nil
	}
}

// applyMistralCompatibility applies Mistral-specific transformations to the request
func (req *OpenAIChatRequest) applyMistralCompatibility() {
	// Mistral uses max_tokens instead of max_completion_tokens
	if req.MaxCompletionTokens != nil {
		req.MaxTokens = req.MaxCompletionTokens
		req.MaxCompletionTokens = nil
	}

	// Mistral does not support ToolChoiceStruct, only simple tool choice strings are supported
	if req.ToolChoice != nil && req.ToolChoice.ChatToolChoiceStruct != nil {
		req.ToolChoice.ChatToolChoiceStr = schemas.Ptr("any")
		req.ToolChoice.ChatToolChoiceStruct = nil
	}
}

// applyXAICompatibility applies xAI-specific transformations to the request
func (req *OpenAIChatRequest) applyXAICompatibility(model string) {
	// Only apply filters if this is a grok reasoning model
	if !schemas.IsGrokReasoningModel(model) {
		return
	}

	req.ChatParameters.PresencePenalty = nil

	// Only non-mini grok-3 models support frequency_penalty and stop
	// grok-3-mini only supports reasoning_effort in reasoning mode
	if !strings.Contains(model, "grok-3") || strings.Contains(model, "grok-3-mini") {
		req.ChatParameters.FrequencyPenalty = nil
		req.ChatParameters.Stop = nil
	}

	// Only grok-3-mini supports reasoning_effort
	if req.ChatParameters.Reasoning != nil &&
		!strings.Contains(model, "grok-3-mini") {
		// Clear reasoning_effort for non-grok-3-mini models
		req.ChatParameters.Reasoning.Effort = nil
	}
}
