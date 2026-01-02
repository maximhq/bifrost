package openai

import (
	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostChatRequest converts an OpenAI chat request to Bifrost format
func (request *OpenAIChatRequest) ToBifrostChatRequest() *schemas.BifrostChatRequest {
	provider, model := schemas.ParseModelString(request.Model, schemas.OpenAI)

	return &schemas.BifrostChatRequest{
		Provider:  provider,
		Model:     model,
		Input:     ConvertOpenAIMessagesToBifrostMessages(request.Messages),
		Params:    &request.ChatParameters,
		Fallbacks: schemas.ParseFallbacks(request.Fallbacks),
	}
}

// ToOpenAIChatRequest converts a Bifrost chat completion request to OpenAI format
func ToOpenAIChatRequest(bifrostReq *schemas.BifrostChatRequest) *OpenAIChatRequest {
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
	}

	switch bifrostReq.Provider {
	case schemas.OpenAI:
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
		openaiReq.filterOpenAISpecificParameters()
		return openaiReq
	}
}

// Filter OpenAI Specific Parameters
func (request *OpenAIChatRequest) filterOpenAISpecificParameters() {
	// Handle reasoning parameter: OpenAI uses effort-based reasoning
	// Priority: effort (native) > max_tokens (estimated)
	if request.ChatParameters.Reasoning != nil {
		if request.ChatParameters.Reasoning.Effort != nil {
			// Native field is provided, use it (and clear max_tokens)
			effort := *request.ChatParameters.Reasoning.Effort
			// Convert "minimal" to "low" for non-OpenAI providers
			if effort == "minimal" {
				request.ChatParameters.Reasoning.Effort = schemas.Ptr("low")
			}
			// Clear max_tokens since OpenAI doesn't use it
			request.ChatParameters.Reasoning.MaxTokens = nil
		} else if request.ChatParameters.Reasoning.MaxTokens != nil {
			// Estimate effort from max_tokens
			maxTokens := *request.ChatParameters.Reasoning.MaxTokens
			maxCompletionTokens := DefaultCompletionMaxTokens
			if request.ChatParameters.MaxCompletionTokens != nil {
				maxCompletionTokens = *request.ChatParameters.MaxCompletionTokens
			}
			effort := utils.GetReasoningEffortFromBudgetTokens(maxTokens, MinReasoningMaxTokens, maxCompletionTokens)
			request.ChatParameters.Reasoning.Effort = schemas.Ptr(effort)
			// Clear max_tokens since OpenAI doesn't use it
			request.ChatParameters.Reasoning.MaxTokens = nil
		}
	}

	if request.ChatParameters.PromptCacheKey != nil {
		request.ChatParameters.PromptCacheKey = nil
	}
	if request.ChatParameters.Verbosity != nil {
		request.ChatParameters.Verbosity = nil
	}
	if request.ChatParameters.Store != nil {
		request.ChatParameters.Store = nil
	}
}

// applyMistralCompatibility applies Mistral-specific transformations to the request
func (request *OpenAIChatRequest) applyMistralCompatibility() {
	// Mistral uses max_tokens instead of max_completion_tokens
	if request.MaxCompletionTokens != nil {
		request.MaxTokens = request.MaxCompletionTokens
		request.MaxCompletionTokens = nil
	}

	// Mistral does not support ToolChoiceStruct, only simple tool choice strings are supported
	if request.ToolChoice != nil && request.ToolChoice.ChatToolChoiceStruct != nil {
		request.ToolChoice.ChatToolChoiceStr = schemas.Ptr("any")
		request.ToolChoice.ChatToolChoiceStruct = nil
	}
}
